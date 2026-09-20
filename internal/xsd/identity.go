package xsd

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// --- parsing ----------------------------------------------------------------

// parseIdentityConstraints reads the xs:key/xs:keyref/xs:unique children of an
// element declaration node.
func (c *compiler) parseIdentityConstraints(node *xmltree.Node) ([]*identityConstraint, error) {
	var out []*identityConstraint
	for _, ch := range node.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		var kind string
		switch ch.Name.Local {
		case "key", "keyref", "unique":
			kind = ch.Name.Local
		default:
			continue
		}
		if refv, hasRef := ch.AttrLocal("ref"); hasRef {
			// XSD 1.1 §3.11.2: an identity constraint may REFERENCE another
			// definition of the same kind instead of declaring one. Only an
			// annotation child is admitted, @name must be absent, and forward
			// references are legal (resolved in checkKeyrefs).
			if c.sch.version == Version10 {
				return nil, invalidf("src-identity-constraint", "identity-constraint @ref is an XSD 1.1 feature")
			}
			if _, hasName := ch.AttrLocal("name"); hasName {
				return nil, invalidf("src-identity-constraint", "@name and @ref are mutually exclusive on xs:%s", kind)
			}
			if _, hasRefer := ch.AttrLocal("refer"); hasRefer {
				return nil, invalidf("src-identity-constraint", "@refer is not allowed together with @ref (s2_2_4si07)")
			}
			for _, sub := range ch.Children {
				if sub.Kind == xmltree.KindElement && !isXS(sub, "annotation") {
					return nil, invalidf("src-identity-constraint", "an xs:%s with @ref admits only an annotation child", kind)
				}
			}
			ic := &identityConstraint{kind: kind, refName: resolveQName(ch, refv), refPending: true}
			out = append(out, ic)
			c.icRefs = append(c.icRefs, ic)
			continue
		}
		ic := &identityConstraint{kind: kind, ns: ch.InScopeNamespaces()}
		name := declName(ch)
		if !isNCName(name) {
			return nil, invalidf("", "invalid identity-constraint name %q", name)
		}
		ic.name = xname{c.tns, name}
		if kind == "keyref" {
			if r, ok := ch.AttrLocal("refer"); ok {
				ic.refer = resolveQName(ch, r)
				// src-resolve: @refer may only reach a namespace this document
				// imports (idC019).
				if !c.reachableNS(ic.refer.Space) {
					return nil, invalidf("src-resolve", "keyref @refer %s is in a namespace this schema does not import", ic.refer)
				}
			}
		}
		sel := firstXSChild(ch, "selector")
		if sel == nil {
			return nil, invalidf("", "identity constraint without a selector")
		}
		sx, _ := sel.AttrLocal("xpath")
		if err := validateIdentityXPath(sx, false, sel); err != nil {
			return nil, err
		}
		sp, err := xpath.Parse(sx)
		if err != nil {
			return nil, ErrUnsupported // a selector our XPath parser can't render
		}
		ic.selector = sp
		ic.selNS = c.resolveXPathDefaultNS(sel)
		for _, fch := range ch.Children {
			if !isXS(fch, "field") {
				continue
			}
			fx, _ := fch.AttrLocal("xpath")
			if err := validateIdentityXPath(fx, true, fch); err != nil {
				return nil, err
			}
			fp, err := xpath.Parse(fx)
			if err != nil {
				return nil, ErrUnsupported
			}
			ic.fields = append(ic.fields, fp)
			ic.fieldRaw = append(ic.fieldRaw, strings.TrimSpace(fx))
			ic.fieldSrc = append(ic.fieldSrc, fx)
			ic.fieldNS = append(ic.fieldNS, c.resolveXPathDefaultNS(fch))
		}
		if len(ic.fields) == 0 {
			return nil, invalidf("", "identity constraint without a field")
		}
		out = append(out, ic)
		c.allICs = append(c.allICs, ic)
	}
	return out, nil
}

