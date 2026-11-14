package xmltree

import (
	"io"
	"strings"
	"unicode/utf8"
)

// streamTokenizer runs the tokenizer of parse.go over an io.Reader, a window
// at a time, for the streaming reader. encoding/xml's RawToken, which the
// reader used before, allocated for every token — the boxed token, a copy of
// every name, attribute value and text run, the attribute slice — and those
// were half of a streamed transformation's allocations; the tokenizer hands
// out substrings of its input and reuses its attribute slice.
//
// The window is the unconsumed rest of the input read so far plus whatever
// the last read brought in. A token is scanned from the window; when it
// reaches the window's end — the tokenizer reports that as an error, or
// hands back a text run that stops exactly there — more input is appended
// and the token is scanned again from its start, so every token the reader
// sees is complete, text runs included (encoding/xml too gave a text run
// whole). A token longer than a chunk grows the window to its own size, as
// the decoder's buffer grew. An error the tokenizer reports before the end
// of the input is retried the same way, since a window cut inside a
// multi-byte character or a reference looks like one; at the end of the
// input the tokenizer's verdict stands.
//
// The strings the tokenizer hands out are substrings of a window, and a node
// keeps its window alive as long as it lives — a bounded amount: the open
// elements and the record being built. What the reader keeps for the whole
// document (interned names, shared whitespace runs) is copied out of the
// window first.
type streamTokenizer struct {
	r     io.Reader
	tz    tokenizer
	base  int  // input offset of tz.s[0]
	eof   bool // r has been read to its end
	chunk int  // bytes read per fill (grown for a token longer than that)
}

// streamChunk is the size of one read.
const streamChunk = 64 << 10

func newStreamTokenizer(r io.Reader, chunk int) *streamTokenizer {
	if chunk <= 0 {
		chunk = streamChunk
	}
	return &streamTokenizer{r: r, tz: tokenizer{eofErr: io.EOF, noDecode: true}, chunk: chunk}
}

// offset is the input offset just after the last token (the decoder's
// InputOffset).
func (w *streamTokenizer) offset() int { return w.base + w.tz.pos }

// next reads the next token; at the end of the input it returns io.EOF.
func (w *streamTokenizer) next() (tokKind, error) {
	for {
		start, needClose, toClose := w.tz.pos, w.tz.needClose, w.tz.toClose
		kind, err := w.tz.next()
		if err == nil && (kind != tokText || w.tz.pos < len(w.tz.s) || w.eof) {
			return kind, nil
		}
		if w.eof || len(w.tz.s)-w.tz.pos > utf8.UTFMax-1 {
			// The input's end, or an error with unread input after it: not
			// the window's doing (a cut multi-byte character leaves at most
			// UTFMax-1 bytes unread; every other scan that needs more input
			// has consumed the window), so the verdict stands and a
			// malformed document fails where the decoder failed it, not
			// after being read to its end.
			return kind, err
		}
		// The token may have run into the end of the window: read on and
		// scan it again from its start, with the state it started from.
		w.tz.pos, w.tz.err, w.tz.needClose, w.tz.toClose = start, nil, needClose, toClose
		if err := w.fill(); err != nil {
			return 0, err
		}
	}
}

// fill drops the consumed part of the window and appends the next chunk of
// input. The unconsumed part (the token being scanned) is kept whole; a
// token already longer than a chunk doubles the window instead, so a long
// text run costs a number of scans logarithmic in its length.
func (w *streamTokenizer) fill() error {
	rest := w.tz.s[w.tz.pos:]
	w.tz.lineBase += strings.Count(w.tz.s[:w.tz.pos], "\n")
	w.base += w.tz.pos
	n := w.chunk
	if len(rest) > n {
		n = len(rest)
	}
	buf := make([]byte, len(rest)+n)
	copy(buf, rest)
	got, err := io.ReadFull(w.r, buf[len(rest):])
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		w.eof = true
		err = nil
	}
	if err != nil {
		return err
	}
	w.tz.s, w.tz.pos = string(buf[:len(rest)+got]), 0
	return nil
}
