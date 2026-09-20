package xsd

import (
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

	item    *SimpleType   // list
	members []*SimpleType // union

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
	simpleType   *SimpleType // contentSimple
	particle     *particle   // this step's element/mixed content (nil ⇒ empty)
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
	sourceDocs []string // the schema documents Compile was given (for hint merges)
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
	// assertTypes: per-Validate-call typed-XDM annotations (element -> the
	// primitive its simple content validated against); consumed by assertTree.
	assertTypes map[*xmltree.Node]xpath.AtomType
	// assertLists is assertTypes for a LIST-varietied simple type, whose typed
	// value is a SEQUENCE and so cannot be described by one primitive alone —
	// the same {item primitive, item type name} pair xmltree.Node.TypeAnno +
	// ListTyped + ListItemType already carry everywhere else. Consumed by
	// assertTree; same per-call lifetime as assertTypes.
	assertLists map[*xmltree.Node]assertList
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
}

// assertList is the typed-XDM description of a LIST-varietied node for
// assertion evaluation — see Schema.assertLists.
type assertList struct {
	prim xpath.AtomType          // the ITEM type's primitive
	item *xmltree.SchemaTypeName // the item type's own name, when it has one
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
