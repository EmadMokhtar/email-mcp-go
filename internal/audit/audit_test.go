package audit

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the driver that the store uses, for the second connection

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// testStart is the time at which most tests start.
var testStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// Characters that Record replaces in a client name. They are built here and not
// written in the source, so that the source itself holds no hidden characters.
var (
	rtlOverride   = string(rune(0x202e)) // right-to-left override (reverses text)
	lineSeparator = string(rune(0x2028))
	nextLine      = string(rune(0x85))   // a C1 control character
	softHyphen    = string(rune(0xad))   // a format character (Cf)
	joiner        = string(rune(0x200d)) // zero width joiner, used inside emoji
	replacement   = string(rune(0xfffd))
	manEmoji      = string(rune(0x1f468))
	womanEmoji    = string(rune(0x1f469))
)

// clock is a time source that a test can move. It is safe for use by many
// goroutines.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// fixture is an audit log on a real store and a real vault, with one account.
type fixture struct {
	t      *testing.T
	dir    string
	store  *store.Store
	vault  *secrets.Vault
	clock  *clock
	log    *Log
	tenant account.TenantID
	acct   account.AccountID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	a := account.NewIMAPAccount(tenant, "work@example.com", account.MailServer{}, testStart)
	require.NoError(t, st.CreateAccount(ctx, a))

	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(st, kek)

	c := &clock{t: testStart}
	return &fixture{
		t:      t,
		dir:    dir,
		store:  st,
		vault:  vault,
		clock:  c,
		log:    New(st, vault, c.Now),
		tenant: tenant,
		acct:   a.ID,
	}
}

// entry returns a valid entry for the fixture account, with two recipients.
func (f *fixture) entry() Entry {
	return Entry{
		Tenant:     f.tenant,
		Account:    f.acct,
		Area:       account.AreaMail,
		Client:     "claude-desktop 1.2",
		Tool:       "send_email",
		Count:      1,
		Recipients: []string{"bob@example.org", "carol@example.org"},
		Outcome:    OutcomeOK,
	}
}

// record stores e and fails the test if that does not work.
func (f *fixture) record(e Entry) {
	f.t.Helper()
	require.NoError(f.t, f.log.Record(context.Background(), e))
}

// list returns the entries of the fixture tenant that match filter.
func (f *fixture) list(filter Filter) []Entry {
	f.t.Helper()
	got, err := f.log.List(context.Background(), f.tenant, filter)
	require.NoError(f.t, err)
	return got
}

// openDB opens a second connection to the database file. The store has no
// method for some things that a test needs, such as a damaged row or a tenant
// other than "local", so the test writes them itself. The caller closes the
// handle.
func (f *fixture) openDB() *sql.DB {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	return db
}

// exec runs one statement on a second connection.
func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err := db.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err)
}

