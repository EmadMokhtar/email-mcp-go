package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

func mailQuery() mail.Query { return mail.Query{Folder: "@inbox"} }

func bodyOptions() mail.BodyOptions { return mail.BodyOptions{Format: mail.BodyText, MaxChars: 1000} }

// readUses lists every read use case of one account, so that a test can check
// them all with the same account and the same expectation.
func readUses(svc *Service, id account.AccountID) map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"ListFolders": func(ctx context.Context) error { _, err := svc.ListFolders(ctx, id); return err },
		"ListTags":    func(ctx context.Context) error { _, _, err := svc.ListTags(ctx, id); return err },
		"Search":      func(ctx context.Context) error { _, err := svc.Search(ctx, id, mailQuery(), ""); return err },
		"NewMessages": func(ctx context.Context) error { _, _, _, err := svc.NewMessages(ctx, id, "@inbox", ""); return err },
		"GetMessage":  func(ctx context.Context) error { _, err := svc.GetMessage(ctx, id, "ref", bodyOptions()); return err },
		"GetAttachment": func(ctx context.Context) error {
			_, err := svc.GetAttachment(ctx, id, "ref", "2")
			return err
		},
	}
}

func TestAccountOfOtherTenantNotFound(t *testing.T) {
	f := newFixture(t)
	f.addTenant("other")
	theirs := f.addAccount("other", "them@example.com", offlineSettings(), "pw")
	backends := &fakeFactory{backend: &fakeBackend{}}
	svc := f.newService(backends, &fakeLinks{})

	// An account that exists in no tenant gives the same answer.
	_, missing := svc.ListFolders(f.ctx(), missingAccountID())
	require.ErrorIs(t, missing, ErrAccountNotFound)

	for name, use := range readUses(svc, theirs.ID) {
		t.Run(name, func(t *testing.T) {
			err := use(f.ctx())
			require.ErrorIs(t, err, ErrAccountNotFound)
			require.Equal(t, missing.Error(), err.Error(), "an account of another tenant must look like a missing account")
		})
	}
	require.Zero(t, backends.calls(), "the account of another tenant must not reach a backend")
}

func TestReadRefusedAtLevel0(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Levels[account.AreaMail] = account.MailOff
	})
	backends := &fakeFactory{backend: &fakeBackend{}}
	links := &fakeLinks{}
	svc := f.newService(backends, links)

	for name, use := range readUses(svc, a.ID) {
		t.Run(name, func(t *testing.T) {
			err := use(f.ctx())

			var perm *PermissionError
			require.ErrorAs(t, err, &perm)
			require.False(t, perm.Decision.Allowed)
			require.False(t, perm.TokenLimited, "the account level is the problem, not the token")
			require.Equal(t, account.MailRead, perm.Decision.Required)
			require.Equal(t, account.MailOff, perm.Decision.Current)
			require.NotEmpty(t, perm.Link)
			require.NotContains(t, perm.Link, "token=")
			require.Contains(t, err.Error(), perm.Decision.Reason)
			require.Contains(t, err.Error(), perm.Link)
		})
	}
	require.Zero(t, backends.calls(), "a refused read must not reach a backend")
	for _, c := range links.settingsCalls() {
		require.Equal(t, settingsCall{ID: a.ID, Area: account.AreaMail, Level: account.MailRead}, c,
			"the link must ask for the level that the action needs")
	}
}

// If the link cannot be built, the refusal still stands. It just has no link.
func TestPermissionErrorWithoutLink(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Levels[account.AreaMail] = account.MailOff
	})
	backends := &fakeFactory{backend: &fakeBackend{}}

	cases := map[string]Links{
		"links fail":   &fakeLinks{err: errors.New("no base address")},
		"no links set": nil,
	}
	for name, links := range cases {
		t.Run(name, func(t *testing.T) {
			svc := f.newService(backends, links)

			_, err := svc.ListFolders(f.ctx(), a.ID)

			var perm *PermissionError
			require.ErrorAs(t, err, &perm)
			require.False(t, perm.Decision.Allowed)
			require.Empty(t, perm.Link)
			require.Contains(t, err.Error(), perm.Decision.Reason)
			require.NotContains(t, err.Error(), "http")
		})
	}
	require.Zero(t, backends.calls())
}

