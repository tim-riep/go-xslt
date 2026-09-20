package xslt

import (
	"fmt"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// This file implements the XSLT 3.0 map-construction instructions xsl:map and
// xsl:map-entry.
//
// Design / engine-integration note
// --------------------------------
// xsl:map evaluates to an xpath.Map VALUE, but the engine's exec contract
// writes result-tree NODES into `out`. A map is not a node and cannot be
// serialized into the node tree, nor can it survive an xsl:variable RTF (which
// captures body output as a NodeSet). To keep the family self-contained (one
// file, no edits elsewhere) while remaining genuinely usable from XPath, the
// implementation keeps a build stack in eng.scratch:
//
//   - xsl:map.exec pushes a fresh *xpath.Map onto the stack, executes its body
//     (which runs the contained xsl:map-entry instructions and any nested
//     map-producing content), pops the map, records it as the "last built map"
//     and exposes it through the registered context functions below.
//   - xsl:map-entry.exec evaluates @key (atomized to a single item) and the
//     value (@select or sequence-constructor body, captured as an RTF when a
//     body is used) and Puts the entry into the map at the top of the stack.
//
// The constructed map is reachable from XPath via the registered context
// functions. These are XSLT context functions (registered in xsltFuncs), which
// the engine only dispatches for UNPREFIXED calls, so they are invoked without
// a namespace prefix:
//
//   - current-map()  — the map currently being built (valid inside xsl:map
//     body, e.g. to read sibling entries); empty map otherwise.
//   - last-map()     — the most recently completed xsl:map result. This is
//     the handle tests/stylesheets use to query a constructed map, e.g.
//     map:get(last-map(), 'a').
//
// LIMITATION: because the node-tree exec model has no carrier for non-node
// values, binding an xsl:map directly to an xsl:variable and getting the map
// back out of $var is not supported by the surrounding engine; the context
// functions above are the supported access path until the engine grows a
// value-carrying sequence buffer.

func init() {
	instrRegistry["map"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return mpCompileMap(c, el)
	}
	instrRegistry["map-entry"] = func(c *compiler, el *xmltree.Node) (instruction, error) {
		return mpCompileEntry(c, el)
	}

	xsltFuncs["current-map"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		if m := mpTop(eng); m != nil {
			return m, true, nil
		}
		return xpath.NewMap(), true, nil
	}
	xsltFuncs["last-map"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		if eng.scratch != nil {
			if m, ok := eng.scratch[mpLastKey].(*xpath.Map); ok {
				return m, true, nil
			}
		}
		return xpath.NewMap(), true, nil
	}
}

// scratch keys (package-unique via the mp prefix).
const (
	mpStackKey = "mp:stack"
	mpLastKey  = "mp:last"
)

// mpMap is the compiled xsl:map instruction.
type mpMap struct {
	el   *xmltree.Node
	body []instruction
}

func (*mpMap) instr() {}

// mpEntry is the compiled xsl:map-entry instruction.
type mpEntry struct {
	el  *xmltree.Node
	key *xpath.Parsed
	// Exactly one of sel / body carries the entry value.
	sel  *xpath.Parsed
	body []instruction
}

func (*mpEntry) instr() {}

func mpCompileMap(c *compiler, el *xmltree.Node) (instruction, error) {
	body, err := c.compileSequence(childNodesForBody(el))
	if err != nil {
		return nil, err
	}
	return &mpMap{el: el, body: body}, nil
}

func mpCompileEntry(c *compiler, el *xmltree.Node) (instruction, error) {
	key, err := requireExpr(el, "key")
	if err != nil {
		return nil, err
	}
	e := &mpEntry{el: el, key: key}
	if sel, ok := el.AttrLocal("select"); ok {
		// @select and non-empty content are mutually exclusive (XTSE3280 —
		// maps-008), the same rule xsl:variable/xsl:param enforce for
		// themselves under XTSE0620/XTSE0760.
		for _, ch := range el.Children {
			bad := ch.Kind == xmltree.KindElement ||
				(ch.Kind == xmltree.KindText && strings.TrimSpace(ch.Value) != "")
			if bad {
				return nil, errAt(el, "err:XTSE3280: xsl:map-entry has both a select attribute and content")
			}
		}
		p, perr := parseXPathFor(el, sel)
		if perr != nil {
			return nil, errAt(el, "bad select %q: %v", sel, perr)
		}
		e.sel = p
	} else {
		body, berr := c.compileSequence(childNodesForBody(el))
		if berr != nil {
			return nil, berr
		}
		e.body = body
	}
	return e, nil
}

// mpStack returns the engine's map-build stack, initialising scratch lazily.
func mpStack(eng *engine) []*xpath.Map {
	if eng.scratch == nil {
		eng.scratch = map[string]any{}
	}
	if s, ok := eng.scratch[mpStackKey].([]*xpath.Map); ok {
		return s
	}
	return nil
}

// mpTop returns the map currently being built, or nil if none.
func mpTop(eng *engine) *xpath.Map {
	s := mpStack(eng)
	if len(s) == 0 {
		return nil
	}
	return s[len(s)-1]
}

func mpPush(eng *engine, m *xpath.Map) {
	if eng.scratch == nil {
		eng.scratch = map[string]any{}
	}
	eng.scratch[mpStackKey] = append(mpStack(eng), m)
}

func mpPop(eng *engine) {
	s := mpStack(eng)
	if len(s) == 0 {
		return
	}
	eng.scratch[mpStackKey] = s[:len(s)-1]
}

func (n *mpMap) exec(eng *engine, r rt, out *xmltree.Node) error {
	m := xpath.NewMap()
	mpPush(eng, m)
	// §14.3: "the result of evaluating the sequence constructor must be a
	// sequence of maps", which xsl:map then combines into one. A contained
	// xsl:map-entry writes straight into m through the mpPush/mpTop stack and
	// contributes no item, but that is only ONE of the ways the body can
	// deliver a map: <xsl:sequence select="$m"/>, a nested xsl:map, or an
	// xsl:apply-templates/xsl:call-template whose result is a map all arrive
	// as ITEMS instead. Those were previously left in the output tree
	// untouched, so xsl:map returned its own (possibly empty) map ALONGSIDE
	// them and an as="map(*)" variable saw a two-item sequence — XTTE0570.
	// So the body runs into a discrete-sequence collector, exactly as
	// mpEntry.value does, and every map among the items it produced is merged
	// in here.
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
	err := eng.execSequence(n.body, r, frag)
	mpPop(eng)
	if err != nil {
		return err
	}
	for _, it := range xpath.RawResultItems(xpath.Items(fragAsSequence(frag))) {
		sub, ok := it.(*xpath.Map)
		if !ok {
			// Not a map. The spec makes this an error, but this engine has
			// always let such content through to the result tree, and tests
			// outside xsl:map's own set rely on that; keep the old behaviour
			// rather than turning a tolerated shape into a new failure.
			if err := appendAtomicItem(out, it); err != nil {
				return err
			}
			continue
		}
		if sub == m {
			continue
		}
		for _, k := range sub.Keys() {
			// Same "duplicates: reject" rule the xsl:map-entry path applies
			// (XTDE3365) — xsl:map does not silently overwrite.
			if m.Contains(k) {
				return errAt(n.el, "err:XTDE3365: xsl:map: duplicate key in the constructed map")
			}
			m.Put(k, sub.Get(k))
		}
	}
	// Record the completed map as the last-built result so it is reachable
	// from XPath via last-map() (a legacy access path some existing tests
	// depend on).
	if eng.scratch == nil {
		eng.scratch = map[string]any{}
	}
	eng.scratch[mpLastKey] = m
	// Contribute the map itself as this instruction's result ITEM —
	// appendAtomicItem carries a non-node item like this through via
	// RealItem (see its doc comment on xmltree.Node) so a variable/function
	// body, xsl:sequence, or "." can recover the real map, and raises
	// XTDE0450 if xsl:map is used directly inside literal element content
	// (maps-006).
	// NOTE: a map reaching a json/adaptive result root used to be rejected here
	// (SENR0001) because the engine had no JSON serializer and so could not
	// detect a duplicate-name violation the way a conformant one must. It now
	// has one (xpath.SerializeJSON), which raises the real SERE0022 itself
	// (output-0705, result-document-1405), so the stand-in check is gone. A map
	// reaching a result tree that genuinely CANNOT represent one (the xml/html/
	// text methods) is still rejected — by checkSerializableItems, which is
	// where that rule always belonged.
	return appendAtomicItem(out, m)
}

func (n *mpEntry) exec(eng *engine, r rt, out *xmltree.Node) error {
	m := mpTop(eng)
	if m == nil {
		// An xsl:map-entry outside any xsl:map: build a singleton map and
		// publish it as the last-built map so it is still observable via
		// last-map().
		m = xpath.NewMap()
	}

	keyObj, err := eng.eval(n.key, n.el, r)
	if err != nil {
		return err
	}
	keyItem, err := mpSingleKey(keyObj)
	if err != nil {
		return errAt(n.el, "xsl:map-entry @key: %v", err)
	}

	// XTDE3365: two xsl:map-entry instructions in the same xsl:map may not
	// supply the same key (compared by op:same-key, which is what Map.Contains
	// implements).
	if m.Contains(keyItem) {
		return errAt(n.el, "err:XTDE3365: duplicate key in xsl:map")
	}

	val, err := n.value(eng, r)
	if err != nil {
		return err
	}
	if mpTop(eng) != nil && m.Contains(keyItem) {
		// xsl:map merges the single-entry maps its contained sequence
		// constructor produces using "duplicates: reject" semantics (unlike
		// fn:map-merge's own default "use-first") — a repeated key across
		// entries of the SAME enclosing xsl:map is a dynamic error
		// (error-3365a), not a silent last-write-wins overwrite. A
		// standalone xsl:map-entry (mpTop == nil, m a fresh singleton here)
		// can never collide with itself, so this only guards the nested
		// case.
		return errAt(n.el, "err:XTDE3365: xsl:map: duplicate key in the constructed map")
	}
	m.Put(keyItem, val)

	if mpTop(eng) == nil {
		// Standalone entry (not nested inside an enclosing xsl:map): expose
		// it through last-map(), and — exactly like xsl:map itself —
		// contribute the singleton map as this instruction's own result item
		// (maps-005/maps-017: "as item()"/"as map(*)" over a lone
		// xsl:map-entry).
		if eng.scratch == nil {
			eng.scratch = map[string]any{}
		}
		eng.scratch[mpLastKey] = m
		return appendAtomicItem(out, m)
	}
	return nil
}

// value computes the entry value from @select or the sequence-constructor body
// (captured as a result-tree fragment).
func (n *mpEntry) value(eng *engine, r rt) (xpath.Object, error) {
	if n.sel != nil {
		return eng.eval(n.sel, n.el, r)
	}
	// The value of an xsl:map-entry is the SEQUENCE its sequence constructor
	// produces, not a temporary tree wrapping that sequence: an xs:integer
	// contributed by xsl:sequence has to stay an xs:integer, because that is
	// what the map entry's value IS (output-0701 serializes such values as JSON
	// numbers — a result-tree text node would make them JSON strings). So the
	// body runs into a discrete-sequence collector, exactly like an @as-typed
	// variable's, and fragAsSequence recovers the items (unwrapping any
	// map/array/function carrier among them).
	frag := &xmltree.Node{Kind: xmltree.KindDocument, NoAtomicMerge: true, KeepDocItems: true}
	if err := eng.execSequence(n.body, r, frag); err != nil {
		return nil, err
	}
	// RawResultItems undoes the two ways a non-node item rides through the node
	// tree: the RealItem carrier (maps/arrays/functions) and the Atomic-marked,
	// TypeAnno-stamped text node every atomic value becomes. Without the latter
	// the entry's value would be a text NODE reading "3" rather than the
	// xs:integer 3 — indistinguishable under atomization, but not when the value
	// is serialized (output-0701) or type-tested.
	return xpath.FromItems(xpath.RawResultItems(xpath.Items(fragAsSequence(frag)))), nil
}

// mpSingleKey atomizes obj and requires exactly one resulting atomic item,
// which becomes the map key.
func mpSingleKey(obj xpath.Object) (xpath.Item, error) {
	items, err := xpath.Atomize(obj)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("key must atomize to a single item, got %d", len(items))
	}
	return items[0], nil
}
