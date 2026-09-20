package xslt

import (
	"math"
	"sort"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// mrgInstr is the compiled form of xsl:merge. It carries one or more merge
// sources (each a sequence of nodes plus its merge keys) and the body of the
// single xsl:merge-action child. This is a non-streaming implementation: each
// source is fully evaluated and sorted by its merge keys, then all sources are
// merged into groups of items sharing the same merge key (in key order). For
// each group eng.curMergeGroup / eng.curMergeKey are set and the merge-action
// body is executed.
type mrgInstr struct {
	sources []*mrgSource
	action  []instruction
	el      *xmltree.Node
}

// mrgSource is one xsl:merge-source: a select expression yielding nodes and the
// merge keys used to order/merge them.
type mrgSource struct {
	sel             *xpath.Parsed
	keys            []*mrgKey
	el              *xmltree.Node
	sortBeforeMerge bool
	// forEachItem / forEachSource (XSLT 3.0 §15.1) turn ONE xsl:merge-source
	// into SEVERAL input sequences: @select is evaluated once per item of
	// @for-each-item (with that item as the context item), or once per
	// document named by @for-each-source. Each resulting sequence is an
	// independent input sequence for the merge — in particular the
	// already-sorted precondition (XTDE2220) is checked per sequence, not on
	// the concatenation. At most one of the two may be present (XTSE3195).
	forEachItem   *xpath.Parsed
	forEachSource *xpath.Parsed
	// val is the compiled [xsl:]validation / [xsl:]type request. §24.4 names
	// xsl:merge-source alongside xsl:source-document as a construct that
	// validates the document it retrieves — and, like it, performs no
	// validation unless explicitly asked (the ambient
	// [xsl:]default-validation does not reach it). nil whenever the run is
	// not schema-aware.
	val *valRequest
	// streamable records streamable="yes". XSLT 3.0 §15.4 applies an implicit
	// fn:snapshot to @select's result then — "whether or not streamed
	// processing is actually used, and whether or not the processor supports
	// streaming" — so merge keys and current-merge-group() see the snapshot's
	// ancestor spine, not the live tree.
	streamable bool
}

// mrgKey is one xsl:merge-key. select (or use-of body) yields the key value;
// dataType/order control comparison, mirroring xsl:sort semantics.
type mrgKey struct {
	sel                                 *xpath.Parsed
	body                                []instruction // sequence-constructor content, used when @select is absent
	dataType                            string        // "text" | "number"
	order                               string        // "ascending" | "descending"
	lang, collation, caseOrder          string
	hasLang, hasCollation, hasCaseOrder bool
	el                                  *xmltree.Node
}

func (*mrgInstr) instr() {}

func init() {
	instrRegistry["merge"] = mrgCompile
	// xsl:merge-source / xsl:merge-key / xsl:merge-action are consumed by the
	// xsl:merge compiler; if they appear standalone, ignore them gracefully.
	instrRegistry["merge-source"] = mrgCompileIgnore
	instrRegistry["merge-key"] = mrgCompileIgnore
	instrRegistry["merge-action"] = mrgCompileIgnore

	xsltFuncs["current-merge-group"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		if !eng.inMerge {
			return nil, true, errAt(nil, "err:XTDE3480: current-merge-group() called when the current merge group is absent")
		}
		// current-merge-group() is only available directly within an
		// xsl:merge-action, not from a function called from it (merge-100:
		// XTDE3480), even one invoked from inside the merge-action itself.
		if eng.funcDepth > 0 {
			return nil, true, errAt(nil, "err:XTDE3480: current-merge-group() is not available inside a called function")
		}
		// An optional source-name argument is accepted but ignored, unless given,
		// in which case it must match an enclosing xsl:merge-source name.
		if len(args) > 0 {
			source := xpath.ToString(args[0])
			if !eng.curMergeSources[source] {
				return nil, true, errAt(nil, "err:XTDE3490: current-merge-group() $source %q does not match any xsl:merge-source name for the current merge", source)
			}
			// With a source name the result is restricted to the items of the
			// current group that came from THAT xsl:merge-source (merge-047).
			return eng.curMergeGroupBySource[source], true, nil
		}
		if eng.curMergeGroup == nil {
			return xpath.NodeSet{}, true, nil
		}
		return eng.curMergeGroup, true, nil
	}
	xsltFuncs["current-merge-key"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		if !eng.inMerge {
			return nil, true, errAt(nil, "err:XTDE3510: current-merge-key() called when the current merge key is absent")
		}
		if eng.funcDepth > 0 {
			return nil, true, errAt(nil, "err:XTDE3510: current-merge-key() is not available inside a called function")
		}
		if eng.curMergeKey == nil {
			return "", true, nil
		}
		return eng.curMergeKey, true, nil
	}
}

func mrgCompileIgnore(c *compiler, el *xmltree.Node) (instruction, error) {
	return nil, nil
}

func mrgCompile(c *compiler, el *xmltree.Node) (instruction, error) {
	m := &mrgInstr{el: el}
	// Content model (XSLT 3.0 §15.1): (xsl:merge-source+, xsl:merge-action,
	// xsl:fallback*) — in that order, with EXACTLY ONE xsl:merge-action
	// (merge-007) and no xsl:fallback before it (merge-086).
	seenAction := false
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS {
			continue
		}
		switch ch.Name.Local {
		case "merge-source":
			if seenAction {
				return nil, errAt(ch, "err:XTSE0010: xsl:merge-source must precede xsl:merge-action")
			}
			src, err := mrgCompileSource(c, ch)
			if err != nil {
				return nil, err
			}
			m.sources = append(m.sources, src)
		case "merge-action":
			if seenAction {
				return nil, errAt(ch, "err:XTSE0010: xsl:merge allows only one xsl:merge-action")
			}
			seenAction = true
			body, err := c.compileSequence(childNodesForBody(ch))
			if err != nil {
				return nil, err
			}
			m.action = body
		case "fallback":
			if !seenAction {
				return nil, errAt(ch, "err:XTSE0010: xsl:fallback may appear only after xsl:merge-action")
			}
		}
	}
	if len(m.sources) == 0 {
		return nil, errAt(el, "xsl:merge requires at least one xsl:merge-source")
	}
	seenNames := map[string]bool{}
	for _, src := range m.sources {
		if len(src.keys) != len(m.sources[0].keys) {
			return nil, errAt(src.el, "err:XTSE2200: every xsl:merge-source must have the same number of xsl:merge-key children")
		}
		if name, ok := src.el.AttrLocal("name"); ok {
			if seenNames[name] {
				return nil, errAt(src.el, "err:XTSE3190: duplicate xsl:merge-source name %q", name)
			}
			seenNames[name] = true
		}
		for i, k := range src.keys {
			if !mrgKeysConsistent(m.sources[0].keys[i], k) {
				return nil, errAt(k.el, "err:XTDE2210: corresponding xsl:merge-key elements have differing effective lang/order/collation/case-order/data-type")
			}
		}
	}
	return m, nil
}

