package xpath

import (
	"fmt"
	"math"
	"sort"
)

// This file implements the "maparray" family: additional map: and array:
// functions from XPath 3.1 Functions & Operators. All private helpers are
// prefixed with "ma" to avoid clashes with sibling files.

func init() {
	// map: namespace additions
	mapFuncs["find"] = maMapFind

	// array: namespace additions
	arrayFuncs["subarray"] = maArraySubarray
	arrayFuncs["remove"] = maArrayRemove
	arrayFuncs["insert-before"] = maArrayInsertBefore
	arrayFuncs["put"] = maArrayPut
	arrayFuncs["head"] = maArrayHead
	arrayFuncs["tail"] = maArrayTail
	arrayFuncs["reverse"] = maArrayReverse
	arrayFuncs["filter"] = maArrayFilter
	arrayFuncs["fold-left"] = maArrayFoldLeft
	arrayFuncs["fold-right"] = maArrayFoldRight
	arrayFuncs["for-each-pair"] = maArrayForEachPair
	arrayFuncs["sort"] = maArraySort
}

// maArrayArg returns the *Array from argument i, or an error if it is not one.
func maArrayArg(args []Object, i int) (*Array, error) {
	if ar, ok := firstItem(arg(args, i)).(*Array); ok {
		return ar, nil
	}
	return nil, fmt.Errorf("err:XPTY0004: argument %d is not an array", i+1)
}

// maFuncArg returns the *Function from argument i, or an error.
func maFuncArg(args []Object, i, arity int) (*Function, error) {
	return hofFuncArg(fmt.Sprintf("array function argument %d", i+1), arg(args, i), arity)
}

// maMapFind searches the maps (and arrays) reachable in $input and collects,
// into a single array, every value associated with the given key. This is a
// pragmatic flattening of the spec's recursive descent.
func maMapFind(c *Context, a []Object) (Object, error) {
	key := firstItem(arg(a, 1))
	var found []Object
	var walk func(o Object)
	walk = func(o Object) {
		for _, it := range Items(o) {
			switch v := it.(type) {
			case *Map:
				if v.Contains(key) {
					found = append(found, v.Get(key))
				}
				for _, k := range v.Keys() {
					walk(v.Get(k))
				}
			case *Array:
				for _, m := range v.Members() {
					walk(m)
				}
			}
		}
	}
	walk(arg(a, 0))
	return NewArray(found), nil
}

// maArraySubarray implements array:subarray($a, $start [, $length]).
// 1-based; $start in 1..size+1; $length defaults to size-start+1.
func maArraySubarray(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	m := ar.Members()
	start := int(maRound(ToNumber(arg(a, 1))))
	length := len(m) - start + 1
	if arg(a, 2) != nil {
		length = int(maRound(ToNumber(arg(a, 2))))
	}
	if start < 1 || start > len(m)+1 {
		return nil, fmt.Errorf("err:FOAY0001: subarray start %d out of bounds", start)
	}
	if length < 0 {
		return nil, fmt.Errorf("err:FOAY0002: subarray length %d is negative", length)
	}
	if start+length > len(m)+1 {
		return nil, fmt.Errorf("err:FOAY0001: subarray range out of bounds")
	}
	out := append([]Object{}, m[start-1:start-1+length]...)
	return NewArray(out), nil
}

// maArrayRemove implements array:remove($a, $positions). $positions may be a
// sequence of integer positions to drop (1-based).
func maArrayRemove(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	m := ar.Members()
	drop := map[int]bool{}
	for _, it := range Items(arg(a, 1)) {
		p := int(maRound(itemNumber(it)))
		if p < 1 || p > len(m) {
			return nil, fmt.Errorf("err:FOAY0001: remove position %d out of bounds", p)
		}
		drop[p] = true
	}
	var out []Object
	for i, member := range m {
		if !drop[i+1] {
			out = append(out, member)
		}
	}
	return NewArray(out), nil
}

