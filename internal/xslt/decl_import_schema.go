package xslt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
	"github.com/tim-riep/go-xslt/internal/xsd"
)

// xsl:import-schema (XSLT 3.0 §3.16) — the STATIC half of schema-awareness:
// the schema components a stylesheet names must exist before any source
// document does, so its declarations are gathered across every module and
// compiled ONCE, before a single expression is parsed. Nothing here validates
// anything; it makes myns:Foo resolvable, which is what @as, a match pattern
// and an element(N,T) node test each need in order to mean anything at all.
//
// ONE SCHEMA PER STYLESHEET, not one per namespace: the spec defines the
// effect in terms of a single "synthetic schema document" whose xs:import
// elements correspond one-for-one with the declarations, and internal/xsd
// answers derivation questions by comparing component POINTERS, so two
// separate compiles of related namespaces would produce components that
// cannot see each other's base types.
//
// STORAGE / CONCURRENCY NOTE
// --------------------------
// Same shape, and the same reason, as instr_accumulator.go's acc2Registry:
// the INSTRAPI contract keeps new state out of the Stylesheet struct, so the
// compiled schema lives in a package-level sync.Map. It is keyed by each
// stylesheet MODULE's own document node rather than by the *Stylesheet
// because the two places that must reach it late — requireExpr, compiling an
// expression written on some element, and asTypeCtx, parsing an @as string at
// evaluation time — are both handed an element and nothing else. Every module
// of one stylesheet maps to the same schema (§3.16: importing components in
// one module makes them available throughout the package).
//
// anySchemaImported keeps the lookup free for the overwhelmingly common case:
// no xsl:import-schema anywhere means no entry was ever written, so the
// ancestor walk and the map read are both skipped outright.
var (
	schemaRegistry    sync.Map // map[*xmltree.Node]*importedSchema — module document node -> compiled schema
	anySchemaImported atomic.Bool
)

// importedSchema is one stylesheet's in-scope schema components.
type importedSchema struct {
	sch *xsd.Schema
}

func init() {
	declRegistry["import-schema"] = func(c *compiler, ss *Stylesheet, el *xmltree.Node) error {
		if !schemaAwareRun() {
			// Unchanged basic-processor behaviour, word for word: the 31
			// conformance cases written for a processor WITHOUT schema
			// awareness assert exactly this rejection.
			return errAt(el, "xsl:%s is an optional feature (packages/schema-awareness) not supported by this processor", el.Name.Local)
		}
		// The declaration itself was already gathered, checked and compiled by
		// compileImportedSchemas, which must run before any expression is
		// parsed; reaching it again here is just the top-level walk passing by.
		return nil
	}
}

