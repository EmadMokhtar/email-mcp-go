package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

// CheckKEKSource returns an error unless source is "keychain" or "file:" with an
// absolute path. A relative path would depend on the directory that the MCP
// client starts the server in, so the same setting could point at different
// files. Open and RotateKEK check every source with it; the command line checks
// --to with it before it opens anything.
func CheckKEKSource(source string) error {
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
//
// With existingOnly, it only loads: a missing keychain entry or key file is an
// error that matches ErrNoMasterKey, and nothing is made. It then takes no lock
// either, because it makes nothing that a lock must guard.
func loadKEK(ctx context.Context, dataDir, source string, st *store.Store, log *slog.Logger, existingOnly bool) (secrets.KEK, error) {
	if err := CheckKEKSource(source); err != nil {
		return nil, err
	}
	if existingOnly {
		return loadExistingKEK(source)
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

// loadExistingKEK loads the master key of source, and never makes one. A
// missing key is an error that matches ErrNoMasterKey.
func loadExistingKEK(source string) (secrets.KEK, error) {
	if source == kekKeychain {
		kek, err := secrets.LoadKeychainKEK()
		if errors.Is(err, secrets.ErrNoKey) {
			return nil, fmt.Errorf("%w: the keychain has no entry email-mcp/master-key", ErrNoMasterKey)
		}
		return kek, err
	}
	path := strings.TrimPrefix(source, kekFilePrefix)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: the key file %s does not exist", ErrNoMasterKey, path)
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

// beforeRewrap is a hook for tests. RotateKEK calls it when it holds the locks
// and has the new master key, just before it wraps the stored keys again.
var beforeRewrap = func() {}

// Rotation describes a change of the master key by RotateKEK.
type Rotation struct {
	// From and To are the sources of the old and the new master key, such as
	// "keychain" or "file:/keys/new.key".
	From, To string
	// MadeKeyFile is true when RotateKEK made the key file of To. It can be true
	// also when the rotation failed: the new key file then holds no stored key.
	MadeKeyFile bool
	// OldKeyFile is the path of the old key file after a rotation that
	// succeeded and whose old source is a key file. RotateKEK refuses the key in
	// use under any spelling, so this file is not the new key. It is empty when
	// the old key is in the keychain, which other data directories may use.
	OldKeyFile string
}

// The errors of master keys. Callers check them with errors.Is.
var (
	// ErrSameKEK means that a rotation was asked for the master key that the
	// data directory uses already, maybe under another spelling of its path.
	// Nothing was changed.
	ErrSameKEK = errors.New("app: the data directory already uses this master key")
	// ErrKeyFileExists means that a rotation was asked for a key file path
	// where something exists already. A key is never overwritten. Nothing was
	// changed.
	ErrKeyFileExists = errors.New("app: a file exists at the path of the new master key, and a key is never overwritten")
	// ErrNoMasterKey means that Open with Config.ExistingKeyOnly found no master
	// key: no keychain entry, or no key file. Nothing was made.
	ErrNoMasterKey = errors.New("app: the master key does not exist")
)

// RotateKEK changes the master key of the data directory to the key of source
// ("keychain" or "file:<absolute path>"). It wraps every stored key again with
// the new key (secrets.Vault.RewrapAll). The sealed values do not change. From
// then on the App uses the new key.
//
// It is for a command that does not serve: call it before Run, or without Run.
// Every other program that uses the data directory must be stopped first, and
// started again afterwards with the new source (see secrets.Vault.RewrapAll).
//
// In this order it:
//  1. refuses a source that is not valid, and the key that the App uses, also
//     under another spelling of its path ("./", "//", "..", a folder that is
//     a symbolic link): ErrSameKEK;
//  2. takes the lock of the old source and the lock of the new one (see
//     kekLockDir), in a fixed order, and holds them to the end. Both locks are
//     the lock of the data directory for two key files. A program that starts
//     now waits, so it cannot make or use a key in the middle;
//  3. gets the new key. For a key file it refuses a path where anything exists
//     (ErrKeyFileExists): a key is never overwritten. Otherwise it writes the
//     new file with secrets.GenerateDurableKeyFile: a temporary file with mode
//     0600, synced to the disk and closed, then renamed into place, then the
//     folder synced. So the key is on the disk before any stored key depends
//     on it. The keychain gives its key, or makes one when it has none;
//  4. refuses a new key with the id of the key in use (ErrSameKEK), as a last
//     check;
//  5. wraps every stored key again, in one database transaction.
//
// It is all or nothing. On any error, every stored key is still wrapped by the
// old master key, and the App keeps using it. The old key file, or the old
// keychain entry, is never deleted or changed: the user deletes it after
// checking that the new key works.
func (a *App) RotateKEK(ctx context.Context, source string) (Rotation, error) {
	a.kekMu.Lock()
	defer a.kekMu.Unlock()
	rot := Rotation{From: a.kekSource, To: source}

	if err := CheckKEKSource(source); err != nil {
		return rot, err
	}
	if sameKEKSource(a.kekSource, source) {
		return rot, fmt.Errorf("%w (%s)", ErrSameKEK, a.kekSource)
	}
	oldLock, err := kekLockDir(a.kekSource, a.dataDir)
	if err != nil {
		return rot, err
	}
	newLock, err := kekLockDir(source, a.dataDir)
	if err != nil {
		return rot, err
	}
	unlock, err := lockDirs(ctx, oldLock, newLock)
	if err != nil {
		return rot, err
	}
	defer unlock()

	newKEK, made, err := openNewKEK(source)
	rot.MadeKeyFile = made
	if err != nil {
		return rot, err
	}
	if newKEK.ID() == a.kekID {
		return rot, fmt.Errorf("%w (%s)", ErrSameKEK, a.kekSource)
	}
	beforeRewrap()
	if err := a.vault.RewrapAll(ctx, newKEK); err != nil {
		return rot, fmt.Errorf("app: wrap the stored keys with the new master key: %w", err)
	}
	if old, isFile := strings.CutPrefix(a.kekSource, kekFilePrefix); isFile {
		rot.OldKeyFile = old
	}
	a.kekSource, a.kekID = source, newKEK.ID()
	a.log.Info("app: changed the master key")
	return rot, nil
}

// sameKEKSource reports whether two valid sources name the same master key:
// the same text, or two key file paths that name one file. The paths are
// compared in their absolute, clean form with the symbolic links resolved, and
// two files that exist are also compared with os.SameFile (which also sees a
// hard link).
func sameKEKSource(a, b string) bool {
	if a == b {
		return true
	}
	pathA, fileA := strings.CutPrefix(a, kekFilePrefix)
	pathB, fileB := strings.CutPrefix(b, kekFilePrefix)
	if !fileA || !fileB {
		return false
	}
	if realPath(pathA) == realPath(pathB) {
		return true
	}
	infoA, errA := os.Stat(pathA)
	infoB, errB := os.Stat(pathB)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// realPath returns path in its absolute, clean form, with the symbolic links
// resolved: of the whole path when it exists, else of its folder.
func realPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// openNewKEK gets the master key of source for a rotation. For a key file, it
// refuses a path where anything exists (ErrKeyFileExists), and writes a new
// key file that is on the disk when it returns. made is true when it made a key
// file. The caller holds the lock of source.
func openNewKEK(source string) (kek secrets.KEK, made bool, err error) {
	if source == kekKeychain {
		kek, err := secrets.NewKeychainKEK()
		return kek, false, err
	}
	path := strings.TrimPrefix(source, kekFilePrefix)
	if err := secrets.GenerateDurableKeyFile(path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, false, fmt.Errorf("%w (%s)", ErrKeyFileExists, path)
		}
		return nil, false, fmt.Errorf("app: make the new master key file: %w", err)
	}
	kek, err = secrets.NewFileKEK(path)
	return kek, true, err
}

// lockDirs takes the locks of the directories, in the order of their names, so
// two programs that take the same two locks cannot wait for each other. A
// directory that is the same as one before it is locked only once: two locks of
// one file in one program would wait for each other. It returns the function
// that releases the locks.
func lockDirs(ctx context.Context, dirs ...string) (unlock func(), err error) {
	var unique []string
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if !slices.ContainsFunc(unique, func(d string) bool { return sameDir(d, dir) }) {
			unique = append(unique, dir)
		}
	}
	slices.Sort(unique)
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	for _, dir := range unique {
		u, err := lockDir(ctx, dir)
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, u)
	}
	return release, nil
}

// sameDir reports whether a and b name the same directory, also through
// another spelling or a symbolic link.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}
