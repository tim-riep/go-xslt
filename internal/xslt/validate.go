package xslt

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Static validation of XSLT-namespace elements: allowed/required attributes
// (XTSE0090/XTSE0010), attribute values (XTSE0020), and content models
// (XTSE0010). Only elements listed in xsltElemSpecs are checked, so unknown
// (future/extension) elements are never falsely rejected.

// vSpec describes the static shape of one XSLT element.
type vSpec struct {
	attrs    string // space-separated allowed no-namespace attributes ("" = none beyond standard)
	required string // space-separated required attributes
	children string // content model: "" = unchecked, "empty", or a space-separated child whitelist
	noText   bool   // non-whitespace text content is not allowed
}

// vStdAttrs are the standard attributes permitted on every XSLT element.
var vStdAttrs = map[string]bool{
	"version": true, "exclude-result-prefixes": true, "extension-element-prefixes": true,
	"xpath-default-namespace": true, "expand-text": true, "default-collation": true,
	"use-when": true, "default-validation": true, "default-mode": true,
}

var xsltElemSpecs = map[string]vSpec{
	"stylesheet": {attrs: "id input-type-annotations", required: "version"},
	"transform":  {attrs: "id input-type-annotations", required: "version"},
	// xsl:package is accepted as a module root (compile.go); @name /
	// @package-version / @declared-modes are the three attributes it has
	// beyond xsl:stylesheet's.
	"package": {attrs: "id input-type-annotations name package-version declared-modes", required: "version"},
	// xsl:use-package and its two children (XSLT 3.0 §3.5). xsl:use-package is
	// consumed by gatherModules, which splices the named library package in at
	// lower import precedence; xsl:override's declarations join the USING
	// module, and xsl:accept only adjusts accepted visibility.
	"use-package": {attrs: "name package-version", required: "name", children: "accept override", noText: true},
	"accept":      {attrs: "component names visibility", required: "component names visibility", children: "empty", noText: true},
	"override":    {children: "template function variable param attribute-set", noText: true},

	"template":        {attrs: "match name priority mode as visibility"},
	"apply-templates": {attrs: "select mode", children: "sort with-param", noText: true},
	"apply-imports":   {attrs: "", children: "with-param", noText: true},
	"next-match":      {attrs: "", children: "with-param fallback", noText: true},
	"call-template":   {attrs: "name", required: "name", children: "with-param", noText: true},

	"for-each": {attrs: "select", required: "select"},
	"for-each-group": {attrs: "select group-by group-adjacent group-starting-with " +
		"group-ending-with collation composite bind-group bind-grouping-key", required: "select"},
	"if":        {attrs: "test", required: "test"},
	"choose":    {attrs: "", children: "when otherwise", noText: true},
	"when":      {attrs: "test", required: "test"},
	"otherwise": {attrs: ""},

	"value-of": {attrs: "select separator disable-output-escaping"},
	"copy-of":  {attrs: "select copy-accumulators copy-namespaces type validation", required: "select", children: "empty", noText: true},
	"copy":     {attrs: "select copy-namespaces inherit-namespaces use-attribute-sets type validation"},
	"text":     {attrs: "disable-output-escaping", children: "empty"},
	"sequence": {attrs: "select"},

	"variable":   {attrs: "name select as static visibility", required: "name"},
	"param":      {attrs: "name select as required tunnel static", required: "name"},
	"with-param": {attrs: "name select as tunnel", required: "name"},

	"attribute":              {attrs: "name namespace select separator type validation", required: "name"},
	"element":                {attrs: "name namespace inherit-namespaces use-attribute-sets type validation", required: "name"},
	"attribute-set":          {attrs: "name use-attribute-sets visibility streamable", required: "name", children: "attribute", noText: true},
	"namespace":              {attrs: "name select", required: "name"},
	"comment":                {attrs: "select"},
	"processing-instruction": {attrs: "name select", required: "name"},
	"document":               {attrs: "validation type"},

	"sort": {attrs: "select lang order collation stable case-order data-type"},

	"key": {attrs: "name match use composite collation", required: "name match"},
	"decimal-format": {attrs: "name decimal-separator grouping-separator infinity minus-sign " +
		"exponent-separator NaN percent per-mille zero-digit digit pattern-separator",
		children: "empty", noText: true},
	"import":         {attrs: "href", required: "href", children: "empty", noText: true},
	"include":        {attrs: "href", required: "href", children: "empty", noText: true},
	"strip-space":    {attrs: "elements", required: "elements", children: "empty", noText: true},
	"preserve-space": {attrs: "elements", required: "elements", children: "empty", noText: true},
	"output": {attrs: "name method allow-duplicate-names build-tree byte-order-mark " +
		"cdata-section-elements doctype-public doctype-system encoding escape-uri-attributes " +
		"html-version include-content-type indent item-separator json-node-output-method " +
		"media-type normalization-form omit-xml-declaration parameter-document standalone " +
		"suppress-indentation undeclare-prefixes use-character-maps version",
		children: "empty", noText: true},
	"namespace-alias":  {attrs: "stylesheet-prefix result-prefix", required: "stylesheet-prefix result-prefix", children: "empty", noText: true},
	"character-map":    {attrs: "name use-character-maps", required: "name", children: "output-character", noText: true},
	"output-character": {attrs: "character string", required: "character string", children: "empty", noText: true},

	"number": {attrs: "value select level count from format lang letter-value ordinal " +
		"start-at grouping-separator grouping-size"},
	"message": {attrs: "select terminate error-code"},
	"assert":  {attrs: "test select error-code", required: "test"},

	"function": {attrs: "name as visibility override override-extension-function cache " +
		"new-each-time identity-sensitive streamability", required: "name"},

	"analyze-string":         {attrs: "select regex flags", required: "select regex", children: "matching-substring non-matching-substring fallback", noText: true},
	"matching-substring":     {attrs: ""},
	"non-matching-substring": {attrs: ""},
	"fallback":               {attrs: ""},
	"where-populated":        {attrs: ""},
	"fork":                   {attrs: "", children: "sequence for-each-group", noText: true},
	"on-empty":               {attrs: "select"},
	"on-non-empty":           {attrs: "select"},
	"iterate":                {attrs: "select", required: "select"},
	"next-iteration":         {attrs: "", children: "with-param", noText: true},
	"break":                  {attrs: "select"},
	"on-completion":          {attrs: "select"},
	"try":                    {attrs: "select rollback-output"},
	"catch":                  {attrs: "errors select"},
	"perform-sort":           {attrs: "select"},
	"merge":                  {attrs: "", children: "merge-source merge-action fallback", noText: true},
	"merge-action":           {attrs: ""},
	"merge-key":              {attrs: "select lang order collation case-order data-type"},
	"mode": {attrs: "name streamable use-accumulators on-no-match on-multiple-match " +
		"warning-on-no-match warning-on-multiple-match typed visibility", children: "empty", noText: true},
	"accumulator":      {attrs: "name initial-value as streamable visibility", required: "name initial-value", children: "accumulator-rule", noText: true},
	"accumulator-rule": {attrs: "match phase select", required: "match"},
	"source-document":  {attrs: "href streamable use-accumulators validation type", required: "href"},
	"result-document": {attrs: "format href validation type method allow-duplicate-names " +
		"build-tree byte-order-mark cdata-section-elements doctype-public doctype-system " +
		"encoding escape-uri-attributes html-version include-content-type indent " +
		"item-separator json-node-output-method media-type normalization-form " +
		"omit-xml-declaration output-version parameter-document standalone " +
		"suppress-indentation undeclare-prefixes use-character-maps version"},
	"map":       {attrs: ""},
	"map-entry": {attrs: "key select", required: "key"},
	"evaluate": {attrs: "xpath as base-uri with-params context-item namespace-context " +
		"schema-aware", required: "xpath"},
	"global-context-item": {attrs: "as use", children: "empty", noText: true},
	"context-item":        {attrs: "as use", children: "empty", noText: true},
}

