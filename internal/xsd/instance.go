package xsd

import (
	"errors"
	"sort"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"os"
	"path/filepath"
)

const xmlNS = "http://www.w3.org/XML/1998/namespace"

// Validate validates the instance XML against the compiled schema. A parse error
// or a schema-validity failure yields res.Valid == false (a genuine "invalid"
// verdict); err is non-nil only when the verdict cannot be determined
// (ErrUnsupported).
func (s *Schema) Validate(instance string) (*Result, error) {
	doc, err := xmltree.ParseLenient11(instance)
	if err != nil {
		return &Result{Valid: false, Errors: []Error{{Message: "instance not well-formed: " + err.Error()}}}, nil
	}
	root := xmltree.RootElement(doc)
	if root == nil {
		return &Result{Valid: false, Errors: []Error{{Message: "no document element"}}}, nil
	}
	// xsi:schemaLocation hints on the document element may supply schema
	// documents beyond the compiled set; they can only ADD components, and a
	// broken hint never invalidates the instance (targetNS00101m1_p).
	if hinted := s.hintDocs(root); len(hinted) > 0 {
		merged := append(append([]string{}, s.sourceDocs...), hinted...)
		if ms, err := Compile(merged, s.baseDir, s.version); err == nil {
			s = ms
		}
	}
	// Validate against a shallow copy so the ID/IDREF binding of this instance
	// stays private to this call (the component maps are read-only here).
	local := *s
	local.ids = &idTable{bound: map[string]idBinding{}}
	local.typeCache = map[*xmltree.Node]Type{}
	local.unparsed = xmltree.UnparsedEntities(xmltree.DecodeBOM(instance))
	local.unassessed = map[*xmltree.Node]bool{}
	local.assertTypes = map[*xmltree.Node]xpath.AtomType{}
	local.assertLists = map[*xmltree.Node]assertList{}
	// Populated on THIS path too, although only ValidateNode writes the
	// supplied attributes back to a caller's tree (supplyDefaults): assertion
	// evaluation reads them as well, and the two entry points must reach the
	// identical verdict on the identical document — the invariant
	// TestValidateNodeMatchesValidate pins across 51,245 instances.
	local.nodeDefaults = map[*xmltree.Node][]defaultAttr{}
	s = &local
	if verr := s.validateElement(root); verr != nil {
		if errors.Is(verr, ErrUnsupported) {
			return nil, ErrUnsupported
		}
		var e *Error
		if errors.As(verr, &e) {
			return &Result{Valid: false, Errors: []Error{*e}}, nil
		}
		return nil, verr
	}
	if verr := s.ids.check(); verr != nil {
		var e *Error
		if errors.As(verr, &e) {
			return &Result{Valid: false, Errors: []Error{*e}}, nil
		}
		return nil, verr
	}
	return &Result{Valid: true}, nil
}

// hintDocs reads the schema documents referenced by the document element's
// xsi:schemaLocation / xsi:noNamespaceSchemaLocation whose namespaces the
// schema does not already hold components for. Unreadable hints are ignored.
func (s *Schema) hintDocs(root *xmltree.Node) []string {
	if s.baseDir == "" {
		return nil
	}
	var locs []string
	if v, ok := root.Attr(xsiNS, "schemaLocation"); ok {
		f := strings.Fields(v)
		for i := 0; i+1 < len(f); i += 2 {
			locs = append(locs, f[i+1]) // a known namespace may still gain decls
		}
	}
	if v, ok := root.Attr(xsiNS, "noNamespaceSchemaLocation"); ok {
		locs = append(locs, strings.TrimSpace(v))
	}
	var out []string
	for _, loc := range locs {
		data, err := os.ReadFile(filepath.Clean(filepath.Join(s.baseDir, loc)))
		if err != nil {
			continue // hints are hints
		}
		out = append(out, string(data))
	}
	return out
}

// bindID records that el carries an ID with value v, sourced from node src
// (the attribute node, or the element itself for content IDs). The SAME
// source repeating a value (one list value holding an ID twice) is never a
// conflict. Beyond that, 1.1 tolerates two bindings of one value on the SAME
// element (several ID attributes on one element are legal there); 1.0 keeps
// strict document-wide uniqueness (elemZ016, idConstrDefs00301m2_n: two
// same-valued ID children of one parent are duplicates).
func (t *idTable) bindID(v string, el, src *xmltree.Node, ver Version) error {
	if prev, ok := t.bound[v]; ok && prev.src != src && !t.elementScope {
		if ver != Version11 || prev.el != el {
			return invalidf("cvc-id.2", "duplicate ID value %q", v)
		}
	}
	t.bound[v] = idBinding{el: el, src: src}
	return nil
}

func (t *idTable) addRef(v string) { t.refs = append(t.refs, v) }

// check resolves the collected IDREF values against the bound IDs.
func (t *idTable) check() error {
	for _, r := range t.refs {
		if _, ok := t.bound[r]; !ok {
			return invalidf("cvc-id.1", "IDREF %q does not match any ID", r)
		}
	}
	return nil
}

// noteIDValues records the ID / IDREF values a validated simple value carries,
// so the document-level ID/IDREF binding can be checked once the whole instance
// has been assessed. bind is the element the ID binds to: the element carrying
// the attribute, or — for an ID in element content — that element's parent, so
// that several ID children of one element may share a value.
func (s *Schema) noteIDValues(st *SimpleType, value string, bind, src *xmltree.Node) error {
	if s.ids == nil || st == nil {
		return nil
	}
	switch st.variety {
	case vAtomic:
		v := applyWhiteSpace("collapse", value)
		switch st.resolvePrim() {
		case xpath.XSid:
			if bind == nil {
				return nil // root-content ID: nothing to bind to (idBindingParent)
			}
			return s.ids.bindID(v, bind, src, s.version)
		case xpath.XSidref:
			s.ids.addRef(v)
		case xpath.XSentity:
			// §3.3.11 (enforced under 1.1): an ENTITY value must name an
			// unparsed entity declared in the instance's DTD (id017/019/020).
			if s.version == Version11 && !s.lenientEntities && !s.unparsed[v] {
				return invalidf("cvc-datatype-valid", "%q does not name an unparsed entity", v)
			}
		}
	case vList:
		if st.item == nil {
			return nil
		}
		// Recurse per item token: the item type may itself be a union whose
		// members carry ID/IDREF (id006/id007 — a list of union(ID, integer)).
		for _, tok := range xmlFields(value) {
			if err := s.noteIDValues(st.item, tok, bind, src); err != nil {
				return err
			}
		}
	case vUnion:
		// The value belongs to the first member type it validates against; only
		// that member decides whether it is an ID or an IDREF.
		for _, m := range st.members {
			if m.validate(value) == nil {
				return s.noteIDValues(m, value, bind, src)
			}
		}
	}
	return nil
}

