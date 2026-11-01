package xpath

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// expandClassSubtraction rewrites XSD character-class subtraction
// (e.g. "[a-z-[aeiou]]") — which Go's RE2 cannot parse — into an explicit class
// over the difference of codepoint ranges. Classes without subtraction, and
// those whose operands aren't pure ranges/singletons/blocks (e.g. they contain
// \d or \p{L}), are left unchanged for the main translator / RE2 to handle.
func expandClassSubtraction(p string) string {
	rs := []rune(p)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			b.WriteRune(rs[i])
			b.WriteRune(rs[i+1])
			i++
			continue
		}
		if rs[i] == '[' {
			if cls, next, ok := tryClassSubtraction(rs, i); ok {
				b.WriteString(cls)
				i = next
				continue
			}
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// unionRanges merges two range sets into one sorted, coalesced set.
func unionRanges(a, b [][2]rune) [][2]rune {
	all := append(append([][2]rune{}, a...), b...)
	sort.Slice(all, func(i, j int) bool { return all[i][0] < all[j][0] })
	var out [][2]rune
	for _, r := range all {
		if len(out) > 0 && r[0] <= out[len(out)-1][1]+1 {
			if r[1] > out[len(out)-1][1] {
				out[len(out)-1][1] = r[1]
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// tryClassSubtraction attempts to parse a class starting at rs[i]=='[' that
// contains a "-[ … ]" subtraction. On success it returns the rewritten RE2 class
// and the index of the closing ']'. ok=false means "not a subtraction class".
func tryClassSubtraction(rs []rune, i int) (string, int, bool) {
	j := i + 1
	neg := false
	if j < len(rs) && rs[j] == '^' {
		neg = true
		j++
	}
	// Collect the base body until a top-level "-[".
	var base []rune
	subStart := -1
	for j < len(rs) {
		if rs[j] == '\\' && j+1 < len(rs) {
			base = append(base, rs[j], rs[j+1])
			j += 2
			continue
		}
		if rs[j] == '-' && j+1 < len(rs) && rs[j+1] == '[' {
			subStart = j + 1
			break
		}
		if rs[j] == ']' {
			return "", 0, false // plain class, no subtraction
		}
		base = append(base, rs[j])
		j++
	}
	if subStart < 0 {
		return "", 0, false
	}
	// Parse the nested subtraction class [ … ]; a leading ^ negates it, so
	// "base - [^X]" removes everything NOT in X, i.e. leaves base ∩ X.
	k := subStart + 1
	subNeg := false
	if k < len(rs) && rs[k] == '^' {
		subNeg = true
		k++
	}
	var sub []rune
	for k < len(rs) {
		if rs[k] == '\\' && k+1 < len(rs) {
			sub = append(sub, rs[k], rs[k+1])
			k += 2
			continue
		}
		if rs[k] == ']' {
			break
		}
		sub = append(sub, rs[k])
		k++
	}
	if k >= len(rs) || k+1 >= len(rs) || rs[k+1] != ']' {
		return "", 0, false // malformed; let RE2 report it
	}
	baseRanges, ok1 := parseClassRanges(string(base))
	subRanges, ok2 := parseClassRanges(string(sub))
	if !ok1 || !ok2 {
		return "", 0, false // operands not pure ranges — leave for fallback
	}
	var diff [][2]rune
	positive := !neg
	switch {
	case !neg && subNeg: // B - ¬S = B ∩ S
		diff = intersectRanges(baseRanges, subRanges)
	case !neg && !subNeg: // B - S
		diff = subtractRanges(baseRanges, subRanges)
	case neg && !subNeg: // ¬B - S = ¬(B ∪ S)  (RegexTest_430)
		diff = unionRanges(baseRanges, subRanges)
		positive = false
	default: // ¬B - ¬S = S \ B
		diff = subtractRanges(subRanges, baseRanges)
		positive = true
	}
	body := emitRanges(diff)
	prefix := "["
	if !positive {
		prefix = "[^"
	}
	neg = !positive
	if body == "" {
		// Empty positive class matches nothing; empty negated matches anything.
		full := emitRanges([][2]rune{{0, 0x10ffff}})
		if neg {
			return "[" + full + "]", k + 1, true
		}
		return "[^" + full + "]", k + 1, true
	}
	return prefix + body + "]", k + 1, true
}

// parseClassRanges parses a class body of literals, ranges (a-z), simple escapes
// and \p{IsBlock} into codepoint ranges. It returns ok=false if it meets a
// construct it cannot reduce to ranges (\d, \p{L}, \s, …).
func parseClassRanges(s string) ([][2]rune, bool) {
	rs := []rune(s)
	var out [][2]rune
	for i := 0; i < len(rs); i++ {
		var lo rune
		switch {
		case rs[i] == '\\' && i+1 < len(rs):
			n := rs[i+1]
			if c, ok := singleCharEscape(n); ok {
				lo = c
				i++
				break
			}
			switch n {
			case 'i':
				// XSD \i (XML NameStartChar) — expand so class subtraction like
				// [\i-[:]] reduces to explicit ranges.
				out = append(out, xmlNameStartRanges...)
				i++
				continue
			case 'c':
				// XSD \c (XML NameChar).
				out = append(out, xmlNameCharRanges...)
				i++
				continue
			case 'I', 'C':
				// Their complements (XSD Part 2 §G.4.2.5), e.g. [\I-[a]].
				if n == 'I' {
					out = append(out, complementRanges(xmlNameStartRanges)...)
				} else {
					out = append(out, complementRanges(xmlNameCharRanges)...)
				}
				i++
				continue
			case 'p', 'P':
				// \p{IsBlock} expands to its range; \p{L}, \p{Nd}, \p{Cn}, ... to
				// the code points of that general category; \P{..} to the
				// complement of either.
				if i+2 >= len(rs) || rs[i+2] != '{' {
					return nil, false
				}
				e := i + 3
				for e < len(rs) && rs[e] != '}' {
					e++
				}
				if e >= len(rs) {
					return nil, false
				}
				name := string(rs[i+3 : e])
				var set [][2]rune
				if strings.HasPrefix(name, "Is") {
					rr, ok := blockRanges(name[2:])
					if !ok || len(rr) == 0 {
						return nil, false
					}
					set = rr
				} else {
					var ok bool
					if set, ok = categoryRanges(name); !ok {
						return nil, false
					}
				}
				if n == 'P' {
					set = complementRanges(set)
				}
				out = append(out, set...)
				i = e
				continue
			case 'd', 'D', 's', 'S', 'w', 'W':
				// XSD multi-character escapes (Unicode semantics, not RE2's ASCII
				// ones): \d = \p{Nd}, \s = [ \t\n\r], \w = everything except
				// \p{P}, \p{Z}, \p{C}; the capital forms are their complements.
				var set [][2]rune
				switch n {
				case 'd', 'D':
					set, _ = categoryRanges("Nd")
				case 's', 'S':
					set = [][2]rune{{9, 10}, {13, 13}, {32, 32}}
				default:
					p, _ := categoryRanges("P")
					z, _ := categoryRanges("Z")
					c, _ := categoryRanges("C")
					set = complementRanges(unionRanges(unionRanges(p, z), c))
				}
				if n == 'D' || n == 'S' || n == 'W' {
					set = complementRanges(set)
				}
				out = append(out, set...)
				i++
				continue
			default:
				return nil, false // \\ escapes this cannot reduce
			}
		default:
			lo = rs[i]
		}
		// Range "lo-hi"?
		if i+1 < len(rs) && rs[i+1] == '-' && i+2 < len(rs) && rs[i+2] != ']' {
			hiStart := i + 2
			var hi rune
			if rs[hiStart] == '\\' && hiStart+1 < len(rs) {
				// An escaped endpoint denotes the escaped character
				// (seRange ::= charOrEsc '-' charOrEsc): [\t-\r] is #x9..#xD.
				c, ok := singleCharEscape(rs[hiStart+1])
				if !ok {
					return nil, false
				}
				hi = c
				i = hiStart + 1
			} else {
				hi = rs[hiStart]
				i = hiStart
			}
			out = append(out, [2]rune{lo, hi})
			continue
		}
		out = append(out, [2]rune{lo, lo})
	}
	return out, true
}

// singleCharEscape decodes the letter after '\' of a single-character escape
// (\n \r \t and the escaped metacharacters) to the character it denotes;
// ok=false for any other escape.
func singleCharEscape(n rune) (rune, bool) {
	switch n {
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case '\\', '-', ']', '[', '^', '.', '|', '(', ')', '{', '}', '*', '+', '?', '/':
		return n, true
	}
	return 0, false
}

// intersectRanges returns the codepoints present in both a and b.
func intersectRanges(a, b [][2]rune) [][2]rune {
	var out [][2]rune
	for _, x := range a {
		for _, y := range b {
			lo, hi := x[0], x[1]
			if y[0] > lo {
				lo = y[0]
			}
			if y[1] < hi {
				hi = y[1]
			}
			if lo <= hi {
				out = append(out, [2]rune{lo, hi})
			}
		}
	}
	return out
}

// subtractRanges returns base minus sub (set difference over codepoint ranges).
func subtractRanges(base, sub [][2]rune) [][2]rune {
	result := base
	for _, s := range sub {
		var next [][2]rune
		for _, r := range result {
			if s[1] < r[0] || s[0] > r[1] { // disjoint
				next = append(next, r)
				continue
			}
			if s[0] > r[0] {
				next = append(next, [2]rune{r[0], s[0] - 1})
			}
			if s[1] < r[1] {
				next = append(next, [2]rune{s[1] + 1, r[1]})
			}
		}
		result = next
	}
	return result
}

func emitRanges(ranges [][2]rune) string {
	var b strings.Builder
	for _, r := range clipSurrogates(ranges) {
		if r[0] == r[1] {
			b.WriteString(classEscape(r[0]))
		} else {
			b.WriteString(classEscape(r[0]) + "-" + classEscape(r[1]))
		}
	}
	return b.String()
}

// classEscape renders a single codepoint for inclusion in a character class.
// It emits literal runes (escaping only class-significant characters) rather
// than \x{…} hex, because expandClassSubtraction's output is re-parsed by
// xsdRegexToGo, which rejects \x as an invalid XSD escape.
func classEscape(r rune) string {
	switch r {
	case '\\', ']', '^', '-', '[':
		return "\\" + string(r)
	}
	return string(r)
}

// --- XML Name productions ----------------------------------------------------
//
// XML 1.0 (5th edition) NameStartChar / NameChar. These back the XSD regex
// escapes \i and \c (whose definition is "the first character of a Name" and
// "a Name character") as well as the xs:Name / xs:NCName / xs:NMTOKEN lexical
// spaces, so all of them agree by construction. The tables are
// xmltree.NameStartRanges / NameExtraRanges — the engine's one definition,
// shared with the parser's name repair and the XSLT name checks.

var xmlNameStartRanges = xmltree.NameStartRanges()

// The additional characters a NameChar may have beyond a NameStartChar.
var xmlNameExtraRanges = xmltree.NameExtraRanges()

var xmlNameCharRanges = append(append([][2]rune{}, xmlNameStartRanges...), xmlNameExtraRanges...)

// The same two productions with ':' removed — the NCName forms.
var (
	ncNameStartRanges = dropColon(xmlNameStartRanges)
	ncNameCharRanges  = dropColon(xmlNameCharRanges)
)

// dropColon returns rs without the ':' character (NCName is Name without colons).
func dropColon(rs [][2]rune) [][2]rune {
	out := make([][2]rune, 0, len(rs))
	for _, r := range rs {
		if r == [2]rune{':', ':'} {
			continue
		}
		out = append(out, r)
	}
	return out
}

// classBody renders ranges as the inside of a Go regexp character class, using
// \x{...} escapes throughout so no member can be read as a metacharacter.
func classBody(rs [][2]rune) string {
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, `\x{%X}`, r[0])
		if r[1] != r[0] {
			fmt.Fprintf(&b, `-\x{%X}`, r[1])
		}
	}
	return b.String()
}

// complementBody renders the COMPLEMENT of a range table as a character-class
// body: the gaps over U+0000..U+10FFFF, with the surrogate block excluded
// (RE2 rejects literal surrogates). Input need not be sorted or merged.
func complementBody(rs [][2]rune) string {
	sorted := append([][2]rune{}, rs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })
	var merged [][2]rune
	for _, r := range sorted {
		if n := len(merged); n > 0 && r[0] <= merged[n-1][1]+1 {
			if r[1] > merged[n-1][1] {
				merged[n-1][1] = r[1]
			}
			continue
		}
		merged = append(merged, r)
	}
	var gaps [][2]rune
	next := rune(0)
	for _, r := range merged {
		if r[0] > next {
			gaps = append(gaps, [2]rune{next, r[0] - 1})
		}
		if r[1]+1 > next {
			next = r[1] + 1
		}
	}
	if next <= 0x10FFFF {
		gaps = append(gaps, [2]rune{next, 0x10FFFF})
	}
	var clipped [][2]rune
	for _, g := range gaps {
		lo, hi := g[0], g[1]
		if lo <= 0xDFFF && hi >= 0xD800 { // clip the surrogate block
			if lo <= 0xD7FF {
				clipped = append(clipped, [2]rune{lo, min32(hi, 0xD7FF)})
			}
			if hi >= 0xE000 {
				clipped = append(clipped, [2]rune{max32(lo, 0xE000), hi})
			}
			continue
		}
		clipped = append(clipped, g)
	}
	return classBody(clipped)
}

func min32(a, b rune) rune {
	if a < b {
		return a
	}
	return b
}
func max32(a, b rune) rune {
	if a > b {
		return a
	}
	return b
}

// Character-class bodies for the XML name productions, for callers that build a
// regexp (the XSD simple-type lexical checks) rather than match rune by rune.
var (
	NameStartClass   = classBody(xmlNameStartRanges)
	NameCharClass    = classBody(xmlNameCharRanges)
	NCNameStartClass = classBody(dropColon(xmlNameStartRanges))
	NCNameCharClass  = classBody(dropColon(xmlNameCharRanges))
	// Complements, for \I and \C inside a character class (a union
	// contribution of "everything that is NOT a Name(Start)Char").
	NameStartComplement = complementBody(xmlNameStartRanges)
	NameCharComplement  = complementBody(xmlNameCharRanges)
)

// clipSurrogates removes U+D800..U+DFFF: a Go string cannot hold a surrogate
// code point, so a class listing one literally would silently become U+FFFD.
func clipSurrogates(rs [][2]rune) [][2]rune {
	var out [][2]rune
	for _, r := range rs {
		lo, hi := r[0], r[1]
		if hi < 0xD800 || lo > 0xDFFF {
			out = append(out, r)
			continue
		}
		if lo < 0xD800 {
			out = append(out, [2]rune{lo, 0xD7FF})
		}
		if hi > 0xDFFF {
			out = append(out, [2]rune{0xE000, hi})
		}
	}
	return out
}

// complementRanges returns the gaps of a (sorted or unsorted) range set over
// U+0000..U+10FFFF.
func complementRanges(rs [][2]rune) [][2]rune {
	merged := unionRanges(rs, nil)
	var out [][2]rune
	next := rune(0)
	for _, r := range merged {
		if r[0] > next {
			out = append(out, [2]rune{next, r[0] - 1})
		}
		if r[1]+1 > next {
			next = r[1] + 1
		}
	}
	if next <= 0x10FFFF {
		out = append(out, [2]rune{next, 0x10FFFF})
	}
	return out
}

// tableRanges turns a Go range table into sorted ranges.
func tableRanges(t *unicode.RangeTable) [][2]rune {
	var out [][2]rune
	for _, r := range t.R16 {
		if r.Stride == 1 {
			out = append(out, [2]rune{rune(r.Lo), rune(r.Hi)})
			continue
		}
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			out = append(out, [2]rune{c, c})
		}
	}
	for _, r := range t.R32 {
		if r.Stride == 1 {
			out = append(out, [2]rune{rune(r.Lo), rune(r.Hi)})
			continue
		}
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			out = append(out, [2]rune{c, c})
		}
	}
	return out
}

// categoryRanges returns the code points of an XSD general-category escape
// (\p{L}, \p{Lu}, \p{Cn}, \p{C}, ...). Go's unicode.C is Cc|Cf|Co|Cs and has no
// Cn, whereas XSD's C includes the unassigned code points, so C and Cn are
// derived as the complement of every other (assigned) category.
func categoryRanges(name string) ([][2]rune, bool) {
	switch name {
	case "C", "Cn":
		var assigned [][2]rune
		for _, g := range []string{"L", "M", "N", "P", "S", "Z"} {
			assigned = append(assigned, tableRanges(unicode.Categories[g])...)
		}
		if name == "Cn" {
			for _, g := range []string{"Cc", "Cf", "Co", "Cs"} {
				assigned = append(assigned, tableRanges(unicode.Categories[g])...)
			}
		}
		return complementRanges(assigned), true
	}
	t, ok := unicode.Categories[name]
	if !ok {
		return nil, false
	}
	return unionRanges(tableRanges(t), nil), true
}
