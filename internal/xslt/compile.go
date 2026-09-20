// Package xslt compiles XSLT stylesheets into an instruction tree and executes
// transformations against a source document. It targets a useful subset of
// XSLT 1.0/3.0 (template rules, modes, the common instructions, AVTs) built on
// the internal xpath evaluator. Coverage grows toward full XSLT 3.0 over time.
package xslt

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// NS is the XSLT namespace URI.
const NS = "http://www.w3.org/1999/XSL/Transform"

// xmlnsURI is the reserved namespace URI XML itself uses for xmlns
// declarations: never a legal namespace for a constructed name (XTDE0835/
// XTDE0865/XTDE0905 — namespace-2621/2622/2623).
const xmlnsURI = "http://www.w3.org/2000/xmlns/"

// xpathFunctionsNS is the standard XPath/XQuery function namespace: XSLT's
// own no-prefix "instruction" functions (document, key, current, …) are
// formally members of it too, so an explicit fn: prefix bound here also
// resolves them (document-0301).
const xpathFunctionsNS = "http://www.w3.org/2005/xpath-functions"

// xsNS is the XML Schema namespace URI (xs: type constructor names).
const xsNS = "http://www.w3.org/2001/XMLSchema"

// isReservedNS reports whether uri is one of the namespaces XSLT 3.0 §3.5.2
// reserves — a user-defined xsl:function (XTSE0080), variable, or
// attribute-set name may not use one (function-1021: xs:test).
func isReservedNS(uri string) bool {
	switch uri {
	case NS, xsNS, "http://www.w3.org/2001/XMLSchema-instance",
		xpathFunctionsNS, "http://www.w3.org/2005/xpath-functions/math",
		"http://www.w3.org/2005/xpath-functions/map",
		"http://www.w3.org/2005/xpath-functions/array",
		"http://www.w3.org/XML/1998/namespace":
		return true
	}
	return false
}

// CompileError carries a source line/column.
type CompileError struct {
	Line, Col int
	Msg       string
}

func (e *CompileError) Error() string { return e.Msg }

func errAt(el *xmltree.Node, format string, args ...any) *CompileError {
	e := &CompileError{Msg: fmt.Sprintf(format, args...)}
	if el != nil {
		e.Line, e.Col = el.Line, el.Col
	}
	return e
}

// checkPatternGroupingFuncs rejects a match pattern that statically calls
// current-group() (XTSE1060) or current-grouping-key() (XTSE1070): neither
// has a defined value in the context a pattern is evaluated in, so the error
// applies regardless of whether the pattern is ever matched against a node.
func checkPatternGroupingFuncs(el *xmltree.Node, pat *xpath.Pattern) error {
	if pat == nil {
		return nil
	}
	if err := pat.CheckGrammar(); err != nil {
		return errAt(el, "%v", err)
	}
	if pat.UsesFunction("current-group") {
		return errAt(el, "err:XTSE1060: current-group() may not be used within a pattern")
	}
	if pat.UsesFunction("current-grouping-key") {
		return errAt(el, "err:XTSE1070: current-grouping-key() may not be used within a pattern")
	}
	if pat.UsesFunction("current-merge-group") {
		return errAt(el, "err:XTSE3470: current-merge-group() may not be used within a pattern")
	}
	if pat.UsesFunction("current-merge-key") {
		return errAt(el, "err:XTSE3500: current-merge-key() may not be used within a pattern")
	}
	return nil
}

// Output captures xsl:output settings.
type Output struct {
	Method             string
	Indent             bool
	OmitXMLDeclaration bool
	Encoding           string
	UseCharacterMaps   []string // xsl:output/@use-character-maps (QName list)
	DoctypePublic      string   // xsl:output/@doctype-public
	DoctypeSystem      string   // xsl:output/@doctype-system
	// NoEscapeURIAttributes is the negated sense of xsl:output/@escape-uri-attributes
	// (default "yes") so the zero value keeps the spec default (escaping ON)
	// without every Output/SerializeOptions literal having to opt in.
	NoEscapeURIAttributes bool
	// Standalone is xsl:output/@standalone ("yes"/"no"); "" means omitted
	// (the spec default — no standalone pseudo-attribute is written).
	Standalone string
	// MediaType is xsl:output/@media-type; "" keeps the serializer's
	// method-based default (text/html) for the html/xhtml content-type meta.
	MediaType string
	// NoContentType is the negated sense of xsl:output/@include-content-type
	// (default "yes") — same zero-value-keeps-default pattern as
	// NoEscapeURIAttributes.
	NoContentType bool
	// CDATASectionElements / SuppressIndentation are xsl:output/@cdata-section-elements
	// and @suppress-indentation: whitespace-separated QName lists.
	CDATASectionElements []xmltree.Name
	SuppressIndentation  []xmltree.Name
	// HTMLVersion is xsl:output/@html-version.
	HTMLVersion string
	// ByteOrderMark is xsl:output/@byte-order-mark: a leading U+FEFF is
	// prepended to the serialized output when set.
	ByteOrderMark bool
	// Version is xsl:output/@version (the serialization version, e.g. an XML
	// version "1.0"/"1.1" or an HTML version); "" means unspecified.
	Version string
	// NormalizationForm is xsl:output/@normalization-form ("" / "none" /
	// "NFC" / "NFD" / "NFKC" / "NFKD" are the ones this serializer accepts;
	// anything else is a serialization error, SESU0011).
	NormalizationForm string
	// UndeclarePrefixes is xsl:output/@undeclare-prefixes.
	UndeclarePrefixes bool
	// InlineCharMap holds character mappings that came from a serialization
	// PARAMETER DOCUMENT (xsl:output/@parameter-document), which spells out
	// output:character-map entries inline instead of naming an
	// xsl:character-map declaration. It is merged with the named maps at
	// serialization time.
	InlineCharMap map[rune]string
	// ItemSeparator / HasItemSeparator are xsl:output/@item-separator: the
	// string written between adjacent ITEMS of the result sequence during
	// serialization (sequence normalization). When one is in force the result
	// root is built with NoAtomicMerge so adjacent atomic items stay discrete
	// instead of being merged with the default single space.
	ItemSeparator    string
	HasItemSeparator bool
	// BuildTree is xsl:output/@build-tree (an XSLT-level attribute, not a
	// serialization parameter): "" leaves it unspecified, in which case the
	// method decides — see outputIsRaw.
	BuildTree string
	// JSONNodeOutputMethod is json-node-output-method: the output method used
	// to render a NODE that appears inside a value serialized by the json
	// output method. "" means the spec default, xml.
	JSONNodeOutputMethod string
	// AllowDuplicateNames is allow-duplicate-names: whether two map keys whose
	// string values coincide may produce duplicate JSON object names instead
	// of raising SERE0022.
	AllowDuplicateNames bool
}

// outputIsRaw reports whether this output definition must collect the result as
// a RAW ITEM SEQUENCE rather than as result-tree content.
//
// Only the two value-serializing output methods need that: json and adaptive
// render maps, arrays, function items and typed atomic values by their own
// rules, so each must survive collection intact rather than being flattened
// into text. XSLT 3.0's @build-tree defaults to "no" for exactly those two
// methods, and an explicit build-tree="yes" opts back out.
//
// build-tree="no" under a NODE-TREE method (xml/html/xhtml/text) deliberately
// does NOT switch this on: there the raw sequence still goes through the
// serializer's own sequence normalization, which joins adjacent atomic items
// with a space — which is precisely what this engine models during collection
// (result-document-0305: build-tree="no" method="xml" over "1 to 2" must still
// serialize as "1 2", not "12"). Its other observable effect, the
// item-separator, is handled by Output.HasItemSeparator independently.
func outputIsRaw(o Output) bool {
	if o.Method != "json" && o.Method != "adaptive" {
		return false
	}
	switch strings.TrimSpace(o.BuildTree) {
	case "yes", "true", "1":
		return false
	}
	return true
}

// yesTrue widens an xsl:output boolean-style attribute value: XSLT 1.0/2.0
// only recognize "yes", but XSLT 3.0 relaxed several of these (indent,
// omit-xml-declaration, ...) to accept the xs:boolean lexical forms too
// (output-0106a/0110a/0110b/0116b/0148a use indent="true"/"0" etc. under
// version="3.0"). Accepting them unconditionally is safe: a 1.0/2.0
// stylesheet has no legitimate reason to rely on "true" being silently
// treated as false.
func yesTrue(v string) bool {
	switch strings.TrimSpace(v) {
	case "yes", "true", "1":
		return true
	default:
		return false
	}
}

// VarDef is a variable or parameter definition (top-level or local).
type VarDef struct {
	name          xmltree.Name
	sel           *xpath.Parsed
	body          []instruction
	el            *xmltree.Node
	isParam       bool
	tunnel        bool   // xsl:param/with-param tunnel="yes"
	as            string // @as sequence type: content form yields the raw sequence, not a doc fragment
	requiredParam bool   // xsl:param required="yes" (XTDE0700 when not supplied)
	importPrec    int    // for global duplicate detection (XTSE0630)
	// staticVal/hasStatic carry the value the static pass settled for a
	// static="yes" declaration. It is the variable's value at run time too —
	// re-evaluating @select then would resolve a name against the DYNAMIC
	// declarations, where a non-static variable of the same name and higher
	// precedence shadows the static one the static expression bound to
	// (static-027: q="{$p+10}" is 11, not 10).
	staticVal xpath.Object
	hasStatic bool
}

// Template is a compiled template rule.
type Template struct {
	pattern     *xpath.Pattern // compiled match pattern (nil for named-only templates)
	matchSrc    string
	name        string
	mode        string
	priority    float64
	hasPriority bool
	body        []instruction
	params      []*VarDef
	el          *xmltree.Node
	order       int
	importPrec  int      // import precedence (higher = wins); from the module
	importLo    int      // lowest precedence this template's module imports (see modChildren.importLo)
	modeToks    []string // parsed @mode list (nil when mode is a single plain name)
	as          string   // @as declared return sequence type (XTTE0505 on mismatch)
	// visibility is @visibility as written ("" = the default, private). Only
	// consulted when a template is selected as the entry point ACROSS a
	// package boundary (fn:transform's package-based invocation).
	visibility string
	// ctxItem is the template's xsl:context-item declaration (XSLT 3.0 §6.4),
	// lifted out of the body by splitContextItem; nil when it has none.
	ctxItem *contextItemDecl
}

// Stylesheet is a compiled stylesheet.
type Stylesheet struct {
	// packages is the host-supplied library-package registry this stylesheet
	// was compiled with; fn:transform's package-based invocation resolves
	// against the same set (transform-005/006).
	packages    []PackageSource
	templates   []*Template
	named       map[string]*Template
	namedClark  map[string]*Template // expanded-QName key, for XTSE0660 duplicate detection only
	output      Output
	globals     []*VarDef
	strip       []spaceTest // xsl:strip-space element name-tests
	preserve    []spaceTest // xsl:preserve-space element name-tests
	keys        []*KeyDef
	functions   map[string]*FuncDef            // by funcKey(name, arity) — see funcKey
	modes       map[string]*ModeDef            // mode name ("" = unnamed) -> EFFECTIVE declaration (populated by resolveModeDecls)
	modeDecls   map[string][]*ModeDef          // every raw xsl:mode declaration, by name, before precedence resolution
	attrSets    map[string][]attrSetDecl       // xsl:attribute-set name -> its declarations, in document order
	decFmts     map[string]xpath.DecimalFormat // xsl:decimal-format name ("" = default) — final, resolved values (checkDecimalFormatConflicts)
	decFmtDecls map[string][]decFmtDecl        // every raw declaration seen, by name, before precedence resolution
	charMaps    map[string]*xmltree.Node       // xsl:character-map declarations by name (resolved lazily)
	// schema is the components xsl:import-schema brought in, nil when none
	// were (or when the run is not schema-aware). Every OTHER consumer reaches
	// them through schemaForElement, keyed by the stylesheet element an
	// expression is written on; this field exists for source-document
	// validation, which has no such element.
	schema *importedSchema
	// stripInputTypes is input-type-annotations="strip" (XSLT 3.0 §3.11),
	// declared on ANY module of the package: the transformation then sees
	// every input document as untyped regardless of what the host supplied.
	// Only observable once something can actually annotate input — see
	// validateSourceDocument.
	stripInputTypes     bool
	namedOutputs        map[string]Output // named xsl:output declarations (for xsl:result-document/@format)
	globalCtxUse        string            // xsl:global-context-item/@use ("" = no declaration)
	hasXPathDefaultNS   bool              // any module uses xpath-default-namespace (skip the walk otherwise)
	hasXMLBase          bool              // any module uses xml:base
	hasDefaultCollation bool              // any module uses [xsl:]default-collation (skip the walk otherwise)
	// hasBackwardsCompat records whether ANY module carries a [xsl:]version
	// attribute below 2.0 anywhere in the tree (including the stylesheet
	// root's own @version, which is how the whole ~2000-case XSLT 1.0 default
	// suite is written) — so eval's per-node inBackwardsCompatScope walk is
	// skipped entirely for a stylesheet that could never trigger it. Genuine
	// 1.0-compatible RELAXATIONS still only apply when backwardsCompatRun()
	// also claims the capability; this flag alone changes no behaviour.
	hasBackwardsCompat bool
	// principalVersionIsOne records whether the PRINCIPAL stylesheet
	// module's own outermost element declares @version="1.0" EXACTLY (not
	// merely <2.0 — XSLT 3.0's default-output-method carve-out for backward-
	// compatible processing, quoted verbatim in the "backwards" test-set's
	// own -019/-019b comment, names the value 1.0 specifically). Consulted
	// only under backwardsCompatRun() (transform.go's implicit-xhtml-result
	// default-method override); false changes nothing.
	principalVersionIsOne bool
	charMapPrec         map[string]int    // xsl:character-map: name -> import precedence it was declared at (XTSE1580)
	// hasOutputDecl records whether the stylesheet has at least one unnamed
	// xsl:output declaration, even one that sets no @method. It distinguishes
	// "an xsl:output element is present" (character-map-017: include-
	// content-type still applies to an auto-detected xhtml/html method) from
	// "no xsl:output at all" (the legacy auto-detect heuristic used by
	// bug-1301/1901/2401/select-6201/sequence-0601, which must NOT gain the
	// content-type meta).
	hasOutputDecl bool
	// defaultMode is the DEFAULT INITIAL MODE: the default mode in scope for
	// the principal stylesheet module's root element ([xsl:]default-mode,
	// XSLT 3.0). "" (the unnamed mode) when the attribute is absent, which is
	// the pre-3.0 behaviour. Used only when the caller does not name an
	// initial mode itself.
	defaultMode string
	// isPackage records whether the compiled module's OWN root element was
	// literally xsl:package, as opposed to xsl:stylesheet/xsl:transform (an
	// "implicit package" per XSLT 3.0 §3.5.1). Gates the one default that
	// genuinely differs between the two: an xsl:mode declaration's default
	// @visibility is "private" (XSLT 3.0's own xsl:mode attribute table), but
	// only inside a real xsl:package — an ordinary top-level stylesheet's
	// modes stay unenforced here for backward compatibility with pre-3.0
	// processors that had no visibility concept at all (mode-1801/1902 name
	// an unstated-visibility mode as their initial mode and must still
	// succeed). See its one read, in TransformEntry's initial-mode check.
	isPackage bool
	// exposedIgnored records the symbolic names ("kind#clarkName", e.g.
	// "template#{uri}local") an xsl:expose element NAMED, when that element
	// itself was dropped by forwards-compatible processing (a validly-placed
	// but version-unrecognized xsl:expose — xsl:expose is otherwise rejected
	// outright as an unsupported optional feature, see decls.go). xsl:expose
	// is exactly the mechanism XSLT 3.0 gives for granting a component
	// visibility WITHOUT annotating the component itself, so a named
	// template this engine cannot fully honor such a declaration for must
	// not be enforced as private-by-default either (forwards-011: a
	// same-shaped, but version-recognized, xsl:expose is the ONLY thing
	// that would make its "go" template eligible as an initial template —
	// dropping the declaration silently while still enforcing the
	// component's bare default would make an unimplemented optional
	// feature look like a hard rejection instead of the graceful
	// non-effect forwards-compatible processing promises). Consulted only
	// by the one check this narrowly needs — see its use in TransformEntry.
	exposedIgnored map[string]bool
}

// attrSetDecl is one xsl:attribute-set declaration's own use-attribute-sets
// and body. Two xsl:attribute-set elements sharing a name are merged as
// SEPARATE UNITS in document order (each one's own use-attribute-sets
// expanded immediately before its own body), not by globally hoisting every
// declaration's use-attribute-sets ahead of every declaration's body —
// attribute-set-1512 pins down the exact interleaving this produces.
type attrSetDecl struct {
	uses []string
	body []instruction
	// streamable records the declaration's own streamable="yes", which
	// XTSE0730 makes binding on every set it references.
	streamable bool
}

// KeyDef is a compiled xsl:key declaration. Its value is produced either by a
// @use expression or (when @use is absent) a sequence-constructor body — the
// same select-attribute-or-content pattern xsl:variable/xsl:param/xsl:sort
// use (key-073/074: a body of xsl:for-each/xsl:sequence computing the values).
type KeyDef struct {
	name         string
	match        *xpath.Pattern
	use          *xpath.Parsed
	body         []instruction
	composite    bool // @composite="yes"/"1"/"true": the use value's items form ONE compound tuple key, not a multi-value key (key-096/097)
	hasComposite bool // whether @composite was explicitly specified (XTSE1222)
	collation    string
	hasCollation bool
	collURI      string // resolved @collation, else in-scope default-collation ("" = codepoint)
	el           *xmltree.Node
}