// markUnassessed records a subtree absorbed by a skip wildcard: identity
// constraints must not select into it (1.1; a no-op map keeps 1.0 semantics —
// the filter below is version-gated anyway).
func (s *Schema) markUnassessed(el *xmltree.Node) {
	if s.unassessed != nil && s.version == Version11 {
		s.unassessed[el] = true
	}
}

// icFilter drops identity-constraint-selected nodes that sit inside a
// skip-absorbed subtree (1.1 §3.11.4: only strictly-assessed nodes reach the
// target/qualified node set — wild101/102/103). Decided declaratively per
// ancestor step, because key tables are built before the subtree is validated.
func (s *Schema) icFilter(nodes []*xmltree.Node) []*xmltree.Node {
	if s.version != Version11 {
		return nodes
	}
	out := nodes[:0]
	for _, n := range nodes {
		if s.icNodeVisible(n) {
			out = append(out, n)
		}
	}
	return out
}

func (s *Schema) icNodeVisible(n *xmltree.Node) bool {
	for cur := n; cur != nil && cur.Kind == xmltree.KindElement; cur = cur.Parent {
		if s.unassessed[cur] {
			return false
		}
		par := cur.Parent
		if par == nil || par.Kind != xmltree.KindElement {
			break
		}
		pt, ok := s.elementTypeOf(par).(*ComplexType)
		if !ok {
			continue
		}
		nn := nameOf(cur)
		decls := map[xname]*ElementDecl{}
		p := s.effectiveParticle(pt)
		s.contentDecls(p, decls)
		if _, declared := decls[nn]; declared {
			continue
		}
		// Mirror validateChildren's attribution order: a skip wildcard absorbs
		// the child BEFORE the global-declaration fallback is consulted.
		var skips []*wildcard
		collectWildcards(p, "skip", &skips)
		if oc := s.effectiveOpenContent(pt); oc != nil && oc.wc != nil && oc.wc.process == "skip" {
			skips = append(skips, oc.wc)
		}
		if matchesAnyWildcard(skips, nn) {
			return false
		}
	}
	return true
}

// annotateAssertType records the type a simple-typed element's or attribute's
// content validated against, feeding the typed-XDM view of later assertion
// evaluations (assert018/assert022). Unions stay untyped.
//
// A LIST is recorded separately (assertLists): its typed value is a SEQUENCE
// of item-typed values, which one primitive cannot describe — the same reframe
// Node.ListTyped applies everywhere else, and without it an assertion reading
// a list-valued attribute sees ONE untyped string holding every token. The
// W3C's own schema-for-xslt30.xsd depends on the distinction: its
// xsl:transform assertion is `every $prefix in (@exclude-result-prefixes...)
// satisfies ... = in-scope-prefixes(.)`, and @exclude-result-prefixes is
// declared xsl:prefix-list, an xs:list — so with the whole attribute
// atomizing to the single string "xs csv" no conformant stylesheet using two
// prefixes could ever satisfy it.
func (s *Schema) annotateAssertType(el *xmltree.Node, st *SimpleType) {
	if st == nil || el == nil {
		return
	}
	if s.assertTypes == nil && s.assertLists == nil {
		// Nothing records annotations on this path, so the union resolution
		// below — which re-validates against each member — would be pure cost.
		return
	}
	// A UNION's typed value is an instance of whichever MEMBER actually
	// accepted the lexical form (XDM 3.1 §3.3.1.2), so it is the member — not
	// the union — that decides between one atomic value and a list's sequence.
	// The same "first member that accepts wins" resolution typeAnnoForNode
	// already uses for the annotation written onto a real tree; a member that
	// is itself a union keeps the old conservative "record nothing".
	//
	// schema-for-xslt30.xsd needs exactly this: @exclude-result-prefixes is
	// xsl:prefix-list-or-all, a union of the xs:list xsl:prefix-list with an
	// enumeration of "#all".
	if st.variety == vUnion {
		m := acceptingUnionMember(st, el.StringValue(), el)
		if m == nil {
			return
		}
		st = m
	}
	if st.variety == vList {
		if s.assertLists == nil {
			return
		}
		it := listItemSimpleType(st)
		if it == nil {
			return
		}
		p := listItemPrim(it)
		if p == 0 {
			return
		}
		s.assertLists[el] = assertList{prim: p, item: s.listItemTypeName(st)}
		return
	}
	if s.assertTypes == nil || st.variety != vAtomic {
		return
	}
	if p := st.resolvePrim(); p != 0 {
		s.assertTypes[el] = p
	}
}

// listItemPrim is the built-in primitive every token of a list whose item type
// is it atomizes through, or 0 when no single primitive covers them all.
//
// An ATOMIC item type simply resolves. A UNION item type is the case that
// matters in practice — xsl:prefix-list's item type is xsl:prefix-or-default,
// a union of xs:NCName with an enumeration of "#default" — and it is only
// answerable at all because one TypeAnno describes every token of the list:
// so the members must AGREE on a primitive (here both are xs:string), and
// when they do not, this reports 0 and the list is left unannotated, exactly
// as before. That is the conservative answer, never a guessed one.
func listItemPrim(it *SimpleType) xpath.AtomType {
	return listItemPrimIn(it, 0)
}

func listItemPrimIn(it *SimpleType, depth int) xpath.AtomType {
	if it == nil || depth > 8 {
		return 0
	}
	switch it.variety {
	case vAtomic:
		return it.resolvePrim()
	case vUnion:
		var got xpath.AtomType
		allStrings := true
		for cur := it; cur != nil; cur = cur.base {
			for _, m := range cur.members {
				p := listItemPrimIn(m, depth+1)
				if p == 0 {
					return 0
				}
				if !isStringFamily(p) {
					allStrings = false
				}
				if got == 0 {
					got = p
				} else if got != p {
					got = -1 // members disagree
				}
			}
			if len(cur.members) > 0 {
				break
			}
		}
		if got != -1 {
			return got
		}
		// The members disagree, and one TypeAnno describes EVERY token of the
		// list, so the only sound answer is a type they all derive from. Within
		// the string family that ancestor exists and is exact — xs:string is
		// the base of every one of them — so each token still casts, and the
		// item type's own NAME travels separately in ListItemType, keeping
		// `data(@a) instance of my:itemType*` answerable precisely.
		// xsl:prefix-list's item type is exactly this shape: a union of
		// xs:NCName with an enumeration restricting xs:token.
		//
		// Outside the string family there is no such ancestor to name without
		// inventing one, so the list is left unannotated — the conservative
		// answer this whole path has always given.
		if allStrings {
			return xpath.XSstring
		}
		return 0
	}
	// A list whose item type is itself a list is not a legal XSD type.
	return 0
}

