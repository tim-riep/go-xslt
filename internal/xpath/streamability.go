package xpath

// Guaranteed-streamability analysis for XPath expressions and XSLT match
// patterns (XSLT 3.0 §19, "Streamability").
//
// SAFETY DIRECTION — this file fails CLOSED, the opposite of var_refs.go.
// walkVarRefs may miss an Expr kind and merely under-report a static error;
// missing a kind HERE would declare an expression safe to evaluate in one
// forward pass when it is not, so the engine would silently produce wrong
// output. Every switch therefore ends in a default that returns
// PostureRoaming/SweepFreeRanging ("not streamable"), and every place the
// analysis lacks the static type information the spec assumes is resolved
// towards the wider sweep.
//
// The AST stays unexported: like UsesVariable, this is a narrow read-only walk
// published as a method on *Parsed / *Pattern.

// Posture classifies how the nodes a construct returns relate to the streamed
// input document (§19.5).
type Posture uint8

const (
	// PostureGrounded: the value contains no nodes from the streamed input.
	PostureGrounded Posture = iota
	// PostureClimbing: nodes reached via parent/ancestor[-or-self]/attribute/
	// namespace from the current streaming position.
	PostureClimbing
	// PostureCrawling: nodes reached via descendant[-or-self], so potentially
	// nested — no further downward navigation is permitted.
	PostureCrawling
	// PostureStriding: peer nodes in document order, none an ancestor of
	// another.
	PostureStriding
	// PostureRoaming: nodes could be anywhere; never streamable.
	PostureRoaming
)

// Sweep measures how far the input-stream position moves while a construct is
// evaluated (§19.7). The values are ordered: motionless < consuming <
// free-ranging.
type Sweep uint8

const (
	// SweepMotionless: evaluable without moving the stream position.
	SweepMotionless Sweep = iota
	// SweepConsuming: repositions the stream over the current node's subtree.
	SweepConsuming
	// SweepFreeRanging: needs data outside the current subtree; never
	// streamable.
	SweepFreeRanging
)

// AccRulePhase says which phase of an xsl:accumulator-rule a construct belongs
// to, or that it belongs to none.
type AccRulePhase uint8

const (
	// AccPhaseNone: the construct is not inside an xsl:accumulator-rule.
	AccPhaseNone AccRulePhase = iota
	// AccPhaseStart: a pre-descent rule, where no post-descent accumulator
	// value for the node can be available yet.
	AccPhaseStart
	// AccPhaseEnd: a post-descent rule, where they all are.
	AccPhaseEnd
)

// Streamable reports whether a (posture, sweep) pair is acceptable. §19.7:
// roaming or free-ranging — in either combination — means not streamable.
func Streamable(p Posture, s Sweep) bool {
	return p != PostureRoaming && s != SweepFreeRanging
}

// StreamContext carries what the containing construct knows about the focus at
// the point an expression is evaluated.
type StreamContext struct {
	// ContextPosture is the posture of the context item (§19.6).
	ContextPosture Posture
	// ContextIsDocument records that the context item is statically known to
	// be a document node. The analysis has no type inferencer, and §19.8.9.18
	// makes a leading "/" collapse to the context item only in that case —
	// without this flag every absolute path would have to fail closed.
	ContextIsDocument bool
	// ContextChildless records that the context item cannot have children (an
	// attribute, a text node, a comment, a PI). It stands in for the static
	// context item type §19.8.1 needs to downgrade absorption to inspection,
	// and is what makes the spec's own motionless pattern
	// text()[starts-with(., '$')] come out motionless.
	ContextChildless bool
	// CurrentPosture is ContextPosture for the item fn:current() returns, and
	// HasCurrentPosture says whether the caller supplied it. The two are
	// separate because the zero Posture is grounded, the PERMISSIVE answer,
	// which a fail-closed analysis must never reach by default: an
	// unsupplied field leaves fn:current() at its old fixed striding.
	//
	// It is needed because fn:current() denotes the context item of the
	// INSTRUCTION whose attribute holds the expression, which is not the
	// context item the expression itself is walking once a predicate or a
	// path step has moved the focus — the same distinction CurrentChildless
	// already draws, for the same reason. The posture of that item is not
	// always striding: inside xsl:for-each select="1 to 5" the current item
	// is a grounded integer, so (20 to 21)[. gt current()] is motionless
	// rather than consuming (strm/sf-current's c-005).
	CurrentPosture    Posture
	HasCurrentPosture bool
	// CurrentChildless is ContextChildless for the node fn:current() returns —
	// the node the CONTAINING PATTERN matches, which is not the context item
	// once a predicate sits on a non-final step. Kept separate so that
	// text()[$x = current()] comes out motionless (the matched node is a text
	// node, so atomizing it reads no children) without weakening the ordinary
	// context-item rule.
	CurrentChildless bool
	// StreamingParams names variables bound to the streaming parameter of a
	// declared-streamable stylesheet function, keyed "prefix:local"
	// (§19.8.8.11). A reference to one is not grounded.
	StreamingParams map[string]bool
	// MapArrayVars names variables whose declared type makes them statically
	// known to hold a map or an array. §19.8.8.11 turns on exactly that
	// knowledge: a dynamic function call's arguments take their usage from the
	// base expression's static signature, and its Note spells out the map case
	// — "Maps and arrays are functions ... If it is statically known that the
	// function in question is a map or array, then it is also known that the
	// argument type is xs:anyAtomicType, and that the operand usage is
	// therefore absorption." Without that knowledge the section's own fallback
	// applies and every argument is navigation, which is why the set only ever
	// makes the analysis more precise, never more permissive than the REC.
	MapArrayVars map[string]bool
	// StreamingParamRepeatable says the function's category still permits a
	// reference to the streaming parameter inside a HIGHER-ORDER operand — one
	// evaluated repeatedly. This is the 2015 draft's §19.8.8.11 "singular"
	// test, which the Recommendation dropped once per-category reference rules
	// covered the same ground; it is kept because it only ever rejects more.
	StreamingParamRepeatable bool
	// StreamingParamPosture is the posture §19.8.5's "Rules for references to
	// the streaming parameter" give such a reference: striding for every
	// category but ascent, which gives climbing.
	StreamingParamPosture Posture
	// StreamingParamSweep is the sweep those same rules give the reference.
	// It is motionless everywhere except in an ABSORBING function whose
	// streaming parameter permits more than one node: reaching the second and
	// later nodes of that sequence means advancing the stream, so a single
	// reference is consuming and two of them can no longer combine.
	StreamingParamSweep Sweep
	// inHigherOrder is set while the walk is inside a higher-order operand.
	inHigherOrder bool
	// FocusReset records that the focus was re-established by an inner
	// focus-setting container (a predicate, a simple-map step, a postfix path
	// step) rather than by the containing instruction. §19.8.9.1 makes
	// accumulator-after free-ranging exactly then: the value it asks for is no
	// longer the one for the node the instruction is positioned on.
	FocusReset bool
	// AccPhase records that the construct sits inside an xsl:accumulator-rule:
	// §19.8.9.1 makes accumulator-after free-ranging in a phase="start" rule
	// and motionless in a phase="end" one.
	AccPhase AccRulePhase
	// AccAfterConsumed records that a PRECEDING instruction of the same
	// sequence constructor already consumed the stream, which §19.8.9.1's last
	// clauses make accumulator-after motionless: the node's subtree has been
	// read, so its post-descent accumulator values are already known.
	AccAfterConsumed bool
	// GroupPosture is the posture of the containing xsl:for-each-group's
	// select expression, which §19.8.9.4 hands to a current-group() call.
	// HasGroup distinguishes "the analysis knows the population" from "it does
	// not", where the classification falls back to the widest safe answer.
	GroupPosture Posture
	HasGroup     bool
	// Funcs resolves calls to stylesheet functions (§19.8.5). A nil resolver —
	// or one that does not know the name — leaves the call unclassifiable, and
	// strmFuncCall then fails closed exactly as it does for an extension
	// function.
	Funcs StreamFuncs
	// Definite, when non-nil, is a channel back to the caller answering ONE
	// question about a walk that came out non-streamable: was that verdict
	// REACHED BY A RULE, or merely not disproved?
	//
	// The distinction has no bearing on the classification itself — a
	// fail-closed analysis is free to answer "not streamable" either way, and
	// every posture/sweep this file computes is identical whether or not the
	// field is set. It matters only to a caller deciding whether to REPORT the
	// verdict as XTSE3430, because those are two different claims: "the
	// Recommendation says this construct is roaming and free-ranging" is a
	// statement about the stylesheet, while "this analysis is incomplete here"
	// is a statement about this engine, and only the first justifies rejecting
	// a stylesheet the user asked to stream. See the XSLT layer's
	// strmCheckStreamableSourceDocs, the only caller that supplies it.
	//
	// Set it ONLY from a rule whose conclusion the REC states unconditionally
	// and which this file implements in full. Writers go through
	// MarkDefinite; a nil field (every other caller) makes them no-ops.
	Definite *bool
}

// MarkDefinite records that a rule stated unconditionally by the
// Recommendation — not a fail-closed default — produced a non-streamable
// verdict. See StreamContext.Definite for why the two are kept apart.
//
// It is deliberately one-way: several rules can fire in one walk, and a later
// recovery (§19.8.8.7's scanning-expression reassessment, say) does not unset
// it. That is safe because the caller only consults the flag once the walk has
// ALREADY come out non-streamable overall — the flag says "a definite rule
// fired", never "the body is non-streamable", and the two are ANDed.
func (e *StreamContext) MarkDefinite() {
	if e != nil && e.Definite != nil {
		*e.Definite = true
	}
}

// InHigherOrder returns the context marked as being inside a HIGHER-ORDER
// operand — one the containing construct may evaluate more than once. It is
// what §19.8.8.11's "singular" test turns on, so the XSLT layer must apply it
// wherever it classifies a body the same way.
func (sc StreamContext) InHigherOrder() StreamContext {
	sc.inHigherOrder = true
	return sc
}

// Streamability classifies the expression's posture and sweep. A nil *Parsed
// is treated as an absent operand: grounded and motionless.
func (p *Parsed) Streamability(sc StreamContext) (Posture, Sweep) {
	if p == nil || p.root == nil {
		return PostureGrounded, SweepMotionless
	}
	return strmExpr(p.root, sc.ContextPosture, &sc)
}

// Streamability classifies a match pattern (§19.8.10). A pattern always yields
// a boolean, so it is either grounded+motionless or roaming+free-ranging.
func (p *Pattern) Streamability(sc StreamContext) (Posture, Sweep) {
	if p == nil {
		return PostureGrounded, SweepMotionless
	}
	if strmPatternMotionless(p, &sc) {
		return PostureGrounded, SweepMotionless
	}
	return PostureRoaming, SweepFreeRanging
}

// --- operand usage and the general streamability rules (§19.4, §19.8.1) ---

// StreamUsage is an operand role's usage (§19.4): how the containing construct
// uses a node supplied in that role.
type StreamUsage uint8

const (
	// UsageAbsorption: the construct reads the subtree rooted at the node
	// (atomization, string value, copying).
	UsageAbsorption StreamUsage = iota
	// UsageInspection: only properties available without reading the subtree
	// (name, existence, type).
	UsageInspection
	// UsageTransmission: the node may be passed through to the result, in
	// document order.
	UsageTransmission
	// UsageNavigation: the construct may navigate freely from the node, or the
	// analysis cannot tell what it does with it.
	UsageNavigation
)

// TypeDeterminedUsage gives the usage implied by a required type (§19.4):
// function(*) inspects, an atomic type absorbs, anything else navigates.
// isFunction and isAtomic describe the required type with its occurrence
// indicator ignored; an absent type ("item()*") is neither, hence navigation.
func TypeDeterminedUsage(isFunction, isAtomic bool) StreamUsage {
	switch {
	case isFunction:
		return UsageInspection
	case isAtomic:
		return UsageAbsorption
	default:
		return UsageNavigation
	}
}

// StreamGeneralRules applies the general streamability rules of §19.8.1 to a
// construct's already-classified operands. The XSLT instruction classifier
// shares this with the expression walk so both obey one implementation.
func StreamGeneralRules(ops []StreamOperand) (Posture, Sweep) {
	return strmGeneral(ops, false, nil)
}

// StreamGeneralRulesIn is StreamGeneralRules with the caller's context, so the
// §19.8.1 clauses that state a verdict UNCONDITIONALLY can report it through
// StreamContext.Definite. An instruction classifier with a context to hand
// should prefer it; StreamGeneralRules stays for the callers that have none
// (their verdicts then read as "this analysis could not prove otherwise").
func StreamGeneralRulesIn(ops []StreamOperand, sc *StreamContext) (Posture, Sweep) {
	return strmGeneral(ops, false, sc)
}