// FuncDef is a compiled xsl:function (user-defined function).
type FuncDef struct {
	name       xmltree.Name
	params     []*VarDef
	body       []instruction
	el         *xmltree.Node
	as         string // @as declared return sequence type (XTTE0780 on mismatch)
	importPrec int    // import precedence (XTSE0770: a same-name/arity redeclaration at the SAME precedence is an error; higher precedence legitimately overrides)
	// visibility is @visibility as written ("" = the default, private). It
	// gates the two places where a function must be PUBLIC or FINAL to be
	// reachable: the static context of xsl:evaluate (XTDE3160) and selection
	// as the initial entry function (XTDE0041).
	visibility string
	// memoize marks a function the stylesheet has declared repeatable:
	// new-each-time="no" (two calls with the same arguments must return the
	// SAME nodes, not merely equal ones) or cache="yes" (a request to reuse
	// results). Both make the result a pure function of the arguments, so it
	// is computed once per distinct argument tuple — see engine.funcMemo.
	memoize bool
}

// instruction is one compiled template-body item.
type instruction interface{ instr() }

// execer is implemented by registry-based instructions (instr_*.go). When an
// instruction implements it, execInstr dispatches here instead of the legacy
// type switch.
type execer interface {
	exec(eng *engine, r rt, out *xmltree.Node) error
}

// instrRegistry maps an xsl: instruction local-name to its compiler. Each
// instr_<name>.go registers via init(); compileElement checks this before the
// legacy switch. This is a package-var literal so init() additions are safe.
var instrRegistry = map[string]func(c *compiler, el *xmltree.Node) (instruction, error){}

type litElement struct {
	name    xmltree.Name
	attrs   []litAttr
	ns      []*xmltree.Node
	body    []instruction
	useSets []string
	el      *xmltree.Node // the stylesheet element itself, for its in-scope namespaces (avt-2101)
	// val is the xsl:validation / xsl:type request (XSLT 3.0 §24.4), nil
	// unless this run is schema-aware — see schema_validation.go.
	val *valRequest
}
type litAttr struct {
	name  xmltree.Name
	value *avt
}
type litText struct{ text string }

type valueOf struct {
	sel *xpath.Parsed
	el  *xmltree.Node
	// @separator, an attribute value template. When absent (nil) the default
	// separator of XSLT 3.0 §5.8.2 applies: a single space for the @select
	// form, a zero-length string for the sequence-constructor form
	// (attribute-set-1811 g/h, construct-node-012).
	sep  *avt
	doe  bool          // disable-output-escaping="yes"
	body []instruction // when @select is absent (3.0 content form)
}
type applyTemplates struct {
	sel    *xpath.Parsed
	mode   string
	sorts  []sortKey
	params []*VarDef
	el     *xmltree.Node
}
type forEach struct {
	sel   *xpath.Parsed
	sorts []sortKey
	body  []instruction
	el    *xmltree.Node
}
type ifInstr struct {
	test *xpath.Parsed
	body []instruction
	el   *xmltree.Node
}
type chooseInstr struct {
	whens     []whenClause
	otherwise []instruction
}
type whenClause struct {
	test *xpath.Parsed
	body []instruction
	el   *xmltree.Node
}
type localVar struct {
	def *VarDef
	// unused marks a variable no later sibling (or its descendants) can
	// reference, so it is never evaluated: XSLT evaluates variables lazily,
	// and param-0301 relies on an unreferenced $b := $x inside a function
	// that $x itself calls NOT tripping the circularity check. It is set
	// ONLY for a select that is a bare variable reference — evaluating any
	// richer expression can surface an error the suite expects reported
	// (maps-901..906: type errors in an otherwise-unused map variable) —
	// and refs, the names such a select mentions, must all resolve in
	// scope, so an undeclared or self-referencing $x (error-XPST0008b) is
	// still raised by evaluating it.
	unused bool
	refs   []string
}
type callTemplate struct {
	name   string
	params []*VarDef
	el     *xmltree.Node
}
type attrInstr struct {
	name *avt
	ns   *avt
	sel  *xpath.Parsed // @select (XSLT 2.0+): mutually exclusive with body
	sep  *avt          // @separator (XSLT 3.0): joins a @select sequence; default " "
	body []instruction
	el   *xmltree.Node
	// val is the [xsl:]validation / [xsl:]type request — see litElement.val.
	val *valRequest
}
type elemInstr struct {
	name    *avt
	ns      *avt
	body    []instruction
	el      *xmltree.Node
	useSets []string
	// noInheritNS is inherit-namespaces="no": the element children this
	// instruction constructs must not inherit its namespace declarations
	// (xmltree.Node.NSBarrier).
	noInheritNS bool
	// val is the [xsl:]validation / [xsl:]type request — see litElement.val.
	val *valRequest
}
type textInstr struct {
	text string
	doe  bool // disable-output-escaping="yes"
}
type copyInstr struct {
	body    []instruction
	el      *xmltree.Node
	useSets []string
	sel     *xpath.Parsed // @select (XSLT 3.0+): the item to copy, overriding the context item
	// noInheritNS is inherit-namespaces="no" — see elemInstr.noInheritNS.
	noInheritNS bool
	// noCopyNS is copy-namespaces="no" — see copyOf.noCopyNS.
	noCopyNS bool
	// val is the [xsl:]validation / [xsl:]type request — see litElement.val.
	val *valRequest
}
type copyOf struct {
	sel *xpath.Parsed
	el  *xmltree.Node
	// copyAccumulators is @copy-accumulators="yes": the copied nodes keep the
	// accumulator values of the nodes they were copied from (XSLT 3.0
	// §18.2.4). See engine.recordAccOrigin.
	copyAccumulators bool
	// noCopyNS is copy-namespaces="no": the copy keeps only the namespace
	// bindings its own name (or an attribute name) actually needs.
	noCopyNS bool
	// val is the [xsl:]validation / [xsl:]type request — see litElement.val.
	val *valRequest
}
type commentInstr struct {
	sel  *xpath.Parsed // @select (XSLT 2.0+): mutually exclusive with body
	body []instruction
	el   *xmltree.Node
}
type piInstr struct {
	name *avt
	sel  *xpath.Parsed // @select (XSLT 2.0+): mutually exclusive with body
	body []instruction
	el   *xmltree.Node
}
type sequenceInstr struct {
	sel  *xpath.Parsed
	body []instruction // when @select is absent (3.0 content form)
	el   *xmltree.Node
}
type analyzeString struct {
	sel         *xpath.Parsed
	regex       *avt
	flags       *avt // @flags (an AVT — avt-0601)
	matching    []instruction
	nonMatching []instruction
	el          *xmltree.Node
}

type sortKey struct {
	sel       *xpath.Parsed
	body      []instruction // sequence-constructor key (XSLT 2.0+, when @select is absent)
	el        *xmltree.Node // the xsl:sort element itself (namespace context for body)
	dataType  *avt          // "text" | "number" (AVT; nil = "text")
	order     *avt          // "ascending" | "descending" (AVT; nil = "ascending")
	caseOrder *avt          // "upper-first" | "lower-first" (AVT; nil = unspecified)
	collation *avt          // @collation (nil = not given; case-order is then ignored, collations-0201)
	lang      *avt          // @lang (nil = not given)
}

func (*litElement) instr()     {}
func (*litText) instr()        {}
func (*valueOf) instr()        {}
func (*applyTemplates) instr() {}
func (*forEach) instr()        {}
func (*ifInstr) instr()        {}
func (*chooseInstr) instr()    {}
func (*localVar) instr()       {}
func (*callTemplate) instr()   {}
func (*attrInstr) instr()      {}
func (*elemInstr) instr()      {}
func (*textInstr) instr()      {}
func (*copyInstr) instr()      {}
func (*copyOf) instr()         {}
func (*commentInstr) instr()   {}
func (*piInstr) instr()        {}
func (*sequenceInstr) instr()  {}
func (*analyzeString) instr()  {}

// Compile parses and compiles an XSLT stylesheet from source text (no file
// resolution; xsl:import/include are ignored).
func Compile(src string) (*Stylesheet, error) { return CompileFrom(src, "") }

// CompileFrom compiles a stylesheet, resolving xsl:import/xsl:include relative
// to baseDir (a workspace directory). Imported modules get lower import
// precedence than the importing module.
func CompileFrom(src, baseDir string) (*Stylesheet, error) {
	return compileModule(src, baseDir, "", nil, nil, nil)
}

// CompileAt compiles a stylesheet whose own retrieval location is known:
// selfPath is the file the source was read from, which becomes the module's
// static base URI (fn:static-base-uri, and the base for relative hrefs) —
// more precise than CompileFrom's directory-only base (use-when-0119).
func CompileAt(src, selfPath string) (*Stylesheet, error) {
	return compileModule(src, filepath.Dir(selfPath), selfPath, nil, nil, nil)
}

// CompileAtWithStatic is CompileAt plus HOST-SUPPLIED STATIC PARAMETERS
// (XSLT 3.0 §3.6: xsl:param static="yes"). Each map entry's key is the
// parameter's name as the host writes it — a lexical QName resolved against
// the declaring xsl:param's in-scope namespaces, or a Q{uri}local — and its
// value an XPath expression evaluated once in the static context. A supplied
// value REPLACES the declaration's own @select before any other static
// expression runs, so use-when, static variables and shadow attributes all see
// it. Names the stylesheet does not declare as static parameters are ignored.
//
// INVARIANT: static parameters are fixed at COMPILE time, which is why they
// cannot travel with the per-run Entry.Params — a caller that caches compiled
// stylesheets must include the supplied static parameters in its cache key;
// the same source with different static parameters is a different Stylesheet.
func CompileAtWithStatic(src, selfPath string, static map[string]string) (*Stylesheet, error) {
	return compileModule(src, filepath.Dir(selfPath), selfPath, static, nil, nil)
}

// CompileAtWithPackages is CompileAtWithStatic plus the set of LIBRARY
// PACKAGES the host makes available to xsl:use-package (XSLT 3.0 §3.5).
// Unlike xsl:import, xsl:use-package names a package rather than locating a
// file, so the mapping from package name + version to a module can only come
// from the host. A package whose PackageSource leaves Name/Version empty takes
// them from its own xsl:package/@name and @package-version.
func CompileAtWithPackages(src, selfPath string, static map[string]string, pkgs []PackageSource) (*Stylesheet, error) {
	return compileModule(src, filepath.Dir(selfPath), selfPath, static, pkgs, nil)
}

// SchemaSource is a schema document the HOST makes available to
// xsl:import-schema, identified by file path.
//
// XSLT 3.0 §3.16 makes @schema-location only a HINT: "the processor is not
// required to use" it, and a processor that already has a schema for the
// namespace being imported may use that instead. This is the channel for
// exactly that — the W3C catalog's own <schema role="stylesheet-import"/>,
// which names the document a test's xsl:import-schema is MEANT to pick up,
// frequently one whose @schema-location hint names a file that does not exist
// (import-schema-185's "variousTypesSchemaInline.xsd").
//
// A source whose Namespace is empty is matched by reading the document's own
// @targetNamespace, which is how the catalog supplies them.
type SchemaSource struct {
	Namespace string
	Path      string
}

// CompileAtWithSchemas is CompileAtWithPackages plus the host-offered schema
// documents above. Host schemas are consulted only when the declaration's own
// @schema-location resolves to nothing, so a stylesheet whose hint works keeps
// using it.
func CompileAtWithSchemas(src, selfPath string, static map[string]string, pkgs []PackageSource, schemas []SchemaSource) (*Stylesheet, error) {
	return compileModule(src, filepath.Dir(selfPath), selfPath, static, pkgs, schemas)
}

func compileModule(src, baseDir, selfPath string, hostStatic map[string]string, pkgs []PackageSource, hostSchemas []SchemaSource) (*Stylesheet, error) {
	doc, err := xmltree.ParseLenient11WithBase(src, baseDir)
	if err != nil {
		if pe, ok := err.(*xmltree.ParseError); ok {
			return nil, &CompileError{Line: pe.Line, Col: pe.Col, Msg: pe.Msg}
		}
		return nil, err
	}
	// The primary module's own retrieval location becomes its intrinsic
	// base-uri (xmltree.Node.Base): the static base URI used to resolve
	// relative hrefs in document()/doc()/unparsed-text() calls and by
	// fn:static-base-uri() when no xml:base attribute overrides it anywhere
	// in the module (document-0107/0701/1201). Only the DIRECTORY is known
	// here (the caller passes no filename), so it is given a trailing slash
	// — a directory reference, not a "file" whose last segment a relative
	// resolution would strip. filepath.Abs resolves any ".."/"." segments
	// against the process's working directory FIRST: net/url's RFC 3986
	// dot-segment removal assumes an absolute path and mangles a base that
	// itself starts with ".." (as the test harness's caller-relative
	// repoRoot() does), silently truncating to the wrong directory.
	if selfPath != "" && strings.Contains(selfPath, "://") {
		// Already a real URI rather than a bare filesystem path — e.g.
		// fn:transform's stylesheet-location can be an arbitrary retrieval
		// URI a host resolver fetched, not necessarily something on disk.
		// Report it as static-base-uri() verbatim (running it through
		// filepath.Abs below would mangle it into a bogus local path):
		// F&O 3.1 §16.3.2 states that stylesheet-location "also acts as the
		// default for stylesheet-base-uri" (fn-transform-20/21).
		doc.Base = selfPath
	} else if selfPath != "" {
		abs := selfPath
		if a, err := filepath.Abs(selfPath); err == nil {
			abs = a
		}
		doc.Base = fileURI(abs)
	} else if baseDir != "" {
		abs := baseDir
		if a, err := filepath.Abs(baseDir); err == nil {
			abs = a
		}
		doc.Base = fileURI(ensureTrailingSlash(abs))
	}
	root := xmltree.RootElement(doc)
	if root == nil {
		return nil, &CompileError{Msg: "stylesheet has no root element"}
	}

	c := &compiler{baseDir: baseDir, hostStatic: hostStatic, packages: pkgs, hostSchemas: hostSchemas}
	ss := newStylesheet()
	ss.packages = pkgs
	if baseDir != "" {
		// A module base (xmltree.Node.Base, set above and — per module — in
		// gatherModules/loadFile below) means every eval() now needs its
		// per-node static-base walk (staticBaseFor), not just when an
		// explicit xml:base attribute is present.
		ss.hasXMLBase = true
	}

	// Simplified stylesheet: a non-xsl root is the body of a match="/" template.
	if root.Name.Space != NS {
		if _, ok := root.Attr(NS, "version"); !ok {
			return nil, errAt(root, "err:XTSE0150: a simplified stylesheet requires an xsl:version attribute")
		}
		// Static validation (XTSE0010/0020/0090/…) applies to a simplified
		// stylesheet's body just as it does to a full xsl:stylesheet module —
		// this was previously skipped entirely for the literal-root form.
		if err := validateTree(root, false); err != nil {
			return nil, err
		}
		// This module's own tree never reaches the scanStaticAttrs loop over
		// mods below (this branch returns before it), so a simplified
		// stylesheet's own xsl:version (backwards-036/037: the WHOLE
		// document root is "<out xsl:version=...>") would otherwise never
		// set ss.hasBackwardsCompat / the other ambient-property flags.
		scanStaticAttrs(root, ss)
		ss.principalVersionIsOne = principalVersionIsOne(root)
		tmpl := &Template{matchSrc: "/", el: root}
		tmpl.pattern, _ = xpath.ParsePattern("/")
		body, err := c.compileSequence([]*xmltree.Node{root})
		if err != nil {
			return nil, err
		}
		tmpl.body = body
		ss.templates = append(ss.templates, tmpl)
		return ss, nil
	}
	// A TOP-LEVEL xsl:package with no xsl:use-package children is, for every
	// purpose this processor implements, an xsl:stylesheet: the package
	// machinery (separate compilation, visibility, overriding) only becomes
	// observable once one package USES another, and xsl:use-package is still
	// rejected (decls.go). Accepting the element as a module root lets the
	// large body of test material that is simply written as xsl:package run.
	// See checkDeclaredModes for the one package-only static rule that does
	// apply to a stand-alone package.
	if root.Name.Local != "stylesheet" && root.Name.Local != "transform" && root.Name.Local != "package" {
		return nil, errAt(root, "root element must be xsl:stylesheet or xsl:transform")
	}
	ss.isPackage = root.Name.Local == "package"
	ss.principalVersionIsOne = principalVersionIsOne(root)
	// XSLT 3.0: the DEFAULT INITIAL MODE of a transformation is the default
	// mode in scope for the principal stylesheet module's root element, i.e.
	// its [xsl:]default-mode attribute ("" — the unnamed mode — when absent,
	// which is the pre-3.0 behaviour).
	ss.defaultMode = defaultModeWalk(root)

	// Conditional inclusion: bind static parameters and prune use-when="false"
	// subtrees before module gathering (so excluded xsl:include/import are skipped).
	// Static variables/parameters are in scope in stylesheet TREE order, which
	// spans modules: establish them across the whole include/import graph
	// before any module is pruned (staticdecl.go). Skipped for a single-module
	// stylesheet, where applyStatic below already sees the declarations in the
	// right order and this pass would only cost an extra parse.
	if hasSecondaryModules(root) {
		if err := c.collectStaticsTreeOrder(root, baseDir); err != nil {
			return nil, err
		}
	}
	if err := c.applyStatic(root, true); err != nil {
		return nil, err
	}
	// After applyStatic, so @package-version reflects any _package-version
	// shadow attribute (package-version-007 vs -908).
	if root.Name.Local == "package" {
		if err := checkPackageAttrs(root); err != nil {
			return nil, err
		}
	}

	// Gather modules in import-precedence order (lowest first), then compile.
	mods, err := c.gatherModules(root, baseDir, map[string]bool{})
	if err != nil {
		return nil, err
	}
	// Shadow attributes ("_select" etc — resolveShadowAttrs' own doc comment
	// carries the full rationale) are resolved here: every module's static
	// params (c.staticParams) are now fully populated (gatherModules calls
	// applyStatic per module as it loads them), and nothing below this point
	// has looked at a real attribute value yet.
	for _, mod := range mods {
		if len(mod.children) > 0 && mod.children[0].Parent != nil {
			if err := resolveShadowAttrs(mod.children[0].Parent, c.staticParams); err != nil {
				return nil, err
			}
		}
	}
	// Pre-scan all modules for xsl:namespace-alias so literal result elements
	// compiled in any module see the full alias set, and note whether any
	// element carries xpath-default-namespace / xml:base (hot-path walks are
	// skipped when absent).
	for prec, mod := range mods {
		for _, child := range mod.children {
			if child.Name.Space == NS && child.Name.Local == "namespace-alias" {
				c.addNamespaceAlias(child, prec)
			}
		}
		if len(mod.children) > 0 && mod.children[0].Parent != nil {
			scanStaticAttrs(mod.children[0].Parent, ss)
		}
	}
	if c.nsAliasErr != nil {
		return nil, c.nsAliasErr
	}
	// Static validation (XTSE0010/0020/0090/0120/0500/0550) of every module.
	for _, mod := range mods {
		if len(mod.children) > 0 && mod.children[0].Parent != nil {
			if err := validateTree(mod.children[0].Parent, false); err != nil {
				return nil, err
			}
		}
	}
	// Schema components have to be in scope BEFORE any expression is parsed:
	// a type name written in @as / a pattern / a node test is resolved where
	// it is parsed, not where it is matched (decl_import_schema.go). Inert
	// unless the run claims schema-awareness.
	if err := c.compileImportedSchemas(ss, mods); err != nil {
		return nil, err
	}
	// gatherModules has already resolved the package-wide
	// input-type-annotations value (and rejected a conflict with XTSE0265);
	// this is simply where it becomes visible to the run.
	ss.stripInputTypes = c.inputTypeAnn == "strip"
	for prec, mod := range mods {
		c.baseDir = mod.baseDir
		c.importPrec = prec
		c.importLo = mod.importLo
		if err := c.compileTopLevel(ss, mod.children); err != nil {
			return nil, err
		}
	}
	// Collapse the raw xsl:mode declarations into one effective ModeDef per
	// mode (per-attribute import precedence, deferred XTSE0545 detection).
	// Nothing may read ss.modes before this point.
	if err := resolveModeDecls(ss); err != nil {
		return nil, err
	}
	// XTSE3085: inside an xsl:package, declared-modes defaults to "yes" and
	// every mode used must then be declared by an xsl:mode declaration.
	if root.Name.Local == "package" {
		var roots []*xmltree.Node
		for _, mod := range mods {
			if len(mod.children) > 0 && mod.children[0].Parent != nil {
				roots = append(roots, mod.children[0].Parent)
			}
		}
		if err := checkAbstractRefs(roots); err != nil {
			return nil, err
		}
		declared := true
		if v, ok := root.AttrLocal("declared-modes"); ok {
			declared = isXSLTTrue(strings.TrimSpace(v))
		}
		if declared {
			if err := checkDeclaredModes(ss, roots); err != nil {
				return nil, err
			}
		}
	}
	if err := checkStripPreserveConflict(ss); err != nil {
		return nil, err
	}
	if err := checkAttributeSetRefs(ss, mods); err != nil {
		return nil, err
	}
	if err := checkCallTemplateTunnelParams(ss, mods); err != nil {
		return nil, err
	}
	if err := checkKeyConflicts(ss); err != nil {
		return nil, err
	}
	if err := checkPatternFunctions(ss); err != nil {
		return nil, err
	}
	if err := checkDecimalFormatConflicts(ss); err != nil {
		return nil, err
	}
	if err := validateCharacterMaps(ss, root); err != nil {
		return nil, err
	}
	if err := checkAccumulatorConflicts(ss); err != nil {
		return nil, err
	}
	// XTSE3430: every template rule of a mode declared streamable="yes" must be
	// guaranteed-streamable (streamability.go).
	if err := strmCheckStreamableModes(ss); err != nil {
		return nil, err
	}
	// §6.6.3's typed="strict"/"lax" pattern rewrite (and XTSE3105). Last, so
	// no earlier analysis can see a rewritten node test.
	if err := applyTypedModeRewrites(ss); err != nil {
		return nil, err
	}
	return ss, nil
}

