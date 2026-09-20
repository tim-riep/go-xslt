package xslt

import (
	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// --- text value template (expand-text) --------------------------------------

type tvtText struct {
	a  *avt
	el *xmltree.Node
}

func (*tvtText) instr() {}

func (t *tvtText) exec(eng *engine, r rt, out *xmltree.Node) error {
	s, err := eng.evalAVT(t.a, t.el, r)
	if err != nil {
		return err
	}
	out.Append(xmltree.NewText(s))
	return nil
}

// --- xsl:namespace ----------------------------------------------------------

type nsInstr struct {
	name *avt
	sel  *xpath.Parsed
	body []instruction
	el   *xmltree.Node
}

func (*nsInstr) instr() {}

func (n *nsInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	prefix, err := eng.evalAVT(n.name, n.el, r)
	if err != nil {
		return err
	}
	var uri string
	if n.sel != nil {
		uri, err = eng.evalString(n.sel, n.el, r)
	} else {
		// xsl:namespace's separator is fixed at a single space (§5.8.2).
		uri, err = eng.stringFromBody(n.body, r, " ")
	}
	if err != nil {
		return err
	}
	const xmlNS = "http://www.w3.org/XML/1998/namespace"
	switch {
	case prefix == "xmlns":
		return errAt(n.el, "err:XTDE0920: xsl:namespace name must not be \"xmlns\"")
	case prefix != "" && !isNCName(prefix):
		return errAt(n.el, "err:XTDE0920: xsl:namespace name %q is neither a zero-length string nor an NCName", prefix)
	case prefix == "xml" && uri != xmlNS:
		return errAt(n.el, "err:XTDE0925: prefix \"xml\" must bind to the XML namespace")
	case prefix != "xml" && uri == xmlNS:
		return errAt(n.el, "err:XTDE0925: the XML namespace must bind to prefix \"xml\"")
	case uri == "":
		return errAt(n.el, "err:XTDE0930: xsl:namespace requires a non-empty namespace URI")
	case !validNamespaceURI(uri):
		return errAt(n.el, "err:XTDE0905: xsl:namespace value %q is not a valid namespace URI", uri)
	case prefix == "" && out.Kind == xmltree.KindElement && out.Name.Space == "":
		return errAt(n.el, "err:XTDE0440: cannot declare a default namespace on an element that is itself in no namespace")
	}
	// A conflicting binding for the same prefix already on this element is
	// XTDE0905 — UNLESS the conflict is with the element's own name-derived
	// prefix binding, in which case it depends on whether that binding was
	// only kept because it is structurally required (the enclosing literal
	// element's own exclude-result-prefixes actually asked to drop it, but
	// nothing else supplies the URI its name needs): then it is not a real,
	// user-intended declaration, and namespace fixup applies — the
	// xsl:namespace binding wins and the element name is re-prefixed at
	// serialization (namespace-2614, spec §11.7 "Conflicting Namespace
	// Prefixes"). If it was NOT excluded, it is a genuine, deliberately kept
	// binding and a conflicting xsl:namespace for the same prefix is a real
	// conflict — XTDE0430 (namespace-2618), not a silent rename.
	for i, ns := range out.NS {
		if ns.Name.Local == prefix && ns.Value != uri {
			if ns.Name.Local == out.Name.Prefix && ns.Value == out.Name.Space {
				if n.el.Parent != nil {
					if excl := excludedResultURIs(n.el.Parent); excl[ns.Value] {
						out.NS[i].Value = uri // fixup: replace the self-binding
						return nil
					}
				}
				return errAt(n.el, "err:XTDE0430: namespace node conflict for prefix %q", prefix)
			}
			return errAt(n.el, "err:XTDE0905: conflicting namespace binding for prefix %q", prefix)
		}
	}
	out.NS = append(out.NS, &xmltree.Node{Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: prefix}, Value: uri, Parent: out})
	return nil
}

// validNamespaceURI reports whether s is acceptable as a namespace URI
// produced by xsl:namespace (XTDE0905): not the reserved xmlns namespace URI,
// and not obviously ill-formed as a URI reference (a URI reference has at
// most one '#' fragment separator and cannot start with an empty scheme).
// validNamespaceURI reports whether an xsl:namespace value is acceptable. The
// lexical space of xs:anyURI under XSD 1.1 — the version this engine
// implements — is unrestricted, so only the reserved xmlns namespace itself
// is rejected (namespace-2622); "####" is a legal namespace URI here
// (error-0905b, the XSD_1.1 member of that pair).
func validNamespaceURI(s string) bool {
	return s != "http://www.w3.org/2000/xmlns/"
}

