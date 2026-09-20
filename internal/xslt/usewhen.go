package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// This file implements XSLT 3.0 conditional inclusion: static parameters
// (xsl:param static="yes") and the use-when attribute. use-when expressions are
// evaluated at compile time in the *static context* — static parameters,
// in-scope namespaces, and a few static functions; there is no source document
// or context item. An element whose use-when is false is removed from the
// stylesheet tree before compilation, exactly as if it had not been written.

// staticEnv resolves variables/namespaces/functions for a static expression.
type staticEnv struct {
	el     *xmltree.Node
	params map[string]xpath.Object
	// resolver serves fn:doc/fn:unparsed-text in a static expression — a
	// shadow attribute may read the stylesheet back with doc('')
	// (package-version-011). nil is fine: fn:doc then reports FODC0002.
	resolver xpath.ResourceResolver
	// unset, when non-nil, names static parameters with no known value: they
	// resolve as UNBOUND (an error) rather than as the empty string, so a
	// shadow attribute that depends on one is left unexpanded instead of
	// silently collapsing to "" (see evalStaticAVT).
	unset map[string]bool
	// sawUnset records that an unset static parameter was actually referenced.
	sawUnset bool
}

func (e *staticEnv) ResolveNS(prefix string) (string, bool) {
	if prefix == "" {
		return "", true
	}
	return e.el.LookupPrefix(prefix)
}

func (e *staticEnv) ResolveVar(prefix, local string) (xpath.Object, bool) {
	uri := ""
	if prefix != "" {
		uri, _ = e.el.LookupPrefix(prefix)
	}
	key := clark(uri, local)
	if e.unset != nil && e.unset[key] {
		e.sawUnset = true
		return nil, false
	}
	v, ok := e.params[key]
	return v, ok
}

func (e *staticEnv) ResolveFunc(prefix, local string, args []xpath.Object, _ *xpath.Context) (xpath.Object, bool, error) {
	if prefix != "" {
		return nil, false, nil
	}
	switch local {
	case "system-property":
		name := xpath.ToString(arg0(args))
		if !isLexicalQName(name) && !strings.HasPrefix(name, "Q{") {
			return nil, true, errAt(e.el, "err:XTDE1390: %q is not a valid QName", name)
		}
		uri, local := resolveAvailableName(e.el, name, "")
		return staticSystemProperty(uri, local), true, nil
	case "function-available":
		name := xpath.ToString(arg0(args))
		if !isLexicalQName(name) {
			return nil, true, errAt(nil, "err:XTDE1400: %q is not a valid QName", name)
		}
		if err := checkPrefixBound(e.el, name); err != nil {
			return nil, true, errAt(nil, "err:XTDE1400: unbound prefix in %q", name)
		}
		hasArity := len(args) > 1
		arity := 0
		if hasArity {
			arity = int(xpath.ToNumber(args[1]))
		}
		uri, local := funcAvailableName(e.el, name)
		if uri == xpathFunctionsNS && !staticContextXSLTFuncs[local] {
			// The STATIC context of a use-when expression contains the F&O
			// function library plus exactly four XSLT-defined functions
			// (XSLT 3.0 §3.9.1). Every other XSLT-defined function needs a
			// dynamic XSLT context that does not exist yet at this point, so
			// it is NOT available — use-when-0407a expects current(), key(),
			// unparsed-entity-uri() and unparsed-entity-public-id() to report
			// false here while still being available at run time (fn:generate-id
			// reports true because it is a genuine F&O 3.0 function, which is
			// what the StdFuncArity check below distinguishes).
			if _, xsltOnly := xsltExtraArity[local]; xsltOnly {
				if _, _, known := xpath.StdFuncArity(uri, local); !known {
					return false, true, nil
				}
			}
		}
		return stdFuncAvailable(uri, local, arity, hasArity), true, nil
	case "element-available":
		// Only the XSLT instructions this processor implements are
		// available; an extension namespace it knows nothing about is not
		// (use-when-0108's saxon:assign, use-when-0214's t:values-of).
		name := xpath.ToString(arg0(args))
		if !isLexicalQName(name) && !strings.HasPrefix(name, "Q{") {
			return nil, true, errAt(e.el, "err:XTDE1440: %q is not a valid QName", name)
		}
		uri, local := elementAvailableName(e.el, name)
		_, known := xsltElemSpecs[local]
		return uri == NS && known, true, nil
	case "type-available":
		name := xpath.ToString(arg0(args))
		if !isLexicalQName(name) && !strings.HasPrefix(name, "Q{") {
			return nil, true, errAt(e.el, "err:XTDE1425: %q is not a valid QName", name)
		}
		uri, local := resolveAvailableName(e.el, name, "")
		// A static expression is evaluated before any xsl:import-schema has been
		// compiled, so only the built-in types can be in scope here.
		return typeAvailable(nil, uri, local), true, nil
	case "available-system-properties":
		return availableSystemProperties(), true, nil
	case "transform":
		// fn:transform is available in the static context too: a static
		// variable may run a transformation to decide what the stylesheet
		// should contain (transform-004).
		// The static context has no compiled stylesheet to carry the host's
		// package registry, so package-based invocation is unavailable here.
		opts, oerr := xfOptsArg(args)
		if oerr != nil {
			return nil, true, oerr
		}
		v, err := xfRun(opts, xfHost{baseURI: xpath.NodeBaseURI(e.el, ""), resolver: e.resolver})
		return v, true, err
	}
	return nil, false, nil
}

