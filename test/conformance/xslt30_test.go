package conformance

// W3C XSLT 3.0 conformance harness (xslt30-test). Runs whole stylesheets against
// source documents and checks the serialized output against the suite's own
// expected-output assertions. Shares the catalog/charset/resolver infrastructure
// with the QT3 harness (qt3_test.go); only the <test>/<dependencies>/<result>
// shapes and the transform invocation differ.
//
// Clone:  git clone --depth 1 https://github.com/w3c/xslt30-test.git \
//             test/conformance/xslt30-test
// Run:    go test ./test/conformance/ -run TestXSLT30 -v

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"github.com/tim-riep/go-xslt/internal/xslt"
)

// ---- XSLT-specific catalog model (dependencies/test differ from FOTS) ----

type xsltValDep struct {
	Value     string `xml:"value,attr"`
	Satisfied string `xml:"satisfied,attr"`
}
type xsltDeps struct {
	Specs     []xsltValDep `xml:"spec"`
	Features  []xsltValDep `xml:"feature"`
	XsdVer    []xsltValDep `xml:"xsd-version"`
	Unicode   []xsltValDep `xml:"unicode-version"`
	YearComps []xsltValDep `xml:"year_component_values"`
	NumLangs  []xsltValDep `xml:"languages_for_numbering"`
	IgnoreDoc []xsltValDep `xml:"ignore_doc_failure"`
	EnableAss []xsltValDep `xml:"enable_assertions"`
	HTMLVer   []xsltValDep `xml:"default_html_version"`
	// PkgVerRes is package_version_resolution: XSLT 3.0 leaves which
	// candidate xsl:use-package picks, among several matching a
	// package-version-ranges expression, implementation-defined (§3.5.2) — a
	// processor may support "highest_version", "lowest_version", or neither
	// in particular ("unspecified", satisfied by any policy). This engine's
	// documented policy (packages.go resolvePackageIn) is highest_version.
	PkgVerRes []xsltValDep `xml:"package_version_resolution"`
}

type xsltEntryParam struct {
	Name   string `xml:"name,attr"`
	Select string `xml:"select,attr"`
	// Static marks a static="yes" catalog <param> override: its value must
	// reach the STYLESHEET at compile time (xslt.CompileAtWithStatic), not
	// the usual runtime Entry.Params/ParamSelects — see ssCache.get's own
	// doc comment for why (several xslt30-test cases share one stylesheet
	// FILE but bake in a different expression per case via a shadow
	// attribute fed from exactly this). A plain Go bool field would
	// unmarshal via strconv.ParseBool, which rejects the catalog's actual
	// spelling "yes" (only accepts true/false/1/0/t/f) — the whole
	// containing XML document fails to decode as a result, silently
	// dropping every test IN THAT FILE to zero, not just the ones using
	// static params. Kept as a raw string and compared explicitly instead.
	Static string     `xml:"static,attr"`
	Tunnel string     `xml:"tunnel,attr"`
	Attr   []xml.Attr `xml:",any,attr"`
}
type xsltTest struct {
	Stylesheet      []fotsSource `xml:"stylesheet"`
	Package         []fotsSource `xml:"package"`
	InitialTemplate *struct {
		Name   string           `xml:"name,attr"`
		Attr   []xml.Attr       `xml:",any,attr"`
		Params []xsltEntryParam `xml:"param"`
	} `xml:"initial-template"`
	InitialMode *struct {
		Name   string           `xml:"name,attr"`
		Select string           `xml:"select,attr"`
		Attr   []xml.Attr       `xml:",any,attr"`
		Params []xsltEntryParam `xml:"param"`
	} `xml:"initial-mode"`
	InitialFunction *struct {
		Name   string           `xml:"name,attr"`
		Attr   []xml.Attr       `xml:",any,attr"`
		Params []xsltEntryParam `xml:"param"`
	} `xml:"initial-function"`
	Params      []xsltEntryParam `xml:"param"`
	ContextItem *struct {
		Select string `xml:"select,attr"`
	} `xml:"context-item"`
	// Output declares where the principal result is written, which is what
	// establishes the BASE OUTPUT URI that fn:current-output-uri() reports and
	// that a relative xsl:result-document/@href resolves against. file="#absent"
	// means the host deliberately supplies none (current-output-uri-013/015).
	// It is also the catalog's <output> element in general: the W3C's own
	// runner keeps the transformation result as a TREE and evaluates the
	// assertions against it unless the test asks for serialization
	// (serialize="yes"); this harness always serializes, which is equivalent
	// for every method that renders a node tree — but NOT for json/adaptive,
	// which render a VALUE (a map becomes JSON text, an element node becomes a
	// JSON string). Only that one case consults Serialize: see
	// treeAssertionOutput.
	Output *struct {
		File      string `xml:"file,attr"`
		Tree      string `xml:"tree,attr"`
		Serialize string `xml:"serialize,attr"`
		// ResultVar names a variable the catalog binds to the RAW sequence
		// the transformation returned, for assertions that cannot be made
		// against the serialized output (initial-template-004).
		ResultVar string `xml:"result-var,attr"`
	} `xml:"output"`
}
type xsltCase struct {
	Name   string    `xml:"name,attr"`
	Deps   xsltDeps  `xml:"dependencies"`
	Envs   []fotsEnv `xml:"environment"`
	Test   xsltTest  `xml:"test"`
	Result struct {
		// Attr captures the <result> element's OWN xmlns:* bindings. Several
		// cases declare the prefix their assertions use there rather than on
		// the test-set root or on each <assert> (validation-0202 writes
		// <result xmlns:h="http://www.w3.org/1999/xhtml"> and then asserts
		// /h:html/...), and without it every such assertion fails to PARSE on
		// an unbound prefix rather than being evaluated at all.
		Attr     []xml.Attr   `xml:",any,attr"`
		Children []fotsAssert `xml:",any"`
	} `xml:"result"`
}
type xsltSet struct {
	Name  string     `xml:"name,attr"`
	Attr  []xml.Attr `xml:",any,attr"`
	Deps  xsltDeps   `xml:"dependencies"`
	Envs  []fotsEnv  `xml:"environment"`
	Cases []xsltCase `xml:"test-case"`
}

// setNSAttrs returns the xmlns:* bindings declared on a <test-set> root
// element — e.g. xmlns:xs="..." on _namespace-test-set.xml — which are in
// scope for every descendant assertion per normal XML scoping (namespace-2611:
// self::xs:foo only resolves if this root-level binding is honored).
func setNSAttrs(set xsltSet) map[string]string {
	var ns map[string]string
	for _, at := range set.Attr {
		if at.Name.Space == "xmlns" {
			if ns == nil {
				ns = map[string]string{}
			}
			ns[at.Name.Local] = at.Value
		}
	}
	return ns
}

// xsltUnsupportedFeatures: features we don't implement; a test requiring one (with
// satisfied != "false") is skipped.
var xsltUnsupportedFeatures = map[string]bool{
	"streaming": true, "schema_aware": true, "schemaImport": true,
	"schemaValidation": true, "schemaAware": true, "static_typing": true,
	"staticTyping": true, "namespace_axis": false, "backwards_compatibility": true,
	"advanced_uca_fallback": true,
	// streaming-fallback names a processor CONFIGURATION, not an engine
	// capability: the test set's own description is "designed for a streaming
	// processor that is CONFIGURED to fall back to non-streaming mode if the
	// stylesheet is found to be non-streamable". XSLT30_FULL=1 configures
	// the exact opposite — SetEnforceStreamability(true), so a declared-
	// streamable construct that is not guaranteed-streamable is a static
	// error — and the two configurations cannot both hold in one run. The
	// suite makes the conflict concrete: streaming-fallback-005/006 run
	// merge-094.xsl and merge-095.xsl, the very stylesheets merge-094/095
	// require XTSE3430 for. Claiming the feature while enforcing strictly
	// would be the dishonest half of that pair, so it stays unsupported and
	// those cases skip. (Under the default run the set already skips on its
	// streaming dependency, so this changes nothing there.)
	"streaming-fallback": true,
}

// XSLT30_FULL=1 runs this engine as a real caller who has opted into both of
// its optional capabilities — genuine bounded-memory streaming execution
// (stream_exec.go, which is always ON regardless of this knob: what turning
// XTSE3430 ENFORCEMENT on adds is that a streamable="yes" declaration this
// analysis cannot PROVE becomes a static error instead of a silent buffered
// fallback) and schema-awareness (xslt.SetSchemaAware, off by default because
// XSLT 3.0 defines "basic" and "schema-aware" as two distinct, non-additive
// conformance profiles with actually different required behaviour — see
// schema_property.go). The two used to be independently selectable
// (XSLT30_STREAMING=1 / XSLT30_SCHEMA=1) across nine measurement rounds, which
// was the right shape for isolating which of the two a given fix belonged to;
// once both were mature the separate numbers stopped being the ones that
// mattered, and a real embedding application does not pick one — it calls
// SetSchemaAware(true) once, if at all, and gets both together. One knob now
// reflects that.
//
// xsltFullHarness reports whether this run makes both claims. Read in several
// places — the feature-dependency tables below, and the <source validation>
// pass-through — so it is one predicate rather than several spellings that
// could drift apart.
//
// XSLT30_STRICTCODE=1 makes an expected-error assertion check the catalog's
// @code against the code the engine actually raised, instead of accepting any
// error. Both knobs are OPT-IN: turning either on by default would move the
// headline denominator or reclassify cases that currently pass on "some error
// was raised", and the project's no-regression bar compares fail names
// against a fixed baseline.
func init() {
	if !xsltFullHarness() {
		return
	}
	delete(xsltUnsupportedFeatures, "streaming")
	// The claim has to be made in BOTH directions, or the knob means two
	// different things at once: a case written for a processor WITHOUT
	// streaming (feature streaming satisfied="false") does not apply to a
	// run that claims it, exactly as for dynamic_evaluation and the rest of
	// xsltSupportedFeatures. Exactly one case in the suite depends on this
	// (system-property-013, the complement of -012).
	xsltSupportedFeatures["streaming"] = true
	// Make a streamable="yes" declaration binding for this run. Off by
	// default, the engine ignores one it cannot prove and runs buffered —
	// which is what XTSE3430's own "unless the user has indicated that the
	// processor is to handle this situation by processing the stylesheet
	// without streaming" clause permits, and what keeps cases such as
	// accumulator-053/054 passing.
	xslt.SetEnforceStreamability(true)
	// The suite spells the schema-awareness dependency four ways across
	// its test sets; all four name the same optional feature.
	for _, f := range []string{"schema_aware", "schemaImport", "schemaValidation", "schemaAware"} {
		delete(xsltUnsupportedFeatures, f)
		// Both directions, exactly as for streaming: the 31 cases written
		// for a processor WITHOUT schema-awareness (satisfied="false" —
		// the validation/error-1660/type-0303 family) do not apply to a
		// run that claims it.
		xsltSupportedFeatures[f] = true
	}
	// static_typing deliberately stays unsupported: XSLT 3.0 §26.1 leaves
	// the interaction of the XPath static typing feature with XSLT
	// unspecified, and no conformance level requires it.
	xslt.SetSchemaAware(true)
	// backwards_compatibility (XSLT 3.0 §3.9.1, an [xsl:]version below 2.0)
	// is the third optional processor capability this combined knob claims —
	// see xslt.SetBackwardsCompatible's own doc comment. Both directions
	// again: error-0160a/initial-template-080/-081 (satisfied="false" — a
	// processor WITHOUT it) do not apply to a run that claims it.
	delete(xsltUnsupportedFeatures, "backwards_compatibility")
	xsltSupportedFeatures["backwards_compatibility"] = true
	xslt.SetBackwardsCompatible(true)
}

