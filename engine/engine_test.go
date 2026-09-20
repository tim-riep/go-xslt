package engine_test

import (
	"strings"
	"testing"

	"github.com/tim-riep/go-xslt/engine"
)

const identityStylesheet = `<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">` +
	`<xsl:template match="/"><xsl:copy-of select="."/></xsl:template></xsl:stylesheet>`

func TestTransformIdentity(t *testing.T) {
	res := engine.Transform(engine.Request{
		Stylesheet: identityStylesheet,
		Source:     `<a><b>x</b></a>`,
	})
	if res.HasErrors() {
		t.Fatalf("unexpected diagnostics: %+v", res.Diagnostics)
	}
	if !strings.Contains(res.Output, "<b>x</b>") {
		t.Fatalf("output = %q, want it to contain %q", res.Output, "<b>x</b>")
	}
}

func TestTransformToWritesTheSameResult(t *testing.T) {
	req := engine.Request{Stylesheet: identityStylesheet, Source: `<a><b>x</b></a>`}
	want := engine.Transform(req)
	var b strings.Builder
	res := engine.TransformTo(&b, req)
	if res.HasErrors() {
		t.Fatalf("unexpected diagnostics: %+v", res.Diagnostics)
	}
	if b.String() != want.Output {
		t.Fatalf("written = %q, want %q", b.String(), want.Output)
	}
	if res.Output != "" {
		t.Fatalf("Output = %q, want it empty: the result went to the writer", res.Output)
	}
}

func TestTransformReportsCompileErrorAsDiagnostic(t *testing.T) {
	res := engine.Transform(engine.Request{
		Stylesheet: `<not-a-stylesheet/>`,
		Source:     `<a/>`,
	})
	if !res.HasErrors() {
		t.Fatal("want diagnostics for invalid stylesheet, got none")
	}
	if res.Diagnostics[0].Severity != engine.SeverityError {
		t.Fatalf("severity = %q, want %q", res.Diagnostics[0].Severity, engine.SeverityError)
	}
}
