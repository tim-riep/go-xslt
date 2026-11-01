// Command go-xslt runs XSLT 3.0 transformations, XSD 1.0/1.1 validation and
// XPath 3.1 expressions from the command line, on the go-xslt engine.
//
//	go-xslt transform -s style.xsl [-o out.xml] [-p name=value]... [source.xml]
//	go-xslt validate [-x schema.xsd]... [-xsd 1.1] instance.xml
//	go-xslt xpath expression [source.xml]
//
// The flags mean the same in every command: -s/--stylesheet is a stylesheet,
// -x/--schema a schema, and the document to work on is always the last
// argument ("-" for stdin).
//
// Diagnostics go to stderr as file:line:col: [CODE] message. The exit status
// is 0 on success, 1 when the run fails or the instance is invalid, and 2 for
// a usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/tim-riep/go-xslt/engine"
)

const usage = `go-xslt: XSLT 3.0, XPath 3.1 and XSD 1.0/1.1 in pure Go

Usage:
  go-xslt transform -s style.xsl [options] [source.xml | -]
  go-xslt validate [-x schema.xsd]... [options] instance.xml
  go-xslt xpath [options] expression [source.xml | -]
  go-xslt version

Flags have a short and a long form: -s/--stylesheet, -x/--schema,
-o/--output, -p/--param.

Run "go-xslt <command> -h" for the options of a command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process: it returns the exit status.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "transform":
		return transform(args[1:], stdin, stdout, stderr)
	case "validate":
		return validate(args[1:], stdin, stdout, stderr)
	case "xpath":
		return xpath(args[1:], stdin, stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, "go-xslt", version())
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "go-xslt: unknown command %q\n\n%s", args[0], usage)
	return 2
}

// version is the module version the binary was built from ("(devel)" for a
// local build).
func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// listFlag collects a repeatable flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// accessFlags are the file and network options every command shares.
type accessFlags struct {
	dirs     listFlag
	allFiles bool
	network  bool
	hosts    listFlag
}

func (a *accessFlags) register(fs *flag.FlagSet) {
	fs.Var(&a.dirs, "allow-dir", "also allow reading files in this `directory` (repeatable)")
	fs.BoolVar(&a.allFiles, "allow-all-files", false, "allow reading any file the process can read")
	fs.BoolVar(&a.network, "allow-network", false, "allow retrieving http and https URIs")
	fs.Var(&a.hosts, "allow-host", "limit network access to this `host` (repeatable; .example.com admits subdomains)")
}

// security allows the given directories (the ones of the files named on the
// command line) plus whatever the flags add.
func (a *accessFlags) security(dirs ...string) engine.Security {
	allowed := append([]string{}, a.dirs...)
	for _, d := range dirs {
		if d != "" {
			allowed = append(allowed, d)
		}
	}
	return engine.Security{AllowedDirs: allowed, AllowAllFiles: a.allFiles, AllowNetwork: a.network, AllowedHosts: a.hosts}
}

// stringFlag defines a string flag under a short and a long name.
func stringFlag(fs *flag.FlagSet, short, long, usage string) *string {
	v := fs.String(short, "", usage)
	fs.StringVar(v, long, "", "same as -"+short)
	return v
}

// listFlags defines a repeatable flag under a short and a long name.
func listFlags(fs *flag.FlagSet, l *listFlag, short, long, usage string) {
	fs.Var(l, short, usage)
	fs.Var(l, long, "same as -"+short)
}

// timeoutFlag is the -timeout option every command shares.
func timeoutFlag(fs *flag.FlagSet) *time.Duration {
	return fs.Duration("timeout", 0, "stop after this `duration` (e.g. 30s; default: no limit)")
}

// withTimeout is the context of a run under -timeout.
func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), d)
}

func newFlagSet(name, synopsis string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: go-xslt %s\n\nOptions:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

func transform(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("transform", "transform -s style.xsl [options] [source.xml | -]", stderr)
	style := stringFlag(fs, "s", "stylesheet", "the stylesheet `file` (required, unless the source embeds one: <?xml-stylesheet href=\"#id\"?>)")
	out := stringFlag(fs, "o", "output", "write the principal result to this `file` instead of stdout")
	secondary := stringFlag(fs, "d", "output-dir", "write xsl:result-document outputs into this `directory` (default: not written)")
	it := stringFlag(fs, "it", "initial-template", "start with this named `template` (xsl:initial-template for the default)")
	var params listFlag
	listFlags(fs, &params, "p", "param", "set a stylesheet parameter: `name=value` (repeatable)")
	var paramExprs listFlag
	fs.Var(&paramExprs, "param-expr", "set a stylesheet parameter to an XPath expression's value: `name=expr` (repeatable), e.g. n=5")
	schemaAware := fs.Bool("schema-aware", false, "enable schema awareness (xsl:import-schema, validation)")
	backwards := fs.Bool("backwards-compatible", false, "process version=\"1.0\" stylesheet parts in XSLT 1.0 compatibility mode")
	strict := fs.Bool("strict-streaming", false, "reject streamable=\"yes\" constructs that are not guaranteed streamable")
	maxSteps := fs.Int64("max-steps", 0, "stop after this many instructions (default 20,000,000)")
	maxDepth := fs.Int("max-depth", 0, "stop beyond this nesting depth (default and maximum 10,000)")
	maxOutput := fs.Int64("max-output", 0, "stop when the results exceed this many `bytes` (default: no limit)")
	timeout := timeoutFlag(fs)
	var acc accessFlags
	acc.register(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}

	req := engine.Request{
		InitialTemplate: *it,
		Features:        engine.Features{SchemaAware: *schemaAware, BackwardsCompatible: *backwards, StrictStreaming: *strict},
		Limits:          engine.Limits{MaxSteps: *maxSteps, MaxDepth: *maxDepth, MaxOutputBytes: *maxOutput},
		Params:          map[string]string{},
	}
	for _, p := range params {
		name, value, ok := strings.Cut(p, "=")
		if !ok || name == "" {
			fmt.Fprintf(stderr, "go-xslt: -p %q: want name=value\n", p)
			return 2
		}
		req.Params[name] = value
	}
	if len(paramExprs) > 0 {
		req.ParamExprs = map[string]string{}
		for _, p := range paramExprs {
			name, expr, ok := strings.Cut(p, "=")
			if !ok || name == "" {
				fmt.Fprintf(stderr, "go-xslt: --param-expr %q: want name=expr\n", p)
				return 2
			}
			req.ParamExprs[name] = expr
		}
	}

	sourceName, sourceDir := "", ""
	if fs.NArg() == 1 {
		sourceName = fs.Arg(0)
		text, err := readInput(sourceName, stdin)
		if err != nil {
			fmt.Fprintln(stderr, "go-xslt:", err)
			return 1
		}
		req.Source = text
		if sourceName != "-" {
			sourceDir = filepath.Dir(sourceName)
			// The source document's own URI, not the stylesheet's directory.
			if abs, err := filepath.Abs(sourceName); err == nil {
				req.SourceURI = abs
			}
		}
	}
	styleName := *style
	if styleName != "" {
		text, err := os.ReadFile(styleName)
		if err != nil {
			fmt.Fprintln(stderr, "go-xslt:", err)
			return 1
		}
		req.Stylesheet = string(text)
		req.BaseDir = filepath.Dir(styleName)
	} else {
		if req.Source == "" {
			fmt.Fprintln(stderr, "go-xslt: no stylesheet (-s) and no source that embeds one")
			return 2
		}
		styleName, req.BaseDir = sourceName, sourceDir
	}
	if req.Source == "" && req.InitialTemplate == "" {
		// No source: start where XSLT 3.0 starts without one.
		req.InitialTemplate = "xsl:initial-template"
	}
	req.Security = acc.security(sourceDir)

	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(stderr, "go-xslt:", err)
			return 1
		}
		defer f.Close()
		w = f
	}
	ctx, cancel := withTimeout(*timeout)
	defer cancel()
	res := engine.TransformToContext(ctx, w, req)
	for _, m := range res.Messages {
		fmt.Fprintln(stderr, m)
	}
	report(stderr, styleName, res.Diagnostics)
	if res.HasErrors() {
		return 1
	}
	if *secondary != "" {
		if err := writeSecondary(*secondary, res.SecondaryOutputs); err != nil {
			fmt.Fprintln(stderr, "go-xslt:", err)
			return 1
		}
	} else if len(res.SecondaryOutputs) > 0 {
		fmt.Fprintf(stderr, "go-xslt: %d xsl:result-document output(s) not written; use -d dir\n", len(res.SecondaryOutputs))
	}
	return 0
}

// writeSecondary writes the xsl:result-document outputs under dir. An href
// that is absolute or climbs out of dir is refused rather than followed.
func writeSecondary(dir string, outs []engine.SecondaryOutput) error {
	for _, o := range outs {
		rel := strings.TrimPrefix(o.Href, "file://")
		if rel == "" || filepath.IsAbs(rel) || !filepath.IsLocal(filepath.FromSlash(rel)) {
			return fmt.Errorf("xsl:result-document href %q: only relative paths inside %s are written", o.Href, dir)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(o.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func validate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("validate", "validate [-x schema.xsd]... [options] instance.xml", stderr)
	var schemas listFlag
	listFlags(fs, &schemas, "x", "schema", "a schema `file` (repeatable; the first is the entry schema). Without -x the instance's xsi:schemaLocation hints are used")
	ver := fs.String("xsd", "1.0", "XSD `version`: 1.0 or 1.1")
	lang := fs.String("lang", "", "language of assertion messages (msg:message), e.g. de")
	maxErrors := fs.Int("max-errors", 0, "report at most this many errors (default: all)")
	quiet := fs.Bool("q", false, "print nothing; only the exit status tells")
	timeout := timeoutFlag(fs)
	var acc accessFlags
	acc.register(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || (*ver != "1.0" && *ver != "1.1") {
		fs.Usage()
		return 2
	}
	instName := fs.Arg(0)
	inst, err := readInput(instName, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "go-xslt:", err)
		return 1
	}
	req := engine.ValidateRequest{Instance: inst, Version: engine.XSDVersion(*ver), Language: *lang, MaxErrors: *maxErrors}
	if instName != "-" {
		req.BaseDir = filepath.Dir(instName)
	}
	var dirs []string
	for i, s := range schemas {
		text, err := os.ReadFile(s)
		if err != nil {
			fmt.Fprintln(stderr, "go-xslt:", err)
			return 1
		}
		req.Schemas = append(req.Schemas, string(text))
		if i == 0 {
			req.BaseDir = filepath.Dir(s) // xs:include/xs:import resolve against the entry schema
		}
		dirs = append(dirs, filepath.Dir(s))
	}
	if instName != "-" {
		dirs = append(dirs, filepath.Dir(instName))
	}
	req.Security = acc.security(dirs...)
	ctx, cancel := withTimeout(*timeout)
	defer cancel()
	res := engine.ValidateContext(ctx, req)
	if !*quiet {
		report(stderr, instName, res.Diagnostics)
		if res.Valid {
			fmt.Fprintf(stdout, "%s: valid\n", instName)
		} else {
			fmt.Fprintf(stdout, "%s: invalid\n", instName)
		}
	}
	if !res.Valid {
		return 1
	}
	return 0
}

func xpath(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("xpath", "xpath [options] expression [source.xml | -]", stderr)
	typed := fs.Bool("t", false, "print each item's type before its value")
	var nsFlags, vars, varExprs listFlag
	listFlags(fs, &nsFlags, "n", "ns", "bind a namespace prefix: `prefix=uri` (repeatable); the document element's prefixes are bound already")
	fs.Var(&vars, "var", "bind $name to a string: `name=value` (repeatable)")
	fs.Var(&varExprs, "var-expr", "bind $name to an XPath expression's value: `name=expr` (repeatable)")
	defaultNS := fs.String("default-ns", "", "the namespace `uri` of unprefixed element names")
	timeout := timeoutFlag(fs)
	var acc accessFlags
	acc.register(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fs.Usage()
		return 2
	}
	req := engine.XPathRequest{Expression: fs.Arg(0)}
	srcDir := ""
	if fs.NArg() == 2 {
		name := fs.Arg(1)
		text, err := readInput(name, stdin)
		if err != nil {
			fmt.Fprintln(stderr, "go-xslt:", err)
			return 1
		}
		req.Source = text
		if name != "-" {
			srcDir = filepath.Dir(name)
		}
	} else if wd, err := os.Getwd(); err == nil {
		srcDir = wd
	}
	req.BaseDir = srcDir
	req.Security = acc.security()
	req.DefaultElementNamespace = *defaultNS
	for _, kv := range []struct {
		flag string
		list listFlag
		dst  *map[string]string
	}{{"--ns", nsFlags, &req.Namespaces}, {"--var", vars, &req.Variables}, {"--var-expr", varExprs, &req.VariableExprs}} {
		for _, p := range kv.list {
			k, v, ok := strings.Cut(p, "=")
			if !ok || k == "" {
				fmt.Fprintf(stderr, "go-xslt: %s %q: want name=value\n", kv.flag, p)
				return 2
			}
			if *kv.dst == nil {
				*kv.dst = map[string]string{}
			}
			(*kv.dst)[k] = v
		}
	}
	ctx, cancel := withTimeout(*timeout)
	defer cancel()
	res := engine.EvalXPathContext(ctx, req)
	report(stderr, "", res.Diagnostics)
	if len(res.Diagnostics) > 0 && res.Diagnostics[0].Severity == engine.SeverityError {
		return 1
	}
	for _, it := range res.Items {
		if *typed {
			fmt.Fprintf(stdout, "%s\t%s\n", it.Type, it.Value)
		} else {
			fmt.Fprintln(stdout, it.Value)
		}
	}
	return 0
}

// readInput reads a named file, or stdin for "-".
func readInput(name string, stdin io.Reader) (string, error) {
	var b []byte
	var err error
	if name == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s: no such file", name)
		}
		return "", err
	}
	return string(b), nil
}

// report prints diagnostics as file:line:col: severity: [CODE] message.
func report(w io.Writer, file string, diags []engine.Diagnostic) {
	for _, d := range diags {
		var loc string
		switch {
		case file != "" && d.Line > 0:
			loc = fmt.Sprintf("%s:%d:%d: ", file, d.Line, d.Col)
		case file != "":
			loc = file + ": "
		}
		code := ""
		if d.Code != "" && !strings.Contains(d.Message, d.Code) {
			code = "[" + d.Code + "] "
		}
		fmt.Fprintf(w, "%s%s: %s%s\n", loc, d.Severity, code, d.Message)
	}
}
