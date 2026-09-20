package xsd

import (
	"regexp"
	"strconv"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"strings"
)

// Structural validation of the schema document against the "schema for schemas"
// grammar: each XSD element admits only a fixed set of attributes. Unknown
// attributes make the schema invalid. The allowed-attribute sets are the union
// of XSD 1.0 and 1.1 (so a 1.1 attribute is never wrongly rejected in 1.0 mode);
// `id` is permitted everywhere, and foreign-namespace attributes are always
// allowed. Modeled on internal/xslt/validate.go.
//
// Only the attribute dimension is checked (not child-element grammar), which is
// enough to catch the large "misspelled / misplaced attribute" cluster while
// keeping false-positive risk low.

var xsdAllowedAttrs = map[string]map[string]bool{
	"schema":             set("targetNamespace", "version", "elementFormDefault", "attributeFormDefault", "blockDefault", "finalDefault", "defaultAttributes", "xpathDefaultNamespace", "lang"),
	"element":            set("name", "ref", "type", "minOccurs", "maxOccurs", "default", "fixed", "nillable", "abstract", "substitutionGroup", "block", "final", "form", "targetNamespace"),
	"attribute":          set("name", "ref", "type", "use", "default", "fixed", "form", "targetNamespace", "inheritable"),
	"complexType":        set("name", "abstract", "mixed", "block", "final", "defaultAttributesApply"),
	"simpleType":         set("name", "final"),
	"complexContent":     set("mixed"),
	"simpleContent":      set(),
	"restriction":        set("base"),
	"extension":          set("base"),
	"sequence":           set("minOccurs", "maxOccurs"),
	"choice":             set("minOccurs", "maxOccurs"),
	"all":                set("minOccurs", "maxOccurs"),
	"group":              set("name", "ref", "minOccurs", "maxOccurs"),
	"attributeGroup":     set("name", "ref"),
	"any":                set("namespace", "processContents", "minOccurs", "maxOccurs", "notNamespace", "notQName"),
	"anyAttribute":       set("namespace", "processContents", "notNamespace", "notQName"),
	"key":                set("name", "ref"),
	"keyref":             set("name", "ref", "refer"),
	"unique":             set("name", "ref"),
	"selector":           set("xpath", "xpathDefaultNamespace"),
	"field":              set("xpath", "xpathDefaultNamespace"),
	"annotation":         set(),
	"appinfo":            set("source"),
	"documentation":      set("source", "lang"),
	"notation":           set("name", "public", "system"),
	"import":             set("namespace", "schemaLocation"),
	"include":            set("schemaLocation"),
	"redefine":           set("schemaLocation"),
	"override":           set("schemaLocation"),
	"list":               set("itemType"),
	"union":              set("memberTypes"),
	"enumeration":        set("value"),
	"pattern":            set("value"),
	"whiteSpace":         set("value", "fixed"),
	"length":             set("value", "fixed"),
	"minLength":          set("value", "fixed"),
	"maxLength":          set("value", "fixed"),
	"minInclusive":       set("value", "fixed"),
	"maxInclusive":       set("value", "fixed"),
	"minExclusive":       set("value", "fixed"),
	"maxExclusive":       set("value", "fixed"),
	"totalDigits":        set("value", "fixed"),
	"fractionDigits":     set("value", "fixed"),
	"explicitTimezone":   set("value", "fixed"),
	"assert":             set("test", "xpathDefaultNamespace"),
	"assertion":          set("test", "xpathDefaultNamespace"),
	"alternative":        set("test", "type", "xpathDefaultNamespace"),
	"openContent":        set("mode"),
	"defaultOpenContent": set("mode", "appliesToEmpty"),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names)+1)
	m["id"] = true
	for _, n := range names {
		m[n] = true
	}
	return m
}

func cset(names ...string) map[string]bool {
	m := make(map[string]bool, len(names)+1)
	m["annotation"] = true
	for _, n := range names {
		m[n] = true
	}
	return m
}

// xsd11OnlyElements are schema constructs XSD 1.1 introduced; their appearance
// in a schema compiled under 1.0 rules is a schema error (complex018).
var xsd11OnlyElements = map[string]bool{
	"openContent":        true,
	"defaultOpenContent": true,
	"assert":             true,
	"alternative":        true,
}

