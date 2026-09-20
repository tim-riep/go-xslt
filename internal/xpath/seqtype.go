package xpath

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// SeqType is a parsed SequenceType (an ItemType plus an occurrence indicator,
// or empty-sequence()).
type SeqType struct {
	Empty bool
	Occur byte // 0 (exactly one), '?', '*', '+'
	Item  ItemType
	// List marks one of the three built-in XSD LIST types (xs:IDREFS,
	// xs:NMTOKENS, xs:ENTITIES) as a cast/castable target: Item then carries
	// the list's ITEM type and the cast produces a sequence of those items,
	// not a single value. Only parseSingleType sets it.
	List bool
}

type itKind int

const (
	itItem     itKind = iota // item()
	itAtomic                 // an AtomicOrUnionType (xs:…)
	itNode                   // a KindTest
	itFunction               // function(*) / typed function test
	itMap                    // map(*) / map(K,V)
	itArray                  // array(*) / array(T)
)

// ItemType is a parsed ItemType.
type ItemType struct {
	Kind itKind
	Atom AtomType
	Node *NodeTest
	// Typed function/map/array parameters. A nil parameter set with the
	// corresponding Wild flag means the wildcard form (function(*), map(*), array(*)).
	Wild       bool       // function(*) / map(*) / array(*)
	FuncParams []*SeqType // function(p1,…,pn) as ret — parameter types
	FuncReturn *SeqType   // function return type
	MapKey     AtomType   // map(K,V) key atomic type
	MapVal     *SeqType   // map(K,V) value type
	ArrayItem  *SeqType   // array(T) member type
	// BareAtom marks an atomic type (Atom, or MapKey for BareKey) written as
	// an UNPREFIXED name ("integer"). Such a name is in the default
	// element/type namespace, so it denotes the built-in only when that
	// namespace is the XML Schema namespace — decided at evaluation time
	// (checkBareTypes): xpathDefaultNamespace="…XMLSchema" (XSD assert-
	// simple003, use-when-0118) vs. XPST0051 (MapTest-008).
	BareAtom bool
	BareKey  bool
	// AtomPrefix is the lexical prefix an atomic type name was written with
	// ("xs", "xsd", …; "" for unprefixed and braced names). The parser has
	// no namespace context, so binding is checked at evaluation time: an
	// unbound prefix is XPST0081 (K-SeqExprCast-8, K-SeqExprInstanceOf-49,
	// K-SeqExprTreat-8 — prefixDoesNotExist:integer).
	AtomPrefix string
	// SchemaType is an AtomicOrUnionType that named a USER-DEFINED simple
	// type in the in-scope schema components, resolved once at parse time by
	// the host's SchemaNameLookup (see schema_names.go). When set, matching
	// asks the schema whether the VALUE's own type is or derives from this
	// one, instead of comparing built-in primitives — which is the only way
	// `instance of my:hatsize` can mean anything.
	SchemaType *xmltree.SchemaTypeName
}

// --- parsing ----------------------------------------------------------------

// parseSequenceType parses SequenceType[79].
func (p *parser) parseSequenceType() (*SeqType, error) {
	if p.cur().kind == tName && p.cur().text == "empty-sequence" && p.peekKind(1) == tLParen {
		p.next()
		p.next()
		if _, err := p.expect(tRParen); err != nil {
			return nil, err
		}
		return &SeqType{Empty: true}, nil
	}
	it, err := p.parseItemType()
	if err != nil {
		return nil, err
	}
	st := &SeqType{Item: it}
	switch {
	case p.cur().kind == tQuestion:
		p.next()
		st.Occur = '?'
	case p.isStarTok(): // '*' may lex as tStar or tOp("*") depending on context
		p.next()
		st.Occur = '*'
	case p.cur().kind == tPlus:
		p.next()
		st.Occur = '+'
	}
	return st, nil
}

// parseSingleType parses SingleType (atomic-or-union type + optional '?') used
// by cast/castable.
func (p *parser) parseSingleType() (*SeqType, error) {
	name, err := p.expect(tName)
	if err != nil {
		return nil, err
	}
	// A PREFIXED name the imported schema really defines wins over
	// atomTypeForEQName's prefix-blind local-part reading (see
	// schemaTypeOverride) — `cast as xsl:QName` must mean the XSLT schema's
	// own Name-derived type, not the built-in xs:QName.
	if t, ok := p.schemaTypeOverride(name.text); ok {
		st := &SeqType{Item: ItemType{Kind: itAtomic, Atom: XSanyAtomicType,
			SchemaType: t, AtomPrefix: lexicalTypePrefix(name.text)}}
		if p.cur().kind == tQuestion {
			p.next()
			st.Occur = '?'
		}
		return st, nil
	}
	atom, bare, ok := atomTypeForEQName(name.text)
	list := false
	if !ok {
		// SingleType admits any simple type in scope, which for a
		// schema-unaware processor includes the three built-in LIST types
		// (castable-007/008/009: 'a b c' castable as xs:NMTOKENS).
		if atom, bare, ok = listTypeForEQName(name.text); ok {
			list = true
		} else if t, ok2 := p.lookupSchemaSimpleType(name.text); ok2 {
			// …and for a schema-aware one, any imported simple type as well
			// (XPath 3.1 §3.14: SingleType is an AtomicOrUnionType).
			st := &SeqType{Item: ItemType{Kind: itAtomic, Atom: XSanyAtomicType,
				SchemaType: t, AtomPrefix: lexicalTypePrefix(name.text)}}
			if p.cur().kind == tQuestion {
				p.next()
				st.Occur = '?'
			}
			return st, nil
		} else if p.schemaWithheld {
			return nil, fmt.Errorf("err:XTDE3160: %s names no type available to this expression", name.text)
		} else {
			return nil, fmt.Errorf("err:XPST0051: unknown atomic type %q", name.text)
		}
	}
	st := &SeqType{List: list, Item: ItemType{Kind: itAtomic, Atom: atom, BareAtom: bare, AtomPrefix: lexicalTypePrefix(name.text)}}
	if p.cur().kind == tQuestion {
		p.next()
		st.Occur = '?'
	}
	return st, nil
}