// StreamUsesFunction reports whether the expression calls the unprefixed
// function named local anywhere within it. The streamability analysis uses it
// for the §19.8.4.35 rule that a grouping-function call may not cross a stream
// boundary.
func (p *Parsed) StreamUsesFunction(local string) bool {
	if p == nil || p.root == nil {
		return false
	}
	found := false
	strmWalk(p.root, func(x Expr) bool {
		switch v := x.(type) {
		case *FuncCall:
			if v.Prefix == "" && v.Local == local {
				found = true
			}
		case *NamedFuncRef:
			if v.Prefix == "" && v.Local == local {
				found = true
			}
		}
		return !found
	})
	return found
}

// StreamUsesDeclaredStreamable reports whether the expression calls a
// stylesheet function that f resolves to a declared-streamable one. It lets the
// XSLT layer scope the XTSE3430 diagnostic to the constructs whose rules this
// processor implements completely.
func (p *Parsed) StreamUsesDeclaredStreamable(f StreamFuncs) bool {
	if p == nil || p.root == nil || f == nil {
		return false
	}
	found := false
	strmWalk(p.root, func(x Expr) bool {
		switch v := x.(type) {
		case *FuncCall:
			if v.Prefix != "" {
				if sig, ok := f.StreamFunc(v.Prefix, v.Local, len(v.Args)); ok && sig.Streamable {
					found = true
				}
			}
		case *ArrowExpr:
			for _, st := range v.Steps {
				if st.Name == "" || st.Pre == "" {
					continue
				}
				if sig, ok := f.StreamFunc(st.Pre, st.Name, len(st.Args)+1); ok && sig.Streamable {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// StreamChildless reports whether every node the pattern can match is childless
// (an attribute, a text node, a comment, a PI). It gives the caller the static
// context item type §19.8.1 needs to downgrade absorption to inspection for a
// construct whose focus is a node matched by this pattern.
func (p *Pattern) StreamChildless() bool {
	if p == nil || (len(p.alts) == 0 && len(p.exprAlts) == 0) {
		return false
	}
	childless := func(pe *PathExpr) bool {
		if pe == nil || len(pe.Steps) == 0 {
			return false
		}
		last := pe.Steps[len(pe.Steps)-1]
		if last == nil || last.Postfix != nil {
			return false
		}
		return strmStepChildless(last.Axis, &last.Test)
	}
	for _, alt := range p.alts {
		if !childless(alt) {
			return false
		}
	}
	for _, alt := range p.exprAlts {
		pe, ok := alt.(*PathExpr)
		if !ok || !childless(pe) {
			return false
		}
	}
	return true
}

// StreamChildless reports whether the expression can be proven to yield only
// items with no children, which downgrades an absorption operand to inspection.
func (p *Parsed) StreamChildless() bool {
	if p == nil || p.root == nil {
		return true
	}
	return strmChildless(p.root, nil)
}

// StreamChildlessIn is StreamChildless with the context the expression will be
// evaluated in. It matters for the expressions that ARE the context item —
// "." and fn:current() — which are childless exactly when the item they stand
// for is, something StreamChildless alone has no way to know (the body of
// xsl:for-each select="@*" doing xsl:value-of select=".").
func (p *Parsed) StreamChildlessIn(sc StreamContext) bool {
	if p == nil || p.root == nil {
		return true
	}
	return strmChildless(p.root, &sc)
}

// StreamOperand is one operand of a construct, already classified.
type StreamOperand struct {
	P Posture
	S Sweep
	U StreamUsage
	// Childless records that the operand's static type cannot include nodes
	// with children, which downgrades absorption to inspection (§19.8.1). It
	// is what makes `x[@code='a']` a motionless predicate. The analysis only
	// ever sets it when it can PROVE childlessness; unknown stays false, the
	// conservative side.
	Childless bool
	// HigherOrder marks an operand evaluated more than once, or under a
	// function item: a single potentially-consuming higher-order operand makes
	// the construct roaming.
	HigherOrder bool
	// Choice groups operands of which at most one is evaluated (if/then/else,
	// xsl:choose branches, xsl:catch clauses). Zero means "not in a group";
	// operands sharing a positive value form one group.
	Choice int
	// RECUsage records that U is the usage the Recommendation itself states
	// for this operand role, rather than one this analysis fell back to.
	// The distinction only matters for UsageNavigation, which carries two
	// quite different meanings in this file: §19.8.9's proforma really does
	// say that fn:reverse, fn:innermost and fn:filter navigate from their
	// first argument, whereas TypeDeterminedUsage hands back navigation
	// merely because a required type was absent or unrecognised. Only the
	// former licenses an XTSE3430 diagnostic; see strmGeneral.
	RECUsage bool
}

// strmGeneral applies the general streamability rules of §19.8.1.
//
// maxCardOne selects the built-in-function clause: a single crawling
// transmission operand of fn:head/exactly-one/zero-or-one yields striding.
//
// env, when non-nil, receives StreamContext.Definite for the clauses below
// that the Recommendation states unconditionally. The distinction drawn
// throughout this analysis is between "the REC says no" and "this analysis
// could not prove yes", and it is drawn HERE by a single structural fact: the
// first loop returns early for any operand that is itself roaming or
// free-ranging, which is how an inconclusive sub-analysis propagates. So every
// clause reached AFTER that loop completes has a fully classified operand list
// in hand — each operand PROVED striding, crawling, climbing or grounded — and
// the verdict it reaches is a positive finding about the stylesheet. Only the
// clauses whose REC text is likewise unconditional are marked; see each site.
func strmGeneral(ops []StreamOperand, maxCardOne bool, env *StreamContext) (Posture, Sweep) {
	if len(ops) == 0 {
		return PostureGrounded, SweepMotionless
	}
	type adjusted struct {
		op StreamOperand
		s  Sweep
		pc bool // potentially consuming
	}
	adj := make([]adjusted, 0, len(ops))
	for _, o := range ops {
		if o.S == SweepFreeRanging || o.P == PostureRoaming {
			return PostureRoaming, SweepFreeRanging
		}
		var s Sweep
		if o.P == PostureGrounded {
			s = o.S
		} else {
			u := o.U
			if u == UsageAbsorption && o.Childless {
				u = UsageInspection
			}
			switch u {
			case UsageAbsorption:
				// Climbing+absorption is free-ranging: reading the subtree of
				// an ancestor means re-reading what the stream already passed.
				// That is the Climbing/Absorption cell of §19.8.1's "Computing
				// the Adjusted Sweep of an Expression" table, which has no
				// escape clause, followed by the same section's "If any
				// operand has an adjusted sweep of free-ranging, then roaming
				// and free-ranging". Both the posture and the usage were
				// established before this point, so the cell is being READ,
				// not guessed at.
				if o.P == PostureClimbing {
					env.MarkDefinite()
					return PostureRoaming, SweepFreeRanging
				}
				s = SweepConsuming
			case UsageInspection, UsageTransmission:
				s = o.S
			default: // UsageNavigation over a streamed node
				if o.RECUsage {
					// The Navigation column of §19.8.1's adjusted-sweep table
					// is free-ranging for every non-grounded posture, without
					// exception, and a free-ranging operand makes the whole
					// construct roaming and free-ranging. Reported as the
					// REC's own verdict ONLY when the navigation usage is the
					// one §19.8.9's proforma states — fn:reverse(N),
					// fn:innermost(N), fn:filter(N, I) and their kin, whose
					// own sections say why (fn:innermost "needs to look ahead
					// in the input stream"; fn:reverse "is not streamable
					// unless the operand is grounded"). Navigation reached
					// any other way is this analysis declining to guess, not
					// the Recommendation speaking.
					env.MarkDefinite()
				}
				return PostureRoaming, SweepFreeRanging
			}
		}
		adj = append(adj, adjusted{o, s, s == SweepConsuming || (o.U == UsageTransmission && o.P != PostureGrounded)})
	}

	var pc []adjusted
	for _, a := range adj {
		if a.pc {
			pc = append(pc, a)
		}
	}
	switch len(pc) {
	case 0:
		return PostureGrounded, SweepMotionless
	case 1:
		o := pc[0]
		switch {
		case o.op.HigherOrder:
			// §19.8.1: "If exactly one operand o is potentially consuming:
			// if o is a higher-order operand of C, then roaming and
			// free-ranging." Being higher-order is a structural fact about
			// the construct (a predicate, a for/let/satisfies return clause,
			// an xsl:for-each-group body — §19.6.4 fixes the list), and the
			// operand was PROVED consuming above, so neither premise is an
			// inference this analysis might merely have failed to make.
			env.MarkDefinite()
			return PostureRoaming, SweepFreeRanging
		case o.op.U == UsageAbsorption || o.op.U == UsageInspection:
			return PostureGrounded, SweepConsuming
		case maxCardOne && o.op.P == PostureCrawling:
			return PostureStriding, o.s
		default:
			return o.op.P, o.s
		}
	default:
		if g := pc[0].op.Choice; g != 0 {
			all := true
			for _, o := range pc {
				if o.op.Choice != g {
					all = false
					break
				}
			}
			if all {
				return strmCombineChoice(pc, func(a adjusted) (Posture, Sweep) { return a.op.P, a.s })
			}
		}
		// All motionless with one common posture: e.g. (@a, @b), which can
		// only happen when every one of them has usage transmission.
		p0 := pc[0].op.P
		for _, o := range pc {
			if o.s != SweepMotionless || o.op.P != p0 {
				// §19.8.1's "If more than one operand is potentially
				// consuming: ... Otherwise, roaming and free-ranging" — the
				// last of that clause's three bullets, reached only after the
				// choice-operand-group and the all-motionless-one-posture
				// bullets above have each been tried and declined. The REC's
				// own Note spells the intent out: "if more than one operand
				// reads the subtree of the context node in a way that would
				// cause the current position of the input stream to change,
				// then the construct is not streamable". Two operands were
				// PROVED to do exactly that; this is the REC's answer, not a
				// gap in the analysis.
				env.MarkDefinite()
				return PostureRoaming, SweepFreeRanging
			}
		}
		return p0, SweepMotionless
	}
}

// strmCombineChoice implements the choice-operand-group clause: the posture is
// the combined posture of the group and the sweep its widest. Grounded members
// contribute no streamed nodes, so they do not conflict with a streamed
// posture; two DIFFERENT streamed postures do (§19.8.1 note: "if (X) then @name
// else name is guaranteed streamable").
func strmCombineChoice[T any](items []T, get func(T) (Posture, Sweep)) (Posture, Sweep) {
	post, seen := PostureGrounded, false
	sweep := SweepMotionless
	for _, it := range items {
		p, s := get(it)
		if s > sweep {
			sweep = s
		}
		if p == PostureGrounded {
			continue
		}
		if !seen {
			post, seen = p, true
		} else if post != p {
			return PostureRoaming, SweepFreeRanging
		}
	}
	return post, sweep
}

// strmFocusOn returns env with the context item moved onto the items base
// yields — the focus a predicate or a following map/path step actually sees.
// Only the properties of the ITEM move: the context item can no longer be
// assumed to be a document node, and its childlessness is base's, not the
// enclosing focus's. CurrentChildless deliberately does not move, since
// fn:current() still denotes the node the containing pattern matched.
func strmFocusOn(base Expr, env *StreamContext) *StreamContext {
	e := StreamContext{}
	if env != nil {
		e = *env
	}
	e.ContextChildless = strmChildless(base, env)
	e.ContextIsDocument = false
	e.FocusReset = true
	return &e
}

// strmHigherOrder returns env marked as being inside a higher-order operand,
// which §19.8.8.11 uses to decide whether a streaming-parameter reference is
// singular.
func strmHigherOrder(env *StreamContext) *StreamContext {
	if env == nil || env.inHigherOrder {
		return env
	}
	e := *env
	e.inHigherOrder = true
	return &e
}

func widerSweep(a, b Sweep) Sweep {
	if a > b {
		return a
	}
	return b
}

// --- expression walk ---

func strmExpr(e Expr, cp Posture, env *StreamContext) (Posture, Sweep) {
	if e == nil {
		return PostureGrounded, SweepMotionless
	}
	one := func(x Expr, u StreamUsage) []StreamOperand {
		p, s := strmExpr(x, cp, env)
		return []StreamOperand{{P: p, S: s, U: u, Childless: strmChildless(x, env)}}
	}
	pair := func(l, r Expr, u StreamUsage) []StreamOperand {
		lp, ls := strmExpr(l, cp, env)
		rp, rs := strmExpr(r, cp, env)
		return []StreamOperand{
			{P: lp, S: ls, U: u, Childless: strmChildless(l, env)},
			{P: rp, S: rs, U: u, Childless: strmChildless(r, env)},
		}
	}

	switch v := e.(type) {
	case *LiteralExpr:
		return PostureGrounded, SweepMotionless

	case *ContextItemExpr:
		// §19.8.8.12: the posture is the context posture; the sweep is always
		// motionless — the containing construct's operand usage decides
		// whether reading "." actually consumes.
		return cp, SweepMotionless

	case *VarRef:
		// §19.8.8.12: a variable reference is grounded and motionless unless it
		// is bound to a streaming parameter, in which case §19.8.5's per-category
		// "Rules for references to the streaming parameter" decide, and the
		// caller has loaded them into StreamingParamPosture/Sweep.
		if env.StreamingParams != nil && env.StreamingParams[v.Prefix+":"+v.Local] {
			if env.inHigherOrder && !env.StreamingParamRepeatable {
				// A non-singular reference: the construct between here and the
				// function body is evaluated repeatedly, so the parameter's
				// subtree would have to be read more than once.
				return PostureRoaming, SweepFreeRanging
			}
			if env.StreamingParamPosture == PostureGrounded {
				// No category gives a streaming-parameter reference a grounded
				// posture, so the zero value means the caller registered the
				// parameter without saying what its references are. Fail closed
				// rather than silently calling a streamed parameter grounded.
				return PostureRoaming, SweepFreeRanging
			}
			return env.StreamingParamPosture, env.StreamingParamSweep
		}
		return PostureGrounded, SweepMotionless

	case *SequenceExpr: // Expr [6]: T, T
		ops := make([]StreamOperand, 0, len(v.Items))
		for _, it := range v.Items {
			p, s := strmExpr(it, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageTransmission, Childless: strmChildless(it, env)})
		}
		return strmGeneral(ops, false, env)

	case *RangeExpr: // A to A
		return strmGeneral(pair(v.From, v.To, UsageAbsorption), false, env)

	case *BinaryExpr:
		switch v.Op {
		case "and", "or":
			return strmGeneral(pair(v.L, v.R, UsageInspection), false, env)
		case "+", "-", "*", "div", "idiv", "mod":
			return strmGeneral(pair(v.L, v.R, UsageAbsorption), false, env)
		case "=", "!=", "<", "<=", ">", ">=":
			return strmGeneral(pair(v.L, v.R, UsageAbsorption), false, env)
		default:
			return PostureRoaming, SweepFreeRanging
		}

	case *UnaryExpr: // +A / -A
		return strmGeneral(one(v.X, UsageAbsorption), false, env)

	case *CompareExpr:
		if v.Kind == compNode { // is / << / >> : I is I
			return strmGeneral(pair(v.L, v.R, UsageInspection), false, env)
		}
		return strmGeneral(pair(v.L, v.R, UsageAbsorption), false, env)

	case *StringConcatExpr: // A || A
		ops := make([]StreamOperand, 0, len(v.Parts))
		for _, x := range v.Parts {
			p, s := strmExpr(x, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageAbsorption, Childless: strmChildless(x, env)})
		}
		return strmGeneral(ops, false, env)

	case *UnionExpr:
		return strmUnionLike(v.L, v.R, cp, env, false)
	case *IntersectExceptExpr:
		// intersect and except can only REMOVE nodes, so the result is a
		// subset of the left operand and keeps its posture — no two streamed
		// subtrees can end up nested in the result that were not nested in the
		// left operand already. The draft's table does not draw that
		// distinction, and its own note says an implementation may narrow the
		// combined posture where it can prove it.
		return strmUnionLike(v.L, v.R, cp, env, true)

	case *InstanceOfExpr:
		// §19.8.8.5: inspection, except document-node(element(...)) which
		// cannot be decided without consuming the document node.
		u := UsageInspection
		if strmIsDocElementTest(v.Type) {
			u = UsageAbsorption
		}
		return strmGeneral(one(v.X, u), false, env)

	case *TreatExpr: // T treat as TYPE
		if strmIsDocElementTest(v.Type) {
			// §19.8.8.6: "If the ItemType of ST is a DocumentTest, optionally
			// parenthesized, that contains an ElementTest or
			// SchemaElementTest then roaming and free-ranging." Unlike the
			// matching "instance of" rule one section earlier, which merely
			// raises that operand's usage to absorption, "treat as" is given
			// the verdict outright — and the premise is the written type, so
			// nothing here is inferred. §19.8.8.5's Note explains both: such a
			// test "matches a document node only if it has exactly one element
			// node child, and this cannot be determined without consuming the
			// document".
			//
			// A bare document-node() with no inner test — which is what the
			// leading-"/" rewrite of §19.8.8.8 introduces — has no ElementTest
			// inside it and is deliberately not caught here.
			env.MarkDefinite()
			return PostureRoaming, SweepFreeRanging
		}
		return strmGeneral(one(v.X, UsageTransmission), false, env)

	case *CastExpr: // A cast/castable as TYPE
		return strmGeneral(one(v.X, UsageAbsorption), false, env)

	case *IfExpr:
		// §19.8.8.3: cond inspection; then/else a choice operand group with
		// usage transmission.
		cpst, cs := strmExpr(v.Cond, cp, env)
		tp, ts := strmExpr(v.Then, cp, env)
		ep, es := strmExpr(v.Else, cp, env)
		return strmGeneral([]StreamOperand{
			{P: cpst, S: cs, U: UsageInspection, Childless: strmChildless(v.Cond, env)},
			{P: tp, S: ts, U: UsageTransmission, Childless: strmChildless(v.Then, env), Choice: 1},
			{P: ep, S: es, U: UsageTransmission, Childless: strmChildless(v.Else, env), Choice: 1},
		}, false, env)

	case *ForExpr:
		// §19.8.8.1: every "in" sequence must be grounded, else roaming; the
		// return clause is a higher-order transmission operand.
		ops := make([]StreamOperand, 0, len(v.Binds)+1)
		for _, b := range v.Binds {
			p, s := strmExpr(b.Seq, cp, env)
			if p != PostureGrounded {
				return PostureRoaming, SweepFreeRanging
			}
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageNavigation})
		}
		bp, bs := strmExpr(v.Body, cp, strmHigherOrder(env))
		ops = append(ops, StreamOperand{P: bp, S: bs, U: UsageTransmission, HigherOrder: true})
		return strmGeneral(ops, false, env)

	case *LetExpr:
		// §19.8.8: let $var := N return T — navigation on the binding keeps a
		// streamed node out of the variable.
		ops := make([]StreamOperand, 0, len(v.Binds)+1)
		for _, b := range v.Binds {
			p, s := strmExpr(b.Seq, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageNavigation})
		}
		bp, bs := strmExpr(v.Body, cp, env)
		ops = append(ops, StreamOperand{P: bp, S: bs, U: UsageTransmission, Childless: strmChildless(v.Body, env)})
		return strmGeneral(ops, false, env)

	case *QuantExpr:
		// §19.8.8.2: "in" navigation, "satisfies" a higher-order inspection
		// operand.
		ops := make([]StreamOperand, 0, len(v.Binds)+1)
		for _, b := range v.Binds {
			p, s := strmExpr(b.Seq, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageNavigation})
		}
		sp, ss := strmExpr(v.Satisfies, cp, strmHigherOrder(env))
		ops = append(ops, StreamOperand{P: sp, S: ss, U: UsageInspection, HigherOrder: true})
		return strmGeneral(ops, false, env)

	case *PathExpr:
		return strmPath(v, cp, env)

	case *FilterExpr:
		bp, bs := strmExpr(v.Primary, cp, env)
		return strmApplyPredicates(bp, bs, v.Preds, strmFocusOn(v.Primary, env))

	case *SimpleMapExpr:
		// §19.8.8.6: the result takes the right operand's posture, assessed
		// with the left operand's posture as context. The spec gives the sweep
		// as the right operand's alone; this takes the WIDER of the two, since
		// the left operand really is evaluated (and a wider sweep can only
		// reject, never wrongly accept).
		p, s := PostureGrounded, SweepMotionless
		if len(v.Steps) == 0 {
			return p, s
		}
		p, s = strmExpr(v.Steps[0], cp, env)
		// Every step after the first runs once per item the one before it
		// delivered, which is what makes it a higher-order operand. That item —
		// not the outer focus — is also the context ITEM, so its childlessness
		// travels with the posture; carrying the outer one over would judge "."
		// against the wrong node in both directions.
		prev, penv := v.Steps[0], env
		henv := strmHigherOrder(env)
		for _, st := range v.Steps[1:] {
			senv := strmFocusOn(prev, penv)
			senv.inHigherOrder = henv.inHigherOrder
			rp, rs := strmExpr(st, p, senv)
			p, s = rp, widerSweep(s, rs)
			prev, penv = st, senv
		}
		return p, s

	case *ArrowExpr:
		// base => f(a, b) is f(base, a, b).
		cur := v.Base
		for _, st := range v.Steps {
			if st.Name == "" {
				// A dynamic function specifier ($f / parenthesized): no
				// signature is available, so every argument is navigation.
				ops := []StreamOperand{}
				bp, bs := strmExpr(st.Spec, cp, env)
				ops = append(ops, StreamOperand{P: bp, S: bs, U: UsageInspection})
				for _, a := range append([]Expr{cur}, st.Args...) {
					p, s := strmExpr(a, cp, env)
					ops = append(ops, StreamOperand{P: p, S: s, U: UsageNavigation})
				}
				return strmGeneral(ops, false, env)
			}
			cur = &FuncCall{Prefix: st.Pre, Local: st.Name, Args: append([]Expr{cur}, st.Args...)}
		}
		return strmExpr(cur, cp, env)

	case *FuncCall:
		return strmFuncCall(v, cp, env)

	case *DynCall:
		// §19.8.8.11: the base is inspection; each argument takes its usage
		// from the base's static signature, and where none is available the
		// section's fallback makes it navigation.
		//
		// The one signature this analysis can always read is the map/array
		// one, which §19.8.8.11's Note singles out: a lookup $map($key) or
		// $array($index) has argument type xs:anyAtomicType, "and ... the
		// operand usage is therefore absorption. A call that passes a streamed
		// node will therefore be grounded and consuming." That is the
		// difference between accepting and rejecting $value(@type) inside a
		// streamable accumulator rule — absorbing an ATTRIBUTE, which has no
		// children, downgrades to inspection and stays motionless, whereas
		// navigation of a striding operand is roaming.
		argU := UsageNavigation
		if strmBaseIsMapOrArray(v.Base, env) {
			argU = UsageAbsorption
		}
		ops := []StreamOperand{}
		bp, bs := strmExpr(v.Base, cp, env)
		ops = append(ops, StreamOperand{P: bp, S: bs, U: UsageInspection})
		for _, a := range v.Args {
			if _, ok := a.(*Placeholder); ok {
				continue
			}
			p, s := strmExpr(a, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: argU, Childless: argU == UsageAbsorption && strmChildless(a, env)})
		}
		return strmGeneral(ops, false, env)

	case *NamedFuncRef:
		// §19.8.8.14.
		if strmFocusDependent(v.Prefix, v.Local, v.Arity) && cp != PostureGrounded {
			return PostureRoaming, SweepFreeRanging
		}
		return PostureGrounded, SweepMotionless

	case *InlineFunc:
		// §19.8.8.15: grounded and motionless unless it closes over a
		// streaming parameter.
		if strmMentionsStreamingParam(v.Body, env) {
			return PostureRoaming, SweepFreeRanging
		}
		return PostureGrounded, SweepMotionless

	case *MapExpr:
		// §19.8.8.17 defers a map constructor to "the posture and sweep of the
		// equivalent xsl:map instruction", one xsl:map-entry per key/value
		// pair; §19.8.4.24 then gives the key absorption and the select
		// navigation, with a Note saying what that is for: "the select
		// expression must not return nodes from a streamed input document".
		// The map is grounded with the widest entry sweep.
		sweep := SweepMotionless
		for i := range v.Keys {
			ops := []StreamOperand{}
			kp, ks := strmExpr(v.Keys[i], cp, env)
			ops = append(ops, StreamOperand{P: kp, S: ks, U: UsageAbsorption, Childless: strmChildless(v.Keys[i], env)})
			if i < len(v.Vals) {
				vp, vs := strmExpr(v.Vals[i], cp, env)
				// RECUsage: navigation here is §19.8.4.24's own word, not a
				// fallback, so a streamed value may be diagnosed rather than
				// merely declined. See StreamOperand.RECUsage.
				ops = append(ops, StreamOperand{P: vp, S: vs, U: UsageNavigation, RECUsage: true})
			}
			p, s := strmGeneral(ops, false, env)
			if !Streamable(p, s) {
				return PostureRoaming, SweepFreeRanging
			}
			sweep = widerSweep(sweep, s)
		}
		return PostureGrounded, sweep

	case *ArrayExpr:
		// The draft has no array entry. Arrays hold items the same way maps
		// do, so members get map:entry's value usage (navigation): an array
		// may not carry streamed nodes.
		ops := make([]StreamOperand, 0, len(v.Items))
		for _, it := range v.Items {
			p, s := strmExpr(it, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageNavigation})
		}
		p, s := strmGeneral(ops, false, env)
		if !Streamable(p, s) {
			return PostureRoaming, SweepFreeRanging
		}
		return PostureGrounded, s

	case *LookupExpr:
		// base?key over a map/array. The base is inspected, the key atomized.
		base := v.Base
		ops := []StreamOperand{}
		if base == nil {
			ops = append(ops, StreamOperand{P: cp, S: SweepMotionless, U: UsageInspection})
		} else {
			p, s := strmExpr(base, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageInspection, Childless: strmChildless(base, env)})
		}
		if v.Key != nil {
			p, s := strmExpr(v.Key, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: UsageAbsorption, Childless: strmChildless(v.Key, env)})
		}
		return strmGeneral(ops, false, env)

	case *Placeholder:
		return PostureGrounded, SweepMotionless

	default:
		// Fail closed: an Expr kind with no rule above is never assumed safe.
		return PostureRoaming, SweepFreeRanging
	}
}

