// Command licenses is the single source of truth for third-party licenses.
//
// It reads the dependency manifests plus one small hand-kept file and
// generates everything that mentions a license:
//
//	inputs                                       outputs
//	-----------------------------------------    ---------------------------------
//	go list -deps per shipped target             desktop/licenses/go/…, npm/…,
//	  (library ./..., desktop main package,        project/LICENSE   (texts)
//	  darwin/windows/linux × amd64/arm64,        desktop/licenses/licenses_gen.go
//	  with and without -tags production)           (the in-app Licenses list)
//	desktop/frontend/package-lock.json           NOTICE
//	  (production tree) + node_modules           THIRD-PARTY-LICENSES.md
//	desktop/licenses/extra.json                  desktop/LICENSE and the license
//	  (what no manifest knows: the project,        file beside the bundled font
//	  Go stdlib, embedded data, copied code,
//	  the font) and the texts it names
//
// Usage, from anywhere inside the repository:
//
//	go run ./scripts/licenses          regenerate (rewrites desktop/licenses/{go,npm,project})
//	go run ./scripts/licenses -check   change nothing; exit 1 when an output is stale,
//	                                   a shipped dependency has no recognisable license
//	                                   text, or a license is not on the allow-list
//	go run ./scripts/licenses -v       also print the module list found per target
//
// See desktop/licenses/README.md for the design notes.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	projectModule = "github.com/tim-riep/go-xslt"
	licDir        = "desktop/licenses"
	lockfile      = "desktop/frontend/package-lock.json"
)

// generatedDirs are rewritten wholesale (and checked for stale files);
// data/ and fonts/ hold hand-kept texts and are never touched.
var generatedDirs = []string{"go", "npm", "project"}

type component struct {
	Name, Version, License, Kind, URL, Why, Copyright string
	Path                                              string // text file, relative to desktop/licenses
	Text                                              string
	Notice                                            bool     // reproduce the text in full in NOTICE
	Module                                            bool     // a Go module or npm package found in a manifest (not from extra.json)
	Origin                                            []string // where it was found (targets, "package-lock.json", "extra.json")
}

type extraFile struct {
	Doc        []string `json:"//"`
	Components []struct {
		Name, Version, License, Kind, Source, Why, Copyright string
		Text                                                 string   `json:"text"`
		CopyFrom                                             string   `json:"copy_from"`
		Copies                                               []string `json:"copies"`
		Notice                                               bool     `json:"notice"`
	} `json:"components"`
	NpmTexts     map[string]string `json:"npm_texts"`
	NoticeFooter string            `json:"notice_footer"`
}

type generator struct {
	root     string
	verbose  bool
	out      map[string][]byte // repo-relative slash path -> content
	comps    []component
	problems []string
	handKept map[string]bool // licenses-relative paths of hand-kept texts in use
}

func main() {
	check := flag.Bool("check", false, "change nothing; fail when outputs are stale or a license is undocumented")
	verbose := flag.Bool("v", false, "print the Go modules found per target")
	rootFlag := flag.String("root", "", "repository root (default: found from the working directory)")
	flag.Parse()

	root := *rootFlag
	if root == "" {
		var err error
		if root, err = findRoot(); err != nil {
			die(err)
		}
	}
	g := &generator{root: root, verbose: *verbose, out: map[string][]byte{}, handKept: map[string]bool{}}
	if err := g.generate(); err != nil {
		die(err)
	}
	if *check {
		g.compareWithDisk()
	}
	if len(g.problems) > 0 {
		fmt.Fprintf(os.Stderr, "licenses: %d problem(s):\n", len(g.problems))
		for _, p := range g.problems {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		if *check {
			fmt.Fprintln(os.Stderr, "\nFix: run `go run ./scripts/licenses` and commit the result; a new data source or an unlisted license goes into desktop/licenses/extra.json (see desktop/licenses/README.md).")
		}
		os.Exit(1)
	}
	if *check {
		fmt.Printf("licenses: ok (%d components documented, %d files up to date)\n", len(g.comps), len(g.out))
		return
	}
	if err := g.write(); err != nil {
		die(err)
	}
	fmt.Printf("licenses: wrote %d files for %d components\n", len(g.out), len(g.comps))
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "licenses:", err)
	os.Exit(1)
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.HasPrefix(bytes.TrimSpace(b), []byte("module "+projectModule+"\n")) {
			return dir, nil
		}
		if p := filepath.Dir(dir); p != dir {
			dir = p
			continue
		}
		return "", fmt.Errorf("not inside the %s repository", projectModule)
	}
}

