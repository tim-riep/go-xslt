package xslt

import (
	"regexp"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// checkPackageAttrs validates the attributes that only xsl:package carries:
// @package-version against the PackageVersion grammar (XSLT 3.0 §3.5.2) and
// @declared-modes as an XSLT boolean. It runs AFTER shadow-attribute
// expansion (applyStatic, usewhen.go), so package-version-007's invalid
// literal overridden by a valid _package-version is accepted while
// package-version-908's reverse case is not.
func checkPackageAttrs(root *xmltree.Node) error {
	if v, ok := root.AttrLocal("package-version"); ok {
		if !isPackageVersion(v) {
			return errAt(root, "err:XTSE0020: package-version=%q is not a valid package version", v)
		}
	}
	if v, ok := root.AttrLocal("declared-modes"); ok {
		if !isXSLTBooleanLexical(v) {
			return errAt(root, "err:XTSE0020: declared-modes=%q is not an XSLT boolean", v)
		}
	}
	return nil
}

// isPackageVersion implements
//
//	PackageVersion ::= NumericPart ("-" NamePart)*
//	NumericPart    ::= Digits ("." Digits)*
//	NamePart       ::= NCName
//
// (XSLT 3.0 §3.5.2). Whitespace is NOT trimmed: the attribute is not of a
// whitespace-collapsing type.
func isPackageVersion(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, "-")
	// NumericPart
	for _, seg := range strings.Split(parts[0], ".") {
		if seg == "" {
			return false
		}
		for _, r := range seg {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	for _, np := range parts[1:] {
		if !reXMLNCName.MatchString(np) {
			return false
		}
	}
	return true
}

// reXMLNCName is the real XML NCName production (validate.go's isNCName is a
// Unicode-category approximation that rejects e.g. the CJK punctuation
// package-version-005 deliberately uses, and accepts private-use planes
// package-version-902 deliberately uses).
var reXMLNCName = regexp.MustCompile("^[" + xpath.NCNameStartClass + "][" + xpath.NCNameCharClass + "]*$")

// isXSLTBooleanLexical reports whether v is one of the ten lexical forms XSLT
// 3.0 accepts for an attribute of type xsl:yes-or-no (surrounding whitespace
// is allowed and stripped).
func isXSLTBooleanLexical(v string) bool {
	switch strings.TrimSpace(v) {
	case "yes", "no", "true", "false", "1", "0":
		return true
	}
	return false
}
