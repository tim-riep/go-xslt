package xpath

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

func init() {
	coreFuncs["format-number"] = numFormatNumber
}

// DecimalFormat holds the symbols of an xsl:decimal-format (or the default).
// The host (XSLT engine) registers named formats in Context.DecimalFormats.
type DecimalFormat struct {
	DecimalSep  rune
	GroupingSep rune
	Percent     rune
	PerMille    rune
	ZeroDigit   rune
	Digit       rune
	PatternSep  rune
	MinusSign   rune
	Infinity    string
	NaN         string
	// ExponentSep is the exponent-separator-sign a picture's 'e'/'E' must
	// match to be read as active rather than a passive literal
	// (numberformat102/format-number-069). The zero value (never explicitly
	// set) is treated as 'e' by expSepIndex's caller normalizing it — see
	// normalizedExponentSep — so every construction site that predates this
	// field (a named xsl:decimal-format built without it, say) still gets
	// the correct default instead of silently matching NO character at all.
	ExponentSep rune
}

// DefaultDecimalFormat returns the unnamed decimal-format's default symbols.
func DefaultDecimalFormat() DecimalFormat {
	return DecimalFormat{
		DecimalSep: '.', GroupingSep: ',', Percent: '%', PerMille: '‰',
		ZeroDigit: '0', Digit: '#', PatternSep: ';', MinusSign: '-',
		Infinity: "Infinity", NaN: "NaN", ExponentSep: 'e',
	}
}

// normalizedExponentSep returns df's exponent-separator, defaulting to 'e'
// for a DecimalFormat value constructed without this field (zero rune) —
// e.g. a host's pre-existing named-format construction site that predates
// it — so an unset ExponentSep never silently means "no picture can ever
// use scientific notation" (matching NO rune at all).
func normalizedExponentSep(df DecimalFormat) rune {
	if df.ExponentSep == 0 {
		return 'e'
	}
	return df.ExponentSep
}

// numFormatNumber implements fn:format-number($value, $picture [, $df-name]).
// The optional third argument names an xsl:decimal-format registered by the
// host in Context.DecimalFormats; an unknown NAMED format is FODF1280.
func numFormatNumber(c *Context, a []Object) (Object, error) {
	// $picture is xs:string: a numeric picture is a type error
	// (numberformat907InputErr), not a picture to be stringified.
	if pa := numFirstAtomic(arg(a, 1)); pa != nil && !isStringType(pa.T) &&
		pa.T != XSuntypedAtomic && pa.T != XSanyURI {
		return nil, fmt.Errorf("err:XPTY0004: format-number picture must be a string, not %s", pa.T)
	}
	df := DefaultDecimalFormat()
	name := ""
	if len(a) > 2 {
		// The decimal-format name is a QName resolved in the static context
		// (leading/trailing whitespace stripped) and keyed by its expanded
		// {uri}local form — so an alias prefix or a Q{uri}local literal reaches
		// the same registered format (numberformat81/87).
		name = expandDFName(strings.TrimSpace(ToString(a[2])), c)
	}
	if c != nil && c.DecimalFormats != nil {
		if d, ok := c.DecimalFormats[name]; ok {
			df = d
		} else if name != "" {
			return nil, fmt.Errorf("err:FODF1280: no decimal format named %q", name)
		}
	} else if name != "" {
		return nil, fmt.Errorf("err:FODF1280: no decimal format named %q", name)
	}
	picture := ToString(arg(a, 1))
	if err := validatePicture(picture, df); err != nil {
		return nil, err
	}
	return formatNumberValue(fmtNumInput(arg(a, 0)), picture, df), nil
}

// expandDFName resolves a decimal-format name (a lexical QName, a Q{uri}local
// literal, or an unprefixed NCName) to its {uri}local expanded form using the
// static namespace context. The unnamed default stays "".
func expandDFName(name string, c *Context) string {
	if name == "" {
		return ""
	}
	clark := func(uri, local string) string {
		if uri == "" {
			return local
		}
		return "{" + uri + "}" + local
	}
	if strings.HasPrefix(name, "Q{") {
		if end := strings.IndexByte(name, '}'); end > 0 {
			return clark(strings.TrimSpace(name[2:end]), name[end+1:])
		}
	}
	if i := strings.IndexByte(name, ':'); i >= 0 {
		pre, local := name[:i], name[i+1:]
		uri := ""
		if n, ok := nsForKnownPrefix(pre); ok {
			uri = n
		} else if c != nil && c.NS != nil {
			if n, ok := c.NS.ResolveNS(pre); ok {
				uri = n
			}
		}
		return clark(uri, local)
	}
	return name
}

// fmtNum is a format-number input: an exact non-negative magnitude with its
// sign, or a non-finite double. Doubles are converted to the decimal with the
// fewest digits that round-trips (F&O §4.7.5), so 1E25 formats as 1 followed by
// 25 zeros rather than the binary expansion 10000000000000000905969664.
type fmtNum struct {
	abs      *big.Rat // magnitude; nil when nan or inf
	neg      bool
	nan, inf bool
	isFloat  bool    // xs:double/xs:float input: %/‰ scaling happens in float arithmetic
	f        float64 // the double magnitude when isFloat
	bits     int     // 64 (double) or 32 (float) when isFloat
}

// fmtNumFromFloat converts a double (bits=64) or float (bits=32) value.
func fmtNumFromFloat(f float64, bits int) fmtNum {
	switch {
	case math.IsNaN(f):
		return fmtNum{nan: true}
	case math.IsInf(f, 0):
		return fmtNum{inf: true, neg: f < 0}
	}
	n := fmtNum{neg: math.Signbit(f), isFloat: true, f: math.Abs(f), bits: bits}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(n.f, 'e', -1, bits))
	if !ok {
		r = new(big.Rat)
	}
	n.abs = r
	return n
}

// fmtNumInput derives the format-number input from the $value argument:
// integers and decimals exactly, doubles/floats by shortest representation,
// anything else (untyped nodes, strings) through ToNumber; empty is NaN.
func fmtNumInput(o Object) fmtNum {
	if numIsEmpty(o) {
		return fmtNum{nan: true}
	}
	if items, err := Atomize(o); err == nil && len(items) > 0 {
		if at, ok := items[0].(*Atomic); ok {
			switch {
			case isIntegerType(at.T) && at.i != nil:
				return fmtNum{abs: new(big.Rat).SetInt(new(big.Int).Abs(at.i)), neg: at.i.Sign() < 0}
			case at.T == XSdecimal && at.d != nil:
				return fmtNum{abs: new(big.Rat).Abs(at.d), neg: at.d.Sign() < 0}
			case at.T == XSfloat:
				return fmtNumFromFloat(at.f, 32)
			case at.T == XSdouble:
				return fmtNumFromFloat(at.f, 64)
			}
		}
	}
	return fmtNumFromFloat(ToNumber(o), 64)
}

// validatePicture rejects the clearly-malformed format-number pictures the spec
// flags as FODF1310: more than two sub-pictures, or a sub-picture with no digit
// sign, more than one decimal separator, or both percent and per-mille.
func validatePicture(picture string, df DecimalFormat) error {
	if strings.Count(picture, string(df.PatternSep)) > 1 {
		return fmt.Errorf("err:FODF1310: too many sub-pictures in %q", picture)
	}
	for _, sub := range splitOnRune(picture, df.PatternSep) {
		hasDigit, decSeps := false, 0
		hasPct, hasPerMille := false, false
		for _, r := range sub {
			switch {
			case (r >= df.ZeroDigit && r <= df.ZeroDigit+9) || r == df.Digit:
				hasDigit = true
			case r == df.DecimalSep:
				decSeps++
			case r == df.Percent:
				hasPct = true
			case r == df.PerMille:
				hasPerMille = true
			}
		}
		if !hasDigit {
			return fmt.Errorf("err:FODF1310: sub-picture has no digit sign in %q", picture)
		}
		if decSeps > 1 {
			return fmt.Errorf("err:FODF1310: multiple decimal separators in %q", picture)
		}
		if hasPct && hasPerMille {
			return fmt.Errorf("err:FODF1310: both percent and per-mille in %q", picture)
		}
		if strings.Count(sub, string(df.Percent)) > 1 || strings.Count(sub, string(df.PerMille)) > 1 {
			return fmt.Errorf("err:FODF1310: multiple percent/per-mille signs in %q", picture)
		}
		if err := validateSubPictureShape(sub, df, picture); err != nil {
			return err
		}
	}
	return nil
}

