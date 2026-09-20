package xslt

import (
	"sort"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// resolveModeName resolves one @mode token to its comparison key. A pseudo-
// mode token (#current/#default/#unnamed/#all) is never namespace-qualified
// and is returned unchanged; a genuine mode QName is expanded to Clark form
// so two different prefixes bound to the SAME namespace name the same mode
// (mode-0901: mode="foo:a" and mode="moo:a" must be one mode when foo and moo
// are both bound to the same URI — modes are compared as expanded names, not
// lexical text).
func resolveModeName(el *xmltree.Node, tok string) string {
	if strings.HasPrefix(tok, "#") {
		return tok
	}
	return clarkName(resolveQName(el, tok))
}

// defaultModeWalk returns the default mode in scope for el — XSLT 3.0's
// [xsl:]default-mode attribute. The attribute is written unprefixed
// (`default-mode`) on elements in the XSLT namespace and as `xsl:default-mode`
// on a literal result element (or any other non-XSLT element); it applies to
// the element it appears on AND to all of its descendants, so the innermost
// occurrence on the ancestor-or-self chain wins. The result is a mode name in
// Clark form; "" is the unnamed mode — which is also what "#unnamed",
// "#default" and "no default-mode attribute anywhere" all resolve to, so on a
// stylesheet that never uses the attribute this is exactly the pre-3.0
// behaviour.
//
// INVARIANT: every place that needs "the mode to use when no @mode was
// written" (xsl:template/@mode, xsl:apply-templates/@mode, and the default
// INITIAL mode taken from the principal module's root element) must go
// through this function rather than defaulting to "" directly.
func defaultModeWalk(el *xmltree.Node) string {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("default-mode")
		} else {
			v, ok = cur.Attr(NS, "default-mode")
		}
		if !ok {
			continue
		}
		tok := strings.TrimSpace(v)
		if tok == "" || tok == "#unnamed" || tok == "#default" {
			return ""
		}
		return resolveModeName(cur, tok)
	}
	return ""
}

// ModeDef is a compiled xsl:mode declaration (or, after resolveModeDecls, the
// single EFFECTIVE declaration for a mode).
type ModeDef struct {
	name       string
	onNoMatch  string // deep-skip|shallow-skip|text-only-copy|shallow-copy|deep-copy|fail
	importPrec int    // module import precedence (conflict detection, XTSE0545)
	visibility string // public|private|final|abstract ("" = unspecified, i.e. private)
	el         *xmltree.Node
	// attrs holds every attribute this declaration actually SPECIFIED,
	// normalized (use-accumulators expanded to Clark names and sorted). An
	// attribute the declaration omits is simply absent from the map — this is
	// what makes the per-attribute import-precedence resolution in
	// resolveModeDecls possible.
	attrs map[string]string
}

// modeDeclAttrs are the xsl:mode attributes subject to per-attribute import
// precedence resolution and the XTSE0545 conflict check.
var modeDeclAttrs = []string{
	"streamable", "use-accumulators", "on-no-match", "on-multiple-match",
	"warning-on-no-match", "warning-on-multiple-match", "typed", "visibility",
}

func init() {
	declRegistry["mode"] = func(c *compiler, ss *Stylesheet, el *xmltree.Node) error {
		md := &ModeDef{onNoMatch: "text-only-copy", attrs: map[string]string{}, el: el}
		if n, ok := el.AttrLocal("name"); ok {
			md.name = resolveModeName(el, n)
		}
		for _, a := range modeDeclAttrs {
			v, ok := el.AttrLocal(a)
			if !ok {
				continue
			}
			if a == "use-accumulators" {
				// Accumulator names are compared as EXPANDED names, so two
				// declarations naming the same accumulators through different
				// prefixes agree, and two naming different namespaces do not
				// (mode-1514 vs mode-1515).
				toks := strings.Fields(v)
				exp := make([]string, 0, len(toks))
				for _, t := range toks {
					if strings.HasPrefix(t, "#") {
						exp = append(exp, t)
						continue
					}
					exp = append(exp, clarkName(resolveQName(el, t)))
				}
				sort.Strings(exp)
				v = strings.Join(exp, " ")
			} else {
				v = strings.TrimSpace(v)
			}
			md.attrs[a] = v
		}
		md.importPrec = c.importPrec
		ss.modeDecls[md.name] = append(ss.modeDecls[md.name], md)
		return nil
	}
}

