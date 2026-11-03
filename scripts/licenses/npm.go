package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// npmPkg is one production package of a package-lock.json.
type npmPkg struct {
	Name    string
	Version string
	Dir     string // node_modules path, relative to the lockfile's directory
}

// parseLockfile returns the packages of a lockfile (version 2 or 3) that can
// ship: everything except the root, links and `dev: true` entries. A
// `devOptional` package is kept (it may be reached from an optional
// production dependency), so a doubtful package is documented, not skipped.
func parseLockfile(data []byte) ([]npmPkg, error) {
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Dev     bool   `json:"dev"`
			Link    bool   `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	if lock.LockfileVersion < 2 || len(lock.Packages) == 0 {
		return nil, fmt.Errorf("package-lock.json: lockfileVersion %d has no \"packages\" map (need 2 or 3)", lock.LockfileVersion)
	}
	var out []npmPkg
	for key, p := range lock.Packages {
		if key == "" || p.Dev || p.Link {
			continue
		}
		i := strings.LastIndex(key, "node_modules/")
		if i < 0 {
			return nil, fmt.Errorf("package-lock.json: unexpected package key %q", key)
		}
		name := p.Name
		if name == "" {
			name = key[i+len("node_modules/"):]
		}
		out = append(out, npmPkg{Name: name, Version: p.Version, Dir: key})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].Dir < out[b].Dir
	})
	return out, nil
}

// packageMeta is the part of a package.json the generator reads.
type packageMeta struct {
	Name       string                  `json:"name"`
	Version    string                  `json:"version"`
	License    json.RawMessage         `json:"license"`
	Licenses   []struct{ Type string } `json:"licenses"`
	Homepage   string                  `json:"homepage"`
	Repository json.RawMessage         `json:"repository"`
}

// declaredLicense reads the license field in its three historic shapes.
func (m packageMeta) declaredLicense() string {
	if len(m.License) > 0 {
		var s string
		if json.Unmarshal(m.License, &s) == nil {
			return s
		}
		var o struct{ Type string }
		if json.Unmarshal(m.License, &o) == nil {
			return o.Type
		}
	}
	if len(m.Licenses) == 1 {
		return m.Licenses[0].Type
	}
	return ""
}

// url is the package's homepage, else its repository, else its npm page.
func (m packageMeta) url(name string) string {
	if strings.HasPrefix(m.Homepage, "http") {
		return m.Homepage
	}
	var repo string
	if len(m.Repository) > 0 {
		if json.Unmarshal(m.Repository, &repo) != nil {
			var o struct{ URL string }
			_ = json.Unmarshal(m.Repository, &o)
			repo = o.URL
		}
	}
	repo = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(repo, "git+"), "git://"), ".git")
	switch {
	case strings.HasPrefix(repo, "http"):
		return repo
	case strings.Count(repo, "/") == 1 && !strings.Contains(repo, ":"):
		return "https://github.com/" + repo
	}
	return "https://www.npmjs.com/package/" + name
}
