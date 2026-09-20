package xpath

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// RegexGrammar selects which regular-expression grammar ValidateRegexGrammar
// enforces. All three share the XML Schema Part 2 Appendix F core; they differ
// in the extensions each host language layers on top.
type RegexGrammar int

const (
	// GrammarXSD10 is the XML Schema 1.0 pattern-facet grammar (Appendix F).
	GrammarXSD10 RegexGrammar = iota
	// GrammarXSD11 is the XML Schema 1.1 pattern-facet grammar (Appendix G):
	// the character-class production was rewritten so a '-' with no usable
	// left endpoint reads as a literal, and an unknown \p{Is…} block name is
	// tolerated.
	GrammarXSD11
	// GrammarXPath is the XPath/XQuery F&O 3.1 §5.6.1 grammar: XSD's, plus
	// the '^'/'$' anchors, reluctant quantifiers, back-references and the
	// non-capturing '(?:…)' group. Its character-class production is XSD
	// 1.0's (F&O references XML Schema 1.0), so the chained-range and
	// dangling-'-' rules apply.
	GrammarXPath
)

// strictClasses reports whether the grammar uses XSD 1.0's character-class
// production, under which a chained range such as [a-c-1-4] is invalid.
//
// GrammarXPath's DEFAULT does NOT: F&O leaves the choice to the processor's
// XSD version in force, and this engine defaults to XSD 1.1, whose rewritten
// charGroupPart production reads a '-' with no usable left endpoint as a
// literal. The W3C suites carry both variants of each affected case (QT3
// re00056/re00056a, re00086/re00086a, re00102a/re00102; XSLT30
// regex-syntax-0056/0086/0102 and their XSD_1.1 counterparts) tagged with the
// dependency they need — the QT3 harness now honors a case's own
// xsd-version dependency (Context.XSDVersion, wired only through
// fn:matches's compileRegexForXSDVersion so nothing else's grammar choice
// moves), so BOTH members of each pair now pass rather than exactly one
// being unwinnable.
func (g RegexGrammar) strictClasses() bool { return g == GrammarXSD10 }

// xpathExt reports whether the F&O extensions (anchors, reluctant quantifiers,
// back-references, non-capturing groups) are part of the grammar.
func (g RegexGrammar) xpathExt() bool { return g == GrammarXPath }

