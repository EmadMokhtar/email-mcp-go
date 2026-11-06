//go:build screenshots

package chrometest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindChromeNamesTheFix(t *testing.T) {
	t.Setenv("CHROME_PATH", "/no/such/chrome")
	_, err := Find()
	require.ErrorContains(t, err, "make screenshots needs Chrome")
	require.ErrorContains(t, err, "CHROME_PATH")
}

func TestFindChromeUsesChromePath(t *testing.T) {
	// CHROME_PATH needs a program that exists, and the test binary always does.
	program, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("CHROME_PATH", program)

	found, err := Find()
	require.NoError(t, err)
	require.Equal(t, program, found)
}

// pathNames returns the names in programs that are looked up in the PATH, the
// ones with no slash. A test can fake those. It cannot fake an absolute path.
func pathNames() []string {
	var names []string
	for _, program := range programs {
		if !strings.Contains(program, "/") {
			names = append(names, program)
		}
	}
	return names
}

// fakePath makes a PATH with a program of each name in it, and returns the
// path of the program of each name.
func fakePath(t *testing.T, names ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	found := map[string]string{}
	for _, name := range names {
		program := filepath.Join(dir, name)
		//nolint:gosec // G306: the test needs a file that can run, and the lookup checks the mode
		require.NoError(t, os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700))
		found[name] = program
	}
	t.Setenv("PATH", dir)
	return found
}

// TestFindChromeKnowsTheNamesOfEachSystem requires that every name that a
// system uses for Chrome is found, one by one.
func TestFindChromeKnowsTheNamesOfEachSystem(t *testing.T) {
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		t.Run(name, func(t *testing.T) {
			want := fakePath(t, name)[name]
			found, ok := firstProgram(pathNames())
			require.True(t, ok, "no program found in the PATH")
			require.Equal(t, want, found)
		})
	}
}

// TestFindChromeKeepsTheOrder requires that the names that were there first
// still win, so a computer that has both finds the same Chrome as before.
func TestFindChromeKeepsTheOrder(t *testing.T) {
	for _, tc := range []struct {
		name, first string
		others      []string
	}{
		{"google-chrome before google-chrome-stable", "google-chrome", []string{"google-chrome-stable"}},
		{"chromium before chromium-browser", "chromium", []string{"chromium-browser"}},
		{"the current names before the new ones", "chromium", []string{"google-chrome-stable", "chromium-browser"}},
		{"google-chrome before everything", "google-chrome", []string{"chromium", "google-chrome-stable", "chromium-browser"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := fakePath(t, append([]string{tc.first}, tc.others...)...)
			got, ok := firstProgram(pathNames())
			require.True(t, ok)
			require.Equal(t, found[tc.first], got)
		})
	}
}

// TestFindChromeListsTheMacApplications requires both applications of macOS in
// the list, after the names that were there first. A test cannot fake an
// absolute path, so the test reads the list.
func TestFindChromeListsTheMacApplications(t *testing.T) {
	const (
		chrome   = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		chromium = "/Applications/Chromium.app/Contents/MacOS/Chromium"
	)
	require.Contains(t, programs, chrome)
	require.Contains(t, programs, chromium)
	for _, old := range []string{"google-chrome", "chromium", chrome} {
		for _, added := range []string{"google-chrome-stable", "chromium-browser", chromium} {
			require.Less(t, slices.Index(programs, old), slices.Index(programs, added),
				"%s must come before %s", old, added)
		}
	}
}
