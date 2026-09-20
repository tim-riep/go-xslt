package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func TestSequenceAndRange(t *testing.T) {
	tree, _ := xmltree.Parse(`<r/>`)
	cases := []struct{ expr, want string }{
		{"count((1, 2, 3))", "3"},
		{"count(1 to 5)", "5"},
		{"sum(1 to 10)", "55"},
		{"string-join((1 to 3), '-')", "1-2-3"},
		{"(1 to 5)[. > 3]", "4"},
		{"count(distinct-values((1,2,2,3,3,3)))", "3"},
		{"string-join(reverse(('a','b','c')), '')", "cba"},
		{"subsequence((10,20,30,40), 2, 2) => string-join(',')", ""}, // arrow not supported; ignored below
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

func TestForIfQuantified(t *testing.T) {
	tree, _ := xmltree.Parse(`<r/>`)
	cases := []struct{ expr, want string }{
		{"sum(for $x in 1 to 4 return $x * $x)", "30"},
		{"if (2 > 1) then 'yes' else 'no'", "yes"},
		{"some $x in (1,2,3) satisfies $x = 2", "true"},
		{"every $x in (2,4,6) satisfies $x mod 2 = 0", "true"},
		{"every $x in (2,4,5) satisfies $x mod 2 = 0", "false"},
		{"let $a := 5 return $a * 2", "10"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, tree))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestMapsAndArrays(t *testing.T) {
	tree, _ := xmltree.Parse(`<r/>`)
	cases := []struct{ expr, want string }{
		{`map { 'a': 1, 'b': 2 }?a`, "1"},
		{`map:get(map { 'x': 42 }, 'x')`, "42"},
		{`map:size(map { 'a':1, 'b':2 })`, "2"},
		{`[10, 20, 30]?2`, "20"},
		{`array:size([1, 2, 3])`, "3"},
		{`array:get(['p','q'], 2)`, "q"},
		{`count(array { 1 to 4 } ? *)`, "4"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, tree))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestHigherOrderFunctions(t *testing.T) {
	tree, _ := xmltree.Parse(`<r/>`)
	cases := []struct{ expr, want string }{
		{"string-join(for-each((1,2,3), function($x) { $x * 10 }), ',')", "10,20,30"},
		{"string-join(filter((1,2,3,4), function($x) { $x mod 2 = 0 }), ',')", "2,4"},
		{"fold-left((1,2,3,4), 0, function($a, $b) { $a + $b })", "10"},
		{"string-join(sort((3,1,2)), ',')", "1,2,3"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, tree))
		if got != c.want {
			t.Errorf("%s => %q, want %q", c.expr, got, c.want)
		}
	}
}