// ValidateRegexGrammar checks a regular expression against the parts of the
// XSD/XPath regular-expression grammar (XML Schema Part 2, Appendix F; F&O 3.1
// §5.6.1) that Go's RE2 accepts but the grammar forbids, so that a schema
// carrying such a pattern — or an fn:matches/replace/tokenize call making one —
// is correctly rejected. It flags only unambiguously-invalid constructs to
// avoid ever rejecting a valid pattern:
//
//   - reluctant quantifiers (*?, +?, ??, }?) — XSD has no lazy quantifiers
//     (F&O does, so GrammarXPath allows them);
//   - a backslash followed by a digit — XSD allows neither back-references nor
//     octal escapes (F&O allows back-references);
//   - "(?" group-extension syntax — XSD has no (?:…) or other (?…) groups
//     (F&O 3.0 added the non-capturing "(?:…)" form only);
//   - an unescaped ']' outside a character class — ']' is not a NormalChar in
//     either grammar;
//   - a '{' that does not open a well-formed {n}, {n,}, or {n,m} quantifier.
//
// '^' and '$' are ordinary characters in XSD and anchors in F&O; either way
// they are accepted here, so the difference does not affect validity.
func ValidateRegexGrammar(pat string, g RegexGrammar) error {
	prevQuant, prevAtom := false, false
	depth := 0
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		if c == '\\' {
			// backReference ::= '\' [1-9][0-9]* — an F&O-only atom, and only
			// outside a character class (charClassEsc has no back-reference).
			if g.xpathExt() && i+1 < len(pat) && pat[i+1] >= '1' && pat[i+1] <= '9' {
				j := i + 1
				for j < len(pat) && pat[j] >= '0' && pat[j] <= '9' {
					j++
				}
				i = j - 1
				prevQuant, prevAtom = false, true
				continue
			}
			end, err := classAtomEnd(pat, i, g)
			if err != nil {
				return fmt.Errorf("invalid pattern %q: %v", pat, err)
			}
			i = end - 1
			prevQuant, prevAtom = false, true
			continue
		}
		switch c {
		case '[':
			end, err := validateCharClass(pat, i, g)
			if err != nil {
				return fmt.Errorf("invalid pattern %q: %v", pat, err)
			}
			i = end
			prevQuant, prevAtom = false, true
		case ']':
			return fmt.Errorf("invalid pattern %q: unescaped ']' outside a character class", pat)
		case '*', '+':
			// piece ::= atom quantifier? — a quantifier needs a preceding
			// atom (reB62-65, reE3-5) and at most one may appear (a**,
			// [abcd]{0,16}* — RegexTest_647/_299).
			if prevQuant {
				return fmt.Errorf("invalid pattern %q: double quantifier", pat)
			}
			if !prevAtom {
				return fmt.Errorf("invalid pattern %q: quantifier with no preceding atom", pat)
			}
			prevQuant = true
		case '?':
			if prevQuant {
				// F&O's quantifier production ends in an optional '?'
				// (the reluctant marker); XSD's does not.
				if g.xpathExt() {
					prevQuant = false // the piece is complete; no further quantifier
					prevAtom = false
					continue
				}
				return fmt.Errorf("invalid pattern %q: reluctant quantifiers are not allowed in XSD", pat)
			}
			if !prevAtom {
				return fmt.Errorf("invalid pattern %q: quantifier with no preceding atom", pat)
			}
			prevQuant = true
		case '{':
			j := i + 1
			for j < len(pat) && pat[j] != '}' {
				j++
			}
			if j >= len(pat) || !validQuantBody(pat[i+1:j]) {
				return fmt.Errorf("invalid pattern %q: malformed quantifier", pat)
			}
			if prevQuant {
				return fmt.Errorf("invalid pattern %q: double quantifier", pat)
			}
			if !prevAtom {
				return fmt.Errorf("invalid pattern %q: quantifier with no preceding atom", pat)
			}
			i = j
			prevQuant = true
		case '(':
			if i+1 < len(pat) && pat[i+1] == '?' {
				// F&O 3.0 added exactly one group extension, the
				// non-capturing "(?:…)"; every other "(?…)" form is still
				// outside the grammar.
				if g.xpathExt() && i+2 < len(pat) && pat[i+2] == ':' {
					i += 2
					depth++
					prevQuant, prevAtom = false, false
					continue
				}
				return fmt.Errorf("invalid pattern %q: (?…) group syntax is not allowed in XSD", pat)
			}
			depth++
			prevQuant, prevAtom = false, false
		case ')':
			// atom ::= '(' regExp ')' — an unmatched ')' is not a NormalChar
			// (reD10-12, reE9, RegexTest_641).
			if depth == 0 {
				return fmt.Errorf("invalid pattern %q: unbalanced ')'", pat)
			}
			depth--
			prevQuant, prevAtom = false, true
		case '|':
			// A branch may be EMPTY (reE10 "\\|" is valid), so '|' only
			// resets the piece state.
			prevQuant, prevAtom = false, false
		default:
			prevQuant, prevAtom = false, true
		}
	}
	if depth > 0 {
		return fmt.Errorf("invalid pattern %q: unterminated group", pat)
	}
	return nil
}

