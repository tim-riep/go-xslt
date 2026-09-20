package xpath

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// SCHEMA COMPONENT NAMES IN TYPE SYNTAX
// -------------------------------------
// element(N, T), attribute(N, T), schema-element(N) and schema-attribute(N)
// name schema components, and nothing in this package can look one up: the
// component graph lives in internal/xsd, which imports this package (xmltree
// <- xpath <- xsd) and therefore cannot be imported back.
//
// The split that resolves it: a name is turned into an identity ONCE, where
// the expression is PARSED, by a lookup the schema-aware host injects; from
// then on this package only ever COMPARES identities — the resolved
// xmltree.SchemaTypeName baked into NodeTest.SchemaType against the one a
// validated node carries in xmltree.Node.SchemaType, with derivation
// questions going to Context.SchemaTypes (the SchemaTypeResolver seam). No
// registry lookup, and no string-to-component resolution, ever happens at
// match time.
//
// Every entry point here is additive: the plain Parse/ParsePattern/
// ParseSeqTypeString keep their exact current behaviour, which is what a
// non-schema-aware run (the default) continues to get.

// SchemaNameLookup resolves the LEXICAL names written in type syntax against
// the schema components a host has imported. It is supplied by the caller
// that owns the static context — internal/xslt, which knows both the
// stylesheet's compiled schema and the in-scope namespace bindings of the
// element the expression is written on — so prefix resolution stays with the
// host rather than being re-invented here.
//
// A lookup that returns false is not an error at this level: the name simply
// names no imported component, and matching falls back to the built-in-only
// reading, which answers false for anything it cannot account for.
type SchemaNameLookup interface {
	// LookupSchemaType resolves a type name (prefixed, unprefixed or
	// Q{uri}local) to a schema type definition.
	LookupSchemaType(lexical string) (xmltree.SchemaTypeName, bool)
	// LookupSchemaElement resolves a top-level element declaration's name,
	// returning its expanded name and its declared type.
	LookupSchemaElement(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool)
	// LookupSchemaAttribute does the same for a top-level attribute
	// declaration.
	LookupSchemaAttribute(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool)
}

// SchemaSubstitutionResolver is an OPTIONAL extension of the object a host
// installs in Context.SchemaTypes. It answers the SUBSTITUTION-GROUP half of
// schema-element(N)'s matching rule, which plain name equality cannot: a
// candidate matches when its name is N or the name of an element declaration
// in the substitution group headed by N (XPath 3.1 §2.5.5.5).
//
// Optional, and type-asserted rather than folded into SchemaTypeResolver, so
// that the fixed Round 1 seam keeps its exact shape: a resolver that does not
// implement this simply answers the old exact-name question, which is what
// every non-schema-aware run already gets.
type SchemaSubstitutionResolver interface {
	SubstitutesFor(member, head xmltree.Name) bool
}

// SchemaDeclTypeResolver is another OPTIONAL extension of Context.SchemaTypes,
// type-asserted exactly like SchemaSubstitutionResolver.
//
// It answers a question a PARSE-time lookup structurally cannot: XPath 3.1
// §2.5.5.4's third condition is "derives-from(AT, ET) is true, where AT is the
// type annotation of the candidate node and ET is the schema type declared in
// the schema element declaration named N" — N being the CANDIDATE's own name,
// not the ElementName written in the test. The REC's own worked example spells
// it out: schema-element(customer) matches a <client> "[if] the type
// annotation of the candidate node is the same as or derived from the schema
// type declared for the CLIENT element". Which declaration that is only
// becomes known when a candidate arrives, so the type cannot be baked into
// NodeTest.SchemaType the way element(N, T)'s explicitly-written T is.
//
// Getting this wrong is not a near miss: an abstract head declared with no
// @type has xs:anyType, which every node's annotation derives from, so reading
// ET off the HEAD made schema-element(head) match every substitution-group
// member regardless of its annotation — including annotation-stripped input
// (validation-1001, whose expected result the WG changed specifically over
// this point).
type SchemaDeclTypeResolver interface {
	// ElementDeclType returns the type declared by the top-level element
	// declaration with this expanded name, and whether such a declaration
	// exists at all.
	ElementDeclType(name xmltree.Name) (xmltree.SchemaTypeName, bool)
}

