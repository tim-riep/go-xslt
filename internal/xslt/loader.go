package xslt

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// xmlURI is the reserved XML namespace (xml:id / xml:base on an embedded
// stylesheet module).
const xmlURI = "http://www.w3.org/XML/1998/namespace"

// modChildren is one stylesheet module's top-level children at a given import
// precedence (its position in the returned slice).
type modChildren struct {
	children []*xmltree.Node
	baseDir  string
	// importLo is the lowest import precedence this module transitively
	// IMPORTS. Because gatherModules appends a module's imported subtrees
	// contiguously just before the module itself, [importLo, ownPrecedence)
	// is exactly the set of modules imported (directly or indirectly) by this
	// one — which is what xsl:apply-imports may reach, as opposed to every
	// module of lower precedence anywhere in the stylesheet (import-1601).
	importLo int
}

// gatherModules expands xsl:import/xsl:include and returns the modules in
// import-precedence order, lowest first. Imported modules precede the importing
// module; xsl:include splices the included module's own declarations into the
// including module at the same precedence (its imports become lower modules).
func (c *compiler) gatherModules(root *xmltree.Node, baseDir string, seen map[string]bool) ([]modChildren, error) {
	var mods []modChildren
	var own []*xmltree.Node
	// Components accepted by the xsl:use-package elements of THIS package
	// manifest, for XTSE3050: symbolic name -> the abs path of the used
	// package it came from. Keyed by PACKAGE IDENTITY rather than by which
	// xsl:use-package element did the accepting, because the same package can
	// legitimately be named by two separate xsl:use-package elements of one
	// manifest (package-017/018, "a package using a package twice" — WG bug
	// 29233): that is one logical import, not a homonym collision, even
	// though it is two distinct xsl:use-package AST nodes. It must not be
	// shared across DIFFERENT package manifests: two different packages each
	// using the same library package is normal (use-package-160's diamond),
	// only two use-package elements of the SAME manifest accepting a homonym
	// FROM DIFFERENT PACKAGES is an error.
	accepted := map[string]string{}

	if v, ok := root.AttrLocal("input-type-annotations"); ok && v != "unspecified" {
		if c.inputTypeAnn == "" {
			c.inputTypeAnn = v
		} else if c.inputTypeAnn != v {
			return nil, errAt(root, "err:XTSE0265: conflicting input-type-annotations value %q (another module specifies %q)", v, c.inputTypeAnn)
		}
	}

	for _, child := range elementChildren(root) {
		if child.Name.Space == NS && child.Name.Local == "use-package" {
			sub, overrides, err := c.gatherUsedPackage(child, seen, accepted)
			if err != nil {
				return nil, err
			}
			// A used package's modules sit BELOW the using module, exactly as
			// an imported module does: that is what makes an overriding
			// template rule win, and what xsl:next-match/xsl:apply-imports
			// walk down into (use-package-172/173).
			base := len(mods)
			for i := range sub {
				sub[i].importLo += base
			}
			mods = append(mods, sub...)
			// xsl:override's own declarations belong to the USING module, at
			// its precedence — they override the used package's components.
			own = append(own, overrides...)
			continue
		}
		if child.Name.Space == NS && (child.Name.Local == "import" || child.Name.Local == "include") {
			// The include/import element is consumed here, so validate it now
			// (empty content, no stray attributes — XTSE0260/XTSE0090).
			if err := validateXSLTElement(child); err != nil {
				return nil, err
			}
			href, ok := child.AttrLocal("href")
			if !ok {
				return nil, errAt(child, "xsl:%s requires href", child.Name.Local)
			}
			hrefBase := baseDir
			// applyEntityBases records an entity-spliced node's origin on
			// EntityBase (Node.Base is reserved for an already-computed base
			// URI); either answers "where did this element come from?".
			ownBase := child.Base
			if ownBase == "" {
				ownBase = child.EntityBase
			}
			if ownBase != "" {
				// This xsl:import/xsl:include element itself came from an
				// external general entity spliced into the module
				// (xmltree.applyEntityBases sets Node.Base to the entity's own
				// location) — its href must resolve relative to the ENTITY's
				// directory, not the enclosing module's (include-0101:
				// xinc01c.xsl's own <xsl:import href="xinc01d.xsl"/> must find
				// x/xinc01d.xsl, not a sibling of the outer include-0101b.xsl).
				hrefBase = filepath.Dir(strings.TrimPrefix(ownBase, "file://"))
			}
			subRoot, subBase, abs, err := c.loadFile(href, hrefBase, seen)
			if err != nil {
				return nil, errAt(child, "%v", err)
			}
			if subRoot.Name.Space != NS {
				// An included/imported SIMPLIFIED stylesheet (a literal result
				// element carrying xsl:version) is equivalent to a module
				// whose single declaration is <xsl:template match="/"> around
				// that element (XSLT §3.7) — the principal-module path in
				// Compile already does this, and a secondary module needs the
				// same treatment (include-0401, include-0601,
				// embedded-stylesheet-016/017).
				subRoot = simplifiedModuleRoot(subRoot)
			}
			sub, err := c.gatherModules(subRoot, subBase, seen)
			// Un-mark abs once this subtree is fully gathered: "seen" guards
			// against a TRUE cycle (a module transitively importing/including
			// itself, still on the current load stack), not against the same
			// module being imported twice from unrelated places in the graph
			// — attribute-set-1815 imports the same module via two sibling
			// xsl:import elements, which XSLT explicitly allows (it creates
			// two copies at different import precedences).
			delete(seen, abs)
			if err != nil {
				return nil, err
			}
			// sub's importLo values are relative to sub's own start; rebase
			// them onto this module's list so they stay absolute precedences.
			base := len(mods)
			for i := range sub {
				sub[i].importLo += base
			}
			if child.Name.Local == "import" {
				// imported modules (all of them) get lower precedence
				mods = append(mods, sub...)
			} else if len(sub) > 0 {
				// include: the included module's imports are lower modules; its
				// own declarations merge into this module at the same precedence.
				mods = append(mods, sub[:len(sub)-1]...)
				own = append(own, sub[len(sub)-1].children...)
			}
			continue
		}
		own = append(own, child)
	}
	// This module sits above everything gathered so far, all of which it
	// imported (directly, or indirectly through an xsl:include).
	mods = append(mods, modChildren{children: own, baseDir: baseDir, importLo: 0})
	return mods, nil
}