// --- xsl:document (temporary tree) ------------------------------------------

type docInstr struct {
	body []instruction
	// val is the validation/type request — see litElement.val. xsl:document is
	// one of §24.4.2's document-node constructors, so a request on it governs
	// the single element child, not the document node itself.
	val *valRequest
}

func (*docInstr) instr() {}

func (n *docInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	d := &xmltree.Node{Kind: xmltree.KindDocument}
	if err := eng.execSequence(n.body, r, d); err != nil {
		return err
	}
	if len(d.Attrs) > 0 || len(d.NS) > 0 {
		// XTDE0420: the sequence used to construct a DOCUMENT node's content
		// may not contain an attribute or namespace node at all (copy-4601:
		// xsl:document whose body copies @* along with the elements).
		return errAt(nil, "err:XTDE0420: an attribute or namespace node cannot be added to a document node")
	}
	if err := eng.applyValidation(n.val, d); err != nil {
		return err
	}
	if out.KeepDocItems {
		// out is a discrete-sequence collector (an @as-typed body/xsl:sequence
		// result), not element/RTF content being built: xsl:document's whole
		// job is to construct a genuine NEW document node, so it must survive
		// as one — a document node can never be a child of an element anyway,
		// so every OTHER context (building real content, OR a simple-content
		// string collector like xsl:comment/xsl:attribute's stringFromBody,
		// which has no way to represent a nested document-node item at all)
		// still flattens its children directly into out below, unchanged
		// (as-0125: @as="document-node()" over a single xsl:document
		// instruction; xsl-document-0601: the SAME instruction nested inside
		// xsl:comment must flatten instead).
		out.Append(d)
		return nil
	}
	for _, ch := range d.Children {
		out.Append(ch)
	}
	return nil
}

// --- xsl:where-populated -----------------------------------------------------

type wherePopulated struct {
	body []instruction
}

func (*wherePopulated) instr() {}

func (n *wherePopulated) exec(eng *engine, r rt, out *xmltree.Node) error {
	// A discrete-sequence scratch collector: every TOP-LEVEL item the
	// contained sequence constructor produces — including each atomic value
	// from an xsl:sequence — stays its OWN separate child instead of merging
	// into a combined text run the way ordinary element/RTF content
	// construction would. "populated" filtering (itemPopulated below) needs
	// to inspect, and potentially drop, each one INDIVIDUALLY; the merge
	// still happens, correctly, when a SURVIVING run of atomics is
	// re-appended to `out` (appendIfPopulated) — coco-003/coco-018 need
	// exactly one space between two SURVIVING atomics even when an empty one
	// used to sit between them in the original sequence.
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
	if err := eng.execSequence(n.body, r, frag); err != nil {
		return err
	}
	for _, a := range frag.Attrs {
		if a.Value != "" {
			appendAttrItem(out, a.Name, a.Value)
		}
	}
	out.NS = append(out.NS, frag.NS...)
	for _, ch := range frag.Children {
		appendIfPopulated(out, ch)
	}
	return nil
}

