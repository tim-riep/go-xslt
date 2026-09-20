package xpath

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

func init() {
	coreFuncs["parse-json"] = fnJSParseJSON
	coreFuncs["json-doc"] = fnJSJSONDoc
	coreFuncs["serialize"] = fnJSSerialize
	coreFuncs["json-to-xml"] = fnJSJSONToXML
	coreFuncs["xml-to-json"] = fnJSXMLToJSON
}

// jsXFNS is the namespace URI for the elements produced by fn:json-to-xml and
// consumed by fn:xml-to-json, per the XSLT/XQuery 3.1 serialization spec.
const jsXFNS = "http://www.w3.org/2005/xpath-functions"

// jsObj is an order-preserving JSON object. fn:xml-to-json and the json output
// method must emit map entries in document order, which Go's map type loses.
type jsObj struct {
	keys []string
	vals []any
	raw  []bool // key is already JSON-escaped; emit verbatim
}

func (o *jsObj) put(k string, v any) {
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
	o.raw = append(o.raw, false)
}

// putRaw records a key that is already JSON-escaped (escaped-key="true") and
// must be emitted verbatim.
func (o *jsObj) putRaw(k string, v any) {
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
	o.raw = append(o.raw, true)
}

// jsPreEscaped is a string value that is already JSON-escaped (an element with
// escaped="true"); it is emitted inside quotes verbatim.
type jsPreEscaped string

// jsNodeText is the result of serializing a NODE under json-node-output-method.
// It is emitted as an ordinary JSON string except that the use-character-maps
// mapping is not applied to it again — that nested serialization already
// applied it (serNodeString), and mapping twice would corrupt the text.
type jsNodeText string

// jsMarshal renders a Go value graph (jsObj, []any, json.RawMessage, string,
// bool, float64, nil) as JSON. It is a self-contained serializer so we control
// key order and string escaping exactly (no HTML escaping of <, >, &; no key
// sorting), matching the XPath 3.1 JSON output rules.
func jsMarshal(v any) (string, error) { return jsMarshalEnc(v, 0) }

// jsEmitOpts carries the per-serialization knobs the JSON writer honours: the
// largest code point the output encoding can carry directly (0 = unrestricted)
// and the use-character-maps mapping, which replaces individual characters
// with arbitrary strings INSTEAD OF the escaping this writer would otherwise
// apply (that is exactly what output-0708's "/" -> "/" map is for: suppressing
// the \/ escape).
type jsEmitOpts struct {
	maxRune rune
	charMap map[string]string
	// indent pretty-prints: each object/array member on its own line, two
	// spaces per level. fn:xml-to-json's 'indent' option asks for it; the
	// exact layout is implementation-defined, so this picks the shape the
	// suite's own round-trip assertion normalises away (whitespace only ever
	// adjacent to one of , : { } [ ]).
	indent bool
	depth  int
}

// jsMarshalEnc is jsMarshal for an output encoding that can carry code points
// only up to maxRune (0 = unrestricted): higher ones are \u-escaped.
func jsMarshalEnc(v any, maxRune rune) (string, error) {
	return jsMarshalOpts(v, jsEmitOpts{maxRune: maxRune})
}

// jsMarshalOpts is jsMarshalEnc with the full option set.
func jsMarshalOpts(v any, o jsEmitOpts) (string, error) {
	var b strings.Builder
	if err := jsEmit(&b, v, o); err != nil {
		return "", err
	}
	return b.String(), nil
}

// jsEmitNewline starts a new pretty-printed line at o's depth. It writes
// nothing at all when indentation is off, so the compact writer is untouched.
func jsEmitNewline(b *strings.Builder, o jsEmitOpts) {
	if !o.indent {
		return
	}
	b.WriteByte('\n')
	for i := 0; i < o.depth; i++ {
		b.WriteString("  ")
	}
}

func jsEmit(b *strings.Builder, v any, o jsEmitOpts) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case *jsObj:
		inner := o
		inner.depth = o.depth + 1
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			jsEmitNewline(b, inner)
			if i < len(t.raw) && t.raw[i] {
				b.WriteByte('"')
				b.WriteString(k)
				b.WriteByte('"')
			} else {
				jsEmitStringOpts(b, k, o)
			}
			b.WriteByte(':')
			if o.indent {
				b.WriteByte(' ')
			}
			if err := jsEmit(b, t.vals[i], inner); err != nil {
				return err
			}
		}
		if len(t.keys) > 0 {
			jsEmitNewline(b, o)
		}
		b.WriteByte('}')
	case []any:
		inner := o
		inner.depth = o.depth + 1
		b.WriteByte('[')
		for i, m := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			jsEmitNewline(b, inner)
			if err := jsEmit(b, m, inner); err != nil {
				return err
			}
		}
		if len(t) > 0 {
			jsEmitNewline(b, o)
		}
		b.WriteByte(']')
	case json.RawMessage:
		b.Write(t)
	case jsPreEscaped:
		b.WriteByte('"')
		b.WriteString(string(t))
		b.WriteByte('"')
	case jsNodeText:
		// A node already serialized by json-node-output-method: escaped like
		// any other JSON string, but NOT character-mapped a second time (the
		// nested serialization applied the character map itself).
		jsEmitStringOpts(b, string(t), jsEmitOpts{maxRune: o.maxRune})
	case string:
		jsEmitStringOpts(b, t, o)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		s, err := jsMarshalNumber(t)
		if err != nil {
			return err
		}
		b.WriteString(s)
	default:
		return fmt.Errorf("err:FOJS0006: cannot serialize %T to JSON", v)
	}
	return nil
}

// jsEmitString writes a JSON string literal, escaping what JSON requires (",
// \, control characters) plus the solidus, which the JSON output method and
// fn:xml-to-json both render as \/ (xml-to-json-017, serialize-json-127/128)
// — never <, > or &.
func jsEmitString(b *strings.Builder, s string) { jsEmitStringEnc(b, s, 0) }

// jsEmitStringEnc is jsEmitString for an encoding limited to maxRune (0 =
// unrestricted): higher code points are written as \uXXXX, non-BMP ones as a
// surrogate pair.
func jsEmitStringEnc(b *strings.Builder, s string, maxRune rune) {
	jsEmitStringOpts(b, s, jsEmitOpts{maxRune: maxRune})
}

