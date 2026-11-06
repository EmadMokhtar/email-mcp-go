//go:build screenshots

package e2e

// This file makes the images of the user manual. "make screenshots" runs
// TestScreenshots: the whole program runs as in the other tests of this package,
// a headless Chrome signs in with a real sign-in link, and each image shows a
// real page of the program. A change that breaks a shot fails the build of the
// site, so the fix goes in the same pull request.
//
// The images hold sample data only, on example.com. A shot of a page that shows
// a secret fails.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/chrometest"
)

const (
	// shotDirVariable names the folder of the images. It is an environment
	// variable, because "go test" runs a test in the folder of its package.
	shotDirVariable = "EMAIL_MCP_SCREENSHOT_DIR"

	// shotStamp is the file that the Makefile touches after a good run. Keep it
	// in step with SCREENSHOT_STAMP in the Makefile.
	shotStamp = ".done"

	// The size of the browser window of a shot, in pixels. The manual uses the
	// desktop layout of the pages.
	shotWidth  = 1280
	shotHeight = 800

	// tokenStart starts every access token (internal/tokens).
	tokenStart = "emcp_"

	// settleScript waits until every colour change over time has ended and the
	// result is painted. The buttons of the pages have such a transition, so an
	// image taken right after the switch to light or dark shows them with a
	// colour half way, and a different one in each run.
	settleScript = `Promise.all(document.getAnimations().map((a) => a.finished)).then(() => new Promise(requestAnimationFrame))`
)

// shooter opens pages in one Chrome tab and saves them as images.
type shooter struct {
	t         *testing.T
	ctx       context.Context // the tab
	dir       string          // the folder of the images
	forbidden []string        // the values that no shot may show
}

// open loads a page and requires that it worked: the answer is 200, and the
// page shows each of the texts in want. So a renamed button, or a sign-in that
// failed, fails the test. An image of the wrong page would not fail by itself.
func (s *shooter) open(link string, want ...string) {
	s.t.Helper()
	resp, err := chromedp.Run(s.ctx, chromedp.NavigateResponse(link))
	require.NoError(s.t, err)
	require.Equal(s.t, int64(http.StatusOK), resp.Status, "the page at %s", resp.URL)
	text := s.pageText()
	for _, w := range want {
		require.Contains(s.t, text, w, "the page at %s", resp.URL)
	}
}

// pageText returns the text that the open page shows.
func (s *shooter) pageText() string {
	s.t.Helper()
	text, err := chromedp.Run(s.ctx, chromedp.Evaluate[string](`document.body.innerText`))
	require.NoError(s.t, err)
	return text
}

// shot saves the open page as <name>-light.png and <name>-dark.png.
func (s *shooter) shot(name string) {
	s.t.Helper()
	require.NoError(s.t, checkPageText(s.pageText(), s.forbidden...), "shot %s", name)
	for _, scheme := range []string{"light", "dark"} {
		_, err := chromedp.Call(s.ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}},
		})
		require.NoError(s.t, err)
		_, err = chromedp.Run(s.ctx, chromedp.Evaluate[chromedp.Void](settleScript, chromedp.EvalAwaitPromise))
		require.NoError(s.t, err)
		image, err := chromedp.Run(s.ctx, chromedp.FullScreenshot(100))
		require.NoError(s.t, err)
		file := filepath.Join(s.dir, name+"-"+scheme+".png")
		require.NoError(s.t, os.WriteFile(file, image, 0o600))
	}
}

// checkPageText returns an error when the text of a page shows an access token
// or one of the forbidden values. The error does not repeat the value, because
// the log of a CI run is public.
func checkPageText(text string, forbidden ...string) error {
	if strings.Contains(text, tokenStart) {
		return errors.New("the page shows an access token")
	}
	for i, value := range forbidden {
		if strings.Contains(text, value) {
			return fmt.Errorf("the page shows forbidden value number %d", i+1)
		}
	}
	return nil
}

