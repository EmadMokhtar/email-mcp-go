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

// accountColumns lists the columns of the accounts table in the order that
// scanAccount reads them.
const accountColumns = `tenant_id, id, provider, address, display_name, mail, levels,
	confirm_sends, confirm_deletes, autonomy_accepted_at, limits, status, created_at`

// CreateAccount adds an account. It fails if the tenant does not exist or if the
// tenant already has an account with the same id.
//
// The store keeps times as whole milliseconds.
func (s *Store) CreateAccount(ctx context.Context, a *account.Account) error {
	if err := validateAccountIDs(a.TenantID, a.ID); err != nil {
		return err
	}
	c, err := encodeAccount(a)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO accounts (`+accountColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(a.TenantID), string(a.ID), string(a.Provider), a.Address, a.DisplayName, c.mail, c.levels,
		a.ConfirmSends, a.ConfirmDeletes, c.autonomyAcceptedAt, c.limits, string(a.Status), c.createdAt)
	if err != nil {
		return fmt.Errorf("store: create account: %w", err)
	}
	return nil
}

// GetAccount returns one account of a tenant. It returns ErrNotFound if the
// tenant has no account with this id.
func (s *Store) GetAccount(ctx context.Context, tenant account.TenantID, id account.AccountID) (*account.Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = ? AND id = ?`,
		string(tenant), string(id))
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get account: %w", err)
	}
	return a, nil
}

// ListAccounts returns all accounts of a tenant, oldest first. Accounts with
// the same creation time are ordered by id.
func (s *Store) ListAccounts(ctx context.Context, tenant account.TenantID) ([]*account.Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = ? ORDER BY created_at, id`,
		string(tenant))
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var accounts []*account.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list accounts: %w", err)
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	return accounts, nil
}

// UpdateAccount saves all fields of an account except its ids and its creation
// time. It returns ErrNotFound if the tenant has no account with this id.
func (s *Store) UpdateAccount(ctx context.Context, a *account.Account) error {
	if err := validateAccountIDs(a.TenantID, a.ID); err != nil {
		return err
	}
	c, err := encodeAccount(a)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET provider = ?, address = ?, display_name = ?, mail = ?, levels = ?,
			confirm_sends = ?, confirm_deletes = ?, autonomy_accepted_at = ?, limits = ?, status = ?
		WHERE tenant_id = ? AND id = ?`,
		string(a.Provider), a.Address, a.DisplayName, c.mail, c.levels,
		a.ConfirmSends, a.ConfirmDeletes, c.autonomyAcceptedAt, c.limits, string(a.Status),
		string(a.TenantID), string(a.ID))
	if err != nil {
		return fmt.Errorf("store: update account: %w", err)
	}
	return requireOneRow(result, "update account")
}

// DeleteAccount deletes an account and, through the foreign key, its secrets.
// It returns ErrNotFound if the tenant has no account with this id.
func (s *Store) DeleteAccount(ctx context.Context, tenant account.TenantID, id account.AccountID) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM accounts WHERE tenant_id = ? AND id = ?`,
		string(tenant), string(id))
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	return requireOneRow(result, "delete account")
}

// PutAccountSecret saves a sealed (encrypted) value of an account. It replaces
// the value of the same kind, if there is one. The key version says which tenant
// key sealed the value.
//
// It fails if the tenant has no account with this id. The store does not look
// inside the sealed value.
func (s *Store) PutAccountSecret(ctx context.Context, tenant account.TenantID, acct account.AccountID, kind string, keyVersion int, sealed []byte) error {
	if err := validateAccountIDs(tenant, acct); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO account_secrets (tenant_id, account_id, kind, key_version, sealed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, account_id, kind) DO UPDATE SET
			key_version = excluded.key_version,
			sealed = excluded.sealed,
			updated_at = excluded.updated_at`,
		string(tenant), string(acct), kind, keyVersion, sealed, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: put account secret: %w", err)
	}
	return nil
}

