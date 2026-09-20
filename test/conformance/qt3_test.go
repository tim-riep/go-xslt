package conformance

// QT3 / FOTS harness: runs the W3C XPath/XQuery test suite (qt3tests) against
// our XPath engine. We are an XPath 3.1 processor (also embedding a real
// XSLT 3.0 one), not an XQuery processor, so the only category-level
// exclusion is a spec dependency our declared identity does not satisfy
// (specApplies below) — most often XQuery-only, but also an exact older
// XPath/XQuery version whose tested behavior 3.0/3.1 deliberately changed.
// Every other dependency (schema import/validation, module import, static
// typing, …) is attempted and scored honestly rather than skipped, so a
// genuine gap shows up as a fail, not a silently-inflated denominator.
//
// Clone the suite under test/conformance/qt3tests (the test skips if absent):
//   git clone --depth 1 https://github.com/w3c/qt3tests.git test/conformance/qt3tests
//
// Run:  go test ./test/conformance/ -run TestQT3 -v

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"github.com/tim-riep/go-xslt/internal/xsd"
)

// repoRoot is the module root relative to this test package (test/conformance).
func repoRoot() string { return filepath.Join("..", "..") }

// stressExpr matches expressions whose eager evaluation would blow up memory in
// our engine (large numeric ranges materialised as a slice), e.g. "1 to 1000000000".
var stressExpr = regexp.MustCompile(`(?:to\s*\d{7,})|(?:\d{7,}\s*to)`)

// safeRunCase runs a case with panic recovery. It runs inline (no goroutine) so
// the suite is deterministic: an earlier per-case timeout goroutine would leak
// and race the shared docCache, corrupting later results. Pathological inputs
// that would hang/OOM are pre-filtered (see stressExpr) rather than timed out.
func safeRunCase(tc fotsCase, env fotsEnv, loadDoc func(string, string) (*xmltree.Node, error)) (status, reason string) {
	defer func() {
		if e := recover(); e != nil {
			status, reason = "fail", "panic"
		}
	}()
	return runCase(tc, env, loadDoc)
}

// fotsCharset lets the XML decoder accept the us-ascii / iso-8859-1 encodings the
// FOTS files declare (Go's decoder errors on them without a CharsetReader).
func fotsCharset(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "iso-8859-1", "latin1":
		return &latin1Reader{r: input}, nil
	default: // us-ascii, utf-8, ascii — ASCII is a UTF-8 subset → passthrough
		return input, nil
	}
}

type latin1Reader struct {
	r   io.Reader
	buf []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(l.buf) > 0 {
		n := copy(p, l.buf)
		l.buf = l.buf[n:]
		return n, nil
	}
	tmp := make([]byte, len(p))
	n, err := l.r.Read(tmp)
	var out []byte
	for _, b := range tmp[:n] {
		if b < 0x80 {
			out = append(out, b)
		} else {
			out = append(out, 0xc0|b>>6, 0x80|b&0x3f)
		}
	}
	n2 := copy(p, out)
	l.buf = out[n2:]
	return n2, err
}

func unmarshalFOTS(b []byte, v any) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.CharsetReader = fotsCharset
	return dec.Decode(v)
}

// ---- FOTS catalog model (matched by local-name; the suite uses a default ns) ----

type fotsDep struct {
	Type      string `xml:"type,attr"`
	Value     string `xml:"value,attr"`
	Satisfied string `xml:"satisfied,attr"`
}
type fotsSource struct {
	Role       string `xml:"role,attr"`
	File       string `xml:"file,attr"`
	URI        string `xml:"uri,attr"`    // a fn:doc()-resolvable URI for this source
	Select     string `xml:"select,attr"` // an XPath constructing/selecting the source (e.g. parse-xml('...'))
	Validation string `xml:"validation,attr"`
	// Streaming is the xslt30-test catalog's <source streaming="true"/>: the
	// host is asked to supply this document as a STREAMED input. Read only by
	// the XSLT harness (see xsltSourceStreamed); the QT3 catalog never sets it.
	Streaming string `xml:"streaming,attr"`
	Content   string `xml:"content"` // inline source content (XSLT tests)
	// Version is a <test><package> element's own package-version attribute —
	// distinct from an <environment><package>'s (fotsPackage.Version): a
	// SECONDARY package named directly inside one <test> (rather than shared
	// via an environment) can pin the version xsl:use-package must match
	// (accept-901, package-016, override-t-003c and others all do), and
	// fotsSource otherwise has no field to receive it.
	Version string `xml:"package-version,attr"`
}
type fotsNS struct {
	Prefix string `xml:"prefix,attr"`
	URI    string `xml:"uri,attr"`
}
type fotsParam struct {
	Name   string `xml:"name,attr"`
	Select string `xml:"select,attr"`
}
type fotsResource struct {
	File      string `xml:"file,attr"`
	URI       string `xml:"uri,attr"`
	Encoding  string `xml:"encoding,attr"`   // the "HTTP header" encoding of the resource
	MediaType string `xml:"media-type,attr"` // its media type (text/plain, text/xml, ...)
}
type fotsEnv struct {
	Name        string         `xml:"name,attr"`
	Ref         string         `xml:"ref,attr"`
	Sources     []fotsSource   `xml:"source"`
	Stylesheets []fotsSource   `xml:"stylesheet"`
	Resources   []fotsResource `xml:"resource"`
	Namespaces  []fotsNS       `xml:"namespace"`
	Params      []fotsParam    `xml:"param"`
	StaticBase  struct {
		URI string `xml:"uri,attr"`
	} `xml:"static-base-uri"`
	Schemas []struct {
		URI string `xml:"uri,attr"`
		// File and Role are read by the XSLT30 runner only: an xslt30-test
		// environment supplies the schema its <source validation="strict">
		// is to be validated against as a FILE, with role="source-reference"
		// (or no role) for the entry document and role="secondary" for the
		// documents that one imports/includes.
		File string `xml:"file,attr"`
		Role string `xml:"role,attr"`
	} `xml:"schema"`
	DecimalFormats []fotsDecimalFormat `xml:"decimal-format"`
	Collections    []fotsCollection    `xml:"collection"`
	// Packages are the library packages an xslt30-test environment makes
	// available to xsl:use-package (always empty for QT3 environments).
	Packages []fotsPackage `xml:"package"`
	base     string        // dir to resolve source files against
}

// fotsPackage mirrors an xslt30-test environment's <package> element: a
// separately-compiled library package that xsl:use-package can name. @uri is
// the package NAME (not a retrieval URI); when absent the name and version
// come from the module's own xsl:package attributes.
type fotsPackage struct {
	File    string `xml:"file,attr"`
	Role    string `xml:"role,attr"`
	URI     string `xml:"uri,attr"`
	Version string `xml:"package-version,attr"`
}

