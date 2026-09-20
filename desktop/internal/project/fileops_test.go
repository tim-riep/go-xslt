package project

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"../escape.txt",
		"a/../../escape.txt",
		"../../../../etc/passwd",
		"/etc/passwd",
		"a/b/../../../escape.txt",
	}
	for _, rel := range cases {
		if _, err := resolve(root, rel); err == nil {
			t.Errorf("resolve(%q) succeeded, want an error", rel)
		}
	}
}

func TestResolveAllowsNormalPaths(t *testing.T) {
	root := t.TempDir()
	realRootPath, err := realRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"a.xsl", "sub/b.xml", "sub/deeper/c.xsd", "a/./b"} {
		full, err := resolve(root, rel)
		if err != nil {
			t.Fatalf("resolve(%q) = %v, want success", rel, err)
		}
		if !withinRoot(realRootPath, full) {
			t.Fatalf("resolve(%q) = %q, escaped root %q", rel, full, realRootPath)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	if _, err := resolve(root, "escape/secret.txt"); err == nil {
		t.Fatal("resolve through a symlink that escapes root succeeded, want an error")
	}
	if _, err := Read(root, "escape/secret.txt"); err == nil {
		t.Fatal("Read through a symlink that escapes root succeeded, want an error")
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, "a/b/c.xsl", "hello"); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root, "a/b/c.xsl")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestCreateFileFailsIfExists(t *testing.T) {
	root := t.TempDir()
	if err := CreateFile(root, "a.xml", "one"); err != nil {
		t.Fatal(err)
	}
	if err := CreateFile(root, "a.xml", "two"); err == nil {
		t.Fatal("CreateFile over an existing file succeeded, want an error")
	}
	got, _ := Read(root, "a.xml")
	if got != "one" {
		t.Fatalf("existing file was clobbered: got %q", got)
	}
}

func TestRenameMovesAndRejectsCollision(t *testing.T) {
	root := t.TempDir()
	if err := CreateFile(root, "old.xml", "x"); err != nil {
		t.Fatal(err)
	}
	if err := Rename(root, "old.xml", "sub/new.xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "old.xml"); err == nil {
		t.Fatal("old.xml still exists after rename")
	}
	got, err := Read(root, "sub/new.xml")
	if err != nil || got != "x" {
		t.Fatalf("Read(sub/new.xml) = %q, %v", got, err)
	}

	if err := CreateFile(root, "other.xml", "y"); err != nil {
		t.Fatal(err)
	}
	if err := Rename(root, "other.xml", "sub/new.xml"); err == nil {
		t.Fatal("Rename onto an existing destination succeeded, want an error")
	}
}

func TestDeleteRemovesFileAndFolder(t *testing.T) {
	root := t.TempDir()
	if err := CreateFile(root, "a.xml", "x"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "a.xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, "a.xml"); err == nil {
		t.Fatal("file still readable after Delete")
	}

	if err := CreateFile(root, "dir/child.xml", "x"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dir")); !os.IsNotExist(err) {
		t.Fatal("folder still exists after Delete")
	}
}

func TestDuplicateFindsAvailableName(t *testing.T) {
	root := t.TempDir()
	if err := CreateFile(root, "a.xsl", "orig"); err != nil {
		t.Fatal(err)
	}
	first, err := Duplicate(root, "a.xsl")
	if err != nil {
		t.Fatal(err)
	}
	if first != "a copy.xsl" {
		t.Fatalf("first duplicate = %q, want %q", first, "a copy.xsl")
	}
	second, err := Duplicate(root, "a.xsl")
	if err != nil {
		t.Fatal(err)
	}
	if second != "a copy 2.xsl" {
		t.Fatalf("second duplicate = %q, want %q", second, "a copy 2.xsl")
	}
	got, err := Read(root, second)
	if err != nil || got != "orig" {
		t.Fatalf("Read(%q) = %q, %v", second, got, err)
	}
}

func TestDuplicateRejectsFolder(t *testing.T) {
	root := t.TempDir()
	if err := CreateFolder(root, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := Duplicate(root, "dir"); err == nil {
		t.Fatal("Duplicate on a folder succeeded, want an error")
	}
}