// itemPopulated reports whether n survives xsl:where-populated's filtering
// (XSLT 3.0 §14.7's "populated" test, reconstructed from the W3C
// where-populated coco-* test cases since no local spec copy is available):
//   - a text node: populated iff its value is non-empty — an atomic item's
//     lexical string, or literal/computed text (coco-003: a mix of
//     23/”/xs:date(...)/xs:untypedAtomic(”)/0/() /xs:base64Binary(”)
//     keeps only the non-empty ones).
//   - a comment/PI: populated iff its value is non-empty (coco-019/020).
//   - an element or document-node: populated iff it has at least one DIRECT
//     child that itself counts, where an ELEMENT child counts
//     UNCONDITIONALLY (no further recursion into its own attributes/
//     content) and a text/comment/PI child counts only if non-empty. NEVER
//     populated by its own attributes alone (coco-002's copied CATEGORY
//     items — attributes, no children — are still dropped). This is
//     deliberately only ONE level deep, not a full recursive prune: an <a>
//     whose only child collapses to a zero-length xsl:value-of text node is
//     correctly dropped (coco-021), while <a><banana x="5"/></a> and
//     <a><b x=” y='yyy'/></a> both keep <a> — and leave banana/b exactly as
//     constructed, attributes untouched — purely because they ARE elements
//     (coco-005/coco-023): banana's own lack of children, and b's own empty
//     x attribute, are never inspected. Each nested element in turn gets
//     this SAME one-level test if IT is ever the thing being asked about
//     (e.g. as a where-populated top-level item, or recursively via this
//     function on a document-node's element child), so the net effect
//     composes correctly through ordinary tree shapes without ever chasing
//     attributes down an arbitrary number of levels.
//   - a map: populated iff non-empty (coco-014, via xsl:map's RealItem
//     carrier — see RealItem's doc comment on xmltree.Node). An array:
//     populated iff at least one of its MEMBERS is itself a populated
//     sequence, recursively (coco-103 "Emptyness of arrays": [] and [[]]
//     and [()] and [”] and [$empty-element] are all unpopulated — an
//     array's own non-zero member COUNT is not enough, unlike a map's; only
//     [42] survives). Any other function item, and anything else reaching
//     here (a namespace node; the top-level Attrs/NS loops in exec never
//     call this), is always kept.
func itemPopulated(n *xmltree.Node) bool {
	if n.RealItem != nil {
		switch v := n.RealItem.(type) {
		case *xpath.Map:
			return v.Size() > 0
		case *xpath.Array:
			return arrayPopulated(v)
		}
		return true
	}
	switch n.Kind {
	case xmltree.KindText:
		return n.Value != ""
	case xmltree.KindComment, xmltree.KindPI:
		return n.Value != ""
	case xmltree.KindElement, xmltree.KindDocument:
		// An element/document-node item is populated iff it has at least one
		// DIRECT child that itself counts — an element child counts
		// UNCONDITIONALLY, with NO further recursion into ITS OWN
		// attributes/content (coco-005/coco-023: <a><banana x="5"/></a> and
		// <a><b x='' y='yyy'/></a> both keep <a>, and leave banana/b exactly
		// as constructed, attributes untouched, purely because they ARE
		// elements — banana's own lack of children, and b's own empty x
		// attribute, are never inspected); a text/comment/PI child counts
		// only if non-empty, checked at this one level (coco-021's <a> whose
		// only child is a zero-length xsl:value-of text node has NO counting
		// child, so <a> itself is dropped). This deliberately does NOT
		// recurse past one level — an element being tested here is itself
		// always either a xsl:where-populated top-level item OR (by this
		// same rule reapplied one level down) reached only that way, so
		// "at least one child counts" already captures every case the W3C
		// where-populated coco-* tests exercise; a genuinely deeper empty
		// structure (three or more levels of wrapper) is not covered and is
		// accepted as an unwon edge case rather than guessed at further.
		for _, ch := range n.Children {
			switch ch.Kind {
			case xmltree.KindElement:
				return true
			case xmltree.KindText, xmltree.KindComment, xmltree.KindPI:
				if ch.Value != "" {
					return true
				}
			}
		}
		return false
	}
	return true
}

// arrayPopulated reports whether a is populated: at least one of its
// members is itself a populated sequence (coco-103). A member is a general
// xpath.Object sequence, so this recurses through seqPopulated/
// valuePopulated for arbitrarily nested arrays/maps/nodes/atomics.
func arrayPopulated(a *xpath.Array) bool {
	for _, m := range a.Members() {
		if seqPopulated(m) {
			return true
		}
	}
	return false
}

// seqPopulated reports whether ANY item of the sequence o is populated.
func seqPopulated(o xpath.Object) bool {
	for _, it := range xpath.Items(o) {
		if valuePopulated(it) {
			return true
		}
	}
	return false
}

// valuePopulated is itemPopulated's counterpart for a bare xpath.Item that
// is not (yet) wrapped in an xmltree.Node — an array member's own items,
// reached only through arrayPopulated/seqPopulated's recursion.
func valuePopulated(it xpath.Item) bool {
	switch v := it.(type) {
	case *xmltree.Node:
		return itemPopulated(v)
	case *xpath.Map:
		return v.Size() > 0
	case *xpath.Array:
		return arrayPopulated(v)
	case *xpath.Function:
		return true
	default:
		// A bare atomic scalar (string/float64/bool) or *xpath.Atomic:
		// populated iff its string form is non-empty — same rule as a plain
		// text item (coco-103's ['']  member, and z's [42]).
		return xpath.ToString(xpath.FromItems([]xpath.Item{it})) != ""
	}
}

