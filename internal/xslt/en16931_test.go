package xslt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCompileEN16931 is a regression test for a large real-world stylesheet
// (the EN16931 CII validation Schematron output), which exercises function
// calls used as relative-path steps, e.g. .../xs:decimal(ram:LineTotalAmount).
// Skipped if the file is not present.
func TestCompileEN16931(t *testing.T) {
	path := filepath.Join("..", "..", "local", "en16931", "EN16931-CII-validation.xslt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("EN16931-CII-validation.xslt not present")
	}
	if _, err := Compile(string(data)); err != nil {
		t.Fatalf("compile EN16931 stylesheet: %v", err)
	}
}
