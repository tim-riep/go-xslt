package xpath

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// slowJSTokenizer is the tokenizer as it was before fast mode: every token
// through json.Decoder.
func slowJSTokenizer(src string, opts jsOptions) *jsTokenizer {
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	return &jsTokenizer{dec: dec, src: src, opts: opts}
}

// jsRandom builds a random JSON text of depth d with the string forms the
// fast scanner must decode exactly as encoding/json does.
func jsRandom(r *rand.Rand, d int) string {
	ws := []string{"", " ", "\n", "\t ", "\r\n"}
	w := func() string { return ws[r.Intn(len(ws))] }
	str := func() string {
		parts := []string{`a`, `é`, `日本`, `\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`, `A`, `é`, `\u0000`,
			`😀`, `😀`, `\uDC00`, `\uD800`, `\uD800x`, `\uD800A`, `￿`, `￾`, "\xff", "\xed\xa0\x80", "\xc3", `😀`, ` `, `,`, `:`, `}`, `]`}
		n := r.Intn(5)
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(parts[r.Intn(len(parts))])
		}
		return `"` + b.String() + `"`
	}
	nums := []string{"0", "-0", "1", "-1", "1.5", "1e5", "1E-5", "-2.25E+3", "123456789012345678901234567890", "0.1", "1.0", "1e400", "-1e-400"}
	if d <= 0 {
		switch r.Intn(4) {
		case 0:
			return str()
		case 1:
			return nums[r.Intn(len(nums))]
		case 2:
			return []string{"true", "false", "null"}[r.Intn(3)]
		default:
			return "[]"
		}
	}
	switch r.Intn(3) {
	case 0:
		n := r.Intn(4)
		var b strings.Builder
		b.WriteString("{" + w())
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(w() + "," + w())
			}
			b.WriteString(str() + w() + ":" + w() + jsRandom(r, d-1))
		}
		b.WriteString(w() + "}")
		return b.String()
	case 1:
		n := r.Intn(4)
		var b strings.Builder
		b.WriteString("[" + w())
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(w() + "," + w())
			}
			b.WriteString(jsRandom(r, d-1))
		}
		b.WriteString(w() + "]")
		return b.String()
	default:
		return jsRandom(r, 0)
	}
}

// TestJSONFastScannerMatchesDecoder checks that, for every JSON text
// encoding/json accepts, the in-place scanner yields the tokens the Decoder
// yields (strings decoded by the same rules, numbers as their text), and that
// the two decoders built on it produce the same values; a text the Decoder
// rejects must be rejected by json.Valid too, so it keeps the Decoder's error.
func TestJSONFastScannerMatchesDecoder(t *testing.T) {
	corpus := []string{
		`{}`, `[]`, ` [ ] `, `"a"`, `""`, `1`, `-0`, `null`, `true`, `false`,
		`{"a":1,"b":[1,2,{"c":"d"}],"e":null,"f":true}`,
		`"😀 \uDC00 \uD800 \u0000 \/ \" \\ \b\f\n\r\t"`,
		"\"\xff\xfe\"", "\"\xed\xa0\x80\"", `"é日本😀"`, `{"":""}`, `[1e400,-1e-400,1.0,0.1]`,
		`{"a":{"a":{"a":{"a":[[[[1]]]]}}}}`, `{"a" : 1 , "b" : 2 }`, `["a","b"]`,
	}
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 3000; i++ {
		corpus = append(corpus, jsRandom(r, 1+r.Intn(4)))
	}
	for _, f := range []string{"../../test/conformance/qt3tests/fn/json-to-xml", "../../test/conformance/qt3tests/fn/parse-json", "../../test/conformance/qt3tests/fn/json-doc"} {
		files, _ := filepath.Glob(filepath.Join(f, "*.json"))
		for _, name := range files {
			b, err := os.ReadFile(name)
			if err == nil {
				corpus = append(corpus, string(b))
			}
		}
	}
	optSets := []jsOptions{{}, {escape: true}}
	valid := 0
	for _, src := range corpus {
		for _, opts := range optSets {
			fast := newJSTokenizer(src, opts)
			if !fast.fast {
				// The Decoder must reject it too, or fast mode is not reached
				// for a text it accepts.
				slow := slowJSTokenizer(src, opts)
				if _, err := jsDecodeOrdered(slow); err == nil && slow.trailing() == nil {
					t.Errorf("%q: Decoder accepts, json.Valid does not", src)
				}
				continue
			}
			valid++
			// Token streams.
			slow := slowJSTokenizer(src, opts)
			for n := 0; ; n++ {
				a, errA := slow.token()
				b, errB := fast.token()
				if (errA == nil) != (errB == nil) {
					t.Fatalf("%q token %d: slow err %v, fast err %v", src, n, errA, errB)
				}
				if errA != nil {
					break
				}
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("%q token %d: slow %#v (%T), fast %#v (%T)", src, n, a, a, b, b)
				}
			}
			// The ordered decoder (json-to-xml) and the map decoder (parse-json).
			va, errA := jsDecodeOrdered(slowJSTokenizer(src, opts))
			vb, errB := jsDecodeOrdered(newJSTokenizer(src, opts))
			if (errA == nil) != (errB == nil) || !reflect.DeepEqual(va, vb) {
				t.Fatalf("%q: ordered decode differs: %#v %v / %#v %v", src, va, errA, vb, errB)
			}
			for _, dup := range []string{"use-last", "use-first", "reject"} {
				o := opts
				o.duplicates = dup
				pa, errA := jsParseToken(slowJSTokenizer(src, o), o)
				pb, errB := jsParseToken(newJSTokenizer(src, o), o)
				if (errA == nil) != (errB == nil) {
					t.Fatalf("%q: parse differs: %v / %v", src, errA, errB)
				}
				if errA == nil && !reflect.DeepEqual(jsToPlain(pa), jsToPlain(pb)) {
					t.Fatalf("%q: parse differs: %#v / %#v", src, jsToPlain(pa), jsToPlain(pb))
				}
			}
		}
	}
	t.Logf("%d valid texts", valid)
	if valid < 1000 {
		t.Fatalf("only %d valid texts in the corpus", valid)
	}
}

// jsToPlain turns a parse-json result into comparable plain Go data.
func jsToPlain(o Object) any {
	switch v := o.(type) {
	case *Map:
		out := map[string]any{}
		v.Range(func(k Item, val Object) bool {
			out[itemString(k)] = jsToPlain(val)
			return true
		})
		return out
	case *Array:
		var out []any
		for _, m := range v.Members() {
			out = append(out, jsToPlain(m))
		}
		return out
	case *Atomic:
		return v.T.String() + ":" + v.Lexical()
	case Sequence:
		return len(v)
	}
	return o
}