// appendIfPopulated appends ch to out when it survives itemPopulated, merging
// an atomic text item into an immediately preceding SURVIVING atomic text
// item exactly as ordinary sequence-constructor content does
// (appendAtomicItem) — so dropping an empty item from between two populated
// atomic values still joins the survivors with a single space, not a
// doubled one (coco-003/coco-018).
func appendIfPopulated(out *xmltree.Node, ch *xmltree.Node) {
	if !itemPopulated(ch) {
		return
	}
	if ch.Kind == xmltree.KindDocument && !out.KeepDocItems {
		// xsl:where-populated collects its body with KeepDocItems so an
		// xsl:document in it survives as one document-node ITEM to be tested
		// as a whole. Once it has survived, it is written on to the REAL
		// destination — and a document node can never be a child of an
		// element, so there it flattens into its children exactly as
		// xsl:document itself does outside a discrete-sequence collector.
		// Leaving it un-flattened put a document node inside the constructed
		// element, which any enclosing xsl:where-populated then did not count
		// as a child at all (element-0106/0107: the <name> element built
		// around such a document was wrongly judged empty and dropped).
		// The filter applies to the top-level item only: the document survived
		// as a whole, so its content is copied through unfiltered (an empty
		// <e/> inside it is not separately re-tested).
		for _, gc := range ch.Children {
			out.Append(gc)
		}
		return
	}
	if ch.Kind == xmltree.KindText && ch.Atomic && ch.RealItem == nil {
		if n := len(out.Children); n > 0 {
			if last := out.Children[n-1]; last.Kind == xmltree.KindText && last.Atomic {
				last.Value += " " + ch.Value
				last.TypeAnno = 0
				return
			}
		}
	}
	out.Append(ch)
}

// --- xsl:fork -----------------------------------------------------------
//
// xsl:fork (XSLT 3.0 §16) exists mainly to let a STREAMING processor compute
// several results in one pass over the same streamed input. EXECUTION here is
// buffered, and that is spec-defined and has nothing to do with streaming per
// se: "the result of the xsl:fork instruction is the sequence formed by
// concatenating the results of evaluating each of its contained instructions,
// in order... the result can be determined by treating the content as a
// sequence constructor and evaluating it as such." That is exactly what an
// ordinary body already does, for BOTH permitted content shapes — a run of
// xsl:sequence children (where-populated coco-010/011) or a single
// xsl:for-each-group.
//
// What xsl:fork adds is a CONTENT-MODEL constraint (§16.1) and a streamability
// rule (§19.8.4.20). Both now go through streamability.go — strmForkContent
// for the content model (XTSE0010) and strmShape/strmFork for the
// streamability classification (XTSE3430) — instead of the two inconsistent
// ad-hoc checks this used to carry, one of which had no error code at all.
type forkInstr struct {
	body []instruction
	// branches are the compiled xsl:sequence children; group is the compiled
	// single xsl:for-each-group child. Exactly one of the two shapes is
	// populated. They duplicate what body holds, so the §19.8.4.20 rule can
	// classify each branch separately.
	branches []instruction
	group    instruction
	el       *xmltree.Node // for the §19.8.4.20 diagnostic's line/column
}

func (*forkInstr) instr() {}

func (n *forkInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	return eng.execSequence(n.body, r, out)
}

func compileFork(c *compiler, el *xmltree.Node) (instruction, error) {
	if _, _, err := strmForkContent(el); err != nil {
		return nil, err
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	// The branches are picked out of the ALREADY-compiled body rather than
	// compiled a second time: compiling a child twice would run any
	// compile-time side effect twice. strmForkContent has already established
	// that the only instructions here are xsl:sequence, or one
	// xsl:for-each-group.
	n := &forkInstr{body: body, el: el}
	for _, in := range body {
		switch in.(type) {
		case *sequenceInstr:
			n.branches = append(n.branches, in)
		case *fegInstr:
			n.group = in
		}
	}
	return n, nil
}

func init() {
	instrRegistry["namespace"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		name, err := requireAVT(el, "name")
		if err != nil {
			return nil, err
		}
		n := &nsInstr{name: name, el: el}
		if sel, ok := el.AttrLocal("select"); ok {
			p, perr := parseXPathFor(el, sel)
			if perr != nil {
				return nil, errAt(el, "bad select %q: %v", sel, perr)
			}
			n.sel = p
		} else {
			body, berr := c.compileSequence(childNodesForBody(el))
			if berr != nil {
				return nil, berr
			}
			n.body = body
		}
		return n, nil
	}

	instrRegistry["document"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		val, err := compileValidation(el, vkDocument)
		if err != nil {
			return nil, err
		}
		return &docInstr{body: body, val: val}, nil
	}

	instrRegistry["where-populated"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		return &wherePopulated{body: body}, nil
	}

	instrRegistry["fork"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return compileFork(c, el)
	}

	// xsl:fallback content is ignored when the (extension) instruction is
	// supported; on its own it is a no-op.
	instrRegistry["fallback"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return &tcNoop{}, nil
	}
	instrRegistry["on-non-empty"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return compileOnEmpty(c, el, true)
	}
	instrRegistry["on-empty"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return compileOnEmpty(c, el, false)
	}
}

// --- xsl:on-empty / xsl:on-non-empty ------------------------------------------
//
// These are deferred instructions: their content is emitted only after the
// enclosing sequence constructor has been evaluated and its (in)significance
// is known. compileSequence wraps any instruction list containing a marker in
// a condContent, which evaluates the other parts into scratch fragments first.

type onEmptyInstr struct {
	nonEmpty bool // true for xsl:on-non-empty
	sel      *xpath.Parsed
	body     []instruction
	el       *xmltree.Node
}

func (*onEmptyInstr) instr() {}

// exec is the standalone fallback (marker not wrapped by compileSequence —
// should not normally happen): approximate as before.
func (n *onEmptyInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	if n.nonEmpty {
		return n.emit(eng, r, out)
	}
	return nil
}

func (n *onEmptyInstr) emit(eng *engine, r rt, out *xmltree.Node) error {
	if n.sel == nil {
		return eng.execSequence(n.body, r, out)
	}
	v, err := eng.eval(n.sel, n.el, r)
	if err != nil {
		return err
	}
	return emitSequenceValue(v, out)
}

func compileOnEmpty(c *compiler, el *xmltree.Node, nonEmpty bool) (instruction, error) {
	n := &onEmptyInstr{nonEmpty: nonEmpty, el: el}
	if sel, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, sel)
		if err != nil {
			return nil, errAt(el, "bad select %q: %v", sel, err)
		}
		n.sel = p
		return n, nil
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	n.body = body
	return n, nil
}

