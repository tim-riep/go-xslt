package xslt

// Guaranteed-streamability analysis at the instruction level (XSLT 3.0
// §19.8.3–§19.8.4), built on internal/xpath's posture/sweep walk.
//
// SAFETY DIRECTION — fail CLOSED. An instruction is streamable only if a rule
// below says so: a type that does not implement strmShaper is rejected, every
// rule switch has a rejecting default, and a construct this engine cannot
// classify (an attribute-set reference, an unknown instruction) rejects rather
// than being assumed harmless. Declaring something streamable that is not would
// make a bounded-memory executor produce silently wrong output; declaring
// something non-streamable that is merely hard to prove only costs a
// diagnostic.
//
// All private names in this file are prefixed strm* so they cannot collide
// with the bounded-memory executor landing separately.

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// strmRule selects which §19.8.4 rule governs an instruction. Most
// instructions use the general streamability rules over their operand list;
// the rest have bespoke rules that read the shape's named fields.
type strmRule uint8

const (
	strmRuleGeneral        strmRule = iota // §19.8.1 over ops
	strmRuleNoOperands                     // grounded + motionless (xsl:text)
	strmRuleReject                         // never streamable
	strmRuleForEach                        // §19.8.4.18
	strmRuleForEachGroup                   // §19.8.4.19
	strmRuleIterate                        // §19.8.4.22
	strmRuleApplyTemplates                 // §19.8.4.5
	strmRuleFork                           // §19.8.4.20
	strmRuleMap                            // §19.8.4.23
	strmRuleMerge                          // §19.8.4.25
	strmRuleSourceDocument                 // §19.8.4.35 (xsl:stream / xsl:source-document)
)

// strmOperandRef is one operand role of an instruction. Exactly one carrier
// field is populated; the analyzer classifies it and applies usage.
type strmOperandRef struct {
	expr   *xpath.Parsed
	avt    *avt
	pat    *xpath.Pattern
	body   []instruction
	params []*VarDef
	sorts  []sortKey
	// fixed is an operand already classified by a bespoke rule (an
	// xsl:attribute-set reference, §19.8.6) and merely joining the general
	// rules alongside the written ones.
	fixed *strmFixed

	usage xpath.StreamUsage
	// recUsage records that usage is the one the Recommendation itself
	// states for this operand role. See xpath.StreamOperand.RECUsage: it
	// only matters for navigation, which this analysis also uses as its
	// "cannot tell" answer.
	recUsage    bool
	higherOrder bool
	// choice groups operands of which at most one is evaluated; operands
	// sharing a positive value form one choice operand group.
	choice int
	// groundedFocus assesses the operand with a grounded context posture — the
	// xsl:sort keys and the xsl:analyze-string branches, whose context item is
	// an atomic value.
	groundedFocus bool
	// selFocus assesses the operand with the posture of the shape's sel
	// operand (xsl:copy's content, §19.8.4.12).
	selFocus bool
	// ctxItem is an IMPLICIT context-item operand — the "." an
	// xsl:call-template, xsl:next-match or xsl:number passes without writing
	// it (§19.8.4.9). It is motionless with the context posture, exactly like
	// a written ".".
	ctxItem bool
}

// strmShape is an instruction's streamability-relevant structure. Every
// instruction type that can appear in a streamable construct must return one;
// an instruction type that does not implement strmShaper is rejected.
type strmShape struct {
	rule strmRule
	ops  []strmOperandRef

	// Named roles the bespoke rules address directly.
	sel    *xpath.Parsed // @select
	body   []instruction // the contained sequence constructor
	onDone []instruction // xsl:on-completion
	sorts  []sortKey     // xsl:sort children
	params []*VarDef     // xsl:param children (xsl:iterate)
	key    *xpath.Parsed // group-by / group-adjacent
	pats   []*xpath.Pattern
	// mode is the mode xsl:apply-templates dispatches into (§19.8.4.5); the
	// literal "#current" when it was written that way.
	mode string
	// groupBy records that the grouping attribute was group-by, which
	// §19.8.4.19 allows only directly inside xsl:fork.
	groupBy bool
	// forkBranches are the xsl:sequence children of an xsl:fork; forkGroup is
	// its single xsl:for-each-group child, if that is the content form used.
	forkBranches []instruction
	forkGroup    instruction
	// useSets names xsl:attribute-set references. §19.8.6 requires each to be
	// declared streamable="yes"; this engine does not classify attribute sets,
	// so any reference rejects.
	useSets []string
	// el locates the instruction for diagnostics.
	el *xmltree.Node
	// what names the instruction in a diagnostic.
	what string
}

// strmShaper is the optional interface an instruction implements to expose its
// operands to the streamability analysis, mirroring execer. Not implementing
// it means "not streamable": a new instruction stays out of streamable
// constructs until its §19.8.4 rule is written down here.
type strmShaper interface {
	strmShape() strmShape
}

// --- the analyzer ---

type strmFailure struct {
	el  *xmltree.Node
	why string
}

type strmAnalyzer struct {
	// fail records the FIRST construct found non-streamable, which is the one
	// worth naming in the diagnostic; later failures are usually consequences.
	fail *strmFailure
	// inFork is set while classifying the xsl:for-each-group child of an
	// xsl:fork, the one position where §19.8.4.19 permits group-by over
	// streamed nodes.
	inFork bool
	// streamableModes names the modes declared streamable="yes", for
	// §19.8.4.5's rule that xsl:apply-templates may only dispatch into one.
	// knowModes distinguishes "no mode is streamable" from "the caller did not
	// supply the stylesheet"; without it an explicit mode cannot be verified
	// and is therefore rejected.
	streamableModes map[string]bool
	knowModes       bool
	// ss is the stylesheet being analysed, which is what makes §19.8.5
	// (stylesheet functions) and §19.8.6 (attribute sets) decidable. It is nil
	// on the executor's own entry point, whose seam carries no stylesheet; a
	// call or reference is then unclassifiable and fails closed, exactly as
	// before those two sections were implemented.
	ss *Stylesheet
	// el is the innermost instruction element reached so far. Resolving a
	// function-call prefix needs an element's in-scope namespaces, and the
	// expression being classified always belongs to the instruction that
	// carries it.
	el *xmltree.Node
	// setsInProgress guards the attribute-set walk of §19.8.6 against a
	// use-attribute-sets cycle. A cycle is XTSE0720, diagnosed elsewhere; here
	// it only has to terminate.
	setsInProgress map[string]bool
}

func (a *strmAnalyzer) reject(el *xmltree.Node, format string, args ...any) (xpath.Posture, xpath.Sweep) {
	if a.fail == nil {
		a.fail = &strmFailure{el: el, why: fmt.Sprintf(format, args...)}
	}
	return xpath.PostureRoaming, xpath.SweepFreeRanging
}

// rejectREC is reject for a verdict the Recommendation states UNCONDITIONALLY,
// rather than one this analysis reached by failing to prove something. The two
// are kept apart because only the first justifies reporting XTSE3430 against a
// construct the user asked to stream — see xpath.StreamContext.Definite, and
// strmCheckStreamableSourceDocs, its only consumer.
//
// Use it ONLY for a numbered rule in the REC's own ordered list for the
// instruction, implemented here in full, whose premises are facts about the
// stylesheet (an attribute is present, a posture IS crawling) rather than
// things this engine might merely have failed to establish. Every call site
// names the clause it implements.
func (a *strmAnalyzer) rejectREC(sc xpath.StreamContext, el *xmltree.Node, format string, args ...any) (xpath.Posture, xpath.Sweep) {
	sc.MarkDefinite()
	return a.reject(el, format, args...)
}

// strmBody classifies a sequence constructor (§19.8.3): each contained
// instruction is a transmission operand, and the general rules decide.
func (a *strmAnalyzer) strmBody(body []instruction, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	if len(body) == 0 {
		return xpath.PostureGrounded, xpath.SweepMotionless
	}
	ops := make([]xpath.StreamOperand, 0, len(body))
	// §19.8.9.1 treats a sequence constructor's operands as ORDERED: once an
	// instruction has consumed the stream, everything after it in the same
	// constructor runs with the current node's subtree already read, so a later
	// accumulator-after call is motionless.
	isc := sc
	for _, in := range body {
		p, s := a.strmInstr(in, isc)
		if s == xpath.SweepConsuming {
			isc.AccAfterConsumed = true
		}
		// A variable declared as a map or array type is exactly the static
		// signature §19.8.8.11's Note needs to read a later $v(@x) as a
		// lookup (absorption) rather than a general dynamic call
		// (navigation). Recorded as the body is walked, so it is in scope for
		// the instructions that FOLLOW the binding, which is where XSLT's own
		// scoping puts it too.
		if lv, ok := in.(*localVar); ok && lv.def != nil && strmTypeIsMapOrArray(lv.def.as) {
			isc.MapArrayVars = strmWithMapArrayVar(isc.MapArrayVars, strmVarKey(lv.def))
		}
		ops = append(ops, xpath.StreamOperand{P: p, S: s, U: xpath.UsageTransmission})
	}
	return xpath.StreamGeneralRulesIn(ops, &sc)
}

// strmInstr classifies one instruction.
func (a *strmAnalyzer) strmInstr(in instruction, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	sh, ok := in.(strmShaper)
	if !ok {
		return a.reject(nil, "instruction %T has no streamability rule", in)
	}
	shape := sh.strmShape()
	if shape.el != nil {
		// Track the innermost element so a function-call prefix inside this
		// instruction's expressions resolves against the right namespaces.
		prev := a.el
		a.el = shape.el
		defer func() { a.el = prev }()
	}
	if len(shape.useSets) > 0 {
		// §19.8.6: a use-attribute-sets reference is one more operand of the
		// referring instruction, with usage transmission — so a set that
		// consumes the stream competes with the instruction's own content
		// exactly as a second consuming operand would. Only the general rule
		// carries such a reference today; anything else fails closed.
		if shape.rule != strmRuleGeneral {
			return a.reject(shape.el, "%s uses an attribute set, which this processor does not analyse for streamability", shape.name())
		}
		p, s, ok := a.strmAttrSets(shape.useSets, shape.el, sc)
		if !ok {
			return xpath.PostureRoaming, xpath.SweepFreeRanging
		}
		shape.ops = append(append([]strmOperandRef{}, shape.ops...),
			strmOperandRef{fixed: &strmFixed{p: p, s: s}, usage: xpath.UsageTransmission})
	}

	switch shape.rule {
	case strmRuleNoOperands:
		return xpath.PostureGrounded, xpath.SweepMotionless
	case strmRuleReject:
		return a.reject(shape.el, "%s is not streamable", shape.name())
	case strmRuleGeneral:
		// selFocus operands (xsl:copy's content, xsl:perform-sort's keys) are
		// assessed against the posture of the shape's own @select — and against
		// ITS childlessness, not the enclosing focus's, which is a different
		// node entirely once @select is written.
		selPosture, selChildless := sc.ContextPosture, sc.ContextChildless
		if shape.sel != nil {
			selPosture, _ = shape.sel.Streamability(sc)
			selChildless = shape.sel.StreamChildlessIn(sc)
		}
		ops := a.strmOperands(shape.ops, sc, selPosture, selChildless)
		// §19.8.1's adjusted-sweep table makes a navigation operand over any
		// non-grounded posture free-ranging, hence the whole construct roaming
		// — with no escape clause. Reported here, ahead of the general rules,
		// for the operand roles whose navigation usage the Recommendation
		// states outright (see strmOperandRef.recUsage); the general rules
		// themselves stay on StreamGeneralRules rather than the Definite
		// variant, because their OTHER two unconditional clauses are measured
		// net-negative at this one site (see strmForEach's note on the suite
		// asking for more than the guaranteed minimum, and si-copy-028).
		for _, o := range ops {
			if o.RECUsage && o.U == xpath.UsageNavigation &&
				o.P != xpath.PostureGrounded && o.P != xpath.PostureRoaming && o.S != xpath.SweepFreeRanging {
				return a.rejectREC(sc, shape.el, "%s binds a node from the streamed document, which its initializer's navigation usage forbids", shape.name())
			}
		}
		return xpath.StreamGeneralRules(ops)
	case strmRuleForEach:
		return a.strmForEach(shape, sc)
	case strmRuleForEachGroup:
		return a.strmForEachGroup(shape, sc)
	case strmRuleIterate:
		return a.strmIterate(shape, sc)
	case strmRuleApplyTemplates:
		return a.strmApplyTemplates(shape, sc)
	case strmRuleFork:
		return a.strmFork(shape, sc)
	case strmRuleMap:
		return a.strmMap(shape, sc)
	case strmRuleMerge:
		return a.strmMerge(shape, sc)
	case strmRuleSourceDocument:
		return a.strmSourceDocument(shape, sc)
	default:
		return a.reject(shape.el, "%s has no streamability rule", shape.name())
	}
}

func (s strmShape) name() string {
	if s.what != "" {
		return s.what
	}
	return "instruction"
}

