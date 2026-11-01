// Package engine is the stable public facade for the XSLT processor.
//
// The GUI service layer and the conformance test harness depend ONLY on this
// package. Everything behind it (xdm, xmltree, xpath, xslt) may be rewritten
// freely without breaking callers.
package engine

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tim-riep/go-xslt/internal/fsaccess"
	"github.com/tim-riep/go-xslt/internal/netfetch"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"github.com/tim-riep/go-xslt/internal/xslt"
)

// Security says which local files and which network resources a run may read.
// The zero value is the safe default: files under BaseDir only, no network.
// A stylesheet, an expression, a schema and a source document are often
// untrusted: unrestricted file access would let them read anything the
// process can (keys, configuration) and copy it into the output, and network
// access would let them reach internal services or send data away in a URL.
// Only the caller widens these limits, never the input.
type Security struct {
	// AllowedDirs are further directories (with their subdirectories) whose
	// files may be read, besides BaseDir.
	AllowedDirs []string `json:"allowedDirs,omitempty"`
	// AllowAllFiles lifts the file restriction entirely: any path the
	// process can read, as before this option existed. Only for trusted
	// input.
	AllowAllFiles bool `json:"allowAllFiles,omitempty"`
	// AllowNetwork lets document(), fn:doc, fn:doc-available,
	// fn:unparsed-text(-lines/-available), fn:collection members and
	// xsl:source-document retrieve http and https URIs. Off by default, in
	// which case such a URI is an unavailable resource. A document retrieved
	// over the network is parsed without reading any external DTD or entity
	// files.
	AllowNetwork bool `json:"allowNetwork"`
	// AllowedHosts, when non-empty, limits network access to these host
	// names; a leading "." also admits every subdomain (".example.com").
	// Redirects are checked against the same list.
	AllowedHosts []string `json:"allowedHosts,omitempty"`
}

// FileGuard is the file guard for a run of s rooted at baseDir: reads are
// confined to baseDir and s.AllowedDirs (no BaseDir and no AllowedDirs: no
// file reads at all), unless s.AllowAllFiles, which returns nil (no check).
// It is a function rather than a method so it does not show up on the public
// engine.Security / xsd.Security alias.
func FileGuard(s Security, baseDir string) *fsaccess.Guard {
	if s.AllowAllFiles {
		return nil
	}
	return fsaccess.New(append([]string{baseDir}, s.AllowedDirs...)...)
}

// fetcher is the per-run retriever for s, nil when s allows no network access.
func (s Security) fetcher() *netfetch.Fetcher {
	return netfetch.New(netfetch.Policy{AllowNetwork: s.AllowNetwork, AllowedHosts: s.AllowedHosts})
}

// Request is a single transformation request.
type Request struct {
	// Stylesheet is the XSLT source text. When it is empty, Transform and
	// TransformTo use the stylesheet the Source embeds and names in an
	// <?xml-stylesheet type="text/xsl" href="#id"?> processing instruction
	// (XSLT 3.0 §3.13): the element with that ID, compiled as a module of its
	// own.
	Stylesheet string `json:"stylesheet"`
	// Source is the XML input document. May be empty when InitialTemplate is set.
	Source string `json:"source"`
	// Params are top-level stylesheet parameters (string-typed for now).
	Params map[string]string `json:"params"`
	// ParamExprs sets global parameters to the value of an XPath expression,
	// so they can be typed: "5", "xs:date('2026-01-31')", "map{'a': 1}",
	// "parse-xml('<x/>')". The expression is evaluated without a context
	// item; a name here wins over the same name in Params.
	ParamExprs map[string]string `json:"paramExprs,omitempty"`
	// InitialTemplate optionally names an xsl:template to invoke first
	// (xsl:initial-template) instead of matching against the source root.
	InitialTemplate string `json:"initialTemplate"`
	// BaseDir is the workspace directory used to resolve relative URIs in
	// xsl:import/xsl:include, document()/doc(), unparsed-text(), and the
	// hrefs of xsl:result-document (returned in SecondaryOutputs; nothing is
	// written to disk). Empty disables disk resolution.
	BaseDir string `json:"baseDir"`
	// SourceURI is the URI (or file path) the Source document was read from.
	// It becomes the document URI and the base URI of the source document
	// (fn:document-uri, fn:base-uri, and relative references resolved from
	// it). Empty: the source is parsed against BaseDir, as before. BaseDir
	// stays the base of the stylesheet and of everything it imports.
	SourceURI string `json:"sourceURI,omitempty"`
	// Security limits which files and network resources the run may read;
	// the zero value allows files under BaseDir only, and no network.
	Security Security `json:"security"`
	// Features turns on optional XSLT 3.0 capabilities for this stylesheet;
	// the zero value is a basic XSLT 3.0 processor.
	Features Features `json:"features"`
	// Limits bounds what the run may execute; the zero value takes the
	// defaults.
	Limits Limits `json:"limits"`
}

