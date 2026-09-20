package xpath

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"golang.org/x/text/unicode/norm"
)

func init() {
	coreFuncs["string-to-codepoints"] = fnStringToCodepoints
	coreFuncs["codepoints-to-string"] = fnCodepointsToString
	coreFuncs["collation-key"] = fnCollationKey
	coreFuncs["default-collation"] = func(c *Context, a []Object) (Object, error) {
		// The static default collation is the Unicode codepoint collation.
		return NewString(collCodepoint), nil
	}
	coreFuncs["generate-id"] = fnGenerateID
	coreFuncs["compare"] = fnCompare
	coreFuncs["codepoint-equal"] = fnCodepointEqual
	coreFuncs["normalize-unicode"] = fnNormalizeUnicode
	coreFuncs["contains-token"] = fnContainsToken
}

// fnContainsToken implements fn:contains-token($input as xs:string*, $token,
// $collation?). It treats each $input string as whitespace-separated tokens and
// returns true if any equals the whitespace-collapsed $token (default codepoint
// collation). A token that is empty or all-whitespace never matches.
func fnContainsToken(c *Context, a []Object) (Object, error) {
	// Tokens are separated by XML whitespace ONLY (#x9/#xA/#xD/#x20); other
	// Unicode spaces such as U+00A0 do NOT separate (fn-contains-token-21/51).
	// $token is whitespace-trimmed; a token comparison honours the optional
	// collation (fn-contains-token-70/71 use the case-insensitive collation).
	token := strings.TrimFunc(itemString(firstItem(arg(a, 1))), isXMLSpaceRune)
	if token == "" {
		return NewBool(false), nil
	}
	cmp := func(x, y string) int {
		if x == y {
			return 0
		}
		return 1
	}
	if len(a) > 2 && !strIsEmpty(arg(a, 2)) {
		coll, err := collationArg(arg(a, 2))
		if err != nil {
			return nil, err
		}
		cmp = coll
	}
	for _, it := range Items(arg(a, 0)) {
		for _, tok := range strings.FieldsFunc(itemString(it), isXMLSpaceRune) {
			if cmp(tok, token) == 0 {
				return NewBool(true), nil
			}
		}
	}
	return NewBool(false), nil
}

// isXMLSpaceRune reports whether r is one of the four XML whitespace characters.
func isXMLSpaceRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// strIsEmpty reports whether an argument represents the empty sequence. A
// missing argument (nil), an empty Sequence, or an empty NodeSet all count as
// empty.
func strIsEmpty(o Object) bool {
	if o == nil {
		return true
	}
	return len(Items(o)) == 0
}

// strEmptySeq is the canonical empty-sequence return value.
func strEmptySeq() Object { return Sequence{} }

// fnStringToCodepoints implements fn:string-to-codepoints. It returns a
// sequence of xs:integer Unicode code points (runes) for the input string.
// An empty/zero-length string yields the empty sequence.
func fnStringToCodepoints(c *Context, a []Object) (Object, error) {
	// $arg is xs:string?: a non-string atomic is XPTY0004
	// (fn-string-to-codepoints1args-7).
	s, err := stringArgStrict("string-to-codepoints", a, 0, true)
	if err != nil {
		return nil, err
	}
	if s == "" {
		return strEmptySeq(), nil
	}
	runes := []rune(s)
	items := make([]Item, len(runes))
	for i, r := range runes {
		items[i] = NewInteger(int64(r))
	}
	return FromItems(items), nil
}

