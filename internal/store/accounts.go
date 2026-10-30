package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// accountColumns lists the columns of the accounts table in the order that
// scanAccount reads them.
const accountColumns = `tenant_id, id, provider, address, display_name, mail, levels,
	confirm_sends, confirm_deletes, autonomy_accepted_at, limits, status, created_at`

// CreateAccount adds an account. It fails if the tenant does not exist or if the
// tenant already has an account with the same id. It writes the first status
// (a.Status); after that only SetAccountStatus and ReconnectAccount change it.
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
	if err := insertAccount(ctx, s.db, a, c); err != nil {
		return fmt.Errorf("store: create account: %w", err)
	}
	return nil
}

// execer runs a statement. Both *sql.DB and *sql.Tx have this method.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertAccount adds the row of an account, with the fields that encodeAccount
// converted. CreateAccount and CreateAccountWithSecret share it, so the list
// of columns and the list of values exist once.
func insertAccount(ctx context.Context, ex execer, a *account.Account, c accountColumnValues) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO accounts (`+accountColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(a.TenantID), string(a.ID), string(a.Provider), a.Address, a.DisplayName, c.mail, c.levels,
		a.ConfirmSends, a.ConfirmDeletes, c.autonomyAcceptedAt, c.limits, string(a.Status), c.createdAt)
	return err
}

// CreateAccountWithSecret adds an account and one sealed secret of it (such as
// the password) in one transaction: both are stored, or neither is.
//
// It returns ErrExists, and stores nothing, when the tenant already has an
// account with the same address. Addresses are compared without regard to case
// (strings.EqualFold, so letters outside ASCII count too). The transaction takes
// the write lock when it starts, so two callers with the same address, also in
// two programs that use the same database file, cannot both pass the check.
//
// Like CreateAccount, it fails if the tenant does not exist or if the tenant
// already has an account with the same id.
func (s *Store) CreateAccountWithSecret(ctx context.Context, a *account.Account, kind string, keyVersion int, sealed []byte) error {
	if err := validateAccountIDs(a.TenantID, a.ID); err != nil {
		return err
	}
	c, err := encodeAccount(a)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: create account: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	taken, err := addressTaken(ctx, tx, a.TenantID, a.Address)
	if err != nil {
		return fmt.Errorf("store: create account: %w", err)
	}
	if taken {
		return ErrExists
	}
	if err := insertAccount(ctx, tx, a, c); err != nil {
		return fmt.Errorf("store: create account: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO account_secrets (tenant_id, account_id, kind, key_version, sealed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		string(a.TenantID), string(a.ID), kind, keyVersion, sealed, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("store: create account secret: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: create account: %w", err)
	}
	return nil
}

// addressTaken reports whether the tenant has an account with this address, in
// any case. SQLite folds only ASCII letters, so the compare is done here.
func addressTaken(ctx context.Context, tx *sql.Tx, tenant account.TenantID, address string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT address FROM accounts WHERE tenant_id = ?`, string(tenant))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return false, err
		}
		if strings.EqualFold(other, address) {
			return true, nil
		}
	}
	return false, rows.Err()
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

// UpdateAccount saves all fields of an account except its ids, its creation
// time, its status and the pins of its mail server. It ignores a.Status and the
// pins in a.Mail: only CreateAccount (the first values), SetAccountStatus and
// ReconnectAccount write them. So a save of the settings that read the account
// before a failed login cannot write back the old status, and a save that read
// it before a reconnect cannot write back the old pins, or bring back a pin
// that the reconnect removed.
//
// The pins come from the stored row. The transaction takes the write lock when
// it starts, so the row cannot change between that read and the write. It
// returns ErrNotFound if the tenant has no account with this id.
func (s *Store) UpdateAccount(ctx context.Context, a *account.Account) error {
	if err := validateAccountIDs(a.TenantID, a.ID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update account: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	var storedMail sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT mail FROM accounts WHERE tenant_id = ? AND id = ?`,
		string(a.TenantID), string(a.ID)).Scan(&storedMail)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: update account: %w", err)
	}
	save := *a
	if a.Mail != nil {
		mail := *a.Mail
		mail.PinnedIMAPCert, mail.PinnedSMTPCert = "", ""
		if storedMail.Valid {
			var stored account.MailServer
			if err := json.Unmarshal([]byte(storedMail.String), &stored); err != nil {
				return fmt.Errorf("store: decode mail settings: %w", err)
			}
			mail.PinnedIMAPCert, mail.PinnedSMTPCert = stored.PinnedIMAPCert, stored.PinnedSMTPCert
		}
		save.Mail = &mail
	}
	c, err := encodeAccount(&save)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE accounts SET provider = ?, address = ?, display_name = ?, mail = ?, levels = ?,
			confirm_sends = ?, confirm_deletes = ?, autonomy_accepted_at = ?, limits = ?
		WHERE tenant_id = ? AND id = ?`,
		string(a.Provider), a.Address, a.DisplayName, c.mail, c.levels,
		a.ConfirmSends, a.ConfirmDeletes, c.autonomyAcceptedAt, c.limits,
		string(a.TenantID), string(a.ID))
	if err != nil {
		return fmt.Errorf("store: update account: %w", err)
	}
	if err := requireOneRow(result, "update account"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: update account: %w", err)
	}
	return nil
}

