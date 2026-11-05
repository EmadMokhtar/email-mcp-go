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
// and the hashes. See migrations 0008 and 0012.
type WebSessionRow struct {
	// ID is the number of the row. It is not a secret: the Connections page
	// uses it to name one session. It is not the session id of the cookie.
	ID int64
	// Hash is the SHA-256 of the session id, 32 bytes. ListWebSessions leaves
	// it empty.
	Hash   []byte
	Tenant account.TenantID
	// CreatedAt, ExpiresAt and LastSeenAt are kept as whole milliseconds. The
	// session is valid while the time is before ExpiresAt, and the web package
	// also refuses a session that was not seen for too long.
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
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
//  4. It adds a session of the token's tenant with sessionHash, made and last
//     seen at now, valid until sessionExpiresAt.
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
		`INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		sessionHash, string(tenant), now.UnixMilli(), sessionExpiresAt.UnixMilli(), now.UnixMilli()); err != nil {
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
		row                    WebSessionRow
		created, expires, seen int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, hash, tenant_id, created_at, expires_at, last_seen_at FROM web_sessions WHERE hash = ?`, hash).
		Scan(&row.ID, &row.Hash, &row.Tenant, &created, &expires, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get web session: %w", err)
	}
	row.CreatedAt = time.UnixMilli(created).UTC()
	row.ExpiresAt = time.UnixMilli(expires).UTC()
	row.LastSeenAt = time.UnixMilli(seen).UTC()
	return &row, nil
}

// ListWebSessions returns the sessions of the tenant that still work: those
// that expire after now and were last seen after idleSince. The session used
// last comes first. The rows have no Hash, so a page cannot show it.
func (s *Store) ListWebSessions(ctx context.Context, tenant account.TenantID, now, idleSince time.Time) ([]WebSessionRow, error) {
	if err := validateID("tenant", string(tenant)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, created_at, expires_at, last_seen_at FROM web_sessions
		 WHERE tenant_id = ? AND expires_at > ? AND last_seen_at > ?
		 ORDER BY last_seen_at DESC, id DESC`,
		string(tenant), now.UnixMilli(), idleSince.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("store: list web sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var list []WebSessionRow
	for rows.Next() {
		var (
			row                    WebSessionRow
			created, expires, seen int64
		)
		if err := rows.Scan(&row.ID, &row.Tenant, &created, &expires, &seen); err != nil {
			return nil, fmt.Errorf("store: list web sessions: %w", err)
		}
		row.CreatedAt = time.UnixMilli(created).UTC()
		row.ExpiresAt = time.UnixMilli(expires).UTC()
		row.LastSeenAt = time.UnixMilli(seen).UTC()
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list web sessions: %w", err)
	}
	return list, nil
}

// TouchWebSession records that the session of the tenant was used at seenAt.
// The time never moves back. A session that no longer exists is not an error.
func (s *Store) TouchWebSession(ctx context.Context, tenant account.TenantID, hash []byte, seenAt time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE web_sessions SET last_seen_at = ? WHERE tenant_id = ? AND hash = ? AND last_seen_at < ?`,
		seenAt.UnixMilli(), string(tenant), hash, seenAt.UnixMilli()); err != nil {
		return fmt.Errorf("store: touch web session: %w", err)
	}
	return nil
}

// DeleteWebSession deletes the session with this hash. Deleting a session that
// does not exist is not an error.
func (s *Store) DeleteWebSession(ctx context.Context, hash []byte) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE hash = ?`, hash); err != nil {
		return fmt.Errorf("store: delete web session: %w", err)
	}
	return nil
}

// DeleteWebSessionByID deletes one session of the tenant, by the number of its
// row. It returns ErrNotFound when the tenant has no session with this number.
func (s *Store) DeleteWebSessionByID(ctx context.Context, tenant account.TenantID, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE tenant_id = ? AND id = ?`, string(tenant), id)
	if err != nil {
		return fmt.Errorf("store: delete web session: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete web session: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeWebSessions closes every way into the web interface of the tenant, in
// one transaction: it deletes all its sessions and all its unused setup tokens.
// So a revoke never leaves a working sign-in link, and a failed revoke changes
// nothing.
//
// It returns how many sessions still worked: those that expire after now and
// were last seen after idleSince, the same rule as ListWebSessions. The other
// sessions are deleted first, so the second delete counts exactly the sessions
// that worked.
func (s *Store) RevokeWebSessions(ctx context.Context, tenant account.TenantID, now, idleSince time.Time) (int64, error) {
	if err := validateID("tenant", string(tenant)); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: revoke web sessions: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM web_sessions WHERE tenant_id = ? AND (expires_at <= ? OR last_seen_at <= ?)`,
		string(tenant), now.UnixMilli(), idleSince.UnixMilli()); err != nil {
		return 0, fmt.Errorf("store: revoke web sessions: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM web_sessions WHERE tenant_id = ?`, string(tenant))
	if err != nil {
		return 0, fmt.Errorf("store: revoke web sessions: %w", err)
	}
	worked, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: revoke web sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_setup_tokens WHERE tenant_id = ?`, string(tenant)); err != nil {
		return 0, fmt.Errorf("store: revoke web sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: revoke web sessions: %w", err)
	}
	return worked, nil
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