// validateCharacterMaps statically checks every use-character-maps reference
// — an xsl:character-map's own, plus the unnamed and named xsl:output
// declarations' — against the set of xsl:character-map names the stylesheet
// actually declares (XTSE1590), and rejects a circular use-character-maps
// chain among the character maps themselves (XTSE1600). el is used only for
// error positioning when no more specific node is available.
func validateCharacterMaps(ss *Stylesheet, el *xmltree.Node) error {
	uses := map[string][]string{}
	for key, cm := range ss.charMaps {
		if v, ok := cm.AttrLocal("use-character-maps"); ok {
			for _, tok := range strings.Fields(v) {
				ref := clarkName(resolveQName(cm, tok))
				if _, ok := ss.charMaps[ref]; !ok {
					return errAt(cm, "err:XTSE1590: xsl:character-map %q references unknown character-map %q", key, tok)
				}
				uses[key] = append(uses[key], ref)
			}
		}
	}
	const (
		white = iota
		gray
		black
	)
	color := map[string]int{}
	var visit func(name string) error
	visit = func(name string) error {
		color[name] = gray
		for _, dep := range uses[name] {
			switch color[dep] {
			case gray:
				return errAt(ss.charMaps[name], "err:XTSE1600: circular use-character-maps reference involving %q", dep)
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		color[name] = black
		return nil
	}
	// Deterministic order so a cycle always reports from the same starting
	// point across runs.
	names := make([]string, 0, len(ss.charMaps))
	for key := range ss.charMaps {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		if color[key] == white {
			if err := visit(key); err != nil {
				return err
			}
		}
	}
	checkList := func(list []string) error {
		for _, ref := range list {
			if _, ok := ss.charMaps[ref]; !ok {
				return errAt(el, "err:XTSE1590: use-character-maps references unknown character-map %q", ref)
			}
		}
		return nil
	}
	if err := checkList(ss.output.UseCharacterMaps); err != nil {
		return err
	}
	for _, o := range ss.namedOutputs {
		if err := checkList(o.UseCharacterMaps); err != nil {
			return err
		}
	}
	return nil
}

// addNamespaceAlias records an xsl:namespace-alias declaration: literal result
// elements/attributes in the stylesheet namespace are emitted in the result
// namespace with the result prefix. "#default" refers to the default namespace
// in scope on the declaration. prec is the declaring module's import
// precedence: two declarations for the same stylesheet-prefix at DIFFERENT
// precedence are not a conflict — the higher-precedence one silently wins,
// same as any other XSLT declaration (namespace-alias-1002/2620) — only two
// declarations at the SAME precedence that disagree are XTSE0810.
func (c *compiler) addNamespaceAlias(el *xmltree.Node, prec int) {
	lookup := func(p string) string {
		if p == "#default" {
			p = ""
		}
		uri, _ := el.LookupPrefix(p)
		return uri
	}
	sp, _ := el.AttrLocal("stylesheet-prefix")
	rp, _ := el.AttrLocal("result-prefix")
	for _, p := range []string{sp, rp} {
		if p == "#default" {
			continue
		}
		if _, bound := el.LookupPrefix(p); !bound {
			if c.nsAliasErr == nil {
				c.nsAliasErr = errAt(el, "err:XTSE0812: xsl:namespace-alias prefix %q has no in-scope namespace binding", p)
			}
			return
		}
	}
	pfx := rp
	if pfx == "#default" {
		pfx = ""
	}
	if c.nsAlias == nil {
		c.nsAlias = map[string]xmltree.Name{}
		c.nsAliasPrec = map[string]int{}
	}
	styURI := lookup(sp)
	target := xmltree.Name{Space: lookup(rp), Prefix: pfx}
	if prev, ok := c.nsAlias[styURI]; ok && prev != target {
		if c.nsAliasPrec[styURI] == prec {
			c.nsAliasErr = errAt(el, "err:XTSE0810: conflicting xsl:namespace-alias declarations for prefix %q", sp)
		}
		// A different precedence always means prec is HIGHER (modules are
		// scanned lowest-first), so it overrides below with no error.
	}
	c.nsAlias[styURI] = target
	c.nsAliasPrec[styURI] = prec
}

// principalVersionIsOne reports whether root — the outermost element of the
// PRINCIPAL stylesheet module (an xsl:stylesheet/transform/package, or a
// simplified stylesheet's own literal root) — declares its OWN @version (or
// xsl:version, for the simplified form) as EXACTLY the number 1.0.
func principalVersionIsOne(root *xmltree.Node) bool {
	v, ok := root.AttrLocal("version")
	if !ok {
		v, ok = root.Attr(NS, "version")
	}
	if !ok {
		return false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil && f == 1.0
}

// scanStaticAttrs walks a module tree once, flagging use of
// xpath-default-namespace and xml:base so per-eval ancestor walks can be
// skipped in the common case.
func scanStaticAttrs(el *xmltree.Node, ss *Stylesheet) {
	if el.Kind == xmltree.KindElement {
		for _, a := range el.Attrs {
			switch {
			case a.Name.Local == "xpath-default-namespace" && (a.Name.Space == "" || a.Name.Space == NS):
				ss.hasXPathDefaultNS = true
			case a.Name.Local == "base" && a.Name.Space == "http://www.w3.org/XML/1998/namespace":
				ss.hasXMLBase = true
			case a.Name.Local == "default-collation" && (a.Name.Space == "" || a.Name.Space == NS):
				ss.hasDefaultCollation = true
			case a.Name.Local == "version" && (a.Name.Space == "" || a.Name.Space == NS):
				if f, err := strconv.ParseFloat(strings.TrimSpace(a.Value), 64); err == nil && f < 2.0 {
					ss.hasBackwardsCompat = true
				}
			}
		}
	}
	for _, ch := range el.Children {
		scanStaticAttrs(ch, ss)
	}
}

// ensureTrailingSlash returns dir with exactly one trailing "/" so it resolves
// as a DIRECTORY reference (RFC 3986 relative resolution otherwise treats the
// last path segment as a "file" and strips it before appending).
func ensureTrailingSlash(dir string) string {
	if dir == "" || strings.HasSuffix(dir, "/") {
		return dir
	}
	return dir + "/"
}

// fileURI turns an absolute filesystem path into a "file:" URI. fn:resolve-uri
// requires its base argument to be an ABSOLUTE URI, meaning it must carry a
// SCHEME (RFC 3986) — a bare "/a/b/c" path does not qualify and fn:resolve-uri
// rejects it with FORG0002 (document-1301: static-base-uri() feeding straight
// into resolve-uri()). fileResolver.path already strips a "file:" prefix back
// off before touching the filesystem, so this round-trips cleanly.
func fileURI(absPath string) string {
	if absPath == "" || strings.HasPrefix(absPath, "file:") {
		return absPath
	}
	return "file://" + absPath
}

func newStylesheet() *Stylesheet {
	return &Stylesheet{
		named:        map[string]*Template{},
		namedClark:   map[string]*Template{},
		output:       Output{Method: "", Encoding: "UTF-8"},
		functions:    map[string]*FuncDef{},
		modes:        map[string]*ModeDef{},
		modeDecls:    map[string][]*ModeDef{},
		attrSets:     map[string][]attrSetDecl{},
		decFmts:      map[string]xpath.DecimalFormat{},
		charMaps:     map[string]*xmltree.Node{},
		namedOutputs: map[string]Output{},
	}
}

// compileTopLevel compiles the top-level declarations of one module.
func (c *compiler) compileTopLevel(ss *Stylesheet, children []*xmltree.Node) error {
	for _, child := range children {
		if child.Name.Space != NS {
			// XTSE0130 only applies to a genuine direct child of xsl:stylesheet/
			// xsl:transform itself — an included SIMPLIFIED module contributes
			// its single literal root's children here too (gatherModules), and
			// those are ordinary content, not stray top-level declarations.
			if child.Name.Space == "" && child.Parent != nil && child.Parent.Name.Space == NS &&
				(child.Parent.Name.Local == "stylesheet" || child.Parent.Name.Local == "transform") {
				return errAt(child, "err:XTSE0130: xsl:stylesheet child <%s> has a null namespace URI", child.Name.Local)
			}
			continue
		}
		switch child.Name.Local {
		case "template":
			// If this template is an xsl:override declaration that shadows an
			// already-compiled component of the used package (registered under
			// its plain name below, lower precedence, at the same point this
			// pass reaches the override), stash the shadowed original under a
			// synthesized name for the duration of compiling this override's
			// body — xsl:original resolves to it (XSLT 3.0 §3.6.3.2). Scoped
			// per override, not a single global slot, so a package with several
			// overrides never lets one's xsl:original reach another's original.
			savedOriginal := c.overrideOriginalName
			c.overrideOriginalName = ""
			if name, hasName := child.AttrLocal("name"); hasName && c.overrideNodes[child] {
				if prev, ok := ss.named[strings.TrimSpace(name)]; ok {
					c.overrideCounter++
					synth := fmt.Sprintf("\x00xsl:original#%d", c.overrideCounter)
					ss.named[synth] = prev
					c.overrideOriginalName = synth
				}
			}
			t, err := c.compileTemplate(child, c.order)
			c.overrideOriginalName = savedOriginal
			if err != nil {
				return err
			}
			c.order++
			ss.templates = append(ss.templates, t)
			// XSLT §6.4: a union match pattern denotes a SET of template
			// rules, one per alternative, each with its own default priority
			// and each separately reachable by xsl:next-match
			// (next-match-021/023/025/026/039). t keeps the first
			// alternative; the rest become clones sharing its body, ordered
			// after it so that — at equal priority — the LAST alternative is
			// still the one apply-templates picks. The rule is stated as part
			// of the DEFAULT-priority computation, so an explicit @priority
			// keeps the union as a single rule that xsl:next-match passes over
			// once (next-match-024).
			if t.pattern != nil && !t.hasPriority {
				alts := xpath.PatternAlternatives(t.matchSrc)
				for i, src := range alts {
					pat, perr := parsePatternFor(t.el, src)
					if perr != nil {
						return errAt(child, "bad match pattern %q: %v", src, perr)
					}
					target := t
					if i > 0 {
						clone := *t
						clone.name = "" // the named template is registered once, on t
						clone.order = c.order
						c.order++
						ss.templates = append(ss.templates, &clone)
						target = &clone
					}
					target.pattern = pat
					if !t.hasPriority {
						target.priority = pat.DefaultPriority()
					}
				}
			}
			if t.name != "" {
				if prev, ok := ss.named[t.name]; ok && prev.importPrec == t.importPrec {
					return errAt(child, "err:XTSE0660: duplicate named template %q at the same import precedence", t.name)
				}
				clarkKey := clarkName(resolveQName(child, t.name))
				if prev, ok := ss.namedClark[clarkKey]; ok && prev.importPrec == t.importPrec {
					return errAt(child, "err:XTSE0660: duplicate named template %q at the same import precedence", t.name)
				}
				ss.namedClark[clarkKey] = t
				ss.named[t.name] = t // higher precedence compiled later overrides
			}
		case "output":
			if name, ok := child.AttrLocal("name"); ok && name != "" {
				// Keyed by the expanded {uri}local QName (not the literal
				// prefix text) so xsl:result-document/@format can resolve it
				// via a different prefix bound to the same namespace
				// (result-document-0238).
				key := clarkName(resolveQName(child, name))
				if err := c.checkOutputConflict(key, child); err != nil {
					return err
				}
				o := ss.namedOutputs[key]
				if err := c.applyParameterDocument(&o, child); err != nil {
					return err
				}
				applyOutput(&o, child)
				ss.namedOutputs[key] = o
			} else {
				if err := c.checkOutputConflict("", child); err != nil {
					return err
				}
				if err := c.applyParameterDocument(&ss.output, child); err != nil {
					return err
				}
				applyOutput(&ss.output, child)
				ss.hasOutputDecl = true
			}
		case "param":
			vd, err := c.compileVarDef(child, true)
			if err != nil {
				return err
			}
			if err := checkGlobalSelfReference(vd, child); err != nil {
				return err
			}
			c.bindSettledStatic(vd, child)
			ss.globals = append(ss.globals, vd)
		case "variable":
			vd, err := c.compileVarDef(child, false)
			if err != nil {
				return err
			}
			if err := checkGlobalSelfReference(vd, child); err != nil {
				return err
			}
			if err := c.checkDupGlobal(ss, vd, child); err != nil {
				return err
			}
			c.bindSettledStatic(vd, child)
			ss.globals = append(ss.globals, vd)
		case "strip-space":
			if err := addNames(&ss.strip, child, c.importPrec); err != nil {
				return err
			}
		case "preserve-space":
			if err := addNames(&ss.preserve, child, c.importPrec); err != nil {
				return err
			}
		case "key":
			kd, err := c.compileKey(child)
			if err != nil {
				return err
			}
			for _, prev := range ss.keys {
				if prev.name == kd.name && prev.collURI != kd.collURI {
					return errAt(child, "err:XTSE1220: xsl:key %q declared with different collations", kd.name)
				}
			}
			ss.keys = append(ss.keys, kd)
		case "function":
			fd, err := c.compileFunction(child)
			if err != nil {
				return err
			}
			key := funcKey(fd.name.Space, fd.name.Local, len(fd.params))
			if prev, ok := ss.functions[key]; ok && prev.importPrec == fd.importPrec {
				return errAt(child, "err:XTSE0770: duplicate function %s#%d at the same import precedence", fd.name.Local, len(fd.params))
			}
			// A one-argument stylesheet function must not take the name of a
			// CONSTRUCTOR FUNCTION either: XPath 3.1 §3.14.4 gives every
			// atomic type in the in-scope schema types a constructor of
			// exactly that name and arity, so an xsl:function with the same
			// expanded QName and arity 1 is a second function with one
			// signature — the same clash XTSE0770 names for two xsl:function
			// declarations (type-functions-0503, whose catalog entry notes
			// "the error isn't explicit in the XSLT spec, but this is the
			// closest it gets"). Only reachable in a schema-aware run, where
			// a user type can be in scope at all.
			if len(fd.params) == 1 && schemaAwareRun() {
				if a := schemaAdapterFor(child); a != nil {
					if t, ok := a.sch.TypeByName(fd.name.Space, fd.name.Local); ok && !t.Complex {
						return errAt(child, "err:XTSE0770: xsl:function %s#1 has the same name and arity as the constructor function of the imported type %s",
							fd.name.Local, fd.name.Local)
					}
				}
			}
			ss.functions[key] = fd
		case "decimal-format":
			name, ok := child.AttrLocal("name")
			if !ok {
				name = ""
			}
			// checkDecimalFormat (symbol distinctness, XTSE1300) runs once
			// per NAME on the final MERGED result in
			// checkDecimalFormatConflicts, not here per raw declaration:
			// compileDecimalFormat fills every unspecified attribute with
			// the XSLT default, so validating this declaration standalone
			// gives a false XTSE1300 whenever a sibling declaration (at the
			// same precedence) is what actually supplies the attribute that
			// would make the symbols distinct (format-number-053/054: "q"
			// split across two declarations, decimal-separator="," in one
			// and grouping-separator="." in the other — the second alone
			// looks like DecimalSep='.' (default) clashing with its own
			// GroupingSep='.').
			//
			// Key by the expanded {uri}local QName so a lookup via any alias
			// prefix or a Q{uri}local literal finds it (matches fn:format-number).
			if name != "" {
				name = clarkName(resolveQName(child, name))
			}
			// Conflict resolution (XTSE1290) is deferred to
			// checkDecimalFormatConflicts, once every module has been
			// compiled: a conflict between two LOWER-precedence declarations
			// is not an error at all if a higher-precedence one later
			// overrides both (format-number-046: the imported module alone
			// has two directly conflicting "q" declarations, which would be
			// an error if compiled standalone, but the importing module's
			// own higher-precedence "q" wins outright and neither lower one
			// is ever consulted).
			if ss.decFmtDecls == nil {
				ss.decFmtDecls = map[string][]decFmtDecl{}
			}
			ss.decFmtDecls[name] = append(ss.decFmtDecls[name], decFmtDecl{prec: c.importPrec, el: child})
		case "character-map":
			if name, ok := child.AttrLocal("name"); ok {
				key := clarkName(resolveQName(child, name))
				if ss.charMapPrec == nil {
					ss.charMapPrec = map[string]int{}
				}
				if prevPrec, exists := ss.charMapPrec[key]; exists && prevPrec == c.importPrec {
					return errAt(child, "err:XTSE1580: duplicate xsl:character-map %q at the same import precedence", name)
				}
				ss.charMaps[key] = child
				ss.charMapPrec[key] = c.importPrec
			}
		default:
			if err := c.compileTopLevelExtra(ss, child); err != nil {
				return err
			}
		}
	}
	return nil
}

type compiler struct {
	baseDir string
	// hostStatic holds host-supplied static-parameter values (see
	// CompileAtWithStatic): parameter name as written -> XPath expression. A
	// name present here overrides the xsl:param's own @select default.
	hostStatic map[string]string
	// packages holds the library packages the host makes available to
	// xsl:use-package (CompileAtWithPackages); see packages.go.
	packages []PackageSource
	// hostSchemas are schema documents the host offers to xsl:import-schema
	// when a declaration's own @schema-location hint resolves to nothing
	// (see SchemaSource).
	hostSchemas []SchemaSource
	// pkgExports caches each library package's component manifest by absolute
	// path (pkgcheck.go) — a package graph is commonly a diamond.
	pkgExports map[string][]pkgComponent
	// staticDecls records the effective binding of each static variable/param
	// established by the cross-module tree-order pass (staticdecl.go), with
	// the import precedence that set it. A name present here is already
	// settled: applyStatic must not overwrite it while walking one module.
	staticDecls map[string]staticDecl
	// staticRecs is every static declaration the cross-module pass met, in
	// stylesheet tree order.
	staticRecs []staticDeclRec
	// staticUnset names static parameters that were declared with neither a
	// @select default nor a host-supplied value. Their value is genuinely
	// UNKNOWN; use-when keeps the historical "" behaviour, but shadow-
	// attribute expansion (expandShadowAttrs) leaves the written attribute
	// alone rather than substituting an empty string.
	staticUnset map[string]bool
	importPrec  int
	// importLo is the lowest import precedence the module now being compiled
	// transitively imports; see modChildren.importLo.
	importLo     int
	order        int
	staticParams map[string]xpath.Object              // static="yes" parameters, for use-when (clark name -> value)
	staticRes    xpath.ResourceResolver               // fn:doc resolver for static expressions (see staticResolver)
	nsAlias      map[string]xmltree.Name              // xsl:namespace-alias: stylesheet URI -> result {Space, Prefix}
	nsAliasPrec  map[string]int                       // stylesheet URI -> import precedence of the recorded alias
	nsAliasErr   error                                // conflicting alias declarations (XTSE0810)
	inputTypeAnn string                               // first explicit input-type-annotations value seen across all modules (XTSE0265)
	outputSeen   map[string]map[string]outputAttrSeen // xsl:output conflict tracking (XTSE1560): output key ("" = unnamed) -> attr name -> last seen value/precedence
	decFmtSeen   map[string]map[string]outputAttrSeen // xsl:decimal-format merge/conflict tracking (XTSE1290), same shape as outputSeen
	// overrideNodes marks the top-level declaration nodes that are direct
	// children of an xsl:override (gatherUsedPackage) — the population that
	// may legitimately call xsl:original (XSLT 3.0 §3.6.3.2).
	overrideNodes map[*xmltree.Node]bool
	// overrideOriginalName is the synthesized ss.named key that
	// xsl:call-template name="xsl:original" resolves to WHILE compiling one
	// override template's body ("" outside such a body). Scoped per override
	// (not a single global slot) so two different overrides in the same
	// compile each reach their OWN original, never each other's.
	overrideOriginalName string
	overrideCounter      int
}

// outputAttrSeen records the last explicit value seen for one xsl:output
// serialization attribute, and the import precedence that set it.
type outputAttrSeen struct {
	value string
	prec  int
}

// checkOutputConflict reports XTSE1560: two xsl:output declarations within
// the same output definition (key = "" for the unnamed one, else the named
// output's expanded QName) giving different explicit values for the same
// attribute at the same import precedence — cdata-section-elements and
// use-character-maps are exempt (they accumulate as a union instead).
func (c *compiler) checkOutputConflict(key string, el *xmltree.Node) error {
	if c.outputSeen == nil {
		c.outputSeen = map[string]map[string]outputAttrSeen{}
	}
	m := c.outputSeen[key]
	if m == nil {
		m = map[string]outputAttrSeen{}
		c.outputSeen[key] = m
	}
	for _, name := range outputAttrNames {
		if name == "cdata-section-elements" || name == "use-character-maps" {
			continue
		}
		v, ok := el.AttrLocal(name)
		if !ok {
			continue
		}
		if prev, seen := m[name]; seen && prev.prec == c.importPrec && prev.value != v {
			return errAt(el, "err:XTSE1560: conflicting xsl:output/@%s values %q and %q at the same import precedence", name, prev.value, v)
		}
		m[name] = outputAttrSeen{value: v, prec: c.importPrec}
	}
	return nil
}

// outputAttrNames lists every serialization attribute applyOutput/
// applyOutputAttr understands — the vocabulary xsl:output and
// xsl:result-document share (result-document's own attributes are
// AVT-evaluated first, then dispatched through applyOutputAttr with the
// resolved string, so both entry points share one implementation).
var outputAttrNames = []string{
	"method", "indent", "omit-xml-declaration", "encoding", "use-character-maps",
	"doctype-public", "doctype-system", "escape-uri-attributes", "standalone",
	"media-type", "include-content-type", "cdata-section-elements",
	"suppress-indentation", "html-version", "byte-order-mark", "version",
	"normalization-form", "undeclare-prefixes", "item-separator",
	"json-node-output-method", "allow-duplicate-names", "build-tree",
}

// serParamNS is the namespace of a serialization parameter document
// (xsl:output/@parameter-document).
const serParamNS = "http://www.w3.org/2010/xslt-xquery-serialization"

// applyParameterDocument implements xsl:output/@parameter-document: the named
// document holds <output:NAME value="..."/> serialization parameters (plus an
// output:use-character-maps element whose output:character-map children give
// inline character mappings). They are applied BEFORE the attributes written on
// the xsl:output element itself, which override them.
func (c *compiler) applyParameterDocument(o *Output, el *xmltree.Node) error {
	href, ok := el.AttrLocal("parameter-document")
	if !ok || strings.TrimSpace(href) == "" {
		return nil
	}
	return loadParameterDocument(o, el, c.baseDir, href)
}

// loadParameterDocument is applyParameterDocument's engine-independent core, so
// xsl:result-document/@parameter-document — whose href is an AVT and therefore
// only known at RUN time — can reuse exactly the same reader (result-document-
// 1406).
func loadParameterDocument(o *Output, el *xmltree.Node, baseDir, href string) error {
	// Resolve against the DECLARING element's static base URI (the module it
	// is written in), not the principal module's directory — an xsl:output in
	// an included module resolves its parameter-document relative to that
	// module (output-0722).
	base := xpath.NodeBaseURI(el, "")
	res := newFileResolver(baseDir, nil)
	target := strings.TrimSpace(href)
	if base != "" {
		if abs, err := xpath.ResolveURIRef(target, base); err == nil && abs != "" {
			target = abs
		}
	}
	doc, ok := res.ResolveDoc(target)
	if !ok || doc == nil {
		return errAt(el, "err:XTDE1460: cannot retrieve parameter-document %q", href)
	}
	root := xmltree.RootElement(doc)
	if root == nil || root.Name.Space != serParamNS {
		return errAt(el, "err:XTSE0020: %q is not a serialization-parameters document", href)
	}
	for _, p := range root.Children {
		if p.Kind != xmltree.KindElement || p.Name.Space != serParamNS {
			continue
		}
		if p.Name.Local == "use-character-maps" {
			for _, cm := range p.Children {
				if cm.Kind != xmltree.KindElement || cm.Name.Local != "character-map" {
					continue
				}
				ch, _ := cm.AttrLocal("character")
				to, _ := cm.AttrLocal("map-string")
				r := []rune(ch)
				if len(r) != 1 {
					return errAt(el, "err:XTSE0020: output:character-map/@character must be a single character")
				}
				if o.InlineCharMap == nil {
					o.InlineCharMap = map[rune]string{}
				}
				o.InlineCharMap[r[0]] = to
			}
			continue
		}
		if v, ok := p.AttrLocal("value"); ok {
			applyOutputAttr(o, p, p.Name.Local, v)
		}
	}
	return nil
}

func applyOutput(o *Output, el *xmltree.Node) {
	for _, name := range outputAttrNames {
		if v, ok := el.AttrLocal(name); ok {
			applyOutputAttr(o, el, name, v)
		}
	}
}

// applyOutputAttr applies one already-resolved serialization attribute value
// (name, v) to o. el supplies the in-scope namespaces for the QName-list
// attributes (cdata-section-elements/suppress-indentation/use-character-maps)
// — resolved against el's OWN static namespace bindings even when v itself
// came from evaluating an AVT (a dynamically-computed name cannot introduce a
// namespace binding that wasn't already declared in the stylesheet).
func applyOutputAttr(o *Output, el *xmltree.Node, name, v string) {
	switch name {
	case "method":
		// An attribute of type QName/enumeration is whitespace-normalized, so
		// method=" xhtml " names the xhtml method (output-0221 writes it with
		// surrounding spaces on purpose).
		o.Method = strings.TrimSpace(v)
	case "indent":
		o.Indent = yesTrue(v)
	case "omit-xml-declaration":
		o.OmitXMLDeclaration = yesTrue(v)
	case "encoding":
		o.Encoding = v
	case "use-character-maps":
		for _, tok := range strings.Fields(v) {
			o.UseCharacterMaps = append(o.UseCharacterMaps, clarkName(resolveQName(el, tok)))
		}
	case "doctype-public":
		o.DoctypePublic = v
	case "doctype-system":
		o.DoctypeSystem = v
	case "escape-uri-attributes":
		switch strings.TrimSpace(v) {
		case "no", "false", "0":
			o.NoEscapeURIAttributes = true
		default: // "yes", "true", "1", or anything else — the spec default is on
			o.NoEscapeURIAttributes = false
		}
	case "standalone":
		// XSLT 3.0 widens standalone's lexical space to the xs:boolean forms
		// too (output-0149a/b: " true "/"1", output-0150a/b: " false "/" 0 ");
		// "omit" (or anything else) means no standalone pseudo-attribute at
		// all (output-0152), matching the "" sentinel SerializeOptions uses.
		switch strings.TrimSpace(v) {
		case "yes", "true", "1":
			o.Standalone = "yes"
		case "no", "false", "0":
			o.Standalone = "no"
		default:
			o.Standalone = ""
		}
	case "media-type":
		o.MediaType = v
	case "include-content-type":
		switch strings.TrimSpace(v) {
		case "no", "false", "0":
			o.NoContentType = true
		default: // "yes", "true", "1", or anything else — the spec default is on
			o.NoContentType = false
		}
	case "cdata-section-elements":
		// Unlike most xsl:output attributes (last-wins), the spec accumulates
		// cdata-section-elements as the UNION across every xsl:output
		// declaration that specifies it (output-0122): two separate
		// declarations each naming one element both apply.
		for _, tok := range strings.Fields(v) {
			o.CDATASectionElements = append(o.CDATASectionElements, resolveElementName(el, tok))
		}
	case "suppress-indentation":
		// Same union treatment as cdata-section-elements above.
		for _, tok := range strings.Fields(v) {
			o.SuppressIndentation = append(o.SuppressIndentation, resolveElementName(el, tok))
		}
	case "html-version":
		o.HTMLVersion = v
	case "byte-order-mark":
		o.ByteOrderMark = yesTrue(v)
	case "version":
		o.Version = strings.TrimSpace(v)
	case "normalization-form":
		o.NormalizationForm = strings.TrimSpace(v)
	case "undeclare-prefixes":
		o.UndeclarePrefixes = yesTrue(v)
	case "item-separator":
		// XSLT 3.0 lets an xsl:result-document attribute reset a named
		// format's value back to "unspecified" with the reserved value
		// "#absent" (result-document-0305).
		if strings.TrimSpace(v) == "#absent" {
			o.ItemSeparator, o.HasItemSeparator = "", false
			return
		}
		o.ItemSeparator, o.HasItemSeparator = v, true
	case "json-node-output-method":
		o.JSONNodeOutputMethod = strings.TrimSpace(v)
	case "allow-duplicate-names":
		o.AllowDuplicateNames = yesTrue(v)
	case "build-tree":
		o.BuildTree = strings.TrimSpace(v)
	}
}

// checkDupGlobal raises XTSE0630 for two global variables/params with the same
// expanded name at the same import precedence.
func (c *compiler) checkDupGlobal(ss *Stylesheet, vd *VarDef, el *xmltree.Node) error {
	key := clarkName(vd.name)
	for _, g := range ss.globals {
		if clarkName(g.name) == key && g.importPrec == c.importPrec {
			return errAt(el, "err:XTSE0630: duplicate global variable $%s at the same import precedence", vd.name.Local)
		}
	}
	vd.importPrec = c.importPrec
	return nil
}

// checkDecimalFormat enforces the decimal-format symbol constraints: zero-digit
// must be a digit denoting zero (XTSE1295), and the picture-significant symbols
// must be distinct (XTSE1300).
func checkDecimalFormat(el *xmltree.Node, df xpath.DecimalFormat) error {
	if zd, ok := el.AttrLocal("zero-digit"); ok {
		r := []rune(zd)
		if len(r) != 1 || !unicode.IsDigit(r[0]) || unicode.ToLower(r[0]) != r[0] || int(r[0])-int('0') != 0 && !isZeroDigit(r[0]) {
			return errAt(el, "err:XTSE1295: zero-digit %q must be a digit with value zero", zd)
		}
	}
	syms := []rune{df.DecimalSep, df.GroupingSep, df.Percent, df.PerMille, df.Digit, df.PatternSep}
	seen := map[rune]bool{}
	for _, r := range syms {
		if seen[r] {
			return errAt(el, "err:XTSE1300: decimal-format symbols must be distinct (%q reused)", string(r))
		}
		seen[r] = true
	}
	// The digit family (zero-digit..zero-digit+9) must not collide with the
	// other symbols.
	for _, r := range syms {
		if r >= df.ZeroDigit && r <= df.ZeroDigit+9 {
			return errAt(el, "err:XTSE1300: decimal-format symbol %q collides with the digit family", string(r))
		}
	}
	return nil
}

// isZeroDigit reports whether r is a Unicode digit whose numeric value is 0.
func isZeroDigit(r rune) bool {
	return unicode.IsDigit(r) && unicode.ToLower(r) == r && digitZeroBase(r) == r
}

// digitZeroBase returns the zero of r's decimal-digit block.
func digitZeroBase(r rune) rune {
	for base := r; base >= r-9; base-- {
		if !unicode.IsDigit(base) {
			return base + 1
		}
		if base == r-9 {
			return base
		}
	}
	return r
}

// spaceTest is a resolved xsl:strip-space / xsl:preserve-space element name
// test. Names are resolved against the declaration's in-scope namespaces, and
// an unprefixed name is in NO namespace (never the default namespace), per
// XSLT §strip-space.
type spaceTest struct {
	kind       int    // 0 = "*"; 1 = wildcard ("pfx:*" or "*:local"); 2 = exact
	space      string // resolved namespace URI (kind 1 prefix-wildcard / kind 2)
	local      string // local name (kind 2, or kind-1 "*:local")
	anyNS      bool   // kind 1: "*:local" (any namespace, specific local)
	importPrec int    // the declaring module's import precedence (higher wins)
	el         *xmltree.Node
}

// addNames resolves the whitespace-declaration @elements name-tests against the
// declaration element's in-scope namespaces and appends them to set, stamped
// with prec (the declaring module's import precedence — strip-space-020: a
// strip-space in a higher-precedence module beats a preserve-space in a lower
// one for the SAME element, regardless of which pattern is more specific).
// A prefix with no in-scope binding is XTSE0280 (strip-space-002).
func addNames(set *[]spaceTest, el *xmltree.Node, prec int) error {
	v, ok := el.AttrLocal("elements")
	if !ok {
		return nil
	}
	for _, tok := range strings.Fields(v) {
		var t spaceTest
		switch {
		case tok == "*":
			t = spaceTest{kind: 0}
		case strings.HasPrefix(tok, "Q{"): // Q{uri}local braced EQName (strip-space-025)
			uri, local, ok := bracedEQName(tok)
			if !ok {
				return errAt(el, "err:XTSE0280: %q is not a valid EQName in xsl:%s/@elements", tok, el.Name.Local)
			}
			t = spaceTest{kind: 2, space: uri, local: local}
		case strings.HasSuffix(tok, ":*"): // pfx:*
			pfx := tok[:len(tok)-2]
			uri, bound := el.LookupPrefix(pfx)
			if !bound {
				return errAt(el, "err:XTSE0280: prefix %q in xsl:%s/@elements is not bound to a namespace", pfx, el.Name.Local)
			}
			t = spaceTest{kind: 1, space: uri}
		case strings.HasPrefix(tok, "*:"): // *:local
			t = spaceTest{kind: 1, anyNS: true, local: tok[2:]}
		default:
			local := tok
			// An unprefixed name is NOT affected by an ordinary xmlns default
			// namespace declaration (strip-space-002), but IS affected by an
			// in-scope [xsl:]xpath-default-namespace (xpath-default-namespace-0201/0202).
			uri := xpathDefaultNSWalk(el)
			if i := strings.LastIndexByte(tok, ':'); i >= 0 {
				pfx := tok[:i]
				var bound bool
				uri, bound = el.LookupPrefix(pfx)
				if !bound {
					return errAt(el, "err:XTSE0280: prefix %q in xsl:%s/@elements is not bound to a namespace", pfx, el.Name.Local)
				}
				local = tok[i+1:]
			} else {
				// An unprefixed name resolves like any other unprefixed
				// element name test: no namespace, unless xpath-default-
				// namespace is in scope on the declaring element or an
				// ancestor (xpath-default-namespace-0201).
				uri = xpathDefaultNSWalk(el)
			}
			t = spaceTest{kind: 2, space: uri, local: local}
		}
		t.importPrec = prec
		t.el = el
		*set = append(*set, t)
	}
	return nil
}

// checkStripPreserveConflict reports XTSE0270: the same element NameTest (two
// name-tests are "the same" when they match the same set of names) appearing
// in both an xsl:strip-space and an xsl:preserve-space declaration at the
// same import precedence.
func checkStripPreserveConflict(ss *Stylesheet) error {
	for _, a := range ss.strip {
		for _, b := range ss.preserve {
			if a.importPrec == b.importPrec && a.kind == b.kind && a.space == b.space &&
				a.local == b.local && a.anyNS == b.anyNS {
				return errAt(b.el, "err:XTSE0270: conflicting xsl:strip-space/xsl:preserve-space declarations at the same import precedence")
			}
		}
	}
	return nil
}

// decFmtDecl is one raw xsl:decimal-format declaration seen during
// compileTopLevel, before precedence resolution (see checkDecimalFormatConflicts).
type decFmtDecl struct {
	prec int
	el   *xmltree.Node
}

// checkDecimalFormatConflicts resolves every xsl:decimal-format name to its
// winning value: only the declarations at the HIGHEST import precedence seen
// for that name matter (XTSE1290 fires only if more than one DISTINCT value
// exists among those, not among precedences a higher one already overrides —
// format-number-046/053/054/055).
func checkDecimalFormatConflicts(ss *Stylesheet) error {
	if ss.decFmts == nil {
		ss.decFmts = map[string]xpath.DecimalFormat{}
	}
	for name, decls := range ss.decFmtDecls {
		merged, errEl, err := mergeDecimalFormatDecls(name, decls)
		if err != nil {
			return err
		}
		if err := checkDecimalFormat(errEl, merged); err != nil {
			return err
		}
		ss.decFmts[name] = merged
	}
	return nil
}

// mergeDecimalFormatDecls combines every xsl:decimal-format declaration of
// one name, at ANY import precedence, into a single definition — resolved
// per ATTRIBUTE, not per declaration: each of the ten symbol/string
// attributes independently takes its value from whichever declaration
// EXPLICITLY sets that attribute at the HIGHEST import precedence (an
// attribute a higher-precedence declaration leaves unset is simply
// INHERITED from a lower-precedence one that does set it, rather than
// falling back to the XSLT default — format-number-055: importing module
// overrides only minus-sign, inheriting the imported module's own decimal-
// separator/grouping-separator). Two declarations conflict (XTSE1290) only
// when they are the joint-highest explicit setters of the SAME attribute and
// disagree (format-number-046: entirely superseded by a higher precedence,
// no error; format-number-053/054: same precedence but different attributes,
// no error).
func mergeDecimalFormatDecls(name string, decls []decFmtDecl) (xpath.DecimalFormat, *xmltree.Node, error) {
	df := xpath.DefaultDecimalFormat()
	first := func(s string) rune {
		for _, r := range s {
			return r
		}
		return 0
	}
	type attrSpec struct {
		attr  string
		apply func(*xpath.DecimalFormat, string)
	}
	specs := []attrSpec{
		{"decimal-separator", func(d *xpath.DecimalFormat, v string) { d.DecimalSep = first(v) }},
		{"grouping-separator", func(d *xpath.DecimalFormat, v string) { d.GroupingSep = first(v) }},
		{"percent", func(d *xpath.DecimalFormat, v string) { d.Percent = first(v) }},
		{"per-mille", func(d *xpath.DecimalFormat, v string) { d.PerMille = first(v) }},
		{"zero-digit", func(d *xpath.DecimalFormat, v string) { d.ZeroDigit = first(v) }},
		{"digit", func(d *xpath.DecimalFormat, v string) { d.Digit = first(v) }},
		{"pattern-separator", func(d *xpath.DecimalFormat, v string) { d.PatternSep = first(v) }},
		{"minus-sign", func(d *xpath.DecimalFormat, v string) { d.MinusSign = first(v) }},
		{"infinity", func(d *xpath.DecimalFormat, v string) { d.Infinity = v }},
		{"NaN", func(d *xpath.DecimalFormat, v string) { d.NaN = v }},
		{"exponent-separator", func(d *xpath.DecimalFormat, v string) { d.ExponentSep = first(v) }},
	}
	// errEl is whichever declaration ends up contributing to the merged
	// result, for XTSE1295/1300 error-position purposes if the FINAL
	// combination turns out to have colliding symbols; zero-digit's own
	// setter is preferred (checkDecimalFormat re-reads that attribute off it).
	var errEl *xmltree.Node
	for _, sp := range specs {
		// Two passes: first find the HIGHEST precedence that sets this
		// attribute at all, so a lower-precedence disagreement is never
		// even inspected once a higher override exists (format-number-046:
		// the imported module's OWN two directly-conflicting declarations,
		// both superseded, must never be compared against each other).
		maxPrec, have := 0, false
		for _, d := range decls {
			if _, ok := d.el.AttrLocal(sp.attr); ok && (!have || d.prec > maxPrec) {
				maxPrec, have = d.prec, true
			}
		}
		if !have {
			continue
		}
		var value string
		var set bool
		for _, d := range decls {
			if d.prec != maxPrec {
				continue
			}
			v, ok := d.el.AttrLocal(sp.attr)
			if !ok {
				continue
			}
			if set && v != value {
				return xpath.DecimalFormat{}, nil, errAt(d.el, "err:XTSE1290: conflicting xsl:decimal-format declarations for %q", name)
			}
			set, value = true, v
			if errEl == nil || sp.attr == "zero-digit" {
				errEl = d.el
			}
		}
		sp.apply(&df, value)
	}
	if errEl == nil {
		errEl = decls[0].el
	}
	return df, errEl, nil
}

// checkKeyConflicts reports XTSE1220 (two xsl:key declarations with the same
// name but different effective collations) and XTSE1222 (…different effective
// composite values).
func checkKeyConflicts(ss *Stylesheet) error {
	seenColl := map[string]string{}
	seenHasColl := map[string]bool{}
	seenComp := map[string]bool{}
	for _, kd := range ss.keys {
		if kd.hasCollation {
			if prev, ok := seenColl[kd.name]; ok && seenHasColl[kd.name] && prev != kd.collation {
				return errAt(kd.el, "err:XTSE1220: xsl:key %q has conflicting collation declarations", kd.name)
			}
			seenColl[kd.name] = kd.collation
			seenHasColl[kd.name] = true
		}
		// XTSE1222 compares the EFFECTIVE @composite value (default "no"), so
		// one declaration writing composite="yes" while another omits it is a
		// conflict too (key-095) — unlike @collation, which only conflicts
		// when both declarations state it.
		if prev, ok := seenComp[kd.name]; ok && prev != kd.composite {
			return errAt(kd.el, "err:XTSE1222: xsl:key %q has conflicting composite declarations", kd.name)
		}
		seenComp[kd.name] = kd.composite
	}
	return nil
}

func (c *compiler) compileTemplate(el *xmltree.Node, order int) (*Template, error) {
	t := &Template{el: el, order: order, importPrec: c.importPrec, importLo: c.importLo}
	if m, ok := el.AttrLocal("match"); ok {
		t.matchSrc = m
		pat, err := parsePatternFor(el, m)
		if err != nil {
			return nil, errAt(el, "bad match pattern %q: %v", m, err)
		}
		if err := checkPatternGroupingFuncs(el, pat); err != nil {
			return nil, err
		}
		t.pattern = pat
		t.priority = pat.DefaultPriority()
	}
	if n, ok := el.AttrLocal("name"); ok {
		t.name = strings.TrimSpace(n)
	}
	if v, ok := el.AttrLocal("visibility"); ok {
		t.visibility = strings.TrimSpace(v)
	}
	if mode, ok := el.AttrLocal("mode"); ok {
		if strings.ContainsAny(mode, " \t\n\r#") {
			toks := strings.Fields(mode)
			for i, tok := range toks {
				if tok == "#default" {
					// XSLT 3.0: #default means "the default mode", which is
					// whatever [xsl:]default-mode is in scope (the unnamed
					// mode when none is).
					toks[i] = defaultModeWalk(el)
					continue
				}
				toks[i] = resolveModeName(el, tok)
			}
			t.modeToks = toks
		} else {
			t.mode = resolveModeName(el, mode)
		}
	} else {
		t.mode = defaultModeWalk(el)
	}
	if p, ok := el.AttrLocal("priority"); ok {
		trimmed := strings.TrimSpace(p)
		if !isXSDDecimalLexical(trimmed) {
			return nil, errAt(el, "err:XTSE0530: priority %q is not a valid number", p)
		}
		f, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, errAt(el, "bad priority %q", p)
		}
		t.priority = f
		t.hasPriority = true
	}
	if t.matchSrc == "" && t.name == "" {
		return nil, errAt(el, "xsl:template needs a match or name attribute")
	}
	if a, ok := el.AttrLocal("as"); ok {
		t.as = a
	}

	// xsl:param children (which must precede other content) define template
	// parameters; everything else (text + elements) is the body.
	for _, ch := range elementChildren(el) {
		if ch.Name.Space == NS && ch.Name.Local == "param" {
			vd, err := c.compileVarDef(ch, true)
			if err != nil {
				return nil, err
			}
			t.params = append(t.params, vd)
		}
	}
	ci, bodyNodes, err := splitContextItem(el)
	if err != nil {
		return nil, err
	}
	t.ctxItem = ci
	body, err := c.compileSequence(bodyNodes)
	if err != nil {
		return nil, err
	}
	t.body = body
	return t, nil
}

func (c *compiler) compileVarDef(el *xmltree.Node, isParam bool) (*VarDef, error) {
	vd := &VarDef{el: el, isParam: isParam}
	name, ok := el.AttrLocal("name")
	if !ok {
		return nil, errAt(el, "xsl:%s requires a name", el.Name.Local)
	}
	vd.name = resolveQName(el, name)
	if t, ok := el.AttrLocal("tunnel"); ok && xsltBool(t) {
		vd.tunnel = true
	}
	if a, ok := el.AttrLocal("as"); ok {
		vd.as = a
	}
	if rq, ok := el.AttrLocal("required"); ok && (rq == "yes" || rq == "true" || rq == "1") {
		vd.requiredParam = true
	}
	if isParam && vd.requiredParam {
		if _, hasSel := el.AttrLocal("select"); hasSel {
			// XTSE0010: an xsl:param (global or local, including
			// with-param's own required — though with-param has no
			// required attribute in practice, xsl:param does) may not
			// specify BOTH required="yes" and a default select — a
			// required parameter, by definition, never falls back to a
			// default (param-0111).
			return nil, errAt(el, "err:XTSE0010: xsl:%s may not specify both required=\"yes\" and a select attribute", el.Name.Local)
		}
	}
	if sel, ok := el.AttrLocal("select"); ok {
		// @select and non-empty content are mutually exclusive (XTSE0620 for
		// variables, XTSE0760 for params).
		for _, ch := range el.Children {
			bad := ch.Kind == xmltree.KindElement ||
				(ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "")
			if bad {
				code := "XTSE0620"
				if isParam || el.Name.Local == "with-param" || el.Name.Local == "param" {
					code = "XTSE0760"
				}
				return nil, errAt(el, "err:%s: xsl:%s has both a select attribute and content", code, el.Name.Local)
			}
		}
		p, err := parseXPathFor(el, sel)
		if err != nil {
			return nil, errAt(el, "bad select %q: %v", sel, err)
		}
		vd.sel = p
	} else {
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		vd.body = body
	}
	// "If an optional parameter has no select attribute and has an empty
	// sequence constructor, and there is an as attribute, then the default
	// value of the parameter is an empty sequence. If the empty sequence is
	// not a valid instance of the required type ..., the parameter is
	// treated as a required parameter" (error-0610d/iterate-902/static-012:
	// as="xs:decimal"/"xs:integer" with no default has NO valid empty-
	// sequence default, so it is a non-recoverable XTDE0700 when the caller
	// supplies no value — exactly like an explicit required="yes"). A "?"/"*"
	// occurrence (as-0129: "document-node()?") legitimately defaults to (),
	// so it stays optional.
	if isParam && !vd.requiredParam && vd.sel == nil && len(vd.body) == 0 && vd.as != "" {
		if _, ok := xpath.CoerceToDeclaredType(vd.as, xpath.Sequence{}); !ok {
			vd.requiredParam = true
		}
	}
	return vd, nil
}

// bindSettledStatic records on vd the value the static pass settled for a
// static="yes" global declaration (see VarDef.staticVal).
func (c *compiler) bindSettledStatic(vd *VarDef, el *xmltree.Node) {
	if st, _ := el.AttrLocal("static"); !isXSLTTrue(strings.TrimSpace(st)) {
		return
	}
	sd, ok := c.staticDecls[clark(vd.name.Space, vd.name.Local)]
	if !ok || sd.unset {
		return
	}
	vd.staticVal, vd.hasStatic = sd.value, true
}

// xmlSpacePreserved reports whether a stylesheet text node is under an
// xml:space="preserve" element (nearest xml:space ancestor wins).
func xmlSpacePreserved(textNode *xmltree.Node) bool {
	for el := textNode.Parent; el != nil; el = el.Parent {
		if el.Kind != xmltree.KindElement {
			continue
		}
		if v, ok := el.Attr("http://www.w3.org/XML/1998/namespace", "space"); ok {
			return v == "preserve"
		}
	}
	return false
}

// localVarReferenced reports whether anything in scope of a local xsl:variable
// — its following siblings and their descendants — could refer to $local.
// Deliberately conservative: any "$", optionally prefixed or Q{}-qualified,
// followed by the local name, anywhere in an attribute value or text node
// (XPath, AVT, TVT, pattern), counts, whatever namespace it would resolve to.
func localVarReferenced(el *xmltree.Node, local string) bool {
	p := el.Parent
	if p == nil {
		return true
	}
	after := false
	for _, sib := range p.Children {
		if sib == el {
			after = true
			continue
		}
		if after && mentionsVar(sib, local) {
			return true
		}
	}
	return false
}

func mentionsVar(n *xmltree.Node, local string) bool {
	switch n.Kind {
	case xmltree.KindText:
		return textMentionsVar(n.Value, local)
	case xmltree.KindElement:
		for _, a := range n.Attrs {
			if textMentionsVar(a.Value, local) {
				return true
			}
		}
		for _, c := range n.Children {
			if mentionsVar(c, local) {
				return true
			}
		}
	}
	return false
}

// textMentionsVar finds "$" [Q{...}] [prefix ":"] local not followed by a
// further name character.
func textMentionsVar(s, local string) bool {
	for i := strings.IndexByte(s, '$'); i >= 0 && i < len(s); i = strings.IndexByte(s, '$') {
		rest := s[i+1:]
		rest = strings.TrimLeft(rest, " \t\r\n")
		if strings.HasPrefix(rest, "Q{") {
			if j := strings.IndexByte(rest, '}'); j >= 0 {
				rest = rest[j+1:]
			}
		} else if j := strings.IndexByte(rest, ':'); j > 0 && j < len(rest)-1 && isNCNameText(rest[:j]) && rest[j+1] != ':' {
			rest = rest[j+1:]
		}
		if strings.HasPrefix(rest, local) {
			tail := rest[len(local):]
			if tail == "" || !isNameCharByte(tail[0]) {
				return true
			}
		}
		s = s[i+1:]
	}
	return false
}

// isBareVarRef reports whether an expression is exactly one variable
// reference ("$x", "$p:x", "$Q{u}x") and nothing else.
func isBareVarRef(expr string) bool {
	e := strings.TrimSpace(expr)
	if !strings.HasPrefix(e, "$") {
		return false
	}
	e = strings.TrimSpace(e[1:])
	if strings.HasPrefix(e, "Q{") {
		j := strings.IndexByte(e, '}')
		if j < 0 {
			return false
		}
		e = e[j+1:]
	} else if j := strings.IndexByte(e, ':'); j > 0 {
		if !isNCNameText(e[:j]) {
			return false
		}
		e = e[j+1:]
	}
	return isNCNameText(e)
}

// varRefsUnder collects the local names of every "$name" mentioned in el's
// own attributes and its subtree (attributes and text).
func varRefsUnder(el *xmltree.Node) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		for i := strings.IndexByte(s, '$'); i >= 0 && i < len(s); i = strings.IndexByte(s, '$') {
			rest := strings.TrimLeft(s[i+1:], " \t\r\n")
			if strings.HasPrefix(rest, "Q{") {
				if j := strings.IndexByte(rest, '}'); j >= 0 {
					rest = rest[j+1:]
				}
			} else if j := strings.IndexByte(rest, ':'); j > 0 && j < len(rest)-1 && isNCNameText(rest[:j]) && rest[j+1] != ':' {
				rest = rest[j+1:]
			}
			k := 0
			for k < len(rest) && isNameCharByte(rest[k]) {
				k++
			}
			if k > 0 && !seen[rest[:k]] {
				seen[rest[:k]] = true
				out = append(out, rest[:k])
			}
			s = s[i+1:]
		}
	}
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		switch n.Kind {
		case xmltree.KindText:
			add(n.Value)
		case xmltree.KindElement:
			for _, a := range n.Attrs {
				add(a.Value)
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
	}
	walk(el)
	return out
}

func isNCNameText(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isNameCharByte(s[i]) {
			return false
		}
	}
	return s != ""
}

