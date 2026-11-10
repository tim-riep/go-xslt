package xpath

import (
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// The per-item loops (simple map, postfix path step, predicate over items)
// reuse one scratch context across items. A named function reference to a
// context-dependent function, or a partial application, CAPTURES the context
// it is evaluated in (XPath 3.1 §3.1.6), so the next item must then get a
// fresh one, or the function item would see the later item's focus. (An
// inline function body has no focus of its own, so it cannot capture one.)
func TestScratchContextNotReusedOnceCaptured(t *testing.T) {
	tree, err := xmltree.Parse(`<r><a>1</a><a>2</a><a>3</a></r>`)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ expr, want string }{
		// simple map: one function item per item, all called after the loop
		{"string-join(((10, 20, 30) ! string#0) ! .(), ' ')", "10 20 30"},
		{"string-join(((10, 20, 30) ! position#0) ! .(), ' ')", "1 2 3"},
		{"string-join(((10, 20, 30) ! concat(?, '-', string(.))) ! .('x'), ' ')", "x-10 x-20 x-30"},
		// postfix path step: one function item per context node
		{"string-join(for $f in /r/a/(string#0) return $f(), ' ')", "1 2 3"},
		{"string-join(for $f in /r/a/(position#0) return $f(), ' ')", "1 2 3"},
		{"string-join(for $f in /r/a/(concat(?, string(.))) return $f('n'), ' ')", "n1 n2 n3"},
		// predicate over non-node items
		{"string-join((10, 20, 30)[string#0() = string(.)], ' ')", "10 20 30"},
		{"string-join((10, 20, 30)[position#0() = 2], ' ')", "20"},
		{"string-join((10, 20, 30)[concat(?, string(.))('') = string(.)], ' ')", "10 20 30"},
	}
	for _, c := range cases {
		got := ToString(evalStr(t, c.expr, tree))
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