func mrgCompileSource(c *compiler, el *xmltree.Node) (*mrgSource, error) {
	_, hasItem := el.AttrLocal("for-each-item")
	_, hasSrc := el.AttrLocal("for-each-source")
	if hasItem && hasSrc {
		return nil, errAt(el, "err:XTSE3195: xsl:merge-source must not have both for-each-item and for-each-source")
	}
	sel, err := requireExpr(el, "select")
	if err != nil {
		return nil, err
	}
	// @name is of type NCName (merge-046a) and @streamable an XSLT boolean
	// (merge-064) — both XTSE0020 when malformed, even though this processor
	// never streams.
	if v, ok := el.AttrLocal("name"); ok && !isNCName(v) {
		return nil, errAt(el, "err:XTSE0020: xsl:merge-source name=%q is not an NCName", v)
	}
	if v, ok := el.AttrLocal("streamable"); ok && !isXSLTBooleanLexical(v) {
		return nil, errAt(el, "err:XTSE0020: streamable=%q is not an XSLT boolean", v)
	}
	// The content of xsl:merge-source is (xsl:merge-key+) and nothing else —
	// a sequence constructor alongside @select is XTSE0010 (merge-027).
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS || ch.Name.Local != "merge-key" {
			return nil, errAt(ch, "err:XTSE0010: xsl:merge-source allows only xsl:merge-key children")
		}
	}
	src := &mrgSource{sel: sel, el: el}
	if v, ok := el.AttrLocal("streamable"); ok {
		src.streamable = isXSLTTrue(strings.TrimSpace(v))
	}
	if v, ok := el.AttrLocal("for-each-item"); ok {
		p, err := parseXPathFor(el, v)
		if err != nil {
			return nil, errAt(el, "bad for-each-item %q: %v", v, err)
		}
		src.forEachItem = p
	}
	if v, ok := el.AttrLocal("for-each-source"); ok {
		p, err := parseXPathFor(el, v)
		if err != nil {
			return nil, errAt(el, "bad for-each-source %q: %v", v, err)
		}
		src.forEachSource = p
	}
	if v, ok := el.AttrLocal("sort-before-merge"); ok {
		v = strings.TrimSpace(v)
		src.sortBeforeMerge = v == "yes" || v == "true" || v == "1"
	}
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS || ch.Name.Local != "merge-key" {
			continue
		}
		k, err := mrgCompileKey(c, ch)
		if err != nil {
			return nil, err
		}
		src.keys = append(src.keys, k)
	}
	if len(src.keys) == 0 {
		return nil, errAt(el, "xsl:merge-source requires at least one xsl:merge-key")
	}
	val, verr := compileValidation(el, vkSourceDoc)
	if verr != nil {
		return nil, verr
	}
	src.val = val
	return src, nil
}