// fotsCollection mirrors an environment <collection uri="..."> declaration and
// the <source>s that make up its content (uri="" is the default collection).
type fotsCollection struct {
	URI     string       `xml:"uri,attr"`
	Sources []fotsSource `xml:"source"`
}

// fotsDecimalFormat mirrors an environment <decimal-format> declaration; an
// empty Name is the default (unnamed) format used by format-number without a
// third argument.
type fotsDecimalFormat struct {
	Name              string     `xml:"name,attr"`
	DecimalSeparator  string     `xml:"decimal-separator,attr"`
	GroupingSeparator string     `xml:"grouping-separator,attr"`
	Percent           string     `xml:"percent,attr"`
	PerMille          string     `xml:"per-mille,attr"`
	ZeroDigit         string     `xml:"zero-digit,attr"`
	Digit             string     `xml:"digit,attr"`
	PatternSeparator  string     `xml:"pattern-separator,attr"`
	MinusSign         string     `xml:"minus-sign,attr"`
	Infinity          string     `xml:"infinity,attr"`
	NaN               string     `xml:"NaN,attr"`
	ExponentSeparator string     `xml:"exponent-separator,attr"`
	Attrs             []xml.Attr `xml:",any,attr"` // captures xmlns:* on the element itself
}

// buildDecimalFormats turns an environment's <decimal-format> declarations into
// the xpath.DecimalFormat map keyed by expanded {uri}local name ("" = default).
func buildDecimalFormats(env fotsEnv) map[string]xpath.DecimalFormat {
	if len(env.DecimalFormats) == 0 {
		return nil
	}
	firstRune := func(s string, def rune) rune {
		for _, r := range s {
			return r
		}
		return def
	}
	// Resolve a decimal-format name QName: prefixes bind from the element's own
	// xmlns:* attributes first, then the environment <namespace> declarations.
	expand := func(name string, own []xml.Attr) string {
		if name == "" {
			return ""
		}
		i := strings.IndexByte(name, ':')
		if i < 0 {
			return name
		}
		pre, local := name[:i], name[i+1:]
		for _, a := range own {
			if a.Name.Local == pre && (a.Name.Space == "xmlns" || a.Name.Space == "http://www.w3.org/2000/xmlns/") {
				return "{" + a.Value + "}" + local
			}
		}
		for _, n := range env.Namespaces {
			if n.Prefix == pre {
				return "{" + n.URI + "}" + local
			}
		}
		return name
	}
	m := map[string]xpath.DecimalFormat{}
	for _, d := range env.DecimalFormats {
		df := xpath.DefaultDecimalFormat()
		df.DecimalSep = firstRune(d.DecimalSeparator, df.DecimalSep)
		df.GroupingSep = firstRune(d.GroupingSeparator, df.GroupingSep)
		df.Percent = firstRune(d.Percent, df.Percent)
		df.PerMille = firstRune(d.PerMille, df.PerMille)
		df.ZeroDigit = firstRune(d.ZeroDigit, df.ZeroDigit)
		df.Digit = firstRune(d.Digit, df.Digit)
		df.PatternSep = firstRune(d.PatternSeparator, df.PatternSep)
		df.MinusSign = firstRune(d.MinusSign, df.MinusSign)
		df.ExponentSep = firstRune(d.ExponentSeparator, df.ExponentSep)
		if d.Infinity != "" {
			df.Infinity = d.Infinity
		}
		if d.NaN != "" {
			df.NaN = d.NaN
		}
		m[expand(d.Name, d.Attrs)] = df
	}
	return m
}

// ---- schema-awareness (env.Schemas) ----
//
// A FOTS environment's <schema> declarations name the components a <source
// validation="strict|lax"> is validated against, and the derivation
// questions element(N,T)/schema-element(N)/instance-of ask about the result.
// This mirrors, for the QT3 harness, exactly what internal/xslt/
// decl_import_schema.go + schema_validation.go already do for the XSLT
// harness — that pair is the reference implementation this is adapted from.
//
// Deliberately conditional throughout: an environment with no <schema>
// declarations compiles nothing (compileEnvSchema returns nil), leaves
// Context.SchemaTypes nil, and validates no source — byte-for-byte the old,
// non-schema-aware behaviour, which is every environment but the handful
// this round's assigned cases actually touch.

// envSchemaCache caches one compiled *xsd.Schema per environment (keyed by
// its schema files' absolute paths), since the SAME global catalog
// environment (e.g. "atomic", "qname") is resolved by hundreds of distinct
// test-cases across many test-sets running concurrently, and re-compiling a
// schema per case would be pure waste. A failed compile is cached as nil too
// (see compileEnvSchema), exactly like internal/xslt's own sourceSchemaCache.
var envSchemaCache sync.Map // joined absolute .xsd paths -> *xsd.Schema (nil cached too)

