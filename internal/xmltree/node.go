// Package xmltree provides the XML/XDM node tree that the XPath evaluator and
// XSLT engine operate on. It is a self-contained model with its own parser
// (built on encoding/xml) and serializer.
package xmltree

// Kind enumerates the XDM node kinds we model.
type Kind int

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
type Node struct {
	Kind     Kind
	Name     Name
	Value    string
	Parent   *Node
	Children []*Node
	Attrs    []*Node // attribute nodes
	NS       []*Node // namespace nodes declared on this element

	Raw    bool // text node whose value bypasses output escaping (disable-output-escaping)
	Atomic bool // text node created from an atomic value; adjacent atomics get a space separator

	// SynthCtx marks a parentless text node that stands in for an ATOMIC
	// context item (xsl:for-each / xsl:analyze-string / filter over a
	// non-node sequence) purely so "." can address it. It is not a real node:
	// an instruction that genuinely requires a node context item must reject
	// it (error-0510a: xsl:apply-templates with no @select over "1 to 5").
	SynthCtx bool

	// RealItem carries the ACTUAL XPath item (as an untyped interface{} —
	// this package cannot import internal/xpath's Item type, which is itself
	// an alias for any) that a synthetic non-node context wrapper (SynthCtx,
	// or the analogous unmarked text-node wrappers xsl:for-each-group /
	// xsl:iterate / xsl:perform-sort / xsl:evaluate / xsl:apply-templates
	// build for a non-node sequence item) stands in for. It is set ONLY when
	// the item cannot round-trip through Value+TypeAnno at all — a map,
	// array, or function item has no meaningful string value (ToString
	// yields "" for these), so without RealItem a wrapper node would
	// silently lose the item's entire identity: "." would see an empty
	// string instead of the real map/array/function, breaking dynamic calls
	// (".('key')"), "instance of", and higher-order use generally. Ordinary
	// atomic items (numbers, strings, dates, ...) deliberately leave this nil
	// and keep using the pre-existing Value+TypeAnno round-trip, which
	// already works correctly — this field's whole purpose is narrowly
	// filling the gap that round-trip cannot cover.
	//
	// Consumers that build an xpath.Context for such a wrapper node (or for
	// pattern-matching against it) MUST set Context.CtxItem from this field
	// (a direct assignment: both are interface{}/xpath.Item) so "." resolves
	// to the real item instead of the wrapper node itself — see eng.eval and
	// the xpath.Context{Node: n, ...} construction sites in internal/xslt,
	// plus stepMatches'/matchExprPattern's per-candidate sub-contexts in
	// internal/xpath/pattern.go.
	RealItem interface{}

	// NoAtomicMerge marks a document-node root built to extract a discrete
	// SEQUENCE of items (an @as-typed xsl:variable/xsl:param/xsl:template
	// body, or an xsl:function result) rather than result-tree-fragment TEXT
	// CONTENT: adjacent atomic-valued sequence-constructor items (e.g. several
	// xsl:sequence instructions in a loop) must stay separate items, each
	// independently typed/atomizable, instead of collapsing into one
	// space-joined text node the way RTF/element content construction does
	// (sequence-0115: a for-each contributing one xs:integer per iteration via
	// xsl:sequence must yield 10 items, not one merged string).
	NoAtomicMerge bool

	// KeepDocItems marks a document-node root (a scratch construction
	// collector) whose own DOCUMENT-NODE children must stay distinct
	// document-node items instead of flattening into their contents the way
	// deepCopyInto otherwise must for real element/RTF content (a document
	// node can never legally be a child of a constructed element). This is
	// narrower than NoAtomicMerge: an xsl:function body always needs it
	// (a returned document-node() item — e.g. xsl:copy-of over document(),
	// as-0136 — must stay one document-node() item, not its flattened
	// children) but, unlike NoAtomicMerge, must NOT also suppress the
	// adjacent-atomics-get-a-space-separator merge (seqtor-031/033: a
	// purely-atomic, non-node function body still collapses through
	// frag.StringValue(), which depends on that merge already having run).
	KeepDocItems bool

	// NSBarrier marks an ELEMENT that does not inherit namespace declarations
	// from its ancestors: every in-scope-namespace walk (LookupPrefix,
	// InScopeNamespaces, the XPath namespace axis, the serializer) stops after
	// this element's OWN NS list and never consults its parent.
	//
	// It is set only by the XSLT engine, on the element children of a node
	// constructed with inherit-namespaces="no" (xsl:copy / xsl:element), which
	// is exactly what that attribute means: "do not copy MY namespace nodes to
	// the elements in my content" (copy-0613..0627).
	//
	// INVARIANT: every ancestor-walking namespace lookup must honour it —
	// adding a new one without the check silently reintroduces inheritance.
	NSBarrier bool

	// IDKind marks an attribute node whose DTD ATTLIST type is ID/IDREF/IDREFS
	// (0 = none), so fn:id and fn:idref can find DTD-typed id attributes.
	IDKind uint8

	// TypeAnno carries an XDM type annotation (an xpath.AtomType value) on
	// specially-built trees — today only the detached clones XSD assertions
	// evaluate against, where already-validated simple-typed elements atomize
	// to their TYPED value instead of xs:untypedAtomic. Zero means untyped.
	TypeAnno int32

	// SchemaType identifies the EXACT schema type a validated node's
	// annotation names, orthogonal to TypeAnno's "what this atomizes as": a
	// user-defined restriction of xs:string atomizes through TypeAnno's
	// xs:string handling, but "is this specifically myns:ZipCodeType, or one
	// of its subtypes" needs the real declared identity, which no built-in
	// AtomType enum value can carry. nil means unannotated (or annotated only
	// with a built-in type TypeAnno already fully represents).
	//
	// Deliberately a plain, import-free struct rather than a pointer into any
	// validator's component graph: xmltree sits below both internal/xpath and
	// internal/xsd (xmltree <- xpath <- xsd) and cannot import either without
	// a cycle, so this is the shared value both the writer (internal/xsd's
	// node-validation bridge) and the reader (internal/xpath's instance-of /
	// element(name,type) name-identity matching) can hold without either
	// package depending on the other. DERIVATION-aware matching (does this
	// node's type derive from a NAMED type, not just equal it) needs more
	// than identity comparison and goes through an injected hook a
	// schema-aware caller supplies (xpath.Context.SchemaTypes), not this
	// field directly.
	SchemaType *SchemaTypeName

	// Nilled is the XDM [nilled] property of an ELEMENT node: true when
	// validation accepted the element as empty because it carried
	// xsi:nil="true" against a nillable declaration. It is part of a node's
	// type identity, not of its content — a nilled element keeps its type
	// annotation, it simply has no value — which is why fn:nilled, the "?" of
	// element(N, T?) and schema-element(N)'s own nillable rule all consult it
	// separately from SchemaType. Always false on an unvalidated node, so a
	// schema-unaware run never observes it.
	Nilled bool

	// ListTyped marks an element or attribute whose governing simple type has
	// the LIST variety. Its typed value is then a SEQUENCE — one item per
	// whitespace-separated token of the string value, each of the ITEM type —
	// and TypeAnno holds that ITEM type's built-in primitive rather than the
	// list type's (a list has no primitive of its own). Storing the item type
	// plus this flag is what lets a list's typed value be COMPUTED on demand
	// instead of stored, which one TypeAnno field could never do.
	ListTyped bool

	// ListItemType names the ITEM type of a ListTyped node's governing list,
	// when that item type is a NAMED simple type. TypeAnno only records the
	// item's built-in primitive, which cannot answer `data($a) instance of
	// my:itemType*` — the item type's own identity is a separate fact, exactly
	// as SchemaType is for a non-list node (import-schema-029/030: a list of
	// the XSLT schema's own xsl:QName, a restriction of xs:Name, where the
	// primitive alone says only "xs:Name"). Nil for an unvalidated node, for a
	// non-list one, and for a list whose item type is anonymous.
	ListItemType *SchemaTypeName

	// ValueType names the type a validated node's TYPED VALUE is an instance
	// of, when that is not the node's own SchemaType. The two genuinely differ
	// for a UNION: XDM 3.1 §3.3.1.1/§3.3.1.2 give the node's [type-name] as
	// the union itself, while its typed value is an instance of whichever
	// MEMBER type actually accepted the lexical form — two easily-conflated
	// facts about one node.
	//
	// TypeAnno already records that member's built-in PRIMITIVE (see xsd's
	// typeAnnoForNode); this records its NAME, which the primitive alone can
	// never supply. Without it, `data($d) instance of my:MemberType` is false
	// for a node the schema validated precisely as that member
	// (validation-0202: GEDCOM's Date has the complex type DateType, whose
	// simple content is the union GeneralDate, and the stylesheet dispatches
	// on `data(.) instance of StandardDate` — the accepting member).
	//
	// Nil whenever the typed value's type is simply SchemaType, which is every
	// non-union node.
	ValueType *SchemaTypeName

	// Base is an intrinsic base-uri override for a Document/Element node,
	// never serialized as an attribute. It is set in two situations: (1) the
	// root of a temporary tree (an xsl:variable/xsl:param/xsl:function
	// result-tree-fragment) records the base URI established by the
	// constructing instruction (its own xml:base, or an ancestor's, in the
	// STYLESHEET source); (2) a node produced by xsl:copy/xsl:copy-of (or the
	// fn:copy-of/fn:snapshot XPath functions) retains its ORIGINAL node's
	// base-uri, per XSLT's node-copy semantics. Case (2) is CLEARED by the
	// caller when the copy is immediately embedded as a child of further
	// newly constructed complex content (an LRE, xsl:element, or another
	// RTF) — in that case the copy's base-uri instead derives normally from
	// its new position, exactly like any other node. Consulted by
	// xpath.NodeBaseURI, which stops climbing ancestors the moment it finds
	// a node with Base set.
	Base string

	// EntityBase is the location of the EXTERNAL PARSED ENTITY a node was
	// spliced in from. Unlike Base (which is an already-computed base URI and
	// therefore final), it is the base against which the node's OWN xml:base
	// attribute still has to be resolved — XML Base §3: an xml:base is
	// relative to the base URI of the entity containing the element, not to
	// the document entity. base-uri-051's entity content declares
	// <item xml:base="dir2/data.xml"> and <para xml:base="dir5/data.xml">,
	// both of which must resolve against the entity's own dir/data.xml.
	EntityBase string

	// SeqIndex orders a standalone ATTRIBUTE or NAMESPACE node inside a
	// discrete-sequence collector. Such an item is an ordinary member of the
	// sequence a body constructs, but it cannot live in Children (the tree
	// keeps attributes and namespaces out-of-band), so its construction
	// position would otherwise be lost and it would surface before every
	// child. SeqIndex records how many Children the collector already held
	// when the item was appended, which is exactly what is needed to
	// reconstruct the original order (see fragAsSequence in internal/xslt).
	// Zero — the value every node that predates this mechanism has — keeps the
	// old "before all children" placement.
	SeqIndex int

	// Ephemeral marks a Document node built as a constructed temporary tree
	// (an xsl:variable/xsl:param/xsl:function RTF, an @as-typed sequence
	// collector, or a detached fn:copy-of/snapshot/xsl:copy-of clone) rather
	// than a document actually retrieved from a resource. Such a node's Base
	// (set for fn:base-uri's sake — see above) must NOT be reported by
	// fn:document-uri, which is empty for anything not genuinely retrieved
	// (accessor-007). Deliberately opt-out (default false) rather than an
	// opt-in "was retrieved" flag: a host's OWN ResourceResolver (e.g. the
	// QT3 conformance harness's) has no reason to know about this field, so
	// document nodes it hands back must still read as retrieved by default.
	Ephemeral bool

	// Unparsed holds the unparsed (NDATA) general entities declared by a
	// PARSED document's DTD, keyed by entity name — the information
	// fn:unparsed-entity-uri / fn:unparsed-entity-public-id report. Set only
	// on Document nodes, and carried over to a document node cloned by
	// xsl:copy / xsl:copy-of / fn:copy-of / fn:snapshot, which per XSLT
	// preserve the source document's unparsed entities.
	Unparsed map[string]UnparsedEntity

	Line, Col int
	order     int // document order, assigned during parse/build
	// tree identifies the tree the node was numbered in: a creation-ordered
	// stamp every node of one parse/AssignOrder walk shares (0 = never
	// numbered). Document order across trees is implementation-dependent
	// but must be stable; ordering whole trees by creation gives that
	// (evaluate-002: $settings1 | $settings2 sorts the earlier-built tree
	// first), where comparing the per-tree order indices interleaved them.
	tree uint64
}

