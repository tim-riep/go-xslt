package engine

import (
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
	Schemas []string `json:"schemas"`
	// Instance is the XML instance document to validate.
	Instance string `json:"instance"`
	// Version is "1.0" (default) or "1.1".
	Version XSDVersion `json:"version"`
	// BaseDir resolves relative schema locations (xs:import/xs:include/
	// xsi:schemaLocation). Empty disables disk resolution.
	BaseDir string `json:"baseDir"`
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
func Validate(req ValidateRequest) (res ValidateResult) {
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
	return runValidate(req)
}

func runValidate(req ValidateRequest) ValidateResult {
	v := xsd.Version10
	if req.Version == XSD11 {
		v = xsd.Version11
	}
	r, err := xsd.Validate(req.Schemas, req.Instance, req.BaseDir, v)
	if err != nil {
		return ValidateResult{Valid: false, Diagnostics: []Diagnostic{{
			Severity: SeverityError,
			Phase:    "validate",
			Message:  err.Error(),
		}}}
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
		})
	}
	return res
}
