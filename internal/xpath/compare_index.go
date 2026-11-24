package xpath

import (
	"math"
	"sync/atomic"
)

// A general comparison "x = $seq" against a long sequence is existential: it
// compares x with every item. Inside a predicate that runs once per item of
// another long sequence ($all[not(. = $seen)]) that is quadratic — 10^11
// comparisons for the unicode-90 test set's code point lists. Where the long
// operand is a sequence of plain integers or plain strings (codepoint
// collation), the same answer comes from a hash lookup. The index is built
// once per sequence and kept on the comparison node, so a predicate reuses it
// on every evaluation.
//
// Only cases where the lookup is provably the same as the item-by-item
// comparison take this path; anything else — untyped items in the long
// operand, a collation, a type mismatch that must raise XPTY0004, a float —
// goes the ordinary way.

// eqIndexMin is the length from which an index pays for itself.
const eqIndexMin = 64

// disableEqIndex turns the index off; tests compare both paths with it.
var disableEqIndex bool

// eqCache remembers the index of the last long operand a comparison node saw.
// It holds the sequence itself, so the backing array cannot be freed and its
// address reused for a different sequence while the cache entry exists:
// identity (first element's address and length) then means the same items.
type eqCache struct {
	seq Sequence
	idx *eqIndex // nil: the sequence does not qualify
}

type eqIndex struct {
	ints   map[int64]struct{}
	strs   map[string]struct{}
	maxAbs int64 // largest |integer| in ints, to know when double is exact
}

// fastGeneralEq answers "l = r" through an index when one operand is a long
// Sequence that qualifies; ok is false when the ordinary comparison must run.
func (n *CompareExpr) fastGeneralEq(l, r Object, ctx *Context) (result, ok bool) {
	if disableEqIndex || n.Op != "=" || (ctx != nil && (ctx.BC10 || ctx.DefaultCollation != nil)) {
		return false, false
	}
	small, long := l, r
	ls, lok := l.(Sequence)
	rs, rok := r.(Sequence)
	switch {
	case rok && len(rs) >= eqIndexMin && (!lok || len(ls) < len(rs)):
	case lok && len(ls) >= eqIndexMin:
		small, long = r, l
	default:
		return false, false
	}
	idx := n.indexFor(long.(Sequence))
	if idx == nil {
		return false, false
	}
	items, err := Atomize(small)
	if err != nil || len(items) == 0 {
		return false, false
	}
	found := false
	for _, it := range items {
		hit, decided := idx.contains(it.(*Atomic))
		if !decided {
			return false, false
		}
		found = found || hit
	}
	return found, true
}

func (n *CompareExpr) indexFor(seq Sequence) *eqIndex {
	if c := n.eq.Load(); c != nil && len(c.seq) == len(seq) && &c.seq[0] == &seq[0] {
		return c.idx
	}
	idx := buildEqIndex(seq)
	n.eq.Store(&eqCache{seq: seq, idx: idx})
	return idx
}

func buildEqIndex(seq Sequence) *eqIndex {
	idx := &eqIndex{}
	// The value model keeps some strings as plain Go strings (xs:string).
	if _, plain := seq[0].(string); plain {
		return buildStringIndex(seq, idx)
	}
	first, ok := seq[0].(*Atomic)
	if !ok {
		return nil
	}
	switch {
	case isIntegerType(first.T):
		idx.ints = make(map[int64]struct{}, len(seq))
		for _, it := range seq {
			a, ok := it.(*Atomic)
			if !ok {
				return nil
			}
			v, ok := a.intFast()
			if !ok {
				return nil
			}
			idx.ints[v] = struct{}{}
			if v == math.MinInt64 {
				idx.maxAbs = math.MaxInt64
			} else if abs := max(v, -v); abs > idx.maxAbs {
				idx.maxAbs = abs
			}
		}
	case isStringType(first.T):
		return buildStringIndex(seq, idx)
	default:
		return nil
	}
	return idx
}

func buildStringIndex(seq Sequence, idx *eqIndex) *eqIndex {
	idx.strs = make(map[string]struct{}, len(seq))
	for _, it := range seq {
		switch v := it.(type) {
		case string:
			idx.strs[v] = struct{}{}
		case *Atomic:
			if !isStringType(v.T) {
				return nil
			}
			idx.strs[v.s] = struct{}{}
		default:
			return nil
		}
	}
	return idx
}

// contains reports whether a equals some item of the index under general
// comparison; decided is false when this cannot be answered by lookup.
func (idx *eqIndex) contains(a *Atomic) (hit, decided bool) {
	if idx.strs != nil {
		switch {
		case isStringType(a.T), a.T == XSanyURI, a.T == XSuntypedAtomic:
			// anyURI is promoted to string, untypedAtomic cast to string.
			_, hit = idx.strs[a.s]
			return hit, true
		}
		return false, false // XPTY0004 or another rule: ordinary path
	}
	switch {
	case isIntegerType(a.T):
		n, ok := a.intFast()
		if !ok {
			return false, true // no int64 item equals it
		}
		_, hit = idx.ints[n]
		return hit, true
	case a.T == XSdecimal:
		if m, s, ok := a.decFast(); ok {
			if s != 0 {
				return false, true
			}
			_, hit = idx.ints[m]
			return hit, true
		}
		if !a.d.IsInt() {
			return false, true
		}
		num := a.d.Num()
		if !num.IsInt64() {
			return false, true
		}
		_, hit = idx.ints[num.Int64()]
		return hit, true
	case a.T == XSdouble, a.T == XSuntypedAtomic:
		// Integers are promoted to double (untypedAtomic is cast to it):
		// exact only while every integer in the index fits a double.
		if idx.maxAbs > 1<<53 {
			return false, false
		}
		f := a.f
		if a.T == XSuntypedAtomic {
			c, err := CastTo(a, XSdouble)
			if err != nil {
				return false, false // FORG0001: ordinary path raises it
			}
			f = c.f
		}
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
			return false, true
		}
		_, hit = idx.ints[int64(f)]
		return hit, true
	}
	return false, false
}

// eqSlot is embedded in CompareExpr: the comparison's index cache.
type eqSlot struct{ eq atomic.Pointer[eqCache] }
