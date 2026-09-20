package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// evEvaluate implements xsl:evaluate (XSLT 3.0): dynamically evaluate an XPath
// expression supplied as a string at run time.
//
//	@xpath          required; expression producing the XPath STRING to evaluate
//	@context-item   optional; expression for the context item
//	xsl:with-param  optional children (name + select) binding variables visible
//	                to the evaluated expression
type evEvaluate struct {
	xpath  *xpath.Parsed // expression yielding the XPath string
	ctx    *xpath.Parsed // optional context-item expression
	hasCtx bool          // @context-item was present (an EMPTY result then means "no context item", not "inherit")
	params []*VarDef     // xsl:with-param children
	el     *xmltree.Node // for namespace context + diagnostics
	// nsCtx is @namespace-context: an expression yielding a NODE whose
	// in-scope namespaces resolve the prefixes of the dynamic expression,
	// replacing those of the xsl:evaluate element itself (evaluate-015/027).
	nsCtx *xpath.Parsed
	// withParams is @with-params: an expression yielding a map from xs:QName
	// to value, binding additional variables for the dynamic expression
	// (evaluate-018a/b/c). It is merged with the xsl:with-param children.
	withParams *xpath.Parsed
	baseURI    *avt   // @base-uri (an AVT): the static base URI of the dynamic expression
	as         string // @as: sequence type the result is checked/converted against
}

func (*evEvaluate) instr() {}

func init() {
	instrRegistry["evaluate"] = evCompile
}

func evCompile(c *compiler, el *xmltree.Node) (instruction, error) {
	xp, err := requireExpr(el, "xpath")
	if err != nil {
		return nil, err
	}
	n := &evEvaluate{xpath: xp, el: el}
	if v, ok := el.AttrLocal("context-item"); ok {
		p, perr := parseXPathFor(el, v)
		if perr != nil {
			return nil, errAt(el, "bad context-item %q: %v", v, perr)
		}
		n.ctx, n.hasCtx = p, true
	}
	if v, ok := el.AttrLocal("namespace-context"); ok {
		p, perr := parseXPathFor(el, v)
		if perr != nil {
			return nil, errAt(el, "bad namespace-context %q: %v", v, perr)
		}
		n.nsCtx = p
	}
	if v, ok := el.AttrLocal("with-params"); ok {
		p, perr := parseXPathFor(el, v)
		if perr != nil {
			return nil, errAt(el, "bad with-params %q: %v", v, perr)
		}
		n.withParams = p
	}
	if v, ok := el.AttrLocal("base-uri"); ok {
		a, perr := parseAVTFor(el, v)
		if perr != nil {
			return nil, errAt(el, "bad base-uri %q: %v", v, perr)
		}
		n.baseURI = a
	}
	if v, ok := el.AttrLocal("as"); ok {
		n.as = v
	}
	_, params, err := c.compileSortsAndParams(el)
	if err != nil {
		return nil, err
	}
	n.params = params
	return n, nil
}

func (n *evEvaluate) exec(eng *engine, r rt, out *xmltree.Node) error {
	v, err := n.value(eng, r)
	if err != nil {
		return err
	}
	// Append the result exactly as xsl:sequence would: the dynamic expression
	// yields a real SEQUENCE, so atomic items stay atomic (evaluate-005 —
	// stringifying the whole value would collapse a multi-item result).
	//
	// An ATTRIBUTE or NAMESPACE node attaches to the element being constructed,
	// as it would from xsl:sequence (evaluate-004 wraps xsl:evaluate xpath="'@id'"
	// in an <item> element and expects <item id="..."/>). Only when there is no
	// element to attach to — the result is being collected into a document/RTF
	// or a discrete-sequence collector — does it fall back to contributing its
	// string value, which is this instruction's long-standing behavior and what
	// the common "use xsl:evaluate to fetch an attribute's value" idiom relies on.
	for _, it := range xpath.Flatten(xpath.Items(v)) {
		if nd, ok := it.(*xmltree.Node); ok &&
			(nd.Kind == xmltree.KindAttribute || nd.Kind == xmltree.KindNamespace) &&
			out.Kind != xmltree.KindElement {
			out.Append(xmltree.NewText(nd.Value))
			continue
		}
		if err := emitSequenceValue(xpath.FromItems([]xpath.Item{it}), out); err != nil {
			return err
		}
	}
	return nil
}