// jsEmitStringOpts is jsEmitStringEnc plus use-character-maps: a character the
// map covers is replaced by its mapped string VERBATIM, bypassing every escape
// rule below (Serialization 3.1 §9: the character map is consulted first, and
// only unmapped characters go through JSON escaping).
func jsEmitStringOpts(b *strings.Builder, s string, o jsEmitOpts) {
	maxRune := o.maxRune
	b.WriteByte('"')
	for _, r := range s {
		if len(o.charMap) > 0 {
			if rep, ok := o.charMap[string(r)]; ok {
				b.WriteString(rep)
				continue
			}
		}
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '/':
			b.WriteString(`\/`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case maxRune > 0 && r > maxRune && r > 0xFFFF:
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
			case maxRune > 0 && r > maxRune:
				fmt.Fprintf(b, `\u%04x`, r)
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// serAdaptive implements the adaptive output method (Serialization 3.1 §10):
// every item gets a form from which its type is recoverable — maps as
// map{k:v,…}, arrays as [m,…], strings double-quoted with quotes doubled,
// booleans as true()/false(), QNames as Q{uri}local, doubles in e-notation,
// functions as name#arity, attribute nodes as name="value" — items joined by
// the item-separator (default a newline; serialize-adaptive-003/004).
func serAdaptive(in Object, p serParams) (Object, error) {
	sep := "\n"
	if p.hasItemSep {
		sep = p.itemSeparator
	}
	var b strings.Builder
	for i, it := range Items(in) {
		if i > 0 {
			b.WriteString(sep)
		}
		s, err := serAdaptiveItem(it, p)
		if err != nil {
			return nil, err
		}
		b.WriteString(s)
	}
	return NewString(b.String()), nil
}

// serAdaptiveSeq renders a map value or array member: a single item stands
// alone, anything else is parenthesised.
func serAdaptiveSeq(o Object, p serParams) (string, error) {
	items := Items(o)
	if len(items) == 1 {
		return serAdaptiveItem(items[0], p)
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		s, err := serAdaptiveItem(it, p)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return "(" + strings.Join(parts, ",") + ")", nil
}

// serAdaptivePrefixes maps the well-known function namespaces to their
// conventional prefixes for the name#arity form.
var serAdaptivePrefixes = map[string]string{
	"http://www.w3.org/2005/xpath-functions":       "fn",
	"http://www.w3.org/2005/xpath-functions/math":  "math",
	"http://www.w3.org/2005/xpath-functions/map":   "map",
	"http://www.w3.org/2005/xpath-functions/array": "array",
	"http://www.w3.org/2001/XMLSchema":             "xs",
}

func serAdaptiveDouble(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "INF"
	case math.IsInf(f, -1):
		return "-INF"
	}
	// strconv's 'e' form ("1.5e+06") reshaped to the spec's 1.5e6.
	mant, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	if !strings.Contains(mant, ".") {
		mant += ".0"
	}
	n, _ := strconv.Atoi(exp)
	return mant + "e" + strconv.Itoa(n)
}

func serAdaptiveItem(it Item, p serParams) (string, error) {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		return strings.ReplaceAll(s, `"`, "&quot;")
	}
	// A character map applies to a string item exactly as to text: the
	// mapped character's replacement goes out raw, inside the quotes
	// (character-map-026: "\&apos;E4").
	quote := func(s string) string {
		if len(p.charMaps) > 0 {
			var b strings.Builder
			for _, r := range s {
				if rep, ok := p.charMaps[string(r)]; ok {
					b.WriteString(rep)
				} else {
					b.WriteRune(r)
				}
			}
			s = b.String()
		}
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	switch v := it.(type) {
	case *xmltree.Node:
		switch v.Kind {
		case xmltree.KindAttribute:
			name := v.Name.Local
			if v.Name.Prefix != "" {
				name = v.Name.Prefix + ":" + name
			} else if v.Name.Space != "" {
				name = "Q{" + v.Name.Space + "}" + name
			}
			return name + `="` + esc(v.Value) + `"`, nil
		case xmltree.KindNamespace:
			if v.Name.Local == "" {
				return `xmlns="` + esc(v.Value) + `"`, nil
			}
			return "xmlns:" + v.Name.Local + `="` + esc(v.Value) + `"`, nil
		}
		// A node is serialized exactly as the xml output method would, XML
		// declaration included unless omit-xml-declaration says otherwise
		// (output-0721 wants it; result-document-0304 sets the parameter
		// precisely to suppress it). fn:serialize's own parameter reader
		// defaults omit-xml-declaration to yes, so the function form is
		// unaffected. The character map is handed to the xml serializer, which
		// applies it INSTEAD of escaping (a pre-mapped tree would have its
		// replacement text escaped again: character-map-026's &apos;).
		var cmap map[rune]string
		if len(p.charMaps) > 0 {
			cmap = make(map[rune]string, len(p.charMaps))
			for k, rep := range p.charMaps {
				for _, r := range k {
					cmap[r] = rep
				}
			}
		}
		return xmltree.Serialize(v, xmltree.SerializeOptions{
			Method: "xml", Indent: p.indent, OmitXMLDeclaration: p.omitXMLDecl,
			Encoding: p.encoding, XMLVersion: p.version, CharacterMap: cmap,
		}), nil
	case *Map:
		var b strings.Builder
		b.WriteString("map{")
		for i, k := range v.Keys() {
			if i > 0 {
				b.WriteByte(',')
			}
			ks, err := serAdaptiveItem(k, p)
			if err != nil {
				return "", err
			}
			vs, err := serAdaptiveSeq(v.Get(k), p)
			if err != nil {
				return "", err
			}
			b.WriteString(ks + ":" + vs)
		}
		b.WriteByte('}')
		return b.String(), nil
	case *Array:
		parts := make([]string, 0, v.Size())
		for _, m := range v.Members() {
			s, err := serAdaptiveSeq(m, p)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case *Function:
		name := "(anonymous-function)"
		if v.Name != "" {
			if pfx, ok := serAdaptivePrefixes[v.NS]; ok {
				name = pfx + ":" + v.Name
			} else if v.NS != "" {
				name = "Q{" + v.NS + "}" + v.Name
			} else {
				name = v.Name
			}
		}
		return name + "#" + strconv.Itoa(v.Arity), nil
	case *Atomic:
		switch {
		case v.T == XSboolean:
			if v.Bool() {
				return "true()", nil
			}
			return "false()", nil
		case v.T == XSqname || v.T == XSnotation:
			return "Q{" + v.qn.Space + "}" + v.qn.Local, nil
		case v.T == XSdouble || v.T == XSfloat:
			return serAdaptiveDouble(v.Float()), nil
		case v.T == XSdecimal:
			lex := v.Lexical()
			if !strings.Contains(lex, ".") {
				lex += ".0"
			}
			return lex, nil
		case v.IsNumeric():
			return v.Lexical(), nil
		case v.T == XSuntypedAtomic || v.T == XSanyURI || isStringType(v.T):
			return quote(v.Lexical()), nil
		}
		return v.Lexical(), nil
	case string:
		return quote(v), nil
	case bool:
		if v {
			return "true()", nil
		}
		return "false()", nil
	case float64:
		return serAdaptiveDouble(v), nil
	}
	return itemString(it), nil
}

// jsMarshalNumber renders a JSON number from a float64 using the XPath
// xs:double canonical string form (1E6 -> "1.0E6", -1E-6 -> "-0.000001").
func jsMarshalNumber(f float64) (string, error) {
	// JSON has no representation for NaN or infinity — serializing one is an
	// error (serialize-json-122).
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("err:FOJS0006: cannot serialize %v to JSON", f)
	}
	return formatDouble(f, 64), nil
}

// jsDecodeOrdered decodes a JSON value into the order-preserving Go graph
// (jsObj / []any / json.RawMessage / string / bool / nil), used by json-to-xml so
// object members keep document order (Go's map decoding would lose it).
//
// It reads through a jsTokenizer rather than the raw json.Decoder so every
// string goes through jsDecodeString: that is what applies the escape /
// fallback options and what replaces a character JSON allows but XML does not
// with U+FFFD (json-to-xml-error-015's \u0000).
func jsDecodeOrdered(t *jsTokenizer) (any, error) {
	dec := t.dec
	tok, err := t.token()
	if err != nil {
		return nil, fmt.Errorf("err:FOJS0001: invalid JSON: %v", err)
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			o := &jsObj{}
			for dec.More() {
				kt, err := t.token()
				if err != nil {
					return nil, fmt.Errorf("err:FOJS0001: invalid JSON: %v", err)
				}
				key, _ := kt.(string)
				val, err := jsDecodeOrdered(t)
				if err != nil {
					return nil, err
				}
				// With escape=false (the default) a codepoint that is not a
				// permitted XML character becomes U+FFFD, exactly as
				// fn:parse-json's own decoder does — otherwise it reaches the
				// result tree and makes it unserializable (error-3250a).
				o.put(jsReplaceInvalidXML(key), val)
			}
			dec.Token() // consume '}'
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				m, err := jsDecodeOrdered(t)
				if err != nil {
					return nil, err
				}
				arr = append(arr, m)
			}
			dec.Token() // consume ']'
			return arr, nil
		}
	case json.Number:
		return json.RawMessage(string(v)), nil
	case string:
		return v, nil
	case bool:
		return v, nil
	case nil:
		return nil, nil
	}
	return nil, nil
}

// jsDedupFirst drops every object member whose key repeats an earlier one, at
// every level (duplicates="use-first" — json-to-xml-duplicates-001). The
// default for fn:json-to-xml is "retain", the only setting under which the XML
// representation can actually show the duplicates, so this runs only when
// use-first is asked for.
func jsDedupFirst(v any) any {
	switch t := v.(type) {
	case *jsObj:
		out := &jsObj{}
		seen := map[string]bool{}
		for i, k := range t.keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			out.put(k, jsDedupFirst(t.vals[i]))
		}
		return out
	case []any:
		for i, m := range t {
			t[i] = jsDedupFirst(m)
		}
		return t
	}
	return v
}

// jsIsEmpty reports whether the argument is an absent/empty sequence.
func jsIsEmpty(o Object) bool {
	if o == nil {
		return true
	}
	return len(Items(o)) == 0
}

// jsConvert converts a value decoded from JSON (via encoding/json with
// UseNumber) into the corresponding XDM Object representation.
//
//	JSON object  -> *Map (keys are xs:string atomics)
//	JSON array   -> *Array (one member per element)
//	number       -> xs:double
//	string       -> xs:string
//	true/false   -> xs:boolean
//	null         -> empty sequence
func jsConvert(v any) Object {
	switch t := v.(type) {
	case nil:
		return Sequence{}
	case bool:
		return NewBool(t)
	case string:
		return NewString(t)
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			return NewDouble(0)
		}
		return NewDouble(f)
	case float64:
		return NewDouble(t)
	case map[string]any:
		m := NewMap()
		for k, mv := range t {
			m.Put(NewString(k), jsConvert(mv))
		}
		return m
	case []any:
		members := make([]Object, len(t))
		for i, mv := range t {
			members[i] = jsConvert(mv)
		}
		return NewArray(members)
	default:
		return Sequence{}
	}
}

// jsParse decodes a JSON string into an XDM Object. It returns an err:FOJS0001
// error when the input is not well-formed JSON.
func jsParse(s string) (Object, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("err:FOJS0001: invalid JSON: %v", err)
	}
	// Reject trailing content after the first JSON value.
	if err := jsCheckTrailing(s, dec); err != nil {
		return nil, err
	}
	return jsConvert(v), nil
}

// jsCheckTrailing reports FOJS0001 when anything but JSON whitespace follows
// the value the decoder has just consumed. (Decoder.More is not the right
// test: it answers false for a stray '}' — fn-parse-json-914 — and a token
// stream happily continues past "-0" into "0" — json-to-xml-error-029.)
func jsCheckTrailing(s string, dec *json.Decoder) error {
	off := dec.InputOffset()
	if off < 0 || off > int64(len(s)) {
		return nil
	}
	if strings.Trim(s[off:], " \t\r\n") != "" {
		return fmt.Errorf("err:FOJS0001: invalid JSON: trailing content")
	}
	return nil
}

