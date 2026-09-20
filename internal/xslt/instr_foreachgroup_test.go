package xslt

import "testing"

func TestForEachGroup(t *testing.T) {
	const hdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>`

	cases := []struct {
		name  string
		sheet string
		src   string
		want  string
	}{
		{
			name: "group-by key and members",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="item" group-by="@cat">
      <xsl:value-of select="current-grouping-key()"/>=<xsl:value-of select="current-group()" separator=","/>;</xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item cat="a">1</item><item cat="b">2</item><item cat="a">3</item><item cat="b">4</item></r>`,
			want: "a=1,3;b=2,4;",
		},
		{
			name: "group-by preserves first-appearance order of keys",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="item" group-by="@cat">
      <xsl:value-of select="current-grouping-key()"/></xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item cat="z"/><item cat="m"/><item cat="z"/><item cat="a"/></r>`,
			want: "zma",
		},
		{
			name: "group-adjacent starts new group when key changes",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="item" group-adjacent="@cat">[<xsl:value-of select="current-grouping-key()"/>:<xsl:value-of select="current-group()" separator=","/>]</xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item cat="a">1</item><item cat="a">2</item><item cat="b">3</item><item cat="a">4</item></r>`,
			want: "[a:1,2][b:3][a:4]",
		},
		{
			name: "group-starting-with",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="*" group-starting-with="h">(<xsl:value-of select="current-group()" separator=","/>)</xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><p>x</p><h>A</h><p>1</p><p>2</p><h>B</h><p>3</p></r>`,
			want: "(x)(A,1,2)(B,3)",
		},
		{
			name: "group-ending-with",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="*" group-ending-with="end">(<xsl:value-of select="current-group()" separator=","/>)</xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><p>1</p><p>2</p><end>E1</end><p>3</p><end>E2</end></r>`,
			want: "(1,2,E1)(3,E2)",
		},
		{
			name: "sort groups by key descending",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="item" group-by="@cat">
      <xsl:sort select="current-grouping-key()" order="descending"/>
      <xsl:value-of select="current-grouping-key()"/></xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item cat="b"/><item cat="a"/><item cat="c"/></r>`,
			want: "cba",
		},
		{
			name: "sort groups by size of group numerically",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="item" group-by="@cat">
      <xsl:sort select="count(current-group())" data-type="number" order="descending"/>
      <xsl:value-of select="current-grouping-key()"/>:<xsl:value-of select="count(current-group())"/> </xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><item cat="a"/><item cat="b"/><item cat="b"/><item cat="b"/><item cat="a"/></r>`,
			want: "b:3a:2",
		},
		{
			name: "group-by computes key from content",
			sheet: hdr + `
  <xsl:template match="/r">
    <xsl:for-each-group select="n" group-by=". mod 2">
      <xsl:value-of select="current-grouping-key()"/>:<xsl:value-of select="current-group()" separator=","/>;</xsl:for-each-group>
  </xsl:template>
</xsl:stylesheet>`,
			src:  `<r><n>1</n><n>2</n><n>3</n><n>4</n></r>`,
			want: "1:1,3;0:2,4;",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transform(t, tc.sheet, tc.src, nil)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
