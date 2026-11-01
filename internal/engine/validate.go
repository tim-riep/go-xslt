package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tim-riep/go-xslt/internal/xsd"
)

// XSDVersion selects the XML Schema language version for Validate.
type XSDVersion string

const (
	// XSD10 is W3C XML Schema 1.0 (the default when Version is empty).
	XSD10 XSDVersion = "1.0"
	// XSD11 is W3C XML Schema 1.1.
	XSD11 XSDVersion = "1.1"
)

// ValidateRequest validates an XML instance document against one or more XSD
// schema documents.
type ValidateRequest struct {
	// Schemas holds the schema document source texts. The first is the entry
	// schema; the rest are additional documents available to xs:import/include.
	// With none, the instance is validated by the schema documents its own
	// xsi:schemaLocation / xsi:noNamespaceSchemaLocation name (read under
	// BaseDir and Security) and the built-in types its xsi:type names.
	Schemas []string `json:"schemas"`
	// Instance is the XML instance document to validate.
	Instance string `json:"instance"`
	// Version is "1.0" (default) or "1.1".
	Version XSDVersion `json:"version"`
	// BaseDir resolves relative schema locations (xs:import/xs:include/
	// xsi:schemaLocation). Empty disables disk resolution.
	BaseDir string `json:"baseDir"`
	// Language selects the xml:lang of assertion messages (msg:message in an
	// xs:assert's xs:appinfo), e.g. "de". Empty means the default message. It
	// only affects diagnostic wording, never validity.
	Language string `json:"language"`
	// MaxErrors stops validation after this many errors; 0 means no limit.
	// Validation reports every error it finds (in document order), not only
	// the first.
	MaxErrors int `json:"maxErrors"`
	// Security limits which schema files (xs:import/xs:include/xs:redefine/
	// xs:override, xsi:schemaLocation) may be read; the zero value allows
	// files under BaseDir only. Network access does not apply here.
	Security Security `json:"security"`
}

// ValidateResult is the outcome of a validation. Valid is true iff the instance
// is schema-valid; Diagnostics carries schema-compile and validation errors (and
// is non-empty when Valid is false).
type ValidateResult struct {
	Valid       bool         `json:"valid"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	DurationMs  int64        `json:"durationMs"`
}

// Validate compiles req.Schemas and validates req.Instance against them. It
// never panics: any internal panic is recovered and surfaced as an error
// diagnostic with Valid=false.
func Validate(req ValidateRequest) ValidateResult {
	return ValidateContext(context.Background(), req)
}

// ValidateContext is Validate that stops when ctx is done, like
// TransformContext.
func ValidateContext(ctx context.Context, req ValidateRequest) (res ValidateResult) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			res.Valid = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Severity: SeverityError,
				Code:     "INTERNAL",
				Phase:    "validate",
				Message:  "internal validator error: " + toString(r),
			})
		}
		res.DurationMs = time.Since(start).Milliseconds()
	}()
	return runValidate(ctx, req)
}

func runValidate(ctx context.Context, req ValidateRequest) ValidateResult {
	v := xsd.Version10
	if req.Version == XSD11 {
		v = xsd.Version11
	}
	sch, err := xsd.CompileFS(req.Schemas, req.BaseDir, v, FileGuard(req.Security, req.BaseDir))
	var r *xsd.Result
	if err == nil {
		r, err = sch.ValidateOptsCtx(ctx, req.Instance, req.Language, req.MaxErrors)
	}
	if err != nil {
		d := Diagnostic{Severity: SeverityError, Phase: "validate", Message: err.Error()}
		// A schema error located in the schema document: its line and column go
		// into the text (not into Line/Col, which locate a place in the
		// INSTANCE and are printed with the instance's file name).
		var xe *xsd.Error
		if errors.As(err, &xe) {
			d.Code = xe.Code
			if xe.Line > 0 {
				d.Message = fmt.Sprintf("schema line %d, column %d: %s", xe.Line, xe.Col, xe.Message)
			}
		}
		return ValidateResult{Valid: false, Diagnostics: []Diagnostic{d}}
	}
	res := ValidateResult{Valid: r.Valid}
	for _, e := range r.Errors {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Severity: SeverityError,
			Phase:    "validate",
			Line:     e.Line,
			Col:      e.Col,
			Code:     e.Code,
			Message:  e.Message,
			Rule:     e.Rule,
			Path:     e.Path,
			Particle: e.Particle,
			Kind:     e.Kind,
		})
	}
	return res
}
