package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// testNow is a fixed time with a millisecond part. The store keeps time as
// whole milliseconds, so a round trip must give this value back exactly.
var testNow = time.Date(2026, 10, 27, 9, 30, 15, 123_000_000, time.UTC)

// newTestStore opens a store in a new temporary directory. It closes the store
// when the test ends.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// testServer returns valid mail server settings.
func testServer() account.MailServer {
	return account.MailServer{
		IMAPHost:     "imap.example.com",
		IMAPPort:     993,
		IMAPSecurity: account.SecurityTLS,
		SMTPHost:     "smtp.example.com",
		SMTPPort:     465,
		SMTPSecurity: account.SecurityTLS,
		Username:     "user@example.com",
		SaveSentCopy: true,
	}
}

// testAccount returns a new account with the given id, tenant and creation
// time.
func testAccount(tenant account.TenantID, id account.AccountID, created time.Time) *account.Account {
	a := account.NewIMAPAccount(tenant, string(id)+"@example.com", testServer(), created)
	a.ID = id
	return a
}

// ensureTestTenant creates a tenant other than "local" for isolation tests.
func ensureTestTenant(t *testing.T, s *Store, id account.TenantID) {
	t.Helper()
	_, err := s.ensureTenant(context.Background(), id)
	require.NoError(t, err)
}

// migrationFileCount returns the number of embedded migration files.
func migrationFileCount(t *testing.T) int {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	require.NoError(t, err)
	return len(entries)
}

func TestOpenCreatesPrivateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "data")

	s, err := Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(dir, "email-mcp.db"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// A write makes SQLite create the WAL and shared-memory files. They must
	// be private too, because they hold recent data.
	_, err = s.EnsureLocalTenant(context.Background())
	require.NoError(t, err)
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err = os.Stat(filepath.Join(dir, "email-mcp.db"+suffix))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), suffix)
	}
}

func TestOpenLeavesExistingDirectoryAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o750)) //nolint:gosec // G302: this test needs a directory with a mode other than 0700

	s, err := Open(context.Background(), dir)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

func TestOpenRejectsFileAsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	_, err := Open(context.Background(), path)
	require.Error(t, err)
}

func TestOpenSetsPragmasOnEveryConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Hold two connections at the same time, so the pool must make two.
	first, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Close()) }()
	second, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, second.Close()) }()

	for _, conn := range []*sql.Conn{first, second} {
		var journalMode string
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode))
		require.Equal(t, "wal", journalMode)

		var foreignKeys int
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys))
		require.Equal(t, 1, foreignKeys)

		var busyTimeout int
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout))
		require.Equal(t, 5000, busyTimeout)

		// secure_delete 1 means ON: deleted content is overwritten with zeros, so
		// a cleared message body does not stay in a free page.
		var secureDelete int
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&secureDelete))
		require.Equal(t, 1, secureDelete)

		// synchronous 2 means FULL: SQLite flushes the log to disk at every commit.
		// Exactly-once sending after a power loss depends on it.
		var synchronous int
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous))
		require.Equal(t, 2, synchronous)
	}
}

func TestOpenWorksWithSpecialCharactersInPath(t *testing.T) {
	// The database path is part of a URI. A "?", "#" or "%" in it must not be
	// read as the start of a query, a fragment or an escape.
	dir := filepath.Join(t.TempDir(), "my data?#100%")

	s, err := Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = os.Stat(filepath.Join(dir, "email-mcp.db"))
	require.NoError(t, err)
}

func TestMigrationsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first, err := Open(ctx, dir)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	var rows int
	require.NoError(t, second.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&rows))
	require.Equal(t, migrationFileCount(t), rows)
}

// A database that a newer email-mcp has used has a migration that this binary
// does not know. This binary must not run on it: the newer schema can have
// states, columns and rules that this code does not respect.
func TestOpenRefusesADatabaseOfANewerVersion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	require.NoError(t, err)
	_, err = first.db.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (9999)")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	s, err := Open(ctx, dir)

	require.ErrorIs(t, err, ErrNewerSchema)
	require.Nil(t, s)
	require.Equal(t, "this data was written by a newer email-mcp; install that version or newer", err.Error(),
		"the text is fixed and names no path")
}

