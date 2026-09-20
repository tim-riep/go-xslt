package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// This file implements the XSLT 3.0 instruction xsl:result-document.
//
// xsl:result-document directs the result of evaluating its sequence-constructor
// body to a secondary output destination identified by @href, serialized using
// its serialization attributes (@method, @format, @use-character-maps, ...).
// The content is NOT written to the principal output; instead it is recorded on
// eng.secondary as a SecondaryDoc, which the host surfaces (and may persist
// relative to eng.baseDir). An absent/empty @href redirects to the PRINCIPAL
// output, carrying its serialization attributes along (eng.rdOutput).
//
// All private helpers/types in this file are prefixed with "rd".

func init() {
	instrRegistry["result-document"] = rdCompile
}

// rdExtraAttrNames are the xsl:output-style serialization attributes this
// instruction also accepts, beyond @method/@format/@use-character-maps/
// @encoding/@escape-uri-attributes/@include-content-type (which get their own
// dedicated fields below — they were already AVT/literal-aware before this
// list existed). Unlike those, every one of these is genuinely used as an
// AVT somewhere in the W3C suite (result-document-0401/0501/0601/0702/0703),
// so each is compiled as one and evaluated fresh per execution, then
// dispatched through applyOutputAttr — the same per-attribute logic
// xsl:output itself uses.
var rdExtraAttrNames = []string{
	"indent", "omit-xml-declaration", "standalone", "doctype-public", "doctype-system",
	"cdata-section-elements", "suppress-indentation", "html-version", "byte-order-mark",
	"version", "normalization-form", "undeclare-prefixes", "media-type",
	"item-separator",
	// The json output method's own parameters, plus the XSLT-level
	// @build-tree — all three genuinely AVT-valued in the suite
	// (result-document-1402's json-node-output-method="ht{$m}l",
	// -1404's allow-duplicate-names="{$dupes}", -1405's build-tree="n{$o}").
	"json-node-output-method", "allow-duplicate-names", "build-tree",
	// XSLT 3.0 spells the "version" serialization parameter output-version on
	// xsl:result-document (plain @version there would mean the XSLT version);
	// it is dispatched below under the serialization-parameter name.
	"output-version",
}

// rdBoolAttrs are the xs:boolean-valued serialization parameters that may be
// written on xsl:result-document; an effective value outside the XSLT boolean
// lexical space is a serialization error (SEPM0016).
var rdBoolAttrs = map[string]bool{
	"indent": true, "omit-xml-declaration": true, "byte-order-mark": true,
	"undeclare-prefixes": true, "escape-uri-attributes": true,
	"include-content-type": true,
}

// rdOutputAttrName maps an xsl:result-document attribute name to the
// serialization-parameter name applyOutputAttr knows it by.
func rdOutputAttrName(n string) string {
	if n == "output-version" {
		return "version"
	}
	return n
}

// rdResultDocument is the compiled form of xsl:result-document.
type rdResultDocument struct {
	el       *xmltree.Node
	href     *avt     // @href AVT (the secondary output URI)
	method   *avt     // @method AVT (optional serialization method)
	format   *avt     // @format AVT (named xsl:output reference; QName-resolved after evaluation)
	paramDoc *avt     // @parameter-document AVT (serialization-parameters document URI)
	useCMaps []string // @use-character-maps (clark-resolved)
	encoding string   // @encoding
	// noEscURI/hasEscURI: @escape-uri-attributes, if given directly on this
	// instruction (result-document-0264/0266/0268: escape-uri-attributes="no").
	noEscURI  bool
	hasEscURI bool
	// noContentType/hasContentType: @include-content-type, if given directly
	// on this instruction (result-document-0271/0273/0275: "no"/"false"/"0").
	noContentType  bool
	hasContentType bool
	// extraAttrs holds the AVT-parsed form of rdExtraAttrNames, keyed by
	// attribute name, for whichever of them are actually present.
	extraAttrs map[string]*avt
	body       []instruction // sequence constructor producing the result content
	// val is the validation/type request — see litElement.val. xsl:result-
	// document is a §24.4.2 document-node constructor.
	val *valRequest
}

func (*rdResultDocument) instr() {}