// SetAccountStatus changes the status of an account, and nothing else. After
// CreateAccount wrote the first status, only this function and ReconnectAccount
// (which sets "ok" with the new password) change it: UpdateAccount does not
// write it. It writes only the status column, so a save
// of the settings that runs at the same time keeps its values, and this change
// is not lost by that save either. It refuses a status that the account package
// does not know. It returns ErrNotFound if the tenant has no account with this
// id.
func (s *Store) SetAccountStatus(ctx context.Context, tenant account.TenantID, id account.AccountID, status account.Status) error {
	if err := validateAccountIDs(tenant, id); err != nil {
		return err
	}
	if status != account.StatusOK && status != account.StatusNeedsReauth {
		return fmt.Errorf("store: unknown account status %q", status)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET status = ? WHERE tenant_id = ? AND id = ?`,
		string(status), string(tenant), string(id))
	if err != nil {
		return fmt.Errorf("store: set account status: %w", err)
	}
	return requireOneRow(result, "set account status")
}

// AccountReconnect is what ReconnectAccount saves.
type AccountReconnect struct {
	// Tested are the mail settings that the reconnect tested, as it read them
	// from the store.
	Tested account.MailServer
	// PinnedIMAPCert and PinnedSMTPCert are the pins to store. An empty pin
	// means no pin.
	PinnedIMAPCert, PinnedSMTPCert string
	// SecretKind is the kind of the secret to replace, such as "password".
	SecretKind string
	// KeyVersion and Sealed are the new sealed secret. A nil Sealed keeps the
	// stored secret, and KeyVersion is not used then.
	KeyVersion int
	Sealed     []byte
}

// ReconnectAccount saves a reconnect of an account in one transaction: the two
// pins of its mail server, the sealed secret when r.Sealed is not nil, and the
// status "ok". It is the only call that changes the pins of an account after
// CreateAccount. All of it is saved, or nothing is. It changes nothing else of
// the account: not its id, its other server settings, its levels, its switches
// or its limits. The queued messages, the pending actions and the audit entries
// of the account are other rows, and stay.
//
// The transaction takes the write lock when it starts, and then reads the
// account. When the stored server settings are not r.Tested, the test of the
// reconnect was made with other settings: another program changed them
// meanwhile. It returns ErrConflict then, and saves nothing. SaveSentCopy does
// not count, because the settings page may change it at any time: the save
// keeps the stored value.
//
// It returns ErrNotFound when the tenant has no account with this id.
func (s *Store) ReconnectAccount(ctx context.Context, tenant account.TenantID, id account.AccountID, r AccountReconnect) error {
	if err := validateAccountIDs(tenant, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: reconnect account: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	a, err := scanAccount(tx.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = ? AND id = ?`, string(tenant), string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: reconnect account: %w", err)
	}
	if a.Mail == nil {
		return ErrConflict
	}
	stored := *a.Mail
	tested := r.Tested
	tested.SaveSentCopy = stored.SaveSentCopy
	if stored != tested {
		return ErrConflict
	}
	stored.PinnedIMAPCert = r.PinnedIMAPCert
	stored.PinnedSMTPCert = r.PinnedSMTPCert
	mail, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("store: encode mail settings: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE accounts SET mail = ?, status = ? WHERE tenant_id = ? AND id = ?`,
		string(mail), string(account.StatusOK), string(tenant), string(id)); err != nil {
		return fmt.Errorf("store: reconnect account: %w", err)
	}
	if r.Sealed != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO account_secrets (tenant_id, account_id, kind, key_version, sealed, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (tenant_id, account_id, kind) DO UPDATE SET
				key_version = excluded.key_version,
				sealed = excluded.sealed,
				updated_at = excluded.updated_at`,
			string(tenant), string(id), r.SecretKind, r.KeyVersion, r.Sealed, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("store: reconnect account secret: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: reconnect account: %w", err)
	}
	return nil
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

// DeleteAccountWithSecrets deletes the secrets of an account and the account in
// one transaction: both are deleted, or neither is. The foreign keys delete the
// other rows of the account with it: its queued messages, pending actions,
// counters, known recipients and audit entries.
//
// The secrets are deleted by their own statement first, so the delete does not
// depend on the foreign key alone. It returns ErrNotFound, and deletes nothing,
// if the tenant has no account with this id.
func (s *Store) DeleteAccountWithSecrets(ctx context.Context, tenant account.TenantID, id account.AccountID) error {
	if err := validateAccountIDs(tenant, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM account_secrets WHERE tenant_id = ? AND account_id = ?`,
		string(tenant), string(id)); err != nil {
		return fmt.Errorf("store: delete account secrets: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM accounts WHERE tenant_id = ? AND id = ?`,
		string(tenant), string(id))
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	if err := requireOneRow(result, "delete account"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	return nil
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
