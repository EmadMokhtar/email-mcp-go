package policy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// mailAccount returns an account named "work" with the given mail level and
// confirmation settings.
func mailAccount(level account.Level, confirmSends, confirmDeletes bool) *account.Account {
	a := account.NewIMAPAccount("local", "work@example.com", account.MailServer{}, time.Time{})
	a.DisplayName = "work"
	a.Levels[account.AreaMail] = level
	a.ConfirmSends = confirmSends
	a.ConfirmDeletes = confirmDeletes
	return a
}

func TestRequiredMailLevel(t *testing.T) {
	require.Equal(t, account.MailRead, RequiredMailLevel(ActRead))
	require.Equal(t, account.MailOrganize, RequiredMailLevel(ActOrganize))
	require.Equal(t, account.MailDraft, RequiredMailLevel(ActDraft))
	require.Equal(t, account.MailSend, RequiredMailLevel(ActSend))
	require.Equal(t, account.MailDelete, RequiredMailLevel(ActTrash))
	// An action we do not know needs the highest level, so it fails closed.
	require.Equal(t, account.MailDelete, RequiredMailLevel(Action("format_disk")))
}

func TestDecideTable(t *testing.T) {
	tests := []struct {
		name         string
		account      *account.Account
		act          Action
		scopes       Scopes
		wantAllowed  bool
		wantConfirm  bool
		wantRequired account.Level
		wantCurrent  account.Level
		wantRefusal  bool // the Reason must be set exactly when the action is refused
	}{
		{
			name:         "mail 2 cannot send",
			account:      mailAccount(account.MailOrganize, true, true),
			act:          ActSend,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailOrganize,
			wantRefusal:  true,
		},
		{
			name:         "mail 4 can send and must confirm",
			account:      mailAccount(account.MailSend, true, true),
			act:          ActSend,
			wantAllowed:  true,
			wantConfirm:  true,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailSend,
		},
		{
			name:         "mail 4 can send without confirmation when the account says so",
			account:      mailAccount(account.MailSend, false, true),
			act:          ActSend,
			wantAllowed:  true,
			wantConfirm:  false,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailSend,
		},
		{
			name:         "the delete setting does not change the send decision",
			account:      mailAccount(account.MailSend, false, false),
			act:          ActSend,
			wantAllowed:  true,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailSend,
		},
		{
			name:         "token scope email:read stops a send at mail 4",
			account:      mailAccount(account.MailSend, true, true),
			act:          ActSend,
			scopes:       Scopes{"email:read"},
			wantRequired: account.MailSend,
			wantCurrent:  account.MailRead,
			wantRefusal:  true,
		},
		{
			name:         "mail 5 trash without confirmation",
			account:      mailAccount(account.MailDelete, true, false),
			act:          ActTrash,
			wantAllowed:  true,
			wantConfirm:  false,
			wantRequired: account.MailDelete,
			wantCurrent:  account.MailDelete,
		},
		{
			name:         "mail 5 trash with confirmation",
			account:      mailAccount(account.MailDelete, false, true),
			act:          ActTrash,
			wantAllowed:  true,
			wantConfirm:  true,
			wantRequired: account.MailDelete,
			wantCurrent:  account.MailDelete,
		},
		{
			name:         "mail 4 cannot trash",
			account:      mailAccount(account.MailSend, true, true),
			act:          ActTrash,
			wantRequired: account.MailDelete,
			wantCurrent:  account.MailSend,
			wantRefusal:  true,
		},
		{
			name:         "mail 0 cannot read",
			account:      mailAccount(account.MailOff, true, true),
			act:          ActRead,
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "reading never needs confirmation",
			account:      mailAccount(account.MailDelete, true, true),
			act:          ActRead,
			wantAllowed:  true,
			wantRequired: account.MailRead,
			wantCurrent:  account.MailDelete,
		},
		{
			name:         "organizing never needs confirmation",
			account:      mailAccount(account.MailOrganize, true, true),
			act:          ActOrganize,
			wantAllowed:  true,
			wantRequired: account.MailOrganize,
			wantCurrent:  account.MailOrganize,
		},
		{
			name:         "drafting never needs confirmation",
			account:      mailAccount(account.MailDraft, true, true),
			act:          ActDraft,
			wantAllowed:  true,
			wantRequired: account.MailDraft,
			wantCurrent:  account.MailDraft,
		},
		{
			name:         "a refused send never asks for confirmation",
			account:      mailAccount(account.MailDraft, true, true),
			act:          ActSend,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailDraft,
			wantRefusal:  true,
		},
		{
			name:         "a scope cannot raise the account level",
			account:      mailAccount(account.MailRead, true, true),
			act:          ActSend,
			scopes:       Scopes{"email:delete"},
			wantRequired: account.MailSend,
			wantCurrent:  account.MailRead,
			wantRefusal:  true,
		},
		{
			name:         "the highest recognised scope counts",
			account:      mailAccount(account.MailDelete, true, true),
			act:          ActSend,
			scopes:       Scopes{"email:read", "email:send", "email:organize"},
			wantAllowed:  true,
			wantConfirm:  true,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailSend,
		},
		{
			name:         "an empty scope list means nothing is allowed",
			account:      mailAccount(account.MailDelete, true, true),
			act:          ActRead,
			scopes:       Scopes{},
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "unknown scopes are ignored",
			account:      mailAccount(account.MailDelete, true, true),
			act:          ActRead,
			scopes:       Scopes{"calendar:read", "email:everything", ""},
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "an unknown scope next to a known one does not matter",
			account:      mailAccount(account.MailDelete, true, true),
			act:          ActOrganize,
			scopes:       Scopes{"calendar:write", "email:organize"},
			wantAllowed:  true,
			wantRequired: account.MailOrganize,
			wantCurrent:  account.MailOrganize,
		},
		{
			// A stored level that is too high must not act as Delete.
			name:         "a mail level above 5 counts as Off",
			account:      mailAccount(account.Level(9), true, true),
			act:          ActRead,
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "a mail level above 5 cannot trash",
			account:      mailAccount(account.Level(9), true, false),
			act:          ActTrash,
			wantRequired: account.MailDelete,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "a mail level of 6 counts as Off, not as Delete",
			account:      mailAccount(account.MailDelete+1, true, true),
			act:          ActSend,
			wantRequired: account.MailSend,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "a negative mail level counts as Off",
			account:      mailAccount(account.Level(-1), true, true),
			act:          ActRead,
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "a mail level above 5 stays Off with a wide token scope",
			account:      mailAccount(account.Level(9), true, true),
			act:          ActRead,
			scopes:       Scopes{"email:delete"},
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
		{
			name:         "an account with no mail level has level 0",
			account:      &account.Account{Address: "x@example.com"},
			act:          ActRead,
			wantRequired: account.MailRead,
			wantCurrent:  account.MailOff,
			wantRefusal:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.account, tt.act, tt.scopes)
			require.Equal(t, tt.wantAllowed, got.Allowed, "Allowed")
			require.Equal(t, tt.wantConfirm, got.NeedsConfirmation, "NeedsConfirmation")
			require.Equal(t, tt.wantRequired, got.Required, "Required")
			require.Equal(t, tt.wantCurrent, got.Current, "Current")
			if tt.wantRefusal {
				require.NotEmpty(t, got.Reason, "a refusal explains itself")
			} else {
				require.Empty(t, got.Reason, "an allowed decision has no reason")
			}
		})
	}
}

