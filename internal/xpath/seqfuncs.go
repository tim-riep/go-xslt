package xpath

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
)

// stringsToSeq wraps a []string as a Sequence Object.
func stringsToSeq(ss []string) Object {
	items := make([]Item, len(ss))
	for i, s := range ss {
		items[i] = s
	}
	return FromItems(items)
}

// fnDistinctValues implements fn:distinct-values under eq semantics: values
// are atomized, two values are duplicates when they compare equal with eq
// (NaN equals NaN, a numeric and a string never compare equal —
// fn-distinct-values-mixed-args-014: (xs:float('NaN'), 'NaN') keeps both),
// durations of either subtype compare by value (distinct-duration-equal-1),
// and string-family values compare under the optional collation, whose URI
// must be supported (K2-SeqDistinctValuesFunc-1, FOCH0002).
func fnDistinctValues(c *Context, a []Object) (Object, error) {
	atoms, err := Atomize(arg(a, 0))
	if err != nil {
		return nil, err
	}
	var coll func(x, y string) int
	if len(a) >= 2 && !numIsEmpty(arg(a, 1)) {
		if coll, err = collationArg(a[1]); err != nil {
			return nil, err
		}
	} else {
		coll = ctxCollation(c)
	}
	// The kept values are BUCKETED so the pairwise eq scan only ever visits
	// candidates that could actually match. Without it a large input with many
	// distinct values is quadratic — 50,000 distinct dates cost 1.2 billion
	// dvEqual calls (strm/sf-distinct-values-001, a 5 MB document).
	//
	// A bucket is sound exactly when two eq-equal values always land in the
	// same one, and the type matrix dvEqual applies (cmpTypeError) makes that
	// provable for the two classes below; collisions merely make a bucket
	// bigger and change nothing. Everything else — dates, durations, booleans,
	// QNames, binary, and strings under a real collation, where two different
	// strings CAN be equal — stays on the linear list it was always on.
	//
	// A comparison involving an xs:float happens in FLOAT space, so xs:decimal
	// 1.2 and xs:float 1.2 are eq while their xs:double values differ
	// (fn-distinct-values-mixed-args-012). One xs:float anywhere in the input is
	// therefore enough to make the whole numeric class key on the narrower
	// value; without one, no comparison can ever narrow and the full double
	// precision keeps the buckets as fine as possible.
	narrow := false
	for _, it := range atoms {
		if x, ok := it.(*Atomic); ok && x.T == XSfloat {
			narrow = true
			break
		}
	}
	buckets := map[string][]*Atomic{}
	var other []*Atomic // the classes with no sound bucket key
	var out []Item
	for _, it := range atoms {
		x := it.(*Atomic)
		key, bucketed := dvBucketKey(x, coll, narrow)
		cands := other
		if bucketed {
			cands = buckets[key]
		}
		dup := false
		for _, y := range cands {
			if dvEqual(x, y, coll) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if bucketed {
			buckets[key] = append(cands, x)
		} else {
			other = append(other, x)
		}
		out = append(out, x)
	}
	return FromItems(out), nil
}

// dvBucketKey returns a key with the property that two values dvEqual reports
// EQUAL always share it, or bucketed=false when no such key is available for
// the value's class.
func dvBucketKey(x *Atomic, coll func(a, b string) int, narrow bool) (string, bool) {
	switch {
	case x.IsNumeric():
		// eq over numerics is value equality across every numeric type, so two
		// eq-equal numerics necessarily share the value they are compared AT —
		// xs:float's precision when the input holds one, xs:double's otherwise.
		// Narrowing only makes buckets coarser, never splits an equal pair; the
		// collation never enters the numeric branch of dvEqual, so this holds
		// with one set too.
		f := x.Float()
		if math.IsNaN(f) {
			return "n|NaN", true // NaN is eq only to NaN here, and never a key
		}
		if narrow {
			f = float64(float32(f))
		}
		if f == 0 {
			f = 0 // -0.0 eq 0.0, but their bit patterns differ
		}
		return "n|" + strconv.FormatFloat(f, 'b', -1, 64), true
	case coll == nil && isComparableAsString(x):
		// Under the codepoint collation two string-family values are eq iff
		// their lexical forms are identical. With a real collation they need
		// not be, so that case is left unbucketed.
		return "s|" + x.Lexical(), true
	}
	return "", false
}

// SameGroupingKey reports whether two atomic values count as "the same" for
// xsl:for-each-group grouping purposes: comparable via eq (dateTime/date/time
// compare by normalized instant, not lexical form), NaN equal to NaN, and a
// pair the eq operator cannot compare (e.g. xs:dateTime vs xs:integer) simply
// unequal rather than a dynamic error — exactly fn:distinct-values' semantics,
// which the grouping-key comparison rules are defined to match. An optional
// collation (nil = codepoint) governs string-family comparisons.
func SameGroupingKey(a, b *Atomic, coll func(x, y string) int) bool {
	return dvEqual(a, b, coll)
}

// dvEqual reports whether two atomic values are the same for distinct-values.
func dvEqual(a, b *Atomic, coll func(x, y string) int) bool {
	if a.IsNumeric() && b.IsNumeric() {
		an, bn := math.IsNaN(a.Float()), math.IsNaN(b.Float())
		if an || bn {
			return an && bn
		}
		cmp, ok := CompareAtomic(a, b)
		return ok && cmp == 0
	}
	if cmpTypeError("=", a, b) != nil {
		return false
	}
	if coll != nil && isComparableAsString(a) && isComparableAsString(b) {
		return coll(a.Lexical(), b.Lexical()) == 0
	}
	cmp, ok := atomicCompare(a, b)
	return ok && cmp == 0
}

func fnReverse(c *Context, a []Object) (Object, error) {
	items := Items(arg(a, 0))
	out := make([]Item, len(items))
	for i, it := range items {
		out[len(items)-1-i] = it
	}
	return FromItems(out), nil
}

// fnSubsequence selects the items at positions p with round($start) <= p <
// round($start) + round($length). The bounds stay in the double space so a NaN
// bound selects nothing and an INF length runs to the end (cbcl-subsequence-
// 003/005/017/024) — only the final slice indices are integers.
func fnSubsequence(c *Context, a []Object) (Object, error) {
	items := Items(arg(a, 0))
	start, err := doubleArg(c, arg(a, 1))
	if err != nil {
		return nil, err
	}
	lo := roundHalfUp(start)
	hi := math.Inf(1)
	if len(a) >= 3 {
		length, err := doubleArg(c, arg(a, 2))
		if err != nil {
			return nil, err
		}
		hi = lo + roundHalfUp(length)
	}
	if math.IsNaN(lo) || math.IsNaN(hi) {
		return Sequence{}, nil
	}
	first := math.Max(lo, 1)
	last := math.Min(hi-1, float64(len(items)))
	if first > last {
		return Sequence{}, nil
	}
	return FromItems(items[int(first)-1 : int(last)]), nil
}

// fnIndexOf implements fn:index-of under eq semantics: untypedAtomic compares
// as a string, a pair the eq operator cannot compare (4 vs "4", or a NaN
// operand) is simply unequal rather than an error, and string comparison
// honours the optional collation (K-SeqIndexOfFunc-3/4/7..11).
func fnIndexOf(c *Context, a []Object) (Object, error) {
	seq, err := Atomize(arg(a, 0))
	if err != nil {
		return nil, err
	}
	search, err := Atomize(arg(a, 1))
	if err != nil {
		return nil, err
	}
	if len(search) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: index-of search value must be a single atomic value")
	}
	var coll func(a, b string) int
	if len(a) >= 3 {
		if coll, err = collationArg(a[2]); err != nil {
			return nil, err
		}
	} else {
		coll = ctxCollation(c)
	}
	target := search[0].(*Atomic)
	var out []Item
	for i, it := range seq {
		x := it.(*Atomic)
		if cmpTypeError("=", x, target) != nil {
			continue
		}
		var equal bool
		if coll != nil && isComparableAsString(x) && isComparableAsString(target) {
			equal = coll(x.Lexical(), target.Lexical()) == 0
		} else {
			// CompareAtomic is exact for the decimal/integer tower
			// (fn-indexof-mix-args-013: decimals differing in the 27th digit).
			cmp, ok := CompareAtomic(x, target)
			equal = ok && cmp == 0
		}
		if equal {
			out = append(out, NewInteger(int64(i+1)))
		}
	}
	return FromItems(out), nil
}

