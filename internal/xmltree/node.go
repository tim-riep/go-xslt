// Package xmltree provides the XML/XDM node tree that the XPath evaluator and
// XSLT engine operate on. It is a self-contained model with its own parser
// (built on encoding/xml) and serializer.
package xmltree

import "strings"

// Kind enumerates the XDM node kinds we model.
type Kind uint8

const (
	KindDocument Kind = iota
	KindElement
	KindAttribute
	KindText
	KindComment
	KindPI
	KindNamespace
)

// XMLNS is the namespace URI for XML namespace declarations.
const XMLNS = "http://www.w3.org/2000/xmlns/"

// Name is an expanded-QName: namespace URI + local part, plus the original
// prefix where one was present (prefix is advisory, used for serialization).
type Name struct {
	Local  string
	Space  string
	Prefix string
}

// SchemaTypeName identifies a schema type definition by its expanded QName —
// see Node.SchemaType for why this is a plain, import-free struct rather than
// a reference into a validator's own component model.
type SchemaTypeName struct {
	Namespace, Local string
	// Complex is true for a complex type definition, false for a simple
	// (atomic/list/union) one. An element or attribute can only ever be
	// annotated with a complex type if it is an element (attributes are
	// always simply typed), but the flag is carried here rather than
	// inferred from the node kind so a type name can be resolved and
	// compared on its own, before any node is annotated with it at all
	// (@as="element(x, myns:Foo)" resolves myns:Foo at compile time).
	Complex bool
}

// Node is a single node in the tree. Not all fields apply to all kinds:
//   - Document/Element: Children (and Attrs/NS for Element)
//   - Attribute/Namespace: Value (Name holds the name; for NS, Local is prefix)
//   - Text/Comment: Value
//   - PI: Name.Local is the target, Value is the data
//
// Every field a JSON/XSLT-heavy workload's node count actually dominates by
// (Kind/Name/Value/Parent/Children/Attrs/NS) stays a direct struct field.
// Everything schema-awareness/RTF-bookkeeping/streaming-only (RealItem,
// SchemaType, ListItemType, ValueType, Base, EntityBase, SeqIndex, Unparsed,
// and every bool/uint8/int32 flag below) moved into *ext, a lazily-allocated
// nodeExt — zero for every node that never needs one of these, which is the
// overwhelming majority in ordinary (non-schema-aware, non-RTF) use. This
// dropped sizeof(Node) from 304 (Go's 320-byte size class) to 192 (its own
// exact size class, zero rounding waste), a 40% per-node reduction, with
// every field's stored VALUE and every caller-visible zero-value default
// unchanged — see the accessor methods below (nodeext.go) for the read/write
// surface every previous direct field access now goes through.
type Node struct {
	// The four small integers are declared first and together: Kind (1 byte)
	// followed by three int32s packs into 16 bytes, where int-typed Kind/Line/
	// Col/order cost 32 — what takes the whole struct from 192 to 176 bytes,
	// the next Go allocator size class down. int32 holds 2 billion lines or
	// nodes per document, far past what this engine can hold in memory.
	Kind      Kind
	order     int32 // document order, assigned during parse/build
	Line, Col int32

	// The fields almost every visit of a node reads — its kind, document
	// order (order, tree), the side-struct pointer behind SynthCtx/RealItem/
	// NSDecls, its parent and name — come first, together: a node is 152
	// bytes, more than two cache lines, and a walk over a large tree (or a
	// sorted node list, which visits it in no memory order at all) pays a
	// miss for every line of a node it touches.

	// tree identifies the tree the node was numbered in: a creation-ordered
	// stamp every node of one parse/AssignOrder walk shares (0 = never
	// numbered). Document order across trees is implementation-dependent
	// but must be stable; ordering whole trees by creation gives that
	// (evaluate-002: $settings1 | $settings2 sorts the earlier-built tree
	// first), where comparing the per-tree order indices interleaved them.
	tree uint64

	// extPtr holds every rarely-used field (see nodeext.go) behind one
	// lazily allocated pointer — nil for a node that never sets any of them.
	// Accessed through the ext()/Field()/SetField() methods in nodeext.go,
	// never directly (kept unexported so nothing outside this package can).
	extPtr *nodeExt

	Parent   *Node
	Name     Name
	Value    string
	Children []*Node
	Attrs    []*Node // attribute nodes
}

