package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
)

// record writes an audit entry of the local tenant.
func (p *pageFixture) record(e audit.Entry) {
	p.t.Helper()
	e.Tenant = p.tenant
	if e.Account != "" {
		e.Area = account.AreaMail
	}
	if e.Outcome == "" {
		e.Outcome = audit.OutcomeOK
	}
	require.NoError(p.t, p.audit.Record(context.Background(), e))
}

// queueSend queues a message of the account in the outbox. It is due in a
// minute.
func (p *pageFixture) queueSend(tenant account.TenantID, a *account.Account) string {
	p.t.Helper()
	item, err := p.outbox.Enqueue(context.Background(), tenant, a.ID, &mail.RawMessage{
		From: a.Address, Recipients: []string{"secret-rcpt@example.com"}, Data: []byte("Subject: secret-subject\r\n\r\nhello\r\n"),
	}, time.Minute, "")
	require.NoError(p.t, err)
	return item.ID
}

// cancelPath returns the address of the Cancel button of a queued send.
func cancelPath(id string) string { return "/activity/sends/" + id + "/cancel" }

// The page lists what waits to go out, with a form for each send. It shows
// nothing of the sealed message.
func TestActivityListsTheQueuedSends(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.queueSend(p.tenant, a)

	resp, body := p.get("/activity")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	for _, want := range []string{"Queued sends", id, "me@example.com", "Waits to go out"} {
		require.Contains(t, text, want)
	}
	require.Contains(t, body, `action="`+cancelPath(id)+`"`)
	require.NotContains(t, body, "secret-subject")
	require.NotContains(t, body, "secret-rcpt@example.com")
}

func TestActivitySaysWhenNothingIsQueued(t *testing.T) {
	p := newPageFixture(t)

	_, body := p.get("/activity")

	require.Contains(t, visibleText(t, body), "No message waits to go out.")
	require.NotContains(t, body, "/activity/sends/")
}

// A send that failed for now waits for its next try, not for its undo window.
func TestActivitySaysWhichSendWaitsForARetry(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.sender.setErr(errors.New("server busy"))
	p.queueSend(p.tenant, a)
	p.clock.Set(t0.Add(time.Minute))
	require.NoError(t, p.outbox.Drain(context.Background(), 0))

	_, body := p.get("/activity")

	require.Contains(t, visibleText(t, body), "Will try again")
	require.NotContains(t, body, "server busy", "the text of the error is not shown")
}

// The user cancels a queued send on the page, also after setting the account to
// Off. The send stays out of the mail, and the log has the entry.
func TestActivityCancelStopsAQueuedSend(t *testing.T) {
	p := newPageFixture(t)
	a := p.account(func(a *account.Account) { a.Levels[account.AreaMail] = account.MailOff })
	id := p.queueSend(p.tenant, a)

	resp, _ := p.post(cancelPath(id), nil)

	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/activity", resp.Header.Get("Location"))
	item, err := p.outbox.Get(context.Background(), p.tenant, id)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusCancelled, item.Status)
	last := p.entries()[0]
	require.Equal(t, "cancel_send", last.Tool)
	require.Equal(t, audit.OutcomeOK, last.Outcome)
	require.Equal(t, "cancelled_by_user", last.Detail)
	require.Equal(t, a.ID, last.Account)

	_, body := p.get("/activity")
	require.NotContains(t, body, cancelPath(id))
	require.Contains(t, visibleText(t, body), "Cancelled by user")

	p.clock.Set(t0.Add(time.Hour))
	require.NoError(t, p.outbox.Drain(context.Background(), 0))
	require.Empty(t, p.sender.sent())
}

