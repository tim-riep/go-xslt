package conformance

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/tim-riep/go-xslt/internal/engine"
)

// TestTransformToMatchesTransform is the breadth gate for the writer-based
// output path, and in particular for the incremental serializer behind it.
//
// The engine's own tests cover the serializer's tricky corners deliberately;
// this one covers the corners nobody thought of, by running the W3C suite's
// stylesheets through BOTH entry points and requiring the same bytes. It is not
// a conformance measurement — a stylesheet is paired with whatever source
// document happens to sit beside it, so most results are uninteresting and many
// runs fail outright, which is fine: every case that does produce output is one
// more construction shape the drained path had to reproduce exactly.
//
// A sample by default, because a handful of the suite's stylesheets run to the
// engine's step budget and each one is paid for several times here;
// TRANSFORMTO_ALL=1 runs every stylesheet (~10 minutes, and the number that
// matters: 6,606 stylesheets produced reproducible output and 0 differed). It
// skips cleanly when the suite is not checked out, like every other test here.
func TestTransformToMatchesTransform(t *testing.T) {
	root := filepath.Join("xslt30-test", "tests")
	if _, err := os.Stat(root); err != nil {
		t.Skip("xslt30-test suite not present")
	}

	var sheets []string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".xsl") {
			return nil
		}
		// A leading underscore marks the suite's own generators and catalogs.
		if strings.HasPrefix(filepath.Base(p), "_") {
			return nil
		}
		sheets = append(sheets, p)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(sheets)
	if os.Getenv("TRANSFORMTO_ALL") == "" {
		var s []string
		for i := 0; i < len(sheets); i += 12 {
			s = append(s, sheets[i])
		}
		sheets = s
	}

	// One source per directory: the suite keeps a test-set's data files beside
	// its stylesheets, so the first plausible one is usually the right one, and
	// where it is not the run simply produces less.
	var mu sync.Mutex
	sources := map[string]string{}
	pickSource := func(dir string) string {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := sources[dir]; ok {
			return s
		}
		src := "<_/>"
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".xml") || strings.HasPrefix(n, "_") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil || len(b) > 64<<10 {
				continue
			}
			src = string(b)
			break
		}
		sources[dir] = src
		return src
	}

	const maxSheet = 512 << 10
	type diff struct{ path, want, got string }
	var (
		wg            sync.WaitGroup
		ran, differed int
		diffs         []diff
		sem           = make(chan struct{}, runtime.NumCPU())
		resMu         sync.Mutex
	)
	for _, p := range sheets {
		b, err := os.ReadFile(p)
		if err != nil || len(b) > maxSheet {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p string, sheet string) {
			defer wg.Done()
			defer func() { <-sem }()
			req := engine.Request{Stylesheet: sheet, Source: pickSource(filepath.Dir(p)), BaseDir: filepath.Dir(p)}
			want := engine.Transform(req)
			if want.HasErrors() {
				return
			}
			// A handful of the suite's stylesheets are not reproducible at all:
			// they print fn:current-dateTime() to the microsecond, or a
			// generate-id() this engine derives from a pointer. Comparing two
			// runs of them says nothing about which path produced them, so they
			// are recognized by running the SAME path twice and dropped.
			if again := engine.Transform(req); again.Output != want.Output {
				return
			}
			var got strings.Builder
			res := engine.TransformTo(&got, req)
			resMu.Lock()
			defer resMu.Unlock()
			ran++
			if res.HasErrors() || got.String() != want.Output {
				differed++
				if len(diffs) < 10 {
					diffs = append(diffs, diff{p, want.Output, got.String()})
				}
			}
		}(p, string(b))
	}
	wg.Wait()

	for _, d := range diffs {
		t.Errorf("%s: output differs\n want %.200q\n  got %.200q", d.path, d.want, d.got)
	}
	t.Logf("TransformTo vs Transform: %d stylesheets produced output, %d differed", ran, differed)
	if ran < 150 {
		t.Errorf("only %d stylesheets produced comparable output; the gate is too thin to mean anything", ran)
	}
}