func rdCompile(c *compiler, el *xmltree.Node) (instruction, error) {
	n := &rdResultDocument{el: el}
	{
		val, err := compileValidation(el, vkDocument)
		if err != nil {
			return nil, err
		}
		n.val = val
	}

	if h, ok := el.AttrLocal("href"); ok {
		a, err := parseAVTFor(el, h)
		if err != nil {
			return nil, errAt(el, "xsl:result-document bad href %q: %v", h, err)
		}
		n.href = a
	}
	if m, ok := el.AttrLocal("method"); ok {
		a, err := parseAVTFor(el, m)
		if err != nil {
			return nil, errAt(el, "xsl:result-document bad method %q: %v", m, err)
		}
		n.method = a
	}
	if f, ok := el.AttrLocal("format"); ok {
		a, err := parseAVTFor(el, f)
		if err != nil {
			return nil, errAt(el, "xsl:result-document bad format %q: %v", f, err)
		}
		n.format = a
	}
	if pd, ok := el.AttrLocal("parameter-document"); ok {
		a, err := parseAVTFor(el, pd)
		if err != nil {
			return nil, errAt(el, "xsl:result-document bad parameter-document %q: %v", pd, err)
		}
		n.paramDoc = a
	}
	if v, ok := el.AttrLocal("use-character-maps"); ok {
		for _, tok := range strings.Fields(v) {
			n.useCMaps = append(n.useCMaps, clarkName(resolveQName(el, tok)))
		}
	}
	if v, ok := el.AttrLocal("encoding"); ok {
		n.encoding = v
	}
	if v, ok := el.AttrLocal("escape-uri-attributes"); ok {
		// A boolean-valued serialization parameter with a statically-known
		// value outside the XSLT boolean lexical space is SEPM0016
		// (result-document-0269's escape-uri-attributes="y e s").
		if !strings.ContainsAny(v, "{}") && strings.TrimSpace(v) != "" && !isOutputBool(v) {
			return nil, errAt(el, "err:SEPM0016: invalid escape-uri-attributes value %q", v)
		}
		n.hasEscURI = true
		switch strings.TrimSpace(v) {
		case "no", "false", "0":
			n.noEscURI = true
		default:
			n.noEscURI = false
		}
	}
	if v, ok := el.AttrLocal("include-content-type"); ok {
		if !strings.ContainsAny(v, "{}") && strings.TrimSpace(v) != "" && !isOutputBool(v) {
			return nil, errAt(el, "err:SEPM0016: invalid include-content-type value %q", v)
		}
		n.hasContentType = true
		switch strings.TrimSpace(v) {
		case "no", "false", "0":
			n.noContentType = true
		case "yes", "true", "1":
			n.noContentType = false
		default:
			// A literal (non-AVT) value outside the xs:boolean-ish lexical
			// space is a static error (result-document-0276: "Yes", capital
			// Y). A value that still contains "{" is an AVT this engine
			// doesn't statically evaluate here — left permissive (treated as
			// content-type ON) rather than risk misjudging a genuinely
			// dynamic value (result-document-0701/1205 use exactly this).
			if !strings.Contains(v, "{") {
				return nil, errAt(el, "err:XTSE0020: invalid include-content-type value %q", v)
			}
			n.noContentType = false
		}
	}
	for _, name := range rdExtraAttrNames {
		if v, ok := el.AttrLocal(name); ok {
			a, err := parseAVTFor(el, v)
			if err != nil {
				return nil, errAt(el, "xsl:result-document bad %s %q: %v", name, v, err)
			}
			if n.extraAttrs == nil {
				n.extraAttrs = map[string]*avt{}
			}
			n.extraAttrs[name] = a
		}
	}

	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	n.body = body
	return n, nil
}