// isStringFamily reports whether t is one of the built-in types derived from
// xs:string, so that xs:string is a genuine ancestor of it. Kept local rather
// than exported from internal/xpath: this is the only caller, and the list is
// closed by the XSD 1.1 built-in hierarchy itself.
func isStringFamily(t xpath.AtomType) bool {
	switch t {
	case xpath.XSstring, xpath.XSnormalizedString, xpath.XStoken, xpath.XSlanguage,
		xpath.XSname, xpath.XSncname, xpath.XSid, xpath.XSidref, xpath.XSentity,
		xpath.XSnmtoken:
		return true
	}
	return false
}

// idBindingParent returns the element an ID carried as el's *content* binds to,
// or nil for the document element: with no parent ELEMENT the ID binds nothing
// at all — it establishes no binding an IDREF could resolve against
// (s3_3_4ii26i/ii27i: idref_attr pointing at a root-content ID is dangling).
func idBindingParent(el *xmltree.Node) *xmltree.Node {
	if el.Parent != nil && el.Parent.Kind == xmltree.KindElement {
		return el.Parent
	}
	return nil
}

// validateElement validates a top-level (document) element against its global
// declaration.
// stampLoc gives err a source location — the innermost element being assessed
// when it occurred — if it doesn't already carry one. validateElement and
// validateElementAgainst are the two entry points every instance element (root
// and every descendant, via validateChildren's recursive calls back into
// validateElementAgainst) flows through, so deferring this there gives every
// instance-validation *Error a Line/Col without touching each of the ~150
// invalidf call sites individually. The deepest failing element stamps first;
// an ancestor's defer then sees Line/Col already set and leaves it alone.
func stampLoc(err *error, el *xmltree.Node) {
	if *err == nil {
		return
	}
	var e *Error
	if errors.As(*err, &e) && e.Line == 0 && e.Col == 0 {
		e.Line, e.Col = el.Line, el.Col
	}
}

func (s *Schema) validateElement(el *xmltree.Node) (err error) {
	defer stampLoc(&err, el)
	name := nameOf(el)
	decl, ok := s.elements[name]
	if !ok {
		// A document element with no governing declaration may still be assessed
		// against an explicit xsi:type (validation "as a type").
		if xt, hasType := el.Attr(xsiNS, "type"); hasType {
			t, err := s.resolveInstanceType(el, xt)
			if err != nil {
				return err
			}
			return s.validateAgainstType(t, el, map[xname]*keyTable{}, nil)
		}
		return invalidf("cvc-elt.1", "element %s is not declared", name)
	}
	if decl.abstract {
		return invalidf("cvc-elt.2", "abstract element %s cannot appear directly", name)
	}
	return s.validateElementAgainst(decl, el, map[xname]*keyTable{})
}

// validateElementAgainst validates el against element declaration decl, honouring
// xsi:type, xsi:nil, and the declaration's identity constraints (scopes carries
// the key/unique tables in scope for keyref resolution).
func (s *Schema) validateElementAgainst(decl *ElementDecl, el *xmltree.Node, scopes map[xname]*keyTable, extraFixed ...string) (err error) {
	defer stampLoc(&err, el)
	typ := decl.typ
	if xt, ok := el.Attr(xsiNS, "type"); ok {
		t, err := s.resolveInstanceType(el, xt)
		if err != nil {
			return err
		}
		// cvc-elt.4.3: an xsi:type must be validly derived from the element's
		// declared type.
		if decl.typ != nil && !derivesFrom(t, decl.typ) {
			return invalidf("cvc-elt.4.3", "xsi:type is not derived from the declared type of %s", decl.name)
		}
		// @block on the element forbids xsi:type substitution by a blocked
		// derivation method (restriction/extension).
		// cvc-elt.4.3 tests the element's {disallowed substitutions} together with
		// the DECLARED TYPE's {prohibited substitutions} — a complex type's @block
		// bars substitution by that method just as the element's own @block does.
		blocked := decl.blocked
		if dt, ok := decl.typ.(*ComplexType); ok && len(dt.block) > 0 {
			merged := make(map[string]bool, len(blocked)+len(dt.block))
			for m := range blocked {
				merged[m] = true
			}
			for m := range dt.block {
				merged[m] = true
			}
			blocked = merged
		}
		if len(blocked) > 0 && t != decl.typ {
			for m := range derivationMethodsTo(t, decl.typ) {
				if blocked[m] {
					return invalidf("cvc-elt.4.3", "xsi:type substitution by %s is blocked on %s", m, decl.name)
				}
			}
		}
		typ = t
	} else if alt, err := s.selectAlternativeType(decl, el); err != nil {
		return err
	} else if alt != nil {
		typ = alt // XSD 1.1 conditional type assignment
	}
	if _, hasNil := el.Attr(xsiNS, "nil"); hasNil {
		// cvc-elt.3.1: xsi:nil may APPEAR only on a nillable element — its
		// value does not matter (elemO011, nillable00201m2).
		if !decl.nillable {
			return invalidf("cvc-elt.3.1", "element %s is not nillable", decl.name)
		}
		if isNilled(el) {
			// cvc-elt.3.2.1 demands NO character children at all — under 1.1
			// even pure whitespace nils the nil (all004.n02); 1.0 keeps the
			// whitespace-tolerant reading.
			if elementHasContent(el) || (s.version == Version11 && elementHasAnyText(el)) {
				return invalidf("cvc-elt.3.2.1", "nilled element %s must be empty", decl.name)
			}
			// cvc-elt.3.2.2: a nilled element must not carry a fixed value
			// constraint (addB065 — the suite places this error at instance
			// level, exactly as its own annotation says).
			if decl.hasFixed {
				return invalidf("cvc-elt.3.2.2", "nilled element %s has a fixed value constraint", decl.name)
			}
			// cvc-type 3.1.1/3.1.2 still apply to a nilled element — only the
			// VALUE checks are waived (typeDef01201m/01202m: an undeclared
			// attribute stays an error even under xsi:nil).
			if err := s.assessNilledElement(typ, el); err != nil {
				return err
			}
			// The element is valid AND nilled: record the XDM [nilled]
			// property for the in-place annotation path. It keeps its type
			// annotation (noted by assessNilledElement) — nilled says the
			// element has no value, not that it has no type.
			s.noteNodeNilled(el)
			return nil
		}
	}

	// Build this element's key/unique tables, extending the in-scope set for any
	// keyref in this element or its descendants. ownScopes holds only the tables
	// whose scope element is THIS element: a keyref resolves against the node
	// table at its own scope, and tables propagate UPWARD only — a key on an
	// ancestor is not visible to a descendant's keyref (idZ010, addB049, TSTF).
	childScopes := scopes
	ownScopes := map[xname]*keyTable{}
	if len(decl.constraints) > 0 {
		childScopes = cloneScopes(scopes)
		for _, ic := range decl.constraints {
			if ic.kind == "keyref" {
				continue
			}
			tbl, err := s.buildKeyTable(ic, el)
			if err != nil {
				return err
			}
			childScopes[ic.name] = tbl
			ownScopes[ic.name] = tbl
		}
	}

	if err := s.validateAgainstType(typ, el, childScopes, decl); err != nil {
		return err
	}

	// cvc-elt.5.2.2.2: an element with a fixed value constraint and simple content
	// must have that exact value (compared in the type's value space). When the
	// content model holds several same-named declarations with different fixed
	// values, matching any of them is enough (positional ambiguity we don't resolve).
	// cvc-elt.5.2.2.1: an element with a fixed value and mixed content must have
	// no element children at all — the fixed value IS the whole content.
	if decl.hasFixed && hasChildElements(el) {
		if ct, ok := typ.(*ComplexType); ok && ct.kind == contentMixed {
			return invalidf("cvc-elt.5.2.2.1", "element %s has a fixed value and must have no element children", decl.name.Local)
		}
	}
	if decl.hasFixed && !hasChildElements(el) && elementHasContent(el) {
		content := elementText(el)
		st := simpleContentType(typ)
		match := func(fixed string) bool {
			if st != nil {
				return st.fixedEqual(content, fixed)
			}
			return lexEqualCollapsed(content, fixed)
		}
		eq := match(decl.fixed)
		for _, alt := range extraFixed {
			eq = eq || match(alt)
		}
		if !eq {
			return invalidf("cvc-elt.5.2.2.2", "element %s must equal its fixed value", decl.name.Local)
		}
	}

	for _, ic := range decl.constraints {
		if ic.kind != "keyref" {
			continue
		}
		if err := s.checkKeyref(ic, el, ownScopes); err != nil {
			return err
		}
	}
	return nil
}

