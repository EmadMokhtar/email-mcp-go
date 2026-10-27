package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
)

// migrationName matches the name of a migration file: four digits, an
// underscore, a short name and ".sql". The digits are the version.
var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

// migration is one SQL file.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads every file in fsys and returns them sorted by version.
// It fails if any entry is not a file with a valid name, or if two files have
// the same version.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var migrations []migration
	seen := make(map[int]string)
	for _, entry := range entries {
		name := entry.Name()
		match := migrationName.FindStringSubmatch(name)
		if match == nil || entry.IsDir() {
			return nil, fmt.Errorf("store: migration file name %q must look like 0001_name.sql", name)
		}
		// The pattern allows only four digits, so Atoi cannot fail.
		version, _ := strconv.Atoi(match[1])
		if version == 0 {
			return nil, fmt.Errorf("store: migration %s: the version must be at least 1", name)
		}
		if other, ok := seen[version]; ok {
			return nil, fmt.Errorf("store: migrations %s and %s both have version %d", other, name, version)
		}
		seen[version] = name

		text, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", name, err)
		}
		migrations = append(migrations, migration{version: version, name: name, sql: string(text)})
	}
	slices.SortFunc(migrations, func(a, b migration) int { return a.version - b.version })
	return migrations, nil
}

// migrate runs the files in fsys that have not run yet, in order of version.
//
// Each file runs in its own transaction, together with the row that records its
// version. If a file fails, the transaction rolls back, so the database is
// left as it was before that file. Later files do not run.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	migrations, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	// The first file also creates this table. The program must read the table
	// before it runs the first file, so it creates the table here too.
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
	}
	return nil
}

// appliedVersions returns the set of versions that have run.
func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one file and records its version in one transaction.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	// Another program may have run this file after the check in migrate. The
	// transaction holds the write lock now, so this check is final.
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
		return err
	}
	return tx.Commit()
}
