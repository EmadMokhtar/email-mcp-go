// Command email-mcp is an MCP (Model Context Protocol) server for mail.
package main

import (
	"context"
	"errors"
	"flag"
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
// Later tasks add their commands to the commands table.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

// commands lists the subcommands in the order that help shows them.
var commands = []command{
	{name: "serve", summary: "serve MCP over stdio, or over local HTTP with --http", run: runServe},
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
// and capture the output.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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

	for _, c := range commands {
		if c.name == name {
			return c.run(ctx, rest, stdin, stdout, stderr)
		}
	}

	_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n", name)
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

func runVersion(_ context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		// The flag package has already printed the problem or the help text.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "version: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}

	_, _ = fmt.Fprintf(stdout, "email-mcp %s\n", version.Version)
	return exitOK
}
