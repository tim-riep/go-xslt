package xsd

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// enumAttrValues lists the legal values of the fixed-vocabulary XSD attributes
// checked at compile time.
var enumAttrValues = map[string][]string{
	"form":                 {"qualified", "unqualified"},
	"elementFormDefault":   {"qualified", "unqualified"},
	"attributeFormDefault": {"qualified", "unqualified"},
	"use":                  {"optional", "required", "prohibited"},
	"processContents":      {"strict", "lax", "skip"},
}

// boolAttrs are the boolean-valued XSD attributes.
var boolAttrs = map[string]bool{"abstract": true, "nillable": true, "mixed": true,
	"inheritable": true /* cta9006/9007err */}

// validateAttrValues walks every XSD-namespace element under el and rejects
// invalid boolean- and enumeration-valued attributes (e.g. abstract="False",
// form="foo"). It only inspects unprefixed attributes (the XSD attributes).
func validateAttrValues(el *xmltree.Node, seenIDs map[string]bool, ver Version) error {
	if el.Kind == xmltree.KindElement && el.Name.Space == xsNS {
		for _, a := range el.Attrs {
			if a.Name.Space == xsNS {
				// An attribute explicitly in the XSD namespace is never a legal
				// attribute of a schema element (its own attributes are unqualified;
				// only foreign-namespace attributes are permitted).
				return invalidf("", "attribute {xsd}%s is not allowed on xs:%s", a.Name.Local, el.Name.Local)
			}
			if a.Name.Space != "" {
				continue
			}
			v := applyWhiteSpace("collapse", a.Value)
			switch {
			case a.Name.Local == "id":
				if !isNCName(v) {
					return invalidf("", "invalid id value %q", a.Value)
				}
				if seenIDs[v] {
					return invalidf("", "duplicate id %q", v)
				}
				seenIDs[v] = true
			case boolAttrs[a.Name.Local]:
				switch v {
				case "true", "false", "1", "0":
				default:
					return invalidf("", "invalid boolean value %q for @%s", a.Value, a.Name.Local)
				}
			case a.Name.Local == "block" || a.Name.Local == "final" ||
				a.Name.Local == "blockDefault" || a.Name.Local == "finalDefault":
				if !validBlockFinal(el.Name.Local, a.Name.Local, v, ver) {
					return invalidf("", "invalid value %q for @%s", a.Value, a.Name.Local)
				}
			default:
				if allowed, ok := enumAttrValues[a.Name.Local]; ok && !contains(allowed, v) {
					return invalidf("", "invalid value %q for @%s", a.Value, a.Name.Local)
				}
			}
		}
	}
	for _, ch := range el.Children {
		if err := validateAttrValues(ch, seenIDs, ver); err != nil {
			return err
		}
	}
	return nil
}

// validBlockFinal checks a block/final value: "#all", or a space-separated list
// of derivation-control keywords drawn from the vocabulary allowed for that
// element/attribute context (XSD Part 1, §3.x XML representation tables):
//
//	element     @block  → extension | restriction | substitution
//	element     @final  → extension | restriction
//	complexType @block/@final → extension | restriction
//	simpleType  @final  → restriction | list | union
//	schema @blockDefault  → extension | restriction | substitution
//	schema @finalDefault  → extension | restriction | list | union
func validBlockFinal(elem, attr, v string, ver Version) bool {
	if v == "#all" {
		return true
	}
	var allowed map[string]bool
	switch elem {
	case "element":
		if attr == "block" {
			allowed = blockFinalSet("extension", "restriction", "substitution")
		} else {
			allowed = blockFinalSet("extension", "restriction")
		}
	case "complexType":
		allowed = blockFinalSet("extension", "restriction")
	case "simpleType":
		if ver == Version10 {
			allowed = blockFinalSet("restriction", "list", "union")
		} else {
			// XSD 1.1 widened simpleDerivationSet to include extension (bug 2074).
			allowed = blockFinalSet("restriction", "list", "union", "extension")
		}
	case "schema":
		if attr == "blockDefault" {
			allowed = blockFinalSet("extension", "restriction", "substitution")
		} else {
			allowed = blockFinalSet("extension", "restriction", "list", "union")
		}
	default:
		allowed = blockFinalSet("extension", "restriction", "substitution", "list", "union")
	}
	for _, tok := range strings.Fields(v) {
		if !allowed[tok] {
			return false
		}
	}
	return true
}

func blockFinalSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// checkElementConsistency enforces "Element Declarations Consistent": two
// element particles with the same expanded name in one content model must have
// the same type. It walks the particle tree (following group references, with a
// cycle guard).
func checkElementConsistency(p *particle) error {
	sigs := map[xname]string{}
	seen := map[*modelGroup]bool{}
	var walk func(pp *particle) error
	walk = func(pp *particle) error {
		if pp == nil {
			return nil
		}
		switch t := pp.term.(type) {
		case *ElementDecl:
			s := typeSignature(t.typ)
			if prev, ok := sigs[t.name]; ok && prev != s {
				return invalidf("", "inconsistent element declarations for %s in one content model", t.name)
			}
			sigs[t.name] = s
		case *modelGroup:
			if seen[t] {
				return nil
			}
			seen[t] = true
			for _, sub := range t.particles {
				if err := walk(sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(p)
}

// typeSignature yields a comparison key for a type that treats two references to
// the same built-in or named type as equal (avoiding false EDC violations from
// distinct built-in wrapper instances).
// checkElementConsistency11 is the XSD 1.1 half of cos-element-consistent.
func (c *compiler) checkElementConsistency11(p *particle) error {
	type entry struct {
		sig  string
		alts int
		decl *ElementDecl
	}
	sigs := map[xname]entry{}
	var wcs []*wildcard
	seen := map[*modelGroup]bool{}
	var collect func(pp *particle) error
	collect = func(pp *particle) error {
		if pp == nil {
			return nil
		}
		switch t := pp.term.(type) {
		case *ElementDecl:
			add := func(n xname, sig string, alts int, d *ElementDecl) error {
				if prev, ok := sigs[n]; ok && (prev.sig != sig || prev.alts != alts) {
					return invalidf("cos-element-consistent",
						"inconsistent element declarations for %s in one content model", n)
				}
				sigs[n] = entry{sig, alts, d}
				return nil
			}
			if err := add(t.name, typeSignature(t.typ), len(t.alternatives), t); err != nil {
				return err
			}
			// Substitution members participate — ABSTRACT ones included
			// (§3.8.6.3 with bug 4337; the M25 instance-side exclusion of
			// abstract members stays untouched).
			if c.sch.elements[t.name] == t {
				seenM := map[xname]bool{}
				var walk func(h xname) error
				walk = func(h xname) error {
					for _, m := range c.sch.substMembers[h] {
						if seenM[m.name] {
							continue
						}
						seenM[m.name] = true
						if err := add(m.name, typeSignature(m.typ), len(m.alternatives), m); err != nil {
							return err
						}
						if err := walk(m.name); err != nil {
							return err
						}
					}
					return nil
				}
				if err := walk(t.name); err != nil {
					return err
				}
			}
		case *wildcard:
			wcs = append(wcs, t)
		case *modelGroup:
			if seen[t] {
				return nil
			}
			seen[t] = true
			for _, sub := range t.particles {
				if err := collect(sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(p); err != nil {
		return err
	}
	// Wildcard-vs-local clause, {TYPE TABLE} reading only (the M29 blunt
	// type-agreement attempt broke 38 valid cases — wild061/062 and all006
	// prove declared TYPES may differ freely): a non-skip wildcard admitting a
	// local declaration's name lets an instance element be attributed either
	// way, so conditional type assignment must not diverge — the local and the
	// GLOBAL declaration of that name need equivalent type tables
	// (wild078/079/081 invalid; wild080 skip-exempt; wild082 equal tables).
	for _, w := range wcs {
		if w.process == "skip" {
			continue
		}
		for n, e := range sigs {
			if !wildcardMatches(w, n) {
				continue
			}
			g, ok := c.sch.elements[n]
			if !ok || g == e.decl {
				continue
			}
			if !typeTablesEquivalent(e.decl, g) {
				return invalidf("cos-element-consistent",
					"element %s: type table differs from the global declaration a wildcard match would be assessed against", n)
			}
		}
	}
	return nil
}

// typeTablesEquivalent reports whether two element declarations carry
// equivalent {type table}s (§3.3.2.1): same alternative count, pairwise
// identical @test text and alternative type (the trailing default alternative
// compares as rawTest ""). Declarations WITHOUT alternatives are trivially
// equivalent — the declared {type definition} is not part of the type table.
func typeTablesEquivalent(a, b *ElementDecl) bool {
	if len(a.alternatives) != len(b.alternatives) {
		return false
	}
	for i, x := range a.alternatives {
		y := b.alternatives[i]
		if x.rawTest != y.rawTest || typeSignature(x.typ) != typeSignature(y.typ) {
			return false
		}
	}
	return true
}

func typeSignature(t Type) string {
	switch v := t.(type) {
	case *SimpleType:
		if v.baseBuiltin && v.name.zero() && !v.facets.any() {
			return "b:" + v.prim.String()
		}
		if !v.name.zero() {
			return "s:" + v.name.String()
		}
		return fmt.Sprintf("sa:%p", v)
	case *ComplexType:
		if !v.name.zero() {
			return "c:" + v.name.String()
		}
		return fmt.Sprintf("ca:%p", v)
	}
	return "?"
}

// checkComplexTypeStructure enforces the element grammar of xs:complexType: at
// most one leading annotation, and exactly one content model (simpleContent XOR
// complexContent XOR a shorthand model group), not several or mixed.
func checkComplexTypeStructure(node *xmltree.Node) error {
	var nAnnot, nSimple, nComplex, nGroup, nAttr int
	sawContent := false
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "annotation":
			nAnnot++
			if sawContent {
				return invalidf("", "xs:annotation must be the first child of xs:complexType")
			}
		case "simpleContent":
			nSimple++
			sawContent = true
		case "complexContent":
			nComplex++
			sawContent = true
		case "sequence", "choice", "all", "group":
			nGroup++
			sawContent = true
		case "attribute", "attributeGroup", "anyAttribute":
			nAttr++
			sawContent = true
		case "element", "any":
			return invalidf("", "particle xs:%s cannot appear directly in xs:complexType", ch.Name.Local)
		default:
			sawContent = true
		}
	}
	if nSimple+nComplex > 0 && nAttr > 0 {
		return invalidf("", "attributes must be inside the extension/restriction when simple/complexContent is used")
	}
	if nAnnot > 1 {
		return invalidf("", "xs:complexType has more than one annotation")
	}
	if nSimple+nComplex > 1 {
		return invalidf("", "xs:complexType has more than one content model")
	}
	if nSimple+nComplex > 0 && nGroup > 0 {
		return invalidf("", "xs:complexType mixes simple/complexContent with a model group")
	}
	if nGroup > 1 {
		return invalidf("", "xs:complexType has more than one model group")
	}
	return nil
}

// checkAttrGroupCycles rejects a schema whose named attribute groups reference
// one another (or themselves) cyclically — an attributeGroup ref must resolve to
// a group that does not, directly or transitively, reference back. XSD 1.1
// dropped this constraint (src-attribute_group.3 has no 1.1 counterpart): a
// cycle simply contributes nothing new to the transitive {attribute uses}, and
// the dependency-ordered flattening pass breaks cycles safely.
func checkAttrGroupCycles(docs []*schemaDoc, ver Version) error {
	if ver != Version10 {
		return nil
	}
	graph := map[xname][]xname{}
	for _, sd := range docs {
		for _, ch := range sd.el.Children {
			if !isXS(ch, "attributeGroup") {
				continue
			}
			name, ok := ch.AttrLocal("name")
			name = applyWhiteSpace("collapse", name)
			if !ok {
				continue
			}
			self := xname{sd.tns, name}
			for _, sub := range ch.Children {
				if isXS(sub, "attributeGroup") {
					if ref, ok := sub.AttrLocal("ref"); ok {
						graph[self] = append(graph[self], resolveQName(sub, ref))
					}
				}
			}
		}
	}
	color := map[xname]int{} // 0 unvisited, 1 on-stack, 2 done
	var dfs func(n xname) bool
	dfs = func(n xname) bool {
		color[n] = 1
		for _, m := range graph[n] {
			if color[m] == 1 || (color[m] == 0 && dfs(m)) {
				return true
			}
		}
		color[n] = 2
		return false
	}
	for n := range graph {
		if color[n] == 0 && dfs(n) {
			return invalidf("", "circular attributeGroup reference")
		}
	}
	return nil
}

// checkAttrDerivation enforces two attribute constraints on a complex type
// derived by restriction: an attribute carried over from the base must keep the
// base's fixed value, and a required base attribute may not be made optional or
// removed.
func (c *compiler) checkAttrDerivation() error {
	for _, t := range c.sch.types {
		ct, ok := t.(*ComplexType)
		if !ok || ct.derivation != "restriction" {
			continue
		}
		base, ok := ct.baseType.(*ComplexType)
		if !ok {
			continue
		}
		baseUses, _ := c.sch.effectiveAttrs(base)
		byName := make(map[xname]*attrUse, len(baseUses))
		for _, u := range baseUses {
			byName[u.decl.name] = u
		}
		for _, du := range ct.attrUses {
			bu, ok := byName[du.decl.name]
			if !ok {
				continue
			}
			if bu.hasFixed && (!du.hasFixed ||
				applyWhiteSpace("collapse", du.fixed) != applyWhiteSpace("collapse", bu.fixed)) {
				return invalidf("", "attribute %s must keep the base fixed value", du.decl.name.Local)
			}
			if bu.required && !du.required {
				return invalidf("", "required attribute %s cannot be relaxed by restriction", du.decl.name.Local)
			}
		}
	}
	return nil
}

// checkFinalDerivation enforces that a complex type does not derive (by
// extension or restriction) from a base whose {final} forbids that method
// (cos-ct-derived-ok / the @final and finalDefault control).
func (c *compiler) checkFinalDerivation() error {
	for _, t := range c.sch.types {
		ct, ok := t.(*ComplexType)
		if !ok || ct.derivation == "" {
			continue
		}
		if base, ok := ct.baseType.(*ComplexType); ok && base.final[ct.derivation] {
			return invalidf("", "derivation by %s from a final base type is not allowed", ct.derivation)
		}
	}
	return nil
}

// parseComplexTypeInto fills ct from an <xs:complexType> node.
func (c *compiler) parseComplexTypeInto(ct *ComplexType, node *xmltree.Node) error {
	if err := checkComplexTypeStructure(node); err != nil {
		return err
	}
	c.allCTs = append(c.allCTs, ct) // named and anonymous alike (whole-schema checks)
	ct.abstract = attrIs(node, "abstract", "true")
	if f, ok := node.AttrLocal("final"); ok {
		ct.final = parseBlockSet(f)
	} else if c.finalDefault != "" {
		ct.final = parseBlockSet(c.finalDefault)
	}
	if b, ok := node.AttrLocal("block"); ok {
		ct.block = parseBlockSet(b)
	} else if c.blockDefault != "" {
		ct.block = parseBlockSet(c.blockDefault)
	}
	mixed := boolAttrTrue(node, "mixed")
	if sc := firstXSChild(node, "simpleContent"); sc != nil {
		if err := c.parseSimpleContent(ct, sc); err != nil {
			return err
		}
		return c.applyDefaultAttrs(ct, node)
	}
	if cc := firstXSChild(node, "complexContent"); cc != nil {
		if err := c.parseComplexContent(ct, cc, mixed); err != nil {
			return err
		}
		return c.applyDefaultAttrs(ct, node)
	}
	// Shorthand: direct model group + attributes (an implicit restriction of
	// xs:anyType).
	p, err := c.parseContentModel(node)
	if err != nil {
		return err
	}
	ct.particle = p
	if err := checkElementConsistency(ct.particle); err != nil {
		return err
	}
	uses, wc, err := c.parseAttributes(node)
	if err != nil {
		return err
	}
	ct.attrUses, ct.attrWildcard = uses, wc
	if ct.asserts, err = c.parseAsserts(node); err != nil {
		return err
	}
	switch {
	case mixed:
		ct.kind = contentMixed
	case p != nil:
		ct.kind = contentElementOnly
	default:
		ct.kind = contentEmpty
	}
	if err := c.applyOpenContent(ct, node); err != nil {
		return err
	}
	return c.applyDefaultAttrs(ct, node)
}

// boolAttrTrue reads an xs:boolean attribute with whitespace collapse
// (mixed=" 1 " is true — open013).
func boolAttrTrue(node *xmltree.Node, name string) bool {
	v, ok := node.AttrLocal(name)
	if !ok {
		return false
	}
	switch applyWhiteSpace("collapse", v) {
	case "true", "1":
		return true
	}
	return false
}

// parseOpenContent reads an xs:openContent / xs:defaultOpenContent element
// into its component form (mode "none" records an explicit opt-out).
func (c *compiler) parseOpenContent(node *xmltree.Node) (*openContent, error) {
	mode := "interleave"
	if mv, ok := node.AttrLocal("mode"); ok {
		mode = applyWhiteSpace("collapse", mv)
	}
	if mode == "none" {
		return &openContent{mode: "none"}, nil
	}
	anyEl := firstXSChild(node, "any")
	if anyEl == nil {
		return nil, invalidf("", "xs:openContent with mode %q requires an xs:any child", mode)
	}
	wc, err := c.parseWildcard(anyEl)
	if err != nil {
		return nil, err
	}
	if wc.process == "" {
		wc.process = "lax"
	}
	return &openContent{mode: mode, wc: wc}, nil
}

// applyOpenContent sets a complex type's {open content} (1.1): an explicit
// xs:openContent in one of the bodies wins; otherwise the document's
// xs:defaultOpenContent applies — never to simple content, and to an EMPTY
// effective content type only with appliesToEmpty="true" (§3.4.2.5/§4.2.2).
func (c *compiler) applyOpenContent(ct *ComplexType, bodies ...*xmltree.Node) error {
	if c.sch.version != Version11 {
		return nil
	}
	for _, b := range bodies {
		if b == nil {
			continue
		}
		if ocEl := firstXSChild(b, "openContent"); ocEl != nil {
			oc, err := c.parseOpenContent(ocEl)
			if err != nil {
				return err
			}
			ct.openContent = oc
			return nil
		}
	}
	sd := c.curDoc
	if sd == nil || sd.defaultOpenNode == nil || ct.kind == contentSimple {
		return nil
	}
	// An EMPTY content type (no particle content, not mixed — an empty
	// <sequence/> counts, open012) takes the default only with
	// appliesToEmpty="true"; mixed-with-empty-particle is NOT empty (open013).
	if ct.kind != contentMixed && isEmptyParticle(c.sch.effectiveParticle(ct)) &&
		!boolAttrTrue(sd.defaultOpenNode, "appliesToEmpty") {
		return nil
	}
	oc, err := c.parseOpenContent(sd.defaultOpenNode)
	if err != nil {
		return err
	}
	ct.openContent = oc
	return nil
}

// applyDefaultAttrs merges the document-wide xs:schema/@defaultAttributes
// group into a complex type unless the type opts out with
// defaultAttributesApply="false" (§3.4.2.1 — open035 opts out on the outer
// type only; scope is per DOCUMENT, so open044x/open045x stay untouched).
func (c *compiler) applyDefaultAttrs(ct *ComplexType, node *xmltree.Node) error {
	sd := c.curDoc
	if c.sch.version != Version11 || sd == nil || !sd.hasDefaultAttrs ||
		attrIs(node, "defaultAttributesApply", "false") {
		return nil
	}
	ag, ok := c.attrGroups[sd.defaultAttrs]
	if !ok {
		return invalidf("src-resolve", "defaultAttributes group %s does not resolve", sd.defaultAttrs)
	}
	seen := map[xname]bool{}
	for _, u := range ct.attrUses {
		seen[u.decl.name] = true
	}
	for _, u := range ag.uses {
		if seen[u.decl.name] {
			// ct-props-correct.4: the merged {attribute uses} may not contain
			// the same attribute twice (s3_4_2_4si02/si03 — a use the type
			// already carries collides with the document default).
			return invalidf("ct-props-correct.4",
				"attribute %s appears both on the type and in the defaultAttributes group", u.decl.name)
		}
		ct.attrUses = append(ct.attrUses, u)
	}
	if ag.wildcard != nil {
		ct.attrWildcard = intersectWildcards(ct.attrWildcard, ag.wildcard)
	}
	return nil
}

func (c *compiler) parseSimpleContent(ct *ComplexType, sc *xmltree.Node) error {
	ct.kind = contentSimple
	ext := firstXSChild(sc, "extension")
	res := firstXSChild(sc, "restriction")
	body := ext
	ct.derivation = "extension"
	if body == nil {
		body, ct.derivation = res, "restriction"
	}
	if body == nil {
		return invalidf("", "simpleContent without extension/restriction")
	}
	if res != nil {
		// The simpleContent restriction content model orders facets strictly before
		// attribute uses: (simpleType?, facet*, (attribute|attributeGroup)*,
		// anyAttribute?, assert*). A facet after an attribute violates it.
		if err := checkFacetsBeforeAttrs(body); err != nil {
			return err
		}
	}
	baseRef, _ := body.AttrLocal("base")
	base, err := c.resolveType(body, baseRef)
	if err != nil {
		return err
	}
	// A simpleContent RESTRICTION starting from ur-type content has no value
	// space to restrict: xs:anySimpleType lacks a {variety}, so cos-st-restricts
	// cannot be satisfied. 1.1 rejects it outright (stZ007/047/055); 1.0 only
	// when the restriction actually constrains it with facets (stZ010).
	if res != nil && firstXSChild(body, "simpleType") == nil {
		// An inline <xs:simpleType> child supplies the content type itself and
		// legitimizes an ur-type base (s3_12v04: restriction of an
		// anySimpleType-content type down to an inline float).
		if bst := simpleContentType(base); isURTypeSimple(bst) &&
			(c.sch.version == Version11 || hasFacetChildren(body)) {
			return invalidf("src-ct.2", "a simpleContent restriction cannot start from xs:anySimpleType content")
		}
	}
	// cos-ct-extends 1.5: a simple base type whose {final} contains extension
	// (reachable via @finalDefault in 1.0, and via @final too in 1.1) bars a
	// simpleContent extension.
	if bst, ok := base.(*SimpleType); ok && ct.derivation == "extension" {
		if err := checkSimpleFinal(bst, "extension"); err != nil {
			return err
		}
	}
	ct.baseType = base
	// Effective simple type: an inline restriction simpleType, else the base's.
	if inline := firstXSChild(body, "simpleType"); inline != nil {
		st := &SimpleType{}
		if err := c.parseSimpleTypeInto(st, inline); err != nil {
			return err
		}
		// derivation-ok-restriction 5.1.1: the inline type must be validly
		// derived from the base's content type. Provably impossible: a list or
		// union standing where an atomic content type is required
		// (particlesZ018: a list of xs:int restricting xs:decimal content).
		if bst := simpleContentType(base); bst != nil && ct.derivation == "restriction" &&
			(st.variety == vList || st.variety == vUnion) &&
			bst.variety == vAtomic && bst.resolvePrim() != xpath.XSanyAtomicType {
			return invalidf("derivation-ok-restriction.5.1.1",
				"the inline simple type is not derived from the base's content type")
		}
		ct.simpleType = st
	} else if st := simpleContentType(base); st != nil {
		// A restriction may narrow the base's simple type with facets.
		if res != nil && hasFacetChildren(body) {
			nst := &SimpleType{base: st}
			if err := c.parseFacets(nst, body); err != nil {
				return err
			}
			ct.simpleType = nst
		} else {
			ct.simpleType = st
		}
	} else {
		ct.simpleType = anySimpleType()
	}
	uses, wc, err := c.parseAttributes(body)
	if err != nil {
		return err
	}
	ct.attrUses, ct.attrWildcard = uses, wc
	if ct.asserts, err = c.parseAsserts(body); err != nil {
		return err
	}
	return nil
}

// checkFacetsBeforeAttrs enforces the child order of a simpleContent restriction:
// all constraining facets must precede any attribute use (cos-particle / the
// schema-for-schemas content model). A facet appearing after an attribute,
// attributeGroup or anyAttribute makes the schema invalid.
func checkFacetsBeforeAttrs(body *xmltree.Node) error {
	seenAttr := false
	for _, ch := range body.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "attribute", "attributeGroup", "anyAttribute":
			seenAttr = true
		case "annotation", "assert":
			// order-neutral here
		case "simpleType":
			// the inline simple type precedes the attribute declarations too
			// (annotation?, simpleType?, facets*, attributes* — ctD041)
			if seenAttr {
				return invalidf("", "xs:simpleType must precede attribute declarations")
			}
		default: // a constraining facet
			if seenAttr {
				return invalidf("", "facet xs:%s must precede attribute declarations", ch.Name.Local)
			}
		}
	}
	return nil
}

func (c *compiler) parseComplexContent(ct *ComplexType, cc *xmltree.Node, mixed bool) error {
	// src-ct.1 (1.1): @mixed on complexType and complexContent must not
	// conflict when both are present (s3_4_1si09s). 1.0 keeps the lenient
	// OR-combination below untouched.
	if c.sch.version == Version11 {
		if ccm, ok := cc.AttrLocal("mixed"); ok {
			if ctm, ok2 := cc.Parent.AttrLocal("mixed"); ok2 {
				b := func(v string) bool { v = applyWhiteSpace("collapse", v); return v == "true" || v == "1" }
				if b(ccm) != b(ctm) {
					return invalidf("src-ct.1", "conflicting @mixed on complexType and complexContent")
				}
			}
		}
	}
	if boolAttrTrue(cc, "mixed") {
		mixed = true
	}
	ext := firstXSChild(cc, "extension")
	res := firstXSChild(cc, "restriction")
	body := ext
	ct.derivation = "extension"
	if body == nil {
		body, ct.derivation = res, "restriction"
	}
	if body == nil {
		return invalidf("", "complexContent without extension/restriction")
	}
	baseRef, _ := body.AttrLocal("base")
	base, err := c.resolveType(body, baseRef)
	if err != nil {
		return err
	}
	ct.baseType = base
	p, err := c.parseContentModel(body)
	if err != nil {
		return err
	}
	ct.particle = p
	if err := checkElementConsistency(ct.particle); err != nil {
		return err
	}
	uses, wc, err := c.parseAttributes(body)
	if err != nil {
		return err
	}
	ct.attrUses, ct.attrWildcard = uses, wc
	if ct.asserts, err = c.parseAsserts(body); err != nil {
		return err
	}
	if mixed {
		ct.kind = contentMixed
	} else {
		ct.kind = contentElementOnly
	}
	// XSD §3.4.2 {content type} clause 2.3.1: "if the effective content is
	// EMPTY, then the {content type} of the type definition resolved to by the
	// actual value of the base attribute". An extension that adds only
	// ATTRIBUTES therefore inherits its base's content type verbatim — mixed
	// stays mixed (validation-0801/1002: t:p extends the mixed t:inline with
	// one attribute and must still admit character data). cos-ct-extends below
	// already assumes exactly this ("An empty extension body copies the base
	// content type verbatim"); this is the parse-side half of it.
	// Narrowed to a MIXED base: that is the only direction this has to cover
	// (an element-only or empty base already agrees with the default computed
	// above), and widening it to copy the base's kind wholesale costs
	// particlesZ031, whose own verdict depends on an empty extension of
	// SIMPLE content staying distinguishable.
	if ct.derivation == "extension" && ct.particle == nil && !mixed {
		if bc, ok := base.(*ComplexType); ok && bc != nil && bc.kind == contentMixed {
			ct.kind = contentMixed
		}
	}
	return c.applyOpenContent(ct, body)
}

// parseContentModel finds the model-group / group-ref child (if any) and returns
// its particle.
func (c *compiler) parseContentModel(node *xmltree.Node) (*particle, error) {
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "sequence", "choice", "all", "group":
			return c.parseParticle(ch)
		}
	}
	return nil, nil
}

// parseParticle parses one particle (a model group, group ref, element, or
// wildcard) with its occurrence bounds.
func (c *compiler) parseParticle(ch *xmltree.Node) (*particle, error) {
	min, max := occursOf(ch)
	switch ch.Name.Local {
	case "sequence", "choice", "all":
		mg, err := c.parseModelGroup(ch)
		if err != nil {
			return nil, err
		}
		return &particle{min: min, max: max, term: mg}, nil
	case "group":
		ref, ok := ch.AttrLocal("ref")
		if !ok {
			return nil, invalidf("", "group without a ref")
		}
		gn := resolveQName(ch, ref)
		g, ok := c.groups[gn]
		if ok && !c.reachableNS(gn.Space) {
			return nil, invalidf("src-resolve", "group %s is in a namespace this schema does not import", gn)
		}
		if orig, isRedef := c.redefineGroup[gn]; isRedef && withinRedefine(ch) {
			g, ok = orig, true // reference to a redefined group from inside the redefine
		}
		if !ok {
			// Chameleon repair: an unqualified ref inside an adopted
			// no-namespace document also denotes the adopting namespace's.
			if alt, aok := c.chamAlt(gn); aok {
				if g2, ok2 := c.groups[alt]; ok2 {
					g, ok = g2, true
				}
			}
		}
		if !ok {
			return nil, invalidf("", "unresolved group ref %s", ref)
		}
		return &particle{min: min, max: max, term: g}, nil
	case "element":
		ed, err := c.parseLocalElement(ch)
		if err != nil {
			return nil, err
		}
		return &particle{min: min, max: max, term: ed}, nil
	case "any":
		w, err := c.parseWildcard(ch)
		if err != nil {
			return nil, err
		}
		return &particle{min: min, max: max, term: w}, nil
	}
	return nil, invalidf("", "unexpected particle xs:%s", ch.Name.Local)
}

func (c *compiler) parseModelGroup(node *xmltree.Node) (*modelGroup, error) {
	mg := &modelGroup{}
	switch node.Name.Local {
	case "choice":
		mg.compositor = cChoice
	case "all":
		mg.compositor = cAll
	default:
		mg.compositor = cSeq
	}
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "element", "group", "sequence", "choice", "any":
			p, err := c.parseParticle(ch)
			if err != nil {
				return nil, err
			}
			mg.particles = append(mg.particles, p)
		case "annotation":
		}
	}
	return mg, nil
}

