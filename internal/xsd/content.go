package xsd

import "github.com/tim-riep/go-xslt/internal/xmltree"

// --- effective (post-derivation) content model and attributes ---------------

func (s *Schema) effectiveParticle(ct *ComplexType) *particle {
	return s.effParticle(ct, map[*ComplexType]bool{})
}

func (s *Schema) effParticle(ct *ComplexType, seen map[*ComplexType]bool) *particle {
	if ct.derivation == "extension" && !seen[ct] {
		seen[ct] = true
		if base, ok := ct.baseType.(*ComplexType); ok {
			bp := s.effParticle(base, seen)
			switch {
			case bp == nil:
				return ct.particle
			case ct.particle == nil:
				return bp
			default:
				// XSD 1.1: extending an xs:all with an xs:all yields one all group
				// holding the members of both, not a sequence of the two groups
				// (which would impose an order between them).
				if s.version == Version11 {
					if bg, eg := allGroupOf(bp), allGroupOf(ct.particle); bg != nil && eg != nil {
						min := bp.min
						if ct.particle.min > min {
							min = ct.particle.min
						}
						merged := append(append([]*particle{}, bg.particles...), eg.particles...)
						return &particle{min: min, max: 1, term: &modelGroup{
							compositor: cAll, particles: merged}}
					}
				}
				return &particle{min: 1, max: 1, term: &modelGroup{
					compositor: cSeq, particles: []*particle{bp, ct.particle}}}
			}
		}
	}
	return ct.particle
}

// allGroupOf returns the xs:all model group a particle carries, if the particle
// is a plain (non-repeating) reference to one.
func allGroupOf(p *particle) *modelGroup {
	if p == nil || p.max != 1 {
		return nil
	}
	if mg, ok := p.term.(*modelGroup); ok && mg.compositor == cAll {
		return mg
	}
	return nil
}

func (s *Schema) effectiveAttrs(ct *ComplexType) ([]*attrUse, *wildcard) {
	return s.effAttrs(ct, map[*ComplexType]bool{})
}

func (s *Schema) effAttrs(ct *ComplexType, seen map[*ComplexType]bool) ([]*attrUse, *wildcard) {
	base, ok := ct.baseType.(*ComplexType)
	if !ok || ct.derivation == "" || seen[ct] {
		return ct.attrUses, ct.attrWildcard
	}
	seen[ct] = true
	bu, bw := s.effAttrs(base, seen)
	merged := mergeAttrs(bu, ct.attrUses)
	wc := ct.attrWildcard
	switch {
	case wc == nil:
		// A RESTRICTION without its own anyAttribute has NO attribute wildcard
		// (§3.4.2 complexContent mapping — sunData combined/008 test.10/11:
		// "the wildcard will accept nothing"); only an extension inherits.
		if ct.derivation == "extension" {
			wc = bw
		}
	case bw != nil && ct.derivation == "extension":
		// Extension: the effective attribute wildcard is the union of the base's
		// and the extension's (Schema Component Constraint: "Attribute Wildcard
		// Union"). A restriction, by contrast, replaces with its own (narrower) one.
		wc = &wildcard{process: wc.process, parts: []*wildcard{wc, bw}}
	}
	return merged, wc
}

// mergeAttrs overlays own uses onto base uses by attribute name; a prohibited use
// removes the inherited one.
func mergeAttrs(base, own []*attrUse) []*attrUse {
	out := make([]*attrUse, 0, len(base)+len(own))
	skip := map[xname]bool{}
	for _, u := range own {
		skip[u.decl.name] = true
	}
	for _, u := range base {
		if !skip[u.decl.name] {
			out = append(out, u)
		}
	}
	for _, u := range own {
		if !u.prohibited {
			out = append(out, u)
		}
	}
	return out
}

// --- grammar matcher: does the child-element sequence satisfy the particle? ---
//
// The matcher is a pure function of (particle, start position) over a fixed
// child list, so results are memoized — this turns the otherwise-exponential
// backtracking over nested/repeating groups into polynomial time. A step budget
// is a final backstop against pathological models.