// rdEffectiveOutput resolves the serialization settings for this instruction:
// the named @format output (if any) overridden by the instruction's own
// serialization attributes.
func (n *rdResultDocument) rdEffectiveOutput(eng *engine, r rt) (Output, error) {
	var cfg Output
	if n.format != nil {
		fv, err := eng.evalAVT(n.format, n.el, r)
		if err != nil {
			return cfg, err
		}
		if fv != "" {
			// The effective value must be a valid EQName whose prefix is
			// bound, and must name an output definition the stylesheet
			// declares — otherwise XTDE1460 (error-0290a/1460d).
			if pfx, _, ok := strings.Cut(fv, ":"); ok && pfx != "" && !strings.HasPrefix(fv, "Q{") {
				if _, bound := n.el.LookupPrefix(pfx); !bound {
					return cfg, errAt(n.el, "err:XTDE1460: no namespace declaration in scope for prefix %q in format %q", pfx, fv)
				}
			}
			// Keyed by the expanded {uri}local QName — resolved against
			// THIS instruction's own in-scope namespaces, which may bind the
			// prefix differently than wherever the xsl:output declaration
			// itself lives (result-document-0238).
			key := clarkName(resolveQName(n.el, fv))
			named, ok := eng.sheet.namedOutputs[key]
			if !ok {
				return cfg, errAt(n.el, "err:XTDE1460: no xsl:output declaration named %q", fv)
			}
			cfg = named
		}
	}
	// @parameter-document supplies a whole serialization-parameters document;
	// like xsl:output's own, it is applied BEFORE this instruction's
	// attributes, which override it (result-document-1406).
	if n.paramDoc != nil {
		pd, err := eng.evalAVT(n.paramDoc, n.el, r)
		if err != nil {
			return cfg, err
		}
		if strings.TrimSpace(pd) != "" {
			if err := loadParameterDocument(&cfg, n.el, eng.baseDir, pd); err != nil {
				return cfg, err
			}
		}
	}
	if n.method != nil {
		m, err := eng.evalAVT(n.method, n.el, r)
		if err != nil {
			return cfg, err
		}
		if m != "" {
			cfg.Method = m
		}
	}
	if len(n.useCMaps) > 0 {
		// Merges with the @format output's maps; later names win on conflicts.
		cfg.UseCharacterMaps = append(append([]string{}, cfg.UseCharacterMaps...), n.useCMaps...)
	}
	if n.encoding != "" {
		cfg.Encoding = n.encoding
	}
	if n.hasEscURI {
		cfg.NoEscapeURIAttributes = n.noEscURI
	}
	if n.hasContentType {
		cfg.NoContentType = n.noContentType
	}
	for _, name := range rdExtraAttrNames {
		a, ok := n.extraAttrs[name]
		if !ok {
			continue
		}
		v, err := eng.evalAVT(a, n.el, r)
		if err != nil {
			return cfg, err
		}
		// Every xs:boolean-valued serialization parameter must hold a value
		// in the XSLT boolean lexical space: anything else is SEPM0016, not a
		// silent "not yes" (xml-version-041's undeclare-prefixes="T",
		// result-document-0255's omit-xml-declaration="01", -0262's
		// byte-order-mark="00", -0283's indent="NO").
		if rdBoolAttrs[name] && strings.TrimSpace(v) != "" && !isOutputBool(v) {
			return cfg, errAt(n.el, "err:SEPM0016: invalid %s value %q", name, v)
		}
		// standalone additionally allows the value "omit".
		if name == "standalone" && strings.TrimSpace(v) != "" &&
			!isOutputBool(v) && strings.TrimSpace(v) != "omit" {
			return cfg, errAt(n.el, "err:SEPM0016: invalid standalone value %q", v)
		}
		applyOutputAttr(&cfg, n.el, rdOutputAttrName(name), v)
	}
	return cfg, nil
}