// strmBaseIsMapOrArray reports whether the base of a dynamic function call is
// STATICALLY known to be a map or an array, which is the condition
// §19.8.8.11's Note attaches its absorption rule to. Only shapes that cannot be
// anything else answer true: a map or array constructor written out, and a
// variable whose declared type the XSLT layer resolved to a map or array type
// (StreamContext.MapArrayVars). Anything else — a general function item, an
// unannotated variable — falls back to the section's navigation default.
func strmBaseIsMapOrArray(base Expr, env *StreamContext) bool {
	switch b := base.(type) {
	case *MapExpr, *ArrayExpr:
		return true
	case *VarRef:
		return env != nil && env.MapArrayVars[b.Prefix+":"+b.Local]
	}
	return false
}

// strmUnionLike implements §19.8.8.4 for union / intersect / except. subset
// says the result can only be a subset of the left operand, which lets it keep
// the left operand's posture rather than widening to crawling.
func strmUnionLike(l, r Expr, cp Posture, env *StreamContext, subset bool) (Posture, Sweep) {
	lp, ls := strmExpr(l, cp, env)
	rp, rs := strmExpr(r, cp, env)
	switch {
	case ls == SweepFreeRanging || rs == SweepFreeRanging || lp == PostureRoaming || rp == PostureRoaming:
		return PostureRoaming, SweepFreeRanging
	case lp == PostureGrounded && ls == SweepMotionless:
		if subset {
			// Nothing streamed survives a subset of a grounded sequence.
			return lp, widerSweep(ls, rs)
		}
		return rp, rs
	case rp == PostureGrounded && rs == SweepMotionless:
		return lp, ls
	case subset:
		return lp, widerSweep(ls, rs)
	case lp == PostureClimbing && rp == PostureClimbing:
		return PostureClimbing, widerSweep(ls, rs)
	case (lp == PostureStriding || lp == PostureCrawling) && (rp == PostureStriding || rp == PostureCrawling):
		return PostureCrawling, widerSweep(ls, rs)
	default:
		return PostureRoaming, SweepFreeRanging
	}
}