// DTD attribute-type kinds for Node.IDKind.
const (
	IDKindNone   uint8 = 0
	IDKindID     uint8 = 1
	IDKindIDREF  uint8 = 2
	IDKindIDREFS uint8 = 3
)

// normalizeIDAttr applies the normalization a tokenized attribute value gets
// beyond CDATA's (XML 1.0 §3.3.3): leading and trailing spaces dropped, every
// run of spaces collapsed to one. It applies to xml:id, which XDM 3.1 §6.3.3
// gives xml:id processing (fn-doc-37), and to attributes the DTD declares ID,
// IDREF or IDREFS.
func normalizeIDAttr(an *Node) {
	if an.IDKind() == IDKindNone && (an.Name.Local != "id" || an.Name.Space != xmlNS) {
		return
	}
	if !strings.Contains(an.Value, " ") {
		return
	}
	an.Value = strings.Join(strings.FieldsFunc(an.Value, func(r rune) bool { return r == ' ' }), " ")
}

// NewElement constructs a detached element node.
func NewElement(name Name) *Node { return &Node{Kind: KindElement, Name: name} }

// NewText constructs a detached text node.
func NewText(value string) *Node { return &Node{Kind: KindText, Value: value} }

// NewRawText constructs a text node serialized without character escaping
// (used for disable-output-escaping="yes").
func NewRawText(value string) *Node {
	n := &Node{Kind: KindText, Value: value}
	n.SetRaw(true)
	return n
}

// NewAttribute constructs a detached attribute node.
func NewAttribute(name Name, value string) *Node {
	return &Node{Kind: KindAttribute, Name: name, Value: value}
}

// NodeAlloc hands out zeroed nodes carved from shared slabs, for code that
// builds a whole tree at once (a deep copy) and would otherwise allocate it
// one node at a time: a node from New is exactly a new(Node), but a slab is
// one allocation for many nodes. A slab lives as long as any node carved
// from it, so the slabs are kept small — the first is sized by Reserve, every
// later one holds nodeAllocSlab nodes — which bounds what one retained node
// can keep alive.
type NodeAlloc struct {
	slab []Node
	next int // size of the next slab (0: nodeAllocSlab)
}

// nodeAllocSlab is the size of a NodeAlloc slab after the first: a couple of
// pages' worth, few enough that retention stays bounded.
const nodeAllocSlab = 64

// Reserve sizes the first slab for n nodes (at most nodeAllocSlab), so a copy
// of a few nodes takes no more than it needs. Call it before the first New.
func (a *NodeAlloc) Reserve(n int) {
	a.next = min(max(n, 1), nodeAllocSlab)
}

// New returns a zeroed node.
func (a *NodeAlloc) New() *Node {
	if len(a.slab) == 0 {
		n := a.next
		if n <= 0 {
			n = nodeAllocSlab
		}
		a.slab = make([]Node, n)
		a.next = nodeAllocSlab
	}
	n := &a.slab[0]
	a.slab = a.slab[1:]
	return n
}

// TreeAlloc carves one tree whose size is known before it is built from
// two slabs, its nodes and the pointer slices of their children and
// attributes: one allocation each instead of one per node and one per
// slice. It is for a copy that is used and discarded as a whole, such as the
// detached tree a schema assertion is evaluated against, so unlike NodeAlloc
// it keeps no slab small — every node of such a tree lives exactly as long
// as the tree. A count that turns out too small only costs an ordinary
// allocation for what is over, never a wrong tree.
type TreeAlloc struct {
	nodes []Node
	ptrs  []*Node
}

// Init sizes the slabs for nodes nodes and ptrs pointer slots in all.
func (a *TreeAlloc) Init(nodes, ptrs int) {
	a.nodes = make([]Node, nodes)
	a.ptrs = make([]*Node, ptrs)
}