// classAtomEnd returns the index just past the atom starting at pat[j] — a single
// character or a backslash escape (including \p{…}). It errors on the escapes XSD
// forbids: a trailing backslash or a \digit back-reference/octal escape.
func classAtomEnd(pat string, j int, g RegexGrammar) (int, error) {
	if pat[j] != '\\' {
		return j + 1, nil
	}
	if j+1 >= len(pat) {
		return 0, fmt.Errorf("trailing backslash")
	}
	n := pat[j+1]
	if n >= '0' && n <= '9' {
		return 0, fmt.Errorf("back-reference/octal escape not allowed in XSD")
	}
	if (n == 'p' || n == 'P') && j+2 < len(pat) && pat[j+2] == '{' {
		k := j + 3
		for k < len(pat) && pat[k] != '}' {
			k++
		}
		if k >= len(pat) {
			return 0, fmt.Errorf("unclosed \\p{…}")
		}
		if err := validCharProp(pat[j+3:k], g); err != nil {
			return 0, err
		}
		return k + 1, nil
	}
	// The XSD escape repertoire is closed: SingleCharEsc ∪ MultiCharEsc ∪
	// catEsc. \\b, \\A, \\z, \\x, \\u and friends are not in it
	// (RegexTest_194-233, _9-11, _57-65…), and \\p/\\P demand a {…} body
	// (RegexTest_51-54).
	switch n {
	case 'n', 'r', 't', '\\', '|', '.', '?', '*', '+', '(', ')', '{', '}', '-', '[', ']', '^':
	case 's', 'S', 'i', 'I', 'c', 'C', 'd', 'D', 'w', 'W':
	case '$':
		// '$' is a NormalChar in XSD (so "\$" is not a SingleCharEsc there)
		// but a metacharacter in F&O, where "\$" IS one (re00990/991).
		if !g.xpathExt() {
			return 0, fmt.Errorf("\\$ is not a valid XSD escape")
		}
	case 'p', 'P':
		return 0, fmt.Errorf("\\%c requires a {…} property", n)
	default:
		return 0, fmt.Errorf("\\%c is not a valid XSD escape", n)
	}
	return j + 2, nil
}

// xsdCategories is the closed vocabulary of catEsc general categories
// (App. F charProp); Cn (unassigned) is a legal XSD category even where the
// host regex engine lacks it.
var xsdCategories = map[string]bool{
	"L": true, "Lu": true, "Ll": true, "Lt": true, "Lm": true, "Lo": true,
	"M": true, "Mn": true, "Mc": true, "Me": true,
	"N": true, "Nd": true, "Nl": true, "No": true,
	"P": true, "Pc": true, "Pd": true, "Ps": true, "Pe": true, "Pi": true, "Pf": true, "Po": true,
	"Z": true, "Zs": true, "Zl": true, "Zp": true,
	"S": true, "Sm": true, "Sc": true, "Sk": true, "So": true,
	"C": true, "Cc": true, "Cf": true, "Co": true, "Cn": true,
}

// validCharProp checks a \\p{…} body: a known general category, or
// Is<BlockName> where the block name is grammatically legal AND names a real
// Unicode block (reK86 \\p{Is}, reK88 \\p{IsaA0-a9}, reK82 \\p{\\L}).
func validCharProp(body string, g RegexGrammar) error {
	if strings.HasPrefix(body, "Is") {
		name := body[2:]
		if name == "" {
			return fmt.Errorf("\\p{Is}: missing block name")
		}
		for i := 0; i < len(name); i++ {
			ch := name[i]
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return fmt.Errorf("\\p{%s}: invalid block name", body)
			}
		}
		// An UNKNOWN block name is an error in XSD 1.0; 1.1 tolerates it —
		// the block then matches EVERY character (reK88, bug 13670).
		if g == GrammarXSD10 && !XSDBlockKnown(name) {
			return fmt.Errorf("\\p{%s}: unknown block", body)
		}
		return nil
	}
	if !xsdCategories[body] {
		return fmt.Errorf("\\p{%s}: unknown category", body)
	}
	return nil
}