// assessNilledElement runs the type checks that survive xsi:nil="true": the
// attribute assessment (cvc-type 3.1.1 for simple types, the full attribute
// rules for complex ones). Content and value-constraint checks are waived.
func (s *Schema) assessNilledElement(typ Type, el *xmltree.Node) error {
	// A nilled element keeps its declared type annotation — only the VALUE
	// checks are waived, so the annotation is as real as any other element's.
	s.noteNodeType(el, typ)
	switch t := typ.(type) {
	case *SimpleType:
		for _, a := range el.Attrs {
			if a.Name.Space == xsiNS || a.Name.Space == xmlNS {
				continue
			}
			return invalidf("cvc-type.3.1.1", "attribute %s not allowed on a simple-typed element", a.Name.Local)
		}
	case *ComplexType:
		return s.validateAttributes(t, el)
	}
	return nil
}

func (s *Schema) validateAgainstType(typ Type, el *xmltree.Node, scopes map[xname]*keyTable, decl *ElementDecl) error {
	// Every element reaches its governing type here, with xsi:type,
	// conditional type assignment and substitution already resolved — the one
	// place ValidateNode's annotation can learn what a node was ACTUALLY
	// assessed against (no-op on the Validate path; see noteNodeType).
	s.noteNodeType(el, typ)
	switch t := typ.(type) {
	case *SimpleType:
		for _, a := range el.Attrs {
			if a.Name.Space == xsiNS || a.Name.Space == xmlNS {
				continue
			}
			return invalidf("cvc-type.3.1.1", "attribute %s not allowed on a simple-typed element", a.Name.Local)
		}
		if hasChildElements(el) {
			return invalidf("cvc-type.3.1.2", "simple-typed element must not have child elements")
		}
		text := effectiveSimpleText(decl, el)
		if err := s.yearZeroOK(t, text); err != nil {
			return err
		}
		if err := t.validateIn(text, el); err != nil {
			return err
		}
		s.annotateAssertType(el, t)
		return s.noteIDValues(t, text, idBindingParent(el), el)
	case *ComplexType:
		return s.validateComplex(t, el, scopes, decl)
	}
	return ErrUnsupported
}

// effectiveSimpleText returns the value to validate as an element's simple
// content. cvc-elt.5.1: an element with no character/element content but a
// default or fixed value constraint on its declaration takes the constraint
// value as its schema-normalized value.
func effectiveSimpleText(decl *ElementDecl, el *xmltree.Node) string {
	if decl != nil && !elementHasContent(el) {
		if decl.hasFixed {
			return decl.fixed
		}
		if decl.hasDefault {
			return decl.def
		}
	}
	return elementText(el)
}

func (s *Schema) validateComplex(ct *ComplexType, el *xmltree.Node, scopes map[xname]*keyTable, decl *ElementDecl) error {
	if ct.abstract {
		return invalidf("cvc-type.2", "type is abstract and cannot be used directly")
	}
	if err := s.validateAttributes(ct, el); err != nil {
		return err
	}
	kids := childElements(el)
	switch ct.kind {
	case contentEmpty:
		// An EMPTY content type opened by appliesToEmpty admits wildcard
		// children (s3_4_1v01, open009-013); character data stays forbidden.
		if s.effectiveOpenContent(ct) != nil {
			if hasNonWSText(el) {
				return invalidf("cvc-complex-type.2.1", "open empty content admits no character data")
			}
			if err := s.validateChildren(ct, el, kids, scopes); err != nil {
				return err
			}
			break
		}
		if len(kids) > 0 || hasNonWSText(el) {
			return invalidf("cvc-complex-type.2.1", "element must be empty")
		}
		// 1.1 cvc-complex-type 2.1 forbids EVERY character information item in
		// empty content — whitespace included (open012.n3).
		if s.version == Version11 && elementText(el) != "" {
			return invalidf("cvc-complex-type.2.1", "empty content admits no character data at all")
		}
	case contentSimple:
		if len(kids) > 0 {
			return invalidf("cvc-complex-type.2.2", "simple content must not contain child elements")
		}
		if ct.simpleType != nil {
			text := effectiveSimpleText(decl, el)
			if err := s.yearZeroOK(ct.simpleType, text); err != nil {
				return err
			}
			if err := ct.simpleType.validateIn(text, el); err != nil {
				return err
			}
			s.annotateAssertType(el, ct.simpleType)
			if err := s.noteIDValues(ct.simpleType, text, idBindingParent(el), el); err != nil {
				return err
			}
		}
	case contentElementOnly:
		if hasNonWSText(el) {
			return invalidf("cvc-complex-type.2.3", "element-only content must not contain character data")
		}
		// An empty particle IS the empty {content type} per the mapping rules —
		// and 1.1's cvc-complex-type 2.1 forbids even whitespace there
		// (open012.n3: "Invalid, even whitespace is not allowed").
		if s.version == Version11 && elementText(el) != "" &&
			s.effectiveOpenContent(ct) == nil && isEmptyParticle(s.effectiveParticle(ct)) {
			return invalidf("cvc-complex-type.2.1", "empty content admits no character data at all")
		}
		if err := s.validateChildren(ct, el, kids, scopes); err != nil {
			return err
		}
	case contentMixed:
		if err := s.validateChildren(ct, el, kids, scopes); err != nil {
			return err
		}
	}
	return s.checkAsserts(ct, el)
}

