package xpath

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file implements the one XPath/XSD regex feature RE2 cannot express:
// BACK-REFERENCES (\1 … \9, and multi-digit forms). A pattern that uses one is
// compiled here into a small backtracking matcher instead of an RE2 program;
// every other pattern still goes straight to regexp.Compile, so the RE2 path —
// and with it the XSD pattern-facet validator, which forbids back-references
// outright — is completely untouched.
//
// The matcher deliberately delegates every SINGLE-CHARACTER decision (literals,
// escapes, character classes, \p{…} categories, the translated '.') back to
// RE2: each leaf atom is compiled on its own as ^(?:atom)$ and tested against
// one rune. Only the control structure — concatenation, alternation, groups,
// quantifiers, anchors and the back-references themselves — is interpreted
// here, which keeps the character-level semantics identical to the RE2 path.

// Regex is the compiled-regex handle the XPath regex functions use. Both
// *regexp.Regexp and the back-reference engine below satisfy it.
type Regex interface {
	MatchString(s string) bool
	FindAllStringSubmatchIndex(s string, n int) [][]int
	Split(s string, n int) []string
	ReplaceAllString(src, repl string) string
	NumSubexp() int
}

// ---- AST -------------------------------------------------------------------

type brNode interface{ brnode() }

type brSeq struct{ items []brNode }
type brAlt struct{ alts []brNode }
type brRepeat struct {
	item     brNode
	min, max int // max < 0 means unbounded
	greedy   bool
}
type brGroup struct {
	idx  int // 1-based capture index; 0 = non-capturing
	body brNode
}
type brLeaf struct{ re *regexp.Regexp } // matches exactly one rune
type brBackref struct {
	n  int
	ci bool
}
type brAnchor struct{ end bool }

func (*brSeq) brnode()     {}
func (*brAlt) brnode()     {}
func (*brRepeat) brnode()  {}
func (*brGroup) brnode()   {}
func (*brLeaf) brnode()    {}
func (*brBackref) brnode() {}
func (*brAnchor) brnode()  {}

// ---- parser ----------------------------------------------------------------

type brParser struct {
	src    []rune
	pos    int
	ngroup int
	ci     bool // 'i' in scope
	multi  bool // 'm' in scope (affects the anchors)
	// closed holds the capture groups whose ')' has already been consumed. A
	// back-reference may only name one of those: a FORWARD reference, or one
	// to the group it sits inside, is FORX0002 (regex-syntax-0820..0938,
	// fn-matches-37/38).
	closed map[int]bool
	// total is the number of capturing groups in the WHOLE pattern, counted
	// up front: F&O resolves "\NN" to the largest group number the pattern
	// has, independently of where the reference appears.
	total int
}

// compileBackref builds the backtracking matcher for an ALREADY-TRANSLATED
// pattern (Go regexp syntax, as produced by xsdRegexToGo) plus the global
// flag letters that would otherwise have been written as a (?…) prefix.
func compileBackref(pattern, goFlags string) (Regex, error) {
	p := &brParser{
		src:    []rune(pattern),
		ci:     strings.Contains(goFlags, "i"),
		multi:  strings.Contains(goFlags, "m"),
		closed: map[int]bool{},
	}
	p.total = countCaptureGroups(p.src)
	node, err := p.parseAlt()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.src) {
		return nil, fmt.Errorf("err:FORX0002: unexpected %q in regular expression", string(p.src[p.pos]))
	}
	return &backrefRegex{root: node, ngroup: p.ngroup, multi: p.multi}, nil
}

func (p *brParser) parseAlt() (brNode, error) {
	first, err := p.parseSeq()
	if err != nil {
		return nil, err
	}
	if p.pos >= len(p.src) || p.src[p.pos] != '|' {
		return first, nil
	}
	alt := &brAlt{alts: []brNode{first}}
	for p.pos < len(p.src) && p.src[p.pos] == '|' {
		p.pos++
		nxt, err := p.parseSeq()
		if err != nil {
			return nil, err
		}
		alt.alts = append(alt.alts, nxt)
	}
	return alt, nil
}

