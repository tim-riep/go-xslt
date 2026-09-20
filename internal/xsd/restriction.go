package xsd

// Particle Valid (Restriction) — XSD 1.0 §3.9.6 / 1.1 §3.9.6. When a complex type
// derives another by restriction, the derived content model must be a valid
// restriction of the base's: an order/structure-preserving mapping where each
// derived particle restricts a corresponding base particle and every unmapped
// base particle is emptiable.
//
// rc.particleRestrictsOK(R, B) reports whether derived particle R is a valid
// restriction of base particle B. Where a case is genuinely undecidable with the
// information modelled (unresolved types, exotic wildcard namespace algebra) it
// errs toward "valid", so it never rejects a schema it cannot prove invalid.

// restrictionChecker carries the schema through the particle-restriction
// recursion so element comparisons can consult substitution groups (and their
// blocking rules). The zero flags are the default comparison context.
type restrictionChecker struct {
	sch *Schema
	// inMapAndSum marks that the comparison sits inside a MapAndSum mapping
	// (seq restricting choice); XSD 1.0 does not grant substitution-group
	// equivalence there (particlesZ028 is invalid in 1.0, valid in 1.1).
	inMapAndSum bool
}

// normalizeParticle collapses "pointless" particles: a model group occurring
// exactly once with a single child particle is equivalent to that child (this is
// how a group reference wrapping one element compares as the element itself).
func normalizeParticle(p *particle) *particle {
	for p != nil && p.min == 1 && p.max == 1 {
		mg, ok := p.term.(*modelGroup)
		if !ok || len(mg.particles) != 1 {
			return p
		}
		p = mg.particles[0]
	}
	return p
}

// flattenSeq splices a POINTLESS sequence wrapper into its parent: XSD 1.1
// Part 1 §3.9.6.3 defines a particle as pointless when "its {term} is a model
// group of variety sequence [and] its parent's {term} is a model group of
// variety sequence and {min occurs}={max occurs}=1", and the model is compared
// with such particles eliminated.
//
// This is exactly the shape xs:complexContent/xs:extension produces: the
// extended type's content is sequence(BASE-CONTENT, EXTENSION-CONTENT) where
// BASE-CONTENT is itself a 1..1 sequence. A derived type that RESTRICTS such an
// extension writes the same content flat, so Recurse's particle-by-particle
// mapping lines an ELEMENT up against a GROUP and rejects a model that is
// language-identical (W3C's own XMLSchema.xsd does this with
// complexRestrictionType restricting restrictionType, via xs:annotated).
//
// Language-preserving by construction — L(seq(A, seq(B, C), D)) is L(seq(A, B,
// C, D)) — so using it only as an extra ACCEPTANCE route (see
// particleRestrictsOK) cannot approve a model the clause tables would reject
// for any reason other than this wrapper.
func flattenSeq(p *particle) *particle {
	if p == nil {
		return nil
	}
	mg, ok := p.term.(*modelGroup)
	if !ok || mg.compositor != cSeq {
		return p
	}
	var out []*particle
	changed := false
	for _, c := range mg.particles {
		if c != nil && c.min == 1 && c.max == 1 {
			if cg, ok := c.term.(*modelGroup); ok && cg.compositor == cSeq {
				out = append(out, cg.particles...)
				changed = true
				continue
			}
		}
		out = append(out, c)
	}
	if !changed {
		return p
	}
	ng := *mg
	ng.particles = out
	np := *p
	np.term = &ng
	// Repeat: splicing may expose a further nested 1..1 sequence.
	return flattenSeq(&np)
}

