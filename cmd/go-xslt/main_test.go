package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/internal/fileuri"
)

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	style := write("s.xsl", `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:param name="who" select="'world'"/>
  <xsl:output method="text"/>
  <xsl:template match="/">hello <xsl:value-of select="$who"/> (<xsl:value-of select="count(//i)"/>)<xsl:result-document href="sub/side.txt">side</xsl:result-document></xsl:template>
  <xsl:template name="xsl:initial-template">no source</xsl:template>
</xsl:stylesheet>`)
	src := write("in.xml", `<r><i/><i/></r>`)
	bad := write("bad.xsl", `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="/"><xsl:value-of select="1 +"/></xsl:template>
</xsl:stylesheet>`)
	schema := write("s.xsd", `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"><xs:element name="n" type="xs:int"/></xs:schema>`)
	good := write("good.xml", `<n>5</n>`)
	invalid := write("invalid.xml", `<n>five</n>`)
	hinted := write("hinted.xml", `<n xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:noNamespaceSchemaLocation="s.xsd">x</n>`)
	embedded := write("emb.xml", `<?xml-stylesheet type="text/xsl" href="#s"?>
<!DOCTYPE d [<!ATTLIST xsl:stylesheet id ID #REQUIRED>]>
<d xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:stylesheet id="s" version="3.0"><xsl:output method="text"/>
<xsl:template match="/">embedded</xsl:template></xsl:stylesheet></d>`)
	out := filepath.Join(dir, "out")
	nsDoc := write("ns.xml", `<r xmlns="urn:q"/>`)
	slow := write("slow.xsl", `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template name="xsl:initial-template"><xsl:iterate select="1 to 9000000"><xsl:value-of select="."/></xsl:iterate></xsl:template>
</xsl:stylesheet>`)

	for _, c := range []struct {
		name   string
		args   []string
		code   int
		stdout string // substring
		stderr string // substring
	}{
		{"transform", []string{"transform", "-s", style, "-p", "who=you", "-d", out, src}, 0, "hello you (2)", ""},
		{"stdin", []string{"transform", "-s", style, "-"}, 0, "hello world (1)", "not written"},
		{"no source", []string{"transform", "-s", style}, 0, "no source", ""},
		{"compile error", []string{"transform", "-s", bad, src}, 1, "", "bad.xsl:2:"},
		{"embedded", []string{"transform", embedded}, 0, "embedded", ""},
		{"valid", []string{"validate", "-x", schema, good}, 0, "valid", ""},
		{"invalid", []string{"validate", "-x", schema, invalid}, 1, "invalid", "error"},
		{"hints", []string{"validate", hinted}, 1, "invalid", ""},
		{"xpath", []string{"xpath", "count(//i), 'x'", src}, 0, "2\nx\n", ""},
		{"xpath stdin", []string{"xpath", "count(//i)", "-"}, 0, "1\n", ""},
		{"long flags", []string{"transform", "--stylesheet", style, "--param", "who=all", "--output-dir", out, src}, 0, "hello all (2)", ""},
		{"timeout", []string{"transform", "-s", slow, "--timeout", "1ns"}, 1, "", "stopped"},
		{"max output", []string{"transform", "-s", style, "--max-output", "3", src}, 1, "", "XPDY0130"},
		{"xpath typed", []string{"xpath", "-t", "1 + 1"}, 0, "xs:integer\t2", ""},
		{"xpath ns", []string{"xpath", "--ns", "q=urn:q", "--var", "s=x", "--var-expr", "n=2", "concat($s, $n, count(/q:r))", nsDoc}, 0, "x21\n", ""},
		{"param-expr", []string{"transform", "-s", style, "--param-expr", "who=1+1", src}, 0, "hello 2 (2)", ""},
		{"xpath error", []string{"xpath", "1 +"}, 1, "", "XPST0003"},
		{"usage", []string{"frobnicate"}, 2, "", "unknown command"},
	} {
		var so, se bytes.Buffer
		code := run(c.args, strings.NewReader(`<r><i/></r>`), &so, &se)
		if code != c.code || !strings.Contains(so.String(), c.stdout) || !strings.Contains(se.String(), c.stderr) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", c.name, code, so.String(), se.String())
		}
	}
	if b, err := os.ReadFile(filepath.Join(out, "sub", "side.txt")); err != nil || string(b) != "side" {
		t.Errorf("secondary output: %q %v", b, err)
	}
}

// With the stylesheet and the source in different directories, the source
// document's URI is the source file, and relative references taken from it
// resolve beside it.
func TestCLISourceURI(t *testing.T) {
	root := t.TempDir()
	styleDir, srcDir := filepath.Join(root, "sub"), filepath.Join(root, "data")
	for _, d := range []string{styleDir, srcDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, text string) {
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	style := filepath.Join(styleDir, "t.xsl")
	write(style, `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/">du=<xsl:value-of select="document-uri(/)"/>|bu=<xsl:value-of select="base-uri(/)"/>|doc=<xsl:value-of select="document(/r/@href, /)/o"/>|rel=<xsl:value-of select="doc(resolve-uri('other.xml', base-uri(/)))/o"/></xsl:template>
</xsl:stylesheet>`)
	src := filepath.Join(srcDir, "in.xml")
	write(src, `<r href="other.xml"/>`)
	write(filepath.Join(srcDir, "other.xml"), `<o>other</o>`)

	var so, se bytes.Buffer
	if code := run([]string{"transform", "-s", style, src}, strings.NewReader(""), &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	want := fileuri.FromPath(src)
	if got := so.String(); got != "du="+want+"|bu="+want+"|doc=other|rel=other" {
		t.Errorf("got %q, want document-uri and base-uri %q", got, want)
	}

	// stdin has no location: no document URI, and the base URI is the
	// stylesheet's directory, as before.
	so.Reset()
	se.Reset()
	plain := filepath.Join(styleDir, "p.xsl")
	write(plain, `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/">du=<xsl:value-of select="document-uri(/)"/>|bu=<xsl:value-of select="base-uri(/)"/></xsl:template>
</xsl:stylesheet>`)
	if code := run([]string{"transform", "-s", plain, "-"}, strings.NewReader(`<r/>`), &so, &se); code != 0 {
		t.Fatalf("stdin exit %d: %s", code, se.String())
	}
	if got := so.String(); !strings.HasPrefix(got, "du=|bu="+fileuri.FromPath(styleDir)) {
		t.Errorf("stdin: got %q, want no document-uri and the stylesheet directory as base-uri", got)
	}
}