// expSepIndex returns the index of the rune treated as the exponent-separator-
// sign — a character matching the active decimal-format's exponent-separator
// property that is both preceded and followed, anywhere in the sub-picture,
// by an active character (F&O §4.7.3) — plus how many candidates qualified;
// -1 when none does. expSep is df.ExponentSep ('e' unless an xsl:decimal-
// format/declare decimal-format overrides it — numberformat102: 'E' does NOT
// match the unnamed format's default 'e', so it is a passive character
// there, not an exponent-separator-sign; format-number-069's own
// exponent-separator="E" declaration is what makes 'E' active for it).
func expSepIndex(runes []rune, isActive func(rune) bool, expSep rune) (idx, count int) {
	idx = -1
	for i, r := range runes {
		if r != expSep {
			continue
		}
		before, after := false, false
		for _, q := range runes[:i] {
			if isActive(q) {
				before = true
				break
			}
		}
		for _, q := range runes[i+1:] {
			if isActive(q) {
				after = true
				break
			}
		}
		if before && after {
			if idx < 0 {
				idx = i
			}
			count++
		}
	}
	return idx, count
}

// validateSubPictureShape enforces the FODF1310 structural rules of one
// sub-picture (F&O §4.7.3): at most one exponent-separator-sign, none next to
// a percent/per-mille, an exponent part of digits only; no passive character
// between active characters; a digit sign in the mantissa; no grouping
// separator adjacent to another one or to the decimal separator, nor ending
// the integer part; optional digits before mandatory in the integer part and
// mandatory before optional in the fractional part.
func validateSubPictureShape(sub string, df DecimalFormat, picture string) error {
	runes := []rune(sub)
	isDigit := func(r rune) bool { return r >= df.ZeroDigit && r <= df.ZeroDigit+9 }
	isDigitSign := func(r rune) bool { return isDigit(r) || r == df.Digit }
	isActive := func(r rune) bool { return isDigitSign(r) || r == df.DecimalSep || r == df.GroupingSep }
	expIdx, expCount := expSepIndex(runes, isActive, normalizedExponentSep(df))
	if expCount > 1 {
		return fmt.Errorf("err:FODF1310: more than one exponent separator in %q", picture)
	}
	active := func(i int) bool { return isActive(runes[i]) || i == expIdx }
	// A passive character between two active characters is invalid (e.g. 0$0).
	lastActive := -1
	sawPassiveSince := false
	for i := range runes {
		if active(i) {
			if lastActive >= 0 && sawPassiveSince {
				return fmt.Errorf("err:FODF1310: passive character inside the number in %q", picture)
			}
			lastActive = i
			sawPassiveSince = false
			continue
		}
		if lastActive >= 0 {
			sawPassiveSince = true
		}
	}
	// The mantissa/exponent boundary: the ordering rules below govern only the
	// mantissa; the exponent part has its own digit-signs and must not be read
	// as extra fractional digits (numberformat132/239/243…).
	mantEnd := len(runes)
	if expIdx >= 0 {
		mantEnd = expIdx
		if strings.ContainsRune(sub, df.Percent) || strings.ContainsRune(sub, df.PerMille) {
			return fmt.Errorf("err:FODF1310: percent or per-mille with an exponent in %q", picture)
		}
		expDigits := 0
		for _, r := range runes[expIdx+1:] {
			if isDigit(r) {
				expDigits++
			} else if isActive(r) {
				return fmt.Errorf("err:FODF1310: exponent part must contain only digits in %q", picture)
			}
		}
		if expDigits == 0 {
			return fmt.Errorf("err:FODF1310: exponent separator without exponent digits in %q", picture)
		}
	}
	hasMantissaDigit := false
	for _, r := range runes[:mantEnd] {
		if isDigitSign(r) {
			hasMantissaDigit = true
		}
	}
	if !hasMantissaDigit {
		return fmt.Errorf("err:FODF1310: mantissa has no digit sign in %q", picture)
	}
	// Grouping separator rules and digit ordering.
	dec := -1
	for i := 0; i < mantEnd; i++ {
		if runes[i] == df.DecimalSep {
			dec = i
			break
		}
	}
	inFrac := false
	seenZeroInt := false
	seenOptFrac := false
	for i := 0; i < mantEnd; i++ {
		r := runes[i]
		if r == df.DecimalSep {
			inFrac = true
			continue
		}
		switch {
		case r == df.GroupingSep:
			if i+1 < len(runes) && runes[i+1] == df.GroupingSep {
				return fmt.Errorf("err:FODF1310: adjacent grouping separators in %q", picture)
			}
			if dec >= 0 && (i == dec-1 || i == dec+1) {
				return fmt.Errorf("err:FODF1310: grouping separator adjacent to decimal separator in %q", picture)
			}
			if !inFrac {
				// A leading separator is fine (',##0' groups by three,
				// numberformat320); one with no digit sign after it in the
				// integer part is not.
				intEnd := mantEnd
				if dec >= 0 {
					intEnd = dec
				}
				followed := false
				for j := i + 1; j < intEnd; j++ {
					if isDigitSign(runes[j]) {
						followed = true
						break
					}
				}
				if !followed {
					return fmt.Errorf("err:FODF1310: grouping separator at the end of the integer part of %q", picture)
				}
			}
		case isDigit(r) && !inFrac:
			seenZeroInt = true
		case r == df.Digit && !inFrac:
			if seenZeroInt {
				return fmt.Errorf("err:FODF1310: optional digit after mandatory digit in the integer part of %q", picture)
			}
		case r == df.Digit && inFrac:
			seenOptFrac = true
		case isDigit(r) && inFrac:
			if seenOptFrac {
				return fmt.Errorf("err:FODF1310: mandatory digit after optional digit in the fractional part of %q", picture)
			}
		}
	}
	return nil
}

type subPicture struct {
	prefix, suffix   string
	minInt           int // minimum integer digits, after the §4.7.4 adjustments
	scale            int // scaling factor: mandatory integer digits in the picture
	minFrac, maxFrac int
	groupInt         int          // regular grouping size (0 = none/positional)
	groupSeps        map[int]bool // positional separator positions from the right
	fracSeps         []int        // fractional grouping positions (digits left of each)
	mult             int          // 1, 100 (%), or 1000 (‰)
	hasExp           bool         // scientific-notation picture (…e…)
	expChar          rune         // the 'e'/'E' separator from the picture
	minExp           int          // mandatory exponent digits
}

// isPicDigit reports whether r is a picture digit-sign (any 0-9 is a mandatory
// digit position; '#' is the optional digit-sign).
func isPicDigit(r rune) bool { return r >= '0' && r <= '9' }

