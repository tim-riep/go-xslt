package xslt

// [xsl:]validation and [xsl:]type — the RUNTIME half of schema-awareness
// (XSLT 3.0 §24.4). decl_import_schema.go made a schema component NAMEABLE;
// this file is what finally puts a real type annotation on a real node, which
// is the only thing that can make element(N,T), schema-element(N) and an
// @as="element(*, my:T)" mean anything at match time.
//
// THREE PROPERTIES HOLD THIS TOGETHER, and each is load-bearing:
//
//   - INERT AT THE DEFAULT CLAIM. compileValidation returns a nil *valRequest
//     unless schemaAwareRun() was true when the instruction was compiled, and
//     every runtime hook is a single `if req == nil { return nil }`. A
//     non-schema-aware run therefore executes not one extra branch, which is
//     what keeps the 7,790-case default baseline byte-identical rather than
//     merely "probably unchanged".
//
//   - DEPTH-FIRST IS ALREADY THE SPEC'S ORDER. §24.4.1.1 says the validation
//     requested for a child is conceptually applied BEFORE its parent's, and
//     the parent's then overrides it ("if the instruction that constructs its
//     parent element specifies validation="strip", then the final effect will
//     be that the child node is annotated as xs:untyped"). XSLT constructs
//     depth-first, so applying each request where its own instruction FINISHES
//     reproduces that ordering exactly — no deferred pass, no second walk.
//
//   - VALIDATION IS internal/xsd's JOB, NOT THIS FILE'S. Everything here
//     decides WHICH assessment to ask for and HOW to name the failure; the
//     assessment itself goes to Schema.ValidateNode (bridge.go), which is the
//     same code the 99.99%-conformant string entry point runs.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"github.com/tim-riep/go-xslt/internal/xsd"
)

// builtinSchema is an EMPTY compiled schema, used purely for its built-in
// components. XSLT 3.0 §3.16 makes the built-in types part of the in-scope
// schema components unconditionally — xsl:import-schema adds user-defined
// components, it does not switch the built-ins on — so type="xs:ID" is a legal
// request in a stylesheet that imports nothing at all (import-schema-001..019
// are exactly that shape: default-validation="preserve" plus
// xsl:attribute/@type="xs:ID", with no xsl:import-schema anywhere).
//
// Compiled once, lazily, and only ever consulted for names the XSD namespace
// already owns, so it cannot introduce a component the stylesheet did not ask
// for.
var (
	builtinSchemaOnce sync.Once
	builtinSchemaVal  *xsd.Schema
)

func builtinSchemaComponents() *xsd.Schema {
	builtinSchemaOnce.Do(func() {
		s, err := xsd.Compile([]string{`<xs:schema xmlns:xs="` + xsdNS + `"/>`}, "", xsd.Version11)
		if err == nil {
			builtinSchemaVal = s
		}
	})
	return builtinSchemaVal
}

// schemaAdapterOrBuiltin is schemaAdapterFor with the built-in components as a
// floor. Deliberately separate from schemaAdapterFor itself, which internal/
// xpath's parse-time name resolution uses: widening THAT would change how
// element(N,T) node tests resolve across the whole engine, which is Round 1's
// settled behaviour and not this file's business to alter.
func schemaAdapterOrBuiltin(el *xmltree.Node) *schemaAdapter {
	if a := schemaAdapterFor(el); a != nil {
		return a
	}
	if s := builtinSchemaComponents(); s != nil {
		return &schemaAdapter{sch: s, el: el}
	}
	return nil
}

// validationMode is the effective [xsl:]validation value. valStrip is the zero
// value deliberately: §24.4 makes strip the answer when nothing is written
// anywhere, so a zero valRequest is already the correct default.
type validationMode uint8

const (
	valStrip validationMode = iota
	valPreserve
	valStrict
	valLax
)

// valKind is the CONSTRUCT a request was written on. It matters because
// §24.4.1.1 gives "preserve" a different meaning for each one — and the
// differences are not cosmetic (see applyValidation).
type valKind uint8

const (
	vkElement   valKind = iota // xsl:element and literal result elements
	vkAttribute                // xsl:attribute
	vkCopy                     // xsl:copy
	vkCopyOf                   // xsl:copy-of
	vkDocument                 // xsl:document / xsl:result-document
	// vkSourceDoc is xsl:source-document (and xsl:merge-source). It behaves
	// like vkDocument once a request exists, but §24.4 excludes it from the
	// ambient default: "The [xsl:]default-validation attribute has no effect
	// on the xsl:stream and xsl:merge-source elements, which perform no
	// validation unless explicitly requested." So with neither attribute
	// written, there is no request at all — not a request for strip.
	vkSourceDoc
)

// valRequest is one instruction's compiled validation request. A nil
// *valRequest means "this run is not schema-aware" — NOT "strip" — which is
// why every hook tests for nil before anything else.
type valRequest struct {
	mode validationMode
	// typ is the [xsl:]type attribute's resolved type name, nil when the
	// instruction carries none. When set it supersedes mode entirely
	// (§24.4.1.2), and the two can never both be present (XTSE1505).
	typ  *xmltree.SchemaTypeName
	kind valKind
	// el is the stylesheet element the request was written on: the position
	// for a diagnostic, and the key schemaForElement resolves the in-scope
	// schema components through.
	el *xmltree.Node
}