// xsdAllowedChildren gives the permitted child elements for XSD elements whose
// content model is unambiguous (restriction/extension are omitted — their
// children differ between the simple- and complex-content contexts). annotation
// is allowed everywhere (added by cset). Elements absent from this map are not
// child-checked.
var xsdAllowedChildren = map[string]map[string]bool{
	"redefine":           cset("simpleType", "complexType", "group", "attributeGroup"),
	"element":            cset("simpleType", "complexType", "key", "keyref", "unique", "alternative"),
	"attribute":          cset("simpleType"),
	"simpleType":         cset("restriction", "list", "union"),
	"complexType":        cset("simpleContent", "complexContent", "group", "sequence", "choice", "all", "attribute", "attributeGroup", "anyAttribute", "assert", "openContent"),
	"complexContent":     cset("restriction", "extension"),
	"simpleContent":      cset("restriction", "extension"),
	"sequence":           cset("element", "group", "sequence", "choice", "any"),
	"choice":             cset("element", "group", "sequence", "choice", "any"),
	"all":                cset("element", "group", "any"),
	"group":              cset("sequence", "choice", "all"),
	"attributeGroup":     cset("attribute", "attributeGroup", "anyAttribute"),
	"key":                cset("selector", "field"),
	"keyref":             cset("selector", "field"),
	"unique":             cset("selector", "field"),
	"list":               cset("simpleType"),
	"union":              cset("simpleType"),
	"selector":           cset(),
	"field":              cset(),
	"any":                cset(),
	"anyAttribute":       cset(),
	"openContent":        cset("any"),
	"defaultOpenContent": cset("any"),
	"notation":           cset(),
	// xs:annotation holds only appinfo/documentation (and never another
	// annotation), and the schema-assembly elements hold only an annotation —
	// which is what makes a top-level-only declaration such as xs:notation
	// invalid when it turns up inside one.
	"annotation": {"appinfo": true, "documentation": true},
	"include":    cset(),
	"import":     cset(),
	"schema": cset("include", "import", "redefine", "override", "element", "attribute",
		"notation", "simpleType", "complexType", "group", "attributeGroup",
		"defaultOpenContent"),
	"enumeration":    cset(),
	"pattern":        cset(),
	"whiteSpace":     cset(),
	"length":         cset(),
	"minLength":      cset(),
	"maxLength":      cset(),
	"minInclusive":   cset(),
	"maxInclusive":   cset(),
	"minExclusive":   cset(),
	"maxExclusive":   cset(),
	"totalDigits":    cset(),
	"fractionDigits": cset(),
}

// facetChildren: the constraining facets (plus annotation), for simple-type and
// simpleContent restrictions.
var facetChildren = cset("simpleType", "minExclusive", "minInclusive", "maxExclusive",
	"maxInclusive", "totalDigits", "fractionDigits", "length", "minLength", "maxLength",
	"enumeration", "whiteSpace", "pattern", "assertion", "explicitTimezone")

// derivChildrenByParent gives, per parent element, the allowed children of a
// restriction/extension nested under it.
var derivChildrenByParent = map[string]map[string]map[string]bool{
	"simpleType": {
		"restriction": facetChildren,
	},
	"simpleContent": {
		"restriction": union(facetChildren, cset("attribute", "attributeGroup", "anyAttribute", "assert")),
		"extension":   cset("attribute", "attributeGroup", "anyAttribute", "assert"),
	},
	"complexContent": {
		"restriction": cset("group", "sequence", "choice", "all", "attribute", "attributeGroup", "anyAttribute", "assert", "openContent"),
		"extension":   cset("group", "sequence", "choice", "all", "attribute", "attributeGroup", "anyAttribute", "assert", "openContent"),
	},
}

func union(a, b map[string]bool) map[string]bool {
	m := make(map[string]bool, len(a)+len(b))
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	return m
}

// reNonNegInt is the nonNegativeInteger lexical space (optional +, digits).
var reNonNegInt = regexp.MustCompile(`^\+?[0-9]+$`)

// ncNameElems are the XSD elements whose @name must be a valid NCName.
var ncNameElems = map[string]bool{
	"element": true, "attribute": true, "simpleType": true, "complexType": true,
	"group": true, "attributeGroup": true, "notation": true,
	"key": true, "keyref": true, "unique": true,
}

// occursValue parses a whiteSpace-collapsed occurrence value, reporting whether
// it is a valid nonNegativeInteger; on overflow it returns a very large sentinel
// (still valid) so the min≤max comparison stays correct.
func occursValue(s string) (int, bool) {
	if !reNonNegInt.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 1 << 60, true
	}
	return n, true
}

