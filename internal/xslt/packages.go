package xslt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tim-riep/go-xslt/internal/xmltree"
)

// ---------------------------------------------------------------------------
// xsl:use-package — library packages (XSLT 3.0 §3.5)
//
// A library package is a separately-written xsl:package module that a using
// package/stylesheet names (rather than locates by href, as xsl:import does).
// The host supplies the set of packages that are AVAILABLE, each with its own
// name URI and package-version; xsl:use-package selects one of them by name
// plus a package-version-ranges expression.
//
// This processor implements the "flattened" model: a used package's modules
// are spliced into the module list at LOWER import precedence than the using
// module, exactly as xsl:import does, and an xsl:override child's declarations
// join the USING module at its own precedence. That reproduces the observable
// semantics the spec prescribes for the features tested here — an overriding
// template rule beats the overridden one, xsl:next-match/xsl:apply-imports
// walk from the overriding rule down through the used package's rules, and a
// component declared in both packages resolves to the using package's — while
// leaving the one aspect this model cannot express (visibility hiding a
// PRIVATE component of a used package from the using package) unenforced.
// ---------------------------------------------------------------------------

// PackageSource describes one library package the host makes available to
// xsl:use-package. Name/Version may be left empty, in which case they are read
// from the package module's own xsl:package/@name and @package-version.
type PackageSource struct {
	Name    string // package name URI
	Version string // package-version ("" = take from the module, default "1.0")
	Path    string // file path of the xsl:package module
}

// pkgVersion is a parsed package-version: a sequence of decimal parts plus an
// optional name-part suffix (everything after the first "-", kept whole
// because ordering between suffixes is a plain lexical comparison).
type pkgVersion struct {
	parts  []int
	suffix string // "" = a release version, which ranks ABOVE any suffixed one
}

// parsePkgVersion parses the PackageVersion production:
//
//	PackageVersion ::= (DecimalPart ".")* DecimalPart NamePart*
//	DecimalPart    ::= [0-9]+
//	NamePart       ::= "-" NCName
//
// The suffix is kept as one string ("arable-environment.27" rather than the
// two NameParts it lexically is) since comparison between two suffixes is
// lexical either way; each hyphen-separated piece is still validated as an
// NCName, which is what rejects "2.0.0-alpha:beta" (use-package-291).
func parsePkgVersion(s string) (pkgVersion, bool) {
	var v pkgVersion
	num, suffix := s, ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		num, suffix = s[:i], s[i+1:]
	}
	if num == "" {
		return v, false
	}
	for _, part := range strings.Split(num, ".") {
		if part == "" {
			return v, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return v, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return v, false
		}
		v.parts = append(v.parts, n)
	}
	if suffix != "" {
		for _, piece := range strings.Split(suffix, "-") {
			if !reXMLNCName.MatchString(piece) {
				return v, false
			}
		}
	}
	v.suffix = suffix
	return v, true
}

