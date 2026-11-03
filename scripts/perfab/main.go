// Command perfab decides whether engine build B is a global improvement over
// engine build A: it runs every workload under scripts/perfab/workloads with
// both, alternating, and reports per workload the median wall time, the peak
// RSS and the allocation totals, with a verdict. PASS means no workload got
// slower than -tolerance, no peak RSS grew by more than -rss-tolerance, no
// allocated-bytes total grew by more than -alloc-tolerance, and every output
// was byte-identical between A and B.
//
//	go run ./scripts/perfab -a origin/main -b . [-runs 5] [-only regex] [-bench]
//
// A and B are git revisions or directories of this module (or, with -a-bin /
// -b-bin, prebuilt runner binaries). The measured process is
// scripts/perfab/runner, built into each revision's tree through a build
// overlay, so a revision that predates the harness can be measured too.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tim-riep/go-xslt/scripts/internal/gendata"
)

//go:embed runner/main.go
var runnerSource []byte

var (
	revA      = flag.String("a", "origin/main", "revision or directory of build A (the base)")
	revB      = flag.String("b", ".", "revision or directory of build B (the candidate)")
	binA      = flag.String("a-bin", "", "prebuilt runner binary for A (instead of -a)")
	binB      = flag.String("b-bin", "", "prebuilt runner binary for B (instead of -b)")
	runs      = flag.Int("runs", 5, "runs per workload and build (the median counts)")
	tol       = flag.Float64("tolerance", 0.02, "a workload whose median is slower than A by more than this fraction fails the verdict")
	rssTol    = flag.Float64("rss-tolerance", 0.03, "a workload whose peak RSS is above A's by more than this fraction fails the verdict")
	allocTol  = flag.Float64("alloc-tolerance", 0.03, "a workload that allocates more bytes than A by more than this fraction fails the verdict")
	cache     = flag.String("cache", "", "directory for the generated inputs, extracted sources and binaries (default: $XDG_CACHE_HOME/go-xslt-perfab)")
	only      = flag.String("only", "", "run only the workloads whose name matches this regular expression")
	bench     = flag.Bool("bench", false, "also run the Go benchmarks of the engine packages on both revisions")
	benchOnly = flag.Bool("bench-only", false, "run only the Go benchmarks")
	benchRe   = flag.String("bench-filter", ".", "-bench pattern for -bench")
	records   = flag.Int("records", 200000, "records in the large generated document")
)

type workload struct {
	name, desc string
	args       []string // runner arguments; {data}, {workloads}, {root} are expanded
	source     string   // the source document under {data} (empty: none)
}