// xsltStrictErrorCodes reports whether an expected-error assertion must match
// the catalog's error code, not merely the fact that an error was raised. It is
// deliberately independent of XSLT30_FULL so each knob measures one thing:
// combining them would mix code mismatches in unrelated test sets (avt, mode,
// static, use-when) into the combined-capability number.
func xsltStrictErrorCodes() bool {
	return os.Getenv("XSLT30_STRICTCODE") != ""
}

// xsltErrorCode extracts the "err:CODE:" token this engine embeds in a
// diagnostic (the same convention instr_trycatch.go's tcExtractCode reads).
// It returns "" when the message carries no catalogued code.
func xsltErrorCode(msg string) string {
	i := strings.Index(msg, "err:")
	if i < 0 {
		return ""
	}
	rest := msg[i+len("err:"):]
	j := strings.IndexByte(rest, ':')
	if j <= 0 {
		return ""
	}
	code := rest[:j]
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return code
}

// xsltSupportedFeatures names optional features this processor DOES implement,
// so a dependency of satisfied="false" on one of them means the test targets a
// different kind of processor and is skipped. Kept deliberately short: a
// feature absent from BOTH this map and xsltUnsupportedFeatures still runs.
var xsltSupportedFeatures = map[string]bool{
	"dynamic_evaluation": true,
	// This engine implements disable-output-escaping (xmltree.Node.Raw,
	// honoured by the serializer; doe-0176, doe-0401 and the rest of the
	// disable-output-escaping test-set exercise it), so the eight cases
	// declaring the feature satisfied="false" — written for a processor that
	// does NOT support it, and asserting the escaped output, or the
	// XTRE1620/XTRE1630 recovery error, or the error-1620b/1630b variants
	// instead — target a different kind of processor and do not apply here.
	"disabling_output_escaping": true,
	// This engine implements the full XPath 3.1 language and F&O catalog, so a
	// variant written for a processor WITHOUT it does not apply
	// (format-number-069b expects xsl:decimal-format/@exponent-separator to be
	// rejected; 069a, the XPath_3.1 variant, is the one that applies and passes).
	"XPath_3.1": true,
	// This engine implements XSD 1.1 — its validator, and the XSD 1.1
	// character-class grammar its regex functions use (regex_grammar.go) —
	// so the XSD-1.0-only member of each regex-syntax pair (0056 vs 0056a,
	// 0086 vs 0086a, 0102 vs 0102a: XSD_1.1 satisfied="false") targets a
	// different processor, and the 1.1 member is the one that applies. The
	// QT3 harness declares the same (its xsd-version dependency).
	"XSD_1.1": true,
}

// xsltSkip returns "" to run, else a short skip reason.
func xsltSkip(deps ...xsltDeps) string {
	for _, d := range deps {
		for _, s := range d.Specs {
			if s.Satisfied == "false" {
				continue
			}
			// An XSLT 3.0 processor satisfies any spec admitting 3.0: XSLT30,
			// XSLT10+/XSLT20+ ("1.0 or later"), XT30, or XPath-only specs. A spec
			// pinned to an EXACT older version (XSLT10/XSLT20 without "+", or an
			// XQuery-only spec) does not apply.
			v := s.Value
			applies := strings.Contains(v, "XSLT30") || strings.Contains(v, "XT30") ||
				strings.Contains(v, "XP") ||
				((strings.HasPrefix(v, "XSLT10") || strings.HasPrefix(v, "XSLT20") ||
					strings.HasPrefix(v, "XT10") || strings.HasPrefix(v, "XT20")) &&
					strings.Contains(v, "+"))
			if !applies {
				return "spec:" + v
			}
		}
		for _, f := range d.Features {
			if f.Satisfied != "false" && xsltUnsupportedFeatures[f.Value] {
				return "feat:" + f.Value
			}
			// satisfied="false" asks for a processor that LACKS the feature;
			// one we do implement cannot satisfy that, so the test does not
			// apply (system-property-014b wants a processor without dynamic
			// evaluation).
			if f.Satisfied == "false" && xsltSupportedFeatures[f.Value] {
				return "feat-not:" + f.Value
			}
		}
		for _, x := range d.XsdVer {
			// XSD 1.1 is what this engine implements (see xsltSupportedFeatures).
			if x.Satisfied != "false" && strings.Contains(x.Value, "1.0") && !strings.Contains(x.Value, "1.1") {
				return "xsd1.0"
			}
		}
		for _, u := range d.Unicode {
			// Tests pinned to an exact old Unicode version compare character
			// ranges Go's Unicode 15 tables can never reproduce (see the
			// unicode-90 cluster) — the dependency is unsatisfied by design.
			if u.Satisfied != "false" {
				return "unicode-version-pinned"
			}
		}
		for _, y := range d.YearComps {
			// year_component_values: "support negative year" / "support year
			// above 9999" declares an assumption about the PROCESSOR's date/
			// time support. Our engine (xpath/datetime.go) supports both
			// arbitrary-precision and negative years, so a variant asserting
			// satisfied="false" (i.e. written for processors that reject
			// those lexical forms, expecting e.g. FODT0001) does not apply to
			// us — it's a "b"/unmarked-variant duplicate that already runs
			// and is expected to succeed.
			if y.Satisfied == "false" {
				return "year-component-values-unsupported-variant"
			}
		}
		for _, l := range d.NumLangs {
			// languages_for_numbering: the test needs xsl:number word-spelling
			// (@format="w"/"W"/... with @lang) in a language other than
			// English. The engine only implements English (and a narrow
			// single-digit German case not exercised by any test declaring
			// this dependency — see num2SpellGerman); full non-English
			// spellout needs CLDR locale data and is deliberately out of
			// scope, so a value other than English is an unsatisfied
			// dependency for us, same as e.g. an unsupported feature.
			if l.Satisfied == "false" {
				continue
			}
			v := strings.ToLower(strings.TrimSpace(l.Value))
			if v != "" && v != "en" && !strings.HasPrefix(v, "en-") {
				return "lang:" + l.Value
			}
		}
		for _, hv := range d.HTMLVer {
			// default_html_version declares what the PROCESSOR's default
			// html-version is, so the suite can offer one variant per default
			// (output-0195 vs -0195a, result-document-1402 vs -1402b: the same
			// stylesheet, different expected results). This processor defaults
			// to 5, so the "4" variants do not apply to it.
			if hv.Satisfied == "false" {
				continue
			}
			if v := strings.TrimSpace(hv.Value); v != "" && v != "5" && v != "5.0" {
				return "default_html_version:" + v
			}
		}
		for _, ea := range d.EnableAss {
			// enable_assertions: whether the processor was asked to DISABLE
			// xsl:assert. This engine always evaluates assertions, so a case
			// declaring the dependency satisfied="false" (written for a
			// processor running with assertions off) does not apply to us.
			if ea.Satisfied == "false" {
				return "feat:assertions-disabled"
			}
		}
		for _, ig := range d.IgnoreDoc {
			// ignore_doc_failure: a Saxon-specific optional recovery policy
			// (silently swallow a failed document()/fn:doc retrieval instead
			// of raising a fatal dynamic error). Our engine implements the
			// spec-conformant default (raise), so a case declaring this
			// dependency satisfied="true" needs behavior we don't offer.
			if ig.Satisfied == "true" {
				return "feat:ignore_doc_failure"
			}
		}
		for _, pv := range d.PkgVerRes {
			// package_version_resolution: this engine's declared policy is
			// "highest_version" (packages.go resolvePackageIn) — a case
			// requiring a different policy targets a different (but equally
			// spec-conformant) processor. "unspecified" is not a policy to
			// compare against ours; it means the test does not depend on
			// which policy the processor implements (use-package-203c and
			// its siblings apply to every processor regardless).
			v := strings.TrimSpace(pv.Value)
			if pv.Satisfied != "false" && v != "highest_version" && v != "unspecified" {
				return "package_version_resolution:" + v
			}
		}
	}
	return ""
}

// ---- compile cache (a stylesheet file is shared by many cases) ----

type ssCache struct {
	mu sync.Mutex
	m  map[string]ssEntry
}
type ssEntry struct {
	ss  *xslt.Stylesheet
	err error
}

