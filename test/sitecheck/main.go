// Command sitecheck checks the built docs site. It is the last step of
// "make site":
//
//	go run ./test/sitecheck -public site/public -base /email-mcp-go/ -root .
//
// Hugo checks some things itself. This program checks what Hugo cannot see in
// the output:
//
//   - Every internal link, image and icon goes to a file that exists under the
//     base path, and every #anchor goes to an id that exists. A link written
//     as /manual/ fails when the site is served under /email-mcp-go/, because
//     it leaves the site.
//   - The site loads nothing from another site, and has no script.
//   - Every image has alt text.
//   - Every link to the source code (blob or tree) goes to a path that exists
//     in the repository, and so does every code-area meta tag.
//   - Every regular page of the manual and the tech docs has a code-area.
//   - Every screenshot that was made is used by a page.
//   - The Markdown files in site/content follow the content rules.
//
// It prints one problem on each line and exits with 1 when it finds any. A
// missing or wrong flag exits with 2.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// repoURL is the address of the repository. Links that start with it and then
// /blob/ or /tree/ are source links. site/hugo.toml has the same address in
// the parameter "repo".
const repoURL = "https://github.com/EmadMokhtar/email-mcp-go"

// maxLine is the longest line of a Markdown file, in characters.
const maxLine = 80

// config says where to look.
type config struct {
	Public string // the folder of the built site, site/public
	Base   string // the path where the site is served, such as /email-mcp-go/
	Root   string // the root of the repository
}

// problem is one thing that is wrong, and the file that has it.
type problem struct {
	File, Text string
}

