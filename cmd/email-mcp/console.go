package main

// This file holds what the commands share: the parts of the system that tests
// replace (the terminal, the browser and discovery), the questions to the
// user, the flags and the opening of the App.
//
// The rules of the output of every command:
//   - Standard output gets what the user asked for and the questions to the
//     user. It may show the address of the user's own account, and a new token
//     or the setup link once.
//   - Standard error gets the errors, the warnings and the log of the App.
//     Nothing written there holds an address, a password, a token or the setup
//     link.
//
// A password is never a flag: other users of the computer can read the
// arguments of a process. It is read from the terminal without echo, or from
// the first line of standard input when standard input is not a terminal.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/discovery"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// cliClient is the client name of the audit entries that the commands make.
const cliClient = "cli"

// maxLineBytes is the longest line that a command reads from standard input: an
// answer or a password.
const maxLineBytes = 64 << 10

// errLineTooLong means that a line of standard input is longer than
// maxLineBytes.
var errLineTooLong = errors.New("the line of standard input is too long")

// The usage text of the flag that every command that opens the data has.
const dataDirUsage = "the data directory (default: $EMAIL_MCP_DATA_DIR, or the data directory of the platform)"

// system holds the parts of the program that touch the real terminal, the
// browser and the network. run uses the real ones (see realSystem). Tests use
// fakes.
type system struct {
	// isTerminal reports whether standard input is a terminal, where a person
	// can answer questions.
	isTerminal func() bool
	// readPassword reads one line from the terminal without showing it. It
	// returns when ctx ends (the first Ctrl-C), and then puts the terminal
	// back as it was.
	readPassword func(ctx context.Context) ([]byte, error)
	// openBrowser opens url in the browser of the user.
	openBrowser func(url string) error
	// discover suggests mail server settings for an address (see
	// discovery.Finder.Mail).
	discover func(ctx context.Context, address string) ([]account.MailServer, error)
}

// realSystem returns the real system for standard input stdin.
func realSystem(stdin io.Reader) system {
	f, isFile := stdin.(*os.File)
	finder := &discovery.Finder{}
	return system{
		isTerminal: func() bool {
			return isFile && term.IsTerminal(int(f.Fd())) //nolint:gosec // G115: a file descriptor fits in an int
		},
		readPassword: func(ctx context.Context) ([]byte, error) {
			if !isFile {
				return nil, errors.New("standard input is not a terminal")
			}
			return readTerminalPassword(ctx, realTerminal, int(f.Fd())) //nolint:gosec // G115: a file descriptor fits in an int
		},
		openBrowser: openBrowser,
		discover:    finder.Mail,
	}
}

// terminalFuncs are the calls of golang.org/x/term that readTerminalPassword
// makes. Tests replace them, so they need no terminal.
type terminalFuncs struct {
	getState     func(fd int) (*term.State, error)
	restore      func(fd int, state *term.State) error
	readPassword func(fd int) ([]byte, error)
}

// realTerminal are the real calls.
var realTerminal = terminalFuncs{getState: term.GetState, restore: term.Restore, readPassword: term.ReadPassword}

// readTerminalPassword reads a password from the terminal fd without echo.
//
// term.ReadPassword turns the echo off and back on when the line is read. A
// Ctrl-C (the first signal) ends ctx but not the read, and a second Ctrl-C
// ends the program at once: the echo of the terminal would stay off. So when
// ctx ends, this puts the terminal back as it was and returns. The read stays
// blocked until the program ends, which it does next.
func readTerminalPassword(ctx context.Context, t terminalFuncs, fd int) ([]byte, error) {
	state, err := t.getState(fd)
	if err != nil {
		return nil, err
	}
	type answer struct {
		line []byte
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		line, err := t.readPassword(fd)
		done <- answer{line: line, err: err}
	}()
	select {
	case a := <-done:
		return a.line, a.err
	case <-ctx.Done():
		_ = t.restore(fd, state)
		return nil, ctx.Err()
	}
}

