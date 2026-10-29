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

// kekLockFile is the name of the lock file in the data directory. Open holds the
// lock while it finds or makes the master key.
const kekLockFile = "kek.lock"

// lockPoll is how often lockDataDir tries again to take a lock that another
// program holds.
const lockPoll = 25 * time.Millisecond

// afterKeyLookup is a hook for tests. loadKEK calls it after it looked for the
// key file and before it makes one. A test makes it slow, to show that a second
// program cannot look in between.
var afterKeyLookup = func() {}

// checkKEKSource returns an error unless source is "keychain" or "file:" with a
// path.
func checkKEKSource(source string) error {
	if source == kekKeychain {
		return nil
	}
	if path, ok := strings.CutPrefix(source, kekFilePrefix); ok && path != "" {
		return nil
	}
	return fmt.Errorf("app: unknown master key source %q: use %q or %q", source, kekKeychain, kekFilePrefix+"<path>")
}

// loadKEK finds the master key of source, or makes it on the first run.
//
// Two programs can start at the same time on a new data directory (for
// example, two MCP clients each start one). Both would find no key and make one
// each: two different keychain keys, or a key file that one program reads while
// the other still writes it. So loadKEK holds a lock file in the data directory
// while it looks for the key and makes it. The data directory must exist.
//
// A missing key file is made only when the database has no wrapped keys yet. A
// database with keys needs the key that wrapped them, and a new key could never
// open them: the path is wrong, so loadKEK refuses.
func loadKEK(ctx context.Context, dataDir, source string, st *store.Store, log *slog.Logger) (secrets.KEK, error) {
	if err := checkKEKSource(source); err != nil {
		return nil, err
	}
	unlock, err := lockDataDir(ctx, dataDir)
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

// lockDataDir takes the lock file of the data directory and returns the
// function that releases it. It waits while another program (or another Open in
// this program) holds the lock, and stops waiting when ctx ends.
//
// The operating system releases the lock when the program ends, so a program
// that crashes never leaves a lock behind. The file itself stays.
func lockDataDir(ctx context.Context, dataDir string) (unlock func(), err error) {
	path := filepath.Join(dataDir, kekLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: the path is the data directory plus a fixed name
	if err != nil {
		return nil, fmt.Errorf("app: open the lock file: %w", err)
	}
	for {
		locked, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("app: lock the data directory: %w", err)
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
			return nil, fmt.Errorf("app: wait for the lock of the data directory: %w", ctx.Err())
		case <-time.After(lockPoll):
		}
	}
}
