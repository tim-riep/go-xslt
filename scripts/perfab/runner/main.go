// Command runner is the measured process of scripts/perfab: one engine
// workload per invocation, through the public engine package exactly as the
// CLI uses it (TransformToContext, ValidateContext, Compile, Transform). It
// prints one JSON line with the wall time of the work itself and the
// process's allocation totals; the harness adds the process wall time and
// the peak RSS.
//
// The harness builds this file into the tree of each revision it compares
// (through a build overlay), so a revision that predates it can be measured
// too. It must therefore use only the public engine API.
//
//	runner -w transform|validate|compile|many|many-compiled [-s style] [-x schema]
//	       [-xsd 1.0|1.1] [-o out] [-n count] [-p name=value]... [-bc] [-it name] [source]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/engine"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	kind := flag.String("w", "transform", "workload kind: transform, validate, compile, many, many-compiled")
	style := flag.String("s", "", "stylesheet file")
	schema := flag.String("x", "", "schema file")
	xsd := flag.String("xsd", "1.0", "XSD version")
	out := flag.String("o", "", "output file (the principal result; validate writes its verdict)")
	count := flag.Int("n", 1, "repetitions for compile, many and many-compiled")
	bc := flag.Bool("bc", false, "backwards-compatible processing")
	it := flag.String("it", "", "initial template")
	var params listFlag
	flag.Var(&params, "p", "stylesheet parameter name=value (repeatable)")
	flag.Parse()

	var source string
	sourcePath := ""
	if flag.NArg() == 1 {
		sourcePath = flag.Arg(0)
		b, err := os.ReadFile(sourcePath)
		fail(err)
		source = string(b)
	}
	w := io.Writer(io.Discard)
	if *out != "" {
		f, err := os.Create(*out)
		fail(err)
		defer f.Close()
		w = f
	}

	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	start := time.Now()
	var err error
	switch *kind {
	case "validate":
		err = validate(w, *schema, *xsd, sourcePath, source)
	case "compile":
		err = compile(*style, *count, *bc)
	case "many", "many-compiled":
		err = many(w, *kind == "many-compiled", *count, request(*style, source, sourcePath, *bc, *it, params))
	default:
		err = transform(w, request(*style, source, sourcePath, *bc, *it, params))
	}
	wall := time.Since(start)
	runtime.ReadMemStats(&ms1)
	fail(err)
	fail(json.NewEncoder(os.Stdout).Encode(map[string]any{
		"wall_ns":     wall.Nanoseconds(),
		"alloc_bytes": ms1.TotalAlloc - ms0.TotalAlloc,
		"mallocs":     ms1.Mallocs - ms0.Mallocs,
	}))
}

func request(style, source, sourcePath string, bc bool, it string, params []string) engine.Request {
	b, err := os.ReadFile(style)
	fail(err)
	req := engine.Request{
		Stylesheet:      string(b),
		Source:          source,
		BaseDir:         filepath.Dir(style),
		InitialTemplate: it,
		Features:        engine.Features{BackwardsCompatible: bc},
		Params:          map[string]string{},
		Security:        engine.Security{AllowAllFiles: true},
	}
	if sourcePath != "" {
		if abs, err := filepath.Abs(sourcePath); err == nil {
			req.SourceURI = abs
		}
	} else if it == "" {
		req.InitialTemplate = "xsl:initial-template"
	}
	for _, p := range params {
		name, value, _ := strings.Cut(p, "=")
		req.Params[name] = value
	}
	return req
}

func transform(w io.Writer, req engine.Request) error {
	res := engine.TransformTo(w, req)
	return diagnostics(res.Diagnostics)
}

func validate(w io.Writer, schema, version, sourcePath, source string) error {
	req := engine.ValidateRequest{Instance: source, Version: engine.XSDVersion(version), BaseDir: filepath.Dir(sourcePath), Security: engine.Security{AllowAllFiles: true}}
	if schema != "" {
		b, err := os.ReadFile(schema)
		if err != nil {
			return err
		}
		req.Schemas = []string{string(b)}
		req.BaseDir = filepath.Dir(schema)
	}
	res := engine.Validate(req)
	fmt.Fprintf(w, "valid=%v\n", res.Valid)
	for _, d := range res.Diagnostics {
		fmt.Fprintf(w, "%d:%d [%s] %s\n", d.Line, d.Col, d.Code, d.Message)
	}
	return nil
}

func compile(style string, n int, bc bool) error {
	b, err := os.ReadFile(style)
	if err != nil {
		return err
	}
	req := engine.Request{Stylesheet: string(b), BaseDir: filepath.Dir(style), Features: engine.Features{BackwardsCompatible: bc}, Security: engine.Security{AllowAllFiles: true}}
	for i := 0; i < n; i++ {
		ss, diags := engine.Compile(req)
		if ss == nil {
			return diagnostics(diags)
		}
	}
	return nil
}

// many transforms the same small source n times, compiling the stylesheet
// every time (the simplest library use) or once (precompiled). The last
// result is the output.
func many(w io.Writer, precompiled bool, n int, req engine.Request) error {
	var last engine.Result
	if precompiled {
		ss, diags := engine.Compile(req)
		if ss == nil {
			return diagnostics(diags)
		}
		tr := engine.TransformRequest{Source: req.Source, SourceURI: req.SourceURI, Params: req.Params, InitialTemplate: req.InitialTemplate}
		for i := 0; i < n; i++ {
			last = ss.Transform(tr)
			if last.HasErrors() {
				break
			}
		}
	} else {
		for i := 0; i < n; i++ {
			last = engine.Transform(req)
			if last.HasErrors() {
				break
			}
		}
	}
	if err := diagnostics(last.Diagnostics); err != nil {
		return err
	}
	_, err := io.WriteString(w, last.Output)
	return err
}

func diagnostics(ds []engine.Diagnostic) error {
	for _, d := range ds {
		if d.Severity == engine.SeverityError {
			return fmt.Errorf("%d:%d [%s] %s", d.Line, d.Col, d.Code, d.Message)
		}
	}
	return nil
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner:", err)
		os.Exit(1)
	}
}
