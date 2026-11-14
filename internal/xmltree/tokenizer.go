package xmltree

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenizer is the XML tokenizer behind parseRaw. It replaces
// encoding/xml's Decoder.RawToken (configured as parseRaw used to configure
// it: Strict, no AutoClose, an Entity map, charsetReader) and reproduces it
// exactly — the same tokens, the same byte offsets, the same accept/reject
// decision and the same error text, line numbers included — but over a
// string held whole in memory, which is what parseRaw has anyway:
//
//   - names, attribute values, character data, comments and PI data are
//     substrings of the source wherever encoding/xml's result equals the raw
//     text; only an entity or character reference or a CR (line-end
//     normalization) makes a value a new string;
//   - a token is a kind plus fields of the tokenizer, not an interface value,
//     and the attribute slice is reused from element to element, so the
//     per-token allocations of RawToken (the boxed StartElement/EndElement,
//     the []Attr, the copied names and text) are gone.
//
// FuzzTokenizer compares it with encoding/xml token for token. Where
// encoding/xml accepts something XML 1.0 forbids, this tokenizer accepts it
// too: the reference is the decoder parseRaw used, not the spec.
//
// stream.go's incremental reader runs it too, over a window of its input
// (stream_tok.go), so a streamed document's names and text are substrings
// of the window they were read in.
type tokenizer struct {
	s   string
	pos int // the decoder's InputOffset: everything before it is consumed
	err error
	// eofErr is what reading past the end reports: io.EOF, or the error a
	// charset decoder stopped with (encoding/xml's reader would return it at
	// that point).
	eofErr error
	entity map[string]string

	needClose bool // the last start tag was <x/>: its end tag comes next
	toClose   xml.Name

	// The current token. Which fields are set depends on the kind; data and
	// attrs are reused by the next call.
	name   xml.Name   // tokStart, tokEnd
	attrs  []xml.Attr // tokStart
	str    string     // tokText when raw, tokComment, tokPI (instruction)
	data   []byte     // tokText when !raw, tokDirective
	raw    bool       // tokText: the text is str, not data
	target string     // tokPI

	buf []byte
	// nameASCII: the name the last successful readName took is all ASCII.
	nameASCII bool

	// The streaming reader runs this tokenizer over a WINDOW of its input
	// (stream_tok.go): lineBase is the number of newlines before s, so an
	// error names the right line, and noDecode tells xmlDecl that the
	// input already arrives decoded (the window cannot be re-read through
	// a charset decoder). Both are zero for a whole-source parse.
	lineBase int
	noDecode bool
}

type tokKind uint8

const (
	tokStart tokKind = iota + 1
	tokEnd
	tokText
	tokComment
	tokPI
	tokDirective
)

func newTokenizer(src string, entity map[string]string) *tokenizer {
	return &tokenizer{s: src, eofErr: io.EOF, entity: entity}
}

// textBytes returns a tokText token's character data without copying a
// substring: the caller appends it somewhere.
func (t *tokenizer) textBytes() []byte { return t.data }

// syntaxError is encoding/xml's SyntaxError at the current position: its
// line is one more than the newlines consumed so far.
func (t *tokenizer) syntaxError(msg string) error {
	return &xml.SyntaxError{Msg: msg, Line: 1 + t.lineBase + strings.Count(t.s[:t.pos], "\n")}
}

func (t *tokenizer) getc() (byte, bool) {
	if t.err != nil {
		return 0, false
	}
	if t.pos >= len(t.s) {
		t.err = t.eofErr
		return 0, false
	}
	b := t.s[t.pos]
	t.pos++
	return b, true
}

func (t *tokenizer) mustgetc() (byte, bool) {
	b, ok := t.getc()
	if !ok && t.err == io.EOF {
		t.err = t.syntaxError("unexpected EOF")
	}
	return b, ok
}

// runOut is what mustgetc does when a scan reaches the end of the input.
func (t *tokenizer) runOut() {
	t.pos = len(t.s)
	if t.err == nil {
		t.err = t.eofErr
	}
	if t.err == io.EOF {
		t.err = t.syntaxError("unexpected EOF")
	}
}

func (t *tokenizer) ungetc() { t.pos-- }

func (t *tokenizer) space() {
	if t.err != nil {
		return
	}
	for t.pos < len(t.s) {
		switch t.s[t.pos] {
		case ' ', '\r', '\n', '\t':
			t.pos++
		default:
			return
		}
	}
	t.err = t.eofErr
}