// parseItemType parses ItemType[81].
func (p *parser) parseItemType() (ItemType, error) {
	if p.cur().kind == tLParen {
		// ParenthesizedItemType
		p.next()
		it, err := p.parseItemType()
		if err != nil {
			return ItemType{}, err
		}
		if _, err := p.expect(tRParen); err != nil {
			return ItemType{}, err
		}
		return it, nil
	}
	if p.cur().kind != tName {
		return ItemType{}, fmt.Errorf("expected item type, got %q", p.cur().text)
	}
	name := p.cur().text
	switch name {
	case "item":
		if p.peekKind(1) == tLParen {
			p.next()
			p.next()
			if _, err := p.expect(tRParen); err != nil {
				return ItemType{}, err
			}
			return ItemType{Kind: itItem}, nil
		}
	case "node", "text", "comment", "processing-instruction", "element",
		"attribute", "document-node", "namespace-node", "schema-element",
		"schema-attribute":
		if p.peekKind(1) == tLParen {
			nt, err := p.parseKindTest()
			if err != nil {
				return ItemType{}, err
			}
			return ItemType{Kind: itNode, Node: &nt}, nil
		}
	case "function":
		if p.peekKind(1) == tLParen {
			return p.parseFunctionTest()
		}
	case "map":
		if p.peekKind(1) == tLParen {
			return p.parseMapTest()
		}
	case "array":
		if p.peekKind(1) == tLParen {
			return p.parseArrayTest()
		}
	}
	// AtomicOrUnionType (an EQName)
	p.next()
	// A PREFIXED name the imported schema really defines wins over
	// atomTypeForEQName's prefix-blind local-part reading — see
	// schemaTypeOverride.
	if t, ok := p.schemaTypeOverride(name); ok {
		return ItemType{Kind: itAtomic, Atom: XSanyAtomicType, SchemaType: t,
			AtomPrefix: lexicalTypePrefix(name)}, nil
	}
	atom, bare, ok := atomTypeForEQName(name)
	if !ok {
		// A name the IMPORTED schema components define is a real type, and
		// answers by derivation rather than by the primitive-only fallback
		// below. This is also what makes an UNPREFIXED user type work at all
		// (notation-0101/0102: a no-target-namespace schema's "nota"), which
		// the static-error branch would otherwise reject outright.
		if t, ok2 := p.lookupSchemaSimpleType(name); ok2 {
			return ItemType{Kind: itAtomic, Atom: XSanyAtomicType, SchemaType: t,
				AtomPrefix: lexicalTypePrefix(name)}, nil
		}
		// An unknown name in the XSD namespace, or an UNPREFIXED name that is
		// no built-in under any default namespace (K-SeqExprInstanceOf-52
		// "none"), is a static error (K-SeqExprInstanceOf-50 xs:doesNotExist,
		// -51 xs:qname wrong case); an unknown prefixed USER type stays
		// lenient (no user type table).
		if isXSDName(name) || !strings.ContainsAny(name, ":{") {
			return ItemType{}, fmt.Errorf("err:XPST0051: unknown type %q", name)
		}
		if p.schemaWithheld {
			return ItemType{}, fmt.Errorf("err:XTDE3160: %s names no type available to this expression", name)
		}
		return ItemType{Kind: itAtomic, Atom: XSanyAtomicType, AtomPrefix: lexicalTypePrefix(name)}, nil
	}
	return ItemType{Kind: itAtomic, Atom: atom, BareAtom: bare, AtomPrefix: lexicalTypePrefix(name)}, nil
}

// lexicalTypePrefix returns the prefix of a prefixed (non-braced) type name.
func lexicalTypePrefix(name string) string {
	if strings.HasPrefix(name, "Q{") {
		return ""
	}
	if i := strings.IndexByte(name, ':'); i > 0 {
		return name[:i]
	}
	return ""
}

// checkTypePrefixes reports XPST0081 for a prefixed atomic type name whose
// prefix has no namespace binding in the static context (K-SeqExprCast-8).
// The conventional xs prefix and the standard prefixes are always bound; with
// no namespace resolver at all nothing can be checked.
func checkTypePrefixes(st *SeqType, ctx *Context) error {
	if st == nil || st.Empty || ctx == nil || ctx.NS == nil {
		return nil
	}
	return itemTypePrefixErr(st.Item, ctx)
}

func itemTypePrefixErr(it ItemType, ctx *Context) error {
	if p := it.AtomPrefix; p != "" && p != "xs" {
		if _, ok := nsForKnownPrefix(p); !ok {
			if _, ok := ctx.NS.ResolveNS(p); !ok {
				return fmt.Errorf("err:XPST0081: prefix %q of the type name has no namespace binding", p)
			}
		}
	}
	for _, fp := range it.FuncParams {
		if err := checkTypePrefixes(fp, ctx); err != nil {
			return err
		}
	}
	for _, sub := range []*SeqType{it.FuncReturn, it.MapVal, it.ArrayItem} {
		if err := checkTypePrefixes(sub, ctx); err != nil {
			return err
		}
	}
	return nil
}

// isXSDName reports whether an EQName is lexically in the XML Schema
// namespace (the conventional xs: prefix or the braced URI form).
func isXSDName(name string) bool {
	return strings.HasPrefix(name, "xs:") || strings.HasPrefix(name, "Q{http://www.w3.org/2001/XMLSchema}")
}

