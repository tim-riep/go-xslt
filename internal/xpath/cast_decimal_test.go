package xpath

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"
)

// TestPlainDecimalMatchesSetString checks that every lexical plainDecimal
// accepts builds the Rat big.Rat.SetString builds, representation included.
func TestPlainDecimalMatchesSetString(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	cases := []string{"0", "-0", "+0", "1", "-1", "12.5", "-12.50", "0.1", "000.100", "123456789012345678",
		"1234567890123456789", "9999999999999999999", "0.000000000000000001", "-00012.0300", "5.", "+.5", ".5",
		"1..2", "1.", "--1", "1e3", "", "12345678901234567.8", "-123456789012345678"}
	for i := 0; i < 20000; i++ {
		n := r.Intn(20) + 1
		b := make([]byte, 0, 24)
		if r.Intn(3) == 0 {
			b = append(b, '-')
		}
		for j := 0; j < n; j++ {
			if j > 0 && r.Intn(6) == 0 {
				b = append(b, '.')
			}
			b = append(b, byte('0'+r.Intn(10)))
		}
		cases = append(cases, string(b))
	}
	taken := 0
	for _, s := range cases {
		mant, scale, ok := plainDecimal(s)
		want, wok := new(big.Rat).SetString(s)
		if !ok {
			continue
		}
		taken++
		if !wok {
			t.Fatalf("%q: plainDecimal accepts, SetString does not", s)
		}
		got := new(big.Rat).SetFrac64(mant, pow10i64[scale])
		if got.Cmp(want) != 0 || fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
			t.Fatalf("%q: got %v (%#v), want %v (%#v)", s, got, got, want, want)
		}
	}
	if taken < 5000 {
		t.Fatalf("only %d lexicals taken by plainDecimal", taken)
	}
}
