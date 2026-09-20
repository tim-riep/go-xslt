package xslt

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// fileResolver implements xpath.ResourceResolver against the local filesystem,
// resolving relative URIs (from fn:doc / fn:unparsed-text / document()) against
// the run's base directory. Parsed documents are cached so repeated doc() calls
// share node identity (as fn:doc requires for stable document order).
type fileResolver struct {
	baseDir string
	docs    map[string]*xmltree.Node
	ss      *Stylesheet // for xsl:strip-space, applied to every resolved document too (document-1502)
	// collections holds the host-supplied fn:collection() bindings, keyed by
	// resolved absolute path ("" = the default collection).
	collections map[string][]string
	// docVal holds the host's per-document schema-validation requests (see
	// Entry.DocValidation), keyed by resolved absolute path; valSchemas are
	// the schema documents to validate them against.
	docVal     map[string]string
	valSchemas []string
}

func newFileResolver(baseDir string, ss *Stylesheet) *fileResolver {
	// Normalized to an absolute path up front so it always agrees with the
	// module Base CompileFrom computes (also via filepath.Abs) from the SAME
	// baseDir string: a relative href reaching path() via two different
	// routes — already resolved against ctx.BaseURI (module Base, absolute)
	// vs falling through to this baseDir unresolved (no static base in
	// scope, e.g. the Context a match pattern evaluates against) — must land
	// on the IDENTICAL cache key either way, or the two routes parse and
	// cache the SAME file as two DIFFERENT node objects, breaking the node
	// identity fn:doc/document() guarantees (match-052/070/071: a pattern
	// like match="doc('x.xml')" compares the pattern's own doc() call by
	// pointer against the candidate node).
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return &fileResolver{baseDir: baseDir, docs: map[string]*xmltree.Node{}, ss: ss}
}

// setDocValidation records the host's per-document validation requests (see
// Entry.DocValidation), keyed by the RESOLVED path so a request written as a
// relative href still matches however the stylesheet spells the same URI.
func (r *fileResolver) setDocValidation(byHref map[string]string, schemas []string) {
	if len(byHref) == 0 {
		return
	}
	r.docVal = make(map[string]string, len(byHref))
	for href, mode := range byHref {
		r.docVal[r.path(href)] = mode
	}
	r.valSchemas = schemas
}

// path maps a (possibly relative, possibly file://) URI to a filesystem path.
func (r *fileResolver) path(uri string) string {
	u := strings.TrimSpace(uri)
	// A fragment identifier is stripped before determining document identity
	// (document-1503): it addresses a part of the retrieved resource, not a
	// different resource, and it is never a real filesystem path component.
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if strings.HasPrefix(u, "file:") {
		if pu, err := url.Parse(u); err == nil && pu.Path != "" {
			u = pu.Path
		}
	}
	if filepath.IsAbs(u) {
		return u
	}
	return filepath.Join(r.baseDir, u)
}

func (r *fileResolver) ResolveText(uri string) (string, bool) {
	b, err := os.ReadFile(r.path(uri))
	if err != nil {
		return "", false
	}
	// A byte-order mark identifies the resource's encoding and is not part of
	// its text (unparsed-text-2001: UTF-16 files written with byte-order-mark
	// ="yes" must read back as the same characters as their UTF-8 twins).
	return xmltree.DecodeRetrievedText(b), true
}

// WasRead reports whether uri (resolved the same way fn:doc/document() would)
// has already been read via fn:doc/document() during this run — used to
// detect a read/write conflict on xsl:result-document (err:XTDE1500).
func (r *fileResolver) WasRead(uri string) bool {
	_, ok := r.docs[r.path(uri)]
	return ok
}

