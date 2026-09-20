package main

import (
	"os/exec"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/tim-riep/go-xslt/desktop/internal/project"
)

// ProjectService is the Wails service backing the Postman-like project
// workbench: a project registry, a per-project file tree, file CRUD, and the
// saved-run manifest. Actually running a saved run still goes through
// XsltService.RunTransform/RunValidate/RunXPath — this service only manages
// the project's files and metadata.
type ProjectService struct{}

// ListProjects returns known projects (newest-opened first) for the sidebar
// project switcher.
func (s *ProjectService) ListProjects() []project.Ref {
	return project.List()
}

// CreateProject creates a new, seeded project inside the managed library
// folder and opens it.
func (s *ProjectService) CreateProject(name string) (*project.Project, error) {
	return project.CreateProject(name)
}

// PickFolder opens a native folder picker and returns the chosen path (empty
// if the user cancels) — used both for "Open folder as project" and any
// other folder-selection need.
func (s *ProjectService) PickFolder() (string, error) {
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("Select project folder").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// OpenProject loads (creating if necessary) the project at dir: its
// manifest — migrating a legacy goxslt.workspace.json if that's all that's
// present — plus a fresh file tree scan. It also registers dir in the
// project registry.
func (s *ProjectService) OpenProject(dir string) (*project.Project, error) {
	return project.Open(dir)
}

// ForgetProject removes dir from the project registry without touching
// anything on disk.
func (s *ProjectService) ForgetProject(dir string) {
	project.Forget(dir)
}

// RefreshTree rescans dir's file tree, picking up changes made outside the
// app (git checkout, another editor, …).
func (s *ProjectService) RefreshTree(dir string) (*project.Node, error) {
	return project.Scan(dir)
}

// ReadFile returns the content of the file at rel within the project rooted
// at dir.
func (s *ProjectService) ReadFile(dir, rel string) (string, error) {
	return project.Read(dir, rel)
}

// WriteFile overwrites the file at rel with content — the autosave
// primitive; the frontend debounces calls per path and flushes pending
// writes before every run.
func (s *ProjectService) WriteFile(dir, rel, content string) error {
	return project.Write(dir, rel, content)
}

// CreateFile creates a new file at rel seeded with content. It fails if
// something already exists there.
func (s *ProjectService) CreateFile(dir, rel, content string) error {
	return project.CreateFile(dir, rel, content)
}

// CreateFolder creates a new folder at rel (and any missing parents).
func (s *ProjectService) CreateFolder(dir, rel string) error {
	return project.CreateFolder(dir, rel)
}

// Rename moves the file or folder at fromRel to toRel within the project
// rooted at dir (also used for drag-and-drop moves between folders).
func (s *ProjectService) Rename(dir, fromRel, toRel string) error {
	return project.Rename(dir, fromRel, toRel)
}

// Delete permanently removes the file or folder at rel. There is no undo —
// the frontend must confirm with the user first.
func (s *ProjectService) Delete(dir, rel string) error {
	return project.Delete(dir, rel)
}

// Duplicate copies the file at rel to an automatically-numbered sibling path
// and returns the new project-relative path.
func (s *ProjectService) Duplicate(dir, rel string) (string, error) {
	return project.Duplicate(dir, rel)
}

// SaveManifest persists the project's manifest (name, saved runs, UI state).
func (s *ProjectService) SaveManifest(dir string, m project.Manifest) error {
	return project.Save(dir, m)
}

// Reveal shows the file or folder at rel in the platform file manager
// (Finder on macOS, Explorer on Windows), reusing the same containment-
// checked path resolution as every other file operation so this can't be
// used to reveal something outside the project.
func (s *ProjectService) Reveal(dir, rel string) error {
	full, err := project.Resolve(dir, rel)
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", full).Run()
	case "windows":
		// explorer's exit code is unreliable even on success; ignore it.
		_ = exec.Command("explorer", "/select,"+full).Run()
		return nil
	default:
		return exec.Command("xdg-open", full).Run()
	}
}