// parseSubPicture analyses one (validated) sub-picture per F&O §4.7.4.
func parseSubPicture(p string, df DecimalFormat) subPicture {
	sp := subPicture{mult: 1}
	runes := []rune(p)
	isDigit := func(r rune) bool { return r >= df.ZeroDigit && r <= df.ZeroDigit+9 }
	isDigitSign := func(r rune) bool { return isDigit(r) || r == df.Digit }
	isActive := func(r rune) bool { return isDigitSign(r) || r == df.DecimalSep || r == df.GroupingSep }
	expIdx, _ := expSepIndex(runes, isActive, normalizedExponentSep(df))
	switch {
	case strings.ContainsRune(p, df.Percent):
		sp.mult = 100
	case strings.ContainsRune(p, df.PerMille):
		sp.mult = 1000
	}
	// Percent and per-mille are passive: they land in the prefix or suffix.
	first, last := -1, -1
	for i := range runes {
		if isActive(runes[i]) || i == expIdx {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		sp.prefix, sp.minInt = p, 1
		return sp
	}
	sp.prefix, sp.suffix = string(runes[:first]), string(runes[last+1:])
	mantEnd := last + 1
	if expIdx >= 0 {
		mantEnd = expIdx
		sp.hasExp, sp.expChar = true, runes[expIdx]
		for _, r := range runes[expIdx+1 : last+1] {
			if isDigit(r) {
				sp.minExp++
			}
		}
	}
	inFrac := false
	intDigits, optInt := 0, 0
	var sepLeftDigits []int // integer digits to the left of each grouping separator
	for i := first; i < mantEnd; i++ {
		r := runes[i]
		switch {
		case r == df.DecimalSep:
			inFrac = true
		case r == df.GroupingSep:
			if inFrac {
				sp.fracSeps = append(sp.fracSeps, sp.maxFrac)
			} else {
				sepLeftDigits = append(sepLeftDigits, intDigits)
			}
		case isDigitSign(r) && !inFrac:
			intDigits++
			if isDigit(r) {
				sp.minInt++
			} else {
				optInt++
			}
		case isDigitSign(r):
			sp.maxFrac++
			if isDigit(r) {
				sp.minFrac++
			}
		}
	}
	sp.scale = sp.minInt
	// The §4.7.4 adjustments: '#' alone shows 0 as "0"; '#.e9' shows 0.123 as
	// 0.1e0 while '.9e9' shows 0.1 as .1e0; '#.#' shows 0.2 as ".2" and 0 as ".0".
	if sp.minInt == 0 && sp.maxFrac == 0 {
		if sp.hasExp {
			sp.minFrac, sp.maxFrac = 1, 1
		} else {
			sp.minInt = 1
		}
	}
	if sp.hasExp && sp.minInt == 0 && optInt > 0 {
		sp.minInt = 1
	}
	if sp.minInt == 0 && sp.minFrac == 0 {
		sp.minFrac = 1
	}
	// Convert each separator's left-digit count to a position-from-right, then
	// classify the grouping as regular (repeats leftward) or positional
	// (explicit separators only — ###,##,00 → 12345,67,89).
	if len(sepLeftDigits) > 0 {
		posFromRight := make([]int, 0, len(sepLeftDigits))
		for _, left := range sepLeftDigits {
			if p := intDigits - left; p > 0 {
				posFromRight = append(posFromRight, p)
			}
		}
		if g, regular := classifyGrouping(posFromRight, intDigits); regular {
			sp.groupInt = g
		} else {
			sp.groupSeps = map[int]bool{}
			for _, p := range posFromRight {
				sp.groupSeps[p] = true
			}
		}
	}
	return sp
}

// classifyGrouping decides whether separators at the given positions-from-right
// form a regular grouping (interval g, repeating) whose leftmost group is no
// wider than g; otherwise the caller treats the positions as explicit.
func classifyGrouping(pos []int, totalIntDigits int) (g int, regular bool) {
	if len(pos) == 0 {
		return 0, false
	}
	ivs := append([]int{}, pos...)
	sortInts(ivs)
	g = ivs[0]
	if g <= 0 {
		return 0, false
	}
	want := g
	maxSep := 0
	for _, iv := range ivs {
		if iv != want {
			return 0, false
		}
		want += g
		maxSep = iv
	}
	if totalIntDigits-maxSep > g {
		return 0, false // leftmost group wider than the interval → positional
	}
	return g, true
}

// dfDigits remaps an ASCII digit string to the decimal-format's zero-digit family.
func dfDigits(s string, df DecimalFormat) string {
	if df.ZeroDigit == '0' {
		return s
	}
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(df.ZeroDigit + (c - '0'))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// formatNumberPic formats a double with the given picture (unit-test entry
// point; fn:format-number goes through formatNumberValue for exact decimals).
func formatNumberPic(v float64, picture string, df DecimalFormat) string {
	return formatNumberValue(fmtNumFromFloat(v, 64), picture, df)
}

// formatNumberValue is F&O §4.7.5: pick the sub-picture by sign, scale for
// percent/per-mille, and render the mantissa (plain or scientific).
func formatNumberValue(n fmtNum, picture string, df DecimalFormat) string {
	if n.nan {
		return df.NaN
	}
	subs := splitOnRune(picture, df.PatternSep)
	sp := parseSubPicture(subs[0], df)
	soleNeg := false
	if n.neg {
		if len(subs) == 2 {
			sp = parseSubPicture(subs[1], df)
		} else {
			soleNeg = true // negative reuses positive picture with a leading minus
		}
	}
	// The adjusted number keeps the input's primitive type: a double is scaled
	// by %/‰ in float arithmetic (so 'x‰' agrees with x*1000, cbcl-035) and
	// becomes infinity on overflow (no error); decimals scale exactly.
	if !n.inf && n.isFloat && sp.mult != 1 {
		scaled := fmtNumFromFloat(n.f*float64(sp.mult), n.bits)
		n.abs, n.inf = scaled.abs, scaled.inf
	}
	if n.inf {
		out := sp.prefix + df.Infinity + sp.suffix
		if soleNeg {
			return string(df.MinusSign) + out
		}
		return out
	}
	av := new(big.Rat).Set(n.abs)
	if sp.mult != 1 && !n.isFloat {
		av.Mul(av, new(big.Rat).SetInt64(int64(sp.mult)))
	}
	var body string
	if sp.hasExp {
		body = renderScientific(av, sp, df)
	} else {
		body = renderMantissa(av, sp, df)
	}
	out := sp.prefix + body + sp.suffix
	if soleNeg {
		out = string(df.MinusSign) + out
	}
	return out
}

// splitOnRune splits s on the first two occurrences of sep (max 2 parts).
func splitOnRune(s string, sep rune) []string {
	for i, r := range s {
		if r == sep {
			return []string{s[:i], s[i+len(string(sep)):]}
		}
	}
	return []string{s}
}

// dfGroupPositional inserts a grouping separator before the digit at each
// given position-from-the-right (explicit, non-repeating grouping).
func dfGroupPositional(digits string, seps map[int]bool, sep rune) string {
	r := []rune(digits)
	n := len(r)
	var out []rune
	for i, d := range r {
		posFromRight := n - i
		if i > 0 && seps[posFromRight] {
			out = append(out, sep)
		}
		out = append(out, d)
	}
	return string(out)
}

// dfGroup inserts the grouping separator every size digits (rune-aware, so it
// works with a multi-byte zero-digit family).
func dfGroup(digits string, size int, sep rune) string {
	r := []rune(digits)
	if size <= 0 || len(r) <= size {
		return digits
	}
	var parts []string
	for len(r) > size {
		parts = append([]string{string(r[len(r)-size:])}, parts...)
		r = r[:len(r)-size]
	}
	parts = append([]string{string(r)}, parts...)
	return strings.Join(parts, string(sep))
}

// dfGroupFrac inserts a grouping separator before the fraction digit that has
// n digits between it and the decimal separator, for each position n
// ('#.#,##,#' → 12345.6,78,9, numberformat157).
func dfGroupFrac(digits string, positions []int, sep rune) string {
	if len(positions) == 0 {
		return digits
	}
	at := map[int]bool{}
	for _, p := range positions {
		at[p] = true
	}
	var out []rune
	for i, d := range []rune(digits) {
		if i > 0 && at[i] {
			out = append(out, sep)
		}
		out = append(out, d)
	}
	return string(out)
}

// renderMantissa rounds av half-to-even at maxFrac places and renders it: no
// leading or trailing zeros beyond minInt/minFrac (zero is "." before padding,
// so '#.#' shows 0 as ".0"), family digits, grouping on both sides, and the
// decimal separator only when a fraction digit follows it.
func renderMantissa(av *big.Rat, sp subPicture, df DecimalFormat) string {
	scale := numPow10(sp.maxFrac)
	k := numRatRoundToEvenInt(new(big.Rat).Mul(av, new(big.Rat).SetInt(scale)))
	q, rem := new(big.Int).QuoRem(k, scale, new(big.Int))
	intPart := ""
	if q.Sign() != 0 {
		intPart = q.String()
	}
	fracPart := ""
	if sp.maxFrac > 0 {
		fracPart = rem.String()
		if len(fracPart) < sp.maxFrac {
			fracPart = strings.Repeat("0", sp.maxFrac-len(fracPart)) + fracPart
		}
		fracPart = strings.TrimRight(fracPart, "0")
	}
	if len(intPart) < sp.minInt {
		intPart = strings.Repeat("0", sp.minInt-len(intPart)) + intPart
	}
	if len(fracPart) < sp.minFrac {
		fracPart += strings.Repeat("0", sp.minFrac-len(fracPart))
	}
	intPart = dfDigits(intPart, df)
	if sp.groupInt > 0 {
		intPart = dfGroup(intPart, sp.groupInt, df.GroupingSep)
	} else if len(sp.groupSeps) > 0 {
		intPart = dfGroupPositional(intPart, sp.groupSeps, df.GroupingSep)
	}
	if fracPart == "" {
		return intPart
	}
	return intPart + string(df.DecimalSep) + dfGroupFrac(dfDigits(fracPart, df), sp.fracSeps, df.GroupingSep)
}

// ratDigitExponent returns k with 10^(k-1) <= r < 10^k for r > 0: the count of
// integer digits, or minus the count of leading fraction zeros.
func ratDigitExponent(r *big.Rat) int {
	k := len(r.Num().String()) - len(r.Denom().String()) + 1
	for r.Cmp(pow10rat(k)) >= 0 {
		k++
	}
	for r.Cmp(pow10rat(k-1)) < 0 {
		k--
	}
	return k
}

// renderScientific formats av in exponential notation: the mantissa is scaled
// into [10^(N-1), 10^N) for the picture's scaling factor N (0 → [0.1, 1)),
// rounded per the mantissa sub-picture without renormalising (0.99999999 with
// '.#e0' is 1.0e0), and the exponent padded to its minimum size.
func renderScientific(av *big.Rat, sp subPicture, df DecimalFormat) string {
	exp := 0
	mant := av
	if av.Sign() != 0 {
		exp = ratDigitExponent(av) - sp.scale
		mant = new(big.Rat).Quo(av, pow10rat(exp))
	}
	out := renderMantissa(mant, sp, df)
	expStr := strconv.Itoa(exp)
	sign := ""
	if exp < 0 {
		sign, expStr = string(df.MinusSign), expStr[1:]
	}
	if len(expStr) < sp.minExp {
		expStr = strings.Repeat("0", sp.minExp-len(expStr)) + expStr
	}
	return out + string(sp.expChar) + sign + dfDigits(expStr, df)
}

// --- typed unary numeric functions (fn:abs/floor/ceiling/round) -------------
// These preserve the input's numeric subtype (integer→integer, decimal→decimal,
// float→float, double→double) per F&O signature ($arg as xs:numeric?) as
// xs:numeric?, with empty→empty. untypedAtomic is cast to xs:double.

// numFirstNumeric returns the single numeric atomic of x (untyped→double),
// ok=false for the empty sequence, or an error for a non-numeric operand.
func numFirstNumeric(ctx *Context, x Object) (*Atomic, bool, error) {
	// XSLT 1.0 backwards-compatible processing has no "optional parameter"
	// concept at all — a path expression is always a node-set, and passing
	// one (empty included) through a number-context function like round()
	// applies the FULL number() conversion, where an empty node-set's
	// string-value "" converts to NaN rather than passing through as empty
	// (xpath-compat-0302: round(doc/none) with doc/none selecting no nodes).
	bc10 := ctx != nil && ctx.BC10
	if numIsEmpty(x) {
		if bc10 {
			return NewDouble(math.NaN()), true, nil
		}
		return nil, false, nil
	}
	items, err := Atomize(x)
	if err != nil {
		return nil, false, err
	}
	if len(items) == 0 {
		if bc10 {
			return NewDouble(math.NaN()), true, nil
		}
		return nil, false, nil
	}
	a, ok := items[0].(*Atomic)
	if !ok {
		return nil, false, fmt.Errorf("err:XPTY0004: numeric function applied to non-atomic")
	}
	if a.T == XSuntypedAtomic {
		c, err := CastTo(a, XSdouble)
		if err != nil {
			return nil, false, err
		}
		return c, true, nil
	}
	if !a.IsNumeric() {
		// XSLT's own backwards-compatible processing (ctx.BC10) restores the
		// function conversion rules' 1.0-flavored coercion here too: a
		// non-numeric argument (e.g. a plain xs:string from fn:concat) is
		// converted via number() instead of raising a type error
		// (backwards-022: round(concat(substring(current-date(),1,2),'.7'))).
		if ctx != nil && ctx.BC10 {
			return NewDouble(ToNumber(a)), true, nil
		}
		return nil, false, fmt.Errorf("err:XPTY0004: numeric function applied to %s", a.T)
	}
	return a, true, nil
}

func pow10rat(p int) *big.Rat {
	ap := p
	if ap < 0 {
		ap = -ap
	}
	e := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(ap)), nil)
	if p >= 0 {
		return new(big.Rat).SetInt(e)
	}
	return new(big.Rat).SetFrac(big.NewInt(1), e)
}