// The send of another tenant answers like an id that does not exist, and it
// stays queued.
func TestActivityCancelOfAnotherTenantIsNotFound(t *testing.T) {
	p := newPageFixture(t)
	p.addTenant("t2")
	theirs := p.addAccount("t2", "them@example.com")
	id := p.queueSend("t2", theirs)

	resp, body := p.post(cancelPath(id), nil)
	missing, missingBody := p.post(cancelPath("no-such-id"), nil)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, http.StatusNotFound, missing.StatusCode)
	require.Equal(t, visibleText(t, missingBody), visibleText(t, body))
	item, err := p.outbox.Get(context.Background(), "t2", id)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusQueued, item.Status)
}

func TestActivityCancelOfASendThatLeftIsTooLate(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	id := p.queueSend(p.tenant, a)
	p.clock.Set(t0.Add(time.Minute))
	require.NoError(t, p.outbox.Drain(context.Background(), 0))
	require.Len(t, p.sender.sent(), 1)

	resp, body := p.post(cancelPath(id), nil)

	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, visibleText(t, body), "too late")
}

func TestActivityShowsTheLog(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.record(audit.Entry{Account: a.ID, Client: "<b>Claude</b>", Tool: "send_message", Count: 1,
		Recipients: []string{"bob@example.com", "carol@example.com"}, Outcome: audit.OutcomePendingApproval, Detail: "confirm_sends"})
	p.record(audit.Entry{Client: "\u3164\u3164", Tool: "remove_account", Detail: "account_removed"})

	resp, body := p.get("/activity")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, logRows(t, body))
	text := visibleText(t, body)
	for _, want := range []string{
		"Send message", "me@example.com", "bob@example.com", "carol@example.com", "Pending approval",
		"Confirm sends", "<b>Claude</b>", "Remove account", "(blank name)",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, body, "<b>Claude</b>", "the client name is escaped")
	require.NotContains(t, body, "\u3164")
}

// An entry of a call with an access token shows the id of the token.
func TestActivityShowsTheTokenID(t *testing.T) {
	p := newPageFixture(t)
	p.record(audit.Entry{Client: "app", Tool: "send_message", TokenID: "ab2c3d"})
	p.record(audit.Entry{Client: "stdio-app", Tool: "list_accounts"})

	_, body := p.get("/activity")

	require.Contains(t, visibleText(t, body), "ab2c3d")
	require.Equal(t, 1, strings.Count(body, "<span>token <code"), "the entry without a token shows none")
}

// The filters are exact matches from fixed lists. A value that is not in the
// list is ignored.
func TestActivityFilters(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	b := p.addAccount(p.tenant, "other@example.com")
	p.record(audit.Entry{Account: a.ID, Tool: "send_message", Count: 1})
	p.record(audit.Entry{Account: a.ID, Tool: "trash_messages", Count: 2})
	p.record(audit.Entry{Account: b.ID, Tool: "send_message", Count: 1})

	cases := map[string]int{
		"":                         3,
		"?account=" + string(a.ID): 2,
		"?account=" + string(b.ID): 1,
		"?tool=send_message":       2,
		"?account=" + string(a.ID) + "&tool=send_message":       1,
		"?account=no-such-account":                              3,
		"?account=" + strings.ToUpper(string(a.ID)):             3,
		"?tool=no_such_tool":                                    3,
		"?tool=send_message&tool=trash_messages":                3,
		"?account=" + string(a.ID) + "&account=" + string(b.ID): 3,
	}
	for query, rows := range cases {
		resp, body := p.get("/activity" + query)
		require.Equal(t, http.StatusOK, resp.StatusCode, query)
		require.Equal(t, rows, logRows(t, body), query)
	}

	// The filter form offers the accounts of the tenant and the known tools.
	_, body := p.get("/activity")
	require.Contains(t, body, `value="`+string(a.ID)+`"`)
	require.Contains(t, body, `value="update_settings"`)
}

