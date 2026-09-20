package xslt

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Bounded-memory execution for xsl:source-document streamable="yes".
//
// WHAT THIS BUYS. The existing execApplyTemplates / execForEach evaluate their
// @select to a fully materialized node set before they iterate, so routing a
// streamed document through them would rebuild the whole tree anyway. This file
// therefore owns DISPATCH: it drives xmltree's incremental reader one record at
// a time and hands each completed record — an ordinary, fully materialized
// subtree — to the unmodified evaluator. Peak memory is O(depth + largest
// record + output) instead of O(document).
//
// WHAT IT DELIBERATELY DOES NOT DO. It is not a streamability analyser. The
// static guaranteed-streamability classification (posture/sweep) lives in
// streamability.go's classifyStreamable, called below at strbBuildPlan; this
// file's own strbFindConsumer/strbExprSafe additionally restrict that verdict
// to the narrower shapes this executor can actually route (see strbStep's doc
// comment). Anything either layer cannot prove falls back to today's
// full-materialization path, so the feature can only ever turn a
// slow-but-correct run into a cheap-and-correct one, never into a wrong one.
//
// THE SILENT-TRUNCATION HAZARD. Dropping a processed record leaves its parent
// with fewer children than the document really had, and every read that depends
// on a complete child list fails SILENTLY today: xpath's siblings() returns nil
// when it cannot find a node among its parent's children, docWalk collects
// whatever is still attached, and Node.StringValue simply concatenates less
// text. Nothing errors. The defences here, in order of strength:
//
//  1. Static: strbExprSafe rejects every expression that could navigate out of
//     the record (absolute path, "//", "..", any reverse or following axis,
//     last(), and the whole-document functions key/id/root/doc/snapshot/…), and
//     strbScan.grounded rejects every instruction whose reach it cannot see
//     through.
//     A body that does not pass is not streamed at all.
//  2. Runtime: keyIndexOn (transform.go) and acc2ensure (instr_accumulator.go)
//     refuse outright to walk a root that is being streamed right now — those
//     are the two whole-document walks reachable INDIRECTLY, through a global
//     variable, a stylesheet function or a match pattern, where no scan of the
//     body could have seen them coming. (A STREAMABLE accumulator is the one
//     exception: stream_acc.go computes it incrementally instead, so acc2ensure
//     finds it already done.)
//  3. Diagnostic: StreamReader.Incomplete marks every node a drop truncated.
//
// Residual gap, stated plainly: a read that reaches a truncated ancestor by a
// route defences 1 and 2 do not cover would still be silent, because making it
// loud needs a check inside xmltree.Node.StringValue and xpath/axes.go, which
// this change does not touch. Defence 1 is what closes it in practice — it is
// why the classifier is deliberately stricter than the spec requires.
//
// All private names here are prefixed "strb".

// strbStreamedRuns counts the xsl:source-document instructions actually
// executed by the streaming path. The fallback to full materialization is
// deliberately invisible in the output, which is exactly why a test asserting
// bounded memory needs a way to tell that it measured the path it meant to.
var strbStreamedRuns atomic.Int64

// ---------------------------------------------------------------------------
// classification
// ---------------------------------------------------------------------------

// strbPlan is what the dispatcher needs to run a streamed body: the path that
// selects records, and the rewritten body in which the consuming instruction
// has been replaced by a strbConsumer (which carries the original).
type strbPlan struct {
	path []strbStep
	body []instruction
	// accs are the accumulators the body reads, evaluated incrementally as the
	// stream advances (stream_acc.go). Empty for the common case, which is what
	// keeps the driver's fast path — skipping every subtree off the record path
	// without building it — available.
	accs []*acc2Def
	// forks are the per-branch consumers of a streamed xsl:fork, in branch
	// order. Empty unless the consumer is an xsl:fork.
	forks []instruction
	// pred filters the records: a predicate on the LAST step of the record
	// path, tested against each record once it is materialized. nil when the
	// path has none. predEl carries its namespace context.
	pred   *xpath.Parsed
	predEl *xmltree.Node
}

// strbStep is one step of a record path. The grammar is deliberately tiny —
// fixed-depth child steps only, no "//", and a predicate only on the LAST step
// — because that is the exact subset for which "a record can never contain
// another record" is a STRUCTURAL fact rather than a hope. With "//rec" over
// nested <rec> elements there is no way to process the outer record and its
// inner one without either buffering or double-counting, and the wrong answer
// would be silent. A predicate on an INNER step is excluded for the same
// reason: whether an element on the path qualifies can depend on content
// arriving after the records inside it have already been handed over.
type strbStep struct {
	any   bool // '*'
	space string
	local string
}

// strbRegistry caches the plan per compiled xsl:source-document. Planning
// depends only on the compiled body and its stylesheet, both fixed for the life
// of the instruction, so the result is stable; a package-level sync.Map keeps
// it out of the Stylesheet struct, exactly as instr_accumulator.go does.
var strbRegistry sync.Map // map[*sdSourceDocument]*strbPlan (nil value = "cannot stream")

// strbPlanFor returns the streaming plan for a source-document body, or nil
// when this executor cannot run it.
func strbPlanFor(eng *engine, sd *sdSourceDocument) *strbPlan {
	if v, ok := strbRegistry.Load(sd); ok {
		p, _ := v.(*strbPlan)
		return p
	}
	p := strbBuildPlan(eng, sd)
	strbRegistry.Store(sd, p)
	return p
}

// strbSkipClassify bypasses the guaranteed-streamability gate. It is set ONLY
// by this package's tests, so that a body shape this executor can drive but the
// classifier does not yet approve is still held to the differential guarantee
// (same output as full materialization) instead of being checked only for
// falling back. It is never set in a real run: the classifier is what makes the
// feature safe, and the two layers are deliberately independent.
var strbSkipClassify bool

func strbBuildPlan(eng *engine, sd *sdSourceDocument) *strbPlan {
	ok, err := classifyStreamable(sd.body, sd.el)
	if (err != nil || !ok) && !strbSkipClassify {
		return nil
	}
	cons := strbFindConsumer(sd.body)
	if cons == nil {
		return nil
	}
	sel := strbConsumerSelect(cons)
	if sel == nil {
		return nil
	}
	consEl := strbConsumerEl(cons)
	path, predSrc, pok := strbParseRecordSel(sel.Text(), consEl)
	if !pok {
		return nil
	}
	sc := &strbScan{eng: eng, seen: map[any]bool{}, accNames: map[string]bool{}}
	if !sc.consumerGrounded(cons) {
		return nil
	}
	accs, aok := strbAccNeeded(eng, sc.accNames)
	if !aok {
		return nil
	}
	plan := &strbPlan{path: path, accs: accs}
	if predSrc != "" {
		p, perr := xpath.Parse(predSrc)
		if perr != nil {
			return nil
		}
		plan.pred, plan.predEl = p, consEl
	}
	if fk, ok := cons.(*forkInstr); ok {
		branches, bok := strbForkBranches(fk, path, predSrc)
		if !bok {
			return nil
		}
		plan.forks = branches
	}
	newBody, holder, bok := strbRewrite(sd.body)
	if !bok {
		return nil
	}
	holder.plan = plan
	plan.body = newBody
	return plan
}

