package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Ref is one entry in the project registry: enough to list and reopen a
// project without scanning its tree.
type Ref struct {
	Dir        string    `json:"dir"`
	Name       string    `json:"name"`
	LastOpened time.Time `json:"lastOpened"`
}

func registryPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "go-xslt", "projects.json"), nil
}

// LibraryRoot is where new, app-managed projects are created by default:
// ~/Documents/go-xslt, falling back to a directory under the user config
// dir if the home documents folder can't be determined.
func LibraryRoot() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Documents", "go-xslt")
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		return filepath.Join(cfg, "go-xslt", "projects")
	}
	return filepath.Join(".", "go-xslt-projects")
}

func loadRegistry() []Ref {
	p, err := registryPath()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var refs []Ref
	_ = json.Unmarshal(raw, &refs)
	return refs
}

func saveRegistry(refs []Ref) {
	p, err := registryPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, raw, 0o644)
}

// touchRecent records dir (with its current display name) as opened/saved
// just now, moving it to the front of the registry.
func touchRecent(dir, name string) {
	refs := loadRegistry()
	out := []Ref{{Dir: dir, Name: name, LastOpened: time.Now()}}
	for _, r := range refs {
		if r.Dir != dir {
			out = append(out, r)
		}
	}
	saveRegistry(out)
}

// Forget removes dir from the registry without touching anything on disk —
// "remove from this list" for a project the user no longer wants to see in
// the sidebar, not a delete.
func Forget(dir string) {
	refs := loadRegistry()
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		if r.Dir != dir {
			out = append(out, r)
		}
	}
	saveRegistry(out)
}

// List returns known projects, newest-opened first, pruned of any directory
// that no longer exists on disk.
func List() []Ref {
	refs := loadRegistry()
	sort.Slice(refs, func(i, j int) bool { return refs[i].LastOpened.After(refs[j].LastOpened) })
	out := make([]Ref, 0, len(refs))
	changed := false
	for _, r := range refs {
		if info, err := os.Stat(r.Dir); err == nil && info.IsDir() {
			out = append(out, r)
		} else {
			changed = true
		}
	}
	if changed {
		saveRegistry(out)
	}
	return out
}