// numRank orders numeric types for promotion: integer < decimal < float < double.
func numRank(t AtomType) int {
	switch {
	case t == XSdouble || t == XSuntypedAtomic:
		return 4
	case t == XSfloat:
		return 3
	case t == XSdecimal:
		return 2
	default:
		return 1 // integer family
	}
}

func rankType(r int) AtomType {
	switch r {
	case 4:
		return XSdouble
	case 3:
		return XSfloat
	case 2:
		return XSdecimal
	default:
		return XSinteger
	}
}

func castNumeric(a *Atomic, kind AtomType) *Atomic {
	switch kind {
	case XSinteger:
		return NewIntegerBig(new(big.Int).Set(a.i))
	case XSdecimal:
		return NewDecimal(toRat(a))
	case XSfloat:
		return NewFloat(a.Float())
	default:
		return NewDouble(a.Float())
	}
}

func sumNumeric(atoms []*Atomic, kind AtomType) *Atomic {
	switch kind {
	case XSinteger:
		s := new(big.Int)
		for _, a := range atoms {
			s.Add(s, a.i)
		}
		return NewIntegerBig(s)
	case XSdecimal:
		s := new(big.Rat)
		for _, a := range atoms {
			s.Add(s, toRat(a))
		}
		return NewDecimal(s)
	default:
		var s float64
		for _, a := range atoms {
			s += a.Float()
		}
		if kind == XSfloat {
			return NewFloat(s)
		}
		return NewDouble(s)
	}
}