type matcher struct {
	s         *Schema
	ch        []*xmltree.Node
	memo      map[mkey]map[int]bool
	active    map[mkey]bool // recursion stack, to break cyclic group references
	budget    int
	exhausted bool // budget ran out ⇒ the verdict is undetermined
	// open, when set, is an INTERLEAVE open-content wildcard (1.1): at any
	// position, children it admits may be skipped over (the closure below).
	open *wildcard
	// noOpenAt, when ≥ 0, bars the open wildcard from absorbing that child —
	// the probe for element-beats-wildcard attribution (open025 vs open047).
	noOpenAt int
	// forceOpen/forceModel pin an attribution per child index during the
	// greedy left-to-right assignment (see greedyOpenAttribution).
	forceOpen  map[int]bool
	forceModel map[int]bool
}

// clo returns pos plus every position reachable by skipping open-content
// children; a no-op when no interleave wildcard is active.
func (m *matcher) clo(pos int) map[int]bool {
	out := map[int]bool{pos: true}
	for i := pos; m.open != nil && i < len(m.ch) &&
		i != m.noOpenAt && !m.forceModel[i] && wildcardMatches(m.open, nameOf(m.ch[i])); i++ {
		out[i+1] = true
	}
	return out
}

func (m *matcher) cloSet(ps map[int]bool) map[int]bool {
	if m.open == nil {
		return ps
	}
	out := map[int]bool{}
	for p := range ps {
		for e := range m.clo(p) {
			out[e] = true
		}
	}
	return out
}

type mkey struct {
	p     *particle
	start int
}

// contentMatches reports whether kids satisfy p. ok is false when the matcher's
// step budget was exhausted (a pathological/huge content model), meaning the
// verdict is undetermined rather than "does not match".
// effectiveOpenContent walks the derivation chain for the nearest {open
// content}; an explicit mode "none" shadows any inherited one.
func (s *Schema) effectiveOpenContent(ct *ComplexType) *openContent {
	seen := map[*ComplexType]bool{}
	for cur := ct; cur != nil && !seen[cur]; {
		seen[cur] = true
		if cur.openContent != nil {
			if cur.openContent.mode == "none" {
				// §3.4.2.5.2: on an EXTENSION step mode="none" merely adds
				// nothing of its own — the base's open content survives
				// (open031.v4); on a restriction it shuts open content off.
				if cur.derivation != "extension" {
					return nil
				}
			} else {
				oc := cur.openContent
				if cur.derivation == "extension" {
					// An extension's own open content UNIONS with the base's
					// (open047: the base namespace stays admitted alongside the
					// extension's negated wildcard).
					if b, ok := cur.baseType.(*ComplexType); ok {
						if bEff := s.effectiveOpenContent(b); bEff != nil && bEff.wc != nil && oc.wc != nil {
							return &openContent{mode: oc.mode, wc: &wildcard{
								process: oc.wc.process,
								parts:   []*wildcard{oc.wc, bEff.wc}}}
						}
					}
				}
				return oc
			}
		}
		if cur.derivation == "restriction" {
			// A restriction without its own xs:openContent has NONE — open
			// content is inherited across extension only (open014.n1).
			return nil
		}
		b, ok := cur.baseType.(*ComplexType)
		if !ok {
			return nil
		}
		cur = b
	}
	return nil
}

// contentMatchesOpen is contentMatches with 1.1 open content: interleave lets
// wildcard-admitted children appear anywhere (position closure), suffix only
// after a complete match of the model.
func (s *Schema) contentMatchesOpen(p *particle, kids []*xmltree.Node, oc *openContent) (matched, ok bool) {
	if oc == nil || oc.wc == nil {
		return s.contentMatches(p, kids)
	}
	if len(kids) > 600 {
		return false, false
	}
	m := &matcher{s: s, ch: kids, memo: map[mkey]map[int]bool{}, active: map[mkey]bool{}, budget: 120_000, noOpenAt: -1}
	if oc.mode == "interleave" {
		m.open = oc.wc
	}
	var ends map[int]bool
	if p == nil {
		ends = m.cloSet(map[int]bool{0: true})
	} else {
		ends = m.particle(p, 0)
	}
	if ends[len(kids)] {
		return true, !m.exhausted
	}
	if oc.mode == "suffix" {
		for e := range ends {
			all := true
			for _, kid := range kids[e:] {
				if !wildcardMatches(oc.wc, nameOf(kid)) {
					all = false
					break
				}
			}
			if all {
				return true, !m.exhausted
			}
		}
	}
	return false, !m.exhausted
}