// ratFloorInt = floor(r); ratCeilInt = ceil(r); ratRoundInt = round-half-up(r).
// big.Rat keeps a positive denominator, and big.Int.Div is Euclidean (floors).
func ratFloorInt(r *big.Rat) *big.Int { return new(big.Int).Div(r.Num(), r.Denom()) }
func ratCeilInt(r *big.Rat) *big.Int {
	m := new(big.Int)
	q, _ := new(big.Int).DivMod(r.Num(), r.Denom(), m)
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}
func ratRoundInt(r *big.Rat) *big.Int { // floor(r + 1/2) = floor((2n+d)/(2d))
	n2 := new(big.Int).Add(new(big.Int).Lsh(r.Num(), 1), r.Denom())
	d2 := new(big.Int).Lsh(r.Denom(), 1)
	return new(big.Int).Div(n2, d2)
}

// ratRoundScaled rounds r half-up to p decimal places, returning a *big.Rat.
func ratRoundScaled(r *big.Rat, p int) *big.Rat {
	scale := pow10rat(p)
	k := ratRoundInt(new(big.Rat).Mul(r, scale))
	return new(big.Rat).Quo(new(big.Rat).SetInt(k), scale)
}

// roundFloatPrec rounds f half-up (ties toward +INF, fn:round's rule) to p
// decimal places.
//
// It rounds the EXACT binary value of f via big.Rat rather than scaling in
// float64 space, for the same reason numRoundFloatToEven does: f*10^p and the
// subsequent /10^p each round again, which both manufactures ties that do not
// exist and destroys ones that do. round(-1.365e1, 1) is -13.7 because the
// double nearest -13.65 is slightly BELOW it, which the scaled form cannot
// see; and round(45e100, -101) is 5.0E101, which 10^-101 as a float cannot
// represent at all (math-3701). Even p == 0 is affected: the classic
// floor(f+0.5) form answers 1 for the largest double below 0.5.
func roundFloatPrec(f float64, p int) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	if p >= 0 && f == math.Trunc(f) {
		return f // already integral: no decimal place can change it
	}
	if p > 308 { // beyond float64 precision: value is unchanged
		return f
	}
	if p < -308 { // rounds to a power of ten larger than any finite double
		return math.Copysign(0, f)
	}
	r := new(big.Rat).SetFloat64(f)
	if r == nil { // only NaN/Inf make SetFloat64 fail, and both are handled above
		return f
	}
	out, _ := ratRoundScaled(r, p).Float64()
	return signedZero(out, f)
}