// anyType is the annotation §24.4.1.1 assigns to an element whose content was
// newly constructed under "preserve", and to any element a strict/lax episode
// left unassessed ("If no validation is performed for a node ... then the node
// is annotated as xs:anyType"). It is a REAL, nameable schema type — xsd's
// TypeByName resolves it — so element(*, xs:anyType) and the derivation walk
// behind schema-element(N) both recognise it with no special-casing anywhere.
//
// Attributes have no counterpart here: their "no validation performed" answer
// is xs:untypedAtomic, which this engine represents as an ABSENT annotation
// (TypeAnno 0, SchemaType nil) — the same representation an unvalidated node
// has always had.
var anyTypeName = xmltree.SchemaTypeName{Namespace: xsdNS, Local: "anyType", Complex: true}

// --- compile time -----------------------------------------------------------

// validationAttrsOf reads the [xsl:]validation and [xsl:]type attributes in
// whichever spelling el's own namespace calls for: unprefixed on an XSLT
// element, xsl:-prefixed on a literal result element. Both spellings name the
// same attribute (§24.4), so exactly one of the two is ever present.
func validationAttrsOf(el *xmltree.Node) (val string, hasVal bool, typ string, hasTyp bool) {
	if el.Name.Space == NS {
		val, hasVal = el.AttrLocal("validation")
		typ, hasTyp = el.AttrLocal("type")
		return
	}
	val, hasVal = el.Attr(NS, "validation")
	typ, hasTyp = el.Attr(NS, "type")
	return
}