// fnCodepointsToString implements fn:codepoints-to-string. It builds an
// xs:string from a sequence of integer code points.
func fnCodepointsToString(c *Context, a []Object) (Object, error) {
	items := Items(arg(a, 0))
	var b strings.Builder
	for _, it := range items {
		// $arg is xs:integer*: function conversion casts only untypedAtomic
		// (and accepts integer-family values); any other atomic type is
		// XPTY0004 (FunctionCall-012: xs:NMTOKENS values are not integers).
		if at, ok := it.(*Atomic); ok && !at.IsNumeric() && at.T != XSuntypedAtomic {
			return nil, fmt.Errorf("err:XPTY0004: codepoints-to-string expects xs:integer*, got %s", at.T)
		}
		cp := int64(itemNumber(it))
		// FOCH0001: each code point must be a legal XML character per the
		// Char production of the host's declared XML version
		// (K-CodepointToStringFunc-8/8a/11/11b/12/12b, cbcl-codepoints-to-
		// string-023/024: codepoints 8/11/12/14/15 are legal Chars under
		// XML 1.1 — [#x1-#xD7FF] | [#xE000-#xFFFD] | [#x10000-#x10FFFF] —
		// but illegal under 1.0, which additionally excludes every C0
		// control other than tab/LF/CR). Only an EXPLICIT "1.0" is strict:
		// a host that never sets XMLVersion at all (every caller besides the
		// QT3 harness, which sets it per-case from the catalog's own
		// dependency — XSLT included, xml-to-json-D015/017/018) keeps this
		// engine's long-standing lenient default rather than newly rejecting
		// a C0 control it previously always accepted. c may be nil (helper
		// reuse).
		legal := cp >= 0x10000 && cp <= 0x10FFFF ||
			cp >= 0xE000 && cp <= 0xFFFD
		if c != nil && c.XMLVersion == "1.0" {
			legal = legal || cp == 0x9 || cp == 0xA || cp == 0xD || (cp >= 0x20 && cp <= 0xD7FF)
		} else {
			legal = legal || (cp >= 0x1 && cp <= 0xD7FF)
		}
		if !legal {
			return nil, fmt.Errorf("err:FOCH0001: codepoint %d is not a valid XML character", cp)
		}
		b.WriteRune(rune(cp))
	}
	return NewString(b.String()), nil
}

// fnCompare implements fn:compare. It returns -1, 0, or 1 according to the
// two strings' collation order: an explicit third (collation) argument, else
// the in-scope default collation, else codepoint order.
func fnCompare(c *Context, a []Object) (Object, error) {
	var cmpFn func(a, b string) int
	if len(a) >= 3 {
		var err error
		if cmpFn, err = resolveCollator(ToString(arg(a, 2))); err != nil {
			return nil, err
		}
	} else if coll := ctxCollation(c); coll != nil {
		cmpFn = coll
	} else {
		cmpFn = strings.Compare
	}
	if strIsEmpty(arg(a, 0)) || strIsEmpty(arg(a, 1)) {
		return strEmptySeq(), nil
	}
	// Both operands are xs:string?: compare(123, 456) is XPTY0004 (compare-011).
	s1, err := stringArgStrict("compare", a, 0, true)
	if err != nil {
		return nil, err
	}
	s2, err := stringArgStrict("compare", a, 1, true)
	if err != nil {
		return nil, err
	}
	cmp := cmpFn(s1, s2)
	if cmp < 0 {
		cmp = -1
	} else if cmp > 0 {
		cmp = 1
	}
	return NewInteger(int64(cmp)), nil
}

// fnCodepointEqual implements fn:codepoint-equal. It returns an xs:boolean that
// is true when the two strings are equal under codepoint comparison. If either
// argument is the empty sequence the result is the empty sequence.
func fnCodepointEqual(c *Context, a []Object) (Object, error) {
	if strIsEmpty(arg(a, 0)) || strIsEmpty(arg(a, 1)) {
		return strEmptySeq(), nil
	}
	// xs:string? operands: an xs:integer is XPTY0004 (fn-codepoint-equal-10/11).
	s1, err := stringArgStrict("codepoint-equal", a, 0, true)
	if err != nil {
		return nil, err
	}
	s2, err := stringArgStrict("codepoint-equal", a, 1, true)
	if err != nil {
		return nil, err
	}
	return NewBool(s1 == s2), nil
}

