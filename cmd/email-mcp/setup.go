package main

// This file holds the "setup" command. A person runs it in a terminal to sign
// a browser in to the local web interface:
//
//  1. It opens the App and starts its background work, as "serve" does: the web
//     interface can approve a send, and the outbox of this program sends it.
//  2. It makes a setup link with a one-time token (single use, 10 minutes), and
//     prints it on standard output. It also prints the address of a help page
//     for a browser that cannot find the name in the link. That address has no
//     token.
//  3. It opens the link in the browser through a private file. The arguments of
//     a program can be read by other users of the computer, so the program that
//     opens the browser gets only the address of the file, never the link.
//  4. It serves the web interface until Ctrl-C (or SIGTERM), then stops as
//     "serve" does.
//
// The link is the only URL with a secret in it. It goes only to standard output
// and to that file, never to the log.
//
// With --revoke-sessions the command does none of this: it signs every browser
// out, makes every unused sign-in link stop working, and stops.

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// signInPageLife is how long the file with the sign-in link stays. The browser
// needs it only to start, and the token in it works once. Setup removes the file
// when it stops, if that comes first.
const signInPageLife = time.Minute

func runSetup(ctx context.Context, c *cli, args []string) int {
	const cmd = "setup"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	revoke := fs.Bool("revoke-sessions", false, "sign every browser out, cancel unused sign-in links, and stop, without a new sign-in")
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.unexpected(cmd)
	}

	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	if *revoke {
		defer c.closeApp(ctx, cmd, a)
		n, err := a.Web.RevokeSessions(ctx)
		if err != nil {
			c.errorf("%s: %v\n", cmd, err)
			return exitError
		}
		word := "browsers"
		if n == 1 {
			word = "browser"
		}
		c.printf("Signed out %d %s. Unused sign-in links no longer work. Run email-mcp setup to sign in again.\n", n, word)
		return exitOK
	}

	a.Run(ctx)
	budget := newStopBudget(ctx)
	defer budget.release()
	// A signal starts the stop: nothing new starts, and the budget runs from now.
	stopOnSignal := context.AfterFunc(ctx, func() {
		a.BeginStop()
		budget.context()
	})
	defer stopOnSignal()

	code = exitOK
	link, err := a.Web.NewSetupURL(ctx)
	help := ""
	if err == nil {
		help, err = a.Web.SignInHelpURL(ctx)
	}
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		code = exitError
	} else {
		c.printf("Open this link in your browser to sign in. It works once, for 10 minutes:\n%s\n", link)
		c.printf("If your browser cannot find that address, this page says what to do:\n%s\n", help)
		defer c.openSignInPage(cmd, link)()
		c.printf("The web interface runs until you press Ctrl-C.\n")
		<-ctx.Done()
	}

	if err := a.Close(budget.context()); err != nil {
		c.errorf("%s: %v\n", cmd, err)
		code = exitError
	}
	return code
}

// openSignInPage opens the sign-in link in the browser through a private file,
// and returns a function that removes the file. The file also goes by itself
// after signInPageLife.
//
// A failure is only a note: the user can open the printed link. The error is not
// printed, because it is about the file or the program that opens the browser.
//
// The program that opens the browser reports success before the browser reads
// the file. It cannot tell that a sandboxed browser with its own temporary
// directory found no file, so setup always says what to do then.
func (c *cli) openSignInPage(cmd, link string) func() {
	const note = "%s: could not open the browser. Open the link above yourself.\n"
	target, remove, err := writeSignInPage(link)
	if err != nil {
		c.errorf(note, cmd)
		return func() {}
	}
	if err := c.sys.openBrowser(target); err != nil {
		remove()
		c.errorf(note, cmd)
		return func() {}
	}
	c.printf("If the browser shows an error, open the link above yourself.\n")
	timer := time.AfterFunc(signInPageLife, remove)
	return func() {
		timer.Stop()
		remove()
	}
}

// writeSignInPage writes a small page that sends the browser on to link, in a
// new directory that only this user can read. It returns the address of the page
// as a file URL, and a function that removes the directory, which is safe to call
// more than once.
//
// On Linux the browser keeps its arguments in /proc for as long as it runs, and
// every user of the computer can read them. So the arguments hold this address,
// and the link stays in the file. A file URL has no spaces, so it is one word for
// "rundll32" on Windows too.
func writeSignInPage(link string) (target string, remove func(), err error) {
	dir, err := os.MkdirTemp("", "email-mcp-sign-in-")
	if err != nil {
		return "", nil, fmt.Errorf("sign-in page: %w", err)
	}
	var once sync.Once
	remove = func() {
		once.Do(func() { _ = os.RemoveAll(dir) })
	}
	address := html.EscapeString(link)
	page := `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="referrer" content="no-referrer">` +
		`<meta http-equiv="refresh" content="0; url=` + address + `">` +
		`<title>email-mcp</title></head>` +
		`<body><p><a href="` + address + `">Sign in to email-mcp</a></p></body></html>`
	path := filepath.Join(dir, "sign-in.html")
	if err := os.WriteFile(path, []byte(page), 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("sign-in page: %w", err)
	}
	return fileURL(path), remove, nil
}

// fileURL returns the file URL of an absolute path. A Windows path such as
// "C:\Users\a" becomes "file:///C:/Users/a".
func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed}
	return u.String()
}
