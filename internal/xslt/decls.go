package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// declRegistry maps a top-level xsl: declaration local-name to a compiler.
// Files (decl_*.go) register additional declarations via init(). Package-var
// literal so init() additions are safe.
var declRegistry = map[string]func(c *compiler, ss *Stylesheet, el *xmltree.Node) error{}

// compileTopLevelExtra handles top-level declarations not covered by the core
// switch: registry-based declarations, rejection of unsupported optional
// features, and silent tolerance of anything else.
func (c *compiler) compileTopLevelExtra(ss *Stylesheet, el *xmltree.Node) error {
	// Forwards-compatible processing: any XSLT element XSLT 3.0 does not allow
	// as a child of xsl:stylesheet is IGNORED, with its content, rather than
	// rejected (forwards-005/006/007/011). This runs before the registry so
	// that even a name this processor implements elsewhere (xsl:value-of,
	// xsl:when, xsl:expose) is ignored when it turns up out of place under a
	// future version.
	if !validTopLevelDecls[el.Name.Local] && inForwardsCompatScope(el) {
		if el.Name.Local == "expose" {
			recordIgnoredExpose(ss, el)
		}
		return nil
	}
	if fn, ok := declRegistry[el.Name.Local]; ok {
		return fn(c, ss, el)
	}
	switch el.Name.Local {
	// xsl:import-schema has moved to declRegistry (decl_import_schema.go): it
	// is really compiled when the run claims schema-awareness, and re-raises
	// this exact rejection when it does not.
	case "package", "expose":
		return errAt(el, "xsl:%s is an optional feature (packages/schema-awareness) not supported by this processor", el.Name.Local)
	}
	// An XSLT instruction is never permitted as a top-level declaration (a direct
	// child of xsl:stylesheet); doing so is a static error (XTSE0010). These names
	// are unambiguously instructions, so rejecting them cannot misclassify a valid
	// declaration.
	if topLevelInstructions[el.Name.Local] {
		return errAt(el, "err:XTSE0010: xsl:%s is not allowed as a top-level declaration", el.Name.Local)
	}
	// Any other name in the XSLT namespace is not part of the xsl:declaration
	// grammar at all (knownTopLevelDecls lists every one that is, beyond the
	// core switch/declRegistry above) — a genuinely unknown top-level XSLT
	// element is a static error (XTSE0010), UNLESS forwards-compatible
	// processing applies (an ancestor's effective version > 3.0), in which
	// case it is simply ignored.
	if !knownTopLevelDecls[el.Name.Local] && !inForwardsCompatScope(el) {
		return errAt(el, "err:XTSE0010: xsl:%s is not a recognized top-level declaration", el.Name.Local)
	}
	return nil
}

// recordIgnoredExpose notes the named TEMPLATE components an xsl:expose
// element asked to expose, when that element itself is being dropped by
// forwards-compatible processing (see compileTopLevelExtra) rather than
// acted on. Only literal EQName tokens are recorded — a wildcard token
// ("*", "p:*", "name#N") is left unrecorded, which only means the
// visibility-enforcement check this feeds stays conservative (still
// enforces private-by-default) for that broader case, never the reverse.
func recordIgnoredExpose(ss *Stylesheet, el *xmltree.Node) {
	comp := strings.TrimSpace(attrOr(el, "component", ""))
	wantsTemplate := comp == "" || comp == "*"
	if !wantsTemplate {
		for _, k := range strings.Fields(comp) {
			if k == "template" || k == "*" {
				wantsTemplate = true
				break
			}
		}
	}
	if !wantsTemplate {
		return
	}
	for _, tok := range strings.Fields(attrOr(el, "names", "")) {
		if strings.ContainsAny(tok, "*#") {
			continue
		}
		if ss.exposedIgnored == nil {
			ss.exposedIgnored = map[string]bool{}
		}
		ss.exposedIgnored["template#"+clarkName(resolveQName(el, tok))] = true
	}
}

// knownTopLevelDecls names xsl:declaration elements that are valid direct
// children of xsl:stylesheet/xsl:transform but are handled outside the core
// compileTopLevel switch and declRegistry (xsl:import/xsl:include are
// consumed earlier still, by gatherModules, and never reach here).
var knownTopLevelDecls = map[string]bool{
	"namespace-alias": true,
}

// topLevelInstructions are XSLT instruction names that may never appear as a
// direct child of xsl:stylesheet/xsl:transform.
var topLevelInstructions = map[string]bool{
	"apply-templates": true, "apply-imports": true, "call-template": true,
	"value-of": true, "copy-of": true, "copy": true, "for-each": true,
	"for-each-group": true, "if": true, "choose": true, "when": true,
	"otherwise": true, "sequence": true, "text": true, "element": true,
	"attribute": true, "comment": true, "processing-instruction": true,
	"number": true, "message": true, "fallback": true, "analyze-string": true,
	"next-match": true, "result-document": true, "iterate": true, "try": true,
	"merge": true, "perform-sort": true, "source-document": true,
	"on-empty": true, "on-non-empty": true, "evaluate": true,
	"where-populated": true, "assert": true, "map": true, "array": true,
	"fork": true,
}