// schemaElemDeclType asks the installed resolver for the declared type of the
// top-level element declaration named by a candidate node. ok is false when no
// resolver is installed, it does not answer declaration questions, or the name
// is not declared — every one of which leaves the caller on its fail-closed
// path.
func schemaElemDeclType(name xmltree.Name, ctx *Context) (xmltree.SchemaTypeName, bool) {
	if ctx == nil || ctx.SchemaTypes == nil {
		return xmltree.SchemaTypeName{}, false
	}
	dr, ok := ctx.SchemaTypes.(SchemaDeclTypeResolver)
	if !ok {
		return xmltree.SchemaTypeName{}, false
	}
	return dr.ElementDeclType(name)
}

// substitutesFor asks the installed resolver whether candidate name got is
// substitutable for the declaration named want. False whenever no resolver is
// installed or it does not answer substitution questions — fail-closed, as
// everywhere else in this file.
func substitutesFor(got, want xmltree.Name, ctx *Context) bool {
	if ctx == nil || ctx.SchemaTypes == nil {
		return false
	}
	sr, ok := ctx.SchemaTypes.(SchemaSubstitutionResolver)
	if !ok {
		return false
	}
	return sr.SubstitutesFor(got, want)
}

// SchemaTypeCaster is a second OPTIONAL extension of Context.SchemaTypes: it
// validates a lexical form against a named simple type and reports the
// built-in primitive that type's value space sits in.
//
// It is what makes `cast as my:T`, `castable as my:T` and the constructor
// function my:T(...) mean anything (XPath 3.1 §3.14, §3.14.4 — a constructor
// exists for every atomic type in the in-scope schema types). The validation
// itself belongs to the schema processor, which owns the facets; this package
// only decides WHEN to ask.
type SchemaTypeCaster interface {
	CastToSchemaType(t xmltree.SchemaTypeName, lexical string) (AtomType, error)
}