// vQNameAttrs lists attributes whose value must be a lexical QName (they are
// not AVTs, so braces or path characters are static errors, XTSE0020).
var vQNameAttrs = map[string]map[string]bool{
	"template":       {"name": true},
	"call-template":  {"name": true},
	"variable":       {"name": true},
	"param":          {"name": true},
	"with-param":     {"name": true},
	"attribute-set":  {"name": true},
	"key":            {"name": true},
	"function":       {"name": true},
	"character-map":  {"name": true},
	"accumulator":    {"name": true},
	"decimal-format": {"name": true},
	// xsl:mode/@name names the mode being declared, so it must be a genuine
	// EQName — not one of the pseudo-mode tokens (#unnamed/#default/#all/
	// #current) that are only meaningful on a DIFFERENT attribute,
	// xsl:template/xsl:apply-templates's own @mode (package-909: name="#unnamed"
	// is XTSE0020, not a synonym for omitting @name).
	"mode": {"name": true},
}

// vBoolAttrs lists non-AVT attributes restricted to the XSLT boolean lexical
// space (yes|no|true|false|1|0).
var vBoolAttrs = map[string]map[string]bool{
	"param":          {"required": true, "tunnel": true, "static": true},
	"with-param":     {"tunnel": true},
	"variable":       {"static": true},
	"copy-of":        {"copy-namespaces": true},
	"text":           {"disable-output-escaping": true},
	"value-of":       {"disable-output-escaping": true},
	"copy":           {"copy-namespaces": true, "inherit-namespaces": true},
	"element":        {"inherit-namespaces": true},
	"key":            {"composite": true},
	"for-each-group": {"composite": true},
	// xsl:output's xs:boolean-valued serialization parameters (they are not
	// AVTs, so the check is purely static): output-0197a..0199a, 0280a..0283a.
	"output": {"indent": true, "omit-xml-declaration": true, "byte-order-mark": true,
		"escape-uri-attributes": true, "include-content-type": true, "undeclare-prefixes": true},
	"mode":          {"streamable": true, "warning-on-no-match": true, "warning-on-multiple-match": true},
	"attribute-set": {"streamable": true},
	"function": {"override": true, "override-extension-function": true, "cache": true,
		"identity-sensitive": true},
}

// vBoolAVTAttrs lists ATTRIBUTE VALUE TEMPLATES whose effective value is
// restricted to the XSLT boolean lexical space. Unlike vBoolAttrs these may
// contain a variable part, so they are only checked when they do not.
var vBoolAVTAttrs = map[string]map[string]bool{
	"message": {"terminate": true},
	"assert":  {"terminate": true},
}

// xsltBool parses an XSLT boolean attribute value (yes|no|true|false|1|0,
// already validated as such by vBoolAttrs) to a Go bool.
func xsltBool(v string) bool {
	switch strings.TrimSpace(v) {
	case "yes", "true", "1":
		return true
	}
	return false
}

// vSelectExcl lists elements where a select attribute is mutually exclusive
// with (some kinds of) content: allowedKids names the XSLT children still
// permitted alongside select (nil = none, i.e. select requires empty content).
var vSelectExcl = map[string]struct {
	code        string
	allowedKids map[string]bool
}{
	"attribute":              {"XTSE0840", nil},
	"value-of":               {"XTSE0870", nil},
	"processing-instruction": {"XTSE0880", nil},
	"comment":                {"XTSE0940", nil},
	"namespace":              {"XTSE0910", map[string]bool{"fallback": true}},
	"sort":                   {"XTSE1015", nil},
	"perform-sort":           {"XTSE1040", map[string]bool{"sort": true, "fallback": true}},
	"break":                  {"XTSE3125", nil},
	"on-completion":          {"XTSE3125", nil},
	"try":                    {"XTSE3140", map[string]bool{"catch": true, "fallback": true}},
	"catch":                  {"XTSE3150", nil},
	"sequence":               {"XTSE3185", map[string]bool{"fallback": true}},
	"merge-key":              {"XTSE3200", nil},
}

// vReservedNSNames lists elements whose name attribute (a declaration, not a
// reference) must not resolve into a reserved namespace (XTSE0080).
var vReservedNSNames = map[string]bool{
	"template": true, "mode": true, "attribute-set": true, "key": true,
	"decimal-format": true, "variable": true, "param": true, "function": true,
	"output": true, "character-map": true,
}

// vLREAttrs lists the XSLT-namespace attributes a literal result element may
// carry — anything else in the XSLT namespace is XTSE0805.
var vLREAttrs = map[string]bool{
	"version": true, "exclude-result-prefixes": true, "extension-element-prefixes": true,
	"xpath-default-namespace": true, "expand-text": true, "default-collation": true,
	"use-when": true, "default-validation": true, "default-mode": true,
	"use-attribute-sets": true, "type": true, "validation": true,
	"inherit-namespaces": true,
}

// checkExcludeResultPrefixes validates an [xsl:]exclude-result-prefixes
// value: every prefix token must be bound (XTSE0808), and #default requires a
// default namespace to be in scope (XTSE0809).
func checkExcludeResultPrefixes(el *xmltree.Node, v string) error {
	for _, tok := range strings.Fields(v) {
		if tok == "#all" {
			continue
		}
		if tok == "#default" {
			if _, ok := el.LookupPrefix(""); !ok {
				return errAt(el, "err:XTSE0809: #default in exclude-result-prefixes but no default namespace is in scope")
			}
			continue
		}
		if _, bound := el.LookupPrefix(tok); !bound {
			return errAt(el, "err:XTSE0808: prefix %q in exclude-result-prefixes has no namespace binding", tok)
		}
	}
	return nil
}

