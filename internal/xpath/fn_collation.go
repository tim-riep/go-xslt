package xpath

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const (
	collCodepoint = "http://www.w3.org/2005/xpath-functions/collation/codepoint"
	collUCA       = "http://www.w3.org/2013/collation/UCA"
	collHTMLCase  = "http://www.w3.org/2005/xpath-functions/collation/html-ascii-case-insensitive"
	// The QT3 catalog's case-blind collation (fn-function-lookup-800): the
	// engine may support any collation URI, and this one is case-insensitive
	// comparison — the same as html-ascii-case-insensitive for our purposes.
	collFOTSCaseBlind = "http://www.w3.org/2010/09/qt-fots-catalog/collation/caseblind"
)

// collValues lists the permitted values of each recognized UCA collation
// keyword; a value outside this set (with fallback=no) is an error.
var collValues = map[string]map[string]bool{
	"fallback":      {"yes": true, "no": true},
	"strength":      {"primary": true, "secondary": true, "tertiary": true, "quaternary": true, "identical": true, "1": true, "2": true, "3": true, "4": true, "5": true},
	"maxVariable":   {"space": true, "punct": true, "symbol": true, "currency": true},
	"alternate":     {"non-ignorable": true, "shifted": true, "shift-trimmed": true, "blanked": true},
	"backwards":     {"yes": true, "no": true},
	"normalization": {"yes": true, "no": true},
	"caseLevel":     {"yes": true, "no": true},
	"caseFirst":     {"upper": true, "lower": true},
	"numeric":       {"yes": true, "no": true},
}

// resolveCollator returns a string comparison function for a collation URI. An
// unsupported collation (or, under fallback=no, an unrecognized keyword/value)
// raises err:FOCH0002.
func resolveCollator(uri string) (func(a, b string) int, error) {
	uri = strings.TrimSpace(uri)
	switch uri {
	case "", collCodepoint:
		return strings.Compare, nil
	case collHTMLCase:
		// Only the ASCII letters fold: "Á" and "á" stay distinct (compare-016).
		return func(a, b string) int { return strings.Compare(asciiFold(a), asciiFold(b)) }, nil
	case collFOTSCaseBlind:
		return func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }, nil
	}
	base, query, _ := strings.Cut(uri, "?")
	if base != collUCA {
		return nil, fmt.Errorf("err:FOCH0002: unsupported collation %q", uri)
	}

	params := map[string]string{}
	for _, kv := range strings.Split(query, ";") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		params[k] = v
	}
	fallback := true
	if v, ok := params["fallback"]; ok {
		if v != "yes" && v != "no" {
			return nil, fmt.Errorf("err:FOCH0002: invalid fallback %q", v)
		}
		fallback = v != "no"
	}

	tag := language.Und
	var opts []collate.Option
	// BCP 47 "u" extension keys the collator reads from the tag: ks (strength
	// levels above tertiary, which the Option API cannot express).
	ext := map[string]string{}
	alternate, level4, identical := "", false, false
	for k, v := range params {
		if k == "fallback" {
			continue
		}
		if k == "lang" {
			if t, err := language.Parse(v); err == nil {
				tag = t
			} else if !fallback {
				return nil, fmt.Errorf("err:FOCH0002: invalid lang %q", v)
			}
			continue
		}
		if k == "version" {
			// Accepted, not applied — but under fallback=no a UCA version we
			// cannot possibly implement (96.5) is FOCH0002 (UCA-collation-024);
			// with fallback the unknown/odd versions are tolerated
			// (UCA-collation-022a/023).
			if !fallback && !collVersionPlausible(v) {
				return nil, fmt.Errorf("err:FOCH0002: unsupported UCA version %q", v)
			}
			continue
		}
		if k == "reorder" { // accepted, not applied
			continue
		}
		allowed, known := collValues[k]
		if !known || !allowed[v] {
			if !fallback {
				return nil, fmt.Errorf("err:FOCH0002: unsupported collation keyword %s=%s", k, v)
			}
			continue
		}
		switch k {
		case "strength":
			switch v {
			case "primary", "1":
				opts = append(opts, collate.Loose)
			case "secondary", "2":
				opts = append(opts, collate.IgnoreCase, collate.IgnoreWidth)
			case "quaternary", "4":
				ext["ks"] = "level4"
				level4 = true
			case "identical", "5":
				ext["ks"] = "identic"
				level4, identical = true, true
			}
		case "alternate":
			if v != "non-ignorable" {
				alternate = v
			}
		case "numeric":
			if v == "yes" {
				opts = append(opts, collate.Numeric)
			}
		case "caseFirst":
			// No collate.Option expresses case ordering; the collator reads it
			// from the tag's BCP 47 "kf" extension.
			ext["kf"] = v
		}
	}
	for k, v := range ext {
		if t, err := tag.SetTypeForKey(k, v); err == nil {
			tag = t
		}
	}
	col := collate.New(tag, opts...)
	if alternate == "" {
		return func(a, b string) int { return col.CompareString(a, b) }, nil
	}
	// alternate=blanked|shifted|shift-trimmed: the "variable" characters —
	// the maxVariable group and every group before it — are ignorable below
	// the quaternary level (fo-test-fn-contains-004: "-d-e-f-" ⊂ "abcdefghi";
	// UCA-maxVariable-001..016). x/text's collator has a fixed variable top,
	// so the variables are stripped here; under shifted at quaternary/
	// identical strength they then decide an otherwise-equal comparison by
	// position and group (UCA-params-014, UCA-maxVariable-004/009).
	variable := collVariablePred(params["maxVariable"])
	strip := func(s string) string {
		return strings.Map(func(r rune) rune {
			if variable(r) {
				return -1
			}
			return r
		}, s)
	}
	quaternary := level4 && alternate != "blanked"
	return func(a, b string) int {
		if c := col.CompareString(strip(a), strip(b)); c != 0 {
			return c
		}
		if quaternary {
			if c := collCompareVariables(a, b, variable); c != 0 {
				return c
			}
		}
		if identical {
			return strings.Compare(a, b)
		}
		return 0
	}, nil
}

