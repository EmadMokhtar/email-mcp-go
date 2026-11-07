//go:build screenshots

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/chrometest"
)

// The size of a small phone, in pixels.
const (
	phoneWidth  = 375
	phoneHeight = 812
)

// TestNoSidewaysScroll opens pages in Chrome, at the width of a phone, and
// requires that the page does not scroll sideways. A long line of code or a
// wide image must stay inside its own box. Only a browser can see this, so the
// site check cannot. "make site" runs this test after the build, with the folder
// of the built site and its base path.
//
// It is one test with one Chrome, because a Chrome takes a few seconds to start.
// The subtests share it:
//
//   - "built pages": every page of the built site.
//   - "wide content": pages that hold wide things. The built site may not hold
//     the widest case of each kind: a table with twelve columns, or a code line
//     of 200 characters. So the test makes its own pages. Each one is a real page
//     of the built site, with its real layout and its real stylesheet, and one
//     wide thing in the place of the text. Each wide thing has a page of its
//     own, so a failure names the thing that broke out of its box.
func TestNoSidewaysScroll(t *testing.T) {
	public, base := builtSite(t)

	pages := indexPages(t, public)
	require.NotEmpty(t, pages, "no index.html in %s. Did the build run?", public)

	cases := []struct{ name, path, content string }{
		{"code block", "code-block/", "<pre><code>" + wideCommand + "</code></pre>"},
		{"table", "table/", wideTable()},
		{"inline code", "inline-code/", "<p>Set <code>" + wideWord + "</code> before you start.</p>"},
		{"link", "link/", `<p>Read <a href="` + wideLink + `">` + wideLink + `</a> first.</p>`},
	}

	// The pages of the wide things are ready before the server starts, and the
	// server only reads them. Every other address is a file of the built site, so
	// the pages find their real stylesheet. The prefix has no slash at the end, so
	// "/manual/" stays a full path for the file server. A request outside the base
	// path gets a 404, as on the real site.
	const fixtureDir = "wide-content/"
	fixtures := map[string]string{}
	for _, c := range cases {
		fixtures[base+fixtureDir+c.path] = pageWithContent(t, public, c.content)
	}
	files := http.StripPrefix(strings.TrimSuffix(base, "/"), http.FileServer(http.Dir(public)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := fixtures[r.URL.Path]
		if !ok {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	}))
	defer server.Close()

	ctx, cancel := chrometest.Start(t)
	defer cancel()
	require.NoError(t, chromedp.Do(ctx, chromedp.EmulateViewport(phoneWidth, phoneHeight)))

	t.Run("built pages", func(t *testing.T) {
		for _, rel := range pages {
			t.Run(rel, func(t *testing.T) {
				requireNoSidewaysScroll(t, ctx, server.URL+base+pagePath(rel))
			})
		}
	})
	t.Run("wide content", func(t *testing.T) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				requireNoSidewaysScroll(t, ctx, server.URL+base+fixtureDir+c.path)
			})
		}
	})
}

// builtSite returns the folder of the built site and the base path where it is
// served, from the environment that "make site" sets.
func builtSite(t *testing.T) (public, base string) {
	t.Helper()
	public = os.Getenv("SITE_PUBLIC_DIR")
	require.NotEmpty(t, public, "SITE_PUBLIC_DIR must name the built site")
	basePath := os.Getenv("SITE_BASE_PATH")
	require.NotEmpty(t, basePath, "SITE_BASE_PATH must be the path where the site is served")
	return public, normalizeBase(basePath)
}

// requireNoSidewaysScroll opens a page in the browser of ctx and requires that
// the page itself is not wider than the window. A box inside the page may
// scroll sideways. The page may not.
func requireNoSidewaysScroll(t *testing.T, ctx context.Context, address string) {
	t.Helper()
	resp, err := chromedp.Run(ctx, chromedp.NavigateResponse(address))
	require.NoError(t, err)
	require.Equal(t, int64(http.StatusOK), resp.Status)

	size, err := chromedp.Run(ctx, chromedp.Evaluate[[]int](
		`[document.documentElement.scrollWidth, document.documentElement.clientWidth]`))
	require.NoError(t, err)
	scrollWidth, clientWidth := size[0], size[1]
	// At another width this test would check nothing.
	require.Equal(t, phoneWidth, clientWidth, "the width of the page")
	require.LessOrEqual(t, scrollWidth, clientWidth, "the page scrolls sideways")
}

// The wide things that a page of the manual can hold. Each one is much longer
// than the window of a phone. The stylesheet must keep each one inside its box,
// with a scroll bar in the box or with a break of the line.
const (
	// A command with one very long line, as in a code block.
	wideCommand = "email-mcp serve --listen 127.0.0.1:8080 --option=value --option=value --option=value " +
		"--option=value --option=value --option=value --option=value --option=value --option=value " +
		"--option=value --option=value --option=value --option=value --option=value --option=value"

	// One word of about 200 characters, as a setting name in a line of text.
	wideWord = "EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_" +
		"EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_" +
		"EMAIL_MCP_SETTING_EMAIL_MCP_SETTING_EMAIL_MCP_SETTING"

	// A link with a long address. The text of the link is the address.
	wideLink = "https://example.com/segmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegment" +
		"segmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegmentsegment"

	// How many columns the wide table has. A phone shows two or three.
	wideColumns = 12
)

// wideTable returns a table with wideColumns columns, in the scroll box that
// the table hook of the site writes (layouts/_markup/render-table.html). The
// table scrolls in its box and not in the page, so the box is part of what this
// test checks.
func wideTable() string {
	var head, row strings.Builder
	for i := 1; i <= wideColumns; i++ {
		_, _ = fmt.Fprintf(&head, "<th>Column %d</th>", i)
		_, _ = fmt.Fprintf(&row, "<td>Value %d</td>", i)
	}
	return `<div class="table-scroll" role="region" tabindex="0" aria-label="Table: wide">` +
		"<table><thead><tr>" + head.String() + "</tr></thead><tbody><tr>" + row.String() + "</tr></tbody></table></div>"
}

// pageWithContent returns the HTML of one real page of the built site, with the
// text of the page replaced by content. The page is the first one that has the
// layout of a page of the manual or the tech docs: an article with the class
// "doc". The menu, the page list and the stylesheet stay as they are.
func pageWithContent(t *testing.T, public, content string) string {
	t.Helper()
	for _, rel := range indexPages(t, public) {
		doc, err := parseFile(filepath.Join(public, filepath.FromSlash(rel)))
		require.NoError(t, err)
		article := docArticle(doc)
		if article == nil {
			continue
		}
		for article.FirstChild != nil {
			article.RemoveChild(article.FirstChild)
		}
		nodes, err := html.ParseFragment(strings.NewReader("<h1>Wide content</h1>"+content), article)
		require.NoError(t, err)
		for _, n := range nodes {
			article.AppendChild(n)
		}
		var out bytes.Buffer
		require.NoError(t, html.Render(&out, doc))
		return out.String()
	}
	require.FailNow(t, "no page of the built site has an article with the class doc", "folder: %s", public)
	return ""
}

// docArticle returns the first article with the class "doc" below n, or nil.
func docArticle(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == atom.Article {
		if class, _ := attr(n, "class"); class == "doc" {
			return n
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := docArticle(child); found != nil {
			return found
		}
	}
	return nil
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
