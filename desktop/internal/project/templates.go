package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SampleStylesheet and SampleSource seed every newly created project so
// there is always something meaningful to run immediately, and seed a v1
// desktop/internal/workspace's DefaultWorkspace() equivalent.
const SampleStylesheet = `<?xml version="1.0" encoding="UTF-8"?>
<xsl:stylesheet version="3.0"
    xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
  <xsl:output method="html" indent="yes"/>

  <xsl:template match="/catalog">
    <html>
      <body>
        <h1>Books</h1>
        <ul>
          <xsl:apply-templates select="book"/>
        </ul>
      </body>
    </html>
  </xsl:template>

  <xsl:template match="book">
    <li>
      <xsl:value-of select="title"/> &#8212; <xsl:value-of select="author"/>
    </li>
  </xsl:template>
</xsl:stylesheet>
`

const SampleSource = `<?xml version="1.0" encoding="UTF-8"?>
<catalog>
  <book>
    <title>The Go Programming Language</title>
    <author>Donovan &amp; Kernighan</author>
  </book>
  <book>
    <title>XSLT 3.0</title>
    <author>Michael Kay</author>
  </book>
</catalog>
`

// Skeleton returns starter content for a brand-new file of the given kind,
// keyed off its extension (see fileKind) rather than FileKind directly so a
// caller creating e.g. "notes.txt" gets an empty file rather than a guess.
func Skeleton(name string) string {
	switch fileKind(name) {
	case KindStylesheet:
		return `<?xml version="1.0" encoding="UTF-8"?>
<xsl:stylesheet version="3.0"
    xmlns:xsl="http://www.w3.org/1999/XSL/Transform">

  <xsl:template match="/">
    <xsl:copy-of select="."/>
  </xsl:template>

</xsl:stylesheet>
`
	case KindSchema:
		return `<?xml version="1.0" encoding="UTF-8"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">

</xs:schema>
`
	case KindXML:
		return `<?xml version="1.0" encoding="UTF-8"?>
<root/>
`
	default:
		return ""
	}
}

// CreateProject creates a fresh, seeded project named name inside
// LibraryRoot() and opens it. The on-disk folder name is sanitized from name
// but never collides with an existing one (a numeric suffix is appended).
func CreateProject(name string) (*Project, error) {
	if strings.TrimSpace(name) == "" {
		name = "Untitled project"
	}
	dir, err := uniqueLibraryDir(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := CreateFile(dir, "transform.xsl", SampleStylesheet); err != nil {
		return nil, err
	}
	if err := CreateFile(dir, "input.xml", SampleSource); err != nil {
		return nil, err
	}
	m := Manifest{
		Schema: CurrentSchema,
		Name:   name,
		Runs: []Run{{
			ID:         "transform-1",
			Name:       "Transform",
			Kind:       RunTransform,
			Stylesheet: "transform.xsl",
			Source:     "input.xml",
			Params:     map[string]string{},
		}},
	}
	if err := Save(dir, m); err != nil {
		return nil, err
	}
	return Open(dir)
}

var slugUnsafe = regexp.MustCompile(`[^a-zA-Z0-9 _-]+`)

func slugify(name string) string {
	s := slugUnsafe.ReplaceAllString(strings.TrimSpace(name), "")
	s = strings.TrimSpace(s)
	if s == "" {
		return "project"
	}
	return s
}

func uniqueLibraryDir(name string) (string, error) {
	root := LibraryRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create library root: %w", err)
	}
	base := slugify(name)
	for n := 0; ; n++ {
		candidate := base
		if n > 0 {
			candidate = fmt.Sprintf("%s %d", base, n+1)
		}
		full := filepath.Join(root, candidate)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			return full, nil
		}
	}
}