// next reads the next token. At the end of the input it returns io.EOF.
func (t *tokenizer) next() (tokKind, error) {
	if t.err != nil {
		return 0, t.err
	}
	if t.needClose {
		t.needClose = false
		t.name = t.toClose
		return tokEnd, nil
	}
	b, ok := t.getc()
	if !ok {
		return 0, t.err
	}
	if b != '<' {
		t.ungetc()
		if !t.scanText(-1, false) {
			return 0, t.err
		}
		return tokText, nil
	}
	if b, ok = t.mustgetc(); !ok {
		return 0, t.err
	}
	switch b {
	case '/':
		name, ok := t.nsname()
		if !ok {
			if t.err == nil {
				t.err = t.syntaxError("expected element name after </")
			}
			return 0, t.err
		}
		t.space()
		if b, ok = t.mustgetc(); !ok {
			return 0, t.err
		}
		if b != '>' {
			t.err = t.syntaxError("invalid characters between </" + name.Local + " and >")
			return 0, t.err
		}
		t.name = name
		return tokEnd, nil

	case '?':
		target, ok := t.nameStr()
		if !ok {
			if t.err == nil {
				t.err = t.syntaxError("expected target name after <?")
			}
			return 0, t.err
		}
		t.space()
		if t.err != nil { // space ran off the end: the next read fails
			t.runOut()
			return 0, t.err
		}
		k := strings.Index(t.s[t.pos:], "?>")
		if k < 0 {
			t.runOut()
			return 0, t.err
		}
		data := t.s[t.pos : t.pos+k]
		t.pos += k + 2
		if target == "xml" && !t.xmlDecl(data) {
			return 0, t.err
		}
		t.target, t.str = target, data
		return tokPI, nil

	case '!':
		if b, ok = t.mustgetc(); !ok {
			return 0, t.err
		}
		switch b {
		case '-':
			if b, ok = t.mustgetc(); !ok {
				return 0, t.err
			}
			if b != '-' {
				t.err = t.syntaxError("invalid sequence <!- not part of <!--")
				return 0, t.err
			}
			// The first "--" must be followed by '>'.
			start := t.pos
			k := strings.Index(t.s[start:], "--")
			if k < 0 || start+k+2 >= len(t.s) {
				t.runOut()
				return 0, t.err
			}
			t.pos = start + k + 3
			if t.s[start+k+2] != '>' {
				t.err = t.syntaxError(`invalid sequence "--" not allowed in comments`)
				return 0, t.err
			}
			t.str = t.s[start : start+k]
			return tokComment, nil

		case '[':
			for i := 0; i < 6; i++ {
				if b, ok = t.mustgetc(); !ok {
					return 0, t.err
				}
				if b != "CDATA["[i] {
					t.err = t.syntaxError("invalid <![ sequence")
					return 0, t.err
				}
			}
			if !t.scanText(-1, true) {
				return 0, t.err
			}
			return tokText, nil
		}
		if !t.directive(b) {
			return 0, t.err
		}
		return tokDirective, nil
	}

	// A start tag.
	t.ungetc()
	name, ok := t.nsname()
	if !ok {
		if t.err == nil {
			t.err = t.syntaxError("expected element name after <")
		}
		return 0, t.err
	}
	t.attrs = t.attrs[:0]
	empty := false
	for {
		t.space()
		if b, ok = t.mustgetc(); !ok {
			return 0, t.err
		}
		if b == '/' {
			empty = true
			if b, ok = t.mustgetc(); !ok {
				return 0, t.err
			}
			if b != '>' {
				t.err = t.syntaxError("expected /> in element")
				return 0, t.err
			}
			break
		}
		if b == '>' {
			break
		}
		t.ungetc()
		an, ok := t.nsname()
		if !ok {
			if t.err == nil {
				t.err = t.syntaxError("expected attribute name in element")
			}
			return 0, t.err
		}
		t.space()
		if b, ok = t.mustgetc(); !ok {
			return 0, t.err
		}
		if b != '=' {
			t.err = t.syntaxError("attribute name without = in element")
			return 0, t.err
		}
		t.space()
		if b, ok = t.mustgetc(); !ok {
			return 0, t.err
		}
		if b != '"' && b != '\'' {
			t.err = t.syntaxError("unquoted or missing attribute value in element")
			return 0, t.err
		}
		if !t.scanText(int(b), false) {
			return 0, t.err
		}
		v := t.str
		if !t.raw {
			v = string(t.data)
		}
		t.attrs = append(t.attrs, xml.Attr{Name: an, Value: v})
	}
	if empty {
		t.needClose = true
		t.toClose = name
	}
	t.name = name
	return tokStart, nil
}