func (e *staticEnv) eval(expr string) (xpath.Object, error) {
	p, err := xpath.Parse(expr)
	if err != nil {
		return nil, err
	}
	// A static expression has NO context item at all — referring to "."
	// (or position()/last()) is an error, not a silent empty sequence
	// (use-when-0123) — and its static base URI is that of the module the
	// attribute appears in (use-when-0119's static-base-uri()).
	return p.Eval(&xpath.Context{
		Vars: e, NS: e, Funcs: e,
		DefaultElemNS: xpathDefaultNS(e.el),
		BaseURI:       xpath.NodeBaseURI(e.el, ""),
		Resolver:      e.resolver,
		// doc('')/document('') in a static expression names the module the
		// expression is written in, exactly as at run time. Without this the
		// resolver is asked to RETRIEVE the stylesheet from disk, which fails
		// for a module compiled from text and is wrong even when it succeeds
		// (package-version-011 reads its own xsl:package/@version through a
		// _package-version shadow attribute).
		HomeDoc: staticHomeDoc(e.el),
		NoFocus: true,
	})
}

// staticSystemProperty answers fn:system-property in the static context, with
// the same values the runtime implementation reports.
func staticSystemProperty(uri, local string) string {
	if uri != NS {
		return ""
	}
	return xsltSystemProperty(local)
}

// settleStaticDecl records a static="yes" xsl:param's HOST-SUPPLIED value into
// c.staticDecls — the map bindSettledStatic (compile.go) reads to bind the
// RUNTIME value of the corresponding VarDef. For a multi-module stylesheet
// this is (also) done by resolveStaticDecls, which additionally enforces
// cross-module import-precedence/consistency (XTSE3450) over every static
// declaration, not just host-supplied ones; this single-module fast path
// only needs to cover the host-supplied case specifically (there is only one
// module, so only one declaration of any given name can exist here either
// way) — deliberately NOT the declaration's own plain @select default, which
// stays dynamically re-evaluated at run time exactly as before, because the
// static evaluation context does not (yet) support every expression shape a
// plain runtime @select does (available-system-properties-001 declares
// several static variables selecting named-function-references/
// function-lookup results with no host override at all, which is why this
// is scoped to the host-supplied branch only, not every settled static
// declaration).
func (c *compiler) settleStaticDecl(key string, value xpath.Object, unset bool) {
	if c.staticDecls == nil {
		c.staticDecls = map[string]staticDecl{}
	}
	c.staticDecls[key] = staticDecl{value: value, unset: unset}
}

