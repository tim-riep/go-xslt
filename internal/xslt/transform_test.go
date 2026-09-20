package xslt

import (
	"strings"
	"testing"
)

func transform(t *testing.T, sheet, src string, params map[string]string) string {
	t.Helper()
	ss, err := Compile(sheet)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, _, err := ss.Transform(src, params)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	return out
}

const ident = `<?xml version="1.0"?>
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">`

func TestValueOfAndApply(t *testing.T) {
	sheet := ident + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/catalog">
    <result><xsl:apply-templates select="book"/></result>
  </xsl:template>
  <xsl:template match="book">
    <item><xsl:value-of select="title"/></item>
  </xsl:template>
</xsl:stylesheet>`
	src := `<catalog><book><title>A</title></book><book><title>B</title></book></catalog>`
	out := transform(t, sheet, src, nil)
	want := `<result><item>A</item><item>B</item></result>`
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestForEachIfChoose(t *testing.T) {
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:template match="/list">
    <xsl:for-each select="n">
      <xsl:choose>
        <xsl:when test=". > 1">big </xsl:when>
        <xsl:otherwise>small </xsl:otherwise>
      </xsl:choose>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	src := `<list><n>1</n><n>5</n><n>0</n></list>`
	out := strings.TrimSpace(transform(t, sheet, src, nil))
	want := "small big  small"
	if strings.Join(strings.Fields(out), " ") != strings.Join(strings.Fields(want), " ") {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestAttributesAndAVT(t *testing.T) {
	sheet := ident + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/r">
    <out id="{@id}-x"><xsl:attribute name="count"><xsl:value-of select="count(item)"/></xsl:attribute></out>
  </xsl:template>
</xsl:stylesheet>`
	src := `<r id="7"><item/><item/></r>`
	out := transform(t, sheet, src, nil)
	if !strings.Contains(out, `id="7-x"`) || !strings.Contains(out, `count="2"`) {
		t.Errorf("got %q", out)
	}
}

func TestParamsAndCallTemplate(t *testing.T) {
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:param name="greeting">Hello</xsl:param>
  <xsl:template match="/">
    <xsl:call-template name="say"><xsl:with-param name="who" select="'World'"/></xsl:call-template>
  </xsl:template>
  <xsl:template name="say">
    <xsl:param name="who"/>
    <xsl:value-of select="$greeting"/>, <xsl:value-of select="$who"/>!
  </xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<x/>`, map[string]string{"greeting": "Hi"}))
	if !strings.Contains(out, "Hi, World!") {
		t.Errorf("got %q", out)
	}
}

func TestSort(t *testing.T) {
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:template match="/list">
    <xsl:for-each select="n">
      <xsl:sort select="." data-type="number" order="descending"/>
      <xsl:value-of select="."/><xsl:text> </xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<list><n>3</n><n>1</n><n>2</n></list>`, nil))
	if out != "3 2 1" {
		t.Errorf("got %q want %q", out, "3 2 1")
	}
}

func TestBuiltinTemplatesAndHTMLMethod(t *testing.T) {
	sheet := ident + `
  <xsl:output method="html"/>
  <xsl:template match="/">
    <html><body><xsl:apply-templates/></body></html>
  </xsl:template>
</xsl:stylesheet>`
	out := transform(t, sheet, `<doc>Hello <b>world</b></doc>`, nil)
	if !strings.Contains(out, "<html>") || !strings.Contains(out, "Hello world") {
		t.Errorf("got %q", out)
	}
}

func TestCopyOf(t *testing.T) {
	sheet := ident + `
  <xsl:output method="xml" omit-xml-declaration="yes"/>
  <xsl:template match="/r"><out><xsl:copy-of select="keep"/></out></xsl:template>
</xsl:stylesheet>`
	out := transform(t, sheet, `<r><keep><a x="1">t</a></keep></r>`, nil)
	if !strings.Contains(out, `<keep><a x="1">t</a></keep>`) {
		t.Errorf("got %q", out)
	}
}

func TestPriorityMatching(t *testing.T) {
	// More specific pattern (book[@special]) should win over book.
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:template match="book">plain </xsl:template>
  <xsl:template match="book[@special]">special </xsl:template>
  <xsl:template match="/cat"><xsl:apply-templates select="book"/></xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<cat><book/><book special="1"/></cat>`, nil))
	if strings.Join(strings.Fields(out), " ") != "plain special" {
		t.Errorf("got %q", out)
	}
}