func (r *fileResolver) ResolveDoc(uri string) (*xmltree.Node, bool) {
	p := r.path(uri)
	if d, ok := r.docs[p]; ok {
		return d, true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	// Parsed with its OWN directory as the base, so a DOCTYPE naming an
	// external subset (a local .dtd of entity declarations) resolves relative
	// to the retrieved document, not to the stylesheet (catalog-004).
	doc, err := xmltree.ParseLenient11WithBase(string(b), filepath.Dir(p))
	if err != nil {
		return nil, false
	}
	// xsl:strip-space is a whitespace-stripping rule for every tree the
	// stylesheet processes, not just the principal source document — a
	// document() / fn:doc() result is subject to it exactly like the primary
	// input (document-1502).
	// Validation first, whitespace stripping second — the same order (and for
	// the same reasons) as the primary source document, whose own comment in
	// transformEntryInto spells them out.
	if r.ss != nil {
		if mode := r.docVal[p]; mode != "" {
			if verr := r.ss.validateSourceDocument(doc, mode, r.valSchemas); verr != nil {
				// A document the host said was valid but is not is a real
				// error, and fn:doc has no way to report one from here; the
				// resource is reported unavailable instead, which is what
				// every other failure in this function does.
				return nil, false
			}
		}
		r.ss.applyStripSpace(doc)
	}
	// This document's own retrieval location becomes its intrinsic base-uri
	// (fn:base-uri of any node in it, absent a closer xml:base override) AND
	// its document-uri (a genuinely retrieved resource, unlike a constructed
	// temporary tree — see xmltree.Node.Ephemeral).
	doc.Base = fileURI(p)
	r.docs[p] = doc
	return doc, true
}

// streamResolver is an OPTIONAL capability a resource resolver may implement
// (mirroring xpath.CollectionResolver): hand back a document's raw bytes as a
// stream instead of a parsed tree, so xsl:source-document streamable="yes" can
// consume it incrementally. A resolver that does not implement it simply never
// streams — every caller falls back to ResolveDoc.
type streamResolver interface {
	ResolveStream(uri string) (io.ReadCloser, bool)
}

// ResolveStream opens uri for incremental reading.
//
// It deliberately bypasses r.docs. That cache exists to give fn:doc/document()
// the stable node identity the spec requires from repeated calls on one URI —
// but a streamed document is read once, destructively, and is missing most of
// its nodes at any given moment, so caching it would hand a later fn:doc call
// a hollow tree with the identity of a real one. A stylesheet that wants both
// must read the document twice, which is exactly what it is asking for.
func (r *fileResolver) ResolveStream(uri string) (io.ReadCloser, bool) {
	f, err := os.Open(r.path(uri))
	if err != nil {
		return nil, false
	}
	return f, true
}

// globCollection resolves a directory collection URI of the widely used
// "dir?select=pattern" form (a Saxon convention the suite itself relies on —
// merge-097's uri-collection($dir || '?select=merge-097-*.xml')): the
// documents in dir whose file names match the glob, in name order. Any other
// query parameter (recurse=, on-error=, …) is ignored. Not a collection this
// host knows about otherwise.
func (r *fileResolver) globCollection(uri string) ([]*xmltree.Node, bool) {
	q := strings.IndexByte(uri, '?')
	if q < 0 {
		return nil, false
	}
	pattern := ""
	for _, kv := range strings.FieldsFunc(uri[q+1:], func(c rune) bool { return c == ';' || c == '&' }) {
		if v, ok := strings.CutPrefix(kv, "select="); ok {
			pattern = v
		}
	}
	if pattern == "" {
		return nil, false
	}
	dir := r.path(uri[:q])
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var out []*xmltree.Node
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(pattern, e.Name()); !ok {
			continue
		}
		if doc, ok := r.ResolveDoc(filepath.Join(dir, e.Name())); ok {
			out = append(out, doc)
		}
	}
	return out, true
}

// setCollections records the host's fn:collection() bindings, keyed by the
// same absolute path form ResolveDoc uses so a base-resolved lookup URI finds
// them. The default collection keeps the empty key.
func (r *fileResolver) setCollections(m map[string][]string) {
	if len(m) == 0 {
		return
	}
	r.collections = map[string][]string{}
	for uri, hrefs := range m {
		key := ""
		if uri != "" {
			key = r.path(uri)
		}
		r.collections[key] = hrefs
	}
}

// ResolveCollection implements xpath.CollectionResolver: each href in the
// named collection is retrieved (and stripped) exactly like a document()
// result, and a fragment identifier selects the element it identifies inside
// that document rather than the document node itself (collection-004).
func (r *fileResolver) ResolveCollection(uri string) ([]*xmltree.Node, bool) {
	key := uri
	if key != "" {
		key = r.path(uri)
	}
	hrefs, ok := r.collections[key]
	if !ok {
		return r.globCollection(uri)
	}
	out := make([]*xmltree.Node, 0, len(hrefs))
	for _, href := range hrefs {
		doc, ok := r.ResolveDoc(href)
		if !ok {
			continue
		}
		frag := ""
		if i := strings.IndexByte(href, '#'); i >= 0 {
			frag = href[i+1:]
		}
		if frag == "" {
			out = append(out, doc)
			continue
		}
		if el := elementByID(doc, frag); el != nil {
			out = append(out, el)
		}
	}
	return out, true
}

// elementByID finds the element identified by an ID within doc: an xml:id
// attribute, or a DTD ID-typed attribute, whose value is id.
func elementByID(n *xmltree.Node, id string) *xmltree.Node {
	if n.Kind == xmltree.KindElement {
		for _, a := range n.Attrs {
			if a.Value != id {
				continue
			}
			if a.IDKind == xmltree.IDKindID ||
				(a.Name.Local == "id" && a.Name.Space == "http://www.w3.org/XML/1998/namespace") {
				return n
			}
		}
	}
	for _, c := range n.Children {
		if el := elementByID(c, id); el != nil {
			return el
		}
	}
	return nil
}