// isNameCharByte is a byte-level over-approximation of an XML NameChar: any
// non-ASCII byte counts, so a reference is never missed on a Unicode name.
func isNameCharByte(b byte) bool {
	return b >= 0x80 || b == '_' || b == '-' || b == '.' ||
		(b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// stripDespiteXMLSpace reports whether a whitespace-only stylesheet text node is
// removed even under xml:space="preserve" (XSLT 3.0 §4.3): its immediate
// following sibling is xsl:param/xsl:sort/xsl:context-item/xsl:on-completion,
// its immediate preceding sibling is xsl:catch, or its parent is one of the
// elements whose whitespace children are always removed (whitespace-015: the
// runs before each xsl:sort of an xml:space="preserve" xsl:for-each go, the
// run after the last one stays). Comments and PIs are invisible to the
// adjacency test since §4.3 removes them first.
func stripDespiteXMLSpace(textNode *xmltree.Node) bool {
	p := textNode.Parent
	if p == nil || p.Kind != xmltree.KindElement {
		return false
	}
	if p.Name.Space == NS {
		switch p.Name.Local {
		case "accumulator", "analyze-string", "apply-imports", "apply-templates",
			"attribute-set", "call-template", "character-map", "choose", "evaluate",
			"fork", "merge", "merge-source", "mode", "next-iteration", "next-match",
			"override", "package", "stylesheet", "transform", "use-package":
			return true
		}
	}
	idx := -1
	for i, c := range p.Children {
		if c == textNode {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	isXSL := func(n *xmltree.Node, names ...string) bool {
		if n.Kind != xmltree.KindElement || n.Name.Space != NS {
			return false
		}
		for _, nm := range names {
			if n.Name.Local == nm {
				return true
			}
		}
		return false
	}
	for i := idx + 1; i < len(p.Children); i++ {
		c := p.Children[i]
		if c.Kind == xmltree.KindComment || c.Kind == xmltree.KindPI {
			continue
		}
		if isXSL(c, "param", "sort", "context-item", "on-completion") {
			return true
		}
		break
	}
	for i := idx - 1; i >= 0; i-- {
		c := p.Children[i]
		if c.Kind == xmltree.KindComment || c.Kind == xmltree.KindPI {
			continue
		}
		return isXSL(c, "catch")
	}
	return false
}

// expandTextFor returns the effective expand-text setting for a text node: the
// nearest ancestor element carrying an expand-text (xsl: elements) or
// xsl:expand-text (literal elements) attribute wins; otherwise it is off.
func (c *compiler) expandTextFor(textNode *xmltree.Node) bool {
	for el := textNode.Parent; el != nil; el = el.Parent {
		if el.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if el.Name.Space == NS {
			v, ok = el.AttrLocal("expand-text")
		} else {
			v, ok = el.Attr(NS, "expand-text")
		}
		if ok {
			// The attribute is an XSLT boolean, so surrounding whitespace is
			// insignificant (cvt-004..007 spell it " yes " / " true " / " 1 ").
			return isXSLTTrue(strings.TrimSpace(v))
		}
	}
	// expand-text is an INHERITED attribute scoped to one stylesheet module's
	// element tree: an imported module that declares none has it off, whatever
	// the importing module said (cvt-049). The walk above already finds the
	// principal module's own root, so there is no cross-module default here.
	return false
}

// isXSLTTrue reports whether an XSLT yes-or-no attribute value is true: the
// legacy "yes" plus the full xs:boolean lexical space ("true"/"1"), matching
// how xsl:key/@composite and xsl:param/@required are already read here.
func isXSLTTrue(v string) bool {
	return v == "yes" || v == "true" || v == "1"
}

// compileSequence compiles the children (in document order, including text) of
// a template-body element into instructions.
func (c *compiler) compileSequence(nodes []*xmltree.Node) ([]instruction, error) {
	var out []instruction
	for _, n := range mergeTextAcrossComments(nodes) {
		switch n.Kind {
		case xmltree.KindText:
			if isXMLSpaceOnly(n.Value) && (!xmlSpacePreserved(n) || stripDespiteXMLSpace(n)) {
				// Strip whitespace-only text nodes from the stylesheet.
				continue
			}
			if c.expandTextFor(n) {
				a, err := parseAVTFor(n, n.Value) // TVT uses the same {expr} syntax as AVTs
				if err != nil {
					return nil, err
				}
				if lit, ok := a.isConstant(); ok {
					out = append(out, &litText{text: lit})
				} else {
					out = append(out, &tvtText{a: a, el: n.Parent})
				}
				continue
			}
			out = append(out, &litText{text: n.Value})
		case xmltree.KindElement:
			ins, err := c.compileElement(n)
			if err != nil {
				return nil, err
			}
			if ins != nil {
				out = append(out, ins)
			}
		}
	}
	// xsl:on-empty / xsl:on-non-empty need the whole constructor's result
	// before deciding: wrap the list in a coordinating instruction. XTSE0010:
	// xsl:on-empty may only be followed by other on-empty/on-non-empty markers.
	hasMarker := false
	for i, in := range out {
		m, ok := in.(*onEmptyInstr)
		if !ok {
			continue
		}
		hasMarker = true
		if !m.nonEmpty && i != len(out)-1 {
			// xsl:on-empty must be the LAST instruction in the sequence
			// constructor — not merely the last content-generating one: an
			// xsl:on-non-empty after it is equally an error
			// (on-non-empty-013, alongside on-empty-003/004's ordinary
			// instructions).
			return nil, fmt.Errorf("err:XTSE0010: xsl:on-empty must be the last instruction in the sequence constructor")
		}
	}
	if hasMarker {
		return []instruction{&condContent{parts: out}}, nil
	}
	return out, nil
}

func (c *compiler) bodyOf(el *xmltree.Node) ([]instruction, error) {
	return c.compileSequence(childNodesForBody(el))
}

func (c *compiler) compileElement(el *xmltree.Node) (instruction, error) {
	if el.Name.Space != NS {
		if isExtensionElement(el) {
			return c.compileExtensionElement(el)
		}
		return c.compileLiteralElement(el)
	}
	// Registry-based instructions (instr_*.go) take precedence.
	if fn, ok := instrRegistry[el.Name.Local]; ok {
		return fn(c, el)
	}
	switch el.Name.Local {
	case "value-of":
		vo := &valueOf{el: el}
		if s, ok := el.AttrLocal("separator"); ok {
			sepavt, err := parseAVTFor(el, s)
			if err != nil {
				return nil, errAt(el, "bad separator %q: %v", s, err)
			}
			vo.sep = sepavt
		}
		if d, ok := el.AttrLocal("disable-output-escaping"); ok && xsltBool(d) {
			vo.doe = true
		}
		if sel, ok := el.AttrLocal("select"); ok {
			p, err := parseXPathFor(el, sel)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", sel, err)
			}
			vo.sel = p
		} else {
			// XSLT 3.0: xsl:value-of may take a sequence-constructor body.
			body, err := c.compileSequence(childNodesForBody(el))
			if err != nil {
				return nil, err
			}
			vo.body = body
		}
		return vo, nil
	case "text":
		var sb strings.Builder
		var firstText *xmltree.Node
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindText {
				if firstText == nil {
					firstText = ch
				}
				sb.WriteString(ch.Value)
			}
		}
		doe := false
		if d, ok := el.AttrLocal("disable-output-escaping"); ok && xsltBool(d) {
			doe = true
		}
		// Text value templates apply inside xsl:text too when expand-text is on.
		if firstText != nil && c.expandTextFor(firstText) {
			a, err := parseAVTFor(el, sb.String())
			if err != nil {
				return nil, err
			}
			if lit, ok := a.isConstant(); ok {
				return &textInstr{text: lit, doe: doe}, nil
			}
			return &tvtText{a: a, el: el}, nil
		}
		return &textInstr{text: sb.String(), doe: doe}, nil
	case "apply-templates":
		at := &applyTemplates{el: el}
		if sel, ok := el.AttrLocal("select"); ok {
			p, err := parseXPathFor(el, sel)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", sel, err)
			}
			at.sel = p
		}
		if mode, ok := el.AttrLocal("mode"); ok {
			if strings.TrimSpace(mode) == "#default" {
				at.mode = defaultModeWalk(el)
			} else {
				at.mode = resolveModeName(el, strings.TrimSpace(mode))
			}
		} else {
			at.mode = defaultModeWalk(el)
		}
		sorts, params, err := c.compileSortsAndParams(el)
		if err != nil {
			return nil, err
		}
		at.sorts = sorts
		at.params = params
		return at, nil
	case "for-each":
		sel, err := requireExpr(el, "select")
		if err != nil {
			return nil, err
		}
		fe := &forEach{sel: sel, el: el}
		sorts, _, err := c.compileSortsAndParams(el)
		if err != nil {
			return nil, err
		}
		fe.sorts = sorts
		body, err := c.compileSequence(nonSortChildren(el))
		if err != nil {
			return nil, err
		}
		fe.body = body
		return fe, nil
	case "if":
		test, err := requireExpr(el, "test")
		if err != nil {
			return nil, err
		}
		body, err := c.bodyOf(el)
		if err != nil {
			return nil, err
		}
		return &ifInstr{test: test, body: body, el: el}, nil
	case "choose":
		return c.compileChoose(el)
	case "variable":
		vd, err := c.compileVarDef(el, false)
		if err != nil {
			return nil, err
		}
		lv := &localVar{def: vd}
		if sel, ok := el.AttrLocal("select"); ok && isBareVarRef(sel) && len(elementChildren(el)) == 0 &&
			!localVarReferenced(el, vd.name.Local) {
			lv.unused = true
			lv.refs = varRefsUnder(el)
		}
		return lv, nil
	case "param":
		// stray param outside template head: treat as local variable
		vd, err := c.compileVarDef(el, true)
		if err != nil {
			return nil, err
		}
		return &localVar{def: vd}, nil
	case "call-template":
		name, ok := el.AttrLocal("name")
		if !ok {
			return nil, errAt(el, "xsl:call-template requires name")
		}
		_, params, err := c.compileSortsAndParams(el)
		if err != nil {
			return nil, err
		}
		// xsl:original, called from within an xsl:override's own body, invokes
		// the component it overrides (XSLT 3.0 §3.6.3.2) — redirected at
		// compile time to the synthesized name compileTopLevel registered the
		// shadowed original template under.
		if c.overrideOriginalName != "" && clarkName(resolveQName(el, name)) == clark(NS, "original") {
			return &callTemplate{name: c.overrideOriginalName, params: params, el: el}, nil
		}
		return &callTemplate{name: name, params: params, el: el}, nil
	case "attribute":
		name, err := requireAVT(el, "name")
		if err != nil {
			return nil, err
		}
		var nsavt *avt
		if v, ok := el.AttrLocal("namespace"); ok {
			nsavt, _ = parseAVTFor(el, v)
		}
		// @select (XSLT 2.0+) computes the value directly from an XPath
		// expression instead of a sequence-constructor body
		// (attribute-set-1813/1814); @separator (XSLT 3.0) then joins a
		// multi-item result (default: a single space).
		var sepavt *avt
		if s, ok := el.AttrLocal("separator"); ok {
			var serr error
			if sepavt, serr = parseAVTFor(el, s); serr != nil {
				return nil, errAt(el, "bad separator %q: %v", s, serr)
			}
		}
		val, err := compileValidation(el, vkAttribute)
		if err != nil {
			return nil, err
		}
		if v, ok := el.AttrLocal("select"); ok {
			sel, err := parseXPathFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", v, err)
			}
			return &attrInstr{name: name, ns: nsavt, sel: sel, sep: sepavt, el: el, val: val}, nil
		}
		body, err := c.bodyOf(el)
		if err != nil {
			return nil, err
		}
		return &attrInstr{name: name, ns: nsavt, body: body, sep: sepavt, el: el, val: val}, nil
	case "element":
		name, err := requireAVT(el, "name")
		if err != nil {
			return nil, err
		}
		if _, ok := el.AttrLocal("type"); ok && !schemaAwareRun() {
			// XTSE1660: @type requires schema-aware processing, which a basic
			// processor never provides (type-0303).
			return nil, errAt(el, "err:XTSE1660: xsl:element/@type requires a schema-aware processor")
		}
		var nsavt *avt
		if v, ok := el.AttrLocal("namespace"); ok {
			nsavt, _ = parseAVTFor(el, v)
		}
		val, err := compileValidation(el, vkElement)
		if err != nil {
			return nil, err
		}
		body, err := c.bodyOf(el)
		if err != nil {
			return nil, err
		}
		return &elemInstr{name: name, ns: nsavt, body: body, el: el, useSets: attrSetNames(el),
			noInheritNS: noInheritNamespaces(el), val: val}, nil
	case "copy":
		body, err := c.bodyOf(el)
		if err != nil {
			return nil, err
		}
		cval, err := compileValidation(el, vkCopy)
		if err != nil {
			return nil, err
		}
		ci := &copyInstr{body: body, el: el, useSets: attrSetNames(el), noInheritNS: noInheritNamespaces(el), val: cval}
		if v, ok := el.AttrLocal("copy-namespaces"); ok && !xsltBool(v) {
			ci.noCopyNS = true
		}
		if v, ok := el.AttrLocal("select"); ok {
			sel, err := parseXPathFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", v, err)
			}
			ci.sel = sel
		}
		return ci, nil
	case "copy-of":
		sel, err := requireExpr(el, "select")
		if err != nil {
			return nil, err
		}
		coval, err := compileValidation(el, vkCopyOf)
		if err != nil {
			return nil, err
		}
		co := &copyOf{sel: sel, el: el, val: coval}
		if v, ok := el.AttrLocal("copy-accumulators"); ok {
			if !isXSLTBooleanLexical(v) {
				return nil, errAt(el, "err:XTSE0020: copy-accumulators=%q is not an XSLT boolean", v)
			}
			co.copyAccumulators = isXSLTTrue(strings.TrimSpace(v))
		}
		if v, ok := el.AttrLocal("copy-namespaces"); ok && !xsltBool(v) {
			co.noCopyNS = true
		}
		return co, nil
	case "comment":
		// @select (XSLT 2.0+) computes the value directly from an XPath
		// expression instead of a sequence-constructor body.
		if v, ok := el.AttrLocal("select"); ok {
			sel, err := parseXPathFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", v, err)
			}
			return &commentInstr{sel: sel, el: el}, nil
		}
		body, err := c.bodyOf(el)
		if err != nil {
			return nil, err
		}
		return &commentInstr{body: body, el: el}, nil
	case "processing-instruction":
		name, err := requireAVT(el, "name")
		if err != nil {
			return nil, err
		}
		if v, ok := el.AttrLocal("select"); ok {
			sel, err := parseXPathFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad select %q: %v", v, err)
			}
			return &piInstr{name: name, sel: sel, el: el}, nil
		}
		body, err := c.bodyOf(el)
		if err != nil {
			return nil, err
		}
		return &piInstr{name: name, body: body, el: el}, nil
	case "sequence":
		// XSLT 3.0 allows either @select or a contained sequence constructor.
		if _, ok := el.AttrLocal("select"); ok {
			sel, err := requireExpr(el, "select")
			if err != nil {
				return nil, err
			}
			return &sequenceInstr{sel: sel, el: el}, nil
		}
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		return &sequenceInstr{body: body, el: el}, nil
	case "analyze-string":
		return c.compileAnalyzeString(el)
	case "sort", "with-param", "matching-substring", "non-matching-substring":
		// handled by their parent; ignore if encountered standalone
		return nil, nil
	case "message", "fallback", "number":
		// not yet implemented; skip silently
		return nil, nil
	case "context-item":
		// Reaching the instruction compiler at all means it is misplaced:
		// compileTemplate lifts a well-placed one out of the body before
		// compiling it (context-item-906 inside xsl:variable, -907 inside
		// xsl:function).
		return nil, errAt(el, "err:XTSE0010: xsl:context-item is allowed only as the first child of xsl:template")
	default:
		if inForwardsCompatScope(el) {
			return c.compileForwardsCompat(el)
		}
		return nil, errAt(el, "unsupported instruction xsl:%s", el.Name.Local)
	}
}