// checkKeyrefs cross-checks the identity constraints of the whole schema: names
// are unique, a keyref's @refer names a key or unique, and it carries exactly as
// many fields as the constraint it refers to.
func (c *compiler) checkKeyrefs() error {
	byName := map[xname]*identityConstraint{}
	for _, ic := range c.allICs {
		if prev, dup := byName[ic.name]; dup && prev != ic {
			return invalidf("c-props-correct.1", "duplicate identity constraint %s", ic.name)
		}
		byName[ic.name] = ic
	}
	// Resolve 1.1 @ref uses: the referenced definition (same kind) is copied
	// into the placeholder, so the SAME constraint is enforced independently
	// at every element it is attached to (id040.n01 vs .n02).
	for _, ic := range c.icRefs {
		if !ic.refPending {
			continue
		}
		ref, ok := byName[ic.refName]
		if !ok {
			return invalidf("src-resolve", "identity constraint %s does not resolve", ic.refName)
		}
		if ref.kind != ic.kind {
			return invalidf("c-props-correct", "xs:%s @ref must name a %s (got %s)", ic.kind, ic.kind, ref.kind)
		}
		*ic = *ref
	}
	for _, ic := range c.allICs {
		if ic.kind != "keyref" || ic.refer.zero() {
			continue
		}
		ref, ok := byName[ic.refer]
		if !ok || ref.kind == "keyref" {
			return invalidf("c-props-correct.2", "keyref %s refers to %s, which is not a key or unique", ic.name, ic.refer)
		}
		if len(ref.fields) != len(ic.fields) {
			return invalidf("c-props-correct.2", "keyref %s has %d fields but %s has %d", ic.name, len(ic.fields), ic.refer, len(ref.fields))
		}
	}
	return nil
}

// --- evaluation -------------------------------------------------------------

// keyTable is the set of key tuples gathered for one key/unique constraint at one
// scope element.
type keyTable struct {
	tuples map[string]bool
}

type xsdNS map[string]string

func (m xsdNS) ResolveNS(prefix string) (string, bool) { u, ok := m[prefix]; return u, ok }

// validateIdentityXPath checks a selector (isField=false) or field (isField=true)
// against the restricted XPath subset of XSD identity constraints (Part 1 §3.11.6):
// a '|'-separated set of paths, each an optional leading './/' then '/'-separated
// steps ('.' | NameTest), with '@'NameTest permitted only as a field's last step.
// Absolute paths, embedded '//', chained './/', and undefined prefixes are invalid.
func validateIdentityXPath(expr string, isField bool, node *xmltree.Node) error {
	// A QName admits no internal whitespace: a single ':' (not the '::' axis
	// separator) directly adjacent to whitespace splits a QName across tokens,
	// which the grammar has no production for ("xpns: *", idJ016). Checked on
	// the raw text, because the collapse below repairs exactly this.
	for i := 0; i < len(expr); i++ {
		if expr[i] != ':' {
			continue
		}
		if (i > 0 && expr[i-1] == ':') || (i+1 < len(expr) && expr[i+1] == ':') {
			continue // '::' axis separator
		}
		if (i > 0 && isXPWS(expr[i-1])) || (i+1 < len(expr) && isXPWS(expr[i+1])) {
			return invalidf("", "invalid xpath %q: whitespace inside a QName", expr)
		}
	}
	// No token in this grammar contains internal whitespace, so collapsing all
	// whitespace lets the structural checks ignore inter-token spacing.
	expr = strings.Join(xmlFields(expr), "")
	for _, path := range strings.Split(expr, "|") {
		if !validIdentityPath(path, isField, node) {
			kind := "selector"
			if isField {
				kind = "field"
			}
			return invalidf("", "invalid %s xpath %q", kind, expr)
		}
	}
	return nil
}

func isXPWS(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func validIdentityPath(p string, isField bool, node *xmltree.Node) bool {
	if p == "" {
		return false
	}
	p = strings.TrimPrefix(p, ".//")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "//") {
		return false // absolute, empty, or a second descendant operator
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if !validIdentityStep(strings.TrimSpace(s), isField, i == len(segs)-1, node) {
			return false
		}
	}
	return true
}