// defaultValidationWalk returns the [xsl:]default-validation value in scope at
// el: the innermost ancestor-or-self carrying the attribute wins, and "" means
// none was written, for which §24.4 prescribes strip.
//
// Exactly the shape of xpathDefaultNSWalk, and for the same reason — this is a
// stylesheet-element-inherited default, resolved up the STYLESHEET's parent
// chain, never up a source document's.
//
// RESIDUAL: §24.4 scopes the default to "the containing stylesheet module or
// package: it does not extend to included or imported stylesheet modules".
// Walking Node.Parent already stops at each module's own document node, so an
// xsl:import never leaks its default into the importing module. A module
// spliced in from an external entity is the one shape this does not model
// separately, matching how every other inherited attribute here behaves.
func defaultValidationWalk(el *xmltree.Node) string {
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		var (
			v  string
			ok bool
		)
		if cur.Name.Space == NS {
			v, ok = cur.AttrLocal("default-validation")
		} else {
			v, ok = cur.Attr(NS, "default-validation")
		}
		if ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// compileValidation builds the validation request for one node-constructing
// instruction, or nil when this run is not schema-aware.
//
// The static errors of §24.4 are raised here rather than at execution time
// because that is what they are: XTSE1505 (the two attributes are mutually
// exclusive), XTSE1520 (the type name resolves to no in-scope component) and
// XTSE1530 (xsl:attribute cannot name a complex type) do not depend on any
// instance, so a stylesheet carrying one is rejected whether or not the
// instruction is ever reached.
func compileValidation(el *xmltree.Node, kind valKind) (*valRequest, error) {
	if !schemaAwareRun() || el == nil {
		return nil, nil
	}
	val, hasVal, typ, hasTyp := validationAttrsOf(el)
	if hasVal && hasTyp {
		return nil, errAt(el, "err:XTSE1505: the type and validation attributes are mutually exclusive")
	}
	req := &valRequest{kind: kind, el: el}
	switch {
	case hasTyp:
		name, err := resolveValidationType(el, typ, kind)
		if err != nil {
			return nil, err
		}
		req.typ = name
	case hasVal:
		m, ok := parseValidationMode(val)
		if !ok {
			// Out of vocabulary. validate.go already rejects this on a literal
			// result element (XTSE1660 via validationValueOK) and the
			// per-element attribute tables cover the XSLT elements, so nothing
			// is gained by inventing a second, differently-coded rejection
			// here — falling back to the §24.4 default is the conservative
			// reading, and cannot turn a currently-diagnosed stylesheet into a
			// silently-accepted one.
			return req, nil
		}
		req.mode = m
	case kind == vkSourceDoc:
		// Neither attribute, and the ambient default does not reach here.
		return nil, nil
	default:
		// §24.4: absent, the effective value is the innermost
		// [xsl:]default-validation, whose own permitted values are only
		// preserve and strip. Anything else there is ignored rather than
		// promoted into a validation episode nobody asked for.
		switch dv := defaultValidationWalk(el); dv {
		case "preserve":
			req.mode = valPreserve
		case "", "strip":
			req.mode = valStrip
		default:
			// §24.4, of [xsl:]default-validation: "The permitted values are
			// preserve and strip." strict and lax are legal on [xsl:]validation
			// but NOT here (validation-0110 declares
			// default-validation="strict" and expects a static error).
			//
			// Raised only under a schema-aware claim: without one, the same
			// attribute is already XTSE1660 (validationValueOK, validate.go),
			// and replacing that long-standing diagnostic would change the
			// default run for no gain.
			return nil, errAt(el, "err:XTSE0020: default-validation=%q is not permitted; the only values are preserve and strip", dv)
		}
	}
	return req, nil
}

// parseValidationMode maps the four-value vocabulary onto a mode.
func parseValidationMode(v string) (validationMode, bool) {
	switch strings.TrimSpace(v) {
	case "strip":
		return valStrip, true
	case "preserve":
		return valPreserve, true
	case "strict":
		return valStrict, true
	case "lax":
		return valLax, true
	}
	return 0, false
}

// resolveValidationType resolves a [xsl:]type attribute to the schema type it
// names, raising XTSE1520/XTSE1530 exactly as §24.4.1.2 defines them.
//
// The lexical expansion (prefix, or the effective [xsl:]xpath-default-namespace
// for an unprefixed name) is schemaAdapter.expand's, which is the SAME
// expansion an element(N,T) node test written on the same element gets — so a
// type name means one thing in a stylesheet, not two.
func resolveValidationType(el *xmltree.Node, typ string, kind valKind) (*xmltree.SchemaTypeName, error) {
	lex := strings.TrimSpace(typ)
	if lex == "" || (!isLexicalQName(lex) && !strings.HasPrefix(lex, "Q{")) {
		return nil, errAt(el, "err:XTSE1520: type=%q is not a valid QName", typ)
	}
	a := schemaAdapterOrBuiltin(el)
	// xs:untyped and xs:untypedAtomic are legal [xsl:]type values even though
	// they are not schema type DEFINITIONS and TypeByName correctly refuses
	// them: §24.4.1.2 defines each by reference to strip rather than to an
	// assessment ("If an element node is validated against the type
	// xs:untyped, the effect is the same as specifying validation='strip'"),
	// so they have to be admitted here and handled at execution
	// (validateAgainstType). Without this, validation-0107's
	// <z xsl:type="xs:untyped"> is rejected as naming nothing.
	if a != nil {
		if ns, local, ok := a.expand(lex); ok && ns == xsdNS && (local == "untyped" || local == "untypedAtomic") {
			return &xmltree.SchemaTypeName{Namespace: ns, Local: local}, nil
		}
	}
	if a == nil {
		return nil, errAt(el, "err:XTSE1520: type=%q names no type definition in the in-scope schema components", typ)
	}
	name, ok := a.LookupSchemaType(lex)
	if !ok {
		return nil, errAt(el, "err:XTSE1520: type=%q names no type definition in the in-scope schema components", typ)
	}
	if kind == vkAttribute && name.Complex {
		return nil, errAt(el, "err:XTSE1530: the type attribute of xsl:attribute must not refer to a complex type definition")
	}
	// §24.4.1.2: a PARENTLESS attribute cannot be validated against a
	// namespace-sensitive type — its QName/NOTATION content would have no
	// element to resolve a prefix against. "This affects the instructions
	// xsl:attribute, xsl:copy, and xsl:copy-of" (error-1545a); only the
	// xsl:attribute case is decidable statically, the other two depending on
	// what is actually being copied.
	if kind == vkAttribute && nsSensitiveType(a, name) {
		return nil, errAt(el, "err:XTTE1545: %s is namespace-sensitive and cannot be used to validate a parentless attribute", typ)
	}
	return &name, nil
}

// nsSensitiveType reports whether a type is, or derives from, xs:QName or
// xs:NOTATION — the two whose value space is namespace-sensitive.
func nsSensitiveType(a *schemaAdapter, name xmltree.SchemaTypeName) bool {
	if a == nil {
		return false
	}
	for _, local := range []string{"QName", "NOTATION"} {
		want := xmltree.SchemaTypeName{Namespace: xsdNS, Local: local}
		if name == want || a.DerivesFrom(name, want) {
			return true
		}
	}
	return false
}

// nsSensitiveNode reports whether a node's typed value is namespace-sensitive
// — "an item of type xs:QName or xs:NOTATION or a type derived therefrom"
// (§11.9.1's definition, the one XTTE0950 turns on).
//
// Read from the annotation's own primitive, which is exactly what the
// definition asks about, so this needs no schema lookup and is inert on any
// node nothing has validated.
func nsSensitiveNode(n *xmltree.Node) bool {
	if n == nil || n.TypeAnno == 0 {
		return false
	}
	at := xpath.AtomType(n.TypeAnno)
	return at == xpath.XSqname || at == xpath.XSnotation
}

// nsSensitiveTree reports whether a node or anything beneath it (attributes
// included) has namespace-sensitive content.
func nsSensitiveTree(n *xmltree.Node) bool {
	if n == nil {
		return false
	}
	if nsSensitiveNode(n) {
		return true
	}
	for _, a := range n.Attrs {
		if nsSensitiveNode(a) {
			return true
		}
	}
	for _, c := range n.Children {
		if nsSensitiveTree(c) {
			return true
		}
	}
	return false
}

// --- run time ---------------------------------------------------------------

// applyValidation carries out req against the node an instruction has just
// finished constructing. It is the single entry point every hook calls, so the
// per-mode rules live in exactly one place.
func (eng *engine) applyValidation(req *valRequest, n *xmltree.Node) error {
	return eng.applyValidationScoped(req, n, isDocumentScope(req, n))
}

// applyValidationScoped is applyValidation for a caller that knows better than
// isDocumentScope whether the episode is document-scoped. xsl:copy-of is the
// one such caller: a document-node operand is FLATTENED into the surrounding
// construction before the validation loop sees it, so only the instruction —
// which still holds the source items — can tell a copied document from a
// copied element, and the two need opposite answers (copy-5011/5012 copy a
// document and must report XTTE1555; accumulator-073 copies elements and must
// not).
func (eng *engine) applyValidationScoped(req *valRequest, n *xmltree.Node, docScope bool) error {
	if req == nil || n == nil {
		return nil
	}
	if req.typ != nil {
		return eng.validateAgainstType(req, n, docScope)
	}
	switch req.mode {
	case valStrict, valLax:
		return eng.validateNode(req, n, docScope)
	case valPreserve:
		applyPreserve(req.kind, n)
		return nil
	default: // valStrip
		stripAnnotations(n)
		return nil
	}
}

// applySourceDocValidation is applyValidation for the one instruction whose
// operand is a RETRIEVED SOURCE DOCUMENT rather than a constructed result:
// xsl:source-document. The episode itself is identical; only the choice of
// schema components differs, and it differs for a reason the engine already
// acts on everywhere else a source document is validated.
//
// §4.4's own definition of "source tree" lists documents read by
// xsl:source-document (spelled xsl:stream in the 2015 draft) alongside
// document()/fn:doc/fn:collection results and the principal input — and for
// every one of those the host, not the stylesheet, states the schema the
// document is to be understood against (Entry.SourceSchemas, consumed by
// validateSourceDocument). xsl:source-document was the one source-retrieval
// path still routing its episode through the stylesheet's imported components
// alone, so a stylesheet that retrieves a host-validated document without
// importing that schema itself got XTTE1512 for a document the very same run
// validates successfully when it arrives through fn:doc instead.
//
// sourceValidationSchema keeps the preference in the right order: the
// stylesheet's OWN components win whenever they can actually assess this
// document's root, which is what makes an annotation comparable to a pattern
// resolved against the same compile (see its own doc comment); the host's are
// a fallback, not an override. A stylesheet that imports the schema therefore
// behaves exactly as before.
func (eng *engine) applySourceDocValidation(req *valRequest, doc *xmltree.Node) error {
	if req == nil || doc == nil {
		return nil
	}
	// A named [xsl:]type, and preserve/strip, make no schema CHOICE at all —
	// they resolve against the stylesheet's own in-scope components or do no
	// assessment whatever, so they keep the generic path untouched.
	if req.typ != nil || (req.mode != valStrict && req.mode != valLax) {
		return eng.applyValidation(req, doc)
	}
	root := xmltree.RootElement(doc)
	if root == nil || eng.sheet == nil {
		return eng.applyValidation(req, doc)
	}
	sch := eng.sheet.sourceValidationSchema(eng.srcSchemas, root)
	if sch == nil {
		// No host schema either: fall back to whatever the stylesheet has,
		// which for an un-importing stylesheet is the built-ins alone — and
		// correctly yields XTTE1512, since it genuinely has no declaration.
		sch = req.schemaFor()
	}
	return eng.validateNodeAgainst(req, doc, isDocumentScope(req, doc), sch)
}

// applyPreserve implements §24.4.1.1's "preserve", whose meaning genuinely
// differs per instruction — the one place in this file where valKind earns its
// existence.
func applyPreserve(kind valKind, n *xmltree.Node) {
	switch kind {
	case vkElement:
		// "the new element has a type annotation of xs:anyType, and the type
		// annotations of contained nodes are retained unchanged."
		setAnyType(n)
	case vkAttribute:
		// "the effect is exactly the same as specifying validation='strip'".
		stripAnnotations(n)
	case vkCopy:
		// An element copied by xsl:copy becomes xs:anyType even under
		// preserve — "because this instruction does not copy the content of
		// the element, it would be wrong to assume that the type is
		// unchanged". Its contained nodes (supplied by the body, not by the
		// original) are retained, as with xsl:element. A copied ATTRIBUTE, by
		// contrast, "will retain its type annotation": nothing to do.
		if n.Kind == xmltree.KindElement {
			setAnyType(n)
		}
	case vkCopyOf, vkDocument:
		// "In the case of xsl:copy-of, all the nodes that are copied will
		// retain their type annotations unchanged" — and a document node under
		// preserve likewise keeps everything beneath it. Genuinely a no-op.
	}
}

// setAnyType annotates one element as xs:anyType, leaving everything beneath
// it exactly as it is.
func setAnyType(n *xmltree.Node) {
	if n == nil || n.Kind != xmltree.KindElement {
		return
	}
	name := anyTypeName
	n.SchemaType = &name
	// The typed value's own identity goes with the annotation that produced
	// it: xs:anyType has no simple content, so no union member accepted
	// anything here.
	n.ValueType = nil
	// xs:anyType has no simple content, so there is no built-in primitive its
	// typed value atomizes through — the same answer typeAnnoFor gives for
	// complex element-only content.
	n.TypeAnno = 0
	// Re-typing to xs:anyType discards the validation outcome that [nilled]
	// records, exactly as it discards the annotation: no validation produced
	// this type, so nothing establishes the element as nilled.
	n.Nilled = false
}

// stripAnnotations implements §24.4.1.1's "strip": the node and every element
// or attribute beneath it become xs:untyped / xs:untypedAtomic, which this
// engine represents as an absent annotation. "Schema validation is not
// invoked", so this is a pure walk.
//
// The walk is unconditional under a schema-aware claim rather than guarded by
// a "has anything ever been annotated?" flag. That is deliberate: a flag would
// have to be set by every site that can introduce an annotation — validation
// here, a copy from an annotated source, a future harness that validates input
// — and a single missed setter would silently UNDER-strip, leaving a stale
// type annotation on a node the spec says is untyped. Over-walking costs time
// in schema-aware runs only; under-stripping would be a correctness bug.
func stripAnnotations(n *xmltree.Node) {
	if n == nil {
		return
	}
	switch n.Kind {
	case xmltree.KindElement, xmltree.KindAttribute:
		// [nilled] goes with the annotation: an xs:untyped element is never
		// nilled, whatever xsi:nil attribute it still carries as plain markup
		// (validation-1202 strips a validated nilled element and must then
		// see nilled() = false).
		// ListTyped/ListItemType go with the annotation too: they only ever
		// describe how a LIST type's typed value is computed, and a stripped
		// node's typed value is one xs:untypedAtomic holding its string value
		// (§4.3), not a token sequence. Node.IDKind deliberately stays — §4.3
		// says the is-id/is-idrefs properties "are not changed".
		n.TypeAnno, n.SchemaType, n.Nilled = 0, nil, false
		n.ListTyped, n.ListItemType, n.ValueType = false, nil, nil
	}
	for _, a := range n.Attrs {
		a.TypeAnno, a.SchemaType = 0, nil
		a.ListTyped, a.ListItemType, a.ValueType = false, nil, nil
	}
	for _, c := range n.Children {
		stripAnnotations(c)
	}
}

// schemaFor returns the compiled schema components in scope for req, falling
// back to the built-ins alone (see builtinSchemaComponents) when the
// stylesheet imported nothing — which is enough to honour a [xsl:]type naming
// a built-in, and correctly finds no top-level declaration for strict
// validation, since an un-importing stylesheet genuinely has none.
func (req *valRequest) schemaFor() *xsd.Schema {
	if is := schemaForElement(req.el); is != nil {
		return is.sch
	}
	return builtinSchemaComponents()
}

// validateNode runs a strict or lax validation episode (§24.4.1.1) and maps
// its outcome onto the right XTTE code.
//
// The "is there a matching top-level declaration?" question is answered HERE,
// through the bridge's own ElementDeclared/AttributeDeclared, rather than by
// reading an error code back out of internal/xsd: strict distinguishes
// XTTE1512 (no declaration) from XTTE1510 (declared but invalid), and deciding
// that from the declaration lookup is exact, where pattern-matching a
// validator's diagnostic string would not be.
func (eng *engine) validateNode(req *valRequest, n *xmltree.Node, docScope bool) error {
	return eng.validateNodeAgainst(req, n, docScope, req.schemaFor())
}

// validateNodeAgainst is validateNode with the schema chosen by the caller,
// for the one caller whose choice is not simply "the components the stylesheet
// imported" — see applySourceDocValidation.
func (eng *engine) validateNodeAgainst(req *valRequest, n *xmltree.Node, docScope bool, sch *xsd.Schema) error {
	strict := req.mode == valStrict
	if sch == nil {
		if strict {
			return errAt(req.el, "err:XTTE1512: strict validation found no schema components to validate against")
		}
		// Lax with nothing to assess against: not an error, every node is
		// simply notKnown.
		markUnassessed(n)
		return nil
	}
	target := validationTarget(n)
	if target == nil {
		// A document node whose children are not exactly one element (plus
		// comments/PIs) cannot be validated at all (§24.4.2). Any other
		// unassessable kind is simply not a validation target.
		if n.Kind == xmltree.KindDocument {
			return errAt(req.el, "err:XTTE1550: a validated document node must have exactly one element child and no text nodes")
		}
		return nil
	}
	if strict && !declaredTopLevel(sch, target) {
		return errAt(req.el, "err:XTTE1512: no matching top-level declaration for %s in the in-scope schema components",
			clarkName(target.Name))
	}
	if !strict && !declaredTopLevel(sch, target) {
		// LAX with no matching top-level declaration: no validation episode
		// happens at all, so the nodes keep the annotation they already had.
		//
		// §3.3's "if no validation is performed for a node ... the node is
		// annotated as xs:anyType" is about a node left unassessed WITHIN an
		// episode — "which can happen when the SCHEMA specifies lax or skip
		// validation for that node or for a subtree", i.e. a processContents
		// wildcard. It does not turn an episode that never found anything to
		// assess into one that annotates everything: stream-013/non-stream-013
		// declare validation="lax" with no schema imported at all and assert
		// that the result is still element(*, xs:untyped)+.
		return nil
	}
	if err := sch.ValidateNode(target, xsd.NodeValidateOptions{Strict: strict, Document: docScope, LenientEntities: true}); err != nil {
		// A DOCUMENT-level constraint (ID uniqueness, IDREF resolution) has
		// its own code, whichever mode asked for the episode: §24 "It is a
		// type error if, when validating a document node, document-level
		// constraints (such as ID/IDREF constraints) are not satisfied"
		// (copy-5011/5012).
		if isDocumentConstraint(err) {
			return errAt(req.el, "err:XTTE1555: document-level constraints are not satisfied: %v", err)
		}
		if strict {
			return errAt(req.el, "err:XTTE1510: strict validation of %s failed: %v", clarkName(target.Name), err)
		}
		return errAt(req.el, "err:XTTE1515: lax validation of %s failed: %v", clarkName(target.Name), err)
	}
	markUnassessed(target)
	return nil
}

// validateAgainstType runs §24.4.1.2's validation against a NAMED type.
//
// Two of that section's own special cases are handled before internal/xsd is
// consulted, because they are defined by reference to strip rather than as
// assessments: xs:untyped means exactly validation="strip", and xs:untypedAtomic
// means "as if xs:string, but annotated untypedAtomic" — which, in an engine
// that spells untypedAtomic as an absent annotation, is again strip.
func (eng *engine) validateAgainstType(req *valRequest, n *xmltree.Node, docScope bool) error {
	if req.typ.Namespace == xsdNS {
		switch req.typ.Local {
		case "untyped":
			// "the effect is the same as specifying validation='strip'".
			stripAnnotations(n)
			return nil
		case "untypedAtomic":
			// "the effect is the same as specifying [xsl:]type='xs:string'
			// except that when validation succeeds, the returned element or
			// attribute has a type annotation of xs:untypedAtomic. Validation
			// fails in the case of an element with element children." Every
			// string is xs:string-valid, so the element-children clause is the
			// only way this can fail — and it is a real one (validation-0109's
			// <z xsl:type="xs:untypedAtomic">abcd<a/>wxyz</z> must raise
			// XTTE1540, where treating untypedAtomic as a plain strip would
			// silently accept it).
			if n.Kind == xmltree.KindElement {
				for _, c := range n.Children {
					if c.Kind == xmltree.KindElement {
						return errAt(req.el, "err:XTTE1540: an element with element children cannot be validated against xs:untypedAtomic")
					}
				}
			}
			stripAnnotations(n)
			// "when validation succeeds, the returned element or attribute has
			// a type annotation of xs:untypedAtomic" — which for an ELEMENT is
			// a genuinely unusual annotation (elements are otherwise xs:untyped
			// or a schema type), and the one thing that distinguishes this from
			// a plain strip. validation-0108 asserts exactly that distinction
			// with element(*, xs:untypedAtomic).
			name := xmltree.SchemaTypeName{Namespace: xsdNS, Local: "untypedAtomic"}
			n.SchemaType = &name
			return nil
		}
	}
	sch := req.schemaFor()
	if sch == nil {
		// compileValidation cannot resolve a type name without a schema, so
		// this is unreachable from a compiled request; answering honestly
		// rather than silently succeeding keeps it that way.
		return errAt(req.el, "err:XTTE1540: no schema components to validate against")
	}
	target := validationTarget(n)
	if target == nil {
		if n.Kind == xmltree.KindDocument {
			return errAt(req.el, "err:XTTE1550: a validated document node must have exactly one element child and no text nodes")
		}
		return nil
	}
	// XTTE1535: xsl:copy/xsl:copy-of against a COMPLEX type, when an item
	// being copied is an attribute node. Its sibling XTSE1530 catches the
	// xsl:attribute case statically; this one cannot be, because which items
	// an xsl:copy-of selects is not known until it runs.
	if req.typ.Complex && target.Kind == xmltree.KindAttribute &&
		(req.kind == vkCopy || req.kind == vkCopyOf) {
		return errAt(req.el, "err:XTTE1535: the type attribute refers to a complex type but an attribute node is being copied")
	}
	typ := *req.typ
	if err := sch.ValidateNode(target, xsd.NodeValidateOptions{Type: &typ, Document: docScope, LenientEntities: true}); err != nil {
		return errAt(req.el, "err:XTTE1540: validation of %s against type %s failed: %v",
			clarkName(target.Name), clark(typ.Namespace, typ.Local), err)
	}
	markUnassessed(target)
	return nil
}

// isDocumentConstraint reports whether a validation failure is one of the
// DOCUMENT-level constraints XSLT gives its own error code (XTTE1555) rather
// than one of the ordinary per-node ones.
func isDocumentConstraint(err error) bool {
	var xe *xsd.Error
	if errors.As(err, &xe) {
		return strings.HasPrefix(xe.Code, "cvc-id")
	}
	var ve xsd.Error
	if errors.As(err, &ve) {
		return strings.HasPrefix(ve.Code, "cvc-id")
	}
	return false
}

// isDocumentScope reports whether the node the instruction asked to validate
// is a DOCUMENT node, which is what decides whether document-level constraints
// (ID uniqueness, IDREF resolution) apply at all — XSLT 3.0 §24's XTTE1555
// attaches them to "validating a document node", not to an element episode.
//
// An xsl:document / xsl:result-document request qualifies even after
// validationTarget has reduced it to the single element child, which is why
// the ORIGINAL node is inspected here rather than the target.
func isDocumentScope(req *valRequest, n *xmltree.Node) bool {
	if n != nil && n.Kind == xmltree.KindDocument {
		return true
	}
	return req != nil && (req.kind == vkDocument || req.kind == vkSourceDoc)
}

// validationTarget is the node an assessment actually runs on. §24.4.2 is
// explicit that a type or validation attribute on a document-node-constructing
// instruction "refers to the required type of the element node that is the
// only element child of the document node. It does not refer to the type of
// the document node itself" — so a document node resolves to that one child,
// and yields nil when its content does not have that shape (XTTE1550).
//
// Only elements, attributes and document nodes can be assessed at all. A text,
// comment or PI node carries no type annotation in the XDM, so a validation
// request simply does not reach it — which matters for xsl:copy-of, whose
// @select routinely copies a mixture of kinds (validation-0203/0204/0208 copy
// text nodes alongside elements under validation="strict"/"lax"/@type).
func validationTarget(n *xmltree.Node) *xmltree.Node {
	switch n.Kind {
	case xmltree.KindElement, xmltree.KindAttribute:
		return n
	case xmltree.KindDocument:
	default:
		return nil
	}
	var elem *xmltree.Node
	for _, c := range n.Children {
		switch c.Kind {
		case xmltree.KindElement:
			if elem != nil {
				return nil // more than one element child
			}
			elem = c
		case xmltree.KindText:
			if c.Value != "" {
				return nil // text children are not permitted
			}
		}
	}
	return elem
}

// declaredTopLevel reports whether the schema has a top-level declaration
// matching n's name — the precondition strict validation fails on with
// XTTE1512 rather than XTTE1510.
func declaredTopLevel(sch *xsd.Schema, n *xmltree.Node) bool {
	switch n.Kind {
	case xmltree.KindAttribute:
		_, ok := sch.AttributeDeclared(n.Name.Space, n.Name.Local)
		return ok
	case xmltree.KindElement:
		_, ok := sch.ElementDeclared(n.Name.Space, n.Name.Local)
		return ok
	}
	return false
}

// validateSourceDocument applies Entry.SourceValidation to the primary source
// document, in place, before the transformation begins.
//
// It validates against the components the stylesheet ITSELF imported, which is
// both the simplest and the most correct choice available: a stylesheet that
// matches schema-element(N) has to have imported N's schema to be able to
// name it at all, so the declaration the source is validated against and the
// one the pattern was resolved against are then the SAME component — which is
// what makes the resulting annotation comparable at match time rather than
// merely similar.
//
// A mode of anything but strict/lax is no validation, so the default costs one
// string comparison; and with no schema imported there is nothing to validate
// against, which is not an error (the stylesheet cannot be asking about types
// it never imported).
func (ss *Stylesheet) validateSourceDocument(doc *xmltree.Node, mode string, schemas []string) error {
	strict := false
	switch mode {
	case "strict":
		strict = true
	case "lax":
	default:
		return nil
	}
	if doc == nil || !schemaAwareRun() {
		return nil
	}
	root := xmltree.RootElement(doc)
	if root == nil {
		return nil
	}
	sch := ss.sourceValidationSchema(schemas, root)
	if sch == nil {
		return nil
	}
	if strict && !hasXSIType(root) {
		// xsi:type on the document element names the type to assess it
		// against directly (XSD 1.0 §3.3.4 clause 1.2 / cvc-elt.4), which is a
		// complete strict assessment even with no top-level element
		// declaration for that name — strip-space-009's <doc xsi:type="t">
		// against a schema declaring only the complex type t. Demanding a
		// declaration as well would reject a document XSD itself calls valid.
		if _, ok := sch.ElementDeclared(root.Name.Space, root.Name.Local); !ok {
			return fmt.Errorf("err:XTTE1512: the source document's element %s has no top-level declaration in the schema",
				clarkName(root.Name))
		}
	}
	if err := sch.ValidateNode(root, xsd.NodeValidateOptions{Strict: strict, Document: true,
		LenientEntities: true, StripElementOnlyWhitespace: true}); err != nil {
		if strict {
			return fmt.Errorf("err:XTTE1510: the source document is not valid against the schema: %v", err)
		}
		return fmt.Errorf("err:XTTE1515: the source document is not valid against the schema: %v", err)
	}
	markUnassessed(root)
	// §3.11: input-type-annotations="strip" on ANY module of the package means
	// the transformation sees the input as untyped, whatever the host
	// supplied. Validation still RUNS — an invalid source is still reported —
	// but the annotations it earned are discarded rather than never computed,
	// which keeps the two halves of the attribute's meaning ("the input is
	// checked" vs "the stylesheet does not rely on the types") separate.
	if ss.stripInputTypes {
		stripAnnotations(root)
	}
	return nil
}

// sourceValidationSchema picks the compiled schema the primary source is
// validated against, and the choice is load-bearing rather than cosmetic.
//
// Annotating a source node records the NAME of its type, but answering
// schema-element(N) later also asks a DERIVATION question ("does this node's
// type derive from N's declared type?"), which internal/xsd settles by walking
// the component graph — so it can only be answered within ONE compiled schema.
// Validate the source against a separately-compiled copy of the very same .xsd
// the stylesheet imported and every such question silently answers "no": the
// annotation names a component in the host's graph, the pattern names the
// equally-valid twin in the stylesheet's, and nothing relates the two. That is
// exactly what validateSourceDocument's own contract is about ("the SAME
// component ... rather than merely similar"), but the old code reached for the
// host's copy first and so defeated it whenever a test environment named the
// same schema the stylesheet already imported — the common case, and why
// count(//schema-element(xsl:instruction)) over an XSLT stylesheet validated
// against schema-for-xslt20.xsd returned 0 instead of 57 (validation-0501/
// 0601/0701).
//
// So: prefer the stylesheet's own schema whenever it can actually assess this
// document, i.e. it declares the document element. Only when it cannot — the
// host validates a source in a vocabulary the stylesheet never imported, as
// validation-02 does with a GEDCOM source under a stylesheet importing only
// XHTML — does the host's own compilation take over, since a "similar" type
// nothing can relate still beats no validation at all.
func (ss *Stylesheet) sourceValidationSchema(schemas []string, root *xmltree.Node) *xsd.Schema {
	var own *xsd.Schema
	if ss.schema != nil {
		own = ss.schema.sch
	}
	if own != nil {
		if _, ok := own.ElementDeclared(root.Name.Space, root.Name.Local); ok {
			return own
		}
	}
	if host := sourceSchema(schemas); host != nil {
		if len(schemas) < 2 {
			return host
		}
		if _, ok := host.ElementDeclared(root.Name.Space, root.Name.Local); ok {
			return host
		}
	}
	// Several host schema documents compiled TOGETHER share one base
	// directory, which is wrong whenever they live in different ones: a
	// relative xs:import inside the second document then resolves against the
	// first's directory and silently contributes nothing (catalog-005/009
	// name ../../../admin/catalog-schema.xsd and a sibling
	// schema-for-xslt30.xsd, whose own xs:import of XMLSchema.xsd is relative
	// to ITS directory). Retry each document on its own, where its base
	// directory is unambiguous, and take the first that can actually assess
	// this document.
	for _, p := range schemas {
		if one := sourceSchema([]string{p}); one != nil {
			if _, ok := one.ElementDeclared(root.Name.Space, root.Name.Local); ok {
				return one
			}
		}
	}
	return own
}

// sourceSchema compiles (and caches) the schema documents a host named for
// source validation. Cached because a test suite hands the same handful of
// .xsd paths to hundreds of consecutive runs, and compiling an XHTML-sized
// schema per case is the difference between seconds and minutes.
func sourceSchema(paths []string) *xsd.Schema {
	if len(paths) == 0 {
		return nil
	}
	key := strings.Join(paths, "\x00")
	if v, ok := sourceSchemaCache.Load(key); ok {
		s, _ := v.(*xsd.Schema)
		return s
	}
	var (
		docs []string
		base string
	)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		docs = append(docs, string(b))
		if base == "" {
			base = filepath.Dir(p)
		}
	}
	var sch *xsd.Schema
	if len(docs) > 0 {
		if s, err := xsd.Compile(docs, base, xsd.Version11); err == nil {
			sch = s
		}
	}
	// A failed compile is cached as nil too: it will fail identically next
	// time, and retrying it per case is pure cost.
	sourceSchemaCache.Store(key, sch)
	return sch
}