func (g *generator) problem(format string, a ...any) {
	g.problems = append(g.problems, fmt.Sprintf(format, a...))
}

func (g *generator) read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(g.root, filepath.FromSlash(rel)))
}

func (g *generator) generate() error {
	var extra extraFile
	b, err := g.read(licDir + "/extra.json")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&extra); err != nil {
		return fmt.Errorf("extra.json: %w", err)
	}
	toolchain, err := g.toolchain()
	if err != nil {
		return err
	}
	g.addExtras(extra, toolchain)
	if err := g.addGo(); err != nil {
		return err
	}
	if err := g.addNpm(extra); err != nil {
		return err
	}
	g.checkOrphans()

	sort.SliceStable(g.comps, func(i, j int) bool {
		a, b := g.comps[i], g.comps[j]
		if ra, rb := kindRank(a.Kind), kindRank(b.Kind); ra != rb {
			return ra < rb
		}
		return a.Name < b.Name
	})
	g.out["NOTICE"] = []byte(renderNotice(g.comps, extra.NoticeFooter))
	g.out["THIRD-PARTY-LICENSES.md"] = []byte(renderThirdParty(g.comps))
	gen, err := renderEntries(g.comps)
	if err != nil {
		return err
	}
	g.out[licDir+"/licenses_gen.go"] = gen
	return nil
}

func kindRank(k string) int {
	switch k {
	case "project":
		return 0
	case "go":
		return 1
	case "npm":
		return 2
	case "font":
		return 3
	}
	return 4
}

// toolchain reads desktop/go.mod's toolchain line ("go1.26.10").
func (g *generator) toolchain() (string, error) {
	b, err := g.read("desktop/go.mod")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "toolchain "); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("desktop/go.mod has no toolchain line")
}

// addText registers a component after reading and identifying its text.
func (g *generator) addText(c component, text []byte) {
	c.Text = string(text)
	if c.Copyright == "" {
		c.Copyright = copyrightLine(c.Text)
	}
	g.comps = append(g.comps, c)
	g.out[licDir+"/"+c.Path] = text
}

func (g *generator) addExtras(extra extraFile, toolchain string) {
	for _, e := range extra.Components {
		c := component{Name: e.Name, Version: e.Version, License: e.License, Kind: e.Kind, URL: e.Source,
			Why: e.Why, Copyright: e.Copyright, Path: e.Text, Notice: e.Notice, Origin: []string{"extra.json"}}
		if c.Version == "@toolchain" {
			c.Version = toolchain
		}
		var text []byte
		var err error
		if e.CopyFrom != "" {
			text, err = g.read(e.CopyFrom)
		} else {
			text, err = g.read(licDir + "/" + e.Text)
			g.handKept[e.Text] = true
		}
		if err != nil {
			g.problem("extra.json: %s: %v", e.Name, err)
			continue
		}
		if id, err := detect(string(text)); err != nil {
			g.problem("extra.json: %s: %v", e.Name, err)
		} else if id != e.License {
			g.problem("extra.json: %s declares %s but its text is %s", e.Name, e.License, id)
		}
		g.addText(c, text)
		for _, cp := range e.Copies {
			g.out[cp] = text
		}
	}
}

// checkOrphans: a hand-kept text nobody names would silently go missing from
// the Licenses view, so it is an error.
func (g *generator) checkOrphans() {
	for _, sub := range []string{"data", "fonts"} {
		base := filepath.Join(g.root, licDir, sub)
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(filepath.Join(g.root, licDir), p)
			rel = filepath.ToSlash(rel)
			if !g.handKept[rel] {
				g.problem("%s/%s is not named by extra.json (a license text that is not shown anywhere); reference or delete it", licDir, rel)
			}
			return nil
		})
	}
}