// aggregate computes min/max/avg/sum with XPath numeric type promotion (the
// result carries the least-common numeric type), and min/max also work over any
// single comparable family (strings, dates, durations). zero is sum()'s optional
// default for the empty sequence.
// uniformDurationType returns the duration type shared by every atom, or
// ok=false if they are not all durations of a single family.
func uniformDurationType(atoms []*Atomic) (AtomType, bool) {
	t := atoms[0].T
	// Only the two concrete duration subtypes support sum/avg; the abstract
	// base xs:duration (with both month and second parts) does not (fn-sum-9).
	if t != XSdayTimeDuration && t != XSyearMonthDuration {
		return 0, false
	}
	for _, a := range atoms[1:] {
		if a.T != t {
			return 0, false
		}
	}
	return t, true
}

func aggregate(o Object, op string, zero Object, coll func(a, b string) int) (Object, error) {
	var atoms []*Atomic
	for _, it := range Items(o) {
		as, err := Atomize(it)
		if err != nil {
			return nil, err
		}
		for _, x := range as {
			if a, ok := x.(*Atomic); ok {
				// Rule 1 of every aggregate: untypedAtomic is cast to xs:double
				// — FORG0001 if it will not cast, and a double thereafter
				// (K-SeqMINFunc-36/37: min((untypedAtomic("3"), "a string"))).
				if a.T == XSuntypedAtomic {
					if a, err = CastTo(a, XSdouble); err != nil {
						return nil, err
					}
				}
				atoms = append(atoms, a)
			}
		}
	}
	if len(atoms) == 0 {
		if op == "sum" {
			if zero != nil {
				return zero, nil
			}
			return NewInteger(0), nil
		}
		return Sequence{}, nil
	}

	allNum := true
	for _, a := range atoms {
		if !a.IsNumeric() {
			allNum = false
			break
		}
	}
	if !allNum {
		if op == "sum" || op == "avg" {
			// sum/avg over a uniform duration family (all xs:dayTimeDuration or
			// all xs:yearMonthDuration): add componentwise, then divide by the
			// count for avg (fn-sum-1/3/4, fn-avg-mix-args-002).
			if dt, ok := uniformDurationType(atoms); ok {
				tot := Duration{}
				for _, a := range atoms {
					// Guard the month accumulator against int overflow — a huge
					// yearMonthDuration total is FODT0002 (cbcl-avg-003).
					if (a.dur.Months > 0 && tot.Months > math.MaxInt64-a.dur.Months) ||
						(a.dur.Months < 0 && tot.Months < math.MinInt64-a.dur.Months) {
						return nil, fmt.Errorf("err:FODT0002: duration overflow in %s", op)
					}
					tot.Months += a.dur.Months
					tot.Secs += a.dur.Secs
				}
				if op == "avg" {
					n := float64(len(atoms))
					tot.Months = int(math.Round(float64(tot.Months) / n))
					tot.Secs /= n
				}
				return NewDuration(dt, tot), nil
			}
			return nil, fmt.Errorf("err:FORG0006: %s of non-numeric values", op)
		}
		// min/max need an ORDERED type — the matrix the lt operator applies:
		// plain xs:duration, QName and the gregorians are not ordered, and the
		// two concrete duration subtypes only order within their own family
		// (fn-min/max-8/9, K-SeqMINFunc-38).
		best, hasString := atoms[0], false
		for _, a := range atoms {
			if cmpTypeError("<", a, a) != nil {
				return nil, fmt.Errorf("err:FORG0006: %s of unordered type %s", op, a.T)
			}
			hasString = hasString || isStringType(a.T)
		}
		for _, a := range atoms[1:] {
			if cmpTypeError("<", best, a) != nil {
				return nil, fmt.Errorf("err:FORG0006: %s of incomparable values %s and %s", op, best.T, a.T)
			}
			var cmp int
			var ok bool
			if coll != nil && isComparableAsString(best) && isComparableAsString(a) {
				cmp, ok = coll(best.Lexical(), a.Lexical()), true
			} else {
				cmp, ok = atomicCompare(best, a)
			}
			if !ok {
				return nil, fmt.Errorf("err:FORG0006: %s of incomparable values", op)
			}
			if (op == "min" && cmp > 0) || (op == "max" && cmp < 0) {
				best = a
			}
		}
		// An xs:anyURI among xs:string values is promoted to xs:string (fn-min/max-16).
		if best.T == XSanyURI && hasString {
			return NewString(best.s), nil
		}
		return best, nil
	}

	// numeric: promote to the common type.
	rank := 1
	hasNaN := false
	for _, a := range atoms {
		if r := numRank(a.T); r > rank {
			rank = r
		}
		if (a.T == XSdouble || a.T == XSfloat) && math.IsNaN(a.f) {
			hasNaN = true
		}
	}
	kind := rankType(rank)

	switch op {
	case "min", "max":
		if hasNaN {
			if kind == XSfloat {
				return NewFloat(math.NaN()), nil
			}
			return NewDouble(math.NaN()), nil
		}
		best := atoms[0]
		for _, a := range atoms[1:] {
			if op == "min" {
				if a.Float() < best.Float() {
					best = a
				}
			} else if a.Float() > best.Float() {
				best = a
			}
		}
		// Float/double promotion converts the winner; within the decimal/
		// integer tower subtype substitution keeps the item's own type
		// (fn-min/max-14/15: max((xs:positiveInteger, xs:unsignedShort)) is
		// the xs:unsignedShort item, which is also an xs:nonNegativeInteger).
		if kind == XSfloat || kind == XSdouble {
			return castNumeric(best, kind), nil
		}
		return best, nil
	case "sum":
		if len(atoms) == 1 {
			// A single value is its own sum, type included
			// (K2-SeqSUMFunc-4: sum(xs:unsignedShort) is an xs:unsignedShort).
			return atoms[0], nil
		}
		return sumNumeric(atoms, kind), nil
	default: // avg
		s := sumNumeric(atoms, kind)
		n := int64(len(atoms))
		switch {
		case isIntegerType(s.T):
			return NewDecimal(new(big.Rat).SetFrac(s.i, big.NewInt(n))), nil
		case s.T == XSdecimal:
			return NewDecimal(new(big.Rat).Quo(s.d, new(big.Rat).SetInt64(n))), nil
		case s.T == XSfloat:
			return NewFloat(s.f / float64(n)), nil
		default:
			return NewDouble(s.f / float64(n)), nil
		}
	}
}

