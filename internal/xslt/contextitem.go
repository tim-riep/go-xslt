package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// contextItemDecl is a compiled xsl:context-item (XSLT 3.0 §6.4): the
// declaration of what a template expects of the context item supplied by
// whoever invokes it.
//
// It is NOT an instruction — it may appear only as the first child of
// xsl:template, is never executed, and instead constrains the focus the
// template body runs with. compileTemplate lifts it out of the body.
type contextItemDecl struct {
	use string         // "required" | "optional" | "absent"
	as  string         // the @as source text ("" = item(), no constraint)
	st  *xpath.SeqType // parsed @as, nil when absent
	el  *xmltree.Node
}

// splitContextItem lifts a leading xsl:context-item out of an xsl:template's
// content and returns it together with the remaining body nodes.
//
// The declaration must be the template's FIRST child (XTSE0010). Whitespace-only
// text before it is not content — it is discarded along with the declaration,
// so a template carrying xml:space="preserve" contributes only the whitespace
// that FOLLOWS the declaration (context-item-019).
func splitContextItem(el *xmltree.Node) (*contextItemDecl, []*xmltree.Node, error) {
	idx := -1
	for i, ch := range el.Children {
		if ch.Kind == xmltree.KindElement && ch.Name.Space == NS && ch.Name.Local == "context-item" {
			if idx >= 0 {
				return nil, nil, errAt(ch, "err:XTSE0010: xsl:template allows at most one xsl:context-item")
			}
			idx = i
		}
	}
	if idx < 0 {
		return nil, childNodesForBody(el), nil
	}
	// Only insignificant whitespace may precede the declaration; anything of
	// substance puts it out of place (context-item-905 an xsl:param, -908 a
	// literal text node).
	for _, ch := range el.Children[:idx] {
		switch ch.Kind {
		case xmltree.KindText:
			if strings.TrimSpace(ch.Value) != "" {
				return nil, nil, errAt(ch, "err:XTSE0010: xsl:context-item must be the first child of xsl:template")
			}
		case xmltree.KindComment, xmltree.KindPI:
		default:
			return nil, nil, errAt(ch, "err:XTSE0010: xsl:context-item must be the first child of xsl:template")
		}
	}
	d, err := compileContextItemDecl(el.Children[idx])
	if err != nil {
		return nil, nil, err
	}
	var body []*xmltree.Node
	for _, ch := range el.Children[idx+1:] {
		if ch.Kind == xmltree.KindElement && ch.Name.Space == NS {
			switch ch.Name.Local {
			case "param", "sort", "with-param", "context-item":
				continue
			}
		}
		body = append(body, ch)
	}
	return d, body, nil
}

// compileContextItemDecl validates one xsl:context-item element.
func compileContextItemDecl(el *xmltree.Node) (*contextItemDecl, error) {
	d := &contextItemDecl{use: "optional", el: el}
	for _, at := range el.Attrs {
		if at.Name.Space != "" {
			continue // a foreign-namespaced attribute is always allowed
		}
		switch at.Name.Local {
		case "use", "as":
		default:
			return nil, errAt(el, "err:XTSE0090: attribute %s is not allowed on xsl:context-item", at.Name.Local)
		}
	}
	if v, ok := el.AttrLocal("use"); ok {
		switch u := strings.TrimSpace(v); u {
		case "required", "optional", "absent":
			d.use = u
		default:
			return nil, errAt(el, "err:XTSE0020: invalid value %q for xsl:context-item/@use", v)
		}
	}
	if v, ok := el.AttrLocal("as"); ok {
		if d.use == "absent" {
			// §6.4: the two attributes contradict each other — a template
			// that will not see a context item cannot constrain its type.
			return nil, errAt(el, "err:XTSE3088: xsl:context-item/@as is not allowed with use=\"absent\"")
		}
		st, err := xpath.ParseItemTypeStrict(v, asTypeCtx(el))
		if err != nil {
			return nil, errAt(el, "err:XTSE0020: invalid xsl:context-item/@as %q: %v", v, err)
		}
		d.as, d.st = v, st
	}
	if len(elementChildren(el)) > 0 {
		return nil, errAt(el, "err:XTSE0260: xsl:context-item must be empty")
	}
	return d, nil
}

// applyContextItem enforces a template's xsl:context-item declaration against
// the focus it is being invoked with, and returns the focus its body runs in.
//
//   - use="absent": the body runs with NO context item at all, whatever the
//     caller's focus is — "." there is XPDY0002 (context-item-016/017), and a
//     body that never touches it is perfectly legal (context-item-011/018).
//   - use="required" with no context item supplied: XTTE3090.
//   - @as: the supplied context item must MATCH the item type as an "instance
//     of" test — no atomization and no function conversion, so an element does
//     not satisfy xs:string (context-item-005) — else XTTE0590.
func (eng *engine) applyContextItem(d *contextItemDecl, r rt) (rt, error) {
	if d.use == "absent" {
		return rt{noFocus: true}, nil
	}
	item, ok := contextItemOf(r)
	if !ok {
		if d.use == "required" {
			return r, errAt(d.el, "err:XTTE3090: the template requires a context item, but none was supplied")
		}
		return r, nil
	}
	if d.st != nil && !xpath.MatchesItemTypeOf(d.st, item, asTypeCtx(d.el)) {
		return r, errAt(d.el, "err:XTTE0590: the context item does not match the required type %q", d.as)
	}
	return r, nil
}

// contextItemOf reports the XDM item the focus currently holds, unwrapping the
// synthetic stand-in nodes the engine uses for non-node context items.
func contextItemOf(r rt) (xpath.Item, bool) {
	if r.item != nil {
		return r.item, true
	}
	if r.node != nil {
		return xpath.ContextItemValue(r.node), true
	}
	return nil, false
}