// ---------------------------------------------------------------- Go modules

type goTarget struct{ goos, goarch string }

var goTargets = func() []goTarget {
	var t []goTarget
	for _, os := range []string{"darwin", "windows", "linux"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t = append(t, goTarget{os, arch})
		}
	}
	return t
}()

type goModule struct{ path, version, dir string }

// goList lists the modules compiled into the packages. -e keeps it working
// where the build itself could not: `go list` does not run cgo or a C
// compiler, so the linux/darwin desktop listing needs neither GTK, pkg-config
// nor Xcode, and a missing frontend/dist (go:embed) is only a reported error.
func goList(dir string, t goTarget, tags string, pkgs ...string) ([]goModule, error) {
	args := []string{"list", "-e", "-deps"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}")
	args = append(args, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cgo := "1"
	if t.goos == "windows" { // the release builds Windows with CGO_ENABLED=0
		cgo = "0"
	}
	cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "CGO_ENABLED="+cgo)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s (%s/%s): %v\n%s", strings.Join(args, " "), t.goos, t.goarch, err, stderr.String())
	}
	var mods []goModule
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 || f[0] == projectModule || strings.HasPrefix(f[0], projectModule+"/") {
			continue
		}
		mods = append(mods, goModule{f[0], f[1], f[2]})
	}
	return mods, nil
}

func (g *generator) addGo() error {
	type found struct {
		mod     goModule
		origins map[string]bool
	}
	all := map[string]*found{}
	targetList := map[string]map[string]bool{}
	// The desktop app is built with -tags production; a plain `go build`
	// (dev, `make build`) is not. Both are listed so the union covers either.
	for _, t := range goTargets {
		label := t.goos + "/" + t.goarch
		targetList[label] = map[string]bool{}
		runs := []struct {
			name, dir, tags string
			pkgs            []string
		}{
			{"library", g.root, "", []string{"./..."}},
			{"desktop", filepath.Join(g.root, "desktop"), "", []string{"."}},
			{"desktop+production", filepath.Join(g.root, "desktop"), "production", []string{"."}},
		}
		for _, r := range runs {
			mods, err := goList(r.dir, t, r.tags, r.pkgs...)
			if err != nil {
				return err
			}
			for _, m := range mods {
				key := m.path + "@" + m.version
				if all[key] == nil {
					all[key] = &found{mod: m, origins: map[string]bool{}}
				}
				all[key].origins[label+" "+r.name] = true
				targetList[label][key] = true
			}
		}
	}
	if g.verbose {
		for _, t := range goTargets {
			label := t.goos + "/" + t.goarch
			var ks []string
			for k := range targetList[label] {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			fmt.Printf("%s: %s\n", label, strings.Join(ks, ", "))
		}
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := all[k]
		m := f.mod
		var origin []string
		for o := range f.origins {
			origin = append(origin, o)
		}
		sort.Strings(origin)
		if m.dir == "" {
			g.problem("%s: module is not in the module cache (go mod download)", k)
			continue
		}
		file, text, err := findLicenseFile(m.dir)
		if err != nil {
			g.problem("go module %s has no license file (looked for LICENSE, LICENSE.md, LICENSE.txt, COPYING): add one upstream or document it in extra.json", k)
			continue
		}
		id, err := detect(string(text))
		if err != nil {
			g.problem("go module %s (%s): %v", k, file, err)
			continue
		}
		if !allowed[id] {
			g.problem("go module %s is %s, which is not on the allow-list", k, id)
			continue
		}
		g.addText(component{Name: m.path, Version: m.version, License: id, Kind: "go",
			URL: "https://pkg.go.dev/" + m.path, Path: "go/" + m.path + "/" + path.Base(file), Origin: origin, Module: true}, text)
	}
	return nil
}

// findLicenseFile picks the module's license: the conventional names in
// order, then any license/licence/copying file.
func findLicenseFile(dir string) (string, []byte, error) {
	for _, n := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING"} {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			return n, b, nil
		}
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		l := strings.ToLower(e.Name())
		if !e.IsDir() && (strings.HasPrefix(l, "licen") || strings.HasPrefix(l, "copying")) {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				return e.Name(), b, nil
			}
		}
	}
	return "", nil, fs.ErrNotExist
}