// DTD attribute-type kinds for Node.IDKind.
const (
	IDKindNone   uint8 = 0
	IDKindID     uint8 = 1
	IDKindIDREF  uint8 = 2
	IDKindIDREFS uint8 = 3
)

// NewElement constructs a detached element node.
func NewElement(name Name) *Node { return &Node{Kind: KindElement, Name: name} }

// NewText constructs a detached text node.
func NewText(value string) *Node { return &Node{Kind: KindText, Value: value} }

// NewRawText constructs a text node serialized without character escaping
// (used for disable-output-escaping="yes").
func NewRawText(value string) *Node { return &Node{Kind: KindText, Value: value, Raw: true} }

// NewAttribute constructs a detached attribute node.
func NewAttribute(name Name, value string) *Node {
	return &Node{Kind: KindAttribute, Name: name, Value: value}
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
	dst.TypeAnno = src.TypeAnno
	dst.IDKind = src.IDKind
	dst.SchemaType = src.SchemaType
	dst.Nilled = src.Nilled
	dst.ListTyped = src.ListTyped
	dst.ListItemType = src.ListItemType
	dst.ValueType = src.ValueType
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
func (n *Node) Order() int { return n.order }

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
		for _, ns := range cur.NS {
			if ns.Name.Local == prefix {
				return ns.Value, true
			}
		}
		if cur.NSBarrier {
			break // inherit-namespaces="no": stop before the parent
		}
	}
	return "", false
}

// InScopeNamespaces returns all prefix->uri bindings in scope at this node.
func (n *Node) InScopeNamespaces() map[string]string {
	out := map[string]string{}
	for cur := n; cur != nil; cur = cur.Parent {
		for _, ns := range cur.NS {
			if _, seen := out[ns.Name.Local]; !seen {
				out[ns.Name.Local] = ns.Value
			}
		}
		if cur.NSBarrier {
			break // inherit-namespaces="no"
		}
	}
	return out
}

// StringValue returns the XPath string-value of the node.
func (n *Node) StringValue() string {
	if nd, ok := n.RealItem.(*Node); ok {
		return nd.StringValue() // a node carried by reference (see RealItem)
	}
	switch n.Kind {
	case KindText, KindComment, KindAttribute, KindNamespace:
		return n.Value
	case KindPI:
		return n.Value
	case KindElement, KindDocument:
		var b []byte
		b = appendText(b, n)
		return string(b)
	}
	return ""
}

func appendText(b []byte, n *Node) []byte {
	for _, c := range n.Children {
		if nd, ok := c.RealItem.(*Node); ok {
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
