# perfab — global A/B performance check

`perfab` answers one question: is engine build B a global improvement over
build A? It runs every workload under `workloads/` with both builds,
alternating, and reports the median wall time, the peak RSS and the
allocation totals of each, with a verdict.

```bash
go run ./scripts/perfab -a origin/main -b .            # base vs. this checkout
go run ./scripts/perfab -a v0.4.0 -b perf-7 -runs 9    # two revisions
go run ./scripts/perfab -a-bin ./runner-old -b-bin ./runner-new
go run ./scripts/perfab -a origin/main -b . -only 'sort|keys' -runs 9
go run ./scripts/perfab -a origin/main -b . -bench     # plus the Go benchmarks
```

Run it from inside the module. `-a`/`-b` take a git revision or a directory
of this module. The measured process is `runner/main.go`, a small program
that drives one workload through the public `engine` package the way the CLI
does (`TransformTo`, `Validate`, `Compile`, `Transform`) and prints its
allocation totals; the harness builds it into each revision's source tree
through a build overlay, so a revision that predates the harness can be
measured too. `-a-bin`/`-b-bin` take prebuilt runner binaries instead.

The inputs are generated once (deterministically, from
`scripts/internal/gendata`) into the cache directory
(`$XDG_CACHE_HOME/go-xslt-perfab`, or `-cache`): a 200,000-record document
(37 MB), a 20,000-record cut, a 2,000-record cut, a 270-record document
(50 KB), the 20,000 records as JSON, and a generated 2,000-template
stylesheet. The compile workload uses the DocBook XHTML stylesheets when the
W3C suite is checked out under `test/conformance/`, else the generated
stylesheet; the output says which.

## Workloads

Each file under `workloads/` starts with a comment saying what it stresses.
The set covers the engine broadly, not only its fast paths: the six XSLT 1.0
workloads of `scripts/bench` (identity, templates, sort, group, xpath,
validate), the XSLT 3.0 ones (group-by with `xsl:map` and JSON output, typed
functions with `format-date` and `tokenize`, `xsl:iterate` with a typed map
and with `array:append`), `xsl:key` lookups, regular expressions, string
functions, number formatting, `xsl:number` with `position()`/`last()`, deep
named-template recursion, the HTML output method, a JSON round trip,
streaming (`xsl:source-document streamable="yes"`, whose peak RSS must stay
small), XSD 1.1 with assertions and identity constraints, compile-only, and
200 transforms of the small document in one process (compiling every time,
and compiled once).

## Reading the output

One table row per workload: the medians of A and B, and B/A, for the wall
time of the whole process, its CPU time (user + system), the peak RSS
(`rusage`), the bytes allocated and the number of allocations during the
work (from `runtime.MemStats`). The runs alternate A, B, B, A, A, B, … so a
drift of the machine hits both alike.

The verdict is PASS only if

- no workload's median time is above A's by more than `-tolerance`
  (default 2 %) — the wall time by default, the CPU time with
  `-metric cpu`, which is the steadier of the two on a shared or virtual
  machine (on a 4-core VM, identical builds differed by up to 9 % in wall
  time and 2–3 % in CPU time over 5 runs),
- no workload's peak RSS is above A's by more than `-rss-tolerance` (3 %),
- no workload allocates more bytes than under A by more than
  `-alloc-tolerance` (3 %), and
- every run's output is byte-identical to the first run's under A.

`-stat min` judges the fastest run of each side instead of the median:
every workload is deterministic, so noise from other tenants of the machine
only ever adds time, and the minimum is the least disturbed run.
`-gomaxprocs 1` runs the measured processes on one core, which takes the
parallel garbage collector's scheduling out of the comparison (the absolute
times are then higher).

A FAIL names each offender. A 1–3 % loss on one workload is often noise on a
busy machine: re-run the offenders with `-only` and `-runs 9` before
believing either way, and run nothing else meanwhile. Allocation totals are
nearly deterministic, so a change there is real.

`-bench` additionally runs `go test -bench . -benchmem` for the engine
packages in both trees, alternating, and tabulates the medians of ns/op,
B/op and allocs/op with the same tolerance.