// The workload set. Each stylesheet or schema file starts with a comment
// saying what it stresses; desc repeats it for the table.
var workloads = []workload{
	{"identity", "copy-of the whole document", []string{"-w", "transform", "-bc", "-s", "{workloads}/identity.xsl"}, "orders-200k.xml"},
	{"templates", "recursive apply-templates rebuilding every node", []string{"-w", "transform", "-bc", "-s", "{workloads}/templates.xsl"}, "orders-200k.xml"},
	{"sort", "xsl:sort by two keys, text output", []string{"-w", "transform", "-bc", "-s", "{workloads}/sort.xsl"}, "orders-200k.xml"},
	{"group", "Muenchian grouping with xsl:key", []string{"-w", "transform", "-bc", "-s", "{workloads}/group.xsl"}, "orders-200k.xml"},
	{"xpath", "predicates, string and number functions", []string{"-w", "transform", "-bc", "-s", "{workloads}/xpath.xsl"}, "orders-200k.xml"},
	{"validate", "XSD 1.0 validation", []string{"-w", "validate", "-x", "{workloads}/validate.xsd"}, "orders-200k.xml"},
	{"validate11", "XSD 1.1 validation with assertions and identity constraints", []string{"-w", "validate", "-xsd", "1.1", "-x", "{workloads}/validate11.xsd"}, "orders-200k.xml"},
	{"group-map-json", "for-each-group group-by, xsl:map, JSON output", []string{"-w", "transform", "-s", "{workloads}/group-map-json.xsl"}, "orders-200k.xml"},
	{"functions", "typed xsl:functions, format-date, tokenize", []string{"-w", "transform", "-s", "{workloads}/functions.xsl"}, "orders-200k.xml"},
	{"iterate-map", "xsl:iterate with a typed map parameter and map:put", []string{"-w", "transform", "-s", "{workloads}/iterate-map.xsl"}, "orders-200k.xml"},
	{"iterate-array", "xsl:iterate with array:append", []string{"-w", "transform", "-s", "{workloads}/iterate-array.xsl"}, "orders-20k.xml"},
	{"keys", "xsl:key lookups per record and item", []string{"-w", "transform", "-bc", "-s", "{workloads}/keys.xsl"}, "orders-200k.xml"},
	{"regex", "matches, replace, analyze-string", []string{"-w", "transform", "-s", "{workloads}/regex.xsl"}, "orders-200k.xml"},
	{"strings", "string functions over all records", []string{"-w", "transform", "-s", "{workloads}/strings.xsl"}, "orders-200k.xml"},
	{"numbers", "format-number, format-integer, round, decimals", []string{"-w", "transform", "-s", "{workloads}/numbers.xsl"}, "orders-20k.xml"},
	{"number-position", "xsl:number and position()/last()", []string{"-w", "transform", "-bc", "-s", "{workloads}/number-position.xsl"}, "orders-2k.xml"},
	{"recursion", "deep named-template recursion", []string{"-w", "transform", "-s", "{workloads}/recursion.xsl"}, "orders-200k.xml"},
	{"html", "HTML output method, attribute-heavy", []string{"-w", "transform", "-bc", "-s", "{workloads}/html.xsl"}, "orders-20k.xml"},
	{"json-roundtrip", "parse-json, json-to-xml, xml-to-json, serialize", []string{"-w", "transform", "-s", "{workloads}/json-roundtrip.xsl", "-p", "json={data}/orders-20k.json"}, ""},
	{"streaming", "xsl:source-document streamable=yes, bounded memory", []string{"-w", "transform", "-s", "{workloads}/streaming.xsl", "-p", "src={data}/orders-200k.xml"}, ""},
	{"compile", "compile a large stylesheet 10 times", []string{"-w", "compile", "-n", "10", "-bc", "-s", "{compile}"}, ""},
	{"many", "the 50 KB document transformed 200 times, compiling each time", []string{"-w", "many", "-n", "200", "-s", "{workloads}/many.xsl"}, "orders-small.xml"},
	{"many-compiled", "the 50 KB document transformed 200 times, compiled once", []string{"-w", "many-compiled", "-n", "200", "-s", "{workloads}/many.xsl"}, "orders-small.xml"},
}

const docbook = "test/conformance/xslt30-test/tests/misc/docbook/docbook-xsl-1.79.1/xhtml/docbook.xsl"

type sample struct {
	wall  time.Duration // whole process
	rss   int64         // peak resident set, bytes
	alloc uint64        // bytes allocated by the work
	count uint64        // allocations by the work
}

func main() {
	flag.Parse()
	root, err := moduleRoot()
	check(err)
	if *cache == "" {
		dir, err := os.UserCacheDir()
		check(err)
		*cache = filepath.Join(dir, "go-xslt-perfab")
	}
	check(os.MkdirAll(*cache, 0o755))

	a, b := *binA, *binB
	srcA, srcB := "", ""
	if a == "" {
		srcA, a = build(root, *revA, "a")
	}
	if b == "" {
		srcB, b = build(root, *revB, "b")
	}
	fmt.Printf("A: %s\nB: %s\n", describe(*revA, *binA, srcA), describe(*revB, *binB, srcB))

	ok := true
	if !*benchOnly {
		ok = compareWorkloads(root, a, b)
	}
	if *bench || *benchOnly {
		if srcA == "" || srcB == "" {
			fmt.Println("-bench needs -a and -b (sources), not prebuilt binaries")
			ok = false
		} else if !compareBenchmarks(srcA, srcB) {
			ok = false
		}
	}
	if !ok {
		os.Exit(1)
	}
}

