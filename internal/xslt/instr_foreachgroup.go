package xslt

import (
	"sort"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// fegMode is the kind of grouping requested by xsl:for-each-group.
type fegMode int

const (
	fegGroupBy fegMode = iota
	fegGroupAdjacent
	fegGroupStartingWith
	fegGroupEndingWith
)

// fegInstr is a compiled xsl:for-each-group instruction.
type fegInstr struct {
	sel       *xpath.Parsed  // @select population
	mode      fegMode        // which grouping attribute was used
	key       *xpath.Parsed  // group-by / group-adjacent expression (nil otherwise)
	pat       *xpath.Pattern // group-starting-with / group-ending-with pattern (nil otherwise)
	collation *avt           // group-by / group-adjacent collation (nil = default)
	// composite is xsl:for-each-group/@composite="yes" (XSLT 3.0): the key
	// expression's WHOLE atomized sequence is ONE composite grouping key
	// (compared component-by-component) instead of each atomic value being a
	// separate key that the item joins a group for.
	composite bool
	sorts     []sortKey
	body      []instruction
	el        *xmltree.Node
}

func (*fegInstr) instr() {}

func init() {
	instrRegistry["for-each-group"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		sel, err := requireExpr(el, "select")
		if err != nil {
			return nil, err
		}
		n := &fegInstr{sel: sel, el: el}

		switch {
		case fegHasAttr(el, "group-by"):
			n.mode = fegGroupBy
			p, err := requireExpr(el, "group-by")
			if err != nil {
				return nil, err
			}
			n.key = p
		case fegHasAttr(el, "group-adjacent"):
			n.mode = fegGroupAdjacent
			p, err := requireExpr(el, "group-adjacent")
			if err != nil {
				return nil, err
			}
			n.key = p
		case fegHasAttr(el, "group-starting-with"):
			n.mode = fegGroupStartingWith
			v, _ := el.AttrLocal("group-starting-with")
			pat, err := parsePatternFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad group-starting-with pattern %q: %v", v, err)
			}
			if err := checkPatternGroupingFuncs(el, pat); err != nil {
				return nil, err
			}
			n.pat = pat
		case fegHasAttr(el, "group-ending-with"):
			n.mode = fegGroupEndingWith
			v, _ := el.AttrLocal("group-ending-with")
			pat, err := parsePatternFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad group-ending-with pattern %q: %v", v, err)
			}
			if err := checkPatternGroupingFuncs(el, pat); err != nil {
				return nil, err
			}
			n.pat = pat
		default:
			return nil, errAt(el, "xsl:for-each-group requires one of group-by, group-adjacent, group-starting-with, group-ending-with")
		}

		if v, ok := el.AttrLocal("composite"); ok {
			n.composite = xsltBool(v)
		}
		if v, ok := el.AttrLocal("collation"); ok {
			// XTSE1090: collation is only meaningful (and only allowed) with
			// group-by/group-adjacent, which compare atomic key values.
			if n.mode != fegGroupBy && n.mode != fegGroupAdjacent {
				return nil, errAt(el, "err:XTSE1090: xsl:for-each-group/@collation requires group-by or group-adjacent")
			}
			a, err := parseAVTFor(el, v)
			if err != nil {
				return nil, errAt(el, "bad collation %q: %v", v, err)
			}
			n.collation = a
		}

		sorts, _, err := c.compileSortsAndParams(el)
		if err != nil {
			return nil, err
		}
		n.sorts = sorts
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		n.body = body
		return n, nil
	}

	// current-group() yields the nodes of the group being processed; calling it
	// with no current group is XTDE1061.
	xsltFuncs["current-group"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		if !eng.curGroupOK {
			return nil, true, errAt(nil, "err:XTDE1061: current-group() called outside xsl:for-each-group")
		}
		return eng.curGroup, true, nil
	}
	// current-grouping-key() yields the key for the current group (empty for
	// starting-with/ending-with grouping); no current group is XTDE1071.
	xsltFuncs["current-grouping-key"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		if !eng.curGroupOK {
			return nil, true, errAt(nil, "err:XTDE1071: current-grouping-key() called outside xsl:for-each-group")
		}
		if !eng.curKeyOK {
			return nil, true, errAt(nil, "err:XTDE1071: current-grouping-key() called when the current grouping key is absent (group-starting-with/group-ending-with)")
		}
		if eng.curKey == nil {
			return xpath.Sequence{}, true, nil
		}
		return eng.curKey, true, nil
	}
}

func fegHasAttr(el *xmltree.Node, name string) bool {
	_, ok := el.AttrLocal(name)
	return ok
}

