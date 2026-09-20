package xpath

import (
	"fmt"
	"math"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["insert-before"] = sqInsertBefore
	coreFuncs["remove"] = sqRemove
	coreFuncs["unordered"] = sqUnordered
	coreFuncs["zero-or-one"] = sqZeroOrOne
	coreFuncs["one-or-more"] = sqOneOrMore
	coreFuncs["exactly-one"] = sqExactlyOne
	coreFuncs["deep-equal"] = sqDeepEqual
}

// sqToInt converts an Object to an int (truncating toward zero).
func sqToInt(o Object) int {
	f := ToNumber(o)
	if math.IsNaN(f) {
		return 0
	}
	return int(f)
}

// sqIntegerArg reads a $position as xs:integer argument under the function
// conversion rules: integer-family atomics pass, nodes and xs:untypedAtomic
// cast to xs:integer, and any other atomic type — a decimal, double or string
// — is XPTY0004 (K-SeqRemoveFunc-25..27: remove(1 to 10, 1.0) / "1").
func sqIntegerArg(fn string, o Object) (int, error) {
	items := Items(o)
	if len(items) != 1 {
		return 0, fmt.Errorf("err:XPTY0004: %s: position must be a single xs:integer, got %d items", fn, len(items))
	}
	switch v := items[0].(type) {
	case *Atomic:
		if isIntegerType(v.T) {
			return sqToInt(v), nil
		}
		if v.T != XSuntypedAtomic {
			return 0, fmt.Errorf("err:XPTY0004: %s: position must be xs:integer, got %s", fn, v.T)
		}
		iv, err := CastTo(v, XSinteger)
		if err != nil {
			return 0, err
		}
		return sqToInt(iv), nil
	case *xmltree.Node:
		iv, err := CastTo(v, XSinteger)
		if err != nil {
			return 0, err
		}
		return sqToInt(iv), nil
	case string:
		return 0, fmt.Errorf("err:XPTY0004: %s: position must be xs:integer, got xs:string", fn)
	}
	return sqToInt(items[0]), nil
}

// fn:insert-before($target, $position as xs:integer, $inserts)
// Inserts the items of $inserts into $target before the item at $position
// (1-based). Positions <= 1 insert at the start; positions > length append.
func sqInsertBefore(ctx *Context, args []Object) (Object, error) {
	target := Items(arg(args, 0))
	// $position is exactly one xs:integer (K-SeqInsertBeforeFunc-4: () is
	// XPTY0004).
	pos, err := sqIntegerArg("insert-before", arg(args, 1))
	if err != nil {
		return nil, err
	}
	inserts := Items(arg(args, 2))

	if pos < 1 {
		pos = 1
	}
	if pos > len(target)+1 {
		pos = len(target) + 1
	}
	// 1-based -> 0-based index of the item to insert before.
	idx := pos - 1

	out := make([]Item, 0, len(target)+len(inserts))
	out = append(out, target[:idx]...)
	out = append(out, inserts...)
	out = append(out, target[idx:]...)
	return FromItems(out), nil
}

// fn:remove($target, $position as xs:integer)
// Removes the item at $position (1-based). Out-of-range positions are no-ops.
func sqRemove(ctx *Context, args []Object) (Object, error) {
	target := Items(arg(args, 0))
	pos, err := sqIntegerArg("remove", arg(args, 1))
	if err != nil {
		return nil, err
	}

	if pos < 1 || pos > len(target) {
		return FromItems(append([]Item(nil), target...)), nil
	}
	idx := pos - 1
	out := make([]Item, 0, len(target)-1)
	out = append(out, target[:idx]...)
	out = append(out, target[idx+1:]...)
	return FromItems(out), nil
}

// fn:unordered($seq) returns the sequence as-is.
func sqUnordered(ctx *Context, args []Object) (Object, error) {
	return FromItems(Items(arg(args, 0))), nil
}

// fn:zero-or-one($seq) returns the sequence if it has 0 or 1 items,
// otherwise raises err:FORG0003.
func sqZeroOrOne(ctx *Context, args []Object) (Object, error) {
	items := Items(arg(args, 0))
	if len(items) > 1 {
		return nil, fmt.Errorf("err:FORG0003: fn:zero-or-one called with a sequence containing more than one item")
	}
	return FromItems(items), nil
}

// fn:one-or-more($seq) returns the sequence if it has at least 1 item,
// otherwise raises err:FORG0004.
func sqOneOrMore(ctx *Context, args []Object) (Object, error) {
	items := Items(arg(args, 0))
	if len(items) == 0 {
		return nil, fmt.Errorf("err:FORG0004: fn:one-or-more called with a sequence containing no items")
	}
	return FromItems(items), nil
}

// fn:exactly-one($seq) returns the sequence if it has exactly 1 item,
// otherwise raises err:FORG0005.
func sqExactlyOne(ctx *Context, args []Object) (Object, error) {
	items := Items(arg(args, 0))
	if len(items) != 1 {
		return nil, fmt.Errorf("err:FORG0005: fn:exactly-one called with a sequence not containing exactly one item")
	}
	return FromItems(items), nil
}

// deepEq carries the collation of one fn:deep-equal call, applied to every
// string comparison it makes (atomic values, text/comment/PI content and
// attribute values). A nil coll is the default codepoint collation.
type deepEq struct {
	coll func(a, b string) int
}

// strEq compares two strings under the call's collation.
func (d deepEq) strEq(a, b string) bool {
	if d.coll == nil {
		return a == b
	}
	return d.coll(a, b) == 0
}