// compileImportedSchemas gathers every xsl:import-schema declaration across
// all modules and compiles the referenced schema documents into one
// xsd.Schema, registered for every module of the stylesheet. It must run
// after module gathering and BEFORE compileTopLevel: a type name is resolved
// where its expression is parsed, so the components have to be in place first.
//
// A no-op unless the run claims schema-awareness, which is what keeps the
// whole feature inert at the default setting.
func (c *compiler) compileImportedSchemas(ss *Stylesheet, mods []modChildren) error {
	if !schemaAwareRun() {
		return nil
	}
	type isDecl struct {
		src, base string
		prec      int
		el        *xmltree.Node
	}
	// §3.16: when two declarations name the SAME namespace, only the one(s) at
	// the highest import precedence are used. Collect per namespace first so a
	// module that deliberately overrides an imported module's schema does not
	// hand both documents to one compile and collide with itself.
	byNS := map[string][]isDecl{}
	var nsOrder []string      // first-seen order — map iteration is randomized, and docs[0] is the compile's entry document
	var firstEl *xmltree.Node // for the position of a whole-schema error
	for prec, mod := range mods {
		for _, el := range mod.children {
			if el.Name.Space != NS || el.Name.Local != "import-schema" {
				continue
			}
			if firstEl == nil {
				firstEl = el
			}
			src, base, err := c.importSchemaSource(el, mod.baseDir)
			if err != nil {
				return err
			}
			if src == "" {
				// §3.16 lets a processor locate the components for a
				// namespace-only declaration "in any way described by [XML
				// Schema Part 1]", including "implicitly from knowledge of the
				// namespace". The XML namespace is the one this processor
				// genuinely has such knowledge of — internal/xsd already
				// answers xml:base/lang/space/id references from a built-in
				// catalogue (builtinXMLAttr) — so an import naming it supplies
				// those four global attribute declarations rather than
				// nothing. attribute-1501/1505 are exactly this shape, and the
				// suite's own note on them ("Import schema for XML namespace.
				// It isn't built in automatically.") says the declaration is
				// what is meant to make them available.
				if importSchemaNamespace(el) == xmlNamespaceURI {
					if _, seen := byNS[xmlNamespaceURI]; !seen {
						nsOrder = append(nsOrder, xmlNamespaceURI)
					}
					byNS[xmlNamespaceURI] = append(byNS[xmlNamespaceURI],
						isDecl{src: xmlNamespaceSchema, base: base, prec: prec, el: el})
					continue
				}
				// Any other namespace-only declaration is a hint with no
				// document behind it; §3.16 is explicit that locating no
				// schema document is not in itself an error — it only bites
				// if the stylesheet then names a component that was never
				// imported, which resolution reports on its own.
				continue
			}
			ns := importSchemaNamespace(el)
			if _, seen := byNS[ns]; !seen {
				nsOrder = append(nsOrder, ns)
			}
			byNS[ns] = append(byNS[ns], isDecl{src: src, base: base, prec: prec, el: el})
		}
	}
	var (
		docs    []string
		baseFor string
	)
	for _, ns := range nsOrder {
		decls := byNS[ns]
		top := decls[0].prec
		for _, d := range decls[1:] {
			if d.prec > top {
				top = d.prec
			}
		}
		for _, d := range decls {
			if d.prec != top {
				continue
			}
			docs = append(docs, d.src)
			if baseFor == "" {
				baseFor = d.base
			}
		}
	}
	if len(docs) == 0 {
		return nil
	}
	// XSD 1.1 is the version this engine presents everywhere else (see the
	// conformance harness's own XSD_1.1 claim), so schema components imported
	// into a stylesheet are read under the same rules.
	sch, err := xsd.Compile(docs, baseFor, xsd.Version11)
	if err != nil {
		if errors.Is(err, xsd.ErrUnsupported) {
			// Not an XTSE0220: the schema is not being called invalid, this
			// processor simply cannot read it. Keeping the two apart is the
			// same honesty discipline internal/xsd applies to its own
			// pass-rate — and refusing to compile is the fail-closed answer,
			// since carrying on would silently resolve nothing.
			return errAt(firstEl, "xsl:import-schema: this processor cannot compile the imported schema: %v", err)
		}
		// XTSE0220: the synthetic schema document must satisfy XML Schema's
		// own structural constraints. Whatever internal/xsd rejected is the
		// reason, carried through rather than flattened to a bare code.
		return errAt(firstEl, "err:XTSE0220: the imported schema is not valid: %v", err)
	}
	is := &importedSchema{sch: sch}
	// Also recorded on the Stylesheet itself, for the one consumer that has no
	// stylesheet ELEMENT to resolve through: validating the SOURCE document
	// (validateSourceDocument), which happens before any instruction runs.
	ss.schema = is
	// Register the schema against the root of EVERY document a module's
	// top-level children come from, not just the first child's.
	//
	// A module's children are not necessarily one document's: loader.go
	// SPLICES an xsl:include's top-level declarations into the including
	// module's own child list, so when xsl:include is written first, children
	// [0] belongs to the INCLUDED file and registering only that root leaves
	// the principal module — every expression in it — with no schema in scope
	// (import-schema-171/172/174). xsl:import is unaffected, each imported
	// module keeping its own entry, which is why this only ever showed up for
	// include.
	seen := map[*xmltree.Node]bool{}
	for _, mod := range mods {
		for _, ch := range mod.children {
			if ch.Parent == nil {
				continue
			}
			r := rootOfNode(ch.Parent)
			if r == nil || seen[r] {
				continue
			}
			seen[r] = true
			schemaRegistry.Store(r, is)
		}
	}
	anySchemaImported.Store(true)
	return nil
}

