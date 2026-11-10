package xpath

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Object is an XDM value: a Sequence of Items — nodes, typed atomic values
// (*Atomic, carrying their XSD type: xs:integer as arbitrary precision,
// xs:decimal as a rational, dates, durations, QNames, …), maps, arrays and
// functions. A NodeSet is the node-only sequence. Plain Go string, float64
// and bool also occur, as the untyped results of internal fast paths and the
// XPath 1.0 conversion functions; Items normalizes all of them.
type Object interface{}

// NodeSet is an ordered, duplicate-free (by the time it is returned) set of
// nodes. Internally it may contain duplicates until normalized.
type NodeSet []*xmltree.Node

// --- conversions (XPath 1.0 rules) -----------------------------------------

// ToBool applies the boolean()/effective-boolean-value conversion.
// EffectiveBoolErr is the SPEC effective boolean value: unlike the lenient
// ToBool (kept for internal/1.0-style call sites), operands with no defined
// EBV — a multi-item sequence not starting with a node, or a singleton
// QName/date/duration/binary/map/array/function — raise err:FORG0006
// (boolean-004../K-QuantExprWithout-28).
func EffectiveBoolErr(o Object) (bool, error) {
	n := itemCount(o)
	if n == 0 {
		return false, nil
	}
	first := itemAt(o, 0)
	if _, isNode := first.(*xmltree.Node); isNode {
		return true, nil
	}
	if n > 1 {
		return false, fmt.Errorf("err:FORG0006: effective boolean value of a %d-item sequence", n)
	}
	// (ebvAtomic, in line: the value of most predicates is a boolean.)
	switch v := first.(type) {
	case bool:
		return v, nil
	case string:
		return v != "", nil
	case float64:
		return v != 0 && !math.IsNaN(v), nil
	case *Atomic:
		switch {
		case v.T == XSboolean:
			return ToBool(v), nil
		case isStringType(v.T) || v.T == XSuntypedAtomic || v.T == XSanyURI:
			return v.Lexical() != "", nil
		case v.IsNumeric():
			f := v.Float()
			return f != 0 && !math.IsNaN(f), nil
		}
	}
	return false, fmt.Errorf("err:FORG0006: no effective boolean value for this item type")
}

// nodeSetEBV is EffectiveBoolErr of ns without boxing it: a carrier node
// (xmltree.Node.RealItem) counts as the item it stands for, as in itemAt.
func nodeSetEBV(ns NodeSet) (bool, error) {
	if len(ns) == 0 {
		return false, nil
	}
	nd := ns[0]
	if nd == nil || nd.RealItem() == nil {
		return true, nil
	}
	first := nd.RealItem()
	if _, isNode := first.(*xmltree.Node); isNode {
		return true, nil
	}
	if len(ns) > 1 {
		return false, fmt.Errorf("err:FORG0006: effective boolean value of a %d-item sequence", len(ns))
	}
	return ebvAtomic(first)
}

// ebvAtomic is the effective boolean value of a single non-node item.
func ebvAtomic(first Item) (bool, error) {
	switch v := first.(type) {
	case bool:
		return v, nil
	case string:
		return v != "", nil
	case float64:
		return v != 0 && !math.IsNaN(v), nil
	case *Atomic:
		switch {
		case v.T == XSboolean:
			return ToBool(v), nil
		case isStringType(v.T) || v.T == XSuntypedAtomic || v.T == XSanyURI:
			return v.Lexical() != "", nil
		case v.IsNumeric():
			f := v.Float()
			return f != 0 && !math.IsNaN(f), nil
		}
	}
	return false, fmt.Errorf("err:FORG0006: no effective boolean value for this item type")
}

func ToBool(o Object) bool {
	switch v := o.(type) {
	case bool:
		return v
	case float64:
		return v != 0 && !math.IsNaN(v)
	case string:
		return v != ""
	case NodeSet:
		return len(v) > 0
	case Sequence:
		return effectiveBool([]Item(v))
	case *Atomic:
		return effectiveBool([]Item{v})
	}
	return false
}

