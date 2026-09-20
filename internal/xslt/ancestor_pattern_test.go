package xslt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A match pattern with an ancestor:: predicate must exclude nodes under that
// ancestor (regression for the BR-CL-11 false positive caused by explicit axes
// being mis-lexed).
func TestAncestorInMatchPattern(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:ram="urn:ram">
  <xsl:output method="text"/>
  <xsl:template match="text()"/>
  <xsl:template match="ram:ID[@schemeID][not(ancestor::ram:Reg)]">M:<xsl:value-of select="@schemeID"/> </xsl:template>
  <xsl:template match="/"><xsl:apply-templates select="//ram:ID"/></xsl:template>
</xsl:stylesheet>`
	src := `<root xmlns:ram="urn:ram">
    <ram:Party><ram:ID schemeID="0088">GLN</ram:ID></ram:Party>
    <ram:Reg><ram:ID schemeID="VA">VAT</ram:ID></ram:Reg>
  </root>`
	out := strings.TrimSpace(transform(t, sheet, src, nil))
	// Only the ID outside <ram:Reg> matches; the VAT id under ram:Reg is excluded.
	if out != "M:0088" {
		t.Errorf("ancestor pattern: got %q want %q", out, "M:0088")
	}
}

// End-to-end: the real EN16931 stylesheet against the example must NOT emit
// BR-CL-11 (the only schemeID, "VA", is under ram:SpecifiedTaxRegistration,
// which the rule excludes via not(ancestor::ram:SpecifiedTaxRegistration)).
func TestEN16931NoFalseBRCL11(t *testing.T) {
	root := filepath.Join("..", "..", "local", "en16931")
	xslt, err := os.ReadFile(filepath.Join(root, "EN16931-CII-validation.xslt"))
	if err != nil {
		t.Skip("stylesheet not present")
	}
	xml, err := os.ReadFile(filepath.Join(root, "CII_example8.xml"))
	if err != nil {
		t.Skip("example not present")
	}
	ss, err := Compile(string(xslt))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, _, err := ss.Transform(string(xml), nil)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if strings.Contains(out, "BR-CL-11") {
		t.Errorf("BR-CL-11 was emitted as a false positive")
	}
}
