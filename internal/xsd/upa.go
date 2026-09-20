package xsd

// Unique Particle Attribution (UPA): a content model must be deterministic — at
// any point, an element must be attributable to a single particle without
// lookahead. This is checked with a Glushkov position automaton: each element/
// wildcard particle is a "position"; the model is UPA-valid iff no reachable
// decision set (the model's first set, or any position's follow set) contains two
// distinct element positions with the same expanded name.
//
// The check is deliberately conservative: it flags collisions between element
// positions whose MATCH NAME SETS overlap (the declaration's own name plus its
// transitive substitution-group members, honoring abstract and blocking) and,
// in 1.0, overlapping wildcard pairs — element-vs-wildcard overlap stays
// unchecked, so it never rejects the schemas the suite holds valid there.

func (c *compiler) checkAllUPA() error {
	for _, ct := range c.allCTs { // named AND anonymous complex types
		if err := checkUPA(c.sch, c.sch.effectiveParticle(ct)); err != nil {
			return err
		}
	}
	return nil
}

func checkUPA(sch *Schema, root *particle) error {
	if root == nil {
		return nil
	}
	root = upaUnfold(root)
	follow := map[*particle][]*particle{}
	upaFollow(root, follow, map[*modelGroup]bool{})

	var first []*particle
	upaFirst(root, &first, map[*modelGroup]bool{})
	names := map[*ElementDecl]map[xname]bool{}
	if err := upaNoCollision(sch, first, names); err != nil {
		return err
	}
	for _, set := range follow {
		if err := upaNoCollision(sch, set, names); err != nil {
			return err
		}
	}
	return nil
}

// upaUnfold returns a structurally identical copy of root in which every
// particle and model group is a FRESH object, so that one named model group
// reached through two different xs:group references contributes two DISTINCT
// sets of Glushkov positions.
//
// The component model deliberately shares a single *modelGroup between every
// reference to the same named group (structure.go's "group" case returns
// &particle{term: g} for the one compiled g). That sharing is right for
// validation, but wrong for a position automaton keyed by particle pointer:
// two occurrences of the group collapse into ONE position whose follow set is
// the UNION of both occurrences' successors, manufacturing collisions the
// unfolded model does not have. The W3C's own XHTML 1.0 schema is the standard
// victim — its `head` model references head.misc five times around two
// mutually exclusive branches, one starting with `title` and one with `base`,
// and without unfolding the shared head.misc position is followed by BOTH
// `base` positions at once, so a perfectly deterministic (and normative)
// content model is rejected for element `base`.
//
// Only the particle/group spine is duplicated: element and wildcard TERMS stay
// shared, since name matching compares declarations, not positions.
//
// A cyclic group reference (which XSD forbids, but which the compiler may
// still have built before the cycle check runs) keeps its cycle rather than
// unfolding forever: a group already being expanded on this path is reused,
// exactly reproducing the pre-existing behaviour, and every traversal below
// carries its own *modelGroup cycle guard. A pathological schema whose group
// references nest deeply enough to unfold exponentially falls back to the
// shared tree once the node budget is spent — the same conservative answer the
// check gave before this function existed.
func upaUnfold(root *particle) *particle {
	budget := 1 << 17
	out := upaUnfoldIn(root, map[*modelGroup]*particle{}, &budget)
	if budget <= 0 {
		return root // budget exhausted: check the shared tree, as before
	}
	return out
}

func upaUnfoldIn(p *particle, active map[*modelGroup]*particle, budget *int) *particle {
	if p == nil {
		return nil
	}
	if *budget <= 0 {
		return p
	}
	*budget--
	np := &particle{min: p.min, max: p.max, term: p.term}
	mg, ok := p.term.(*modelGroup)
	if !ok {
		return np
	}
	if prev, on := active[mg]; on {
		return prev // cyclic group reference: keep the cycle
	}
	active[mg] = np
	defer delete(active, mg)
	nmg := &modelGroup{compositor: mg.compositor, particles: make([]*particle, 0, len(mg.particles))}
	np.term = nmg
	for _, sub := range mg.particles {
		nmg.particles = append(nmg.particles, upaUnfoldIn(sub, active, budget))
	}
	return np
}