// ToNumber applies the number() conversion.
func ToNumber(o Object) float64 {
	switch v := o.(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		return parseNumber(v)
	case NodeSet:
		return parseNumber(ToString(o))
	case Sequence:
		if len(v) == 0 {
			return math.NaN()
		}
		return itemNumber(v[0])
	case *Atomic:
		return v.Float()
	}
	return math.NaN()
}

func parseNumber(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// ToString applies the string() conversion.
func ToString(o Object) string {
	switch v := o.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return formatNumber(v)
	case NodeSet:
		if len(v) == 0 {
			return ""
		}
		first := v.first()
		return nodeStringValue(first)
	case Sequence:
		if len(v) == 0 {
			return ""
		}
		return itemString(v[0])
	case *Atomic:
		return v.Lexical()
	case *Map, *Array:
		return ""
	}
	return ""
}

// formatNumber renders a float per XPath string(number) rules.
func formatNumber(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ToNodeSet returns the value as a NodeSet, or false if it is not all nodes.
func ToNodeSet(o Object) (NodeSet, bool) {
	switch v := o.(type) {
	case NodeSet:
		return v, true
	case *xmltree.Node:
		return NodeSet{v}, true
	case Sequence:
		ns := make(NodeSet, 0, len(v))
		for _, it := range v {
			n, ok := it.(*xmltree.Node)
			if !ok {
				return nil, false
			}
			ns = append(ns, n)
		}
		return ns, true
	}
	return nil, false
}

// Unique returns the node set in document order with duplicates removed.
func (ns NodeSet) Unique() NodeSet { return append(NodeSet{}, ns...).normalize() }

// NormalizeNodes puts ns in document order without duplicates, IN PLACE on
// its own array where it can (the result may be ns itself or a prefix of it):
// for a caller that owns ns and wants to normalize it once, ahead of handing
// out copies.
func NormalizeNodes(ns []*xmltree.Node) []*xmltree.Node { return NodeSet(ns).normalize() }

// NodeString is the string value of one node as ToString gives it for a node
// set holding just that node (a typed node's value normalized by its type).
func NodeString(n *xmltree.Node) string { return nodeStringValue(n) }

// first returns the node first in document order.
func (ns NodeSet) first() *xmltree.Node {
	if len(ns) == 0 {
		return nil
	}
	best := ns[0]
	for _, n := range ns[1:] {
		if docOrderBefore(n, best) {
			best = n
		}
	}
	return best
}

// normalize sorts the node set in document order and removes duplicates.
func (ns NodeSet) normalize() NodeSet {
	if len(ns) <= 1 {
		return ns
	}
	if alreadyNormalized(ns) {
		// Already strictly ordered with no duplicates: skip both the sort and
		// the dedup map below. For a large axis-step result over one actually
		// parsed document (this is the common, large case: assignOrder gives
		// every node a genuinely unique, strictly ordered (Tree(),Order())
		// pair, so adjacent pairs are never tied) this is the entire cost of
		// normalize() — no O(n log n) sort, no len(ns)-sized map allocation.
		return ns
	}
	sort.SliceStable(ns, func(i, j int) bool { return docOrderBefore(ns[i], ns[j]) })
	out := ns[:0]
	// A map-based "seen" check (not just comparing to the immediately
	// preceding element): nodes built as part of a constructed temporary
	// tree (an untyped xsl:variable/RTF, never run through xmltree's
	// document-parse assignOrder pass) all tie at Order()==0, so a stable
	// sort leaves two references to the SAME node wherever they started —
	// not necessarily adjacent to each other once other (also tied-at-0)
	// nodes sit between them. select-2016/2037: "$var//* union $var/*"
	// duplicates num1/num4/num5 (present in both operands) at non-adjacent
	// positions; comparing only to the previous element let every one of
	// them through twice.
	seen := make(map[*xmltree.Node]bool, len(ns))
	for _, n := range ns {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	// Adjacent-duplicate removal is exact whenever the sort actually ordered
	// equal nodes together. It cannot when two DIFFERENT nodes compare equal —
	// which happens for the roots of distinct documents, since document order
	// between separate trees is implementation-defined and every root's
	// Order() is 0. Re-check by identity only in that case (fn:collection() |
	// fn:collection(()) must be the same 3 documents, not 6 — QT3
	// collection-003/007).
	if hasDistinctRoots(out) {
		out = dedupByIdentity(out)
	}
	return out
}

// alreadyNormalized reports whether ns is already in STRICT document order
// with no duplicates: docOrderBefore(ns[i-1], ns[i]) holds for every adjacent
// pair.
//
// This is deliberately stricter than "no adjacent inversion" (which would
// also accept a TIE — docOrderBefore false in both directions — between two
// adjacent elements). Two different nodes from unrelated trees genuinely do
// tie this way (e.g. two separate RTFs' roots, or anything under them never
// resolved by Tree()/Order() or by a shared ancestorPath prefix), and
// tolerating a tie here would let a duplicate recur non-adjacently THROUGH
// it undetected — exactly the bug the map-based dedup below exists to catch
// (select-2016/2037: "$var//* union $var/*" duplicates num1/num4/num5 at
// non-adjacent positions, with other tied elements sitting between the
// repeats). Requiring STRICT order throughout rules that out by
// transitivity instead: if the same node appeared twice, docOrderBefore of
// it against itself is false by definition, breaking strictness right there.
//
// The common, large case this exists for — an axis step's result over one
// actually-parsed document — is never affected by the stricter requirement:
// assignOrder gives every node of a parsed tree a genuinely unique, totally
// ordered (Tree(),Order()) pair, so two distinct nodes from it never tie.
func alreadyNormalized(ns NodeSet) bool {
	for i := 1; i < len(ns); i++ {
		if !docOrderBefore(ns[i-1], ns[i]) {
			return false
		}
	}
	return true
}

// hasDistinctRoots reports whether ns spans more than one tree.
func hasDistinctRoots(ns NodeSet) bool {
	if len(ns) < 2 {
		return false
	}
	first := ns[0].Root()
	for _, n := range ns[1:] {
		if n.Root() != first {
			return true
		}
	}
	return false
}

// dedupByIdentity removes repeated node pointers, preserving first-occurrence
// order.
func dedupByIdentity(ns NodeSet) NodeSet {
	seen := make(map[*xmltree.Node]bool, len(ns))
	out := ns[:0]
	for _, n := range ns {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// docOrderBefore reports whether a precedes b in document order. Parsed trees
// carry distinct Order() indices assigned at parse time; nodes of a CONSTRUCTED
// tree (a result-tree fragment, an xsl:variable body) all share index 0, so
// they are compared structurally instead — otherwise a reverse axis such as
// ancestor::* over an RTF would never be re-sorted into document order
// (expression-0929).
func docOrderBefore(a, b *xmltree.Node) bool {
	if a == b {
		return false
	}
	// Different (numbered) trees: the earlier-created tree comes first,
	// whole (see xmltree.Node.Tree). A node numbered in no tree yet keeps the
	// index/structural comparison below.
	if ta, tb := a.Tree(), b.Tree(); ta != 0 && tb != 0 && ta != tb {
		return ta < tb
	}
	if oa, ob := a.Order(), b.Order(); oa != ob {
		return oa < ob
	}
	pa, pb := ancestorPath(a), ancestorPath(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		// Siblings (or same-parent attribute/namespace/child nodes): compare
		// their positions in the parent's document-order child listing.
		return siblingIndex(pa[i]) < siblingIndex(pb[i])
	}
	// One path is a prefix of the other: the ancestor comes first.
	return len(pa) < len(pb)
}

// ancestorPath returns the chain from the tree root down to n inclusive.
func ancestorPath(n *xmltree.Node) []*xmltree.Node {
	var up []*xmltree.Node
	for cur := n; cur != nil; cur = cur.Parent {
		up = append(up, cur)
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up
}

// siblingIndex is n's position among its parent's namespace nodes, attribute
// nodes and children, in document order (namespaces, then attributes, then
// children — the same order assignOrder uses).
func siblingIndex(n *xmltree.Node) int {
	p := n.Parent
	if p == nil {
		return 0
	}
	i := 0
	for _, x := range p.NSDecls() {
		if x == n {
			return i
		}
		i++
	}
	for _, x := range p.Attrs {
		if x == n {
			return i
		}
		i++
	}
	for _, x := range p.Children {
		if x == n {
			return i
		}
		i++
	}
	return i
}
