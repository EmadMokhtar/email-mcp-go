package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// setBeforeRewrap replaces the hook for one test.
func setBeforeRewrap(t *testing.T, hook func()) {
	t.Helper()
	old := beforeRewrap
	beforeRewrap = hook
	t.Cleanup(func() { beforeRewrap = old })
}

// requireLocked checks that another program could not take the lock of dir now.
func requireLocked(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	unlock, err := lockDir(ctx, dir)
	if err == nil {
		unlock()
		t.Fatalf("the lock in %s was free during the rotation", dir)
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// sealTestSecret seals a value with the tenant key of the App, so the test can
// check later that the tenant key still opens it.
func sealTestSecret(t *testing.T, a *App) (version int, sealed []byte) {
	t.Helper()
	version, sealed, err := a.vault.Encrypt(context.Background(), store.LocalTenantID, "test|aad", []byte("sealed-value"))
	require.NoError(t, err)
	return version, sealed
}

// opensWith opens the data directory with only the key file at keyPath, and
// reports whether that key opens the server key and the sealed value.
func opensWith(t *testing.T, dir, keyPath string, version int, sealed []byte) bool {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(st, kek)
	if _, err := vault.ServerKey(context.Background()); err != nil {
		return false
	}
	plain, err := vault.Decrypt(context.Background(), store.LocalTenantID, version, "test|aad", sealed)
	return err == nil && string(plain) == "sealed-value"
}

// RotateKEK wraps every stored key with a new key file, under the lock of the
// data directory, and never deletes the old key file.
func TestRotateKEKToAFileHoldsTheLock(t *testing.T) {
	ta := openTest(t)
	version, sealed := sealTestSecret(t, ta.App)
	newKey := filepath.Join(t.TempDir(), "new.key")
	called := false
	setBeforeRewrap(t, func() {
		called = true
		requireLocked(t, ta.dir)
	})

	rot, err := ta.RotateKEK(context.Background(), "file:"+newKey)

	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, Rotation{From: "file:" + ta.keyPath, To: "file:" + newKey, MadeKeyFile: true, OldKeyFile: ta.keyPath}, rot)
	require.Equal(t, "file:"+newKey, ta.kekSource)
	info, err := os.Stat(newKey)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	entries, err := os.ReadDir(filepath.Dir(newKey))
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the key file, no temporary file")
	_, err = os.Stat(ta.keyPath)
	require.NoError(t, err, "the old key file is never deleted")

	// The App goes on with the new key, and only the new key opens the data.
	_, _, err = ta.vault.Encrypt(context.Background(), store.LocalTenantID, "test|aad", []byte("x"))
	require.NoError(t, err)
	require.NoError(t, ta.CloseNow(context.Background()))
	require.True(t, opensWith(t, ta.dir, newKey, version, sealed))
	require.False(t, opensWith(t, ta.dir, ta.keyPath, version, sealed))
}

// A rotation never overwrites a file: a key file that exists already is
// refused, and it does not change.
func TestRotateKEKRefusesAKeyFileThatExists(t *testing.T) {
	ta := openTest(t)
	version, sealed := sealTestSecret(t, ta.App)
	newKey := filepath.Join(t.TempDir(), "new.key")
	require.NoError(t, secrets.GenerateKeyFile(newKey))
	before, err := os.ReadFile(newKey) //nolint:gosec // G304: a path of the test
	require.NoError(t, err)
	setBeforeRewrap(t, func() { t.Fatal("nothing is wrapped again") })

	rot, err := ta.RotateKEK(context.Background(), "file:"+newKey)

	require.ErrorIs(t, err, ErrKeyFileExists)
	require.False(t, rot.MadeKeyFile)
	after, err := os.ReadFile(newKey) //nolint:gosec // G304: a path of the test
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, "file:"+ta.keyPath, ta.kekSource)
	require.NoError(t, ta.CloseNow(context.Background()))
	require.True(t, opensWith(t, ta.dir, ta.keyPath, version, sealed))
}

// The key file in use is refused under any spelling of its path: with "./",
// with two slashes, and through a folder that is a symbolic link (macOS has
// such links: /var is /private/var). The rotation would wrap the keys with the
// same key and then tell the user to delete the "old" file, which is the only
// key. A spelling with ".." is refused before that, by CheckKEKSource.
func TestRotateKEKRefusesTheKeyInUseUnderAnySpelling(t *testing.T) {
	ta := openTest(t)
	version, sealed := sealTestSecret(t, ta.App)
	dir, name := filepath.Dir(ta.keyPath), filepath.Base(ta.keyPath)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	setBeforeRewrap(t, func() { t.Fatal("nothing is wrapped again") })

	for _, spelling := range []string{
		dir + "/./" + name,
		dir + "//" + name,
		filepath.Join(link, name),
	} {
		rot, err := ta.RotateKEK(context.Background(), "file:"+spelling)
		require.ErrorIs(t, err, ErrSameKEK, spelling)
		require.Empty(t, rot.OldKeyFile, spelling)
	}
	rot, err := ta.RotateKEK(context.Background(), "file:"+dir+"/../"+filepath.Base(dir)+"/"+name)
	require.ErrorIs(t, err, errDotDotInKeyPath)
	require.Empty(t, rot.OldKeyFile)
	require.Equal(t, "file:"+ta.keyPath, ta.kekSource)
	require.NoError(t, ta.CloseNow(context.Background()))
	require.True(t, opensWith(t, ta.dir, ta.keyPath, version, sealed))
}

