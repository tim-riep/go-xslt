# go-xslt — notes for Claude

Pure-Go **XSLT 3.0 / XPath 3.1 engine** built **from scratch** (no
libxslt/CGo/JRE), shipped as an importable library **plus** an optional desktop
**XSLT 3.0 tester** (Wails v3 / React / CodeMirror GUI; macOS native + Windows
cross-compiled from a Mac).

## Two modules

The repo is a monorepo of two Go modules so the engine can be imported without
dragging in the GUI's Wails/CGo dependencies:

- **root** `github.com/tim-riep/go-xslt` — the **library**. Only dependency is
  `golang.org/x/text`. Contains `engine/`, `internal/…`, `test/conformance/`.
- **`desktop/`** `github.com/tim-riep/go-xslt/desktop` — the **Wails GUI app**.
  Requires wails/v3 + the library (via `replace … => ../`). Contains `main.go`,
  `xsltservice.go`, `internal/workspace`, `frontend/`, `build/`.

## Layout

- `engine/` — **public importable facade** (`package engine`, path
  `github.com/tim-riep/go-xslt/engine`). Re-exports `internal/engine` via type
  aliases: `Transform(Request) Result`, `TransformTo(io.Writer, Request) Result`
  (bounded-memory output), `Request`/`Result`/`Diagnostic`/…
- `xsd/` — **public read-only schema component model** (`Compile` →
  `*Schema`: global elements/types/attributes, substitution members, content
  particles with min/max, attribute uses, builtin bases through restriction
  chains). It is a thin alias layer over `internal/xsd/model.go`'s `*View`
  wrappers, so the compiler's own components stay private and free to change.
  `engine.Compile` → `*engine.Stylesheet` (immutable, concurrent-safe,
  `-race`-tested) is the precompiled-stylesheet API; a runtime error carries the
  stylesheet `Line`/`Col` of the innermost instruction it passed through
  (`engine.errSite`, set in `execInstr`, applied once at the top so error
  identity is untouched) and its W3C `Code`.
- `internal/engine` — **stable internal facade**. `Transform(Request) Result`.
  Panic-safe. An empty `Request.Stylesheet` means the source's embedded one
  (`<?xml-stylesheet href="#id"?>`, XSLT 3.0 §3.13 — `xslt.EmbeddedStylesheet`
  serializes that element out of the source with its inherited namespaces;
  `href="#id"` only; the harness maps `defines-stylesheet="true"` to it). The `engine/` re-export, GUI, and tests depend only on this;
  rewrite anything behind it freely.
- `internal/xmltree` — own XML node tree, document order, line/col. The
  parser (`parse.go`) runs on its own string tokenizer (`tokenizer.go`): a
  step-for-step reimplementation of `encoding/xml`'s `RawToken` (same tokens,
  offsets, accept/reject and error text — `FuzzTokenizer` and
  `TestTokenizerMatchesEncodingXMLOnSuites` diff it against encoding/xml,
  `TestXMLConf` records the W3C XML suite's verdicts) that hands out
  substrings of the source instead of copies. Change it only together with
  those gates; where encoding/xml is laxer than XML 1.0, it stays as lax.
  Also a serializer for xml/html/text (`serialize.go`, plus an
  `io.Writer`-based `WriteTo`/`ChunkWriter` for bounded-memory output);
  `stream.go` is the incremental pull-reader behind XSLT streaming (pull-style
  over `xml.Decoder.RawToken()`, builds ordinary `*Node`s one record at a
  time). `CopyTypeInfo(dst, src)` must be used at every node-rebuild/copy site
  to carry `TypeAnno`/`SchemaType`/`Nilled`/`ListTyped`/etc. forward.
- `internal/xpath` — `lexer.go`, `parser.go`/`ast.go`, `eval.go` + `axes.go` +
  `compare.go` + `functions.go` (core fn library), `pattern.go` (XSLT match
  patterns + default priorities), `value.go` (XPath-1.0 value model), the F&O
  3.1 catalog split across `fn_*.go` family files (registered into
  `coreFuncs`/`mapFuncs`/`arrayFuncs`/`mathFuncs` via `init()` — API contract:
  `internal/xpath/FNAPI.md`; each built-in also needs its signature line in
  `fn_catalog.go`, see the first Conventions bullet), `streamability.go` (posture/sweep
  classification, XSLT 3.0 §19/Appendix J), `schema_names.go` (schema-aware
  name resolution for `element(N,T)`/`schema-element(N)`), `static_axes.go`
  (XPath 3.1's *optional* Static Typing Feature — narrow static-emptiness
  axis-step analysis only, gated behind `ParseStaticTyping`; ordinary `Parse`
  is untouched). Cross-package seams `internal/xpath` cannot resolve on its
  own (schema types, streamable functions, …) are always **injected hooks**
  on `Context` (`SchemaTypes`/`SchemaNameLookup`/`TransformFunc`/…), mirroring
  the pre-existing `ResolveNamedFunction` pattern — never a direct import,
  since `internal/xmltree ← internal/xpath ← internal/xsd`/`xslt` is a real
  cycle constraint.
