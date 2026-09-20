package xpath

import (
	"bytes"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// evalCompareExpr evaluates a CompareExpr (value/general/node comparison).
func evalCompareExpr(n *CompareExpr, ctx *Context) (Object, error) {
	if n.Kind == compGeneral {
		// "$c = (32 to 55295)": a general comparison against a range is a
		// bounds test, not a walk over every integer the range denotes —
		// materializing it per call is what made normalize-unicode-008's
		// f:valid-character allocate 68GB of integers.
		if v, ok, err := rangeComparison(n, ctx); ok || err != nil {
			return v, err
		}
	}
	l, err := evalExpr(n.L, ctx)
	if err != nil {
		return nil, err
	}
	r, err := evalExpr(n.R, ctx)
	if err != nil {
		return nil, err
	}
	switch n.Kind {
	case compNode:
		return nodeComparison(n.Op, l, r)
	case compValue:
		return valueComparison(n.Op, l, r, ctx)
	default:
		return generalComparison(n.Op, l, r, ctx)
	}
}

// rangeComparison evaluates a general comparison one of whose operands is a
// RangeExpr without materializing the range: ok is false (and nothing has
// been evaluated except the range's own bounds) when the OTHER operand holds
// anything but numeric or untypedAtomic-as-double items, in which case the
// caller takes the ordinary path. The existential semantics are exact: for a
// numeric v and the integers from..to, "v = range" holds iff v is an integer
// in [from, to]; "v != range" iff some range member differs from v; the
// orderings compare v against the nearest bound. An empty range (from > to)
// makes every comparison false, as an empty sequence operand does.
func rangeComparison(n *CompareExpr, ctx *Context) (Object, bool, error) {
	if ctx != nil && ctx.BC10 {
		// This fast path's existential semantics assume XPath 2.0+ typing
		// throughout; BC10's own general-comparison rules (generalComparisonBC10)
		// and its first-item range-operand relaxation (rangeOperand) are
		// enough of a behavioral difference that it is simplest, and safe —
		// a BC10-scoped stylesheet's ranges are never the huge ones this
		// optimization exists for — to just decline it and materialize.
		return nil, false, nil
	}
	var rng *RangeExpr
	var other Expr
	rangeLeft := false
	if re, isRange := n.L.(*RangeExpr); isRange {
		rng, other, rangeLeft = re, n.R, true
	} else if re, isRange := n.R.(*RangeExpr); isRange {
		rng, other = re, n.L
	} else {
		return nil, false, nil
	}
	ov, err := evalExpr(other, ctx)
	if err != nil {
		return nil, false, err
	}
	items, err := Atomize(ov)
	if err != nil {
		return nil, false, err
	}
	vals := make([]float64, 0, len(items))
	for _, it := range items {
		a, isAtomic := it.(*Atomic)
		if !isAtomic {
			return nil, false, nil
		}
		switch {
		case a.IsNumeric():
			vals = append(vals, a.Float())
		case a.T == XSuntypedAtomic:
			c, cerr := CastTo(a, XSdouble)
			if cerr != nil {
				return nil, false, fmt.Errorf("err:FORG0001: %v", cerr)
			}
			vals = append(vals, c.Float())
		default:
			return nil, false, nil
		}
	}
	fromV, err := evalExpr(rng.From, ctx)
	if err != nil {
		return nil, false, err
	}
	toV, err := evalExpr(rng.To, ctx)
	if err != nil {
		return nil, false, err
	}
	from, fromOK, err := rangeOperand(ctx, fromV)
	if err != nil {
		return nil, false, err
	}
	to, toOK, err := rangeOperand(ctx, toV)
	if err != nil {
		return nil, false, err
	}
	if !fromOK || !toOK || from > to || len(vals) == 0 {
		return false, true, nil
	}
	lo, hi := float64(from), float64(to)
	// Orient the operator so v is always the left operand.
	op := n.Op
	if rangeLeft {
		switch op {
		case "<":
			op = ">"
		case ">":
			op = "<"
		case "<=":
			op = ">="
		case ">=":
			op = "<="
		}
	}
	for _, v := range vals {
		if math.IsNaN(v) {
			if op == "!=" {
				return true, true, nil
			}
			continue
		}
		var hit bool
		switch op {
		case "=":
			hit = v >= lo && v <= hi && v == math.Trunc(v)
		case "!=":
			hit = from != to || v != lo
		case "<":
			hit = v < hi
		case "<=":
			hit = v <= hi
		case ">":
			hit = v > lo
		case ">=":
			hit = v >= lo
		default:
			return nil, false, nil
		}
		if hit {
			return true, true, nil
		}
	}
	return false, true, nil
}

// ctxCollation returns the in-scope default collation comparator, or nil for
// the codepoint collation (ctx == nil, or no default-collation established).
func ctxCollation(ctx *Context) func(a, b string) int {
	if ctx == nil {
		return nil
	}
	return ctx.DefaultCollation
}

// nodeComparison implements is, <<, >>. Operands must be () or a SINGLE node
// (XPTY0004 otherwise — K2-NodeTest: `1 is 1` and multi-node operands fail).
func nodeComparison(op string, l, r Object) (Object, error) {
	ln, lok := ToNodeSet(l)
	rn, rok := ToNodeSet(r)
	if (!lok && len(Items(l)) > 0) || (!rok && len(Items(r)) > 0) {
		return nil, fmt.Errorf("err:XPTY0004: node comparison %s requires node operands", op)
	}
	if len(ln) == 0 || len(rn) == 0 {
		return Sequence{}, nil
	}
	if len(ln) > 1 || len(rn) > 1 {
		return nil, fmt.Errorf("err:XPTY0004: node comparison %s requires singleton operands", op)
	}
	a, b := ln[0], rn[0]
	// Nodes of DIFFERENT trees have an implementation-dependent but stable
	// relative order (nodeexpression31/47: every pair across the same two
	// documents must agree): order whole trees by root identity, then nodes
	// within a tree by document order.
	before := func(x, y *xmltree.Node) bool {
		if rx, ry := x.Root(), y.Root(); rx != ry {
			// Numbered trees order by creation, like every other
			// document-order comparison (docOrderBefore); a never-numbered
			// tree keeps the identity-based tie-break.
			if tx, ty := rx.Tree(), ry.Tree(); tx != 0 && ty != 0 {
				return tx < ty
			}
			return reflect.ValueOf(rx).Pointer() < reflect.ValueOf(ry).Pointer()
		}
		return docOrderBefore(x, y)
	}
	switch op {
	case "is":
		return a == b, nil
	case "<<":
		return before(a, b), nil
	case ">>":
		return before(b, a), nil
	}
	return false, fmt.Errorf("bad node comparison %q", op)
}

// valueComparison implements eq, ne, lt, le, gt, ge on single atomized values.
func valueComparison(op string, l, r Object, ctx *Context) (Object, error) {
	la, err := Atomize(l)
	if err != nil {
		return nil, err
	}
	ra, err := Atomize(r)
	if err != nil {
		return nil, err
	}
	if len(la) == 0 || len(ra) == 0 {
		return Sequence{}, nil
	}
	if len(la) > 1 || len(ra) > 1 {
		return nil, fmt.Errorf("err:XPTY0004: value comparison operand is not a singleton")
	}
	a0, b0 := la[0].(*Atomic), ra[0].(*Atomic)
	if err := cmpTypeError(valueOpToSym(op), a0, b0); err != nil {
		return nil, err
	}
	cmp, ok := atomicCompareColl(a0, b0, ctxCollation(ctx))
	if !ok {
		// NaN operands are not a type error: every comparison is false except
		// "ne", which is true (NaN ne x for all x).
		if a0.IsNumeric() && b0.IsNumeric() {
			return op == "ne", nil
		}
		return nil, fmt.Errorf("err:XPTY0004: incomparable types in value comparison")
	}
	return applyCmpOp(valueOpToSym(op), cmp), nil
}

// cmpTypeError reports the XPTY0004 an operator/type-pair combination
// deserves under the XPath 2.0+ operator mapping (op in symbol form). The
// engine's 1.0-era leniency silently compared or ignored these:
//   - cross-family pairs: string-vs-numeric ("1" = 1), boolean-vs-numeric …
//   - date/time values of DIFFERENT primitives (dateTime eq date)
//   - ORDER on plain xs:duration or mixed duration subtypes (only
//     yearMonth↔yearMonth and dayTime↔dayTime are ordered)
//   - ORDER on gregorians and QNames (eq/ne only)
//
// Binaries stay fully ordered (XPath 3.1 added their order relations).
func cmpTypeError(op string, a, b *Atomic) error {
	order := op == "<" || op == "<=" || op == ">" || op == ">="
	bad := func() error {
		return fmt.Errorf("err:XPTY0004: %s and %s are not comparable with %s", a.T, b.T, op)
	}
	an, bn := a.IsNumeric(), b.IsNumeric()
	if an || bn {
		if an != bn {
			return bad()
		}
		return nil
	}
	as, bs := isComparableAsString(a), isComparableAsString(b)
	if as || bs {
		if as != bs {
			return bad()
		}
		return nil
	}
	if a.T == XSboolean || b.T == XSboolean {
		if a.T != b.T {
			return bad()
		}
		return nil
	}
	if isDateTimeType(a.T) || isDateTimeType(b.T) {
		if !isDateTimeType(a.T) || !isDateTimeType(b.T) {
			return bad()
		}
		pa, pb := dtPrim(a.T), dtPrim(b.T)
		if pa != pb {
			return bad()
		}
		if order && pa != XSdateTime && pa != XSdate && pa != XStime {
			return bad() // gregorians: equality only
		}
		return nil
	}
	if isDurationType(a.T) || isDurationType(b.T) {
		if !isDurationType(a.T) || !isDurationType(b.T) {
			return bad()
		}
		if order &&
			!(a.T == XSyearMonthDuration && b.T == XSyearMonthDuration) &&
			!(a.T == XSdayTimeDuration && b.T == XSdayTimeDuration) {
			return bad()
		}
		return nil
	}
	if a.T == XSqname || b.T == XSqname || a.T == XSnotation || b.T == XSnotation {
		if a.T != b.T || order {
			return bad()
		}
		return nil
	}
	if a.T == XShexBinary || a.T == XSbase64Binary || b.T == XShexBinary || b.T == XSbase64Binary {
		if a.T != b.T {
			return bad()
		}
		return nil
	}
	return nil
}

// dtPrim maps a date/time type to its primitive (dateTimeStamp → dateTime).
func dtPrim(t AtomType) AtomType {
	if t == XSdateTimeStamp {
		return XSdateTime
	}
	return t
}

func valueOpToSym(op string) string {
	switch op {
	case "eq":
		return "="
	case "ne":
		return "!="
	case "lt":
		return "<"
	case "le":
		return "<="
	case "gt":
		return ">"
	case "ge":
		return ">="
	}
	return op
}

// generalComparison implements =, !=, <, <=, >, >= with existential semantics
// and untypedAtomic coercion.
func generalComparison(op string, l, r Object, ctx *Context) (Object, error) {
	if ctx != nil && ctx.BC10 {
		return generalComparisonBC10(op, l, r)
	}
	la, err := Atomize(l)
	if err != nil {
		return nil, err
	}
	ra, err := Atomize(r)
	if err != nil {
		return nil, err
	}
	coll := ctxCollation(ctx)
	for _, ai := range la {
		for _, bi := range ra {
			a := ai.(*Atomic)
			b := bi.(*Atomic)
			cc, cerr := coerceGeneralErr(a, b, ctx)
			if cerr != nil {
				return nil, cerr // an untyped operand that will not cast (FORG0001)
			}
			ca, cb := cc[0], cc[1]
			if err := cmpTypeError(op, ca, cb); err != nil {
				return nil, err // a typed incomparable pair is an error, not a no-match
			}
			cmp, ok := atomicCompareColl(ca, cb, coll)
			if !ok {
				// NaN is unequal to everything: a "!=" pair involving NaN is true.
				if op == "!=" && ca.IsNumeric() && cb.IsNumeric() {
					return true, nil
				}
				continue
			}
			if ToBool(applyCmpOp(op, cmp)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// coerceGeneral applies untypedAtomic coercion for general comparison: an
// untypedAtomic operand is cast to the type of the other operand (or string if
// both untyped / the other is also untyped).
func coerceGeneral(a, b *Atomic) (*Atomic, *Atomic) {
	c, _ := coerceGeneralErr(a, b, nil)
	return c[0], c[1]
}

// coerceGeneralErr applies general-comparison untypedAtomic coercion
// (XPath 3.1 §3.7.3): an untyped operand facing a NUMERIC operand casts to
// xs:double, facing any other type casts to THAT type, and two untyped
// operands compare as strings. A cast failure is returned as FORG0001 so the
// comparison raises it (xs:untypedAtomic("three") = 3).
func coerceGeneralErr(a, b *Atomic, ctx *Context) ([2]*Atomic, error) {
	au := a.T == XSuntypedAtomic
	bu := b.T == XSuntypedAtomic
	castTarget := func(other *Atomic) AtomType {
		switch {
		case other.IsNumeric():
			return XSdouble // numeric family: compare in the double space
		case isComparableAsString(other):
			return XSstring // string family compares in the string space
			// (untyped("1") = NCName("string") -> "1" vs "string" -> false)
		default:
			return other.T
		}
	}
	// An untyped operand facing an xs:QName casts through the expression's
	// own namespace context, so a prefixed lexical value resolves as it would
	// in a literal xs:QName("p:x") constructor (choose-0106: the same
	// comparison under three different xmlns:my bindings).
	castUntyped := func(u, other *Atomic) (*Atomic, error) {
		if other.T == XSqname && ctx != nil && ctx.NS != nil {
			v, err := constructQName(ctx, NewString(u.s))
			if err != nil {
				return nil, err
			}
			if q, ok := v.(*Atomic); ok {
				return q, nil
			}
		}
		return CastTo(u, castTarget(other))
	}
	switch {
	case au && bu:
		return [2]*Atomic{NewString(a.s), NewString(b.s)}, nil
	case au:
		c, err := castUntyped(a, b)
		if err != nil {
			return [2]*Atomic{a, b}, fmt.Errorf("err:FORG0001: %v", err)
		}
		return [2]*Atomic{c, b}, nil
	case bu:
		c, err := castUntyped(b, a)
		if err != nil {
			return [2]*Atomic{a, b}, fmt.Errorf("err:FORG0001: %v", err)
		}
		return [2]*Atomic{a, c}, nil
	}
	return [2]*Atomic{a, b}, nil
}

// atomicCompare returns -1/0/1 comparing two atomics, and ok=false if they are
// not comparable.
// ratValue returns the exact rational value of an integer- or decimal-typed
// atomic, avoiding the precision loss of Float() for large magnitudes.
func (a *Atomic) ratValue() (*big.Rat, bool) {
	switch {
	case isIntegerType(a.T) && a.i != nil:
		return new(big.Rat).SetInt(a.i), true
	case a.T == XSdecimal && a.d != nil:
		return a.d, true
	}
	return nil, false
}

// CompareAtomic orders two atomic values, returning (sign, comparable). It is
// exact for the integer/decimal tower (used by XSD facet bounds where float64
// rounding would corrupt 18-digit comparisons); otherwise it defers to the
// regular value comparison.
func CompareAtomic(a, b *Atomic) (int, bool) {
	if ra, ok := a.ratValue(); ok {
		if rb, ok := b.ratValue(); ok {
			return ra.Cmp(rb), true
		}
	}
	return atomicCompare(a, b)
}

func atomicCompare(a, b *Atomic) (int, bool) {
	switch {
	case isIntegerType(a.T) && isIntegerType(b.T) && a.i != nil && b.i != nil:
		// Two integers compare exactly, with no float conversion to allocate.
		return a.i.Cmp(b.i), true
	case a.IsNumeric() && b.IsNumeric():
		fa, fb := a.Float(), b.Float()
		// Numeric promotion: a decimal/integer facing an xs:float is promoted
		// to xs:float, i.e. rounded to single precision, so xs:float(1.01) eq
		// 1.01 holds (a double operand keeps both sides in double).
		if a.T == XSfloat && b.T != XSfloat && b.T != XSdouble {
			fb = float64(float32(fb))
		}
		if b.T == XSfloat && a.T != XSfloat && a.T != XSdouble {
			fa = float64(float32(fa))
		}
		if math.IsNaN(fa) || math.IsNaN(fb) {
			// NaN is unordered: not equal to anything (incl. itself) and has no
			// ordering. Report "not comparable" so callers apply NaN semantics.
			return 0, false
		}
		switch {
		case fa < fb:
			return -1, true
		case fa > fb:
			return 1, true
		default:
			return 0, true
		}
	case a.T == XSboolean && b.T == XSboolean:
		if a.b == b.b {
			return 0, true
		}
		if !a.b {
			return -1, true
		}
		return 1, true
	case isDateTimeType(a.T) && isDateTimeType(b.T):
		switch {
		case a.tm.Before(b.tm):
			return -1, true
		case a.tm.After(b.tm):
			return 1, true
		default:
			return 0, true
		}
	case isDurationType(a.T) && isDurationType(b.T):
		av := float64(a.dur.Months)*2629746 + a.dur.Secs
		bv := float64(b.dur.Months)*2629746 + b.dur.Secs
		switch {
		case av < bv:
			return -1, true
		case av > bv:
			return 1, true
		default:
			return 0, true
		}
	case (a.T == XSqname || a.T == XSnotation) && a.T == b.T:
		// QNames — and xs:NOTATION, which shares their value space — support
		// only eq/ne: equal iff namespace+local match. The prefix is not part
		// of the value.
		if a.qn.Space == b.qn.Space && a.qn.Local == b.qn.Local {
			return 0, true
		}
		return 1, true
	case (a.T == XShexBinary || a.T == XSbase64Binary) && a.T == b.T:
		return bytes.Compare(a.bin, b.bin), true
	case isComparableAsString(a) && isComparableAsString(b):
		return strings.Compare(a.Lexical(), b.Lexical()), true
	}
	return 0, false
}

// atomicCompareColl is atomicCompare with an optional default-collation
// override for the string-family branch (eq/ne/general comparison honor the
// in-scope default collation, collations-0108/0121..0124); a nil coll keeps
// the plain codepoint comparison for every other type pair.
func atomicCompareColl(a, b *Atomic, coll func(x, y string) int) (int, bool) {
	if coll != nil && isComparableAsString(a) && isComparableAsString(b) {
		return coll(a.Lexical(), b.Lexical()), true
	}
	return atomicCompare(a, b)
}

func isComparableAsString(a *Atomic) bool {
	return isStringType(a.T) || a.T == XSuntypedAtomic || a.T == XSanyURI
}

func applyCmpOp(op string, cmp int) Object {
	switch op {
	case "=":
		return cmp == 0
	case "!=":
		return cmp != 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	}
	return false
}

// --- legacy node-set comparison helpers (used by general comparison fast path
// for node operands; kept for the XPath-1.0-style call sites) -----------------

var _ = xmltree.KindElement

// --- XSLT 1.0 backwards-compatible general comparison (XPath 1.0 §3.4) -----
//
// A general comparison under BC10 (XSLT's own [xsl:]version<2.0, wired
// through xpath.Context.BC10 — see internal/xslt's inBackwardsCompatScope)
// reverts to the exact XPath 1.0 rule table instead of XPath 2.0+'s typed
// atomization/untypedAtomic-coercion rules, but keeps 1.0's own EXISTENTIAL
// semantics over a multi-item operand (backwards-033: a 5-item integer
// sequence general-compared against a 5-item string sequence still checks
// every pair, not just the first of each) — so the top level is the same
// nested loop generalComparison itself uses, over the two operands'
// unatomized items (a bare item may still be a node — an RTF/copied
// fragment — which is why the PER-PAIR rule below is what actually branches
// on kind, not a whole-operand classification).
func generalComparisonBC10(op string, l, r Object) (Object, error) {
	// A node-set — EMPTY included — compared against a boolean is
	// boolean(node-set) op otherBoolean, checked before the existential loop
	// below because an empty node-set contributes no items to it at all, and
	// existing over zero pairs would wrongly report "no match" instead of
	// the definite value boolean(()) = false demands (backwards-045's own
	// b1: $s/j, no "j" children anywhere, = false() is true).
	if ln, ok := ToNodeSet(l); ok && bc10ObjIsBoolean(r) {
		return bc10BoolCompare(op, len(ln) > 0, ToBool(r)), nil
	}
	if rn, ok := ToNodeSet(r); ok && bc10ObjIsBoolean(l) {
		return bc10BoolCompare(bc10FlipOp(op), len(rn) > 0, ToBool(l)), nil
	}
	for _, li := range Items(l) {
		for _, ri := range Items(r) {
			if bc10ItemCompare(op, li, ri) {
				return true, nil
			}
		}
	}
	return false, nil
}

// bc10ObjIsBoolean reports whether o's first item (BC10's lenient,
// first-item classification, matching every other conversion in this file)
// is boolean-kind.
func bc10ObjIsBoolean(o Object) bool {
	items := Items(o)
	if len(items) == 0 {
		return false
	}
	return bc10ItemKind(items[0]) == "bool"
}

// bc10ItemCompare implements the XPath 1.0 rule table (§3.4) for one pair of
// items: two nodes compare via their string-values (recursively — neither is
// a node any more, so = /!= do string equality and the relational operators
// number-convert, per the "otherwise" bucket below); a node paired with a
// boolean/number/string scalar converts the node's string-value through
// THAT scalar's own kind; two scalars apply boolean-priority for =/!=
// (boolean beats number beats string: 'false' = true() converts BOTH SIDES
// to boolean, giving true, since a non-empty string is always boolean-true)
// and always-numeric comparison for the relational operators, unconditional
// regardless of operand type ('10' > '2' is true: number('10')=10 >
// number('2')=2).
func bc10ItemCompare(op string, li, ri Item) bool {
	ln, lIsNode := li.(*xmltree.Node)
	rn, rIsNode := ri.(*xmltree.Node)
	switch {
	case lIsNode && rIsNode:
		return bc10CompareStrings(op, nodeStringValue(ln), nodeStringValue(rn))
	case lIsNode:
		return bc10NodeVsItem(op, ln, ri)
	case rIsNode:
		return bc10NodeVsItem(bc10FlipOp(op), rn, li)
	}
	return bc10ScalarCompare(op, li, ri)
}

// bc10NodeVsItem compares a single node against a non-node item, converting
// the node's string-value through the OTHER item's own kind. A lone node is
// always boolean-true (matching boolean(node-set) for any NON-EMPTY
// node-set — an empty one never reaches here, since it contributes no items
// to the existential loop above at all).
func bc10NodeVsItem(op string, n *xmltree.Node, other Item) bool {
	switch bc10ItemKind(other) {
	case "bool":
		return bc10BoolCompare(op, true, ToBool(other))
	case "number":
		return bc10NumCompare(op, ToNumber(nodeStringValue(n)), ToNumber(other))
	default:
		return bc10CompareStrings(op, nodeStringValue(n), ToString(other))
	}
}

// bc10ScalarCompare implements the "neither operand is a node" bucket for one
// pair of items: boolean priority for =/!=, unconditional numeric comparison
// otherwise.
func bc10ScalarCompare(op string, li, ri Item) bool {
	if op == "=" || op == "!=" {
		lk, rk := bc10ItemKind(li), bc10ItemKind(ri)
		switch {
		case lk == "bool" || rk == "bool":
			return bc10BoolCompare(op, ToBool(li), ToBool(ri))
		case lk == "number" || rk == "number":
			return bc10NumCompare(op, ToNumber(li), ToNumber(ri))
		}
		ls, rs := ToString(li), ToString(ri)
		if op == "=" {
			return ls == rs
		}
		return ls != rs
	}
	return bc10NumCompare(op, ToNumber(li), ToNumber(ri))
}

// bc10CompareStrings compares two already-stringified operands: = and != as
// strings, the relational operators numerically (both sides are plain
// strings here, never boolean-typed, so no boolean priority applies).
func bc10CompareStrings(op, a, b string) bool {
	switch op {
	case "=":
		return a == b
	case "!=":
		return a != b
	}
	return bc10NumCompare(op, ToNumber(a), ToNumber(b))
}

// bc10NumCompare compares two float64s per IEEE 754 — Go's own NaN semantics
// (every comparison false except !=, which is true) already match what
// XPath 1.0 requires, with no extra casing needed.
func bc10NumCompare(op string, a, b float64) bool {
	switch op {
	case "=":
		return a == b
	case "!=":
		return a != b
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

// bc10BoolCompare compares two bools via the numeric encoding (false=0,
// true=1), which gives = and != their obvious meaning and a well-defined
// (if rarely-exercised) ordering for the relational operators.
func bc10BoolCompare(op string, a, b bool) bool {
	af, bf := 0.0, 0.0
	if a {
		af = 1
	}
	if b {
		bf = 1
	}
	return bc10NumCompare(op, af, bf)
}

// bc10FlipOp swaps the operand order for a relational operator (used when
// the node-set operand is the RIGHT-hand side); = and != are symmetric.
func bc10FlipOp(op string) string {
	switch op {
	case "<":
		return ">"
	case ">":
		return "<"
	case "<=":
		return ">="
	case ">=":
		return "<="
	}
	return op
}

// bc10ItemKind classifies a single general-comparison item as "bool",
// "number" or "string" per XPath 1.0's three-way object model (a node is
// handled by the caller before this is ever consulted).
func bc10ItemKind(it Item) string {
	switch v := it.(type) {
	case bool:
		return "bool"
	case float64:
		return "number"
	case *Atomic:
		if v.T == XSboolean {
			return "bool"
		}
		if v.IsNumeric() {
			return "number"
		}
	}
	return "string"
}
