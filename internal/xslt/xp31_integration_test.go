package xslt

import (
	"strings"
	"testing"
)

// TestXPath31InStylesheet exercises XPath 3.1 language features and F&O
// functions through the XSLT engine (i.e. via the engine facade), confirming
// the typed value model and new functions are reachable from select/test.
func TestXPath31InStylesheet(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:value-of select="'a' || '-' || 'b'"/><xsl:text>|</xsl:text>
    <xsl:value-of select="(1 to 5) => count()"/><xsl:text>|</xsl:text>
    <xsl:value-of select="year-from-date(xs:date('2020-05-01'))"/><xsl:text>|</xsl:text>
    <xsl:value-of select="upper-case('hi') eq 'HI'"/><xsl:text>|</xsl:text>
    <xsl:value-of select="string-join((1,2,3) ! string(. * . ), ',')"/><xsl:text>|</xsl:text>
    <xsl:value-of select="'5' cast as xs:integer + 2"/>
  </xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r/>`, nil))
	want := "a-b|5|2020|true|1,4,9|7"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}