// atomTypeForEQName resolves a type EQName to a built-in atomic type. A
// prefixed name resolves by local name (the parser holds no namespace
// context and stylesheets commonly bind xsd:); a braced name must carry the
// XSD URI; an UNPREFIXED name resolves too but is reported bare — it only
// denotes the built-in when the default type namespace is XSD, which
// checkBareTypes decides at evaluation time. xs:error is the valid type with
// an empty value space (xs-error-015/016).
func atomTypeForEQName(name string) (atom AtomType, bare, ok bool) {
	if uri, local, ok := parseBracedName(name); ok {
		if uri != nsXS {
			return 0, false, false
		}
		atom, ok = AtomTypeByName(local)
		return atom, false, ok
	}
	if !strings.Contains(name, ":") {
		atom, ok = AtomTypeByName(name)
		return atom, true, ok
	}
	atom, ok = AtomTypeByName(localOfEQName(name))
	return atom, false, ok
}

// listTypeForEQName is atomTypeForEQName for the three built-in XSD LIST
// types, returning the list's ITEM type (xs:IDREFS -> xs:IDREF, and so on).
func listTypeForEQName(name string) (item AtomType, bare, ok bool) {
	if uri, local, braced := parseBracedName(name); braced {
		if uri != nsXS {
			return 0, false, false
		}
		item, ok = xsListItemType(local)
		return item, false, ok
	}
	if !strings.Contains(name, ":") {
		item, ok = xsListItemType(name)
		return item, true, ok
	}
	item, ok = xsListItemType(localOfEQName(name))
	return item, false, ok
}

// seqTypeHasBare reports whether a SequenceType mentions an unprefixed
// atomic type name anywhere (including nested function/map/array types).
func seqTypeHasBare(st *SeqType) bool {
	if st == nil || st.Empty {
		return false
	}
	return itemTypeHasBare(st.Item)
}

func itemTypeHasBare(it ItemType) bool {
	if it.BareAtom || it.BareKey {
		return true
	}
	for _, p := range it.FuncParams {
		if seqTypeHasBare(p) {
			return true
		}
	}
	return seqTypeHasBare(it.FuncReturn) || seqTypeHasBare(it.MapVal) || seqTypeHasBare(it.ArrayItem)
}

// defaultTypeNSIsXSD reports whether the static context's default
// element/type namespace is the XML Schema namespace, which makes unprefixed
// built-in type names legal.
func defaultTypeNSIsXSD(ctx *Context) bool {
	if ctx == nil {
		return false
	}
	if ctx.DefaultElemNS == nsXS {
		return true
	}
	if ctx.NS != nil {
		if uri, ok := ctx.NS.ResolveNS(""); ok && uri == nsXS {
			return true
		}
	}
	return false
}

// checkBareTypes raises XPST0051 for a SequenceType that names a built-in
// atomic type without a prefix when the default type namespace is not XSD
// (MapTest-008 map(integer, string); use-when-0118 and XSD assert-simple003
// legitimately rely on an XSD default namespace).
func checkBareTypes(st *SeqType, ctx *Context) error {
	if seqTypeHasBare(st) && !defaultTypeNSIsXSD(ctx) {
		return fmt.Errorf("err:XPST0051: unprefixed type name is not in the XML Schema namespace")
	}
	return checkTypePrefixes(st, ctx)
}

// parseFunctionTest parses `function(*)` or `function(t1,…,tn) as ret`. The
// keyword "function" is the current token.
func (p *parser) parseFunctionTest() (ItemType, error) {
	p.next() // consume "function"
	if _, err := p.expect(tLParen); err != nil {
		return ItemType{}, err
	}
	it := ItemType{Kind: itFunction}
	if p.isStarTok() {
		p.next()
		it.Wild = true
		if _, err := p.expect(tRParen); err != nil {
			return ItemType{}, err
		}
		return it, nil
	}
	if p.cur().kind != tRParen {
		for {
			pt, err := p.parseSequenceType()
			if err != nil {
				return ItemType{}, err
			}
			it.FuncParams = append(it.FuncParams, pt)
			if p.cur().kind != tComma {
				break
			}
			p.next()
		}
	}
	if _, err := p.expect(tRParen); err != nil {
		return ItemType{}, err
	}
	if !p.isKeyword("as") {
		return ItemType{}, fmt.Errorf("expected 'as' in function type")
	}
	p.next()
	rt, err := p.parseSequenceType()
	if err != nil {
		return ItemType{}, err
	}
	it.FuncReturn = rt
	return it, nil
}

// parseMapTest parses `map(*)` or `map(K, V)`. "map" is the current token.
func (p *parser) parseMapTest() (ItemType, error) {
	p.next() // consume "map"
	if _, err := p.expect(tLParen); err != nil {
		return ItemType{}, err
	}
	it := ItemType{Kind: itMap}
	if p.isStarTok() {
		p.next()
		it.Wild = true
		_, err := p.expect(tRParen)
		return it, err
	}
	key, err := p.expect(tName)
	if err != nil {
		return ItemType{}, err
	}
	atom, bare, ok := atomTypeForEQName(key.text)
	if !ok {
		if isXSDName(key.text) || !strings.ContainsAny(key.text, ":{") {
			return ItemType{}, fmt.Errorf("err:XPST0051: unknown type %q", key.text)
		}
		atom = XSanyAtomicType
	}
	it.MapKey = atom
	it.BareKey = bare
	if _, err := p.expect(tComma); err != nil {
		return ItemType{}, err
	}
	val, err := p.parseSequenceType()
	if err != nil {
		return ItemType{}, err
	}
	it.MapVal = val
	_, err = p.expect(tRParen)
	return it, err
}

// parseArrayTest parses `array(*)` or `array(T)`. "array" is the current token.
func (p *parser) parseArrayTest() (ItemType, error) {
	p.next() // consume "array"
	if _, err := p.expect(tLParen); err != nil {
		return ItemType{}, err
	}
	it := ItemType{Kind: itArray}
	if p.isStarTok() {
		p.next()
		it.Wild = true
		_, err := p.expect(tRParen)
		return it, err
	}
	mt, err := p.parseSequenceType()
	if err != nil {
		return ItemType{}, err
	}
	it.ArrayItem = mt
	_, err = p.expect(tRParen)
	return it, err
}

// --- matching ---------------------------------------------------------------

