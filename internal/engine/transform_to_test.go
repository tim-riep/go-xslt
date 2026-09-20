package engine

import (
	"errors"
	"strings"
	"testing"
)

const ttSrc = `<root><rec id="a"><v>1</v></rec><rec id="b"><v>2</v></rec></root>`

// ttSheet wraps a template body in a stylesheet with the given xsl:output.
func ttSheet(out, body string) string {
	return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  ` + out + `
  <xsl:template match="/">` + body + `</xsl:template>
</xsl:stylesheet>`
}

// TestTransformToMirrorsTransform checks the contract: the same bytes, on w
// instead of in Output, with every other field of the Result unchanged.
func TestTransformToMirrorsTransform(t *testing.T) {
	cases := []struct{ name, out, body string }{
		{"xml", `<xsl:output method="xml"/>`, `<out><xsl:for-each select="root/rec"><r><xsl:value-of select="@id"/></r></xsl:for-each></out>`},
		{"text", `<xsl:output method="text"/>`, `<xsl:for-each select="root/rec"><xsl:value-of select="v"/></xsl:for-each>`},
		{"html", `<xsl:output method="html"/>`, `<html><head><title>t</title></head><body><p/></body></html>`},
		{"auto-method", ``, `<out><xsl:copy-of select="root"/></out>`},
		{"indent", `<xsl:output method="xml" indent="yes"/>`, `<out><a><b/></a></out>`},
		{"messages", `<xsl:output method="xml"/>`, `<out><xsl:message>hello</xsl:message><a/></out>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Stylesheet: ttSheet(tc.out, tc.body), Source: ttSrc}
			want := Transform(req)
			if want.HasErrors() {
				t.Fatalf("Transform: %v", want.Diagnostics)
			}
			var b strings.Builder
			got := TransformTo(&b, req)
			if got.HasErrors() {
				t.Fatalf("TransformTo: %v", got.Diagnostics)
			}
			if b.String() != want.Output {
				t.Errorf("bytes differ\n want %q\n  got %q", want.Output, b.String())
			}
			if got.Output != "" {
				t.Errorf("Output should stay empty, got %q", got.Output)
			}
			if got.Method != want.Method {
				t.Errorf("Method %q, want %q", got.Method, want.Method)
			}
			if strings.Join(got.Messages, "|") != strings.Join(want.Messages, "|") {
				t.Errorf("Messages %v, want %v", got.Messages, want.Messages)
			}
		})
	}
}

// TestTransformToSecondaryOutputs checks that xsl:result-document still reaches
// the caller — and, incidentally, the opt-out that makes it safe: a stylesheet
// containing one is never drained, because an empty-href result-document
// rewrites the principal serialization parameters halfway through the run.
func TestTransformToSecondaryOutputs(t *testing.T) {
	req := Request{
		Stylesheet: ttSheet(`<xsl:output method="xml"/>`,
			`<out><xsl:result-document href="side.xml"><side/></xsl:result-document><a/></out>`),
		Source: ttSrc,
	}
	if chunkableStylesheet(req.Stylesheet) {
		t.Error("a stylesheet using xsl:result-document must not be eligible for draining")
	}
	want := Transform(req)
	var b strings.Builder
	got := TransformTo(&b, req)
	if got.HasErrors() {
		t.Fatalf("TransformTo: %v", got.Diagnostics)
	}
	if b.String() != want.Output {
		t.Errorf("bytes differ\n want %q\n  got %q", want.Output, b.String())
	}
	if len(got.SecondaryOutputs) != len(want.SecondaryOutputs) || len(got.SecondaryOutputs) == 0 {
		t.Fatalf("secondary outputs %v, want %v", got.SecondaryOutputs, want.SecondaryOutputs)
	}
	if got.SecondaryOutputs[0].Href != want.SecondaryOutputs[0].Href ||
		got.SecondaryOutputs[0].Content != want.SecondaryOutputs[0].Content {
		t.Errorf("secondary output %+v, want %+v", got.SecondaryOutputs[0], want.SecondaryOutputs[0])
	}
}

// TestTransformToErrors: a failure is a diagnostic, never a panic, and a write
// failure is reported rather than swallowed.
func TestTransformToErrors(t *testing.T) {
	var b strings.Builder
	if res := TransformTo(&b, Request{}); !res.HasErrors() {
		t.Error("an empty stylesheet should be an error")
	}
	if res := TransformTo(&b, Request{Stylesheet: "<not-a-stylesheet/>", Source: ttSrc}); !res.HasErrors() {
		t.Error("a non-stylesheet should be an error")
	}
	req := Request{Stylesheet: ttSheet(`<xsl:output method="xml"/>`,
		`<out><xsl:for-each select="root/rec"><r><xsl:value-of select="@id"/></r></xsl:for-each></out>`), Source: ttSrc}
	if res := TransformTo(failWriter{}, req); !res.HasErrors() {
		t.Error("a failing writer should surface as a diagnostic")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