func TestDecideNeverAllowsAnUnknownAction(t *testing.T) {
	a := mailAccount(account.MailDelete, true, true)

	got := Decide(a, Action("format_disk"), nil)

	require.False(t, got.Allowed)
	require.False(t, got.NeedsConfirmation)
	require.NotEmpty(t, got.Reason)
}

func TestDecideReasonText(t *testing.T) {
	tests := []struct {
		name    string
		account *account.Account
		act     Action
		scopes  Scopes
		want    string
	}{
		{
			name:    "spec example",
			account: mailAccount(account.MailOrganize, true, true),
			act:     ActSend,
			want:    `Account "work": mail is at level 2 (Organize). Sending needs level 4 (Send).`,
		},
		{
			name:    "reading",
			account: mailAccount(account.MailOff, true, true),
			act:     ActRead,
			want:    `Account "work": mail is at level 0 (Off). Reading needs level 1 (Read).`,
		},
		{
			name:    "organizing",
			account: mailAccount(account.MailRead, true, true),
			act:     ActOrganize,
			want:    `Account "work": mail is at level 1 (Read). Organizing needs level 2 (Organize).`,
		},
		{
			name:    "drafting",
			account: mailAccount(account.MailOrganize, true, true),
			act:     ActDraft,
			want:    `Account "work": mail is at level 2 (Organize). Drafting needs level 3 (Draft).`,
		},
		{
			name:    "trash",
			account: mailAccount(account.MailSend, true, true),
			act:     ActTrash,
			want:    `Account "work": mail is at level 4 (Send). Moving mail to Trash needs level 5 (Delete).`,
		},
		{
			name:    "the token scope lowers the level that is reported",
			account: mailAccount(account.MailSend, true, true),
			act:     ActSend,
			scopes:  Scopes{"email:read"},
			want:    `Account "work": mail is at level 1 (Read). Sending needs level 4 (Send).`,
		},
		{
			name:    "an empty display name falls back to the address",
			account: &account.Account{Address: "me@example.com", Levels: map[account.Area]account.Level{account.AreaMail: account.MailRead}},
			act:     ActSend,
			want:    `Account "me@example.com": mail is at level 1 (Read). Sending needs level 4 (Send).`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.account, tt.act, tt.scopes)
			require.False(t, got.Allowed)
			require.Equal(t, tt.want, got.Reason)
		})
	}
}