// upaMatchNames returns the set of element names position decl can match: its
// own name (unless abstract) plus every non-abstract transitive substitution-
// group member it permits. Expansion applies only to the GLOBAL declaration a
// head actually is — a local element sharing a head's name is not a head.
func upaMatchNames(sch *Schema, decl *ElementDecl, cache map[*ElementDecl]map[xname]bool) map[xname]bool {
	if m, ok := cache[decl]; ok {
		return m
	}
	names := map[xname]bool{}
	cache[decl] = names
	if !decl.abstract {
		names[decl.name] = true
	}
	if sch == nil || sch.elements[decl.name] != decl {
		return names
	}
	seen := map[xname]bool{}
	var walk func(h xname)
	walk = func(h xname) {
		for _, m := range sch.substMembers[h] {
			if seen[m.name] {
				continue
			}
			seen[m.name] = true
			// 1.1 counts ABSTRACT members for schema-time UPA too (bug 4337,
			// wg upa.xsd); 1.0 keeps the M25 exclusion (elemZ028 family).
			if (!m.abstract || sch.version == Version11) && sch.substitutionAllowed(decl, m) {
				names[m.name] = true
			}
			walk(m.name)
		}
	}
	walk(decl.name)
	return names
}

// upaNoCollision fails if two distinct positions in set can match the same
// element: two element positions whose match-name sets intersect (substitution
// groups expanded), or two wildcards whose namespace constraints overlap.
func upaNoCollision(sch *Schema, set []*particle, cache map[*ElementDecl]map[xname]bool) error {
	byName := map[xname]*particle{}
	var wilds []*particle
	for _, pos := range set {
		switch t := pos.term.(type) {
		case *ElementDecl:
			for n := range upaMatchNames(sch, t, cache) {
				if prev, exists := byName[n]; exists && prev != pos {
					return invalidf("", "content model is not deterministic (UPA): element %s", n)
				}
				byName[n] = pos
			}
		case *wildcard:
			// Wildcard-vs-wildcard competition violates UPA in BOTH versions:
			// XSD 1.1 §3.8.6.4 only made wildcards weak against ELEMENT
			// declarations (wildI009/010/013/014, all243, all305).
			for _, other := range wilds {
				if other == pos {
					continue
				}
				if wildcardsOverlap(other.term.(*wildcard), t) {
					return invalidf("", "content model is not deterministic (UPA): overlapping wildcards")
				}
			}
			if sch.version == Version10 {
				// Element-vs-wildcard overlap (cos-nonambig, 1.0 only — in 1.1
				// the element declaration wins the competition): a wildcard
				// admitting a sibling element position's name competes with it.
				// Match names include substitution members. Safe only now that
				// maxOccurs=0 particles are pruned (particlesJd005/Jf005).
				for _, epos := range set {
					ed, isEl := epos.term.(*ElementDecl)
					if !isEl || epos == pos {
						continue
					}
					for n := range upaMatchNames(sch, ed, cache) {
						if wildcardMatches(t, n) {
							return invalidf("", "content model is not deterministic (UPA): element %s vs wildcard", n)
						}
					}
				}
			}
			wilds = append(wilds, pos)
		}
	}
	return nil
}

// wildcardsOverlap reports whether two wildcards can admit a common name. It is
// deliberately conservative: only the namespace constraint is considered, and a
// union wildcard (whose parts would need pairwise intersection) counts as
// overlapping nothing.
// anyNSOutside reports whether any namespace in list survives the exclusions.
func anyNSOutside(list, excl []string) bool {
	for _, ns := range list {
		if !containsStr(excl, ns) {
			return true
		}
	}
	return false
}

func wildcardsOverlap(a, b *wildcard) bool {
	if len(a.parts) > 0 || len(b.parts) > 0 {
		return false
	}
	if a.nsMode == "any" || b.nsMode == "any" {
		// Refinement for 1.1 @notNamespace: an ##any wildcard excluding a finite
		// namespace list still shares infinitely many namespaces with anything
		// except another finite list — only there must a listed namespace
		// survive the exclusions.
		if a.nsMode == "any" && b.nsMode == "list" {
			return anyNSOutside(b.namespaces, a.notNS)
		}
		if b.nsMode == "any" && a.nsMode == "list" {
			return anyNSOutside(a.namespaces, b.notNS)
		}
		return true
	}
	switch {
	case a.nsMode == "list" && b.nsMode == "list":
		for _, ns := range a.namespaces {
			if containsStr(b.namespaces, ns) {
				return true
			}
		}
		return false
	case a.nsMode == "other" && b.nsMode == "other":
		return true
	case a.nsMode == "other":
		for _, ns := range b.namespaces {
			if ns != a.targetNS && ns != "" {
				return true
			}
		}
		return false
	default: // b is "other", a is "list"
		for _, ns := range a.namespaces {
			if ns != b.targetNS && ns != "" {
				return true
			}
		}
		return false
	}
}

