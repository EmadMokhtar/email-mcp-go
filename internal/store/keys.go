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

// PutTenantKey creates a wrapped tenant key. It only creates: it never replaces
// a key. Versions start at 1.
//
// If the tenant already has a key with this version, PutTenantKey returns
// ErrExists and leaves the stored key as it is. This matters when two programs
// start together. Both can see no key and make one, and the second must not
// overwrite the first, because secrets sealed with the first key could then
// never be opened. The program that gets ErrExists reads the stored key with
// TenantKey. To replace the wrapped bytes of an existing key, use
// RewrapTenantKey.
//
// It fails if the tenant does not exist.
func (s *Store) PutTenantKey(ctx context.Context, tenant account.TenantID, version int, wrapped []byte, kekID string) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	if version < 1 {
		return errors.New("store: tenant key version must be at least 1")
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_keys (tenant_id, version, wrapped, kek_id, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, version) DO NOTHING`,
		string(tenant), version, wrapped, kekID, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put tenant key: %w", err)
	}
	return requireInserted(result, "put tenant key")
}

// RewrapTenantKey replaces the wrapped bytes and the master key id of an
// existing tenant key. A change of master key uses it to wrap every tenant key
// again. It does not create a key. It returns ErrNotFound if the tenant has no
// key with this version. To create a key, use PutTenantKey.
func (s *Store) RewrapTenantKey(ctx context.Context, tenant account.TenantID, version int, wrapped []byte, kekID string) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE tenant_keys SET wrapped = ?, kek_id = ? WHERE tenant_id = ? AND version = ?`,
		wrapped, kekID, string(tenant), version)
	if err != nil {
		return fmt.Errorf("store: rewrap tenant key: %w", err)
	}
	return requireOneRow(result, "rewrap tenant key")
}

// requireInserted returns ErrExists if an insert with ON CONFLICT DO NOTHING
// added no row, because the row was already there.
func requireInserted(result sql.Result, action string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: %w", action, err)
	}
	if n == 0 {
		return ErrExists
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

// PutServerKey creates the wrapped server key. It only creates: it never
// replaces the key.
//
// If a server key exists already, PutServerKey returns ErrExists and leaves the
// stored key as it is. The reason is the same as for PutTenantKey: two programs
// that start together must not overwrite each other's key. The program that
// gets ErrExists reads the stored key with ServerKey. To replace the wrapped
// bytes of the existing key, use RewrapServerKey.
func (s *Store) PutServerKey(ctx context.Context, wrapped []byte, kekID string) error {
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO server_keys (id, wrapped, kek_id, created_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		wrapped, kekID, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put server key: %w", err)
	}
	return requireInserted(result, "put server key")
}

// RewrapServerKey replaces the wrapped bytes and the master key id of the
// existing server key. A change of master key uses it to wrap the key again. It
// does not create a key. It returns ErrNotFound if there is no server key. To
// create the key, use PutServerKey.
func (s *Store) RewrapServerKey(ctx context.Context, wrapped []byte, kekID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE server_keys SET wrapped = ?, kek_id = ? WHERE id = 1`,
		wrapped, kekID)
	if err != nil {
		return fmt.Errorf("store: rewrap server key: %w", err)
	}
	return requireOneRow(result, "rewrap server key")
}
