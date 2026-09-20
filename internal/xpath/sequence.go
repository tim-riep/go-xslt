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
			if n != nil && n.RealItem != nil {
				out[i] = n.RealItem
				continue
			}
			out[i] = n
		}
		return out
	default:
		return []Item{v}
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
	out := ""
	for i, it := range items {
		if i > 0 {
			out += sep
		}
		out += itemString(it)
	}
	return out
}
