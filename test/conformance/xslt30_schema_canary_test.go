package conformance

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The 31 catalog cases that declare feature schema_aware satisfied="false", i.e.
// that are written for a processor which is NOT schema-aware and assert the
// XTSE1660-family rejections and untyped readings such a processor owes. They
// are the canary for schema-awareness work: a gate that leaks — an XTSE1660
// lifted a shade too broadly, a type annotation that starts surviving where a
// basic processor must not produce one — shows up here first, and the headline
// TestXSLT30 number can absorb a handful of them moving without visibly
// changing. This keeps them named.
//
// The list is the one the catalog yields for
//
//	grep -rl 'feature value="schema_aware" satisfied="false"' tests/
//
// case by case; re-derive it if the suite is updated.
var schemaUnawareCases = []string{
	"built-in-templates-0301",
	"error-1535b", "error-1650a",
	"error-1660a", "error-1660b", "error-1660c", "error-1660d", "error-1660e",
	"error-1665a", "error-3245a",
	"import-schema-191", "json-to-xml-typed-010",
	"strip-type-annotations-023", "strip-type-annotations-024", "strip-type-annotations-025",
	"try-015", "try-017", "try-025", "try-026",
	"type-0303", "type-available-0148",
	"validation-0101", "validation-0102a", "validation-0102b",
	"validation-0103a", "validation-0103b", "validation-0104",
	"validation-0105a", "validation-0105b", "validation-0106", "validation-0106a",
}

func TestXSLT30SchemaUnawareCases(t *testing.T) {
	root := filepath.Join(repoRoot(), "test", "conformance", "xslt30-test")
	data, err := os.ReadFile(filepath.Join(root, "catalog.xml"))
	if err != nil {
		t.Skip("xslt30-test not present")
	}
	var cat fotsCatalog
	if err := unmarshalFOTS(data, &cat); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, e := range cat.Envs {
		e.base = root
		xsltCatEnvs[e.Name] = e
	}
	want := map[string]bool{}
	for _, n := range schemaUnawareCases {
		want[n] = true
	}

	cache := &ssCache{m: map[string]ssEntry{}}
	seen := map[string]string{}
	for _, ts := range cat.TestSets {
		setFile := filepath.Join(root, ts.File)
		b, err := os.ReadFile(setFile)
		if err != nil {
			continue
		}
		var set xsltSet
		if err := unmarshalFOTS(b, &set); err != nil {
			continue
		}
		for _, tc := range set.Cases {
			if !want[tc.Name] {
				continue
			}
			st, reason := safeRunXSLT(tc, set, filepath.Dir(setFile), cache)
			seen[tc.Name] = st
			if st == "fail" {
				t.Errorf("%s: fail (%s) — a case written for a NON-schema-aware processor", tc.Name, reason)
			}
		}
	}

	var missing []string
	for _, n := range schemaUnawareCases {
		if _, ok := seen[n]; !ok {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("not found in the catalog (suite updated? re-derive the list): %v", missing)
	}

	// With the claim OFF these cases apply and must genuinely run; a few are
	// pinned to XSLT 2.0 exactly and skip on the spec version, which is the
	// harness's own business, not this feature's. With XSLT30_FULL=1 they all
	// skip instead — a run that CLAIMS schema-awareness (XSLT30_FULL claims it
	// alongside streaming — see xsltFullHarness in xslt30_test.go) cannot
	// satisfy a dependency asking for its absence.
	if !xsltFullHarness() {
		ran := 0
		for _, st := range seen {
			if st == "pass" {
				ran++
			}
		}
		if ran == 0 {
			t.Error("none of the non-schema-aware cases ran; the canary is vacuous")
		}
	} else {
		for n, st := range seen {
			if st != "skip" {
				t.Errorf("%s: %s under XSLT30_FULL=1, want skip", n, st)
			}
		}
	}
}