// validateCharClass validates the XSD character-class expression starting at
// pat[start]=='[' and returns the index of its closing ']'. It enforces the parts
// of the XSD grammar RE2 is lax about: no bare '[', no chained ranges
// (e.g. [a-c-1-4]), and a '-' only as a range separator, a leading/trailing
// literal, or the '-[…]' class-subtraction operator. The chained-range rule is
// XSD 1.0-only: 1.1's rewritten grammar (App. G, charGroupPart) reads a '-'
// with no usable left endpoint as a literal, so [a-c-1-4] is valid there.
func validateCharClass(pat string, start int, g RegexGrammar) (int, error) {
	j := start + 1
	if j < len(pat) && pat[j] == '^' {
		j++
	}
	first, prevRange := true, false
	for j < len(pat) {
		c := pat[j]
		if c == ']' {
			if first {
				return 0, fmt.Errorf("empty character class")
			}
			return j, nil
		}
		if c == '[' {
			return 0, fmt.Errorf("unescaped '[' in character class")
		}
		if c == '-' {
			if j+1 < len(pat) && pat[j+1] == '[' { // class subtraction, must be last
				if first {
					// charClassSub requires a non-empty (pos|neg)CharGroup
					// before the subtraction (RegexTest_469/470).
					return 0, fmt.Errorf("class subtraction requires a non-empty base")
				}
				end, err := validateCharClass(pat, j+1, g)
				if err != nil {
					return 0, err
				}
				if end+1 >= len(pat) || pat[end+1] != ']' {
					return 0, fmt.Errorf("class subtraction must be last")
				}
				return end + 1, nil
			}
			// A bare '-' immediately after a completed range is only a legal
			// literal under strict XSD 1.0 when it is the class's own
			// TRAILING hyphen (immediately closing the class, e.g. [a-z-]) or
			// the base group's trailing literal right before a class-
			// subtraction operator (reF56: [a-z--[b-z]] is (a-z or '-') minus
			// [b-z] — the FIRST of the two hyphens is this trailing literal,
			// the second is '-[…]') — the formal grammar's XmlCharIncDash
			// excludes '-' from ever standing alone as an ordinary class
			// member, so any OTHER bare hyphen there — whether followed by
			// another complete range ([a-c-1-4], the "chained range" shape)
			// or by just one more trailing literal before ']' ([0-9-.],
			// K2-MatchesFunc-16a) — is invalid. XSD 1.1's rewritten
			// charGroupPart grammar reads it as a literal either way, so
			// this is strictClasses()-gated.
			trailingHyphen := j+1 < len(pat) && pat[j+1] == ']'
			subtractionLeadIn := j+2 < len(pat) && pat[j+1] == '-' && pat[j+2] == '['
			if g.strictClasses() && prevRange && !trailingHyphen && !subtractionLeadIn {
				return 0, fmt.Errorf("chained range in character class")
			}
			// An unescaped '-' may not be a range's LEFT endpoint (both
			// versions): under the greedy grammar '-' directly followed by
			// '-atom' reads as a range whose left endpoint is the bare '-'
			// (simple041 [--z]); '-' before ']' or '-[' stays a literal.
			if j+2 < len(pat) && pat[j+1] == '-' && pat[j+2] != '[' && pat[j+2] != ']' {
				return 0, fmt.Errorf("unescaped '-' as range endpoint")
			}
			j++
			first, prevRange = false, false
			continue
		}
		aEnd, err := classAtomEnd(pat, j, g)
		if err != nil {
			return 0, err
		}
		first = false
		if aEnd < len(pat) && pat[aEnd] == '-' &&
			!(aEnd+1 < len(pat) && (pat[aEnd+1] == '[' || pat[aEnd+1] == ']')) {
			if aEnd+1 >= len(pat) {
				return 0, fmt.Errorf("unclosed character class")
			}
			// Nor may an unescaped '-' be the RIGHT endpoint (simple042 [!--],
			// reF57 [a--b]); the escaped form \- remains a valid endpoint.
			if pat[aEnd+1] == '-' {
				return 0, fmt.Errorf("unescaped '-' as range endpoint")
			}
			bEnd, err := classAtomEnd(pat, aEnd+1, g) // a range: atom '-' atom
			if err != nil {
				return 0, err
			}
			// seRange ::= charOrEsc '-' charOrEsc: each endpoint must denote a
			// SINGLE character — a MultiCharEsc or catEsc endpoint is illegal
			// ([a-\d], RegexTest_43-50) — and the range may not be reversed
			// ([a-;] reG37, [a-\\] reG34).
			lo, lok := rangeEndpointRune(pat, j, aEnd)
			hi, hok := rangeEndpointRune(pat, aEnd+1, bEnd)
			if !lok || !hok {
				return 0, fmt.Errorf("a multi-character escape cannot be a range endpoint")
			}
			if lo > hi {
				return 0, fmt.Errorf("reversed character range")
			}
			j, prevRange = bEnd, true
			continue
		}
		j, prevRange = aEnd, false
	}
	return 0, fmt.Errorf("unclosed character class")
}