// checkOccurs validates the minOccurs/maxOccurs attributes on a particle element:
// minOccurs is a nonNegativeInteger, maxOccurs is a nonNegativeInteger or
// "unbounded", and minOccurs must not exceed maxOccurs.
func checkOccurs(el *xmltree.Node) error {
	minV, maxV := 1, 1
	if v, ok := el.AttrLocal("minOccurs"); ok {
		n, valid := occursValue(applyWhiteSpace("collapse", v))
		if !valid {
			return invalidf("", "invalid minOccurs %q", v)
		}
		minV = n
	}
	if v, ok := el.AttrLocal("maxOccurs"); ok {
		switch cv := applyWhiteSpace("collapse", v); cv {
		case "unbounded":
			maxV = 1 << 62
		default:
			n, valid := occursValue(cv)
			if !valid {
				return invalidf("", "invalid maxOccurs %q", v)
			}
			maxV = n
		}
	}
	if minV > maxV {
		return invalidf("", "minOccurs (%d) exceeds maxOccurs", minV)
	}
	return nil
}

// checkDeclConstraints enforces the syntactic Schema-Representation constraints
// on xs:attribute and xs:element declarations that are independent of the wider
// schema (mutual exclusions between attributes / child type definitions). These
// never fire on a valid schema.
func checkDeclConstraints(el *xmltree.Node, v Version) error {
	has := func(a string) bool { _, ok := el.AttrLocal(a); return ok }
	val := func(a string) string { v, _ := el.AttrLocal(a); return v }
	hasChild := func(local string) bool {
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindElement && ch.Name.Space == xsNS && ch.Name.Local == local {
				return true
			}
		}
		return false
	}
	// An identity constraint is `annotation? selector field+` — exactly one
	// selector, at least one field, selector before every field, and (for keyref)
	// a @refer. A @name is required unless this is the 1.1 @ref form.
	switch el.Name.Local {
	case "key", "keyref", "unique":
		nSel, nField, sawSel := 0, 0, false
		for _, ch := range el.Children {
			if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
				continue
			}
			switch ch.Name.Local {
			case "annotation":
			case "selector":
				if sawSel || nField > 0 {
					return invalidf("", "xs:%s must have exactly one xs:selector, before its fields", el.Name.Local)
				}
				sawSel, nSel = true, nSel+1
			case "field":
				if !sawSel {
					return invalidf("", "xs:%s: xs:field must follow the xs:selector", el.Name.Local)
				}
				nField++
			}
		}
		if !has("ref") {
			if nSel != 1 || nField == 0 {
				return invalidf("", "xs:%s requires one xs:selector followed by at least one xs:field", el.Name.Local)
			}
			if !has("name") {
				return invalidf("", "xs:%s requires a name", el.Name.Local)
			}
			if el.Name.Local == "keyref" && !has("refer") {
				return invalidf("", "xs:keyref requires a refer")
			}
		}
	}

	// Position-dependent attributes. A top-level declaration is named and has no
	// occurrence or use of its own; a local one is the mirror image.
	topLevel := el.Parent != nil && el.Parent.Kind == xmltree.KindElement &&
		el.Parent.Name.Space == xsNS &&
		(el.Parent.Name.Local == "schema" || el.Parent.Name.Local == "redefine" || el.Parent.Name.Local == "override")
	switch el.Name.Local {
	case "complexType", "group", "attributeGroup":
		if topLevel && !has("name") {
			return invalidf("", "a top-level xs:%s requires a name", el.Name.Local)
		}
	case "element", "attribute":
		if topLevel {
			if !has("name") {
				return invalidf("", "a top-level xs:%s requires a name", el.Name.Local)
			}
			for _, a := range []string{"minOccurs", "maxOccurs", "use", "form", "ref"} {
				if has(a) {
					return invalidf("", "a top-level xs:%s must not have @%s", el.Name.Local, a)
				}
			}
		}
	}

	// The particle child of a NAMED model-group definition carries no occurrence
	// of its own — the occurrence belongs to the group reference.
	if el.Name.Local == "group" && has("name") {
		for _, ch := range el.Children {
			if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
				continue
			}
			switch ch.Name.Local {
			case "sequence", "choice", "all":
				if _, ok := ch.AttrLocal("minOccurs"); ok {
					return invalidf("", "the content of a named xs:group must not have minOccurs")
				}
				if _, ok := ch.AttrLocal("maxOccurs"); ok {
					return invalidf("", "the content of a named xs:group must not have maxOccurs")
				}
			}
		}
	}

	// A reference carries no content of its own: xs:element/xs:attribute/
	// xs:group/xs:attributeGroup with a @ref may hold nothing but an annotation.
	switch el.Name.Local {
	case "element", "attribute", "group", "attributeGroup":
		if has("ref") {
			for _, ch := range el.Children {
				if ch.Kind == xmltree.KindElement && ch.Name.Space == xsNS && ch.Name.Local != "annotation" {
					return invalidf("", "xs:%s with a ref must not have %s children", el.Name.Local, ch.Name.Local)
				}
			}
		}
	}

	// An element/attribute may hold at most one inline type definition.
	if el.Name.Local == "attribute" || el.Name.Local == "element" {
		types := 0
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindElement && ch.Name.Space == xsNS &&
				(ch.Name.Local == "simpleType" || ch.Name.Local == "complexType") {
				types++
			}
		}
		if types > 1 {
			return invalidf("", "xs:%s may have at most one inline type definition", el.Name.Local)
		}
	}
	switch el.Name.Local {
	case "complexType", "restriction", "extension", "group", "attributeGroup":
		groups, anyAttrs, seenAttr, seenAnyAttr := 0, 0, false, false
		for _, ch := range el.Children {
			if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
				continue
			}
			switch ch.Name.Local {
			case "group", "all", "choice", "sequence":
				groups++
				// The content model must precede attribute declarations.
				if seenAttr {
					return invalidf("", "model group must come before attributes in xs:%s", el.Name.Local)
				}
			case "anyAttribute":
				anyAttrs++
				seenAttr = true
				seenAnyAttr = true
			case "attribute", "attributeGroup":
				seenAttr = true
				// The attribute wildcard must be the last of the attribute uses.
				if seenAnyAttr {
					return invalidf("", "anyAttribute must be the last attribute in xs:%s", el.Name.Local)
				}
			}
		}
		// A complex type / derivation body admits at most one model-group particle
		// (the content model) and at most one attribute wildcard.
		if groups > 1 {
			return invalidf("", "xs:%s has more than one model-group child", el.Name.Local)
		}
		if anyAttrs > 1 {
			return invalidf("", "xs:%s has more than one anyAttribute", el.Name.Local)
		}
	}
	// Every named schema component's name must be a valid NCName. (Top-level names
	// are re-checked in the loader; this also covers local/anonymous decls.)
	if name, ok := el.AttrLocal("name"); ok && ncNameElems[el.Name.Local] {
		if !isNCName(applyWhiteSpace("collapse", name)) {
			return invalidf("", "invalid name %q for xs:%s", name, el.Name.Local)
		}
	}
	// An inline type definition (a simpleType nested in restriction/list/union, or a
	// complexType nested in an element) must be anonymous.
	if _, hasName := el.AttrLocal("name"); hasName && el.Parent != nil && el.Parent.Name.Space == xsNS {
		pl := el.Parent.Name.Local
		if el.Name.Local == "simpleType" && (pl == "restriction" || pl == "list" || pl == "union") ||
			el.Name.Local == "complexType" && pl == "element" {
			return invalidf("", "an inline xs:%s must not have a name", el.Name.Local)
		}
	}
	switch el.Name.Local {
	case "all":
		// cos-all-limited: an xs:all has minOccurs 0/1, maxOccurs 1, and (1.0 only)
		// each of its member particles has maxOccurs ≤ 1. XSD 1.1 drops the member
		// restriction — a member may repeat, and xs:any / a group ref to another
		// xs:all may appear alongside the element members.
		if a, ok := el.AttrLocal("maxOccurs"); ok {
			switch applyWhiteSpace("collapse", a) {
			case "1":
			case "0":
				// XSD 1.1 allows maxOccurs="0" on xs:all (mgO001/mgO018).
				if v == Version10 {
					return invalidf("", "xs:all must have maxOccurs=1")
				}
			default:
				return invalidf("", "xs:all must have maxOccurs=1")
			}
		}
		if a, ok := el.AttrLocal("minOccurs"); ok {
			switch applyWhiteSpace("collapse", a) {
			case "0", "1":
			default:
				return invalidf("", "xs:all minOccurs must be 0 or 1")
			}
		}
		if v == Version10 {
			for _, ch := range el.Children {
				if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS || ch.Name.Local != "element" {
					continue
				}
				if a, ok := ch.AttrLocal("maxOccurs"); ok {
					switch applyWhiteSpace("collapse", a) {
					case "0", "1":
					default:
						return invalidf("", "a member of xs:all must have maxOccurs 0 or 1")
					}
				}
			}
		}
	case "sequence", "choice":
		// An xs:all may only be the whole content model, never nested.
		for _, ch := range el.Children {
			if isXS(ch, "all") {
				return invalidf("", "xs:all may only appear as the top-level content model")
			}
		}
	case "appinfo", "documentation":
		// @source is typed xs:anyURI — under 1.0 it must be an RFC 2396 URI
		// reference (anyURI_a001; @public stays out, it is xs:token per errata).
		if v == Version10 {
			if src, ok := el.AttrLocal("source"); ok {
				if !validAnyURI10(applyWhiteSpace("collapse", src)) {
					return invalidf("", "invalid anyURI %q for @source", src)
				}
			}
		}
	case "openContent", "defaultOpenContent":
		// S4S consistency of @mode with the xs:any child: mode="none" forbids
		// it (open036, spec bug 7069), any other mode requires it (s3_4_1si01s),
		// and defaultOpenContent has no "none" at all.
		mode := "interleave"
		if m, ok := el.AttrLocal("mode"); ok {
			mode = applyWhiteSpace("collapse", m)
		}
		switch mode {
		case "none", "interleave", "suffix":
		default:
			return invalidf("", "invalid xs:%s mode %q", el.Name.Local, mode)
		}
		if el.Name.Local == "defaultOpenContent" && mode == "none" {
			return invalidf("", "xs:defaultOpenContent mode may not be \"none\"")
		}
		hasAny := false
		for _, ch := range el.Children {
			if isXS(ch, "any") {
				hasAny = true
			}
		}
		if mode == "none" && hasAny {
			return invalidf("", "xs:openContent with mode=\"none\" must not have an xs:any child")
		}
		if mode != "none" && !hasAny {
			return invalidf("", "xs:%s with mode=%q requires an xs:any child", el.Name.Local, mode)
		}
		// Inside a complexContent RESTRICTION an openContent needs a particle
		// sibling to attach to (spec bug 16786, complex018); extensions are
		// exempt (s3_4_1si06 — the base supplies their particle).
		if el.Name.Local == "openContent" && el.Parent != nil && isXS(el.Parent, "restriction") {
			hasParticle := false
			for _, sib := range el.Parent.Children {
				if isXS(sib, "group") || isXS(sib, "sequence") || isXS(sib, "choice") || isXS(sib, "all") {
					hasParticle = true
					break
				}
			}
			if !hasParticle {
				return invalidf("", "xs:openContent in a restriction requires a sibling particle")
			}
		}
		// The open-content xs:any is not a particle: it admits no occurrence
		// attributes (bug 15618, open048).
		for _, ch := range el.Children {
			if !isXS(ch, "any") {
				continue
			}
			if _, has := ch.AttrLocal("minOccurs"); has {
				return invalidf("", "minOccurs is not allowed on an open-content xs:any")
			}
			if _, has := ch.AttrLocal("maxOccurs"); has {
				return invalidf("", "maxOccurs is not allowed on an open-content xs:any")
			}
		}
	case "simpleType", "simpleContent":
		// A simple-type (or simpleContent) restriction must not derive from
		// xs:anySimpleType or xs:anyType (the ur-types cannot be a simple-type
		// base; stZ010).
		for _, ch := range el.Children {
			if !isXS(ch, "restriction") {
				continue
			}
			if b, ok := ch.AttrLocal("base"); ok {
				switch resolveQName(ch, b) {
				case xname{xsNS, "anySimpleType"}, xname{xsNS, "anyType"}:
					return invalidf("", "a simpleType cannot restrict %s", b)
				case xname{xsNS, "anyAtomicType"}:
					// 1.1 Part 2 §3.4.1: anyAtomicType may not be the base of a
					// user-defined restriction (simple051); it stays usable as
					// an element/attribute type (simple050).
					if v != Version10 {
						return invalidf("", "a simpleType cannot restrict %s", b)
					}
				}
			}
		}
	case "schema", "redefine", "override":
		// A top-level element/attribute/group/attributeGroup is a global
		// definition: it must carry a name (not a ref), and the local-only
		// attributes minOccurs/maxOccurs/form do not apply to it.
		// xs:defaultOpenContent additionally belongs to the schema PROLOG: it
		// must precede every component declaration (s3_4_1si02s).
		seenComponent := false
		for _, ch := range el.Children {
			if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
				continue
			}
			switch ch.Name.Local {
			case "element", "attribute", "notation", "simpleType", "complexType", "group", "attributeGroup":
				seenComponent = true
			case "defaultOpenContent":
				if seenComponent {
					return invalidf("", "xs:defaultOpenContent must precede the schema's component declarations")
				}
			}
			switch ch.Name.Local {
			case "element", "attribute", "group", "attributeGroup":
				if _, ok := ch.AttrLocal("ref"); ok {
					return invalidf("", "a top-level xs:%s must be a definition, not a ref", ch.Name.Local)
				}
				if _, ok := ch.AttrLocal("minOccurs"); ok {
					return invalidf("", "minOccurs is not allowed on a top-level xs:%s", ch.Name.Local)
				}
				if _, ok := ch.AttrLocal("maxOccurs"); ok {
					return invalidf("", "maxOccurs is not allowed on a top-level xs:%s", ch.Name.Local)
				}
				if ch.Name.Local == "element" || ch.Name.Local == "attribute" {
					if _, ok := ch.AttrLocal("form"); ok {
						return invalidf("", "form is not allowed on a top-level xs:%s", ch.Name.Local)
					}
				}
				if ch.Name.Local == "attribute" {
					if _, ok := ch.AttrLocal("use"); ok {
						return invalidf("", "use is not allowed on a top-level attribute")
					}
				}
			}
		}
	}
	switch el.Name.Local {
	case "attribute":
		hasRef := has("ref")
		hasType := has("type")
		hasST := hasChild("simpleType")
		if has("default") && has("fixed") {
			return invalidf("", "xs:attribute has both default and fixed")
		}
		if hasRef && (hasType || has("form") || has("name") || hasST) {
			return invalidf("", "xs:attribute ref is not compatible with name/type/form/simpleType")
		}
		if hasType && hasST {
			return invalidf("", "xs:attribute has both a type attribute and a simpleType child")
		}
		if has("default") {
			switch val("use") {
			case "required", "prohibited":
				return invalidf("", "xs:attribute default requires use=optional")
			}
		}
		if n := val("name"); n == "xmlns" {
			return invalidf("", "an attribute declaration must not be named xmlns")
		}
	case "element":
		if has("default") && has("fixed") {
			return invalidf("", "xs:element has both default and fixed")
		}
		if has("ref") {
			for _, a := range []string{"name", "type", "form", "nillable", "block",
				"final", "abstract", "substitutionGroup", "default", "fixed"} {
				if has(a) {
					return invalidf("", "xs:element ref is not compatible with %s", a)
				}
			}
			if hasChild("simpleType") || hasChild("complexType") {
				return invalidf("", "xs:element ref must not have an inline type")
			}
		}
		if has("type") && (hasChild("simpleType") || hasChild("complexType")) {
			return invalidf("", "xs:element has both a type attribute and an inline type")
		}
		// abstract/final/substitutionGroup are only permitted on a global (top-level)
		// element declaration.
		if p := el.Parent; !isTopLevelParent(p) {
			for _, a := range []string{"abstract", "final", "substitutionGroup"} {
				if has(a) {
					return invalidf("", "%s is not allowed on a local element", a)
				}
			}
		}
	}
	return nil
}