// greedyOpenAttribution decides, LEFT TO RIGHT, which children the model takes
// and which fall to the interleave open wildcard: at each index the model
// consumes the child if a full parse with all earlier pins still exists
// (open025: a repeatable model element takes the repeat; open047: an earlier
// same-named child pinned to the model leaves the later one to the wildcard).
// Returns the open-attributed index set (nil when not applicable).
func (s *Schema) greedyOpenAttribution(p *particle, kids []*xmltree.Node, oc *openContent) map[int]bool {
	if oc == nil || oc.wc == nil || oc.mode != "interleave" || p == nil || len(kids) > 600 {
		return nil
	}
	forceOpen := map[int]bool{}
	forceModel := map[int]bool{}
	try := func() bool {
		m := &matcher{s: s, ch: kids, memo: map[mkey]map[int]bool{}, active: map[mkey]bool{},
			budget: 120_000, noOpenAt: -1, open: oc.wc,
			forceOpen: forceOpen, forceModel: forceModel}
		return m.particle(p, 0)[len(kids)]
	}
	open := map[int]bool{}
	for i := range kids {
		forceModel[i] = true
		if try() {
			continue // the model takes it
		}
		delete(forceModel, i)
		forceOpen[i] = true
		if !try() {
			// Neither pin completes a parse — leave the rest unpinned; the
			// caller's overall match already decided validity.
			delete(forceOpen, i)
			break
		}
		open[i] = true
	}
	return open
}

func (s *Schema) contentMatches(p *particle, kids []*xmltree.Node) (matched, ok bool) {
	if p == nil {
		return len(kids) == 0, true
	}
	// Per-particle matching cost of the position-set NFA scales ~O(P·N²), so
	// very large instances go to the LINEAR counting matcher instead
	// (bigmatch.go) — it decides every name-deterministic model and returns
	// no-verdict for the rest, exactly the old behavior of this cap.
	if len(kids) > 600 {
		return s.countMatch(p, kids)
	}
	m := &matcher{s: s, ch: kids, memo: map[mkey]map[int]bool{}, active: map[mkey]bool{}, budget: 120_000, noOpenAt: -1}
	res := m.particle(p, 0)[len(kids)]
	if abMatch && !m.exhausted {
		s.abCompare(p, kids, res)
	}
	return res, !m.exhausted
}

func (m *matcher) particle(p *particle, start int) map[int]bool {
	k := mkey{p, start}
	if r, ok := m.memo[k]; ok {
		return r
	}
	if m.active[k] {
		// A cyclic (invalid) group reference: treat as no match.
		return map[int]bool{}
	}
	if m.budget <= 0 {
		m.exhausted = true
		return map[int]bool{}
	}
	m.active[k] = true
	m.budget--

	// A nullable term (e.g. an xs:all / group whose members are all optional) can
	// satisfy an occurrence with zero-width, so min required occurrences can all
	// be padded with empty matches. Such a particle therefore behaves like min=0
	// for the purpose of reachable end positions.
	termNull := m.nullable(p.term)
	ends := map[int]bool{}
	if p.min == 0 || termNull {
		for e := range m.clo(start) {
			ends[e] = true
		}
	}
	positions := m.clo(start)
	for reps := 1; ; reps++ {
		if p.max != unbounded && reps > p.max {
			break
		}
		if reps > len(m.ch)+1 {
			break // only advancing matches count, so this bounds repetition
		}
		next := map[int]bool{}
		for pos := range positions {
			for e := range m.termOnce(p.term, pos) {
				if e > pos {
					next[e] = true
				}
			}
		}
		if len(next) == 0 {
			break
		}
		positions = m.cloSet(next)
		if reps >= p.min || termNull {
			for e := range positions {
				ends[e] = true
			}
		}
	}
	delete(m.active, k)
	m.memo[k] = ends
	return ends
}

// nullable reports whether term t can match zero child elements. Element and
// wildcard terms always consume one child; a model group is nullable when its
// structure permits an empty match (a choice with a nullable branch, or a
// sequence/all whose every member is nullable). Cyclic group references are
// treated as non-nullable to break the recursion.
func (m *matcher) nullable(t term) bool {
	mg, ok := t.(*modelGroup)
	if !ok {
		return false
	}
	return m.groupNullable(mg, map[*modelGroup]bool{})
}

