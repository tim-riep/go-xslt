package xsd

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Compile parses and assembles one or more schema documents into a Schema,
// resolving xs:include / xs:import / xs:redefine / xs:override against baseDir.
// A handful of not-yet-handled constructs yield ErrUnsupported; anything
// genuinely malformed yields a normal invalid error.
func Compile(docs []string, baseDir string, v Version) (*Schema, error) {
	if len(docs) == 0 {
		return nil, ErrUnsupported
	}
	root, err := xmltree.ParseLenient11(docs[0])
	if err != nil {
		return nil, invalidf("", "schema not well-formed: %v", err)
	}
	primary := xmltree.RootElement(root)
	if primary == nil || primary.Name.Space != xsNS || primary.Name.Local != "schema" {
		return nil, invalidf("", "document element is not xs:schema")
	}

	sch := &Schema{
		version:      v,
		baseDir:      baseDir,
		sourceDocs:   append([]string{}, docs...),
		elements:     map[xname]*ElementDecl{},
		types:        map[xname]Type{},
		attributes:   map[xname]*AttributeDecl{},
		substMembers: map[xname][]*ElementDecl{},
		anon:         &anonReg{byType: map[Type]xname{}, byName: map[xname]Type{}},
	}
	c := &compiler{
		sch:        sch,
		groups:     map[xname]*modelGroup{},
		attrGroups: map[xname]*attrGroupDef{},
	}
	seedXSIAttributes(sch)

	// Resolve the include/import graph into a flat list of documents. Seed the
	// visited set with the primary's content so a cyclic import that resolves
	// back to it is not collected twice.
	var collected []*schemaDoc
	visited := map[string]bool{"c:" + docs[0]: true}
	if err := c.collect(primary, baseDir, "", &collected, visited, ""); err != nil {
		return nil, err
	}
	// Every supplied document contributes components, not just the entry one: a
	// test set may hand us several schema documents that reference each other by
	// namespace alone, with no schemaLocation for collect to follow. Documents
	// already reached through the include/import graph are skipped — collecting
	// one a second time standalone would lose the namespace it adopted there.
	for _, d := range docs[1:] {
		if visited["c:"+d] || visited["overridden:"+d] {
			continue
		}
		visited["c:"+d] = true
		extra, err := xmltree.ParseLenient11(d)
		if err != nil {
			// A supplementary document we cannot read (the suite has UTF-16 ones,
			// which our parser does not decode) simply contributes no components —
			// the same position we were in before reading it at all.
			continue
		}
		el := xmltree.RootElement(extra)
		if el == nil || el.Name.Space != xsNS || el.Name.Local != "schema" {
			continue
		}
		if err := c.collect(el, baseDir, "", &collected, visited, ""); err != nil {
			return nil, err
		}
	}
	for _, sd := range collected {
		if err := validateAttrValues(sd.el, map[string]bool{}, sch.version); err != nil {
			return nil, err
		}
		if err := validateSchemaAttrs(sd.el, sch.version); err != nil {
			return nil, err
		}
	}

	// Pass 1: pre-register named top-level components across all documents.
	var simpleW, complexW, elemW, attrW, groupW, agW []work
	notationNames := map[xname]bool{}
	for _, sd := range collected {
		for _, ch := range sd.el.Children {
			if ch.Kind != xmltree.KindElement {
				continue
			}
			if ch.Name.Space != xsNS {
				return nil, ErrUnsupported
			}
			named := func() (xname, error) {
				name := declName(ch)
				if !isNCName(name) {
					return xname{}, invalidf("", "invalid xs:%s name %q", ch.Name.Local, name)
				}
				return xname{sd.tns, name}, nil
			}
			switch ch.Name.Local {
			case "annotation", "include", "import", "redefine", "override":
				// resolved during collect / not modelled
			case "notation":
				nm := declName(ch)
				if !isNCName(nm) {
					return nil, invalidf("", "xs:notation requires a valid name")
				}
				_, hasPub := ch.AttrLocal("public")
				_, hasSys := ch.AttrLocal("system")
				if !hasPub && !hasSys {
					return nil, invalidf("", "xs:notation %q requires public or system", nm)
				}
				nk := xname{sd.tns, nm}
				if notationNames[nk] {
					return nil, invalidf("", "duplicate notation %s", nk)
				}
				notationNames[nk] = true
				if c.notationLocals == nil {
					c.notationLocals = map[string]bool{}
				}
				c.notationLocals[nk.Local] = true
			case "simpleType", "complexType":
				k, err := named()
				if err != nil {
					return nil, err
				}
				if _, dup := sch.types[k]; dup {
					return nil, invalidf("", "duplicate type definition %s", k)
				}
				if ch.Name.Local == "simpleType" {
					sch.types[k] = &SimpleType{name: k}
					simpleW = append(simpleW, work{sd, ch})
				} else {
					sch.types[k] = &ComplexType{name: k}
					complexW = append(complexW, work{sd, ch})
				}
			case "element":
				k, err := named()
				if err != nil {
					return nil, err
				}
				if _, dup := sch.elements[k]; dup {
					return nil, invalidf("", "duplicate element declaration %s", k)
				}
				sch.elements[k] = &ElementDecl{name: k}
				elemW = append(elemW, work{sd, ch})
			case "attribute":
				k, err := named()
				if err != nil {
					return nil, err
				}
				if k.Space == xsiNS {
					// no-xsi: a top-level attribute in an xsi-targeted schema
					// declares into the XSI namespace (attKa015).
					return nil, invalidf("no-xsi", "an attribute declaration cannot target the XSI namespace")
				}
				if _, dup := sch.attributes[k]; dup {
					return nil, invalidf("", "duplicate attribute declaration %s", k)
				}
				sch.attributes[k] = &AttributeDecl{name: k}
				attrW = append(attrW, work{sd, ch})
			case "group":
				k, err := named()
				if err != nil {
					return nil, err
				}
				if _, dup := c.groups[k]; dup {
					return nil, invalidf("sch-props-correct.2", "duplicate group definition %s", k)
				}
				c.groups[k] = &modelGroup{}
				groupW = append(groupW, work{sd, ch})
			case "attributeGroup":
				k, err := named()
				if err != nil {
					return nil, err
				}
				if _, dup := c.attrGroups[k]; dup {
					return nil, invalidf("sch-props-correct.2", "duplicate attributeGroup definition %s", k)
				}
				c.attrGroups[k] = &attrGroupDef{}
				agW = append(agW, work{sd, ch})
			case "defaultOpenContent":
				// Per-document default open content (1.1); the S4S checks ran
				// in validateSchemaAttrs, the semantics apply per complexType.
				sd.defaultOpenNode = ch
			default:
				return nil, ErrUnsupported
			}
		}
	}

	// Collect xs:redefine redefinition nodes. Group/attributeGroup redefinitions
	// are applied BEFORE complex-type bodies (which flatten attributeGroup/group
	// references by value, so they must see the redefined version); type
	// redefinitions are applied after, once the originals they reference are built.
	var groupRedefs, typeRedefs []work
	for _, sd := range collected {
		for _, rd := range sd.el.Children {
			if !isXS(rd, "redefine") {
				continue
			}
			for _, ch := range rd.Children {
				if ch.Kind == xmltree.KindElement && ch.Name.Space == xsNS {
					switch ch.Name.Local {
					case "group", "attributeGroup":
						groupRedefs = append(groupRedefs, work{sd, ch})
					case "simpleType", "complexType":
						typeRedefs = append(typeRedefs, work{sd, ch})
					}
				}
			}
		}
	}
	c.redefineType = map[xname]Type{}
	c.redefineGroup = map[xname]*modelGroup{}
	c.redefineAttrGroup = map[xname]*attrGroupDef{}

	// Pass 2+: parse bodies. Each work item carries its document's namespace
	// context, applied via setDoc before parsing. simpleTypes are compiled in
	// dependency order (a restriction base / list itemType / union memberType is
	// built before the type deriving from it) so the base chain — and thus
	// resolvePrim/facet-applicability — is complete when each type is validated.
	simpleByName := make(map[xname]work, len(simpleW))
	for _, w := range simpleW {
		name := declName(w.node)
		simpleByName[xname{w.sd.tns, name}] = w
	}
	simpleOrder := make([]work, 0, len(simpleW))
	simpleVisited := make(map[xname]int, len(simpleW))
	var stack []xname   // DFS path of type names currently being visited
	var edgeHard []bool // edgeHard[i]: is the edge stack[i] → stack[i+1] a hard (restriction/list) edge?
	hardCycle := false
	var visitST func(xname)
	visitST = func(n xname) {
		if simpleVisited[n] == 1 {
			// Back-edge to a type on the current path: a circular derivation. In
			// 1.1 every cycle is invalid (cos-no-circular-unions; simple017/020,
			// s3_16_2si01/05 — known −1 price: ste110.i, whose valid twin is
			// queried-excluded). In 1.0 it is invalid only if some edge around
			// the loop is hard (restriction/list); a pure-union cycle is
			// tolerated so the suite's stE110 stays accepted.
			if sch.version == Version11 {
				hardCycle = true
				return
			}
			hardInLoop := edgeHard[len(edgeHard)-1] // the edge that just closed the loop
			for i := len(stack) - 1; i >= 0 && !hardInLoop; i-- {
				if stack[i] == n {
					break
				}
				hardInLoop = edgeHard[i-1]
			}
			if hardInLoop {
				hardCycle = true
			}
			return
		}
		if simpleVisited[n] == 2 {
			return
		}
		simpleVisited[n] = 1
		stack = append(stack, n)
		if w, ok := simpleByName[n]; ok {
			for _, dep := range simpleTypeDeps(w.node) {
				edgeHard = append(edgeHard, dep.hard)
				visitST(dep.n)
				edgeHard = edgeHard[:len(edgeHard)-1]
			}
			simpleOrder = append(simpleOrder, w)
		}
		stack = stack[:len(stack)-1]
		simpleVisited[n] = 2
	}
	for _, w := range simpleW {
		name := declName(w.node)
		visitST(xname{w.sd.tns, name})
	}
	if hardCycle {
		return nil, invalidf("", "circular simpleType derivation")
	}
	for _, w := range simpleOrder {
		c.setDoc(w.sd)
		name := declName(w.node)
		st, ok := sch.types[xname{w.sd.tns, name}].(*SimpleType)
		if !ok {
			return nil, invalidf("", "conflicting definitions for type %s", name)
		}
		if err := c.parseSimpleTypeInto(st, w.node); err != nil {
			return nil, err
		}
	}
	for _, w := range attrW {
		c.setDoc(w.sd)
		if err := c.parseGlobalAttribute(w.node); err != nil {
			return nil, err
		}
	}
	for _, w := range groupW {
		c.setDoc(w.sd)
		name := declName(w.node)
		g, err := c.parseGroupBody(w.node)
		if err != nil {
			return nil, err
		}
		*c.groups[xname{w.sd.tns, name}] = *g
	}
	if err := c.checkGroupCycles(); err != nil {
		return nil, err
	}
	if err := checkAttrGroupCycles(collected, sch.version); err != nil {
		return nil, err
	}
	// Compile attributeGroups in dependency order: a group flattens the uses of
	// every attributeGroup it references by value, so each referenced group must be
	// built first. checkAttrGroupCycles has ruled out cycles, so a topological order
	// exists; the visited guard also degrades safely on any residual cycle.
	agByName := make(map[xname]work, len(agW))
	for _, w := range agW {
		name := declName(w.node)
		agByName[xname{w.sd.tns, name}] = w
	}
	agOrder := make([]work, 0, len(agW))
	agVisited := make(map[xname]int, len(agW))
	var visitAG func(xname)
	visitAG = func(n xname) {
		if agVisited[n] != 0 {
			return
		}
		agVisited[n] = 1
		if w, ok := agByName[n]; ok {
			for _, ch := range w.node.Children {
				if isXS(ch, "attributeGroup") {
					if ref, ok := ch.AttrLocal("ref"); ok {
						visitAG(resolveQName(ch, ref))
					}
				}
			}
			agOrder = append(agOrder, w)
		}
		agVisited[n] = 2
	}
	for _, w := range agW {
		name := declName(w.node)
		visitAG(xname{w.sd.tns, name})
	}
	for _, w := range agOrder {
		c.setDoc(w.sd)
		name := declName(w.node)
		ag, err := c.parseAttrGroupBody(w.node)
		if err != nil {
			return nil, err
		}
		*c.attrGroups[xname{w.sd.tns, name}] = *ag
	}
	// src-resolve for xs:schema/@defaultAttributes: the group must exist and be
	// reachable from the DECLARING document (open203/open204) — checked here so
	// it fires even when no complexType uses it.
	for _, sd := range collected {
		if !sd.hasDefaultAttrs {
			continue
		}
		if _, ok := c.attrGroups[sd.defaultAttrs]; !ok {
			return nil, invalidf("src-resolve", "defaultAttributes group %s does not resolve", sd.defaultAttrs)
		}
		if ns := sd.defaultAttrs.Space; ns != sd.tns && ns != "" && !sd.imports[ns] {
			return nil, invalidf("src-resolve", "defaultAttributes group %s is in a namespace this document does not import", sd.defaultAttrs)
		}
	}
	// Apply group/attributeGroup redefinitions now, so complex-type bodies below
	// pick up the redefined content when they flatten group references.
	if err := c.applyRedefinitions(groupRedefs); err != nil {
		return nil, err
	}
	// Compile complex types in dependency order: a simpleContent/complexContent
	// body reads its base's compiled content type while parsing (a restriction
	// declared BEFORE its base otherwise sees a half-built type and falls back
	// to anySimpleType — baseTD00101m). Cycles terminate via the emitted guard
	// and are rejected later by checkTypeCycles.
	ctIndex := make(map[xname]int, len(complexW))
	for i, w := range complexW {
		ctIndex[xname{w.sd.tns, declName(w.node)}] = i
	}
	ctBase := func(n *xmltree.Node) (xname, bool) {
		for _, kind := range []string{"simpleContent", "complexContent"} {
			if sc := firstXSChild(n, kind); sc != nil {
				for _, deriv := range []string{"restriction", "extension"} {
					if d := firstXSChild(sc, deriv); d != nil {
						if b, ok := d.AttrLocal("base"); ok {
							return resolveQName(d, b), true
						}
					}
				}
			}
		}
		return xname{}, false
	}
	ctOrder := make([]work, 0, len(complexW))
	emitted := make([]bool, len(complexW))
	var emitCT func(i int)
	emitCT = func(i int) {
		if emitted[i] {
			return
		}
		emitted[i] = true
		if b, ok := ctBase(complexW[i].node); ok {
			if j, ok2 := ctIndex[b]; ok2 && j != i {
				emitCT(j)
			}
		}
		ctOrder = append(ctOrder, complexW[i])
	}
	for i := range complexW {
		emitCT(i)
	}
	for _, w := range ctOrder {
		c.setDoc(w.sd)
		name := declName(w.node)
		ct, ok := sch.types[xname{w.sd.tns, name}].(*ComplexType)
		if !ok {
			return nil, invalidf("", "conflicting definitions for type %s", name)
		}
		if err := c.parseComplexTypeInto(ct, w.node); err != nil {
			return nil, err
		}
	}
	for _, w := range elemW {
		c.setDoc(w.sd)
		if err := c.parseGlobalElement(w.node); err != nil {
			return nil, err
		}
	}
	if err := c.applyRedefinitions(typeRedefs); err != nil {
		return nil, err
	}
	c.buildSubstitutionGroups()
	if err := c.checkSubstGroupCycles(); err != nil {
		return nil, err
	}
	c.inheritSubstGroupTypes()
	c.fillAllWildcardSiblings()
	if err := c.checkTypeCycles(); err != nil {
		return nil, err
	}
	if err := c.checkIDAttributeUses(); err != nil {
		return nil, err
	}
	if err := c.checkDuplicateConstraints(); err != nil {
		return nil, err
	}
	if err := c.checkAllRestrictions(); err != nil {
		return nil, err
	}
	if err := c.checkKeyrefs(); err != nil {
		return nil, err
	}
	if err := c.checkSubstitutionGroups(); err != nil {
		return nil, err
	}
	if err := c.checkFinalDerivation(); err != nil {
		return nil, err
	}
	if err := c.checkAttrDerivation(); err != nil {
		return nil, err
	}
	if err := c.checkAllUPA(); err != nil {
		return nil, err
	}
	// e-props-correct cl.5 (1.1): every type alternative's {type definition}
	// must be validly derived from the declaration's own (cta9008err,
	// s3_12si03s).
	if sch.version == Version11 {
		for _, e := range sch.elements {
			for _, alt := range e.alternatives {
				// xs:error is validly substitutable for EVERY type — its whole
				// point is the always-fail alternative (vc_007, s3_12ii05).
				if st, ok := alt.typ.(*SimpleType); ok && st == xsErrorType {
					continue
				}
				if alt.typ != nil && e.typ != nil && !derivesFrom(alt.typ, e.typ) {
					return nil, invalidf("e-props-correct.5",
						"a type alternative of %s is not derived from its declared type", e.name)
				}
			}
		}
	}
	return sch, nil
}

