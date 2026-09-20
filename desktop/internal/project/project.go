// Package project handles go-xslt desktop projects: a project is a real
// directory on disk containing arbitrary XSLT/XML/XSD files plus a manifest
// (goxslt.project.json) that records the project's name and its saved runs
// (reusable transform/validate/xpath configurations, the Postman-"request"
// analogue). The file tree itself is not stored in the manifest — it is
// scanned from disk on demand (see tree.go) so external changes (git, another
// editor) are always reflected.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ManifestName is the fixed filename of a project manifest inside its folder.
const ManifestName = "goxslt.project.json"

// legacyManifestName is the v1 single-workspace manifest this package
// migrates on first open (see migrateWorkspace).
const legacyManifestName = "goxslt.workspace.json"

// CurrentSchema is the manifest schema version written by this package.
const CurrentSchema = 2

// RunKind identifies which engine entry point a saved run drives.
type RunKind string

const (
	RunTransform RunKind = "transform"
	RunValidate  RunKind = "validate"
	RunXPath     RunKind = "xpath"
)

// Run is one saved, reusable run configuration — the Postman "request"
// analogue. Which fields are meaningful depends on Kind. Paths are always
// project-relative, slash-separated.
type Run struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Kind   RunKind `json:"kind"`
	Folder string  `json:"folder,omitempty"` // sidebar grouping, "" = ungrouped

	// transform
	Stylesheet      string            `json:"stylesheet,omitempty"`
	Source          string            `json:"source,omitempty"`
	Params          map[string]string `json:"params,omitempty"`
	InitialTemplate string            `json:"initialTemplate,omitempty"`

	// validate. Schemas is an ordered multi-file schema set for xs:import/
	// xs:include/xs:redefine across real project files; when it's empty, the
	// UI instead offers a single inline schema (SchemaInline) typed directly
	// into the run itself, no project file required — the quick-validate
	// path. Instance works the same way against InstanceInline.
	Schemas        []string `json:"schemas,omitempty"`
	SchemaInline   string   `json:"schemaInline,omitempty"`
	Instance       string   `json:"instance,omitempty"`
	InstanceInline string   `json:"instanceInline,omitempty"`
	XSDVersion     string   `json:"xsdVersion,omitempty"` // "1.0" | "1.1"

	// xpath. ContextDoc is a project file reference; when empty, the UI
	// offers inline context text (ContextInline) instead — same quick-start
	// shape as validate's InstanceInline.
	Expression    string `json:"expression,omitempty"`
	ContextDoc    string `json:"contextDoc,omitempty"` // "" = no context document
	ContextInline string `json:"contextInline,omitempty"`
}

// UIState is small persisted UI state that isn't worth losing across
// restarts but also isn't meaningful engine input.
type UIState struct {
	Expanded []string `json:"expanded,omitempty"` // expanded folder paths in the file tree
}

// Manifest is the on-disk project description.
type Manifest struct {
	Schema int     `json:"schema"`
	Name   string  `json:"name"`
	Runs   []Run   `json:"runs"`
	UI     UIState `json:"ui"`
}

// Project is a loaded manifest plus its absolute folder path and scanned file
// tree.
type Project struct {
	Dir      string   `json:"dir"`
	Manifest Manifest `json:"manifest"`
	Tree     *Node    `json:"tree"`
}

// Open loads the project rooted at dir: its manifest (migrating a legacy
// goxslt.workspace.json if that's all that's present) plus a fresh directory
// scan. dir is created if it does not yet exist, so opening doubles as
// "create at this exact path."
func Open(dir string) (*Project, error) {
	if dir == "" {
		return nil, errors.New("empty project directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create project dir: %w", err)
	}

	m, err := loadManifest(abs)
	if err != nil {
		return nil, err
	}

	tree, err := Scan(abs)
	if err != nil {
		return nil, fmt.Errorf("scan project tree: %w", err)
	}

	touchRecent(abs, m.Name)
	return &Project{Dir: abs, Manifest: *m, Tree: tree}, nil
}

// loadManifest reads goxslt.project.json, or migrates a legacy
// goxslt.workspace.json, or returns a fresh empty manifest named after the
// directory if neither exists.
func loadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err == nil {
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		if m.Runs == nil {
			m.Runs = []Run{}
		}
		m.Schema = CurrentSchema
		return &m, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	if m, migrated, mErr := migrateWorkspace(dir); mErr == nil && migrated {
		return m, nil
	}

	return &Manifest{
		Schema: CurrentSchema,
		Name:   filepath.Base(dir),
		Runs:   []Run{},
	}, nil
}

// legacyManifest mirrors just enough of the v1 desktop/internal/workspace
// format to migrate it, without importing that package.
type legacyManifest struct {
	Name   string            `json:"name"`
	Params map[string]string `json:"params,omitempty"`
	Main   struct {
		Stylesheet string `json:"stylesheet"`
		Source     string `json:"source"`
	} `json:"main"`
}

// migrateWorkspace converts a v1 goxslt.workspace.json (one stylesheet + one
// source + one shared param set) into a single transform Run named
// "Transform". The legacy file is left in place, untouched, so this is
// idempotent and non-destructive; on subsequent opens goxslt.project.json
// exists and this path is never taken again.
func migrateWorkspace(dir string) (*Manifest, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, legacyManifestName))
	if err != nil {
		return nil, false, err
	}
	var lm legacyManifest
	if err := json.Unmarshal(raw, &lm); err != nil {
		return nil, false, fmt.Errorf("parse legacy manifest: %w", err)
	}

	name := lm.Name
	if name == "" {
		name = filepath.Base(dir)
	}
	m := &Manifest{
		Schema: CurrentSchema,
		Name:   name,
		Runs:   []Run{},
	}
	if lm.Main.Stylesheet != "" || lm.Main.Source != "" {
		m.Runs = append(m.Runs, Run{
			ID:         "transform-1",
			Name:       "Transform",
			Kind:       RunTransform,
			Stylesheet: lm.Main.Stylesheet,
			Source:     lm.Main.Source,
			Params:     lm.Params,
		})
	}
	return m, true, nil
}

// Save persists the manifest to dir/goxslt.project.json. File contents are
// not written here — the frontend autosaves individual files via
// WriteFile/CreateFile as they're edited (see fileops.go); Save only ever
// touches the manifest.
func Save(dir string, m Manifest) error {
	if dir == "" {
		return errors.New("project has no directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if m.Runs == nil {
		m.Runs = []Run{}
	}
	m.Schema = CurrentSchema
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
		return err
	}
	touchRecent(dir, m.Name)
	return nil
}
