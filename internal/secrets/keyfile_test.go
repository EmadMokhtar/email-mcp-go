package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// keyFileRace replaces the hooks of GenerateKeyFile for one test. Every call
// waits in its file sync until calls calls have arrived there, so each one has
// passed the first check for an existing file before any of them puts its key
// in place. It records what each temporary file holds, and which temporary file
// was put in place.
type keyFileRace struct {
	mu       sync.Mutex
	contents map[string]string // temporary file name -> key text
	placed   string            // the temporary file that the link put in place
	arrived  chan struct{}
	calls    int
}

func newKeyFileRace(t *testing.T, calls int, link func(oldname, newname string) error) *keyFileRace {
	t.Helper()
	r := &keyFileRace{contents: map[string]string{}, arrived: make(chan struct{}, calls), calls: calls}
	oldSync, oldLink := syncFile, linkFile
	t.Cleanup(func() { syncFile, linkFile = oldSync, oldLink })
	var once sync.Once
	all := make(chan struct{})
	syncFile = func(f *os.File) error {
		// Only the sync of a temporary file meets the others. The exclusive
		// create of a file system without hard links syncs the key file too.
		if !strings.HasPrefix(filepath.Base(f.Name()), ".email-mcp-key-") {
			return f.Sync()
		}
		content, err := os.ReadFile(f.Name())
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.contents[f.Name()] = string(content)
		r.mu.Unlock()
		r.arrived <- struct{}{}
		if len(r.arrived) == calls {
			once.Do(func() { close(all) })
		}
		select {
		case <-all:
		case <-time.After(10 * time.Second):
			t.Error("the calls did not meet")
		}
		return f.Sync()
	}
	linkFile = func(oldname, newname string) error {
		err := link(oldname, newname)
		if err == nil {
			r.mu.Lock()
			r.placed = oldname
			r.mu.Unlock()
		}
		return err
	}
	return r
}

// runConcurrently runs n calls of GenerateKeyFile(path) at the same time and
// returns their errors.
func runConcurrently(n int, path string) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = GenerateKeyFile(path)
		}()
	}
	wg.Wait()
	return errs
}

// requireOneWinner checks that exactly one call made the key file and the
// others got fs.ErrExist.
func requireOneWinner(t *testing.T, errs []error) {
	t.Helper()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		require.ErrorIs(t, err, fs.ErrExist)
	}
	require.Equal(t, 1, wins, "exactly one call makes the key file")
}

// requireOnlyTheKeyFile checks that the folder holds the key file and no
// temporary file.
func requireOnlyTheKeyFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "master.key", entries[0].Name())
}

// Two programs that make one key file path at the same moment (two data
// directories that share it) both pass the first check. The link that puts
// the key in place fails for the second one: it never replaces the first key,
// whose program may already wrap its data with it.
func TestGenerateKeyFileNeverReplacesAConcurrentKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	race := newKeyFileRace(t, 2, os.Link)

	errs := runConcurrently(2, path)

	requireOneWinner(t, errs)
	require.Len(t, race.contents, 2, "both calls wrote a key")
	require.Equal(t, race.contents[race.placed], string(readFile(t, path)), "the file holds the key of the call that won")
	requireOnlyTheKeyFile(t, dir)
}

// errNoHardLinks is the error of a file system without hard links, such as FAT.
var errNoHardLinks = &os.LinkError{Op: "link", Err: errors.ErrUnsupported}

// On a file system without hard links, the key file is made with an exclusive
// create of its path, which never replaces a file either. It is not renamed:
// a rename would replace the key of another program.
func TestGenerateKeyFileWithoutHardLinks(t *testing.T) {
	noLinks := func(string, string) error { return errNoHardLinks }

	t.Run("one call", func(t *testing.T) {
		old := linkFile
		t.Cleanup(func() { linkFile = old })
		linkFile = noLinks
		dir := t.TempDir()
		path := filepath.Join(dir, "master.key")

		require.NoError(t, GenerateKeyFile(path))

		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
		_, err := NewFileKEK(path)
		require.NoError(t, err)
		requireOnlyTheKeyFile(t, dir)
		before := readFile(t, path)
		require.ErrorIs(t, GenerateKeyFile(path), fs.ErrExist)
		require.Equal(t, before, readFile(t, path))
		requireOnlyTheKeyFile(t, dir)
	})

	t.Run("two calls at once", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "master.key")
		race := newKeyFileRace(t, 2, func(string, string) error { return errNoHardLinks })

		errs := runConcurrently(2, path)

		requireOneWinner(t, errs)
		var written []string
		for _, c := range race.contents {
			written = append(written, c)
		}
		require.Contains(t, written, string(readFile(t, path)), "the file holds one whole key")
		requireOnlyTheKeyFile(t, dir)
	})
}

// A failed sync of the folder comes after the key file is in place. The error
// says so: the file exists, but it may not be on the disk yet.
func TestGenerateKeyFileReportsAFolderSyncFailure(t *testing.T) {
	old := syncFolder
	t.Cleanup(func() { syncFolder = old })
	syncFolder = func(string) error { return errors.New("the disk is gone") }
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")

	err := GenerateKeyFile(path)

	require.ErrorIs(t, err, ErrNotDurable)
	_, err = NewFileKEK(path)
	require.NoError(t, err, "the key file is in place")
	requireOnlyTheKeyFile(t, dir)
}

// The temporary file and the synced folder are the folder that the system
// finds for the path, also when a ".." follows a folder that is a symbolic
// link. Cleaning the text of the path would name another folder.
func TestGenerateKeyFileUsesTheFolderOfThePathAsWritten(t *testing.T) {
	skipOnWindows(t)
	keys := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(keys, "sub"), 0o700))
	other := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(keys, "sub"), filepath.Join(other, "l")))
	path := other + "/l/../master.key" // the system reads it as keys/master.key
	var synced []string
	old := syncFolder
	t.Cleanup(func() { syncFolder = old })
	syncFolder = func(dir string) error {
		synced = append(synced, dir)
		return syncDir(dir)
	}

	require.NoError(t, GenerateKeyFile(path))

	entries, err := os.ReadDir(keys)
	require.NoError(t, err)
	require.Equal(t, []string{"master.key", "sub"}, names(entries), "the key file is made in the folder that the system finds")
	entries, err = os.ReadDir(other)
	require.NoError(t, err)
	require.Equal(t, []string{"l"}, names(entries), "no temporary file in the other folder")
	require.Len(t, synced, 1)
	got, err := os.Stat(synced[0])
	require.NoError(t, err)
	want, err := os.Stat(keys)
	require.NoError(t, err)
	require.True(t, os.SameFile(got, want), "the folder of the key is synced, not %s", synced[0])
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}
