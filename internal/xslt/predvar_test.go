package xslt

import (
	"strings"
	"testing"
)

// Mirrors the EN16931 failure: a quantified variable ($Currency) referenced
// inside a predicate within an xsl:when/@test. Regression for local-variable
// scope loss in predicates.
func TestQuantifiedVarInTestPredicate(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="text"/>
  <xsl:template match="/inv">
    <xsl:choose>
      <xsl:when test="every $cur in currency satisfies count(amt[@c = $cur]) = 2">OK</xsl:when>
      <xsl:otherwise>FAIL</xsl:otherwise>
    </xsl:choose>
  </xsl:template>
</xsl:stylesheet>`
	src := `<inv><currency>EUR</currency><amt c="EUR">1</amt><amt c="EUR">2</amt><amt c="USD">9</amt></inv>`
	out := strings.TrimSpace(transform(t, sheet, src, nil))
	if out != "OK" {
		t.Errorf("got %q want OK", out)
	}
}
