package project

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FileKind classifies a file by extension, purely to drive icons and
// default pickers in the UI — it has no effect on what the engine accepts.
type FileKind string

const (
	KindStylesheet FileKind = "stylesheet" // .xsl, .xslt
	KindSchema     FileKind = "schema"     // .xsd
	KindXML        FileKind = "xml"        // .xml
	KindText       FileKind = "text"       // .json, .txt, .csv, .html, .md
	KindOther      FileKind = "other"
)

// Node is one entry in a scanned project file tree. Path is project-relative
// and always slash-separated, regardless of host OS.
type Node struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Dir      bool     `json:"dir"`
	Kind     FileKind `json:"kind,omitempty"`
	Children []*Node  `json:"children,omitempty"`
}

// maxScanFileSize skips files too large to be reasonable stylesheet/schema/
// source documents; they still exist on disk, they simply don't appear in
// the tree (a project folder is not a general-purpose file manager).
const maxScanFileSize = 64 << 20 // 64 MiB

// skipNames are directory/file names never descended into or listed.
var skipNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
}

// Scan walks root and returns its file tree. Folders sort before files;
// within each group, entries sort case-insensitively by name. Dotfiles and
// entries in skipNames are omitted. root itself is not included as a node —
// the returned Node's Children are root's direct entries... actually Scan
// returns the synthetic root node itself so callers have one value to work
// with; its Path is "".
func Scan(root string) (*Node, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &os.PathError{Op: "scan", Path: root, Err: os.ErrInvalid}
	}
	node := &Node{Name: filepath.Base(root), Path: "", Dir: true}
	children, err := scanDir(root, "")
	if err != nil {
		return nil, err
	}
	node.Children = children
	return node, nil
}

func scanDir(absDir, relDir string) ([]*Node, error) {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}

	var nodes []*Node
	for _, e := range entries {
		name := e.Name()
		if name == "" || strings.HasPrefix(name, ".") || skipNames[name] {
			continue
		}
		if name == ManifestName || name == legacyManifestName {
			continue
		}

		relPath := name
		if relDir != "" {
			relPath = path.Join(relDir, name)
		}
		absPath := filepath.Join(absDir, name)

		if e.IsDir() {
			children, err := scanDir(absPath, relPath)
			if err != nil {
				// A directory we can't read (permissions, race with a
				// delete) is skipped rather than failing the whole scan.
				continue
			}
			nodes = append(nodes, &Node{Name: name, Path: relPath, Dir: true, Children: children})
			continue
		}

		info, err := e.Info()
		if err != nil || info.Size() > maxScanFileSize {
			continue
		}
		nodes = append(nodes, &Node{Name: name, Path: relPath, Kind: fileKind(name)})
	}

	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.Dir != b.Dir {
			return a.Dir // directories first
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return nodes, nil
}

func fileKind(name string) FileKind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".xsl", ".xslt":
		return KindStylesheet
	case ".xsd":
		return KindSchema
	case ".xml":
		return KindXML
	case ".json", ".txt", ".csv", ".html", ".htm", ".md":
		return KindText
	default:
		return KindOther
	}
}
