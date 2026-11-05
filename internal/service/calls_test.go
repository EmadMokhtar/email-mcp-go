package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

func TestRecordToolCallWritesACallEntry(t *testing.T) {
	e := newWriteEnv(t)
	a := e.account()

	e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant, Client: "test client"}, ToolCall{
		Tool: "get_message", Account: a.ID, Outcome: audit.OutcomeRefused, Detail: "permission",
		Arguments: json.RawMessage(`{"ref":"r1"}`),
	})

	got := e.lastEntry()
	require.Equal(t, audit.KindCall, got.Kind)
	require.Equal(t, a.ID, got.Account)
	require.Equal(t, account.AreaMail, got.Area)
	require.Equal(t, "test client", got.Client)
	require.Equal(t, "get_message", got.Tool)
	require.Equal(t, audit.OutcomeRefused, got.Outcome)
	require.Equal(t, "permission", got.Detail)
	require.JSONEq(t, `{"ref":"r1"}`, string(got.Arguments))
}

// The account of a call is text from the agent. The log must keep the call when
// that text is no account of the tenant, and the foreign key of the table would
// refuse the row if it named one.
func TestRecordToolCallKeepsACallForAnAccountThatIsNotThere(t *testing.T) {
	for name, id := range map[string]account.AccountID{
		"no such account": "no-such-account",
		"not a valid id":  "a|b",
		"too strange":     "new\nline",
	} {
		t.Run(name, func(t *testing.T) {
			e := newWriteEnv(t)

			e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{
				Tool: "search_messages", Account: id, Outcome: audit.OutcomeRefused, Detail: "account_not_found",
			})

			got := e.lastEntry()
			require.Equal(t, "search_messages", got.Tool)
			require.Empty(t, got.Account)
			require.Empty(t, got.Area)
		})
	}
}

func TestRecordToolCallNeverNamesAnAccountOfAnotherTenant(t *testing.T) {
	e := newWriteEnv(t)
	e.f.addTenant("other")
	theirs := e.f.addAccount("other", "them@example.com", offlineSettings(), "pw")

	e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{
		Tool: "get_message", Account: theirs.ID, Outcome: audit.OutcomeRefused, Detail: "account_not_found",
	})

	require.Empty(t, e.lastEntry().Account)
}

// A client that leaves must not hide what it asked for.
func TestRecordToolCallSurvivesACancelledContext(t *testing.T) {
	e := newWriteEnv(t)
	a := e.account()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e.svc.RecordToolCall(ctx, Principal{Tenant: e.f.tenant}, ToolCall{
		Tool: "get_message", Account: a.ID, Outcome: audit.OutcomeError, Detail: "canceled",
	})

	require.Equal(t, a.ID, e.lastEntry().Account)
}

func TestRecordToolCallLogsAnEntryThatCannotBeWritten(t *testing.T) {
	e := newWriteEnv(t)

	e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{
		Tool: "", Outcome: audit.OutcomeOK, Arguments: json.RawMessage(`{"subject":"SecretSubjectMarker"}`),
	})

	require.Empty(t, e.entries())
	require.Contains(t, e.logs.String(), "audit_entry_invalid")
	require.NotContains(t, e.logs.String(), "SecretSubjectMarker")
}

func TestRecordToolCallWithoutAnAuditLogDoesNothing(t *testing.T) {
	e := newWriteEnv(t, func(d *Deps) { d.Audit = nil })

	require.NotPanics(t, func() {
		e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{Tool: "list_accounts", Outcome: audit.OutcomeOK})
	})
}

func TestAuthorizeNotesTheAccountThatItFinds(t *testing.T) {
	e := newWriteEnv(t)
	off := e.account(level(account.MailOff))

	cases := map[string]struct {
		id   account.AccountID
		want account.AccountID
	}{
		"a refused account is still an account": {off.ID, off.ID},
		"no such account":                       {"no-such-account", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, note := WithAccountNote(e.ctx())

			_, err := e.svc.authorize(ctx, tc.id, policy.ActRead)

			require.Error(t, err)
			require.Equal(t, tc.want, *note)
		})
	}
}

// The foreign key refuses this row, so an entry can only exist if the service
// looked the account up, which it must not do for an account that it knows.
func TestRecordToolCallDoesNotLookUpAnAccountThatWasFound(t *testing.T) {
	e := newWriteEnv(t)

	e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{
		Tool: "get_message", Account: "no-such-account", AccountFound: true, Outcome: audit.OutcomeOK,
	})

	require.Empty(t, e.entries())
	require.Contains(t, e.logs.String(), "audit_write_failed")
}

// A failed lookup leaves the entry without its account, so it must show in the log.
func TestRecordToolCallLogsAnAccountThatCouldNotBeChecked(t *testing.T) {
	e := newWriteEnv(t)
	a := e.account()
	require.NoError(t, e.f.store.Close())

	e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{
		Tool: "get_message", Account: a.ID, Outcome: audit.OutcomeOK,
	})

	require.Contains(t, e.logs.String(), "audit_account_check_failed")
}

func TestRecordToolCallDoesNotLogAnAccountThatIsNotThere(t *testing.T) {
	e := newWriteEnv(t)

	e.svc.RecordToolCall(context.Background(), Principal{Tenant: e.f.tenant}, ToolCall{
		Tool: "get_message", Account: "no-such-account", Outcome: audit.OutcomeOK,
	})

	require.NotContains(t, e.logs.String(), "audit_account_check_failed")
}

func TestAuditCode(t *testing.T) {
	cases := map[string]struct {
		err     error
		outcome audit.Outcome
		detail  string
		known   bool
	}{
		"a refused level": {&PermissionError{}, audit.OutcomeRefused, "level_too_low", true},
		"a failed move":   {mail.ErrConflict, audit.OutcomeError, "conflict", true},
		"another error":   {errors.New("no"), audit.OutcomeError, "failed", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			outcome, detail, known := AuditCode(tc.err)
			require.Equal(t, tc.outcome, outcome)
			require.Equal(t, tc.detail, detail)
			require.Equal(t, tc.known, known)
		})
	}
}

// The activity page offers these tools as a filter, so a call of each one can be
// found there.
func TestAuditToolsListsTheToolsThatOnlyHaveCallEntries(t *testing.T) {
	tools := AuditTools()
	for _, want := range []string{
		"list_accounts", "list_folders", "list_tags", "search_messages", "list_new_messages", "get_message",
		"get_attachment", "get_send_status", "request_permission_change", "unknown_tool",
	} {
		require.Contains(t, tools, want)
	}
}