type compiler struct {
	sch                *Schema
	tns                string
	chameleonNS        string // adopted ns when the current doc is a chameleon include ("" otherwise)
	elementQualified   bool
	attributeQualified bool
	finalDefault       string
	blockDefault       string
	imports            map[string]bool
	curDoc             *schemaDoc            // the document currently being compiled (setDoc)
	allICs             []*identityConstraint // every identity constraint, for cross-checks
	groups             map[xname]*modelGroup
	attrGroups         map[xname]*attrGroupDef
	// redefine* hold the pre-redefine components: a reference to a redefined name
	// from INSIDE the xs:redefine block denotes the original, not the redefinition.
	redefineType      map[xname]Type
	redefineGroup     map[xname]*modelGroup
	redefineAttrGroup map[xname]*attrGroupDef
	// notationLocals holds the local names of every declared xs:notation, for
	// the NOTATION-enumeration-must-name-a-declared-notation rule.
	notationLocals map[string]bool
	// tnsByPath caches each schema document's targetNamespace by resolved path,
	// so an include/import directive reaching an already-collected document
	// still has its namespace-consistency rules checked (schF6).
	tnsByPath map[string]string
	// redefTargets records, per redefined document path, the {kind, name}
	// identities redefined INTO it (for the mutual-cycle discriminator).
	redefTargets map[string]map[string]bool
	// icRefs holds 1.1 identity-constraint @ref placeholders awaiting
	// resolution in checkKeyrefs.
	icRefs []*identityConstraint
	// redefineEdges records which document redefines which (by path), for the
	// indirect-cycle rule of src-redefine.2.
	redefineEdges map[string][]string
	// allCTs collects every complex type as it is parsed — named AND anonymous
	// (inline in element declarations) — so whole-schema checks like the
	// restriction validator see inline derivations too.
	allCTs []*ComplexType
}

// work is a pending top-level component to compile: its declaration node plus the
// document (namespace context) it came from.
type work struct {
	sd   *schemaDoc
	node *xmltree.Node
}

