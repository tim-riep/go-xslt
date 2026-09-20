package xsd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// schemaDoc is one schema document (the primary plus any transitively
// included/imported/redefined documents), with its own namespace context.
type schemaDoc struct {
	el           *xmltree.Node
	tns          string
	elemQ        bool
	attrQ        bool
	finalDefault string
	blockDefault string
	baseDir      string
	// chameleon is true when this document declared no targetNamespace of its own
	// but adopted one from an including schema (xs:include/redefine/override). In
	// such a document an *unqualified* reference to a component of the same document
	// resolves to the adopted namespace, not to no-namespace (the widely-implemented
	// chameleon-inclusion reading). See resolveType/chamAlt.
	chameleon bool
	// imports holds the namespaces this document itself declares an xs:import for
	// (the empty string for an import with no @namespace). A QName reference may
	// only reach a namespace that is this document's own or one of these.
	imports map[string]bool
	// defaultAttrs: xs:schema/@defaultAttributes (1.1) — the attribute group
	// every complexType of THIS document gains unless it opts out with
	// defaultAttributesApply="false" (§3.4.2.1, open035/044/045, s3_4_2_4).
	defaultAttrs    xname
	hasDefaultAttrs bool
	// defaultOpenNode: this document's xs:defaultOpenContent element (1.1),
	// materialized per complexType in applyOpenContent; appliesToEmpty extends
	// it to empty content models (§4.2.2, open009-013/031/040-043).
	defaultOpenNode *xmltree.Node
}

// collect resolves the include/import/redefine/override graph starting from a
// schema element, appending every reachable schema document to out. Missing
// files are skipped (import without a resolvable location is legal). A visited
// set (by absolute path) breaks cycles.
// vcNS is the XSD versioning namespace: vc:minVersion / vc:maxVersion mark
// schema elements for conditional inclusion — a processor RETAINS an element
// iff minVersion <= its version < maxVersion, and must otherwise remove it
// before any other processing (so a 1.0 processor never sees a guarded
// xs:assert, and a guarded alternative never reaches the 1.1 grammar checks).
const vcNS = "http://www.w3.org/2007/XMLSchema-versioning"

// stripVersioning removes, in place, every descendant element excluded by its
// vc:minVersion/vc:maxVersion for processor version ver. It reports whether the
// root element itself is retained — an excluded document element means the whole
// document contributes no components (§4.2.1: conditional inclusion is applied
// before any other processing). Under 1.1 a lexically-invalid vc attribute value
// is a schema error (vc901/vc902: 1.0 tolerates the same value, bug 13906).
func stripVersioning(el *xmltree.Node, ver Version) (bool, error) {
	our := 1.0
	if ver == Version11 {
		our = 1.1
	}
	var vcErr error
	setErr := func(err error) {
		if vcErr == nil {
			vcErr = err
		}
	}
	// vcDecimal parses a vc version number, recording a 1.1 schema error on a
	// value that is not a lexical xs:decimal.
	vcDecimal := func(v, attr string) (float64, bool) {
		v = strings.TrimSpace(v)
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || strings.ContainsAny(v, "eExX+") {
			if ver == Version11 {
				setErr(invalidf("", "invalid vc:%s value %q", attr, v))
			}
			return 0, false
		}
		return f, true
	}
	// vcQNames checks each token of a vc type/facet list: it must be a lexical
	// QName whose prefix (if any) is declared in scope (vc904/vc905, 1.1 only).
	vcQNames := func(n *xmltree.Node, v, attr string) {
		if ver != Version11 {
			return
		}
		for _, tok := range strings.Fields(v) {
			if !validQNameLexical(tok) {
				setErr(invalidf("", "invalid QName %q in vc:%s", tok, attr))
				return
			}
			if i := strings.IndexByte(tok, ':'); i >= 0 {
				if _, ok := n.LookupPrefix(tok[:i]); !ok {
					setErr(invalidf("", "undeclared prefix %q in vc:%s", tok[:i], attr))
					return
				}
			}
		}
	}
	var keep func(n *xmltree.Node) bool
	keep = func(n *xmltree.Node) bool {
		if n.Kind != xmltree.KindElement {
			return true
		}
		if v, ok := n.Attr(vcNS, "minVersion"); ok {
			if f, ok := vcDecimal(v, "minVersion"); ok && our < f {
				return false
			}
		}
		if v, ok := n.Attr(vcNS, "maxVersion"); ok {
			if f, ok := vcDecimal(v, "maxVersion"); ok && our >= f {
				return false
			}
		}
		if v, ok := n.Attr(vcNS, "facetAvailable"); ok {
			vcQNames(n, v, "facetAvailable")
			for _, f := range strings.Fields(v) {
				if !vcFacetSupported(f, ver) {
					return false
				}
			}
		}
		if v, ok := n.Attr(vcNS, "facetUnavailable"); ok {
			vcQNames(n, v, "facetUnavailable")
			// Retained iff at least one named facet is unsupported.
			all := true
			for _, f := range strings.Fields(v) {
				if !vcFacetSupported(f, ver) {
					all = false
					break
				}
			}
			if all {
				return false
			}
		}
		if v, ok := n.Attr(vcNS, "typeAvailable"); ok {
			vcQNames(n, v, "typeAvailable")
			for _, t := range strings.Fields(v) {
				if !vcTypeAvailable(t, ver) {
					return false
				}
			}
		}
		if v, ok := n.Attr(vcNS, "typeUnavailable"); ok {
			vcQNames(n, v, "typeUnavailable")
			// Retained iff at least one named type is unavailable (vc013: a mix
			// of known and unknown types keeps the element).
			all := true
			for _, t := range strings.Fields(v) {
				if !vcTypeAvailable(t, ver) {
					all = false
					break
				}
			}
			if all {
				return false
			}
		}
		kept := n.Children[:0]
		for _, ch := range n.Children {
			if keep(ch) {
				kept = append(kept, ch)
			}
		}
		n.Children = kept
		return true
	}
	rootKept := keep(el)
	return rootKept, vcErr
}

