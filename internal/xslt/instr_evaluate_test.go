package xslt

import (
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
`
	cases := []struct {
		name string
		tmpl string
		src  string
		want string
	}{
		{
			name: "literal arithmetic",
			tmpl: `<xsl:template match="/r"><xsl:evaluate xpath="'1 + 2 * 3'"/></xsl:template>`,
			src:  `<r/>`,
			want: "7",
		},
		{
			// XSLT 3.0 §10.4.2: with no @context-item the evaluated expression
			// has NO focus — it does not inherit the containing one — so a
			// stylesheet that wants the current node must say so explicitly.
			name: "expression built from content",
			tmpl: `<xsl:template match="/r"><xsl:evaluate xpath="concat('count(item', ')')" context-item="."/></xsl:template>`,
			src:  `<r><item/><item/><item/></r>`,
			want: "3",
		},
		{
			name: "with-param binding",
			tmpl: `<xsl:template match="/r"><xsl:evaluate xpath="'$x + $y'"><xsl:with-param name="x" select="10"/><xsl:with-param name="y" select="5"/></xsl:evaluate></xsl:template>`,
			src:  `<r/>`,
			want: "15",
		},
		{
			name: "context-item",
			tmpl: `<xsl:template match="/r"><xsl:evaluate xpath="'@id'" context-item="item[2]"/></xsl:template>`,
			src:  `<r><item id="a"/><item id="b"/></r>`,
			want: "b",
		},
		{
			name: "explicit context-item=. is the current node",
			tmpl: `<xsl:template match="/r"><xsl:evaluate xpath="'name()'" context-item="."/></xsl:template>`,
			src:  `<r/>`,
			want: "r",
		},
		{
			name: "string function on context",
			tmpl: `<xsl:template match="/r"><xsl:evaluate xpath="'string-length(.)'" context-item="msg"/></xsl:template>`,
			src:  `<r><msg>hello</msg></r>`,
			want: "5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := hdr + tc.tmpl + "\n</xsl:stylesheet>"
			out := strings.TrimSpace(transform(t, sheet, tc.src, nil))
			if out != tc.want {
				t.Errorf("got %q want %q", out, tc.want)
			}
		})
	}
}

// TestEvaluateAbsentFocus pins XSLT 3.0 §10.4.2: omitting @context-item leaves
// the context item, position and size ABSENT for the evaluated expression, so a
// focus-dependent expression there is XPDY0002 even though the containing
// template has a current node (the W3C case evaluate-024 is exactly this).
func TestEvaluateAbsentFocus(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r"><xsl:evaluate xpath="'position()'"/></xsl:template>
</xsl:stylesheet>`
	ss, err := Compile(sheet)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, _, err := ss.Transform(`<r/>`, nil); err == nil {
		t.Fatal("expected XPDY0002 for a focus-dependent expression with no context-item")
	} else if !strings.Contains(err.Error(), "XPDY0002") {
		t.Errorf("got %v, want XPDY0002", err)
	}
}

func TestEvaluateParseError(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r"><xsl:evaluate xpath="'1 +'"/></xsl:template>
</xsl:stylesheet>`
	ss, err := Compile(sheet)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, _, err := ss.Transform(`<r/>`, nil); err == nil {
		t.Fatal("expected error from malformed dynamic xpath, got nil")
	}
}