// inlineWithInScopeNS returns the inline xs:schema element with every prefix
// that is in scope on it materialized as its own namespace node, so that
// serializing the subtree cannot lose a binding only an ANCESTOR declared.
//
// The element is copied shallowly (children and attributes are shared, not
// cloned): the result is fed straight to the serializer and discarded, and
// nothing about the stylesheet's own tree may change.
func inlineWithInScopeNS(inline *xmltree.Node) *xmltree.Node {
	scope := inline.InScopeNamespaces()
	if len(scope) == 0 {
		return inline
	}
	cp := *inline
	cp.NS = append([]*xmltree.Node(nil), inline.NS...)
	have := map[string]bool{}
	for _, ns := range cp.NS {
		have[ns.Name.Local] = true
	}
	for pfx, uri := range scope {
		if have[pfx] {
			continue
		}
		cp.NS = append(cp.NS, &xmltree.Node{Kind: xmltree.KindNamespace,
			Name: xmltree.Name{Local: pfx}, Value: uri, Parent: &cp})
	}
	return &cp
}

// importSchemaNamespace is the namespace one declaration imports: @namespace
// when written, else the target namespace an inline xs:schema declares, else
// the no-namespace case (""). It is the key §3.16's import-precedence rule
// groups declarations by.
func importSchemaNamespace(el *xmltree.Node) string {
	if ns, ok := el.AttrLocal("namespace"); ok {
		return strings.TrimSpace(ns)
	}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space == xsdNS && ch.Name.Local == "schema" {
			if tns, ok := ch.AttrLocal("targetNamespace"); ok {
				return strings.TrimSpace(tns)
			}
		}
	}
	return ""
}