var sourceSchemaCache sync.Map // joined paths -> *xsd.Schema (nil when it would not compile)

// markUnassessed gives xs:anyType to every element a successful validation
// episode left without a type — §24.4.1.1's "If no validation is performed for
// a node, which can happen when the schema specifies lax or skip validation
// for that node or for a subtree, then the node is annotated as xs:anyType",
// and §24.4.1.1's lax clause again for notKnown outcomes.
//
// This is the difference between "validated and found to be nothing in
// particular" and "never looked at". ValidateNode clears the annotation of any
// node it did not assess (bridge.go annotate), which is the right primitive
// but the wrong XDM answer on its own: xs:untyped would say the node had been
// STRIPPED. Attributes need no counterpart — their unassessed answer is
// xs:untypedAtomic, which an absent annotation already spells.
func markUnassessed(n *xmltree.Node) {
	if n == nil {
		return
	}
	if n.Kind == xmltree.KindElement && n.SchemaType == nil {
		name := anyTypeName
		n.SchemaType = &name
	}
	for _, c := range n.Children {
		markUnassessed(c)
	}
}

// checkDefaultValidationValue enforces §24.4's narrower vocabulary for
// [xsl:]default-validation: "The permitted values are preserve and strip."
// strict and lax are legal on [xsl:]validation itself but not as a default,
// which is a distinction only a schema-aware run can even reach — without the
// claim, validationValueOK has already rejected strict as XTSE1660, and
// replacing that long-standing diagnostic would change the default run for no
// gain (validation-0110).
func checkDefaultValidationValue(el *xmltree.Node, v string) error {
	if !schemaAwareRun() {
		return nil
	}
	switch strings.TrimSpace(v) {
	case "preserve", "strip":
		return nil
	}
	return errAt(el, "err:XTSE0020: default-validation=%q is not permitted; the only values are preserve and strip", v)
}

