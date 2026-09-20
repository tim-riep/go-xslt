# Known-unsupported XPath/XQuery 3.1 features

This engine targets high QT3/FOTS conformance (currently ~86.6%, `tools/qt3_results.txt`).
The clusters below are **deliberately not implemented** — each needs a capability Go's RE2
cannot provide, a large CLDR/locale dataset, or stylesheet-prolog support outside the XPath
engine's scope. They are documented here rather than worked around. Counts are the
approximate number of remaining failing QT3 cases each represents.

## Regex back-references & look-around — ~150 cases (`fn-matches.re`, parts of replace/tokenize)
Go's `regexp` (RE2) by design has **no back-references** (`\1`, `\2`) and **no look-around**.
XSD/XPath regex permits both. Everything else in the XSD regex grammar that RE2 lacks **is**
now translated (`functions.go xsdRegexToGo` + `regex_blocks.go` + `regex_class.go`): the ~110
Unicode block names `\p{IsBasicLatin}`, character-class subtraction `[a-z-[aeiou]]`, the
`\i \I \c \C` escapes, and rejection of XSD-disallowed constructs (`\b`, inline-flag groups,
invalid escapes). Only back-references / look-around remain impossible without a backtracking
engine.

## format-number `declare decimal-format` — ~80 cases (`fn-format-number`)
Most remaining `format-number` failures use a **named or default-overridden decimal-format**
(custom zero-digit/grouping/decimal characters, `infinity`/`NaN` text, e.g.
`format-number(x,'+!!!,!!!','myminus')`). Decimal-format declarations live in the XQuery/XSLT
prolog, which the (XPath-only) engine and harness do not parse. Scientific notation, multi-grouping,
and the standard `0`/`#` picture features **are** supported (`fn_numeric.go`).

## format-date/time locale NAMES & non-Gregorian calendars — ~110 cases (`fn-format-date`/`-time`)
Timezone (`[Z]`/`[z]`), numeric components, and fractional-second width modifiers are
implemented. What is **not**: localized month/day **names** in languages other than English
(`[MNn]`/`[FNn]` with `lang='de'`…), ordinal-name forms, non-Gregorian calendars, and non-ASCII
**digit families** (Thai, Osmanya, …). These need CLDR locale data.

## Per-type cast lexical validation — ~130 cases (`prod-CastExpr`)
Many `cast as` cases expect an error for an **invalid lexical value** of a specific target type
(malformed QName/anyURI/gregorian/duration strings, unresolvable type prefixes, multi-item
source sequences). Source-type guards, derived-integer bounds, gregorian/duration/binary
validation, and date range/leap-day validation **are** implemented; the long tail of per-type
lexical-form regexes is not exhaustively covered.

## Resolver-dependent functions — partial (`fn-doc`, `fn-collection`, some `fn-function-lookup`)
`fn:unparsed-text`/`-available`/`-lines` and `fn:doc`/`doc-available` now work through a
`Context.Resolver` (the QT3 harness wires the `<resource>` map). `fn:collection`,
`fn:uri-collection`, `fn:parse-xml`, `fn:transform`, and XQuery-module loading remain
unimplemented — they need host-application resource/transform infrastructure, not core XPath.

---
**Now SUPPORTED (previously listed here):** UCA collation via `golang.org/x/text/collate`
(`fn_collation.go`) and `fn:normalize-unicode` via `x/text/unicode/norm`. `go.mod` therefore
depends on **`golang.org/x/text`** (BSD-3-Clause).

By design, out of scope for the whole project (see `CLAUDE.md`): XSLT streaming, packages,
schema-awareness.