// scalar returns the number from a query that selects one number.
func (f *fixture) scalar(query string, args ...any) int {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	var n int
	require.NoError(f.t, db.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

// rows returns the number of rows in audit_log.
func (f *fixture) rows() int {
	f.t.Helper()
	return f.scalar(`SELECT count(*) FROM audit_log`)
}

// sealedRecipients returns the stored recipients and key version of a row.
func (f *fixture) sealedRecipients(id int64) (sealed []byte, keyVersion int) {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	require.NoError(f.t, db.QueryRowContext(context.Background(),
		`SELECT recipients, recipients_key_version FROM audit_log WHERE id = ?`, id).Scan(&sealed, &keyVersion))
	return sealed, keyVersion
}

// addTenant adds a tenant with one account, for isolation tests.
func (f *fixture) addTenant(id account.TenantID) account.AccountID {
	f.t.Helper()
	f.exec(`INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	return f.addAccount(id, "other@example.com")
}

// addAccount adds an account to an existing tenant.
func (f *fixture) addAccount(tenant account.TenantID, address string) account.AccountID {
	f.t.Helper()
	a := account.NewIMAPAccount(tenant, address, account.MailServer{}, testStart)
	require.NoError(f.t, f.store.CreateAccount(context.Background(), a))
	return a.ID
}

// dataFiles returns the bytes of every file in the data directory: the database
// and its write-ahead log.
func (f *fixture) dataFiles() []byte {
	f.t.Helper()
	entries, err := os.ReadDir(f.dir)
	require.NoError(f.t, err)
	var all []byte
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		require.NoError(f.t, err)
		all = append(all, data...)
	}
	return all
}

func TestRecordAndList(t *testing.T) {
	f := newFixture(t)
	e := f.entry()
	e.Count = 2
	e.Outcome = OutcomeRefused
	e.Detail = "rate_limited"
	// An address with a comma and a non-ASCII name must come back unchanged.
	e.Recipients = []string{"bob@example.org", "José, Q <jose@example.org>"}

	f.record(e)

	got := f.list(Filter{})
	require.Len(t, got, 1)
	want := e
	want.ID = got[0].ID
	want.Time = testStart
	require.Equal(t, want, got[0])
	require.Positive(t, got[0].ID)
	require.Equal(t, time.UTC, got[0].Time.Location())
}

func TestRecordKeepsEveryOutcome(t *testing.T) {
	f := newFixture(t)
	outcomes := []Outcome{OutcomeOK, OutcomeRefused, OutcomeDeclined, OutcomePendingApproval, OutcomeError}
	require.Equal(t, []Outcome{"ok", "refused", "declined", "pending_approval", "error"}, outcomes)
	for _, o := range outcomes {
		e := f.entry()
		e.Outcome = o
		f.record(e)
	}

	got := f.list(Filter{})
	require.Len(t, got, len(outcomes))
	// Newest first, so the order is the reverse of the order of the calls.
	for i, o := range outcomes {
		require.Equal(t, o, got[len(outcomes)-1-i].Outcome)
	}
}

func TestListNewestFirst(t *testing.T) {
	f := newFixture(t)
	times := []time.Duration{0, 2 * time.Minute, time.Minute}
	for i, d := range times {
		f.clock.Set(testStart.Add(d))
		e := f.entry()
		e.Count = i + 1
		f.record(e)
	}

	got := f.list(Filter{})
	require.Len(t, got, 3)
	// By time, newest first: the second call, the third call, the first call.
	require.Equal(t, []int{2, 3, 1}, []int{got[0].Count, got[1].Count, got[2].Count})
	require.Equal(t, testStart.Add(2*time.Minute), got[0].Time)
}

func TestListBreaksTiesByIDDescending(t *testing.T) {
	f := newFixture(t)
	// The clock does not move, so every entry has the same time.
	for i := 1; i <= 4; i++ {
		e := f.entry()
		e.Count = i
		f.record(e)
	}

	got := f.list(Filter{})
	require.Len(t, got, 4)
	for i, want := range []int{4, 3, 2, 1} {
		require.Equal(t, want, got[i].Count)
	}
	require.Greater(t, got[0].ID, got[1].ID)
	require.Greater(t, got[1].ID, got[2].ID)
	require.Greater(t, got[2].ID, got[3].ID)
}

func TestRecordUsesTheInjectedClock(t *testing.T) {
	f := newFixture(t)
	// A time in another zone with a part of a millisecond: the entry keeps the
	// instant, in UTC, as whole milliseconds. The time and the ID of the caller
	// are ignored.
	zone := time.FixedZone("test", 3*3600)
	f.clock.Set(time.Date(2026, 10, 27, 12, 0, 0, 1_234_567, zone))
	e := f.entry()
	e.Time = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	e.ID = 999

	f.record(e)

	got := f.list(Filter{})
	require.Len(t, got, 1)
	require.Equal(t, time.Date(2026, 10, 27, 9, 0, 0, 1_000_000, time.UTC), got[0].Time)
	require.NotEqual(t, int64(999), got[0].ID)
}

func TestNewWithoutClockUsesTheWallClock(t *testing.T) {
	f := newFixture(t)
	log := New(f.store, f.vault, nil)
	before := time.Now().Add(-time.Second)

	require.NoError(t, log.Record(context.Background(), f.entry()))

	got := f.list(Filter{})
	require.Len(t, got, 1)
	require.True(t, got[0].Time.After(before), "time %v", got[0].Time)
	require.True(t, got[0].Time.Before(time.Now().Add(time.Second)), "time %v", got[0].Time)
}

func TestRecipientsEncryptedAtRest(t *testing.T) {
	f := newFixture(t)
	e := f.entry()
	e.Recipients = []string{"bob@example.org", "carol@example.org", "dave.secret@mail.example.net"}
	f.record(e)

	// The stored value is a sealed blob with the version of the tenant key.
	got := f.list(Filter{})
	require.Len(t, got, 1)
	sealed, version := f.sealedRecipients(got[0].ID)
	require.NotEmpty(t, sealed)
	require.Equal(t, 1, version)

	raw := f.dataFiles()
	for _, addr := range e.Recipients {
		require.False(t, bytes.Contains(raw, []byte(addr)), "address %q is on disk", addr)
	}
	for _, part := range []string{"example.org", "mail.example.net", "dave.secret"} {
		require.False(t, bytes.Contains(raw, []byte(part)), "%q is on disk", part)
	}
	// The scan can see plain columns, so a pass above is not an empty result.
	require.True(t, bytes.Contains(raw, []byte(e.Tool)))
	require.True(t, bytes.Contains(raw, []byte(e.Client)))
	// The recipients come back through the vault.
	require.Equal(t, e.Recipients, got[0].Recipients)
}

func TestRecordWithoutRecipientsStoresNull(t *testing.T) {
	for name, recipients := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			e := f.entry()
			e.Tool = "list_mailboxes"
			e.Recipients = recipients
			f.record(e)

			require.Equal(t, 1, f.scalar(
				`SELECT count(*) FROM audit_log WHERE recipients IS NULL AND recipients_key_version IS NULL`))
			got := f.list(Filter{})
			require.Len(t, got, 1)
			require.Nil(t, got[0].Recipients)
		})
	}
}

func TestRecipientsAreBoundToTheirTenantAndAccount(t *testing.T) {
	// A sealed value that was copied into another row must not open there. The
	// associated data names the tenant and the account.
	f := newFixture(t)
	secondAcct := f.addAccount(f.tenant, "second@example.com")
	otherTenant := account.TenantID("tenant-two")
	otherAcct := f.addTenant(otherTenant)

	first := f.entry()
	f.record(first)
	second := f.entry()
	second.Account = secondAcct
	second.Recipients = []string{"zed@example.org"}
	f.record(second)
	other := f.entry()
	other.Tenant = otherTenant
	other.Account = otherAcct
	other.Recipients = []string{"yan@example.org"}
	f.record(other)

	secondID := int64(f.scalar(`SELECT id FROM audit_log WHERE account_id = ?`, string(secondAcct)))
	secondSealed, _ := f.sealedRecipients(secondID)
	otherID := int64(f.scalar(`SELECT id FROM audit_log WHERE tenant_id = ?`, string(otherTenant)))
	otherSealed, _ := f.sealedRecipients(otherID)
	firstID := int64(f.scalar(`SELECT id FROM audit_log WHERE account_id = ?`, string(f.acct)))

	for name, tc := range map[string]struct {
		sealed []byte
	}{
		"from another account of the same tenant": {secondSealed},
		"from another tenant":                     {otherSealed},
	} {
		t.Run(name, func(t *testing.T) {
			f.exec(`UPDATE audit_log SET recipients = ? WHERE id = ?`, tc.sealed, firstID)
			_, err := f.log.List(context.Background(), f.tenant, Filter{})
			require.Error(t, err)
		})
	}
}

func TestListFailsWhenRecipientsCannotBeRead(t *testing.T) {
	ctx := context.Background()
	valid := func(f *fixture) {
		f.record(f.entry())
		f.record(f.entry())
	}
	cases := map[string]func(f *fixture, id int64, sealed []byte){
		"a changed byte": func(f *fixture, id int64, sealed []byte) {
			damaged := bytes.Clone(sealed)
			damaged[len(damaged)-1] ^= 0xff
			f.exec(`UPDATE audit_log SET recipients = ? WHERE id = ?`, damaged, id)
		},
		"a cut value": func(f *fixture, id int64, sealed []byte) {
			f.exec(`UPDATE audit_log SET recipients = ? WHERE id = ?`, sealed[:len(sealed)/2], id)
		},
		"a key version that does not exist": func(f *fixture, id int64, _ []byte) {
			f.exec(`UPDATE audit_log SET recipients_key_version = 99 WHERE id = ?`, id)
		},
		"content that is not a list of recipients": func(f *fixture, id int64, _ []byte) {
			// Sealed correctly, with the right associated data, but not JSON.
			version, sealed, err := f.vault.Encrypt(ctx, f.tenant,
				secrets.AAD(string(f.tenant), string(f.acct), "audit:recipients"), []byte("bob@example.org"))
			require.NoError(f.t, err)
			f.exec(`UPDATE audit_log SET recipients = ?, recipients_key_version = ? WHERE id = ?`, sealed, version, id)
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			valid(f)
			newest := f.list(Filter{})[0]
			sealed, _ := f.sealedRecipients(newest.ID)
			damage(f, newest.ID, sealed)

			// One bad entry fails the whole call. The recipients of an entry are
			// never dropped without a sign.
			got, err := f.log.List(ctx, f.tenant, Filter{})
			require.Error(t, err)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), "bob@example.org")
		})
	}
}

func TestListTenantScoped(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	otherTenant := account.TenantID("tenant-two")
	otherAcct := f.addTenant(otherTenant)

	mine := f.entry()
	mine.Tool = "mine"
	f.record(mine)
	theirs := f.entry()
	theirs.Tenant = otherTenant
	theirs.Account = otherAcct
	theirs.Tool = "theirs"
	theirs.Recipients = []string{"yan@example.org"}
	f.record(theirs)

	got := f.list(Filter{})
	require.Len(t, got, 1)
	require.Equal(t, "mine", got[0].Tool)
	require.Equal(t, f.tenant, got[0].Tenant)

	other, err := f.log.List(ctx, otherTenant, Filter{})
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.Equal(t, "theirs", other[0].Tool)
	require.Equal(t, []string{"yan@example.org"}, other[0].Recipients)

	// Asking for the account of another tenant finds nothing.
	got = f.list(Filter{Account: string(otherAcct)})
	require.Empty(t, got)

	// A call without a tenant is an error, not "all tenants".
	_, err = f.log.List(ctx, "", Filter{})
	require.Error(t, err)
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	secondAcct := f.addAccount(f.tenant, "second@example.com")

	add := func(acct account.AccountID, area account.Area, tool string) {
		e := f.entry()
		e.Account = acct
		e.Area = area
		e.Tool = tool
		e.Recipients = nil
		f.record(e)
	}
	add(f.acct, account.AreaMail, "send_email")
	add(f.acct, account.AreaMail, "search_emails")
	add(f.acct, account.AreaCalendar, "list_events")
	add(secondAcct, account.AreaMail, "send_email")
	add(secondAcct, account.AreaContacts, "list_contacts")

	cases := []struct {
		name   string
		filter Filter
		want   int
	}{
		{"an empty filter means any", Filter{}, 5},
		{"account", Filter{Account: string(f.acct)}, 3},
		{"other account", Filter{Account: string(secondAcct)}, 2},
		{"area", Filter{Area: "mail"}, 3},
		{"tool", Filter{Tool: "send_email"}, 2},
		{"account and area", Filter{Account: string(f.acct), Area: "mail"}, 2},
		{"account and tool", Filter{Account: string(secondAcct), Tool: "send_email"}, 1},
		{"all three", Filter{Account: string(f.acct), Area: "calendar", Tool: "list_events"}, 1},
		{"no match", Filter{Account: string(f.acct), Area: "contacts"}, 0},
		{"a part of a tool name is not a match", Filter{Tool: "send"}, 0},
		{"a tool name in another case is not a match", Filter{Tool: "SEND_EMAIL"}, 0},
		{"a pattern is not a match", Filter{Tool: "%"}, 0},
		{"an unknown account", Filter{Account: "nope"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Len(t, f.list(tc.filter), tc.want)
		})
	}
}

func TestListLimit(t *testing.T) {
	f := newFixture(t)
	// Insert many rows with one statement. Record would be slow for 1,005 rows.
	f.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 1005)
		INSERT INTO audit_log (tenant_id, account_id, recorded_at, area, client, tool, item_count, outcome, detail)
		SELECT ?, ?, ? + i, 'mail', 'test', 'search_emails', i, 'ok', '' FROM n`,
		string(f.tenant), string(f.acct), testStart.UnixMilli())

	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero means 100", 0, 100},
		{"a small limit", 7, 7},
		{"one", 1, 1},
		{"1000 is allowed", 1000, 1000},
		{"more than 1000 is cut to 1000", 1001, 1000},
		{"a very large limit is cut to 1000", 1 << 30, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.list(Filter{Limit: tc.limit})
			require.Len(t, got, tc.want)
			// The newest entries come first, and the limit keeps the newest.
			require.Equal(t, 1005, got[0].Count)
			require.Equal(t, 1005-tc.want+1, got[len(got)-1].Count)
		})
	}

	t.Run("a negative limit is an error", func(t *testing.T) {
		got, err := f.log.List(context.Background(), f.tenant, Filter{Limit: -1})
		require.Error(t, err)
		require.Nil(t, got)
	})
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	otherTenant := account.TenantID("tenant-two")
	otherAcct := f.addTenant(otherTenant)
	now := testStart.Add(200 * 24 * time.Hour)
	const retention = 90 * 24 * time.Hour

	add := func(age time.Duration, tool string) {
		f.clock.Set(now.Add(-age))
		e := f.entry()
		e.Tool = tool
		f.record(e)
	}
	add(100*24*time.Hour, "old_a")
	add(91*24*time.Hour, "old_b")
	add(retention+time.Millisecond, "old_c")
	add(retention, "at_cutoff") // exactly 90 days old: kept
	add(retention-time.Millisecond, "recent_a")
	add(time.Hour, "recent_b")
	// An old entry of another tenant is deleted too: Prune is for the whole
	// program.
	f.clock.Set(now.Add(-120 * 24 * time.Hour))
	theirs := f.entry()
	theirs.Tenant = otherTenant
	theirs.Account = otherAcct
	theirs.Tool = "old_other_tenant"
	f.record(theirs)

	f.clock.Set(now)
	n, err := f.log.Prune(ctx, retention)
	require.NoError(t, err)
	require.Equal(t, 4, n)

	var tools []string
	for _, e := range f.list(Filter{}) {
		tools = append(tools, e.Tool)
	}
	require.Equal(t, []string{"recent_b", "recent_a", "at_cutoff"}, tools)
	other, err := f.log.List(ctx, otherTenant, Filter{})
	require.NoError(t, err)
	require.Empty(t, other)

	// A second run has nothing to delete.
	n, err = f.log.Prune(ctx, retention)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPruneRejectsNegativeAge(t *testing.T) {
	f := newFixture(t)
	f.record(f.entry())

	n, err := f.log.Prune(context.Background(), -time.Hour)
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, 1, f.rows())
}