func (p *brParser) parseSeq() (brNode, error) {
	seq := &brSeq{}
	for p.pos < len(p.src) && p.src[p.pos] != '|' && p.src[p.pos] != ')' {
		item, err := p.parseQuantified()
		if err != nil {
			return nil, err
		}
		if item != nil {
			seq.items = append(seq.items, item)
		}
	}
	return seq, nil
}

func (p *brParser) parseQuantified() (brNode, error) {
	atom, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	if atom == nil || p.pos >= len(p.src) {
		return atom, nil
	}
	min, max := 0, 0
	switch p.src[p.pos] {
	case '*':
		min, max = 0, -1
		p.pos++
	case '+':
		min, max = 1, -1
		p.pos++
	case '?':
		min, max = 0, 1
		p.pos++
	case '{':
		n, m, next, ok := parseBrace(p.src, p.pos)
		if !ok {
			return atom, nil // a literal '{' the translator already vetted
		}
		min, max, p.pos = n, m, next
	default:
		return atom, nil
	}
	greedy := true
	if p.pos < len(p.src) && p.src[p.pos] == '?' {
		greedy = false
		p.pos++
	}
	return &brRepeat{item: atom, min: min, max: max, greedy: greedy}, nil
}

// parseBrace reads a {n}, {n,} or {n,m} quantifier starting at i.
func parseBrace(rs []rune, i int) (min, max, next int, ok bool) {
	j := i + 1
	start := j
	for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
		j++
	}
	if j == start {
		return 0, 0, 0, false
	}
	min, err := strconv.Atoi(string(rs[start:j]))
	if err != nil {
		return 0, 0, 0, false
	}
	max = min
	if j < len(rs) && rs[j] == ',' {
		j++
		start = j
		for j < len(rs) && rs[j] >= '0' && rs[j] <= '9' {
			j++
		}
		if j == start {
			max = -1
		} else {
			max, err = strconv.Atoi(string(rs[start:j]))
			if err != nil {
				return 0, 0, 0, false
			}
		}
	}
	if j >= len(rs) || rs[j] != '}' {
		return 0, 0, 0, false
	}
	return min, max, j + 1, true
}

func (p *brParser) parseAtom() (brNode, error) {
	if p.pos >= len(p.src) {
		return nil, nil
	}
	switch r := p.src[p.pos]; r {
	case '^':
		p.pos++
		return &brAnchor{}, nil
	case '$':
		p.pos++
		return &brAnchor{end: true}, nil
	case '(':
		return p.parseGroup()
	case '[':
		start := p.pos
		end, err := scanClass(p.src, p.pos)
		if err != nil {
			return nil, err
		}
		p.pos = end
		return p.leaf(string(p.src[start:end]))
	case '\\':
		return p.parseEscape()
	case ')':
		return nil, fmt.Errorf("err:FORX0002: unbalanced ')' in regular expression")
	case '*', '+', '?':
		// A quantifier where an atom is expected has nothing to repeat
		// (re00971's "(?:?=(a+?))").
		return nil, fmt.Errorf("err:FORX0002: nothing to repeat before %q", string(r))
	default:
		p.pos++
		return p.leaf(regexp.QuoteMeta(string(r)))
	}
}

func (p *brParser) parseGroup() (brNode, error) {
	p.pos++ // '('
	idx := 0
	savedCI, savedMulti := p.ci, p.multi
	if p.pos < len(p.src) && p.src[p.pos] == '?' {
		// (?:…) or an inline flag group (?flags:…) — the translator emits
		// (?-i:…) around \p{…} category escapes under the 'i' flag.
		j := p.pos + 1
		neg := false
		for j < len(p.src) && p.src[j] != ':' && p.src[j] != ')' {
			switch p.src[j] {
			case '-':
				neg = true
			case 'i':
				p.ci = !neg
			case 'm':
				p.multi = !neg
			case 's':
				// dot-all was already resolved by the translator
			default:
				return nil, fmt.Errorf("err:FORX0002: unsupported group modifier %q", string(p.src[j]))
			}
			j++
		}
		if j >= len(p.src) || p.src[j] != ':' {
			return nil, fmt.Errorf("err:FORX0002: unsupported group construct")
		}
		p.pos = j + 1
	} else {
		p.ngroup++
		idx = p.ngroup
	}
	body, err := p.parseAlt()
	if err != nil {
		return nil, err
	}
	if p.pos >= len(p.src) || p.src[p.pos] != ')' {
		return nil, fmt.Errorf("err:FORX0002: missing ')' in regular expression")
	}
	p.pos++
	p.ci, p.multi = savedCI, savedMulti
	if idx > 0 {
		p.closed[idx] = true
	}
	return &brGroup{idx: idx, body: body}, nil
}