// fnMinMax implements fn:min / fn:max with the optional $collation argument
// (an unsupported URI is FOCH0002 — K2-SeqMINFunc-4), falling back to the
// in-scope default collation when the argument is absent.
func fnMinMax(c *Context, op string, a []Object) (Object, error) {
	var coll func(a, b string) int
	if len(a) >= 2 {
		var err error
		if coll, err = collationArg(a[1]); err != nil {
			return nil, err
		}
	} else {
		coll = ctxCollation(c)
	}
	return aggregate(arg(a, 0), op, nil, coll)
}

// doubleArg converts an argument declared xs:double: a single numeric value
// (or an untypedAtomic that casts to one) is fine, anything else — a string,
// the empty sequence — is XPTY0004 (K2-SeqSubsequenceFunc-10).
func doubleArg(ctx *Context, o Object) (float64, error) {
	items, err := Atomize(o)
	if err != nil {
		return 0, err
	}
	if len(items) != 1 {
		// XSLT's own backwards-compatible processing (ctx.BC10) restores the
		// function conversion rules' first-item cardinality relaxation here
		// too (xpath-compat-0303's own "choosing first of multiple nodes").
		if ctx != nil && ctx.BC10 && len(items) > 1 {
			return ToNumber(items[0]), nil
		}
		return 0, fmt.Errorf("err:XPTY0004: expected a single xs:double argument, got %d items", len(items))
	}
	a := items[0].(*Atomic)
	if a.T == XSuntypedAtomic {
		if a, err = castToDouble(a, XSdouble); err != nil {
			return 0, err
		}
	}
	if !a.IsNumeric() {
		// Same BC10 relaxation as numFirstNumeric: a non-numeric argument
		// (e.g. a plain xs:string like '4' or '1.000') converts via
		// number() instead of raising a type error (xpath-compat-0401:
		// subsequence()'s second/third arguments being cast to double).
		if ctx != nil && ctx.BC10 {
			return ToNumber(a), nil
		}
		return 0, fmt.Errorf("err:XPTY0004: %s is not a valid xs:double argument", a.T)
	}
	return a.Float(), nil
}

