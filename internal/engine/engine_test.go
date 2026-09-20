package engine

import (
	"strings"
	"testing"
)

func TestTransformSample(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="html" indent="yes"/>
  <xsl:template match="/catalog">
    <html><body><h1>Books</h1><ul><xsl:apply-templates select="book"/></ul></body></html>
  </xsl:template>
  <xsl:template match="book">
    <li><xsl:value-of select="title"/> — <xsl:value-of select="author"/></li>
  </xsl:template>
</xsl:stylesheet>`
	src := `<catalog><book><title>Go</title><author>K&amp;D</author></book></catalog>`

	res := Transform(Request{Stylesheet: sheet, Source: src})
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %+v", res.Diagnostics)
	}
	if res.Method != "html" {
		t.Errorf("method = %q, want html", res.Method)
	}
	if !strings.Contains(res.Output, "<li>Go — K&amp;D</li>") {
		t.Errorf("output missing list item:\n%s", res.Output)
	}
}

func TestCompileErrorHasLine(t *testing.T) {
	// Unknown instruction should surface a diagnostic.
	sheet := `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="/"><xsl:bogus/></xsl:template>
</xsl:stylesheet>`
	res := Transform(Request{Stylesheet: sheet, Source: "<x/>"})
	if !res.HasErrors() {
		t.Fatalf("expected an error diagnostic")
	}
	if res.Diagnostics[0].Line == 0 {
		t.Errorf("expected a line number, got %+v", res.Diagnostics[0])
	}
}

func TestEmptyStylesheet(t *testing.T) {
	res := Transform(Request{Stylesheet: ""})
	if !res.HasErrors() {
		t.Fatal("expected error for empty stylesheet")
	}
}
