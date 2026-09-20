package xpath

import "testing"

func TestFnSeq(t *testing.T) {
	cases := []struct{ expr, want string }{
		// insert-before
		{"string-join(insert-before(('a','b','c'), 2, 'X'), ',')", "a,X,b,c"},
		{"string-join(insert-before(('a','b'), 1, ('X','Y')), ',')", "X,Y,a,b"},
		{"string-join(insert-before(('a','b'), 99, 'Z'), ',')", "a,b,Z"},
		{"string-join(insert-before(('a','b'), 0, 'Z'), ',')", "Z,a,b"},

		// remove
		{"string-join(remove(('a','b','c'), 2), ',')", "a,c"},
		{"string-join(remove(('a','b','c'), 1), ',')", "b,c"},
		{"string-join(remove(('a','b','c'), 99), ',')", "a,b,c"},
		{"string-join(remove(('a','b','c'), 0), ',')", "a,b,c"},

		// unordered
		{"string-join(unordered(('a','b','c')), ',')", "a,b,c"},

		// zero-or-one
		{"zero-or-one(())", ""},
		{"zero-or-one('a')", "a"},

		// one-or-more
		{"string-join(one-or-more(('a','b')), ',')", "a,b"},
		{"one-or-more('x')", "x"},

		// exactly-one
		{"exactly-one('only')", "only"},

		// deep-equal atomics
		{"deep-equal(('a','b'), ('a','b'))", "true"},
		{"deep-equal(('a','b'), ('a','c'))", "false"},
		{"deep-equal(('a','b'), ('a'))", "false"},
		{"deep-equal((1, 2, 3), (1, 2, 3))", "true"},
		{"deep-equal((1, 2), (1, 2, 3))", "false"},
		{"deep-equal((), ())", "true"},
		{"deep-equal(1, 'x')", "false"},
	}
	for _, c := range cases {
		if got := xpStr(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestFnSeqNodes(t *testing.T) {
	xml := `<r><a><b>1</b></a><a><b>1</b></a><c><b>2</b></c></r>`
	cases := []struct{ expr, want string }{
		// two identical <a> subtrees are deep-equal
		{"deep-equal(/r/a[1], /r/a[2])", "true"},
		// <a> and <c> differ by name and content
		{"deep-equal(/r/a[1], /r/c)", "false"},
		// a node is deep-equal to itself
		{"deep-equal(/r/a[1], /r/a[1])", "true"},
		// node vs atomic
		{"deep-equal(/r/a[1], 'x')", "false"},
	}
	for _, c := range cases {
		if got := xpStrCtx(t, c.expr, xml); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