// jsOptions holds the resolved fn:parse-json $options.
type jsOptions struct {
	duplicates string    // reject | use-first | use-last | use-any
	escape     bool      // represent special characters as JSON escapes in the result
	fallback   *Function // escape=false: called with the escape sequence of a non-XML character
}

// jsReadOptions resolves the $options map of fn:parse-json, validating values.
func jsReadOptions(o Object) (jsOptions, error) {
	opts := jsOptions{duplicates: "use-first"}
	m, ok := firstItem(o).(*Map)
	if !ok {
		return opts, nil
	}
	if dv := m.Get(NewString("duplicates")); !jsIsEmpty(dv) {
		d := itemString(firstItem(dv))
		switch d {
		case "reject", "use-first", "use-last", "use-any":
			opts.duplicates = d
		default:
			return opts, fmt.Errorf("err:FOJS0005: invalid value %q for option 'duplicates'", d)
		}
	}
	// Option-map VALUE TYPING. F&O 3.1 §17.1 splits the two failure modes: a
	// value of the WRONG TYPE is XPTY0004 (the option map is checked against
	// the function's declared record type), while a value of the right type
	// that is not one of the allowed values — "duplicates" above — is
	// FOJS0005. liberal/validate/escape each take exactly one xs:boolean, so
	// (), a two-item sequence and a string are all XPTY0004
	// (json-doc-error-012..015, json-to-xml-error-020/022/023/025/026/027).
	for _, key := range []string{"liberal", "validate", "escape"} {
		b, present, err := jsBoolOption(m, key)
		if err != nil {
			return opts, err
		}
		if present && key == "escape" {
			opts.escape = b
		}
	}
	if fv := m.Get(NewString("fallback")); !jsIsEmpty(fv) {
		fn, isFn := firstItem(fv).(*Function)
		if !isFn {
			return opts, fmt.Errorf("err:XPTY0004: option \"fallback\" must be a function")
		}
		if fn.Arity != 1 {
			return opts, fmt.Errorf("err:XPTY0004: the fallback function must have arity 1")
		}
		// fallback is only meaningful with escape=false; combining it with
		// escape=true is an error (json-doc-027).
		if ev := m.Get(NewString("escape")); !jsIsEmpty(ev) && ToBool(ev) {
			return opts, fmt.Errorf("err:FOJS0005: options \"escape\" and \"fallback\" cannot be combined")
		}
		opts.fallback = fn
	}
	return opts, nil
}

// jsBoolOption reads one xs:boolean-valued entry from a JSON function's option
// map. It reports whether the key was present at all (a key bound to the empty
// sequence IS present — and is a type error, since the empty sequence does not
// match the required single xs:boolean).
func jsBoolOption(m *Map, key string) (val, present bool, err error) {
	k := NewString(key)
	if !m.Contains(k) {
		return false, false, nil
	}
	items := Items(m.Get(k))
	if len(items) != 1 {
		return false, true, fmt.Errorf("err:XPTY0004: option %q must be a single xs:boolean", key)
	}
	switch b := items[0].(type) {
	case bool:
		return b, true, nil
	case *Atomic:
		if b.T == XSboolean {
			return b.Bool(), true, nil
		}
	}
	return false, true, fmt.Errorf("err:XPTY0004: option %q must be an xs:boolean", key)
}

// jsTokenizer wraps a json.Decoder so that string tokens can be re-derived
// from their RAW source text: encoding/json has already validated the string
// (so the raw form is well-formed) but has also unescaped it and replaced an
// unpaired surrogate escape with U+FFFD, which loses exactly what the
// escape/fallback options need to see (json-doc-019/021/024/025/026/039).
type jsTokenizer struct {
	dec  *json.Decoder
	src  string
	prev int64 // input offset after the previous token
	opts jsOptions
}

func (t *jsTokenizer) token() (json.Token, error) {
	tok, err := t.dec.Token()
	if err != nil {
		return nil, err
	}
	end := t.dec.InputOffset()
	if s, ok := tok.(string); ok {
		// Between two tokens only whitespace, ',' and ':' occur, so the first
		// '"' at or after prev opens this string; end-1 is its closing quote.
		start := strings.IndexByte(t.src[t.prev:end], '"')
		if start >= 0 && int64(start)+t.prev+1 <= end-1 {
			raw := t.src[t.prev+int64(start)+1 : end-1]
			s, err = jsDecodeString(raw, s, t.opts)
			if err != nil {
				return nil, err
			}
		}
		tok = s
	}
	t.prev = end
	return tok, nil
}

// jsDecodeString produces the XDM string for a JSON string whose raw (still
// escaped) source text is raw and whose plain decoding is decoded:
//   - default:        decoded, non-XML characters replaced by U+FFFD;
//   - escape=true:    special characters (controls, non-XML characters incl.
//     unpaired surrogates, backslash) re-escaped in JSON form, all else literal;
//   - fallback given: each non-XML character replaced by fallback(escape-seq).
func jsDecodeString(raw, decoded string, opts jsOptions) (string, error) {
	if !opts.escape && opts.fallback == nil {
		return jsReplaceInvalidXML(decoded), nil
	}
	var b strings.Builder
	emit := func(r rune, seq string) error {
		switch {
		case opts.escape:
			switch {
			case r == '\\':
				b.WriteString(`\\`)
			case r == '\b':
				b.WriteString(`\b`)
			case r == '\f':
				b.WriteString(`\f`)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\r':
				b.WriteString(`\r`)
			case r == '\t':
				b.WriteString(`\t`)
			case r < 0x20 || (r >= 0x7F && r <= 0x9F) || !isValidXMLChar(r):
				if r > 0xFFFF {
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(&b, `\u%04X\u%04X`, r1, r2)
				} else {
					fmt.Fprintf(&b, `\u%04X`, r)
				}
			default:
				b.WriteRune(r)
			}
		case isValidXMLChar(r):
			b.WriteRune(r)
		default:
			if seq == "" {
				seq = fmt.Sprintf(`\u%04X`, r)
			}
			res, err := opts.fallback.Call([]Object{NewString(seq)})
			if err != nil {
				return err
			}
			b.WriteString(ToString(res))
		}
		return nil
	}
	hex4 := func(s string) (rune, bool) {
		if len(s) < 4 {
			return 0, false
		}
		v, err := strconv.ParseUint(s[:4], 16, 32)
		return rune(v), err == nil
	}
	for i := 0; i < len(raw); {
		if raw[i] != '\\' || i+1 >= len(raw) {
			r, size := utf8.DecodeRuneInString(raw[i:])
			if err := emit(r, ""); err != nil {
				return "", err
			}
			i += size
			continue
		}
		var r rune
		seq := raw[i : i+2]
		switch raw[i+1] {
		case 'n':
			r = '\n'
		case 'r':
			r = '\r'
		case 't':
			r = '\t'
		case 'b':
			r = '\b'
		case 'f':
			r = '\f'
		case '"':
			r = '"'
		case '\\':
			r = '\\'
		case '/':
			r = '/'
		case 'u':
			cp, ok := hex4(raw[i+2:])
			if !ok {
				return "", fmt.Errorf("err:FOJS0001: invalid JSON: bad \\u escape")
			}
			seq = raw[i : i+6]
			r = cp
			if utf16.IsSurrogate(cp) && cp < 0xDC00 && i+12 <= len(raw) && raw[i+6] == '\\' && raw[i+7] == 'u' {
				if lo, ok := hex4(raw[i+8:]); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					r = utf16.DecodeRune(cp, lo)
					seq = raw[i : i+12]
				}
			}
			if utf16.IsSurrogate(r) {
				// unpaired surrogate: not an XML character
				if err := emit(r, seq); err != nil {
					return "", err
				}
				i += len(seq)
				continue
			}
		default:
			return "", fmt.Errorf("err:FOJS0001: invalid JSON: invalid escape \\%c", raw[i+1])
		}
		if err := emit(r, seq); err != nil {
			return "", err
		}
		i += len(seq)
	}
	return b.String(), nil
}

// jsParseErr wraps a tokenizer error as FOJS0001 unless it already carries an
// XPath error code (a fallback function's own dynamic error must propagate).
func jsParseErr(err error) error {
	if strings.HasPrefix(err.Error(), "err:") {
		return err
	}
	return fmt.Errorf("err:FOJS0001: invalid JSON: %v", err)
}

// jsReplaceInvalidXML replaces each codepoint that is not a valid XML
// character with U+FFFD (the parse-json default when unescaping).
func jsReplaceInvalidXML(s string) string {
	needs := false
	for _, r := range s {
		if !isValidXMLChar(r) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isValidXMLChar(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('�')
		}
	}
	return b.String()
}

// isValidXMLChar reports whether r is a legal XML 1.0 character.
func isValidXMLChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// jsParseWithOptions parses JSON honouring the parse-json $options: the
// duplicate-key policy (a token stream lets duplicate object keys be detected
// and resolved rather than silently last-wins) and the escape/fallback string
// handling (jsTokenizer / jsDecodeString).
func jsParseWithOptions(s string, opts jsOptions) (Object, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	tz := &jsTokenizer{dec: dec, src: s, opts: opts}
	v, err := jsParseToken(tz, opts)
	if err != nil {
		return nil, err
	}
	if err := jsCheckTrailing(s, dec); err != nil {
		return nil, err
	}
	return v, nil
}