// value computes the sequence xsl:evaluate produces, without appending it.
// evalFuncBody calls it directly so a function whose whole body is one
// xsl:evaluate returns the real sequence rather than its constructed-content
// projection (evaluate-028/029: a boolean false must stay a boolean, not
// become a truthy text node).
func (n *evEvaluate) value(eng *engine, r rt) (xpath.Object, error) {
	// 1. Evaluate @xpath to obtain the expression string, then compile it.
	exprStr, err := eng.evalString(n.xpath, n.el, r)
	if err != nil {
		return nil, err
	}
	// XSLT 3.0 §10.4: the evaluated expression gets the stylesheet's in-scope
	// SCHEMA COMPONENTS only when this instruction declares
	// schema-aware="yes". Without it, a type name from an imported schema
	// resolves to nothing — parse-time (element(N,T), instance of my:T) and
	// run-time (the my:T() constructor) alike, which is why the lookup is
	// withheld from both (evaluate-012/013/014).
	schemaAware := false
	if v, ok := n.el.AttrLocal("schema-aware"); ok {
		schemaAware = isXSLTTrue(strings.TrimSpace(v))
	}
	var parsed *xpath.Parsed
	if schemaAware {
		parsed, err = parseXPathFor(n.el, exprStr)
	} else {
		parsed, err = xpath.ParseWithheldSchema(exprStr)
	}
	if err != nil {
		return nil, errAt(n.el, "xsl:evaluate could not parse xpath %q: %v", exprStr, err)
	}

	// 2. Determine the context item for the evaluated expression: a node is
	// used directly, an atomic value is modelled as a synthetic text node
	// (same technique as xsl:for-each) so "." yields it (collations-0128:
	// context-item="$x" with $x an xs:string). @context-item="()" means there
	// is NO context item at all (evaluate-025), and more than one item is
	// XTTE3210 (evaluate-026).
	// With NO @context-item the evaluated expression has no context item,
	// position or size at all — it does NOT inherit the containing focus
	// (XSLT 3.0 §10.4.2), so fn:position() there is XPDY0002 (evaluate-024).
	er := rt{noFocus: true}
	if n.hasCtx {
		cv, cerr := eng.eval(n.ctx, n.el, r)
		if cerr != nil {
			return nil, cerr
		}
		items := xpath.Items(cv)
		switch {
		case len(items) == 0:
			er = rt{noFocus: true}
		case len(items) > 1:
			return nil, errAt(n.el, "err:XTTE3210: xsl:evaluate/@context-item selects %d items", len(items))
		default:
			er = rt{node: evContextNode(cv, r.node), pos: 1, size: 1}
		}
	}

	// 3. The namespace context (and hence the element whose in-scope prefixes
	// resolve names in the dynamic expression) is the xsl:evaluate element,
	// unless @namespace-context names a node.
	nsEl := n.el
	var ov evalOverride
	if n.nsCtx != nil {
		nv, nerr := eng.eval(n.nsCtx, n.el, r)
		if nerr != nil {
			return nil, nerr
		}
		nitems := xpath.Items(nv)
		if len(nitems) > 1 {
			return nil, errAt(n.el, "err:XTTE3170: xsl:evaluate/@namespace-context selects %d items, not a single node", len(nitems))
		}
		if ns, ok := xpath.ToNodeSet(nv); ok && len(ns) > 0 {
			nsEl = ns[0]
			// The default namespace for element/type names in the dynamic
			// expression is the DEFAULT NAMESPACE of the namespace-context
			// node — never the stylesheet's xpath-default-namespace
			// (evaluate-027 deliberately sets a bogus one).
			dns, _ := nsEl.LookupPrefix("")
			ov.dns, ov.hasDNS = dns, true
		}
	}

	if n.baseURI != nil {
		base, berr := eng.evalAVT(n.baseURI, n.el, r)
		if berr != nil {
			return nil, berr
		}
		ov.base, ov.hasBase = base, true
	}

	// 4. Bind xsl:with-param values, and any @with-params map entries, in a
	// fresh scope.
	eng.pushScope()
	defer eng.popScope()
	for _, p := range n.params {
		val, verr := eng.evalVarDef(p, r)
		if verr != nil {
			return nil, verr
		}
		eng.bindVar(p.name, val)
	}
	if n.withParams != nil {
		wv, werr := eng.eval(n.withParams, n.el, r)
		if werr != nil {
			return nil, werr
		}
		wits := xpath.Items(wv)
		var m *xpath.Map
		if len(wits) == 1 {
			m, _ = wits[0].(*xpath.Map)
		}
		if m == nil {
			return nil, errAt(n.el, "err:XPTY0004: xsl:evaluate/@with-params must be a map")
		}
		for _, k := range m.Keys() {
			a, aerr := xpath.AtomicFromItem(k)
			if aerr != nil {
				return nil, errAt(n.el, "err:XPTY0004: xsl:evaluate/@with-params keys must be xs:QName values")
			}
			qn, qok := a.QNameValue()
			if !qok {
				return nil, errAt(n.el, "err:XPTY0004: xsl:evaluate/@with-params keys must be xs:QName values")
			}
			v := m.Get(k)
			eng.bindVar(qn, v)
		}
	}

	// 5. Evaluate the dynamically-compiled expression. Its static context
	//    excludes the XSLT functions that depend on the evaluation context of
	//    the containing stylesheet (XSLT 3.0 §10.3) — see evNotAvailable.
	eng.evaluateDepth++
	ov.noSchema = !schemaAware
	v, err := eng.evalWith(parsed, nsEl, er, ov)
	eng.evaluateDepth--
	if err != nil {
		return nil, err
	}
	if n.as != "" {
		cv, ok := xpath.CoerceToDeclaredTypeCtx(n.as, v, asTypeCtx(n.el))
		if !ok {
			return nil, errAt(n.el, "err:XTTE3165: xsl:evaluate result does not match declared type %q", n.as)
		}
		v = cv
	}
	return v, nil
}