// get compiles (and caches) the stylesheet at path. static holds the test
// case's <param static="yes"> entries, which are STATIC parameters: they must
// be supplied at compile time, so they are part of the cache key — two cases
// running the same file with different static values get two compilations
// (several xslt30-test cases share one stylesheet FILE but bake in a
// DIFFERENT expression at compile time via a shadow attribute fed from a
// per-case static param override — maps-901..907, date-094/095).
func (c *ssCache) get(path string, static map[string]string, pkgs []xslt.PackageSource, schemas []xslt.SchemaSource) (*xslt.Stylesheet, error) {
	key := path
	for _, p := range pkgs {
		key += "\x01" + p.Name + "@" + p.Version + "=" + p.Path
	}
	// Host-offered schemas change what xsl:import-schema resolves to, so two
	// cases sharing one stylesheet file under different environments must not
	// share a compilation.
	for _, sc := range schemas {
		key += "\x02" + sc.Namespace + "=" + sc.Path
	}
	if len(static) > 0 {
		names := make([]string, 0, len(static))
		for k := range static {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, n := range names {
			key += "\x00" + n + "=" + static[n]
		}
	}
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok {
		return e.ss, e.err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		e = ssEntry{nil, err}
	} else {
		ss, cerr := xslt.CompileAtWithSchemas(string(b), path, static, pkgs, schemas)
		e = ssEntry{ss, cerr}
	}
	c.mu.Lock()
	c.m[key] = e
	c.mu.Unlock()
	return e.ss, e.err
}

// ---- running one case ----

type xsltResult struct {
	pass, fail, skip int
	skipReasons      map[string]int
	fails            []string
	passByVer        map[string]int // XSLT30_BYVER: ran-pass bucketed by min spec version
	failByVer        map[string]int
}

// xsltSpecVersion buckets an applicable test by the minimum XSLT spec it needs
// ("1.0"/"2.0"/"3.0", or "xpath"/"none") for the XSLT30_BYVER report. Only the
// spec deps that a 3.0 processor satisfies remain by the time a test is run, so
// the highest version among them is the one the test actually exercises.
func xsltSpecVersion(deps ...xsltDeps) string {
	best := -1
	label := map[int]string{3: "3.0", 2: "2.0", 1: "1.0", 0: "xpath"}
	for _, d := range deps {
		for _, s := range d.Specs {
			if s.Satisfied == "false" {
				continue
			}
			v := s.Value
			n := -1
			switch {
			case strings.Contains(v, "XSLT30") || strings.Contains(v, "XT30"):
				n = 3
			case strings.Contains(v, "XSLT20") || strings.Contains(v, "XT20"):
				n = 2
			case strings.Contains(v, "XSLT10") || strings.Contains(v, "XT10"):
				n = 1
			case strings.Contains(v, "XP"):
				n = 0
			}
			if n > best {
				best = n
			}
		}
	}
	if best < 0 {
		return "none"
	}
	return label[best]
}

// xsltFullHarness reports whether this run claims both optional capabilities
// together — see the doc comment on init() above. Defined here as a plain
// function, and read at every call site below, rather than a package var, so
// the claim can never be read before it is set: the streaming and schema
// canary test files (xslt30_streaming_claim_test.go,
// xslt30_schema_canary_test.go) call this same predicate from their own
// init()s, whose relative ordering against this file's init() Go does not
// guarantee.
func xsltFullHarness() bool { return os.Getenv("XSLT30_FULL") != "" }

// fnJSONNamespace is the namespace of the XML representation of JSON that
// fn:json-to-xml produces (F&O 3.1 §17.5).
const fnJSONNamespace = "http://www.w3.org/2005/xpath-functions"

// suiteJSONSchemaPath is the SUITE's own copy of the F&O schema for the XML
// representation of JSON, offered to every schema-aware compile as a
// host-provided schema for that namespace.
//
// WHY THIS LIVES IN THE HARNESS AND NOT IN THE ENGINE
// ---------------------------------------------------
// fn:json-to-xml's validate:true option is defined against a specific
// normative schema, and json-to-xml-typed.xsl asks for its components the only
// way XSLT offers — `xsl:import-schema namespace="…"` with no
// @schema-location, leaving the processor to supply them. The one in-engine
// precedent for answering such a declaration from the processor's own
// knowledge, decl_import_schema.go's xmlNamespaceSchema, covers the four
// attributes of the XML namespace: tiny, and part of XSLT's own spec text.
// The F&O JSON schema is a substantial third-party normative document, and
// this repo's stated policy is that the public repo carries no third-party
// licensed material — so embedding it in product code is out.
//
// The suite ships the document itself, under the fixtures that use it. Since
// test/conformance/xslt30-test is a gitignored local-only clone (see
// README.md), sourcing it from there keeps the schema entirely outside the
// repo proper: only this PATH is committed, exactly like every other suite
// reference in this file. Returns "" when the suite is absent or moves the
// file, which simply leaves the declaration unanswered as before.
//
// Offering it unconditionally is safe because a host schema is only ever
// CONSULTED by an xsl:import-schema that names its namespace and locates no
// document of its own (compiler.hostSchemaFor); three suite stylesheets do
// that for this namespace, and none for a conflicting one.
func suiteJSONSchemaPath() string {
	p := filepath.Join(repoRoot(), "test", "conformance", "xslt30-test",
		"tests", "fn", "json-to-xml", "schema-for-json.xsd")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func runXSLTSet(setFile string, cache *ssCache, verbose bool) *xsltResult {
	r := &xsltResult{skipReasons: map[string]int{}, passByVer: map[string]int{}, failByVer: map[string]int{}}
	byVer := os.Getenv("XSLT30_BYVER") != ""
	dir := filepath.Dir(setFile)
	b, err := os.ReadFile(setFile)
	if err != nil {
		return r
	}
	var set xsltSet
	if err := unmarshalFOTS(b, &set); err != nil {
		return r
	}
	for _, tc := range set.Cases {
		st, reason := safeRunXSLT(tc, set, dir, cache)
		switch st {
		case "pass":
			r.pass++
			if byVer {
				r.passByVer[xsltSpecVersion(set.Deps, tc.Deps)]++
			}
		case "skip":
			r.skip++
			r.skipReasons[reason]++
		default:
			r.fail++
			if byVer {
				r.failByVer[xsltSpecVersion(set.Deps, tc.Deps)]++
			}
			if verbose && len(r.fails) < 4000 {
				r.fails = append(r.fails, fmt.Sprintf("FAIL %s/%s [%s] ver=%s", set.Name, tc.Name, reason, xsltSpecVersion(set.Deps, tc.Deps)))
			}
		}
	}
	return r
}

func safeRunXSLT(tc xsltCase, set xsltSet, dir string, cache *ssCache) (string, string) {
	type res struct{ st, rs string }
	ch := make(chan res, 1)
	go func() {
		defer func() {
			if e := recover(); e != nil {
				if os.Getenv("XSLT30_PANICDUMP") != "" {
					fmt.Printf("PANIC %s/%s: %v\n%s\n", set.Name, tc.Name, e, debug.Stack())
				}
				ch <- res{"fail", "panic"}
			}
		}()
		st, rs := runXSLTCase(tc, set, dir, cache)
		ch <- res{st, rs}
	}()
	select {
	case r := <-ch:
		return r.st, r.rs
	case <-time.After(30 * time.Second):
		// The engine's depth/step guards prevent unrecoverable overflow, so an
		// abandoned goroutine is safe; a genuine hang is counted as a failure.
		// The cap is generous because a few suite cases legitimately do a LOT
		// of work — the catalog-* cases each compile and inspect every
		// stylesheet in the suite — and at 8s they flaked in and out of
		// passing depending on how loaded the machine was, which made the
		// fail set non-reproducible between runs.
		return "fail", "timeout"
	}
}

func runXSLTCase(tc xsltCase, set xsltSet, dir string, cache *ssCache) (string, string) {
	if reason := xsltSkip(set.Deps, tc.Deps); reason != "" {
		return "skip", reason
	}
	// The unicode-90 set asserts exact Unicode character counts pinned to an old
	// Unicode version (e.g. \d=\p{Nd} expects 370; \p{L} expects 48739). Go's
	// unicode package ships Unicode 15.0 (\p{Nd}=680, \p{L}=136103), so these
	// version-pinned counts can never match without bundling obsolete Unicode
	// tables. Our regex semantics are correct for current Unicode; skip by design.
	if strings.Contains(set.Name, "unicode-90") {
		return "skip", "unicode-version-pinned"
	}
	if len(tc.Result.Children) == 0 {
		return "skip", "no-assertion"
	}

	// Resolve the principal module (from <test> or an inline environment): an
	// xsl:stylesheet/xsl:transform, OR an xsl:package — compile.go already
	// accepts a top-level xsl:package as a module root exactly like
	// xsl:stylesheet ("for every purpose this processor implements, an
	// xsl:stylesheet"), and xsl:use-package itself is fully implemented
	// (packages.go/loader.go/pkgcheck.go), so the only piece actually missing
	// was this harness recognizing <test><package> as something to compile and
	// run instead of skipping outright. A <test> names its principal artifact
	// with either a <stylesheet role!="secondary"> or a <package
	// role!="secondary"> — verified against the whole corpus that the two
	// never both appear non-secondary in the same <test> — so whichever is
	// present resolves ssFile the same way; compileModule doesn't care which
	// kind of root selfPath points at.
	ssFile := ""
	for _, s := range tc.Test.Stylesheet {
		if s.Role != "secondary" {
			ssFile = s.File
		}
	}
	for _, s := range tc.Test.Package {
		if s.Role != "secondary" {
			ssFile = s.File
		}
	}
	env := resolveXsltEnv(tc, set)
	if ssFile == "" {
		for _, s := range env.Stylesheets {
			if s.Role != "secondary" {
				ssFile = s.File
			}
		}
	}
	if ssFile == "" {
		return "skip", "no-stylesheet"
	}
	staticParams := map[string]string{}
	for _, p := range tc.Test.Params {
		switch strings.TrimSpace(p.Static) {
		case "yes", "true", "1":
			sel := p.Select
			// The catalog's own @as declares the TYPE the value is supplied
			// as, which can differ from the receiving xsl:param's declared
			// type (static-013c supplies as="xs:string" select="111" to a
			// param declared as="xs:integer" and expects XTTE0590). The
			// engine takes the value as an expression, so the supplied type
			// is applied by constructing it.
			for _, at := range p.Attr {
				if at.Name.Local == "as" && at.Name.Space == "" && strings.TrimSpace(at.Value) != "" {
					sel = strings.TrimSpace(at.Value) + "((" + sel + "))"
				}
			}
			staticParams[p.Name] = sel
		}
	}
	// Source document (role="."). Catalog-level envs resolve files against the
	// catalog root (env.base); set/inline envs against the test-set dir.
	srcBase := dir
	if env.base != "" {
		srcBase = env.base
	}
	// Library packages the environment makes available to xsl:use-package.
	// @uri is the package NAME; when it is absent the engine falls back to the
	// module's own xsl:package/@name and @package-version.
	var pkgs []xslt.PackageSource
	for _, p := range env.Packages {
		if p.File == "" {
			continue
		}
		pkgs = append(pkgs, xslt.PackageSource{
			Name:    p.URI,
			Version: p.Version,
			Path:    filepath.Join(srcBase, p.File),
		})
	}
	// A <test> can also name library packages directly (role="secondary"),
	// rather than (or in addition to) via its <environment> — package-100/101
	// and next-match-036/037/040 use a role="secondary" <package> alongside a
	// <stylesheet role="principal"> in the SAME <test>, and several
	// role="principal" <package> cases (accept-901, package-016,
	// override-t-003c, ...) pull in a sibling secondary <package> the same
	// way. These live in the test-set file itself, so — unlike env.Packages,
	// which may come from a shared catalog-level environment resolved against
	// srcBase — they resolve against dir, exactly like ssFile.
	for _, p := range tc.Test.Package {
		if p.Role != "secondary" || p.File == "" {
			continue
		}
		pkgs = append(pkgs, xslt.PackageSource{
			Name:    p.URI,
			Version: p.Version,
			Path:    filepath.Join(dir, p.File),
		})
	}
	// Schema documents the environment offers to the STYLESHEET's own
	// xsl:import-schema (role="stylesheet-import"). XSLT 3.0 §3.16 makes
	// @schema-location a hint the processor need not use, and several suite
	// stylesheets hint at a file that does not exist, relying on the catalog
	// to name the real document (import-schema-185 hints
	// "variousTypesSchemaInline.xsd"; the environment supplies schema004.xsd).
	// Gated on the combined-capability knob so the DEFAULT run is untouched.
	var hostSchemas []xslt.SchemaSource
	if xsltFullHarness() {
		// A ROLELESS <schema> counts too: the catalog schema documents the
		// element as "a schema to be used to validate a source document"
		// with role optional, and an environment that names one without a
		// role is naming the one schema the whole case is about — which the
		// stylesheet's own xsl:import-schema then needs as well
		// (import-schema-202 hints at lc-simple.xsd, a file the suite does
		// not ship, while the environment supplies import-schema-202.xsd).
		// role="source-reference" counts as well, and for the reason
		// sourceValidationSchema documents at length: derivation questions are
		// answered inside ONE compiled Schema, so a stylesheet asking
		// schema-element(N) about a source validated against that schema has
		// to have imported the SAME document (catalog-001 validates catalog.xml
		// against catalog-schema.xsd and then asks
		// "* instance of schema-element(cat:catalog)").
		// role="secondary" is still excluded: that is a document the entry
		// schema itself imports, not a top-level one.
		for _, sc := range env.Schemas {
			if sc.File == "" || sc.Role == "secondary" {
				continue
			}
			hostSchemas = append(hostSchemas, xslt.SchemaSource{
				Namespace: sc.URI,
				Path:      filepath.Join(srcBase, sc.File),
			})
		}
		// The F&O schema for the XML representation of JSON, supplied by the
		// HOST rather than carried inside the engine — see suiteJSONSchemaPath
		// for why it lives here and not in the product code.
		if p := suiteJSONSchemaPath(); p != "" {
			hostSchemas = append(hostSchemas, xslt.SchemaSource{
				Namespace: fnJSONNamespace,
				Path:      p,
			})
		}
	}
	ss, cerr := cache.get(filepath.Join(dir, ssFile), staticParams, pkgs, hostSchemas)

	srcXML, srcSelect, srcURI, srcValidation := "", "", "", ""
	srcStreaming := false
	hasExplicitPrimary := false
	for _, s := range env.Sources {
		if s.Role == "." {
			hasExplicitPrimary = true
			break
		}
	}
	for _, s := range env.Sources {
		// role="." is unambiguously the primary source. A source with no role
		// is only treated as an implicit primary when it also has no @uri —
		// a roleless source WITH a uri is a secondary resource meant to be
		// fetched via document()/fn:doc(uri), not read as the transform's
		// input (key-021: bib.xml has no role but carries uri="bib.xml" and
		// must not shadow the role="." source key118.xml). And when an
		// explicit role="." source is present in the SAME environment, a
		// roleless-no-uri source never competes for primary even if it comes
		// later in document order (namespace-1901: namespace-19b.xml has no
		// role/uri but is meant only to be fetched via doc(), not to shadow
		// the real role="." primary namespace-19a.xml).
		if s.Role != "." && !(s.Role == "" && s.URI == "" && !hasExplicitPrimary) {
			continue
		}
		srcValidation = strings.TrimSpace(s.Validation)
		// <source streaming="true"/> declares the primary input a STREAMED
		// document. The engine still builds the whole tree — it streams only
		// what xsl:source-document opens — but XSLT 3.0 attaches observable
		// consequences to the DECLARATION alone (§10.3.6's absent captured
		// focus, XTDE3362's non-streamable accumulator), so it is passed
		// through. Gated on the streaming knob below so the default run is
		// untouched by construction.
		srcStreaming = strings.TrimSpace(s.Streaming) == "true" || strings.TrimSpace(s.Streaming) == "yes"
		if strings.TrimSpace(s.Content) != "" {
			srcXML = s.Content
			// @select on a source that also supplies the document itself picks
			// the INITIAL CONTEXT NODE within it (mode-1105: select="/doc"
			// starts the transform at the document element, not the document
			// node).
			srcSelect = strings.TrimSpace(s.Select)
			srcURI = ""
		} else if s.File != "" {
			p := filepath.Join(srcBase, s.File)
			if sb, err := os.ReadFile(p); err == nil {
				srcXML = string(sb)
				srcSelect = strings.TrimSpace(s.Select)
				if abs, aerr := filepath.Abs(p); aerr == nil {
					srcURI = "file://" + abs
				}
			}
		} else if lit, ok := parseXMLLiteral(s.Select); ok {
			// A source with no file/content, wholly specified by
			// select="parse-xml('...')" (id-043): the literal argument IS the
			// source document, synthesized without touching the filesystem.
			srcXML = lit
			srcURI = ""
		}
	}

	// Entry point + params.
	//
	// The catalog's own <source validation="strict|lax"> (admin/catalog-schema.xsd
	// validationEnumType) asks for the source document to arrive already
	// schema-validated — which is how a schema-aware processor is SUPPOSED to
	// receive typed input (XSLT 3.0 §26.2 consumes a PSVI the host supplies),
	// and the only way a schema-element(N) pattern can ever match a source
	// node. The W3C's own runner implements it by round-tripping the document
	// through a throwaway schema-aware stylesheet (runner/run-tests.xsl,
	// c:validated-document); this hands the same request to the engine
	// directly.
	//
	// Gated on the combined-capability knob so the DEFAULT run is untouched by
	// construction: a non-schema-aware processor is required to reject typed
	// input outright (XTDE1665), so honouring the request there would be wrong
	// as well as regression-prone. "skip" is neither strict nor lax and so
	// passes through as no validation, exactly as the enumeration intends.
	e := xslt.Entry{Params: map[string]string{}, SourceURI: srcURI}
	if srcStreaming && xsltFullHarness() {
		e.SourceStreamed = true
	}
	if xsltFullHarness() {
		// A <schema role="source-reference"/> names the schema the environment's
		// SOURCE documents are to be understood as having been validated
		// against — that is what the role means, and the only way a case like
		// type-functions-0401 ("arguments coming from TYPED NODES in input
		// source") can assert data(elem-date) instance of xs:date without its
		// <source> repeating validation="strict". An explicit validation
		// attribute always wins; the implied episode is LAX, which annotates
		// everything the schema declares without turning an undeclared root
		// into an XTTE1512 the catalog never asked for.
		if srcValidation == "" {
			for _, sc := range env.Schemas {
				if sc.File != "" && sc.Role == "source-reference" {
					srcValidation = "lax"
					break
				}
			}
		}
		e.SourceValidation = srcValidation
		// The catalog's <source validation="..."> on a SECONDARY source (one
		// the stylesheet reaches by uri through document()/fn:doc) asks for
		// exactly the same thing as on the primary one — validation-2001/2002
		// declare two such sources strict and then match schema-element()
		// patterns against them.
		for _, sc := range env.Sources {
			if sc.Role == "." || sc.File == "" {
				continue
			}
			mode := strings.TrimSpace(sc.Validation)
			if mode != "strict" && mode != "lax" {
				continue
			}
			href := sc.URI
			if href == "" {
				href = sc.File
			}
			if e.DocValidation == nil {
				e.DocValidation = map[string]string{}
			}
			e.DocValidation[href] = mode
		}
		// The schema to validate the SOURCE against is the environment's, not
		// the stylesheet's: they are frequently different documents
		// (validation-0203..0208 import schema-for-xslt20.xsd into the
		// stylesheet while the source is a GEDCOM instance governed by
		// gedSchema.xsd).
		//
		// Only the ENTRY schemas are handed over (role="source-reference", or
		// no role at all). A role="secondary" entry is a document the entry
		// schema itself imports or includes, which the loader already resolves
		// from disk — and passing one as a top-level compile document instead
		// is actively harmful: validation-02 lists xhtml1-transitional.xsd as
		// secondary, whose content model this validator rejects as
		// non-deterministic (UPA), which would fail the WHOLE compile and take
		// gedSchema.xsd — the schema the source actually needs — down with it.
		for _, sc := range env.Schemas {
			if sc.File == "" || sc.Role == "secondary" {
				continue
			}
			e.SourceSchemas = append(e.SourceSchemas, filepath.Join(srcBase, sc.File))
		}
	}
	// fn:collection() bindings declared by the environment. A collection
	// source's @uri is the URI the stylesheet would see; its @file is where
	// the content actually lives, which is what the engine resolves — the
	// suite always keeps the two in step, so @file (falling back to @uri)
	// with any fragment identifier re-attached is what gets handed over.
	for _, col := range env.Collections {
		hrefs := []string{}
		for _, src := range col.Sources {
			href := src.File
			if href == "" {
				href = src.URI
			}
			if href == "" {
				continue
			}
			if i := strings.IndexByte(src.URI, '#'); i >= 0 && !strings.Contains(href, "#") {
				href += src.URI[i:]
			}
			hrefs = append(hrefs, href)
		}
		if e.Collections == nil {
			e.Collections = map[string][]string{}
		}
		e.Collections[col.URI] = hrefs
	}
	for _, p := range tc.Test.Params {
		e.Params[p.Name] = stripQuotes(p.Select)
		if e.ParamSelects == nil {
			e.ParamSelects = map[string]string{}
		}
		// The catalog's @select is an XPath expression, so the engine gets it
		// verbatim and evaluates it to a TYPED value; Params keeps the old
		// text form as the fallback for anything it cannot evaluate.
		e.ParamSelects[p.Name] = p.Select
	}
	switch {
	case tc.Test.InitialTemplate != nil:
		e.Template = tc.Test.InitialTemplate.Name
		if e.Template == "" {
			e.Template = "xsl:initial-template"
		}
		// A prefixed initial-template name is resolved in the CATALOG's
		// namespace scope, not the stylesheet's, so it is handed to the engine
		// already expanded (call-template-0105 declares my: on the
		// <initial-template> element itself, bound to a different URI than the
		// stylesheet's my:).
		if pfx, local, ok := strings.Cut(e.Template, ":"); ok {
			for _, at := range tc.Test.InitialTemplate.Attr {
				if at.Name.Space == "xmlns" && at.Name.Local == pfx {
					e.Template = "{" + at.Value + "}" + local
					break
				}
			}
		}
		// <param> children of <initial-template> are parameters of the
		// TEMPLATE, not global stylesheet parameters (initial-template-002/
		// 003 supply an "a" of each kind). Their prefixes, like the
		// template's own, resolve in the CATALOG's namespace scope.
		for _, p := range tc.Test.InitialTemplate.Params {
			name := p.Name
			if pfx, local, ok := strings.Cut(name, ":"); ok {
				for _, at := range p.Attr {
					if at.Name.Space == "xmlns" && at.Name.Local == pfx {
						name = "{" + at.Value + "}" + local
						break
					}
				}
			}
			e.TemplateParams = append(e.TemplateParams, xslt.EntryParam{
				Name:   name,
				Select: p.Select,
				Tunnel: p.Tunnel == "yes" || p.Tunnel == "true" || p.Tunnel == "1",
			})
		}
	case tc.Test.InitialFunction != nil:
		e.Function = tc.Test.InitialFunction.Name
		// As for <initial-template>/<initial-mode>, the entry function's
		// prefix is resolved in the CATALOG element's namespace scope, not
		// the stylesheet's, and handed to the engine already expanded
		// (initial-function-102a: the catalog's own xmlns:f on
		// <initial-function> is deliberately bound to a DIFFERENT URI than
		// the stylesheet's xmlns:f, so "f:square1" must resolve against
		// THAT to correctly come up XTDE0041/not-found instead of
		// accidentally matching the stylesheet's own f:square1 were prefix
		// resolution skipped entirely).
		if pfx, local, ok := strings.Cut(e.Function, ":"); ok {
			for _, at := range tc.Test.InitialFunction.Attr {
				if at.Name.Space == "xmlns" && at.Name.Local == pfx {
					e.Function = "{" + at.Value + "}" + local
					break
				}
			}
		}
		for _, p := range tc.Test.InitialFunction.Params {
			e.FuncArgs = append(e.FuncArgs, p.Select)
		}
	case tc.Test.InitialMode != nil:
		e.Mode = tc.Test.InitialMode.Name
		// <initial-mode select="..."> is the XSLT 3.0 initial match selection.
		e.MatchSelection = tc.Test.InitialMode.Select
		// A prefixed initial-mode name resolves against the CATALOG element's
		// own in-scope namespaces (mode-1606 declares xmlns:test on the
		// <initial-mode> element itself), so expand it to Clark form here —
		// exactly as <initial-template> above already does.
		if pfx, local, ok := strings.Cut(e.Mode, ":"); ok {
			for _, at := range tc.Test.InitialMode.Attr {
				if at.Name.Space == "xmlns" && at.Name.Local == pfx {
					e.Mode = "{" + at.Value + "}" + local
					break
				}
			}
		}
		// <param> children of <initial-mode> are parameters supplied to the
		// INITIAL TEMPLATE RULE (tunnel or not), exactly like those of
		// <initial-template> above — initial-mode-004.
		for _, p := range tc.Test.InitialMode.Params {
			name := p.Name
			if pfx, local, ok := strings.Cut(name, ":"); ok {
				for _, at := range p.Attr {
					if at.Name.Space == "xmlns" && at.Name.Local == pfx {
						name = "{" + at.Value + "}" + local
						break
					}
				}
			}
			e.TemplateParams = append(e.TemplateParams, xslt.EntryParam{
				Name:   name,
				Select: p.Select,
				Tunnel: p.Tunnel == "yes" || p.Tunnel == "true" || p.Tunnel == "1",
			})
		}
	}
	if tc.Test.ContextItem != nil {
		e.ContextItem = tc.Test.ContextItem.Select
	} else if srcSelect != "" {
		e.ContextItem = srcSelect
	}
	// <output file="..."> establishes the base output URI, resolved against
	// the test-set directory. "#absent" means explicitly none; an EMPTY file
	// resolves to the directory itself (current-output-uri-014). With no
	// <output> element at all the host supplies no base output URI, which is
	// the case for every test outside this one test-set.
	if tc.Test.Output != nil && tc.Test.Output.File != "#absent" {
		if abs, aerr := filepath.Abs(filepath.Join(dir, tc.Test.Output.File)); aerr == nil {
			if tc.Test.Output.File == "" {
				abs += "/"
			}
			e.BaseOutputURI = "file://" + abs
		}
	}

	var output string
	var runErr error
	var secondary []xslt.SecondaryDoc
	var messages []string
	var entryValue xpath.Object
	// The reference runner (runner/assert.xsl, c:assert) always evaluates an
	// assertion with $result bound to the transformation's result; the
	// catalog's result-var only renames it (on-empty-115b's $result/child::foo
	// declares no result-var at all).
	entryVar := "result"
	if tc.Test.Output != nil && strings.TrimSpace(tc.Test.Output.ResultVar) != "" {
		entryVar = strings.TrimSpace(tc.Test.Output.ResultVar)
	}
	if cerr != nil {
		runErr = cerr
	} else {
		rr, terr := ss.TransformEntry(srcXML, e, dir)
		if terr != nil {
			runErr = terr
		} else {
			output = treeAssertionOutput(tc, rr)
			secondary = rr.Secondary
			messages = rr.Messages
			entryValue = rr.Value
			if entryValue == nil && rr.Root != nil && tc.Test.InitialFunction == nil {
				// A template/mode entry point's raw result is the principal
				// result tree itself — one document node — which is what the
				// reference runner's assert-type/assert-count see for it
				// (seqtor-043b: assert-type document-node()).
				entryValue = xpath.NodeSet{rr.Root}
			}
		}
	}
	var assertSchema assertSchemaHooks
	if ss != nil {
		assertSchema.lookup, assertSchema.resolver = ss.SchemaComponentsForHost()
	}
	// The <result> element's own bindings sit between the test-set root's and
	// each assert's, and are inherited by every assertion beneath it.
	resultNS := withNSAttrs(fotsAssert{Attr: tc.Result.Attr}, setNSAttrs(set))
	return checkXSLTAssert(tc.Result.Children[0], output, runErr, dir, "", secondary, resultNS, messages, entryValue, entryVar, assertSchema)
}

// treeAssertionOutput returns the text the assertions should be checked
// against. Normally that is simply the serialized result.
//
// The exception is the json and adaptive output methods, which do not serialize
// a node tree at all — they render the result VALUE (a map becomes a JSON
// object, an element node becomes a JSON string containing its markup). A test
// that did not ask for serialization (<output serialize="yes"/> absent) is
// written against the RESULT TREE in the W3C's own runner — e.g. maps-017 sets
// method="json" but asserts /out = '...', which only makes sense against the
// tree — so for those the tree is serialized as XML instead, exactly as the
// reference runner's tree-valued assertions see it. Every other output method
// serializes its tree either way, so nothing else is affected.
func treeAssertionOutput(tc xsltCase, rr *xslt.RunResult) string {
	switch rr.Method {
	case "json", "adaptive":
	default:
		return rr.Output
	}
	if tc.Test.Output != nil && tc.Test.Output.Serialize == "yes" {
		return rr.Output
	}
	if rr.Root == nil {
		return rr.Output
	}
	return xmltree.Serialize(rr.Root, xmltree.SerializeOptions{Method: "xml"})
}

// nsMapResolver resolves namespace prefixes collected from xmlns:* attributes
// on an <assert>/<all-of>/<any-of> element and its ancestors in the catalog
// XML — some assertions (e.g. number-0403) use a prefix declared on a wrapper
// element rather than the individual <assert>.
type nsMapResolver map[string]string

func (m nsMapResolver) ResolveNS(prefix string) (string, bool) {
	uri, ok := m[prefix]
	return uri, ok
}

// withNSAttrs returns ns extended with any xmlns:* attributes declared
// directly on a (child bindings shadow the parent's, as in real XML scoping).
func withNSAttrs(a fotsAssert, ns map[string]string) map[string]string {
	var own map[string]string
	for _, at := range a.Attr {
		if at.Name.Space == "xmlns" {
			if own == nil {
				own = make(map[string]string, len(ns)+1)
				for k, v := range ns {
					own[k] = v
				}
			}
			own[at.Name.Local] = at.Value
		}
	}
	if own != nil {
		return own
	}
	return ns
}

// resolveXsltEnv returns the case's environment: the inline one, or — when the
// case references one by name (<environment ref="…"/>) — the matching test-set
// environment.
func resolveXsltEnv(tc xsltCase, set xsltSet) fotsEnv {
	if len(tc.Envs) > 0 {
		e := tc.Envs[0]
		if e.Ref == "" {
			return e
		}
		for _, se := range set.Envs {
			if se.Name == e.Ref {
				return se
			}
		}
		if se, ok := xsltCatEnvs[e.Ref]; ok {
			return se
		}
	}
	return fotsEnv{}
}

// xsltCatEnvs holds the catalog-level (shared) environments, populated once by
// TestXSLT30 before the parallel run (read-only during it).
var xsltCatEnvs = map[string]fotsEnv{}

// stripQuotes unwraps a simple string-literal param select ('x' or "x").
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// parseXMLLiteral recognizes an environment <source>'s select attribute of the
// form parse-xml('...') / parse-xml("...") — a source wholly synthesized from a
// string literal, with no file/content (id-043) — and returns the literal XML
// text. Anything more general (a select against inline <content>, e.g.
// select="/doc") is left to the caller.
func parseXMLLiteral(sel string) (string, bool) {
	sel = strings.TrimSpace(sel)
	const prefix = "parse-xml("
	if !strings.HasPrefix(sel, prefix) || !strings.HasSuffix(sel, ")") {
		return "", false
	}
	arg := strings.TrimSpace(sel[len(prefix) : len(sel)-1])
	if len(arg) < 2 || (arg[0] != '\'' && arg[0] != '"') || arg[len(arg)-1] != arg[0] {
		return "", false
	}
	return stripQuotes(arg), true
}

// ---- assertion checking ----

// assertSchemaHooks carries the STYLESHEET's imported schema components into
// assertion evaluation, so an <assert> naming schema-element(Q{...}name) can be
// parsed and evaluated at all. Both fields are nil unless the run is
// schema-aware and the stylesheet imported a schema, in which case every
// assertion is parsed and evaluated exactly as before.
type assertSchemaHooks struct {
	lookup   xpath.SchemaNameLookup
	resolver xpath.SchemaTypeResolver
}

func checkXSLTAssert(a fotsAssert, output string, runErr error, dir, baseURI string, secondary []xslt.SecondaryDoc, ns map[string]string, messages []string, entryValue xpath.Object, entryVar string, assertSchema assertSchemaHooks) (string, string) {
	ns = withNSAttrs(a, ns)
	kind := a.XMLName.Local
	switch kind {
	case "error":
		if runErr == nil {
			return "fail", "expected-error"
		}
		// Default: any error satisfies any expected code. Under
		// XSLT30_STRICTCODE the catalog's @code must actually match.
		if want := a.attr("code"); want != "" && xsltStrictErrorCodes() {
			if got := xsltErrorCode(runErr.Error()); got != want {
				if got == "" {
					got = "(uncatalogued)"
				}
				return "fail", "error-code " + got + " != " + want
			}
		}
		return "pass", ""
	case "assert-message":
		// Asserts that the transform output an xsl:message which, considered as
		// an XML document, satisfies the contained assertion. Any one message
		// may match; additional messages are allowed (catalog-schema.xsd:
		// "there is no way to assert the absence of a message").
		if len(a.Children) == 0 {
			if len(messages) > 0 {
				return "pass", ""
			}
			return "fail", "no-message"
		}
		for _, msg := range messages {
			// The message is the thing asserted on: no raw principal-result
			// value stands in for it (see the markup-free case of "assert").
			if st, _ := checkXSLTAssert(a.Children[0], msg, nil, dir, baseURI, secondary, ns, messages, nil, entryVar, assertSchema); st == "pass" {
				return "pass", ""
			}
		}
		return "fail", "assert-message"
	case "assert-warning":
		// The W3C suite's OWN reference runner (runner/assert.xsl) treats
		// c:assert-warning as unconditionally true — "Warning output cannot be
		// tested without vendor extensions". Matching it here rather than
		// failing every test that merely expects an optional warning.
		return "pass", ""
	case "assert-serialization-error":
		if runErr != nil {
			return "pass", ""
		}
		return "fail", "expected-serialization-error"
	case "all-of":
		for i, c := range a.Children {
			if st, reason := checkXSLTAssert(c, output, runErr, dir, baseURI, secondary, ns, messages, entryValue, entryVar, assertSchema); st != "pass" {
				if os.Getenv("XSLT30_DIFF") != "" {
					return "fail", fmt.Sprintf("all-of[%d] %s", i, reason)
				}
				return "fail", "all-of"
			}
		}
		return "pass", ""
	case "any-of":
		var reasons []string
		for _, c := range a.Children {
			st, rs := checkXSLTAssert(c, output, runErr, dir, baseURI, secondary, ns, messages, entryValue, entryVar, assertSchema)
			if st == "pass" {
				return "pass", ""
			}
			reasons = append(reasons, rs)
		}
		if os.Getenv("XSLT30_DIFF") != "" {
			return "fail", "any-of{" + strings.Join(reasons, " | ") + "}"
		}
		return "fail", "any-of"
	case "not":
		if len(a.Children) > 0 {
			if st, _ := checkXSLTAssert(a.Children[0], output, runErr, dir, baseURI, secondary, ns, messages, entryValue, entryVar, assertSchema); st == "pass" {
				return "fail", "not"
			}
			return "pass", ""
		}
		return "fail", "not"
	}
	if runErr != nil {
		if os.Getenv("XSLT30_ERRMSG") != "" {
			return "fail", "errored:" + runErr.Error()
		}
		return "fail", "errored"
	}
	switch kind {
	case "assert-xml":
		exp := assertExpected(a, dir)
		if xmlTreeEqual(exp, output, a.attr("normalize-space") == "true") {
			return "pass", ""
		}
		if os.Getenv("XSLT30_DIFF") != "" {
			return "fail", fmt.Sprintf("assert-xml WANT=%q GOT=%q", exp, output)
		}
		return "fail", "assert-xml"
	case "assert-serialization":
		exp := assertExpected(a, dir)
		if serialEqual(exp, output, a.attr("normalize-space") == "true") {
			return "pass", ""
		}
		if os.Getenv("XSLT30_DIFF") != "" {
			return "fail", fmt.Sprintf("assert-serialization WANT=%q GOT=%q", exp, output)
		}
		return "fail", "assert-serialization"
	case "serialization-matches":
		re, err := xpath.CompileRegex(strings.TrimSpace(a.Value), a.attr("flags"))
		if err != nil {
			return "fail", "bad-regex"
		}
		if re.MatchString(output) {
			return "pass", ""
		}
		if os.Getenv("XSLT30_DIFF") != "" {
			return "fail", fmt.Sprintf("serialization-matches RE=%q GOT=%q", a.Value, output)
		}
		return "fail", "serialization-matches"
	case "assert-string-value":
		want := a.Value
		got := output
		// The string value of the result is the concatenation of its text. When
		// the output serialises as XML, extract that; text output is its own value.
		if d, err := xmltree.Parse(wrapFragment(output)); err == nil {
			got = d.StringValue()
		}
		// Per the catalog schema, normalize-space defaults to "true" (only an
		// explicit "false" disables normalization for this assertion kind).
		if a.attr("normalize-space") != "false" {
			want = strings.Join(strings.Fields(want), " ")
			got = strings.Join(strings.Fields(got), " ")
		}
		if want == got {
			return "pass", ""
		}
		if os.Getenv("XSLT30_DIFF") != "" {
			return "fail", fmt.Sprintf("assert-string-value WANT=%q GOT=%q", want, got)
		}
		return "fail", "assert-string-value"
	case "assert-result-document":
		// Find the secondary document produced for this @uri (matched by exact
		// href or basename), then check the nested assertion against its content.
		want := a.attr("uri")
		sec, ok := findSecondary(secondary, want)
		if !ok {
			return "fail", "result-document-missing"
		}
		if len(a.Children) == 0 {
			return "pass", ""
		}
		// The nested assertion may query base-uri()/document-uri() (e.g.
		// result-document-0102: "ends-with(base-uri(/), '/out/second.xml')")
		// — resolve the secondary document's own href against the stylesheet's
		// directory the same way the engine resolves it, so re-parsing its
		// content for the assertion can carry that as its base URI.
		secBase := ""
		if p, err := filepath.Abs(filepath.Join(dir, sec.Href)); err == nil {
			secBase = "file://" + p
		}
		// An assertion ABOUT TYPE ANNOTATIONS inside assert-result-document
		// has the same problem the top-level "assert" case documents below:
		// sec.Content is the SERIALIZED secondary document, and serializing
		// provably discards the PSVI, so no reparse can answer "/in instance
		// of element(*, xs:decimal)" (si-result-document-116, whose whole
		// point is that xsl:result-document/@type validated the result).
		// sec.Root is that same document before serialization, so binding it
		// as the raw entry value lets the existing assertNeedsPSVI branch
		// reach it. Bound ONLY when the nested assertion actually asks a
		// type question, so every other assert-result-document — including
		// the assert-count/deep-eq/type trio, which read entryValue for a
		// different purpose entirely — behaves exactly as before.
		var secRaw xpath.Object
		if sec.Root != nil && sec.Root.Kind == xmltree.KindDocument && assertTreeNeedsPSVI(a.Children[0]) {
			secRaw = xpath.NodeSet{sec.Root}
		}
		return checkXSLTAssert(a.Children[0], sec.Content, nil, dir, secBase, secondary, ns, messages, secRaw, "", assertSchema)
	case "assert-eq":
		// The result, atomized, equals the given XPath value. For an XSLT
		// test the result is the serialized output, so its string value is
		// what gets compared (avt-0701, initial-function-101*).
		p, xerr := xpath.Parse(strings.TrimSpace(a.Value))
		if xerr != nil {
			return "fail", "assert-eq-bad-xpath"
		}
		v, eerr := p.Eval(&xpath.Context{NoFocus: true, NS: nsMapResolver(ns)})
		if eerr != nil {
			return "fail", "assert-eq-eval"
		}
		want := xpath.ToString(v)
		got := strings.TrimSpace(stripDecl(output))
		if d, err := parseAssertXML(output); err == nil {
			got = d.StringValue()
		}
		if want == got || strings.TrimSpace(want) == strings.TrimSpace(got) {
			return "pass", ""
		}
		if os.Getenv("XSLT30_DIFF") != "" {
			return "fail", fmt.Sprintf("assert-eq WANT=%q GOT=%q", want, got)
		}
		return "fail", "assert-eq"
	case "assert":
		// An assertion ABOUT TYPE ANNOTATIONS can only be answered by the RAW
		// result tree. Serialization provably discards the PSVI, so no reparse
		// of the output can ever satisfy "not(/* instance of element(*,
		// xs:untyped))" however correctly the transformation validated its
		// result (validation-1601..1607, whose stated purpose is exactly
		// "test whether the implicit result document is validated").
		//
		// Narrow ON PURPOSE. Switching EVERY assertion to the raw tree was
		// measured at +6/-10 and reverted (see the note below): ten cases
		// assert over what SERIALIZATION produced, which only the reparse
		// shows. Those ten were checked individually — none of them mentions a
		// type at all — so this rule and that set provably do not overlap, and
		// every other assertion keeps the reparse unchanged.
		if assertNeedsPSVI(a.Value) {
			if raw, ok := entryValue.(xpath.NodeSet); ok && len(raw) == 1 && raw[0].Kind == xmltree.KindDocument {
				return evalXSLTAssertOn(a, raw[0], output, baseURI, ns, entryValue, entryVar, assertSchema)
			}
		}
		// RESIDUAL, measured and deliberately not taken: asserting over the
		// raw result TREE instead of a reparse of its serialization would let
		// the type-annotation assertions pass (validation-1601..1607 and
		// friends assert "not(/* instance of element(*,xs:untyped))", which no
		// reparse can ever satisfy since serializing discards annotations) —
		// but it costs more than it wins: 10 currently-passing cases
		// (namespace-3314/4302, copy-5011/5012/5201, element-0306,
		// accumulator-041, si-copy-of-020, si-element-025/026) assert over
		// what SERIALIZATION produced, which only the reparse shows. Net −4,
		// so the reparse stays.
		if body := stripDecl(output); !strings.HasPrefix(body, "<") {
			// No markup at all (<output well-formed="no"/>: a bare text
			// result — whitespace-019 asserts /child::text() and a regex
			// over its untrimmed whitespace): the raw result tree bound as
			// entryValue IS that document node, so use it as-is.
			if raw, ok := entryValue.(xpath.NodeSet); ok && len(raw) == 1 && raw[0].Kind == xmltree.KindDocument {
				return evalXSLTAssertOn(a, raw[0], output, baseURI, ns, entryValue, entryVar, assertSchema)
			}
		}
		doc, perr := xmltree.Parse(strings.TrimSpace(stripDecl(output)))
		if perr != nil {
			doc, perr = xmltree.Parse(wrapFragment(output))
		}
		if perr != nil {
			// A stylesheet with no xsl:output whose root element is <html>
			// serializes under the HTML output method, where void elements
			// have no end tag — not XML, but the catalog still states its
			// expectation as an XPath assertion over the result
			// (unparsed-entity-50). Close the void tags so the assertion can
			// be evaluated at all.
			// Unwrapped first: an HTML result normally has a single <html>
			// root, and the assertions are written against it as the document
			// element ("/html/body/..."), which a wrapper would hide
			// (copy-2801).
			doc, perr = xmltree.Parse(strings.TrimSpace(stripDecl(closeHTMLVoidTags(output))))
			if perr != nil {
				doc, perr = xmltree.Parse(wrapFragment(closeHTMLVoidTags(output)))
			}
		}
		if perr != nil {
			// The same XML 1.1 / 5th-edition-name leniency parseAssertXML
			// gives assert-xml (xml-version-012 asserts over an element whose
			// name carries a U+0346 combining mark).
			doc, perr = xmltree.ParseLenient11(`<?xml version="1.1"?>` + strings.TrimSpace(stripDecl(output)))
			if perr != nil {
				doc, perr = xmltree.ParseLenient11(`<?xml version="1.1"?>` + wrapFragment(output))
			}
		}
		if perr != nil {
			// Not XML at all (<output well-formed="no"/>: a bare text
			// result — whitespace-019 asserts /child::text() on it): the
			// raw result tree bound as entryValue IS that document node.
			if ns, ok := entryValue.(xpath.NodeSet); ok && len(ns) == 1 && ns[0].Kind == xmltree.KindDocument {
				doc, perr = ns[0], nil
			}
		}
		if perr != nil {
			return "fail", "assert-parse"
		}
		return evalXSLTAssertOn(a, doc, output, baseURI, ns, entryValue, entryVar, assertSchema)

	case "assert-count", "assert-deep-eq", "assert-type":
		// These three assert on the RAW sequence the transformation returned,
		// which only an initial-FUNCTION entry point produces (<output
		// tree="no" serialize="no"/>). The serialized Output cannot answer
		// them: a two-item sequence is one joined string there.
		if entryValue == nil {
			return "skip", "assert:" + kind
		}
		return checkXSLTValueAssert(kind, strings.TrimSpace(a.Value), entryValue, ns)
	}
	return "skip", "assert:" + kind
}

// assertNeedsPSVI reports whether an <assert> expression asks a question that
// only a type-annotated (post-validation) tree can answer, and which a reparse
// of the serialized output therefore cannot answer for ANY implementation.
func assertNeedsPSVI(expr string) bool {
	norm := strings.Join(strings.Fields(expr), " ")
	for _, k := range []string{
		"instance of element(", "instance of attribute(",
		"instance of document-node(", "instance of schema-element(",
		"schema-element(", "schema-attribute(", "nilled(", "xs:untyped",
	} {
		if strings.Contains(norm, k) {
			return true
		}
	}
	return false
}

// assertTreeNeedsPSVI reports whether a (possibly nested all-of/any-of)
// assertion tree contains an <assert> whose expression only a type-annotated
// tree can answer. It is assertNeedsPSVI lifted over the catalog's grouping
// elements, since assert-result-document's own child is usually one of those.
func assertTreeNeedsPSVI(a fotsAssert) bool {
	if a.XMLName.Local == "assert" && assertNeedsPSVI(a.Value) {
		return true
	}
	for _, c := range a.Children {
		if assertTreeNeedsPSVI(c) {
			return true
		}
	}
	return false
}

// evalXSLTAssertOn evaluates an <assert> XPath against doc as the context node.
func evalXSLTAssertOn(a fotsAssert, doc *xmltree.Node, output, baseURI string, ns map[string]string, entryValue xpath.Object, entryVar string, assertSchema assertSchemaHooks) (string, string) {
	if baseURI != "" && doc.Base == "" {
		doc.Base = baseURI
	}
	// An assertion may name schema components the STYLESHEET imported
	// (schema-element(Q{...}name) — validation-1601..1607, -1705/1706): without
	// them it cannot even be PARSED, since schema-element() is XPST0008 with no
	// schema in scope. Both hooks are nil for a non-schema-aware run or a
	// stylesheet that imported nothing, and ParseSchemaAware is then exactly
	// xpath.Parse — so the ordinary case is untouched.
	names, types := assertSchema.lookup, assertSchema.resolver
	p, xerr := xpath.ParseSchemaAware(strings.TrimSpace(a.Value), names)
	if xerr != nil {
		return "fail", "assert-bad-xpath"
	}
	ectx := &xpath.Context{Node: doc, Pos: 1, Size: 1, NS: nsMapResolver(ns), SchemaTypes: types}
	if entryVar != "" {
		ectx.Vars = varMap{entryVar: entryValue}
	}
	v, eerr := p.Eval(ectx)
	if eerr != nil {
		return "fail", "assert-eval"
	}
	if xpath.ToBool(v) {
		return "pass", ""
	}
	if os.Getenv("XSLT30_DIFF") != "" {
		return "fail", fmt.Sprintf("assert %q GOT=%q", strings.TrimSpace(a.Value), output)
	}
	return "fail", "assert"

}

// assertExpected returns the expected output text: a referenced file or inline value.
//
// A file-referenced expectation has every CR stripped, exactly as the suite's own
// reference runner does (runner/assert.xsl:
//
//	select="if (@file) then translate(unparsed-text(...), '&#xd;', '') else string(.)"
//
// A number of the checked-in .out fixtures are stored with CRLF line terminators
// (verify with `file`), while XML line-end normalization means a conforming
// processor can only ever emit LF; without this the fixture's storage format,
// not the processor, decides the outcome.
func assertExpected(a fotsAssert, dir string) string {
	if f := a.attr("file"); f != "" {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			// Read exactly as fn:unparsed-text reads a resource: a leading
			// byte-order mark is dropped (date-061's .out fixture carries
			// one) and a non-UTF-8 file declaring its encoding is decoded to
			// characters (select-6101.out is ISO-8859-1, declared by the
			// assertion's own encoding attribute), so the comparison is
			// between character strings, as the reference runner's is.
			return strings.ReplaceAll(xmltree.DecodeRetrievedText(b), "\r", "")
		}
	}
	return a.Value
}

// xmlTreeEqual compares expected XML and actual serialized output as trees,
// tolerating the XML declaration and (optionally) insignificant whitespace.
func xmlTreeEqual(expected, actual string, normWS bool) bool {
	ed, err1 := parseAssertXML(expected)
	ad, err2 := parseAssertXML(actual)
	if err1 != nil || err2 != nil {
		return serialEqual(expected, actual, true)
	}
	return xsltNodeEqual(ed, ad, normWS)
}

// parseAssertXML parses an expected/actual result fragment into a tree. Some
// reference fixtures (and the outputs that must match them) legitimately carry
// XML 1.1-only content — C0 control characters written as character
// references, 5th-edition name characters — which the strict 1.0 parser
// rejects; those get a second attempt through the lenient 1.1 entry point
// (xml-version-023).
func parseAssertXML(s string) (*xmltree.Node, error) {
	d, err := xmltree.Parse(wrapFragment(s))
	if err == nil {
		return d, nil
	}
	return xmltree.ParseLenient11(`<?xml version="1.1"?>` + wrapFragment(s))
}

// xsltNodeEqual structurally compares two XML trees (element names, attributes,
// children in order, text/comment/PI values). Whitespace-only text-node
// CHILDREN (pure stylesheet-source indentation in the test author's
// hand-formatted <assert-xml> CDATA, e.g. as-1303/1304 — never present in our
// compact serialized GOT output) are always insignificant between element
// siblings and are ignored on both sides before comparing child lists; normWS
// (the assert's own normalize-space="true") additionally collapses internal
// whitespace runs within a genuine (non-blank) text/comment/PI value.
func xsltNodeEqual(a, b *xmltree.Node, normWS bool) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case xmltree.KindElement:
		if a.Name.Local != b.Name.Local || a.Name.Space != b.Name.Space {
			return false
		}
		if !xsltAttrsEqual(a, b) {
			return false
		}
		return xsltChildrenEqual(a, b, normWS)
	case xmltree.KindDocument:
		return xsltChildrenEqual(a, b, normWS)
	default: // text, comment, PI
		if normWS {
			return strings.Join(strings.Fields(a.Value), " ") == strings.Join(strings.Fields(b.Value), " ")
		}
		return a.Value == b.Value
	}
}

