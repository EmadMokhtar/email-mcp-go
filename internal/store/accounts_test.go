package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

func TestCreateAccountWithSecret(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	a := testAccount(tenant, "acct1", testNow)
	a.Address = "Älice@Example.com"
	require.NoError(t, s.CreateAccountWithSecret(ctx, a, "password", 2, []byte("sealed-1")))

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, a, got)
	version, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, 2, version)
	require.Equal(t, []byte("sealed-1"), sealed)
}

func TestCreateAccountWithSecretRefusesADuplicateAddress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")

	first := testAccount(tenant, "acct1", testNow)
	first.Address = "Älice@Example.com"
	require.NoError(t, s.CreateAccountWithSecret(ctx, first, "password", 1, []byte("sealed-1")))

	// The same address in another case is the same address. The letters outside
	// ASCII count too.
	for i, address := range []string{"Älice@Example.com", "älice@example.com", "ÄLICE@EXAMPLE.COM"} {
		dup := testAccount(tenant, account.AccountID("dup"+strconv.Itoa(i)), testNow)
		dup.Address = address
		err := s.CreateAccountWithSecret(ctx, dup, "password", 1, []byte("sealed-2"))
		require.ErrorIs(t, err, ErrExists, address)
		_, err = s.GetAccount(ctx, tenant, dup.ID)
		require.ErrorIs(t, err, ErrNotFound, "nothing of the refused account is stored")
	}
	_, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, []byte("sealed-1"), sealed, "the first secret is not changed")

	// Another tenant may connect the same address.
	other := testAccount("other", "acct2", testNow)
	other.Address = first.Address
	require.NoError(t, s.CreateAccountWithSecret(ctx, other, "password", 1, []byte("sealed-3")))

	// An account with another address is fine.
	second := testAccount(tenant, "acct3", testNow)
	second.Address = "bob@example.com"
	require.NoError(t, s.CreateAccountWithSecret(ctx, second, "password", 1, []byte("sealed-4")))
}

func TestCreateAccountWithSecretIsAllOrNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// The secret row needs a sealed value (NOT NULL), so its insert fails after
	// the account insert worked. The account must not stay.
	a := testAccount(tenant, "acct1", testNow)
	require.Error(t, s.CreateAccountWithSecret(ctx, a, "password", 1, nil))
	_, err = s.GetAccount(ctx, tenant, "acct1")
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.ErrorIs(t, err, ErrNotFound)

	// An unsafe id or an unknown tenant stores nothing either.
	require.Error(t, s.CreateAccountWithSecret(ctx, testAccount(tenant, "a|b", testNow), "password", 1, []byte("x")))
	require.Error(t, s.CreateAccountWithSecret(ctx, testAccount("ghost", "acct2", testNow), "password", 1, []byte("x")))
	accounts, err := s.ListAccounts(ctx, tenant)
	require.NoError(t, err)
	require.Empty(t, accounts)
}

func TestCreateAccountWithSecretOnceUnderRace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			a := testAccount(tenant, account.AccountID("acct"+strconv.Itoa(i)), testNow)
			a.Address = "same@example.com"
			errs[i] = s.CreateAccountWithSecret(ctx, a, "password", 1, []byte("sealed"))
		})
	}
	wg.Wait()

	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrExists):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 1, created)
	accounts, err := s.ListAccounts(ctx, tenant)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
}

