// Package engine is the stable public facade for the XSLT processor.
//
// The GUI service layer and the conformance test harness depend ONLY on this
// package. Everything behind it (xdm, xmltree, xpath, xslt) may be rewritten
// freely without breaking callers.
package engine

import (
	"time"
)

// Request is a single transformation request.
type Request struct {
	// Stylesheet is the XSLT source text.
	Stylesheet string `json:"stylesheet"`
	// Source is the XML input document. May be empty when InitialTemplate is set.
	Source string `json:"source"`
	// Params are top-level stylesheet parameters (string-typed for now).
	Params map[string]string `json:"params"`
	// InitialTemplate optionally names an xsl:template to invoke first
	// (xsl:initial-template) instead of matching against the source root.
	InitialTemplate string `json:"initialTemplate"`
	// BaseDir is the workspace directory used to resolve relative URIs in
	// xsl:import/xsl:include, document()/doc(), unparsed-text(), and to write
	// xsl:result-document. Empty disables disk resolution.
	BaseDir string `json:"baseDir"`
}

// SecondaryOutput is a result produced by xsl:result-document.
type SecondaryOutput struct {
	Href    string `json:"href"`
	Content string `json:"content"`
	Method  string `json:"method"`
}

// Severity levels for diagnostics.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Diagnostic is a single error or warning produced while compiling or running
// a transformation. Line/Col are 1-based; 0 means "unknown".
type Diagnostic struct {
	Severity string `json:"severity"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	// Code is a W3C-style error code where known (e.g. "XPST0003").
	Code    string `json:"code"`
	Message string `json:"message"`
	// Phase is where the diagnostic originated: "parse", "compile" or "run".
	Phase string `json:"phase"`
}

// Result is the outcome of a transformation. Output holds the serialized
// result tree; Diagnostics holds any errors/warnings. When a fatal error
// occurs, Output is empty and Diagnostics contains at least one error.
type Result struct {
	Output      string       `json:"output"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	// DurationMs is wall-clock transform time in milliseconds.
	DurationMs int64 `json:"durationMs"`
	// Method is the effective output method ("xml", "html", "text", "json").
	Method string `json:"method"`
	// Messages holds text emitted by xsl:message.
	Messages []string `json:"messages"`
	// SecondaryOutputs holds documents produced by xsl:result-document.
	SecondaryOutputs []SecondaryOutput `json:"secondaryOutputs"`
}

// HasErrors reports whether the result contains any error-severity diagnostic.
func (r Result) HasErrors() bool {
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Transform runs req and returns the result. It never panics: any internal
// panic is recovered and surfaced as an error diagnostic, so a partially
// implemented engine can never crash the host application.
func Transform(req Request) (res Result) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Severity: SeverityError,
				Code:     "INTERNAL",
				Phase:    "run",
				Message:  "internal engine error: " + toString(r),
			})
		}
		res.DurationMs = time.Since(start).Milliseconds()
	}()

	return run(req)
}
