// Command sitecheck checks the built docs site. "make site" runs it after Hugo:
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
//   - The site loads nothing from another site, and has no script. That covers
//     every <link> (except a canonical one), the elements that load a page or
//     a media file, event attributes (onclick and the like), javascript:
//     addresses, and the @import and url() of the stylesheets.
//   - Every image has alt text.
//   - Every <table> has an aria-label that is not empty, and no other table of
//     the page has the same one, because a screen reader lists the tables by
//     name. Every <table> is also the only child element of a scroll box: an
//     element with the class table-scroll. A table that scrolls by itself needs
//     display:block, and then a browser or a screen reader may stop reading it
//     as a table. The box scrolls, and the table keeps its normal display. The
//     box has no tabindex and no role: a browser gives the focus to a box that
//     scrolls.
//   - Every link to the source code (blob or tree) goes to a path that exists
//     in the repository, and so does every code-area meta tag.
//   - Every regular page of the manual and the tech docs has a code-area, and a
//     link to itself in the page list (nav.docs-nav). A page that page-order.html
//     does not reach is built, but it is not in the list, and nothing else
//     fails. The built site has at least one regular page (data-kind="page"),
//     because without one these rules would check nothing and pass.
//   - Every screenshot that was made is used by a page.
//   - The Markdown files in site/content follow the content rules: no H1, no
//     heading deeper than H3, a language on every code fence, and short lines.
//   - The maps from code to docs page (mapsFile) name every Go package of cmd/
//     and internal/: the map of the tech docs in a row, and the map of the user
//     manual in a row or in its list of areas with no manual page. Every path
//     that the first column of a map names exists. A root with no go.mod is
//     not the Go module, so the small sites of the tests need no maps.
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
	"regexp"
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
	c.pageMaps()
	return c.problems
}

type checker struct {
	cfg      config
	base     string                     // cfg.Base with a slash at both ends
	ids      map[string]map[string]bool // the ids of each page that a link went to
	regular  int                        // how many pages have data-kind="page"
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

// pages checks every HTML page and every stylesheet of the built site.
func (c *checker) pages() {
	count := 0
	err := filepath.WalkDir(c.cfg.Public, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Ext(file) {
		case ".css":
			// The same walk reads the stylesheets, so a built site that
			// cannot be read is one problem and not two.
			c.stylesheet(file)
		case ".html":
			rel, err := filepath.Rel(c.cfg.Public, file)
			if err != nil {
				return err
			}
			count++
			c.page(file, filepath.ToSlash(rel))
		}
		return nil
	})
	switch {
	case err != nil:
		c.add(c.cfg.Public, "cannot read the built site: %v", err)
	case count == 0:
		c.add(c.cfg.Public, "no HTML page found. Did the build run?")
	case c.regular == 0:
		// The code-area rule runs only on regular pages. If the shell loses
		// its data-kind marker, no page is regular and the rule would pass
		// with no sign, so the missing marker is a problem on its own.
		c.add(c.cfg.Public, `no page has data-kind="page". Does the page shell still print it?`)
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
	var codeAreas, tables int
	names := map[string]int{} // the aria-label of each table, and its number
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		c.attributes(file, n)
		for _, name := range loaders[n.Data] {
			if ref, _ := attr(n, name); ref != "" {
				c.resource(file, pageURL, "<"+n.Data+" "+name+">", ref)
			}
		}
		switch n.Data {
		case "a":
			if href, ok := attr(n, "href"); ok {
				c.link(file, pageURL, href)
			}
		case "script":
			c.add(file, "<script> found: the site has no script")
		case "table":
			tables++
			c.table(file, n, tables, names)
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
		case "image":
			// The parser turns an HTML <image> into an <img>, so this is the
			// <image> of an SVG. It has href, or xlink:href in an old drawing,
			// and the parser gives both the name href.
			if n.Namespace == "svg" {
				for _, a := range n.Attr {
					if a.Key == "href" && a.Val != "" {
						c.resource(file, pageURL, "<image href>", a.Val)
					}
				}
			}
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
	if kind == "page" {
		c.regular++
	}
	if kind == "page" && codeAreaRequired(rel) && codeAreas == 0 {
		c.add(file, "the page has no code-area meta (add code_areas to its front matter)")
	}
	if kind == "page" && inDocs(rel) && !inPageList(doc, pageURL) {
		c.add(file, "the page is not in the page list (nav.docs-nav has no link to it). "+
			"Is it outside the groups, and the one level under them, that page-order.html walks?")
	}
}

// scrollBox is the class of the box around every table. The table hook of the
// site (layouts/_markup/render-table.html) writes the box, and the stylesheet
// makes it scroll.
const scrollBox = "table-scroll"

// table checks a <table>. It must have an aria-label that is not empty and
// different from the aria-label of every earlier table of the page, and it must
// be the only child element of a scroll box. number is the place of the table
// in the page, to tell the tables apart in a message. names holds the aria-label
// of each earlier table of the page, with its number, and table adds this one.
// Each broken rule is a problem of its own.
func (c *checker) table(file string, n *html.Node, number int, names map[string]int) {
	label, _ := attr(n, "aria-label")
	label = strings.TrimSpace(label)
	switch earlier, seen := names[label]; {
	case label == "":
		c.add(file, "table %d has no aria-label, so a screen reader cannot name it", number)
	case seen:
		c.add(file, "table %d has the same aria-label as table %d: %q. "+
			"A screen reader lists the tables by name, so each name must be different", number, earlier, label)
	default:
		names[label] = number
	}

	box := n.Parent
	if box == nil || box.Type != html.ElementNode || !hasClass(box, scrollBox) {
		c.add(file, "table %d is not the child of a box with the class %s. "+
			"Does the table hook (render-table.html) still draw it?", number, scrollBox)
		return
	}
	elements := 0
	for child := box.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			elements++
		}
	}
	if elements != 1 {
		c.add(file, "table %d: its box has %d child elements, and the table must be the only one", number, elements)
	}
}

