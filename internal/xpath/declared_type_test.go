package xpath

import "testing"

// TestMatchesDeclaredTypeNoContext pins the context-free fast path hosts try
// before CoerceToDeclaredTypeCtx: it may only say yes where the full check
// says (value, true), and it says no for every type whose test needs a
// context.
func TestMatchesDeclaredTypeNoContext(t *testing.T) {
	for _, c := range []struct {
		as   string
		v    Object
		want bool
	}{
		{"xs:integer", NewInteger(3), true},
		{"xs:decimal", NewInteger(3), true}, // integer derives from decimal
		{"xs:integer", NewDouble(3), false},
		{"xs:double", NewInteger(3), false}, // promotion is a conversion, not a match
		{"xs:string?", Sequence{}, true},
		{"xs:string", Sequence{}, false},
		{"xs:string+", FromItems([]Item{NewString("a"), NewString("b")}), true},
		{"item()*", FromItems([]Item{NewString("a"), NewInteger(1)}), true},
		{"empty-sequence()", Sequence{}, true},
		{"map(*)", NewMap(), false},           // not a context-free type: left to the full check
		{"element()", Sequence{}, false},      // likewise
		{"xs:integer)", NewInteger(1), false}, // does not parse
	} {
		for i := 0; i < 2; i++ { // the second round reads the parse cache
			if got := MatchesDeclaredTypeNoContext(c.as, c.v); got != c.want {
				t.Errorf("%s / %v: got %v, want %v", c.as, c.v, got, c.want)
			}
			if c.want {
				if _, ok := CoerceToDeclaredTypeCtx(c.as, c.v, nil); !ok {
					t.Errorf("%s / %v: the full check disagrees", c.as, c.v)
				}
			}
		}
	}
}
