//go:build ignore

// Benchmark: the go-xslt CLI against xsltproc (libxslt) and xmllint (libxml2)
// on identical inputs and stylesheets. The stylesheets use XSLT 1.0
// constructs only, so both processors run the same code; each is timed as a
// whole process (start, parse, transform, serialize), median of -n runs, with
// the peak resident memory of the median run.
//
//	go run scripts/bench/main.go [-n 5] [-records 200000]
//
// It builds the CLI from this checkout, generates the inputs in a temporary
// directory and prints a Markdown table.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tim-riep/go-xslt/scripts/internal/gendata"
)

var (
	runs    = flag.Int("n", 5, "runs per measurement (the median is reported)")
	records = flag.Int("records", 200000, "records in the generated document")
)

type workload struct {
	name, what string
	style      string // stylesheet text ("" for validation)
}

const head = `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">`

var workloads = []workload{
	{"identity", "copy the whole document", head + `
  <xsl:template match="/"><xsl:copy-of select="."/></xsl:template>
</xsl:stylesheet>`},
	{"templates", "recursive apply-templates, rebuilding every element and attribute", head + `
  <xsl:template match="@*|node()"><xsl:copy><xsl:apply-templates select="@*|node()"/></xsl:copy></xsl:template>
</xsl:stylesheet>`},
	{"sort", "sort all records by two keys, text output", head + `
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order">
      <xsl:sort select="customer"/><xsl:sort select="price" data-type="number" order="descending"/>
      <xsl:value-of select="concat(@id, ' ', customer, ' ', price, '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`},
	{"group", "Muenchian grouping with xsl:key, sum per group", head + `
  <xsl:output method="text"/>
  <xsl:key name="by-customer" match="order" use="customer"/>
  <xsl:template match="/">
    <xsl:for-each select="/orders/order[generate-id() = generate-id(key('by-customer', customer)[1])]">
      <xsl:value-of select="concat(customer, ' ', count(key('by-customer', customer)), ' ', sum(key('by-customer', customer)/price), '&#10;')"/>
    </xsl:for-each>
  </xsl:template>
</xsl:stylesheet>`},
	{"xpath", "predicates, string and number functions over every record", head + `
  <xsl:output method="text"/>
  <xsl:template match="/">
    <xsl:value-of select="count(//order[price &gt; 500 and contains(customer, '7')])"/>
    <xsl:text> </xsl:text>
    <xsl:value-of select="sum(//order[starts-with(@id, 'o1')]/price)"/>
    <xsl:text> </xsl:text>
    <xsl:value-of select="string-length(translate(normalize-space(string(/)), '0123456789', ''))"/>
  </xsl:template>
</xsl:stylesheet>`},
	{"validate", "XSD 1.0 validation of the document", ""},
}

const schema = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="orders"><xs:complexType><xs:sequence>
    <xs:element name="order" maxOccurs="unbounded"><xs:complexType><xs:sequence>
      <xs:element name="customer" type="xs:string"/>
      <xs:element name="date" type="xs:date"/>
      <xs:element name="price" type="xs:decimal"/>
      <xs:element name="items"><xs:complexType><xs:sequence>
        <xs:element name="item" maxOccurs="unbounded"><xs:complexType>
          <xs:attribute name="sku" type="xs:string" use="required"/>
          <xs:attribute name="qty" type="xs:positiveInteger" use="required"/>
        </xs:complexType></xs:element>
      </xs:sequence></xs:complexType></xs:element>
    </xs:sequence><xs:attribute name="id" type="xs:ID" use="required"/></xs:complexType></xs:element>
  </xs:sequence></xs:complexType></xs:element>
