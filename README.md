<p align="center">
  <img src="assets/logo.svg" alt="go-xslt logo" width="128" height="128">
</p>

<h1 align="center">go-xslt</h1>

<p align="center">
  XSLT 3.0, XPath 3.1 and XSD 1.0 / 1.1 in pure Go.<br>
  <sub>by Tim Riep</sub>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/tim-riep/go-xslt"><img src="https://pkg.go.dev/badge/github.com/tim-riep/go-xslt.svg" alt="Go Reference"></a>
  <a href="https://github.com/tim-riep/go-xslt/actions/workflows/ci.yml"><img src="https://github.com/tim-riep/go-xslt/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/tim-riep/go-xslt/actions/workflows/conformance.yml"><img src="https://github.com/tim-riep/go-xslt/actions/workflows/conformance.yml/badge.svg" alt="W3C conformance"></a>
  <a href="https://codecov.io/gh/tim-riep/go-xslt"><img src="https://codecov.io/gh/tim-riep/go-xslt/graph/badge.svg" alt="Coverage"></a>
  <a href="https://github.com/tim-riep/go-xslt/releases/latest"><img src="https://img.shields.io/github/v/release/tim-riep/go-xslt" alt="Release"></a>
</p>

<p align="center">
  <a href="https://tim-riep.github.io/go-xslt/"><b>Documentation</b></a> ·
  <a href="https://tim-riep.github.io/go-xslt/conformance">Conformance</a> ·
  <a href="https://tim-riep.github.io/go-xslt/performance">Performance</a> ·
  <a href="https://github.com/tim-riep/go-xslt/releases">Desktop app</a>
</p>

go-xslt transforms, queries and validates XML in Go programs, without libxslt,
libxml2, CGo or a JVM:

- **XSLT 3.0** with streaming, schema awareness, packages, `xsl:evaluate`,
  JSON/maps/arrays and XSLT 1.0 compatibility
- **XPath 3.1** with the full function library
- **XSD 1.0 and 1.1** validation that reports every error with its location
- one dependency (`golang.org/x/text`), panic-safe: every failure comes back as
  a diagnostic with a W3C error code and line/column
- as a **library**, a **command-line tool**, and a **desktop app** for
  developing stylesheets

## Desktop app

<p align="center">
  <img src="assets/screenshot-transform.png" alt="The desktop app: a stylesheet, its source XML and the live output, docked side by side" width="900">
</p>

