// Package engine is the public facade of the goxslt XSLT/XPath processor.
// It re-exports goxslt/internal/engine, the module's stable compatibility
// boundary, so external modules can consume the engine; everything behind
// this facade may be rewritten freely.
package engine

import (
	"io"

	"github.com/tim-riep/go-xslt/internal/engine"
)

type (
	Request         = engine.Request
	Result          = engine.Result
	Diagnostic      = engine.Diagnostic
	SecondaryOutput = engine.SecondaryOutput

	// XSD schema-validation API.
	ValidateRequest = engine.ValidateRequest
	ValidateResult  = engine.ValidateResult
	XSDVersion      = engine.XSDVersion

	// Standalone XPath 3.1 evaluation API.
	XPathRequest = engine.XPathRequest
	XPathResult  = engine.XPathResult
	XPathItem    = engine.XPathItem
)

const (
	SeverityError   = engine.SeverityError
	SeverityWarning = engine.SeverityWarning

	XSD10 = engine.XSD10
	XSD11 = engine.XSD11
)

// Transform runs req and returns the result. It never panics; errors are
// reported as Diagnostics on the Result.
func Transform(req Request) Result { return engine.Transform(req) }

// TransformTo runs req and writes the principal result to w rather than
// returning it in Result.Output, which stays empty; every other field is
// populated exactly as Transform populates it. A result large enough to matter
// never exists as one string, and when the stylesheet allows it the result tree
// is serialized and released while it is built — so a transformation that
// streams a large xsl:source-document through to its output runs in bounded
// memory on both sides. It never panics; errors are reported as Diagnostics.
func TransformTo(w io.Writer, req Request) Result { return engine.TransformTo(w, req) }

// Validate validates an XML instance against XSD 1.0/1.1 schemas. It never
// panics; errors are reported as Diagnostics with Valid=false.
func Validate(req ValidateRequest) ValidateResult { return engine.Validate(req) }

// EvalXPath evaluates a standalone XPath 3.1 expression, optionally against an
// XML context document (req.Source). It never panics; errors are reported as
// Diagnostics.
func EvalXPath(req XPathRequest) XPathResult { return engine.EvalXPath(req) }
