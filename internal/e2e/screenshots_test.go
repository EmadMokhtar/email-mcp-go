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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
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
	// desktop layout of the pages. A page with little on it, such as the first
	// form of "Connect an account", gets the short window, so the image has no
	// large empty area.
	shotWidth       = 1280
	shotHeight      = 800
	shotHeightShort = 420

	// tokenStart starts every access token (internal/tokens).
	tokenStart = "emcp_"

	// settleScript waits until every colour change over time has ended and the
	// result is painted. The buttons of the pages have such a transition, so an
	// image taken right after the switch to light or dark shows them with a
	// colour half way, and a different one in each run.
	//
	// It uses allSettled and not all. A transition that is cancelled while it
	// runs (the page can cancel one when a form changes) makes its "finished"
	// promise fail. Promise.all would then fail at once, and the shot would
	// fail at random. allSettled waits for every transition, however it ends.
	settleScript = `Promise.allSettled(document.getAnimations().map((a) => a.finished)).then(() => new Promise(requestAnimationFrame))`
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
	s.openStatus(link, http.StatusOK, want...)
}

// openStatus is open for a page that answers with the status wantStatus.
func (s *shooter) openStatus(link string, wantStatus int64, want ...string) {
	s.t.Helper()
	resp, err := chromedp.Run(s.ctx, chromedp.NavigateResponse(link))
	require.NoError(s.t, navigationError(link, err))
	require.NotNil(s.t, resp, "the page at %s gave no answer", addressPath(link))
	// The text is read only when the answer is good, and it is never printed.
	text := ""
	if resp.Status == wantStatus {
		text = s.pageText()
	}
	require.NoError(s.t, checkPage(resp.URL, resp.Status, wantStatus, text, want...))
}

// checkPage returns an error when the page that was opened is not the page that
// a shot needs: the answer does not have the status wantStatus, or the text
// lacks one of want. Most pages answer 200. A browser that is not signed in gets
// a page that answers 401, and the page is the thing to show.
//
// The log of a CI run is public, so the error holds only what is safe: the path
// of the address (the sign-in link has its token in the query), the status, and
// the text that was wanted. It never holds the address with its query, and it
// never holds the text of the page, which may show a secret.
func checkPage(address string, status, wantStatus int64, text string, want ...string) error {
	where := addressPath(address)
	if status != wantStatus {
		return fmt.Errorf("the page at %s answered with status %d, not %d", where, status, wantStatus)
	}
	if w, missing := firstMissing(text, want); missing {
		return fmt.Errorf("the page at %s does not show %q", where, w)
	}
	return nil
}

// firstMissing returns the first text in want that the text of a page does not
// contain, and true. It returns "" and false when the text has every one of
// them. Both checkPage and reloadUntil use it, so they name the same text.
func firstMissing(text string, want []string) (string, bool) {
	for _, w := range want {
		if !strings.Contains(text, w) {
			return w, true
		}
	}
	return "", false
}

// navigationError turns the error of the browser into an error for the log, or
// returns nil when there is none. The browser may repeat the address, or a
// part of it, in its message, so those parts are replaced.
func navigationError(link string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if link != "" {
		msg = strings.ReplaceAll(msg, link, addressPath(link))
	}
	if u, parseErr := url.Parse(link); parseErr == nil {
		secrets := []string{u.RawQuery, u.Fragment}
		for _, values := range u.Query() {
			secrets = append(secrets, values...)
		}
		for _, secret := range secrets {
			if secret != "" {
				msg = strings.ReplaceAll(msg, secret, "(left out)")
			}
		}
	}
	return fmt.Errorf("open %s: %s", addressPath(link), msg)
}

// addressPath returns the path of an address for a message, and nothing else:
// not the host, not the query, not the fragment. An address with no path gives
// "/". An address that cannot be read gives a fixed text and never the address.
func addressPath(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return "(an address that cannot be read)"
	}
	if u.Path == "" {
		return "/"
	}
	return u.Path
}

// pageText returns the text that the open page shows.
func (s *shooter) pageText() string {
	s.t.Helper()
	text, err := chromedp.Run(s.ctx, chromedp.Evaluate[string](`document.body.innerText`))
	require.NoError(s.t, err)
	return text
}

// viewport sets the height of the browser window for the next shots. The width
// is always shotWidth.
func (s *shooter) viewport(height int64) {
	s.t.Helper()
	require.NoError(s.t, chromedp.Do(s.ctx, chromedp.EmulateViewport(shotWidth, height)))
}

