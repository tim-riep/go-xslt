package xsd

import (
	"github.com/tim-riep/go-xslt/internal/fsaccess"
	"github.com/tim-riep/go-xslt/internal/xmltree"

	"fmt"
	"regexp"
	"sync"

	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Namespace URIs.
const (
	xsNS    = "http://www.w3.org/2001/XMLSchema"
	xsiNS   = "http://www.w3.org/2001/XMLSchema-instance"
	xlinkNS = "http://www.w3.org/1999/xlink"
)

// xname is an expanded (namespace-qualified) name — the identity key for schema
// components and instance elements.
type xname struct{ Space, Local string }

func (n xname) String() string {
	if n.Space == "" {
		return n.Local
	}
	return "{" + n.Space + "}" + n.Local
}

func (n xname) zero() bool { return n.Space == "" && n.Local == "" }

// Type is a simple or complex type definition.
type Type interface{ isType() }

func (*SimpleType) isType()  {}
func (*ComplexType) isType() {}

// --- simple types -----------------------------------------------------------

type variety int

const (
	vAtomic variety = iota
	vList
	vUnion
)

// SimpleType is a compiled XSD simple type (atomic, list, or union).
type SimpleType struct {
	name xname // zero for anonymous types

	variety variety

	prim        xpath.AtomType // resolved built-in primitive at the bottom of the base chain
	base        *SimpleType    // immediate base simple type (nil when the base is a built-in)
	baseBuiltin bool           // true when base is a built-in (prim is authoritative)
	facets      facetSet
	docs        []Documentation // CompileWithDocs only

	item    *SimpleType   // list
	members []*SimpleType // union; never leads back to this type (closeUnion)

	// declared marks a USER-DEFINED type (global or inline) as opposed to the
	// sentinel a bare builtin reference materializes: an anonymous facet-less
	// <restriction base="xsd:integer"/> is structurally identical to one but a
	// distinct component (particlesIk025: derivation needs membership, not a
	// same-primitive sibling).
	declared bool

	final map[string]bool // derivation methods this type forbids (restriction/list/union)

	// assertions: XSD 1.1 xs:assertion facets declared AT THIS restriction
	// step ($value-bound XPath). They accumulate down the base chain — each
	// step's list runs when that step's facets do.
	assertions []*assertion
}

// facetSet holds the constraining facets declared at one restriction step.
type facetSet struct {
	patterns [][]*regexp.Regexp
	// patternSrc is patterns' source text, group for group (CompileWithDocs only).
	patternSrc [][]string

	enumeration []string
	// enumQName is enumeration expanded against the FACET element's own
	// namespace bindings, in Clark form, one entry per enumeration entry
	// ("" where the value is not a resolvable QName). Read only for an
	// xs:QName/xs:NOTATION-based type, whose enumeration is a set of expanded
	// names rather than of lexical strings (see checkPatternEnumIn).
	enumQName []string
	hasEnum   bool

	length, minLength, maxLength          int
	hasLength, hasMinLength, hasMaxLength bool

	minIncl, maxIncl, minExcl, maxExcl             string
	hasMinIncl, hasMaxIncl, hasMinExcl, hasMaxExcl bool

	totalDigits, fractionDigits       int
	hasTotalDigits, hasFractionDigits bool

	whiteSpace string
	explicitTZ string

	// fixed marks facets declared with fixed="true" at this step (by facet
	// element name); a restriction may not change a fixed facet's value.
	fixed map[string]bool
}

// --- complex types ----------------------------------------------------------

type contentKind int

const (
	contentEmpty       contentKind = iota // no content
	contentSimple                         // character data validated by a simple type
	contentElementOnly                    // child elements per a particle
	contentMixed                          // character data interspersed with child elements
)

// ComplexType is a compiled complex type. Content model and attributes are the
// definition at THIS derivation step; the effective (post-derivation) values are
// computed on demand via effectiveParticle/effectiveAttrs, which walk the base
// chain (so parse order and forward references do not matter).
type ComplexType struct {
	name xname // zero for anonymous types

	kind         contentKind
	docs         []Documentation // CompileWithDocs only
	simpleType   *SimpleType     // contentSimple
	particle     *particle       // this step's element/mixed content (nil ⇒ empty)
	attrUses     []*attrUse
	attrWildcard *wildcard
	abstract     bool

	baseType   Type            // derivation base (nil ⇒ implicitly xs:anyType)
	derivation string          // "extension" | "restriction" | ""
	final      map[string]bool // derivation methods this type forbids as a base
	block      map[string]bool // derivation methods this type forbids for xsi:type substitution
	asserts    []*assertion    // XSD 1.1 xs:assert (this step's)
	// openContent is the XSD 1.1 {open content} declared AT THIS STEP (explicit
	// xs:openContent, or the document's xs:defaultOpenContent materialized at
	// compile time). mode "none" records an explicit opt-out that shadows the
	// base's; the effective value walks the base chain (effectiveOpenContent).
	openContent *openContent
}

// openContent is an XSD 1.1 {open content} property: extra children the content
// model admits, anywhere (interleave) or after the model (suffix).
type openContent struct {
	mode string // "interleave" | "suffix" | "none"
	wc   *wildcard
}

// assertion is an XSD 1.1 xs:assert / simple-type xs:assertion.
type assertion struct {
	test      *xpath.Parsed
	ns        map[string]string
	defaultNS string
	// msgs are the msg:message entries of the assertion's xs:appinfo (see
	// messages.go); they only enrich the diagnostic, never the verdict.
	msgs []assertMsg
}

// typeAlternative is an XSD 1.1 xs:alternative (conditional type assignment). A
// nil test is the default alternative.
type typeAlternative struct {
	test      *xpath.Parsed
	rawTest   string // the @test source text ("" for the default alternative)
	typ       Type
	ns        map[string]string
	defaultNS string
}

// particle is a term with occurrence bounds. max == unbounded for "unbounded".
type particle struct {
	min, max int
	term     term
	docs     *[]Documentation // an xs:element particle's own annotation (CompileWithDocs only)
	// det memoizes bigDeterministic(p): 0 not yet computed, 1 deterministic,
	// 2 not. Accessed atomically — a compiled particle is shared by every
	// concurrent validation of its schema.
	det int32
}

const unbounded = -1

type term interface{ isTerm() }

func (*ElementDecl) isTerm() {}
func (*modelGroup) isTerm()  {}
func (*wildcard) isTerm()    {}

type compositor int

const (
	cSeq compositor = iota
	cChoice
	cAll
)

type modelGroup struct {
	compositor compositor
	particles  []*particle
	// msgs are the group's msg:message entries (see messages.go): own wording
	// for a cardinality error of the group's members.
	msgs []assertMsg
}

// wildcard is an xs:any / xs:anyAttribute.
type wildcard struct {
	// nsMode: "any", "other", "list"
	nsMode     string
	namespaces []string // for "list" (##local ⇒ "")
	targetNS   string   // schema target namespace (for ##other in 1.0)
	process    string   // strict | lax | skip
	// XSD 1.1 negations. notNS holds @notNamespace (##local ⇒ "",
	// ##targetNamespace ⇒ the target namespace); notNames holds the QNames of
	// @notQName, with ##defined already expanded to the schema's global
	// declarations and ##definedSibling filled in from the enclosing content
	// model (see fillWildcardSiblings).
	notNS      []string
	notNames   []xname
	notSibling bool
	// notDefined records that @notQName listed ##defined, whose expansion lives
	// in notDefNames. The two are kept apart because a wildcard union only keeps
	// ##defined when *every* operand carries it (XSD 1.1 Attribute Wildcard
	// Union), while explicit names intersect naturally under union matching.
	notDefined  bool
	notDefNames []xname
	// parts, when non-empty, makes this a union wildcard: a namespace matches iff
	// it matches any part. Produced when a type extends a base and both carry an
	// attribute wildcard (the effective wildcard is their union).
	parts []*wildcard
}

// --- declarations -----------------------------------------------------------

// ElementDecl is a compiled element declaration (global or local).
type ElementDecl struct {
	name        xname
	docs        []Documentation // CompileWithDocs only
	msgs        []assertMsg     // msg:message entries of the declaration's appinfo
	typ         Type
	nillable    bool
	abstract    bool
	substGroups []xname // affiliations (global elements; a 1.1 list may name several heads)
	// typeDefaulted marks a declaration with neither @type nor an inline type:
	// its anyType default may be replaced by a substitution-group head's type.
	typeDefaulted bool
	def, fixed    string
	hasDefault    bool
	hasFixed      bool
	blocked       map[string]bool // derivation methods blocked for xsi:type substitution
	final         map[string]bool // derivation methods this element forbids for substitution-group members
	constraints   []*identityConstraint
	alternatives  []*typeAlternative // XSD 1.1 conditional type assignment
}

// identityConstraint is a compiled xs:key / xs:keyref / xs:unique.
type identityConstraint struct {
	kind     string // "key" | "keyref" | "unique"
	name     xname
	refer    xname             // keyref only
	selector *xpath.Parsed     // restricted XPath, relative to the scope element
	fields   []*xpath.Parsed   // one per <field>
	fieldRaw []string          // the fields' raw XPath (for absent-attr defaults)
	fieldSrc []string          // each field's source XPath, for value-constraint lookup
	ns       map[string]string // in-scope namespaces for the XPath
	selNS    string            // resolved xpathDefaultNamespace for the selector (1.1)
	fieldNS  []string          // resolved xpathDefaultNamespace per field (1.1)
	// refName/refPending: an XSD 1.1 <xs:key ref="..."/> use — the referenced
	// definition is copied in during checkKeyrefs' resolution pass (id040/043/
	// 044, ibm s2_2_4). A ref use is NOT a re-declaration.
	refName    xname
	refPending bool
}

// AttributeDecl is a compiled attribute declaration.
type AttributeDecl struct {
	name xname
	docs []Documentation // CompileWithDocs only
	msgs []assertMsg     // msg:message entries of the declaration's appinfo
	typ  *SimpleType
	// inheritable: the XSD 1.1 {inheritable} property — descendants' CTA tests
	// see the attribute (cta0009/cta0014).
	inheritable bool
	// The declaration's own value constraint, which a reference to it may repeat
	// but never contradict.
	def, fixed string
	hasDefault bool
	hasFixed   bool
}

type attrUse struct {
	docs       []Documentation // the xs:attribute's own annotation (CompileWithDocs only)
	msgs       []assertMsg     // msg:message entries of the xs:attribute's appinfo
	decl       *AttributeDecl
	required   bool
	prohibited bool
	def, fixed string
	hasDefault bool
	hasFixed   bool
	// inheritable: the USE-level {inheritable}, which overrides the referenced
	// declaration's (cta0012: ref="lang" inheritable="false" wins).
	inheritable bool
}

// Schema is a compiled set of schema documents (the schema component model).
type Schema struct {
	version    Version
	baseDir    string
	files      *fsaccess.Guard // checks every schema document read from disk; nil: no check
	sourceDocs []string        // the schema documents Compile was given (for hint merges)
	elements   map[xname]*ElementDecl
	types      map[xname]Type
	attributes map[xname]*AttributeDecl
	// substitution groups: head → members (transitive closure computed on demand)
	substMembers map[xname][]*ElementDecl
	// ids is the ID/IDREF binding of the instance being validated. Validate works
	// on a shallow copy of the Schema so this stays per-call.
	ids *idTable
	// typeCache memoises the type governing each element of the instance being
	// validated (see declaredTypeOf); per-call, like ids.
	typeCache map[*xmltree.Node]Type
	// childModels memoises the static part of validateChildren per complex
	// type (effective particle, open content, declared names, fixed values,
	// wildcards by processContents) — per-call, like ids; nil means "do not
	// cache" (the information is then computed per element, as before).
	childModels map[*ComplexType]*childModel
	// unparsed holds the instance DTD's unparsed (NDATA) entity names — the
	// xs:ENTITY value space (1.1 enforcement; per-call, like ids).
	// anon is the registry of synthetic identities for ANONYMOUS types (see
	// Schema.anonName). A pointer so every per-call copy of this Schema shares
	// one numbering.
	anon *anonReg

	unparsed map[string]bool
	// lenientEntities drops the XSD-1.1-only "an xs:ENTITY value must name an
	// unparsed entity" rule. Set ONLY by ValidateNode (the XSLT bridge), never
	// on the string Validate path, so the XSD conformance floors cannot move.
	//
	// The bridge compiles every schema as 1.1 regardless of the version the
	// host asked for, which turns that 1.1-only rule on for schemas written
	// against 1.0 — as-3301/3401 and match-208/209 supply a 1.0
	// source-reference schema over an instance whose &ENTITY; is an ordinary
	// internal PARSED entity, which 1.0 never constrained.
	lenientEntities bool
	// unassessed marks instance subtree roots absorbed by a skip wildcard —
	// identity-constraint selectors must not see into them (1.1 §3.11.4,
	// wild101/102/103; per-call, like ids).
	unassessed map[*xmltree.Node]bool
	// hasAsserts is set at compile time when any xs:assert was parsed. The
	// only reader of assertAnnos is assertTree, reached through
	// checkAsserts solely for a complex type with effective assertions — and
	// solely under XSD 1.1 — so a Validate call under 1.0, or over a schema
	// without a single xs:assert, leaves the map nil and records nothing
	// (recording cost a map entry per simple-typed element).
	hasAsserts bool
	// assertAnnos: per-Validate-call typed-XDM annotations (element or
	// attribute -> what its simple content validated against); consumed by
	// assertTree, which copies them onto the assertion tree. One entry per
	// simple-typed node of the instance, so the value is kept to a word
	// (see assertAnno); a list's item type name lives in assertListItems.
	assertAnnos map[*xmltree.Node]assertAnno
	// assertListItems: the item type's own name for the nodes assertAnnos
	// marks as lists, when it has one. Same lifetime as assertAnnos.
	assertListItems map[*xmltree.Node]*xmltree.SchemaTypeName
	// coll collects every error of a Validate call (nil on ValidateNode).
	coll *collector
	// interrupt, set for one ValidateOptsCtx call, is polled for every
	// element validated; a non-nil error cancels the validation.
	interrupt func() error
	// nodeTypes records the schema type each validated node was assessed
	// against, for ValidateNode's in-place annotation (bridge.go). nil on the
	// Validate path, which annotates nothing — see noteNodeType.
	nodeTypes map[*xmltree.Node]Type
	// nodeNilled records which validated elements were accepted as EMPTY via
	// xsi:nil="true" against a nillable declaration — the XDM [nilled]
	// property, which a node's type annotation alone cannot express (a nilled
	// element keeps its declared type). Same lifetime and nil-ness as
	// nodeTypes: ValidateNode's annotation path only.
	nodeNilled map[*xmltree.Node]bool
	// nodeDefaults records the attribute uses whose value constraint SUPPLIED
	// a value because the instance omitted the attribute (XSD 1.0 §3.4.4
	// clause 4 / cvc-complex-type.4: an absent attribute use with a default or
	// fixed value contributes that value to the PSVI). For ValidateNode those
	// attributes have to appear on the real tree — XSLT 3.0 §4.3 says outright
	// that "any defaulted elements and attributes that were added to the tree
	// by the validation process will still be present" even after annotations
	// are stripped. Same lifetime and nil-ness as nodeTypes.
	nodeDefaults map[*xmltree.Node][]defaultAttr
	// scratch is the per-call space the evaluation of assertions and
	// identity constraints reuses from one element to the next (see
	// checkAsserts): scratchBuf of the per-call copy, so that it costs no
	// allocation of its own. nil, as on a Schema not copied for a call,
	// means allocate afresh each time.
	scratch    *validateScratch
	scratchBuf validateScratch
}

// validateScratch holds what checkAsserts and the identity-constraint
// evaluation would otherwise allocate per element: the allocator the
// assertion tree is carved from (Reset for every tree), the annotations the
// tree's counting walk looks up for its cloning walk, and the XPath
// evaluation contexts. A context is reused only while no function item
// captured it (Captured, and the ContextCaptured notice to vars); busy
// guards the assertion fields against a nested use.
type validateScratch struct {
	tree  xmltree.TreeAlloc
	annos []assertAnno
	ctx   *xpath.Context
	vars  *valueVar
	busy  bool
	// icCtx evaluates identity-constraint selectors and fields; tuple is the
	// buffer a key tuple is built in (tupleFor).
	icCtx *xpath.Context
	tuple []byte
}

// assertAnno is the typed-XDM description of a validated node for assertion
// evaluation — see Schema.assertAnnos: the primitive an atomic value
// validated against, or, for a LIST-varietied simple type whose typed value
// is a SEQUENCE and so cannot be described by one primitive alone, the ITEM
// type's primitive and name — the same {item primitive, item type name}
// pair xmltree.Node.TypeAnno + ListTyped + ListItemType carry everywhere
// else.
//
// It is one word: an xpath.AtomType is an int, but every value fits an
// int32 (Node.TypeAnno is one), and a wider value here would cost a few
// bytes per simple-typed node of a large instance.
type assertAnno struct {
	prim int32 // the type's primitive (a list's: the ITEM type's), an xpath.AtomType
	list bool  // a list: the typed value is a sequence
}

// defaultAttr is one attribute a value constraint supplied for an element that
// omitted it — see Schema.nodeDefaults.
type defaultAttr struct {
	name  xname
	value string
	typ   Type
}

// idTable records the ID/IDREF binding of one instance document: which element
// each ID value is bound to, and every IDREF value awaiting resolution.
type idBinding struct{ el, src *xmltree.Node }

// anonReg hands out and resolves the synthetic names of anonymous types.
type anonReg struct {
	mu     sync.Mutex
	byType map[Type]xname
	byName map[xname]Type
	n      int
}

type idTable struct {
	bound map[string]idBinding
	refs  []string
	// elementScope suppresses the cvc-id.2 uniqueness conflict. ID/IDREF are
	// DOCUMENT-level constraints: XSLT 3.0 §24 gives them their own error
	// (XTTE1555, "it is a type error if, when validating a DOCUMENT NODE,
	// document-level constraints (such as ID/IDREF constraints) are not
	// satisfied"), so an episode whose validation root is an ELEMENT — which
	// is what [xsl:]validation on a literal result element, xsl:element,
	// xsl:copy or xsl:copy-of requests — must not raise them
	// (validation-1601..1607 construct a deliberately ID-duplicating element
	// and require the result to be typed).
	elementScope bool
}

// invalidf builds a genuine "invalid" error (not ErrUnsupported).
func invalidf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