func mrgCompileKey(c *compiler, el *xmltree.Node) (*mrgKey, error) {
	k := &mrgKey{el: el, dataType: "text", order: "ascending"}
	if v, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, v)
		if err != nil {
			return nil, errAt(el, "bad merge-key select %q: %v", v, err)
		}
		k.sel = p
	} else if body := childNodesForBody(el); len(body) > 0 {
		// No @select: the key value comes from the sequence-constructor
		// content instead (result-document-1143's "m").
		b, err := c.compileSequence(body)
		if err != nil {
			return nil, err
		}
		k.body = b
	}
	if v, ok := el.AttrLocal("data-type"); ok {
		k.dataType = v
	}
	if v, ok := el.AttrLocal("order"); ok {
		k.order = v
	}
	if v, ok := el.AttrLocal("lang"); ok {
		k.lang, k.hasLang = v, true
	}
	if v, ok := el.AttrLocal("collation"); ok {
		k.collation, k.hasCollation = v, true
	}
	if v, ok := el.AttrLocal("case-order"); ok {
		k.caseOrder, k.hasCaseOrder = v, true
	}
	return k, nil
}

// mrgKeysConsistent reports whether two xsl:merge-key elements occupying
// corresponding positions in different xsl:merge-source elements have the
// same effective lang/order/collation/case-order/data-type (XTDE2210): a
// value is considered to differ if the attribute is present on one and not
// the other, or present on both with different values. All five attributes
// are AVTs, so a pair where either side looks like an attribute value
// template (contains '{') is not statically comparable and is left alone —
// this check only catches the literal-value case.
func mrgKeysConsistent(a, b *mrgKey) bool {
	isAVT := func(s string) bool { return strings.Contains(s, "{") }
	strEq := func(x, y string) bool {
		if isAVT(x) || isAVT(y) {
			return true
		}
		return x == y
	}
	if !strEq(a.dataType, b.dataType) || !strEq(a.order, b.order) {
		return false
	}
	if a.hasLang != b.hasLang || (a.hasLang && !strEq(a.lang, b.lang)) {
		return false
	}
	if a.hasCollation != b.hasCollation || (a.hasCollation && !strEq(a.collation, b.collation)) {
		return false
	}
	if a.hasCaseOrder != b.hasCaseOrder || (a.hasCaseOrder && !strEq(a.caseOrder, b.caseOrder)) {
		return false
	}
	return true
}

// mrgItem is one merged item: a node from some source together with its
// computed (stringified) merge-key values and the original key Objects.
type mrgItem struct {
	node    *xmltree.Node
	keys    []string       // stringified key values, one per merge-key
	keyObjs []xpath.Object // raw key values (for current-merge-key)
	source  *mrgSource
}