// hasClass tells if the class attribute of n has name as one of its words.
func hasClass(n *html.Node, name string) bool {
	class, _ := attr(n, "class")
	return slices.Contains(strings.Fields(class), name)
}

// loaders lists, for each element that loads a page or a media file, the
// attributes that hold an address. <img>, <source> and <link> have their own
// code, because they need more than the address.
var loaders = map[string][]string{
	"iframe": {"src"},
	"embed":  {"src"},
	"object": {"data"},
	"video":  {"src", "poster"},
	"audio":  {"src"},
	"track":  {"src"},
}

// attributes checks the attributes of any element. The site has no script, and
// script can hide in two kinds of attribute: the event attributes, which all
// start with "on", and an address that starts with "javascript:".
func (c *checker) attributes(file string, n *html.Node) {
	for _, a := range n.Attr {
		if strings.HasPrefix(a.Key, "on") {
			c.add(file, "<%s %s> is an event attribute: the site has no script", n.Data, a.Key)
		}
		if (a.Key == "href" || a.Key == "src") && isJavaScriptAddress(a.Val) {
			c.add(file, "<%s %s> %q starts with javascript: and the site has no script", n.Data, a.Key, a.Val)
		}
	}
}

// isJavaScriptAddress tells if a browser would run v as script. The browser
// ignores the case of the letters, the spaces and control characters at the
// start, and every tab and new line inside, so the check does too.
func isJavaScriptAddress(v string) bool {
	v = strings.TrimLeftFunc(v, func(r rune) bool { return r <= ' ' })
	v = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, v)
	return strings.HasPrefix(strings.ToLower(v), "javascript:")
}