// fn:deep-equal($a, $b, $collation?) compares two sequences item-by-item.
func sqDeepEqual(ctx *Context, args []Object) (Object, error) {
	var d deepEq
	if len(args) >= 3 {
		coll, err := collationArg(args[2])
		if err != nil {
			return nil, err
		}
		d.coll = coll
	} else {
		d.coll = ctxCollation(ctx)
	}
	a := Items(arg(args, 0))
	b := Items(arg(args, 1))
	// Function items have no equality: FOTY0015 (inline-fn-029).
	if sqHasFunctionItem(a) || sqHasFunctionItem(b) {
		return nil, fmt.Errorf("err:FOTY0015: fn:deep-equal cannot compare function items")
	}
	return NewBool(d.items(a, b)), nil
}

// sqHasFunctionItem reports whether a function item occurs anywhere in the
// items, including inside array members and map values.
func sqHasFunctionItem(items []Item) bool {
	for _, it := range items {
		switch v := it.(type) {
		case *Function:
			return true
		case *Array:
			for _, m := range v.Members() {
				if sqHasFunctionItem(Items(m)) {
					return true
				}
			}
		case *Map:
			for _, k := range v.Keys() {
				if sqHasFunctionItem(Items(v.Get(k))) {
					return true
				}
			}
		}
	}
	return false
}

// items reports whether two item sequences are deep-equal.
func (d deepEq) items(a, b []Item) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !d.item(a[i], b[i]) {
			return false
		}
	}
	return true
}

// item reports whether two items are deep-equal.
func (d deepEq) item(a, b Item) bool {
	an, aIsNode := a.(*xmltree.Node)
	bn, bIsNode := b.(*xmltree.Node)
	if aIsNode != bIsNode {
		return false
	}
	if aIsNode {
		return d.node(an, bn)
	}
	// Maps: same keys (order-insensitive) with deep-equal values. Arrays:
	// deep-equal members in order.
	if am, ok := a.(*Map); ok {
		bm, ok := b.(*Map)
		if !ok || am.Size() != bm.Size() {
			return false
		}
		for _, k := range am.Keys() {
			if !bm.Contains(k) || !d.items(Items(am.Get(k)), Items(bm.Get(k))) {
				return false
			}
		}
		return true
	}
	if aar, ok := a.(*Array); ok {
		bar, ok := b.(*Array)
		if !ok || aar.Size() != bar.Size() {
			return false
		}
		am, bm := aar.Members(), bar.Members()
		for i := range am {
			if !d.items(Items(am[i]), Items(bm[i])) {
				return false
			}
		}
		return true
	}
	if _, ok := b.(*Map); ok {
		return false
	}
	if _, ok := b.(*Array); ok {
		return false
	}
	// Atomic comparison via typed value. Unlike eq, NaN is deep-equal to NaN
	// (K-SeqDeepEqualFunc-8..11), and strings compare under the collation
	// (K-SeqDeepEqualFunc-64).
	aa, errA := sqAtomicOf(a)
	bb, errB := sqAtomicOf(b)
	if errA != nil || errB != nil || aa == nil || bb == nil {
		return false
	}
	if atomIsNaN(aa) && atomIsNaN(bb) {
		return true
	}
	if d.coll != nil && isComparableAsString(aa) && isComparableAsString(bb) {
		return d.strEq(aa.Lexical(), bb.Lexical())
	}
	cmp, ok := atomicCompare(aa, bb)
	return ok && cmp == 0
}

// atomIsNaN reports whether an atomic is a float/double NaN.
func atomIsNaN(a *Atomic) bool {
	return (a.T == XSdouble || a.T == XSfloat) && math.IsNaN(a.f)
}

// sqAtomicOf coerces an item to a single *Atomic via fn:data semantics.
func sqAtomicOf(it Item) (*Atomic, error) {
	if a, ok := it.(*Atomic); ok {
		return a, nil
	}
	atoms, err := Atomize(it)
	if err != nil {
		return nil, err
	}
	if len(atoms) != 1 {
		return nil, fmt.Errorf("err:FORG0001: expected a single atomic value")
	}
	a, ok := atoms[0].(*Atomic)
	if !ok {
		return nil, fmt.Errorf("err:FORG0001: expected a single atomic value")
	}
	return a, nil
}

// node compares two nodes by kind, name and string value, recursing into
// element children.
func (d deepEq) node(a, b *xmltree.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind {
		return false
	}
	if a.Name.Local != b.Name.Local || a.Name.Space != b.Name.Space {
		return false
	}
	switch a.Kind {
	case xmltree.KindDocument, xmltree.KindElement:
		if !d.attrs(a, b) {
			return false
		}
		ac := sqContentChildren(a)
		bc := sqContentChildren(b)
		if len(ac) != len(bc) {
			return false
		}
		for i := range ac {
			if !d.node(ac[i], bc[i]) {
				return false
			}
		}
		return true
	default:
		return d.strEq(a.StringValue(), b.StringValue())
	}
}

// sqContentChildren returns the child nodes that participate in deep-equal
// content comparison (elements, text, comments, PIs).
func sqContentChildren(n *xmltree.Node) []*xmltree.Node {
	out := make([]*xmltree.Node, 0, len(n.Children))
	for _, c := range n.Children {
		switch c.Kind {
		case xmltree.KindElement, xmltree.KindText, xmltree.KindComment, xmltree.KindPI:
			out = append(out, c)
		}
	}
	return out
}

// attrs compares the attribute sets of two element nodes, ignoring order.
func (d deepEq) attrs(a, b *xmltree.Node) bool {
	if len(a.Attrs) != len(b.Attrs) {
		return false
	}
	for _, aa := range a.Attrs {
		found := false
		for _, ba := range b.Attrs {
			if aa.Name.Local == ba.Name.Local && aa.Name.Space == ba.Name.Space {
				if !d.strEq(aa.Value, ba.Value) {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
