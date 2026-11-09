// Package storetest gives tests a new database that already has every
// migration.
//
// store.Open runs every migration on a new database file. SQLite parses each
// statement, and after each change of the schema it parses the whole schema
// again. The SQLite of modernc.org/sqlite is Go code, so the race detector
// checks every memory access of that work. With -race one store.Open of a new
// file took about 170 ms, and without it about 11 ms. Hundreds of tests open a
// new database, so the migrations were up to half of the time of some test
// packages.
//
// This package runs the migrations once in each test binary and keeps the
// database file in memory (see internal/testutil/seedfile). Seed writes a copy
// of it into the data directory of a test. store.Open then finds every
// migration done and runs none. The migrations create tables and indexes, and
// their only rows are the versions in schema_migrations. So the copy has the
// same schema, rows and settings as a file that store.Open migrated itself.
// TestSeedGivesTheDatabaseOfANewStore checks that.
package storetest

import (
	"context"
	"testing"

	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/seedfile"
)

// migrated is the migrated database file. Closing the last connection copies
// the write-ahead log into the database file and removes the log; seedfile
// fails when the log stays. The tests of internal/store make the same file
// with the same lines, because they cannot import this package.
var migrated = seedfile.New(store.DBFileName, func(dir string) error {
	s, err := store.Open(context.Background(), dir)
	if err != nil {
		return err
	}
	return s.Close()
})

// Seed writes a migrated database file into dir, so that store.Open(ctx, dir)
// runs no migration. It creates dir with mode 0700 when dir does not exist, and
// the file with mode 0600, as store.Open does.
//
// Seed does nothing when dir already has a database file. So a test can call
// it each time it opens the same directory.
func Seed(t testing.TB, dir string) {
	t.Helper()
	migrated.Seed(t, dir)
}