func jsParseToken(dec *jsTokenizer, opts jsOptions) (Object, error) {
	tok, err := dec.token()
	if err != nil {
		return nil, jsParseErr(err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := NewMap()
			seen := map[string]bool{}
			for dec.dec.More() {
				kt, err := dec.token()
				if err != nil {
					return nil, jsParseErr(err)
				}
				key, _ := kt.(string)
				val, err := jsParseToken(dec, opts)
				if err != nil {
					return nil, err
				}
				if seen[key] {
					switch opts.duplicates {
					case "reject":
						return nil, fmt.Errorf("err:FOJS0003: duplicate key %q in JSON object", key)
					case "use-first", "use-any":
						continue // keep the first occurrence
					default: // use-last
						m.Put(NewString(key), val)
					}
				} else {
					seen[key] = true
					m.Put(NewString(key), val)
				}
			}
			if _, err := dec.token(); err != nil { // consume '}'
				return nil, jsParseErr(err)
			}
			return m, nil
		case '[':
			members := []Object{}
			for dec.dec.More() {
				v, err := jsParseToken(dec, opts)
				if err != nil {
					return nil, err
				}
				members = append(members, v)
			}
			if _, err := dec.token(); err != nil { // consume ']'
				return nil, jsParseErr(err)
			}
			return NewArray(members), nil
		}
	case string:
		// Already decoded per the options by the tokenizer (jsDecodeString):
		// by default a non-XML character (U+0000, U+FFFF, backspace, unpaired
		// surrogate) became U+FFFD (fn-parse-json-053/055/056/058).
		return NewString(t), nil
	case json.Number:
		f, perr := strconv.ParseFloat(string(t), 64)
		if perr != nil {
			return NewDouble(0), nil
		}
		return NewDouble(f), nil
	case bool:
		return NewBool(t), nil
	case nil:
		return Sequence{}, nil
	}
	return Sequence{}, nil
}

// fnJSParseJSON implements fn:parse-json($json as xs:string?, $options?).
// An empty/absent input yields an empty sequence.
func fnJSParseJSON(ctx *Context, args []Object) (Object, error) {
	in := arg(args, 0)
	if jsIsEmpty(in) {
		return Sequence{}, nil
	}
	opts, err := jsReadOptions(arg(args, 1))
	if err != nil {
		return nil, err
	}
	return jsParseWithOptions(ToString(in), opts)
}

// fnJSJSONDoc implements fn:json-doc($href as xs:string?, $options?): the
// text at $href (via the host Resolver — FOTS supplies its resources that
// way) parsed as JSON.
func fnJSJSONDoc(ctx *Context, args []Object) (Object, error) {
	in := arg(args, 0)
	if jsIsEmpty(in) {
		return Sequence{}, nil
	}
	uri := ToString(in)
	if ctx == nil || ctx.Resolver == nil {
		return nil, fmt.Errorf("err:FOUT1170: cannot retrieve %q (no resolver)", uri)
	}
	s, ok := ctx.Resolver.ResolveText(uri)
	if !ok {
		return nil, fmt.Errorf("err:FOUT1170: cannot retrieve %q", uri)
	}
	opts, err := jsReadOptions(arg(args, 1))
	if err != nil {
		return nil, err
	}
	return jsParseWithOptions(s, opts)
}

// serParams holds the resolved fn:serialize output parameters we honour.
type serParams struct {
	method        string // xml | html | xhtml | text | json | adaptive
	indent        bool
	omitXMLDecl   bool
	itemSeparator string
	hasItemSep    bool
	charMaps      map[string]string // use-character-maps: char -> replacement
	standalone    string            // "yes" | "no"; "" omits the pseudo-attribute
	cdataElems    []xmltree.Name    // cdata-section-elements
	suppressInd   []xmltree.Name    // suppress-indentation
	htmlVersion   string            // html-version (or version under the html method)
	version       string
	noContentType bool   // include-content-type="no"
	allowDup      bool   // allow-duplicate-names (json)
	nodeMethod    string // json-node-output-method
	encoding      string
	undeclarePfx  bool // undeclare-prefixes
}

// serBool interprets a serialization-parameter boolean ("yes"/"no"/"true"/"false").
func serBool(s string) bool { return s == "yes" || s == "true" || s == "1" }

// serApply sets one string-valued parameter; it is the common tail of the map
// and element forms (the QName-list parameters are collected by the callers).
func serApply(p *serParams, name, val string) error {
	switch name {
	case "method":
		p.method = val
	case "indent":
		p.indent = serBool(val)
	case "omit-xml-declaration":
		p.omitXMLDecl = serBool(val)
	case "item-separator":
		p.itemSeparator, p.hasItemSep = val, true
	case "standalone":
		switch val {
		case "yes", "true", "1":
			p.standalone = "yes"
		case "no", "false", "0":
			p.standalone = "no"
		case "omit":
			p.standalone = ""
		default:
			return fmt.Errorf("err:SEPM0016: standalone must be yes, no or omit, got %q", val)
		}
	case "html-version":
		p.htmlVersion = val
	case "version":
		p.version = val
	case "include-content-type":
		p.noContentType = !serBool(val)
	case "allow-duplicate-names":
		p.allowDup = serBool(val)
	case "json-node-output-method":
		p.nodeMethod = val
	case "encoding":
		p.encoding = val
	case "undeclare-prefixes":
		p.undeclarePfx = serBool(val)
	}
	return nil
}

// serMapKey classifies a parameter-map key: an xs:string names a standard
// parameter; an xs:QName key is an implementation-defined parameter and is
// ignored — including one in no namespace, which is not the standard
// parameter of that name (serialize-xml-120b: QName("","indent") does not
// switch indentation on).
func serMapKey(k Item) (string, bool) {
	if a, ok := k.(*Atomic); ok && a.T == XSqname {
		return "", false
	}
	return itemString(k), true
}

// serSetMapValue applies one map-form parameter, enforcing the value types of
// the serialization spec §3.1: booleans must be xs:boolean (serialize-json-
// 133..135 — 23, "true" and a two-item sequence are all XPTY0004), the QName-
// list parameters take xs:QName*, everything else a single item.
func serSetMapValue(p *serParams, name string, v Object) error {
	items := Items(v)
	if len(items) == 0 {
		return nil // an empty sequence is as if the entry were absent
	}
	switch name {
	case "cdata-section-elements", "suppress-indentation":
		// A JSON-style map value can't hold "several values under one key"
		// except via an array, so fn:serialize accepts an array of xs:QName
		// as an alternate encoding of this xs:QName* parameter alongside an
		// ordinary sequence (serialize-xml-106a: [QName(...), QName(...), …]).
		if len(items) == 1 {
			if arr, ok := items[0].(*Array); ok {
				var flat []Item
				for _, m := range arr.Members() {
					flat = append(flat, Items(m)...)
				}
				items = flat
			}
		}
		names := make([]xmltree.Name, 0, len(items))
		for _, it := range items {
			a, ok := it.(*Atomic)
			if !ok || a.T != XSqname {
				return fmt.Errorf("err:XPTY0004: serialize: %s must be a sequence of xs:QName", name)
			}
			names = append(names, xmltree.Name{Space: a.qn.Space, Local: a.qn.Local})
		}
		if name == "cdata-section-elements" {
			p.cdataElems = append(p.cdataElems, names...)
		} else {
			p.suppressInd = append(p.suppressInd, names...)
		}
		return nil
	}
	if len(items) > 1 {
		return fmt.Errorf("err:XPTY0004: serialize: %s must be a single item, got %d", name, len(items))
	}
	it := items[0]
	boolLex := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	if serBoolParams[name] || name == "standalone" {
		switch b := it.(type) {
		case bool:
			return serApply(p, name, boolLex(b))
		case *Atomic:
			if b.T == XSboolean {
				return serApply(p, name, boolLex(b.Bool()))
			}
			if b.T == XSuntypedAtomic { // function conversion: cast to xs:boolean
				bv, err := CastTo(b, XSboolean)
				if err != nil {
					return err
				}
				return serApply(p, name, boolLex(bv.Bool()))
			}
			if name == "standalone" && b.T == XSstring && b.Lexical() == "omit" {
				return serApply(p, name, "omit")
			}
		}
		return fmt.Errorf("err:XPTY0004: serialize: %s must be an xs:boolean", name)
	}
	if name == "html-version" {
		if a, ok := it.(*Atomic); !ok || !a.IsNumeric() {
			return fmt.Errorf("err:XPTY0004: serialize: html-version must be numeric")
		}
	}
	return serApply(p, name, itemString(it))
}

