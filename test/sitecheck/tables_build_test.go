//go:build sitebuild

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// These tests check the table hook of the site (layouts/_markup/render-table.html)
// on the pages of testdata/tables, as Hugo built them. "make site" builds them
// with the Hugo environment tabletest and sets SITE_TABLES_DIR to the folder of
// the build. The pages come out of the real layouts, so the tests see what a
// reader sees, and not a copy of the markup. The tests skip when
// SITE_TABLES_DIR is empty, and fail when it is empty in CI.
//
// The site check (main.go) already requires a name, a box and unique names on
// every page. These tests add what only the fixtures can show: the exact name
// of each table, the escaping, the numbers and the alignment classes.

// wantLabels lists, for each fixture page, the aria-label of each of its tables
// in order. The Markdown of each page says why the labels are these.
var wantLabels = map[string][]string{
	// Tables 1 to 3 have the same headers, and tables 4 and 5 have other ones.
	// The number is the place of the table in the page.
	"duplicates": {
		"Command, What it does",
		"Command, What it does (table 2)",
		"Command, What it does (table 3)",
		"Flag, Meaning",
		"Flag, Meaning (table 5)",
	},
	// The label is the plain text of the header cells: no tags, and the signs
	// as the reader sees them.
	"escaping": {
		"Fish & Chips, a < b",
		`say "hi", a<b`,
		"Bold and slanted, link",
	},
	// An empty header cell is "column N". With no text in the header, the
	// first body row names the table. With no text there too, it is "Table N".
	"empty-headers": {
		"column 1, z",
		"a, column 2",
		"x, y",
		"column 1, q",
		"Table 5",
	},
	"alignment": {"Default, Left, Center, Right"},
	"wide":      {"Option one, Option two, Option three, Option four, Option five"},
}

// fixturePage returns the file of a fixture page in the build.
func fixturePage(dir, name string) string {
	return filepath.Join(dir, fixtureSection, name, "index.html")
}

// tablesOf returns every <table> of doc, in the order of the page.
func tablesOf(doc *html.Node) []*html.Node {
	var tables []*html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "table" {
			tables = append(tables, n)
		}
	}
	return tables
}

// readFixture reads and parses a fixture page.
func readFixture(t *testing.T, dir, name string) (raw string, doc *html.Node) {
	t.Helper()
	file := fixturePage(dir, name)
	data, err := os.ReadFile(file) //nolint:gosec // G304: the path is the build folder that make site gives
	require.NoError(t, err, "Did the build with the environment tabletest run? Is %s mounted?", fixtureSection)
	doc, err = parseFile(file)
	require.NoError(t, err)
	return string(data), doc
}

// TestTableHookFixturePages requires that the build has exactly the fixture
// pages that this file knows. A new fixture page needs its expected labels, and
// a page that did not build is a failure and not a silent gap.
func TestTableHookFixturePages(t *testing.T) {
	dir := tablesBuild(t)
	entries, err := os.ReadDir(filepath.Join(dir, fixtureSection))
	require.NoError(t, err, "the build has no section %s", fixtureSection)
	var built []string
	for _, e := range entries {
		if e.IsDir() {
			built = append(built, e.Name())
		}
	}
	var want []string
	for name := range wantLabels {
		want = append(want, name)
	}
	slices.Sort(built)
	slices.Sort(want)
	require.Equal(t, want, built, "the fixture pages in testdata/tables and wantLabels must list the same pages")
}

// TestTableHookNames requires the exact aria-label of each fixture table.
func TestTableHookNames(t *testing.T) {
	dir := tablesBuild(t)
	for name, want := range wantLabels {
		t.Run(name, func(t *testing.T) {
			_, doc := readFixture(t, dir, name)
			var got []string
			for _, table := range tablesOf(doc) {
				label, ok := attr(table, "aria-label")
				require.True(t, ok, "a table has no aria-label")
				got = append(got, label)
			}
			require.Equal(t, want, got)
		})
	}
}

// TestTableHookEscapesOnce requires that a sign such as & or < is escaped once
// in the aria-label and once in the header cell, and not twice. The parsed
// label is the plain text (TestTableHookNames). Here the raw page is read,
// because a parser turns "&amp;amp;" into "&amp;" and hides a second escape
// from a test that reads the parsed page only.
func TestTableHookEscapesOnce(t *testing.T) {
	raw, _ := readFixture(t, tablesBuild(t), "escaping")

	// The messages name the text, because the page is too long to print.
	for _, once := range []string{
		`aria-label="Fish &amp; Chips, a &lt; b"`,
		`<th scope="col">Fish &amp; Chips</th>`,
		`<th scope="col">a &lt; b</th>`,
		`aria-label="say &#34;hi&#34;, a&lt;b"`,
	} {
		require.True(t, strings.Contains(raw, once), "the page does not have %s", once)
	}
	for _, twice := range []string{"&amp;amp;", "&amp;lt;", "&amp;gt;", "&amp;quot;", "&amp;#"} {
		require.False(t, strings.Contains(raw, twice), "the page has %s: a sign is escaped twice", twice)
	}
}

// TestTableHookAlignment requires a class for the alignment of a column, on the
// header cell and on every cell of the column, and no inline style.
func TestTableHookAlignment(t *testing.T) {
	_, doc := readFixture(t, tablesBuild(t), "alignment")
	tables := tablesOf(doc)
	require.Len(t, tables, 1)

	want := []string{"", "align-left", "align-center", "align-right"}
	rows := 0
	for tr := range tables[0].Descendants() {
		if tr.Type != html.ElementNode || tr.Data != "tr" {
			continue
		}
		rows++
		var got []string
		for cell := tr.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type != html.ElementNode {
				continue
			}
			class, _ := attr(cell, "class")
			got = append(got, class)
			_, hasStyle := attr(cell, "style")
			require.False(t, hasStyle, "a <%s> has an inline style", cell.Data)
		}
		require.Equal(t, want, got, "row %d", rows)
	}
	require.Equal(t, 3, rows, "one header row and two rows of the body")
}

// TestTableHookBox requires, on every table of the build (the fixtures and the
// real pages), what the site check does not: the box is a plain box with no
// tabindex, no role and no name (a browser gives the focus to a box that
// scrolls, and the name is on the table), the header cells have scope="col",
// and no cell has an inline style.
func TestTableHookBox(t *testing.T) {
	dir := tablesBuild(t)
	pages := indexPages(t, dir)
	require.NotEmpty(t, pages, "no index.html in %s. Did the build run?", dir)

	seen := 0
	for _, rel := range pages {
		doc, err := parseFile(filepath.Join(dir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		for _, table := range tablesOf(doc) {
			seen++
			box := table.Parent
			require.True(t, hasClass(box, scrollBox), "%s: a table is not in a box", rel)
			class, _ := attr(box, "class")
			require.Equal(t, scrollBox, class, "%s: the box has other classes", rel)
			for _, name := range []string{"role", "tabindex", "aria-label", "style"} {
				_, has := attr(box, name)
				require.False(t, has, "%s: the box has %s", rel, name)
			}
			for cell := range table.Descendants() {
				if cell.Type != html.ElementNode || (cell.Data != "th" && cell.Data != "td") {
					continue
				}
				_, has := attr(cell, "style")
				require.False(t, has, "%s: a <%s> has an inline style", rel, cell.Data)
				if cell.Data == "th" {
					scope, _ := attr(cell, "scope")
					require.Equal(t, "col", scope, "%s: a <th> has no scope", rel)
				}
			}
		}
	}
	require.NotZero(t, seen, "the build has no table, so this test checked nothing")
}