// --- path expressions (§19.8.8.7) and axis steps (§19.8.8.8) ---

func strmPath(v *PathExpr, cp Posture, env *StreamContext) (Posture, Sweep) {
	p, s := cp, SweepMotionless
	// rootCollapsed records that a leading "/" resolved to the context item
	// rather than to an upward climb, which is what lets the remaining steps
	// still qualify as a scanning expression (//a inside xsl:source-document).
	rootCollapsed := !v.Absolute

	switch {
	case v.Start != nil:
		p, s = strmExpr(v.Start, cp, env)
	case v.Absolute:
		// §19.8.8.7 + §19.8.9.18: "/" becomes root(self::node()), which
		// collapses to the context item when that is statically a document
		// node in a striding posture, and otherwise to
		// head(ancestor-or-self::node()) — climbing, so any following downward
		// step is roaming. That is exactly the spec's note that //a streams
		// only when the context item type is document-node().
		if env.ContextIsDocument && (cp == PostureStriding || cp == PostureGrounded) {
			p, s = cp, SweepMotionless
			rootCollapsed = true
		} else if cp == PostureGrounded {
			p, s = PostureGrounded, SweepMotionless
			rootCollapsed = true
		} else {
			p, s = PostureClimbing, SweepMotionless
		}
	}

	// §19.8.8.7: "a/b/c" is a TREE of binary expressions, (a/b)/c. Each binary
	// level gets its own provisional posture and its own scanning-expression
	// reassessment, so the walk has to reassess after EVERY step rather than
	// once over the whole path — a prefix that recovers from roaming back to
	// crawling is what the following step is then assessed against. Applying it
	// only at the end loses section//head//p/ancestor-or-self::section, whose
	// last step is not scanning-shaped even though every prefix of it is.
	base := cp
	if v.Start != nil {
		base, _ = strmExpr(v.Start, cp, env)
	}
	for i, st := range v.Steps {
		if st == nil {
			return PostureRoaming, SweepFreeRanging
		}
		var sp Posture
		var ss Sweep
		if st.Postfix != nil {
			// A postfix step is evaluated with each item the PRECEDING step
			// delivered as its focus, not with the path's outer one.
			penv := env
			if i > 0 {
				penv = strmFocusOn(&PathExpr{Steps: v.Steps[:i]}, env)
			}
			sp, ss = strmExpr(st.Postfix, p, penv)
		} else {
			sp, ss = strmAxisStep(p, st, env)
		}
		p, s = sp, widerSweep(s, ss)
		// Scanning-expression reassessment (§19.8.8.8): a purely downward path
		// whose predicates are all motionless and non-positional can be
		// evaluated by testing each descendant as it streams past, so its
		// provisional roaming posture becomes crawling and its sweep is that
		// single pass — NOT the free-ranging the roaming transition produced.
		// This is what makes section//head streamable.
		//
		// It belongs to the RelativePathExpr rule, which is BINARY: the REC
		// phrases the whole two-phase assessment in terms of a left and a
		// right operand, and a/b/c is "taken as a tree of binary expressions
		// (that is, (a/b)/c)". A lone AxisStep has no left operand and is not
		// a RelativePathExpr at all — §19.8.8.9 governs it instead, and that
		// section's table ends "Any other combination: Roaming, Free-ranging"
		// with no reassessment clause anywhere in it. So a bare step must keep
		// the table's verdict: `*` evaluated with a climbing context posture
		// (inside xsl:for-each select=".."), or with a crawling one, is
		// roaming, and rescuing it here would approve re-reading input the
		// stream has already passed. Steps after the first DO have a left
		// operand — the path prefix — and are reassessed exactly as before.
		hasLeftOperand := i > 0 || v.Start != nil || v.Absolute
		if p == PostureRoaming && hasLeftOperand && rootCollapsed && base != PostureRoaming &&
			strmPrefixScanning(v, i, env) {
			if strmStepSelectsElementsAt(v.Steps[i]) {
				p = PostureCrawling
			} else {
				p = PostureStriding
			}
			s = SweepConsuming
		}
		if p == PostureRoaming && strmStartRootedPath(v.Start) {
			// The other half of the same question, answered from §19.8.10
			// rather than from §5.5.2's grammar: a scanning expression must be
			// equivalent to a MOTIONLESS pattern, and the first condition
			// §19.8.10 gives for motionless is "the pattern does not contain a
			// RootedPath". A variable reference or a function call in path-root
			// position IS a RootedPath (§5.5.2 [6] — its trailing path is
			// optional, so a bare $v qualifies), and the pattern grammar has no
			// other production that reaches either. So no pattern equivalent to
			// this expression can be motionless, whatever postures the walk
			// computed — the REC's own Note says exactly this of $node//section/head
			// in §19.8.8.8. That is a fact about how the expression is written,
			// so it is reportable rather than an admission of incompleteness.
			env.MarkDefinite()
		}
		if p == PostureRoaming && strmStartNeverPattern(v.Start) {
			// §19.8.8.8 ends the two-phase assessment with "Otherwise (if the
			// provisional posture is not roaming, or the expression is not a
			// scanning expression), the posture of the expression is the
			// provisional posture", and a scanning expression is DEFINED as
			// one "syntactically equivalent to some motionless pattern".
			// strmStartNeverPattern answers that syntactic half alone, and
			// only for the shapes §5.5.2's grammar cannot derive at any
			// depth — so declining here is a fact about how the expression is
			// written, not about how far this analysis got. The rest of
			// strmPrefixScanning's "no" is deliberately NOT reported: it is
			// stricter than the grammar (which admits a parenthesized union
			// and a doc/id/key/root call as a step), and a stricter test
			// failing proves nothing.
			env.MarkDefinite()
		}
	}
	return p, s
}

// strmStartNeverPattern reports whether a path's start expression is of a kind
// the XSLT 3.0 pattern grammar (§5.5.2) cannot derive ANYWHERE — so a path
// built on it is provably not "syntactically equivalent to some motionless
// pattern", whatever its postures turn out to be.
//
// The grammar's only ways into a step are AxisStepP, a parenthesized UnionExprP,
// and RootedPath's leading VarRef or doc/id/element-with-id/key/root call with
// literal or variable arguments. None of them admits a comma, a range, an
// operator, a conditional, a binding expression, a map or array constructor, a
// function item, an arrow, a simple map, or a type expression. Kinds NOT listed
// here (a union, a function call, a variable reference, a parenthesized path)
// might still be pattern-shaped, so they return false and stay unreported.
// strmStartRootedPath reports whether x, in the start (StepExprP) position of a
// path, provably CONTAINS a RootedPath — §5.5.2 [6],
//
//	RootedPath ::= (VarRef | FunctionCallP) PredicateList (("/" | "//") RelativePathExprP)?
//
// whose trailing path is optional, so a bare $v is one on its own. §19.8.10's
// first condition for a motionless pattern is that the pattern does not contain
// a RootedPath, and a scanning expression must be equivalent to a motionless
// pattern (§19.8.8.8), so such an expression is definitively not one.
//
// The recursion follows the grammar: a ParenthesizedExprP wraps a UnionExprP
// whose operands are IntersectExceptExprP / PathExprP, and "contains" reaches
// through all of them. Predicates are deliberately NOT followed — §19.8.10's
// own list of motionless patterns includes p[@status = $status-codes[1]], so a
// variable reference inside a predicate is not a RootedPath of the pattern.
func strmStartRootedPath(x Expr) bool {
	switch v := x.(type) {
	case *VarRef:
		return true
	case *FuncCall:
		// Either a FunctionCallP (doc/id/element-with-id/key/root), which is a
		// RootedPath, or a name the pattern grammar cannot derive at all.
		// Neither can appear in a motionless pattern.
		return true
	case *FilterExpr:
		return strmStartRootedPath(v.Primary)
	case *UnionExpr:
		return strmStartRootedPath(v.L) || strmStartRootedPath(v.R)
	case *IntersectExceptExpr:
		return strmStartRootedPath(v.L) || strmStartRootedPath(v.R)
	case *PathExpr:
		return strmStartRootedPath(v.Start)
	}
	return false
}