// xmlDecl applies an XML declaration the way encoding/xml does: a version
// other than 1.0 is an error, and an encoding other than UTF-8 re-reads the
// rest of the input through charsetReader.
func (t *tokenizer) xmlDecl(content string) bool {
	ver := procInstParam("version", content)
	if ver != "" && ver != "1.0" {
		t.err = fmt.Errorf("xml: unsupported version %q; only version 1.0 is supported", ver)
		return false
	}
	enc := procInstParam("encoding", content)
	if enc == "" || strings.EqualFold(enc, "utf-8") || t.noDecode {
		return true
	}
	in := strings.NewReader(t.s[t.pos:])
	r, err := charsetReader(enc, in)
	if err != nil {
		t.err = fmt.Errorf("xml: opening charset %q: %w", enc, err)
		return false
	}
	if r == nil {
		panic("CharsetReader returned a nil Reader for charset " + enc)
	}
	if r == io.Reader(in) { // passed through unchanged
		return true
	}
	rest, rerr := io.ReadAll(r)
	t.s = t.s[:t.pos] + string(rest)
	if rerr != nil {
		t.eofErr = rerr
	}
	return true
}

// procInstParam returns the quoted value of param="…" or param='…' in s, ""
// when there is none — encoding/xml's own (deliberately loose) reading of
// the XML declaration, kept identical: the first "param=" that is followed
// by a quote wins, wherever it stands.
func procInstParam(param, s string) string {
	param += "="
	i := 0
	var sep byte
	for i < len(s) {
		sub := s[i:]
		k := strings.Index(sub, param)
		if k < 0 || len(param)+k >= len(sub) {
			return ""
		}
		i += len(param) + k + 1
		if c := sub[len(param)+k]; c == '\'' || c == '"' {
			sep = c
			break
		}
	}
	if sep == 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], sep)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

// directive reads a <!…> declaration after its first byte b (a DOCTYPE and
// its internal subset, typically) into data. Quoted angle brackets do not
// nest; a comment inside is replaced by a space.
func (t *tokenizer) directive(b byte) bool {
	buf := append(t.buf[:0], b)
	var inquote byte
	depth := 0
	ok := true
	for {
		if b, ok = t.mustgetc(); !ok {
			t.buf = buf
			return false
		}
		if inquote == 0 && b == '>' && depth == 0 {
			break
		}
	HandleB:
		buf = append(buf, b)
		switch {
		case b == inquote:
			inquote = 0
		case inquote != 0:
			// in quotes, no special action
		case b == '\'' || b == '"':
			inquote = b
		case b == '>':
			depth--
		case b == '<':
			const open = "!--"
			for i := 0; i < len(open); i++ {
				if b, ok = t.mustgetc(); !ok {
					t.buf = buf
					return false
				}
				if b != open[i] {
					buf = append(buf, open[:i]...)
					depth++
					goto HandleB
				}
			}
			buf = buf[:len(buf)-1] // the '<'
			var b0, b1 byte
			for {
				if b, ok = t.mustgetc(); !ok {
					t.buf = buf
					return false
				}
				if b0 == '-' && b1 == '-' && b == '>' {
					break
				}
				b0, b1 = b1, b
			}
			buf = append(buf, ' ')
		}
	}
	t.buf = buf
	t.data = buf
	return true
}

// Byte classes for scanText's fast path: 0 copies the byte unexamined, 2
// copies it but the text then needs the character check, 1 stops the fast
// path (a byte the slow path has to look at).
var textClass, cdataClass, dqClass, sqClass [256]uint8

func init() {
	for _, tab := range []*[256]uint8{&textClass, &cdataClass, &dqClass, &sqClass} {
		for c := 0; c < 256; c++ {
			if c >= utf8.RuneSelf || (c < 0x20 && c != '\t' && c != '\n') {
				tab[c] = 2
			}
		}
		tab['\r'] = 1
	}
	for _, c := range "<&]>" {
		textClass[c] = 1
	}
	for _, c := range "]>" {
		cdataClass[c] = 1
	}
	for _, c := range "<&\"" {
		dqClass[c] = 1
	}
	for _, c := range "<&'" {
		sqClass[c] = 1
	}
}