// skipWildcardParseExcuses reports whether some parse of the content model
// attributes child i to a processContents="skip" wildcard (in which case that
// child is not assessed under that parse). The probe replaces the child with a
// name no declaration can match: if the model still matches structurally, a
// wildcard can absorb that position. The name must match only SKIP wildcards —
// a lax/strict wildcard would still assess it against its global declaration.
func (s *Schema) skipWildcardParseExcuses(p *particle, oc *openContent, kids []*xmltree.Node, i int, nn xname, skipWc []*wildcard) bool {
	if !matchesAnyWildcard(skipWc, nn) {
		return false
	}
	if oc != nil && oc.wc != nil && oc.mode == "interleave" {
		if open := s.greedyOpenAttribution(p, kids, oc); open != nil && !open[i] {
			return false // the model takes this child greedily (open025 vs open047)
		}
	}
	var laxWc, strictWc []*wildcard
	collectWildcards(p, "lax", &laxWc)
	collectWildcards(p, "strict", &strictWc)
	if oc != nil && oc.wc != nil && oc.wc.process != "skip" {
		if oc.wc.process == "lax" {
			laxWc = append(laxWc, oc.wc)
		} else {
			strictWc = append(strictWc, oc.wc)
		}
	}
	if matchesAnyWildcard(laxWc, nn) || matchesAnyWildcard(strictWc, nn) {
		return false
	}
	probe := *kids[i]
	probe.Name.Local = "\x00wildcard-probe" // unmatchable by any declaration
	cp := make([]*xmltree.Node, len(kids))
	copy(cp, kids)
	cp[i] = &probe
	// The probe name must stay admissible by the SKIP open wildcard for the
	// open parse to exist (open047: a model-named child re-attributed to the
	// skip open content). A namespace-constrained wildcard keeps the child's
	// namespace admissible; only the local name becomes undeclarable.
	m, ok := s.contentMatchesOpen(p, cp, oc)
	return ok && m
}

// yearZeroOK rejects the year 0000 in the date family under XSD 1.0 (1.1
// adopts the proleptic calendar where it is valid).
func (s *Schema) yearZeroOK(t Type, raw string) error {
	if s.version != Version10 {
		return nil
	}
	st, ok := t.(*SimpleType)
	if !ok || st == nil {
		return nil
	}
	switch st.resolvePrim() {
	case xpath.XSdate, xpath.XSdateTime, xpath.XSgYear, xpath.XSgYearMonth:
		v := strings.TrimSpace(raw)
		v = strings.TrimPrefix(v, "-")
		if strings.HasPrefix(v, "0000") {
			return invalidf("cvc-datatype-valid", "year 0000 is not a valid year in XSD 1.0")
		}
	case xpath.XSfloat, xpath.XSdouble:
		// "+INF" entered the float/double lexical space only in 1.1
		// (float018/double018; the 1.1-valid twins are version-gated).
		if strings.TrimSpace(raw) == "+INF" {
			return invalidf("cvc-datatype-valid", "\"+INF\" is not a valid float/double lexical in XSD 1.0")
		}
	}
	return nil
}

// validateChildren checks the child element sequence against the content model
// and recursively validates each child against its governing declaration.
// elementCapacityFor returns the total number of children the like-named
// element particles of model p can consume, or -1 when that is unbounded.
func elementCapacityFor(p *particle, nn xname) int {
	total, unb := 0, false
	seen := map[*modelGroup]bool{}
	var walk func(pp *particle, mult int)
	walk = func(pp *particle, mult int) {
		if pp == nil || pp.max == 0 || unb {
			return
		}
		switch t := pp.term.(type) {
		case *ElementDecl:
			if t.name != nn {
				return
			}
			if pp.max == unbounded || mult < 0 {
				unb = true
				return
			}
			total += pp.max * mult
		case *modelGroup:
			if seen[t] {
				unb = true // cyclic model group: never exhausted
				return
			}
			seen[t] = true
			m2 := -1
			if pp.max != unbounded && mult > 0 {
				m2 = mult * pp.max
			}
			for _, sub := range t.particles {
				walk(sub, m2)
			}
			delete(seen, t)
		}
	}
	walk(p, 1)
	if unb {
		return -1
	}
	return total
}

// wildcardParseExists reports whether some parse of the content model
// attributes child i to a wildcard: the probe rename keeps the namespace (so
// namespace-constrained wildcards still admit it) but takes a local name no
// declaration can match. kids may already carry probes for children pinned to
// wildcards by earlier attribution decisions.
func (s *Schema) wildcardParseExists(p *particle, oc *openContent, kids []*xmltree.Node, i int) bool {
	probe := *kids[i]
	probe.Name.Local = "\x00wildcard-probe"
	cp := make([]*xmltree.Node, len(kids))
	copy(cp, kids)
	cp[i] = &probe
	m, ok := s.contentMatchesOpen(p, cp, oc)
	return ok && m
}

// assessWildcardChild handles a child attributed to a non-skip wildcard whose
// name ALSO has a like-named model particle (dynamic EDC, 1.1): the governing
// type must be validly derived from the context-determined type tc, then the
// child is assessed against its global declaration (or its xsi:type when no
// declaration exists; a lax match with neither is accepted as-is).
func (s *Schema) assessWildcardChild(tc Type, kid *xmltree.Node, nn xname, scopes map[xname]*keyTable, laxWc, strictWc []*wildcard) error {
	var tg Type
	xt, hasXsiType := kid.Attr(xsiNS, "type")
	if hasXsiType {
		t, err := s.resolveInstanceType(kid, xt)
		if err != nil {
			return err
		}
		tg = t
	} else if g, ok := s.elements[nn]; ok {
		tg = g.typ
	}
	if tg != nil && tc != nil && !derivesFrom(tg, tc) {
		return invalidf("cvc-assess-elt",
			"element %s: governing type is not validly derived from its context-determined type", nn)
	}
	if g, ok := s.elements[nn]; ok {
		return s.validateElementAgainst(g, kid, scopes)
	}
	if hasXsiType {
		return s.validateElementAgainst(&ElementDecl{name: nn}, kid, scopes)
	}
	if matchesAnyWildcard(strictWc, nn) && !matchesAnyWildcard(laxWc, nn) {
		return invalidf("cvc-assess-elt", "element %s has no declaration to assess it against", nn)
	}
	return nil
}

// substMatchesAnyDecl reports whether nn can reach some model declaration as a
// substitution-group member.
func substMatchesAnyDecl(s *Schema, decls map[xname]*ElementDecl, nn xname) bool {
	for _, d := range decls {
		if s.matchesElementName(d, nn) {
			return true
		}
	}
	return false
}

// baseChainContextType finds the type of a like-named element particle in ct's
// base chain (the context-determined type a restriction removed from the model).
func (s *Schema) baseChainContextType(ct *ComplexType, nn xname) Type {
	for c, n := ct, 0; n < 64; n++ {
		base, ok := c.baseType.(*ComplexType)
		if !ok || base == nil || base == c {
			return nil
		}
		decls := map[xname]*ElementDecl{}
		s.contentDecls(s.effectiveParticle(base), decls)
		if d, ok := decls[nn]; ok {
			return d.typ
		}
		c = base
	}
	return nil
}

