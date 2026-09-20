package xslt

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Result is the outcome of a transformation.
type Result struct {
	Root   *xmltree.Node // synthetic document root of the result tree
	Method string
}

// Transform runs the stylesheet against the given source XML with the provided
// top-level string parameters. It returns the serialized output and the
// effective output method.
func (ss *Stylesheet) Transform(srcXML string, params map[string]string) (output, method string, err error) {
	rr, err := ss.TransformFull(srcXML, params, "")
	if err != nil {
		return "", "", err
	}
	return rr.Output, rr.Method, nil
}

// TransformFull runs the stylesheet and returns the full result including
// xsl:message output and xsl:result-document secondary outputs. baseDir is the
// workspace directory used to resolve/write files.
func (ss *Stylesheet) TransformFull(srcXML string, params map[string]string, baseDir string) (*RunResult, error) {
	eng, root, err := ss.transformInto(srcXML, params, baseDir)
	if err != nil {
		return nil, err
	}
	return ss.finishRun(eng, root)
}

// finishRun serializes the result tree and packages it with the run's messages
// and secondary outputs.
func (ss *Stylesheet) finishRun(eng *engine, root *xmltree.Node) (*RunResult, error) {
	cfg := ss.output
	if eng.rdOutput != nil {
		// An empty-href xsl:result-document redirects the principal output and
		// carries its own serialization attributes.
		if eng.rdOutput.Method != "" {
			cfg.Method = eng.rdOutput.Method
		}
		if eng.rdOutput.Encoding != "" {
			cfg.Encoding = eng.rdOutput.Encoding
		}
		if eng.rdOutput.Indent {
			// One-way like NoEscapeURIAttributes/NoContentType below: the
			// principal xsl:output's own indent="yes" (if any) isn't
			// overridden back to "no" by a result-document redirect that
			// simply didn't mention @indent (result-document-0277).
			cfg.Indent = true
		}
		if len(eng.rdOutput.UseCharacterMaps) > 0 {
			cfg.UseCharacterMaps = eng.rdOutput.UseCharacterMaps
		}
		if eng.rdOutput.NoEscapeURIAttributes {
			// "true" is the only non-default state for this flag (default is
			// escaping ON), so — like the string/slice fields above — it only
			// overrides the principal xsl:output when actually set, rather
			// than blindly resetting an inherited "no" back to the default.
			cfg.NoEscapeURIAttributes = true
		}
		if eng.rdOutput.NoContentType {
			// Same one-way pattern as NoEscapeURIAttributes above (default is
			// content-type ON): result-document-0271/0273/0275.
			cfg.NoContentType = true
		}
		if eng.rdOutput.OmitXMLDeclaration {
			cfg.OmitXMLDeclaration = true
		}
		if eng.rdOutput.DoctypePublic != "" {
			cfg.DoctypePublic = eng.rdOutput.DoctypePublic
		}
		if eng.rdOutput.DoctypeSystem != "" {
			cfg.DoctypeSystem = eng.rdOutput.DoctypeSystem
		}
		if len(eng.rdOutput.CDATASectionElements) > 0 {
			cfg.CDATASectionElements = eng.rdOutput.CDATASectionElements
		}
		if len(eng.rdOutput.SuppressIndentation) > 0 {
			cfg.SuppressIndentation = eng.rdOutput.SuppressIndentation
		}
		if eng.rdOutput.Standalone != "" {
			cfg.Standalone = eng.rdOutput.Standalone
		}
		if eng.rdOutput.MediaType != "" {
			cfg.MediaType = eng.rdOutput.MediaType
		}
		if eng.rdOutput.HTMLVersion != "" {
			cfg.HTMLVersion = eng.rdOutput.HTMLVersion
		}
		if eng.rdOutput.ByteOrderMark {
			cfg.ByteOrderMark = true
		}
		if eng.rdOutput.Version != "" {
			cfg.Version = eng.rdOutput.Version
		}
		if eng.rdOutput.NormalizationForm != "" {
			cfg.NormalizationForm = eng.rdOutput.NormalizationForm
		}
		if eng.rdOutput.UndeclarePrefixes {
			cfg.UndeclarePrefixes = true
		}
		if eng.rdOutput.HasItemSeparator {
			cfg.ItemSeparator, cfg.HasItemSeparator = eng.rdOutput.ItemSeparator, true
		}
		if len(eng.rdOutput.InlineCharMap) > 0 {
			// Character mappings spelled out inline by a serialization
			// PARAMETER DOCUMENT reached through this instruction
			// (@parameter-document, or a named @format that has one) — same
			// one-way override as the named-map list above
			// (result-document-1406/1411).
			cfg.InlineCharMap = mergeCharMaps(cfg.InlineCharMap, eng.rdOutput.InlineCharMap)
		}
		if eng.rdOutput.JSONNodeOutputMethod != "" {
			cfg.JSONNodeOutputMethod = eng.rdOutput.JSONNodeOutputMethod
		}
		if eng.rdOutput.AllowDuplicateNames {
			cfg.AllowDuplicateNames = true
		}
		if eng.rdOutput.BuildTree != "" {
			cfg.BuildTree = eng.rdOutput.BuildTree
		}
	}
	if eng.outSink != nil && eng.rdOutput != nil {
		// An empty-href xsl:result-document rewrites the principal output
		// definition after the fact, and nothing can be re-decided once the
		// declaration has gone out. TransformFullTo's caller is required to have
		// ruled such a stylesheet out; failing loudly here beats writing a
		// result under parameters that were replaced halfway through it.
		return nil, fmt.Errorf("internal: incremental output cannot be combined with an empty-href xsl:result-document")
	}
	method := cfg.Method
	if method == "" {
		method = autoMethod(root)
		// XSLT 3.0's own backwards-compatible-processing carve-out (quoted
		// verbatim in the "backwards" test-set's -019/-019b comment): an
		// IMPLICITLY generated result tree (never one reached through an
		// explicit xsl:result-document — eng.rdOutput is nil only for the
		// principal result) whose principal stylesheet module's own
		// outermost element declares version="1.0" defaults to the xml
		// output method instead of xhtml.
		if method == "xhtml" && eng.rdOutput == nil && backwardsCompatRun() && ss.principalVersionIsOne {
			method = "xml"
		}
	}
	// include-content-type's "yes" default only kicks in for an EXPLICITLY
	// requested method="html"/"xhtml" (xsl:output/@method, or a named format /
	// result-document override — anything landing in cfg.Method), OR when an
	// xsl:output declaration is present at all even without @method
	// (character-map-017: the method is auto-detected, but xsl:output itself
	// was still explicitly used), OR when the auto-detected method-html/xhtml
	// content came from an xsl:result-document instruction (eng.rdOutput !=
	// nil — result-document-0209/0210). The legacy auto-detect (an html/HTML-
	// or xhtml-namespaced root with NO xsl:output and NO xsl:result-document
	// at all) predates the content-type-meta feature and must keep producing
	// plain output — stylesheets relying on that heuristic (bug-1301, bug-1901,
	// bug-2401, select-6201, sequence-0601) never asked for html/xhtml
	// serialization and don't expect the extra <meta>, and in bug-1301/2401/
	// select-6201's case an unclosed non-XHTML <meta> would also break their
	// XPath-assertion re-parse.
	// hasOutputDecl only widens the html-detection case for an xhtml method
	// reached via a NAMESPACE match (an html element actually in the XHTML
	// namespace) — sequence-0601 has an xsl:output (@indent only, no
	// @method) too, but its root is a plain no-namespace <HTML> matched by
	// LOCAL NAME alone, and that combination must still produce no meta.
	autoXHTMLWithOutputDecl := method == "xhtml" && ss.hasOutputDecl
	explicitOutput := cfg.Method != "" || eng.rdOutput != nil || autoXHTMLWithOutputDecl
	includeContentType := !cfg.NoContentType && explicitOutput && (method == "xhtml" || method == "html")
	// The json and adaptive output methods serialize a VALUE, not a node tree:
	// they take the (raw) result sequence and render maps, arrays, atomic
	// values and nodes by their own rules — see serializeRawResult.
	if method == "json" || method == "adaptive" {
		out, err := ss.serializeRawResult(cfg, method, root)
		if err != nil {
			return nil, err
		}
		if eng.outWriter != nil {
			if _, err := io.WriteString(eng.outWriter, out); err != nil {
				return nil, err
			}
			out = ""
		}
		return &RunResult{Output: out, Method: method, Messages: eng.messages, Secondary: eng.secondary, Root: root}, nil
	}
	if err := checkSerializationErrors(cfg, method, root); err != nil {
		return nil, err
	}
	so := ss.serializeOptions(cfg, method, includeContentType)
	if eng.outSink != nil {
		// The result tree has been drained into the writer as it was built;
		// only the tail is left to flush (see TransformFullTo).
		if err := eng.outSink.Finish(); err != nil {
			return nil, err
		}
		return &RunResult{Method: method, Messages: eng.messages, Secondary: eng.secondary, Root: root}, nil
	}
	if eng.outWriter != nil {
		if err := xmltree.WriteTo(eng.outWriter, root, so); err != nil {
			return nil, err
		}
		return &RunResult{Method: method, Messages: eng.messages, Secondary: eng.secondary, Root: root, Value: eng.entryValue}, nil
	}
	out := xmltree.Serialize(root, so)
	return &RunResult{Output: out, Method: method, Messages: eng.messages, Secondary: eng.secondary, Root: root, Value: eng.entryValue}, nil
}

// serializeOptions maps a resolved xsl:output definition onto the serializer's
// own option set.
func (ss *Stylesheet) serializeOptions(cfg Output, method string, includeContentType bool) xmltree.SerializeOptions {
	return xmltree.SerializeOptions{
		Method:                method,
		Indent:                cfg.Indent,
		OmitXMLDeclaration:    cfg.OmitXMLDeclaration,
		Encoding:              cfg.Encoding,
		CharacterMap:          mergeCharMaps(ss.resolveCharMap(cfg.UseCharacterMaps, map[string]bool{}), cfg.InlineCharMap),
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
	}
}

// outputCharMapStrings resolves this output definition's character map (named
// xsl:character-map declarations merged with any inline map from a
// parameter-document) into the single-character-keyed string form the
// value-serializing output methods in internal/xpath take.
func (ss *Stylesheet) outputCharMapStrings(cfg Output) map[string]string {
	cm := mergeCharMaps(ss.resolveCharMap(cfg.UseCharacterMaps, map[string]bool{}), cfg.InlineCharMap)
	if len(cm) == 0 {
		return nil
	}
	out := make(map[string]string, len(cm))
	for r, s := range cm {
		out[string(r)] = s
	}
	return out
}

// outputValueParams packages a resolved Output as the parameter set the json /
// adaptive writers in internal/xpath observe.
func (ss *Stylesheet) outputValueParams(cfg Output) xpath.OutputParams {
	return xpath.OutputParams{
		NodeMethod:          cfg.JSONNodeOutputMethod,
		AllowDuplicateNames: cfg.AllowDuplicateNames,
		Encoding:            cfg.Encoding,
		CharacterMap:        ss.outputCharMapStrings(cfg),
		HTMLVersion:         cfg.HTMLVersion,
		ItemSeparator:       cfg.ItemSeparator,
		HasItemSeparator:    cfg.HasItemSeparator,
		OmitXMLDeclaration:  cfg.OmitXMLDeclaration,
	}
}

// serializeRawResult renders a result under the json or adaptive output method.
// Both serialize the result SEQUENCE rather than a tree, so the items are first
// recovered from the collector root: a map/array/function item travels through
// the node tree as a RealItem carrier and a typed atomic value as an
// Atomic-marked text node, and xpath.RawResultItems undoes both round-trips.
func (ss *Stylesheet) serializeRawResult(cfg Output, method string, root *xmltree.Node) (string, error) {
	items := xpath.RawResultItems(xpath.Items(fragAsSequence(root)))
	p := ss.outputValueParams(cfg)
	if method == "json" {
		return xpath.SerializeJSON(items, p)
	}
	return xpath.SerializeAdaptive(items, p)
}

// prepareRawRoot marks a result-collector root as holding a RAW result
// sequence when the output definition says so (XSLT 3.0 @build-tree, defaulting
// to "no" for the json/adaptive methods — see outputIsRaw): its top-level items
// stay discrete and individually typed instead of collapsing into result-tree
// text, which is the only shape the value-serializing output methods can work
// from.
func prepareRawRoot(root *xmltree.Node, cfg Output) {
	if root == nil || !outputIsRaw(cfg) {
		return
	}
	root.NoAtomicMerge = true
	root.KeepDocItems = true
}

// checkSerializationErrors validates the handful of xsl:output serialization
// parameter combinations the spec requires the serializer to reject outright
// (an "SE"-class error) rather than silently reinterpret. Deliberately narrow
// — only the cases the W3C xslt30-test output test-set exercises
// (output-0182 through output-0196); the harness only checks that SOME error
// occurred; for assert-serialization-error, not the specific code, so getting
// a code slightly wrong here doesn't matter as long as the condition is right.
func checkSerializationErrors(cfg Output, method string, root *xmltree.Node) error {
	isXML := method == "xml" || method == "xhtml"

	// SEPM0004: a standalone value other than "omit", or doctype-system, both
	// assert the serialized result is (or looks like) a single well-formed
	// XML document — impossible with more than one top-level element
	// (output-0182/0183).
	if isXML && (cfg.Standalone != "" || cfg.DoctypeSystem != "") {
		n := 0
		for _, c := range serializationTopLevel(root) {
			if c.Kind == xmltree.KindElement {
				n++
			}
		}
		if n > 1 {
			return fmt.Errorf("err:SEPM0004: cannot serialize %d top-level elements with standalone or doctype-system set", n)
		}
	}

	// SEPM0009: omit-xml-declaration=yes cannot be combined with a
	// non-omitted standalone (there would be nowhere to put it), nor — at a
	// version other than 1.0 — with doctype-system (output-0186/0187).
	if isXML && cfg.OmitXMLDeclaration {
		if cfg.Standalone != "" {
			return fmt.Errorf("err:SEPM0009: omit-xml-declaration=yes is incompatible with standalone=%q", cfg.Standalone)
		}
		if cfg.DoctypeSystem != "" && cfg.Version != "" && cfg.Version != "1.0" {
			return fmt.Errorf("err:SEPM0009: omit-xml-declaration=yes is incompatible with doctype-system at version %q", cfg.Version)
		}
	}

	// SEPM0010: undeclare-prefixes=yes needs the XML 1.1 xmlns:p="" syntax,
	// unavailable for the xml method at version 1.0 (output-0188).
	if method == "xml" && cfg.UndeclarePrefixes && (cfg.Version == "" || cfg.Version == "1.0") {
		return fmt.Errorf("err:SEPM0010: undeclare-prefixes=yes requires version 1.1")
	}

	// SEPM0016: a doctype-public value containing a character outside the XML
	// PubidChar repertoire is not a legal public identifier (output-0284, per
	// XSLT 2.0 Erratum E3).
	if isXML && cfg.DoctypePublic != "" && !validPubidLiteral(cfg.DoctypePublic) {
		return fmt.Errorf("err:SEPM0016: doctype-public %q is not a valid public identifier", cfg.DoctypePublic)
	}

	// SESU0007: an encoding this serializer doesn't support, checked for the
	// html/text methods (output-0184/0185) — the engine always serializes as
	// UTF-8 internally regardless of @encoding, so a value outside this small
	// recognized set is genuinely unsupported, not just untested.
	if (method == "html" || method == "text") && cfg.Encoding != "" && !supportedOutputEncoding(cfg.Encoding) {
		return fmt.Errorf("err:SESU0007: unsupported encoding %q", cfg.Encoding)
	}

	// SESU0011: an unrecognized normalization-form (output-0189/0190/0191/
	// 0192/0193 — "fully-normalized" is spec-legal but requires normalization
	// analysis this engine doesn't implement, so it's treated as unsupported
	// too, one of the two error codes output-0193 explicitly accepts).
	if cfg.NormalizationForm != "" && !supportedNormalizationForm(cfg.NormalizationForm) {
		return fmt.Errorf("err:SESU0011: unsupported normalization-form %q", cfg.NormalizationForm)
	}

	// SERE0006: the result tree carries a character the target XML version
	// cannot represent at all. The C0 controls (bar tab/LF/CR) are legal in
	// XML 1.1 only as character references and not legal in XML 1.0 in any
	// form, so serializing one as version 1.0 is an error rather than an
	// escaping decision (xml-version-029/030).
	if isXML && strings.TrimSpace(cfg.Version) != "1.1" {
		if c, bad := firstIllegalXML10Char(root); bad {
			return fmt.Errorf("err:SERE0006: character #x%X cannot be serialized as XML 1.0", c)
		}
	}

	// SESU0013: an html-method @version this serializer doesn't recognize —
	// accepts anything parsing as a number >= 1 (output-0194: "0.0" fails).
	if method == "html" && cfg.Version != "" {
		if v, err := strconv.ParseFloat(cfg.Version, 64); err != nil || v < 1 {
			return fmt.Errorf("err:SESU0013: unsupported html output version %q", cfg.Version)
		}
	}

	// XTSE0020: an html/xhtml @html-version that isn't even a number is an
	// invalid attribute value (output-0230: html-version="five").
	if (method == "html" || method == "xhtml") && cfg.HTMLVersion != "" {
		if _, err := strconv.ParseFloat(strings.TrimSpace(cfg.HTMLVersion), 64); err != nil {
			return fmt.Errorf("err:XTSE0020: invalid html-version %q", cfg.HTMLVersion)
		}
	}

	// SERE0014/SERE0015: the plain html method (unlike xhtml, which can
	// always fall back to a numeric character reference) has no syntax for a
	// C1 control character (or DEL), nor for ">" inside a processing
	// instruction (output-0195/0196).
	if method == "html" {
		// SERE0014 is an HTML 4 rule: HTML 4 has no way to write a C1 control
		// character at all, but HTML 5 does (a numeric character reference), so
		// at html-version 5 the character is serialized rather than rejected
		// (output-0195 vs output-0195a/0195b — the suite splits the two cases
		// on exactly this, one per processor default). This processor's default
		// html-version is 5. SERE0015 below is NOT version-dependent: no HTML
		// version can represent ">" inside a processing instruction
		// (output-0196 declares no version dependency at all).
		if !htmlVersionAtLeast5(cfg) && hasForbiddenHTMLChar(root) {
			return fmt.Errorf("err:SERE0014: the html output method cannot represent a #x7F-#x9F character")
		}
		if piContainsGT(root) {
			return fmt.Errorf(`err:SERE0015: the html output method cannot represent ">" inside a processing instruction`)
		}
	}
	return nil
}

// htmlVersionAtLeast5 reports whether the effective html-version for an
// html/xhtml serialization is 5.0 or later. The version comes from
// @html-version, or from @version (which IS the html version under those two
// methods); with neither given the processor's own default applies, and this
// processor's default is 5 — the same default modern processors report through
// the default_html_version test dependency.
func htmlVersionAtLeast5(cfg Output) bool {
	v := strings.TrimSpace(cfg.HTMLVersion)
	if v == "" {
		v = strings.TrimSpace(cfg.Version)
	}
	if v == "" {
		return true
	}
	f, err := strconv.ParseFloat(v, 64)
	return err == nil && f >= 5
}

// supportedOutputEncoding is the small set of @encoding values this
// (internally always-UTF-8) serializer treats as genuinely usable.
// validPubidLiteral reports whether s consists solely of XML PubidChars
// (the grammar for a DOCTYPE PUBLIC identifier literal): #x20 | #xD | #xA |
// [a-zA-Z0-9] | [-'()+,./:=?;!*#@$_%].
func validPubidLiteral(s string) bool {
	for _, r := range s {
		switch {
		case r == ' ' || r == '\r' || r == '\n':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-'()+,./:=?;!*#@$_%", r):
		default:
			return false
		}
	}
	return true
}

func supportedOutputEncoding(enc string) bool {
	switch strings.ToUpper(strings.TrimSpace(enc)) {
	case "UTF-8", "UTF-16", "UTF-16BE", "UTF-16LE", "US-ASCII", "ASCII", "ISO-8859-1":
		return true
	}
	return false
}

func supportedNormalizationForm(v string) bool {
	switch v {
	case "none", "NFC", "NFD", "NFKC", "NFKD":
		return true
	}
	return false
}

// serializationTopLevel mirrors xmltree's own topLevel: the nodes actually
// serialized at the top of the document (children of a document/result root,
// or the node itself if it's already an element).
func serializationTopLevel(n *xmltree.Node) []*xmltree.Node {
	if n.Kind == xmltree.KindDocument || (n.Kind == xmltree.KindElement && n.Name.Local == "" && n.Name.Space == "") {
		return n.Children
	}
	return []*xmltree.Node{n}
}

// hasForbiddenHTMLChar reports whether any text or attribute value in the
// tree contains a character in #x7F-#x9F (DEL plus the C1 controls).
func hasForbiddenHTMLChar(n *xmltree.Node) bool {
	inRange := func(s string) bool {
		for _, r := range s {
			if r >= 0x7F && r <= 0x9F {
				return true
			}
		}
		return false
	}
	switch n.Kind {
	case xmltree.KindText:
		if inRange(n.Value) {
			return true
		}
	case xmltree.KindElement:
		for _, a := range n.Attrs {
			if inRange(a.Value) {
				return true
			}
		}
	}
	for _, c := range n.Children {
		if hasForbiddenHTMLChar(c) {
			return true
		}
	}
	return false
}

// piContainsGT reports whether any processing instruction in the tree has a
// ">" in its content (illegal in the html method, which has no PI escaping).
func piContainsGT(n *xmltree.Node) bool {
	if n.Kind == xmltree.KindPI && strings.Contains(n.Value, ">") {
		return true
	}
	for _, c := range n.Children {
		if piContainsGT(c) {
			return true
		}
	}
	return false
}

// mergeCharMaps overlays the inline character mappings a serialization
// parameter document supplied (Output.InlineCharMap) on the named
// xsl:character-map ones.
func mergeCharMaps(named, inline map[rune]string) map[rune]string {
	if len(inline) == 0 {
		return named
	}
	out := make(map[rune]string, len(named)+len(inline))
	for k, v := range named {
		out[k] = v
	}
	for k, v := range inline {
		out[k] = v
	}
	return out
}

// resolveCharMap merges the xsl:character-map declarations referenced by
// use-character-maps names (recursively; later mappings override earlier ones).
func (ss *Stylesheet) resolveCharMap(names []string, visited map[string]bool) map[rune]string {
	if len(names) == 0 {
		return nil
	}
	out := map[rune]string{}
	for _, name := range names {
		if visited[name] {
			continue
		}
		visited[name] = true
		decl, ok := ss.charMaps[name]
		if !ok {
			continue
		}
		if use, ok := decl.AttrLocal("use-character-maps"); ok {
			var refs []string
			for _, tok := range strings.Fields(use) {
				refs = append(refs, clarkName(resolveQName(decl, tok)))
			}
			for k, v := range ss.resolveCharMap(refs, visited) {
				out[k] = v
			}
		}
		for _, ch := range decl.Children {
			if ch.Kind != xmltree.KindElement || ch.Name.Space != NS || ch.Name.Local != "output-character" {
				continue
			}
			cs, _ := ch.AttrLocal("character")
			rs, _ := ch.AttrLocal("string")
			for _, r := range cs {
				out[r] = rs
				break
			}
		}
	}
	return out
}

// EntryParam is one parameter the host supplies to an entry point.
// Name is a lexical QName, or "{uri}local" when the host has already resolved
// the prefix in ITS OWN namespace scope (which is not the stylesheet's).
// Select is an XPath expression giving the value.
type EntryParam struct {
	Name   string
	Select string
	Tunnel bool
}

// Entry specifies a transformation entry point (for conformance testing). A zero
// Entry runs the default: apply-templates to the source document in the unnamed
// mode. Exactly one of Template / Mode / Function selects an alternative entry.
type Entry struct {
	Template    string   // initial named template
	Mode        string   // initial mode for apply-templates
	Function    string   // initial function (QName/local)
	FuncArgs    []string // positional argument selects for the initial function
	ContextItem string   // XPath select for the initial context item (optional)
	// InitialItems supplies the initial context item / initial match selection
	// as REAL XDM items rather than as an expression to evaluate. It is what
	// fn:transform's source-node / initial-match-selection options carry: the
	// caller already has the value, and a node must keep its identity. When
	// set it wins over ContextItem and over the parsed source document.
	InitialItems []xpath.Item
	// MatchSelection is the XSLT 3.0 "initial match selection": an XPath
	// expression (evaluated with the source document as context) giving the
	// whole SEQUENCE apply-templates starts from, instead of the single
	// ContextItem node. Only meaningful for the initial-mode entry point.
	MatchSelection string
	Params         map[string]string
	// TemplateParams are parameters supplied by the host to the INITIAL
	// TEMPLATE itself (xsl:param children of that template), as distinct from
	// the global stylesheet parameters in Params (initial-template-002/003
	// supply both, with the same name, and each must land in its own place).
	TemplateParams []EntryParam
	// Collections supplies the host's fn:collection() bindings: a collection
	// URI (relative ones resolve against baseDir; "" is the DEFAULT
	// collection) mapped to the hrefs of the documents it contains. An href
	// may carry a fragment identifier naming an xml:id/ID-typed element
	// inside the document, in which case that element — not the document
	// node — is the collection member.
	Collections map[string][]string
	// BaseOutputURI is the absolute URI the PRINCIPAL result document is being
	// written to, as supplied by the host. fn:current-output-uri() returns it
	// while the principal output is being built, and xsl:result-document's
	// relative @href resolves against it. Empty means the host did not say
	// where the output goes, in which case fn:current-output-uri() is the
	// empty sequence everywhere (current-output-uri-013/015).
	BaseOutputURI string
	// SourceStreamed says the host supplied the primary source document as a
	// STREAMED document. XSLT 3.0 leaves it to the host how an input reaches
	// the processor (§26.2), and the W3C catalog says so with
	// <source streaming="true"/>. The engine still builds the whole tree — it
	// streams only what xsl:source-document opens — but the DECLARATION is
	// observable in its own right: §10.3.6 saves an absent focus into a
	// context-dependent function item captured over a streamed node, and
	// XTDE3362 forbids reading a non-streamable accumulator on one.
	SourceStreamed bool
	// RequirePublicEntry restricts the entry point to a component whose
	// visibility is public or final. It is set when the invocation crosses a
	// PACKAGE boundary — fn:transform's package-based invocation names a
	// separately-compiled package, whose private components it cannot see
	// (transform-007 asks for a private named template: XTDE0040).
	RequirePublicEntry bool
	// SourceURI optionally names the real retrieval location the primary
	// source document's text came from (e.g. a conformance harness that read
	// it off disk itself instead of letting fn:doc do so). It becomes that
	// document's intrinsic base URI, which is what a relative href in
	// document()/doc() called on one of its nodes resolves against
	// (catalog-002/003/004), AND is seeded into the run's resolver cache
	// under that same resolved path — so fn:doc(fn:document-uri(.)) on the
	// primary source returns the identical node (accessor-008/036: "doc(
	// document-uri($arg)) is $arg"), not a second, freshly-reparsed copy.
	// Without it, the primary source's base-uri falls back to the bare
	// baseDir (a directory, not this specific file).
	SourceURI string
	// SourceValidation requests schema validation of the PRIMARY SOURCE
	// document before the transformation starts: "strict" or "lax" (anything
	// else, including the empty string, means no validation).
	//
	// XSLT 3.0 leaves how a source document acquires type annotations to the
	// host — §26.2's schema-aware processor consumes a PSVI it is GIVEN, and
	// the spec deliberately says nothing about how the caller obtained one.
	// This is that host interface: it is the only way a source node can carry
	// a real type annotation, which is what schema-element(N) and
	// element(N, T) need in order to match anything at all. The W3C test
	// runner does the same thing by hand, round-tripping the document through
	// a throwaway stylesheet (runner/run-tests.xsl c:validated-document).
	SourceValidation string
	// SourceSchemas are the schema documents SourceValidation validates
	// against, as file paths. Empty means "whatever the stylesheet itself
	// imported" — which is the right answer when the two coincide, and not
	// when they do not, so a host that knows better says so.
	SourceSchemas []string
	// DocValidation extends SourceValidation to the SECONDARY source
	// documents a host makes available by URI — the ones a stylesheet reaches
	// through document()/fn:doc() rather than as its input. It maps each
	// such href to "strict" or "lax".
	//
	// Same host-interface reasoning as SourceValidation, and needed for the
	// same reason: a pattern like schema-element(t:test) can only ever match
	// a node something annotated, and a document the stylesheet merely FETCHES
	// is not otherwise validated by anything (validation-2001/2002 fetch two
	// documents the environment declares validation="strict" and match typed
	// patterns against them).
	DocValidation map[string]string
	// ParamSelects supplies stylesheet parameters as XPath EXPRESSIONS rather
	// than plain text, so a caller can hand over a typed value (an integer, a
	// boolean, a constructed atomic) instead of an xs:untypedAtomic string —
	// which is what the W3C catalog's <param select="8"/> means (key-085a/b:
	// a key pattern comparing "string-length(@name) gt $min" needs a NUMBER).
	// A name present here wins over the same name in Params; an expression
	// that fails to parse or evaluate falls back to the Params text.
	ParamSelects map[string]string
	// GlobalContextItem overrides the item global variables are evaluated
	// against — fn:transform's own "global-context-item" option (XSLT 3.0
	// §21.2). By default this is the SAME node the initial match selection
	// runs against (InitialItems[0], or — per F&O 3.1 §16.3.2's own
	// source-node row — the ROOT of the tree containing it), but fn:transform
	// lets a caller supply a DIFFERENT global context item than the one
	// apply-templates/call-template actually starts from (fn-transform-82c/
	// 82d). Nil means "no override": the pre-existing default (ctxNode)
	// applies, so every other caller of TransformEntry is unaffected.
	GlobalContextItem xpath.Item
}