// manyR returns a string of n letters "r".
func manyR(n int) string { return strings.Repeat("r", n) }

func TestRecordValidation(t *testing.T) {
	long := strings.Repeat("a", 65)
	// 65 characters of two bytes each are 130 bytes: the limit is in bytes.
	wide := strings.Repeat("é", 65)
	cases := []struct {
		name   string
		mutate func(e *Entry)
		// leak is a value that the error text must not contain. The fields come
		// from the client or hold secrets, and the error may reach a log.
		leak string
	}{
		{"detail with capitals", func(e *Entry) { e.Detail = "Rate_Limited" }, "Rate_Limited"},
		{"detail with a space", func(e *Entry) { e.Detail = "rate limited" }, "rate limited"},
		{"detail with a dash", func(e *Entry) { e.Detail = "rate-limited" }, "rate-limited"},
		{"detail with an address", func(e *Entry) { e.Detail = "bob@example.org" }, "bob@example.org"},
		{"detail with a trailing newline", func(e *Entry) { e.Detail = "rate_limited\n" }, "rate_limited"},
		{"detail with non-ASCII letters", func(e *Entry) { e.Detail = "taux_limité" }, "taux_limité"},
		{"detail of 65 characters", func(e *Entry) { e.Detail = long }, long},
		{"tool of 129 bytes", func(e *Entry) { e.Tool = strings.Repeat("t", 129) }, strings.Repeat("t", 129)},
		{"tool of more than 128 bytes in 65 characters", func(e *Entry) { e.Tool = wide }, wide},
		{"tool that is not UTF-8", func(e *Entry) { e.Tool = "send\xff" }, "send\xff"},
		{"tool with a newline", func(e *Entry) { e.Tool = "send\nemail" }, "send\nemail"},
		{"empty outcome", func(e *Entry) { e.Outcome = "" }, ""},
		{"unknown outcome", func(e *Entry) { e.Outcome = "success" }, "success"},
		{"outcome in another case", func(e *Entry) { e.Outcome = "OK" }, ""},
		{"negative count", func(e *Entry) { e.Count = -1 }, ""},
		{"empty tenant", func(e *Entry) { e.Tenant = "" }, ""},
		{"tenant with a bar", func(e *Entry) { e.Tenant = "a|b" }, "a|b"},
		{"account with a bar", func(e *Entry) { e.Account = "a|b" }, "a|b"},
		{"no area for an account", func(e *Entry) { e.Area = "" }, ""},
		{"empty tool", func(e *Entry) { e.Tool = "" }, ""},
		{"201 recipients", func(e *Entry) { e.Recipients = slices.Repeat([]string{"bob@example.org"}, 201) }, "bob@example.org"},
		{"a recipient of 513 bytes", func(e *Entry) { e.Recipients = []string{"bob@example.org", manyR(513)} }, manyR(513)},
		{"a recipient that is not UTF-8", func(e *Entry) { e.Recipients = []string{"bob@example.org", "x\xff@example.org"} }, "x\xff@example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			e := f.entry()
			tc.mutate(&e)

			err := f.log.Record(context.Background(), e)

			require.Error(t, err)
			// A caller can tell a bad entry from a failure of the database.
			require.ErrorIs(t, err, ErrInvalidEntry)
			if tc.leak != "" {
				require.NotContains(t, err.Error(), tc.leak)
			}
			require.NotContains(t, err.Error(), "bob@example.org")
			// Nothing is stored for an entry that is refused.
			require.Zero(t, f.rows())
		})
	}
}

