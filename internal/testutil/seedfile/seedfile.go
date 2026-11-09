// Package seedfile makes a file once in each test binary and writes a copy of
// it into the folder of each test that asks for it.
//
// It is for a file that is slow to make and the same for every test, such as
// a migrated database (see internal/testutil/storetest). This package does not
// import the code that makes the file. So the tests of that code can use it
// too: the tests of internal/store cannot import storetest, because storetest
// imports store.
package seedfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// File is a file that its build function makes once. Use New to make one.
type File struct {
	name  string
	build func(dir string) error

	once sync.Once
	data []byte
	err  error
}

// New returns a File with the file name name. The first Seed calls build with
// a new empty folder. build must leave the finished file in that folder, and
// no other file. A second file, such as the write-ahead log of SQLite, would
// mean that the file alone is not complete, so the first Seed fails then.
func New(name string, build func(dir string) error) *File {
	return &File{name: name, build: build}
}

// Seed writes a copy of the file into dir. It creates dir with mode 0700 when
// dir does not exist, and the file with mode 0600.
//
// Seed does nothing when dir already has a file of that name. So a test can
// call it each time it opens the same folder.
func (f *File) Seed(t testing.TB, dir string) {
	t.Helper()
	data, err := f.bytes()
	if err != nil {
		t.Fatalf("seedfile: %v", err)
	}
	if err := write(dir, f.name, data); err != nil {
		t.Fatalf("seedfile: %v", err)
	}
}

// bytes returns the content of the file. The first call makes the file, and
// later calls return the same bytes or the same error.
func (f *File) bytes() ([]byte, error) {
	f.once.Do(func() {
		f.data, f.err = f.make()
	})
	return f.data, f.err
}

// make runs build in a temporary folder and returns the content of the file.
func (f *File) make() (data []byte, err error) {
	dir, err := os.MkdirTemp("", "seedfile-")
	if err != nil {
		return nil, fmt.Errorf("create a temporary folder: %w", err)
	}
	defer func() {
		if rerr := os.RemoveAll(dir); err == nil && rerr != nil {
			err = fmt.Errorf("remove the temporary folder: %w", rerr)
		}
	}()

	if err := f.build(dir); err != nil {
		return nil, fmt.Errorf("make %s: %w", f.name, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the temporary folder: %w", err)
	}
	for _, e := range entries {
		if e.Name() != f.name {
			return nil, fmt.Errorf("making %s also left %s, so %s alone is not complete", f.name, e.Name(), f.name)
		}
	}
	data, err = os.ReadFile(filepath.Join(dir, f.name)) //nolint:gosec // G304: a temporary folder plus a fixed name
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.name, err)
	}
	return data, nil
}

// write creates dir/name with data, unless that file already exists.
func write(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the folder: %w", err)
	}
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a test folder plus a fixed name
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err = file.Write(data)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
