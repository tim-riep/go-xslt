package xsd

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// XSD 1.1: assertions (xs:assert) and conditional type assignment
// (xs:alternative). Both compile an XPath test with the engine and evaluate it
// against the element being validated. They are enforced only in 1.1 mode.

// parseAsserts reads the xs:assert children of node (a complexType body,
// extension, or restriction).
func (c *compiler) parseAsserts(node *xmltree.Node) ([]*assertion, error) {
	var out []*assertion
	for _, ch := range node.Children {
		if !isXS(ch, "assert") {
			continue
		}
		a, err := c.parseAssert(ch)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (c *compiler) parseAssert(ch *xmltree.Node) (*assertion, error) {
	test, _ := ch.AttrLocal("test")
	p, err := xpath.Parse(test)
	if err != nil {
		// A STATIC XPath error in @test is a schema error (s3_12si04-06's
		// deliberately malformed expressions); no valid suite schema carries
		// XPath our parser cannot handle.
		return nil, invalidf("", "invalid XPath in @test: %v", err)
	}
	a := &assertion{test: p, ns: ch.InScopeNamespaces()}
	a.defaultNS = c.resolveXPathDefaultNS(ch)
	return a, nil
}

// resolveXPathDefaultNS resolves the effective xpathDefaultNamespace for an
// XPath-carrying schema element: the nearest xpathDefaultNamespace attribute on
// the element or an ancestor (xs:schema supplies a document-wide default) wins,
// and the three ## tokens expand per §3.13.2 — ##targetNamespace to the schema
// document's (possibly chameleon-adopted) target namespace, ##defaultNamespace
// to the default namespace in scope at the carrying element, ##local (and an
// absent attribute) to the absent namespace.
func (c *compiler) resolveXPathDefaultNS(node *xmltree.Node) string {
	var raw string
	var carrier *xmltree.Node
	for n := node; n != nil && n.Kind == xmltree.KindElement; n = n.Parent {
		if v, ok := n.AttrLocal("xpathDefaultNamespace"); ok {
			raw, carrier = v, n
			break
		}
		if isXS(n, "schema") {
			break
		}
	}
	if carrier == nil {
		return ""
	}
	switch strings.TrimSpace(raw) {
	case "##local":
		return ""
	case "##targetNamespace":
		return c.tns
	case "##defaultNamespace":
		ns, _ := carrier.LookupPrefix("")
		return ns
	default:
		return strings.TrimSpace(raw)
	}
}

// parseAlternatives reads the xs:alternative children of an element node.
func (c *compiler) parseAlternatives(node *xmltree.Node) ([]*typeAlternative, error) {
	var out []*typeAlternative
	var last *xmltree.Node
	for _, ch := range node.Children {
		if isXS(ch, "alternative") {
			last = ch
		}
	}
	for _, ch := range node.Children {
		if !isXS(ch, "alternative") {
			continue
		}
		// Only the LAST alternative (the default) may omit @test
		// (§3.3.3 element sanity, cta9001err).
		if _, hasTest := ch.AttrLocal("test"); !hasTest && ch != last {
			return nil, invalidf("", "only the last xs:alternative may omit @test")
		}
		alt := &typeAlternative{ns: ch.InScopeNamespaces()}
		alt.defaultNS = c.resolveXPathDefaultNS(ch)
		if test, ok := ch.AttrLocal("test"); ok {
			p, err := xpath.Parse(test)
			if err != nil {
				return nil, invalidf("", "invalid XPath in xs:alternative/@test: %v", err)
			}
			alt.test = p
			alt.rawTest = test
		}
		if typeRef, ok := ch.AttrLocal("type"); ok {
			t, err := c.resolveType(ch, typeRef)
			if err != nil {
				return nil, err
			}
			alt.typ = t
		} else if inline := firstXSChild(ch, "complexType"); inline != nil {
			t := &ComplexType{}
			if err := c.parseComplexTypeInto(t, inline); err != nil {
				return nil, err
			}
			alt.typ = t
		} else if st := firstXSChild(ch, "simpleType"); st != nil {
			t := &SimpleType{}
			if err := c.parseSimpleTypeInto(t, st); err != nil {
				return nil, err
			}
			alt.typ = t
		}
		out = append(out, alt)
	}
	return out, nil
}

// inheritedAttrs collects the nearest-ancestor values of {inheritable}
// attributes not overridden on el itself (§3.3.4.3).
func (s *Schema) inheritedAttrs(el *xmltree.Node) []*xmltree.Node {
	var out []*xmltree.Node
	have := map[xname]bool{}
	for _, a := range el.Attrs {
		have[xname{a.Name.Space, a.Name.Local}] = true
	}
	for anc := el.Parent; anc != nil && anc.Kind == xmltree.KindElement; anc = anc.Parent {
		ct, ok := s.elementTypeOf(anc).(*ComplexType)
		if !ok {
			continue
		}
		uses, _ := s.effectiveAttrs(ct)
		for _, u := range uses {
			if !u.inheritable || have[u.decl.name] {
				continue
			}
			for _, a := range anc.Attrs {
				if a.Name.Space == u.decl.name.Space && a.Name.Local == u.decl.name.Local {
					out = append(out, a)
					have[u.decl.name] = true
					break
				}
			}
		}
	}
	return out
}

// valueVar binds $value for simple-type assertion facets.
type valueVar struct{ v xpath.Object }

func (b valueVar) ResolveVar(prefix, local string) (xpath.Object, bool) {
	if prefix == "" && local == "value" {
		return b.v, true
	}
	return nil, false
}

// typedSimpleValue casts an already-validated lexical to its primitive's
// typed atomic; on a cast the engine cannot do, the untyped reading stands in.
func typedSimpleValue(v string, prim xpath.AtomType) xpath.Object {
	if a, err := xpath.CastTo(xpath.NewString(v), prim); err == nil {
		return a
	}
	return xpath.NewUntyped(v)
}

// evalSimpleAssertions runs one restriction step's xs:assertion facets
// against the typed $value (§3.13.4.1 applied to datatypes): the focus is
// ABSENT — '.', position() and last() raise dynamic errors — and any dynamic
// error or non-true result makes the value invalid (assert-simple007-010).
func evalSimpleAssertions(asserts []*assertion, value xpath.Object) error {
	for _, a := range asserts {
		obj, err := a.test.Eval(&xpath.Context{
			NS:            xsdNS(a.ns),
			DefaultElemNS: a.defaultNS,
			Vars:          valueVar{value},
			NoFocus:       true,
		})
		if err != nil || !xpath.ToBool(obj) {
			return invalidf("cvc-assertions-valid", "assertion on the value failed")
		}
	}
	return nil
}

// effectiveAsserts collects the assertions of a complex type and its base chain.
func (s *Schema) effectiveAsserts(ct *ComplexType) []*assertion {
	var out []*assertion
	for c, n := ct, 0; c != nil && n < 64; n++ {
		out = append(out, c.asserts...)
		base, ok := c.baseType.(*ComplexType)
		if !ok || c.derivation == "" {
			break
		}
		c = base
	}
	return out
}

// assertTree builds the XDM tree an assertion is evaluated against
// (§3.13.4.2): a DETACHED copy of the element — '/', '//' and ancestor axes
// cannot escape it (d4_3_15ii14i/ii15i) — with comment and PI nodes filtered
// out (assert023: empty(.//comment()) must hold).
func (s *Schema) assertTree(el *xmltree.Node) *xmltree.Node {
	var clone func(n *xmltree.Node) *xmltree.Node
	clone = func(n *xmltree.Node) *xmltree.Node {
		cp := *n
		cp.Parent = nil
		// Attributes are VALUE-cloned so their typed-XDM annotation lives
		// only on the assertion tree (assert_002/018, d4_3_15v02/v11: a
		// validated @length atomizes as its declared type, not untyped).
		cp.Attrs = make([]*xmltree.Node, 0, len(n.Attrs)+len(s.nodeDefaults[n]))
		for _, at := range n.Attrs {
			ac := *at
			ac.Parent = &cp
			s.annotateAssertNode(&ac, at)
			cp.Attrs = append(cp.Attrs, &ac)
		}
		// Attributes the element OMITTED but whose use carries a default or
		// fixed value constraint (cvc-complex-type.4) are part of the
		// post-schema-validation element the assertion is evaluated against —
		// validateAttributes has already recorded them for this element, and
		// it runs before checkAsserts.
		//
		// The W3C's own schema-for-xslt30.xsd settles that reading for this
		// engine: xsl:number declares level with default="single" and then
		// asserts `normalize-space(@level)='single'` whenever @value is
		// present, which is its way of spelling "@level was not written". A
		// plain <xsl:number value="$x"/> — legal XSLT, and used three times in
		// the Recommendation's OWN examples — can satisfy that assertion only
		// if the default is visible to it.
		for _, d := range s.nodeDefaults[n] {
			if _, exists := n.Attr(d.name.Space, d.name.Local); exists {
				continue
			}
			ac := &xmltree.Node{
				Kind:   xmltree.KindAttribute,
				Name:   xmltree.Name{Space: d.name.Space, Local: d.name.Local},
				Value:  d.value,
				Parent: &cp,
			}
			ac.TypeAnno = int32(typeAnnoForNode(d.typ, ac))
			ac.ListTyped = listTypedFor(d.typ)
			ac.ListItemType = s.listItemTypeName(d.typ)
			cp.Attrs = append(cp.Attrs, ac)
		}
		cp.Children = nil
		// Typed-XDM view (§3.13.4.1): an already-validated simple-typed
		// DESCENDANT atomizes to its typed value (assert018 data(d),
		// assert022 data(event/d)); the assertion ROOT itself stays untyped —
		// its own type is exactly what is being validated (assert022's base
		// assert demands xs:untypedAtomic there).
		if n != el {
			s.annotateAssertNode(&cp, n)
		}
		for _, ch := range n.Children {
			if ch.Kind == xmltree.KindComment || ch.Kind == xmltree.KindPI {
				continue
			}
			c2 := clone(ch)
			c2.Parent = &cp
			cp.Children = append(cp.Children, c2)
		}
		return &cp
	}
	root := clone(el)
	// The tree is DETACHED, so namespace bindings declared on ANCESTORS of
	// the assertion root would be lost: materialize the root's in-scope
	// bindings (QName-typed content — assert024's @name — resolves against
	// them).
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
	return root
}

// annotateAssertNode copies the typed-XDM annotation recorded for src onto its
// assertion-tree clone dst: one primitive for an atomic type, or the
// item-primitive + list marks for a list, whose typed value is a sequence.
func (s *Schema) annotateAssertNode(dst, src *xmltree.Node) {
	if t, ok := s.assertTypes[src]; ok {
		dst.TypeAnno = int32(t)
		return
	}
	if l, ok := s.assertLists[src]; ok {
		dst.TypeAnno = int32(l.prim)
		dst.ListTyped = true
		dst.ListItemType = l.item
	}
}

// checkAsserts evaluates a complex type's assertions against el (1.1 only).
func (s *Schema) checkAsserts(ct *ComplexType, el *xmltree.Node) error {
	if s.version != Version11 {
		return nil
	}
	asserts := s.effectiveAsserts(ct)
	if len(asserts) == 0 {
		return nil
	}
	// $value IS bound in element-level asserts (§3.13.4.1): the typed simple
	// content when the {content type} is simple (assert010/015; a list gives
	// a SEQUENCE — assert_035), and the EMPTY sequence otherwise (assert019
	// pins empty($value) valid on complex content).
	var value xpath.Object = xpath.Sequence{}
	if ct.kind == contentSimple && ct.simpleType != nil {
		value = typedValueOf(ct.simpleType, elementText(el))
	}
	el = s.assertTree(el)
	for _, a := range asserts {
		obj, err := a.test.Eval(&xpath.Context{
			Node: el, Pos: 1, Size: 1,
			NS:            xsdNS(a.ns),
			DefaultElemNS: a.defaultNS,
			Vars:          valueVar{value},
			RootlessTree:  true,
		})
		if err != nil {
			// ANY dynamic error makes the assertion NOT satisfied
			// (§3.13.4.1): the rootless-tree '/' error (d4_3_15ii31/ii32),
			// division by zero (assert012), an unavailable doc() (assert011
			// expects BOTH instances invalid).
			return invalidf("cvc-assertion", "assertion failed on %s", nameOf(el))
		}
		if !xpath.ToBool(obj) {
			return invalidf("cvc-assertion", "assertion failed on %s", nameOf(el))
		}
	}
	return nil
}

// typedValueOf builds the typed $value of a simple-typed lexical: an atomic,
// a sequence of typed items for a list, or the accepting member's reading for
// a union.
func typedValueOf(st *SimpleType, raw string) xpath.Object {
	v := applyWhiteSpace(st.effectiveWhiteSpace(), raw)
	switch st.variety {
	case vList:
		itemPrim := xpath.XSuntypedAtomic
		if st.item != nil {
			itemPrim = st.item.resolvePrim()
		}
		items := xmlFields(v)
		seq := make([]xpath.Item, 0, len(items))
		for _, it := range items {
			seq = append(seq, typedSimpleValue(it, itemPrim))
		}
		return xpath.FromItems(seq)
	case vUnion:
		if m := st.firstMemberRaw(v); m != nil {
			return typedValueOf(m, v)
		}
		return xpath.NewUntyped(v)
	default:
		return typedSimpleValue(v, st.resolvePrim())
	}
}

// selectAlternativeType applies conditional type assignment: it returns the type
// selected by the element declaration's alternatives (1.1 only), or nil to keep
// the declared type.
func (s *Schema) selectAlternativeType(decl *ElementDecl, el *xmltree.Node) (Type, error) {
	if s.version != Version11 || len(decl.alternatives) == 0 {
		return nil, nil
	}
	// CTA tests see the element's INHERITED attributes too (§3.4.4.1 —
	// cta0009/cta0014: @type declared inheritable on an ancestor); asserts do
	// not, so the merged view exists only here.
	if inh := s.inheritedAttrs(el); len(inh) > 0 {
		cp := *el
		cp.Attrs = append(append([]*xmltree.Node{}, el.Attrs...), inh...)
		el = &cp
	}
	for _, alt := range decl.alternatives {
		if alt.test == nil {
			return alt.typ, nil // default alternative
		}
		obj, err := alt.test.Eval(&xpath.Context{
			Node: el, Pos: 1, Size: 1,
			NS:            xsdNS(alt.ns),
			DefaultElemNS: alt.defaultNS,
		})
		if err != nil {
			// A dynamic error in a CTA test means the alternative is NOT
			// selected — same effect as a false test (cvc-cta / §3.4.4.1;
			// s3_12v07: xs:int("1.0") errors, the element falls through).
			continue
		}
		if xpath.ToBool(obj) {
			return alt.typ, nil
		}
	}
	return nil, nil
}