func fnAbsT(c *Context, a []Object) (Object, error) {
	x, ok, err := numFirstNumeric(c, arg(a, 0))
	if err != nil || !ok {
		return emptyOr(err)
	}
	switch {
	case isIntegerType(x.T):
		return NewIntegerBig(new(big.Int).Abs(x.i)), nil
	case x.T == XSdecimal:
		return NewDecimal(new(big.Rat).Abs(x.d)), nil
	case x.T == XSfloat:
		return NewFloat(math.Abs(x.f)), nil
	default:
		return NewDouble(math.Abs(x.f)), nil
	}
}

func fnFloorT(c *Context, a []Object) (Object, error) {
	x, ok, err := numFirstNumeric(c, arg(a, 0))
	if err != nil || !ok {
		return emptyOr(err)
	}
	switch {
	case isIntegerType(x.T):
		return NewIntegerBig(new(big.Int).Set(x.i)), nil
	case x.T == XSdecimal:
		return NewDecimal(new(big.Rat).SetInt(ratFloorInt(x.d))), nil
	case x.T == XSfloat:
		return NewFloat(math.Floor(x.f)), nil
	default:
		return NewDouble(math.Floor(x.f)), nil
	}
}

func fnCeilingT(c *Context, a []Object) (Object, error) {
	x, ok, err := numFirstNumeric(c, arg(a, 0))
	if err != nil || !ok {
		return emptyOr(err)
	}
	switch {
	case isIntegerType(x.T):
		return NewIntegerBig(new(big.Int).Set(x.i)), nil
	case x.T == XSdecimal:
		return NewDecimal(new(big.Rat).SetInt(ratCeilInt(x.d))), nil
	case x.T == XSfloat:
		return NewFloat(math.Ceil(x.f)), nil
	default:
		return NewDouble(math.Ceil(x.f)), nil
	}
}

func fnRoundT(c *Context, a []Object) (Object, error) {
	x, ok, err := numFirstNumeric(c, arg(a, 0))
	if err != nil || !ok {
		return emptyOr(err)
	}
	p := 0
	if pa := arg(a, 1); !numIsEmpty(pa) {
		if pv, pok, _ := numFirstNumeric(c, pa); pok {
			p = int(pv.Float())
		}
	}
	// Clamp absurd precisions (the FOTS suite probes 2^32) so 10^p stays a cheap
	// big.Int; the rounded result is identical for any value the suite uses.
	if p > roundPrecisionLimit {
		p = roundPrecisionLimit
	} else if p < -roundPrecisionLimit {
		p = -roundPrecisionLimit
	}
	switch {
	case isIntegerType(x.T):
		if p >= 0 {
			return NewIntegerBig(new(big.Int).Set(x.i)), nil
		}
		return NewIntegerBig(ratRoundScaled(new(big.Rat).SetInt(x.i), p).Num()), nil
	case x.T == XSdecimal:
		return NewDecimal(ratRoundScaled(x.d, p)), nil
	case x.T == XSfloat:
		return NewFloat(roundFloatPrec(x.f, p)), nil
	default:
		return NewDouble(roundFloatPrec(x.f, p)), nil
	}
}

func emptyOr(err error) (Object, error) {
	if err != nil {
		return nil, err
	}
	return Sequence{}, nil
}

func init() {
	coreFuncs["round-half-to-even"] = numRoundHalfToEven
	coreFuncs["format-integer"] = numFormatInteger

	mathFuncs["tan"] = math1(math.Tan)
	mathFuncs["asin"] = math1(math.Asin)
	mathFuncs["acos"] = math1(math.Acos)
	mathFuncs["atan"] = math1(math.Atan)
	mathFuncs["atan2"] = func(c *Context, a []Object) (Object, error) {
		if numIsEmpty(arg(a, 0)) || numIsEmpty(arg(a, 1)) {
			return Sequence{}, nil
		}
		return NewDouble(math.Atan2(ToNumber(arg(a, 0)), ToNumber(arg(a, 1)))), nil
	}
}

// numFirstAtomic returns the first *Atomic in an argument, if any, so we can
// inspect the numeric subtype of the input.
func numFirstAtomic(o Object) *Atomic {
	items := Items(o)
	if len(items) == 0 {
		return nil
	}
	if a, ok := items[0].(*Atomic); ok {
		return a
	}
	return nil
}

// numIsEmpty reports whether an argument carries no items (empty sequence).
func numIsEmpty(o Object) bool {
	if o == nil {
		return true
	}
	return len(Items(o)) == 0
}

// numRoundHalfToEven implements fn:round-half-to-even($value, $precision = 0).
// It applies banker's rounding to the given number of decimal places and
// attempts to preserve the integer/decimal/double type of the input.
func numRoundHalfToEven(c *Context, a []Object) (Object, error) {
	val := arg(a, 0)
	// $arg is xs:numeric?: a string is XPTY0004 (K-RoundEvenFunc-5), and
	// $precision is an xs:integer (cbcl-round-half-to-even-014: "two").
	at, ok, err := numFirstNumeric(c, val)
	if err != nil {
		return nil, err
	}
	if !ok {
		return Sequence{}, nil
	}

	precision := int(0)
	if len(a) > 1 {
		if precision, err = sqIntegerArg("round-half-to-even", arg(a, 1)); err != nil {
			return nil, err
		}
	}
	// Guard against absurd precisions (the FOTS suite probes 2^32): rounding to
	// more places than any value's significant digits leaves it unchanged, and
	// rounding to a power of ten larger than any finite value yields zero — but
	// computing 10^precision as a big.Int would allocate billions of digits and
	// hang. Clamp to a bound far beyond any real value's scale; the result is
	// identical for every value the suite uses.
	if precision > roundPrecisionLimit {
		precision = roundPrecisionLimit
	} else if precision < -roundPrecisionLimit {
		precision = -roundPrecisionLimit
	}

	// Integer input: rounding to precision >= 0 yields the same integer;
	// negative precision rounds to powers of ten.
	if at != nil && isIntegerType(at.T) && at.i != nil {
		if precision >= 0 {
			return NewIntegerBig(new(big.Int).Set(at.i)), nil
		}
		return NewIntegerBig(numRoundIntToEven(at.i, -precision)), nil
	}

	// Decimal input: round exactly using big.Rat.
	if at != nil && at.T == XSdecimal {
		r := toRat(at)
		return NewDecimal(numRoundRatToEven(r, precision)), nil
	}

	// xs:float input: round in float64 space but preserve the float type.
	if at != nil && at.T == XSfloat {
		if math.IsNaN(at.f) || math.IsInf(at.f, 0) {
			return NewFloat(at.f), nil
		}
		return NewFloat(numRoundFloatToEven(at.f, precision)), nil
	}

	// Double / everything else: round in float64 space.
	f := ToNumber(val)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return NewDouble(f), nil
	}
	return NewDouble(numRoundFloatToEven(f, precision)), nil
}

// numRoundFloatToEven rounds f to the given number of decimal places using
// round-half-to-even (banker's rounding). It rounds the EXACT binary value of
// f (via big.Rat, reusing numRoundRatToEven — the same exact-rational engine
// xs:decimal uses), not a float64*scale/scale approximation: naive scaling
// (f * 10^precision, round, / 10^precision) performs its OWN extra rounding
// during the multiply/divide, which can spuriously manufacture — or, just as
// wrongly, erase — an exact tie that was never really there (math-3303: the
// double nearest the 250.0250 literal is NOT exactly *.025, so it must round
// consistently to 250.03, not fall into the ties-to-even branch as if it
// were).
func numRoundFloatToEven(f float64, precision int) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	// Beyond the dynamic range of float64 (~1e308) the scale over/underflows.
	// Rounding to more places than the value's precision leaves it unchanged;
	// rounding to a power of ten larger than any finite double yields zero.
	if precision > 308 {
		return f
	}
	if precision < -308 {
		return math.Copysign(0, f)
	}
	r := new(big.Rat).SetFloat64(f)
	if r == nil { // f is ±0: SetFloat64 only fails for NaN/Inf, already handled above.
		return f
	}
	out, _ := numRoundRatToEven(r, precision).Float64()
	return signedZero(out, f)
}