// With ExistingKeyOnly, Open loads the master key and never makes one: not in
// the keychain, and not as a key file, also for a database without keys.
func TestOpenWithExistingKeyOnlyNeverMakesAKey(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	setUserDirs(t, t.TempDir())

	_, err := Open(context.Background(), Config{DataDir: t.TempDir(), KEKSource: kekKeychain, ExistingKeyOnly: true, LogOutput: &syncBuffer{}})
	require.ErrorIs(t, err, ErrNoMasterKey)
	_, err = keyring.Get("email-mcp", "master-key")
	require.ErrorIs(t, err, keyring.ErrNotFound, "no keychain key was made")

	keyPath := filepath.Join(t.TempDir(), "master.key")
	_, err = Open(context.Background(), Config{DataDir: t.TempDir(), KEKSource: "file:" + keyPath, ExistingKeyOnly: true, LogOutput: &syncBuffer{}})
	require.ErrorIs(t, err, ErrNoMasterKey)
	_, err = os.Stat(keyPath)
	require.ErrorIs(t, err, os.ErrNotExist, "no key file was made")
}

// The keychain belongs to the user, so a rotation to it also holds the lock of
// the user. When the data directory is the directory of that lock (the default
// data directory on macOS and Windows), the rotation takes the one lock once,
// and does not wait for itself.
func TestRotateKEKToTheKeychainHoldsBothLocks(t *testing.T) {
	keyring.MockInit()
	setUserDirs(t, t.TempDir())
	base, err := os.UserConfigDir()
	require.NoError(t, err)
	userDir := filepath.Join(base, dataDirName)

	for name, dataDir := range map[string]string{
		"another data directory":  filepath.Join(t.TempDir(), "data"),
		"the default of the user": userDir,
	} {
		t.Run(name, func(t *testing.T) {
			keyring.MockInit()
			keyPath := filepath.Join(t.TempDir(), "master.key")
			a, err := Open(context.Background(), Config{DataDir: dataDir, KEKSource: "file:" + keyPath, LogOutput: &syncBuffer{}})
			require.NoError(t, err)
			defer func() { _ = a.CloseNow(context.Background()) }()
			setBeforeRewrap(t, func() {
				requireLocked(t, dataDir)
				requireLocked(t, userDir)
			})

			done := make(chan error, 1)
			go func() {
				_, err := a.RotateKEK(context.Background(), kekKeychain)
				done <- err
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the rotation waited for its own lock")
			}
			require.Equal(t, kekKeychain, a.kekSource)
			_, err = keyring.Get("email-mcp", "master-key")
			require.NoError(t, err, "the keychain holds the new key")
		})
	}
}

// A rotation to the source that the App uses, or to a source that is not
// valid, is refused before anything changes.
func TestRotateKEKRefusesBadSources(t *testing.T) {
	ta := openTest(t)
	called := false
	setBeforeRewrap(t, func() { called = true })

	for _, source := range []string{"file:" + ta.keyPath, "file:relative.key", "vault:x", ""} {
		_, err := ta.RotateKEK(context.Background(), source)
		require.Error(t, err, source)
	}
	require.False(t, called)
	_, err := os.Stat(filepath.Join(ta.dir, "relative.key"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// Resolve fills the empty fields as Open does, and the App reports the master
// key that it uses.
func TestResolveAndTheSettingsOfTheApp(t *testing.T) {
	t.Setenv(envKEK, "file:/env.key")
	t.Setenv(envDataDir, "/env/data")
	got, err := Config{}.Resolve()
	require.NoError(t, err)
	require.Equal(t, "/env/data", got.DataDir)
	require.Equal(t, "file:/env.key", got.KEKSource)

	ta := openTest(t)
	require.Equal(t, "file:"+ta.keyPath, ta.kekSource)
}

// CloseNow is for a command that never served: it leaves the queued messages
// to the program that serves, and returns at once.
func TestCloseNowLeavesTheQueue(t *testing.T) {
	ta := openTest(t)
	acct := ta.addAccount(t)
	item, err := ta.Outbox.Enqueue(context.Background(), store.LocalTenantID, acct.ID, testMessage(), time.Second, "")
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, ta.CloseNow(context.Background()))

	require.Less(t, time.Since(start), 500*time.Millisecond, "it does not wait for the queue")
	require.Zero(t, ta.factory.backend.sentCount())
	status, _ := outboxStatus(t, ta.dir, item.ID)
	require.Equal(t, "queued", status)
	require.True(t, ta.factory.isClosed())
	require.NotContains(t, ta.logs.String(), "drain_stopped")
	require.NoError(t, ta.Close(context.Background()), "a second close does nothing")
}

// A key file that is in place but maybe not on the disk yet (the folder sync
// failed) counts as made: the command line tells the user where it is. Nothing
// is wrapped with it.
func TestRotateKEKReportsAKeyFileThatIsNotDurable(t *testing.T) {
	ta := openTest(t)
	version, sealed := sealTestSecret(t, ta.App)
	newKey := filepath.Join(t.TempDir(), "new.key")
	old := generateKeyFile
	t.Cleanup(func() { generateKeyFile = old })
	generateKeyFile = func(path string) error {
		if err := secrets.GenerateKeyFile(path); err != nil {
			return err
		}
		return fmt.Errorf("%w: the folder sync failed in the test", secrets.ErrNotDurable)
	}
	setBeforeRewrap(t, func() { t.Fatal("nothing is wrapped with a key that may not be on the disk") })

	rot, err := ta.RotateKEK(context.Background(), "file:"+newKey)

	require.ErrorIs(t, err, secrets.ErrNotDurable)
	require.True(t, rot.MadeKeyFile)
	require.FileExists(t, newKey)
	require.Equal(t, "file:"+ta.keyPath, ta.kekSource)
	require.NoError(t, ta.CloseNow(context.Background()))
	require.True(t, opensWith(t, ta.dir, ta.keyPath, version, sealed))
}