func TestRecordAcceptsTheLimits(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *Entry)
	}{
		{"detail of 64 characters", func(e *Entry) { e.Detail = strings.Repeat("a", 64) }},
		{"detail with digits and underscores", func(e *Entry) { e.Detail = "level_too_low_0_9" }},
		{"empty detail", func(e *Entry) { e.Detail = "" }},
		{"tool of 128 bytes", func(e *Entry) { e.Tool = strings.Repeat("t", 128) }},
		{"tool of 128 bytes in 64 characters", func(e *Entry) { e.Tool = strings.Repeat("é", 64) }},
		{"empty client", func(e *Entry) { e.Client = "" }},
		{"tool of 1 byte", func(e *Entry) { e.Tool = "t" }},
		{"200 recipients", func(e *Entry) { e.Recipients = distinctRecipients(200) }},
		{"a recipient of 512 bytes", func(e *Entry) { e.Recipients = []string{"bob@example.org", manyR(512)} }},
		{"a recipient of 512 bytes in 256 characters", func(e *Entry) { e.Recipients = []string{strings.Repeat("é", 256)} }},
		{"count 0", func(e *Entry) { e.Count = 0 }},
		{"a large count", func(e *Entry) { e.Count = 1 << 30 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			e := f.entry()
			tc.mutate(&e)

			require.NoError(t, f.log.Record(context.Background(), e))

			got := f.list(Filter{})
			require.Len(t, got, 1)
			require.Equal(t, e.Detail, got[0].Detail)
			require.Equal(t, e.Tool, got[0].Tool)
			require.Equal(t, e.Client, got[0].Client)
			require.Equal(t, e.Count, got[0].Count)
			require.Equal(t, e.Recipients, got[0].Recipients)
		})
	}
}