// strmOperands classifies each operand reference. selPosture/selChildless
// describe the focus an operand marked selFocus is assessed against.
func (a *strmAnalyzer) strmOperands(refs []strmOperandRef, sc xpath.StreamContext, selPosture xpath.Posture, selChildless bool) []xpath.StreamOperand {
	var out []xpath.StreamOperand
	for _, r := range refs {
		osc := sc
		if r.higherOrder {
			osc = osc.InHigherOrder()
		}
		switch {
		case r.groundedFocus:
			osc.ContextPosture = xpath.PostureGrounded
			osc.ContextIsDocument = false
		case r.selFocus:
			osc.ContextPosture = selPosture
			osc.ContextIsDocument = false
			osc.ContextChildless = selChildless
		}
		mk := func(p xpath.Posture, s xpath.Sweep, childless bool) {
			out = append(out, xpath.StreamOperand{
				P: p, S: s, U: r.usage, Childless: childless,
				HigherOrder: r.higherOrder, Choice: r.choice,
				RECUsage: r.recUsage,
			})
		}
		switch {
		case r.fixed != nil:
			mk(r.fixed.p, r.fixed.s, false)
		case r.ctxItem:
			mk(osc.ContextPosture, xpath.SweepMotionless, false)
		case r.expr != nil:
			p, s := r.expr.Streamability(osc)
			mk(p, s, r.expr.StreamChildlessIn(osc))
		case r.avt != nil:
			for _, p := range a.strmAVT(r.avt, osc) {
				p.U, p.HigherOrder, p.Choice = r.usage, r.higherOrder, r.choice
				out = append(out, p)
			}
		case r.pat != nil:
			p, s := r.pat.Streamability(osc)
			mk(p, s, true)
		case r.body != nil:
			p, s := a.strmBody(r.body, osc)
			mk(p, s, false)
		case r.params != nil:
			out = append(out, a.strmParams(r.params, osc)...)
		case r.sorts != nil:
			out = append(out, a.strmSorts(r.sorts, osc)...)
		}
	}
	return out
}

// strmAVT turns an attribute value template into one operand per embedded
// expression; a constant AVT has no operands at all.
func (a *strmAnalyzer) strmAVT(t *avt, sc xpath.StreamContext) []xpath.StreamOperand {
	if t == nil {
		return nil
	}
	var out []xpath.StreamOperand
	for _, part := range t.parts {
		if part.expr == nil {
			continue
		}
		p, s := part.expr.Streamability(sc)
		out = append(out, xpath.StreamOperand{
			P: p, S: s, U: xpath.UsageAbsorption, Childless: part.expr.StreamChildlessIn(sc),
		})
	}
	return out
}

// strmParams classifies xsl:with-param / xsl:param initialisers. §19.8.4.9:
// the usage is type-determined from @as, so passing a streamed node to a
// template is streamable only when the declared type atomizes it.
func (a *strmAnalyzer) strmParams(params []*VarDef, sc xpath.StreamContext) []xpath.StreamOperand {
	var out []xpath.StreamOperand
	for _, vd := range params {
		if vd == nil {
			continue
		}
		u := strmTypeUsage(vd.as)
		if vd.sel != nil {
			p, s := vd.sel.Streamability(sc)
			out = append(out, xpath.StreamOperand{P: p, S: s, U: u, Childless: vd.sel.StreamChildlessIn(sc)})
		}
		if len(vd.body) > 0 {
			p, s := a.strmBody(vd.body, sc)
			out = append(out, xpath.StreamOperand{P: p, S: s, U: u})
		}
	}
	return out
}

// strmSorts classifies xsl:sort children: the AVT attributes absorb, and the
// sort key absorbs with a grounded focus (§19.8.4.5, §19.8.4.31).
func (a *strmAnalyzer) strmSorts(sorts []sortKey, sc xpath.StreamContext) []xpath.StreamOperand {
	var out []xpath.StreamOperand
	for _, sk := range sorts {
		for _, t := range []*avt{sk.dataType, sk.order, sk.caseOrder, sk.collation, sk.lang} {
			out = append(out, a.strmAVT(t, sc)...)
		}
		ksc := sc
		ksc.ContextPosture = xpath.PostureGrounded
		ksc.ContextIsDocument = false
		if sk.sel != nil {
			p, s := sk.sel.Streamability(ksc)
			out = append(out, xpath.StreamOperand{P: p, S: s, U: xpath.UsageAbsorption, Childless: sk.sel.StreamChildlessIn(ksc)})
		}
		if len(sk.body) > 0 {
			p, s := a.strmBody(sk.body, ksc)
			out = append(out, xpath.StreamOperand{P: p, S: s, U: xpath.UsageAbsorption})
		}
	}
	return out
}

// strmTypeUsage maps an @as sequence type onto its type-determined usage
// (§19.4). The type is kept as written; anything that is not recognisably an
// atomic type or a function test navigates, which is the conservative side.
// strmTypeIsMapOrArray reports whether a declared sequence type names a map or
// an array — the static signature §19.8.8.11's Note lets a dynamic function
// call read as a lookup. It answers only for types written as map(...) or
// array(...); a bare function(...) is not enough, since its argument type is
// whatever that signature says rather than xs:anyAtomicType.
func strmTypeIsMapOrArray(as string) bool {
	t := strings.TrimRight(strings.TrimSpace(as), "*+? \t")
	return strings.HasPrefix(t, "map(") || strings.HasPrefix(t, "array(")
}

// strmWithMapArrayVar returns set plus name, COPYING rather than mutating: a
// binding recorded while walking one body must not leak into a sibling scope
// that started from the same StreamContext value.
func strmWithMapArrayVar(set map[string]bool, name string) map[string]bool {
	out := make(map[string]bool, len(set)+1)
	for k, v := range set {
		out[k] = v
	}
	out[name] = true
	return out
}

// strmTypeNavigationCertain reports whether a declared type is one §19.4
// unambiguously places in the navigation column — a node test or item(). Any
// other spelling (a user-declared schema type name, a syntax this analysis does
// not parse) also lands on navigation, but as the fall-through rather than as
// the Recommendation's own determination, so it must not be diagnosed. See
// strmOperandRef.recUsage.
func strmTypeNavigationCertain(as string) bool {
	t := strings.TrimRight(strings.TrimSpace(as), "*+? \t")
	for _, k := range []string{"item(", "node(", "element(", "attribute(",
		"document-node(", "text(", "comment(", "processing-instruction(",
		"namespace-node(", "schema-element(", "schema-attribute("} {
		if strings.HasPrefix(t, k) {
			return true
		}
	}
	return false
}

func strmTypeUsage(as string) xpath.StreamUsage {
	t := strings.TrimSpace(as)
	if t == "" {
		return xpath.TypeDeterminedUsage(false, false)
	}
	t = strings.TrimRight(t, "*+? \t")
	// §19.4 says "function(*) or a SUBTYPE thereof", and in XPath 3.1 maps and
	// arrays are function types — so map(*)/array(*) inspect like any other
	// function item rather than navigating.
	isFunc := strings.HasPrefix(t, "function(") ||
		strings.HasPrefix(t, "map(") || strings.HasPrefix(t, "array(")
	isAtomic := strings.HasPrefix(t, "xs:") && !strings.HasPrefix(t, "xs:anyType")
	return xpath.TypeDeterminedUsage(isFunc, isAtomic)
}

// --- bespoke instruction rules ---

// strmForEach implements §19.8.4.18.
func (a *strmAnalyzer) strmForEach(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	sp, ss := sh.sel.Streamability(sc)
	if sp == xpath.PostureGrounded {
		ops := []xpath.StreamOperand{{P: sp, S: ss, U: xpath.UsageInspection}}
		gsc := sc.InHigherOrder()
		gsc.ContextPosture = xpath.PostureGrounded
		gsc.ContextIsDocument = false
		// A grounded population makes fn:current() in the body grounded too:
		// the item xsl:for-each is positioned on came from @select, not from
		// the stream (strm/sf-current's c-005 iterates over 1 to 5).
		gsc.CurrentPosture, gsc.HasCurrentPosture = xpath.PostureGrounded, true
		bp, bs := a.strmBody(sh.body, gsc)
		ops = append(ops, xpath.StreamOperand{P: bp, S: bs, U: xpath.UsageTransmission, HigherOrder: true})
		ops = append(ops, a.strmSorts(sh.sorts, sc)...)
		return xpath.StreamGeneralRulesIn(ops, &sc)
	}
	if len(sh.sorts) > 0 {
		return a.reject(sh.el, "xsl:for-each over streamed nodes cannot sort")
	}
	bsc := sc
	bsc.ContextPosture = sp
	// fn:current() in this body denotes the item this instruction is
	// positioned on, not whatever a nested predicate has moved the focus to.
	bsc.CurrentPosture, bsc.HasCurrentPosture = sp, true
	bsc.ContextIsDocument = false
	// The body's context item is one item of @select, so what @select can
	// yield decides whether reading that item's value has to descend at all:
	// over @* or text() it cannot, which is what keeps an xsl:value-of
	// select="." in the body motionless rather than consuming
	// (si-apply-templates-001's <Value><xsl:value-of select="."/></Value>
	// inside xsl:for-each select="@*").
	bsc.ContextChildless = sh.sel.StreamChildlessIn(sc)
	bp, bs := a.strmBody(sh.body, bsc)
	if sp == xpath.PostureCrawling && bs == xpath.SweepConsuming {
		// §19.8.4.18, third ordered rule: "If the posture of the select
		// expression is crawling and the sweep of the contained sequence
		// constructor is consuming, then roaming and free-ranging" — word for
		// word the rule §19.8.4.19 and §19.8.4.22 state for xsl:for-each-group
		// and xsl:iterate, both of which DO report it through the Definite
		// channel (see strmForEachGroup and strmIterate).
		//
		// Not reported here, and the asymmetry is measured rather than
		// principled: xsl:for-each is the common instruction, and routing this
		// through rejectREC costs 18 cases to win 1. The whole of the suite's
		// sx-union-C stylesheet turns on r-015,
		//
		//	<xsl:for-each select="(.../PRICE union .../QUANTITY)">
		//	  <xsl:value-of select=".+1 || ' '"/>
		//	</xsl:for-each>
		//
		// whose select is a union of two striding paths — crawling, as
		// sx-union-202's own description concedes ("the union of two striding
		// expressions is crawling. Not guaranteed streamable (though streamable
		// in Saxon)") — over a body that atomizes an element, hence consuming.
		// The REC's answer is roaming; the suite wants it to run. Classifying
		// it unstreamable and falling back silently gives the suite what it
		// asks for without this analysis claiming something false.
		return a.reject(sh.el, "xsl:for-each body consumes a crawling selection")
	}
	if !xpath.Streamable(sp, ss) {
		return a.reject(sh.el, "xsl:for-each/@select is not streamable")
	}
	if !xpath.Streamable(bp, bs) {
		return a.reject(sh.el, "xsl:for-each body is not streamable")
	}
	return bp, strmWider(ss, bs)
}

