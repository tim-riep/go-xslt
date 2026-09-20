package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	// xsl:attribute-set: store its xsl:attribute children as an instruction body
	// keyed by name; applied via use-attribute-sets.
	declRegistry["attribute-set"] = func(c *compiler, ss *Stylesheet, el *xmltree.Node) error {
		name, ok := el.AttrLocal("name")
		if !ok {
			return errAt(el, "xsl:attribute-set requires a name")
		}
		// xsl:attribute-set's content model is (xsl:attribute)* only — any
		// text-node children are stray whitespace kept around solely because
		// an xml:space="preserve" ancestor stopped the stylesheet's own
		// whitespace stripping (whitespace-006). They carry no meaning here
		// and must never be compiled as literal output: doing so would try
		// to append a text child to the element being decorated BEFORE a
		// later xsl:attribute runs, tripping XTDE0420 ("attribute after
		// children") on a set that is otherwise pure xsl:attribute content.
		var kids []*xmltree.Node
		for _, c2 := range childNodesForBody(el) {
			if c2.Kind == xmltree.KindText {
				continue
			}
			kids = append(kids, c2)
		}
		body, err := c.compileSequence(kids)
		if err != nil {
			return err
		}
		decl := attrSetDecl{body: body}
		if v, ok := el.AttrLocal("streamable"); ok {
			decl.streamable = isXSLTTrue(strings.TrimSpace(v))
		}
		if use, ok := el.AttrLocal("use-attribute-sets"); ok {
			decl.uses = resolveAttrSetNames(el, strings.Fields(use))
		}
		// The set's name (and its own use-attribute-sets, above) is a QName
		// resolved against the DECLARATION's in-scope namespaces, keyed by
		// its expanded (Clark) form — matching attrSetNames below, so a
		// use-attribute-sets reference through a DIFFERENT prefix bound to
		// the same namespace URI still finds it (attribute-set-1806, mirrors
		// key-013's xsl:key name resolution).
		key := clarkName(resolveQName(el, name))
		ss.attrSets[key] = append(ss.attrSets[key], decl)
		return nil
	}

	// xsl:global-context-item declares what the stylesheet expects of the
	// GLOBAL context item (the one supplied on invocation). Its attribute
	// vocabulary is validated in validate.go; all that is recorded here is
	// @use, which Transform enforces (XTDE3086) before running anything.
	declRegistry["global-context-item"] = func(c *compiler, ss *Stylesheet, el *xmltree.Node) error {
		if v, ok := el.AttrLocal("use"); ok {
			ss.globalCtxUse = strings.TrimSpace(v)
		} else if ss.globalCtxUse == "" {
			ss.globalCtxUse = "optional"
		}
		return nil
	}
}

// applyAttrSets executes the named attribute-sets into el (adding attributes).
// An undeclared name is XTSE0710; a circular reference is XTSE0720.
func (eng *engine) applyAttrSets(names []string, r rt, el *xmltree.Node) error {
	// Only top-level (global) variables/params are visible inside an
	// xsl:attribute-set's own body — never the calling instruction's local
	// variables (attribute-set-1802): hide the local scope stack for the
	// duration of the attribute-set expansion and restore it afterward.
	prevScopes := eng.scopes
	eng.scopes = nil
	defer func() { eng.scopes = prevScopes }()
	return eng.applyAttrSetsRec(names, r, el, map[string]bool{})
}

func (eng *engine) applyAttrSetsRec(names []string, r rt, el *xmltree.Node, active map[string]bool) error {
	for _, name := range names {
		decls, ok := eng.sheet.attrSets[name]
		if !ok {
			return errAt(nil, "err:XTSE0710: no xsl:attribute-set named %q", name)
		}
		if active[name] {
			return errAt(nil, "err:XTSE0720: circular xsl:attribute-set reference %q", name)
		}
		active[name] = true
		// Two or more xsl:attribute-set elements sharing this name (merged at
		// the same import precedence) are applied as separate units in
		// document order — each declaration's own use-attribute-sets expands
		// immediately before that SAME declaration's own attributes, rather
		// than hoisting every declaration's use-attribute-sets ahead of every
		// declaration's body (attribute-set-1512).
		for _, decl := range decls {
			if len(decl.uses) > 0 {
				if err := eng.applyAttrSetsRec(decl.uses, r, el, active); err != nil {
					return err
				}
			}
			if err := eng.execSequence(decl.body, r, el); err != nil {
				return err
			}
		}
		delete(active, name)
	}
	return nil
}

// attrSetNames splits a use-attribute-sets attribute value into the expanded
// (Clark) names of the attribute sets it references, resolved against el's
// in-scope namespaces — matching how the xsl:attribute-set declaration's own
// @name is keyed (declRegistry["attribute-set"] above), so a reference through
// a different prefix bound to the same namespace URI still resolves
// (attribute-set-1806).
func attrSetNames(el *xmltree.Node) []string {
	// On xsl:element/xsl:copy it is a no-namespace attribute; on literal result
	// elements it is xsl:use-attribute-sets (XSLT namespace).
	if v, ok := el.AttrLocal("use-attribute-sets"); ok {
		return resolveAttrSetNames(el, strings.Fields(v))
	}
	if v, ok := el.Attr(NS, "use-attribute-sets"); ok {
		return resolveAttrSetNames(el, strings.Fields(v))
	}
	return nil
}

