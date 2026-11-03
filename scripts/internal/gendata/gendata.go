// Package gendata generates the deterministic benchmark inputs the scripts
// under scripts/ share: an orders document of any size and a JSON rendering
// of the same shape. The same seed always gives the same bytes, so a
// measurement is repeatable and two engine builds see identical input.
package gendata

import (
	"fmt"
	"math/rand"
	"strings"
)

// Orders returns an XML document of n order records (about 185 bytes each).
func Orders(n int) string {
	r := rand.New(rand.NewSource(1))
	var b strings.Builder
	b.Grow(n * 190)
	b.WriteString("<?xml version=\"1.0\"?>\n<orders>\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `  <order id="o%d"><customer>customer %d</customer><date>2024-%02d-%02d</date><price>%d.%02d</price><items>`,
			i, r.Intn(5000), 1+r.Intn(12), 1+r.Intn(28), r.Intn(1000), r.Intn(100))
		for j := 0; j < 1+r.Intn(4); j++ {
			fmt.Fprintf(&b, `<item sku="s%d" qty="%d"/>`, r.Intn(100000), 1+r.Intn(9))
		}
		b.WriteString("</items></order>\n")
	}
	b.WriteString("</orders>\n")
	return b.String()
}

// OrdersJSON returns the same n records as a JSON array of objects.
func OrdersJSON(n int) string {
	r := rand.New(rand.NewSource(1))
	var b strings.Builder
	b.Grow(n * 150)
	b.WriteString("[\n")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, `{"id":"o%d","customer":"customer %d","date":"2024-%02d-%02d","price":%d.%02d,"items":[`,
			i, r.Intn(5000), 1+r.Intn(12), 1+r.Intn(28), r.Intn(1000), r.Intn(100))
		for j := 0; j < 1+r.Intn(4); j++ {
			if j > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"sku":"s%d","qty":%d}`, r.Intn(100000), 1+r.Intn(9))
		}
		b.WriteString("]}")
	}
	b.WriteString("\n]\n")
	return b.String()
}

// Templates returns a stylesheet of n templates with distinct match
// patterns, modes and bodies, for measuring compile time alone.
func Templates(n int) string {
	var b strings.Builder
	b.WriteString(`<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:f="urn:f" exclude-result-prefixes="xs f">` + "\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `  <xsl:template match="e%d[@a = '%d' or f:p(., %d)]" mode="m%d" priority="%d">
    <xsl:param name="depth" as="xs:integer" select="0"/>
    <xsl:variable name="v" as="xs:string*" select="for $x in tokenize(@b, ',') return upper-case($x)"/>
    <r%d n="{count($v)}" d="{$depth + %d}"><xsl:apply-templates select="*" mode="m%d"><xsl:with-param name="depth" select="$depth + 1"/></xsl:apply-templates></r%d>
  </xsl:template>
`, i, i, i%7, i%50, i%5, i, i, (i+1)%50, i)
	}
	b.WriteString(`  <xsl:function name="f:p" as="xs:boolean"><xsl:param name="n" as="node()"/><xsl:param name="k" as="xs:integer"/><xsl:sequence select="count($n/*) mod 7 = $k"/></xsl:function>
</xsl:stylesheet>
`)
	return b.String()
}