func (m *mrgInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	// Documents read via for-each-source carry their merge-source's
	// use-accumulators restriction only while this merge runs.
	mark := len(eng.mrgUndo)
	defer func() {
		for i := len(eng.mrgUndo) - 1; i >= mark; i-- {
			eng.mrgUndo[i]()
		}
		eng.mrgUndo = eng.mrgUndo[:mark]
	}()
	var items []*mrgItem
	type srcRange struct {
		src        *mrgSource
		start, end int
	}
	var srcRanges []srcRange

	// Evaluate every merge source, computing each item's key values.
	for _, src := range m.sources {
		anchors, err := eng.mrgAnchors(src, r)
		if err != nil {
			return err
		}
		for _, anchor := range anchors {
			v, err := eng.eval(src.sel, src.el, anchor)
			if err != nil {
				return err
			}
			nodes := mrgToNodes(v)
			if src.streamable {
				snap := make([]*xmltree.Node, len(nodes))
				for i, n := range nodes {
					snap[i] = snapshotNode(n)
					materializeInScopeNS(snap[i], n)
					// fn:snapshot PRESERVES accumulator values (§18.2.4), which
					// a merge key may well be (merge-082 keys on
					// accumulator-before, and without this every key collapses
					// to the accumulator's initial value).
					eng.recordAccOrigin(snap[i], n)
				}
				nodes = snap
			}
			srcStart := len(items)
			for i, node := range nodes {
				it := &mrgItem{node: node, source: src}
				// XSLT 3.0 §15.1: an xsl:merge-key is evaluated with a
				// SINGLETON focus — the item itself, at position 1 of 1
				// (merge-092: select="position()" must give 1 for every item,
				// collapsing every source into a single group).
				_ = i
				nr := rt{node: node, pos: 1, size: 1}
				for _, k := range src.keys {
					var kv xpath.Object
					switch {
					case k.sel != nil:
						kv, err = eng.eval(k.sel, k.el, nr)
						if err != nil {
							return err
						}
						// A merge key must be a SINGLETON (XSLT 3.0 §15.1):
						// xsl:merge-key's declared type is xs:anyAtomicType?,
						// so a multi-item selection is a type error rather
						// than silently taking the first item (merge-038's
						// select="country" picks five elements).
						if n := len(xpath.Items(kv)); n > 1 {
							return errAt(k.el, "err:XTTE2230: xsl:merge-key selects %d items, but a merge key must be a single atomic value", n)
						}
					case k.body != nil:
						// No @select: the key value is "constructed simple
						// content" from the xsl:merge-key body — XSLT 3.0
						// "temporary output state" (5.7.1) forbids
						// xsl:result-document there (err:XTDE1480 —
						// result-document-1143).
						frag := &xmltree.Node{Kind: xmltree.KindDocument}
						eng.tempOutputDepth++
						err = eng.execSequence(k.body, nr, frag)
						eng.tempOutputDepth--
						if err != nil {
							return err
						}
						kv = xpath.NodeSet{frag}
					default:
						kv = xpath.NodeSet{node}
					}
					it.keyObjs = append(it.keyObjs, kv)
					it.keys = append(it.keys, xpath.ToString(kv))
				}
				items = append(items, it)
			}
			srcRanges = append(srcRanges, srcRange{src: src, start: srcStart, end: len(items)})
		}
	}

	// Merge-key descriptors are taken from the first source (all sources are
	// required to have the same number of merge keys; ordering/data-type for
	// comparison comes from the first source's keys).
	keySpecs := m.sources[0].keys
	effDataType := mrgEffectiveDataTypes(items, keySpecs)
	// @order (and @data-type) on xsl:merge-key are ATTRIBUTE VALUE TEMPLATES:
	// evaluate them once, in the focus of the xsl:merge instruction itself
	// (merge-028 computes order="{if(position() lt 2) then 'ascending' else
	// 'descending'}" from the enclosing xsl:for-each's position).
	effOrder := make([]string, len(keySpecs))
	// @collation, @case-order and @lang are attribute value templates too —
	// the REC's own syntax summary for xsl:merge-key writes every one of
	// lang/order/collation/case-order/data-type in { } braces — so they are
	// resolved HERE, in the xsl:merge instruction's focus, rather than from
	// the raw compile-time text. merge-071/072 run the identical stylesheet
	// with collation="{$collation}" bound to two different UCA tailorings,
	// and the whole point of the pair is that inputs sorted for one of them
	// are NOT sorted for the other.
	effColl := make([]func(a, b string) int, len(keySpecs))
	effCase := make([]string, len(keySpecs))
	for ki, k := range keySpecs {
		effOrder[ki] = eng.mrgKeyAttr(k, k.order, r)
		if strings.Contains(k.dataType, "{") {
			effDataType[ki] = eng.mrgKeyAttr(k, k.dataType, r)
		}
		effColl[ki], effCase[ki] = eng.mrgCollatorFor(k, r)
	}
	// XTDE2210: corresponding merge-keys of different sources must have the
	// same EFFECTIVE order/data-type/collation/lang/case-order. The static
	// check (mrgKeysConsistent) skips any pair written as an attribute value
	// template, so a mismatch that only shows up once the AVTs are evaluated
	// is caught here instead (merge-011).
	for _, src := range m.sources[1:] {
		for ki, k := range src.keys {
			if ki >= len(keySpecs) {
				break
			}
			if eng.mrgKeyAttr(k, k.order, r) != effOrder[ki] {
				return errAt(k.el, "err:XTDE2210: corresponding xsl:merge-key elements have different effective order")
			}
			dt := eng.mrgKeyAttr(k, k.dataType, r)
			want := effDataType[ki]
			if want == "" {
				want = keySpecs[ki].dataType
			}
			if _, explicit := k.el.AttrLocal("data-type"); explicit && dt != want {
				return errAt(k.el, "err:XTDE2210: corresponding xsl:merge-key elements have different effective data-type")
			}
		}
	}

	// Unless sort-before-merge="yes", each source's own sequence must already
	// be sorted by its merge keys (XTDE2220). The comparison uses the
	// EFFECTIVE order/data-type/collation/case-order computed above — the
	// REC's own words are "using the collation rules defined by the
	// attributes of the corresponding xsl:merge-key children" — so a merge
	// key written as an attribute value template is checked like any other
	// rather than skipped for fear of comparing against an unevaluated
	// default.
	for _, sr := range srcRanges {
		if sr.src.sortBeforeMerge {
			continue
		}
		srcItems := items[sr.start:sr.end]
		for i := 1; i < len(srcItems); i++ {
			if mrgLess(srcItems[i], srcItems[i-1], sr.src.keys, effDataType, effOrder, effColl, effCase) {
				return errAt(sr.src.el, "err:XTDE2220: xsl:merge-source input is not correctly sorted by its merge keys")
			}
		}
	}

	if err := mrgCheckComparable(items); err != nil {
		return err
	}

	// Sort all items by their merge keys (stable, so equal-key items keep
	// document/source order). This yields the merged ordering.
	sort.SliceStable(items, func(a, b int) bool {
		return mrgLess(items[a], items[b], keySpecs, effDataType, effOrder, effColl, effCase)
	})

	// Group consecutive items that share the same key tuple, then run the
	// merge-action body once per group.
	prevMergeGroup, prevMergeKey := eng.curMergeGroup, eng.curMergeKey
	prevInMerge, prevSources := eng.inMerge, eng.curMergeSources
	prevBySource := eng.curMergeGroupBySource
	eng.inMerge = true
	eng.curMergeSources = map[string]bool{}
	for _, src := range m.sources {
		if name, ok := src.el.AttrLocal("name"); ok {
			eng.curMergeSources[name] = true
		}
	}
	defer func() {
		eng.curMergeGroup, eng.curMergeKey = prevMergeGroup, prevMergeKey
		eng.inMerge, eng.curMergeSources = prevInMerge, prevSources
		eng.curMergeGroupBySource = prevBySource
	}()

	// Group boundaries are computed up front because the merge-action's focus
	// is (first item of the group, POSITION OF THE GROUP, NUMBER OF GROUPS) —
	// XSLT 3.0 §15.2, merge-047's "position() mod 2".
	var groups [][]*mrgItem
	for i, n := 0, len(items); i < n; {
		j := i + 1
		for j < n && mrgSameKey(items[i], items[j]) {
			j++
		}
		groups = append(groups, items[i:j])
		i = j
	}
	for gi, group := range groups {
		ns := make(xpath.NodeSet, 0, len(group))
		bySource := map[string]xpath.NodeSet{}
		for _, it := range group {
			ns = append(ns, it.node)
			if name, ok := it.source.el.AttrLocal("name"); ok {
				bySource[name] = append(bySource[name], it.node)
			}
		}
		eng.curMergeGroup = ns
		eng.curMergeGroupBySource = bySource
		eng.curMergeKey = mrgKeyValue(group[0])

		gr := rt{node: group[0].node, pos: gi + 1, size: len(groups)}
		if err := eng.execSequence(m.action, gr, out); err != nil {
			return err
		}
	}
	return nil
}