// MatchesSeqType reports whether a value matches a SequenceType. Prefixed
// names in element()/attribute() kind tests cannot be resolved without a
// context; use MatchesSeqTypeCtx where one is available.
func MatchesSeqType(st *SeqType, o Object) bool {
	return MatchesSeqTypeCtx(st, o, nil)
}

// MatchesSeqTypeCtx is MatchesSeqType with the evaluation context, whose
// in-scope namespaces resolve prefixed kind-test names (instance of
// document-node(element(j:map)) — json-to-xml-008/012).
func MatchesSeqTypeCtx(st *SeqType, o Object, ctx *Context) bool {
	if ctx == nil {
		ctx = &Context{}
	}
	items := Items(o)
	if st.Empty {
		return len(items) == 0
	}
	switch st.Occur {
	case 0:
		if len(items) != 1 {
			return false
		}
	case '?':
		if len(items) > 1 {
			return false
		}
	case '+':
		if len(items) < 1 {
			return false
		}
	case '*':
	}
	for _, it := range items {
		if !matchesItemType(st.Item, it, ctx) {
			return false
		}
	}
	return true
}

func matchesItemType(it ItemType, item Item, ctx *Context) bool {
	switch it.Kind {
	case itItem:
		return true
	case itAtomic:
		t, ok := itemAtomType(item)
		if !ok {
			return false
		}
		if it.SchemaType != nil {
			// A USER-DEFINED type: the value must carry a schema type of its
			// own that is, or derives from, this one. A value with no schema
			// identity (anything a schema never validated) matches nothing
			// here — the fail-closed answer, and the correct one: an
			// xs:integer is not an instance of my:hatsize merely because
			// hatsize restricts xs:integer.
			a, isAtomic := item.(*Atomic)
			if !isAtomic {
				return false
			}
			got := a.st
			if got == nil {
				// No USER-defined identity, but the value still has a type:
				// the built-in one T records. That is enough to satisfy a
				// UNION whose members are built-ins (evaluate-032:
				// current-date() instance of a union of xs:date/xs:time/
				// xs:dateTime), and correctly not enough for a named type
				// that merely RESTRICTS a built-in — an xs:integer is not an
				// instance of my:hatsize.
				if a.T == 0 {
					return false
				}
				got = &xmltree.SchemaTypeName{Namespace: nsXS,
					Local: strings.TrimPrefix(a.T.String(), "xs:")}
			}
			if *got == *it.SchemaType {
				return true
			}
			if ctx == nil || ctx.SchemaTypes == nil {
				return false
			}
			return ctx.SchemaTypes.DerivesFrom(*got, *it.SchemaType)
		}
		return it.Atom == XSanyAtomicType || atomDerivesFrom(t, it.Atom)
	case itNode:
		n, ok := item.(*xmltree.Node)
		if !ok {
			return false
		}
		ok2, _ := matchTest(*it.Node, "child", n, ctx)
		// For attribute kind tests the axis matters; matchTest checks kind+name.
		if it.Node.Kind == testAttribute || it.Node.Kind == testSchemaAttr {
			ok2, _ = matchTest(*it.Node, "attribute", n, ctx)
		}
		return ok2
	case itFunction:
		// Maps and arrays ARE function items: function(*) matches them, and a
		// typed function test matches them by the XPath 3.1 subtyping rules
		// (ArrayTest-042/043, MapTest-059..066).
		if it.Wild || it.FuncReturn == nil {
			switch item.(type) {
			case *Map, *Array, *Function:
				return true
			}
			return false
		}
		switch v := item.(type) {
		case *Map:
			// map(K, V) is a function(xs:anyAtomicType) as V?: the test's
			// single parameter must be an atomic type (contravariance), and
			// V? — every entry value, and the empty sequence — must match
			// the test's return type (MapTest-059 true, -061/-062/-066 false).
			if len(it.FuncParams) != 1 || !subtypeSeq(it.FuncParams[0], atomicOneType) {
				return false
			}
			if !MatchesSeqTypeCtx(it.FuncReturn, Sequence{}, ctx) {
				return false
			}
			for _, k := range v.Keys() {
				if !MatchesSeqTypeCtx(it.FuncReturn, v.Get(k), ctx) {
					return false
				}
			}
			return true
		case *Array:
			// array(T) is a function(xs:integer) as T: the parameter must be
			// xs:integer or a subtype; every member must match the return type.
			if len(it.FuncParams) != 1 || !subtypeSeq(it.FuncParams[0], integerOneType) {
				return false
			}
			for _, m := range v.Members() {
				if !MatchesSeqTypeCtx(it.FuncReturn, m, ctx) {
					return false
				}
			}
			return true
		case *Function:
			if v.Arity != len(it.FuncParams) {
				return false
			}
			if !v.Typed {
				// A built-in function item carries no declared signature;
				// match structurally on arity (lenient on types).
				return true
			}
			// Declared signature: function(Pa…) as Ra ⊆ function(Pb…) as Rb
			// iff every Pb ⊆ Pa (contravariant) and Ra ⊆ Rb (MapTest-040..
			// 054, function-item-13..17, inline-fn-032/033).
			for i, pb := range it.FuncParams {
				var pa *SeqType
				if i < len(v.Params) {
					pa = v.Params[i]
				}
				if !subtypeSeq(pb, pa) {
					return false
				}
			}
			return subtypeSeq(v.Ret, it.FuncReturn)
		}
		return false
	case itMap:
		m, ok := item.(*Map)
		if !ok {
			return false
		}
		if it.Wild || it.MapVal == nil {
			return true
		}
		for _, k := range m.Keys() {
			kt, ok := itemAtomType(k)
			if !ok || !atomDerivesFrom(kt, it.MapKey) {
				return false
			}
			if !MatchesSeqTypeCtx(it.MapVal, m.Get(k), ctx) {
				return false
			}
		}
		return true
	case itArray:
		a, ok := item.(*Array)
		if !ok {
			return false
		}
		if it.Wild || it.ArrayItem == nil {
			return true
		}
		for _, mem := range a.Members() {
			if !MatchesSeqTypeCtx(it.ArrayItem, mem, ctx) {
				return false
			}
		}
		return true
	}
	return false
}