// GetAccountSecret returns the sealed value of one kind and the version of the
// tenant key that sealed it. It returns ErrNotFound if there is none.
func (s *Store) GetAccountSecret(ctx context.Context, tenant account.TenantID, acct account.AccountID, kind string) (keyVersion int, sealed []byte, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT key_version, sealed FROM account_secrets WHERE tenant_id = ? AND account_id = ? AND kind = ?`,
		string(tenant), string(acct), kind).Scan(&keyVersion, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("store: get account secret: %w", err)
	}
	return keyVersion, sealed, nil
}

// DeleteAccountSecrets deletes all secrets of an account. It is not an error if
// there are none.
func (s *Store) DeleteAccountSecrets(ctx context.Context, tenant account.TenantID, acct account.AccountID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM account_secrets WHERE tenant_id = ? AND account_id = ?`,
		string(tenant), string(acct))
	if err != nil {
		return fmt.Errorf("store: delete account secrets: %w", err)
	}
	return nil
}

// validateAccountIDs checks the tenant id and the account id.
func validateAccountIDs(tenant account.TenantID, id account.AccountID) error {
	if err := validateID("tenant", string(tenant)); err != nil {
		return err
	}
	return validateID("account", string(id))
}

// requireOneRow returns ErrNotFound if the statement changed no row.
func requireOneRow(result sql.Result, action string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: %w", action, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// accountColumnValues holds the account fields that need conversion before they
// go into the database.
type accountColumnValues struct {
	mail               sql.NullString // JSON, or NULL when the account has no mail settings
	levels             string         // JSON
	limits             string         // JSON
	autonomyAcceptedAt sql.NullInt64  // Unix milliseconds, or NULL
	createdAt          int64          // Unix milliseconds
}

// encodeAccount converts the fields of an account that are stored as JSON or
// as milliseconds.
func encodeAccount(a *account.Account) (accountColumnValues, error) {
	var c accountColumnValues
	if a.Mail != nil {
		mail, err := json.Marshal(a.Mail)
		if err != nil {
			return c, fmt.Errorf("store: encode mail settings: %w", err)
		}
		c.mail = sql.NullString{String: string(mail), Valid: true}
	}
	levels, err := json.Marshal(a.Levels)
	if err != nil {
		return c, fmt.Errorf("store: encode levels: %w", err)
	}
	c.levels = string(levels)
	limits, err := json.Marshal(a.Limits)
	if err != nil {
		return c, fmt.Errorf("store: encode limits: %w", err)
	}
	c.limits = string(limits)
	if a.AutonomyAcceptedAt != nil {
		c.autonomyAcceptedAt = sql.NullInt64{Int64: a.AutonomyAcceptedAt.UnixMilli(), Valid: true}
	}
	c.createdAt = a.CreatedAt.UnixMilli()
	return c, nil
}

// rowScanner is the Scan method of both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAccount reads one row with the columns of accountColumns. Times are
// returned in UTC.
func scanAccount(row rowScanner) (*account.Account, error) {
	var (
		a                  account.Account
		mail               sql.NullString
		levels, limits     string
		autonomyAcceptedAt sql.NullInt64
		createdAt          int64
	)
	err := row.Scan(&a.TenantID, &a.ID, &a.Provider, &a.Address, &a.DisplayName, &mail, &levels,
		&a.ConfirmSends, &a.ConfirmDeletes, &autonomyAcceptedAt, &limits, &a.Status, &createdAt)
	if err != nil {
		return nil, err
	}

	if mail.Valid {
		a.Mail = new(account.MailServer)
		if err := json.Unmarshal([]byte(mail.String), a.Mail); err != nil {
			return nil, fmt.Errorf("decode mail settings: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(levels), &a.Levels); err != nil {
		return nil, fmt.Errorf("decode levels: %w", err)
	}
	if err := json.Unmarshal([]byte(limits), &a.Limits); err != nil {
		return nil, fmt.Errorf("decode limits: %w", err)
	}
	if autonomyAcceptedAt.Valid {
		t := time.UnixMilli(autonomyAcceptedAt.Int64).UTC()
		a.AutonomyAcceptedAt = &t
	}
	a.CreatedAt = time.UnixMilli(createdAt).UTC()
	return &a, nil
}
