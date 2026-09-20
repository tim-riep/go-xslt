package xslt

import (
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:key name="bycat" match="book" use="@cat"/>
  <xsl:template match="/catalog">
    <xsl:for-each select="key('bycat','go')">
      <xsl:value-of select="title"/><xsl:text>;</xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	src := `<catalog>
    <book cat="go"><title>Go</title></book>
    <book cat="xml"><title>XSLT</title></book>
    <book cat="go"><title>Effective Go</title></book>
  </catalog>`
	out := strings.TrimSpace(transform(t, sheet, src, nil))
	if out != "Go;Effective Go;" {
		t.Errorf("got %q", out)
	}
}

func TestUserFunction(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:my="urn:my">
  <xsl:output method="text"/>
  <xsl:function name="my:double">
    <xsl:param name="n"/>
    <xsl:sequence select="$n * 2"/>
  </xsl:function>
  <xsl:template match="/r"><xsl:value-of select="my:double(number(.))"/></xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r>21</r>`, nil))
	if out != "42" {
		t.Errorf("got %q want 42", out)
	}
}

func TestAnalyzeString(t *testing.T) {
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:analyze-string select="." regex="(\d+)-(\d+)">
      <xsl:matching-substring>[<xsl:value-of select="regex-group(1)"/>/<xsl:value-of select="regex-group(2)"/>]</xsl:matching-substring>
      <xsl:non-matching-substring><xsl:value-of select="."/></xsl:non-matching-substring>
    </xsl:analyze-string>
  </xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r>a 12-34 b</r>`, nil))
	if out != "a [12/34] b" {
		t.Errorf("got %q want %q", out, "a [12/34] b")
	}
}

func TestSequencesInXSLT(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:my="urn:my">
  <xsl:output method="text"/>
  <xsl:function name="my:fact">
    <xsl:param name="n"/>
    <xsl:sequence select="if ($n &lt;= 1) then 1 else $n * my:fact($n - 1)"/>
  </xsl:function>
  <xsl:template match="/r">
    <xsl:for-each select="1 to 4">
      <xsl:value-of select="."/>!=<xsl:value-of select="my:fact(.)"/><xsl:text> </xsl:text>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<r/>`, nil))
	want := "1!=1 2!=2 3!=6 4!=24"
	if strings.Join(strings.Fields(out), " ") != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestRegexAndStringFuncs(t *testing.T) {
	sheet := ident + `
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:value-of select="replace(., 'a', 'X')"/><xsl:text>|</xsl:text>
    <xsl:value-of select="matches(., '^h')"/><xsl:text>|</xsl:text>
    <xsl:value-of select="string-join(item, ',')"/>
  </xsl:template>
</xsl:stylesheet>`
	src := `<r>haha<item>x</item><item>y</item><item>z</item></r>`
	out := strings.TrimSpace(transform(t, sheet, src, nil))
	// note: string-value of /r includes child text; replace operates on it.
	if !strings.Contains(out, "|true|x,y,z") {
		t.Errorf("got %q", out)
	}
}