// strmStartUnionScanning reports whether x is a ParenthesizedExprP (§5.5.2 [14])
// whose every union operand is itself shaped like a motionless pattern, so a
// path built on it can still be a scanning expression. The pattern grammar
// admits a parenthesized UnionExprP as a StepExprP, so (a|b)/c is a pattern —
// which is why strmPrefixScanning cannot simply refuse every composite start.
func strmStartUnionScanning(x Expr, env *StreamContext) bool {
	switch v := x.(type) {
	case *UnionExpr:
		return strmStartUnionScanning(v.L, env) && strmStartUnionScanning(v.R, env)
	case *PathExpr:
		if v.Start != nil {
			// A RootedPath, or a shape the grammar cannot derive; either way
			// strmStartRootedPath/strmStartNeverPattern govern it, not this.
			return false
		}
		if v.Absolute && (env == nil || !env.ContextIsDocument) {
			// Same reason the whole-path test has: with a non-document context
			// item the leading "/" is root(self::node()), which §19.8.9.18
			// rewrites to a climb, and the rewritten expression is no longer
			// syntactically a pattern.
			return false
		}
		for _, st := range v.Steps {
			if !strmStepScanning(st, env) {
				return false
			}
		}
		return true
	}
	return false
}

func strmStartNeverPattern(x Expr) bool {
	switch x.(type) {
	case *SequenceExpr, *RangeExpr, *BinaryExpr, *UnaryExpr, *CompareExpr,
		*StringConcatExpr, *IfExpr, *ForExpr, *LetExpr, *QuantExpr,
		*MapExpr, *ArrayExpr, *InlineFunc, *ArrowExpr, *SimpleMapExpr,
		*CastExpr, *InstanceOfExpr, *TreatExpr:
		return true
	}
	return false
}

// strmPrefixScanning reports whether the path truncated after step upto is a
// scanning expression (§19.8.8.7). The start expression is admissible when it
// merely names the nodes the downward scan begins at and needs no navigation of
// its own: "." and a variable reference (which in a streamable construct can
// only be a streaming parameter or a grounded value). $node//section scans from
// each of $node's nodes exactly as .//section scans from the context item.
func strmPrefixScanning(v *PathExpr, upto int, env *StreamContext) bool {
	if v.Absolute && !env.ContextIsDocument {
		return false
	}
	if v.Start != nil {
		switch v.Start.(type) {
		case *ContextItemExpr, *VarRef:
		default:
			// §5.5.2 [12]-[14]: the other way into a StepExprP is a
			// ParenthesizedExprP wrapping a UnionExprP, so (a|b)/c IS
			// pattern-shaped and can be a scanning expression when each
			// operand is. Anything else is not derivable here.
			if !strmStartUnionScanning(v.Start, env) {
				return false
			}
		}
	}
	for _, st := range v.Steps[:upto+1] {
		if !strmStepScanning(st, env) {
			return false
		}
	}
	return true
}

// strmStepScanning is §19.8.8.7's AxisStep clause: a downward axis whose every
// predicate is motionless and non-positional.
func strmStepScanning(st *Step, env *StreamContext) bool {
	if st == nil || st.Postfix != nil {
		return false
	}
	axis := st.Axis
	if axis == "" {
		axis = "child"
	}
	switch axis {
	case "child", "descendant", "descendant-or-self", "self":
	default:
		return false
	}
	for _, pr := range st.Preds {
		if !strmPredScanning(pr, env) {
			return false
		}
	}
	return true
}

// strmStepSelectsElementsAt reports whether a step's static type can contain
// U{element}, which §19.8.8.7 uses to pick crawling over striding.
func strmStepSelectsElementsAt(st *Step) bool {
	if st == nil || st.Postfix != nil {
		return true
	}
	axis := st.Axis
	if axis == "" {
		axis = "child"
	}
	return strmStepSelectsElements(axis, &st.Test)
}

// strmAxisStep implements the §19.8.8.8 table plus its preceding special
// clauses.
func strmAxisStep(cp Posture, st *Step, env *StreamContext) (Posture, Sweep) {
	switch cp {
	case PostureGrounded:
		return PostureGrounded, SweepMotionless
	case PostureRoaming:
		return PostureRoaming, SweepFreeRanging
	}

	axis := st.Axis
	if axis == "" {
		axis = "child"
	}
	selectsElements := strmStepSelectsElements(axis, &st.Test)

	// A numeric, focus-independent predicate on the descendant axis picks at
	// most one node, so the result strides instead of crawling.
	if cp == PostureStriding && (axis == "descendant" || axis == "descendant-or-self") {
		for _, pr := range st.Preds {
			if strmNumericFocusFree(pr) {
				return PostureStriding, SweepConsuming
			}
		}
	}

	var p Posture
	var s Sweep
	switch cp {
	case PostureClimbing:
		switch axis {
		case "self", "parent", "ancestor-or-self", "ancestor":
			p, s = PostureClimbing, SweepMotionless
		case "attribute", "namespace":
			p, s = PostureStriding, SweepMotionless
		default:
			return PostureRoaming, SweepFreeRanging
		}
	case PostureStriding:
		switch axis {
		case "parent", "ancestor-or-self", "ancestor":
			p, s = PostureClimbing, SweepMotionless
		case "self", "attribute", "namespace":
			p, s = PostureStriding, SweepMotionless
		case "child":
			p, s = PostureStriding, SweepConsuming
		case "descendant", "descendant-or-self":
			if selectsElements {
				p, s = PostureCrawling, SweepConsuming
			} else {
				p, s = PostureStriding, SweepConsuming
			}
		default:
			return PostureRoaming, SweepFreeRanging
		}
	case PostureCrawling:
		switch axis {
		case "parent", "ancestor-or-self", "ancestor":
			p, s = PostureClimbing, SweepMotionless
		case "attribute", "namespace":
			p, s = PostureStriding, SweepMotionless
		case "self":
			if selectsElements {
				p, s = PostureCrawling, SweepMotionless
			} else {
				p, s = PostureStriding, SweepMotionless
			}
		default:
			return PostureRoaming, SweepFreeRanging
		}
	default:
		return PostureRoaming, SweepFreeRanging
	}

	// Predicates on an axis step are assessed with the step's own posture AND
	// node kind as context; any that is not motionless makes the step roam.
	// (This is the §19.8.8.8 rule, NOT the filter-expression rule of
	// §19.8.8.9.)
	if len(st.Preds) > 0 {
		psc := *env
		psc.ContextPosture = p
		psc.ContextIsDocument = false
		psc.ContextChildless = strmStepChildless(axis, &st.Test)
		for _, pr := range st.Preds {
			pp, ps := strmExpr(pr, p, &psc)
			if ps != SweepMotionless || pp == PostureRoaming {
				// "If the PredicateList contains a Predicate that is not
				// motionless, then the sweep is free-ranging and the posture
				// is roaming" — stated flatly, with no escape clause, so this
				// verdict is the REC's and not this analysis giving up.
				env.MarkDefinite()
				return PostureRoaming, SweepFreeRanging
			}
		}
	}
	return p, s
}

// strmApplyPredicates implements §19.8.8.9 for a genuine filter expression.
func strmApplyPredicates(bp Posture, bs Sweep, preds []Expr, env *StreamContext) (Posture, Sweep) {
	for _, pr := range preds {
		if bp == PostureCrawling && strmNumericFocusFree(pr) {
			bp = PostureStriding
			continue
		}
		pp, ps := strmExpr(pr, bp, env)
		if ps != SweepMotionless || pp == PostureRoaming {
			// §19.8.8.10's last bullet: a filter expression whose predicate is
			// not motionless is "otherwise, roaming and free-ranging" — again
			// a stated conclusion, reached only after the two recovering
			// bullets above have each been tried and declined.
			env.MarkDefinite()
			return PostureRoaming, SweepFreeRanging
		}
		// Posture and sweep of the base are unchanged by a motionless
		// predicate.
	}
	return bp, bs
}

// --- function calls (§19.8.9) ---

func strmFuncCall(v *FuncCall, cp Posture, env *StreamContext) (Posture, Sweep) {
	name := strmFuncName(v.Prefix, v.Local)
	args := v.Args

	// §19.8.5: a call to a stylesheet function is classified from the category
	// the function declares, not from the built-in catalogue below. A
	// stylesheet function is always namespace-qualified and may not be in a
	// reserved namespace, so this lookup can never shadow a built-in.
	if env.Funcs != nil && v.Prefix != "" {
		if sig, ok := env.Funcs.StreamFunc(v.Prefix, v.Local, len(args)); ok {
			return strmStylesheetCall(sig, args, cp, env)
		}
	}

	partial := false
	for _, a := range args {
		if _, ok := a.(*Placeholder); ok {
			partial = true
		}
	}
	if partial {
		// §19.8.8.13: a partial application of a focus-dependent function is
		// roaming whenever the focus is streamed.
		if strmFocusDependent(v.Prefix, v.Local, len(args)) && cp != PostureGrounded {
			return PostureRoaming, SweepFreeRanging
		}
	}

	// Zero/short-arity forms that default an argument to the context item (or
	// to the root) are analysed as the expanded call.
	if d, ok := strmContextDefaults[name]; ok && len(args) == d.arity {
		if d.root {
			args = append(append([]Expr{}, args...), &PathExpr{Absolute: true})
		} else {
			args = append(append([]Expr{}, args...), &ContextItemExpr{})
		}
	}

	switch name {
	case "fn:position", "fn:current-grouping-key", "fn:current-merge-group", "fn:current-merge-key":
		return PostureGrounded, SweepMotionless

	case "fn:last":
		// §19.8.9.14: usable only where the focus is grounded or climbing.
		if cp == PostureStriding || cp == PostureCrawling || cp == PostureRoaming {
			// "If the context posture for a call on the last function is
			// striding, crawling, or roaming, then the posture of the
			// function is roaming, and the sweep is free-ranging." The rule
			// reads only the context posture, so unlike most of this file it
			// cannot be reached by failing to prove something.
			env.MarkDefinite()
			return PostureRoaming, SweepFreeRanging
		}
		return PostureGrounded, SweepMotionless

	case "fn:current":
		// §19.8.9.3: motionless, with the posture the context item would have
		// at the top of the containing XPath expression — which is the focus
		// the INSTRUCTION established, not the one this walk has reached.
		// Striding is the right answer wherever a template rule or a streamed
		// source document set that focus, and stays the fallback; an
		// instruction that rebinds the focus to something else says so.
		if env != nil && env.HasCurrentPosture {
			return env.CurrentPosture, SweepMotionless
		}
		return PostureStriding, SweepMotionless

	case "fn:current-group":
		// §19.8.9.4: the call takes the posture and sweep of the containing
		// xsl:for-each-group's select expression, which the XSLT layer supplies.
		// A GROUNDED population is already materialised, so reading the group
		// cannot move the stream however often the body does it — that is what
		// makes a group body with several current-group() references streamable
		// at all. Without a known population the safe answer stays the group's
		// own worst case: striding and consuming.
		if env.HasGroup {
			if env.GroupPosture == PostureGrounded {
				return PostureGrounded, SweepMotionless
			}
			return env.GroupPosture, SweepConsuming
		}
		// §19.8.9.4's three conditions all begin with "C has a containing
		// xsl:for-each-group instruction"; with none, the section ends
		// "Otherwise, roaming and free-ranging" — stated flatly, and the
		// premise is a lexical fact about where the call is written, so the
		// verdict is the REC's. A template rule reached by apply-templates
		// from inside a group is NOT a containing instruction, which is what
		// si-fork-116 turns on.
		env.MarkDefinite()
		return PostureRoaming, SweepFreeRanging

	case "fn:accumulator-before", "fn:accumulator-after":
		// §19.8.9.1/.2: always grounded, and free-ranging if the accumulator
		// name is not itself motionless.
		if len(args) == 1 {
			if _, s := strmExpr(args[0], cp, env); s != SweepMotionless {
				return PostureGrounded, SweepFreeRanging
			}
		}
		if name == "fn:accumulator-before" {
			// §19.8.9.2 has only those two clauses: the pre-descent value of
			// an accumulator for a node is known the moment the stream reaches
			// that node, so the call never moves the stream.
			return PostureGrounded, SweepMotionless
		}
		// §19.8.9.1 (accumulator-after), in the spec's own order.
		if cp == PostureGrounded || env.ContextChildless {
			return PostureGrounded, SweepMotionless
		}
		if env.FocusReset {
			// §19.8.9.1: the call's focus-setting container is not the
			// innermost containing instruction, so the stream would have to be
			// re-read to answer for that other node.
			return PostureGrounded, SweepFreeRanging
		}
		switch env.AccPhase {
		case AccPhaseStart:
			// A pre-descent rule may not read a post-descent value.
			return PostureGrounded, SweepFreeRanging
		case AccPhaseEnd:
			return PostureGrounded, SweepMotionless
		}
		if env.AccAfterConsumed {
			// A preceding instruction of the same sequence constructor already
			// read the subtree, so this value is known without moving further.
			return PostureGrounded, SweepMotionless
		}
		// The remaining clauses turn on finer detail of where the call sits
		// within its instruction, so the answer here is the widest outcome
		// they allow short of rejecting: consuming.
		return PostureGrounded, SweepConsuming

	case "fn:root":
		// §19.8.9.18: root(X) collapses to X when X is statically a document
		// node in a striding posture; otherwise it is
		// head(X/ancestor-or-self::node()).
		var ap Posture
		var as Sweep
		if len(args) == 1 {
			ap, as = strmExpr(args[0], cp, env)
		} else {
			ap, as = cp, SweepMotionless
		}
		if ap == PostureGrounded {
			return PostureGrounded, as
		}
		if env.ContextIsDocument && ap == PostureStriding && len(args) == 0 {
			return ap, as
		}
		return PostureClimbing, as

	case "fn:outermost":
		// §19.8.9.15: transmission, with crawling lifted to striding.
		if len(args) != 1 {
			return PostureRoaming, SweepFreeRanging
		}
		p, s := strmExpr(args[0], cp, env)
		rp, rs := strmGeneral([]StreamOperand{{P: p, S: s, U: UsageTransmission}}, false, env)
		if p == PostureCrawling && Streamable(rp, rs) {
			return PostureStriding, rs
		}
		return rp, rs

	case "fn:head", "fn:exactly-one", "fn:zero-or-one":
		// The three built-ins with a transmission argument and a return type
		// of maximum cardinality one (§19.8.1's crawling clause).
		if len(args) != 1 {
			return PostureRoaming, SweepFreeRanging
		}
		p, s := strmExpr(args[0], cp, env)
		return strmGeneral([]StreamOperand{{P: p, S: s, U: UsageTransmission, Childless: strmChildless(args[0], env)}}, true, env)

	case "fn:normalize-space":
		// The draft's table lists the 0-arity form with no operands, which
		// would make it motionless even though it reads the context item's
		// string value. Treated as absorption of "." — the honest reading, and
		// the conservative one.
		if len(args) == 0 {
			args = []Expr{&ContextItemExpr{}}
		}
	}

	usages, known := strmFuncUsages(name, len(args))
	if !known {
		// Fail closed: an unknown function (a stylesheet function, an
		// extension function, or one outside the draft's catalogue) may do
		// anything with a streamed node.
		return PostureRoaming, SweepFreeRanging
	}
	ops := make([]StreamOperand, 0, len(args))
	for i, a := range args {
		if _, ok := a.(*Placeholder); ok {
			continue
		}
		p, s := strmExpr(a, cp, env)
		// The usage came from §19.8.9's own per-function proforma, so it is
		// the Recommendation's and not this analysis's fallback: see
		// StreamOperand.RECUsage.
		ops = append(ops, StreamOperand{P: p, S: s, U: usages[i], RECUsage: true, Childless: strmChildless(a, env)})
	}
	return strmGeneral(ops, false, env)
}

