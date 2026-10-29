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
	"path/filepath"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
)

// The forms of --to.
const (
	keySourceKeychain = "keychain"
	keySourceFile     = "file:"
)

// keyCommands are the subcommands of "key".
var keyCommands = []command{
	{name: "rotate", summary: "change the master key: rotate --to file:<absolute path>|keychain", run: runKeyRotate},
}

func runKey(ctx context.Context, c *cli, args []string) int {
	return runGroup(ctx, c, "key", keyCommands, args)
}

func runKeyRotate(ctx context.Context, c *cli, args []string) int {
	const cmd = "key rotate"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	to := fs.String("to", "", "the new master key: file:<absolute path> (made with mode 0600 when it is missing) or keychain")
	rest, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.usage(cmd, "unexpected argument %q", rest[0])
	}
	if !validKeySource(*to) {
		return c.usage(cmd, "--to must be file:<absolute path> or keychain")
	}

	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	c.printf("Stop every other email-mcp program that uses this data directory before the rotation, " +
		"and start it again afterwards.\n")
	rot, err := a.RotateKEK(ctx, *to)
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		c.errorf("%s: nothing was changed. Every stored key is still wrapped by the old master key (%s). "+
			"Keep EMAIL_MCP_KEK as it is.\n", cmd, rot.From)
		if rot.MadeKeyFile {
			c.errorf("%s: the new key file %s was made, but no stored key uses it. You can run the command again "+
				"with the same --to.\n", cmd, strings.TrimPrefix(rot.To, keySourceFile))
		}
		return exitError
	}
	c.printRotation(rot)
	return exitOK
}

// validKeySource reports whether source is "keychain" or "file:" with an
// absolute path. The App checks it again.
func validKeySource(source string) bool {
	if source == keySourceKeychain {
		return true
	}
	path, ok := strings.CutPrefix(source, keySourceFile)
	return ok && filepath.IsAbs(path)
}

// printRotation tells the user what changed, the exact setting of the next
// start, and when the old key may go.
func (c *cli) printRotation(rot app.Rotation) {
	c.printf("The master key was changed. Every stored key is now wrapped by the new master key (%s).\n", rot.To)
	c.printf("From now on, start email-mcp with this setting, in your shell and in the \"env\" block of your harness config:\n")
	c.printf("  EMAIL_MCP_KEK=%s\n", rot.To)
	if rot.To == keySourceKeychain {
		c.printf("(The keychain is also the default: leaving EMAIL_MCP_KEK unset works too.)\n")
	}
	c.printf("Restart every email-mcp program that is still running, so it uses the new key.\n")
	if old, isFile := strings.CutPrefix(rot.From, keySourceFile); isFile {
		c.printf("The old key file %s was not deleted. First run \"email-mcp doctor\" with the new setting. "+
			"When it passes, you can delete the old key file.\n", old)
		return
	}
	c.printf("The old master key stays in the keychain (service \"email-mcp\", account \"master-key\"). First run " +
		"\"email-mcp doctor\" with the new setting. When it passes, and no other data directory uses the keychain, " +
		"you can delete that entry.\n")
}