// mrgAnchors returns the contexts in which one xsl:merge-source's @select is
// evaluated: the merge's own context when neither @for-each-item nor
// @for-each-source is present, otherwise one context per item / per named
// source document. Each anchor yields an independent input sequence.
func (eng *engine) mrgAnchors(src *mrgSource, r rt) ([]rt, error) {
	switch {
	case src.forEachItem != nil:
		v, err := eng.eval(src.forEachItem, src.el, r)
		if err != nil {
			return nil, err
		}
		nodes := mrgToNodes(v)
		out := make([]rt, 0, len(nodes))
		for i, nd := range nodes {
			out = append(out, rt{node: nd, pos: i + 1, size: len(nodes)})
		}
		return out, nil
	case src.forEachSource != nil:
		v, err := eng.eval(src.forEachSource, src.el, r)
		if err != nil {
			return nil, err
		}
		// @for-each-source is declared as xs:string* — an item that is not a
		// string (merge-043 passes "1 to 5") is XPTY0004, not a URI.
		its := xpath.Items(v)
		out := make([]rt, 0, len(its))
		for i, it := range its {
			a, err := xpath.AtomicFromItem(it)
			if err != nil {
				return nil, errAt(src.el, "err:XPTY0004: xsl:merge-source/@for-each-source must be a sequence of strings")
			}
			if tag, ok := xpath.ItemAtomTypeTag(a); ok {
				switch xpath.AtomType(tag) {
				case xpath.XSstring, xpath.XSanyURI, xpath.XSuntypedAtomic:
				default:
					return nil, errAt(src.el, "err:XPTY0004: xsl:merge-source/@for-each-source must be a sequence of strings")
				}
			}
			href := xpath.ToString(a)
			// @for-each-source's items are URI REFERENCES, resolved against the
			// xsl:merge-source element's own base URI (merge-041: xml:base="../.."
			// on xsl:merge itself), not against the run's starting directory.
			if abs, aerr := xpath.ResolveURIRef(href, xpath.NodeBaseURI(src.el, "")); aerr == nil && abs != "" {
				href = abs
			}
			if eng.resolver == nil {
				return nil, errAt(src.el, "err:FODC0002: cannot retrieve document %q", href)
			}
			doc, ok := eng.resolver.ResolveDoc(href)
			if !ok {
				return nil, errAt(src.el, "err:FODC0002: cannot retrieve document %q", href)
			}
			if verr := eng.applyValidation(src.val, doc); verr != nil {
				return nil, verr
			}
			eng.mrgUndo = append(eng.mrgUndo, eng.restrictAccumulators(doc, src.el))
			out = append(out, rt{node: doc, pos: i + 1, size: len(its)})
		}
		return out, nil
	}
	return []rt{r}, nil
}