// fnNormalizeUnicode implements fn:normalize-unicode($arg, $form?). It applies
// the requested normalization form (NFC default, plus NFD/NFKC/NFKD) using
// golang.org/x/text/unicode/norm. The empty form "" leaves the input unchanged.
func fnNormalizeUnicode(c *Context, a []Object) (Object, error) {
	// $arg is xs:string? (fn-normalize-unicode1args-7: 12 is XPTY0004) and
	// $normalizationForm a REQUIRED xs:string (2args-5: () is XPTY0004).
	s, err := stringArgStrict("normalize-unicode", a, 0, true)
	if err != nil {
		return nil, err
	}
	form := "NFC"
	if len(a) >= 2 {
		f, err := stringArgStrict("normalize-unicode", a, 1, false)
		if err != nil {
			return nil, err
		}
		form = strings.ToUpper(strings.TrimSpace(f))
	}
	switch form {
	case "":
		return NewString(s), nil
	case "NFC":
		return NewString(norm.NFC.String(s)), nil
	case "NFD":
		return NewString(norm.NFD.String(s)), nil
	case "NFKC":
		return NewString(norm.NFKC.String(s)), nil
	case "NFKD":
		return NewString(norm.NFKD.String(s)), nil
	case "FULLY-NORMALIZED":
		// F&O 3.1 §5.4.6: a string is fully-normalized if (a) it is in NFC
		// and (b) it does not start with a "composing character" — one that
		// is either the second character of some non-excluded canonical
		// decomposition, or has non-zero canonical combining class. The
		// conversion prepends a space before such a leading character, then
		// applies NFC. golang.org/x/text/unicode/norm's Properties.
		// BoundaryBefore() is exactly !composing (ccc==0 && !combinesBackward,
		// where combinesBackward is the "second character of a decomposition"
		// NFC_QC=Maybe flag), so its negation is the spec's "composing"
		// predicate (cbcl-fn-normalize-unicode-001/006).
		if s != "" {
			if p := norm.NFC.PropertiesString(s); !p.BoundaryBefore() {
				s = " " + s
			}
		}
		return NewString(norm.NFC.String(s)), nil
	default:
		return nil, fmt.Errorf("err:FOCH0003: unsupported normalization form %q", form)
	}
}

// fnCollationKey implements fn:collation-key: an opaque xs:base64Binary whose
// byte equality/order mirrors the collation's.
func fnCollationKey(c *Context, a []Object) (Object, error) {
	coll := ""
	if len(a) >= 2 {
		coll = ToString(arg(a, 1))
	}
	keyer, err := resolveCollationKeyer(coll)
	if err != nil {
		return nil, err
	}
	items := Items(arg(a, 0))
	if len(items) != 1 {
		return nil, fmt.Errorf("err:XPTY0004: collation-key requires exactly one string")
	}
	if at, ok := items[0].(*Atomic); ok && !(isStringType(at.T) || at.T == XSuntypedAtomic || at.T == XSanyURI) {
		return nil, fmt.Errorf("err:XPTY0004: collation-key requires a string, got %s", at.T)
	}
	return &Atomic{T: XSbase64Binary, bin: keyer(ToString(arg(a, 0)))}, nil
}

// fnGenerateID implements fn:generate-id (XPath 3.0): a stable NCName-shaped
// identifier per node; () yields the empty string.
func fnGenerateID(c *Context, a []Object) (Object, error) {
	var n *xmltree.Node
	if len(a) == 0 {
		if c.Node == nil {
			return nil, fmt.Errorf("err:XPDY0002: generate-id() with no context node")
		}
		n = c.Node
	} else {
		items := Items(arg(a, 0))
		if len(items) == 0 {
			return NewString(""), nil
		}
		nd, ok := items[0].(*xmltree.Node)
		if !ok {
			return nil, fmt.Errorf("err:XPTY0004: generate-id argument must be a node")
		}
		if len(items) > 1 && !(c != nil && c.BC10) {
			return nil, fmt.Errorf("err:XPTY0004: generate-id argument must be a node")
		}
		n = nd
	}
	// n.Order() is assigned per-tree: xmltree.Parse numbers each parsed
	// document from 0, and a constructed result-tree fragment (an xsl:variable
	// holding element content, a document() call, xsl:copy's temp trees, …)
	// is never renumbered at all, defaulting to 0 throughout. Order() alone
	// therefore collides across trees — two nodes from different document()
	// calls, or from different temporary trees, can share the same order
	// (xslt30 key-042). Fold in the containing tree's identity (its root
	// node's address, stable for the process's lifetime) so generate-id()
	// stays unique across every tree reachable in one execution, not just
	// within one.
	tag := fmt.Sprintf("t%p", n.Root())
	if n.Kind == xmltree.KindNamespace {
		// An in-scope namespace node inherited from an ancestor shares its
		// element's document-order slot (xmltree.NewInheritedNamespace), so the
		// prefix is needed to keep IDs unique (generate-id-011).
		return NewString("d" + tag + "n" + strconv.Itoa(n.Order()) + "ns" + n.Name.Local), nil
	}
	return NewString("d" + tag + "n" + strconv.Itoa(n.Order())), nil
}