// click clicks the element that the CSS selector names, and waits until the
// element ready shows on the page that follows. It then requires that this page
// shows each of the texts in want, as open does. So a renamed button fails the
// test, and so does a click that leads to another page. Take no shot while a
// form is busy: the busy sign of a form never ends, and a shot would wait for
// it until the time limit. So click only links and buttons that load a page
// at once. The button of a form is fine when ready names an element that only
// the page after the redirect has: the click returns once that page shows.
func (s *shooter) click(selector, ready string, want ...string) {
	s.t.Helper()
	require.NoError(s.t, chromedp.Do(s.ctx,
		chromedp.Click(chromedp.CSS(selector)),
		chromedp.WaitVisible(chromedp.CSS(ready)),
	))
	where, err := chromedp.Run(s.ctx, chromedp.Location())
	require.NoError(s.t, err)
	require.NoError(s.t, checkPage(where, http.StatusOK, http.StatusOK, s.pageText(), want...))
}

// reloadUntil reloads the open page until its text has every text in want, or
// the time is over. The result of an approved send comes from the outbox, so the
// page may first say that the send is still going out. A reload of a page that
// was opened with GET is safe. The failure names the wanted text and never the
// text of the page, because the log of a CI run is public.
func (s *shooter) reloadUntil(limit time.Duration, want ...string) {
	s.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		missing, ok := firstMissing(s.pageText(), want)
		if !ok {
			return
		}
		require.True(s.t, time.Now().Before(deadline), "the page does not show %q after %s", missing, limit)
		time.Sleep(250 * time.Millisecond)
		require.NoError(s.t, chromedp.Do(s.ctx, chromedp.Reload()))
	}
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

// TestShotRefusesToPrintTheSignInLink requires that the failure of open never
// shows the query of an address, where the sign-in link has its token, and
// never shows the text of the page. The log of a CI run is public.
func TestShotRefusesToPrintTheSignInLink(t *testing.T) {
	const token = "tok-secret-123"
	link := "http://127.0.0.1:4321/setup?token=" + token + "#top"
	const pageSecret = "secret-page-body emcp_abc123"

	requireSafe := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		for _, leak := range []string{token, "token=", "?", "#top", pageSecret, "emcp_"} {
			require.NotContains(t, err.Error(), leak)
		}
	}

	t.Run("a wrong status names the status and the path", func(t *testing.T) {
		err := checkPage(link, http.StatusInternalServerError, http.StatusOK, "", "Sign out")
		requireSafe(t, err)
		require.Contains(t, err.Error(), "status 500")
		require.Contains(t, err.Error(), "/setup")
	})

	t.Run("a missing text names the wanted text and not the page", func(t *testing.T) {
		err := checkPage(link, http.StatusOK, http.StatusOK, "Hello "+pageSecret, "Sign out", "Approve and send")
		requireSafe(t, err)
		require.Contains(t, err.Error(), `"Sign out"`)
		require.Contains(t, err.Error(), "/setup")
	})

	t.Run("the second missing text is named, not the first", func(t *testing.T) {
		err := checkPage(link, http.StatusOK, http.StatusOK, "Sign out", "Sign out", "Approve and send")
		requireSafe(t, err)
		require.Contains(t, err.Error(), `"Approve and send"`)
		require.NotContains(t, err.Error(), `"Sign out"`)
	})

	t.Run("a good page has no error", func(t *testing.T) {
		require.NoError(t, checkPage(link, http.StatusOK, http.StatusOK, "Sign out and more", "Sign out"))
		require.NoError(t, checkPage(link, http.StatusOK, http.StatusOK, "anything"))
	})

	t.Run("an error of the browser has the path and not the address", func(t *testing.T) {
		cause := errors.New("cannot load " + link + " (token " + token + "): net::ERR_FAILED")
		err := navigationError(link, cause)
		requireSafe(t, err)
		require.Contains(t, err.Error(), "/setup")
		require.Contains(t, err.Error(), "net::ERR_FAILED")
		require.NoError(t, navigationError(link, nil))
	})

	t.Run("an address that cannot be read is not printed", func(t *testing.T) {
		bad := "http://[::1?token=" + token
		requireSafe(t, checkPage(bad, http.StatusNotFound, http.StatusOK, ""))
		require.NotContains(t, addressPath(bad), token)
	})

	t.Run("a path with no slash and an address with no path", func(t *testing.T) {
		require.Equal(t, "/", addressPath("http://127.0.0.1:4321?token="+token))
		require.Equal(t, "/approvals/abc", addressPath("http://x.localhost:1/approvals/abc?a=1"))
	})
}

