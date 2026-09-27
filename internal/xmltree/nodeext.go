package xmltree

// nodeExt holds every Node field that only a minority of nodes ever set:
// schema-awareness annotations, RTF/sequence-construction bookkeeping, and a
// handful of narrow one-off flags. Keeping them off the hot Node struct and
// behind one lazily-allocated pointer is what shrinks sizeof(Node) from 304
// to 192 bytes (see node.go's doc comment) — a node that never touches any
// of these fields (the overwhelming majority in a schema-unaware, non-RTF
// workload such as fn:json-to-xml's output) pays nothing beyond the one nil
// pointer.
//
// Every field here keeps its original name, comment, and zero-value default
// from when it lived directly on Node; only the access syntax changed, from
// a direct field to the accessor methods below (n.Field() / n.SetField(v)).
type nodeExt struct {
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

	// Unparsed holds the unparsed (NDATA) general entities declared by a
	// PARSED document's DTD, keyed by entity name — the information
	// fn:unparsed-entity-uri / fn:unparsed-entity-public-id report. Set only
	// on Document nodes, and carried over to a document node cloned by
	// xsl:copy / xsl:copy-of / fn:copy-of / fn:snapshot, which per XSLT
	// preserve the source document's unparsed entities.
	Unparsed map[string]UnparsedEntity

	Raw    bool // text node whose value bypasses output escaping (disable-output-escaping)
	Atomic bool // text node created from an atomic value; adjacent atomics get a space separator

	// SynthCtx marks a parentless text node that stands in for an ATOMIC
	// context item (xsl:for-each / xsl:analyze-string / filter over a
	// non-node sequence) purely so "." can address it. It is not a real node:
	// an instruction that genuinely requires a node context item must reject
	// it (error-0510a: xsl:apply-templates with no @select over "1 to 5").
	SynthCtx bool

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
}

// ext lazily allocates n's nodeExt on first write. Every setter below goes
// through this; getters never allocate (nil ext just means every field below
// is at its zero value, which every getter returns directly).
func (n *Node) ext() *nodeExt {
	if n.extPtr == nil {
		n.extPtr = &nodeExt{}
	}
	return n.extPtr
}

func (n *Node) RealItem() interface{} {
	if n.extPtr == nil {
		return nil
	}
	return n.extPtr.RealItem
}
func (n *Node) SetRealItem(v interface{}) { n.ext().RealItem = v }

func (n *Node) SchemaType() *SchemaTypeName {
	if n.extPtr == nil {
		return nil
	}
	return n.extPtr.SchemaType
}
func (n *Node) SetSchemaType(v *SchemaTypeName) { n.ext().SchemaType = v }

func (n *Node) ListItemType() *SchemaTypeName {
	if n.extPtr == nil {
		return nil
	}
	return n.extPtr.ListItemType
}
func (n *Node) SetListItemType(v *SchemaTypeName) { n.ext().ListItemType = v }

func (n *Node) ValueType() *SchemaTypeName {
	if n.extPtr == nil {
		return nil
	}
	return n.extPtr.ValueType
}
func (n *Node) SetValueType(v *SchemaTypeName) { n.ext().ValueType = v }

func (n *Node) Base() string {
	if n.extPtr == nil {
		return ""
	}
	return n.extPtr.Base
}
func (n *Node) SetBase(v string) { n.ext().Base = v }

func (n *Node) EntityBase() string {
	if n.extPtr == nil {
		return ""
	}
	return n.extPtr.EntityBase
}
func (n *Node) SetEntityBase(v string) { n.ext().EntityBase = v }

func (n *Node) SeqIndex() int {
	if n.extPtr == nil {
		return 0
	}
	return n.extPtr.SeqIndex
}
func (n *Node) SetSeqIndex(v int) { n.ext().SeqIndex = v }

func (n *Node) Unparsed() map[string]UnparsedEntity {
	if n.extPtr == nil {
		return nil
	}
	return n.extPtr.Unparsed
}
func (n *Node) SetUnparsed(v map[string]UnparsedEntity) { n.ext().Unparsed = v }

func (n *Node) Raw() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.Raw
}
func (n *Node) SetRaw(v bool) { n.ext().Raw = v }

func (n *Node) Atomic() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.Atomic
}
func (n *Node) SetAtomic(v bool) { n.ext().Atomic = v }

func (n *Node) SynthCtx() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.SynthCtx
}
func (n *Node) SetSynthCtx(v bool) { n.ext().SynthCtx = v }

func (n *Node) NoAtomicMerge() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.NoAtomicMerge
}
func (n *Node) SetNoAtomicMerge(v bool) { n.ext().NoAtomicMerge = v }

func (n *Node) KeepDocItems() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.KeepDocItems
}
func (n *Node) SetKeepDocItems(v bool) { n.ext().KeepDocItems = v }

func (n *Node) NSBarrier() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.NSBarrier
}
func (n *Node) SetNSBarrier(v bool) { n.ext().NSBarrier = v }

func (n *Node) IDKind() uint8 {
	if n.extPtr == nil {
		return 0
	}
	return n.extPtr.IDKind
}
func (n *Node) SetIDKind(v uint8) { n.ext().IDKind = v }

func (n *Node) TypeAnno() int32 {
	if n.extPtr == nil {
		return 0
	}
	return n.extPtr.TypeAnno
}
func (n *Node) SetTypeAnno(v int32) { n.ext().TypeAnno = v }

func (n *Node) Nilled() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.Nilled
}
func (n *Node) SetNilled(v bool) { n.ext().Nilled = v }

func (n *Node) ListTyped() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.ListTyped
}
func (n *Node) SetListTyped(v bool) { n.ext().ListTyped = v }

func (n *Node) Ephemeral() bool {
	if n.extPtr == nil {
		return false
	}
	return n.extPtr.Ephemeral
}
func (n *Node) SetEphemeral(v bool) { n.ext().Ephemeral = v }
