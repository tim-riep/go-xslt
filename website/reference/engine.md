# Package engine

```go
import "github.com/tim-riep/go-xslt/engine"
```

The public entry point for transformations, XPath evaluation and XSD
validation. No function in this package panics: errors come back as
[diagnostics](/guide/diagnostics).

Full generated documentation:
[pkg.go.dev/github.com/tim-riep/go-xslt/engine](https://pkg.go.dev/github.com/tim-riep/go-xslt/engine)

## Functions

| Function | Does |
|---|---|
| `Transform(Request) Result` | Compiles and runs a stylesheet. |
| `TransformTo(io.Writer, Request) Result` | Like `Transform`, but writes the principal result to the writer. `Result.Output` stays empty. |
| `TransformFromReader(io.Reader, Request) Result` | Like `Transform`, but reads the source document from the reader. `Request.Source` is ignored. |
| `Compile(Request) (*Stylesheet, []Diagnostic)` | Compiles a stylesheet for repeated use. |
| `EvalXPath(XPathRequest) XPathResult` | Evaluates a standalone XPath 3.1 expression. |
| `Validate(ValidateRequest) ValidateResult` | Validates an XML document against XSD schemas. |
| `TransformContext`, `TransformToContext`, `EvalXPathContext`, `ValidateContext` | The same, taking a `context.Context` first: the run stops when it is done. See [Time](/guide/security#time-cancel-with-a-context). |

## Stylesheet

A compiled stylesheet. Immutable and safe for concurrent use.

| Method | Does |
|---|---|
| `(*Stylesheet) Transform(TransformRequest) Result` | Runs the stylesheet. |
| `(*Stylesheet) TransformTo(io.Writer, TransformRequest) Result` | Runs it and writes the principal result to the writer. |
| `(*Stylesheet) TransformFromReader(io.Reader, TransformRequest) Result` | Runs it with the source read from the reader. |
| `(*Stylesheet) TransformContext`, `TransformToContext` | The same with a `context.Context` that cancels the run. |

## Request

```go
type Request struct {
	Stylesheet      string            // XSLT source text; empty = the one the Source embeds
	Source          string            // source document; may be empty with InitialTemplate
	Params          map[string]string // global stylesheet parameters, as strings
	ParamExprs      map[string]string // … or as the value of an XPath expression
	InitialTemplate string            // named template to start with
	BaseDir         string            // resolves relative URIs; empty = no file reads
	SourceURI       string            // where Source was read from; empty = parsed against BaseDir
	Security        Security
	Features        Features          // optional XSLT 3.0 features; zero = all off
	Limits          Limits            // instruction steps and nesting depth; zero = defaults
}
```

### Embedded stylesheets

When `Stylesheet` is empty, `Transform` and `TransformTo` use the stylesheet
the source document carries itself (XSLT 3.0 §3.13): the source names it in an
`xml-stylesheet` processing instruction before its document element, and the
fragment identifier is the ID of the stylesheet element.

```xml
<?xml-stylesheet type="text/xsl" href="#style1"?>
<!DOCTYPE doc [<!ATTLIST xsl:stylesheet id ID #REQUIRED>]>
<doc xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:stylesheet id="style1" version="3.0">
    <xsl:template match="/">…</xsl:template>
  </xsl:stylesheet>
  …
</doc>
```

The ID must be an ID in the XML sense: declared `ID` in the DTD, or an
`xml:id` attribute. The embedded module keeps the namespaces it inherits from
its ancestors and nothing else of them, so their attributes (such as
`xsl:version`) have no effect. Only a stylesheet inside the document itself
(`href="#id"`) is used; a separate file named in the processing instruction
is not loaded. A source without such an instruction is an error, as an empty
stylesheet always was.

## TransformRequest

One run of a compiled `Stylesheet`. The stylesheet, its `BaseDir` and its
`Features` were fixed by `Compile`.

```go
type TransformRequest struct {
	Source          string
	SourceURI       string
	Params          map[string]string
	ParamExprs      map[string]string
	InitialTemplate string
	Security        Security
	Limits          Limits
}
```

## Result

```go
type Result struct {
	Output           string            // principal result, serialized
	Diagnostics      []Diagnostic
	DurationMs       int64
	Method           string            // "xml", "html", "text" or "json"
	Messages         []string          // xsl:message output
	SecondaryOutputs []SecondaryOutput // xsl:result-document output
}

func (r Result) HasErrors() bool

type SecondaryOutput struct {
	Href    string
	Content string
	Method  string
}
```

## XPathRequest and XPathResult

```go
type XPathRequest struct {
	Expression              string
	Source                  string            // optional context document
	BaseDir                 string
	Security                Security
	Namespaces              map[string]string // prefix → URI; plus the document element's and the standard ones
	DefaultElementNamespace string            // namespace of unprefixed element names
	Variables               map[string]string // $name → string
	VariableExprs           map[string]string // $name → value of an XPath expression
}

type XPathResult struct {
	Value       string      // string value of the whole sequence
	Items       []XPathItem // one per item
	Count       int
	Diagnostics []Diagnostic
	DurationMs  int64
}

func (r XPathResult) HasErrors() bool

type XPathItem struct {
	Type  string // "element", "xs:integer", "map", …
	Value string
}
```

## ValidateRequest and ValidateResult

```go
type ValidateRequest struct {
	Schemas   []string   // the first is the entry schema; none: the instance's xsi:schemaLocation
	Instance  string
	Version   XSDVersion // XSD10 (default) or XSD11
	BaseDir   string
	Language  string     // xml:lang of assertion messages
	MaxErrors int        // 0 = no limit
	Security  Security   // file access only; no network
}

type ValidateResult struct {
	Valid       bool
	Diagnostics []Diagnostic
	DurationMs  int64
}
```

## Security

```go
type Security struct {
	AllowedDirs   []string // more readable directories besides BaseDir
	AllowAllFiles bool     // lift the file restriction (trusted input only)
	AllowNetwork  bool     // allow http and https
	AllowedHosts  []string // limit network access; ".example.com" admits subdomains
}
```

The zero value allows files under `BaseDir` and no network. See
[Security](/guide/security).

## Features

```go
type Features struct {
	SchemaAware         bool // xsl:import-schema, validation, typed nodes
	BackwardsCompatible bool // XSLT 1.0 rules where [xsl:]version < 2.0
	StrictStreaming     bool // unprovable streamable constructs are XTSE3430
}
```

All three are off by default and apply only to the stylesheet of the
request; stylesheets compiled with different `Features` can run at the same
time. See [Optional features](/guide/xslt#optional-features).

## Limits

```go
type Limits struct {
	MaxSteps       int64 // instructions per run; default 20,000,000
	MaxDepth       int   // nesting of instructions, templates, functions; default and maximum 10,000
	MaxOutputBytes int64 // principal and secondary results together; 0 = no limit
}
```

A run that exceeds a limit stops with `XPDY0130`. Runs started with
`fn:transform` spend from the same budget. See
[Resource limits](/guide/security#resource-limits).

## Diagnostic

```go
type Diagnostic struct {
	Severity string // SeverityError or SeverityWarning
	Line     int
	Col      int
	Code     string // W3C error code
	Message  string
	Phase    string // "parse", "compile" or "run"
	Rule     string // XSD assertion rule
	Path     string // XSD element path
	Particle string // XSD content-model particle
	Kind     string // "missing", "too-many", "unexpected" or "order"
}
```

## Constants

| Constant | Value |
|---|---|
| `SeverityError` | `"error"` |
| `SeverityWarning` | `"warning"` |
| `XSD10` | `"1.0"` |
| `XSD11` | `"1.1"` |