// condContent evaluates a sequence constructor containing xsl:on-empty /
// xsl:on-non-empty markers: all other parts run first (into scratch
// fragments, preserving order), then markers emit based on whether the
// combined result is empty. Per XSLT 3.0 §5.8.2 zero-length text nodes and
// zero-length string values are insignificant; attributes/namespaces count.
type condContent struct {
	parts []instruction
}

func (*condContent) instr() {}

func (n *condContent) exec(eng *engine, r rt, out *xmltree.Node) error {
	type slot struct {
		marker *onEmptyInstr
		frag   *xmltree.Node
	}
	slots := make([]slot, 0, len(n.parts))
	empty := true
	for _, p := range n.parts {
		// NoAtomicMerge keeps each atomic value its own text node in the
		// scratch fragment. §8.4.2 tests a sibling instruction on the SEQUENCE
		// it yields, where an atomic value casting to a zero-length string is
		// itself "deemed empty" — merging first would leave only the separator
		// spaces behind and make six empty strings look like content
		// (si-on-non-empty-044). transferContent re-joins them on the way out,
		// so the emitted text is unchanged.
		frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true}
		if m, ok := p.(*onEmptyInstr); ok {
			// Evaluate eagerly so the marker sees the variable bindings at its
			// position; its content never counts toward the emptiness test.
			if err := m.emit(eng, r, frag); err != nil {
				return err
			}
			slots = append(slots, slot{marker: m, frag: frag})
			continue
		}
		if err := eng.execInstr(p, r, frag); err != nil {
			return err
		}
		if !fragEmptyContent(frag) {
			empty = false
		}
		slots = append(slots, slot{frag: frag})
	}
	for _, s := range slots {
		if s.marker != nil && s.marker.nonEmpty == empty {
			continue // marker whose condition is not met
		}
		if s.marker == nil && empty {
			// Every non-marker part yielded only items "deemed to be empty"
			// (§8.4.1) — otherwise empty would be false. Emitting them anyway
			// would leak the separator spaces simple-content construction puts
			// BETWEEN them, which is content the sequence never had.
			continue
		}
		transferContent(s.frag, out)
	}
	return nil
}

// transferContent moves a scratch fragment's attributes/namespaces/children to
// out, preserving atomic-value space separation across fragment boundaries.
func transferContent(frag, out *xmltree.Node) {
	for _, a := range frag.Attrs {
		out.SetAttr(a.Name, a.Value)
	}
	out.NS = append(out.NS, frag.NS...)
	for _, ch := range frag.Children {
		if ch.Kind == xmltree.KindText && ch.Atomic {
			appendAtomicText(out, ch.Value)
			continue
		}
		out.Append(ch)
	}
}

// fragEmptyContent reports whether a scratch fragment counts as empty for
// on-empty purposes: no attributes/namespaces, and only zero-length text nodes.
func fragEmptyContent(frag *xmltree.Node) bool {
	if len(frag.Attrs) > 0 || len(frag.NS) > 0 {
		return false
	}
	for _, c := range frag.Children {
		if c.Kind != xmltree.KindText || c.Value != "" {
			return false
		}
	}
	return true
}
