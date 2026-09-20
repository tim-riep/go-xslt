# W3C conformance harnesses

This package runs our engine against the official **W3C test suites** and checks
our output against the suites' **own expected-output files** — no external
reference processor required. Both harnesses **skip cleanly** if their suite
clone is absent, so a fresh checkout builds and tests green.

- `qt3_test.go` (`TestQT3`) — the XPath/XQuery FOTS suite, run against our XPath
  3.1 engine directly.
- `xslt30_test.go` (`TestXSLT30`) — the XSLT 3.0 suite, run against our full
  stylesheet engine.

Compile a binary for live progress (plain `go test` buffers all output):

```
go test -c -o /tmp/conf.bin ./test/conformance/
```

## QT3 / FOTS XPath conformance (`qt3_test.go`)

`TestQT3` is the XPath/XQuery conformance harness. Clone the suite (the test
skips if absent):

```
git clone --depth 1 https://github.com/w3c/qt3tests.git test/conformance/qt3tests
go test ./test/conformance/ -run TestQT3 -v
```

It parses the FOTS catalog + test-sets and runs every case whose spec
dependency this engine's declared identity (a real XPath 3.1 processor,
`specApplies` in `qt3_test.go`) actually satisfies — that is the *only*
category-level exclusion; everything else (schema-awareness, features,
xml/xsd-version, …) is genuinely attempted rather than pre-emptively skipped,
so a real gap shows up as a fail, not a silently-inflated pass rate. Each case
runs under a panic guard; pathological stress ranges (huge numeric ranges) are
pre-skipped. Test-sets run in parallel (per-worker doc cache).

`QT3_ONLY=<set-substring>` runs and dumps failures for one test-set (triage).
`QT3_PROGRESS=1` prints live per-set progress. `QT3_SEQ=1` forces sequential
(non-parallel) test-set execution. `QT3_FAILDUMP=1` (with
`grep -o 'FAIL .*'`, unanchored — the log line always carries a
`file:line:` prefix) dumps every fail name for a by-name regression diff.
Results are summarised in `tools/qt3_results.txt` and `tools/qt3_byset.csv`.

**Current: ~99.6% pass** of the applicable cases. Deliberately-unsupported
clusters (regex back-refs beyond RE2's reach, deep UCA tailoring, ordinal-word
locale spellout, an IANA timezone-abbreviation database, XQuery module
loading, XPath 3.0's full optional Static Typing Feature) are documented in
`internal/xpath/UNSUPPORTED.md` and in the project root `CLAUDE.md`.

## XSLT 3.0 conformance (`xslt30_test.go`)

`TestXSLT30` runs whole stylesheets against source documents and checks the
serialized output against the suite's `<assert-xml>` / `<assert-serialization>` /
`serialization-matches` / `error` assertions. Clone the suite (the test skips if
absent):

```
git clone --depth 1 https://github.com/w3c/xslt30-test.git test/conformance/xslt30-test
go test ./test/conformance/ -run TestXSLT30 -v
```

It reuses the shared FOTS catalog model from `qt3_test.go` and adds XSLT-specific
parsing (`<stylesheet>`/`<package>`, `<initial-template>`, `<initial-mode>`,
`<initial-function>`, `<param>`, `<context-item>`, `<output>`). The default run
is the basic XSLT 3.0 processor; set `XSLT30_FULL=1` to additionally claim
streaming, schema-awareness, and XSLT 1.0 backwards-compatible processing
together (a real embedding caller does not pick one of these, it opts into all
of them at once — see the claim switches in `internal/xslt/*_property.go`).
`XSLT30_BYVER=1` gives a per-min-spec-version breakdown.

`XSLT30_ONLY=<set-substring>` triages one test-set; `XSLT30_PROGRESS=1` prints
live progress; `XSLT30_SEQ=1` forces sequential (non-parallel) execution;
`XSLT30_FAILS=1` dumps every fail name (same unanchored-grep caveat as
`QT3_FAILDUMP` above) for a by-name regression diff; `XSLT30_ERRMSG=1`
appends the actual error message to an `errored` failure reason;
`XSLT30_DIFF=1` reports which branch of an `all-of`/`any-of` assertion
actually failed instead of just "all-of"/"any-of"; `XSLT30_PANICDUMP=1`
prints a stack trace for a case that panics; `XSLT30_STRICTCODE=1`
additionally requires an expected-error assertion's catalog `@code` to
match, not just that some error was raised. Results are summarised in
`tools/xslt30_results.txt` and `tools/xslt30_byset.csv`.

**Current: ~98.8% pass (default), ~98.9% (`XSLT30_FULL=1`)** of the applicable
cases. See the project root `CLAUDE.md` for the current permanent residual and
why each item is out of scope.

## Why differential/suite testing matters

Hand-written tests only cover what we think to test — e.g. all explicit XPath
axes (`ancestor::`, `parent::`) were once broken while 117 unit tests passed,
because none used them. Running the official suites exercises code paths we would
never enumerate by hand.