// applyStatic collects the static parameters declared in root and removes any
// use-when="false" subtree. It is applied to every module (principal + included).
func (c *compiler) applyStatic(root *xmltree.Node, principal bool) error {
	if c.staticParams == nil {
		c.staticParams = map[string]xpath.Object{}
	}
	// Shadow attributes on the module root itself (_version, _package-version,
	// _default-collation, …) cannot depend on static parameters declared inside
	// the module, so they are expanded first; every other element's are
	// expanded just before that element is looked at (below, and in
	// pruneUseWhen), so a shadow AVT may use any static parameter declared
	// EARLIER in document order.
	if err := c.expandShadowAttrs(root); err != nil {
		return err
	}
	for _, ch := range elementChildren(root) {
		if err := c.expandShadowAttrs(ch); err != nil {
			return err
		}
		// Both static parameters and static variables enter the static context.
		if ch.Name.Space != NS || (ch.Name.Local != "param" && ch.Name.Local != "variable") {
			continue
		}
		// @static is an XSLT boolean: "yes"/"true"/"1", with surrounding
		// whitespace allowed (use-when-0140 writes static=" 1 ").
		st, _ := ch.AttrLocal("static")
		if !isXSLTTrue(strings.TrimSpace(st)) {
			continue
		}
		// A static param/variable's own use-when (if present) is evaluated
		// in the static context established by EARLIER declarations only —
		// this element's own binding is NOT yet in c.staticParams at this
		// point, so a self-reference (static-019: use-when="$static-param"
		// on that very declaration) sees it as undefined -> XPST0008,
		// matching the ordinary declaration-order restriction static params
		// already enforce for @select below. A false use-when excludes the
		// declaration entirely (never added to c.staticParams), same as an
		// ordinary element use-when-pruned elsewhere in the tree.
		if uw, ok := useWhenAttr(ch); ok {
			env := &staticEnv{el: ch, params: c.staticParams}
			v, err := env.eval(uw)
			if err != nil {
				return errAt(ch, "use-when: %v", err)
			}
			if !xpath.ToBool(v) {
				continue
			}
		}
		name, _ := ch.AttrLocal("name")
		qn := resolveQName(ch, name)
		key := clark(qn.Space, qn.Local)
		var val xpath.Object = ""
		// A static param/variable with no @select must have EMPTY content
		// (XTSE0010) — see the fuller note at the else branch below. It is a
		// property of the DECLARATION, so it holds whether or not the host
		// supplies a value for it (static-006/006a supply one and still
		// expect XTSE0010).
		if _, hasSel := ch.AttrLocal("select"); !hasSel {
			for _, cc := range ch.Children {
				bad := cc.Kind == xmltree.KindElement ||
					(cc.Kind == xmltree.KindText && strings.TrimSpace(cc.Value) != "")
				if bad {
					return errAt(ch, "err:XTSE0010: static xsl:%s %q must not have non-empty content", ch.Name.Local, name)
				}
			}
		}
		// A host-supplied static parameter value (CompileAtWithStatic)
		// overrides the declaration's own @select — only for xsl:param; a
		// static xsl:variable is never externally settable.
		// A name the cross-module tree-order pass already settled keeps the
		// value (and import precedence) it decided; this per-module walk must
		// not re-bind it out of order (staticdecl.go).
		_, settled := c.staticDecls[key]
		if sel, ok := c.hostStaticSelect(ch, key); ok {
			env := &staticEnv{el: ch, params: c.staticParams, resolver: c.staticResolver()}
			v, err := env.eval(sel)
			if err != nil {
				return errAt(ch, "static param %s: %v", name, err)
			}
			if !settled {
				cv, cerr := coerceStaticValue(ch, name, v)
				if cerr != nil {
					return cerr
				}
				c.staticParams[key] = cv
				// A single-module stylesheet never runs the cross-module
				// resolveStaticDecls pass (compile.go only calls it when
				// hasSecondaryModules), which is the ONLY other place that
				// populates c.staticDecls — the map bindSettledStatic reads
				// to bind a static="yes" xsl:param's RUNTIME value. Without
				// this, a host-supplied override was seen correctly by every
				// static (use-when/shadow-attribute) evaluation but silently
				// ignored at run time, where $static-param still evaluated
				// the declaration's own @select from scratch
				// (fn-transform-50/51/52).
				c.settleStaticDecl(key, cv, false)
			}
			continue
		}
		if sel, ok := ch.AttrLocal("select"); ok {
			env := &staticEnv{el: ch, params: c.staticParams, resolver: c.staticResolver()}
			v, err := env.eval(sel)
			if err != nil {
				return errAt(ch, "static %s %s: %v", ch.Name.Local, name, err)
			}
			cv, cerr := coerceStaticValue(ch, name, v)
			if cerr != nil {
				return cerr
			}
			val = cv
		} else {
			// The XTSE0010 "no @select means empty content" check for a
			// static param/variable (unlike a dynamic xsl:param/xsl:variable,
			// its default can only be a static expression — there is no
			// document or context to run a sequence constructor against at
			// compile time; static-006/006a/007/007a) now runs ABOVE, before
			// the host-supplied-value branch, so a host value cannot mask it.
			//
			// No host-supplied value and no declared default: the parameter
			// is UNBOUND rather than the empty string, so a shadow attribute
			// that depends on it is left unexpanded instead of silently
			// collapsing to "" (see evalStaticAVT/staticEnv.unset).
			if !settled {
				if c.staticUnset == nil {
					c.staticUnset = map[string]bool{}
				}
				c.staticUnset[key] = true
			}
		}
		if !settled {
			c.staticParams[key] = val
		}
	}
	// A use-when on an INCLUDED/IMPORTED module's own root element excludes
	// the whole module (use-when-0116). On the PRINCIPAL module it has no
	// effect at all — a static error elsewhere on that element must still be
	// reported (use-when-0227).
	if uw, ok := useWhenAttr(root); ok && !principal {
		env := &staticEnv{el: root, params: c.staticParams, resolver: c.staticResolver()}
		v, err := env.eval(uw)
		if err != nil {
			return errAt(root, "use-when: %v", err)
		}
		if !xpath.ToBool(v) {
			root.Children = nil
			return nil
		}
	}
	return c.pruneUseWhen(root)
}

