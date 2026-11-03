# What is go-xslt?

go-xslt is an implementation of the W3C XML processing stack in Go:

- **XSLT 3.0** transformations
- **XPath 3.1** evaluation
- **XSD 1.0 and 1.1** validation

It is written from scratch. It does not wrap libxslt or libxml2, does not use
CGo and does not need a Java runtime. The only dependency is
`golang.org/x/text`, so `go get` is all the installation there is.

## Who it is for

- Go programs that need to run XSLT, for example to convert e-invoices,
  generate reports or reshape XML from another system.
- Services that validate XML against XSD schemas and want readable error
  messages for every defect, not only the first.
- Tools that inspect schemas statically, without an instance document.
- People who write stylesheets and want a fast desktop tester for them.

## How it is tested

Development is measured against the official W3C conformance test suites,
not against hand-picked examples. About 99% of the applicable cases pass in
each suite; see [Conformance](/conformance). Every case that still fails is
listed with its reason, and the test harness fails if an undocumented failure
appears.

## Two modules

The repository holds two Go modules, so that importing the engine never pulls
in GUI dependencies:

| Module | Import path | Dependencies | Use it for |
|---|---|---|---|
| Library (repository root) | `github.com/tim-riep/go-xslt` | `golang.org/x/text` | Embedding the engine in your Go code |
| Desktop (`desktop/`) | `github.com/tim-riep/go-xslt/desktop` | Wails v3 and the library | The [desktop tester](/desktop) |

## Stability

go-xslt is **before version 1.0**. The public API in `engine/` and `xsd/`, the
desktop app's project format and the internal architecture can still change
between releases. Pin a version in your `go.mod`.

## License

Apache License 2.0. The third-party components the library, the CLI and the
desktop app build on, with their license texts, are listed in
[`THIRD-PARTY-LICENSES.md`](https://github.com/tim-riep/go-xslt/blob/main/THIRD-PARTY-LICENSES.md)
(attached to every release; the desktop app shows them under Licenses).
