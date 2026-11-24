package xpath

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strconv"
)

// An integer-family Atomic has two forms, like a decimal (decimal.go). The
// compact one, with i == nil, holds the value as an int64 in the bits of f
// (an integer never uses f otherwise); the exact one holds a *big.Int in i,
// and is only ever built for a value outside int64, so nearly every integer
// met in practice — a position, a count, a sum, a parsed lexical — is
// compact, and its arithmetic, comparison, formatting and float conversion
// stay in int64, with every operation that could overflow checked and sent
// to big.Int. The hot paths read intFast first; bigInt hands any integer
// out as a *big.Int, allocating one for a compact value, and a caller that
// changes the result takes intCopy. The small-integer cache (smallInts)
// holds compact values.

// intFast returns a's value when a is a compact integer.
func (a *Atomic) intFast() (int64, bool) {
	if a.i != nil || !isIntegerType(a.T) {
		return 0, false
	}
	return int64(math.Float64bits(a.f)), true
}

// bigInt returns a's integer value: a's own *big.Int for the exact form,
// which the caller must not change, or a new one for a compact value.
func (a *Atomic) bigInt() *big.Int {
	if a.i != nil {
		return a.i
	}
	return big.NewInt(int64(math.Float64bits(a.f)))
}

// intCopy returns a's integer value as a *big.Int of the caller's own.
func (a *Atomic) intCopy() *big.Int {
	if a.i != nil {
		return new(big.Int).Set(a.i)
	}
	return big.NewInt(int64(math.Float64bits(a.f)))
}

// newIntFast builds the compact xs:integer n (a cached one when small).
func newIntFast(n int64) *Atomic {
	if n >= -16 && n < 1024 {
		return smallInts[n+16]
	}
	return &Atomic{T: XSinteger, f: math.Float64frombits(uint64(n))}
}

// newIntTyped builds an integer of type t (xs:integer or a derived type)
// holding z: compact when z fits an int64, else z itself.
func newIntTyped(t AtomType, z *big.Int) *Atomic {
	if z.IsInt64() {
		n := z.Int64()
		if t == XSinteger {
			return newIntFast(n)
		}
		return &Atomic{T: t, f: math.Float64frombits(uint64(n))}
	}
	return &Atomic{T: t, i: z}
}

// formatInt renders an integer in canonical form.
func (a *Atomic) formatInt() string {
	if n, ok := a.intFast(); ok {
		return strconv.FormatInt(n, 10)
	}
	return a.i.String()
}

// addInt64, subInt64 and mulInt64 compute in int64; ok is false on overflow.
func addInt64(a, b int64) (int64, bool) {
	s := a + b
	return s, (a >= 0) != (b >= 0) || (s >= 0) == (a >= 0)
}

func subInt64(a, b int64) (int64, bool) {
	d := a - b
	return d, (a >= 0) == (b >= 0) || (d >= 0) == (a >= 0)
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, false
	}
	p := a * b
	return p, p/b == a
}

// integerArithFast is integerArith for two compact integers; ok is false
// when the result does not fit (or, for div, has no compact decimal), and
// the caller takes the big.Int path, which gives the same value.
func integerArithFast(op string, a, b int64) (v Object, ok bool, err error) {
	switch op {
	case "+":
		s, ok := addInt64(a, b)
		if !ok {
			return nil, false, nil
		}
		return newIntFast(s), true, nil
	case "-":
		d, ok := subInt64(a, b)
		if !ok {
			return nil, false, nil
		}
		return newIntFast(d), true, nil
	case "*":
		p, ok := mulInt64(a, b)
		if !ok {
			return nil, false, nil
		}
		return newIntFast(p), true, nil
	case "idiv":
		if b == 0 {
			return nil, true, fmt.Errorf("err:FOAR0001: integer division by zero")
		}
		if a == math.MinInt64 && b == -1 {
			return nil, false, nil
		}
		return newIntFast(a / b), true, nil // truncates toward zero, as big.Quo does
	case "mod":
		if b == 0 {
			return nil, true, fmt.Errorf("err:FOAR0001: integer division by zero")
		}
		return newIntFast(a % b), true, nil // the dividend's sign, as big.Rem gives
	case "div":
		if b == 0 {
			return nil, true, fmt.Errorf("err:FOAR0001: division by zero")
		}
		if d, ok := intDivFast(a, b); ok {
			return d, true, nil
		}
		return nil, false, nil
	}
	return nil, false, nil
}

// intDivFast is the xs:decimal a div b when that has a compact form: the
// quotient terminates (its denominator in lowest terms has no prime factor
// but 2 and 5) and its mantissa and scale fit. It gives exactly what
// NewDecimal(big.Rat a/b) gives then: the Rat is in lowest terms, so its
// mantissa, n * 10^s / d, is the normalized one — n and d coprime, d even
// or a multiple of five whenever s > 0 — and the magnitudes are checked the
// way decFromRat checks them.
func intDivFast(a, b int64) (*Atomic, bool) {
	m, s, ok := intDivRaw(a, b)
	if !ok {
		return nil, false
	}
	return newDecFast(m, s), true
}

// intDivRaw is intDivFast's quotient as a normalized mantissa and scale.
func intDivRaw(a, b int64) (m int64, s int, ok bool) {
	if a == 0 {
		return 0, 0, true
	}
	if a == math.MinInt64 || b == math.MinInt64 {
		return 0, 0, false
	}
	neg := (a < 0) != (b < 0)
	an, bn := uint64(a), uint64(b)
	if a < 0 {
		an = uint64(-a)
	}
	if b < 0 {
		bn = uint64(-b)
	}
	g := gcd64(an, bn)
	n, d := an/g, bn/g
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
	q := uint64(pow10i64[s]) / (bn / g) // exact: the denominator divides 10^s
	hi, lo := bits.Mul64(n, q)
	if hi != 0 || lo > math.MaxInt64 {
		return 0, 0, false
	}
	m = int64(lo)
	if neg {
		m = -m
	}
	return m, s, true
}
