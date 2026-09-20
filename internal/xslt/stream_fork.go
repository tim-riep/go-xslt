package xslt

import (
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// Bounded-memory xsl:fork.
//
// xsl:fork exists so that several results can be computed in ONE pass over the
// input (XSLT 3.0 §16): conceptually each branch gets its own pass, and the
// fork's result is the branches' results CONCATENATED — branch 1 entire, then
// branch 2 entire. A buffered processor gets that for free by running the
// branches one after another, which is what forkInstr.exec does.
//
// A streamed run cannot: there is only one pass, and rewinding it is exactly
// what streaming forbids. What it can do instead is feed every branch the SAME
// materialized record before moving to the next one — the branches then run
// interleaved, so each branch's output has to be collected into its own buffer
// and the buffers concatenated when the stream ends. That reverses the buffered
// processor's trade: memory now grows with the RESULT (which a fork's branches
// are written to keep small — a count, a sum, a filtered subset) instead of
// with the INPUT.
//
// DELIBERATELY NARROW. A branch must be an xsl:sequence whose body is exactly
// one consuming instruction, and every branch must select records with the same
// path. Anything else — a literal element wrapping the consumer inside a
// branch, two branches walking different paths — falls back to full
// materialization: emitting a wrapper once while its content arrives record by
// record is a different construction problem, and two paths would need two
// independent readers over one stream.
//
// All private names here are prefixed "strbFork".

// strbForkInner returns the single consuming instruction of one xsl:fork
// branch, or nil when the branch is not a shape this executor can drive.
func strbForkInner(branch instruction) instruction {
	seq, ok := branch.(*sequenceInstr)
	if !ok || seq.sel != nil {
		// xsl:sequence/@select yields a value with no record dispatch in it;
		// there is nothing for the driver to feed.
		return nil
	}
	in, ok := strbOnlyItem(seq.body)
	if !ok {
		return nil
	}
	switch in.(type) {
	case *forEach, *applyTemplates, *copyOf, *itrIterate, *fegInstr:
		return in
	}
	return nil
}

// strbForkBranches returns the fork's per-branch consumers, in branch order,
// once every one of them has been shown to select records by the same path as
// the plan.
func strbForkBranches(c *forkInstr, path []strbStep, pred string) ([]instruction, bool) {
	var out []instruction
	add := func(in instruction) bool {
		if in == nil {
			return false
		}
		sel := strbConsumerSelect(in)
		if sel == nil {
			return false
		}
		// Comparing the compiled steps rather than the source text lets two
		// branches spell the same path differently ("/a/b" and "a/b"), and
		// stops two different paths that happen to share a spelling prefix
		// from being taken for one. The record FILTER has to match as well:
		// one reader feeds every branch, so they must all want the same
		// records.
		got, gotPred, ok := strbParseRecordSel(sel.Text(), strbConsumerEl(in))
		if !ok || !strbSamePath(got, path) || strings.TrimSpace(gotPred) != strings.TrimSpace(pred) {
			return false
		}
		out = append(out, in)
		return true
	}
	if c.group != nil {
		if !add(c.group) {
			return nil, false
		}
		return out, true
	}
	if len(c.branches) == 0 {
		return nil, false
	}
	for _, b := range c.branches {
		if !add(strbForkInner(b)) {
			return nil, false
		}
	}
	return out, true
}

func strbSamePath(a, b []strbStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// forkGrounded scans every branch of a fork exactly as the single-consumer case
// is scanned.
func (sc *strbScan) forkGrounded(c *forkInstr) bool {
	if c.group != nil {
		return sc.consumerGrounded(c.group)
	}
	if len(c.branches) == 0 {
		return false
	}
	for _, b := range c.branches {
		in := strbForkInner(b)
		if in == nil || !sc.consumerGrounded(in) {
			return false
		}
	}
	return true
}

// strbForkState holds one output buffer per branch, plus each branch's own
// per-record state (xsl:iterate's parameters, an open group).
type strbForkState struct {
	inner []instruction
	bufs  []*xmltree.Node
	iters []*strbIterState
	grps  []*strbGrpState
	// done marks a branch that has stopped early (xsl:break). Only that branch
	// stops: in the buffered model each branch walks the input independently,
	// so one breaking says nothing about the others, and its own
	// xsl:on-completion is skipped exactly as a buffered xsl:break skips it.
	done []bool
}

func strbNewForkState(eng *engine, inner []instruction) (*strbForkState, error) {
	s := &strbForkState{
		inner: inner,
		bufs:  make([]*xmltree.Node, len(inner)),
		iters: make([]*strbIterState, len(inner)),
		grps:  make([]*strbGrpState, len(inner)),
		done:  make([]bool, len(inner)),
	}
	for i, in := range inner {
		s.bufs[i] = &xmltree.Node{Kind: xmltree.KindDocument, Ephemeral: true}
		switch c := in.(type) {
		case *itrIterate:
			st, err := strbNewIterState(eng, c)
			if err != nil {
				return nil, err
			}
			s.iters[i] = st
		case *fegInstr:
			s.grps[i] = strbNewGrpState(c)
		}
	}
	return s, nil
}

// strbAppendFrag moves a branch buffer's content into the real output,
// reproducing the adjacent-text merge that writing straight to out would have
// done (appendLiteralText's rule): without it, two branches whose results abut
// as text would serialize the same but compare as two nodes instead of one.
func strbAppendFrag(out, frag *xmltree.Node) {
	for _, a := range frag.Attrs {
		appendAttrItem(out, a.Name, a.Value)
	}
	out.NS = append(out.NS, frag.NS...)
	for _, ch := range frag.Children {
		if ch.Kind == xmltree.KindText && !ch.Atomic && !out.NoAtomicMerge {
			if n := len(out.Children); n > 0 {
				if last := out.Children[n-1]; last.Kind == xmltree.KindText && !last.Atomic && last.Raw == ch.Raw {
					last.Value += ch.Value
					continue
				}
			}
		}
		out.Append(ch)
	}
}
