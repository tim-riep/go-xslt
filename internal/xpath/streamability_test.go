package xpath

import "testing"

func strmClassify(t *testing.T, expr string, sc StreamContext) (Posture, Sweep) {
	t.Helper()
	p, err := Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return p.Streamability(sc)
}

func strmName(p Posture, s Sweep) string {
	pn := map[Posture]string{
		PostureGrounded: "grounded", PostureClimbing: "climbing",
		PostureCrawling: "crawling", PostureStriding: "striding",
		PostureRoaming: "roaming",
	}
	sn := map[Sweep]string{
		SweepMotionless: "motionless", SweepConsuming: "consuming",
		SweepFreeRanging: "free-ranging",
	}
	return pn[p] + "/" + sn[s]
}

// TestStreamabilityPostureSweepTable walks the worked examples the spec itself
// gives in §19.7, §19.8.8.7 and §19.8.8.8, evaluated with the striding context
// posture of a streamable template rule.
func TestStreamabilityPostureSweepTable(t *testing.T) {
	sc := StreamContext{ContextPosture: PostureStriding}
	cases := []struct {
		expr string
		want string
	}{
		// §19.7's table of posture/sweep combinations (rows are postures,
		// columns sweeps).
		{`name()`, "grounded/motionless"},
		{`string(title)`, "grounded/consuming"},
		{`parent::*`, "climbing/motionless"},
		{`child::x/ancestor::y`, "climbing/consuming"},
		{`@status`, "striding/motionless"},
		{`child::*`, "striding/consuming"},
		{`descendant::*`, "crawling/consuming"},
		{`preceding::*`, "roaming/free-ranging"},
		// §19.8.8.7's worked path examples.
		{`a/b/c`, "striding/consuming"},
		{`a/descendant::c`, "crawling/consuming"},
		{`../@status`, "striding/motionless"},
		{`section//head`, "crawling/consuming"},
		{`section//head[1]`, "roaming/free-ranging"},
		{`copy-of(.)//a/following-sibling::*`, "grounded/consuming"},
		// §19.8.8.8's descendant-with-numeric-predicate clause.
		{`descendant::section[1]`, "striding/consuming"},
		// Literals and the context item.
		{`3`, "grounded/motionless"},
		{`.`, "striding/motionless"},
		{`()`, "grounded/motionless"},
		// §19.8.1's note: (@a, @b) is motionless and striding.
		{`(@a, @b)`, "striding/motionless"},
		// §19.8.1's note: if (X) then @name else name is streamable — the
		// choice group combines a motionless and a consuming branch.
		{`if (@x) then @name else name`, "striding/consuming"},
		// §19.8.9.14: last() is roaming over a striding focus.
		{`last()`, "roaming/free-ranging"},
		{`position()`, "grounded/motionless"},
		// §19.8.8.1: the "in" clause of a for expression must be grounded.
		{`for $i in 1 to 3 return $i*2`, "grounded/motionless"},
		{`for $x in child::section return $x/para`, "roaming/free-ranging"},
		// §19.8.8.2's rewrite advice.
		{`exists(child::section[has-children(.)])`, "grounded/consuming"},
		// §19.8.8.4's union examples.
		// One operand is grounded and motionless, so the result is the other's.
		{`. | doc('abc.com')//x`, "striding/motionless"},
		{`parent::A | */ancestor::B`, "climbing/consuming"},
		{`* | */*`, "crawling/consuming"},
		{`child::div | parent::div`, "roaming/free-ranging"},
		// §19.8.9.17: reverse navigates, so it needs a grounded operand.
		{`reverse(ancestor::*)/name()`, "roaming/free-ranging"},
		{`reverse(ancestor::*/name())`, "grounded/motionless"},
		// §19.8.9.15: outermost lifts crawling to striding.
		{`outermost(descendant::para)`, "striding/consuming"},
		// §19.8.5's atomization note: abs(discount) grounds.
		{`abs(discount)`, "grounded/consuming"},
		// The spec's own streamable example (§16.2).
		{`transactions/transaction[@value >= 0]`, "striding/consuming"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			p, s := strmClassify(t, tc.expr, sc)
			if got := strmName(p, s); got != tc.want {
				t.Errorf("%s: got %s, want %s", tc.expr, got, tc.want)
			}
		})
	}
}

