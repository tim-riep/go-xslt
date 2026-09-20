package xslt

import (
	"strings"
	"testing"
)

func TestPerformSort(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">`

	cases := []struct {
		name  string
		sheet string
		src   string
		want  string
	}{
		{
			name: "select with text output sorted ascending",
			sheet: hdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:perform-sort select="item">
      <xsl:sort select="."/>
    </xsl:perform-sort>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item>banana</item><item>apple</item><item>cherry</item></r>`,
			want: "applebananacherry",
		},
		{
			name: "select sorted descending",
			sheet: hdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:perform-sort select="item">
      <xsl:sort select="." order="descending"/>
    </xsl:perform-sort>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item>banana</item><item>apple</item><item>cherry</item></r>`,
			want: "cherrybananaapple",
		},
		{
			name: "numeric sort",
			sheet: hdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:perform-sort select="n">
      <xsl:sort select="." data-type="number"/>
    </xsl:perform-sort>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><n>10</n><n>2</n><n>1</n><n>21</n></r>`,
			want: "121021",
		},
		{
			name: "body supplies input sequence",
			sheet: hdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:perform-sort>
      <xsl:sort select="."/>
      <xsl:for-each select="item">
        <v><xsl:value-of select="."/></v>
      </xsl:for-each>
    </xsl:perform-sort>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item>gamma</item><item>alpha</item><item>beta</item></r>`,
			want: "alphabetagamma",
		},
		{
			name: "result is element nodes preserved in xml output",
			sheet: hdr + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/r">
    <out><xsl:perform-sort select="item"><xsl:sort select="@k" data-type="number"/></xsl:perform-sort></out>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item k="3">c</item><item k="1">a</item><item k="2">b</item></r>`,
			want: `<out><item k="1">a</item><item k="2">b</item><item k="3">c</item></out>`,
		},
		{
			name: "no sort keys keeps input order",
			sheet: hdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:perform-sort select="item"/>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item>z</item><item>a</item><item>m</item></r>`,
			want: "zam",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.TrimSpace(transform(t, tc.sheet, tc.src, nil))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