// openBrowser opens url in the default browser: "open" on macOS,
// "rundll32 url.dll,FileProtocolHandler" on Windows and "xdg-open" elsewhere.
// The URL is one argument of the program, never a text for a shell. It does not
// wait for the browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec // G204: a fixed program; the URL is one argument, not shell text
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // G204: a fixed program; the URL is one argument, not shell text
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec // G204: a fixed program; the URL is one argument, not shell text
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait in the background, so the finished program does not stay as a
	// zombie process.
	go func() { _ = cmd.Wait() }()
	return nil
}

// cli is what a command works with: the standard streams and the system.
type cli struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	sys    system
	// lines reads standard input. It is made on first use, and every read of a
	// line uses it, so no read loses what another one buffered.
	lines *bufio.Reader
}

// printf writes to standard output. A failed write to the terminal leaves
// nothing useful to do, so write errors are ignored.
func (c *cli) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.stdout, format, args...)
}

// errorf writes to standard error. The text must hold no address, password,
// token or setup link.
func (c *cli) errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.stderr, format, args...)
}

// readLine reads one line of standard input, without the line end. A last line
// without a line end counts. At the end of the input it returns io.EOF.
func (c *cli) readLine() (string, error) {
	if c.lines == nil {
		c.lines = bufio.NewReader(c.stdin)
	}
	var line []byte
	for {
		chunk, more, err := c.lines.ReadLine()
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return "", errLineTooLong
		}
		if err != nil {
			return "", err
		}
		if !more {
			return string(line), nil
		}
	}
}

// ask writes the question to standard output and reads the answer, without
// outer spaces. ok is false when there is no answer (the input ended).
func (c *cli) ask(question string) (answer string, ok bool) {
	c.printf("%s", question)
	line, err := c.readLine()
	if err != nil {
		c.printf("\n")
		return "", false
	}
	return strings.TrimSpace(line), true
}

// confirm asks a question that the user answers with y or n. Only "y" or "yes"
// (in any case) is a yes.
func (c *cli) confirm(question string) bool {
	answer, ok := c.ask(question + " [y/N] ")
	answer = strings.ToLower(answer)
	return ok && (answer == "y" || answer == "yes")
}

// confirmTyped asks the user to type the word yes. Anything else is a no. It
// is the second, explicit confirmation of a choice with a risk.
func (c *cli) confirmTyped(question string) bool {
	answer, ok := c.ask(question + " Type yes to continue: ")
	return ok && strings.ToLower(answer) == "yes"
}

// readPassword reads a password: from the terminal without echo, or from the
// first line of standard input when it is not a terminal. The password is never
// shown, logged or put in an error.
func (c *cli) readPassword(ctx context.Context, prompt string) (secrets.Secret, error) {
	if !c.sys.isTerminal() {
		line, err := c.readLine()
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read the password from standard input: %w", err)
		}
		return secrets.Secret(line), nil
	}
	c.printf("%s", prompt)
	b, err := c.sys.readPassword(ctx)
	// The terminal did not show the line end that the user typed.
	c.printf("\n")
	if err != nil {
		return "", fmt.Errorf("read the password from the terminal: %w", err)
	}
	password := secrets.Secret(b)
	clear(b)
	return password, nil
}

// newFlags returns the flag set of a command. The flag package writes nothing:
// its messages repeat the text of a bad flag, and the user may have typed a
// password there. parseFlags prints fixed texts instead.
func (c *cli) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseFlags parses args with fs. Flags may come before and after the
// arguments that are not flags, such as "remove ID --yes". After "--" every
// argument is one that is not a flag. It returns the arguments that are not
// flags. When the command must stop, ok is false and code is the exit code:
// after -h it prints the flags on standard output (exitOK); after a bad flag it
// prints a fixed text on standard error (exitUsage), never the text that the
// user typed.
func (c *cli) parseFlags(cmd string, fs *flag.FlagSet, args []string) (rest []string, code int, ok bool) {
	for {
		if code, ok := c.parseOnce(cmd, fs, args, c.stdout); !ok {
			return nil, code, false
		}
		left := fs.Args()
		if len(left) == 0 {
			return rest, exitOK, true
		}
		if used := len(args) - len(left); used > 0 && args[used-1] == "--" {
			return append(rest, left...), exitOK, true
		}
		rest = append(rest, left[0])
		args = left[1:]
	}
}