// clearShots makes dir ready for a new run. It creates the folder when it is
// missing. It removes the images (*.png) and the stamp file of the Makefile
// (.done) that are directly inside it. It leaves every other file and every
// subfolder alone.
//
// The folder comes from the environment. A wrong value must not be able to
// delete a real folder of data, so the code removes files by name and never a
// whole folder. The stamp goes too. If a run fails halfway, an old stamp would
// still say that the shots are current, and "make" would skip the next run
// although the folder holds only some of the images.
func clearShots(dir string) error {
	// G703 flags both calls below, because dir comes from the environment.
	// TestScreenshots requires an absolute path first, and the loop removes only
	// single files by name.
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G703: see above
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (name != shotStamp && filepath.Ext(name) != ".png") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil { //nolint:gosec // G703: see above
			return err
		}
	}
	return nil
}

func TestShotRefusesSecrets(t *testing.T) {
	require.NoError(t, checkPageText("Waiting for you", "pw-1"))
	require.Error(t, checkPageText("your token emcp_abc123", "pw-1"))
	require.Error(t, checkPageText("password pw-1 here", "pw-1"))
}

// TestClearShotsKeepsOtherFiles requires that clearShots removes the images and
// the stamp file, and nothing else. The folder comes from the environment, so a
// wrong value must not be able to delete a real folder of data.
func TestClearShotsKeepsOtherFiles(t *testing.T) {
	t.Run("removes only the images and the stamp", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		require.NoError(t, os.Mkdir(sub, 0o750))
		for _, name := range []string{"a-light.png", ".done", "notes.txt", filepath.Join("sub", "b.png")} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
		}

		require.NoError(t, clearShots(dir))

		require.NoFileExists(t, filepath.Join(dir, "a-light.png"))
		require.NoFileExists(t, filepath.Join(dir, ".done"))
		require.FileExists(t, filepath.Join(dir, "notes.txt"))
		require.FileExists(t, filepath.Join(sub, "b.png"))
	})

	t.Run("creates a folder that is missing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "not", "there", "yet")

		require.NoError(t, clearShots(dir))

		require.DirExists(t, dir)
	})
}

// TestScreenshots makes every image of the manual. It starts without the images
// of an earlier run, so an image of a shot that was removed cannot stay behind.
func TestScreenshots(t *testing.T) {
	dir := os.Getenv(shotDirVariable)
	require.NotEmpty(t, dir, "%s must name the folder of the images", shotDirVariable)
	require.True(t, filepath.IsAbs(dir),
		"%s must be an absolute path, because go test runs in the folder of the package. Got %q", shotDirVariable, dir)
	require.NoError(t, clearShots(dir))

	h := newHarness(t)
	m := newMailServers(t)
	a := h.connect(localCtx(), "you@example.com", m)

	// The client has no elicitation, so the send waits for the web page.
	const (
		to      = "anna@example.com"
		subject = "Re: Thursday's meeting"
		body    = "Thursday at 10:00 works for me. See you then."
	)
	send := mustOK(t, h.client(nil), "send_message", map[string]any{
		"account": string(a.ID),
		"to":      []any{to},
		"subject": subject,
		"body":    body,
	})
	require.Equal(t, "pending_approval", send["status"])

	setupURL, err := h.app.Web.NewSetupURL(context.Background())
	require.NoError(t, err)

	ctx, cancel := chrometest.Start(t)
	defer cancel()
	require.NoError(t, chromedp.Do(ctx, chromedp.EmulateViewport(shotWidth, shotHeight)))
	s := &shooter{t: t, ctx: ctx, dir: dir, forbidden: []string{secretPassword, setupURL}}

	s.open(setupURL, "Sign out")
	s.open(send["approval_link"].(string), "Waiting for you", subject, to, body, "Reject", "Approve and send")
	s.shot("waiting")
}