// applyRedefinitions installs xs:redefine redefinitions over the fully-compiled
// originals. All originals are stashed first (so a self-reference resolves to the
// pre-redefine component), then each redefinition is compiled into a fresh slot
// under the same name.
// checkRedefineSyntax enforces the src-redefine rules that constrain the SHAPE
// of an xs:redefine: no name may be redefined twice in the same element, and a
// redefined group/attributeGroup may refer to itself at most once, with
// minOccurs = maxOccurs = 1 (the self-reference stands for the original, which
// cannot be repeated or made optional).
func (c *compiler) checkRedefineSyntax(redefs []work) error {
	seen := map[*xmltree.Node]map[string]bool{}
	for _, w := range redefs {
		name := declName(w.node)
		kind := w.node.Name.Local
		parent := w.node.Parent
		if seen[parent] == nil {
			seen[parent] = map[string]bool{}
		}
		if seen[parent][kind+" "+name] {
			return invalidf("src-redefine.6.1.1", "%s %q is redefined twice in one xs:redefine", kind, name)
		}
		seen[parent][kind+" "+name] = true

		if kind != "group" && kind != "attributeGroup" {
			continue
		}
		self := xname{w.sd.tns, name}
		var refs []*xmltree.Node
		var walk func(n *xmltree.Node)
		walk = func(n *xmltree.Node) {
			for _, ch := range n.Children {
				if ch.Kind != xmltree.KindElement {
					continue
				}
				if isXS(ch, kind) {
					if ref, ok := ch.AttrLocal("ref"); ok && resolveQName(ch, ref) == self {
						refs = append(refs, ch)
					}
				}
				walk(ch)
			}
		}
		walk(w.node)
		if len(refs) > 1 {
			return invalidf("src-redefine.6.1.1", "redefined %s %q refers to itself more than once", kind, name)
		}
		for _, r := range refs {
			min, hasMin := r.AttrLocal("minOccurs")
			max, hasMax := r.AttrLocal("maxOccurs")
			if (hasMin && min != "1") || (hasMax && max != "1") {
				return invalidf("src-redefine.6.1.2", "the self-reference in redefined %s %q must have minOccurs = maxOccurs = 1", kind, name)
			}
		}
	}
	return nil
}

func (c *compiler) applyRedefinitions(redefs []work) error {
	// Snapshot every original first (so a self-reference resolves to the
	// pre-redefine component even when redefinitions cross-reference). A
	// redefinition whose original is not found (e.g. a chameleon namespace case we
	// do not port) is skipped rather than treated as an error, to avoid rejecting a
	// valid schema.
	if err := c.checkRedefineSyntax(redefs); err != nil {
		return err
	}
	// Apply INNERMOST-first: `collected` lists documents outermost-first (a
	// document is appended before its redefine targets are collected), so the
	// reverse order builds a redefine CHAIN bottom-up — and each original is
	// snapshotted AT APPLICATION time, so an outer redefinition's self-reference
	// composes over the already-redefined inner component (stZ033: enum{2,3}
	// then enum{3}; schN10: the duplicated group members meet in one UPA check).
	applied := make([]work, len(redefs))
	for i, w := range redefs {
		applied[len(redefs)-1-i] = w
	}
	// Compile each redefinition INTO the original component (mutating it in place),
	// so existing references to the name now see the redefinition.
	for _, w := range applied {
		name := declName(w.node)
		k := xname{w.sd.tns, name}
		ok := false
		switch w.node.Name.Local {
		case "simpleType", "complexType":
			switch o := c.sch.types[k].(type) {
			case *ComplexType:
				cp := *o
				c.redefineType[k], ok = &cp, true
			case *SimpleType:
				cp := *o
				c.redefineType[k], ok = &cp, true
			}
		case "group":
			if orig, found := c.groups[k]; found {
				cp := *orig
				c.redefineGroup[k], ok = &cp, true
			}
		case "attributeGroup":
			if orig, found := c.attrGroups[k]; found {
				cp := *orig
				c.redefineAttrGroup[k], ok = &cp, true
			}
		}
		if !ok {
			// src-redefine.6.1.1/7.1: a redefinition must have something to
			// redefine — the name has to exist in the document being redefined.
			return invalidf("src-redefine", "redefined %s %s is not present in the redefined schema", w.node.Name.Local, k)
		}
		c.setDoc(w.sd)
		switch w.node.Name.Local {
		case "simpleType":
			if !c.redefineSelfDerives(w.node, k) {
				return invalidf("src-redefine.5", "redefined simpleType %s must restrict itself", k)
			}
			st := c.sch.types[k].(*SimpleType)
			*st = SimpleType{name: k}
			if err := c.parseSimpleTypeInto(st, w.node); err != nil {
				return err
			}
		case "complexType":
			if !c.redefineSelfDerives(w.node, k) {
				return invalidf("src-redefine.6", "redefined complexType %s must derive from itself", k)
			}
			ct := c.sch.types[k].(*ComplexType)
			*ct = ComplexType{name: k}
			if err := c.parseComplexTypeInto(ct, w.node); err != nil {
				return err
			}
		case "group":
			g, err := c.parseGroupBody(w.node)
			if err != nil {
				return err
			}
			// src-redefine.6.2.2: with NO self-reference, the redefinition must
			// be a valid RESTRICTION of the original (schR5, schZ006, mgO013).
			if !redefineHasSelfRef(w.node, "group", k) {
				rc := &restrictionChecker{sch: c.sch}
				rp := &particle{min: 1, max: 1, term: g}
				bp := &particle{min: 1, max: 1, term: c.redefineGroup[k]}
				if !rc.particleRestrictsOK(rp, bp) {
					return invalidf("src-redefine.6.2.2", "redefined group %s is not a valid restriction of the original", k)
				}
			}
			*c.groups[k] = *g
		case "attributeGroup":
			ag, err := c.parseAttrGroupBody(w.node)
			if err != nil {
				return err
			}
			// src-redefine.7.2.2: with NO self-reference, the redefinition's
			// uses must be a valid restriction of the original's (schM10,
			// attgC028, attgZ002/Z003).
			if !redefineHasSelfRef(w.node, "attributeGroup", k) {
				if err := attrGroupRestrictsOK(ag, c.redefineAttrGroup[k], k); err != nil {
					return err
				}
			}
			*c.attrGroups[k] = *ag
		}
	}
	return nil
}

// redefineHasSelfRef reports whether the redefinition node contains a
// same-kind reference to the name being redefined.
func redefineHasSelfRef(node *xmltree.Node, kind string, self xname) bool {
	found := false
	var walk func(n *xmltree.Node)
	walk = func(n *xmltree.Node) {
		for _, ch := range n.Children {
			if ch.Kind != xmltree.KindElement || found {
				continue
			}
			if isXS(ch, kind) {
				if ref, ok := ch.AttrLocal("ref"); ok && resolveQName(ch, ref) == self {
					found = true
					return
				}
			}
			walk(ch)
		}
	}
	walk(node)
	return found
}

// attrGroupRestrictsOK enforces the restriction reading of src-redefine.7.2.2:
// the redefined attributeGroup may drop nothing required, add nothing new, and
// must preserve fixed values as fixed.
func attrGroupRestrictsOK(nu, orig *attrGroupDef, k xname) error {
	byName := map[xname]*attrUse{}
	for _, ou := range orig.uses {
		byName[ou.decl.name] = ou
	}
	newNames := map[xname]bool{}
	for _, u := range nu.uses {
		newNames[u.decl.name] = true
	}
	for _, ou := range orig.uses {
		if ou.required && !newNames[ou.decl.name] {
			return invalidf("src-redefine.7.2.2", "redefined attributeGroup %s drops required attribute %s", k, ou.decl.name)
		}
	}
	for _, u := range nu.uses {
		ou, ok := byName[u.decl.name]
		if !ok {
			if orig.wildcard == nil {
				return invalidf("src-redefine.7.2.2", "redefined attributeGroup %s adds attribute %s", k, u.decl.name)
			}
			continue
		}
		if ou.required && !u.required {
			return invalidf("src-redefine.7.2.2", "redefined attributeGroup %s weakens required attribute %s", k, u.decl.name)
		}
		if ou.decl.typ != nil && u.decl.typ != nil && !typeRestrictsFrom(u.decl.typ, ou.decl.typ) {
			return invalidf("src-redefine.7.2.2", "redefined attributeGroup %s changes the type of %s to one not derived from the original", k, u.decl.name)
		}
		if ou.hasFixed {
			if u.hasDefault || !u.hasFixed || !lexEqualCollapsed(u.fixed, ou.fixed) {
				return invalidf("src-redefine.7.2.2", "redefined attributeGroup %s does not preserve the fixed value of %s", k, u.decl.name)
			}
		}
	}
	return nil
}

// redefineSelfDerives enforces src-redefine.5/6: a redefined simpleType must be a
// restriction of the type it redefines, and a redefined complexType must derive
// (restriction or extension) from itself. A redefinition that instead derives from
// some other type — or does not derive at all — is invalid.
func (c *compiler) redefineSelfDerives(node *xmltree.Node, k xname) bool {
	var deriv *xmltree.Node
	switch node.Name.Local {
	case "simpleType":
		deriv = firstXSChild(node, "restriction")
	case "complexType":
		for _, model := range []string{"complexContent", "simpleContent"} {
			if m := firstXSChild(node, model); m != nil {
				if deriv = firstXSChild(m, "restriction"); deriv == nil {
					deriv = firstXSChild(m, "extension")
				}
				break
			}
		}
	}
	if deriv == nil {
		return false // no self-derivation (e.g. a bare complexType) is not a valid redefinition
	}
	base, ok := deriv.AttrLocal("base")
	if !ok {
		return false
	}
	bn := resolveQName(deriv, base)
	if bn == k {
		return true
	}
	if alt, ok := c.chamAlt(bn); ok && alt == k {
		return true
	}
	return false
}

