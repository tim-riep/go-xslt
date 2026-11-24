package xpath

import (
	"math"
	"math/big"
	"math/bits"
	"strconv"
)

// An xs:decimal Atomic has two forms. The exact one holds the value as a
// *big.Rat in d. The compact one, with d == nil, holds mant / 10^scale: the
// mantissa, an int64, in the bits of f (a decimal never uses f otherwise, and
// a field of its own would push Atomic past 64 bytes), the scale in ds, and
// is normalized — a nonzero scale never ends in a zero digit of the
// mantissa, and zero is (0, 0) — so two compact values are equal exactly
// when their fields are. Nearly every decimal met in practice (a lexical of
// up to 18 digits, a sum, a product, a quotient that terminates) is compact,
// and its arithmetic, comparison, formatting and float conversion stay in
// int64; whatever does not fit falls back to big.Rat, and NewDecimal takes a
// Rat whose value fits back to the compact form, so a Rat only ever lives
// where it is needed. rat() hands any decimal out as a Rat; it allocates one
// for a compact value, so the hot paths read decFast first.

// decMaxScale is the largest compact scale: 10^18 is the last power of ten
// in an int64.
const decMaxScale = 18

// decFast returns the compact mantissa and scale of a, and whether a is a
// compact decimal.
func (a *Atomic) decFast() (m int64, s int, ok bool) {
	if a.T != XSdecimal || a.d != nil {
		return 0, 0, false
	}
	return int64(math.Float64bits(a.f)), int(a.ds), true
}

// newDecFast builds the compact decimal m / 10^s, normalizing it.
func newDecFast(m int64, s int) *Atomic {
	m, s = decNormalize(m, s)
	return &Atomic{T: XSdecimal, f: math.Float64frombits(uint64(m)), ds: uint8(s)}
}

// decNormalize strips the trailing zero digits a nonzero scale allows.
func decNormalize(m int64, s int) (int64, int) {
	if m == 0 {
		return 0, 0
	}
	for s > 0 && m%10 == 0 {
		m /= 10
		s--
	}
	return m, s
}

// decFromRat finds the compact form of r, when its value has one: an
// integer in int64, or a fraction whose denominator divides a power of ten
// up to 10^18 with a mantissa in int64. r is in lowest terms, as every Rat
// is, so the mantissa it yields is already normalized (a factor 2 or 5 left
// in the denominator means the numerator has none of that factor).
func decFromRat(r *big.Rat) (m int64, s int, ok bool) {
	num := r.Num()
	if !num.IsInt64() {
		return 0, 0, false
	}
	n := num.Int64()
	den := r.Denom()
	if !den.IsUint64() {
		return 0, 0, false
	}
	d := den.Uint64()
	if d == 1 {
		return n, 0, true
	}
	twos := bits.TrailingZeros64(d)
	d >>= uint(twos)
	fives := 0
	for d%5 == 0 {
		d /= 5
		fives++
	}
	if d != 1 {
		return 0, 0, false // a repeating decimal
	}
	s = max(twos, fives)
	if s > decMaxScale {
		return 0, 0, false
	}
	q := uint64(pow10i64[s]) / den.Uint64() // exact: den divides 10^s
	an := uint64(n)
	if n < 0 {
		an = -an
	}
	hi, lo := bits.Mul64(an, q)
	if hi != 0 || lo > math.MaxInt64 {
		return 0, 0, false
	}
	m = int64(lo)
	if n < 0 {
		m = -m
	}
	return m, s, true
}

// rat returns the value of a decimal Atomic as a Rat, which is a's own for
// the exact form and a new one for the compact form.
func (a *Atomic) rat() *big.Rat {
	if a.d != nil {
		return a.d
	}
	return ratFromDec(int64(math.Float64bits(a.f)), int(a.ds))
}

// ratFromDec builds m / 10^s in lowest terms. The reduction is done in
// uint64 and the Rat is assembled from the two coprime parts, so Rat.norm's
// big GCD is never run.
func ratFromDec(m int64, s int) *big.Rat {
	r := new(big.Rat).SetInt64(m)
	if s == 0 {
		return r
	}
	am := uint64(m)
	if m < 0 {
		am = -am
	}
	d := uint64(pow10i64[s])
	g := gcd64(am, d)
	if g > 1 {
		if m < 0 {
			r.SetInt64(-int64(am / g))
		} else {
			r.SetInt64(int64(am / g))
		}
		d /= g
	}
	if d > 1 {
		// Denom is a reference to r's denominator once r is initialized (it
		// is, by SetInt64); setting it keeps r in lowest terms since am/g
		// and d are coprime.
		r.Denom().SetUint64(d)
	}
	return r
}

func gcd64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// decAlign brings two compact decimals to one scale. ok is false when the
// scaled mantissa would not fit.
func decAlign(ma int64, sa int, mb int64, sb int) (int64, int64, int, bool) {
	switch {
	case sa == sb:
		return ma, mb, sa, true
	case sa < sb:
		m, ok := decScale(ma, sb-sa)
		return m, mb, sb, ok
	default:
		m, ok := decScale(mb, sa-sb)
		return ma, m, sa, ok
	}
}

// decScale returns m * 10^k, or ok=false on overflow.
func decScale(m int64, k int) (int64, bool) {
	if k > decMaxScale {
		return 0, false
	}
	p := pow10i64[k]
	if m > math.MaxInt64/p || m < math.MinInt64/p {
		return 0, false
	}
	return m * p, true
}

// decAdd adds two compact decimals (sub negates mb first); ok is false where
// int64 does not suffice and the Rat path must be taken.
func decAdd(ma int64, sa int, mb int64, sb int) (*Atomic, bool) {
	sum, s, ok := decAddRaw(ma, sa, mb, sb)
	if !ok {
		return nil, false
	}
	return newDecFast(sum, s), true
}