// The token scopes can lower the level below the level of the account.
func TestReadRefusedByTokenScopes(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw") // level Send
	backends := &fakeFactory{backend: &fakeBackend{}}
	svc := f.newService(backends, &fakeLinks{})
	ctxWith := func(scopes policy.Scopes) context.Context {
		return WithPrincipal(context.Background(), Principal{Tenant: f.tenant, Scopes: scopes})
	}

	t.Run("no scopes allow nothing", func(t *testing.T) {
		_, err := svc.ListFolders(ctxWith(policy.Scopes{}), a.ID)
		var perm *PermissionError
		require.ErrorAs(t, err, &perm)
		require.Equal(t, account.MailOff, perm.Decision.Current)
	})
	t.Run("a higher scope also allows reading", func(t *testing.T) {
		_, err := svc.ListFolders(ctxWith(policy.Scopes{"email:send"}), a.ID)
		require.NoError(t, err)
	})
	t.Run("an unknown scope allows nothing", func(t *testing.T) {
		_, err := svc.ListFolders(ctxWith(policy.Scopes{"calendar:read"}), a.ID)
		var perm *PermissionError
		require.ErrorAs(t, err, &perm)
	})
	t.Run("the read scope allows reading", func(t *testing.T) {
		_, err := svc.ListFolders(ctxWith(policy.Scopes{"email:read"}), a.ID)
		require.NoError(t, err)
	})
}

func TestNeedsReauthRefusedBeforeBackend(t *testing.T) {
	f := newFixture(t)
	// Level Organize (2) is neither Off nor the level that reading needs (1), so
	// the test can tell which level the link asks for.
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Status = account.StatusNeedsReauth
		a.Levels[account.AreaMail] = account.MailOrganize
	})
	backends := &fakeFactory{backend: &fakeBackend{}}

	t.Run("with a link", func(t *testing.T) {
		links := &fakeLinks{}
		svc := f.newService(backends, links)
		for name, use := range readUses(svc, a.ID) {
			t.Run(name, func(t *testing.T) {
				err := use(f.ctx())
				require.ErrorIs(t, err, ErrNeedsReauth)
				require.Contains(t, err.Error(), "reconnect")
				require.Contains(t, err.Error(), "https://mcp.test/settings/"+string(a.ID))
				require.Contains(t, err.Error(), fmt.Sprintf("level=%d", int(account.MailOrganize)),
					"the link asks for the level the account has now")
				require.NotContains(t, err.Error(), "token=")
			})
		}
		for _, c := range links.settingsCalls() {
			require.Equal(t, settingsCall{ID: a.ID, Area: account.AreaMail, Level: account.MailOrganize}, c)
		}
	})
	t.Run("without a link", func(t *testing.T) {
		svc := f.newService(backends, &fakeLinks{err: errors.New("no base address")})
		_, err := svc.ListFolders(f.ctx(), a.ID)
		require.ErrorIs(t, err, ErrNeedsReauth)
		require.Contains(t, err.Error(), "reconnect")
		require.NotContains(t, err.Error(), "http")
	})
	require.Zero(t, backends.calls(), "an account that needs to be reconnected must not reach a backend")
}

// A caller that may not read the account is told about the permission first.
func TestPermissionErrorComesBeforeNeedsReauth(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Status = account.StatusNeedsReauth
		a.Levels[account.AreaMail] = account.MailOff
	})
	svc := f.newService(&fakeFactory{backend: &fakeBackend{}}, &fakeLinks{})

	_, err := svc.ListFolders(f.ctx(), a.ID)

	var perm *PermissionError
	require.ErrorAs(t, err, &perm)
	require.NotErrorIs(t, err, ErrNeedsReauth)
}

func TestListAccountsOnlyOwnTenant(t *testing.T) {
	f := newFixture(t)
	f.addTenant("other")
	mine1 := f.addAccount(f.tenant, "one@example.com", offlineSettings(), "very-secret-password")
	mine2 := f.addAccount(f.tenant, "two@example.com", offlineSettings(), "very-secret-password", func(a *account.Account) {
		a.Levels[account.AreaMail] = account.MailRead
		a.Status = account.StatusNeedsReauth
		a.CreatedAt = a.CreatedAt.Add(time.Second) // the store keeps milliseconds and sorts by time
	})
	theirs := f.addAccount("other", "them@example.com", offlineSettings(), "pw")
	svc := f.newService(&fakeFactory{backend: &fakeBackend{}}, &fakeLinks{})

	views, err := svc.ListAccounts(f.ctx())

	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, mine1.ID, views[0].ID)
	require.Equal(t, "one@example.com", views[0].Address)
	require.Equal(t, string(account.ProviderIMAP), views[0].Provider)
	require.Equal(t, map[account.Area]string{account.AreaMail: "Send"}, views[0].Areas)
	require.Equal(t, account.StatusOK, views[0].Status)
	require.Equal(t, mine2.ID, views[1].ID)
	require.Equal(t, map[account.Area]string{account.AreaMail: "Read"}, views[1].Areas)
	require.Equal(t, account.StatusNeedsReauth, views[1].Status)
	for _, v := range views {
		require.NotEqual(t, theirs.ID, v.ID)
	}

	// The view must not hold a password or a server setting.
	text := fmt.Sprintf("%+v", views)
	require.NotContains(t, text, "very-secret-password")
	require.NotContains(t, text, "imap.example.com")
	require.NotContains(t, text, "user@example.com")
}

