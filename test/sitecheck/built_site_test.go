//go:build sitebuild || screenshots

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file has what the tests with the tag sitebuild and the tests with the
// tag screenshots both use to read a build of the site.

// fixtureSection is the section of the build that holds the fixture pages. It
// is the target of the mount in site/config/tabletest/hugo.toml.
const fixtureSection = "tabletest"

// tablesBuild returns the folder of the build of the table fixtures. It skips
// the test, or fails it in CI, when SITE_TABLES_DIR is empty (see tablesDir).
func tablesBuild(t *testing.T) string {
	t.Helper()
	dir, skip, err := tablesDir(os.Getenv(tablesDirVar), os.Getenv("CI") != "")
	require.NoError(t, err)
	if skip {
		t.Skip(tablesDirVar + " is empty. Run make site to build the table fixtures and test them.")
	}
	return dir
}

// indexPages returns the path of every index.html below public, with forward
// slashes.
func indexPages(t *testing.T, public string) []string {
	t.Helper()
	var pages []string
	//nolint:gosec // G703: the Makefile sets this path
	err := filepath.WalkDir(public, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "index.html" {
			return nil
		}
		rel, err := filepath.Rel(public, file)
		if err != nil {
			return err
		}
		pages = append(pages, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	return pages
}