// signedZero returns r, but when r is zero it carries the sign of the original
// value f — fn:round / round-half-to-even must yield -0.0 for small negatives.
func signedZero(r, f float64) float64 {
	if r == 0 {
		return math.Copysign(0, f)
	}
	return r
}

// numRoundRatToEven rounds an exact rational to precision decimal places using
// banker's rounding, returning an exact rational.
func numRoundRatToEven(r *big.Rat, precision int) *big.Rat {
	// scale = 10^precision (may be negative -> divide).
	pow := numPow10(numAbs(precision))
	scaled := new(big.Rat).Set(r)
	if precision >= 0 {
		scaled.Mul(scaled, new(big.Rat).SetInt(pow))
	} else {
		scaled.Quo(scaled, new(big.Rat).SetInt(pow))
	}

	rounded := numRatRoundToEvenInt(scaled)

	out := new(big.Rat).SetInt(rounded)
	if precision >= 0 {
		out.Quo(out, new(big.Rat).SetInt(pow))
	} else {
		out.Mul(out, new(big.Rat).SetInt(pow))
	}
	return out
}

// numRatRoundToEvenInt rounds a rational to the nearest integer, ties to even.
func numRatRoundToEvenInt(r *big.Rat) *big.Int {
	num := r.Num()
	den := r.Denom()

	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(num, den, rem) // truncated toward zero

	if rem.Sign() == 0 {
		return q
	}

	// Compare 2*|rem| with den to decide rounding of the fractional part.
	twoRem := new(big.Int).Mul(new(big.Int).Abs(rem), big.NewInt(2))
	cmp := twoRem.Cmp(den)

	neg := num.Sign() < 0

	up := false
	switch {
	case cmp > 0:
		up = true
	case cmp < 0:
		up = false
	default:
		// Exactly halfway: round to even.
		if q.Bit(0) == 1 {
			up = true
		}
	}

	if up {
		if neg {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q
}

// numRoundIntToEven rounds a big integer to a multiple of 10^digits using
// banker's rounding (used for negative precision on integer inputs).
func numRoundIntToEven(n *big.Int, digits int) *big.Int {
	if digits <= 0 {
		return new(big.Int).Set(n)
	}
	pow := numPow10(digits)
	r := new(big.Rat).SetFrac(n, pow)
	rounded := numRatRoundToEvenInt(r)
	return new(big.Int).Mul(rounded, pow)
}

// roundPrecisionLimit bounds the $precision argument of round / round-half-to-even.
// No xs:decimal in practice has more significant digits than this, so rounding to
// this many places (or to 10^this) is indistinguishable from rounding to a larger
// magnitude — while keeping 10^precision a cheap, bounded big.Int.
const roundPrecisionLimit = 4096

func numPow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

func numAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// numFormatInteger implements a pragmatic fn:format-integer($value, $picture).
// Supported pictures: "0", "000" (zero-padding to width), "#", "#,##0"
// (grouping separators with mandatory and optional digits), and an empty/other
// picture which falls back to the canonical integer string.
func numFormatInteger(c *Context, a []Object) (Object, error) {
	val := arg(a, 0)
	if numIsEmpty(val) {
		return "", nil
	}

	// Obtain the integer value as a big.Int where possible.
	bi := numToBigInt(val)
	picture := ToString(arg(a, 1))

	if picture == "" {
		return nil, fmt.Errorf("err:FODF1310: format-integer picture must not be empty")
	}

	// Everything before the LAST semicolon is the primary format token and
	// everything after it the format modifier, so ';' can serve as a grouping
	// separator ('#;##1;' → 1;234, format-integer-060). The primary token must
	// not be empty (format-integer-061).
	primary, modifier := picture, ""
	if i := strings.LastIndexByte(picture, ';'); i >= 0 {
		primary, modifier = picture[:i], picture[i+1:]
	}
	if primary == "" {
		return nil, fmt.Errorf("err:FODF1310: format-integer primary format token must not be empty")
	}
	ordinal, err := parseIntegerModifier(modifier)
	if err != nil {
		return nil, err
	}

	neg := bi.Sign() < 0
	abs := new(big.Int).Abs(bi)
	sign := ""
	if neg {
		sign = "-"
	}

	// Sequences keyed by a single letter token (Roman / alphabetic / words) only
	// make sense for values that fit an int64 and are non-zero; otherwise fall
	// back to decimal.
	n64, fits := numInt64(abs)
	switch primary {
	case "I", "i":
		if fits && n64 > 0 && n64 < 5000 {
			return sign + romanNumeral(n64, primary == "I"), nil
		}
	case "A", "a":
		if fits && n64 > 0 {
			return sign + alphaSequence(n64, primary == "A"), nil
		}
	case "w", "W", "Ww":
		if fits {
			return sign + numberWords(n64, primary, ordinal), nil
		}
	case "Α", "α": // Greek Α/α alphabetic sequence (format-integer-049/050)
		if fits && n64 > 0 {
			return sign + alphabetSequence(n64, greekAlphabet(primary == "Α")), nil
		}
	case "一": // Kanji 一 numerals (format-integer-052)
		if fits && n64 > 0 && n64 < 10000 {
			return sign + kanjiNumeral(n64), nil
		}
	}

	// A decimal-digit-pattern picture (App. §4.6.1): optional-digit-signs
	// ('#'), mandatory digits of ONE family (ASCII or Thai/Arabic-Indic/
	// Osmanya…), and arbitrary grouping-separator characters between them.
	// A single-character token from a Unicode numbering sequence (circled
	// ①-⑳, parenthesized ⑴-⒇, digit-period ⒈-⒛) produces the n-th member
	// (format-integer-046/047/048).
	if start, seqLen, ok := unicodeNumberSeq(primary); ok && fits && n64 >= 1 && n64 <= int64(seqLen) {
		return sign + string(rune(start)+rune(n64-1)), nil
	}
	if dp, ok := parseDigitPattern(primary); ok {
		digits := abs.String()
		if len(digits) < dp.mandatory {
			digits = strings.Repeat("0", dp.mandatory-len(digits)) + digits
		}
		out := dp.render(digits)
		if ordinal && fits {
			out += ordinalSuffix(n64)
		}
		return sign + out, nil
	}
	if isDigitPatternPicture(primary) {
		// It contains a digit, so it IS a decimal-digit-pattern — a malformed
		// one → FODF1310 (format-integer-020/023/024/027/028/040).
		return nil, fmt.Errorf("err:FODF1310: invalid format-integer picture %q", primary)
	}
	// Any other token is formatted as if the picture were "1" (bug 19004:
	// '#a' → 1500000, '()Ww;o' → 1234th; format-integer-026/038).
	out := abs.String()
	if ordinal && fits {
		out += ordinalSuffix(n64)
	}
	return sign + out, nil
}

// parseIntegerModifier validates a format-integer modifier against
// ^([co](\(.+\))?)?[at]?$ (FODF1310 otherwise: 'o(-er)z', 'o()(', 'o(' in
// format-integer-034/037/067) and reports whether it requests ordinals.
func parseIntegerModifier(m string) (ordinal bool, err error) {
	rest := m
	if rest != "" && (rest[0] == 'c' || rest[0] == 'o') {
		ordinal = rest[0] == 'o'
		rest = rest[1:]
		if strings.HasPrefix(rest, "(") {
			switch {
			case len(rest) >= 3 && strings.HasSuffix(rest, ")"):
				rest = ""
			case len(rest) >= 4 && (strings.HasSuffix(rest, ")a") || strings.HasSuffix(rest, ")t")):
				rest = rest[len(rest)-1:]
			default:
				return false, fmt.Errorf("err:FODF1310: invalid format-integer modifier %q", m)
			}
		}
	}
	if rest != "" && rest != "a" && rest != "t" {
		return false, fmt.Errorf("err:FODF1310: invalid format-integer modifier %q", m)
	}
	return ordinal, nil
}

// unicodeNumberSeq reports whether primary is the first member of a supported
// Unicode numbering sequence, returning that member's codepoint and the
// sequence length.
func unicodeNumberSeq(primary string) (start rune, length int, ok bool) {
	r := []rune(primary)
	if len(r) != 1 {
		return 0, 0, false
	}
	switch r[0] {
	case '①': // ① circled digit one (①-⑳)
		return '①', 20, true
	case '⑴': // ⑴ parenthesized digit one (⑴-⒇)
		return '⑴', 20, true
	case '⒈': // ⒈ digit one full stop (⒈-⒛)
		return '⒈', 20, true
	}
	return 0, 0, false
}

// digitPattern is a parsed decimal-digit-pattern picture.
type digitPattern struct {
	mandatory int          // count of mandatory digit positions
	zero      rune         // family zero ('0' for ASCII)
	seps      map[int]rune // grouping separators keyed by position-from-right
	regular   int          // regular grouping interval (0 = none)
	regRune   rune         // the separator for a regular interval
}

// isDigitPatternPicture reports whether p is a decimal-digit-pattern by the
// F&O §4.6.1 criterion — it contains at least one Unicode digit — so a failed
// parse is a malformed digit pattern (FODF1310), while a digit-free token such
// as '#a' is merely unrecognised and falls back to '1'.
func isDigitPatternPicture(p string) bool {
	for _, r := range p {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// parseDigitPattern validates and parses a decimal-digit-pattern picture,
// returning ok=false when it is malformed (empty, separator at an edge,
// adjacent separators, '#' after a mandatory digit, or mixed digit families).
func parseDigitPattern(p string) (digitPattern, bool) {
	rs := []rune(p)
	if len(rs) == 0 {
		return digitPattern{}, false
	}
	dp := digitPattern{zero: '0', seps: map[int]rune{}}
	var famZero rune
	mandatory, optional := 0, 0
	sawMandatory := false
	// sepPositions: index-from-right (in digit count) -> separator rune
	digitCount := 0
	lastWasSep := false
	// count total digit signs first
	total := 0
	for _, r := range rs {
		if r == '#' || unicode.IsDigit(r) {
			total++
		}
	}
	if total == 0 {
		return digitPattern{}, false
	}
	sepAt := map[int]rune{}
	var sepIntervals []int
	for i, r := range rs {
		switch {
		case r == '#':
			if sawMandatory {
				return digitPattern{}, false // optional after mandatory
			}
			optional++
			digitCount++
			lastWasSep = false
		case unicode.IsDigit(r):
			z := r - rune(digitValue(r))
			if famZero == 0 {
				famZero = z
			} else if z != famZero {
				return digitPattern{}, false // mixed families
			}
			sawMandatory = true
			mandatory++
			digitCount++
			lastWasSep = false
		default: // grouping separator: neither a letter nor a number
			if i == 0 || i == len(rs)-1 || lastWasSep || unicode.IsLetter(r) || unicode.IsNumber(r) {
				return digitPattern{}, false // edge, adjacent, or non-separator
			}
			posFromRight := total - digitCount
			sepAt[posFromRight] = r
			sepIntervals = append(sepIntervals, posFromRight)
			lastWasSep = true
		}
	}
	dp.zero = famZero
	if famZero == 0 {
		dp.zero = '0'
	}
	dp.mandatory = mandatory
	dp.seps = sepAt
	// A single separator at a consistent interval that divides evenly is
	// "regular" and repeats leftward (App. rule); otherwise separators sit at
	// their explicit positions only.
	// A set of separators at a CONSISTENT interval (all multiples of the
	// smallest, same rune) is a regular grouping that repeats leftward
	// (format-integer-071 '00,00,00' -> groups of 2).
	if len(sepIntervals) >= 1 {
		g := sepIntervals[0]
		for _, iv := range sepIntervals { // smallest interval is the group size
			if iv < g {
				g = iv
			}
		}
		sameRune := true
		regular := g > 0
		for _, iv := range sepIntervals {
			if sepAt[iv] != sepAt[sepIntervals[0]] {
				sameRune = false
			}
			if g == 0 || iv%g != 0 {
				regular = false
			}
		}
		// also require the intervals to be exactly g, 2g, 3g, …
		if regular && sameRune {
			want := g
			maxSep := 0
			ivs := append([]int{}, sepIntervals...)
			sortInts(ivs)
			for _, iv := range ivs {
				if iv != want {
					regular = false
					break
				}
				want += g
				maxSep = iv
			}
			// A grouping is regular (repeats leftward) only if the leftmost
			// group is no wider than the interval; otherwise the separators
			// are positional (###,##,00 → 12345,67,89, not 1,23,45,67,89).
			if regular && total-maxSep > g {
				regular = false
			}
			if regular {
				dp.regular = g
				dp.regRune = sepAt[sepIntervals[0]]
				dp.seps = map[int]rune{} // regular interval supersedes explicit
			}
		}
	}
	_ = optional
	return dp, true
}

// render inserts the family digits and grouping separators.
func (dp digitPattern) render(ascii string) string {
	fam := mapDigitsToFamily(ascii, dp.zero)
	famRunes := []rune(fam)
	n := len(famRunes)
	var out []rune
	for i, r := range famRunes {
		posFromRight := n - i
		if i > 0 {
			if sep, ok := dp.seps[posFromRight]; ok {
				out = append(out, sep)
			} else if dp.regular > 0 && posFromRight%dp.regular == 0 {
				out = append(out, dp.regRune)
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// digitFamilyZero reports the zero code point of the decimal-digit family a
// primary picture is written in, when that family is NOT ASCII '0'. A picture
// qualifies when every rune is a decimal digit (unicode Nd) of one family
// plus optional '#'/grouping, and at least one digit is non-ASCII.
func digitFamilyZero(p string) (rune, bool) {
	var zero rune
	haveNonASCII := false
	sawDigit := false
	for _, r := range p {
		if r == '#' || r == ',' {
			continue
		}
		if !unicode.IsDigit(r) {
			return 0, false
		}
		sawDigit = true
		z := r - rune(digitValue(r)) // the family's zero
		if zero == 0 {
			zero = z
		} else if z != zero {
			return 0, false // mixed families
		}
		if r > 0x7F {
			haveNonASCII = true
		}
	}
	if !sawDigit || !haveNonASCII {
		return 0, false
	}
	return zero, true
}

// digitValue returns the 0..9 value of a Unicode decimal digit by descending
// to its contiguous family zero (Nd families span exactly ten code points).
func digitValue(r rune) int {
	z := r
	for r-z < 9 && unicode.IsDigit(z-1) {
		z--
	}
	return int(r - z)
}

// mapDigitsToFamily rewrites ASCII digits into the family whose zero is z.
func mapDigitsToFamily(ascii string, z rune) string {
	var b strings.Builder
	for _, r := range ascii {
		if r >= '0' && r <= '9' {
			b.WriteRune(z + (r - '0'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func numInt64(b *big.Int) (int64, bool) {
	if b.IsInt64() {
		return b.Int64(), true
	}
	return 0, false
}

// romanNumeral renders 1..4999 as Roman numerals (upper or lower case).
func romanNumeral(n int64, upper bool) string {
	vals := []struct {
		v int64
		s string
	}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"},
		{50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}
	var b strings.Builder
	for _, e := range vals {
		for n >= e.v {
			b.WriteString(e.s)
			n -= e.v
		}
	}
	out := b.String()
	if !upper {
		return strings.ToLower(out)
	}
	return out
}

// alphaSequence renders 1→A, 26→Z, 27→AA … (bijective base-26).
func alphaSequence(n int64, upper bool) string {
	base := byte('a')
	if upper {
		base = 'A'
	}
	var rev []byte
	for n > 0 {
		n--
		rev = append(rev, base+byte(n%26))
		n /= 26
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return string(rev)
}

// alphabetSequence renders n (>= 1) in bijective numeration over the given
// alphabet: 1 → first letter, len → last, len+1 → first-first.
func alphabetSequence(n int64, alphabet []rune) string {
	base := int64(len(alphabet))
	var rev []rune
	for n > 0 {
		n--
		rev = append(rev, alphabet[n%base])
		n /= base
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return string(rev)
}

// greekAlphabet is the 24-letter Greek alphabet Α..Ω / α..ω (skipping the
// unassigned U+03A2 and the final sigma ς).
func greekAlphabet(upper bool) []rune {
	var out []rune
	for r := rune(0x391); r <= 0x3a9; r++ {
		if r == 0x3a2 {
			continue
		}
		if upper {
			out = append(out, r)
		} else {
			out = append(out, r+0x20)
		}
	}
	return out
}

// kanjiNumeral renders 1..9999 with the Japanese/Chinese numerals: a unit
// (千 百 十) is written without a leading 一, and empty places are skipped
// (151 → 百五十一, 302 → 三百二, 2025 → 二千二十五; format-integer-052).
func kanjiNumeral(n int64) string {
	digits := []rune("〇一二三四五六七八九")
	var b strings.Builder
	for _, u := range []struct {
		v    int64
		unit rune
	}{{1000, '千'}, {100, '百'}, {10, '十'}} {
		if d := n / u.v; d > 0 {
			if d > 1 {
				b.WriteRune(digits[d])
			}
			b.WriteRune(u.unit)
		}
		n %= u.v
	}
	if n > 0 {
		b.WriteRune(digits[n])
	}
	return b.String()
}

func ordinalSuffix(n int64) string {
	a := n
	if a < 0 {
		a = -a
	}
	if a%100 >= 11 && a%100 <= 13 {
		return "th"
	}
	switch a % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

var wordsOnes = []string{"zero", "one", "two", "three", "four", "five", "six",
	"seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen",
	"fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
var wordsTens = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty",
	"seventy", "eighty", "ninety"}
var wordsOrdinalOnes = []string{"zeroth", "first", "second", "third", "fourth",
	"fifth", "sixth", "seventh", "eighth", "ninth", "tenth", "eleventh", "twelfth",
	"thirteenth", "fourteenth", "fifteenth", "sixteenth", "seventeenth",
	"eighteenth", "nineteenth"}
var wordsTensOrdinal = []string{"", "", "twentieth", "thirtieth", "fortieth",
	"fiftieth", "sixtieth", "seventieth", "eightieth", "ninetieth"}

// cardinalWords renders a non-negative int64 below one billion as English words.
func cardinalWords(n int64) string {
	if n < 20 {
		return wordsOnes[n]
	}
	if n < 100 {
		w := wordsTens[n/10]
		if n%10 != 0 {
			w += "-" + wordsOnes[n%10]
		}
		return w
	}
	if n < 1000 {
		w := wordsOnes[n/100] + " hundred"
		if n%100 != 0 {
			w += " and " + cardinalWords(n%100)
		}
		return w
	}
	for _, scale := range []struct {
		v int64
		s string
	}{{1000000000, "billion"}, {1000000, "million"}, {1000, "thousand"}} {
		if n >= scale.v {
			w := cardinalWords(n/scale.v) + " " + scale.s
			if rem := n % scale.v; rem != 0 {
				// The traditional English "and" appears once, directly
				// before a final tens/ones group with nothing of its own
				// scale (format-date-en-026/028: "one thousand nine hundred
				// AND ninetieth", "two thousand AND first" — but no "and"
				// between "thousand" and a full "two hundred").
				if rem < 100 {
					w += " and " + cardinalWords(rem)
				} else {
					w += " " + cardinalWords(rem)
				}
			}
			return w
		}
	}
	return wordsOnes[0]
}

// ordinalWords renders a non-negative int64 as an English ordinal phrase.
func ordinalWords(n int64) string {
	if n < 20 {
		return wordsOrdinalOnes[n]
	}
	if n < 100 {
		if n%10 == 0 {
			return wordsTensOrdinal[n/10]
		}
		return wordsTens[n/10] + "-" + wordsOrdinalOnes[n%10]
	}
	// 100+: cardinalise the leading part, ordinalise the trailing remainder.
	for _, scale := range []struct {
		v int64
		s string
	}{{1000000000, "billion"}, {1000000, "million"}, {1000, "thousand"}, {100, "hundred"}} {
		if n >= scale.v {
			head := cardinalWords(n/scale.v) + " " + scale.s
			rem := n % scale.v
			if rem == 0 {
				return head + "th"
			}
			// See cardinalWords: "and" precedes a final tens/ones group only.
			if rem < 100 {
				return head + " and " + ordinalWords(rem)
			}
			return head + " " + ordinalWords(rem)
		}
	}
	return wordsOrdinalOnes[0]
}

// numberWords renders n as English words for the w/W/Ww tokens, applying the
// ordinal modifier and the requested casing.
func numberWords(n int64, token string, ordinal bool) string {
	abs := n
	sign := ""
	if abs < 0 {
		abs = -abs
		sign = "-"
	}
	var w string
	if ordinal {
		w = ordinalWords(abs)
	} else {
		w = cardinalWords(abs)
	}
	switch token {
	case "W":
		w = strings.ToUpper(w)
	case "Ww":
		w = titleCaseWords(w)
	}
	return sign + w
}

func titleCaseWords(s string) string {
	var b strings.Builder
	upNext := true
	for _, r := range s {
		switch {
		case r == ' ' || r == '-':
			upNext = true
			b.WriteRune(r)
		case upNext:
			if r >= 'a' && r <= 'z' {
				r -= 32
			}
			b.WriteRune(r)
			upNext = false
		default:
			if r >= 'A' && r <= 'Z' {
				r += 32
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// numToBigInt extracts an integer value from an Object.
func numToBigInt(o Object) *big.Int {
	if at := numFirstAtomic(o); at != nil {
		if isIntegerType(at.T) && at.i != nil {
			return new(big.Int).Set(at.i)
		}
	}
	f := ToNumber(o)
	bi, _ := big.NewFloat(f).Int(nil)
	if bi == nil {
		return big.NewInt(0)
	}
	return bi
}

// numIsDigitPicture reports whether the picture is composed only of the
// decimal-digit-pattern characters we support: 0, #, and the grouping comma.
func numIsDigitPicture(p string) bool {
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9', r == '#', r == ',':
		default:
			return false
		}
	}
	return len(p) > 0
}

// numParseDigitPicture extracts the count of mandatory ('0') digits, the
// grouping size, and the grouping separator from a digit picture.
func numParseDigitPicture(p string) (minDigits, grouping int, sep byte) {
	sep = ','
	// Strip grouping separators to count mandatory digits and detect grouping.
	clean := strings.ReplaceAll(p, ",", "")
	for _, r := range clean {
		if r >= '0' && r <= '9' {
			minDigits++
		}
	}
	// Grouping size = digits after the last comma.
	if idx := strings.LastIndex(p, ","); idx >= 0 {
		grouping = 0
		for _, r := range p[idx+1:] {
			if r == '0' || r == '#' {
				grouping++
			}
		}
	}
	return minDigits, grouping, sep
}

// numGroup inserts a separator every `size` digits from the right.
func numGroup(digits string, size int, sep byte) string {
	if size <= 0 || len(digits) <= size {
		return digits
	}
	var b strings.Builder
	n := len(digits)
	first := n % size
	if first == 0 {
		first = size
	}
	b.WriteString(digits[:first])
	for i := first; i < n; i += size {
		b.WriteByte(sep)
		b.WriteString(digits[i : i+size])
	}
	return b.String()
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
