package xpath

import (
	"math"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Sequence is a general XDM sequence of items. An Item is one of:
//   - *xmltree.Node      (a node)
//   - string, float64, bool   (atomic values)
//   - *Map, *Array       (XPath 3.1 maps and arrays)
//   - *Function          (a function item)
//
// Node-only sequences are usually represented as NodeSet (which flows through
// the path machinery); Sequence is used for atomic or mixed sequences.
type Sequence []Item

// Item is a single member of a sequence.
type Item = any

// itemCount is len(Items(o)), and itemAt(o, i) is Items(o)[i], without
// building the slice — for the hot callers that look at one or two items of
// what is mostly a single value.
func itemCount(o Object) int {
	switch v := o.(type) {
	case nil:
		return 0
	case Sequence:
		return len(v)
	case NodeSet:
		return len(v)
	default:
		return 1
	}
}

// ItemCount is len(Items(o)) without building the slice.
func ItemCount(o Object) int { return itemCount(o) }

func itemAt(o Object, i int) Item {
	switch v := o.(type) {
	case Sequence:
		return v[i]
	case NodeSet:
		// As Items: a carrier node unwraps to the item it stands for.
		if n := v[i]; n != nil && n.RealItem() != nil {
			return n.RealItem()
		}
		return v[i]
	default:
		if i != 0 {
			panic("itemAt: index out of range")
		}
		return v
	}
}

// Items flattens any Object into a slice of items.
func Items(o Object) []Item {
	switch v := o.(type) {
	case nil:
		return nil
	case Sequence:
		return []Item(v)
	case NodeSet:
		out := make([]Item, len(v))
		for i, n := range v {
			// A carrier node standing in for a non-node item that cannot
			// round-trip through the node tree at all (a map, array, or
			// function — see xmltree.Node.RealItem's doc comment) unwraps
			// back to the real item here, at the canonical Object->[]Item
			// choke point every sequence consumer in this package funnels
			// through — additive only: nil for every ordinary node, which is
			// every node that predates this mechanism.
			if n != nil && n.RealItem() != nil {
				out[i] = n.RealItem()
				continue
			}
			out[i] = n
		}
		return out
	default:
		return []Item{v}
	}
}

// appendItems appends the items of o to dst: Items(o) without the one-item
// slice that a single non-node item would be boxed into first.
func appendItems(dst []Item, o Object) []Item {
	switch v := o.(type) {
	case nil:
		return dst
	case Sequence:
		return append(dst, v...)
	case NodeSet:
		for _, n := range v {
			if n != nil && n.RealItem() != nil {
				dst = append(dst, n.RealItem())
				continue
			}
			dst = append(dst, n)
		}
		return dst
	default:
		return append(dst, v)
	}
}

// FromItems builds an Object from items: empty -> empty Sequence, all-nodes ->
// NodeSet, single item -> that item, otherwise a Sequence.
func FromItems(items []Item) Object {
	if len(items) == 0 {
		return Sequence{}
	}
	allNodes := true
	for _, it := range items {
		if _, ok := it.(*xmltree.Node); !ok {
			allNodes = false
			break
		}
	}
	if allNodes {
		ns := make(NodeSet, len(items))
		for i, it := range items {
			ns[i] = it.(*xmltree.Node)
		}
		return ns
	}
	if len(items) == 1 {
		return items[0]
	}
	return Sequence(items)
}

// itemString returns the string value of a single item.
func itemString(it Item) string {
	switch v := it.(type) {
	case *xmltree.Node:
		return nodeStringValue(v)
	case *Atomic:
		return v.Lexical()
	case string:
		return v
	case float64:
		return formatNumber(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case *Map:
		return ""
	case *Array:
		return ""
	}
	return ""
}

// itemNumber returns the numeric value of a single item.
func itemNumber(it Item) float64 {
	switch v := it.(type) {
	case *Atomic:
		return v.Float()
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	default:
		return parseNumber(itemString(it))
	}
}

// effectiveBool computes the XPath effective boolean value of a sequence.
func effectiveBool(seq []Item) bool {
	if len(seq) == 0 {
		return false
	}
	if _, ok := seq[0].(*xmltree.Node); ok {
		return true
	}
	if len(seq) == 1 {
		switch v := seq[0].(type) {
		case bool:
			return v
		case string:
			return v != ""
		case float64:
			return v != 0 && !math.IsNaN(v)
		case *Atomic:
			switch {
			case v.T == XSboolean:
				return v.b
			case isStringType(v.T) || v.T == XSuntypedAtomic || v.T == XSanyURI:
				return v.s != ""
			case v.IsNumeric():
				f := v.Float()
				return f != 0 && !math.IsNaN(f)
			}
			return true
		}
	}
	// Multiple non-node items: per spec this is an error; be lenient.
	return true
}

// seqAsString joins item string-values with sep (used by value-of/string-join).
func seqAsString(items []Item, sep string) string {
	if len(items) == 1 {
		return itemString(items[0])
	}
	// The strings are taken first so the result is built in one
	// allocation of its final size (joinParts), not grown piece by piece.
	var buf [16]string
	parts := buf[:0]
	total := 0
	for _, it := range items {
		s := itemString(it)
		parts = append(parts, s)
		total += len(s)
	}
	return joinParts(parts, total, sep)
}