// hostStaticSelect returns the host-supplied XPath expression for the static
// parameter declared by el (whose expanded name is key), if any. The host's
// key is matched both as the expanded name (resolved against el's in-scope
// namespaces, so an unprefixed host name reaches an unprefixed declaration)
// and verbatim, which covers a Q{uri}local spelling.
func (c *compiler) hostStaticSelect(el *xmltree.Node, key string) (string, bool) {
	if len(c.hostStatic) == 0 || el.Name.Local != "param" {
		return "", false
	}
	for hostName, expr := range c.hostStatic {
		qn := resolveQName(el, hostName)
		if clark(qn.Space, qn.Local) == key || hostName == key {
			return expr, true
		}
	}
	return "", false
}

// expandShadowAttrs implements XSLT 3.0 §3.9 "shadow attributes" for a SINGLE
// element el (not its descendants — the caller, applyStatic/pruneUseWhen,
// recurses so that each element is expanded just before it is looked at,
// letting a shadow AVT reference any static parameter declared earlier in
// document order): an attribute whose name is an ordinary XSLT attribute name
// prefixed with an underscore (`_name` on an XSLT element, `xsl:_name` on a
// literal result element) holds an attribute value template evaluated in the
// STATIC context — static parameters, in-scope namespaces, the static
// function subset, no context item — whose result supplies the value of the
// corresponding real attribute `name`, replacing any statically-written
// value. It is idempotent (the shadow attribute is kept), so re-running it
// simply recomputes the same value.
func (c *compiler) expandShadowAttrs(el *xmltree.Node) error {
	if el.Kind != xmltree.KindElement {
		return nil
	}
	xsltElem := el.Name.Space == NS
	for _, a := range append([]*xmltree.Node(nil), el.Attrs...) {
		if !strings.HasPrefix(a.Name.Local, "_") || len(a.Name.Local) < 2 {
			continue
		}
		var target xmltree.Name
		switch {
		case xsltElem && a.Name.Space == "":
			target = xmltree.Name{Local: a.Name.Local[1:]}
		case !xsltElem && a.Name.Space == NS:
			target = xmltree.Name{Space: NS, Local: a.Name.Local[1:], Prefix: a.Name.Prefix}
		default:
			continue
		}
		val, expanded, err := c.evalStaticAVT(el, a.Value)
		if err != nil {
			return errAt(el, "shadow attribute %s: %v", a.Name.Local, err)
		}
		if !expanded {
			// The shadow attribute depends on a static parameter whose value
			// the host never supplied: leave whatever was written statically
			// in place rather than substituting "".
			continue
		}
		el.SetAttr(target, val)
	}
	return nil
}

// evalStaticAVT evaluates an attribute value template in the static context
// of element el (no context item, static parameters only). expanded is false
// only when the AVT references an UNSET static parameter (one with neither a
// host-supplied value nor a declared default) — see staticEnv.unset.
func (c *compiler) evalStaticAVT(el *xmltree.Node, value string) (result string, expanded bool, err error) {
	a, perr := parseAVTFor(el, value)
	if perr != nil {
		return "", false, perr
	}
	if lit, ok := a.isConstant(); ok {
		return lit, true, nil
	}
	env := &staticEnv{el: el, params: c.staticParams, unset: c.staticUnset, resolver: c.staticResolver()}
	var b strings.Builder
	for _, part := range a.parts {
		if part.expr == nil {
			b.WriteString(part.literal)
			continue
		}
		v, everr := part.expr.Eval(&xpath.Context{
			Vars: env, NS: env, Funcs: env,
			DefaultElemNS: xpathDefaultNS(el),
			BaseURI:       xpath.NodeBaseURI(el, ""),
			Resolver:      env.resolver,
			// doc('') in a shadow attribute names this very module
			// (package-version-011 reads its own xsl:package/@version).
			HomeDoc: staticHomeDoc(el),
			NoFocus: true,
		})
		if everr != nil {
			if env.sawUnset {
				return "", false, nil
			}
			return "", false, everr
		}
		b.WriteString(joinSeq(v, " "))
	}
	return b.String(), true, nil
}

