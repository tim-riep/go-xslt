package xpath

import (
	"math"
	"math/big"
	"math/rand"
	"strconv"
	"testing"
)

// ratDecimal is the exact form of a decimal, bypassing the compact one.
func ratDecimal(r *big.Rat) *Atomic { return &Atomic{T: XSdecimal, d: r} }

func decimalLexicals() []string {
	ls := []string{"0", "-0", "1", "-1", "0.5", "-0.5", "1.50", "0.001", "-0.001", "123456789012345678",
		"-123456789012345678", "999999999999999999", "9223372036854775807", "-9223372036854775808",
		"9223372036854775808", "0.000000000000000001", "1.000000000000000001", "12345.6789", "-12345.6789",
		"3.14159", "100", "1000000", "0.1", "0.2", "0.3", "2.675", "-2.675", "2.5", "-2.5", "0.25", "1.125",
		"99999999999999999.9", "1234567890123456789012", "0.12345678901234567890123", "1e2",
	}
	rnd := rand.New(rand.NewSource(13))
	for i := 0; i < 400; i++ {
		digits := rnd.Intn(22) + 1
		var b []byte
		if rnd.Intn(2) == 0 {
			b = append(b, '-')
		}
		point := rnd.Intn(digits + 1)
		for j := 0; j < digits; j++ {
			if j == point && j != 0 && j != digits {
				b = append(b, '.')
			}
			b = append(b, byte('0'+rnd.Intn(10)))
		}
		ls = append(ls, string(b))
	}
	return ls
}

