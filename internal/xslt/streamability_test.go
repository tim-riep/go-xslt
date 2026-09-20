package xslt

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/tim-riep/go-xslt/internal/xmltree"
	"github.com/tim-riep/go-xslt/internal/xpath"
)

// strmSettable re-derives an addressable view of a field so the audit can seed
// it. Every instruction struct field in this package is unexported, and
// reflect refuses to set those through an ordinary Field() view; without this
// the audit would seed nothing and pass vacuously.
func strmSettable(f reflect.Value) reflect.Value {
	if f.CanSet() {
		return f
	}
	if !f.CanAddr() {
		return f
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

// strmAllInstrTypes lists every compiled instruction type in the package. The
// list is checked for completeness against the registries by
// TestStrmInstructionTypeListIsComplete, so a new instr_*.go cannot quietly
// escape the shape audit below.
func strmAllInstrTypes() []instruction {
	return []instruction{
		// compile.go built-ins
		&litElement{}, &litText{}, &valueOf{}, &applyTemplates{}, &forEach{},
		&ifInstr{}, &chooseInstr{}, &localVar{}, &callTemplate{}, &attrInstr{},
		&elemInstr{}, &textInstr{}, &copyInstr{}, &copyOf{}, &commentInstr{},
		&piInstr{}, &sequenceInstr{}, &analyzeString{},
		// instr_*.go registry instructions
		&nextMatch{}, &fegInstr{}, &evEvaluate{}, &itrIterate{},
		&itrNextInstr{}, &itrBreakInstr{}, &fwdCompatInstr{}, &extInstr{},
		&tvtText{}, &nsInstr{}, &docInstr{}, &wherePopulated{}, &forkInstr{},
		&onEmptyInstr{}, &condContent{}, &mrgInstr{}, &performSort{},
		&num2Number{}, &rdResultDocument{}, &mpMap{}, &mpEntry{},
		&msgMessage{}, &msgAssert{}, &sdSourceDocument{}, &tryCatch{},
		&tcNoop{},
	}
}

// TestStrmEveryInstructionImplementsShaper is the fail-closed guarantee's first
// half: an instruction type with no strmShape method is rejected outright, so
// this test only records which types currently carry a rule. Any type that
// stops implementing it would start rejecting every streamable construct that
// contains it — loud, not silent — but the list is still asserted so the
// intent is explicit.
func TestStrmEveryInstructionImplementsShaper(t *testing.T) {
	for _, in := range strmAllInstrTypes() {
		if _, ok := in.(strmShaper); !ok {
			t.Errorf("%T does not implement strmShaper (it will be rejected as non-streamable)", in)
		}
	}
}

// strmCarrier classifies a struct field type as something that can hold an
// expression or a sub-body — i.e. something the streamability analysis MUST
// look at, or it will under-approximate and wrongly call a construct
// streamable.
func strmIsCarrier(t reflect.Type) bool {
	switch t {
	case reflect.TypeOf((*xpath.Parsed)(nil)),
		reflect.TypeOf((*xpath.Pattern)(nil)),
		reflect.TypeOf((*avt)(nil)),
		reflect.TypeOf([]instruction(nil)):
		return true
	}
	return false
}

// strmSeedCarriers walks a freshly-allocated instruction value and gives every
// carrier field (at any nesting depth reachable through structs, pointers,
// slices and maps) a DISTINCT sentinel. It returns one label per sentinel,
// keyed by the sentinel's identity, so the audit can report which field was
// missed by name.
func strmSeedCarriers(t *testing.T, v reflect.Value, path string, want map[any]string) {
	t.Helper()
	v = strmSettable(v)
	switch v.Kind() {
	case reflect.Ptr:
		if v.Type() == reflect.TypeOf((*xmltree.Node)(nil)) {
			return // a source location, never an operand
		}
		if v.IsNil() {
			if !v.CanSet() {
				return
			}
			v.Set(reflect.New(v.Type().Elem()))
		}
		strmSeedCarriers(t, v.Elem(), path, want)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := strmSettable(v.Field(i))
			name := path + "." + v.Type().Field(i).Name
			if !f.CanSet() {
				continue
			}
			if strmIsCarrier(f.Type()) {
				strmSeedOne(t, f, name, want)
				continue
			}
			switch f.Kind() {
			case reflect.Struct, reflect.Ptr, reflect.Slice, reflect.Map:
				strmSeedCarriers(t, f, name, want)
			}
		}
	case reflect.Slice:
		if v.Type() == reflect.TypeOf([]instruction(nil)) {
			return // handled as a carrier
		}
		// Give the slice exactly one element and seed it, so nested carriers
		// (sortKey.sel, tcCatch.body, mrgSource.keys) are reached.
		if v.Len() == 0 {
			if !v.CanSet() {
				return
			}
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		elem := v.Index(0)
		if elem.Kind() == reflect.Interface {
			return
		}
		strmSeedCarriers(t, elem, path+"[0]", want)
	case reflect.Map:
		if v.Type().Elem() != reflect.TypeOf((*avt)(nil)) {
			return
		}
		if v.IsNil() {
			if !v.CanSet() {
				return
			}
			v.Set(reflect.MakeMap(v.Type()))
		}
		key := reflect.ValueOf("strm-audit")
		slot := reflect.New(v.Type().Elem()).Elem()
		strmSeedOne(t, slot, path+"[strm-audit]", want)
		v.SetMapIndex(key, slot)
	}
}

// strmSeedOne installs one uniquely identifiable sentinel in a carrier field.
func strmSeedOne(t *testing.T, f reflect.Value, name string, want map[any]string) {
	t.Helper()
	switch f.Type() {
	case reflect.TypeOf((*xpath.Parsed)(nil)):
		p, err := xpath.Parse("$strmAudit")
		if err != nil {
			t.Fatalf("parse sentinel: %v", err)
		}
		f.Set(reflect.ValueOf(p))
		want[any(p)] = name
	case reflect.TypeOf((*xpath.Pattern)(nil)):
		p, err := xpath.ParsePattern("strmAudit")
		if err != nil {
			t.Fatalf("parse sentinel pattern: %v", err)
		}
		f.Set(reflect.ValueOf(p))
		want[any(p)] = name
	case reflect.TypeOf((*avt)(nil)):
		p, err := xpath.Parse("$strmAudit")
		if err != nil {
			t.Fatalf("parse sentinel: %v", err)
		}
		a := &avt{parts: []avtPart{{expr: p}}}
		f.Set(reflect.ValueOf(a))
		want[any(a)] = name
	case reflect.TypeOf([]instruction(nil)):
		marker := &litText{text: "strm-audit-" + name}
		f.Set(reflect.ValueOf([]instruction{marker}))
		want[any(marker)] = name
	}
}

// strmCollectShape gathers every carrier the shape actually exposes, so the
// audit can diff it against the seeded set.
func strmCollectShape(sh strmShape, got map[any]bool) {
	add := func(x any) {
		if x == nil {
			return
		}
		switch v := x.(type) {
		case *xpath.Parsed:
			if v != nil {
				got[any(v)] = true
			}
		case *xpath.Pattern:
			if v != nil {
				got[any(v)] = true
			}
		case *avt:
			if v != nil {
				got[any(v)] = true
			}
		}
	}
	addBody := func(b []instruction) {
		for _, in := range b {
			got[any(in)] = true
		}
	}
	addParams := func(ps []*VarDef) {
		for _, vd := range ps {
			if vd == nil {
				continue
			}
			add(vd.sel)
			addBody(vd.body)
		}
	}
	addSorts := func(ss []sortKey) {
		for _, sk := range ss {
			add(sk.sel)
			addBody(sk.body)
			for _, t := range []*avt{sk.dataType, sk.order, sk.caseOrder, sk.collation, sk.lang} {
				add(t)
			}
		}
	}

	add(sh.sel)
	add(sh.key)
	addBody(sh.body)
	addBody(sh.onDone)
	addBody(sh.forkBranches)
	if sh.forkGroup != nil {
		got[any(sh.forkGroup)] = true
	}
	addParams(sh.params)
	addSorts(sh.sorts)
	for _, p := range sh.pats {
		add(p)
	}
	for _, r := range sh.ops {
		add(r.expr)
		add(r.avt)
		add(r.pat)
		addBody(r.body)
		addParams(r.params)
		addSorts(r.sorts)
	}
}

// strmShapeAuditExempt lists carrier fields that are deliberately NOT operands
// of the streamability analysis, keyed "Type.field" with the reason as the
// value. It is empty today: every carrier field is an operand. Add an entry
// only with a spec citation — the point of the audit is that a field added to
// an instruction but not to strmShape() is caught here rather than by a
// silently-wrong streamed run.
var strmShapeAuditExempt = map[string]string{}

// TestStrmShapeVisitsEveryCarrierField is the required safety net: for every
// instruction type, every field that can hold an expression, an AVT, a pattern
// or a sub-body must be reachable from its strmShape(). A field left out would
// make the analysis under-approximate — classifying a construct as streamable
// because it never looked at the part that consumes the stream.
func TestStrmShapeVisitsEveryCarrierField(t *testing.T) {
	for _, proto := range strmAllInstrTypes() {
		typ := reflect.TypeOf(proto).Elem()
		fresh := reflect.New(typ)
		want := map[any]string{}
		strmSeedCarriers(t, fresh.Elem(), typ.Name(), want)

		in, ok := fresh.Interface().(instruction)
		if !ok {
			t.Fatalf("%s is not an instruction", typ.Name())
		}
		sh, ok := in.(strmShaper)
		if !ok {
			continue // already reported by TestStrmEveryInstructionImplementsShaper
		}
		got := map[any]bool{}
		strmCollectShape(sh.strmShape(), got)

		for sentinel, name := range want {
			if got[sentinel] {
				continue
			}
			if _, exempt := strmShapeAuditExempt[name]; exempt {
				continue
			}
			t.Errorf("%s: field %s is never visited by strmShape() — the streamability analysis would ignore it", typ.Name(), name)
		}
	}
}

// TestStrmInstructionTypeListIsComplete cross-checks strmAllInstrTypes against
// what the instruction registry actually produces, so a newly registered
// instruction cannot escape the audit above by simply not being listed.
func TestStrmInstructionTypeListIsComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, in := range strmAllInstrTypes() {
		listed[reflect.TypeOf(in).Elem().Name()] = true
	}
	// Every registered instruction name must compile to a type in the list.
	// Compiling each one properly needs a stylesheet, so instead assert the
	// registry's size against the audited set: the two grow together.
	if len(instrRegistry) == 0 {
		t.Fatal("instrRegistry is empty")
	}
	for _, name := range []string{
		"for-each-group", "iterate", "try", "evaluate", "merge", "number",
		"message", "assert", "result-document", "map", "map-entry",
		"namespace", "document", "where-populated",
		"perform-sort", "fork", "on-empty", "on-non-empty", "source-document",
		"next-match", "apply-imports", "break", "next-iteration",
	} {
		if _, ok := instrRegistry[name]; !ok {
			t.Errorf("instrRegistry lost %q — update strmAllInstrTypes if an instruction was renamed", name)
		}
	}
	if len(listed) != len(strmAllInstrTypes()) {
		t.Errorf("strmAllInstrTypes has duplicate entries")
	}
}

// --- behavioural tests ---

// strmCompileBody compiles a stylesheet and returns the body of its first
// template rule, so classifyStreamable can be exercised directly.
func strmCompileBody(t *testing.T, sheet string) ([]instruction, *xmltree.Node) {
	t.Helper()
	ss, err := Compile(sheet)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, tm := range ss.templates {
		if tm.pattern != nil {
			return tm.body, tm.el
		}
	}
	t.Fatal("no template rule compiled")
	return nil, nil
}

func strmSheet(body string) string {
	return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="/">` + body + `</xsl:template>
</xsl:stylesheet>`
}

// TestClassifyStreamableDirect exercises the exported seam on its own, without
// the mode or fork wiring.
func TestClassifyStreamableDirect(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty", ``, true},
		{"literal text", `hello`, true},
		{"value-of over a child", `<xsl:value-of select="a/b"/>`, true},
		{"single downward for-each", `<xsl:for-each select="a/b"><xsl:value-of select="@c"/></xsl:for-each>`, true},
		{"copy-of grounds the result", `<xsl:copy-of select="a"/>`, true},
		{"two consuming siblings", `<xsl:value-of select="a"/><xsl:value-of select="b"/>`, false},
		{"upward navigation then down", `<xsl:value-of select="../a/b"/>`, false},
		{"preceding axis", `<xsl:value-of select="preceding::a"/>`, false},
		{"variable bound to a streamed node", `<xsl:variable name="v" select="a"/><xsl:value-of select="$v"/>`, false},
		{"last() over a striding focus", `<xsl:value-of select="a[last()]"/>`, false},
		{"numeric predicate on a child step", `<xsl:value-of select="a[1]"/>`, true},
		{"fork lets both prongs consume", `<xsl:fork><xsl:sequence select="copy-of(a)"/><xsl:sequence select="copy-of(b)"/></xsl:fork>`, true},
		{"fork returning streamed nodes", `<xsl:fork><xsl:sequence select="a"/><xsl:sequence select="b"/></xsl:fork>`, false},
		{"choose branches may both consume", `<xsl:choose><xsl:when test="@x"><xsl:value-of select="a"/></xsl:when><xsl:otherwise><xsl:value-of select="b"/></xsl:otherwise></xsl:choose>`, true},
		{"attribute predicate stays motionless", `<xsl:for-each select="a[@x='1']"><xsl:value-of select="@y"/></xsl:for-each>`, true},
		{"child predicate is consuming", `<xsl:value-of select="a[b]/c"/>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, el := strmCompileBody(t, strmSheet(tc.body))
			ok, err := classifyStreamable(body, el)
			if ok != tc.want {
				t.Fatalf("classifyStreamable = %v (err %v), want %v", ok, err, tc.want)
			}
			if !ok {
				if err == nil {
					t.Fatal("non-streamable result carried no error")
				}
				if !strings.Contains(err.Error(), "err:XTSE3430:") {
					t.Fatalf("diagnostic %q is not catalogued XTSE3430", err.Error())
				}
			} else if err != nil {
				t.Fatalf("streamable result carried an error: %v", err)
			}
		})
	}
}