// --- stylesheet function calls (§19.8.5) ---

// StreamFuncCategory is a stylesheet function's streamability category
// (§19.8.5): the promise its declaration makes about what it does with the
// nodes supplied in its first argument.
type StreamFuncCategory uint8

const (
	// StreamFnUnclassified is the default. The function promises nothing, so a
	// streamed node reaches it only where the parameter's own declared type
	// atomizes it.
	StreamFnUnclassified StreamFuncCategory = iota
	// StreamFnAbsorbing reads the subtrees of the first argument's nodes.
	StreamFnAbsorbing
	// StreamFnInspection reads only properties available without advancing the
	// stream.
	StreamFnInspection
	// StreamFnFilter returns a motionless-selected subset of the first
	// argument.
	StreamFnFilter
	// StreamFnShallowDescent returns nodes below the first argument's, none an
	// ancestor of another.
	StreamFnShallowDescent
	// StreamFnDeepDescent returns descendants, which may nest.
	StreamFnDeepDescent
	// StreamFnAscent returns ancestors.
	StreamFnAscent
)

// StreamFuncSig is what §19.8.5 needs to know about a stylesheet function at a
// CALL site: its category, and the usages its signature gives each argument.
// The constraints each category places on the function BODY are checked where
// the declaration itself is analysed, not here.
type StreamFuncSig struct {
	Category StreamFuncCategory
	// ParamUsage is the type-determined usage (§19.4) of each declared
	// parameter, in declaration order.
	ParamUsage []StreamUsage
	// FirstAdjust is the usage that applies the function conversion rules to
	// the first argument — what §19.8.5.5/.6 call its type-adjusted posture and
	// sweep. It differs from ParamUsage[0]: a required type that triggers no
	// conversion at all leaves the argument's posture and sweep untouched
	// (transmission), where the type-determined usage would say navigation.
	// That is the reading the spec's own worked example demands — the
	// shallow-descent example declares its first parameter as element()* and
	// is stated to be streamable over a striding, consuming argument, which
	// only holds if the type adjustment passes both through.
	FirstAdjust StreamUsage
	// FirstParamNodes reports whether the first parameter's declared type
	// admits document or element nodes (§19.8.5.5's "the intersection of T0
	// with U{document-node(), element()}"), which decides the sweep of a
	// descent call.
	FirstParamNodes bool
	// Streamable records a category other than unclassified, which is what
	// §19.8.5 calls declared-streamable.
	Streamable bool
}

// StreamFuncs resolves a stylesheet function call for the streamability
// analysis. The XSLT layer implements it over the compiled stylesheet; a name
// it does not know reports false, and the call then fails closed exactly as an
// extension function does.
type StreamFuncs interface {
	StreamFunc(prefix, local string, arity int) (StreamFuncSig, bool)
}

// usage is the type-determined usage of the i'th argument. The resolver matched
// on arity, so an index past the declared parameters cannot arise; navigation
// is the fail-closed answer if it ever does.
func (s StreamFuncSig) usage(i int) StreamUsage {
	if i < len(s.ParamUsage) {
		return s.ParamUsage[i]
	}
	return UsageNavigation
}

// strmStylesheetCall classifies a call to a stylesheet function (§19.8.5).
func strmStylesheetCall(sig StreamFuncSig, args []Expr, cp Posture, env *StreamContext) (Posture, Sweep) {
	// §19.8.5: "All function calls to zero-arity stylesheet functions are
	// grounded and motionless" — a streamed node can only reach a function
	// through an argument, and there is none.
	if len(args) == 0 {
		return PostureGrounded, SweepMotionless
	}

	partial := false
	for _, a := range args {
		if _, ok := a.(*Placeholder); ok {
			partial = true
			break
		}
	}
	if partial {
		// §19.8.8.13: partially applying a declared-streamable function to a
		// supplied, non-grounded first argument would capture a streamed node
		// in the function item it produces.
		if sig.Streamable {
			if _, ph := args[0].(*Placeholder); !ph {
				if p, _ := strmExpr(args[0], cp, env); p != PostureGrounded {
					return PostureRoaming, SweepFreeRanging
				}
			}
		}
		ops := make([]StreamOperand, 0, len(args))
		for i, a := range args {
			if _, ok := a.(*Placeholder); ok {
				continue
			}
			p, s := strmExpr(a, cp, env)
			ops = append(ops, StreamOperand{P: p, S: s, U: sig.usage(i), Childless: strmChildless(a, env)})
		}
		return strmGeneral(ops, false, env)
	}

	// Every category but the two descent ones just re-labels the first
	// argument's usage and hands the result to the general rules.
	ops := func(firstUsage StreamUsage) []StreamOperand {
		out := make([]StreamOperand, 0, len(args))
		for i, a := range args {
			u := sig.usage(i)
			if i == 0 {
				u = firstUsage
			}
			p, s := strmExpr(a, cp, env)
			out = append(out, StreamOperand{P: p, S: s, U: u, Childless: strmChildless(a, env)})
		}
		return out
	}

	switch sig.Category {
	case StreamFnUnclassified:
		return strmGeneral(ops(sig.usage(0)), false, env)

	case StreamFnAbsorbing:
		// §19.8.5.2: absorption may not be applied to a crawling sequence,
		// whose members' subtrees can overlap — unlike atomization, a general
		// absorbing function is not expected to cope with that.
		if p, _ := strmExpr(args[0], cp, env); p == PostureCrawling {
			return PostureRoaming, SweepFreeRanging
		}
		return strmGeneral(ops(UsageAbsorption), false, env)

	case StreamFnInspection:
		return strmGeneral(ops(UsageInspection), false, env)

	case StreamFnFilter:
		return strmGeneral(ops(UsageTransmission), false, env)

	case StreamFnAscent:
		// §19.8.5.7: the call reports on ancestors, so it climbs — provided
		// nothing it does moves the stream at all.
		p0, s0 := strmGeneral(ops(UsageInspection), false, env)
		if p0 == PostureRoaming || s0 != SweepMotionless {
			return PostureRoaming, SweepFreeRanging
		}
		if p0 == PostureGrounded {
			return PostureGrounded, SweepMotionless
		}
		return PostureClimbing, SweepMotionless

	case StreamFnShallowDescent, StreamFnDeepDescent:
		return strmDescentCall(sig, args, cp, env)
	}
	return PostureRoaming, SweepFreeRanging
}

// strmDescentCall implements the ordered rules of §19.8.5.5 (shallow-descent)
// and §19.8.5.6 (deep-descent), which differ only in the posture they give a
// call whose first argument carries streamed nodes.
func strmDescentCall(sig StreamFuncSig, args []Expr, cp Posture, env *StreamContext) (Posture, Sweep) {
	ap, as := strmExpr(args[0], cp, env)
	p0, s0 := strmGeneral([]StreamOperand{{
		P: ap, S: as, U: sig.FirstAdjust, Childless: strmChildless(args[0], env),
	}}, false, env)
	if p0 != PostureStriding && p0 != PostureGrounded {
		return PostureRoaming, SweepFreeRanging
	}
	// The arguments after the first form a construct of their own, which may
	// not itself reach into the stream.
	rest := make([]StreamOperand, 0, len(args)-1)
	for i, a := range args[1:] {
		p, s := strmExpr(a, cp, env)
		rest = append(rest, StreamOperand{P: p, S: s, U: sig.usage(i + 1), Childless: strmChildless(a, env)})
	}
	p1, s1 := strmGeneral(rest, false, env)
	if p1 != PostureGrounded {
		return PostureRoaming, SweepFreeRanging
	}
	if s0 == SweepFreeRanging || s1 == SweepFreeRanging || (s0 == SweepConsuming && s1 == SweepConsuming) {
		return PostureRoaming, SweepFreeRanging
	}
	if p0 == PostureGrounded {
		return PostureGrounded, widerSweep(s0, s1)
	}
	p := p0
	if sig.Category == StreamFnDeepDescent {
		// A deep selection can return nodes nested inside one another.
		p = PostureCrawling
	}
	// The sweep stays s0 only where nothing that could carry a subtree can
	// reach the function — neither the declared parameter type nor the
	// argument's own static type. Otherwise the descent has to read into it.
	if sig.FirstParamNodes && !strmChildless(args[0], env) {
		return p, SweepConsuming
	}
	return p, s0
}

func strmFuncName(prefix, local string) string {
	switch prefix {
	case "", "fn":
		return "fn:" + local
	case "map", "array", "math", "xs":
		return prefix + ":" + local
	default:
		return prefix + ":" + local
	}
}

// strmFuncUsages returns the per-argument operand usages of a built-in
// function, or known=false when the function is outside the §19.8.9 catalogue.
func strmFuncUsages(name string, arity int) ([]StreamUsage, bool) {
	if sig, ok := strmFuncSigs[name]; ok {
		out := make([]StreamUsage, arity)
		for i := range out {
			if i < len(sig) {
				out[i] = sig[i]
			} else {
				// Variadic tails (fn:concat) repeat the last declared usage.
				out[i] = sig[len(sig)-1]
			}
		}
		return out, true
	}
	if strmAbsorbingFuncs[name] {
		out := make([]StreamUsage, arity)
		for i := range out {
			out[i] = UsageAbsorption
		}
		return out, true
	}
	if name == "xs:" || len(name) > 3 && name[:3] == "xs:" {
		// A constructor function: §19.8.8.13 gives its single argument
		// absorption.
		return []StreamUsage{UsageAbsorption}, arity == 1
	}
	return nil, false
}

