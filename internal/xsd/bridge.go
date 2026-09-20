package xsd

// The XSLT bridge: component lookup by QName, derivation queries, and
// ValidateNode — validating a tree that ALREADY EXISTS rather than one parsed
// from a string.
//
// Schema-awareness (XSLT 3.0 §26.2) needs two things from this package that
// Validate cannot give it: a stylesheet compiler has to resolve type names
// out of the schema at COMPILE time ([xsl:]type, @as, element(n,t),
// schema-element(n)), and [xsl:]validation has to validate a CONSTRUCTED
// subtree and leave real type annotations on it. Everything here is additive:
// Validate's own path is untouched apart from four nil-guarded recording
// calls (see noteNodeType), so the 99.99%-conformant string entry point keeps
// its exact behaviour.

import (
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// --- component lookup -------------------------------------------------------

// typeByName resolves an expanded type name to its compiled component, over
// both the schema's own global types and the built-ins. It mirrors
// resolveInstanceType's rules — the XDM-only names are not schema types, and
// the 1.1-only built-ins do not exist under 1.0 — so a name resolves
// identically whether it arrives from an instance's xsi:type or from a
// stylesheet.
func (s *Schema) typeByName(n xname) (Type, bool) {
	if n.Space == xsNS {
		if n.Local == "untyped" || n.Local == "untypedAtomic" ||
			(s.version == Version10 && xsd11OnlyBuiltin[n.Local]) {
			return nil, false
		}
		if n.Local == "error" {
			return xsErrorType, true
		}
		if lt, ok := builtinList(n.Local); ok {
			return lt, true
		}
		if at, ok := builtinAtom(n.Local); ok {
			return &SimpleType{variety: vAtomic, baseBuiltin: true, prim: at}, true
		}
		if n.Local == "anyType" {
			return anyType(), true
		}
		return nil, false
	}
	if t, ok := s.anonType(n); ok {
		return t, true
	}
	t, ok := s.types[n]
	if !ok || isNilType(t) {
		return nil, false
	}
	return t, true
}

func typeIsComplex(t Type) bool {
	c, ok := t.(*ComplexType)
	return ok && c != nil
}

// typeNameOf renders a compiled type as the import-free identity xmltree
// carries. An ANONYMOUS type has no QName to render, so the result is the zero
// name — which annotate() turns into a nil Node.SchemaType rather than a
// name-less one, since two unrelated anonymous types would otherwise compare
// equal. The Complex flag is meaningful either way.
func (s *Schema) typeNameOf(t Type) xmltree.SchemaTypeName {
	var n xname
	switch v := t.(type) {
	case *ComplexType:
		if v != nil {
			n = v.name
		}
	case *SimpleType:
		if v != nil {
			n = v.name
			if n.zero() {
				n = builtinRefName(v)
			}
		}
	}
	if n.zero() {
		n = s.anonName(t)
	}
	return xmltree.SchemaTypeName{Namespace: n.Space, Local: n.Local, Complex: typeIsComplex(t)}
}

// anonTypeNS is the namespace the synthetic names of ANONYMOUS types live in.
// Deliberately not a real namespace: nothing in a schema document or a
// stylesheet can spell it, so a synthetic name can never collide with, or be
// mistaken for, a name a user wrote.
const anonTypeNS = "urn:x-go-xslt:anonymous-type"

// anonName gives an anonymous type a stable identity for the XSLT bridge.
//
// Most W3C element declarations use an INLINE xs:complexType, which has no
// QName at all. Reporting the zero name for those (the old behaviour) makes
// annotate() leave Node.SchemaType nil and schemaTypeMatches fail-close — so
// schema-element(N) could never match any of them, however correct everything
// else was. Two unrelated anonymous types must still not compare equal, which
// a shared zero name would have made them.
//
// The identity is per-COMPONENT-POINTER and per-Schema: the declaration's
// declared type and the type a node was assessed against are the same pointer,
// so both sides get the same name, and typeByName resolves it back for the
// derivation walk. The names are not stable ACROSS runs — they are assigned in
// first-asked order — which is fine because nothing outside one compiled
// Schema ever sees them.
//
// The registry is a POINTER field so that ValidateNode's per-call `local := *s`
// copy shares it (a copy must not invent a second numbering), and it is
// mutex-guarded because one compiled Schema is reused across concurrent
// transformations.
func (s *Schema) anonName(t Type) xname {
	if isNilType(t) || s == nil || s.anon == nil {
		return xname{}
	}
	s.anon.mu.Lock()
	defer s.anon.mu.Unlock()
	if n, ok := s.anon.byType[t]; ok {
		return n
	}
	s.anon.n++
	n := xname{anonTypeNS, "t" + strconv.Itoa(s.anon.n)}
	s.anon.byType[t] = n
	s.anon.byName[n] = t
	return n
}

// anonType resolves a synthetic anonymous-type name back to its component.
func (s *Schema) anonType(n xname) (Type, bool) {
	if s == nil || s.anon == nil || n.Space != anonTypeNS {
		return nil, false
	}
	s.anon.mu.Lock()
	defer s.anon.mu.Unlock()
	t, ok := s.anon.byName[n]
	return t, ok
}

// builtinRefName recovers the XSD-namespace name of a PRISTINE reference to a
// built-in atomic type.
//
// resolveType materializes every such reference as a FRESH, NAME-LESS sentinel
// (`&SimpleType{variety: vAtomic, baseBuiltin: true, prim: at}`) — the built-in
// hierarchy lives in xpath.AtomType, not in the component graph, so there is no
// shared component to point at. That is invisible to the validator, which only
// ever asks value-space questions, but it is fatal to the XSLT bridge: without
// a name, annotate() leaves Node.SchemaType nil on every node whose declared
// type is a built-in, and element(*, xs:ID) / schema-element(N) — whose whole
// question is "is this node's type T, or derived from T?" — can then never
// answer yes for any of xs:string, xs:ID, xs:anyURI, xs:integer and the rest.
//
// The sentinel is recognised structurally rather than by giving SimpleType a
// name at construction time: linkBase, isURTypeSimple, simpleDerivesFrom and
// typeRestrictsFrom all read `name.zero()` as "anonymous, stay conservative",
// so populating the field would change the validator's own derivation
// decisions. Nothing outside this file calls typeNameOf.
//
// Two shapes are deliberately NOT named:
//
//   - The ur-type sentinel (prim xs:anyAtomicType), which a reference to EITHER
//     xs:anySimpleType or xs:anyAtomicType produces — the two are
//     indistinguishable here, and inventing one of them would be a guess.
//   - An ANONYMOUS user restriction of a built-in, which linkBase collapses
//     into exactly this shape but WITH facets (or assertions) of its own. It is
//     genuinely a different type from the built-in it restricts; a node
//     governed by it keeps the nil SchemaType an anonymous type has always had.
//     xs:dateTimeStamp is the one built-in that carries facets of its own
//     (§3.4.28's fixed explicitTimezone, set by resolveType), so it is admitted
//     by its primitive rather than by facet-emptiness.
func builtinRefName(st *SimpleType) xname {
	if st == nil || st.variety != vAtomic || !st.baseBuiltin || st.base != nil ||
		len(st.assertions) != 0 || st.prim == xpath.XSanyAtomicType {
		return xname{}
	}
	if st.facets.any() && st.prim != xpath.XSdateTimeStamp {
		return xname{}
	}
	local := strings.TrimPrefix(st.prim.String(), "xs:")
	if local == "" {
		return xname{}
	}
	return xname{xsNS, local}
}

// TypeByName resolves a QName to the schema type it names (a global
// xs:simpleType/xs:complexType, or a built-in), for @as/instance-of/node-test
// resolution. ok is false when nothing in this Schema's imported components
// has that name.
func (s *Schema) TypeByName(namespace, local string) (xmltree.SchemaTypeName, bool) {
	t, ok := s.typeByName(xname{namespace, local})
	if !ok {
		return xmltree.SchemaTypeName{}, false
	}
	// The name the CALLER asked for is authoritative: a built-in materializes a
	// fresh unnamed sentinel struct (see resolveType), so typeNameOf would
	// report the zero name for xs:string.
	return xmltree.SchemaTypeName{Namespace: namespace, Local: local, Complex: typeIsComplex(t)}, true
}

// ElementDeclared reports whether namespace/local names a global element
// declaration in this Schema, and if so, the type it declares (for
// schema-element(name) / element(name) without an explicit type).
//
// ok answers DECLAREDNESS. A declaration whose type is anonymous yields a zero
// Namespace+Local with ok true — the element exists, but its type has no name
// a type test could be written against (see typeNameOf).
func (s *Schema) ElementDeclared(namespace, local string) (xmltree.SchemaTypeName, bool) {
	d, ok := s.elements[xname{namespace, local}]
	if !ok || d == nil {
		return xmltree.SchemaTypeName{}, false
	}
	return s.typeNameOf(d.typ), true
}

// AttributeDeclared is ElementDeclared's attribute counterpart
// (schema-attribute(name)).
func (s *Schema) AttributeDeclared(namespace, local string) (xmltree.SchemaTypeName, bool) {
	d, ok := s.attributes[xname{namespace, local}]
	if !ok || d == nil {
		return xmltree.SchemaTypeName{}, false
	}
	return s.typeNameOf(d.typ), true
}

// DerivesFrom implements xpath.SchemaTypeResolver for a compiled Schema: got
// IS want, or derives from it by extension/restriction, or (for an element
// declaration's type) via substitution-group membership.
//
// A zero name never derives from anything: it is what an anonymous type
// reports, and two anonymous types are not the same component just because
// neither has a name.
func (s *Schema) DerivesFrom(got, want xmltree.SchemaTypeName) bool {
	gn := xname{got.Namespace, got.Local}
	wn := xname{want.Namespace, want.Local}
	if gn.zero() || wn.zero() {
		return false
	}
	if gn == wn {
		return true
	}
	if gt, ok := s.typeByName(gn); ok {
		if wt, ok := s.typeByName(wn); ok {
			return xpathDerivesFrom(gt, wt, 0)
		}
	}
	// The substitution-group route: the names denote ELEMENT declarations, not
	// types (schema-element(head) matches every member of head's group). Only
	// reached when the type route found no components, so a name that is both a
	// type and an element still answers as a type.
	if head, ok := s.elements[wn]; ok && head != nil {
		if _, ok := s.elements[gn]; ok {
			return s.matchesElementName(head, gn)
		}
	}
	return false
}

// SubstitutesFor reports whether the global element declaration named by
// member is head itself, or a member (directly or transitively) of the
// substitution group headed by head — the first of schema-element(head)'s two
// matching conditions (XPath 3.1 §2.5.5.5: "the name of the candidate node
// matches the specified ElementName, or ... the name of an element declaration
// that is a member of the substitution group headed by ElementName").
//
// Separate from DerivesFrom, which answers about TYPES: a name can denote both
// a type and an element declaration, and DerivesFrom's own substitution route
// is reached only when the type route found nothing, so asking it this question
// would give the wrong answer for any such collision. This asks the element
// graph directly, through the same matchesElementName the content-model walk
// uses — so @abstract and @block are honoured here exactly as they are during
// validation.
func (s *Schema) SubstitutesFor(member, head xmltree.Name) bool {
	hd, ok := s.elements[xname{head.Space, head.Local}]
	if !ok || hd == nil {
		return false
	}
	return s.matchesElementName(hd, xname{member.Space, member.Local})
}

// CastToSchemaType implements xpath.SchemaTypeCaster: it validates a lexical
// form against the named simple type and reports the built-in primitive that
// type's value space sits in, so the caller can build an atomic value of the
// right shape and tag it with the named type.
//
// This is XPath 3.1 §3.14's `cast as`/`castable as` over an imported simple
// type, and §3.14.4's constructor function for the same. The facets are the
// schema's business, so the whole decision is SimpleType.validate's — the same
// code an instance document's value goes through.
//
// A LIST type is refused rather than mis-answered: its value is a sequence,
// which the one-atomic-value result shape here cannot represent (the same
// documented limitation Node.TypeAnno has).
func (s *Schema) CastToSchemaType(name xmltree.SchemaTypeName, lexical string) (xpath.AtomType, error) {
	return s.CastToSchemaTypeIn(name, lexical, nil)
}

// CastToSchemaTypeIn implements xpath.SchemaTypeCasterIn: CastToSchemaType
// carrying the node whose in-scope namespaces expand a prefix inside lexical.
// It matters only for xs:QName/xs:NOTATION-derived types, whose enumeration
// facet compares EXPANDED names (see facetSet.checkPatternEnumIn) — for every
// other type at is simply unused, which is why the two entry points share one
// body rather than branching.
func (s *Schema) CastToSchemaTypeIn(name xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (xpath.AtomType, error) {
	t, ok := s.typeByName(xname{name.Namespace, name.Local})
	if !ok {
		return 0, invalidf("XPST0051", "no type named %s in the imported schema components",
			xname{name.Namespace, name.Local})
	}
	st, ok := t.(*SimpleType)
	if !ok || st == nil {
		return 0, invalidf("XPTY0004", "%s is not a simple type", name.Local)
	}
	if st.variety == vList {
		return 0, invalidf("XPST0051", "casting to the list type %s is not supported", name.Local)
	}
	if err := st.validateIn(lexical, at); err != nil {
		return 0, err
	}
	prim := st.resolvePrim()
	if st.variety == vUnion {
		// A union's value is an instance of the MEMBER type that accepted it
		// (XSD 1.0 Part 2 §4.1.2), so that member's primitive — not the
		// union's own, which it has none of — is what the value atomizes as
		// and what the casting table has to be consulted about
		// (castable-005's Castable-UnionType-9: xs:gYear castable as a union
		// of integer and date is FALSE, which only the member's primitive can
		// show).
		if m := acceptingUnionMember(st, lexical, at); m != nil && m.variety == vAtomic {
			prim = m.resolvePrim()
		}
	}
	if prim == xpath.XSanyAtomicType {
		// A union whose members disagree on a primitive: xs:string is the one
		// reading that can hold any of their lexical forms without loss.
		prim = xpath.XSstring
	}
	return prim, nil
}

// --- validate-in-place ------------------------------------------------------

// NodeValidateOptions controls an in-place validation run.
type NodeValidateOptions struct {
	// Strict, if true, requires a matching top-level declaration and fails
	// validation entirely if none is found (XTTE1512-shaped). If false (lax),
	// a missing declaration is tolerated (not an error) but an invalid match
	// still fails (XTTE1510-shaped) — see XSLT 3.0 §24.4.1.
	Strict bool
	// Type, when non-nil, validates against this NAMED type directly
	// ([xsl:]type="...") instead of a matching top-level declaration.
	Type *xmltree.SchemaTypeName
	// LenientEntities drops the XSD-1.1-only rule that an xs:ENTITY value must
	// name an unparsed entity. An XSLT host compiles every imported schema as
	// 1.1, including ones written against 1.0 where that rule does not exist,
	// and has no way to tell the two apart — so it asks for the 1.0 reading
	// rather than inventing an error the schema's own version never defined.
	// Deliberately an OPTION rather than a property of ValidateNode, so the
	// Validate/ValidateNode differential keeps comparing like with like.
	LenientEntities bool
	// Document records that the node the HOST asked to validate was a
	// document node (or a whole source document), as opposed to an element
	// plucked out of a result tree. Only then do the document-level
	// constraints — ID uniqueness, IDREF resolution — apply (XTTE1555).
	Document bool
	// StripElementOnlyWhitespace removes whitespace-only text children of
	// every element whose validated type has ELEMENT-ONLY content.
	//
	// This is part of building an XDM tree FROM A PSVI, not part of deciding
	// validity, which is why the host asks for it explicitly: XSLT 3.0 §4.4's
	// own note says "elements that are defined in a DTD or a Schema to contain
	// element-only content will have whitespace text nodes stripped,
	// regardless of the xsl:strip-space and xsl:preserve-space declarations",
	// and that is a statement about the SOURCE tree the processor is handed.
	// A constructed result tree being validated by [xsl:]validation is not
	// built that way and must keep exactly the nodes the stylesheet wrote.
	StripElementOnlyWhitespace bool
}

// ValidateNode validates n (and its descendants) against s in place,
// annotating n.TypeAnno (the nearest built-in primitive, via
// SimpleType.resolvePrim()) and n.SchemaType (the exact declared type) on
// every element/attribute it validates. err is non-nil exactly when n is
// invalid, undeclared-under-Strict, or validation cannot proceed — mirroring
// Validate's contract, adapted for a tree that already exists rather than
// being parsed from text.
//
// The walk itself runs on a detached, text-NORMALIZED clone of n's subtree,
// never on the caller's own nodes (see cloneForValidation for why), and the
// resulting annotations are written back through the clone→original mapping
// that clone produced. Nothing is written until validation has SUCCEEDED: an
// invalid subtree is left exactly as the caller handed it over.
func (s *Schema) ValidateNode(n *xmltree.Node, opts NodeValidateOptions) error {
	if n == nil {
		return invalidf("", "no node to validate")
	}
	if n.Kind == xmltree.KindDocument {
		root := xmltree.RootElement(n)
		if root == nil {
			return invalidf("cvc-elt.1", "document has no document element")
		}
		n = root
	}
	switch n.Kind {
	case xmltree.KindElement, xmltree.KindAttribute:
	default:
		// A comment/PI/text node has no schema type at all; refusing is the
		// fail-closed answer, not a silent no-op that would report "valid".
		return invalidf("", "cannot schema-validate a node of this kind")
	}

	// Per-call state, exactly as Validate builds it — except that hintDocs is
	// deliberately NOT consulted: for XSLT the imported schema set is fixed
	// STATICALLY by xsl:import-schema (XSLT 3.0 §26.2, XTSE0220's consistency
	// requirement), never discovered from an instance's xsi:schemaLocation.
	local := *s
	// ID/IDREF are DOCUMENT-level constraints (XSLT 3.0 §24's XTTE1555), so an
	// episode rooted at an ELEMENT does not enforce uniqueness — see
	// idTable.elementScope.
	local.ids = &idTable{bound: map[string]idBinding{}, elementScope: !opts.Document}
	local.typeCache = map[*xmltree.Node]Type{}
	local.unassessed = map[*xmltree.Node]bool{}
	local.assertTypes = map[*xmltree.Node]xpath.AtomType{}
	local.assertLists = map[*xmltree.Node]assertList{}
	local.nodeTypes = map[*xmltree.Node]Type{}
	local.nodeNilled = map[*xmltree.Node]bool{}
	local.nodeDefaults = map[*xmltree.Node][]defaultAttr{}
	// No instance TEXT exists to re-scan for <!ENTITY … NDATA …> declarations,
	// so the xs:ENTITY value space comes from the owning document's own
	// already-parsed table (Node.Unparsed, set by the parser and carried across
	// xsl:copy-of). A constructed tree has none, which simply means no value
	// is entity-valid there — the conservative reading.
	local.unparsed = unparsedOf(n)
	local.lenientEntities = opts.LenientEntities
	s = &local

	clone, back := cloneForValidation(n)
	if err := s.validateCloned(clone, opts); err != nil {
		return err
	}
	// The dangling-IDREF resolution (cvc-id.1) is a DOCUMENT-level rule: an
	// IDREF inside a subtree may legitimately point at an ID that lives outside
	// it, so running it over a proper subtree would invent errors. The CALLER
	// says whether this episode is document-scoped — a node copied into an
	// XSLT temporary tree hangs off a document-node FRAGMENT that was never a
	// document, so deciding it from the node's own parent answers yes for a
	// plucked-out subtree and invents exactly those errors (accumulator-073
	// copies three ITEM elements whose IDREF points at an ID left behind).
	if opts.Document {
		if err := s.ids.check(); err != nil {
			return err
		}
	}
	s.annotate(back)
	s.supplyDefaults(back)
	if opts.StripElementOnlyWhitespace {
		s.stripElementOnlyWhitespace(back)
	}
	return nil
}

// stripElementOnlyWhitespace implements the XDM-construction half of XSLT 3.0
// §4.4 (see NodeValidateOptions.StripElementOnlyWhitespace): a whitespace-only
// text node cannot survive in an element whose validated type has
// element-only content, because the PSVI it is built from has no character
// information items there to represent it.
//
// Its counterpart — an element with SIMPLE content keeps its whitespace text
// nodes regardless of xsl:strip-space, "because stripping ... could make the
// element invalid" — needs no code here: it is a rule about what
// xsl:strip-space may remove, not about what validation contributes.
func (s *Schema) stripElementOnlyWhitespace(back map[*xmltree.Node]*xmltree.Node) {
	for cl, orig := range back {
		if orig == nil || orig.Kind != xmltree.KindElement || len(orig.Children) == 0 {
			continue
		}
		ct, ok := s.nodeTypes[cl].(*ComplexType)
		if !ok || ct == nil || ct.kind != contentElementOnly {
			continue
		}
		kept := orig.Children[:0]
		for _, ch := range orig.Children {
			if ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) == "" {
				continue
			}
			kept = append(kept, ch)
		}
		orig.Children = kept
	}
}

// validateCloned runs the requested validation episode against the cloned
// root. Split out so ValidateNode's own bookkeeping stays readable.
func (s *Schema) validateCloned(el *xmltree.Node, opts NodeValidateOptions) error {
	if opts.Type != nil {
		t, ok := s.typeByName(xname{opts.Type.Namespace, opts.Type.Local})
		if !ok {
			return invalidf("cvc-type", "no type named %s in the imported schema components",
				xname{opts.Type.Namespace, opts.Type.Local})
		}
		if el.Kind == xmltree.KindAttribute {
			return s.validateAttributeValue(el, t)
		}
		return s.validateAgainstType(t, el, map[xname]*keyTable{}, nil)
	}
	name := nameOf(el)
	if el.Kind == xmltree.KindAttribute {
		d, ok := s.attributes[name]
		if !ok || d == nil {
			if opts.Strict {
				return invalidf("cvc-assess-attr", "attribute %s is not declared", name)
			}
			return nil // lax: no declaration, nothing assessed
		}
		if d.typ == nil {
			return nil
		}
		return s.validateAttributeValue(el, d.typ)
	}
	decl, ok := s.elements[name]
	if !ok || decl == nil {
		// An element with no governing declaration may still be assessed
		// against an explicit xsi:type ("validation as a type") — exactly the
		// route validateElement takes on the string path, mirrored here so the
		// two entry points agree (strip-space-009's <doc xsi:type="t">).
		if xt, hasType := el.Attr(xsiNS, "type"); hasType {
			t, terr := s.resolveInstanceType(el, xt)
			if terr != nil {
				return terr
			}
			return s.validateAgainstType(t, el, map[xname]*keyTable{}, nil)
		}
		// Lax validation of an element with no top-level declaration leaves it
		// (and, in this implementation, its whole subtree) unassessed rather
		// than erroring — XSLT 3.0 §24.4.1's "lax" is not an error, it is an
		// absence of assessment. RESIDUAL: the spec's lax also descends,
		// assessing any DESCENDANT that does have a declaration; skipping the
		// subtree wholesale under-annotates but never mis-annotates.
		if opts.Strict {
			return invalidf("cvc-elt.1", "element %s is not declared", name)
		}
		return nil
	}
	if decl.abstract {
		return invalidf("cvc-elt.2", "abstract element %s cannot appear directly", name)
	}
	return s.validateElementAgainst(decl, el, map[xname]*keyTable{})
}

// validateAttributeValue assesses a STANDALONE attribute node (xsl:attribute's
// own [xsl:]validation/[xsl:]type), which the element walk never reaches
// because it has no owning element to be assessed through.
func (s *Schema) validateAttributeValue(a *xmltree.Node, t Type) error {
	st, ok := t.(*SimpleType)
	if !ok || st == nil {
		return invalidf("cvc-attribute.2", "attribute %s cannot be validated against a complex type", nameOf(a))
	}
	if err := s.yearZeroOK(st, a.Value); err != nil {
		return err
	}
	if err := st.validateIn(a.Value, a); err != nil {
		return err
	}
	s.noteNodeType(a, st)
	return nil
}

// isWholeDocument reports whether el is the document element of its own tree
// (as opposed to a subtree plucked out of a larger one) — see ValidateNode for
// what that decides.
func isWholeDocument(el *xmltree.Node) bool {
	p := el.Parent
	return p == nil || p.Kind == xmltree.KindDocument
}

// unparsedOf returns the unparsed (NDATA) entity names declared by the DTD of
// the document n belongs to, in the shape Schema.unparsed wants. Empty for a
// constructed (DTD-less) tree.
func unparsedOf(n *xmltree.Node) map[string]bool {
	root := n.Root()
	if root == nil || len(root.Unparsed) == 0 {
		return nil
	}
	out := make(map[string]bool, len(root.Unparsed))
	for name := range root.Unparsed {
		out[name] = true
	}
	return out
}

// --- the clone ---------------------------------------------------------------

// cloneForValidation deep-copies el into a DETACHED, text-normalized tree and
// returns it together with a clone→original mapping of every element and
// attribute node.
//
// Three separate reasons the walk cannot run on the caller's own nodes:
//
//   - TEXT SHAPE. The validator inspects children one by one (hasNonWSText,
//     elementText, elementHasAnyText, elementHasContent) on the shape Parse
//     always produces: no empty text nodes, no two adjacent ones. Constructed
//     XSLT content violates both routinely (one text node per xsl:value-of,
//     empty ones from a branch that produced nothing). Normalizing here means
//     the walk sees exactly what it sees for a parsed document — the
//     "nodes built outside the normal path are missing invariants other code
//     depends on" bug class, pre-empted rather than chased.
//   - DETACHMENT. A constructed subtree usually hangs off a larger result tree.
//     '/' and the ancestor axes inside an xs:assert or an identity-constraint
//     selector would otherwise escape the part being validated, and
//     icNodeVisible's parent walk would climb into elements no declaration
//     governs. Cutting the parent link bounds all three.
//   - PURITY. Validation must not change the caller's tree structurally; only
//     the type annotations it earns are written back, and only on success.
//
// Nodes are struct-copied, so Line/Col (error locations), Base, Raw and the
// rest survive; order is re-stamped afterwards because a constructed tree's is
// frequently unassigned, which would break any document-order-dependent
// identity-constraint or assertion evaluation.
func cloneForValidation(el *xmltree.Node) (*xmltree.Node, map[*xmltree.Node]*xmltree.Node) {
	back := map[*xmltree.Node]*xmltree.Node{}

	var clone func(n *xmltree.Node) *xmltree.Node
	clone = func(n *xmltree.Node) *xmltree.Node {
		cp := *n
		cp.Parent = nil
		cp.Children = nil
		cp.Attrs = make([]*xmltree.Node, len(n.Attrs))
		for i, at := range n.Attrs {
			ac := *at
			ac.Parent = &cp
			cp.Attrs[i] = &ac
			back[&ac] = at
		}
		cp.NS = make([]*xmltree.Node, len(n.NS))
		for i, ns := range n.NS {
			nc := *ns
			nc.Parent = &cp
			cp.NS[i] = &nc
		}
		for _, ch := range n.Children {
			// Merge an adjacent text RUN into the node that opened it, and drop
			// a run that turns out to be empty — exactly the two shapes Parse
			// never emits. Text is not merged ACROSS a comment or PI, because
			// Parse does not merge across one either.
			if ch.Kind == xmltree.KindText {
				if k := len(cp.Children); k > 0 && cp.Children[k-1].Kind == xmltree.KindText {
					cp.Children[k-1].Value += ch.Value
					continue
				}
				if ch.Value == "" {
					continue
				}
			}
			c2 := clone(ch)
			c2.Parent = &cp
			cp.Children = append(cp.Children, c2)
		}
		back[&cp] = n
		return &cp
	}

	root := clone(el)
	// Detaching loses every namespace binding declared on an ANCESTOR, which
	// xsi:type and QName-typed content resolve against (resolveQName walks the
	// parent chain). Materialize the in-scope set onto the clone root, the same
	// way assertTree does for an assertion's detached tree.
	have := map[string]bool{}
	for _, ns := range root.NS {
		have[ns.Name.Local] = true
	}
	for pfx, uri := range el.InScopeNamespaces() {
		if !have[pfx] {
			root.NS = append(root.NS, &xmltree.Node{Kind: xmltree.KindNamespace,
				Name: xmltree.Name{Local: pfx}, Value: uri, Parent: root})
		}
	}
	xmltree.AssignOrder(root)
	return root, back
}

// --- annotation --------------------------------------------------------------

// noteNodeType records the schema type that governed one validated node.
// Validate never sets Schema.nodeTypes, so on that path this is a single nil
// check and nothing more — which is the whole reason the recording lives at
// the walk's own call sites rather than in a re-derivation pass afterwards:
// the type a node was ACTUALLY assessed against (post xsi:type, post
// conditional type assignment, post substitution group) is known there and
// nowhere else.
func (s *Schema) noteNodeType(n *xmltree.Node, t Type) {
	if s.nodeTypes == nil || n == nil || isNilType(t) {
		return
	}
	s.nodeTypes[n] = t
}

// noteNodeNilled records that one validated element was accepted as empty
// through xsi:nil="true" (XSD §3.3.4 cvc-elt.3.2), i.e. its XDM [nilled]
// property is true. Like noteNodeType this is a single nil check on the
// string-based Validate path, which annotates nothing.
func (s *Schema) noteNodeNilled(n *xmltree.Node) {
	if s.nodeNilled == nil || n == nil {
		return
	}
	s.nodeNilled[n] = true
}

// noteDefaultAttr records one attribute a value constraint supplied for an
// element that omitted it — see Schema.nodeDefaults. Like noteNodeType this is
// a single nil check on the string-based Validate path, which builds no tree.
func (s *Schema) noteDefaultAttr(el *xmltree.Node, name xname, value string, t Type) {
	if s.nodeDefaults == nil || el == nil {
		return
	}
	s.nodeDefaults[el] = append(s.nodeDefaults[el], defaultAttr{name: name, value: value, typ: t})
}

// supplyDefaults materializes the recorded default/fixed attributes on the
// CALLER's elements. Guarded against overwriting an attribute the instance
// already carries — only an ABSENT use is ever recorded, but the same original
// element can be reached through more than one validation episode, and a
// second pass must not duplicate what the first supplied.
func (s *Schema) supplyDefaults(back map[*xmltree.Node]*xmltree.Node) {
	if len(s.nodeDefaults) == 0 {
		return
	}
	for cl, orig := range back {
		if orig == nil || orig.Kind != xmltree.KindElement {
			continue
		}
		for _, d := range s.nodeDefaults[cl] {
			if _, exists := orig.Attr(d.name.Space, d.name.Local); exists {
				continue
			}
			a := orig.SetAttr(xmltree.Name{
				Space:  d.name.Space,
				Local:  d.name.Local,
				Prefix: nsFixupPrefix(orig, d.name.Space),
			}, d.value)
			if a == nil {
				continue
			}
			a.TypeAnno = int32(typeAnnoForNode(d.typ, a))
			a.ListTyped = listTypedFor(d.typ)
			a.ListItemType = s.listItemTypeName(d.typ)
			if k := idKindFor(d.typ); k != xmltree.IDKindNone {
				a.IDKind = k
			}
			if name := s.typeNameOf(d.typ); name.Namespace != "" || name.Local != "" {
				n := name
				a.SchemaType = &n
			}
		}
	}
}

// nsFixupPrefix is XSD 1.1 §3.4.5.1's namespace fixup for an attribute a
// value constraint SUPPLIED: a defaulted attribute in a namespace has to reach
// the tree with a usable prefix, and with that prefix actually DECLARED, or
// the document the processor hands on could not be serialized and re-parsed to
// the same XDM instance.
//
// An attribute name is never in the default namespace, so a matching xmlns=""
// binding is no help and is skipped. An already-bound prefix for the same URI
// is REUSED rather than shadowed — the instance's own choice wins, which is
// what import-schema-164's third element pins (xmlns:q="http://p.com/" in
// scope ⇒ the supplied attribute must be q:foo). Otherwise a fresh prefix is
// invented and declared on the element itself; the test's own 2026 revision
// states the rule exactly — "the processor is free to choose any prefix it
// likes, so long as it declares it".
//
// Returns "" for a no-namespace attribute, which needs no prefix at all.
func nsFixupPrefix(el *xmltree.Node, space string) string {
	if el == nil || space == "" {
		return ""
	}
	// The XML namespace is bound to "xml" implicitly and everywhere, and
	// Namespaces in XML forbids declaring it — so it needs the prefix but no
	// declaration. InScopeNamespaces() reports only EXPLICIT bindings and
	// therefore never mentions it, which would otherwise send the loop below
	// off to invent a prefix and emit an illegal xmlns:ns1 for it (the XHTML
	// schema defaults xml:space="preserve" onto <style>).
	if space == xmlNS {
		return "xml"
	}
	inScope := el.InScopeNamespaces()
	for pfx, uri := range inScope {
		// The default namespace (empty prefix) never governs an attribute
		// name, so a binding of it cannot serve as this attribute's prefix.
		if pfx != "" && uri == space {
			return pfx
		}
	}
	pfx := ""
	for i := 1; ; i++ {
		cand := "ns" + strconv.Itoa(i)
		if _, taken := inScope[cand]; !taken && cand != "xml" && cand != "xmlns" {
			pfx = cand
			break
		}
	}
	el.NS = append(el.NS, &xmltree.Node{
		Kind:   xmltree.KindNamespace,
		Name:   xmltree.Name{Local: pfx},
		Value:  space,
		Parent: el,
	})
	return pfx
}

// annotate writes the recorded types onto the CALLER's nodes, through the
// clone→original mapping. A mapped node with no recorded type was not assessed
// (absorbed by a skip wildcard, or below an unassessed ancestor) and has its
// annotation CLEARED: validation replaces a node's type annotation, so leaving
// a stale one behind would be worse than leaving none.
func (s *Schema) annotate(back map[*xmltree.Node]*xmltree.Node) {
	for cl, orig := range back {
		if orig == nil {
			continue
		}
		t := s.nodeTypes[cl]
		if isNilType(t) {
			orig.TypeAnno, orig.SchemaType, orig.Nilled, orig.ListTyped, orig.ListItemType = 0, nil, false, false, nil
			orig.ValueType = nil
			continue
		}
		orig.ValueType = s.valueTypeNameOf(t, orig)
		orig.TypeAnno = int32(typeAnnoForNode(t, orig))
		// [nilled] is assigned unconditionally, not only when true: this
		// episode's verdict REPLACES whatever a previous validation left on
		// the node, exactly as the annotation above does.
		orig.Nilled = s.nodeNilled[cl]
		orig.ListTyped = listTypedFor(t)
		orig.ListItemType = s.listItemTypeName(t)
		// IDKind is the DTD-derived half of the same fact TypeAnno carries
		// here, and fn:id/fn:idref read IT, not the annotation (xmltree's own
		// CopyTypeInfo deliberately carries the two together for exactly this
		// reason). A schema that types an attribute xs:ID/xs:IDREF/xs:IDREFS
		// must therefore feed the same field a DTD ATTLIST would
		// (import-schema-077/078/079): without it, id() finds nothing in a
		// document whose IDs were declared in a schema rather than a DTD.
		if k := idKindFor(t); k != xmltree.IDKindNone {
			orig.IDKind = k
		}
		if name := s.typeNameOf(t); name.Namespace != "" || name.Local != "" {
			n := name
			orig.SchemaType = &n
		} else {
			orig.SchemaType = nil // anonymous: no nameable identity (typeNameOf)
		}
	}
}

// typeAnnoFor is the "what does this atomize as" half of an annotation: the
// nearest built-in primitive of the node's SIMPLE content.
//
// Zero (untyped) for two documented cases, both inherited from the precedent
// annotateAssertType already set:
//   - complex ELEMENT-ONLY/mixed/empty content, which has no typed value at
//     all — the exact declared type still reaches Node.SchemaType (with
//     Complex true), which is what an atomization attempt has to consult to
//     raise FOTY0012 rather than silently returning a string;
//   - list and union simple types, whose typed value is a SEQUENCE (or a
//     member-dependent single item) that one AtomType cannot represent.
//
// idKindFor maps an assessed type onto xmltree's DTD-shaped ID marker. A LIST
// of xs:IDREF (xs:IDREFS, or any restriction of it) is IDKindIDREFS, which is
// why the list variety is inspected rather than only the atomic primitive.
func idKindFor(t Type) uint8 {
	st := simpleContentType(t)
	if st == nil {
		return xmltree.IDKindNone
	}
	if st.variety == vList && st.item != nil && st.item.resolvePrim() == xpath.XSidref {
		return xmltree.IDKindIDREFS
	}
	if st.variety != vAtomic {
		return xmltree.IDKindNone
	}
	switch st.resolvePrim() {
	case xpath.XSid:
		return xmltree.IDKindID
	case xpath.XSidref:
		return xmltree.IDKindIDREF
	}
	return xmltree.IDKindNone
}

func typeAnnoFor(t Type) xpath.AtomType {
	st := simpleContentType(t)
	if st == nil {
		return 0
	}
	if st.variety == vList {
		// A LIST type has no primitive of its own — its typed value is a
		// sequence of ITEM-type values — so the annotation carries the ITEM
		// type's primitive and xmltree.Node.ListTyped records that the string
		// value must be split into one such value per token. See
		// listTypedFor and internal/xpath's nodeTypedItems.
		if it := listItemSimpleType(st); it != nil {
			return it.resolvePrim()
		}
		return 0
	}
	if st.variety != vAtomic {
		return 0
	}
	return st.resolvePrim()
}

// typeAnnoForNode is typeAnnoFor with the validated node in hand, which is
// what a UNION type needs.
//
// A union's typed value is NOT a sequence (that is the list variety): it is
// ONE atomic value whose type is the member type that actually accepted this
// particular instance value (XSD 1.0 Part 2 §4.1.2 — "the value ... is the
// value of that member type definition" — and XDM 3.1 §3.3.1.2, which
// annotates a union-typed node with the member type, never with the union
// itself). So the member cannot be read off the type alone, and the type
// alone is exactly what typeAnnoFor has; here the node's own string value
// re-runs the same first-member-wins selection the validator already made
// when it accepted this node, which is deterministic and cheap.
//
// This is the union half of the "compute, don't store" reframe the list
// variety got: nothing new is carried on the node, the annotation simply
// names the member's primitive rather than nothing at all — so
// data(unionElem) is an xs:integer (or whichever member won) instead of
// falling back to xs:untypedAtomic (strip-type-annotations-014's E6:
// `data(simpleUserUnion) instance of xs:untypedAtomic` must be FALSE).
// valueTypeNameOf names the type a node's TYPED VALUE is an instance of, when
// that differs from the node's own type — see xmltree.Node.ValueType.
//
// Only a UNION creates the difference: the node's [type-name] is the union,
// its typed value is an instance of the member that accepted the lexical form.
// It re-runs the same first-member-wins selection typeAnnoForNode does for the
// primitive, so the two halves of one fact cannot disagree.
//
// Nil for everything else, and for an anonymous or non-atomic member, which
// has no name to report.
func (s *Schema) valueTypeNameOf(t Type, n *xmltree.Node) *xmltree.SchemaTypeName {
	st := simpleContentType(t)
	if st == nil || st.variety != vUnion || n == nil {
		return nil
	}
	m := acceptingUnionMember(st, n.StringValue(), n)
	if m == nil || m.variety != vAtomic {
		return nil
	}
	name := s.typeNameOf(m)
	if name.Namespace == "" && name.Local == "" {
		return nil
	}
	return &name
}

func typeAnnoForNode(t Type, n *xmltree.Node) xpath.AtomType {
	st := simpleContentType(t)
	if st == nil || st.variety != vUnion || n == nil {
		return typeAnnoFor(t)
	}
	if m := acceptingUnionMember(st, n.StringValue(), n); m != nil {
		// A member may itself be a list or a further union; only an ATOMIC
		// member has one primitive to annotate with, and anything else keeps
		// the old conservative zero.
		if m.variety == vAtomic {
			return m.resolvePrim()
		}
	}
	return 0
}

// acceptingUnionMember returns the first member type of a union that accepts
// v — the same "first member wins" rule validateIn itself applies, so the
// answer is the member the instance was actually validated against. Members
// are searched through a restriction chain, since a restriction of a union
// keeps the members on its base.
func acceptingUnionMember(st *SimpleType, v string, at *xmltree.Node) *SimpleType {
	for cur := st; cur != nil; cur = cur.base {
		for _, m := range cur.members {
			if m.validateIn(v, at) == nil {
				return m
			}
		}
		if len(cur.members) > 0 {
			return nil
		}
	}
	return nil
}

// listTypedFor reports whether t's simple content has the LIST variety, i.e.
// whether the annotated node's typed value is a sequence rather than one
// atomic value.
func listTypedFor(t Type) bool {
	st := simpleContentType(t)
	return st != nil && st.variety == vList && listItemSimpleType(st) != nil
}

// listItemTypeName names the ITEM type of t's list-varietied simple content —
// the identity xmltree.Node.ListItemType carries, so `data($a) instance of
// my:itemType*` can be answered from a list-typed node. Nil for anything but
// a list, and for a list whose item type is anonymous (typeNameOf's own
// answer: an anonymous type has no nameable identity to compare against).
func (s *Schema) listItemTypeName(t Type) *xmltree.SchemaTypeName {
	st := simpleContentType(t)
	if st == nil || st.variety != vList {
		return nil
	}
	it := listItemSimpleType(st)
	if it == nil {
		return nil
	}
	name := s.typeNameOf(it)
	if name.Namespace == "" && name.Local == "" {
		return nil
	}
	return &name
}

// listItemSimpleType walks a list type to the item type its tokens are values
// of. A RESTRICTION of a list (xs:NMTOKENS narrowed by a length facet, say)
// keeps the variety but records the item type on the base, so the chain is
// followed rather than read off the type itself.
func listItemSimpleType(st *SimpleType) *SimpleType {
	for cur := st; cur != nil; cur = cur.base {
		if cur.item != nil {
			return cur.item
		}
	}
	return nil
}

// xpathDerivesFrom is XPath 3.1 §2.5.6's derives-from(AT, ET), which is NOT
// XSD's cos-st-derived-ok and differs in exactly one clause.
//
// The spec defines it as: AT is ET; or ET is the base type of AT; or "ET is a
// PURE UNION TYPE of which AT is a member type"; plus transitivity. XSD's own
// relation (clause 2.2.4) admits ANY union, pure or not — which is right for
// xsi:type substitutability and wrong here: match-197 declares
// my:listUnionType as union(my:sixDoubles, my:myListType), whose myListType
// member is a LIST, so it is not a pure union (XSD 1.1 Part 2 §3.16.7.3: every
// member of a pure union is atomic, or itself a pure union). A
// my:myListType-annotated element therefore does NOT match
// element(*, my:listUnionType), which is precisely what that test asserts by
// expecting the two <listUnion> elements and not <my:simpleUserList>.
//
// Everything but the union clause is shared with the validator, so a change
// here cannot drift from the derivation walk the rest of the package uses.
func xpathDerivesFrom(at, et Type, depth int) bool {
	if depth > 16 {
		return false // a pathological union cycle; conservative answer
	}
	if derivesFromMode(at, et, false) {
		return true
	}
	es, ok := et.(*SimpleType)
	if !ok || es == nil || !isPureUnionType(es, 0) {
		return false
	}
	for _, m := range es.members {
		if m != nil && xpathDerivesFrom(at, m, depth+1) {
			return true
		}
	}
	return false
}

// isPureUnionType reports whether st is a PURE union type: variety union, with
// every member type atomic or itself a pure union (XSD 1.1 Part 2 §3.16.7.3).
// A union carrying facets of its own is excluded for the same reason
// simpleDerivesFrom excludes it — a member type escapes those facets, so
// treating it as an instance of the restricted union would be unsound.
func isPureUnionType(st *SimpleType, depth int) bool {
	if st == nil || st.variety != vUnion || st.facets.any() || depth > 16 {
		return false
	}
	for _, m := range st.members {
		if m == nil {
			return false
		}
		switch m.variety {
		case vAtomic:
		case vUnion:
			if !isPureUnionType(m, depth+1) {
				return false
			}
		default: // vList — a list member makes the union impure
			return false
		}
	}
	return true
}

// CastToSchemaListType implements xpath.SchemaListTypeCaster: `cast as`/
// `castable as` and the constructor function of an imported LIST simple type,
// whose result is a SEQUENCE of item-type values rather than one atomic value.
//
// The whole lexical form is validated against the list type itself, so the
// list's own facets (length/minLength/maxLength/pattern/enumeration) decide
// validity exactly as they would for an instance document's attribute; the
// caller then splits it and casts each token to the item primitive returned
// here. isList false means t is not a list at all, and the caller falls back to
// the ordinary single-value path — so a host that never sees a list type pays
// one type check.
func (s *Schema) CastToSchemaListType(name xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (
	xpath.AtomType, *xmltree.SchemaTypeName, bool, error) {
	t, ok := s.typeByName(xname{name.Namespace, name.Local})
	if !ok {
		return 0, nil, false, nil // the ordinary path reports XPST0051
	}
	st, ok := t.(*SimpleType)
	if !ok || st == nil || st.variety != vList {
		return 0, nil, false, nil
	}
	item := listItemSimpleType(st)
	if item == nil {
		return 0, nil, true, invalidf("XPST0051", "list type %s has no item type", name.Local)
	}
	if err := st.validateIn(lexical, at); err != nil {
		return 0, nil, true, err
	}
	var itemName *xmltree.SchemaTypeName
	if n := s.typeNameOf(item); n.Namespace != "" || n.Local != "" {
		itemName = &n
	}
	return item.resolvePrim(), itemName, true, nil
}