// --- higher-order functions -------------------------------------------------

func asFunc(o Object) (*Function, bool) {
	switch v := firstItem(o).(type) {
	case *Function:
		return v, true
	case *Map, *Array:
		// Maps and arrays ARE function items of arity 1 (key lookup / index):
		// fn:for-each(("we","th"), $map) applies the map (map-get-100).
		item := v
		return &Function{Arity: 1, Call: func(args []Object) (Object, error) {
			return callItemAsFunction(item, args)
		}}, true
	}
	return nil, false
}

// hofFuncArg returns the function argument of a higher-order function: it
// must be exactly ONE callable item (a function, or a map/array applied as
// one) whose arity is the one the parameter type declares — a plural argument
// (fn-for-each-pair-033) or an arity mismatch (fold-left-010, for-each-pair-
// 901: deep-equal#3 where function(item(), item()) is required) is XPTY0004.
func hofFuncArg(fn string, o Object, arity int) (*Function, error) {
	items := Items(o)
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: fn:%s expects a single function item, got %d items", fn, len(items))
	}
	f, ok := asFunc(items[0])
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: fn:%s: the argument is not a function", fn)
	}
	if f.Arity != arity {
		return nil, fmt.Errorf("err:XPTY0004: fn:%s expects a function of arity %d, got arity %d", fn, arity, f.Arity)
	}
	return f, nil
}

func fnForEach(c *Context, a []Object) (Object, error) {
	fn, err := hofFuncArg("for-each", arg(a, 1), 1)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range Items(arg(a, 0)) {
		r, err := fn.Call([]Object{FromItems([]Item{it})})
		if err != nil {
			return nil, err
		}
		out = append(out, Items(r)...)
	}
	return FromItems(out), nil
}

func fnFilter(c *Context, a []Object) (Object, error) {
	pred, err := hofFuncArg("filter", arg(a, 1), 1)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range Items(arg(a, 0)) {
		r, err := pred.Call([]Object{FromItems([]Item{it})})
		if err != nil {
			return nil, err
		}
		// The predicate is declared function(item()) as xs:boolean, so its
		// result must be exactly one xs:boolean — a non-boolean result (e.g.
		// normalize-space#1's string) is a type error, not an EBV coercion
		// (filter-901); a map/array predicate returns the stored value.
		b, ok := singleBoolean(r)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: filter predicate must return xs:boolean")
		}
		if b {
			out = append(out, it)
		}
	}
	return FromItems(out), nil
}

// isCallableItem reports whether an item can be applied as a function — a
// function value, or a map/array (each callable with one argument).
func isCallableItem(it Item) bool {
	switch it.(type) {
	case *Function, *Map, *Array:
		return true
	}
	return false
}