func (s *Schema) validateChildren(ct *ComplexType, el *xmltree.Node, kids []*xmltree.Node, scopes map[xname]*keyTable) error {
	p := s.effectiveParticle(ct)
	oc := s.effectiveOpenContent(ct)
	matched, ok := s.contentMatchesOpen(p, kids, oc)
	if !ok {
		return ErrUnsupported // pathological content model — no verdict
	}
	if !matched {
		e := invalidf("cvc-complex-type.2.4", "element %s: content does not match its content model (%s; allowed child elements: %s)",
			nameOf(el), describeChildren(kids), s.describeAllowedNames(p))
		e.Line, e.Col = el.Line, el.Col
		return e
	}
	decls := map[xname]*ElementDecl{}
	s.contentDecls(p, decls)
	fixedsByName := map[xname][]string{}
	collectFixedsByName(p, fixedsByName, map[*modelGroup]bool{})
	var skipWc, laxWc, strictWc []*wildcard
	collectWildcards(p, "skip", &skipWc)
	collectWildcards(p, "lax", &laxWc)
	collectWildcards(p, "strict", &strictWc)
	if oc != nil && oc.wc != nil {
		// Children admitted by the open-content wildcard are assessed per its
		// processContents, exactly like model wildcards.
		switch oc.wc.process {
		case "skip":
			skipWc = append(skipWc, oc.wc)
		case "lax":
			laxWc = append(laxWc, oc.wc)
		default:
			strictWc = append(strictWc, oc.wc)
		}
	}
	elemUsed := map[xname]int{}
	pinned := kids // lazily copied; wildcard-attributed children get probed out
	for i, kid := range kids {
		nn := nameOf(kid)
		if d, ok := decls[nn]; ok {
			// XSD 1.1 dynamic Element Declarations Consistent: once the
			// like-named element particles are EXHAUSTED, a further same-named
			// child is attributed to a non-skip wildcard — its governing type
			// (xsi:type, else the global declaration's) must then be validly
			// derived from the like-named particle's type, and assessment runs
			// against the GLOBAL declaration, not the local one
			// (wild062.n1-n3, wild063.n1/v1/v2, s3_8_6v01i/ii01i).
			if s.version == Version11 &&
				(matchesAnyWildcard(laxWc, nn) || matchesAnyWildcard(strictWc, nn)) {
				if cap := elementCapacityFor(p, nn); cap >= 0 && elemUsed[nn] >= cap &&
					s.wildcardParseExists(p, oc, pinned, i) {
					if err := s.assessWildcardChild(d.typ, kid, nn, scopes, laxWc, strictWc); err != nil {
						return err
					}
					if &pinned[0] == &kids[0] {
						pinned = make([]*xmltree.Node, len(kids))
						copy(pinned, kids)
					}
					pr := *kid
					pr.Name.Local = "\x00wildcard-probe"
					pinned[i] = &pr
					continue
				}
			}
			elemUsed[nn]++
			if err := s.validateElementAgainst(d, kid, scopes, fixedsByName[nn]...); err != nil {
				// Attribution is by name, but the model may also offer a
				// processContents="skip" wildcard absorbing this child at this
				// position — under that parse the child is not assessed at all
				// (QFE1700c2: <any skip/> before <ref e2/> takes the first e2).
				// A child for which SOME parse validates is not an error.
				if s.skipWildcardParseExcuses(p, oc, kids, i, nn, skipWc) {
					s.markUnassessed(kid)
					continue
				}
				return err
			}
			continue
		}
		// A child matched by a processContents="skip" wildcard is not assessed.
		if matchesAnyWildcard(skipWc, nn) {
			s.markUnassessed(kid)
			continue
		}
		// Substitution-group member or a strict/lax wildcard match: its own global
		// declaration governs, if one exists.
		if g, ok := s.elements[nn]; ok {
			// Dynamic EDC also reaches a wildcard-attributed child whose
			// like-named particle lives only in the BASE chain — a restriction
			// dropped it, but the context-determined type survives (wild068.n1:
			// global e:duration vs the base's union(date,time)). A child that
			// can arrive as a substitution-group member is exempt: that
			// attribution is not a wildcard match.
			if s.version == Version11 &&
				(matchesAnyWildcard(laxWc, nn) || matchesAnyWildcard(strictWc, nn)) &&
				!substMatchesAnyDecl(s, decls, nn) {
				if tc := s.baseChainContextType(ct, nn); tc != nil {
					tg := g.typ
					if xt, ok := kid.Attr(xsiNS, "type"); ok {
						if t, err := s.resolveInstanceType(kid, xt); err == nil {
							tg = t
						}
					}
					if tg != nil && !derivesFrom(tg, tc) {
						return invalidf("cvc-assess-elt",
							"element %s: governing type is not validly derived from its context-determined type", nn)
					}
				}
			}
			if err := s.validateElementAgainst(g, kid, scopes); err != nil {
				return err
			}
			continue
		}
		// A strict wildcard demands that the match be assessed, so a name with no
		// declaration (and no xsi:type to supply one) is invalid.
		// In 1.1 an entirely unknown namespace keeps the lenient reading
		// (tns5); in 1.0 a strict wildcard match without a declaration is an
		// error outright (addB013, addB087) — hints, if any, were loaded above.
		// Attribution is positional: if a lax or skip wildcard in the model
		// also admits the name, some parse assigns the child there and no
		// strict assessment is demanded (wildI005).
		if _, hasXsiType := kid.Attr(xsiNS, "type"); !hasXsiType &&
			matchesAnyWildcard(strictWc, nn) &&
			!matchesAnyWildcard(laxWc, nn) && !matchesAnyWildcard(skipWc, nn) {
			return invalidf("cvc-assess-elt", "element %s has no declaration to assess it against", nn)
		}
		// Otherwise (skip/lax wildcard, or strict with xsi:type) accept as-is —
		// enforcing strict-with-no-declaration here caused false positives.
		//
		// Accepting the ELEMENT as-is is not the same as ignoring its
		// contents: XSD 1.0 §3.3.4 clause 2 says an element a LAX wildcard
		// admits but for which no declaration can be found is ·laxly
		// assessed·, and lax assessment recurses — the element's own [type
		// definition] stays absent, but its children are each assessed in
		// turn against their own global declaration where one can be found.
		// Only a processContents="skip" wildcard makes a subtree opaque.
		if matchesAnyWildcard(laxWc, nn) && !matchesAnyWildcard(skipWc, nn) {
			if err := s.laxAssessSubtree(kid, scopes); err != nil {
				return err
			}
		}
	}
	return nil
}