// vOneCharAttrs: xsl:decimal-format attributes that must hold a single character.
var vOneCharAttrs = map[string]bool{
	"decimal-separator": true, "grouping-separator": true, "percent": true,
	"per-mille": true, "zero-digit": true, "digit": true, "pattern-separator": true,
	"exponent-separator": true,
}

// validateTree walks a stylesheet subtree, applying the static checks to every
// element in the XSLT namespace.
func validateTree(el *xmltree.Node, topLevel bool) error {
	if el.Kind != xmltree.KindElement {
		return nil
	}
	// Forwards-compatible processing (XSLT 3.0 §3.9): an XSLT-namespace
	// element this version does not recognize is ignored ALONG WITH ITS
	// CONTENT — nothing inside it is statically checked, even an otherwise
	// valid-looking XSLT instruction (forwards-203/204/205: xsl:accumulator
	// and xsl:use-package appear inside an xsl:future).
	if el.Name.Space == NS && inForwardsCompatScope(el) {
		if _, known := xsltElemSpecs[el.Name.Local]; !known {
			return nil
		}
	}
	if el.Name.Space == NS {
		if err := validateXSLTElement(el); err != nil {
			return err
		}
	}
	if el.Name.Space != NS && el.Kind == xmltree.KindElement {
		if _, ok := el.Attr(NS, "type"); ok && !schemaAwareRun() {
			return errAt(el, "err:XTSE1660: xsl:type requires a schema-aware processor")
		}
		// XTSE1660 fires for a value this run cannot honour — see
		// validationValueOK (schema_property.go) for which those are. The xsl:-
		// prefixed spelling on a literal result element is the same attribute
		// as validateXSLTElement's unprefixed one and must agree with it.
		if v, ok := el.Attr(NS, "validation"); ok && !validationValueOK(v) {
			return errAt(el, "err:XTSE1660: xsl:validation=%q requires a schema-aware processor", v)
		}
		if v, ok := el.Attr(NS, "default-validation"); ok {
			if !validationValueOK(v) {
				return errAt(el, "err:XTSE1660: xsl:default-validation=%q requires a schema-aware processor", v)
			}
			if err := checkDefaultValidationValue(el, v); err != nil {
				return err
			}
		}
		if v, ok := el.Attr(NS, "version"); ok {
			if !isXSDDecimalLexical(strings.TrimSpace(v)) {
				return errAt(el, "err:XTSE0110: xsl:version=%q is not a valid number", v)
			}
			// An xsl:version BELOW 2.0 on a literal result element switches
			// backwards-compatible behaviour on for that subtree. A run that
			// does not claim genuine 1.0-compatible processing
			// (backwardsCompatRun, off by default) must say so rather than
			// silently ignore the request (error-0160a); one that does claim
			// it implements the relaxations instead (inBackwardsCompatScope),
			// so there is nothing to reject. Note this is xsl:version on an
			// LRE only — never xsl:stylesheet/@version, which is how every
			// 1.0 stylesheet in the suite declares itself.
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f < 2.0 && !backwardsCompatRun() {
				return errAt(el, "err:XTDE0160: xsl:version=%q requests backwards compatible behaviour, which this processor does not support", v)
			}
		}
		for _, a := range el.Attrs {
			if a.Name.Space == NS && !vLREAttrs[a.Name.Local] && !inForwardsCompatScope(el) {
				return errAt(el, "err:XTSE0805: xsl:%s is not allowed on a literal result element", a.Name.Local)
			}
		}
		if v, ok := el.Attr(NS, "exclude-result-prefixes"); ok {
			if err := checkExcludeResultPrefixes(el, v); err != nil {
				return err
			}
		}
		if v, ok := el.Attr(NS, "extension-element-prefixes"); ok {
			for _, tok := range strings.Fields(v) {
				pfx := tok
				if pfx == "#default" {
					pfx = ""
				}
				if _, bound := el.LookupPrefix(pfx); !bound {
					return errAt(el, "err:XTSE1430: prefix %q in extension-element-prefixes has no namespace binding", tok)
				}
			}
		}
		if v, ok := el.Attr(NS, "inherit-namespaces"); ok {
			switch strings.TrimSpace(v) {
			case "yes", "no", "true", "false", "1", "0":
			default:
				return errAt(el, "err:XTSE0020: xsl:inherit-namespaces=%q is not an XSLT boolean", v)
			}
		}
		if v, ok := el.Attr(NS, "expand-text"); ok {
			if err := checkXSLTBooleanAttr(el, "xsl:expand-text", v); err != nil {
				return err
			}
		}
	}
	isSheet := el.Name.Space == NS && (el.Name.Local == "stylesheet" || el.Name.Local == "transform" || el.Name.Local == "package")
	for _, ch := range el.Children {
		switch ch.Kind {
		case xmltree.KindText:
			if isSheet && strings.TrimSpace(ch.Value) != "" {
				return errAt(el, "err:XTSE0120: text is not allowed at the top level of a stylesheet")
			}
		case xmltree.KindElement:
			// In forwards-compatibility mode ANY XSLT element that XSLT 3.0
			// does not allow as a child of xsl:stylesheet is ignored along
			// with its content — whether the name is unknown (version-024) or
			// known but out of place (forwards-005 xsl:value-of, forwards-006
			// xsl:transform, forwards-007 xsl:when, forwards-011 xsl:expose).
			if isSheet && ch.Name.Space == NS && !validTopLevelDecls[ch.Name.Local] && inForwardsCompatScope(ch) {
				continue
			}
			if err := validateTree(ch, isSheet); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkIterateExit enforces the position rules for xsl:break and
// xsl:next-iteration (XSLT 3.0 §8.3): each must be the LAST instruction of the
// sequence constructor forming the content of xsl:iterate, or of an
// xsl:if/xsl:when/xsl:otherwise/xsl:try/xsl:catch/xsl:fork that is itself in
// that position — XTSE3120 (iterate-009 puts a sibling after xsl:break,
// iterate-010 wraps it in a literal result element, iterate-011 in an
// xsl:for-each). With no enclosing xsl:iterate at all it is XTSE0010
// (iterate-006 puts xsl:next-iteration in a called template).
//
// For xsl:next-iteration each xsl:with-param must also name a parameter the
// enclosing xsl:iterate declares — XTSE3130 (iterate-022 tries to update the
// enclosing TEMPLATE's parameter).
func checkIterateExit(el *xmltree.Node) error {
	var iter *xmltree.Node
	for cur := el; ; {
		p := cur.Parent
		if p == nil || p.Kind != xmltree.KindElement {
			break
		}
		if !isLastInstruction(p, cur) {
			if hasIterateAncestor(el) {
				return errAt(el, "err:XTSE3120: xsl:%s must be the last instruction in its sequence constructor", el.Name.Local)
			}
			return errAt(el, "err:XTSE0010: xsl:%s must appear within xsl:iterate", el.Name.Local)
		}
		if p.Name.Space == NS {
			if p.Name.Local == "iterate" {
				iter = p
				break
			}
			switch p.Name.Local {
			case "if", "try", "catch", "fork":
				cur = p
				continue
			case "when", "otherwise":
				// A branch of xsl:choose need not be the LAST branch — it is
				// the xsl:choose itself whose position matters next
				// (iterate-003: xsl:break in an xsl:when followed by an
				// xsl:otherwise is perfectly legal).
				c := p.Parent
				if c == nil || c.Kind != xmltree.KindElement || c.Name.Space != NS || c.Name.Local != "choose" {
					break
				}
				cur = c
				continue
			}
		}
		break
	}
	if iter == nil {
		if hasIterateAncestor(el) {
			return errAt(el, "err:XTSE3120: xsl:%s is not in a permitted position within xsl:iterate", el.Name.Local)
		}
		return errAt(el, "err:XTSE0010: xsl:%s must appear within xsl:iterate", el.Name.Local)
	}
	if el.Name.Local != "next-iteration" {
		return nil
	}
	declared := map[string]bool{}
	for _, ch := range el.Parent.Children {
		_ = ch
	}
	for _, ch := range iter.Children {
		if ch.Kind == xmltree.KindElement && ch.Name.Space == NS && ch.Name.Local == "param" {
			if n, ok := ch.AttrLocal("name"); ok {
				declared[clarkName(resolveQName(ch, n))] = true
			}
		}
	}
	for _, ch := range el.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != NS || ch.Name.Local != "with-param" {
			continue
		}
		n, ok := ch.AttrLocal("name")
		if !ok {
			continue
		}
		if !declared[clarkName(resolveQName(ch, n))] {
			return errAt(ch, "err:XTSE3130: xsl:next-iteration/xsl:with-param %q does not match a parameter of the enclosing xsl:iterate", n)
		}
	}
	return nil
}

// hasIterateAncestor reports whether el has an xsl:iterate ancestor at all.
func hasIterateAncestor(el *xmltree.Node) bool {
	for cur := el.Parent; cur != nil && cur.Kind == xmltree.KindElement; cur = cur.Parent {
		if cur.Name.Space == NS && cur.Name.Local == "iterate" {
			return true
		}
	}
	return false
}

// isLastInstruction reports whether child is the last element child of parent,
// with nothing but whitespace text after it.
func isLastInstruction(parent, child *xmltree.Node) bool {
	seen := false
	for _, ch := range parent.Children {
		if ch == child {
			seen = true
			continue
		}
		if !seen {
			continue
		}
		switch ch.Kind {
		case xmltree.KindElement:
			// Declarations and fallbacks are not INSTRUCTIONS: an
			// xsl:fallback, xsl:param, xsl:on-completion, xsl:sort,
			// xsl:with-param or xsl:catch after the break still leaves it the
			// last instruction (iterate-016).
			if ch.Name.Space == NS && vNonInstructionSiblings[ch.Name.Local] {
				continue
			}
			return false
		case xmltree.KindComment:
			continue
		case xmltree.KindText:
			if !isXMLSpaceOnly(ch.Value) {
				return false
			}
		}
	}
	return seen
}

// vNonInstructionSiblings are XSLT elements that may follow an instruction in
// a sequence constructor without being instructions themselves.
var vNonInstructionSiblings = map[string]bool{
	"fallback": true, "param": true, "on-completion": true, "sort": true,
	"with-param": true, "catch": true,
}

// validTopLevelDecls names every XSLT element XSLT 3.0 allows as a child of
// xsl:stylesheet / xsl:transform / xsl:package. Anything else in the XSLT
// namespace found there is out of place, and under forwards-compatible
// processing is ignored rather than rejected.
var validTopLevelDecls = map[string]bool{
	"accumulator": true, "attribute-set": true, "character-map": true,
	"decimal-format": true, "function": true, "global-context-item": true,
	"import": true, "import-schema": true, "include": true, "key": true,
	"mode": true, "namespace-alias": true, "output": true, "param": true,
	"preserve-space": true, "strip-space": true, "template": true,
	"variable": true, "use-package": true,
	// xsl:expose / xsl:override are deliberately NOT listed: they belong to
	// xsl:package/xsl:use-package, which this processor rejects, and leaving
	// them out lets forwards-compatible processing ignore one carried on a
	// future-version element (forwards-011) while a plain occurrence is still
	// reported as the unsupported optional feature it is.
}

// vAllowedParents restricts elements that only make sense inside specific
// parents (checked only when the parent is itself an XSLT element, so
// extension wrappers are not falsely rejected).
var vAllowedParents = map[string]map[string]bool{
	"sort":                   {"for-each": true, "for-each-group": true, "apply-templates": true, "perform-sort": true},
	"with-param":             {"apply-templates": true, "call-template": true, "apply-imports": true, "next-match": true, "next-iteration": true, "evaluate": true},
	"param":                  {"stylesheet": true, "transform": true, "package": true, "template": true, "function": true, "iterate": true},
	"when":                   {"choose": true},
	"otherwise":              {"choose": true},
	"matching-substring":     {"analyze-string": true},
	"non-matching-substring": {"analyze-string": true},
	"output-character":       {"character-map": true},
	"merge-source":           {"merge": true},
	"merge-action":           {"merge": true},
	"merge-key":              {"merge-source": true},
	"accumulator-rule":       {"accumulator": true},
	"catch":                  {"try": true},
	"on-completion":          {"iterate": true},
	"context-item":           {"template": true},
	"function":               {"stylesheet": true, "transform": true, "package": true, "override": true},
	"accept":                 {"use-package": true},
	"override":               {"use-package": true},
	"key":                    {"stylesheet": true, "transform": true, "package": true},
	"attribute-set":          {"stylesheet": true, "transform": true, "package": true, "override": true},
	"character-map":          {"stylesheet": true, "transform": true, "package": true},
	"decimal-format":         {"stylesheet": true, "transform": true, "package": true},
	"namespace-alias":        {"stylesheet": true, "transform": true, "package": true},
	"strip-space":            {"stylesheet": true, "transform": true, "package": true},
	"preserve-space":         {"stylesheet": true, "transform": true, "package": true},
	"output":                 {"stylesheet": true, "transform": true, "package": true},
	"accumulator":            {"stylesheet": true, "transform": true, "package": true},
	"import":                 {"stylesheet": true, "transform": true, "package": true},
	"include":                {"stylesheet": true, "transform": true, "package": true},
	"mode":                   {"stylesheet": true, "transform": true, "package": true},
	"global-context-item":    {"stylesheet": true, "transform": true, "package": true},
	"import-schema":          {"stylesheet": true, "transform": true, "package": true},
}

func validateXSLTElement(el *xmltree.Node) error {
	if v, ok := el.AttrLocal("version"); ok && !isXSDDecimalLexical(strings.TrimSpace(v)) {
		return errAt(el, "err:XTSE0110: version=%q is not a valid number", v)
	}
	if v, ok := el.AttrLocal("default-validation"); ok {
		if !validationValueOK(v) {
			return errAt(el, "err:XTSE1660: default-validation=%q requires a schema-aware processor", v)
		}
		if err := checkDefaultValidationValue(el, v); err != nil {
			return err
		}
	}
	// expand-text is a STANDARD attribute allowed on every XSLT element, so it
	// is not covered by the per-element boolean table; its value is still an
	// XSLT boolean and nothing else — in particular it is not an AVT
	// (cvt-008 "{$yesOrNo}", cvt-009 "TRUE", cvt-010 "").
	if v, ok := el.AttrLocal("expand-text"); ok {
		if err := checkXSLTBooleanAttr(el, "expand-text", v); err != nil {
			return err
		}
	}
	// @validation="strict"/"lax" (and @type at all) ask for schema validation,
	// which a non-schema-aware processor cannot provide (validation-0101/0102).
	// @validation="strict" asks for schema validation, which a non-schema-aware
	// processor cannot provide (validation-0101). XSLT 3.0 deliberately makes
	// validation="lax" acceptable without a schema (validation-0102b).
	// Note this deliberately checks ONLY "strict", not the whole vocabulary the
	// literal-result-element path above rejects: an out-of-vocabulary value on
	// an XSLT element is caught by that element's own attribute-value checks.
	if v, ok := el.AttrLocal("validation"); ok && strings.TrimSpace(v) == "strict" && !schemaAwareRun() {
		return errAt(el, "err:XTSE1660: validation=%q requires a schema-aware processor", v)
	}
	if v, ok := el.AttrLocal("default-collation"); ok {
		recognized := false
		for _, tok := range strings.Fields(v) {
			if _, err := xpath.ResolveCollator(tok); err == nil {
				recognized = true
				break
			}
		}
		if !recognized {
			return errAt(el, "err:XTSE0125: default-collation=%q names no collation recognized by this processor", v)
		}
	}
	switch el.Name.Local {
	case "break", "next-iteration":
		if err := checkIterateExit(el); err != nil {
			return err
		}
	}
	if parents, restricted := vAllowedParents[el.Name.Local]; restricted {
		if p := el.Parent; p != nil && p.Kind == xmltree.KindElement {
			if p.Name.Space == NS && !parents[p.Name.Local] {
				return errAt(el, "err:XTSE0010: xsl:%s is not allowed inside xsl:%s", el.Name.Local, p.Name.Local)
			}
			// Literal result elements cannot contain these either (extension
			// elements in other namespaces are given the benefit of the doubt
			// only for sort/with-param).
			if p.Name.Space != NS && el.Name.Local != "sort" && el.Name.Local != "with-param" {
				return errAt(el, "err:XTSE0010: xsl:%s is not allowed inside a literal result element", el.Name.Local)
			}
		}
	}
	spec, known := xsltElemSpecs[el.Name.Local]
	if !known {
		return nil
	}
	allowed := fieldSet(spec.attrs)

	// Attributes: no-namespace attributes must be declared for the element or
	// standard; XSLT-namespace attributes are never allowed on XSLT elements.
	for _, a := range el.Attrs {
		switch a.Name.Space {
		case "":
			if allowed[a.Name.Local] || vStdAttrs[a.Name.Local] || strings.HasPrefix(a.Name.Local, "_") {
				continue
			}
			// Forwards-compatible processing (XSLT §3.4): an attribute a
			// known element doesn't otherwise allow is not a static error
			// when the nearest [xsl:]version in scope is greater than 3.0 —
			// it is simply ignored (version-007).
			if inForwardsCompatScope(el) {
				continue
			}
			return errAt(el, "err:XTSE0090: attribute %q is not allowed on xsl:%s", a.Name.Local, el.Name.Local)
		case NS:
			return errAt(el, "err:XTSE0090: XSLT-namespace attribute %q is not allowed on xsl:%s", a.Name.Local, el.Name.Local)
		}
	}

	// Required attributes.
	for _, req := range strings.Fields(spec.required) {
		if _, ok := el.AttrLocal(req); !ok {
			return errAt(el, "err:XTSE0010: xsl:%s requires a %s attribute", el.Name.Local, req)
		}
	}
	if el.Name.Local == "template" {
		_, hasMatch := el.AttrLocal("match")
		_, hasName := el.AttrLocal("name")
		if !hasMatch && !hasName {
			return errAt(el, "err:XTSE0010: xsl:template requires a match or name attribute")
		}
		if !hasMatch {
			// mode/priority are only allowed together with match.
			if _, ok := el.AttrLocal("mode"); ok {
				return errAt(el, "err:XTSE0500: xsl:template with mode requires a match attribute")
			}
			if _, ok := el.AttrLocal("priority"); ok {
				return errAt(el, "err:XTSE0500: xsl:template with priority requires a match attribute")
			}
		}
		if mode, ok := el.AttrLocal("mode"); ok {
			if err := validateModeList(el, mode); err != nil {
				return err
			}
		}
		if !hasName {
			// visibility names a PACKAGE COMPONENT's exposure, and a template
			// rule (match, no name) declares no such component — it only
			// contributes to a MODE (package-001t: match+visibility with no
			// name is a static error, not a silent no-op).
			if _, ok := el.AttrLocal("visibility"); ok {
				return errAt(el, "err:XTSE0500: xsl:template with no name attribute cannot have a visibility attribute")
			}
		}
	}

	// Attribute values.
	if qn := vQNameAttrs[el.Name.Local]; qn != nil {
		for name := range qn {
			if v, ok := el.AttrLocal(name); ok && !isLexicalQName(v) {
				return errAt(el, "err:XTSE0020: %s=%q is not a valid QName on xsl:%s", name, v, el.Name.Local)
			}
		}
	}
	// It is a static error to use a reserved namespace (the XSLT namespace, or
	// the xml: namespace) in the name of a named template, a mode, an
	// attribute set, a key, a decimal-format, a variable or parameter, a
	// stylesheet function, a named output definition, or a character map
	// (XTSE0080).
	if vReservedNSNames[el.Name.Local] {
		if name, ok := el.AttrLocal("name"); ok && name != "" {
			rq := resolveQName(el, name)
			// xsl:initial-template is the one reserved name a stylesheet may
			// declare — and it is recognized by EXPANDED name, so any prefix
			// bound to the XSLT namespace works (output-0702/0713..0717 bind
			// it as t:initial-template).
			if rq.Space == NS && rq.Local == "initial-template" && el.Name.Local == "template" {
				// allowed
			} else if rq.Space == NS || rq.Space == "http://www.w3.org/XML/1998/namespace" {
				return errAt(el, "err:XTSE0080: %q on xsl:%s uses a reserved namespace", name, el.Name.Local)
			}
		}
	}
	if bs := vBoolAttrs[el.Name.Local]; bs != nil {
		for name := range bs {
			if v, ok := el.AttrLocal(name); ok {
				switch strings.TrimSpace(v) {
				case "yes", "no", "true", "false", "1", "0":
				default:
					return errAt(el, "err:XTSE0020: %s=%q is not an XSLT boolean on xsl:%s", name, v, el.Name.Local)
				}
			}
		}
	}
	if bs := vBoolAVTAttrs[el.Name.Local]; bs != nil {
		// An attribute value template whose effective value is restricted to a
		// fixed token set is still checked STATICALLY whenever it has no
		// variable part (message-0316: terminate="NO").
		for name := range bs {
			if v, ok := el.AttrLocal(name); ok && !strings.ContainsAny(v, "{}") {
				switch strings.TrimSpace(v) {
				case "yes", "no", "true", "false", "1", "0":
				default:
					return errAt(el, "err:XTSE0020: %s=%q is not an XSLT boolean on xsl:%s", name, v, el.Name.Local)
				}
			}
		}
	}
	if el.Name.Local == "decimal-format" {
		for _, a := range el.Attrs {
			if a.Name.Space == "" && vOneCharAttrs[a.Name.Local] && len([]rune(a.Value)) != 1 {
				return errAt(el, "err:XTSE0020: xsl:decimal-format %s=%q must be a single character", a.Name.Local, a.Value)
			}
		}
	}
	if el.Name.Local == "global-context-item" {
		use, hasUse := el.AttrLocal("use")
		use = strings.TrimSpace(use)
		if hasUse {
			switch use {
			case "required", "optional", "absent":
			default:
				return errAt(el, "err:XTSE0020: use=%q is not allowed on xsl:global-context-item", use)
			}
		}
		if _, hasAs := el.AttrLocal("as"); hasAs && use == "absent" {
			return errAt(el, "err:XTSE3089: xsl:global-context-item cannot combine use=\"absent\" with an as attribute")
		}
	}
	if el.Name.Local == "output" {
		// standalone is xs:boolean plus the extra value "omit".
		if v, ok := el.AttrLocal("standalone"); ok {
			switch strings.TrimSpace(v) {
			case "yes", "no", "true", "false", "1", "0", "omit":
			default:
				return errAt(el, "err:XTSE0020: standalone=%q is not allowed on xsl:output", v)
			}
		}
	}
	// xsl:package/@package-version is validated by checkPackageAttrs
	// (shadow.go), which runs AFTER shadow-attribute expansion so a
	// _package-version override is checked at its effective value
	// (package-version-007 vs -908) — this function's static validation
	// pass runs too early in the compile pipeline for that.
	if el.Name.Local == "accumulator" {
		// An accumulator must have at least one xsl:accumulator-rule
		// (accumulator-024).
		n := 0
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindElement && ch.Name.Space == NS && ch.Name.Local == "accumulator-rule" {
				n++
			}
		}
		if n == 0 {
			return errAt(el, "err:XTSE0010: xsl:accumulator requires at least one xsl:accumulator-rule")
		}
	}
	if el.Name.Local == "mode" {
		// XSLT 3.0 xsl:mode attribute value vocabularies (XTSE0020). These are
		// fixed token sets, not AVTs, and the tokens are case-sensitive
		// ("Yes" is not an XSLT boolean).
		vocab := func(name string, allowed ...string) error {
			v, ok := el.AttrLocal(name)
			if !ok {
				return nil
			}
			v = strings.TrimSpace(v)
			for _, a := range allowed {
				if v == a {
					return nil
				}
			}
			return errAt(el, "err:XTSE0020: %s=%q is not allowed on xsl:mode", name, v)
		}
		if err := vocab("on-no-match", "deep-copy", "shallow-copy", "deep-skip", "shallow-skip", "text-only-copy", "fail"); err != nil {
			return err
		}
		if err := vocab("on-multiple-match", "use-last", "fail"); err != nil {
			return err
		}
		if err := vocab("typed", "yes", "no", "true", "false", "1", "0", "lax", "strict", "unspecified"); err != nil {
			return err
		}
		if err := vocab("visibility", "public", "private", "final", "abstract"); err != nil {
			return err
		}
		if _, named := el.AttrLocal("name"); !named {
			// The unnamed mode is implicitly private and its visibility
			// cannot be declared otherwise (mode-1507/1508).
			if v, ok := el.AttrLocal("visibility"); ok && strings.TrimSpace(v) != "private" {
				return errAt(el, "err:XTSE0020: visibility=%q is not allowed on a declaration of the unnamed mode", strings.TrimSpace(v))
			}
		}
	}
	if el.Name.Local == "apply-templates" {
		if m, ok := el.AttrLocal("mode"); ok {
			m = strings.TrimSpace(m)
			if m != "#current" && m != "#default" && m != "#unnamed" && !isLexicalQName(m) {
				return errAt(el, "err:XTSE0020: mode=%q is not a valid mode name", m)
			}
		}
	}

	// Content model.
	if spec.children != "" {
		allowedKids := fieldSet(spec.children)
		empty := spec.children == "empty"
		for _, ch := range el.Children {
			// Within an element required to be empty, a whitespace-only text
			// node kept alive by an ancestor xml:space="preserve" is ALSO a
			// static error (XTSE0260, error-0260d) — but ordinary stylesheet
			// whitespace (not yet stripped at this point in compilation) must
			// not be flagged, so this only fires under an explicit
			// xml:space="preserve" in scope.
			if empty && ch.Kind == xmltree.KindText && ch.Value != "" && xmlSpacePreserved(ch) && el.Name.Local != "text" {
				return errAt(el, "err:XTSE0260: xsl:%s must be empty", el.Name.Local)
			}
			if ch.Kind != xmltree.KindElement {
				continue
			}
			if empty {
				return errAt(el, "err:XTSE0010: xsl:%s must be empty", el.Name.Local)
			}
			if ch.Name.Space != NS || !allowedKids[ch.Name.Local] {
				return errAt(el, "err:XTSE0010: element <%s> is not allowed inside xsl:%s", ch.Name.Local, el.Name.Local)
			}
		}
	}
	if spec.noText {
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "" {
				return errAt(el, "err:XTSE0010: text content is not allowed inside xsl:%s", el.Name.Local)
			}
		}
	}

	if excl, ok := vSelectExcl[el.Name.Local]; ok {
		if _, hasSelect := el.AttrLocal("select"); hasSelect {
			for _, ch := range el.Children {
				switch ch.Kind {
				case xmltree.KindText:
					if strings.TrimSpace(ch.Value) != "" {
						return errAt(el, "err:%s: xsl:%s must have empty content when select is present", excl.code, el.Name.Local)
					}
				case xmltree.KindElement:
					if ch.Name.Space != NS || !excl.allowedKids[ch.Name.Local] {
						return errAt(el, "err:%s: xsl:%s must have empty content when select is present", excl.code, el.Name.Local)
					}
				}
			}
		}
	}

	// Prefixed QName-valued attributes must have a bound prefix (XTSE0280).
	if qn := vQNameAttrs[el.Name.Local]; qn != nil {
		for name := range qn {
			if v, ok := el.AttrLocal(name); ok {
				if err := checkPrefixBound(el, v); err != nil {
					return err
				}
			}
		}
	}

	// tunnel="yes" is only meaningful on template parameters (XTSE0020 on
	// stylesheet-level or function parameters).
	if el.Name.Local == "param" {
		if t, ok := el.AttrLocal("tunnel"); ok && (t == "yes" || t == "true" || t == "1") {
			if p := el.Parent; p != nil && p.Kind == xmltree.KindElement && p.Name.Space == NS {
				switch p.Name.Local {
				case "stylesheet", "transform", "function":
					return errAt(el, "err:XTSE0020: tunnel parameters are not allowed on xsl:%s", p.Name.Local)
				}
			}
		}
		// A function's parameters are always required, so @required="yes" is
		// accepted (function-0119b) but any other value is pointless/invalid
		// (function-0120b: required="no").
		if rq, ok := el.AttrLocal("required"); ok && !xsltBool(rq) {
			if p := el.Parent; p != nil && p.Kind == xmltree.KindElement && p.Name.Space == NS && p.Name.Local == "function" {
				return errAt(el, "err:XTSE0020: required=%q is not allowed on a parameter of xsl:function", rq)
			}
		}
	}
	if el.Name.Local == "param" || el.Name.Local == "variable" {
		// static="yes" declares a value bound at COMPILE time, which only a
		// top-level (global) declaration can be: a template/function parameter
		// or a local variable has no value until the transform runs
		// (static-025 puts static="yes" on an xsl:template's xsl:param).
		if st, ok := el.AttrLocal("static"); ok && xsltBool(st) {
			top := false
			if p := el.Parent; p != nil && p.Kind == xmltree.KindElement && p.Name.Space == NS {
				switch p.Name.Local {
				case "stylesheet", "transform", "package", "override":
					top = true
				}
			}
			if !top {
				return errAt(el, "err:XTSE0090: static=%q is only allowed on a top-level xsl:%s", st, el.Name.Local)
			}
			// A static variable is bound before any package is assembled, so
			// it cannot be exposed across package boundaries: only the default
			// visibility="private" is compatible with static="yes"
			// (static-026 combines it with visibility="final").
			if v, ok := el.AttrLocal("visibility"); ok && strings.TrimSpace(v) != "private" {
				return errAt(el, "err:XTSE0020: visibility=%q is not allowed on a static xsl:%s", v, el.Name.Local)
			}
		}
	}
	if el.Name.Local == "function" {
		// @new-each-time's lexical space is {yes, no, maybe} (a tri-state,
		// NOT a plain XSLT boolean) — "maybe" is the default and a valid
		// value (function-1033), but any other spelling, including a
		// differently-cased boolean like "Yes", is XTSE0020 (function-0118).
		if v, ok := el.AttrLocal("new-each-time"); ok {
			switch v {
			case "yes", "no", "maybe":
			default:
				return errAt(el, "err:XTSE0020: new-each-time=%q is not yes, no or maybe on xsl:function", v)
			}
		}
		// override-extension-function is the current name for the deprecated
		// override attribute; both may be given together as long as they
		// agree (function-0116: yes+true), but conflicting values are a
		// static error (function-0117: 1+no).
		ov, hasOverride := el.AttrLocal("override")
		ovExt, hasOverrideExt := el.AttrLocal("override-extension-function")
		if hasOverride && hasOverrideExt && xsltBool(ov) != xsltBool(ovExt) {
			return errAt(el, "err:XTSE0020: xsl:function override=%q conflicts with override-extension-function=%q", ov, ovExt)
		}
	}
	// exclude-result-prefixes / extension-element-prefixes tokens must be bound
	// (XTSE0280 / XTSE0808).
	if v, ok := el.AttrLocal("exclude-result-prefixes"); ok {
		if err := checkExcludeResultPrefixes(el, v); err != nil {
			return err
		}
	}
	if v, ok := el.AttrLocal("extension-element-prefixes"); ok {
		for _, tok := range strings.Fields(v) {
			if tok == "#all" || tok == "#default" {
				continue
			}
			uri, bound := el.LookupPrefix(tok)
			if !bound {
				return errAt(el, "err:XTSE0808: prefix %q in extension-element-prefixes has no namespace binding", tok)
			}
			// A reserved namespace (the XSLT namespace itself, or the
			// standard xs/xsi/fn/math namespaces) can never name an
			// extension instruction namespace (extension-functions-0105).
			if isReservedNS(uri) {
				return errAt(el, "err:XTSE0800: prefix %q in extension-element-prefixes names a reserved namespace", tok)
			}
		}
	}

	switch el.Name.Local {
	case "number":
		if _, hasValue := el.AttrLocal("value"); hasValue {
			for _, excl := range []string{"select", "count", "from", "level"} {
				if _, ok := el.AttrLocal(excl); ok {
					return errAt(el, "err:XTSE0975: xsl:number value attribute excludes %s", excl)
				}
			}
		}
	case "for-each-group":
		n := 0
		for _, g := range []string{"group-by", "group-adjacent", "group-starting-with", "group-ending-with"} {
			if _, ok := el.AttrLocal(g); ok {
				n++
			}
		}
		if n != 1 {
			return errAt(el, "err:XTSE1300: xsl:for-each-group requires exactly one grouping attribute (got %d)", n)
		}
	case "apply-templates":
		if m, ok := el.AttrLocal("mode"); ok && !strings.HasPrefix(m, "#") {
			if err := checkPrefixBound(el, strings.TrimSpace(m)); err != nil {
				return err
			}
		}
	case "output":
		if m, ok := el.AttrLocal("method"); ok {
			mt := strings.TrimSpace(m)
			if !isLexicalQName(mt) {
				return errAt(el, "err:XTSE1570: xsl:output method=%q is not a valid EQName", m)
			}
			if !strings.Contains(mt, ":") {
				switch mt {
				case "xml", "html", "xhtml", "text", "json", "adaptive":
				default:
					return errAt(el, "err:XTSE1570: xsl:output method=%q must be xml, html, xhtml, text, json, or adaptive", m)
				}
			}
		}
	}

	// Duplicate sibling xsl:with-param names (XTSE0670) and duplicate
	// xsl:param declarations (XTSE0580).
	if el.Name.Space == NS {
		seenWP := map[string]bool{}
		seenP := map[string]bool{}
		for _, ch := range elementChildren(el) {
			if ch.Name.Space != NS {
				continue
			}
			var set map[string]bool
			var code string
			switch ch.Name.Local {
			case "with-param":
				set, code = seenWP, "XTSE0670"
			case "param":
				set, code = seenP, "XTSE0580"
			default:
				continue
			}
			name, _ := ch.AttrLocal("name")
			key := clarkName(resolveQName(ch, name))
			if set[key] {
				return errAt(ch, "err:%s: duplicate xsl:%s name %q", code, ch.Name.Local, name)
			}
			set[key] = true
		}
	}

	switch el.Name.Local {
	case "analyze-string":
		// Content model (xsl:matching-substring?, xsl:non-matching-substring?,
		// xsl:fallback*): each of the first two is optional but, if present,
		// must come in that order, and at least one of them is required
		// (XTSE1130); xsl:fallback must come after both (analyze-string-093/094).
		state := 0
		sawMatching, sawNonMatching := false, false
		for _, ch := range elementChildren(el) {
			switch ch.Name.Local {
			case "matching-substring":
				if state > 0 {
					return errAt(el, "err:XTSE0010: xsl:matching-substring must precede xsl:non-matching-substring and xsl:fallback")
				}
				sawMatching = true
				state = 1
			case "non-matching-substring":
				if state > 1 {
					return errAt(el, "err:XTSE0010: xsl:non-matching-substring must precede xsl:fallback")
				}
				sawNonMatching = true
				state = 2
			case "fallback":
				state = 2
			}
		}
		if !sawMatching && !sawNonMatching {
			return errAt(el, "err:XTSE1130: xsl:analyze-string requires an xsl:matching-substring or xsl:non-matching-substring")
		}
	case "choose":
		seenWhen, seenOther := false, false
		for _, ch := range elementChildren(el) {
			switch ch.Name.Local {
			case "when":
				if seenOther {
					return errAt(el, "err:XTSE0010: xsl:when must precede xsl:otherwise")
				}
				seenWhen = true
			case "otherwise":
				if seenOther {
					return errAt(el, "err:XTSE0010: xsl:choose allows at most one xsl:otherwise")
				}
				seenOther = true
			}
		}
		if !seenWhen {
			return errAt(el, "err:XTSE0010: xsl:choose requires at least one xsl:when")
		}
	case "template", "function":
		// Parameters must precede the body.
		seenBody := false
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindText {
				if strings.TrimSpace(ch.Value) != "" {
					seenBody = true
				}
				continue
			}
			if ch.Kind != xmltree.KindElement {
				continue
			}
			if ch.Name.Space == NS && ch.Name.Local == "param" {
				if seenBody {
					return errAt(el, "err:XTSE0010: xsl:param must come first in xsl:%s", el.Name.Local)
				}
				continue
			}
			if ch.Name.Space == NS && ch.Name.Local == "context-item" && el.Name.Local == "template" {
				// xsl:template content is ( xsl:context-item?, xsl:param*,
				// sequence-constructor ): the declaration precedes the
				// parameters and is not body content. Its own placement rules
				// are enforced by splitContextItem.
				continue
			}
			seenBody = true
		}
	case "for-each", "for-each-group", "perform-sort":
		// xsl:sort children must come first.
		seenBody := false
		for _, ch := range el.Children {
			if ch.Kind == xmltree.KindText {
				if strings.TrimSpace(ch.Value) != "" {
					seenBody = true
				}
				continue
			}
			if ch.Kind != xmltree.KindElement {
				continue
			}
			if ch.Name.Space == NS && ch.Name.Local == "sort" {
				if seenBody {
					return errAt(el, "err:XTSE0010: xsl:sort must come first in xsl:%s", el.Name.Local)
				}
				continue
			}
			seenBody = true
		}
	}
	return nil
}

