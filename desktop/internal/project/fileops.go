package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrEscapesRoot is returned by any operation whose path (after cleaning, or
// after resolving symlinks) would land outside the project root.
var ErrEscapesRoot = errors.New("path escapes project root")

// resolve turns a project-relative, slash-separated path into an absolute
// filesystem path guaranteed to be inside root, or fails. This is the single
// choke point every exported function in this file goes through — a project
// root is the security boundary between "files this app may touch" and
// everything else on the user's disk, so no method may build a path any
// other way.
//
// Two independent checks, because either alone is insufficient: (1) clean
// the relative path with a synthetic leading slash so a literal ".." (or
// "a/../../etc") cannot walk above root textually, then (2) once symlinks
// are resolved — for an existing target directly, or for its nearest
// existing ancestor when the target itself doesn't exist yet, e.g. before a
// create — confirm the *real* path is still inside the *real* root. Step 2
// catches a symlink planted inside the project that points outside it,
// which step 1 cannot see.
func resolve(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(rel) || strings.ContainsRune(rel, '\\') || strings.Contains(rel, "\x00") {
		return "", ErrEscapesRoot
	}

	// path.Clean resolves "." and ".." lexically. Unlike clamping to root
	// (e.g. cleaning "/"+rel), an unbalanced ".." that would climb above rel
	// itself surfaces as a leading "../" in the cleaned result — reject that
	// outright rather than silently redirecting the caller at some other
	// path inside root, which would be a confusing way to fail a rename or
	// create.
	cleaned := path.Clean(rel)
	if cleaned == "" || cleaned == "." {
		return "", errors.New("empty path")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrEscapesRoot
	}

	rootReal, err := realRoot(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(rootReal, filepath.FromSlash(cleaned))
	if !withinRoot(rootReal, candidate) {
		return "", ErrEscapesRoot
	}

	real, err := realExistingPrefix(candidate)
	if err != nil {
		return "", err
	}
	if !withinRoot(rootReal, real) {
		return "", ErrEscapesRoot
	}
	return candidate, nil
}

// realRoot resolves symlinks on root itself; the project root is assumed to
// exist (Open creates it via os.MkdirAll).
func realRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	return real, nil
}

// realExistingPrefix resolves symlinks along p, walking up to the nearest
// existing ancestor when p itself doesn't exist (e.g. a path about to be
// created), and rejoins the not-yet-existing suffix onto the resolved
// ancestor.
func realExistingPrefix(p string) (string, error) {
	suffix := ""
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if suffix == "" {
				return real, nil
			}
			return filepath.Join(real, suffix), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %q", p)
		}
		suffix = filepath.Join(filepath.Base(cur), suffix)
		cur = parent
	}
}

func withinRoot(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// Resolve exposes the same containment-checked path resolution every method
// in this file uses, for callers (e.g. "reveal in file manager") that need
// the real absolute path of a project-relative one without performing I/O.
func Resolve(root, rel string) (string, error) {
	return resolve(root, rel)
}

// Read returns the content of the file at rel (project-relative).
func Read(root, rel string) (string, error) {
	full, err := resolve(root, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Write overwrites (or creates) the file at rel with content, creating any
// missing parent directories. This is both the "New file" and the autosave
// primitive — the frontend debounces calls per path.
func Write(root, rel, content string) error {
	full, err := resolve(root, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// CreateFile creates a new, empty-or-seeded file at rel. It fails if
// something already exists there, so it never silently clobbers a file the
// tree scan just showed the user.
func CreateFile(root, rel, content string) error {
	full, err := resolve(root, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", rel)
		}
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// CreateFolder creates a new, empty directory at rel (and any missing
// parents). It is not an error if the folder already exists, matching
// os.MkdirAll and the common "New Folder" idempotent-retry UX.
func CreateFolder(root, rel string) error {
	full, err := resolve(root, rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0o755)
}

// Rename moves the file or folder at fromRel to toRel (also used for
// drag-and-drop moves between folders, since both are just a path change).
// It fails if the destination already exists.
func Rename(root, fromRel, toRel string) error {
	fromFull, err := resolve(root, fromRel)
	if err != nil {
		return err
	}
	toFull, err := resolve(root, toRel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(toFull); err == nil {
		return fmt.Errorf("%s already exists", toRel)
	}
	if err := os.MkdirAll(filepath.Dir(toFull), 0o755); err != nil {
		return err
	}
	return os.Rename(fromFull, toFull)
}

// Delete permanently removes the file or folder at rel (recursively for a
// folder). There is no undo and no OS trash involved — callers must confirm
// with the user before calling this; the project.Manifest itself is
// untouched, so a saved run that referenced the deleted path simply starts
// pointing at a missing file (surfaced by the frontend, not this package).
func Delete(root, rel string) error {
	full, err := resolve(root, rel)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}

// Duplicate copies the file at rel to a sibling path with the first
// available "name copy", "name copy 2", … suffix (before the extension) and
// returns the new project-relative path. Folders are not duplicable via this
// function (v1 scope: files only).
func Duplicate(root, rel string) (string, error) {
	full, err := resolve(root, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a folder", rel)
	}

	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	ext := path.Ext(rel)
	base := strings.TrimSuffix(path.Base(rel), ext)

	for n := 0; ; n++ {
		name := base + " copy" + ext
		if n > 0 {
			name = base + " copy " + strconv.Itoa(n+1) + ext
		}
		candidateRel := name
		if dir != "" {
			candidateRel = path.Join(dir, name)
		}
		candidateFull, err := resolve(root, candidateRel)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(candidateFull); err == nil {
			continue // already taken, try the next suffix
		}
		if err := copyFile(full, candidateFull); err != nil {
			return "", err
		}
		return candidateRel, nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