func describe(rev, bin, src string) string {
	if bin != "" {
		return "prebuilt " + bin
	}
	if sha, err := output(src, "git", "rev-parse", "--short", "HEAD"); err == nil {
		return rev + " (" + strings.TrimSpace(sha) + ")"
	}
	return rev
}

// build resolves rev (a directory or a git revision of the module at root)
// to a source tree and compiles the runner into it through a build overlay,
// so the tree does not need to contain the runner.
func build(root, rev, tag string) (src, bin string) {
	if fi, err := os.Stat(rev); err == nil && fi.IsDir() {
		src, err = filepath.Abs(rev)
		check(err)
	} else {
		sha, err := output(root, "git", "rev-parse", rev)
		if err != nil {
			check(fmt.Errorf("%s: neither a directory nor a git revision: %v", rev, err))
		}
		sha = strings.TrimSpace(sha)
		src = filepath.Join(*cache, "src-"+sha)
		if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
			os.RemoveAll(src)
			check(os.MkdirAll(src, 0o755))
			archive := exec.Command("git", "archive", sha)
			archive.Dir = root
			untar := exec.Command("tar", "-x", "-C", src)
			untar.Stdin, _ = archive.StdoutPipe()
			untar.Stderr = os.Stderr
			check(untar.Start())
			check(archive.Run())
			check(untar.Wait())
		}
	}
	runner := filepath.Join(*cache, "runner-"+tag+".go")
	check(os.WriteFile(runner, runnerSource, 0o644))
	overlay := filepath.Join(*cache, "overlay-"+tag+".json")
	ov, _ := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(src, "scripts", "perfab", "runner", "main.go"): runner}})
	check(os.WriteFile(overlay, ov, 0o644))
	bin = filepath.Join(*cache, "runner-"+tag)
	cmd := exec.Command("go", "build", "-overlay", overlay, "-o", bin, "./scripts/perfab/runner")
	cmd.Dir = src
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	check(cmd.Run())
	return src, bin
}

// inputs generates the data files once.
func inputs(root string) (data string, compile string) {
	data = filepath.Join(*cache, "data")
	check(os.MkdirAll(data, 0o755))
	gen := func(name string, make func() string) {
		p := filepath.Join(data, name)
		if _, err := os.Stat(p); err == nil {
			return
		}
		fmt.Printf("generating %s\n", p)
		check(os.WriteFile(p, []byte(make()), 0o644))
	}
	gen("orders-200k.xml", func() string { return gendata.Orders(*records) })
	gen("orders-20k.xml", func() string { return gendata.Orders(20000) })
	gen("orders-2k.xml", func() string { return gendata.Orders(2000) })
	gen("orders-small.xml", func() string { return gendata.Orders(270) })
	gen("orders-20k.json", func() string { return gendata.OrdersJSON(20000) })
	gen("templates-2000.xsl", func() string { return gendata.Templates(2000) })
	compile = filepath.Join(root, docbook)
	if _, err := os.Stat(compile); err != nil {
		compile = filepath.Join(data, "templates-2000.xsl")
	}
	return data, compile
}

