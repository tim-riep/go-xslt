package xslt

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// This file implements the XSLT 3.0 accumulator feature:
//
//   - xsl:accumulator (top-level declaration) with @name, @initial-value and a
//     set of child xsl:accumulator-rule elements.
//   - xsl:accumulator-rule with @match (pattern), @phase (start|end, default
//     start) and either @select or a sequence-constructor body computing the
//     new accumulator value from $value (the pre-rule value).
//   - the context functions accumulator-before($name) and
//     accumulator-after($name).
//
// STORAGE / CONCURRENCY NOTE
// --------------------------
// Top-level declarations are compiled with access to the *Stylesheet, but the
// INSTRAPI contract forbids adding a new field to Stylesheet. We therefore keep
// the compiled accumulator definitions in a package-level sync.Map keyed by the
// owning *Stylesheet pointer. This is safe for concurrent transforms (each
// Stylesheet has its own entry, and compilation populates the entry before any
// transform reads it); the only shared structure is the map itself, which
// sync.Map guards. The per-run accumulator VALUES (the computed value at every
// node) are memoized in eng.accCache, which is private engine state.

// acc2Rule is one compiled xsl:accumulator-rule.
type acc2Rule struct {
	match *xpath.Pattern // @match pattern
	end   bool           // true => phase="end" (post-order), false => "start" (pre-order)
	sel   *xpath.Parsed  // @select expression (nil if a body is used)
	body  []instruction  // sequence constructor (used when @select is absent)
	el    *xmltree.Node  // the xsl:accumulator-rule element (namespace context)
}

// acc2Def is one compiled xsl:accumulator declaration.
type acc2Def struct {
	name       xmltree.Name  // expanded accumulator name
	clarkN     string        // clark form of name (cache key prefix)
	initial    *xpath.Parsed // @initial-value expression
	rules      []*acc2Rule
	el         *xmltree.Node // the xsl:accumulator element (namespace context)
	importPrec int
	as         string // @as sequence type ("" = item()*), applied to the initial value and to every rule result
	// streamable is @streamable. This processor never actually streams, but a
	// NON-streamable accumulator may not be read for a node in a document the
	// stylesheet asked to have streamed — XTDE3362 (error-3362a/b).
	streamable bool
}

// acc2valueVar is the QName of the implicit $value variable bound while a rule
// computes the post-update accumulator value.
var acc2valueVar = xmltree.Name{Local: "value"}

// acc2Registry maps a *Stylesheet to its compiled accumulator definitions.
// See the storage note above for why this is a package-level map.
var acc2Registry sync.Map // map[*Stylesheet][]*acc2Def

func init() {
	declRegistry["accumulator"] = func(c *compiler, ss *Stylesheet, el *xmltree.Node) error {
		def, err := acc2compile(c, el)
		if err != nil {
			return err
		}
		var defs []*acc2Def
		if v, ok := acc2Registry.Load(ss); ok {
			defs = v.([]*acc2Def)
		}
		// The XTSE3350 duplicate check is DEFERRED to
		// checkAccumulatorConflicts: two declarations of one name at the same
		// import precedence are only an error when nothing at a HIGHER
		// precedence overrides them (accumulator-027 imports a module that
		// declares "a" twice, and overrides it in the importing module).
		defs = append(defs, def)
		acc2Registry.Store(ss, defs)
		return nil
	}

	// accumulator-before($name) — value of the accumulator as it stands when
	// the (argument or context) node is entered, i.e. after applying all
	// start-phase rules in pre-order up to and including that node and all
	// end-phase rules for nodes that close before it.
	xsltFuncs["accumulator-before"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		return eng.acc2lookup(args, env, false)
	}
	// accumulator-after($name) — value of the accumulator as it stands when the
	// node is left (after its end-phase rule, and after the start-phase rules of
	// the node and its descendants).
	xsltFuncs["accumulator-after"] = func(eng *engine, args []xpath.Object, env *evalEnv) (xpath.Object, bool, error) {
		return eng.acc2lookup(args, env, true)
	}
}

