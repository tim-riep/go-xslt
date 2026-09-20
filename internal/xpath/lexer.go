package xpath

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type tokKind int

const (
	tEOF tokKind = iota
	tLParen
	tRParen
	tLBracket
	tRBracket
	tDot        // .
	tDotDot     // ..
	tAt         // @
	tComma      // ,
	tColonCol   // ::
	tSlash      // /
	tSlashSlash // //
	tPipe       // |
	tPlus
	tMinus
	tEq
	tNeq
	tLt
	tLe
	tGt
	tGe
	tStar // * (multiply or wildcard)
	tDollar
	tNumber
	tLiteral  // quoted string
	tName     // NCName or QName
	tOp       // and/or/div/mod/to/idiv (operator keyword)
	tQuestion // ?
	tLBrace   // {
	tRBrace   // }
	tAssign   // :=
	tColon    // : (map entry separator)
	tConcat   // ||
	tBang     // !
	tArrow    // =>
	tNodegt   // >> (node after)
	tNodelt   // << (node before)
	tHash     // #
)

type token struct {
	kind tokKind
	text string
}

type lexer struct {
	src    string
	pos    int
	tokens []token
}

// lex tokenizes an XPath expression applying the special disambiguation rules
// for '*' and the operator names (and, or, div, mod) and function names.
func lex(src string) ([]token, error) {
	l := &lexer{src: src}
	for {
		l.skipSpace()
		if l.pos >= len(l.src) {
			l.tokens = append(l.tokens, token{kind: tEOF})
			return l.tokens, nil
		}
		c := l.src[l.pos]
		switch {
		case c == '(' && l.peek(1) == ':':
			if err := l.skipComment(); err != nil {
				return nil, err
			}
		case c == '(':
			l.emit(tLParen, "(")
		case c == ')':
			l.emit(tRParen, ")")
		case c == '[':
			l.emit(tLBracket, "[")
		case c == ']':
			l.emit(tRBracket, "]")
		case c == '@':
			l.emit(tAt, "@")
		case c == ',':
			l.emit(tComma, ",")
		case c == '|':
			if l.peek(1) == '|' {
				l.pos += 2
				l.push(tConcat, "||")
			} else {
				l.emit(tPipe, "|")
			}
		case c == '+':
			l.emit(tPlus, "+")
		case c == '-':
			l.emit(tMinus, "-")
		case c == '=':
			if l.peek(1) == '>' {
				l.pos += 2
				l.push(tArrow, "=>")
			} else {
				l.emit(tEq, "=")
			}
		case c == '$':
			l.emit(tDollar, "$")
		case c == '?':
			l.emit(tQuestion, "?")
		case c == '#':
			l.emit(tHash, "#")
		case c == '{':
			l.emit(tLBrace, "{")
		case c == '}':
			l.emit(tRBrace, "}")
		case c == '!':
			if l.peek(1) == '=' {
				l.pos += 2
				l.push(tNeq, "!=")
			} else {
				l.emit(tBang, "!")
			}
		case c == '<':
			if l.peek(1) == '=' {
				l.pos += 2
				l.push(tLe, "<=")
			} else if l.peek(1) == '<' {
				l.pos += 2
				l.push(tNodelt, "<<")
			} else {
				l.emit(tLt, "<")
			}
		case c == '>':
			if l.peek(1) == '=' {
				l.pos += 2
				l.push(tGe, ">=")
			} else if l.peek(1) == '>' {
				l.pos += 2
				l.push(tNodegt, ">>")
			} else {
				l.emit(tGt, ">")
			}
		case c == '/':
			if l.peek(1) == '/' {
				l.pos += 2
				l.push(tSlashSlash, "//")
			} else {
				l.emit(tSlash, "/")
			}
		case c == ':':
			if l.peek(1) == ':' {
				l.pos += 2
				l.push(tColonCol, "::")
			} else if l.peek(1) == '=' {
				l.pos += 2
				l.push(tAssign, ":=")
			} else {
				l.emit(tColon, ":")
			}
		case c == '.':
			if l.peek(1) == '.' {
				l.pos += 2
				l.push(tDotDot, "..")
			} else if isDigit(l.peek(1)) {
				if err := l.number(); err != nil {
					return nil, err
				}
			} else {
				l.emit(tDot, ".")
			}
		case c == '*':
			l.emitStarOrName()
		case c == '\'' || c == '"':
			if err := l.literal(c); err != nil {
				return nil, err
			}
		case isDigit(c):
			if err := l.number(); err != nil {
				return nil, err
			}
		case c == 'Q' && l.peek(1) == '{':
			l.bracedName()
		default:
			// A name token must begin with an XML NameStartChar. Anything
			// else — including a non-ASCII character the Name production
			// excludes, such as U+00B5 MICRO SIGN — is a syntax error rather
			// than the start of a QName (error-XPST0003o).
			r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
			if !isNameStart(r) {
				return nil, fmt.Errorf("err:XPST0003: unexpected character %q", string(r))
			}
			l.name()
		}
	}
}