// compileEnvSchema compiles the schema documents an environment's <schema>
// elements declare, for use both by source-document validation and by
// Context.SchemaTypes / schema-aware parsing. Returns nil when the
// environment declares no schemas, or none of its declarations resolve to an
// actual document — which is the correct "no schema in scope" answer, not an
// error: §3.16-style namespace-only hints the harness cannot itself resolve
// are simply not offered.
//
// One entry is special-cased: a <schema uri="http://www.w3.org/2005/xpath-
// functions" .../> with NO @file (fn/json-to-xml.xml's "json-ns" environment
// — "the test driver is expected to recognize this URI") is answered from the
// qt3tests suite's OWN local copy of the F&O JSON schema (qt3JSONSchemaPath),
// mirroring xslt30_test.go's suiteJSONSchemaPath/hostSchemaFor: that schema is
// third-party normative material, so only a PATH into the gitignored suite
// checkout is ever committed, never its text.
func compileEnvSchema(env fotsEnv) *xsd.Schema {
	if len(env.Schemas) == 0 {
		return nil
	}
	var paths []string
	for _, sc := range env.Schemas {
		if sc.File == "" {
			if strings.TrimSpace(sc.URI) == fnJSONNamespace {
				if jp := qt3JSONSchemaPath(); jp != "" {
					paths = append(paths, jp)
				}
			}
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(env.base, sc.File)); err == nil {
			paths = append(paths, abs)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	key := strings.Join(paths, "\x00")
	if v, ok := envSchemaCache.Load(key); ok {
		s, _ := v.(*xsd.Schema)
		return s
	}
	var (
		docs []string
		base string
	)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		docs = append(docs, string(b))
		if base == "" {
			base = filepath.Dir(p)
		}
	}
	var sch *xsd.Schema
	if len(docs) > 0 {
		// XSD 1.1, the version this engine presents everywhere else (the
		// same choice internal/xslt/decl_import_schema.go and
		// schema_validation.go make for the XSLT harness).
		if s, err := xsd.Compile(docs, base, xsd.Version11); err == nil {
			sch = s
		}
	}
	envSchemaCache.Store(key, sch)
	return sch
}

// qt3JSONSchemaPath returns the qt3tests suite's own local copy of the F&O
// schema for the XML representation of JSON (see compileEnvSchema). Falls
// back to the sibling xslt30-test suite's copy — equally local test material,
// both already present under this repo's gitignored conformance folder — if
// the qt3tests copy has moved or is absent, and "" if neither is.
func qt3JSONSchemaPath() string {
	p := filepath.Join(repoRoot(), "test", "conformance", "qt3tests", "fn", "json-to-xml", "schema-for-json.xsd")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	p = filepath.Join(repoRoot(), "test", "conformance", "xslt30-test", "tests", "fn", "json-to-xml", "schema-for-json.xsd")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// qt3SchemaAdapter bridges one environment's compiled schema components to
// internal/xpath's injected-hook seam (SchemaNameLookup at parse time,
// SchemaTypeResolver and its optional extensions at match/cast time). It is
// internal/xslt/decl_import_schema.go's schemaAdapter adapted for a FOTS
// environment: prefix resolution goes through the flat nsMap this harness
// already builds from <namespace> declarations, since a QT3 test expression
// has no stylesheet element to resolve a prefix against.
type qt3SchemaAdapter struct {
	sch *xsd.Schema
	ns  nsMap
}

func (a *qt3SchemaAdapter) DerivesFrom(got, want xmltree.SchemaTypeName) bool {
	return a.sch.DerivesFrom(got, want)
}

// SubstitutesFor implements xpath.SchemaSubstitutionResolver.
func (a *qt3SchemaAdapter) SubstitutesFor(member, head xmltree.Name) bool {
	return a.sch.SubstitutesFor(member, head)
}

// ElementDeclType implements xpath.SchemaDeclTypeResolver.
func (a *qt3SchemaAdapter) ElementDeclType(name xmltree.Name) (xmltree.SchemaTypeName, bool) {
	return a.sch.ElementDeclared(name.Space, name.Local)
}

// CastToSchemaType/CastToSchemaTypeIn implement xpath.SchemaTypeCaster/
// SchemaTypeCasterIn.
func (a *qt3SchemaAdapter) CastToSchemaType(t xmltree.SchemaTypeName, lexical string) (xpath.AtomType, error) {
	return a.sch.CastToSchemaType(t, lexical)
}

func (a *qt3SchemaAdapter) CastToSchemaTypeIn(t xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (xpath.AtomType, error) {
	return a.sch.CastToSchemaTypeIn(t, lexical, at)
}

// CastToSchemaListType implements xpath.SchemaListTypeCaster.
func (a *qt3SchemaAdapter) CastToSchemaListType(t xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (
	xpath.AtomType, *xmltree.SchemaTypeName, bool, error) {
	return a.sch.CastToSchemaListType(t, lexical, at)
}

// ValidateSchemaDocument implements xpath.SchemaDocumentValidator: the
// episode fn:json-to-xml's `validate:true` option asks for (F&O 3.1 §17.5.1).
// Strict, exactly as internal/xslt/decl_import_schema.go's own
// ValidateSchemaDocument: a tree with no declaration in scope for its
// document element has not been validated against it.
func (a *qt3SchemaAdapter) ValidateSchemaDocument(doc *xmltree.Node) error {
	if a.sch == nil {
		return fmt.Errorf("no schema components are in scope")
	}
	return a.sch.ValidateNode(doc, xsd.NodeValidateOptions{Strict: true, Document: true, LenientEntities: true})
}

func (a *qt3SchemaAdapter) LookupSchemaType(lexical string) (xmltree.SchemaTypeName, bool) {
	ns, local, ok := a.expand(lexical)
	if !ok {
		return xmltree.SchemaTypeName{}, false
	}
	return a.sch.TypeByName(ns, local)
}

func (a *qt3SchemaAdapter) LookupSchemaElement(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool) {
	ns, local, ok := a.expand(lexical)
	if !ok {
		return xmltree.Name{}, xmltree.SchemaTypeName{}, false
	}
	t, found := a.sch.ElementDeclared(ns, local)
	return xmltree.Name{Space: ns, Local: local}, t, found
}

func (a *qt3SchemaAdapter) LookupSchemaAttribute(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool) {
	ns, local, ok := a.expand(lexical)
	if !ok {
		return xmltree.Name{}, xmltree.SchemaTypeName{}, false
	}
	t, found := a.sch.AttributeDeclared(ns, local)
	return xmltree.Name{Space: ns, Local: local}, t, found
}

// expand turns a lexical EQName into an expanded one, using the environment's
// flat namespace map — mirroring schemaAdapter.expand, but there is no
// default-element-namespace concept in a bare FOTS environment, so an
// unprefixed name stays in no namespace, exactly the plain XPath node-test
// rule (see this repo's own CLAUDE.md: "XPath name tests with no prefix
// match the no-namespace").
func (a *qt3SchemaAdapter) expand(lexical string) (ns, local string, ok bool) {
	lexical = strings.TrimSpace(lexical)
	if strings.HasPrefix(lexical, "Q{") {
		if end := strings.IndexByte(lexical, '}'); end > 0 {
			return strings.Join(strings.Fields(lexical[2:end]), " "), lexical[end+1:], true
		}
		return "", "", false
	}
	if i := strings.IndexByte(lexical, ':'); i > 0 {
		uri, found := a.ns.ResolveNS(lexical[:i])
		if !found {
			return "", "", false
		}
		return uri, lexical[i+1:], true
	}
	return "", lexical, true
}

// validateEnvSource validates a loaded source document in place against sch
// when the <source> element itself asked for it (validation="strict"|"lax"),
// annotating real xmltree.Node.SchemaType/TypeAnno/Nilled/ListTyped fields —
// the same ValidateNode bridge and options internal/xslt's
// validateSourceDocument uses for the XSLT harness's own primary input,
// including StripElementOnlyWhitespace (XSLT 3.0 §4.4's PSVI-construction
// rule: "elements ... defined ... to contain element-only content will have
// whitespace text nodes stripped" — several FOTS assertions, e.g.
// fn-normalize-space-23/fn-string-22, are written assuming exactly this).
//
// A validation failure is not reported here — sch is presumed valid for the
// documents these hand-picked environments pair it with, and on failure
// ValidateNode leaves the node exactly as it was handed over (see its own
// doc comment), so the source is simply left unannotated, same as when sch
// is nil.
func validateEnvSource(sch *xsd.Schema, mode string, d *xmltree.Node) {
	if sch == nil || d == nil {
		return
	}
	mode = strings.TrimSpace(mode)
	if mode != "strict" && mode != "lax" {
		return
	}
	root := xmltree.RootElement(d)
	if root == nil {
		return
	}
	_ = sch.ValidateNode(root, xsd.NodeValidateOptions{
		Strict: mode == "strict", Document: true,
		LenientEntities: true, StripElementOnlyWhitespace: true,
	})
}

// evalXPSchema is evalXP with the expression parsed schema-aware against sn —
// exactly evalXP when sn is nil (ParseSchemaAware's own contract), so this is
// a strict superset used only for the one call site (the test expression
// itself) that needs schema-element()/element(N, userType) to resolve;
// env.Params and every checkAssert assertion keep going through plain evalXP,
// since none of this round's cases need schema syntax there.
func evalXPSchema(expr string, ctx *xpath.Context, sn xpath.SchemaNameLookup) (xpath.Object, error) {
	p, err := xpath.ParseSchemaAware(expr, sn)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return p.Eval(ctx)
}

type fotsAssert struct {
	XMLName  xml.Name
	Attr     []xml.Attr   `xml:",any,attr"`
	Value    string       `xml:",chardata"`
	Children []fotsAssert `xml:",any"`
}

func (a fotsAssert) attr(name string) string {
	for _, at := range a.Attr {
		if at.Name.Local == name {
			return at.Value
		}
	}
	return ""
}

type fotsTest struct {
	File string `xml:"file,attr"`
	Expr string `xml:",chardata"`
}
type fotsCase struct {
	Name string    `xml:"name,attr"`
	Deps []fotsDep `xml:"dependency"`
	Env  []fotsEnv `xml:"environment"`
	Test fotsTest  `xml:"test"`
	Res  struct {
		Children []fotsAssert `xml:",any"`
	} `xml:"result"`
}
type fotsTestSet struct {
	Name  string     `xml:"name,attr"`
	Deps  []fotsDep  `xml:"dependency"`
	Envs  []fotsEnv  `xml:"environment"`
	Cases []fotsCase `xml:"test-case"`
}
type fotsCatalog struct {
	Envs     []fotsEnv `xml:"environment"`
	TestSets []struct {
		Name string `xml:"name,attr"`
		File string `xml:"file,attr"`
	} `xml:"test-set"`
}

// ---- resolvers ----

var defaultNS = map[string]string{
	"xs":    "http://www.w3.org/2001/XMLSchema",
	"fn":    "http://www.w3.org/2005/xpath-functions",
	"math":  "http://www.w3.org/2005/xpath-functions/math",
	"map":   "http://www.w3.org/2005/xpath-functions/map",
	"array": "http://www.w3.org/2005/xpath-functions/array",
	"err":   "http://www.w3.org/2005/xqt-errors",
	"xsi":   "http://www.w3.org/2001/XMLSchema-instance",
	"xml":   "http://www.w3.org/XML/1998/namespace",
}

type nsMap map[string]string

func (m nsMap) ResolveNS(p string) (string, bool) { u, ok := m[p]; return u, ok }

type varMap map[string]xpath.Object

func (m varMap) ResolveVar(prefix, local string) (xpath.Object, bool) {
	if v, ok := m[local]; ok {
		return v, true
	}
	return nil, false
}

// ---- dependency filtering ----

// engineSpecVersions declares which language identities this processor
// actually implements, as {family -> version}: a real XPath 3.1 processor
// (XP31), which also embeds a real XSLT 3.0 processor (XT30) — several F&O
// cases (fn:transform, and others) are tagged applicable to an XT30+ host
// specifically. No XQ (XQuery) entry: this engine has no XQuery grammar at
// all, so an XQ-only dependency never applies, at any version.
var engineSpecVersions = map[string]int{"XP": 31, "XT": 30}

// specToken splits a dependency token like "XP31+" into its family ("XP"),
// numeric version (31), and whether it is open-ended ("+", meaning "this
// version or later" rather than exactly this one).
func specToken(tok string) (family string, version int, open bool) {
	open = strings.HasSuffix(tok, "+")
	tok = strings.TrimSuffix(tok, "+")
	i := 0
	for i < len(tok) && (tok[i] < '0' || tok[i] > '9') {
		i++
	}
	family = tok[:i]
	for _, c := range tok[i:] {
		if c < '0' || c > '9' {
			return family, 0, open
		}
		version = version*10 + int(c-'0')
	}
	return family, version, open
}

// specApplies reports whether a spec dependency value (e.g. "XP31+ XQ31+"),
// a space-separated OR of tokens, admits this processor's actual declared
// identity (engineSpecVersions) — exactly, unless the token is open-ended.
// This is version-PRECISE, not just family-prefix matching: a token naming
// an EXACT older version we exceed (e.g. "XP30" with no "+") does not admit
// us, because a good few cases are written against behavior XPath 3.0/3.1
// deliberately changed (fn:tokenize/string-join/round gained new arities,
// "[true()]" went from a syntax error to a legal square array constructor)
// — running those against our newer, correctly-different behavior and
// calling the result a "fail" would be scoring the wrong thing, the same
// class of denominator dishonesty as ignoring xsd-version used to be.
func specApplies(value string) bool {
	for _, tok := range strings.Fields(value) {
		family, version, open := specToken(tok)
		have, ok := engineSpecVersions[family]
		if !ok {
			continue
		}
		if open && have >= version {
			return true
		}
		if !open && have == version {
			return true
		}
	}
	return false
}

// skipReason returns "" if the case should run, else a short reason. The
// only category excluded here is a spec dependency this processor's actual
// declared identity does not satisfy (see specApplies) — most commonly
// XQuery-only, but also an exact older XPath/XQuery version whose tested
// behavior a newer version deliberately superseded. Every other dependency
// (feature, xml-version, xsd-version, …) is left to actually run, so a real
// capability gap is scored as a fail rather than hidden behind a skip.
func skipReason(deps []fotsDep) string {
	for _, d := range deps {
		if d.Type == "spec" && d.Satisfied != "false" && !specApplies(d.Value) {
			return "spec-inapplicable"
		}
		// unicode-normalization-form satisfied="false" asks for a processor
		// that does NOT implement the named form (cbcl-fn-normalize-unicode-
		// 001a/006a, the "alternative version... for processors that do not
		// implement this normalization form" siblings of -001/-006) — the
		// same "wants a lesser processor than we are" shape as feat-not:
		// below. FULLY-NORMALIZED is the only value the catalog pairs with a
		// satisfied="false" variant at all; NFC/NFD/NFKC/NFKD never are.
		if d.Type == "unicode-normalization-form" && d.Satisfied == "false" && d.Value == "FULLY-NORMALIZED" {
			return "norm-form-not:" + d.Value
		}
	}
	return ""
}

// ---- the test ----

// setResult accumulates the outcome of one test-set.
type setResult struct {
	name             string
	pass, fail, skip int
	skipReasons      map[string]int
	fails            []string // "name [reason] expr / want" for QT3_ONLY logging
	sampleFails      []string // "set/name: reason"
}

// runSet evaluates a single test-set with its own doc cache (so workers don't
// share mutable state) and returns the aggregated result.
func runSet(root, file string, globalEnv map[string]fotsEnv, verbose bool) *setResult {
	r := &setResult{skipReasons: map[string]int{}}
	tb, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return r
	}
	var set fotsTestSet
	if err := unmarshalFOTS(tb, &set); err != nil {
		return r
	}
	r.name = set.Name
	tsDir := filepath.Dir(filepath.Join(root, file))
	localEnv := map[string]fotsEnv{}
	for _, e := range set.Envs {
		e.base = tsDir
		localEnv[e.Name] = e
	}
	docCache := map[string]*xmltree.Node{}
	loadDoc := func(base, f string) (*xmltree.Node, error) {
		p := filepath.Join(base, f)
		if d, ok := docCache[p]; ok {
			return d, nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		// Source documents may start with a byte-order mark (docs/auction.xml):
		// strip it, or it becomes a text node before the document element
		// (fn-string-31).
		d, err := xmltree.Parse(xmltree.DecodeBOM(string(b)))
		if err != nil {
			return nil, err
		}
		docCache[p] = d
		return d, nil
	}
	resolveEnv := func(refs []fotsEnv) (fotsEnv, bool) {
		if len(refs) == 0 {
			return fotsEnv{base: tsDir}, true
		}
		ref := refs[0]
		if ref.Ref != "" {
			if e, ok := localEnv[ref.Ref]; ok {
				return e, true
			}
			if e, ok := globalEnv[ref.Ref]; ok {
				return e, true
			}
			return fotsEnv{}, false
		}
		ref.base = tsDir
		return ref, true
	}

	for _, tc := range set.Cases {
		deps := append(append([]fotsDep{}, set.Deps...), tc.Deps...)
		if reason := skipReason(deps); reason != "" {
			r.skip++
			r.skipReasons[reason]++
			continue
		}
		env, ok := resolveEnv(tc.Env)
		if !ok {
			r.skip++
			r.skipReasons["env-missing"]++
			continue
		}
		// tc.Deps is replaced with the test-set+test-case UNION so runCase
		// can see a test-SET-level dependency too (prod-AxisStep.static-
		// typing declares `feature value="staticTyping"` once, at the
		// set — not the case — level).
		tc.Deps = deps
		status, reason := safeRunCase(tc, env, loadDoc)
		switch status {
		case "pass":
			r.pass++
		case "skip":
			r.skip++
			r.skipReasons[reason]++
		default:
			r.fail++
			if verbose {
				ex := tc.Test.Expr
				if len(ex) > 90 {
					ex = ex[:90]
				}
				want := ""
				if len(tc.Res.Children) > 0 {
					want = tc.Res.Children[0].XMLName.Local + ":" + strings.TrimSpace(tc.Res.Children[0].Value)
				}
				r.fails = append(r.fails, fmt.Sprintf("FAIL %s [%s] expr=%q want=%q", tc.Name, reason, ex, want))
			} else if len(r.sampleFails) < 3 {
				r.sampleFails = append(r.sampleFails, fmt.Sprintf("%s/%s: %s", set.Name, tc.Name, reason))
			}
		}
	}
	return r
}

func TestQT3(t *testing.T) {
	root := filepath.Join(repoRoot(), "test", "conformance", "qt3tests")
	catPath := filepath.Join(root, "catalog.xml")
	data, err := os.ReadFile(catPath)
	if err != nil {
		t.Skip("qt3tests not present (git clone https://github.com/w3c/qt3tests.git test/conformance/qt3tests)")
	}
	var cat fotsCatalog
	if err := unmarshalFOTS(data, &cat); err != nil {
		t.Fatalf("parse catalog: %v", err)
	}

	globalEnv := map[string]fotsEnv{}
	for _, e := range cat.Envs {
		e.base = root
		globalEnv[e.Name] = e
	}

	only := os.Getenv("QT3_ONLY")
	var files []string
	for _, ts := range cat.TestSets {
		if only != "" && !strings.Contains(ts.Name, only) {
			continue
		}
		files = append(files, ts.File)
	}

	// Run test-sets concurrently; each runSet uses its own doc cache, and the
	// XPath engine's registries are read-only after init, so this is race-free.
	results := make([]*setResult, len(files))
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if os.Getenv("QT3_SEQ") != "" {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f string) {
			defer wg.Done()
			defer func() { <-sem }()
			if os.Getenv("QT3_PROGRESS") != "" {
				fmt.Fprintln(os.Stderr, "SET", f)
			}
			results[i] = runSet(root, f, globalEnv, only != "" || os.Getenv("QT3_FAILDUMP") != "")
		}(i, f)
	}
	wg.Wait()

	var pass, fail, skip int
	skipReasons := map[string]int{}
	failBySet := map[string]int{}
	passBySet := map[string]int{}
	skipBySet := map[string]int{}
	var sampleFails []string
	for _, r := range results {
		if r == nil {
			continue
		}
		pass += r.pass
		fail += r.fail
		skip += r.skip
		if r.name != "" {
			passBySet[r.name] += r.pass
			failBySet[r.name] += r.fail
			skipBySet[r.name] += r.skip
		}
		for k, v := range r.skipReasons {
			skipReasons[k] += v
		}
		if len(sampleFails) < 40 {
			sampleFails = append(sampleFails, r.sampleFails...)
		}
		for _, line := range r.fails {
			t.Log(line)
		}
	}
	total := pass + fail + skip

	ran := pass + fail
	rate := 0.0
	if ran > 0 {
		rate = 100 * float64(pass) / float64(ran)
	}
	t.Logf("QT3 results: %d cases | ran %d (pass %d, fail %d = %.1f%% pass) | skipped %d",
		total, ran, pass, fail, rate, skip)

	// Per-set CSV for tracking conformance progress across fixes.
	if os.Getenv("QT3_ONLY") == "" {
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
		var b strings.Builder
		b.WriteString("set,pass,fail,skip\n")
		for _, n := range names {
			b.WriteString(fmt.Sprintf("%s,%d,%d,%d\n", n, passBySet[n], failBySet[n], skipBySet[n]))
		}
		_ = os.WriteFile(filepath.Join(repoRoot(), "tools", "qt3_byset.csv"), []byte(b.String()), 0644)
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

	var fs []kv
	for k, v := range failBySet {
		fs = append(fs, kv{k, v})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].v > fs[j].v })
	var top []string
	for i, e := range fs {
		if i >= 25 {
			break
		}
		top = append(top, fmt.Sprintf("%s=%d", e.k, e.v))
	}
	t.Logf("top failing test-sets: %s", strings.Join(top, " "))
	for _, s := range sampleFails {
		t.Logf("  FAIL %s", s)
	}
}

// fotsResolver resolves the URIs declared by an environment's <resource> elements
// (and relative/file URIs) to local file content for fn:unparsed-text / fn:doc.
type fotsResolver struct {
	base     string
	byURI    map[string]string        // resource uri -> absolute file path
	encoding map[string]string        // resource uri -> declared encoding (may be "")
	docs     map[string]*xmltree.Node // fn:doc cache: the same URI yields the SAME node (fn-doc-5)
	// collections holds the environment's <collection> declarations: the
	// collection URI ("" = the default collection) -> the absolute paths of
	// its member documents.
	collections map[string][]string
}

func newFotsResolver(env fotsEnv) *fotsResolver {
	r := &fotsResolver{base: env.base, byURI: map[string]string{}, encoding: map[string]string{}, docs: map[string]*xmltree.Node{}}
	for _, res := range env.Resources {
		if res.URI == "" || res.File == "" {
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(env.base, res.File)); err == nil {
			r.byURI[res.URI] = abs
			r.encoding[res.URI] = strings.ToLower(res.Encoding)
		}
	}
	// A <source> may carry a uri, making it retrievable via fn:doc()/fn:collection.
	// Every source is also retrievable under its own file: URI, which is what
	// document-uri(/) reports (fn-doc-available-5).
	for _, src := range env.Sources {
		if src.File == "" {
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(env.base, src.File)); err == nil {
			if src.URI != "" {
				r.byURI[src.URI] = abs
			}
			r.byURI["file://"+abs] = abs
		}
	}
	for _, col := range env.Collections {
		var paths []string
		for _, src := range col.Sources {
			if src.File == "" {
				continue
			}
			if abs, err := filepath.Abs(filepath.Join(env.base, src.File)); err == nil {
				paths = append(paths, abs)
				r.byURI["file://"+abs] = abs
			}
		}
		if r.collections == nil {
			r.collections = map[string][]string{}
		}
		r.collections[col.URI] = paths
	}
	return r
}

// ResolveCollection implements xpath.CollectionResolver over the
// environment's <collection> declarations. Member documents go through the
// same fn:doc cache, so a collection is stable across repeated calls.
func (r *fotsResolver) ResolveCollection(uri string) ([]*xmltree.Node, bool) {
	paths, ok := r.collections[uri]
	if !ok {
		return nil, false
	}
	out := make([]*xmltree.Node, 0, len(paths))
	for _, p := range paths {
		if d, ok := r.docAt(p); ok {
			out = append(out, d)
		}
	}
	return out, true
}

// TransformBaseDir implements the optional hint internal/xslt's fn:transform
// bridge checks for via a type assertion (mirroring how CollectionResolver is
// an optional ResourceResolver extension): a real filesystem directory to
// resolve a relative stylesheet-location/stylesheet-base-uri against when the
// calling expression has no usable static base URI of its own (e.g. the QT3
// catalog's environments without a "." source, such as "works-mod-local" —
// fn-transform-25's "stylesheet-location":"transform/render.xsl", or an
// empty environment — fn-transform-err-9a's relative stylesheet-base-uri).
func (r *fotsResolver) TransformBaseDir() string { return r.base }

// path resolves a (already base-resolved, absolute) URI to a local file, using
// ONLY the environment's declared <resource> map. Filesystem path-guessing is
// intentionally avoided: a URI not in the map is genuinely unavailable, which is
// what the tests assert.
func (r *fotsResolver) path(uri string) (string, bool) {
	p, ok := r.byURI[uri]
	return p, ok
}

// textPath is path() plus, for fn:unparsed-text only, a relative reference
// resolved against the environment's base — the test-set directory, which is
// the static base URI of a test without a <source> (fn-parse-json-101..105:
// unparsed-text('parse-json/data001.json')). fn:doc keeps the declared-only
// rule: encoding/xml tolerates top-level text, so a resolvable non-XML file
// would otherwise count as an available document (fn-doc-available-8).
func (r *fotsResolver) textPath(uri string) (string, bool) {
	if p, ok := r.path(uri); ok {
		return p, true
	}
	rel := uri
	// A relative reference resolved against a non-empty static base URI (see
	// baseCtx's own env.base fallback) arrives here already absolute — as a
	// "file:" URI whose path is exactly base-relative — rather than as the
	// bare relative string this fallback originally matched only when the
	// static base URI was empty (fn-parse-json-101..105/parse-xml-001/
	// parse-xml-fragment-001: unparsed-text("../docs/atomic.xml") and
	// similar). Recover the bare relative-to-base form so the fallback below
	// still applies uniformly whichever way the URI got here. r.base itself
	// may be relative (e.g. the test binary's own "../../…" working-
	// directory-relative root) while the "file:" URI's path is always
	// absolute, so filepath.Rel needs both sides made absolute first, or it
	// simply fails to relate them.
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" && u.Path != "" {
		if absBase, aerr := filepath.Abs(r.base); aerr == nil {
			if relPath, rerr := filepath.Rel(absBase, u.Path); rerr == nil {
				rel = filepath.ToSlash(relPath)
			}
		}
	}
	if r.base != "" && rel != "" && !strings.Contains(rel, ":") && !strings.HasPrefix(rel, "/") {
		p := filepath.Join(r.base, filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func (r *fotsResolver) ResolveText(uri string) (string, bool) {
	p, ok := r.textPath(uri)
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	// The resource's declared encoding plays the role of the HTTP
	// Content-Type charset (fn-unparsed-text-045/048: iso-8859-1 resources).
	switch r.encoding[uri] {
	case "iso-8859-1", "latin1", "latin-1":
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		return string(rs), true
	}
	return decodeUnparsedText(b), true
}

// decodeUnparsedText decodes a resource's bytes to a string, honouring a
// leading UTF-16/UTF-8 byte-order mark (the unparsed-text corpus is a mix of
// UTF-8, UTF-16LE-BOM and UTF-16BE-BOM files).
func decodeUnparsedText(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], binary.BigEndian)
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], binary.LittleEndian)
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return string(b[3:])
	default:
		return string(b)
	}
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