// resolveModeDecls collapses every xsl:mode declaration collected during
// compilation into one effective ModeDef per mode name, populating ss.modes.
//
// Resolution is PER ATTRIBUTE and DEFERRED to the end of compilation, the same
// shape xsl:output uses: for each attribute the value comes from the
// declaration of highest import precedence that specifies it, and XTSE0545 is
// raised only when two declarations AT THAT precedence give different values.
// Doing it eagerly (comparing each new declaration against the previous one as
// it is compiled) reports a conflict that a later, higher-precedence
// declaration masks — mode-1513 and mode-1905 are exactly that case.
//
// INVARIANT: ss.modes is empty until this runs, so nothing that inspects
// ss.modes (modeDef, knownModeName, checkDeclaredModes) may run before it.
func resolveModeDecls(ss *Stylesheet) error {
	names := make([]string, 0, len(ss.modeDecls))
	for n := range ss.modeDecls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		decls := ss.modeDecls[name]
		eff := &ModeDef{name: name, onNoMatch: "text-only-copy", attrs: map[string]string{}, el: decls[0].el}
		for _, d := range decls {
			if d.importPrec > eff.importPrec {
				eff.importPrec = d.importPrec
			}
		}
		for _, a := range modeDeclAttrs {
			// Pass 1: the highest import precedence at which ANY declaration
			// specifies this attribute. Pass 2: conflicts are only looked for
			// among the declarations at THAT precedence — a disagreement at a
			// lower precedence is masked, not an error (mode-1513/1905).
			best := -1
			for _, d := range decls {
				if _, ok := d.attrs[a]; ok && d.importPrec > best {
					best = d.importPrec
				}
			}
			if best < 0 {
				continue
			}
			var val string
			var at *xmltree.Node
			for _, d := range decls {
				v, ok := d.attrs[a]
				if !ok || d.importPrec != best {
					continue
				}
				if at == nil {
					val, at = v, d.el
				} else if v != val {
					return errAt(d.el, "err:XTSE0545: conflicting xsl:mode/@%s declarations for mode %q", a, name)
				}
			}
			eff.attrs[a] = val
		}
		if v, ok := eff.attrs["on-no-match"]; ok {
			eff.onNoMatch = v
		}
		eff.visibility = eff.attrs["visibility"]
		ss.modes[name] = eff
	}
	return nil
}