// ReconnectAccount saves the new pins, the new sealed password and the status
// "ok" in one step, and nothing else of the account changes: not its id, its
// levels, its limits or its other server settings.
func TestReconnectAccount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	a := testAccount(tenant, "acct1", testNow)
	a.Mail.PinnedIMAPCert = strings.Repeat("aa", 32)
	a.Mail.PinnedSMTPCert = strings.Repeat("bb", 32)
	a.Levels[account.AreaMail] = account.MailOrganize
	a.Status = account.StatusNeedsReauth
	require.NoError(t, s.CreateAccountWithSecret(ctx, a, "password", 1, []byte("sealed-old")))
	tested := *a.Mail

	// The settings page turns on the Sent copy while the test runs. That is not
	// a change of the tested settings, and the save keeps it.
	changed := *a
	changedMail := *a.Mail
	changedMail.SaveSentCopy = !a.Mail.SaveSentCopy
	changed.Mail = &changedMail
	require.NoError(t, s.UpdateAccount(ctx, &changed))

	newIMAP := strings.Repeat("cc", 32)
	require.NoError(t, s.ReconnectAccount(ctx, tenant, "acct1", AccountReconnect{
		Tested: tested, PinnedIMAPCert: newIMAP, PinnedSMTPCert: tested.PinnedSMTPCert,
		SecretKind: "password", KeyVersion: 2, Sealed: []byte("sealed-new"),
	}))

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	want := changed
	wantMail := changedMail
	wantMail.PinnedIMAPCert = newIMAP
	want.Mail = &wantMail
	want.Status = account.StatusOK
	want.CredentialsVersion = 1
	require.Equal(t, &want, got, "only the pins, the status and the credentials version changed")
	version, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, 2, version)
	require.Equal(t, []byte("sealed-new"), sealed)

	// Without a sealed value, the stored password stays.
	require.NoError(t, s.SetAccountStatus(ctx, tenant, "acct1", account.StatusNeedsReauth))
	require.NoError(t, s.ReconnectAccount(ctx, tenant, "acct1", AccountReconnect{
		Tested: wantMail, PinnedIMAPCert: "", PinnedSMTPCert: wantMail.PinnedSMTPCert, SecretKind: "password",
	}))
	got, err = s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Empty(t, got.Mail.PinnedIMAPCert, "a pin can be removed")
	require.Equal(t, account.StatusOK, got.Status)
	version, sealed, err = s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, 2, version)
	require.Equal(t, []byte("sealed-new"), sealed)
}

// When the stored server settings are not the ones that the reconnect tested
// (another program changed them meanwhile), nothing is saved.
func TestReconnectAccountRefusesChangedSettings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	a := testAccount(tenant, "acct1", testNow)
	a.Mail.PinnedIMAPCert = strings.Repeat("aa", 32)
	a.Status = account.StatusNeedsReauth
	require.NoError(t, s.CreateAccountWithSecret(ctx, a, "password", 1, []byte("sealed-old")))
	before, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)

	for name, change := range map[string]func(*account.MailServer){
		"another pin":  func(m *account.MailServer) { m.PinnedIMAPCert = strings.Repeat("dd", 32) },
		"another host": func(m *account.MailServer) { m.IMAPHost = "imap.other.example.com" },
		"another port": func(m *account.MailServer) { m.SMTPPort++ },
		"another user": func(m *account.MailServer) { m.Username = "someone" },
	} {
		tested := *a.Mail
		change(&tested)
		err := s.ReconnectAccount(ctx, tenant, "acct1", AccountReconnect{
			Tested: tested, PinnedIMAPCert: strings.Repeat("cc", 32), SecretKind: "password", KeyVersion: 2, Sealed: []byte("sealed-new"),
		})
		require.ErrorIs(t, err, ErrConflict, name)
	}
	for name, id := range map[string]struct {
		tenant account.TenantID
		acct   account.AccountID
	}{"another tenant": {"other", "acct1"}, "missing": {tenant, "missing"}} {
		err := s.ReconnectAccount(ctx, id.tenant, id.acct, AccountReconnect{
			Tested: *a.Mail, SecretKind: "password", KeyVersion: 2, Sealed: []byte("sealed-new"),
		})
		require.ErrorIs(t, err, ErrNotFound, name)
	}
	require.ErrorIs(t, s.ReconnectAccount(ctx, tenant, "a|b", AccountReconnect{Tested: *a.Mail, SecretKind: "password"}), ErrInvalidID)

	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, before, got, "nothing changed")
	_, sealed, err := s.GetAccountSecret(ctx, tenant, "acct1", "password")
	require.NoError(t, err)
	require.Equal(t, []byte("sealed-old"), sealed)
}