// gatherUsedPackage expands one xsl:use-package element: it resolves the named
// library package against the host-supplied registry (packages.go), gathers
// that package's own modules (recursively, so a package that itself uses other
// packages works), and returns them lowest-precedence-first together with the
// declarations contributed by any xsl:override children.
//
// Every component of the used package is made visible to the using module.
// This processor does not enforce the @visibility hiding rules across a
// package boundary — see the note in packages.go.
func (c *compiler) gatherUsedPackage(el *xmltree.Node, seen map[string]bool, accepted map[string]string) ([]modChildren, []*xmltree.Node, error) {
	if err := validateXSLTElement(el); err != nil {
		return nil, nil, err
	}
	name, ok := el.AttrLocal("name")
	if !ok {
		return nil, nil, errAt(el, "err:XTSE0010: xsl:use-package requires a name attribute")
	}
	name = strings.TrimSpace(name)
	verSpec := "*"
	if v, ok := el.AttrLocal("package-version"); ok {
		verSpec = strings.TrimSpace(v)
	}
	src, err := c.resolvePackage(name, verSpec)
	if err != nil {
		return nil, nil, errAt(el, "%v", err)
	}
	abs, err := filepath.Abs(src.Path)
	if err != nil {
		return nil, nil, errAt(el, "%v", err)
	}
	if seen[abs] {
		// A package that transitively uses itself (XTSE3005).
		return nil, nil, errAt(el, "err:XTSE3005: package %q is used recursively", name)
	}
	seen[abs] = true
	defer delete(seen, abs)
	root, err := c.loadPackageRoot(abs)
	if err != nil {
		return nil, nil, errAt(el, "%v", err)
	}
	sub, err := c.gatherModules(root, filepath.Dir(abs), seen)
	if err != nil {
		return nil, nil, err
	}
	// The static rules on xsl:accept / xsl:override need the used package's
	// COMPONENT MANIFEST, which the flattened module list above deliberately
	// does not preserve; build it separately (pkgcheck.go).
	exports, err := c.packageExports(abs, root, map[string]bool{})
	if err != nil {
		return nil, nil, err
	}
	if err := checkUsePackage(el, abs, exports, accepted); err != nil {
		return nil, nil, err
	}
	var overrides []*xmltree.Node
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS {
			return nil, nil, errAt(ch, "err:XTSE0010: %s is not allowed inside xsl:use-package", ch.Name.Local)
		}
		switch ch.Name.Local {
		case "override":
			decls := elementChildren(ch)
			overrides = append(overrides, decls...)
			if c.overrideNodes == nil {
				c.overrideNodes = map[*xmltree.Node]bool{}
			}
			for _, d := range decls {
				c.overrideNodes[d] = true
			}
		case "accept":
			// xsl:accept only adjusts the visibility a component is accepted
			// with; since every component of a used package is already
			// visible here, there is nothing to do beyond validating it.
			if err := validateXSLTElement(ch); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, errAt(ch, "err:XTSE0010: xsl:%s is not allowed inside xsl:use-package", ch.Name.Local)
		}
	}
	return sub, overrides, nil
}

