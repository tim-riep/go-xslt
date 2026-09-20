package xpath

import "testing"

// TestEmptyNumericFns: fn:abs/floor/ceiling/round have signature
// ($arg as xs:double?) as xs:double?, so empty in → empty out (not NaN).
// Regression for EN16931 BR-CO false-negatives where xs:decimal(<absent>) fed
// a round()/abs() and the NaN then compared equal, masking the violation.
func TestEmptyNumericFns(t *testing.T) {
	for _, expr := range []string{
		"round(())", "abs(())", "floor(())", "ceiling(())",
		"round(() * 10 * 10) div 100",
	} {
		if got := xpStr(t, expr); got != "" {
			t.Errorf("%s = %q, want empty sequence", expr, got)
		}
	}
}

// TestNaNComparisons: NaN is unordered — every comparison is false except "!=".
func TestNaNComparisons(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"number('x') = 1", "false"},
		{"1 = number('x')", "false"},
		{"number('x') != 1", "true"},
		{"number('x') < 1", "false"},
		{"number('x') >= 1", "false"},
		{"number('x') eq 1", "false"},
		{"number('x') ne 1", "true"},
		{"number('x') lt 1", "false"},
		// the BR-CO-13 shape: a present total compared to round() of an absent operand
		{"1436.5 = round(() * 10 * 10) div 100", "false"},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
