package engine

import (
	"context"
	"io"
	"math"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/internal/xslt"
)

// Stylesheet is a stylesheet compiled once and run many times — the same
// generated stylesheet against changing data, as an interactive preview does.
//
// A Stylesheet is immutable after Compile and safe for concurrent use: every
// Transform call builds its own run state (variables, key indexes, document
// cache, result tree) and only reads the compiled form.
type Stylesheet struct {
	ss      *xslt.Stylesheet
	src     string
	baseDir string
}

// TransformRequest is one run of a compiled Stylesheet. The stylesheet and its
// BaseDir were fixed by Compile.
type TransformRequest struct {
	// Source is the XML input document. May be empty when InitialTemplate is set.
	Source string `json:"source"`
	// SourceURI is the URI (or file path) Source was read from; see
	// Request.SourceURI. Empty: the source is parsed against the Stylesheet's
	// BaseDir.
	SourceURI string `json:"sourceURI,omitempty"`
	// Params are top-level stylesheet parameters (string-typed).
	Params map[string]string `json:"params"`
	// ParamExprs sets parameters to the value of an XPath expression; see
	// Request.ParamExprs.
	ParamExprs map[string]string `json:"paramExprs,omitempty"`
	// InitialTemplate optionally names an xsl:template to invoke first.
	InitialTemplate string `json:"initialTemplate"`
	// Security limits which files (under the Stylesheet's BaseDir) and
	// network resources this run may read; the zero value allows files under
	// BaseDir only, and no network. The compile itself was limited by the
	// Security of the Request given to Compile.
	Security Security `json:"security"`
	// Limits bounds what this run may execute; the zero value takes the
	// defaults.
	Limits Limits `json:"limits"`
}

// Compile compiles req.Stylesheet (resolving xsl:import/xsl:include against
// req.BaseDir) without running it. Source, Params and InitialTemplate of req
// are ignored — they belong to each Transform call. On failure the returned
// Stylesheet is nil and the diagnostics carry the error with its line and
// column in the stylesheet. It never panics.
func Compile(req Request) (sheet *Stylesheet, diags []Diagnostic) {
	defer func() {
		if r := recover(); r != nil {
			sheet = nil
			diags = []Diagnostic{{Severity: SeverityError, Code: "INTERNAL", Phase: "compile",
				Message: "internal engine error: " + toString(r)}}
		}
	}()
	if req.Stylesheet == "" {
		return nil, []Diagnostic{{Severity: SeverityError, Code: "XTSE0010", Phase: "parse", Message: "empty stylesheet"}}
	}
	ss, err := xslt.CompileWith(req.Stylesheet, req.BaseDir, compileOptions(req.Security, req.Features, req.BaseDir))
	if err != nil {
		return nil, []Diagnostic{diagFromErr(err, "compile")}
	}
	return &Stylesheet{ss: ss, src: req.Stylesheet, baseDir: req.BaseDir}, nil
}

// Transform runs the compiled stylesheet. It never panics: errors, including
// runtime errors with the position of the stylesheet instruction that raised
// them where known, are reported as Diagnostics.
func (s *Stylesheet) Transform(tr TransformRequest) Result {
	return s.TransformContext(context.Background(), tr)
}

// TransformContext is Transform that stops when ctx is done, like the
// package-level TransformContext.
func (s *Stylesheet) TransformContext(ctx context.Context, tr TransformRequest) (res Result) {
	start := time.Now()
	defer s.finish(&res, start)
	if d := checkParamExprs(tr.ParamExprs); len(d) > 0 {
		return Result{Diagnostics: d}
	}
	entry := xslt.Entry{SourceURI: sourceURIOf(tr.SourceURI), Template: tr.InitialTemplate, Params: tr.Params, ParamSelects: tr.ParamExprs, Fetcher: tr.Security.fetcher(), Files: FileGuard(tr.Security, s.baseDir), Limits: tr.Limits.internal(), Context: ctx}
	rr, err := s.ss.TransformEntry(tr.Source, entry, s.baseDir)
	if err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "run")}}
	}
	res = Result{Output: rr.Output, Method: rr.Method, Messages: rr.Messages}
	for _, sec := range rr.Secondary {
		res.SecondaryOutputs = append(res.SecondaryOutputs, SecondaryOutput{Href: sec.Href, Content: sec.Content, Method: sec.Method})
	}
	if err := tr.Limits.checkOutputSize(0, res); err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "run")}}
	}
	return res
}

// TransformTo is Transform writing the principal result to w (Output stays
// empty); see the package-level TransformTo.
func (s *Stylesheet) TransformTo(w io.Writer, tr TransformRequest) Result {
	return s.TransformToContext(context.Background(), w, tr)
}

// TransformToContext is TransformTo that stops when ctx is done.
func (s *Stylesheet) TransformToContext(ctx context.Context, w io.Writer, tr TransformRequest) (res Result) {
	start := time.Now()
	defer s.finish(&res, start)
	if d := checkParamExprs(tr.ParamExprs); len(d) > 0 {
		return Result{Diagnostics: d}
	}
	entry := xslt.Entry{SourceURI: sourceURIOf(tr.SourceURI), Template: tr.InitialTemplate, Params: tr.Params, ParamSelects: tr.ParamExprs, Fetcher: tr.Security.fetcher(), Files: FileGuard(tr.Security, s.baseDir), Limits: tr.Limits.internal(), Context: ctx}
	lw := &limitWriter{w: w, max: math.MaxInt64}
	if tr.Limits.MaxOutputBytes > 0 {
		lw.max = tr.Limits.MaxOutputBytes
	}
	rr, err := s.ss.TransformEntryTo(lw, tr.Source, entry, s.baseDir, chunkableStylesheet(s.src))
	if err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "run")}}
	}
	res = Result{Method: rr.Method, Messages: rr.Messages}
	for _, sec := range rr.Secondary {
		res.SecondaryOutputs = append(res.SecondaryOutputs, SecondaryOutput{Href: sec.Href, Content: sec.Content, Method: sec.Method})
	}
	if err := tr.Limits.checkOutputSize(lw.n, res); err != nil {
		return Result{Diagnostics: []Diagnostic{diagFromErr(err, "run")}}
	}
	return res
}

// TransformFromReader is Transform with the source read from src (tr.Source is
// ignored); a read failure is reported as a diagnostic.
func (s *Stylesheet) TransformFromReader(src io.Reader, tr TransformRequest) (res Result) {
	var sb strings.Builder
	if _, err := io.Copy(&sb, src); err != nil {
		return Result{Diagnostics: []Diagnostic{{Severity: SeverityError, Code: "IO", Phase: "read",
			Message: "reading source: " + err.Error()}}}
	}
	tr.Source = sb.String()
	return s.Transform(tr)
}

func (s *Stylesheet) finish(res *Result, start time.Time) {
	if r := recover(); r != nil {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: SeverityError, Code: "INTERNAL", Phase: "run",
			Message: "internal engine error: " + toString(r)})
	}
	res.DurationMs = time.Since(start).Milliseconds()
}
