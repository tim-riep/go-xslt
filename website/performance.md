# Performance

go-xslt is compared here with `xsltproc` and `xmllint`, the command-line tools
of libxslt and libxml2, the C libraries most XML tooling is built on. Both
run the same XSLT 1.0 stylesheets on the same document, so they do the same
work; each run is timed as a whole process (start, parse, transform,
serialize).

Apple M4, macOS, libxml2 2.9.13 / libxslt 1.1.35; input 200,000 records,
37 MB; median of 5 runs.

| Workload | go-xslt | libxslt / libxml2 | ratio | peak memory go-xslt | peak memory libxml2 |
|---|---|---|---|---|---|
| copy the whole document | 0.43 s | 0.69 s | 0.62× | 1,106 MB | 1,067 MB |
| recursive apply-templates, rebuilding every node | 0.84 s | 1.23 s | 0.68× | 1,134 MB | 1,074 MB |
| sort all records by two keys, text output | 0.54 s | 18.66 s * | — | 761 MB | 587 MB |
| Muenchian grouping with `xsl:key` | 0.51 s | 0.87 s | 0.59× | 784 MB | 538 MB |
| predicates, string and number functions | 0.44 s | 0.83 s | 0.53× | 712 MB | 590 MB |
| XSD 1.0 validation | 0.42 s | 0.51 s | 0.81× | 593 MB | 539 MB |

\* Not a sorting measurement. Almost all of libxslt's time here goes into
building the 5.6 MB text output: the libxslt 1.1.35 that macOS ships appends
each piece to one text node and measures that node's whole length again
every time, which grows quadratically. The same stylesheet without
`xsl:sort` takes it just as long; the sorting itself costs libxslt about
1.5 s. Current libxslt releases avoid this, so the row is left out of the
comparison.

The machine was not idle during this run, which slows both tools alike; the
ratios are what carries over.

## Reading the numbers

- go-xslt is faster than the C libraries on every workload both can run,
  validation included. It needs somewhat more memory for the document tree.
- The comparison is limited to what both can do. go-xslt also implements
  XSLT 3.0, XPath 3.1 and XSD 1.1, which libxslt and libxml2 do not.
- For documents too large for memory, XSLT 3.0
  [streaming](/guide/streaming) processes them in a few megabytes, whatever
  their size.
- A program that runs the same stylesheet many times should
  [compile it once](/guide/compiled-stylesheets); the numbers above include
  compiling the stylesheet on every run.

## Where the time goes

Six rounds of profiling and measurement brought go-xslt from 1.5–4.5 times
libxslt's time to where it is now:

- reusing evaluation contexts (per predicate, key walk, template match and
  XPath evaluation) instead of allocating one per node, carving parsed
  nodes from blocks, and matching XSD content models without per-element
  maps;
- an own XML tokenizer in place of `encoding/xml` that hands out pieces of
  the document instead of copies — it accepts and rejects exactly what
  `encoding/xml` did, which differential fuzzing and the W3C XML
  Conformance Test Suite check — about 260 MB less per 37 MB document;
- skipping the DTD passes for documents without a DOCTYPE, fewer
  allocations per template call, and serializing without building strings;
- matching simple patterns such as `node()` and `@*` without a full
  evaluation context, keeping `defer` out of the hottest functions, and a
  faster sort where the result is provably the same;
- normalizing whitespace in attribute values only where an attribute value
  actually contains a tab or line break, writing parsed nodes straight into
  their blocks, numbering document order while parsing, and building
  copies from per-run blocks;
- for XSLT 3.0: `xsl:for-each-group group-by` files its groups by key
  instead of comparing every item with every group so far (5× faster with
  5,000 groups); maps are persistent hash tries, so `map:put` no longer
  copies the whole map, and `array:append` appends in place — collecting one
  entry per record in `xsl:iterate` was quadratic and is now linear (5,000
  keys over 20,000 records: 53 s before, 0.4 s now); a typed parameter is
  bound without parsing its `as` type again on every call, and built-in
  functions convert their arguments with fewer allocations (12 % less time
  for a transform made of function calls).

- a seventh round, with a committed A/B harness (`scripts/perfab`) that
  runs 23 workloads on two builds — timed, and counted in instructions
  under cachegrind where the machine is too noisy to time — and passes a
  change only if no workload gets slower, bigger or different: `xsl:number` builds one evaluation
  context per numbering instead of one per candidate node (numbering 2,000
  records with `count="order|item"`: 3.3 s and 3.5 GB allocated before,
  0.55 s and 8 MB now); an `array(xs:string)` parameter accumulated with
  `array:append` in `xsl:iterate` was re-checked member by member on every
  step — the shared array now remembers the verified prefix, so 20,000
  records take 0.2 s instead of 8.5 s; and matching a single item against a
  declared type no longer boxes it into a slice (one allocation less per
  typed parameter, variable or result binding).

