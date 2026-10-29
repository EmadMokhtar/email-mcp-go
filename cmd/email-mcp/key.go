package main

// This file holds the "key rotate" command. It changes the master key (KEK)
// that wraps the data keys in the database. The sealed secrets keep their data
// keys, so only the wrapped keys change.
//
// The program learns its master key from the environment variable
// EMAIL_MCP_KEK ("keychain" when it is not set) every time it starts. There is
// no config file. So after a rotation the command prints the exact setting that
// every later start needs.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
)

// keyCommands are the subcommands of "key".
var keyCommands = []command{
	{name: "rotate", summary: "change the master key: rotate --to file:<absolute path>|keychain [--yes]", run: runKeyRotate},
}

func runKey(ctx context.Context, c *cli, args []string) int {
	return runGroup(ctx, c, "key", keyCommands, args)
}

func runKeyRotate(ctx context.Context, c *cli, args []string) int {
	const cmd = "key rotate"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	to := fs.String("to", "", "the new master key: file:<absolute path> (a new file, made with mode 0600) or keychain")
	yes := fs.Bool("yes", false, "rotate without a question (needed when standard input is not a terminal)")
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.unexpected(cmd)
	}
	// CheckKEKSource refuses a ".." part too. This check comes first, so the
	// user gets the text that names the problem.
	if path, isFile := strings.CutPrefix(*to, "file:"); isFile && hasDotDot(path) {
		return c.usage(cmd, `--to must not have a ".." part: after a folder that is a symbolic link, ".." does `+
			`not go where the text says. Give the path without it`)
	}
	if app.CheckKEKSource(*to) != nil {
		return c.usage(cmd, "--to must be file:<absolute path> or keychain")
	}

	// Another program that runs now keeps the old master key in memory. A key
	// that it makes during the rotation would be wrapped by the old key, and
	// lost after its restart. So the user stops them before anything is opened.
	c.printf("Stop every other email-mcp program that uses this data directory (serve, setup, and the MCP clients " +
		"that start email-mcp) before the rotation. Start them again afterwards, with the new setting that this " +
		"command prints.\n")
	if !*yes {
		if !c.sys.isTerminal() {
			c.errorf("%s: without a terminal, run again with --yes when every other email-mcp program is stopped. "+
				"Nothing was changed.\n", cmd)
			return exitError
		}
		if !c.confirm("Is every other email-mcp program stopped?") {
			c.errorf("%s: nothing was changed.\n", cmd)
			return exitError
		}
	}

	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	rot, err := a.RotateKEK(ctx, *to)
	if err != nil {
		c.printRotationError(cmd, rot, err)
		return exitError
	}
	c.printRotation(rot)
	return exitOK
}

// printRotationError says why the rotation failed, and that nothing changed.
func (c *cli) printRotationError(cmd string, rot app.Rotation, err error) {
	switch {
	case errors.Is(err, app.ErrSameKEK):
		c.errorf("%s: the data directory already uses this master key (%s), maybe under another spelling of the "+
			"path. Give a new file or the keychain.\n", cmd, rot.From)
	case errors.Is(err, app.ErrKeyFileExists):
		c.errorf("%s: a file exists at the path of --to. A key is never overwritten: give a path where nothing "+
			"exists.\n", cmd)
	default:
		c.errorf("%s: %v\n", cmd, err)
	}
	c.errorf("%s: nothing was changed. Every stored key is still wrapped by the old master key (%s). "+
		"Keep EMAIL_MCP_KEK as it is.\n", cmd, rot.From)
	if rot.MadeKeyFile {
		c.errorf("%s: the new key file %s was made, but no stored key uses it. A rotation never overwrites a "+
			"file: to try again, give another path, or remove that file first.\n",
			cmd, strings.TrimPrefix(rot.To, "file:"))
	}
}

// printRotation tells the user what changed, the exact setting of the next
// start, and when the old key may go.
func (c *cli) printRotation(rot app.Rotation) {
	c.printf("The master key was changed. Every stored key is now wrapped by the new master key (%s).\n", rot.To)
	c.printf("From now on, start email-mcp with this setting. In a shell (POSIX quoting):\n")
	c.printf("  EMAIL_MCP_KEK=%s\n", shellQuote(rot.To))
	c.printf("In the \"env\" block of your harness config (JSON):\n")
	c.printf("  \"EMAIL_MCP_KEK\": %s\n", jsonString(rot.To))
	if rot.To == "keychain" {
		c.printf("(The keychain is also the default: leaving EMAIL_MCP_KEK unset works too.)\n")
	}
	c.printf("Restart every email-mcp program, so it uses the new key.\n")
	if rot.OldKeyFile != "" {
		c.printf("The old key file %s was not deleted. First run \"email-mcp doctor\" with the new setting. "+
			"When it passes, you can delete the old key file.\n", rot.OldKeyFile)
		return
	}
	// Other data directories may use the keychain entry, so the command never
	// says to delete it.
	c.printf("The old master key stays in the keychain (service \"email-mcp\", account \"master-key\"), unchanged. " +
		"Other data directories may use it.\n")
}

// hasDotDot reports whether path has a ".." part.
func hasDotDot(path string) bool {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == filepath.Separator })
	return slices.Contains(parts, "..")
}

// shellQuote quotes s for a POSIX shell: in single quotes, with each single
// quote written as '\”. The shell then takes every other character as it is.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// jsonString writes s as a JSON string.
func jsonString(s string) string {
	// Marshaling a string cannot fail.
	b, _ := json.Marshal(s)
	return string(b)
}
