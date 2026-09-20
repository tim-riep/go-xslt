package xslt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const xslHdr = `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">`

func TestModeOnNoMatch(t *testing.T) {
	// Default (text-only-copy): unmatched element recurses, text is copied.
	def := xslHdr + `
  <xsl:output method="text"/>
  <xsl:template match="/r"><xsl:apply-templates/></xsl:template>
</xsl:stylesheet>`
	if out := strings.TrimSpace(transform(t, def, `<r>hello<keep>x</keep></r>`, nil)); out != "hellox" {
		t.Errorf("text-only-copy: got %q want %q", out, "hellox")
	}
	// deep-skip: the unmatched <r> subtree is skipped entirely (no recursion),
	// so nothing is copied — in contrast to text-only-copy above.
	ds := xslHdr + `
  <xsl:output method="text"/>
  <xsl:mode on-no-match="deep-skip"/>
  <xsl:template match="/"><xsl:apply-templates/></xsl:template>
</xsl:stylesheet>`
	if out := strings.TrimSpace(transform(t, ds, `<r>hello</r>`, nil)); out != "" {
		t.Errorf("deep-skip: got %q want %q", out, "")
	}
}

func TestNextMatch(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="text"/>
  <xsl:template match="a" priority="2">A<xsl:next-match/></xsl:template>
  <xsl:template match="a" priority="1">B</xsl:template>
  <xsl:template match="/"><xsl:apply-templates select="doc/a"/></xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<doc><a/></doc>`, nil))
	if out != "AB" {
		t.Errorf("next-match: got %q want %q", out, "AB")
	}
}

func TestApplyImports(t *testing.T) {
	dir := t.TempDir()
	imported := xslHdr + `
  <xsl:template match="a">base(<xsl:value-of select="."/>)</xsl:template>
</xsl:stylesheet>`
	writeFile(t, dir, "imp.xsl", imported)
	main := xslHdr + `
  <xsl:import href="imp.xsl"/>
  <xsl:output method="text"/>
  <xsl:template match="a">[<xsl:apply-imports/>]</xsl:template>
  <xsl:template match="/"><xsl:apply-templates select="doc/a"/></xsl:template>
</xsl:stylesheet>`
	ss, err := CompileFrom(main, dir)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := ss.Transform(`<doc><a>z</a></doc>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[base(z)]" {
		t.Errorf("apply-imports: got %q want %q", out, "[base(z)]")
	}
}

func TestTunnelParams(t *testing.T) {
	sheet := xslHdr + `
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:apply-templates select="doc/a"><xsl:with-param name="t" select="'TUN'" tunnel="yes"/></xsl:apply-templates>
  </xsl:template>
  <xsl:template match="a"><xsl:apply-templates select="b"/></xsl:template>
  <xsl:template match="b">
    <xsl:param name="t" tunnel="yes"/>
    <xsl:value-of select="$t"/>
  </xsl:template>
</xsl:stylesheet>`
	out := strings.TrimSpace(transform(t, sheet, `<doc><a><b/></a></doc>`, nil))
	if out != "TUN" {
		t.Errorf("tunnel: got %q want %q", out, "TUN")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