func TestOpenConcurrentlyRunsMigrationsOnce(t *testing.T) {
	// Two programs can start at the same time, for example the server and a
	// setup command. Both must succeed and each migration must run once.
	ctx := context.Background()
	dir := t.TempDir()

	const workers = 6
	stores := make([]*Store, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stores[i], errs[i] = Open(ctx, dir)
		}()
	}
	wg.Wait()

	for i := range workers {
		require.NoError(t, errs[i])
	}
	var rows int
	require.NoError(t, stores[0].db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&rows))
	require.Equal(t, migrationFileCount(t), rows)
	for _, s := range stores {
		require.NoError(t, s.Close())
	}
}

func TestEnsureLocalTenant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.Equal(t, account.TenantID("local"), first)

	// A second call returns the same tenant and does not add a row.
	second, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.Equal(t, first, second)

	var rows int
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM tenants").Scan(&rows))
	require.Equal(t, 1, rows)
}

func TestAccountRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	want := testAccount(tenant, "acct1", testNow)
	want.DisplayName = "Work mail"
	want.Mail.PinnedIMAPCert = "aa11"
	want.Mail.PinnedSMTPCert = "bb22"
	want.Levels[account.AreaCalendar] = account.Level(2)
	want.ConfirmSends = false
	accepted := testNow.Add(2 * time.Hour)
	want.AutonomyAcceptedAt = &accepted
	want.Limits = account.Limits{
		SendsPerHour:         7,
		SendsPerDay:          70,
		RecipientsPerMessage: 9,
		TrashPerDay:          11,
		UndoWindowAuto:       45 * time.Second,
		UndoWindowConfirmed:  5 * time.Second,
		NewRecipientCheck:    true,
	}
	want.Status = account.StatusNeedsReauth

	require.NoError(t, s.CreateAccount(ctx, want))

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestAccountRoundTripWithEmptyOptionalFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// No mail settings, no levels and no autonomy time.
	want := &account.Account{
		ID:        "bare",
		TenantID:  tenant,
		Provider:  account.ProviderIMAP,
		Address:   "bare@example.com",
		Status:    account.StatusOK,
		CreatedAt: testNow,
	}
	require.NoError(t, s.CreateAccount(ctx, want))

	got, err := s.GetAccount(ctx, tenant, "bare")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Nil(t, got.Mail)
	require.Nil(t, got.AutonomyAcceptedAt)
}

func TestAccountTimesAreStoredAsMilliseconds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// A time with microseconds loses them. The result is in UTC.
	withMicros := testNow.Add(456 * time.Microsecond)
	a := testAccount(tenant, "acct1", withMicros.In(time.FixedZone("UTC+3", 3*60*60)))
	require.NoError(t, s.CreateAccount(ctx, a))

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, testNow, got.CreatedAt)
	require.Equal(t, time.UTC, got.CreatedAt.Location())
}

func TestCreateAccountRejectsDuplicateAndUnknownTenant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))
	require.Error(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))

	// The tenant must exist.
	require.Error(t, s.CreateAccount(ctx, testAccount("ghost", "acct2", testNow)))
}

func TestGetAccountOtherTenantNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))

	_, err = s.GetAccount(ctx, "other", "acct1")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = s.GetAccount(ctx, tenant, "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSameAccountIDInTwoTenants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	mine := testAccount(local, "same", testNow)
	theirs := testAccount("other", "same", testNow)
	theirs.Address = "other@example.com"
	require.NoError(t, s.CreateAccount(ctx, mine))
	require.NoError(t, s.CreateAccount(ctx, theirs))

	got, err := s.GetAccount(ctx, local, "same")
	require.NoError(t, err)
	require.Equal(t, mine, got)

	got, err = s.GetAccount(ctx, "other", "same")
	require.NoError(t, err)
	require.Equal(t, theirs, got)
}