// setDoc switches the compiler's namespace context to a particular document.
func (c *compiler) setDoc(sd *schemaDoc) {
	c.curDoc = sd
	c.tns = sd.tns
	c.chameleonNS = ""
	if sd.chameleon {
		c.chameleonNS = sd.tns
	}
	c.elementQualified = sd.elemQ
	c.attributeQualified = sd.attrQ
	c.finalDefault = sd.finalDefault
	c.blockDefault = sd.blockDefault
	c.imports = sd.imports
}

// reachableNS reports whether a reference in the current document may resolve
// into namespace ns: its own target namespace, one it imports, or the XSD
// namespace itself (src-resolve / "must be imported to be referenced").
func (c *compiler) reachableNS(ns string) bool {
	switch {
	case ns == c.tns || ns == xsNS || ns == xmlNS || ns == xsiNS:
		return true
	case c.chameleonNS != "" && ns == c.chameleonNS:
		return true
	}
	return c.imports[ns]
}

// chamAlt returns the adopted-namespace variant of an unqualified name to try as
// a fallback when the primary (no-namespace) lookup misses inside a chameleon
// document. It only ever offers an *additional* candidate — callers try the exact
// name first — so it can recover false negatives but never turn a resolving
// reference unresolved. Returns ok=false when no alternative applies.
func (c *compiler) chamAlt(n xname) (xname, bool) {
	if n.Space == "" && c.chameleonNS != "" {
		return xname{c.chameleonNS, n.Local}, true
	}
	return xname{}, false
}

type attrGroupDef struct {
	uses     []*attrUse
	wildcard *wildcard
}

// checkSubstGroupCycles rejects a substitution-group affiliation that loops back
// on itself (element A substitutes B substitutes … A). Walks the affiliation chain
// by name, so there is no typed-nil pointer risk.
func (c *compiler) checkSubstGroupCycles() error {
	for _, e := range c.sch.elements {
		onPath := map[xname]bool{}
		var walk func(d *ElementDecl) bool
		walk = func(d *ElementDecl) bool {
			if onPath[d.name] {
				return true
			}
			onPath[d.name] = true
			for _, h := range d.substGroups {
				if hd := c.sch.elements[h]; hd != nil && walk(hd) {
					return true
				}
			}
			delete(onPath, d.name)
			return false
		}
		if walk(e) {
			return invalidf("", "cyclic substitution group")
		}
	}
	return nil
}

// buildSubstitutionGroups indexes each head element to its (direct) members.
// inheritSubstGroupTypes gives an element declared with neither @type nor an
// inline type the {type definition} of its substitution-group head (walking up
// through heads that are themselves defaulted), per XSD §3.3.2.
func (c *compiler) inheritSubstGroupTypes() {
	for _, e := range c.sch.elements {
		if !e.typeDefaulted || len(e.substGroups) == 0 {
			continue
		}
		// A defaulted type comes from the FIRST affiliation (§3.3.2).
		seen := map[xname]bool{}
		for cur := e; len(cur.substGroups) > 0 && !seen[cur.name]; {
			seen[cur.name] = true
			h := c.sch.elements[cur.substGroups[0]]
			if h == nil {
				break
			}
			if !h.typeDefaulted || len(h.substGroups) == 0 {
				if h.typ != nil {
					e.typ = h.typ
				}
				break
			}
			cur = h
		}
	}
}

// checkTypeCycles rejects a type that participates in its own derivation chain
// (st-props-correct.2 / ct-props-correct.3: base type definitions may not form
// a cycle — e.g. a complexType extending itself, addB101).
func (c *compiler) checkTypeCycles() error {
	for name, t := range c.sch.types {
		seen := map[Type]bool{}
		cur := t
		for n := 0; cur != nil && n < 256; n++ {
			if seen[cur] {
				return invalidf("ct-props-correct.3", "circular type derivation involving %s", name)
			}
			seen[cur] = true
			switch x := cur.(type) {
			case *ComplexType:
				cur = x.baseType
			case *SimpleType:
				if x.base != nil {
					cur = x.base
				} else {
					cur = nil
				}
			default:
				cur = nil
			}
		}
	}
	return nil
}

// checkIDAttributeUses enforces ct-props-correct.5 (XSD 1.0 only): a complex
// type may not have two attribute uses whose types are or derive from xs:ID.
func (c *compiler) checkIDAttributeUses() error {
	if c.sch.version != Version10 {
		return nil
	}
	for _, ct := range c.allCTs {
		uses, _ := c.sch.effectiveAttrs(ct)
		n := 0
		for _, u := range uses {
			if u.prohibited || u.decl.typ == nil {
				continue
			}
			if u.decl.typ.resolvePrim() == xpath.XSid {
				if n++; n > 1 {
					return invalidf("ct-props-correct.5", "complex type %s has more than one ID-typed attribute", ct.name)
				}
			}
		}
	}
	return nil
}

func (c *compiler) buildSubstitutionGroups() {
	for _, e := range c.sch.elements {
		for _, h := range e.substGroups {
			c.sch.substMembers[h] = append(c.sch.substMembers[h], e)
		}
	}
}

// --- global element / attribute ---------------------------------------------

func (c *compiler) parseGlobalElement(node *xmltree.Node) error {
	name := declName(node)
	if name == "" {
		return ErrUnsupported
	}
	decl := c.sch.elements[xname{c.tns, name}]
	if decl == nil {
		decl = &ElementDecl{name: xname{c.tns, name}}
		c.sch.elements[decl.name] = decl
	}
	if err := c.fillElementCommon(decl, node); err != nil {
		return err
	}
	if err := checkValueConstraint(decl.typ, node, c.sch.version); err != nil {
		return err
	}
	if sg, ok := node.AttrLocal("substitutionGroup"); ok {
		if c.sch.version == Version10 {
			// 1.0: the attribute is a single QName; a whitespace-separated list
			// resolves (garbage-in) exactly as before this field became a list.
			decl.substGroups = []xname{resolveQName(node, sg)}
		} else {
			// 1.1: xs:QNameList — the element joins EVERY named group.
			for _, tok := range strings.Fields(sg) {
				decl.substGroups = append(decl.substGroups, resolveQName(node, tok))
			}
		}
	}
	decl.abstract = attrIs(node, "abstract", "true")
	cs, err := c.parseIdentityConstraints(node)
	if err != nil {
		return err
	}
	decl.constraints = cs
	if decl.alternatives, err = c.parseAlternatives(node); err != nil {
		return err
	}
	return nil
}

// fillElementCommon resolves an element's type (type= | inline | default anyType)
// and nillable/default/fixed. Used for global and local elements.
func (c *compiler) fillElementCommon(decl *ElementDecl, node *xmltree.Node) error {
	decl.nillable = attrIs(node, "nillable", "true")
	if b, ok := node.AttrLocal("block"); ok {
		decl.blocked = parseBlockSet(b)
	} else if c.blockDefault != "" {
		decl.blocked = parseBlockSet(c.blockDefault)
	}
	if f, ok := node.AttrLocal("final"); ok {
		decl.final = parseBlockSet(f)
	} else if c.finalDefault != "" {
		decl.final = parseBlockSet(c.finalDefault)
	}
	if d, ok := node.AttrLocal("default"); ok {
		decl.def, decl.hasDefault = d, true
	}
	if f, ok := node.AttrLocal("fixed"); ok {
		decl.fixed, decl.hasFixed = f, true
	}
	if typeRef, ok := node.AttrLocal("type"); ok {
		t, err := c.resolveTypeTolerant(node, typeRef)
		if err != nil {
			return err
		}
		if st, ok := t.(*SimpleType); ok && st != absentType {
			if err := c.checkNotationUsable(st); err != nil {
				return err
			}
		}
		decl.typ = t
		return nil
	}
	if st := firstXSChild(node, "simpleType"); st != nil {
		t := &SimpleType{}
		if err := c.parseSimpleTypeInto(t, st); err != nil {
			return err
		}
		decl.typ = t
		return nil
	}
	if ct := firstXSChild(node, "complexType"); ct != nil {
		t := &ComplexType{}
		if err := c.parseComplexTypeInto(t, ct); err != nil {
			return err
		}
		decl.typ = t
		return nil
	}
	// No type ⇒ xs:anyType: accept any content (a permissive complex type) —
	// unless a substitution-group affiliation supplies the head's type later.
	decl.typ = anyType()
	decl.typeDefaulted = true
	return nil
}

func (c *compiler) parseGlobalAttribute(node *xmltree.Node) error {
	name := declName(node)
	if name == "" {
		return ErrUnsupported
	}
	ad := c.sch.attributes[xname{c.tns, name}]
	if ad == nil {
		ad = &AttributeDecl{name: xname{c.tns, name}}
		c.sch.attributes[ad.name] = ad
	}
	st, err := c.attributeType(node)
	if err != nil {
		return err
	}
	ad.typ = st
	ad.inheritable = boolAttrTrue(node, "inheritable")
	if d, ok := node.AttrLocal("default"); ok {
		ad.def, ad.hasDefault = d, true
	}
	if f, ok := node.AttrLocal("fixed"); ok {
		ad.fixed, ad.hasFixed = f, true
	}
	if err := checkValueConstraint(st, node, c.sch.version); err != nil {
		return err
	}
	return nil
}

