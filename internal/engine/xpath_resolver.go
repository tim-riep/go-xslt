package engine

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// standaloneResolver implements xpath.ResourceResolver (+ CollectionResolver)
// for the standalone XPath playground (EvalXPath): fn:doc/fn:unparsed-text/
// fn:collection resolve relative URIs against BaseDir, exactly as the XSLT
// engine's own resolver does (see internal/xslt/resolver.go), but without
// that resolver's stylesheet-aware strip-space/node-identity-cache concerns
// — a bare expression has no stylesheet and evaluates once, so there is
// nothing to cache across calls.
type standaloneResolver struct {
	baseDir string
}

func newStandaloneResolver(baseDir string) *standaloneResolver {
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return &standaloneResolver{baseDir: baseDir}
}

// baseDirURI turns baseDir into the "file:" directory URI used as
// Context.BaseURI, so a relative href a core function resolves via
// resolveAgainstBase(c.BaseURI, href) lands on the same absolute path this
// resolver's own path() would join it to. The trailing slash is required:
// without it, URI reference resolution treats the last path segment as a
// filename to be replaced rather than a directory to resolve within (RFC
// 3986 §5.3) — "file:///a/proj" + "input.xml" would wrongly yield
// "file:///a/input.xml", dropping "proj".
func baseDirURI(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasSuffix(abs, "/") {
		abs += "/"
	}
	return "file://" + abs
}

func (r *standaloneResolver) path(uri string) string {
	u := strings.TrimSpace(uri)
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

func (r *standaloneResolver) ResolveText(uri string) (string, bool) {
	b, err := os.ReadFile(r.path(uri))
	if err != nil {
		return "", false
	}
	return xmltree.DecodeRetrievedText(b), true
}

func (r *standaloneResolver) ResolveDoc(uri string) (*xmltree.Node, bool) {
	p := r.path(uri)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	doc, err := xmltree.ParseLenient11WithBase(string(b), filepath.Dir(p))
	if err != nil {
		return nil, false
	}
	doc.Base = "file://" + filepath.ToSlash(p)
	return doc, true
}

// ResolveCollection supports the "dir?select=pattern" glob convention (the
// same one internal/xslt's resolver honors): the documents in dir whose
// names match pattern, in name order. Any other collection URI is
// unavailable — there is no host-supplied collection binding in the
// standalone playground.
func (r *standaloneResolver) ResolveCollection(uri string) ([]*xmltree.Node, bool) {
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