// strmForEachGroup implements §19.8.4.19.
func (a *strmAnalyzer) strmForEachGroup(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	sp, ss := sh.sel.Streamability(sc)
	if sp == xpath.PostureGrounded {
		ops := []xpath.StreamOperand{{P: sp, S: ss, U: xpath.UsageInspection}}
		ops = append(ops, a.strmOperands(sh.ops, sc, sp, sh.sel.StreamChildlessIn(sc))...)
		ops = append(ops, a.strmSorts(sh.sorts, sc)...)
		gsc := sc
		gsc.ContextPosture = xpath.PostureGrounded
		gsc.ContextIsDocument = false
		if sh.key != nil {
			kp, ks := sh.key.Streamability(gsc)
			ops = append(ops, xpath.StreamOperand{P: kp, S: ks, U: xpath.UsageAbsorption, Childless: sh.key.StreamChildlessIn(gsc)})
		}
		for _, pat := range sh.pats {
			// group-starting-with / group-ending-with are matched against the
			// POPULATION, which is grounded here — testing a materialised node
			// moves nothing, whatever the pattern's own predicates do.
			pp, ps := pat.Streamability(gsc)
			ops = append(ops, xpath.StreamOperand{P: pp, S: ps, U: xpath.UsageInspection, HigherOrder: true})
		}
		// The body still has to be classified even over a grounded population:
		// it can reach back into the enclosing stream through the context of
		// the xsl:for-each-group itself.
		gsc.HasGroup, gsc.GroupPosture = true, xpath.PostureGrounded
		gsc = gsc.InHigherOrder()
		bp, bs := a.strmBody(sh.body, gsc)
		ops = append(ops, xpath.StreamOperand{P: bp, S: bs, U: xpath.UsageTransmission, HigherOrder: true})
		return xpath.StreamGeneralRules(ops)
	}
	// group-by over streamed nodes buffers whole groups, which §19.8.4.19
	// licenses only directly inside xsl:fork.
	if sh.groupBy && !a.inFork {
		// §19.8.4.19, second ordered rule: "If there is a group-by attribute
		// and the instruction is not a child of xsl:fork, then roaming and
		// free-ranging." Both premises are written facts about the stylesheet.
		return a.rejectREC(sc, sh.el, "xsl:for-each-group/@group-by over streamed nodes is only streamable as a child of xsl:fork")
	}
	if sh.key != nil {
		// The grouping key is evaluated with a POPULATION ITEM as its focus,
		// not with the xsl:for-each-group's own context item — which is what
		// keeps group-by="substring-after(., ',')" motionless over a
		// population of text nodes.
		ksc := sc
		ksc.ContextPosture = sp
		ksc.ContextIsDocument = false
		ksc.ContextChildless = sh.sel.StreamChildlessIn(sc)
		if _, ks := sh.key.Streamability(ksc); ks != xpath.SweepMotionless {
			// §19.8.4.19, third ordered rule: "If there is a group-by or
			// group-adjacent attribute that is not motionless, then roaming
			// and free-ranging."
			return a.rejectREC(sc, sh.el, "xsl:for-each-group grouping expression is not motionless")
		}
	}
	if len(sh.sorts) > 0 {
		return a.reject(sh.el, "xsl:for-each-group over streamed nodes cannot sort")
	}
	// group-starting-with / group-ending-with over a STREAMED population.
	//
	// §19.8.4.19 lists these patterns as "higher-order operands with usage
	// inspection", but only inside its FIRST ordered rule, the one for a
	// grounded select; rules 2-6, which govern a streamed population, never
	// mention them. The W3C suite settles what the REC leaves unsaid:
	// si-group-030 (a value-comparison predicate, record[foo = 'a']) and
	// si-group-063 (a positional one) both require XTSE3430, and §19.8.10's
	// own list of patterns that are NOT motionless contains p[b] verbatim.
	//
	// The rejection is scoped to strict enforcement, and that scoping is the
	// substance of the rule rather than a convenience. Guaranteed-streamable
	// is a claim about what EVERY conforming processor must manage; it is not
	// the boundary of what this one can do. strbGrpState materialises each
	// record before the pattern ever sees it and buffers only the open group
	// (see stream_group.go's own memory note), so rec[v = 1] streams here
	// safely — an extension beyond the minimum, exactly the kind XTSE3430's
	// "unless the user has indicated that the processor is to handle this
	// situation by processing the stylesheet without streaming" anticipates,
	// and the kind Saxon takes elsewhere. So when the caller has asked for the
	// REC's guarantee to be binding, the REC's answer is given; otherwise the
	// engine keeps streaming what it can genuinely stream. This is the same
	// split strmEnforce/strmClaim already draws between "what must be
	// rejected" and "what this engine actually does".
	if strmEnforce.Load() {
		for _, pat := range sh.pats {
			if pat == nil {
				continue
			}
			psc := sc
			psc.ContextPosture = xpath.PostureStriding
			psc.ContextIsDocument = false
			if _, ps := pat.Streamability(psc); ps != xpath.SweepMotionless {
				return a.reject(sh.el, "xsl:for-each-group grouping pattern is not motionless")
			}
		}
	}
	bsc := sc
	bsc.ContextPosture = sp
	// fn:current() in this body denotes the item this instruction is
	// positioned on, not whatever a nested predicate has moved the focus to.
	bsc.CurrentPosture, bsc.HasCurrentPosture = sp, true
	bsc.ContextIsDocument = false
	bsc.HasGroup, bsc.GroupPosture = true, sp
	bp, bs := a.strmBody(sh.body, bsc)
	if sp == xpath.PostureCrawling && bs == xpath.SweepConsuming {
		// §19.8.4.19, fifth ordered rule: "If the posture of the select
		// expression is crawling and the sweep of the contained sequence
		// constructor is consuming, then roaming and free-ranging."
		return a.rejectREC(sc, sh.el, "xsl:for-each-group body consumes a crawling population")
	}
	if !xpath.Streamable(sp, ss) || !xpath.Streamable(bp, bs) {
		return a.reject(sh.el, "xsl:for-each-group is not streamable")
	}
	return bp, strmWider(ss, bs)
}

// strmIterate implements §19.8.4.22.
func (a *strmAnalyzer) strmIterate(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	sp, ss := sh.sel.Streamability(sc)
	if sp == xpath.PostureGrounded {
		ops := []xpath.StreamOperand{{P: sp, S: ss, U: xpath.UsageInspection}}
		for _, o := range a.strmParams(sh.params, sc) {
			o.U = xpath.UsageNavigation
			ops = append(ops, o)
		}
		bsc := sc
		bsc.ContextPosture = sp
		bsc.CurrentPosture, bsc.HasCurrentPosture = sp, true
		bp, bs := a.strmBody(sh.body, bsc)
		ops = append(ops, xpath.StreamOperand{P: bp, S: bs, U: xpath.UsageTransmission})
		// xsl:on-completion has no context item at all: §19.8.4.22 gives it a
		// roaming context posture so any reference to "." rejects.
		osc := sc
		osc.ContextPosture = xpath.PostureRoaming
		if len(sh.onDone) > 0 {
			op, os := a.strmBody(sh.onDone, osc)
			ops = append(ops, xpath.StreamOperand{P: op, S: os, U: xpath.UsageTransmission})
		}
		return xpath.StreamGeneralRules(ops)
	}
	for _, o := range a.strmParams(sh.params, sc) {
		if o.P != xpath.PostureGrounded || o.S != xpath.SweepMotionless {
			// §19.8.4.22, first ordered rule of the non-grounded case: "If
			// there is an xsl:param whose initializing select expression or
			// sequence constructor is not grounded and motionless, then
			// roaming and free-ranging."
			return a.rejectREC(sc, sh.el, "xsl:iterate parameter is not grounded and motionless")
		}
	}
	if len(sh.onDone) > 0 {
		osc := sc
		osc.ContextPosture = xpath.PostureRoaming
		op, os := a.strmBody(sh.onDone, osc)
		if op != xpath.PostureGrounded || os != xpath.SweepMotionless {
			// §19.8.4.22, second ordered rule: "If there is an
			// xsl:on-completion child whose select expression or sequence
			// constructor is not grounded and motionless, then roaming and
			// free-ranging."
			return a.rejectREC(sc, sh.el, "xsl:on-completion is not grounded and motionless")
		}
	}
	bsc := sc
	bsc.ContextPosture = sp
	// fn:current() in this body denotes the item this instruction is
	// positioned on, not whatever a nested predicate has moved the focus to.
	bsc.CurrentPosture, bsc.HasCurrentPosture = sp, true
	bsc.ContextIsDocument = false
	// The body's context item is one item of @select, so what @select can
	// yield decides whether reading that item's value has to descend at all:
	// over @* or text() it cannot, which is what keeps an xsl:value-of
	// select="." in the body motionless rather than consuming
	// (si-apply-templates-001's <Value><xsl:value-of select="."/></Value>
	// inside xsl:for-each select="@*").
	bsc.ContextChildless = sh.sel.StreamChildlessIn(sc)
	bp, bs := a.strmBody(sh.body, bsc)
	if sp == xpath.PostureCrawling && bs == xpath.SweepConsuming {
		// §19.8.4.22, third ordered rule: "If the posture of the select
		// expression is crawling and the sweep of the contained sequence
		// constructor is consuming, then roaming and free-ranging."
		return a.rejectREC(sc, sh.el, "xsl:iterate body consumes a crawling selection")
	}
	if !xpath.Streamable(sp, ss) || !xpath.Streamable(bp, bs) {
		return a.reject(sh.el, "xsl:iterate is not streamable")
	}
	return bp, strmWider(ss, bs)
}

// strmApplyTemplates implements §19.8.4.5.
func (a *strmAnalyzer) strmApplyTemplates(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	// An absent @select is an implicit select="child::node()".
	var sp xpath.Posture
	var ss xpath.Sweep
	if sh.sel != nil {
		sp, ss = sh.sel.Streamability(sc)
	} else {
		sp, ss = strmChildAxisPosture(sc.ContextPosture)
	}
	// A selection that can only hold childless nodes (attributes, text) is
	// inspected rather than absorbed (§19.8.1), which is what makes
	// apply-templates select="@*" motionless.
	childless := sh.sel.StreamChildlessIn(sc)
	if sh.sel == nil {
		childless = false // the implicit child::node() can select elements
	}
	if sp == xpath.PostureGrounded {
		ops := []xpath.StreamOperand{{P: sp, S: ss, U: xpath.UsageAbsorption, Childless: childless}}
		ops = append(ops, a.strmParams(sh.params, sc)...)
		ops = append(ops, a.strmSorts(sh.sorts, sc)...)
		return xpath.StreamGeneralRules(ops)
	}
	if len(sh.sorts) > 0 {
		return a.reject(sh.el, "xsl:apply-templates over streamed nodes cannot sort")
	}
	// §19.8.4.5: the dispatched mode must itself be declared streamable.
	// mode="#current" is always acceptable — if the template was reached with
	// a streamed node, the mode in force is already a streamable one.
	if sh.mode != "#current" && !a.streamableMode(sh.mode) {
		return a.reject(sh.el, "xsl:apply-templates dispatches into mode %s, which is not declared streamable", strmModeName(sh.mode))
	}
	if sp == xpath.PostureClimbing || sp == xpath.PostureCrawling {
		// §19.8.4.5, fourth ordered rule: "If the select expression is climbing
		// or crawling, then roaming and free-ranging." Both premises are
		// established rather than assumed — the select expression was fully
		// classified above, and the rule has no escape clause — so this is the
		// Recommendation's verdict on the stylesheet, not this analysis giving
		// up. sx-union-202 is the suite's own case for it, and its description
		// concedes the point: "the union of two striding expressions is
		// crawling. Not guaranteed streamable (though streamable in Saxon)".
		return a.rejectREC(sc, sh.el, "xsl:apply-templates/@select is %s, which cannot be dispatched in a single pass", strmPostureName(sp))
	}
	ops := []xpath.StreamOperand{{P: sp, S: ss, U: xpath.UsageAbsorption, Childless: childless}}
	ops = append(ops, a.strmParams(sh.params, sc)...)
	return xpath.StreamGeneralRules(ops)
}

// strmFork implements §19.8.4.20: each xsl:sequence branch may consume the
// stream independently, provided every branch result is grounded.
func (a *strmAnalyzer) strmFork(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	if sh.forkGroup != nil {
		prev := a.inFork
		a.inFork = true
		p, s := a.strmInstr(sh.forkGroup, sc)
		a.inFork = prev
		return p, s
	}
	if len(sh.forkBranches) == 0 {
		return xpath.PostureGrounded, xpath.SweepMotionless
	}
	sweep := xpath.SweepMotionless
	for _, br := range sh.forkBranches {
		p, s := a.strmInstr(br, sc)
		if p != xpath.PostureGrounded {
			// §19.8.4.20, third ordered rule: "If there is a child
			// xsl:sequence instruction whose posture is not grounded, then
			// roaming and free-ranging", with the Note giving the reason
			// ("xsl:fork has to assemble its results in the correct order,
			// and streamed nodes cannot be re-ordered").
			//
			// Classified, but deliberately NOT reported through
			// rejectREC/Definite even though the rule is unconditional: the
			// suite's own si-fork-006 is the shape "xsl:fork can return
			// streamed nodes if only one branch is consuming" (its comment's
			// words) and expects it to RUN, with eight sibling cases sharing
			// its stylesheet. Measured at +1/-9. That is the same divergence
			// already recorded for si-copy-028 and for the ITEM[1]//text()
			// family in streaming round 7: the suite asks for more than the
			// guaranteed-streamability minimum, so the honest response is to
			// keep classifying it unstreamable — which silently falls back to
			// buffered execution, and those nine cases pass — rather than to
			// fail the stylesheet outright.
			return a.reject(sh.el, "an xsl:fork branch returns streamed nodes, which cannot be reordered")
		}
		if s == xpath.SweepFreeRanging {
			return a.reject(sh.el, "an xsl:fork branch is not streamable")
		}
		sweep = strmWider(sweep, s)
	}
	return xpath.PostureGrounded, sweep
}

// strmMap implements §19.8.4.23: a body made only of xsl:map-entry children may
// compute every entry in one pass.
func (a *strmAnalyzer) strmMap(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	allEntries := len(sh.body) > 0
	for _, in := range sh.body {
		if _, ok := in.(*mpEntry); !ok {
			allEntries = false
			break
		}
	}
	if !allEntries {
		return a.strmBody(sh.body, sc)
	}
	sweep := xpath.SweepMotionless
	for _, in := range sh.body {
		p, s := a.strmInstr(in, sc)
		if !xpath.Streamable(p, s) {
			// §19.8.4.23, first ordered rule: when the body is made only of
			// xsl:map-entry children, "If any of these xsl:map-entry children
			// is roaming or free-ranging, then roaming and free-ranging." The
			// child itself is classified by §19.8.4.24's general rules, whose
			// own Note states the consequence outright: "the select expression
			// must not return nodes from a streamed input document."
			return a.rejectREC(sc, sh.el, "an xsl:map-entry is not streamable")
		}
		sweep = strmWider(sweep, s)
	}
	return xpath.PostureGrounded, sweep
}

