package xpath

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// The ONE reading of xmltree.Node.TypeAnno.
//
// Six places used to decide independently that "TypeAnno == 0 means untyped,
// otherwise cast the string value to that type and fall back to untyped if the
// cast fails" — atomization (toAtomic), the context-item unwrappings in eval.go
// and ContextItemValue, TypedTextItem, the raw-result reader RawResultItem, and
// the XSLT engine's current()/xsl:key node readings. They agreed by accident,
// not by construction, and every future refinement of what an annotation MEANS
// (a complex type is not atomizable at all — FOTY0012; a list or union type
// yields a SEQUENCE of atomic values, not one) would have had to find all six.
// They go through here instead.

// IsTypeAnnotated reports whether a node carries an XDM type annotation, i.e.
// whether its typed value is anything other than the untyped reading of its
// string value. Callers that only need the question — not the value — ask here
// so the definition of "annotated" stays in one place.
func IsTypeAnnotated(n *xmltree.Node) bool {
	return n != nil && n.TypeAnno != 0
}

// nodeAnnotationType returns the annotation ITSELF, for the callers that ask
// what type a node is annotated with rather than what value that yields — the
// xs:QName special case in atomization (which needs the node's own namespace
// context to resolve a prefix, so it cannot go through CastTo) and the
// element(name, type) kind tests.
func nodeAnnotationType(n *xmltree.Node) (AtomType, bool) {
	if !IsTypeAnnotated(n) {
		return 0, false
	}
	return AtomType(n.TypeAnno), true
}

// NodeAnnotation is nodeAnnotationType for hosts outside this package — the
// XSLT engine's xsl:key indexing, which has to recognize the two annotations
// (xs:QName/xs:NOTATION) whose string value does not determine their value.
// ok is false for an unannotated node, which is every node in a
// non-schema-aware run.
func NodeAnnotation(n *xmltree.Node) (AtomType, bool) { return nodeAnnotationType(n) }

// nodeStringValue returns the XDM STRING VALUE of a node, which for a
// SCHEMA-VALIDATED node is not quite the text it was built from.
//
// The string value of a validated element or attribute is its PSVI [schema
// normalized value] — the lexical form after the governing type's whiteSpace
// facet has been applied — not the raw text and NOT the canonical form of the
// typed value. Both halves of that are pinned by the suite:
//
//   - match-136/137/138/139/140/141/218/220/221/243 and nodetest-036 read a
//     node whose declared type is a restriction of xs:decimal and whose text
//     is "    1.1     " through xsl:value-of, and want "1.1": whiteSpace
//     (collapse, as for every type but the two string ones) IS applied.
//   - strip-type-annotations-020's stated purpose is that "the typed value of
//     the integer 003 is different [from] its string value": data() is 3 and
//     string() is "003". Canonicalizing would have given "3" and destroyed
//     exactly the distinction the test exists to check.
//
// The facet is derived from the annotation's nearest built-in primitive rather
// than carried on the node: XSD fixes whiteSpace at "collapse" for every
// built-in but xs:string (preserve) and xs:normalizedString (replace), and a
// restriction may only strengthen it — so reading the primitive can only ever
// under-normalize a user type that tightened preserve to collapse, never
// invent normalization the schema did not ask for.
//
// Inert without an annotation, which is every node in a non-schema-aware run.
func nodeStringValue(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	at, ok := nodeAnnotationType(n)
	if !ok {
		return n.StringValue()
	}
	s := n.StringValue()
	switch at {
	case XSstring, XSuntypedAtomic, XSanyAtomicType:
		return s // whiteSpace = preserve
	case XSnormalizedString:
		return replaceWhitespace(s)
	}
	return strings.Join(strings.Fields(s), " ") // whiteSpace = collapse
}

