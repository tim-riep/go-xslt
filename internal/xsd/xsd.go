// Package xsd is a from-scratch, pure-Go W3C XML Schema (XSD) 1.0 and 1.1
// validator. It compiles one or more schema documents into a schema component
// model and validates an XML instance document against them, reporting the
// top-level valid/invalid verdict plus diagnostics.
//
// It is built on the engine's own XML tree (internal/xmltree) and XSD datatype
// and regex machinery (internal/xpath).
//
// The public entry point is Validate (or Compile + (*Schema).Validate). The
// validator is being filled in milestone by milestone: M1 covers simple-type
// (datatype) validation with facets; complex types, structural content models,
// identity constraints, and the XSD 1.1 additions land later. When a schema uses
// a construct that is not yet implemented, Compile/Validate report ErrUnsupported
// (distinct from a genuine invalid verdict).
package xsd

import "errors"

// Version selects the schema language version to validate against.
type Version int

const (
	// Version10 is W3C XML Schema 1.0.
	Version10 Version = iota
	// Version11 is W3C XML Schema 1.1.
	Version11
)

func (v Version) String() string {
	if v == Version11 {
		return "1.1"
	}
	return "1.0"
}

// Error is a single schema-compile or instance-validation error. Line/Col are
// 1-based; 0 means unknown. Code is a spec clause / component-constraint id
// where known.
type Error struct {
	Line, Col int
	Code      string
	Message   string
}

func (e Error) Error() string { return e.Message }

// Result is the outcome of a validation attempt. Valid is true iff the instance
// is schema-valid against the compiled schema; when false, Errors explains why.
type Result struct {
	Valid  bool
	Errors []Error
}

// ErrUnsupported is returned while the validator is still being implemented. It
// is distinct from a genuine invalid verdict: it means "the engine could not
// decide", not "the instance/schema is invalid". Callers should treat it as
// "no verdict" rather than "invalid".
var ErrUnsupported = errors.New("xsd: schema construct not yet supported")

// Validate is a convenience that compiles the schemas and validates the instance
// against them in one call.
func Validate(schemas []string, instance, baseDir string, v Version) (*Result, error) {
	s, err := Compile(schemas, baseDir, v)
	if err != nil {
		return nil, err
	}
	return s.Validate(instance)
}