// laxAssessSubtree carries out ·lax assessment· (XSD 1.0 §3.3.4 clause 2)
// below an element that a lax wildcard admitted but no declaration governs:
// each descendant element is assessed against its own global declaration as
// soon as one can be found, and descended into otherwise.
//
// Without it a lax region is a black hole rather than a lenient one. An XSLT
// stylesheet validated against schema-for-xslt20.xsd is the standard case:
// literal result elements reach the sequence constructor through
// <xs:any namespace="##local" processContents="lax"/> and have no declaration
// of their own, so every xsl:* instruction nested inside one used to come out
// untyped — count(//schema-element(xsl:instruction)) gave 57 of the document's
// 108 instructions, exactly those with no literal result element above them
// (validation-0501/0601/0701).
func (s *Schema) laxAssessSubtree(n *xmltree.Node, scopes map[xname]*keyTable) error {
	for _, kid := range n.Children {
		if kid.Kind != xmltree.KindElement {
			continue
		}
		nn := nameOf(kid)
		if g, ok := s.elements[nn]; ok {
			if err := s.validateElementAgainst(g, kid, scopes); err != nil {
				return err
			}
			continue
		}
		if err := s.laxAssessSubtree(kid, scopes); err != nil {
			return err
		}
	}
	return nil
}

// describeChildren renders the actual child element sequence for a
// cvc-complex-type.2.4 diagnostic ("found: a, b, c" / "found: (none)").
func describeChildren(kids []*xmltree.Node) string {
	if len(kids) == 0 {
		return "found: (none)"
	}
	names := make([]string, len(kids))
	for i, k := range kids {
		names[i] = nameOf(k).String()
	}
	return "found: " + strings.Join(names, ", ")
}