// validIdentityStep validates one step: '.', a NameTest (optionally with a
// 'child::' axis), or — only as a field's final step — an attribute step
// ('@NameTest' or 'attribute::NameTest'). Whitespace around '::' is tolerated.
func validIdentityStep(s string, isField, last bool, node *xmltree.Node) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "@") {
		return isField && last && validIdentityNameTest(strings.TrimSpace(s[1:]), node)
	}
	if idx := strings.Index(s, "::"); idx >= 0 {
		axis := strings.TrimSpace(s[:idx])
		rest := strings.TrimSpace(s[idx+2:])
		switch axis {
		case "child":
			return validIdentityNameTest(rest, node)
		case "attribute":
			return isField && last && validIdentityNameTest(rest, node)
		}
		return false
	}
	if s == "." {
		return true
	}
	return validIdentityNameTest(s, node)
}

func validIdentityNameTest(s string, node *xmltree.Node) bool {
	if s == "*" {
		return true
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false // whitespace inside a name test ("xpns: *", idJ016)
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		prefix, local := s[:i], s[i+1:]
		if !reNCName.MatchString(prefix) {
			return false
		}
		if _, ok := node.LookupPrefix(prefix); !ok {
			return false // undefined namespace prefix
		}
		return local == "*" || reNCName.MatchString(local)
	}
	return reNCName.MatchString(s)
}

func (ic *identityConstraint) evalNodes(p *xpath.Parsed, ctx *xmltree.Node, defNS string) ([]*xmltree.Node, error) {
	obj, err := p.Eval(&xpath.Context{
		Node: ctx, Pos: 1, Size: 1,
		NS:            xsdNS(ic.ns),
		DefaultElemNS: defNS,
	})
	if err != nil {
		return nil, err
	}
	var nodes []*xmltree.Node
	for _, it := range xpath.Items(obj) {
		if n, ok := it.(*xmltree.Node); ok {
			nodes = append(nodes, n)
		}
	}
	return nodes, nil
}

const tupleSep = "\x00"

// tupleFor evaluates the fields against a target node, returning the serialized
// key tuple and whether every field selected exactly one node (complete).
func (s *Schema) tupleFor(ic *identityConstraint, target *xmltree.Node) (string, bool, error) {
	vals := make([]string, 0, len(ic.fields))
	for fi, f := range ic.fields {
		nodes, err := ic.evalNodes(f, target, ic.fieldNS[fi])
		if err != nil {
			return "", false, ErrUnsupported
		}
		if len(nodes) == 0 {
			// An absent attribute with a default/fixed value still contributes
			// to a KEY/UNIQUE key-sequence (idF016/017, idG011/012, idZ011_a) —
			// but a keyref must not resolve through a supplied default
			// (idH015/016, idK004: the M24 revert's counter-tests).
			if ic.kind != "keyref" && fi < len(ic.fieldRaw) {
				if v, st, ok := s.absentAttrDefault(ic.fieldRaw[fi], target); ok {
					// The supplied value must take the SAME canonical key form
					// as an explicit one (fieldKeyValue), or "test" and the
					// defaulted "test" would never collide.
					key := v
					if st != nil && st.variety == vAtomic {
						if cv, cok := canonAtomic(st, target, applyWhiteSpace(st.effectiveWhiteSpace(), v)); cok {
							key = cv
						}
					}
					vals = append(vals, key)
					continue
				}
			}
			return "", false, nil // incomplete
		}
		if len(nodes) > 1 {
			return "", false, invalidf("cvc-identity-constraint.3", "field selects more than one node")
		}
		if err := s.fieldTypeOK(nodes[0]); err != nil {
			return "", false, err
		}
		vals = append(vals, s.fieldKeyValue(nodes[0]))
	}
	return strings.Join(vals, tupleSep), true, nil
}

