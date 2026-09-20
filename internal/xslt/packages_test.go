package xslt

import "testing"

// TestPackageVersionRanges pins the package-version-ranges semantics the
// xslt30-test use-package-2xx cases define: "2.0" and "2.0.0" are the SAME
// version, a pre-release suffix sorts BELOW the release it qualifies, two
// suffixes compare lexically, and the four range forms select accordingly.
func TestPackageVersionRanges(t *testing.T) {
	// The versions use-package-env-004 makes available.
	avail := []string{"1.0.0", "2.0.0", "2.0.0-alpha", "2.0.0-beta", "3.5.4"}

	match := func(spec string) []string {
		rs, err := parsePkgRanges(spec)
		if err != nil {
			t.Fatalf("parsePkgRanges(%q): %v", spec, err)
		}
		var out []string
		for _, v := range avail {
			pv, ok := parsePkgVersion(v)
			if !ok {
				t.Fatalf("parsePkgVersion(%q) failed", v)
			}
			if matchesAnyPkgRange(rs, pv) {
				out = append(out, v)
			}
		}
		return out
	}
	eq := func(spec string, want ...string) {
		t.Helper()
		got := match(spec)
		if len(got) != len(want) {
			t.Errorf("%q selected %v, want %v", spec, got, want)
			return
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q selected %v, want %v", spec, got, want)
				return
			}
		}
	}

	eq("1.0.0", "1.0.0")                                           // use-package-201
	eq("2.0.0", "2.0.0")                                           // -202: the release only, not its pre-releases
	eq("2.0", "2.0.0")                                             // -212: missing decimal parts are zero
	eq("1.0.0, 2.0", "1.0.0", "2.0.0")                             // -203
	eq("1.5 to 2.5", "2.0.0", "2.0.0-alpha", "2.0.0-beta")         // -204
	eq("to 1.5", "1.0.0")                                          // -205
	eq("1.5+", "2.0.0", "2.0.0-alpha", "2.0.0-beta", "3.5.4")      // -206
	eq("3.5.*", "3.5.4")                                           // -207
	eq("2.0.0-alpha", "2.0.0-alpha")                               // -208
	eq("2.0.0-arable-environment.27 to 2.0.0-gamma", "2.0.0-beta") // -209: "alpha" < "arable..." lexically
	eq("2.0.0-a to 2.0.0-gamma", "2.0.0-alpha", "2.0.0-beta")      // -210
	eq("2.7.0-a to 5.0.0-gamma", "3.5.4")                          // -211
	eq("*", avail...)

	// A release outranks its own pre-releases (so "highest wins" picks 2.0.0).
	a, _ := parsePkgVersion("2.0.0")
	b, _ := parsePkgVersion("2.0.0-beta")
	if comparePkgVersion(a, b) <= 0 {
		t.Error("2.0.0 must rank above 2.0.0-beta")
	}

	// Invalid range forms are rejected (use-package-291..294 expect XTSE0020).
	for _, bad := range []string{"2.0.0-alpha:beta", "TotallyInvalid", "-3.6", "-alpha", ""} {
		if _, err := parsePkgRanges(bad); err == nil {
			t.Errorf("parsePkgRanges(%q) should be rejected", bad)
		}
	}
}