// staticResolver lazily builds the fn:doc/fn:unparsed-text resolver used while
// evaluating static expressions (use-when, static variable selects, shadow
// attributes). It is rooted at the module directory being compiled, so fn:doc
// with an empty URI reads the stylesheet module itself.
func (c *compiler) staticResolver() xpath.ResourceResolver {
	if c.staticRes == nil {
		c.staticRes = newFileResolver(c.baseDir, nil)
	}
	return c.staticRes
}

// pruneUseWhen recursively removes element children whose use-when evaluates to
// false in the static context.
func (c *compiler) pruneUseWhen(node *xmltree.Node) error {
	var kept []*xmltree.Node
	for _, ch := range node.Children {
		if ch.Kind == xmltree.KindElement {
			// Shadow attributes are expanded before use-when is consulted —
			// _use-when itself is a shadow attribute (shadow-005) — and only
			// for elements that survive, so a shadow AVT inside an excluded
			// subtree is never evaluated.
			if err := c.expandShadowAttrs(ch); err != nil {
				return err
			}
			if uw, ok := useWhenAttr(ch); ok {
				env := &staticEnv{el: ch, params: c.staticParams, resolver: c.staticResolver()}
				v, err := env.eval(uw)
				if err != nil {
					// An XSLT element this version does not recognize, under
					// forwards-compatible processing, is ignored along with
					// its content — so a use-when on it that this version
					// cannot even evaluate (it may call a function only the
					// FUTURE version defines, forwards-008) simply excludes
					// the element instead of failing the compilation. The
					// expression is still evaluated first, since a use-when
					// this version CAN evaluate governs inclusion as usual
					// (version-030's element-available test).
					if ch.Name.Space == NS && inForwardsCompatScope(ch) {
						if _, known := xsltElemSpecs[ch.Name.Local]; !known {
							continue
						}
					}
					return errAt(ch, "use-when: %v", err)
				}
				if !xpath.ToBool(v) {
					continue // excluded from the stylesheet
				}
			}
		}
		if err := c.pruneUseWhen(ch); err != nil {
			return err
		}
		kept = append(kept, ch)
	}
	node.Children = kept
	return nil
}

// evalStaticAVT evaluates s (an attribute's raw text, possibly a text value
// template with {…} sections) in the STATIC context — staticParams only, no
// document, no focus, exactly use-when's own context (staticEnv.eval) —
// reusing the ordinary AVT parser/part model (avt.go) so a shadow attribute
// gets identical {…}/literal-text/escaping handling to any other AVT. Each
// {expr} part's result is stringified the same way a normal (dynamic) AVT
// does (joinSeq with a single-space separator — XSLT 3.0 §5.6.2), so this
// only differs from evalAVT in WHERE the expression is evaluated (statically,
// with no document/focus) and in taking raw text rather than a pre-compiled
// *avt (a shadow attribute's text is read straight off the source tree,
// before the normal compiler ever sees it).
func evalStaticAVT(s string, el *xmltree.Node, staticParams map[string]xpath.Object) (string, error) {
	a, err := parseAVTFor(el, s)
	if err != nil {
		return "", err
	}
	if lit, ok := a.isConstant(); ok {
		return lit, nil
	}
	env := &staticEnv{el: el, params: staticParams}
	var b strings.Builder
	for _, part := range a.parts {
		if part.expr == nil {
			b.WriteString(part.literal)
			continue
		}
		v, err := part.expr.Eval(&xpath.Context{
			Vars: env, NS: env, Funcs: env,
			DefaultElemNS: xpathDefaultNS(el),
			BaseURI:       xpath.NodeBaseURI(el, ""),
			HomeDoc:       staticHomeDoc(el),
			NoFocus:       true,
		})
		if err != nil {
			return "", err
		}
		b.WriteString(joinSeq(v, " "))
	}
	return b.String(), nil
}