// TransformEntry runs the stylesheet from the given entry point.
func (ss *Stylesheet) TransformEntry(srcXML string, e Entry, baseDir string) (*RunResult, error) {
	eng, root, err := ss.transformEntryInto(srcXML, e, baseDir)
	if err != nil {
		return nil, err
	}
	return ss.finishRun(eng, root)
}

// entryTemplateParams compiles the host-supplied initial-template parameters
// into the same *VarDef shape an xsl:with-param would produce, so
// invokeTemplate binds them exactly as a call from within the stylesheet does
// (including required-parameter and tunnel handling).
func entryTemplateParams(ps []EntryParam) ([]*VarDef, error) {
	if len(ps) == 0 {
		return nil, nil
	}
	out := make([]*VarDef, 0, len(ps))
	for _, p := range ps {
		name := xmltree.Name{Local: p.Name}
		if uri, local, ok := bracedName(p.Name); ok {
			name = xmltree.Name{Space: uri, Local: local}
		}
		sel, err := xpath.Parse(p.Select)
		if err != nil {
			return nil, fmt.Errorf("initial-template parameter %s: %v", p.Name, err)
		}
		out = append(out, &VarDef{name: name, sel: sel, isParam: true, tunnel: p.Tunnel})
	}
	return out, nil
}

// bracedName splits a "{uri}local" Clark name.
func bracedName(s string) (uri, local string, ok bool) {
	if len(s) == 0 || s[0] != '{' {
		return "", "", false
	}
	i := strings.IndexByte(s, '}')
	if i < 0 {
		return "", "", false
	}
	return s[1:i], s[i+1:], true
}

func (ss *Stylesheet) transformEntryInto(srcXML string, e Entry, baseDir string) (*engine, *xmltree.Node, error) {
	var doc *xmltree.Node
	if strings.TrimSpace(srcXML) != "" {
		d, err := xmltree.ParseLenient11WithBase(srcXML, baseDir)
		if err != nil {
			if pe, ok := err.(*xmltree.ParseError); ok {
				return nil, nil, &CompileError{Line: pe.Line, Col: pe.Col, Msg: "source: " + pe.Msg}
			}
			return nil, nil, err
		}
		if e.SourceURI != "" {
			d.Base = e.SourceURI
		}
		// Validation FIRST, whitespace stripping second. XSLT 3.0 §4.4's
		// stripping rules are written against the tree the processor is
		// handed — a PSVI-derived tree when the host validates the source —
		// and two of them read the type annotation directly: an element with
		// simple content keeps its whitespace whatever xsl:strip-space says
		// (see simpleContentTyped), and an element-only one loses it whatever
		// xsl:preserve-space says. Stripping first also risks inventing
		// invalidity the spec explicitly warns about ("stripping a whitespace
		// text node from an element with simple content could ... cause the
		// minLength facet to be violated").
		if err := ss.validateSourceDocument(d, e.SourceValidation, e.SourceSchemas); err != nil {
			return nil, nil, err
		}
		ss.applyStripSpace(d)
		doc = d
	}
	res := newFileResolver(baseDir, ss)
	res.setCollections(e.Collections)
	res.setDocValidation(e.DocValidation, e.SourceSchemas)
	// paramSelects: e.ParamSelects was already being read by evalGlobal (see
	// its own doc comment there), but nothing ever copied it from Entry onto
	// the engine — every caller-supplied param with a non-trivial @select
	// (anything beyond a bare string literal, e.g. static-009a's
	// "xs:untypedAtomic(23)") silently fell all the way through to the
	// plain-string paramOverrides path instead, arriving as its own LITERAL
	// EXPRESSION TEXT rather than that text's evaluated value.
	eng := &engine{sheet: ss, doc: doc, globals: map[string]xpath.Object{}, baseDir: baseDir, resolver: res, now: time.Now().UTC(), principalSealedAt: -1, paramSelects: e.ParamSelects, outURI: e.BaseOutputURI, srcSchemas: e.SourceSchemas}
	if doc != nil && e.SourceStreamed {
		// Deliberately NOT eng.streamedRoots, which drives XTDE3362: that
		// error's own text ends "Implementations may raise this error but are
		// not required to do so, if they are capable of streaming documents
		// without imposing this restriction", and this engine builds the whole
		// tree for a host-supplied source, so a non-streamable accumulator
		// read on one is something it genuinely can evaluate
		// (accumulator-033s/034/036/042/043 all do exactly that over a
		// <source streaming="true"/> and expect it to work). The declaration
		// is recorded only for the consequence §10.3.6 states unconditionally.
		eng.hostStreamedDocs = map[*xmltree.Node]bool{doc: true}
	}
	if doc != nil && e.SourceURI != "" {
		// Seed the resolver's doc cache under the SAME resolved path the
		// primary source's base-uri already carries (set above where doc was
		// parsed), so fn:doc(fn:document-uri(.)) returns this identical node
		// instead of a second, freshly-reparsed copy (accessor-008/036).
		res.docs[res.path(e.SourceURI)] = doc
	}
	// Entry.ParamSelects (typed, XPath-expression parameter values) must be in
	// place BEFORE the globals are resolved — evalGlobal consults it first,
	// falling back to the untypedAtomic Params text. Without this assignment
	// the typed form was silently never used (assert-010, key-085a/b).
	eng.paramSelects = e.ParamSelects

	ctxNode := doc
	// InitialItems: the host handed over the initial context item / match
	// selection as real XDM items (fn:transform's source-node /
	// initial-match-selection). A node keeps its identity; a non-node is
	// modelled the same way xsl:for-each models an atomic context item.
	var initialNodes xpath.NodeSet
	for _, it := range e.InitialItems {
		if nd, ok := it.(*xmltree.Node); ok {
			initialNodes = append(initialNodes, nd)
			continue
		}
		nd := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true,
			Value: xpath.ToString(xpath.FromItems([]xpath.Item{it}))}
		if tag, ok := xpath.ItemAtomTypeTag(it); ok {
			nd.TypeAnno = tag
		}
		initialNodes = append(initialNodes, nd)
	}
	if len(initialNodes) > 0 {
		ctxNode = initialNodes[0]
	}
	if e.ContextItem != "" {
		p, err := xpath.Parse(e.ContextItem)
		if err != nil {
			return nil, nil, err
		}
		v, err := eng.eval(p, nil, rt{node: doc, pos: 1, size: 1})
		if err != nil {
			return nil, nil, err
		}
		if ns, ok := xpath.ToNodeSet(v); ok {
			if len(ns) > 0 {
				ctxNode = ns[0]
			} else {
				// The environment's initial-context-item @select resolved to
				// the empty sequence: there is NO initial context item (not a
				// silent fallback to the whole document) — a later "." use is
				// then XPDY0002, as required (strip-space-023: @select
				// targets a whitespace text node that xsl:strip-space
				// removed from the tree first).
				ctxNode = nil
			}
		}
	}
	// Global variables see the global context item — the node actually
	// supplied (or nothing, when a supplied whitespace text node was stripped:
	// strip-space-023's select="." must then be XPDY0002), not the document
	// it came from — so the context item must be settled before they are
	// evaluated.
	eng.globalCtx = ctxNode
	if e.GlobalContextItem != nil {
		if nd, ok := e.GlobalContextItem.(*xmltree.Node); ok {
			eng.globalCtx = nd
		} else {
			gc := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true,
				Value: xpath.ToString(xpath.FromItems([]xpath.Item{e.GlobalContextItem}))}
			if tag, ok := xpath.ItemAtomTypeTag(e.GlobalContextItem); ok {
				gc.TypeAnno = tag
			}
			eng.globalCtx = gc
		}
	}
	if err := eng.resolveGlobals(e.Params); err != nil {
		return nil, nil, err
	}
	// xsl:global-context-item use="required" makes supplying a global context
	// item a precondition of running the stylesheet at all (glob-cxt-item-012).
	if ss.globalCtxUse == "required" && ctxNode == nil {
		return nil, nil, fmt.Errorf("err:XTDE3086: the stylesheet requires a global context item, but none was supplied")
	}
	r := rt{node: ctxNode, pos: 1, size: 1}
	resultRoot := &xmltree.Node{Kind: xmltree.KindDocument}
	// An item-separator in force means sequence normalization must keep the
	// result's top-level ITEMS discrete (they are joined by the separator at
	// serialization time, not by the default single space during
	// construction) — see Output.ItemSeparator.
	if ss.output.HasItemSeparator {
		resultRoot.NoAtomicMerge = true
	}
	prepareRawRoot(resultRoot, ss.output)
	eng.principalRoot = resultRoot

entryKind:
	switch {
	case e.Template != "":
		// An initial template name may arrive already expanded as a Clark name
		// ("{uri}local") when the host resolved its prefix in ITS OWN
		// namespace scope — which is not the stylesheet's, so the raw lexical
		// name must not be used to match (call-template-0105: the invocation's
		// my: and the stylesheet's my: are different namespaces).
		tmpl, ok := ss.named[e.Template]
		if strings.HasPrefix(e.Template, "{") {
			tmpl, ok = ss.namedClark[e.Template]
		}
		if !ok {
			// A no-namespace name looks identical lexically and in Clark form,
			// but the stylesheet may have written it as an EQName
			// (name=" Q{}temp ") and registered it only under its expanded
			// form (call-template-0109).
			tmpl, ok = ss.namedClark[e.Template]
		}
		if ok && (e.RequirePublicEntry || ss.isPackage) && tmpl.visibility != "public" && tmpl.visibility != "final" &&
			!ss.exposedIgnored["template#"+clarkName(resolveQName(tmpl.el, tmpl.name))] {
			// Reached across a package boundary (RequirePublicEntry), or the
			// compiled module is itself a genuine xsl:package (whose named
			// templates default to private per XSLT 3.0's component
			// visibility table — package-001a: an explicit initial-template
			// name pointing at a template with no stated visibility must
			// raise XTDE0040, exactly as fn:transform's package-based
			// invocation already requires via RequirePublicEntry): a private
			// named template is not a legitimate entry point. Exempted when
			// an xsl:expose this engine could not honor (forwards-compatible-
			// dropped, see recordIgnoredExpose) named exactly this template —
			// forwards-011's "go" template has no visibility of its own, but
			// IS named by a version-23.0 xsl:expose asking for public
			// visibility; this engine cannot apply that grant, but it must
			// not enforce the bare default against it either, since a
			// correctly-behaving processor that DID honor the expose would
			// not.
			ok = false
		}
		if !ok {
			return nil, nil, fmt.Errorf("err:XTDE0040: no template named %q", e.Template)
		}
		callerParams, perr := entryTemplateParams(e.TemplateParams)
		if perr != nil {
			return nil, nil, perr
		}
		tunnelIn, terr := eng.tunnelFrom(callerParams, r)
		if terr != nil {
			return nil, nil, terr
		}
		eng.captureEntryValue = true
		err := eng.invokeTemplate(tmpl, r, resultRoot, callerParams, tunnelIn, r)
		eng.captureEntryValue = false
		if err != nil {
			return nil, nil, err
		}
	case e.Function != "":
		fd := eng.lookupInitialFunction(e.Function, len(e.FuncArgs))
		if fd == nil {
			return nil, nil, fmt.Errorf("err:XTDE0041: no function %q/%d", e.Function, len(e.FuncArgs))
		}
		var args []xpath.Object
		for _, sel := range e.FuncArgs {
			p, err := xpath.Parse(sel)
			if err != nil {
				return nil, nil, err
			}
			av, err := eng.eval(p, nil, r)
			if err != nil {
				return nil, nil, err
			}
			args = append(args, av)
		}
		res, _, err := eng.callUserFunc(fd, args, &evalEnv{eng: eng, current: ctxNode})
		if err != nil {
			return nil, nil, err
		}
		eng.entryValue = res
		if ns, ok := xpath.ToNodeSet(res); ok {
			for _, node := range ns {
				deepCopyInto(node, resultRoot)
			}
		} else if res != nil {
			resultRoot.Append(xmltree.NewText(xpath.ToString(res)))
		}
	default: // initial mode (named or unnamed)
		callerNamedMode := e.Mode != "" && e.Mode != "#default"
		if !callerNamedMode {
			// No initial mode named by the caller: use the stylesheet's
			// DEFAULT initial mode (XSLT 3.0 [xsl:]default-mode on the
			// principal module's root element; "" — unnamed — when absent).
			// The XTDE0045 "must be a declared mode" rule below applies only
			// to a mode the CALLER asked for — the stylesheet's own default
			// mode needs no separate declaration (mode-1803).
			e.Mode = ss.defaultMode
		}
		if callerNamedMode && e.Mode != "#unnamed" && e.Mode != ss.defaultMode &&
			!strings.Contains(e.Mode, ":") && !ss.knownModeName(e.Mode) {
			// XTDE0045: the initial mode named on invocation must be a mode
			// the stylesheet actually declares — either via xsl:mode or as
			// the explicit @mode of some template. A template's mode="#all"
			// matches every mode dynamically but does not itself DECLARE any
			// particular mode name, so it does not make an arbitrary initial
			// mode name valid (initial-mode-002, per WG bug 3690). Only a
			// bare (unprefixed) initial-mode name is checked here: a
			// prefixed one needs resolving against the CATALOG XML's own
			// namespace scope (not the stylesheet's), which this entry point
			// does not have available, so it is left unvalidated rather than
			// risk a false XTDE0045 on a real match under a different prefix.
			return nil, nil, fmt.Errorf("err:XTDE0045: initial mode %q is not declared in the stylesheet", e.Mode)
		}
		if e.Mode == "#unnamed" {
			// "#unnamed" is the pseudo-name for the actual unnamed mode ("");
			// every subsequent lookup (the visibility check right below, and
			// matchingTemplates/matchesMode via applyToNodes further down)
			// compares against the real key, exactly as an ordinary
			// xsl:apply-templates/mode="#unnamed" already resolves through
			// resolveMode. Left as the literal token, ss.modeDef("#unnamed")
			// would silently miss the real unnamed mode's declared visibility
			// and matchesMode("#unnamed") would never equal a template's own
			// (unset, hence "") @mode — package-001d/e ("initial mode #unnamed
			// does NOT need to be public") reached "no template matched" (the
			// built-in rule) with the token left unresolved.
			e.Mode = ""
		}
		if callerNamedMode && e.Mode != "" {
			// XTDE0045 also covers a NAMED mode the caller can reference but is
			// not allowed to ENTER: XSLT 3.0's xsl:mode/@visibility table states
			// "A named mode is not eligible to be used as the initial mode if
			// its visibility is private" — the UNNAMED mode (e.Mode == "", after
			// the #unnamed normalization above) carries no such restriction
			// regardless of its own visibility (package-001k explicitly declares
			// the unnamed mode private and still uses #unnamed as its initial
			// mode successfully), so it is excluded from this check entirely.
			md, wasDeclared := ss.modes[e.Mode]
			if !wasDeclared {
				md = &ModeDef{name: e.Mode, onNoMatch: "text-only-copy"}
			}
			switch md.visibility {
			case "private", "abstract":
				return nil, nil, fmt.Errorf("err:XTDE0045: initial mode %q has visibility %q; an entry-point mode must be public or final",
					e.Mode, md.visibility)
			case "":
				// The same table's default value for an EXPLICIT xsl:mode
				// declaration's @visibility is "private" — but only inside a
				// genuine xsl:package (an "implicit package" wrapping an
				// ordinary xsl:stylesheet/xsl:transform gets none of this
				// enforcement, for backward compatibility with pre-3.0
				// processors that had no visibility concept at all —
				// mode-1801/1902 name an unstated-visibility mode as their
				// initial mode and must still succeed), and only when the
				// mode actually HAS an xsl:mode declaration: a mode named
				// only implicitly (via a template's own @mode, with no
				// xsl:mode element at all) stays exempt regardless of
				// package-ness, per WG bug #29827 (package-001f: "implicitly
				// declared mode is private but is nonetheless an eligible
				// initial mode").
				if wasDeclared && ss.isPackage {
					return nil, nil, fmt.Errorf("err:XTDE0045: initial mode %q has no declared visibility (defaults to private inside xsl:package); an entry-point mode must be public or final", e.Mode)
				}
			}
		}
		var sel xpath.NodeSet
		switch {
		case len(initialNodes) > 0:
			// Host-supplied REAL XDM items (Entry.InitialItems, e.g.
			// fn:transform's source-node/initial-match-selection options) —
			// the caller already has the value, a node must keep its
			// identity, so this wins over both ContextItem and MatchSelection.
			sel = initialNodes
		case e.MatchSelection != "":
			// XSLT 3.0 "initial match selection": the caller supplies a whole
			// SEQUENCE to apply templates to, not just one context item — and,
			// unlike the plain context-item entry below, this is evaluated
			// whether or not a global context item exists at all (package-001d/
			// e: <initial-mode select="42"/> with no source document supplies
			// its own self-contained sequence and needs no context node —
			// XTDE0044 must not fire just because ctxNode is absent here).
			p, err := xpath.Parse(e.MatchSelection)
			if err != nil {
				return nil, nil, err
			}
			v, err := eng.eval(p, nil, r)
			if err != nil {
				return nil, nil, err
			}
			for _, it := range xpath.Flatten(xpath.Items(v)) {
				if nd, isNode := it.(*xmltree.Node); isNode {
					sel = append(sel, nd)
					continue
				}
				// Same synthetic-node modelling as xsl:apply-templates over an
				// atomic sequence (see execApplyTemplates).
				nd := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true, Atomic: true,
					Value: xpath.ToString(xpath.FromItems([]xpath.Item{it}))}
				if tag, ok := xpath.ItemAtomTypeTag(it); ok {
					nd.TypeAnno = tag
				}
				sel = append(sel, nd)
			}
		default:
			if ctxNode == nil {
				// With no context item and no initial match selection, fall
				// back to the default-named initial template
				// (xsl:initial-template) if the stylesheet declares one — and,
				// inside a genuine xsl:package, only if it is actually usable
				// as an entry point (visibility public/final), exactly as an
				// EXPLICITLY named initial template already requires above
				// (package-001b: an xsl:initial-template with no stated —
				// hence default-private — visibility must raise XTDE0040
				// rather than silently run).
				if tmpl, ok := ss.namedClark[clark(NS, "initial-template")]; ok {
					if ss.isPackage && tmpl.visibility != "public" && tmpl.visibility != "final" {
						return nil, nil, fmt.Errorf("err:XTDE0040: no template named %q", "xsl:initial-template")
					}
					if err := eng.invokeTemplate(tmpl, r, resultRoot, nil, nil, r); err != nil {
						return nil, nil, err
					}
					break entryKind
				}
				return nil, nil, fmt.Errorf("err:XTDE0044: no context item for apply-templates entry")
			}
			sel = xpath.NodeSet{ctxNode}
		}
		// Entry.TemplateParams are parameters supplied to the INITIAL TEMPLATE
		// RULE when the entry point is an initial mode, just as they are for an
		// initial named template; applyToNodes splits tunnel from non-tunnel
		// itself (initial-mode-004).
		modeParams, perr := entryTemplateParams(e.TemplateParams)
		if perr != nil {
			return nil, nil, perr
		}
		// Same capture as the named-template entry point above: an @as-typed
		// initial template rule's raw result becomes RunResult.Value, not
		// just its projection into resultRoot (fn:transform's delivery-format
		// "raw" needs the actual value — fn-transform-62/84 — for an
		// apply-templates/initial-match-selection entry exactly as much as
		// for a named one; noteEntryValue itself already restricts this to
		// the OUTERMOST rule via eng.depth==1, not a rule it in turn calls).
		eng.captureEntryValue = true
		err := eng.applyToNodes(sel, e.Mode, resultRoot, modeParams, r)
		eng.captureEntryValue = false
		if err != nil {
			return nil, nil, err
		}
	}
	if err := eng.checkPrincipalSealed(); err != nil {
		return nil, nil, err
	}
	return eng, resultRoot, nil
}