// SchemaTypeCasterIn is an OPTIONAL refinement of SchemaTypeCaster, type-
// asserted exactly like SchemaSubstitutionResolver so the fixed seam above is
// untouched for a host that does not implement it.
//
// It exists for xs:QName- and xs:NOTATION-derived types, whose ENUMERATION
// facet constrains the value space — expanded names — rather than the lexical
// space. Deciding whether "n:wav" is in an enumeration written as "test:wav"
// means expanding both sides, and the instance side's prefix is bound by the
// STATIC namespace context of the expression doing the cast, which the schema
// processor cannot see. An instance document's value carries its own element
// to resolve against; a constructor function call (`n:nota($q)`) or a
// `cast as` has only the query's namespace bindings, so this hands over a
// carrier node holding the one binding the lexical form actually needs.
type SchemaTypeCasterIn interface {
	CastToSchemaTypeIn(t xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (AtomType, error)
}

// SchemaListTypeCaster is a third OPTIONAL extension of Context.SchemaTypes,
// type-asserted like the others.
//
// A LIST simple type is the one cast target whose result is a SEQUENCE, not a
// single atomic value (XPath 3.1 §3.14.3: the operand's string value is split
// into tokens and each token cast to the item type), so the one-AtomType shape
// SchemaTypeCaster returns cannot describe it — which is exactly why casting
// to a named list type used to be refused outright. Nothing needed to be
// STORED to fix that: the sequence is COMPUTED from the (already
// schema-validated) lexical form, the same reframe Node.ListTyped applies to a
// list-annotated node's typed value.
type SchemaListTypeCaster interface {
	// CastToSchemaListType reports whether t names a LIST simple type and, if
	// so, validates lexical against it as a whole (the list type's own facets:
	// length, minLength, pattern) and returns the ITEM type's primitive
	// together with the item type's own name when it has one.
	CastToSchemaListType(t xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (
		item AtomType, itemName *xmltree.SchemaTypeName, isList bool, err error)
}

// SchemaDocumentValidator is a fourth OPTIONAL extension of
// Context.SchemaTypes, type-asserted exactly like the resolvers above.
//
// It is the one place in the whole function library that asks for a VALIDATION
// EPISODE rather than a question ABOUT types: fn:json-to-xml's `validate`
// option (F&O 3.1 §17.5.1 — "if the option is true, the resulting XDM instance
// is validated against the schema for the namespace
// http://www.w3.org/2005/xpath-functions"). Deciding WHEN to ask stays here;
// the validation itself belongs to the schema processor that owns the
// components, exactly as SchemaTypeCaster already splits `cast as`.
//
// A host that installs no validator — every non-schema-aware run, and any
// schema-aware run whose stylesheet brought no components into scope — leaves
// this nil, which is precisely the condition F&O attaches FOJS0004 to.
type SchemaDocumentValidator interface {
	// ValidateSchemaDocument validates doc (a document node) strictly against
	// the host's in-scope schema components, annotating every element and
	// attribute it assesses in place. It reports an error when the document is
	// invalid, or when no top-level declaration for its document element is in
	// scope.
	ValidateSchemaDocument(doc *xmltree.Node) error
}

// schemaDocumentValidator returns the installed validator, or nil when the
// host installed none (no schema in scope, or a run that is not schema-aware).
func schemaDocumentValidator(ctx *Context) SchemaDocumentValidator {
	if ctx == nil || ctx.SchemaTypes == nil {
		return nil
	}
	v, _ := ctx.SchemaTypes.(SchemaDocumentValidator)
	return v
}

// prefixCarrier builds a throwaway element node binding just the prefix that
// appears in lexical (or the default namespace when it has none), so a schema
// processor can expand the value against the caller's static namespace context
// using the same node-based resolution an instance document's value gets.
// Returns nil when nothing can be resolved, which keeps the plain
// CastToSchemaType path in play.
func prefixCarrier(lexical string, ctx *Context) *xmltree.Node {
	if ctx == nil || ctx.NS == nil {
		return nil
	}
	prefix := ""
	if i := strings.IndexByte(strings.TrimSpace(lexical), ':'); i >= 0 {
		prefix = strings.TrimSpace(lexical)[:i]
	}
	uri, ok := ctx.NS.ResolveNS(prefix)
	if !ok || uri == "" {
		return nil
	}
	return &xmltree.Node{
		Kind: xmltree.KindElement,
		NS: []*xmltree.Node{{
			Kind:  xmltree.KindNamespace,
			Name:  xmltree.Name{Local: prefix},
			Value: uri,
		}},
	}
}

// castToNamedSchemaType casts one atomic value to a named simple type. The
// result carries both the built-in primitive (so arithmetic and comparison
// keep working) and the named type itself (so `instance of` can answer).
func castToNamedSchemaType(it Item, name xmltree.SchemaTypeName, ctx *Context) (Object, error) {
	if ctx == nil || ctx.SchemaTypes == nil {
		return nil, fmt.Errorf("err:XPST0051: %s is not an in-scope schema type", name.Local)
	}
	c, ok := ctx.SchemaTypes.(SchemaTypeCaster)
	if !ok {
		return nil, fmt.Errorf("err:XPST0051: %s is not an in-scope schema type", name.Local)
	}
	lex := itemString(it)
	// A LIST target's result is a sequence, so it is settled before the
	// single-value path below — see SchemaListTypeCaster.
	if lc, ok := c.(SchemaListTypeCaster); ok {
		item, itemName, isList, lerr := lc.CastToSchemaListType(name, lex, prefixCarrier(lex, ctx))
		if lerr != nil {
			return nil, lerr
		}
		if isList {
			out := []Item{}
			for _, tok := range strings.Fields(lex) {
				a, cerr := CastTo(NewString(tok), item)
				if cerr != nil {
					return nil, cerr
				}
				out = append(out, a.withSchemaType(itemName))
			}
			return FromItems(out), nil
		}
	}
	var (
		prim AtomType
		err  error
	)
	if cin, ok := c.(SchemaTypeCasterIn); ok {
		if at := prefixCarrier(lex, ctx); at != nil {
			prim, err = cin.CastToSchemaTypeIn(name, lex, at)
		} else {
			prim, err = c.CastToSchemaType(name, lex)
		}
	} else {
		prim, err = c.CastToSchemaType(name, lex)
	}
	if err != nil {
		return nil, err
	}
	// A named type whose primitive is xs:QName or xs:NOTATION inherits the
	// casting table's SOURCE restriction (F&O 3.0 §19.1, shipped with the
	// suite at specs/functions-and-operators-rec30.xml): only the string
	// family, xs:untypedAtomic, xs:QName and xs:NOTATION may be cast to one —
	// "Cast anyURI to QName? No", "Cast anyURI to NOTATION? No". The schema
	// only ever checks the LEXICAL form against the type's facets, so without
	// this an xs:anyURI spelled "n:wav" would be castable to a NOTATION
	// subtype purely because its text happens to satisfy the enumeration
	// (notation-0002 case l).
	if prim == XSqname || prim == XSnotation {
		if a, aok := it.(*Atomic); aok {
			switch {
			case isStringType(a.T), a.T == XSuntypedAtomic, a.T == XSqname, a.T == XSnotation:
			default:
				return nil, fmt.Errorf("err:XPTY0004: cannot cast %s to %s", a.T, name.Local)
			}
		}
	}
	// The SOURCE type must also be able to reach that primitive at all: the
	// schema only ever checked the value's LEXICAL form against the type's
	// facets, and a lexical form alone says nothing about whether the casting
	// table (F&O 3.0 §19.1) permits the conversion. xs:gYear("2001") has the
	// lexical form "2001", which a union of xs:integer and xs:date happily
	// validates as an integer — but "Cast gYear to integer? No", so the cast
	// must fail (castable-005's Castable-UnionType-9/22). QName/NOTATION keep
	// their own rule above: CastTo refuses the abstract xs:NOTATION outright,
	// and untypedAtomic->QName is barred there for a reason that does not
	// apply to a named schema type's own constructor.
	if prim != XSqname && prim != XSnotation {
		if _, cerr := CastTo(it, prim); cerr != nil {
			return nil, cerr
		}
	}
	var v *Atomic
	if prim == XSnotation {
		v, err = notationAtomicFor(lex, ctx)
	} else {
		v, err = CastTo(lex, prim)
	}
	if err != nil {
		return nil, err
	}
	n := name
	return v.withSchemaType(&n), nil
}

// notationAtomicFor builds the value of a lexical form that has already been
// validated against a NOTATION-DERIVED simple type.
//
// It exists because CastTo refuses xs:NOTATION outright (XPST0080). That rule
// is about the TARGET a cast names — XPath 3.1 §3.14 forbids `cast as
// xs:NOTATION` and `cast as xs:anySimpleType` because both are abstract — and
// says nothing against a type DERIVED from xs:NOTATION, which is exactly what
// a constructor like test:nota('n:wav') names and what XSD requires every
// usable NOTATION to be (§3.2.19: xs:NOTATION may only be used through a
// restriction with an enumeration). cast.go's node-typed-value path already
// makes this same distinction for an annotated node, for the same reason.
//
// xs:NOTATION shares xs:QName's value space (expanded names), so the prefix is
// resolved against the expression's own static namespace context and kept, as
// it is what the value's lexical form is built from.
func notationAtomicFor(lex string, ctx *Context) (*Atomic, error) {
	s := strings.TrimSpace(lex)
	prefix, local := "", s
	if i := strings.IndexByte(s, ':'); i >= 0 {
		prefix, local = s[:i], s[i+1:]
	}
	uri := ""
	if ctx != nil && ctx.NS != nil {
		if u, ok := ctx.NS.ResolveNS(prefix); ok {
			uri = u
		} else if prefix != "" {
			return nil, fmt.Errorf("err:FONS0004: no namespace declaration for prefix %q", prefix)
		}
	} else if prefix != "" {
		return nil, fmt.Errorf("err:FONS0004: no namespace declaration for prefix %q", prefix)
	}
	return &Atomic{T: XSnotation, qn: xmltree.Name{Space: uri, Local: local, Prefix: prefix}}, nil
}

// lookupSchemaSimpleType resolves an AtomicOrUnionType name against the
// imported schema components, reporting false when no schema is in scope, the
// name names nothing, or it names a COMPLEX type (which is not an
// AtomicOrUnionType and so belongs in the caller's static-error path).
func (p *parser) lookupSchemaSimpleType(name string) (*xmltree.SchemaTypeName, bool) {
	if p.schema == nil {
		return nil, false
	}
	t, ok := p.schema.LookupSchemaType(name)
	if !ok || t.Complex {
		return nil, false
	}
	// A built-in resolves through the schema too (every schema contains the
	// built-ins); those keep the existing AtomType path, which knows their
	// hierarchy natively.
	if t.Namespace == nsXS {
		return nil, false
	}
	return &t, true
}

// schemaTypeOverride resolves a PREFIXED AtomicOrUnionType name that the
// imported schema actually defines, ahead of atomTypeForEQName's lenient
// local-part reading.
//
// atomTypeForEQName deliberately accepts ANY prefix on a name whose local part
// is a built-in ("xsd:integer", "xsdt:integer", …) because this engine does not
// resolve the prefix there. That leniency is harmless until a schema defines a
// type of its own whose local name collides with a built-in — which the W3C's
// own schema for XSLT does on purpose: its xsl:QName is a restriction of
// xs:Name, NOT the built-in xs:QName, and its own documentation explains why.
// Without this the lenient reading silently answered every `instance of
// xsl:QName` against the built-in and could never say yes (import-schema-
// 029/030).
//
// Strictly monotone: lookupSchemaSimpleType already declines a name that
// resolves INTO the XSD namespace, so a genuine xs:-prefixed built-in is
// untouched, and a prefixed name the schema does NOT define still falls
// through to the lenient reading exactly as before. A BARE (unprefixed) name
// is deliberately left alone — parseItemType's own schema branch already
// handles it, after the built-in reading, which is what an unprefixed name in
// no namespace needs.
func (p *parser) schemaTypeOverride(name string) (*xmltree.SchemaTypeName, bool) {
	if p.schema == nil || !strings.Contains(name, ":") || strings.HasPrefix(name, "Q{") {
		return nil, false
	}
	return p.lookupSchemaSimpleType(name)
}

// TypedModeRewrite implements the extra provision XSLT 3.0 §6.6.3 attaches to
// xsl:mode/@typed="strict" and "lax": in the match pattern of any template
// rule applicable to such a mode, "any NameTest used in the ForwardStepP of
// the first StepExprP of a RelativePathExprP" whose axis has Element as its
// principal node kind "is interpreted as schema-element(E)".
//
// The two values differ only in what happens when E names no global element
// declaration: strict makes that a static error (XTSE3105), lax lets the step
// stand as an ordinary name test ("the template matching proceeds as if the
// typed attribute were absent"). A WILDCARD, or an attribute/namespace axis,
// is left alone under both.
//
// The rewrite is recorded as NodeTest.TypedElem rather than by changing the
// test's Kind to testSchemaElement, so that the pattern's DEFAULT PRIORITY and
// its streamability classification — both computed from the test kind — stay
// exactly what the pattern as WRITTEN gives. §6.6.3 phrases the provision as
// being about matching ("When matching templates in this mode"), not about
// how the pattern is otherwise classified.
func (p *Pattern) TypedModeRewrite(sn SchemaNameLookup, strict bool) error {
	if p == nil || sn == nil {
		return nil
	}
	for _, alt := range p.alts {
		if err := typedModeRewritePath(alt, sn, strict); err != nil {
			return err
		}
	}
	// exprAlts holds the XSLT 3.0 expression-shaped patterns — (a|b)[p],
	// id(...), $var, except/intersect. §6.6.3's rule is written against a
	// RelativePathExprP's first StepExprP, which none of these is.
	return nil
}

func typedModeRewritePath(pe *PathExpr, sn SchemaNameLookup, strict bool) error {
	if pe == nil || pe.Start != nil || pe.predPattern || len(pe.Steps) == 0 {
		return nil
	}
	i := 0
	// "//" contributes a synthetic descendant-or-self::node() step; the
	// RelativePathExprP begins after it (match-244's "//elname" is exactly
	// this shape, and its first StepExprP is elname).
	if len(pe.Steps) > 1 && !pe.Steps[0].AxisExplicit &&
		pe.Steps[0].Axis == "descendant-or-self" && pe.Steps[0].Test.Kind == testNode {
		i = 1
	}
	st := pe.Steps[i]
	if st.Axis == "attribute" || st.Axis == "namespace" {
		return nil // principal node kind is not Element
	}
	t := &st.Test
	if t.Kind != testName || t.AnyName || t.WildNS || t.Local == "" {
		return nil // a wildcard is left alone
	}
	decl, typ, ok := sn.LookupSchemaElement(lexicalNameOf(*t))
	if !ok {
		if strict {
			return fmt.Errorf("err:XTSE3105: %s is not the name of any global element declaration, "+
				"and this template rule is applicable to a mode declared typed=\"strict\"", t.Local)
		}
		return nil
	}
	t.URI, t.Braced, t.Local = decl.Space, true, decl.Local
	t.SchemaType = &typ
	t.TypedElem = true
	return nil
}

// lexicalNameOf reconstructs the name a NAME test was written with, in the
// form a SchemaNameLookup expects: the host's own expand() resolves a prefix
// through the stylesheet element's bindings and an unprefixed name through
// [xsl:]xpath-default-namespace, which is exactly how the test itself resolves
// at match time.
func lexicalNameOf(t NodeTest) string {
	switch {
	case t.Prefix != "":
		return t.Prefix + ":" + t.Local
	case t.Braced:
		return "Q{" + t.URI + "}" + t.Local
	}
	return t.Local
}

// ParseWithheldSchema compiles an XPath expression for a host that HAS
// in-scope schema components but is deliberately withholding them —
// xsl:evaluate without schema-aware="yes". It is exactly Parse, except that a
// user type name it cannot resolve is XTDE3160 rather than the usual lenient
// "treat an unknown prefixed name as xs:anyAtomicType".
func ParseWithheldSchema(src string) (*Parsed, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", src, err)
	}
	p := &parser{toks: toks, schemaWithheld: true}
	e, err := p.parseExpr()
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", src, err)
	}
	if p.cur().kind != tEOF {
		return nil, fmt.Errorf("xpath %q: unexpected trailing token %q", src, p.cur().text)
	}
	return &Parsed{root: e, text: src}, nil
}