// A settings save that read the account before a reconnect must not write back
// the old pins, and must not bring back a pin that the reconnect removed:
// UpdateAccount keeps the pins that are stored now. Only CreateAccount and
// ReconnectAccount write pins.
func TestUpdateAccountKeepsThePinsStoredNow(t *testing.T) {
	for name, newPin := range map[string]string{"a new pin": strings.Repeat("cc", 32), "a removed pin": ""} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			tenant, err := s.EnsureLocalTenant(ctx)
			require.NoError(t, err)
			a := testAccount(tenant, "acct1", testNow)
			a.Mail.PinnedIMAPCert = strings.Repeat("aa", 32)
			a.Mail.PinnedSMTPCert = strings.Repeat("bb", 32)
			require.NoError(t, s.CreateAccountWithSecret(ctx, a, "password", 1, []byte("sealed")))

			old, err := s.GetAccount(ctx, tenant, "acct1")
			require.NoError(t, err)
			require.NoError(t, s.ReconnectAccount(ctx, tenant, "acct1", AccountReconnect{
				Tested: *old.Mail, PinnedIMAPCert: newPin, PinnedSMTPCert: old.Mail.PinnedSMTPCert, SecretKind: "password",
			}))

			old.Limits.SendsPerHour = 7
			old.Mail.SaveSentCopy = !old.Mail.SaveSentCopy
			require.NoError(t, s.UpdateAccount(ctx, old))

			got, err := s.GetAccount(ctx, tenant, "acct1")
			require.NoError(t, err)
			require.Equal(t, 7, got.Limits.SendsPerHour, "the setting is saved")
			require.Equal(t, old.Mail.SaveSentCopy, got.Mail.SaveSentCopy, "the setting is saved")
			require.Equal(t, newPin, got.Mail.PinnedIMAPCert, "the pin of the reconnect stays")
			require.Equal(t, strings.Repeat("bb", 32), got.Mail.PinnedSMTPCert)
		})
	}
}

// MarkNeedsReauth sets needs_reauth only while the credentials version is the
// one that the caller read: a refused login of a call that started before a
// reconnect does not undo the reconnect. The check and the write are one
// statement. ReconnectAccount raises the version, and UpdateAccount never
// writes it.
func TestMarkNeedsReauthOnlyForTheCredentialsThatTheCallRead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "other")
	require.NoError(t, s.CreateAccountWithSecret(ctx, testAccount(tenant, "acct1", testNow), "password", 1, []byte("sealed")))
	read, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Zero(t, read.CredentialsVersion)

	// A reconnect after the read: the call's version is old.
	require.NoError(t, s.ReconnectAccount(ctx, tenant, "acct1", AccountReconnect{
		Tested: *read.Mail, SecretKind: "password", KeyVersion: 1, Sealed: []byte("sealed-new"),
	}))
	marked, err := s.MarkNeedsReauth(ctx, tenant, "acct1", read.CredentialsVersion)
	require.NoError(t, err)
	require.False(t, marked)
	got, err := s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, account.StatusOK, got.Status, "the reconnect stays")
	require.Equal(t, 1, got.CredentialsVersion)

	// UpdateAccount does not write the version.
	got.CredentialsVersion = 99
	require.NoError(t, s.UpdateAccount(ctx, got))
	got, err = s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, 1, got.CredentialsVersion)

	// With the version of now, the mark is written.
	marked, err = s.MarkNeedsReauth(ctx, tenant, "acct1", got.CredentialsVersion)
	require.NoError(t, err)
	require.True(t, marked)
	got, err = s.GetAccount(ctx, tenant, "acct1")
	require.NoError(t, err)
	require.Equal(t, account.StatusNeedsReauth, got.Status)

	marked, err = s.MarkNeedsReauth(ctx, "other", "acct1", 1)
	require.NoError(t, err)
	require.False(t, marked, "another tenant cannot mark it")
	_, err = s.MarkNeedsReauth(ctx, tenant, "a|b", 1)
	require.ErrorIs(t, err, ErrInvalidID)
}
