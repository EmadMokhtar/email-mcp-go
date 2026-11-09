package seedfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFile returns a build function that writes content to name, and counts
// its calls in calls.
func writeFile(name, content string, calls *int) func(dir string) error {
	return func(dir string) error {
		*calls++
		return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	}
}

func TestSeedBuildsTheFileOnce(t *testing.T) {
	calls := 0
	f := New("db", writeFile("db", "content", &calls))

	first, second := t.TempDir(), t.TempDir()
	f.Seed(t, first)
	f.Seed(t, second)

	require.Equal(t, 1, calls)
	for _, dir := range []string{first, second} {
		data, err := os.ReadFile(filepath.Join(dir, "db")) //nolint:gosec // G304: a test folder plus a fixed name
		require.NoError(t, err)
		require.Equal(t, "content", string(data))
	}
}

func TestSeedMakesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	calls := 0
	f := New("db", writeFile("db", "content", &calls))
	dir := filepath.Join(t.TempDir(), "data")

	f.Seed(t, dir)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	info, err = os.Stat(filepath.Join(dir, "db"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// A test may open the same folder more than once. The second Seed must not
// replace the data of the first open.
func TestSeedLeavesAnExistingFileAlone(t *testing.T) {
	calls := 0
	f := New("db", writeFile("db", "content", &calls))
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))

	f.Seed(t, dir)

	data, err := os.ReadFile(path) //nolint:gosec // G304: a test folder plus a fixed name
	require.NoError(t, err)
	require.Equal(t, "existing", string(data))
}

// A build that leaves a second file, such as the write-ahead log of SQLite,
// did not finish the file. The copy would miss what is in the second file.
func TestMakeRefusesASecondFile(t *testing.T) {
	f := New("db", func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, "db"), []byte("content"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "db-wal"), []byte("log"), 0o600)
	})

	_, err := f.bytes()

	require.ErrorContains(t, err, "also left db-wal")
}

func TestMakeReportsTheErrorOfBuild(t *testing.T) {
	boom := errors.New("boom")
	f := New("db", func(string) error { return boom })

	_, err := f.bytes()
	require.ErrorIs(t, err, boom)

	// The error is kept: a second call does not run build again.
	_, again := f.bytes()
	require.ErrorIs(t, again, boom)
}

func TestMakeNeedsTheFile(t *testing.T) {
	f := New("db", func(string) error { return nil })

	_, err := f.bytes()

	require.ErrorContains(t, err, "read db")
}