// ParseSchemaAware compiles an XPath expression with schema component names
// resolved through sn. A nil sn is exactly Parse.
func ParseSchemaAware(src string, sn SchemaNameLookup) (*Parsed, error) {
	if sn == nil {
		return Parse(src)
	}
	return parseWithSchema(src, sn)
}

// ParsePatternSchemaAware compiles an XSLT match pattern the same way.
func ParsePatternSchemaAware(src string, sn SchemaNameLookup) (*Pattern, error) {
	if sn == nil {
		return ParsePattern(src)
	}
	return parsePatternWithSchema(src, sn)
}

// ParseSeqTypeStringSchemaAware parses a declared sequence type (an @as
// attribute's value) the same way. As with ParseSeqTypeString an empty string
// means "no declared type" and yields a nil SeqType.
func ParseSeqTypeStringSchemaAware(s string, sn SchemaNameLookup) (*SeqType, error) {
	if sn == nil {
		return ParseSeqTypeString(s)
	}
	return parseSeqTypeStringWithSchema(s, sn)
}

// schemaLookupOf recovers the name lookup from a Context. The host installs
// ONE adapter object in Context.SchemaTypes; it always answers derivation
// questions (SchemaTypeResolver, the fixed seam) and, when it can also resolve
// names, implements SchemaNameLookup too. Type strings this engine keeps
// unparsed until evaluation (@as on a template/function/variable) are parsed
// through it, so they resolve component names identically to an expression
// parsed at compile time.
func schemaLookupOf(ctx *Context) SchemaNameLookup {
	if ctx == nil || ctx.SchemaTypes == nil {
		return nil
	}
	sn, _ := ctx.SchemaTypes.(SchemaNameLookup)
	return sn
}