// lookupInitialFunction resolves an initial-function name (possibly an EQName or
// prefixed/plain local name) to a compiled xsl:function of the given arity.
// lookupInitialFunction finds the xsl:function a host names as the entry
// point. name is an EXPANDED name: a Clark name "{uri}local" (what a host
// produces after resolving the prefix in ITS OWN namespace scope), a
// Q{uri}local EQName, or a bare local name — which, like every unprefixed
// name in XPath, is in NO namespace and never in some default namespace
// (initial-function-102d/102e). Matching is by expanded name AND arity, so a
// same-local-name function in a different namespace is not the entry point
// (initial-function-102a). A still-lexical prefixed name (a host that could
// not expand it) keeps the older local-name fallback.
// localOf returns the local-name part of a lexical prefixed name ("p:local")
// or an already-expanded Clark name ("{uri}local"); a bare name is returned
// unchanged.
func localOf(name string) string {
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		return name[i+1:]
	}
	if i := strings.LastIndexByte(name, '}'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func (eng *engine) lookupInitialFunction(name string, arity int) *FuncDef {
	// The name arrives already expanded ("{uri}local" Clark form) when the
	// caller gave a prefixed name; an UNPREFIXED name means the no-namespace
	// function, never "any function with that local name" (initial-function-
	// 102d/102e pin that down). A "Q{uri}local" EQName is accepted too. A
	// still-lexical prefixed name (a host that could not expand it) keeps
	// the older local-name-only fallback.
	want := name
	if strings.HasPrefix(want, "Q{") {
		want = want[1:]
	}
	want = strings.TrimPrefix(want, "{}") // clark() writes no-namespace as the bare local name
	lexical := !strings.HasPrefix(want, "{") && strings.Contains(want, ":")
	for _, fd := range eng.sheet.functions {
		if len(fd.params) != arity {
			continue
		}
		// Only a PUBLIC or FINAL function is an eligible entry point: the
		// initial function is selected from outside the package, so a private
		// one is simply not there to be found — XTDE0041 (initial-function-905).
		if !funcPubliclyVisible(fd) {
			continue
		}
		if clarkName(fd.name) == want || (lexical && fd.name.Local == localOf(name)) {
			return fd
		}
	}
	return nil
}

func autoMethod(root *xmltree.Node) string {
	for _, c := range root.Children {
		if c.Kind == xmltree.KindElement {
			if strings.EqualFold(c.Name.Local, "html") {
				switch c.Name.Space {
				case "":
					return "html"
				case "http://www.w3.org/1999/xhtml":
					// output-0130: a root html element in the XHTML namespace
					// defaults to the xhtml output method, not xml.
					return "xhtml"
				}
			}
			return "xml"
		}
	}
	return "xml"
}

// transformInto runs the transformation and returns the engine and the
// principal result tree. tgt is optional (TransformFullTo passes one): it
// redirects the result to a writer instead of a string — variadic so the
// ordinary two call sites stay as they are.
func (ss *Stylesheet) transformInto(srcXML string, params map[string]string, baseDir string, tgt ...*outTarget) (*engine, *xmltree.Node, error) {
	doc, err := xmltree.ParseLenient11WithBase(srcXML, baseDir)
	if err != nil {
		if pe, ok := err.(*xmltree.ParseError); ok {
			return nil, nil, &CompileError{Line: pe.Line, Col: pe.Col, Msg: "source: " + pe.Msg}
		}
		return nil, nil, err
	}
	ss.applyStripSpace(doc)

	eng := &engine{
		sheet:    ss,
		doc:      doc,
		globals:  map[string]xpath.Object{},
		baseDir:  baseDir,
		resolver: newFileResolver(baseDir, ss),
		now:      time.Now().UTC(),

		principalSealedAt: -1,
		globalCtx:         doc,
	}
	if err := eng.resolveGlobals(params); err != nil {
		return nil, nil, err
	}

	resultRoot := &xmltree.Node{Kind: xmltree.KindDocument}
	if ss.output.HasItemSeparator {
		resultRoot.NoAtomicMerge = true
	}
	prepareRawRoot(resultRoot, ss.output)
	eng.principalRoot = resultRoot
	// An output target (TransformFullTo) writes the principal result instead of
	// returning it. Its chunk writer has to be attached HERE, before the first
	// instruction runs: a node appended before the attachment would never be
	// noticed, and would surface out of order at the end.
	if len(tgt) > 0 && tgt[0] != nil {
		eng.outWriter = tgt[0].w
		if tgt[0].sink != nil {
			eng.outSink = tgt[0].sink
			eng.outSink.Attach(resultRoot)
		}
	}
	if err := eng.applyToNodes(xpath.NodeSet{doc}, "", resultRoot, nil, rt{node: doc, pos: 1, size: 1}); err != nil {
		return nil, nil, err
	}
	if err := eng.checkPrincipalSealed(); err != nil {
		return nil, nil, err
	}
	return eng, resultRoot, nil
}

// checkPrincipalSealed reports XTDE1490 when the transform wrote to the
// principal result tree after an explicit empty-href xsl:result-document had
// already produced it in full — two final result trees for the same URI
// (result-document-1002).
func (eng *engine) checkPrincipalSealed() error {
	if eng.principalSealedAt < 0 || eng.principalRoot == nil {
		return nil
	}
	if len(eng.principalRoot.Children) > eng.principalSealedAt {
		return errAt(nil, "err:XTDE1490: content was written to the principal result tree after an xsl:result-document had already produced it")
	}
	// A child-count comparison alone misses text appended AFTER sealing that
	// merges into the already-sealed LAST child instead of adding a new one
	// (appendLiteralText merges adjacent literal-text runs) — capture that
	// child's length at seal time too, so growth there is caught as well.
	if eng.principalSealedLastText != nil && len(eng.principalSealedLastText.Value) > eng.principalSealedTextLen {
		return errAt(nil, "err:XTDE1490: content was written to the principal result tree after an xsl:result-document had already produced it")
	}
	return nil
}

// outTarget redirects a run's principal result to a writer instead of a
// string. sink, when set, drains the result tree as it is built (bounded
// memory); without it the tree is built in full and written at the end.
type outTarget struct {
	w    io.Writer
	sink *xmltree.ChunkWriter
}

// engine carries transformation state.
type engine struct {
	// outWriter/outSink are set only by TransformFullTo; see outTarget.
	outWriter io.Writer
	outSink   *xmltree.ChunkWriter

	sheet          *Stylesheet
	doc            *xmltree.Node
	globals        map[string]xpath.Object
	scopes         []map[string]xpath.Object
	funcMemo       map[string]memoResult                      // results of xsl:function new-each-time="no" / cache="yes" calls
	keyIndex       map[keyCacheKey]map[string][]*xmltree.Node // lazily built key indexes (per key name and document root)
	keyBuilding    map[string]bool                            // keys currently being indexed (circularity -> XTDE0640)
	globalDefs     map[string]*VarDef                         // global declarations for on-demand evaluation
	globalBusy     map[string]bool                            // globals being evaluated (circularity -> XTDE0640)
	paramOverrides map[string]string                          // externally-supplied top-level param values, keyed by local name
	paramSelects   map[string]string                          // externally-supplied top-level param values as XPath expressions (Entry.ParamSelects)
	evaluateDepth  int                                        // >0 while evaluating an xsl:evaluate @xpath expression
	pendingErr     error                                      // error raised in a context without an error channel
	regexGroups    [][]string                                 // xsl:analyze-string group stack
	baseDir        string                                     // workspace dir for resolving/writing files
	resolver       xpath.ResourceResolver                     // fn:doc / unparsed-text / document() resolver
	srcSchemas     []string                                   // Entry.SourceSchemas: the host's schema documents for SOURCE validation (see applySourceDocValidation)
	messages       []string                                   // xsl:message output
	secondary      []SecondaryDoc                             // xsl:result-document output
	rdOutput       *Output                                    // serialization override from an empty-href xsl:result-document
	principalRoot  *xmltree.Node                              // the true top-level result tree (a nested/empty-href xsl:result-document always redirects here)
	// grouping/merge runtime context (set during for-each-group / merge):
	curGroup        xpath.NodeSet
	curGroupOK      bool
	curKey          xpath.Object
	curKeyOK        bool // current-grouping-key() is only defined for group-by/group-adjacent (XTDE1071)
	curMergeGroup   xpath.NodeSet
	curMergeKey     xpath.Object
	inMerge         bool            // current-merge-group/-key() are only valid inside an xsl:merge-action (XTDE3480/XTDE3510)
	curMergeSources map[string]bool // the enclosing xsl:merge's source names (XTDE3490)
	// inStreamable is set while the body of a DECLARED-streamable construct
	// (today: xsl:source-document streamable="yes") is executing, whether or
	// not the bounded-memory path was actually taken — the declaration is what
	// the rule turns on, not the execution strategy. XSLT 3.0 §14.2.1/§14.2.2:
	// an invocation construct inside such a body sets the current group and
	// grouping key to ABSENT in the callee, which is what makes an
	// xsl:for-each-group's streamability statically assessable (§19.8.4.19).
	inStreamable bool
	// errEl records the stylesheet element whose XPath evaluation raised the
	// dynamic error now propagating, for $err:module / $err:line-number /
	// $err:column-number in xsl:catch. eng.eval sets it on the INNERMOST
	// failing evaluation only (it never overwrites a value already set), and
	// tryCatch.exec is the only reader: it clears it before running a try body
	// and restores the outer value afterwards, so nested xsl:try each report
	// their own failure site.
	errEl *xmltree.Node
	// accOrigin maps a node of an accumulator-preserving COPY back to the
	// original node it was copied from — see recordAccOrigin for the
	// invariant.
	accOrigin map[*xmltree.Node]*xmltree.Node
	// curMergeGroupBySource splits the current merge group by the NAME of the
	// xsl:merge-source each item came from, for current-merge-group($source)
	// (merge-047). Set together with curMergeGroup and cleared with it.
	curMergeGroupBySource map[string]xpath.NodeSet
	accCache              map[string]any // accumulator state (owned by instr_accumulator.go)
	scratch               map[string]any // generic per-run scratch for instruction state
	// mode / template-rule chaining / tunnel params:
	curMode    string
	tunnel     map[string]xpath.Object // active tunnel parameters
	applyStack []*applyFrame           // for next-match / apply-imports
	depth      int                     // template/rule invocation depth (stack-overflow guard)
	steps      int64                   // instructions executed (runtime budget guard)
	// funcDepth > 0 while evaluating a called xsl:function's body: current-
	// merge-group()/current-merge-key() are only available directly within an
	// xsl:merge-action, not from a function called from it — even one called
	// from inside the merge-action itself (merge-100/101: XTDE3480/XTDE3510).
	funcDepth int
	// tempOutputDepth > 0 while evaluating an xsl:key's use/body: XSLT 3.0
	// "temporary output state" (5.7.1) forbids xsl:result-document there
	// (err:XTDE1480 — result-document-1131/1137/1139..1144), unlike the
	// principal transformation. A counter (not a bool) so a key that itself
	// triggers another key's evaluation stays correctly flagged throughout.
	tempOutputDepth int
	// outURI is the base output URI (Entry.BaseOutputURI); outURIStack holds
	// the resolved URI of each xsl:result-document currently being written,
	// innermost last. fn:current-output-uri() reports the innermost.
	outURI      string
	outURIStack []string
	// patternDepth > 0 while a match/count/grouping PATTERN is being
	// evaluated. A pattern is not part of writing any result document, so
	// fn:current-output-uri() is absent inside one (current-output-uri-008).
	// It is an engine counter rather than an xpath.Context flag because the
	// pattern matcher builds fresh sub-contexts for each predicate, which
	// would drop a flag.
	patternDepth int
	// streamedRoots holds the root of each document the stylesheet asked to
	// process with xsl:source-document streamable="yes". The tree is built in
	// full either way; the flag exists only so the rules that are conditional
	// on streaming (XTDE3362) can still be enforced.
	streamedRoots map[*xmltree.Node]bool
	// hostStreamedDocs holds a document the HOST declared streamed
	// (Entry.SourceStreamed). Unlike streamedRoots it carries no XTDE3362
	// consequence — see transformEntryInto — and exists for §10.3.6's rule
	// that a focus over a streamed node cannot be saved in a function item.
	hostStreamedDocs map[*xmltree.Node]bool
	// globalCtx is the global context item's node (nil when absent): the
	// context in which global variables and parameters are evaluated.
	globalCtx *xmltree.Node
	// homeDocs caches, per stylesheet module, the tree document('')/doc('')
	// hands back for it — see homeDoc.
	homeDocs map[*xmltree.Node]*xmltree.Node
	// accApplicable restricts, per document root, the accumulators
	// applicable to that tree (XSLT 3.0 §18.2.4): the Clark names listed by
	// the xsl:source-document / xsl:merge-source use-accumulators attribute
	// that read it. A root with no entry keeps every accumulator (documents
	// from fn:doc, temporary trees, the principal source — the modes-based
	// rule for the latter is acc2checkModeApplies). See restrictAccumulators.
	accApplicable map[*xmltree.Node]map[string]bool
	// mrgUndo stacks the restrictAccumulators undo funcs of the documents
	// the currently running xsl:merge instructions read (see mrgInstr.exec).
	mrgUndo []func()
	// entryValue is the raw sequence an initial-function entry point returned
	// (RunResult.Value).
	entryValue xpath.Object
	// captureEntryValue is set while the INITIAL named template runs, so its
	// @as-typed result is recorded in entryValue as a real sequence.
	captureEntryValue bool
	// secondaryDocDepth > 0 while executing the body of an xsl:result-document
	// with a non-empty (genuinely secondary) href — an empty-href
	// xsl:result-document nested there legitimately redirects back to the
	// principal tree (result-document-0205). Outside any secondary document
	// (depth 0), an empty-href xsl:result-document is a redundant second
	// claim on the principal tree's URI (err:XTDE1490 — error-1490c).
	secondaryDocDepth int
	// principalSealedAt records how many children the principal result tree
	// had when an explicit empty-href xsl:result-document finished writing it
	// (-1: that never happened). Content appended to the principal tree after
	// that point is a SECOND final result tree for the same URI — XTDE1490,
	// raised by checkPrincipalSealed once the transform is over
	// (result-document-1002).
	principalSealedAt int
	// principalSealedLastText/principalSealedTextLen close a gap in the
	// principalSealedAt child-count check: appendLiteralText merges an
	// adjacent literal-text run into the tree's existing LAST child instead
	// of adding a new one, so content appended after sealing can grow that
	// same already-sealed text node without changing the child count at all.
	// Captured together with principalSealedAt when that last child is a
	// plain (non-atomic) text node; nil/0 otherwise.
	principalSealedLastText *xmltree.Node
	principalSealedTextLen  int
	// now is the ONE current instant for this whole transformation
	// (fn:current-dateTime()/-date()/-time() must be stable throughout —
	// date-020), stamped once at engine construction and threaded into every
	// xpath.Context this engine builds.
	now time.Time
}

// maxTemplateDepth bounds template/rule recursion so a pathological or
// non-terminating stylesheet errors out (XTDE) instead of overflowing the
// goroutine stack (which is an unrecoverable fatal error). Legitimate
// stylesheets do recurse deeply — the classic "reverse a string one character
// at a time" idiom is linear in the input length (call-template-1001/1002
// recurse 500 and 1000 levels respectively) — so the ceiling has to be well
// clear of that while still bounding a runaway.
const maxTemplateDepth = 10000

// maxSteps bounds the number of instructions a single transform may execute, so
// a non-terminating stylesheet errors out rather than hanging the test run.
const maxSteps = 20_000_000

// applyFrame records the matched-template candidate list for the node currently
// being processed, enabling xsl:next-match and xsl:apply-imports.
type applyFrame struct {
	node  *xmltree.Node
	mode  string
	cands []*Template // best-first
	idx   int         // index of the currently-executing candidate
}

// SecondaryDoc is a document produced by xsl:result-document.
type SecondaryDoc struct {
	Href    string
	Content string
	Method  string
	// Root is the constructed result tree behind Content, kept so a caller
	// that wants the nodes rather than the serialization can have them —
	// fn:transform's delivery-format="document"/"raw" (transform-008/009).
	Root *xmltree.Node
}

// RunResult is the full outcome of a transformation.
type RunResult struct {
	Output    string
	Method    string
	Messages  []string
	Secondary []SecondaryDoc
	// Root is the principal result tree behind Output — see SecondaryDoc.Root.
	Root *xmltree.Node
	// Value is the RAW sequence an initial-function entry point returned,
	// before it was projected into the result tree. Output can only show that
	// projection (a two-item sequence becomes one joined string), so a caller
	// that needs the actual items — their count, their types, their identity —
	// must read them here (initial-function-002/100e assert on exactly that).
	// Nil for every other entry point.
	Value xpath.Object
}

// rt is the dynamic runtime position.
type rt struct {
	node    *xmltree.Node
	pos     int
	size    int
	noFocus bool // true when there is definitely no context item (e.g. entering an xsl:function body) — "." there is XPDY0002, not an empty sequence
	// item, when non-nil, is a context item that is NEITHER a node NOR an
	// atomic value — a function, map or array delivered by a general sequence
	// to xsl:for-each. Atomic items keep the older, cheaper synthetic-text-node
	// model (see execForEach); those three kinds cannot be stringified into a
	// text node without losing their identity, so they travel here instead and
	// are handed straight to xpath.Context.CtxItem, where "." yields the item.
	// INVARIANT: node is nil whenever item is non-nil.
	item xpath.Item
}

func (eng *engine) resolveGlobals(params map[string]string) error {
	eng.paramOverrides = params
	// Import precedence: when several modules declare a global variable/param
	// with the same expanded name, the highest-precedence declaration wins —
	// even when a lower-precedence module is the one that references it.
	// ss.globals is in ascending precedence order (ascending compile order,
	// see CompileFrom), so a plain last-write-wins pass picks the winner.
	eng.globalDefs = map[string]*VarDef{}
	for _, vd := range eng.sheet.globals {
		eng.globalDefs[clark(vd.name.Space, vd.name.Local)] = vd
	}
	// Evaluate each name's winning declaration in its natural (ascending)
	// declaration order, skipping any definition that import precedence has
	// shadowed. Keeping ascending order (rather than reversing it) preserves
	// same-module top-to-bottom dependency order (e.g. a global variable
	// whose select expression reads an earlier xsl:param) — reversing that
	// would force such variables through the on-demand forward-reference path
	// for no benefit, since shadowed definitions are skipped outright here.
	for _, vd := range eng.sheet.globals {
		key := clark(vd.name.Space, vd.name.Local)
		if eng.globalDefs[key] != vd {
			continue // shadowed by a higher import-precedence declaration
		}
		if _, done := eng.globals[key]; done {
			continue // already evaluated on demand (forward reference)
		}
		val, err := eng.evalGlobal(key, vd)
		if err != nil {
			return err
		}
		eng.globals[key] = val
	}
	return nil
}

// evalGlobal evaluates a global variable with circularity detection (XTDE0640).
// A top-level param with an externally-supplied value short-circuits straight
// to that value (coerced to its declared @as type, if any) — this also
// applies when a global is pulled in early via the on-demand forward-reference
// path in lookupVar, so the external override is never bypassed regardless of
// which global happens to be evaluated first.
func (eng *engine) evalGlobal(key string, vd *VarDef) (xpath.Object, error) {
	if vd.hasStatic {
		return vd.staticVal, nil
	}
	if vd.isParam {
		if sel, ok := eng.paramSelects[vd.name.Local]; ok {
			if p, perr := xpath.Parse(sel); perr == nil {
				if v, verr := p.Eval(&xpath.Context{NoFocus: true}); verr == nil {
					if vd.as != "" {
						if cv, _ := xpath.CoerceToDeclaredTypeCtx(vd.as, v, asTypeCtx(vd.el)); cv != nil {
							v = cv
						}
					}
					return v, nil
				}
			}
		}
		if pv, ok := eng.paramOverrides[vd.name.Local]; ok {
			// A supplied param arrives as xs:untypedAtomic — exactly like any
			// other externally-sourced text (an attribute, a command-line
			// value) — not the concrete xs:string a bare Go string is
			// otherwise treated as (itemAtomType): a value comparison
			// ('gt'/'lt'/…) against it must be free to cast to whichever
			// type the OTHER operand needs (key-085a/b: a key match pattern
			// "string-length(@name) gt $min" needs $min to cast to a NUMBER,
			// which xs:string could never do — untypedAtomic can). With an
			// @as it is then coerced to the declared type so typed use
			// (arithmetic/comparison) sees the right type (static-010a:
			// xs:integer 541).
			v := xpath.Object(xpath.NewUntyped(pv))
			if vd.as != "" {
				if cv, _ := xpath.CoerceToDeclaredTypeCtx(vd.as, v, asTypeCtx(vd.el)); cv != nil {
					v = cv
				}
			}
			return v, nil
		}
		if vd.requiredParam {
			// No caller-supplied value for a required global parameter —
			// either explicit required="yes", or an implicitly-required one
			// (compileVarDef: an @as with no select/content whose declared
			// type does not admit the empty-sequence default) — is a
			// non-recoverable dynamic error (error-0610d).
			return nil, errAt(vd.el, "err:XTDE0700: required parameter $%s was not supplied", vd.name.Local)
		}
	}
	if eng.globalBusy == nil {
		eng.globalBusy = map[string]bool{}
	}
	if eng.globalBusy[key] {
		return nil, errAt(vd.el, "err:XTDE0640: circular reference to global variable $%s", vd.name.Local)
	}
	eng.globalBusy[key] = true
	defer delete(eng.globalBusy, key)
	return eng.evalVarDef(vd, rt{node: eng.globalCtx, pos: 1, size: 1, noFocus: eng.globalCtx == nil})
}

// --- variable scope ---------------------------------------------------------

func (eng *engine) pushScope() { eng.scopes = append(eng.scopes, map[string]xpath.Object{}) }
func (eng *engine) popScope()  { eng.scopes = eng.scopes[:len(eng.scopes)-1] }

func (eng *engine) bindVar(name xmltree.Name, v xpath.Object) {
	if len(eng.scopes) == 0 {
		eng.pushScope()
	}
	eng.scopes[len(eng.scopes)-1][clark(name.Space, name.Local)] = v
}

func (eng *engine) lookupVar(key string) (xpath.Object, bool) {
	return eng.lookupVarIn(eng.scopes, key)
}

// varsResolvable reports whether every local name in refs names SOME variable
// visible right now — any local scope frame or any global, whatever its
// namespace (see localVar.refs).
func (eng *engine) varsResolvable(refs []string) bool {
	localOf := func(key string) string {
		if i := strings.LastIndexByte(key, '}'); i >= 0 {
			return key[i+1:]
		}
		return key
	}
	for _, ref := range refs {
		found := false
		for i := len(eng.scopes) - 1; i >= 0 && !found; i-- {
			for key := range eng.scopes[i] {
				if localOf(key) == ref {
					found = true
					break
				}
			}
		}
		for key := range eng.globalDefs {
			if found {
				break
			}
			if localOf(key) == ref {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// lookupVarIn resolves a variable against an explicit scope-frame chain
// (innermost last) instead of the engine's own live eng.scopes, falling
// through to globals exactly like lookupVar. An inline function (function(){…})
// created inside a template/xsl:function body must go on seeing the local
// variables in scope at its POINT OF CREATION even after that scope's frame
// has been popped by the time the closure is actually called (function-0004:
// a closure returned from an xsl:function referencing the function's own
// param) — eng.eval snapshots eng.scopes into the evalEnv it builds, and the
// closure created by evalInlineFunc captures that evalEnv by pointer, so the
// frozen snapshot (not the live, by-then-popped stack) is what it sees.
func (eng *engine) lookupVarIn(scopes []map[string]xpath.Object, key string) (xpath.Object, bool) {
	for i := len(scopes) - 1; i >= 0; i-- {
		if v, ok := scopes[i][key]; ok {
			return v, true
		}
	}
	if v, ok := eng.globals[key]; ok {
		return v, true
	}
	// Forward reference to a global not yet evaluated: evaluate on demand.
	// A circular reference has no error channel here, so it is stashed on the
	// engine (consumed by eng.eval / keyIndexOn).
	if vd, ok := eng.globalDefs[key]; ok {
		v, err := eng.evalGlobal(key, vd)
		if err != nil {
			if eng.pendingErr == nil {
				eng.pendingErr = err
			}
			return nil, false
		}
		eng.globals[key] = v
		return v, true
	}
	return nil, false
}

func clark(uri, local string) string {
	if uri == "" {
		return local
	}
	return "{" + uri + "}" + local
}

// --- expression evaluation env ---------------------------------------------

type evalEnv struct {
	eng     *engine
	el      *xmltree.Node // stylesheet element for namespace context
	current *xmltree.Node // the current node (for current())
	// scopes, when non-nil, is a snapshot of eng.scopes taken when this
	// evalEnv was built (eng.eval), frozen against later eng.popScope calls —
	// see lookupVarIn. Sites that build an evalEnv outside eng.eval (pattern
	// matching, key/accumulator/grouping evaluation, …) leave this nil and so
	// keep resolving against the live eng.scopes, as before.
	scopes []map[string]xpath.Object
	// focusNode is the XPath CONTEXT node at the point the function call being
	// dispatched appears — NOT the XSLT current node (e.current), which is what
	// current() returns. The two differ inside a path step or predicate:
	// "$v/w[1]/accumulator-after('x')" must read the accumulator at that w, not
	// at the template's current node. ResolveFunc sets it from the xpath.Context
	// immediately before each dispatch, so a callee reads the focus of ITS own
	// call site; it is nil for the sites that build an evalEnv outside eng.eval.
	focusNode *xmltree.Node
}

// asTypeCtx builds the minimal xpath.Context needed to resolve a prefixed
// name inside an @as sequence type (e.g. element(my:item)) against el's
// in-scope namespaces, for xpath.CoerceToDeclaredTypeCtx/MatchesSeqTypeCtx
// (as-0130: element(my:item)* needs the "my" binding from the stylesheet).
//
// An @as type is kept as unparsed SOURCE and parsed on demand, so this is also
// where its schema component names are resolved. SchemaTypes carries both
// halves that needs at once: the name lookup the parse uses, and the
// derivation resolver the resulting type test asks at match time
// (decl_import_schema.go); nil whenever no schema was imported.
func asTypeCtx(el *xmltree.Node) *xpath.Context {
	// DefaultElemNS matters here as much as in any other evaluation: an
	// UNPREFIXED ElementName inside element(N)/element(N,T) takes the default
	// element/type namespace (XPath 3.1 §2.5.5.3), which XSLT sets with
	// [xsl:]xpath-default-namespace. Without it an @as="element(base)*" under
	// xpath-default-namespace="..." could never match the very nodes the
	// select expression's own unprefixed name test just selected
	// (import-schema-202).
	return &xpath.Context{NS: &evalEnv{el: el}, DefaultElemNS: xpathDefaultNS(el),
		SchemaTypes: schemaTypesFor(el)}
}

func (e *evalEnv) ResolveNS(prefix string) (string, bool) {
	if prefix == "" {
		return "", true
	}
	return e.el.LookupPrefix(prefix)
}

func (e *evalEnv) ResolveVar(prefix, local string) (xpath.Object, bool) {
	uri := ""
	if strings.HasPrefix(prefix, "Q{") && strings.HasSuffix(prefix, "}") {
		// A braced-EQName reference $Q{uri}local arrives with the pseudo-prefix
		// "Q{uri}" (splitVarName); its namespace is explicit (variable-0119).
		uri = prefix[2 : len(prefix)-1]
	} else if prefix != "" {
		uri, _ = e.el.LookupPrefix(prefix)
	}
	if e.scopes != nil {
		return e.eng.lookupVarIn(e.scopes, clark(uri, local))
	}
	return e.eng.lookupVar(clark(uri, local))
}

func (e *evalEnv) ResolveFunc(prefix, local string, args []xpath.Object, ctx *xpath.Context) (xpath.Object, bool, error) {
	saved := e.focusNode
	if ctx != nil {
		e.focusNode = ctx.Node
	}
	defer func() { e.focusNode = saved }()
	return e.eng.callFunc(prefix, local, args, e, ctx)
}

func (eng *engine) eval(p *xpath.Parsed, el *xmltree.Node, r rt) (xpath.Object, error) {
	return eng.evalWith(p, el, r, evalOverride{})
}

// evalOverride carries static-context overrides for a DYNAMICALLY evaluated
// expression (xsl:evaluate, XSLT 3.0 §10.3), each applied only when its
// companion flag is set: @base-uri replaces the static base URI
// (evaluate-030), and @namespace-context replaces both the in-scope namespaces
// (by passing that node as el) and the default element namespace, which is
// then the namespace-context node's own default namespace rather than any
// xpath-default-namespace in the stylesheet (evaluate-020/027).
type evalOverride struct {
	base    string
	hasBase bool
	dns     string
	hasDNS  bool
	// noSchema withholds the stylesheet's in-scope schema components from the
	// evaluated expression. XSLT 3.0 §10.4 gives xsl:evaluate the imported
	// schema components ONLY when it declares schema-aware="yes"; without it
	// a type name from an imported schema must not resolve
	// (evaluate-012/013/014, whose expected error is XTDE3160).
	noSchema bool
}

// evalWith is eval with explicit static-context overrides.
func (eng *engine) evalWith(p *xpath.Parsed, el *xmltree.Node, r rt, ov evalOverride) (xpath.Object, error) {
	eng.depth++
	defer func() { eng.depth-- }()
	if eng.depth > maxTemplateDepth {
		return nil, fmt.Errorf("err:XTDE0040: evaluation recursion too deep (possible non-terminating stylesheet)")
	}
	env := &evalEnv{eng: eng, el: el, current: r.node, scopes: append([]map[string]xpath.Object{}, eng.scopes...)}
	var dns, base string
	if eng.sheet.hasXPathDefaultNS {
		dns = xpathDefaultNSWalk(el)
	}
	if eng.sheet.hasXMLBase {
		base = staticBaseFor(el)
	}
	if ov.hasBase {
		base = ov.base
	}
	if ov.hasDNS {
		dns = ov.dns
	}
	var coll func(a, b string) int
	if eng.sheet.hasDefaultCollation {
		coll = eng.ambientDefaultCollation(el)
	}
	// RootlessTree: true — '/' is XPDY0050 whenever the CURRENT context
	// node's own tree root isn't a document node (sequence-0135/0136: a
	// for-each over an @as="element()" variable's value is a detached,
	// parentless element — fragAsSequence extracts it without its scratch
	// document wrapper — so its own Root() is itself, not KindDocument).
	// This is unconditional (not gated on the CALLER's tree), unlike XSD's
	// own unrelated, always-true use of this same flag for its detached
	// assertion trees.
	// The context item for "." may come from either non-node-item carrier:
	// r.item (xsl:for-each directly over a map/array/function item, r.node
	// left nil — see forEachItems) or Node.RealItem (a non-node item attached
	// to a synthetic node wrapper elsewhere, e.g. for-each-group/xsl:map —
	// see attachRealItem). The two are mutually exclusive in practice (r.item
	// is only ever set together with r.node == nil, so realItemOf(r.node) is
	// nil whenever r.item is), but check r.item first regardless.
	ctxItem := r.item
	if ctxItem == nil {
		ctxItem = realItemOf(r.node)
	}
	schemaTypes := schemaTypesFor(el)
	if ov.noSchema {
		schemaTypes = nil
	}
	var bc10 bool
	if eng.sheet.hasBackwardsCompat && backwardsCompatRun() {
		bc10 = inBackwardsCompatScope(el)
	}
	v, err := p.Eval(&xpath.Context{Node: r.node, CtxItem: ctxItem, Pos: r.pos, Size: r.size, NoFocus: r.noFocus, Vars: env, NS: env, Funcs: env, ResolveNamedFunction: eng.resolveNamedFunction(env), Resolver: eng.resolver, DecimalFormats: eng.sheet.decFmts, DefaultElemNS: dns, BaseURI: base, HomeDoc: eng.homeDoc(rootOfNode(el)), DefaultCollation: coll, Now: eng.now, RootlessTree: true, HostPrefixesOnly: true, SchemaTypes: schemaTypes, BC10: bc10})
	if err != nil && eng.errEl == nil {
		eng.errEl = el
	}
	if eng.pendingErr != nil {
		err, eng.pendingErr = eng.pendingErr, nil
		return nil, err
	}
	return v, err
}

// resolveNamedFunction builds the xpath.Context.ResolveNamedFunction hook
// (see its own doc comment) for one evaluation: a closure over eng and this
// call's env (needed by callUserFunc for namespace/variable resolution
// inside the called function's own body) that looks up a compiled
// xsl:function by (namespace, local name, arity) and wraps it as a callable
// *xpath.Function — used by fn:function-lookup to find a user-declared
// function the built-in F&O catalog has no match for (function-lookup-001/
// 002). Visibility is deliberately NOT checked here: both a public and a
// private xsl:function are found this way from WITHIN the declaring
// stylesheet (visibility restricts cross-package/cross-module access, not
// lookup from inside the module that declares it).
func (eng *engine) resolveNamedFunction(env *evalEnv) func(ns, local string, arity int) (*xpath.Function, bool) {
	return func(ns, local string, arity int) (*xpath.Function, bool) {
		fd, ok := eng.sheet.functions[funcKey(ns, local, arity)]
		if !ok {
			return nil, false
		}
		fn := &xpath.Function{Arity: arity, NS: ns, Name: local, Call: func(args []xpath.Object) (xpath.Object, error) {
			v, _, err := eng.callUserFunc(fd, args, env)
			return v, err
		}}
		// An xsl:function declares the types of its parameters and result, so
		// the function item carries that signature and a typed function test
		// judges it by the XPath 3.1 subtype rules rather than by arity alone
		// (higher-order-functions-032/033/034). A parameter or result with no
		// @as is item()*, spelled as a nil entry.
		params := make([]*xpath.SeqType, arity)
		typed := true
		for i, p := range fd.params {
			if i >= arity {
				break
			}
			st, err := xpath.ParseSeqTypeStringSchemaAware(p.as, schemaLookupFor(p.el))
			if err != nil {
				typed = false
				break
			}
			params[i] = st
		}
		ret, err := xpath.ParseSeqTypeStringSchemaAware(fd.as, schemaLookupFor(fd.el))
		if err != nil {
			typed = false
		}
		if typed {
			fn.Typed, fn.Params, fn.Ret = true, params, ret
		}
		return fn, true
	}
}

// staticBaseFor computes the static base URI of a stylesheet element: its
// xml:base ancestor chain, falling back to its own module's retrieval
// location (xmltree.Node.Base, set on each module's Document root by
// CompileFrom/loadFile) when no xml:base attribute overrides it.
func staticBaseFor(el *xmltree.Node) string {
	if el == nil {
		return ""
	}
	return xpath.NodeBaseURI(el, "")
}

// xpathDefaultNS returns the in-scope xpath-default-namespace for expressions
// on a stylesheet element: the nearest ancestor-or-self carrying
// xpath-default-namespace (xsl: elements) or xsl:xpath-default-namespace
// (literal elements). Unprefixed element name tests then match that namespace.
func xpathDefaultNS(el *xmltree.Node) string {
	return xpathDefaultNSWalk(el)
}

func xpathDefaultNSWalk(el *xmltree.Node) string {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("xpath-default-namespace")
		} else {
			v, ok = cur.Attr(NS, "xpath-default-namespace")
		}
		if ok {
			return v
		}
	}
	return ""
}

// defaultCollationWalk returns the in-scope default-collation attribute value
// for expressions on a stylesheet element: the nearest ancestor-or-self
// carrying default-collation (xsl: elements) or xsl:default-collation
// (literal elements). "" means no ambient default is established (codepoint
// collation). The value may be a whitespace-separated list of candidate URIs
// (resolveDefaultCollation picks the first the engine supports).
func defaultCollationWalk(el *xmltree.Node) string {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("default-collation")
		} else {
			v, ok = cur.Attr(NS, "default-collation")
		}
		if ok {
			return v
		}
	}
	return ""
}

// ambientDefaultCollation resolves the in-scope default-collation to a
// comparator (nil = codepoint collation, either because none is established
// or because none of its candidate URIs is supported).
func (eng *engine) ambientDefaultCollation(el *xmltree.Node) func(a, b string) int {
	if !eng.sheet.hasDefaultCollation {
		return nil
	}
	return resolveDefaultCollation(defaultCollationWalk(el))
}

func resolveDefaultCollation(v string) func(a, b string) int {
	for _, uri := range strings.Fields(v) {
		if cmp, err := xpath.ResolveCollator(uri); err == nil {
			return cmp
		}
	}
	return nil
}

// canonKeyString returns s's canonical xsl:key index/lookup form under
// collURI ("" = codepoint, s unchanged): two strings collate equal under the
// collation iff their canonical forms are byte-identical, so a hash-map index
// keyed by this form is collation-aware.
func canonKeyString(s, collURI string) string {
	if collURI == "" {
		return s
	}
	keyer, err := xpath.ResolveCollationKeyer(collURI)
	if err != nil {
		return s
	}
	return string(keyer(s))
}

func (eng *engine) evalString(p *xpath.Parsed, el *xmltree.Node, r rt) (string, error) {
	v, err := eng.eval(p, el, r)
	if err != nil {
		return "", err
	}
	return xpath.ToString(v), nil
}

// --- variable values --------------------------------------------------------

func (eng *engine) evalVarDef(vd *VarDef, r rt) (xpath.Object, error) {
	if vd.sel != nil {
		v, err := eng.eval(vd.sel, vd.el, r)
		if err != nil {
			return nil, err
		}
		// Coerce a select-expression value to its declared @as type under the
		// function-conversion rules (numeric promotion, untypedAtomic cast),
		// so downstream arithmetic/comparison sees the typed value
		// (static-010: an xs:integer param arrives as an integer, not a
		// string). Body (RTF) values keep their node semantics below.
		//
		// It is a type error (XTTE0570) if the supplied value cannot be
		// converted (error-0570c/0570d, error-0600a: a param's own default
		// select value is checked the same way as an explicitly-supplied
		// one). This is raised only here — a direct XPath expression value,
		// analogous to xsl:function's single-instruction fast path — not for
		// the tree-construction body form below, where engine limitations in
		// reporting a constructed result's true shape (e.g. document-node
		// flattening) make a mismatch less trustworthy as a genuine error.
		if vd.as != "" {
			cv, ok := xpath.CoerceToDeclaredTypeCtx(vd.as, v, asTypeCtx(vd.el))
			if !ok {
				return nil, errAt(vd.el, "err:XTTE0570: supplied value of $%s does not match declared type %q", vd.name.Local, vd.as)
			}
			return cv, nil
		}
		return v, nil
	}
	if len(vd.body) == 0 {
		if vd.as != "" {
			// An @as-typed body that compiled to nothing (its only content was
			// insignificant whitespace, stripped like any other stylesheet
			// text) is the empty SEQUENCE — matched by a "?"/"*" occurrence —
			// not a one-item empty string (as-0129: @as="document-node()?").
			return xpath.Sequence{}, nil
		}
		return "", nil
	}
	// Fast path: a body of exactly one xsl:sequence[@select] contributes its
	// RAW evaluated value directly, bypassing the tree round-trip below. This
	// matters for a value that cannot survive being serialized to text and
	// re-atomized — notably xs:QName, whose untypedAtomic->xs:QName cast is
	// deliberately rejected (CastTo: only the xs:QName() constructor, with
	// its static prefix context, may produce one; K-SeqExprCast-71a/422/423)
	// — as-0111/0111b var33: <xsl:sequence select="xs:QName(...)"/> as
	// as="xs:QName" must keep the already-typed QName value, not round-trip
	// it through text. Mirrors callUserFunc's identical single-instruction
	// shortcut for xsl:function bodies.
	if vd.as != "" && len(vd.body) == 1 {
		if si, ok := vd.body[0].(*sequenceInstr); ok && si.sel != nil {
			v, err := eng.eval(si.sel, si.el, r)
			if err != nil {
				return nil, err
			}
			cv, ok := xpath.CoerceToDeclaredTypeCtx(vd.as, v, asTypeCtx(vd.el))
			if !ok {
				return nil, errAt(vd.el, "err:XTTE0570: supplied value of $%s does not match declared type %q", vd.name.Local, vd.as)
			}
			return cv, nil
		}
	}
	// Result-tree-fragment: execute body into a fresh root node. With @as the
	// body must yield a discrete SEQUENCE (NoAtomicMerge), not RTF text
	// content — see NoAtomicMerge's doc comment (sequence-0115). Base records
	// the base URI established by this instruction (its own xml:base, or an
	// ancestor's, in the stylesheet source) so freshly constructed content
	// with no xml:base of its own inherits it (base-uri-013/016/025/…), and
	// so a plain document-node value (@as absent) reports it directly for
	// fn:base-uri() (base-uri-028's second assertion).
	//
	// XSLT 3.0 "temporary output state" (5.7.1): a variable/param value built
	// from a sequence-constructor BODY forbids xsl:result-document (XTDE1480).
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: vd.as != "", KeepDocItems: vd.as != "", Base: xpath.NodeBaseURI(vd.el, ""), Ephemeral: true}
	eng.tempOutputDepth++
	err := eng.execSequence(vd.body, r, frag)
	eng.tempOutputDepth--
	if err != nil {
		return nil, err
	}
	// Give this now-finalized temporary tree real document-order numbering
	// (xmltree.AssignOrder's doc comment) — a variable's value is reachable
	// as a standalone tree/sequence that later XPath over it may compare or
	// combine (select-2016/2025/2037: "$var//* union $var/*" needs true
	// relative order to both sort AND dedupe the shared num1/num4/num5
	// nodes correctly; every freshly-constructed node otherwise ties at the
	// same zero order).
	xmltree.AssignOrder(frag)
	// With @as, the variable's value is the constructed SEQUENCE itself
	// (attribute nodes + children), not a document node wrapping it (XSLT 2.0+),
	// and — exactly like a @select value — that sequence is converted to the
	// declared type under the function-conversion rules: atomize node content
	// when the target is atomic, cast xs:untypedAtomic to the target type,
	// promote numerics (as-0105: @as="xs:untypedAtomic" over a text-only body
	// must atomize the constructed text node; as-0111/0111b/0802/0802b: a
	// body of xs:duration/xs:dateTime/… text must cast up to the typed atomic
	// value, not stay the literal text node).
	if vd.as == "" && (len(frag.Attrs) > 0 || len(frag.NS) > 0) {
		// With no @as the variable's value is a DOCUMENT node wrapping the
		// constructed content, and the content of a document node may not
		// contain an attribute or namespace node (error-0420b).
		return nil, errAt(vd.el, "err:XTDE0420: the content of a document node cannot contain an attribute or namespace node")
	}
	if vd.as != "" {
		seq := fragAsSequence(frag)
		cv, ok := xpath.CoerceToDeclaredTypeCtx(vd.as, seq, asTypeCtx(vd.el))
		if !ok {
			return nil, errAt(vd.el, "err:XTTE0570: supplied value of $%s does not match declared type %q", vd.name.Local, vd.as)
		}
		return cv, nil
	}
	// An untyped variable/param whose ENTIRE body reduces to one zero-length
	// text node (variable-0123: <xsl:value-of select="…"/> alone, evaluating
	// to "") is a document node with NO children, not one holding a lone
	// empty text child — but only in this exact single-empty-child shape;
	// several instructions' contributions (even if individually or jointly
	// empty) are a different, more established case (select-2301/2303,
	// xsl-document-0601) left untouched.
	if len(frag.Children) == 1 && len(frag.Attrs) == 0 && len(frag.NS) == 0 {
		if c := frag.Children[0]; c.Kind == xmltree.KindText && c.Value == "" {
			frag.Children = nil
		}
	}
	return xpath.NodeSet{frag}, nil
}

