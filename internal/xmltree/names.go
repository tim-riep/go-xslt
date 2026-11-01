package xmltree

import "unicode/utf8"

// The engine's one definition of an XML name: the NameStartChar and NameChar
// productions of XML 1.0 (5th edition) §2.3, the edition XDM 3.1, F&O 3.1 and
// Namespaces in XML 1.0 (3rd edition) refer to, and the one XML 1.1 shares.
// NCName is Name without ':' (Namespaces in XML §3).
//
// Everything that judges a name by the Name production goes through these
// predicates or through the tables behind them: the XPath lexer, the
// xs:NCName / xs:QName / xs:Name casts and XSD's \i \c escapes (which build
// their character classes from NameStartRanges / NameExtraRanges), the XSLT
// checks on constructed names (xsl:element, xsl:attribute, xsl:namespace,
// xsl:processing-instruction) and the parser's repair of names that
// encoding/xml's older tables (namechars.go) refuse.
//
// This is NOT the table the tokenizer applies: that one stays encoding/xml's
// (XML 1.0 2nd edition Appendix B, BMP only) so the tokenizer remains a
// step-for-step copy of RawToken. parseLenient11WithBaseStrip bridges the
// difference for the names this table allows and that one does not.

// nameStartRanges5e is NameStartChar, ':' included.
var nameStartRanges5e = [][2]rune{
	{':', ':'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'},
	{0xC0, 0xD6}, {0xD8, 0xF6}, {0xF8, 0x2FF}, {0x370, 0x37D}, {0x37F, 0x1FFF},
	{0x200C, 0x200D}, {0x2070, 0x218F}, {0x2C00, 0x2FEF}, {0x3001, 0xD7FF},
	{0xF900, 0xFDCF}, {0xFDF0, 0xFFFD}, {0x10000, 0xEFFFF},
}

// nameExtraRanges5e are the characters a NameChar may have beyond a
// NameStartChar.
var nameExtraRanges5e = [][2]rune{
	{'-', '-'}, {'.', '.'}, {'0', '9'}, {0xB7, 0xB7},
	{0x300, 0x36F}, {0x203F, 0x2040},
}

// NameStartRanges returns a copy of the NameStartChar ranges (':' included).
func NameStartRanges() [][2]rune { return append([][2]rune(nil), nameStartRanges5e...) }

// NameExtraRanges returns a copy of the ranges a NameChar adds to a
// NameStartChar.
func NameExtraRanges() [][2]rune { return append([][2]rune(nil), nameExtraRanges5e...) }

func inRanges5e(r rune, rs [][2]rune) bool {
	for _, rg := range rs {
		if r < rg[0] {
			return false // the tables are sorted
		}
		if r <= rg[1] {
			return true
		}
	}
	return false
}

// IsNameStartChar reports whether r is a NameStartChar (':' included).
func IsNameStartChar(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == ':'
	}
	return inRanges5e(r, nameStartRanges5e)
}

// IsNameChar reports whether r is a NameChar (':' included).
func IsNameChar(r rune) bool {
	if r < utf8.RuneSelf {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == ':' ||
			r >= '0' && r <= '9' || r == '-' || r == '.'
	}
	return inRanges5e(r, nameStartRanges5e) || inRanges5e(r, nameExtraRanges5e)
}

// IsNCNameStartChar is IsNameStartChar without ':'.
func IsNCNameStartChar(r rune) bool { return r != ':' && IsNameStartChar(r) }

// IsNCNameChar is IsNameChar without ':'.
func IsNCNameChar(r rune) bool { return r != ':' && IsNameChar(r) }

// IsNCName reports whether s is an NCName: one NameStartChar other than ':'
// followed by NameChars other than ':'. Invalid UTF-8 is no name.
func IsNCName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); {
		r, n := rune(s[i]), 1
		if r >= utf8.RuneSelf {
			r, n = utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				return false
			}
		}
		if i == 0 {
			if !IsNCNameStartChar(r) {
				return false
			}
		} else if !IsNCNameChar(r) {
			return false
		}
		i += n
	}
	return true
}

// IsName reports whether s is a Name (colons allowed anywhere, as in the XML
// 1.0 production).
func IsName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); {
		r, n := rune(s[i]), 1
		if r >= utf8.RuneSelf {
			r, n = utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				return false
			}
		}
		if i == 0 {
			if !IsNameStartChar(r) {
				return false
			}
		} else if !IsNameChar(r) {
			return false
		}
		i += n
	}
	return true
}