// absentAttrDefault resolves a field of the simple form "@name" against the
// target element's declared type: an absent attribute whose use carries a
// default or fixed value contributes that value to the key sequence.
func (s *Schema) absentAttrDefault(raw string, target *xmltree.Node) (string, *SimpleType, bool) {
	if !strings.HasPrefix(raw, "@") {
		return "", nil, false
	}
	name := strings.TrimSpace(raw[1:])
	if name == "" || strings.ContainsAny(name, "/|[]:*") {
		return "", nil, false // only the plain unprefixed @NCName form
	}
	typ := s.elementTypeOf(target)
	ct, ok := typ.(*ComplexType)
	if !ok {
		return "", nil, false
	}
	uses, _ := s.effectiveAttrs(ct)
	for _, u := range uses {
		if u.decl.name.Local != name || u.decl.name.Space != "" {
			continue
		}
		switch {
		case u.hasDefault:
			return u.def, u.decl.typ, true
		case u.hasFixed:
			return u.fixed, u.decl.typ, true
		}
	}
	return "", nil, false
}

// fieldTypeOK enforces cvc-identity-constraint.3: a field must select a node
// with a SIMPLE type — a simple-typed element, an element whose complex type
// has simple content, or a declared attribute. An untyped (anyType) element,
// an element-only/mixed complex type, or an attribute known only through a
// skip wildcard has no key value (idF018, idG006, idK012, idZ015).
func (s *Schema) fieldTypeOK(n *xmltree.Node) error {
	owner, attr := n, (*xmltree.Node)(nil)
	if n.Kind == xmltree.KindAttribute {
		owner, attr = n.Parent, n
	}
	if owner == nil || owner.Kind != xmltree.KindElement {
		return nil
	}
	typ := s.elementTypeOf(owner)
	if attr != nil {
		ct, ok := typ.(*ComplexType)
		if !ok {
			return nil // conservative: owner's type unknown or simple
		}
		an := nameOf(attr)
		uses, _ := s.effectiveAttrs(ct)
		for _, u := range uses {
			if u.decl.name == an {
				return nil // declared attribute — always simple-typed
			}
		}
		if _, ok := s.attributes[an]; ok {
			return nil // matched via a wildcard, assessed against its global decl
		}
		if an.Space == xsiNS || an.Space == xmlNS {
			return nil
		}
		return invalidf("cvc-identity-constraint.3", "field selects attribute %s, which has no declaration", an)
	}
	switch t := typ.(type) {
	case nil:
		return nil // undeclared element: no verdict, stay conservative
	case *SimpleType:
		return nil
	case *ComplexType:
		if simpleContentType(t) != nil {
			return nil
		}
		return invalidf("cvc-identity-constraint.3", "field selects element %s, whose type has no simple content", nameOf(owner))
	}
	return nil
}

// fieldKeyValue produces the comparison key for one identity-constraint field
// value. Values are compared in their type's VALUE space, so the value is
// canonicalised and prefixed with the type: boolean "1" and decimal "1" are
// distinct keys, while decimal "1.0" and "1.00" are the same one. Without a
// determinable type it falls back to the collapsed lexical string.
func (s *Schema) fieldKeyValue(node *xmltree.Node) string {
	st := s.declaredTypeOf(node)
	if st == nil {
		return applyWhiteSpace("collapse", nodeStringValue(node))
	}
	// The comparison value is the field's value AFTER the type's own whiteSpace
	// processing — xs:string preserves, so "  test  " and " test " are distinct
	// keys (idK003).
	raw := applyWhiteSpace(st.effectiveWhiteSpace(), nodeStringValue(node))
	switch st.variety {
	case vAtomic:
		if v, ok := canonAtomic(st, node, raw); ok {
			return v
		}
	case vList:
		// A list's key is its items' canonical forms joined — which makes a
		// singleton list equal to the same value held atomically, as XSD requires.
		if st.item == nil || st.item.variety != vAtomic {
			break
		}
		prim := xsdPrimitiveOf(st.item.resolvePrim())
		items := xmlFields(raw)
		vals := make([]string, 0, len(items))
		for _, it := range items {
			v, ok := canonAtomic(st.item, node, it)
			if !ok {
				return raw
			}
			vals = append(vals, strings.TrimPrefix(v, atomName(prim)+":"))
		}
		return atomName(prim) + ":" + strings.Join(vals, " ")
	}
	return raw
}