func (c *compiler) compileKey(el *xmltree.Node) (*KeyDef, error) {
	name, ok := el.AttrLocal("name")
	if !ok {
		return nil, errAt(el, "xsl:key requires a name")
	}
	// The key name is a QName resolved against the in-scope namespaces of the
	// xsl:key declaration; store its expanded (Clark) form so key('prefix:name',
	// ...) calls that use a DIFFERENT prefix bound to the same namespace URI
	// still find it (key-013: xsl:key uses prefix "baz", the key() call "bar",
	// both bound to the same URI).
	kd := &KeyDef{name: clarkName(resolveQName(el, name)), el: el}
	if comp, ok := el.AttrLocal("composite"); ok {
		kd.composite = comp == "yes" || comp == "1" || comp == "true"
		kd.hasComposite = true
	}
	if coll, ok := el.AttrLocal("collation"); ok {
		if _, err := xpath.ResolveCollator(coll); err != nil {
			return nil, errAt(el, "err:XTSE1210: xsl:key collation %q is not recognized by this processor", coll)
		}
		kd.collation = coll
		kd.hasCollation = true
	}
	if v, ok := el.AttrLocal("collation"); ok {
		kd.collURI = v
	} else {
		kd.collURI = defaultCollationWalk(el)
	}
	m, ok := el.AttrLocal("match")
	if !ok {
		return nil, errAt(el, "xsl:key requires a match")
	}
	pat, err := parsePatternFor(el, m)
	if err != nil {
		return nil, errAt(el, "bad key match %q: %v", m, err)
	}
	if err := checkPatternGroupingFuncs(el, pat); err != nil {
		return nil, err
	}
	kd.match = pat
	// @use and non-empty content are mutually exclusive, and exactly one of
	// them must be present (err:XTSE1205 — error-1205a/b): a use-attribute
	// key with a body, or a body-less key with no use attribute either, are
	// both static errors.
	hasContent := false
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindElement || (ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "") {
			hasContent = true
			break
		}
	}
	if useSrc, ok := el.AttrLocal("use"); ok {
		if hasContent {
			return nil, errAt(el, "err:XTSE1205: xsl:key has both a use attribute and non-empty content")
		}
		use, err := parseXPathFor(el, useSrc)
		if err != nil {
			return nil, errAt(el, "bad use %q: %v", useSrc, err)
		}
		kd.use = use
	} else {
		if !hasContent {
			return nil, errAt(el, "err:XTSE1205: xsl:key requires either a use attribute or non-empty content")
		}
		// No @use: the values come from a sequence-constructor body instead
		// (key-073/074 — mirrors xsl:variable's select-attribute-or-content
		// pattern).
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		kd.body = body
	}
	return kd, nil
}

