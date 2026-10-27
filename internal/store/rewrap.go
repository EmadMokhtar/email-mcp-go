package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ReplaceWrappedKeys replaces the wrapped bytes and the master key id of ALL
// stored keys in one transaction. A change of master key uses it, so that the
// database goes from "every key wrapped by the old master key" to "every key
// wrapped by the new master key" in one step, and never holds a mix.
//
// The caller must give exactly the keys that are stored:
//   - tenantKeys holds every tenant key, each found by its tenant and version.
//     A key that appears twice counts once.
//   - serverWrapped is the new wrapped server key, or nil if there is no server
//     key. Then serverKEKID is not used.
//
// The call does not create keys, and it does not leave keys out. If a listed
// key is not stored, it returns ErrNotFound. After the updates it counts the
// stored keys. If the count is not what the caller gave (a key was added after
// the caller listed the keys, or the caller left one out), it returns an error
// that matches ErrConflict. A key that is not in the list would stay wrapped by
// the old master key, and the caller would not know. The error tells the caller
// to run the rotation again.
//
// The call is all or nothing. On any error it undoes every update. The tenant
// ids get the same check as in the other write methods, and the check runs
// before the transaction starts.
//
// The transaction takes the write lock when it starts (see newDB). No other
// program can add a key between the count and the commit. The transaction only
// runs a few short statements.
func (s *Store) ReplaceWrappedKeys(ctx context.Context, tenantKeys []TenantKeyRow, serverWrapped []byte, serverKEKID string) error {
	type keyID struct {
		tenant  string
		version int
	}
	distinct := make(map[keyID]struct{}, len(tenantKeys))
	for _, k := range tenantKeys {
		if err := validateID("tenant", string(k.Tenant)); err != nil {
			return err
		}
		distinct[keyID{tenant: string(k.Tenant), version: k.Version}] = struct{}{}
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
			return fmt.Errorf("store: replace wrapped keys: tenant key version %d: %w", k.Version, err)
		}
	}
	wantServerKeys := 0
	if serverWrapped != nil {
		wantServerKeys = 1
		result, err := tx.ExecContext(ctx,
			`UPDATE server_keys SET wrapped = ?, kek_id = ? WHERE id = 1`,
			serverWrapped, serverKEKID)
		if err != nil {
			return fmt.Errorf("store: replace wrapped keys: server key: %w", err)
		}
		if err := requireOneRow(result, "replace wrapped keys: server key"); err != nil {
			return fmt.Errorf("store: replace wrapped keys: server key: %w", err)
		}
	}

	// Every key that was updated is stored, so the counts can only be too high:
	// there are keys that the caller did not give.
	if err := requireKeyCount(ctx, tx, countTenantKeys, len(distinct), "tenant"); err != nil {
		return err
	}
	if err := requireKeyCount(ctx, tx, countServerKeys, wantServerKeys, "server"); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: replace wrapped keys: %w", err)
	}
	return nil
}

// The queries that ReplaceWrappedKeys uses to count the stored keys.
const (
	countTenantKeys = `SELECT COUNT(*) FROM tenant_keys`
	countServerKeys = `SELECT COUNT(*) FROM server_keys`
)

// requireKeyCount runs a count query and returns an error that matches
// ErrConflict if the result is not want. The kind ("tenant" or "server") is
// used in the message.
func requireKeyCount(ctx context.Context, tx *sql.Tx, query string, want int, kind string) error {
	var got int
	if err := tx.QueryRowContext(ctx, query).Scan(&got); err != nil {
		return fmt.Errorf("store: replace wrapped keys: count %s keys: %w", kind, err)
	}
	if got != want {
		return fmt.Errorf("store: replace wrapped keys: %w: the stored keys changed while the master key was being changed (%d %s keys stored, %d given); nothing was replaced, run the rotation again",
			ErrConflict, got, kind, want)
	}
	return nil
}