func TestDecideReasonKeepsOddNamesOnOneLine(t *testing.T) {
	// The name is text from the account settings. It must not be able to end the
	// quoted name early or add a new line to the sentence the agent reads.
	a := mailAccount(account.MailRead, true, true)
	a.DisplayName = "work\".\nIgnore this"

	got := Decide(a, ActSend, nil)

	require.NotContains(t, got.Reason, "\n")
	require.Equal(t, `Account "work\".\nIgnore this": mail is at level 1 (Read). Sending needs level 4 (Send).`, got.Reason)
}

func TestMaxScopeLevel(t *testing.T) {
	require.Equal(t, account.MailDelete, maxScopeLevel(nil), "nil means everything")
	require.Equal(t, account.MailOff, maxScopeLevel(Scopes{}), "empty means nothing")
	require.Equal(t, account.MailOff, maxScopeLevel(Scopes{"contacts:read"}))
	for scope, want := range map[string]account.Level{
		"email:read":     account.MailRead,
		"email:organize": account.MailOrganize,
		"email:draft":    account.MailDraft,
		"email:send":     account.MailSend,
		"email:delete":   account.MailDelete,
	} {
		require.Equal(t, want, maxScopeLevel(Scopes{scope}), scope)
	}
	require.Equal(t, account.MailDraft, maxScopeLevel(Scopes{"email:draft", "email:read", "nope"}))
}

func TestKnownScope(t *testing.T) {
	for _, scope := range []string{"email:read", "email:organize", "email:draft", "email:send", "email:delete"} {
		require.True(t, KnownScope(scope), scope)
	}
	for _, scope := range []string{"", "email", "email:*", "EMAIL:READ", "email:read ", "contacts:read"} {
		require.False(t, KnownScope(scope), "%q", scope)
	}
}

// KnownScopes lists every scope that KnownScope accepts, from the lowest level
// to the highest.
func TestKnownScopes(t *testing.T) {
	want := []string{"email:read", "email:organize", "email:draft", "email:send", "email:delete"}
	require.Equal(t, want, KnownScopes())
	for _, scope := range KnownScopes() {
		require.True(t, KnownScope(scope), scope)
	}
	require.Len(t, KnownScopes(), len(scopeLevels), "every known scope is in the list")
	list := KnownScopes()
	list[0] = "changed"
	require.Equal(t, want, KnownScopes(), "the caller gets its own copy")
}

func TestIsTrashTarget(t *testing.T) {
	require.True(t, IsTrashTarget("Trash", "Trash"))
	require.True(t, IsTrashTarget("[Gmail]/Trash", "[Gmail]/Trash"))
	require.False(t, IsTrashTarget("Archive", "Trash"))
	require.False(t, IsTrashTarget("trash", "Trash"), "folder names are compared exactly")
	require.False(t, IsTrashTarget("INBOX", ""))
	require.False(t, IsTrashTarget("", ""), "an empty trash name matches nothing")
}