func (c *compiler) compileFunction(el *xmltree.Node) (*FuncDef, error) {
	name, ok := el.AttrLocal("name")
	if !ok {
		return nil, errAt(el, "xsl:function requires a name")
	}
	fd := &FuncDef{name: resolveQName(el, name), el: el, importPrec: c.importPrec}
	if fd.name.Space == "" {
		return nil, errAt(el, "xsl:function name %q must be in a namespace", name)
	}
	if isReservedNS(fd.name.Space) {
		return nil, errAt(el, "err:XTSE0080: xsl:function name %q is in a reserved namespace", name)
	}
	if a, ok := el.AttrLocal("as"); ok {
		fd.as = a
	}
	if v, ok := el.AttrLocal("visibility"); ok {
		fd.visibility = strings.TrimSpace(v)
	}
	// new-each-time="no" makes the function DETERMINISTIC in the strong sense:
	// repeated calls with the same arguments must deliver the same node
	// identities (function-1025 counts distinct nodes, -1026 distinct
	// generate-id values). cache="yes" is the weaker "please reuse results"
	// request (function-1031's fib(92) is unusable without it). Both are
	// honoured by memoizing on the argument values.
	if v, ok := el.AttrLocal("new-each-time"); ok {
		switch strings.TrimSpace(v) {
		case "no", "false", "0":
			fd.memoize = true
		}
	}
	if v, ok := el.AttrLocal("cache"); ok && isXSLTTrue(strings.TrimSpace(v)) {
		fd.memoize = true
	}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space == NS && ch.Name.Local == "param" {
			// A stylesheet function's parameters must not specify a default
			// value: no select attribute, and empty content (XTSE0760) —
			// every call supplies every argument.
			if _, ok := ch.AttrLocal("select"); ok {
				return nil, errAt(ch, "err:XTSE0760: xsl:param inside xsl:function must not have a select attribute")
			}
			for _, gc := range ch.Children {
				if gc.Kind == xmltree.KindElement || (gc.Kind == xmltree.KindText && strings.TrimSpace(gc.Value) != "") {
					return nil, errAt(ch, "err:XTSE0760: xsl:param inside xsl:function must have empty content")
				}
			}
			vd, err := c.compileVarDef(ch, true)
			if err != nil {
				return nil, err
			}
			fd.params = append(fd.params, vd)
		}
	}
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	fd.body = body
	return fd, nil
}

