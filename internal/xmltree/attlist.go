package xmltree

import (
	"os"
	"path/filepath"
	"strings"
)

// idAttrKinds scans the DOCTYPE internal subset for <!ATTLIST> declarations and
// returns a map element-name -> attr-name -> IDKind (ID/IDREF/IDREFS), so the
// parser can mark id-typed attributes for fn:id / fn:idref.
//
// One ATTLIST may declare several attributes: <!ATTLIST e a1 ID #REQUIRED a2
// CDATA #IMPLIED …>. Only the ID/IDREF/IDREFS types are recorded; everything
// else is ignored. Names are matched by their lexical (possibly prefixed) form,
// which is how DTD declarations and instance markup both spell them.
func idAttrKinds(src string) map[string]map[string]uint8 {
	sub, ok := internalSubset(src)
	if !ok {
		return nil
	}
	out := map[string]map[string]uint8{}
	for i := 0; ; {
		j := strings.Index(sub[i:], "<!ATTLIST")
		if j < 0 {
			break
		}
		p := i + j + len("<!ATTLIST")
		end := strings.IndexByte(sub[p:], '>')
		if end < 0 {
			break
		}
		body := sub[p : p+end]
		i = p + end + 1
		fields := strings.Fields(body)
		if len(fields) < 2 {
			continue
		}
		elem := localPart(fields[0])
		// Walk the (attr, type, default…) triples. The type may be followed by
		// a defaulting keyword (#REQUIRED/#IMPLIED/#FIXED value) or a literal;
		// stepping token-by-token and recording only recognised types is robust
		// to enumerated types "(a|b)" and default values.
		for k := 1; k+1 < len(fields); k++ {
			var kind uint8
			switch fields[k+1] {
			case "ID":
				kind = IDKindID
			case "IDREF":
				kind = IDKindIDREF
			case "IDREFS":
				kind = IDKindIDREFS
			default:
				continue
			}
			if out[elem] == nil {
				out[elem] = map[string]uint8{}
			}
			out[elem][localPart(fields[k])] = kind
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// localPart returns the part of a (possibly prefixed) name after the colon.
func localPart(s string) string {
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// elementOnlyDecls scans a document's DOCTYPE internal subset — plus, when
// baseDir is non-empty, its external SYSTEM subset resolved against baseDir
// (number-4501: <!DOCTYPE iddata SYSTEM "number-45.dtd">) — for <!ELEMENT>
// content models, and returns the set of (local) element names declared with
// ELEMENT CONTENT — a children content model with no "#PCDATA" alternative
// (so NOT EMPTY, ANY, or Mixed). A validating processor treats whitespace-only
// text found directly in such an element as ignorable whitespace and strips it
// by default, even with no explicit xsl:strip-space (id-003/id-036: a
// stylesheet that never declares xsl:strip-space still gets clean output over
// a DTD like "<!ELEMENT t04 (a*)>"). An unreadable/absent external file is
// silently skipped, matching documentEntities' treatment of the same case.
func elementOnlyDecls(src, baseDir string) map[string]bool {
	out := map[string]bool{}
	if sub, ok := internalSubset(src); ok {
		scanElementOnlyDecls(sub, out)
	}
	if baseDir != "" {
		if sysID, ok := externalDTDSystemID(src); ok {
			path := sysID
			if !filepath.IsAbs(path) {
				path = filepath.Join(baseDir, path)
			}
			if data, err := os.ReadFile(path); err == nil {
				scanElementOnlyDecls(string(data), out)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scanElementOnlyDecls scans DTD subset text for <!ELEMENT> declarations and
// records the element-content ones (see elementOnlyDecls) into out.
func scanElementOnlyDecls(sub string, out map[string]bool) {
	for i := 0; ; {
		j := strings.Index(sub[i:], "<!ELEMENT")
		if j < 0 {
			break
		}
		p := i + j + len("<!ELEMENT")
		end := strings.IndexByte(sub[p:], '>')
		if end < 0 {
			break
		}
		body := strings.TrimSpace(sub[p : p+end])
		i = p + end + 1
		k := strings.IndexAny(body, " \t\r\n")
		if k < 0 {
			continue
		}
		name := body[:k]
		content := strings.TrimSpace(body[k+1:])
		switch {
		case content == "EMPTY", content == "ANY":
			continue
		case strings.Contains(content, "#PCDATA"):
			continue // mixed content — whitespace is significant
		}
		out[localPart(name)] = true
	}
}

// attlistDefault is a DTD ATTLIST's #FIXED or plain default value for one
// attribute — the value the infoset supplies when the attribute is absent
// from the instance (attribute-0501).
type attlistDefault struct {
	value string
	fixed bool // #FIXED "value" vs a plain default "value" (both supply value; unused here beyond documentation)
}

// attlistDefaults scans the DOCTYPE internal subset for <!ATTLIST> declared
// default/#FIXED attribute values, keyed by element-local-name -> attribute-
// local-name. #REQUIRED and #IMPLIED attributes supply no value and are not
// recorded. Only the internal subset is read — no external DTD subset is
// fetched here (unlike documentEntities' character-entity support).
func attlistDefaults(src string) map[string]map[string]attlistDefault {
	sub, ok := internalSubset(src)
	if !ok {
		return nil
	}
	out := map[string]map[string]attlistDefault{}
	for i := 0; ; {
		j := strings.Index(sub[i:], "<!ATTLIST")
		if j < 0 {
			break
		}
		p := i + j + len("<!ATTLIST")
		end := strings.IndexByte(sub[p:], '>')
		if end < 0 {
			break
		}
		body := sub[p : p+end]
		i = p + end + 1

		elem, pos, ok := attlistToken(body, 0)
		if !ok {
			continue
		}
		elem = localPart(elem)
		for {
			name, np, ok := attlistToken(body, pos)
			if !ok {
				break
			}
			typ, tp, ok := attlistToken(body, np)
			if !ok {
				break
			}
			if typ == "NOTATION" {
				// The enumeration list is a separate parenthesized token.
				if _, tp2, ok2 := attlistToken(body, tp); ok2 {
					tp = tp2
				}
			}
			def, dp, ok := attlistToken(body, tp)
			if !ok {
				break
			}
			pos = dp
			switch def {
			case "#REQUIRED", "#IMPLIED":
				// no value supplied when absent
			case "#FIXED":
				val, dp2, ok := attlistToken(body, dp)
				if !ok {
					continue
				}
				pos = dp2
				if lit, ok := unquoteAttlist(val); ok {
					recordAttlistDefault(out, elem, localPart(name), attlistDefault{value: lit, fixed: true})
				}
			default:
				if lit, ok := unquoteAttlist(def); ok {
					recordAttlistDefault(out, elem, localPart(name), attlistDefault{value: lit})
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func recordAttlistDefault(out map[string]map[string]attlistDefault, elem, attr string, d attlistDefault) {
	if out[elem] == nil {
		out[elem] = map[string]attlistDefault{}
	}
	out[elem][attr] = d
}

// unquoteAttlist strips a '...'/"..." literal's quotes, reporting ok=false for
// anything else (a bare keyword/enumeration token, not a value literal).
func unquoteAttlist(s string) (string, bool) {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1], true
	}
	return "", false
}

// attlistToken reads one ATTLIST token starting at i: a parenthesized group
// (an enumeration or NOTATION list, kept together as one token), a quoted
// literal (kept WITH its quotes, for unquoteAttlist to strip), or a bare
// whitespace-delimited word.
func attlistToken(s string, i int) (tok string, next int, ok bool) {
	i = skipSpace(s, i)
	if i >= len(s) {
		return "", i, false
	}
	switch s[i] {
	case '(':
		depth, start := 0, i
		for i < len(s) {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return s[start : i+1], i + 1, true
				}
			}
			i++
		}
		return s[start:], i, true
	case '"', '\'':
		q, start := s[i], i
		i++
		for i < len(s) && s[i] != q {
			i++
		}
		if i < len(s) {
			i++
		}
		return s[start:i], i, true
	default:
		start := i
		for i < len(s) && !isXMLSpace(s[i]) && s[i] != '(' {
			i++
		}
		return s[start:i], i, true
	}
}