func TestListAccountsWithoutAccountsIsEmptyNotNil(t *testing.T) {
	f := newFixture(t)
	svc := f.newService(&fakeFactory{backend: &fakeBackend{}}, &fakeLinks{})

	views, err := svc.ListAccounts(f.ctx())

	require.NoError(t, err)
	require.NotNil(t, views)
	require.Empty(t, views)
}

func TestListAccountsCapabilities(t *testing.T) {
	f := newFixture(t)
	readable := f.addAccount(f.tenant, "a@example.com", offlineSettings(), "pw")
	off := f.addAccount(f.tenant, "b@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Levels[account.AreaMail] = account.MailOff
		a.CreatedAt = a.CreatedAt.Add(time.Second) // the store keeps milliseconds and sorts by time
	})
	reauth := f.addAccount(f.tenant, "c@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Status = account.StatusNeedsReauth
		a.CreatedAt = a.CreatedAt.Add(2 * time.Second)
	})
	want := mail.Capabilities{Tags: true, MaxBatch: 500}

	t.Run("only a readable account is asked", func(t *testing.T) {
		backends := &fakeFactory{backend: &fakeBackend{caps: want}}
		svc := f.newService(backends, &fakeLinks{})

		views, err := svc.ListAccounts(f.ctx())

		require.NoError(t, err)
		require.Len(t, views, 3)
		require.Equal(t, readable.ID, views[0].ID)
		require.Equal(t, want, views[0].Capabilities)
		require.Equal(t, off.ID, views[1].ID)
		require.Equal(t, mail.Capabilities{}, views[1].Capabilities)
		require.Equal(t, reauth.ID, views[2].ID)
		require.Equal(t, mail.Capabilities{}, views[2].Capabilities)
		require.Equal(t, 1, backends.calls(), "only the readable account may open a backend")
	})
	t.Run("a backend that cannot be set up does not fail the list", func(t *testing.T) {
		backends := &fakeFactory{err: errors.New("no stored password")}
		svc := f.newService(backends, &fakeLinks{})

		views, err := svc.ListAccounts(f.ctx())

		require.NoError(t, err)
		require.Len(t, views, 3)
		require.Equal(t, mail.Capabilities{}, views[0].Capabilities)
	})
	t.Run("the token scopes decide what may be asked", func(t *testing.T) {
		backends := &fakeFactory{backend: &fakeBackend{caps: want}}
		svc := f.newService(backends, &fakeLinks{})
		ctx := WithPrincipal(context.Background(), Principal{Tenant: f.tenant, Scopes: policy.Scopes{}})

		views, err := svc.ListAccounts(ctx)

		require.NoError(t, err)
		require.Len(t, views, 3, "the list itself needs no permission")
		require.Zero(t, backends.calls())
	})
}

// When the token scopes lower the level, a link to the settings is wrong
// advice: the user cannot fix it there. The error says that the token does not
// allow the action.
func TestReadRefusedByTokenScopesHasNoSettingsLink(t *testing.T) {
	f := newFixture(t)
	sendLevel := f.addAccount(f.tenant, "a@example.com", offlineSettings(), "pw") // level Send
	off := f.addAccount(f.tenant, "b@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Levels[account.AreaMail] = account.MailOff
	})
	backends := &fakeFactory{backend: &fakeBackend{}}
	links := &fakeLinks{}
	svc := f.newService(backends, links)
	ctxWith := func(scopes policy.Scopes) context.Context {
		return WithPrincipal(context.Background(), Principal{Tenant: f.tenant, Scopes: scopes})
	}

	t.Run("the token is the problem", func(t *testing.T) {
		for name, scopes := range map[string]policy.Scopes{
			"no scopes":     {},
			"unknown scope": {"calendar:read"},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := svc.ListFolders(ctxWith(scopes), sendLevel.ID)

				var perm *PermissionError
				require.ErrorAs(t, err, &perm)
				require.True(t, perm.TokenLimited)
				require.Empty(t, perm.Link)
				require.Contains(t, err.Error(), perm.Decision.Reason)
				require.Contains(t, err.Error(), "access token does not allow")
				require.NotContains(t, err.Error(), "settings")
				require.NotContains(t, err.Error(), "http")
			})
		}
		require.Empty(t, links.settingsCalls(), "no link may be built when the settings cannot help")
	})
	t.Run("the account level is the problem, even with scopes", func(t *testing.T) {
		_, err := svc.ListFolders(ctxWith(policy.Scopes{"email:read"}), off.ID)

		var perm *PermissionError
		require.ErrorAs(t, err, &perm)
		require.False(t, perm.TokenLimited)
		require.NotEmpty(t, perm.Link)
		require.NotContains(t, err.Error(), "access token")
	})
	require.Zero(t, backends.calls())
}

