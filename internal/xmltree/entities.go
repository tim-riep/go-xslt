package xmltree

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// internalEntities extracts the general entity declarations from a document's
// DOCTYPE internal subset, so encoding/xml (which does not process DTDs) can
// still resolve references to them via Decoder.Entity.
//
// Two XML rules matter here and are both implemented: the FIRST declaration of
// a name wins (later ones are ignored, not overrides), and an entity's
// replacement text may itself contain entity references, which are expanded
// here because encoding/xml substitutes Decoder.Entity values verbatim.
//
// Only internal general entities with a literal value are handled; parameter
// entities (<!ENTITY % …>) and external ones (SYSTEM/PUBLIC) are skipped, and
// replacement text is inserted as character data, not as markup.
// UnparsedEntities returns the names of the unparsed (NDATA) entities declared
// in the document's DOCTYPE internal subset — the xs:ENTITY value space.
func UnparsedEntities(src string) map[string]bool {
	decls := unparsedEntityDecls(src, "")
	if len(decls) == 0 {
		return nil
	}
	out := make(map[string]bool, len(decls))
	for name := range decls {
		out[name] = true
	}
	return out
}

// UnparsedEntity is one <!ENTITY … NDATA …> declaration's external identifier:
// the (possibly relative) system identifier and the public identifier, if any.
type UnparsedEntity struct {
	SystemID string
	PublicID string
}

