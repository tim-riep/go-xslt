package xslt

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// ---------------------------------------------------------------------------
// Static variables and parameters across modules (XSLT 3.0 §3.6).
//
// A static variable/parameter is in scope for every use-when expression and
// shadow attribute that FOLLOWS it in "stylesheet tree order" — the order the
// declarations appear in when each xsl:include/xsl:import is replaced, in
// place, by the declarations of the module it names. That is a cross-module
// order, so it cannot be established one module at a time: applyStatic on the
// principal module alone runs before any included module has been loaded, and
// would leave a variable declared in an included module undefined for the rest
// of the including module (use-when-0133).
//
// This pass therefore walks the whole include/import graph in tree order ONCE,
// before any module is pruned, recording each static declaration's value with
// the IMPORT PRECEDENCE of the module that made it. applyStatic then keeps its
// per-module validation but no longer decides the value of a name this pass
// already settled.
// ---------------------------------------------------------------------------

// staticModule is one stylesheet module's slot in the import-precedence order.
// prec mirrors gatherModules' numbering exactly: a module is numbered AFTER
// every module it imports, so a higher number is a higher import precedence,
// and two sibling xsl:import elements get distinct, increasing numbers (which
// is what makes importing the SAME module twice with different static values a
// conflict — use-when-0137, W3C bug 24478). xsl:include contributes no slot of
// its own: the included module's declarations belong to the includer.
type staticModule struct{ prec int }

// staticDeclRec is one static variable/parameter declaration, in the order the
// cross-module walk met it.
type staticDeclRec struct {
	key   string
	value xpath.Object
	unset bool
	mod   *staticModule
	el    *xmltree.Node
	kind  string
	name  string
}

// collectStaticsTreeOrder establishes every static variable/parameter in the
// include/import graph rooted at root, then applies the import-precedence
// rules to decide each name's effective value and to report XTSE3450.
func (c *compiler) collectStaticsTreeOrder(root *xmltree.Node, baseDir string) error {
	if c.staticParams == nil {
		c.staticParams = map[string]xpath.Object{}
	}
	counter := 0
	principal := &staticModule{}
	if err := c.walkStatics(root, baseDir, principal, true, &counter, map[string]bool{}, true); err != nil {
		return err
	}
	return c.resolveStaticDecls()
}

// walkStatics visits one module in stylesheet tree order. owner is the module
// whose import precedence the declarations found here belong to; owns is true
// when this call is responsible for numbering it (false for an xsl:include,
// which merges into its includer).
func (c *compiler) walkStatics(root *xmltree.Node, baseDir string, owner *staticModule, owns bool, counter *int, seen map[string]bool, isPrincipal bool) error {
	if err := c.expandShadowAttrs(root); err != nil {
		return err
	}
	// use-when="false" on a SECONDARY module's root excludes the whole module,
	// declarations included (use-when-0116).
	if !isPrincipal {
		if uw, ok := useWhenAttr(root); ok {
			env := &staticEnv{el: root, params: c.staticParams, resolver: c.staticResolver()}
			v, err := env.eval(uw)
			if err != nil {
				return errAt(root, "use-when: %v", err)
			}
			if !xpath.ToBool(v) {
				if owns {
					owner.prec = *counter
					*counter++
				}
				return nil
			}
		}
	}
	for _, ch := range elementChildren(root) {
		if ch.Name.Space != NS {
			continue
		}
		if err := c.expandShadowAttrs(ch); err != nil {
			return err
		}
		switch ch.Name.Local {
		case "include", "import":
			sub, subBase, abs, ok := c.loadStaticModule(ch, baseDir, seen)
			if !ok {
				// Unloadable/embedded/cyclic: gatherModules produces the real
				// diagnostic; this pass gathers only what it can see.
				continue
			}
			var err error
			if ch.Name.Local == "import" {
				m := &staticModule{}
				err = c.walkStatics(sub, subBase, m, true, counter, seen, false)
			} else {
				err = c.walkStatics(sub, subBase, owner, false, counter, seen, false)
			}
			delete(seen, abs)
			if err != nil {
				return err
			}
		case "param", "variable":
			if err := c.recordStaticDecl(ch, owner); err != nil {
				return err
			}
		}
	}
	if owns {
		owner.prec = *counter
		*counter++
	}
	return nil
}

