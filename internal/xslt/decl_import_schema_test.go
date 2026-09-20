package xslt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

const isSchemaXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"
           targetNamespace="http://example.com/is"
           xmlns:t="http://example.com/is"
           elementFormDefault="qualified">
  <xs:simpleType name="Code">
    <xs:restriction base="xs:string"/>
  </xs:simpleType>
  <xs:element name="item" type="t:Code"/>
</xs:schema>`

// sheetImporting wraps one xsl:import-schema declaration in a minimal module.
func sheetImporting(decl string) string {
	return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
                xmlns:xs="http://www.w3.org/2001/XMLSchema"
                xmlns:t="http://example.com/is">
  ` + decl + `
  <xsl:template match="/"><out/></xsl:template>
</xsl:stylesheet>`
}

// TestImportSchemaRejectedWhenNotClaimed is the no-regression half: with the
// claim off — the default, and what the 31 non-schema-aware conformance cases
// are written for — xsl:import-schema must still be refused, in the same words.
func TestImportSchemaRejectedWhenNotClaimed(t *testing.T) {
	for _, decl := range []string{
		`<xsl:import-schema schema-location="nowhere.xsd"/>`,
		`<xsl:import-schema namespace="http://example.com/is"/>`,
		`<xsl:import-schema><xs:schema/></xsl:import-schema>`,
	} {
		_, err := Compile(sheetImporting(decl))
		if err == nil {
			t.Errorf("%s compiled with the schema-awareness claim off", decl)
			continue
		}
		if !strings.Contains(err.Error(), "optional feature") {
			t.Errorf("%s: err = %v, want the unchanged optional-feature rejection", decl, err)
		}
	}
}

// TestImportSchemaInlineCompiles covers the inline xs:schema form: the
// declaration parses, the schema compiles, and the components are registered
// for the module so an expression written in it can resolve names against them.
func TestImportSchemaInlineCompiles(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	inline := strings.TrimPrefix(isSchemaXSD, `<?xml version="1.0"?>`+"\n")
	ss, err := Compile(sheetImporting("<xsl:import-schema>" + inline + "</xsl:import-schema>"))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	el := ss.templates[0].el
	if schemaForElement(el) == nil {
		t.Fatal("no schema registered for the module")
	}
	// The same adapter must reach the evaluation-time static context, or an
	// @as string would be parsed without it.
	if asTypeCtx(el).SchemaTypes == nil {
		t.Error("asTypeCtx did not carry the schema adapter")
	}
}

// TestImportSchemaInlineInheritedPrefix pins the shape the conformance suite
// actually writes: the inline schema's xs prefix is bound on the STYLESHEET
// root, not on the xs:schema element, so extracting the subtree has to carry
// the in-scope declarations with it or the extracted document names nothing.
func TestImportSchemaInlineInheritedPrefix(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	ss, err := Compile(sheetImporting(`<xsl:import-schema>
    <xs:schema targetNamespace="http://example.com/is">
      <xs:simpleType name="Code"><xs:restriction base="xs:string"/></xs:simpleType>
    </xs:schema>
  </xsl:import-schema>`))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if schemaForElement(ss.templates[0].el) == nil {
		t.Fatal("no schema registered — the inherited xs binding was probably lost")
	}
}

// TestImportSchemaLocationCompiles covers the external-document form.
func TestImportSchemaLocationCompiles(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "is.xsd"), []byte(isSchemaXSD), 0o644); err != nil {
		t.Fatal(err)
	}
	ss, err := CompileFrom(sheetImporting(
		`<xsl:import-schema namespace="http://example.com/is" schema-location="is.xsd"/>`), dir)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if schemaForElement(ss.templates[0].el) == nil {
		t.Fatal("no schema registered for the module")
	}
}

// TestImportSchemaHintOnly pins §3.16's "locating no schema document is not in
// itself an error": a namespace-only declaration, and a schema-location that
// leads nowhere, both compile and simply contribute no components.
func TestImportSchemaHintOnly(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	for _, decl := range []string{
		`<xsl:import-schema namespace="http://example.com/is"/>`,
		`<xsl:import-schema namespace="http://example.com/is" schema-location="no-such-file.xsd"/>`,
	} {
		ss, err := Compile(sheetImporting(decl))
		if err != nil {
			t.Errorf("%s: compile: %v", decl, err)
			continue
		}
		if schemaForElement(ss.templates[0].el) != nil {
			t.Errorf("%s: components were registered for a declaration with no document", decl)
		}
	}
}