// inPageList tells if the page list of the page (the nav with the class
// docs-nav) has a link to the page itself. The list of a page that
// page-order.html does not reach cannot have it. The pager and the top menu
// are other navs, and a link in them does not count.
func inPageList(doc *html.Node, pageURL string) bool {
	for nav := range doc.Descendants() {
		if nav.Type != html.ElementNode || nav.Data != "nav" {
			continue
		}
		if !hasClass(nav, "docs-nav") {
			continue
		}
		for a := range nav.Descendants() {
			if a.Type != html.ElementNode || a.Data != "a" {
				continue
			}
			href, ok := attr(a, "href")
			if !ok {
				continue
			}
			u, err := url.Parse(href)
			if err != nil || u.Host != "" || u.Scheme != "" {
				continue
			}
			if (&url.URL{Path: pageURL}).ResolveReference(u).Path == pageURL {
				return true
			}
		}
	}
	return false
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

// inDocs says if a page is a page of the manual or of the tech docs.
func inDocs(rel string) bool {
	return strings.HasPrefix(rel, "manual/") || strings.HasPrefix(rel, "tech/")
}

// codeAreaRequired says if a regular page must say which code it covers. The
// manual and the tech docs need it, except the glossaries, which cover no code.
// The glossaries are only free of the code-area. They are in the page list like
// every other page.
func codeAreaRequired(rel string) bool {
	return inDocs(rel) && !strings.HasSuffix("/"+pagePath(rel), "/glossary/")
}

// link checks an <a href>. A link with no host and no scheme goes to this site.
// A source link goes to the repository. Other links are allowed: the site may
// link to anything, but it may not load anything from outside.
func (c *checker) link(file, pageURL, href string) {
	if isJavaScriptAddress(href) {
		return // attributes reports it, and a second message would only repeat it
	}
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

// linkTag checks a <link>. Whatever its rel, a link with an href may make the
// browser load a file, so the address must stay inside the site. The one free
// kind is a canonical link, which only says where the page lives. It has a full
// address, and the browser does not load it. A link that has other kinds too,
// such as rel="canonical preload", is not free.
func (c *checker) linkTag(file, pageURL string, n *html.Node) {
	href, ok := attr(n, "href")
	if !ok {
		return
	}
	rel, _ := attr(n, "rel")
	if kinds := strings.Fields(strings.ToLower(rel)); len(kinds) == 1 && kinds[0] == "canonical" {
		return
	}
	c.resource(file, pageURL, `<link rel="`+rel+`" href>`, href)
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

// cssURL finds the url(...) of a stylesheet. The address is in one of three
// groups: in double quotes, in single quotes, or with no quotes. Comments are
// not skipped on purpose: a rule that skips them could be fooled by one.
var cssURL = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s"']*))\s*\)`)

// cssScheme matches the start of an address that has a scheme, like https:.
var cssScheme = regexp.MustCompile(`^[a-z][a-z0-9+.\-]*:`)

// stylesheet checks a CSS file of the built site. A page can load nothing from
// another site, and neither can its stylesheet. An @import is a problem even
// when it points inside the site, because this check does not follow it. A
// url() with a scheme or with // at the start goes to another site. A data:
// address holds its own bytes and loads nothing.
func (c *checker) stylesheet(file string) {
	data, err := os.ReadFile(file) //nolint:gosec // G304: the walk of the built site gave this path
	if err != nil {
		c.add(file, "cannot read the stylesheet: %v", err)
		return
	}
	css := string(data)
	if strings.Contains(strings.ToLower(css), "@import") {
		c.add(file, "@import found: the stylesheet must not load another file")
	}
	for _, m := range cssURL.FindAllStringSubmatch(css, -1) {
		value := strings.TrimSpace(m[1] + m[2] + m[3]) // only one group has text
		if cssLoadsFromOutside(value) {
			c.add(file, "url(%s) loads from another site", value)
		}
	}
}

// cssLoadsFromOutside tells if the address of a url() goes to another site. A
// backslash counts as a slash, because a browser reads /\host/ as //host/.
func cssLoadsFromOutside(value string) bool {
	value = strings.ReplaceAll(strings.ToLower(value), `\`, "/")
	if strings.HasPrefix(value, "data:") {
		return false
	}
	return strings.HasPrefix(value, "//") || cssScheme.MatchString(value)
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

// markdown checks the content rules on one file: no H1 (written with # or with
// a line of =), nothing deeper than H3, a language on every code fence, and
// lines of at most maxLine characters. The front matter, the code fences and
// the tables are skipped, because they have their own syntax and they cannot
// always wrap.
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
	fence := ""        // the fence that is open, or empty
	paragraph := false // the line before is a line of a paragraph
	for i := start; i < len(lines); i++ {
		line, number := lines[i], i+1
		wasParagraph := paragraph
		paragraph = false
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if f := fenceOf(line); f != "" {
			fence = f
			if fenceLanguage(line, f) == "" {
				c.add(file, "line %d: code fence with no language. Write one after the fence, such as sh or text", number)
			}
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "|") {
			continue
		}
		underline := wasParagraph && isSetextUnderline(line)
		if underline {
			c.add(file, "line %d: H1 in the body (a line of = under text). The title comes from the front matter", number)
		}
		// The underline belongs to the heading, so it is not text for the next line.
		paragraph = !underline && isParagraphLine(line)
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

// fenceLanguage returns the language of an opening fence line: the first word
// after the fence. Attributes in braces, like {linenos=true}, are not a
// language. It returns "" when the fence has none.
func fenceLanguage(line, fence string) string {
	info := strings.TrimLeft(strings.TrimSpace(line), fence[:1])
	if words := strings.Fields(info); len(words) > 0 && !strings.HasPrefix(words[0], "{") {
		return words[0]
	}
	return ""
}

// isSetextUnderline tells if line is a line of = signs, with at most three
// spaces before it. Under a line of a paragraph, it makes that paragraph an H1.
func isSetextUnderline(line string) bool {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 {
		return false // four spaces start a code block
	}
	s = strings.TrimRight(s, " \t")
	return s != "" && strings.Trim(s, "=") == ""
}

// isParagraphLine tells if line can be the text of a setext heading: it is not
// blank, not an ATX heading, and not the start of a quote or a list item. A
// line of = under those is text and not a heading.
func isParagraphLine(line string) bool {
	s := strings.TrimLeft(line, " \t")
	if s == "" || headingLevel(line) > 0 || strings.HasPrefix(s, ">") {
		return false
	}
	if len(s) > 1 && strings.ContainsRune("-+*", rune(s[0])) && s[1] == ' ' {
		return false
	}
	digits := len(s) - len(strings.TrimLeft(s, "0123456789"))
	if digits > 0 && digits < len(s) && (s[digits] == '.' || s[digits] == ')') &&
		(digits+1 == len(s) || s[digits+1] == ' ') {
		return false
	}
	return true
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