// rangeEndpointRune decodes the single character a range endpoint denotes:
// a literal rune, or a SingleCharEsc. Multi-character escapes (\\d, \\s,
// \\p{…} …) denote SETS and return ok=false.
func rangeEndpointRune(pat string, j, end int) (rune, bool) {
	if pat[j] != '\\' {
		r, _ := utf8.DecodeRuneInString(pat[j:end])
		return r, true
	}
	if end-j != 2 {
		return 0, false // \\p{…} or malformed
	}
	switch n := pat[j+1]; n {
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	case '\\', '|', '.', '?', '*', '+', '(', ')', '{', '}', '-', '[', ']', '^', '$':
		// '\$' only reaches here under GrammarXPath: classAtomEnd already
		// rejects it for the XSD grammars, so accepting it as an endpoint
		// here cannot loosen those.
		return rune(n), true
	default:
		return 0, false // MultiCharEsc
	}
}


// validQuantBody reports whether s is the body of a well-formed XSD quantifier:
// "n", "n,", or "n,m" with n,m sequences of digits.
func validQuantBody(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.SplitN(s, ",", 2)
	if !allDigits(parts[0]) {
		return false
	}
	if len(parts) == 2 && parts[1] != "" {
		if !allDigits(parts[1]) {
			return false
		}
		// quantRange: n must not exceed m (reB79 a{2,1}, RegexTest_999
		// a{37,17}). Compared as unbounded naturals: strip leading zeros,
		// longer wins, else lexicographic.
		n := strings.TrimLeft(parts[0], "0")
		m := strings.TrimLeft(parts[1], "0")
		if len(n) > len(m) || (len(n) == len(m) && n > m) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// stripRegexWSOutsideClasses removes the whitespace the 'x' flag ignores:
// #x9, #xA, #xD and #x20 outside a character-class expression (F&O 3.1
// §5.6.2 keeps whitespace INSIDE a class significant). This is a raw TEXTUAL
// pass, deliberately not escape-aware about whitespace itself: the spec's own
// worked example strips the space out of "hello\ sworld" to get "hello\sworld"
// (turning it into a \s escape), so a backslash does NOT protect a following
// whitespace character from removal (K2-MatchesFunc-1) — it only means the
// character AFTER a backslash never toggles class-bracket tracking on its own
// (so an escaped "\[" or "\]" does not confuse inClass).
func stripRegexWSOutsideClasses(p string) string {
	var b strings.Builder
	rs := []rune(p)
	inClass := false
	escaped := false
	isWS := func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }
	for _, r := range rs {
		if escaped {
			escaped = false
			if !inClass && isWS(r) {
				continue // the space in "\ " is still stripped; \s survives
			}
			b.WriteRune(r)
			continue
		}
		switch r {
		case '\\':
			escaped = true
			b.WriteRune(r)
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		}
		if !inClass && isWS(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