func (m *matcher) partNullable(p *particle, seen map[*modelGroup]bool) bool {
	if p.min == 0 {
		return true
	}
	if mg, ok := p.term.(*modelGroup); ok {
		return m.groupNullable(mg, seen)
	}
	return false
}

func (m *matcher) groupNullable(mg *modelGroup, seen map[*modelGroup]bool) bool {
	if seen[mg] {
		return false
	}
	seen[mg] = true
	defer delete(seen, mg)
	if mg.compositor == cChoice {
		for _, sub := range mg.particles {
			if m.partNullable(sub, seen) {
				return true
			}
		}
		// An empty choice matches NOTHING — not even the empty sequence — so a
		// required empty choice is unsatisfiable (saxon complex022).
		return false
	}
	for _, sub := range mg.particles { // sequence / all
		if !m.partNullable(sub, seen) {
			return false
		}
	}
	return true
}

func (m *matcher) termOnce(t term, pos int) map[int]bool {
	switch term := t.(type) {
	case *ElementDecl:
		if pos < len(m.ch) && !m.forceOpen[pos] && m.s.matchesElementName(term, nameOf(m.ch[pos])) {
			return map[int]bool{pos + 1: true}
		}
	case *wildcard:
		if pos < len(m.ch) && !m.forceOpen[pos] && wildcardMatches(term, nameOf(m.ch[pos])) {
			return map[int]bool{pos + 1: true}
		}
	case *modelGroup:
		return m.group(term, pos)
	}
	return map[int]bool{}
}

func (m *matcher) group(mg *modelGroup, pos int) map[int]bool {
	switch mg.compositor {
	case cChoice:
		out := map[int]bool{}
		for _, sub := range mg.particles {
			for e := range m.particle(sub, pos) {
				out[e] = true
			}
		}
		return out
	case cAll:
		return m.all(mg, pos)
	default: // cSeq
		cur := map[int]bool{pos: true}
		for _, sub := range mg.particles {
			next := map[int]bool{}
			for p := range cur {
				for e := range m.particle(sub, p) {
					next[e] = true
				}
			}
			if len(next) == 0 {
				return map[int]bool{}
			}
			cur = next
		}
		return cur
	}
}

// all matches an xs:all group: each member may appear in any order, up to its
// own occurrence limits (1.0 caps every member at 0..1; 1.1 allows any bounds,
// wildcard members, and a group reference to another xs:all). Members are
// distinguished by name, so a greedy assignment — most specific member first —
// is enough.
func (m *matcher) all(mg *modelGroup, pos int) map[int]bool {
	members := allMembers(mg)
	counts := make([]int, len(members))
	p := pos
	for p < len(m.ch) {
		matched := m.allPick(members, counts, nameOf(m.ch[p]))
		if matched < 0 {
			// Interleave open content admits extra children between the all's
			// members (open008: interleave inside xs:all).
			if m.open != nil && wildcardMatches(m.open, nameOf(m.ch[p])) {
				p++
				continue
			}
			break
		}
		counts[matched]++
		p++
	}
	for i, sub := range members {
		if counts[i] < sub.min {
			return map[int]bool{}
		}
	}
	return map[int]bool{p: true}
}

// allPick returns the index of the member that should consume a child named nn,
// or -1. Element members are preferred over wildcards, so a wildcard alongside
// named members only picks up what the names do not cover.
func (m *matcher) allPick(members []*particle, counts []int, nn xname) int {
	room := func(i int) bool {
		max := members[i].max
		return max == unbounded || counts[i] < max
	}
	for i, sub := range members {
		ed, ok := sub.term.(*ElementDecl)
		if ok && room(i) && m.s.matchesElementName(ed, nn) {
			return i
		}
	}
	for i, sub := range members {
		w, ok := sub.term.(*wildcard)
		if ok && room(i) && wildcardMatches(w, nn) {
			return i
		}
	}
	return -1
}

// allMembers flattens the member particles of an xs:all: a 1..1 reference to a
// named group holding another xs:all contributes that group's members directly
// (XSD 1.1 allows such a reference; the wrapper particle carries no occurrence
// of its own).
func allMembers(mg *modelGroup) []*particle {
	return allMembersSeen(mg, map[*modelGroup]bool{})
}

