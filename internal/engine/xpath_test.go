package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXPathHostMapArrayDisplay(t *testing.T) {
	cases := []struct {
		expr     string
		wantType string
		wantVal  string
	}{
		{`map{"a":1,"b":2}`, "map", `map{"a":1,"b":2}`},
		{`[1,2,3]`, "array", `[1,2,3]`},
		{`map{"n":"Tim","tags":("x","y")}`, "map", `map{"n":"Tim","tags":("x","y")}`},
		{`[map{"k":1}, 2]`, "array", `[map{"k":1},2]`},
		{`1 + 2`, "xs:integer", "3"},
		{`"hello"`, "xs:string", "hello"},
	}
	for _, c := range cases {
		res := EvalXPath(XPathRequest{Expression: c.expr})
		if res.HasErrors() {
			t.Errorf("%s: unexpected error %+v", c.expr, res.Diagnostics)
			continue
		}
		if len(res.Items) != 1 {
			t.Errorf("%s: got %d items, want 1", c.expr, len(res.Items))
			continue
		}
		it := res.Items[0]
		if it.Type != c.wantType || it.Value != c.wantVal {
			t.Errorf("%s: got type=%q value=%q; want type=%q value=%q",
				c.expr, it.Type, it.Value, c.wantType, c.wantVal)
		}
		if res.Value != c.wantVal {
			t.Errorf("%s: whole-sequence Value=%q, want %q", c.expr, res.Value, c.wantVal)
		}
	}
}

func TestXPathBaseDirResolvesDocAndUnparsedText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.xml"), []byte(`<r><v>42</v></r>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := EvalXPath(XPathRequest{Expression: `doc('other.xml')/r/v/string()`, BaseDir: dir})
	if res.HasErrors() {
		t.Fatalf("doc(): unexpected error %+v", res.Diagnostics)
	}
	if res.Value != "42" {
		t.Fatalf("doc(): got %q, want %q", res.Value, "42")
	}

	res = EvalXPath(XPathRequest{Expression: `unparsed-text('notes.txt')`, BaseDir: dir})
	if res.HasErrors() {
		t.Fatalf("unparsed-text(): unexpected error %+v", res.Diagnostics)
	}
	if res.Value != "hello world" {
		t.Fatalf("unparsed-text(): got %q, want %q", res.Value, "hello world")
	}
}

func TestXPathNoBaseDirLeavesDocUnavailable(t *testing.T) {
	res := EvalXPath(XPathRequest{Expression: `doc-available('other.xml')`})
	if res.HasErrors() {
		t.Fatalf("unexpected error %+v", res.Diagnostics)
	}
	if res.Value != "false" {
		t.Fatalf("got %q, want %q (no BaseDir => no resolver)", res.Value, "false")
	}
}