// TokenLimited is set only when the token scopes really lowered the level and
// the level of the account would have been enough.
func TestPermissionErrorTokenLimited(t *testing.T) {
	f := newFixture(t)
	svc := f.newService(&fakeFactory{}, &fakeLinks{})
	tests := []struct {
		name   string
		level  account.Level
		action policy.Action
		scopes policy.Scopes
		want   bool
	}{
		{"scopes lowered the level below what the action needs", account.MailSend, policy.ActRead, policy.Scopes{}, true},
		{"the account level is too low, with scopes that would allow it", account.MailOff, policy.ActRead, policy.Scopes{"email:read"}, false},
		{"the account level is too low, without scopes", account.MailRead, policy.ActSend, nil, false},
		{"an action that is not known, at the highest level, without scopes", account.MailDelete, policy.Action("bogus"), nil, false},
		{"a damaged account level counts as Off", account.Level(9), policy.ActRead, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &account.Account{
				ID: "acct", TenantID: f.tenant, Address: "me@example.com",
				Levels: map[account.Area]account.Level{account.AreaMail: tt.level},
			}
			d := policy.Decide(a, tt.action, tt.scopes)
			require.False(t, d.Allowed, "the test needs a refused decision")

			err := svc.permissionError(f.ctx(), a, d)

			require.Equal(t, tt.want, err.TokenLimited)
			if tt.want {
				require.Empty(t, err.Link)
				require.Contains(t, err.Error(), "access token does not allow")
			} else {
				require.NotEmpty(t, err.Link)
				require.NotContains(t, err.Error(), "access token")
			}
		})
	}
}

// listWithin runs ListAccounts and fails the test when it does not return
// within the limit. It stops a test that would hang for ever.
func listWithin(t *testing.T, svc *Service, ctx context.Context, limit time.Duration) []AccountView {
	t.Helper()
	type result struct {
		views []AccountView
		err   error
	}
	done := make(chan result, 1)
	go func() {
		views, err := svc.ListAccounts(ctx)
		done <- result{views, err}
	}()
	select {
	case r := <-done:
		require.NoError(t, r.err)
		return r.views
	case <-time.After(limit):
		require.FailNow(t, "ListAccounts did not return in time")
		return nil
	}
}

