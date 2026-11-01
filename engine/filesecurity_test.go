package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/engine"
	"github.com/tim-riep/go-xslt/xsd"
)

// fileLayout makes a project directory and, beside it, a directory the project
// must not be able to read.
func fileLayout(t *testing.T) (project, outside string) {
	t.Helper()
	root := t.TempDir()
	project = filepath.Join(root, "project")
	outside = filepath.Join(root, "outside")
	for _, d := range []string{project, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "in.xml"), `<v>inside</v>`)
	write(filepath.Join(outside, "secret.txt"), `TOPSECRET`)
	write(filepath.Join(outside, "secret.xml"), `<v>TOPSECRET</v>`)
	write(filepath.Join(outside, "secret.dtd"), `<!ENTITY leak "TOPSECRET">`)
	write(filepath.Join(outside, "lib.xsl"), `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:variable name="lib" select="'TOPSECRET'"/></xsl:stylesheet>`)
	write(filepath.Join(outside, "lib.xsd"), `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"><xs:element name="secret"/></xs:schema>`)
	return project, outside
}

func readerXSL(expr string) string {
	return `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template name="xsl:initial-template"><out><xsl:value-of select="` + expr + `"/></out></xsl:template>
</xsl:stylesheet>`
}

func run(t *testing.T, project, expr string, sec engine.Security) engine.Result {
	t.Helper()
	return engine.Transform(engine.Request{Stylesheet: readerXSL(expr), InitialTemplate: "xsl:initial-template",
		BaseDir: project, Security: sec})
}

func TestFilesInsideBaseDirAreReadable(t *testing.T) {
	project, _ := fileLayout(t)
	res := run(t, project, `doc('in.xml')/v || ' ' || unparsed-text-available('in.xml')`, engine.Security{})
	if !strings.Contains(res.Output, "<out>inside true</out>") {
		t.Fatalf("got %s %+v", res.Output, res.Diagnostics)
	}
}

func TestFilesOutsideBaseDirAreRefused(t *testing.T) {
	project, outside := fileLayout(t)
	secretTxt := filepath.ToSlash(filepath.Join(outside, "secret.txt"))
	secretXML := filepath.ToSlash(filepath.Join(outside, "secret.xml"))
	for _, expr := range []string{
		`unparsed-text-available('../outside/secret.txt')`,
		`unparsed-text-available('` + secretTxt + `')`,
		`unparsed-text-available('file://` + secretTxt + `')`,
		`doc-available('../outside/secret.xml')`,
		`doc-available('` + secretXML + `')`,
	} {
		res := run(t, project, expr, engine.Security{})
		if !strings.Contains(res.Output, "<out>false</out>") {
			t.Errorf("%s: got %s %+v", expr, res.Output, res.Diagnostics)
		}
		// Control: the same read succeeds once the directory is allowed.
		res = run(t, project, expr, engine.Security{AllowedDirs: []string{outside}})
		if !strings.Contains(res.Output, "<out>true</out>") {
			t.Errorf("%s with AllowedDirs: got %s %+v", expr, res.Output, res.Diagnostics)
		}
	}
	res := run(t, project, `doc-available('`+secretXML+`')`, engine.Security{AllowAllFiles: true})
	if !strings.Contains(res.Output, "<out>true</out>") {
		t.Errorf("AllowAllFiles: got %s %+v", res.Output, res.Diagnostics)
	}
}

func TestSymlinkOutOfBaseDirIsRefused(t *testing.T) {
	project, outside := fileLayout(t)
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(project, "link.txt")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	res := run(t, project, `unparsed-text-available('link.txt')`, engine.Security{})
	if !strings.Contains(res.Output, "<out>false</out>") {
		t.Fatalf("a link out of the project was followed: %s", res.Output)
	}
}

func TestNoBaseDirReadsNoFiles(t *testing.T) {
	project, _ := fileLayout(t)
	in := filepath.ToSlash(filepath.Join(project, "in.xml"))
	res := engine.Transform(engine.Request{Stylesheet: readerXSL(`doc-available('` + in + `')`), InitialTemplate: "xsl:initial-template"})
	if !strings.Contains(res.Output, "<out>false</out>") {
		t.Fatalf("without BaseDir no file may be read, got %s %+v", res.Output, res.Diagnostics)
	}
}

