package xslt

import (
	"strings"
	"testing"
)

func TestNumberInstruction(t *testing.T) {
	cases := []struct {
		name string
		body string // body of the match="/r" template
		src  string
		want string
	}{
		{
			name: "value decimal",
			body: `<xsl:number value="42"/>`,
			src:  `<r/>`,
			want: "42",
		},
		{
			name: "value zero padded",
			body: `<xsl:number value="7" format="001"/>`,
			src:  `<r/>`,
			want: "007",
		},
		{
			name: "value lower alpha",
			body: `<xsl:number value="27" format="a"/>`,
			src:  `<r/>`,
			want: "aa",
		},
		{
			name: "value upper alpha",
			body: `<xsl:number value="1" format="A"/>`,
			src:  `<r/>`,
			want: "A",
		},
		{
			name: "value lower roman",
			body: `<xsl:number value="14" format="i"/>`,
			src:  `<r/>`,
			want: "xiv",
		},
		{
			name: "value upper roman",
			body: `<xsl:number value="2024" format="I"/>`,
			src:  `<r/>`,
			want: "MMXXIV",
		},
		{
			name: "value grouping",
			body: `<xsl:number value="1234567" grouping-separator="," grouping-size="3"/>`,
			src:  `<r/>`,
			want: "1,234,567",
		},
		{
			name: "level single preceding siblings",
			body: `<xsl:for-each select="item"><xsl:number/><xsl:text>;</xsl:text></xsl:for-each>`,
			src:  `<r><item/><item/><item/></r>`,
			want: "1;2;3;",
		},
		{
			name: "level single with count default ignores other names",
			body: `<xsl:for-each select="item"><xsl:number/><xsl:text>;</xsl:text></xsl:for-each>`,
			src:  `<r><other/><item/><other/><item/></r>`,
			want: "1;2;",
		},
		{
			name: "level any across tree",
			body: `<xsl:for-each select="//x"><xsl:number level="any"/><xsl:text>;</xsl:text></xsl:for-each>`,
			src:  `<r><x/><g><x/></g><x/></r>`,
			want: "1;2;3;",
		},
		{
			name: "level multiple ancestor chain",
			body: `<xsl:for-each select="//item"><xsl:number level="multiple" count="sec|item" format="1.1"/><xsl:text>;</xsl:text></xsl:for-each>`,
			src:  `<r><sec><item/><item/></sec><sec><item/></sec></r>`,
			want: "1.1;1.2;2.1;",
		},
		{
			name: "format alpha sequence single",
			body: `<xsl:for-each select="item"><xsl:number format="a"/><xsl:text>;</xsl:text></xsl:for-each>`,
			src:  `<r><item/><item/></r>`,
			want: "a;b;",
		},
	}

	const header = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">`
	const footer = `</xsl:template>
</xsl:stylesheet>`

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sheet := header + tc.body + footer
			out := strings.TrimSpace(transform(t, sheet, tc.src, nil))
			if out != tc.want {
				t.Errorf("got %q want %q", out, tc.want)
			}
		})
	}
}
