package main

import "github.com/tim-riep/go-xslt/desktop/licenses"

// LicenseService exposes this project's own license and every bundled
// third-party dependency's license text to the frontend, for a visible
// "Licenses" view. See desktop/licenses/licenses.go for what's included and
// how to update it after a dependency change.
type LicenseService struct{}

// License is the frontend-facing shape of licenses.Entry, with the text
// already resolved into a plain field (a method value wouldn't survive the
// bindings' JSON serialization).
type License struct {
	Name    string
	Version string
	License string
	Kind    string
	URL     string
	Text    string
}

// List returns every bundled license, project first.
func (s *LicenseService) List() []License {
	entries := licenses.All()
	out := make([]License, len(entries))
	for i, e := range entries {
		out[i] = License{
			Name:    e.Name,
			Version: e.Version,
			License: e.License,
			Kind:    e.Kind,
			URL:     e.URL,
			Text:    e.Text(),
		}
	}
	return out
}
