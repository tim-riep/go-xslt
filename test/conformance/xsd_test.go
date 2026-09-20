package conformance

// W3C XML Schema (XSD) 1.0 + 1.1 conformance harness (xsdtests). Runs the
// official suite against our from-scratch validator (internal/xsd) and checks
// the top-level valid/invalid verdict — the only thing the suite asserts.
//
// The suite groups tests as: <testGroup> → one <schemaTest> (compile a schema:
// valid/invalid) plus zero+ <instanceTest> (validate an instance against that
// schema: valid/invalid). A testGroup / expected may carry version="1.0|1.1";
// absent means it applies to both. We run two passes (1.0 and 1.1).
//
// Clone (the test skips if absent):
//   git clone --depth 1 https://github.com/w3c/xsdtests.git test/conformance/xsdtests
// Run:  go test ./test/conformance/ -run TestXSD -v
//   XSD_ONLY=<set-substring>  XSD_PROGRESS=1  XSD_SEQ=1  XSD_VERSION=1.1

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/tim-riep/go-xslt/internal/xsd"
)

// ---- catalog model (the ts: test-suite schema) ----

type xsdSuite struct {
	Refs []struct {
		Href string `xml:"href,attr"`
	} `xml:"testSetRef"`
}

type xsdTestSet struct {
	Name    string         `xml:"name,attr"`
	Version string         `xml:"version,attr"` // token list, e.g. "1.1" or "1.0 1.1"
	Groups  []xsdTestGroup `xml:"testGroup"`
}
type xsdTestGroup struct {
	Name      string            `xml:"name,attr"`
	Version   string            `xml:"version,attr"` // "", "1.0", "1.1"
	Schema    *xsdSchemaTest    `xml:"schemaTest"`
	Instances []xsdInstanceTest `xml:"instanceTest"`
}
type xsdSchemaTest struct {
	Name     string        `xml:"name,attr"`
	Version  string        `xml:"version,attr"`
	Docs     []xsdDocRef   `xml:"schemaDocument"`
	Expected []xsdExpected `xml:"expected"`
	Current  []xsdCurrent  `xml:"current"`
}
type xsdInstanceTest struct {
	Name     string        `xml:"name,attr"`
	Version  string        `xml:"version,attr"`
	Doc      xsdDocRef     `xml:"instanceDocument"`
	Expected []xsdExpected `xml:"expected"`
	Current  []xsdCurrent  `xml:"current"`
}
type xsdDocRef struct {
	Href string `xml:"href,attr"`
}
type xsdCurrent struct {
	Status string `xml:"status,attr"` // accepted | stable | queried | disputed-spec
}

type xsdExpected struct {
	Validity string `xml:"validity,attr"` // valid | invalid | indeterminate
	Version  string `xml:"version,attr"`  // "", "1.0", "1.1"
}

// expectedFor picks the expected validity applicable to run version ver: an
// <expected> whose version matches ver wins; otherwise the versionless one.
func expectedFor(exps []xsdExpected, ver string) (string, bool) {
	generic, hasGeneric := "", false
	for _, e := range exps {
		if e.Version == ver {
			return e.Validity, true
		}
		if e.Version == "" {
			generic, hasGeneric = e.Validity, true
		}
	}
	return generic, hasGeneric
}

// versionApplies implements the suite's version-token semantics (xsts.xsd): an
// absent attribute applies everywhere; otherwise the whitespace-separated token
// list must contain the run version. Alien tokens (e.g. "Unicode_4.0.0") match
// neither pass. Used at testSet, testGroup, schemaTest and instanceTest level.
func versionApplies(attr, runVer string) bool {
	if attr == "" {
		return true
	}
	for _, tok := range strings.Fields(attr) {
		if tok == runVer {
			return true
		}
	}
	return false
}

// isQueried reports whether a test's latest <current> marks it as W3C-disputed
// (status="queried"). Queried tests are counted in their own bucket, outside
// the pass/fail denominator, like skip.
func isQueried(cur []xsdCurrent) bool {
	return len(cur) > 0 && cur[len(cur)-1].Status == "queried"
}

func xsdVersionOf(ver string) xsd.Version {
	if ver == "1.1" {
		return xsd.Version11
	}
	return xsd.Version10
}

// ---- per-set result accumulator ----

type xsdResult struct {
	pass, fail, skip, unsup int
	queried                 int
	skipReasons             map[string]int
	fails                   []string
}

var xsdShowErr bool
var xsdShowUnsup bool

func (r *xsdResult) skipWith(reason string) { r.skip++; r.skipReasons[reason]++ }