func (n *rdResultDocument) exec(eng *engine, r rt, out *xmltree.Node) error {
	// XSLT 3.0 "temporary output state" (5.7.1): forbidden while evaluating
	// an xsl:key's value (result-document-1131/1137/1139..1144).
	if eng.tempOutputDepth > 0 {
		return errAt(n.el, "err:XTDE1480: xsl:result-document is not allowed while evaluating a temporary-output-state expression (e.g. an xsl:key value)")
	}
	cfg, err := n.rdEffectiveOutput(eng, r)
	if err != nil {
		return err
	}

	// Resolve the secondary output URI.
	href := ""
	if n.href != nil {
		h, err := eng.evalAVT(n.href, n.el, r)
		if err != nil {
			return err
		}
		href = h
	}
	if strings.TrimSpace(href) != "" {
		if fr, ok := eng.resolver.(*fileResolver); ok && fr.WasRead(href) {
			return errAt(n.el, "err:XTDE1500: xsl:result-document writes to %q, which has already been read (via fn:doc/document()) during this transformation", href)
		}
	}

	// An absent or empty href targets the base output URI — i.e. the principal
	// output. Write the body straight into the current output tree and carry
	// the serialization attributes to the principal serialization.
	if strings.TrimSpace(href) == "" {
		// A bare/empty-href xsl:result-document redirects to the principal
		// tree. That is fine when it is the SOLE contributor (nested inside
		// another, genuinely secondary xsl:result-document that merges its
		// content back — result-document-0205 — or as the template's only
		// content, as in most result-document-02xx tests). It is a
		// duplicate-URI error when the enclosing template flow has ALREADY
		// written other content directly to the principal tree — i.e. this
		// instruction is not the tree's sole/first writer (err:XTDE1490 —
		// error-1490c, where a literal wrapping element already opened the
		// principal tree before the nested result-document runs;
		// result-document-1005/1006, where it is opened before an enclosing
		// SECONDARY result-document, so the nesting depth is irrelevant).
		if eng.principalRoot != nil && len(eng.principalRoot.Children) > 0 {
			return errAt(n.el, "err:XTDE1490: xsl:result-document with no href duplicates the principal output's URI")
		}
		c := cfg
		eng.rdOutput = &c
		// A bare/empty-href xsl:result-document always redirects to the TRUE
		// principal output tree, never to whatever fragment happens to be
		// the immediate `out` parameter — which, nested inside another
		// xsl:result-document's own body execution, would be that OTHER
		// instruction's isolated temporary fragment instead of the real
		// principal (result-document-0205).
		target := out
		if eng.principalRoot != nil {
			target = eng.principalRoot
		}
		// This instruction's own item-separator governs the principal
		// serialization, so its target must keep top-level items discrete
		// too (see Output.ItemSeparator).
		if cfg.HasItemSeparator && target != nil {
			target.NoAtomicMerge = true
		}
		// This instruction also decides whether the principal result is a tree
		// or a raw sequence (its own @build-tree / json-or-adaptive @method) —
		// the root was created before that was knowable, but it is still empty
		// (the XTDE1490 check above guarantees it), so switching it now is safe.
		prepareRawRoot(target, cfg)
		if err := eng.execSequence(n.body, r, target); err != nil {
			return err
		}
		// The href-less form redirects into the PRINCIPAL result tree, and
		// §24.4.2's validation applies to it just the same. It was previously
		// skipped on the theory that this instruction does not exclusively own
		// that tree — but the XTDE1490 check above has already established
		// that the tree was empty when this instruction started and that
		// nothing may write to it afterwards, so for the duration of this
		// episode the instruction IS its sole writer (error-1550a/1550b,
		// error-1555a/1555b/1555c, import-schema-118/119/161).
		// The instruction's own attributes override the stylesheet's
		// xsl:output, exactly as finishRun merges them for serialization —
		// item-separator is usually written on xsl:output and not repeated
		// on the instruction (validation-0214).
		sep, hasSep := cfg.ItemSeparator, cfg.HasItemSeparator
		if !hasSep {
			sep, hasSep = eng.sheet.output.ItemSeparator, eng.sheet.output.HasItemSeparator
		}
		eng.applyItemSeparator(n.val, target, sep, hasSep)
		if err := eng.applyValidation(n.val, target); err != nil {
			return err
		}
		if eng.principalRoot != nil {
			// The principal result tree is now FINAL: this instruction wrote
			// it in its entirety. Anything the transform appends to it
			// afterwards would be a second result tree for the same URI
			// (result-document-1002) — recorded here and checked once the
			// transform finishes (see checkPrincipalSealed).
			eng.principalSealedAt = len(eng.principalRoot.Children)
			if n := eng.principalSealedAt; n > 0 {
				if last := eng.principalRoot.Children[n-1]; last.Kind == xmltree.KindText && !last.Atomic {
					eng.principalSealedLastText = last
					eng.principalSealedTextLen = len(last.Value)
				}
			}
		}
		return nil
	}

	// Execute the body into a temporary fragment so it does not touch the
	// principal output tree.
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: cfg.HasItemSeparator}
	prepareRawRoot(frag, cfg)
	eng.secondaryDocDepth++
	// While the body runs, fn:current-output-uri() reports THIS document's
	// absolute URI: @href resolved against the base output URI
	// (current-output-uri-003). The raw href is deliberately left untouched
	// everywhere else — it is the key the harness and the XTDE1490 duplicate
	// check use to identify the secondary output.
	eng.outURIStack = append(eng.outURIStack, resolveOutputURI(href, eng.outURI))
	err = eng.execSequence(n.body, r, frag)
	eng.outURIStack = eng.outURIStack[:len(eng.outURIStack)-1]
	eng.secondaryDocDepth--
	if err != nil {
		return err
	}
	// §24.4.2: the result document is a constructed document node, so its own
	// validation/type request governs its single element child.
	//
	// RESIDUAL: only this, the genuine SECONDARY-output path, is validated.
	// The href-less form above redirects into the PRINCIPAL result tree, which
	// this instruction does not exclusively own — it may already hold content
	// written before the redirect — so validating it here would assess nodes
	// that are not this instruction's to assess.
	sepS, hasSepS := cfg.ItemSeparator, cfg.HasItemSeparator
	if !hasSepS {
		sepS, hasSepS = eng.sheet.output.ItemSeparator, eng.sheet.output.HasItemSeparator
	}
	eng.applyItemSeparator(n.val, frag, sepS, hasSepS)
	if err := eng.applyValidation(n.val, frag); err != nil {
		return err
	}

	method := cfg.Method
	if method == "" {
		method = "xml"
	}
	if method == "json" || method == "adaptive" {
		content, err := eng.sheet.serializeRawResult(cfg, method, frag)
		if err != nil {
			return err
		}
		return n.recordSecondary(eng, href, content, method, frag)
	}
	includeContentType := !cfg.NoContentType && cfg.Method != "" && (method == "xhtml" || method == "html")
	content := xmltree.Serialize(frag, xmltree.SerializeOptions{
		Method:                method,
		Indent:                cfg.Indent,
		OmitXMLDeclaration:    cfg.OmitXMLDeclaration,
		Encoding:              cfg.Encoding,
		CharacterMap:          mergeCharMaps(eng.sheet.resolveCharMap(cfg.UseCharacterMaps, map[string]bool{}), cfg.InlineCharMap),
		DoctypePublic:         cfg.DoctypePublic,
		DoctypeSystem:         cfg.DoctypeSystem,
		NoEscapeURIAttributes: cfg.NoEscapeURIAttributes,
		Standalone:            cfg.Standalone,
		MediaType:             cfg.MediaType,
		IncludeContentType:    includeContentType,
		CDATAElements:         cfg.CDATASectionElements,
		SuppressIndentation:   cfg.SuppressIndentation,
		HTMLVersion:           cfg.HTMLVersion,
		ByteOrderMark:         cfg.ByteOrderMark,
		NormalizationForm:     cfg.NormalizationForm,
		XMLVersion:            cfg.Version,
		UndeclarePrefixes:     cfg.UndeclarePrefixes,
		SeparateItems:         cfg.HasItemSeparator,
		ItemSeparator:         cfg.ItemSeparator,
	})

	return n.recordSecondary(eng, href, content, method, frag)
}

