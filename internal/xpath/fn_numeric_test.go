package xpath

import "testing"

func TestNumericFuncs(t *testing.T) {
	cases := []struct{ expr, want string }{
		// round-half-to-even: banker's rounding on decimals.
		{"fn:round-half-to-even(xs:decimal('0.5'))", "0"},
		{"fn:round-half-to-even(xs:decimal('1.5'))", "2"},
		{"fn:round-half-to-even(xs:decimal('2.5'))", "2"},
		{"fn:round-half-to-even(xs:decimal('3.5'))", "4"},
		{"fn:round-half-to-even(xs:decimal('-2.5'))", "-2"},
		// round-half-to-even with precision.
		{"fn:round-half-to-even(xs:decimal('2.125'), 2)", "2.12"},
		{"fn:round-half-to-even(xs:decimal('2.135'), 2)", "2.14"},
		// integer input is preserved.
		{"fn:round-half-to-even(xs:integer('17'))", "17"},
		// negative precision on integer.
		{"fn:round-half-to-even(xs:integer('1250'), -2)", "1200"},
		// double input.
		{"fn:round-half-to-even(0.5e0)", "0"},
		{"fn:round-half-to-even(2.5e0)", "2"},

		// format-integer.
		{"fn:format-integer(123, '0')", "123"},
		{"fn:format-integer(7, '000')", "007"},
		{"fn:format-integer(123, '#')", "123"},
		{"fn:format-integer(-45, '000')", "-045"},
		{"fn:format-integer(1234567, '#,##0')", "1,234,567"},
		{"fn:format-integer(0, '0')", "0"},

		// math: trig functions.
		{"math:tan(0)", "0"},
		{"math:asin(0)", "0"},
		{"math:acos(1)", "0"},
		{"math:atan(0)", "0"},
		{"math:atan2(0, 1)", "0"},
	}

	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