// mrgKeyAttr evaluates one xsl:merge-key attribute value, which is an
// attribute value template, in the focus of the xsl:merge instruction.
func (eng *engine) mrgKeyAttr(k *mrgKey, raw string, r rt) string {
	if !strings.Contains(raw, "{") {
		return raw
	}
	a, err := parseAVTFor(k.el, raw)
	if err != nil {
		return raw
	}
	v, err := eng.evalAVT(a, k.el, r)
	if err != nil {
		return raw
	}
	return strings.TrimSpace(v)
}

// mrgCollatorFor resolves one xsl:merge-key's effective collation and
// case-order in the focus of the xsl:merge instruction. All of @collation,
// @case-order and @lang are attribute value templates (XSLT 3.0 §15.4's syntax
// summary), so each is evaluated rather than read as compile-time text.
//
// The precedence mirrors xsl:sort: an explicit @collation wins; failing that,
// @lang selects a tailored collator (Swedish sorts a-ring/a-diaeresis/
// o-diaeresis after z, German does not — which is exactly what merge-071 and
// merge-072 turn on); @case-order applies only when no collator was chosen,
// since a real collator already fixes case ordering itself.
func (eng *engine) mrgCollatorFor(k *mrgKey, r rt) (func(a, b string) int, string) {
	var coll func(a, b string) int
	var caseOrder string
	if k.hasCollation {
		if uri := eng.mrgKeyAttr(k, k.collation, r); uri != "" {
			coll, _ = xpath.ResolveCollator(uri)
		}
	}
	if k.hasCaseOrder && coll == nil {
		caseOrder = eng.mrgKeyAttr(k, k.caseOrder, r)
	}
	if coll == nil && k.hasLang {
		if lc := xpath.CollatorForLang(eng.mrgKeyAttr(k, k.lang, r), caseOrder); lc != nil {
			coll, caseOrder = lc, ""
		}
	}
	return coll, caseOrder
}