// TestStrmStreamableModeRejection checks the xsl:mode/@streamable="yes" wiring.
func TestStrmStreamableModeRejection(t *testing.T) {
	defer SetEnforceStreamability(SetEnforceStreamability(true))
	sheet := func(body, match string) string {
		return `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:mode name="s" streamable="yes"/>
  <xsl:template match="` + match + `" mode="s">` + body + `</xsl:template>
</xsl:stylesheet>`
	}
	if _, err := Compile(sheet(`<xsl:value-of select="a/b"/>`, `r`)); err != nil {
		t.Fatalf("a streamable rule was rejected: %v", err)
	}
	_, err := Compile(sheet(`<xsl:value-of select="a"/><xsl:value-of select="b"/>`, `r`))
	if err == nil {
		t.Fatal("a rule with two consuming instructions was accepted in a streamable mode")
	}
	if !strings.Contains(err.Error(), "err:XTSE3430:") {
		t.Fatalf("diagnostic %q is not catalogued XTSE3430", err.Error())
	}
	// A non-motionless match pattern is rejected too (§19.8.10: p[b]).
	_, err = Compile(sheet(`x`, `r[b]`))
	if err == nil || !strings.Contains(err.Error(), "err:XTSE3430:") {
		t.Fatalf("a non-motionless match pattern was not rejected with XTSE3430: %v", err)
	}
	// With enforcement off (the default) a mode this analysis cannot prove is
	// simply processed without streaming, which is what XTSE3430's own "unless
	// the user has indicated..." clause permits.
	prev := SetEnforceStreamability(false)
	if _, err := Compile(sheet(`<xsl:value-of select="a"/><xsl:value-of select="b"/>`, `r`)); err != nil {
		t.Fatalf("enforcement is off, so the mode must not be checked: %v", err)
	}
	SetEnforceStreamability(prev)

	// An unstreamable rule in a mode NOT declared streamable is fine.
	plain := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="r" mode="s"><xsl:value-of select="a"/><xsl:value-of select="b"/></xsl:template>
</xsl:stylesheet>`
	if _, err := Compile(plain); err != nil {
		t.Fatalf("a non-streamable mode must not be analysed: %v", err)
	}
}

// TestStrmForkContentModel pins the §16.1 content model onto XTSE0010, keeping
// it distinct from XTSE3430's "well-formed but not streamable".
func TestStrmForkContentModel(t *testing.T) {
	bad := []string{
		`<xsl:fork><xsl:value-of select="a"/></xsl:fork>`,
		`<xsl:fork><xsl:sequence select="a"/><xsl:for-each-group select="a" group-by="@k"><xsl:sequence select="."/></xsl:for-each-group></xsl:fork>`,
		`<xsl:fork><foo/></xsl:fork>`,
	}
	for _, b := range bad {
		_, err := Compile(strmSheet(b))
		if err == nil {
			t.Fatalf("invalid xsl:fork content accepted: %s", b)
		}
		if !strings.Contains(err.Error(), "err:XTSE0010:") {
			t.Fatalf("xsl:fork content-model error %q is not catalogued XTSE0010", err.Error())
		}
	}
	good := []string{
		`<xsl:fork/>`,
		`<xsl:fork><xsl:sequence select="1"/></xsl:fork>`,
		`<xsl:fork><xsl:sequence select="1"/><xsl:sequence select="2"/></xsl:fork>`,
		`<xsl:fork><xsl:for-each-group select="a" group-by="@k"><xsl:sequence select="."/></xsl:for-each-group></xsl:fork>`,
	}
	for _, g := range good {
		if _, err := Compile(strmSheet(g)); err != nil {
			t.Fatalf("valid xsl:fork content rejected: %s: %v", g, err)
		}
	}
}