// validateModeList checks xsl:template/@mode (XTSE0550: tokens must be valid,
// non-duplicate, and #all must appear alone).
func validateModeList(el *xmltree.Node, mode string) error {
	toks := strings.Fields(mode)
	if len(toks) == 0 {
		return errAt(el, "err:XTSE0550: empty mode list")
	}
	seen := map[string]bool{}
	for _, tok := range toks {
		if seen[tok] {
			return errAt(el, "err:XTSE0550: duplicate mode token %q", tok)
		}
		seen[tok] = true
		switch tok {
		case "#all":
			if len(toks) > 1 {
				return errAt(el, "err:XTSE0550: #all must be the only mode token")
			}
		case "#default", "#unnamed":
		default:
			if !isLexicalQName(tok) {
				return errAt(el, "err:XTSE0550: mode token %q is not a valid QName", tok)
			}
			if err := checkPrefixBound(el, tok); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPrefixBound reports XTSE0280 for a prefixed QName whose prefix has no
// in-scope namespace binding.
func checkPrefixBound(el *xmltree.Node, qname string) error {
	// Whitespace-normalize first, like every other reader of a QName-typed
	// attribute: " Q{uri}local " is an EQName, not a prefix " Q{http"
	// (call-template-0109).
	qname = strings.TrimSpace(qname)
	i := strings.IndexByte(qname, ':')
	if i <= 0 || strings.HasPrefix(qname, "Q{") {
		return nil
	}
	prefix := qname[:i]
	if _, ok := el.LookupPrefix(prefix); !ok {
		return errAt(el, "err:XTSE0280: prefix %q in %q has no namespace binding", prefix, qname)
	}
	return nil
}

// isXSDDecimalLexical reports whether s is a valid xs:decimal lexical form
// (optional sign, digits, optional fractional part; no exponent) — the shape
// required of an [xsl:]version attribute value (XTSE0110).
func isXSDDecimalLexical(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	dot := false
	digits := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits
}

func fieldSet(s string) map[string]bool {
	if s == "" || s == "empty" {
		return nil
	}
	out := map[string]bool{}
	for _, f := range strings.Fields(s) {
		out[f] = true
	}
	return out
}

// isLexicalQName checks NCName(':'NCName)? (also accepting EQName Q{uri}local).
// Leading/trailing whitespace is stripped first, matching the whitespace
// normalization a QName-typed attribute undergoes (call-template-0109);
// INTERNAL whitespace (name="a b") is still rejected.
func isLexicalQName(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "Q{") {
		i := strings.IndexByte(s, '}')
		if i < 0 {
			return false
		}
		return isNCName(s[i+1:])
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return isNCName(s[:i]) && isNCName(s[i+1:])
	}
	return isNCName(s)
}

func isNCName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' &&
			r != 0xB7 {
			return false
		}
	}
	return true
}

// isPackageVersion (the PackageVersion grammar) lives in shadow.go, used by
// checkPackageAttrs — that runs AFTER shadow-attribute expansion, which this
// file's static validation pass runs too early in compile to do correctly.

// checkXSLTBooleanAttr reports XTSE0020 unless v is in the XSLT boolean lexical
// space ("yes"/"no" plus the xs:boolean forms), ignoring surrounding whitespace.
func checkXSLTBooleanAttr(el *xmltree.Node, name, v string) error {
	switch strings.TrimSpace(v) {
	case "yes", "no", "true", "false", "1", "0":
		return nil
	}
	return errAt(el, "err:XTSE0020: %s=%q is not an XSLT boolean", name, v)
}
