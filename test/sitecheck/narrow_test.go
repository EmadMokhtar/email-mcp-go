//go:build screenshots

package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/chrometest"
)

// The size of a small phone, in pixels.
const (
	phoneWidth  = 375
	phoneHeight = 812
)

// TestNoSidewaysScroll opens every page of the built site in Chrome, at the
// width of a phone, and requires that the page does not scroll sideways. A long
// line of code or a wide image must stay inside its own box. Only a browser can
// see this, so the site check cannot. "make site" runs this test after the
// build, with the folder of the built site and its base path.
func TestNoSidewaysScroll(t *testing.T) {
	public := os.Getenv("SITE_PUBLIC_DIR")
	require.NotEmpty(t, public, "SITE_PUBLIC_DIR must name the built site")
	basePath := os.Getenv("SITE_BASE_PATH")
	require.NotEmpty(t, basePath, "SITE_BASE_PATH must be the path where the site is served")
	base := normalizeBase(basePath)

	pages := indexPages(t, public)
	require.NotEmpty(t, pages, "no index.html in %s. Did the build run?", public)

	// The prefix has no slash at the end, so "/manual/" stays a full path for
	// the file server. A request outside the base path gets a 404, as on the
	// real site.
	files := http.StripPrefix(strings.TrimSuffix(base, "/"), http.FileServer(http.Dir(public)))
	server := httptest.NewServer(files)
	defer server.Close()

	ctx, cancel := chrometest.Start(t)
	defer cancel()
	require.NoError(t, chromedp.Do(ctx, chromedp.EmulateViewport(phoneWidth, phoneHeight)))

	for _, rel := range pages {
		t.Run(rel, func(t *testing.T) {
			resp, err := chromedp.Run(ctx, chromedp.NavigateResponse(server.URL+base+pagePath(rel)))
			require.NoError(t, err)
			require.Equal(t, int64(http.StatusOK), resp.Status)

			size, err := chromedp.Run(ctx, chromedp.Evaluate[[]int](
				`[document.documentElement.scrollWidth, document.documentElement.clientWidth]`))
			require.NoError(t, err)
			scrollWidth, clientWidth := size[0], size[1]
			// At another width this test would check nothing.
			require.Equal(t, phoneWidth, clientWidth, "the width of the page")
			require.LessOrEqual(t, scrollWidth, clientWidth, "the page scrolls sideways")
		})
	}
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