// importSchemaSource resolves ONE xsl:import-schema declaration to the text of
// the schema document it contributes (empty when it names no document) and
// the directory that document's own relative xs:include/xs:import hrefs
// resolve against.
func (c *compiler) importSchemaSource(el *xmltree.Node, baseDir string) (src, base string, err error) {
	loc, hasLoc := el.AttrLocal("schema-location")
	ns, hasNS := el.AttrLocal("namespace")
	if hasNS && strings.TrimSpace(ns) == "" {
		// "The zero-length string is not a valid namespace URI, and is
		// therefore not a valid value for the namespace attribute."
		return "", "", errAt(el, "err:XTSE0020: xsl:import-schema/@namespace must not be a zero-length string")
	}
	// The declaration's content model is xs:schema? — anything else in it is
	// not part of the grammar at all.
	var inline *xmltree.Node
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != xsdNS || ch.Name.Local != "schema" || inline != nil {
			return "", "", errAt(ch, "err:XTSE0010: only a single xs:schema element is allowed inside xsl:import-schema")
		}
		inline = ch
	}
	// Relative references resolve against the DECLARATION's own base URI,
	// which differs from the module's when the element arrived through an
	// external entity (the rule xsl:import/@href follows — see gatherModules).
	dir := baseDir
	if own := el.Base; own == "" {
		own = el.EntityBase
		if own != "" {
			dir = filepath.Dir(strings.TrimPrefix(own, "file://"))
		}
	} else {
		dir = filepath.Dir(strings.TrimPrefix(own, "file://"))
	}
	// §3.16: "The base URI of the xs:import element is the same as the base
	// URI of the xsl:import-schema declaration" — so an xml:base written on
	// the declaration (or inherited from an ancestor) moves both the
	// @schema-location lookup and an INLINE schema's own xs:import/
	// xs:include resolution, which is exactly what import-schema-081 tests
	// ("Test use of base URI in an inline schema": xml:base="dir/" with a
	// nested xs:import of schema091a.xsd that only exists under dir/).
	dir = embeddedBaseDir(el, dir)
	if inline != nil {
		// XTSE0215, both halves: an inline schema rules out @schema-location
		// outright, and @namespace may only repeat the inline schema's own
		// target namespace (or be absent, in which case the inline one wins).
		if hasLoc {
			return "", "", errAt(el, "err:XTSE0215: xsl:import-schema with an inline xs:schema must not have a schema-location attribute")
		}
		tns, hasTNS := inline.AttrLocal("targetNamespace")
		if hasNS && (!hasTNS || strings.TrimSpace(tns) != strings.TrimSpace(ns)) {
			return "", "", errAt(el, "err:XTSE0215: the namespace attribute conflicts with the target namespace of the inline schema")
		}
		// Serializing the subtree materializes the namespace declarations it
		// only INHERITS in the stylesheet — the suite's usual shape binds the
		// xs prefix on the stylesheet root, so the extracted document would
		// otherwise name nothing.
		//
		// That materialization is driven by the NAMES actually used, which
		// covers element and attribute names but NOT the QNames that live in
		// attribute VALUES — xs:element/@type, @base, @ref, @itemType,
		// @memberTypes. A schema whose own prefix (xmlns:foo) is declared on
		// the stylesheet root rather than on xs:schema therefore serializes
		// with "foo" unbound and every foo:-qualified type reference becomes
		// unresolvable (import-schema-180/188). Copying the whole in-scope set
		// onto the extracted root first is the same technique the validator's
		// own detached-tree paths use (xsd's cloneForValidation, assertTree).
		return xmltree.Serialize(inlineWithInScopeNS(inline),
			xmltree.SerializeOptions{Method: "xml", OmitXMLDeclaration: true}), dir, nil
	}
	loc = strings.TrimSpace(loc)
	if !hasLoc || loc == "" {
		return c.hostSchemaFor(ns)
	}
	path := loc
	if !filepath.IsAbs(path) && dir != "" {
		path = filepath.Join(dir, path)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		// Not an error in itself (§3.16): a hint that leads nowhere simply
		// contributes no components — but the HOST may still have a schema
		// for this namespace, which §3.16 explicitly allows a processor to
		// use in preference to the hint.
		return c.hostSchemaFor(ns)
	}
	// XSD 1.0 Part 1 §4.2.3 (src-import), which XTSE0220 imports wholesale
	// (§3.16: "a static error if the synthetic schema document does not
	// satisfy the constraints described in [XML Schema Part 1] section 5.1"):
	// an xs:import's @namespace must equal the imported document's own
	// @targetNamespace, and an import with NO @namespace may only reach a
	// document with no target namespace. Checked only for a document the
	// declaration's own @schema-location actually resolved — a host-supplied
	// one (hostSchemaFor) is already matched on its namespace by construction.
	if tns := schemaTargetNamespace(string(data)); strings.TrimSpace(tns) != strings.TrimSpace(ns) {
		// @schema-location is only a HINT (§3.16: the processor "is not
		// required to use" it, and may locate the components "in any way
		// described by [XML Schema Part 1]"), so a hint pointing at the wrong
		// vocabulary yields to a schema the HOST supplies for the declared
		// namespace before it becomes an error — import-schema-186/187 point
		// at testSchemaInline.xsd while the test environment supplies the
		// real schema002.xsd for the same declaration.
		if hsrc, hbase, herr := c.hostSchemaFor(ns); herr == nil && hsrc != "" {
			return hsrc, hbase, nil
		}
		return "", "", errAt(el, "err:XTSE0220: xsl:import-schema names namespace %q but %s has target namespace %q",
			strings.TrimSpace(ns), loc, strings.TrimSpace(tns))
	}
	return string(data), filepath.Dir(path), nil
}