// schemaTypeMatches reports whether node n's type annotation satisfies a
// resolved schema type name — the "and whose type is T or derived from T"
// half of element(N,T)/attribute(N,T)/schema-element(N)/schema-attribute(N).
//
// Fail-closed by construction: an UNANNOTATED node (nothing has validated it,
// which is every node until validation is wired) carries no type identity at
// all and therefore satisfies no named type, and with no resolver installed
// nothing is known to derive from anything.
func schemaTypeMatches(want xmltree.SchemaTypeName, n *xmltree.Node, ctx *Context) bool {
	if n == nil {
		return false
	}
	if n.SchemaType == nil {
		// An UNVALIDATED node still has a type annotation in the XDM:
		// xs:untyped for an element, xs:untypedAtomic for an attribute. Both
		// derive from xs:anyType, so element(*, xs:anyType) matches every
		// element whether or not anything has validated it — which is what
		// the built-in-only reading in kindTestTypeOK always answered, and
		// what import-schema-076 asserts over an RTF nothing validated. Any
		// other named type is unsatisfiable without an annotation.
		return want.Namespace == nsXS && (want.Local == "anyType" || want.Local == "anySimpleType")
	}
	if *n.SchemaType == want {
		return true
	}
	if ctx == nil || ctx.SchemaTypes == nil {
		return false
	}
	return ctx.SchemaTypes.DerivesFrom(*n.SchemaType, want)
}