// One slow mail server must not slow down the whole list.
func TestListAccountsDoesNotWaitForASlowBackend(t *testing.T) {
	f := newFixture(t)
	first := f.addAccount(f.tenant, "a@example.com", offlineSettings(), "pw")
	slow := f.addAccount(f.tenant, "b@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.CreatedAt = a.CreatedAt.Add(time.Second)
	})
	last := f.addAccount(f.tenant, "c@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.CreatedAt = a.CreatedAt.Add(2 * time.Second)
	})
	want := mail.Capabilities{Tags: true, MaxBatch: 500}

	// newBackends makes a factory in which the second account uses slowBackend.
	newBackends := func(slowBackend *fakeBackend) *fakeFactory {
		return &fakeFactory{
			backend:   &fakeBackend{caps: want},
			byAccount: map[account.AccountID]mail.Backend{slow.ID: slowBackend},
		}
	}
	// check looks at the views: the fast accounts have answers, the slow one has
	// none.
	check := func(t *testing.T, views []AccountView) {
		t.Helper()
		require.Len(t, views, 3)
		require.Equal(t, []account.AccountID{first.ID, slow.ID, last.ID}, []account.AccountID{views[0].ID, views[1].ID, views[2].ID})
		require.Equal(t, want, views[0].Capabilities)
		require.Equal(t, mail.Capabilities{}, views[1].Capabilities, "an account that did not answer keeps empty capabilities")
		require.Equal(t, want, views[2].Capabilities)
	}

	t.Run("the budget ends the wait for a backend that ignores the context", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		svc := f.newService(newBackends(&fakeBackend{capsStuck: release}), &fakeLinks{})
		svc.capabilitiesBudget = 200 * time.Millisecond

		start := time.Now()
		views := listWithin(t, svc, f.ctx(), 5*time.Second)

		require.Less(t, time.Since(start), 3*time.Second)
		check(t, views)
	})

	t.Run("the budget ends the wait for a backend that honours the context", func(t *testing.T) {
		blocker := &fakeBackend{capsBlock: true}
		svc := f.newService(newBackends(blocker), &fakeLinks{})
		svc.capabilitiesBudget = 200 * time.Millisecond

		views := listWithin(t, svc, f.ctx(), 5*time.Second)

		check(t, views)
		// The end of the budget also ends the question to the backend.
		require.Eventually(t, func() bool { return blocker.capsRunning.Load() == 0 }, 2*time.Second, 5*time.Millisecond)
	})

	t.Run("a cancel by the caller ends the wait at once", func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		stuck := &fakeBackend{capsStuck: release, capsEntered: make(chan struct{}, 8)}
		svc := f.newService(newBackends(stuck), &fakeLinks{})
		// The default budget (5 seconds) stays: only the cancel can end the wait
		// within the limit of this test.
		ctx, cancel := context.WithCancel(f.ctx())
		defer cancel()

		type result struct {
			views []AccountView
			err   error
		}
		done := make(chan result, 1)
		go func() {
			views, err := svc.ListAccounts(ctx)
			done <- result{views, err}
		}()
		select {
		case <-stuck.capsEntered:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the slow backend was never asked")
		}
		start := time.Now()
		cancel()

		select {
		case r := <-done:
			require.NoError(t, r.err, "a cancel is not an error: the answers that came are returned")
			require.Less(t, time.Since(start), 2*time.Second)
			// The test cancels as soon as the slow backend is asked. The fast
			// accounts may or may not have answered by then, so this test does not
			// look at their capabilities.
			// TestListAccountsKeepsTheAnswersThatCameBeforeTheCancel does.
			require.Len(t, r.views, 3)
			require.Equal(t, []account.AccountID{first.ID, slow.ID, last.ID},
				[]account.AccountID{r.views[0].ID, r.views[1].ID, r.views[2].ID})
			require.Equal(t, mail.Capabilities{}, r.views[1].Capabilities, "the slow account never answered")
		case <-time.After(3 * time.Second):
			require.FailNow(t, "ListAccounts did not return after the caller cancelled")
		}
	})

	t.Run("without a slow backend nothing is lost", func(t *testing.T) {
		svc := f.newService(newBackends(&fakeBackend{caps: want}), &fakeLinks{})

		views := listWithin(t, svc, f.ctx(), 5*time.Second)

		require.Len(t, views, 3)
		for _, v := range views {
			require.Equal(t, want, v.Capabilities)
		}
	})
}

// The answers that came before the cancel are kept. The test makes sure that
// every fast answer waits in the channel of ListAccounts when the cancel comes,
// so the collector sees the cancel and the answers at the same time. It may
// take the cancel first. It must still return every answer.
func TestListAccountsKeepsTheAnswersThatCameBeforeTheCancel(t *testing.T) {
	f := newFixture(t)
	const total, slowIndex = 13, 5
	accounts := make([]*account.Account, total)
	for i := range total {
		accounts[i] = f.addAccount(f.tenant, fmt.Sprintf("user%d@example.com", i), offlineSettings(), "pw", func(a *account.Account) {
			a.CreatedAt = a.CreatedAt.Add(time.Duration(i) * time.Second) // the store sorts by time
		})
	}
	want := mail.Capabilities{Tags: true, MaxBatch: 500}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	backends := &fakeFactory{
		backend:   &fakeBackend{caps: want},
		byAccount: map[account.AccountID]mail.Backend{accounts[slowIndex].ID: &fakeBackend{capsStuck: release}},
	}
	svc := f.newService(backends, &fakeLinks{})
	ctx, cancel := context.WithCancel(f.ctx())
	defer cancel()
	svc.beforeCollect = func(ready func() int) {
		// Wait until all the fast answers are in the channel, then cancel.
		for deadline := time.Now().Add(5 * time.Second); ready() < total-1 && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}

	start := time.Now()
	views, err := svc.ListAccounts(ctx)

	require.NoError(t, err)
	require.Less(t, time.Since(start), 2*time.Second, "the cancel must end the wait for the slow account")
	require.Len(t, views, total)
	for i, v := range views {
		require.Equal(t, accounts[i].ID, v.ID)
		if i == slowIndex {
			require.Equal(t, mail.Capabilities{}, v.Capabilities, "the slow account never answered")
			continue
		}
		require.Equal(t, want, v.Capabilities, "account %d answered before the cancel", i)
	}
}