// resolveShadowAttrs walks the WHOLE subtree rooted at node, resolving every
// "shadow attribute" — XSLT 3.0's mechanism for computing an attribute's
// value from static params where a literal text value template is not
// otherwise permitted (e.g. xsl:value-of/@select is a plain XPath
// expression, never a TVT, so "_select" lets a stylesheet compute that
// expression's TEXT itself, from a static param, instead — the W3C
// xslt30-test suite's own maps-901..907 and date-094/095 test-case
// generators are built entirely on this: one stylesheet, one shadow
// attribute, a different static-param-supplied expression string per test
// case). An unprefixed, no-namespace attribute whose name starts with "_"
// (and is more than just "_") is evaluated as a text value template via
// evalStaticAVT, and its result REPLACES the real attribute (the same name
// with the leading "_" stripped, overwriting any literal same-named
// attribute also present — the two are meant to be mutually exclusive in
// practice); the shadow attribute itself is then removed so ordinary
// static validation/compilation never sees an attribute name it doesn't
// recognize. Must run AFTER every module's static params (staticParams,
// i.e. compiler.staticParams) are fully populated (every module's own
// applyStatic call has already run) but BEFORE static validation
// (validateTree) or normal compilation reads the real attributes.
func resolveShadowAttrs(node *xmltree.Node, staticParams map[string]xpath.Object) error {
	// Shadow attributes are an XSLT-INSTRUCTION mechanism only — an
	// underscore-prefixed attribute on a literal result element is just an
	// ordinary (if unusually spelled) OUTPUT attribute name, left completely
	// alone (shadow-007: <out _one="1.0" _two="two"/> must serialize with
	// those exact literal names, not have them silently promoted/stripped).
	if node.Kind == xmltree.KindElement && node.Name.Space == NS {
		var shadows []*xmltree.Node
		kept := node.Attrs[:0:0]
		for _, a := range node.Attrs {
			if a.Name.Space == "" && strings.HasPrefix(a.Name.Local, "_") && len(a.Name.Local) > 1 {
				shadows = append(shadows, a)
				continue
			}
			kept = append(kept, a)
		}
		node.Attrs = kept
		for _, a := range shadows {
			real := a.Name.Local[1:]
			s, err := evalStaticAVT(a.Value, node, staticParams)
			if err != nil {
				return errAt(node, "shadow attribute _%s: %v", real, err)
			}
			node.SetAttr(xmltree.Name{Local: real}, s)
		}
	}
	for _, ch := range node.Children {
		if err := resolveShadowAttrs(ch, staticParams); err != nil {
			return err
		}
	}
	return nil
}

// useWhenAttr returns the use-when expression of an element: the no-namespace
// "use-when" on XSLT elements, or "xsl:use-when" on literal result elements.
func useWhenAttr(el *xmltree.Node) (string, bool) {
	if el.Name.Space == NS {
		return el.AttrLocal("use-when")
	}
	return el.Attr(NS, "use-when")
}

// staticContextXSLTFuncs are the XSLT-defined functions that ARE part of the
// static context of a use-when expression (XSLT 3.0 §3.9.1) — the four whose
// result depends only on the processor and the stylesheet, not on a source
// document or an evaluation focus.
var staticContextXSLTFuncs = map[string]bool{
	"element-available":  true,
	"function-available": true,
	"type-available":     true,
	"system-property":    true,
}

// staticHomeDoc returns the document node of the module el belongs to, for
// doc(”)/document(”) in a static expression. Nil when el is detached.
func staticHomeDoc(el *xmltree.Node) *xmltree.Node {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind == xmltree.KindDocument {
			return cur
		}
	}
	return nil
}

// coerceStaticValue applies a static xsl:param/xsl:variable's own declared @as
// to the value bound to it, under the function-conversion rules. A value that
// cannot be reconciled with the declared type is XTTE0590, exactly as for a
// runtime parameter — it is simply detected at compile time here because a
// static parameter is bound at compile time (static-013c supplies the STRING
// "111" to a parameter declared as="xs:integer").
func coerceStaticValue(el *xmltree.Node, name string, v xpath.Object) (xpath.Object, error) {
	as, ok := el.AttrLocal("as")
	if !ok || strings.TrimSpace(as) == "" {
		return v, nil
	}
	cv, ok := xpath.CoerceToDeclaredTypeCtx(as, v, asTypeCtx(el))
	if !ok {
		return nil, errAt(el, "err:XTTE0590: value of static xsl:%s %q does not match declared type %q", el.Name.Local, name, as)
	}
	return cv, nil
}