// particleRestrictsOK first tries the particles as-is; when that rejects, it
// retries with the derived side's pointless wrappers collapsed. Normalization is
// thus purely an ADDITIONAL acceptance route (monotone accept-more): it can fix a
// false rejection (a 1..1 group ref wrapping one element restricting that
// element) but can never newly reject a mapping the raw pass accepted.
func (rc *restrictionChecker) particleRestrictsOK(r, b *particle) bool {
	if rc.particleRestrictsOKRaw(r, b) {
		return true
	}
	if nr := normalizeParticle(r); nr != r && rc.particleRestrictsOKRaw(nr, b) {
		return true
	}
	// Base-side collapse is retried ONLY when the base reduces to a wildcard:
	// that routes into the strict NSRecurseCheckCardinality/occurs check (which
	// cannot blanket-accept), fixing e.g. all{e1,e2,e3} restricting seq{any 3..3}.
	// A general base collapse would instead reach lenient group paths and has
	// been shown to accept invalid restrictions.
	if nb := normalizeParticle(b); nb != b {
		if _, isWc := nb.term.(*wildcard); isWc {
			if rc.particleRestrictsOKRaw(normalizeParticle(r), nb) {
				return true
			}
		}
	}
	// Pointless-sequence elimination on BOTH sides (see flattenSeq), tried as
	// one more accept-more route so a flat restriction of an xs:extension's
	// nested base-content sequence maps particle-for-particle.
	if fr, fb := flattenSeq(r), flattenSeq(b); fr != r || fb != b {
		if rc.particleRestrictsOKRaw(fr, fb) {
			return true
		}
	}
	// XSD 1.1 subsumption, min-0 lifting: with an emptiable base, L(R{min:0})
	// = {ε} ∪ L(R{min:1}) and ε ∈ L(B) — so R fits iff its min:1 twin does
	// (particlesHa161, addB118; in 1.0 the same pairs stay invalid under the
	// clause tables — Ha162 pins that).
	if rc.sch != nil && rc.sch.version == Version11 && r.min == 0 && emptiable(b) {
		r1 := *r
		r1.min = 1
		if rc.particleRestrictsOKRaw(&r1, b) {
			return true
		}
		if nr := normalizeParticle(&r1); nr != &r1 && rc.particleRestrictsOKRaw(nr, b) {
			return true
		}
	}
	return false
}

func (rc *restrictionChecker) particleRestrictsOKRaw(r, b *particle) bool {
	if r == nil {
		return b == nil || emptiable(b)
	}
	if b == nil {
		// Restricting an empty (no-particle) base: the derived particle is valid
		// only if it matches *nothing but* empty (e.g. an empty <sequence/>) — an
		// optional element would add content and is invalid.
		return matchesOnlyEmpty(r)
	}
	switch bt := b.term.(type) {
	case *wildcard:
		// NSRecurseCheckCardinality: a GROUP restricting a wildcard compares its
		// total element-count range (not the group particle's own occurs) against
		// the wildcard's occurrence range; every leaf must be wildcard-admitted.
		if _, isGroup := r.term.(*modelGroup); isGroup {
			lo, hi := particleElementBounds(r)
			if lo < b.min {
				return false
			}
			if b.max != unbounded && (hi == unbounded || hi > b.max) {
				return false
			}
			return leavesMatchWildcard(r, bt)
		}
		// Element/wildcard R: occurrences fit and its namespace is admitted. A
		// derived wildcard additionally must not WEAKEN processContents
		// (strict > lax > skip).
		if rw, isWc := r.term.(*wildcard); isWc {
			if wcProcessRank(rw.process) < wcProcessRank(bt.process) {
				return false
			}
		}
		return occursOK(r, b) && leavesMatchWildcard(r, bt)
	case *ElementDecl:
		re, ok := r.term.(*ElementDecl)
		if !ok {
			// A wildcard cannot restrict an element. A GROUP can when every
			// element leaf is the base element itself or substitutable for it
			// (elemZ027a/b: choice{m1,m2} restricting the head), compared on
			// the element-count basis like NSRecurseCheckCardinality. Value
			// constraints or blocking on the base stay conservative rejects.
			if _, isGroup := r.term.(*modelGroup); isGroup && !bt.hasFixed && len(bt.blocked) == 0 {
				lo, hi := particleElementBounds(r)
				if lo >= b.min && (b.max == unbounded || (hi != unbounded && hi <= b.max)) &&
					rc.leavesSubstitutableFor(r, bt) {
					return true
				}
			}
			return false
		}
		if !occursOK(r, b) || !rc.elementRestrictsElement(re, bt) {
			return false
		}
		if re.name == bt.name {
			return true
		}
		// NameAndTypeOK via substitution: the derived element may be a
		// transitive substitution-group member of the base element that the
		// base permits (abstract members and blocked derivations excluded).
		return rc.substitutableFor(re, bt)
	case *modelGroup:
		switch rt := r.term.(type) {
		case *modelGroup:
			occ := occursOK(r, b)
			if rt.compositor == cSeq && bt.compositor == cChoice {
				// MapAndSum clause 2 is THE occurrence rule for seq→choice: the
				// sequence's summed range — min/max times the particle count —
				// must fit the base choice's range. It both accepts what the
				// plain range check rejects (particlesV003) and rejects what it
				// accepts (particlesV005: (1,2)×2=(2,4) ⊄ (0,2)).
				occ = sumOccursOK(r, rt, b)
			}
			if occ && rc.groupRestrictsGroup(rt, bt) {
				return true
			}
			// XSD 1.1 subsumption, choice over an all: L(choice) = ∪ L(branch),
			// so a 1..1 choice fits iff EVERY branch restricts the whole base
			// all on its own (all231/232 valid; all233.n's a{1,8} branch still
			// fails; the route stays out of 1.0 — particlesHb009). Gated on
			// exact 1..1 occurs: a repeating choice concatenates branch picks
			// and cannot be decomposed per branch.
			if rc.sch != nil && rc.sch.version == Version11 &&
				rt.compositor == cChoice && bt.compositor == cAll &&
				r.min == 1 && r.max == 1 && len(rt.particles) > 0 {
				ok := true
				for _, br := range rt.particles {
					if !rc.particleRestrictsOK(br, b) {
						ok = false
						break
					}
				}
				if ok {
					return true
				}
			}
			return false
		case *ElementDecl, *wildcard:
			// RecurseAsIfGroup is ambiguous about whether the synthesized wrapper
			// group carries R's occurrence range (with R inside at 1..1) or 1..1
			// (with R keeping its occurs). The suite grants the permissive reading
			// only when the base group repeats without bound (particlesZ001), and
			// holds the strict one elsewhere (particlesHa161/K006), so the
			// wrapper-carries-occurs route is gated on b.max == unbounded.
			if b.max == unbounded && occursOK(r, b) {
				r1 := *r
				r1.min, r1.max = 1, 1
				if rc.particleRestrictsGroup(&r1, bt) {
					return true
				}
			}
			// Legacy route kept as an additional acceptance path: R with its own
			// occurs against a base group admitting exactly one iteration.
			if b.min > 1 || b.max == 0 {
				return false
			}
			return rc.particleRestrictsGroup(r, bt)
		}
	}
	return true
}