func TestListAccountsAsksAtMostEightBackendsAtOnce(t *testing.T) {
	f := newFixture(t)
	const accounts = 20
	for i := range accounts {
		f.addAccount(f.tenant, fmt.Sprintf("user%d@example.com", i), offlineSettings(), "pw")
	}
	want := mail.Capabilities{Tags: true, MaxBatch: 500}
	shared := &fakeBackend{caps: want, capsDelay: 20 * time.Millisecond}
	svc := f.newService(&fakeFactory{backend: shared}, &fakeLinks{})

	views := listWithin(t, svc, f.ctx(), 10*time.Second)

	require.Len(t, views, accounts)
	for _, v := range views {
		require.Equal(t, want, v.Capabilities)
	}
	require.EqualValues(t, 8, maxCapabilityCalls)
	require.LessOrEqual(t, int(shared.capsPeak.Load()), maxCapabilityCalls)
	require.Greater(t, int(shared.capsPeak.Load()), 1, "the questions must run at the same time")
}

// The error of a failed backend setup must carry one "service:" prefix, not two.
func TestBackendSetupErrorHasOneServicePrefix(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")
	require.NoError(t, f.store.DeleteAccountSecrets(context.Background(), a.TenantID, a.ID))
	svc := f.newService(f.newCache(), &fakeLinks{})

	_, err := svc.ListFolders(f.ctx(), a.ID)

	require.Error(t, err)
	require.Equal(t, 1, strings.Count(err.Error(), "service:"), err.Error())
}

func TestListTags(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")

	t.Run("supported", func(t *testing.T) {
		fb := &fakeBackend{tags: []mail.Tag{{Name: "work"}, {Name: "home"}}}
		svc := f.newService(&fakeFactory{backend: fb}, &fakeLinks{})

		tags, supported, err := svc.ListTags(f.ctx(), a.ID)

		require.NoError(t, err)
		require.True(t, supported)
		require.Equal(t, []mail.Tag{{Name: "work"}, {Name: "home"}}, tags)
	})
	t.Run("not supported is not an error", func(t *testing.T) {
		fb := &fakeBackend{tagsErr: fmt.Errorf("%w: no keywords", mail.ErrUnsupported)}
		svc := f.newService(&fakeFactory{backend: fb}, &fakeLinks{})

		tags, supported, err := svc.ListTags(f.ctx(), a.ID)

		require.NoError(t, err)
		require.False(t, supported)
		require.Empty(t, tags)
	})
	t.Run("another error is returned", func(t *testing.T) {
		boom := errors.New("server down")
		fb := &fakeBackend{tagsErr: boom}
		svc := f.newService(&fakeFactory{backend: fb}, &fakeLinks{})

		_, supported, err := svc.ListTags(f.ctx(), a.ID)

		require.ErrorIs(t, err, boom)
		require.False(t, supported)
	})
}

func TestGetAttachmentCapsAtTenMegabytes(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")

	t.Run("passes the limit to the backend", func(t *testing.T) {
		fb := &fakeBackend{}
		svc := f.newService(&fakeFactory{backend: fb}, &fakeLinks{})

		att, err := svc.GetAttachment(f.ctx(), a.ID, "ref", "2")

		require.NoError(t, err)
		require.NotNil(t, att)
		require.Equal(t, int64(10<<20), fb.attBytes)
	})
	t.Run("returns the error of the backend as it is", func(t *testing.T) {
		tooLarge := fmt.Errorf("%w: the attachment is larger than %d bytes", mail.ErrTooLarge, 10<<20)
		fb := &fakeBackend{attErr: tooLarge}
		svc := f.newService(&fakeFactory{backend: fb}, &fakeLinks{})

		att, err := svc.GetAttachment(f.ctx(), a.ID, "ref", "2")

		require.Nil(t, att)
		require.Equal(t, tooLarge, err, "the service must not wrap or replace the error")
		require.ErrorIs(t, err, mail.ErrTooLarge)
	})
}

func TestBackendSetupErrorIsReturned(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")
	boom := errors.New("the account has no stored password")
	svc := f.newService(&fakeFactory{err: boom}, &fakeLinks{})

	for name, use := range readUses(svc, a.ID) {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, use(f.ctx()), boom)
		})
	}
}

func TestNewPanicsWhenWiringIsIncomplete(t *testing.T) {
	f := newFixture(t)
	require.Panics(t, func() { New(Deps{Backends: &fakeFactory{}}) })
	require.Panics(t, func() { New(Deps{Store: f.store}) })
	require.NotPanics(t, func() { New(Deps{Store: f.store, Backends: &fakeFactory{}}) })
}

// ---------------------------------------------------------------- end to end

// attachmentMail returns a message with a text part and one attachment.
func attachmentMail(subject, attachmentText string) []byte {
	return crlf("From: Sender <sender@example.com>\n" +
		"To: Me <me@example.com>\n" +
		"Subject: " + subject + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\n" +
		"\n" +
		"--B\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" +
		"See the attached file.\n" +
		"--B\n" +
		"Content-Type: text/csv; name=\"data.csv\"\n" +
		"Content-Disposition: attachment; filename=\"data.csv\"\n" +
		"Content-Transfer-Encoding: base64\n" +
		"\n" +
		base64.StdEncoding.EncodeToString([]byte(attachmentText)) + "\n" +
		"--B--\n")
}

