package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// TestGenerateIDComparisonMatchesStrings checks that the fused
// generate-id(A) = generate-id(B) comparison answers exactly as comparing
// the two id strings does — one node, two nodes, the empty sequence, the
// nodes of a constructed tree (every order 0), namespace nodes, and the
// first-node rule of 1.0 compatibility mode — and that it leaves the error
// cases to the functions.
func TestGenerateIDComparisonMatchesStrings(t *testing.T) {
	tree, err := xmltree.Parse(`<a xmlns:p="urn:p" xmlns:q="urn:q"><b id="1"/><b id="2"/><c/></a>`)
	if err != nil {
		t.Fatal(err)
	}
	// A constructed tree: no document order assigned, every node is 0.
	built := xmltree.NewElement(xmltree.Name{Local: "r"})
	for i := 0; i < 2; i++ {
		built.Append(xmltree.NewElement(xmltree.Name{Local: "x"}))
	}
	cases := []struct{ a, op, b string }{
		{"", "=", "."},
		{"", "=", "/a/b[1]"},
		{"/a/b[1]", "=", "/a/b[2]"},
		{"/a/b[1]", "!=", "/a/b[2]"},
		{"/a/b[1]", "eq", "/a/b[1]"},
		{"/a/b[1]", "ne", "/a/b[1]"},
		{"/a/nope", "=", "/a/zip"},
		{"/a/nope", "=", "."},
		{".", "!=", "/a/nope"},
		{"/a/nope", "eq", "/a/zip"},
		{"/a/namespace::p", "=", "/a/namespace::q"},
		{"/a/namespace::p", "=", "/a/namespace::p"},
		{"/a/namespace::p", "=", "/a"},
		{"$v", "=", "$v"},
		{"/a/b", "=", "/a/c"},
		{"/a/b", "=", "/a/b"},
		{"1", "=", "."},
		{"/a", "=", "."},
		{"$x[1]", "=", "$x[2]"},
		{"$x[1]", "=", "$x[1]"},
		{"$x", "=", "$x"},
		{"/a/b/@id", "=", "/a/b[2]/@id"},
	}
	exprs := make([][2]string, len(cases))
	for i, c := range cases {
		exprs[i] = [2]string{
			"generate-id(" + c.a + ") " + c.op + " generate-id(" + c.b + ")",
			"concat(generate-id(" + c.a + "), '') " + c.op + " generate-id(" + c.b + ")",
		}
	}
	vars := map[string]Object{"v": NodeSet{tree}, "x": NodeSet(built.Children)}
	for _, bc := range []bool{false, true} {
		for _, pair := range exprs {
			e := pair[0]
			fused, err1 := Parse(e)
			if err1 != nil {
				t.Fatal(err1)
			}
			if !fused.root.(*CompareExpr).genID {
				t.Fatalf("%s: not planned as a generate-id comparison", e)
			}
			// The same comparison over the id strings themselves:
			// concat(generate-id(A), "") is not a generate-id call.
			plain, err1 := Parse(pair[1])
			if err1 != nil {
				t.Fatal(err1)
			}
			if plain.root.(*CompareExpr).genID {
				t.Fatalf("%s: planned as a generate-id comparison", pair[1])
			}
			for _, cn := range []*xmltree.Node{tree.Children[0], tree.Children[0].Children[0], nil} {
				mk := func() *Context {
					return &Context{Node: cn, Pos: 1, Size: 1, Vars: mapVars(vars), BC10: bc, NoFocus: cn == nil}
				}
				got, gerr := fused.Eval(mk())
				want, werr := plain.Eval(mk())
				if (gerr == nil) != (werr == nil) {
					t.Errorf("%s (bc=%v, node %v): err %v vs %v", e, bc, cn != nil, gerr, werr)
					continue
				}
				if gerr == nil && ToBool(got) != ToBool(want) {
					t.Errorf("%s (bc=%v, node %v): %v vs %v", e, bc, cn != nil, got, want)
				}
			}
		}
	}
}

type mapVars map[string]Object

func (m mapVars) ResolveVar(prefix, local string) (Object, bool) {
	v, ok := m[local]
	return v, ok
}