// hasXSIType reports whether an element carries an xsi:type attribute, i.e.
// names its own governing type rather than relying on a declaration.
func hasXSIType(n *xmltree.Node) bool {
	if n == nil {
		return false
	}
	_, ok := n.Attr("http://www.w3.org/2001/XMLSchema-instance", "type")
	return ok
}

// applyItemSeparator materializes the [xsl:]output/@item-separator into the
// result tree before it is validated.
//
// Sequence normalization — the process that turns a raw result SEQUENCE into
// a result TREE (Serialization 3.1 §2, which XSLT 3.0 §25.1 invokes by name
// for both the principal and every secondary result) — inserts the separator
// in step 3, "copy each item in S2 to the new sequence, inserting between each
// pair of items a string whose value is equal to the value of the
// item-separator parameter", and step 4 turns those strings into TEXT NODES.
// So the separators are part of the TREE, not of its serialization, and a
// [xsl:]validation episode sees them (validation-0214: a result document of
// comment/html/comment with item-separator="+++" is a document node with text
// children, which XTTE1550 forbids).
//
// Scoped deliberately to a strict/lax/[xsl:]type request, which is the only
// place the distinction is observable: for anything else the tree is built
// only to be serialized, and internal/xmltree's serializer writes the same
// separators between the same top-level items — materializing them here as
// well would double them. Under strict/lax/type, by contrast, a tree that
// gained separators has text at document level and can never validate, so it
// never reaches the serializer at all.
func (eng *engine) applyItemSeparator(req *valRequest, n *xmltree.Node, sep string, has bool) {
	if req == nil || n == nil || !has || n.Kind != xmltree.KindDocument {
		return
	}
	switch {
	case req.typ != nil, req.mode == valStrict, req.mode == valLax:
	default:
		return
	}
	if len(n.Children) < 2 {
		return
	}
	out := make([]*xmltree.Node, 0, len(n.Children)*2-1)
	for i, c := range n.Children {
		if i > 0 {
			t := &xmltree.Node{Kind: xmltree.KindText, Value: sep, Parent: n}
			out = append(out, t)
		}
		out = append(out, c)
	}
	n.Children = out
}
