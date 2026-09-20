package xslt

import (
	"strings"
	"testing"
)

func schemaRunStylesheet(t *testing.T, sheet string) (string, error) {
	t.Helper()
	ss, err := CompileFrom(sheet, "")
	if err != nil {
		return "", err
	}
	out, _, err := ss.Transform("<_/>", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// TestIsSchemaAwareProperty pins system-property('xsl:is-schema-aware') to the
// claim rather than to a hardcoded "no" — the mistake the streaming work had to
// spend a later round undoing for its own property.
func TestIsSchemaAwareProperty(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="text"/>
  <xsl:template match="/"><xsl:value-of select="system-property('xsl:is-schema-aware')"/></xsl:template>
</xsl:stylesheet>`

	got, err := schemaRunStylesheet(t, sheet)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if got != "no" {
		t.Errorf("default is-schema-aware = %q, want \"no\"", got)
	}

	SetSchemaAware(true)
	defer SetSchemaAware(false)
	got, err = schemaRunStylesheet(t, sheet)
	if err != nil {
		t.Fatalf("claimed: %v", err)
	}
	if got != "yes" {
		t.Errorf("is-schema-aware = %q after the host claimed it, want \"yes\"", got)
	}
}

// TestXTSE1660GatedOnTheClaim covers the three shapes of "that needs a
// schema-aware processor" the compiler rejects, in both switch positions. The
// OFF column is the one 31 conformance cases depend on.
func TestXTSE1660GatedOnTheClaim(t *testing.T) {
	cases := []struct{ name, sheet string }{
		{"xsl:element/@type", `<xsl:template match="/"><xsl:element name="e" type="xs:string"/></xsl:template>`},
		{"xsl:copy-of/@validation", `<xsl:template match="/"><xsl:copy-of select="." validation="strict"/></xsl:template>`},
		{"literal element xsl:type", `<xsl:template match="/"><e xsl:type="xs:string"/></xsl:template>`},
		{"literal element xsl:validation", `<xsl:template match="/"><e xsl:validation="strict"/></xsl:template>`},
		{"stylesheet @default-validation", `<xsl:template match="/"><e/></xsl:template>`},
	}
	wrap := func(body string, dv bool) string {
		attr := ""
		if dv {
			attr = ` default-validation="strict"`
		}
		return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"` +
			` xmlns:xs="http://www.w3.org/2001/XMLSchema"` + attr + `>` + body + `</xsl:stylesheet>`
	}
	for i, c := range cases {
		sheet := wrap(c.sheet, i == len(cases)-1)
		if _, err := schemaRunStylesheet(t, sheet); err == nil {
			t.Errorf("%s: accepted with the claim off, want XTSE1660", c.name)
		} else if !strings.Contains(err.Error(), "XTSE1660") {
			t.Errorf("%s: %v, want XTSE1660", c.name, err)
		}

		SetSchemaAware(true)
		_, err := schemaRunStylesheet(t, sheet)
		SetSchemaAware(false)
		if err != nil && strings.Contains(err.Error(), "XTSE1660") {
			t.Errorf("%s: still XTSE1660 with the claim on: %v", c.name, err)
		}
	}
}

// A value outside the four-value vocabulary is XTSE1660 whatever the claim is —
// only "strict" is about schema-awareness (§3.15 makes preserve and lax behave
// as strip for a basic processor, which is why they are never rejected).
func TestValidationVocabulary(t *testing.T) {
	for _, v := range []string{"strip", "preserve", "lax"} {
		if !validationValueOK(v) {
			t.Errorf("validationValueOK(%q) = false, want true", v)
		}
	}
	if validationValueOK("strict") {
		t.Error("validationValueOK(\"strict\") = true with the claim off")
	}
	if validationValueOK("nonsense") {
		t.Error("validationValueOK(\"nonsense\") = true")
	}
	SetSchemaAware(true)
	defer SetSchemaAware(false)
	if !validationValueOK("strict") {
		t.Error("validationValueOK(\"strict\") = false with the claim on")
	}
	if validationValueOK("nonsense") {
		t.Error("validationValueOK(\"nonsense\") = true with the claim on")
	}
}