// The limit is clamped to 1 to 200, and is 50 when it is missing or not a
// number.
func TestActivityLimitIsClamped(t *testing.T) {
	p := newPageFixture(t)
	for range 205 {
		p.record(audit.Entry{Tool: "connect_account"})
	}
	cases := map[string]int{
		"":                            50,
		"?limit=abc":                  50,
		"?limit=":                     50,
		"?limit=3":                    3,
		"?limit=0":                    1,
		"?limit=-5":                   1,
		"?limit=1000":                 200,
		"?limit=99999999999999999999": 50,
		"?limit=3&limit=4":            50,
	}
	for query, rows := range cases {
		resp, body := p.get("/activity" + query)
		require.Equal(t, http.StatusOK, resp.StatusCode, query)
		require.Equal(t, rows, logRows(t, body), query)
	}
}

func TestActivityShowsTheArgumentsOfACall(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.record(audit.Entry{Kind: audit.KindCall, Account: a.ID, Client: "Claude", Tool: "search_messages",
		Arguments: []byte("{\"subject\":\"<b>quarterly</b>\u3164\"}")})

	resp, body := p.get("/activity")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 1, logRows(t, body))
	require.Contains(t, body, "<span>Request</span>")
	require.Contains(t, visibleText(t, body), `{"subject":"<b>quarterly</b>"}`)
	require.NotContains(t, body, "<b>quarterly</b>", "the arguments come from the agent, so they are escaped")
	require.NotContains(t, body, "\u3164", "hidden characters are removed")
}

func TestActivityNamesTheKindOfAnAction(t *testing.T) {
	p := newPageFixture(t)
	p.record(audit.Entry{Tool: "remove_account", Detail: "account_removed"})

	_, body := p.get("/activity")

	require.Contains(t, body, "<span>Action</span>")
	require.NotContains(t, body, "<details", "an action has no arguments to show")
}

// The kind is an exact match from a fixed list, like the other filters.
func TestActivityFilterByKind(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.record(audit.Entry{Account: a.ID, Tool: "send_message", Count: 1})
	p.record(audit.Entry{Kind: audit.KindCall, Account: a.ID, Tool: "send_message"})
	p.record(audit.Entry{Kind: audit.KindCall, Account: a.ID, Tool: "get_message"})

	cases := map[string]int{
		"":                            3,
		"?kind=call":                  2,
		"?kind=action":                1,
		"?kind=other":                 3,
		"?kind=call&kind=action":      3,
		"?kind=call&tool=get_message": 1,
	}
	for query, want := range cases {
		resp, body := p.get("/activity" + query)
		require.Equal(t, http.StatusOK, resp.StatusCode, query)
		require.Equal(t, want, logRows(t, body), query)
	}
}

func TestActivityOffersTheKindFilter(t *testing.T) {
	p := newPageFixture(t)

	_, body := p.get("/activity?kind=call")

	require.Contains(t, body, `<option value="call" selected>`)
	require.Contains(t, body, `<option value="action">`)
}

// A page has up to 200 entries, so it must not hold all of their arguments.
func TestActivityCutsLongArguments(t *testing.T) {
	long := `"` + strings.Repeat("r", 10000) + `"`
	p := newPageFixture(t)
	p.record(audit.Entry{Kind: audit.KindCall, Tool: "move_messages", Arguments: []byte(long)})

	_, body := p.get("/activity")

	text := visibleText(t, body)
	require.Contains(t, text, "(10002 bytes in all)")
	require.NotContains(t, body, strings.Repeat("r", 4097))
	require.Contains(t, body, strings.Repeat("r", 4000))
}

func TestArgumentsTextOfAnEntry(t *testing.T) {
	require.Empty(t, argumentsText(audit.Entry{}))
	require.Equal(t, "These arguments cannot be read.", argumentsText(audit.Entry{ArgumentsUnreadable: true}))
	// The cut must not fall inside a character: byte 4096 is inside one here.
	text := "x" + strings.Repeat("\u00e9", 3000)
	require.Equal(t, "x"+strings.Repeat("\u00e9", 2047)+"\u2026 (6001 bytes in all)", argumentsText(audit.Entry{Arguments: []byte(text)}))
}
