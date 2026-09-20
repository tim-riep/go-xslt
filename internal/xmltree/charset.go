package xmltree

import (
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// decodeBOM transcodes a byte-order-marked UTF-16 source to UTF-8 and strips a
// UTF-8 BOM. It must run BEFORE xml.NewDecoder ever sees the bytes: the
// decoder tokenizes the prolog as UTF-8 to find the encoding declaration, and
// a 0xFF/0xFE first byte kills it there — CharsetReader can never fire.
// DecodeBOM is the exported form for callers that inspect the raw source
// themselves (the XSD validator's DTD entity harvest).
func DecodeBOM(src string) string { return decodeBOM(src) }

func decodeBOM(src string) string {
	var enc encoding.Encoding
	switch {
	case strings.HasPrefix(src, "\xFF\xFE"):
		enc = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM)
	case strings.HasPrefix(src, "\xFE\xFF"):
		enc = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM)
	}
	if enc != nil {
		if out, err := enc.NewDecoder().String(src); err == nil {
			src = out
		}
	}
	return strings.TrimPrefix(src, "\uFEFF")
}

// charsetReader resolves the encoding named in an XML declaration so the decoder
// can read non-UTF-8 documents. Go's encoding/xml errors out on any non-UTF-8
// encoding unless a CharsetReader is supplied. ASCII and UTF-8 pass through
// unchanged; ISO-8859-1/Latin-1 is handled directly; anything else is resolved
// via the IANA charset registry (golang.org/x/text), falling back to a
// pass-through so a misdeclared document still has a chance of parsing.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii",
		// A UTF-16 source reaches the decoder already transcoded to UTF-8 by
		// decodeBOM (the raw prolog is invalid UTF-8, so this hook could never
		// have fired for it) — the declared label must not double-decode.
		"utf-16", "utf-16le", "utf-16be", "utf16":
		return input, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1":
		return &latin1Reader{r: input}, nil
	}
	if enc, err := ianaindex.IANA.Encoding(label); err == nil && enc != nil {
		return transform.NewReader(input, enc.NewDecoder()), nil
	}
	return input, nil // best effort
}

// latin1Reader transcodes ISO-8859-1 bytes to UTF-8 on the fly.
type latin1Reader struct {
	r   io.Reader
	buf []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(l.buf) > 0 {
		n := copy(p, l.buf)
		l.buf = l.buf[n:]
		return n, nil
	}
	tmp := make([]byte, len(p))
	n, err := l.r.Read(tmp)
	var out []byte
	for _, b := range tmp[:n] {
		if b < 0x80 {
			out = append(out, b)
		} else {
			out = append(out, 0xc0|b>>6, 0x80|b&0x3f)
		}
	}
	n2 := copy(p, out)
	l.buf = out[n2:]
	return n2, err
}

// DecodeRetrievedText decodes the bytes of a retrieved text resource
// (fn:unparsed-text and friends) to a string: a byte-order mark identifies and
// strips itself, and a resource that is not valid UTF-8 but declares an
// encoding in an XML declaration is transcoded from THAT encoding
// (unparsed-text-2001's xiso-8859-1.xml). Bytes that are neither is left
// as-is, for the caller to reject as undecodable.
func DecodeRetrievedText(b []byte) string {
	s := decodeBOM(string(b))
	if utf8.ValidString(s) {
		return s
	}
	label := xmlDeclEncoding(s)
	if label == "" {
		return s
	}
	r, err := charsetReader(label, strings.NewReader(s))
	if err != nil {
		return s
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return s
	}
	return string(out)
}

// xmlDeclEncoding returns the encoding named by a leading XML declaration, or
// "" when there is none.
func xmlDeclEncoding(s string) string {
	if !strings.HasPrefix(s, "<?xml") {
		return ""
	}
	end := strings.Index(s, "?>")
	if end < 0 {
		return ""
	}
	decl := s[:end]
	i := strings.Index(decl, "encoding")
	if i < 0 {
		return ""
	}
	rest := decl[i+len("encoding"):]
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[eq+1:], " \t\r\n")
	if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
		return ""
	}
	q := rest[0]
	rest = rest[1:]
	j := strings.IndexByte(rest, q)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
