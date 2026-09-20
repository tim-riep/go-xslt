package xslt

// Tests for the second round of streamability rules: stylesheet functions
// (§19.8.5), attribute sets (§19.8.6), streamable accumulator declarations
// (§18.2.8), and the accumulator/current-group function rules of §19.8.9 they
// depend on.

import (
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xpath"
)

// strmFnSheet wraps a stylesheet function declaration and a template rule for
// the streamable unnamed mode, which is where the XTSE3430 gate fires.
func strmFnSheet(decl, body string) string {
	return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
   xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:f="http://example.com/f">
  <xsl:mode streamable="yes"/>
  ` + decl + `
  <xsl:template match="doc">` + body + `</xsl:template>
</xsl:stylesheet>`
}

// TestStrmFuncCategoryParse pins the §19.8.5 vocabulary, including the rule
// that an unrecognised category is analysed as unclassified.
func TestStrmFuncCategoryParse(t *testing.T) {
	cases := map[string]xpath.StreamFuncCategory{
		"":                xpath.StreamFnUnclassified,
		"unclassified":    xpath.StreamFnUnclassified,
		"absorbing":       xpath.StreamFnAbsorbing,
		"inspection":      xpath.StreamFnInspection,
		"filter":          xpath.StreamFnFilter,
		"shallow-descent": xpath.StreamFnShallowDescent,
		"deep-descent":    xpath.StreamFnDeepDescent,
		"ascent":          xpath.StreamFnAscent,
		// A QName in an implementation-defined namespace this processor does
		// not recognise must be analysed as unclassified, not rejected.
		"saxon:capture": xpath.StreamFnUnclassified,
		"nonsense":      xpath.StreamFnUnclassified,
	}
	for attr, want := range cases {
		decl := `<xsl:function name="f:g" as="xs:integer"><xsl:param name="n"/><xsl:sequence select="1"/></xsl:function>`
		if attr != "" {
			decl = strings.Replace(decl, `name="f:g"`, `name="f:g" streamability="`+attr+`"`, 1)
		}
		ss, err := Compile(strmFnSheet(decl, `x`))
		if err != nil {
			t.Fatalf("%q: compile: %v", attr, err)
		}
		fd := ss.functions[funcKey("http://example.com/f", "g", 1)]
		if fd == nil {
			t.Fatalf("%q: function not compiled", attr)
		}
		if got := strmFuncCat(fd.el); got != want {
			t.Errorf("streamability=%q: category %v, want %v", attr, got, want)
		}
	}
}

// TestStrmStylesheetFunctionCalls checks that a call to a stylesheet function
// is classified from the function's declared category rather than rejected
// outright, which is what §19.8.5 exists for.
func TestStrmStylesheetFunctionCalls(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	cases := []struct {
		name string
		decl string
		body string
		want bool
	}{
		{
			// The spec's own worked case: an unclassified function whose
			// argument is grounded before it reaches the call.
			name: "unclassified over a grounded argument",
			decl: `<xsl:function name="f:g" streamability="unclassified"><xsl:param name="n"/><xsl:sequence select="$n"/></xsl:function>`,
			body: `<xsl:copy-of select="f:g(copy-of(.))"/>`,
			want: true,
		},
		{
			// Without a category the parameter's own type decides, and
			// item()* navigates: a streamed node may not be passed.
			name: "unclassified over a streamed argument",
			decl: `<xsl:function name="f:g" streamability="unclassified"><xsl:param name="n"/><xsl:sequence select="$n"/></xsl:function>`,
			body: `<xsl:copy-of select="f:g(a)"/>`,
			want: false,
		},
		{
			// An atomic parameter type atomizes the node, which is allowed for
			// any category (§19.8.5's second condition).
			name: "atomizing parameter type accepts a streamed node",
			decl: `<xsl:function name="f:g" as="xs:string"><xsl:param name="n" as="xs:string"/><xsl:sequence select="$n"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(a)"/>`,
			want: true,
		},
		{
			name: "absorbing accepts a striding first argument",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="absorbing"><xsl:param name="n" as="node()"/><xsl:sequence select="string($n)"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(a)"/>`,
			want: true,
		},
		{
			// §19.8.5.2: absorption may not be applied to a crawling sequence.
			name: "absorbing rejects a crawling first argument",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="absorbing"><xsl:param name="n" as="node()"/><xsl:sequence select="string($n)"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(.//a)"/>`,
			want: false,
		},
		{
			// §19.8.5.1: an argument after the first carries its own
			// type-determined usage, so a streamed node there navigates.
			name: "streamed node in a second argument",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="absorbing"><xsl:param name="n" as="node()"/><xsl:param name="o"/><xsl:sequence select="string($n)"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(a, b)"/>`,
			want: false,
		},
		{
			name: "inspection over a streamed argument",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="inspection"><xsl:param name="n" as="node()"/><xsl:sequence select="name($n)"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(a)"/>`,
			want: true,
		},
		{
			name: "filter keeps the argument's posture",
			decl: `<xsl:function name="f:g" as="node()*" streamability="filter"><xsl:param name="n" as="node()"/><xsl:sequence select="$n[@x]"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(a)/@y"/>`,
			want: true,
		},
		{
			name: "shallow-descent selects children",
			decl: `<xsl:function name="f:g" as="node()*" streamability="shallow-descent"><xsl:param name="n" as="element()"/><xsl:sequence select="$n/node()"/></xsl:function>`,
			body: `<xsl:copy-of select="f:g(a)"/>`,
			want: true,
		},
		{
			name: "deep-descent selects descendants",
			decl: `<xsl:function name="f:g" as="node()*" streamability="deep-descent"><xsl:param name="n" as="element()"/><xsl:sequence select="$n//comment()"/></xsl:function>`,
			body: `<xsl:copy-of select="f:g(a)"/>`,
			want: true,
		},
		{
			// §19.8.5.7 requires the call's own sweep to be motionless, so the
			// argument may not consume: @a qualifies, a child step does not.
			name: "ascent selects ancestors",
			decl: `<xsl:function name="f:g" as="node()*" streamability="ascent"><xsl:param name="n" as="node()"/><xsl:sequence select="$n/ancestor::x"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(@a)/@y"/>`,
			want: true,
		},
		{
			name: "ascent over a consuming argument",
			decl: `<xsl:function name="f:g" as="node()*" streamability="ascent"><xsl:param name="n" as="node()"/><xsl:sequence select="$n/ancestor::x"/></xsl:function>`,
			body: `<xsl:value-of select="f:g(a)/@y"/>`,
			want: false,
		},
		{
			// §19.8.5: a zero-arity function holds no streamed node, so every
			// call to one is grounded and motionless.
			name: "zero-arity call is grounded",
			decl: `<xsl:function name="f:g" as="xs:integer"><xsl:sequence select="42"/></xsl:function>`,
			body: `<xsl:value-of select="a"/><xsl:value-of select="f:g()"/>`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(strmFnSheet(tc.decl, tc.body))
			if tc.want && err != nil {
				t.Fatalf("streamable construct rejected: %v", err)
			}
			if !tc.want {
				if err == nil {
					t.Fatal("non-streamable construct accepted")
				}
				if !strings.Contains(err.Error(), "err:XTSE3430:") {
					t.Fatalf("diagnostic %q is not catalogued XTSE3430", err.Error())
				}
			}
		})
	}
}