// TestStreamabilityAbsolutePathNeedsDocumentContext pins §19.8.9.18's rewrite:
// a leading "/" collapses to the context item only when that is statically a
// document node.
func TestStreamabilityAbsolutePathNeedsDocumentContext(t *testing.T) {
	doc := StreamContext{ContextPosture: PostureStriding, ContextIsDocument: true}
	elem := StreamContext{ContextPosture: PostureStriding}
	if p, s := strmClassify(t, `/a/b`, doc); !Streamable(p, s) {
		t.Errorf("/a/b over a document context: got %s, want streamable", strmName(p, s))
	}
	if p, s := strmClassify(t, `/a/b`, elem); Streamable(p, s) {
		t.Errorf("/a/b over an element context: got %s, want non-streamable", strmName(p, s))
	}
	if p, s := strmClassify(t, `//a`, doc); !Streamable(p, s) {
		t.Errorf("//a over a document context: got %s, want streamable", strmName(p, s))
	}
}

// TestStreamabilityFailsClosed checks the safety direction: constructs with no
// rule, or whose rule this engine cannot prove, classify as non-streamable.
func TestStreamabilityFailsClosed(t *testing.T) {
	sc := StreamContext{ContextPosture: PostureStriding}
	for _, expr := range []string{
		`my:unknownFunction(a)`,   // a stylesheet or extension function
		`let $x := . return $x/a`, // a variable bound to a streamed node
		`[a, b]`,                  // an array holding streamed nodes
		`map{'k': a}`,             // a map holding streamed nodes
		`some $x in child::s satisfies has-children($x)`,
		`a/following::b`,
		`count((.., *))`, // §19.8.1's note: two streamed operands
	} {
		t.Run(expr, func(t *testing.T) {
			p, s := strmClassify(t, expr, sc)
			if Streamable(p, s) {
				t.Errorf("%s classified %s, want non-streamable", expr, strmName(p, s))
			}
		})
	}
}

// TestStreamabilityPatterns walks §19.8.10's own two example lists.
func TestStreamabilityPatterns(t *testing.T) {
	sc := StreamContext{ContextPosture: PostureStriding}
	motionless := []string{
		`/`, `*`, `/*`, `p`, `p|q`, `p/q`, `p[@status='red']`,
		`p[base-uri()]`, `p[@class or @style]`, `p[@status]`,
		`p[@class | @style]`, `p[contains(@class, ':')]`,
		`p[substring-after(@class, ':')]`, `p[ancestor::*[@xml:lang]]`,
		`text()[starts-with(., '$')]`, `@price`, `@price[starts-with(., '$')]`,
		`//p/text()[. = 'Introduction']`, `document-node(element(html))`,
	}
	for _, src := range motionless {
		t.Run("motionless/"+src, func(t *testing.T) {
			pat, err := ParsePattern(src)
			if err != nil {
				t.Fatalf("parse pattern %q: %v", src, err)
			}
			if p, s := pat.Streamability(sc); !Streamable(p, s) {
				t.Errorf("pattern %q: got %s, want motionless", src, strmName(p, s))
			}
		})
	}
	notMotionless := []string{
		`id('abc')`, `p[b]`, `p[. = 'Introduction']`, `p[starts-with(., '$')]`,
		`p[preceding-sibling::p[1] = '']`, `p[1]`, `p[position() gt 2]`,
		`p[last()]`, `p[data(@status)]`,
	}
	for _, src := range notMotionless {
		t.Run("free-ranging/"+src, func(t *testing.T) {
			pat, err := ParsePattern(src)
			if err != nil {
				t.Fatalf("parse pattern %q: %v", src, err)
			}
			if p, s := pat.Streamability(sc); Streamable(p, s) {
				t.Errorf("pattern %q: got %s, want free-ranging", src, strmName(p, s))
			}
		})
	}
}
