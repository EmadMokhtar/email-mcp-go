// Command email-mcp is an MCP (Model Context Protocol) server for mail.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/EmadMokhtar/email-mcp-go/internal/version"
)

// Exit codes returned by run.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// command is one subcommand. Each subcommand owns its own flag.FlagSet.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, c *cli, args []string) int
}

// commands lists the subcommands in the order that help shows them.
var commands = []command{
	{name: "serve", summary: "serve MCP over stdio, or over local HTTP with --http", run: runServe},
	{name: "setup", summary: "sign a browser in to the local web interface, and serve it until Ctrl-C", run: runSetup},
	{name: "account", summary: "add, list, remove or test an account, without a browser (account help)", run: runAccount},
	{name: "token", summary: "create, list or revoke an access token of serve --http (token help)", run: runToken},
	{name: "import-env", summary: "import the account of a .env file of the old email-mcp: import-env [path]", run: runImportEnv},
	{name: "key", summary: "change the master key: key rotate --to file:<path>|keychain", run: runKey},
	{name: "doctor", summary: "check the data directory, the master key and every account", run: runDoctor},
	{name: "version", summary: "print the version and exit", run: runVersion},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The first signal starts a clean stop, which waits for sends in progress.
	// After it, the default handling comes back, so a second signal ends the
	// program at once. A send that is cut off then is never sent again: the next
	// start marks it "delivery unknown".
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	// os.Exit does not run deferred calls, so release the signal handler here.
	stop()
	os.Exit(code)
}

// run dispatches args to a subcommand and returns the process exit code.
// It takes the input and output streams as parameters so tests can use pipes
// and capture the output. It uses the real terminal, browser and discovery (see
// realSystem).
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWith(ctx, realSystem(stdin), args, stdin, stdout, stderr)
}

// runWith is run with the given system. Tests give it a fake system, so they
// never open a browser and never need a terminal.
func runWith(ctx context.Context, sys system, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}

	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "-help", "--help":
		printUsage(stdout)
		return exitOK
	}

	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr, sys: sys}
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd.run(ctx, c, rest)
		}
	}

	// The name is not repeated: the user may have typed a password.
	_, _ = fmt.Fprint(stderr, "unknown command: see the list of commands below\n\n")
	printUsage(stderr)
	return exitUsage
}

// printUsage writes the command list to w. A failed write to the terminal
// leaves nothing useful to do, so write errors are ignored.
func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  email-mcp <command> [flags]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		_, _ = fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	_, _ = fmt.Fprintf(w, "  %-10s %s\n", "help", "show this help")
}

func runVersion(_ context.Context, c *cli, args []string) int {
	// A bad flag gets a fixed text, never the text that was typed (see
	// parseOnce).
	fs := c.newFlags("version")
	if code, ok := c.parseOnce("version", fs, args, c.stderr); !ok {
		return code
	}
	if fs.NArg() > 0 {
		return c.unexpected("version")
	}

	_, _ = fmt.Fprintf(c.stdout, "email-mcp %s\n", version.Version)
	return exitOK
}
