package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// APITokenRow is one access token of the local HTTP server. The store keeps only
// the hash of a token, never the token. The tokens package makes the tokens and
// the hashes, and checks the scope names. See migration 0007.
type APITokenRow struct {
	ID     string
	Tenant account.TenantID
	// Name is a label that the user chose.
	Name string
	// Hash is the SHA-256 of the token, 32 bytes. Only APITokenByHash returns it.
	// ListAPITokens leaves it nil.
	Hash []byte
	// Scopes is never empty.
	Scopes []string
	// CreatedAt and RevokedAt are kept as whole milliseconds. RevokedAt is zero
	// while the token is valid.
	CreatedAt time.Time
	RevokedAt time.Time
}

// apiTokenColumns lists the columns that scanAPIToken reads, without the hash.
const apiTokenColumns = `id, tenant_id, name, scopes, created_at, revoked_at` //nolint:gosec // G101: column names, not a credential

// CreateAPIToken adds a token. RevokedAt of the row is ignored: a new token is
// valid.
//
// It fails if the tenant does not exist, if the tenant already has a token with
// this id, if any token has the same hash, or if a value breaks a rule of the
// table (see migration 0007).
func (s *Store) CreateAPIToken(ctx context.Context, row *APITokenRow) error {
	if err := validateID("tenant", string(row.Tenant)); err != nil {
		return err
	}
	if err := validateID("token", row.ID); err != nil {
		return err
	}
	if len(row.Scopes) == 0 {
		return errors.New("store: create api token: a token needs at least one scope")
	}
	scopes, err := json.Marshal(row.Scopes)
	if err != nil {
		return fmt.Errorf("store: create api token: encode scopes: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (tenant_id, id, name, hash, scopes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		string(row.Tenant), row.ID, row.Name, row.Hash, string(scopes), row.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: create api token: %w", err)
	}
	return nil
}

// APITokenByHash returns the token with this hash, revoked or not. It returns
// ErrNotFound when no token has the hash.
//
// It is the one query on tenant data without a tenant condition, on purpose:
// the caller has only the token, and the row says which tenant the token
// belongs to. A hash is 256 bits, so it cannot be guessed.
func (s *Store) APITokenByHash(ctx context.Context, hash []byte) (*APITokenRow, error) {
	row, err := scanAPIToken(s.db.QueryRowContext(ctx,
		`SELECT `+apiTokenColumns+`, hash FROM api_tokens WHERE hash = ?`, hash), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get api token: %w", err)
	}
	return row, nil
}

// ListAPITokens returns the tokens of a tenant, revoked ones too, oldest first.
// Tokens with the same time are ordered by id. The rows have no hash.
func (s *Store) ListAPITokens(ctx context.Context, tenant account.TenantID) ([]*APITokenRow, error) {
	if err := validateID("tenant", string(tenant)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+apiTokenColumns+` FROM api_tokens WHERE tenant_id = ? ORDER BY created_at, id`, string(tenant))
	if err != nil {
		return nil, fmt.Errorf("store: list api tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var list []*APITokenRow
	for rows.Next() {
		row, err := scanAPIToken(rows, false)
		if err != nil {
			return nil, fmt.Errorf("store: list api tokens: %w", err)
		}
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list api tokens: %w", err)
	}
	return list, nil
}

// RevokeAPIToken marks the token of the tenant as revoked at the given time. A
// token that is already revoked keeps its first time, and the call succeeds. It
// returns ErrNotFound when the tenant has no token with this id, also when
// another tenant has one.
//
// The time is never stored as earlier than the creation time, so a clock that
// went back cannot make the revoke fail.
func (s *Store) RevokeAPIToken(ctx context.Context, tenant account.TenantID, id string, at time.Time) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, MAX(created_at, ?))
		WHERE tenant_id = ? AND id = ?`,
		at.UnixMilli(), string(tenant), id)
	if err != nil {
		return fmt.Errorf("store: revoke api token: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: revoke api token: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanAPIToken reads one row with the columns of apiTokenColumns, and the hash
// after them when withHash is true. Times are returned in UTC.
func scanAPIToken(row rowScanner, withHash bool) (*APITokenRow, error) {
	var (
		t         APITokenRow
		scopes    string
		created   int64
		revokedAt sql.NullInt64
	)
	dest := []any{&t.ID, &t.Tenant, &t.Name, &scopes, &created, &revokedAt}
	if withHash {
		dest = append(dest, &t.Hash)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopes), &t.Scopes); err != nil {
		return nil, fmt.Errorf("decode scopes: %w", err)
	}
	t.CreatedAt = time.UnixMilli(created).UTC()
	if revokedAt.Valid {
		t.RevokedAt = time.UnixMilli(revokedAt.Int64).UTC()
	}
	return &t, nil
}