func (l *lexer) emit(k tokKind, s string) {
	l.pos += len(s)
	l.push(k, s)
}

func (l *lexer) push(k tokKind, s string) {
	l.tokens = append(l.tokens, token{kind: k, text: s})
}

func (l *lexer) peek(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\n', '\r':
			l.pos++
		default:
			return
		}
	}
}

// skipComment consumes a nestable XPath comment (: … :) starting at l.pos.
func (l *lexer) skipComment() error {
	depth := 0
	for l.pos < len(l.src) {
		if l.pos+1 < len(l.src) && l.src[l.pos] == '(' && l.src[l.pos+1] == ':' {
			depth++
			l.pos += 2
			continue
		}
		if l.pos+1 < len(l.src) && l.src[l.pos] == ':' && l.src[l.pos+1] == ')' {
			depth--
			l.pos += 2
			if depth == 0 {
				return nil
			}
			continue
		}
		l.pos++
	}
	return fmt.Errorf("unterminated comment")
}

// bracedName reads a URIQualifiedName Q{uri}local as a single tName token.
func (l *lexer) bracedName() {
	start := l.pos
	l.pos += 2 // consume "Q{"
	for l.pos < len(l.src) && l.src[l.pos] != '}' {
		l.pos++
	}
	if l.pos < len(l.src) {
		l.pos++ // consume '}'
	}
	for l.pos < len(l.src) {
		r, sz := utf8.DecodeRuneInString(l.src[l.pos:])
		if isNameChar(r) {
			l.pos += sz
		} else {
			break
		}
	}
	l.push(tName, l.src[start:l.pos])
}

func (l *lexer) literal(quote byte) error {
	// A doubled delimiter inside the literal stands for one literal quote
	// character ("abc""def" is abc"def — XPath A.1 EscapeQuot/EscapeApos;
	// serialize-json-120).
	pos := l.pos + 1
	var text strings.Builder
	for {
		end := strings.IndexByte(l.src[pos:], quote)
		if end < 0 {
			return fmt.Errorf("unterminated string literal")
		}
		text.WriteString(l.src[pos : pos+end])
		pos += end + 1
		if pos < len(l.src) && l.src[pos] == quote {
			text.WriteByte(quote)
			pos++
			continue
		}
		break
	}
	l.pos = pos
	l.push(tLiteral, text.String())
	return nil
}

