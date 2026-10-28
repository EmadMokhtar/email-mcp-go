package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// AuditRow is one entry of the audit log: one tool call of an agent. The store
// does not look inside the sealed recipients and does not know what the outcomes
// mean. The audit package does. The table checks the rules that do not depend on
// the meaning, such as the form of Detail. See migration 0005.
type AuditRow struct {
	// ID is assigned by the database. CreateAudit ignores the ID of the row.
	ID int64
	// Time is kept as whole milliseconds.
	Time   time.Time
	Tenant account.TenantID
	// Account is empty for an entry of the tenant that is not about one account.
	Account account.AccountID
	// Area is empty only when Account is empty.
	Area         account.Area
	Client, Tool string
	Count        int
	Outcome      string
	// Detail is a short code, never free text.
	Detail string
	// Recipients is sealed. It is nil when the call had no recipients, and then
	// RecipientsKeyVersion is 0. RecipientsKeyVersion says which tenant key
	// sealed the recipients.
	Recipients           []byte
	RecipientsKeyVersion int
}

// AuditFilter selects entries. A field that is empty means "any". The fields
// that are set must match exactly. Limit is the most entries to return, and it
// must be at least 1.
type AuditFilter struct {
	Account account.AccountID
	Area    account.Area
	Tool    string
	Limit   int
}

// auditColumns lists the columns that scanAudit reads.
const auditColumns = `id, tenant_id, COALESCE(account_id, ''), recorded_at, area, client, tool,
	item_count, outcome, detail, recipients, COALESCE(recipients_key_version, 0)`

// CreateAudit adds an entry. The ID of the row is ignored.
//
// It fails if the tenant does not exist, or if the entry has an account and the
// tenant has no account with this id. An entry without recipients is stored with
// NULL for the recipients and for the key version.
func (s *Store) CreateAudit(ctx context.Context, row *AuditRow) error {
	if err := validateID("tenant", string(row.Tenant)); err != nil {
		return err
	}
	if row.Account != "" {
		if err := validateID("account", string(row.Account)); err != nil {
			return err
		}
	}
	// Without recipients, both columns are NULL. The table needs it so.
	var recipients, keyVersion any
	if len(row.Recipients) > 0 {
		if row.RecipientsKeyVersion < 1 {
			return errors.New("store: create audit entry: the key version of the recipients must be at least 1")
		}
		recipients, keyVersion = row.Recipients, row.RecipientsKeyVersion
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (tenant_id, account_id, recorded_at, area, client, tool, item_count,
			outcome, detail, recipients, recipients_key_version)
		VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(row.Tenant), string(row.Account), row.Time.UnixMilli(), string(row.Area),
		row.Client, row.Tool, row.Count, row.Outcome, row.Detail, recipients, keyVersion)
	if err != nil {
		return fmt.Errorf("store: create audit entry: %w", err)
	}
	return nil
}

// ListAudit returns the entries of a tenant that match the filter, newest first.
// Entries with the same time are ordered by id, largest first. The filter must
// have a Limit of at least 1: SQLite reads a negative limit as "no limit", so the
// store refuses it.
func (s *Store) ListAudit(ctx context.Context, tenant account.TenantID, f AuditFilter) ([]*AuditRow, error) {
	if err := validateID("tenant", string(tenant)); err != nil {
		return nil, err
	}
	if f.Limit < 1 {
		return nil, errors.New("store: list audit entries: the limit must be at least 1")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+auditColumns+` FROM audit_log
		WHERE tenant_id = ?
			AND (? = '' OR account_id = ?)
			AND (? = '' OR area = ?)
			AND (? = '' OR tool = ?)
		ORDER BY recorded_at DESC, id DESC
		LIMIT ?`,
		string(tenant), string(f.Account), string(f.Account), string(f.Area), string(f.Area),
		f.Tool, f.Tool, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var list []*AuditRow
	for rows.Next() {
		row, err := scanAudit(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list audit entries: %w", err)
		}
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audit entries: %w", err)
	}
	return list, nil
}

// PruneAudit deletes the entries that were recorded before the given time
// (strictly earlier) and returns how many it deleted.
//
// It deletes the entries of ALL tenants, on purpose: it is housekeeping for the
// whole program. No call that is made for one user may use it.
func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_log WHERE recorded_at < ?`, before.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("store: prune audit entries: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune audit entries: %w", err)
	}
	return int(n), nil
}

// scanAudit reads one row with the columns of auditColumns. The time is returned
// in UTC.
func scanAudit(row rowScanner) (*AuditRow, error) {
	var (
		a  AuditRow
		at int64
	)
	err := row.Scan(&a.ID, &a.Tenant, &a.Account, &at, &a.Area, &a.Client, &a.Tool,
		&a.Count, &a.Outcome, &a.Detail, &a.Recipients, &a.RecipientsKeyVersion)
	if err != nil {
		return nil, err
	}
	a.Time = time.UnixMilli(at).UTC()
	return &a, nil
}