// TestSearchThroughService runs the read use cases through the real store, the
// real vault, the default backend factory and a real IMAP server. The password
// travels the whole way: sealed in the store, decrypted by the factory, used to
// log in.
func TestSearchThroughService(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	srv.Append("INBOX", plainMail("Invoice for March", "Please pay the invoice."))
	srv.Append("INBOX", plainMail("Lunch plans", "Pizza on Friday?"))
	srv.Append("INBOX", attachmentMail("Quarterly data", "a,b\n1,2\n"))
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	svc := f.newService(f.newCache(), &fakeLinks{})
	ctx := f.ctx()

	t.Run("search by subject", func(t *testing.T) {
		page, err := svc.Search(ctx, a.ID, mail.Query{Folder: "@inbox", Subject: "Invoice"}, "")

		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Equal(t, "Invoice for March", page.Items[0].Subject)
		require.False(t, page.Items[0].Seen)
	})

	t.Run("pages hold every message once", func(t *testing.T) {
		first, err := svc.Search(ctx, a.ID, mail.Query{Folder: "@inbox", Limit: 2}, "")
		require.NoError(t, err)
		require.Len(t, first.Items, 2)
		require.NotEmpty(t, first.Next)

		second, err := svc.Search(ctx, a.ID, mail.Query{Folder: "@inbox", Limit: 2}, first.Next)
		require.NoError(t, err)
		require.Len(t, second.Items, 1)
		require.Empty(t, second.Next)

		seen := map[string]bool{}
		for _, it := range append(first.Items, second.Items...) {
			seen[it.Subject] = true
		}
		require.Equal(t, map[string]bool{"Invoice for March": true, "Lunch plans": true, "Quarterly data": true}, seen)
	})

	t.Run("get a message without marking it read", func(t *testing.T) {
		page, err := svc.Search(ctx, a.ID, mail.Query{Folder: "@inbox", Subject: "Lunch"}, "")
		require.NoError(t, err)
		require.Len(t, page.Items, 1)

		msg, err := svc.GetMessage(ctx, a.ID, page.Items[0].Ref, bodyOptions())

		require.NoError(t, err)
		require.Contains(t, msg.Text, "Pizza on Friday?")
		require.Equal(t, 3, srv.Unseen("INBOX"), "reading must not set \\Seen")
	})

	t.Run("get an attachment", func(t *testing.T) {
		page, err := svc.Search(ctx, a.ID, mail.Query{Folder: "@inbox", Subject: "Quarterly"}, "")
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		msg, err := svc.GetMessage(ctx, a.ID, page.Items[0].Ref, bodyOptions())
		require.NoError(t, err)
		require.Len(t, msg.Attachments, 1)

		att, err := svc.GetAttachment(ctx, a.ID, page.Items[0].Ref, msg.Attachments[0].ID)

		require.NoError(t, err)
		require.Equal(t, "data.csv", att.Filename)
		require.Equal(t, "a,b\n1,2\n", string(att.Data))
	})

	t.Run("list folders", func(t *testing.T) {
		folders, err := svc.ListFolders(ctx, a.ID)

		require.NoError(t, err)
		var names []string
		for _, fo := range folders {
			names = append(names, fo.Name)
		}
		require.Contains(t, names, "INBOX")
	})

	t.Run("new messages", func(t *testing.T) {
		none, cursor, reset, err := svc.NewMessages(ctx, a.ID, "@inbox", "")
		require.NoError(t, err)
		require.Empty(t, none, "the first call only starts tracking")
		require.False(t, reset)
		require.NotEmpty(t, cursor)

		srv.Append("INBOX", plainMail("Fresh mail", "Hello"))
		got, _, _, err := svc.NewMessages(ctx, a.ID, "@inbox", cursor)

		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "Fresh mail", got[0].Subject)
	})

	t.Run("list tags", func(t *testing.T) {
		tags, supported, err := svc.ListTags(ctx, a.ID)

		require.NoError(t, err)
		require.True(t, supported, "the test server lets clients make new keywords")
		require.Empty(t, tags)
	})

	t.Run("list accounts shows capabilities", func(t *testing.T) {
		views, err := svc.ListAccounts(ctx)

		require.NoError(t, err)
		require.Len(t, views, 1)
		require.Equal(t, a.ID, views[0].ID)
		require.True(t, views[0].Capabilities.Tags)
		require.NotZero(t, views[0].Capabilities.MaxBatch)
	})
}

