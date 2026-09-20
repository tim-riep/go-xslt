package xpath

import (
	"math/big"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func TestAtomicLexical(t *testing.T) {
	cases := []struct {
		a    *Atomic
		want string
	}{
		{NewInteger(42), "42"},
		{NewInteger(-7), "-7"},
		{NewDecimal(big.NewRat(3, 2)), "1.5"},
		{NewDecimal(big.NewRat(10, 1)), "10"},
		{NewDouble(0), "0"},
		{NewDouble(1.5), "1.5"},
		{NewDouble(1e22), "1.0E22"},
		{NewBool(true), "true"},
		{NewString("hi"), "hi"},
		{NewBinary(XShexBinary, []byte{0x0f, 0xa0}), "0FA0"},
	}
	for _, c := range cases {
		if got := c.a.Lexical(); got != c.want {
			t.Errorf("Lexical(%v) = %q, want %q", c.a.T, got, c.want)
		}
	}
}

func TestCastTo(t *testing.T) {
	cases := []struct {
		in     Item
		target AtomType
		want   string
		err    bool
	}{
		{NewString("5"), XSinteger, "5", false},
		{NewString("5.5"), XSinteger, "", true}, // a decimal LEXICAL is not a valid integer (FORG0001)
		{NewInteger(5), XSdouble, "5", false},
		{NewDouble(3.9), XSinteger, "3", false},
		{NewString("3.14"), XSdecimal, "3.14", false},
		{NewString("true"), XSboolean, "true", false},
		{NewString("0"), XSboolean, "false", false},
		{NewInteger(1), XSboolean, "true", false},
		{NewString("2004-01-15"), XSdate, "2004-01-15", false},
		{NewString("2004-01-15T12:30:00"), XSdateTime, "2004-01-15T12:30:00", false},
		{NewString("P1Y2M"), XSyearMonthDuration, "P1Y2M", false},
		{NewString("PT1H30M"), XSdayTimeDuration, "PT1H30M", false},
		{NewInteger(255), XSstring, "255", false},
		{NewString("notanumber"), XSinteger, "", true},
		{NewString("xyz"), XSboolean, "", true},
	}
	for _, c := range cases {
		got, err := CastTo(c.in, c.target)
		if c.err {
			if err == nil {
				t.Errorf("CastTo(%v,%v) expected error", c.in, c.target)
			}
			continue
		}
		if err != nil {
			t.Errorf("CastTo(%v,%v) error: %v", c.in, c.target, err)
			continue
		}
		if got.Lexical() != c.want {
			t.Errorf("CastTo(%v,%v) = %q, want %q", c.in, c.target, got.Lexical(), c.want)
		}
	}
}

func TestCastable(t *testing.T) {
	if !Castable(NewString("5"), XSinteger) {
		t.Error("'5' should be castable to integer")
	}
	if Castable(NewString("five"), XSinteger) {
		t.Error("'five' should not be castable to integer")
	}
}

func TestAtomizeNode(t *testing.T) {
	doc, _ := xmltree.Parse(`<a>hello</a>`)
	root := xmltree.RootElement(doc)
	items, err := Atomize(NodeSet{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 atomic, got %d", len(items))
	}
	a := items[0].(*Atomic)
	if a.T != XSuntypedAtomic || a.Lexical() != "hello" {
		t.Errorf("atomized node = %v %q", a.T, a.Lexical())
	}
}

func TestConstructor(t *testing.T) {
	res, ok, err := CallConstructor("integer", NewString("123"))
	if !ok || err != nil {
		t.Fatalf("constructor ok=%v err=%v", ok, err)
	}
	if ToString(res) != "123" {
		t.Errorf("xs:integer('123') = %q", ToString(res))
	}
}
