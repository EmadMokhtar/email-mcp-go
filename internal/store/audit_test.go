package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// testAuditRow returns a valid audit row with sealed recipients.
func testAuditRow(tenant account.TenantID, acct account.AccountID, at time.Time) *AuditRow {
	return &AuditRow{
		Time:                 at,
		Tenant:               tenant,
		Account:              acct,
		Area:                 account.AreaMail,
		Client:               "test client",
		Tool:                 "send_email",
		Count:                3,
		Outcome:              "ok",
		Detail:               "rate_limited",
		Recipients:           []byte("sealed recipients"),
		RecipientsKeyVersion: 2,
	}
}

func TestAuditRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	withRecipients := testAuditRow(tenant, acct, testNow)
	withoutRecipients := testAuditRow(tenant, acct, testNow.Add(time.Second))
	withoutRecipients.Recipients = nil
	withoutRecipients.RecipientsKeyVersion = 0
	tenantLevel := testAuditRow(tenant, "", testNow.Add(2*time.Second))
	tenantLevel.Area = ""
	tenantLevel.Recipients = nil
	tenantLevel.RecipientsKeyVersion = 0
	for _, row := range []*AuditRow{withRecipients, withoutRecipients, tenantLevel} {
		require.NoError(t, s.CreateAudit(ctx, row))
	}

	got, err := s.ListAudit(ctx, tenant, AuditFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Newest first. The database assigns the ids and the caller's id is ignored.
	want := []*AuditRow{tenantLevel, withoutRecipients, withRecipients}
	for i, w := range want {
		expected := *w
		expected.ID = got[i].ID
		require.Equal(t, &expected, got[i])
		require.Positive(t, got[i].ID)
	}
}

func TestAuditTimeIsStoredAsMilliseconds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	require.NoError(t, s.CreateAudit(ctx, testAuditRow(tenant, acct, testNow)))

	var at int64
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT recorded_at FROM audit_log`).Scan(&at))
	require.Equal(t, testNow.UnixMilli(), at)
}

func TestCreateAuditRejectsBadIDs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	cases := map[string]func(r *AuditRow){
		"empty tenant":       func(r *AuditRow) { r.Tenant = "" },
		"tenant with a bar":  func(r *AuditRow) { r.Tenant = "a|b" },
		"account with a bar": func(r *AuditRow) { r.Account = "a|b" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			row := testAuditRow(tenant, acct, testNow)
			mutate(row)
			require.ErrorIs(t, s.CreateAudit(ctx, row), ErrInvalidID)
		})
	}
	var n int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&n))
	require.Zero(t, n)
}

func TestListAuditNeedsAPositiveLimit(t *testing.T) {
	// SQLite reads a negative LIMIT as "no limit", so the store refuses it.
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	require.NoError(t, s.CreateAudit(ctx, testAuditRow(tenant, acct, testNow)))

	for _, limit := range []int{0, -1} {
		got, err := s.ListAudit(ctx, tenant, AuditFilter{Limit: limit})
		require.Error(t, err, "limit %d", limit)
		require.Nil(t, got)
	}
	_, err := s.ListAudit(ctx, "", AuditFilter{Limit: 1})
	require.ErrorIs(t, err, ErrInvalidID)
}

func TestListAuditOnlyReturnsTheTenantsRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenants (id, created_at) VALUES ('other', 0)`)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount("other", "a1", testNow)))
	require.NoError(t, s.CreateAudit(ctx, testAuditRow(tenant, acct, testNow)))
	require.NoError(t, s.CreateAudit(ctx, testAuditRow("other", "a1", testNow)))

	got, err := s.ListAudit(ctx, tenant, AuditFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, tenant, got[0].Tenant)

	// The same account id in another tenant is another account.
	got, err = s.ListAudit(ctx, "other", AuditFilter{Account: "a1", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, account.TenantID("other"), got[0].Tenant)
}

func TestPruneAuditDeletesRowsStrictlyBefore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	cutoff := testNow
	for _, at := range []time.Time{
		cutoff.Add(-time.Hour), cutoff.Add(-time.Millisecond), cutoff, cutoff.Add(time.Millisecond),
	} {
		require.NoError(t, s.CreateAudit(ctx, testAuditRow(tenant, acct, at)))
	}

	n, err := s.PruneAudit(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	got, err := s.ListAudit(ctx, tenant, AuditFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, cutoff.Add(time.Millisecond), got[0].Time)
	require.Equal(t, cutoff, got[1].Time)
}