// strmMerge implements §19.8.4.25: xsl:merge does not touch the containing
// stream at all, provided every merge source selects grounded, motionless
// input.
func (a *strmAnalyzer) strmMerge(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	for _, o := range a.strmOperands(sh.ops, sc, sc.ContextPosture, sc.ContextChildless) {
		if o.P != xpath.PostureGrounded || o.S != xpath.SweepMotionless {
			return a.reject(sh.el, "an xsl:merge-source selection is not grounded and motionless")
		}
	}
	// The merge action runs over grounded merge groups, but it is still
	// checked: nothing in it may reach back into the enclosing stream.
	asc := sc
	asc.ContextPosture = xpath.PostureGrounded
	asc.ContextIsDocument = false
	if p, s := a.strmBody(sh.body, asc); !xpath.Streamable(p, s) {
		return a.reject(sh.el, "the xsl:merge-action body is not streamable")
	}
	return xpath.PostureGrounded, xpath.SweepMotionless
}

// strmSourceDocument implements §19.8.4.35: the instruction opens its OWN
// stream, so w.r.t. the enclosing one it is grounded, with the sweep of its
// @href. The spec's exception is a current-group()/current-merge-group() call
// inside it whose owning instruction is outside; this rejects any such call,
// which is the conservative form of that test.
func (a *strmAnalyzer) strmSourceDocument(sh strmShape, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep) {
	// A grounded grouping population is already materialised, so reaching its
	// current group from inside the nested stream crosses no boundary that
	// matters.
	groundedGroup := sc.HasGroup && sc.GroupPosture == xpath.PostureGrounded
	if !groundedGroup && strmMentionsGroupFunction(sh.body) {
		return a.reject(sh.el, "%s contains a grouping-function call, which cannot be assessed across a stream boundary", sh.name())
	}
	return xpath.StreamGeneralRules(a.strmOperands(sh.ops, sc, sc.ContextPosture, sc.ContextChildless))
}

// --- helpers ---

// streamableMode reports whether a mode may be dispatched into from a
// streamable construct. Without the stylesheet in hand the answer cannot be
// verified, so only the unnamed mode — the one an xsl:source-document body
// conventionally uses, and the one the executor checks for itself — is
// accepted; a named mode rejects.
func (a *strmAnalyzer) streamableMode(name string) bool {
	if !a.knowModes {
		return name == ""
	}
	return a.streamableModes[name]
}

func strmWider(a, b xpath.Sweep) xpath.Sweep {
	if a > b {
		return a
	}
	return b
}

func strmPostureName(p xpath.Posture) string {
	switch p {
	case xpath.PostureGrounded:
		return "grounded"
	case xpath.PostureClimbing:
		return "climbing"
	case xpath.PostureCrawling:
		return "crawling"
	case xpath.PostureStriding:
		return "striding"
	default:
		return "roaming"
	}
}

// strmChildAxisPosture is the posture/sweep of the implicit
// select="child::node()" of xsl:apply-templates (§19.8.8.8's table row for the
// child axis).
func strmChildAxisPosture(cp xpath.Posture) (xpath.Posture, xpath.Sweep) {
	switch cp {
	case xpath.PostureGrounded:
		return xpath.PostureGrounded, xpath.SweepMotionless
	case xpath.PostureStriding:
		return xpath.PostureStriding, xpath.SweepConsuming
	default:
		return xpath.PostureRoaming, xpath.SweepFreeRanging
	}
}

// strmMentionsGroupFunction reports whether a body calls current-group() or
// current-merge-group() at any depth.
func strmMentionsGroupFunction(body []instruction) bool {
	found := false
	var walk func([]instruction)
	check := func(p *xpath.Parsed) {
		if p == nil {
			return
		}
		if p.StreamUsesFunction("current-group") || p.StreamUsesFunction("current-merge-group") {
			found = true
		}
	}
	walk = func(is []instruction) {
		for _, in := range is {
			if found {
				return
			}
			sh, ok := in.(strmShaper)
			if !ok {
				continue
			}
			s := sh.strmShape()
			check(s.sel)
			check(s.key)
			for _, r := range s.ops {
				check(r.expr)
				if r.avt != nil {
					for _, part := range r.avt.parts {
						check(part.expr)
					}
				}
				walk(r.body)
			}
			walk(s.body)
			walk(s.onDone)
			walk(s.forkBranches)
			if s.forkGroup != nil {
				walk([]instruction{s.forkGroup})
			}
		}
	}
	walk(body)
	return found
}

// --- stylesheet functions (§19.8.5) and attribute sets (§19.8.6) ---

// strmFixed is an operand a bespoke rule has already classified, carried
// through the general rules alongside the written operands.
type strmFixed struct {
	p xpath.Posture
	s xpath.Sweep
}

// strmFuncCat reads xsl:function/@streamability (§19.8.5). The attribute
// defaults to unclassified, and a category named by a QName in an
// implementation-defined namespace "must be analyzed as if
// streamability='unclassified' were specified" — which is also the honest
// answer for any value this processor does not recognise.
func strmFuncCat(el *xmltree.Node) xpath.StreamFuncCategory {
	if el == nil {
		return xpath.StreamFnUnclassified
	}
	v, ok := el.AttrLocal("streamability")
	if !ok {
		return xpath.StreamFnUnclassified
	}
	switch strings.TrimSpace(v) {
	case "absorbing":
		return xpath.StreamFnAbsorbing
	case "inspection":
		return xpath.StreamFnInspection
	case "filter":
		return xpath.StreamFnFilter
	case "shallow-descent":
		return xpath.StreamFnShallowDescent
	case "deep-descent":
		return xpath.StreamFnDeepDescent
	case "ascent":
		return xpath.StreamFnAscent
	}
	return xpath.StreamFnUnclassified
}

// strmFuncSig builds the call-site signature §19.8.5 needs from a compiled
// xsl:function.
func strmFuncSig(fd *FuncDef) xpath.StreamFuncSig {
	sig := xpath.StreamFuncSig{Category: strmFuncCat(fd.el)}
	sig.Streamable = sig.Category != xpath.StreamFnUnclassified
	for _, p := range fd.params {
		sig.ParamUsage = append(sig.ParamUsage, strmTypeUsage(strmParamType(p)))
	}
	if len(fd.params) > 0 {
		as := strmParamType(fd.params[0])
		sig.FirstAdjust = strmTypeAdjustUsage(as)
		sig.FirstParamNodes = strmTypeAdmitsNodes(as)
	}
	return sig
}

func strmParamType(vd *VarDef) string {
	if vd == nil {
		return ""
	}
	return vd.as
}

// StreamFunc resolves a stylesheet function call for the XPath analysis
// (§19.8.5). The prefix is resolved against the element the walk is currently
// inside, the same way the evaluator resolves it — including the braced-EQName
// form, which reaches here with the URI itself standing in for a prefix.
func (a *strmAnalyzer) StreamFunc(prefix, local string, arity int) (xpath.StreamFuncSig, bool) {
	if a.ss == nil || a.el == nil {
		return xpath.StreamFuncSig{}, false
	}
	uri, ok := a.el.LookupPrefix(prefix)
	if !ok {
		uri = prefix
	}
	fd := a.ss.functions[funcKey(uri, local, arity)]
	if fd == nil {
		return xpath.StreamFuncSig{}, false
	}
	return strmFuncSig(fd), true
}

// strmTypeAdjustUsage is the usage that applies the FUNCTION CONVERSION RULES
// to a value of the declared type — §19.8.5's type-adjusted posture and sweep.
// It is deliberately NOT the type-determined usage: a required type that
// converts nothing leaves the operand's posture and sweep untouched, which is
// transmission, where §19.4 would say navigation. That is the only reading
// under which the spec's own worked example comes out as stated —
// shallow-descent's f:alternate-children declares its first parameter
// as element()* and is said to be streamable over a striding, consuming
// argument, which requires both to survive the adjustment.
func strmTypeAdjustUsage(as string) xpath.StreamUsage {
	switch strmTypeUsage(as) {
	case xpath.UsageAbsorption:
		return xpath.UsageAbsorption
	case xpath.UsageInspection:
		return xpath.UsageInspection
	}
	return xpath.UsageTransmission
}

// strmTypeAdmitsNodes reports whether a declared sequence type can include a
// document or element node — §19.8.5.5's "intersection of T0 with
// U{document-node(), element()}". A type this test cannot read answers true,
// which widens a descent call's sweep to consuming: the conservative side.
func strmTypeAdmitsNodes(as string) bool {
	t := strings.TrimSpace(as)
	if t == "" {
		return true // the default item()* admits everything
	}
	t = strings.TrimRight(t, "*+? \t")
	switch {
	case strings.HasPrefix(t, "xs:"),
		strings.HasPrefix(t, "attribute("),
		strings.HasPrefix(t, "schema-attribute("),
		strings.HasPrefix(t, "text("),
		strings.HasPrefix(t, "comment("),
		strings.HasPrefix(t, "processing-instruction("),
		strings.HasPrefix(t, "namespace-node("),
		strings.HasPrefix(t, "function("),
		strings.HasPrefix(t, "map("),
		strings.HasPrefix(t, "array("):
		return false
	}
	return true
}

// strmCatNeedsSingleNode reports whether §19.8.5's "Rules for the function
// signature" constrain the streaming parameter of this category to at most one
// node. Every declared-streamable category does EXCEPT absorbing, which reads
// the supplied subtrees one after another and so tolerates a sequence — the
// spec states the exemption explicitly ("Rules for the function signature:
// there are no constraints") only for unclassified and absorbing.
func strmCatNeedsSingleNode(cat xpath.StreamFuncCategory) bool {
	switch cat {
	case xpath.StreamFnInspection, xpath.StreamFnFilter,
		xpath.StreamFnShallowDescent, xpath.StreamFnDeepDescent, xpath.StreamFnAscent:
		return true
	}
	return false
}

// strmTypeManyNodes reports whether a declared sequence type permits MORE THAN
// ONE NODE — §19.8.5's signature rule, and the test that widens an absorbing
// function's streaming-parameter references to consuming. A type that admits no
// node at all (xs:string*, function(*)) permits no nodes, however many items it
// allows; an absent type is item()*, which permits any number.
func strmTypeManyNodes(as string) bool {
	t := strings.TrimSpace(as)
	if t == "" {
		return true // the default item()* permits any number of nodes
	}
	if !strmTypePermitsNodes(t) {
		return false
	}
	switch t[len(t)-1] {
	case '*', '+':
		return true
	}
	return false
}

// strmParamTypeName is strmParamType with the implied default spelled out, for
// a diagnostic that would otherwise name an empty type.
func strmParamTypeName(vd *VarDef) string {
	if t := strings.TrimSpace(strmParamType(vd)); t != "" {
		return t
	}
	return "item()*"
}

// strmTypePermitsNodes reports whether a declared sequence type has a non-empty
// intersection with U{N} — §19.8.8.11's test for whether a streaming parameter
// can carry a streamed node at all. An absent type (item()*) permits them.
func strmTypePermitsNodes(as string) bool {
	t := strings.TrimSpace(as)
	if t == "" {
		return true
	}
	t = strings.TrimRight(t, "*+? \t")
	switch {
	case strings.HasPrefix(t, "xs:"),
		strings.HasPrefix(t, "function("),
		strings.HasPrefix(t, "map("),
		strings.HasPrefix(t, "array("):
		return false
	}
	return true
}

// strmVarKey is the key a *VarDef has in StreamContext.StreamingParams, which
// the XPath walk looks up by the LEXICAL spelling of a variable reference
// ("prefix:local", or ":local" when unprefixed).
func strmVarKey(vd *VarDef) string {
	if vd == nil {
		return ""
	}
	if vd.el != nil {
		if lex, ok := vd.el.AttrLocal("name"); ok {
			lex = strings.TrimSpace(lex)
			if i := strings.IndexByte(lex, ':'); i > 0 && !strings.HasPrefix(lex, "Q{") {
				return lex
			}
		}
	}
	return ":" + vd.name.Local
}