// score records one schemaTest/instanceTest outcome. unsupported means the
// engine could not decide (still being implemented); it is neither pass nor
// fail so the baseline is honest.
func (r *xsdResult) score(label, expected string, unsupported, ok, verbose bool) {
	switch {
	case unsupported:
		r.unsup++
		if xsdShowUnsup && len(r.fails) < 4000 {
			r.fails = append(r.fails, fmt.Sprintf("UNSUP exp=%s %s", expected, label))
		}
	case ok:
		r.pass++
	default:
		r.fail++
		if verbose && len(r.fails) < 4000 {
			r.fails = append(r.fails, fmt.Sprintf("FAIL exp=%s %s", expected, label))
		}
	}
}

func verdictOK(ourValid bool, expected string) bool {
	switch expected {
	case "valid":
		return ourValid
	case "invalid":
		return !ourValid
	}
	return false
}

// runXSDSet runs every applicable test group in one .testSet under version ver.
func runXSDSet(setPath, ver string, verbose bool) *xsdResult {
	r := &xsdResult{skipReasons: map[string]int{}}
	b, err := os.ReadFile(setPath)
	if err != nil {
		return r
	}
	var set xsdTestSet
	if err := unmarshalFOTS(b, &set); err != nil {
		return r
	}
	if !versionApplies(set.Version, ver) {
		return r // whole set out of scope for this version (e.g. saxon 1.1 sets)
	}
	dir := filepath.Dir(setPath)
	for _, g := range set.Groups {
		safeRunXSDGroup(g, dir, ver, r, verbose)
	}
	return r
}

// safeRunXSDGroup runs one group under a panic guard so a single pathological
// schema cannot abort the whole suite; a panic is recorded as a fail.
func safeRunXSDGroup(g xsdTestGroup, dir, ver string, r *xsdResult, verbose bool) {
	defer func() {
		if e := recover(); e != nil {
			r.fail++
			if verbose && len(r.fails) < 4000 {
				r.fails = append(r.fails, fmt.Sprintf("PANIC %v/%v", g.Name, e))
			}
		}
	}()
	runXSDGroup(g, dir, ver, r, verbose)
}

func runXSDGroup(g xsdTestGroup, dir, ver string, r *xsdResult, verbose bool) {
	if !versionApplies(g.Version, ver) {
		return // not applicable to this version — excluded from the denominator
	}
	if g.Schema == nil {
		r.skipWith("no-schema-test")
		return
	}
	if !versionApplies(g.Schema.Version, ver) {
		return // schemaTest out of scope — instance verdicts have nothing to stand on
	}
	schemaExp, ok := expectedFor(g.Schema.Expected, ver)
	if !ok || schemaExp == "indeterminate" {
		r.skipWith("no-schema-verdict")
		return
	}

	// Read the schema document(s); the first is the entry, baseDir is its folder.
	var docs []string
	var baseDir string
	for i, d := range g.Schema.Docs {
		p := filepath.Clean(filepath.Join(dir, d.Href))
		data, err := os.ReadFile(p)
		if err != nil {
			r.skipWith("missing-schema-file")
			return
		}
		docs = append(docs, string(data))
		if i == 0 {
			baseDir = filepath.Dir(p)
		}
	}
	if len(docs) == 0 {
		r.skipWith("no-schema-doc")
		return
	}

	sch, cerr := xsd.Compile(docs, baseDir, xsdVersionOf(ver))
	schemaUnsup := errors.Is(cerr, xsd.ErrUnsupported)
	ourSchemaValid := cerr == nil
	schemaLabel := g.Schema.Name
	if xsdShowErr {
		if schemaExp == "valid" && cerr != nil && !schemaUnsup {
			schemaLabel += " || " + cerr.Error()
		} else if schemaExp == "invalid" && ourSchemaValid {
			schemaLabel += " <<" + g.Schema.Docs[0].Href + ">>"
		}
	}
	if isQueried(g.Schema.Current) {
		r.queried++ // W3C-disputed verdict — excluded from the denominator
	} else {
		r.score(schemaLabel, schemaExp, schemaUnsup, verdictOK(ourSchemaValid, schemaExp), verbose)
	}

	// Instance tests only make sense when the schema is expected to be valid.
	if schemaExp != "valid" {
		return
	}
	for _, it := range g.Instances {
		if !versionApplies(it.Version, ver) {
			continue // test-level version gate (e.g. a 1.1-only instance twin)
		}
		instExp, ok := expectedFor(it.Expected, ver)
		if !ok || instExp == "indeterminate" {
			r.skipWith("no-instance-verdict")
			continue
		}
		if isQueried(it.Current) {
			r.queried++ // W3C-disputed verdict — excluded from the denominator
			continue
		}
		p := filepath.Clean(filepath.Join(dir, it.Doc.Href))
		data, err := os.ReadFile(p)
		if err != nil {
			r.skipWith("missing-instance-file")
			continue
		}
		switch {
		case schemaUnsup:
			r.score(it.Name, instExp, true, false, verbose) // engine can't compile yet
		case cerr != nil:
			// We rejected a schema the suite says is valid; the instance verdict
			// is unreachable — count it as a genuine miss.
			r.score(it.Name, instExp, false, false, verbose)
		default:
			res, verr := sch.Validate(string(data))
			instUnsup := errors.Is(verr, xsd.ErrUnsupported)
			ourValid := verr == nil && res.Valid
			instLabel := it.Name
			if xsdShowErr && !instUnsup {
				if instExp == "valid" && !ourValid {
					instLabel = it.Doc.Href
					if verr != nil {
						instLabel += " || " + verr.Error()
					} else if len(res.Errors) > 0 {
						instLabel += " || " + res.Errors[0].Message
					}
				} else if instExp == "invalid" && ourValid {
					instLabel += " <<" + it.Doc.Href + ">>"
				}
			}
			r.score(instLabel, instExp, instUnsup, verdictOK(ourValid, instExp), verbose)
		}
	}
}