// mrgCheckComparable reports XTTE2230: a type error if some key item selected
// in one merge source's input sequence is not comparable (via "le") with the
// corresponding key item selected in another source. This is checked as a
// dedicated pass — separate from the (string-based) ordering comparator used
// for the actual sort — so an incomparable pair is always caught, even though
// the merge itself never touches xs:sort's shared string-comparison path.
func mrgCheckComparable(items []*mrgItem) error {
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			if a.source == b.source {
				continue
			}
			n := len(a.keyObjs)
			if len(b.keyObjs) < n {
				n = len(b.keyObjs)
			}
			for ki := 0; ki < n; ki++ {
				ai := xpath.Items(a.keyObjs[ki])
				bi := xpath.Items(b.keyObjs[ki])
				if len(ai) != 1 || len(bi) != 1 {
					continue
				}
				aa, aerr := xpath.AtomicFromItem(ai[0])
				bb, berr := xpath.AtomicFromItem(bi[0])
				if aerr != nil || berr != nil {
					continue
				}
				if _, comparable := xpath.CompareAtomic(aa, bb); !comparable {
					return errAt(nil, "err:XTTE2230: xsl:merge key values from different input sequences are not comparable")
				}
			}
		}
	}
	return nil
}

// mrgKeyValue returns the merge-key value reported by current-merge-key(): the
// single key value when there is one merge key, otherwise the sequence of key
// values.
func mrgKeyValue(it *mrgItem) xpath.Object {
	if len(it.keyObjs) == 1 {
		return it.keyObjs[0]
	}
	var items []xpath.Item
	for _, k := range it.keyObjs {
		items = append(items, xpath.Items(k)...)
	}
	return xpath.FromItems(items)
}

func mrgToNodes(v xpath.Object) []*xmltree.Node {
	if ns, ok := xpath.ToNodeSet(v); ok {
		return []*xmltree.Node(ns)
	}
	// Non-node items are modelled as synthetic text nodes so "." works.
	items := xpath.Items(v)
	nodes := make([]*xmltree.Node, 0, len(items))
	for _, it := range items {
		if nd, ok := it.(*xmltree.Node); ok {
			nodes = append(nodes, nd)
			continue
		}
		// Mark it as what it is — a SYNTHETIC stand-in for an atomic item,
		// exactly as xsl:for-each / for-each-group / iterate do. Without
		// SynthCtx/Atomic it looked like a genuine text node, so simple-content
		// construction merged adjacent ones instead of separating the distinct
		// atomic values they stand for (merge-025: current-merge-group() over
		// two overlapping integer ranges must read "20 20", not "2020").
		//
		// Deliberately NOT stamped with the item's TypeAnno, unlike the other
		// carrier sites: that would be a further improvement ("." inside a
		// merge-source select would keep its integer type, so ". to . + 9"
		// would stop raising a spurious XPTY0004) but current-output-uri-010
		// currently passes only BECAUSE of that spurious error — its other
		// alternative needs a host base output URI, which this engine does not
		// have. Add the annotation together with base-output-uri support.
		nd := &xmltree.Node{
			Kind:     xmltree.KindText,
			SynthCtx: true,
			Atomic:   true,
			Value:    xpath.ToString(xpath.FromItems([]xpath.Item{it})),
		}
		attachRealItem(nd, it)
		nodes = append(nodes, nd)
	}
	return nodes
}