// strmAttrSets classifies a use-attribute-sets reference (§19.8.6): the
// operands are every xsl:attribute instruction of every declaration making up
// each named set, plus the sets THOSE reference, all with usage transmission.
//
// The REC frames the result in terms of the sets' DECLARED streamable
// attribute rather than their contents, so that overriding a set in another
// package cannot change a caller's streamability — and it says so twice, once
// per direction. §19.8.6: "Because attribute sets can be overridden in another
// package, the streamability of a construct such as an xsl:element instruction
// containing a use-attribute-sets attribute is based on the DECLARED
// streamability of the named attribute sets". And the identical Note carried
// by §19.8.4.1 (literal result element), §19.8.4.11 (xsl:copy) and §19.8.4.15
// (xsl:element): "a reference to an attribute set that is declared-streamable
// does not affect the analysis, while a reference to ANY OTHER attribute set
// makes the [instruction] roaming and free-ranging."
//
// So the declaration is checked FIRST and on its own terms. An earlier version
// of this function skipped that and inspected the sets' bodies instead, on the
// reasoning that with no package overrides to protect, reading the real
// declarations answers the same question more precisely. It does not: §10.2.3's
// own Note says outright that a constant-valued set "will always be grounded
// and motionless and therefore streamable" yet is still "not guaranteed
// streamable unless the attribute set is declared with the attribute
// streamable='yes'". Being more permissive than the REC is not extra precision
// here, it is the wrong answer — the suite's si-copy/si-element/si-LRE -901 and
// -902 pairs test exactly this, with a set declared streamable="no" and a set
// carrying no streamable attribute at all.
//
// The body analysis is kept as an ADDITIONAL requirement on top (a set that
// says streamable="yes" must also actually be streamable — §19.10's "a
// streaming processor is required to check that an attribute set containing
// such a declaration does in fact satisfy the streamability rules"), which is
// what the -903 pair tests with a streamable="yes" set whose body is last().
func (a *strmAnalyzer) strmAttrSets(names []string, el *xmltree.Node, sc xpath.StreamContext) (xpath.Posture, xpath.Sweep, bool) {
	if a.ss == nil {
		a.reject(el, "an attribute set is referenced where the analysis has no stylesheet to read it from")
		return 0, 0, false
	}
	if a.setsInProgress == nil {
		a.setsInProgress = map[string]bool{}
	}
	var ops []xpath.StreamOperand
	for _, name := range names {
		if a.setsInProgress[name] {
			// XTSE0720 territory; here it only has to terminate.
			continue
		}
		decls, ok := a.ss.attrSets[name]
		if !ok {
			a.reject(el, "xsl:attribute-set %s is not declared", name)
			return 0, 0, false
		}
		for _, decl := range decls {
			if !decl.streamable {
				// Stated by the REC without qualification, and turning only on
				// an attribute that is written or not — so unlike most of this
				// analysis it cannot be reached by failing to prove something.
				sc.MarkDefinite()
				a.reject(el, "xsl:attribute-set %s is not declared streamable", name)
				return 0, 0, false
			}
		}
		a.setsInProgress[name] = true
		for _, decl := range decls {
			if len(decl.uses) > 0 {
				p, s, ok := a.strmAttrSets(decl.uses, el, sc)
				if !ok {
					delete(a.setsInProgress, name)
					return 0, 0, false
				}
				ops = append(ops, xpath.StreamOperand{P: p, S: s, U: xpath.UsageTransmission})
			}
			p, s := a.strmBody(decl.body, sc)
			ops = append(ops, xpath.StreamOperand{P: p, S: s, U: xpath.UsageTransmission})
		}
		delete(a.setsInProgress, name)
	}
	p, s := xpath.StreamGeneralRules(ops)
	if !xpath.Streamable(p, s) {
		a.reject(el, "xsl:attribute-set %s is not streamable", strings.Join(names, " "))
		return 0, 0, false
	}
	return p, s, true
}

// strmFuncResultOK applies §19.8.5's per-category constraint on the posture and
// sweep of a declared-streamable function's RESULT.
func strmFuncResultOK(cat xpath.StreamFuncCategory, p xpath.Posture, s xpath.Sweep) bool {
	switch cat {
	case xpath.StreamFnAbsorbing:
		return p == xpath.PostureGrounded && s != xpath.SweepFreeRanging
	case xpath.StreamFnInspection:
		return p == xpath.PostureGrounded && s == xpath.SweepMotionless
	case xpath.StreamFnFilter:
		return (p == xpath.PostureStriding || p == xpath.PostureGrounded) && s == xpath.SweepMotionless
	case xpath.StreamFnShallowDescent:
		return (p == xpath.PostureStriding || p == xpath.PostureGrounded) && s != xpath.SweepFreeRanging
	case xpath.StreamFnDeepDescent:
		return (p == xpath.PostureCrawling || p == xpath.PostureStriding || p == xpath.PostureGrounded) &&
			s != xpath.SweepFreeRanging
	case xpath.StreamFnAscent:
		// §19.8.5.7: "climbing or grounded", and motionless. An earlier round
		// had to relax this to "not roaming" because a reference to the
		// streaming parameter was classified striding, which made the suite's
		// own valid su-ascent-005/006 come out striding too. §19.8.5.7's "Rules
		// for references to the streaming parameter" say climbing, not striding
		// — with that in place the spec's real constraint holds for both, so
		// the relaxation is gone.
		return (p == xpath.PostureClimbing || p == xpath.PostureGrounded) && s == xpath.SweepMotionless
	}
	return true // unclassified places no constraint on the body
}

func strmCategoryName(cat xpath.StreamFuncCategory) string {
	switch cat {
	case xpath.StreamFnAbsorbing:
		return "absorbing"
	case xpath.StreamFnInspection:
		return "inspection"
	case xpath.StreamFnFilter:
		return "filter"
	case xpath.StreamFnShallowDescent:
		return "shallow-descent"
	case xpath.StreamFnDeepDescent:
		return "deep-descent"
	case xpath.StreamFnAscent:
		return "ascent"
	}
	return "unclassified"
}

// --- entry points ---

// classifyStreamable reports whether body is provably guaranteed-streamable
// when evaluated at el's position, and otherwise returns a catalogued
// XTSE3430 diagnostic naming the construct that defeated the analysis.
//
// The entry focus is a striding posture over a document node: that is the
// context §19.6 gives an xsl:source-document / xsl:stream body, which is the
// construct that opens a stream in the first place.
func classifyStreamable(body []instruction, el *xmltree.Node) (bool, error) {
	sc := xpath.StreamContext{ContextPosture: xpath.PostureStriding, ContextIsDocument: true}
	return strmClassify(body, el, sc, "construct")
}

// strmClassify is classifyStreamable with an explicit entry focus, used where
// the caller knows more about the context item than the exported seam can
// assume (a template rule matching an element, say).
func strmClassify(body []instruction, el *xmltree.Node, sc xpath.StreamContext, what string) (bool, error) {
	return strmClassifyIn(&strmAnalyzer{}, body, el, sc, what)
}

func strmClassifyIn(a *strmAnalyzer, body []instruction, el *xmltree.Node, sc xpath.StreamContext, what string) (bool, error) {
	_, _, ok, err := strmClassifyInP(a, body, el, sc, what)
	return ok, err
}

// strmClassifyInP is strmClassifyIn that also hands back the posture and sweep
// it computed for the body. A caller that has a FURTHER condition to apply to
// them (§6.6.4's type-adjusted grounded test for a template rule, say) must
// take them from here rather than re-running strmBody: the walk only resolves
// stylesheet-function calls once sc.Funcs has been wired up below, so a second
// walk over a fresh context would fail closed on every such call.
func strmClassifyInP(a *strmAnalyzer, body []instruction, el *xmltree.Node, sc xpath.StreamContext, what string) (xpath.Posture, xpath.Sweep, bool, error) {
	if a.ss != nil {
		// §19.8.5 is decidable only with the stylesheet's function catalogue in
		// hand; without it every stylesheet function call fails closed.
		sc.Funcs = a
		if a.el == nil {
			a.el = el
		}
	}
	p, s := a.strmBody(body, sc)
	if xpath.Streamable(p, s) && a.fail == nil {
		return p, s, true, nil
	}
	at, why := el, ""
	if a.fail != nil {
		why = a.fail.why
		if a.fail.el != nil {
			at = a.fail.el
		}
	}
	if why == "" {
		why = fmt.Sprintf("the %s is %s and %s", what, strmPostureName(p), strmSweepName(s))
	}
	return p, s, false, errAt(at, "err:XTSE3430: %s is declared streamable but is not guaranteed-streamable: %s", what, why)
}

func strmSweepName(s xpath.Sweep) string {
	switch s {
	case xpath.SweepMotionless:
		return "motionless"
	case xpath.SweepConsuming:
		return "consuming"
	default:
		return "free-ranging"
	}
}

// strmEnforce gates the XTSE3430 rejection. XSLT 3.0's own wording for that
// error ends "...unless the user has indicated that the processor is to handle
// this situation by processing the stylesheet without streaming", and that is
// this engine's default: it builds the whole tree, so a streamable="yes"
// declaration it cannot prove is simply ignored rather than fatal. Turning it
// on makes the declaration binding, which is what a bounded-memory executor
// needs before it will honour one.
var strmEnforce atomic.Bool

// SetEnforceStreamability turns the XTSE3430 static check on or off, and
// returns the previous setting. Off (the default) means a stylesheet that
// declares streamability this processor cannot prove still compiles and runs
// buffered.
func SetEnforceStreamability(on bool) bool { return strmEnforce.Swap(on) }

// strmCheckStreamableModes is the whole static XTSE3430 gate: everything a
// stylesheet declares streamable must be provably guaranteed-streamable — its
// streamable modes (§19.8.10 + §19.8.3), its declared-streamable stylesheet
// functions (§19.8.5), and the body of every xsl:source-document that asks for
// streaming (§19.8.4.35). The name is the seam compile.go calls.
func strmCheckStreamableModes(ss *Stylesheet) error {
	if !strmEnforce.Load() {
		return nil
	}
	names := map[string]bool{}
	for name, md := range ss.modes {
		// streamable is an ordinary XSLT boolean attribute, so "true" and "1"
		// declare streamability just as "yes" does — and a shadow attribute
		// (_streamable="{$STREAMABLE}") normally expands to "true".
		if md != nil && isXSLTTrue(strings.TrimSpace(md.attrs["streamable"])) {
			names[name] = true
		}
	}
	if err := strmCheckModeRules(ss, names); err != nil {
		return err
	}
	if err := strmCheckStreamableFunctions(ss, names); err != nil {
		return err
	}
	if err := strmCheckStreamableAccumulators(ss, names); err != nil {
		return err
	}
	if err := strmCheckStreamableMergeSources(ss); err != nil {
		return err
	}
	return strmCheckStreamableSourceDocs(ss, names)
}

// strmCheckStreamableMergeSources enforces XTSE3430 on every xsl:merge-source
// that asks to be streamed. §15.4 states the conditions as a closed list, all
// of which must hold for the element to be guaranteed-streamable:
//
//   - it carries streamable="yes";
//   - the for-each-source attribute is present;
//   - the select expression, "assessed with a context posture of striding and
//     a context item type of U{document-node()}, has striding or grounded
//     posture and motionless or consuming sweep";
//   - sort-before-merge is "either absent or takes its default value of no".
//
// Unlike xsl:source-document, whose body this file deliberately does not
// police (see strmCheckStreamableSourceDocs's SCOPE note), every one of these
// reads a written attribute or the posture of ONE expression the analysis
// classifies completely — none of them can be reached by failing to prove
// something — so they are reported directly rather than through the Definite
// channel.
func strmCheckStreamableMergeSources(ss *Stylesheet) error {
	var err error
	strmEachBody(ss, func(owner *xmltree.Node, body []instruction) {
		if err != nil {
			return
		}
		strmWalkInstrs(body, func(in instruction) bool {
			mi, ok := in.(*mrgInstr)
			if !ok || err != nil {
				return true
			}
			for _, src := range mi.sources {
				if src == nil || !src.streamable {
					continue
				}
				at := src.el
				if at == nil {
					at = mi.el
				}
				if src.forEachSource == nil {
					err = errAt(at, "err:XTSE3430: xsl:merge-source is declared streamable but has no for-each-source attribute")
					return false
				}
				if src.sortBeforeMerge {
					err = errAt(at, "err:XTSE3430: xsl:merge-source is declared streamable but specifies sort-before-merge=\"yes\"")
					return false
				}
				sc := xpath.StreamContext{
					ContextPosture:    xpath.PostureStriding,
					ContextIsDocument: true,
				}
				p, s := src.sel.Streamability(sc)
				if (p != xpath.PostureStriding && p != xpath.PostureGrounded) ||
					(s != xpath.SweepMotionless && s != xpath.SweepConsuming) {
					err = errAt(at, "err:XTSE3430: xsl:merge-source is declared streamable but its select expression is %s and %s, not striding or grounded with a motionless or consuming sweep",
						strmPostureName(p), strmSweepName(s))
					return false
				}
			}
			return true
		})
	})
	return err
}