An editor for XSLT, XSD and XPath with live results, errors marked in the
code, project folders and saved runs, with its interface in 28 languages.
**[Download for macOS, Windows and
Linux](https://github.com/tim-riep/go-xslt/releases/latest)** (signed for macOS
and Windows) · [Desktop app guide →](https://tim-riep.github.io/go-xslt/desktop)

It lives in the separate `desktop/` module, so importing the library never
pulls in GUI dependencies.

## How it is tested

The engine is measured against the official W3C test suites — together about
127,000 test cases — not against hand-picked examples:

| Suite | Cases passed |
|---|---|
| XSLT 3.0 ([xslt30-test](https://github.com/w3c/xslt30-test)) | **99.9%** (8,635 of 8,647); with streaming, schema awareness and 1.0 compatibility switched on **99.8%** (12,288 of 12,317) |
| XPath 3.1 and F&O 3.1 ([qt3tests](https://github.com/w3c/qt3tests)) | **99.7%** (22,419 of 22,478) |
| XSD 1.0 ([xsdtests](https://github.com/w3c/xsdtests)) | **99.99%** (39,396 of 39,401) |
| XSD 1.1 (same) | **99.99%** (41,549 of 41,553) |

Measured on 2026-10-23 for v0.3.0 against the pinned suite commits; a
[nightly job](https://github.com/tim-riep/go-xslt/actions/workflows/conformance.yml)
reruns all four measurements and fails as soon as a number differs or a case
fails that is not documented.

- **Every failing case is listed with its reason** in
  [`docs/accepted-failures/`](docs/accepted-failures/README.md). The test run
  itself fails on any failure that is not listed there, so a regression cannot
  go unnoticed.
- Cases are only skipped when they test something outside the processor's
  definition (XQuery, or a variant written for a processor *without* a feature
  go-xslt has), and the skips are counted and named, never hidden in the pass
  rate.
- **Anyone can check the numbers**: `make conformance` clones the suites at
  the pinned commits, runs everything and compares the result with the table
  above.
- CI runs the library's own tests on every push, and the W3C suites every
  night. The memory bound of streaming is measured in tests, not just
  claimed.

[Conformance details →](https://tim-riep.github.io/go-xslt/conformance)

## Install

```bash
go get github.com/tim-riep/go-xslt                         # library (Go 1.25+)
go install github.com/tim-riep/go-xslt/cmd/go-xslt@latest  # command-line tool
```

The desktop app: see [above](#desktop-app).

## Use

```go
import "github.com/tim-riep/go-xslt/engine"

res := engine.Transform(engine.Request{
	Stylesheet: xsl,
	Source:     xml,
	Params:     map[string]string{"title": "Report"},
	BaseDir:    "/srv/job", // for xsl:import, document(), …
})
if res.HasErrors() {
	for _, d := range res.Diagnostics {
		fmt.Printf("%d:%d %s %s\n", d.Line, d.Col, d.Code, d.Message)
	}
	return
}
fmt.Println(res.Output)
```

Optional XSLT 3.0 features, resource limits and cancellation are part of the
request:

```go
res := engine.TransformContext(ctx, engine.Request{
	Stylesheet: xsl,
	Source:     xml,
	Features:   engine.Features{SchemaAware: true, BackwardsCompatible: true, StrictStreaming: true},
	Limits:     engine.Limits{MaxSteps: 1_000_000, MaxOutputBytes: 10 << 20},
})
```

Compile once and run many times, concurrently:

```go
sheet, diags := engine.Compile(engine.Request{Stylesheet: xsl, BaseDir: dir})
res := sheet.Transform(engine.TransformRequest{Source: xml})
```

Validate, and evaluate XPath:

```go
v := engine.Validate(engine.ValidateRequest{Schemas: []string{xsd}, Instance: xml, Version: engine.XSD11})
x := engine.EvalXPath(engine.XPathRequest{Expression: "sum(//item/@price)", Source: xml})
```

From the command line:

```bash
go-xslt transform -s style.xsl -o out.html input.xml
go-xslt validate -x order.xsd order.xml
go-xslt xpath 'sum(//item/@price)' order.xml
```

[Library guide →](https://tim-riep.github.io/go-xslt/guide/getting-started) ·
[Command line →](https://tim-riep.github.io/go-xslt/guide/cli) ·
[API reference →](https://tim-riep.github.io/go-xslt/reference/engine)

## Security

Stylesheets and documents can name other resources (`document()`,
`xsl:import`, a DTD, …). By default go-xslt reads only files under `BaseDir`,
makes no network requests and writes no files at all; only your program can
widen that, never the input.

| | Default | Setting |
|---|---|---|
| Local files | only under `BaseDir`; none without it | `Security.AllowedDirs`, `AllowAllFiles` |
| Network | off | `Security.AllowNetwork`, `AllowedHosts` |
| Writing files | never: `xsl:result-document` output is returned in `Result.SecondaryOutputs` | — |
| `xsl:evaluate`, `fn:transform` | same rules and budget as the calling stylesheet | — |
| Endless loops and recursion | stopped after 20 M instructions / 10,000 levels | `Limits.MaxSteps`, `MaxDepth` |
| Time | cancelled with a `context.Context` | `TransformContext`, `ValidateContext`, `EvalXPathContext` |
| Output size | not limited | `Limits.MaxOutputBytes` |

Input that would overflow the Go stack (documents nested over 10,000 levels,
absurdly nested expressions, endless recursion of inline functions) is
refused with an error instead of crashing the process. The memory of the
trees a run builds is not limited by the library; for a hard memory limit on
untrusted input, run it in a separate process.
[Security guide →](https://tim-riep.github.io/go-xslt/guide/security) ·
[Reporting a vulnerability →](SECURITY.md)

## Performance

Against `xsltproc` and `xmllint` (libxslt/libxml2, C) on the same XSLT 1.0
stylesheets, 37 MB input, whole process, Apple M4:

| Workload | go-xslt | libxslt / libxml2 |
|---|---|---|
| copy the document | 0.4 s | 0.7 s |
| recursive apply-templates | 0.8 s | 1.2 s |
| Muenchian grouping | 0.5 s | 0.9 s |
| XPath predicates and functions | 0.4 s | 0.8 s |
| XSD 1.0 validation | 0.4 s | 0.5 s |

So go-xslt is faster than the C libraries on every workload both can run,
validation included. It uses somewhat more memory, with streaming available
when memory matters. It implements XSLT 3.0, XPath 3.1 and XSD 1.1, which
they do not.
`go run scripts/bench/main.go` reproduces the table; `go run ./scripts/perfab`
is the A/B harness every performance change has to pass (23 workloads, no
workload may get slower, bigger or different).
[Performance details →](https://tim-riep.github.io/go-xslt/performance)

## Status

go-xslt is **before 1.0**: the API of `engine` and `xsd` may still change.
Known limits:

- no XQuery, and not XPath's optional static typing feature
- packages (`xsl:use-package`) are compiled into one unit; visibility rules are
  checked when names are resolved, not by separate execution
- Unicode data is version 15 (Go's); the suites' Unicode 9 expectations are
  excluded case by case

Everything the suites still fail is in
[`docs/accepted-failures/`](docs/accepted-failures/README.md).

## Contributing and license

From v0.3.0 on, bug reports are welcome as
[issues](https://github.com/tim-riep/go-xslt/issues); pull requests are not
accepted — see [CONTRIBUTING.md](CONTRIBUTING.md).
Apache License 2.0, see [LICENSE](LICENSE); the licenses of everything bundled
are listed in [THIRD-PARTY-LICENSES.md](THIRD-PARTY-LICENSES.md) and in the
desktop app's Licenses view.