func (p problem) String() string { return p.File + ": " + p.Text }

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is main without the process: it returns the exit code and writes the
// problems to out. A test can call it.
func run(args []string, out io.Writer) int {
	flags := flag.NewFlagSet("sitecheck", flag.ContinueOnError)
	flags.SetOutput(out)
	var cfg config
	flags.StringVar(&cfg.Public, "public", "site/public", "the folder of the built site")
	// There is no default for the base path. A wrong default would pass links
	// that fail on the real site.
	flags.StringVar(&cfg.Base, "base", "", "the path where the site is served, such as /email-mcp-go/ (required)")
	flags.StringVar(&cfg.Root, "root", ".", "the root of the repository")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if cfg.Base == "" {
		_, _ = fmt.Fprintln(out, "sitecheck: -base is required")
		return 2
	}
	problems := check(cfg)
	for _, p := range problems {
		_, _ = fmt.Fprintln(out, p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// check looks at the built site, the screenshots and the content, and returns
// every problem it finds. A part that cannot be read is a problem too, because
// a check that reads nothing would pass.
func check(cfg config) []problem {
	c := &checker{cfg: cfg, base: normalizeBase(cfg.Base), ids: map[string]map[string]bool{}}
	c.pages()
	c.screenshots()
	c.content()
	return c.problems
}

type checker struct {
	cfg      config
	base     string                     // cfg.Base with a slash at both ends
	ids      map[string]map[string]bool // the ids of each page that a link went to
	problems []problem
}

func (c *checker) add(file, format string, args ...any) {
	c.problems = append(c.problems, problem{File: file, Text: fmt.Sprintf(format, args...)})
}

// normalizeBase gives the base path a slash at both ends. Without the slash at
// the end, "/email-mcp-go" would also match "/email-mcp-gofoo/".
func normalizeBase(base string) string {
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base
}

// pages checks every HTML page of the built site.
func (c *checker) pages() {
	count := 0
	err := filepath.WalkDir(c.cfg.Public, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(file) != ".html" {
			return nil
		}
		rel, err := filepath.Rel(c.cfg.Public, file)
		if err != nil {
			return err
		}
		count++
		c.page(file, filepath.ToSlash(rel))
		return nil
	})
	switch {
	case err != nil:
		c.add(c.cfg.Public, "cannot read the built site: %v", err)
	case count == 0:
		c.add(c.cfg.Public, "no HTML page found. Did the build run?")
	}
}

// page checks one page. rel is the path of the file below the built site, with
// forward slashes.
func (c *checker) page(file, rel string) {
	doc, err := parseFile(file)
	if err != nil {
		c.add(file, "cannot read the page: %v", err)
		return
	}
	pageURL := c.base + pagePath(rel)
	var kind string
	var codeAreas int
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		switch n.Data {
		case "a":
			if href, ok := attr(n, "href"); ok {
				c.link(file, pageURL, href)
			}
		case "script":
			c.add(file, "<script> found: the site has no script")
		case "img":
			src, _ := attr(n, "src")
			if src != "" {
				c.resource(file, pageURL, "<img src>", src)
			}
			c.srcset(file, pageURL, n)
			if alt, _ := attr(n, "alt"); strings.TrimSpace(alt) == "" {
				c.add(file, "<img src=%q> has no alt text", src)
			}
		case "source":
			if src, ok := attr(n, "src"); ok {
				c.resource(file, pageURL, "<source src>", src)
			}
			c.srcset(file, pageURL, n)
		case "link":
			c.linkTag(file, pageURL, n)
		case "meta":
			if c.meta(file, pageURL, n) {
				codeAreas++
			}
		case "body":
			kind, _ = attr(n, "data-kind")
		}
	}
	if kind == "page" && codeAreaRequired(rel) && codeAreas == 0 {
		c.add(file, "the page has no code-area meta (add code_areas to its front matter)")
	}
}

// pagePath is the address of a page below the base path: "manual/" for
// manual/index.html.
func pagePath(rel string) string {
	if rel == "index.html" {
		return ""
	}
	if dir, ok := strings.CutSuffix(rel, "/index.html"); ok {
		return dir + "/"
	}
	return rel
}

// codeAreaRequired says if a regular page must say which code it covers. The
// manual and the tech docs need it, except the glossaries, which cover no code.
func codeAreaRequired(rel string) bool {
	if !strings.HasPrefix(rel, "manual/") && !strings.HasPrefix(rel, "tech/") {
		return false
	}
	return !strings.HasSuffix("/"+pagePath(rel), "/glossary/")
}

// link checks an <a href>. A link with no host and no scheme goes to this site.
// A source link goes to the repository. Other links are allowed: the site may
// link to anything, but it may not load anything from outside.
func (c *checker) link(file, pageURL, href string) {
	if p, ok := sourcePath(href); ok {
		c.sourceLink(file, href, p)
		return
	}
	u, err := url.Parse(href)
	if err != nil {
		c.add(file, "<a href> %q is not a valid address: %v", href, err)
		return
	}
	if u.Host != "" || u.Scheme != "" {
		return
	}
	c.internal(file, pageURL, "<a href>", href, u)
}

// resource checks an address that the browser loads. It must stay inside the
// site and go to a file that exists. A data: address has no host, and loads
// nothing, so it passes.
func (c *checker) resource(file, pageURL, what, ref string) {
	u, err := url.Parse(ref)
	if err != nil {
		c.add(file, "%s %q is not a valid address: %v", what, ref, err)
		return
	}
	if u.Host != "" || (u.Scheme != "" && u.Scheme != "data") {
		c.add(file, "%s %q loads from another site", what, ref)
		return
	}
	if u.Scheme == "data" {
		return
	}
	c.internal(file, pageURL, what, ref, u)
}

// srcset checks the addresses in the srcset of an <img> or a <source>. The
// list is separated by commas, and each item is an address and a size.
func (c *checker) srcset(file, pageURL string, n *html.Node) {
	set, _ := attr(n, "srcset")
	for item := range strings.SplitSeq(set, ",") {
		if fields := strings.Fields(item); len(fields) > 0 {
			c.resource(file, pageURL, "<"+n.Data+" srcset>", fields[0])
		}
	}
}

// linkTag checks a <link>. Only the kinds that the browser loads as part of the
// page are resources. A canonical link only says where the page lives, and it
// has a full address.
func (c *checker) linkTag(file, pageURL string, n *html.Node) {
	rel, _ := attr(n, "rel")
	href, ok := attr(n, "href")
	if !ok {
		return
	}
	kinds := strings.Fields(strings.ToLower(rel))
	if slices.Contains(kinds, "stylesheet") || slices.Contains(kinds, "icon") {
		c.resource(file, pageURL, `<link rel="`+rel+`" href>`, href)
	}
}

// meta checks a <meta>. It returns true for a code-area.
func (c *checker) meta(file, pageURL string, n *html.Node) bool {
	content, _ := attr(n, "content")
	name, _ := attr(n, "name")
	property, _ := attr(n, "property")
	switch {
	case name == "code-area":
		c.codeArea(file, content)
		return true
	case name == "og:image" || property == "og:image":
		// This address has a host, because a social network needs the full
		// address. The file is in the built site, so the host is dropped and
		// the rest is checked like any other address.
		u, err := url.Parse(content)
		if err != nil {
			c.add(file, "og:image %q is not a valid address: %v", content, err)
			return false
		}
		u.Scheme, u.Host = "", ""
		c.internal(file, pageURL, "og:image", content, u)
	}
	return false
}

// codeArea checks that the path of a code-area meta exists in the repository.
func (c *checker) codeArea(file, area string) {
	local := filepath.FromSlash(area)
	if !filepath.IsLocal(local) {
		c.add(file, "code-area %q is not a path inside the repository", area)
		return
	}
	if _, err := os.Stat(filepath.Join(c.cfg.Root, local)); err != nil {
		c.add(file, "code-area %q does not exist in the repository", area)
	}
}

// sourcePath tells if href is a link to a file or a folder of the repository,
// like <repo>/blob/<ref>/<path>. It returns the path with the ref removed.
func sourcePath(href string) (string, bool) {
	for _, kind := range []string{"/blob/", "/tree/"} {
		rest, ok := strings.CutPrefix(href, repoURL+kind)
		if !ok {
			continue
		}
		rest, _, _ = strings.Cut(rest, "#")
		rest, _, _ = strings.Cut(rest, "?")
		_, p, _ := strings.Cut(rest, "/")
		return p, true
	}
	return "", false
}

// sourceLink checks that the path of a source link exists in the repository.
func (c *checker) sourceLink(file, href, p string) {
	if p == "" {
		return // the top of the repository
	}
	if unescaped, err := url.PathUnescape(p); err == nil {
		p = unescaped
	}
	local := filepath.FromSlash(p)
	if !filepath.IsLocal(local) {
		c.add(file, "source link %q leaves the repository", href)
		return
	}
	if _, err := os.Stat(filepath.Join(c.cfg.Root, local)); err != nil {
		c.add(file, "source link %q goes to %s, which does not exist in the repository", href, p)
	}
}

// internal checks an address that goes to this site: it must stay under the
// base path, go to a file of the built site, and (for #name) go to an id.
// what names the place of the address, for the message.
func (c *checker) internal(file, pageURL, what, ref string, u *url.URL) {
	target := (&url.URL{Path: pageURL}).ResolveReference(u)
	rel, ok := strings.CutPrefix(target.Path, c.base)
	if !ok {
		c.add(file, "%s %q goes outside the base path %s", what, ref, c.base)
		return
	}
	found, ok := c.locate(rel)
	if !ok {
		c.add(file, "%s %q goes to a missing file: %s", what, ref, rel)
		return
	}
	if u.Fragment == "" || filepath.Ext(found) != ".html" {
		return
	}
	if !c.pageIDs(found)[u.Fragment] {
		c.add(file, "%s %q: %s has no element with id %q", what, ref, c.relPublic(found), u.Fragment)
	}
}

// locate finds the file that serves rel, an address below the base path. A
// folder is served by its index.html. A name with a slash at the end must be a
// folder.
func (c *checker) locate(rel string) (string, bool) {
	file := filepath.Join(c.cfg.Public, filepath.FromSlash(rel))
	info, err := os.Stat(file)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		return file, !strings.HasSuffix(rel, "/")
	}
	file = filepath.Join(file, "index.html")
	if _, err := os.Stat(file); err != nil {
		return "", false
	}
	return file, true
}

