package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// TenantKeyRow is one stored tenant key. The key is wrapped (encrypted) with the
// master key. KEKID names the master key (the key encryption key) that wrapped
// it.
type TenantKeyRow struct {
	Tenant  account.TenantID
	Version int
	Wrapped []byte
	KEKID   string
}

// PutTenantKey saves a wrapped tenant key. Versions start at 1. A put for a
// version that exists replaces it. A change of master key does this when it
// wraps every tenant key again.
//
// It fails if the tenant does not exist.
func (s *Store) PutTenantKey(ctx context.Context, tenant account.TenantID, version int, wrapped []byte, kekID string) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	if version < 1 {
		return errors.New("store: tenant key version must be at least 1")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_keys (tenant_id, version, wrapped, kek_id, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, version) DO UPDATE SET
			wrapped = excluded.wrapped,
			kek_id = excluded.kek_id`,
		string(tenant), version, wrapped, kekID, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put tenant key: %w", err)
	}
	return nil
}

// TenantKey returns the wrapped key of one version and the id of the master key
// that wrapped it. It returns ErrNotFound if the tenant has no such version.
func (s *Store) TenantKey(ctx context.Context, tenant account.TenantID, version int) (wrapped []byte, kekID string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT wrapped, kek_id FROM tenant_keys WHERE tenant_id = ? AND version = ?`,
		string(tenant), version).Scan(&wrapped, &kekID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("store: get tenant key: %w", err)
	}
	return wrapped, kekID, nil
}

// LatestTenantKeyVersion returns the highest key version of a tenant, or 0 if
// the tenant has no key.
func (s *Store) LatestTenantKeyVersion(ctx context.Context, tenant account.TenantID) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM tenant_keys WHERE tenant_id = ?`,
		string(tenant)).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: latest tenant key version: %w", err)
	}
	return version, nil
}

// ListTenantKeys returns the keys of all tenants, ordered by tenant and then by
// version. It is not limited to one tenant, because a change of master key must
// wrap every tenant key again.
func (s *Store) ListTenantKeys(ctx context.Context) ([]TenantKeyRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tenant_id, version, wrapped, kek_id FROM tenant_keys ORDER BY tenant_id, version`)
	if err != nil {
		return nil, fmt.Errorf("store: list tenant keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var keys []TenantKeyRow
	for rows.Next() {
		var k TenantKeyRow
		if err := rows.Scan(&k.Tenant, &k.Version, &k.Wrapped, &k.KEKID); err != nil {
			return nil, fmt.Errorf("store: list tenant keys: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tenant keys: %w", err)
	}
	return keys, nil
}

// ServerKey returns the wrapped server key and the id of the master key that
// wrapped it. It returns ErrNotFound if no server key was saved yet.
func (s *Store) ServerKey(ctx context.Context) (wrapped []byte, kekID string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT wrapped, kek_id FROM server_keys WHERE id = 1`).Scan(&wrapped, &kekID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("store: get server key: %w", err)
	}
	return wrapped, kekID, nil
}

// PutServerKey saves the wrapped server key. It replaces the key that was saved
// before, if there is one.
func (s *Store) PutServerKey(ctx context.Context, wrapped []byte, kekID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_keys (id, wrapped, kek_id, created_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			wrapped = excluded.wrapped,
			kek_id = excluded.kek_id`,
		wrapped, kekID, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put server key: %w", err)
	}
	return nil
}
