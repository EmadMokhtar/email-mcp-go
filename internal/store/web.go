package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// webHashSize is the size of the hash of a setup token or a session id: a
// SHA-256.
const webHashSize = 32

// WebSessionRow is one signed-in browser of the web interface. The store keeps
// only the hash of the session id, never the id. The web package makes the ids
// and the hashes. See migration 0008.
type WebSessionRow struct {
	// Hash is the SHA-256 of the session id, 32 bytes.
	Hash   []byte
	Tenant account.TenantID
	// CreatedAt and ExpiresAt are kept as whole milliseconds. The session is
	// valid while the time is before ExpiresAt.
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateWebSetupToken adds a setup token of the tenant, by its hash. In the
// same transaction it deletes the setup tokens that expired at createdAt or
// before, so old tokens do not stay.
//
// It fails if the hash is not 32 bytes, if any token has the same hash, if
// expiresAt is not after createdAt, or if the tenant does not exist.
func (s *Store) CreateWebSetupToken(ctx context.Context, tenant account.TenantID, hash []byte, createdAt, expiresAt time.Time) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	if err := checkWebRow(hash, createdAt, expiresAt); err != nil {
		return fmt.Errorf("store: create web setup token: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create web setup token: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM web_setup_tokens WHERE expires_at <= ?`, createdAt.UnixMilli()); err != nil {
		return fmt.Errorf("store: create web setup token: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO web_setup_tokens (hash, tenant_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash, string(tenant), createdAt.UnixMilli(), expiresAt.UnixMilli()); err != nil {
		return fmt.Errorf("store: create web setup token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create web setup token: %w", err)
	}
	return nil
}

// UseWebSetupToken swaps a setup token for a session, in one transaction:
//  1. It deletes the setup token with tokenHash. The delete is the check, so of
//     two callers with the same token, in this program or in another one that
//     uses the same database file, only one finds the row.
//  2. If the token expired at now or before, it keeps the delete and returns
//     ErrNotFound. No session is made.
//  3. It deletes the sessions that expired at now or before.
//  4. It adds a session of the token's tenant with sessionHash, made at now,
//     valid until sessionExpiresAt.
//
// It returns the tenant of the new session. It returns ErrNotFound when no
// token has the hash or the token expired. If any step fails, nothing changes,
// and the token can still be used.
//
// Like APITokenByHash, the lookup has no tenant condition, on purpose: the
// caller has only the token, and the row says which tenant it belongs to.
func (s *Store) UseWebSetupToken(ctx context.Context, tokenHash, sessionHash []byte, now, sessionExpiresAt time.Time) (account.TenantID, error) {
	if len(tokenHash) != webHashSize {
		return "", fmt.Errorf("store: use web setup token: the token hash must be %d bytes", webHashSize)
	}
	if err := checkWebRow(sessionHash, now, sessionExpiresAt); err != nil {
		return "", fmt.Errorf("store: use web setup token: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("store: use web setup token: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		tenant  account.TenantID
		expires int64
	)
	err = tx.QueryRowContext(ctx,
		`DELETE FROM web_setup_tokens WHERE hash = ? RETURNING tenant_id, expires_at`, tokenHash).
		Scan(&tenant, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: use web setup token: %w", err)
	}
	if now.UnixMilli() >= expires {
		// The expired token stays deleted.
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("store: use web setup token: %w", err)
		}
		return "", ErrNotFound
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM web_sessions WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return "", fmt.Errorf("store: use web setup token: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		sessionHash, string(tenant), now.UnixMilli(), sessionExpiresAt.UnixMilli()); err != nil {
		return "", fmt.Errorf("store: use web setup token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("store: use web setup token: %w", err)
	}
	return tenant, nil
}

// WebSessionByHash returns the session with this hash, expired or not: the
// caller compares ExpiresAt with its clock. It returns ErrNotFound when no
// session has the hash.
//
// Like APITokenByHash, the lookup has no tenant condition, on purpose: the
// caller has only the cookie, and the row says which tenant it belongs to.
func (s *Store) WebSessionByHash(ctx context.Context, hash []byte) (*WebSessionRow, error) {
	var (
		row              WebSessionRow
		created, expires int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT hash, tenant_id, created_at, expires_at FROM web_sessions WHERE hash = ?`, hash).
		Scan(&row.Hash, &row.Tenant, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get web session: %w", err)
	}
	row.CreatedAt = time.UnixMilli(created).UTC()
	row.ExpiresAt = time.UnixMilli(expires).UTC()
	return &row, nil
}

// DeleteWebSession deletes the session with this hash. Deleting a session that
// does not exist is not an error.
func (s *Store) DeleteWebSession(ctx context.Context, hash []byte) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE hash = ?`, hash); err != nil {
		return fmt.Errorf("store: delete web session: %w", err)
	}
	return nil
}

// checkWebRow checks the hash and the times of a new setup token or session.
// The message does not show the hash.
func checkWebRow(hash []byte, createdAt, expiresAt time.Time) error {
	if len(hash) != webHashSize {
		return fmt.Errorf("the hash must be %d bytes", webHashSize)
	}
	if expiresAt.UnixMilli() <= createdAt.UnixMilli() {
		return errors.New("the expiry must be after the creation time")
	}
	return nil
}