// parseOnce runs fs.Parse(args) with the rules of parseFlags: fs writes
// nothing; -h prints the flags to help (exitOK); a bad flag prints a fixed text
// on standard error (exitUsage). ok is false when the command must stop.
// "serve" gives standard error as help, because on stdio standard output
// carries only the protocol.
func (c *cli) parseOnce(cmd string, fs *flag.FlagSet, args []string, help io.Writer) (code int, ok bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return exitOK, true
	case errors.Is(err, flag.ErrHelp):
		_, _ = fmt.Fprintf(help, "Usage of email-mcp %s:\n", cmd)
		fs.SetOutput(help)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
		return exitOK, false
	default:
		c.errorf("%s: %s. Run \"email-mcp %s -h\" to see the flags.\n", cmd, flagProblem(fs, err), cmd)
		return exitUsage, false
	}
}

// flagProblem describes an error of the flag package without the text that the
// user typed. It names a flag only when the command has a flag of that name: an
// unknown "flag" could be a password.
func flagProblem(fs *flag.FlagSet, err error) string {
	msg := err.Error()
	known := func(name string) bool { return name != "" && fs.Lookup(name) != nil }
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined"):
		return "unknown flag"
	case strings.HasPrefix(msg, "flag needs an argument: -"):
		if name := strings.TrimPrefix(msg, "flag needs an argument: -"); known(name) {
			return "the flag -" + name + " needs a value"
		}
		return "a flag needs a value"
	case strings.HasPrefix(msg, "invalid "):
		// "invalid value %q for flag -NAME: …" or "invalid boolean value %q
		// for -NAME: …". The value comes before the name, so the name is after
		// the last " for ".
		if i := strings.LastIndex(msg, " for "); i >= 0 {
			name, reason, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(msg[i+len(" for "):], "flag "), "-"), ":")
			if known(name) {
				// The reason after the name is a fixed text of the flag package
				// ("parse error") or of a flag of this program, never the value.
				return "the value of the flag -" + name + " is not valid: " + strings.TrimSpace(reason)
			}
		}
		return "a flag has a value that is not valid"
	default:
		return "a flag is not valid"
	}
}

// usage prints a usage error of a command and returns exitUsage.
func (c *cli) usage(cmd, format string, args ...any) int {
	c.errorf("%s: %s\n", cmd, fmt.Sprintf(format, args...))
	return exitUsage
}

// unexpected prints that the command got arguments that it does not take, and
// returns exitUsage. It names the position of the first one, never its text:
// the user may have typed a password there.
func (c *cli) unexpected(cmd string) int {
	return c.usage(cmd, "unexpected argument 1: this command takes no arguments that are not flags")
}

// openApp opens the App in the data directory, as "serve" does: the master key
// is found or made under the lock of the data directory, from $EMAIL_MCP_KEK
// (the keychain by default). The log goes to standard error. On an error it
// prints it and returns nil.
func (c *cli) openApp(ctx context.Context, cmd, dataDir string) *app.App {
	a, err := app.Open(ctx, app.Config{DataDir: dataDir, Local: true, LogOutput: c.stderr})
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return nil
	}
	return a
}

// closeApp closes an App that a command opened and did not serve. It leaves
// the queued messages to the program that serves (see app.App.CloseNow).
func (c *cli) closeApp(ctx context.Context, cmd string, a *app.App) {
	if err := a.CloseNow(context.WithoutCancel(ctx)); err != nil {
		c.errorf("%s: %v\n", cmd, err)
	}
}

// userContext returns ctx with the principal of the user at the terminal: the
// local tenant, no scope limit (a local user acts, not an agent) and the client
// name "cli".
func userContext(ctx context.Context) context.Context {
	return service.WithPrincipal(ctx, service.Principal{Tenant: store.LocalTenantID, Client: cliClient})
}