// fegGroup is one formed group: its member nodes and (for by/adjacent) its key.
type fegGroup struct {
	nodes []*xmltree.Node
	key   xpath.Object // nil for starting/ending-with
	// atoms are the key's component atomic values: exactly one for an
	// ordinary group-by/group-adjacent key, the whole (possibly empty,
	// possibly variable-length) sequence for a composite key.
	atoms  []*xpath.Atomic
	hasKey bool
}

// sameFegKey compares two (possibly composite) grouping keys: same length and
// every component pair equal under the fn:distinct-values rules.
func sameFegKey(a, b []*xpath.Atomic, coll func(x, y string) int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !xpath.SameGroupingKey(a[i], b[i], coll) {
			return false
		}
	}
	return true
}

// fegKeyObject packages a key's component atomics as the value
// current-grouping-key() returns: the single atomic itself for a simple key,
// the whole sequence for a composite one.
func fegKeyObject(atoms []*xpath.Atomic, composite bool) xpath.Object {
	if !composite && len(atoms) == 1 {
		return atoms[0]
	}
	items := make([]xpath.Item, len(atoms))
	for i, a := range atoms {
		items[i] = a
	}
	return xpath.FromItems(items)
}

func (n *fegInstr) exec(eng *engine, r rt, out *xmltree.Node) error {
	v, err := eng.eval(n.sel, n.el, r)
	if err != nil {
		return err
	}
	// The population is a sequence; atomic items become synthetic text nodes so
	// "." inside the body yields their value.
	items := xpath.Items(v)
	pop := make([]*xmltree.Node, len(items))
	for i, it := range items {
		if nd, ok := it.(*xmltree.Node); ok {
			pop[i] = nd
		} else {
			// SynthCtx so a pattern like group-starting-with=".[. mod 3 = 0]"
			// sees the ATOMIC value through "." (match-134/135). Atomic so
			// two adjacent grouped values keep their space separator when
			// current-group() is copied out (for-each-group-041/042) — same
			// "adjacent atomics join with a single space, never silently
			// discarded when empty" treatment simpleContentJoin's own doc
			// comment documents as the intended behavior for this kind of
			// wrapper (the same fix xsl:for-each's identical wrapper needed
			// in execForEach).
			nd := &xmltree.Node{Kind: xmltree.KindText, SynthCtx: true, Atomic: true, Value: xpath.ToString(xpath.FromItems([]xpath.Item{it}))}
			// Preserve the item's exact original atomic type through
			// atomization, same as xsl:for-each (for-each-group-068: a
			// group-by="." key over an atomic population must see the real
			// xs:float/xs:decimal/xs:double value, not a degraded
			// xs:untypedAtomic string that erases the type distinctions the
			// non-transitive-comparison test depends on).
			if tag, ok := xpath.ItemAtomTypeTag(it); ok {
				nd.TypeAnno = tag
			}
			attachRealItem(nd, it)
			pop[i] = nd
		}
	}

	// The @collation AVT (group-by/group-adjacent only, XTSE1090) is resolved
	// once against the instruction's own context, not per grouping item.
	var coll func(a, b string) int
	if n.collation != nil {
		s, err := eng.evalAVT(n.collation, n.el, r)
		if err != nil {
			return err
		}
		if coll, err = xpath.ResolveCollator(s); err != nil {
			return errAt(n.el, "%v", err)
		}
	} else {
		coll = eng.ambientDefaultCollation(n.el)
	}

	var groups []*fegGroup
	switch n.mode {
	case fegGroupBy:
		groups, err = n.fegFormBy(eng, pop, coll)
	case fegGroupAdjacent:
		groups, err = n.fegFormAdjacent(eng, pop, coll)
	case fegGroupStartingWith:
		groups, err = n.fegFormStartingWith(eng, pop)
	case fegGroupEndingWith:
		groups, err = n.fegFormEndingWith(eng, pop)
	}
	if err != nil {
		return err
	}

	if len(n.sorts) > 0 {
		groups, err = n.fegSortGroups(eng, groups, r)
		if err != nil {
			return err
		}
	}

	// Save/restore grouping context so nested groupings work correctly.
	prevGroup, prevOK, prevKey, prevKeyOK := eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK
	defer func() {
		eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK = prevGroup, prevOK, prevKey, prevKeyOK
	}()

	total := len(groups)
	for i, g := range groups {
		eng.curGroup = xpath.NodeSet(g.nodes)
		eng.curGroupOK = true
		eng.curKeyOK = g.hasKey
		if g.hasKey {
			eng.curKey = g.key
		} else {
			eng.curKey = nil
		}
		var first *xmltree.Node
		if len(g.nodes) > 0 {
			first = g.nodes[0]
		}
		// Each group's body is its own sequence constructor and gets its own
		// variable scope — see execForEach's note on closure capture.
		if err := eng.forEachIteration(n.body, rt{node: first, pos: i + 1, size: total}, out); err != nil {
			return err
		}
	}
	return nil
}

