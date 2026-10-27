package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// newRawDB opens an empty database that has no migrations applied.
func newRawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := newDB(filepath.Join(t.TempDir(), "raw.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// recordedVersions returns the versions in schema_migrations, lowest first.
func recordedVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var versions []int
	for rows.Next() {
		var v int
		require.NoError(t, rows.Scan(&v))
		versions = append(versions, v)
	}
	require.NoError(t, rows.Err())
	return versions
}

// tableCount returns the number of tables with the given name.
func tableCount(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n))
	return n
}

func file(sqlText string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(sqlText)}
}

func TestMigrateRunsFilesInVersionOrder(t *testing.T) {
	db := newRawDB(t)
	// The second file needs the table from the first. The map has no order,
	// so only sorting by version can make this pass.
	fsys := fstest.MapFS{
		"0002_add_row.sql":    file("INSERT INTO t (x) VALUES (42);"),
		"0001_make_table.sql": file("CREATE TABLE t (x INTEGER);"),
		"0003_more.sql":       file("INSERT INTO t (x) VALUES (43); INSERT INTO t (x) VALUES (44);"),
	}

	require.NoError(t, migrate(context.Background(), db, fsys))

	require.Equal(t, []int{1, 2, 3}, recordedVersions(t, db))
	var rows int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM t").Scan(&rows))
	require.Equal(t, 3, rows)
}

func TestMigrateSkipsAppliedFiles(t *testing.T) {
	db := newRawDB(t)
	ctx := context.Background()
	// Running this file twice would fail, because the table would exist.
	first := fstest.MapFS{"0001_make_table.sql": file("CREATE TABLE t (x INTEGER);")}
	require.NoError(t, migrate(ctx, db, first))

	both := fstest.MapFS{
		"0001_make_table.sql": file("CREATE TABLE t (x INTEGER);"),
		"0002_make_other.sql": file("CREATE TABLE u (x INTEGER);"),
	}
	require.NoError(t, migrate(ctx, db, both))
	require.NoError(t, migrate(ctx, db, both))

	require.Equal(t, []int{1, 2}, recordedVersions(t, db))
	require.Equal(t, 1, tableCount(t, db, "u"))
}

func TestMigrateRollsBackAFailedFile(t *testing.T) {
	db := newRawDB(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_make_table.sql": file("CREATE TABLE t (x INTEGER);"),
		// The first statement works and the second fails. Neither may stay.
		"0002_broken.sql": file("CREATE TABLE u (x INTEGER); INSERT INTO missing_table VALUES (1);"),
		"0003_after.sql":  file("CREATE TABLE v (x INTEGER);"),
	}

	err := migrate(ctx, db, fsys)

	require.ErrorContains(t, err, "0002_broken.sql")
	require.Equal(t, []int{1}, recordedVersions(t, db))
	require.Zero(t, tableCount(t, db, "u"))
	// Later files do not run after a failure.
	require.Zero(t, tableCount(t, db, "v"))
}

func TestMigrateRejectsBadFileNames(t *testing.T) {
	names := map[string]string{
		"no number":         "init.sql",
		"no name":           "0001.sql",
		"three digits":      "001_init.sql",
		"five digits":       "00001_init.sql",
		"letters in number": "00a1_init.sql",
		"version zero":      "0000_init.sql",
		"wrong extension":   "0001_init.txt",
		"upper case":        "0001_Init.sql",
		"space in name":     "0001_my init.sql",
	}
	for label, name := range names {
		t.Run(label, func(t *testing.T) {
			db := newRawDB(t)
			fsys := fstest.MapFS{
				"0001_valid.sql": file("CREATE TABLE t (x INTEGER);"),
				name:             file("CREATE TABLE u (x INTEGER);"),
			}
			if name == "0001_valid.sql" {
				t.Fatal("test name clashes with the valid file")
			}

			err := migrate(context.Background(), db, fsys)

			require.ErrorContains(t, err, name)
			// A bad name is found before any file runs.
			require.Zero(t, tableCount(t, db, "t"))
		})
	}
}

func TestMigrateRejectsDuplicateVersion(t *testing.T) {
	db := newRawDB(t)
	fsys := fstest.MapFS{
		"0001_first.sql":  file("CREATE TABLE t (x INTEGER);"),
		"0001_second.sql": file("CREATE TABLE u (x INTEGER);"),
	}

	err := migrate(context.Background(), db, fsys)

	require.ErrorContains(t, err, "version 1")
	require.Zero(t, tableCount(t, db, "t"))
	require.Zero(t, tableCount(t, db, "u"))
}

func TestMigrateRejectsDirectory(t *testing.T) {
	db := newRawDB(t)
	fsys := fstest.MapFS{"0001_dir.sql/inner.sql": file("CREATE TABLE t (x INTEGER);")}

	require.Error(t, migrate(context.Background(), db, fsys))
}

func TestMigrateConcurrentlyRunsEachFileOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared.db")
	// Each file fails if it runs twice, because the table would exist.
	fsys := fstest.MapFS{
		"0001_make_a.sql": file("CREATE TABLE a (x INTEGER);"),
		"0002_make_b.sql": file("CREATE TABLE b (x INTEGER);"),
		"0003_make_c.sql": file("CREATE TABLE c (x INTEGER);"),
	}

	// Each worker has its own handle to the same file, like separate programs.
	const workers = 6
	dbs := make([]*sql.DB, workers)
	for i := range dbs {
		db, err := newDB(path)
		require.NoError(t, err)
		require.NoError(t, connect(ctx, db))
		dbs[i] = db
		t.Cleanup(func() { require.NoError(t, db.Close()) })
	}

	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i, db := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = migrate(ctx, db, fsys)
		}()
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, []int{1, 2, 3}, recordedVersions(t, dbs[0]))
}