// isBlankText reports whether n is a text node containing only whitespace —
// insignificant stylesheet-source (or CDATA-fixture) indentation, never
// itself a meaningful sequence item.
func isBlankText(n *xmltree.Node) bool {
	return n.Kind == xmltree.KindText && strings.TrimSpace(n.Value) == ""
}

func xsltChildrenEqual(a, b *xmltree.Node, normWS bool) bool {
	var an, bn []*xmltree.Node
	for _, c := range a.Children {
		if !isBlankText(c) {
			an = append(an, c)
		}
	}
	for _, c := range b.Children {
		if !isBlankText(c) {
			bn = append(bn, c)
		}
	}
	if len(an) != len(bn) {
		return false
	}
	for i := range an {
		if !xsltNodeEqual(an[i], bn[i], normWS) {
			return false
		}
	}
	return true
}

func xsltAttrsEqual(a, b *xmltree.Node) bool {
	if len(a.Attrs) != len(b.Attrs) {
		return false
	}
	for _, aa := range a.Attrs {
		found := false
		for _, ba := range b.Attrs {
			if aa.Name.Local == ba.Name.Local && aa.Name.Space == ba.Name.Space {
				if aa.Value != ba.Value {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// findSecondary locates the xsl:result-document output matching the assertion's
// @uri. The engine's recorded href may be absolute or base-resolved, so match on
// the exact value, a suffix, or the basename.
func findSecondary(docs []xslt.SecondaryDoc, uri string) (xslt.SecondaryDoc, bool) {
	for _, d := range docs {
		if d.Href == uri || strings.HasSuffix(d.Href, "/"+uri) || filepath.Base(d.Href) == filepath.Base(uri) {
			return d, true
		}
	}
	return xslt.SecondaryDoc{}, false
}

// stripDecl removes a leading XML declaration.
func stripDecl(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<?xml") {
		if i := strings.Index(s, "?>"); i >= 0 {
			return strings.TrimSpace(s[i+2:])
		}
	}
	return s
}

// wrapFragment strips a leading XML declaration and wraps a possibly multi-root /
// text fragment so it parses as one tree.
func wrapFragment(s string) string {
	return "<_x_>" + stripDecl(s) + "</_x_>"
}

func serialEqual(expected, actual string, normWS bool) bool {
	e, a := expected, actual
	if normWS {
		e = strings.Join(strings.Fields(e), " ")
		a = strings.Join(strings.Fields(a), " ")
	} else {
		e, a = strings.TrimSpace(e), strings.TrimSpace(a)
	}
	return e == a
}

// ---- the test ----

func TestXSLT30(t *testing.T) {
	// Headroom so the engine's depth guards (which error out) fire before a
	// non-terminating stylesheet overflows the goroutine stack (unrecoverable).
	debug.SetMaxStack(2 << 30)
	root := filepath.Join(repoRoot(), "test", "conformance", "xslt30-test")
	catPath := filepath.Join(root, "catalog.xml")
	data, err := os.ReadFile(catPath)
	if err != nil {
		t.Skip("xslt30-test not present (git clone --depth 1 https://github.com/w3c/xslt30-test.git test/conformance/xslt30-test)")
	}
	var cat fotsCatalog
	if err := unmarshalFOTS(data, &cat); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, e := range cat.Envs {
		e.base = root
		xsltCatEnvs[e.Name] = e
	}
	only := os.Getenv("XSLT30_ONLY")

	var files []string
	for _, ts := range cat.TestSets {
		if only != "" && !strings.Contains(ts.Name, only) {
			continue
		}
		files = append(files, filepath.Join(root, ts.File))
	}

	cache := &ssCache{m: map[string]ssEntry{}}
	results := make([]*xsltResult, len(files))
	workers := runtime.NumCPU()
	if os.Getenv("XSLT30_SEQ") != "" {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f string) {
			defer wg.Done()
			defer func() { <-sem }()
			if os.Getenv("XSLT30_PROGRESS") != "" {
				fmt.Fprintln(os.Stderr, "SET", filepath.Base(filepath.Dir(f)))
			}
			results[i] = runXSLTSet(f, cache, only != "" || os.Getenv("XSLT30_FAILS") != "")
		}(i, f)
	}
	wg.Wait()

	var pass, fail, skip int
	skipReasons := map[string]int{}
	failBySet := map[string]int{}
	passBySet := map[string]int{}
	skipBySet := map[string]int{}
	passByVer := map[string]int{}
	failByVer := map[string]int{}
	var b strings.Builder
	for i, r := range results {
		if r == nil {
			continue
		}
		name := filepath.Base(filepath.Dir(files[i]))
		pass += r.pass
		fail += r.fail
		skip += r.skip
		passBySet[name] += r.pass
		failBySet[name] += r.fail
		skipBySet[name] += r.skip
		for k, v := range r.passByVer {
			passByVer[k] += v
		}
		for k, v := range r.failByVer {
			failByVer[k] += v
		}
		for k, v := range r.skipReasons {
			skipReasons[k] += v
		}
		for _, line := range r.fails {
			b.WriteString(line + "\n")
		}
	}
	if only != "" || os.Getenv("XSLT30_FAILS") != "" {
		fmt.Print(b.String())
	}

	ran := pass + fail
	rate := 0.0
	if ran > 0 {
		rate = 100 * float64(pass) / float64(ran)
	}
	t.Logf("XSLT30 results: %d cases | ran %d (pass %d, fail %d = %.1f%% pass) | skipped %d",
		pass+fail+skip, ran, pass, fail, rate, skip)

	if os.Getenv("XSLT30_BYVER") != "" {
		for _, v := range []string{"1.0", "2.0", "3.0", "xpath", "none"} {
			p, f := passByVer[v], failByVer[v]
			if p+f == 0 {
				continue
			}
			t.Logf("  spec %-5s ran %5d  pass %5d  fail %5d  = %.1f%% pass",
				v, p+f, p, f, 100*float64(p)/float64(p+f))
		}
	}

	if only == "" {
		var names []string
		for n := range passBySet {
			names = append(names, n)
		}
		for n := range failBySet {
			if _, ok := passBySet[n]; !ok {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		var csv strings.Builder
		csv.WriteString("set,pass,fail,skip\n")
		for _, n := range names {
			csv.WriteString(fmt.Sprintf("%s,%d,%d,%d\n", n, passBySet[n], failBySet[n], skipBySet[n]))
		}
		_ = os.WriteFile(filepath.Join(repoRoot(), "tools", "xslt30_byset.csv"), []byte(csv.String()), 0o644)
	}

	type kv struct {
		k string
		v int
	}
	var sr []kv
	for k, v := range skipReasons {
		sr = append(sr, kv{k, v})
	}
	sort.Slice(sr, func(i, j int) bool { return sr[i].v > sr[j].v })
	var srLines []string
	for _, e := range sr {
		srLines = append(srLines, fmt.Sprintf("%s=%d", e.k, e.v))
	}
	t.Logf("skip reasons: %s", strings.Join(srLines, " "))
}

// htmlVoidTagRE matches an unclosed HTML void element start tag.
var htmlVoidTagRE = regexp.MustCompile(`(?i)<(area|base|basefont|br|col|embed|frame|hr|img|input|isindex|link|meta|param|source|track|wbr)(\s[^<>]*[^<>/]|\s*)>`)

// closeHTMLVoidTags makes an HTML-serialized result re-parsable as XML so an
// XPath assertion can be evaluated over it: void element start tags become
// self-closing, and &nbsp; — the one HTML named entity the html/xhtml output
// methods emit, and one XML has no declaration for — becomes the equivalent
// numeric character reference (copy-2801 asserts over an HTML result whose
// cells contain U+00A0). This runs only after a straight parse has already
// failed, so it can never change how an already-parsable result is read.
func closeHTMLVoidTags(s string) string {
	s = strings.ReplaceAll(s, "&nbsp;", "&#xa0;")
	return htmlVoidTagRE.ReplaceAllString(s, `<$1$2/>`)
}

// checkXSLTValueAssert implements the catalog assertions that inspect the raw
// returned SEQUENCE rather than the serialized output: assert-count (the
// number of items), assert-deep-eq (fn:deep-equal against an expression) and
// assert-type (the sequence matches a SequenceType).
func checkXSLTValueAssert(kind, text string, value xpath.Object, ns map[string]string) (string, string) {
	items := xpath.Items(value)
	switch kind {
	case "assert-count":
		want, err := strconv.Atoi(text)
		if err != nil {
			return "skip", "assert:assert-count"
		}
		if len(items) == want {
			return "pass", ""
		}
		return "fail", fmt.Sprintf("assert-count want %d got %d", want, len(items))
	case "assert-deep-eq":
		// Compared with fn:deep-equal itself, by binding the returned sequence
		// to a variable and letting the engine answer — there is no exported
		// Go-level deep-equal, and reusing the real function keeps the
		// comparison's semantics (typed values, node identity) exact.
		p, err := xpath.Parse("deep-equal($__result, (" + text + "))")
		if err != nil {
			return "skip", "assert:assert-deep-eq"
		}
		got, err := p.Eval(&xpath.Context{
			NS:      nsMapResolver(ns),
			Vars:    varMap{"__result": value},
			NoFocus: true,
		})
		if err != nil {
			return "fail", "assert-deep-eq: " + err.Error()
		}
		if xpath.ToBool(got) {
			return "pass", ""
		}
		return "fail", fmt.Sprintf("assert-deep-eq %q got %q", text, xpath.ToString(value))
	case "assert-type":
		if _, ok := xpath.CoerceToDeclaredTypeCtx(text, value, nil); ok {
			return "pass", ""
		}
		return "fail", "assert-type " + text
	}
	return "skip", "assert:" + kind
}