// serReadParams resolves the $params argument, which may be either a map
// (map{'method':'json',...}) or a sequence of serialization-parameter elements
// (<output:method value="json"/>), per the XSLT/XQuery serialization spec.
func serReadParams(o Object) (serParams, error) {
	// Default to omitting the XML declaration: serialize is overwhelmingly used
	// on element/fragment values where a declaration is unwanted, and an explicit
	// omit-xml-declaration='no' re-enables it.
	p := serParams{method: "xml", omitXMLDecl: true, nodeMethod: "xml"}
	if jsIsEmpty(o) {
		return p, nil
	}
	if m, ok := firstItem(o).(*Map); ok {
		for _, k := range m.Keys() {
			name, std := serMapKey(k)
			if !std {
				continue
			}
			if name == "use-character-maps" {
				cm, ok := firstItem(m.Get(k)).(*Map)
				if !ok {
					return p, fmt.Errorf("err:XPTY0004: use-character-maps must be a map")
				}
				p.charMaps = map[string]string{}
				for _, ck := range cm.Keys() {
					// Keys must be single-character strings; values strings.
					ks, ok := serStringItem(ck)
					if !ok {
						return p, fmt.Errorf("err:XPTY0004: use-character-maps key must be a string")
					}
					if utf8.RuneCountInString(ks) != 1 {
						return p, fmt.Errorf("err:SEPM0018: use-character-maps key must be one character")
					}
					vitems := Items(cm.Get(ck))
					if len(vitems) != 1 {
						return p, fmt.Errorf("err:XPTY0004: use-character-maps value must be a single string")
					}
					vs, ok := serStringItem(vitems[0])
					if !ok {
						return p, fmt.Errorf("err:XPTY0004: use-character-maps value must be a string")
					}
					p.charMaps[ks] = vs
				}
				continue
			}
			if err := serSetMapValue(&p, name, m.Get(k)); err != nil {
				return p, err
			}
		}
		return p, nil
	}
	// Element form: the argument is the output:serialization-parameters element.
	items := Items(o)
	if len(items) == 1 {
		if n, ok := items[0].(*xmltree.Node); ok && n.Kind == xmltree.KindElement {
			if n.Name.Space == serNS && n.Name.Local == "serialization-parameters" {
				return p, serReadParamElement(&p, n)
			}
			return p, fmt.Errorf("err:XPTY0004: serialize: parameter element must be {%s}serialization-parameters", serNS)
		}
	}
	// Legacy bare-sequence fallback.
	for _, it := range items {
		n, ok := it.(*xmltree.Node)
		if !ok || n.Kind != xmltree.KindElement {
			continue
		}
		val, _ := n.AttrLocal("value")
		if err := serSetLexical(&p, n, val); err != nil {
			return p, err
		}
	}
	return p, nil
}

// serSetLexical applies one element-form parameter from its @value. Values are
// whitespace-trimmed (params-029: standalone=" no ") except the item-separator,
// whose whitespace is significant; the QName-list parameters resolve prefixes
// against the parameter element's in-scope namespaces.
func serSetLexical(p *serParams, c *xmltree.Node, val string) error {
	name := c.Name.Local
	if name != "item-separator" {
		val = strings.TrimSpace(val)
	}
	switch name {
	case "cdata-section-elements", "suppress-indentation":
		for _, tok := range strings.Fields(val) {
			pre, local, _ := strings.Cut(tok, ":")
			if local == "" {
				pre, local = "", pre
			}
			uri := ""
			if pre != "" {
				u, ok := c.LookupPrefix(pre)
				if !ok {
					return fmt.Errorf("err:SEPM0017: unbound prefix %q in %s", pre, name)
				}
				uri = u
			}
			if name == "cdata-section-elements" {
				p.cdataElems = append(p.cdataElems, xmltree.Name{Space: uri, Local: local})
			} else {
				p.suppressInd = append(p.suppressInd, xmltree.Name{Space: uri, Local: local})
			}
		}
		return nil
	}
	return serApply(p, name, val)
}

// serStringItem returns the lexical value of an item that is an xs:string or
// xs:untypedAtomic (or a bare Go string), and false for any other type — used
// to enforce the use-character-maps key/value string types (139b/140b/141b).
func serStringItem(it Item) (string, bool) {
	switch v := it.(type) {
	case string:
		return v, true
	case *Atomic:
		if v.T == XSstring || v.T == XSuntypedAtomic {
			return v.Lexical(), true
		}
	}
	return "", false
}

// serNS is the serialization-parameters namespace.
const serNS = "http://www.w3.org/2010/xslt-xquery-serialization"

// serStdParams is the set of standard serialization-parameter local names.
var serStdParams = map[string]bool{
	"allow-duplicate-names": true, "byte-order-mark": true,
	"cdata-section-elements": true, "doctype-public": true,
	"doctype-system": true, "encoding": true, "escape-uri-attributes": true,
	"html-version": true, "include-content-type": true, "indent": true,
	"item-separator": true, "json-node-output-method": true, "media-type": true,
	"method": true, "normalization-form": true, "omit-xml-declaration": true,
	"standalone": true, "suppress-indentation": true, "undeclare-prefixes": true,
	"use-character-maps": true, "version": true,
}

// serBoolParams are the parameters whose value must be a serialization boolean.
var serBoolParams = map[string]bool{
	"indent": true, "omit-xml-declaration": true, "byte-order-mark": true,
	"undeclare-prefixes": true, "escape-uri-attributes": true,
	"include-content-type": true, "allow-duplicate-names": true,
}

func serValidBool(s string) bool {
	switch s {
	case "yes", "no", "true", "false", "0", "1":
		return true
	}
	return false
}

// serReadParamElement validates and reads the element form of the
// serialization parameters (the output:serialization-parameters wrapper),
// per the XSLT/XQuery serialization spec §3.1. Any violation is reported as
// an error (the SEPM family); fn:serialize surfaces it to the caller.
func serReadParamElement(p *serParams, root *xmltree.Node) error {
	if len(root.Attrs) > 0 {
		return fmt.Errorf("err:SEPM0017: serialization-parameters element has unexpected attribute %q", root.Attrs[0].Name.Local)
	}
	seen := map[string]bool{}
	for _, c := range root.Children {
		if c.Kind != xmltree.KindElement {
			continue
		}
		key := c.Name.Space + " " + c.Name.Local
		if seen[key] {
			return fmt.Errorf("err:SEPM0019: serialization parameter %q specified more than once", c.Name.Local)
		}
		seen[key] = true
		if c.Name.Space == "" {
			return fmt.Errorf("err:SEPM0017: serialization parameter %q is in no namespace", c.Name.Local)
		}
		if c.Name.Space != serNS {
			continue // extension parameter in a foreign namespace: ignored
		}
		if !serStdParams[c.Name.Local] {
			return fmt.Errorf("err:SEPM0017: unknown serialization parameter %q", c.Name.Local)
		}
		if c.Name.Local == "use-character-maps" {
			if err := serReadCharMapElement(p, c); err != nil {
				return err
			}
			continue
		}
		val := ""
		for _, a := range c.Attrs {
			if a.Name.Space == "" && a.Name.Local == "value" {
				val = a.Value
				continue
			}
			return fmt.Errorf("err:SEPM0017: serialization parameter %q has unexpected attribute %q", c.Name.Local, a.Name.Local)
		}
		if serBoolParams[c.Name.Local] && !serValidBool(strings.TrimSpace(val)) {
			return fmt.Errorf("err:SEPM0016: parameter %q has invalid boolean value %q", c.Name.Local, val)
		}
		if err := serSetLexical(p, c, val); err != nil {
			return err
		}
	}
	return nil
}

// serReadCharMapElement validates a use-character-maps element (its children
// must be character-map elements with exactly a single-character @character and
// a @map-string, no duplicates and no attribute on the wrapper).
func serReadCharMapElement(p *serParams, uc *xmltree.Node) error {
	if len(uc.Attrs) > 0 {
		return fmt.Errorf("err:SEPM0017: use-character-maps must not carry attributes")
	}
	p.charMaps = map[string]string{}
	for _, cm := range uc.Children {
		if cm.Kind != xmltree.KindElement {
			continue
		}
		if cm.Name.Space != serNS || cm.Name.Local != "character-map" {
			return fmt.Errorf("err:SEPM0017: use-character-maps child must be character-map, got %q", cm.Name.Local)
		}
		ch, hasCh := "", false
		str, hasStr := "", false
		for _, a := range cm.Attrs {
			switch {
			case a.Name.Space == "" && a.Name.Local == "character":
				ch, hasCh = a.Value, true
			case a.Name.Space == "" && a.Name.Local == "map-string":
				str, hasStr = a.Value, true
			default:
				return fmt.Errorf("err:SEPM0017: character-map has unexpected attribute %q", a.Name.Local)
			}
		}
		if !hasCh || !hasStr {
			return fmt.Errorf("err:SEPM0017: character-map needs character and map-string")
		}
		if utf8.RuneCountInString(ch) != 1 {
			return fmt.Errorf("err:SEPM0018: character-map @character must be one character, got %q", ch)
		}
		if _, dup := p.charMaps[ch]; dup {
			return fmt.Errorf("err:SEPM0018: duplicate character map for %q", ch)
		}
		p.charMaps[ch] = str
	}
	return nil
}