// fragAsSequence extracts the constructed SEQUENCE from a body executed into a
// temporary document root: its attribute, namespace, and child nodes/items, in
// construction order — as opposed to treating frag itself as a single
// result-tree-fragment/document-node value. This is how XSLT 2.0+ resolves an
// @as-typed xsl:variable's body, and how an xsl:function body ALWAYS resolves
// (a function's sequence constructor yields a sequence, never an RTF, whether
// or not it declares @as — copy-3702: a two-instruction function body such as
// xsl:copy-of followed by a conditional xsl:sequence must still return actual
// NODES, not their concatenated string-value).
func fragAsSequence(frag *xmltree.Node) xpath.Object {
	ns := make(xpath.NodeSet, 0, len(frag.Attrs)+len(frag.NS)+len(frag.Children))
	// A standalone attribute or namespace node (an xsl:attribute/xsl:namespace
	// with no enclosing element constructor) is an ordinary member of the
	// constructed sequence, but the tree keeps both kinds out-of-band, so they
	// have to be woven back in at the position they were built —
	// Node.SeqIndex records how many children preceded each one
	// (sequence-0102: text, attribute, comment, PI must stay in THAT order;
	// result-document-0304 likewise). Dropping frag.NS entirely, as an earlier
	// version did, silently turned every namespace-node variable into the
	// empty sequence (namespace-3005).
	emitOutOfBand := func(at int, last bool) {
		for _, a := range frag.Attrs {
			if a.SeqIndex == at || (last && a.SeqIndex > at) {
				ns = append(ns, a)
			}
		}
		for _, a := range frag.NS {
			if a.SeqIndex == at || (last && a.SeqIndex > at) {
				ns = append(ns, a)
			}
		}
	}
	for i, c := range frag.Children {
		emitOutOfBand(i, false)
		ns = append(ns, c)
	}
	emitOutOfBand(len(frag.Children), true)
	if len(ns) == 0 {
		return xpath.Sequence{}
	}
	// A child may be a carrier node standing in for a non-node item that
	// cannot round-trip through the tree at all (a map, array, or function —
	// see RealItem's doc comment on xmltree.Node). Unwrapping through
	// Items()/FromItems() here means the variable/param/function-return's
	// stored VALUE is the real item itself, not a NodeSet wrapping its
	// carrier (maps-017: leaving it wrapped broke serialize($v, ...) — its
	// JSON writer inspects the concrete Go value directly rather than going
	// through xpath.Items() first the way most other consumers already do).
	// When every child is an ordinary real node (no carriers at all — the
	// overwhelmingly common case) FromItems reconstructs an equivalent
	// NodeSet, so this is a no-op renormalization there.
	return xpath.FromItems(xpath.Items(ns))
}

// --- execution --------------------------------------------------------------

func (eng *engine) execSequence(instrs []instruction, r rt, out *xmltree.Node) error {
	for _, in := range instrs {
		if err := eng.execInstr(in, r, out); err != nil {
			return err
		}
	}
	return nil
}

func (eng *engine) execInstr(in instruction, r rt, out *xmltree.Node) error {
	// Universal recursion guard: a non-terminating stylesheet (template/function/
	// accumulator/key recursion) errors out rather than overflowing the stack.
	eng.depth++
	defer func() { eng.depth-- }()
	if eng.depth > maxTemplateDepth {
		return fmt.Errorf("err:XTDE0040: instruction recursion too deep (possible non-terminating stylesheet)")
	}
	// Step budget: bounds a single transform's runtime so a non-terminating-but-
	// non-deepening loop (e.g. unbounded xsl:iterate) errors instead of hanging.
	eng.steps++
	if eng.steps > maxSteps {
		return fmt.Errorf("err:XTDE0040: step budget exhausted (possible non-terminating stylesheet)")
	}
	if eng.outSink != nil {
		// The one QUIESCENT point in the whole engine: about to dispatch an
		// instruction means the previous one has fully returned, fix-ups
		// included, and every enclosing one is between siblings of its own
		// body. That is exactly what the incremental serializer needs — see
		// xmltree.ChunkWriter. (Notifying on every Node.Append instead is not
		// safe: execCopyOf appends its copies and only then revisits them by
		// index, and a flush in between renumbers them.)
		eng.outSink.Tick()
	}
	// Registry-based instructions implement execer.
	if e, ok := in.(execer); ok {
		return e.exec(eng, r, out)
	}
	switch n := in.(type) {
	case *litText:
		appendLiteralText(out, n.text, false)
		return nil
	case *textInstr:
		appendLiteralText(out, n.text, n.doe)
		return nil
	case *litElement:
		return eng.execLitElement(n, r, out)
	case *valueOf:
		// §5.8.2: the default separator is a single space for the @select
		// form and a zero-length string for the sequence-constructor form.
		sep := " "
		if n.sel == nil {
			sep = ""
		}
		hasExplicitSep := n.sep != nil
		if n.sep != nil {
			var err error
			if sep, err = eng.evalAVT(n.sep, n.el, r); err != nil {
				return err
			}
		}
		if n.sel == nil {
			s, err := eng.stringFromBody(n.body, r, sep)
			if err != nil {
				return err
			}
			out.Append(textNode(s, n.doe))
			return nil
		}
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return err
		}
		// xsl:value-of computes the STRING VALUE of its selected sequence —
		// atomizing a map or function item (an array is flattened into its
		// members first, same as joinSeq itself does) is FOTY0013, not the
		// silent "" that the general lenient ToString/joinSeq machinery
		// (shared with attribute/simple-content construction elsewhere)
		// otherwise produces (maps-907).
		for _, it := range xpath.Flatten(xpath.Items(v)) {
			switch it.(type) {
			case *xpath.Map, *xpath.Function:
				return errAt(n.el, "err:FOTY0013: xsl:value-of cannot compute the string value of a map or function item")
			}
		}
		// XSLT 1.0 backwards-compatible processing: xsl:value-of/@select
		// takes the string value of the FIRST item of a multi-item sequence
		// instead of joining the whole sequence — but only when no explicit
		// @separator overrides the default, since a separator always joins
		// regardless of version ("backwards-009: xsl:value-of with
		// separator, version is irrelevant").
		if !hasExplicitSep && eng.bc10At(n.el) {
			out.Append(textNode(xpath.ToString(v), n.doe))
			return nil
		}
		out.Append(textNode(joinSeq(v, sep), n.doe))
		return nil
	case *applyTemplates:
		return eng.execApplyTemplates(n, r, out)
	case *forEach:
		return eng.execForEach(n, r, out)
	case *ifInstr:
		v, err := eng.eval(n.test, n.el, r)
		if err != nil {
			return err
		}
		if xpath.ToBool(v) {
			return eng.execSequence(n.body, r, out)
		}
		return nil
	case *chooseInstr:
		return eng.execChoose(n, r, out)
	case *localVar:
		if n.unused && eng.varsResolvable(n.refs) {
			return nil // never referenced: lazily never evaluated (see localVar)
		}
		v, err := eng.evalVarDef(n.def, r)
		if err != nil {
			return err
		}
		eng.bindVar(n.def.name, v)
		return nil
	case *callTemplate:
		return eng.execCallTemplate(n, r, out)
	case *attrInstr:
		return eng.execAttribute(n, r, out)
	case *elemInstr:
		return eng.execElement(n, r, out)
	case *copyInstr:
		return eng.execCopy(n, r, out)
	case *copyOf:
		return eng.execCopyOf(n, r, out)
	case *commentInstr:
		s, err := eng.simpleContentValue(n.sel, n.body, n.el, r)
		if err != nil {
			return err
		}
		// A constructed comment can never legally contain "--" or end in
		// "-" (both would prematurely close an XML comment): the processor
		// inserts a space to keep the serialized comment well-formed
		// (construct-node-007).
		s = strings.ReplaceAll(s, "--", "- -")
		if strings.HasSuffix(s, "-") {
			s += " "
		}
		out.Append(&xmltree.Node{Kind: xmltree.KindComment, Value: s})
		return nil
	case *piInstr:
		name, err := eng.evalAVT(n.name, nil, r)
		if err != nil {
			return err
		}
		if !isNCName(strings.TrimSpace(name)) || strings.EqualFold(strings.TrimSpace(name), "xml") {
			return fmt.Errorf("err:XTDE0890: xsl:processing-instruction name %q is not a valid NCName", name)
		}
		s, err := eng.simpleContentValue(n.sel, n.body, n.el, r)
		if err != nil {
			return err
		}
		// A constructed PI's content can never legally contain "?>" (it
		// would prematurely close the PI): the processor inserts a space
		// between the "?" and ">" to keep it well-formed (construct-node-022).
		s = strings.ReplaceAll(s, "?>", "? >")
		out.Append(&xmltree.Node{Kind: xmltree.KindPI, Name: xmltree.Name{Local: name}, Value: s})
		return nil
	case *sequenceInstr:
		if n.sel == nil { // 3.0 content form: run the sequence constructor
			return eng.execSequence(n.body, r, out)
		}
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return err
		}
		if err := eng.checkSerializableItems(v, out); err != nil {
			return err
		}
		return emitSequenceValueRef(v, out)
	case *analyzeString:
		return eng.execAnalyzeString(n, r, out)
	}
	return fmt.Errorf("unknown instruction %T", in)
}

func (eng *engine) execLitElement(n *litElement, r rt, out *xmltree.Node) error {
	el := xmltree.NewElement(n.name)
	// n.ns is the compiled TEMPLATE of namespace bindings (this element's own
	// literal xmlns:* plus any in-scope stylesheet namespaces copied per
	// XSLT's literal-result-element rule) — fresh Node instances are built
	// per invocation since the compiled list is shared across every
	// invocation of this instruction, but each output element needs its own
	// namespace nodes (correct Parent linkage, no cross-invocation aliasing).
	for _, ns := range n.ns {
		el.NS = append(el.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: ns.Name, Value: ns.Value, Parent: el})
	}
	if err := eng.applyAttrSets(n.useSets, r, el); err != nil {
		return err
	}
	for _, a := range n.attrs {
		val, err := eng.evalAVT(a.value, n.el, r)
		if err != nil {
			return err
		}
		el.SetAttr(a.name, val)
	}
	out.Append(el)
	// A literal result element's content is its OWN sequence constructor: an
	// xsl:variable declared among its children is in scope for the REST of
	// THIS element's children only, not for the enclosing construct's later
	// siblings of the element itself (param-0107: <p1><xsl:variable .../>
	// ...</p1><e1>...$y...</e1> — the rebindings inside <p1> must not leak
	// into the sibling <e1>). Push/pop a fresh scope frame around the body
	// exactly as xsl:for-each/template invocation already do.
	eng.pushScope()
	err := eng.execSequence(n.body, r, el)
	eng.popScope()
	if err != nil {
		return err
	}
	stripEmptyLiteralText(el)
	// The element's content is complete, which is the point §24.4.1.1 defines
	// its own xsl:validation/xsl:type to take effect at — after any request on
	// a CHILD instruction has already had its turn, so an outer strip can
	// override an inner strict exactly as the spec describes.
	return eng.applyValidation(n.val, el)
}

// stripEmptyLiteralText removes any zero-length, NON-ATOMIC text-node child
// of el — the trace an xsl:text/xsl:value-of/literal text contribution
// leaves when it evaluates to the empty string — now that el's ENTIRE
// content has finished constructing, so no further adjacency merge can
// still depend on that node being present (element-0303: <xsl:text/> alone
// in an element must leave it childless, serializing as <x_2/> not
// <x_2></x_2>; element-0304: such a contribution must not be counted by
// text()). This runs as a one-time cleanup AFTER construction rather than
// suppressing the node's creation during it, precisely because an ATOMIC
// empty text node (from xsl:sequence or another computed-value instruction —
// left untouched here) can still be the anchor a LATER adjacent atomic
// value's separating space merges into while content is still being built
// (on-empty-113b/114a: xsl:sequence select="”" followed by xsl:on-empty
// select="'|'" must read "... |", the space coming from exactly that
// empty-but-present atomic node) — removing it only once everything is
// already finalized cannot perturb those merge decisions.
func stripEmptyLiteralText(el *xmltree.Node) {
	if len(el.Children) == 0 {
		return
	}
	kept := el.Children[:0]
	for _, c := range el.Children {
		if c.Kind == xmltree.KindText && !c.Atomic && c.Value == "" {
			continue
		}
		kept = append(kept, c)
	}
	el.Children = kept
}

func (eng *engine) execChoose(n *chooseInstr, r rt, out *xmltree.Node) error {
	for _, w := range n.whens {
		v, err := eng.eval(w.test, w.el, r)
		if err != nil {
			return err
		}
		if xpath.ToBool(v) {
			return eng.execSequence(w.body, r, out)
		}
	}
	return eng.execSequence(n.otherwise, r, out)
}

// realItemOf returns the real xpath.Item a synthetic non-node context wrapper
// stands in for (n.RealItem — see its doc comment on xmltree.Node), or nil
// for an ordinary node/absent context. Safe to call with n == nil.
func realItemOf(n *xmltree.Node) xpath.Item {
	if n == nil {
		return nil
	}
	return n.RealItem
}

// attachRealItem records it on nd.RealItem when it is a map, array, or
// function — the item kinds with no meaningful string value, so a synthetic
// context-item wrapper (built from xpath.ToString(it) elsewhere) would
// otherwise silently lose the item's identity entirely. See RealItem's own
// doc comment on xmltree.Node for the full invariant and why ordinary atomic
// items are deliberately left alone (nd.RealItem stays nil for them).
func attachRealItem(nd *xmltree.Node, it xpath.Item) {
	switch it.(type) {
	case *xpath.Map, *xpath.Array, *xpath.Function:
		nd.RealItem = it
	}
}

func (eng *engine) execForEach(n *forEach, r rt, out *xmltree.Node) error {
	v, err := eng.eval(n.sel, n.el, r)
	if err != nil {
		return err
	}
	// XSLT 2.0+: for-each iterates any sequence. Atomic items are modelled as
	// synthetic text nodes so "." yields their value.
	items := xpath.Items(v)
	// A function/map/array item cannot be modelled as a synthetic text node
	// without losing its identity (available-system-properties-002 iterates
	// over function items and asks ". instance of function(*)"), so when the
	// selected sequence contains one, iterate the items directly with the
	// item carried in rt.item. Sorting is not supported in that shape — a sort
	// key over a function item is a type error anyway.
	if len(n.sorts) == 0 && hasOpaqueItem(items) {
		return eng.forEachItems(n, items, out)
	}
	nodes := make([]*xmltree.Node, len(items))
	for i, it := range items {
		if nd, ok := it.(*xmltree.Node); ok {
			nodes[i] = nd
		} else {
			// Atomic: true (matching psInput's identical wrapper in
			// instr_performsort.go) is REQUIRED, not cosmetic: when "."
			// inside this for-each is itself copied onward — e.g.
			// xsl:sequence select="." — deepCopyInto's KindText case only
			// applies appendAtomicText's "adjacent atomics get a single
			// space separator" merge for a node with Atomic set, so leaving
			// it unset here silently concatenated every re-emitted item with
			// NO separator at all (seqtor-007/011/012 and others: several
			// xsl:sequence select="." contributions across for-each
			// iterations must still join "1 2 | 3 4 |", not "12|34|").
			nd := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true, Atomic: true, Value: xpath.ToString(xpath.FromItems([]xpath.Item{it}))}
			// Preserve the item's exact original atomic type through
			// atomization (position-0102: "." + 1 over an xs:integer item
			// must stay integer arithmetic, not untypedAtomic->xs:double).
			if tag, ok := xpath.ItemAtomTypeTag(it); ok {
				nd.TypeAnno = tag
			}
			attachRealItem(nd, it)
			nodes[i] = nd
		}
	}
	if len(n.sorts) > 0 {
		nodes, err = eng.sortNodes(nodes, n.sorts, n.el, r)
		if err != nil {
			return err
		}
	}
	// The current template rule is ABSENT inside xsl:for-each (XSLT 3.0
	// §6.7), so xsl:apply-imports / xsl:next-match there is XTDE0560 rather
	// than a continuation of the enclosing template's search (error-0560a).
	savedApply := eng.applyStack
	eng.applyStack = nil
	defer func() { eng.applyStack = savedApply }()
	size := len(nodes)
	for i, node := range nodes {
		// Each iteration is its OWN sequence constructor and so gets its own
		// variable scope. Sharing one scope across the whole loop let each
		// iteration overwrite the previous iteration's xsl:variable in the
		// same map — invisible while the value is read immediately, but fatal
		// for a closure that captures it: eng.eval snapshots the scope stack
		// by copying map POINTERS, so every closure built in the loop saw the
		// LAST iteration's value (higher-order-functions-042).
		if err := eng.forEachIteration(n.body, rt{node: node, pos: i + 1, size: size}, out); err != nil {
			return err
		}
	}
	return nil
}

// forEachIteration runs one iteration of a loop body in its own variable scope.
func (eng *engine) forEachIteration(body []instruction, r rt, out *xmltree.Node) error {
	eng.pushScope()
	defer eng.popScope()
	return eng.execSequence(body, r, out)
}