func (c *compiler) compileAnalyzeString(el *xmltree.Node) (instruction, error) {
	sel, err := requireExpr(el, "select")
	if err != nil {
		return nil, err
	}
	regex, err := requireAVT(el, "regex")
	if err != nil {
		return nil, err
	}
	as := &analyzeString{sel: sel, regex: regex, el: el}
	if v, ok := el.AttrLocal("flags"); ok {
		a, err := parseAVTFor(el, v)
		if err != nil {
			return nil, errAt(el, "xsl:analyze-string bad flags %q: %v", v, err)
		}
		as.flags = a
	}
	hasMatching, hasNonMatching := false, false
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS {
			continue
		}
		switch ch.Name.Local {
		case "matching-substring":
			hasMatching = true
			body, err := c.compileSequence(childNodesForBody(ch))
			if err != nil {
				return nil, err
			}
			as.matching = body
		case "non-matching-substring":
			hasNonMatching = true
			body, err := c.compileSequence(childNodesForBody(ch))
			if err != nil {
				return nil, err
			}
			as.nonMatching = body
		}
	}
	if !hasMatching && !hasNonMatching {
		return nil, errAt(el, "err:XTSE1130: xsl:analyze-string requires an xsl:matching-substring or xsl:non-matching-substring child")
	}
	return as, nil
}

func (c *compiler) compileChoose(el *xmltree.Node) (instruction, error) {
	ch := &chooseInstr{}
	for _, w := range elementChildren(el) {
		if w.Name.Space != NS {
			continue
		}
		switch w.Name.Local {
		case "when":
			test, err := requireExpr(w, "test")
			if err != nil {
				return nil, err
			}
			body, err := c.bodyOf(w)
			if err != nil {
				return nil, err
			}
			ch.whens = append(ch.whens, whenClause{test: test, body: body, el: w})
		case "otherwise":
			body, err := c.bodyOf(w)
			if err != nil {
				return nil, err
			}
			ch.otherwise = body
		}
	}
	return ch, nil
}

// excludedResultURIs collects, from el and every stylesheet ancestor, the
// namespace URIs an exclude-result-prefixes (xsl:exclude-result-prefixes on a
// literal element; plain exclude-result-prefixes on an xsl: element) token
// resolves to at ITS OWN declaring element's scope — "#default" resolves to
// that scope's default-namespace URI, "#all" sets excludeAll (nothing is ever
// copied below that point).
func excludedResultURIs(el *xmltree.Node) map[string]bool {
	var uris map[string]bool
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("exclude-result-prefixes")
		} else {
			v, ok = cur.Attr(NS, "exclude-result-prefixes")
		}
		if !ok {
			continue
		}
		for _, tok := range strings.Fields(v) {
			switch tok {
			case "#all":
				// "#all indicates that all namespaces that are in scope FOR
				// THE STYLESHEET ELEMENT that is the parent of the
				// exclude-result-prefixes attribute are designated as
				// excluded namespaces" (XSLT 3.0 §5.6.2) — a fixed snapshot
				// taken AT cur, not "everything found anywhere in the
				// subtree forever after". The spec's own worked example:
				// exclude-result-prefixes="#all" on xsl:stylesheet (in
				// scope there: xsl, a, b) does NOT exclude xmlns:d="d.uri"
				// declared later on a literal result element deep inside a
				// template, because d.uri was never in scope at
				// xsl:stylesheet itself (copy-1220/1221: an unrelated
				// "w" prefix declared on a literal element nested inside
				// the template survives an ancestor #all the same way).
				for _, uri := range cur.InScopeNamespaces() {
					if uri == "" {
						continue
					}
					if uris == nil {
						uris = map[string]bool{}
					}
					uris[uri] = true
				}
			case "#default":
				if def, ok := cur.LookupPrefix(""); ok && def != "" {
					if uris == nil {
						uris = map[string]bool{}
					}
					uris[def] = true
				}
			default:
				if uri, ok := cur.LookupPrefix(tok); ok {
					if uris == nil {
						uris = map[string]bool{}
					}
					uris[uri] = true
				}
			}
		}
	}
	return uris
}

// extensionElementURIs collects, from el and every stylesheet ancestor, the
// namespace URIs an extension-element-prefixes (xsl:extension-element-prefixes
// on a literal element; plain extension-element-prefixes on an xsl: element —
// most commonly xsl:stylesheet/xsl:transform itself) token resolves to at ITS
// OWN declaring element's scope — "#default" resolves to that scope's
// default-namespace URI. Per XSLT §3.5 these namespaces are extension
// namespaces for el and every descendant, cumulatively across all declaring
// ancestors (version-005/032: a literal element in one of these namespaces
// that this processor doesn't implement as an extension instruction runs its
// xsl:fallback content instead of being copied through).
func extensionElementURIs(el *xmltree.Node) map[string]bool {
	var uris map[string]bool
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var v string
		var ok bool
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("extension-element-prefixes")
		} else {
			v, ok = cur.Attr(NS, "extension-element-prefixes")
		}
		if !ok {
			continue
		}
		for _, tok := range strings.Fields(v) {
			var uri string
			var found bool
			if tok == "#default" {
				uri, found = cur.LookupPrefix("")
			} else {
				uri, found = cur.LookupPrefix(tok)
			}
			if found && uri != "" {
				if uris == nil {
					uris = map[string]bool{}
				}
				uris[uri] = true
			}
		}
	}
	return uris
}

// isExtensionElement reports whether el's own namespace is an extension
// namespace in scope for it (see extensionElementURIs). This processor
// implements no extension instructions, so any such element is compiled as a
// fallback-driven extInstr rather than a literal result element.
func isExtensionElement(el *xmltree.Node) bool {
	if el.Name.Space == "" || el.Name.Space == NS {
		return false
	}
	return extensionElementURIs(el)[el.Name.Space]
}

// ambientResultNamespaces implements the XSLT rule that a literal result
// element carries every namespace binding that is in scope for it IN THE
// STYLESHEET (not just ones its own name/attributes happen to use), unless
// excluded via exclude-result-prefixes, aliased via xsl:namespace-alias, or
// it is the XSLT namespace itself (attribute-0601/1301: a "ped"/"bdd" prefix
// bound only on an ancestor xsl:stylesheet must still show up — and still
// occupy that prefix, forcing a real conflict's fixup elsewhere — on a
// literal result element deep inside it). already lists the namespace nodes
// explicitly declared ON el (so they are not duplicated).
// usedURIs marks the namespace URIs actually needed to represent el's own
// (post-alias) name or one of its attribute names — those survive exclusion
// below (namespace-0913/0914: even under exclude-result-prefixes="#all",
// nothing else would supply the binding an element's own QName needs).
func ambientResultNamespaces(el *xmltree.Node, already []*xmltree.Node, nsAlias map[string]xmltree.Name, usedURIs map[string]bool, neededPrefix map[string]string) []*xmltree.Node {
	ambient := el.InScopeNamespaces()
	if len(ambient) == 0 {
		return nil
	}
	excl := excludedResultURIs(el)
	have := make(map[string]bool, len(already))
	haveURI := make(map[string]bool, len(already))
	for _, ns := range already {
		have[ns.Name.Local] = true
		haveURI[ns.Value] = true
	}
	prefixes := make([]string, 0, len(ambient))
	for pfx := range ambient {
		prefixes = append(prefixes, pfx)
	}
	sort.Strings(prefixes) // deterministic output order
	var out []*xmltree.Node
	for _, pfx := range prefixes {
		uri := ambient[pfx]
		if uri == "" || uri == NS || have[pfx] {
			continue
		}
		if excl[uri] {
			// The used-URI exception only applies when nothing else already
			// provides this URI a binding (result-document-0217/0401: el's
			// own literal xmlns="..." already satisfies its name's need, so
			// a second, ambient prefix for the same excluded URI must still
			// be dropped, not duplicated).
			if !usedURIs[uri] || haveURI[uri] || pfx != neededPrefix[uri] {
				continue
			}
		}
		if _, aliased := nsAlias[uri]; aliased {
			continue
		}
		out = append(out, &xmltree.Node{Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: pfx}, Value: uri})
	}
	return out
}

func (c *compiler) compileLiteralElement(el *xmltree.Node) (instruction, error) {
	name := el.Name
	nsNodes := el.NS
	if alias, ok := c.nsAlias[name.Space]; ok {
		name = xmltree.Name{Local: name.Local, Space: alias.Space, Prefix: alias.Prefix}
	}
	if len(c.nsAlias) > 0 && len(nsNodes) > 0 {
		// Drop namespace nodes for aliased stylesheet URIs (the result
		// namespace is declared via the rewritten names instead).
		kept := nsNodes[:0:0]
		for _, ns := range nsNodes {
			if _, aliased := c.nsAlias[ns.Value]; !aliased {
				kept = append(kept, ns)
			}
		}
		nsNodes = kept
	}
	// exclude-result-prefixes applies to el's OWN namespace nodes too, not
	// just the ambient ones added below (namespace-0911: it excludes by
	// resolved URI, so a prefix re-declared here to an already-excluded URI
	// is dropped as well) — the namespace:: axis's normal ancestor-walk
	// inheritance then still finds an unexcluded outer binding of the same
	// prefix, if any, exactly as if this element had never redeclared it.
	// A URI actually used by el's own (post-alias) name or one of its
	// attribute names is never dropped — it is structurally required to
	// serialize that name at all (result-document-0217/0401: an excluded
	// prefix's URI, used as this very element's unprefixed default
	// namespace, must still appear).
	usedURIs := map[string]bool{name.Space: true}
	// neededPrefix pairs each used URI with the SPECIFIC prefix that el's own
	// name (or an attribute name) actually spells it with — not just any
	// prefix bound to that URI. Two different excluded prefixes can be bound
	// to the same URI (output-0138: "my" and "one" both -> ns.example.com);
	// only the one this particular element's name is written with may survive
	// the exclusion below, or a sibling element using the OTHER prefix for
	// the same URI would wrongly resurrect both bindings on every element.
	neededPrefix := map[string]string{name.Space: name.Prefix}
	for _, a := range el.Attrs {
		if a.Name.Space == NS || a.Name.Space == "" {
			continue
		}
		aSpace := a.Name.Space
		aPrefix := a.Name.Prefix
		if alias, ok := c.nsAlias[aSpace]; ok {
			aSpace = alias.Space
			aPrefix = alias.Prefix
		}
		usedURIs[aSpace] = true
		if _, ok := neededPrefix[aSpace]; !ok {
			neededPrefix[aSpace] = aPrefix
		}
	}
	// XSLT §11.1: the namespace nodes copied from a literal result element
	// NEVER include one whose string value is the XSLT namespace URI — under
	// ANY prefix, and whether declared on this element itself or inherited
	// (ambientResultNamespaces already skips the inherited ones). A stylesheet
	// that deliberately generates XSLT output does so through
	// xsl:namespace-alias, which rewrites the element's own name into that
	// namespace; that use is kept (next-match-021/023/025/026/039).
	if !usedURIs[NS] && len(nsNodes) > 0 {
		kept := nsNodes[:0:0]
		for _, ns := range nsNodes {
			if ns.Value != NS {
				kept = append(kept, ns)
			}
		}
		nsNodes = kept
	}
	if excl := excludedResultURIs(el); len(excl) > 0 && len(nsNodes) > 0 {
		kept := nsNodes[:0:0]
		for _, ns := range nsNodes {
			if usedURIs[ns.Value] || !excl[ns.Value] {
				kept = append(kept, ns)
			}
		}
		nsNodes = kept
	}
	nsNodes = append(nsNodes, ambientResultNamespaces(el, nsNodes, c.nsAlias, usedURIs, neededPrefix)...)
	leVal, err := compileValidation(el, vkElement)
	if err != nil {
		return nil, err
	}
	le := &litElement{
		name:    name,
		ns:      nsNodes,
		useSets: attrSetNames(el),
		el:      el,
		val:     leVal,
	}
	for _, a := range el.Attrs {
		// Skip attributes in the XSLT namespace on literal result elements.
		if a.Name.Space == NS {
			continue
		}
		av, err := parseAVTFor(el, a.Value)
		if err != nil {
			return nil, errAt(el, "bad attribute value template %q: %v", a.Value, err)
		}
		aname := a.Name
		if alias, ok := c.nsAlias[aname.Space]; ok && aname.Space != "" {
			aname = xmltree.Name{Local: aname.Local, Space: alias.Space, Prefix: alias.Prefix}
		}
		le.attrs = append(le.attrs, litAttr{name: aname, value: av})
	}
	body, err := c.compileSequence(el.Children)
	if err != nil {
		return nil, err
	}
	le.body = body
	return le, nil
}

// isValidLangLiteral reports whether s is a valid xsl:sort/@lang value: the
// xs:language lexical space (an xml:lang-shaped tag), reusing the engine's
// own xs:language cast rather than duplicating its lexical-space regex.
func isValidLangLiteral(s string) bool {
	_, err := xpath.CastTo(s, xpath.XSlanguage)
	return err == nil
}

// isValidStableLiteral reports whether s is a valid literal value for
// xsl:sort/@stable: an xs:boolean lexical form (whitespace-collapsed) or the
// XSLT-specific "yes"/"no" spelling.
func isValidStableLiteral(s string) bool {
	switch strings.TrimSpace(s) {
	case "true", "false", "1", "0", "yes", "no":
		return true
	}
	return false
}