// checkValueConstraint verifies that a fixed/default value on node is a valid
// value of type t.
func checkValueConstraint(t Type, node *xmltree.Node, ver Version) error {
	v, ok := node.AttrLocal("fixed")
	if !ok {
		v, ok = node.AttrLocal("default")
	}
	if !ok {
		return nil
	}
	var st *SimpleType
	switch tt := t.(type) {
	case *SimpleType:
		st = tt
	case *ComplexType:
		if tt.kind == contentSimple {
			st = tt.simpleType
		} else if tt.kind == contentElementOnly {
			// A value constraint supplies character content, which element-only
			// content has nowhere to put — in BOTH versions (1.1 only relaxed
			// the ID rule below, not this one; elemD004/elemG003/elemG004).
			return invalidf("cos-valid-default.2.1", "a value constraint requires simple or mixed content")
		}
	}
	if st == nil || st == absentType {
		return nil // complex/empty/absent content type: nothing to validate against
	}
	// An ID is unique per document, so a declaration that would hand the same ID
	// to every instance of itself cannot have a value constraint. XSD 1.1 dropped
	// the rule.
	if ver == Version10 && st.resolvePrim() == xpath.XSid {
		return invalidf("a-props-correct.3", "an ID-typed declaration must not have a value constraint")
	}
	if err := st.validate(v); err != nil {
		return invalidf("", "value constraint %q is not valid for the type: %v", v, err)
	}
	return nil
}

func (c *compiler) attributeType(node *xmltree.Node) (*SimpleType, error) {
	if typeRef, ok := node.AttrLocal("type"); ok {
		t, err := c.resolveTypeTolerant(node, typeRef)
		if err != nil {
			return nil, err
		}
		st, ok := t.(*SimpleType)
		if !ok {
			return nil, invalidf("", "attribute type must be simple")
		}
		if st != absentType {
			if err := c.checkNotationUsable(st); err != nil {
				return nil, err
			}
		}
		return st, nil
	}
	if inline := firstXSChild(node, "simpleType"); inline != nil {
		st := &SimpleType{}
		if err := c.parseSimpleTypeInto(st, inline); err != nil {
			return nil, err
		}
		return st, nil
	}
	return anySimpleType(), nil
}

// --- type resolution --------------------------------------------------------

func (c *compiler) resolveType(node *xmltree.Node, qname string) (Type, error) {
	n := resolveQName(node, qname)
	if n.Space == xsNS {
		if c.sch.version == Version10 && xsd11OnlyBuiltin[n.Local] {
			return nil, invalidf("src-resolve", "xs:%s is an XSD 1.1 type", n.Local)
		}
		if n.Local == "error" {
			return xsErrorType, nil // 1.1-only; the Version10 gate above rejected it there
		}
		if lt, ok := builtinList(n.Local); ok {
			return lt, nil
		}
		if at, ok := builtinAtom(n.Local); ok {
			st := &SimpleType{variety: vAtomic, baseBuiltin: true, prim: at}
			if at == xpath.XSdateTimeStamp {
				// §3.4.28: dateTimeStamp carries {explicitTimezone: required,
				// fixed} (zone101/102, d3_4_28si11s). The facets keep linkBase
				// from collapsing restrictions of it, so the chain checks see it.
				st.facets.explicitTZ = "required"
				st.facets.fixed = map[string]bool{"explicitTimezone": true}
			}
			return st, nil
		}
		if n.Local == "anyType" {
			return anyType(), nil
		}
		// The XSD namespace is not closed to user declarations: XML Schema's
		// OWN schema-for-schemas (XMLSchema.xsd) declares a handful of global
		// simple types in it that are not datatypes — xs:derivationControl,
		// xs:blockSet, xs:formChoice, xs:namespaceList and friends — and a
		// schema that IMPORTS that document may legitimately reference them
		// (catalog-009: schema-for-xslt30.xsd imports XMLSchema.xsd because a
		// stylesheet may carry an inline xs:schema). Those live in the ordinary
		// global type table, so consult it before reporting a bad reference;
		// nothing changes for a schema that never imported it, which is every
		// case below.
		if t, ok := c.sch.types[n]; ok && !isNilType(t) {
			return t, nil
		}
		// Every real builtin resolved above: an unknown local in the XSD
		// namespace is a reference error, not a missing feature (stK002's
		// xsd:timeDuration, xsd003b.e's xsd:undefined, ctD024's
		// xs:timeInstant — all 1999-CR relics the suite expects rejected).
		return nil, invalidf("src-resolve", "unknown built-in type xs:%s", n.Local)
	}
	if withinRedefine(node) {
		if orig, ok := c.redefineType[n]; ok {
			return orig, nil
		}
	}
	if t, ok := c.sch.types[n]; ok {
		if !c.reachableNS(n.Space) {
			return nil, invalidf("src-resolve", "type %s is in a namespace this schema does not import", n)
		}
		return t, nil
	}
	if alt, ok := c.chamAlt(n); ok {
		if t, ok := c.sch.types[alt]; ok {
			return t, nil
		}
	}
	return nil, invalidf("", "unresolved type %s", n)
}

// withinRedefine reports whether node lies inside an xs:redefine element (so a
// reference to a redefined name denotes the pre-redefine component).
func withinRedefine(node *xmltree.Node) bool {
	for p := node.Parent; p != nil; p = p.Parent {
		if p.Name.Space == xsNS {
			switch p.Name.Local {
			case "redefine":
				return true
			case "schema":
				return false
			}
		}
	}
	return false
}

func anyType() *ComplexType {
	return &ComplexType{name: xname{xsNS, "anyType"}, kind: contentMixed,
		particle:     &particle{min: 0, max: unbounded, term: &wildcard{nsMode: "any", process: "lax"}},
		attrWildcard: &wildcard{nsMode: "any", process: "lax"}}
}

// isURTypeSimple reports whether st is the bare ur-type sentinel a reference to
// xs:anySimpleType or xs:anyAtomicType produces — no name, no user derivation
// steps, no facets, ur-type primitive.
func isURTypeSimple(st *SimpleType) bool {
	return st != nil && st.baseBuiltin && st.base == nil && st.name.Local == "" &&
		st.variety == vAtomic && !st.facets.any() &&
		(st.prim == xpath.XSanyAtomicType || st.prim == xpath.XSuntypedAtomic)
}

func anySimpleType() *SimpleType {
	return &SimpleType{variety: vAtomic, baseBuiltin: true, prim: xpath.XSanyAtomicType}
}

// seedXSIAttributes declares the four attributes of the XSI namespace, which
// every schema implicitly contains (XSD §3.2.7). Without them a schema that
// references one — xs:attribute ref="xsi:type" — fails to resolve. Instance
// validation handles xsi:* separately, so these declarations only serve
// references from schema documents.
func seedXSIAttributes(sch *Schema) {
	builtin := func(t xpath.AtomType) *SimpleType {
		return &SimpleType{variety: vAtomic, baseBuiltin: true, prim: t}
	}
	uriList := &SimpleType{variety: vList, item: builtin(xpath.XSanyURI)}
	for name, typ := range map[string]*SimpleType{
		"type":                      builtin(xpath.XSqname),
		"nil":                       builtin(xpath.XSboolean),
		"schemaLocation":            uriList,
		"noNamespaceSchemaLocation": builtin(xpath.XSanyURI),
	} {
		n := xname{xsiNS, name}
		sch.attributes[n] = &AttributeDecl{name: n, typ: typ}
	}
}

// --- simple types (from M1) -------------------------------------------------

// stDep is a simpleType derivation dependency. hard marks a restriction @base or
// list @itemType edge — a cycle through one of those is an invalid circular
// derivation. A union @memberTypes edge is soft: a pure-union cycle (whose members
// still bottom out in concrete types) is permitted, so it orders but never rejects.
type stDep struct {
	n    xname
	hard bool
}

// simpleTypeDeps returns the named simpleTypes a definition derives from (the
// restriction @base, list @itemType, and union @memberTypes) so the compiler can
// order compilation base-first. Inline (anonymous) derivations and builtin
// references need no ordering and are simply resolved as encountered.
func simpleTypeDeps(node *xmltree.Node) []stDep {
	var deps []stDep
	add := func(ref string, on *xmltree.Node, hard bool) {
		for _, q := range strings.Fields(ref) {
			deps = append(deps, stDep{resolveQName(on, q), hard})
		}
	}
	for _, ch := range node.Children {
		switch {
		case isXS(ch, "restriction"):
			if b, ok := ch.AttrLocal("base"); ok {
				add(b, ch, true)
			}
		case isXS(ch, "list"):
			if it, ok := ch.AttrLocal("itemType"); ok {
				add(it, ch, true)
			}
		case isXS(ch, "union"):
			if mt, ok := ch.AttrLocal("memberTypes"); ok {
				add(mt, ch, false)
			}
		}
	}
	return deps
}

func (c *compiler) parseSimpleTypeInto(st *SimpleType, node *xmltree.Node) error {
	if err := checkSimpleTypeStructure(node); err != nil {
		return err
	}
	st.declared = true
	if f, ok := node.AttrLocal("final"); ok {
		st.final = parseBlockSet(f)
	} else if c.finalDefault != "" {
		st.final = parseBlockSet(c.finalDefault)
	}
	if r := firstXSChild(node, "restriction"); r != nil {
		return c.parseRestriction(st, r)
	}
	if l := firstXSChild(node, "list"); l != nil {
		return c.parseList(st, l)
	}
	if u := firstXSChild(node, "union"); u != nil {
		return c.parseUnion(st, u)
	}
	return invalidf("", "simpleType without restriction/list/union")
}