// mrgLess orders two merged items by their key tuples per the merge-key specs.
func mrgLess(a, b *mrgItem, keys []*mrgKey, effDataType, effOrder []string, effColl []func(a, b string) int, effCase []string) bool {
	n := len(a.keys)
	if len(b.keys) < n {
		n = len(b.keys)
	}
	for ki := 0; ki < n; ki++ {
		dataType, order := "text", "ascending"
		if ki < len(keys) {
			dataType, order = keys[ki].dataType, keys[ki].order
		}
		if ki < len(effDataType) && effDataType[ki] != "" {
			dataType = effDataType[ki]
		}
		if ki < len(effOrder) && effOrder[ki] != "" {
			order = effOrder[ki]
		}
		// The effective collation and case-order were resolved by
		// mrgCollatorFor in the xsl:merge instruction's own focus, since all
		// five xsl:merge-key attributes are attribute value templates. An
		// unresolvable collation URI falls back to the default comparator
		// rather than raising XTDE1035 here: mrgLess has no error return, and
		// every call site is a boolean sort/pre-sort-check predicate.
		var caseOrder string
		var coll func(a, b string) int
		if ki < len(effColl) {
			coll = effColl[ki]
		}
		if ki < len(effCase) {
			caseOrder = effCase[ki]
		}
		cmp := compareKey(a.keys[ki], b.keys[ki], dataType, caseOrder, coll)
		if cmp == 0 {
			continue
		}
		if order == "descending" {
			return cmp > 0
		}
		return cmp < 0
	}
	return false
}

// mrgEffectiveDataTypes auto-detects, for each merge-key position whose
// data-type was not explicitly given (mirrors xsl:sort's own auto-detection
// in sortNodes), "number" when every non-empty key value seen across ALL
// items (from every source) at that position looks numeric — otherwise
// "text". Checked against the already-stringified key (mrgItem.keys, the same
// string compareKey's "number" mode itself parses via xpath.ToNumber) rather
// than the atomic keyObjs: an atomic (non-node) merge-source item is
// round-tripped through a synthetic text node (mrgToNodes) and its merge-key
// "." re-atomizes to xs:untypedAtomic, which IsNumeric() never reports true
// for, even though its string form is plainly numeric. A data-type given as
// an attribute value template is left alone (neither this detection nor the
// raw AVT text can be trusted statically).
func mrgEffectiveDataTypes(items []*mrgItem, keySpecs []*mrgKey) []string {
	eff := make([]string, len(keySpecs))
	for ki, k := range keySpecs {
		if _, ok := k.el.AttrLocal("data-type"); ok {
			continue // explicit (including AVT) — leave as-is
		}
		allNum, anySeen := true, false
		for _, it := range items {
			if ki >= len(it.keys) || it.keys[ki] == "" {
				continue
			}
			anySeen = true
			if math.IsNaN(xpath.ToNumber(it.keys[ki])) {
				allNum = false
			}
		}
		if allNum && anySeen {
			eff[ki] = "number"
		}
	}
	return eff
}

// mrgSameKey reports whether two items share the same merge-key tuple (by
// stringified key value, the comparison basis for grouping).
func mrgSameKey(a, b *mrgItem) bool {
	if len(a.keys) != len(b.keys) {
		return false
	}
	for i := range a.keys {
		if a.keys[i] != b.keys[i] {
			return false
		}
	}
	return true
}