// TestShotRefusesWhenATextIsMissing requires that firstMissing names the first
// wanted text that the page lacks, and that it names none when the page has
// them all. checkPage and reloadUntil both depend on this.
func TestShotRefusesWhenATextIsMissing(t *testing.T) {
	t.Run("the first missing text is named", func(t *testing.T) {
		w, missing := firstMissing("Sign out", []string{"Sign out", "Approve and send", "Reject"})
		require.True(t, missing)
		require.Equal(t, "Approve and send", w)
	})

	t.Run("a page with every text has none missing", func(t *testing.T) {
		w, missing := firstMissing("Sign out and Approve and send", []string{"Sign out", "Approve and send"})
		require.False(t, missing)
		require.Empty(t, w)
	})

	t.Run("nothing wanted means nothing missing", func(t *testing.T) {
		_, missing := firstMissing("anything", nil)
		require.False(t, missing)
	})
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

	setupURL, err := h.app.Web.NewSetupURL(context.Background())
	require.NoError(t, err)

	ctx, cancel := chrometest.Start(t)
	defer cancel()
	s := &shooter{t: t, ctx: ctx, dir: dir, forbidden: []string{secretPassword, setupURL}}
	s.viewport(shotHeightShort)

	// The first two shots come before any account exists: the browser signs in
	// to a program with no account, and the person clicks the button that the
	// page offers. The shot of the first form stops there. The next step looks
	// up the mail provider on the network, and this test uses none.
	s.open(setupURL, "Sign out", "No account is connected yet", "Connect your first account")
	s.shot("accounts-empty")
	s.click(`.empty a[href="/connect"]`, "#email",
		"Connect an account", "Email address", "Find the server settings", "Enter the settings yourself")
	s.shot("connect")

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

	s.viewport(shotHeight)
	s.open(send["approval_link"].(string), "Waiting for you", subject, to, body, "Reject", "Approve and send")
	s.shot("waiting")

	// The person approves. After a confirmation the undo window is 0 seconds, so
	// the send goes out at once. The page may say "Sending" for a moment, so the
	// shot waits for the true result.
	s.click(`form[action$="/approve"] button`, ".outcome", "Waiting for you")
	s.reloadUntil(20*time.Second, "Sent", "The mail server accepted the message.")
	s.shot("result-sent")

	// The agent has made requests and an action by now. The log shows them.
	s.click(`a.menu-link[href="/activity"]`, "ol.log", "Activity", "Queued sends", "Log", "Send message")
	s.shot("activity")

	// The account, its settings and its reconnect page are one click apart. The
	// list of accounts is short, so its shot uses the short window.
	s.viewport(shotHeightShort)
	s.click(`a.menu-link[href="/accounts"]`, ".rows", "Accounts", "you@example.com", "Connected", "Mail level: Send",
		"Permissions and settings", "Reconnect")
	s.shot("accounts")
	s.viewport(shotHeight)
	s.click(`a[href$="/settings"]`, "#mail_level", "Permissions", "Mail level", "Confirm sends", "Save")
	s.shot("account-settings")
	s.open(h.webBase()+"/accounts", "Accounts")
	s.click(`a[href$="/reconnect"]`, "#password", "Reconnect you@example.com", "New password or app password",
		"Test the connection and save")
	s.shot("reconnect")

	// The Connections page makes a token with its real form. The answer to the
	// form shows the new token once, and a shot of it would hold the secret (a
	// shot of that page fails, see checkPageText). So the form is only filled
	// and sent, and the shot is of the list when the page is opened again. The
	// list shows the name, the permissions and the end date of the token, and
	// never the token.
	const tokenName = "my-app"
	s.open(h.webBase()+"/connections", "Connections", "No token yet.", "New token", "Make a token")
	require.NoError(t, chromedp.Do(ctx,
		chromedp.SendKeys(chromedp.CSS(`input[name="name"]`), tokenName),
		chromedp.Click(chromedp.CSS(`input[name="scope"][value="email:read"]`)),
		chromedp.Click(chromedp.CSS(`input[name="scope"][value="email:draft"]`)),
	))
	s.click(`form[action="/connections"] button[type="submit"]`, ".notice.good",
		"Connections", "Copy this token now. It is shown only once.")
	s.open(h.webBase()+"/connections", "Connections", tokenName, "email:read", "email:draft", "Revoke",
		"Signed-in browsers")
	// require.NotContains would print the whole text on failure, and the log of a
	// CI run is public. So the check prints only its own message.
	require.False(t, strings.Contains(s.pageText(), "Copy this token now"),
		"the list must not show the notice of a new token")
	s.shot("connections")

	// The last shot is a browser that never signed in, so the cookies go. The
	// page answers 401 and says only what to do.
	_, err = chromedp.Call(ctx, network.ClearBrowserCookies, cdp.Empty{})
	require.NoError(t, err)
	s.viewport(shotHeightShort)
	s.openStatus(h.webBase()+"/approvals", http.StatusUnauthorized,
		"Run email-mcp setup in a terminal, then open this link again.")
	s.shot("no-session")
}