// singleBoolean returns the boolean value of an object that is exactly one
// xs:boolean item, and ok=false otherwise.
func singleBoolean(o Object) (val, ok bool) {
	items := Items(o)
	if len(items) != 1 {
		return false, false
	}
	switch v := items[0].(type) {
	case bool:
		return v, true
	case *Atomic:
		if v.T == XSboolean {
			return v.Bool(), true
		}
	}
	return false, false
}

func fnFoldLeft(c *Context, a []Object) (Object, error) {
	fn, err := hofFuncArg("fold-left", arg(a, 2), 2)
	if err != nil {
		return nil, err
	}
	acc := arg(a, 1)
	for _, it := range Items(arg(a, 0)) {
		r, err := fn.Call([]Object{acc, FromItems([]Item{it})})
		if err != nil {
			return nil, err
		}
		acc = r
	}
	return acc, nil
}

func fnFoldRight(c *Context, a []Object) (Object, error) {
	fn, err := hofFuncArg("fold-right", arg(a, 2), 2)
	if err != nil {
		return nil, err
	}
	acc := arg(a, 1)
	items := Items(arg(a, 0))
	for i := len(items) - 1; i >= 0; i-- {
		r, err := fn.Call([]Object{FromItems([]Item{items[i]}), acc})
		if err != nil {
			return nil, err
		}
		acc = r
	}
	return acc, nil
}

func fnForEachPair(c *Context, a []Object) (Object, error) {
	fn, err := hofFuncArg("for-each-pair", arg(a, 2), 2)
	if err != nil {
		return nil, err
	}
	xs, ys := Items(arg(a, 0)), Items(arg(a, 1))
	n := len(xs)
	if len(ys) < n {
		n = len(ys)
	}
	var out []Item
	for i := 0; i < n; i++ {
		r, err := fn.Call([]Object{FromItems([]Item{xs[i]}), FromItems([]Item{ys[i]})})
		if err != nil {
			return nil, err
		}
		out = append(out, Items(r)...)
	}
	return FromItems(out), nil
}

// fnSort implements fn:sort($input [, $collation [, $key]]) with the F&O 3.1
// rules array:sort already applies (maKeySeqCompareColl): every item's key is
// the ATOMIZED result of $key (or the item itself), keys are compared as
// sequences with lt semantics — NaN sorts least and equals NaN, a shorter key
// is a prefix-less (fn-sort-17/18/19), untypedAtomic is a string — under the
// collation for string-family keys (fn-sort-collation-4/5), and an
// incomparable pair such as (1, "a") is XPTY0004 (fn-sort-error-1/2/3). The
// sort is stable.
func fnSort(c *Context, a []Object) (Object, error) {
	items := Items(arg(a, 0))
	var coll func(x, y string) int
	if len(a) >= 2 && !numIsEmpty(arg(a, 1)) {
		cf, err := collationArg(arg(a, 1))
		if err != nil {
			return nil, err
		}
		coll = cf
	}
	var keyFn *Function
	if len(a) >= 3 {
		fn, err := hofFuncArg("sort", arg(a, 2), 1)
		if err != nil {
			return nil, err
		}
		keyFn = fn
	}
	keys := make([][]Item, len(items))
	for i, it := range items {
		k := FromItems([]Item{it})
		if keyFn != nil {
			r, err := keyFn.Call([]Object{k})
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
	idx := make([]int, len(items))
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
	out := make([]Item, len(items))
	for i, k := range idx {
		out[i] = items[k]
	}
	return FromItems(out), nil
}

func parseFloatOK(s string) (float64, bool) {
	f := parseNumber(s)
	return f, !math.IsNaN(f)
}

// --- math: functions --------------------------------------------------------

// math1 wraps a single-argument math function: an empty first argument yields
// the empty sequence (the F&O math: functions take xs:double?), otherwise the
// double result (NaN/±Inf propagate naturally).
func math1(f func(float64) float64) coreFunc {
	return func(c *Context, a []Object) (Object, error) {
		if numIsEmpty(arg(a, 0)) {
			return Sequence{}, nil
		}
		return NewDouble(f(ToNumber(arg(a, 0)))), nil
	}
}

var mathFuncs = map[string]coreFunc{
	"pi":    func(c *Context, a []Object) (Object, error) { return NewDouble(math.Pi), nil },
	"sqrt":  math1(math.Sqrt),
	"sin":   math1(math.Sin),
	"cos":   math1(math.Cos),
	"exp":   math1(math.Exp),
	"log":   math1(math.Log),
	"log10": math1(math.Log10),
	"pow": func(c *Context, a []Object) (Object, error) {
		if numIsEmpty(arg(a, 0)) {
			return Sequence{}, nil
		}
		return NewDouble(math.Pow(ToNumber(arg(a, 0)), ToNumber(arg(a, 1)))), nil
	},
}

// --- map: functions ---------------------------------------------------------

// mapArg returns the single map an argument must hold ($map as map(*)):
// an empty or plural sequence, or a non-map item, is XPTY0004
// (map-get-906/map-contains-906: (map{}, map{..}) is not a map).
func mapArg(fn string, a []Object, i int) (*Map, error) {
	items := Items(arg(a, i))
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: map:%s requires exactly one map, got %d items", fn, len(items))
	}
	m, ok := items[0].(*Map)
	if !ok {
		return nil, fmt.Errorf("err:XPTY0004: map:%s requires a map", fn)
	}
	return m, nil
}

// mapKeyArg returns the single atomic key an argument must hold ($key as
// xs:anyAtomicType): nodes atomize; an empty or plural result is XPTY0004
// (map-get-901/902, map-contains-901/902).
func mapKeyArg(fn string, a []Object, i int) (Item, error) {
	items, err := Atomize(arg(a, i))
	if err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: map:%s requires exactly one atomic key, got %d items", fn, len(items))
	}
	return items[0], nil
}

