# Compiled stylesheets

`engine.Compile` compiles a stylesheet without running it. The resulting
`*engine.Stylesheet` can be run any number of times against changing data.

```go
sheet, diags := engine.Compile(engine.Request{
	Stylesheet: stylesheetText,
	BaseDir:    dir, // xsl:import and xsl:include are resolved here
})
if sheet == nil {
	for _, d := range diags {
		log.Printf("%d:%d %s %s", d.Line, d.Col, d.Code, d.Message)
	}
	return
}

res := sheet.Transform(engine.TransformRequest{
	Source: sourceText,
	Params: map[string]string{"currency": "EUR"},
})
```

`Compile` uses only `Stylesheet`, `BaseDir` and `Security` of the request.
`Source`, `SourceURI`, `Params` and `InitialTemplate` belong to each run and go
into the `TransformRequest`.

When compilation fails, the returned stylesheet is `nil` and the diagnostics
carry the error with its line and column in the stylesheet. A successful
compile can still return warnings.

## Concurrency

A `*engine.Stylesheet` is **immutable and safe for concurrent use**. Every run
builds its own state: variables, key indexes, the document cache and the
result tree. A server can compile its stylesheets at start-up and share them
between all request handlers.

```go
var invoiceSheet *engine.Stylesheet // compiled once at start-up

func handle(w http.ResponseWriter, r *http.Request) {
	res := invoiceSheet.TransformFromReader(r.Body, engine.TransformRequest{})
	if res.HasErrors() {
		http.Error(w, res.Diagnostics[0].Message, http.StatusUnprocessableEntity)
		return
	}
	io.WriteString(w, res.Output)
}
```

## Methods

| Method | Like |
|---|---|
| `sheet.Transform(TransformRequest)` | `engine.Transform` |
| `sheet.TransformTo(io.Writer, TransformRequest)` | `engine.TransformTo` |
| `sheet.TransformFromReader(io.Reader, TransformRequest)` | `engine.TransformFromReader` |

## Mapping errors back

A runtime error carries the W3C error `Code` and the `Line` and `Col` of the
innermost stylesheet instruction it passed through. A program that
*generates* stylesheets from its own templates can use that position to point
at its own source.

## Per-run security

The `Security` of the request given to `Compile` limits what the compiler
reads (imported modules, schemas, `use-when`). Each `TransformRequest` has its
own `Security` for what the run reads. Files are always resolved against the
`BaseDir` fixed at compile time. See [Security](/guide/security).