// ---------------------------------------------------------------------- npm

func (g *generator) addNpm(extra extraFile) error {
	lock, err := g.read(lockfile)
	if err != nil {
		return err
	}
	pkgs, err := parseLockfile(lock)
	if err != nil {
		return err
	}
	count := map[string]int{}
	for _, p := range pkgs {
		count[p.Name]++
	}
	lockDir := path.Dir(lockfile)
	for _, p := range pkgs {
		id := p.Name + "@" + p.Version
		raw, err := g.read(lockDir + "/" + p.Dir + "/package.json")
		if err != nil {
			g.problem("npm %s: %v (run `npm ci` in desktop/frontend first)", id, err)
			continue
		}
		var meta packageMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			g.problem("npm %s: package.json: %v", id, err)
			continue
		}
		if meta.Version != p.Version {
			g.problem("npm %s: node_modules has version %s (run `npm ci`)", id, meta.Version)
			continue
		}
		declared := meta.declaredLicense()
		file, text, err := findLicenseFile(filepath.Join(g.root, lockDir, filepath.FromSlash(p.Dir)))
		origin := []string{"package-lock.json"}
		if err != nil {
			rel, ok := extra.NpmTexts[p.Name]
			if !ok {
				g.problem("npm %s ships no license file: keep the upstream text under desktop/licenses/data/npm/ and name it in extra.json npm_texts", id)
				continue
			}
			if text, err = g.read(licDir + "/" + rel); err != nil {
				g.problem("npm %s: %v", id, err)
				continue
			}
			g.handKept[rel] = true
			origin = append(origin, "text from extra.json npm_texts")
			file = "LICENSE"
		}
		detected, err := detect(string(text))
		if err != nil {
			g.problem("npm %s: %v", id, err)
			continue
		}
		if declared != detected {
			g.problem("npm %s: package.json declares %q but its license text is %s", id, declared, detected)
			continue
		}
		if !allowed[detected] {
			g.problem("npm %s is %s, which is not on the allow-list", id, detected)
			continue
		}
		dir := p.Name
		if count[p.Name] > 1 {
			dir += "@" + p.Version
		}
		g.addText(component{Name: p.Name, Version: p.Version, License: detected, Kind: "npm",
			URL: meta.url(p.Name), Path: "npm/" + dir + "/" + path.Base(file), Origin: origin, Module: true}, text)
	}
	// An override for a package no longer in the tree is dead configuration.
	names := map[string]bool{}
	for _, p := range pkgs {
		names[p.Name] = true
	}
	for n := range extra.NpmTexts {
		if !names[n] {
			g.problem("extra.json npm_texts names %s, which is not in the production tree", n)
		}
	}
	return nil
}

// ------------------------------------------------------------------- output

func renderEntries(comps []component) ([]byte, error) {
	var b strings.Builder
	b.WriteString("// Code generated by `go run ./scripts/licenses`; DO NOT EDIT.\n" +
		"// Source: the dependency manifests and extra.json (see README.md).\n\n" +
		"package licenses\n\nvar entries = []Entry{\n")
	for _, c := range comps {
		fmt.Fprintf(&b, "\t{Name: %q, Version: %q, License: %q, Kind: %q, URL: %q, path: %q},\n",
			c.Name, c.Version, c.License, c.Kind, c.URL, c.Path)
	}
	b.WriteString("}\n")
	return format.Source([]byte(b.String()))
}