func TestIncludeOutsideBaseDirIsRefused(t *testing.T) {
	project, outside := fileLayout(t)
	src := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:include href="../outside/lib.xsl"/>
  <xsl:template name="xsl:initial-template"><out><xsl:value-of select="$lib"/></out></xsl:template>
</xsl:stylesheet>`
	res := engine.Transform(engine.Request{Stylesheet: src, InitialTemplate: "xsl:initial-template", BaseDir: project})
	if !res.HasErrors() || strings.Contains(res.Output, "TOPSECRET") {
		t.Fatalf("an include outside BaseDir must fail, got %s %+v", res.Output, res.Diagnostics)
	}
	res = engine.Transform(engine.Request{Stylesheet: src, InitialTemplate: "xsl:initial-template", BaseDir: project,
		Security: engine.Security{AllowedDirs: []string{outside}}})
	if !strings.Contains(res.Output, "TOPSECRET") {
		t.Fatalf("control: with AllowedDirs the include works, got %s %+v", res.Output, res.Diagnostics)
	}
}

func TestSourceDTDOutsideBaseDirIsNotRead(t *testing.T) {
	project, outside := fileLayout(t)
	source := `<!DOCTYPE r SYSTEM "` + filepath.Join(outside, "secret.dtd") + `"><r>&leak;</r>`
	xsl := `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="/"><out><xsl:value-of select="."/></out></xsl:template>
</xsl:stylesheet>`
	res := engine.Transform(engine.Request{Stylesheet: xsl, Source: source, BaseDir: project})
	if strings.Contains(res.Output, "TOPSECRET") {
		t.Fatalf("the source's DOCTYPE read a file outside BaseDir: %s", res.Output)
	}
	res = engine.Transform(engine.Request{Stylesheet: xsl, Source: source, BaseDir: project,
		Security: engine.Security{AllowedDirs: []string{outside}}})
	if !strings.Contains(res.Output, "TOPSECRET") {
		t.Fatalf("control: with AllowedDirs the entity expands, got %s %+v", res.Output, res.Diagnostics)
	}
}

func TestCompiledStylesheetRunIsLimited(t *testing.T) {
	project, outside := fileLayout(t)
	sheet, diags := engine.Compile(engine.Request{Stylesheet: readerXSL(`unparsed-text-available('../outside/secret.txt')`), BaseDir: project})
	if sheet == nil {
		t.Fatal(diags)
	}
	if res := sheet.Transform(engine.TransformRequest{InitialTemplate: "xsl:initial-template"}); !strings.Contains(res.Output, "<out>false</out>") {
		t.Errorf("default run: got %s %+v", res.Output, res.Diagnostics)
	}
	res := sheet.Transform(engine.TransformRequest{InitialTemplate: "xsl:initial-template",
		Security: engine.Security{AllowedDirs: []string{outside}}})
	if !strings.Contains(res.Output, "<out>true</out>") {
		t.Errorf("run with AllowedDirs: got %s %+v", res.Output, res.Diagnostics)
	}
}

func TestXPathFilesAreLimited(t *testing.T) {
	project, outside := fileLayout(t)
	expr := `unparsed-text-available('../outside/secret.txt')`
	if r := engine.EvalXPath(engine.XPathRequest{Expression: expr, BaseDir: project}); r.Value != "false" {
		t.Errorf("default: got %q %+v", r.Value, r.Diagnostics)
	}
	if r := engine.EvalXPath(engine.XPathRequest{Expression: expr, BaseDir: project,
		Security: engine.Security{AllowedDirs: []string{outside}}}); r.Value != "true" {
		t.Errorf("AllowedDirs: got %q %+v", r.Value, r.Diagnostics)
	}
}

func TestSchemaImportsAreLimited(t *testing.T) {
	project, outside := fileLayout(t)
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:include schemaLocation="../outside/lib.xsd"/>
</xs:schema>`
	res := engine.Validate(engine.ValidateRequest{Schemas: []string{schema}, Instance: `<secret/>`, BaseDir: project})
	if res.Valid {
		t.Errorf("a schema included from outside BaseDir must not be read")
	}
	res = engine.Validate(engine.ValidateRequest{Schemas: []string{schema}, Instance: `<secret/>`, BaseDir: project,
		Security: engine.Security{AllowedDirs: []string{outside}}})
	if !res.Valid {
		t.Errorf("control: with AllowedDirs the include is read, got %+v", res.Diagnostics)
	}
	s, _ := xsd.Compile(xsd.Request{Schemas: []string{schema}, BaseDir: project})
	if s != nil && len(s.GlobalElements()) > 0 {
		t.Errorf("xsd.Compile read a schema outside BaseDir")
	}
	s, diags := xsd.Compile(xsd.Request{Schemas: []string{schema}, BaseDir: project, Security: xsd.Security{AllowedDirs: []string{outside}}})
	if s == nil || len(s.GlobalElements()) != 1 {
		t.Errorf("control: xsd.Compile with AllowedDirs: %+v", diags)
	}
}

// A SourceURI outside the allowed directories only labels the source: it does
// not let relative doc() references taken from it read there. The same call
// succeeds once that directory is allowed.
func TestSourceURIDoesNotWidenFileAccess(t *testing.T) {
	project, outside := fileLayout(t)
	src := filepath.Join(outside, "src.xml")
	req := func(sec engine.Security) engine.Result {
		return engine.Transform(engine.Request{
			Stylesheet: `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="/"><out><xsl:value-of select="doc-available(resolve-uri('secret.xml', base-uri(/)))"/></out></xsl:template>
</xsl:stylesheet>`,
			Source: `<s/>`, SourceURI: src, BaseDir: project, Security: sec})
	}
	if res := req(engine.Security{}); !strings.Contains(res.Output, "<out>false</out>") {
		t.Errorf("outside the allowed dirs: got %s %+v", res.Output, res.Diagnostics)
	}
	if res := req(engine.Security{AllowedDirs: []string{outside}}); !strings.Contains(res.Output, "<out>true</out>") {
		t.Errorf("with AllowedDirs: got %s %+v", res.Output, res.Diagnostics)
	}
}
