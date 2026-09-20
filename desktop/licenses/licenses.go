// Package licenses embeds this project's own license plus the license text
// of every third-party dependency that ends up compiled/bundled into the
// shipped desktop app — Go modules (root + this module, direct and
// indirect, since Go static-links all of them into the final binary) and
// the frontend's runtime npm dependencies (its build-time-only devDependencies
// — typescript, vite, @types/* — never ship, so they're not listed here).
//
// Regenerating after a dependency change: copy the new version's LICENSE
// file over the matching path below (Go: $(go env GOMODCACHE)/<module>@
// <version>/LICENSE*; npm: frontend/node_modules/<package>/LICENSE*) and
// update its Entries record. There is no automated generator yet — this is
// a short, infrequently-changing list, hand-maintained deliberately rather
// than guessed at build time.
package licenses

import "embed"

//go:embed go npm project fonts
var files embed.FS

// Entry describes one bundled license: this project's own, or one
// third-party dependency's.
type Entry struct {
	// Name is the module/package name as its own ecosystem spells it
	// (e.g. "golang.org/x/text", "react").
	Name string
	// Version is the exact version bundled, matching go.mod/package.json.
	Version string
	// License is the SPDX license identifier.
	License string
	// Kind is "project", "go", "npm", or "font".
	Kind string
	// URL is the project's homepage or source repository, for reference.
	URL string
	path string
}

var entries = []Entry{
	{Name: "go-xslt", Version: "", License: "Apache-2.0", Kind: "project",
		URL: "https://github.com/tim-riep/go-xslt", path: "project/LICENSE"},

	// Go modules (root go.mod + desktop/go.mod, direct and indirect — all
	// statically linked into the compiled binary).
	{Name: "golang.org/x/text", Version: "v0.38.0", License: "BSD-3-Clause", Kind: "go",
		URL: "https://pkg.go.dev/golang.org/x/text", path: "go/golang.org/x/text/LICENSE"},
	{Name: "golang.org/x/sys", Version: "v0.43.0", License: "BSD-3-Clause", Kind: "go",
		URL: "https://pkg.go.dev/golang.org/x/sys", path: "go/golang.org/x/sys/LICENSE"},
	{Name: "github.com/wailsapp/wails/v3", Version: "v3.0.0-alpha2.103", License: "MIT", Kind: "go",
		URL: "https://wails.io", path: "go/github.com/wailsapp/wails/v3/LICENSE"},
	{Name: "github.com/wailsapp/wails/webview2", Version: "v1.0.24", License: "MIT", Kind: "go",
		URL: "https://github.com/wailsapp/wails", path: "go/github.com/wailsapp/wails/webview2/LICENSE"},
	{Name: "github.com/adrg/xdg", Version: "v0.5.3", License: "MIT", Kind: "go",
		URL: "https://github.com/adrg/xdg", path: "go/github.com/adrg/xdg/LICENSE"},
	{Name: "github.com/coder/websocket", Version: "v1.8.14", License: "ISC", Kind: "go",
		URL: "https://github.com/coder/websocket", path: "go/github.com/coder/websocket/LICENSE"},
	{Name: "github.com/ebitengine/purego", Version: "v0.9.1", License: "Apache-2.0", Kind: "go",
		URL: "https://github.com/ebitengine/purego", path: "go/github.com/ebitengine/purego/LICENSE"},
	{Name: "github.com/go-ole/go-ole", Version: "v1.3.0", License: "MIT", Kind: "go",
		URL: "https://github.com/go-ole/go-ole", path: "go/github.com/go-ole/go-ole/LICENSE"},
	{Name: "github.com/godbus/dbus/v5", Version: "v5.2.2", License: "BSD-2-Clause", Kind: "go",
		URL: "https://github.com/godbus/dbus", path: "go/github.com/godbus/dbus/v5/LICENSE"},
	{Name: "github.com/jchv/go-winloader", Version: "v0.0.0-20250406163304-c1995be93bd1", License: "ISC", Kind: "go",
		URL: "https://github.com/jchv/go-winloader", path: "go/github.com/jchv/go-winloader/LICENSE"},
	{Name: "github.com/mattn/go-colorable", Version: "v0.1.14", License: "MIT", Kind: "go",
		URL: "https://github.com/mattn/go-colorable", path: "go/github.com/mattn/go-colorable/LICENSE"},
	{Name: "github.com/mattn/go-isatty", Version: "v0.0.20", License: "MIT", Kind: "go",
		URL: "https://github.com/mattn/go-isatty", path: "go/github.com/mattn/go-isatty/LICENSE"},

	// Frontend runtime npm dependencies (bundled by Vite into the shipped
	// JS; devDependencies — typescript, vite, @vitejs/plugin-react,
	// @types/* — are build tools only and never ship, so aren't listed).
	{Name: "react", Version: "18.3.1", License: "MIT", Kind: "npm",
		URL: "https://react.dev", path: "npm/react/LICENSE"},
	{Name: "react-dom", Version: "18.3.1", License: "MIT", Kind: "npm",
		URL: "https://react.dev", path: "npm/react-dom/LICENSE"},
	{Name: "@codemirror/lang-xml", Version: "6.1.0", License: "MIT", Kind: "npm",
		URL: "https://codemirror.net", path: "npm/@codemirror/lang-xml/LICENSE"},
	{Name: "@codemirror/lint", Version: "6.9.7", License: "MIT", Kind: "npm",
		URL: "https://codemirror.net", path: "npm/@codemirror/lint/LICENSE"},
	{Name: "@codemirror/state", Version: "6.6.0", License: "MIT", Kind: "npm",
		URL: "https://codemirror.net", path: "npm/@codemirror/state/LICENSE"},
	{Name: "@codemirror/view", Version: "6.43.1", License: "MIT", Kind: "npm",
		URL: "https://codemirror.net", path: "npm/@codemirror/view/LICENSE"},
	{Name: "@uiw/codemirror-theme-github", Version: "4.25.10", License: "MIT", Kind: "npm",
		URL: "https://github.com/uiwjs/react-codemirror", path: "npm/@uiw/codemirror-theme-github/LICENSE"},
	{Name: "@uiw/react-codemirror", Version: "4.25.10", License: "MIT", Kind: "npm",
		URL: "https://github.com/uiwjs/react-codemirror", path: "npm/@uiw/react-codemirror/LICENSE"},
	{Name: "@wailsio/runtime", Version: "3.0.0-alpha.79", License: "MIT", Kind: "npm",
		URL: "https://wails.io", path: "npm/@wailsio/runtime/LICENSE"},
	{Name: "react-resizable-panels", Version: "2.1.9", License: "MIT", Kind: "npm",
		URL: "https://github.com/bvaughn/react-resizable-panels", path: "npm/react-resizable-panels/LICENSE"},

	// Bundled font.
	{Name: "Inter", Version: "", License: "OFL-1.1", Kind: "font",
		URL: "https://rsms.me/inter/", path: "fonts/Inter/LICENSE.txt"},
}

// All returns every entry with its full license text loaded, project first,
// then in the order declared above.
func All() []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// Text returns e's full embedded license text, or "" if it can't be read
// (should not happen for anything in Entries — every path above is checked
// against the embedded tree at compile time by go:embed itself).
func (e Entry) Text() string {
	b, err := files.ReadFile(e.path)
	if err != nil {
		return ""
	}
	return string(b)
}