- the eighth round, on the validator, the readers and the string functions,
  judged by instructions executed under cachegrind and by allocations, since
  the machine could not time the change (two runs of the same build differed
  by up to 15 %): an XSD 1.1 assertion is evaluated against a detached copy
  of its element, which is now carved from two slabs instead of one
  allocation per node, with one annotation lookup per element or attribute
  instead of two per node, and identity constraints no longer rebuild a
  content-model map for every node a selector picks (XSD 1.1 validation of
  the 200,000-order document: 20 % fewer allocations, 9 % fewer
  instructions); the streaming reader allocates an element together with
  its attributes (8 % fewer allocations in the streaming workload);
  `fn:string-join` joins nodes without atomizing them into a new sequence,
  `fn:normalize-space` returns an already-normalized string as it is,
  `fn:upper-case`/`lower-case` take an ASCII path, `fn:tokenize`/`replace`/
  `analyze-string` decide "matches the empty string" once per pattern and
  `format-date` reads its picture by byte (the function-call workload: 7 %
  fewer instructions and allocations; the string workload: 4 % fewer
  instructions, 6 % fewer allocations, 40 MB less allocated);
  `fn:json-to-xml` builds its tree from slabs with children slices made to
  size (6 % fewer allocations in the JSON round trip); and indentation is
  written from a constant instead of a string per element. No workload
  executes more instructions than before, and every output is byte-identical.

- a ninth round, on the evaluation context and the value model: the static
  base URI of an expression (the `xml:base` chain above its element in the
  stylesheet, which `doc()`, `document()`, `unparsed-text()`,
  `static-base-uri()` and a few others read) was walked before every
  evaluation of every expression in a stylesheet loaded from a file; it is
  now computed when one of those functions asks for it (HTML output
  workload: 4 % fewer instructions); and four places that turned a node
  set into a boxed sequence of items — `fn:subsequence`, matching a node
  set against a declared type, the string of a one-item `xsl:value-of` or
  attribute value template, and `current-group()` — now work on the node
  set itself (deep named-template recursion: 9 % fewer instructions, 5 %
  fewer allocations; 200 transforms in one process: 5 % fewer
  instructions, 9 % fewer allocations; the function-call workload: 3 %
  fewer allocations; the regular-expression workload: 4 % fewer). No
  workload executes more instructions than before, and every output is
  byte-identical.

- a tenth round, in two parallel halves. The validator's half: an XSD 1.1
  assertion is evaluated against a detached copy of its element, and that
  copy was carved from two fresh slabs per element, every typed node in it
  got a node extension of its own, and every assertion allocated an
  evaluation context, a variable resolver and an empty `$value`; the copy now
  comes from one arena the validation call keeps — reset for every element,
  its slabs grow to the largest tree and then serve every later one, with a
  third slab for the extensions, sized by the same walk that already counts
  the nodes and now also looks each one's type annotation up once — and one
  context and resolver are reused until a function item captures them.
  Identity constraints reuse one context the same way, take the evaluator's
  node set as it is instead of copying it, and build each key tuple field by
  field in one buffer (a string-typed field appended directly, since its cast
  hands the lexical back unchanged) instead of one string per field plus
  one for the join; and the per-element count of children that XSD 1.1's
  dynamic Element Declarations Consistent check needs is kept only where
  the content model has a lax or strict wildcard, the only case that ever
  reads it. XSD 1.1 validation of the 200,000-order document: a third
  fewer allocations, 60 % fewer bytes allocated, 9 % fewer instructions,
  11 % less peak memory. No workload executes more instructions than
  before, and every output is byte-identical. Tried and dropped: carving
  the streaming reader's nodes from shared slabs would have saved a fifth of
  that workload's allocations, but a slab is one object to the collector,
  so the dead records sharing a slab with a live one kept their own
  children — and, slab by slab, the whole document — reachable; the
  memory-bound tests caught it (1.2 GB peak for 82 MB of input where 0.1 MB
  is the norm). Also dropped: a non-recursive walk for the XSD 1.0
  schema-location-hint check, which saved 0.1 % of the instructions and
  cost an 8 MB stack of child pointers.

The sixth to ninth rounds were measured on Linux (a 4-core x86-64 virtual machine)
against the previous version rather than against libxslt, so the table
above is unchanged; its XSLT 1.0 workloads moved by less than the noise.

An earlier version of this page put a third of the time into the operating
system's memory calls. That came from the CPU profiler on macOS, which books
kernel time to the wrong system call; measured directly, those calls take a
few milliseconds. The garbage collector is the largest real share now.
What remains is mostly the document and result trees themselves.

## Same results

Both processors produce the same results. In two workloads the text differs
in the last digits of sums: XPath 1.0 writes a number with as many digits as
are needed to identify it exactly (`17251.089999999997`), libxslt rounds to 15
digits (`17251.09`). The benchmark compares numbers to 12 significant digits.

## Running the benchmark

```bash
go run scripts/bench/main.go            # -n runs, -records size of the input
```

It builds the command-line tool from the checkout, generates the input, and
prints this table for your machine. It needs `xsltproc` and `xmllint`, which
macOS ships and Linux distributions package as `xsltproc` and
`libxml2-utils`.