// fnJSSerialize implements fn:serialize($seq, $params?). Nodes serialize as XML
// (or HTML/text) text; the json method renders maps/arrays/atomics as JSON; an
// item-separator joins the sequence; atomic values use their string value.
func fnJSSerialize(ctx *Context, args []Object) (Object, error) {
	in := arg(args, 0)
	p, err := serReadParams(arg(args, 1))
	if err != nil {
		return nil, err
	}
	if jsIsEmpty(in) {
		if p.method == "json" {
			return NewString("null"), nil
		}
		return NewString(""), nil
	}
	if p.method == "json" {
		v, err := jsToJSONValue(in, &p)
		if err != nil {
			return nil, err
		}
		b, err := jsMarshalOpts(v, jsEmitOpts{maxRune: serMaxRune(p.encoding), charMap: p.charMaps})
		if err != nil {
			return nil, fmt.Errorf("err:SERE0020: %v", err)
		}
		return NewString(b), nil
	}
	if p.method == "adaptive" {
		return serAdaptive(in, p)
	}
	sep := " "
	if p.hasItemSep {
		sep = p.itemSeparator
	}
	opts := xmltree.SerializeOptions{
		Method:              p.method,
		Indent:              p.indent,
		OmitXMLDeclaration:  p.omitXMLDecl,
		Encoding:            p.encoding,
		Standalone:          p.standalone,
		CDATAElements:       p.cdataElems,
		SuppressIndentation: p.suppressInd,
		XMLVersion:          p.version,
		UndeclarePrefixes:   p.undeclarePfx,
	}
	if p.method == "html" {
		opts.HTMLVersion = p.htmlVersion
		if opts.HTMLVersion == "" {
			opts.HTMLVersion = p.version
		}
		opts.IncludeContentType = !p.noContentType
	}
	items := Items(in)
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString(sep)
		}
		switch n := it.(type) {
		case *xmltree.Node:
			// SER 3.1's own sequence-normalization step 4 rejects an
			// attribute or namespace node in the sequence being serialized
			// outright — there is no XML/text/html serialized form for one
			// on its own (serialize-xml-002/011/012: a lone (//@*)[1] or
			// (//namespace::*)[1]).
			if n.Kind == xmltree.KindAttribute || n.Kind == xmltree.KindNamespace {
				return nil, fmt.Errorf("err:SENR0001: cannot serialize a lone attribute or namespace node")
			}
			if len(p.charMaps) > 0 {
				n = serApplyCharMaps(n, p.charMaps)
			}
			b.WriteString(xmltree.Serialize(n, opts))
		case *Map, *Array, *Function:
			return nil, fmt.Errorf("err:SENR0001: cannot serialize a %T with method %q", it, p.method)
		default:
			b.WriteString(itemString(it))
		}
	}
	return NewString(b.String()), nil
}

// serMaxRune returns the largest code point the output encoding can carry
// directly; anything above it is written as a \uXXXX escape by the JSON
// method (serialize-json-114: ISO-8859-1 renders U+1D11E as 𝄞).
// Zero means an unrestricted (Unicode) encoding.
func serMaxRune(encoding string) rune {
	switch strings.ToUpper(strings.TrimSpace(encoding)) {
	case "US-ASCII", "ASCII":
		return 0x7F
	case "ISO-8859-1", "ISO8859-1", "LATIN1", "LATIN-1":
		return 0xFF
	}
	return 0
}

// serNodeString renders a node for the JSON output method: nodes become the
// string of their serialization under json-node-output-method (default xml,
// serialize-json-008b/009b/127); attribute and namespace nodes have no such
// form (SENR0001).
func serNodeString(n *xmltree.Node, p *serParams) (jsNodeText, error) {
	switch n.Kind {
	case xmltree.KindAttribute, xmltree.KindNamespace:
		return "", fmt.Errorf("err:SENR0001: cannot serialize an attribute or namespace node as JSON")
	}
	method := "xml"
	if p != nil && p.nodeMethod != "" {
		method = p.nodeMethod
	}
	if method == "text" {
		return jsNodeText(n.StringValue()), nil
	}
	if p != nil && len(p.charMaps) > 0 {
		n = serApplyCharMaps(n, p.charMaps)
	}
	// An html/xhtml json-node-output-method writes the content-type <meta>
	// the same way the standalone html serializer does (output-0702,
	// result-document-1402: the nested document's <head> must gain
	// http-equiv="Content-Type"); include-content-type defaults to yes and
	// there is no way to spell it separately for the nested serialization.
	opts := xmltree.SerializeOptions{Method: method, OmitXMLDeclaration: true}
	if method == "html" || method == "xhtml" {
		opts.IncludeContentType = true
		opts.HTMLVersion = p.htmlVersion
	}
	return jsNodeText(xmltree.Serialize(n, opts)), nil
}

// jsXMLValue converts an XDM Object decoded representation into the Go value
// graph used by encoding/json (used by xml-to-json). p carries the fn:serialize
// parameters (allow-duplicate-names, json-node-output-method); nil for callers
// without any.
func jsToJSONValue(o Object, p *serParams) (any, error) {
	switch v := o.(type) {
	case *Map:
		out := &jsObj{}
		seen := map[string]bool{}
		for _, k := range v.Keys() {
			mv, err := jsToJSONValue(v.Get(k), p)
			if err != nil {
				return nil, err
			}
			name := itemString(k)
			// Distinct map keys whose string values coincide (e.g. an xs:QName
			// and a string) would emit duplicate JSON object names, which is an
			// error unless allow-duplicate-names is set (serialize-json-010/011).
			if seen[name] && (p == nil || !p.allowDup) {
				return nil, fmt.Errorf("err:SERE0022: duplicate name %q in JSON output", name)
			}
			seen[name] = true
			out.put(name, mv)
		}
		return out, nil
	case *Array:
		out := make([]any, 0, v.Size())
		for _, m := range v.Members() {
			mv, err := jsToJSONValue(m, p)
			if err != nil {
				return nil, err
			}
			out = append(out, mv)
		}
		return out, nil
	case *xmltree.Node:
		return serNodeString(v, p)
	case *Atomic:
		switch {
		case v.T == XSboolean:
			return v.Bool(), nil
		case v.IsNumeric():
			return v.Float(), nil
		default:
			return v.Lexical(), nil
		}
	case bool:
		return v, nil
	case float64:
		return v, nil
	case string:
		return v, nil
	case Sequence:
		if len(v) == 0 {
			return nil, nil
		}
		if len(v) == 1 {
			return jsToJSONValue(v[0], p)
		}
		return nil, fmt.Errorf("err:FOJS0006: sequence of more than one item cannot be serialized to JSON")
	case NodeSet:
		if len(v) == 0 {
			return nil, nil
		}
		if len(v) == 1 {
			return serNodeString(v[0], p)
		}
		return nil, fmt.Errorf("err:FOJS0006: sequence of more than one item cannot be serialized to JSON")
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("err:FOJS0006: value cannot be serialized to JSON")
	}
}

// jsXMLNodeToValue interprets a json-to-xml style element (in the
// http://www.w3.org/2005/xpath-functions namespace) and produces the Go value
// graph for re-serialization as JSON. Element local names are: map, array,
// string, number, boolean, null. The "key" attribute names map entries.
func jsXMLNodeToValue(n *xmltree.Node) (any, error) {
	return jsXMLNodeToValueIn(n, false)
}

// jsUnescape validates and decodes a JSON-escaped string (content of an
// element carrying escaped="true" / a key with escaped-key="true"). Unlike a
// JSON string literal, raw quotes and control characters stand for themselves;
// only backslash sequences are validated and decoded.
func jsUnescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("err:FOJS0007: trailing backslash in escaped string")
		}
		switch s[i+1] {
		case '"', '\\', '/':
			b.WriteByte(s[i+1])
			i += 2
		case 'b':
			b.WriteByte('\b')
			i += 2
		case 'f':
			b.WriteByte('\f')
			i += 2
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'u':
			if i+6 > len(s) {
				return "", fmt.Errorf("err:FOJS0007: truncated \\u escape")
			}
			v, err := strconv.ParseUint(s[i+2:i+6], 16, 32)
			if err != nil {
				return "", fmt.Errorf("err:FOJS0007: invalid \\u escape %q", s[i:i+6])
			}
			r := rune(v)
			// Combine a valid surrogate pair into one code point.
			if r >= 0xD800 && r <= 0xDBFF && i+12 <= len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
				if lo, err2 := strconv.ParseUint(s[i+8:i+12], 16, 32); err2 == nil && lo >= 0xDC00 && lo <= 0xDFFF {
					b.WriteRune(0x10000 + (r-0xD800)<<10 + (rune(lo) - 0xDC00))
					i += 12
					continue
				}
			}
			b.WriteRune(r)
			i += 6
		default:
			return "", fmt.Errorf("err:FOJS0007: invalid escape \\%c", s[i+1])
		}
	}
	return b.String(), nil
}