func (p *brParser) parseEscape() (brNode, error) {
	if p.pos+1 >= len(p.src) {
		return nil, fmt.Errorf("err:FORX0002: trailing backslash in regular expression")
	}
	n := p.src[p.pos+1]
	if n >= '1' && n <= '9' {
		// A back-reference. The digit run is resolved greedily against the
		// groups closed SO FAR — "\11" is group 11 when that group is already
		// closed, else group 1 followed by a literal '1'.
		j := p.pos + 1
		for j < len(p.src) && p.src[j] >= '0' && p.src[j] <= '9' {
			j++
		}
		digits := p.src[p.pos+1 : j]
		p.pos = j
		num, k := 0, 0
		for n := len(digits); n >= 1; n-- {
			v, err := strconv.Atoi(string(digits[:n]))
			if err != nil {
				break
			}
			if v >= 1 && v <= p.total {
				num, k = v, n
				break
			}
		}
		if num == 0 {
			return nil, fmt.Errorf("err:FORX0002: back-reference \\%s does not refer to a capturing group", string(digits))
		}
		if !p.closed[num] {
			return nil, fmt.Errorf("err:FORX0002: back-reference \\%d refers to a group that is not yet closed", num)
		}
		ref := &brBackref{n: num, ci: p.ci}
		if k == len(digits) {
			return ref, nil
		}
		seq := &brSeq{items: []brNode{ref}}
		for _, d := range digits[k:] {
			lit, lerr := p.leaf(regexp.QuoteMeta(string(d)))
			if lerr != nil {
				return nil, lerr
			}
			seq.items = append(seq.items, lit)
		}
		return seq, nil
	}
	if n == 'p' || n == 'P' {
		j := p.pos + 2
		if j < len(p.src) && p.src[j] == '{' {
			for j < len(p.src) && p.src[j] != '}' {
				j++
			}
			if j >= len(p.src) {
				return nil, fmt.Errorf("err:FORX0002: unterminated \\%c{", n)
			}
			j++
		} else {
			j++
		}
		src := string(p.src[p.pos:j])
		p.pos = j
		return p.leaf(src)
	}
	src := string(p.src[p.pos : p.pos+2])
	p.pos += 2
	return p.leaf(src)
}

// leaf compiles one single-character atom as its own RE2 program, so every
// character-level decision keeps exactly the semantics the RE2 path has.
func (p *brParser) leaf(src string) (brNode, error) {
	pat := "^(?:" + src + ")$"
	if p.ci {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("err:FORX0002: invalid regular expression: %v", err)
	}
	return &brLeaf{re: re}, nil
}

// scanClass returns the index just past the character class starting at i.
func scanClass(rs []rune, i int) (int, error) {
	j := i + 1
	if j < len(rs) && rs[j] == '^' {
		j++
	}
	if j < len(rs) && rs[j] == ']' { // a leading ']' is a literal
		j++
	}
	for j < len(rs) {
		switch rs[j] {
		case '\\':
			j += 2
			continue
		case ']':
			return j + 1, nil
		}
		j++
	}
	return 0, fmt.Errorf("err:FORX0002: unterminated character class")
}

// ---- matcher ---------------------------------------------------------------

// backrefRegex is the compiled back-reference-capable pattern.
type backrefRegex struct {
	root   brNode
	ngroup int
	multi  bool
}