// evContextNode extracts a context node from the value of @context-item: a
// node is used directly; a non-node (atomic) item is modelled as a
// synthetic, parentless text node — exactly xsl:for-each's technique for an
// atomic context item — stamped with its original atomic type so "." keeps
// numeric/typed comparisons correct, not just its string value. The empty
// sequence falls back to def (the current context node).
func evContextNode(v xpath.Object, def *xmltree.Node) *xmltree.Node {
	items := xpath.Items(v)
	if len(items) == 0 {
		return def
	}
	if nd, ok := items[0].(*xmltree.Node); ok {
		return nd
	}
	// Atomic: true, matching xsl:for-each's own identical wrapper — the same
	// "modeled as text but is really an atomic value" marker
	// simpleContentJoin's doc comment documents (seqtor-007 was xsl:for-
	// each's version of the bug omitting this causes: a re-emitted "."
	// silently losing its single-space join with an adjacent atomic value).
	nd := &xmltree.Node{Kind: xmltree.KindText, Atomic: true, Value: xpath.ToString(xpath.FromItems([]xpath.Item{items[0]}))}
	if tag, ok := xpath.ItemAtomTypeTag(items[0]); ok {
		nd.TypeAnno = tag
	}
	attachRealItem(nd, items[0])
	return nd
}

// evNotAvailable lists the XSLT-defined functions whose meaning depends on the
// containing stylesheet's evaluation context; XSLT 3.0 §10.3 removes them from
// the static context of the expression xsl:evaluate compiles, so calling one
// there is XTDE3160 (evaluate-007, system-property-022).
var evNotAvailable = map[string]bool{
	"current": true, "current-group": true, "current-grouping-key": true,
	"current-merge-group": true, "current-merge-key": true, "regex-group": true,
	"system-property": true, "element-available": true, "function-available": true,
	"type-available": true, "unparsed-entity-uri": true, "unparsed-entity-public-id": true,
	// fn:current-output-uri depends on which result document is being written,
	// which is part of the dynamic context xsl:evaluate does not inherit
	// (current-output-uri-902).
	"current-output-uri": true,
}

// funcPubliclyVisible reports whether a stylesheet function's visibility makes
// it reachable from OUTSIDE the code of its own package — which is what both
// the static context of xsl:evaluate and the choice of an initial entry
// function require. The default visibility of a component declaration is
// private (XSLT 3.0 §3.5.1), so a function that says nothing is not reachable.
func funcPubliclyVisible(fd *FuncDef) bool {
	switch fd.visibility {
	case "public", "final":
		return true
	}
	return false
}

// checkEvaluateVisibility reports XTDE3160 when an expression compiled by
// xsl:evaluate calls a stylesheet function that is not public or final: XSLT
// 3.0 §10.4.1 puts only the public/final functions of the containing package
// into that expression's static context (evaluate-045).
func (eng *engine) checkEvaluateVisibility(fd *FuncDef) error {
	if eng.evaluateDepth == 0 || funcPubliclyVisible(fd) {
		return nil
	}
	return errAt(fd.el, "err:XTDE3160: %s() has visibility %q and is not in the static context of xsl:evaluate",
		fd.name.Local, declVisibility(fd.el))
}
