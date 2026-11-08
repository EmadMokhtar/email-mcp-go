package main

import (
	"maps"
	"strings"
	"testing"
)

// mapsPage is a small page with the two maps, in the shape of the real page.
// Its first table row names cmd/a, its second internal/b. The manual map names
// a file of cmd/a and puts internal/b in the list of areas with no manual page.
const mapsPage = `---
title: T
---

## Which page do I update?

### Map of the tech docs

| Code area | Main tech page | Also check |
|---|---|---|
| ` + "`cmd/a`" + ` | A | |
| ` + "`internal/b`" + ` | B | |

| File or folder | Main tech page | Also check |
|---|---|---|
| ` + "`Makefile`, `internal/b/b.go`" + ` | this page | |

### Map of the user manual

| Code | Manual pages |
|---|---|
| ` + "`cmd/a/main.go` (`Instructions`), `other.go`" + ` | A |

These areas have no manual page: ` + "`internal/b`" + `.

## Next
`

// mapsRepo returns the files of a small repository with Go packages and the
// page of the maps. extra adds or replaces files; an empty value removes one.
func mapsRepo(extra map[string]string) map[string]string {
	files := map[string]string{
		"site/public/index.html":   htmlPage("", ""),
		"Makefile":                 "all:\n",
		"go.mod":                   "module example.com/x\n",
		"cmd/a/main.go":            "package main\n",
		"cmd/a/other.go":           "package main\n",
		"internal/b/b.go":          "package b\n",
		"internal/b/testdata/x.go": "package x\n",
		mapsFile:                   mapsPage,
	}
	maps.Copy(files, extra)
	for name, body := range files {
		if body == "" {
			delete(files, name)
		}
	}
	return files
}

func TestPageMaps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"every package and path is in the maps", nil, ""},
		{
			"a package with no row in the tech map",
			map[string]string{
				"internal/c/c.go": "package c\n",
				mapsFile:          replace(mapsPage, "manual page: `internal/b`", "manual page: `internal/b`, `internal/c`"),
			},
			"package internal/c has no row in the map of the tech docs",
		},
		{
			"a package below a row is covered by it",
			map[string]string{"internal/b/deep/d.go": "package deep\n"},
			"",
		},
		{
			"a package with only test files counts",
			map[string]string{
				"cmd/z/z_test.go": "package z\n",
				mapsFile:          replace(mapsPage, "manual page: `internal/b`", "manual page: `internal/b`, `cmd/z`"),
			},
			"package cmd/z has no row in the map of the tech docs",
		},
		{
			"a package with no row in the manual map",
			map[string]string{mapsFile: replace(mapsPage, "manual page: `internal/b`", "manual page: `cmd/a`")},
			"package internal/b has no row in the map of the user manual",
		},
		{
			"a path in a map that does not exist",
			map[string]string{"internal/b/b.go": "", "internal/b/c.go": "package b\n"},
			"line 16: `internal/b/b.go` does not exist in the repository",
		},
		{
			"a short name is read from the folder of the first path of its cell",
			map[string]string{"cmd/a/other.go": "", "cmd/a/third.go": "package main\n"},
			"line 22: `other.go` does not exist in the repository",
		},
		{
			"no tech map",
			map[string]string{mapsFile: replace(mapsPage, "### Map of the tech docs", "### Other")},
			`has no section "Map of the tech docs"`,
		},
		{
			"no manual map",
			map[string]string{mapsFile: replace(mapsPage, "### Map of the user manual", "### Other")},
			`has no section "Map of the user manual"`,
		},
		{
			"no page of the maps",
			map[string]string{mapsFile: ""},
			"the page of the maps is missing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, mapsRepo(tc.extra))
			requireOutcome(t, cfg, mapsFile, tc.want)
		})
	}
}

// TestPageMapsNeedNoPageOutsideTheModule keeps the other tests small: a root
// with no go.mod needs no page of the maps, even with Go files.
func TestPageMapsNeedNoPageOutsideTheModule(t *testing.T) {
	cfg := newSite(t, subPath, mapsRepo(map[string]string{"go.mod": "", mapsFile: ""}))
	requireClean(t, cfg)
}

// TestPageMapsNeedAPackage covers a module with no Go package in cmd/ or
// internal/: there is nothing to map, so no page is needed.
func TestPageMapsNeedAPackage(t *testing.T) {
	cfg := newSite(t, subPath, map[string]string{
		"site/public/index.html": htmlPage("", ""),
		"go.mod":                 "module example.com/x\n",
	})
	requireClean(t, cfg)
}

// replace is strings.Replace for one match, and it fails the test of a typo:
// a replacement that matches nothing would test the unchanged page.
func replace(s, old, new string) string {
	out := strings.Replace(s, old, new, 1)
	if out == s {
		panic("replace: " + old + " is not in the text")
	}
	return out
}
