package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Explicit axis syntax (axis::test) must not be swallowed by the QName lexer:
// "parent::*" is parent + :: + *, not the single name "parent::".
func TestExplicitAxes(t *testing.T) {
	doc, _ := xmltree.Parse(`<root><a><b><c id="1">x</c></b></a></root>`)
	var c *xmltree.Node
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		if n.Name.Local == "c" {
			c = n
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(doc)

	cases := []struct{ expr, want string }{
		{"name(parent::*)", "b"},
		{"count(ancestor::*)", "3"},         // a, b, root
		{"count(ancestor-or-self::*)", "4"}, // + c
		{"count(ancestor::a)", "1"},
		{"boolean(ancestor::a)", "true"},
		{"not(ancestor::nope)", "true"},
		{"not(ancestor::a)", "false"},
		{"name(self::c)", "c"},
		{"count(child::*)", "0"},
		{"count(preceding::*)", "0"},
	}
	for _, tc := range cases {
		p, err := Parse(tc.expr)
		if err != nil {
			t.Errorf("%s: parse error %v", tc.expr, err)
			continue
		}
		v, err := p.Eval(&Context{Node: c, Pos: 1, Size: 1})
		if err != nil {
			t.Errorf("%s: eval error %v", tc.expr, err)
			continue
		}
		if got := ToString(v); got != tc.want {
			t.Errorf("%s => %q, want %q", tc.expr, got, tc.want)
		}
	}
}