// hasOpaqueItem reports whether any item is one this engine cannot round-trip
// through a synthetic text node: a function, map or array (no string form at
// all), an xs:QName or xs:NOTATION (whose string form drops the namespace URI,
// so the item could never be recovered — available-system-properties-101;
// xs:NOTATION shares xs:QName's value space exactly, XSD 1.0 Part 2 §3.2.19),
// or a value carrying a USER-DEFINED schema type (the synthetic wrapper keeps
// only the built-in primitive in TypeAnno, so `. instance of my:T` inside the
// loop could never answer yes again — notation-0401..0404).
func hasOpaqueItem(items []xpath.Item) bool {
	for _, it := range items {
		switch it.(type) {
		case *xpath.Function, *xpath.Map, *xpath.Array:
			return true
		}
		if _, isNode := it.(*xmltree.Node); isNode {
			continue
		}
		if tag, ok := xpath.ItemAtomTypeTag(it); ok {
			if t := xpath.AtomType(tag); t == xpath.XSqname || t == xpath.XSnotation {
				return true
			}
		}
		if a, ok := it.(*xpath.Atomic); ok && a.SchemaType() != nil {
			return true
		}
	}
	return false
}

// forEachItems runs an xsl:for-each body over a sequence containing at least
// one function/map/array item, passing every item through rt.item (and nodes
// through rt.node as usual).
func (eng *engine) forEachItems(n *forEach, items []xpath.Item, out *xmltree.Node) error {
	savedApply := eng.applyStack
	eng.applyStack = nil
	defer func() { eng.applyStack = savedApply }()
	for i, it := range items {
		r := rt{pos: i + 1, size: len(items)}
		if nd, ok := it.(*xmltree.Node); ok {
			r.node = nd
		} else {
			r.item = it
		}
		// Per-iteration scope — see execForEach's own note.
		if err := eng.forEachIteration(n.body, r, out); err != nil {
			return err
		}
	}
	return nil
}

func (eng *engine) execApplyTemplates(n *applyTemplates, r rt, out *xmltree.Node) error {
	var nodes xpath.NodeSet
	if n.sel != nil {
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return err
		}
		ns, ok := xpath.ToNodeSet(v)
		if !ok {
			// XSLT 3.0: apply-templates processes a general sequence.
			ns = itemsAsNodes(xpath.Items(v))
		}
		nodes = ns
	} else {
		// With no @select the instruction processes the CHILDREN of the
		// context item, which therefore has to be a node (XTTE0510).
		if r.node != nil && r.node.SynthCtx {
			return errAt(n.el, "err:XTTE0510: xsl:apply-templates with no select requires a node context item")
		}
		nodes = childrenOf(r.node)
	}
	if len(n.sorts) > 0 {
		sorted, err := eng.sortNodes([]*xmltree.Node(nodes), n.sorts, n.el, r)
		if err != nil {
			return err
		}
		nodes = xpath.NodeSet(sorted)
	}
	return eng.applyToNodes(nodes, eng.resolveMode(n.mode), out, n.params, r)
}

// itemsAsNodes models a general sequence as the node list apply-templates
// dispatches on. A node is itself. Any other item becomes a synthetic context
// node stamped with its ORIGINAL atomic type so a match pattern such as
// ".[. instance of xs:integer]" can see the real value (match-127..135) and
// the built-in text-only-copy rule still emits its string value;
// attachRealItem additionally carries a map/array/function item's real
// identity through (see its doc comment) so it survives re-emission via
// RealItem, exactly like xsl:for-each's own identical wrapper (seqtor-007).
// A function item is NOT rejected here: a template rule whose pattern is
// ".[. instance of function(*)]" may match it, and only when none does is
// processing it an error — which the built-in rule reports
// (higher-order-functions-069). An array likewise stays ONE item, so a rule
// matching ".[. instance of array(*)]" gets it whole (square-array-019); only
// the built-in rule spreads it over its members.
func itemsAsNodes(items []xpath.Item) xpath.NodeSet {
	var ns xpath.NodeSet
	for _, it := range items {
		if nd, isNode := it.(*xmltree.Node); isNode {
			ns = append(ns, nd)
			continue
		}
		nd := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true, Atomic: true, Value: xpath.ToString(xpath.FromItems([]xpath.Item{it}))}
		if tag, ok := xpath.ItemAtomTypeTag(it); ok {
			nd.TypeAnno = tag
		}
		attachRealItem(nd, it)
		ns = append(ns, nd)
	}
	return ns
}

// applyToNodes applies templates to nodes in mode. callerR is the context
// (context item/position/size) of the xsl:apply-templates instruction itself
// — with-param select expressions are evaluated against callerR, NOT against
// each selected node's own context (XSLT: params are computed using the focus
// of the instruction that supplies them, which does not vary as apply-templates
// iterates over the nodes it selected).
func (eng *engine) applyToNodes(nodes xpath.NodeSet, mode string, out *xmltree.Node, params []*VarDef, callerR rt) error {
	// Compute incoming tunnel params (with-param tunnel="yes") once, in the
	// CALLER's context (callerR) — matching the doc comment above and the
	// non-tunnel with-param handling elsewhere in this file (as-0703: a
	// relative select like "item-list/item2" must resolve against the
	// apply-templates instruction's own context item, not always the
	// document root).
	tunnelIn, err := eng.tunnelFrom(params, callerR)
	if err != nil {
		return err
	}
	size := len(nodes)
	// XSLT 3.0 §6.6.3: yes/strict/lax all require the nodes this mode
	// processes to be typed (XTTE3100); no requires them to be untyped
	// (XTTE3110). Both are about what xsl:apply-templates SELECTS, which is
	// exactly this list.
	typedKind := modeTypedKind(eng.sheet.modeDef(mode))
	for i, node := range nodes {
		switch node.Kind {
		case xmltree.KindElement, xmltree.KindAttribute:
			switch typedKind {
			case "yes", "strict", "lax":
				if !nodeIsTyped(node) {
					// Reached unconditionally in a non-schema-aware run, where
					// nothing can ever annotate a node (mode-1439).
					return errAt(nil, "err:XTTE3100: mode %q is declared typed=%q but %s is untyped",
						mode, typedKind, node.Name.Local)
				}
			case "no":
				if nodeIsTyped(node) {
					return errAt(nil, "err:XTTE3110: mode %q is declared typed=\"no\" but %s carries a type annotation",
						mode, node.Name.Local)
				}
			}
		}
		r := rt{node: node, pos: i + 1, size: size}
		cands := eng.matchingTemplates(node, mode)
		// on-multiple-match="fail": conflict resolution leaving two rules of
		// the SAME import precedence and priority is XTDE0540 rather than a
		// silent "last one wins" (error-0540a). Two branches of ONE union
		// pattern are compiled as separate rules of the SAME xsl:template and
		// are not a conflict (mode-1516, spec bug 30402).
		if len(cands) > 1 && cands[0].importPrec == cands[1].importPrec &&
			cands[0].priority == cands[1].priority && cands[0].el != cands[1].el &&
			eng.sheet.modeDef(mode).attrs["on-multiple-match"] == "fail" {
			return errAt(cands[0].el, "err:XTDE0540: more than one template rule matches and the mode specifies on-multiple-match=\"fail\"")
		}
		if len(cands) == 0 {
			// Per XSLT spec §6.6: "If the built-in rule was invoked with
			// parameters, those parameters are passed on in the implicit
			// xsl:apply-templates instruction" — non-tunnel with-params
			// supplied to the instruction that fell through to the built-in
			// rule keep flowing, unevaluated further, through however many
			// levels of built-in-rule recursion until a real template
			// declares a matching xsl:param (as-0601: with-params on an
			// apply-templates matching "/" must still reach templates two
			// element levels below, through two built-in-rule hops).
			if err := eng.builtinTemplate(node, mode, out, params, callerR); err != nil {
				return err
			}
			continue
		}
		if err := eng.invokeRule(cands, 0, node, mode, r, out, params, tunnelIn, callerR); err != nil {
			return err
		}
	}
	return nil
}

// invokeRule executes candidate template cands[idx] for node, recording the
// frame so xsl:next-match / xsl:apply-imports can run lower-ranked rules.
func (eng *engine) invokeRule(cands []*Template, idx int, node *xmltree.Node, mode string, r rt, out *xmltree.Node, callerParams []*VarDef, tunnelIn map[string]xpath.Object, callerR rt) error {
	prevMode := eng.curMode
	eng.curMode = mode
	eng.applyStack = append(eng.applyStack, &applyFrame{node: node, mode: mode, cands: cands, idx: idx})
	defer func() {
		eng.applyStack = eng.applyStack[:len(eng.applyStack)-1]
		eng.curMode = prevMode
	}()
	return eng.invokeTemplate(cands[idx], r, out, callerParams, tunnelIn, callerR)
}

// callerR is the context in which callerParams' with-param select expressions
// are evaluated — the context of the calling instruction (apply-templates/
// call-template/apply-imports/next-match), which for apply-templates differs
// from r (the matched node currently being processed).
func (eng *engine) invokeTemplate(tmpl *Template, r rt, out *xmltree.Node, callerParams []*VarDef, tunnelIn map[string]xpath.Object, callerR rt) error {
	eng.depth++
	defer func() { eng.depth-- }()
	if eng.depth > maxTemplateDepth {
		return errAt(tmpl.el, "err:XTDE0040: template recursion too deep (possible non-terminating stylesheet)")
	}
	if err := checkBackwardsCompatVersion(tmpl.el); err != nil {
		return err
	}
	// xsl:context-item constrains the focus the body runs in — and with
	// use="absent" removes it entirely. It applies at EVERY entry point
	// (call-template, apply-templates, next-match/apply-imports and the
	// initial named template), which all funnel through here.
	if tmpl.ctxItem != nil {
		nr, err := eng.applyContextItem(tmpl.ctxItem, r)
		if err != nil {
			return err
		}
		r = nr
		if tmpl.ctxItem.use == "absent" {
			// With no context item there is no node for a lower-priority rule
			// to be applied TO, so the current template rule is absent inside
			// such a body and xsl:next-match / xsl:apply-imports there is
			// XTDE0560 (next-match-029). Restoring the whole saved slice —
			// rather than truncating — keeps invokeRule's own pop correct.
			savedApply := eng.applyStack
			eng.applyStack = nil
			defer func() { eng.applyStack = savedApply }()
		}
	}
	// The current merge group/key is ABSENT inside a template invoked from an
	// xsl:merge-action (XSLT 3.0 §15.2): xsl:call-template and
	// xsl:apply-templates both clear it, so current-merge-group() there is
	// XTDE3480 and current-merge-key() XTDE3510 (merge-055/056/087/088).
	// Restored on return so the rest of the merge-action still sees it.
	if eng.inMerge {
		savedMerge, savedGroup, savedKey, savedSrcs := eng.inMerge, eng.curMergeGroup, eng.curMergeKey, eng.curMergeSources
		savedBySource := eng.curMergeGroupBySource
		eng.inMerge, eng.curMergeGroup, eng.curMergeKey, eng.curMergeSources = false, nil, nil, nil
		eng.curMergeGroupBySource = nil
		defer func() { eng.curMergeGroupBySource = savedBySource }()
		defer func() {
			eng.inMerge, eng.curMergeGroup, eng.curMergeKey, eng.curMergeSources = savedMerge, savedGroup, savedKey, savedSrcs
		}()
	}
	// The current group/grouping key are likewise ABSENT inside a template
	// invoked from within a declared-streamable construct (XSLT 3.0 §14.2.1):
	// there the group's scope is STATIC — it may be read only in the body of
	// the xsl:for-each-group itself, never in a template that body calls or
	// applies. The callee is outside that static extent, so the flag is
	// cleared for it too: its own xsl:for-each-group is an ordinary one.
	if eng.inStreamable {
		savedIn, savedGroup, savedOK, savedKey, savedKeyOK := eng.inStreamable, eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK
		eng.inStreamable, eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK = false, nil, false, nil, false
		defer func() {
			eng.inStreamable, eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK = savedIn, savedGroup, savedOK, savedKey, savedKeyOK
		}()
	}
	// Every xsl:with-param is evaluated in the context of the CALLING
	// instruction, so all supplied values must be computed BEFORE the callee's
	// scope exists and before any of its parameters are bound. Evaluating them
	// lazily inside the binding loop below let an already-bound callee
	// parameter shadow the caller's variable of the same name (copy-4306: a
	// recursive call whose with-param body reads $count must see the CALLER's
	// $count, not the value just bound for the callee's own $count). A
	// parameter's DEFAULT, by contrast, is evaluated in the callee's scope and
	// may legitimately refer to an earlier parameter of the same template, so
	// it stays inside the loop.
	type suppliedParam struct {
		val xpath.Object
		def *VarDef
	}
	var supplied map[string]suppliedParam
	for _, p := range tmpl.params {
		if p.tunnel {
			continue
		}
		cp := findParam(callerParams, p.name)
		if cp == nil || cp.tunnel {
			continue
		}
		v, err := eng.evalVarDef(cp, callerR)
		if err != nil {
			return err
		}
		if supplied == nil {
			supplied = make(map[string]suppliedParam, len(tmpl.params))
		}
		supplied[clark(p.name.Space, p.name.Local)] = suppliedParam{v, cp}
	}
	eng.pushScope()
	prevTunnel := eng.tunnel
	if tunnelIn != nil {
		eng.tunnel = tunnelIn
	}
	defer func() { eng.popScope(); eng.tunnel = prevTunnel }()

	for _, p := range tmpl.params {
		var val xpath.Object
		var err error
		if p.tunnel {
			if tv, ok := eng.tunnel[clark(p.name.Space, p.name.Local)]; ok {
				if p.as != "" {
					// The function-conversion rules apply to a tunnel value
					// flowing in from the ambient set exactly as they do to a
					// non-tunnel with-param's supplied value: XTTE0590 if it
					// cannot be reconciled with the receiving xsl:param's own
					// declared type (variable-0206: an upstream tunnel
					// with-param select="17" [xs:integer] reaching a
					// downstream $t1 declared as="node()").
					cv, ok := xpath.CoerceToDeclaredTypeCtx(p.as, tv, asTypeCtx(p.el))
					if !ok {
						return errAt(p.el, "err:XTTE0590: supplied value of parameter $%s does not match declared type %q", p.name.Local, p.as)
					}
					tv = cv
				}
				eng.bindVar(p.name, tv)
				continue
			}
			// A required="yes" tunnel param not present anywhere in the
			// ambient tunnel set (not supplied by any enclosing
			// apply-templates/apply-imports/next-match/call-template on the
			// path here) is XTDE0700, exactly like an unsupplied required
			// non-tunnel param below — evalVarDef has no general "required"
			// check of its own outside the separate global-variable case
			// (variable-0205: template match="d"'s tunnel required $t4 is
			// never supplied by either apply-templates call in the chain).
			if p.requiredParam {
				return errAt(p.el, "err:XTDE0700: required parameter $%s was not supplied", p.name.Local)
			}
			val, err = eng.evalVarDef(p, r)
		} else if sp, ok := supplied[clark(p.name.Space, p.name.Local)]; ok {
			// A non-tunnel xsl:param is bound only from a NON-tunnel with-param
			// of the same name. A tunnel with-param flowing through (e.g.
			// forwarded down the built-in-rule chain) must not satisfy it — it
			// stays in the tunnel and this param takes its default
			// (tunnel-0304: a template's tunnel="no" par1 defaults to '123'
			// even while a tunnel="yes" par1 passes by unset).
			cp := sp.def
			val = sp.val
			if p.as != "" {
				// The function-conversion rules apply to a with-param's
				// supplied value against the PARAM's own declared type (not
				// just the with-param element's, which usually has none of
				// its own): it is a type error (XTTE0590) if conversion
				// fails (error-0590a: with-param select="current-date()"
				// supplied for a param declared as="xs:integer").
				cv, ok := xpath.CoerceToDeclaredTypeCtx(p.as, val, asTypeCtx(p.el))
				if !ok {
					return errAt(cp.el, "err:XTTE0590: supplied value of parameter $%s does not match declared type %q", p.name.Local, p.as)
				}
				val = cv
			}
		} else {
			if p.requiredParam {
				return errAt(p.el, "err:XTDE0700: required parameter $%s was not supplied", p.name.Local)
			}
			val, err = eng.evalVarDef(p, r)
		}
		if err != nil {
			return err
		}
		eng.bindVar(p.name, val)
	}
	if tmpl.as == "" {
		return eng.execSequence(tmpl.body, r, out)
	}
	// Fast path, mirroring evalVarDef: a body of exactly one xsl:sequence
	// contributes its raw value directly (needed for xs:QName and similar
	// values that cannot round-trip through text — see evalVarDef's comment).
	if len(tmpl.body) == 1 {
		if si, ok := tmpl.body[0].(*sequenceInstr); ok && si.sel != nil {
			v, err := eng.eval(si.sel, si.el, r)
			if err != nil {
				return err
			}
			cv, ok := xpath.CoerceToDeclaredTypeCtx(tmpl.as, v, asTypeCtx(tmpl.el))
			if !ok {
				return errAt(tmpl.el, "err:XTTE0505: result of template %s does not match declared type %q", templateLabel(tmpl), tmpl.as)
			}
			eng.noteEntryValue(cv)
			emitSequenceValueRef(cv, out)
			return nil
		}
	}
	// @as on xsl:template: the sequence constructor's result is converted to
	// the declared type using the function-conversion rules (as-0802/0802b:
	// nodes atomize and cast up to the declared atomic type); it is a type
	// error (XTTE0505) if the result — after conversion — does not match
	// (type-0169/0170/…: wrong cardinality or an item of the wrong kind).
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true, Ephemeral: true}
	if err := eng.execSequence(tmpl.body, r, frag); err != nil {
		return err
	}
	seq := fragAsSequence(frag)
	cv, ok := xpath.CoerceToDeclaredTypeCtx(tmpl.as, seq, asTypeCtx(tmpl.el))
	if !ok {
		return errAt(tmpl.el, "err:XTTE0505: result of template %s does not match declared type %q", templateLabel(tmpl), tmpl.as)
	}
	eng.noteEntryValue(cv)
	// The template's result sequence is the NODES it returned, by reference
	// (byRef), exactly as xsl:sequence contributes them: snapshot-0102a's
	// f:graft-to-parent hands back a child of a copied document node through
	// a template declared as="node()", and root() of the node the caller
	// receives must still be that document.
	emitSequenceValueOpts(cv, out, true, true)
	return nil
}

// templateLabel names a template for an error message: its name if named,
// otherwise its match pattern.
func templateLabel(t *Template) string {
	if t.name != "" {
		return "name=\"" + t.name + "\""
	}
	return "match=\"" + t.matchSrc + "\""
}

// tunnelFrom builds the tunnel-parameter map for a call: the current tunnel map
// extended/overridden by the tunnel with-params in params.
func (eng *engine) tunnelFrom(params []*VarDef, r rt) (map[string]xpath.Object, error) {
	hasTunnel := false
	for _, p := range params {
		if p.tunnel {
			hasTunnel = true
			break
		}
	}
	if !hasTunnel && eng.tunnel == nil {
		return nil, nil
	}
	out := map[string]xpath.Object{}
	for k, v := range eng.tunnel {
		out[k] = v
	}
	for _, p := range params {
		if !p.tunnel {
			continue
		}
		v, err := eng.evalVarDef(p, r)
		if err != nil {
			return nil, err
		}
		out[clark(p.name.Space, p.name.Local)] = v
	}
	return out, nil
}

func (eng *engine) execCallTemplate(n *callTemplate, r rt, out *xmltree.Node) error {
	tmpl, ok := eng.sheet.named[n.name]
	if !ok {
		// ss.named is keyed by the RAW lexical name (whatever prefix the
		// declaring xsl:template used), so a call site using a DIFFERENT
		// prefix bound to the same namespace URI needs a fallback,
		// expanded-name-based lookup (call-template-1701, mirrors key-013's
		// xsl:key name resolution).
		target := clarkName(resolveQName(n.el, n.name))
		for name, t := range eng.sheet.named {
			if name == n.name {
				continue // already tried above
			}
			if t.el != nil && clarkName(resolveQName(t.el, name)) == target {
				tmpl, ok = t, true
				break
			}
		}
	}
	if !ok {
		return errAt(n.el, "no template named %q", n.name)
	}
	// XTSE0680: every non-tunnel xsl:with-param must name a parameter the
	// called template actually declares (error-0680a) — unless backwards-
	// compatible processing is in effect (backwards-013), which reverts to
	// XSLT 1.0/2.0's lenient "an unwanted parameter is simply ignored"
	// behaviour; see checkOneCallTemplate's identical, STATIC form of this
	// same check for the reasoning.
	bc10 := backwardsCompatRun() && inBackwardsCompatScope(n.el)
	for _, p := range n.params {
		if p.tunnel || bc10 {
			continue
		}
		found := false
		for _, tp := range tmpl.params {
			if tp.name.Local == p.name.Local && tp.name.Space == p.name.Space {
				found = true
				break
			}
		}
		if !found {
			return errAt(n.el, "err:XTSE0680: template %q has no parameter named %q", n.name, p.name.Local)
		}
	}
	tunnelIn, err := eng.tunnelFrom(n.params, r)
	if err != nil {
		return err
	}
	return eng.invokeTemplate(tmpl, r, out, n.params, tunnelIn, r)
}

func (eng *engine) execAttribute(n *attrInstr, r rt, out *xmltree.Node) error {
	name, err := eng.evalAVT(n.name, n.el, r)
	if err != nil {
		return err
	}
	// §5.8.2: the default separator is a single space for the @select form and
	// a zero-length string for the sequence-constructor form
	// (attribute-set-1811 g/h).
	sep := " "
	if n.sel == nil {
		sep = ""
	}
	if n.sep != nil {
		if sep, err = eng.evalAVT(n.sep, n.el, r); err != nil {
			return err
		}
	}
	var val string
	if n.sel != nil {
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return err
		}
		// §5.8.2 simple content construction ATOMIZES the sequence, and
		// atomizing a map or function item is FOTY0013 — not the silent "" the
		// lenient joinSeq produces. Same check, same reason, as execValueOf.
		for _, it := range xpath.Flatten(xpath.Items(v)) {
			switch it.(type) {
			case *xpath.Map, *xpath.Function:
				return errAt(n.el, "err:FOTY0013: xsl:attribute cannot compute the string value of a map or function item")
			}
		}
		val = joinSeq(v, sep)
	} else {
		val, err = eng.stringFromBody(n.body, r, sep)
		if err != nil {
			return err
		}
	}
	trimmedName := strings.TrimSpace(name)
	if !isLexicalQName(trimmedName) {
		return errAt(n.el, "err:XTDE0850: xsl:attribute name %q is not a valid QName", name)
	}
	if out.Kind == xmltree.KindElement && len(out.Children) > 0 {
		return errAt(n.el, "err:XTDE0420: cannot add an attribute after child nodes")
	}
	if n.ns == nil {
		if trimmedName == "xmlns" {
			return errAt(n.el, "err:XTDE0855: xsl:attribute name must not be \"xmlns\" when there is no namespace attribute")
		}
		// A prefixed computed name with no in-scope binding for that prefix (and
		// no @namespace to override it) is a dynamic error (namespace-0101),
		// mirroring xsl:element's XTDE0830 check. A Q{uri}local braced EQName
		// carries its namespace explicitly and is excluded (its URI may itself
		// contain a colon).
		if i := strings.IndexByte(trimmedName, ':'); i > 0 && !strings.HasPrefix(trimmedName, "Q{") {
			if _, bound := n.el.LookupPrefix(trimmedName[:i]); !bound {
				return errAt(n.el, "err:XTDE0860: prefix %q in xsl:attribute name has no namespace binding", trimmedName[:i])
			}
		}
	}
	qn := resolveQName(n.el, name)
	if n.ns != nil {
		uri, err := eng.evalAVT(n.ns, n.el, r)
		if err != nil {
			return err
		}
		if uri == xmlnsURI {
			return errAt(n.el, "err:XTDE0865: xsl:attribute namespace must not be the reserved xmlns namespace")
		}
		qn.Space = uri
	}
	// appendAttrItem (not a bare out.SetAttr) so several xsl:attribute
	// instructions producing the SAME name into a discrete-sequence
	// collector (out.NoAtomicMerge — an @as-typed variable/param/function
	// body) stay distinct sequence ITEMS instead of the later one silently
	// overwriting the earlier by name (sequence-0106: as="xs:integer*" over
	// four xsl:attribute, two named "a", must yield all 4 values "1,2,3,4").
	// Building real element content still collapses to one attribute by
	// name, exactly as SetAttr always did.
	attr := appendAttrItem(out, qn, val)
	if err := eng.applyValidation(n.val, attr); err != nil {
		return err
	}
	if out.Kind == xmltree.KindElement {
		// A namespace declaration only has meaning attached to a genuine
		// owning ELEMENT. A standalone xsl:attribute at the top of a
		// sequence constructor (an @as-typed variable/param/function/
		// template body, or bare xsl:sequence content) writes into a
		// transient document-node collector instead — auto-declaring a
		// namespace there would inject a phantom, unrequested namespace-node
		// ITEM into the constructed SEQUENCE alongside the real attribute
		// (as-1402: @as="attribute(my:elem, xs:untypedAtomic)+" over one
		// prefixed xsl:attribute must yield exactly that one item).
		ensureNamespaceInScope(out, qn.Space, qn.Prefix)
	}
	return nil
}

// ensureNamespaceInScope guarantees el (or an ancestor) carries an in-scope
// namespace binding for uri, adding one to el itself if none already exists —
// constructing an attribute or element in a non-null namespace implicitly
// requires such a binding to exist (attribute-0806: xsl:attribute name=
// "xmlns:xsl" namespace="http://whatever.example.com/" must still make that
// URI visible on the namespace:: axis, even though "xmlns" itself can never
// be used as the actual serialized prefix). preferPrefix is tried first
// (falling back to "ns", then "ns0"/"ns1"/… on a collision); "xmlns" itself is
// never a legal prefix so it is treated the same as an empty preference.
func ensureNamespaceInScope(el *xmltree.Node, uri, preferPrefix string) {
	if uri == "" {
		return
	}
	for cur := el; cur != nil; cur = cur.Parent {
		for _, ns := range cur.NS {
			if ns.Value == uri {
				return
			}
		}
	}
	taken := map[string]bool{}
	for _, ns := range el.NS {
		taken[ns.Name.Local] = true
	}
	base := preferPrefix
	if base == "" || base == "xmlns" {
		base = "ns"
	}
	prefix := base
	for i := 0; taken[prefix]; i++ {
		prefix = fmt.Sprintf("%s%d", base, i)
	}
	el.NS = append(el.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: prefix}, Value: uri, Parent: el})
}