// seqTypeIsItemStar reports whether st is exactly item()* — the only return
// type as permissive as the intrinsic map/array function signatures.
func seqTypeIsItemStar(st *SeqType) bool {
	return st != nil && !st.Empty && st.Occur == '*' && st.Item.Kind == itItem
}

// The intrinsic parameter types of maps (xs:anyAtomicType) and arrays
// (xs:integer) as function items, and item()* for an undeclared type.
var (
	atomicOneType  = &SeqType{Item: ItemType{Kind: itAtomic, Atom: XSanyAtomicType}}
	integerOneType = &SeqType{Item: ItemType{Kind: itAtomic, Atom: XSinteger}}
	itemStarType   = &SeqType{Occur: '*', Item: ItemType{Kind: itItem}}
)

// occurRange gives the cardinality bounds of an occurrence indicator
// (max -1 = unbounded).
func occurRange(o byte) (min, max int) {
	switch o {
	case '?':
		return 0, 1
	case '*':
		return 0, -1
	case '+':
		return 1, -1
	}
	return 1, 1
}

// subtypeSeq implements the XPath 3.1 §2.5.6.1 judgement "A is a subtype of
// B" on SequenceTypes: A's cardinality range lies within B's and A's item type
// is a subtype of B's. A nil SeqType is an undeclared type, item()*.
func subtypeSeq(a, b *SeqType) bool {
	if a == nil {
		a = itemStarType
	}
	if b == nil {
		b = itemStarType
	}
	if a.Empty {
		return b.Empty || b.Occur == '?' || b.Occur == '*'
	}
	if b.Empty {
		return false
	}
	amin, amax := occurRange(a.Occur)
	bmin, bmax := occurRange(b.Occur)
	if amin < bmin || (bmax != -1 && (amax == -1 || amax > bmax)) {
		return false
	}
	return subtypeItem(a.Item, b.Item)
}

// mapAsFunctionType gives the function signature a map type stands for:
// map(K, V) is function(xs:anyAtomicType) as V?; map(*) is
// function(xs:anyAtomicType) as item()*.
func mapAsFunctionType(m ItemType) ItemType {
	ret := itemStarType
	if !m.Wild && m.MapVal != nil {
		v := *m.MapVal
		switch v.Occur {
		case 0:
			v.Occur = '?'
		case '+':
			v.Occur = '*'
		}
		ret = &v
	}
	return ItemType{Kind: itFunction, FuncParams: []*SeqType{atomicOneType}, FuncReturn: ret}
}

// arrayAsFunctionType gives the function signature an array type stands for:
// array(T) is function(xs:integer) as T; array(*) is function(xs:integer) as
// item()*.
func arrayAsFunctionType(a ItemType) ItemType {
	ret := itemStarType
	if !a.Wild && a.ArrayItem != nil {
		ret = a.ArrayItem
	}
	return ItemType{Kind: itFunction, FuncParams: []*SeqType{integerOneType}, FuncReturn: ret}
}

// subtypeItem implements the §2.5.6.2 judgement "Ai is a subtype of Bi" on
// ItemTypes.
func subtypeItem(a, b ItemType) bool {
	if b.Kind == itItem {
		return true
	}
	switch b.Kind {
	case itAtomic:
		if a.Kind != itAtomic {
			return false
		}
		switch {
		case a.Atom == XSerror:
			return true // empty value space: a subtype of every atomic type
		case b.Atom == XSerror:
			return false
		case a.Atom == XSnumeric:
			return b.Atom == XSnumeric || b.Atom == XSanyAtomicType
		}
		return atomDerivesFrom(a.Atom, b.Atom)
	case itNode:
		if a.Kind != itNode || a.Node == nil || b.Node == nil {
			return false
		}
		if b.Node.Kind == testNode {
			return true
		}
		if a.Node.Kind != b.Node.Kind {
			return false
		}
		if b.Node.Kind == testDocument {
			if b.Node.Inner == nil {
				return true
			}
			return a.Node.Inner != nil && subtypeItem(ItemType{Kind: itNode, Node: a.Node.Inner}, ItemType{Kind: itNode, Node: b.Node.Inner})
		}
		// element(N, T) demands a SCHEMA TYPE annotation. This processor is not
		// schema-aware, so no declared type it can see carries one: a test that
		// names a type is never satisfied by one that does not
		// (higher-order-functions-034's last three sub-cases — element(e,
		// xs:anyType)*, element(*, xs:anyType)?, element(*, xs:untyped)? —
		// are all false for a function declared as="element(e)?").
		if b.Node.TypeName != "" && a.Node.TypeName == "" {
			return false
		}
		if b.Node.AnyName || b.Node.Local == "" {
			return true
		}
		return a.Node.Local == b.Node.Local && a.Node.Prefix == b.Node.Prefix && a.Node.URI == b.Node.URI
	case itFunction:
		switch a.Kind {
		case itMap:
			a = mapAsFunctionType(a)
		case itArray:
			a = arrayAsFunctionType(a)
		case itFunction:
		default:
			return false
		}
		if b.Wild || b.FuncReturn == nil {
			return true
		}
		if a.Wild || a.FuncReturn == nil || len(a.FuncParams) != len(b.FuncParams) {
			return false
		}
		for i := range a.FuncParams {
			if !subtypeSeq(b.FuncParams[i], a.FuncParams[i]) {
				return false
			}
		}
		return subtypeSeq(a.FuncReturn, b.FuncReturn)
	case itMap:
		if a.Kind != itMap {
			return false
		}
		if b.Wild || b.MapVal == nil {
			return true
		}
		if a.Wild || a.MapVal == nil {
			return b.MapKey == XSanyAtomicType && subtypeSeq(itemStarType, b.MapVal)
		}
		return atomDerivesFrom(a.MapKey, b.MapKey) && subtypeSeq(a.MapVal, b.MapVal)
	case itArray:
		if a.Kind != itArray {
			return false
		}
		if b.Wild || b.ArrayItem == nil {
			return true
		}
		if a.Wild || a.ArrayItem == nil {
			return subtypeSeq(itemStarType, b.ArrayItem)
		}
		return subtypeSeq(a.ArrayItem, b.ArrayItem)
	}
	return false
}

