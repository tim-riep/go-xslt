package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

const doc = `<catalog>
  <book id="b1" cat="go">
    <title>The Go Programming Language</title>
    <price>40</price>
  </book>
  <book id="b2" cat="xml">
    <title>XSLT 3.0</title>
    <price>60</price>
  </book>
</catalog>`

func evalStr(t *testing.T, expr string, ctxNode *xmltree.Node) Object {
	t.Helper()
	p, err := Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	v, err := p.Eval(&Context{Node: ctxNode, Pos: 1, Size: 1})
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return v
}

func TestXPathBasics(t *testing.T) {
	tree, err := xmltree.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	root := tree // document node

	cases := []struct {
		expr string
		want string
	}{
		{"count(/catalog/book)", "2"},
		{"/catalog/book[1]/title", "The Go Programming Language"},
		{"/catalog/book[2]/price", "60"},
		{"/catalog/book[@id='b2']/title", "XSLT 3.0"},
		{"//title", "The Go Programming Language"},
		{"sum(//price)", "100"},
		{"/catalog/book[price > 50]/title", "XSLT 3.0"},
		{"count(//book[@cat='go'])", "1"},
		{"string-length('hello')", "5"},
		{"concat('a', '-', 'b')", "a-b"},
		{"substring('hello', 2, 3)", "ell"},
		{"normalize-space('  a   b ')", "a b"},
		{"translate('bar','abc','ABC')", "BAr"},
		{"2 + 3 * 4", "14"},
		{"(2 + 3) * 4", "20"},
		{"7 mod 3", "1"},
		{"not(1 = 2)", "true"},
		{"'a' = 'a' and 'b' != 'c'", "true"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, root))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestXPathPositionLast(t *testing.T) {
	tree, _ := xmltree.Parse(doc)
	got := ToString(evalStr(t, "/catalog/book[last()]/title", tree))
	if got != "XSLT 3.0" {
		t.Errorf("last() title = %q", got)
	}
}

func TestXPathContextRelative(t *testing.T) {
	tree, _ := xmltree.Parse(doc)
	root := xmltree.RootElement(tree)
	firstBook := root.Children[1] // [0] is whitespace text
	got := ToString(evalStr(t, "title", firstBook))
	if got != "The Go Programming Language" {
		t.Errorf("relative title = %q", got)
	}
}