func (c *compiler) parseRestriction(st *SimpleType, r *xmltree.Node) error {
	st.variety = vAtomic
	if baseRef, ok := r.AttrLocal("base"); ok {
		base, err := c.resolveSimpleType(r, baseRef)
		if err != nil {
			return err
		}
		if err := checkSimpleFinal(base, "restriction"); err != nil {
			return err
		}
		linkBase(st, base)
	} else if inline := firstXSChild(r, "simpleType"); inline != nil {
		base := &SimpleType{}
		if err := c.parseSimpleTypeInto(base, inline); err != nil {
			return err
		}
		linkBase(st, base)
	} else {
		return invalidf("", "restriction without a base")
	}
	if !st.baseBuiltin && st.base != nil {
		switch st.base.variety {
		case vList:
			st.variety = vList
			st.item = st.base.item
		case vUnion:
			st.variety = vUnion
			st.members = st.base.members
		}
	}
	if err := c.parseFacets(st, r); err != nil {
		return err
	}
	if err := checkFacetConsistency(st); err != nil {
		return err
	}
	if err := c.checkNotationUsable(st); err != nil {
		return err
	}
	return validateFacetValues(st, c.sch.version)
}

// checkSimpleFinal rejects a derivation from a simple type whose {final} forbids
// that derivation method.
func checkSimpleFinal(base *SimpleType, method string) error {
	if base != nil && base.final[method] {
		return invalidf("st-props-correct.3", "derivation by %s from %s is forbidden by its final", method, base.name)
	}
	return nil
}

// checkNotationUsable enforces the NOTATION rule of XSD Part 2 §3.2.19: NOTATION
// may not be used directly in a schema, only through a restriction that pins it
// down with an enumeration facet. It applies both to a restriction of NOTATION
// (which must supply the enumeration) and to every place a type is USED — as an
// element or attribute type, a list item type, or a union member.
func (c *compiler) checkNotationUsable(st *SimpleType) error {
	if st == nil {
		return nil
	}
	// A union USED as a declaration's type may not carry a bare xs:NOTATION
	// as a DIRECT member (simple093); one nested a union deeper escapes —
	// particlesZ007 declares union(xsd:NOTATION) but only ever uses it inside
	// another union, and the suite holds that valid.
	if st.variety == vUnion {
		for _, m := range st.members {
			if err := c.checkNotationUsable(m); err != nil {
				return err
			}
		}
		return nil
	}
	if st.variety != vAtomic || st.resolvePrim() != xpath.XSnotation {
		return nil
	}
	for s := st; s != nil; s = s.base {
		if s.facets.hasEnum {
			// Each enumeration value must name a DECLARED notation (compared by
			// local name — prefixes cannot be resolved this late, and a false
			// accept is preferable to a false reject across namespaces).
			for _, v := range s.facets.enumeration {
				local := v
				if i := strings.LastIndex(v, ":"); i >= 0 {
					local = v[i+1:]
				}
				if !c.notationLocals[strings.TrimSpace(local)] {
					return invalidf("enumeration-valid-notation", "enumeration value %q does not name a declared notation", v)
				}
			}
			return nil
		}
	}
	return invalidf("enumeration-required-notation", "xs:NOTATION may only be used through a restriction with an enumeration facet")
}

// checkFacetConsistency enforces that a restriction step's facets narrow (never
// loosen) the corresponding facets of its base: length/digit bounds move inward
// and whiteSpace may only strengthen (preserve → replace → collapse).
func checkFacetConsistency(st *SimpleType) error {
	if st.base == nil && !st.baseBuiltin {
		return nil
	}
	f := &st.facets
	baseMaxLen, baseMinLen, baseTotal, baseFrac := -1, -1, -1, -1
	for b, n := st.base, 0; b != nil && n < 64; b, n = b.base, n+1 {
		bf := &b.facets
		if bf.hasMaxLength && (baseMaxLen < 0 || bf.maxLength < baseMaxLen) {
			baseMaxLen = bf.maxLength
		}
		if bf.hasMinLength && bf.minLength > baseMinLen {
			baseMinLen = bf.minLength
		}
		if bf.hasTotalDigits && (baseTotal < 0 || bf.totalDigits < baseTotal) {
			baseTotal = bf.totalDigits
		}
		if bf.hasFractionDigits && (baseFrac < 0 || bf.fractionDigits < baseFrac) {
			baseFrac = bf.fractionDigits
		}
		if b.baseBuiltin {
			break
		}
	}
	if f.hasMaxLength && baseMaxLen >= 0 && f.maxLength > baseMaxLen {
		return invalidf("", "maxLength exceeds the base maxLength")
	}
	if f.hasMinLength && baseMinLen >= 0 && f.minLength < baseMinLen {
		return invalidf("", "minLength is below the base minLength")
	}
	// A derived maxLength below an INHERITED minLength empties the type
	// (NMTOKENS_maxLength001: maxLength=0 vs the built-in list's minLength=1).
	if f.hasMaxLength && baseMinLen >= 0 && f.maxLength < baseMinLen {
		return invalidf("", "maxLength is below the base minLength")
	}
	// length may co-occur with minLength/maxLength ONLY when that facet is
	// inherited from an ancestor's {facets} (WG bug 6446: NMTOKENS_length006
	// valid — the built-in list carries minLength — while string_length006
	// and NMTOKENS_length007 stay invalid).
	if f.hasLength && f.hasMinLength && baseMinLen < 0 {
		return invalidf("length-minLength-maxLength", "length cannot appear with minLength unless minLength is inherited")
	}
	if f.hasLength && f.hasMaxLength && baseMaxLen < 0 {
		return invalidf("length-minLength-maxLength", "length cannot appear with maxLength unless maxLength is inherited")
	}
	// Enumeration literals of a LIST restriction must themselves be valid
	// against the base (NMTOKENS_enumeration001: value="" vs minLength 1).
	if st.variety == vList && f.hasEnum && st.base != nil {
		for _, e := range f.enumeration {
			if st.base.validate(e) != nil {
				return invalidf("enumeration-valid-restriction", "enumeration value %q is not valid against the base type", e)
			}
		}
	}
	if f.hasTotalDigits && baseTotal >= 0 && f.totalDigits > baseTotal {
		return invalidf("", "totalDigits exceeds the base totalDigits")
	}
	if f.hasFractionDigits && baseFrac >= 0 && f.fractionDigits > baseFrac {
		return invalidf("", "fractionDigits exceeds the base fractionDigits")
	}
	if f.whiteSpace != "" {
		// The base's effective whiteSpace: from the base chain, or — for a direct
		// builtin restriction (st.base nil) — the builtin's own default.
		baseWS := primitiveWhiteSpace(st.resolvePrim(), st.variety)
		if st.base != nil {
			baseWS = st.base.effectiveWhiteSpace()
		}
		if wsRank(f.whiteSpace) < wsRank(baseWS) {
			return invalidf("", "whiteSpace %q weakens the base whiteSpace %q", f.whiteSpace, baseWS)
		}
	}
	// Bounds narrowing: the derived step's value range must be a subset of the
	// base chain's range, across facet KINDS (a derived maxInclusive is checked
	// against a base maxExclusive too — e.g. maxInclusive=11 under maxExclusive=10
	// loosens). The nearest base bound of either kind is the effective one (each
	// prior step already narrowed). Compared in the resolved builtin's value space.
	var bHi, bLo string
	var bHiExcl, bLoExcl bool
	for b, n := st.base, 0; b != nil && n < 64; b, n = b.base, n+1 {
		bf := &b.facets
		if bHi == "" {
			if bf.hasMaxIncl {
				bHi, bHiExcl = bf.maxIncl, false
			} else if bf.hasMaxExcl {
				bHi, bHiExcl = bf.maxExcl, true
			}
		}
		if bLo == "" {
			if bf.hasMinIncl {
				bLo, bLoExcl = bf.minIncl, false
			} else if bf.hasMinExcl {
				bLo, bLoExcl = bf.minExcl, true
			}
		}
		if b.baseBuiltin {
			break
		}
	}
	prim := st.resolvePrim()
	cmp := func(dv, bv string) (int, bool) {
		d, e1 := xpath.CastTo(xpath.NewString(applyWhiteSpace("collapse", dv)), prim)
		bb, e2 := xpath.CastTo(xpath.NewString(applyWhiteSpace("collapse", bv)), prim)
		if e1 != nil || e2 != nil {
			return 0, false
		}
		return xpath.CompareAtomic(d, bb)
	}
	dHi, dHiExcl, dHasHi := f.maxIncl, false, f.hasMaxIncl
	if f.hasMaxExcl {
		dHi, dHiExcl, dHasHi = f.maxExcl, true, true
	}
	dLo, dLoExcl, dHasLo := f.minIncl, false, f.hasMinIncl
	if f.hasMinExcl {
		dLo, dLoExcl, dHasLo = f.minExcl, true, true
	}
	if dHasHi && bHi != "" {
		if c, ok := cmp(dHi, bHi); ok {
			// Loosening: derived upper bound above the base's, or equal to an
			// exclusive base bound while itself admitting that value.
			// (An exclusive derived bound equal to an inclusive base bound is a
			// valid narrowing; equal exclusive/exclusive or incl/incl is a no-op.)
			if c > 0 || (c == 0 && bHiExcl && !dHiExcl) {
				return invalidf("", "upper bound %q loosens the base upper bound %q", dHi, bHi)
			}
		}
	}
	if dHasLo && bLo != "" {
		if c, ok := cmp(dLo, bLo); ok {
			if c < 0 || (c == 0 && bLoExcl && !dLoExcl) {
				return invalidf("", "lower bound %q loosens the base lower bound %q", dLo, bLo)
			}
		}
	}
	// Cross-direction: a derived upper bound below the base's lower bound (or vice
	// versa) yields an empty value space — the spec words this as e.g. maxInclusive
	// must be ≥ the base's minInclusive (Datatypes §4.3.7.4 pt. 2 etc.).
	if dHasHi && bLo != "" {
		if c, ok := cmp(dHi, bLo); ok {
			if c < 0 || (c == 0 && (dHiExcl || bLoExcl)) {
				return invalidf("", "upper bound %q is below the base lower bound %q", dHi, bLo)
			}
		}
	}
	if dHasLo && bHi != "" {
		if c, ok := cmp(dLo, bHi); ok {
			if c > 0 || (c == 0 && (dLoExcl || bHiExcl)) {
				return invalidf("", "lower bound %q is above the base upper bound %q", dLo, bHi)
			}
		}
	}
	// Builtin implicit bounds: the bounded integer types carry implicit
	// minInclusive/maxInclusive (positiveInteger ≥ 1, byte ∈ [-128,127], …). A
	// derived bound emptying that range is invalid. Compared as xs:integer so a
	// bound lexically outside the builtin's own range still compares.
	if isIntegerLike(prim) {
		iLo, iHi := builtinIntBounds(prim)
		icmp := func(a, b string) (int, bool) {
			x, e1 := xpath.CastTo(xpath.NewString(applyWhiteSpace("collapse", a)), xpath.XSinteger)
			y, e2 := xpath.CastTo(xpath.NewString(applyWhiteSpace("collapse", b)), xpath.XSinteger)
			if e1 != nil || e2 != nil {
				return 0, false
			}
			return xpath.CompareAtomic(x, y)
		}
		if dHasHi && iLo != "" {
			if c, ok := icmp(dHi, iLo); ok && (c < 0 || (c == 0 && dHiExcl)) {
				return invalidf("", "upper bound %q empties the %s value space", dHi, prim)
			}
		}
		if dHasLo && iHi != "" {
			if c, ok := icmp(dLo, iHi); ok && (c > 0 || (c == 0 && dLoExcl)) {
				return invalidf("", "lower bound %q empties the %s value space", dLo, prim)
			}
		}
	}
	// explicitTimezone Valid Restriction (§4.3.16.4): a base required/prohibited
	// pins the value — the nearest base declaration wins (zone004/005/408/409,
	// d4_3_16si03s; xs:dateTimeStamp's synthetic {required, fixed} covers
	// zone101/102 and d3_4_28si11s).
	if f.explicitTZ != "" {
		for b, n := st.base, 0; b != nil && n < 64; b, n = b.base, n+1 {
			if bz := b.facets.explicitTZ; bz != "" {
				if (bz == "required" || bz == "prohibited") && f.explicitTZ != bz {
					return invalidf("", "explicitTimezone %q cannot loosen the base's %q", f.explicitTZ, bz)
				}
				break
			}
		}
	}
	// Fixed-facet preservation: a facet declared fixed="true" anywhere in the
	// base chain may not take a DIFFERENT value in a restriction step
	// (d4_3_16si04s, d3_4_26si11s).
	for b, n := st.base, 0; b != nil && n < 64; b, n = b.base, n+1 {
		for name := range b.facets.fixed {
			bv, bok := scalarFacet(&b.facets, name)
			dv, dok := scalarFacet(f, name)
			if bok && dok && dv != bv {
				return invalidf("", "facet %s is fixed to %q in the base type", name, bv)
			}
		}
	}
	return nil
}

