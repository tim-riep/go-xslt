package xpath

import "testing"

func TestFnNode(t *testing.T) {
	const doc = `<root><a><b>1</b><b>2</b></a><c>x</c></root>`

	cases := []struct {
		expr, xml, want string
	}{
		// has-children: context node is the document, which has the root child.
		{"has-children()", doc, "true"},
		{"has-children(/root/c)", doc, "true"}, // <c>x</c> has a text-node child
		{"has-children(/root/a)", doc, "true"},

		// innermost: keep the deepest nodes (no input node is their descendant).
		// Input is root plus its descendant b elements; innermost are the b's.
		{"string-join(for-each(innermost((/root, /root/a/b)), string#1), ',')", doc, "1,2"},

		// outermost: keep the topmost (no input node is an ancestor).
		{"string-join(for-each(outermost((/root, /root/a/b)), local-name#1), ',')", doc, "root"},

		// path: canonical path with positional predicates.
		{"path(/)", doc, "/"},
		{"path(/root)", doc, "/Q{}root[1]"},
		{"path(/root/a/b[2])", doc, "/Q{}root[1]/Q{}a[1]/Q{}b[2]"},
		{"path(/root/c)", doc, "/Q{}root[1]/Q{}c[1]"},
	}

	for _, c := range cases {
		if got := xpStrCtx(t, c.expr, c.xml); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