func renderNotice(comps []component, footer string) string {
	var b strings.Builder
	var project component
	for _, c := range comps {
		if c.Kind == "project" {
			project = c
		}
	}
	fmt.Fprintf(&b, "%s\n%s\n\nLicensed under the Apache License, Version 2.0 (see LICENSE).\n\n", project.Name, project.Copyright)
	b.WriteString("This file is generated by `go run ./scripts/licenses` from the dependency\n" +
		"manifests and desktop/licenses/extra.json; do not edit it. The full license\n" +
		"text of every component below is in THIRD-PARTY-LICENSES.md and, for the\n" +
		"desktop app, in its Licenses view (toolbar).\n")
	groups := []struct{ title, kind string }{
		{"Go standard library and code derived from it", "go-other"},
		{"Go modules (the library needs only golang.org/x/text; the desktop app the rest)", "go"},
		{"Frontend packages bundled into the desktop app", "npm"},
		{"Fonts", "font"},
		{"Data", "data"},
	}
	for _, grp := range groups {
		var rows []component
		for _, c := range comps {
			switch {
			case grp.kind == "go-other" && c.Kind == "go" && !c.Module,
				grp.kind == "go" && c.Kind == "go" && c.Module,
				grp.kind == c.Kind && !strings.HasPrefix(grp.kind, "go"):
				rows = append(rows, c)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s\n%s\n", grp.title, strings.Repeat("-", len(grp.title)))
		for _, c := range rows {
			v := ""
			if c.Version != "" {
				v = " " + c.Version
			}
			cr := c.Copyright
			if cr == "" {
				cr = "no copyright line in the license text"
			}
			fmt.Fprintf(&b, "  %s%s - %s - %s\n", c.Name, v, c.License, cr)
		}
	}
	b.WriteString("\nNotices that must accompany the software, reproduced in full\n" +
		"------------------------------------------------------------\n")
	for _, c := range comps {
		if c.Notice {
			fmt.Fprintf(&b, "\n%s%s (%s)\n%s\n\n%s\n", c.Name, versionSuffix(c), c.License, c.Why, strings.TrimSpace(c.Text))
		}
	}
	if footer != "" {
		fmt.Fprintf(&b, "\n--------------------------------------------------------------------------------\n%s\n", footer)
	}
	return b.String()
}

func versionSuffix(c component) string {
	if c.Version == "" {
		return ""
	}
	return " " + c.Version
}

func renderThirdParty(comps []component) string {
	var b strings.Builder
	b.WriteString("# Third-party licenses\n\n" +
		"go-xslt itself is licensed under the Apache License 2.0 (see `LICENSE`).\n" +
		"It incorporates the components below, each under its own license. This file\n" +
		"is generated by `go run ./scripts/licenses`; do not edit it.\n\n" +
		"| Component | Version | License |\n|---|---|---|\n")
	for _, c := range comps {
		if c.Kind != "project" {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, c.Version, c.License)
		}
	}
	for _, c := range comps {
		if c.Kind == "project" {
			continue
		}
		fmt.Fprintf(&b, "\n---\n\n## %s%s\n\nLicense: %s - %s\n\n%s\n\n~~~~text\n%s\n~~~~\n",
			c.Name, versionSuffix(c), c.License, c.URL, c.Why, strings.TrimSpace(c.Text))
	}
	return b.String()
}

// ------------------------------------------------------- disk: write / check

func (g *generator) write() error {
	for _, d := range generatedDirs {
		if err := os.RemoveAll(filepath.Join(g.root, licDir, d)); err != nil {
			return err
		}
	}
	for rel, b := range g.out {
		p := filepath.Join(g.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (g *generator) compareWithDisk() {
	var rels []string
	for r := range g.out {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		got, err := g.read(rel)
		switch {
		case err != nil:
			g.problem("%s is missing (generated output not committed)", rel)
		case !bytes.Equal(got, g.out[rel]):
			g.problem("%s is stale (differs from what the manifests and extra.json produce)", rel)
		}
	}
	for _, d := range generatedDirs {
		base := filepath.Join(g.root, licDir, d)
		_ = filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(g.root, p)
			rel = filepath.ToSlash(rel)
			if _, ok := g.out[rel]; !ok {
				g.problem("%s is not generated by any component (stale file; regenerate)", rel)
			}
			return nil
		})
	}
}