// strmFuncSigs holds every §19.8.9 entry whose usages are not all absorption.
var strmFuncSigs = map[string][]StreamUsage{
	"fn:base-uri":                  {UsageInspection},
	"fn:boolean":                   {UsageInspection},
	"fn:count":                     {UsageInspection},
	"fn:document":                  {UsageAbsorption, UsageInspection},
	"fn:document-uri":              {UsageInspection},
	"fn:element-with-id":           {UsageAbsorption, UsageNavigation},
	"fn:empty":                     {UsageInspection},
	"fn:error":                     {UsageAbsorption, UsageAbsorption, UsageNavigation},
	"fn:exactly-one":               {UsageTransmission},
	"fn:exists":                    {UsageInspection},
	"fn:filter":                    {UsageNavigation, UsageInspection},
	"fn:fold-left":                 {UsageNavigation, UsageAbsorption, UsageInspection},
	"fn:fold-right":                {UsageNavigation, UsageAbsorption, UsageInspection},
	"fn:for-each":                  {UsageNavigation, UsageInspection},
	"fn:for-each-pair":             {UsageNavigation, UsageNavigation, UsageInspection},
	"fn:function-lookup":           {UsageAbsorption, UsageAbsorption},
	"fn:generate-id":               {UsageInspection},
	"fn:has-children":              {UsageInspection},
	"fn:head":                      {UsageTransmission},
	"fn:id":                        {UsageAbsorption, UsageNavigation},
	"fn:idref":                     {UsageAbsorption, UsageNavigation},
	"fn:in-scope-prefixes":         {UsageInspection},
	"fn:innermost":                 {UsageNavigation},
	"fn:insert-before":             {UsageTransmission, UsageAbsorption, UsageTransmission},
	"fn:key":                       {UsageAbsorption, UsageAbsorption, UsageNavigation},
	"fn:lang":                      {UsageAbsorption, UsageInspection},
	"fn:local-name":                {UsageInspection},
	"fn:name":                      {UsageInspection},
	"fn:namespace-uri":             {UsageInspection},
	"fn:namespace-uri-for-prefix":  {UsageAbsorption, UsageInspection},
	"fn:nilled":                    {UsageInspection},
	"fn:node-name":                 {UsageInspection},
	"fn:not":                       {UsageInspection},
	"fn:one-or-more":               {UsageTransmission},
	"fn:path":                      {UsageNavigation},
	"fn:remove":                    {UsageTransmission, UsageAbsorption},
	"fn:resolve-QName":             {UsageAbsorption, UsageInspection},
	"fn:reverse":                   {UsageNavigation},
	"fn:subsequence":               {UsageTransmission, UsageAbsorption, UsageAbsorption},
	"fn:tail":                      {UsageTransmission},
	"fn:trace":                     {UsageTransmission, UsageAbsorption},
	"fn:unordered":                 {UsageTransmission},
	"fn:unparsed-entity-public-id": {UsageAbsorption, UsageInspection},
	"fn:unparsed-entity-uri":       {UsageAbsorption, UsageInspection},
	"fn:zero-or-one":               {UsageTransmission},
	"map:entry":                    {UsageAbsorption, UsageNavigation},
	"map:put":                      {UsageAbsorption, UsageAbsorption, UsageNavigation},
}

// strmAbsorbingFuncs lists the §19.8.9 entries whose arguments are all
// absorption (the large majority of the catalogue).
var strmAbsorbingFuncs = map[string]bool{
	"fn:QName": true, "fn:abs": true, "fn:adjust-date-to-timezone": true,
	"fn:adjust-dateTime-to-timezone": true, "fn:adjust-time-to-timezone": true,
	"fn:analyze-string": true, "fn:available-environment-variables": true,
	"fn:avg": true, "fn:ceiling": true, "fn:codepoint-equal": true,
	"fn:codepoints-to-string": true, "fn:collation-key": true,
	"fn:collection": true, "fn:compare": true, "fn:concat": true,
	"fn:contains": true, "fn:copy-of": true, "fn:current-date": true,
	"fn:current-dateTime": true, "fn:current-output-uri": true,
	"fn:current-time": true, "fn:data": true, "fn:dateTime": true,
	"fn:day-from-date": true, "fn:day-from-dateTime": true,
	"fn:days-from-duration": true, "fn:deep-equal": true,
	"fn:default-collation": true, "fn:distinct-values": true, "fn:doc": true,
	"fn:doc-available": true, "fn:element-available": true,
	"fn:encode-for-uri": true, "fn:ends-with": true,
	"fn:environment-variable": true, "fn:escape-html-uri": true,
	"fn:false": true, "fn:floor": true, "fn:format-date": true,
	"fn:format-dateTime": true, "fn:format-integer": true,
	"fn:format-number": true, "fn:format-time": true,
	"fn:function-arity": true, "fn:function-available": true,
	"fn:function-name": true, "fn:hours-from-dateTime": true,
	"fn:hours-from-duration": true, "fn:hours-from-time": true,
	"fn:implicit-timezone": true, "fn:index-of": true, "fn:iri-to-uri": true,
	"fn:json-to-xml": true, "fn:json-doc": true, "fn:parse-json": true,
	"fn:local-name-from-QName": true, "fn:lower-case": true,
	"fn:matches": true, "fn:max": true, "fn:min": true,
	"fn:minutes-from-dateTime": true, "fn:minutes-from-duration": true,
	"fn:minutes-from-time": true, "fn:month-from-date": true,
	"fn:month-from-dateTime": true, "fn:months-from-duration": true,
	"fn:namespace-uri-from-QName": true, "fn:normalize-space": true,
	"fn:normalize-unicode": true, "fn:number": true, "fn:parse-xml": true,
	"fn:parse-xml-fragment": true, "fn:prefix-from-QName": true,
	// fn:random-number-generator($seed as xs:anyAtomicType?) as map(*) takes
	// an atomic seed and returns a map, so §19.4's type-determined usage makes
	// its one argument absorption like every other atomic-argument entry here.
	// Its absence made any call fail closed to roaming (si-iterate-037).
	"fn:random-number-generator": true,
	"fn:regex-group":             true, "fn:replace": true, "fn:resolve-uri": true,
	"fn:round": true, "fn:round-half-to-even": true,
	"fn:seconds-from-dateTime": true, "fn:seconds-from-duration": true,
	"fn:seconds-from-time": true, "fn:serialize": true, "fn:snapshot": true,
	"fn:starts-with": true, "fn:static-base-uri": true,
	"fn:stream-available": true, "fn:string": true, "fn:string-join": true,
	"fn:string-length": true, "fn:string-to-codepoints": true,
	"fn:substring": true, "fn:substring-after": true,
	"fn:substring-before": true, "fn:sum": true, "fn:system-property": true,
	"fn:timezone-from-date": true, "fn:timezone-from-dateTime": true,
	"fn:timezone-from-time": true, "fn:tokenize": true, "fn:translate": true,
	"fn:true": true, "fn:type-available": true, "fn:unparsed-text": true,
	"fn:unparsed-text-available": true, "fn:unparsed-text-lines": true,
	"fn:upper-case": true, "fn:uri-collection": true, "fn:xml-to-json": true,
	"fn:year-from-date": true, "fn:year-from-dateTime": true,
	"fn:years-from-duration": true,
	"map:contains":           true, "map:for-each": true, "map:get": true,
	"map:keys": true, "map:merge": true, "map:remove": true, "map:size": true,
	"math:acos": true, "math:asin": true, "math:atan": true,
	"math:atan2": true, "math:cos": true, "math:exp": true,
	"math:exp10": true, "math:log": true, "math:log10": true, "math:pi": true,
	"math:pow": true, "math:sin": true, "math:sqrt": true, "math:tan": true,
}

// strmContextDefaults maps a function whose short form defaults an argument
// (to "." or to "/") onto the arity at which that default applies.
var strmContextDefaults = map[string]struct {
	arity int
	root  bool
}{
	"fn:base-uri": {0, false}, "fn:copy-of": {0, false}, "fn:data": {0, false},
	"fn:document-uri": {0, false}, "fn:element-with-id": {1, false},
	"fn:generate-id": {0, false}, "fn:has-children": {0, false},
	"fn:id": {1, false}, "fn:idref": {1, false}, "fn:key": {2, true},
	"fn:lang": {1, false}, "fn:local-name": {0, false}, "fn:name": {0, false},
	"fn:namespace-uri": {0, false}, "fn:nilled": {0, false},
	"fn:node-name": {0, false}, "fn:number": {0, false}, "fn:path": {0, false},
	"fn:snapshot": {0, false}, "fn:string": {0, false},
	"fn:string-length":             {0, false},
	"fn:unparsed-entity-public-id": {1, true},
	"fn:unparsed-entity-uri":       {1, true},
}

// strmFocusDependent reports whether a call of this name and arity reads the
// context item implicitly (§19.8.8.14, §19.8.10's non-positional rule).
func strmFocusDependent(prefix, local string, arity int) bool {
	name := strmFuncName(prefix, local)
	switch name {
	case "fn:position", "fn:last", "fn:current", "fn:current-group",
		"fn:current-grouping-key", "fn:current-merge-group",
		"fn:current-merge-key", "fn:regex-group", "fn:function-lookup",
		"fn:static-base-uri", "fn:default-collation":
		return true
	}
	if d, ok := strmContextDefaults[name]; ok && arity == d.arity {
		return true
	}
	if name == "fn:accumulator-before" || name == "fn:accumulator-after" {
		return true
	}
	return false
}

// --- static shape helpers ---

// strmChildless reports whether an expression can be PROVEN to yield only
// items without children (attributes, namespaces, text/comment/PI nodes, or
// atomic values). Absorption over such an operand is downgraded to inspection
// (§19.8.1), which is what keeps `x[@code = 'a']` motionless. Unknown answers
// false — the conservative side.
func strmChildless(e Expr, env *StreamContext) bool {
	switch v := e.(type) {
	case nil:
		return true
	case *ContextItemExpr:
		return env != nil && env.ContextChildless
	case *LiteralExpr:
		return true
	case *VarRef:
		// The flag exists only to decide whether absorbing a STREAMED operand
		// has to read the input, and a variable that is not bound to a
		// streaming parameter is grounded — nothing it holds can move the
		// stream. This is what keeps a rule such as
		// "if (@amount lt $value) then @amount else $value" motionless.
		return env == nil || !env.StreamingParams[v.Prefix+":"+v.Local]
	case *PathExpr:
		if len(v.Steps) == 0 {
			return false
		}
		last := v.Steps[len(v.Steps)-1]
		if last == nil || last.Postfix != nil {
			return false
		}
		return strmStepChildless(last.Axis, &last.Test)
	case *FilterExpr:
		return strmChildless(v.Primary, env)
	case *SimpleMapExpr:
		// The value of A!B is the value of the last step.
		if len(v.Steps) == 0 {
			return false
		}
		return strmChildless(v.Steps[len(v.Steps)-1], env)
	case *IntersectExceptExpr:
		// A subset of the left operand.
		return strmChildless(v.L, env)
	case *UnionExpr:
		return strmChildless(v.L, env) && strmChildless(v.R, env)
	case *IfExpr:
		return strmChildless(v.Then, env) && strmChildless(v.Else, env)
	case *SequenceExpr:
		for _, it := range v.Items {
			if !strmChildless(it, env) {
				return false
			}
		}
		return len(v.Items) > 0
	case *BinaryExpr, *UnaryExpr, *CompareExpr, *StringConcatExpr, *RangeExpr,
		*CastExpr, *InstanceOfExpr, *MapExpr, *ArrayExpr, *InlineFunc,
		*NamedFuncRef:
		return true
	case *FuncCall:
		// Only the node-returning built-ins matter here; everything else in
		// the catalogue is atomic.
		switch strmFuncName(v.Prefix, v.Local) {
		case "fn:copy-of", "fn:snapshot", "fn:doc", "fn:root", "fn:id",
			"fn:idref", "fn:element-with-id", "fn:key", "fn:collection",
			"fn:parse-xml", "fn:parse-xml-fragment", "fn:json-to-xml",
			"fn:head", "fn:tail", "fn:subsequence", "fn:remove",
			"fn:insert-before", "fn:reverse", "fn:unordered", "fn:outermost",
			"fn:innermost", "fn:for-each", "fn:filter", "fn:fold-left",
			"fn:fold-right", "fn:for-each-pair", "fn:trace", "fn:exactly-one",
			"fn:zero-or-one", "fn:one-or-more", "fn:current-group",
			"fn:current-merge-group", "fn:function-lookup",
			"fn:accumulator-before", "fn:accumulator-after":
			return false
		case "fn:current":
			return env != nil && env.CurrentChildless
		}
		return true
	default:
		return false
	}
}