// ListAccounts is the first call to a new backend here. The backend does not
// know about tags yet, so it must ask the server. MaxBatch is also reported for
// a server that cannot be reached, so only Tags shows that the answer is real.
func TestListAccountsAsksTheServer(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	svc := f.newService(f.newCache(), &fakeLinks{})

	views, err := svc.ListAccounts(f.ctx())

	require.NoError(t, err)
	require.Len(t, views, 1)
	require.True(t, views[0].Capabilities.Tags)
	require.NotZero(t, views[0].Capabilities.MaxBatch)
	require.Equal(t, 1, srv.TotalConns())
}

// withoutWildcard is a WithPermanentFlags function for a server that does not
// let clients make new keywords.
func withoutWildcard(_ string, _ bool, flags []imap.Flag) []imap.Flag {
	var out []imap.Flag
	for _, fl := range flags {
		if fl != imap.FlagWildcard {
			out = append(out, fl)
		}
	}
	return out
}

// A server without new keywords has no tags. ListTags says so, and
// ListAccounts reports Tags false.
func TestNoTagsThroughService(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t, imaptest.WithPermanentFlags(withoutWildcard))
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), srv.Pass)
	svc := f.newService(f.newCache(), &fakeLinks{})

	tags, supported, err := svc.ListTags(f.ctx(), a.ID)

	require.NoError(t, err)
	require.False(t, supported)
	require.Empty(t, tags)

	views, err := svc.ListAccounts(f.ctx())
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.False(t, views[0].Capabilities.Tags)
	require.NotZero(t, views[0].Capabilities.MaxBatch)
}

func TestSearchWithWrongStoredPasswordFails(t *testing.T) {
	f := newFixture(t)
	srv := imaptest.New(t)
	a := f.addAccount(f.tenant, "me@example.com", serverSettings(srv), "not-the-password")
	svc := f.newService(f.newCache(), &fakeLinks{})

	_, err := svc.Search(f.ctx(), a.ID, mailQuery(), "")

	require.Error(t, err)
	require.NotContains(t, err.Error(), "not-the-password")
	require.NotContains(t, err.Error(), srv.Pass)
}

// The error for an account that must be reconnected is a typed error, so a
// caller reads the link from a field and not from the text.
func TestNeedsReauthErrorIsTyped(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw", func(a *account.Account) {
		a.Status = account.StatusNeedsReauth
		a.Levels[account.AreaMail] = account.MailOrganize
	})
	backends := &fakeFactory{backend: &fakeBackend{}}

	t.Run("with a link", func(t *testing.T) {
		svc := f.newService(backends, &fakeLinks{})

		_, err := svc.ListFolders(f.ctx(), a.ID)

		var re *ReauthError
		require.ErrorAs(t, err, &re)
		require.ErrorIs(t, err, ErrNeedsReauth)
		require.Equal(t, fmt.Sprintf("https://mcp.test/settings/%s?area=mail&level=%d", a.ID, int(account.MailOrganize)), re.Link)
		require.Contains(t, err.Error(), "reconnect it here: "+re.Link)
	})
	t.Run("without a link", func(t *testing.T) {
		svc := f.newService(backends, &fakeLinks{err: errors.New("no base address")})

		_, err := svc.ListFolders(f.ctx(), a.ID)

		var re *ReauthError
		require.ErrorAs(t, err, &re)
		require.ErrorIs(t, err, ErrNeedsReauth)
		require.Empty(t, re.Link)
		require.Contains(t, err.Error(), "reconnect it in the email-mcp settings")
	})
}

// The wording of the error is the same as before it was typed.
func TestReauthErrorWording(t *testing.T) {
	withLink := &ReauthError{Link: "https://mcp.test/x"}
	require.Equal(t, "service: the account needs to be reconnected. Ask the user to reconnect it here: https://mcp.test/x", withLink.Error())
	require.ErrorIs(t, withLink, ErrNeedsReauth)

	without := &ReauthError{}
	require.Equal(t, "service: the account needs to be reconnected. Ask the user to reconnect it in the email-mcp settings", without.Error())
	require.ErrorIs(t, without, ErrNeedsReauth)
}

// The limit for attachments is one exported constant: 10 MB.
func TestMaxAttachmentBytes(t *testing.T) {
	require.EqualValues(t, 10<<20, MaxAttachmentBytes)

	b := &fakeBackend{}
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")
	svc := f.newService(&fakeFactory{backend: b}, &fakeLinks{})

	_, err := svc.GetAttachment(f.ctx(), a.ID, "ref", "2")

	require.NoError(t, err)
	require.EqualValues(t, MaxAttachmentBytes, b.attBytes)
}
