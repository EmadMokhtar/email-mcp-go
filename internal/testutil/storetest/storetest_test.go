package storetest_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the "sqlite" driver, to read the files without the store

	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/storetest"
)

// A seeded directory must give the same database as a store that ran its own
// migrations. Otherwise the tests that use Seed test another schema than the
// one of the program.
func TestSeedGivesTheSchemaOfANewStore(t *testing.T) {
	ctx := context.Background()

	fresh := t.TempDir()
	s, err := store.Open(ctx, fresh)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	seeded := t.TempDir()
	storetest.Seed(t, seeded)
	s, err = store.Open(ctx, seeded)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	require.Equal(t, describe(t, fresh), describe(t, seeded))
}

func TestSeedMakesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "data")

	storetest.Seed(t, dir)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	info, err = os.Stat(filepath.Join(dir, store.DBFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// A test may open the same directory more than once. The second Seed must not
// replace the data of the first open.
func TestSeedLeavesAnExistingDatabaseAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, store.DBFileName)
	require.NoError(t, os.WriteFile(path, []byte("existing"), 0o600))

	storetest.Seed(t, dir)

	data, err := os.ReadFile(path) //nolint:gosec // G304: a test directory plus a fixed name
	require.NoError(t, err)
	require.Equal(t, "existing", string(data))
}

// describe returns the schema, the applied migrations and the stored settings
// of the database in dir.
func describe(t *testing.T, dir string) []string {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(dir, store.DBFileName))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	var out []string
	queries := []string{
		`SELECT type || ' ' || name || ' ' || tbl_name || ' ' || coalesce(sql, '') FROM sqlite_master ORDER BY type, name`,
		`SELECT 'migration ' || version FROM schema_migrations ORDER BY version`,
		`SELECT 'user_version ' || user_version FROM pragma_user_version`,
		`SELECT 'auto_vacuum ' || auto_vacuum FROM pragma_auto_vacuum`,
		`SELECT 'page_size ' || page_size FROM pragma_page_size`,
		`SELECT 'journal_mode ' || journal_mode FROM pragma_journal_mode`,
	}
	for _, q := range queries {
		rows, err := db.QueryContext(ctx, q)
		require.NoError(t, err, q)
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			out = append(out, line)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	require.NotEmpty(t, out)
	return out
}