// scanText reads character data: text up to the next '<' (quote < 0, !cdata),
// a quoted attribute value up to its closing quote (quote is the quote
// byte), or a CDATA section up to "]]>" (cdata). The result is str (raw) or
// data. It is encoding/xml's Decoder.text, step for step: line ends are
// normalized, references replaced, and only once the whole run is read is
// every character checked — so which error a bad run reports, and the
// offset and line it reports it at, are the decoder's.
func (t *tokenizer) scanText(quote int, cdata bool) bool {
	var class *[256]uint8
	switch {
	case cdata:
		class = &cdataClass
	case quote == '"':
		class = &dqClass
	case quote == '\'':
		class = &sqClass
	default:
		class = &textClass
	}
	s := t.s
	var b0, b1 byte // the last two bytes read (reset by a reference)
	trunc := 0
	start := t.pos
	end := 0
	raw := true // the text so far is s[start:t.pos]; else it is buf
	buf := t.buf[:0]
	check := false
Input:
	for {
		// The fast path copies a run of bytes that need no look; a \n right
		// after a \r is not one (the pair becomes a single \n).
		if p := t.pos; p < len(s) && class[s[p]] != 1 && b1 != '\r' {
			for p < len(s) {
				c := class[s[p]]
				if c == 1 {
					break
				}
				if c == 2 {
					check = true
				}
				p++
			}
			if !raw {
				buf = append(buf, s[t.pos:p]...)
			}
			if p-t.pos >= 2 {
				b0 = s[p-2]
			} else {
				b0 = b1
			}
			b1 = s[p-1]
			t.pos = p
		}
		b, ok := t.getc()
		if !ok {
			if cdata {
				if t.err == io.EOF {
					t.err = t.syntaxError("unexpected EOF in CDATA section")
				}
				return false
			}
			end = t.pos
			break Input
		}
		if class[b] == 2 { // the fast path was skipped (after a \r)
			check = true
		}
		// A CDATA section ends with ]]>, which is an error in ordinary text
		// but allowed in a quoted string.
		if quote < 0 && b0 == ']' && b1 == ']' && b == '>' {
			if cdata {
				trunc = 2
				end = t.pos - 1
				break Input
			}
			t.err = t.syntaxError("unescaped ]]> not in CDATA section")
			return false
		}
		if b == '<' && !cdata {
			if quote >= 0 {
				t.err = t.syntaxError("unescaped < inside quoted string")
				return false
			}
			t.ungetc()
			end = t.pos
			break Input
		}
		if quote >= 0 && b == byte(quote) {
			end = t.pos - 1
			break Input
		}
		if b == '&' && !cdata {
			amp := t.pos - 1
			if raw {
				buf = append(buf, s[start:amp]...)
				raw = false
			}
			text, haveText := "", false
			if b, ok = t.mustgetc(); !ok {
				return false
			}
			if b == '#' {
				if b, ok = t.mustgetc(); !ok {
					return false
				}
				base := 10
				if b == 'x' {
					base = 16
					if b, ok = t.mustgetc(); !ok {
						return false
					}
				}
				digits := t.pos - 1
				for '0' <= b && b <= '9' ||
					base == 16 && 'a' <= b && b <= 'f' ||
					base == 16 && 'A' <= b && b <= 'F' {
					if b, ok = t.mustgetc(); !ok {
						return false
					}
				}
				if b != ';' {
					t.ungetc()
				} else {
					n, err := strconv.ParseUint(s[digits:t.pos-1], base, 64)
					if err == nil && n <= unicode.MaxRune {
						text = string(rune(n))
						haveText = true
					}
				}
			} else {
				t.ungetc()
				if !t.readName() && t.err != nil {
					return false
				}
				if b, ok = t.mustgetc(); !ok {
					return false
				}
				if b != ';' {
					t.ungetc()
				} else if name := s[amp+1 : t.pos-1]; isXMLName(name) {
					switch name {
					case "lt":
						text, haveText = "<", true
					case "gt":
						text, haveText = ">", true
					case "amp":
						text, haveText = "&", true
					case "apos":
						text, haveText = "'", true
					case "quot":
						text, haveText = `"`, true
					default:
						if t.entity != nil {
							text, haveText = t.entity[name]
						}
					}
				}
			}
			if haveText {
				buf = append(buf, text...)
				check = true
				b0, b1 = 0, 0
				continue Input
			}
			ent := s[amp:t.pos]
			if ent[len(ent)-1] != ';' {
				ent += " (no semicolon)"
			}
			t.err = t.syntaxError("invalid character entity " + ent)
			return false
		}
		// Unescaped \r and \r\n become \n.
		if b == '\r' {
			if raw {
				buf = append(buf, s[start:t.pos-1]...)
				raw = false
			}
			buf = append(buf, '\n')
		} else if b1 == '\r' && b == '\n' {
			// skip: the \n is written already
		} else if !raw {
			buf = append(buf, b)
		}
		b0, b1 = b1, b
	}
	t.raw = raw
	if raw {
		t.str = s[start : end-trunc]
		t.buf = buf
		if check {
			return t.checkString(t.str)
		}
		return true
	}
	buf = buf[:len(buf)-trunc]
	t.buf = buf
	t.data = buf
	if check {
		return t.checkBytes(buf)
	}
	return true
}

