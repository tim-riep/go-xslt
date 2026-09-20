package xslt

import (
	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Bounded-memory xsl:for-each-group, for the two grouping modes XSLT 3.0
// §19.8.4.19 allows a streaming processor to support: group-starting-with and
// group-ending-with. They are the streamable pair because a group's boundary is
// decided by the CURRENT item alone — group-by and group-adjacent both need
// keys compared against items that have not arrived yet (group-by can put the
// first and last item of a document in one group), so neither can close a group
// before the end of the stream and neither is streamable at all.
//
// Memory is O(largest group) rather than O(document): a group is buffered while
// it is open — the spec's own model, since current-group() hands the body the
// whole group — and released as soon as its body has run. Each record is still
// detached from its parent the moment it completes (strbRun.drive's Drop), so
// the retained set is exactly the open group, never the document.
//
// All private names here are prefixed "strbGrp".

// strbGrpState is the live state of one streamed xsl:for-each-group.
type strbGrpState struct {
	feg *fegInstr
	// open is the group being accumulated. Its nodes are detached from the
	// streamed tree but held alive here until the group closes.
	open []*xmltree.Node
	idx  int // groups closed so far; the running position() of the next one
	// release, when set, lets go of a member's cached accumulator values once
	// the group that held it has run. The driver cannot do it at the usual
	// point — the record is dropped from the tree while the group still hands
	// it to the body through current-group().
	release func(*xmltree.Node)
}

func strbNewGrpState(feg *fegInstr) *strbGrpState {
	return &strbGrpState{feg: feg}
}

// step feeds one record to the grouping. It closes (and runs) the previous
// group first for group-starting-with, or the current one afterwards for
// group-ending-with, which is exactly fegFormStartingWith/fegFormEndingWith's
// logic re-expressed one item at a time.
func (s *strbGrpState) step(eng *engine, rec, out *xmltree.Node) error {
	match, err := s.feg.fegMatches(eng, rec)
	if err != nil {
		return err
	}
	if s.feg.mode == fegGroupStartingWith {
		if match && len(s.open) > 0 {
			if err := s.close(eng, out); err != nil {
				return err
			}
		}
		s.open = append(s.open, rec)
		return nil
	}
	s.open = append(s.open, rec)
	if match {
		return s.close(eng, out)
	}
	return nil
}

// complete runs the group still open when the stream ends. A
// group-starting-with grouping always has one; a group-ending-with grouping has
// one only when the last record did not close it.
func (s *strbGrpState) complete(eng *engine, out *xmltree.Node) error {
	if len(s.open) == 0 {
		return nil
	}
	return s.close(eng, out)
}

// close runs the body once for the open group and then releases it.
func (s *strbGrpState) close(eng *engine, out *xmltree.Node) error {
	group := s.open
	s.open = nil
	s.idx++
	prevGroup, prevOK, prevKey, prevKeyOK := eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK
	eng.curGroup = xpath.NodeSet(group)
	eng.curGroupOK = true
	// group-starting-with / group-ending-with form no grouping key at all, so
	// current-grouping-key() stays absent (XSLT 3.0 §14.3) — the same thing
	// fegGroup.hasKey==false expresses on the buffered path.
	eng.curKey, eng.curKeyOK = nil, false
	// size is the number of groups closed SO FAR, never the real total: how
	// many groups a forward-only stream still holds is unknowable. It is
	// unobservable because strbExprSafe rejects last().
	err := eng.forEachIteration(s.feg.body, rt{node: group[0], pos: s.idx, size: s.idx}, out)
	eng.curGroup, eng.curGroupOK, eng.curKey, eng.curKeyOK = prevGroup, prevOK, prevKey, prevKeyOK
	if s.release != nil {
		for _, n := range group {
			s.release(n)
		}
	}
	return err
}

// strbGrpStreamable reports whether a compiled xsl:for-each-group is one this
// executor can drive from a stream, and returns the raw source of the grouping
// pattern so the caller can scan it the same way it scans every other
// expression the streamed dispatch will evaluate.
func strbGrpStreamable(feg *fegInstr) (patSrc string, ok bool) {
	switch feg.mode {
	case fegGroupStartingWith, fegGroupEndingWith:
	default:
		// group-by / group-adjacent cannot close a group before the end of the
		// stream (see the file comment).
		return "", false
	}
	if len(feg.sorts) > 0 {
		// xsl:sort orders the GROUPS, which means holding every one of them.
		return "", false
	}
	if feg.pat == nil {
		return "", false
	}
	return feg.pat.Src(), true
}
