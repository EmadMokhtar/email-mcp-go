package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// pendingStateWaiting is the state of an action that waits for the user. The
// confirm package owns the state names. The store needs this one, because only
// a waiting action expires.
const pendingStateWaiting = "pending"

// PendingRow is one action that waits for the user to confirm it. The store
// does not look inside the sealed values or know what the states and kinds
// mean. The confirm package does.
//
// The payload is not part of the row, because it can be large and most reads do
// not need it. See CreatePending and GetPendingPayload.
type PendingRow struct {
	ID      string
	Tenant  account.TenantID
	Account account.AccountID
	Kind    string
	// Hash is the SHA-256 of the kind and the payload. It is 32 bytes.
	Hash  []byte
	State string
	// CreatedAt and ExpiresAt are kept as whole milliseconds.
	CreatedAt, ExpiresAt time.Time
	ResultID             string
	// Preview is sealed. PreviewKeyVersion says which tenant key sealed it.
	Preview           []byte
	PreviewKeyVersion int
}

// pendingColumns lists the columns of pending_actions that scanPending reads.
// The payload columns are not in the list.
const pendingColumns = `id, tenant_id, account_id, kind, hash, state, created_at, expires_at,
	result_id, preview, preview_key_version`

// CreatePending adds a pending action. The payload is sealed. The key version
// says which tenant key sealed it.
//
// It fails if the tenant has no account with this id, or if the tenant already
// has an action with the same id.
func (s *Store) CreatePending(ctx context.Context, row *PendingRow, payload []byte, payloadKeyVersion int) error {
	if err := validateAccountIDs(row.Tenant, row.Account); err != nil {
		return err
	}
	if err := validateID("pending action", row.ID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_actions (`+pendingColumns+`, payload, payload_key_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, string(row.Tenant), string(row.Account), row.Kind, row.Hash, row.State,
		row.CreatedAt.UnixMilli(), row.ExpiresAt.UnixMilli(), row.ResultID,
		row.Preview, row.PreviewKeyVersion, payload, payloadKeyVersion)
	if err != nil {
		return fmt.Errorf("store: create pending action: %w", err)
	}
	return nil
}

// GetPending returns one pending action of a tenant, without its payload. It
// returns ErrNotFound if the tenant has no action with this id.
//
// The row has the state that is stored. A waiting action whose time has passed
// still has the state "pending" here. The caller compares ExpiresAt with the
// time.
func (s *Store) GetPending(ctx context.Context, tenant account.TenantID, id string) (*PendingRow, error) {
	row, err := scanPending(s.db.QueryRowContext(ctx,
		`SELECT `+pendingColumns+` FROM pending_actions WHERE tenant_id = ? AND id = ?`,
		string(tenant), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pending action: %w", err)
	}
	return row, nil
}

// GetPendingPayload returns the sealed payload of a pending action and the
// version of the tenant key that sealed it. It returns ErrNotFound if the
// tenant has no action with this id.
func (s *Store) GetPendingPayload(ctx context.Context, tenant account.TenantID, id string) (keyVersion int, sealed []byte, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT payload_key_version, payload FROM pending_actions WHERE tenant_id = ? AND id = ?`,
		string(tenant), id).Scan(&keyVersion, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("store: get pending action payload: %w", err)
	}
	return keyVersion, sealed, nil
}

// ListPending returns the actions of a tenant that wait for the user and whose
// time has not passed at now. The newest comes first. Actions with the same
// creation time are ordered by id, largest first.
//
// An action is live up to and including its ExpiresAt. The comparison uses
// whole milliseconds, the same as the stored times.
func (s *Store) ListPending(ctx context.Context, tenant account.TenantID, now time.Time) ([]*PendingRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pendingColumns+` FROM pending_actions
		WHERE tenant_id = ? AND state = ? AND expires_at >= ?
		ORDER BY created_at DESC, id DESC`,
		string(tenant), pendingStateWaiting, now.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("store: list pending actions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var list []*PendingRow
	for rows.Next() {
		row, err := scanPending(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list pending actions: %w", err)
		}
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list pending actions: %w", err)
	}
	return list, nil
}

// TransitionPending changes the state of an action from one state to another.
// It is a compare-and-set: one statement checks the old state and writes the
// new one, so of two callers that make the same change, one succeeds and the
// other gets ErrNotFound.
//
// It returns ErrNotFound if the tenant has no action with this id, if the action
// is not in the state "from", or if the action waits for the user and its time
// has passed at now. The time does not matter for the other states, so an
// approved action can still finish after its waiting time has passed.
//
// The result id is saved when it is not empty. An empty one keeps the value that
// is stored.
func (s *Store) TransitionPending(ctx context.Context, tenant account.TenantID, id, from, to, resultID string, now time.Time) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	if err := validateID("pending action", id); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE pending_actions
		SET state = ?, result_id = COALESCE(NULLIF(?, ''), result_id)
		WHERE tenant_id = ? AND id = ? AND state = ? AND (state <> ? OR expires_at >= ?)`,
		to, resultID, string(tenant), id, from, pendingStateWaiting, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: transition pending action: %w", err)
	}
	return requireOneRow(result, "transition pending action")
}

// scanPending reads one row with the columns of pendingColumns. Times are
// returned in UTC.
func scanPending(row rowScanner) (*PendingRow, error) {
	var (
		p                    PendingRow
		createdAt, expiresAt int64
	)
	err := row.Scan(&p.ID, &p.Tenant, &p.Account, &p.Kind, &p.Hash, &p.State, &createdAt, &expiresAt,
		&p.ResultID, &p.Preview, &p.PreviewKeyVersion)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.UnixMilli(createdAt).UTC()
	p.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	return &p, nil
}