// comparePkgVersion orders two package versions. Decimal parts compare
// numerically left to right with a missing part treated as 0 (so "2.0" and
// "2.0.0" are the SAME version — package-version-102, use-package-212); when
// those are equal, a version with no name-part suffix ranks above one with a
// suffix (a release beats its pre-releases), and two suffixed versions compare
// lexically by suffix ("alpha" < "arable-environment.27" < "beta" < "gamma" —
// use-package-209/210).
func comparePkgVersion(a, b pkgVersion) int {
	n := len(a.parts)
	if len(b.parts) > n {
		n = len(b.parts)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(a.parts) {
			av = a.parts[i]
		}
		if i < len(b.parts) {
			bv = b.parts[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.suffix == b.suffix:
		return 0
	case a.suffix == "":
		return 1
	case b.suffix == "":
		return -1
	case a.suffix < b.suffix:
		return -1
	}
	return 1
}

// pkgRange is one alternative of a package-version-ranges list.
type pkgRange struct {
	any    bool        // "*"
	prefix []int       // "3.5.*" — match any version starting with these parts
	exact  *pkgVersion // a bare PackageVersion
	lo     *pkgVersion // "X to Y", "X+", "to Y" (nil = unbounded)
	hi     *pkgVersion
	ranged bool
}

// parsePkgRanges parses the package-version-ranges production:
//
//	PackageVersionRanges ::= PackageVersionRange ("," PackageVersionRange)*
//	PackageVersionRange  ::= "*" | PackageVersion | PackageVersion ".*"
//	                       | PackageVersion "to" PackageVersion
//	                       | "to" PackageVersion | PackageVersion "+"
//
// An unparseable range is a static error (XTSE0020) — use-package-291..294.
func parsePkgRanges(s string) ([]pkgRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("err:XTSE0020: package-version is empty")
	}
	var out []pkgRange
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		r, err := parsePkgRange(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func parsePkgRange(tok string) (pkgRange, error) {
	var r pkgRange
	bad := func() (pkgRange, error) {
		return r, fmt.Errorf("err:XTSE0020: %q is not a valid package-version range", tok)
	}
	if tok == "" {
		return bad()
	}
	if tok == "*" {
		r.any = true
		return r, nil
	}
	// "X to Y" / "to Y" — "to" is a whitespace-delimited keyword.
	fields := strings.Fields(tok)
	for i, f := range fields {
		if f != "to" {
			continue
		}
		r.ranged = true
		lo := strings.Join(fields[:i], "")
		hi := strings.Join(fields[i+1:], "")
		if lo != "" {
			v, ok := parsePkgVersion(lo)
			if !ok {
				return bad()
			}
			r.lo = &v
		}
		if hi != "" {
			v, ok := parsePkgVersion(hi)
			if !ok {
				return bad()
			}
			r.hi = &v
		}
		if r.lo == nil && r.hi == nil {
			return bad()
		}
		return r, nil
	}
	if len(fields) != 1 {
		return bad()
	}
	tok = fields[0]
	// "X+" — that version or any higher one.
	if strings.HasSuffix(tok, "+") {
		v, ok := parsePkgVersion(strings.TrimSuffix(tok, "+"))
		if !ok {
			return bad()
		}
		r.ranged, r.lo = true, &v
		return r, nil
	}
	// "3.5.*" — any version whose leading decimal parts are 3, 5.
	if strings.HasSuffix(tok, ".*") {
		head := strings.TrimSuffix(tok, ".*")
		v, ok := parsePkgVersion(head)
		if !ok || v.suffix != "" {
			return bad()
		}
		r.prefix = v.parts
		return r, nil
	}
	v, ok := parsePkgVersion(tok)
	if !ok {
		return bad()
	}
	r.exact = &v
	return r, nil
}

// matches reports whether version v satisfies this range.
func (r pkgRange) matches(v pkgVersion) bool {
	switch {
	case r.any:
		return true
	case r.prefix != nil:
		if len(v.parts) < len(r.prefix) {
			return false
		}
		for i, p := range r.prefix {
			if v.parts[i] != p {
				return false
			}
		}
		return true
	case r.exact != nil:
		return comparePkgVersion(v, *r.exact) == 0
	case r.ranged:
		if r.lo != nil && comparePkgVersion(v, *r.lo) < 0 {
			return false
		}
		if r.hi != nil && comparePkgVersion(v, *r.hi) > 0 {
			return false
		}
		return true
	}
	return false
}

// matchesAnyPkgRange reports whether v satisfies at least one alternative.
func matchesAnyPkgRange(rs []pkgRange, v pkgVersion) bool {
	for _, r := range rs {
		if r.matches(v) {
			return true
		}
	}
	return false
}

// defaultPackageVersion is the version an xsl:package that declares none is
// taken to have (XSLT 3.0 §3.5.2).
const defaultPackageVersion = "1.0"

// pkgIdentity reads a package module's own xsl:package/@name and
// @package-version, for a host registration that gave only the file. Parsing
// is cheap relative to the compile that follows and the result is cached.
func pkgIdentity(path string) (name, version string, ok bool) {
	pkgIdentityMu.Lock()
	if id, hit := pkgIdentityCache[path]; hit {
		pkgIdentityMu.Unlock()
		return id.name, id.version, id.ok
	}
	pkgIdentityMu.Unlock()
	name, version, ok = readPkgIdentity(path)
	pkgIdentityMu.Lock()
	pkgIdentityCache[path] = pkgIdent{name, version, ok}
	pkgIdentityMu.Unlock()
	return name, version, ok
}

type pkgIdent struct {
	name, version string
	ok            bool
}

var (
	pkgIdentityMu    sync.Mutex
	pkgIdentityCache = map[string]pkgIdent{}
)

func readPkgIdentity(path string) (string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	doc, err := xmltree.ParseLenient11WithBase(string(data), filepath.Dir(path))
	if err != nil {
		return "", "", false
	}
	root := xmltree.RootElement(doc)
	if root == nil || root.Name.Space != NS || root.Name.Local != "package" {
		return "", "", false
	}
	name, _ := root.AttrLocal("name")
	ver, _ := root.AttrLocal("package-version")
	return strings.TrimSpace(name), strings.TrimSpace(ver), true
}

// resolvePackage picks the library package named name whose version satisfies
// verSpec. Where several candidates qualify, the HIGHEST version wins — this
// processor's declared package-version-resolution policy. Failing to locate
// any package is XTSE3000.
func (c *compiler) resolvePackage(name, verSpec string) (PackageSource, error) {
	return resolvePackageIn(c.packages, name, verSpec)
}

// resolvePackageIn is resolvePackage over an explicit registry, for callers
// that have no compiler (fn:transform's package-based invocation).
func resolvePackageIn(registry []PackageSource, name, verSpec string) (PackageSource, error) {
	ranges, err := parsePkgRanges(verSpec)
	if err != nil {
		return PackageSource{}, err
	}
	type cand struct {
		src PackageSource
		ver pkgVersion
	}
	var cands []cand
	for _, p := range registry {
		if p.Name == "" || p.Version == "" {
			// The host named only the FILE (the catalog's <package file="..."/>
			// form): the package's identity is whatever its own xsl:package
			// element declares.
			if n, v, ok := pkgIdentity(p.Path); ok {
				if p.Name == "" {
					p.Name = n
				}
				if p.Version == "" {
					p.Version = v
				}
			}
		}
		if p.Name != name {
			continue
		}
		vs := p.Version
		if strings.TrimSpace(vs) == "" {
			vs = defaultPackageVersion
		}
		v, ok := parsePkgVersion(strings.TrimSpace(vs))
		if !ok {
			// A package whose OWN version is unparseable is a static error in
			// that package, not here; skip it as a candidate.
			continue
		}
		if matchesAnyPkgRange(ranges, v) {
			cands = append(cands, cand{p, v})
		}
	}
	if len(cands) == 0 {
		return PackageSource{}, fmt.Errorf("err:XTSE3000: no package named %q with version matching %q is available", name, verSpec)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return comparePkgVersion(cands[i].ver, cands[j].ver) < 0
	})
	return cands[len(cands)-1].src, nil
}
