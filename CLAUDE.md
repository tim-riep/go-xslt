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
- `internal/engine` — **stable internal facade**. `Transform(Request) Result`.
  Panic-safe. The `engine/` re-export, GUI, and tests depend only on this;
  rewrite anything behind it freely.
- `internal/xmltree` — own XML node tree (parser on `encoding/xml`), document
  order, line/col, and a serializer for xml/html/text (`serialize.go`, plus an
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
  `internal/xpath/FNAPI.md`), `streamability.go` (posture/sweep
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
  `schema_property.go` (`SetSchemaAware`/`schemaAwareRun` claim switch, off by
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
  (`SetBackwardsCompatible`/`backwardsCompatRun` claim switch, off by
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
  **Gotcha**: `Compile` returns `ErrUnsupported` (not a verdict) for anything
  beyond what's implemented — keep that discipline so the pass rate stays honest.
- `desktop/main.go`, `desktop/xsltservice.go`, `desktop/projectservice.go` —
  Wails app + two bound services: `XsltService` (`RunTransform`/`RunValidate`/
  `RunXPath`, engine-only) and `ProjectService` (project registry/CRUD, file
  tree scan, file CRUD, manifest save, reveal-in-Finder). Both import the
  engine via the public `engine/` path.
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
  (what a tab shows), sharing `ResultParts.tsx`, `FilePickerField.tsx`/
  `SchemaListEditor.tsx`, and the `Modal`/`ContextMenuHost`/`Toaster` overlay
  trio (`window.prompt`/`confirm` are no-ops in the Wails WKWebView, so every
  rename/delete goes through these). `lib/api.ts` re-exports the **generated**
  bindings types directly instead of hand-mirroring the Go structs.

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
- **CI/CD** (`.github/workflows/`): `ci.yml` runs library build/vet/test on
  push/PR to `main`. `release.yml` triggers on any `v*` tag push — builds all
  5 desktop binaries (macOS arm64+amd64 and Windows amd64+arm64 natively on
  one `macos-latest` runner; Linux amd64 natively on `ubuntu-latest` with
  GTK/WebKitGTK installed, avoiding the `wails-cross` Docker image) and
  publishes them as assets on a GitHub release named after the tag. The
  `wails3` CLI version is pinned there to match `desktop/go.mod`'s
  `wails/v3` require line exactly.

## Conventions / gotchas

- The Windows build must be `CGO_ENABLED=0` with `-ldflags "-H=windowsgui"`
  (the Makefile does this). macOS build is native CGo (webkit).
- `react-resizable-panels` is pinned to **v2** (v4 renamed the API).
- XPath name tests with no prefix match the **no-namespace** (XPath 1.0 rule);
  prefixes resolve via the stylesheet element's in-scope namespaces.
- Template bodies must be compiled from `childNodesForBody(el)` (text + elements
  minus leading params), **not** `elementChildren` — text nodes matter.
- Engine value model is currently XPath-1.0-shaped (NodeSet/string/float64/bool)
  in `xpath/value.go`; XSLT 3.0 features (maps/arrays/HOF) extend it via
  `Sequence`/`Item`/`RealItem` rather than replacing it.
- **Never trust the locally embedded spec copy**
  (`test/conformance/xslt30-test/specs/xslt-lcwd30.xml` is a stale March-2015
  Working Draft, not the June-2017 REC) for a precise semantic question —
  fetch the live W3C REC text instead. This has mattered repeatedly.
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
  - **Default (basic processor): 98.8% (8,037/8,138)**. **Combined
    (`XSLT30_FULL=1`): 98.9% (11,681/11,808)**.
  - `<test><package role="principal"></test>` catalog entries (347 cases)
    used to be hard-skipped unconditionally; the engine already accepted a
    top-level `xsl:package` as a module root and already fully implemented
    `xsl:use-package`/`xsl:override`/`xsl:accept` — only the harness's own
    recognition was missing. Per XSLT 3.0 §26's own Conformance clause,
    packages are **not** one of the six optional features (schema-awareness,
    serialization, backwards-compatibility, streaming, dynamic evaluation,
    XQuery invocation) — so unskipping them genuinely widens the **default**
    denominator too, not just the combined one; 8,037 of the 8,138 now pass.
  - Genuine XSLT 1.0/pre-2.0 **backwards-compatible processing** (an
    `[xsl:]version` below 2.0 anywhere in scope — XSLT 3.0 §3.9.1) is real,
    not a flat XTDE0160 rejection: a claim switch
    (`SetBackwardsCompatible`/`backwardsCompatRun`, `backcompat_property.go`,
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
  - **Permanent residual, default mode** (~101 fails; not winnable without
    disproportionate effort, or genuinely impossible — do not re-attempt
    without new evidence): the original 11 — network-dependent assertions
    (`unparsed-text-2002/2003` — proven, not just suspected: the live page
    content changed since the test was written); XInclude (`base-uri-052`,
    unimplemented); per-package `xsl:strip-space` scoping (`collection-006`,
    `document-2401/2402` — needs a "current package" identity threaded
    through every `document()`/`collection()`/`fn:doc` call site down to the
    resolver, a large, high-blast-radius change for 3 tests, declined by the
    user); proven suite self-contradictions (`output-0130/0715`'s stale
    reference fixtures vs. a live SaxonJS check; `copy-3002`'s own catalog
    entry says "the test should fail"); `copy-1220/1221`'s
    ambient-namespace-on-copy gap (the only known fix is measured
    net-negative, repeatedly) — plus ~90 new ones from the package-entry
    unskip, all genuine gaps in the current **flattened** package execution
    model (packages compile and run, but cross-package isolation is only
    partial): `xsl:expose` (unimplemented), private-component visibility not
    hidden across a package boundary, decimal-formats/keys/namespace-aliases/
    character-maps/output scoped per-package rather than merged
    stylesheet-wide, XTSE3008 (`xsl:import` splicing `xsl:use-package`
    definitions across a module boundary), global-context-item package
    scoping, diamond package-version-range resolution.
  - **Additional combined-mode-only residual** (~26 more): `catalog-005/005b`
    (needs a schema in scope the stylesheet never imports — Saxon's own
    global-cache behavior, not what §3.16 provides); `square-array-201`
    (Saxon evaluates globals lazily, this engine eagerly — both orderings
    are spec-permitted, cross-tree document order is implementation-defined);
    broad `xsl:source-document` static enforcement (measured net-negative
    every time it's been tried); a handful of streaming/backwards-compat
    edge cases individually root-caused and reported, not forced (e.g.
    `streamable-141` needs the streamability classifier to know "1.0
    compat mode is roaming and free-ranging", correctly left to that
    separately-owned, historically sensitive area rather than a one-test
    patch; `backwards-018` needs the same undecided XHTML meta-tag
    heuristic as `output-0130/0715`, not a real backwards-compat gap).
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
    root-caused REC-non-derivable or Saxon-exceeds-the-minimum cases
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
  - **The claim switch** (`SetSchemaAware`/`schemaAwareRun`, default false)
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
  `TestQT3`): **99.6% (22,066/22,153 applicable), 87 fails**.
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
  - **Permanent residual, do not re-attempt**: XQuery module loading
    (`fn-load-xquery-module-*`, no XQuery grammar exists at all); deep UCA
    tailoring (case-level, reorder codes, secondary-strength diacritics —
    documented in `internal/xpath/UNSUPPORTED.md`); ordinal-word spellout
    (French/Italian, and German beyond the month/day names above) + an
    IANA timezone-abbreviation database; a live network fetch
    (`fn-unparsed-text-054a`); `fn-transform-901/902/err-14` (each wants
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
  `TestXSD` vs the W3C `xsdtests` suite). **1.0: 99.99% (39,346/39,351). 1.1:
  99.99% (41,499/41,503)** — `unsupported` is zero in both versions, so
  of-ran and of-applicable coincide; the 9 remaining fails across both
  versions are documented suite self-contradictions (disputed/`queried`
  status, TSTF-implementation-determined cases), not engine gaps.
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