// acc2compile parses an xsl:accumulator declaration into an acc2Def.
func acc2compile(c *compiler, el *xmltree.Node) (*acc2Def, error) {
	nameStr, ok := el.AttrLocal("name")
	if !ok {
		return nil, errAt(el, "xsl:accumulator requires a name attribute")
	}
	name := resolveQName(el, nameStr)
	initial, err := requireExpr(el, "initial-value")
	if err != nil {
		return nil, err
	}
	def := &acc2Def{
		name:       name,
		clarkN:     clark(name.Space, name.Local),
		initial:    initial,
		el:         el,
		importPrec: c.importPrec,
	}
	if as, ok := el.AttrLocal("as"); ok {
		def.as = as
	}
	// @streamable is an XSLT boolean; anything else is XTSE0020 even though
	// this processor never streams (accumulator-037's streamable="No").
	if v, ok := el.AttrLocal("streamable"); ok {
		if !isXSLTBooleanLexical(v) {
			return nil, errAt(el, "err:XTSE0020: streamable=%q is not an XSLT boolean", v)
		}
		def.streamable = isXSLTTrue(strings.TrimSpace(v))
	}
	for _, child := range elementChildren(el) {
		if child.Name.Space != NS || child.Name.Local != "accumulator-rule" {
			continue
		}
		rule, err := acc2compileRule(c, child)
		if err != nil {
			return nil, err
		}
		def.rules = append(def.rules, rule)
	}
	// The content model of xsl:accumulator is (xsl:accumulator-rule+) —
	// at least one rule is required (accumulator-024).
	if len(def.rules) == 0 {
		return nil, errAt(el, "err:XTSE0010: xsl:accumulator requires at least one xsl:accumulator-rule")
	}
	return def, nil
}

// acc2compileRule parses one xsl:accumulator-rule.
func acc2compileRule(c *compiler, el *xmltree.Node) (*acc2Rule, error) {
	matchStr, ok := el.AttrLocal("match")
	if !ok {
		return nil, errAt(el, "xsl:accumulator-rule requires a match attribute")
	}
	pat, err := parsePatternFor(el, matchStr)
	if err != nil {
		return nil, errAt(el, "bad match pattern %q: %v", matchStr, err)
	}
	if err := checkPatternGroupingFuncs(el, pat); err != nil {
		return nil, err
	}
	// $value is bound only while the rule computes the NEW value (@select or
	// the body); it is not in scope in @match, so referring to it there is an
	// unbound-variable static error (accumulator-091, Saxon bug 6095).
	if refsValueVariable(matchStr) {
		return nil, errAt(el, "err:XPST0008: $value is not in scope in xsl:accumulator-rule/@match")
	}
	rule := &acc2Rule{match: pat, el: el}
	if phase, ok := el.AttrLocal("phase"); ok {
		switch phase {
		case "start":
			rule.end = false
		case "end":
			rule.end = true
		default:
			return nil, errAt(el, "xsl:accumulator-rule phase must be 'start' or 'end', got %q", phase)
		}
	}
	if sel, ok := el.AttrLocal("select"); ok {
		p, err := parseXPathFor(el, sel)
		if err != nil {
			return nil, errAt(el, "bad select %q: %v", sel, err)
		}
		rule.sel = p
	} else {
		body, err := c.compileSequence(childNodesForBody(el))
		if err != nil {
			return nil, err
		}
		rule.body = body
	}
	return rule, nil
}

// refsValueVariable reports whether an XPath expression source references the
// variable $value outside string literals and comments. A lexical scan is
// enough here: the only question is whether the name appears as a variable
// reference at all.
func refsValueVariable(src string) bool {
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\'', '"':
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				i++
			}
		case '(':
			if i+1 < len(src) && src[i+1] == ':' {
				depth, j := 0, i
				for j < len(src) {
					if strings.HasPrefix(src[j:], "(:") {
						depth++
						j += 2
						continue
					}
					if strings.HasPrefix(src[j:], ":)") {
						depth--
						j += 2
						if depth == 0 {
							break
						}
						continue
					}
					j++
				}
				i = j - 1
			}
		case '$':
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			if strings.HasPrefix(src[j:], "value") {
				k := j + len("value")
				if k >= len(src) || !isNCNameChar(src[k]) {
					return true
				}
			}
		}
	}
	return false
}

