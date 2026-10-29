package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The forms of Config.KEKSource.
const (
	kekKeychain   = "keychain"
	kekFilePrefix = "file:"
)

// kekLockFile is the name of the lock file (see kekLockDir). Open holds the
// lock while it finds or makes the master key.
const kekLockFile = "kek.lock"

// lockPoll is how often lockDir tries again to take a lock that another
// program holds.
const lockPoll = 25 * time.Millisecond

// afterKeyLookup is a hook for tests. loadKEK calls it after it looked for the
// key file and before it makes one. A test makes it slow, to show that a second
// program cannot look in between.
var afterKeyLookup = func() {}

// checkKEKSource returns an error unless source is "keychain" or "file:" with an
// absolute path. A relative path would depend on the directory that the MCP
// client starts the server in, so the same setting could point at different
// files.
func checkKEKSource(source string) error {
	if source == kekKeychain {
		return nil
	}
	path, ok := strings.CutPrefix(source, kekFilePrefix)
	if !ok || path == "" {
		return fmt.Errorf("app: unknown master key source %q: use %q or %q", source, kekKeychain, kekFilePrefix+"<absolute path>")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("app: the master key file path %q must be absolute: a relative path depends on the "+
			"directory that the MCP client starts the server in", path)
	}
	return nil
}

// loadKEK finds the master key of source, or makes it on the first run.
//
// Two programs can start at the same time on a new install (for example, two
// MCP clients each start one). Both would find no key and make one each: two
// different keychain keys (one overwrites the other), or a key file that one
// program reads while the other still writes it. So loadKEK holds a lock file
// while it looks for the key and makes it (see kekLockDir). The data directory
// must exist.
//
// A missing key file is made only when the database has no wrapped keys yet. A
// database with keys needs the key that wrapped them, and a new key could never
// open them: the path is wrong, so loadKEK refuses.
func loadKEK(ctx context.Context, dataDir, source string, st *store.Store, log *slog.Logger) (secrets.KEK, error) {
	if err := checkKEKSource(source); err != nil {
		return nil, err
	}
	lockPath, err := kekLockDir(source, dataDir)
	if err != nil {
		return nil, err
	}
	unlock, err := lockDir(ctx, lockPath)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if source == kekKeychain {
		return secrets.NewKeychainKEK()
	}
	path := strings.TrimPrefix(source, kekFilePrefix)
	_, statErr := os.Lstat(path)
	afterKeyLookup()
	if errors.Is(statErr, fs.ErrNotExist) {
		if err := makeKeyFile(ctx, path, st); err != nil {
			return nil, err
		}
		log.Info("app: made a new master key file")
	}
	return secrets.NewFileKEK(path)
}

// makeKeyFile makes a new key file at path, but only for a database that has no
// wrapped keys yet. The caller holds the lock of the data directory.
//
// The lock of a key file is per data directory. Two data directories that start
// at the same time with one new key file path both try to make it: the file is
// made with O_EXCL, so one of them fails with an error and makes no second key.
func makeKeyFile(ctx context.Context, path string, st *store.Store) error {
	hasKeys, err := databaseHasKeys(ctx, st)
	if err != nil {
		return err
	}
	if hasKeys {
		return fmt.Errorf("app: the master key file %s does not exist, and the database has keys that "+
			"only the old master key can open. Check the path in %s", path, envKEK)
	}
	if err := secrets.GenerateKeyFile(path); err != nil {
		return fmt.Errorf("app: make the master key file: %w", err)
	}
	return nil
}

// databaseHasKeys reports whether the database holds a wrapped server key or a
// wrapped tenant key.
func databaseHasKeys(ctx context.Context, st *store.Store) (bool, error) {
	_, _, err := st.ServerKey(ctx)
	switch {
	case err == nil:
		return true, nil
	case !errors.Is(err, store.ErrNotFound):
		return false, fmt.Errorf("app: read the server key: %w", err)
	}
	tenantKeys, err := st.ListTenantKeys(ctx)
	if err != nil {
		return false, fmt.Errorf("app: read the tenant keys: %w", err)
	}
	return len(tenantKeys) > 0, nil
}

// kekLockDir returns the directory of the lock file that loadKEK holds.
//
// The keychain entry belongs to the user, not to a data directory: two data
// directories (for example two --data-dir settings) share it. So the lock of the
// keychain is in a directory of the user, os.UserConfigDir()/email-mcp, which
// kekLockDir makes with mode 0700 when it is missing. A key file belongs to its
// data directory, so its lock is there.
func kekLockDir(source, dataDir string) (string, error) {
	if source != kekKeychain {
		return dataDir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("app: find the directory of the keychain lock: %w", err)
	}
	dir := filepath.Join(base, dataDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("app: make the directory of the keychain lock: %w", err)
	}
	return dir, nil
}

// lockDir takes the lock file (kek.lock, mode 0600) in dir and returns the
// function that releases it. It waits while another program (or another Open in
// this program) holds the lock, and stops waiting when ctx ends.
//
// The operating system releases the lock when the program ends, so a program
// that crashes never leaves a lock behind. The file itself stays.
func lockDir(ctx context.Context, dir string) (unlock func(), err error) {
	path := filepath.Join(dir, kekLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: the path is a directory of the program plus a fixed name
	if err != nil {
		return nil, fmt.Errorf("app: open the lock file: %w", err)
	}
	for {
		locked, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("app: take the lock of the master key: %w", err)
		}
		if locked {
			return func() {
				// Closing the file also releases the lock. The unlock is there to
				// make the order clear.
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("app: wait for the lock of the master key: %w", ctx.Err())
		case <-time.After(lockPoll):
		}
	}
}