func TestListAccountsIsScopedAndOrdered(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	// Order is creation time, then id. "a" and "c" share a time.
	later := testAccount(local, "b", testNow.Add(2*time.Second))
	sameTimeC := testAccount(local, "c", testNow)
	sameTimeA := testAccount(local, "a", testNow)
	foreign := testAccount("other", "z", testNow)
	for _, a := range []*account.Account{later, sameTimeC, foreign, sameTimeA} {
		require.NoError(t, s.CreateAccount(ctx, a))
	}

	got, err := s.ListAccounts(ctx, local)
	require.NoError(t, err)
	require.Equal(t, []*account.Account{sameTimeA, sameTimeC, later}, got)

	got, err = s.ListAccounts(ctx, "other")
	require.NoError(t, err)
	require.Equal(t, []*account.Account{foreign}, got)

	got, err = s.ListAccounts(ctx, "nobody")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestUpdateAccount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	original := testAccount(tenant, "acct1", testNow)
	require.NoError(t, s.CreateAccount(ctx, original))

	changed := testAccount(tenant, "acct1", testNow)
	changed.DisplayName = "Renamed"
	changed.Levels[account.AreaMail] = account.MailRead
	changed.ConfirmDeletes = false
	changed.Mail.IMAPHost = "imap.other.example.com"
	changed.Limits.SendsPerHour = 3
	changed.Status = account.StatusNeedsReauth
	accepted := testNow.Add(time.Minute)
	changed.AutonomyAcceptedAt = &accepted
	// The creation time never changes, even if the caller changes it.
	changed.CreatedAt = testNow.Add(24 * time.Hour)
	require.NoError(t, s.UpdateAccount(ctx, changed))

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	changed.CreatedAt = testNow
	require.Equal(t, changed, got)
}

func TestUpdateAccountNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	original := testAccount(tenant, "acct1", testNow)
	require.NoError(t, s.CreateAccount(ctx, original))

	// An update that names another tenant changes nothing.
	hijack := testAccount("other", "acct1", testNow)
	hijack.DisplayName = "Hijacked"
	require.ErrorIs(t, s.UpdateAccount(ctx, hijack), ErrNotFound)
	require.ErrorIs(t, s.UpdateAccount(ctx, testAccount(tenant, "missing", testNow)), ErrNotFound)

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestDeleteAccount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	keep := testAccount(tenant, "keep", testNow)
	require.NoError(t, s.CreateAccount(ctx, keep))
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "gone", testNow)))

	// Another tenant cannot delete the account.
	require.ErrorIs(t, s.DeleteAccount(ctx, "other", "gone"), ErrNotFound)
	_, err = s.GetAccount(ctx, tenant, "gone")
	require.NoError(t, err)

	require.NoError(t, s.DeleteAccount(ctx, tenant, "gone"))
	_, err = s.GetAccount(ctx, tenant, "gone")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteAccount(ctx, tenant, "gone"), ErrNotFound)

	got, err := s.GetAccount(ctx, tenant, "keep")
	require.NoError(t, err)
	require.Equal(t, keep, got)
}

func TestWritesRejectUnsafeIDs(t *testing.T) {
	// The encryption package joins ids with "|" and does not escape them. An
	// id with "|" in it, or an empty id, must never reach the database.
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))

	for name, bad := range map[string]string{"empty": "", "pipe": "a|b", "only a pipe": "|"} {
		t.Run(name, func(t *testing.T) {
			badTenant := account.TenantID(bad)
			badAccount := account.AccountID(bad)

			_, err := s.ensureTenant(ctx, badTenant)
			require.ErrorIs(t, err, ErrInvalidID)

			require.ErrorIs(t, s.CreateAccount(ctx, testAccount(badTenant, "acct2", testNow)), ErrInvalidID)
			require.ErrorIs(t, s.CreateAccount(ctx, testAccount(tenant, badAccount, testNow)), ErrInvalidID)

			require.ErrorIs(t, s.UpdateAccount(ctx, testAccount(badTenant, "acct1", testNow)), ErrInvalidID)
			require.ErrorIs(t, s.UpdateAccount(ctx, testAccount(tenant, badAccount, testNow)), ErrInvalidID)

			require.ErrorIs(t, s.PutTenantKey(ctx, badTenant, 1, []byte("w"), "kek"), ErrInvalidID)
			require.ErrorIs(t, s.RewrapTenantKey(ctx, badTenant, 1, []byte("w"), "kek"), ErrInvalidID)

			require.ErrorIs(t, s.PutAccountSecret(ctx, badTenant, "acct1", "password", 1, []byte("s")), ErrInvalidID)
			require.ErrorIs(t, s.PutAccountSecret(ctx, tenant, badAccount, "password", 1, []byte("s")), ErrInvalidID)
		})
	}

	// Nothing was written by the rejected calls.
	accounts, err := s.ListAccounts(ctx, tenant)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	var tenants, keys, secrets int
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM tenants").Scan(&tenants))
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM tenant_keys").Scan(&keys))
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM account_secrets").Scan(&secrets))
	require.Equal(t, 1, tenants)
	require.Zero(t, keys)
	require.Zero(t, secrets)
}

func TestAccountSecretRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))

	require.NoError(t, s.PutAccountSecret(ctx, tenant, "acct1", "imap_password", 3, []byte("sealed-1")))
	require.NoError(t, s.PutAccountSecret(ctx, tenant, "acct1", "oauth_refresh", 4, []byte("sealed-2")))

	version, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "imap_password")
	require.NoError(t, err)
	require.Equal(t, 3, version)
	require.Equal(t, []byte("sealed-1"), sealed)

	// A second put for the same kind replaces the first.
	require.NoError(t, s.PutAccountSecret(ctx, tenant, "acct1", "imap_password", 5, []byte("sealed-3")))
	version, sealed, err = s.GetAccountSecret(ctx, tenant, "acct1", "imap_password")
	require.NoError(t, err)
	require.Equal(t, 5, version)
	require.Equal(t, []byte("sealed-3"), sealed)

	// The other kind is not touched.
	version, sealed, err = s.GetAccountSecret(ctx, tenant, "acct1", "oauth_refresh")
	require.NoError(t, err)
	require.Equal(t, 4, version)
	require.Equal(t, []byte("sealed-2"), sealed)

	_, _, err = s.GetAccountSecret(ctx, tenant, "acct1", "unknown_kind")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAccountSecretIsScopedByTenant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccount(ctx, testAccount(local, "acct1", testNow)))

	require.NoError(t, s.PutAccountSecret(ctx, local, "acct1", "imap_password", 1, []byte("sealed")))

	// Another tenant cannot read it, even with the right account id.
	_, _, err = s.GetAccountSecret(ctx, "other", "acct1", "imap_password")
	require.ErrorIs(t, err, ErrNotFound)

	// Another tenant cannot attach a secret to the account either.
	require.Error(t, s.PutAccountSecret(ctx, "other", "acct1", "imap_password", 1, []byte("evil")))
	_, sealed, err := s.GetAccountSecret(ctx, local, "acct1", "imap_password")
	require.NoError(t, err)
	require.Equal(t, []byte("sealed"), sealed)

	// A secret needs an existing account.
	require.Error(t, s.PutAccountSecret(ctx, local, "missing", "imap_password", 1, []byte("x")))
}

func TestDeleteAccountSecrets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct2", testNow)))
	require.NoError(t, s.CreateAccount(ctx, testAccount("other", "acct1", testNow)))

	for _, id := range []struct {
		tenant account.TenantID
		acct   account.AccountID
	}{{tenant, "acct1"}, {tenant, "acct2"}, {"other", "acct1"}} {
		require.NoError(t, s.PutAccountSecret(ctx, id.tenant, id.acct, "imap_password", 1, []byte("a")))
		require.NoError(t, s.PutAccountSecret(ctx, id.tenant, id.acct, "oauth_refresh", 1, []byte("b")))
	}

	require.NoError(t, s.DeleteAccountSecrets(ctx, tenant, "acct1"))

	for _, kind := range []string{"imap_password", "oauth_refresh"} {
		_, _, err = s.GetAccountSecret(ctx, tenant, "acct1", kind)
		require.ErrorIs(t, err, ErrNotFound)
		// The same account id in another tenant, and another account of the
		// same tenant, keep their secrets.
		_, _, err = s.GetAccountSecret(ctx, "other", "acct1", kind)
		require.NoError(t, err)
		_, _, err = s.GetAccountSecret(ctx, tenant, "acct2", kind)
		require.NoError(t, err)
	}

	// Deleting again, or for an account without secrets, is not an error.
	require.NoError(t, s.DeleteAccountSecrets(ctx, tenant, "acct1"))
	require.NoError(t, s.DeleteAccountSecrets(ctx, tenant, "missing"))
}

