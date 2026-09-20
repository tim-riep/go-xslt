package xslt

import (
	"fmt"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// Bounded-memory accumulator evaluation over a streamed document.
//
// acc2ensure's own walk (instr_accumulator.go) is already in the right ORDER —
// one recursive document-order pass, start-phase rules pre-order, end-phase
// rules post-order — but at the wrong TIME: it runs when a value is first
// asked for, over a tree that by then holds only the record currently attached.
// What this file does is run that same rule application AS the reader advances,
// so each node's before/after value is computed at the one moment the node is
// live, and cached only for as long as the node is.
//
// WHY THIS IS BOUNDED. Two things could have grown with the document and do
// not. (1) The value cache: an entry is written when a node is entered/left and
// dropped when the node is, so what is held is the ancestor spine plus the
// record in hand — the same working set the executor already keeps. (2) The
// traversal: a streamed run normally SKIPS every subtree off the record path
// without building it, which an accumulator cannot allow (it must see every
// node). With accumulators live the driver walks those subtrees instead of
// skipping them, but still drops each node the instant it completes, so the
// retained set is O(depth), not O(subtree).
//
// WHY THE END-PHASE RULE MAY READ NOTHING BUT THE NODE ITSELF. Dropping a
// child as it completes would break an end-phase rule that read the node's
// content — but XSLT 3.0 §18.2.8 requires every rule of a streamable
// accumulator (both phases) to be grounded and MOTIONLESS, so no conforming
// streamable rule can. A NON-streamable accumulator is still refused outright
// (acc2ensure), exactly as before.
//
// All private names here are prefixed "strbAcc".

// strbAccSet is the live accumulator state of one streamed source-document:
// one running value per accumulator the body may read.
type strbAccSet struct {
	defs []*acc2Def
	cur  []xpath.Object
	eng  *engine
	// keys records, per live node, the cache entries written for it, so
	// dropping the node drops its values too.
	keys map[*xmltree.Node][]string
}

// strbAccStart evaluates every accumulator's initial value and marks each one
// as already-evaluated for this root, so acc2ensure hands lookups straight to
// the cache this file maintains instead of trying to walk the tree.
func strbAccStart(eng *engine, defs []*acc2Def, root *xmltree.Node) (*strbAccSet, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	if eng.accCache == nil {
		eng.accCache = map[string]any{}
	}
	s := &strbAccSet{defs: defs, cur: make([]xpath.Object, len(defs)), eng: eng, keys: map[*xmltree.Node][]string{}}
	for i, def := range defs {
		v, err := eng.eval(def.initial, def.el, rt{node: root, pos: 1, size: 1})
		if err != nil {
			return nil, err
		}
		if v, err = def.coerce(v); err != nil {
			return nil, err
		}
		s.cur[i] = v
		eng.accCache[fmt.Sprintf("acc2-done|%s|%p", def.clarkN, root)] = true
	}
	// The document node itself is entered before any token is read, so its
	// before-value is the initial value unless a rule matches the document node
	// itself. Its after-value is written at end of stream.
	if err := s.enter(root); err != nil {
		return nil, err
	}
	return s, nil
}

// enter applies the start-phase rules for a node and records its before-value.
func (s *strbAccSet) enter(n *xmltree.Node) error {
	for i, def := range s.defs {
		v, err := s.eng.acc2applyFirst(def, n, s.cur[i], false)
		if err != nil {
			return err
		}
		s.cur[i] = v
		s.put(def, n, false, v)
	}
	return nil
}

// exit applies the end-phase rules for a node and records its after-value.
func (s *strbAccSet) exit(n *xmltree.Node) error {
	for i, def := range s.defs {
		v, err := s.eng.acc2applyFirst(def, n, s.cur[i], true)
		if err != nil {
			return err
		}
		s.cur[i] = v
		s.put(def, n, true, v)
	}
	return nil
}

func (s *strbAccSet) put(def *acc2Def, n *xmltree.Node, after bool, v xpath.Object) {
	k := acc2cacheKey(def.clarkN, n, after)
	s.eng.accCache[k] = v
	s.keys[n] = append(s.keys[n], k)
}

// forget releases the cached values of a node and its whole subtree. It is
// called at the moment the driver drops the node, which is what keeps the cache
// the size of the live working set rather than the size of the document.
func (s *strbAccSet) forget(n *xmltree.Node) {
	if s == nil || n == nil {
		return
	}
	if ks, ok := s.keys[n]; ok {
		for _, k := range ks {
			delete(s.eng.accCache, k)
		}
		delete(s.keys, n)
	}
	for _, c := range n.Children {
		s.forget(c)
	}
}

// strbAccNeeded collects the accumulators a streamed body may read, given the
// clark names its expressions mention. An accumulator that is not declared
// streamable is rejected here rather than at the point of use: XSLT 3.0 §18.2
// makes reading one over a streamed document XTDE3362, and this executor would
// otherwise have to evaluate it to find that out.
func strbAccNeeded(eng *engine, names map[string]bool) ([]*acc2Def, bool) {
	if len(names) == 0 {
		return nil, true
	}
	out := make([]*acc2Def, 0, len(names))
	for n := range names {
		def := eng.acc2find(n)
		if def == nil || !def.streamable {
			return nil, false
		}
		out = append(out, def)
	}
	// Stable order so two runs of the same stylesheet apply the rules of
	// several accumulators in the same sequence. They are independent of one
	// another, so the order does not affect the values — but a deterministic
	// one keeps any error they raise deterministic too.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].clarkN < out[j-1].clarkN; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, true
}