func upaNullable(p *particle) bool { return upaNullableSeen(p, map[*modelGroup]bool{}) }

func upaNullableSeen(p *particle, seen map[*modelGroup]bool) bool {
	if p == nil || p.min == 0 {
		return true
	}
	switch t := p.term.(type) {
	case *ElementDecl, *wildcard:
		return false
	case *modelGroup:
		if seen[t] {
			return false // a cyclic model group is not finitely nullable; break the recursion
		}
		seen[t] = true
		defer delete(seen, t)
		switch t.compositor {
		case cChoice:
			for _, sub := range t.particles {
				if upaNullableSeen(sub, seen) {
					return true
				}
			}
			return false // an empty choice matches nothing, not even ε
		default: // sequence, all
			for _, sub := range t.particles {
				if !upaNullableSeen(sub, seen) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func upaFirst(p *particle, out *[]*particle, seen map[*modelGroup]bool) {
	if p == nil || p.max == 0 {
		return // a maxOccurs=0 particle matches nothing (particlesJd005/Jf005)
	}
	switch t := p.term.(type) {
	case *ElementDecl, *wildcard:
		*out = append(*out, p)
	case *modelGroup:
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		if t.compositor == cSeq {
			for _, sub := range t.particles {
				upaFirst(sub, out, seen)
				if !upaNullable(sub) {
					break
				}
			}
		} else { // choice, all
			for _, sub := range t.particles {
				upaFirst(sub, out, seen)
			}
		}
	}
}

func upaLast(p *particle, out *[]*particle, seen map[*modelGroup]bool) {
	if p == nil || p.max == 0 {
		return // a maxOccurs=0 particle matches nothing
	}
	switch t := p.term.(type) {
	case *ElementDecl, *wildcard:
		*out = append(*out, p)
	case *modelGroup:
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		if t.compositor == cSeq {
			for i := len(t.particles) - 1; i >= 0; i-- {
				upaLast(t.particles[i], out, seen)
				if !upaNullable(t.particles[i]) {
					break
				}
			}
		} else {
			for _, sub := range t.particles {
				upaLast(sub, out, seen)
			}
		}
	}
}

func upaFollow(p *particle, follow map[*particle][]*particle, seen map[*modelGroup]bool) {
	if p != nil && p.max == 0 {
		return // a maxOccurs=0 particle contributes no positions
	}
	if p == nil {
		return
	}
	if t, ok := p.term.(*modelGroup); ok {
		if seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		switch t.compositor {
		case cSeq:
			for i := range t.particles {
				var lastI []*particle
				upaLast(t.particles[i], &lastI, map[*modelGroup]bool{})
				var firstNext []*particle
				for j := i + 1; j < len(t.particles); j++ {
					upaFirst(t.particles[j], &firstNext, map[*modelGroup]bool{})
					if !upaNullable(t.particles[j]) {
						break
					}
				}
				for _, l := range lastI {
					follow[l] = append(follow[l], firstNext...)
				}
			}
		case cAll:
			// In an all-group any member may follow any other.
			for i := range t.particles {
				var lastI []*particle
				upaLast(t.particles[i], &lastI, map[*modelGroup]bool{})
				for j := range t.particles {
					if i == j {
						continue
					}
					var firstJ []*particle
					upaFirst(t.particles[j], &firstJ, map[*modelGroup]bool{})
					for _, l := range lastI {
						follow[l] = append(follow[l], firstJ...)
					}
				}
			}
		}
		for _, sub := range t.particles {
			upaFollow(sub, follow, seen)
		}
	}
	// A repeatable particle can loop: its last positions are followed by its
	// first. A counted particle with min == max never offers a loop-vs-exit
	// CHOICE (unrolled it is deterministic: loop until min, then exit), and the
	// cross-iteration first-set edges are already carried by every edge entering
	// the particle — so adding the loop edge there only manufactures collisions
	// between loop and exit successors that the unrolled model does not have
	// (mgZ005: b{2,2} followed by b{1,1} is valid).
	if p.max == unbounded || (p.max > 1 && p.min != p.max) {
		var fp, lp []*particle
		upaFirst(p, &fp, map[*modelGroup]bool{})
		upaLast(p, &lp, map[*modelGroup]bool{})
		for _, l := range lp {
			follow[l] = append(follow[l], fp...)
		}
	}
}
