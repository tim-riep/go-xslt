package xslt

import (
	"strings"
	"testing"
)

// The static base URI of an expression is computed on demand
// (xpath.Context.StaticBase) instead of before every evaluation. These cases
// pin the xml:base semantics that must come out the same either way: the
// chain of xml:base attributes above the expression's element, resolved
// against the module's retrieval location; base-uri() of a node copied out
// of the stylesheet; a function item that reads the base after the
// evaluation that made it has returned; and xsl:evaluate's own base-uri.
func TestStaticBaseOnDemand(t *testing.T) {
	sheet := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xml:base="sheet/">
  <xsl:output method="text"/>
  <xsl:variable name="f" xml:base="fn/" select="function() { static-base-uri() }"/>
  <xsl:template match="/">
    <xsl:value-of select="static-base-uri()"/><xsl:text>&#10;</xsl:text>
    <xsl:value-of select="resolve-uri('x.xml')"/><xsl:text>&#10;</xsl:text>
    <xsl:for-each select="/*" xml:base="deep/">
      <xsl:value-of select="static-base-uri()"/><xsl:text>&#10;</xsl:text>
      <xsl:variable name="lit" as="element()"><e xml:base="inner/"><f/></e></xsl:variable>
      <xsl:value-of select="base-uri($lit/f)"/><xsl:text>&#10;</xsl:text>
      <xsl:value-of select="$f()"/><xsl:text>&#10;</xsl:text>
      <xsl:evaluate xpath="'static-base-uri()'" base-uri="http://example.org/ev/"/><xsl:text>&#10;</xsl:text>
      <xsl:evaluate xpath="'static-base-uri()'" namespace-context="/*"/><xsl:text>&#10;</xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	ss, err := CompileFrom(sheet, "/base/dir")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, _, err := ss.Transform(`<doc xml:base="src/"/>`, nil)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	want := strings.Join([]string{
		"file:///base/dir/sheet/",
		"file:///base/dir/sheet/x.xml",
		"file:///base/dir/sheet/deep/",
		"file:///base/dir/sheet/deep/inner/",
		"file:///base/dir/sheet/fn/",
		"http://example.org/ev/",
		"src/",
		"",
	}, "\n")
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}