func (eng *engine) execElement(n *elemInstr, r rt, out *xmltree.Node) error {
	name, err := eng.evalAVT(n.name, n.el, r)
	if err != nil {
		return err
	}
	// The @name value is typed as xs:QName, whose whitespace facet is
	// "collapse": leading/trailing whitespace is stripped and internal runs
	// are collapsed to a single space before the value is used (XSLT
	// whitespace-028: "  { concat(' ', name(), ' ') }  " must yield
	// "document", not "  document  ").
	name = strings.Join(strings.Fields(name), " ")
	if !isLexicalQName(name) {
		return errAt(n.el, "err:XTDE0820: xsl:element name %q is not a valid QName", name)
	}
	if i := strings.IndexByte(name, ':'); i > 0 && n.ns == nil {
		if _, bound := n.el.LookupPrefix(name[:i]); !bound {
			return errAt(n.el, "err:XTDE0830: prefix %q in xsl:element name has no namespace binding", name[:i])
		}
	}
	qn := resolveQName(n.el, name)
	switch {
	case n.ns != nil:
		uri, err := eng.evalAVT(n.ns, n.el, r)
		if err != nil {
			return err
		}
		if uri == xmlnsURI {
			return errAt(n.el, "err:XTDE0835: xsl:element namespace must not be the reserved xmlns namespace")
		}
		qn.Space = uri
	case qn.Prefix == "" && !strings.HasPrefix(strings.TrimSpace(name), "Q{"):
		// An unprefixed xsl:element name with no @namespace takes the default
		// namespace in scope at the xsl:element's position in the stylesheet
		// (XSLT §xsl:element). This differs from xsl:attribute, whose unprefixed
		// names are always in no namespace.
		if def, ok := n.el.LookupPrefix(""); ok {
			qn.Space = def
		}
	}
	el := xmltree.NewElement(qn)
	if err := eng.applyAttrSets(n.useSets, r, el); err != nil {
		return err
	}
	out.Append(el)
	// Same fresh scope frame as execLitElement — see its comment.
	eng.pushScope()
	err = eng.execSequence(n.body, r, el)
	eng.popScope()
	if err != nil {
		return err
	}
	stripEmptyLiteralText(el)
	if n.noInheritNS {
		markNSBarrier(el, 0)
	}
	return eng.applyValidation(n.val, el)
}

// copiedNamespaceNodes returns the namespace nodes xsl:copy (default
// copy-namespaces="yes") gives the copy of an element: every namespace node
// in the ORIGINAL's full in-scope set, not just its own literal declarations
// — an ancestor's binding that never redeclares on the copied element itself
// is still part of the copy's namespace:: axis (namespace-2101: a "soapenc"
// prefix bound several levels up, never mentioned on the matched element,
// must still be visible after xsl:copy).
func copiedNamespaceNodes(node *xmltree.Node) []*xmltree.Node {
	ambient := node.InScopeNamespaces()
	if len(ambient) == 0 {
		return nil
	}
	prefixes := make([]string, 0, len(ambient))
	for pfx := range ambient {
		prefixes = append(prefixes, pfx)
	}
	sort.Strings(prefixes)
	out := make([]*xmltree.Node, 0, len(prefixes))
	for _, pfx := range prefixes {
		if uri := ambient[pfx]; uri != "" {
			out = append(out, &xmltree.Node{Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: pfx}, Value: uri})
		}
	}
	return out
}

func (eng *engine) execCopy(n *copyInstr, r rt, out *xmltree.Node) error {
	node := r.node
	if n.sel != nil {
		// XSLT 3.0: @select names the item to copy instead of the context
		// item — at most ONE item (XTTE3180: copy-4805), zero items is a
		// no-op (copy-4803), and a non-node (atomic) item copies as its
		// plain value with the body ignored, like xsl:value-of (copy-4804).
		v, err := eng.eval(n.sel, n.el, r)
		if err != nil {
			return err
		}
		items := xpath.Items(v)
		if len(items) == 0 {
			// An empty @select sequence means xsl:copy (and its children) are
			// simply not evaluated — no output, not an error (copy-1207).
			return nil
		}
		if len(items) > 1 {
			return errAt(n.el, "err:XTTE3180: xsl:copy/@select must select at most one item, got %d", len(items))
		}
		nd, ok := items[0].(*xmltree.Node)
		if !ok {
			// A non-node item is "copied" by simply outputting its value, like
			// xsl:sequence would.
			return appendAtomicItem(out, items[0])
		}
		node = nd
		// XSLT 3.0 §11.9.1: the contained sequence constructor is evaluated
		// with the SELECTED item as the context item and the context position
		// and size both 1 (copy-4501: position()/last() inside an
		// xsl:copy select="$n" in a function are 1, not an inherited focus —
		// and in a function there is no inherited focus at all, so without
		// this they were XPDY0002), and with the current template rule ABSENT
		// (next-match-030: xsl:next-match inside such an xsl:copy is
		// XTDE0560, not a continuation of the enclosing rule's search).
		r = rt{node: node, pos: 1, size: 1}
		savedApply := eng.applyStack
		eng.applyStack = nil
		defer func() { eng.applyStack = savedApply }()
	}
	if node == nil {
		// xsl:copy has no @select: it copies the context item, so a dynamic
		// context with no context item at all (error-0945a: xsl:copy reached
		// from a named template's initial call, which has none) is a type
		// error, not a crash.
		return errAt(n.el, "err:XPDY0002: xsl:copy has no context item to copy")
	}
	switch node.Kind {
	case xmltree.KindElement:
		el := xmltree.NewElement(node.Name)
		for _, ns := range copiedNamespaceNodes(node) {
			ns.Parent = el
			el.NS = append(el.NS, ns)
		}
		if err := eng.applyAttrSets(n.useSets, r, el); err != nil {
			return err
		}
		// xsl:copy retains the ORIGINAL node's base-uri (base-uri-024) — but
		// only when the copy is the raw, unwrapped result of an @as-typed
		// body (out.NoAtomicMerge). Embedded as a child of further real
		// complex content (an LRE, xsl:element, or RTF), the copy instead
		// derives its base-uri from its new position, so el.Base is left
		// unset there (base-uri-025/028/030/…).
		if out.NoAtomicMerge {
			el.Base = retainedBase(el, node)
		}
		out.Append(el)
		if err := eng.execSequence(n.body, r, el); err != nil {
			return err
		}
		if n.noCopyNS {
			// XSLT 3.0 §11.9.1: with copy-namespaces="no" the copy keeps only
			// the bindings namespace fixup demands. It is pruned AFTER the body
			// has run because the body is what supplies the attributes whose
			// names those bindings may be needed for (si-copy-020/026).
			pruneCopiedNamespaces(el)
		}
		if n.noInheritNS {
			markNSBarrier(el, 0)
		}
		return eng.applyValidation(n.val, el)
	case xmltree.KindText:
		if node.Atomic {
			// The context item was an ATOMIC value, which this engine carries
			// as a synthetic text node — so copying it must keep the "adjacent
			// atomics get a space between them" rule of §5.7.1, exactly as
			// deepCopyInto does (si-copy-002/007/010: xsl:copy over
			// "data(@value), 101, 102" must separate the copies).
			appendAtomicText(out, node.Value)
			return nil
		}
		out.Append(xmltree.NewText(node.Value))
	case xmltree.KindAttribute:
		// An attribute copied after non-attribute/non-namespace content has
		// already been added to the current element is XTDE0410 (copy-4701:
		// xsl:copy of @* invoked, via apply-templates, after a <child/> was
		// already appended by the calling template).
		if out.Kind == xmltree.KindElement && len(out.Children) > 0 {
			return errAt(n.el, "err:XTDE0410: cannot copy an attribute after non-attribute content")
		}
		cp := appendAttrItem(out, node.Name, node.Value)
		// §24.4.1.1, preserve: "Where the node being copied is an attribute,
		// the copied attribute will retain its type annotation". appendAttrItem
		// builds a fresh node from name+value, so the annotation has to be
		// carried across explicitly before any request is applied to it — a
		// no-op whenever the original carries none, which is every node until
		// something validates one.
		xmltree.CopyTypeInfo(cp, node)
		return eng.applyValidation(n.val, cp)
	case xmltree.KindComment:
		out.Append(&xmltree.Node{Kind: xmltree.KindComment, Value: node.Value})
	case xmltree.KindPI:
		out.Append(&xmltree.Node{Kind: xmltree.KindPI, Name: node.Name, Value: node.Value})
	case xmltree.KindDocument:
		if out.KeepDocItems {
			// out is a discrete-sequence collector (an @as-typed body/
			// xsl:sequence result), not element/RTF content being built:
			// xsl:copy of a document node must produce an actual NEW
			// document node (mirroring the element case's xmltree.NewElement
			// wrapper), not flatten its constructed content directly into
			// out — a document node can never be a child of an element
			// anyway, so every OTHER context (building real content) still
			// flattens below, unchanged (copy-4302: as="document-node()*"
			// over three xsl:copy of a document-node context item).
			d := &xmltree.Node{Kind: xmltree.KindDocument, Base: xpath.NodeBaseURI(node, ""), Ephemeral: true}
			mergeUnparsed(d, node)
			if err := eng.execSequence(n.body, r, d); err != nil {
				return err
			}
			// §24.4.2: a document node's own validation request governs its
			// single element child, not the document node itself.
			if err := eng.applyValidation(n.val, d); err != nil {
				return err
			}
			out.Append(d)
			mergeUnparsed(out, node)
			return nil
		}
		mergeUnparsed(out, node)
		// Even when the copied document node's content is being flattened
		// into the surrounding construction, that content is still the
		// content of a DOCUMENT node: an attribute or namespace node in it is
		// XTDE0420 (error-0420a). Build it separately so those can be seen.
		d := &xmltree.Node{Kind: xmltree.KindDocument}
		if err := eng.execSequence(n.body, r, d); err != nil {
			return err
		}
		if len(d.Attrs) > 0 || len(d.NS) > 0 {
			return errAt(n.el, "err:XTDE0420: the content of a document node cannot contain an attribute or namespace node")
		}
		// Validated while the content is still gathered under its own document
		// node, so §24.4.2's "exactly one element child" shape is the one that
		// is actually checked — flattening it into out first would measure the
		// surrounding construction instead.
		if err := eng.applyValidation(n.val, d); err != nil {
			return err
		}
		for _, c := range d.Children {
			c.Parent = out
			out.Children = append(out.Children, c)
		}
		return nil
	}
	return nil
}

// mergeUnparsed carries a source document node's unparsed (NDATA) entity
// declarations onto a document node copied from it: XSLT's node-copy semantics
// preserve the unparsed entities of a copied document node, so
// unparsed-entity-uri() still resolves against the copy (unparsed-entity-05/
// 06/07/08).
func mergeUnparsed(dst, src *xmltree.Node) {
	if dst == nil || src == nil || len(src.Unparsed) == 0 || dst.Kind != xmltree.KindDocument {
		return
	}
	if dst.Unparsed == nil {
		dst.Unparsed = map[string]xmltree.UnparsedEntity{}
	}
	for k, v := range src.Unparsed {
		if _, ok := dst.Unparsed[k]; !ok {
			dst.Unparsed[k] = v
		}
	}
}

func (eng *engine) execCopyOf(n *copyOf, r rt, out *xmltree.Node) error {
	v, err := eng.eval(n.sel, n.el, r)
	if err != nil {
		return err
	}
	// copy-accumulators="yes": the copied nodes keep the accumulator values of
	// the originals (XSLT 3.0 §18.2.4, accumulator-048/063..072). The copies
	// are made by emitSequenceValue below, so the source nodes are paired with
	// the children it appends, positionally — a pairing that only holds when
	// exactly one child was appended per source node, which is the case for
	// the element/text/comment/PI kinds (a document node flattens, and is not
	// paired).
	// The same positional pairing also carries the copied elements' in-scope
	// namespaces (below), so it is built unconditionally.
	var srcNodes []*xmltree.Node
	before := len(out.Children)
	beforeAttrs := len(out.Attrs)
	for _, it := range xpath.Flatten(xpath.Items(v)) {
		if nd, ok := it.(*xmltree.Node); ok {
			srcNodes = append(srcNodes, nd)
		}
	}
	// Arrays flatten into their members; nodes deep-copy, atomics become text.
	if err := emitSequenceValue(v, out); err != nil {
		return err
	}
	paired := len(srcNodes) > 0 && len(out.Children)-before == len(srcNodes)
	if n.copyAccumulators && paired {
		for i, src := range srcNodes {
			eng.recordAccOrigin(out.Children[before+i], src)
		}
	}
	if !n.noCopyNS && paired {
		// copy-namespaces="yes" (the default) copies ALL of the original
		// element's in-scope namespaces, not just the declarations written on
		// the element itself — a prefix bound several levels up in the source
		// is part of what the copy is (copy-3702/copy-1220). deepCopyInto only
		// carries an element's own NS list, which is what the descendants of
		// the copy need; the OUTERMOST copy needs the full set, exactly as
		// xsl:copy already gives it (copiedNamespaceNodes).
		for i, src := range srcNodes {
			dst := out.Children[before+i]
			if dst.Kind != xmltree.KindElement || src.Kind != xmltree.KindElement {
				continue
			}
			// NOTE: the suite's copy-1220/1221 additionally expect a copied
			// element NOT to inherit the constructing parent's namespaces
			// (an NSBarrier on dst and on noCopyNS's pruned copies).
			// Re-measured 2026-09-13 after fixing exclude-result-prefixes
			// "#all" (see excludedResultURIs): still a net loss, and worse
			// than previously recorded — copy-0614/0618/0620/0622/0624/0626
			// and namespace-4302 all regress (7 broken) to fix only
			// copy-1220 (1 gained; copy-1221 stays broken even so) — so the
			// copy keeps inheriting; those two stay as the known residual.
			nss := copiedNamespaceNodes(src)
			if len(nss) == 0 {
				continue
			}
			for _, ns := range nss {
				ns.Parent = dst
			}
			dst.NS = nss
		}
	}
	if n.noCopyNS {
		for _, c := range out.Children[before:] {
			pruneCopiedNamespaces(c)
		}
	}
	// Every node this instruction copied — children AND the attributes an
	// attribute-valued @select lands in out.Attrs instead — is a node the
	// request governs. Under "preserve" applyValidation is a genuine no-op
	// here (the deep copy already carried each annotation across via
	// CopyTypeInfo), which is exactly §24.4.1.1's rule for xsl:copy-of: unlike
	// xsl:copy, a copied ELEMENT keeps its annotation rather than dropping to
	// xs:anyType, because copy-of really does copy the content.
	if n.val != nil {
		// Document-level constraints (ID uniqueness, IDREF resolution) apply
		// only when what was COPIED is a document node — and by this point a
		// copied document has been flattened into out, so only the source
		// items still say so (copy-5011/5012 copy an RTF document and must
		// report XTTE1555; accumulator-073 copies loose elements whose IDREFs
		// legitimately point outside them and must not).
		// XTTE0950 (§11.9.1), both halves. Namespace-sensitive content is a
		// typed value of xs:QName/xs:NOTATION (or a derivative), whose prefix
		// only resolves through bindings the copy would lose: with
		// copy-namespaces="no" the bindings are not copied at all, and an
		// attribute copied without its parent element has no element to
		// resolve against. Both apply only under "preserve", which is the one
		// mode that keeps the annotation that makes the value sensitive.
		if n.val.mode == valPreserve && n.val.typ == nil {
			for _, c := range out.Children[before:] {
				if n.noCopyNS && nsSensitiveTree(c) {
					return errAt(n.el, "err:XTTE0950: copy-namespaces=\"no\" cannot copy %s, whose content is namespace-sensitive",
						c.Name.Local)
				}
			}
			for _, a := range out.Attrs[beforeAttrs:] {
				if nsSensitiveNode(a) {
					return errAt(n.el, "err:XTTE0950: the attribute %s has namespace-sensitive content and is being copied without its parent element",
						a.Name.Local)
				}
			}
		}
		copiedDoc := false
		for _, src := range srcNodes {
			if src.Kind == xmltree.KindDocument {
				copiedDoc = true
				break
			}
		}
		for _, c := range out.Children[before:] {
			if err := eng.applyValidationScoped(n.val, c, copiedDoc); err != nil {
				return err
			}
		}
		for _, a := range out.Attrs[beforeAttrs:] {
			if err := eng.applyValidationScoped(n.val, a, copiedDoc); err != nil {
				return err
			}
		}
	}
	return nil
}

// retainedBase computes the intrinsic xmltree.Node.Base a copied element
// should carry: the ORIGINAL node's own base-uri, computed against its real
// (pre-copy) ancestor chain — but only when the copy did NOT itself end up
// with a literal xml:base ATTRIBUTE of its own (a deep copy, unlike xsl:copy's
// shallow one, copies attributes, so it may already carry one). When it does,
// that attribute must be resolved fresh, against whatever base the copy is
// eventually queried in, not pre-baked from the original document's ancestors
// (base-uri-046: a deep copy of an element whose OWN xml:base is a *relative*
// URI resolves it against the STYLESHEET's base, not the source document's).
func retainedBase(el, orig *xmltree.Node) string {
	if _, ok := el.Attr("http://www.w3.org/XML/1998/namespace", "base"); ok {
		return ""
	}
	return xpath.NodeBaseURI(orig, "")
}

// deepCopyInto appends a deep copy of node under out.
// cloneNode returns a detached deep copy of a node with fresh identity, as
// fn:copy-of / fn:snapshot require. Every Document/Element level of the clone
// retains the corresponding ORIGINAL node's base-uri (xmltree.Node.Base) since
// the result is always a standalone, unembedded value (base-uri-024's rule,
// but exact at every depth since the original's own ancestor chain — not the
// clone's — is what's being preserved).
func cloneNode(n *xmltree.Node) *xmltree.Node {
	switch n.Kind {
	case xmltree.KindDocument:
		d := &xmltree.Node{Kind: xmltree.KindDocument, Base: xpath.NodeBaseURI(n, ""), Ephemeral: true}
		mergeUnparsed(d, n)
		for _, c := range n.Children {
			ch := cloneNode(c)
			ch.Parent = d
			d.Children = append(d.Children, ch)
		}
		return d
	case xmltree.KindElement:
		el := xmltree.NewElement(n.Name)
		el.NSBarrier = n.NSBarrier // see deepCopyInto's note on this field
		xmltree.CopyTypeInfo(el, n)
		for _, ns := range n.NS {
			el.NS = append(el.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: ns.Name, Value: ns.Value, Parent: el})
		}
		for _, a := range n.Attrs {
			xmltree.CopyTypeInfo(el.SetAttr(a.Name, a.Value), a)
		}
		el.Base = retainedBase(el, n)
		for _, c := range n.Children {
			ch := cloneNode(c)
			ch.Parent = el
			el.Children = append(el.Children, ch)
		}
		return el
	case xmltree.KindAttribute:
		a := &xmltree.Node{Kind: xmltree.KindAttribute, Name: n.Name, Value: n.Value}
		xmltree.CopyTypeInfo(a, n)
		return a
	case xmltree.KindText:
		t := xmltree.NewText(n.Value)
		xmltree.CopyTypeInfo(t, n)
		return t
	case xmltree.KindComment:
		return &xmltree.Node{Kind: xmltree.KindComment, Value: n.Value}
	case xmltree.KindPI:
		return &xmltree.Node{Kind: xmltree.KindPI, Name: n.Name, Value: n.Value}
	}
	return &xmltree.Node{Kind: n.Kind, Name: n.Name, Value: n.Value}
}

// textNode builds a text node, marking it raw (unescaped on serialization) when
// disable-output-escaping is in effect.
func textNode(s string, doe bool) *xmltree.Node {
	if doe {
		return xmltree.NewRawText(s)
	}
	return xmltree.NewText(s)
}

// restrictAccumulators applies an instruction's use-accumulators attribute
// (default: the empty list — no accumulator applies; "#all": unrestricted) to
// the document rooted at root, for as long as the instruction runs — the
// returned func lifts it again, since the resolver's cache may hand the same
// tree to a later fn:doc, where everything applies (merge-067: an accumulator
// rule reading a SECOND accumulator the merge-source never listed is
// XTDE3362).
func (eng *engine) restrictAccumulators(root, el *xmltree.Node) func() {
	if root == nil {
		return func() {}
	}
	set := map[string]bool{}
	if v, ok := el.AttrLocal("use-accumulators"); ok {
		for _, tok := range strings.Fields(v) {
			if tok == "#all" {
				return func() {}
			}
			qn := resolveQName(el, tok)
			set[clark(qn.Space, qn.Local)] = true
		}
	}
	if eng.accApplicable == nil {
		eng.accApplicable = map[*xmltree.Node]map[string]bool{}
	}
	prev, had := eng.accApplicable[root]
	eng.accApplicable[root] = set
	return func() {
		if had {
			eng.accApplicable[root] = prev
		} else {
			delete(eng.accApplicable, root)
		}
	}
}

// homeDoc returns the tree document(”)/fn:doc(”) (or the module's own URI)
// yields for a stylesheet module: the module read as a SOURCE document, which
// XSLT 3.0 §4.4 subjects to xsl:strip-space like any other — so, when the
// stylesheet strips at all, a stripped COPY rather than the compiled module
// tree itself, which must keep its whitespace for compilation (where-
// populated-100: document('self.xsl') under strip-space elements="*" must
// see no whitespace-only text nodes). Cached per module so repeated reads
// return the same nodes.
func (eng *engine) homeDoc(mod *xmltree.Node) *xmltree.Node {
	if mod == nil || len(eng.sheet.strip) == 0 {
		return mod
	}
	if d, ok := eng.homeDocs[mod]; ok {
		return d
	}
	cp := &xmltree.Node{Kind: xmltree.KindDocument, Base: mod.Base, Unparsed: mod.Unparsed}
	for _, c := range mod.Children {
		deepCopyInto(c, cp)
	}
	eng.sheet.applyStripSpace(cp)
	xmltree.AssignOrder(cp)
	if eng.homeDocs == nil {
		eng.homeDocs = map[*xmltree.Node]*xmltree.Node{}
	}
	eng.homeDocs[mod] = cp
	return cp
}

// applyStripSpace removes whitespace-only text children of source elements
// matched by xsl:strip-space, unless xsl:preserve-space or an ancestor
// xml:space="preserve" overrides.
func (ss *Stylesheet) applyStripSpace(doc *xmltree.Node) {
	if doc == nil || len(ss.strip) == 0 {
		return
	}
	ss.stripWalk(doc, false)
}

func (ss *Stylesheet) stripWalk(n *xmltree.Node, preserve bool) {
	if n.Kind == xmltree.KindElement {
		if v, ok := n.Attr("http://www.w3.org/XML/1998/namespace", "space"); ok {
			preserve = v == "preserve"
		}
		if !preserve && ss.spaceStripped(n.Name) && !simpleContentTyped(n) {
			kept := n.Children[:0:0]
			for _, c := range n.Children {
				if c.Kind == xmltree.KindText && strings.TrimSpace(c.Value) == "" {
					continue
				}
				kept = append(kept, c)
			}
			n.Children = kept
		}
	}
	for _, c := range n.Children {
		if c.Kind == xmltree.KindElement {
			ss.stripWalk(c, preserve)
		}
	}
}

// simpleContentTyped reports whether a source element's type annotation is a
// simple type, or a complex type with SIMPLE content — the one case XSLT 3.0
// §4.4 exempts from xsl:strip-space outright: "If an element in a source
// document has a type annotation that is a simple type or a complex type with
// simple content, then any whitespace text nodes among its children are
// preserved, regardless of any xsl:strip-space declarations. The reason for
// this is that stripping a whitespace text node from an element with simple
// content could make the element invalid: for example, it could cause the
// minLength facet to be violated."
//
// Simple content is exactly what leaves a TYPED VALUE on the element, so the
// annotation itself is the test — an element-only, mixed or empty complex type
// has no typed value and carries TypeAnno 0 (see xsd's typeAnnoFor). Inert on
// an unannotated node, which is every node in a non-schema-aware run, and
// correctly inert after input-type-annotations="strip", which the same section
// says runs first ("Stripping of type annotations happens before stripping of
// whitespace text nodes, so this situation will not occur").
func simpleContentTyped(n *xmltree.Node) bool {
	return xpath.IsTypeAnnotated(n) || n.ListTyped
}

// spaceStripped decides strip vs preserve for an element name: the more
// specific of the two lists wins (exact name > pre:* > *); preserve wins ties.
func (ss *Stylesheet) spaceStripped(name xmltree.Name) bool {
	sPrec, sRank, sOK := spaceMatchBest(ss.strip, name)
	pPrec, pRank, pOK := spaceMatchBest(ss.preserve, name)
	switch {
	case !sOK:
		return false
	case !pOK:
		return true
	case sPrec != pPrec:
		// Import precedence decides FIRST, regardless of which pattern is
		// more specific (strip-space-020: a higher-precedence module's
		// strip-space "abc:*" beats a lower-precedence module's exact
		// preserve-space "abc:x").
		return sPrec > pPrec
	default:
		// Same precedence: the more specific pattern wins; preserve wins a
		// genuine tie.
		return sRank > pRank
	}
}

// spaceMatchBest finds the matching spaceTest in set with the highest import
// precedence, breaking ties by specificity (exact name > pfx:*/*:local > *).
func spaceMatchBest(set []spaceTest, name xmltree.Name) (prec, rank int, ok bool) {
	rank = -1
	for _, t := range set {
		r := -1
		switch t.kind {
		case 0: // "*" — every element
			r = 0
		case 1: // wildcard: "pfx:*" (namespace match) or "*:local" (local match)
			if t.anyNS {
				if t.local == name.Local {
					r = 1
				}
			} else if t.space == name.Space {
				r = 1
			}
		case 2: // exact expanded-QName match (namespace URI + local)
			if t.space == name.Space && t.local == name.Local {
				r = 2
			}
		}
		if r < 0 {
			continue
		}
		if !ok || t.importPrec > prec || (t.importPrec == prec && r > rank) {
			prec, rank, ok = t.importPrec, r, true
		}
	}
	return prec, rank, ok
}

// appendAtomicText appends the string form of an atomic value to out. Per the
// XSLT content-construction rules adjacent atomic values are separated by a
// single space; intervening node content resets the adjacency. A zero-length
// value is NOT dropped here (unlike appendLiteralText below): it is still a
// discrete atomic ITEM for adjacency purposes, so it can be — and needs to
// remain — the anchor a later adjacent atomic value's separating space
// merges into (on-empty-113b/114a: xsl:sequence select="”" followed later by
// xsl:on-empty select="'|'" must still read "... |", the space coming from
// exactly this empty-but-present atomic node).
func appendAtomicText(out *xmltree.Node, s string) {
	if !out.NoAtomicMerge {
		if n := len(out.Children); n > 0 {
			if last := out.Children[n-1]; last.Kind == xmltree.KindText && last.Atomic {
				last.Value += " " + s
				return
			}
		}
	}
	t := xmltree.NewText(s)
	t.Atomic = true
	out.Append(t)
}

// appendLiteralText appends genuine literal text (a literal result-element
// text child, or xsl:text) to out, MERGING with an immediately preceding
// plain (non-atomic, matching disable-output-escaping) text node by direct
// concatenation — no separator, unlike appendAtomicText's computed-value
// merge. This is ordinary adjacent-text-node combination (two sibling text
// nodes with nothing of a different kind between them are one XDM text node,
// not two), not the "atomic values get a space" rule (select-2301: two
// adjacent xsl:text elements "t1"+"a" must read as one node "t1a"; "t2" +
// an empty xsl:text + "t3" likewise merge straight through to "t2t3"). It
// never merges with a preceding ATOMIC node — that boundary always resets
// adjacency for both kinds of merge, in either direction.
//
// Gated on !out.NoAtomicMerge exactly like appendAtomicText: building a
// discrete SEQUENCE (an @as-typed variable/param/function/xsl:sequence body)
// must keep each instruction's own contribution a separate item — several
// xsl:text instructions there are several sequence ITEMS, not one merged
// node (sequence-0101: @as="item()*" over three xsl:text "a"/"b"/"c" must
// atomize to 3 distinct items joined "a,b,c", not merge into one "abc").
//
// A zero-length string IS still appended/merged here (not dropped) — the
// standalone empty node it can produce still needs to act as an adjacency
// boundary for OTHER instructions' merging (on-empty-113b/114a: an
// intervening empty xsl:text between two xsl:sequence-produced atomic values
// must still block them from merging into each other). The complementary
// "a zero-length string contributes nothing at all" rule (element-0303/0304)
// is instead applied as a POST-CONSTRUCTION cleanup once a whole element's
// content is finalized — see stripEmptyLiteralText — precisely so it cannot
// perturb adjacency decisions made while content is still being built.
func appendLiteralText(out *xmltree.Node, s string, doe bool) {
	if !out.NoAtomicMerge {
		if n := len(out.Children); n > 0 {
			if last := out.Children[n-1]; last.Kind == xmltree.KindText && !last.Atomic && last.Raw == doe {
				last.Value += s
				return
			}
		}
	}
	out.Append(textNode(s, doe))
}

