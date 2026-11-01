package xpath

import (
	"math/rand"
	"strconv"
	"sync"
	"testing"
)

// TestMapPersistent checks the persistent map against a plain model under a
// random mix of With (a new version from any older one) and Put (changing a
// version in place, also one whose nodes other versions share): every
// version must keep exactly its own entries, in insertion order.
func TestMapPersistent(t *testing.T) {
	type pair struct {
		key Item
		val string
	}
	keyOf := func(r *rand.Rand) Item {
		n := r.Intn(300)
		switch r.Intn(4) {
		case 0:
			return NewInteger(int64(n))
		case 1:
			return NewDouble(float64(n)) // the same key as the integer n
		case 2:
			return NewString("k" + itoa(n))
		}
		return NewUntyped("k" + itoa(n)) // the same key as the string
	}
	r := rand.New(rand.NewSource(7))
	maps := []*Map{NewMap()}
	models := [][]pair{nil}
	set := func(model []pair, key Item, val string) []pair {
		k := mapKey(key)
		for i, p := range model {
			if mapKey(p.key) == k {
				model[i] = pair{key, val}
				return model
			}
		}
		return append(model, pair{key, val})
	}
	check := func(step int) {
		for i, m := range maps {
			model := models[i]
			if m.Size() != len(model) {
				t.Fatalf("step %d, version %d: size %d, want %d", step, i, m.Size(), len(model))
			}
			keys := m.Keys()
			for j, p := range model {
				if keys[j] != p.key {
					t.Fatalf("step %d, version %d: key %d is %v, want %v", step, i, j, keys[j], p.key)
				}
				if got := ToString(m.Get(p.key)); got != p.val {
					t.Fatalf("step %d, version %d: value of %v is %q, want %q", step, i, p.key, got, p.val)
				}
			}
			if m.keysOne || m.valsOne {
				m.Range(func(k Item, v Object) bool {
					if kt, _ := itemAtomType(k); m.keysOne && kt != m.kt {
						t.Fatalf("step %d, version %d: summary says keys are %v, %v is not", step, i, m.kt, k)
					}
					if vt, ok := singleAtomType(v); m.valsOne && (!ok || vt != m.vt) {
						t.Fatalf("step %d, version %d: summary says values are %v", step, i, m.vt)
					}
					return true
				})
			}
		}
	}
	for step := 0; step < 4000; step++ {
		i := len(maps) - 1
		if r.Intn(4) == 0 {
			i = r.Intn(len(maps))
		}
		key := keyOf(r)
		val := "v" + itoa(step)
		var v Object = NewString(val)
		if r.Intn(10) == 0 {
			v = NewUntyped(val) // breaks the value summary
		}
		if r.Intn(3) == 0 {
			maps[i].Put(key, v)
			models[i] = set(models[i], key, val)
		} else {
			maps = append(maps, maps[i].With(key, v))
			models = append(models, set(append([]pair(nil), models[i]...), key, val))
		}
		if step%97 == 0 {
			check(step)
		}
	}
	check(-1)
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestMapHashCollision drives the trie's collision lists and deep splits
// with hand-made hashes.
func TestMapHashCollision(t *testing.T) {
	var root *hnode
	ents := []*hentry{
		{h: 1, k: "a"}, {h: 1, k: "b"}, {h: 1, k: "c"}, // one hash, three keys
		{h: 1 | 1<<60, k: "d"}, // differs only in the last level
		{h: 33, k: "e"},        // differs in the second level
	}
	for _, e := range ents {
		root, _ = root.assoc(nil, 0, e)
	}
	for _, e := range ents {
		if got := root.find(e.h, e.k); got != e {
			t.Fatalf("find %q: got %v", e.k, got)
		}
	}
	if root.find(1, "z") != nil || root.find(2, "a") != nil {
		t.Fatal("found an absent key")
	}
	nb := &hentry{h: 1, k: "b", val: "new"}
	r2, old := root.assoc(nil, 0, nb)
	if old != ents[1] || r2.find(1, "b") != nb || root.find(1, "b") != ents[1] {
		t.Fatal("replacing in a collision list must leave the old trie alone")
	}
}

// TestMapConcurrentWith derives maps from one shared map in several
// goroutines at once (run with -race): a frozen map is read-only.
func TestMapConcurrentWith(t *testing.T) {
	base := NewMap()
	for i := 0; i < 1000; i++ {
		base.Put(NewInteger(int64(i)), NewInteger(int64(i)))
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			m := base
			for i := 0; i < 500; i++ {
				m = m.With(NewInteger(int64(1000+g*1000+i)), NewInteger(int64(g)))
				_ = base.With(NewInteger(int64(i)), NewString("x")).Get(NewInteger(int64(i)))
			}
			if m.Size() != 1500 || base.Size() != 1000 || len(m.Keys()) != 1500 {
				t.Errorf("goroutine %d: sizes %d %d", g, m.Size(), base.Size())
			}
		}(g)
	}
	wg.Wait()
}

// TestArrayAppendShared appends to old and new arrays in any order: every
// array keeps exactly its own members although appends share backing
// arrays, also when several goroutines append to one array at once (-race).
func TestArrayAppendShared(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	arrs := []*Array{NewArray(nil), NewArray([]Object{NewInteger(-1)})}
	models := [][]int64{nil, {-1}}
	for step := 0; step < 3000; step++ {
		i := len(arrs) - 1
		if r.Intn(3) == 0 {
			i = r.Intn(len(arrs))
		}
		arrs = append(arrs, arrs[i].Append(NewInteger(int64(step))))
		models = append(models, append(append([]int64(nil), models[i]...), int64(step)))
	}
	for i, a := range arrs {
		if a.Size() != len(models[i]) {
			t.Fatalf("array %d: size %d, want %d", i, a.Size(), len(models[i]))
		}
		for j, want := range models[i] {
			if got := a.Get(j + 1).(*Atomic).i.Int64(); got != want {
				t.Fatalf("array %d member %d: %d, want %d", i, j+1, got, want)
			}
		}
		if m := a.Members(); cap(m) != len(m) {
			t.Fatalf("array %d: Members has spare capacity", i)
		}
	}
	base := NewArray(nil).Append(NewInteger(0))
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			a := base
			for i := 0; i < 200; i++ {
				a = a.Append(NewInteger(int64(g)))
			}
			if a.Size() != 201 || ToString(a.Get(201)) != strconv.Itoa(g) {
				t.Errorf("goroutine %d: size %d", g, a.Size())
			}
		}(g)
	}
	wg.Wait()
}