// vcFacetSupported reports whether this processor, at version ver, supports
// the named constraining facet (QName prefixes are ignored — the vc namespace
// vocabulary names only XSD facets).
func vcFacetSupported(qname string, ver Version) bool {
	local := qname
	if i := strings.LastIndex(qname, ":"); i >= 0 {
		local = qname[i+1:]
	}
	switch local {
	case "length", "minLength", "maxLength", "pattern", "enumeration",
		"whiteSpace", "totalDigits", "fractionDigits",
		"minInclusive", "maxInclusive", "minExclusive", "maxExclusive":
		return true
	case "assertion", "explicitTimezone":
		return ver == Version11
	}
	return false
}

// vcTypeAvailable reports whether the named built-in type exists at version ver.
func vcTypeAvailable(qname string, ver Version) bool {
	local := qname
	if i := strings.LastIndex(qname, ":"); i >= 0 {
		local = qname[i+1:]
	}
	if xsd11OnlyBuiltin[local] {
		return ver == Version11
	}
	switch local {
	case "NMTOKENS", "IDREFS", "ENTITIES":
		return true // built-in list types (both versions)
	}
	_, ok := xpath.AtomTypeByName(local)
	return ok
}

func (c *compiler) collect(el *xmltree.Node, baseDir, selfPath string, out *[]*schemaDoc, visited map[string]bool, adoptNs string) error {
	rootKept, vcErr := stripVersioning(el, c.sch.version)
	if vcErr != nil {
		return vcErr
	}
	if !rootKept {
		// The document element itself is vc-excluded: the document contributes
		// no components, and its includes/imports are never followed (vc006).
		return nil
	}
	sd := &schemaDoc{
		el:      el,
		baseDir: baseDir,
		elemQ:   attrIs(el, "elementFormDefault", "qualified"),
		attrQ:   attrIs(el, "attributeFormDefault", "qualified"),
	}
	var hasTns bool
	sd.tns, hasTns = el.AttrLocal("targetNamespace")
	if hasTns && sd.tns == "" {
		// A no-namespace schema omits @targetNamespace; the empty string is not a
		// namespace name.
		return invalidf("", "targetNamespace must not be the empty string")
	}
	if !hasTns && adoptNs != "" {
		sd.tns = adoptNs // chameleon: a no-namespace include adopts the including ns
		sd.chameleon = true
	}
	sd.imports = map[string]bool{}
	for _, ch := range el.Children {
		if ch.Kind == xmltree.KindElement && ch.Name.Space == xsNS && ch.Name.Local == "import" {
			ns, _ := ch.AttrLocal("namespace")
			sd.imports[ns] = true
		}
	}
	sd.finalDefault, _ = el.AttrLocal("finalDefault")
	sd.blockDefault, _ = el.AttrLocal("blockDefault")
	if v, ok := el.AttrLocal("defaultAttributes"); ok && c.sch.version == Version11 {
		sd.defaultAttrs = resolveQName(el, v)
		sd.hasDefaultAttrs = true
	}
	*out = append(*out, sd)

	for _, ch := range el.Children {
		if ch.Kind != xmltree.KindElement || ch.Name.Space != xsNS {
			continue
		}
		switch ch.Name.Local {
		case "include", "import", "redefine", "override":
			// src-import: an import whose @namespace is absent may only appear in a
			// schema that itself has a target namespace.
			if ch.Name.Local == "import" {
				ns, hasNs := ch.AttrLocal("namespace")
				if !hasNs && sd.tns == "" {
					return invalidf("", "import without a namespace requires a targetNamespace")
				}
				// src-import.1.1/1.2: the namespace must differ from the importing
				// schema's own, and the empty string is not a namespace name.
				if hasNs && ns == "" {
					return invalidf("src-import.1.2", "xs:import namespace must not be the empty string")
				}
				if hasNs && ns == sd.tns {
					return invalidf("src-import.1.1", "a schema may not import its own target namespace %q", ns)
				}
			}
			loc, ok := ch.AttrLocal("schemaLocation")
			if !ok && ch.Name.Local != "import" {
				// schemaLocation is optional only on xs:import (schB3).
				return invalidf("", "xs:%s requires a schemaLocation", ch.Name.Local)
			}
			if !ok || baseDir == "" {
				continue // import with no location: components simply unavailable
			}
			path := filepath.Clean(filepath.Join(baseDir, loc))
			if ch.Name.Local == "override" && c.sch.version == Version11 {
				// src-override: distinct {kind, name} among one override's
				// children (over021).
				if err := checkOverrideUniqueness(ch); err != nil {
					return err
				}
				// An override WITH component children goes through the §4.2.5
				// transformation; a childless one keeps the include-like path
				// below (over023a). 1.0 never sees xs:override semantics.
				if len(overrideComponents(ch)) > 0 {
					if err := c.collectOverride(ch, sd, path, loc, out, visited); err != nil {
						return err
					}
					continue
				}
			}
			if ch.Name.Local == "redefine" {
				if path == selfPath {
					// src-redefine.1: a schema document cannot redefine itself.
					return invalidf("src-redefine.1", "xs:redefine targets its own schema document")
				}
				if c.redefineEdges == nil {
					c.redefineEdges = map[string][]string{}
				}
				c.redefineEdges[selfPath] = append(c.redefineEdges[selfPath], path)
				// Remember WHICH components this directive redefines in the
				// target document, for the mutual-cycle discriminator below.
				if c.redefTargets == nil {
					c.redefTargets = map[string]map[string]bool{}
				}
				if c.redefTargets[path] == nil {
					c.redefTargets[path] = map[string]bool{}
				}
				for comp := range redefChildNames(ch) {
					c.redefTargets[path][comp] = true
				}
				// src-redefine.2 for INDIRECT cycles: two documents redefining
				// each other (s4_2_4si01s/bs).
				seen := map[string]bool{selfPath: true}
				stack := []string{path}
				for len(stack) > 0 {
					cur := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					if cur == selfPath {
						return invalidf("src-redefine.2", "circular xs:redefine between schema documents")
					}
					if seen[cur] && cur != path {
						continue
					}
					for _, nxt := range c.redefineEdges[cur] {
						if nxt == selfPath {
							return invalidf("src-redefine.2", "circular xs:redefine between schema documents")
						}
						if !seen[nxt] {
							seen[nxt] = true
							stack = append(stack, nxt)
						}
					}
				}
			}
			if c.tnsByPath == nil {
				c.tnsByPath = map[string]string{}
			}
			if cached, seen := c.tnsByPath[path]; seen {
				// Already collected via another route — the namespace-consistency
				// rules still apply to THIS directive (schF6), and a REDEFINE of
				// an already-collected document is circular or conflicting
				// (s4_2_4si01, schN7).
				if ch.Name.Local == "redefine" {
					return invalidf("src-redefine.2", "xs:redefine targets an already-collected schema document")
				}
				if err := directiveTnsOK(ch, sd.tns, cached, loc); err != nil {
					return err
				}
				continue
			}
			if visited[path] {
				continue
			}
			visited[path] = true
			data, err := os.ReadFile(path)
			if err != nil {
				continue // unresolvable location — skip
			}
			doc, err := xmltree.ParseLenient11(string(data))
			if err != nil {
				return invalidf("", "included schema %s not well-formed: %v", loc, err)
			}
			sub := xmltree.RootElement(doc)
			if sub == nil || sub.Name.Space != xsNS || sub.Name.Local != "schema" {
				return invalidf("", "included %s is not a schema", loc)
			}
			subTns, _ := sub.AttrLocal("targetNamespace")
			c.tnsByPath[path] = subTns
			if err := directiveTnsOK(ch, sd.tns, subTns, loc); err != nil {
				return err
			}
			// Dedupe by content too: a cyclic import that resolves back to an
			// already-collected document (including the primary) must not be
			// re-collected, or its components would register twice. A REDEFINE
			// resolving back here is a mutual-redefine cycle: it is an error
			// exactly when the two arcs redefine the SAME component — c1 both
			// ways is a non-well-founded derivation (ibm s4_2_4si01/si01b) —
			// while disjoint arcs stay tolerated (schU1, disputed-test 4135,
			// redefines a different attributeGroup in each direction).
			if visited["c:"+string(data)] {
				if ch.Name.Local == "redefine" {
					for comp := range redefChildNames(ch) {
						if c.redefTargets[selfPath][comp] {
							return invalidf("src-redefine.2",
								"mutual xs:redefine of the same component %s", comp)
						}
					}
				}
				continue
			}
			visited["c:"+string(data)] = true
			// (moved) An include/redefine/override of a no-namespace schema adopts this
			// document's target namespace (chameleon); an import keeps its own.
			subAdopt := ""
			if ch.Name.Local != "import" {
				subAdopt = sd.tns
			}
			if err := c.collect(sub, filepath.Dir(path), path, out, visited, subAdopt); err != nil {
				return err
			}
		}
	}
	return nil
}