// hostSchemaFor returns the host-offered schema document for namespace ns, if
// the host offered one (see SchemaSource). A source that does not state its
// namespace is matched on the document's OWN @targetNamespace, which is how
// the W3C catalog supplies <schema role="stylesheet-import"/> entries; an
// import with no @namespace matches a no-target-namespace schema.
func (c *compiler) hostSchemaFor(ns string) (src, base string, err error) {
	ns = strings.TrimSpace(ns)
	for _, hs := range c.hostSchemas {
		if hs.Path == "" {
			continue
		}
		if hs.Namespace != "" {
			if hs.Namespace != ns {
				continue
			}
			data, rerr := os.ReadFile(hs.Path)
			if rerr != nil {
				continue
			}
			return string(data), filepath.Dir(hs.Path), nil
		}
		data, rerr := os.ReadFile(hs.Path)
		if rerr != nil {
			continue
		}
		if schemaTargetNamespace(string(data)) != ns {
			continue
		}
		return string(data), filepath.Dir(hs.Path), nil
	}
	return "", "", nil
}

// schemaTargetNamespace reads the @targetNamespace of a schema document's root
// element, or "" for a no-namespace schema (or anything unparsable, which then
// simply matches no import).
func schemaTargetNamespace(doc string) string {
	root, err := xmltree.Parse(doc)
	if err != nil {
		return ""
	}
	el := xmltree.RootElement(root)
	if el == nil {
		return ""
	}
	tns, _ := el.AttrLocal("targetNamespace")
	return strings.TrimSpace(tns)
}

// schemaForElement returns the schema components in scope for an expression
// written on el, or nil when the stylesheet imported none.
func schemaForElement(el *xmltree.Node) *importedSchema {
	if el == nil || !anySchemaImported.Load() {
		return nil
	}
	v, ok := schemaRegistry.Load(rootOfNode(el))
	if !ok {
		return nil
	}
	return v.(*importedSchema)
}

// schemaAdapterFor builds the object handed to internal/xpath for an
// expression written on el: ONE value that answers both the derivation
// question the SchemaTypeResolver seam asks at match time and the name
// resolution a parse needs. nil when no schema is in scope, which is the
// correct "no derivation information" default.
func schemaAdapterFor(el *xmltree.Node) *schemaAdapter {
	is := schemaForElement(el)
	if is == nil {
		return nil
	}
	return &schemaAdapter{sch: is.sch, el: el}
}

// schemaLookupFor is schemaAdapterFor typed for the parse-time interface. It
// must return a nil INTERFACE when there is no schema, not a typed nil
// pointer, or every parse would take the schema-aware path.
func schemaLookupFor(el *xmltree.Node) xpath.SchemaNameLookup {
	if a := schemaAdapterFor(el); a != nil {
		return a
	}
	return nil
}

// schemaTypesFor is the same value as xpath.Context.SchemaTypes, with the same
// typed-nil caution.
func schemaTypesFor(el *xmltree.Node) xpath.SchemaTypeResolver {
	if a := schemaAdapterFor(el); a != nil {
		return a
	}
	return nil
}

// parseXPathFor compiles an XPath expression WRITTEN ON el, so any schema
// component name in it resolves against the stylesheet's imported components
// and el's own namespace bindings. With no schema in scope it is exactly
// xpath.Parse, which is what every non-schema-aware compile gets.
func parseXPathFor(el *xmltree.Node, src string) (*xpath.Parsed, error) {
	return xpath.ParseSchemaAware(src, schemaLookupFor(el))
}

// parsePatternFor is parseXPathFor for an XSLT match pattern.
func parsePatternFor(el *xmltree.Node, src string) (*xpath.Pattern, error) {
	return xpath.ParsePatternSchemaAware(src, schemaLookupFor(el))
}

// schemaAdapter bridges the stylesheet's compiled schema to internal/xpath: it
// implements xpath.SchemaTypeResolver (derivation, asked while matching) and
// xpath.SchemaNameLookup (name resolution, asked while parsing). Prefix
// resolution stays on this side — el carries the in-scope namespace bindings
// of the stylesheet element the expression was written on, which internal/
// xpath has no business knowing about.
type schemaAdapter struct {
	sch *xsd.Schema
	el  *xmltree.Node
}

func (a *schemaAdapter) DerivesFrom(got, want xmltree.SchemaTypeName) bool {
	return a.sch.DerivesFrom(got, want)
}

// SubstitutesFor implements xpath.SchemaSubstitutionResolver: it answers
// schema-element(head)'s name condition, which a substitution group makes
// wider than plain name equality.
func (a *schemaAdapter) SubstitutesFor(member, head xmltree.Name) bool {
	return a.sch.SubstitutesFor(member, head)
}