var mapFuncs = map[string]coreFunc{
	"get": func(c *Context, a []Object) (Object, error) {
		m, err := mapArg("get", a, 0)
		if err != nil {
			return nil, err
		}
		k, err := mapKeyArg("get", a, 1)
		if err != nil {
			return nil, err
		}
		return m.Get(k), nil
	},
	"contains": func(c *Context, a []Object) (Object, error) {
		m, err := mapArg("contains", a, 0)
		if err != nil {
			return nil, err
		}
		k, err := mapKeyArg("contains", a, 1)
		if err != nil {
			return nil, err
		}
		return m.Contains(k), nil
	},
	"size": func(c *Context, a []Object) (Object, error) {
		if m, ok := firstItem(arg(a, 0)).(*Map); ok {
			return NewInteger(int64(m.Size())), nil
		}
		return float64(0), nil
	},
	"keys": func(c *Context, a []Object) (Object, error) {
		if m, ok := firstItem(arg(a, 0)).(*Map); ok {
			return FromItems(append([]Item{}, m.Keys()...)), nil
		}
		return Sequence{}, nil
	},
	"put": func(c *Context, a []Object) (Object, error) {
		m, err := mapArg("put", a, 0)
		if err != nil {
			return nil, err
		}
		k, err := mapKeyArg("put", a, 1)
		if err != nil {
			return nil, err
		}
		nm := m.Copy()
		nm.Put(k, arg(a, 2))
		return nm, nil
	},
	"remove": func(c *Context, a []Object) (Object, error) {
		m, err := mapArg("remove", a, 0)
		if err != nil {
			return nil, err
		}
		// $keys as xs:anyAtomicType*: every listed key is removed; absent
		// keys are ignored (map-remove-017/019).
		keys, err := Atomize(arg(a, 1))
		if err != nil {
			return nil, err
		}
		drop := map[string]bool{}
		for _, k := range keys {
			drop[mapKey(k)] = true
		}
		nm := NewMap()
		for _, k := range m.Keys() {
			if !drop[mapKey(k)] {
				nm.Put(k, m.Get(k))
			}
		}
		return nm, nil
	},
	"entry": func(c *Context, a []Object) (Object, error) {
		k, err := mapKeyArg("entry", a, 0)
		if err != nil {
			return nil, err
		}
		m := NewMap()
		m.Put(k, arg(a, 1))
		return m, nil
	},
	"merge": func(c *Context, a []Object) (Object, error) {
		if len(a) == 0 {
			return nil, fmt.Errorf("err:XPST0017: map:merge requires at least one argument")
		}
		// The optional $options map carries "duplicates":
		// use-first (default) | use-last | use-any | reject | combine.
		dup := "use-first"
		if len(a) >= 2 {
			// $options as map(*) — when supplied it must be exactly one map
			// (map-merge-026: an empty second argument is XPTY0004).
			opt, err := mapArg("merge", a, 1)
			if err != nil {
				return nil, err
			}
			if dv := opt.Get(NewString("duplicates")); len(Items(dv)) > 0 {
				dup = itemString(firstItem(dv))
				switch dup {
				case "use-first", "use-last", "use-any", "reject", "combine":
				default:
					return nil, fmt.Errorf("err:FOJS0005: invalid duplicates value %q", dup)
				}
			}
		}
		out := NewMap()
		for _, it := range Items(arg(a, 0)) {
			m, ok := it.(*Map)
			if !ok {
				continue
			}
			for _, k := range m.Keys() {
				if !out.Contains(k) {
					out.Put(k, m.Get(k))
					continue
				}
				switch dup {
				case "use-last", "use-any":
					out.Put(k, m.Get(k))
				case "reject":
					return nil, fmt.Errorf("err:FOJS0003: duplicate key on map:merge with duplicates=reject")
				case "combine":
					out.Put(k, FromItems(append(Items(out.Get(k)), Items(m.Get(k))...)))
				}
				// use-first: keep the existing entry.
			}
		}
		return out, nil
	},
	"for-each": func(c *Context, a []Object) (Object, error) {
		m, ok := firstItem(arg(a, 0)).(*Map)
		fn, ok2 := asFunc(arg(a, 1))
		if !ok || !ok2 {
			return Sequence{}, nil
		}
		var out []Item
		for _, k := range m.Keys() {
			r, err := fn.Call([]Object{FromItems([]Item{k}), m.Get(k)})
			if err != nil {
				return nil, err
			}
			out = append(out, Items(r)...)
		}
		return FromItems(out), nil
	},
}