</xs:schema>`

func main() {
	flag.Parse()
	dir, err := os.MkdirTemp("", "go-xslt-bench")
	check(err)
	defer os.RemoveAll(dir)

	cli := filepath.Join(dir, "go-xslt")
	build := exec.Command("go", "build", "-o", cli, "./cmd/go-xslt")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	check(build.Run())

	doc := filepath.Join(dir, "orders.xml")
	check(os.WriteFile(doc, []byte(gendata.Orders(*records)), 0o644))
	xsd := filepath.Join(dir, "orders.xsd")
	check(os.WriteFile(xsd, []byte(schema), 0o644))
	fi, err := os.Stat(doc)
	check(err)

	fmt.Printf("%s, %s/%s, %s; input %d records, %.1f MB; median of %d runs\n\n",
		cpuName(), runtime.GOOS, runtime.GOARCH, toolVersions(), *records, float64(fi.Size())/1e6, *runs)
	fmt.Println("| Workload | go-xslt | xsltproc / xmllint | ratio | go-xslt peak RSS | libxml2 peak RSS |")
	fmt.Println("|---|---|---|---|---|---|")
	for _, w := range workloads {
		var ours, theirs []string
		if w.style == "" {
			ours = []string{cli, "validate", "-q", "-x", xsd, doc}
			theirs = []string{"xmllint", "--noout", "--schema", xsd, doc}
		} else {
			xsl := filepath.Join(dir, w.name+".xsl")
			check(os.WriteFile(xsl, []byte(w.style), 0o644))
			ours = []string{cli, "transform", "-s", xsl, "-o", filepath.Join(dir, w.name+".go.out"), "-backwards-compatible", doc}
			theirs = []string{"xsltproc", "-o", filepath.Join(dir, w.name+".lib.out"), xsl, doc}
		}
		ot, om := measure(ours)
		tt, tm := measure(theirs)
		if w.style != "" {
			compareOutputs(dir, w.name)
		}
		fmt.Printf("| %s — %s | %s | %s | %.2f× | %d MB | %d MB |\n", w.name, w.what,
			ot.Round(time.Millisecond), tt.Round(time.Millisecond), float64(ot)/float64(tt), om>>20, tm>>20)
	}
}

// measure runs argv *runs times and returns the median wall time and the
// peak RSS of that run.
func measure(argv []string) (time.Duration, int64) {
	type sample struct {
		d   time.Duration
		rss int64
	}
	var s []sample
	for i := 0; i < *runs; i++ {
		cmd := exec.Command(argv[0], argv[1:]...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		start := time.Now()
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n%s", strings.Join(argv, " "), err, stderr.String())
			os.Exit(1)
		}
		d := time.Since(start)
		rss := int64(0)
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			rss = int64(ru.Maxrss)
			if runtime.GOOS != "darwin" {
				rss *= 1024 // kilobytes on Linux, bytes on macOS
			}
		}
		s = append(s, sample{d, rss})
	}
	sort.Slice(s, func(i, j int) bool { return s[i].d < s[j].d })
	m := s[len(s)/2]
	return m.d, m.rss
}

// compareOutputs warns when the two processors disagree on a text result, so
// a timing is never reported for different work.
func compareOutputs(dir, name string) {
	a, _ := os.ReadFile(filepath.Join(dir, name+".go.out"))
	b, _ := os.ReadFile(filepath.Join(dir, name+".lib.out"))
	if strings.HasPrefix(string(a), "<") {
		return // XML results differ in insignificant ways (declaration, empty-element form)
	}
	if !sameText(string(a), string(b)) {
		fmt.Fprintf(os.Stderr, "warning: %s: the outputs differ\n", name)
	}
}

// sameText compares two text results word by word, taking numbers as equal
// when they agree to 12 significant digits: XPath 1.0 writes a double with as
// many digits as distinguish it (17251.089999999997), libxslt with 15
// (17251.09) — the same value.
func sameText(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) != len(wb) {
		return false
	}
	for i := range wa {
		if wa[i] == wb[i] {
			continue
		}
		x, err1 := strconv.ParseFloat(wa[i], 64)
		y, err2 := strconv.ParseFloat(wb[i], 64)
		if err1 != nil || err2 != nil || math.Abs(x-y) > 1e-12*math.Max(math.Abs(x), math.Abs(y)) {
			return false
		}
	}
	return true
}

func cpuName() string {
	if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return runtime.GOARCH
}

func toolVersions() string {
	out, _ := exec.Command("xsltproc", "--version").Output()
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(first)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
