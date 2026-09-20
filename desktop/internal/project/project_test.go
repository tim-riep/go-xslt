package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesFreshManifestForNewDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myproj")
	p, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Name != "myproj" {
		t.Fatalf("Name = %q, want %q", p.Manifest.Name, "myproj")
	}
	if p.Manifest.Schema != CurrentSchema {
		t.Fatalf("Schema = %d, want %d", p.Manifest.Schema, CurrentSchema)
	}
	if len(p.Manifest.Runs) != 0 {
		t.Fatalf("Runs = %+v, want empty", p.Manifest.Runs)
	}
	if p.Tree == nil {
		t.Fatal("Tree is nil")
	}
}

func TestSaveOpenRoundTrip(t *testing.T) {
	root := t.TempDir()
	m := Manifest{
		Name: "Invoice pipeline",
		Runs: []Run{
			{ID: "r1", Name: "Render", Kind: RunTransform, Stylesheet: "xsl/main.xsl", Source: "samples/in.xml",
				Params: map[string]string{"lang": "de"}},
			{ID: "r2", Name: "Validate UBL", Kind: RunValidate, Folder: "Validation",
				Schemas: []string{"xsd/a.xsd", "xsd/b.xsd"}, Instance: "samples/in.xml", XSDVersion: "1.1"},
			{ID: "r3", Name: "Probe", Kind: RunXPath, Expression: "//x", ContextDoc: "samples/in.xml"},
		},
		UI: UIState{Expanded: []string{"xsl", "xsd"}},
	}
	if err := Save(root, m); err != nil {
		t.Fatal(err)
	}

	p, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Name != m.Name {
		t.Fatalf("Name = %q, want %q", p.Manifest.Name, m.Name)
	}
	if len(p.Manifest.Runs) != 3 {
		t.Fatalf("Runs = %+v, want 3 entries", p.Manifest.Runs)
	}
	v := p.Manifest.Runs[1]
	if len(v.Schemas) != 2 || v.Schemas[0] != "xsd/a.xsd" || v.Schemas[1] != "xsd/b.xsd" {
		t.Fatalf("validate run Schemas = %+v, want ordered [xsd/a.xsd xsd/b.xsd]", v.Schemas)
	}
	if len(p.Manifest.UI.Expanded) != 2 {
		t.Fatalf("UI.Expanded = %+v", p.Manifest.UI.Expanded)
	}
}

func TestMigrateLegacyWorkspace(t *testing.T) {
	root := t.TempDir()
	legacy := `{
		"name": "Old workspace",
		"files": [{"path":"transform.xsl","role":"stylesheet"},{"path":"input.xml","role":"source"}],
		"params": {"debug":"true"},
		"main": {"stylesheet":"transform.xsl","source":"input.xml"}
	}`
	if err := os.WriteFile(filepath.Join(root, legacyManifestName), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, "transform.xsl", SampleStylesheet)
	mustWrite(t, root, "input.xml", SampleSource)

	p, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Name != "Old workspace" {
		t.Fatalf("Name = %q, want %q", p.Manifest.Name, "Old workspace")
	}
	if len(p.Manifest.Runs) != 1 {
		t.Fatalf("Runs = %+v, want exactly one migrated transform run", p.Manifest.Runs)
	}
	r := p.Manifest.Runs[0]
	if r.Kind != RunTransform || r.Stylesheet != "transform.xsl" || r.Source != "input.xml" {
		t.Fatalf("migrated run = %+v", r)
	}
	if r.Params["debug"] != "true" {
		t.Fatalf("migrated params = %+v", r.Params)
	}

	// The legacy file must survive untouched, and re-opening must not
	// re-migrate (goxslt.project.json now exists and takes precedence).
	if _, err := os.Stat(filepath.Join(root, legacyManifestName)); err != nil {
		t.Fatal("legacy manifest was removed, want it left in place")
	}
	if err := Save(root, Manifest{Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	p2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Manifest.Name != "Renamed" || len(p2.Manifest.Runs) != 0 {
		t.Fatalf("re-open after Save = %+v, want the saved manifest, not a re-migration", p2.Manifest)
	}
}

func TestCreateProjectIsUnique(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p1, err := CreateProject("Demo")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := CreateProject("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if p1.Dir == p2.Dir {
		t.Fatalf("two projects named %q collided at %q", "Demo", p1.Dir)
	}
	if len(p1.Manifest.Runs) != 1 || p1.Manifest.Runs[0].Kind != RunTransform {
		t.Fatalf("seeded project runs = %+v", p1.Manifest.Runs)
	}
	got, err := Read(p1.Dir, "transform.xsl")
	if err != nil || got != SampleStylesheet {
		t.Fatalf("seeded stylesheet missing or wrong: %v", err)
	}
}
