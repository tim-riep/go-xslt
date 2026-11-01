package xpath

import (
	"hash/maphash"
	"math/bits"
	"slices"
	"sync"
	"sync/atomic"
)

// Map is an XPath 3.1 map: an ordered set of key/value entries. Keys are
// atomic items compared by the op:same-key rule (a value-and-category
// canonical form, mapKey). Values are arbitrary Objects (sequences).
//
// A map is a persistent hash trie, so map:put (With) shares everything but
// the path to the changed entry with the map it was made from: accumulating
// n entries one map:put at a time (xsl:iterate, fold-left) used to copy the
// whole map every time, n²/2 entry copies in all.
//
// Entries keep their insertion order: order lists every key once, where it
// was first put. Replacing an entry's value keeps its place (and replaces
// its key item, as before); there is no removal in place — map:remove
// builds a new map.
//
// Put changes the map itself, as a builder does before the map is handed
// out. It may change trie nodes this map made in place, until the map is
// frozen: With freezes its source, whose nodes are shared from then on, so a
// Put on that source later copies the path instead. A frozen map's nodes are
// never changed again, which is what lets maps be read from any goroutine.
type Map struct {
	root   *hnode
	order  []mapSlot // the keys in insertion order (len = number of entries)
	log    *mapLog   // claims on order's backing array, shared with derived maps
	frozen atomic.Bool

	// A summary of the entries for map(K, V) tests (matchesItemType): when
	// keysOne, every key is of the atomic type kt; when valsOne, every value
	// is a single atomic item of type vt. Put keeps it sound (an entry that
	// does not fit clears the flag for good); a cleared flag only means the
	// test looks at every entry.
	kt, vt           AtomType
	keysOne, valsOne bool
}

// mapSlot is one key in a map's insertion order: its canonical form and
// that form's hash.
type mapSlot struct {
	h uint64
	k string
}

// mapLog guards the append-only use of a backing array that several maps'
// order slices share: a map may append in place only while no other map has
// appended past its end (n is the length claimed so far). Elements below a
// claim are never written again, so readers need no lock.
type mapLog struct {
	mu sync.Mutex
	n  int
}

// hentry is one entry. It is never changed after it is made.
type hentry struct {
	h   uint64
	k   string // mapKey(key)
	key Item
	val Object
}

// hnode is a trie node: a 32-way bitmap-compressed branch over five bits of
// the hash per level, or — when two different keys have the same 64-bit
// hash — a collision list (coll non-nil). owner is the map that made the
// node and may still change it in place (see Map).
type hnode struct {
	owner  *Map
	bitmap uint32
	kids   []hkid
	coll   []*hentry
}

// hkid is a branch slot: an entry, or a subtree (sub non-nil).
type hkid struct {
	e   *hentry
	sub *hnode
}

var mapSeed = maphash.MakeSeed()

func mapHash(k string) uint64 { return maphash.String(mapSeed, k) }

// NewMap returns an empty map.
func NewMap() *Map { return &Map{} }

// find returns the entry with canonical key k (hash h), or nil.
func (n *hnode) find(h uint64, k string) *hentry {
	for shift := uint(0); n != nil; shift += 5 {
		if n.coll != nil {
			for _, e := range n.coll {
				if e.k == k {
					return e
				}
			}
			return nil
		}
		bit := uint32(1) << ((h >> shift) & 31)
		if n.bitmap&bit == 0 {
			return nil
		}
		kid := n.kids[bits.OnesCount32(n.bitmap&(bit-1))]
		if kid.sub == nil {
			if kid.e.h == h && kid.e.k == k {
				return kid.e
			}
			return nil
		}
		n = kid.sub
	}
	return nil
}

// editable returns n itself when own may change it in place, else a copy
// owned by own (nil: by nobody).
func (n *hnode) editable(own *Map) *hnode {
	if own != nil && n.owner == own {
		return n
	}
	c := &hnode{owner: own, bitmap: n.bitmap}
	if n.kids != nil {
		c.kids = make([]hkid, len(n.kids), len(n.kids)+1)
		copy(c.kids, n.kids)
	}
	if n.coll != nil {
		c.coll = slices.Clone(n.coll)
	}
	return c
}

// assoc returns the node with e in place of any entry of the same key, and
// the entry it replaced (nil when e is a new key). The recursion is at most
// 14 levels deep: thirteen five-bit levels exhaust the 64-bit hash, and a
// collision list ends it.
func (n *hnode) assoc(own *Map, shift uint, e *hentry) (*hnode, *hentry) {
	if n == nil {
		return &hnode{owner: own, bitmap: 1 << ((e.h >> shift) & 31), kids: []hkid{{e: e}}}, nil
	}
	if n.coll != nil {
		for i, c := range n.coll {
			if c.k == e.k {
				nn := n.editable(own)
				nn.coll[i] = e
				return nn, c
			}
		}
		nn := n.editable(own)
		nn.coll = append(nn.coll, e)
		return nn, nil
	}
	bit := uint32(1) << ((e.h >> shift) & 31)
	idx := bits.OnesCount32(n.bitmap & (bit - 1))
	if n.bitmap&bit == 0 {
		nn := n.editable(own)
		nn.kids = slices.Insert(nn.kids, idx, hkid{e: e})
		nn.bitmap |= bit
		return nn, nil
	}
	kid := n.kids[idx]
	if kid.sub != nil {
		sub, old := kid.sub.assoc(own, shift+5, e)
		if sub == kid.sub {
			return n, old // changed in place below; n is own's too
		}
		nn := n.editable(own)
		nn.kids[idx].sub = sub
		return nn, old
	}
	old := kid.e
	if old.k == e.k {
		nn := n.editable(own)
		nn.kids[idx].e = e
		return nn, old
	}
	var sub *hnode
	if old.h == e.h {
		sub = &hnode{owner: own, coll: []*hentry{old, e}}
	} else {
		sub = splitEntries(own, shift+5, old, e)
	}
	nn := n.editable(own)
	nn.kids[idx] = hkid{sub: sub}
	return nn, nil
}