// checkString and checkBytes reject character data that is not UTF-8 or
// holds a character outside the XML Char production.
func (t *tokenizer) checkString(d string) bool {
	for i := 0; i < len(d); {
		if c := d[i]; c < utf8.RuneSelf {
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				t.err = t.syntaxError(fmt.Sprintf("illegal character code %U", rune(c)))
				return false
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(d[i:])
		if !t.checkRune(r, n) {
			return false
		}
		i += n
	}
	return true
}

func (t *tokenizer) checkBytes(d []byte) bool {
	for i := 0; i < len(d); {
		if c := d[i]; c < utf8.RuneSelf {
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				t.err = t.syntaxError(fmt.Sprintf("illegal character code %U", rune(c)))
				return false
			}
			i++
			continue
		}
		r, n := utf8.DecodeRune(d[i:])
		if !t.checkRune(r, n) {
			return false
		}
		i += n
	}
	return true
}

func (t *tokenizer) checkRune(r rune, n int) bool {
	if r == utf8.RuneError && n == 1 {
		t.err = t.syntaxError("invalid UTF-8")
		return false
	}
	if !(r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF) {
		t.err = t.syntaxError(fmt.Sprintf("illegal character code %U", r))
		return false
	}
	return true
}

// nsname reads a name with at most one colon, which splits it into prefix
// (Space, as RawToken reports it) and local part.
func (t *tokenizer) nsname() (xml.Name, bool) {
	s, ok := t.nameStr()
	if !ok {
		return xml.Name{}, false
	}
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return xml.Name{Local: s}, true
	}
	if strings.IndexByte(s[i+1:], ':') >= 0 {
		return xml.Name{}, false
	}
	if i == 0 || i == len(s)-1 {
		return xml.Name{Local: s}, true
	}
	return xml.Name{Space: s[:i], Local: s[i+1:]}, true
}

// nameStr reads a name. A missing name leaves t.err unset (unless the input
// ended), for the caller to report in context.
func (t *tokenizer) nameStr() (string, bool) {
	start := t.pos
	if !t.readName() {
		return "", false
	}
	s := t.s[start:t.pos]
	// readName took only name bytes; when they were all ASCII, isXMLName's
	// verdict comes down to the first one.
	if t.nameASCII {
		if c := s[0]; c == '-' || c == '.' || '0' <= c && c <= '9' {
			t.err = t.syntaxError("invalid XML name: " + s)
			return "", false
		}
		return s, true
	}
	if !isXMLName(s) {
		t.err = t.syntaxError("invalid XML name: " + s)
		return "", false
	}
	return s, true
}

// readName consumes the bytes of a name: up to the first ASCII byte that
// cannot occur in one (every non-ASCII byte is taken; isXMLName judges them).
func (t *tokenizer) readName() bool {
	b, ok := t.mustgetc()
	if !ok {
		return false
	}
	if b < utf8.RuneSelf && !nameByte[b] {
		t.ungetc()
		return false
	}
	ascii := b < utf8.RuneSelf
	for p := t.pos; p < len(t.s); p++ {
		if c := t.s[p]; c >= utf8.RuneSelf {
			ascii = false
		} else if !nameByte[c] {
			t.pos = p
			t.nameASCII = ascii
			return true
		}
	}
	t.runOut()
	return false
}

// nameByte holds the ASCII bytes a name may contain.
var nameByte = func() (tab [utf8.RuneSelf]bool) {
	for c := 0; c < utf8.RuneSelf; c++ {
		tab[c] = 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
			c == '_' || c == ':' || c == '.' || c == '-'
	}
	return
}()

// isXMLName reports whether s is a name by encoding/xml's rules.
func isXMLName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if !nameByte[c] || i == 0 && (c == '-' || c == '.' || '0' <= c && c <= '9') {
				return false
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			return false
		}
		if i == 0 {
			if !inNameRanges(nameStartRanges, r) {
				return false
			}
		} else if !inNameRanges(nameCharRanges, r) {
			return false
		}
		i += n
	}
	return true
}
