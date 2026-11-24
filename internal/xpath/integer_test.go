package xpath

import (
	"math"
	"math/big"
	"testing"
)

// TestCompactIntegerMatchesBig checks the compact int64 form of an integer
// against the big.Int form over the int64 boundaries pairwise: the value,
// its lexical, float and map key, each arithmetic operator (the fast path
// and the big.Int path must agree to the bit, overflow included), the
// comparison, and the integer div as a decimal.
func TestCompactIntegerMatchesBig(t *testing.T) {
	ns := []int64{0, 1, -1, 2, -2, 3, 7, 10, 100, 999, 1000, 1023, 1024, -16, -17, 12345, -12345,
		1 << 31, -1 << 31, 1<<31 - 1, 1<<32 + 1, 1 << 40, 3037000499, 3037000500, -3037000500,
		1e15, 1e18, 999999999999999999, math.MaxInt64, math.MinInt64, math.MaxInt64 - 1, math.MinInt64 + 1,
		1 << 62, -1 << 62, 6, 25, 125, 1 << 20, 5, 11}
	exact := func(n int64) *Atomic { return &Atomic{T: XSinteger, i: big.NewInt(n)} }
	for _, n := range ns {
		a, e := NewInteger(n), exact(n)
		if a.i != nil {
			t.Errorf("NewInteger(%d) is not compact", n)
		}
		if v, ok := a.intFast(); !ok || v != n {
			t.Errorf("intFast(%d) = %d, %v", n, v, ok)
		}
		if a.Lexical() != e.Lexical() || a.Float() != e.Float() || mapKey(a) != mapKey(e) {
			t.Errorf("%d: lexical %q/%q float %v/%v", n, a.Lexical(), e.Lexical(), a.Float(), e.Float())
		}
		if a.bigInt().Cmp(e.i) != 0 || a.intCopy().Cmp(e.i) != 0 {
			t.Errorf("bigInt(%d)", n)
		}
		if b := NewIntegerBig(big.NewInt(n)); b.i != nil || b.Lexical() != e.Lexical() {
			t.Errorf("NewIntegerBig(%d) not compact", n)
		}
		if c, err := CastTo(a, XSdecimal); err != nil || c.Lexical() != e.Lexical() {
			t.Errorf("cast %d to decimal: %v %v", n, c, err)
		}
	}
	// Values outside int64 stay exact.
	huge := new(big.Int).Lsh(big.NewInt(1), 70)
	if h := NewIntegerBig(huge); h.i == nil || h.Lexical() != huge.String() {
		t.Errorf("huge integer: %v", h)
	}
	same := func(what string, x, y Object, err1, err2 error) {
		if (err1 == nil) != (err2 == nil) {
			t.Errorf("%s: err %v vs %v", what, err1, err2)
			return
		}
		if err1 != nil {
			if err1.Error() != err2.Error() {
				t.Errorf("%s: err %v vs %v", what, err1, err2)
			}
			return
		}
		xa, ya := x.(*Atomic), y.(*Atomic)
		if xa.T != ya.T || xa.Lexical() != ya.Lexical() {
			t.Errorf("%s: %s %q vs %s %q", what, xa.T, xa.Lexical(), ya.T, ya.Lexical())
		}
		if xa.T == XSdecimal && (xa.d == nil) != (ya.d == nil) {
			t.Errorf("%s: decimal forms differ", what)
		}
	}
	for _, x := range ns {
		for _, y := range ns {
			for _, op := range []string{"+", "-", "*", "idiv", "mod", "div"} {
				r1, err1 := evalArith(nil, op, NewInteger(x), NewInteger(y))
				r2, err2 := integerArith(op, big.NewInt(x), big.NewInt(y))
				same(op, r1, r2, err1, err2)
				if err1 == nil {
					if ra, ok := r1.(*Atomic); ok && isIntegerType(ra.T) && ra.i != nil && ra.i.IsInt64() {
						t.Errorf("%d %s %d: result in int64 is not compact", x, op, y)
					}
				}
			}
			c1, _ := atomicCompare(NewInteger(x), NewInteger(y))
			if c2 := big.NewInt(x).Cmp(big.NewInt(y)); c1 != c2 {
				t.Errorf("compare %d %d: %d vs %d", x, y, c1, c2)
			}
			s1 := sumNumeric([]*Atomic{NewInteger(x), NewInteger(y)}, XSinteger)
			s2 := new(big.Int).Add(big.NewInt(x), big.NewInt(y))
			if s1.Lexical() != s2.String() {
				t.Errorf("sum %d %d: %s vs %s", x, y, s1.Lexical(), s2)
			}
		}
		u1, err1 := unaryArith(nil, NewInteger(x), true)
		u2 := new(big.Int).Neg(big.NewInt(x))
		if err1 != nil || u1.(*Atomic).Lexical() != u2.String() {
			t.Errorf("-%d: %v %v", x, u1, err1)
		}
	}
}
