package xpath

import (
	"math"
	"math/rand"
	"testing"
)

// TestRoundFloatPrecZero checks roundFloatPrec's precision-0 shortcut against
// the exact big.Rat rounding it replaces, on halves, their neighbours and
// random doubles of every magnitude.
func TestRoundFloatPrecZero(t *testing.T) {
	var vals []float64
	for _, h := range []float64{0.5, 1.5, 2.5, -0.5, -1.5, -2.5, 1e15 + 0.5, -1e15 - 0.5, 0.25, -0.25, 1e-300, -1e-300, 4503599627370495.5, -4503599627370495.5} {
		vals = append(vals, h, math.Nextafter(h, math.Inf(1)), math.Nextafter(h, math.Inf(-1)))
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 200000; i++ {
		vals = append(vals, (r.Float64()-0.5)*math.Pow(10, float64(r.Intn(34)-16)))
	}
	for _, f := range vals {
		if f == math.Trunc(f) {
			continue
		}
		got, want := roundFloatPrec(f, 0), roundFloatPrecExact(f, 0)
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("round(%v): %v, want %v", f, got, want)
		}
	}
}
