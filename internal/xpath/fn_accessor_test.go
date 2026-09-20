package xpath

import "testing"

func TestFnAccessor(t *testing.T) {
	cases := []struct{ expr, want string }{
		// data: atomizes its argument.
		{"data('abc')", "abc"},
		{"data(42)", "42"},
		{"string-join(data((1, 2, 3)), ',')", "1,2,3"},

		// base-uri / document-uri: always the empty sequence.
		{"base-uri()", ""},
		{"document-uri()", ""},

		// node-name on a node without a name yields the empty sequence.
		{"node-name()", ""},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestFnAccessorCtx(t *testing.T) {
	const xml = `<r xmlns:p="urn:x"><p:a/><b/></r>`
	cases := []struct{ expr, want string }{
		// node-name returns the element's QName.
		{"node-name(/r)", "r"},
		{"node-name(/r/b)", "b"},
		{"node-name(/r/*[1])", "p:a"},

		// no node / unnamed node -> empty sequence.
		{"node-name(/r/text())", ""},

		// nilled is always false for an existing node, empty when absent.
		{"nilled(/r)", "false"},
		{"nilled(/r/nope)", ""},

		// data on a node atomizes to its string value.
		{"data(/r/b)", ""},

		// base-uri / document-uri remain empty even for real nodes.
		{"base-uri(/r)", ""},
		{"document-uri(/r)", ""},
	}
	for _, c := range cases {
		if got := xpStrCtx(t, c.expr, xml); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
