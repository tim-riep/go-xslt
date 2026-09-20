package xpath

import (
	"fmt"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// This file is the host-facing entry point for the two output methods that
// serialize a RAW XDM SEQUENCE rather than a node tree: json and adaptive.
// Everything they need already exists inside this package (the JSON writer
// behind fn:serialize/fn:xml-to-json and the adaptive writer behind
// fn:serialize), but it is all unexported; internal/xslt reaches it through
// the two functions below instead of duplicating the mapping rules.

// OutputParams carries the serialization parameters an XSLT host resolves from
// xsl:output / xsl:result-document for the json and adaptive output methods.
// Only the parameters those two methods actually observe are listed — the
// node-tree parameters (doctype-*, standalone, cdata-section-elements, …) have
// no meaning for either method.
type OutputParams struct {
	// NodeMethod is json-node-output-method: the output method used to turn a
	// NODE appearing in the value into the JSON string that represents it.
	// "" means the spec default, xml.
	NodeMethod string
	// AllowDuplicateNames permits two map keys whose string values coincide to
	// produce duplicate JSON object names instead of raising SERE0022.
	AllowDuplicateNames bool
	// Encoding limits which code points may be written literally; anything
	// above the encoding's range becomes a \uXXXX escape.
	Encoding string
	// CharacterMap is use-character-maps, keyed by the single character to
	// replace. A mapped character is written as its replacement VERBATIM,
	// bypassing JSON escaping entirely.
	CharacterMap map[string]string
	// HTMLVersion is html-version, consulted only when NodeMethod is html or
	// xhtml.
	HTMLVersion string
	// OmitXMLDeclaration suppresses the XML declaration the ADAPTIVE method
	// writes in front of a node item (it serializes a node exactly as the xml
	// method does, declaration included — output-0721 relies on the
	// declaration being there, result-document-0304 sets this to switch it
	// off). It has no effect on the json method, whose nested node
	// serializations never carry a declaration.
	OmitXMLDeclaration bool
	// ItemSeparator/HasItemSeparator is item-separator, which the adaptive
	// method writes between adjacent items (its default is a newline). The
	// json method never joins items: more than one item is an error there.
	ItemSeparator    string
	HasItemSeparator bool
}

func (p OutputParams) toSerParams(method string) serParams {
	sp := serParams{
		method:        method,
		nodeMethod:    p.NodeMethod,
		allowDup:      p.AllowDuplicateNames,
		encoding:      p.Encoding,
		charMaps:      p.CharacterMap,
		htmlVersion:   p.HTMLVersion,
		itemSeparator: p.ItemSeparator,
		hasItemSep:    p.HasItemSeparator,
		omitXMLDecl:   p.OmitXMLDeclaration,
	}
	if sp.nodeMethod == "" {
		sp.nodeMethod = "xml"
	}
	return sp
}

// SerializeJSON renders a raw result sequence under the json output method
// (Serialization 3.1 §9): a map becomes a JSON object, an array a JSON array,
// a string/number/boolean its JSON counterpart, any other atomic value its
// string value as a JSON string, and a node the string produced by serializing
// it with json-node-output-method.
//
// The method serializes a VALUE, not a sequence: exactly one item (or none).
// Anything longer is SERE0023 — a host that wants several items joined wants
// the adaptive method instead.
func SerializeJSON(items []Item, p OutputParams) (string, error) {
	sp := p.toSerParams("json")
	switch len(items) {
	case 0:
		// An empty raw result has no JSON value to write. The spec leaves a
		// zero-length result string; "null" would assert a JSON null nobody
		// produced.
		return "", nil
	case 1:
	default:
		return "", fmt.Errorf("err:SERE0023: the json output method cannot serialize a sequence of %d items", len(items))
	}
	v, err := jsToJSONValue(FromItems(items[:1]), &sp)
	if err != nil {
		return "", err
	}
	return jsMarshalOpts(v, jsEmitOpts{maxRune: serMaxRune(p.Encoding), charMap: p.CharacterMap})
}

// SerializeAdaptive renders a raw result sequence under the adaptive output
// method (Serialization 3.1 §10), joining the items with the item-separator
// (a newline by default).
func SerializeAdaptive(items []Item, p OutputParams) (string, error) {
	sp := p.toSerParams("adaptive")
	o, err := serAdaptive(FromItems(items), sp)
	if err != nil {
		return "", err
	}
	return ToString(o), nil
}

// RawResultItem recovers the real XDM item a node in a RAW result sequence
// stands for. The XSLT engine builds every result — even a "raw" one — into a
// node tree, so a non-node item arrives either as a RealItem carrier (a map,
// array or function: see RealItem's doc comment on xmltree.Node) or as an
// Atomic-marked text node whose TypeAnno records the value's real type. Both
// round-trips are undone here so the json/adaptive writers see the value the
// stylesheet actually produced (an xs:integer 3, not a text node reading "3").
// Any node that is a genuine node stays itself.
func RawResultItem(it Item) Item {
	n, ok := it.(*xmltree.Node)
	if !ok || n == nil {
		return it
	}
	if n.RealItem != nil {
		return n.RealItem
	}
	if n.Kind == xmltree.KindText && n.Atomic {
		if a, ok := nodeTypedValue(n); ok {
			return a
		}
		// An atomic item whose type was not preserved (several values merged
		// into one text run, or a type with no tag) is a string — which is
		// what its string value already is.
		return NewString(n.StringValue())
	}
	return it
}

// RawResultItems applies RawResultItem across a whole sequence.
func RawResultItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = RawResultItem(it)
	}
	return out
}
