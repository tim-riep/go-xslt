package xpath

import (
	"fmt"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["node-name"] = fnNodeName
	coreFuncs["nilled"] = fnNilled
	coreFuncs["data"] = fnData
	coreFuncs["base-uri"] = fnBaseURI
	coreFuncs["document-uri"] = fnDocumentURI
	coreFuncs["static-base-uri"] = fnStaticBaseURI
}

// fnStaticBaseURI implements fn:static-base-uri() as xs:anyURI?. It returns the
// static base URI in scope (Context.BaseURI), or the empty sequence if unknown.
func fnStaticBaseURI(c *Context, a []Object) (Object, error) {
	if c.BaseURI == "" {
		return Sequence{}, nil
	}
	return NewAnyURI(c.BaseURI), nil
}

// accFirstNode returns the node from the optional first argument, or the
// context node when the argument is omitted (nil arg). It distinguishes
// "argument omitted" from "argument is the empty sequence": when an explicit
// empty argument is supplied the result node is nil.
func accFirstNode(c *Context, a []Object) *xmltree.Node {
	if len(a) == 0 {
		return c.Node
	}
	ns, _ := ToNodeSet(a[0])
	return ns.first()
}

// accHasName reports whether a node kind carries a usable expanded QName.
// Elements, attributes, processing instructions and (prefixed) namespace
// nodes do; document, text, comment nodes and the default-namespace node
// (unprefixed) have no node-name (XDM 3.1 6.5.3 dm:node-name).
func accHasName(n *xmltree.Node) bool {
	switch n.Kind {
	case xmltree.KindElement, xmltree.KindAttribute, xmltree.KindPI:
		return true
	case xmltree.KindNamespace:
		return n.Name.Local != ""
	default:
		return false
	}
}

// fnNodeName implements fn:node-name($arg as node()?) as xs:QName?.
// Returns the empty sequence when there is no node or the node has no name.
// A non-node argument or context item is XPTY0004 (79[node-name()],
// node-name("string") — fn-node-name-30/31, K2-NodeNameFunc-2) and an
// absent focus is XPDY0002 (fn-node-name-32).
func fnNodeName(c *Context, a []Object) (Object, error) {
	n, err := firstNodeOrContextErr(c, a)
	if err != nil {
		return nil, err
	}
	if n == nil || !accHasName(n) {
		return Sequence{}, nil
	}
	if n.Name.Local == "" && n.Name.Space == "" {
		return Sequence{}, nil
	}
	return NewQName(n.Name), nil
}

// fnNilled implements fn:nilled($arg as node()?) as xs:boolean?.
// Our tree is untyped, so an element is never nilled (false); any other node
// kind yields the empty sequence (fn-nilled-2/25/26: text, attribute,
// document). A non-node argument or context item is XPTY0004 (23[nilled()],
// K-NilledFunc-4) and an absent focus is XPDY0002 (fn-nilled-30).
func fnNilled(c *Context, a []Object) (Object, error) {
	var n *xmltree.Node
	if len(a) > 0 {
		items := Items(a[0])
		if len(items) == 0 {
			return Sequence{}, nil
		}
		nd, ok := items[0].(*xmltree.Node)
		if !ok || len(items) > 1 {
			return nil, fmt.Errorf("err:XPTY0004: fn:nilled requires a single node")
		}
		n = nd
	} else {
		n = c.Node
		if n == nil {
			if nd, ok := c.CtxItem.(*xmltree.Node); ok {
				n = nd
			} else if c.CtxItem != nil {
				return nil, fmt.Errorf("err:XPTY0004: the context item is not a node")
			} else {
				return nil, fmt.Errorf("err:XPDY0002: the context item is absent")
			}
		}
	}
	if n.Kind != xmltree.KindElement {
		return Sequence{}, nil
	}
	// F&O §5.3: [nilled] is true only for an element that validation accepted
	// as empty under xsi:nil="true". An unvalidated element is xs:untyped and
	// never nilled, so this stays false for every schema-unaware run.
	return NewBool(n.Nilled), nil
}

// fnData implements fn:data($arg as item()*) as xs:anyAtomicType*.
func fnData(c *Context, a []Object) (Object, error) {
	o, err := argOrContextErr(c, a)
	if err != nil {
		return nil, err
	}
	items, err := Atomize(o)
	if err != nil {
		return nil, err
	}
	return FromItems(items), nil
}

// fnBaseURI implements fn:base-uri($arg as node()?) as xs:anyURI?. The node's
// xml:base ancestors apply on top of the context document base URI.
func fnBaseURI(c *Context, a []Object) (Object, error) {
	var node *xmltree.Node
	if len(a) == 0 {
		node = c.Node
		if node == nil && c.CtxItem != nil {
			// (1 to 100)[base-uri()]: a non-node context item is XPTY0004
			// (fn-base-uri-2).
			if nd, ok := c.CtxItem.(*xmltree.Node); ok {
				node = nd
			} else {
				return nil, fmt.Errorf("err:XPTY0004: the context item is not a node")
			}
		}
	} else {
		if numIsEmpty(arg(a, 0)) {
			return Sequence{}, nil
		}
		if ns, ok := ToNodeSet(arg(a, 0)); ok && len(ns) > 0 {
			node = ns[0]
		}
	}
	if node == nil || node.Kind == xmltree.KindNamespace {
		// XDM §6.5.3: the base-uri accessor of a namespace node is the empty
		// sequence (XSLT base-uri-023).
		return Sequence{}, nil
	}
	b := NodeBaseURI(node, c.BaseURI)
	if b == "" {
		return Sequence{}, nil
	}
	return NewAnyURI(b), nil
}

// fnDocumentURI implements fn:document-uri($arg as node()?) as xs:anyURI?. It
// returns the context document URI when the argument is (or resolves to) the
// document node.
func fnDocumentURI(c *Context, a []Object) (Object, error) {
	var n *xmltree.Node
	if len(a) == 0 {
		n = c.Node
	} else {
		items := Items(arg(a, 0))
		if len(items) == 0 {
			return Sequence{}, nil
		}
		nd, ok := items[0].(*xmltree.Node)
		if !ok {
			return Sequence{}, nil
		}
		n = nd
	}
	// document-uri returns () for anything that is not a DOCUMENT node
	// (fn-document-uri-13/14: an element or attribute yields ()).
	if n == nil || n.Kind != xmltree.KindDocument {
		return Sequence{}, nil
	}
	// A constructed temporary tree (an RTF, an @as sequence collector, or a
	// detached fn:copy-of/snapshot clone) is never a "retrieved" resource,
	// even though its Document root carries a Base for fn:base-uri's sake
	// (accessor-007) — that Base must not leak out as a document-uri.
	if n.Ephemeral {
		return Sequence{}, nil
	}
	// Prefer the node's own recorded retrieval location (set by the engine's
	// resolver — internal/xslt's fileResolver, or a stylesheet module's own
	// file) when known; otherwise fall back to the calling expression's
	// static base URI, matching a host resolver (e.g. the QT3 conformance
	// harness's) that has no reason to populate xmltree.Node.Base itself.
	base := n.Base
	if base == "" {
		base = c.BaseURI
	}
	if base == "" {
		return Sequence{}, nil
	}
	return NewAnyURI(base), nil
}