// loadPackageRoot reads and parses a library package module, returning its
// xsl:package root with static parameters bound and use-when subtrees pruned
// (the same preparation loadFile does for an imported module).
func (c *compiler) loadPackageRoot(abs string) (*xmltree.Node, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	doc, err := xmltree.ParseLenient11WithBase(string(data), filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	doc.Base = fileURI(abs)
	root := xmltree.RootElement(doc)
	if root == nil {
		return nil, errAt(nil, "package module %q has no root element", abs)
	}
	if root.Name.Space != NS || root.Name.Local != "package" {
		return nil, errAt(root, "err:XTSE0165: %q is not an xsl:package module", abs)
	}
	if err := c.applyStatic(root, false); err != nil {
		return nil, err
	}
	if err := checkPackageAttrs(root); err != nil {
		return nil, err
	}
	return root, nil
}

// simplifiedModuleRoot wraps a simplified stylesheet's literal result element
// in the xsl:stylesheet/xsl:template match="/" module it abbreviates, so that
// gatherModules and the declaration compiler can treat it like any other
// module. The literal element keeps its ORIGINAL parent link, so its in-scope
// namespaces, xpath-default-namespace and exclude-result-prefixes still
// resolve against the document it was actually written in.
func simplifiedModuleRoot(lit *xmltree.Node) *xmltree.Node {
	tmpl := xmltree.NewElement(xmltree.Name{Space: NS, Local: "template", Prefix: "xsl"})
	tmpl.SetAttr(xmltree.Name{Local: "match"}, "/")
	tmpl.Children = []*xmltree.Node{lit}
	root := xmltree.NewElement(xmltree.Name{Space: NS, Local: "stylesheet", Prefix: "xsl"})
	if v, ok := lit.Attr(NS, "version"); ok {
		root.SetAttr(xmltree.Name{Local: "version"}, v)
	}
	root.Parent, root.Base = lit.Parent, lit.Base
	root.Children = []*xmltree.Node{tmpl}
	tmpl.Parent = root
	return root
}

// findEmbeddedModule locates the embedded stylesheet module identified by a
// fragment identifier: the element carrying an ID-typed attribute (declared by
// the document's internal DTD subset) whose value is id. Failing that — a
// document with no ATTLIST declaration at all — an attribute literally named
// "id" is accepted, which is how every real embedded-stylesheet document in
// the wild is written.
func findEmbeddedModule(n *xmltree.Node, id string) *xmltree.Node {
	if n.Kind == xmltree.KindElement {
		for _, a := range n.Attrs {
			if a.Value != id {
				continue
			}
			if a.IDKind == xmltree.IDKindID ||
				(a.Name.Space == "" && a.Name.Local == "id") ||
				(a.Name.Space == xmlURI && a.Name.Local == "id") {
				return n
			}
		}
	}
	for _, ch := range n.Children {
		if m := findEmbeddedModule(ch, id); m != nil {
			return m
		}
	}
	return nil
}

// embeddedBaseDir applies the xml:base attributes in scope on an embedded
// stylesheet module (outermost first) to the containing document's directory,
// following relative-reference semantics: a base that does not end in "/"
// contributes only its directory part.
func embeddedBaseDir(el *xmltree.Node, dir string) string {
	var chain []string
	for cur := el; cur != nil; cur = cur.Parent {
		if cur.Kind != xmltree.KindElement {
			continue
		}
		if v, ok := cur.Attr(xmlURI, "base"); ok {
			chain = append(chain, v)
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		b := chain[i]
		if j := strings.LastIndexByte(b, '/'); j >= 0 {
			b = b[:j]
		} else {
			b = ""
		}
		if b == "" {
			continue
		}
		if p := filepath.FromSlash(b); filepath.IsAbs(p) {
			dir = p
		} else {
			dir = filepath.Join(dir, p)
		}
	}
	return dir
}

// loadFile resolves href against baseDir, reads + parses the stylesheet, and
// returns its root element, the directory to resolve nested references, and
// its own resolved absolute path (for the caller to un-mark in seen once the
// subtree rooted at it has been fully gathered — see gatherModules).
func (c *compiler) loadFile(href, baseDir string, seen map[string]bool) (*xmltree.Node, string, string, error) {
	if baseDir == "" {
		return nil, "", "", errAt(nil, "cannot resolve %q: no workspace directory", href)
	}
	// An href of the form "doc.xml#id" selects an EMBEDDED stylesheet module:
	// the element in that document whose ID-typed attribute has that value
	// (XSLT §3.10 — include-0102/0103).
	path, fragment := href, ""
	if i := strings.IndexByte(path, '#'); i >= 0 {
		path, fragment = path[:i], path[i+1:]
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", "", err
	}
	if seen[abs] {
		return nil, "", "", errAt(nil, "import/include cycle at %q", href)
	}
	seen[abs] = true
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", "", err
	}
	doc, err := xmltree.ParseLenient11WithBase(string(data), filepath.Dir(abs))
	if err != nil {
		return nil, "", "", err
	}
	// This module's own retrieval location (its exact file, known here unlike
	// the primary module's) becomes its intrinsic base-uri: document()/doc()
	// calls with a relative href in THIS module resolve against ITS OWN
	// directory, not the primary stylesheet's (document-1901), and doc('')
	// inside it returns ITS OWN tree (document-1001/1002 — see fnDoc's
	// HomeDoc special case, which doesn't even need this, but static-base-uri
	// and any other relative resolution here does).
	doc.Base = fileURI(abs)
	if fragment != "" {
		el := findEmbeddedModule(doc, fragment)
		if el == nil {
			return nil, "", "", errAt(nil, "err:XTSE0165: %q contains no element with ID %q", path, fragment)
		}
		if err := c.applyStatic(el, false); err != nil {
			return nil, "", "", err
		}
		// An embedded module may carry xml:base (include-0103), which governs
		// how ITS own xsl:include/import hrefs resolve.
		return el, embeddedBaseDir(el, filepath.Dir(abs)), abs, nil
	}
	root := xmltree.RootElement(doc)
	if root == nil {
		return nil, "", "", errAt(nil, "%q has no root element", href)
	}
	// The target must be a stylesheet module: xsl:stylesheet/xsl:transform, or
	// a simplified stylesheet (literal root with xsl:version) — XTSE0165.
	if root.Name.Space != NS || (root.Name.Local != "stylesheet" && root.Name.Local != "transform") {
		if _, ok := root.Attr(NS, "version"); !ok {
			return nil, "", "", errAt(root, "err:XTSE0165: %q is not an XSLT stylesheet module", href)
		}
	}
	// Bind any static parameters this module adds and prune its use-when subtrees
	// before it is gathered/compiled.
	if err := c.applyStatic(root, false); err != nil {
		return nil, "", "", err
	}
	return root, filepath.Dir(abs), abs, nil
}
