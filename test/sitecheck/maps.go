package main

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// mapsFile is the page with the two maps from code to docs page, as a path
// from the root of the repository. The section "Which page do I update?" of
// that page holds them.
const mapsFile = "site/content/tech/work-on-the-code/local-development.md"

// The headings of the two maps, and the start of the sentence that lists the
// areas with no manual page.
const (
	techMapHeading   = "Map of the tech docs"
	manualMapHeading = "Map of the user manual"
	noManualStart    = "These areas have no manual page:"
)

// codeSpan finds the text between two backquotes, such as `internal/web`.
var codeSpan = regexp.MustCompile("`([^`]+)`")

// pageMaps checks the two maps of mapsFile against the Go packages of the
// repository. The maps are written by hand, so without this rule a new
// package could have no row, and a renamed file could stay in a row.
func (c *checker) pageMaps() {
	// Only the root of the Go module has packages to map. The small sites of
	// the tests have no go.mod, so they need no page of the maps.
	if !c.exists("go.mod") {
		return
	}
	packages, err := goPackages(c.cfg.Root)
	if err != nil {
		c.add(c.cfg.Root, "cannot list the Go packages: %v", err)
		return
	}
	if len(packages) == 0 {
		return
	}
	file := filepath.Join(c.cfg.Root, filepath.FromSlash(mapsFile))
	data, err := os.ReadFile(file) //nolint:gosec // G304: a fixed path below -root
	if err != nil {
		c.add(file, "the page of the maps is missing or cannot be read: %v", err)
		return
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	techStart, techEnd, techFound := section(lines, techMapHeading)
	if !techFound {
		c.add(file, "the page has no section %q", techMapHeading)
	}
	manualStart, manualEnd, manualFound := section(lines, manualMapHeading)
	if !manualFound {
		c.add(file, "the page has no section %q", manualMapHeading)
	}
	if !techFound || !manualFound {
		return
	}
	tech := c.mapAreas(file, lines, techStart, techEnd)
	manual := c.mapAreas(file, lines, manualStart, manualEnd)
	for _, p := range packages {
		if !slices.ContainsFunc(tech, func(a string) bool { return a == p || isBelow(p, a) }) {
			c.add(file, "package %s has no row in the map of the tech docs", p)
		}
		// A row of the manual map often names one file of a package, so a
		// file directly in the folder of the package covers it too.
		if !slices.ContainsFunc(manual, func(a string) bool { return a == p || isBelow(p, a) || path.Dir(a) == p }) {
			c.add(file, "package %s has no row in the map of the user manual, and it is not in its list of areas with no manual page", p)
		}
	}
}

// goPackages returns the folders below cmd/ and internal/ that hold a Go file,
// test files included, as slash paths from root. A folder named testdata holds
// test input, and Go does not build it, so it is skipped. A missing cmd/ or
// internal/ is no error: the small sites of the tests have neither.
func goPackages(root string) ([]string, error) {
	var packages []string
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(file string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(file) != ".go" {
				return nil
			}
			rel, err := filepath.Rel(root, filepath.Dir(file))
			if err != nil {
				return err
			}
			if p := filepath.ToSlash(rel); !slices.Contains(packages, p) {
				packages = append(packages, p)
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	slices.Sort(packages)
	return packages, nil
}

// section returns the lines of the H3 with the text heading: from the line
// after the heading to the next H2 or H3, or to the end of the file.
func section(lines []string, heading string) (start, end int, found bool) {
	for i, line := range lines {
		if strings.TrimSpace(line) != "### "+heading {
			continue
		}
		for end = i + 1; end < len(lines); end++ {
			if strings.HasPrefix(lines[end], "## ") || strings.HasPrefix(lines[end], "### ") {
				break
			}
		}
		return i + 1, end, true
	}
	return 0, 0, false
}

// mapAreas returns the paths that a map names: the paths in the first column of
// its tables, and the paths of the sentence that starts with noManualStart. It
// adds a problem for a path that does not exist.
func (c *checker) mapAreas(file string, lines []string, start, end int) []string {
	var areas []string
	for i := start; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "|"):
			cells := strings.Split(line, "|")
			if len(cells) > 1 {
				areas = append(areas, c.cellAreas(file, i+1, cells[1])...)
			}
		case strings.HasPrefix(line, noManualStart):
			// The sentence can go on over the next lines of the paragraph. It
			// ends at the first full stop that a space or the end follows.
			text := line
			for j := i + 1; j < end && strings.TrimSpace(lines[j]) != ""; j++ {
				text += " " + strings.TrimSpace(lines[j])
			}
			if k := strings.Index(text+" ", ". "); k >= 0 {
				text = text[:k]
			}
			areas = append(areas, c.cellAreas(file, i+1, text)...)
		}
	}
	return areas
}

// cellAreas returns the paths that one cell of a map names, without a slash at
// the end. A cell can shorten a path: in "`cmd/email-mcp/account.go`,
// `connect.go`", the second name is in the folder of the first path of the
// cell. A name with no slash and no dot that is not a file of the root, such
// as `Instructions`, is not a path, and it is skipped.
func (c *checker) cellAreas(file string, number int, cell string) []string {
	var areas []string
	folder := ""
	for _, m := range codeSpan.FindAllStringSubmatch(cell, -1) {
		name := strings.TrimSuffix(m[1], "/")
		if strings.ContainsAny(name, " \t") {
			continue
		}
		pathLike := strings.ContainsAny(name, "/.")
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			c.add(file, "line %d: `%s` is not a path inside the repository", number, m[1])
			continue
		}
		area := ""
		switch {
		case c.exists(name):
			area = name
		case folder != "" && c.exists(path.Join(folder, name)):
			area = path.Join(folder, name)
		case pathLike:
			c.add(file, "line %d: `%s` does not exist in the repository", number, m[1])
			continue
		default:
			continue
		}
		if folder == "" {
			folder = path.Dir(area)
		}
		areas = append(areas, area)
	}
	return areas
}

// exists reports whether the slash path rel is a file or a folder of the
// repository.
func (c *checker) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(c.cfg.Root, filepath.FromSlash(rel)))
	return err == nil
}

// isBelow reports whether the slash path p is inside the folder dir.
func isBelow(p, dir string) bool {
	return strings.HasPrefix(p, dir+"/")
}