// scalarFacet reads a facet's declared value by element name, for the
// fixed-facet comparison.
func scalarFacet(fs *facetSet, name string) (string, bool) {
	switch name {
	case "whiteSpace":
		return fs.whiteSpace, fs.whiteSpace != ""
	case "explicitTimezone":
		return fs.explicitTZ, fs.explicitTZ != ""
	case "length":
		return strconv.Itoa(fs.length), fs.hasLength
	case "minLength":
		return strconv.Itoa(fs.minLength), fs.hasMinLength
	case "maxLength":
		return strconv.Itoa(fs.maxLength), fs.hasMaxLength
	case "totalDigits":
		return strconv.Itoa(fs.totalDigits), fs.hasTotalDigits
	case "fractionDigits":
		return strconv.Itoa(fs.fractionDigits), fs.hasFractionDigits
	case "minInclusive":
		return fs.minIncl, fs.hasMinIncl
	case "maxInclusive":
		return fs.maxIncl, fs.hasMaxIncl
	case "minExclusive":
		return fs.minExcl, fs.hasMinExcl
	case "maxExclusive":
		return fs.maxExcl, fs.hasMaxExcl
	}
	return "", false
}

// builtinIntBounds returns the implicit inclusive bounds of a bounded builtin
// integer type ("" for an unbounded side).
func builtinIntBounds(t xpath.AtomType) (lo, hi string) {
	switch t {
	case xpath.XSpositiveInteger:
		return "1", ""
	case xpath.XSnonNegativeInteger:
		return "0", ""
	case xpath.XSnegativeInteger:
		return "", "-1"
	case xpath.XSnonPositiveInteger:
		return "", "0"
	case xpath.XSlong:
		return "-9223372036854775808", "9223372036854775807"
	case xpath.XSint:
		return "-2147483648", "2147483647"
	case xpath.XSshort:
		return "-32768", "32767"
	case xpath.XSbyte:
		return "-128", "127"
	case xpath.XSunsignedLong:
		return "0", "18446744073709551615"
	case xpath.XSunsignedInt:
		return "0", "4294967295"
	case xpath.XSunsignedShort:
		return "0", "65535"
	case xpath.XSunsignedByte:
		return "0", "255"
	}
	return "", ""
}

func nonNegInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (c *compiler) parseList(st *SimpleType, l *xmltree.Node) error {
	st.variety = vList
	if itemRef, ok := l.AttrLocal("itemType"); ok {
		var it *SimpleType
		t, err := c.resolveTypeTolerant(l, itemRef)
		if err != nil {
			return err
		}
		if it, ok = t.(*SimpleType); !ok {
			return invalidf("", "expected a simple type, got complex %s", itemRef)
		}
		if it != absentType {
			if err := c.checkNotationUsable(it); err != nil {
				return err
			}
			if err := checkSimpleFinal(it, "list"); err != nil {
				return err
			}
			if it.variety == vList {
				return invalidf("cos-list-of-atomic", "a list's item type must be atomic or a union (not a list)")
			}
			// Nor is an ur-type a legal item type (simple052: a list of
			// xs:anyAtomicType; the union path enforces the same rule).
			if c.sch.version == Version11 && isURTypeSimple(it) {
				return invalidf("cos-list-of-atomic", "an ur-type is not a valid list item type")
			}
		}
		st.item = it
	} else if inline := firstXSChild(l, "simpleType"); inline != nil {
		it := &SimpleType{}
		if err := c.parseSimpleTypeInto(it, inline); err != nil {
			return err
		}
		if it.variety == vList {
			return invalidf("cos-list-of-atomic", "a list's item type must be atomic or a union (not a list)")
		}
		if c.sch.version == Version11 && isURTypeSimple(it) {
			return invalidf("cos-list-of-atomic", "an ur-type is not a valid list item type")
		}
		st.item = it
	} else {
		return invalidf("", "list without an item type")
	}
	return nil
}

func (c *compiler) parseUnion(st *SimpleType, u *xmltree.Node) error {
	st.variety = vUnion
	if mt, ok := u.AttrLocal("memberTypes"); ok {
		for _, ref := range strings.Fields(mt) {
			// src-resolve: a QName with an undeclared prefix does not resolve —
			// it must not silently fall back to the absent namespace (stE008).
			if i := strings.IndexByte(ref, ':'); i > 0 {
				if _, bound := u.LookupPrefix(ref[:i]); !bound {
					return invalidf("src-resolve", "undefined namespace prefix in %q", ref)
				}
			}
			m, err := c.resolveSimpleType(u, ref)
			if err != nil {
				return err
			}
			// XSD 1.1: xs:anySimpleType / xs:anyAtomicType may not be a union
			// member type (Part 2 §2.4.1 note; stE053 — 1.0 tolerates it).
			if c.sch.version == Version11 && isURTypeSimple(m) {
				return invalidf("", "%s is not a valid union member type", ref)
			}
			// Bare xs:NOTATION as a union MEMBER is legal (particlesZ007's
			// union memberTypes="xsd:NOTATION"); the enumeration-required
			// rule applies where a type is USED as an element/attribute type.
			if err := checkSimpleFinal(m, "union"); err != nil {
				return err
			}
			st.members = append(st.members, m)
		}
	}
	for _, ch := range u.Children {
		if isXS(ch, "simpleType") {
			m := &SimpleType{}
			if err := c.parseSimpleTypeInto(m, ch); err != nil {
				return err
			}
			st.members = append(st.members, m)
		}
	}
	if len(st.members) == 0 {
		return invalidf("", "union without members")
	}
	return nil
}