func TestDeleteAccountRemovesSecrets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))
	require.NoError(t, s.PutAccountSecret(ctx, tenant, "acct1", "imap_password", 1, []byte("sealed")))

	require.NoError(t, s.DeleteAccount(ctx, tenant, "acct1"))

	_, _, err = s.GetAccountSecret(ctx, tenant, "acct1", "imap_password")
	require.ErrorIs(t, err, ErrNotFound)
	var rows int
	require.NoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM account_secrets").Scan(&rows))
	require.Zero(t, rows)
}

// DeleteAccountWithSecrets deletes the account and its secrets together. The
// rows of the account in other tables (such as queued messages) go with it.
func TestDeleteAccountWithSecrets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "gone", testNow)))
	require.NoError(t, s.PutAccountSecret(ctx, tenant, "gone", "password", 1, []byte("sealed")))
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "keep", testNow)))
	require.NoError(t, s.PutAccountSecret(ctx, tenant, "keep", "password", 1, []byte("sealed keep")))

	require.ErrorIs(t, s.DeleteAccountWithSecrets(ctx, "other", "gone"), ErrNotFound,
		"another tenant cannot delete the account")
	_, _, err = s.GetAccountSecret(ctx, tenant, "gone", "password")
	require.NoError(t, err, "a refused delete deletes no secret")

	require.NoError(t, s.DeleteAccountWithSecrets(ctx, tenant, "gone"))
	_, err = s.GetAccount(ctx, tenant, "gone")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetAccountSecret(ctx, tenant, "gone", "password")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteAccountWithSecrets(ctx, tenant, "gone"), ErrNotFound)

	_, _, err = s.GetAccountSecret(ctx, tenant, "keep", "password")
	require.NoError(t, err, "the other account keeps its secret")
	require.ErrorIs(t, s.DeleteAccountWithSecrets(ctx, tenant, "a|b"), ErrInvalidID)
}

// When the delete of the account fails, the secrets stay too: the two deletes
// are one transaction.
func TestDeleteAccountWithSecretsIsAllOrNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "acct1", testNow)))
	require.NoError(t, s.PutAccountSecret(ctx, tenant, "acct1", "password", 1, []byte("sealed")))
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER refuse_account_delete BEFORE DELETE ON accounts
		BEGIN SELECT RAISE(ABORT, 'refused by the test'); END`)
	require.NoError(t, err)

	require.Error(t, s.DeleteAccountWithSecrets(ctx, tenant, "acct1"))

	_, err = s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	_, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err, "the secret was not deleted without its account")
	require.Equal(t, []byte("sealed"), sealed)
}

// CheckpointWAL copies the write-ahead log into the database file and cuts the
// log to zero bytes. secure_delete does not clear old frames of the log, so
// deleted data stays there until this runs.
func TestCheckpointWALTruncatesTheLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "a1", testNow)))

	walSize := func() int64 {
		t.Helper()
		info, err := os.Stat(filepath.Join(dir, DBFileName+"-wal"))
		require.NoError(t, err)
		return info.Size()
	}
	require.Positive(t, walSize(), "the writes are in the log")

	complete, err := s.CheckpointWAL(ctx, CheckpointTruncate)
	require.NoError(t, err)
	require.True(t, complete)
	require.Zero(t, walSize())

	got, err := s.GetAccount(ctx, tenant, "a1")
	require.NoError(t, err, "the data is in the database file")
	require.Equal(t, account.AccountID("a1"), got.ID)
}

// A passive checkpoint copies what it can into the database file without
// waiting for other connections, and leaves the size of the log file as it is.
// The program uses it at start-up, where a TRUNCATE checkpoint could hold up
// the writes of other programs for the busy timeout.
func TestCheckpointWALPassiveKeepsTheLogFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAccount(ctx, testAccount(tenant, "a1", testNow)))
	walPath := filepath.Join(dir, DBFileName+"-wal")
	before, err := os.Stat(walPath)
	require.NoError(t, err)

	complete, err := s.CheckpointWAL(ctx, CheckpointPassive)
	require.NoError(t, err)
	require.True(t, complete, "no other connection reads, so every frame is copied")

	after, err := os.Stat(walPath)
	require.NoError(t, err)
	require.Equal(t, before.Size(), after.Size(), "a passive checkpoint does not cut the log")
	require.Positive(t, after.Size())
}

func TestCheckpointWALRefusesAnUnknownMode(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CheckpointWAL(context.Background(), CheckpointMode("RESTART; DROP TABLE accounts"))
	require.Error(t, err)
}