// jsValidateEscaped validates an already-JSON-escaped string and returns the
// form to emit verbatim between quotes: existing escape sequences are kept,
// while raw quotes and control characters get escaped.
func jsValidateEscaped(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\\' {
			if i+1 >= len(s) {
				return "", fmt.Errorf("err:FOJS0007: trailing backslash in escaped string")
			}
			switch s[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				b.WriteString(s[i : i+2])
				i += 2
			case 'u':
				if i+6 > len(s) {
					return "", fmt.Errorf("err:FOJS0007: truncated \\u escape")
				}
				if _, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err != nil {
					return "", fmt.Errorf("err:FOJS0007: invalid \\u escape %q", s[i:i+6])
				}
				b.WriteString(s[i : i+6])
				i += 6
			default:
				return "", fmt.Errorf("err:FOJS0007: invalid escape \\%c", s[i+1])
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '/':
			b.WriteString(`\/`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || (r >= 0x7F && r <= 0x9F):
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String(), nil
}

// jsBoolAttrOf reads a no-namespace attribute holding an xs:boolean.
func jsBoolAttrOf(n *xmltree.Node, name string) (val, present bool, err error) {
	v, ok := n.AttrLocal(name)
	if !ok {
		return false, false, nil
	}
	switch strings.TrimSpace(v) {
	case "true", "1":
		return true, true, nil
	case "false", "0":
		return false, true, nil
	}
	return false, true, fmt.Errorf("err:FOJS0006: attribute %s=%q is not an xs:boolean", name, v)
}

// jsCheckAttrs enforces the xml-to-json attribute rules: no-namespace
// attributes must be in the allowed set for the element, and attributes in the
// xpath-functions namespace are forbidden (other namespaces are ignored).
func jsCheckAttrs(n *xmltree.Node, inMap bool) error {
	for _, a := range n.Attrs {
		switch a.Name.Space {
		case "":
			// key/escaped-key are allowed anywhere (only significant inside a
			// map); escaped only on string elements.
			ok := a.Name.Local == "key" || a.Name.Local == "escaped-key" ||
				(n.Name.Local == "string" && a.Name.Local == "escaped")
			if !ok {
				return fmt.Errorf("err:FOJS0006: unexpected attribute %q on <%s>", a.Name.Local, n.Name.Local)
			}
		case jsXFNS:
			return fmt.Errorf("err:FOJS0006: attribute %s in the xpath-functions namespace", a.Name.Local)
		}
	}
	return nil
}

// jsTextOnly errors when an element that may only hold character data has
// element children; jsElementsOnly errors on non-whitespace text in a
// container element.
func jsTextOnly(n *xmltree.Node) error {
	for _, c := range n.Children {
		if c.Kind == xmltree.KindElement {
			return fmt.Errorf("err:FOJS0006: element content not allowed in <%s>", n.Name.Local)
		}
	}
	return nil
}

func jsElementsOnly(n *xmltree.Node) error {
	for _, c := range n.Children {
		if c.Kind == xmltree.KindText && strings.TrimSpace(c.Value) != "" {
			return fmt.Errorf("err:FOJS0006: text content not allowed in <%s>", n.Name.Local)
		}
	}
	return nil
}

func jsXMLNodeToValueIn(n *xmltree.Node, inMap bool) (any, error) {
	// Every element in the input must be in the xpath-functions namespace.
	if n.Name.Space != jsXFNS {
		return nil, fmt.Errorf("err:FOJS0006: element <%s> is not in the xpath-functions namespace", n.Name.Local)
	}
	if err := jsCheckAttrs(n, inMap); err != nil {
		return nil, err
	}
	switch n.Name.Local {
	case "map":
		if err := jsElementsOnly(n); err != nil {
			return nil, err
		}
		out := &jsObj{}
		seen := map[string]bool{}
		for _, c := range n.Children {
			if c.Kind != xmltree.KindElement {
				continue
			}
			key, ok := c.AttrLocal("key")
			if !ok {
				return nil, fmt.Errorf("err:FOJS0006: map child <%s> has no key attribute", c.Name.Local)
			}
			escKey := false
			if esc, present, err := jsBoolAttrOf(c, "escaped-key"); err != nil {
				return nil, err
			} else if present && esc {
				escKey = true
			}
			// Duplicate detection compares the DECODED key; output keeps the
			// escaped form verbatim.
			decoded := key
			if escKey {
				var err error
				decoded, err = jsUnescape(key)
				if err != nil {
					return nil, err
				}
			}
			if seen[decoded] {
				return nil, fmt.Errorf("err:FOJS0006: duplicate key %q in map", decoded)
			}
			seen[decoded] = true
			cv, err := jsXMLNodeToValueIn(c, true)
			if err != nil {
				return nil, err
			}
			if escKey {
				raw, err := jsValidateEscaped(key)
				if err != nil {
					return nil, err
				}
				out.putRaw(raw, cv)
			} else {
				out.put(key, cv)
			}
		}
		return out, nil
	case "array":
		if err := jsElementsOnly(n); err != nil {
			return nil, err
		}
		out := []any{}
		for _, c := range n.Children {
			if c.Kind != xmltree.KindElement {
				continue
			}
			cv, err := jsXMLNodeToValueIn(c, false)
			if err != nil {
				return nil, err
			}
			out = append(out, cv)
		}
		return out, nil
	case "string":
		if err := jsTextOnly(n); err != nil {
			return nil, err
		}
		s := n.StringValue()
		if esc, present, err := jsBoolAttrOf(n, "escaped"); err != nil {
			return nil, err
		} else if present && esc {
			raw, err := jsValidateEscaped(s)
			if err != nil {
				return nil, err
			}
			return jsPreEscaped(raw), nil
		}
		return s, nil
	case "number":
		if err := jsTextOnly(n); err != nil {
			return nil, err
		}
		s := strings.TrimSpace(n.StringValue())
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("err:FOJS0006: invalid number: %v", err)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("err:FOJS0006: number %q is not a valid JSON number", s)
		}
		// The number is the xs:double VALUE, serialized canonically (the
		// lexical form is not preserved: " -0e0 " serializes as "-0").
		rendered, _ := jsMarshalNumber(f)
		return json.RawMessage(rendered), nil
	case "boolean":
		if err := jsTextOnly(n); err != nil {
			return nil, err
		}
		switch strings.TrimSpace(n.StringValue()) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		default:
			return nil, fmt.Errorf("err:FOJS0006: invalid boolean %q", n.StringValue())
		}
	case "null":
		if err := jsTextOnly(n); err != nil {
			return nil, err
		}
		if strings.TrimSpace(n.StringValue()) != "" {
			return nil, fmt.Errorf("err:FOJS0006: <null> must be empty")
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("err:FOJS0006: unexpected element <%s> in json-to-xml input", n.Name.Local)
	}
}

// fnJSXMLToJSON implements fn:xml-to-json($input as node()?, $options?). It
// converts an element tree in the xpath-functions namespace into a JSON string.
func fnJSXMLToJSON(ctx *Context, args []Object) (Object, error) {
	in := arg(args, 0)
	if jsIsEmpty(in) {
		return Sequence{}, nil
	}
	ns, ok := ToNodeSet(in)
	if !ok || len(ns) == 0 {
		return nil, fmt.Errorf("err:FOJS0006: xml-to-json expects a node")
	}
	// The input must be a single document or element node; anything else (a
	// sequence of several nodes, an attribute, an atomic) is a type error.
	if len(ns) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: xml-to-json expects a single document or element node")
	}
	// Validate the options map (second argument): 'indent' must be a single
	// xs:boolean.
	indent := false
	if len(args) > 1 {
		if m, ok := arg(args, 1).(*Map); ok && m.Contains(NewString("indent")) {
			iv := m.Get(NewString("indent"))
			items := Items(iv)
			if len(items) != 1 {
				return nil, fmt.Errorf("err:XPTY0004: xml-to-json 'indent' option must be a single xs:boolean")
			}
			switch it := items[0].(type) {
			case bool:
				indent = it
			case *Atomic:
				if it.T != XSboolean {
					return nil, fmt.Errorf("err:XPTY0004: xml-to-json 'indent' option must be an xs:boolean")
				}
				indent = it.Bool()
			default:
				return nil, fmt.Errorf("err:XPTY0004: xml-to-json 'indent' option must be an xs:boolean")
			}
		}
	}
	root := ns[0]
	// If given a document node, it must have exactly one element child (and
	// only whitespace text besides it).
	if root.Kind == xmltree.KindDocument {
		var el *xmltree.Node
		for _, c := range root.Children {
			switch c.Kind {
			case xmltree.KindElement:
				if el != nil {
					return nil, fmt.Errorf("err:FOJS0006: document has more than one element child")
				}
				el = c
			case xmltree.KindText:
				if strings.TrimSpace(c.Value) != "" {
					return nil, fmt.Errorf("err:FOJS0006: document has top-level text content")
				}
			}
		}
		if el == nil {
			return nil, fmt.Errorf("err:FOJS0006: document has no element child")
		}
		root = el
	}
	if root.Kind != xmltree.KindElement || root.Name.Space != jsXFNS {
		return nil, fmt.Errorf("err:XPTY0004: xml-to-json root must be an element in the xpath-functions namespace")
	}
	v, err := jsXMLNodeToValue(root)
	if err != nil {
		return nil, err
	}
	b, err := jsMarshalOpts(v, jsEmitOpts{indent: indent})
	if err != nil {
		return nil, fmt.Errorf("err:FOJS0006: %v", err)
	}
	return NewString(b), nil
}

// jsBuildXMLNode builds a json-to-xml style element node for the given Go value
// graph (as produced by encoding/json with UseNumber). When key is non-empty a
// "key" attribute is set on the produced element.
func jsBuildXMLNode(v any, key string) *xmltree.Node {
	return jsBuildXMLNodeEsc(v, key, false)
}