// ElementDeclType implements xpath.SchemaDeclTypeResolver: the type declared
// by the top-level element declaration a CANDIDATE node's own name denotes,
// which is the ET in XPath 3.1 §2.5.5.4's derives-from condition for
// schema-element(). See the seam's own doc comment.
func (a *schemaAdapter) ElementDeclType(name xmltree.Name) (xmltree.SchemaTypeName, bool) {
	return a.sch.ElementDeclared(name.Space, name.Local)
}

// CastToSchemaType implements xpath.SchemaTypeCaster: `cast as`/`castable as`
// and the constructor function of an imported simple type.
func (a *schemaAdapter) CastToSchemaTypeIn(t xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (xpath.AtomType, error) {
	return a.sch.CastToSchemaTypeIn(t, lexical, at)
}

func (a *schemaAdapter) CastToSchemaType(t xmltree.SchemaTypeName, lexical string) (xpath.AtomType, error) {
	return a.sch.CastToSchemaType(t, lexical)
}

// ValidateSchemaDocument implements xpath.SchemaDocumentValidator: the
// validation episode fn:json-to-xml's `validate` option asks for.
//
// STRICT, because F&O 3.1 §17.5.1 names the schema the result is validated
// against rather than leaving the outcome open — a tree whose document element
// has no declaration in scope has not been validated against it, and saying so
// is what lets the caller raise FOJS0004 rather than hand back an untyped tree
// under a typed contract. Document:true because the node handed over IS a
// document node, so the document-level ID/IDREF constraints apply (the same
// distinction instr_copy makes per copied item). LenientEntities for the
// reason NodeValidateOptions documents: an XSLT host compiles every imported
// schema as XSD 1.1 with no way to tell a 1.0 document apart.
func (a *schemaAdapter) ValidateSchemaDocument(doc *xmltree.Node) error {
	if a.sch == nil {
		return errors.New("no schema components are in scope")
	}
	return a.sch.ValidateNode(doc, xsd.NodeValidateOptions{
		Strict: true, Document: true, LenientEntities: true,
	})
}

// CastToSchemaListType implements xpath.SchemaListTypeCaster: a LIST simple
// type as a cast target, whose result is a sequence rather than one value.
func (a *schemaAdapter) CastToSchemaListType(t xmltree.SchemaTypeName, lexical string, at *xmltree.Node) (
	xpath.AtomType, *xmltree.SchemaTypeName, bool, error) {
	return a.sch.CastToSchemaListType(t, lexical, at)
}

func (a *schemaAdapter) LookupSchemaType(lexical string) (xmltree.SchemaTypeName, bool) {
	ns, local, ok := a.expand(lexical)
	if !ok {
		return xmltree.SchemaTypeName{}, false
	}
	return a.sch.TypeByName(ns, local)
}

func (a *schemaAdapter) LookupSchemaElement(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool) {
	ns, local, ok := a.expand(lexical)
	if !ok {
		return xmltree.Name{}, xmltree.SchemaTypeName{}, false
	}
	t, found := a.sch.ElementDeclared(ns, local)
	return xmltree.Name{Space: ns, Local: local}, t, found
}

func (a *schemaAdapter) LookupSchemaAttribute(lexical string) (xmltree.Name, xmltree.SchemaTypeName, bool) {
	ns, local, ok := a.expand(lexical)
	if !ok {
		return xmltree.Name{}, xmltree.SchemaTypeName{}, false
	}
	t, found := a.sch.AttributeDeclared(ns, local)
	return xmltree.Name{Space: ns, Local: local}, t, found
}

// expand turns a lexical EQName into an expanded one. An UNPREFIXED name takes
// the default element/type namespace ([xsl:]xpath-default-namespace), which is
// the same namespace an unprefixed element name in a node test takes; an
// unbound prefix names nothing at all rather than silently landing in no
// namespace.
func (a *schemaAdapter) expand(lexical string) (ns, local string, ok bool) {
	lexical = strings.TrimSpace(lexical)
	if strings.HasPrefix(lexical, "Q{") {
		if end := strings.IndexByte(lexical, '}'); end > 0 {
			return strings.Join(strings.Fields(lexical[2:end]), " "), lexical[end+1:], true
		}
		return "", "", false
	}
	if a.el == nil {
		// No namespace context at all: only a braced name can be expanded.
		return "", "", false
	}
	if i := strings.IndexByte(lexical, ':'); i > 0 {
		uri, found := a.el.LookupPrefix(lexical[:i])
		if !found {
			return "", "", false
		}
		return uri, lexical[i+1:], true
	}
	return xpathDefaultNS(a.el), lexical, true
}

// xsdNS is the XML Schema namespace, for recognising an inline xs:schema.
const xsdNS = "http://www.w3.org/2001/XMLSchema"

// xmlNamespaceURI is the one namespace whose components this processor can
// supply from its own knowledge when an xsl:import-schema names it without a
// schema-location — see compileImportedSchemas.
const xmlNamespaceURI = "http://www.w3.org/XML/1998/namespace"

// xmlNamespaceSchema declares the four global attributes of the XML namespace,
// following the W3C's own xml.xsd rather than the looser modelling internal/
// xsd's builtinXMLAttr uses for by-ref lookups.
//
// The difference is not cosmetic here, and the suite proves it: builtinXMLAttr
// types xml:lang and xml:space as plain strings on the explicit grounds that
// their "finer lexical/enumeration constraints do not affect validity
// decisions we make" — true where it is used, false here, since
// attribute-1502 requires xml:lang="!@$%^*" to be INVALID (while
// attribute-1501 requires the empty string to be valid: hence the union of
// xs:language with an empty enumeration, exactly as xml.xsd writes it) and
// attribute-1503 requires xml:space="foo-bar" to be invalid.
const xmlNamespaceSchema = `<xs:schema xmlns:xs="` + xsdNS + `" targetNamespace="` + xmlNamespaceURI + `">
  <xs:attribute name="lang">
    <xs:simpleType>
      <xs:union memberTypes="xs:language">
        <xs:simpleType>
          <xs:restriction base="xs:string">
            <xs:enumeration value=""/>
          </xs:restriction>
        </xs:simpleType>
      </xs:union>
    </xs:simpleType>
  </xs:attribute>
  <xs:attribute name="space">
    <xs:simpleType>
      <xs:restriction base="xs:NCName">
        <xs:enumeration value="default"/>
        <xs:enumeration value="preserve"/>
      </xs:restriction>
    </xs:simpleType>
  </xs:attribute>
  <xs:attribute name="base" type="xs:anyURI"/>
  <xs:attribute name="id" type="xs:ID"/>
</xs:schema>`

// SchemaComponentsForHost returns the stylesheet's imported schema components
// as the two hooks internal/xpath consumes: a xpath.SchemaNameLookup (for
// ParseSchemaAware) and a xpath.SchemaTypeResolver (for Context.SchemaTypes).
// Both are nil when the stylesheet imported no schema, or when the run is not
// schema-aware — in which case a caller simply parses and evaluates the plain,
// non-schema-aware way, exactly as before.
//
// It exists for a HOST that must parse an expression written against the
// stylesheet's schema but NOT written on any stylesheet element: the W3C
// conformance harness's own <assert> expressions, which name declarations as
// schema-element(Q{...}name) and cannot be PARSED at all without the
// components (schema-element() is XPST0008 with no schema in scope), however
// correctly the transformation itself ran.
//
// Only BRACED (Q{uri}local) names resolve through it, since there is no
// stylesheet element to resolve a prefix against — which is exactly the form
// those assertions are written in.
func (ss *Stylesheet) SchemaComponentsForHost() (xpath.SchemaNameLookup, xpath.SchemaTypeResolver) {
	if ss == nil || ss.schema == nil || ss.schema.sch == nil || !schemaAwareRun() {
		return nil, nil
	}
	a := &schemaAdapter{sch: ss.schema.sch}
	return a, a
}