// isTopLevelParent reports whether p is an xs:schema/redefine/override element
// (so a component directly under it is a global definition).
func isTopLevelParent(p *xmltree.Node) bool {
	if p == nil || p.Name.Space != xsNS {
		return false
	}
	switch p.Name.Local {
	case "schema", "redefine", "override":
		return true
	}
	return false
}

// validateSchemaAttrs walks el and every descendant XSD element, rejecting an
// unqualified attribute that the element's grammar does not allow.
func validateSchemaAttrs(el *xmltree.Node, v Version) error {
	if el.Kind == xmltree.KindElement && el.Name.Space == xsNS {
		if allowed, known := xsdAllowedAttrs[el.Name.Local]; known {
			for _, a := range el.Attrs {
				if a.Name.Space != "" {
					continue // foreign / xsi:* attributes are permitted
				}
				if !allowed[a.Name.Local] {
					return invalidf("", "attribute %q is not allowed on xs:%s", a.Name.Local, el.Name.Local)
				}
			}
		}
		if lang, ok := el.Attr(xmlNS, "lang"); ok && !reLanguage.MatchString(lang) {
			return invalidf("", "xml:lang %q is not a valid xs:language", lang)
		}
		// The schema-for-schemas gives component elements element-only content:
		// no character data and no foreign-namespace children anywhere except
		// inside xs:appinfo / xs:documentation (notatG001/002/003).
		if el.Name.Local != "appinfo" && el.Name.Local != "documentation" {
			for _, ch := range el.Children {
				if ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "" {
					return invalidf("", "character data is not allowed inside xs:%s", el.Name.Local)
				}
				if ch.Kind == xmltree.KindElement && ch.Name.Space != xsNS {
					return invalidf("", "element {%s}%s is not allowed inside xs:%s", ch.Name.Space, ch.Name.Local, el.Name.Local)
				}
			}
		}
		if allowedCh, known := xsdAllowedChildren[el.Name.Local]; known {
			for _, ch := range el.Children {
				if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
					continue // text / foreign elements handled elsewhere
				}
				if v == Version10 && el.Name.Local == "all" && ch.Name.Local == "any" {
					return invalidf("", "an XSD 1.0 xs:all group may contain only element declarations")
				}
				if v == Version10 && xsd11OnlyElements[ch.Name.Local] {
					return invalidf("", "xs:%s is an XSD 1.1 construct (not valid in a 1.0 schema)", ch.Name.Local)
				}
				if !allowedCh[ch.Name.Local] {
					return invalidf("", "element xs:%s is not allowed inside xs:%s", ch.Name.Local, el.Name.Local)
				}
			}
		}
		if el.Name.Local == "element" {
			// Grammar order: annotation?, (simpleType|complexType)?, then the
			// identity constraints — a type AFTER unique/key/keyref is invalid
			// (idZ003).
			seenIC := false
			for _, ch := range el.Children {
				if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
					continue
				}
				switch ch.Name.Local {
				case "unique", "key", "keyref":
					seenIC = true
				case "simpleType", "complexType":
					if seenIC {
						return invalidf("", "xs:%s must precede the identity constraints of xs:element", ch.Name.Local)
					}
				}
			}
		}
		// Every component element admits at most one xs:annotation, and it must be
		// the first child. Only xs:schema / xs:redefine / xs:override may carry
		// several annotations interspersed with their other children.
		switch el.Name.Local {
		case "schema", "redefine", "override":
			// several annotations allowed, interspersed
		case "appinfo", "documentation":
			// arbitrary content — a nested xs:annotation here is just data
		default:
			seenOther, annots := false, 0
			for _, ch := range el.Children {
				if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
					continue
				}
				if ch.Name.Local == "annotation" {
					if annots++; annots > 1 {
						return invalidf("", "xs:%s may have at most one xs:annotation", el.Name.Local)
					}
					if seenOther {
						return invalidf("", "xs:annotation must be the first child of xs:%s", el.Name.Local)
					}
				} else {
					seenOther = true
				}
			}
		}
		if err := checkOccurs(el); err != nil {
			return err
		}
		if err := checkDeclConstraints(el, v); err != nil {
			return err
		}
		// restriction/extension children depend on their context (simple type vs
		// simpleContent vs complexContent), so validate them from the parent.
		if allowedD, ok := derivChildrenByParent[el.Name.Local]; ok {
			for _, body := range el.Children {
				if !isXS(body, "restriction") && !isXS(body, "extension") {
					continue
				}
				allowed := allowedD[body.Name.Local]
				for _, ch := range body.Children {
					if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
						continue
					}
					if !allowed[ch.Name.Local] {
						return invalidf("", "element xs:%s is not allowed inside xs:%s", ch.Name.Local, body.Name.Local)
					}
				}
			}
		}
	}
	if el.Kind == xmltree.KindElement && el.Name.Space == xsNS &&
		(el.Name.Local == "appinfo" || el.Name.Local == "documentation") {
		return nil // arbitrary content — nothing below is schema grammar
	}
	for _, ch := range el.Children {
		if err := validateSchemaAttrs(ch, v); err != nil {
			return err
		}
	}
	return nil
}