func (c *compiler) parseFacets(st *SimpleType, r *xmltree.Node) error {
	var patternGroup []*regexp.Regexp
	seen := map[string]bool{}
	for _, f := range r.Children {
		if f.Kind != xmltree.KindElement || f.Name.Space != xsNS {
			continue
		}
		// A simpleContent restriction body mixes facets with attribute uses and
		// asserts; those are parsed separately (parseAttributes/parseAsserts), so
		// they are not facets here. (Their invalid appearance in a *pure* simpleType
		// restriction is rejected by the schema-for-schemas grammar check.)
		switch f.Name.Local {
		case "attribute", "attributeGroup", "anyAttribute", "assert":
			continue
		}
		// A single-valued facet may appear at most once (pattern/enumeration/
		// assertion may repeat).
		switch f.Name.Local {
		case "annotation", "simpleType", "pattern", "enumeration", "assertion":
		default:
			if seen[f.Name.Local] {
				return invalidf("", "facet xs:%s specified more than once", f.Name.Local)
			}
			seen[f.Name.Local] = true
		}
		val, _ := f.AttrLocal("value")
		switch f.Name.Local {
		case "annotation", "simpleType":
		case "pattern":
			if err := validateXSDRegex(val, c.sch.version); err != nil {
				return err // an XSD-invalid pattern makes the schema invalid
			}
			re, err := compileXSDPattern(val, c.sch.version)
			if err != nil {
				return ErrUnsupported
			}
			patternGroup = append(patternGroup, re)
		case "enumeration":
			st.facets.enumeration = append(st.facets.enumeration, val)
			// Expanded here, where the facet element's own namespace bindings
			// are in hand; consulted only for a QName/NOTATION-based type.
			st.facets.enumQName = append(st.facets.enumQName, resolveQName(f, val).String())
			st.facets.hasEnum = true
		case "minInclusive":
			st.facets.minIncl, st.facets.hasMinIncl = val, true
		case "maxInclusive":
			st.facets.maxIncl, st.facets.hasMaxIncl = val, true
		case "minExclusive":
			st.facets.minExcl, st.facets.hasMinExcl = val, true
		case "maxExclusive":
			st.facets.maxExcl, st.facets.hasMaxExcl = val, true
		case "length", "minLength", "maxLength", "fractionDigits":
			n, ok := nonNegInt(val)
			if !ok {
				return invalidf("", "facet xs:%s has an invalid value %q", f.Name.Local, val)
			}
			switch f.Name.Local {
			case "length":
				st.facets.length, st.facets.hasLength = n, true
			case "minLength":
				st.facets.minLength, st.facets.hasMinLength = n, true
			case "maxLength":
				st.facets.maxLength, st.facets.hasMaxLength = n, true
			case "fractionDigits":
				st.facets.fractionDigits, st.facets.hasFractionDigits = n, true
			}
		case "totalDigits":
			// totalDigits must be a positiveInteger (>= 1).
			n, ok := nonNegInt(val)
			if !ok || n < 1 {
				return invalidf("", "facet xs:totalDigits has an invalid value %q", val)
			}
			st.facets.totalDigits, st.facets.hasTotalDigits = n, true
		case "whiteSpace":
			st.facets.whiteSpace = val
		case "explicitTimezone":
			st.facets.explicitTZ = applyWhiteSpace("collapse", val)
		case "assertion":
			// XSD 1.1 simple-type assertion facet; 1.0 has no such facet.
			if c.sch.version != Version11 {
				return invalidf("", "unknown facet xs:assertion")
			}
			a, err := c.parseAssert(f)
			if err != nil {
				return err
			}
			st.assertions = append(st.assertions, a)
		default:
			return invalidf("", "unknown facet xs:%s", f.Name.Local)
		}
		// A facet declared fixed="true" pins its value for every further
		// restriction step (d4_3_16si04s, d3_4_26si11s).
		if fx, ok := f.AttrLocal("fixed"); ok {
			switch applyWhiteSpace("collapse", fx) {
			case "true", "1":
				if st.facets.fixed == nil {
					st.facets.fixed = map[string]bool{}
				}
				st.facets.fixed[f.Name.Local] = true
			}
		}
	}
	if len(patternGroup) > 0 {
		st.facets.patterns = [][]*regexp.Regexp{patternGroup}
	}
	return nil
}

func linkBase(st, base *SimpleType) {
	if base.baseBuiltin && base.base == nil && base.name.Local == "" && !base.facets.any() &&
		len(base.assertions) == 0 && base.variety == vAtomic {
		st.baseBuiltin = true
		st.prim = base.prim
		return
	}
	st.base = base
}

// absentType is the sentinel bound where an element/attribute @type or a list
// @itemType names a component the schema set does not contain (the saxon
// Missing family): XSD 1.0's missing-sub-components rules keep such a schema
// usable, and the error surfaces only when a declaration carrying the absent
// type is actually exercised at validation time (SimpleType.check refuses it).
// restriction/extension bases and every @ref stay fatal at compile time.
var absentType = &SimpleType{name: xname{Local: "\x00absent"}}

// xsErrorType is XSD 1.1's xs:error: the type with an EMPTY value space. Any
// element or attribute it governs is invalid — its whole purpose is the
// always-fail branch of conditional type assignment (cta0010/0011/0013,
// s3_12ii05) and vc-gated declarations (vc014, vc_007).
var xsErrorType = &SimpleType{name: xname{xsNS, "error"}, variety: vAtomic}

// resolveTypeTolerant resolves a type reference, mapping a clean "no such
// component" miss to the absentType sentinel. The tolerance never applies to
// the XSD namespace (ctZ002: xs:strong) or to a namespace the referring
// document cannot reach (addB009 stays a src-resolve error).
func (c *compiler) resolveTypeTolerant(node *xmltree.Node, qname string) (Type, error) {
	t, err := c.resolveType(node, qname)
	if err == nil {
		return t, nil
	}
	if c.sch.version != Version10 {
		// 1.1 keeps the compile-time error: its version-gated sets carry
		// counter-tests (s3_12si02s, simple006) the 1.0 sweep verified absent.
		return nil, err
	}
	n := resolveQName(node, qname)
	if _, exists := c.sch.types[n]; !exists && n.Space != xsNS && c.reachableNS(n.Space) &&
		!errors.Is(err, ErrUnsupported) && validQNameLexical(strings.TrimSpace(qname)) {
		return absentType, nil
	}
	return nil, err
}

func (c *compiler) resolveSimpleType(node *xmltree.Node, qname string) (*SimpleType, error) {
	t, err := c.resolveType(node, qname)
	if err != nil {
		return nil, err
	}
	st, ok := t.(*SimpleType)
	if !ok {
		return nil, invalidf("", "expected a simple type, got complex %s", qname)
	}
	return st, nil
}

// --- shared helpers ---------------------------------------------------------

func resolveQName(node *xmltree.Node, q string) xname {
	q = strings.TrimSpace(q)
	if i := strings.IndexByte(q, ':'); i >= 0 {
		ns, _ := node.LookupPrefix(q[:i])
		return xname{ns, q[i+1:]}
	}
	ns, _ := node.LookupPrefix("")
	return xname{ns, q}
}

// xsd11OnlyBuiltin lists the built-in datatypes XSD 1.1 added; naming one in a
// 1.0 schema is a reference to a type that does not exist.
var xsd11OnlyBuiltin = map[string]bool{
	// xs:anyAtomicType is deliberately absent: the suite expects 1.0 processors
	// to tolerate it (saxonData/Simple simple050).
	"dateTimeStamp": true, "dayTimeDuration": true,
	"yearMonthDuration": true, "precisionDecimal": true, "error": true,
}

// builtinList synthesizes the three built-in LIST types (xs:NMTOKENS /
// xs:IDREFS / xs:ENTITIES): a list of the corresponding atomic item type with
// minLength 1, exactly the datatypes schema's own definition. The name is set
// because every reference mints a fresh copy and derivation membership is
// decided by name (typeRestrictsFromDepth); declared stays false — these are
// builtins, not user components.
func builtinList(local string) (*SimpleType, bool) {
	var item xpath.AtomType
	switch local {
	case "NMTOKENS":
		item = xpath.XSnmtoken
	case "IDREFS":
		item = xpath.XSidref
	case "ENTITIES":
		item = xpath.XSentity
	default:
		return nil, false
	}
	st := &SimpleType{
		name:    xname{xsNS, local},
		variety: vList,
		item:    &SimpleType{variety: vAtomic, baseBuiltin: true, prim: item},
	}
	st.facets.minLength = 1
	st.facets.hasMinLength = true
	return st, true
}

func builtinAtom(local string) (xpath.AtomType, bool) {
	switch local {
	case "anySimpleType", "anyAtomicType":
		return xpath.XSanyAtomicType, true
	case "anyType":
		return 0, false
	}
	return xpath.AtomTypeByName(local)
}

func firstXSChild(node *xmltree.Node, local string) *xmltree.Node {
	for _, ch := range node.Children {
		if isXS(ch, local) {
			return ch
		}
	}
	return nil
}

func isXS(n *xmltree.Node, local string) bool {
	return n.Kind == xmltree.KindElement && n.Name.Space == xsNS && n.Name.Local == local
}

func attrIs(node *xmltree.Node, name, want string) bool {
	v, _ := node.AttrLocal(name)
	if want == "true" {
		return v == "true" || v == "1"
	}
	return v == want
}

func atoiOr(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// isNCName reports whether s is a usable no-colon name. It is deliberately
// conservative (rejects the clear violations: empty, contains a colon or space,
// or starts with a digit/'.'/'-') so it never rejects a valid Unicode name.
func isNCName(s string) bool {
	// ASCII chars are checked strictly ([A-Za-z_] first, [A-Za-z0-9._-] after —
	// attC006's name="'" must be rejected); non-ASCII stays permissive to avoid
	// Unicode-class false rejections.
	return permissiveNCName(s)
}

func (f facetSet) any() bool {
	return f.hasEnum || f.hasLength || f.hasMinLength || f.hasMaxLength ||
		f.hasMinIncl || f.hasMaxIncl || f.hasMinExcl || f.hasMaxExcl ||
		f.hasTotalDigits || f.hasFractionDigits || len(f.patterns) > 0 ||
		f.whiteSpace != "" || f.explicitTZ != ""
}

// declName returns a declaration's @name with the whiteSpace facet of xs:NCName
// applied. The schema for schemas types @name as an NCName, so the value is
// collapsed before it is used or checked — "sub2-elem " names the same component
// as "sub2-elem" (addB193).
func declName(n *xmltree.Node) string {
	v, _ := n.AttrLocal("name")
	return applyWhiteSpace("collapse", v)
}