func compareWorkloads(root, binA, binB string) bool {
	data, compileSheet := inputs(root)
	wl := filepath.Join(root, "scripts", "perfab", "workloads")
	outDir := filepath.Join(*cache, "out")
	check(os.MkdirAll(outDir, 0o755))
	var onlyRe *regexp.Regexp
	if *only != "" {
		onlyRe = regexp.MustCompile(*only)
	}
	fmt.Printf("compile workload: %s\n%d runs per build, alternating; medians\n\n", compileSheet, *runs)
	fmt.Println("| workload | A wall | B wall | B/A | A RSS | B RSS | B/A | A alloc | B alloc | B/A | A mallocs | B mallocs | B/A |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	var offenders []string
	for _, w := range workloads {
		if onlyRe != nil && !onlyRe.MatchString(w.name) {
			continue
		}
		args := make([]string, 0, len(w.args)+4)
		for _, a := range w.args {
			a = strings.NewReplacer("{data}", data, "{workloads}", wl, "{root}", root, "{compile}", compileSheet).Replace(a)
			args = append(args, a)
		}
		var sa, sb []sample
		var refOut []byte
		for i := 0; i < *runs; i++ {
			order := []struct {
				bin string
				s   *[]sample
				tag string
			}{{binA, &sa, "a"}, {binB, &sb, "b"}}
			if i%2 == 1 {
				order[0], order[1] = order[1], order[0]
			}
			for _, o := range order {
				out := filepath.Join(outDir, w.name+"."+o.tag+".out")
				full := append(append([]string{}, args...), "-o", out)
				if w.source != "" {
					full = append(full, filepath.Join(data, w.source))
				}
				s, err := run(o.bin, full)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s (%s): %v\n", w.name, o.tag, err)
					os.Exit(1)
				}
				*o.s = append(*o.s, s)
				got, err := os.ReadFile(out)
				check(err)
				if refOut == nil {
					refOut = got
				} else if !bytes.Equal(refOut, got) {
					offenders = append(offenders, w.name+": output differs between A and B")
					fmt.Fprintf(os.Stderr, "%s: output of %s differs from the first run's (see %s)\n", w.name, o.tag, outDir)
				}
			}
		}
		ma, mb := median(sa), median(sb)
		wr := ratio(float64(mb.wall), float64(ma.wall))
		rr := ratio(float64(mb.rss), float64(ma.rss))
		ar := ratio(float64(mb.alloc), float64(ma.alloc))
		cr := ratio(float64(mb.count), float64(ma.count))
		fmt.Printf("| %s | %s | %s | %.3f | %d MB | %d MB | %.3f | %d MB | %d MB | %.3f | %s | %s | %.3f |\n",
			w.name, ms(ma.wall), ms(mb.wall), wr, ma.rss>>20, mb.rss>>20, rr, ma.alloc>>20, mb.alloc>>20, ar, thousands(ma.count), thousands(mb.count), cr)
		if wr > 1+*tol {
			offenders = append(offenders, fmt.Sprintf("%s: wall %.3f (%s -> %s)", w.name, wr, ms(ma.wall), ms(mb.wall)))
		}
		if rr > 1+*rssTol {
			offenders = append(offenders, fmt.Sprintf("%s: peak RSS %.3f (%d MB -> %d MB)", w.name, rr, ma.rss>>20, mb.rss>>20))
		}
		if ar > 1+*allocTol {
			offenders = append(offenders, fmt.Sprintf("%s: allocated bytes %.3f (%d MB -> %d MB)", w.name, ar, ma.alloc>>20, mb.alloc>>20))
		}
	}
	fmt.Println()
	if len(offenders) > 0 {
		fmt.Println("verdict: FAIL")
		for _, o := range offenders {
			fmt.Println("  " + o)
		}
		return false
	}
	fmt.Printf("verdict: PASS (no workload slower than A by more than %.0f%%, no peak RSS or allocation above A by more than %.0f%%/%.0f%%, all outputs identical)\n", *tol*100, *rssTol*100, *allocTol*100)
	return true
}