func TestXSD(t *testing.T) {
	root := filepath.Join(repoRoot(), "test", "conformance", "xsdtests")
	suitePath := filepath.Join(root, "suite.xml")
	sb, err := os.ReadFile(suitePath)
	if err != nil {
		t.Skip("xsdtests not present (git clone --depth 1 https://github.com/w3c/xsdtests.git test/conformance/xsdtests)")
	}
	var suite xsdSuite
	if err := unmarshalFOTS(sb, &suite); err != nil {
		t.Fatalf("parse suite.xml: %v", err)
	}

	only := os.Getenv("XSD_ONLY")
	progress := os.Getenv("XSD_PROGRESS") != ""
	xsdShowErr = os.Getenv("XSD_ERR") != ""
	xsdShowUnsup = os.Getenv("XSD_UNSUP") != ""
	verbose := testing.Verbose() || only != ""

	var setFiles []string
	for _, ref := range suite.Refs {
		if only != "" && !strings.Contains(ref.Href, only) {
			continue
		}
		setFiles = append(setFiles, filepath.Join(root, filepath.FromSlash(ref.Href)))
	}

	versions := []string{"1.0", "1.1"}
	if v := os.Getenv("XSD_VERSION"); v != "" {
		versions = []string{v}
	}

	workers := runtime.NumCPU()
	if os.Getenv("XSD_SEQ") != "" {
		workers = 1
	}

	for _, ver := range versions {
		results := make([]*xsdResult, len(setFiles))
		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for i, sf := range setFiles {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, sf string) {
				defer wg.Done()
				defer func() { <-sem }()
				if progress {
					fmt.Fprintf(os.Stderr, "XSD %s SET %s\n", ver, filepath.Base(sf))
				}
				results[i] = runXSDSet(sf, ver, verbose)
			}(i, sf)
		}
		wg.Wait()

		var pass, fail, skip, unsup, queried int
		skipReasons := map[string]int{}
		var csv strings.Builder
		csv.WriteString("set,pass,fail,skip,unsupported,queried\n")
		for i, r := range results {
			if r == nil {
				continue
			}
			pass, fail, skip, unsup, queried = pass+r.pass, fail+r.fail, skip+r.skip, unsup+r.unsup, queried+r.queried
			for k, v := range r.skipReasons {
				skipReasons[k] += v
			}
			csv.WriteString(fmt.Sprintf("%s,%d,%d,%d,%d,%d\n", filepath.Base(setFiles[i]), r.pass, r.fail, r.skip, r.unsup, r.queried))
			for _, f := range r.fails {
				t.Logf("[%s] %s", ver, f)
			}
		}
		ran := pass + fail
		rate := 0.0
		if ran > 0 {
			rate = 100 * float64(pass) / float64(ran)
		}
		t.Logf("XSD %s: %.1f%% pass (%d/%d ran) — unsupported=%d skip=%d queried=%d", ver, rate, pass, ran, unsup, skip, queried)
		reasons := make([]string, 0, len(skipReasons))
		for k := range skipReasons {
			reasons = append(reasons, k)
		}
		sort.Slice(reasons, func(a, b int) bool { return skipReasons[reasons[a]] > skipReasons[reasons[b]] })
		for _, k := range reasons {
			t.Logf("  skip[%s]=%d", k, skipReasons[k])
		}
		if only == "" {
			out := filepath.Join(repoRoot(), "tools", fmt.Sprintf("xsd_byset_%s.csv", ver))
			_ = os.WriteFile(out, []byte(csv.String()), 0o644)
		}
	}
}