// xsdPrimitiveOf maps a built-in type to the XSD primitive at the root of its
// branch. Identity constraints compare values, and a value lives in its
// PRIMITIVE's value space: xs:decimal 1 and xs:unsignedByte 1 are the same key,
// as are xs:string "x" and xs:token "x".
func xsdPrimitiveOf(t xpath.AtomType) xpath.AtomType {
	switch t {
	case xpath.XSinteger, xpath.XSnonNegativeInteger, xpath.XSpositiveInteger,
		xpath.XSnonPositiveInteger, xpath.XSnegativeInteger, xpath.XSlong,
		xpath.XSint, xpath.XSshort, xpath.XSbyte, xpath.XSunsignedLong,
		xpath.XSunsignedInt, xpath.XSunsignedShort, xpath.XSunsignedByte:
		return xpath.XSdecimal
	case xpath.XSnormalizedString, xpath.XStoken, xpath.XSlanguage, xpath.XSname,
		xpath.XSncname, xpath.XSid, xpath.XSidref, xpath.XSentity, xpath.XSnmtoken:
		return xpath.XSstring
	case xpath.XSdateTimeStamp:
		return xpath.XSdateTime
	}
	return t
}

// canonAtomic renders one atomic value in its type's value space, prefixed with
// the type so values of different types cannot collide.
func canonAtomic(st *SimpleType, node *xmltree.Node, raw string) (string, bool) {
	prim := xsdPrimitiveOf(st.resolvePrim())
	if prim == xpath.XSqname || prim == xpath.XSnotation {
		// QName values are equal when their namespace and local part match; the
		// prefix carries no identity of its own.
		return atomName(prim) + ":" + resolveQName(node, raw).String(), true
	}
	if prim == xpath.XStime {
		// §3.3.8: the time value space maps 24:00:00 to 00:00:00 of the SAME
		// (arbitrary) reference date — normalize before casting so the dummy
		// date does not roll over (zone206.v02 vs .n02: 02:00+14:00 anchors a
		// day earlier than 12:00Z and must stay UNEQUAL).
		if t := strings.TrimSpace(raw); strings.HasPrefix(t, "24:") {
			raw = "00:" + t[3:]
		}
	}
	a, err := xpath.CastTo(xpath.NewString(raw), prim)
	if err != nil {
		return "", false
	}
	if dateTimePrim(prim) {
		// Key identity is on the TIMELINE for timezoned values (02:00:00-05:00
		// equals 07:00:00Z — zone206); values without a timezone live in their
		// own partition and never collide with timezoned ones.
		if tm, hasTZ := a.TimeValue(); hasTZ {
			return atomName(prim) + ":Z:" + tm.UTC().Format("2006-01-02T15:04:05.999999999"), true
		}
		return atomName(prim) + ":-:" + a.Lexical(), true
	}
	return atomName(prim) + ":" + a.Lexical(), true
}

// buildKeyTable evaluates a key/unique constraint at scope element el, checking
// uniqueness (and, for key, that every selected target has all fields).
func (s *Schema) buildKeyTable(ic *identityConstraint, el *xmltree.Node) (*keyTable, error) {
	targets, err := ic.evalNodes(ic.selector, el, ic.selNS)
	if err != nil {
		return nil, ErrUnsupported
	}
	targets = s.icFilter(targets)
	tbl := &keyTable{tuples: map[string]bool{}}
	for _, t := range targets {
		tuple, complete, err := s.tupleFor(ic, t)
		if err != nil {
			return nil, err
		}
		if !complete {
			if ic.kind == "key" {
				return nil, invalidf("cvc-identity-constraint.4.2.1", "key %s: a field is missing", ic.name)
			}
			continue // unique: incomplete tuples are not compared
		}
		if tbl.tuples[tuple] {
			return nil, invalidf("cvc-identity-constraint.4.1", "duplicate key/unique value for %s", ic.name)
		}
		tbl.tuples[tuple] = true
	}
	return tbl, nil
}

