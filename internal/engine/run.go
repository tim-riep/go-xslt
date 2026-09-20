package engine

import (
	"fmt"

	"github.com/tim-riep/go-xslt/internal/xslt"
)

// toString renders a recovered panic value for a diagnostic message.
func toString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%v", v)
}

// run compiles the stylesheet and executes the transformation.
func run(req Request) Result {
	if req.Stylesheet == "" {
		return Result{Diagnostics: []Diagnostic{{
			Severity: SeverityError,
			Code:     "XTSE0010",
			Phase:    "parse",
			Message:  "empty stylesheet",
		}}}
	}

	ss, err := xslt.CompileFrom(req.Stylesheet, req.BaseDir)
	if err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "compile")}}
	}

	source := req.Source
	if source == "" {
		// A non-empty document is required for matching against the source tree.
		source = "<_/>"
	}

	rr, terr := ss.TransformFull(source, req.Params, req.BaseDir)
	if terr != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(terr, "run")}}
	}
	res := Result{Output: rr.Output, Method: rr.Method, Messages: rr.Messages}
	for _, s := range rr.Secondary {
		res.SecondaryOutputs = append(res.SecondaryOutputs, SecondaryOutput{Href: s.Href, Content: s.Content, Method: s.Method})
	}
	return res
}

// diagFromErr converts an xslt/xpath error into a Diagnostic, extracting
// line/column when available.
func diagFromErr(err error, phase string) Diagnostic {
	d := Diagnostic{Severity: SeverityError, Phase: phase, Message: err.Error()}
	if ce, ok := err.(*xslt.CompileError); ok {
		d.Line, d.Col = ce.Line, ce.Col
	}
	return d
}
