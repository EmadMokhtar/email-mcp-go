package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// These tests keep the landing page true in the places that the build cannot
// check: the truth notes of its source, the theme-color of its head, and its
// preview card. They read the files of the repository, not the built site, so
// "go test" runs them without Hugo.

// The files, from this folder (test/sitecheck).
const (
	repoRoot       = "../.."
	landingSource  = repoRoot + "/site/content/_index.md"
	landingSpec    = repoRoot + "/docs/superpowers/specs/2026-11-05-landing-page-design.md"
	pageShell      = repoRoot + "/site/layouts/baseof.html"
	siteStylesheet = repoRoot + "/site/assets/css/site.css"
	cardSource     = repoRoot + "/site/assets/social-card.svg"
	cardRecord     = repoRoot + "/site/assets/social-card.png.sha256"
	siteDir        = repoRoot + "/site"
)

// ---------------------------------------------------------------- truth notes

var (
	// truthShortcode is a note of the body: {{< truth "T13" >}}.
	truthShortcode = regexp.MustCompile(`\{\{<\s*truth\s+"(T[0-9]+)"\s*>\}\}`)
	// truthComment is a note of the front matter: a YAML comment at the end of
	// a line that holds only row names, such as "# T13" or "# T4 T5".
	truthComment = regexp.MustCompile(`(?m)#((?:[ \t]+T[0-9]+)+)[ \t]*$`)
	// rowName is one row name inside a comment.
	rowName = regexp.MustCompile(`T[0-9]+`)
	// truthRow is a row of the truth table in the spec: a line that starts with
	// "**T13.".
	truthRow = regexp.MustCompile(`(?m)^\*\*(T[0-9]+)\.`)
)

// truthNotes returns the row names that the source of the landing page notes,
// in the front matter (YAML comments) and in the body (the shortcode truth),
// each once and sorted.
func truthNotes(source string) (frontMatter, body []string) {
	matter, text := splitFrontMatter(source)
	for _, m := range truthComment.FindAllStringSubmatch(matter, -1) {
		frontMatter = append(frontMatter, rowName.FindAllString(m[1], -1)...)
	}
	for _, m := range truthShortcode.FindAllStringSubmatch(text, -1) {
		body = append(body, m[1])
	}
	slices.Sort(frontMatter)
	slices.Sort(body)
	return slices.Compact(frontMatter), slices.Compact(body)
}

// missingRows returns the notes that have no row in the spec.
func missingRows(notes []string, spec string) []string {
	rows := map[string]bool{}
	for _, m := range truthRow.FindAllStringSubmatch(spec, -1) {
		rows[m[1]] = true
	}
	var missing []string
	for _, note := range notes {
		if !rows[note] {
			missing = append(missing, note)
		}
	}
	return missing
}

// Each note of the landing page names a row that the truth table of the spec
// has. The shortcode truth checks only that a name looks like "T13".
func TestLandingTruthNotesHaveRows(t *testing.T) {
	requireSiteFolder(t)
	source := readRepoFile(t, landingSource)
	spec := readRepoFile(t, landingSpec)

	frontMatter, body := truthNotes(source)
	require.NotEmpty(t, frontMatter, "no # Tnn note found in the front matter: did the format change?")
	require.NotEmpty(t, body, `no {{< truth "Tnn" >}} note found in the body: did the format change?`)
	notes := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(frontMatter), body...))))
	require.Empty(t, missingRows(notes, spec),
		"these notes of %s have no row (a line that starts with **Tnn.) in %s", landingSource, landingSpec)

	// The proof that the check sees a missing row: the same notes, with a copy
	// of the spec in memory that lost the row of the first note.
	first := notes[0]
	lost := strings.Replace(spec, "**"+first+".", "**Gone.", 1)
	require.NotEqual(t, spec, lost)
	require.Equal(t, []string{first}, missingRows(notes, lost), "a spec without the row %s must fail the check", first)
}

// ---------------------------------------------------------------- theme-color