// Node returns a zeroed node.
func (a *TreeAlloc) Node() *Node {
	if len(a.nodes) == 0 {
		return new(Node)
	}
	n := &a.nodes[0]
	a.nodes = a.nodes[1:]
	return n
}

// Ptrs returns an empty slice with room for exactly n pointers (an append
// past n reallocates, as it would from a make of that size).
func (a *TreeAlloc) Ptrs(n int) []*Node {
	if n == 0 {
		return nil
	}
	if len(a.ptrs) < n {
		return make([]*Node, 0, n)
	}
	s := a.ptrs[:0:n]
	a.ptrs = a.ptrs[n:]
	return s
}

// SetAttrFrom is SetAttr with a new attribute node taken from a.
func (n *Node) SetAttrFrom(a *NodeAlloc, name Name, value string) *Node {
	for _, at := range n.Attrs {
		if at.Name.Local == name.Local && at.Name.Space == name.Space {
			at.Value = value
			return at
		}
	}
	at := a.New()
	at.Kind, at.Name, at.Value = KindAttribute, name, value
	at.Parent = n
	n.Attrs = append(n.Attrs, at)
	return at
}

// Append adds child to n, setting the parent link.
func (n *Node) Append(child *Node) {
	child.Parent = n
	n.Children = append(n.Children, child)
}

// SetAttr sets (or replaces) an attribute by expanded name, returning the
// attribute node it set. A COPY routine rebuilding an element's attributes
// needs that node back to carry the source attribute's type information over
// with CopyTypeInfo; callers that only set a value ignore it.
func (n *Node) SetAttr(name Name, value string) *Node {
	for _, a := range n.Attrs {
		if a.Name.Local == name.Local && a.Name.Space == name.Space {
			a.Value = value
			return a
		}
	}
	a := NewAttribute(name, value)
	a.Parent = n
	n.Attrs = append(n.Attrs, a)
	return a
}

// CopyTypeInfo carries the TYPE-MODEL fields of a node — its XDM type
// annotation and its DTD-derived ID kind — from src to dst.
//
// Every node-rebuild path in the engine (cloneNode, deepCopyInto, snapshotNode,
// and the attribute rebuilds those do through SetAttr) constructs fresh nodes
// field by field, so a field nobody thought to list is silently dropped by all
// of them at once. TypeAnno and IDKind were exactly that: fn:id over an
// xsl:copy-of'd subtree could not see the DTD ID attributes the original
// carried. Rebuild sites call this instead of enumerating the fields, so the
// next field of this kind is added in one place.
//
// It is deliberately NOT the spec's per-instruction type-annotation RETENTION
// rules (XSLT 3.0 §5.7.1: xsl:copy of an element resets to xs:anyType even
// under validation="preserve", while xsl:copy-of keeps annotations verbatim).
// Those need real annotations to act on, and a caller that must strip clears
// the field after copying.
func CopyTypeInfo(dst, src *Node) {
	if dst == nil || src == nil {
		return
	}
	// The overwhelming majority of nodes are schema-unaware (src.extPtr ==
	// nil), so skip allocating dst's ext entirely in that case rather than
	// unconditionally round-tripping seven zero values through it — the
	// exact per-copy allocation this Node layout exists to avoid. Falling
	// through to per-field zeroing (not clearing dst.extPtr wholesale) keeps
	// this function's effect scoped to only its own seven fields, exactly as
	// before.
	if src.extPtr == nil {
		if dst.extPtr != nil {
			dst.extPtr.TypeAnno = 0
			dst.extPtr.IDKind = 0
			dst.extPtr.SchemaType = nil
			dst.extPtr.Nilled = false
			dst.extPtr.ListTyped = false
			dst.extPtr.ListItemType = nil
			dst.extPtr.ValueType = nil
		}
		return
	}
	dst.ext().TypeAnno = src.extPtr.TypeAnno
	dst.ext().IDKind = src.extPtr.IDKind
	dst.ext().SchemaType = src.extPtr.SchemaType
	dst.ext().Nilled = src.extPtr.Nilled
	dst.ext().ListTyped = src.extPtr.ListTyped
	dst.ext().ListItemType = src.extPtr.ListItemType
	dst.ext().ValueType = src.extPtr.ValueType
}