// decAddRaw is decAdd's sum as a mantissa and scale, not normalized (a
// running sum is normalized once, at the end).
func decAddRaw(ma int64, sa int, mb int64, sb int) (sum int64, s int, ok bool) {
	a, b, s, ok := decAlign(ma, sa, mb, sb)
	if !ok {
		return 0, 0, false
	}
	sum = a + b
	if (sum > a) != (b > 0) { // overflow: the sign of the change is wrong
		return 0, 0, false
	}
	return sum, s, true
}

// decDiv divides two compact decimals: ma/10^sa over mb/10^sb is the
// quotient ma/mb (intDivRaw: compact only when it terminates) scaled by
// 10^(sb-sa). ok is false where that does not fit, or mb is zero (the Rat
// path raises the error), and decimalArith then does the same in big.Rat,
// whose NewDecimal gives the same compact value (a quotient's compact form
// is unique).
func decDiv(ma int64, sa int, mb int64, sb int) (*Atomic, bool) {
	if mb == 0 {
		return nil, false
	}
	m, s, ok := intDivRaw(ma, mb)
	if !ok {
		return nil, false
	}
	s += sa - sb
	if s < 0 {
		if m, ok = decScale(m, -s); !ok {
			return nil, false
		}
		s = 0
	}
	if s > decMaxScale {
		return nil, false
	}
	return newDecFast(m, s), true
}

// decIdiv is the xs:integer trunc(a div b) of two compact decimals: the
// mantissas aligned to one scale divide as integers (Go's quotient
// truncates toward zero, as big.Int.Quo does). ok is false where the
// alignment overflows or mb is zero.
func decIdiv(ma int64, sa int, mb int64, sb int) (*Atomic, bool) {
	a, b, _, ok := decAlign(ma, sa, mb, sb)
	if !ok || b == 0 || (a == math.MinInt64 && b == -1) {
		return nil, false
	}
	return newIntFast(a / b), true
}

// decMod is a - b * trunc(a div b) of two compact decimals: the remainder
// of the aligned mantissas (Go's % has the dividend's sign, as the exact
// definition gives) at their scale.
func decMod(ma int64, sa int, mb int64, sb int) (*Atomic, bool) {
	a, b, s, ok := decAlign(ma, sa, mb, sb)
	if !ok || b == 0 {
		return nil, false
	}
	if b == -1 || b == 1 {
		return newDecFast(0, 0), true
	}
	return newDecFast(a%b, s), true
}

// decMul multiplies two compact decimals; ok is false where the product or
// its scale does not fit.
func decMul(ma int64, sa int, mb int64, sb int) (*Atomic, bool) {
	aa, ab := uint64(ma), uint64(mb)
	if ma < 0 {
		aa = -aa
	}
	if mb < 0 {
		ab = -ab
	}
	hi, lo := bits.Mul64(aa, ab)
	if hi != 0 || lo > math.MaxInt64 {
		return nil, false
	}
	m := int64(lo)
	if (ma < 0) != (mb < 0) {
		m = -m
	}
	m, s := decNormalize(m, sa+sb)
	if s > decMaxScale {
		return nil, false
	}
	return newDecFast(m, s), true
}

// decFloat is the float64 nearest to m / 10^s, for the values where one
// division of two exact doubles gives it: IEEE division is correctly
// rounded, so it agrees with Rat.Float64 (round half to even) exactly.
func decFloat(m int64, s int) (float64, bool) {
	if m > 1<<53 || m < -(1<<53) {
		return 0, false
	}
	return float64(m) / float64(pow10i64[s]), true
}

// formatDecFast renders a compact decimal in canonical form, as
// formatDecimal does for a Rat of the same value.
func formatDecFast(m int64, s int) string {
	if s == 0 {
		return strconv.FormatInt(m, 10)
	}
	am := uint64(m)
	if m < 0 {
		am = -am
	}
	var dbuf [20]byte
	digits := strconv.AppendUint(dbuf[:0], am, 10)
	var buf [48]byte
	out := buf[:0]
	if m < 0 {
		out = append(out, '-')
	}
	if len(digits) <= s {
		out = append(out, '0', '.')
		for i := len(digits); i < s; i++ {
			out = append(out, '0')
		}
		out = append(out, digits...)
	} else {
		out = append(out, digits[:len(digits)-s]...)
		out = append(out, '.')
		out = append(out, digits[len(digits)-s:]...)
	}
	return string(out)
}

// decRoundScaled rounds a compact decimal half-up (toward +INF) to p places,
// as ratRoundScaled does; ok is false where it leaves int64. p may be
// negative (places before the point).
func decRoundScaled(m int64, s int, p int) (*Atomic, bool) {
	if p >= s {
		return newDecFast(m, s), true // nothing to round off
	}
	// Drop k = s - p digits: q = floor(m / 10^k + 1/2) exactly, in integers.
	k := s - p
	if k > decMaxScale {
		return nil, false
	}
	div := pow10i64[k]
	q := m / div // toward zero
	rem := m - q*div
	if rem < 0 {
		rem = -rem
	}
	// half-up: the fraction |rem|/div is >= 1/2 → away from zero for a
	// positive value; for a negative value a fraction of exactly 1/2 rounds
	// toward +INF, i.e. toward zero (floor(m/div + 1/2)).
	switch {
	case m >= 0:
		if 2*rem >= div {
			q++
		}
	default:
		if 2*rem > div {
			q--
		}
	}
	if p < 0 {
		// The result is q * 10^(-p), an integer value.
		v, ok := decScale(q, -p)
		if !ok {
			return nil, false
		}
		return newDecFast(v, 0), true
	}
	return newDecFast(q, p), true
}
