package main

// This file holds the "setup" command. A person runs it in a terminal to sign
// a browser in to the local web interface:
//
//  1. It opens the App and starts its background work, as "serve" does: the web
//     interface can approve a send, and the outbox of this program sends it.
//  2. It makes a setup link with a one-time token (single use, 10 minutes), and
//     prints it on standard output.
//  3. It opens the link in the browser. The link is one argument of the
//     program that opens the browser, never a text for a shell.
//  4. It serves the web interface until Ctrl-C (or SIGTERM), then stops as
//     "serve" does.
//
// The link is the only URL with a secret in it. It goes only to standard output
// and to the browser, never to the log.

import (
	"context"
)

func runSetup(ctx context.Context, c *cli, args []string) int {
	const cmd = "setup"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
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
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		code = exitError
	} else {
		c.printf("Open this link in your browser to sign in. It works once, for 10 minutes:\n%s\n", link)
		if err := c.sys.openBrowser(link); err != nil {
			// The error is not printed: it is about the program that opens the
			// browser, and the user can open the link above.
			c.errorf("%s: could not open the browser. Open the link above yourself.\n", cmd)
		}
		c.printf("The web interface runs until you press Ctrl-C.\n")
		<-ctx.Done()
	}

	if err := a.Close(budget.context()); err != nil {
		c.errorf("%s: %v\n", cmd, err)
		code = exitError
	}
	return code
}