// isNCNameChar reports whether an ASCII byte may continue an NCName.
func isNCNameChar(c byte) bool {
	return c == '-' || c == '.' || c == '_' || (c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// acc2lookup resolves $name, ensures the accumulator has been evaluated over
// the document, and returns the memoized value for the relevant node.
func (eng *engine) acc2lookup(args []xpath.Object, env *evalEnv, after bool) (xpath.Object, bool, error) {
	if len(args) == 0 {
		return xpath.Sequence{}, true, nil
	}
	nameStr := xpath.ToString(args[0])
	name := resolveQName(env.el, nameStr)
	clarkN := clark(name.Space, name.Local)

	def := eng.acc2find(clarkN)
	if def == nil {
		return nil, true, errAt(nil, "err:XTDE3340: no xsl:accumulator named %q", nameStr)
	}
	if err := eng.acc2checkModeApplies(clarkN, nameStr); err != nil {
		return nil, true, err
	}

	// accumulator-before/after read the accumulator at the XPath CONTEXT item,
	// which inside a path step or predicate is not the XSLT current node
	// (accumulator-038/039: "$v/w[1]/accumulator-after('big')" asks about that
	// w, and accumulator-087/088 about each node a path selects, not about the
	// template's current node).
	node := env.focusNode
	if node == nil {
		node = env.current
	}
	if node == nil {
		node = eng.doc
	}
	node = eng.accOriginOf(node)
	if node == nil {
		return nil, true, errAt(nil, "err:XPDY0002: accumulator-before/after has no context item")
	}
	if node.Kind == xmltree.KindAttribute || node.Kind == xmltree.KindNamespace || node.SynthCtx {
		// SynthCtx marks the parentless text node that stands in for a
		// NON-NODE context item (xsl:for-each over "1 to 10") — for which
		// accumulator-before/after is a type error just as for an attribute
		// or namespace node (error-3360a/b).
		return nil, true, errAt(nil, "err:XTTE3360: accumulator-before/after requires a context item that is a node other than an attribute or namespace node")
	}
	// An accumulator applies to whichever tree the queried node actually
	// belongs to — not necessarily the principal source document (a node from
	// a temporary tree built inside a template is legitimate too:
	// result-document-1144's "n" queries accumulator-before() against a
	// $var-constructed <a><b/></a>, not eng.doc, which may not even exist —
	// an initial-template entry point with no <source> at all).
	root := node
	if root == nil {
		root = eng.doc
	} else {
		root = rootOfNode(root)
	}
	if node == nil {
		// Neither a context item nor a principal source document: there is no
		// node to report an accumulator value for (accumulator-038 reaches
		// this through an initial-template entry point with no source).
		// acc2valueFor would dereference the nil node for its cache key.
		return nil, true, errAt(nil, "err:XPDY0002: accumulator-before/after requires a context item")
	}
	// XTDE3362: reading a non-streamable accumulator for a node in a document
	// the stylesheet asked to have STREAMED. This processor always builds the
	// whole tree, but it must still honour the stylesheet's own declaration
	// that the document is streamed (error-3362a/b).
	if !def.streamable && eng.streamedRoots[root] {
		return nil, true, errAt(nil, "err:XTDE3362: accumulator %q is not streamable but the context node is in a streamed document", nameStr)
	}
	if set, restricted := eng.accApplicable[root]; restricted && !set[def.clarkN] {
		return nil, true, errAt(nil, "err:XTDE3362: accumulator %q is not applicable to this document (not in the use-accumulators list that read it)", nameStr)
	}
	if err := eng.acc2ensure(def, root); err != nil {
		return nil, false, err
	}
	// A lookup made from INSIDE this accumulator's own document-order walk
	// (acc2ensure returned early on its busy marker) is answerable only from
	// what the single pass has already determined. A hit is the value XSLT 3.0
	// §18.2 defines — an end-phase rule legitimately reading the pre-descent
	// value its own node already has. A MISS is the genuine circularity
	// XTDE3400 forbids: the value being asked for is the one still being
	// computed, which is what a start-phase rule written as
	// select="$value + accumulator-before('a')" asks for (error-3400a,
	// error-3410a). Answering () there would silently invent a value the
	// stylesheet has no definition for.
	if eng.acc2walking(def, root) {
		if _, ok := eng.accCache[acc2cacheKey(def.clarkN, node, after)]; !ok {
			return nil, true, errAt(def.el, "err:XTDE3400: accumulator %q depends on its own value at the node being processed", nameStr)
		}
	}
	val := eng.acc2valueFor(def, node, after)
	return val, true, nil
}

// acc2walking reports whether this engine is currently inside acc2ensure's
// document-order walk for (def, root) — see acc2ensure's busy marker.
func (eng *engine) acc2walking(def *acc2Def, root *xmltree.Node) bool {
	if eng.accCache == nil {
		return false
	}
	_, busy := eng.accCache[fmt.Sprintf("acc2-busy|%s|%p", def.clarkN, root)]
	return busy
}

// acc2checkModeApplies enforces XTDE3362: an accumulator may only be read in a
// mode to which it applies. A mode says so with xsl:mode/@use-accumulators — a
// whitespace-separated list of accumulator names (already expanded to Clark
// form when the declaration was compiled), or the token #all.
//
// Only a mode that ACTUALLY SPECIFIES the attribute restricts anything: a mode
// that never mentions accumulators places no limit on them, which is what every
// accumulator test without an xsl:mode declaration relies on. An explicitly
// EMPTY list therefore means "no accumulator applies here" and is the case the
// suite pins down (mode-1106b/1106e/1107b: use-accumulators="" on the initial
// mode, versus "counter" / "#all" on its siblings).
func (eng *engine) acc2checkModeApplies(clarkN, nameStr string) error {
	md, ok := eng.sheet.modes[eng.curMode]
	if !ok {
		return nil
	}
	list, declared := md.attrs["use-accumulators"]
	if !declared {
		return nil
	}
	for _, tok := range strings.Fields(list) {
		if tok == "#all" || tok == clarkN {
			return nil
		}
	}
	modeName := eng.curMode
	if modeName == "" {
		modeName = "#unnamed"
	}
	return errAt(nil, "err:XTDE3362: accumulator %q does not apply to mode %q (its use-accumulators list does not include it)", nameStr, modeName)
}

// acc2find returns the compiled accumulator with the given clark name for the
// current stylesheet, or nil. When several modules declare the same
// accumulator name, the one at the HIGHEST import precedence wins
// (accumulator-023/026/027: an importing module's declaration overrides the
// imported one, which is not a duplicate-definition error).
func (eng *engine) acc2find(clarkN string) *acc2Def {
	v, ok := acc2Registry.Load(eng.sheet)
	if !ok {
		return nil
	}
	var best *acc2Def
	for _, d := range v.([]*acc2Def) {
		if d.clarkN == clarkN && (best == nil || d.importPrec >= best.importPrec) {
			best = d
		}
	}
	return best
}

// checkAccumulatorConflicts reports XTSE3350: two xsl:accumulator declarations
// with the same expanded name at the same import precedence — unless a
// declaration of that name exists at a HIGHER precedence, which overrides them
// both and makes the clash harmless (XSLT 3.0 §18.2, accumulator-027).
func checkAccumulatorConflicts(ss *Stylesheet) error {
	v, ok := acc2Registry.Load(ss)
	if !ok {
		return nil
	}
	defs := v.([]*acc2Def)
	maxPrec := map[string]int{}
	for _, d := range defs {
		if p, seen := maxPrec[d.clarkN]; !seen || d.importPrec > p {
			maxPrec[d.clarkN] = d.importPrec
		}
	}
	seen := map[string]*acc2Def{}
	for _, d := range defs {
		if d.importPrec != maxPrec[d.clarkN] {
			continue
		}
		if prev, dup := seen[d.clarkN]; dup && prev.importPrec == d.importPrec {
			name, _ := d.el.AttrLocal("name")
			return errAt(d.el, "err:XTSE3350: duplicate xsl:accumulator %q at the same import precedence", name)
		}
		seen[d.clarkN] = d
	}
	return nil
}

// recordAccOrigin remembers, for every node of a copy that PRESERVES
// accumulator values (fn:copy-of, fn:snapshot, xsl:copy-of with
// copy-accumulators="yes" — XSLT 3.0 §18.2.4), the ORIGINAL node it was copied
// from, so accumulator-before/after asked about a node of the copy answers
// with the original's accumulated value (accumulator-046/047/048/063..072).
//
// INVARIANT: this map is purely additive engine state. A node absent from it
// behaves exactly as before — the accumulator is evaluated over the node's own
// tree — so nothing that does not consult it can be affected.
func (eng *engine) recordAccOrigin(copyNode, orig *xmltree.Node) {
	if copyNode == nil || orig == nil {
		return
	}
	if eng.accOrigin == nil {
		eng.accOrigin = map[*xmltree.Node]*xmltree.Node{}
	}
	eng.accOrigin[copyNode] = orig
	for i, c := range copyNode.Children {
		if i < len(orig.Children) {
			eng.recordAccOrigin(c, orig.Children[i])
		}
	}
	for i, a := range copyNode.Attrs {
		if i < len(orig.Attrs) {
			eng.accOrigin[a] = orig.Attrs[i]
		}
	}
}

// accOriginOf follows a node back to the original it was copied from, if the
// copy preserved accumulator values.
func (eng *engine) accOriginOf(n *xmltree.Node) *xmltree.Node {
	for i := 0; i < 16; i++ { // bounded: a copy of a copy of a copy …
		o, ok := eng.accOrigin[n]
		if !ok || o == n {
			return n
		}
		n = o
	}
	return n
}

// acc2cacheKey builds the eng.accCache key for a (accumulator, node, phase).
//
// Node.Order() is a per-tree document-order index, which is exactly what
// distinguishes the nodes of a STREAMED document too: the incremental reader
// stamps order as it builds (xmltree.StreamReader.stamp), in the same sequence
// a full parse would have, so no change is needed here — leaving order at its
// zero value, on the other hand, would have collapsed every node of the
// document onto one key.
func acc2cacheKey(clarkN string, node *xmltree.Node, after bool) string {
	phase := "b"
	if after {
		phase = "a"
	}
	return "acc2|" + clarkN + "|" + phase + "|" + strconv.Itoa(node.Order())
}

// acc2valueFor returns the memoized accumulator value at node for the given
// phase. acc2ensure must have populated the cache first.
func (eng *engine) acc2valueFor(def *acc2Def, node *xmltree.Node, after bool) xpath.Object {
	eng.depth++
	defer func() { eng.depth-- }()
	if eng.depth > maxTemplateDepth {
		return xpath.Sequence{} // break a circular accumulator/key dependency
	}
	if eng.accCache == nil {
		return xpath.Sequence{}
	}
	if v, ok := eng.accCache[acc2cacheKey(def.clarkN, node, after)]; ok {
		return v.(xpath.Object)
	}
	return xpath.Sequence{}
}

// acc2ensure evaluates the accumulator over the given tree's root (once per
// root) and memoizes the before/after value at every node in it into
// eng.accCache. root is whichever tree the queried node actually belongs to —
// the principal source document in the common case, but a node from a
// constructed temporary tree is equally legitimate (result-document-1144's
// "n": an initial-template entry point with no <source> at all, querying
// accumulator-before() against a $var-constructed tree) and gets its own,
// independently-tracked traversal.
//
// The algorithm performs a single recursive document-order traversal. The
// running value starts at the initial-value. For each node N:
//   - start-phase rules apply on entry (pre-order), updating the running value;
//     the result becomes the value seen by descendants and by accumulator-after
//     of preceding siblings' subtrees;
//   - the "before" value of N (XSLT 3.0 §18.2's PRE-DESCENT value, what
//     fn:accumulator-before reports) is the running value AFTER N's own
//     start-phase rule has been applied — not the value on entry;
//   - children are processed recursively;
//   - end-phase rules apply on exit (post-order);
//   - the "after" value of N is the running value on exit.
func (eng *engine) acc2ensure(def *acc2Def, root *xmltree.Node) error {
	if eng.accCache == nil {
		eng.accCache = map[string]any{}
	}
	doneKey := fmt.Sprintf("acc2-done|%s|%p", def.clarkN, root)
	if _, ok := eng.accCache[doneKey]; ok {
		return nil
	}
	// An accumulator RULE may legitimately call accumulator-before/after for
	// the accumulator it belongs to: XSLT 3.0 §18.2's traversal has already
	// determined the pre-descent value of the node a rule is firing on (it is
	// cached by acc2walk before the children are visited and before the
	// end-phase rule runs), so reading it back is not the cyclic dependency
	// XTDE3400 forbids — that rule is about a value depending on ITSELF.
	//
	// Without this marker the re-entrant lookup finds doneKey unset — it is
	// only written once the whole walk returns — and starts the entire
	// document-order walk again from the root, which re-enters at the same
	// node, and so on until the XTDE0040 depth guard fires. evaluate-046's
	// preprocessor accumulator (an xsl:evaluate whose with-params is
	// accumulator-before of the accumulator being built) is exactly that
	// shape. Marking the walk as in progress makes the re-entrant lookup a
	// plain cache read of whatever the single pass has established so far,
	// which is the value §18.2 defines for it.
	busyKey := fmt.Sprintf("acc2-busy|%s|%p", def.clarkN, root)
	if _, ok := eng.accCache[busyKey]; ok {
		return nil
	}
	eng.accCache[busyKey] = true
	defer delete(eng.accCache, busyKey)
	// acc2walk is an eager document-order walk of the WHOLE tree. That is the
	// right shape for a streamed document — the traversal order is already the
	// streaming one — but not the right time: run against a tree that is being
	// read incrementally it would see only the record currently attached, and
	// forcing the rest into memory to fix that is precisely what streaming
	// exists to avoid.
	//
	// A STREAMABLE accumulator therefore never gets here: stream_acc.go applies
	// its rules as the reader advances and marks it done for this root (the
	// doneKey above) before the body runs, so the lookup is a cache read. What
	// is left to refuse is a NON-streamable one, which XSLT 3.0 §18.2 forbids
	// reading over a streamed document anyway — and which is also why the
	// executor declines to stream a body that reads one at all, rather than
	// letting the refusal surface halfway through a run.
	//
	// The non-streaming path is untouched: strbStreaming is false for every
	// tree that was built in full, including one merely FLAGGED streamable
	// (eng.streamedRoots), which XTDE3362 above still governs.
	if strbStreaming(eng, root) {
		name, _ := def.el.AttrLocal("name")
		return errAt(def.el, "err:XTDE3362: accumulator %q cannot be evaluated over a document being streamed", name)
	}

	init, err := eng.eval(def.initial, def.el, rt{node: root, pos: 1, size: 1})
	if err != nil {
		return err
	}
	if init, err = def.coerce(init); err != nil {
		return err
	}
	// A nil root (no principal source document AND no queried node) has
	// nothing to walk; acc2valueFor's cache-miss already answers every lookup
	// with () safely.
	if root != nil {
		if _, err := eng.acc2walk(def, root, init); err != nil {
			return err
		}
	}
	eng.accCache[doneKey] = true
	return nil
}

// acc2walk recursively processes node with the incoming accumulator value and
// returns the value after the node's subtree (including its end-phase rule).
func (eng *engine) acc2walk(def *acc2Def, node *xmltree.Node, value xpath.Object) (xpath.Object, error) {
	// start-phase rule (pre-order).
	v, err := eng.acc2applyFirst(def, node, value, false)
	if err != nil {
		return nil, err
	}
	value = v

	// before(node) = the pre-descent value: AFTER node's own start-phase rule
	// (accumulator-001: inside the template matching the first <fig>, with rule
	// match="fig" select="$value + 1", accumulator-before('figNr') is 1).
	eng.accCache[acc2cacheKey(def.clarkN, node, false)] = value

	// ATTRIBUTE nodes are NOT visited by accumulator rules: XSLT 3.0 §18.2
	// defines the accumulator traversal over the document, element, text,
	// comment and processing-instruction nodes only, so an accumulator-rule
	// whose pattern matches an attribute is legal but never fires
	// (accumulator-026). accumulator-before/after over an attribute is
	// XTTE3360 in any case, so no value needs caching for one.

	for _, c := range node.Children {
		v, err := eng.acc2walk(def, c, value)
		if err != nil {
			return nil, err
		}
		value = v
	}

	// end-phase rule (post-order).
	v, err = eng.acc2applyFirst(def, node, value, true)
	if err != nil {
		return nil, err
	}
	value = v

	// after(node) = value on exit.
	eng.accCache[acc2cacheKey(def.clarkN, node, true)] = value
	return value, nil
}

// acc2applyFirst applies the accumulator-rule (of the requested phase) that
// governs node, binding $value to the current value, and returns the new
// value. If no rule matches, value is returned unchanged.
//
// XSLT 3.0 §18.2 selects "the xsl:accumulator-rule that is LAST in document
// order" among those whose pattern matches — NOT the first, and not the
// highest-priority one (accumulator-081 declares two rules both matching
// `element` and expects the second to win).
func (eng *engine) acc2applyFirst(def *acc2Def, node *xmltree.Node, value xpath.Object, end bool) (xpath.Object, error) {
	var chosen *acc2Rule
	for _, rule := range def.rules {
		if rule.end != end {
			continue
		}
		ok, err := eng.acc2match(rule, node)
		if err != nil {
			return nil, err
		}
		if ok {
			chosen = rule
		}
	}
	if chosen != nil {
		v, err := eng.acc2eval(def, chosen, node, value)
		if err != nil {
			return nil, err
		}
		return def.coerce(v)
	}
	return value, nil
}

// coerce applies the accumulator's declared @as type to a computed value.
// XSLT 3.0 §18.2 requires the initial value and every rule result to conform
// (accumulator-038: as="xs:integer*" with a rule yielding a string is
// XPTY0004).
func (def *acc2Def) coerce(v xpath.Object) (xpath.Object, error) {
	if def.as == "" {
		return v, nil
	}
	cv, ok := xpath.CoerceToDeclaredTypeCtx(def.as, v, asTypeCtx(def.el))
	if !ok {
		name, _ := def.el.AttrLocal("name")
		return nil, errAt(def.el, "err:XPTY0004: accumulator %q value does not match declared type %q", name, def.as)
	}
	return cv, nil
}

// acc2pureValue evaluates a sequence constructor that is a pure VALUE — one
// xsl:sequence with @select, or an xsl:choose / xsl:if built from such bodies —
// returning (value, true). Anything else returns ok=false so the caller falls
// back to result-tree-fragment construction. It exists because an accumulator
// value is an arbitrary XDM sequence (commonly a map), and routing one through
// a tree would turn it into a node.
func (eng *engine) acc2pureValue(body []instruction, r rt) (xpath.Object, bool, error) {
	if len(body) != 1 {
		return nil, false, nil
	}
	switch b := body[0].(type) {
	case *sequenceInstr:
		if b.sel == nil {
			return nil, false, nil
		}
		v, err := eng.eval(b.sel, b.el, r)
		return v, true, err
	case *ifInstr:
		tv, err := eng.eval(b.test, b.el, r)
		if err != nil {
			return nil, true, err
		}
		if !xpath.ToBool(tv) {
			return xpath.Sequence{}, true, nil
		}
		v, pure, err := eng.acc2pureValue(b.body, r)
		if !pure && err == nil {
			return nil, false, nil
		}
		return v, true, err
	case *chooseInstr:
		for _, w := range b.whens {
			tv, err := eng.eval(w.test, w.el, r)
			if err != nil {
				return nil, true, err
			}
			if xpath.ToBool(tv) {
				v, pure, err := eng.acc2pureValue(w.body, r)
				if !pure && err == nil {
					return nil, false, nil
				}
				return v, true, err
			}
		}
		if b.otherwise == nil {
			return xpath.Sequence{}, true, nil
		}
		v, pure, err := eng.acc2pureValue(b.otherwise, r)
		if !pure && err == nil {
			return nil, false, nil
		}
		return v, true, err
	}
	return nil, false, nil
}

// acc2match reports whether the rule's pattern matches node.
func (eng *engine) acc2match(rule *acc2Rule, node *xmltree.Node) (bool, error) {
	env := &evalEnv{eng: eng, el: rule.el, current: node}
	eng.patternDepth++
	defer func() { eng.patternDepth-- }()
	return rule.match.Match(node, &xpath.Context{Node: node, CtxItem: realItemOf(node), Pos: 1, Size: 1, Vars: env, NS: env, Funcs: env, Resolver: eng.resolver, NoOutputURI: true, DefaultElemNS: xpathDefaultNS(env.el), Now: eng.now, SchemaTypes: schemaTypesFor(rule.el)})
}

// acc2eval computes a rule's new value with $value bound to the current value.
// def is the owning declaration, whose @as decides how a body-valued rule's
// result is collected (see the non-node branch below).
func (eng *engine) acc2eval(def *acc2Def, rule *acc2Rule, node *xmltree.Node, value xpath.Object) (xpath.Object, error) {
	eng.pushScope()
	eng.bindVar(acc2valueVar, value)
	defer eng.popScope()

	r := rt{node: node, pos: 1, size: 1}
	if rule.sel != nil {
		return eng.eval(rule.sel, rule.el, r)
	}
	// A body that is a pure VALUE (an xsl:sequence/@select, possibly wrapped in
	// xsl:choose/xsl:if) yields its value directly: an accumulator value is
	// very often a map or an atomic, which a result-tree fragment cannot carry
	// (accumulator-043/053 thread a map(...) through map:put).
	if v, ok, err := eng.acc2pureValue(rule.body, r); ok || err != nil {
		return v, err
	}
	// A body that is NOT a pure value but whose declared @as asks for
	// something other than nodes is the same shape an @as-typed
	// xsl:variable/xsl:function body has: its instructions each contribute an
	// ITEM, and those items — a map, an array, a typed atomic — are the
	// accumulator's value, not the string value of a result-tree fragment. The
	// RTF path below cannot carry them at all: a plain document collector
	// refuses a map outright (checkSerializableItems' SENR0001), which is what
	// an accumulator declared as="map(xs:QName, item()*)" hits the moment its
	// rule body needs more than one instruction (evaluate-046 binds an
	// xsl:variable first, then returns map:put($value, …)).
	if def.as != "" && !xpath.SeqTypeWantsNodes(def.as) {
		items, err := eng.sequenceFromBody(rule.body, r)
		if err != nil {
			return nil, err
		}
		return xpath.FromItems(items), nil
	}
	// Body: execute into a fragment and return it as a node-set RTF, mirroring
	// evalVarDef's handling of body-valued variables. XSLT 3.0 "temporary
	// output state" (5.7.1) forbids xsl:result-document here too
	// (err:XTDE1480 — result-document-1144).
	frag := &xmltree.Node{Kind: xmltree.KindDocument}
	eng.tempOutputDepth++
	err := eng.execSequence(rule.body, r, frag)
	eng.tempOutputDepth--
	if err != nil {
		return nil, err
	}
	return xpath.NodeSet{frag}, nil
}