// strmCheckModeRules enforces XTSE3430 for every mode declared
// streamable="yes": each of its template rules must have a motionless match
// pattern (§19.8.10) and a guaranteed-streamable body, assessed with the
// striding context posture §19.6 gives a streamable mode's rules.
func strmCheckModeRules(ss *Stylesheet, names map[string]bool) error {
	if len(names) == 0 {
		return nil
	}
	for _, t := range ss.templates {
		if t == nil || t.pattern == nil {
			continue
		}
		for _, mode := range strmTemplateModes(t) {
			if !names[mode] {
				continue
			}
			psc := xpath.StreamContext{ContextPosture: xpath.PostureStriding}
			if p, s := t.pattern.Streamability(psc); !xpath.Streamable(p, s) {
				return errAt(t.el, "err:XTSE3430: template rule for streamable mode %s has a match pattern that is not motionless: %s",
					strmModeName(mode), t.matchSrc)
			}
			// §19.3: the context item type of a template rule's body is the
			// match type of its pattern, which is what §19.8.1 needs to
			// downgrade absorption to inspection — a rule matching @x, text()
			// or a non-node type was being analysed as if "." had children.
			sc := xpath.StreamContext{
				ContextPosture:    xpath.PostureStriding,
				ContextIsDocument: strmPatternIsDocument(t.pattern),
				ContextChildless:  t.pattern.StreamChildless(),
				CurrentChildless:  t.pattern.StreamChildless(),
			}
			a := &strmAnalyzer{ss: ss, streamableModes: names, knowModes: true}
			bp, bs, ok, err := strmClassifyInP(a, t.body, t.el, sc, "template rule for streamable mode "+strmModeName(mode))
			if !ok {
				return err
			}
			// §6.6.4 condition 4: "The type-adjusted posture of the sequence
			// constructor forming the body of the xsl:template element, with
			// respect to the U-type that corresponds to the declared return
			// type of the template (defaulting to item()*), is grounded."
			//
			// strmClassifyIn only establishes conditions 2 and 3 (a motionless
			// pattern, a body that is motionless or consuming); a STRIDING body
			// satisfies both and is still not guaranteed-streamable, because a
			// template rule cannot hand streamed nodes back to its caller. The
			// REC's own Note spells out why the adjustment matters: the body
			// may be "grounded as written", or become grounded because the
			// declared result type is atomic and the result is atomized.
			if ap, _ := xpath.StreamGeneralRules([]xpath.StreamOperand{
				{P: bp, S: bs, U: strmTypeAdjustUsage(t.as)},
			}); ap != xpath.PostureGrounded {
				return errAt(t.el, "err:XTSE3430: template rule for streamable mode %s is declared streamable but returns streamed nodes: its body is %s",
					strmModeName(mode), strmPostureName(ap))
			}
		}
	}
	return nil
}

// strmCheckStreamableFunctions enforces §19.8.5's constraints on the BODY of
// every declared-streamable xsl:function: the category fixes what posture and
// sweep the function result may have, and the result's posture and sweep are
// those of the contained sequence constructor type-adjusted by the declared
// return type.
//
// A stylesheet function has no context item, so the body is assessed with a
// roaming context posture: any use of the focus is then rejected, which is what
// a function with no focus deserves. The first parameter of a
// declared-streamable function is its streaming parameter (§19.8.5), and a
// reference to it is striding rather than grounded.
func strmCheckStreamableFunctions(ss *Stylesheet, modes map[string]bool) error {
	for _, key := range strmSortedFuncKeys(ss.functions) {
		fd := ss.functions[key]
		if fd == nil {
			continue
		}
		cat := strmFuncCat(fd.el)
		if cat == xpath.StreamFnUnclassified {
			continue
		}
		if len(fd.params) == 0 {
			// §19.8.5: "The only category permitted for a zero-arity function
			// is unclassified" — with no argument there is no streamed node to
			// make a promise about.
			return errAt(fd.el, "err:XTSE3155: xsl:function %s has no parameters, so its streamability category must be unclassified, not %q",
				clarkName(fd.name), strmCategoryName(cat))
		}
		// §19.8.5.3/.4/.5/.6/.7 "Rules for the function signature": for every
		// category but absorbing, "If the declared type of the streaming
		// parameter permits more than one node, the function is not
		// guaranteed-streamable." The spec's own note gives the reason: with a
		// sequence in the parameter, an expression such as ($input/name(),
		// $input/@id) would have to revisit several distinct nodes without
		// advancing the stream, which streaming cannot serve. Absorbing is
		// exempt because it reads each supplied subtree in turn, in order.
		if strmCatNeedsSingleNode(cat) && strmTypeManyNodes(strmParamType(fd.params[0])) {
			return errAt(fd.el, "err:XTSE3430: xsl:function %s is declared streamability=%q, whose streaming parameter must not permit more than one node, but $%s is declared as %q",
				clarkName(fd.name), strmCategoryName(cat), fd.params[0].name.Local, strmParamTypeName(fd.params[0]))
		}
		sc := xpath.StreamContext{ContextPosture: xpath.PostureRoaming}
		// §19.8.8.12: a reference to the streaming parameter is grounded (the
		// ordinary variable-reference rule) whenever the parameter's declared
		// type cannot hold a node at all — there is then nothing streamed to
		// leak out of it, and §19.8.5's category rules have nothing to say.
		if strmTypePermitsNodes(strmParamType(fd.params[0])) {
			sc.StreamingParams = map[string]bool{strmVarKey(fd.params[0]): true}
			// §19.8.5's per-category "Rules for references to the streaming
			// parameter": striding everywhere, and consuming only in an
			// absorbing function whose parameter permits more than one node.
			sc.StreamingParamPosture = xpath.PostureStriding
			sc.StreamingParamSweep = xpath.SweepMotionless
			switch {
			case cat == xpath.StreamFnAscent:
				// §19.8.5.7: "Such a variable reference is climbing and
				// motionless" — an ascent function may only look upwards, so the
				// parameter itself is treated as if it were already an ancestor.
				sc.StreamingParamPosture = xpath.PostureClimbing
			case cat == xpath.StreamFnAbsorbing && strmTypeManyNodes(strmParamType(fd.params[0])):
				sc.StreamingParamSweep = xpath.SweepConsuming
			}
			// Retained conservatism, not a Recommendation rule: the 2015 draft's
			// §19.8.8.11 "singular" test rejected a reference reached through a
			// higher-order operand, and the Recommendation dropped it — for
			// absorbing because a consuming reference now makes the general
			// rules reject the same shapes, for the rest because a motionless
			// reference to one node really can be re-read. Keeping it only ever
			// rejects more, which is the safe direction for this analysis.
			switch cat {
			case xpath.StreamFnInspection, xpath.StreamFnFilter, xpath.StreamFnAscent:
				sc.StreamingParamRepeatable = true
			}
		}
		a := &strmAnalyzer{ss: ss, el: fd.el, streamableModes: modes, knowModes: true}
		sc.Funcs = a
		p, s := a.strmBody(fd.body, sc)
		// §19.8.5: the function result is the type-adjusted posture and sweep
		// of the body, given the declared return type (default item()*).
		p, s = xpath.StreamGeneralRules([]xpath.StreamOperand{{P: p, S: s, U: strmTypeAdjustUsage(fd.as)}})
		if strmFuncResultOK(cat, p, s) && a.fail == nil {
			continue
		}
		why := fmt.Sprintf("its result is %s and %s", strmPostureName(p), strmSweepName(s))
		at := fd.el
		if a.fail != nil {
			why = a.fail.why
			if a.fail.el != nil {
				at = a.fail.el
			}
		}
		return errAt(at, "err:XTSE3430: xsl:function %s is declared streamability=%q but is not guaranteed-streamable: %s",
			clarkName(fd.name), strmCategoryName(cat), why)
	}
	return nil
}

// strmCheckStreamableAccumulators enforces §18.2.8 for every xsl:accumulator
// declared streamable="yes": every rule's match pattern must be motionless, and
// the initial value and every rule's value must be grounded and motionless.
func strmCheckStreamableAccumulators(ss *Stylesheet, modes map[string]bool) error {
	v, ok := acc2Registry.Load(ss)
	if !ok {
		return nil
	}
	defs, _ := v.([]*acc2Def)
	for _, def := range defs {
		if def == nil || !def.streamable {
			continue
		}
		// The initial value is evaluated with no context item at all, so a
		// roaming context posture rejects any use of the focus.
		isc := xpath.StreamContext{ContextPosture: xpath.PostureRoaming}
		if p, s := def.initial.Streamability(isc); p != xpath.PostureGrounded || s != xpath.SweepMotionless {
			return errAt(def.el, "err:XTSE3430: xsl:accumulator %s is declared streamable but its initial-value is %s and %s",
				clarkName(def.name), strmPostureName(p), strmSweepName(s))
		}
		for _, r := range def.rules {
			if r == nil {
				continue
			}
			psc := xpath.StreamContext{ContextPosture: xpath.PostureStriding}
			if p, s := r.match.Streamability(psc); !xpath.Streamable(p, s) {
				return errAt(r.el, "err:XTSE3430: xsl:accumulator %s is declared streamable but the match pattern %q of one of its rules is not motionless",
					clarkName(def.name), r.match.Src())
			}
			// The rule's value is evaluated with the matched node as its
			// focus, so a pattern that can only match childless nodes is what
			// lets select="string(.)" stay motionless.
			sc := xpath.StreamContext{
				ContextPosture:   xpath.PostureStriding,
				ContextChildless: r.match.StreamChildless(),
				// current() in a rule's value is the matched node, which is the
				// same node the focus is on here.
				CurrentChildless: r.match.StreamChildless(),
				AccPhase:         xpath.AccPhaseStart,
			}
			if r.end {
				sc.AccPhase = xpath.AccPhaseEnd
			}
			// $value holds the accumulator's value so far, so the
			// accumulator's own @as IS its declared type. When that type is a
			// map or array, §19.8.8.11's Note applies to a lookup written as
			// $value(@type) exactly as it would to any other variable of that
			// type (accumulator-053).
			if strmTypeIsMapOrArray(def.as) {
				sc.MapArrayVars = strmWithMapArrayVar(sc.MapArrayVars, ":value")
			}
			a := &strmAnalyzer{ss: ss, el: r.el, streamableModes: modes, knowModes: true}
			sc.Funcs = a
			var p xpath.Posture
			var s xpath.Sweep
			var childless bool
			if r.sel != nil {
				p, s = r.sel.Streamability(sc)
				childless = r.sel.StreamChildlessIn(sc)
			} else {
				p, s = a.strmBody(r.body, sc)
			}
			// The accumulator's @as applies the function conversion rules to
			// every rule result, so the value the accumulator actually holds is
			// the type-adjusted one: select="@amount" on an xs:double
			// accumulator atomizes the attribute and is grounded.
			p, s = xpath.StreamGeneralRules([]xpath.StreamOperand{
				{P: p, S: s, U: strmTypeAdjustUsage(def.as), Childless: childless},
			})
			if p != xpath.PostureGrounded || s != xpath.SweepMotionless {
				return errAt(r.el, "err:XTSE3430: xsl:accumulator %s is declared streamable but one of its rules computes a value that is %s and %s",
					clarkName(def.name), strmPostureName(p), strmSweepName(s))
			}
		}
	}
	return nil
}

// strmCheckStreamableSourceDocs enforces XTSE3430 on the body of an
// xsl:source-document that asks for streaming (§19.8.4.35). Unlike a streamable
// mode, the instruction is reached from anywhere in the stylesheet, so the walk
// covers every compiled body the stylesheet owns.
//
// SCOPE — every such body is CLASSIFIED, but only some are DIAGNOSED.
// XTSE3430's own wording ends "...unless the user has indicated that the
// processor is to handle this situation by processing the stylesheet without
// streaming", and that fallback is this engine's default wherever its analysis
// is incomplete. Reporting every body this analysis cannot prove was measured
// against the W3C suite twice and rejects roughly 1,200 stylesheets — 62 cases
// won against 754 lost — so incompleteness stays silent. A body is diagnosed
// on either of two grounds instead:
//
//   - A rule the Recommendation states UNCONDITIONALLY produced the verdict,
//     reported through xpath.StreamContext.Definite. Those rules read only
//     things that are given rather than inferred — a context posture
//     (§19.8.9.14, fn:last) or the presence of an attribute (§19.8.6, an
//     attribute set not declared streamable) — so reaching one is a fact about
//     the stylesheet, not about this engine's reach.
//   - The body calls a declared-streamable stylesheet function: §19.8.5 is
//     implemented in full here, and it is the part a stylesheet opts into
//     explicitly.
//
// The two are ANDed with the classification failing, never consulted on their
// own — a definite rule can fire inside a subexpression that a later rule
// recovers (§19.8.8.7's scanning reassessment), and that body must still pass.
func strmCheckStreamableSourceDocs(ss *Stylesheet, modes map[string]bool) error {
	var err error
	strmEachBody(ss, func(owner *xmltree.Node, body []instruction) {
		if err != nil {
			return
		}
		strmWalkInstrs(body, func(in instruction) bool {
			sd, ok := in.(*sdSourceDocument)
			if !ok || err != nil {
				return true
			}
			if v, has := sd.el.AttrLocal("streamable"); !has || !isXSLTTrue(strings.TrimSpace(v)) {
				return true
			}
			// The fresh context below asserts more than "the context item is a
			// striding document node": it also asserts that nothing outside
			// this instruction is in scope. That is false when the instruction
			// is written INSIDE an xsl:for-each-group or xsl:merge, whose
			// current group the body may legitimately reach (si-group-051
			// selects //transaction[@date = current-group()[1]/Date] inside a
			// nested xsl:source-document, over a GROUNDED population). This
			// walk visits every body in the stylesheet on its own, so it has no
			// enclosing group to hand; rather than classify against a premise
			// it cannot check, it leaves such an instruction alone — the
			// enclosing construct's own analysis still classifies it through
			// strmSourceDocument, which does have the group in context.
			if strmInsideGroupingElement(sd.el) {
				return true
			}
			// §19.6: the body of a streamed source document sees a striding
			// context item that is statically a document node.
			var definite bool
			sc := xpath.StreamContext{
				ContextPosture:    xpath.PostureStriding,
				ContextIsDocument: true,
				Definite:          &definite,
			}
			a := &strmAnalyzer{ss: ss, el: sd.el, streamableModes: modes, knowModes: true}
			bp, _, ok2, cerr := strmClassifyInP(a, sd.body, sd.el, sc, "xsl:source-document")
			if ok2 {
				// §18.1 gives the instruction exactly two conditions, jointly
				// necessary and sufficient: it is declared streamable (checked
				// above), and "the contained sequence constructor is GROUNDED,
				// as assessed using the streamability analysis in 19
				// Streamability". strmClassifyInP only establishes the weaker
				// Streamable(posture, sweep) — a STRIDING body satisfies that
				// and is still not guaranteed-streamable, because, as §18.1's
				// own Note puts it, the rules "ensure that the sequence
				// constructor ... cannot return any nodes from the streamed
				// document ... it cannot contain the instruction <xsl:sequence
				// select="//chapter"/>".
				//
				// A verdict reached HERE is definite in the sense
				// StreamContext.Definite means: the analysis did not fail to
				// prove something — it succeeded, and the posture it proved is
				// one §18.1 rules out. That is a fact about the stylesheet, so
				// unlike an incompleteness verdict it justifies XTSE3430.
				if bp != xpath.PostureGrounded {
					err = errAt(sd.el, "err:XTSE3430: xsl:source-document is declared streamable but is not guaranteed-streamable: its sequence constructor is %s rather than grounded, so it returns nodes from the streamed document",
						strmPostureName(bp))
				}
				return true
			}
			// The body did not classify as streamable. Whether that is worth
			// REPORTING is the question this gate answers — see the SCOPE note
			// above. Two independent grounds, either sufficient:
			//   - a rule the REC states unconditionally produced the verdict
			//     (xpath.StreamContext.Definite), so the stylesheet really is
			//     not guaranteed-streamable; or
			//   - the body calls a declared-streamable stylesheet function,
			//     the §19.8.5 analysis that is complete here and that the
			//     stylesheet opted into explicitly.
			// Anything else is this analysis being incomplete, and falls back
			// to buffered execution silently, as it always has.
			if definite || strmCallsDeclaredStreamable(ss, sd.body, a) {
				err = cerr
			}
			return true
		})
	})
	return err
}

