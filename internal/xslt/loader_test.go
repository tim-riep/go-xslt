package xslt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportPrecedence(t *testing.T) {
	dir := t.TempDir()
	// imported module: a low-precedence template for <x>
	base := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:template match="x">base</xsl:template>
</xsl:stylesheet>`
	if err := os.WriteFile(filepath.Join(dir, "base.xsl"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	main := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:import href="base.xsl"/>
  <xsl:output method="text"/>
  <xsl:template match="x">main</xsl:template>
</xsl:stylesheet>`
	ss, err := CompileFrom(main, dir)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, _, err := ss.Transform(`<x/>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The importing module's template wins over the imported one.
	if strings.TrimSpace(out) != "main" {
		t.Errorf("import precedence: got %q want %q", out, "main")
	}
}

// TestUsePackageUnresolvable checks that xsl:use-package naming a package the
// HOST never made available is XTSE3000. (xsl:use-package itself is supported
// now — see packages.go — but only against the host-supplied registry that
// CompileAtWithPackages takes; plain Compile supplies none.)
func TestUsePackageUnresolvable(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:use-package name="urn:p"/>
  <xsl:template match="/"/>
</xsl:stylesheet>`
	_, err := Compile(sheet)
	if err == nil {
		t.Fatal("expected an error for a package that cannot be located")
	}
	if !strings.Contains(err.Error(), "XTSE3000") {
		t.Errorf("got %v, want XTSE3000", err)
	}
}
