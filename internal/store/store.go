// Package store keeps all data in one SQLite database. It is the only package
// that talks to the database.
//
// Every query on tenant data has a "tenant_id = ?" condition. A caller can
// never read or change the data of another tenant by guessing an id.
//
// Each task that needs new tables adds a migration file in the migrations
// directory and its methods in a new file in this package.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	// The driver is pure Go, so the program builds with CGO_ENABLED=0.
	_ "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DBFileName is the name of the database file inside the data directory. SQLite
// keeps its write-ahead log next to it, in DBFileName+"-wal" and
// DBFileName+"-shm".
const DBFileName = "email-mcp.db"

// ErrNotFound means that no row matched. It is also returned when the row
// exists for another tenant.
var ErrNotFound = errors.New("not found")

// ErrExists means that an insert-only call found the row already there and left
// it as it was. See PutTenantKey and PutServerKey.
var ErrExists = errors.New("already exists")

// ErrConflict means that the keys in the database are not the keys that the
// caller expected. ReplaceWrappedKeys returns it when the set of stored keys
// changed after the caller listed them. The call changed nothing, and the
// caller should start again.
var ErrConflict = errors.New("conflict")

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is a handle to the database. It is safe for use by many goroutines.
type Store struct {
	db *sql.DB
}

// Open opens the database in dir and runs the migrations that have not run yet.
//
// If dir does not exist, Open creates it (and any missing parents) with mode
// 0700. An existing directory is left as it is. Open creates the database file
// with mode 0600. SQLite gives its WAL and shared-memory files the same mode as
// the database file.
func Open(ctx context.Context, dir string) (*Store, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, DBFileName)
	if err := createPrivateFile(path); err != nil {
		return nil, err
	}

	db, err := newDB(path)
	if err != nil {
		return nil, err
	}
	// Connect now, so a problem with the file shows here and not on first use.
	if err := connect(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open database: %w", err)
	}
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: embedded migrations: %w", err)
	}
	if err := migrate(ctx, db, migrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// connectAttempts and connectDelay set how long connect tries again when SQLite
// reports that the database is busy.
const (
	connectAttempts = 40
	connectDelay    = 25 * time.Millisecond
)

// connect makes the first connection to the database.
//
// The first connection to a new file switches it to WAL mode. If two programs
// do this at the same moment, SQLite can fail one of them at once with
// SQLITE_BUSY and not wait for the busy timeout. The failed program tries
// again. By then the file is in WAL mode, and the switch does nothing.
func connect(ctx context.Context, db *sql.DB) error {
	var err error
	for attempt := 0; attempt < connectAttempts; attempt++ {
		if err = db.PingContext(ctx); err == nil || !IsBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(connectDelay):
		}
	}
	return err
}

// IsBusy reports whether err says that SQLite could not get a lock: "database is
// locked" (SQLITE_BUSY, with its extended codes) or "database table is locked"
// (SQLITE_LOCKED). The statement did not run, so a caller may try it again after
// a short wait. SQLite has several extended codes for each. The low 8 bits are
// the main code.
//
// It looks for any error in the chain that has a Code method, as the error of the
// SQLite driver has. Every database error of this package wraps that error.
func IsBusy(err error) bool {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return false
	}
	code := coded.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// CheckpointMode is the mode of CheckpointWAL.
type CheckpointMode string

// The modes of CheckpointWAL.
const (
	// CheckpointPassive copies as much of the log as it can into the database
	// file without waiting for other connections, and does not cut the log
	// file. It never holds up another program.
	CheckpointPassive CheckpointMode = "PASSIVE"
	// CheckpointTruncate waits (up to the busy timeout) until no other
	// connection reads or writes, copies the whole log, and cuts the log file to
	// zero bytes. Other programs cannot write while it runs.
	CheckpointTruncate CheckpointMode = "TRUNCATE"
)

// CheckpointWAL copies the write-ahead log into the database file (PRAGMA
// wal_checkpoint) in the given mode. See CheckpointMode.
//
// secure_delete overwrites deleted content in the database file, but not in old
// frames of the log. A deleted message body can stay in the log until the log
// is cut with CheckpointTruncate. Run that after a large delete, such as a
// prune.
//
// complete is true when every frame of the log was copied (and, for
// CheckpointTruncate, the log was cut). It is false, with a nil error, when
// another connection (also of another program) was in the way: the next run
// tries again.
func (s *Store) CheckpointWAL(ctx context.Context, mode CheckpointMode) (complete bool, err error) {
	if mode != CheckpointPassive && mode != CheckpointTruncate {
		return false, fmt.Errorf("store: unknown checkpoint mode %q", mode)
	}
	var busy, logFrames, checkpointed int
	// The mode is one of the two fixed words above, never input.
	err = s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(`+string(mode)+`)`).Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return false, fmt.Errorf("store: checkpoint the write-ahead log: %w", err)
	}
	return busy == 0 && checkpointed == logFrames, nil
}

// ensureDir creates dir with mode 0700 if it does not exist. It leaves an
// existing directory as it is, so it never changes a mode the user chose.
func ensureDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("store: %s is not a directory", dir)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("store: create data directory: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("store: check data directory: %w", err)
	}
}

// createPrivateFile makes sure the file at path exists. A new file gets mode
// 0600. SQLite would use mode 0644, so the file must exist before SQLite
// opens it. An existing file is not changed.
func createPrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: the path is the operator's data directory plus a fixed name
	if err != nil {
		return fmt.Errorf("store: create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: create database file: %w", err)
	}
	return nil
}

// newDB returns a handle for the database file at path. It does not run
// migrations.
//
// The settings are in the DSN (data source name, the connection string), so
// that every connection in the pool gets them:
//   - journal_mode(WAL): readers do not block the writer.
//   - busy_timeout(5000): wait up to 5 seconds for a lock, then fail.
//   - foreign_keys(1): SQLite does not check foreign keys unless this is set.
//   - secure_delete(1): SQLite overwrites deleted content with zeros. A cleared
//     message body then does not stay in a free page of the file.
//   - synchronous(FULL): SQLite flushes the write-ahead log to disk at every
//     commit. This is the default, and it is set here on purpose: sending a
//     message exactly once, also after a power loss, depends on it.
//   - _txlock=immediate: a transaction takes the write lock when it starts. A
//     transaction that reads first and writes later could fail at once with
//     "database is locked", and the busy timeout would not help.
func newDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve database path: %w", err)
	}
	// The name is a file: URI, so url.URL escapes "?", "#" and "%" in the path.
	// A path on Windows, like C:/data, needs a slash in front.
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{
		"_pragma": {
			"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(1)",
			"secure_delete(1)", "synchronous(FULL)",
		},
		"_txlock": {"immediate"},
	}
	dsn := url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}

	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("store: open database: %w", err)
	}
	return db, nil
}
