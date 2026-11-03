package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mitText = `MIT License

Copyright (c) 2021 Example Author

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.`

const bsdHead = `Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer.
`

func TestDetect(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"mit", mitText, "MIT"},
		{"mit rewrapped", strings.ReplaceAll(mitText, "\n", "\r\n  "), "MIT"},
		{"bsd2", "Copyright (c) 2013 X\n" + bsdHead, "BSD-2-Clause"},
		{"bsd3", bsdHead + "   * Neither the name of Google Inc. nor the names of its\ncontributors may be used to endorse or promote products.\n", "BSD-3-Clause"},
		{"bsd4 is not allowed through", bsdHead + "All advertising materials mentioning features must display\n", ""},
		{"apache", "Apache License\nVersion 2.0, January 2004\n", "Apache-2.0"},
		{"isc", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice", "ISC"},
		{"0bsd", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted.\n\nTHE SOFTWARE IS PROVIDED", "0BSD"},
		{"ofl", "SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007", "OFL-1.1"},
		{"unicode", "UNICODE LICENSE V3\n\nCOPYRIGHT AND PERMISSION NOTICE", "Unicode-3.0"},
		{"unlicense", "This is free and unencumbered software released into the public domain.", "Unlicense"},
		{"gpl", "GNU GENERAL PUBLIC LICENSE Version 3", ""},
		{"two licenses", mitText + "\n\nApache License Version 2.0", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		got, err := detect(c.text)
		if c.want == "" {
			if err == nil {
				t.Errorf("%s: want an error, got %q", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %s", c.name, got, err, c.want)
		}
	}
}

func TestAllowList(t *testing.T) {
	for _, id := range []string{"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC", "OFL-1.1", "Unicode-3.0", "0BSD", "Unlicense"} {
		if !allowed[id] {
			t.Errorf("%s should be allowed", id)
		}
	}
	for _, id := range []string{"MPL-2.0", "GPL-3.0", "SunPro", ""} {
		if allowed[id] {
			t.Errorf("%s must not be on the allow-list", id)
		}
	}
}

func TestCopyrightLine(t *testing.T) {
	cases := map[string]string{
		mitText: "Copyright (c) 2021 Example Author",
		"Apache template\nCopyright [yyyy] [name of copyright owner]\n":         "",
		"Copyright notice, this list of conditions\nCopyright (c) Meta, Inc.\n": "Copyright (c) Meta, Inc.",
		"Copyright © 1991-2023 Unicode, Inc.":                                   "Copyright © 1991-2023 Unicode, Inc.",
		bsdHead:                                                                 "",
	}
	for text, want := range cases {
		if got := copyrightLine(text); got != want {
			t.Errorf("copyrightLine(%q) = %q, want %q", text, got, want)
		}
	}
}

const lockFixture = `{
  "name": "x", "lockfileVersion": 3,
  "packages": {
    "": {"name": "x", "dependencies": {"a": "1"}},
    "node_modules/a": {"version": "1.0.0", "license": "MIT"},
    "node_modules/@s/b": {"version": "2.0.0"},
    "node_modules/a/node_modules/c": {"version": "3.0.0"},
    "node_modules/devonly": {"version": "9.9.9", "dev": true},
    "node_modules/opt": {"version": "1.1.1", "devOptional": true},
    "node_modules/self": {"resolved": "..", "link": true}
  }
}`

func TestParseLockfile(t *testing.T) {
	got, err := parseLockfile([]byte(lockFixture))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.Name+"@"+p.Version)
	}
	want := "@s/b@2.0.0 a@1.0.0 c@3.0.0 opt@1.1.1"
	if strings.Join(names, " ") != want {
		t.Errorf("got %v, want %s", names, want)
	}
	if got[2].Dir != "node_modules/a/node_modules/c" {
		t.Errorf("nested dir: %s", got[2].Dir)
	}
	if _, err := parseLockfile([]byte(`{"lockfileVersion":1,"dependencies":{}}`)); err == nil {
		t.Error("lockfile v1 must be rejected")
	}
	if _, err := parseLockfile([]byte(`{`)); err == nil {
		t.Error("invalid JSON must be rejected")
	}
}

func TestPackageMeta(t *testing.T) {
	var m packageMeta
	for in, want := range map[string]string{
		`{"license":"MIT"}`:                      "MIT",
		`{"license":{"type":"ISC"}}`:             "ISC",
		`{"licenses":[{"type":"BSD-2-Clause"}]}`: "BSD-2-Clause",
		`{}`:                                     "",
		`{"license":"(MIT OR Apache-2.0)"}`:      "(MIT OR Apache-2.0)",
	} {
		m = packageMeta{}
		if err := jsonUnmarshal(in, &m); err != nil {
			t.Fatal(err)
		}
		if got := m.declaredLicense(); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`{"homepage":"https://example.org/p"}`:                    "https://example.org/p",
		`{"repository":"foo/bar"}`:                                "https://github.com/foo/bar",
		`{"repository":{"url":"git+https://github.com/a/b.git"}}`: "https://github.com/a/b",
		`{}`: "https://www.npmjs.com/package/n",
	} {
		m = packageMeta{}
		if err := jsonUnmarshal(in, &m); err != nil {
			t.Fatal(err)
		}
		if got := m.url("n"); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

// Every license text committed under desktop/licenses must be recognised and
// agree with the identifier of the entry that names it: the same test the
// -check run applies, without needing node_modules or a module cache.
func TestCommittedTextsAreRecognised(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Skip(err)
	}
	n := 0
	for _, sub := range []string{"go", "npm", "data", "fonts", "project"} {
		_ = filepath.Walk(filepath.Join(root, licDir, sub), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			if _, err := detect(string(b)); err != nil {
				t.Errorf("%s: %v", p, err)
			}
			n++
			return nil
		})
	}
	if n == 0 {
		t.Error("no license texts found")
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