// collVariableGroup classifies a rune into the UCA reordering groups that the
// maxVariable parameter ranges over: 1 space, 2 punct, 3 symbol, 4 currency;
// 0 for everything else.
func collVariableGroup(r rune) int {
	switch {
	case unicode.IsSpace(r) || unicode.Is(unicode.Z, r):
		return 1
	case unicode.IsPunct(r):
		return 2
	case unicode.Is(unicode.Sc, r):
		return 4
	case unicode.IsSymbol(r):
		return 3
	}
	return 0
}

// collVariablePred returns the predicate for "variable" characters under a
// maxVariable setting (default punct: spaces and punctuation).
func collVariablePred(maxVariable string) func(rune) bool {
	max := 2
	switch maxVariable {
	case "space":
		max = 1
	case "symbol":
		max = 3
	case "currency":
		max = 4
	}
	return func(r rune) bool {
		g := collVariableGroup(r)
		return g > 0 && g <= max
	}
}

// collCompareVariables is the quaternary level of alternate=shifted: every
// non-variable character weighs the maximum, a variable character weighs by
// its group then code point, and the weight sequences compare lexicographically
// — so "data base" < "data-base" < "database".
func collCompareVariables(a, b string, variable func(rune) bool) int {
	weight := func(r rune) int {
		if variable(r) {
			return collVariableGroup(r)<<21 | int(r)
		}
		return 1 << 30
	}
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if wa, wb := weight(ra[i]), weight(rb[i]); wa != wb {
			if wa < wb {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ra) < len(rb):
		return -1
	case len(ra) > len(rb):
		return 1
	}
	return 0
}

// strMatcher performs the collation-aware substring matching of fn:contains,
// starts-with, ends-with, substring-before and substring-after (F&O 3.1
// §5.4.1: the match is on collation units, so characters the collation
// ignores are neither part of the match nor an obstacle to it). A nil matcher
// is the default codepoint collation.
type strMatcher struct {
	fold bool                  // html-ascii-case-insensitive: ASCII case fold
	cmp  func(a, b string) int // any other collation
}