// distinctRecipients returns n different addresses.
func distinctRecipients(n int) []string {
	list := make([]string, n)
	for i := range list {
		list[i] = fmt.Sprintf("user%03d@example.org", i)
	}
	return list
}

// TestRecordSanitizesTheClient: the client name comes from the audited party, so
// a bad name must never hide the action. Record cleans it and writes the entry.
func TestRecordSanitizesTheClient(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"a clean name is kept", "Claude für Mac (1.2.3)", "Claude für Mac (1.2.3)"},
		{"an empty name is kept", "", ""},
		{"128 bytes are kept", strings.Repeat("c", 128), strings.Repeat("c", 128)},
		{"129 bytes are cut to 128", strings.Repeat("c", 129), strings.Repeat("c", 128)},
		{"a very long name is cut to 128", strings.Repeat("c", 100_000), strings.Repeat("c", 128)},
		{"two-byte letters are cut on a character", strings.Repeat("é", 65), strings.Repeat("é", 64)},
		{"a cut never splits a character", strings.Repeat("c", 127) + "é", strings.Repeat("c", 127)},
		{"a soft hyphen", "x" + softHyphen + "y", "x" + replacement + "y"},
		{"a right-to-left override", "x" + rtlOverride + "y", "x" + replacement + "y"},
		{"a joiner inside an emoji sequence", manEmoji + joiner + womanEmoji, manEmoji + replacement + womanEmoji},
		{"a line separator", "x" + lineSeparator + "y", "x" + replacement + "y"},
		{"a C1 control", "x" + nextLine + "y", "x" + replacement + "y"},
		{"a NUL", "x\x00y", "x" + replacement + "y"},
		{"a DEL", "x\x7fy", "x" + replacement + "y"},
		{"a tab", "x\ty", "x" + replacement + "y"},
		{"a carriage return and a line feed", "ok\r\nEVIL", "ok" + replacement + replacement + "EVIL"},
		{"an escape sequence", "\x1b[31mred", replacement + "[31mred"},
		{"invalid UTF-8 inside the name", "x\xc3(", "x" + replacement + "("},
		{"invalid UTF-8 at the end", "x\xff", "x" + replacement},
		{"a cut after the replacement", strings.Repeat("\xff", 50), strings.Repeat(replacement, 42)},
		{"a replacement that is in the name already", "x" + replacement + "y", "x" + replacement + "y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			e := f.entry()
			e.Client = tc.input

			require.NoError(t, f.log.Record(context.Background(), e))

			got := f.list(Filter{})
			require.Len(t, got, 1, "the action must be in the log")
			require.Equal(t, tc.want, got[0].Client)
			require.True(t, utf8.ValidString(got[0].Client))
			require.LessOrEqual(t, len(got[0].Client), 128)
			// The rest of the entry is as it was.
			require.Equal(t, e.Tool, got[0].Tool)
			require.Equal(t, e.Recipients, got[0].Recipients)
		})
	}
}

