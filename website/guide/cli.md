# Command line

`go-xslt` runs transformations, validation and XPath expressions from a shell,
a pipeline or CI, like `xsltproc` and `xmllint`:

```bash
go install github.com/tim-riep/go-xslt/cmd/go-xslt@latest
```

It is part of the library module, so it needs nothing beyond Go.

The options mean the same in every command: `-s`/`--stylesheet` is a
stylesheet, `-x`/`--schema` a schema, and the document to work on is always
the last argument (`-` reads it from stdin).

## Transform

```bash
go-xslt transform -s style.xsl input.xml > output.xml
go-xslt transform -s style.xsl -p title="Annual report" -o report.html input.xml
cat input.xml | go-xslt transform -s style.xsl -
go-xslt transform -s style.xsl            # no source: starts at xsl:initial-template
go-xslt transform input.xml               # the source names its own stylesheet
```

| Option | |
|---|---|
| `input.xml` | The source, always the last argument. It is the source document's URI: `document-uri(/)` and `base-uri(/)` are this file, whichever directory the stylesheet is in. From stdin (`-`) the source has no document URI. |
| `-s`, `--stylesheet file` | The stylesheet. Without it, the source must embed one (`<?xml-stylesheet href="#id"?>`). |
| `-o`, `--output file` | Write the result to a file instead of stdout. |
| `-p`, `--param name=value` | A stylesheet parameter (repeatable), as a string. |
| `--param-expr name=expr` | A stylesheet parameter as the value of an XPath expression (repeatable), e.g. `n=5`. |
| `-it`, `--initial-template name` | Start with this named template. |
| `-d`, `--output-dir dir` | Write the `xsl:result-document` outputs into this directory. Only relative paths inside it are written. Without it they are not written, and a note says so. |
| `--schema-aware`, `--backwards-compatible`, `--strict-streaming` | The [optional features](/reference/engine#features). |
| `--max-steps n`, `--max-depth n`, `--max-output bytes` | The [resource limits](/guide/security#resource-limits). |
| `--timeout 30s` | Stop the run after this time. |

`xsl:message` output goes to stderr.

## Validate

```bash
go-xslt validate -x order.xsd order.xml
go-xslt validate -xsd 1.1 -x main.xsd -x types.xsd -lang de order.xml
go-xslt validate order.xml                # uses the instance's xsi:schemaLocation
```

Prints `order.xml: valid` or `invalid` with every error. `-x`/`--schema`
names a schema (repeatable, the first is the entry schema), `--max-errors n`
stops after n errors, `-q` prints nothing, `--timeout` stops the validation
after that time.

## XPath

```bash
go-xslt xpath 'sum(//item/@price)' order.xml
go-xslt xpath 'format-date(current-date(), "[D] [MNn] [Y]", "de", (), ())'
go-xslt xpath -t '1 to 3, "x"'            # with each item's type
curl -s https://example.com/feed.xml | go-xslt xpath 'count(//item)' -
```

The expression comes first, the document (optional) second. Each item of the
result is printed on its own line.

| Option | |
|---|---|
| `-n`, `--ns prefix=uri` | Bind a namespace prefix (repeatable). The prefixes the document element declares are bound already. |
| `--default-ns uri` | The namespace of element names without a prefix. |
| `--var name=value` | Bind `$name` to a string (repeatable). |
| `--var-expr name=expr` | Bind `$name` to the value of an XPath expression (repeatable). |
| `-t` | Print each item's type before its value. |

```bash
go-xslt xpath --ns inv=urn:oasis:names:specification:ubl:schema:xsd:Invoice-2 \
  'string(/inv:Invoice/cbc:ID)' invoice.xml
```

## Files and network

Each command may read the files in the directories of the files named on its
command line: the stylesheet, the source, the schemas. To allow more:

| Option | |
|---|---|
| `-allow-dir dir` | Also read files in this directory (repeatable). |
| `-allow-all-files` | Read any file. |
| `-allow-network` | Retrieve `http` and `https` URIs. |
| `-allow-host name` | Only from these hosts (repeatable). |

See [Security](/guide/security).

## Exit status and messages

| Status | |
|---|---|
| 0 | success; the instance is valid |
| 1 | the run failed, or the instance is invalid |
| 2 | wrong usage |

Errors are printed as `file:line:col: error: [CODE] message`, which editors and
CI systems pick up as locations.
