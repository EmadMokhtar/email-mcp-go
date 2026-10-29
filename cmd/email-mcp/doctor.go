package main

// This file holds the "doctor" command. It checks the install and prints one
// line per check: "<check>: ok" or "<check>: FAIL <reason>". It exits with 1
// when a check fails. It makes nothing: a data directory that does not exist is
// a failure, and it stays missing.
//
// The checks, in order:
//   - the data directory exists and has mode 0700;
//   - the database file exists, and it and its write-ahead log files have mode
//     0600;
//   - the master key of EMAIL_MCP_KEK opens the stored keys (the App opens);
//   - each account logs in to its IMAP and SMTP servers, with its stored
//     settings, password and pins (Service.ProbeAccount).
//
// A reason is a fixed text. It never holds a password or a text of a server.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
)

// dbFileName is the name of the database file in the data directory.
const dbFileName = "email-mcp.db"

// The modes that the checks want.
const (
	wantDirMode  fs.FileMode = 0o700
	wantFileMode fs.FileMode = 0o600
)

// doctor collects the result of the checks.
type doctor struct {
	c      *cli
	failed bool
}

// ok prints a check that passed.
func (d *doctor) ok(name string) {
	d.c.printf("%s: ok\n", name)
}

// fail prints a check that failed.
func (d *doctor) fail(name, format string, args ...any) {
	d.failed = true
	d.c.printf("%s: FAIL %s\n", name, fmt.Sprintf(format, args...))
}

func runDoctor(ctx context.Context, c *cli, args []string) int {
	const cmd = "doctor"
	flags := c.newFlags(cmd)
	dataDir := flags.String("data-dir", "", dataDirUsage)
	rest, code, ok := parseFlags(flags, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.usage(cmd, "unexpected argument %q", rest[0])
	}
	cfg, err := app.Config{DataDir: *dataDir}.Resolve()
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	d := &doctor{c: c}
	dirName := "data directory " + cfg.DataDir
	if !d.checkDir(dirName, cfg.DataDir) {
		return exitError
	}
	if !d.checkDatabase("database file", cfg.DataDir) {
		return exitError
	}

	keyName := "master key (" + cfg.KEKSource + ")"
	a, err := app.Open(ctx, app.Config{DataDir: cfg.DataDir, KEKSource: cfg.KEKSource, Local: true, LogOutput: c.stderr})
	if err != nil {
		d.fail(keyName, "%v", err)
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	d.ok(keyName)

	views, err := a.Service.ListStoredAccounts(userContext(ctx))
	if err != nil {
		d.fail("accounts", "%v", err)
		return exitError
	}
	if len(views) == 0 {
		c.printf("accounts: ok (none is connected)\n")
	}
	for _, v := range views {
		name := fmt.Sprintf("account %s (%s)", v.ID, v.Address)
		if err := a.Service.ProbeAccount(userContext(ctx), v.ID); err != nil {
			d.fail(name, "%s", probeText(err))
			continue
		}
		d.ok(name)
	}
	if d.failed {
		return exitError
	}
	return exitOK
}

// checkDir checks the data directory. It returns false when the other checks
// cannot run: the directory is missing or is not a directory.
func (d *doctor) checkDir(name, dir string) bool {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		d.fail(name, "it does not exist. Connect an account with \"email-mcp setup\" or \"email-mcp account add\" first, "+
			"or give the right directory with --data-dir or EMAIL_MCP_DATA_DIR")
		return false
	case err != nil:
		d.fail(name, "it cannot be read: %v", err)
		return false
	case !info.IsDir():
		d.fail(name, "it is not a directory")
		return false
	}
	if mode := info.Mode().Perm(); runtime.GOOS != "windows" && mode != wantDirMode {
		d.fail(name, "its mode is %04o, it must be %04o. Fix it with: chmod 700 %s", mode, wantDirMode, dir)
		return true
	}
	d.ok(name)
	return true
}

// checkDatabase checks the database file and its write-ahead log files. It
// returns false when the file is missing.
func (d *doctor) checkDatabase(name, dir string) bool {
	path := filepath.Join(dir, dbFileName)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		d.fail(name, "%s does not exist", path)
		return false
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			info, err := os.Stat(p)
			if errors.Is(err, fs.ErrNotExist) && p != path {
				continue
			}
			if err != nil {
				d.fail(name, "%s cannot be read: %v", p, err)
				return true
			}
			if mode := info.Mode().Perm(); mode != wantFileMode {
				d.fail(name, "%s has mode %04o, it must be %04o. Fix it with: chmod 600 %s", p, mode, wantFileMode, p)
				return true
			}
		}
	}
	d.ok(name)
	return true
}
