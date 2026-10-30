package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
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

func TestActivityShowsTheLog(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.record(audit.Entry{Account: a.ID, Client: "<b>Claude</b>", Tool: "send_message", Count: 1,
		Recipients: []string{"bob@example.com", "carol@example.com"}, Outcome: audit.OutcomePendingApproval, Detail: "confirm_sends"})
	p.record(audit.Entry{Client: "\u3164\u3164", Tool: "remove_account", Detail: "account_removed"})

	resp, body := p.get("/activity")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, tableRows(t, body))
	text := visibleText(t, body)
	for _, want := range []string{
		"send_message", "me@example.com", "bob@example.com", "carol@example.com", "pending_approval",
		"confirm_sends", "<b>Claude</b>", "remove_account", "(blank name)",
	} {
		require.Contains(t, text, want)
	}
	require.NotContains(t, body, "<b>Claude</b>", "the client name is escaped")
	require.NotContains(t, body, "\u3164")
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
		require.Equal(t, rows, tableRows(t, body), query)
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
		require.Equal(t, rows, tableRows(t, body), query)
	}
}
