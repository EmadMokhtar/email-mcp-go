package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tests of the table hook read the table fixtures from a build that "make
// site" makes: the site plus the pages of testdata/tables, built with the
// Hugo environment tabletest (site/config/tabletest/hugo.toml). The variable
// SITE_TABLES_DIR is the folder of that build.
//
// Outside CI, a test that finds the variable empty skips, so go test ./... works
// with no build. In CI (the variable CI is set, as GitHub Actions does) an empty
// variable fails the test. Without this rule a CI step that forgot the variable
// would skip the tests and pass.
const tablesDirVar = "SITE_TABLES_DIR"

// errNoTablesDir is the error of an empty SITE_TABLES_DIR in CI.
var errNoTablesDir = errors.New(tablesDirVar + " is empty in CI. make site sets it to the build of the table fixtures")

// tablesDir decides what a test does with the value of SITE_TABLES_DIR. It
// returns the folder when the value is not empty. An empty value gives skip
// outside CI and an error in CI.
func tablesDir(value string, ci bool) (dir string, skip bool, err error) {
	switch {
	case value != "":
		return value, false, nil
	case ci:
		return "", false, errNoTablesDir
	default:
		return "", true, nil
	}
}

func TestTablesDir(t *testing.T) {
	dir, skip, err := tablesDir("/build", false)
	require.NoError(t, err)
	require.False(t, skip)
	require.Equal(t, "/build", dir)

	dir, skip, err = tablesDir("/build", true)
	require.NoError(t, err)
	require.False(t, skip)
	require.Equal(t, "/build", dir)

	_, skip, err = tablesDir("", false)
	require.NoError(t, err)
	require.True(t, skip, "outside CI, an empty variable skips the test")

	_, skip, err = tablesDir("", true)
	require.ErrorIs(t, err, errNoTablesDir, "in CI, an empty variable must fail the test")
	require.False(t, skip)
}