// maArrayPut implements array:put($array, $position, $member): a new array with
// the member at $position (1..size) replaced by $member (a whole new member,
// which may itself be a sequence). $position out of range is FOAY0001.
func maArrayPut(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	m := ar.Members()
	p := int(maRound(itemNumber(firstItem(arg(a, 1)))))
	if p < 1 || p > len(m) {
		return nil, fmt.Errorf("err:FOAY0001: put position %d out of bounds (1..%d)", p, len(m))
	}
	out := make([]Object, len(m))
	copy(out, m)
	out[p-1] = arg(a, 2)
	return NewArray(out), nil
}

// maArrayInsertBefore implements array:insert-before($a, $position, $member).
// $position in 1..size+1; the new member is inserted before that position.
func maArrayInsertBefore(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	m := ar.Members()
	pos := int(maRound(ToNumber(arg(a, 1))))
	if pos < 1 || pos > len(m)+1 {
		return nil, fmt.Errorf("err:FOAY0001: insert position %d out of bounds", pos)
	}
	out := make([]Object, 0, len(m)+1)
	out = append(out, m[:pos-1]...)
	out = append(out, arg(a, 2))
	out = append(out, m[pos-1:]...)
	return NewArray(out), nil
}

// maArrayHead implements array:head($a) -> the first member.
func maArrayHead(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	if ar.Size() == 0 {
		return nil, fmt.Errorf("err:FOAY0001: array:head of empty array")
	}
	return ar.Get(1), nil
}

// maArrayTail implements array:tail($a) -> the array without its first member.
func maArrayTail(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	if ar.Size() == 0 {
		return nil, fmt.Errorf("err:FOAY0001: array:tail of empty array")
	}
	return NewArray(append([]Object{}, ar.Members()[1:]...)), nil
}

// maArrayReverse implements array:reverse($a).
func maArrayReverse(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	m := ar.Members()
	out := make([]Object, len(m))
	for i := range m {
		out[len(m)-1-i] = m[i]
	}
	return NewArray(out), nil
}

// maArrayFilter implements array:filter($a, $f) keeping members for which the
// supplied function returns an effective boolean true.
func maArrayFilter(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	fn, err := maFuncArg(a, 1, 1)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, m := range ar.Members() {
		r, err := fn.Call([]Object{m})
		if err != nil {
			return nil, err
		}
		// function(item()*) as xs:boolean: the result must be exactly one
		// boolean, not an EBV (array-filter-007/009: substring-after(?, "e")
		// returns a string).
		b, ok := singleBoolean(r)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: array:filter predicate must return xs:boolean")
		}
		if b {
			out = append(out, m)
		}
	}
	return NewArray(out), nil
}

// maArrayFoldLeft implements array:fold-left($a, $zero, $f).
func maArrayFoldLeft(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	fn, err := maFuncArg(a, 2, 2)
	if err != nil {
		return nil, err
	}
	acc := arg(a, 1)
	for _, m := range ar.Members() {
		acc, err = fn.Call([]Object{acc, m})
		if err != nil {
			return nil, err
		}
	}
	return acc, nil
}

// maArrayFoldRight implements array:fold-right($a, $zero, $f).
func maArrayFoldRight(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	fn, err := maFuncArg(a, 2, 2)
	if err != nil {
		return nil, err
	}
	acc := arg(a, 1)
	m := ar.Members()
	for i := len(m) - 1; i >= 0; i-- {
		acc, err = fn.Call([]Object{m[i], acc})
		if err != nil {
			return nil, err
		}
	}
	return acc, nil
}

