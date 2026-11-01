package engine

import (
	"github.com/tim-riep/go-xslt/internal/fileuri"
	"path/filepath"
	"strings"

	"github.com/tim-riep/go-xslt/internal/fsaccess"
	"github.com/tim-riep/go-xslt/internal/netfetch"
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
	// net retrieves http(s) URIs; nil means no network access.
	net *netfetch.Fetcher
	// noDisk is set when there is no BaseDir: then no local file is read.
	noDisk bool
	// files checks every local file read; nil checks nothing.
	files *fsaccess.Guard
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
	uri := fileuri.FromPath(abs)
	if !strings.HasSuffix(uri, "/") {
		uri += "/"
	}
	return uri
}

// sourceURIOf turns Request.SourceURI into the URI of the source document: a
// string with a URI scheme of two or more letters ("file:", "https:") is taken
// as it is, anything else is a file path and becomes an absolute "file:" URI
// the same way BaseDir does (a single letter before the colon is a Windows
// drive, not a scheme). Empty stays empty.
func sourceURIOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, ':'); i >= 2 {
		isScheme := s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z'
		for _, c := range s[:i] {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
				isScheme = false
			}
		}
		if isScheme {
			return s
		}
	}
	abs, err := filepath.Abs(s)
	if err != nil {
		abs = s
	}
	return fileuri.FromPath(abs)
}

func (r *standaloneResolver) path(uri string) string {
	u := strings.TrimSpace(uri)
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if strings.HasPrefix(u, "file:") {
		if p, ok := fileuri.ToPath(u); ok {
			u = p
		}
	}
	if filepath.IsAbs(u) {
		return u
	}
	return filepath.Join(r.baseDir, u)
}

func (r *standaloneResolver) ResolveText(uri string) (string, bool) {
	if netfetch.IsNetworkURI(uri) {
		resp, err := r.net.Fetch(uri)
		if err != nil {
			return "", false
		}
		return xmltree.DecodeRetrievedTextAs(resp.Body, resp.Charset), true
	}
	if r.noDisk {
		return "", false
	}
	b, err := r.files.ReadFile(r.path(uri))
	if err != nil {
		return "", false
	}
	return xmltree.DecodeRetrievedText(b), true
}

func (r *standaloneResolver) ResolveDoc(uri string) (*xmltree.Node, bool) {
	if netfetch.IsNetworkURI(uri) {
		resp, err := r.net.Fetch(uri)
		if err != nil {
			return nil, false
		}
		// No base directory: a remote DOCTYPE never reads a local file.
		doc, err := xmltree.ParseLenient11WithBase(string(resp.Body), "")
		if err != nil {
			return nil, false
		}
		doc.SetBase(strings.TrimSpace(uri))
		return doc, true
	}
	if r.noDisk {
		return nil, false
	}
	p := r.path(uri)
	b, err := r.files.ReadFile(p)
	if err != nil {
		return nil, false
	}
	doc, err := xmltree.ParseLenient11WithBaseFS(string(b), filepath.Dir(p), r.files)
	if err != nil {
		return nil, false
	}
	doc.SetBase(fileuri.FromPath(p))
	return doc, true
}

// ResolveCollection supports the "dir?select=pattern" glob convention (the
// same one internal/xslt's resolver honors): the documents in dir whose
// names match pattern, in name order. Any other collection URI is
// unavailable — there is no host-supplied collection binding in the
// standalone playground.
func (r *standaloneResolver) ResolveCollection(uri string) ([]*xmltree.Node, bool) {
	if r.noDisk {
		return nil, false
	}
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
	entries, err := r.files.ReadDir(dir)
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