// strMatcherArg resolves the optional $collation argument at index i (an
// absent argument is the codepoint collation; an unknown URI is FOCH0002). A
// relative URI is resolved against the static base URI (fn-substring-before-23:
// "collation/codepoint" under http://www.w3.org/2005/xpath-functions/).
func strMatcherArg(c *Context, a []Object, i int) (*strMatcher, error) {
	if len(a) <= i {
		if coll := ctxCollation(c); coll != nil {
			return &strMatcher{cmp: coll}, nil
		}
		return nil, nil
	}
	items := Items(a[i])
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: the collation argument must be a single xs:string")
	}
	uri := strings.TrimSpace(itemString(items[0]))
	if c != nil && c.BaseURI != "" && uri != "" {
		if u, err := url.Parse(uri); err == nil && !u.IsAbs() {
			uri = resolveAgainstBase(c.BaseURI, uri)
		}
	}
	switch uri {
	case "", collCodepoint:
		return nil, nil
	case collHTMLCase, collFOTSCaseBlind:
		return &strMatcher{fold: true}, nil
	}
	cmp, err := resolveCollator(uri)
	if err != nil {
		return nil, err
	}
	return &strMatcher{cmp: cmp}, nil
}

// asciiFold lowers the ASCII letters of s and nothing else, so byte offsets
// into the folded string are valid in s.
func asciiFold(s string) string {
	i := 0
	for i < len(s) && (s[i] < 'A' || s[i] > 'Z') {
		i++
	}
	if i == len(s) {
		return s
	}
	b := []byte(s)
	for ; i < len(b); i++ {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// find locates sub within s and returns the byte range of the match; under a
// real collation the range excludes leading and trailing ignorable
// characters (substring-before("abc--d-e-fghi", "--d-e-") is "abc--"). A sub
// with no collation units matches the empty range at the start of s.
func (m *strMatcher) find(s, sub string) (start, end int, ok bool) {
	switch {
	case m == nil:
		i := strings.Index(s, sub)
		return i, i + len(sub), i >= 0
	case m.fold:
		i := strings.Index(asciiFold(s), asciiFold(sub))
		return i, i + len(sub), i >= 0
	}
	if m.cmp("", sub) == 0 {
		return 0, 0, true
	}
	// Rune boundaries of s; a span is tried from every non-ignorable start
	// and ends at the shortest boundary that compares equal to sub.
	bounds := make([]int, 0, len(s)+1)
	for i := range s {
		bounds = append(bounds, i)
	}
	bounds = append(bounds, len(s))
	for ai := 0; ai+1 < len(bounds); ai++ {
		a := bounds[ai]
		if m.cmp(s[a:bounds[ai+1]], "") == 0 {
			continue
		}
		for bi := ai + 1; bi < len(bounds); bi++ {
			if m.cmp(s[a:bounds[bi]], sub) == 0 {
				return a, bounds[bi], true
			}
		}
	}
	return 0, 0, false
}

// hasPrefix reports whether s starts with sub (some prefix of s, taken at a
// rune boundary, compares equal to sub).
func (m *strMatcher) hasPrefix(s, sub string) bool {
	switch {
	case m == nil:
		return strings.HasPrefix(s, sub)
	case m.fold:
		return strings.HasPrefix(asciiFold(s), asciiFold(sub))
	}
	if m.cmp("", sub) == 0 {
		return true
	}
	for i := range s {
		if i > 0 && m.cmp(s[:i], sub) == 0 {
			return true
		}
	}
	return m.cmp(s, sub) == 0
}

// hasSuffix reports whether s ends with sub (some suffix of s, taken at a
// rune boundary, compares equal to sub).
func (m *strMatcher) hasSuffix(s, sub string) bool {
	switch {
	case m == nil:
		return strings.HasSuffix(s, sub)
	case m.fold:
		return strings.HasSuffix(asciiFold(s), asciiFold(sub))
	}
	if m.cmp("", sub) == 0 {
		return true
	}
	for i := range s {
		if m.cmp(s[i:], sub) == 0 {
			return true
		}
	}
	return false
}

// collationArg resolves an explicit $collation argument of a sequence function
// (index-of, deep-equal). The parameter is xs:string, not xs:string?, so the
// empty sequence is XPTY0004 (K-SeqIndexOfFunc-3) and an unknown URI is
// FOCH0002 (K-SeqIndexOfFunc-4). A nil comparator means the default codepoint
// collation, for which callers keep their plain comparison.
// collVersionPlausible reports whether a UCA "version" parameter names a
// version that could exist: "major[.minor[.patch]]" with a major number in
// the range the Unicode Collation Algorithm has been published for.
func collVersionPlausible(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	if major == "" || len(major) > 2 {
		return false
	}
	n := 0
	for _, r := range major {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n >= 1 && n <= 20
}

// ResolveCollator returns a string comparison function for a collation URI
// ("" = the default codepoint collation), for hosts (the XSLT engine's
// xsl:for-each-group/@collation) that need to resolve a collation outside the
// fn: function-call path. See resolveCollator for the supported URI set.
func ResolveCollator(uri string) (func(a, b string) int, error) {
	return resolveCollator(uri)
}

// ResolveCollationKeyer returns a canonical-key function for a collation URI
// ("" = the default codepoint collation), for hosts (the XSLT engine's
// xsl:key/@collation) that need a string form two values compare equal under
// iff their canonical forms are byte-identical — the shape a hash-map index
// needs, unlike the pairwise ResolveCollator comparator.
func ResolveCollationKeyer(uri string) (func(string) []byte, error) {
	return resolveCollationKeyer(uri)
}

// CollatorForLang returns the collation an xsl:sort or xsl:merge-key with a
// @lang but no @collation asks for: XSLT 3.0 §13.1.3 leaves the choice to the
// processor, and a UCA collator tailored to that language is the only sensible
// one — Swedish sorts å/ä/ö AFTER z, which no untailored comparison does
// (merge-076/078/…/084 merge files pre-sorted in Swedish order and would
// otherwise all be rejected as unsorted). caseOrder is XSLT's own
// "upper-first"/"lower-first" spelling, or "" for the collation's own default.
//
// An unusable language tag returns nil — the codepoint comparison every caller
// used before @lang was honoured at all — rather than an error: @lang is a
// preference, not a demand for a specific collation the way @collation is.
func CollatorForLang(lang, caseOrder string) func(a, b string) int {
	if strings.TrimSpace(lang) == "" {
		return nil
	}
	coll, err := resolveCollator(collUCA + "?fallback=yes;lang=" + strings.TrimSpace(lang))
	if err != nil {
		return nil
	}
	if caseOrder != "upper-first" && caseOrder != "lower-first" {
		return coll
	}
	// case-order breaks a tie the collation itself leaves, rather than
	// replacing it: the caller's own case-order handling would otherwise have
	// to be skipped entirely to let the tailored collation apply at all.
	return func(a, b string) int {
		if c := coll(a, b); c != 0 {
			return c
		}
		c := strings.Compare(a, b)
		if caseOrder == "lower-first" {
			return -c
		}
		return c
	}
}

func collationArg(o Object) (func(a, b string) int, error) {
	items := Items(o)
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: the collation argument must be a single xs:string")
	}
	uri := strings.TrimSpace(itemString(items[0]))
	if uri == "" || uri == collCodepoint {
		return nil, nil
	}
	return resolveCollator(uri)
}

// resolveCollationKeyer returns a sort-key function for a collation URI: two
// strings compare equal under the collation iff their keys are byte-equal
// (fn:collation-key).
func resolveCollationKeyer(uri string) (func(string) []byte, error) {
	uri = strings.TrimSpace(uri)
	switch uri {
	case "", collCodepoint:
		return func(s string) []byte { return []byte(s) }, nil
	case collHTMLCase:
		return func(s string) []byte { return []byte(asciiFold(s)) }, nil
	}
	// Validate parameters through the comparator path, then rebuild the
	// collator for key extraction (same parsing, same FOCH0002 behavior).
	if _, err := resolveCollator(uri); err != nil {
		return nil, err
	}
	base, query, _ := strings.Cut(uri, "?")
	_ = base
	tag := language.Und
	var opts []collate.Option
	for _, kv := range strings.Split(query, ";") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "lang":
			if t, err := language.Parse(v); err == nil {
				tag = t
			}
		case "strength":
			switch v {
			case "primary", "1":
				opts = append(opts, collate.IgnoreCase, collate.IgnoreWidth, collate.IgnoreDiacritics)
			case "secondary", "2":
				opts = append(opts, collate.IgnoreCase, collate.IgnoreWidth)
			}
		case "numeric":
			if v == "yes" {
				opts = append(opts, collate.Numeric)
			}
		}
	}
	col := collate.New(tag, opts...)
	var buf collate.Buffer
	return func(s string) []byte {
		k := col.KeyFromString(&buf, s)
		out := append([]byte{}, k...)
		buf.Reset()
		return out
	}, nil
}