// strbFindConsumer returns the single consuming instruction of a body that is
// a chain of literal-element wrappers around exactly one of them, or nil.
func strbFindConsumer(body []instruction) instruction {
	in, ok := strbOnlyItem(body)
	if !ok {
		return nil
	}
	switch c := in.(type) {
	case *litElement:
		if len(c.useSets) > 0 {
			return nil // xsl:use-attribute-sets bodies are outside what is scanned
		}
		if !strbLitElementSafe(c) {
			return nil
		}
		return strbFindConsumer(c.body)
	case *forEach, *applyTemplates, *copyOf, *itrIterate, *fegInstr, *forkInstr:
		return in
	}
	return nil
}

// strbOnlyItem returns the single significant item of a sequence constructor
// (whitespace-only literal text is not significant).
func strbOnlyItem(body []instruction) (instruction, bool) {
	var found instruction
	for _, in := range body {
		if lt, ok := in.(*litText); ok && strings.TrimSpace(lt.text) == "" {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = in
	}
	return found, found != nil
}

// strbRewrite copies the wrapper chain, replacing the consumer with a
// strbConsumer. The copy matters: the compiled body is shared by every
// concurrent transform of this stylesheet and must not be mutated.
func strbRewrite(body []instruction) ([]instruction, *strbConsumer, bool) {
	in, ok := strbOnlyItem(body)
	if !ok {
		return nil, nil, false
	}
	out := make([]instruction, len(body))
	copy(out, body)
	idx := -1
	for i, x := range body {
		if x == in {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, nil, false
	}
	switch c := in.(type) {
	case *litElement:
		inner, holder, iok := strbRewrite(c.body)
		if !iok {
			return nil, nil, false
		}
		cp := *c
		cp.body = inner
		out[idx] = &cp
		return out, holder, true
	case *forEach, *applyTemplates, *copyOf, *itrIterate, *fegInstr, *forkInstr:
		holder := &strbConsumer{inner: in}
		out[idx] = holder
		return out, holder, true
	}
	return nil, nil, false
}

func strbConsumerSelect(in instruction) *xpath.Parsed {
	switch c := in.(type) {
	case *forEach:
		if len(c.sorts) > 0 {
			return nil // sorting needs the whole selection buffered
		}
		return c.sel
	case *applyTemplates:
		if len(c.sorts) > 0 || len(c.params) > 0 {
			return nil
		}
		return c.sel
	case *copyOf:
		return c.sel
	case *itrIterate:
		return c.sel
	case *fegInstr:
		if _, ok := strbGrpStreamable(c); !ok {
			return nil
		}
		return c.sel
	case *forkInstr:
		// A fork has no @select of its own: the record path is its branches',
		// which must all agree (strbForkBranches re-checks that). The first
		// branch's supplies the plan.
		return strbConsumerSelect(strbForkFirst(c))
	}
	return nil
}

// strbForkFirst is the branch consumer a fork's record path is taken from.
func strbForkFirst(c *forkInstr) instruction {
	for _, b := range c.branches {
		if in := strbForkInner(b); in != nil {
			return in
		}
	}
	return c.group
}

func strbConsumerEl(in instruction) *xmltree.Node {
	switch c := in.(type) {
	case *forEach:
		return c.el
	case *applyTemplates:
		return c.el
	case *copyOf:
		return c.el
	case *itrIterate:
		return c.el
	case *fegInstr:
		return c.el
	case *forkInstr:
		// The namespace context for the record path is the branch the path was
		// taken from, not the xsl:fork element.
		if el := strbConsumerEl(strbForkFirst(c)); el != nil {
			return el
		}
		return c.el
	}
	return nil
}

// strbScan carries the state of one classification walk.
type strbScan struct {
	eng *engine
	// seen breaks the recursion through template and named-template calls.
	seen map[any]bool
	// noPosition forbids position() anywhere in the walk. It is set for the
	// closure reachable from a streamed xsl:apply-templates, which dispatches
	// records ONE AT A TIME: applyToNodes then reports position()=1 for every
	// record, where the buffered path reports its real index. xsl:for-each and
	// xsl:iterate are unaffected — this executor feeds them the true running
	// index — so they leave it clear. Once set it stays set for the rest of
	// the walk: a rule reachable both ways would otherwise be accepted on
	// whichever visit came first.
	noPosition bool
	// groupOK permits current-group(), and is set only while scanning the body
	// of a streamed xsl:for-each-group. It is cleared again when the scan
	// descends into a template or named template: the group is not in scope
	// there (XSLT 3.0 §14.3 makes current-group() absent in a called or applied
	// template), so accepting it on that route would stream a body whose
	// buffered twin is an error.
	groupOK bool
	// accNames collects the accumulators the scanned closure reads. Left nil
	// the calls stay forbidden outright; strbBuildPlan supplies a map, so a
	// body that reads one is streamable exactly when every accumulator it names
	// is itself declared streamable.
	accNames map[string]bool
}

func (sc *strbScan) exprOpts() strbExprOpts {
	return strbExprOpts{currentGroup: sc.groupOK, accs: sc.accNames}
}

// consumerGrounded checks everything the consumer will execute per record.
func (sc *strbScan) consumerGrounded(in instruction) bool {
	eng := sc.eng
	switch c := in.(type) {
	case *forEach:
		return sc.grounded(c.body)
	case *fegInstr:
		// The grouping PATTERN is evaluated against each record exactly like
		// any other expression here, so it is scanned like one — unlike a
		// template rule's match pattern, which this executor documents as a
		// gap because the rule is reached indirectly.
		patSrc, ok := strbGrpStreamable(c)
		if !ok || !sc.expr(patSrc) {
			return false
		}
		// @select is the RECORD PATH, checked by strbParsePath rather than
		// scanned as an ordinary expression — it is absolute by nature, which
		// strbExprSafe rejects for every expression evaluated inside a record.
		saved := sc.groupOK
		sc.groupOK = true
		ok = sc.grounded(c.body)
		sc.groupOK = saved
		return ok
	case *forkInstr:
		return sc.forkGrounded(c)
	case *applyTemplates:
		// Every rule the dispatch could reach has to be grounded too, and the
		// mode a rule belongs to is resolved by machinery this file does not
		// own — so the check errs wide: the named mode's rules plus every rule
		// carrying an explicit @mode list at all.
		sc.noPosition = true
		return sc.modeGrounded(eng.resolveMode(c.mode))
	case *copyOf:
		return !c.copyAccumulators
	case *itrIterate:
		for _, p := range c.params {
			if !strbIterParamSafe(p) {
				return false
			}
		}
		return sc.grounded(c.body) && sc.grounded(c.onCompletion)
	}
	return false
}

// strbIterParamSafe checks one xsl:iterate iteration parameter's INITIAL value,
// which is established once before any record is current. See strbFocusFree for
// why it may not read the context item.
func strbIterParamSafe(p *VarDef) bool {
	if p == nil {
		return true
	}
	for _, in := range p.body {
		switch in.(type) {
		case *litText, *textInstr:
		default:
			return false
		}
	}
	return p.sel == nil || strbFocusFree(p.sel.Text())
}

func (sc *strbScan) modeGrounded(mode string) bool {
	// current-group() is absent in an applied template (XSLT 3.0 §14.3), so a
	// rule reached from inside a streamed grouping body may not use it.
	saved := sc.groupOK
	sc.groupOK = false
	defer func() { sc.groupOK = saved }()
	for _, t := range sc.eng.sheet.templates {
		if t.pattern == nil {
			continue // named-only: apply-templates never dispatches to it
		}
		if t.mode != mode && len(t.modeToks) == 0 {
			continue
		}
		if sc.seen[t] {
			continue
		}
		sc.seen[t] = true
		if !sc.grounded(t.body) {
			return false
		}
		for _, p := range t.params {
			if !sc.varDefGrounded(p) {
				return false
			}
		}
	}
	return true
}

// grounded reports whether every instruction in a sequence constructor stays
// inside the record it is executed against. The default is "no": an
// instruction kind this switch does not name is rejected, so adding a new
// instruction to the engine cannot silently widen what gets streamed.
func (sc *strbScan) grounded(instrs []instruction) bool {
	for _, in := range instrs {
		if !sc.instrGrounded(in) {
			return false
		}
	}
	return true
}

func (sc *strbScan) instrGrounded(in instruction) bool {
	switch c := in.(type) {
	case nil:
		return true
	case *litText, *textInstr:
		return true
	case *litElement:
		// Inside a record, reading the context item is the whole point, so the
		// ordinary grounded check applies here — strbLitElementSafe's stricter
		// focus-free rule is only for the WRAPPER elements outside every
		// record (see strbFindConsumer).
		if len(c.useSets) > 0 {
			return false
		}
		for _, a := range c.attrs {
			if !sc.avt(a.value) {
				return false
			}
		}
		return sc.grounded(c.body)
	case *valueOf:
		return sc.sel(c.sel) && sc.avt(c.sep) && sc.grounded(c.body)
	case *ifInstr:
		return sc.sel(c.test) && sc.grounded(c.body)
	case *chooseInstr:
		for _, w := range c.whens {
			if !sc.sel(w.test) || !sc.grounded(w.body) {
				return false
			}
		}
		return sc.grounded(c.otherwise)
	case *forEach:
		if !sc.sel(c.sel) || !sc.grounded(c.body) {
			return false
		}
		return sc.sortsGrounded(c.sorts)
	case *localVar:
		return sc.varDefGrounded(c.def)
	case *attrInstr:
		return sc.avt(c.name) && sc.avt(c.ns) && sc.sel(c.sel) && sc.avt(c.sep) && sc.grounded(c.body)
	case *elemInstr:
		return len(c.useSets) == 0 && sc.avt(c.name) && sc.avt(c.ns) && sc.grounded(c.body)
	case *copyInstr:
		return len(c.useSets) == 0 && sc.sel(c.sel) && sc.grounded(c.body)
	case *copyOf:
		// copy-accumulators would have to read accumulator values over the
		// streamed tree, which acc2ensure refuses (see instr_accumulator.go).
		return !c.copyAccumulators && sc.sel(c.sel)
	case *commentInstr:
		return sc.sel(c.sel) && sc.grounded(c.body)
	case *piInstr:
		return sc.avt(c.name) && sc.sel(c.sel) && sc.grounded(c.body)
	case *sequenceInstr:
		return sc.sel(c.sel) && sc.grounded(c.body)
	case *analyzeString:
		return sc.sel(c.sel) && sc.avt(c.regex) && sc.avt(c.flags) &&
			sc.grounded(c.matching) && sc.grounded(c.nonMatching)
	case *applyTemplates:
		if !sc.sel(c.sel) || !sc.sortsGrounded(c.sorts) {
			return false
		}
		for _, p := range c.params {
			if !sc.varDefGrounded(p) {
				return false
			}
		}
		return sc.modeGrounded(sc.eng.resolveMode(c.mode))
	case *callTemplate:
		for _, p := range c.params {
			if !sc.varDefGrounded(p) {
				return false
			}
		}
		t := sc.eng.sheet.named[c.name]
		if t == nil {
			return false
		}
		if sc.seen[t] {
			return true
		}
		sc.seen[t] = true
		saved := sc.groupOK
		sc.groupOK = false // see modeGrounded: not in scope in a called template
		ok := sc.grounded(t.body)
		sc.groupOK = saved
		return ok
	case *itrIterate:
		for _, p := range c.params {
			if !sc.varDefGrounded(p) {
				return false
			}
		}
		return sc.sel(c.sel) && sc.grounded(c.body) && sc.grounded(c.onCompletion)
	case *itrNextInstr:
		for _, p := range c.params {
			if !sc.varDefGrounded(p) {
				return false
			}
		}
		return true
	case *itrBreakInstr:
		return sc.sel(c.sel) && sc.grounded(c.body)
	}
	return false
}

func (sc *strbScan) sortsGrounded(sorts []sortKey) bool {
	for _, s := range sorts {
		if !sc.sel(s.sel) || !sc.grounded(s.body) {
			return false
		}
		for _, a := range []*avt{s.dataType, s.order, s.caseOrder, s.collation, s.lang} {
			if !sc.avt(a) {
				return false
			}
		}
	}
	return true
}

func (sc *strbScan) varDefGrounded(d *VarDef) bool {
	if d == nil {
		return true
	}
	return sc.sel(d.sel) && sc.grounded(d.body)
}

// strbLitElementSafe checks a WRAPPER literal element — one outside every
// record, whose attributes are evaluated once with the streamed document node
// as the context item. At that moment the document is empty (nothing has been
// read yet), so an attribute that reads content would quietly answer from
// nothing where the buffered path answers from the whole document. Only
// focus-free attributes are allowed.
func strbLitElementSafe(c *litElement) bool {
	for _, a := range c.attrs {
		if a.value == nil {
			continue
		}
		for _, part := range a.value.parts {
			if part.expr != nil && !strbFocusFree(part.expr.Text()) {
				return false
			}
		}
	}
	return true
}

// strbOperatorWords are the XPath keywords a bare name can legitimately be
// without being a name test (which would read the context node's content).
var strbOperatorWords = map[string]bool{
	"or": true, "and": true, "div": true, "idiv": true, "mod": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true, "is": true,
	"to": true, "if": true, "then": true, "else": true, "for": true, "in": true,
	"return": true, "let": true, "some": true, "every": true, "satisfies": true,
	"union": true, "intersect": true, "except": true,
}

// strbFocusFree reports whether an expression provably ignores the context item
// entirely, so that evaluating it against a document that has not been read yet
// gives the same answer as against the fully built one.
//
// It is used only where the streamed and buffered paths would otherwise see
// DIFFERENT context items: a wrapper element's attributes, and xsl:iterate's
// initial parameter values. (The spec reaches the same restriction from the
// other direction — those positions must be motionless to be streamable at
// all.) The test is blunt on purpose: no navigation characters at all, no
// zero-argument call (every one of those defaults to the context item), and
// every bare name must be a variable, a function being called, or an operator
// keyword.
func strbFocusFree(src string) bool {
	clean, ok := strbStripLiterals(src)
	if !ok {
		return false
	}
	// "." also spells a decimal literal and "*" also spells multiplication, so
	// 0.5 and 2*3 are rejected along with the name tests they share a
	// character with. Erring that way costs nothing in the two positions this
	// is used for.
	if strings.ContainsAny(clean, "./@:*") {
		return false
	}
	for i := 0; i < len(clean); i++ {
		if clean[i] != '(' {
			continue
		}
		// Only a CALL's empty argument list counts. A bare "()" is the empty
		// sequence, which is the commonest initial value there is.
		b := i - 1
		for b >= 0 && strbSpaceByte(clean[b]) {
			b--
		}
		if b < 0 || !strbNameByte(clean[b]) {
			continue
		}
		j := i + 1
		for j < len(clean) && strbSpaceByte(clean[j]) {
			j++
		}
		if j < len(clean) && clean[j] == ')' {
			return false
		}
	}
	for i := 0; i < len(clean); {
		if !strbNameStartByte(clean[i]) {
			i++
			continue
		}
		j := i
		for j < len(clean) && strbNameByte(clean[j]) {
			j++
		}
		word := clean[i:j]
		k := j
		for k < len(clean) && strbSpaceByte(clean[k]) {
			k++
		}
		isVar := i > 0 && clean[i-1] == '$'
		// A kind test is spelled exactly like a zero-or-one-argument call, so
		// "count(node())" would otherwise pass the call test while reading the
		// context node's children.
		isCall := k < len(clean) && clean[k] == '(' && !strbKindTests[word]
		if !isVar && !isCall && !strbOperatorWords[word] {
			return false
		}
		i = j
	}
	return true
}

// strbKindTests are the node tests that look like function calls.
var strbKindTests = map[string]bool{
	"node": true, "text": true, "comment": true, "processing-instruction": true,
	"element": true, "attribute": true, "document-node": true, "namespace-node": true,
	"schema-element": true, "schema-attribute": true, "item": true,
}

func strbSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func strbNameStartByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func (sc *strbScan) sel(p *xpath.Parsed) bool {
	return p == nil || sc.expr(p.Text())
}

func (sc *strbScan) avt(a *avt) bool {
	if a == nil {
		return true
	}
	for _, part := range a.parts {
		if part.expr != nil && !sc.expr(part.expr.Text()) {
			return false
		}
	}
	return true
}

// expr applies strbExprSafe plus whatever the walk adds to it.
func (sc *strbScan) expr(src string) bool {
	if !strbExprSafeOpts(src, sc.exprOpts()) {
		return false
	}
	if sc.noPosition {
		clean, ok := strbStripLiterals(src)
		if !ok || strbCalls(clean, "position") {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// expression scanning
// ---------------------------------------------------------------------------

// strbForbiddenAxes are the axes that can leave the record: upward, backward,
// or forward past its end. child/self/attribute/descendant(-or-self)/namespace
// stay inside it and are fine.
var strbForbiddenAxes = []string{
	"parent::", "ancestor::", "ancestor-or-self::",
	"preceding::", "preceding-sibling::", "following::", "following-sibling::",
}

// strbForbiddenCalls are functions whose reach is the whole document (or the
// whole selected sequence) rather than the context node's subtree.
var strbForbiddenCalls = []string{
	// last() is unknowable mid-stream: the length of a forward-only sequence
	// cannot be had without buffering it.
	"last",
	// whole-tree indexes and lookups
	"key", "id", "idref", "element-with-id", "root",
	// NOTE: a few names below are conditionally re-admitted by strbExprOpts —
	// accumulator-before/after when the executor evaluates the accumulator as
	// the stream advances, current-group() inside a streamed grouping body.
	// They stay on this list because the DEFAULT for every other position must
	// remain rejection.
	// other documents, and this one re-read
	"doc", "doc-available", "document", "collection", "uri-collection",
	// snapshot copies the ancestor spine, which is exactly what is truncated
	"snapshot",
	// accumulators over a streamed tree need the streaming accumulator
	// machinery this executor does not implement (acc2ensure refuses them)
	"accumulator-before", "accumulator-after",
	// grouping state: xsl:for-each-group is not streamed here
	"current-group", "current-grouping-key",
	// DTD-scoped lookups on the document node
	"unparsed-entity-uri", "unparsed-entity-public-id",
}

// strbExprOpts widens strbExprSafe for the two positions where a name on the
// forbidden list is legitimately readable from a stream. Both are opt-in per
// scan, never globally: an expression is only as safe as the construct it sits
// in, and the default (an empty strbExprOpts) is the strict rule.
type strbExprOpts struct {
	// currentGroup: inside a streamed xsl:for-each-group body, where the whole
	// group IS held in memory. current-grouping-key() stays forbidden — the two
	// streamable grouping modes form no key at all.
	currentGroup bool
	// accs, when non-nil, permits accumulator-before/after and collects the
	// accumulator names asked for. The driver evaluates exactly those as the
	// stream advances (stream_acc.go); a name it cannot read statically makes
	// the whole expression unsafe.
	accs map[string]bool
}

// strbExprSafe reports whether an XPath expression provably navigates only
// downward from its context item. It is a lexical check — the parsed AST is not
// reachable from this package (xpath.Parsed keeps its root unexported) — so it
// is written to err toward rejection: anything it cannot classify is unsafe.
func strbExprSafe(src string) bool { return strbExprSafeOpts(src, strbExprOpts{}) }

func strbExprSafeOpts(src string, o strbExprOpts) bool {
	clean, ok := strbStripLiterals(src)
	if !ok {
		return false
	}
	if strings.Contains(clean, "//") || strings.Contains(clean, "..") {
		return false
	}
	for _, ax := range strbForbiddenAxes {
		if strings.Contains(clean, ax) {
			return false
		}
	}
	for _, fn := range strbForbiddenCalls {
		if !strbCalls(clean, fn) {
			continue
		}
		switch fn {
		case "current-group":
			if o.currentGroup {
				continue
			}
		case "accumulator-before", "accumulator-after":
			if o.accs != nil && strbAccCallNames(src, clean, fn, o.accs) {
				continue
			}
		}
		return false
	}
	return !strbHasAbsolutePath(clean)
}

// strbAccCallNames records the accumulator every accumulator-before/after call
// in src names, reporting false if any call does not name one readably. The
// name must be a plain string literal holding an unprefixed NCName: a computed
// name is not knowable before the stream starts, and a prefixed one would need
// the calling element's namespace bindings, which this lexical scan does not
// carry.
//
// Call sites are found in clean (where a "accumulator-before(" inside a string
// literal has been blanked out) but the literal ARGUMENT is read from src at
// the same offset — strbStripLiterals blanks in place, so the two strings index
// identically.
func strbAccCallNames(src, clean, fn string, sink map[string]bool) bool {
	for i := 0; ; {
		j := strings.Index(clean[i:], fn)
		if j < 0 {
			return true
		}
		p := i + j
		i = p + len(fn)
		if p > 0 && strbNameByte(clean[p-1]) {
			continue
		}
		k := i
		for k < len(clean) && strbSpaceByte(clean[k]) {
			k++
		}
		if k >= len(clean) || clean[k] != '(' {
			continue
		}
		k++
		for k < len(src) && strbSpaceByte(src[k]) {
			k++
		}
		if k >= len(src) || (src[k] != '\'' && src[k] != '"') {
			return false
		}
		q := src[k]
		k++
		start := k
		for k < len(src) && src[k] != q {
			k++
		}
		if k >= len(src) {
			return false
		}
		name := src[start:k]
		k++
		for k < len(src) && strbSpaceByte(src[k]) {
			k++
		}
		if k >= len(src) || src[k] != ')' || !strbIsNCName(name) {
			return false
		}
		sink[name] = true
		i = k + 1
	}
}

// strbStripLiterals blanks out string literals and (: comments :) so the token
// checks above cannot fire on text that is not expression syntax. It returns
// ok=false for an unterminated literal, which a valid expression never has.
func strbStripLiterals(src string) (string, bool) {
	b := []byte(src)
	for i := 0; i < len(b); {
		switch {
		case b[i] == '\'' || b[i] == '"':
			q := b[i]
			b[i] = ' '
			i++
			for i < len(b) && b[i] != q {
				b[i] = ' '
				i++
			}
			if i >= len(b) {
				return "", false
			}
			b[i] = ' '
			i++
		case b[i] == '(' && i+1 < len(b) && b[i+1] == ':':
			depth := 0
			for i < len(b) {
				if b[i] == '(' && i+1 < len(b) && b[i+1] == ':' {
					depth++
					b[i], b[i+1] = ' ', ' '
					i += 2
					continue
				}
				if b[i] == ':' && i+1 < len(b) && b[i+1] == ')' {
					depth--
					b[i], b[i+1] = ' ', ' '
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				b[i] = ' '
				i++
			}
			if depth != 0 {
				return "", false
			}
		default:
			i++
		}
	}
	return string(b), true
}

// strbCalls reports whether name appears as a function call. The character
// before the name must not continue an NCName, so "mykey(" does not match
// "key" — but "fn:key(" does, because ':' cannot continue one.
func strbCalls(clean, name string) bool {
	for i := 0; ; {
		j := strings.Index(clean[i:], name)
		if j < 0 {
			return false
		}
		p := i + j
		i = p + len(name)
		if p > 0 && strbNameByte(clean[p-1]) {
			continue
		}
		k := i
		for k < len(clean) && (clean[k] == ' ' || clean[k] == '\t' || clean[k] == '\n' || clean[k] == '\r') {
			k++
		}
		if k < len(clean) && clean[k] == '(' {
			return true
		}
	}
}

func strbNameByte(c byte) bool {
	return c == '-' || c == '.' || c == '_' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// strbHasAbsolutePath reports whether a '/' starts a path at the document root
// rather than separating two steps. A step separator always follows a name, a
// ')', a ']', a '.', a '*' or an '@'; anything else (start of expression, an
// operator, an opening bracket, a comma) means the '/' is anchoring at the root
// — which for a streamed document reaches a truncated ancestor.
func strbHasAbsolutePath(clean string) bool {
	prev := byte(0)
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '/':
			if prev == 0 {
				return true
			}
			if !strbNameByte(prev) && prev != ')' && prev != ']' && prev != '*' && prev != '@' {
				return true
			}
		}
		prev = c
	}
	return false
}

// ---------------------------------------------------------------------------
// record paths
// ---------------------------------------------------------------------------

// strbParsePath compiles a record-selecting path, discarding any record filter.
// Callers that must honour the filter use strbParseRecordSel.
func strbParsePath(src string, el *xmltree.Node) ([]strbStep, bool) {
	steps, _, ok := strbParseRecordSel(src, el)
	return steps, ok
}

// strbParseRecordSel compiles a record-selecting path plus, if the last step
// carries one, the source of the predicate that filters the records. The
// context item of an xsl:source-document body is the document node, so an
// absolute and a relative path mean the same thing here and the leading '/' is
// simply dropped.
func strbParseRecordSel(src string, el *xmltree.Node) ([]strbStep, string, bool) {
	s := strings.TrimSpace(src)
	if s == "" || strings.Contains(s, "//") {
		return nil, "", false
	}
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimPrefix(s, "./")
	// A predicate on the LAST step is a filter over records, which this
	// executor can honour by testing each record once it is materialized. One
	// on an inner step is not: it would decide whether an ELEMENT ON THE PATH
	// qualifies, and that answer can depend on content arriving after the
	// records inside it have already been processed.
	var pred string
	if i := strings.IndexByte(s, '['); i >= 0 {
		if !strings.HasSuffix(s, "]") || strings.LastIndexByte(s, '/') > i {
			return nil, "", false
		}
		if strings.Count(s, "[") != 1 || strings.Count(s, "]") != 1 {
			return nil, "", false // nested or consecutive predicates: not analysed
		}
		pred, s = s[i+1:len(s)-1], s[:i]
		if !strbPredicateSafe(pred) {
			return nil, "", false
		}
	}
	var out []strbStep
	for _, seg := range strings.Split(s, "/") {
		seg = strings.TrimSpace(seg)
		seg = strings.TrimPrefix(seg, "child::")
		if seg == "" || strings.ContainsAny(seg, "[]()@$,|*'\"") && seg != "*" {
			return nil, "", false
		}
		if seg == "*" {
			out = append(out, strbStep{any: true})
			continue
		}
		prefix, local := "", seg
		if i := strings.IndexByte(seg, ':'); i >= 0 {
			prefix, local = seg[:i], seg[i+1:]
		}
		if local == "" || !strbIsNCName(local) || (prefix != "" && !strbIsNCName(prefix)) {
			return nil, "", false
		}
		space := ""
		if prefix != "" {
			uri, ok := el.LookupPrefix(prefix)
			if !ok {
				return nil, "", false
			}
			space = uri
		} else {
			space = xpathDefaultNS(el)
		}
		out = append(out, strbStep{space: space, local: local})
	}
	if len(out) == 0 {
		return nil, "", false
	}
	return out, pred, true
}

// strbBoolPredWords are the names whose presence makes a predicate plainly a
// BOOLEAN test rather than a positional one.
var strbBoolPredWords = map[string]bool{
	"and": true, "or": true, "not": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
	"exists": true, "empty": true, "boolean": true, "true": true, "false": true,
	"contains": true, "starts-with": true, "ends-with": true, "matches": true,
}

// strbPredicateSafe reports whether a record-path predicate can be applied by
// testing each record on its own, once, after it is materialized.
//
// Two things have to hold. It must not navigate outside the record
// (strbExprSafe), and it must be a BOOLEAN test rather than a positional one:
// XPath makes a numeric predicate select by position WITHIN THE PARENT, a
// quantity a reader that sees records one at a time across many parents does
// not have. position() and last() are the explicit spellings of that and are
// already forbidden; the rest is caught by requiring the predicate to look
// unambiguously boolean — it carries a comparison or logical operator, names
// one of the boolean built-ins, or is a bare existence test such as [x] or
// [@id]. Everything else, "[1]" and "[$n]" included, falls back.
func strbPredicateSafe(src string) bool {
	clean, ok := strbStripLiterals(src)
	if !ok {
		return false
	}
	s := strings.TrimSpace(clean)
	if s == "" || !strbExprSafe(src) || strbCalls(clean, "position") {
		return false
	}
	if strings.ContainsAny(s, "=<>!") {
		return true
	}
	bare := true
	for i := 0; i < len(s); {
		if !strbNameStartByte(s[i]) {
			if !strings.ContainsRune(" \t\r\n@/:.*", rune(s[i])) {
				bare = false
			}
			i++
			continue
		}
		j := i
		for j < len(s) && strbNameByte(s[j]) {
			j++
		}
		if strbBoolPredWords[s[i:j]] {
			return true
		}
		i = j
	}
	// A bare relative path or attribute test — [x], [@id], [a/b] — is an
	// existence test, which is boolean.
	return bare
}

func strbIsNCName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 0 && (c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
		if !strbNameByte(c) {
			return false
		}
	}
	return s != ""
}

func strbStepMatches(st strbStep, n *xmltree.Node) bool {
	if n.Kind != xmltree.KindElement {
		return false
	}
	if st.any {
		return true
	}
	return n.Name.Local == st.local && n.Name.Space == st.space
}

// ---------------------------------------------------------------------------
// the consumer instruction and the driver
// ---------------------------------------------------------------------------

// strbConsumer stands in for the consuming instruction inside a rewritten
// streamed body. It carries no mutable state — the live reader lives on the
// engine — so one instance is safe for concurrent transforms.
type strbConsumer struct {
	inner instruction
	plan  *strbPlan
}

func (*strbConsumer) instr() {}

func (n *strbConsumer) exec(eng *engine, r rt, out *xmltree.Node) error {
	run := strbActiveRun(eng)
	if run == nil {
		// Only a streamed xsl:source-document ever executes a rewritten body,
		// so this cannot happen — but silently running inner against the empty
		// streamed document node would produce a plausible, wrong, empty
		// result, which is the one outcome worth erroring over.
		return errAt(strbConsumerEl(n.inner), "internal: streaming consumer reached outside a streamed source-document")
	}
	if run.plan != n.plan {
		// The run's record path and this consumer's must be the same object:
		// driving one consumer with another's path would select the wrong
		// elements and say nothing about it.
		return errAt(strbConsumerEl(n.inner), "internal: streaming consumer does not belong to the active stream")
	}
	return run.drive(eng, n.inner, out)
}

// strbRun is the live state of one streamed source-document.
type strbRun struct {
	rd   *xmltree.StreamReader
	plan *strbPlan
	// onPath is how much of the record path the currently open element chain
	// matches; a record is an element entered at depth len(path) with the whole
	// prefix already matched.
	onPath int
	idx    int // records processed so far (1-based position of the current one)
	// acc is the live accumulator state, nil when the body reads none.
	acc *strbAccSet
	// pendLeaf defers a completed text/comment/PI node by one event while
	// accumulators are live. Character data arriving in several decoder chunks
	// COALESCES into a text node already reported (StreamReader.charData emits
	// no second event for it), so a rule matching text() applied the moment the
	// first chunk lands would see a value the document never had. One event of
	// lag is enough: Advance consumes every continuation before it returns, so
	// a leaf that is still pending when the next event arrives is final.
	pendLeaf *xmltree.Node
}

const strbRunKey = "strb.run"
const strbLiveKey = "strb.live"

func strbActiveRun(eng *engine) *strbRun {
	if eng.scratch == nil {
		return nil
	}
	r, _ := eng.scratch[strbRunKey].(*strbRun)
	return r
}

// strbPushRun installs the live run and marks its root as being streamed,
// returning the undo. The mark is what keyIndexOn and acc2ensure consult.
func strbPushRun(eng *engine, run *strbRun) func() {
	if eng.scratch == nil {
		eng.scratch = map[string]any{}
	}
	prev := eng.scratch[strbRunKey]
	eng.scratch[strbRunKey] = run
	live, _ := eng.scratch[strbLiveKey].(map[*xmltree.Node]bool)
	if live == nil {
		live = map[*xmltree.Node]bool{}
		eng.scratch[strbLiveKey] = live
	}
	root := run.rd.Doc()
	live[root] = true
	return func() {
		eng.scratch[strbRunKey] = prev
		delete(live, root)
	}
}

// strbStreaming reports whether root is a document this engine is reading
// incrementally right now, and therefore holds only a fraction of at any
// instant. Any whole-document operation over such a root must refuse rather
// than answer from what happens to be attached.
func strbStreaming(eng *engine, root *xmltree.Node) bool {
	if eng == nil || eng.scratch == nil || root == nil {
		return false
	}
	live, _ := eng.scratch[strbLiveKey].(map[*xmltree.Node]bool)
	return live[root]
}

// strbDot is the "." expression a per-record copy-of is rewritten to use.
var strbDot = func() *xpath.Parsed {
	p, err := xpath.Parse(".")
	if err != nil {
		panic("xslt: cannot parse \".\": " + err.Error())
	}
	return p
}()

// drive is the streaming dispatch loop. It advances the reader, materializes
// exactly one record at a time, runs the per-record work against it with the
// ordinary evaluator, and then drops it. Everything that is not a record is
// skipped before it is ever built (SkipSubtree) or dropped as soon as it
// completes, which is what makes peak memory independent of document size.
func (run *strbRun) drive(eng *engine, inner instruction, out *xmltree.Node) error {
	path := run.plan.path

	// The current template rule is absent inside xsl:for-each / xsl:iterate /
	// xsl:for-each-group (XSLT 3.0 §6.7), exactly as execForEach arranges for
	// the buffered path. An xsl:fork does not itself change it — each of its
	// branches does or does not, so the suppression moves inside the branch
	// loop (see process).
	defer strbSuppressRule(eng, inner)()

	st, err := strbNewDispatch(eng, run.plan, inner)
	if err != nil {
		return err
	}
	if len(run.plan.accs) > 0 {
		acc, err := strbAccStart(eng, run.plan.accs, run.rd.Doc())
		if err != nil {
			return err
		}
		run.acc = acc
		st.setRelease(run.acc.forget)
	}

	for {
		ev, err := run.rd.Advance()
		if err != nil {
			return errAt(strbConsumerEl(inner), "err:FODC0002: %v", err)
		}
		if err := run.flushPending(); err != nil {
			return err
		}
		switch ev.Kind {
		case xmltree.StreamEOF:
			if run.acc != nil {
				if err := run.acc.exit(run.rd.Doc()); err != nil {
					return err
				}
			}
			return st.complete(eng, out)

		case xmltree.StreamEnter:
			if run.acc != nil {
				if err := run.acc.enter(ev.Node); err != nil {
					return err
				}
			}
			d := ev.Depth
			if d <= len(path) && run.onPath == d-1 && strbStepMatches(path[d-1], ev.Node) {
				run.onPath = d
				if d < len(path) {
					continue
				}
				// A record: read it to its end tag, run the body, drop it.
				if err := run.captureTo(ev.Node); err != nil {
					return errAt(strbConsumerEl(inner), "err:FODC0002: %v", err)
				}
				run.onPath = d - 1
				strbStripRecord(eng, ev.Node)
				keep, err := run.keepRecord(eng, ev.Node)
				if err != nil {
					return err
				}
				if !keep {
					// A filtered-out record never existed as far as the body is
					// concerned — in particular it does not advance position().
					// Its accumulator rules have already fired, which is right:
					// an accumulator traverses the document, not the selection.
					// It is released unconditionally, not through release(): no
					// consumer can be holding a record it was never given, not
					// even a grouping one.
					run.acc.forget(ev.Node)
					run.rd.Drop(ev.Node)
					continue
				}
				run.idx++
				done, err := run.process(eng, st, ev.Node, out)
				run.release(ev.Node, st)
				if err != nil {
					return err
				}
				if done {
					if st.fork != nil {
						// Every branch has stopped, but their buffered results
						// still have to reach the output.
						return st.complete(eng, out)
					}
					return nil // xsl:break already wrote its result
				}
				continue
			}
			if run.acc == nil {
				// Off the record path: nothing inside can be a record, and
				// skipping is the only way to pass it without building it.
				if err := run.rd.SkipSubtree(); err != nil {
					return errAt(strbConsumerEl(inner), "err:FODC0002: %v", err)
				}
				continue
			}
			// With accumulators live the subtree cannot be skipped — a rule may
			// match inside it — so it is walked instead, each node dropped as it
			// completes. Retention stays O(depth), not O(subtree).

		case xmltree.StreamExit:
			if run.acc != nil {
				if err := run.acc.exit(ev.Node); err != nil {
					return err
				}
				run.acc.forget(ev.Node)
			}
			if run.onPath == ev.Depth {
				run.onPath = ev.Depth - 1
			}
			run.rd.Drop(ev.Node)

		case xmltree.StreamLeaf:
			// Text, comments and PIs outside every record are unreachable from
			// a grounded body (strbExprSafe forbids navigating to them), so
			// they are dropped the moment they complete — except while
			// accumulators run, which need the completed value first.
			if run.acc == nil {
				run.rd.Drop(ev.Node)
				continue
			}
			run.pendLeaf = ev.Node
		}
	}
}

// keepRecord applies the record-path predicate, if the path has one. The
// record is fully built by now, so the test sees exactly what it would have
// seen over a materialized document — the predicate cannot read anything
// outside the record (strbPredicateSafe) and cannot be positional, so context
// position and size are immaterial and are reported as 1.
func (run *strbRun) keepRecord(eng *engine, rec *xmltree.Node) (bool, error) {
	if run.plan.pred == nil {
		return true, nil
	}
	v, err := eng.eval(run.plan.pred, run.plan.predEl, rt{node: rec, pos: 1, size: 1})
	if err != nil {
		return false, err
	}
	return xpath.ToBool(v), nil
}

// flushPending applies the deferred accumulator rules of a leaf whose value is
// now known to be final, and drops it.
func (run *strbRun) flushPending() error {
	n := run.pendLeaf
	if n == nil {
		return nil
	}
	run.pendLeaf = nil
	if err := run.acc.enter(n); err != nil {
		return err
	}
	if err := run.acc.exit(n); err != nil {
		return err
	}
	run.acc.forget(n)
	run.rd.Drop(n)
	return nil
}

// release lets go of a processed record. A grouping consumer still holds it —
// current-group() hands the whole group to the body — so its accumulator values
// have to outlive this point too; strbGrpState releases both when the group
// closes.
func (run *strbRun) release(rec *xmltree.Node, st *strbDispatch) {
	if !st.retainsRecords() {
		run.acc.forget(rec)
	}
	run.rd.Drop(rec)
}

// captureTo advances until rec's own end tag, leaving its subtree fully built.
// Nothing inside is dropped: the body is about to run against the whole record.
func (run *strbRun) captureTo(rec *xmltree.Node) error {
	nested := 0
	for {
		ev, err := run.rd.Advance()
		if err != nil {
			return err
		}
		// A leaf inside the record is held back one event for the same reason
		// as outside it (see strbRun.pendLeaf) — here only its accumulator
		// rules are deferred, since the node itself stays attached.
		if run.acc != nil && run.pendLeaf != nil {
			n := run.pendLeaf
			run.pendLeaf = nil
			if err := run.acc.enter(n); err != nil {
				return err
			}
			if err := run.acc.exit(n); err != nil {
				return err
			}
		}
		switch ev.Kind {
		case xmltree.StreamEOF:
			return errors.New("unexpected end of document inside a record")
		case xmltree.StreamEnter:
			nested++
			if run.acc != nil {
				if err := run.acc.enter(ev.Node); err != nil {
					return err
				}
			}
		case xmltree.StreamLeaf:
			if run.acc != nil {
				run.pendLeaf = ev.Node
			}
		case xmltree.StreamExit:
			if run.acc != nil {
				if err := run.acc.exit(ev.Node); err != nil {
					return err
				}
			}
			if nested == 0 {
				if ev.Node != rec {
					return errors.New("internal: record end tag mismatch")
				}
				return nil
			}
			nested--
		}
	}
}

// strbStripRecord applies xsl:strip-space to a freshly completed record, the
// per-subtree equivalent of the whole-document pass ResolveDoc runs. The
// inherited xml:space state has to come from the (retained) ancestor spine.
func strbStripRecord(eng *engine, rec *xmltree.Node) {
	if eng.sheet == nil || len(eng.sheet.strip) == 0 {
		return
	}
	preserve := false
	var chain []*xmltree.Node
	for p := rec.Parent; p != nil; p = p.Parent {
		chain = append(chain, p)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if v, ok := chain[i].Attr("http://www.w3.org/XML/1998/namespace", "space"); ok {
			preserve = v == "preserve"
		}
	}
	eng.sheet.stripWalk(rec, preserve)
}

// strbDispatch is the per-record state of the consumer being driven: one
// consumer in the ordinary case, one per branch for an xsl:fork.
type strbDispatch struct {
	inner instruction
	iter  *strbIterState
	grp   *strbGrpState
	fork  *strbForkState
}

func strbNewDispatch(eng *engine, plan *strbPlan, inner instruction) (*strbDispatch, error) {
	st := &strbDispatch{inner: inner}
	switch c := inner.(type) {
	case *itrIterate:
		it, err := strbNewIterState(eng, c)
		if err != nil {
			return nil, err
		}
		st.iter = it
	case *fegInstr:
		st.grp = strbNewGrpState(c)
	case *forkInstr:
		fk, err := strbNewForkState(eng, plan.forks)
		if err != nil {
			return nil, err
		}
		st.fork = fk
	}
	return st, nil
}

// strbSuppressRule makes the current template rule absent for the duration of
// one consumer's work, and returns the undo. xsl:apply-templates sets its own
// rule; xsl:fork sets none of its own (its branches do).
func strbSuppressRule(eng *engine, in instruction) func() {
	switch in.(type) {
	case *applyTemplates, *forkInstr:
		return func() {}
	}
	saved := eng.applyStack
	eng.applyStack = nil
	return func() { eng.applyStack = saved }
}

// setRelease tells every grouping state how to let go of a closed group's
// accumulator values.
func (st *strbDispatch) setRelease(f func(*xmltree.Node)) {
	if st.grp != nil {
		st.grp.release = f
	}
	if st.fork != nil {
		for _, g := range st.fork.grps {
			if g != nil {
				g.release = f
			}
		}
	}
}

// retainsRecords reports whether the consumer holds records past the point
// where they were processed, so the driver knows not to release their
// accumulator values yet.
func (st *strbDispatch) retainsRecords() bool {
	if st.grp != nil {
		return true
	}
	if st.fork != nil {
		for _, g := range st.fork.grps {
			if g != nil {
				return true
			}
		}
	}
	return false
}

// complete runs whatever the consumer owes at end of stream: xsl:iterate's
// xsl:on-completion, a grouping's last open group, a fork's buffered branches.
func (st *strbDispatch) complete(eng *engine, out *xmltree.Node) error {
	switch {
	case st.iter != nil:
		return st.iter.complete(eng, out)
	case st.grp != nil:
		return st.grp.complete(eng, out)
	case st.fork != nil:
		for i := range st.fork.inner {
			if err := st.fork.finishBranch(eng, i); err != nil {
				return err
			}
			strbAppendFrag(out, st.fork.bufs[i])
		}
	}
	return nil
}

// finishBranch runs one fork branch's end-of-stream work into its own buffer,
// under the same current-template-rule suppression its per-record work had.
func (s *strbForkState) finishBranch(eng *engine, i int) error {
	// A branch that broke skips its xsl:on-completion, exactly as a buffered
	// xsl:break does.
	if s.done[i] {
		return nil
	}
	undo := strbSuppressRule(eng, s.inner[i])
	defer undo()
	switch {
	case s.iters[i] != nil:
		return s.iters[i].complete(eng, s.bufs[i])
	case s.grps[i] != nil:
		return s.grps[i].complete(eng, s.bufs[i])
	}
	return nil
}

// process runs the per-record work. done=true means the consumer asked to stop
// (xsl:break).
func (run *strbRun) process(eng *engine, st *strbDispatch, rec, out *xmltree.Node) (bool, error) {
	// A record is handed over whole or not at all. If the driver had already
	// dropped something out of it, every expression over it would quietly see
	// less than the document held — the exact failure mode this executor exists
	// to avoid, so it is asserted rather than assumed.
	if run.rd.Incomplete(rec) {
		return false, errAt(strbConsumerEl(st.inner), "internal: streamed record was truncated before it was processed")
	}
	if st.fork != nil {
		// Every branch sees the SAME record before the driver moves on, each
		// writing into its own buffer so the branch results still concatenate
		// in branch order (see stream_fork.go).
		allDone := true
		for i, in := range st.fork.inner {
			if st.fork.done[i] {
				continue
			}
			undo := strbSuppressRule(eng, in)
			done, err := run.processOne(eng, in, st.fork.iters[i], st.fork.grps[i], rec, st.fork.bufs[i])
			undo()
			if err != nil {
				return false, err
			}
			st.fork.done[i] = done
			allDone = allDone && done
		}
		// The driver stops only when every branch has: a fork's result needs
		// the branches that are still running to see the rest of the stream.
		return allDone, nil
	}
	return run.processOne(eng, st.inner, st.iter, st.grp, rec, out)
}

func (run *strbRun) processOne(eng *engine, inner instruction, it *strbIterState, grp *strbGrpState, rec, out *xmltree.Node) (bool, error) {
	// size is the record count SO FAR, never the real total: the length of a
	// forward-only sequence is unknown mid-stream. It is unobservable because
	// strbExprSafe rejects last(); position() reads run.idx, which is exact.
	r := rt{node: rec, pos: run.idx, size: run.idx}
	switch c := inner.(type) {
	case *forEach:
		return false, eng.forEachIteration(c.body, r, out)
	case *applyTemplates:
		return false, eng.applyToNodes(xpath.NodeSet{rec}, eng.resolveMode(c.mode), out, nil, r)
	case *copyOf:
		// Reuse the real xsl:copy-of, pointed at the record itself: its
		// namespace-fixup and base-uri handling are not worth re-deriving.
		cp := &copyOf{sel: strbDot, el: c.el, noCopyNS: c.noCopyNS}
		return false, eng.execInstr(cp, r, out)
	case *itrIterate:
		return it.step(eng, r, out)
	case *fegInstr:
		return false, grp.step(eng, rec, out)
	}
	return false, errAt(strbConsumerEl(inner), "internal: unsupported streaming consumer")
}

// ---------------------------------------------------------------------------
// xsl:iterate over a stream
// ---------------------------------------------------------------------------

// strbIterState carries xsl:iterate's iteration parameters across records. It
// mirrors itrIterate.exec's loop; the only difference is that the items arrive
// one at a time from the reader instead of from a materialized node list, which
// is precisely what lets an aggregating stylesheet run in constant memory.
type strbIterState struct {
	it  *itrIterate
	cur map[string]xpath.Object
}

func strbNewIterState(eng *engine, it *itrIterate) (*strbIterState, error) {
	cur := make(map[string]xpath.Object, len(it.params))
	for _, p := range it.params {
		if p.requiredParam {
			return nil, errAt(p.el, "err:XTDE0700: required parameter $%s was not supplied", p.name.Local)
		}
		// Initial values are established once, in the outer context — before
		// any record is current (rt.noFocus, since the streamed document node
		// holds nothing meaningful at this point).
		v, err := eng.evalVarDef(p, rt{noFocus: true})
		if err != nil {
			return nil, err
		}
		cur[clark(p.name.Space, p.name.Local)] = v
	}
	return &strbIterState{it: it, cur: cur}, nil
}

func (s *strbIterState) step(eng *engine, r rt, out *xmltree.Node) (bool, error) {
	eng.pushScope()
	for _, p := range s.it.params {
		eng.bindVar(p.name, s.cur[clark(p.name.Space, p.name.Local)])
	}
	err := eng.execSequence(s.it.body, r, out)
	eng.popScope()
	if err == nil {
		return false, nil
	}
	var nx itrNext
	if errors.As(err, &nx) {
		for _, p := range s.it.params {
			key := clark(p.name.Space, p.name.Local)
			nv, ok := nx.params[key]
			if !ok {
				continue
			}
			if p.as != "" {
				if cv, cok := xpath.CoerceToDeclaredTypeCtx(p.as, nv, asTypeCtx(p.el)); cok {
					nv = cv
				}
			}
			s.cur[key] = nv
		}
		return false, nil
	}
	var br itrBreak
	if errors.As(err, &br) {
		return true, nil // xsl:break already wrote its result
	}
	return false, err
}

func (s *strbIterState) complete(eng *engine, out *xmltree.Node) error {
	if len(s.it.onCompletion) == 0 {
		return nil
	}
	eng.pushScope()
	for _, p := range s.it.params {
		eng.bindVar(p.name, s.cur[clark(p.name.Space, p.name.Local)])
	}
	err := eng.execSequence(s.it.onCompletion, rt{noFocus: true}, out)
	eng.popScope()
	return err
}

// ---------------------------------------------------------------------------
// entry point used by instr_sourcedoc.go
// ---------------------------------------------------------------------------

// strbTryStream runs an xsl:source-document body against href incrementally.
// It reports handled=false — having produced no output and consumed nothing —
// whenever anything at all stands in the way (no streaming resolver, an
// unreadable or unstreamable document, a body this executor cannot prove safe),
// so the caller can fall back to full materialization without a trace.
func strbTryStream(eng *engine, sd *sdSourceDocument, href string, out *xmltree.Node) (handled bool, err error) {
	if strings.ContainsRune(href, '#') {
		// A fragment identifier addresses a node by ID, which needs the whole
		// document indexed before the body can start.
		return false, nil
	}
	sr, ok := eng.resolver.(streamResolver)
	if !ok {
		return false, nil
	}
	plan := strbPlanFor(eng, sd)
	if plan == nil {
		return false, nil
	}
	rc, ok := sr.ResolveStream(href)
	if !ok {
		return false, nil
	}
	defer rc.Close()
	rd, err := xmltree.NewStreamReader(rc)
	if err != nil {
		// Every construction failure is a routing decision, not a verdict:
		// ErrStreamUnsupported means the full parser handles this document
		// better, and a malformed prolog means it will report the failure —
		// with its own line and column, which this reader does not track.
		// Either way nothing has been written, so falling back is invisible.
		return false, nil
	}
	doc := rd.Doc()
	if fr, okr := eng.resolver.(*fileResolver); okr {
		doc.Base = fileURI(fr.path(href))
	}
	strbStreamedRuns.Add(1)
	run := &strbRun{rd: rd, plan: plan}
	undo := strbPushRun(eng, run)
	defer undo()
	defer eng.restrictAccumulators(doc, sd.el)()

	eng.pushScope()
	defer eng.popScope()
	if err := eng.execSequence(plan.body, rt{node: doc, pos: 1, size: 1}, out); err != nil {
		return true, err
	}
	return true, nil
}