// strmInsideGroupingElement reports whether el is written inside an
// xsl:for-each-group or xsl:merge, the two instructions that bind a current
// group an inner expression may refer to.
func strmInsideGroupingElement(el *xmltree.Node) bool {
	if el == nil {
		return false
	}
	for p := el.Parent; p != nil; p = p.Parent {
		if p.Kind != xmltree.KindElement || p.Name.Space != NS {
			continue
		}
		switch p.Name.Local {
		case "for-each-group", "merge":
			return true
		}
	}
	return false
}

// strmCallsDeclaredStreamable reports whether a body calls a stylesheet
// function declared with a streamability category, at any depth.
func strmCallsDeclaredStreamable(ss *Stylesheet, body []instruction, f xpath.StreamFuncs) bool {
	found := false
	check := func(p *xpath.Parsed) {
		if p != nil && p.StreamUsesDeclaredStreamable(f) {
			found = true
		}
	}
	strmWalkInstrs(body, func(in instruction) bool {
		sh, ok := in.(strmShaper)
		if !ok {
			return true
		}
		s := sh.strmShape()
		check(s.sel)
		check(s.key)
		for _, r := range s.ops {
			check(r.expr)
			if r.avt != nil {
				for _, part := range r.avt.parts {
					check(part.expr)
				}
			}
		}
		return !found
	})
	return found
}

// strmEachBody calls fn for every sequence constructor the stylesheet owns, so
// a check that must reach EVERY instruction (not just those reachable from a
// streamable mode) has one place to enumerate them.
func strmEachBody(ss *Stylesheet, fn func(owner *xmltree.Node, body []instruction)) {
	seen := map[*Template]bool{}
	visit := func(t *Template) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		fn(t.el, t.body)
		for _, p := range t.params {
			if p != nil {
				fn(p.el, p.body)
			}
		}
	}
	for _, t := range ss.templates {
		visit(t)
	}
	for _, name := range strmSortedTemplateKeys(ss.named) {
		visit(ss.named[name])
	}
	for _, key := range strmSortedFuncKeys(ss.functions) {
		if fd := ss.functions[key]; fd != nil {
			fn(fd.el, fd.body)
		}
	}
	for _, vd := range ss.globals {
		if vd != nil {
			fn(vd.el, vd.body)
		}
	}
	for _, k := range ss.keys {
		if k != nil {
			fn(k.el, k.body)
		}
	}
	for _, name := range strmSortedAttrSetKeys(ss.attrSets) {
		for _, decl := range ss.attrSets[name] {
			fn(nil, decl.body)
		}
	}
}

// strmWalkInstrs visits every instruction of a sequence constructor and of the
// sub-constructors reachable through its shape. fn returns false to stop.
func strmWalkInstrs(body []instruction, fn func(instruction) bool) {
	stop := false
	var walk func([]instruction)
	walk = func(is []instruction) {
		for _, in := range is {
			if stop || in == nil {
				return
			}
			if !fn(in) {
				stop = true
				return
			}
			sh, ok := in.(strmShaper)
			if !ok {
				continue
			}
			s := sh.strmShape()
			for _, r := range s.ops {
				walk(r.body)
				for _, vd := range r.params {
					if vd != nil {
						walk(vd.body)
					}
				}
				for _, sk := range r.sorts {
					walk(sk.body)
				}
			}
			walk(s.body)
			walk(s.onDone)
			walk(s.forkBranches)
			for _, sk := range s.sorts {
				walk(sk.body)
			}
			for _, vd := range s.params {
				if vd != nil {
					walk(vd.body)
				}
			}
			if s.forkGroup != nil {
				walk([]instruction{s.forkGroup})
			}
		}
	}
	walk(body)
}

func strmSortedFuncKeys(m map[string]*FuncDef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func strmSortedTemplateKeys(m map[string]*Template) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func strmSortedAttrSetKeys(m map[string][]attrSetDecl) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// strmTemplateModes lists the mode names a template rule applies to.
func strmTemplateModes(t *Template) []string {
	if len(t.modeToks) > 0 {
		return t.modeToks
	}
	return []string{t.mode}
}

func strmModeName(name string) string {
	if name == "" {
		return "#unnamed"
	}
	return name
}

// strmPatternIsDocument reports whether a match pattern can only match a
// document node, which is what lets a leading "/" in the body collapse to the
// context item (§19.8.9.18).
func strmPatternIsDocument(p *xpath.Pattern) bool {
	if p == nil {
		return false
	}
	switch strings.TrimSpace(p.Src()) {
	case "/", "document-node()":
		return true
	}
	return false
}

// strmForkContent validates the xsl:fork content model (§16.1) and splits it
// into the two permitted shapes. A violation is XTSE0010 — the content of the
// element does not correspond to what is allowed — not XTSE3430, which is
// reserved for a construct that is well-formed but not guaranteed-streamable.
func strmForkContent(el *xmltree.Node) (branches []*xmltree.Node, group *xmltree.Node, err error) {
	for _, ch := range elementChildren(el) {
		if ch.Name.Space != NS {
			return nil, nil, errAt(el, "err:XTSE0010: xsl:fork content must be xsl:sequence elements or a single xsl:for-each-group")
		}
		switch ch.Name.Local {
		case "fallback":
			// Ignored by an XSLT 3.0 processor, permitted anywhere.
		case "sequence":
			if group != nil {
				return nil, nil, errAt(el, "err:XTSE0010: xsl:fork must not mix xsl:sequence with xsl:for-each-group")
			}
			branches = append(branches, ch)
		case "for-each-group":
			if group != nil || len(branches) > 0 {
				return nil, nil, errAt(el, "err:XTSE0010: xsl:fork may contain at most one xsl:for-each-group, and not alongside xsl:sequence")
			}
			group = ch
		default:
			return nil, nil, errAt(el, "err:XTSE0010: xsl:fork must not contain xsl:%s", ch.Name.Local)
		}
	}
	return branches, group, nil
}

// --- per-instruction shapes (§19.8.4) ---
//
// These live here rather than beside each instruction so the whole rule table
// can be read — and audited against the spec — in one place. Go lets a method
// be declared in any file of the package that owns the type.

// strmOp stamps one usage onto a run of operand references.
func strmOp(u xpath.StreamUsage, refs ...strmOperandRef) []strmOperandRef {
	out := make([]strmOperandRef, 0, len(refs))
	for _, r := range refs {
		r.usage = u
		out = append(out, r)
	}
	return out
}

// §19.8.4.1 Literal result elements.
func (n *litElement) strmShape() strmShape {
	ops := []strmOperandRef{{body: n.body, usage: xpath.UsageAbsorption}}
	for _, at := range n.attrs {
		ops = append(ops, strmOperandRef{avt: at.value, usage: xpath.UsageAbsorption})
	}
	return strmShape{rule: strmRuleGeneral, ops: ops, useSets: n.useSets, el: n.el, what: "literal result element"}
}

// §19.8.3: literal text in a sequence constructor has no operands.
func (n *litText) strmShape() strmShape {
	return strmShape{rule: strmRuleNoOperands, what: "text"}
}

// §19.8.4.38 xsl:value-of.
func (n *valueOf) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:value-of",
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{expr: n.sel},
			strmOperandRef{avt: n.sep},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.5 xsl:apply-templates.
func (n *applyTemplates) strmShape() strmShape {
	return strmShape{rule: strmRuleApplyTemplates, el: n.el, what: "xsl:apply-templates",
		sel: n.sel, sorts: n.sorts, params: n.params, mode: n.mode}
}

// §19.8.4.18 xsl:for-each.
func (n *forEach) strmShape() strmShape {
	return strmShape{rule: strmRuleForEach, el: n.el, what: "xsl:for-each",
		sel: n.sel, sorts: n.sorts, body: n.body}
}

// §19.8.4.21 xsl:if.
func (n *ifInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:if",
		ops: []strmOperandRef{
			{expr: n.test, usage: xpath.UsageInspection},
			{body: n.body, usage: xpath.UsageTransmission},
		}}
}

// §19.8.4.10 xsl:choose — the branch bodies form one choice operand group, so
// each may consume the stream provided the tests are motionless.
func (n *chooseInstr) strmShape() strmShape {
	var ops []strmOperandRef
	for _, w := range n.whens {
		ops = append(ops,
			strmOperandRef{expr: w.test, usage: xpath.UsageInspection},
			strmOperandRef{body: w.body, usage: xpath.UsageTransmission, choice: 1})
	}
	ops = append(ops, strmOperandRef{body: n.otherwise, usage: xpath.UsageTransmission, choice: 1})
	return strmShape{rule: strmRuleGeneral, ops: ops, what: "xsl:choose"}
}

// §19.8.4.39 xsl:variable / xsl:param: with @as the usage is type-determined,
// without it the initialiser navigates — which is what stops a variable being
// bound to a streamed node.
func (n *localVar) strmShape() strmShape {
	vd := n.def
	if vd == nil {
		return strmShape{rule: strmRuleNoOperands, what: "xsl:variable"}
	}
	selU, bodyU := xpath.UsageNavigation, xpath.UsageAbsorption
	// §19.8.4.41 gives the select expression navigation usage only when there
	// is NO as attribute; with one, the usage is type-determined, and a
	// navigation that comes out of strmTypeUsage may just mean the type was
	// item()* or unrecognised. So only the no-@as case is the REC speaking.
	recSel := true
	if strings.TrimSpace(vd.as) != "" {
		selU = strmTypeUsage(vd.as)
		bodyU = selU
		// With an as attribute the usage is type-determined, and only a
		// written node or item type makes navigation the REC's answer rather
		// than this analysis's fall-through.
		recSel = strmTypeNavigationCertain(vd.as)
	}
	return strmShape{rule: strmRuleGeneral, el: vd.el, what: "xsl:variable",
		ops: []strmOperandRef{
			{expr: vd.sel, usage: selU, recUsage: recSel},
			{body: vd.body, usage: bodyU},
		}}
}

// §19.8.4.9 xsl:call-template: the context item is passed implicitly with the
// usage its xsl:context-item declares, defaulting to item()* — navigation.
func (n *callTemplate) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:call-template",
		ops: []strmOperandRef{
			{ctxItem: true, usage: xpath.UsageNavigation},
			{params: n.params},
		}}
}