// recordSecondary files one finished secondary result under its href, refusing
// a second result for a URI already written (XTDE1490).
func (n *rdResultDocument) recordSecondary(eng *engine, href, content, method string, frag *xmltree.Node) error {
	for _, sec := range eng.secondary {
		if sec.Href == href {
			return errAt(n.el, "err:XTDE1490: two xsl:result-document instructions write to %q", href)
		}
	}
	eng.secondary = append(eng.secondary, SecondaryDoc{
		Href:    href,
		Content: content,
		Method:  method,
		Root:    frag,
	})
	return nil
}

// isOutputBool reports whether v is one of the lexical forms an xs:boolean
// serialization parameter accepts.
func isOutputBool(v string) bool {
	switch strings.TrimSpace(v) {
	case "yes", "no", "true", "false", "1", "0":
		return true
	}
	return false
}

// resolveOutputURI resolves an xsl:result-document @href against the base
// output URI. With no base output URI there is no absolute URI to report, so
// the result is "" (fn:current-output-uri is then the empty sequence).
func resolveOutputURI(href, base string) string {
	href = strings.TrimSpace(href)
	if base == "" {
		return ""
	}
	if href == "" {
		return base
	}
	if abs, err := xpath.ResolveURIRef(href, base); err == nil && abs != "" {
		return abs
	}
	return href
}
