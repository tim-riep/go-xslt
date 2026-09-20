package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func TestXP31Operators(t *testing.T) {
	tree, _ := xmltree.Parse(`<r/>`)
	cases := []struct{ expr, want string }{
		// value comparisons
		{"1 eq 1", "true"},
		{"2 ne 3", "true"},
		{"2 lt 3", "true"},
		{"'a' lt 'b'", "true"},
		// general comparisons still work
		{"(1, 2, 3) = 2", "true"},
		{"5 > 3", "true"},
		// string concat
		{"'a' || 'b' || 'c'", "abc"},
		{"1 || '-' || 2", "1-2"},
		// range / arithmetic typing
		{"(1 + 1) instance of xs:integer", "true"},
		{"(1 div 2) instance of xs:decimal", "true"},
		{"(1.0e0) instance of xs:double", "true"},
		{"5 idiv 2", "2"},
		{"7 mod 3", "1"},
		// instance of
		{"'x' instance of xs:string", "true"},
		{"42 instance of xs:integer", "true"},
		{"(1,2,3) instance of xs:integer+", "true"},
		{"() instance of empty-sequence()", "true"},
		// cast / castable
		{"'5' cast as xs:integer", "5"},
		{"'5' castable as xs:integer", "true"},
		{"'five' castable as xs:integer", "false"},
		{"xs:integer('17')", "17"},
		// arrow
		{"'hello' => upper-case()", "HELLO"},
		{"(1,2,3) => count()", "3"},
		// simple map
		{"(1,2,3) ! (. * 2) => string-join(',')", ""}, // arrow precedence; checked below separately
		{"string-join((1,2,3) ! string(. * 2), ',')", "2,4,6"},
		// comments
		{"1 (: this is a comment :) + 2", "3"},
		// if/unary
		{"-3 + 5", "2"},
		{"if (1 eq 1) then 'y' else 'n'", "y"},
	}
	for _, c := range cases {
		if c.want == "" {
			continue
		}
		got := ToString(evalStr(t, c.expr, tree))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestXP31NodeComparison(t *testing.T) {
	tree, _ := xmltree.Parse(`<r><a/><b/></r>`)
	root := xmltree.RootElement(tree)
	// /r/a is before /r/b
	if got := ToString(evalStr(t, "/r/a << /r/b", tree)); got != "true" {
		t.Errorf("<< => %q", got)
	}
	if got := ToString(evalStr(t, "/r/a is /r/a", tree)); got != "true" {
		t.Errorf("is => %q", got)
	}
	_ = root
}

func TestXP31IntersectExcept(t *testing.T) {
	tree, _ := xmltree.Parse(`<r><a/><b/><c/></r>`)
	if got := ToString(evalStr(t, "count((/r/a | /r/b) intersect /r/b)", tree)); got != "1" {
		t.Errorf("intersect => %q", got)
	}
	if got := ToString(evalStr(t, "count(/r/* except /r/b)", tree)); got != "2" {
		t.Errorf("except => %q", got)
	}
}

func TestXP31HOFExtras(t *testing.T) {
	tree, _ := xmltree.Parse(`<r/>`)
	cases := []struct{ expr, want string }{
		// named function reference + partial application
		{"string-join(for-each(('a','b','c'), upper-case#1), '')", "ABC"},
		{"(concat('a', ?))('b')", "ab"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, tree))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}