// Limits bounds one transformation, including the transformations it starts
// itself with fn:transform, which spend from the same budget. A run that
// exceeds a limit stops with the error XPDY0130 ("implementation limit
// exceeded"). A zero field takes the default.
type Limits struct {
	// MaxSteps is the number of instructions the run may execute (default
	// 20,000,000), which bounds an endless loop such as an unbounded
	// xsl:iterate.
	MaxSteps int64 `json:"maxSteps"`
	// MaxDepth is how deeply instructions, templates and functions may nest
	// (default 10,000), which bounds endless recursion. It can only lower the
	// default: deeper recursion could overflow the Go stack, which ends the
	// whole process.
	MaxDepth int `json:"maxDepth"`
	// MaxOutputBytes, when positive, bounds the serialized results: the
	// principal result and the xsl:result-document outputs together. With
	// TransformTo the writer receives no more than that.
	MaxOutputBytes int64 `json:"maxOutputBytes"`
}

// errOutputLimit is the error of a run whose results exceed MaxOutputBytes.
func errOutputLimit(max int64) error {
	return fmt.Errorf("err:XPDY0130: the results are larger than the limit of %d bytes", max)
}

// checkOutputSize reports a result over l.MaxOutputBytes; written is what
// already went to a TransformTo writer.
func (l Limits) checkOutputSize(written int64, res Result) error {
	if l.MaxOutputBytes <= 0 {
		return nil
	}
	n := written + int64(len(res.Output))
	for _, s := range res.SecondaryOutputs {
		n += int64(len(s.Content))
	}
	if n > l.MaxOutputBytes {
		return errOutputLimit(l.MaxOutputBytes)
	}
	return nil
}

// limitWriter passes at most max bytes on to w and fails the write that
// would exceed them.
type limitWriter struct {
	w      io.Writer
	max, n int64
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.n+int64(len(p)) > lw.max {
		return 0, errOutputLimit(lw.max)
	}
	n, err := lw.w.Write(p)
	lw.n += int64(n)
	return n, err
}

// checkParamExprs reports an expression of ParamExprs that does not
// evaluate. The engine would otherwise fall back to the Params text for it
// without a word; it evaluates them the same way, without a context item.
func checkParamExprs(exprs map[string]string) []Diagnostic {
	var diags []Diagnostic
	for name, expr := range exprs {
		p, err := xpath.Parse(expr)
		if err == nil {
			_, err = p.Eval(&xpath.Context{NoFocus: true})
		}
		if err != nil {
			d := xpathDiag("parse", err)
			if d.Code == "" {
				d.Code = "XPST0003"
			}
			d.Message = fmt.Sprintf("parameter $%s: %s", name, d.Message)
			diags = append(diags, d)
		}
	}
	return diags
}

func (l Limits) internal() xslt.Limits {
	return xslt.Limits{MaxSteps: l.MaxSteps, MaxDepth: l.MaxDepth}
}

// Features are the optional XSLT 3.0 capabilities (XSLT 3.0 §26) a
// stylesheet is compiled with. Each is off by default and applies only to the
// stylesheet of the request, so differently configured stylesheets can run at
// the same time. Streamed execution (xsl:source-document streamable="yes",
// streamable modes) is always available and needs no switch.
type Features struct {
	// SchemaAware makes the processor schema-aware: xsl:import-schema,
	// validation through [xsl:]validation and [xsl:]type, typed nodes, and
	// system-property('xsl:is-schema-aware') = "yes". Without it,
	// xsl:import-schema is a compile error.
	SchemaAware bool `json:"schemaAware"`
	// BackwardsCompatible enables XSLT 1.0 backwards-compatible processing
	// (§3.9.1) wherever [xsl:]version is below 2.0: the first-item rule of
	// xsl:value-of, XPath 1.0 conversions in arithmetic and comparisons, and
	// so on. Without it, a version="1.0" stylesheet runs under XSLT 3.0
	// rules.
	BackwardsCompatible bool `json:"backwardsCompatible"`
	// StrictStreaming makes every construct declared streamable that cannot
	// be proven guaranteed-streamable (§19) a compile error, XTSE3430.
	// Without it, such a construct runs with the whole document in memory.
	StrictStreaming bool `json:"strictStreaming"`
}

// compileOptions are the internal compile settings for a request.
func compileOptions(sec Security, feat Features, baseDir string) xslt.CompileOptions {
	return xslt.CompileOptions{
		Files: FileGuard(sec, baseDir),
		Features: &xslt.Features{
			SchemaAware:         feat.SchemaAware,
			BackwardsCompatible: feat.BackwardsCompatible,
			StrictStreaming:     feat.StrictStreaming,
			StreamingClaim:      true,
		},
	}
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
	// Rule is the rule identifier (msg:message/@rule) of a failed XSD
	// assertion; empty when not applicable.
	Rule string `json:"rule,omitempty"`
	// Path locates the element a failed XSD assertion was evaluated on, e.g.
	// /Doc[1]/Item[2]; empty when not applicable.
	Path string `json:"path,omitempty"`
	// Particle is the element declaration a content-model error refers to, as
	// an expanded name ({namespace}local); empty otherwise.
	Particle string `json:"particle,omitempty"`
	// Kind tells content-model errors apart: "missing", "too-many",
	// "unexpected" or "order"; empty otherwise.
	Kind string `json:"kind,omitempty"`
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
func Transform(req Request) Result { return TransformContext(context.Background(), req) }

// TransformContext is Transform that stops when ctx is done — a deadline, a
// cancelled request: the run ends with an error diagnostic whose message
// names ctx.Err(). The check is made every 1024 instructions and inside long
// XPath evaluations, so a run stops within moments of the cancellation.
func TransformContext(ctx context.Context, req Request) (res Result) {
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

	return run(ctx, req)
}