// Attr returns the value of the attribute with the given namespace+local, and
// whether it exists.
func (n *Node) Attr(space, local string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name.Local == local && a.Name.Space == space {
			return a.Value, true
		}
	}
	return "", false
}

// AttrLocal returns a no-namespace attribute value (the common case for XSLT
// instruction attributes like select/match/test).
func (n *Node) AttrLocal(local string) (string, bool) { return n.Attr("", local) }

// Order returns the document-order index of the node.
func (n *Node) Order() int { return int(n.order) }

// Tree returns the creation-ordered stamp of the tree the node was numbered
// in (0 when it never was) — see the tree field.
func (n *Node) Tree() uint64 { return n.tree }

// NewInheritedNamespace creates a namespace node for a binding that is in
// scope on element n but declared on an ancestor (the XDM namespace axis has
// one node per element and in-scope binding). It sorts with n in document
// order — after any preceding element's namespace nodes and before n's
// children — so that (//namespace::*)[1] is the document element's.
func (n *Node) NewInheritedNamespace(prefix, uri string) *Node {
	return &Node{Kind: KindNamespace, Name: Name{Local: prefix}, Value: uri, Parent: n, order: n.order}
}

// Root walks up to the document/root node.
func (n *Node) Root() *Node {
	cur := n
	for cur.Parent != nil {
		cur = cur.Parent
	}
	return cur
}

// IsElement reports whether n is an element node.
func (n *Node) IsElement() bool { return n.Kind == KindElement }

// LookupPrefix returns the namespace URI in scope for prefix at this node,
// walking up the ancestor chain. The "xml" prefix is always bound.
func (n *Node) LookupPrefix(prefix string) (string, bool) {
	if prefix == "xml" {
		return "http://www.w3.org/XML/1998/namespace", true
	}
	for cur := n; cur != nil; cur = cur.Parent {
		for _, ns := range cur.NSDecls() {
			if ns.Name.Local == prefix {
				return ns.Value, true
			}
		}
		if cur.NSBarrier() {
			break // inherit-namespaces="no": stop before the parent
		}
	}
	return "", false
}

// InScopeNamespaces returns all prefix->uri bindings in scope at this node.
func (n *Node) InScopeNamespaces() map[string]string {
	out := map[string]string{}
	for cur := n; cur != nil; cur = cur.Parent {
		for _, ns := range cur.NSDecls() {
			if _, seen := out[ns.Name.Local]; !seen {
				out[ns.Name.Local] = ns.Value
			}
		}
		if cur.NSBarrier() {
			break // inherit-namespaces="no"
		}
	}
	return out
}

// StringValue returns the XPath string-value of the node.
func (n *Node) StringValue() string {
	if nd, ok := n.RealItem().(*Node); ok {
		return nd.StringValue() // a node carried by reference (see RealItem)
	}
	switch n.Kind {
	case KindText, KindComment, KindAttribute, KindNamespace:
		return n.Value
	case KindPI:
		return n.Value
	case KindElement, KindDocument:
		if len(n.Children) == 1 {
			// One text child (the common leaf element): its value is the
			// string value, as appendText would copy it.
			if c := n.Children[0]; c.Kind == KindText && c.RealItem() == nil {
				return c.Value
			}
		}
		var b []byte
		b = appendText(b, n)
		return string(b)
	}
	return ""
}

func appendText(b []byte, n *Node) []byte {
	for _, c := range n.Children {
		if nd, ok := c.RealItem().(*Node); ok {
			b = append(b, nd.StringValue()...)
			continue
		}
		switch c.Kind {
		case KindText:
			b = append(b, c.Value...)
		case KindElement:
			b = appendText(b, c)
		}
	}
	return b
}
