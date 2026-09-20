package xpath

import "hash/maphash"

func init() {
	coreFuncs["random-number-generator"] = fnRandomNumberGenerator
}

// fnRandomNumberGenerator implements fn:random-number-generator($seed?). It
// returns a map with entries:
//
//	number  — an xs:double in [0,1)
//	next    — a zero-arity function returning the next generator (a like map)
//	permute — a function(item()*) that returns a pseudo-random permutation
//
// The result is deterministic for a given seed, as required by the spec (two
// calls with the same seed yield equal generators).
func fnRandomNumberGenerator(_ *Context, args []Object) (Object, error) {
	var seed uint64 = 0x9e3779b97f4a7c15 // fixed default seed (no nondeterminism)
	if len(args) >= 1 && !numIsEmpty(arg(args, 0)) {
		seed = seedFromItem(firstItem(arg(args, 0)))
	}
	return makeRNG(seed), nil
}

// seedFromItem derives a stable 64-bit seed from any atomic value via its string
// representation, so equal seeds produce equal generators.
func seedFromItem(it Item) uint64 {
	var h maphash.Hash
	h.SetSeed(rngHashSeed)
	h.WriteString(itemString(it))
	return h.Sum64()
}

// rngHashSeed is a fixed maphash seed so seeding is reproducible across calls.
var rngHashSeed = maphash.MakeSeed()

// splitmix64 advances a 64-bit state and returns the next pseudo-random value.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	z := x
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// unitFloat maps a 64-bit value to an xs:double in [0,1) using its top 53 bits.
func unitFloat(v uint64) float64 {
	return float64(v>>11) / float64(uint64(1)<<53)
}

func makeRNG(seed uint64) *Map {
	r := splitmix64(seed)
	m := NewMap()
	m.Put(NewString("number"), NewDouble(unitFloat(r)))
	next := seed + 0x9e3779b97f4a7c15
	m.Put(NewString("next"), &Function{Arity: 0, Name: "next", Call: func(_ []Object) (Object, error) {
		return makeRNG(next), nil
	}})
	m.Put(NewString("permute"), &Function{Arity: 1, Name: "permute", Call: func(a []Object) (Object, error) {
		items := append([]Item(nil), Items(arg(a, 0))...)
		// Fisher–Yates using a PRNG stream seeded from this generator.
		state := seed
		for i := len(items) - 1; i > 0; i-- {
			state = splitmix64(state)
			j := int(state % uint64(i+1))
			items[i], items[j] = items[j], items[i]
		}
		return FromItems(items), nil
	}})
	return m
}