// fegKeyAtoms evaluates the grouping expression for a single item, atomized
// to a sequence of atomic values. An item whose key atomizes to the empty
// sequence joins no group at all (XSLT 2.0/3.0 §14.3).
func (n *fegInstr) fegKeyAtoms(eng *engine, node *xmltree.Node, pos, size int) ([]*xpath.Atomic, error) {
	v, err := eng.eval(n.key, n.el, rt{node: node, pos: pos, size: size})
	if err != nil {
		return nil, err
	}
	items, err := xpath.Atomize(v)
	if err != nil {
		return nil, err
	}
	atoms := make([]*xpath.Atomic, len(items))
	for i, it := range items {
		atoms[i] = it.(*xpath.Atomic)
	}
	return atoms, nil
}

// fegFormBy implements group-by. The key expression may return a SEQUENCE of
// atomic values for a single item, in which case the item joins one group per
// DISTINCT value in that sequence (for-each-group-033/063: an item can belong
// to more than one group, or to none if its key sequence is empty). Two key
// values are "the same" under the fn:distinct-values equality rules (SameGroupingKey):
// eq-comparable values compare by value (a timezoned xs:dateTime groups by
// instant, not lexical form — for-each-group-061/064), and a pair eq cannot
// compare (e.g. dateTime vs integer) is simply unequal rather than an error
// (for-each-group-064/065). Groups are ordered by first appearance of their key.
func (n *fegInstr) fegFormBy(eng *engine, pop []*xmltree.Node, coll func(a, b string) int) ([]*fegGroup, error) {
	var groups []*fegGroup
	size := len(pop)
	for i, node := range pop {
		atoms, err := n.fegKeyAtoms(eng, node, i+1, size)
		if err != nil {
			return nil, err
		}
		if n.composite {
			// One composite key per item: the item joins exactly one group,
			// even when its key sequence is empty or of varying length.
			var target *fegGroup
			for _, g := range groups {
				if sameFegKey(atoms, g.atoms, coll) {
					target = g
					break
				}
			}
			if target == nil {
				target = &fegGroup{atoms: atoms, key: fegKeyObject(atoms, true), hasKey: true}
				groups = append(groups, target)
			}
			target.nodes = append(target.nodes, node)
			continue
		}
		var joined []*fegGroup // groups this item already joined, this iteration
	valueLoop:
		for _, a := range atoms {
			for _, g := range joined {
				if xpath.SameGroupingKey(a, g.key.(*xpath.Atomic), coll) {
					continue valueLoop // already added to this group for this item
				}
			}
			var target *fegGroup
			for _, g := range groups {
				if xpath.SameGroupingKey(a, g.key.(*xpath.Atomic), coll) {
					target = g
					break
				}
			}
			if target == nil {
				target = &fegGroup{key: a, atoms: []*xpath.Atomic{a}, hasKey: true}
				groups = append(groups, target) // preserve first-appearance order of keys
			}
			target.nodes = append(target.nodes, node)
			joined = append(joined, target)
		}
	}
	return groups, nil
}

func (n *fegInstr) fegFormAdjacent(eng *engine, pop []*xmltree.Node, coll func(a, b string) int) ([]*fegGroup, error) {
	var groups []*fegGroup
	var cur *fegGroup
	var curKey []*xpath.Atomic
	size := len(pop)
	for i, node := range pop {
		atoms, err := n.fegKeyAtoms(eng, node, i+1, size)
		if err != nil {
			return nil, err
		}
		// group-adjacent requires exactly one grouping-key item (XTTE1100) —
		// unless composite="yes", where the whole sequence is one key.
		if !n.composite && len(atoms) != 1 {
			return nil, errAt(n.el, "err:XTTE1100: group-adjacent key must be a single item (got %d)", len(atoms))
		}
		if cur == nil || !sameFegKey(atoms, curKey, coll) {
			cur = &fegGroup{nodes: []*xmltree.Node{node}, atoms: atoms, key: fegKeyObject(atoms, n.composite), hasKey: true}
			curKey = atoms
			groups = append(groups, cur)
			continue
		}
		cur.nodes = append(cur.nodes, node)
	}
	return groups, nil
}