// jsBuildXMLNodeEsc is jsBuildXMLNode for the escape=true option, where the
// string values already carry JSON escape sequences instead of the characters
// they stand for. Such a value is marked escaped="true" (a key, escaped-key=
// "true") so fn:xml-to-json can read it back — but only when it actually
// CONTAINS a backslash escape; a string that needed none is indistinguishable
// from an unescaped one and carries no attribute (json-to-xml-escape-003 vs
// -005/-006).
func jsBuildXMLNodeEsc(v any, key string, escape bool) *xmltree.Node {
	esc := func(s string) bool { return escape && strings.Contains(s, `\`) }
	var el *xmltree.Node
	switch t := v.(type) {
	case nil:
		el = xmltree.NewElement(xmltree.Name{Local: "null", Space: jsXFNS})
	case bool:
		el = xmltree.NewElement(xmltree.Name{Local: "boolean", Space: jsXFNS})
		el.Append(xmltree.NewText(strconv.FormatBool(t)))
	case string:
		el = xmltree.NewElement(xmltree.Name{Local: "string", Space: jsXFNS})
		el.Append(xmltree.NewText(t))
		if esc(t) {
			el.SetAttr(xmltree.Name{Local: "escaped"}, "true")
		}
	case json.RawMessage:
		el = xmltree.NewElement(xmltree.Name{Local: "number", Space: jsXFNS})
		el.Append(xmltree.NewText(string(t)))
	case json.Number:
		el = xmltree.NewElement(xmltree.Name{Local: "number", Space: jsXFNS})
		el.Append(xmltree.NewText(string(t)))
	case float64:
		el = xmltree.NewElement(xmltree.Name{Local: "number", Space: jsXFNS})
		el.Append(xmltree.NewText(strconv.FormatFloat(t, 'g', -1, 64)))
	case *jsObj:
		el = xmltree.NewElement(xmltree.Name{Local: "map", Space: jsXFNS})
		for i, k := range t.keys {
			el.Append(jsBuildXMLNodeEsc(t.vals[i], k, escape))
		}
	case []any:
		el = xmltree.NewElement(xmltree.Name{Local: "array", Space: jsXFNS})
		for _, mv := range t {
			el.Append(jsBuildXMLNodeEsc(mv, "", escape))
		}
	default:
		el = xmltree.NewElement(xmltree.Name{Local: "null", Space: jsXFNS})
	}
	if key != "" {
		el.SetAttr(xmltree.Name{Local: "key"}, key)
		if esc(key) {
			el.SetAttr(xmltree.Name{Local: "escaped-key"}, "true")
		}
	}
	return el
}

// fnJSJSONToXML implements fn:json-to-xml($json as xs:string?, $options?). It
// parses the JSON string and returns an element tree in the xpath-functions
// namespace, wrapped in a document node.
func fnJSJSONToXML(ctx *Context, args []Object) (Object, error) {
	in := arg(args, 0)
	if jsIsEmpty(in) {
		return Sequence{}, nil
	}
	// Validate the options map: 'liberal'/'validate' must be xs:boolean.
	reject, useFirst, validate := false, false, false
	var opts jsOptions
	if len(args) > 1 {
		if m, ok := arg(args, 1).(*Map); ok {
			// Each of these takes exactly one xs:boolean; the empty sequence,
			// a longer sequence and a non-boolean are all XPTY0004 — a TYPE
			// mismatch against the option map's record type, not the
			// FOJS0005 that a wrong VALUE of the right type gets
			// (json-to-xml-error-020/022/023/025/026/027).
			for _, opt := range []string{"liberal", "validate", "escape"} {
				b, present, err := jsBoolOption(m, opt)
				if err != nil {
					return nil, err
				}
				if present && opt == "escape" {
					opts.escape = b
				}
				// validate:true() asks for the result to be validated against
				// the schema for the XPath functions namespace, which only a
				// schema-aware processor can do (error-3245a). Whether this
				// run is one is settled below, once the tree exists: the
				// option's other effects (option-type checking above,
				// FOJS0001 on malformed input) are unconditional, and F&O
				// orders neither against the other.
				if opt == "validate" && present && b {
					validate = true
				}
			}
			// json-to-xml accepts only reject|use-first|retain for duplicates
			// (use-last/use-any are parse-json-only → FOJS0005, json-to-xml-error-040).
			if dv := m.Get(NewString("duplicates")); !jsIsEmpty(dv) {
				switch itemString(firstItem(dv)) {
				case "reject":
					reject = true
				case "use-first":
					useFirst = true
				case "retain":
				default:
					return nil, fmt.Errorf("err:FOJS0005: invalid 'duplicates' value for json-to-xml")
				}
			}
			// A fallback must be a single-argument function (json-to-xml-error-041).
			if fv := m.Get(NewString("fallback")); !jsIsEmpty(fv) {
				fn, ok := firstItem(fv).(*Function)
				if !ok {
					return nil, fmt.Errorf("err:XPTY0004: 'fallback' must be a function")
				}
				if fn.Arity != 1 {
					return nil, fmt.Errorf("err:XPTY0004: 'fallback' function must have arity 1")
				}
				// fallback is only meaningful with escape=false; combining the
				// two is an error (json-to-xml-027).
				if ev := m.Get(NewString("escape")); !jsIsEmpty(ev) && ToBool(ev) {
					return nil, fmt.Errorf("err:FOJS0005: options \"escape\" and \"fallback\" cannot be combined")
				}
				opts.fallback = fn
			}
		}
	}
	src := ToString(in)
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	tok := &jsTokenizer{dec: dec, src: src, opts: opts}
	v, err := jsDecodeOrdered(tok)
	if err != nil {
		return nil, err
	}
	if err := jsCheckTrailing(src, dec); err != nil {
		return nil, err
	}
	if reject {
		if err := jsCheckNoDupKeys(v); err != nil {
			return nil, err
		}
	}
	if useFirst {
		v = jsDedupFirst(v)
	}
	doc := &xmltree.Node{Kind: xmltree.KindDocument}
	root := jsBuildXMLNodeEsc(v, "", opts.escape)
	// The elements are built with their expanded name only; the serializer
	// synthesizes xmlns="…" from Name.Space at write time, so the output looks
	// right, but the NAMESPACE AXIS is derived from Node.NS and would be empty
	// (static-030 counts //namespace::*). Declaring the default namespace on
	// the root gives every descendant the binding by inheritance, exactly as a
	// parsed document would have it.
	if root != nil && root.Kind == xmltree.KindElement {
		root.NS = append(root.NS, &xmltree.Node{
			Kind: xmltree.KindNamespace, Name: xmltree.Name{Local: ""}, Value: jsXFNS, Parent: root,
		})
	}
	doc.Append(root)
	// validate:true() (F&O 3.1 §17.5.1): "the resulting XDM instance is
	// validated against the schema for the namespace
	// http://www.w3.org/2005/xpath-functions", which annotates every element
	// with its declared type — the whole point of the option, since the tree
	// json-to-xml builds is otherwise untyped and element(j:map, j:mapType)
	// can never match it.
	//
	// FOJS0004 is raised exactly when there is no validator to ask: a
	// non-schema-aware run installs none at all (error-3245a), and neither
	// does a schema-aware run whose stylesheet brought no components into
	// scope — in both cases this processor genuinely cannot perform the
	// requested validation, which is what the error says.
	if validate {
		v := schemaDocumentValidator(ctx)
		if v == nil {
			return nil, fmt.Errorf("err:FOJS0004: the json-to-xml 'validate' option requires a schema-aware processor")
		}
		if err := v.ValidateSchemaDocument(doc); err != nil {
			// Reaching here means the tree this function itself constructed
			// does not satisfy the schema it is defined to satisfy, so there
			// is no catalogued code for it — failing closed with the
			// validator's own reason beats silently returning an untyped tree
			// the caller was told would be typed.
			return nil, fmt.Errorf("err:FOJS0004: the json-to-xml result could not be validated against the schema for %s: %v", jsXFNS, err)
		}
	}
	return NodeSet{doc}, nil
}

// jsCheckNoDupKeys reports FOJS0003 if any object in the decoded graph has a
// duplicate key (used by json-to-xml with duplicates="reject").
func jsCheckNoDupKeys(v any) error {
	switch t := v.(type) {
	case *jsObj:
		seen := map[string]bool{}
		for i, k := range t.keys {
			if seen[k] {
				return fmt.Errorf("err:FOJS0003: duplicate key %q in JSON object", k)
			}
			seen[k] = true
			if err := jsCheckNoDupKeys(t.vals[i]); err != nil {
				return err
			}
		}
	case []any:
		for _, m := range t {
			if err := jsCheckNoDupKeys(m); err != nil {
				return err
			}
		}
	}
	return nil
}

// serApplyCharMaps returns a copy of n with use-character-maps substitutions
// applied to text and attribute values (serialize-xml-138b: {'x':'j','m':'so',
// 'l':'n'} turns <e>xml</e> into <e>json</e>).
func serApplyCharMaps(n *xmltree.Node, maps map[string]string) *xmltree.Node {
	mapStr := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if rep, ok := maps[string(r)]; ok {
				b.WriteString(rep)
			} else {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	var clone func(x *xmltree.Node, parent *xmltree.Node) *xmltree.Node
	clone = func(x *xmltree.Node, parent *xmltree.Node) *xmltree.Node {
		cp := *x
		cp.Parent = parent
		if x.Kind == xmltree.KindText {
			cp.Value = mapStr(x.Value)
		}
		cp.Attrs = make([]*xmltree.Node, len(x.Attrs))
		for i, a := range x.Attrs {
			ac := *a
			ac.Parent = &cp
			ac.Value = mapStr(a.Value)
			cp.Attrs[i] = &ac
		}
		cp.Children = make([]*xmltree.Node, len(x.Children))
		for i, ch := range x.Children {
			cp.Children[i] = clone(ch, &cp)
		}
		return &cp
	}
	return clone(n, n.Parent)
}