// convertForParam applies the XPath function conversion rules to coerce an
// argument to a declared parameter SeqType: atomize a node/array to atomics,
// promote a numeric to a wider numeric target, and cast untypedAtomic to the
// target atomic type. It returns the converted value and whether it now
// matches the type (inline-function-5: integer -> xs:double).
// CoerceToDeclaredType applies the function-conversion rules to coerce value
// to the sequence type written as `asType` (e.g. "xs:integer", "xs:double*").
// It reuses the parser + convertForParam; on an unparsable type or a value
// that cannot be coerced it returns the value unchanged (best-effort — the
// XSLT host is lenient where a strict engine would raise XPTY0004). ok
// reports whether the coercion produced a value MATCHING the type.
func CoerceToDeclaredType(asType string, value Object) (Object, bool) {
	return CoerceToDeclaredTypeCtx(asType, value, nil)
}

// SeqTypeWantsNodes reports whether a declared sequence type's item type is
// specifically a node kind test (text(), node(), element(), …) — as opposed
// to item(), an atomic type, or function/map/array. A host (xsl:function
// @as, …) that materializes RTF/text CONTENT rather than a genuine node needs
// this to decide how to present that content for coercion: an actual node
// (or nodes) so a node()/text() target can match structurally, or collapsed
// to one xs:untypedAtomic value — which item()/an atomic type both accept
// under the function conversion rules (item() trivially, an atomic type via
// the untypedAtomic->target cast) — for every other target (avt-1205/
// doe-0184/static-032/whitespace-025 declare text()*/node()/text() and need
// real nodes; function-1001/math-3311 declare xs:integer and as-0146/
// seqtor-024 declare an atomic type / item()+, both fine collapsed).
func SeqTypeWantsNodes(asType string) bool {
	toks, err := lex(asType)
	if err != nil {
		return false
	}
	p := &parser{toks: toks}
	st, err := p.parseSequenceType()
	if err != nil || p.cur().kind != tEOF || st.Empty {
		return false
	}
	return st.Item.Kind == itNode
}

// CoerceToDeclaredTypeCtx is CoerceToDeclaredType with an evaluation context
// whose in-scope namespaces resolve a prefixed kind-test name in asType (e.g.
// element(my:item)) — needed when the declared type comes from a host
// attribute (xsl:template/@as, …) that can name a non-built-in element via a
// stylesheet-bound prefix (as-0130: element(my:item)*).
func CoerceToDeclaredTypeCtx(asType string, value Object, ctx *Context) (Object, bool) {
	toks, err := lex(asType)
	if err != nil {
		return value, false
	}
	// A declared type reaches here as unparsed SOURCE (the host keeps @as as a
	// string and parses it on demand), so this is where its schema component
	// names get resolved — through the same injected lookup a compile-time
	// parse uses, carried on the Context (schema_names.go).
	p := &parser{toks: toks, schema: schemaLookupOf(ctx)}
	st, err := p.parseSequenceType()
	if err != nil || p.cur().kind != tEOF {
		return value, false
	}
	if MatchesSeqTypeCtx(st, value, ctx) {
		return value, true
	}
	cv, ok := convertForParamCtx(st, value, ctx)
	return cv, ok
}

func convertForParam(st *SeqType, av Object) (Object, bool) {
	return convertForParamCtx(st, av, nil)
}

