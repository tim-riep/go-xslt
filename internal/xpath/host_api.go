package xpath

import "github.com/tim-riep/go-xslt/internal/xmltree"

// ItemString returns a display string for a single XPath item. For nodes and
// atomic values it matches the engine's internal per-item stringification. Maps,
// arrays and function items have no string value (atomizing them is a type
// error, so the internal form is empty); for those it returns an adaptive
// serialization — map{k:v,…}, [m,…], name#arity — so host tools (the desktop
// XPath tester) show their structure rather than a blank cell.
func ItemString(it Item) string {
	switch it.(type) {
	case *Map, *Array, *Function:
		if s, err := serAdaptiveItem(it, serParams{}); err == nil {
			return s
		}
	}
	return itemString(it)
}

// ItemKind returns a short, human-readable type label for a single XPath item:
// a node kind ("element", "attribute", "text", "comment", "processing-instruction",
// "namespace", "document"), an atomic type's QName ("xs:integer", …), or one of
// "map" / "array" / "function". Used by host tools to annotate result items.
func ItemKind(it Item) string {
	switch v := it.(type) {
	case *xmltree.Node:
		switch v.Kind {
		case xmltree.KindElement:
			return "element"
		case xmltree.KindAttribute:
			return "attribute"
		case xmltree.KindText:
			return "text"
		case xmltree.KindComment:
			return "comment"
		case xmltree.KindPI:
			return "processing-instruction"
		case xmltree.KindNamespace:
			return "namespace"
		case xmltree.KindDocument:
			return "document"
		}
		return "node"
	case *Atomic:
		return v.T.String()
	case *Map:
		return "map"
	case *Array:
		return "array"
	case *Function:
		return "function"
	case string:
		// The value model carries some atomics as bare Go scalars.
		return "xs:string"
	case float64:
		return "xs:double"
	case bool:
		return "xs:boolean"
	}
	return "item"
}