func (r *backrefRegex) NumSubexp() int { return r.ngroup }

// brRun is one match attempt's mutable state.
type brRun struct {
	in    []rune
	caps  []int // 2*(ngroup+1) rune positions; -1 = not set
	steps int
	multi bool
}

// maxBackrefSteps bounds one match attempt so a pathological pattern fails to
// match rather than running forever.
const maxBackrefSteps = 4_000_000

func (m *brRun) match(n brNode, pos int, k func(int) bool) bool {
	m.steps++
	if m.steps > maxBackrefSteps {
		return false
	}
	switch v := n.(type) {
	case *brSeq:
		return m.matchSeq(v.items, pos, k)
	case *brAlt:
		for _, a := range v.alts {
			saved := append([]int(nil), m.caps...)
			if m.match(a, pos, k) {
				return true
			}
			copy(m.caps, saved)
		}
		return false
	case *brGroup:
		if v.idx == 0 {
			return m.match(v.body, pos, k)
		}
		oldS, oldE := m.caps[2*v.idx], m.caps[2*v.idx+1]
		ok := m.match(v.body, pos, func(end int) bool {
			ps, pe := m.caps[2*v.idx], m.caps[2*v.idx+1]
			m.caps[2*v.idx], m.caps[2*v.idx+1] = pos, end
			if k(end) {
				return true
			}
			m.caps[2*v.idx], m.caps[2*v.idx+1] = ps, pe
			return false
		})
		if !ok {
			m.caps[2*v.idx], m.caps[2*v.idx+1] = oldS, oldE
		}
		return ok
	case *brRepeat:
		return m.matchRepeat(v, pos, 0, k)
	case *brLeaf:
		if pos < len(m.in) && v.re.MatchString(string(m.in[pos])) {
			return k(pos + 1)
		}
		return false
	case *brBackref:
		s, e := m.caps[2*v.n], m.caps[2*v.n+1]
		if s < 0 || e < 0 {
			// An unmatched group's back-reference matches the zero-length
			// string (F&O 5.6.1, and the XSD errata).
			return k(pos)
		}
		want := m.in[s:e]
		if pos+len(want) > len(m.in) {
			return false
		}
		for i, w := range want {
			got := m.in[pos+i]
			if got == w {
				continue
			}
			if v.ci && foldEqual(got, w) {
				continue
			}
			return false
		}
		return k(pos + len(want))
	case *brAnchor:
		if v.end {
			if pos == len(m.in) || (m.multi && m.in[pos] == '\n') {
				return k(pos)
			}
			return false
		}
		if pos == 0 || (m.multi && m.in[pos-1] == '\n') {
			return k(pos)
		}
		return false
	}
	return false
}

func (m *brRun) matchSeq(items []brNode, pos int, k func(int) bool) bool {
	if len(items) == 0 {
		return k(pos)
	}
	return m.match(items[0], pos, func(next int) bool {
		return m.matchSeq(items[1:], next, k)
	})
}

func (m *brRun) matchRepeat(v *brRepeat, pos, count int, k func(int) bool) bool {
	m.steps++
	if m.steps > maxBackrefSteps {
		return false
	}
	canMore := v.max < 0 || count < v.max
	more := func() bool {
		if !canMore {
			return false
		}
		return m.match(v.item, pos, func(next int) bool {
			if next == pos && count >= v.min {
				return false // zero-width repetition: stop rather than loop
			}
			return m.matchRepeat(v, next, count+1, k)
		})
	}
	if count < v.min {
		return more()
	}
	if v.greedy {
		if more() {
			return true
		}
		return k(pos)
	}
	if k(pos) {
		return true
	}
	return more()
}

// foldEqual reports whether two runes are equal under simple case folding.
func foldEqual(a, b rune) bool {
	return strings.EqualFold(string(a), string(b))
}