var (
	// groundDay is --ground in the first :root block of the stylesheet.
	groundDay = regexp.MustCompile(`(?s)^.*?:root\s*\{[^}]*?--ground:\s*(#[0-9a-fA-F]{6})\s*;`)
	// groundNight is --ground in the :root block of the dark media query.
	groundNight = regexp.MustCompile(`(?s)@media\s*\(prefers-color-scheme:\s*dark\)\s*\{\s*:root\s*\{[^}]*?--ground:\s*(#[0-9a-fA-F]{6})\s*;`)
)

// The theme-color tags of the page shell equal the ground colour of the
// stylesheet, by day and at night. A meta tag cannot read a CSS variable, so
// the value is written twice, and this test keeps the two equal.
func TestLandingThemeColorIsTheGround(t *testing.T) {
	requireSiteFolder(t)
	css := readRepoFile(t, siteStylesheet)
	day := groundDay.FindStringSubmatch(css)
	night := groundNight.FindStringSubmatch(css)
	require.NotNil(t, day, "no --ground in the first :root block of %s", siteStylesheet)
	require.NotNil(t, night, "no --ground in the dark :root block of %s", siteStylesheet)

	colors := themeColors(t, readRepoFile(t, pageShell))
	require.Equal(t, map[string]string{
		"(prefers-color-scheme: light)": strings.ToLower(day[1]),
		"(prefers-color-scheme: dark)":  strings.ToLower(night[1]),
	}, colors, "the theme-color tags of %s must equal --ground of %s", pageShell, siteStylesheet)
}

// themeColors returns the content of each <meta name="theme-color">, by its
// media.
func themeColors(t *testing.T, page string) map[string]string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	colors := map[string]string{}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Meta && attrValue(n, "name") == "theme-color" {
			colors[attrValue(n, "media")] = strings.ToLower(attrValue(n, "content"))
		}
	}
	return colors
}

// ---------------------------------------------------------------- social card

var (
	// quotedKey is a front matter key with a value in double quotes.
	quotedKey = `(?m)^%s:\s*"([^"]*)"`
	// listItem is one item of a YAML list, without its comment.
	listItem = regexp.MustCompile(`^\s+-\s+(.*?)(?:\s+#.*)?$`)
	// t3Sentence is the sentence of the lead just before the note T3.
	t3Sentence = regexp.MustCompile(`([^.{}]*\.)\{\{<\s*truth\s+"T3"\s*>\}\}`)
	// heroLead is the Markdown between the tags of the shortcode hero.
	heroLead = regexp.MustCompile(`(?s)\{\{<\s*hero\s*>\}\}(.*?)\{\{<\s*/hero\s*>\}\}`)
)

// The preview card shows the words of the landing page: its description (the
// og:image:alt) is card_alt, then the name, the two lines of the headline, the
// title of the page after "email-mcp: ", and the sentence with the note T3.
func TestLandingSocialCardHasTheWordsOfThePage(t *testing.T) {
	requireSiteFolder(t)
	source := readRepoFile(t, landingSource)
	matter, body := splitFrontMatter(source)

	title := quotedValue(t, matter, "title")
	alt := quotedValue(t, matter, "card_alt")
	name, rest, ok := strings.Cut(title, ": ")
	require.True(t, ok, "the title %q must be the name, a colon and what the tool does", title)
	lead := heroLead.FindStringSubmatch(body)
	require.NotNil(t, lead, "the body has no hero")
	sentence := t3Sentence.FindStringSubmatch(lead[1])
	require.NotNil(t, sentence, "the lead has no sentence with the note T3")

	want := append([]string{name}, headline(t, matter)...)
	want = append(want, upperFirst(rest)+".", strings.Join(strings.Fields(sentence[1]), " "))

	cardTitle, lines := cardText(t, readRepoFile(t, cardSource))
	require.Equal(t, alt, cardTitle, "the <title> of %s must be card_alt", cardSource)
	require.Equal(t, want, lines, "the lines of %s must be the words of the page", cardSource)
}

