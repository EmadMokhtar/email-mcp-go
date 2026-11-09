package storetest_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the "sqlite" driver, to read the files without the store

	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/storetest"
)

// A seeded directory must give the same database as a store that ran its own
// migrations. Otherwise the tests that use Seed test another database than the
// one of the program. The tests of internal/testutil/seedfile check the copy
// itself: the file modes, and that an existing file stays.
func TestSeedGivesTheDatabaseOfANewStore(t *testing.T) {
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

// settings are the pragmas that a migration or store.Open can change, and
// that stay in the database file.
var settings = []string{"application_id", "auto_vacuum", "encoding", "journal_mode", "page_size", "user_version"}

// describe returns the schema, every row of every table and the settings of
// the database in dir.
func describe(t *testing.T, dir string) []string {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(dir, store.DBFileName))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	var out []string
	var tables []string
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, coalesce(sql, '') FROM sqlite_master ORDER BY type, name`)
	require.NoError(t, err)
	for rows.Next() {
		var kind, name, table, def string
		require.NoError(t, rows.Scan(&kind, &name, &table, &def))
		out = append(out, kind+" "+name+" "+table+" "+def)
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.NotEmpty(t, tables)

	for _, table := range tables {
		out = append(out, tableRows(t, db, table)...)
	}
	for _, name := range settings {
		var value string
		require.NoError(t, db.QueryRowContext(ctx, "SELECT * FROM pragma_"+name).Scan(&value), name)
		out = append(out, name+" "+value)
	}
	return out
}

// tableRows returns every row of the table, in the order of its rows.
func tableRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT * FROM "`+table+`"`) //nolint:gosec // G202: the name comes from sqlite_master of a test database
	require.NoError(t, err, table)
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err)

	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		require.NoError(t, rows.Scan(pointers...))
		out = append(out, fmt.Sprintf("row %s %v", table, values))
	}
	require.NoError(t, rows.Err())
	return out
}
