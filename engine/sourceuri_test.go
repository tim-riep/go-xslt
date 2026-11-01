package engine_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/engine"
	"github.com/tim-riep/go-xslt/internal/fileuri"
)

// sourceLayout makes a stylesheet directory and a different source directory;
// other.xml exists only beside the source.
func sourceLayout(t *testing.T) (styleDir, srcDir string) {
	t.Helper()
	root := t.TempDir()
	styleDir = filepath.Join(root, "sub")
	srcDir = filepath.Join(root, "data dir")
	for _, d := range []string{styleDir, srcDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcDir, "other.xml"), []byte(`<o>other</o>`), 0o600); err != nil {
		t.Fatal(err)
	}
	return styleDir, srcDir
}

const sourceURIXSL = `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:text>du=</xsl:text><xsl:value-of select="document-uri(/)"/>
    <xsl:text>|bu=</xsl:text><xsl:value-of select="base-uri(/)"/>
    <xsl:text>|doc=</xsl:text><xsl:value-of select="document('other.xml', /)/o = 'other' and doc(resolve-uri('other.xml', base-uri(/)))/o = 'other'"/>
  </xsl:template>
</xsl:stylesheet>`

func TestSourceURIIsTheSourceDocumentURI(t *testing.T) {
	styleDir, srcDir := sourceLayout(t)
	srcPath := filepath.Join(srcDir, "in.xml")
	want := fileuri.FromPath(srcPath)
	sec := engine.Security{AllowedDirs: []string{srcDir}}

	check := func(name, got string) {
		t.Helper()
		if !strings.Contains(got, "du="+want+"|") || !strings.Contains(got, "|bu="+want+"|") || !strings.HasSuffix(got, "|doc=true") {
			t.Errorf("%s: got %q, want document-uri and base-uri %q and doc=true", name, got, want)
		}
	}
	norm := func(s string) string { return s }

	// A plain path and the equivalent file: URI behave the same.
	for _, su := range []string{srcPath, want} {
		req := engine.Request{Stylesheet: sourceURIXSL, Source: `<s/>`, SourceURI: su, BaseDir: styleDir, Security: sec}
		res := engine.Transform(req)
		check("Transform "+su, norm(res.Output))
		if len(res.Diagnostics) > 0 {
			t.Fatalf("%+v", res.Diagnostics)
		}
		var buf bytes.Buffer
		res = engine.TransformTo(&buf, req)
		check("TransformTo "+su, norm(buf.String()))

		sheet, diags := engine.Compile(req)
		if len(diags) > 0 {
			t.Fatalf("%+v", diags)
		}
		tr := engine.TransformRequest{Source: `<s/>`, SourceURI: su, Security: sec}
		check("Stylesheet.Transform "+su, norm(sheet.Transform(tr).Output))
		buf.Reset()
		sheet.TransformTo(&buf, tr)
		check("Stylesheet.TransformTo "+su, norm(buf.String()))
	}
}

// Without a SourceURI the source is parsed against BaseDir, as before: its
// base URI is the stylesheet's directory and relative doc() resolves there.
func TestWithoutSourceURIBaseDirStillRules(t *testing.T) {
	styleDir, srcDir := sourceLayout(t)
	if err := os.WriteFile(filepath.Join(styleDir, "other.xml"), []byte(`<o>other</o>`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := engine.Transform(engine.Request{Stylesheet: sourceURIXSL, Source: `<s/>`, BaseDir: styleDir})
	out := res.Output
	dirURI := fileuri.FromPath(styleDir)
	if !strings.Contains(out, "|bu="+dirURI) || !strings.HasSuffix(out, "|doc=true") {
		t.Errorf("got %q (%+v), want base-uri under %q and doc=true", out, res.Diagnostics, dirURI)
	}
	if strings.Contains(out, filepath.Base(srcDir)) {
		t.Errorf("source dir leaked into %q", out)
	}
}