// wcProcessRank orders wildcard processContents by strictness (skip < lax <
// strict); a restriction may only keep or raise the rank.
func wcProcessRank(p string) int {
	switch p {
	case "skip":
		return 0
	case "lax":
		return 1
	default: // "strict" (the default)
		return 2
	}
}

// sumOccursOK implements the MapAndSum occurrence-sum step: the derived
// sequence's total range (occurs multiplied by its content-bearing particle
// count) must be within the base choice particle's range.
func sumOccursOK(r *particle, rmg *modelGroup, b *particle) bool {
	// Spec letter: the factor is the NUMBER OF PARTICLES (§3.9.6 MapAndSum 2),
	// not the content-bearing count — verified against particlesV001-V020.
	n := len(rmg.particles)
	if r.min*n < b.min {
		return false
	}
	if b.max == unbounded {
		return true
	}
	if r.max == unbounded {
		return false
	}
	return r.max*n <= b.max
}

// occursOK reports whether R's occurrence range is within B's (a subset).
func occursOK(r, b *particle) bool {
	if r.min < b.min {
		return false
	}
	if b.max == unbounded {
		return true
	}
	if r.max == unbounded {
		return false
	}
	return r.max <= b.max
}

func emptiable(p *particle) bool { return upaNullable(p) }

// matchesOnlyEmpty reports whether particle p can match nothing but the empty
// sequence — i.e. it contributes no element content at all. An empty model group
// (e.g. <sequence/>) qualifies; any element/wildcard that can occur does not.
func matchesOnlyEmpty(p *particle) bool {
	if p == nil || p.max == 0 {
		return true
	}
	mg, ok := p.term.(*modelGroup)
	if !ok {
		return false // an element or wildcard contributes content
	}
	for _, sub := range mg.particles {
		if !matchesOnlyEmpty(sub) {
			return false
		}
	}
	return true
}