func allMembersSeen(mg *modelGroup, seen map[*modelGroup]bool) []*particle {
	if seen[mg] {
		return nil // cyclic group reference: stop rather than recurse forever
	}
	seen[mg] = true
	defer delete(seen, mg)
	var out []*particle
	for _, sub := range mg.particles {
		if inner, ok := sub.term.(*modelGroup); ok && inner.compositor == cAll && sub.min == 1 && sub.max == 1 {
			out = append(out, allMembersSeen(inner, seen)...)
			continue
		}
		out = append(out, sub)
	}
	return out
}

// matchesElementName reports whether an instance element named nn is governed by
// element declaration decl — directly or via decl's substitution group.
func (s *Schema) matchesElementName(decl *ElementDecl, nn xname) bool {
	if nn == decl.name && !decl.abstract {
		return true
	}
	// Substitution groups expand only from the GLOBAL head declaration — a
	// local element that happens to share a head's name has no members
	// (elemZ021b/f, elemZ023; mirrors the upaMatchNames guard).
	if s.elements[decl.name] != decl {
		return false
	}
	seen := map[xname]bool{}
	var walk func(h xname) bool
	walk = func(h xname) bool {
		for _, m := range s.substMembers[h] {
			if seen[m.name] {
				continue
			}
			seen[m.name] = true
			if m.name == nn && !m.abstract && s.substitutionAllowed(decl, m) {
				return true
			}
			if walk(m.name) {
				return true
			}
		}
		return false
	}
	return walk(decl.name)
}

// substitutionAllowed reports whether m may stand in for head in a content
// model. The head's {disallowed substitutions} — its own @block plus the
// @block of its declared type — bars substitution outright when it contains
// "substitution", and otherwise bars any member whose type reaches the head's
// type through a blocked derivation method.
func (s *Schema) substitutionAllowed(head, m *ElementDecl) bool {
	blocked := head.blocked
	if ht, ok := head.typ.(*ComplexType); ok && len(ht.block) > 0 {
		merged := make(map[string]bool, len(blocked)+len(ht.block))
		for k := range blocked {
			merged[k] = true
		}
		for k := range ht.block {
			merged[k] = true
		}
		blocked = merged
	}
	if len(blocked) == 0 {
		return true
	}
	if blocked["substitution"] {
		return false
	}
	if head.typ == nil || m.typ == nil || m.typ == head.typ {
		return true
	}
	for meth := range derivationMethodsTo(m.typ, head.typ) {
		if blocked[meth] {
			return false
		}
	}
	return true
}

func wildcardMatches(w *wildcard, nn xname) bool {
	if len(w.parts) > 0 {
		// A union admits a name unless every operand excludes it. ##defined
		// survives the union only when every operand carries it.
		allDefined := true
		for _, p := range w.parts {
			if !p.notDefined {
				allDefined = false
				break
			}
		}
		for _, p := range w.parts {
			if wildcardAdmits(p, nn, allDefined) {
				return true
			}
		}
		return false
	}
	return wildcardAdmits(w, nn, true)
}

func wildcardAdmits(w *wildcard, nn xname, useDefined bool) bool {
	if len(w.parts) > 0 {
		return wildcardMatches(w, nn)
	}
	ns := nn.Space
	// XSD 1.1 negations narrow whatever the positive constraint admits.
	for _, x := range w.notNS {
		if x == ns {
			return false
		}
	}
	for _, x := range w.notNames {
		if x == nn {
			return false
		}
	}
	if useDefined {
		for _, x := range w.notDefNames {
			if x == nn {
				return false
			}
		}
	}
	switch w.nsMode {
	case "any":
		return true
	case "other":
		return ns != w.targetNS && ns != ""
	case "list":
		for _, a := range w.namespaces {
			if a == ns {
				return true
			}
		}
	}
	return false
}

// --- name → declaration binding (for recursive child type validation) -------

// contentDecls collects, from a particle tree, a map of element name → decl.
// Valid (UPA-conforming) content models bind each name to one decl.
func (s *Schema) contentDecls(p *particle, out map[xname]*ElementDecl) {
	if p == nil {
		return
	}
	switch t := p.term.(type) {
	case *ElementDecl:
		out[t.name] = t
	case *modelGroup:
		for _, sub := range t.particles {
			s.contentDecls(sub, out)
		}
	}
}

func nameOf(n *xmltree.Node) xname { return xname{n.Name.Space, n.Name.Local} }
