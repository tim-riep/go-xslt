package xslt

import (
	"strings"
	"testing"
)

func TestTextValueTemplates(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" expand-text="yes">
  <xsl:output method="text"/>
  <xsl:template match="/r">Hello {@name}, {count(item)} items; {{literal}}</xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r name="Tim"><item/><item/></r>`, nil))
	if out != "Hello Tim, 2 items; {literal}" {
		t.Errorf("TVT: got %q", out)
	}
}

func TestValueOfContent(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r"><xsl:value-of>computed <xsl:value-of select="1+2"/></xsl:value-of></xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r/>`, nil))
	if out != "computed 3" {
		t.Errorf("value-of content: got %q", out)
	}
}

func TestAttributeSet(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:attribute-set name="common">
    <xsl:attribute name="class">box</xsl:attribute>
    <xsl:attribute name="lang">en</xsl:attribute>
  </xsl:attribute-set>
  <xsl:template match="/r"><div xsl:use-attribute-sets="common" id="x"/></xsl:template>
</xsl:stylesheet>`
	out := transform(t, sheet, `<r/>`, nil)
	if !strings.Contains(out, `class="box"`) || !strings.Contains(out, `lang="en"`) || !strings.Contains(out, `id="x"`) {
		t.Errorf("attribute-set: got %q", out)
	}
}

func TestNamespaceInstr(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/r">
    <out><xsl:namespace name="foo" select="'urn:foo'"/></out>
  </xsl:template>
</xsl:stylesheet>`
	out := transform(t, sheet, `<r/>`, nil)
	if !strings.Contains(out, `xmlns:foo="urn:foo"`) {
		t.Errorf("namespace: got %q", out)
	}
}

func TestWherePopulated(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/r"><out><xsl:where-populated><x><xsl:value-of select="a"/></x></xsl:where-populated><xsl:where-populated><y><xsl:value-of select="missing"/></y></xsl:where-populated></out></xsl:template>
</xsl:stylesheet>`
	// <x> has content (a=hi) -> kept; <y> is empty... but <y> always has the
	// element node, so where-populated keeps it only if it has populated content.
	out := transform(t, sheet, `<r><a>hi</a></r>`, nil)
	if !strings.Contains(out, "<x>hi</x>") {
		t.Errorf("where-populated (populated): got %q", out)
	}
}

func TestDocumentInstr(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="text"/>
  <xsl:variable name="d"><xsl:document><v>42</v></xsl:document></xsl:variable>
  <xsl:template match="/r"><xsl:value-of select="$d"/></xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r/>`, nil))
	if out != "42" {
		t.Errorf("document: got %q", out)
	}
}