// groupRestrictsGroup handles the same-compositor recursions and seq-vs-all.
func (rc *restrictionChecker) groupRestrictsGroup(r, b *modelGroup) bool {
	if r.compositor == b.compositor {
		switch r.compositor {
		case cSeq:
			return rc.recurseSeq(r.particles, b.particles) ||
				rc.recurseSeqLenient(r.particles, b.particles)
		case cChoice:
			// rcase-RecurseLax is ORDER-PRESERVING in 1.0 (particlesT002/T009);
			// 1.1 subsumption is order-free.
			if rc.sch != nil && rc.sch.version == Version10 {
				return rc.recurseChoiceOrdered(r.particles, b.particles)
			}
			return rc.recurseChoice(r.particles, b.particles)
		case cAll:
			// rcase-Recurse (all:all) is order-preserving in 1.0 too
			// (particlesS002); 1.1 keeps the unordered mapping (all221 fan-in).
			if rc.sch != nil && rc.sch.version == Version10 {
				return rc.recurseSeq(r.particles, b.particles)
			}
			return rc.recurseUnordered(r.particles, b.particles)
		}
	}
	if r.compositor == cSeq && b.compositor == cAll {
		return rc.recurseUnordered(r.particles, b.particles)
	}
	if r.compositor == cSeq && b.compositor == cChoice {
		prev := rc.inMapAndSum
		rc.inMapAndSum = true
		ok := rc.mapAndSumMapping(r, b)
		rc.inMapAndSum = prev
		return ok
	}
	// Every other compositor pairing (choice:seq, all:seq, all:choice, choice:all)
	// is Forbidden by the Particle Valid (Restriction) case table (§3.9.6).
	return false
}

