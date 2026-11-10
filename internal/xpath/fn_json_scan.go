package xpath

import (
	"encoding/json"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// newJSTokenizer makes the tokenizer for src. A document json.Valid accepts
// is scanned in place (the fast mode below): json.Decoder.Token decodes
// every scalar through Decode, which costs several allocations per token —
// among them a SyntaxError it builds for the byte after a top-level value and
// discards — and was a third of the time of a JSON round trip. A document
// json.Valid rejects goes through the Decoder exactly as before, so the error
// it reports is unchanged.
func newJSTokenizer(src string, opts jsOptions) *jsTokenizer {
	if json.Valid(unsafe.Slice(unsafe.StringData(src), len(src))) {
		return &jsTokenizer{src: src, opts: opts, fast: true}
	}
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	return &jsTokenizer{dec: dec, src: src, opts: opts}
}

// skipSpace moves past JSON whitespace (and, when seps is set, past the ','
// and ':' separators, as Decoder.Token does before a token).
func (t *jsTokenizer) skipSpace(seps bool) {
	for t.pos < len(t.src) {
		switch t.src[t.pos] {
		case ' ', '\t', '\r', '\n':
		case ',', ':':
			if !seps {
				return
			}
		default:
			return
		}
		t.pos++
	}
}

// more is Decoder.More: whether another element follows in the current
// array or object.
func (t *jsTokenizer) more() bool {
	if !t.fast {
		return t.dec.More()
	}
	t.skipSpace(false)
	return t.pos < len(t.src) && t.src[t.pos] != ']' && t.src[t.pos] != '}'
}

// skipClose consumes the ']' or '}' that ends the current array or object.
func (t *jsTokenizer) skipClose() {
	if !t.fast {
		t.dec.Token()
		return
	}
	t.skipSpace(false)
	t.pos++
}

// trailing reports FOJS0001 when anything but whitespace follows the value
// (jsCheckTrailing); json.Valid has already ruled that out in fast mode.
func (t *jsTokenizer) trailing() error {
	if t.fast {
		return nil
	}
	return jsCheckTrailing(t.src, t.dec)
}

// fastToken is token for a document json.Valid accepted: the same tokens
// Decoder.Token (with UseNumber) yields, with a string decoded by jsUnquote
// and then jsDecodeString from its raw text.
func (t *jsTokenizer) fastToken() (json.Token, error) {
	t.skipSpace(true)
	if t.pos >= len(t.src) {
		return nil, errJSEOF
	}
	c := t.src[t.pos]
	switch c {
	case '{', '[', '}', ']':
		t.pos++
		return json.Delim(c), nil
	case '"':
		i := t.pos + 1
		for t.src[i] != '"' {
			if t.src[i] == '\\' {
				i++
			}
			i++
		}
		raw := t.src[t.pos+1 : i]
		t.pos = i + 1
		s, err := jsDecodeString(raw, jsUnquote(raw), t.opts)
		if err != nil {
			return nil, err
		}
		return s, nil
	case 't':
		t.pos += 4
		return true, nil
	case 'f':
		t.pos += 5
		return false, nil
	case 'n':
		t.pos += 4
		return nil, nil
	}
	i := t.pos + 1
	for i < len(t.src) {
		switch t.src[i] {
		case ' ', '\t', '\r', '\n', ',', ']', '}':
			goto done
		}
		i++
	}
done:
	n := json.Number(t.src[t.pos:i])
	t.pos = i
	return n, nil
}

// errJSEOF is the error token() reports at the end of the input in fast
// mode; the callers only ever ask for a token the grammar promises, so it is
// not reached for a document json.Valid accepted.
var errJSEOF = jsError("unexpected end of JSON input")

type jsError string

func (e jsError) Error() string { return string(e) }

// jsUnquote decodes the raw text of a JSON string that json.Valid accepted,
// exactly as encoding/json does: escapes replaced, a \u escape of an unpaired
// surrogate and a byte sequence that is not UTF-8 both become U+FFFD. A
// string with no escape and only UTF-8 is returned as it is.
func jsUnquote(raw string) string {
	i := 0
	for i < len(raw) && raw[i] != '\\' && raw[i] < utf8.RuneSelf {
		i++
	}
	if i == len(raw) {
		return raw
	}
	if strings.IndexByte(raw[i:], '\\') < 0 && utf8.ValidString(raw[i:]) {
		return raw
	}
	b := make([]byte, 0, len(raw)+2*utf8.UTFMax)
	b = append(b, raw[:i]...)
	for i < len(raw) {
		c := raw[i]
		switch {
		case c == '\\':
			i++
			switch raw[i] {
			case '"', '\\', '/':
				b = append(b, raw[i])
				i++
			case 'b':
				b = append(b, '\b')
				i++
			case 'f':
				b = append(b, '\f')
				i++
			case 'n':
				b = append(b, '\n')
				i++
			case 'r':
				b = append(b, '\r')
				i++
			case 't':
				b = append(b, '\t')
				i++
			case 'u':
				rr := jsHex4(raw[i+1:])
				i += 5
				if utf16.IsSurrogate(rr) {
					rr1 := rune(-1)
					if i+5 < len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
						rr1 = jsHex4(raw[i+2:])
					}
					if dec := utf16.DecodeRune(rr, rr1); dec != utf8.RuneError {
						i += 6
						b = utf8.AppendRune(b, dec)
						break
					}
					rr = utf8.RuneError
				}
				b = utf8.AppendRune(b, rr)
			}
		case c < utf8.RuneSelf:
			b = append(b, c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(raw[i:])
			if r == utf8.RuneError && size == 1 {
				b = utf8.AppendRune(b, utf8.RuneError)
				i++
				continue
			}
			b = append(b, raw[i:i+size]...)
			i += size
		}
	}
	return string(b)
}

// jsHex4 reads the four hex digits of a \u escape; -1 when s does not start
// with four hex digits.
func jsHex4(s string) rune {
	if len(s) < 4 {
		return -1
	}
	var r rune
	for i := 0; i < 4; i++ {
		c := s[i]
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}
