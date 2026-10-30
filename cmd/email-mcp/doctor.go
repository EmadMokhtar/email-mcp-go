package main

// This file holds the "doctor" command. It checks the install and prints one
// line per check: "<check>: ok" or "<check>: FAIL <reason>". It exits with 1
// when a check fails.
//
// It never makes a master key: it loads the key with
// app.Config.ExistingKeyOnly, so a missing keychain entry or key file is a
// failure. It makes no data directory and no database file: when they are
// missing, it stops before it opens anything. What it may write: when it opens
// an existing database, app.Open runs the migrations that have not run yet,
// adds the row of the local tenant when it is missing, and makes the server
// key (wrapped by the existing master key) when the database has none. And an
// account that needed to be reconnected, and whose logins work now, is marked
// as connected again (Service.ProbeAccount).
//
// The checks, in order:
//   - the data directory exists and has mode 0700;
//   - the database file exists, and it and its write-ahead log files have mode
//     0600;
//   - the master key of EMAIL_MCP_KEK exists and opens the stored keys (the
//     App opens). When the App does not open because a newer email-mcp wrote
//     the database (store.ErrNewerSchema), the check is named "database
//     schema" instead;
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
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The modes that the checks want.
const (
	wantDirMode  fs.FileMode = 0o700
	wantFileMode fs.FileMode = 0o600
)

// schemaName is the name of the check that fails when a newer email-mcp wrote
// the database (store.ErrNewerSchema).
const schemaName = "database schema"

// msgNoMasterKey is the reason of the master key check when the key does not
// exist.
const msgNoMasterKey = "the master key does not exist: no keychain entry email-mcp/master-key, or no key file at " +
	"the path of EMAIL_MCP_KEK. Check EMAIL_MCP_KEK. doctor never makes a key"

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
	rest, code, ok := c.parseFlags(cmd, flags, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.unexpected(cmd)
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
	a, err := app.Open(ctx, app.Config{
		DataDir: cfg.DataDir, KEKSource: cfg.KEKSource, Local: true, LogOutput: c.stderr, ExistingKeyOnly: true,
	})
	switch {
	case errors.Is(err, app.ErrNoMasterKey):
		d.fail(keyName, "%s", msgNoMasterKey)
		return exitError
	case errors.Is(err, store.ErrNewerSchema):
		// The key is not the problem: a newer email-mcp wrote the database.
		// app.Open returns the error of store.Open as it is, and the text of
		// ErrNewerSchema is fixed.
		d.fail(schemaName, "%s", store.ErrNewerSchema.Error())
		return exitError
	case err != nil:
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
	path := filepath.Join(dir, store.DBFileName)
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