// number reads a numeric literal. The literal must be delimited: a NameStartChar
// or '.' glued to it ("10div 3", "1.2.3") is XPST0003, not "10 div 3" (XPath
// A.2.2 terminal delimitation — K-NumericDivide-37, K-NumericMod-22).
func (l *lexer) number() error {
	start := l.pos
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) {
		r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
		// ':' IS a delimiter after a numeric literal — it is the map
		// constructor's key separator (map{1: 'x'} — maps-014). Every other
		// NameStartChar (and '.') would continue the token and is therefore
		// the missing-delimiter error.
		if r != ':' && (r == '.' || isNameStart(r)) {
			return fmt.Errorf("err:XPST0003: numeric literal %q must be followed by a delimiter", l.src[start:l.pos])
		}
	}
	l.push(tNumber, l.src[start:l.pos])
	return nil
}

// emitStarOrName decides whether '*' is the multiply operator or a wildcard
// name test, per the XPath disambiguation rule.
func (l *lexer) emitStarOrName() {
	if l.precededByOperand() {
		l.emit(tOp, "*") // multiply
	} else {
		l.emit(tStar, "*") // wildcard
	}
}

func (l *lexer) name() {
	start := l.pos
	for l.pos < len(l.src) {
		r, sz := utf8.DecodeRuneInString(l.src[l.pos:])
		if r == ':' {
			// A single ':' joins a QName (prefix:local), but '::' (axis) and
			// ':=' (let/for assignment — $f:= must not swallow the '=') must
			// not be absorbed into the name.
			if l.pos+1 < len(l.src) && (l.src[l.pos+1] == ':' || l.src[l.pos+1] == '=') {
				break
			}
			l.pos += sz
			continue
		}
		if isNameChar(r) {
			l.pos += sz
			continue
		}
		break
	}
	text := l.src[start:l.pos]
	// Operator-name disambiguation: and/or/div/mod are operators only when an
	// operand precedes them.
	if isKeywordOp(text) && l.precededByOperand() {
		l.push(tOp, text)
		return
	}
	l.push(tName, text)
}

// precededByOperand reports whether the previous token allows the next '*' or
// name keyword to be treated as a binary operator.
func (l *lexer) precededByOperand() bool {
	if len(l.tokens) == 0 {
		return false
	}
	last := l.tokens[len(l.tokens)-1]
	if last.kind == tName && strings.HasSuffix(last.text, ":") {
		// An incomplete QName ("xsl:") is a PREFIX awaiting its local part, so
		// the '*' that follows is a wildcard name test, not multiplication
		// (catalog-012: "self::xsl:* except self::xsl:output").
		return false
	}
	switch last.kind {
	case tLParen, tLBracket, tComma, tColonCol, tSlash, tSlashSlash, tAt, tDollar,
		tPipe, tPlus, tMinus, tEq, tNeq, tLt, tLe, tGt, tGe, tOp,
		tQuestion, tLBrace, tAssign, tConcat, tBang, tArrow, tColon, tHash:
		return false
	default:
		// Note: tStar is intentionally treated as an operand (it is only ever
		// emitted for a wildcard name test), so a following keyword/'*' is an
		// operator.
		return true
	}
}

// isKeywordOp reports whether a name is an infix/operator keyword (only treated
// as an operator when an operand precedes it).
func isKeywordOp(text string) bool {
	switch text {
	case "and", "or", "div", "mod", "to", "idiv",
		"eq", "ne", "lt", "le", "gt", "ge", "is",
		"union", "intersect", "except",
		"instance", "treat", "castable", "cast":
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isNameStart / isNameChar implement the XML NameStartChar / NameChar
// productions exactly (the same tables the XSD \i / \c regex escapes use).
// unicode.IsLetter would be WRONG here: it admits characters the XML Name
// production deliberately excludes — U+00B5 MICRO SIGN, for instance, is an
// Ll letter but not a NameStartChar, so an expression naming it is a syntax
// error (error-XPST0003o).
func isNameStart(r rune) bool { return inRanges(r, xmlNameStartRanges) }

func isNameChar(r rune) bool { return inRanges(r, xmlNameCharRanges) }

func inRanges(r rune, rs [][2]rune) bool {
	for _, rg := range rs {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}