// maArrayForEachPair implements array:for-each-pair($a, $b, $f). It applies $f
// to corresponding members and returns an array of the results, stopping at
// the shorter array.
func maArrayForEachPair(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	br, err := maArrayArg(a, 1)
	if err != nil {
		return nil, err
	}
	fn, err := maFuncArg(a, 2, 2)
	if err != nil {
		return nil, err
	}
	n := ar.Size()
	if br.Size() < n {
		n = br.Size()
	}
	out := make([]Object, 0, n)
	for i := 0; i < n; i++ {
		r, err := fn.Call([]Object{ar.Members()[i], br.Members()[i]})
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return NewArray(out), nil
}

// maArraySort implements array:sort($a [, $collation [, $key]]). The collation
// argument is accepted and ignored; the optional key function maps each member
// to a sort key.
func maArraySort(c *Context, a []Object) (Object, error) {
	ar, err := maArrayArg(a, 0)
	if err != nil {
		return nil, err
	}
	var keyFn *Function
	if len(a) > 2 && arg(a, 2) != nil {
		keyFn, _ = asFunc(a[2])
	}
	// $collation (2nd argument) orders string-family keys; nil keeps the
	// codepoint comparison (fn-function-lookup-800: array:sort#2 caseblind).
	var coll func(x, y string) int
	if len(a) > 1 && !numIsEmpty(arg(a, 1)) {
		c, err := collationArg(arg(a, 1))
		if err != nil {
			return nil, err
		}
		coll = c
	}
	members := ar.Members()
	// Precompute each member's atomized sort key, propagating any key-function
	// error (array-sort-005/009).
	keys := make([][]Item, len(members))
	for i, m := range members {
		k := Object(m)
		if keyFn != nil {
			r, err := keyFn.Call([]Object{m})
			if err != nil {
				return nil, err
			}
			k = r
		}
		atoms, err := Atomize(k)
		if err != nil {
			return nil, err
		}
		keys[i] = atoms
	}
	idx := make([]int, len(members))
	for i := range idx {
		idx[i] = i
	}
	var sortErr error
	sort.SliceStable(idx, func(i, j int) bool {
		cmp, err := maKeySeqCompareColl(keys[idx[i]], keys[idx[j]], coll)
		if err != nil {
			if sortErr == nil {
				sortErr = err
			}
			return false
		}
		return cmp < 0
	})
	if sortErr != nil {
		return nil, sortErr
	}
	out := make([]Object, len(members))
	for i, k := range idx {
		out[i] = members[k]
	}
	return NewArray(out), nil
}

// maKeySeqCompare orders two atomized sort keys item-by-item; a shorter key
// that is a prefix of the other sorts first, and an incomparable pair is a
// type error (array-sort-003/007).
func maKeySeqCompare(a, b []Item) (int, error) {
	return maKeySeqCompareColl(a, b, nil)
}

// maKeySeqCompareColl is maKeySeqCompare with an optional collation applied
// to string-family key pairs.
func maKeySeqCompareColl(a, b []Item, coll func(x, y string) int) (int, error) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for k := 0; k < n; k++ {
		var cmp int
		var err error
		if ax, ok := a[k].(*Atomic); ok && coll != nil && qnStringy(ax) {
			if ay, ok := b[k].(*Atomic); ok && qnStringy(ay) {
				cmp = coll(ax.Lexical(), ay.Lexical())
				if cmp != 0 {
					return cmp, nil
				}
				continue
			}
		}
		cmp, err = maKeyCompare(a[k], b[k])
		if err != nil {
			return 0, err
		}
		if cmp != 0 {
			return cmp, nil
		}
	}
	return len(a) - len(b), nil
}

// maKeyCompare orders two atomic sort-key items with fn:sort semantics: NaN
// (and the empty sequence, handled by the caller) sorts least, numeric keys
// compare numerically, string-family keys by codepoint, and a mixed numeric/
// non-numeric pair is XPTY0004.
func maKeyCompare(x, y Item) (int, error) {
	ax, okx := x.(*Atomic)
	ay, oky := y.(*Atomic)
	if !okx || !oky {
		return 0, fmt.Errorf("err:XPTY0004: sort key must be atomic")
	}
	xn := ax.IsNumeric() && math.IsNaN(ax.Float())
	yn := ay.IsNumeric() && math.IsNaN(ay.Float())
	if xn || yn {
		switch {
		case xn && yn:
			return 0, nil
		case xn:
			return -1, nil
		default:
			return 1, nil
		}
	}
	if ax.IsNumeric() != ay.IsNumeric() {
		return 0, fmt.Errorf("err:XPTY0004: incomparable sort keys")
	}
	cmp, ok := atomicCompare(ax, ay)
	if !ok {
		return 0, fmt.Errorf("err:XPTY0004: incomparable sort keys")
	}
	return cmp, nil
}

// maRound rounds half away from zero, matching xs:integer coercion of a number.
func maRound(f float64) float64 {
	if f < 0 {
		return math.Ceil(f - 0.5)
	}
	return math.Floor(f + 0.5)
}