func (c *compiler) compileSortsAndParams(el *xmltree.Node) ([]sortKey, []*VarDef, error) {
	var sorts []sortKey
	var params []*VarDef
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS {
			continue
		}
		switch ch.Name.Local {
		case "sort":
			sk := sortKey{el: ch}
			if v, ok := ch.AttrLocal("select"); ok {
				p, err := parseXPathFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad sort select %q: %v", v, err)
				}
				sk.sel = p
			} else {
				// XSLT 2.0+: with no @select, the sort key for each item is
				// computed by evaluating the xsl:sort element's own sequence-
				// constructor content (sort-004/051: the key can call a
				// function, or dispatch through xsl:apply-templates, rather
				// than being a plain XPath expression).
				body, err := c.compileSequence(childNodesForBody(ch))
				if err != nil {
					return nil, nil, err
				}
				sk.body = body
			}
			// data-type/order are attribute value templates (sort-041/042: a
			// stylesheet may compute data-type from a variable), so they are
			// resolved at sort time (transform.go resolveSortAttrs), not here.
			if v, ok := ch.AttrLocal("data-type"); ok {
				a, err := parseAVTFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad data-type %q: %v", v, err)
				}
				sk.dataType = a
			}
			if v, ok := ch.AttrLocal("order"); ok {
				a, err := parseAVTFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad order %q: %v", v, err)
				}
				sk.order = a
			}
			if v, ok := ch.AttrLocal("case-order"); ok {
				a, err := parseAVTFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad case-order %q: %v", v, err)
				}
				sk.caseOrder = a
			}
			if v, ok := ch.AttrLocal("collation"); ok {
				a, err := parseAVTFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad collation %q: %v", v, err)
				}
				sk.collation = a
			}
			if v, ok := ch.AttrLocal("lang"); ok {
				a, err := parseAVTFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad lang %q: %v", v, err)
				}
				// A literal (non-AVT) value must be a valid xml:lang-shaped
				// language code (XTSE0020, sort-028); a computed AVT value is
				// checked at sort time instead (XTDE0030, sort-029).
				if lit, isLit := a.isConstant(); isLit && !isValidLangLiteral(lit) {
					return nil, nil, errAt(ch, "err:XTSE0020: invalid value %q for xsl:sort/@lang", v)
				}
				sk.lang = a
			}
			if v, ok := ch.AttrLocal("stable"); ok {
				// XTSE1017: stable is only meaningful (and only allowed) on
				// the first of a group of sibling xsl:sort elements.
				if len(sorts) > 0 {
					return nil, nil, errAt(ch, "err:XTSE1017: stable is only allowed on the first xsl:sort")
				}
				a, err := parseAVTFor(ch, v)
				if err != nil {
					return nil, nil, errAt(ch, "bad stable %q: %v", v, err)
				}
				// A literal (non-AVT) value must be a valid xs:boolean lexical
				// form or the XSLT yes-or-no form (XTSE0020); a computed AVT
				// value is left for the (unimplemented) runtime check.
				if lit, isLit := a.isConstant(); isLit && !isValidStableLiteral(lit) {
					return nil, nil, errAt(ch, "err:XTSE0020: invalid value %q for xsl:sort/@stable", v)
				}
			}
			sorts = append(sorts, sk)
		case "with-param":
			vd, err := c.compileVarDef(ch, false)
			if err != nil {
				return nil, nil, err
			}
			params = append(params, vd)
		}
	}
	return sorts, params, nil
}

// --- small helpers ----------------------------------------------------------

func elementChildren(n *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, c := range n.Children {
		if c.Kind == xmltree.KindElement {
			out = append(out, c)
		}
	}
	return out
}

// childNodesForBody returns body children (text + elements) excluding leading
// xsl:param and xsl:sort/with-param housekeeping children.
func childNodesForBody(el *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, c := range el.Children {
		if c.Kind == xmltree.KindElement && c.Name.Space == NS {
			switch c.Name.Local {
			case "param", "sort", "with-param":
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func nonSortChildren(el *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, c := range el.Children {
		if c.Kind == xmltree.KindElement && c.Name.Space == NS && c.Name.Local == "sort" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func requireExpr(el *xmltree.Node, attr string) (*xpath.Parsed, error) {
	v, ok := el.AttrLocal(attr)
	if !ok {
		return nil, errAt(el, "xsl:%s requires a %s attribute", el.Name.Local, attr)
	}
	// Resolves any schema component name against the components the
	// stylesheet imported and el's own namespace bindings; identical to
	// xpath.Parse when none were (decl_import_schema.go).
	p, err := parseXPathFor(el, v)
	if err != nil {
		return nil, errAt(el, "bad %s %q: %v", attr, v, err)
	}
	return p, nil
}

func requireAVT(el *xmltree.Node, attr string) (*avt, error) {
	v, ok := el.AttrLocal(attr)
	if !ok {
		return nil, errAt(el, "xsl:%s requires a %s attribute", el.Name.Local, attr)
	}
	return parseAVTFor(el, v)
}

// resolveElementName resolves a QName in an "element name list" xsl:output
// attribute (cdata-section-elements, suppress-indentation): unlike
// resolveQName, an unprefixed name here takes the ambient default namespace
// (output-0138: cdata-section-elements="h1 ..." must match a literal <h1>
// that inherits xmlns="http://www.w3.org/1999/xhtml" from an ancestor) —
// these name QNames just like element/attribute names in the source, not
// like the no-namespace-by-default names used elsewhere (e.g. use-character-maps).
func resolveElementName(el *xmltree.Node, q string) xmltree.Name {
	if uri, local, ok := bracedEQName(q); ok {
		return xmltree.Name{Local: local, Space: uri}
	}
	prefix, local := "", q
	if i := strings.IndexByte(q, ':'); i >= 0 {
		prefix, local = q[:i], q[i+1:]
	}
	uri, _ := el.LookupPrefix(prefix) // prefix=="" resolves the default namespace
	return xmltree.Name{Local: local, Space: uri, Prefix: prefix}
}

// resolveQName builds an expanded name from a lexical QName using the
// in-scope namespaces of el. A braced EQName Q{uri}local carries its namespace
// explicitly (variable-0119: name="Q{v}var" binds under {v}var).
func resolveQName(el *xmltree.Node, q string) xmltree.Name {
	// A QName/EQName-typed attribute is whitespace-normalized before it is
	// interpreted (XSLT 3.0 §3.2), so name=" Q{}temp " names the same
	// component as name="temp" (call-template-0109).
	q = strings.TrimSpace(q)
	if uri, local, ok := bracedEQName(q); ok {
		return xmltree.Name{Local: local, Space: uri}
	}
	prefix, local := "", q
	if i := strings.IndexByte(q, ':'); i >= 0 {
		prefix, local = q[:i], q[i+1:]
	}
	uri := ""
	if prefix != "" {
		uri, _ = el.LookupPrefix(prefix)
	}
	return xmltree.Name{Local: local, Space: uri, Prefix: prefix}
}

// bracedEQName parses a Q{uri}local EQName (URI whitespace collapsed), matching
// the XPath lexer's handling.
func bracedEQName(s string) (uri, local string, ok bool) {
	if !strings.HasPrefix(s, "Q{") {
		return "", "", false
	}
	end := strings.IndexByte(s, '}')
	if end < 0 {
		return "", "", false
	}
	return strings.Join(strings.Fields(s[2:end]), " "), s[end+1:], true
}

// funcKey is the sheet.functions map key: xsl:function permits several
// declarations of the same name at DIFFERENT arities (function-1002), each an
// independent overload, so the name alone is not a unique key.
// patternReservedFuncNS are the namespaces whose functions are supplied by the
// processor itself rather than declared by the stylesheet; a call into any of
// them is left to the normal (runtime) function dispatch.
var patternReservedFuncNS = map[string]bool{
	NS:                                 true, // xsl:
	xpathFunctionsNS:                   true, // fn:
	"http://www.w3.org/2001/XMLSchema": true, // xs: constructor functions
	"http://www.w3.org/2005/xpath-functions/math":  true,
	"http://www.w3.org/2005/xpath-functions/map":   true,
	"http://www.w3.org/2005/xpath-functions/array": true,
	"http://www.w3.org/XML/1998/namespace":         true,
	"http://www.w3.org/2001/XMLSchema-instance":    true,
}

// checkPatternFunctions reports XPST0017 for a match pattern that calls a
// function in a stylesheet-defined namespace which no xsl:function actually
// declares. A pattern's predicates are only evaluated when a node is tested
// against them — and a failed pattern evaluation is (deliberately) treated as
// a non-match — so without this check an undeclared function in a pattern
// would silently make the rule unmatchable instead of being reported
// (match-040). Unprefixed calls resolve into the fn: namespace and are left to
// the core catalog; so is any call into a processor-supplied namespace.
func checkPatternFunctions(ss *Stylesheet) error {
	for _, t := range ss.templates {
		if t.pattern == nil || t.el == nil {
			continue
		}
		for _, fc := range t.pattern.FunctionCalls() {
			if fc.Prefix == "" {
				continue
			}
			uri, bound := t.el.LookupPrefix(fc.Prefix)
			if !bound || uri == "" || patternReservedFuncNS[uri] {
				continue
			}
			if _, ok := ss.functions[funcKey(uri, fc.Local, fc.Arity)]; !ok {
				return errAt(t.el, "err:XPST0017: no function %s:%s with %d argument(s) is in scope in the match pattern",
					fc.Prefix, fc.Local, fc.Arity)
			}
		}
	}
	return nil
}

func funcKey(uri, local string, arity int) string {
	return clark(uri, local) + "#" + strconv.Itoa(arity)
}

func clarkName(n xmltree.Name) string { return clark(n.Space, n.Local) }

// noInheritNamespaces reports whether an xsl:copy / xsl:element carries
// inherit-namespaces="no" — its constructed element children must not inherit
// its namespace declarations (see xmltree.Node.NSBarrier).
func noInheritNamespaces(el *xmltree.Node) bool {
	v, ok := el.AttrLocal("inherit-namespaces")
	return ok && !xsltBool(v)
}

// pruneCopiedNamespaces implements copy-namespaces="no": a copied element
// keeps only the namespace bindings its own name — or one of its attribute
// names — actually requires; every other declaration it carried is dropped
// (copy-0614/0615/0622/0623: an unused "s" prefix must not survive the copy).
func pruneCopiedNamespaces(n *xmltree.Node) {
	if n.Kind == xmltree.KindElement && len(n.NS) > 0 {
		need := map[string]bool{}
		if n.Name.Space != "" {
			need[n.Name.Space] = true
		}
		for _, a := range n.Attrs {
			if a.Name.Space != "" {
				need[a.Name.Space] = true
			}
		}
		// For each name, the binding kept is the one for the PREFIX the name
		// actually carries, falling back to any binding of its URI: a source
		// <Part> in the default namespace whose root also binds ns1 to that
		// URI must come out as <Part xmlns="...">, not ns1:Part (copy-4901).
		keep := map[*xmltree.Node]bool{}
		pick := func(name xmltree.Name) {
			if name.Space == "" {
				return
			}
			var fallback *xmltree.Node
			for _, ns := range n.NS {
				if ns.Value != name.Space {
					continue
				}
				if ns.Name.Local == name.Prefix {
					keep[ns] = true
					delete(need, name.Space)
					return
				}
				if fallback == nil {
					fallback = ns
				}
			}
			if fallback != nil && need[name.Space] {
				keep[fallback] = true
				delete(need, name.Space)
			}
		}
		pick(n.Name)
		for _, a := range n.Attrs {
			pick(a.Name)
		}
		kept := n.NS[:0]
		for _, ns := range n.NS {
			if keep[ns] {
				kept = append(kept, ns)
			}
		}
		n.NS = kept
		// Namespace fixup (XSLT 3.0 §5.7.3): whatever the copy's own
		// declarations happened to cover, the copy must still END UP with a
		// binding for every prefix its name and its attribute names use. A
		// source element that inherits its prefix from an ancestor declares
		// nothing itself, so pruning alone left it with none and the
		// serializer fell back to whatever namespace happened to be in scope
		// at the copy's new position (copy-5201: qri:QuoteRef, whose xmlns:qri
		// is declared on the source's root, lost its prefix entirely).
		addFixup(n, n.Name, need)
		for _, a := range n.Attrs {
			addFixup(n, a.Name, need)
		}
	}
	for _, c := range n.Children {
		pruneCopiedNamespaces(c)
	}
}

// addFixup gives n the namespace declaration one of its names requires, unless
// n already declares that prefix. need tracks which URIs still lack a kept
// declaration, so a namespace shared by the element name and several attributes
// is added once.
func addFixup(n *xmltree.Node, name xmltree.Name, need map[string]bool) {
	if name.Space == "" || !need[name.Space] {
		return
	}
	for _, ns := range n.NS {
		if ns.Name.Local == name.Prefix {
			return // that prefix is already bound on this element
		}
	}
	delete(need, name.Space)
	n.NS = append(n.NS, &xmltree.Node{
		Kind:   xmltree.KindNamespace,
		Name:   xmltree.Name{Local: name.Prefix},
		Value:  name.Space,
		Parent: n,
	})
}

// markNSBarrier applies inherit-namespaces="no" to everything an instruction's
// body appended to el after mark: each new ELEMENT child becomes a namespace-
// inheritance barrier.
func markNSBarrier(el *xmltree.Node, mark int) {
	for _, c := range el.Children[mark:] {
		if c.Kind == xmltree.KindElement {
			c.NSBarrier = true
		}
	}
}

// sortInstrUnused keeps the sort import referenced (sorting is in transform.go).
var _ = sort.Strings

// isXMLSpaceOnly reports whether s consists solely of XML whitespace (space,
// tab, LF, CR) — the ONLY four characters XSLT's stylesheet whitespace
// stripping may remove. strings.TrimSpace would be wrong here: Go's
// unicode.IsSpace also covers U+0085 NEL and U+00A0 NBSP, which are ordinary
// characters to XML and must survive into the result (xml-version-023's
// <nel>&#x85;</nel>).
func isXMLSpaceOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

// checkGlobalSelfReference reports XPST0008 when a global variable/parameter's
// own @select refers to the variable being declared: a global variable is OUT
// OF SCOPE within its own declaration (XSLT 3.0 §9.5), so such a reference is
// STATICALLY unbound — not a circularity (XTDE0640), which is why it must be
// caught here rather than at evaluation time. The two differ observably when
// the reference sits inside an inline function that is only invoked later, by
// which point the variable IS bound and nothing would ever complain
// (variable-0118, higher-order-functions-070: a recursive gcd written as
// "function($x,$y){ ... $gcd($y, $x mod $y) }").
//
// The comparison is by the name's LEXICAL spelling, and a reference shadowed by
// an enclosing for/let/some/every binding or inline-function parameter does not
// count — so the check under-reports rather than ever rejecting a valid
// stylesheet.
func checkGlobalSelfReference(vd *VarDef, el *xmltree.Node) error {
	if vd == nil || vd.sel == nil {
		return nil
	}
	name, _ := el.AttrLocal("name")
	name = strings.TrimSpace(name)
	prefix, local := "", name
	if i := strings.IndexByte(name, ':'); i > 0 {
		prefix, local = name[:i], name[i+1:]
	}
	if local == "" {
		return nil
	}
	if vd.sel.UsesVariable(prefix, local) {
		return errAt(el, "err:XPST0008: $%s is out of scope within its own declaration", name)
	}
	return nil
}

// mergeTextAcrossComments applies the part of stylesheet-tree preparation that
// makes a comment or processing instruction INVISIBLE to the constructor it
// sits in: such a node contributes nothing, and the text nodes it separated are
// then adjacent and form a single text node.
//
// This matters because a text value template may legally be split by one:
// <out expand-text="yes">The Lord of the {str<!--n-->ing($p)} Rings</out> is
// one TVT, and compiling the two text nodes separately fails on an unbalanced
// '{' (cvt-043/044/050). Merging also runs BEFORE the whitespace-only strip in
// compileSequence, so a run whose FIRST piece is whitespace is no longer
// discarded (whitespace-012/013).
//
// The stylesheet tree itself is deliberately NOT mutated: it is reachable via
// doc(”)/document(”), where the comments must still be there.
func mergeTextAcrossComments(nodes []*xmltree.Node) []*xmltree.Node {
	need := false
	for i, n := range nodes {
		if i > 0 && n.Kind == xmltree.KindText &&
			(nodes[i-1].Kind == xmltree.KindComment || nodes[i-1].Kind == xmltree.KindPI) {
			need = true
			break
		}
	}
	if !need {
		return nodes
	}
	out := make([]*xmltree.Node, 0, len(nodes))
	var lastText *xmltree.Node // the merged node currently at the tail of out
	var lastSrc *xmltree.Node  // the ORIGINAL node it was last extended from
	for _, n := range nodes {
		if n.Kind == xmltree.KindComment || n.Kind == xmltree.KindPI {
			continue // invisible: skipped without breaking an adjacent text run
		}
		if n.Kind == xmltree.KindText && lastText != nil && onlyCommentsBetween(lastSrc, n) {
			merged := xmltree.NewText(lastText.Value + n.Value)
			merged.Parent = lastText.Parent
			merged.Line, merged.Col = lastText.Line, lastText.Col
			out[len(out)-1] = merged
			lastText, lastSrc = merged, n
			continue
		}
		out = append(out, n)
		if n.Kind == xmltree.KindText {
			lastText, lastSrc = n, n
		} else {
			lastText, lastSrc = nil, nil
		}
	}
	return out
}

// onlyCommentsBetween reports whether a and b are siblings with nothing but
// comments and processing instructions between them. Without this check a text
// node could be merged across a sibling the CALLER filtered out for its own
// reasons — childNodesForBody drops xsl:param, so two text nodes separated by
// one must stay separate.
func onlyCommentsBetween(a, b *xmltree.Node) bool {
	if a == nil || b == nil || a.Parent == nil || a.Parent != b.Parent {
		return false
	}
	sibs := a.Parent.Children
	ai, bi := -1, -1
	for i, ch := range sibs {
		if ch == a {
			ai = i
		}
		if ch == b {
			bi = i
		}
	}
	if ai < 0 || bi < 0 || bi <= ai {
		return false
	}
	for _, ch := range sibs[ai+1 : bi] {
		if ch.Kind != xmltree.KindComment && ch.Kind != xmltree.KindPI {
			return false
		}
	}
	return true
}