// checkSerializableItems reports SENR0001 when a map or array item would be
// written straight into a RESULT TREE that is going to be serialized by a
// method that has no representation for one (everything except json/adaptive).
// out is the principal result root (or an xsl:result-document root) exactly
// when it is a plain document node — an @as-typed value collector carries
// NoAtomicMerge/KeepDocItems and legitimately holds maps and arrays, and
// element content is already covered by XTDE0450 in emitSequenceValue.
func (eng *engine) checkSerializableItems(v xpath.Object, out *xmltree.Node) error {
	if out == nil || out.Kind != xmltree.KindDocument || out.NoAtomicMerge || out.KeepDocItems {
		return nil
	}
	switch eng.sheet.output.Method {
	case "json", "adaptive":
		return nil
	}
	for _, it := range xpath.Items(v) {
		switch it.(type) {
		case *xpath.Map, *xpath.Array:
			return errAt(nil, "err:SENR0001: a map or array cannot be serialized by the %q output method", eng.sheet.output.Method)
		}
	}
	return nil
}

// emitSequenceValue writes an evaluated sequence into content: arrays are
// flattened, nodes are deep-copied, atomic values become space-separated text.
func emitSequenceValue(v xpath.Object, out *xmltree.Node) error {
	return emitSequenceValueOpts(v, out, false, false)
}

// emitSequenceValueRef is emitSequenceValue for xsl:sequence, whose nodes are
// contributed by reference (see emitSequenceValueOpts).
func emitSequenceValueRef(v xpath.Object, out *xmltree.Node) error {
	return emitSequenceValueOpts(v, out, false, true)
}

// emitSequenceValueOpts is emitSequenceValue with control over whether a
// top-level text node's disable-output-escaping flag survives.
//
// keepRaw is true ONLY where the sequence being written is the body's own
// freshly-constructed result being handed to the destination it was always
// going to (an @as-typed xsl:template/xsl:function result): there the d-o-e
// request belongs to the very instruction that produced the node, exactly as
// if the body had written into the output directly (doe-0180..0182). It is
// false for xsl:copy-of and friends, where the node is a COPY of one captured
// in a temporary tree and d-o-e does not survive (doe-0183..0186).
func emitSequenceValueOpts(v xpath.Object, out *xmltree.Node, keepRaw, byRef bool) error {
	items := xpath.Items(v)
	if !out.NoAtomicMerge {
		// Constructing tree CONTENT (element/RTF): arrays flatten into their
		// members, XDM's content-construction flattening rule (an array
		// contributed to element content contributes each of its members as
		// a separate content item). A discrete-sequence collector
		// (out.NoAtomicMerge — an @as-typed variable/param/function body, or
		// an xsl:sequence feeding one) is NOT tree content being built, so an
		// array there must survive as a single array ITEM instead — e.g.
		// @as="array(*)" over a body of xsl:sequence select="[1,2,3]" must
		// keep one 3-member array, not flatten to three integers.
		items = xpath.Flatten(items)
	}
	for _, it := range items {
		if nd, ok := it.(*xmltree.Node); ok {
			if keepRaw && nd.Kind == xmltree.KindText && nd.Raw && !nd.Atomic {
				t := xmltree.NewText(nd.Value)
				t.Raw = true
				out.Append(t)
				continue
			}
			// XTDE0410: an attribute or namespace node in the sequence used to
			// construct an ELEMENT's content must not be preceded there by a
			// node of any other kind (copy-4702: xsl:copy-of of @* after a
			// literal <child/>). A discrete-sequence collector
			// (out.NoAtomicMerge — an @as-typed body or xsl:sequence result)
			// is not element content and legitimately holds attribute items
			// in any position.
			if (nd.Kind == xmltree.KindAttribute || nd.Kind == xmltree.KindNamespace) &&
				!out.NoAtomicMerge && out.Kind == xmltree.KindElement && len(out.Children) > 0 {
				return errAt(nil, "err:XTDE0410: an attribute or namespace node cannot be added to an element after its children")
			}
			if byRef && out.KeepDocItems && out.Kind == xmltree.KindDocument && !nd.SynthCtx &&
				(out.NoAtomicMerge || nd.Kind != xmltree.KindText || nd.Parent != nil) {
				// xsl:sequence (byRef) into a discrete-sequence collector
				// contributes the node ITSELF — it never copies — so the node
				// keeps its identity (function-1025 counts the distinct nodes
				// a deterministic function returned across an xsl:for-each).
				// It rides through the tree as a carrier exactly like a
				// map/array item does (RealItem; fragAsSequence unwraps it).
				// Every other route — element content, xsl:copy-of, coerced
				// @as results — keeps deep-copying, as construction must
				// (seqtor-036a: a copy-of'd document node flattens to its
				// text child, which then merges with adjacent text). A
				// collector whose declared type wants nodes (an xsl:function
				// as="node()*", NoAtomicMerge off so its atomics merge) still
				// takes elements/attributes/comments/PIs by reference — a
				// copied @author would lose its parent (function-1201's
				// $vSorted/../@title) — and likewise a TEXT node that belongs
				// to a tree (snapshot-0102a's f:graft-to-parent, as="node()",
				// returns a text child whose root() must stay its document);
				// only a fresh parentless text node keeps copying, so adjacent
				// constructed text merges as before.
				c := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true}
				c.RealItem = nd
				out.Append(c)
				continue
			}
			deepCopyInto(nd, out)
			continue
		}
		// A function item (which includes maps and arrays in the XDM
		// model — maps-006) directly inside the CONTENT OF AN ELEMENT NODE is
		// XTDE0450 (error-0450a); at the top level of the overall result
		// sequence (out.Kind == KindDocument: the principal result, an
		// xsl:result-document body, or an @as-typed value collector) it is
		// fine, notably under the adaptive/json output methods
		// (output-0707, result-document-1407) — appendAtomicItem enforces
		// exactly this distinction (out.Kind == KindElement) uniformly for
		// every non-node item kind.
		if err := appendAtomicItem(out, it); err != nil {
			return err
		}
	}
	return nil
}

// appendAtomicItem is appendAtomicText plus type preservation: a FRESH
// (non-merged) atomic text node also gets the item's real atomic type
// stamped via ItemAtomTypeTag, exactly like xsl:for-each's synthetic context-
// item nodes already do — so a later atomization of this node (fn:data(),
// xsl:key/key() comparison) recovers the item's true type instead of
// defaulting to xs:untypedAtomic (key-073/074/082: an xsl:key body of
// xsl:for-each/xsl:sequence over string-to-codepoints(.) must still compare
// its produced integers as NUMBERS, not as their digit text). A text run
// MERGED with a preceding atomic (two+ adjacent sequence-constructor items)
// no longer represents a single value, so it is left untagged — exactly as
// it already was before this value ever had a type to preserve.
func appendAtomicItem(out *xmltree.Node, it xpath.Item) error {
	switch it.(type) {
	case *xpath.Map, *xpath.Array, *xpath.Function:
		// A map/array/function item has no meaningful string value at all
		// (xpath.ToString returns "" for these) and can never merge with
		// adjacent atomic text the way a real atomic value can — carry the
		// REAL item through via RealItem instead (xpath.Items() recovers it
		// intact; see RealItem's own doc comment on xmltree.Node for the
		// invariant). Directly inside literal ELEMENT content this is
		// XTDE0450 (maps-006), exactly like the pre-existing Function-only
		// check this folds in — maps/arrays are function items too (a
		// distinct Go type here, but the same XDM function-item category).
		if out.Kind == xmltree.KindElement {
			return errAt(nil, "err:XTDE0450: a function item cannot appear in the constructed content of an element")
		}
		nd := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true}
		attachRealItem(nd, it)
		out.Append(nd)
		return nil
	}
	s := xpath.ToString(xpath.FromItems([]xpath.Item{it}))
	if !out.NoAtomicMerge {
		if n := len(out.Children); n > 0 {
			if last := out.Children[n-1]; last.Kind == xmltree.KindText && last.Atomic {
				last.Value += " " + s
				last.TypeAnno = 0
				return nil
			}
		}
	}
	t := xmltree.NewText(s)
	t.Atomic = true
	if tag, ok := xpath.ItemAtomTypeTag(it); ok {
		t.TypeAnno = tag
	}
	out.Append(t)
	return nil
}

func deepCopyInto(node *xmltree.Node, out *xmltree.Node) {
	if nd, ok := node.RealItem.(*xmltree.Node); ok {
		node = nd // a node carried by reference (see RealItem)
	}
	switch node.Kind {
	case xmltree.KindDocument:
		if out.KeepDocItems {
			// out is a discrete-sequence collector (an @as-typed body or
			// xsl:sequence result), not element/RTF content being built: a
			// document-node item (e.g. from fn:document/fn:doc) must stay a
			// document-node ITEM, not dissolve into its children the way it
			// would as literal element content, where a document node can
			// never legally appear as a child (as-0136: xsl:copy-of over
			// document('as-15.xml') under @as="document-node()*").
			out.Append(cloneNode(node))
			// The clone is APPENDED under the collector root, so a node in it
			// still walks up to `out` — give the collector the same unparsed
			// entities so unparsed-entity-uri() finds them from either.
			mergeUnparsed(out, node)
			return
		}
		mergeUnparsed(out, node)
		for _, c := range node.Children {
			deepCopyInto(c, out)
		}
	case xmltree.KindElement:
		el := xmltree.NewElement(node.Name)
		// A namespace-inheritance barrier (inherit-namespaces="no") is part of
		// what the element's in-scope namespaces ARE, so a copy has to keep it
		// — otherwise the copy silently re-inherits the ambient bindings at its
		// new position and the whole point of the attribute is lost as soon as
		// the constructed element passes through an @as-typed body, an
		// xsl:copy-of, or the result-tree copy (namespace-0913/0914).
		el.NSBarrier = node.NSBarrier
		xmltree.CopyTypeInfo(el, node)
		for _, ns := range node.NS {
			el.NS = append(el.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: ns.Name, Value: ns.Value, Parent: el})
		}
		for _, a := range node.Attrs {
			xmltree.CopyTypeInfo(el.SetAttr(a.Name, a.Value), a)
		}
		// xsl:copy-of/fn:copy-of retains the ORIGINAL node's base-uri only
		// when this (outermost) copy is the raw, unwrapped result of an
		// @as-typed body (out.NoAtomicMerge) — base-uri-039/042: embedded as
		// RTF/element content instead, it derives its base-uri from its new
		// position, so el.Base stays unset (deeper recursive calls below
		// always pass a fresh, non-NoAtomicMerge out, so descendants never
		// retain either — only the outermost copy can).
		if out.NoAtomicMerge {
			el.Base = retainedBase(el, node)
		}
		out.Append(el)
		for _, c := range node.Children {
			deepCopyInto(c, el)
		}
	case xmltree.KindText:
		// A text node that is itself a synthetic stand-in for an atomic
		// value — the "." context item of a for-each/analyze-string
		// iteration over a non-node sequence — keeps the "adjacent atomics
		// get a space separator" behavior when copied via xsl:sequence
		// select="." (analyze-string-091b: 6 separate non-matching-substring
		// executions each copying their single-char "." must still join with
		// spaces, exactly as if they were 6 items of one xsl:sequence).
		if node.Atomic {
			appendAtomicText(out, node.Value)
		} else {
			// NOTE: disable-output-escaping is deliberately NOT carried over
			// by a copy. XSLT §20: the d-o-e request applies only to the text
			// node the instruction itself writes into a final result tree; a
			// text node captured in a temporary tree and later copied out is
			// escaped normally (doe-0183..0186).
			t := xmltree.NewText(node.Value)
			xmltree.CopyTypeInfo(t, node)
			out.Append(t)
		}
	case xmltree.KindAttribute:
		xmltree.CopyTypeInfo(appendAttrItem(out, node.Name, node.Value), node)
	case xmltree.KindNamespace:
		// A namespace-node ITEM (e.g. xsl:copy-of over a namespace:: axis
		// step) contributes a namespace binding to the constructed element —
		// the serializer's own conflict resolution then renumbers whichever
		// OTHER binding (e.g. the element's own name prefix) collides with
		// this explicit one, exactly as a real xsl:namespace would
		// (namespace-2001).
		out.NS = append(out.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: node.Name, Value: node.Value, Parent: out})
	case xmltree.KindComment:
		out.Append(&xmltree.Node{Kind: xmltree.KindComment, Value: node.Value})
	case xmltree.KindPI:
		out.Append(&xmltree.Node{Kind: xmltree.KindPI, Name: node.Name, Value: node.Value})
	}
}

// appendAttrItem adds an attribute NODE to out. Building a discrete SEQUENCE
// (out.NoAtomicMerge — an @as-typed body/xsl:sequence result) always appends
// a fresh node: several same-named attribute items can legitimately coexist
// as distinct sequence members there (as-1402: copying 3 same-named @attrib
// nodes from the source into "attribute(attrib, xs:untypedAtomic)+" must
// yield 3 items). Building real element/RTF content instead uses SetAttr's
// by-name uniqueness, matching how attributes actually work on one element.
func appendAttrItem(out *xmltree.Node, name xmltree.Name, value string) *xmltree.Node {
	// A discrete-sequence collector — NoAtomicMerge, or a KeepDocItems
	// document collector whose atomics DO merge because its declared type
	// wants nodes (an xsl:function as="node()*") — holds every attribute as
	// its own item: nine sorted @author attributes are nine items, not one
	// same-named attribute overwritten eight times (function-1201). A
	// document node never legitimately owns attributes, so this is the only
	// reading a document collector can have.
	if out.NoAtomicMerge || (out.Kind == xmltree.KindDocument && out.KeepDocItems) {
		a := xmltree.NewAttribute(name, value)
		a.Parent = out
		// Remember where in the sequence this standalone attribute was built,
		// so fragAsSequence can put it back there instead of hoisting every
		// attribute in front of the children (sequence-0102, result-document-
		// 0304). See Node.SeqIndex.
		a.SeqIndex = len(out.Children)
		out.Attrs = append(out.Attrs, a)
		return a
	}
	return out.SetAttr(attrFixupName(out, name), value)
}

// attrFixupName applies XSLT namespace fixup (§5.7.3) to an attribute being
// added to a constructed element: when its prefix is already bound on that
// element to a DIFFERENT namespace URI, the attribute gets a fresh prefix so
// both bindings can coexist. Without it two attributes copied from different
// namespaces but written with the same prefix end up indistinguishable — same
// lexical name, and only one declarable binding (bug-1501/1601). The expanded
// name is untouched, so SetAttr's by-expanded-name replacement is unaffected.
func attrFixupName(out *xmltree.Node, name xmltree.Name) xmltree.Name {
	if out.Kind != xmltree.KindElement || name.Space == "" || name.Prefix == "" {
		return name
	}
	if !attrPrefixConflicts(out, name) {
		return name
	}
	for i := 1; ; i++ {
		cand := name
		cand.Prefix = name.Prefix + "_" + strconv.Itoa(i)
		if !attrPrefixConflicts(out, cand) {
			return cand
		}
	}
}

func attrPrefixConflicts(out *xmltree.Node, name xmltree.Name) bool {
	if out.Name.Prefix == name.Prefix && out.Name.Space != name.Space {
		return true
	}
	for _, ns := range out.NS {
		if ns.Name.Local == name.Prefix && ns.Value != name.Space {
			return true
		}
	}
	for _, a := range out.Attrs {
		if a.Name.Prefix == name.Prefix && a.Name.Space != name.Space {
			return true
		}
	}
	return false
}

