# Transformations

`engine.Transform` compiles a stylesheet, runs it against a source document and
returns the serialized result.

```go
res := engine.Transform(engine.Request{
	Stylesheet:      stylesheetText,
	Source:          sourceText,
	Params:          map[string]string{"lang": "de"},
	InitialTemplate: "",
	BaseDir:         "/srv/jobs/42",
})
```

## The request

| Field | Meaning |
|---|---|
| `Stylesheet` | The XSLT source text. A top-level `xsl:package` is accepted as well as `xsl:stylesheet`. |
| `Source` | The source document as text. It may be empty when `InitialTemplate` is set. |
| `Params` | Values for global `xsl:param`s, by name. Values are strings. |
| `ParamExprs` | Values for global `xsl:param`s as XPath expressions, so they can be typed: `"5"`, `"xs:date('2026-01-31')"`, `"map{'a': 1}"`, `"parse-xml('<x/>')"`. Evaluated without a context item; an expression that does not evaluate is reported as an error. A name here wins over `Params`. |
| `InitialTemplate` | The name of a template to start with, instead of applying templates to the source document. |
| `BaseDir` | The directory against which relative URIs are resolved: `xsl:import`, `xsl:include`, `document()`, `fn:doc`, `fn:unparsed-text`, collections and `xsl:result-document`. Empty means no file is read. |
| `SourceURI` | The file path or URI `Source` was read from. It becomes the document URI and base URI of the source document (`fn:document-uri`, `fn:base-uri`, and what relative references taken from it, such as `document(@href)`, resolve against). `BaseDir` stays the base of the stylesheet. Empty means the source is parsed against `BaseDir` and has no document URI. |
| `Security` | Which files and network resources the run may read. See [Security](/guide/security). |
| `Features` | Optional XSLT 3.0 features: schema awareness, XSLT 1.0 compatibility, strict streaming. See [Features](/reference/engine#features). |
| `Limits` | How many instructions the run may execute and how deeply it may nest. See [Resource limits](/guide/security#resource-limits). |

An empty `Stylesheet` means the one the source embeds and names in an
`<?xml-stylesheet href="#id"?>` instruction; see
[Embedded stylesheets](/reference/engine#embedded-stylesheets).

## The result

| Field | Meaning |
|---|---|
| `Output` | The principal result, serialized with the stylesheet's `xsl:output`. |
| `Method` | The effective output method: `xml`, `html`, `text` or `json`. |
| `Messages` | Text written by `xsl:message`. |
| `SecondaryOutputs` | The documents created by `xsl:result-document`, each with `Href`, `Content` and `Method`. |
| `Diagnostics` | Errors and warnings. `res.HasErrors()` tells whether any of them is an error. |
| `DurationMs` | Wall-clock time of the run. |

When a fatal error occurs, `Output` is empty and `Diagnostics` holds at least
one error.

## Secondary outputs

go-xslt never writes files. Each `xsl:result-document` comes back in
`SecondaryOutputs`, and your program decides where it goes:

```go
for _, out := range res.SecondaryOutputs {
	path := filepath.Join(outDir, filepath.Base(out.Href))
	if err := os.WriteFile(path, []byte(out.Content), 0o644); err != nil {
		return err
	}
}
```

## Reading the source from a stream

If the source document is already an `io.Reader` (a file, an HTTP body), use
`TransformFromReader`. It saves the copy you would otherwise make by reading
it into a string yourself. `req.Source` is ignored.

```go
f, err := os.Open("input.xml")
if err != nil {
	return err
}
defer f.Close()
res := engine.TransformFromReader(f, engine.Request{Stylesheet: stylesheetText})
```

## Writing the result to a stream

`TransformTo` writes the principal result to an `io.Writer` instead of
returning it in `Output`. The bytes are the same as with `Transform`. For
large outputs this matters: see [Streaming](/guide/streaming).

```go
res := engine.TransformTo(w, engine.Request{Stylesheet: s, Source: src})
```

If a run fails half-way, some bytes may already have been written to `w`; the
caller has to discard them.

## Running the same stylesheet many times

`Transform` compiles the stylesheet on every call. If the stylesheet stays the
same and only the data changes, compile it once: see
[Compiled stylesheets](/guide/compiled-stylesheets).