func (r *fotsResolver) ResolveDoc(uri string) (*xmltree.Node, bool) {
	if p, ok := r.path(uri); ok {
		return r.docAt(p)
	}
	// path() is deliberately declared-sources-only (see its own comment) —
	// but a URI that already resolved to a real "file:" location (because
	// the calling expression's static base URI is now the test-set's own
	// directory — see baseCtx — rather than empty) genuinely does denote a
	// file on disk. Needed for fn-function-lookup-766a's
	// doc("function-lookup/collection-1.xml") with no declared
	// <environment> at all. xmltree.Parse is lenient enough to accept
	// arbitrary non-XML text as a rootless "document" with no error at all
	// (fn-doc-available-8's own ".xq" module parses that way), so docAt's
	// error check alone is not the well-formedness gate path() normally
	// relies on for a declared, presumed-XML source — a real ROOT ELEMENT
	// is additionally required here, exactly the condition path()'s
	// declared sources never had to prove because they are only ever
	// pre-registered when they ARE real XML.
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" && u.Path != "" {
		if d, ok := r.docAt(u.Path); ok && xmltree.RootElement(d) != nil {
			return d, true
		}
	}
	return nil, false
}

// docAt parses (and caches) the document at an absolute local path.
func (r *fotsResolver) docAt(p string) (*xmltree.Node, bool) {
	// fn:doc is stable: the same URI returns the same document node within
	// one evaluation (fn-doc-5/18/19/22: doc($uri) is doc($uri)).
	if d, ok := r.docs[p]; ok {
		return d, true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	doc, err := xmltree.Parse(strings.TrimPrefix(string(b), "\ufeff"))
	if err != nil {
		return nil, false
	}
	r.docs[p] = doc
	return doc, true
}

// runCase runs one test-case and returns ("pass"|"fail"|"skip", reason).
func runCase(tc fotsCase, env fotsEnv, loadDoc func(string, string) (*xmltree.Node, error)) (string, string) {
	// namespaces
	ns := nsMap{}
	for k, v := range defaultNS {
		ns[k] = v
	}
	// defaultElemNS: a catalog environment's own <namespace prefix="" .../>
	// is the FOTS catalog's way of declaring the static xpath-default-
	// namespace for that environment's test expressions (fn-local-name-
	// from-QName/fn-namespace-uri-from-QName's "qname" environment relies
	// on this — /root/elemQN only selects anything once the unprefixed name
	// test resolves against it) — distinct from ns[""], which the engine's
	// name-test resolution never consults (an unprefixed name test always
	// means "no namespace" unless DefaultElemNS says otherwise).
	defaultElemNS := ""
	for _, n := range env.Namespaces {
		ns[n.Prefix] = n.URI
		if n.Prefix == "" {
			defaultElemNS = n.URI
		}
	}
	// variables from params, plus context doc
	vars := varMap{}
	var ctxNode *xmltree.Node
	// envSch is nil for every environment without <schema> declarations —
	// the overwhelming majority — which keeps every case below byte-for-byte
	// on the old, non-schema-aware path: no compile attempted, no source
	// validated, Context.SchemaTypes left nil, the test expression parsed by
	// plain xpath.Parse exactly as before. See "schema-awareness" above.
	envSch := compileEnvSchema(env)
	for _, s := range env.Sources {
		d, err := loadDoc(env.base, s.File)
		if err != nil {
			return "skip", "src-load"
		}
		if envSch != nil {
			validateEnvSource(envSch, s.Validation, d)
		}
		switch {
		case s.Role == ".":
			ctxNode = d
		case strings.HasPrefix(s.Role, "$"):
			// A variable-bound source needs a real base URI: fn:transform's
			// stylesheet-node/stylesheet-base-uri options (fn-transform-19/
			// 23/24/41/42) resolve relative xsl:include hrefs and report
			// static-base-uri() against exactly this. Prefer the source's own
			// declared uri= (the catalog's canonical identity for it, e.g.
			// "http://www.w3.org/fots/fn/transform/staticbaseuri.xsl") over
			// the real on-disk path, matching what a real fn:doc-style
			// retrieval would report. Scoped to "$"-bound sources only — the
			// far more heavily exercised "." context document is untouched,
			// so this cannot affect base-uri(.)/document-uri(.) anywhere else
			// in the suite.
			if d.Base == "" {
				if s.URI != "" {
					d.Base = s.URI
				} else if abs, aerr := filepath.Abs(filepath.Join(env.base, s.File)); aerr == nil {
					d.Base = "file://" + abs
				}
			}
			vars[s.Role[1:]] = xpath.NodeSet{d}
		}
	}
	var schemaTypes xpath.SchemaTypeResolver
	var schemaLookup xpath.SchemaNameLookup
	if envSch != nil {
		adapter := &qt3SchemaAdapter{sch: envSch, ns: ns}
		schemaTypes, schemaLookup = adapter, adapter
	}
	resolver := newFotsResolver(env)
	baseURI := env.StaticBase.URI
	if baseURI == "" {
		for _, s := range env.Sources {
			if s.Role == "." {
				if abs, err := filepath.Abs(filepath.Join(env.base, s.File)); err == nil {
					baseURI = "file://" + abs
				}
			}
		}
		if baseURI == "" && env.base != "" {
			// Neither an explicit <static-base-uri> nor a "." source: fall
			// back to the test-set's own directory, matching what a real
			// host naturally gives an expression it evaluates from a
			// particular catalog file — several fn-transform cases compute
			// options (base-output-uri, stylesheet-base-uri) via
			// resolve-uri(…, static-base-uri()) and need a real answer, not
			// the empty sequence, to behave as the catalog's own authors
			// evidently assumed (fn-transform-33/37/38/43/44 all resolve a
			// relative sandbox path against exactly this).
			if abs, err := filepath.Abs(env.base); err == nil {
				if !strings.HasSuffix(abs, "/") {
					abs += "/"
				}
				baseURI = "file://" + abs
			}
		}
	}
	decFmts := buildDecimalFormats(env)
	// xmlVersion honors the case's own dependency type="xml-version" (a
	// case-level dep, not just test-set-level — see codepoints-to-string's
	// paired 1.0/1.1 cases): defaults to "1.0" when absent, matching a
	// conventional processor's default identity.
	xmlVersion := ""
	xsdVersion := ""
	bc10 := false
	defaultLanguage := ""
	for _, d := range tc.Deps {
		switch {
		case d.Type == "xml-version" && d.Satisfied != "false":
			xmlVersion = d.Value
		case d.Type == "xsd-version" && d.Satisfied != "false":
			xsdVersion = d.Value
		case d.Type == "feature" && d.Value == "xpath-1.0-compatibility" && d.Satisfied != "false":
			bc10 = true
		case d.Type == "default-language" && d.Satisfied != "false":
			// A HOST CONFIGURATION dependency (default-language-006): the
			// catalog asks the processor to be configured with this default
			// language, checked via fn:default-language(). Not a language
			// feature test's own subject matter, just a knob the harness sets.
			defaultLanguage = d.Value
		}
	}
	baseCtx := func() *xpath.Context {
		// An environment without a "." source (the catalog's "empty" env) has
		// NO focus: '.', position() and the context-defaulting functions raise
		// XPDY0002 there (fn-local-name-1a/73, fn-name-23, fn-node-name-32).
		return &xpath.Context{Node: ctxNode, NoFocus: ctxNode == nil, Pos: 1, Size: 1, NS: ns, Vars: vars, Resolver: resolver, BaseURI: baseURI, DecimalFormats: decFmts, SchemaTypes: schemaTypes, DefaultElemNS: defaultElemNS, XMLVersion: xmlVersion, XSDVersion: xsdVersion, BC10: bc10, DefaultLanguage: defaultLanguage}
	}
	for _, p := range env.Params {
		if p.Select == "" {
			continue
		}
		v, err := evalXP(p.Select, baseCtx())
		if err == nil {
			vars[p.Name] = v
		}
	}

	// the test expression
	expr := tc.Test.Expr
	if tc.Test.File != "" {
		b, err := os.ReadFile(filepath.Join(env.base, tc.Test.File))
		if err != nil {
			return "skip", "test-file"
		}
		expr = string(b)
	}
	if strings.Contains(expr, "declare ") || strings.Contains(expr, "import ") {
		return "skip", "xquery-prolog"
	}
	if stressExpr.MatchString(expr) {
		return "skip", "stress-range"
	}

	// A case (or its test-set) declaring `dependency type="feature"
	// value="staticTyping"` explicitly OPTS IN to XPath 3.1's optional
	// Static Typing Feature (prod-AxisStep.static-typing's ST-Axes001..015:
	// "/attribute::*", "//center/self::nowhere", … are provable, by axis+
	// node-test shape alone, to always select nothing, so XTST0005 applies
	// — but ONLY under that feature; every other caller of this engine
	// (XSLT included) keeps ordinary Parse, where the identical shape
	// legitimately evaluates to an empty sequence rather than erroring).
	staticTyping := false
	for _, d := range tc.Deps {
		if d.Type == "feature" && d.Value == "staticTyping" && d.Satisfied != "false" {
			staticTyping = true
		}
	}
	var result xpath.Object
	var resErr error
	if staticTyping {
		var p *xpath.Parsed
		if p, resErr = xpath.ParseStaticTyping(expr); resErr == nil {
			result, resErr = p.Eval(baseCtx())
		}
	} else {
		// Schema-aware parsing is only needed for the test expression itself:
		// it is the one place these cases write schema-element()/element(N,
		// userType) syntax (json-to-xml-017/037/038's own `instance of
		// document-node(schema-element(...))`. With schemaLookup nil — every
		// environment without <schema> declarations — this is exactly evalXP
		// (ParseSchemaAware's own contract).
		result, resErr = evalXPSchema(expr, baseCtx(), schemaLookup)
	}

	if len(tc.Res.Children) == 0 {
		return "skip", "no-assertion"
	}
	st, reason := checkAssert(tc.Res.Children[0], result, resErr, ns, vars)
	return st, reason
}

func evalXP(expr string, ctx *xpath.Context) (xpath.Object, error) {
	p, err := xpath.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return p.Eval(ctx)
}

// checkAssert evaluates a FOTS assertion against the test result.
// Returns ("pass"|"fail"|"skip", reason).
func checkAssert(a fotsAssert, result xpath.Object, resErr error, ns nsMap, vars varMap) (string, string) {
	kind := a.XMLName.Local

	// Build a context that binds $result for assertion expressions.
	withResult := func() *xpath.Context {
		v := varMap{}
		for k, val := range vars {
			v[k] = val
		}
		if resErr == nil {
			v["result"] = result
		}
		return &xpath.Context{Pos: 1, Size: 1, NS: ns, Vars: v}
	}
	// evalBool evaluates an XPath predicate with $result bound.
	evalBool := func(expr string) (bool, bool) {
		o, err := evalXP(expr, withResult())
		if err != nil {
			return false, false
		}
		return xpath.ToBool(o), true
	}
	pass := func(b bool) (string, string) {
		if b {
			return "pass", ""
		}
		return "fail", kind
	}

	switch kind {
	case "all-of":
		for _, c := range a.Children {
			if st, r := checkAssert(c, result, resErr, ns, vars); st != "pass" {
				return st, r
			}
		}
		return "pass", ""
	case "any-of":
		anySkip := ""
		for _, c := range a.Children {
			st, r := checkAssert(c, result, resErr, ns, vars)
			if st == "pass" {
				return "pass", ""
			}
			if st == "skip" {
				anySkip = r
			}
		}
		if anySkip != "" {
			return "skip", anySkip
		}
		return "fail", "any-of"
	case "not":
		if len(a.Children) == 1 {
			st, r := checkAssert(a.Children[0], result, resErr, ns, vars)
			if st == "skip" {
				return "skip", r
			}
			return pass(st != "pass")
		}
		return "skip", "not"

	case "error":
		return pass(resErr != nil)

	case "assert-true":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool("$result eq true()")
		return pass(ok && b)
	case "assert-false":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool("$result eq false()")
		return pass(ok && b)
	case "assert-empty":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool("empty($result)")
		return pass(ok && b)
	case "assert-count":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool(fmt.Sprintf("count($result) eq %s", strings.TrimSpace(a.Value)))
		return pass(ok && b)
	case "assert-type":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool(fmt.Sprintf("$result instance of %s", strings.TrimSpace(a.Value)))
		if !ok {
			return "skip", "type-parse"
		}
		return pass(b)
	case "assert-eq":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool(fmt.Sprintf("$result eq (%s)", a.Value))
		if !ok {
			return "skip", "eq-eval"
		}
		return pass(b)
	case "assert-deep-eq":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool(fmt.Sprintf("deep-equal($result, (%s))", a.Value))
		if !ok {
			return "skip", "deepeq-eval"
		}
		return pass(b)
	case "assert-string-value":
		if resErr != nil {
			return "fail", "errored"
		}
		want := a.Value
		got, err := evalXP("string-join(for $r in $result return string($r), ' ')", withResult())
		if err != nil {
			return "fail", "strval"
		}
		gs := xpath.ToString(got)
		if a.attr("normalize-space") == "true" {
			gs = strings.Join(strings.Fields(gs), " ")
			want = strings.Join(strings.Fields(want), " ")
		}
		return pass(gs == want)
	case "assert":
		if resErr != nil {
			return "fail", "errored"
		}
		b, ok := evalBool(a.Value)
		if !ok {
			return "skip", "assert-eval"
		}
		return pass(b)

	case "assert-xml", "assert-serialization", "serialization-matches":
		return "skip", "serialization"
	case "assert-serialization-error":
		return "skip", "ser-error"
	case "assert-permutation":
		return "skip", "permutation"
	}
	return "skip", "unknown:" + kind
}