// unparsedEntityDecls collects the unparsed (NDATA) general entities declared
// by a document: its DOCTYPE internal subset first and — when baseDir is
// non-empty — the external SYSTEM subset after it, so the internal declaration
// of a name wins. Within one subset the FIRST declaration of a name is binding
// (XML §4.2, unparsed-entity-50).
func unparsedEntityDecls(src, baseDir string) map[string]UnparsedEntity {
	out := map[string]UnparsedEntity{}
	if sub, ok := internalSubset(src); ok {
		scanUnparsedEntityDecls(sub, out)
	}
	if baseDir != "" {
		if sysID, ok := externalDTDSystemID(src); ok {
			path := sysID
			if !filepath.IsAbs(path) {
				path = filepath.Join(baseDir, path)
			}
			if data, err := os.ReadFile(path); err == nil {
				scanUnparsedEntityDecls(string(data), out)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scanUnparsedEntityDecls records every NDATA entity declaration found in one
// DTD subset into out, never overriding a name already present.
func scanUnparsedEntityDecls(sub string, out map[string]UnparsedEntity) {
	for i := 0; ; {
		j := strings.Index(sub[i:], "<!ENTITY")
		if j < 0 {
			break
		}
		p := i + j + len("<!ENTITY")
		i = p
		p = skipSpace(sub, p)
		if p >= len(sub) || sub[p] == '%' {
			continue // parameter entity
		}
		start := p
		for p < len(sub) && !isXMLSpace(sub[p]) && sub[p] != '>' {
			p++
		}
		name := sub[start:p]
		end := strings.IndexByte(sub[p:], '>')
		if end < 0 {
			break
		}
		decl := sub[p : p+end]
		i = p + end + 1
		if name == "" || !strings.Contains(decl, "NDATA") {
			continue
		}
		if _, seen := out[name]; seen {
			continue // first declaration is binding
		}
		var ent UnparsedEntity
		if k := strings.Index(decl, "PUBLIC"); k >= 0 {
			pub, rest, ok := firstQuotedLiteral(decl[k+len("PUBLIC"):])
			if ok {
				ent.PublicID = strings.Join(strings.Fields(pub), " ")
				if sys, _, ok := firstQuotedLiteral(rest); ok {
					ent.SystemID = sys
				}
			}
		} else if k := strings.Index(decl, "SYSTEM"); k >= 0 {
			if sys, _, ok := firstQuotedLiteral(decl[k+len("SYSTEM"):]); ok {
				ent.SystemID = sys
			}
		}
		out[name] = ent
	}
}

func internalEntities(src string) map[string]string {
	return documentEntities(src, "")
}

// documentEntities extracts a document's general entity declarations: the
// DOCTYPE internal subset (if any), plus — when baseDir is non-empty — the
// external subset named by a SYSTEM identifier, resolved against baseDir and
// read from the filesystem (copy-1201/1202: a stylesheet's own DOCTYPE
// referencing a local .dtd file of named character entities, e.g. &egrave;).
// An unreadable/absent external file is silently skipped — the processor does
// not fail a document over an entity set it cannot fetch; any reference to an
// entity it declared just stays unresolved, as it always did before baseDir
// support existed. Internal declarations take precedence over external ones
// (scanEntityDecls never overrides an already-seen name), matching the XML
// rule that the internal subset wins over the external one.
func documentEntities(src, baseDir string) map[string]string {
	ents := map[string]string{}
	if sub, ok := internalSubset(src); ok {
		scanEntityDecls(expandParamEntities(sub, baseDir), ents)
	}
	if baseDir != "" {
		if sysID, ok := externalDTDSystemID(src); ok {
			path := sysID
			if !filepath.IsAbs(path) {
				path = filepath.Join(baseDir, path)
			}
			if data, err := os.ReadFile(path); err == nil {
				scanEntityDecls(string(data), ents)
			}
		}
	}
	if len(ents) == 0 {
		return nil
	}
	expandEntityValues(ents)
	// encoding/xml inserts a Decoder.Entity value verbatim, without rescanning it
	// for references, so character references and the five predefined entities
	// have to be resolved here — exactly once, and only after general-entity
	// expansion has reached its fixpoint, so that "&amp;#37;" stays "&#37;".
	for name, val := range ents {
		ents[name] = expandBuiltinRefs(val)
	}
	return ents
}

// scanEntityDecls scans text for <!ENTITY name "value"> declarations (skipping
// parameter entities and ones with an external id) and records each
// first-seen name into ents without overriding an entry already present —
// letting a caller seed ents with higher-priority declarations first (see
// documentEntities: internal-subset entities must win over external-subset
// ones of the same name).
func scanEntityDecls(text string, ents map[string]string) {
	for i := 0; ; {
		j := strings.Index(text[i:], "<!ENTITY")
		if j < 0 {
			break
		}
		p := i + j + len("<!ENTITY")
		i = p
		p = skipSpace(text, p)
		if p >= len(text) || text[p] == '%' {
			continue // parameter entity
		}
		start := p
		for p < len(text) && !isXMLSpace(text[p]) && text[p] != '>' {
			p++
		}
		name := text[start:p]
		p = skipSpace(text, p)
		if name == "" || p >= len(text) || (text[p] != '"' && text[p] != '\'') {
			continue // external id, or malformed — leave it to the parser
		}
		q := text[p]
		p++
		start = p
		for p < len(text) && text[p] != q {
			p++
		}
		if p >= len(text) {
			break
		}
		if _, seen := ents[name]; !seen {
			ents[name] = text[start:p]
		}
		i = p + 1
	}
}

// externalDTDSystemID extracts the SYSTEM literal from a document's DOCTYPE
// declaration's external ID (<!DOCTYPE root SYSTEM "uri"> or <!DOCTYPE root
// PUBLIC "public-id" "uri">). PUBLIC identifiers are not resolved — only the
// accompanying system literal is used, matching how browsers/processors
// commonly fall back for a PUBLIC id they don't otherwise recognize.
func externalDTDSystemID(src string) (string, bool) {
	d := strings.Index(src, "<!DOCTYPE")
	if d < 0 {
		return "", false
	}
	i := d + len("<!DOCTYPE")
	end := i
	for end < len(src) && src[end] != '[' && src[end] != '>' {
		end++
	}
	decl := src[i:end]
	if j := strings.Index(decl, "SYSTEM"); j >= 0 {
		lit, _, ok := firstQuotedLiteral(decl[j+len("SYSTEM"):])
		return lit, ok
	}
	if j := strings.Index(decl, "PUBLIC"); j >= 0 {
		_, rest, ok := firstQuotedLiteral(decl[j+len("PUBLIC"):])
		if !ok {
			return "", false
		}
		lit, _, ok := firstQuotedLiteral(rest)
		return lit, ok
	}
	return "", false
}

// firstQuotedLiteral returns the content of the first '...'/"..." literal in
// s, plus the remainder of s following its closing quote.
func firstQuotedLiteral(s string) (lit, rest string, ok bool) {
	i := skipSpace(s, 0)
	if i >= len(s) || (s[i] != '"' && s[i] != '\'') {
		return "", "", false
	}
	q := s[i]
	i++
	start := i
	for i < len(s) && s[i] != q {
		i++
	}
	if i >= len(s) {
		return "", "", false
	}
	return s[start:i], s[i+1:], true
}

// expandEntityValues resolves entity references inside replacement text. The
// pass count bounds the recursion depth, so a circular declaration leaves the
// unresolved reference in place instead of looping.
func expandEntityValues(ents map[string]string) {
	for pass := 0; pass < 40; pass++ {
		changed := false
		for name, val := range ents {
			out, hit := substituteEntities(val, ents)
			if hit {
				ents[name] = out
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

// expandBuiltinRefs resolves character references and the five predefined
// entities in text, leaving anything else untouched.
func expandBuiltinRefs(s string) string {
	predef := map[string]string{"lt": "<", "gt": ">", "amp": "&", "apos": "'", "quot": `"`}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		name := s[i+1 : i+end]
		switch {
		case strings.HasPrefix(name, "#x"), strings.HasPrefix(name, "#X"):
			if r, err := strconv.ParseInt(name[2:], 16, 32); err == nil && r > 0 {
				b.WriteRune(rune(r))
			} else {
				b.WriteString(s[i : i+end+1])
			}
		case strings.HasPrefix(name, "#"):
			if r, err := strconv.ParseInt(name[1:], 10, 32); err == nil && r > 0 {
				b.WriteRune(rune(r))
			} else {
				b.WriteString(s[i : i+end+1])
			}
		default:
			if rep, ok := predef[name]; ok {
				b.WriteString(rep)
			} else {
				b.WriteString(s[i : i+end+1])
			}
		}
		i += end + 1
	}
	return b.String()
}

func substituteEntities(s string, ents map[string]string) (string, bool) {
	if !strings.Contains(s, "&") {
		return s, false
	}
	var b strings.Builder
	hit := false
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		name := s[i+1 : i+end]
		if rep, ok := ents[name]; ok {
			b.WriteString(rep)
			hit = true
		} else {
			b.WriteString(s[i : i+end+1])
		}
		i += end + 1
	}
	return b.String(), hit
}

// internalSubset returns the text between the '[' and matching ']' of a DOCTYPE
// declaration, skipping over comments and quoted strings so a ']' inside either
// does not end the subset early.
func internalSubset(src string) (string, bool) {
	d := strings.Index(src, "<!DOCTYPE")
	if d < 0 {
		return "", false
	}
	open := strings.IndexByte(src[d:], '[')
	if open < 0 {
		return "", false
	}
	i := d + open + 1
	start := i
	for i < len(src) {
		switch {
		case strings.HasPrefix(src[i:], "<!--"):
			e := strings.Index(src[i:], "-->")
			if e < 0 {
				return "", false
			}
			i += e + 3
		case src[i] == '"' || src[i] == '\'':
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				i++
			}
			i++
		case src[i] == ']':
			return src[start:i], true
		default:
			i++
		}
	}
	return "", false
}

func skipSpace(s string, i int) int {
	for i < len(s) && isXMLSpace(s[i]) {
		i++
	}
	return i
}

func isXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// externalGeneralEntityDecls returns the EXTERNAL parsed general entities a
// document declares (name -> the file their replacement text lives in),
// resolved against the subset they were declared in. NDATA (unparsed)
// entities are excluded — they are never expanded — as are parameter
// entities and internal ones (handled by documentEntities).
func externalGeneralEntityDecls(src, baseDir string) map[string]string {
	if baseDir == "" {
		return nil
	}
	out := map[string]string{}
	if sub, ok := internalSubset(src); ok {
		scanExternalEntityDecls(sub, baseDir, out)
	}
	if sysID, ok := externalDTDSystemID(src); ok {
		path := sysID
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if data, err := os.ReadFile(path); err == nil {
			scanExternalEntityDecls(string(data), filepath.Dir(path), out)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func scanExternalEntityDecls(text, baseDir string, out map[string]string) {
	for i := 0; ; {
		j := strings.Index(text[i:], "<!ENTITY")
		if j < 0 {
			return
		}
		p := i + j + len("<!ENTITY")
		i = p
		p = skipSpace(text, p)
		if p >= len(text) || text[p] == '%' {
			continue // parameter entity
		}
		start := p
		for p < len(text) && !isXMLSpace(text[p]) && text[p] != '>' {
			p++
		}
		name := text[start:p]
		end := strings.IndexByte(text[p:], '>')
		if end < 0 {
			return
		}
		decl := text[p : p+end]
		i = p + end + 1
		if name == "" || strings.Contains(decl, "NDATA") {
			continue
		}
		if _, seen := out[name]; seen {
			continue // first declaration binds
		}
		sys := ""
		if k := strings.Index(decl, "PUBLIC"); k >= 0 {
			if _, rest, ok := firstQuotedLiteral(decl[k+len("PUBLIC"):]); ok {
				sys, _, _ = firstQuotedLiteral(rest)
			}
		} else if k := strings.Index(decl, "SYSTEM"); k >= 0 {
			sys, _, _ = firstQuotedLiteral(decl[k+len("SYSTEM"):])
		}
		if sys == "" {
			continue
		}
		if !filepath.IsAbs(sys) {
			sys = filepath.Join(baseDir, sys)
		}
		// The recorded path becomes the entity's BASE URI ("file://"+path —
		// see applyEntityBases), so it has to be absolute: a relative baseDir
		// (the conformance harness passes one) otherwise produced a nonsense
		// "file://../../…" that no relative xml:base could resolve against
		// (base-uri-051).
		if abs, err := filepath.Abs(sys); err == nil {
			sys = abs
		}
		out[name] = sys
	}
}

// expandExternalEntities textually substitutes references to external parsed
// general entities in a document's BODY (everything after the DOCTYPE
// declaration). Go's encoding/xml can only supply an entity's replacement as
// character data, so an external entity whose content is markup — the normal
// case — has to be spliced into the source before tokenizing. Each
// replacement's own text declaration is stripped, and expansion repeats so an
// entity referencing another one resolves too.
func expandExternalEntities(src, baseDir string) (string, []entitySpan) {
	paths := externalGeneralEntityDecls(src, baseDir)
	reps := map[string]string{}
	for name, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		reps[name] = stripTextDecl(string(b))
	}
	// An INTERNAL general entity whose replacement text is markup gets the
	// same textual splice, for the same reason — the decoder would hand
	// "<t:text>" back as character data (whitespace-011's &br;). Its literal
	// is spliced RAW: a general-entity reference inside an EntityValue is
	// bypassed at declaration time (XML §4.4.7) and only resolved where the
	// entity is included, so the &quot; in streamable-137's macro must reach
	// the decoder as a reference, not as a bare quote that would end the
	// attribute early. Plain-text entities keep going through Decoder.Entity
	// (whose values ARE pre-expanded, as verbatim substitution needs).
	if sub, ok := internalSubset(src); ok {
		raw := map[string]string{}
		scanEntityDecls(expandParamEntities(sub, baseDir), raw)
		for name, val := range raw {
			if _, ok := reps[name]; ok {
				continue
			}
			if strings.Contains(val, "<") {
				reps[name] = val
			}
		}
	}
	if len(reps) == 0 {
		return src, nil
	}
	head := doctypeEnd(src)
	var spans []entitySpan
	body := spliceEntities(src[head:], reps, paths, head, 0, &spans)
	return src[:head] + body, spans
}

// spliceEntities replaces every reference to a known external entity in text
// with that entity's replacement, recursively, recording where each
// replacement landed. offset is text's absolute position in the document being
// built, so the recorded spans are absolute. A nested entity's span is
// appended BEFORE its container's, which makes the first matching span the
// innermost one.
func spliceEntities(text string, reps, paths map[string]string, offset, depth int, spans *[]entitySpan) string {
	if depth > 8 || !strings.Contains(text, "&") {
		return text
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.IndexByte(text[i:], '&')
		if j < 0 {
			b.WriteString(text[i:])
			break
		}
		j += i
		k := strings.IndexByte(text[j:], ';')
		if k < 0 {
			b.WriteString(text[i:])
			break
		}
		name := text[j+1 : j+k]
		rep, ok := reps[name]
		if !ok {
			b.WriteString(text[i : j+k+1])
			i = j + k + 1
			continue
		}
		b.WriteString(text[i:j])
		begin := offset + b.Len()
		b.WriteString(spliceEntities(rep, reps, paths, begin, depth+1, spans))
		if path := paths[name]; path != "" {
			// Only an EXTERNAL entity's text carries its own base URI.
			*spans = append(*spans, entitySpan{start: begin, end: offset + b.Len(), path: path})
		}
		i = j + k + 1
	}
	return b.String()
}

// entitySpan records where one external entity's replacement text landed in
// the expanded source, so the nodes parsed out of it can be given the
// entity's own base URI.
type entitySpan struct {
	start, end int
	path       string
}

// doctypeEnd returns the index just past a document's DOCTYPE declaration
// (0 when there is none), so entity expansion never touches the DTD itself.
func doctypeEnd(src string) int {
	d := strings.Index(src, "<!DOCTYPE")
	if d < 0 {
		return 0
	}
	i := d + len("<!DOCTYPE")
	for i < len(src) {
		switch {
		case src[i] == '[':
			// Skip the internal subset, then the closing '>'.
			if sub, ok := internalSubset(src); ok {
				i = strings.Index(src, sub) + len(sub)
			}
			for i < len(src) && src[i] != '>' {
				i++
			}
			return i + 1
		case src[i] == '"' || src[i] == '\'':
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				i++
			}
			i++
		case src[i] == '>':
			return i + 1
		default:
			i++
		}
	}
	return 0
}

// stripTextDecl removes a leading XML/text declaration from an external
// entity's replacement text (an external parsed entity may begin with one,
// and it is not part of the replacement).
func stripTextDecl(s string) string {
	t := strings.TrimLeft(s, "\uFEFF \t\r\n")
	if !strings.HasPrefix(t, "<?xml") {
		return s
	}
	if end := strings.Index(t, "?>"); end >= 0 {
		return t[end+2:]
	}
	return s
}

// expandParamEntities resolves parameter-entity references (%name;) inside a
// DTD subset: an internal parameter entity contributes its literal value, an
// external one the contents of the file it names (resolved against baseDir).
// This is what makes declarations living in a separate .dtd reachable from the
// internal subset (whitespace-011: "<!ENTITY % ext SYSTEM 'entity.ent'>%ext;").
func expandParamEntities(sub, baseDir string) string {
	if !strings.Contains(sub, "%") {
		return sub
	}
	vals := map[string]string{}
	for i := 0; ; {
		j := strings.Index(sub[i:], "<!ENTITY")
		if j < 0 {
			break
		}
		p := i + j + len("<!ENTITY")
		i = p
		p = skipSpace(sub, p)
		if p >= len(sub) || sub[p] != '%' {
			continue // a general entity
		}
		p = skipSpace(sub, p+1)
		start := p
		for p < len(sub) && !isXMLSpace(sub[p]) && sub[p] != '>' {
			p++
		}
		name := sub[start:p]
		end := strings.IndexByte(sub[p:], '>')
		if end < 0 {
			break
		}
		decl := sub[p : p+end]
		i = p + end + 1
		if name == "" {
			continue
		}
		if lit, _, ok := firstQuotedLiteral(decl); ok && !strings.Contains(decl, "SYSTEM") && !strings.Contains(decl, "PUBLIC") {
			vals[name] = lit
			continue
		}
		if baseDir == "" {
			continue
		}
		sys := ""
		if k := strings.Index(decl, "PUBLIC"); k >= 0 {
			if _, rest, ok := firstQuotedLiteral(decl[k+len("PUBLIC"):]); ok {
				sys, _, _ = firstQuotedLiteral(rest)
			}
		} else if k := strings.Index(decl, "SYSTEM"); k >= 0 {
			sys, _, _ = firstQuotedLiteral(decl[k+len("SYSTEM"):])
		}
		if sys == "" {
			continue
		}
		path := sys
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		if data, err := os.ReadFile(path); err == nil {
			vals[name] = stripTextDecl(string(data))
		}
	}
	if len(vals) == 0 {
		return sub
	}
	for pass := 0; pass < 4; pass++ {
		changed := false
		for name, val := range vals {
			ref := "%" + name + ";"
			if strings.Contains(sub, ref) {
				sub = strings.ReplaceAll(sub, ref, val)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return sub
}
