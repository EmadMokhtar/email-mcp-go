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
// database file in memory. Seed writes a copy of it into the data directory of
// a test. store.Open then finds every migration done and runs none. The copy
// has the same bytes as a file that store.Open migrated itself, because the
// migrations only create tables and indexes and add no rows.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// template is the migrated database file, made once in each test binary.
var template struct {
	once sync.Once
	data []byte
	err  error
}

// Seed writes a migrated database file into dir, so that store.Open(ctx, dir)
// runs no migration. It creates dir with mode 0700 when dir does not exist, and
// the file with mode 0600, as store.Open does.
//
// Seed does nothing when dir already has a database file. So a test can call
// it each time it opens the same directory.
func Seed(t testing.TB, dir string) {
	t.Helper()
	data, err := migrated()
	if err != nil {
		t.Fatalf("storetest: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("storetest: create the data directory: %v", err)
	}
	path := filepath.Join(dir, store.DBFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the path is a test directory plus a fixed name
	if errors.Is(err, fs.ErrExist) {
		return
	}
	if err != nil {
		t.Fatalf("storetest: create the database file: %v", err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("storetest: write the database file: %v", err)
	}
}

// migrated returns the bytes of a database file that has every migration. The
// first call makes the file, and later calls return the same bytes.
func migrated() ([]byte, error) {
	template.once.Do(func() {
		template.data, template.err = makeTemplate()
	})
	return template.data, template.err
}

// makeTemplate migrates a new database in a temporary directory and returns
// the bytes of its database file.
func makeTemplate() (data []byte, err error) {
	dir, err := os.MkdirTemp("", "storetest-")
	if err != nil {
		return nil, fmt.Errorf("create a temporary directory: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(dir); err == nil && rerr != nil {
			err = fmt.Errorf("remove the temporary directory: %w", rerr)
		}
	}()

	ctx := context.Background()
	s, err := store.Open(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate the template database: %w", err)
	}
	// The migrations are in the write-ahead log. The checkpoint copies them
	// into the database file and cuts the log to zero bytes, so the database
	// file alone has every table.
	complete, err := s.CheckpointWAL(ctx, store.CheckpointTruncate)
	if cerr := s.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close the template database: %w", cerr)
	}
	if err != nil {
		return nil, fmt.Errorf("checkpoint the template database: %w", err)
	}
	if !complete {
		return nil, errors.New("the checkpoint of the template database did not copy the whole log")
	}
	data, err = os.ReadFile(filepath.Join(dir, store.DBFileName)) //nolint:gosec // G304: the path is a temporary directory plus a fixed name
	if err != nil {
		return nil, fmt.Errorf("read the template database: %w", err)
	}
	return data, nil
}