- `internal/xslt` — `compile.go` (stylesheet → instruction tree; also accepts
  a top-level `xsl:package` as a module root), `avt.go` (attribute value
  templates), `transform.go` (the engine), `functions.go` (XSLT functions),
  `streamability.go` (the instruction-level half of guaranteed-streamability
  analysis), `stream_exec.go`/`stream_acc.go`/`stream_group.go`/
  `stream_fork.go` (the bounded-memory streaming dispatcher — reuses the
  ordinary buffered `exec*` evaluator unmodified once a streamed record is
  materialized), `transform_to.go` (output-side bounded-memory serialization),
  `schema_property.go` (`Features.SchemaAware`, per stylesheet, off by
  default), `decl_import_schema.go` (`xsl:import-schema` → one per-stylesheet
  `*xsd.Schema`, the reference implementation of the injected-hook pattern
  above), `schema_validation.go` (runtime `[xsl:]validation`/`[xsl:]type`
  dispatch into `internal/xsd`'s `ValidateNode` bridge), `packages.go`/
  `pkgcheck.go` (`xsl:use-package`/`xsl:override`/`xsl:accept`; a package is
  a valid top-level module root just like `xsl:stylesheet`, but execution is
  currently **flattened** — cross-package component visibility/isolation is
  only partially enforced), `fn_transform.go` (F&O `fn:transform`, real
  XSLT-engine-backed, reachable both in-stylesheet and as a standalone
  XPath function via `internal/xpath/fn_transform.go`'s injected
  `TransformFunc` hook), `backcompat_property.go`
  (`Features.BackwardsCompatible`, per stylesheet, off by
  default, gating real XSLT 1.0-compatible processing per `[xsl:]version`
  scope — see `xpath.Context.BC10`).
- `internal/xsd` — **from-scratch XSD 1.0/1.1 validator**, 99.99% conformant
  both versions. Public API `engine.Validate(ValidateRequest) ValidateResult`.
  `Compile(schemas, baseDir, version)` → `Schema`; `(*Schema).Validate(instance)`.
  Reuses `xpath.CastTo`/`CompileRegex`/`AtomTypeByName`, `xmltree`, and the
  XPath engine (1.1 assertions/identity constraints). `bridge.go` is the XSLT
  schema-awareness bridge: component lookup by QName (`TypeByName`/
  `ElementDeclared`/`AttributeDeclared`/`DerivesFrom`) and `ValidateNode`, a
  validate-**in-place** entry point for a tree that already exists (vs.
  `Validate`, which parses from text) — kept behaviorally identical to
  `Validate` via a standing differential test over the whole XSD conformance
  corpus. Content-model matching: a memoized position-set NFA (`content.go`)
  for ≤600 children; `bigmatch.go`'s linear counting matcher above that.
  `messages.go`: a failed `xs:assert`/`xs:assertion` reports the
  `msg:message` (namespace `…/xsd/messages`, in its `xs:appinfo`) matching
  `ValidateRequest.Language`, plus `Rule`/`Path`/`Line`/`Col` — wording only,
  never the verdict.
  `collect.go`: `Validate` collects ALL errors (document order, `MaxErrors`)
  through a `collector` on the per-call Schema copy — nil on the `ValidateNode`
  path, which still stops at the first. Recording points are `soft()` calls
  (child errors, attributes, each assertion, content-model mismatch →
  `errModelMismatch`); the skip-wildcard excuse withdraws what a child already
  recorded (`uncollect`). After a first error an `ErrUnsupported`/panic ends
  the list instead of replacing the invalid verdict.
  `explain.go`: when the matcher says "no" and a collecting `Validate` is
  running, a small position automaton (unrolled occurrence bounds, shortest
  insertion path) replays the children and names each defect — `Kind`
  missing/too-many/unexpected/order, `Particle`, own `msg:message` (`on=`) from
  the element declaration or enclosing choice. Never decides validity; `xs:all`,
  open content and huge bounds fall back to the generic message (~11% of the
  suite's content-model errors). A child that would start one more iteration of
  a non-root group that has used up its occurrences (a choice's second
  alternative) is `too-many` of that GROUP (`exhaustedGroup`), with the group's
  messages. Missing required attributes take `on="missing"` messages from the
  `xs:attribute` (use, else declaration) and are all reported (`attributes.go`).
  **Gotcha**: `Compile` returns `ErrUnsupported` (not a verdict) for anything
  beyond what's implemented — keep that discipline so the pass rate stays honest.
  A compiled simple type never reaches itself: `visitST` rejects a named
  cycle through a restriction/list (and any cycle in 1.1), `compiler.open`
  catches an inline type naming the enclosing one, and `closeUnion` collapses
  the pure-union cycle XSD 1.0 tolerates (stE110) to the members that do not
  lead back — so walks over `members`/`base`/`item` need no visited set
  (issue #12 was a stack overflow in one of them).
- `desktop/main.go`, `desktop/xsltservice.go`, `desktop/projectservice.go` —
  Wails app + two bound services: `XsltService` (`RunTransform`/`RunValidate`/
  `RunXPath`, engine-only) and `ProjectService` (project registry/CRUD, file
  tree scan, file CRUD, manifest save, reveal-in-Finder). Both import the
  engine via the public `engine/` path.
- `desktop/updateservice.go` + `frontend/src/components/UpdateNotice.tsx` —
  the app's only network request: GitHub's latest-release API, at every start
  and every six hours, never cached across starts (a day-long cache hid a new
  release in its first hours), switchable (pref `go-xslt.checkUpdates`). A newer release opens a dialog
  with its notes once per start (`UpdateNotice.tsx`): GitHub's `body_html`
  (asked for with the html media type), cleaned once more by
  `sanitizeNotes`; links go to the browser; "Skip this version" is
  `go-xslt.skippedRelease`. The settings dialog (`SettingsView.tsx`, toolbar
  icon) holds the theme — `go-xslt.theme` = system|light|dark, resolved in
  `prefs.ts` to `data-theme` on <html>; `tokens.css` has a light palette under
  `:root[data-theme="light"]`, so colors belong in tokens, never literal —
  and the update-check switch, and the language (`go-xslt.language` =
  system|en|de|fr|it). UI text lives in `frontend/src/i18n/messages/<area>.ts`
  (settings, shell, runs, views, actions), declared with `defineMessages`:
  English is the reference and tsc fails when de/fr/it lack a key or have an
  extra one. Components use `useT(messages)`, non-React code
  `translate(messages, key, params)`; placeholders are `{name}`, one key per
  whole sentence. A new visible string goes into a messages file in all four
  languages, never as a literal. Engine diagnostics stay English; Validate
  passes the language on for `msg:message`. The build's version comes from
  `desktop/version.go` (`appVersion`, "dev" by default), set by the gitignored
  `version_stamp.go` the Makefile writes before every build (`stamp-version`).
  A transform run's optional-feature switches are saved in the run
  (`project.Run.SchemaAware/BackwardsCompatible/StrictStreaming`).
- `desktop/schemamodel.go` + `frontend/src/components/SchemaStructureView.tsx` —
  read-only **XSD structure view** (a Code | Structure toggle on `.xsd` file
  tabs): `XsltService.SchemaStructure` compiles the schema set through the
  public `xsd` package (imports/includes resolved against the file's dir) and
  returns a flat model — types listed ONCE and referenced by id, so a type used
  thousands of times stays small and the view expands it lazily; recursion is
  cut by the id on the path. Every element/group/any shows its `min..max`.
- `desktop/internal/project` — the Postman-like project model: a **project**
  is a real directory (new ones seeded under `~/Documents/go-xslt`, or any
  existing folder opened in place) holding arbitrary files/folders plus a
  `goxslt.project.json` manifest of **saved runs** (reusable named
  transform/validate/xpath configs — validate's `Schemas []string` is an
  *ordered multi-file set*). The file tree is never stored — `tree.go`
  rescans on demand. `fileops.go` is the security boundary: every path a
  service method touches is resolved and symlink-checked against the project
  root before any I/O (`resolve`/`ErrEscapesRoot`). `registry.go` tracks known
  projects in `os.UserConfigDir()/go-xslt/projects.json`.
- `desktop/frontend/src` — `App.tsx` is just `AppStateProvider` + `AppShell`.
  `state/store.tsx` (projects/tree/tabs/dialogs via context+reducer),
  `state/buffers.ts` and `state/results.ts` (file text and run results live
  *outside* the reducer, via `useSyncExternalStore`, so a keystroke or a
  running-transform tick doesn't re-render the tree/tabs/other editors),
  `state/projectActions.ts` (the only place that calls the Go services and
  keeps buffers/results/tabs/manifest in sync). `components/{Sidebar,
  ProjectSwitcher,FileTree,RunList,TabStrip,AppShell}.tsx` (the shell) and
  `components/{Transform,Validate,Xpath}RunView.tsx` + `FileEditorView.tsx`
  (what a tab shows), whose panes are **dockable** (`DockLayout.tsx`, on `dockview-react`: drag a
  pane's tab to dock it left/right/above/below another or stack it as a tab, like
  VS Code; the arrangement is kept in localStorage under `go-xslt.dock.<view>`,
  "Reset layout" restores the default `arrange*` function; a stored layout is
  only used if it holds exactly the current pane ids; the panes are plain React
  elements handed to the dock through a context, so they keep closing over the
  view's state), sharing `ResultParts.tsx`, `FilePickerField.tsx`/
  `SchemaListEditor.tsx`, and the `Modal`/`ContextMenuHost`/`Toaster` overlay
  trio (`window.prompt`/`confirm` are no-ops in the Wails WKWebView, so every
  rename/delete goes through these). The transform output pane's Format toggle
  pretty-prints XML/HTML (`formatXml`) and JSON (`formatJson`, display only:
  parse + re-stringify, falls back to the raw text when it is not valid JSON);
  `ReadOnlyViewer`/`Editor` take a `language` (xml|json). `lib/api.ts`
  re-exports the **generated** bindings types directly instead of hand-mirroring
  the Go structs.

## Commands

Library (repo root):
- `make test` / `go test ./...` — engine unit tests + conformance harnesses
  (which skip if the W3C suites are absent).
- Import it elsewhere: `go get github.com/tim-riep/go-xslt` then
  `engine.Transform(engine.Request{Stylesheet: …, Source: …})`.

Desktop GUI (**run from `desktop/`**):
- `cd desktop && make dev` (`wails3 dev`) — live dev. `wails3` lives in
  `$(go env GOPATH)/bin`.
- `make build` (host-native) / `build-win` (x64+ARM64) / `build-mac`
  (Apple Silicon+Intel) / `build-linux` (x64, via Docker — `make setup-docker`
  once first) / `build-all` (all 5) — outputs to `desktop/build/bin/`,
  arch-suffixed. The windows/mac/linux targets delegate to `wails3 task
  <os>:build ARCH=…`, not raw `go build`, so icon/manifest generation stays
  correct.
- `make bindings` (`wails3 generate bindings -ts -i`) — **rerun after changing
  any bound Go service signature or `engine`/`project` struct**.
- Frontend build: `cd desktop/frontend && npm run build` (tsc + vite). It is
  embedded via `//go:embed all:frontend/dist`, so build the frontend before
  `go build`.
- **CI/CD** (`.github/workflows/`): `ci.yml` runs library build/vet/test and
  `govulncheck` on push/PR to `main`. Both `go.mod` files keep `go 1.25.0`
  (the minimum for library users) plus a `toolchain` line, which is what
  CI and the release builds actually use: setup-go's `go-version-file`
  reads only the `go` line, so each job first extracts the `toolchain`
  version with `sed` and passes it as `go-version` (otherwise 1.25.0 is
  installed, and tools built outside the module — the wails3 CLI,
  govulncheck — are compiled with it and warn "file requires newer Go
  version go1.26"; the wails3 cache key carries the Go version). Keep it on
  the latest patch release — the published binaries were built with a
  year-old go1.25.0 until v0.3.5. `release.yml` triggers on any `v*` tag push — builds all
  5 desktop binaries (macOS arm64+amd64 and Windows amd64+arm64 natively on
  one `macos-latest` runner; Linux amd64 natively on `ubuntu-latest` with
  GTK/WebKitGTK installed, avoiding the `wails-cross` Docker image) and
  attaches them to a **draft** GitHub release named after the tag. The macOS
  apps are signed and notarized in CI (Developer ID; notarytool logs in with an
  App Store Connect API key — secrets `APPLE_API_KEY_P8`/`_KEY_ID`/`_ISSUER`). The Windows .exe files
  are signed afterwards on the maintainer's Mac with `scripts/sign-windows.sh
  <tag>` (Certum Open Source Code Signing, SimplySign cloud, through ssign
  0.1.8 — reviewed: talks only to Certum's cloudsign/time servers; the 6-digit
  code is typed per run, the TOTP seed never leaves the phone), which
  verifies the signatures, replaces the draft's .exe files and publishes the
  release. Nothing is visible to users (or the update check) before that. The
  `wails3` CLI version is pinned there to match `desktop/go.mod`'s
  `wails/v3` require line exactly.

## Conventions / gotchas

- **Built-in function arguments are checked centrally** (XPath 3.1 §3.1.5.2,
  issue #9): `dispatchFunc` looks the function up in the signature catalogue
  (`internal/xpath/fn_catalog.go`, F&O 3.1's own `$name as type` lines, one per
  arity; XSLT's `current`/`key`/`document`/… in `xsltSigSrc`,
  `internal/xslt/functions.go`) and `fn_argcheck.go` atomizes, casts
  `xs:untypedAtomic`, accepts numeric/anyURI promotion and matches the declared
  type BEFORE the implementation runs — XPTY0004 on a type or cardinality
  mismatch, XPTY0117 untypedAtomic→QName, the cast's FORG0001, FOTY0013 for a
  map/function. Static calls, `name#N`, `fn:function-lookup`, partial
  application and dynamic calls all pass through it. A matching argument is
  passed on untouched (no allocation; keep `argSpec.fast` that way — measure
  with `BenchmarkBuiltinCall`). A new function without a catalogue line fails
  `TestBuiltinCatalogComplete`; do not add ad-hoc argument-type checks to an
  implementation. Function-typed parameters are checked as `function(*)`;
  `Context.BC10` skips the check (the old lenient behaviour).
- **Optional features are per stylesheet** (`internal/xslt/features.go`,
  `engine.Features{SchemaAware, BackwardsCompatible, StrictStreaming}` on
  `Request`): `xslt.CompileWith(…, CompileOptions{Features})` stores them on
  the `Stylesheet` (`ss.feat`) and the compiler (`c.feat`); code holding only
  a stylesheet node uses `featuresAt(el)`, which looks the module's root up in
  `featureRegistry`. Every module root is registered as it is parsed
  (compileModule, loader, staticdecl). Both that registry and `schemaRegistry`
  are keyed by weak pointers with a cleanup (`weakStore`), so discarded
  stylesheets are collected (`TestRegistriesReleaseStylesheets`). The old
  setters (`SetSchemaAware`/`SetBackwardsCompatible`/`SetEnforceStreamability`/
  `SetStreamingClaim`) are now only the DEFAULTS for a compile given no
  Features — that is how the harness claims them, so its numbers are
  unchanged by construction. Never read a feature from a global again.
- **Local file reads are confined, per request** (`internal/fsaccess`,
  `engine.Security{AllowedDirs, AllowAllFiles}`, also on `ValidateRequest`
  and `xsd.Request`): the public facades build a `*fsaccess.Guard` rooted at
  BaseDir (`engine.FileGuard`; no BaseDir = no file reads) and thread it
  through EVERY read — `xslt.CompileFromFS` → `compiler.files`/
  `Stylesheet.files` (import/include, import-schema, use-when, parameter
  documents, fn:transform), `Entry.Files` → `fileResolver.files`,
  `xmltree.Parse…FS` (external DTD subsets/entities), `xsd.CompileFS` →
  `Schema.files` (xs:include/import/redefine/override, xsi:schemaLocation).
  Symlinks are resolved before the check. A NIL guard means no check — that
  is what the internal packages get when called directly, so the conformance
  harnesses and package tests are unaffected by construction. New file reads
  must go through the guard (`files.ReadFile`, never bare `os.ReadFile`);
  host-supplied paths (`PackageSource`, `SchemaSource`, `Entry.SourceSchemas`)
  are trusted configuration and stay unguarded. Tests:
  `engine/filesecurity_test.go`, each with an allowed control.
- **Network access is opt-in, per request** (`internal/netfetch`,
  `engine.Security{AllowNetwork, AllowedHosts}` on `Request`/
  `TransformRequest`/`XPathRequest`). Off, an http(s) URI is an unavailable
  resource and is never looked up on disk. On, the run's `Fetcher` caches
  every answer (F&O stability), checks redirects against the host list, caps
  size/time, refuses URL credentials, decodes text by the HTTP charset first
  (F&O "external encoding information"), and parses a remote document with
  an EMPTY base dir — every external DTD/entity read in `xmltree` is skipped
  then, so a remote DOCTYPE cannot read local files (tested with a positive
  control in `engine/network_test.go`). Not covered yet: `xsl:import`/
  `xsl:include`/`xsl:import-schema`/`xs:import` locations and static
  (use-when) evaluation stay local-only. The harnesses go online only with
  `XSLT30_NETWORK=1` / `QT3_NETWORK=1`; the floors are measured offline.
- `exsl:node-set` is deliberately NOT implemented: EXSLT is not a W3C spec,
  its definition is informal (behaviour for node-sets/numbers/booleans is
  unstated), and XSLT 3.0 does not require it — `docbook-001/002` (combined
  mode) fail on it and stay failing.

- **Nothing may overflow the Go stack** (a fatal error no recover() catches —
  one crafted input would kill a whole service). Recursion is bounded at
  every level: `xmltree.MaxDepth` (10,000 element levels, both readers),
  `maxParseDepth` (5,000 nested ExprSingle in the XPath parser),
  `maxCallDepth` (10,000 inline-function calls; the counter travels with the
  function value, `Context.calls`), and the XSLT engine's per-run `budget`
  (`Limits` — MaxSteps 20 M, MaxDepth 10,000, lower only), which nested
  `fn:transform` runs SHARE. All limit errors are XPDY0130 (not XTDE0040,
  which means "no such initial template"). `engine/limits_test.go`'s
  `TestNoStackOverflow` pins the crash inputs. A new recursive walk over
  input-controlled structure needs the same treatment.
  Entity expansion is bounded the same way: `expansionLimit` (8 × the
  input, at least 16 MiB) caps entity values (`expandEntityValues`), markup
  splicing (`spliceEntities`), references in the body (`entityRefVolume`)
  and, in `StreamReader`, the text the decoder produces (`countText`);
  parameter entities (`expandParamEntities`) too. Every check runs BEFORE
  the string is built (a check after building let 699 bytes allocate 5 GB —
  GHSA-6xxr-4cc9-4m89); `StreamReader` leaves values over 24 bytes to the
  full parser, since encoding/xml builds a whole text run before it can
  count. `TestEntityExpansionIsBounded` pins billion-laughs shapes,
  `TestEntityExpansionAllocationIsBounded` the bytes allocated.
- **Cancellation**: `TransformContext`/`TransformToContext`/
  `Stylesheet.TransformContext`/`EvalXPathContext`/`ValidateContext`. The
  run's `budget` holds the ctx (checked every 1024 steps) and hands XPath an
  `Interrupt` hook (set on the main eval context in `transform.go`); XPath
  polls it in for/some/every/`!`/predicate loops, range building and inline
  calls — the four explicit sub-context literals in `eval.go` copy it, so a
  NEW literal `&Context{…}` must copy `Interrupt` too. XSD polls per element
  (`validateAgainstType`) and leaves by `panic(canceled{…})`, so the
  collector never books a cancellation as a validation error.
  `Limits.MaxOutputBytes` is checked in the facade (`limitWriter` for
  TransformTo). Profiling (website/performance.md): on darwin, pprof books
  kernel time to whichever syscall a thread enters next, so its large
  `runtime.madvise` share (~35%) was mostly a misattribution — timed
  directly, madvise is ~13 ms a run. Profile with macOS `sample` (per-thread
  stacks) and decide by A/B wall/CPU time in alternating processes; GC
  (scanning pointer-heavy small objects) is now the largest real share.
- **Reused scratch contexts** (predicates in `applyPredicateOrdered`, the
  `xsl:key` walk, `matchingTemplates`' pool, `evalWith`'s `evalPool`) are
  overwritten per use. Any code that keeps a `*xpath.Context` beyond the
  evaluation (an inline function, function-lookup, a named function
  reference, partial application, function coercion) must call
  `ctx.capture()` — never set the flag directly: it also tells the host's
  VarResolver (`CaptureListener`, the XSLT `evalEnv`), since a context derived
  inside for/let/predicates still resolves through it. A reuser checks
  `Captured()`/`env.captured` and takes a fresh pair; `evalWith`'s scope
  snapshot is taken at that moment (`ContextCaptured`), not per evaluation. The parser carves nodes from slabs and collects children on a shared
  stack (`parseRaw`), so nothing may read `Children` of an open element while
  parsing.
- **Results must not depend on the platform.** The nightly job runs on
  linux/amd64, the maintainer's Mac is darwin/arm64, and two classes of
  difference have bitten: converting a NaN/huge float64 to int is
  implementation-defined (0 on arm64, MinInt64 on amd64 — `fnSubstring`
  clamps as float first), and `math.Exp` is assembly on amd64 whose result
  depends on CPU FMA support (`exactExp` in `internal/xpath/exp.go`, a copy of
  Go's portable code with products wrapped in `float64()` against fusing).
  Check a numeric change with a `GOARCH=amd64` build too (runs under
  Rosetta, which lacks FMA — the worst case).
- `cmd/go-xslt` is the CLI (`transform|validate|xpath|version`) in the
  LIBRARY module; it may read the directories of the files on its command
  line, more only via `-allow-*` flags. Flags are uniform across commands:
  `-s/--stylesheet`, `-x/--schema`, the document is always the last argument.
  `.github/workflows/conformance.yml` runs `scripts/conformance.sh --strict`
  nightly (fails on any number differing from the published one). `scripts/bench/main.go` benchmarks it
  against xsltproc/xmllint (`go run scripts/bench/main.go`).
- **Tests are deterministic — no verdict may depend on wall-clock time or on how
  busy the machine is.** No per-case timeouts in the harnesses (a case that
  cannot finish is stopped by the engine's counted budgets — instruction steps,
  recursion depth — and a true hang by `go test -timeout`, loudly); no heap
  sampling on a timer. The memory-bound tests read the LIVE heap (forced GC) at
  fixed points of the run via `stepProbe` (`internal/xslt/probe.go`: every N
  executed instructions), so a bounded run reads the same on a loaded CI runner
  and an idle laptop. Verified: three combined-conformance runs started at once
  under a load average of ~60 gave identical fail lists.

- The Windows build must be `CGO_ENABLED=0` with `-ldflags "-H=windowsgui"`
  (the Makefile does this). macOS build is native CGo (webkit).
- `react-resizable-panels` is pinned to **v2** (v4 renamed the API).
- XPath name tests with no prefix match the **no-namespace** (XPath 1.0 rule);
  prefixes resolve via the stylesheet element's in-scope namespaces.
- Template bodies must be compiled from `childNodesForBody(el)` (text + elements
  minus leading params), **not** `elementChildren` — text nodes matter.
- Value model (`xpath/value.go`): `Object` is an XDM value — `Sequence` of
  items (nodes, typed `*Atomic`, maps, arrays, functions); `NodeSet` is the
  node-only form. Plain Go `string`/`float64`/`bool` still occur as untyped
  results of fast paths and the 1.0 conversion functions — `Items()`
  normalizes everything, so new code should work on items.
- **Maps and arrays share structure** (`xpath/map.go`, `Array.Append` in
  `xpath/types.go`): a `*Map` is a persistent hash trie and `map:put` is
  `Map.With`, which freezes its source; `Map.Put` changes a map IN PLACE
  and is only for builders before the map is handed out (it edits only trie
  nodes that map made itself, so a frozen map's nodes never change). Keep
  that split — never `Put` into a map you did not just create. Arrays from
  `Append` share one backing array, so `Members()` is capped at its length
  and nothing may write into a member slice. Iterate with `Map.Range`
  (`Keys()` builds a new slice). A parsed `SeqType` is shared too
  (`declaredType` caches @as parses for every goroutine; xslt's `coerceAs`
  skips the context for types that need none): never modify one after
  parsing.
- **Never trust the locally embedded spec copy**
  (`test/conformance/xslt30-test/specs/xslt-lcwd30.xml` is a stale March-2015
  Working Draft, not the June-2017 REC) for a precise semantic question —
  fetch the live W3C REC text instead. This has mattered repeatedly.
- **Every failing conformance case is documented** in
  `docs/accepted-failures/{xslt30,qt3,xsd}.md` (status Accepted/Open plus
  the reason). `checkAcceptedFailures` (test/conformance/
  accepted_failures_test.go) fails a harness run on any failing case not
  named there in backticks — so a regression is loud, and a newly accepted
  failure needs a written reason. Remove an entry when its case passes.
- **Regression discipline for any change touching conformance**: diff fail
  lists by **name**, never by count — a count-only diff can hide a real
  regression a fix elsewhere silently introduces. `XSLT30_FAILS=1` /
  `QT3_FAILDUMP=1` + `grep -o '^FAIL .*'` (unanchored — `t.Log` output always
  carries a `file:line:` prefix that `grep '^FAIL'` alone will miss). Always
  re-check the 4 floors after any change: default XSLT30, QT3, XSD 1.0, XSD 1.1.
- **Parallel-agent campaigns** (this project's standard shape for large
  conformance pushes): isolated git worktrees per workstream (symlink the W3C
  suite clones in, since worktrees don't carry gitignored files), each agent
  independently gated (build/vet/test + by-name floor diff) before merging,
  merged serially with real `git merge`/`git merge-file`. The one recurring,
  costly failure mode: a **zero-git-conflict merge can still be semantically
  broken** — two agents independently adding a same-named local/field/struct
  member in non-overlapping line ranges compiles fine as a duplicate
  declaration error, or (worse) doesn't even fail to compile and just
  silently drops one side's behavior. Always re-verify claimed fixes
  independently (don't trust an agent's own report), and after a clean merge,
  grep for the shared mechanism's call sites before trusting it.

## Status

- ✅ **Core engine**: full XPath 3.1 (typed atomic model, all operators, the
  F&O 3.1 catalog, maps/arrays/HOF) and the XSLT 3.0 basic processor
  (instruction registry, import/include loader, modes/tunnel-params, the full
  instruction set including packages/`xsl:use-package`/`xsl:override`).
  Add an XPath function: `internal/xpath/FNAPI.md`. Add an XSLT instruction:
  `internal/xslt/INSTRAPI.md`.
- 🟢 **XSLT 3.0 conformance** (`test/conformance/xslt30_test.go`, `TestXSLT30`;
  `XSLT30_BYVER=1` for a per-version breakdown; `XSLT30_FULL=1` additionally
  claims streaming + schema-awareness + backwards-compatibility together —
  see below):
  - **Default (basic processor): 99.9% (8,635/8,647)**. **Combined
    (`XSLT30_FULL=1`): 99.8% (12,288/12,317)**.
  - `<test><package role="principal"></test>` catalog entries (347 cases)
    used to be hard-skipped unconditionally; the engine already accepted a
    top-level `xsl:package` as a module root and already fully implemented
    `xsl:use-package`/`xsl:override`/`xsl:accept` — only the harness's own
    recognition was missing. Per XSLT 3.0 §26's own Conformance clause,
    packages are **not** one of the six optional features (schema-awareness,
    serialization, backwards-compatibility, streaming, dynamic evaluation,
    XQuery invocation) — so unskipping them genuinely widens the **default**
    denominator too, not just the combined one.
  - `xsl:expose` is real (`internal/xslt/expose.go`): the §3.5.3.1 precedence
    (explicit name > declared `@visibility` > partial wildcard > `*` >
    private), XTSE0020/3010/3020/3022/3025, implicit modes, and it feeds
    `packageExports`. A used package's non-exported components (private, or
    lowered to hidden by `xsl:accept`) are now hidden from the using package's
    own code at the points a name is resolved (`hiddenAt`/`hiddenRef`:
    function calls, variable references, `xsl:call-template`,
    `xsl:use-attribute-sets`) — execution is still flattened, so this is a
    resolution-time check, not isolation. Also in `expose.go`/`pkgcheck.go`:
    XTSE3050 (redeclaring an accepted component outside `xsl:override`),
    XTSE3060 for template rules in final/private modes, XTSE3008
    (`xsl:use-package` reached through `xsl:import`), XTSE3087/XTTE0590/
    XTSE3088 for `xsl:global-context-item`, and `xsl:original` for
    functions (`originalFuncs`), global variables (`originalVars`) and
    attribute sets (an override REPLACES the set). Package-local declarations (`scope.go`): when more than one package
    takes part (`multiPkg`), keys, decimal formats, accumulators, namespace
    aliases, named `xsl:output` formats and character maps are filed per
    package, and only the principal package's UNNAMED `xsl:output` counts.
    Diamond graphs: a package that overrides (or whose used package declares)
    a global variable sees its own (`overrideVars`/`globalFromUsedPackage`);
    a used package's globals have no context item. A namespace that is the
    result of an `xsl:namespace-alias` is not removed by
    `exclude-result-prefixes`. `fn:function-lookup` inside a used package
    sees that package's own functions (`ResolveLookupFunction`), unlike a
    static `f#N` reference. XTSE3070 covers `as`, parameters (name/tunnel/
    required/type), context item and function `new-each-time`. Abstract components: a top-level package may
    contain none (XTSE3080 — declared abstract, or accepted as abstract and
    not overridden); an abstract component not accepted *as* abstract becomes
    hidden; evaluating an unoverridden abstract attribute set is XTDE3052; a
    used package's template is an entry point only if an `xsl:accept` said so.
    Accept precedence is explicit name > partial wildcard > `*`, last wins;
    `Q{}*` means no-namespace names. A global of a used package that fails to
    evaluate is only an error once read (lazy).
  - Genuine XSLT 1.0/pre-2.0 **backwards-compatible processing** (an
    `[xsl:]version` below 2.0 anywhere in scope — XSLT 3.0 §3.9.1) is real,
    not a flat XTDE0160 rejection: a claim switch
    (`Features.BackwardsCompatible`, `backcompat_property.go`,
    off by default, on under `XSLT30_FULL=1`) gates per-element-scoped 1.0
    comparison/arithmetic/conversion leniency (`xpath.Context.BC10`,
    threaded through every sub-context construction — a real, general bug
    this fix found and closed: it had been silently dropped in 4 places).
  - Reached through many iterative campaigns (parallel-agent worktree
    rounds, see the discipline above). Streaming, schema-awareness, and
    backwards-compatibility were all originally "rejected by design" (this
    file used to say so) — all three were built from scratch and are now
    real, load-bearing features (see their own entries below), not
    conformance-only paper capabilities.
  - **Unicode-version sets are run, with named exclusions**
    (`test/conformance/unicode_exclusions_test.go`): `regex-classes` (91 of
    120 run) and `unicode-90` (397 of 1,460 run) used to be skipped whole. Now
    only cases that cannot be judged are excluded, each with a reason:
    `unicode-version` (44 — the expected data is an old Unicode version's, and
    F&O 3.1 makes the version implementation-defined; the engine agrees with
    Unicode 15 code point for code point) and `suite-defect` (1,048 — the
    generated unicode-90 cases never pass their `charclass` parameter, so they
    test Ll; count cases with BMP-only totals; empty `<c>` comparisons;
    U+FFFE/FFFF as `c=""`). Running them found real gaps, all fixed: Unicode
    15 block names (`regex_blocks_ucd.go` from Blocks.txt 15.0; the XSD 1.0
    names stay as aliases), `{n,m}` above RE2's 1000 (falls back to the
    backtracking matcher), and three quadratic paths (regex recompiled per
    call — now cached; `x = $long` — hash index on the comparison node;
    `translate()` with a long map — indexed). unicode-90 is the slowest set
    (~4 min, ~3.7 GB: 50 MB data files); `XSLT30_U90=1` runs even the
    excluded cases. `test/conformance/unicode_categories_test.go` still checks
    every category escape against the suite's 9.0 data per code point. Other
    skips:
    `spec:XSLT20`-only cases, features not claimed in a given mode,
    `feat-not:*` cases that want a processor WITHOUT a feature we implement.
  - **Residual, default mode** (12 fails; not winnable without
    disproportionate effort, or genuinely impossible — do not re-attempt
    without new evidence): network-dependent assertions (`unparsed-text-2002/
    2003` — they read live w3.org pages; with `XSLT30_NETWORK=1` both pass,
    see the network entry under Conventions); XInclude (`base-uri-052`, unimplemented);
    per-package `xsl:strip-space` scoping (`collection-006`, `document-2401/
    2402` — needs a "current package" identity threaded through every
    `document()`/`collection()`/`fn:doc` call site down to the resolver, a
    large, high-blast-radius change for 3 tests, declined by the user);
    proven suite self-contradictions (`output-0130/0715`'s stale reference
    fixtures vs. a live check with an independent reference processor; `copy-3002`'s own catalog entry says
    "the test should fail"); `copy-1220/1221`'s ambient-namespace-on-copy gap
    (the only known fix is measured net-negative, repeatedly); and
    `override-as-005` (two PRIVATE attribute sets of one name in two
    packages: attribute-set references are resolved without their package,
    so scoping them means threading it through every `use-attribute-sets`
    site and the streamability analysis).
  - **Additional combined-mode-only residual** (~26 more): `catalog-005/005b`
    (needs a schema in scope the stylesheet never imports — a global
    schema cache, not what §3.16 provides); `square-array-201`
    (the reference results assume lazy evaluation of globals, this engine evaluates eagerly — both orderings
    are spec-permitted, cross-tree document order is implementation-defined);
    broad `xsl:source-document` static enforcement (measured net-negative
    every time it's been tried); a handful of streaming/backwards-compat
    edge cases individually root-caused and reported, not forced (e.g.
    `backwards-018` needs the same undecided XHTML meta-tag heuristic as
    `output-0130/0715`, not a real backwards-compat gap). Streamability now
    also reports (definite, so XTSE3430): an instruction processed in 1.0
    compatibility mode (§3.9.1 — roaming/free-ranging), and an xsl:for-each
    whose select WRITES a descendant step (`//a/b`) over a consuming body;
    the union-of-striding-paths crawling case stays silent (sx-union). A
    streamable initial mode gives global VARIABLES (not params) no context
    item (XPDY0002, streamable-116).
- 🟢 **XSLT 3.0 guaranteed streaming** — real bounded-memory execution, not
  conformance-only. Streaming-only conformance **99.6% (10,398/10,442)**.
  - **Architecture** ("bound the tree, don't abstract the node" — the reason
    every prior attempt at this was rejected: `xmltree.Node` is a concrete
    struct referenced ~1,000+ times, no abstraction layer to swap in): a real
    incremental XML reader (`internal/xmltree/stream.go`) builds ordinary
    `*Node`s one record at a time, retaining only the ancestor spine;
    `internal/xslt/stream_exec.go` (+ `stream_acc/group/fork.go`) dispatches
    the streamed case, handing each completed record to the **unmodified**
    ordinary buffered evaluator once materialized. A compile-time
    posture/sweep classifier (XSLT 3.0 §19/Appendix J,
    `internal/{xpath,xslt}/streamability.go`) decides whether a
    `streamable="yes"` construct is provably safe, raising `XTSE3430`-class
    diagnostics rather than ever silently mis-executing — deliberately
    fail-closed (over-conservative is merely incomplete; an unsound "yes" is
    the one unacceptable outcome).
  - **The memory guarantee is proven, not asserted**: input side flat ~2.0 MB
    peak heap across a 2–82 MB document range vs. ~630 MB buffered (313×);
    output side flat ~2.5 MB across 1.2–49 MB of output vs. ~355 MB
    string-returning (155×). `engine.TransformTo(io.Writer, Request)` is
    additive alongside `Transform`.
  - `system-property('xsl:supports-streaming')` reports a real, separate
    claim flag (`strmClaim`, default true) from the strict-enforcement flag
    (`strmEnforce`, only the conformance harness sets it) — a real caller
    gets genuine bounded-memory execution for a qualifying `streamable="yes"`
    construct regardless of whether strict enforcement is on.
  - **Residual** (~44 cases): mostly broad `xsl:source-document` static
    enforcement (see above, deliberately off), a handful of individually
    root-caused REC-non-derivable or suite-asks-more-than-the-minimum cases
    (`su-ascent-903`, `si-fork-953`, `sf-reverse-001` — the last cites a live
    upstream XPath bug number confirming the tension is unresolved
    upstream), and the `streaming-fallback` test set (its own description
    wants a processor configured to silently fall back from an unprovable
    `streamable="yes"`, the literal opposite of what strict enforcement
    configures — no single configuration can honestly pass both).
- 🟢 **XSLT 3.0 schema-awareness** — schema-only conformance **99.8%
  (8,446/8,461)** under the `XSLT30_FULL=1` claim (not the default, which
  stays untouched by construction).
  - **Scope, resolved by the spec itself**: XSLT 3.0 §26.2 defines
    "schema-aware processor" as an exact, closed, three-item list
    (`xsl:import-schema`, real `[xsl:]validation`/`[xsl:]type` dispatch,
    handling real type-annotated input) — a **runtime-only** schema-aware
    processor (dynamic XTTE-class errors, no compile-time type provability)
    is spec-complete. §26.1 explicitly leaves XPath 3.0's *static* typing
    feature's interaction with XSLT unspecified and required by no
    conformance level — `static_typing`/`staticTyping` stay out of scope
    permanently, the one deliberate boundary of this whole effort.
  - **Architecture**: `xmltree.Node.SchemaType` is opaque data (a
    `{Namespace, Local, Complex}` triple `internal/xpath` compares but never
    resolves) because of the `xmltree ← xpath ← xsd` import-cycle
    constraint; every derivation/name-resolution question routes through the
    injected-hook seam (`xpath.SchemaTypeResolver`/`SchemaNameLookup`/…,
    implemented by `internal/xslt/decl_import_schema.go`'s `schemaAdapter`).
  - **The claim** (`Features.SchemaAware`, default false)
    exists because 31 currently-passing conformance cases explicitly require
    a **non**-schema-aware processor (`TestXSLT30SchemaUnawareCases` pins
    all 31 as a canary, checked in both claim states after any change here).
  - **Residual** (~15 cases): `catalog-005/005b` (see above); union-type
    atomization and casting-to-a-list-type both need the same
    compute-on-demand reframe list-type atomization already got (a
    documented once-thought architectural wall that turned out not to be
    one — the typed value doesn't need to be *stored*, only computed when
    asked for); a handful of `XTTE1545`/`1555`/AVT-schema-awareness edge cases.
- 🟢 **QT3/FOTS (XPath+XQuery) conformance** (`test/conformance/qt3_test.go`,
  `TestQT3`): **99.7% (22,419/22,478 applicable), 59 fails**.
  - **Harness philosophy**: this engine is a real XP31 processor (also
    embedding a real XT30 one), not an XQuery processor — the **only**
    category-level exclusion is a spec dependency this declared identity
    doesn't satisfy (`specApplies`: family+version+open-ended("+") match
    against the engine's actual identity, so an exact-older-version-only
    dependency whose tested behavior 3.0/3.1 changed correctly falls out of
    scope — e.g. `fn:tokenize`/`string-join`/`round` gaining new-arity
    overloads, `[true()]` becoming a legal array constructor). Every other
    dependency (schema, feature, xml/xsd-version, …) is genuinely attempted
    and scored honestly — this replaced an earlier, much broader
    pre-emptive-skip design that was quietly inflating the denominator;
    removing it exposed real capability that already worked (304 of 542
    newly-run cases already passed) alongside a real, now-visible residual.
  - **No harness-side skips left**: `assert-xml` (parsed-tree comparison,
    prefixes and whitespace significant, `@file`), `serialization-matches`,
    `assert-serialization-error` and `assert-permutation` are checked; a
    malformed environment source is left to the engine (FODC0002); an
    assertion the engine cannot evaluate is a fail; assertions run under the
    case's `xml-version`/`xsd-version`. Only `specApplies` and two
    unsatisfiable dependencies skip. Running them found and fixed:
    `fn:serialize` spaced adjacent nodes (SER 3.1 §2: only atomics); an
    element serialized out of its tree dropped inherited namespaces (§5);
    `analyze-string` nested groups by position, not by the pattern's
    parentheses (`asGroupParents`); `json-to-xml` lost `key=""`; a leading
    BOM in `parse-json`/`json-to-xml`; `xml:id` not ID-normalized at tree
    construction (XDM §6.3.3, also DTD ID/IDREF(S)); ranges clamped integers
    above int64 (`to` and its comparison fast path are now `big.Int`-exact;
    an over-long range is XPDY0130); the `xml` prefix unbound without a
    resolver.
  - `fn:transform` (F&O 3.1 §16.3.2) is real, standalone-callable, and
    XSLT-engine-backed (`internal/xpath/fn_transform.go`'s injected
    `TransformFunc` hook into `internal/xslt/fn_transform.go`), covering
    essentially the full option table (stylesheet-location/node/text,
    all four param kinds, initial-template/mode/function/match-selection,
    base-output-uri, delivery-format, serialization-params, the
    FOXT0001–FOXT0004 error family).
  - German month/weekday names for `format-date`/`format-time`/
    `format-dateTime` (`$language="de"`) are real, built from two small data
    tables — cheaper than expected since the existing width/case-modifier
    machinery needed no changes.
  - `fn:default-language()` honors a real host-configured default
    (`Context.DefaultLanguage`, threaded from the harness's own
    `dependency type="default-language"`) instead of hardcoding "en";
    `fn:local-name-from-QName`/`namespace-uri-from-QName` enforce the
    `xs:QName?` argument's singleton cardinality (XPTY0004 on a multi-node
    selection) like every other optional-singleton argument already does;
    a bounded-repeat regex quantifier too large for RE2 to compile
    (`a{2147483647}`) is safely clamped to `len(input)+1` before compiling
    — provably equivalent, not a behavior change, and specifically
    engineered to introduce no path that could allocate proportional to
    the raw count; the `m` flag's exact F&O 3.1 §5.6.2 carve-out (neither
    `^` nor `$` may match right after a string's own trailing newline,
    unlike Go's native `(?m)`) is implemented.
  - Collation: `caseFirst=upper`, `caseLevel=yes`, `backwards=yes` (and
    fr-CA's default backwards) are real, layered over x/text's collators in
    `collLayered` (x/text cannot express them); `reorder=` is not applied and
    is FOCH0002 under `fallback=no`; a `numeric=yes` substring match may not
    cut a digit run (`splitsNumber`). `format-*` `$place` honors Olson
    `Region/City` names via the embedded `time/tzdata` (DST and `[ZN]`
    abbreviations). `fn:analyze-string`'s result elements/`@nr` carry their
    schema type annotations. The QT3 harness honors an environment's
    `<collation default="true">`. German, French and Italian integer spellout
    (cardinal + ordinal, `format-integer` `Ww`/`o(...)` and the same tokens in
    `format-date*` for `de`) is native, in `internal/xpath/words_lang.go`
    (CLDR spellings, < 10^12; any other language stays English).
  - **Permanent residual, do not re-attempt**: XQuery module loading
    (`fn-load-xquery-module-*`, no XQuery grammar exists at all); UCA
    `reorder=` codes (documented in `internal/xpath/UNSUPPORTED.md`);
    a live network fetch
    (`fn-unparsed-text-054a`: timeanddate.com now answers every client with
    403, so it fails even under `QT3_NETWORK=1`); `fn-transform-901/902/err-14` (each wants
    `fn:transform` to be *unavailable* — a genuine implementation
    necessarily contradicts them, the same shape as streaming-fallback
    above); ~20 cases that genuinely need XPath 3.0's full optional Static
    Typing Feature (static type inference over if/then/else branch unions,
    inline-function bodies, HOF signatures) — confirmed individually by
    reading each case's own `staticTyping` dependency, not assumed; a
    disproportionate feature for ~20 cases. The narrow, unconditional part
    of Static Typing this engine DOES implement (provably-empty axis steps,
    `ST-Axes*`) is gated behind an explicit `staticTyping` dependency via
    `xpath.ParseStaticTyping` — ordinary `Parse`/XSLT stay untouched, since
    the identical shape legitimately evaluates to an empty sequence outside
    that opt-in feature.
- 🟡 **XSD 1.0 / 1.1 validation** (`internal/xsd`, `test/conformance/xsd_test.go`
  `TestXSD` vs the W3C `xsdtests` suite). **1.0: 99.99% (39,396/39,401). 1.1:
  99.99% (41,549/41,553)** — `unsupported` is zero in both versions, so
  of-ran and of-applicable coincide; the 9 remaining fails across both
  versions are documented suite self-contradictions (disputed/`queried`
  status, TSTF-implementation-determined cases), not engine gaps.
  An EMPTY schema set compiles (built-ins only; `internal/xsd` `Compile(nil,
  …)` — the public model API `xsd.Compile` still refuses it): the instance is
  then governed by its own `xsi:schemaLocation` hints and `xsi:type`, which
  is how the 55 msMeta groups without a schema test run. XSD 1.0 only:
  `checkLateHints` (§4.3.2) — a NEW hint for a namespace already seen is an
  error; repeating an earlier (namespace, location) pair is not (addB163).
  Full feature set: facets, complex content models (groups/wildcards/
  substitution groups/extension/restriction), identity constraints,
  XSD 1.1 assertions + conditional type assignment, `xs:override`,
  `xs:redefine`, `xs:openContent`, multi-document import/include, a
  from-scratch XSD-regex grammar validator (also gates XPath's own regex
  compilation — see the QT3 entry), and the full "schema for schemas"
  structural self-validation. Run `XSD_PROGRESS=1 <bin> -test.run=TestXSD
  -test.v` (`XSD_ONLY`/`XSD_VERSION`/`XSD_SEQ` knobs) for live progress.
- **Local-only test material** (gitignored, never commit): any local-only,
  non-redistributable third-party test/corpus material a differential-testing
  gate needs lives as one unit under `local/` — see the README there, if one
  exists, for what's actually present and how to set it up; nothing under it
  is ever committed, so it's absent in a fresh clone. A differential-gate Go
  test file that depends on such material stays in whatever package it must
  live in to compile, but reads the material through `local/` and is itself
  gitignored too. Plus the W3C suite clones (`qt3tests/`, `xslt30-test/`,
  `xsdtests/`) under `test/conformance/`. All conformance tests skip cleanly
  when their material is absent — a fresh public clone always builds and
  tests green. The public repo carries no third-party licensed material.