// headline returns the items of hero.headline in the front matter.
func headline(t *testing.T, matter string) []string {
	t.Helper()
	var items []string
	lines := strings.Split(matter, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "headline:" {
			continue
		}
		for _, next := range lines[i+1:] {
			m := listItem.FindStringSubmatch(next)
			if m == nil {
				break
			}
			items = append(items, m[1])
		}
	}
	require.NotEmpty(t, items, "the front matter has no hero.headline")
	return items
}

// cardText returns the <title> of the card and the text of each <text>, in
// order.
func cardText(t *testing.T, svg string) (string, []string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(svg))
	require.NoError(t, err)
	var title string
	var lines []string
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Namespace != "svg" {
			continue
		}
		switch n.Data {
		case "title":
			title = textOf(n)
		case "text":
			lines = append(lines, textOf(n))
		}
	}
	return title, lines
}

// The PNG of the card was made from the SVG as it is now: the record that
// "make social-card" writes holds the SHA-256 of the SVG. A change of the SVG
// without a new PNG fails here.
func TestLandingSocialCardWasMadeFromThisSVG(t *testing.T) {
	requireSiteFolder(t)
	svg := []byte(readRepoFile(t, cardSource))
	record := readRepoFile(t, cardRecord)
	require.NoError(t, cardRecordMatches(svg, record),
		"%s changed after the PNG was made. Run make social-card, and commit the PNG and the record", cardSource)

	// The proof that the check sees a change: a copy of the SVG in memory with
	// one more character.
	changed := append(slices.Clone(svg), '\n')
	require.Error(t, cardRecordMatches(changed, record), "a changed SVG with the old record must fail the check")
}

// cardRecordMatches tells if record, in the format of sha256sum ("<hex>
// <file>"), holds the SHA-256 of svg.
func cardRecordMatches(svg []byte, record string) error {
	fields := strings.Fields(record)
	if len(fields) == 0 {
		return errors.New("the record is empty")
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(svg)); fields[0] != want {
		return fmt.Errorf("the record has %s, and the SVG has %s", fields[0], want)
	}
	return nil
}

// ---------------------------------------------------------------- helpers

// splitFrontMatter returns the YAML between the first two lines of "---", and
// the text after them.
func splitFrontMatter(source string) (matter, body string) {
	rest, ok := strings.CutPrefix(source, "---\n")
	if !ok {
		return "", source
	}
	matter, body, ok = strings.Cut(rest, "\n---\n")
	if !ok {
		return "", source
	}
	return matter, body
}

// quotedValue returns the value of a front matter key that has its value in
// double quotes.
func quotedValue(t *testing.T, matter, key string) string {
	t.Helper()
	m := regexp.MustCompile(fmt.Sprintf(quotedKey, regexp.QuoteMeta(key))).FindStringSubmatch(matter)
	require.NotNil(t, m, "the front matter has no %s in double quotes", key)
	return m[1]
}

// upperFirst returns s with its first letter in upper case.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// textOf returns the text inside n, with each run of white space made one
// space.
func textOf(n *html.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			b.WriteString(d.Data)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// attrValue returns the value of an attribute, or "".
func attrValue(n *html.Node, key string) string {
	v, _ := attr(n, key)
	return v
}

// readRepoFile reads a file of the repository.
func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(path)) //nolint:gosec // G304: the paths are constants of this test
	require.NoError(t, err, "cannot read %s", path)
	return string(data)
}

// requireSiteFolder skips the test when the site folder is missing outside
// CI, as in a source archive with no site. In CI (the variable CI is set, as
// GitHub Actions does) the folder must be there: a missing folder means that
// it was moved, and a skip would turn these checks off with no sign.
func requireSiteFolder(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.FromSlash(siteDir)); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if os.Getenv("CI") != "" {
		require.FailNow(t, "the site/ folder is missing, and CI is set, so the landing page cannot be checked. If the site moved, change the paths of this test")
	}
	t.Skip("the site/ folder is missing (for example in a source archive)")
}
