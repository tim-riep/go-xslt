package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var spaceRun = regexp.MustCompile(`\s+`)

// normalize lowercases a license text, folds typographic quotes and collapses
// every whitespace run, so the phrases below match however the text is wrapped.
func normalize(text string) string {
	r := strings.NewReplacer("“", `"`, "”", `"`, "‘", "'", "’", "'")
	return spaceRun.ReplaceAllString(strings.ToLower(r.Replace(text)), " ")
}

var (
	reISC  = regexp.MustCompile(`permission to use, copy, modify, and(/or)? distribute this software for any purpose with or without fee is hereby granted, provided that`)
	re0BSD = regexp.MustCompile(`permission to use, copy, modify, and(/or)? distribute this software for any purpose with or without fee is hereby granted\. the software is provided`)
)

// detect names the SPDX identifier of a license text. A text no rule knows is
// an error, and so is one that several rules claim (a file holding two
// licenses must be read by a human). It decides only what the file says, not
// what a manifest declares: callers compare the two.
func detect(text string) (string, error) {
	n := normalize(text)
	has := func(s string) bool { return strings.Contains(n, s) }
	var got []string
	add := func(ok bool, id string) {
		if ok {
			got = append(got, id)
		}
	}
	add(has("apache license") && has("version 2.0"), "Apache-2.0")
	add(has("mozilla public license") && has("version 2.0"), "MPL-2.0")
	add(has("sil open font license") && has("version 1.1"), "OFL-1.1")
	add(has("unicode license v3"), "Unicode-3.0")
	add(has("this is free and unencumbered software released into the public domain"), "Unlicense")
	add(has("permission to use, copy, modify, and distribute this software is freely granted, provided that this notice is preserved"), "SunPro")
	add(has("permission is hereby granted, free of charge, to any person obtaining a copy of this software") &&
		has("the above copyright notice and this permission notice shall be included in all copies or substantial portions of the software"), "MIT")
	add(re0BSD.MatchString(n), "0BSD")
	add(reISC.MatchString(n) && !re0BSD.MatchString(n), "ISC")

	bsd := has("redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met") &&
		has("redistributions of source code must retain") &&
		has("redistributions in binary form must reproduce") &&
		!has("advertising materials")
	third := has("neither the name") || has("may be used to endorse or promote") || has("may not be used to endorse")
	add(bsd && third, "BSD-3-Clause")
	add(bsd && !third, "BSD-2-Clause")

	switch len(got) {
	case 0:
		return "", fmt.Errorf("unrecognised license text (starts %q)", firstWords(text))
	case 1:
		return got[0], nil
	}
	sort.Strings(got)
	return "", fmt.Errorf("license text matches several licenses: %s", strings.Join(got, ", "))
}

func firstWords(text string) string {
	f := strings.Fields(text)
	if len(f) > 8 {
		f = f[:8]
	}
	return strings.Join(f, " ")
}

// allowed lists the licenses a shipped dependency may carry. Anything else
// fails the check until a human has read it and added it here.
var allowed = map[string]bool{
	"MIT": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "Apache-2.0": true,
	"ISC": true, "OFL-1.1": true, "Unicode-3.0": true, "0BSD": true, "Unlicense": true,
}

var (
	reCopyright = regexp.MustCompile(`(?im)^[ \t]*(?:copyright|\(c\)|©).*$`)
	// "Copyright notice, this list of conditions…" is body text, not a notice.
	reNotCopyright = regexp.MustCompile(`(?i)^copyright\s+(notice|holders?|owners?|law|license|and|in|is|or|\[)`)
)

// copyrightLine returns the first "Copyright … <year> …" line of a license
// text, or "" (the Apache text itself only has a template line).
func copyrightLine(text string) string {
	for _, l := range reCopyright.FindAllString(text, -1) {
		l = strings.TrimSpace(l)
		if strings.Contains(strings.ToLower(l), "yyyy") || reNotCopyright.MatchString(l) {
			continue
		}
		return l
	}
	return ""
}