func convertForParamCtx(st *SeqType, av Object, ctx *Context) (Object, bool) {
	if MatchesSeqTypeCtx(st, av, ctx) {
		return av, true
	}
	if st.Item.Kind == itFunction && !st.Empty {
		// Function coercion (XPath 3.1 §3.1.5.3): a function item whose
		// arity matches the expected function type is accepted — its
		// declared parameter/return types are checked when it is CALLED, not
		// here (fold-left-008 passes function(element(employee)) as
		// xs:integer where function(item()) as xs:anyAtomicType is expected).
		items := Items(av)
		switch n := len(items); st.Occur {
		case 0:
			if n != 1 {
				return av, false
			}
		case '?':
			if n > 1 {
				return av, false
			}
		case '+':
			if n < 1 {
				return av, false
			}
		}
		out := make([]Item, 0, len(items))
		for _, it := range items {
			arity := -1
			var actual ItemType
			switch v := it.(type) {
			case *Function:
				arity = v.Arity
				actual = ItemType{Kind: itFunction, FuncParams: v.Params, FuncReturn: v.Ret}
				if !v.Typed {
					actual.Wild = true
				}
			case *Map:
				arity, actual = 1, ItemType{Kind: itMap, Wild: true}
			case *Array:
				arity, actual = 1, ItemType{Kind: itArray, Wild: true}
			}
			if arity < 0 || (!st.Item.Wild && st.Item.FuncReturn != nil && arity != len(st.Item.FuncParams)) {
				return av, false
			}
			// §3.1.5.3 rule 3: when the supplied function's type is not a
			// subtype of the expected function type, the value is a NEW
			// function that converts each argument to the supplied function's
			// declared parameter type and its result to the EXPECTED return
			// type, raising XPTY0004 when either conversion fails. Deferring
			// the check to call time (rather than rejecting here) is what
			// keeps a merely-lenient signature working while a genuinely
			// incompatible one still errors on real values
			// (higher-order-functions-060: string-length#1 bound to
			// function(xs:string) as xs:string; -066: an xs:float-typed
			// function bound to function(xs:double) as xs:double).
			if fn, isFn := it.(*Function); isFn && !st.Item.Wild && st.Item.FuncReturn != nil &&
				!subtypeItem(actual, st.Item) {
				out = append(out, coerceFunctionItem(fn, st.Item, ctx))
				continue
			}
			out = append(out, it)
		}
		return FromItems(out), true
	}
	if st.Item.Kind != itAtomic {
		return av, false
	}
	target := st.Item.Atom
	items := Items(av)
	// Atomize FIRST, then convert each resulting atomic item on its own. A
	// node's typed value is not necessarily a single item: a LIST-typed node
	// yields one item per whitespace-separated token (see nodeTypedItems), and
	// requiring exactly one — as this loop used to — rejected the whole value
	// outright (import-schema-020's attribute(*, xs:NMTOKENS),
	// validation-0301's xs:decimal* over an xs:list of xs:decimal).
	atoms := make([]Item, 0, len(items))
	for _, it := range items {
		if _, isAtom := it.(*Atomic); isAtom {
			atoms = append(atoms, it)
			continue
		}
		sub, err := Atomize(FromItems([]Item{it}))
		if err != nil {
			return av, false
		}
		atoms = append(atoms, sub...)
	}
	out := make([]Item, 0, len(atoms))
	for _, ai := range atoms {
		a, ok := ai.(*Atomic)
		if !ok {
			return av, false
		}
		switch {
		case st.Item.SchemaType != nil:
			// Function conversion rule 4 against a USER-DEFINED type: an
			// xs:untypedAtomic item (everything read out of an untyped
			// document) is cast to the required type, which for a named
			// simple type means validating it against that type's facets —
			// as-2001/2002/2401 declare as="my:partNumberType" over exactly
			// such a value and require NO type error.
			if a.T != XSuntypedAtomic {
				out = append(out, a)
				break
			}
			cv, err := castToNamedSchemaType(a, *st.Item.SchemaType, ctx)
			if err != nil {
				return av, false
			}
			ca, ok := cv.(*Atomic)
			if !ok {
				return av, false
			}
			out = append(out, ca)
		case a.T == XSuntypedAtomic && target == XSanyAtomicType:
			// Function conversion rule 4 (XPath 3.1 §3.1.5.3) only casts an
			// xs:untypedAtomic item up to the required type when that type is
			// something OTHER than xs:anyAtomicType/xs:untypedAtomic itself —
			// xs:untypedAtomic is already an instance of xs:anyAtomicType, so
			// no cast is applied (and none could be: XSPT0080 forbids casting
			// to the abstract xs:anyAtomicType). as-0107/0108/0109: a
			// heterogeneous atomic sequence declared @as="xs:anyAtomicType*"
			// must pass through unchanged, not fail as an invalid cast.
			out = append(out, a)
		case a.T == XSuntypedAtomic:
			c, err := CastTo(a, target)
			if err != nil {
				return av, false
			}
			out = append(out, c)
		case a.IsNumeric() && target == XSdouble:
			// Numeric type promotion (XPath 3.1 §3.1.5.3 rule 2/3):
			// xs:decimal (incl. xs:integer) and xs:float both promote to
			// xs:double automatically.
			c, err := CastTo(a, target)
			if err != nil {
				return av, false
			}
			out = append(out, c)
		case a.IsNumeric() && target == XSfloat && a.T != XSdouble:
			// Promotion to xs:float only comes from xs:decimal/xs:integer —
			// NOT from xs:double, which is wider: an xs:double value does not
			// implicitly narrow to xs:float under the function conversion
			// rules (type-0174/0175: a param declared as="xs:float" supplied
			// an xs:double must be a type error, not a silent narrowing).
			c, err := CastTo(a, target)
			if err != nil {
				return av, false
			}
			out = append(out, c)
		case (isStringType(a.T) || a.T == XSanyURI) && target == XSstring:
			out = append(out, NewString(a.Lexical()))
		default:
			out = append(out, a)
		}
	}
	conv := FromItems(out)
	if MatchesSeqTypeCtx(st, conv, ctx) {
		return conv, true
	}
	return av, false
}

// atomParent maps each derived atomic type to its immediate base type, forming
// the XSD type-derivation chains (string family, integer tower, durations, …).
// Types not present derive directly from xs:anyAtomicType.
var atomParent = map[AtomType]AtomType{
	XSnormalizedString:   XSstring,
	XStoken:              XSnormalizedString,
	XSlanguage:           XStoken,
	XSnmtoken:            XStoken,
	XSname:               XStoken,
	XSncname:             XSname,
	XSid:                 XSncname,
	XSidref:              XSncname,
	XSentity:             XSncname,
	XSinteger:            XSdecimal,
	XSnonNegativeInteger: XSinteger,
	XSpositiveInteger:    XSnonNegativeInteger,
	XSnonPositiveInteger: XSinteger,
	XSnegativeInteger:    XSnonPositiveInteger,
	XSlong:               XSinteger,
	XSint:                XSlong,
	XSshort:              XSint,
	XSbyte:               XSshort,
	XSunsignedLong:       XSnonNegativeInteger,
	XSunsignedInt:        XSunsignedLong,
	XSunsignedShort:      XSunsignedInt,
	XSunsignedByte:       XSunsignedShort,
	XSyearMonthDuration:  XSduration,
	XSdayTimeDuration:    XSduration,
	XSdateTimeStamp:      XSdateTime,
}

// atomDerivesFrom reports whether type t is base or transitively derived from it,
// AtomDerivesFrom reports whether atomic type t is derived (transitively) from
// base in the XSD type hierarchy. Exported for the schema validator.
func AtomDerivesFrom(t, base AtomType) bool { return atomDerivesFrom(t, base) }

// walking the XSD derivation chain in atomParent.
func atomDerivesFrom(t, base AtomType) bool {
	if base == XSanyAtomicType {
		return true
	}
	if base == XSnumeric { // the numeric union matches any numeric member
		return isNumericType(t)
	}
	if base == XSerror { // empty value space: nothing is an xs:error
		return false
	}
	for c := t; ; {
		if c == base {
			return true
		}
		parent, ok := atomParent[c]
		if !ok {
			return false
		}
		c = parent
	}
}