func TestRecordTenantLevelEntry(t *testing.T) {
	f := newFixture(t)
	// A tenant-level entry has no account. It may have an area or not. Its
	// recipients are sealed with an empty account in the associated data.
	for _, area := range []account.Area{"", account.AreaMail} {
		e := f.entry()
		e.Account = ""
		e.Area = area
		e.Tool = "list_accounts"
		f.record(e)
	}

	require.Equal(t, 2, f.scalar(`SELECT count(*) FROM audit_log WHERE account_id IS NULL`))
	got := f.list(Filter{})
	require.Len(t, got, 2)
	for _, e := range got {
		require.Empty(t, e.Account)
		require.Equal(t, "list_accounts", e.Tool)
		require.Equal(t, []string{"bob@example.org", "carol@example.org"}, e.Recipients)
	}
	// A filter by account does not return entries without an account.
	require.Empty(t, f.list(Filter{Account: string(f.acct)}))
}

func TestRecordNeedsAnExistingTenantAndAccount(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *Entry)
	}{
		{"an unknown account", func(e *Entry) { e.Account = "no-such-account" }},
		{"an unknown tenant", func(e *Entry) { e.Tenant = "no-such-tenant" }},
		{"an unknown tenant without recipients", func(e *Entry) { e.Tenant = "no-such-tenant"; e.Account = ""; e.Recipients = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			e := f.entry()
			tc.mutate(&e)

			err := f.log.Record(context.Background(), e)

			require.Error(t, err)
			// The entry is well formed. The failure is in the data, not in the entry.
			require.NotErrorIs(t, err, ErrInvalidEntry)
			require.Zero(t, f.rows())
		})
	}
}