// TypedNodeLexical returns the lexical form of a schema-validated element or
// attribute node's TYPED value, for the one caller outside this package that
// has to atomize a node without going through the full Item machinery: XSLT's
// simple-content construction (§5.8.2 step 3).
//
// ok is false for an unannotated node — which is every node in a
// non-schema-aware run — and for any other node kind, so the caller keeps its
// existing string-value behaviour untouched there.
func TypedNodeLexical(n *xmltree.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.Kind {
	case xmltree.KindElement, xmltree.KindAttribute:
	default:
		return "", false
	}
	// A LIST-annotated node's typed value is a SEQUENCE, so §5.8.2 atomizes it
	// to N items (step 3) and joins their string values with the item
	// separator (step 4) — it is not one cast of the whole string.
	//
	// Getting here via nodeTypedValue instead does not merely lose the
	// separator: CastTo("0.50 2.33 4.44", xs:decimal) simply FAILS, so ok came
	// back false and the caller silently fell through to the raw string value,
	// leaving every multi-token list value uncanonicalized while single-token
	// ones (which do cast) were canonicalized — the exact split
	// streamable-035 pins ("0.5 2.33 4.44" wanted, "0.50 2.33 4.44" produced,
	// alongside a correctly-canonicalized "-15" from "-15.00" in the same
	// document).
	if items, ok := nodeTypedItems(n); ok {
		parts := make([]string, 0, len(items))
		for _, it := range items {
			parts = append(parts, itemString(it))
		}
		return strings.Join(parts, " "), true
	}
	a, ok := nodeTypedValue(n)
	if !ok {
		return "", false
	}
	return a.Lexical(), true
}

// replaceWhitespace is XSD's "replace" whiteSpace normalization: every tab,
// line feed and carriage return becomes a single space, and nothing else
// changes.
func replaceWhitespace(s string) string {
	if !strings.ContainsAny(s, "\t\n\r") {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// nodeValueTypeName is the named type a node's TYPED VALUE is an instance of.
//
// It is the node's own SchemaType for every ordinary node, and its ValueType
// when the two differ — which they do for a UNION, whose [type-name] is the
// union itself while the typed value is an instance of whichever member
// accepted the lexical form (XDM 3.1 §3.3.1.1/§3.3.1.2). See
// xmltree.Node.ValueType.
func nodeValueTypeName(n *xmltree.Node) *xmltree.SchemaTypeName {
	if n == nil {
		return nil
	}
	if n.ValueType != nil {
		return n.ValueType
	}
	return n.SchemaType
}

// nodeTypedValue returns the typed atomic value of an annotated node. The
// annotation records a type the value was already validated against, so the
// cast succeeding is the norm; ok is false both for an unannotated node and
// for the should-not-happen case of an annotation its own string value no
// longer satisfies, which every caller reads as "use the untyped reading".
func nodeTypedValue(n *xmltree.Node) (*Atomic, bool) {
	if !IsTypeAnnotated(n) {
		return nil, false
	}
	a, err := CastTo(n.StringValue(), AtomType(n.TypeAnno))
	if err != nil {
		return nil, false
	}
	return a, true
}

// nodeTypedItems returns the typed value of a LIST-annotated node: one atomic
// item per whitespace-separated token of its string value, each of the list's
// ITEM type.
//
// This is the one annotation whose typed value is a SEQUENCE rather than a
// single value, which is why it cannot come from nodeTypedValue. It needs no
// schema access at match time because the annotation already carries what it
// needs: xsd's typeAnnoFor stores the ITEM type's primitive in TypeAnno and
// marks the node ListTyped, so the value is COMPUTED here from the string
// value rather than stored — a sequence a single TypeAnno field could never
// hold (import-schema-020/026..030, validation-0301).
//
// ok is false for any node that is not list-annotated, and for the
// should-not-happen case of a token its own annotation no longer admits, which
// callers read as "use the ordinary reading".
func nodeTypedItems(n *xmltree.Node) ([]Item, bool) {
	if n == nil || !n.ListTyped || !IsTypeAnnotated(n) {
		return nil, false
	}
	at := AtomType(n.TypeAnno)
	// XSD list item separation is by XML whitespace, and the list type's own
	// whiteSpace facet is fixed at "collapse", so splitting on fields IS the
	// normalization. An empty list is a legal, empty typed value.
	toks := strings.Fields(n.StringValue())
	out := make([]Item, 0, len(toks))
	for _, tok := range toks {
		a, err := CastTo(tok, at)
		if err != nil {
			return nil, false
		}
		// Each item carries the list's ITEM TYPE identity, not just its
		// primitive — the same fact SchemaType carries for a non-list node,
		// and the only thing that can answer `data($a) instance of
		// my:itemType*` (import-schema-029/030).
		out = append(out, a.withSchemaType(n.ListItemType))
	}
	return out, true
}