// collectOverride loads the target of an xs:override carrying component
// children, applies the override transformation, and collects the REWRITTEN
// document. The visited/dedup keys carry the override's signature: the same
// path under a DIFFERENT override is a fresh collection (over022's duplicate
// globals), while the merge fixpoint in override cycles terminates because a
// repeated (path, signature) pair is skipped (over023 valid, over024's
// mutating cycle still surfaces its duplicates).
func (c *compiler) collectOverride(ch *xmltree.Node, sd *schemaDoc, path, loc string, out *[]*schemaDoc, visited map[string]bool) error {
	sig := ovSignature(ch)
	key := "ov:" + path + "\x00" + sig
	if visited[key] {
		return nil
	}
	visited[key] = true
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // unresolvable location: components simply unavailable
	}
	doc, err := xmltree.ParseLenient11(string(data))
	if err != nil {
		return invalidf("", "overridden schema %s not well-formed: %v", loc, err)
	}
	sub := xmltree.RootElement(doc)
	if sub == nil || sub.Name.Space != xsNS || sub.Name.Local != "schema" {
		return invalidf("", "overridden %s is not a schema", loc)
	}
	subTns, _ := sub.AttrLocal("targetNamespace")
	if c.tnsByPath == nil {
		c.tnsByPath = map[string]string{}
	}
	if _, seen := c.tnsByPath[path]; !seen {
		c.tnsByPath[path] = subTns
	}
	if err := directiveTnsOK(ch, sd.tns, subTns, loc); err != nil {
		return err
	}
	ckey := "ovc:" + sig + "\x00" + string(data)
	if visited[ckey] {
		return nil
	}
	visited[ckey] = true
	// Mark the ORIGINAL content as consumed-by-override: a supplementary
	// document handed to Compile that is also an override target (the suite's
	// role="overridden") contributes only its TRANSFORMED self, never its
	// plain components alongside (s3_4_2_4ii08/ii10).
	visited["overridden:"+string(data)] = true
	transformed := applyOverride(sub, ch)
	mark := len(*out)
	if err := c.collect(transformed, filepath.Dir(path), path, out, visited, sd.tns); err != nil {
		return err
	}
	// Bug 17574: QName references inside the transplanted override children
	// must also reach the namespaces the OVERRIDING document imports
	// (over029's spc:points).
	if len(*out) > mark {
		tsd := (*out)[mark]
		for ns := range sd.imports {
			tsd.imports[ns] = true
		}
	}
	return nil
}

