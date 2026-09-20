package xslt

import (
	"fmt"
	"github.com/tim-riep/go-xslt/internal/xmltree"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xpath"
)

// avt is a compiled attribute value template: a sequence of literal text and
// embedded XPath expressions (the {…} parts).
type avt struct {
	parts []avtPart
}

type avtPart struct {
	literal string
	expr    *xpath.Parsed // nil for literal parts
}

// isConstant reports whether the AVT has no embedded expressions.
func (a *avt) isConstant() (string, bool) {
	if len(a.parts) == 0 {
		return "", true
	}
	if len(a.parts) == 1 && a.parts[0].expr == nil {
		return a.parts[0].literal, true
	}
	return "", false
}

// parseAVT compiles an attribute value template string.
func parseAVT(s string) (*avt, error) { return parseAVTFor(nil, s) }

// parseAVTFor is parseAVT for an AVT WRITTEN ON el, so that a schema component
// name inside an embedded expression resolves against the stylesheet's
// imported components exactly as it would in a plain @select
// (validation-0801/1001/1002 write "{... instance of schema-element(t:x)}";
// notation-0002 writes "{$q castable as n:nota}"). A nil el, or a stylesheet
// with no schema in scope, is exactly the old behaviour.
func parseAVTFor(el *xmltree.Node, s string) (*avt, error) {
	a := &avt{}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			a.parts = append(a.parts, avtPart{literal: lit.String()})
			lit.Reset()
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '{':
			if i+1 < len(s) && s[i+1] == '{' {
				lit.WriteByte('{')
				i += 2
				continue
			}
			flush()
			expr, j, err := scanAVTExpr(s, i+1)
			if err != nil {
				return nil, err
			}
			// An expression that is empty (or holds nothing but whitespace
			// and XPath comments) contributes nothing at all — it is not a
			// syntax error (bug 29226 / avt-2102: a="x{}y{}z" is "xyz").
			if isBlankXPath(expr) {
				i = j
				continue
			}
			parsed, perr := parseXPathFor(el, expr)
			if perr != nil {
				return nil, perr
			}
			a.parts = append(a.parts, avtPart{expr: parsed})
			i = j
		case '}':
			if i+1 < len(s) && s[i+1] == '}' {
				lit.WriteByte('}')
				i += 2
				continue
			}
			// An unescaped right curly bracket in a FIXED part of an
			// attribute/text value template is a static error (error-0370a).
			return nil, fmt.Errorf("err:XTSE0370: unescaped '}' in %q", s)
		default:
			lit.WriteByte(c)
			i++
		}
	}
	flush()
	return a, nil
}

// scanAVTExpr reads the XPath expression starting at s[i] (just past the
// opening '{') and returns it together with the index just past its closing
// '}'. Braces are only significant OUTSIDE string literals and XPath
// comments, so an unbalanced brace inside either — '}' in a string literal
// (string-095) or in a (: comment :) (avt-1202/2102) — does not end the
// expression; a nested '{' (a map/array constructor, or a text value
// template inside a nested constructor) raises the nesting level.
func scanAVTExpr(s string, i int) (string, int, error) {
	start := i
	depth := 1
	for i < len(s) {
		switch c := s[i]; c {
		case '\'', '"':
			// A string literal: doubling the delimiter escapes it, which this
			// scan gets for free (the closing quote simply reopens a new,
			// immediately-closed literal).
			i++
			for i < len(s) && s[i] != c {
				i++
			}
			if i >= len(s) {
				return "", 0, fmt.Errorf("unterminated string literal in attribute value template %q", s)
			}
			i++
		case '(':
			if i+1 < len(s) && s[i+1] == ':' {
				end, err := skipXPathComment(s, i)
				if err != nil {
					return "", 0, err
				}
				i = end
				continue
			}
			i++
		case '{':
			depth++
			i++
		case '}':
			depth--
			if depth == 0 {
				return s[start:i], i + 1, nil
			}
			i++
		default:
			i++
		}
	}
	return "", 0, fmt.Errorf("unbalanced '{' in attribute value template %q", s)
}

// skipXPathComment returns the index just past the (: … :) comment starting at
// s[i]; XPath comments nest.
func skipXPathComment(s string, i int) (int, error) {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "(:"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], ":)"):
			depth--
			i += 2
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return 0, fmt.Errorf("unterminated XPath comment in attribute value template %q", s)
}

// isBlankXPath reports whether an expression consists solely of whitespace and
// XPath comments, i.e. contains no expression at all.
func isBlankXPath(expr string) bool {
	i := 0
	for i < len(expr) {
		switch {
		case expr[i] == ' ' || expr[i] == '\t' || expr[i] == '\n' || expr[i] == '\r':
			i++
		case strings.HasPrefix(expr[i:], "(:"):
			end, err := skipXPathComment(expr, i)
			if err != nil {
				return false
			}
			i = end
		default:
			return false
		}
	}
	return true
}