// checkDuplicateConstraints rejects two identity constraints (key/keyref/unique)
// sharing a name — they must be unique across the schema. It walks all element
// declarations (global and, via their types' content models, local), guarded
// against cycles.
func (c *compiler) checkDuplicateConstraints() error {
	byName := map[xname]string{} // name → kind
	var keyrefs []*identityConstraint
	visitedDecl := map[*ElementDecl]bool{}
	visitedGroup := map[*modelGroup]bool{}
	var addDecl func(e *ElementDecl) error
	var walkP func(p *particle) error
	addDecl = func(e *ElementDecl) error {
		if e == nil || visitedDecl[e] {
			return nil
		}
		visitedDecl[e] = true
		for _, ic := range e.constraints {
			if ic.refPending {
				continue // a 1.1 @ref use is not a re-declaration
			}
			if _, dup := byName[ic.name]; dup {
				return invalidf("", "duplicate identity constraint %s", ic.name)
			}
			byName[ic.name] = ic.kind
			if ic.kind == "keyref" {
				keyrefs = append(keyrefs, ic)
			}
		}
		if ct, ok := e.typ.(*ComplexType); ok {
			return walkP(ct.particle)
		}
		return nil
	}
	walkP = func(p *particle) error {
		if p == nil {
			return nil
		}
		switch t := p.term.(type) {
		case *ElementDecl:
			return addDecl(t)
		case *modelGroup:
			if visitedGroup[t] {
				return nil
			}
			visitedGroup[t] = true
			for _, sub := range t.particles {
				if err := walkP(sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, e := range c.sch.elements {
		if err := addDecl(e); err != nil {
			return err
		}
	}
	// A keyref's @refer must name a key or unique. Only flag the clear case where
	// it resolves to another keyref — a "not found" refer is left alone, since our
	// constraint walk may not reach keys on unused/deeply-nested declarations
	// (avoiding a false rejection of a valid schema).
	for _, kr := range keyrefs {
		if kind, ok := byName[kr.refer]; ok && kind == "keyref" {
			return invalidf("", "keyref %s refers to %s which is itself a keyref", kr.name, kr.refer)
		}
	}
	return nil
}

// checkAllRestrictions rejects complexContent restrictions that introduce an
// element name absent from the base content model (restriction may only remove
// or narrow, never add). It is deliberately conservative: skipped when the base
// has a wildcard or any base element heads a substitution group, since either can
// legitimately admit the "new" name.
// checkContentBaseKind enforces src-ct.2: what a derivation may take as its
// base. A simpleContent restriction must start from a complex type that itself
// has simple content; a simpleContent extension may also start from a simple
// type (that is how a simple type acquires attributes). Either way xs:anyType,
// whose content is mixed, qualifies as neither. Run as a post-pass, because at
// parse time a forward-referenced base has not been filled in yet.
func (c *compiler) checkContentBaseKind(ct *ComplexType) error {
	if ct.baseType == nil {
		return nil
	}
	if ct.kind != contentSimple {
		// src-ct.1: a complexContent derivation must start from a complex type.
		if _, isSimple := ct.baseType.(*SimpleType); isSimple && ct.derivation != "" {
			return invalidf("src-ct.1", "a complexContent derivation requires a complex-type base")
		}
		// derivation-ok-restriction 5.2/5.3: a complexContent RESTRICTION of a
		// base whose content type is simple has no particle to restrict — an
		// empty or element-only derived content type cannot come from it
		// (particlesZ039).
		if b, ok := ct.baseType.(*ComplexType); ok && ct.derivation == "restriction" &&
			b.kind == contentSimple {
			return invalidf("derivation-ok-restriction.5.2",
				"a complexContent restriction cannot derive from the simple-content type %s", b.name)
		}
		// cos-ct-extends 1.4.2.2: a complexContent EXTENSION of a simple-content
		// base may only add attributes — an extension particle with element
		// content has nowhere to concatenate (addB033/036; particlesZ031 adds
		// only attributes and stays valid).
		if b, ok := ct.baseType.(*ComplexType); ok && ct.derivation == "extension" &&
			b.kind == contentSimple &&
			(!isEmptyParticle(ct.particle) || c.sch.version == Version11) {
			// 1.1 cos-ct-extends 1.4.2: the derived {content type} must stay the
			// base's simple one, which a complexContent extension cannot express
			// even with an empty particle (particlesZ031); 1.0 tolerates the
			// attribute-only form.
			return invalidf("cos-ct-extends.1.4.2.2",
				"an extension of the simple-content type %s cannot add element content", b.name)
		}
		// cos-ct-extends.1.4.3.2.2.1: an EXTENSION may not change mixedness — the
		// base's content model is concatenated with the extension's, and the two
		// halves cannot disagree about whether character data is allowed. Only
		// checked when both sides actually carry a particle.
		if b, ok := ct.baseType.(*ComplexType); ok && ct.derivation == "extension" &&
			b.name != (xname{xsNS, "anyType"}) &&
			!isEmptyParticle(c.sch.effectiveParticle(b)) {
			derivedMixed, baseMixed := ct.kind == contentMixed, b.kind == contentMixed
			if !isEmptyParticle(ct.particle) && derivedMixed != baseMixed {
				return invalidf("cos-ct-extends.1.4.3.2.2.1", "an extension may not change the mixedness of %s", b.name)
			}
			// An empty extension body copies the base content type verbatim —
			// dropping mixedness that way is fine (ctZ012b), but DECLARING
			// mixed over an element-only base is not (ctF008, complex025/026).
			if isEmptyParticle(ct.particle) && derivedMixed && !baseMixed {
				return invalidf("cos-ct-extends.1.4.3.2.2.1", "an extension may not change the mixedness of %s", b.name)
			}
		}
		// derivation-ok-restriction 5.4.1.2, one direction only: a MIXED
		// restriction requires a mixed base (element-only restricting mixed is
		// legal — that variant was measured and stays out). ctF006, ctZ010e.
		if b, ok := ct.baseType.(*ComplexType); ok && ct.derivation == "restriction" &&
			b.name != (xname{xsNS, "anyType"}) &&
			ct.kind == contentMixed && b.kind != contentMixed {
			return invalidf("derivation-ok-restriction.5.4.1.2", "a mixed restriction requires a mixed base (%s)", b.name)
		}
		return nil
	}
	switch b := ct.baseType.(type) {
	case *ComplexType:
		if b.kind == contentSimple {
			return nil
		}
		// src-ct.2.1.2: a RESTRICTION may also start from a mixed complex type
		// whose particle is emptiable — restricting it away is exactly how such a
		// type becomes simple-content. xs:anyType is mixed but never emptiable
		// down to nothing in this sense, so it stays excluded.
		if ct.derivation == "restriction" && b.kind == contentMixed &&
			b.name != (xname{xsNS, "anyType"}) &&
			particleEmptiable(c.sch.effectiveParticle(b), 0) {
			return nil
		}
		return invalidf("src-ct.2.1", "simpleContent base %s does not have simple content", b.name)
	case *SimpleType:
		if ct.derivation == "restriction" {
			return invalidf("src-ct.2.1", "a simpleContent restriction requires a complex-type base")
		}
	}
	return nil
}

// particleEmptiable reports whether a particle can match the empty sequence.
func particleEmptiable(p *particle, depth int) bool {
	if p == nil || depth > 32 {
		return true
	}
	if p.min == 0 {
		return true
	}
	mg, ok := p.term.(*modelGroup)
	if !ok {
		return false
	}
	if len(mg.particles) == 0 {
		return true
	}
	if mg.compositor == cChoice {
		for _, sub := range mg.particles {
			if particleEmptiable(sub, depth+1) {
				return true
			}
		}
		return false
	}
	for _, sub := range mg.particles {
		if !particleEmptiable(sub, depth+1) {
			return false
		}
	}
	return true
}

// checkAllLimited enforces the extension half of cos-all-limited: an extension
// concatenates the base's content model with the extension's, so an xs:all on
// either side of a non-empty extension would end up nested inside that
// sequence. XSD 1.0 rejects any such xs:all. XSD 1.1 permits exactly the
// all-extends-all case (effParticle merges the two groups into one), which
// requires the two xs:all particles to carry the SAME minOccurs (all313);
// combining xs:all with another compositor stays invalid in either direction
// (all309-313, ctH013/019-023, particlesFb002).
// checkAllPlacement enforces the placement half of cos-all-limited: an xs:all
// group may only stand as the whole content model with effective occurs 1
// (min 0/1; 1.1 also allows maxOccurs 0) — never repeated (particlesEa025) and
// never nested inside sequence/choice, not even through a group reference
// (mgA020). In 1.1 an xs:all group MAY additionally appear inside another
// xs:all, but only as a 1..1 group reference to an all group (all008-011:
// a sequence/choice group has no place in an xs:all, and an all-in-all
// reference may not carry minOccurs 0 or maxOccurs > 1).
func (c *compiler) checkAllPlacement(ct *ComplexType) error {
	if ct.particle == nil {
		return nil
	}
	root := ct.particle
	if mg, ok := root.term.(*modelGroup); ok && mg.compositor == cAll {
		maxOK := root.max == 1 || (c.sch.version != Version10 && root.max == 0)
		if !maxOK || root.min > 1 {
			return invalidf("cos-all-limited.2", "an xs:all group must have minOccurs 0 or 1 and maxOccurs 1")
		}
	}
	seen := map[*modelGroup]bool{}
	var walk func(p *particle, parent *modelGroup) error
	walk = func(p *particle, parent *modelGroup) error {
		mg, ok := p.term.(*modelGroup)
		if !ok || seen[mg] {
			return nil
		}
		seen[mg] = true
		if parent != nil {
			switch {
			case c.sch.version == Version10:
				if mg.compositor == cAll {
					return invalidf("cos-all-limited.1", "an xs:all group must be the whole content model")
				}
			case parent.compositor == cAll:
				// 1.1: inside an xs:all, only a 1..1 reference to another all
				// group is admitted (all008-011).
				if mg.compositor != cAll {
					return invalidf("cos-all-limited.1", "a sequence/choice group may not appear inside an xs:all")
				}
				if p.min != 1 || p.max != 1 {
					return invalidf("cos-all-limited.1", "an xs:all inside an xs:all must have minOccurs and maxOccurs 1")
				}
			default:
				if mg.compositor == cAll {
					return invalidf("cos-all-limited.1", "an xs:all group must be the whole content model")
				}
			}
		}
		for _, sub := range mg.particles {
			if err := walk(sub, mg); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, nil)
}

func (c *compiler) checkAllLimited(ct *ComplexType) error {
	if ct.derivation != "extension" {
		return nil
	}
	base, ok := ct.baseType.(*ComplexType)
	if !ok {
		return nil
	}
	baseP := c.sch.effectiveParticle(base)
	if isEmptyParticle(baseP) || isEmptyParticle(ct.particle) {
		return nil
	}
	baseAll, derAll := isAllParticle(baseP), isAllParticle(ct.particle)
	if c.sch.version == Version10 {
		if baseAll || derAll {
			return invalidf("cos-all-limited.1.2", "an xs:all group may not be extended with further content")
		}
		return nil
	}
	switch {
	case baseAll && derAll:
		if baseP.min != ct.particle.min {
			return invalidf("cos-all-limited", "an xs:all extending an xs:all must repeat its minOccurs")
		}
	case baseAll != derAll:
		return invalidf("cos-all-limited.1.2", "an extension may not combine xs:all with another compositor")
	}
	return nil
}

func isEmptyParticle(p *particle) bool {
	if p == nil {
		return true
	}
	if mg, ok := p.term.(*modelGroup); ok {
		return len(mg.particles) == 0
	}
	return false
}

func isAllParticle(p *particle) bool {
	if p == nil {
		return false
	}
	mg, ok := p.term.(*modelGroup)
	return ok && mg.compositor == cAll
}

func (c *compiler) checkAllRestrictions() error {
	for _, ct := range c.allCTs { // named AND anonymous complex types
		if err := c.checkAllLimited(ct); err != nil {
			return err
		}
		if err := c.checkAllPlacement(ct); err != nil {
			return err
		}
		if err := c.checkContentBaseKind(ct); err != nil {
			return err
		}
		// Open-content derivation validity (1.1): a restriction may only NARROW
		// (open016-019, complex018), an extension only WIDEN (open030/033/046,
		// s3_4_1si05s/si06s).
		if c.sch.version == Version11 {
			if err := c.checkOpenContentDerivation(ct); err != nil {
				return err
			}
		}
		// EDC must also hold on the POST-EXTENSION content model — the base's
		// and the extension's particles are concatenated, and a redeclared
		// element with a different type is inconsistent there even though each
		// half is consistent alone (complex017: child1 integer vs date).
		if ct.derivation == "extension" {
			if err := checkElementConsistency(c.sch.effectiveParticle(ct)); err != nil {
				return err
			}
		}
		// 1.1 EDC additions over the EFFECTIVE model: substitution members —
		// abstract ones included — count (wg upa/upa2/edc, subsgroup901);
		// {type table}s must agree (wild078/081); and a wildcard admitting a
		// locally-declared name whose GLOBAL declaration carries a different
		// type makes the model inconsistent (wild063/069/079).
		if c.sch.version == Version11 {
			if err := c.checkElementConsistency11(c.sch.effectiveParticle(ct)); err != nil {
				return err
			}
		}
		// XSD 1.0 Attribute Wildcard Union expressibility (errata E1-10,
		// wildZ013): the union of not(X) with a list containing ABSENT but not
		// X has no 1.0 representation — a schema error. (1.1's negation model
		// expresses every union; wildZ013a's list without ##local is fine.)
		if c.sch.version == Version10 && ct.derivation == "extension" && ct.attrWildcard != nil {
			if base, bok := ct.baseType.(*ComplexType); bok {
				if _, bw := c.sch.effectiveAttrs(base); bw != nil &&
					!wcUnionExpressible10(ct.attrWildcard, bw) {
					return invalidf("src-ct",
						"the attribute wildcard union of %s and its base is not expressible in XSD 1.0", ct.name)
				}
			}
		}
		if ct.derivation != "restriction" {
			continue
		}
		base, ok := ct.baseType.(*ComplexType)
		if !ok {
			continue
		}
		baseP := c.sch.effectiveParticle(base)
		rc := &restrictionChecker{sch: c.sch}
		if !rc.particleRestrictsOK(ct.particle, baseP) {
			return invalidf("", "content model is not a valid restriction of base type %s", base.name)
		}
		// derivation-ok-restriction 2.1.2: an attribute use redeclared in the
		// restriction must have a type validly derived from the base use's
		// (rejected only when provably impossible, e.g. a union type standing
		// where an integer-derived one is required — particlesZ013).
		if len(ct.attrUses) > 0 {
			baseUses, _ := c.sch.effectiveAttrs(base)
			byName := make(map[xname]*attrUse, len(baseUses))
			for _, bu := range baseUses {
				byName[bu.decl.name] = bu
			}
			for _, u := range ct.attrUses {
				if u.prohibited {
					// derivation-ok-restriction.3: a REQUIRED base attribute use
					// may not be removed by the restriction (attZ012).
					if bu, ok := byName[u.decl.name]; ok && bu.required {
						return invalidf("derivation-ok-restriction.3",
							"required attribute %s cannot be prohibited in a restriction of %s", u.decl.name, base.name)
					}
					continue
				}
				if u.decl.typ == nil {
					continue
				}
				if bu, ok := byName[u.decl.name]; ok && bu.decl.typ != nil &&
					!typeRestrictsFrom(u.decl.typ, bu.decl.typ) {
					return invalidf("derivation-ok-restriction",
						"attribute %s: type is not derived from its type in base %s", u.decl.name, base.name)
				}
				// 1.1: a restriction may not change {inheritable}
				// (cta9004/cta9005err).
				if bu, ok := byName[u.decl.name]; ok && c.sch.version == Version11 &&
					bu.inheritable != u.inheritable {
					return invalidf("derivation-ok-restriction",
						"attribute %s: {inheritable} must not change in a restriction", u.decl.name)
				}
				// derivation-ok-restriction 2.2: an attribute use with NO
				// corresponding base use must be admitted by the base's
				// attribute wildcard (ctO004: ##other never admits the absent
				// namespace; ctO003's ##any twin stays valid). Both versions —
				// the old 1.0 gate protected target003, which resolves via
				// local @targetNamespace since M28.
				if _, ok := byName[u.decl.name]; !ok {
					if _, bw := c.sch.effectiveAttrs(base); bw == nil || !wildcardMatches(bw, u.decl.name) {
						return invalidf("derivation-ok-restriction.2.2",
							"attribute %s is not admitted by the base type's attribute wildcard", u.decl.name)
					}
				}
			}
		}
		// The restriction's own attribute wildcard may only narrow the base's
		// (Schema Component Constraint: Attribute Wildcard Subset).
		if rw := ct.attrWildcard; rw != nil {
			if _, bw := c.sch.effectiveAttrs(base); bw == nil || !nsSubset(rw, bw) ||
				wcProcessRank(rw.process) < wcProcessRank(bw.process) ||
				!wcNameSubsetOK(rw, bw) {
				return invalidf("", "attribute wildcard is not a valid restriction of base type %s", base.name)
			}
		}
	}
	return nil
}

// directBuiltinPrim reports the primitive of a SimpleType that is a DIRECT
// reference to a built-in atomic type (type="xs:string" and the like) — no
// user-defined derivation steps, no facets — and whose primitive is a real
// datatype (not one of the ur-types).
func directBuiltinPrim(t *SimpleType) (xpath.AtomType, bool) {
	if t == nil || !t.baseBuiltin || t.base != nil || t.name.Local != "" ||
		t.variety != vAtomic || t.facets.any() {
		return 0, false
	}
	p := xsdPrimitiveOf(t.resolvePrim())
	if p == 0 || p == xpath.XSanyAtomicType || p == xpath.XSuntypedAtomic {
		return 0, false
	}
	return p, true
}

// wcNameSubsetOK checks the QName-level half of Wildcard Subset (1.1): the
// derived wildcard may not re-admit a name the base's notQName excludes, and
// the dynamic ##defined exclusion cannot be traded for a fixed name list —
// declarations added later would escape it (wild057).
// checkOpenContentDerivation enforces the {open content} halves of
// derivation-ok-restriction / cos-ct-extends (1.1).
func (c *compiler) checkOpenContentDerivation(ct *ComplexType) error {
	base, ok := ct.baseType.(*ComplexType)
	if !ok {
		return nil
	}
	bEff := c.sch.effectiveOpenContent(base)
	own := ct.openContent
	switch ct.derivation {
	case "restriction":
		if own == nil || own.mode == "none" || own.wc == nil {
			return nil // restricting open content away is always legal
		}
		if isEmptyParticle(ct.particle) {
			// With an EMPTY derived content model interleave and suffix admit
			// the same instances and no base child survives to constrain —
			// derivation-ok-restriction waives the open-content clauses
			// (open020/021/022 are valid; open016-019 all carry content).
			return nil
		}
		if bEff == nil || bEff.wc == nil {
			return invalidf("derivation-ok-restriction",
				"a restriction cannot introduce open content over a closed base (%s)", base.name)
		}
		if bEff.mode == "suffix" && own.mode == "interleave" {
			return invalidf("derivation-ok-restriction",
				"open content mode cannot loosen suffix to interleave in a restriction of %s", base.name)
		}
		if !nsSubset(own.wc, bEff.wc) || !wcNameSubsetOK(own.wc, bEff.wc) ||
			wcProcessRank(own.wc.process) < wcProcessRank(bEff.wc.process) {
			return invalidf("derivation-ok-restriction",
				"open-content wildcard is not a valid restriction of the base's (%s)", base.name)
		}
	case "extension":
		if own == nil || own.mode == "none" || own.wc == nil {
			return nil // the base's open content carries over unchanged
		}
		if bEff == nil || bEff.wc == nil {
			return nil // adding open content over a closed base is legal
		}
		if bEff.mode == "interleave" && own.mode == "suffix" {
			return invalidf("cos-ct-extends",
				"open content mode cannot narrow interleave to suffix in an extension of %s", base.name)
		}
		// The wildcards themselves UNION (open047) — no subset requirement.
	}
	return nil
}

// wcUnionExpressible10 reports whether the union of two attribute wildcards has
// an XSD 1.0 representation. The single inexpressible case (errata E1-10) is
// not(X) ∪ S where the list S contains the absent namespace but not X.
func wcUnionExpressible10(a, b *wildcard) bool {
	if len(a.parts) > 0 || len(b.parts) > 0 {
		return true // already-union operands: stay conservative
	}
	inexpressible := func(other, list *wildcard) bool {
		if other.nsMode != "other" || list.nsMode != "list" {
			return false
		}
		return containsStr(list.namespaces, "") && !containsStr(list.namespaces, other.targetNS)
	}
	return !inexpressible(a, b) && !inexpressible(b, a)
}

func wcNameSubsetOK(a, b *wildcard) bool {
	if b.notDefined && !a.notDefined {
		return false
	}
	for _, n := range b.notNames {
		if wildcardMatches(a, n) {
			return false
		}
	}
	return true
}

// typeRestrictsFrom reports whether derived is a valid restriction of base. It is
// conservative — it returns true whenever it cannot prove otherwise (unions,
// lists, complex types, unresolved types), so it never rejects a valid schema;
// it only rejects the clear atomic mismatches (e.g. xs:int restricting xs:string).
func typeRestrictsFrom(derived, base Type) bool {
	return typeRestrictsFromDepth(derived, base, 0, false)
}

// typeRestrictsFromStrict is typeRestrictsFrom for the rcase-NameAndTypeOK
// context, where the derived type must be reachable given {extension, list,
// union} excluded — an extension step anywhere in the complex chain disqualifies
// (particlesIj008).
func typeRestrictsFromStrict(derived, base Type) bool {
	return typeRestrictsFromDepth(derived, base, 0, true)
}

func typeRestrictsFromDepth(derived, base Type, depth int, restrictionOnly bool) bool {
	if depth > 32 {
		return true // runaway union nesting: give up conservatively
	}
	bs, bok := base.(*SimpleType)
	ds, dok := derived.(*SimpleType)
	switch {
	case !bok && !dok:
		// Complex vs complex: when both are NAMED complex types, the derived
		// type's base chain (by name) must reach the base type — two unrelated
		// named types are not a valid element-type restriction.
		bc, bcok := base.(*ComplexType)
		dc, dcok := derived.(*ComplexType)
		if !bcok || !dcok || bc == nil || dc == nil || bc.name.zero() || dc.name.zero() {
			return true
		}
		if bc.name == (xname{xsNS, "anyType"}) {
			return true // every type derives from xs:anyType
		}
		for cur, n := dc, 0; cur != nil && n < 64; n++ {
			if cur == bc || cur.name == bc.name {
				return true
			}
			if restrictionOnly && cur.derivation == "extension" {
				return false // an extension step disqualifies under rcase-NameAndTypeOK
			}
			next, ok := cur.baseType.(*ComplexType)
			if !ok || next == nil {
				return false
			}
			cur = next
		}
		return false
	case bok && !dok:
		// Base simple, derived complex: only valid if the derived complex chain
		// (via simpleContent) reaches a simple type deriving from the base.
		if bs == nil || bs.name.zero() {
			return true // anonymous simple base: stay conservative
		}
		cur := derived
		for n := 0; n < 64; n++ {
			switch v := cur.(type) {
			case *ComplexType:
				if v == nil || v.baseType == nil {
					return false // reached the anyType root without meeting the base
				}
				cur = v.baseType
			case *SimpleType:
				if v == nil {
					return true
				}
				return typeRestrictsFromDepth(v, bs, depth+1, restrictionOnly)
			default:
				return true
			}
		}
		return true
	case !bok: // base complex, derived simple: a simple type derives from anyType/anySimpleType only
		bc, _ := base.(*ComplexType)
		if bc == nil || bc.name.zero() {
			return true
		}
		return bc.name == (xname{xsNS, "anyType"})
	}
	// Both simple. Walk the derived restriction chain looking for the base type
	// itself — the membership test applies to every variety (atomic, list, union).
	for s, n := ds, 0; s != nil && n < 64; s, n = s.base, n+1 {
		if s == bs || (!s.name.zero() && s.name == bs.name) {
			return true
		}
	}
	// anySimpleType/anyAtomicType base admits any simple type.
	if bs.baseBuiltin && bs.base == nil && bs.name.zero() && bs.resolvePrim() == xpath.XSanyAtomicType {
		return true
	}
	if bs.variety == vUnion && bs.base == nil {
		// Union member substitutability: a type validly derives from a union if
		// it derives from (or is) any of its member types — but ONLY for a
		// PRISTINE union declaration. A union derived by RESTRICTION from
		// another union (bs.base != nil) narrows the value space with facets;
		// substituting via an ANCESTOR union's member would bypass them
		// (simple014/simple015: sub-chap of xs:date vs faceted chap/dt).
		for _, m := range bs.members {
			if typeRestrictsFromDepth(ds, m, depth+1, restrictionOnly) {
				return true
			}
		}
		return bs.name.zero() && len(bs.members) == 0 // unchecked only when the union is opaque
	}
	if ds.variety != bs.variety {
		// Cross-variety derivation is impossible except via union membership
		// (handled above): a union/list cannot restrict an atomic and vice versa.
		return false
	}
	if bs.variety != vAtomic {
		// Same-variety list/union: derivation is only via the restriction chain.
		// A NAMED user base not on the chain (e.g. two structurally-identical
		// list types) is not a valid restriction; anonymous bases stay unchecked.
		return bs.name.zero()
	}
	bp := bs.resolvePrim()
	if bp == xpath.XSanyAtomicType {
		return true
	}
	// When the base is a USER-DEFINED simple type (named, or carrying facets),
	// membership in the derived chain is the only way to derive from it — the
	// primitive fallback would wrongly accept e.g. xs:string as a restriction of
	// a user restriction OF xs:string. The builtin-hierarchy fallback applies
	// only when the base is a plain builtin reference.
	if !(bs.baseBuiltin && bs.base == nil && bs.name.zero() && !bs.facets.any() && !bs.declared) {
		return false
	}
	return xpath.AtomDerivesFrom(ds.resolvePrim(), bp)
}

func collectContentDeclMap(p *particle, out map[xname]*ElementDecl, seen map[*modelGroup]bool) {
	if p == nil {
		return
	}
	switch t := p.term.(type) {
	case *ElementDecl:
		out[t.name] = t
	case *modelGroup:
		if seen[t] {
			return
		}
		seen[t] = true
		for _, sub := range t.particles {
			collectContentDeclMap(sub, out, seen)
		}
	}
}

// checkGroupCycles rejects a circular (infinite) model-group reference, e.g. a
// group whose content references itself directly or transitively.
func (c *compiler) checkGroupCycles() error {
	done := map[*modelGroup]bool{}
	for _, g := range c.groups {
		if groupHasCycle(g, map[*modelGroup]bool{}, done) {
			return invalidf("", "circular group reference")
		}
	}
	return nil
}

func groupHasCycle(mg *modelGroup, inPath, done map[*modelGroup]bool) bool {
	if inPath[mg] {
		return true
	}
	if done[mg] {
		return false
	}
	inPath[mg] = true
	for _, p := range mg.particles {
		if sub, ok := p.term.(*modelGroup); ok {
			if groupHasCycle(sub, inPath, done) {
				return true
			}
		}
	}
	delete(inPath, mg)
	done[mg] = true
	return false
}

func (c *compiler) parseGroupBody(node *xmltree.Node) (*modelGroup, error) {
	for _, ch := range node.Children {
		if isXS(ch, "sequence") || isXS(ch, "choice") || isXS(ch, "all") {
			return c.parseModelGroup(ch)
		}
	}
	return nil, invalidf("", "group without a model group")
}

// localTargetNS reads the XSD 1.1 @targetNamespace of a LOCAL element or
// attribute declaration (target001/target003). A value different from the
// schema's target namespace requires @form absent and a containing complex
// type derived by RESTRICTION (src-element 5 / src-attribute 4 — target002/
// target004 place it in an extension and are invalid).
func localTargetNS(ch *xmltree.Node, ver Version, tns string) (string, bool, error) {
	v, ok := ch.AttrLocal("targetNamespace")
	if !ok {
		return "", false, nil
	}
	if ver == Version10 {
		// The attribute does not exist in the 1.0 schema for schemas
		// (s3_2_3si05s runs under 1.0 too and expects rejection).
		return "", false, invalidf("src-element", "@targetNamespace on a local declaration is an XSD 1.1 feature")
	}
	v = applyWhiteSpace("collapse", v)
	if v == tns {
		return v, true, nil
	}
	if _, hasForm := ch.AttrLocal("form"); hasForm {
		return "", false, invalidf("src-element", "@targetNamespace and @form are mutually exclusive")
	}
	for n := ch.Parent; n != nil && n.Kind == xmltree.KindElement; n = n.Parent {
		if n.Name.Space != xsNS {
			continue
		}
		switch n.Name.Local {
		case "restriction":
			// A restriction OF xs:anyType is no restriction context at all —
			// §3.3.3/§3.2.3 clause 4.3.2 requires a real base (s3_2_3si05/si08).
			if b, ok := n.AttrLocal("base"); ok {
				if bn := resolveQName(n, b); bn == (xname{xsNS, "anyType"}) {
					return "", false, invalidf("src-element",
						"a targetNamespace-divergent local declaration requires a restriction of a real base type")
				}
			}
			return v, true, nil
		case "extension", "complexType", "schema":
			return "", false, invalidf("src-element",
				"a local declaration may take a different targetNamespace only inside a restriction")
		}
	}
	return "", false, invalidf("src-element",
		"a local declaration may take a different targetNamespace only inside a restriction")
}

func (c *compiler) parseLocalElement(ch *xmltree.Node) (*ElementDecl, error) {
	if ref, ok := ch.AttrLocal("ref"); ok {
		if _, hasName := ch.AttrLocal("name"); hasName {
			return nil, invalidf("", "xs:element has both ref and name")
		}
		if _, hasTN := ch.AttrLocal("targetNamespace"); hasTN {
			// @targetNamespace belongs to a local DECLARATION, never a
			// reference (s3_2_3si10).
			return nil, invalidf("src-element", "xs:element ref cannot carry targetNamespace")
		}
		if _, hasType := ch.AttrLocal("type"); hasType {
			return nil, invalidf("", "xs:element ref cannot also have a type")
		}
		rn := resolveQName(ch, ref)
		if ed, ok := c.sch.elements[rn]; ok {
			if !c.reachableNS(rn.Space) {
				return nil, invalidf("src-resolve", "element %s is in a namespace this schema does not import", rn)
			}
			return ed, nil
		}
		// Chameleon repair: an unqualified ref inside an adopted no-namespace
		// document also denotes the adopting namespace's component.
		if alt, aok := c.chamAlt(rn); aok {
			if ed, ok := c.sch.elements[alt]; ok {
				return ed, nil
			}
		}
		return nil, invalidf("", "unresolved element ref %s", rn)
	}
	name := declName(ch)
	if name == "" || !isNCName(name) {
		return nil, invalidf("src-element", "local xs:element requires a valid NCName name or a ref (got %q)", name)
	}
	ns := ""
	if form, ok := ch.AttrLocal("form"); ok {
		if form == "qualified" {
			ns = c.tns
		}
	} else if c.elementQualified {
		ns = c.tns
	}
	if tn, ok, err := localTargetNS(ch, c.sch.version, c.tns); err != nil {
		return nil, err
	} else if ok {
		ns = tn
	}
	ed := &ElementDecl{name: xname{ns, name}}
	if err := c.fillElementCommon(ed, ch); err != nil {
		return nil, err
	}
	if err := checkValueConstraint(ed.typ, ch, c.sch.version); err != nil {
		return nil, err // a local element's fixed/default must fit its type (stZ070)
	}
	ed.abstract = attrIs(ch, "abstract", "true")
	cs, err := c.parseIdentityConstraints(ch)
	if err != nil {
		return nil, err
	}
	ed.constraints = cs
	if ed.alternatives, err = c.parseAlternatives(ch); err != nil {
		return nil, err
	}
	return ed, nil
}

func (c *compiler) parseWildcard(ch *xmltree.Node) (*wildcard, error) {
	w := &wildcard{targetNS: c.tns, process: "strict"}
	if p, ok := ch.AttrLocal("processContents"); ok {
		w.process = p
	}
	nsc, hasNS := ch.AttrLocal("namespace")
	notNS, hasNotNS := ch.AttrLocal("notNamespace")
	if hasNS && hasNotNS {
		return nil, invalidf("", "xs:%s must not carry both namespace and notNamespace", ch.Name.Local)
	}
	switch {
	case hasNS && nsc == "":
		w.nsMode = "list" // an explicit empty list admits nothing (TSTF, wildZ010)
	case !hasNS || nsc == "##any":
		w.nsMode = "any"
	case nsc == "##other":
		w.nsMode = "other"
	default:
		w.nsMode = "list"
		for _, tok := range strings.Fields(nsc) {
			switch {
			case tok == "##targetNamespace":
				w.namespaces = append(w.namespaces, c.tns)
			case tok == "##local":
				w.namespaces = append(w.namespaces, "")
			case len(tok) >= 2 && tok[:2] == "##":
				return nil, invalidf("", "invalid wildcard namespace token %q", tok)
			default:
				w.namespaces = append(w.namespaces, tok)
			}
		}
	}
	if hasNotNS {
		// XSD 1.1 @notNamespace: the wildcard admits everything except these
		// namespaces. The list must not be empty.
		toks := strings.Fields(notNS)
		if len(toks) == 0 {
			return nil, invalidf("", "notNamespace must not be empty")
		}
		for _, tok := range toks {
			switch {
			case tok == "##targetNamespace":
				w.notNS = append(w.notNS, c.tns)
			case tok == "##local":
				w.notNS = append(w.notNS, "")
			case len(tok) >= 2 && tok[:2] == "##":
				return nil, invalidf("", "invalid notNamespace token %q", tok)
			default:
				w.notNS = append(w.notNS, tok)
			}
		}
	}
	if notQN, ok := ch.AttrLocal("notQName"); ok {
		attrWildcard := ch.Name.Local == "anyAttribute"
		for _, tok := range strings.Fields(notQN) {
			switch {
			case tok == "##defined":
				// Every global declaration of the schema; pass 1 has registered
				// them all by the time any content model is compiled.
				w.notDefined = true
				if attrWildcard {
					for n := range c.sch.attributes {
						w.notDefNames = append(w.notDefNames, n)
					}
				} else {
					for n := range c.sch.elements {
						w.notDefNames = append(w.notDefNames, n)
					}
				}
			case tok == "##definedSibling":
				if attrWildcard {
					return nil, invalidf("", "##definedSibling is not allowed on xs:anyAttribute")
				}
				w.notSibling = true
			case len(tok) >= 2 && tok[:2] == "##":
				return nil, invalidf("", "invalid notQName token %q", tok)
			default:
				if !validQNameLexical(tok) {
					return nil, invalidf("", "invalid QName in notQName %q", tok)
				}
				if i := strings.IndexByte(tok, ':'); i >= 0 {
					if _, ok := ch.LookupPrefix(tok[:i]); !ok {
						return nil, invalidf("", "undeclared prefix in notQName %q", tok)
					}
				}
				n := resolveQName(ch, tok)
				// A name that the wildcard would not admit anyway cannot be
				// excluded from it (XSD 1.1 Schema Representation Constraint:
				// each notQName member must be in an allowed namespace).
				if !w.admitsNamespace(n.Space) {
					return nil, invalidf("", "notQName %q is not in a namespace the wildcard allows", tok)
				}
				w.notNames = append(w.notNames, n)
			}
		}
	}
	return w, nil
}

// admitsNamespace reports whether the wildcard's namespace constraint (positive
// part plus @notNamespace, ignoring @notQName) admits ns.
func (w *wildcard) admitsNamespace(ns string) bool {
	for _, x := range w.notNS {
		if x == ns {
			return false
		}
	}
	switch w.nsMode {
	case "other":
		return ns != w.targetNS && ns != ""
	case "list":
		return containsStr(w.namespaces, ns)
	}
	return true
}

// fillWildcardSiblings resolves ##definedSibling for every wildcard in a content
// model: the excluded names are those of the element declarations appearing
// elsewhere in the same content model, together with every element substitutable
// for one of them (§3.10.2.2 — sns1/s1sn/wild071). It therefore runs as a
// post-pass once substitution groups are built, not during type parsing.
func (c *compiler) fillWildcardSiblings(p *particle, oc *openContent) {
	var wcs []*wildcard
	var names []xname
	seen := map[*modelGroup]bool{}
	if oc != nil && oc.wc != nil && oc.wc.notSibling {
		wcs = append(wcs, oc.wc)
	}
	addSubst := func(head *ElementDecl) {
		visited := map[xname]bool{}
		var walk func(h xname)
		walk = func(h xname) {
			for _, m := range c.sch.substMembers[h] {
				if visited[m.name] {
					continue
				}
				visited[m.name] = true
				if c.sch.substitutionAllowed(head, m) {
					names = append(names, m.name)
				}
				walk(m.name)
			}
		}
		walk(head.name)
	}
	var walk func(*particle)
	walk = func(q *particle) {
		if q == nil {
			return
		}
		switch t := q.term.(type) {
		case *ElementDecl:
			names = append(names, t.name)
			if c.sch.elements[t.name] == t {
				addSubst(t) // a global reference admits its substitutes too
			}
		case *wildcard:
			if t.notSibling {
				wcs = append(wcs, t)
			}
		case *modelGroup:
			if seen[t] {
				return
			}
			seen[t] = true
			for _, sub := range t.particles {
				walk(sub)
			}
		}
	}
	walk(p)
	for _, w := range wcs {
		w.notNames = append(w.notNames, names...)
		w.notSibling = false // filled once; a shared group keeps its first scope
	}
}

// fillAllWildcardSiblings runs fillWildcardSiblings over every complex type
// once substitution groups are known. The type's OPEN-CONTENT wildcard gets
// the same sibling exclusion — its ##definedSibling names the content model's
// elements too (wild074).
func (c *compiler) fillAllWildcardSiblings() {
	for _, ct := range c.allCTs {
		c.fillWildcardSiblings(ct.particle, ct.openContent)
	}
}

// parseAttributes collects the attribute uses (and any attribute wildcard) that
// are direct children of node (a complexType, extension, restriction, or
// attributeGroup).
func (c *compiler) parseAttributes(node *xmltree.Node) ([]*attrUse, *wildcard, error) {
	var uses []*attrUse
	var wc *wildcard
	seen := map[xname]bool{}
	for _, ch := range node.Children {
		switch {
		case isXS(ch, "attribute"):
			u, err := c.parseAttrUse(ch)
			if err != nil {
				return nil, nil, err
			}
			if u != nil {
				if seen[u.decl.name] {
					return nil, nil, invalidf("", "duplicate attribute %s", u.decl.name)
				}
				seen[u.decl.name] = true
				uses = append(uses, u)
			}
		case isXS(ch, "attributeGroup"):
			ref, ok := ch.AttrLocal("ref")
			if !ok {
				return nil, nil, invalidf("", "attributeGroup without a ref")
			}
			agn := resolveQName(ch, ref)
			ag, ok := c.attrGroups[agn]
			if ok && !c.reachableNS(agn.Space) {
				return nil, nil, invalidf("src-resolve", "attributeGroup %s is in a namespace this schema does not import", agn)
			}
			if orig, isRedef := c.redefineAttrGroup[agn]; isRedef && withinRedefine(ch) {
				ag, ok = orig, true // redefined attributeGroup self-reference
			}
			if !ok {
				// Chameleon repair: an unqualified ref inside an adopted
				// no-namespace document also denotes the adopting namespace's.
				if alt, aok := c.chamAlt(agn); aok {
					if ag2, ok2 := c.attrGroups[alt]; ok2 {
						ag, ok = ag2, true
					}
				}
			}
			if !ok {
				return nil, nil, invalidf("", "unresolved attributeGroup ref %s", ref)
			}
			// ct-props-correct.4: two attribute uses of the same name, however they
			// are reached, are an error — a referenced group may not reintroduce a
			// name already contributed here.
			for _, gu := range ag.uses {
				if seen[gu.decl.name] {
					return nil, nil, invalidf("ct-props-correct.4", "duplicate attribute %s", gu.decl.name)
				}
				seen[gu.decl.name] = true
			}
			uses = append(uses, ag.uses...)
			wc = intersectWildcards(wc, ag.wildcard)
		case isXS(ch, "anyAttribute"):
			w, err := c.parseWildcard(ch)
			if err != nil {
				return nil, nil, err
			}
			wc = intersectWildcards(wc, w)
		}
	}
	return uses, wc, nil
}

// intersectWildcards combines the attribute wildcards a complex type collects
// from its own xs:anyAttribute and from each attributeGroup it references. XSD
// §3.4.2 makes the type's "complete wildcard" their INTERSECTION, not the last
// one seen: two groups admitting disjoint namespaces together admit nothing.
func intersectWildcards(a, b *wildcard) *wildcard {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case len(a.parts) > 0 || len(b.parts) > 0:
		return a // a union operand: leave the (already computed) result alone
	}
	proc := a.process
	if proc != b.process {
		proc = "strict" // the stronger requirement wins
	}
	out := &wildcard{process: proc, targetNS: a.targetNS,
		notNS:    append(append([]string{}, a.notNS...), b.notNS...),
		notNames: append(append([]xname{}, a.notNames...), b.notNames...),
		// ##defined survives an INTERSECTION when either operand carries it —
		// both constraints apply, so either exclusion set stays effective
		// (wild057/058/059; contrast the union rule in wildcardMatches).
		notDefined:  a.notDefined || b.notDefined,
		notDefNames: append(append([]xname{}, a.notDefNames...), b.notDefNames...)}
	switch {
	case a.nsMode == "any":
		out.nsMode, out.namespaces = b.nsMode, b.namespaces
		out.targetNS = b.targetNS
	case b.nsMode == "any":
		out.nsMode, out.namespaces = a.nsMode, a.namespaces
	case a.nsMode == "other" && b.nsMode == "other":
		out.nsMode = "other"
		if a.targetNS != b.targetNS {
			// Two different ##other constraints: not expressible as one, so fall
			// back to the empty set rather than admitting too much.
			out.nsMode, out.namespaces = "list", nil
		}
	case a.nsMode == "other":
		out.nsMode, out.namespaces = "list", excludeNS(b.namespaces, a.targetNS)
	case b.nsMode == "other":
		out.nsMode, out.namespaces = "list", excludeNS(a.namespaces, b.targetNS)
	default: // both lists
		out.nsMode = "list"
		for _, ns := range a.namespaces {
			if containsStr(b.namespaces, ns) {
				out.namespaces = append(out.namespaces, ns)
			}
		}
	}
	return out
}

func excludeNS(list []string, drop string) []string {
	var out []string
	for _, ns := range list {
		if ns != drop && ns != "" {
			out = append(out, ns)
		}
	}
	return out
}

func (c *compiler) parseAttrGroupBody(node *xmltree.Node) (*attrGroupDef, error) {
	uses, wc, err := c.parseAttributes(node)
	if err != nil {
		return nil, err
	}
	// An attribute group's {attribute uses} contain no prohibited uses:
	// use="prohibited" only has meaning as a DIRECT child of a complex-type
	// restriction, where it suppresses the inherited use. Reached through a
	// group reference it contributes nothing — and must not erase anything
	// (attZ015, TSTF-confirmed).
	kept := uses[:0]
	for _, u := range uses {
		if !u.prohibited {
			kept = append(kept, u)
		}
	}
	return &attrGroupDef{uses: kept, wildcard: wc}, nil
}

// builtinXMLAttr returns the built-in declaration for a reference into the XML
// namespace (xml:lang, xml:space, xml:base, xml:id), which any schema may use by
// ref without importing them.
func builtinXMLAttr(n xname) (*AttributeDecl, bool) {
	if n.Space != xmlNS {
		return nil, false
	}
	typ := xpath.XSstring
	switch n.Local {
	case "base":
		typ = xpath.XSanyURI
	case "id":
		typ = xpath.XSid
	case "lang", "space":
		// modelled as string (their finer lexical/enumeration constraints do not
		// affect validity decisions we make)
	default:
		return nil, false
	}
	return &AttributeDecl{name: n, typ: &SimpleType{variety: vAtomic, baseBuiltin: true, prim: typ}}, true
}

// builtinXLinkAttr returns the built-in declaration for a reference into the
// XLink namespace. XLink's global attributes are a fixed, well-known vocabulary
// whose schema document lives at an http: URL (which we never dereference), so
// they are supplied from a built-in catalogue exactly like the XML-namespace
// attributes above. Only the ten globals of XLink 1.0 are provided, and only on
// a lookup miss, so a schema that really does import xlink.xsd wins.
func builtinXLinkAttr(n xname) (*AttributeDecl, bool) {
	if n.Space != xlinkNS {
		return nil, false
	}
	var typ xpath.AtomType
	switch n.Local {
	case "href", "role", "arcrole":
		typ = xpath.XSanyURI
	case "title":
		typ = xpath.XSstring
	case "type", "show", "actuate":
		typ = xpath.XStoken
	case "label", "from", "to":
		typ = xpath.XSncname
	default:
		return nil, false
	}
	return &AttributeDecl{name: n, typ: &SimpleType{variety: vAtomic, baseBuiltin: true, prim: typ}}, true
}

// builtinAttrDecl resolves an attribute reference that no declaration in the
// schema satisfies, against the two namespaces every processor knows without an
// import: the XML namespace and XLink.
func builtinAttrDecl(n xname) (*AttributeDecl, bool) {
	if ad, ok := builtinXMLAttr(n); ok {
		return ad, true
	}
	return builtinXLinkAttr(n)
}

func (c *compiler) parseAttrUse(ch *xmltree.Node) (*attrUse, error) {
	u := &attrUse{}
	// Use-level {inheritable}: an explicit attribute on the use wins over the
	// referenced declaration's (cta0012); resolved after the decl below.
	useInh, hasUseInh := ch.AttrLocal("inheritable")
	if ref, ok := ch.AttrLocal("ref"); ok {
		if _, hasName := ch.AttrLocal("name"); hasName {
			return nil, invalidf("", "xs:attribute has both ref and name")
		}
		if _, hasType := ch.AttrLocal("type"); hasType {
			return nil, invalidf("", "xs:attribute ref cannot also have a type")
		}
		rn := resolveQName(ch, ref)
		ad, ok := c.sch.attributes[rn]
		if ok && !c.reachableNS(rn.Space) {
			return nil, invalidf("src-resolve", "attribute %s is in a namespace this schema does not import", rn)
		}
		if !ok {
			// Chameleon repair: an unqualified ref inside an adopted
			// no-namespace document also denotes the adopting namespace's.
			if alt, aok := c.chamAlt(rn); aok {
				if ad2, ok2 := c.sch.attributes[alt]; ok2 {
					ad, ok = ad2, true
				}
			}
		}
		if !ok {
			// The XML namespace provides four built-in attributes (xml:lang,
			// xml:space, xml:base, xml:id) referenceable without an import.
			if ad, ok = builtinAttrDecl(rn); !ok {
				return nil, invalidf("", "unresolved attribute ref %s", ref)
			}
		}
		u.decl = ad
	} else {
		name := declName(ch)
		if name == "" || !isNCName(name) {
			return nil, invalidf("src-attribute", "local xs:attribute requires a valid NCName name or a ref (got %q)", name)
		}
		ns := ""
		if form, ok := ch.AttrLocal("form"); ok {
			if form == "qualified" {
				ns = c.tns
			}
		} else if c.attributeQualified {
			ns = c.tns
		}
		if tn, tok, err := localTargetNS(ch, c.sch.version, c.tns); err != nil {
			return nil, err
		} else if tok {
			ns = tn
		}
		// no-xsi: an attribute declaration's {target namespace} must not be the
		// XSI namespace (attKa015/attKb018a; an UNQUALIFIED local in an
		// xsi-targeted schema is fine — attKb018/attKc018).
		if ns == xsiNS {
			return nil, invalidf("no-xsi", "an attribute declaration cannot target the XSI namespace")
		}
		st, err := c.attributeType(ch)
		if err != nil {
			return nil, err
		}
		u.decl = &AttributeDecl{name: xname{ns, name}, typ: st,
			inheritable: boolAttrTrue(ch, "inheritable")}
	}
	if hasUseInh {
		u.inheritable = applyWhiteSpace("collapse", useInh) == "true" || applyWhiteSpace("collapse", useInh) == "1"
	} else if u.decl != nil {
		u.inheritable = u.decl.inheritable
	}
	switch use, _ := ch.AttrLocal("use"); use {
	case "required":
		u.required = true
	case "prohibited":
		u.prohibited = true
	}
	if d, ok := ch.AttrLocal("default"); ok {
		u.def, u.hasDefault = d, true
	}
	if fx, ok := ch.AttrLocal("fixed"); ok {
		u.fixed, u.hasFixed = fx, true
	}
	if u.prohibited && u.hasFixed {
		// use="prohibited" together with an explicit fixed value: XSD 1.1 makes
		// the combination a schema error (bug 14245); in 1.0 the suite holds
		// that the fixed use SURVIVES — the prohibition is dropped, and an
		// instance may supply the attribute with the fixed value (attP031).
		if c.sch.version == Version11 {
			return nil, invalidf("src-attribute", "attribute %s cannot be both prohibited and fixed", u.decl.name)
		}
		u.prohibited = false
	}
	// A reference with no value constraint of its own inherits the declaration's,
	// which is what makes a global fixed value binding on every use of it.
	if u.decl != nil && !u.hasDefault && !u.hasFixed {
		switch {
		case u.decl.hasFixed:
			u.fixed, u.hasFixed = u.decl.fixed, true
		case u.decl.hasDefault:
			u.def, u.hasDefault = u.decl.def, true
		}
	}
	// au-props-correct.2: a reference may repeat the declaration's fixed value
	// but not change it, and may not turn it into a default.
	if u.decl != nil && u.decl.hasFixed {
		switch {
		case u.hasDefault:
			return nil, invalidf("au-props-correct.2", "attribute %s is fixed in its declaration and cannot take a default", u.decl.name)
		case u.hasFixed && applyWhiteSpace("collapse", u.fixed) != applyWhiteSpace("collapse", u.decl.fixed):
			return nil, invalidf("au-props-correct.2", "attribute %s must keep the fixed value of its declaration", u.decl.name)
		}
	}
	if u.decl != nil {
		if err := checkValueConstraint(u.decl.typ, ch, c.sch.version); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// --- helpers ----------------------------------------------------------------

// checkSimpleTypeStructure enforces the element grammar of xs:simpleType: at
// most one leading annotation, and exactly one derivation (restriction XOR list
// XOR union).
func checkSimpleTypeStructure(node *xmltree.Node) error {
	var nAnnot, nDeriv int
	sawDeriv := false
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "annotation":
			nAnnot++
			if sawDeriv {
				return invalidf("", "xs:annotation must be the first child of xs:simpleType")
			}
		case "restriction", "list", "union":
			nDeriv++
			sawDeriv = true
		default:
			sawDeriv = true
		}
	}
	if nAnnot > 1 {
		return invalidf("", "xs:simpleType has more than one annotation")
	}
	if nDeriv != 1 {
		return invalidf("", "xs:simpleType must have exactly one of restriction/list/union")
	}
	// A name belongs to a top-level definition only; a local (anonymous)
	// simpleType must not carry one, and a top-level one must.
	_, named := node.AttrLocal("name")
	topLevel := node.Parent != nil && (isXS(node.Parent, "schema") || isXS(node.Parent, "redefine") || isXS(node.Parent, "override"))
	if named && !topLevel {
		return invalidf("st-props-correct", "a local xs:simpleType must not have a name")
	}
	if !named && topLevel {
		return invalidf("st-props-correct", "a top-level xs:simpleType requires a name")
	}
	// The derivation itself takes its base either by reference or inline, never
	// both, and never twice.
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		var refAttr string
		switch ch.Name.Local {
		case "restriction":
			refAttr = "base"
		case "list":
			refAttr = "itemType"
		default:
			continue
		}
		inline := 0
		for _, sub := range ch.Children {
			if isXS(sub, "simpleType") {
				inline++
			}
		}
		_, hasRef := ch.AttrLocal(refAttr)
		if inline > 1 {
			return invalidf("", "xs:%s may hold at most one inline xs:simpleType", ch.Name.Local)
		}
		if hasRef && inline > 0 {
			return invalidf("", "xs:%s has both @%s and an inline xs:simpleType", ch.Name.Local, refAttr)
		}
	}
	return nil
}

func occursOf(ch *xmltree.Node) (int, int) {
	min := 1
	if v, ok := ch.AttrLocal("minOccurs"); ok {
		min = atoiOr(v)
	}
	max := 1
	if v, ok := ch.AttrLocal("maxOccurs"); ok {
		if v == "unbounded" {
			max = unbounded
		} else {
			max = atoiOr(v)
		}
	}
	return min, max
}

func simpleContentType(t Type) *SimpleType {
	switch b := t.(type) {
	case *SimpleType:
		return b
	case *ComplexType:
		return b.simpleType
	}
	return nil
}

func hasFacetChildren(node *xmltree.Node) bool {
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "annotation", "attribute", "attributeGroup", "anyAttribute", "simpleType":
		default:
			return true // a facet element
		}
	}
	return false
}

// absentGroup is the stand-in for a model group reference that resolves to
// nothing: it admits any content, so an incomplete schema constrains nothing
// through the missing group instead of being rejected outright.
func absentGroup() *modelGroup {
	return &modelGroup{compositor: cSeq, particles: []*particle{
		{min: 0, max: unbounded, term: &wildcard{nsMode: "any", process: "skip"}},
	}}
}

// checkSubstitutionGroups enforces the constraints on substitution-group
// affiliation (cos-equiv-derived-ok-rec): a member's type must be validly
// derived from the head's type, and the head's {substitution group exclusions}
// — its @final, or the schema's @finalDefault — bars a member that reaches the
// head's type by an excluded derivation method.
func (c *compiler) checkSubstitutionGroups() error {
	for head, members := range c.sch.substMembers {
		h, ok := c.sch.elements[head]
		if !ok || h.typ == nil {
			continue
		}
		for _, m := range members {
			if m.typ == nil || m.typ == h.typ {
				continue
			}
			// e-props-correct.4: the member's type must be validly derived from
			// the head's. Provably impossible cases (a variety never reaches the
			// other through a restriction chain): union/list vs an atomic head
			// (particlesZ014/Z021); anything non-list vs a list head (addB141);
			// a complex type with non-simple content vs any simple head
			// (stZ048); a simple type vs a NAMED complex head (stZ049).
			if st, sok := m.typ.(*SimpleType); sok {
				if ht, hok := h.typ.(*SimpleType); hok {
					atomicHead := ht.variety == vAtomic && ht.resolvePrim() != xpath.XSanyAtomicType
					if ((st.variety == vUnion || st.variety == vList) && atomicHead) ||
						(ht.variety == vList && st.variety != vList) {
						return invalidf("e-props-correct.4",
							"element %s: type is not derived from the type of substitution-group head %s", m.name, head)
					}
					// Two DIRECT built-in atomic types with different primitives
					// can never stand in a derivation chain (restriction preserves
					// the primitive; s2_2_2si02s: an xs:string member under an
					// xs:integer head). Restricted to direct builtin references —
					// user chains (redefine, the ipo* canaries) stay untouched.
					if sp, sok2 := directBuiltinPrim(st); sok2 {
						if hp, hok2 := directBuiltinPrim(ht); hok2 && sp != hp {
							return invalidf("e-props-correct.4",
								"element %s: type is not derived from the type of substitution-group head %s", m.name, head)
						}
					}
				}
				if hc, hok := h.typ.(*ComplexType); hok && !hc.name.zero() &&
					hc.name != (xname{xsNS, "anyType"}) {
					_ = hc
					return invalidf("e-props-correct.4",
						"element %s: a simple type cannot derive from complex head type of %s", m.name, head)
				}
			}
			if mc, mok := m.typ.(*ComplexType); mok && mc.kind != contentSimple {
				if _, hok := h.typ.(*SimpleType); hok {
					return invalidf("e-props-correct.4",
						"element %s: a complex type cannot derive from the simple head type of %s", m.name, head)
				}
			}
			methods := derivationMethodsTo(m.typ, h.typ)
			if len(methods) == 0 {
				continue // not reached — leave it to the other derivation checks
			}
			for meth := range methods {
				if h.final[meth] {
					return invalidf("cos-equiv-derived-ok-rec",
						"element %s may not join the substitution group of %s: %s is final there", m.name, head, meth)
				}
			}
		}
	}
	return nil
}