// §19.8.4.7 xsl:attribute.
func (n *attrInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:attribute",
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{avt: n.name},
			strmOperandRef{avt: n.ns},
			strmOperandRef{expr: n.sel},
			strmOperandRef{avt: n.sep},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.15 xsl:element.
func (n *elemInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:element", useSets: n.useSets,
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{avt: n.name},
			strmOperandRef{avt: n.ns},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.36 xsl:text.
func (n *textInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleNoOperands, what: "xsl:text"}
}

// §19.8.4.12 xsl:copy: the node itself is only inspected (a shallow copy), and
// the content is assessed with the selected node as its focus.
func (n *copyInstr) strmShape() strmShape {
	// §19.8.4.12's Note: "when a select attribute is present, the sequence
	// constructor contained by the xsl:copy instruction is deemed to be a
	// higher-order operand of the instruction, even though it can only be
	// evaluated once" — with the REC's own worked example of what that rules
	// out being an xsl:copy select=".." whose body copies current-group().
	// Without a @select the body runs in the outer focus and is an ordinary
	// operand.
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:copy", useSets: n.useSets, sel: n.sel,
		ops: []strmOperandRef{
			{expr: n.sel, usage: xpath.UsageInspection},
			{body: n.body, usage: xpath.UsageAbsorption, selFocus: true, higherOrder: n.sel != nil},
		}}
}

// §19.8.4.13 xsl:copy-of.
func (n *copyOf) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:copy-of",
		ops: []strmOperandRef{{expr: n.sel, usage: xpath.UsageAbsorption}}}
}

// §19.8.4.11 xsl:comment.
func (n *commentInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:comment",
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{expr: n.sel},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.32 xsl:processing-instruction.
func (n *piInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:processing-instruction",
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{avt: n.name},
			strmOperandRef{expr: n.sel},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.34 xsl:sequence — the one instruction that transmits streamed nodes.
func (n *sequenceInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:sequence",
		ops: strmOp(xpath.UsageTransmission,
			strmOperandRef{expr: n.sel},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.3 xsl:analyze-string: the two branches navigate (they run more than
// once) over a grounded focus — their context item is an xs:string.
func (n *analyzeString) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:analyze-string",
		ops: []strmOperandRef{
			{expr: n.sel, usage: xpath.UsageAbsorption},
			{avt: n.regex, usage: xpath.UsageAbsorption},
			{avt: n.flags, usage: xpath.UsageAbsorption},
			{body: n.matching, usage: xpath.UsageNavigation, groundedFocus: true},
			{body: n.nonMatching, usage: xpath.UsageNavigation, groundedFocus: true},
		}}
}

// §19.8.4.4 / §19.8.4.29 xsl:apply-imports and xsl:next-match: an implicit
// context-item operand with usage absorption, plus the parameters.
func (n *nextMatch) strmShape() strmShape {
	what := "xsl:next-match"
	if n.importsOnly {
		what = "xsl:apply-imports"
	}
	return strmShape{rule: strmRuleGeneral, el: n.el, what: what,
		ops: []strmOperandRef{
			{ctxItem: true, usage: xpath.UsageAbsorption},
			{params: n.params},
		}}
}

// §19.8.4.19 xsl:for-each-group.
func (n *fegInstr) strmShape() strmShape {
	sh := strmShape{rule: strmRuleForEachGroup, el: n.el, what: "xsl:for-each-group",
		sel: n.sel, key: n.key, sorts: n.sorts, body: n.body,
		groupBy: n.mode == fegGroupBy,
		ops:     []strmOperandRef{{avt: n.collation, usage: xpath.UsageAbsorption}},
	}
	if n.pat != nil {
		sh.pats = []*xpath.Pattern{n.pat}
	}
	return sh
}

// §19.8.4.16 xsl:evaluate.
func (n *evEvaluate) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:evaluate",
		ops: []strmOperandRef{
			{expr: n.xpath, usage: xpath.UsageAbsorption},
			{expr: n.ctx, usage: xpath.UsageNavigation},
			{expr: n.withParams, usage: xpath.UsageNavigation},
			{expr: n.nsCtx, usage: xpath.UsageInspection},
			{avt: n.baseURI, usage: xpath.UsageAbsorption},
			{params: n.params},
		}}
}

// §19.8.4.22 xsl:iterate.
func (n *itrIterate) strmShape() strmShape {
	return strmShape{rule: strmRuleIterate, el: n.el, what: "xsl:iterate",
		sel: n.sel, params: n.params, body: n.body, onDone: n.onCompletion}
}

// §19.8.4.28 xsl:next-iteration.
func (n *itrNextInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:next-iteration",
		ops: []strmOperandRef{{params: n.params}}}
}

// §19.8.4.8 xsl:break.
func (n *itrBreakInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:break",
		ops: strmOp(xpath.UsageTransmission,
			strmOperandRef{expr: n.sel},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.2 unrecognized extension instructions and forwards-compatible XSLT
// elements: only their xsl:fallback content counts.
func (n *extInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "extension instruction " + n.name,
		ops: []strmOperandRef{{body: n.body, usage: xpath.UsageTransmission}}}
}

func (n *fwdCompatInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:" + n.name,
		ops: []strmOperandRef{{body: n.body, usage: xpath.UsageTransmission}}}
}

// §19.8.3: a text value template is an absorption operand of its sequence
// constructor.
func (n *tvtText) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "text value template",
		ops: []strmOperandRef{{avt: n.a, usage: xpath.UsageAbsorption}}}
}

// §19.8.4.27 xsl:namespace.
func (n *nsInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:namespace",
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{avt: n.name},
			strmOperandRef{expr: n.sel},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.14 xsl:document.
func (n *docInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, what: "xsl:document",
		ops: []strmOperandRef{{body: n.body, usage: xpath.UsageAbsorption}}}
}

// xsl:where-populated has no entry in this draft. It is the sibling of
// xsl:on-empty, which §19.8.3 makes an ordinary transmission operand of its
// sequence constructor, so its content is treated the same way.
func (n *wherePopulated) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, what: "xsl:where-populated",
		ops: []strmOperandRef{{body: n.body, usage: xpath.UsageTransmission}}}
}

// §19.8.4.20 xsl:fork.
func (n *forkInstr) strmShape() strmShape {
	return strmShape{rule: strmRuleFork, el: n.el, what: "xsl:fork",
		body: n.body, forkBranches: n.branches, forkGroup: n.group}
}

// §19.8.3: xsl:on-empty / xsl:on-non-empty are ordinary transmission operands
// and get no special treatment for being mutually exclusive.
func (n *onEmptyInstr) strmShape() strmShape {
	what := "xsl:on-empty"
	if n.nonEmpty {
		what = "xsl:on-non-empty"
	}
	return strmShape{rule: strmRuleGeneral, el: n.el, what: what,
		ops: strmOp(xpath.UsageTransmission,
			strmOperandRef{expr: n.sel},
			strmOperandRef{body: n.body},
		)}
}

// condContent is the compiler's wrapper around a body carrying on-empty /
// on-non-empty parts; it classifies as the sequence constructor it stands for.
func (n *condContent) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, what: "sequence constructor",
		ops: []strmOperandRef{{body: n.parts, usage: xpath.UsageTransmission}}}
}

// §19.8.4.25 xsl:merge: the sources must select grounded, motionless input;
// the action then runs over grounded merge groups.
func (n *mrgInstr) strmShape() strmShape {
	var ops []strmOperandRef
	for _, src := range n.sources {
		if src == nil {
			continue
		}
		// §19.8.4.25 requires @select to be grounded and motionless only when
		// NEITHER for-each-item nor for-each-source is present. With either of
		// them @select is evaluated against THAT input's own root, never the
		// enclosing stream, so it is assessed with a grounded focus — which is
		// how "not an operand of this stream at all" is spelled here, and keeps
		// the field visibly carried rather than silently dropped.
		ops = append(ops,
			strmOperandRef{expr: src.sel, usage: xpath.UsageNavigation,
				groundedFocus: src.forEachItem != nil || src.forEachSource != nil},
			strmOperandRef{expr: src.forEachItem, usage: xpath.UsageNavigation},
			strmOperandRef{expr: src.forEachSource, usage: xpath.UsageNavigation})
		for _, k := range src.keys {
			if k == nil {
				continue
			}
			ops = append(ops,
				strmOperandRef{expr: k.sel, usage: xpath.UsageAbsorption, groundedFocus: true},
				strmOperandRef{body: k.body, usage: xpath.UsageAbsorption, groundedFocus: true})
		}
	}
	return strmShape{rule: strmRuleMerge, el: n.el, what: "xsl:merge", ops: ops, body: n.action}
}

// §19.8.4.31 xsl:perform-sort: the population navigates, because sorting does
// not preserve document order.
func (n *performSort) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:perform-sort", sel: n.sel,
		ops: []strmOperandRef{
			{expr: n.sel, usage: xpath.UsageNavigation},
			{body: n.body, usage: xpath.UsageNavigation},
			{sorts: n.sorts, selFocus: true},
		}}
}

// §19.8.4.30 xsl:number: @value absorbs, but the node being numbered
// navigates, so streamed nodes cannot be numbered.
func (n *num2Number) strmShape() strmShape {
	ops := []strmOperandRef{{expr: n.value, usage: xpath.UsageAbsorption}}
	switch {
	case n.sel != nil:
		ops = append(ops, strmOperandRef{expr: n.sel, usage: xpath.UsageNavigation})
	case n.value == nil:
		// No @value and no @select: the instruction numbers the context item.
		ops = append(ops, strmOperandRef{ctxItem: true, usage: xpath.UsageNavigation})
	}
	for _, t := range []*avt{n.format, n.groupSep, n.groupSz, n.ordinal, n.startAt, n.lang} {
		ops = append(ops, strmOperandRef{avt: t, usage: xpath.UsageAbsorption})
	}
	for _, p := range []*xpath.Pattern{n.count, n.from} {
		ops = append(ops, strmOperandRef{pat: p, usage: xpath.UsageInspection, higherOrder: true})
	}
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:number", ops: ops}
}

// §19.8.4.33 xsl:result-document.
func (n *rdResultDocument) strmShape() strmShape {
	ops := []strmOperandRef{
		{avt: n.href, usage: xpath.UsageAbsorption},
		{avt: n.method, usage: xpath.UsageAbsorption},
		{avt: n.format, usage: xpath.UsageAbsorption},
		{avt: n.paramDoc, usage: xpath.UsageAbsorption},
		{body: n.body, usage: xpath.UsageAbsorption},
	}
	for _, k := range strmSortedKeys(n.extraAttrs) {
		ops = append(ops, strmOperandRef{avt: n.extraAttrs[k], usage: xpath.UsageAbsorption})
	}
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:result-document", ops: ops}
}

// §19.8.4.23 xsl:map.
func (n *mpMap) strmShape() strmShape {
	return strmShape{rule: strmRuleMap, el: n.el, what: "xsl:map", body: n.body}
}

// §19.8.4.24 xsl:map-entry: the value navigates, so a map cannot hold streamed
// nodes.
func (n *mpEntry) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:map-entry",
		ops: []strmOperandRef{
			{expr: n.key, usage: xpath.UsageAbsorption},
			{expr: n.sel, usage: xpath.UsageNavigation},
			{body: n.body, usage: xpath.UsageNavigation},
		}}
}

// §19.8.4.26 xsl:message.
func (n *msgMessage) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:message",
		ops: strmOp(xpath.UsageAbsorption,
			strmOperandRef{expr: n.sel},
			strmOperandRef{avt: n.terminate},
			strmOperandRef{body: n.body},
		)}
}

// §19.8.4.6 xsl:assert.
func (n *msgAssert) strmShape() strmShape {
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:assert",
		ops: []strmOperandRef{
			{expr: n.test, usage: xpath.UsageInspection},
			{expr: n.sel, usage: xpath.UsageAbsorption},
			{body: n.body, usage: xpath.UsageAbsorption},
		}}
}

// §19.8.4.35 xsl:source-document (xsl:stream in this draft): it opens its own
// stream, so relative to the enclosing one only its @href counts.
func (n *sdSourceDocument) strmShape() strmShape {
	return strmShape{rule: strmRuleSourceDocument, el: n.el, what: "xsl:source-document",
		body: n.body,
		ops:  []strmOperandRef{{avt: n.href, usage: xpath.UsageAbsorption}}}
}

// §19.8.4.37 xsl:try: the try body and the catch bodies form a choice operand
// group, so either side — but not both — may consume the stream.
func (n *tryCatch) strmShape() strmShape {
	ops := []strmOperandRef{
		{expr: n.sel, usage: xpath.UsageTransmission},
		{body: n.body, usage: xpath.UsageTransmission},
	}
	for _, c := range n.catches {
		if c == nil {
			continue
		}
		ops = append(ops,
			strmOperandRef{expr: c.sel, usage: xpath.UsageTransmission, choice: 1},
			strmOperandRef{body: c.body, usage: xpath.UsageTransmission, choice: 1})
	}
	return strmShape{rule: strmRuleGeneral, el: n.el, what: "xsl:try", ops: ops}
}

// tcNoop is a standalone xsl:catch, already folded into its xsl:try.
func (n *tcNoop) strmShape() strmShape {
	return strmShape{rule: strmRuleNoOperands, what: "xsl:catch"}
}

func strmSortedKeys(m map[string]*avt) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