func (c *checker) relPublic(file string) string {
	rel, err := filepath.Rel(c.cfg.Public, file)
	if err != nil {
		return file
	}
	return filepath.ToSlash(rel)
}

// pageIDs returns the ids in a page. It reads each page once.
func (c *checker) pageIDs(file string) map[string]bool {
	if ids, ok := c.ids[file]; ok {
		return ids
	}
	ids := map[string]bool{}
	if doc, err := parseFile(file); err == nil {
		for n := range doc.Descendants() {
			if n.Type == html.ElementNode {
				if id, ok := attr(n, "id"); ok {
					ids[id] = true
				}
			}
		}
	}
	// A page that cannot be read has no ids. The page loop reports it.
	c.ids[file] = ids
	return ids
}

// screenshots checks that every screenshot that was made is in the built site.
// The shortcode publishes an image only when a page uses it, so a missing file
// means that no page does. A missing folder means that no screenshot was made,
// which is not a problem: a site with no screenshot is valid.
func (c *checker) screenshots() {
	dir := filepath.Join(c.cfg.Root, "site", "assets", "screenshots")
	err := filepath.WalkDir(dir, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(file) != ".png" {
			return nil
		}
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(c.cfg.Public, "screenshots", rel)); err != nil {
			c.add(file, "no page uses this screenshot: the built site has no screenshots/%s", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.add(dir, "cannot read the screenshots: %v", err)
	}
}

// content checks the Markdown files of the site. Unlike the screenshots, the
// folder must exist: a missing folder means that -root is wrong.
func (c *checker) content() {
	dir := filepath.Join(c.cfg.Root, "site", "content")
	err := filepath.WalkDir(dir, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(file) == ".md" {
			c.markdown(file)
		}
		return nil
	})
	if err != nil {
		c.add(dir, "cannot read the content: %v", err)
	}
}