// strmStepChildless reports whether an axis step can only select childless
// nodes.
func strmStepChildless(axis string, t *NodeTest) bool {
	if axis == "" {
		axis = "child"
	}
	if axis == "attribute" || axis == "namespace" {
		return true
	}
	switch t.Kind {
	case testText, testComment, testPI, testAttribute, testNamespace:
		return true
	}
	return false
}

// strmStepSelectsElements reports whether a step's U-type can include
// element(), which decides crawling vs. striding on the descendant axes.
func strmStepSelectsElements(axis string, t *NodeTest) bool {
	switch axis {
	case "attribute", "namespace":
		return false
	}
	switch t.Kind {
	case testText, testComment, testPI, testAttribute, testNamespace,
		testSchemaAttr:
		return false
	case testDocument:
		return false
	}
	return true
}

// strmPredScanning: motionless (assessed with a striding focus) and
// non-positional.
func strmPredScanning(pr Expr, env *StreamContext) bool {
	if !strmNonPositional(pr) {
		return false
	}
	p, s := strmExpr(pr, PostureStriding, env)
	return s == SweepMotionless && p != PostureRoaming
}

// strmNumericFocusFree implements the "static type is numeric and the
// predicate does not depend on the focus" test shared by §19.8.8.8 and
// §19.8.8.9 — it detects a predicate such as [1] or [$i+1] that selects at
// most one node.
func strmNumericFocusFree(pr Expr) bool {
	return strmStaticNumeric(pr) && strmFocusFree(pr)
}

func strmStaticNumeric(e Expr) bool {
	switch v := e.(type) {
	case *LiteralExpr:
		switch lit := v.Val.(type) {
		case *Atomic:
			return lit.IsNumeric()
		case float64, int, int64:
			return true
		}
		return false
	case *BinaryExpr:
		switch v.Op {
		case "+", "-", "*", "div", "idiv", "mod":
			return true
		}
		return false
	case *UnaryExpr:
		return true
	case *RangeExpr:
		return true
	case *FuncCall:
		switch strmFuncName(v.Prefix, v.Local) {
		case "fn:position", "fn:last", "fn:count", "fn:number", "fn:sum",
			"fn:avg", "fn:min", "fn:max", "fn:abs", "fn:floor", "fn:ceiling",
			"fn:round", "fn:round-half-to-even", "fn:string-length",
			"fn:index-of", "fn:function-arity":
			return true
		}
		return false
	case *FilterExpr:
		return strmStaticNumeric(v.Primary)
	}
	return false
}

// strmFocusFree reports that neither the expression nor any sub-expression
// whose focus-setting container is this predicate reads the focus.
func strmFocusFree(e Expr) bool {
	found := false
	strmWalk(e, func(x Expr) bool {
		switch v := x.(type) {
		case *ContextItemExpr:
			found = true
		case *PathExpr:
			// A relative path is rooted at the context item; an absolute one
			// still needs the focus to find its root.
			found = true
		case *FilterExpr:
			// Its own predicates re-set the focus, but the primary does not.
			return true
		case *FuncCall:
			if strmFocusDependent(v.Prefix, v.Local, len(v.Args)) {
				found = true
			}
		case *NamedFuncRef:
			if strmFocusDependent(v.Prefix, v.Local, v.Arity) {
				found = true
			}
		}
		return !found
	})
	return !found
}

// strmNonPositional implements §19.8.10's non-positional-predicate test: no
// call to position/last/function-lookup outside a nested predicate, and a
// static type that cannot be numeric.
func strmNonPositional(pr Expr) bool {
	if strmStaticNumericLoose(pr) {
		return false
	}
	bad := false
	strmWalkNoNestedPreds(pr, func(x Expr) bool {
		switch v := x.(type) {
		case *FuncCall:
			switch strmFuncName(v.Prefix, v.Local) {
			case "fn:position", "fn:last", "fn:function-lookup":
				bad = true
			}
		case *NamedFuncRef:
			switch strmFuncName(v.Prefix, v.Local) {
			case "fn:position", "fn:last", "fn:function-lookup":
				bad = true
			}
		}
		return !bad
	})
	return !bad
}

// strmStaticNumericLoose is strmStaticNumeric widened to the cases §19.8.10
// calls "potentially numeric" — p[data(@status)] is a positional predicate
// because data() may yield a number.
func strmStaticNumericLoose(e Expr) bool {
	if strmStaticNumeric(e) {
		return true
	}
	if v, ok := e.(*FuncCall); ok {
		switch strmFuncName(v.Prefix, v.Local) {
		case "fn:data", "fn:string-to-codepoints":
			return true
		}
	}
	if _, ok := e.(*VarRef); ok {
		// A variable's static type is unknown; the spec's p[$pnum + 1]
		// example shows the numeric case must be assumed.
		return true
	}
	return false
}

// --- pattern classification (§19.8.10) ---

func strmPatternMotionless(p *Pattern, env *StreamContext) bool {
	for _, alt := range p.alts {
		if !strmPatternPathMotionless(alt, env) {
			return false
		}
	}
	for _, alt := range p.exprAlts {
		if !strmPatternExprMotionless(alt, env) {
			return false
		}
	}
	return true
}

// strmPatternExprMotionless handles the EXPRESSION-shaped pattern alternatives
// — (a|b)[p], (* except xsl:*)[p] and the like. §19.8.10 names only three
// disqualifiers (a RootedPath, a non-motionless or positional top-level
// predicate, a streaming-parameter reference), so the combining operators
// themselves are transparent: each branch is assessed on its own, and a
// predicate written on the parenthesised whole is one more top-level predicate.
func strmPatternExprMotionless(e Expr, env *StreamContext) bool {
	switch v := e.(type) {
	case *PathExpr:
		return strmPatternPathMotionless(v, env)
	case *UnionExpr:
		return strmPatternExprMotionless(v.L, env) && strmPatternExprMotionless(v.R, env)
	case *IntersectExceptExpr:
		return strmPatternExprMotionless(v.L, env) && strmPatternExprMotionless(v.R, env)
	case *FilterExpr:
		if !strmPatternExprMotionless(v.Primary, env) {
			return false
		}
		// The context item type for a predicate on a combined branch is not
		// statically known here, so it is assessed with the pattern's own
		// striding focus and no childless assumption — the conservative side.
		psc := *env
		psc.ContextPosture = PostureStriding
		psc.ContextIsDocument = false
		psc.ContextChildless = false
		for _, pr := range v.Preds {
			if !strmNonPositional(pr) {
				return false
			}
			p, s := strmExpr(pr, PostureStriding, &psc)
			if s != SweepMotionless || p == PostureRoaming {
				return false
			}
		}
		return true
	}
	// Anything else — a variable reference, a function call — IS a RootedPath,
	// which §19.8.10 excludes outright.
	return false
}

func strmPatternPathMotionless(pe *PathExpr, env *StreamContext) bool {
	if pe == nil {
		return false
	}
	// RootedPath: a pattern anchored at a variable reference or function call
	// ($doc//p, id('abc')). A leading "/" is NOT a RootedPath.
	if pe.Start != nil {
		switch pe.Start.(type) {
		case *VarRef, *FuncCall, *DynCall:
			return false
		}
	}
	// fn:current() inside ANY of the pattern's predicates denotes the node the
	// whole pattern matches, which is what the LAST step selects — so its
	// childlessness is a property of the path, not of the step being walked.
	curChildless := false
	if n := len(pe.Steps); n > 0 {
		if last := pe.Steps[n-1]; last != nil && last.Postfix == nil {
			curChildless = strmStepChildless(last.Axis, &last.Test)
		}
	}
	for _, st := range pe.Steps {
		if st == nil {
			return false
		}
		if st.Postfix != nil {
			return false
		}
		axis := st.Axis
		if axis == "" {
			axis = "child"
		}
		// §19.8.10 assesses a pattern's predicates with a striding focus. The
		// one exception is a caller that already knows the nodes being matched
		// are grounded — an xsl:for-each-group over a materialised population —
		// where nothing a predicate does can move the stream.
		ppost := PostureStriding
		if env.ContextPosture == PostureGrounded {
			ppost = PostureGrounded
		}
		psc := *env
		psc.ContextPosture = ppost
		psc.ContextIsDocument = false
		// §19.8.10 assesses a top-level predicate with the context item type
		// set to the static type of the expression it applies to, which is
		// what makes text()[starts-with(., '$')] motionless.
		psc.ContextChildless = strmStepChildless(axis, &st.Test)
		psc.CurrentChildless = curChildless
		for _, pr := range st.Preds {
			// Top-level predicates of a pattern must be motionless (assessed
			// with a striding focus) and non-positional.
			if !strmNonPositional(pr) {
				return false
			}
			p, s := strmExpr(pr, ppost, &psc)
			if s != SweepMotionless || p == PostureRoaming {
				return false
			}
		}
	}
	return true
}

// --- generic traversal helpers ---

// strmWalk calls fn for every sub-expression, depth first. fn returns false to
// stop the walk.
func strmWalk(e Expr, fn func(Expr) bool) {
	strmWalkOpt(e, fn, false)
}

// strmWalkNoNestedPreds is strmWalk but does not descend into the predicates
// of a nested step or filter — §19.8.10's "unless that call or reference
// occurs within a nested predicate".
func strmWalkNoNestedPreds(e Expr, fn func(Expr) bool) {
	strmWalkOpt(e, fn, true)
}

func strmWalkOpt(e Expr, fn func(Expr) bool, skipPreds bool) {
	if e == nil {
		return
	}
	if !fn(e) {
		return
	}
	rec := func(xs ...Expr) {
		for _, x := range xs {
			strmWalkOpt(x, fn, skipPreds)
		}
	}
	switch v := e.(type) {
	case *FuncCall:
		rec(v.Args...)
	case *BinaryExpr:
		rec(v.L, v.R)
	case *UnaryExpr:
		rec(v.X)
	case *UnionExpr:
		rec(v.L, v.R)
	case *IntersectExceptExpr:
		rec(v.L, v.R)
	case *CompareExpr:
		rec(v.L, v.R)
	case *StringConcatExpr:
		rec(v.Parts...)
	case *PathExpr:
		rec(v.Start)
		for _, s := range v.Steps {
			if s == nil {
				continue
			}
			if !skipPreds {
				rec(s.Preds...)
			}
			rec(s.Postfix)
		}
	case *FilterExpr:
		rec(v.Primary)
		if !skipPreds {
			rec(v.Preds...)
		}
	case *SequenceExpr:
		rec(v.Items...)
	case *RangeExpr:
		rec(v.From, v.To)
	case *ForExpr:
		for _, b := range v.Binds {
			rec(b.Seq)
		}
		rec(v.Body)
	case *LetExpr:
		for _, b := range v.Binds {
			rec(b.Seq)
		}
		rec(v.Body)
	case *QuantExpr:
		for _, b := range v.Binds {
			rec(b.Seq)
		}
		rec(v.Satisfies)
	case *IfExpr:
		rec(v.Cond, v.Then, v.Else)
	case *MapExpr:
		rec(v.Keys...)
		rec(v.Vals...)
	case *ArrayExpr:
		rec(v.Items...)
	case *InlineFunc:
		rec(v.Body)
	case *LookupExpr:
		rec(v.Base, v.Key)
	case *DynCall:
		rec(v.Base)
		rec(v.Args...)
	case *InstanceOfExpr:
		rec(v.X)
	case *TreatExpr:
		rec(v.X)
	case *CastExpr:
		rec(v.X)
	case *ArrowExpr:
		rec(v.Base)
		for _, s := range v.Steps {
			rec(s.Spec)
			rec(s.Args...)
		}
	case *SimpleMapExpr:
		rec(v.Steps...)
	}
}

// strmMentionsStreamingParam reports whether the expression textually
// references a variable bound to a streaming parameter.
func strmMentionsStreamingParam(e Expr, env *StreamContext) bool {
	if len(env.StreamingParams) == 0 {
		return false
	}
	found := false
	strmWalk(e, func(x Expr) bool {
		if v, ok := x.(*VarRef); ok && env.StreamingParams[v.Prefix+":"+v.Local] {
			found = true
		}
		return !found
	})
	return found
}

// strmIsDocElementTest reports a SequenceType of the form
// document-node(element(...)), the one ItemType §19.8.8.5 treats as
// absorption.
func strmIsDocElementTest(t *SeqType) bool {
	if t == nil || t.Item.Kind != itNode || t.Item.Node == nil {
		return false
	}
	return t.Item.Node.Kind == testDocument && t.Item.Node.Inner != nil
}
