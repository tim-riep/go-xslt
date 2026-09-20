package xpath

import "testing"

func TestFnHof(t *testing.T) {
	cases := []struct{ expr, want string }{
		// function-arity: inline function with two params.
		{"function-arity(function($a, $b) { $a + $b })", "2"},
		{"function-arity(function($x) { $x })", "1"},
		{"function-arity(function() { 1 })", "0"},

		// function-name: empty sequence for inline functions.
		{"function-name(function($a) { $a })", ""},
		{"empty(function-name(function($a) { $a }))", "true"},

		// function-lookup: the name must be an xs:QName; a resolvable name
		// returns the function, which can then be applied.
		{"function-lookup(fn:QName('http://www.w3.org/2005/xpath-functions','abs'), 1)(-3)", "3"},

		// apply: spread array members as arguments.
		{"apply(function($a, $b) { $a + $b }, [3, 4])", "7"},
		{"apply(function($x) { $x * 2 }, [21])", "42"},
		{"apply(concat#2, ['foo', 'bar'])", "foobar"},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