// redefChildNames returns the {kind, name} identities of the components an
// xs:redefine element redefines, as "kind name" strings.
func redefChildNames(ch *xmltree.Node) map[string]bool {
	out := map[string]bool{}
	for _, sub := range ch.Children {
		if sub.Kind != xmltree.KindElement || sub.Name.Space != xsNS {
			continue
		}
		switch sub.Name.Local {
		case "simpleType", "complexType", "group", "attributeGroup":
			if n, ok := sub.AttrLocal("name"); ok {
				out[sub.Name.Local+" "+applyWhiteSpace("collapse", n)] = true
			}
		}
	}
	return out
}

// directiveTnsOK checks the namespace-consistency rules between an
// include/redefine/override/import directive and the targetNamespace of the
// document it resolves to — also when that document was already collected.
func directiveTnsOK(ch *xmltree.Node, ownTns, subTns, loc string) error {
	switch ch.Name.Local {
	case "include", "redefine", "override":
		// The referenced schema must share this document's target namespace
		// or have none (a "chameleon" inclusion).
		if subTns != "" && subTns != ownTns {
			return invalidf("", "included schema namespace %q does not match %q", subTns, ownTns)
		}
	case "import":
		// The import's @namespace must equal the imported document's target
		// namespace (both absent means importing no-namespace components).
		impNs, _ := ch.AttrLocal("namespace")
		if subTns != impNs {
			return invalidf("", "imported schema namespace %q does not match import namespace %q", subTns, impNs)
		}
	}
	return nil
}