// mapAndSumMapping is the MapAndSum mapping step: every non-empty derived
// particle must be a valid restriction of SOME base choice branch. (The
// occurrence-sum step is enforced separately via sumOccursOK.)
func (rc *restrictionChecker) mapAndSumMapping(r, b *modelGroup) bool {
	for _, rp := range r.particles {
		if matchesOnlyEmpty(rp) {
			continue
		}
		ok := false
		for _, bp := range b.particles {
			if rc.particleRestrictsOK(rp, bp) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// elementRestrictsElement applies the NameAndTypeOK clauses that concern the
// two element declarations themselves (nillable, blocking, fixed values, type
// derivation) — name and occurrence are the caller's business.
func (rc *restrictionChecker) elementRestrictsElement(re, bt *ElementDecl) bool {
	if re.nillable && !bt.nillable {
		return false // NameAndTypeOK 2: nillable may only narrow (true → false)
	}
	for k := range bt.blocked {
		if !re.blocked[k] {
			return false // NameAndTypeOK 5: disallowed substitutions must not shrink
		}
	}
	if bt.hasFixed {
		// NameAndTypeOK 4: a base fixed value constraint must be preserved
		// with the same value (compared in the value space when possible).
		if !re.hasFixed {
			return false
		}
		if !lexEqualCollapsed(re.fixed, bt.fixed) {
			st := simpleContentType(bt.typ)
			if st == nil || !st.fixedEqual(re.fixed, bt.fixed) {
				return false
			}
		}
	}
	// 1.1 NameAndTypeOK also demands the {type table}s be equivalent (bug
	// 12185, cta0043): same alternative count, pairwise identical @test text
	// and type (the trailing default alternative compares as rawTest "").
	if rc.sch != nil && rc.sch.version == Version11 {
		if len(re.alternatives) != len(bt.alternatives) {
			return false
		}
		for i, a := range re.alternatives {
			b := bt.alternatives[i]
			if a.rawTest != b.rawTest || a.typ != b.typ {
				return false
			}
		}
	}
	return typeRestrictsFromStrict(re.typ, bt.typ)
}

// substitutableFor reports whether derived element re may stand in for base
// element bt through bt's substitution group (transitive, honoring @block and
// abstract). In XSD 1.0 this equivalence is not granted inside a MapAndSum
// mapping (particlesZ028).
func (rc *restrictionChecker) substitutableFor(re, bt *ElementDecl) bool {
	if rc.sch == nil {
		return false
	}
	if rc.inMapAndSum && rc.sch.version == Version10 {
		return false
	}
	return rc.sch.matchesElementName(bt, re.name)
}

// leavesSubstitutableFor reports whether every element leaf of r is the base
// element bt itself or substitutable for it (wildcard leaves disqualify).
func (rc *restrictionChecker) leavesSubstitutableFor(r *particle, bt *ElementDecl) bool {
	ok := true
	seen := map[*modelGroup]bool{}
	var walk func(p *particle)
	walk = func(p *particle) {
		if p == nil || !ok || p.max == 0 {
			return
		}
		switch t := p.term.(type) {
		case *ElementDecl:
			if t.name == bt.name {
				// The base element itself as a leaf: the 1.0 case table holds
				// group:elt Forbidden unless the group is the head's
				// substitution expansion (particlesHb011 invalid, elemZ027a
				// valid); 1.1 subsumption admits it.
				if rc.sch != nil && rc.sch.version == Version10 {
					ok = false
				} else if !typeRestrictsFrom(t.typ, bt.typ) {
					ok = false
				}
			} else if !rc.substitutableFor(t, bt) {
				ok = false
			} else if !typeRestrictsFrom(t.typ, bt.typ) {
				ok = false
			}
		case *wildcard:
			ok = false
		case *modelGroup:
			if seen[t] {
				return
			}
			seen[t] = true
			for _, sub := range t.particles {
				walk(sub)
			}
		}
	}
	walk(r)
	return ok
}

// recurseSeq: an order-preserving one-to-one mapping of R's particles onto B's,
// skipping only emptiable base particles. (A more permissive mapping that lets a
// repeatable base wildcard absorb several derived particles was tried but proved
// to reject valid restrictions on net — the ~46 "multiple particles restrict a
// repeatable wildcard" cases remain a known limitation.)
func (rc *restrictionChecker) recurseSeq(rs, bs []*particle) bool {
	bi := 0
	for _, rp := range rs {
		if matchesOnlyEmpty(rp) {
			continue // a removed particle (e.g. maxOccurs=0) contributes nothing
		}
		matched := false
		for bi < len(bs) {
			if rc.particleRestrictsOK(rp, bs[bi]) {
				bi++
				matched = true
				break
			}
			// 1.1 subsumption: a 1..1 CHOICE may cover a contiguous RUN of
			// base particles when EVERY branch restricts the run-as-sequence
			// on its own — only one branch materializes, and the members it
			// leaves unused must be emptiable, which the per-branch sequence
			// check enforces (particlesHb008: choice(e3{2,3}|e4{1,3}) over
			// e3{0,3},e4{0,3}).
			if rc.sch != nil && rc.sch.version == Version11 {
				if mg, ok := rp.term.(*modelGroup); ok && mg.compositor == cChoice &&
					rp.min == 1 && rp.max == 1 {
					if j := rc.choiceRunFits(mg, bs, bi); j > bi {
						bi = j
						matched = true
						break
					}
				}
			}
			if !emptiable(bs[bi]) {
				return false
			}
			bi++
		}
		if !matched {
			return false
		}
	}
	for ; bi < len(bs); bi++ {
		if !emptiable(bs[bi]) {
			return false
		}
	}
	return true
}

// choiceRunFits returns the end index of the shortest run bs[bi:end] of base
// particles such that every branch of the derived choice restricts the run as
// a 1..1 sequence, or 0 when no run works.
func (rc *restrictionChecker) choiceRunFits(mg *modelGroup, bs []*particle, bi int) int {
	for end := bi + 1; end <= len(bs); end++ {
		run := &particle{min: 1, max: 1, term: &modelGroup{compositor: cSeq, particles: bs[bi:end]}}
		ok := true
		for _, br := range mg.particles {
			if !rc.particleRestrictsOK(br, run) {
				ok = false
				break
			}
		}
		if ok {
			return end
		}
	}
	return 0
}

// recurseSeqLenient is a fallback tried only after the strict one-to-one mapping
// fails: it lets a repeatable base particle (e.g. <any minOccurs=2 maxOccurs=3/>)
// absorb several consecutive derived particles. Because it runs ONLY when the
// strict mapping already rejected, it can only *accept* more — never reject a
// restriction the strict pass allowed — so it cannot introduce a false positive.
func (rc *restrictionChecker) recurseSeqLenient(rs, bs []*particle) bool {
	ri := 0
	for _, bp := range bs {
		if w, ok := bp.term.(*wildcard); ok {
			// A repeatable base wildcard absorbs the run of derived particles it can
			// match; their summed occurrence range must fit within the wildcard's.
			sumMin, sumMax, unbMax := 0, 0, false
			for ri < len(rs) && leavesMatchWildcard(rs[ri], w) {
				lo, hi := particleElementBounds(rs[ri])
				sumMin += lo
				if hi == unbounded {
					unbMax = true
				} else {
					sumMax += hi
				}
				ri++
			}
			if sumMin < bp.min {
				return false
			}
			if bp.max != unbounded && (unbMax || sumMax > bp.max) {
				return false
			}
			continue
		}
		if ri < len(rs) && rc.particleRestrictsOK(rs[ri], bp) {
			ri++
		} else if !emptiable(bp) {
			return false
		}
	}
	return ri == len(rs)
}

// particleElementBounds returns the range of element counts a particle can
// contribute, accounting for nested groups × their occurrence (a repeatable
// wildcard restriction compares the derived run against the wildcard's occurrence
// on this element-count basis).
func particleElementBounds(p *particle) (min, max int) {
	mg, ok := p.term.(*modelGroup)
	if !ok {
		return p.min, p.max // an element/wildcard contributes p.min..p.max elements
	}
	var cmin, cmax int
	if mg.compositor == cChoice {
		cmin = -1
		for _, sub := range mg.particles {
			smin, smax := particleElementBounds(sub)
			if cmin < 0 || smin < cmin {
				cmin = smin
			}
			if smax == unbounded {
				cmax = unbounded
			} else if cmax != unbounded && smax > cmax {
				cmax = smax
			}
		}
		if cmin < 0 {
			cmin = 0
		}
	} else { // sequence, all
		for _, sub := range mg.particles {
			smin, smax := particleElementBounds(sub)
			cmin += smin
			if smax == unbounded || cmax == unbounded {
				cmax = unbounded
			} else {
				cmax += smax
			}
		}
	}
	min = cmin * p.min
	if p.max == unbounded || cmax == unbounded {
		max = unbounded
	} else {
		max = cmax * p.max
	}
	return
}

// recurseChoiceOrdered: every R branch restricts some B branch, in an
// order-preserving mapping where each base branch is used at most once
// (XSD 1.0 rcase-RecurseLax).
func (rc *restrictionChecker) recurseChoiceOrdered(rs, bs []*particle) bool {
	bi := 0
	for _, rp := range rs {
		if matchesOnlyEmpty(rp) {
			continue // a removed branch just drops that option
		}
		matched := false
		for bi < len(bs) {
			if rc.particleRestrictsOK(rp, bs[bi]) {
				matched = true
				bi++
				break
			}
			bi++
		}
		if !matched {
			return false
		}
	}
	return true
}

// recurseChoice: every R branch restricts some B branch (order-independent).
func (rc *restrictionChecker) recurseChoice(rs, bs []*particle) bool {
	for _, rp := range rs {
		if matchesOnlyEmpty(rp) {
			continue // a removed branch (e.g. maxOccurs=0) just drops that option
		}
		ok := false
		for _, bp := range bs {
			if rc.particleRestrictsOK(rp, bp) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// recurseUnordered (all / seq-vs-all): each R particle maps to a distinct B
// particle; unmapped base particles must be emptiable.
func (rc *restrictionChecker) recurseUnordered(rs, bs []*particle) bool {
	used := make([]bool, len(bs))
	reserved := make([]bool, len(bs))
	type acc struct {
		lo, hi int
		unb    bool
	}
	sums := map[int]*acc{}
	var fanWCs []*wildcard
	accumulate := func(tgt int, rp *particle) {
		a := sums[tgt]
		if a == nil {
			a = &acc{}
			sums[tgt] = a
		}
		lo, hi := particleElementBounds(rp)
		a.lo += lo
		if hi == unbounded {
			a.unb = true
		} else {
			a.hi += hi
		}
		used[tgt] = true
	}
	for _, rp := range rs {
		if matchesOnlyEmpty(rp) {
			continue // a removed particle contributes nothing
		}
		// 1.1 subsumption: EVERY derived wildcard particle a base wildcard
		// subsumes (namespace/notQName/processContents-wise) accumulates onto
		// it — exact 1:1 fits included, so the occurrence-sum check at the end
		// sees all contributors (all237/wild050 valid; all235.n hi 3+3>5 and
		// all236.n lo 2+2<5 stay invalid). A derived wildcard can never
		// restrict a base element, so this pre-route skips nothing else.
		if rc.sch != nil && rc.sch.version == Version11 {
			if rw, isWc := rp.term.(*wildcard); isWc {
				tgt := -1
				for i, bp := range bs {
					if bw, isW := bp.term.(*wildcard); isW && leavesMatchWildcard(rp, bw) {
						tgt = i
						break
					}
				}
				if tgt >= 0 {
					accumulate(tgt, rp)
					fanWCs = append(fanWCs, rw)
					continue
				}
			}
		}
		matched := false
		for i, bp := range bs {
			if used[i] || reserved[i] {
				continue
			}
			if rc.particleRestrictsOK(rp, bp) {
				used[i] = true
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		// A 1..1 CHOICE member of the derived model: only one branch
		// materializes, so it fits when every branch restricts a DISTINCT
		// still-free base member (all234). The targets are only RESERVED, not
		// used — the final emptiability check still applies to them, exactly
		// because the unpicked branches leave their members absent.
		if rc.sch != nil && rc.sch.version == Version11 {
			if mg, ok := rp.term.(*modelGroup); ok && mg.compositor == cChoice &&
				rp.min == 1 && rp.max == 1 {
				taken := map[int]bool{}
				allOK := true
				for _, br := range mg.particles {
					found := -1
					for i, bp := range bs {
						if used[i] || reserved[i] || taken[i] {
							continue
						}
						if rc.particleRestrictsOK(br, bp) {
							found = i
							break
						}
					}
					if found < 0 {
						allOK = false
						break
					}
					taken[found] = true
				}
				if allOK {
					for i := range taken {
						reserved[i] = true
					}
					continue
				}
			}
		}
		// Substitution fan-in: several derived element members substitutable
		// for the same base ELEMENT particle map onto it together, their
		// occurrence ranges summed against its range (all221: A1{6,8} and
		// A2{6,8} restricting a{10,20}).
		if re, isEl := rp.term.(*ElementDecl); isEl {
			tgt := -1
			for i, bp := range bs {
				if bt, isB := bp.term.(*ElementDecl); isB {
					if (re.name == bt.name || rc.substitutableFor(re, bt)) &&
						rc.elementRestrictsElement(re, bt) {
						tgt = i
						break
					}
				}
			}
			if tgt >= 0 {
				a := sums[tgt]
				if a == nil {
					a = &acc{}
					sums[tgt] = a
				}
				a.lo += rp.min
				if rp.max == unbounded {
					a.unb = true
				} else {
					a.hi += rp.max
				}
				used[tgt] = true
				continue
			}
		}
		// GROUP-shaped stragglers a single base wildcard subsumes fan in the
		// same way (their element-count bounds are group totals).
		if rc.sch != nil && rc.sch.version == Version11 {
			tgt := -1
			for i, bp := range bs {
				if bw, isW := bp.term.(*wildcard); isW && leavesMatchWildcard(rp, bw) {
					tgt = i
					break
				}
			}
			if tgt >= 0 {
				accumulate(tgt, rp)
				continue
			}
		}
		if !coveredByWildcardUnion(rp, bs) {
			return false
		}
	}
	for i, a := range sums {
		bp := bs[i]
		if a.lo < bp.min {
			return false
		}
		if bp.max != unbounded && (a.unb || a.hi > bp.max) {
			return false
		}
	}
	for i, bp := range bs {
		if !used[i] && !emptiable(bp) {
			return false
		}
		// 1.1 all-group competition: a REMOVED (emptiable, unused) base
		// element whose name a derived wildcard admits flips the attribution —
		// the base element-assesses that name, the derived wildcard-assesses
		// it — so the derived only restricts the base if the wildcard route is
		// at most as permissive: the name's global declaration type must
		// derive from the removed particle's type; a lax/skip wildcard with NO
		// declaration admits anything and cannot restrict it (wild069 invalid
		// vs wild068's sequence twin, where order shields the removed slot).
		if !used[i] && rc.sch != nil && rc.sch.version == Version11 {
			if bt, isEl := bp.term.(*ElementDecl); isEl {
				for _, rw := range fanWCs {
					if !wildcardMatches(rw, bt.name) {
						continue
					}
					g, ok := rc.sch.elements[bt.name]
					if !ok {
						if rw.process == "strict" {
							continue // strict without a declaration rejects the name outright
						}
						return false
					}
					if !typeRestrictsFrom(g.typ, bt.typ) {
						return false
					}
				}
			}
		}
	}
	return true
}

// coveredByWildcardUnion reports whether a derived wildcard particle is admitted
// by the *combination* of the base's wildcard members. No single base particle
// subsumes it then, but the base may still accept everything it does — e.g. an
// xs:all carrying one ##local and one notNamespace="##local" wildcard, restricted
// to a single ##any wildcard. The subset test is decided over a FINITE witness
// alphabet: wildcard admission depends only on (a) which of the mentioned
// namespaces a name's namespace is, and (b) whether the exact QName sits on a
// notQName list — so every mentioned namespace plus the absent one plus one
// fresh namespace, crossed with every mentioned local plus one fresh local,
// covers all equivalence classes (wild048: {c} and x:e escape both base
// wildcards).
func coveredByWildcardUnion(rp *particle, bs []*particle) bool {
	rw, ok := rp.term.(*wildcard)
	if !ok {
		return false
	}
	var ws []*wildcard
	for _, bp := range bs {
		if w, ok := bp.term.(*wildcard); ok {
			ws = append(ws, w)
		}
	}
	if len(ws) < 2 {
		return false
	}
	nsSet := map[string]bool{"": true, "\x01fresh-ns": true}
	localSet := map[string]bool{"\x01fresh": true}
	var collect func(w *wildcard)
	collect = func(w *wildcard) {
		for _, p := range w.parts {
			collect(p)
		}
		for _, ns := range w.namespaces {
			nsSet[ns] = true
		}
		if w.nsMode == "other" {
			nsSet[w.targetNS] = true
		}
		for _, ns := range w.notNS {
			nsSet[ns] = true
		}
		for _, q := range w.notNames {
			nsSet[q.Space] = true
			localSet[q.Local] = true
		}
		for _, q := range w.notDefNames {
			nsSet[q.Space] = true
			localSet[q.Local] = true
		}
	}
	collect(rw)
	for _, w := range ws {
		collect(w)
	}
	for ns := range nsSet {
		for local := range localSet {
			nn := xname{ns, local}
			if !wildcardMatches(rw, nn) {
				continue
			}
			admitted := false
			for _, w := range ws {
				if wildcardMatches(w, nn) {
					admitted = true
					break
				}
			}
			if !admitted {
				return false
			}
		}
	}
	return true
}

// particleRestrictsGroup: a single element/wildcard R restricting a group B.
func (rc *restrictionChecker) particleRestrictsGroup(r *particle, b *modelGroup) bool {
	switch b.compositor {
	case cChoice:
		for _, bp := range b.particles {
			if rc.particleRestrictsOK(r, bp) {
				return true
			}
		}
		return false
	default: // sequence, all
		matched := false
		for _, bp := range b.particles {
			if !matched && rc.particleRestrictsOK(r, bp) {
				matched = true
				continue
			}
			if !emptiable(bp) {
				return false
			}
		}
		return matched
	}
}

// leavesMatchWildcard reports whether every element/wildcard leaf of r is
// admitted by wildcard w.
func leavesMatchWildcard(r *particle, w *wildcard) bool {
	ok := true
	seen := map[*modelGroup]bool{}
	var walk func(p *particle)
	walk = func(p *particle) {
		if p == nil || !ok {
			return
		}
		switch t := p.term.(type) {
		case *ElementDecl:
			if !wildcardMatches(w, t.name) {
				ok = false
			}
		case *wildcard:
			// A restricting wildcard may neither widen the namespace set nor
			// WEAKEN processContents (wildZ008/009), nor drop the base's
			// notQName exclusions (wild050).
			if !nsSubset(t, w) || wcProcessRank(t.process) < wcProcessRank(w.process) ||
				!wcNameSubsetOK(t, w) {
				ok = false
			}
		case *modelGroup:
			if seen[t] {
				return
			}
			seen[t] = true
			for _, sub := range t.particles {
				walk(sub)
			}
		}
	}
	walk(r)
	return ok
}

// nsSubset reports whether wildcard a's namespace constraint is a subset of b's.
// Conservative: returns true unless it can show a admits a namespace b forbids.
func nsSubset(a, b *wildcard) bool {
	// XSD 1.1 negated constraints ("everything but these namespaces"). A larger
	// exclusion list is the *smaller* set, so the subset direction inverts.
	if len(b.notNS) > 0 {
		switch a.nsMode {
		case "list":
			for _, ns := range a.namespaces {
				if containsStr(b.notNS, ns) {
					return false
				}
			}
			return true
		case "other":
			// a admits everything but its target namespace and the absent one.
			for _, ns := range b.notNS {
				if ns != a.targetNS && ns != "" {
					return false
				}
			}
			return true
		default: // "any", possibly itself negated
			for _, ns := range b.notNS {
				if !containsStr(a.notNS, ns) {
					return false
				}
			}
			return true
		}
	}
	if len(a.notNS) > 0 {
		switch b.nsMode {
		case "any":
			return true
		case "other":
			return containsStr(a.notNS, b.targetNS) && containsStr(a.notNS, "")
		default:
			return false // a is infinite, b is a finite list
		}
	}
	switch b.nsMode {
	case "any":
		return true
	case "other":
		// b admits anything except its targetNS (and absent).
		switch a.nsMode {
		case "any":
			return false
		case "other":
			return a.targetNS == b.targetNS
		case "list":
			for _, ns := range a.namespaces {
				if ns == b.targetNS || ns == "" {
					return false
				}
			}
			return true
		}
	case "list":
		if a.nsMode != "list" {
			return false
		}
		for _, ns := range a.namespaces {
			if !containsStr(b.namespaces, ns) {
				return false
			}
		}
		return true
	}
	return true
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