// run executes the runner once and returns its measurements.
func run(bin string, args []string) (sample, error) {
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	if err != nil {
		return sample{}, fmt.Errorf("%s %s: %v\n%s", bin, strings.Join(args, " "), err, stderr.String())
	}
	var m struct {
		Wall  int64  `json:"wall_ns"`
		Alloc uint64 `json:"alloc_bytes"`
		Count uint64 `json:"mallocs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		return sample{}, fmt.Errorf("%s: unreadable measurement %q: %v", bin, stdout.String(), err)
	}
	s := sample{wall: wall, alloc: m.Alloc, count: m.Count}
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		s.rss = int64(ru.Maxrss) * 1024 // kilobytes on Linux
	}
	return s, nil
}

// median takes the median of each measure separately (the wall median's run
// need not be the RSS median's).
func median(s []sample) sample {
	n := len(s)
	walls := make([]time.Duration, n)
	rss := make([]int64, n)
	alloc := make([]uint64, n)
	count := make([]uint64, n)
	for i, x := range s {
		walls[i], rss[i], alloc[i], count[i] = x.wall, x.rss, x.alloc, x.count
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	sort.Slice(rss, func(i, j int) bool { return rss[i] < rss[j] })
	sort.Slice(alloc, func(i, j int) bool { return alloc[i] < alloc[j] })
	sort.Slice(count, func(i, j int) bool { return count[i] < count[j] })
	return sample{walls[n/2], rss[n/2], alloc[n/2], count[n/2]}
}

func ratio(b, a float64) float64 {
	if a == 0 {
		if b == 0 {
			return 1
		}
		return 9.999
	}
	return b / a
}

func ms(d time.Duration) string { return fmt.Sprintf("%.0f ms", float64(d)/1e6) }

func thousands(n uint64) string {
	s := strconv.FormatUint(n, 10)
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	return string(b)
}

var benchPackages = []string{"./internal/xpath", "./internal/xslt", "./internal/xmltree", "./internal/xsd", "./engine"}

type benchResult struct{ ns, bytes, allocs float64 }

// compareBenchmarks runs the engine packages' Go benchmarks in both trees,
// alternating, and tabulates the medians.
func compareBenchmarks(srcA, srcB string) bool {
	fmt.Printf("\nGo benchmarks (%s), %d runs per revision, alternating; medians of ns/op\n\n", strings.Join(benchPackages, " "), *runs)
	resA, resB := map[string][]benchResult{}, map[string][]benchResult{}
	for i := 0; i < *runs; i++ {
		for _, side := range []struct {
			src string
			res map[string][]benchResult
		}{{srcA, resA}, {srcB, resB}} {
			args := append([]string{"test", "-run", "NONE", "-bench", *benchRe, "-benchmem", "-count", "1"}, benchPackages...)
			out, err := output(side.src, "go", args...)
			if err != nil {
				fmt.Fprintf(os.Stderr, "go test -bench in %s: %v\n%s", side.src, err, out)
				return false
			}
			for _, line := range strings.Split(out, "\n") {
				f := strings.Fields(line)
				if len(f) < 7 || !strings.HasPrefix(f[0], "Benchmark") {
					continue
				}
				var r benchResult
				r.ns, _ = strconv.ParseFloat(f[2], 64)
				r.bytes, _ = strconv.ParseFloat(f[4], 64)
				r.allocs, _ = strconv.ParseFloat(f[6], 64)
				side.res[f[0]] = append(side.res[f[0]], r)
			}
		}
	}
	names := make([]string, 0, len(resA))
	for n := range resA {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println("| benchmark | A ns/op | B ns/op | B/A | A B/op | B B/op | A allocs/op | B allocs/op |")
	fmt.Println("|---|---|---|---|---|---|---|---|")
	ok := true
	for _, n := range names {
		a, b := medianBench(resA[n]), medianBench(resB[n])
		r := ratio(b.ns, a.ns)
		fmt.Printf("| %s | %.0f | %.0f | %.3f | %.0f | %.0f | %.0f | %.0f |\n", n, a.ns, b.ns, r, a.bytes, b.bytes, a.allocs, b.allocs)
		if r > 1+*tol || b.bytes > a.bytes*(1+*allocTol) {
			ok = false
			fmt.Fprintf(os.Stderr, "  %s is slower or allocates more\n", n)
		}
	}
	if ok {
		fmt.Println("\nbenchmarks: PASS")
	} else {
		fmt.Println("\nbenchmarks: FAIL (see above)")
	}
	return ok
}

func medianBench(rs []benchResult) benchResult {
	pick := func(f func(benchResult) float64) float64 {
		v := make([]float64, len(rs))
		for i, r := range rs {
			v[i] = f(r)
		}
		sort.Float64s(v)
		return v[len(v)/2]
	}
	return benchResult{pick(func(r benchResult) float64 { return r.ns }), pick(func(r benchResult) float64 { return r.bytes }), pick(func(r benchResult) float64 { return r.allocs })}
}

func output(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// moduleRoot is the directory of the go.mod this program belongs to (the
// harness is run from inside the module, usually at its root).
func moduleRoot() (string, error) {
	out, err := output("", "go", "env", "GOMOD")
	if err != nil {
		return "", err
	}
	gomod := strings.TrimSpace(out)
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("run perfab from inside the go-xslt module")
	}
	return filepath.Dir(gomod), nil
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "perfab:", err)
		os.Exit(1)
	}
}
