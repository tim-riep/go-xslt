package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// TestBareNodeStringArgument checks the contract under which evalFunc hands a
// string parameter its one plain node as the bare *xmltree.Node: every
// built-in reads it exactly as it read the one-node set, in XPath 3.1 mode
// and in XSLT 1.0 compatibility mode alike.
func TestBareNodeStringArgument(t *testing.T) {
	doc, err := xmltree.Parse(`<r><a x="3" y=" hello  world ">hello world</a><n>2.5</n><m>1</m><m>2</m></r>`)
	if err != nil {
		t.Fatal(err)
	}
	ctxP, _ := Parse("/r/a")
	v, err := ctxP.Eval(&Context{Node: doc, Pos: 1, Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	a := v.(NodeSet)[0]
	for _, c := range []struct{ expr, want string }{
		{`string-length(.)`, "11"},
		{`string-length(@y)`, "14"},
		{`normalize-space(@y)`, "hello world"},
		{`upper-case(@y)`, " HELLO  WORLD "},
		{`contains(., 'wor')`, "true"},
		{`starts-with(@y, ' he')`, "true"},
		{`concat(., '-', @x, 'z')`, "hello world-3z"},
		{`string-join(@x, ',')`, "3"},
		{`string-join(../m, ',')`, "1,2"},
		{`string-join((@x, ../n), '+')`, "3+2.5"},
		{`substring(., 2, 3)`, "ell"},
		{`substring-after(@y, 'hello')`, "  world "},
		{`translate(@x, '3', '4')`, "4"},
		{`compare(@x, '3')`, "0"},
		{`tokenize(@y)`, "hello world"},
		{`matches(@y, 'wor')`, "true"},
		{`replace(@x, '3', 'three')`, "three"},
		{`string-to-codepoints(@x)`, "51"},
		{`contains-token(@y, 'world')`, "true"},
		{`distinct-values(@x)`, "3"},
		{`index-of(@x, '3')`, "1"},
		{`xs:integer(@x) + 1`, "4"},
		{`sum(@x)`, "3"},
		{`min(@x)`, "3"},
		{`max(../n)`, "2.5"},
		{`avg(@x)`, "3"},
		{`number(@x)`, "3"},
		{`boolean(@x)`, "true"},
		{`string(@x)`, "3"},
		{`data(@x)`, "3"},
		{`map:get(map{'3': 'yes'}, @x)`, "yes"},
		{`map:contains(map{'3': 'yes'}, @x)`, "true"},
		{`count(@x)`, "1"},
		{`string-length(@none)`, "0"},
		{`normalize-space(@none)`, ""},
		{`string-join(@none, ',')`, ""},
		{`string-join(../m[1], ',')`, "1"},
	} {
		p, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		for _, bc := range []bool{false, true} {
			got, err := p.Eval(&Context{Node: a, Pos: 1, Size: 1, BC10: bc})
			if err != nil {
				t.Errorf("%s (BC10=%v): %v", c.expr, bc, err)
				continue
			}
			items, _ := Atomize(got)
			var strs []string
			for _, it := range items {
				strs = append(strs, itemString(it))
			}
			if s := joinStrings(strs, " "); s != c.want {
				t.Errorf("%s (BC10=%v): %q, want %q", c.expr, bc, s, c.want)
			}
		}
	}
	// A plural set is passed as the set and is still an error.
	p, _ := Parse(`string-length(../m)`)
	if _, err := p.Eval(&Context{Node: a, Pos: 1, Size: 1}); err == nil {
		t.Errorf("string-length(../m): expected XPTY0004")
	}
	// The bare node reads as a one-node set through the value helpers.
	if ToString(a) != "hello world" || !ToBool(a) || ToNumber(doc.Children[0].Children[1]) != 2.5 {
		t.Errorf("ToString/ToBool/ToNumber of a bare node")
	}
}

func joinStrings(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