func (n *fegInstr) fegMatches(eng *engine, node *xmltree.Node) (bool, error) {
	env := &evalEnv{eng: eng, el: n.el, current: node}
	// XSLT §5.5: while a PATTERN is evaluated, the "current captured
	// substrings" component of the dynamic context is an empty sequence — so
	// regex-group() inside a group-starting-with/group-ending-with predicate
	// returns "" even when the xsl:for-each-group sits inside an
	// xsl:matching-substring (analyze-string-076).
	saved := eng.regexGroups
	eng.regexGroups = nil
	eng.patternDepth++
	ok, err := n.pat.Match(node, &xpath.Context{Node: node, CtxItem: realItemOf(node), Pos: 1, Size: 1, Vars: env, NS: env, Funcs: env, Resolver: eng.resolver, NoOutputURI: true, DefaultElemNS: xpathDefaultNS(env.el), Now: eng.now, SchemaTypes: schemaTypesFor(env.el)})
	eng.patternDepth--
	eng.regexGroups = saved
	return ok, err
}

func (n *fegInstr) fegFormStartingWith(eng *engine, pop []*xmltree.Node) ([]*fegGroup, error) {
	var groups []*fegGroup
	var cur *fegGroup
	for _, node := range pop {
		match, err := n.fegMatches(eng, node)
		if err != nil {
			return nil, err
		}
		if cur == nil || match {
			cur = &fegGroup{nodes: []*xmltree.Node{node}}
			groups = append(groups, cur)
			continue
		}
		cur.nodes = append(cur.nodes, node)
	}
	return groups, nil
}

func (n *fegInstr) fegFormEndingWith(eng *engine, pop []*xmltree.Node) ([]*fegGroup, error) {
	var groups []*fegGroup
	var cur *fegGroup
	for _, node := range pop {
		if cur == nil {
			cur = &fegGroup{}
			groups = append(groups, cur)
		}
		cur.nodes = append(cur.nodes, node)
		match, err := n.fegMatches(eng, node)
		if err != nil {
			return nil, err
		}
		if match {
			cur = nil // close the group; next item starts a fresh one
		}
	}
	return groups, nil
}

// fegSortGroups orders the groups by the xsl:sort keys, evaluated in the
// group context (current node = first item, current-group/key available).
func (n *fegInstr) fegSortGroups(eng *engine, groups []*fegGroup, ctx rt) ([]*fegGroup, error) {
	type keyed struct {
		g    *fegGroup
		keys []string
	}
	prevGroup, prevOK, prevKey, prevKeyOK := eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK
	defer func() {
		eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK = prevGroup, prevOK, prevKey, prevKeyOK
	}()

	dataTypes := make([]string, len(n.sorts))
	orders := make([]string, len(n.sorts))
	caseOrders := make([]string, len(n.sorts))
	colls := make([]func(a, b string) int, len(n.sorts))
	for si, sk := range n.sorts {
		dt, ord, co, coll, err := eng.resolveSortAttrs(sk, n.el, ctx)
		if err != nil {
			return nil, err
		}
		dataTypes[si], orders[si], caseOrders[si], colls[si] = dt, ord, co, coll
	}

	items := make([]keyed, len(groups))
	total := len(groups)
	for i, g := range groups {
		eng.curGroup = xpath.NodeSet(g.nodes)
		eng.curGroupOK = true
		eng.curKeyOK = g.hasKey
		if g.hasKey {
			eng.curKey = g.key
		} else {
			eng.curKey = nil
		}
		var first *xmltree.Node
		if len(g.nodes) > 0 {
			first = g.nodes[0]
		}
		k := keyed{g: g}
		for _, sk := range n.sorts {
			var s string
			if sk.sel != nil {
				v, err := eng.evalString(sk.sel, n.el, rt{node: first, pos: i + 1, size: total})
				if err != nil {
					return nil, err
				}
				s = v
			} else if first != nil {
				s = first.StringValue()
			}
			k.keys = append(k.keys, s)
		}
		items[i] = k
	}
	sort.SliceStable(items, func(a, b int) bool {
		for ki := range n.sorts {
			cmp := compareKey(items[a].keys[ki], items[b].keys[ki], dataTypes[ki], caseOrders[ki], colls[ki])
			if cmp == 0 {
				continue
			}
			if orders[ki] == "descending" {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	out := make([]*fegGroup, len(items))
	for i, it := range items {
		out[i] = it.g
	}
	return out, nil
}
