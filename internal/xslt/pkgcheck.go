package xslt

import (
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// ---------------------------------------------------------------------------
// Static checks on xsl:use-package / xsl:accept / xsl:override (XSLT 3.0 §3.5).
//
// These need a COMPONENT MODEL of the used package — which components it
// exposes and with what visibility — even though execution itself uses the
// flattened import-precedence model (packages.go). The model built here is
// deliberately limited to what the static rules need: the package's own
// top-level declarations plus whatever its own xsl:use-package/xsl:accept
// children explicitly re-expose.
// ---------------------------------------------------------------------------

// pkgComponent is one component in a package's manifest. kind is the
// declaration's local name ("template", "function", "variable",
// "attribute-set", "mode"); name is its expanded QName in Clark notation ("" is
// the unnamed mode); arity distinguishes overloaded functions.
type pkgComponent struct {
	kind       string
	name       string
	arity      int
	visibility string
	el         *xmltree.Node // the declaration, for signature comparison
}

// symbolic is the component's symbolic name: two components are HOMONYMOUS
// when these match.
func (c pkgComponent) symbolic() string {
	if c.kind == "function" {
		return c.kind + "#" + c.name + "#" + itoa(c.arity)
	}
	return c.kind + "#" + c.name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// componentDeclKinds are the top-level declarations that declare a component
// with a symbolic name (a template rule — xsl:template with @match and no
// @name — declares no named component; it contributes to a MODE instead).
var componentDeclKinds = map[string]bool{
	"template": true, "function": true, "variable": true,
	"param": true, "attribute-set": true, "mode": true,
}

// declVisibility returns a component declaration's effective visibility. The
// default for every component declaration is "private" (XSLT 3.0 §3.5.1).
func declVisibility(el *xmltree.Node) string {
	if v, ok := el.AttrLocal("visibility"); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return "private"
}

// componentOf builds the pkgComponent a top-level declaration declares, or
// ok=false when it declares none (a template rule, or a non-component element).
func componentOf(el *xmltree.Node) (pkgComponent, bool) {
	if el.Name.Space != NS || !componentDeclKinds[el.Name.Local] {
		return pkgComponent{}, false
	}
	kind := el.Name.Local
	if kind == "param" {
		// A global xsl:param is a variable component.
		kind = "variable"
	}
	name, has := el.AttrLocal("name")
	if !has {
		// An xsl:mode with no name declares the UNNAMED mode; any other
		// nameless declaration (a template rule) is not a named component.
		if el.Name.Local != "mode" {
			return pkgComponent{}, false
		}
		name = ""
	}
	comp := pkgComponent{kind: kind, visibility: declVisibility(el), el: el}
	if name != "" {
		comp.name = clarkName(resolveQName(el, strings.TrimSpace(name)))
	}
	if kind == "function" {
		for _, ch := range elementChildren(el) {
			if ch.Name.Space == NS && ch.Name.Local == "param" {
				comp.arity++
			}
		}
	}
	if el.Name.Local == "mode" && name == "" {
		// The unnamed mode is always private and cannot be exposed.
		comp.visibility = "private"
	}
	return comp, true
}

// exposedVisibilities are the visibilities with which a component is part of a
// package's manifest, i.e. can be seen by a package that uses it.
func isExposed(vis string) bool {
	switch vis {
	case "public", "final", "abstract":
		return true
	}
	return false
}

// packageExports returns the components package module abs exposes: its own
// public/final/abstract declarations, plus components it explicitly re-exposes
// from packages IT uses via an xsl:accept with an exposed visibility. Results
// are cached per compile because a package graph is commonly a diamond
// (use-package-160 reaches generalVariables by four distinct routes).
func (c *compiler) packageExports(abs string, root *xmltree.Node, stack map[string]bool) ([]pkgComponent, error) {
	if c.pkgExports == nil {
		c.pkgExports = map[string][]pkgComponent{}
	}
	if got, ok := c.pkgExports[abs]; ok {
		return got, nil
	}
	if stack[abs] {
		return nil, nil // recursive use; the cycle itself is reported elsewhere
	}
	stack[abs] = true
	defer delete(stack, abs)

	var out []pkgComponent
	for _, ch := range elementChildren(root) {
		if ch.Name.Space != NS {
			continue
		}
		if ch.Name.Local == "use-package" {
			sub, subAbs, err := c.usedPackageExports(ch, stack)
			if err != nil {
				return nil, err
			}
			_ = subAbs
			// Only an explicit xsl:accept re-exposes a used component further;
			// without one the component is usable here but not part of THIS
			// package's manifest.
			for _, ac := range elementChildren(ch) {
				if ac.Name.Space != NS || ac.Name.Local != "accept" {
					continue
				}
				vis := strings.TrimSpace(attrOr(ac, "visibility", ""))
				if !isExposed(vis) {
					continue
				}
				for _, comp := range sub {
					if acceptMatches(ac, comp) {
						comp.visibility = vis
						out = append(out, comp)
					}
				}
			}
			continue
		}
		comp, ok := componentOf(ch)
		if !ok || !isExposed(comp.visibility) {
			continue
		}
		out = append(out, comp)
	}
	c.pkgExports[abs] = out
	return out, nil
}

// usedPackageExports resolves one xsl:use-package element to the component
// manifest of the package it names.
func (c *compiler) usedPackageExports(el *xmltree.Node, stack map[string]bool) ([]pkgComponent, string, error) {
	name := strings.TrimSpace(attrOr(el, "name", ""))
	verSpec := "*"
	if v, ok := el.AttrLocal("package-version"); ok {
		verSpec = strings.TrimSpace(v)
	}
	src, err := c.resolvePackage(name, verSpec)
	if err != nil {
		return nil, "", errAt(el, "%v", err)
	}
	abs, err := filepath.Abs(src.Path)
	if err != nil {
		return nil, "", errAt(el, "%v", err)
	}
	if got, ok := c.pkgExports[abs]; ok {
		return got, abs, nil
	}
	root, err := c.loadPackageRoot(abs)
	if err != nil {
		return nil, "", errAt(el, "%v", err)
	}
	comps, err := c.packageExports(abs, root, stack)
	return comps, abs, err
}

func attrOr(el *xmltree.Node, name, def string) string {
	if v, ok := el.AttrLocal(name); ok {
		return v
	}
	return def
}

// isWildcardToken reports whether an xsl:accept/@names token is a wildcard
// form ("*", "p:*", "*:local", "Q{uri}*") rather than a specific name.
func isWildcardToken(tok string) bool { return strings.Contains(tok, "*") }

// acceptMatches reports whether an xsl:accept element selects comp, i.e. its
// @component covers the component's kind and one of its @names tokens matches
// the component's name.
func acceptMatches(ac *xmltree.Node, comp pkgComponent) bool {
	kinds := strings.TrimSpace(attrOr(ac, "component", ""))
	if kinds != "*" {
		found := false
		for _, k := range strings.Fields(kinds) {
			if k == comp.kind || k == "*" {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, tok := range strings.Fields(attrOr(ac, "names", "")) {
		if acceptTokenMatches(ac, tok, comp) {
			return true
		}
	}
	return false
}

// acceptTokenMatches matches one @names token against comp's expanded name
// (Clark notation) and, for a function, its arity. Supported forms: "*",
// "prefix:*", "*:local", "Q{uri}*", "Q{uri}local", a lexical QName, and — for
// a function component only — any of those exact-name forms followed by
// "#" and an integer arity (package-017: names="pkg:function1#0" must match
// only the zero-arity pkg:function1, the same EQName#arity suffix
// xsl:override's own function matching and F&O's named-function-reference
// syntax both use).
func acceptTokenMatches(ac *xmltree.Node, tok string, comp pkgComponent) bool {
	clark := comp.name
	wantArity := -1
	if comp.kind == "function" {
		if i := strings.LastIndexByte(tok, '#'); i >= 0 {
			if n, ok := parseArityDigits(tok[i+1:]); ok {
				wantArity, tok = n, tok[:i]
			}
		}
	}
	if wantArity >= 0 && wantArity != comp.arity {
		return false
	}
	switch {
	case tok == "*":
		return true
	case strings.HasSuffix(tok, ":*"):
		pfx := strings.TrimSuffix(tok, ":*")
		uri, _ := ac.LookupPrefix(pfx)
		return strings.HasPrefix(clark, "{"+uri+"}")
	case strings.HasPrefix(tok, "*:"):
		local := strings.TrimPrefix(tok, "*:")
		if i := strings.LastIndexByte(clark, '}'); i >= 0 {
			return clark[i+1:] == local
		}
		return clark == local
	case strings.HasPrefix(tok, "Q{") && strings.HasSuffix(tok, "}*"):
		return strings.HasPrefix(clark, "{"+tok[2:len(tok)-2]+"}")
	}
	return clarkName(resolveQName(ac, tok)) == clark
}

// parseArityDigits parses a non-empty run of ASCII digits (the "#N" arity
// suffix of a function name token) as a non-negative int. Anything else
// (empty, a sign, non-digits) is not an arity suffix at all — the "#" was
// part of something else — so the caller leaves tok untouched.
func parseArityDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// checkUsePackage applies the static rules that govern one xsl:use-package
// element against the manifest of the package it uses (usedAbs is that used
// package's own resolved abs path — its identity for XTSE3050 purposes).
// accepted accumulates the symbolic names already accepted, keyed by the abs
// path of the used package they came from, from EARLIER xsl:use-package
// elements of the same package manifest.
func checkUsePackage(el *xmltree.Node, usedAbs string, exports []pkgComponent, accepted map[string]string) error {
	byName := map[string]pkgComponent{}
	for _, comp := range exports {
		byName[comp.symbolic()] = comp
	}

	// --- xsl:override children -------------------------------------------
	overridden := map[string]bool{}
	for _, ov := range elementChildren(el) {
		if ov.Name.Space != NS || ov.Name.Local != "override" {
			continue
		}
		for _, d := range elementChildren(ov) {
			if d.Name.Space != NS {
				continue
			}
			if err := checkOverrideDecl(d, byName, overridden); err != nil {
				return err
			}
		}
	}

	// --- xsl:accept children ---------------------------------------------
	for _, ac := range elementChildren(el) {
		if ac.Name.Space != NS || ac.Name.Local != "accept" {
			continue
		}
		vis := strings.TrimSpace(attrOr(ac, "visibility", ""))
		toks := strings.Fields(attrOr(ac, "names", ""))
		allWild := true
		matchedAny := false
		for _, tok := range toks {
			if isWildcardToken(tok) {
				for _, comp := range exports {
					if acceptMatches(ac, comp) {
						matchedAny = true
						break
					}
				}
				continue
			}
			allWild = false
			// XTSE3051: a non-wildcard token naming a component that this same
			// xsl:use-package overrides.
			hit := false
			for _, comp := range exports {
				if !acceptTokenMatches(ac, tok, comp) {
					continue
				}
				if kinds := strings.TrimSpace(attrOr(ac, "component", "")); kinds != "*" {
					ok := false
					for _, k := range strings.Fields(kinds) {
						if k == comp.kind || k == "*" {
							ok = true
							break
						}
					}
					if !ok {
						continue
					}
				}
				hit, matchedAny = true, true
				if overridden[comp.symbolic()] {
					return errAt(ac, "err:XTSE3051: xsl:accept names %q, a component overridden by the same xsl:use-package", tok)
				}
				// XTSE3040: the accepted visibility must be compatible with
				// the component's visibility in the used package.
				if !visibilityCompatible(comp.visibility, vis) {
					return errAt(ac, "err:XTSE3040: cannot accept %s %q (visibility %s) with visibility %q", comp.kind, tok, comp.visibility, vis)
				}
			}
			if !hit {
				// A non-wildcard token may also name a component that exists
				// but is PRIVATE in the used package, which never reaches the
				// manifest; that is an incompatible visibility (XTSE3040), not
				// a no-match. Distinguishing the two needs the used package's
				// private declarations, which checkOverrideDecl's caller
				// supplies via byName only for exposed ones, so treat an
				// unmatched non-wildcard token as no-match here and let the
				// XTSE3030 test below fire when nothing at all matched.
				_ = hit
			}
		}
		// XTSE3030: the xsl:accept matched no component at all, and not every
		// token was a wildcard.
		if !matchedAny && !allWild {
			return errAt(ac, "err:XTSE3030: xsl:accept matches no component of the used package")
		}
	}

	// --- XTSE3050: homonymous components accepted from two used packages ---
	for _, comp := range exports {
		sym := comp.symbolic()
		if prev, dup := accepted[sym]; dup && prev != usedAbs {
			return errAt(el, "err:XTSE3050: %s %q is accepted from more than one used package", comp.kind, comp.name)
		}
		accepted[sym] = usedAbs
	}
	return nil
}

// visibilityCompatible implements the xsl:accept visibility table (XSLT 3.0
// §3.5.5): a public component may be accepted with any visibility; a final one
// may not be made public or abstract; an abstract one may take any; a private
// component is not in the manifest at all.
func visibilityCompatible(declared, accepted string) bool {
	if accepted == "" {
		return true
	}
	switch declared {
	case "public", "abstract":
		switch accepted {
		case "public", "private", "final", "abstract", "hidden":
			return true
		}
		return false
	case "final":
		switch accepted {
		case "private", "final", "hidden":
			return true
		}
		return false
	}
	return false
}

// checkOverrideDecl applies the rules governing one declaration inside an
// xsl:override: XTSE3058 (must override something), XTSE3060 (the overridden
// component must be public or abstract), XTSE3070 (compatible signature),
// XTSE3440 (a template rule's mode) and XTSE3460 (no xsl:apply-imports).
func checkOverrideDecl(d *xmltree.Node, byName map[string]pkgComponent, overridden map[string]bool) error {
	name, hasName := d.AttrLocal("name")
	_, hasMatch := d.AttrLocal("match")

	if d.Name.Local == "template" && hasMatch && !hasName {
		// A TEMPLATE RULE inside xsl:override overrides a MODE, not a named
		// component.
		if err := checkOverrideRuleMode(d); err != nil {
			return err
		}
		return checkNoApplyImports(d)
	}
	if !hasName || !componentDeclKinds[d.Name.Local] {
		return nil
	}
	comp, ok := componentOf(d)
	if !ok {
		return nil
	}
	sym := comp.symbolic()
	target, found := byName[sym]
	if !found {
		return errAt(d, "err:XTSE3058: xsl:override declares %s %q, which is not a component of the used package", d.Name.Local, strings.TrimSpace(name))
	}
	if target.visibility != "public" && target.visibility != "abstract" {
		return errAt(d, "err:XTSE3060: %s %q has visibility %q in the used package and cannot be overridden", d.Name.Local, strings.TrimSpace(name), target.visibility)
	}
	overridden[sym] = true
	if err := checkOverrideSignature(d, target); err != nil {
		return err
	}
	return checkNoApplyImports(d)
}

// checkOverrideSignature implements XTSE3070: an overriding component's
// signature must be compatible with the overridden one's. Compared here are
// the declared result type (@as) and, for a function, its parameter types —
// the parts of the signature written in the stylesheet. A type written
// differently but equivalently (a rare case in practice) is deliberately NOT
// resolved: only a DIFFERENT declared type is reported, so the check cannot
// fire on a component whose signature was simply repeated verbatim.
func checkOverrideSignature(d *xmltree.Node, target pkgComponent) error {
	if target.el == nil {
		return nil
	}
	norm := func(n *xmltree.Node) string {
		v, ok := n.AttrLocal("as")
		if !ok {
			return ""
		}
		return strings.Join(strings.Fields(v), " ")
	}
	if a, b := norm(d), norm(target.el); a != b && a != "" && b != "" {
		return errAt(d, "err:XTSE3070: overriding %s %q declares as=%q but the overridden component declares as=%q",
			d.Name.Local, target.name, a, b)
	}
	if d.Name.Local != "function" {
		return nil
	}
	params := func(n *xmltree.Node) []string {
		var out []string
		for _, ch := range elementChildren(n) {
			if ch.Name.Space == NS && ch.Name.Local == "param" {
				out = append(out, norm(ch))
			}
		}
		return out
	}
	pa, pb := params(d), params(target.el)
	if len(pa) != len(pb) {
		return errAt(d, "err:XTSE3070: overriding function %q has a different arity", target.name)
	}
	for i := range pa {
		if pa[i] != pb[i] && pa[i] != "" && pb[i] != "" {
			return errAt(d, "err:XTSE3070: overriding function %q declares parameter %d as %q but the overridden component declares %q",
				target.name, i+1, pa[i], pb[i])
		}
	}
	return nil
}

// checkOverrideRuleMode implements XTSE3440: a template rule inside
// xsl:override must name a specific, non-unnamed mode — #all and #unnamed are
// never allowed, and #default (or an omitted @mode) is an error when the
// default mode in scope is the unnamed mode.
func checkOverrideRuleMode(d *xmltree.Node) error {
	mode, has := d.AttrLocal("mode")
	defUnnamed := defaultModeWalk(d) == ""
	if !has {
		if defUnnamed {
			return errAt(d, "err:XTSE3440: a template rule in xsl:override must specify a named mode")
		}
		return nil
	}
	for _, tok := range strings.Fields(mode) {
		switch tok {
		case "#all", "#unnamed":
			return errAt(d, "err:XTSE3440: mode=%q is not allowed on a template rule in xsl:override", tok)
		case "#default":
			if defUnnamed {
				return errAt(d, "err:XTSE3440: mode=\"#default\" in xsl:override resolves to the unnamed mode")
			}
		}
	}
	return nil
}

// checkNoApplyImports implements XTSE3460: xsl:apply-imports may not appear in
// a template rule declared within xsl:override (xsl:next-match is the way to
// reach the overridden rule).
func checkNoApplyImports(d *xmltree.Node) error {
	var walk func(n *xmltree.Node) error
	walk = func(n *xmltree.Node) error {
		if n.Kind == xmltree.KindElement && n.Name.Space == NS && n.Name.Local == "apply-imports" {
			return errAt(n, "err:XTSE3460: xsl:apply-imports is not allowed in a template rule declared in xsl:override")
		}
		for _, ch := range n.Children {
			if err := walk(ch); err != nil {
				return err
			}
		}
		return nil
	}
	for _, ch := range d.Children {
		if err := walk(ch); err != nil {
			return err
		}
	}
	return nil
}