func (eng *engine) execAnalyzeString(n *analyzeString, r rt, out *xmltree.Node) error {
	selVal, err := eng.eval(n.sel, n.el, r)
	if err != nil {
		return err
	}
	// @select is xs:string? under the function conversion rules — a
	// genuinely-typed non-string atomic (a number: analyze-string-001) or a
	// sequence of more than one item (analyze-string-003) does not coerce,
	// unlike fn:string()'s lenient any-atomic acceptance.
	cv, ok := xpath.CoerceToDeclaredTypeCtx("xs:string?", selVal, asTypeCtx(n.el))
	if !ok {
		return errAt(n.el, "err:XPTY0004: xsl:analyze-string/@select does not match the required type xs:string?")
	}
	input := xpath.ToString(cv)
	pattern, err := eng.evalAVT(n.regex, n.el, r)
	if err != nil {
		return err
	}
	flags := ""
	if n.flags != nil {
		if flags, err = eng.evalAVT(n.flags, n.el, r); err != nil {
			return err
		}
	}
	re, err := xpath.CompileRegexStrict(pattern, flags)
	if err != nil {
		return errAt(n.el, "bad regex %q: %v", pattern, err)
	}

	// The context position/size inside matching/non-matching-substring count
	// through the WHOLE combined sequence of matching and non-matching
	// substrings (analyze-string-033/083: position() runs 1..N and last()
	// is the total substring count, not a fixed 1/1 per substring) — so the
	// segments are collected first and only then executed, once the total
	// count is known.
	type segment struct {
		text     string
		matching bool
		groups   []string
	}
	var segs []segment
	matches := re.FindAllStringSubmatchIndex(input, -1)
	last := 0
	for _, m := range matches {
		s, e := m[0], m[1]
		if s > last {
			segs = append(segs, segment{text: input[last:s]})
		}
		groups := make([]string, len(m)/2)
		for gi := 0; gi*2 < len(m); gi++ {
			a, b := m[gi*2], m[gi*2+1]
			if a >= 0 {
				groups[gi] = input[a:b]
			}
		}
		segs = append(segs, segment{text: input[s:e], matching: true, groups: groups})
		last = e
	}
	if last < len(input) {
		segs = append(segs, segment{text: input[last:]})
	}

	size := len(segs)
	for i, sg := range segs {
		body := n.nonMatching
		if sg.matching {
			body = n.matching
			eng.regexGroups = append(eng.regexGroups, sg.groups)
		}
		// The context item inside matching/non-matching-substring is the
		// substring; model it as a synthetic text node so "." works. Atomic
		// is set so a plain "xsl:sequence select='.'" copy of it still joins
		// with a space against an adjacent atomic from another iteration,
		// like any other sequence-constructor-contributed atomic value
		// (analyze-string-091b).
		synthetic := &xmltree.Node{Kind: xmltree.KindText, Value: sg.text, Atomic: true, SynthCtx: true}
		err := eng.execSequence(body, rt{node: synthetic, pos: i + 1, size: size}, out)
		if sg.matching {
			eng.regexGroups = eng.regexGroups[:len(eng.regexGroups)-1]
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// keyIndexFor lazily builds (and caches) the index for an xsl:key by name:
// key value -> matching nodes.
// keyCacheKey identifies a built key index: the key name and the root of the
// document (or temporary tree) it indexes.
type keyCacheKey struct {
	name string
	root *xmltree.Node
}

// rootOfNode walks to the top of the tree containing n.
func rootOfNode(n *xmltree.Node) *xmltree.Node {
	for n != nil && n.Parent != nil {
		n = n.Parent
	}
	return n
}

// keyNodeString returns the canonical xsl:key index/lookup string for one
// item of a NodeSet-shaped use/search value. A genuine document or
// constructed-tree node compares by its own string-value, as always. But an
// xsl:for-each/filter/analyze-string context item over an ATOMIC sequence is
// modelled as a synthetic, parentless text node stamped with
// ItemAtomTypeTag (see execForEach) purely so "." can address it — it is not
// a real node and must compare with the item's actual type (KeyString),
// not its raw digit/lexical text, or a typed use value (dateTime, a number)
// can never be found via such a context item (key-073/074/075: "97 to 105"
// range items looked up against an xs:integer(.)-typed use).
func keyNodeString(n *xmltree.Node) string {
	if n.Kind == xmltree.KindText && xpath.IsTypeAnnotated(n) {
		return xpath.KeyString(n)
	}
	// A REAL node annotated xs:QName/xs:NOTATION is the one other case whose
	// string value provably does not determine its value: the value is the
	// EXPANDED name, and the prefix written in the document is lexical
	// spelling only (XSD 1.0 Part 2 §3.2.18/§3.2.19). Three attributes
	// spelled one:mp3, first:mp3 and mp3 in a default-namespaced document all
	// hold the same value and must index under one key (notation-0305).
	// Deliberately narrow: every other annotation's string value is a
	// perfectly good key already, and routing those through KeyString would
	// re-tag them ("n:" for numerics) out of reach of an untyped lookup.
	if at, ok := xpath.NodeAnnotation(n); ok && (at == xpath.XSqname || at == xpath.XSnotation) {
		return xpath.KeyString(n)
	}
	return n.StringValue()
}

func (eng *engine) keyIndexFor(name string) map[string][]*xmltree.Node {
	idx, _ := eng.keyIndexOn(name, eng.doc)
	return idx
}

// evalKeyUse computes an xsl:key declaration's value for the current node:
// its @use expression, or (when @use is absent) its sequence-constructor
// body, executed exactly like an @as-typed xsl:variable body — a raw
// SEQUENCE of the constructed items, not a document-fragment/string
// (key-073/074: xsl:for-each/xsl:sequence content computing the values).
func (eng *engine) evalKeyUse(kd *KeyDef, r rt) (xpath.Object, error) {
	// XSLT 3.0 "temporary output state" (5.7.1): xsl:result-document is not
	// allowed while evaluating an xsl:key's value (err:XTDE1480).
	eng.tempOutputDepth++
	defer func() { eng.tempOutputDepth-- }()
	if kd.use != nil {
		return eng.eval(kd.use, kd.el, r)
	}
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
	if err := eng.execSequence(kd.body, r, frag); err != nil {
		return nil, err
	}
	return fragAsSequence(frag), nil
}

// keyIndexOn lazily builds (and caches) the index for an xsl:key over the tree
// rooted at root (the source document or a temporary tree). A circular key
// definition (the key's match/use referencing itself) is XTDE0640.
func (eng *engine) keyIndexOn(name string, root *xmltree.Node) (map[string][]*xmltree.Node, error) {
	eng.depth++
	defer func() { eng.depth-- }()
	if eng.depth > maxTemplateDepth || root == nil {
		return map[string][]*xmltree.Node{}, nil
	}
	// Building a key index means walking the entire tree (walkAll below). Over
	// a document being read incrementally right now that walk sees only the one
	// record currently attached, and would hand back an index silently missing
	// everything already processed and everything not yet read. key() is
	// reachable indirectly — from a global variable, a stylesheet function, a
	// match pattern — where no scan of the streamed body could have seen it
	// coming, so the refusal has to be here, at the walk itself.
	if strbStreaming(eng, root) {
		return nil, errAt(nil, "err:XTSE3430: key %q cannot be used over a document being streamed (building its index would read the whole document)", name)
	}
	if eng.keyBuilding == nil {
		eng.keyBuilding = map[string]bool{}
	}
	if eng.keyBuilding[name] {
		return nil, errAt(nil, "err:XTDE0640: circular definition of xsl:key %q", name)
	}
	if eng.keyIndex == nil {
		eng.keyIndex = map[keyCacheKey]map[string][]*xmltree.Node{}
	}
	ck := keyCacheKey{name: name, root: root}
	if idx, ok := eng.keyIndex[ck]; ok {
		return idx, nil
	}
	eng.keyBuilding[name] = true
	defer delete(eng.keyBuilding, name)
	idx := map[string][]*xmltree.Node{}
	var walkErr error
	for _, kd := range eng.sheet.keys {
		if kd.name != name {
			continue
		}
		eng.walkAll(root, func(node *xmltree.Node) {
			if walkErr != nil {
				return
			}
			env := &evalEnv{eng: eng, el: kd.el, current: node}
			eng.patternDepth++
			ok, err := kd.match.Match(node, &xpath.Context{Node: node, CtxItem: realItemOf(node), Pos: 1, Size: 1, Vars: env, NS: env, Funcs: env, Resolver: eng.resolver, NoOutputURI: true, DefaultElemNS: xpathDefaultNS(env.el), Now: eng.now, SchemaTypes: schemaTypesFor(env.el)})
			eng.patternDepth--
			if err != nil {
				// A circular key definition surfaces here (XTDE0640).
				if strings.Contains(err.Error(), "XTDE0640") {
					walkErr = err
				}
				return
			}
			if !ok {
				return
			}
			val, err := eng.evalKeyUse(kd, rt{node: node, pos: 1, size: 1})
			if err != nil {
				// A circular key definition (XTDE0640) or an attempt to use
				// xsl:result-document while evaluating the key's value
				// (XTDE1480, temporary output state — result-document-1141)
				// must surface; any other dynamic error is swallowed here
				// (a key index is built eagerly over every matching node, so
				// an unrelated error on a node the actual key() call never
				// needed must not fail the whole lookup).
				if strings.Contains(err.Error(), "XTDE0640") || strings.Contains(err.Error(), "XTDE1480") {
					walkErr = err
				}
				return
			}
			if kd.composite {
				// @composite="yes": the use value's items together form ONE
				// compound tuple key (key-096/097), not a multi-value key —
				// the opposite of the composite="no" default just below.
				k := canonKeyString(xpath.CompositeKeyString(xpath.Items(val)), kd.collURI)
				idx[k] = append(idx[k], node)
				return
			}
			if ns, ok := xpath.ToNodeSet(val); ok {
				for _, u := range ns {
					k := canonKeyString(keyNodeString(u), kd.collURI)
					idx[k] = append(idx[k], node)
				}
			} else {
				// A non-node (typed atomic) use value: index the node under
				// EVERY item of the sequence, not just a string of the whole
				// thing — a multi-item use result (key-073/074/075:
				// string-to-codepoints(.) yielding one entry per character) is
				// a genuine multi-value key, exactly like a use expression
				// selecting several nodes. Each item's canonical "eq"-class
				// string keeps typed comparisons (dateTime instants, numeric
				// value space, NaN never matching) correct (key-069/070), and
				// @collation/@default-collation re-canonicalizes string-family
				// keys so a case-insensitive lookup finds them (collations-0105).
				for _, it := range xpath.Items(val) {
					k := canonKeyString(xpath.KeyString(it), kd.collURI)
					idx[k] = append(idx[k], node)
				}
			}
		})
	}
	eng.keyIndex[ck] = idx
	if walkErr == nil && eng.pendingErr != nil {
		walkErr, eng.pendingErr = eng.pendingErr, nil
	}
	if walkErr != nil {
		return nil, walkErr
	}
	return idx, nil
}

// walkAll visits every element (and its attributes) plus text nodes in the tree.
// walkAll visits every node of a tree in document order: the node itself, its
// attribute nodes, its NAMESPACE nodes (XSLT 3.0 lets an xsl:key match pattern
// select namespace nodes — key-087/090), then its children.
func (eng *engine) walkAll(n *xmltree.Node, fn func(*xmltree.Node)) {
	fn(n)
	for _, a := range n.Attrs {
		fn(a)
	}
	if n.Kind == xmltree.KindElement {
		for _, ns := range xpath.InScopeNamespaceNodes(n) {
			fn(ns)
		}
	}
	for _, c := range n.Children {
		eng.walkAll(c, fn)
	}
}

// --- helpers ----------------------------------------------------------------

func (eng *engine) evalAVT(a *avt, el *xmltree.Node, r rt) (string, error) {
	if a == nil {
		return "", nil
	}
	if lit, ok := a.isConstant(); ok {
		return lit, nil
	}
	var b strings.Builder
	for _, part := range a.parts {
		if part.expr == nil {
			b.WriteString(part.literal)
			continue
		}
		// An AVT's {expr} is stringified the same way as constructing simple
		// content (XSLT 3.0 §5.6.2): a multi-item sequence result joins with a
		// single space, not just its first item's string (function-1013:
		// "{$x[position()]}" over a 10-item sequence must print all 10 values).
		//
		// XSLT 1.0 backwards-compatible processing reverts this to the
		// FIRST item's string value only (backwards-010's own point: "effect
		// of BC on AVTs").
		v, err := eng.eval(part.expr, elOrDoc(el, eng.doc), r)
		if err != nil {
			return "", err
		}
		if eng.bc10At(el) {
			b.WriteString(xpath.ToString(v))
		} else {
			b.WriteString(joinSeq(v, " "))
		}
	}
	return b.String(), nil
}

// simpleContentValue computes a simple-content constructor's string value for
// an instruction whose separator cannot be changed (xsl:comment,
// xsl:processing-instruction, xsl:namespace): XSLT 3.0 §5.8.2 fixes it at a
// single space for BOTH the @select and the sequence-constructor form
// (bug-0305, construct-node-032/033, copy-4101/4102, bug-1404).
func (eng *engine) simpleContentValue(sel *xpath.Parsed, body []instruction, el *xmltree.Node, r rt) (string, error) {
	if sel != nil {
		v, err := eng.eval(sel, el, r)
		if err != nil {
			return "", err
		}
		return joinSeq(v, " "), nil
	}
	return eng.stringFromBody(body, r, " ")
}

// stringFromBody computes the string value of a simple-content constructor
// given as a sequence constructor, per XSLT 3.0 §5.8.2 with the supplied
// separator.
func (eng *engine) stringFromBody(body []instruction, r rt, sep string) (string, error) {
	items, err := eng.sequenceFromBody(body, r)
	if err != nil {
		return "", err
	}
	return simpleContentJoin(items, sep, true), nil
}

// sequenceFromBody runs a sequence constructor as a DISCRETE sequence rather
// than as result-tree-fragment content (NoAtomicMerge), so that a simple-
// content constructor can apply the §5.8.2 rules to its individual items:
// adjacent atomic values must be joined by the instruction's separator, not
// pre-merged with the single space that building element content would use.
func (eng *engine) sequenceFromBody(body []instruction, r rt) ([]xpath.Item, error) {
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true, Ephemeral: true}
	if err := eng.execSequence(body, r, frag); err != nil {
		return nil, err
	}
	items := make([]xpath.Item, 0, len(frag.Children)+len(frag.Attrs))
	for _, c := range frag.Children {
		if nd, ok := c.RealItem.(*xmltree.Node); ok {
			// A node xsl:sequence contributed by reference (see
			// emitSequenceValueOpts) — the item is that node, not its
			// carrier (seqtor-036c: three sequenced " " text nodes merge
			// with the literal text between them).
			items = append(items, nd)
			continue
		}
		items = append(items, c)
	}
	// An attribute node produced directly by the body is an item of the result
	// sequence too, atomized like any other (namespace-2602: an xsl:namespace
	// whose body is an xsl:attribute carrying the URI). The fragment keeps
	// attributes out-of-band, so they are appended after the child items.
	for _, a := range frag.Attrs {
		items = append(items, a)
	}
	return items, nil
}

// simpleContentJoin implements steps 1-5 of XSLT 3.0 §5.8.2 "Constructing
// Simple Content" over an already-evaluated result sequence: zero-length text
// nodes are discarded, adjacent text nodes are merged into one (so a separator
// is never inserted between them), the remaining items are atomized and cast
// to strings, and those strings are concatenated with sep between successive
// pairs.
//
// A text node standing in for an ATOMIC value (Node.Atomic — how this engine
// models a bare atomic item inside constructed content) is an atomic value for
// these purposes, not a text node: it neither merges with its neighbours nor
// disappears when zero-length (construct-node-013/018).
//
// mergeText applies step 2 and is set only for the sequence-constructor form.
// A sequence delivered by an XPath @select carries its atomic values as real
// atomic items EXCEPT where this engine has to model an atomic context item as
// a parentless synthetic text node ("." inside xsl:for-each over atomics,
// current-group(), current-merge-group()); merging those would wrongly swallow
// the separator between genuinely distinct atomic values
// (for-each-group-046a, merge-025).
func simpleContentJoin(items []xpath.Item, sep string, mergeText bool) string {
	parts := make([]string, 0, len(items))
	prevText, prevSynth := false, false
	for _, it := range items {
		if nd, ok := it.(*xmltree.Node); ok && nd.Kind == xmltree.KindText && !nd.Atomic {
			if nd.Value == "" {
				continue // step 1: zero-length text nodes are discarded
			}
			// Step 2 — adjacent text nodes are merged. On the @select path
			// (mergeText false) it still applies to GENUINE text nodes: the
			// exemption above is about this engine's synthetic stand-ins
			// (SynthCtx), never about real ones, and a @select delivering
			// several real text nodes must concatenate them with no separator
			// (seqtor-043d: ten xsl:function results declared as="text()").
			if prevText && (mergeText || (!nd.SynthCtx && !prevSynth)) {
				parts[len(parts)-1] += nd.Value
				continue
			}
			parts = append(parts, nd.Value)
			prevText, prevSynth = true, nd.SynthCtx
			continue
		}
		prevText, prevSynth = false, false
		// Step 3 ATOMIZES, and a schema-validated element or attribute's
		// atomized value is its TYPED value, not its lexical form: an
		// attribute of type xs:integer written as "003" contributes "3"
		// (import-schema-002/003/004). ToString below would give the string
		// value, which §5.8.2 reaches only for an untyped node — where the
		// two coincide.
		if nd, ok := it.(*xmltree.Node); ok {
			if s, ok := xpath.TypedNodeLexical(nd); ok {
				parts = append(parts, s)
				continue
			}
		}
		parts = append(parts, xpath.ToString(xpath.FromItems([]xpath.Item{it})))
	}
	return strings.Join(parts, sep)
}

// resolveSortAttrs evaluates one xsl:sort key's data-type/order attribute
// value templates (sort-041/042: a stylesheet may compute data-type from a
// variable) once — per XSLT convention these are not expected to vary across
// the items of a single sort, so evaluating them once against ctx (the
// context of the containing instruction) rather than per item is sufficient.
func (eng *engine) resolveSortAttrs(sk sortKey, el *xmltree.Node, ctx rt) (dataType, order, caseOrder string, coll func(a, b string) int, err error) {
	dataType, order = "text", "ascending"
	if sk.dataType != nil {
		if dataType, err = eng.evalAVT(sk.dataType, el, ctx); err != nil {
			return "", "", "", nil, err
		}
	}
	if sk.order != nil {
		if order, err = eng.evalAVT(sk.order, el, ctx); err != nil {
			return "", "", "", nil, err
		}
	}
	if sk.collation != nil {
		uri, cerr := eng.evalAVT(sk.collation, el, ctx)
		if cerr != nil {
			return "", "", "", nil, cerr
		}
		// A collation URI the engine cannot resolve is a dynamic error
		// (sort-027), not a silent fallback to the default collation.
		if coll, cerr = xpath.ResolveCollator(uri); cerr != nil {
			return "", "", "", nil, errAt(sk.el, "err:XTDE1035: unsupported collation %q", uri)
		}
	} else {
		// No explicit @collation: fall back to the in-scope default collation
		// (collations-0101/0104 use this via a default-collation attribute).
		coll = eng.ambientDefaultCollation(el)
	}
	if sk.caseOrder != nil && coll == nil {
		// case-order has an effect only under the default, codepoint
		// collation (collations-0201): an explicit OR ambient default
		// @collation makes it a no-op here, same as a real collation-aware
		// comparator would ignore it too.
		if caseOrder, err = eng.evalAVT(sk.caseOrder, el, ctx); err != nil {
			return "", "", "", nil, err
		}
	}
	if sk.lang != nil {
		lang, lerr := eng.evalAVT(sk.lang, el, ctx)
		if lerr != nil {
			return "", "", "", nil, lerr
		}
		// A literal (non-AVT) value was already checked at compile time
		// (XTSE0020); a computed AVT value (sort-029) is checked here,
		// dynamically, the first time it's actually evaluated (XTDE0030).
		if lang != "" && !isValidLangLiteral(lang) {
			return "", "", "", nil, errAt(sk.el, "err:XTDE0030: invalid value %q for xsl:sort/@lang", lang)
		}
		// NOTE: @lang is NOT turned into a locale-tailored collation here, the
		// way xsl:merge-key's is. Measured: it wins nothing in the suite and
		// costs sort-018, whose "en"+case-order="upper-first" ordering the
		// engine's own case-order tiebreak produces and a UCA collator does
		// not (golang.org/x/text/collate has no caseFirst control). xsl:merge
		// needs the tailoring — its inputs are pre-sorted in Swedish order and
		// are otherwise rejected as unsorted — and xsl:sort does not.
	}
	return dataType, order, caseOrder, coll, nil
}

// sortKeyValue evaluates one xsl:sort key for one item, returning both the
// raw value (for data-type inference) and its string form (for compareKey).
// outerEl is the containing instruction (xsl:for-each/apply-templates/
// perform-sort/xsl:for-each-group) — @select is evaluated against it, exactly
// as before this function existed. A body (no @select) is evaluated against
// the xsl:sort element's own scope instead.
func (eng *engine) sortKeyValue(sk sortKey, outerEl *xmltree.Node, ctx rt) (xpath.Object, string, error) {
	switch {
	case sk.sel != nil:
		v, err := eng.eval(sk.sel, outerEl, ctx)
		if err != nil {
			return nil, "", err
		}
		// XSLT 1.0 backwards-compatible processing takes only the FIRST item
		// of a multi-item sort key instead of raising XTTE1020
		// (backwards-012: select="(-., 'banana')" sorts by -POSITION alone).
		if eng.bc10At(sk.el) {
			if items := xpath.Items(v); len(items) > 1 {
				v = xpath.FromItems(items[:1])
			}
		}
		return v, xpath.ToString(v), nil
	case len(sk.body) > 0:
		// Fast path: a body of exactly one xsl:sequence[@select] contributes
		// its raw evaluated value directly — mirrors evalVarDef's identical
		// shortcut, for the same reason: the general tree round-trip below
		// cannot preserve an item's exact atomic type (needed here so a
		// numeric key computed this way, e.g. sort-051's round(.), is still
		// recognized as numeric for the default data-type inference).
		if len(sk.body) == 1 {
			if si, ok := sk.body[0].(*sequenceInstr); ok && si.sel != nil {
				v, err := eng.eval(si.sel, si.el, ctx)
				if err != nil {
					return nil, "", err
				}
				return v, xpath.ToString(v), nil
			}
		}
		frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
		if err := eng.execSequence(sk.body, ctx, frag); err != nil {
			return nil, "", err
		}
		v := fragAsSequence(frag)
		return v, xpath.ToString(v), nil
	default:
		return nil, ctx.node.StringValue(), nil
	}
}

// sortKeyIsNumeric reports whether v is a single, genuinely numeric atomic
// value (empty is true for an empty-sequence key, which doesn't count either
// way toward the default data-type="number" inference in sortNodes).
func sortKeyIsNumeric(v xpath.Object) (numeric, empty bool) {
	items, err := xpath.Atomize(v)
	if err != nil || len(items) == 0 {
		return false, true
	}
	if len(items) != 1 {
		return false, false
	}
	a, ok := items[0].(*xpath.Atomic)
	return ok && a.IsNumeric(), false
}

// sortKeyIsOrderableNonString mirrors sortKeyIsNumeric for the date/time/
// duration family (see xpath.Atomic.IsOrderableNonString).
func sortKeyIsOrderableNonString(v xpath.Object) (orderable, empty bool) {
	items, err := xpath.Atomize(v)
	if err != nil || len(items) == 0 {
		return false, true
	}
	if len(items) != 1 {
		return false, false
	}
	a, ok := items[0].(*xpath.Atomic)
	return ok && a.IsOrderableNonString(), false
}

// sortKeyIsUnorderedDuration reports whether v atomizes to a single general
// xs:duration value (date-053: sorting by that type, with no @data-type, is
// XTDE1030 — it has no defined 'lt' ordering, unlike yearMonthDuration/
// dayTimeDuration, which sortKeyIsOrderableNonString already handles).
func sortKeyIsUnorderedDuration(v xpath.Object) bool {
	items, err := xpath.Atomize(v)
	if err != nil || len(items) != 1 {
		return false
	}
	a, ok := items[0].(*xpath.Atomic)
	return ok && a.IsUnorderedDuration()
}

// sortValueDataType is an internal compareKey/dataTypes sentinel (never a
// real @data-type value) meaning "compare the raw atomic values, not their
// string form" — sortNodes' default inference for date/time/duration keys.
const sortValueDataType = "\x00value"

// compareAtomicKey compares two sort key values by their typed atomic value
// (xpath.CompareAtomic), for the sortValueDataType default. An empty
// sequence sorts before every non-empty key, in both ascending and
// descending order, matching compareKey's NaN handling for "number".
func compareAtomicKey(a, b xpath.Object) int {
	ai, aErr := xpath.Atomize(a)
	bi, bErr := xpath.Atomize(b)
	aEmpty := aErr != nil || len(ai) == 0
	bEmpty := bErr != nil || len(bi) == 0
	switch {
	case aEmpty && bEmpty:
		return 0
	case aEmpty:
		return -1
	case bEmpty:
		return 1
	}
	aa, aok := ai[0].(*xpath.Atomic)
	ba, bok := bi[0].(*xpath.Atomic)
	if !aok || !bok {
		return 0
	}
	if cmp, ok := xpath.CompareAtomic(aa, ba); ok {
		return cmp
	}
	return 0
}

func (eng *engine) sortNodes(nodes []*xmltree.Node, sorts []sortKey, el *xmltree.Node, ctx rt) ([]*xmltree.Node, error) {
	type keyed struct {
		node *xmltree.Node
		keys []string
		vals []xpath.Object
	}
	dataTypes := make([]string, len(sorts))
	orders := make([]string, len(sorts))
	caseOrders := make([]string, len(sorts))
	colls := make([]func(a, b string) int, len(sorts))
	for si, sk := range sorts {
		dt, ord, co, coll, err := eng.resolveSortAttrs(sk, el, ctx)
		if err != nil {
			return nil, err
		}
		dataTypes[si], orders[si], caseOrders[si], colls[si] = dt, ord, co, coll
	}
	// XSLT 2.0+ §14.3: when @data-type is omitted, a sort key whose value is
	// always numeric (across every item) defaults to data-type="number"
	// instead of "text" (sort-013/015/051/062). allNumeric[ki] tracks that
	// per key column, based on the key's REAL atomic type (not its lexical
	// shape), so a numeric-looking but genuinely textual key never flips.
	// allOrderable[ki] tracks the same thing for the date/time/duration
	// family: with no @data-type, such keys default to comparison BY VALUE
	// (chronological/by magnitude), not by their lexical string form
	// (date-019/date-054: dateTime/yearMonthDuration keys sort in instant/
	// magnitude order, which differs from codepoint string order).
	allNumeric := make([]bool, len(sorts))
	anyNumericSeen := make([]bool, len(sorts))
	allOrderable := make([]bool, len(sorts))
	anyOrderableSeen := make([]bool, len(sorts))
	anyUnorderedDuration := make([]bool, len(sorts))
	// XTDE1030 tracking: with no @data-type the keys are compared with the
	// XPath "lt" operator, which is an ERROR between a number and a string
	// (error-1030a: sorting "1 to 5, 'fred'"). xs:untypedAtomic keys — the
	// normal case, a node's string value — are exempt: they cast to whatever
	// the comparison needs.
	sawNumericKey := make([]bool, len(sorts))
	sawStringKey := make([]bool, len(sorts))
	// An xs:untypedAtomic key (a node's string value — by far the commonest
	// case) compares happily against a string or a number, because it casts to
	// whatever the comparison needs. It does NOT compare against a date/time/
	// duration: the value comparison would have to cast it to xs:string, and
	// xs:string lt xs:date is a type error — which xsl:sort reports as
	// XTDE1030 (sort-080 sorts untyped @at attributes together with a real
	// xs:date).
	sawUntypedKey := make([]bool, len(sorts))
	sawTemporalKey := make([]bool, len(sorts))
	for si, sk := range sorts {
		allNumeric[si] = sk.dataType == nil
		allOrderable[si] = sk.dataType == nil
	}
	items := make([]keyed, len(nodes))
	for i, node := range nodes {
		k := keyed{node: node}
		for si, sk := range sorts {
			v, s, err := eng.sortKeyValue(sk, el, rt{node: node, pos: i + 1, size: len(nodes)})
			if err != nil {
				return nil, err
			}
			if allNumeric[si] {
				if num, empty := sortKeyIsNumeric(v); !empty {
					anyNumericSeen[si] = true
					if !num {
						allNumeric[si] = false
					}
				}
			}
			if allOrderable[si] {
				if ord, empty := sortKeyIsOrderableNonString(v); !empty {
					anyOrderableSeen[si] = true
					if !ord {
						allOrderable[si] = false
					}
				}
			}
			if sk.dataType == nil && sortKeyIsUnorderedDuration(v) {
				anyUnorderedDuration[si] = true
			}
			// XTTE1020: after atomization and any @data-type conversion, a
			// sort key value that is a sequence of more than one item is a
			// type error (error-1020a: select="(.,.,.)").
			atoms, aerr := xpath.Atomize(v)
			if aerr != nil {
				return nil, aerr
			}
			if len(atoms) > 1 {
				return nil, errAt(sk.el, "err:XTTE1020: the xsl:sort key value is a sequence of %d items", len(atoms))
			}
			if sk.dataType == nil && len(atoms) == 1 {
				if a, ok := atoms[0].(*xpath.Atomic); ok {
					switch {
					case a.IsNumeric():
						sawNumericKey[si] = true
					case a.T == xpath.XSuntypedAtomic:
						sawUntypedKey[si] = true
					case a.IsStringFamily():
						sawStringKey[si] = true
					}
					if a.IsOrderableNonString() {
						sawTemporalKey[si] = true
					}
				}
			}
			k.keys = append(k.keys, s)
			k.vals = append(k.vals, v)
		}
		items[i] = k
	}
	for si, sk := range sorts {
		switch {
		case sk.dataType != nil:
		case sawNumericKey[si] && sawStringKey[si]:
			return nil, errAt(sk.el, "err:XTDE1030: xsl:sort key values of incomparable types (numeric and string)")
		case sawTemporalKey[si] && (sawUntypedKey[si] || sawStringKey[si] || sawNumericKey[si]):
			return nil, errAt(sk.el, "err:XTDE1030: xsl:sort key values of incomparable types (a date/time/duration value alongside an untyped, string or numeric one)")
		case anyUnorderedDuration[si]:
			// A general xs:duration key has no defined ordering with
			// @data-type left to its default (date-053): XTDE1030, not a
			// silent fallback to lexical string or value comparison.
			return nil, errAt(sk.el, "err:XTDE1030: xsl:sort key value of type xs:duration does not support ordering; specify @data-type or @collation")
		case allNumeric[si] && anyNumericSeen[si]:
			dataTypes[si] = "number"
		case allOrderable[si] && anyOrderableSeen[si]:
			dataTypes[si] = sortValueDataType
		}
	}
	sort.SliceStable(items, func(a, b int) bool {
		for ki := range sorts {
			var cmp int
			if dataTypes[ki] == sortValueDataType {
				cmp = compareAtomicKey(items[a].vals[ki], items[b].vals[ki])
			} else {
				cmp = compareKey(items[a].keys[ki], items[b].keys[ki], dataTypes[ki], caseOrders[ki], colls[ki])
			}
			if cmp == 0 {
				continue
			}
			if orders[ki] == "descending" {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	out := make([]*xmltree.Node, len(items))
	for i, it := range items {
		out[i] = it.node
	}
	return out, nil
}

// compareKey compares two sort keys per @data-type, and for text keys, per an
// explicit @case-order (sort-043: case-order is only meaningful for the
// default, code-point-based collation — it decides which of two strings that
// differ ONLY in case sorts first; unrelated letters keep their normal
// case-insensitive alphabetic order).
func compareKey(a, b, dataType, caseOrder string, coll func(a, b string) int) int {
	if dataType == "number" {
		na, nb := xpath.ToNumber(a), xpath.ToNumber(b)
		aNaN, bNaN := math.IsNaN(na), math.IsNaN(nb)
		switch {
		case aNaN && bNaN:
			return 0
		case aNaN:
			// A sort key that doesn't convert to a number sorts before every
			// numeric key, in both ascending and descending order (sort-001/
			// 050): Go's NaN comparisons are all false, so without this the
			// key compared "equal" to everything and sort.SliceStable just
			// left it in its original position instead of moving it.
			return -1
		case bNaN:
			return 1
		case na < nb:
			return -1
		case na > nb:
			return 1
		default:
			return 0
		}
	}
	if caseOrder == "upper-first" || caseOrder == "lower-first" {
		// Case-insensitive alphabetic order is primary; case-order only
		// breaks a tie between strings that differ solely in case.
		if cmp := strings.Compare(strings.ToLower(a), strings.ToLower(b)); cmp != 0 {
			return cmp
		}
		cmp := strings.Compare(a, b)
		if caseOrder == "lower-first" {
			return -cmp
		}
		return cmp
	}
	if coll != nil {
		return coll(a, b)
	}
	return strings.Compare(a, b)
}

func childrenOf(n *xmltree.Node) xpath.NodeSet {
	return xpath.NodeSet(append([]*xmltree.Node{}, n.Children...))
}

func findParam(params []*VarDef, name xmltree.Name) *VarDef {
	for _, p := range params {
		if p.name.Local == name.Local && p.name.Space == name.Space {
			return p
		}
	}
	return nil
}

func elOrDoc(el, doc *xmltree.Node) *xmltree.Node {
	if el != nil {
		return el
	}
	return doc
}

// joinSeq renders an Object as a simple-content string, joining the sequence's
// items with sep per XSLT 3.0 §5.8.2 (see simpleContentJoin) — used by the
// @select form of xsl:value-of/xsl:attribute/xsl:comment/xsl:processing-
// instruction and by attribute value template expansion.
func joinSeq(o xpath.Object, sep string) string {
	return simpleContentJoin(xpath.Flatten(xpath.Items(o)), sep, false)
}

// firstIllegalXML10Char walks a result tree for the first character that no
// XML 1.0 document may contain — a C0 control other than tab, LF and CR.
// (XML 1.1 admits them as character references, which is why the caller only
// applies this at version 1.0.) Element/attribute NAMES are not checked: the
// 5th edition of XML 1.0 adopted the 1.1 Name production, so a name legal in
// 1.1 is legal in 1.0 too (xml-version-029's note).
func firstIllegalXML10Char(n *xmltree.Node) (rune, bool) {
	bad := func(s string) (rune, bool) {
		for _, r := range s {
			if r >= 0x1 && r <= 0x1F && r != 0x9 && r != 0xA && r != 0xD {
				return r, true
			}
		}
		return 0, false
	}
	switch n.Kind {
	case xmltree.KindText, xmltree.KindComment, xmltree.KindPI:
		if r, ok := bad(n.Value); ok {
			return r, true
		}
	}
	for _, a := range n.Attrs {
		if r, ok := bad(a.Value); ok {
			return r, true
		}
	}
	for _, c := range n.Children {
		if r, ok := firstIllegalXML10Char(c); ok {
			return r, true
		}
	}
	return 0, false
}

// currentOutputURI returns the URI of the result document currently being
// written: the innermost enclosing xsl:result-document's resolved href, else
// the base output URI. It is "" — the empty sequence for fn:current-output-uri
// — when the host supplied no base output URI, and also in TEMPORARY OUTPUT
// STATE (an xsl:function/xsl:key/accumulator/variable body), where there is no
// result document being written at all.
func (eng *engine) currentOutputURI() string {
	if eng.tempOutputDepth > 0 || eng.patternDepth > 0 {
		return ""
	}
	if n := len(eng.outURIStack); n > 0 {
		return eng.outURIStack[n-1]
	}
	return eng.outURI
}

// checkBackwardsCompatVersion reports XTDE0160 when EXECUTING an element that
// explicitly overrides the effective version to XSLT 1.0 — a request for 1.0
// backwards-compatible behaviour, which this processor does not implement.
//
// Three deliberate restrictions, each pinned by a suite case:
//
//   - Only an EXPLICIT version attribute ON THE ELEMENT ITSELF counts, never
//     one inherited from the stylesheet root. A whole stylesheet written to
//     version="1.0" is processed with 3.0 semantics — that is how this engine
//     runs the suite's ~2000 XSLT 1.0 cases at all — whereas a per-element
//     override is a deliberate request for that version's behaviour here.
//   - The check is made at INVOCATION, not at compile time: the error arises
//     only if the 1.0 code actually runs. initial-template-080 invokes a
//     version="2.0" template in a stylesheet that ALSO contains a
//     version="1.0" one and must not fail; initial-template-081 invokes the
//     version="1.0" one and must.
//   - Exactly "1.0", not "anything below 2.0". version-018 carries
//     version="1.5" on a template and expects it to run normally (its own
//     keyword is "forwards-compatibility-mode"): backwards-compatible
//     behaviour means emulating a version that exists, and 1.0 is the only
//     one there is. Reading the trigger as "< 2.0" would make version-018 and
//     initial-template-081 contradict each other; this reading satisfies both.
func checkBackwardsCompatVersion(el *xmltree.Node) error {
	if el == nil || backwardsCompatRun() {
		// A run that claims genuine backwards-compatible processing
		// (SetBackwardsCompatible) implements the relaxations instead of
		// rejecting them — see inBackwardsCompatScope and evalWith's BC10
		// wiring — so there is nothing to reject here.
		return nil
	}
	v, ok := el.AttrLocal("version")
	if !ok {
		return nil
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil || f != 1.0 {
		return nil
	}
	return errAt(el, "err:XTDE0160: version=%q requests XSLT 1.0 backwards compatible behaviour, which this processor does not support", v)
}

// bc10At reports whether el runs under CLAIMED backwards-compatible
// processing — the same test evalWith applies to build xpath.Context.BC10,
// exposed here for the handful of instruction-level relaxations (xsl:value-of
// and an AVT's default, unseparated join; xsl:number/@value — see their own
// call sites) that apply without going through an XPath evaluation at all.
func (eng *engine) bc10At(el *xmltree.Node) bool {
	return eng.sheet.hasBackwardsCompat && backwardsCompatRun() && inBackwardsCompatScope(el)
}

// noteEntryValue records an @as-typed template result as the transformation's
// raw return value, but only for the INITIAL named template itself (depth 1
// under captureEntryValue) — a template it in turn calls has its own result,
// which is not the transformation's (initial-template-004 asserts over the
// xs:decimal* sequence its entry template returns, which the serialized output
// cannot express).
func (eng *engine) noteEntryValue(v xpath.Object) {
	if !eng.captureEntryValue || eng.depth != 1 {
		return
	}
	if eng.entryValue == nil {
		// The (still-overwhelmingly-common) single-invocation case — a named
		// entry template, or an apply-templates entry whose initial match
		// selection has exactly one item — is unchanged from before this
		// accumulated.
		eng.entryValue = v
		return
	}
	// An apply-templates/initial-match-selection entry point can invoke the
	// matching template once per item in the selection (fn-transform-84
	// applies an as="xs:integer" template to five items): the transformation's
	// raw result is their CONCATENATION, in selection order, not just the
	// last one to run.
	eng.entryValue = xpath.FromItems(append(xpath.Items(eng.entryValue), xpath.Items(v)...))
}