// TestCompactDecimalMatchesRat checks that every operation on a decimal gives
// the same result through the compact form as through big.Rat.
func TestCompactDecimalMatchesRat(t *testing.T) {
	ls := decimalLexicals()
	var vals []*Atomic
	var rats []*Atomic
	for _, l := range ls {
		r, ok := new(big.Rat).SetString(l)
		if !ok {
			continue
		}
		a, ok := NewDecimalFromString(l)
		if !ok {
			t.Fatalf("NewDecimalFromString(%q) rejected", l)
		}
		rr := ratDecimal(r)
		if a.Lexical() != rr.Lexical() {
			t.Errorf("Lexical(%q): %q vs %q", l, a.Lexical(), rr.Lexical())
		}
		if a.Float() != rr.Float() {
			t.Errorf("Float(%q): %v vs %v", l, a.Float(), rr.Float())
		}
		if a.rat().Cmp(r) != 0 || a.rat().RatString() != r.RatString() {
			t.Errorf("rat(%q): %s vs %s", l, a.rat().RatString(), r.RatString())
		}
		if mapKey(a) != mapKey(rr) {
			t.Errorf("mapKey(%q)", l)
		}
		if m, s, ok := decFromRat(r); ok {
			if got := newDecFast(m, s); got.Lexical() != rr.Lexical() {
				t.Errorf("decFromRat(%q): %q", l, got.Lexical())
			}
		} else if a.d == nil {
			t.Errorf("decFromRat(%q) failed but the lexical parsed compact", l)
		}
		vals = append(vals, a)
		rats = append(rats, rr)
	}
	same := func(what string, x, y Object, err1, err2 error) {
		if (err1 == nil) != (err2 == nil) {
			t.Errorf("%s: err %v vs %v", what, err1, err2)
			return
		}
		if err1 != nil {
			return
		}
		xa, ya := x.(*Atomic), y.(*Atomic)
		if xa.T != ya.T || xa.Lexical() != ya.Lexical() {
			t.Errorf("%s: %s %s vs %s %s", what, xa.T, xa.Lexical(), ya.T, ya.Lexical())
		}
		if xa.T == XSdecimal && xa.rat().Cmp(ya.rat()) != 0 {
			t.Errorf("%s: value %s vs %s", what, xa.rat(), ya.rat())
		}
	}
	for i, a := range vals {
		ra := rats[i]
		for _, p := range []int{0, 1, 2, 3, -1, -2, 5, 18, 20, -20} {
			x, e1 := fnRoundT(nil, []Object{a, NewInteger(int64(p))})
			y, e2 := fnRoundT(nil, []Object{ra, NewInteger(int64(p))})
			same("round("+ls[i]+","+strconv.Itoa(p)+")", x, y, e1, e2)
		}
		for name, f := range map[string]func(*Context, []Object) (Object, error){"abs": fnAbsT, "floor": fnFloorT, "ceiling": fnCeilingT} {
			x, e1 := f(nil, []Object{a})
			y, e2 := f(nil, []Object{ra})
			same(name+"("+a.Lexical()+")", x, y, e1, e2)
		}
		x, e1 := evalUnaryNeg(a)
		y, e2 := evalUnaryNeg(ra)
		same("-"+a.Lexical(), x, y, e1, e2)
		ci, e1 := CastTo(a, XSinteger)
		cj, e2 := CastTo(ra, XSinteger)
		same("integer("+a.Lexical()+")", ci, cj, e1, e2)
		cd, _ := CastTo(a, XSdouble)
		ce, _ := CastTo(ra, XSdouble)
		same("double("+a.Lexical()+")", cd, ce, nil, nil)
		for j, b := range vals {
			rb := rats[j]
			for _, op := range []string{"+", "-", "*", "div", "idiv", "mod"} {
				x, e1 := numericArith(op, a, b)
				y, e2 := numericArith(op, ra, rb)
				same(a.Lexical()+" "+op+" "+b.Lexical(), x, y, e1, e2)
			}
			c1, ok1 := atomicCompare(a, b)
			c2, ok2 := atomicCompare(ra, rb)
			if c1 != c2 || ok1 != ok2 {
				t.Errorf("compare %s %s: %d/%v vs %d/%v", a.Lexical(), b.Lexical(), c1, ok1, c2, ok2)
			}
			if c1, _ := CompareAtomic(a, b); c1 != ra.rat().Cmp(rb.rat()) {
				t.Errorf("CompareAtomic %s %s", a.Lexical(), b.Lexical())
			}
			// sum and avg: the compact running sum and quotient against the Rat's.
			for _, op := range []string{"sum", "avg"} {
				x, e1 := aggregate(Sequence{a, b, a}, op, nil, nil)
				y, e2 := aggregate(Sequence{ra, rb, ra}, op, nil, nil)
				same(op+"("+a.Lexical()+","+b.Lexical()+","+a.Lexical()+")", x, y, e1, e2)
			}
		}
	}
	// Integer operands and sums.
	ints := []*Atomic{NewInteger(0), NewInteger(7), NewInteger(-7), NewInteger(1 << 40), NewIntegerBig(new(big.Int).Lsh(big.NewInt(1), 70))}
	for i, a := range vals {
		for _, n := range ints {
			for _, op := range []string{"+", "-", "*", "div", "idiv", "mod"} {
				x, e1 := numericArith(op, a, n)
				y, e2 := numericArith(op, rats[i], n)
				same(a.Lexical()+" "+op+" "+n.Lexical(), x, y, e1, e2)
				x, e1 = numericArith(op, n, a)
				y, e2 = numericArith(op, n, rats[i])
				same(n.Lexical()+" "+op+" "+a.Lexical(), x, y, e1, e2)
			}
		}
	}
	// Float of a compact value against Rat.Float64 across magnitudes.
	for m := int64(1); m < 1<<62; m = m*3 + 1 {
		for s := 0; s <= decMaxScale; s++ {
			a := newDecFast(m, s)
			if a.d != nil {
				continue
			}
			want, _ := ratFromDec(m, s).Float64()
			if a.Float() != want {
				t.Errorf("Float(%d/10^%d) = %v, want %v", m, s, a.Float(), want)
			}
			if b := newDecFast(-m, s); b.Float() != -want {
				t.Errorf("Float(-%d/10^%d)", m, s)
			}
		}
	}
	if newDecFast(math.MinInt64, 3).Lexical() != "-9223372036854775.808" {
		t.Errorf("MinInt64 mantissa: %s", newDecFast(math.MinInt64, 3).Lexical())
	}
}

func evalUnaryNeg(a *Atomic) (Object, error) { return unaryArith(nil, a, true) }