// describeAllowedNames renders the (deduplicated, sorted) set of element names
// that appear anywhere in a content model, for a cvc-complex-type.2.4
// diagnostic. It is a coarse "what's legal here at all" hint, not a
// position-exact expectation — the matcher doesn't track a failure position.
func (s *Schema) describeAllowedNames(p *particle) string {
	decls := map[xname]*ElementDecl{}
	s.contentDecls(p, decls)
	if len(decls) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(decls))
	for n := range decls {
		names = append(names, n.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// collectFixedsByName records, per element name in the content model, every fixed
// value carried by a declaration of that name (used to tolerate several same-named
// declarations with different fixed values).
func collectFixedsByName(p *particle, out map[xname][]string, seen map[*modelGroup]bool) {
	if p == nil {
		return
	}
	switch t := p.term.(type) {
	case *ElementDecl:
		if t.hasFixed {
			out[t.name] = append(out[t.name], t.fixed)
		}
	case *modelGroup:
		if seen[t] {
			return
		}
		seen[t] = true
		for _, sub := range t.particles {
			collectFixedsByName(sub, out, seen)
		}
	}
}

// collectWildcards gathers every wildcard leaf of p whose processContents is proc.
func collectWildcards(p *particle, proc string, out *[]*wildcard) {
	if p == nil {
		return
	}
	switch t := p.term.(type) {
	case *wildcard:
		if t.process == proc {
			*out = append(*out, t)
		}
	case *modelGroup:
		for _, sub := range t.particles {
			collectWildcards(sub, proc, out)
		}
	}
}

func matchesAnyWildcard(ws []*wildcard, nn xname) bool {
	for _, w := range ws {
		if wildcardMatches(w, nn) {
			return true
		}
	}
	return false
}

func cloneScopes(m map[xname]*keyTable) map[xname]*keyTable {
	out := make(map[xname]*keyTable, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Schema) resolveInstanceType(el *xmltree.Node, q string) (Type, error) {
	n := resolveQName(el, q)
	if n.Space == xsNS {
		// XDM-only types are not schema types, and 1.1-only built-ins do not
		// resolve under 1.0 — same gate as the schema-side resolveType.
		if n.Local == "untypedAtomic" || n.Local == "untyped" ||
			(s.version == Version10 && xsd11OnlyBuiltin[n.Local]) {
			return nil, invalidf("cvc-elt.4.2", "xsi:type %s is not a schema type", n)
		}
		if n.Local == "error" {
			return xsErrorType, nil
		}
		if lt, ok := builtinList(n.Local); ok {
			return lt, nil
		}
		if at, ok := builtinAtom(n.Local); ok {
			return &SimpleType{variety: vAtomic, baseBuiltin: true, prim: at}, nil
		}
		if n.Local == "anyType" {
			return anyType(), nil
		}
		return nil, invalidf("cvc-elt.4.2", "xsi:type names an unknown built-in xs:%s", n.Local)
	}
	if t, ok := s.types[n]; ok {
		return t, nil
	}
	return nil, invalidf("cvc-elt.4.2", "xsi:type %s cannot be resolved", n)
}

// --- node helpers -----------------------------------------------------------

// parseBlockSet parses a block value into the set of blocked derivation methods.
func parseBlockSet(v string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.Fields(v) {
		if tok == "#all" {
			out["restriction"], out["extension"], out["substitution"] = true, true, true
		} else {
			out[tok] = true
		}
	}
	return out
}

// derivationMethodsTo returns the derivation methods used walking t's type chain
// down to base (empty if base is not reached).
func derivationMethodsTo(t, base Type) map[string]bool {
	anyTypeBase := false
	if bc, ok := base.(*ComplexType); ok && bc != nil && bc.name == (xname{xsNS, "anyType"}) {
		anyTypeBase = true
	}
	methods := map[string]bool{}
	cur := t
	for i := 0; cur != nil && i < 64; i++ {
		if typeIdentityEqual(cur, base) {
			return methods
		}
		switch c := cur.(type) {
		case *ComplexType:
			if c == nil {
				return map[string]bool{}
			}
			if c.derivation != "" {
				methods[c.derivation] = true
				cur = c.baseType
			} else if c.name != (xname{xsNS, "anyType"}) {
				// A complexType with no explicit derivation is an implicit
				// RESTRICTION of xs:anyType (particlesIg003: block="restriction"
				// bars an xsi:type whose chain passes through a plain type).
				methods["restriction"] = true
				if anyTypeBase {
					return methods
				}
				cur = nil
			} else {
				cur = c.baseType
			}
		case *SimpleType:
			if c == nil {
				return map[string]bool{}
			}
			methods["restriction"] = true
			if c.base != nil {
				cur = c.base
			} else {
				// The user chain bottomed out on a BUILT-IN base. If the target
				// base is a direct builtin reference the remaining steps are the
				// builtin restriction ladder — reached iff c's builtin derives
				// from it (disallowedSubst00501m2: a facet-less restriction of
				// xs:string IS derived-by-restriction from xs:string).
				if bs, ok := base.(*SimpleType); ok && bs != nil &&
					bs.base == nil && bs.baseBuiltin && bs.name.zero() &&
					c.baseBuiltin && xpath.AtomDerivesFrom(c.prim, bs.prim) {
					return methods
				}
				// A simple-type root's ultimate base is xs:anySimpleType → xs:anyType,
				// reached by restriction.
				if anyTypeBase {
					return methods
				}
				cur = nil
			}
		default:
			cur = nil
		}
	}
	// Union-member substitutability counts as derivation BY RESTRICTION, so an
	// element blocking restriction also blocks the member route (elemT074).
	if bs, ok := base.(*SimpleType); ok && bs.variety == vUnion {
		if ts, ok := t.(*SimpleType); ok && simpleDerivesFrom(ts, bs) {
			return map[string]bool{"restriction": true}
		}
	}
	return map[string]bool{} // base not reached — no blocked path
}

// typeIdentityEqual reports whether two type references denote the same named
// type (or are the same instance). Named types are single instances but built-in
// wrappers may be distinct, so names are compared as a fallback.
func typeIdentityEqual(a, b Type) bool {
	if a == b {
		return true
	}
	switch av := a.(type) {
	case *ComplexType:
		if bv, ok := b.(*ComplexType); ok {
			return !av.name.zero() && av.name == bv.name
		}
	case *SimpleType:
		if bv, ok := b.(*SimpleType); ok {
			if !av.name.zero() && av.name == bv.name {
				return true
			}
			// Two direct references to the same built-in denote the same type
			// (each reference materializes its own sentinel struct).
			return av.name.zero() && bv.name.zero() &&
				av.baseBuiltin && bv.baseBuiltin && av.base == nil && bv.base == nil &&
				!av.facets.any() && !bv.facets.any() && av.prim == bv.prim
		}
	}
	return false
}

// derivesFrom reports whether type t is base, or is derived from base through a
// chain of extension/restriction. A nil base or xs:anyType base matches anything.
func derivesFrom(t, base Type) bool { return derivesFromMode(t, base, true) }

// derivesFromMode is derivesFrom with the XSD "a union's member type is
// substitutable for the union" clause (cos-st-derived-ok 2.2.4) switchable.
//
// XSD needs it: an xsi:type naming a member of a union is valid against that
// union. XPath does NOT use the same relation — its own derives-from(AT, ET)
// (XPath 3.1 §2.5.6) admits a union only when ET is a PURE union type, i.e.
// one whose member types are all atomic (or themselves pure unions) — so the
// XSLT bridge asks with unionSubst false and applies the narrower rule itself
// (see xpathDerivesFrom).
func derivesFromMode(t, base Type, unionSubst bool) bool {
	if base == nil {
		return true
	}
	if bc, ok := base.(*ComplexType); ok && bc.name == (xname{xsNS, "anyType"}) {
		return true
	}
	if bs, ok := base.(*SimpleType); ok {
		// Every type derives from xs:anySimpleType, which resolves to a fresh
		// unnamed wrapper on each reference — so identity/name comparison down the
		// chain would never find it.
		if bs.name.zero() && bs.baseBuiltin && bs.base == nil && !bs.facets.any() &&
			bs.variety == vAtomic && bs.prim == xpath.XSanyAtomicType {
			// …except xs:anyType, which sits *above* xs:anySimpleType.
			if tc, ok := t.(*ComplexType); ok && tc.name == (xname{xsNS, "anyType"}) {
				return false
			}
			return true
		}
		// Simple→simple substitution follows the atomic hierarchy; a complex type
		// with simple content deriving from a simple base falls through to the
		// general chain walk below.
		if ts, ok := t.(*SimpleType); ok {
			return simpleDerivesFromMode(ts, bs, unionSubst)
		}
	}
	cur := t
	for i := 0; !isNilType(cur) && i < 64; i++ {
		if typeIdentityEqual(cur, base) {
			return true
		}
		switch c := cur.(type) {
		case *ComplexType:
			cur = c.baseType
		case *SimpleType:
			cur = c.base
		default:
			cur = nil
		}
	}
	return false
}

// isNilType reports whether a Type is absent — either a nil interface or an
// interface holding a nil *SimpleType / *ComplexType, which a plain `!= nil`
// test would let through (a builtin's base is such a typed nil).
func isNilType(t Type) bool {
	switch v := t.(type) {
	case nil:
		return true
	case *SimpleType:
		return v == nil
	case *ComplexType:
		return v == nil
	}
	return false
}

// simpleDerivesFrom reports whether simple type t is, or restricts (directly or
// transitively), the simple type base. Atomic types fall back to the built-in
// atomic hierarchy; list/union derivations are accepted conservatively.
func simpleDerivesFrom(t, base *SimpleType) bool { return simpleDerivesFromMode(t, base, true) }

func simpleDerivesFromMode(t, base *SimpleType, unionSubst bool) bool {
	for s := t; s != nil; s = s.base {
		if s == base || (!s.name.zero() && s.name == base.name) {
			return true
		}
	}
	// A member of a union (or a type derived from one) is substitutable for the
	// union via xsi:type, even though it is not "derived" from it — but only
	// while the union carries NO facets of its own: a restriction of the union
	// constrains the value space, and a member type escapes those facets
	// (cos-st-derived-ok 2.2.4; stZ073b).
	if base.variety == vUnion && !base.facets.any() {
		if !unionSubst {
			return false
		}
		for _, m := range base.members {
			if simpleDerivesFrom(t, m) {
				return true
			}
		}
		return false
	}
	// The atomic-hierarchy fallback applies only when the base is a built-in
	// primitive (unnamed wrapper). For a named user simple type, the name walk
	// above is authoritative — sharing a primitive does NOT imply derivation.
	if base.baseBuiltin && base.name.zero() {
		bp := base.resolvePrim()
		if bp == xpath.XSanyAtomicType {
			return true // everything derives from xs:anySimpleType/anyAtomicType
		}
		return t.variety == vAtomic && xpath.AtomDerivesFrom(t.resolvePrim(), bp)
	}
	// A named user base the name walk did not connect is not a valid derivation.
	return false
}

func isNilled(el *xmltree.Node) bool {
	v, ok := el.Attr(xsiNS, "nil")
	return ok && (v == "true" || v == "1")
}

func childElements(el *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindElement {
			out = append(out, ch)
		}
	}
	return out
}

func hasChildElements(el *xmltree.Node) bool {
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindElement {
			return true
		}
	}
	return false
}

func hasNonWSText(el *xmltree.Node) bool {
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "" {
			return true
		}
	}
	return false
}

func elementText(el *xmltree.Node) string {
	var b strings.Builder
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindText {
			b.WriteString(ch.Value)
		}
	}
	return b.String()
}

// elementHasAnyText reports whether el has ANY text children, whitespace
// included (the 1.1 reading of cvc-elt.3.2.1's "no character children").
func elementHasAnyText(el *xmltree.Node) bool {
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindText && ch.Value != "" {
			return true
		}
	}
	return false
}

func elementHasContent(el *xmltree.Node) bool {
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindElement {
			return true
		}
		if ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "" {
			return true
		}
	}
	return false
}