// recordStaticDecl evaluates one static declaration and appends it to the
// tree-ordered list. Its @select sees the bindings made so far IN TREE ORDER
// (use-when-0137 reads $flip-flop back between three imports of the module
// that sets it), so the binding is applied immediately; precedence is settled
// afterwards by resolveStaticDecls.
func (c *compiler) recordStaticDecl(ch *xmltree.Node, owner *staticModule) error {
	st, _ := ch.AttrLocal("static")
	if !isXSLTTrue(strings.TrimSpace(st)) {
		return nil
	}
	if uw, ok := useWhenAttr(ch); ok {
		env := &staticEnv{el: ch, params: c.staticParams, resolver: c.staticResolver()}
		v, err := env.eval(uw)
		if err != nil {
			return errAt(ch, "use-when: %v", err)
		}
		if !xpath.ToBool(v) {
			return nil
		}
	}
	name, _ := ch.AttrLocal("name")
	qn := resolveQName(ch, name)
	key := clark(qn.Space, qn.Local)

	rec := staticDeclRec{key: key, mod: owner, el: ch, kind: ch.Name.Local, name: name}
	if sel, ok := c.hostStaticSelect(ch, key); ok {
		env := &staticEnv{el: ch, params: c.staticParams, resolver: c.staticResolver()}
		v, err := env.eval(sel)
		if err != nil {
			return errAt(ch, "static param %s: %v", name, err)
		}
		rec.value = v
	} else if sel, ok := ch.AttrLocal("select"); ok {
		env := &staticEnv{el: ch, params: c.staticParams, resolver: c.staticResolver()}
		v, err := env.eval(sel)
		if err != nil {
			return errAt(ch, "static %s %s: %v", ch.Name.Local, name, err)
		}
		rec.value = v
	} else {
		rec.unset = true
		rec.value = ""
	}
	c.staticRecs = append(c.staticRecs, rec)
	c.staticParams[key] = rec.value
	return nil
}

// resolveStaticDecls applies the import-precedence rules to the declarations
// collected in tree order: the HIGHEST-precedence declaration of each name
// supplies its value, and it is a static error (XTSE3450) for a declaration to
// disagree with one of the SAME name that appears EARLIER in tree order and
// has LOWER import precedence.
func (c *compiler) resolveStaticDecls() error {
	if c.staticDecls == nil {
		c.staticDecls = map[string]staticDecl{}
	}
	seenByKey := map[string][]staticDeclRec{}
	for _, rec := range c.staticRecs {
		for _, prev := range seenByKey[rec.key] {
			if prev.mod.prec < rec.mod.prec && !sameStaticValue(prev, rec) {
				return errAt(rec.el, "err:XTSE3450: static %s %q is inconsistent with a declaration of lower import precedence earlier in the stylesheet", rec.kind, rec.name)
			}
		}
		seenByKey[rec.key] = append(seenByKey[rec.key], rec)
	}
	for key, recs := range seenByKey {
		best := recs[0]
		for _, r := range recs[1:] {
			// Ties (the same module, or two modules at the same precedence)
			// go to the later declaration in tree order.
			if r.mod.prec >= best.mod.prec {
				best = r
			}
		}
		c.staticDecls[key] = staticDecl{value: best.value, unset: best.unset}
		c.staticParams[key] = best.value
		if best.unset {
			if c.staticUnset == nil {
				c.staticUnset = map[string]bool{}
			}
			c.staticUnset[key] = true
		} else if c.staticUnset != nil {
			delete(c.staticUnset, key)
		}
	}
	return nil
}

// staticDecl is the settled binding of one static variable/parameter.
type staticDecl struct {
	value xpath.Object
	unset bool
}

// sameStaticValue reports whether two declarations of the same name agree.
// They must agree in KIND as well as in value: declaring a name as a static
// xsl:variable in one module and a static xsl:param in another is inconsistent
// however the two values compare (static-023 declares both as 1).
func sameStaticValue(a, b staticDeclRec) bool {
	if a.kind != b.kind {
		return false
	}
	if a.unset != b.unset {
		return false
	}
	if a.unset {
		return true
	}
	return xpath.ToString(a.value) == xpath.ToString(b.value)
}

// loadStaticModule parses the module an xsl:include/xsl:import names, without
// the binding/pruning loadFile performs (this pass IS what establishes the
// static context, so it must not recurse through that path).
func (c *compiler) loadStaticModule(el *xmltree.Node, baseDir string, seen map[string]bool) (*xmltree.Node, string, string, bool) {
	href, ok := el.AttrLocal("href")
	if !ok {
		return nil, "", "", false
	}
	hrefBase := baseDir
	if el.Base != "" {
		hrefBase = filepath.Dir(strings.TrimPrefix(el.Base, "file://"))
	}
	path := href
	if strings.IndexByte(path, '#') >= 0 {
		// An embedded module addressed by fragment; left to loadFile.
		return nil, "", "", false
	}
	if hrefBase == "" {
		return nil, "", "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(hrefBase, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", "", false
	}
	if seen[abs] {
		return nil, "", "", false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", "", false
	}
	doc, err := xmltree.ParseLenient11WithBase(string(data), filepath.Dir(abs))
	if err != nil {
		return nil, "", "", false
	}
	doc.Base = fileURI(abs)
	sub := xmltree.RootElement(doc)
	if sub == nil || sub.Name.Space != NS {
		return nil, "", "", false
	}
	seen[abs] = true
	return sub, filepath.Dir(abs), abs, true
}

// hasSecondaryModules reports whether root has any xsl:include/xsl:import
// child. The cross-module static pass is only needed — and only pays for its
// extra parse — when there is more than one module.
func hasSecondaryModules(root *xmltree.Node) bool {
	for _, ch := range elementChildren(root) {
		if ch.Name.Space == NS && (ch.Name.Local == "include" || ch.Name.Local == "import") {
			return true
		}
	}
	return false
}