// TestImportSchemaImportPrecedence covers §3.16's precedence rule: when two
// declarations name the same namespace only the highest-precedence one is
// used. Both schemas here define the same global type, so compiling them
// TOGETHER would be a duplicate-definition error — that the importing module's
// declaration simply supersedes the imported one is what the rule buys.
func TestImportSchemaImportPrecedence(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	dir := t.TempDir()
	schema := func(base string) string {
		return `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="http://example.com/is">
  <xs:simpleType name="Code"><xs:restriction base="` + base + `"/></xs:simpleType>
</xs:schema>`
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lo.xsd", schema("xs:string"))
	write("hi.xsd", schema("xs:integer"))
	write("lo.xsl", `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:import-schema namespace="http://example.com/is" schema-location="lo.xsd"/>
</xsl:stylesheet>`)

	ss, err := CompileFrom(`<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:import href="lo.xsl"/>
  <xsl:import-schema namespace="http://example.com/is" schema-location="hi.xsd"/>
  <xsl:template match="/"><out/></xsl:template>
</xsl:stylesheet>`, dir)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if schemaForElement(ss.templates[0].el) == nil {
		t.Fatal("no schema registered")
	}

	// The negative control, without which the case above proves nothing: at
	// EQUAL precedence both declarations are used, and the conflict §3.16
	// warns about is then a real XTSE0220.
	_, err = CompileFrom(`<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:import-schema namespace="http://example.com/is" schema-location="lo.xsd"/>
  <xsl:import-schema namespace="http://example.com/is" schema-location="hi.xsd"/>
  <xsl:template match="/"><out/></xsl:template>
</xsl:stylesheet>`, dir)
	if err == nil {
		t.Error("two same-precedence declarations of one namespace compiled without conflict")
	}
}

// TestImportSchemaStaticErrors covers XTSE0215's two clauses and the
// zero-length-namespace rule.
func TestImportSchemaStaticErrors(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	inline := `<xs:schema targetNamespace="http://example.com/is"/>`
	cases := []struct{ decl, want string }{
		{`<xsl:import-schema schema-location="is.xsd">` + inline + `</xsl:import-schema>`, "XTSE0215"},
		{`<xsl:import-schema namespace="http://other.example.com/">` + inline + `</xsl:import-schema>`, "XTSE0215"},
		{`<xsl:import-schema namespace=""/>`, "XTSE0020"},
	}
	for _, tc := range cases {
		_, err := Compile(sheetImporting(tc.decl))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %s", tc.decl, err, tc.want)
		}
	}
}

// TestSchemaTypeNameFailsClosed is the safety property that matters most until
// validation exists: a type name that resolves to nothing, and a node that
// carries no annotation, must produce a clean false — never a match, never a
// panic. Both readings are exercised through the real engine, with a schema
// imported so the schema-aware path is genuinely taken.
func TestSchemaTypeNameFailsClosed(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	inline := strings.TrimPrefix(isSchemaXSD, `<?xml version="1.0"?>`+"\n")
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
                xmlns:xs="http://www.w3.org/2001/XMLSchema"
                xmlns:t="http://example.com/is">
  <xsl:import-schema>` + inline + `</xsl:import-schema>
  <xsl:output method="text"/>
  <xsl:template match="/r">
    <xsl:value-of select="e instance of element(e, t:Code)"/>
    <xsl:text>|</xsl:text>
    <xsl:value-of select="e instance of element(e, t:NoSuchType)"/>
    <xsl:text>|</xsl:text>
    <xsl:value-of select="count(e[. instance of element(*, t:Code)])"/>
  </xsl:template>
</xsl:stylesheet>`
	ss, err := Compile(sheet)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, _, err := ss.Transform(`<r><e>x</e></r>`, nil)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out != "false|false|0" {
		t.Errorf("got %q, want %q — an unannotated node must satisfy no named type", out, "false|false|0")
	}
}

// TestSchemaElementPatternUnresolved pins the static half of failing closed:
// with schema components in scope, a schema-element() naming a declaration
// that is not among them is a static error, not a pattern that quietly never
// matches.
func TestSchemaElementPatternUnresolved(t *testing.T) {
	SetSchemaAware(true)
	defer SetSchemaAware(false)

	inline := strings.TrimPrefix(isSchemaXSD, `<?xml version="1.0"?>`+"\n")
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
                xmlns:xs="http://www.w3.org/2001/XMLSchema"
                xmlns:t="http://example.com/is">
  <xsl:import-schema>` + inline + `</xsl:import-schema>
  <xsl:template match="schema-element(t:noSuchElement)"><out/></xsl:template>
</xsl:stylesheet>`
	if _, err := Compile(sheet); err == nil || !strings.Contains(err.Error(), "XPST0008") {
		t.Errorf("err = %v, want XPST0008", err)
	}
}

// TestSchemaAdapterPrefixResolution checks the half of name resolution that
// stays on this side of the seam: prefixes resolve through the stylesheet
// element's own bindings, an unbound prefix names nothing, and a braced name
// carries its own URI.
func TestSchemaAdapterPrefixResolution(t *testing.T) {
	doc, err := xmltree.Parse(`<e xmlns:p="http://example.com/p"/>`)
	if err != nil {
		t.Fatal(err)
	}
	a := &schemaAdapter{el: xmltree.RootElement(doc)}
	cases := []struct {
		lexical string
		ns      string
		local   string
		ok      bool
	}{
		{"p:T", "http://example.com/p", "T", true},
		{"zz:T", "", "", false},
		{"Q{http://example.com/q}T", "http://example.com/q", "T", true},
		{"T", "", "T", true},
	}
	for _, tc := range cases {
		ns, local, ok := a.expand(tc.lexical)
		if ok != tc.ok || (ok && (ns != tc.ns || local != tc.local)) {
			t.Errorf("expand(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.lexical, ns, local, ok, tc.ns, tc.local, tc.ok)
		}
	}
}
