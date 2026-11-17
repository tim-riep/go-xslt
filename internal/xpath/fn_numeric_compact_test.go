package xpath

import (
	"math/big"
	"testing"
)

// TestFormatNumberCompactMatchesRat formats many values through the compact
// path and through the Rat path and expects the same text.
func TestFormatNumberCompactMatchesRat(t *testing.T) {
	pictures := []string{"#.##0,00", "0.00", "#,##0.###", "0", "#", "#.#", "000.000", "0.0%", "#‰", "#,###,###.##########",
		"0.000000000000000000", "#.#######################", "00,00,000.00", "#.#,##,#", "0.0e0", "00.000E00", "#0.0#"}
	de := DecimalFormat{DecimalSep: ',', GroupingSep: '.', MinusSign: '-', Percent: '%', PerMille: '‰', ZeroDigit: '0', Digit: '#', PatternSep: ';', Infinity: "Infinity", NaN: "NaN", ExponentSep: 'e'}
	var dfs []DecimalFormat
	dfs = append(dfs, DefaultDecimalFormat(), de)
	for _, l := range decimalLexicals() {
		r, ok := new(big.Rat).SetString(l)
		if !ok {
			continue
		}
		a, _ := NewDecimalFromString(l)
		for _, df := range dfs {
			for _, pic := range pictures {
				if err := validatePicture(pic, df); err != nil {
					continue
				}
				got := formatNumberValue(fmtNumInput(a), pic, df)
				exact := fmtNum{abs: new(big.Rat).Abs(r), neg: r.Sign() < 0}
				want := formatNumberValue(exact, pic, df)
				if got != want {
					t.Errorf("format-number(%s, %q): %q vs %q", l, pic, got, want)
				}
			}
		}
	}
	for _, n := range []int64{0, 1, -1, 7, 12345, -999999, 1 << 62, -(1 << 62)} {
		for _, pic := range pictures {
			a := NewInteger(n)
			got := formatNumberValue(fmtNumInput(a), pic, DefaultDecimalFormat())
			want := formatNumberValue(fmtNum{abs: new(big.Rat).SetInt(new(big.Int).Abs(big.NewInt(n))), neg: n < 0}, pic, DefaultDecimalFormat())
			if got != want {
				t.Errorf("format-number(%d, %q): %q vs %q", n, pic, got, want)
			}
		}
	}
}