// markdown checks the content rules on one file: no H1, nothing deeper than
// H3, and lines of at most maxLine characters. The front matter, the code
// fences and the tables are skipped, because they have their own syntax and
// they cannot always wrap.
func (c *checker) markdown(file string) {
	data, err := os.ReadFile(file) //nolint:gosec // G304: the walk of site/content gave this path
	if err != nil {
		c.add(file, "cannot read the file: %v", err)
		return
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	start := afterFrontMatter(lines)
	fence := "" // the fence that is open, or empty
	for i := start; i < len(lines); i++ {
		line, number := lines[i], i+1
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if f := fenceOf(line); f != "" {
			fence = f
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "|") {
			continue
		}
		if level := headingLevel(line); level == 1 {
			c.add(file, "line %d: H1 in the body. The title comes from the front matter", number)
		} else if level > 3 {
			c.add(file, "line %d: heading deeper than H3", number)
		}
		if n := utf8.RuneCountInString(line); n > maxLine {
			c.add(file, "line %d: %d characters, the limit is %d", number, n, maxLine)
		}
	}
}

// afterFrontMatter returns the index of the first line after the front matter.
// Front matter is a block between two lines of "---" (YAML) or "+++" (TOML) at
// the top of the file. A file with none starts at line 0.
func afterFrontMatter(lines []string) int {
	if len(lines) == 0 || (lines[0] != "---" && lines[0] != "+++") {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == lines[0] {
			return i + 1
		}
	}
	return 0 // never closed: it is not front matter
}

// fenceOf returns the fence that line opens (three or more backticks or
// tildes), or "" when the line opens none. The indent has no limit, because a
// fence inside a list item is indented.
func fenceOf(line string) string {
	s := strings.TrimLeft(line, " \t")
	for _, mark := range []string{"`", "~"} {
		n := len(s) - len(strings.TrimLeft(s, mark))
		if n < 3 {
			continue
		}
		// ```code``` on one line is inline code, not a fence.
		if mark == "`" && strings.Contains(s[n:], "`") {
			return ""
		}
		return strings.Repeat(mark, n)
	}
	return ""
}

// closesFence tells if line closes the open fence: only the same mark, at least
// as many times.
func closesFence(line, fence string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, fence) && strings.Trim(s, fence[:1]) == ""
}

// headingLevel returns the level of an ATX heading ("## Text" is 2), or 0 when
// the line is not a heading. A "#" with no space after it is text, like "#T13".
func headingLevel(line string) int {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 {
		return 0 // four spaces start a code block
	}
	n := len(s) - len(strings.TrimLeft(s, "#"))
	if n == 0 || n > 6 {
		return 0
	}
	if rest := s[n:]; rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0
	}
	return n
}

// parseFile reads an HTML file.
func parseFile(file string) (*html.Node, error) {
	f, err := os.Open(file) //nolint:gosec // G304: the walk of the built site gave this path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	doc, err := html.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return doc, nil
}

// attr returns the value of an attribute, and whether the element has it.
func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}
