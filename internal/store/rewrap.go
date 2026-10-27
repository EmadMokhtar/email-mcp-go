package store

import (
	"context"
	"fmt"
)

// ReplaceWrappedKeys replaces the wrapped bytes and the master key id of many
// existing keys in one transaction. A change of master key uses it, so that the
// database never holds keys wrapped by two different master keys.
//
// Every row in tenantKeys is found by its tenant and version. If serverWrapped
// is not nil, the server key is replaced too, with serverWrapped and
// serverKEKID. If serverWrapped is nil, the server key is not touched.
//
// The call is all or nothing. If any row does not exist, it undoes every
// change and returns ErrNotFound. The same is true for any other database
// error. The tenant ids get the same check as in the other write methods, and
// the check runs before the transaction starts.
//
// The call does not create keys. The transaction only runs the updates, so it
// stays short.
func (s *Store) ReplaceWrappedKeys(ctx context.Context, tenantKeys []TenantKeyRow, serverWrapped []byte, serverKEKID string) error {
	for _, k := range tenantKeys {
		if err := validateID("tenant", string(k.Tenant)); err != nil {
			return err
		}
	}
	if len(tenantKeys) == 0 && serverWrapped == nil {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: replace wrapped keys: %w", err)
	}
	// Rollback after a successful Commit does nothing. On every error return
	// below it undoes the updates that already ran.
	defer func() { _ = tx.Rollback() }()

	for _, k := range tenantKeys {
		result, err := tx.ExecContext(ctx,
			`UPDATE tenant_keys SET wrapped = ?, kek_id = ? WHERE tenant_id = ? AND version = ?`,
			k.Wrapped, k.KEKID, string(k.Tenant), k.Version)
		if err != nil {
			return fmt.Errorf("store: replace wrapped keys: tenant key version %d: %w", k.Version, err)
		}
		if err := requireOneRow(result, "replace wrapped keys: tenant key"); err != nil {
			return err
		}
	}
	if serverWrapped != nil {
		result, err := tx.ExecContext(ctx,
			`UPDATE server_keys SET wrapped = ?, kek_id = ? WHERE id = 1`,
			serverWrapped, serverKEKID)
		if err != nil {
			return fmt.Errorf("store: replace wrapped keys: server key: %w", err)
		}
		if err := requireOneRow(result, "replace wrapped keys: server key"); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: replace wrapped keys: %w", err)
	}
	return nil
}