// TestStrmFunctionBodyRules covers §19.8.5's per-category constraints on the
// body of a declared-streamable function.
func TestStrmFunctionBodyRules(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	cases := []struct {
		name string
		decl string
		want bool
	}{
		{
			// Absorbing requires a GROUNDED result; returning the streaming
			// parameter itself does not qualify.
			name: "absorbing returning a streamed node",
			decl: `<xsl:function name="f:g" streamability="absorbing"><xsl:param name="n" as="node()"/><xsl:sequence select="$n"/></xsl:function>`,
			want: false,
		},
		{
			name: "absorbing returning an atomic value",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="absorbing"><xsl:param name="n" as="node()"/><xsl:sequence select="string($n)"/></xsl:function>`,
			want: true,
		},
		{
			// Two downward reads of the streaming parameter cannot share one
			// pass.
			name: "absorbing reading the parameter twice",
			decl: `<xsl:function name="f:g" as="xs:boolean" streamability="absorbing"><xsl:param name="n" as="node()*"/><xsl:sequence select="deep-equal($n[1], $n[2])"/></xsl:function>`,
			want: false,
		},
		{
			// Inspection must not move the stream at all.
			name: "inspection that absorbs",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="inspection"><xsl:param name="n" as="node()"/><xsl:sequence select="string($n)"/></xsl:function>`,
			want: false,
		},
		{
			name: "inspection reading only the name",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="inspection"><xsl:param name="n" as="node()"/><xsl:sequence select="name($n)"/></xsl:function>`,
			want: true,
		},
		{
			// Filter must stay motionless: a downward step does not.
			name: "filter selecting children",
			decl: `<xsl:function name="f:g" as="node()*" streamability="filter"><xsl:param name="n" as="node()"/><xsl:sequence select="$n/x"/></xsl:function>`,
			want: false,
		},
		{
			name: "filter selecting on an attribute",
			decl: `<xsl:function name="f:g" as="node()*" streamability="filter"><xsl:param name="n" as="node()"/><xsl:sequence select="$n[@x]"/></xsl:function>`,
			want: true,
		},
		{
			// §19.8.5.3/.4/.5/.6/.7 "Rules for the function signature": every
			// category but absorbing requires a streaming parameter that permits
			// at most one node, because a sequence of them could not be revisited
			// without advancing the stream.
			name: "filter whose streaming parameter permits a sequence",
			decl: `<xsl:function name="f:g" as="node()*" streamability="filter"><xsl:param name="n" as="node()*"/><xsl:sequence select="$n[@x]"/></xsl:function>`,
			want: false,
		},
		{
			name: "inspection whose streaming parameter is untyped",
			decl: `<xsl:function name="f:g" as="xs:string" streamability="inspection"><xsl:param name="n"/><xsl:sequence select="name($n)"/></xsl:function>`,
			want: false,
		},
		{
			// §19.8.5.2 exempts absorbing: it reads each supplied subtree in
			// turn, so a sequence-typed streaming parameter is allowed.
			name: "absorbing accepts a sequence-typed streaming parameter",
			decl: `<xsl:function name="f:g" as="xs:integer" streamability="absorbing"><xsl:param name="n" as="node()*"/><xsl:sequence select="count($n//*)"/></xsl:function>`,
			want: true,
		},
		{
			// §19.8.5.2: with a sequence-typed streaming parameter each
			// reference is CONSUMING, so two of them cannot share one pass.
			name: "absorbing reading a sequence parameter in two operands",
			decl: `<xsl:function name="f:g" as="xs:integer" streamability="absorbing"><xsl:param name="n" as="node()*"/><xsl:sequence select="count($n/a) + count($n/b)"/></xsl:function>`,
			want: false,
		},
		{
			// shallow-descent must not return nested nodes.
			name: "shallow-descent selecting descendants",
			decl: `<xsl:function name="f:g" as="node()*" streamability="shallow-descent"><xsl:param name="n" as="node()"/><xsl:sequence select="$n//x"/></xsl:function>`,
			want: false,
		},
		{
			name: "deep-descent selecting descendants",
			decl: `<xsl:function name="f:g" as="node()*" streamability="deep-descent"><xsl:param name="n" as="node()"/><xsl:sequence select="$n//x"/></xsl:function>`,
			want: true,
		},
		{
			// A reference to the streaming parameter inside a higher-order
			// operand is evaluated repeatedly (§19.8.8.11's singular rule).
			name: "streaming parameter inside a for expression",
			decl: `<xsl:function name="f:g" as="xs:string*" streamability="absorbing"><xsl:param name="n" as="node()*"/><xsl:sequence select="for $i in 1 to 3 return name($n[$i])"/></xsl:function>`,
			want: false,
		},
		{
			// §19.8.5: only unclassified is permitted for a zero-arity
			// function, and the suite spells that error XTSE3155.
			name: "zero-arity declared streamable",
			decl: `<xsl:function name="f:g" streamability="shallow-descent"><xsl:sequence select="23"/></xsl:function>`,
			want: false,
		},
		{
			// An unclassified function places no constraint on its body.
			name: "unclassified body is unconstrained",
			decl: `<xsl:function name="f:g" streamability="unclassified"><xsl:param name="n"/><xsl:sequence select="$n/x/preceding-sibling::y"/></xsl:function>`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(strmFnSheet(tc.decl, `x`))
			if tc.want && err != nil {
				t.Fatalf("streamable function rejected: %v", err)
			}
			if !tc.want {
				if err == nil {
					t.Fatal("non-streamable function accepted")
				}
				if !strings.Contains(err.Error(), "err:XTSE3") {
					t.Fatalf("diagnostic %q is not a catalogued streamability error", err.Error())
				}
			}
		})
	}
}

// TestStrmZeroArityCategoryCode pins the specific error code, which the suite
// distinguishes from the general XTSE3430.
func TestStrmZeroArityCategoryCode(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	_, err := Compile(strmFnSheet(
		`<xsl:function name="f:g" streamability="absorbing"><xsl:sequence select="1"/></xsl:function>`, `x`))
	if err == nil || !strings.Contains(err.Error(), "err:XTSE3155:") {
		t.Fatalf("zero-arity category error = %v, want XTSE3155", err)
	}
}

// TestStrmAttributeSets covers §19.8.6: a use-attribute-sets reference is an
// operand of the referring instruction, not an automatic rejection — but only
// once the set is DECLARED streamable, which §19.8.6 and the identical Notes
// on §19.8.4.1/§19.8.4.11/§19.8.4.15 make the first thing a reference turns
// on ("a reference to any other attribute set makes the instruction roaming
// and free-ranging"), ahead of anything the set's own contents say.
func TestStrmAttributeSets(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	sheet := func(decl, set, body string) string {
		return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:mode streamable="yes"/>
  <xsl:attribute-set name="s" ` + decl + `>` + set + `</xsl:attribute-set>
  <xsl:template match="doc">` + body + `</xsl:template>
</xsl:stylesheet>`
	}
	const yes = `streamable="yes"`
	// A motionless attribute set costs the referring element nothing.
	if _, err := Compile(sheet(yes,
		`<xsl:attribute name="a" select="'x'"/>`,
		`<xsl:element name="e" use-attribute-sets="s"><xsl:value-of select="a"/></xsl:element>`)); err != nil {
		t.Fatalf("a motionless attribute set was rejected: %v", err)
	}
	// A consuming attribute set competes with a consuming element body exactly
	// as a second consuming operand would.
	_, err := Compile(sheet(yes,
		`<xsl:attribute name="a" select="b"/>`,
		`<xsl:element name="e" use-attribute-sets="s"><xsl:value-of select="a"/></xsl:element>`))
	if err == nil || !strings.Contains(err.Error(), "err:XTSE3430:") {
		t.Fatalf("two consuming operands were accepted: %v", err)
	}
	// The consuming set on its own is fine.
	if _, err := Compile(sheet(yes,
		`<xsl:attribute name="a" select="b"/>`,
		`<xsl:element name="e" use-attribute-sets="s"/>`)); err != nil {
		t.Fatalf("a single consuming operand was rejected: %v", err)
	}
	// The declaration is what a reference reads, so a set that would pass the
	// contents test on its own is still unusable without streamable="yes" —
	// §10.2.3's own Note: a constant-valued set "will always be grounded and
	// motionless and therefore streamable", yet is "not guaranteed streamable
	// unless the attribute set is declared with the attribute streamable=yes".
	for _, decl := range []string{``, `streamable="no"`} {
		_, err := Compile(sheet(decl,
			`<xsl:attribute name="a" select="'x'"/>`,
			`<xsl:element name="e" use-attribute-sets="s"/>`))
		if err == nil || !strings.Contains(err.Error(), "err:XTSE3430:") {
			t.Fatalf("attribute set with %q accepted by reference: %v", decl, err)
		}
	}
}

// TestStrmStreamableAccumulator covers §18.2.8.
func TestStrmStreamableAccumulator(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	sheet := func(acc string) string {
		return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
   xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xsl:mode streamable="yes"/>
  ` + acc + `
  <xsl:template match="doc"><xsl:value-of select="a"/></xsl:template>
</xsl:stylesheet>`
	}
	cases := []struct {
		name string
		acc  string
		want bool
	}{
		{
			name: "counting rule",
			acc: `<xsl:accumulator name="c" as="xs:integer" initial-value="0" streamable="yes">
                    <xsl:accumulator-rule match="fig" select="$value + 1"/>
                  </xsl:accumulator>`,
			want: true,
		},
		{
			// The accumulator's @as atomizes the attribute, so the rule value
			// is grounded even though @amount is a streamed node.
			name: "rule returning an attribute under an atomic @as",
			acc: `<xsl:accumulator name="c" as="xs:double" initial-value="0" streamable="yes">
                    <xsl:accumulator-rule match="t" select="if (@amount lt $value) then @amount else $value"/>
                  </xsl:accumulator>`,
			want: true,
		},
		{
			// A rule whose value reads the subtree moves the stream.
			name: "rule absorbing the matched element",
			acc: `<xsl:accumulator name="c" as="xs:string" initial-value="''" streamable="yes">
                    <xsl:accumulator-rule match="fig" select="string(.)"/>
                  </xsl:accumulator>`,
			want: false,
		},
		{
			// The same rule over a text node is motionless: a text node's whole
			// value is available without reading further input.
			name: "rule absorbing a text node",
			acc: `<xsl:accumulator name="c" as="xs:string" initial-value="''" streamable="yes">
                    <xsl:accumulator-rule match="fig/text()" select="string(.)"/>
                  </xsl:accumulator>`,
			want: true,
		},
		{
			name: "non-motionless match pattern",
			acc: `<xsl:accumulator name="c" as="xs:integer" initial-value="0" streamable="yes">
                    <xsl:accumulator-rule match="fig[x]" select="$value + 1"/>
                  </xsl:accumulator>`,
			want: false,
		},
		{
			name: "initial value reading the input",
			acc: `<xsl:accumulator name="c" as="xs:integer" initial-value="count(//x)" streamable="yes">
                    <xsl:accumulator-rule match="fig" select="$value + 1"/>
                  </xsl:accumulator>`,
			want: false,
		},
		{
			// An accumulator that is not declared streamable is unconstrained.
			name: "non-streamable accumulator is unconstrained",
			acc: `<xsl:accumulator name="c" as="xs:string" initial-value="''">
                    <xsl:accumulator-rule match="fig" select="string(.)"/>
                  </xsl:accumulator>`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(sheet(tc.acc))
			if tc.want && err != nil {
				t.Fatalf("streamable accumulator rejected: %v", err)
			}
			if !tc.want && (err == nil || !strings.Contains(err.Error(), "err:XTSE3430:")) {
				t.Fatalf("non-streamable accumulator accepted: %v", err)
			}
		})
	}
}

// TestStrmAccumulatorFunctions covers the §19.8.9.1/.2 rules the accumulator
// tests depend on: accumulator-before never moves the stream, and
// accumulator-after is motionless once something earlier in the same sequence
// constructor has consumed it.
func TestStrmAccumulatorFunctions(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	sheet := func(body string) string {
		return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
   xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xsl:mode streamable="yes"/>
  <xsl:accumulator name="c" as="xs:integer" initial-value="0" streamable="yes">
    <xsl:accumulator-rule match="fig" select="$value + 1"/>
  </xsl:accumulator>
  <xsl:template match="doc">` + body + `</xsl:template>
</xsl:stylesheet>`
	}
	// accumulator-before is motionless, so it can sit alongside a consuming
	// instruction.
	if _, err := Compile(sheet(
		`<xsl:value-of select="accumulator-before('c')"/><xsl:apply-templates/>`)); err != nil {
		t.Fatalf("accumulator-before was treated as consuming: %v", err)
	}
	// accumulator-after AFTER a consuming instruction is motionless.
	if _, err := Compile(sheet(
		`<xsl:apply-templates/><xsl:value-of select="accumulator-after('c')"/>`)); err != nil {
		t.Fatalf("accumulator-after following a consuming instruction was rejected: %v", err)
	}
	// BEFORE one it is consuming, so the pair cannot share a pass.
	_, err := Compile(sheet(
		`<xsl:value-of select="accumulator-after('c')"/><xsl:apply-templates/>`))
	if err == nil || !strings.Contains(err.Error(), "err:XTSE3430:") {
		t.Fatalf("accumulator-after preceding a consuming instruction was accepted: %v", err)
	}
}

// TestStrmScanningStart covers the §19.8.8.7 reassessment for a path that
// begins at "." or at a variable rather than at a bare step.
func TestStrmScanningStart(t *testing.T) {
	body, el := strmCompileBody(t, strmSheet(`<xsl:copy-of select="outermost(.//p)"/>`))
	if ok, err := classifyStreamable(body, el); !ok {
		t.Fatalf(`outermost(.//p) was rejected: %v`, err)
	}
	// The same shape rooted at a step still works, which is the case the first
	// round already covered.
	body, el = strmCompileBody(t, strmSheet(`<xsl:copy-of select="outermost(a//p)"/>`))
	if ok, err := classifyStreamable(body, el); !ok {
		t.Fatalf(`outermost(a//p) was rejected: %v`, err)
	}
	// An upward step is still not a scanning expression.
	body, el = strmCompileBody(t, strmSheet(`<xsl:copy-of select=".//p/../q"/>`))
	if ok, _ := classifyStreamable(body, el); ok {
		t.Fatal(`.//p/../q was accepted as a scanning expression`)
	}
}

// TestStrmIntersectExceptPosture pins the narrowing of §19.8.8.4 for the two
// operators whose result can only be a subset of the left operand.
func TestStrmIntersectExceptPosture(t *testing.T) {
	sc := xpath.StreamContext{ContextPosture: xpath.PostureStriding}
	for _, src := range []string{"a except b", "a intersect b"} {
		p, err := xpath.Parse(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		post, sweep := p.Streamability(sc)
		if post != xpath.PostureStriding {
			t.Errorf("%q posture = %v, want striding", src, post)
		}
		if sweep != xpath.SweepConsuming {
			t.Errorf("%q sweep = %v, want consuming", src, sweep)
		}
	}
	// A union of two striding operands still widens to crawling, since the two
	// results may nest.
	p, err := xpath.Parse("a | b")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if post, _ := p.Streamability(sc); post != xpath.PostureCrawling {
		t.Errorf("union posture = %v, want crawling", post)
	}
}

// TestStrmCurrentGroupPosture covers §19.8.9.4: current-group() takes the
// posture of the population, and a grounded population never moves the stream —
// which is what lets a group body read it more than once.
func TestStrmCurrentGroupPosture(t *testing.T) {
	body, el := strmCompileBody(t, strmSheet(
		`<xsl:for-each-group select="copy-of(tr)" group-starting-with="tr[th]">
            <a><xsl:value-of select="current-group()[1]/th"/></a>
            <b><xsl:value-of select="current-group()[1]/td"/></b>
         </xsl:for-each-group>`))
	if ok, err := classifyStreamable(body, el); !ok {
		t.Fatalf("a grounded population's current-group() was treated as consuming: %v", err)
	}
	// Over a STREAMED population the same two reads do compete.
	body, el = strmCompileBody(t, strmSheet(
		`<xsl:for-each-group select="tr" group-starting-with="tr[@x]">
            <a><xsl:value-of select="current-group()[1]/th"/></a>
            <b><xsl:value-of select="current-group()[1]/td"/></b>
         </xsl:for-each-group>`))
	if ok, _ := classifyStreamable(body, el); ok {
		t.Fatal("two reads of a streamed current-group() were accepted")
	}
}
