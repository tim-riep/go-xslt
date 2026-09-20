package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanOrdersFoldersFirstThenCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "b.xml", "")
	mustWrite(t, root, "A.xsl", "")
	mustMkdir(t, root, "Zeta")
	mustMkdir(t, root, "alpha")

	node, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Children) != 4 {
		t.Fatalf("got %d children, want 4", len(node.Children))
	}
	names := make([]string, len(node.Children))
	for i, c := range node.Children {
		names[i] = c.Name
	}
	want := []string{"alpha", "Zeta", "A.xsl", "b.xml"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("children[%d] = %q, want %q (order: %v)", i, names[i], w, names)
		}
	}
	if !node.Children[0].Dir || !node.Children[1].Dir {
		t.Fatal("folders should be marked Dir=true and sort first")
	}
}

func TestScanSkipsDotfilesAndManifests(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, ".hidden", "")
	mustWrite(t, root, ManifestName, "{}")
	mustWrite(t, root, legacyManifestName, "{}")
	mustMkdir(t, root, "node_modules")
	mustWrite(t, root, "node_modules/x.js", "")
	mustWrite(t, root, "visible.xml", "")

	node, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Children) != 1 || node.Children[0].Name != "visible.xml" {
		t.Fatalf("children = %+v, want only visible.xml", node.Children)
	}
}

func TestScanAssignsFileKind(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "s.xsl", "")
	mustWrite(t, root, "s.xsd", "")
	mustWrite(t, root, "s.xml", "")
	mustWrite(t, root, "s.json", "")
	mustWrite(t, root, "s.bin", "")

	node, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]FileKind{}
	for _, c := range node.Children {
		kinds[c.Name] = c.Kind
	}
	want := map[string]FileKind{
		"s.xsl": KindStylesheet, "s.xsd": KindSchema, "s.xml": KindXML,
		"s.json": KindText, "s.bin": KindOther,
	}
	for name, k := range want {
		if kinds[name] != k {
			t.Errorf("kind(%s) = %q, want %q", name, kinds[name], k)
		}
	}
}

func TestScanNestedFolders(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "xsl/lib/common.xsl", "")

	node, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Children) != 1 || node.Children[0].Name != "xsl" {
		t.Fatalf("children = %+v", node.Children)
	}
	xsl := node.Children[0]
	if xsl.Path != "xsl" {
		t.Fatalf("xsl.Path = %q, want %q", xsl.Path, "xsl")
	}
	if len(xsl.Children) != 1 || xsl.Children[0].Name != "lib" {
		t.Fatalf("xsl.Children = %+v", xsl.Children)
	}
	lib := xsl.Children[0]
	if len(lib.Children) != 1 || lib.Children[0].Path != "xsl/lib/common.xsl" {
		t.Fatalf("lib.Children = %+v, want path xsl/lib/common.xsl", lib.Children)
	}
}

func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
}