// checkDeclaredModes implements XTSE3085: inside an xsl:package whose
// declared-modes attribute is (explicitly or by default) "yes", every mode
// referenced by an xsl:template/@mode or xsl:apply-templates/@mode — including
// the unnamed mode used implicitly (no @mode at all) or explicitly (#unnamed/
// #default) — must be declared by an xsl:mode declaration. The pseudo-modes
// #all and #current name no particular mode and are not checked.
func checkDeclaredModes(ss *Stylesheet, roots []*xmltree.Node) error {
	check := func(el *xmltree.Node, name string) error {
		if _, ok := ss.modes[name]; !ok {
			shown := name
			if shown == "" {
				shown = "#unnamed"
			}
			return errAt(el, "err:XTSE3085: mode %s is not declared by an xsl:mode declaration (declared-modes=yes)", shown)
		}
		return nil
	}
	var walk func(el *xmltree.Node) error
	walk = func(el *xmltree.Node) error {
		if el.Kind == xmltree.KindElement && el.Name.Space == NS {
			isTemplateRule := el.Name.Local == "template"
			if isTemplateRule {
				if _, ok := el.AttrLocal("match"); !ok {
					isTemplateRule = false
				}
			}
			if isTemplateRule || el.Name.Local == "apply-templates" {
				if m, ok := el.AttrLocal("mode"); ok {
					for _, tok := range strings.Fields(m) {
						switch tok {
						case "#all", "#current":
							continue
						case "#unnamed":
							if err := check(el, ""); err != nil {
								return err
							}
						case "#default":
							if err := check(el, defaultModeWalk(el)); err != nil {
								return err
							}
						default:
							if err := check(el, resolveModeName(el, tok)); err != nil {
								return err
							}
						}
					}
				} else if err := check(el, defaultModeWalk(el)); err != nil {
					return err
				}
			}
		}
		for _, ch := range el.Children {
			if ch.Kind != xmltree.KindElement {
				continue
			}
			if err := walk(ch); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range roots {
		if err := walk(r); err != nil {
			return err
		}
	}
	return nil
}

// checkAbstractRefs implements XTSE3080: a top-level (executable) package may
// not contain a symbolic reference to a component whose visibility is
// abstract. Every xsl:package the engine compiles IS a top-level package — a
// library package reached through xsl:use-package is not supported — so a
// reference to an abstract named template is always an error here.
func checkAbstractRefs(roots []*xmltree.Node) error {
	abstract := map[string]bool{}
	concrete := map[string]bool{}
	var scan func(el *xmltree.Node)
	scan = func(el *xmltree.Node) {
		if el.Kind == xmltree.KindElement && el.Name.Space == NS && el.Name.Local == "template" {
			if n, ok := el.AttrLocal("name"); ok {
				name := clarkName(resolveQName(el, n))
				if v, ok := el.AttrLocal("visibility"); ok && strings.TrimSpace(v) == "abstract" {
					abstract[name] = true
				} else {
					// A CONCRETE (non-abstract) declaration of the same name
					// also exists — almost always an xsl:override supplying
					// the abstract original's implementation (accept-040:
					// "OK to have a hidden function that isn't called, even
					// if the original function is abstract" covers the same
					// idea for a template named "t1" that IS called, once
					// overridden). Per XSLT 3.0 §3.5.4, an override always
					// resolves in place of the used package's own
					// declaration, so a reference to that name is no longer
					// a reference to the abstract one.
					concrete[name] = true
				}
			}
		}
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindElement {
				scan(ch)
			}
		}
	}
	for _, r := range roots {
		scan(r)
	}
	for name := range concrete {
		delete(abstract, name)
	}
	if len(abstract) == 0 {
		return nil
	}
	var walk func(el *xmltree.Node) error
	walk = func(el *xmltree.Node) error {
		if el.Kind == xmltree.KindElement && el.Name.Space == NS && el.Name.Local == "call-template" {
			if n, ok := el.AttrLocal("name"); ok && abstract[clarkName(resolveQName(el, n))] {
				return errAt(el, "err:XTSE3080: a top-level package cannot reference the abstract component %q", strings.TrimSpace(n))
			}
		}
		for _, ch := range el.Children {
			if ch.Kind != xmltree.KindElement {
				continue
			}
			if err := walk(ch); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range roots {
		if err := walk(r); err != nil {
			return err
		}
	}
	return nil
}

// knownModeName reports whether mode (already resolved to Clark form, or the
// empty string for #default) is a mode the stylesheet actually declares:
// named by an xsl:mode declaration, or the explicit @mode of some template.
// mode="#all" on a template does not itself declare any specific mode name
// (initial-mode-002, XTDE0045).
func (ss *Stylesheet) knownModeName(mode string) bool {
	if mode == "" {
		return true
	}
	if _, ok := ss.modes[mode]; ok {
		return true
	}
	for _, t := range ss.templates {
		if t.modeToks == nil {
			if t.mode == mode {
				return true
			}
			continue
		}
		for _, tok := range t.modeToks {
			if tok == mode {
				return true
			}
		}
	}
	return false
}

func (ss *Stylesheet) modeDef(mode string) *ModeDef {
	if m, ok := ss.modes[mode]; ok {
		return m
	}
	return &ModeDef{name: mode, onNoMatch: "text-only-copy"}
}

// modeTypedKind is the effective [xsl:]typed value of a mode, normalized:
// whitespace collapsed (match-227 writes typed=" yes "), the boolean synonyms
// folded onto "yes"/"no", and anything else — including the explicit
// "unspecified" — reported as "".
func modeTypedKind(md *ModeDef) string {
	if md == nil {
		return ""
	}
	switch strings.Join(strings.Fields(md.attrs["typed"]), " ") {
	case "yes", "true", "1":
		return "yes"
	case "no", "false", "0":
		return "no"
	case "strict":
		return "strict"
	case "lax":
		return "lax"
	}
	return ""
}

// nodeIsTyped reports whether a node carries a real type annotation, i.e.
// anything other than xs:untyped / xs:untypedAtomic — the question both
// XTTE3100 and XTTE3110 turn on.
//
// Both halves of the annotation count. A validated element with COMPLEX
// content has no atomizable typed value and so leaves TypeAnno at 0, carrying
// its identity in SchemaType alone; an element a lax episode did not assess
// carries the xs:anyType sentinel there for the same reason. Reading only
// TypeAnno would call every one of those untyped.
func nodeIsTyped(n *xmltree.Node) bool {
	return n != nil && (n.TypeAnno != 0 || n.SchemaType != nil)
}

// applyTypedModeRewrites carries out the §6.6.3 provision that
// xsl:mode/@typed="strict"/"lax" attaches to the match patterns of every
// template rule applicable to such a mode, and raises XTSE3105 where strict
// finds no declaration.
//
// It runs LAST in the compile, after priorities and the streamability
// classification are settled, so that a rewrite cannot feed back into either
// (Pattern.TypedModeRewrite is careful not to change the test's kind for the
// same reason, but ordering makes it independent of that care).
//
// A template applicable to several modes is rewritten for each in turn. Two
// modes disagreeing about strictness over one shared pattern object would be
// ambiguous, but the rewrite is idempotent on names that DO resolve, so the
// ambiguity is confined to strict-vs-lax over an undeclared name — where
// strict's static error wins whichever order they are visited in.
func applyTypedModeRewrites(ss *Stylesheet) error {
	if !schemaAwareRun() {
		return nil
	}
	for _, t := range ss.templates {
		if t.pattern == nil {
			continue
		}
		sn := schemaLookupFor(t.el)
		if sn == nil {
			continue
		}
		for name, md := range ss.modes {
			k := modeTypedKind(md)
			if (k != "strict" && k != "lax") || !t.matchesMode(name) {
				continue
			}
			if err := t.pattern.TypedModeRewrite(sn, k == "strict"); err != nil {
				return errAt(t.el, "%v", err)
			}
		}
	}
	return nil
}

// matchingTemplates returns all templates matching node in the given mode,
// best-first by (import precedence desc, priority desc, document order desc).
func (eng *engine) matchingTemplates(node *xmltree.Node, mode string) []*Template {
	var cands []*Template
	// XSLT §5.5: a pattern is evaluated with an empty "current captured
	// substrings" (see fegMatches).
	savedGroups := eng.regexGroups
	eng.regexGroups = nil
	defer func() { eng.regexGroups = savedGroups }()
	for _, t := range eng.sheet.templates {
		if t.pattern == nil || !t.matchesMode(mode) {
			continue
		}
		env := &evalEnv{eng: eng, el: t.el, current: node}
		eng.patternDepth++
		ok, err := t.pattern.Match(node, &xpath.Context{Node: node, CtxItem: realItemOf(node), Pos: 1, Size: 1, Vars: env, NS: env, Funcs: env, Resolver: eng.resolver, NoOutputURI: true, DefaultElemNS: eng.templateDefaultNS(t), Now: eng.now, SchemaTypes: schemaTypesFor(t.el)})
		eng.patternDepth--
		if err != nil || !ok {
			continue
		}
		cands = append(cands, t)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.importPrec != b.importPrec {
			return a.importPrec > b.importPrec
		}
		if a.priority != b.priority {
			return a.priority > b.priority
		}
		return a.order > b.order
	})
	return cands
}

// matchTemplate returns the single best-matching template (or nil).
func (eng *engine) matchTemplate(node *xmltree.Node, mode string) *Template {
	c := eng.matchingTemplates(node, mode)
	if len(c) == 0 {
		return nil
	}
	// on-multiple-match="fail": conflict resolution leaving two rules of the
	// SAME import precedence and priority is XTDE0540 rather than a silent
	// "last one wins" (error-0540a). The error is raised lazily by the caller
	// via eng.pendingErr so this signature stays unchanged.
	if len(c) > 1 && c[0].importPrec == c[1].importPrec && c[0].priority == c[1].priority &&
		c[0].el != c[1].el && eng.sheet.modeDef(mode).attrs["on-multiple-match"] == "fail" {
		if eng.pendingErr == nil {
			eng.pendingErr = errAt(c[0].el, "err:XTDE0540: more than one template rule matches and the mode specifies on-multiple-match=\"fail\"")
		}
	}
	return c[0]
}

// builtinTemplate applies the built-in template rule for the mode's on-no-match.
// params/callerR are the with-params (and their evaluation context) supplied
// to the apply-templates/next-match/apply-imports instruction that fell
// through to this built-in rule: per XSLT §6.6, "If the built-in rule was
// invoked with parameters, those parameters are passed on in the implicit
// xsl:apply-templates instruction" — they are forwarded unevaluated to the
// rule's own recursive apply-templates over children, so a real template
// several built-in-rule hops down can still receive them (as-0601/0702/0703/
// 0901/1001/1101).
func (eng *engine) builtinTemplate(node *xmltree.Node, mode string, out *xmltree.Node, params []*VarDef, callerR rt) error {
	// There is no built-in rule for a function item: reaching one here means
	// no template rule matched it, which is XTDE0555 (higher-order-functions-
	// 069 supplies function items that DO match a rule and must not error).
	if node != nil && node.RealItem != nil {
		if _, isFn := node.RealItem.(*xpath.Function); isFn {
			return errAt(nil, "err:XTDE0555: no template rule matches a function item and there is no built-in rule for one")
		}
	}
	policy := eng.sheet.modeDef(mode).onNoMatch
	if node != nil && node.RealItem != nil {
		if arr, isArr := node.RealItem.(*xpath.Array); isArr {
			// The built-in rule for an array applies templates to its members
			// (xsl:apply-templates select="?*"), under every policy except the
			// two that process nothing / refuse.
			switch policy {
			case "deep-skip":
				return nil
			case "fail":
				return errAt(nil, "err:XTDE0555: no template rule matches an array and the mode's on-no-match is fail")
			}
			var items []xpath.Item
			for _, m := range arr.Members() {
				items = append(items, xpath.Items(m)...)
			}
			return eng.applyToNodes(itemsAsNodes(items), mode, out, params, callerR)
		}
	}
	switch policy {
	case "deep-skip":
		// The DOCUMENT node is not skipped: the built-in rule for a document
		// node always processes its children, whatever the on-no-match policy
		// (W3C bug 30219/30220 — mode-1437a "deep skip should not skip
		// document node", next-match-034).
		if node.Kind == xmltree.KindDocument {
			return eng.applyToNodes(childrenOf(node), mode, out, params, callerR)
		}
		return nil
	case "fail":
		// XTDE0555 — spelled out so xsl:catch errors="err:XTDE0555" can select
		// it (mode-1425 catches exactly this error and recovers).
		return errAt(nil, "err:XTDE0555: no template rule matches %s and the mode's on-no-match is fail", node.Name.Local)
	case "shallow-skip":
		if node.Kind == xmltree.KindElement || node.Kind == xmltree.KindDocument {
			// Attributes are also visited (a matching rule can transform them).
			if len(node.Attrs) > 0 {
				attrs := make(xpath.NodeSet, len(node.Attrs))
				copy(attrs, node.Attrs)
				if err := eng.applyToNodes(attrs, mode, out, params, callerR); err != nil {
					return err
				}
			}
			return eng.applyToNodes(childrenOf(node), mode, out, params, callerR)
		}
		return nil
	case "shallow-copy":
		return eng.shallowCopyAndRecurse(node, mode, out, params, callerR)
	case "deep-copy":
		deepCopyInto(node, out)
		return nil
	default: // text-only-copy (the classic XSLT built-ins)
		switch node.Kind {
		case xmltree.KindDocument, xmltree.KindElement:
			return eng.applyToNodes(childrenOf(node), mode, out, params, callerR)
		case xmltree.KindText, xmltree.KindAttribute:
			out.Append(xmltree.NewText(node.StringValue()))
		}
		return nil
	}
}

func (eng *engine) shallowCopyAndRecurse(node *xmltree.Node, mode string, out *xmltree.Node, params []*VarDef, callerR rt) error {
	switch node.Kind {
	case xmltree.KindElement:
		el := xmltree.NewElement(node.Name)
		// The shallow copy is a COPY, so — exactly like xsl:copy, whose
		// semantics the built-in rule reproduces — it retains the original's
		// base-uri, but only when it is the raw, unwrapped result of an
		// @as-typed body (out.NoAtomicMerge); embedded as further complex
		// content it derives its base-uri from its new position instead
		// (base-uri-053's shallow-copy-elem2, which otherwise reported the
		// STYLESHEET's base URI).
		if out.NoAtomicMerge {
			el.Base = retainedBase(el, node)
		}
		out.Append(el)
		// The built-in shallow-copy rule applies templates to the attributes
		// as well as the children (a matching rule can transform them).
		if len(node.Attrs) > 0 {
			attrs := make(xpath.NodeSet, len(node.Attrs))
			copy(attrs, node.Attrs)
			if err := eng.applyToNodes(attrs, mode, el, params, callerR); err != nil {
				return err
			}
		}
		return eng.applyToNodes(childrenOf(node), mode, el, params, callerR)
	case xmltree.KindDocument:
		if out.KeepDocItems {
			// Same distinction xsl:copy draws for a document node: in a
			// discrete-sequence collector the copy must stay a real document
			// node (carrying the original's base-uri); as ordinary content it
			// flattens into its children, which is all a document node can do
			// there (base-uri-053's shallow-copy-doc2).
			d := &xmltree.Node{Kind: xmltree.KindDocument, Base: xpath.NodeBaseURI(node, ""), Ephemeral: true}
			mergeUnparsed(d, node)
			if err := eng.applyToNodes(childrenOf(node), mode, d, params, callerR); err != nil {
				return err
			}
			out.Append(d)
			mergeUnparsed(out, node)
			return nil
		}
		return eng.applyToNodes(childrenOf(node), mode, out, params, callerR)
	case xmltree.KindText, xmltree.KindComment, xmltree.KindPI, xmltree.KindAttribute:
		deepCopyInto(node, out)
	}
	return nil
}

// templateDefaultNS returns the template's xpath-default-namespace, walking
// only when the stylesheet declares one anywhere.
func (eng *engine) templateDefaultNS(t *Template) string {
	if !eng.sheet.hasXPathDefaultNS {
		return ""
	}
	return xpathDefaultNSWalk(t.el)
}

// matchesMode checks the template's @mode (a whitespace-separated list that
// may include #all/#default/#unnamed, pre-tokenized at compile time) against
// the mode of the current apply-templates.
func (t *Template) matchesMode(mode string) bool {
	if t.modeToks == nil {
		return t.mode == mode
	}
	for _, tok := range t.modeToks {
		switch tok {
		case "#all":
			return true
		case "#default", "#unnamed":
			if mode == "" {
				return true
			}
		default:
			if tok == mode {
				return true
			}
		}
	}
	return false
}

// resolveMode maps the #current/#default/#unnamed pseudo-modes to a real mode.
func (eng *engine) resolveMode(mode string) string {
	switch mode {
	case "#current":
		return eng.curMode
	case "#default", "#unnamed", "":
		return ""
	}
	return mode
}