func localOfEQName(s string) string {
	// Q{uri}local
	if len(s) > 1 && s[0] == 'Q' && s[1] == '{' {
		if i := indexByteFrom(s, '}', 2); i >= 0 {
			return s[i+1:]
		}
	}
	if i := lastColon(s); i >= 0 {
		return s[i+1:]
	}
	return s
}

func indexByteFrom(s string, b byte, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// ParseItemTypeStrict parses an ItemType — a SequenceType with NO occurrence
// indicator — as required by host attributes that admit exactly one item type
// (xsl:context-item/@as). It rejects, as static errors, everything a
// non-schema-aware processor cannot honour: a trailing occurrence indicator,
// trailing junk, an unbound type prefix, and a prefixed atomic type name that
// names no built-in (there are no in-scope schema components to define one).
func ParseItemTypeStrict(s string, ctx *Context) (*SeqType, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, schema: schemaLookupOf(ctx)}
	st, err := p.parseSequenceType()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tEOF {
		return nil, fmt.Errorf("unexpected %q after the item type", p.cur().text)
	}
	if st.Empty || st.Occur != 0 {
		return nil, fmt.Errorf("an occurrence indicator is not allowed on an item type")
	}
	if ctx != nil {
		if err := checkTypePrefixes(st, ctx); err != nil {
			return nil, err
		}
	}
	// parseItemType is deliberately lenient about a PREFIXED user type name:
	// with no user type table it degrades to xs:anyAtomicType, which would
	// then match anything. A host that must reject an undefined type asks for
	// the strict reading here.
	if it := st.Item; it.Kind == itAtomic && it.Atom == XSanyAtomicType &&
		it.AtomPrefix != "" && it.AtomPrefix != "xs" {
		return nil, fmt.Errorf("err:XPST0051: %q is not a known type", s)
	}
	return st, nil
}

// MatchesItemTypeOf reports whether one item matches a parsed single-item
// SequenceType, with no function-conversion or atomization applied — the
// "instance of" relation, which is what xsl:context-item/@as tests
// (context-item-005: an element context item does NOT satisfy xs:string).
func MatchesItemTypeOf(st *SeqType, item Item, ctx *Context) bool {
	if st == nil {
		return true
	}
	if ctx == nil {
		ctx = &Context{}
	}
	return matchesItemType(st.Item, item, ctx)
}

// ContextItemValue returns the actual XDM item that a host context NODE stands
// for. Most nodes stand for themselves; a RealItem carrier stands for the
// map/array/function it carries, and a SynthCtx text node stamped with a
// TypeAnno stands for that typed atomic value — the same unwrapping "." itself
// performs (see ContextItemExpr in eval.go).
func ContextItemValue(n *xmltree.Node) Item {
	if n == nil {
		return nil
	}
	if n.RealItem != nil {
		return n.RealItem
	}
	if n.SynthCtx {
		if a, ok := nodeTypedValue(n); ok {
			return a
		}
	}
	return n
}

// ParseSeqTypeString parses a SequenceType from its source text, for a HOST
// that carries declared types as strings (xsl:function/@as, xsl:param/@as) and
// needs them as parsed types — e.g. to give a named-function reference to an
// xsl:function the declared signature a typed function test judges it by.
// An empty string means "no declared type", i.e. item()* — reported as a nil
// SeqType, which is exactly how Function.Params/Ret spell that.
func ParseSeqTypeString(s string) (*SeqType, error) {
	return parseSeqTypeStringWithSchema(s, nil)
}

// parseSeqTypeStringWithSchema is ParseSeqTypeString with schema component
// names resolved through sn (nil for an ordinary parse) — see schema_names.go.
func parseSeqTypeStringWithSchema(s string, sn SchemaNameLookup) (*SeqType, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, schema: sn}
	st, err := p.parseSequenceType()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tEOF {
		return nil, fmt.Errorf("unexpected %q after the sequence type", p.cur().text)
	}
	return st, nil
}

// coerceFunctionItem builds the coerced function CF of XPath 3.1 §3.1.5.3
// rule 3: calling it converts each argument to the corresponding parameter type
// of the EXPECTED function type and the result to the expected return type,
// under the function conversion rules, before and after invoking the supplied
// function. A conversion that cannot be made is XPTY0004 — raised at call time,
// on actual values, not when the function item is merely bound.
func coerceFunctionItem(fn *Function, want ItemType, ctx *Context) *Function {
	return &Function{
		Arity: fn.Arity, Name: fn.Name, NS: fn.NS,
		Typed: true, Params: want.FuncParams, Ret: want.FuncReturn,
		Call: func(args []Object) (Object, error) {
			conv := make([]Object, len(args))
			for i, a := range args {
				conv[i] = a
				if i < len(want.FuncParams) && want.FuncParams[i] != nil {
					cv, ok := convertForParamCtx(want.FuncParams[i], a, ctx)
					if !ok {
						return nil, fmt.Errorf("err:XPTY0004: argument %d does not match the required parameter type", i+1)
					}
					conv[i] = cv
				}
			}
			res, err := fn.Call(conv)
			if err != nil {
				return nil, err
			}
			if want.FuncReturn != nil {
				cv, ok := convertForParamCtx(want.FuncReturn, res, ctx)
				if !ok {
					return nil, fmt.Errorf("err:XPTY0004: the function result does not match the required return type")
				}
				return cv, nil
			}
			return res, nil
		},
	}
}

// TypedTextItem recovers the typed atomic value a TEXT node stands in for. The
// XSLT engine models an atomic item constructed inside a sequence constructor
// as a text node stamped with the item's original type (xmltree.Node.TypeAnno);
// a host that must hand the constructed value back as a real XDM item — rather
// than as the node standing in for it — asks for it here. ok is false for an
// ordinary (unstamped) text node, whose value is untyped content.
func TypedTextItem(n *xmltree.Node) (Item, bool) {
	if n == nil || n.Kind != xmltree.KindText {
		return nil, false
	}
	a, ok := nodeTypedValue(n)
	if !ok {
		return nil, false
	}
	return a, true
}