// checkAttributeSetRefs statically validates every use-attribute-sets /
// xsl:use-attribute-sets reference in the stylesheet (on xsl:copy, xsl:element,
// xsl:attribute-set, and literal result elements) against the set of
// xsl:attribute-set names actually declared (XTSE0710) — this must be
// detected even along a sequence constructor path never executed at runtime,
// so it cannot rely solely on the dynamic check in applyAttrSetsRec.
func checkAttributeSetRefs(ss *Stylesheet, mods []modChildren) error {
	var walk func(el *xmltree.Node) error
	walk = func(el *xmltree.Node) error {
		if el.Kind != xmltree.KindElement {
			return nil
		}
		var v string
		var ok bool
		if el.Name.Space == NS {
			switch el.Name.Local {
			case "copy", "element", "attribute-set":
				v, ok = el.AttrLocal("use-attribute-sets")
			}
		} else {
			v, ok = el.Attr(NS, "use-attribute-sets")
		}
		if ok {
			// A streamable set may only reference streamable sets (XTSE0730):
			// otherwise a caller's streamability would depend on a declaration
			// it cannot see.
			streamable := el.Name.Space == NS && el.Name.Local == "attribute-set"
			if streamable {
				sv, has := el.AttrLocal("streamable")
				streamable = has && isXSLTTrue(strings.TrimSpace(sv))
			}
			for _, name := range resolveAttrSetNames(el, strings.Fields(v)) {
				decls, exists := ss.attrSets[name]
				if !exists {
					return errAt(el, "err:XTSE0710: no xsl:attribute-set named %q", name)
				}
				if !streamable {
					continue
				}
				for _, d := range decls {
					if !d.streamable {
						return errAt(el, "err:XTSE0730: a streamable xsl:attribute-set may not use %q, which is not declared streamable", name)
					}
				}
			}
		}
		for _, ch := range el.Children {
			if err := walk(ch); err != nil {
				return err
			}
		}
		return nil
	}
	for _, mod := range mods {
		for _, child := range mod.children {
			if err := walk(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkCallTemplateTunnelParams implements XTSE0680: "In the case of
// xsl:call-template, it is a static error to pass a non-tunnel parameter
// named x to a template that does not have a non-tunnel template parameter
// named x." xsl:call-template's target is always statically named (unlike
// apply-templates' dynamically-dispatched rule match), so the compiler can
// check every xsl:with-param against the resolved template's own xsl:param
// declarations directly (tunnel-0109/0110: with-param par1/par2 supplied
// non-tunnel against a template whose par1/par2 are BOTH declared
// tunnel="yes"; error-0680a: with-param "my:p" against a template with no
// params at all).
func checkCallTemplateTunnelParams(ss *Stylesheet, mods []modChildren) error {
	var walk func(el *xmltree.Node) error
	walk = func(el *xmltree.Node) error {
		if el.Kind != xmltree.KindElement {
			return nil
		}
		if el.Name.Space == NS && el.Name.Local == "call-template" {
			if name, ok := el.AttrLocal("name"); ok {
				if err := checkOneCallTemplate(ss, el, name); err != nil {
					return err
				}
			}
		}
		for _, ch := range el.Children {
			if err := walk(ch); err != nil {
				return err
			}
		}
		return nil
	}
	for _, mod := range mods {
		for _, child := range mod.children {
			if err := walk(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkOneCallTemplate(ss *Stylesheet, el *xmltree.Node, name string) error {
	// Mirrors execCallTemplate's own resolution exactly (transform.go): an
	// exact raw-lexical-name lookup first, then an expanded-name fallback
	// for a call site using a different prefix bound to the same URI
	// (call-template-1701). A name that resolves to NO template at all is a
	// separate (currently unenforced) static error, out of scope here — if
	// resolution fails, there is nothing to check against.
	tmpl, ok := ss.named[name]
	if !ok {
		target := clarkName(resolveQName(el, name))
		for tname, t := range ss.named {
			if tname == name {
				continue
			}
			if t.el != nil && clarkName(resolveQName(t.el, tname)) == target {
				tmpl, ok = t, true
				break
			}
		}
	}
	if !ok {
		return nil
	}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS || ch.Name.Local != "with-param" {
			continue
		}
		pname, ok := ch.AttrLocal("name")
		if !ok {
			continue
		}
		if v, ok := ch.AttrLocal("tunnel"); ok && xsltBool(v) {
			continue // a tunnel with-param is never subject to this check
		}
		target := clarkName(resolveQName(ch, pname))
		hasNonTunnelMatch := false
		for _, p := range tmpl.params {
			if clarkName(p.name) == target && !p.tunnel {
				hasNonTunnelMatch = true
				break
			}
		}
		if !hasNonTunnelMatch {
			// XSLT 1.0/2.0 silently ignored an unwanted with-param; XTSE0680
			// is a 3.0-era tightening, so backwards-compatible processing
			// (the call-template instruction's own effective version < 2.0)
			// reverts to the lenient behaviour (backwards-013).
			if backwardsCompatRun() && inBackwardsCompatScope(el) {
				continue
			}
			return errAt(ch, "err:XTSE0680: xsl:call-template supplies non-tunnel parameter %q, but template %q has no matching non-tunnel xsl:param", pname, name)
		}
	}
	return nil
}

// resolveAttrSetNames resolves each lexical QName in names to its expanded
// (Clark) form using el's in-scope namespaces.
func resolveAttrSetNames(el *xmltree.Node, names []string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = clarkName(resolveQName(el, n))
	}
	return out
}