func TestDeletingAnAccountDeletesItsEntries(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	secondAcct := f.addAccount(f.tenant, "second@example.com")

	first := f.entry()
	f.record(first)
	second := f.entry()
	second.Account = secondAcct
	second.Tool = "kept_account_entry"
	f.record(second)
	tenantLevel := f.entry()
	tenantLevel.Account = ""
	tenantLevel.Area = ""
	tenantLevel.Tool = "kept_tenant_entry"
	tenantLevel.Recipients = nil
	f.record(tenantLevel)
	require.Equal(t, 3, f.rows())

	require.NoError(t, f.store.DeleteAccount(ctx, f.tenant, f.acct))

	got := f.list(Filter{})
	require.Len(t, got, 2)
	require.Equal(t, []string{"kept_tenant_entry", "kept_account_entry"}, []string{got[0].Tool, got[1].Tool})
	require.Zero(t, f.scalar(`SELECT count(*) FROM audit_log WHERE account_id = ?`, string(f.acct)))
}

func TestRecordSurvivesACancelledContext(t *testing.T) {
	// The caller may leave while the tool runs. The entry for the action must
	// still be written, and the call must not report an error for a write that
	// happened.
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, f.log.Record(ctx, f.entry()))

	got := f.list(Filter{})
	require.Len(t, got, 1)
	require.Equal(t, []string{"bob@example.org", "carol@example.org"}, got[0].Recipients)
}

func TestListStopsOnACancelledContext(t *testing.T) {
	f := newFixture(t)
	f.record(f.entry())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := f.log.List(ctx, f.tenant, Filter{})
	require.Error(t, err)
	require.Nil(t, got)
}

func TestSameRecipientsGiveDifferentSealedValues(t *testing.T) {
	// The same recipients in two entries must not give the same bytes on disk.
	f := newFixture(t)
	f.record(f.entry())
	f.record(f.entry())

	got := f.list(Filter{})
	require.Len(t, got, 2)
	a, _ := f.sealedRecipients(got[0].ID)
	b, _ := f.sealedRecipients(got[1].ID)
	require.NotEqual(t, a, b)
}