// --- array: functions -------------------------------------------------------

var arrayFuncs = map[string]coreFunc{
	"size": func(c *Context, a []Object) (Object, error) {
		if ar, ok := firstItem(arg(a, 0)).(*Array); ok {
			return NewInteger(int64(ar.Size())), nil
		}
		return float64(0), nil
	},
	"get": func(c *Context, a []Object) (Object, error) {
		ar, ok := firstItem(arg(a, 0)).(*Array)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: array:get requires an array")
		}
		// $position is exactly one xs:integer (array-get-007: a plural
		// position is XPTY0004).
		i, err := sqIntegerArg("array:get", arg(a, 1))
		if err != nil {
			return nil, err
		}
		if i < 1 || i > ar.Size() {
			return nil, fmt.Errorf("err:FOAY0001: array index %d out of bounds [1,%d]", i, ar.Size())
		}
		return ar.Get(i), nil
	},
	"append": func(c *Context, a []Object) (Object, error) {
		ar, ok := firstItem(arg(a, 0)).(*Array)
		if !ok {
			return Sequence{}, nil
		}
		return NewArray(append(append([]Object{}, ar.Members()...), arg(a, 1))), nil
	},
	"flatten": func(c *Context, a []Object) (Object, error) {
		var out []Item
		var flat func(o Object)
		flat = func(o Object) {
			for _, it := range Items(o) {
				if ar, ok := it.(*Array); ok {
					for _, m := range ar.Members() {
						flat(m)
					}
				} else {
					out = append(out, it)
				}
			}
		}
		flat(arg(a, 0))
		return FromItems(out), nil
	},
	"join": func(c *Context, a []Object) (Object, error) {
		var members []Object
		for _, it := range Items(arg(a, 0)) {
			if ar, ok := it.(*Array); ok {
				members = append(members, ar.Members()...)
			}
		}
		return NewArray(members), nil
	},
	"for-each": func(c *Context, a []Object) (Object, error) {
		ar, ok := firstItem(arg(a, 0)).(*Array)
		if !ok {
			return Sequence{}, nil
		}
		fn, err := hofFuncArg("array:for-each", arg(a, 1), 1)
		if err != nil {
			return nil, err
		}
		members := make([]Object, ar.Size())
		for i, m := range ar.Members() {
			r, err := fn.Call([]Object{m})
			if err != nil {
				return nil, err
			}
			members[i] = r
		}
		return NewArray(members), nil
	},
}