// checkKeyref verifies that each complete keyref tuple exists in the referenced
// key/unique table.
func (s *Schema) checkKeyref(ic *identityConstraint, el *xmltree.Node, scopes map[xname]*keyTable) error {
	ref := scopes[ic.refer]
	targets, err := ic.evalNodes(ic.selector, el, ic.selNS)
	if err != nil {
		return ErrUnsupported
	}
	targets = s.icFilter(targets)
	for _, t := range targets {
		tuple, complete, err := s.tupleFor(ic, t)
		if err != nil {
			return err
		}
		if !complete {
			continue
		}
		if ref == nil || !ref.tuples[tuple] {
			return invalidf("cvc-identity-constraint.4.3", "keyref %s has no matching key", ic.name)
		}
	}
	return nil
}

func nodeStringValue(n *xmltree.Node) string {
	if n.Kind == xmltree.KindAttribute {
		return n.Value
	}
	var b strings.Builder
	var walk func(*xmltree.Node)
	walk = func(x *xmltree.Node) {
		if x.Kind == xmltree.KindText {
			b.WriteString(x.Value)
		}
		for _, ch := range x.Children {
			walk(ch)
		}
	}
	walk(n)
	return b.String()
}

// declaredTypeOf returns the simple type an identity-constraint field's value is
// validated against. Key comparison happens in the type's VALUE space, so the
// type has to be known even when the instance carries no xsi:type: a field on
// two xs:decimal elements must see "1.0" and "1.00" as the same key.
//
// The type is found by walking from the document element down to n, resolving
// each step through its parent's content model, rather than being recorded
// during validation — identity constraints are evaluated at a scope element
// before its subtree has been walked, so nothing is recorded yet. Results are
// memoised on the per-call Schema copy.
func (s *Schema) declaredTypeOf(n *xmltree.Node) *SimpleType {
	owner, attr := n, (*xmltree.Node)(nil)
	if n.Kind == xmltree.KindAttribute {
		owner, attr = n.Parent, n
	}
	if owner == nil || owner.Kind != xmltree.KindElement {
		return nil
	}
	typ := s.elementTypeOf(owner)
	if typ == nil {
		return nil
	}
	if attr != nil {
		ct, ok := typ.(*ComplexType)
		if !ok {
			return nil
		}
		uses, _ := s.effectiveAttrs(ct)
		an := nameOf(attr)
		for _, u := range uses {
			if u.decl.name == an {
				return u.decl.typ
			}
		}
		return nil
	}
	return simpleContentType(typ)
}

// elementTypeOf resolves the type governing an element node, walking the
// ancestor chain down from the document element.
func (s *Schema) elementTypeOf(el *xmltree.Node) Type {
	if t, ok := s.typeCache[el]; ok {
		return t
	}
	nn := nameOf(el)
	var decl *ElementDecl
	if parent := el.Parent; parent != nil && parent.Kind == xmltree.KindElement {
		if pt, ok := s.elementTypeOf(parent).(*ComplexType); ok {
			decls := map[xname]*ElementDecl{}
			s.contentDecls(s.effectiveParticle(pt), decls)
			decl = decls[nn]
		}
	}
	if decl == nil {
		// The document element, a substitution-group member, or a child admitted
		// by a lax wildcard (whose own global declaration governs, as in
		// validateChildren) — and the same fallback keeps the walk going when an
		// ancestor is itself unresolvable.
		decl = s.elements[nn]
	}
	var typ Type
	if decl != nil {
		typ = decl.typ
	}
	// An xsi:type on the instance overrides the declared type.
	if q, ok := el.Attr(xsiNS, "type"); ok {
		if t, err := s.resolveInstanceType(el, q); err == nil {
			typ = t
		}
	}
	if s.typeCache != nil {
		s.typeCache[el] = typ
	}
	return typ
}