// findAt attempts a match anchored at rune position start, returning the
// capture vector (in rune positions) or nil.
func (r *backrefRegex) findAt(in []rune, start int) []int {
	m := &brRun{in: in, caps: make([]int, 2*(r.ngroup+1)), multi: r.multi}
	for i := range m.caps {
		m.caps[i] = -1
	}
	end := -1
	if !m.match(r.root, start, func(e int) bool { end = e; return true }) {
		return nil
	}
	out := make([]int, 2*(r.ngroup+1))
	copy(out, m.caps)
	out[0], out[1] = start, end
	return out
}

func (r *backrefRegex) MatchString(s string) bool {
	in := []rune(s)
	for i := 0; i <= len(in); i++ {
		if r.findAt(in, i) != nil {
			return true
		}
	}
	return false
}

// FindAllStringSubmatchIndex mirrors regexp.Regexp's method, returning BYTE
// offsets into s.
func (r *backrefRegex) FindAllStringSubmatchIndex(s string, n int) [][]int {
	in := []rune(s)
	byteOf := runeByteOffsets(s, len(in))
	var out [][]int
	pos := 0
	for pos <= len(in) {
		if n >= 0 && len(out) >= n {
			break
		}
		caps := r.findAt(in, pos)
		if caps == nil {
			pos++
			continue
		}
		bcaps := make([]int, len(caps))
		for i, v := range caps {
			if v < 0 {
				bcaps[i] = -1
			} else {
				bcaps[i] = byteOf[v]
			}
		}
		out = append(out, bcaps)
		if caps[1] == caps[0] {
			pos = caps[1] + 1
		} else {
			pos = caps[1]
		}
	}
	return out
}

// runeByteOffsets maps rune index -> byte offset (with a final entry for the
// end of the string).
func runeByteOffsets(s string, nrunes int) []int {
	offs := make([]int, 0, nrunes+1)
	for i := range s {
		offs = append(offs, i)
	}
	offs = append(offs, len(s))
	return offs
}

func (r *backrefRegex) Split(s string, n int) []string {
	matches := r.FindAllStringSubmatchIndex(s, -1)
	var out []string
	prev := 0
	for _, m := range matches {
		if n >= 0 && len(out) >= n-1 {
			break
		}
		out = append(out, s[prev:m[0]])
		prev = m[1]
	}
	out = append(out, s[prev:])
	return out
}

func (r *backrefRegex) ReplaceAllString(src, repl string) string {
	matches := r.FindAllStringSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return src
	}
	var b strings.Builder
	prev := 0
	for _, m := range matches {
		b.WriteString(src[prev:m[0]])
		b.WriteString(expandRepl(repl, src, m))
		prev = m[1]
	}
	b.WriteString(src[prev:])
	return b.String()
}

// expandRepl expands the Go-style $N / ${N} references in a replacement
// string against one match's capture vector.
func expandRepl(repl, src string, m []int) string {
	var b strings.Builder
	for i := 0; i < len(repl); {
		r, sz := utf8.DecodeRuneInString(repl[i:])
		if r != '$' {
			b.WriteRune(r)
			i += sz
			continue
		}
		i += sz
		if i < len(repl) && repl[i] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		braced := false
		if i < len(repl) && repl[i] == '{' {
			braced = true
			i++
		}
		start := i
		for i < len(repl) && repl[i] >= '0' && repl[i] <= '9' {
			i++
		}
		if start == i {
			b.WriteByte('$')
			if braced {
				b.WriteByte('{')
			}
			continue
		}
		num, _ := strconv.Atoi(repl[start:i])
		if braced && i < len(repl) && repl[i] == '}' {
			i++
		}
		if 2*num+1 < len(m) && m[2*num] >= 0 {
			b.WriteString(src[m[2*num]:m[2*num+1]])
		}
	}
	return b.String()
}

// countCaptureGroups counts the capturing groups in a translated pattern:
// every '(' that is not escaped, not inside a character class, and not
// followed by '?'.
func countCaptureGroups(rs []rune) int {
	n, inClass := 0, false
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			i++
		case '[':
			if !inClass {
				inClass = true
			}
		case ']':
			inClass = false
		case '(':
			if !inClass && !(i+1 < len(rs) && rs[i+1] == '?') {
				n++
			}
		}
	}
	return n
}
