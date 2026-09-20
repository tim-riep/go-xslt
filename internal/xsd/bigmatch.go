package xsd

import (
	"fmt"
	"os"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Large-instance counting matcher (M32). The memoized position-set NFA in
// content.go is ~O(P·N²) and gives no verdict above 600 children; this file
// adds a LINEAR greedy interpreter with occurrence counters, used only above
// that cap. It is sound only for name-DETERMINISTIC models, so a static
// pre-scan DECLINES anything it cannot prove (xs:all, cyclic group refs,
// overlapping choice branches, wildcard-vs-element competition) — a declined
// model keeps today's no-verdict behavior. Greedy loop-vs-exit and
// inner-vs-outer counter attribution are correct for UPA-valid models (the
// classic counter-ambiguous shape a{1,2}·a is UPA-invalid and never reaches
// validation); the whole engine was additionally A/B-validated against the
// NFA over every ≤600-child instance of the W3C suite.

// countMatch reports whether kids match particle p, and whether that verdict
// was actually decided. It never returns a guessed verdict: decided=false
// means "use another engine or give no verdict".
// MATCHED is always trustworthy — the greedy run found a witness parse. An
// INVALID verdict is trustworthy only when no COUNTER-SPLITTING ambiguity was
// met along the way: UPA cannot forbid a position competing with ITSELF
// across loop iterations (seq(e{1,2}){2,10} on "ee", cho(foo{3,5},..){0,inf}
// on 6xfoo — the A/B campaign's five divergence shapes), so whenever an
// iteration was cut at maxOccurs with the next child still acceptable, or a
// repeat failed its minOccurs after consuming SOMETHING, a different
// iteration allocation might have succeeded — the run reports undecided.
func (s *Schema) countMatch(p *particle, kids []*xmltree.Node) (matched, decided bool) {
	if p == nil {
		return len(kids) == 0, true
	}
	if !s.bigDeterministic(p) {
		return false, false
	}
	r := &bigRun{s: s, kids: kids}
	pos, ok := r.particle(p, 0)
	matched = ok && pos == len(kids)
	if !matched && r.unsure {
		return false, false
	}
	return matched, true
}

// bigRun is one greedy counting run; unsure records that an INVALID outcome
// might be an artifact of the greedy iteration allocation.
type bigRun struct {
	s      *Schema
	kids   []*xmltree.Node
	unsure bool
}

// bigParticle consumes kids[pos:] greedily for particle p: while another
// iteration of the term can START with the current child's name (and the
// counter allows), run it; a started iteration that fails is a definitive
// mismatch — deterministic models commit on the first symbol.
func (r *bigRun) particle(p *particle, pos int) (int, bool) {
	if p == nil || p.max == 0 {
		return pos, true // a maxOccurs=0 particle matches nothing (ε)
	}
	count := 0
	for {
		if p.max != unbounded && count >= p.max {
			// Cut off at maxOccurs while the next child could still start
			// another iteration: with max > 1 a different allocation could
			// have left room (max == 1 has nothing to reallocate — UPA
			// guarantees no OTHER position wants the name, particlesZ036_a).
			if p.max > 1 && pos < len(r.kids) && r.s.firstAcceptsTerm(p.term, nameOf(r.kids[pos])) {
				r.unsure = true
			}
			break
		}
		if pos >= len(r.kids) || !r.s.firstAcceptsTerm(p.term, nameOf(r.kids[pos])) {
			break
		}
		np, ok := r.term(p.term, pos)
		if !ok {
			return pos, false
		}
		if np == pos {
			break // a nullable term consumed nothing; more iterations are pointless
		}
		pos = np
		count++
	}
	if count < p.min && !bigNullableTerm(p.term) {
		if count >= 1 {
			r.unsure = true // shorter earlier iterations might have fed more
		}
		return pos, false // minOccurs unsatisfied and no vacuous iterations possible
	}
	return pos, true
}

// term runs ONE iteration of a term at pos.
func (r *bigRun) term(t term, pos int) (int, bool) {
	switch v := t.(type) {
	case *ElementDecl:
		if pos < len(r.kids) && r.s.matchesElementName(v, nameOf(r.kids[pos])) {
			return pos + 1, true
		}
		return pos, false
	case *wildcard:
		if pos < len(r.kids) && wildcardMatches(v, nameOf(r.kids[pos])) {
			return pos + 1, true
		}
		return pos, false
	case *modelGroup:
		switch v.compositor {
		case cChoice:
			// The pre-scan guarantees at most one branch can start with the
			// current name.
			if pos < len(r.kids) {
				nn := nameOf(r.kids[pos])
				for _, br := range v.particles {
					if br.max != 0 && r.s.firstAcceptsTerm(br.term, nn) {
						return r.particle(br, pos)
					}
				}
			}
			// No branch starts here: the choice matches ε iff some branch can.
			for _, br := range v.particles {
				if br.min == 0 || (br.max != 0 && bigNullableTerm(br.term)) {
					return pos, true
				}
			}
			return pos, false
		default: // cSeq (cAll is declined by the pre-scan)
			for _, sub := range v.particles {
				np, ok := r.particle(sub, pos)
				if !ok {
					return pos, false
				}
				pos = np
			}
			return pos, true
		}
	}
	return pos, false
}

// firstAcceptsTerm reports whether an iteration of t can START by consuming
// an element named nn.
func (s *Schema) firstAcceptsTerm(t term, nn xname) bool {
	switch v := t.(type) {
	case *ElementDecl:
		return s.matchesElementName(v, nn)
	case *wildcard:
		return wildcardMatches(v, nn)
	case *modelGroup:
		if v.compositor == cChoice {
			for _, br := range v.particles {
				if br.max != 0 && s.firstAcceptsTerm(br.term, nn) {
					return true
				}
			}
			return false
		}
		for _, sub := range v.particles { // cSeq: the nullable prefix
			if sub.max == 0 {
				continue
			}
			if s.firstAcceptsTerm(sub.term, nn) {
				return true
			}
			if !upaNullable(sub) {
				return false
			}
		}
		return false
	}
	return false
}

// bigNullableTerm reports whether one iteration of t can match ε.
func bigNullableTerm(t term) bool {
	return upaNullable(&particle{min: 1, max: 1, term: t})
}

// bigDeterministic is the static pre-scan: it accepts only models the greedy
// interpreter is provably exact on. Declines: xs:all; cyclic group refs; a
// choice whose branches' first-name sets overlap (name-vs-name via the
// substitution-expanded match sets, any wildcard-vs-wildcard, and
// wildcard-vs-name); and any wildcard in the model that admits the name of
// any element in the model (competition — mirroring upa.go's conservative
// element-vs-wildcard gap in 1.1).
func (s *Schema) bigDeterministic(p *particle) bool {
	var elems []*ElementDecl
	var wilds []*wildcard
	cache := map[*ElementDecl]map[xname]bool{}
	seen := map[*modelGroup]bool{}
	ok := true
	var walk func(pp *particle)
	walk = func(pp *particle) {
		if pp == nil || pp.max == 0 || !ok {
			return
		}
		switch t := pp.term.(type) {
		case *ElementDecl:
			elems = append(elems, t)
		case *wildcard:
			wilds = append(wilds, t)
		case *modelGroup:
			if t.compositor == cAll {
				ok = false
				return
			}
			if seen[t] {
				ok = false // cyclic group reference: not provably deterministic
				return
			}
			seen[t] = true
			if t.compositor == cChoice && !s.choiceBranchesDisjoint(t, cache) {
				ok = false
				return
			}
			for _, sub := range t.particles {
				walk(sub)
			}
			delete(seen, t)
		}
	}
	walk(p)
	if !ok {
		return false
	}
	for _, w := range wilds {
		for _, e := range elems {
			for n := range upaMatchNames(s, e, cache) {
				if wildcardMatches(w, n) {
					return false // element-vs-wildcard competition: decline
				}
			}
		}
	}
	return true
}

// choiceBranchesDisjoint reports whether the branches of a choice have
// pairwise-disjoint first sets, so the branch pick is name-deterministic.
func (s *Schema) choiceBranchesDisjoint(mg *modelGroup, cache map[*ElementDecl]map[xname]bool) bool {
	type first struct {
		names map[xname]bool
		wilds []*wildcard
	}
	firsts := make([]first, 0, len(mg.particles))
	for _, br := range mg.particles {
		f := first{names: map[xname]bool{}}
		collectFirst(s, br, &f.names, &f.wilds, cache, map[*modelGroup]bool{})
		firsts = append(firsts, f)
	}
	for i := range firsts {
		for j := i + 1; j < len(firsts); j++ {
			for n := range firsts[i].names {
				if firsts[j].names[n] {
					return false
				}
			}
			if len(firsts[i].wilds) > 0 && len(firsts[j].wilds) > 0 {
				return false // wildcard-vs-wildcard first overlap: stay conservative
			}
			for _, w := range firsts[i].wilds {
				for n := range firsts[j].names {
					if wildcardMatches(w, n) {
						return false
					}
				}
			}
			for _, w := range firsts[j].wilds {
				for n := range firsts[i].names {
					if wildcardMatches(w, n) {
						return false
					}
				}
			}
		}
	}
	return true
}

// collectFirst gathers the names/wildcards an iteration of p can start with.
func collectFirst(s *Schema, p *particle, names *map[xname]bool, wilds *[]*wildcard, cache map[*ElementDecl]map[xname]bool, seen map[*modelGroup]bool) {
	if p == nil || p.max == 0 {
		return
	}
	switch t := p.term.(type) {
	case *ElementDecl:
		for n := range upaMatchNames(s, t, cache) {
			(*names)[n] = true
		}
	case *wildcard:
		*wilds = append(*wilds, t)
	case *modelGroup:
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		if t.compositor == cChoice {
			for _, br := range t.particles {
				collectFirst(s, br, names, wilds, cache, seen)
			}
			return
		}
		for _, sub := range t.particles { // cSeq nullable prefix
			collectFirst(s, sub, names, wilds, cache, seen)
			if !upaNullable(sub) {
				return
			}
		}
	}
}

// abMatch enables the dev-only A/B comparison of the counting matcher
// against the position-set NFA on every small instance (XSD_ABMATCH=1).
var abMatch = os.Getenv("XSD_ABMATCH") != ""

// abCompare cross-checks a decided NFA verdict against countMatch and
// reports divergences on stderr (dev tooling; never changes behavior).
func (s *Schema) abCompare(p *particle, kids []*xmltree.Node, nfa bool) {
	cm, decided := s.countMatch(p, kids)
	if decided && cm != nfa {
		first := ""
		if len(kids) > 0 {
			first = nameOf(kids[0]).String()
		}
		fmt.Fprintf(os.Stderr, "ABDIV kids=%d first=%s nfa=%v count=%v model=%s\n",
			len(kids), first, nfa, cm, particleString(p))
	}
}

// particleString renders a particle tree for A/B diagnostics.
func particleString(p *particle) string {
	if p == nil {
		return "nil"
	}
	occ := fmt.Sprintf("{%d,%d}", p.min, p.max)
	switch t := p.term.(type) {
	case *ElementDecl:
		return t.name.Local + occ
	case *wildcard:
		return "any(" + t.nsMode + ")" + occ
	case *modelGroup:
		kind := "seq"
		switch t.compositor {
		case cChoice:
			kind = "cho"
		case cAll:
			kind = "all"
		}
		out := kind + "("
		for i, sub := range t.particles {
			if i > 0 {
				out += ","
			}
			if len(out) > 300 {
				out += "..."
				break
			}
			out += particleString(sub)
		}
		return out + ")" + occ
	}
	return "?"
}
