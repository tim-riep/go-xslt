package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
func run(ctx context.Context, req Request) Result {
	if d := checkParamExprs(req.ParamExprs); len(d) > 0 {
		return Result{Diagnostics: d}
	}
	opts := compileOptions(req.Security, req.Features, req.BaseDir)
	files := opts.Files
	if d, ok := useEmbeddedStylesheet(&req, opts); !ok {
		return Result{Diagnostics: []Diagnostic{d}}
	}
	ss, err := xslt.CompileWith(req.Stylesheet, req.BaseDir, opts)
	if err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "compile")}}
	}

	entry := xslt.Entry{SourceURI: sourceURIOf(req.SourceURI), Template: req.InitialTemplate, Params: req.Params, ParamSelects: req.ParamExprs, Fetcher: req.Security.fetcher(), Files: files, Limits: req.Limits.internal(), Context: ctx}
	rr, terr := ss.TransformEntry(req.Source, entry, req.BaseDir)
	if terr != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(terr, "run")}}
	}
	res := Result{Output: rr.Output, Method: rr.Method, Messages: rr.Messages}
	for _, s := range rr.Secondary {
		res.SecondaryOutputs = append(res.SecondaryOutputs, SecondaryOutput{Href: s.Href, Content: s.Content, Method: s.Method})
	}
	if err := req.Limits.checkOutputSize(0, res); err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "run")}}
	}
	return res
}

// useEmbeddedStylesheet supplies the stylesheet of a request that has none:
// the module the source embeds and names in its xml-stylesheet processing
// instruction (XSLT 3.0 §3.13). ok is false, with the diagnostic to report,
// when there is neither.
func useEmbeddedStylesheet(req *Request, opts xslt.CompileOptions) (Diagnostic, bool) {
	if req.Stylesheet != "" {
		return Diagnostic{}, true
	}
	if req.Source != "" {
		text, err := xslt.EmbeddedStylesheet(req.Source, req.BaseDir, opts.Files)
		if err == nil {
			req.Stylesheet = text
			return Diagnostic{}, true
		}
		if !errors.Is(err, xslt.ErrNoEmbeddedStylesheet) {
			return diagFromErr(err, "parse"), false
		}
	}
	return Diagnostic{Severity: SeverityError, Code: "XTSE0010", Phase: "parse", Message: "empty stylesheet"}, false
}

// diagFromErr converts an xslt/xpath error into a Diagnostic, extracting
// line/column when available.
func diagFromErr(err error, phase string) Diagnostic {
	d := Diagnostic{Severity: SeverityError, Phase: phase, Message: err.Error(), Code: errCode(err.Error())}
	if ce, ok := err.(*xslt.CompileError); ok {
		d.Line, d.Col = ce.Line, ce.Col
	}
	return d
}

// errCode extracts the W3C error code of a message of the form "err:CODE: ..."
// (XPST0003, FORG0001, XTDE0040, ...); "" when the message does not carry one.
func errCode(msg string) string {
	rest, ok := strings.CutPrefix(msg, "err:")
	if !ok {
		return ""
	}
	end := 0
	for end < len(rest) && (rest[end] >= 'A' && rest[end] <= 'Z' || rest[end] >= '0' && rest[end] <= '9') {
		end++
	}
	if end < 6 {
		return ""
	}
	return rest[:end]
}
