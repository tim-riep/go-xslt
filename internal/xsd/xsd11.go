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
	if len(out) > 0 {
		c.sch.hasAsserts = true // see Schema.hasAsserts
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
	a.msgs = assertMessages(ch)
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
type valueVar struct {
	v xpath.Object
	// captured is set when a function item retained a context resolving
	// through this resolver (xpath.CaptureListener): the pair is then not
	// reused for the next element.
	captured bool
}

func (b *valueVar) ResolveVar(prefix, local string) (xpath.Object, bool) {
	if prefix == "" && local == "value" {
		return b.v, true
	}
	return nil, false
}

func (b *valueVar) ContextCaptured() { b.captured = true }

// noValue is the $value of an element-level assertion over complex content:
// the empty sequence (one shared boxed value, never appended to in place).
var noValue xpath.Object = xpath.Sequence{}

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
			Vars:          &valueVar{v: value},
			NoFocus:       true,
		})
		if err != nil || !xpath.ToBool(obj) {
			e := invalidf("cvc-assertions-valid", "assertion on the value failed")
			e.msgs = a.msgs
			return e
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
//
// The copy is used by the assertions of this one element and then dropped,
// so its nodes, pointer slices and node extensions come from one slab each
// (xmltree.TreeAlloc), sized by a first walk over the subtree, instead of an
// allocation per node — and the slabs are sc's, reset for every tree, so
// the next element's copy is carved from the same memory. The first walk
// also looks up each node's typed-XDM annotation and leaves it in sc.annos
// for the second, in the same order, so a node is looked up once.
func (s *Schema) assertTree(el *xmltree.Node, sc *validateScratch) *xmltree.Node {
	nodes, ptrs, exts := 0, 0, 0
	annos := sc.annos[:0]
	note := func(n *xmltree.Node) {
		a := s.assertAnnos[n]
		annos = append(annos, a)
		if a.prim != 0 {
			exts++
		}
	}
	var count func(n *xmltree.Node)
	count = func(n *xmltree.Node) {
		if n != el {
			note(n)
		}
		attrs := len(n.Attrs)
		for _, at := range n.Attrs {
			note(at)
		}
		if len(s.nodeDefaults) > 0 {
			d := len(s.nodeDefaults[n])
			attrs += d
			exts += d
		}
		nodes += 1 + attrs
		ptrs += attrs
		kids := 0
		for _, ch := range n.Children {
			if ch.Kind == xmltree.KindComment || ch.Kind == xmltree.KindPI {
				continue
			}
			kids++
			count(ch)
		}
		ptrs += kids
	}
	count(el)
	sc.annos = annos
	alloc := &sc.tree
	alloc.Reset(nodes, ptrs, exts)
	k := 0 // the next entry of annos: the walk below visits in count's order
	var clone func(n *xmltree.Node) *xmltree.Node
	clone = func(n *xmltree.Node) *xmltree.Node {
		cp := alloc.Node()
		*cp = *n
		cp.Parent = nil
		var own assertAnno
		if n != el {
			own = annos[k]
			k++
		}
		if own.prim == 0 {
			cp.CloneExt() // annotateAssertNode below gives an annotated clone its own
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
		var defaults []defaultAttr
		if len(s.nodeDefaults) > 0 {
			defaults = s.nodeDefaults[n]
		}
		// Attributes are VALUE-cloned so their typed-XDM annotation lives
		// only on the assertion tree (assert_002/018, d4_3_15v02/v11: a
		// validated @length atomizes as its declared type, not untyped).
		cp.Attrs = alloc.Ptrs(len(n.Attrs) + len(defaults))
		for _, at := range n.Attrs {
			ac := alloc.Node()
			*ac = *at
			ac.Parent = cp
			if annos[k].prim == 0 {
				ac.CloneExt()
			} else {
				s.annotateAssertNode(ac, at, annos[k], alloc)
			}
			k++
			cp.Attrs = append(cp.Attrs, ac)
		}
		for _, d := range defaults {
			if _, exists := n.Attr(d.name.Space, d.name.Local); exists {
				continue
			}
			ac := alloc.Node()
			ac.Kind = xmltree.KindAttribute
			ac.Name = xmltree.Name{Space: d.name.Space, Local: d.name.Local}
			ac.Value = d.value
			ac.Parent = cp
			alloc.Ext(ac)
			ac.SetTypeAnno(int32(typeAnnoForNode(d.typ, ac)))
			ac.SetListTyped(listTypedFor(d.typ))
			ac.SetListItemType(s.listItemTypeName(d.typ))
			cp.Attrs = append(cp.Attrs, ac)
		}
		cp.Children = nil
		// Typed-XDM view (§3.13.4.1): an already-validated simple-typed
		// DESCENDANT atomizes to its typed value (assert018 data(d),
		// assert022 data(event/d)); the assertion ROOT itself stays untyped —
		// its own type is exactly what is being validated (assert022's base
		// assert demands xs:untypedAtomic there). Only elements and
		// attributes are ever annotated (annotateAssertType), so a text
		// node's lookup is skipped.
		if n != el && n.Kind == xmltree.KindElement {
			s.annotateAssertNode(cp, n, own, alloc)
		}
		kids := 0
		for _, ch := range n.Children {
			if ch.Kind != xmltree.KindComment && ch.Kind != xmltree.KindPI {
				kids++
			}
		}
		if kids > 0 {
			cp.Children = alloc.Ptrs(kids)
			for _, ch := range n.Children {
				if ch.Kind == xmltree.KindComment || ch.Kind == xmltree.KindPI {
					continue
				}
				c2 := clone(ch)
				c2.Parent = cp
				cp.Children = append(cp.Children, c2)
			}
		}
		return cp
	}
	root := clone(el)
	// The tree is DETACHED, so namespace bindings declared on ANCESTORS of
	// the assertion root would be lost: materialize the root's in-scope
	// bindings (QName-typed content — assert024's @name — resolves against
	// them). When no ancestor declares any, the root's own declarations
	// already are its in-scope bindings and there is nothing to add.
	if ancestorsDeclareNS(el) {
		have := map[string]bool{}
		for _, ns := range root.NSDecls() {
			have[ns.Name.Local] = true
		}
		for pfx, uri := range el.InScopeNamespaces() {
			if !have[pfx] {
				root.AddNS(&xmltree.Node{Kind: xmltree.KindNamespace,
					Name: xmltree.Name{Local: pfx}, Value: uri, Parent: root})
			}
		}
	}
	return root
}

// ancestorsDeclareNS reports whether an ancestor of el within the scope
// Node.InScopeNamespaces walks (up to an element that inherits none)
// declares a namespace.
func ancestorsDeclareNS(el *xmltree.Node) bool {
	if el.NSBarrier() {
		return false
	}
	for p := el.Parent; p != nil; p = p.Parent {
		if len(p.NSDecls()) > 0 {
			return true
		}
		if p.NSBarrier() {
			break
		}
	}
	return false
}

// annotateAssertNode copies the typed-XDM annotation a recorded for src onto
// its assertion-tree clone dst, whose extension comes from alloc.
func (s *Schema) annotateAssertNode(dst, src *xmltree.Node, a assertAnno, alloc *xmltree.TreeAlloc) {
	if a.prim == 0 {
		return
	}
	alloc.Ext(dst)
	dst.SetTypeAnno(a.prim)
	if a.list {
		dst.SetListTyped(true)
		dst.SetListItemType(s.assertListItems[src])
	}
}

// checkAsserts evaluates a complex type's assertions against el (1.1 only).
func (s *Schema) checkAsserts(ct *ComplexType, el *xmltree.Node) error {
	if s.version != Version11 || !s.hasAsserts {
		return nil
	}
	asserts := s.childModelOf(ct).asserts
	if len(asserts) == 0 {
		return nil
	}
	// $value IS bound in element-level asserts (§3.13.4.1): the typed simple
	// content when the {content type} is simple (assert010/015; a list gives
	// a SEQUENCE — assert_035), and the EMPTY sequence otherwise (assert019
	// pins empty($value) valid on complex content).
	value := noValue
	if ct.kind == contentSimple && ct.simpleType != nil {
		value = typedValueOf(ct.simpleType, elementText(el))
	}
	// The per-call scratch space, unless this is a nested use of it (an
	// assertion's evaluation cannot validate, so none is expected) or there
	// is none: then a private one, as every call used to have.
	sc := s.scratch
	if sc == nil || sc.busy {
		sc = &validateScratch{}
	}
	sc.busy = true
	err := s.evalAsserts(asserts, el, value, sc)
	sc.busy = false
	return err
}

// evalAsserts runs asserts against the detached copy of el, with $value
// bound to value.
func (s *Schema) evalAsserts(asserts []*assertion, el *xmltree.Node, value xpath.Object, sc *validateScratch) error {
	orig := el
	el = s.assertTree(el, sc)
	for _, a := range asserts {
		// One context and one resolver serve every assertion of every
		// element, until a function item captures them (see
		// xpath.Context.Captured): from then on that pair is theirs.
		ctx, vars := sc.ctx, sc.vars
		if ctx == nil || ctx.Captured() || vars.captured {
			ctx, vars = &xpath.Context{}, &valueVar{}
			sc.ctx, sc.vars = ctx, vars
		}
		vars.v = value
		*ctx = xpath.Context{
			Node: el, Pos: 1, Size: 1,
			NS:            xsdNS(a.ns),
			DefaultElemNS: a.defaultNS,
			Vars:          vars,
			RootlessTree:  true,
		}
		obj, err := a.test.Eval(ctx)
		if err != nil {
			// ANY dynamic error makes the assertion NOT satisfied
			// (§3.13.4.1): the rootless-tree '/' error (d4_3_15ii31/ii32),
			// division by zero (assert012), an unavailable doc() (assert011
			// expects BOTH instances invalid).
			if cont, ret := s.failAssert(a, orig); !cont {
				return ret
			}
			continue
		}
		if !xpath.ToBool(obj) {
			if cont, ret := s.failAssert(a, orig); !cont {
				return ret
			}
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
	// The test sees E's attributes "but not its [children]": in the copy,
	// "The [children] property is modified to have the empty list as its
	// value" (Part 1 §3.12.4 clause 1.1.2/1.2), so test="a" is false even
	// when E has an <a> child. (The parent is kept: the element's in-scope
	// namespaces are resolved through it.)
	cp := *el
	cp.CloneExt()
	cp.Children = nil
	if inh := s.inheritedAttrs(el); len(inh) > 0 {
		cp.Attrs = append(append([]*xmltree.Node{}, el.Attrs...), inh...)
	}
	el = &cp
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