// splitEntries makes the subtree holding two entries whose hashes differ.
// They differ in some bit below 64, so this ends by shift 60 at the latest.
func splitEntries(own *Map, shift uint, a, b *hentry) *hnode {
	ia, ib := (a.h>>shift)&31, (b.h>>shift)&31
	if ia == ib {
		return &hnode{owner: own, bitmap: 1 << ia, kids: []hkid{{sub: splitEntries(own, shift+5, a, b)}}}
	}
	if ia > ib {
		a, b = b, a
		ia, ib = ib, ia
	}
	return &hnode{owner: own, bitmap: 1<<ia | 1<<ib, kids: []hkid{{e: a}, {e: b}}}
}

// Put sets a key/value entry in m itself. Replacing an entry replaces its
// KEY item too: map:put(map{3:..}, xs:float('3.0'), ..) is map(xs:float, ..)
// (map-put-011, map-merge-011 use-last, same-key-004 keys become xs:float+).
func (m *Map) Put(key Item, val Object) {
	k := mapKey(key)
	h := mapHash(k)
	var own *Map
	if !m.frozen.Load() {
		own = m
	}
	root, old := m.root.assoc(own, 0, &hentry{h: h, k: k, key: key, val: val})
	m.root = root
	if old == nil {
		m.appendSlot(mapSlot{h: h, k: k})
	}
	m.summarize(key, val, len(m.order) == 1 && old == nil)
}

// appendSlot adds a new key at the end of m's insertion order, in the shared
// backing array when no other map has claimed that place, else in a copy.
func (m *Map) appendSlot(s mapSlot) {
	if m.log != nil {
		m.log.mu.Lock()
		if m.log.n == len(m.order) {
			m.order = append(m.order, s)
			m.log.n++
			m.log.mu.Unlock()
			return
		}
		m.log.mu.Unlock()
		order := make([]mapSlot, len(m.order), len(m.order)+len(m.order)/2+1)
		copy(order, m.order)
		m.order = order
	}
	m.order = append(m.order, s)
	m.log = &mapLog{n: len(m.order)}
}

// summarize folds one entry into the map(K, V) summary; first says it is
// the only entry.
func (m *Map) summarize(key Item, val Object, first bool) {
	kt, kok := itemAtomType(key)
	vt, vok := singleAtomType(val)
	if first {
		m.kt, m.keysOne = kt, kok
		m.vt, m.valsOne = vt, vok
		return
	}
	if m.keysOne && (!kok || kt != m.kt) {
		m.keysOne = false
	}
	if m.valsOne && (!vok || vt != m.vt) {
		m.valsOne = false
	}
}

// singleAtomType returns the atomic type of a value that is exactly one
// atomic item.
func singleAtomType(o Object) (AtomType, bool) {
	if s, ok := o.(Sequence); ok {
		if len(s) != 1 {
			return 0, false
		}
		o = s[0]
	}
	return itemAtomType(o)
}

// With returns a new map: m with the entry key→val set, sharing all of m it
// can. m is frozen from now on (see Map).
func (m *Map) With(key Item, val Object) *Map {
	if !m.frozen.Load() {
		m.frozen.Store(true)
	}
	nm := &Map{root: m.root, order: m.order, log: m.log,
		kt: m.kt, vt: m.vt, keysOne: m.keysOne, valsOne: m.valsOne}
	nm.Put(key, val)
	return nm
}

// Get returns the value for key (empty sequence if absent).
func (m *Map) Get(key Item) Object {
	k := mapKey(key)
	if e := m.root.find(mapHash(k), k); e != nil {
		return e.val
	}
	return Sequence{}
}

// Contains reports whether key is present.
func (m *Map) Contains(key Item) bool {
	k := mapKey(key)
	return m.root.find(mapHash(k), k) != nil
}

// Keys returns the keys in insertion order, in a new slice.
func (m *Map) Keys() []Item {
	if len(m.order) == 0 {
		return nil
	}
	out := make([]Item, len(m.order))
	for i, s := range m.order {
		out[i] = m.root.find(s.h, s.k).key
	}
	return out
}

// Range calls f for every entry in insertion order, until f returns false.
func (m *Map) Range(f func(key Item, val Object) bool) {
	for _, s := range m.order {
		e := m.root.find(s.h, s.k)
		if !f(e.key, e.val) {
			return
		}
	}
}

// Size returns the number of entries.
func (m *Map) Size() int { return len(m.order) }