// TestAuditTableRules checks that the table keeps its own rules. A bug in the
// code above the store must not be able to put a bad row on disk.
func TestAuditTableRules(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	const insert = `INSERT INTO audit_log
		(tenant_id, account_id, recorded_at, area, client, tool, item_count, outcome, detail, recipients, recipients_key_version)
		VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`
	type values struct {
		account    any
		area       string
		client     string
		tool       string
		count      int
		outcome    string
		detail     string
		recipients any
		version    any
	}
	valid := values{string(acct), "mail", "c", "t", 1, "ok", "a_b_9", []byte("sealed"), 1}
	run := func(v values) error {
		_, err := s.db.ExecContext(ctx, insert, string(tenant), v.account, v.area, v.client, v.tool,
			v.count, v.outcome, v.detail, v.recipients, v.version)
		return err
	}
	require.NoError(t, run(valid), "the valid row must be accepted, or the cases below prove nothing")
	// The case "an empty recipients value" needs the driver to bind an empty slice
	// as an empty blob. If it bound NULL, the case would fail for another reason.
	var bound string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT typeof(?)`, []byte{}).Scan(&bound))
	require.Equal(t, "blob", bound)

	cases := map[string]func(v *values){
		"outcome that is unknown":            func(v *values) { v.outcome = "success" },
		"outcome that is empty":              func(v *values) { v.outcome = "" },
		"negative count":                     func(v *values) { v.count = -1 },
		"detail with a capital":              func(v *values) { v.detail = "Rate" },
		"detail with a space":                func(v *values) { v.detail = "a b" },
		"detail with an address":             func(v *values) { v.detail = "bob@example.org" },
		"detail with a newline at the end":   func(v *values) { v.detail = "rate\n" },
		"detail with non-ASCII letters":      func(v *values) { v.detail = "limité" },
		"detail of 65 characters":            func(v *values) { v.detail = strings.Repeat("a", 65) },
		"tool of 129 bytes":                  func(v *values) { v.tool = strings.Repeat("t", 129) },
		"tool of 130 bytes in 65 characters": func(v *values) { v.tool = strings.Repeat("é", 65) },
		"client of 129 bytes":                func(v *values) { v.client = strings.Repeat("c", 129) },
		"empty tool":                         func(v *values) { v.tool = "" },
		"a key version of 0":                 func(v *values) { v.version = 0 },
		"a negative key version":             func(v *values) { v.version = -1 },
		"an empty recipients value":          func(v *values) { v.recipients = []byte{} },
		"recipients without a key version":   func(v *values) { v.version = nil },
		"a key version without recipients":   func(v *values) { v.recipients = nil },
		"no area for an account":             func(v *values) { v.area = "" },
		"an account that does not exist":     func(v *values) { v.account = "nope" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := valid
			mutate(&v)
			require.Error(t, run(v))
		})
	}

	// The limits themselves are allowed.
	for name, mutate := range map[string]func(v *values){
		"detail of 64 characters":          func(v *values) { v.detail = strings.Repeat("a", 64) },
		"empty detail":                     func(v *values) { v.detail = "" },
		"tool of 128 bytes":                func(v *values) { v.tool = strings.Repeat("é", 64) },
		"tool of 1 byte":                   func(v *values) { v.tool = "t" },
		"a recipients value of 1 byte":     func(v *values) { v.recipients = []byte{0} },
		"count 0":                          func(v *values) { v.count = 0 },
		"no recipients and no key version": func(v *values) { v.recipients, v.version = nil, nil },
		"a tenant-level row":               func(v *values) { v.account, v.area, v.recipients, v.version = nil, "", nil, nil },
		"a tenant-level row with an area":  func(v *values) { v.account, v.recipients, v.version = nil, nil, nil },
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			v := valid
			mutate(&v)
			require.NoError(t, run(v))
		})
	}
}

func TestAuditIDsAreNotReused(t *testing.T) {
	// After the newest row is deleted, the next row must not get its id back.
	// An old id for a new row would break the order for rows with the same time.
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	require.NoError(t, s.CreateAudit(ctx, testAuditRow(tenant, acct, testNow)))
	var first int64
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT max(id) FROM audit_log`).Scan(&first))
	_, err := s.PruneAudit(ctx, testNow.Add(time.Hour))
	require.NoError(t, err)

	require.NoError(t, s.CreateAudit(ctx, testAuditRow(tenant, acct, testNow)))
	var second int64
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT max(id) FROM audit_log`).Scan(&second))
	require.Greater(t, second, first)
}

// TestAuditAccountIndex: deleting an account deletes its entries (a foreign key
// action). SQLite finds them by the account columns. Without an index on them,
// each delete would scan the whole history of the tenant.
func TestAuditAccountIndex(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rows, err := s.db.QueryContext(ctx,
		`EXPLAIN QUERY PLAN SELECT id FROM audit_log WHERE tenant_id = ? AND account_id = ?`, "t", "a")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, strings.Join(plan, "\n"), "audit_log_by_account")
}
