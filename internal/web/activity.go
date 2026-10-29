package web

// This file holds the activity page: the audit log of the tenant, newest
// first, with filters by account and by tool.
//
// Each filter takes a value only from a fixed list: the accounts of the tenant
// and the tools that the log can hold. A value that is not in the list is
// ignored, as is a filter that is given twice. The number of entries is clamped
// to 1 to 200 (50 when it is missing or not a number) before the log is read.
//
// The recipients are decrypted only to show them here. Every text is written as
// text. The client name is the name that an MCP client gave for itself, so it is
// not trusted: it is cleaned, and a name that is only blank characters is shown
// as "(blank name)".

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The name of the page of this file.
const pageActivity = "activity"

// codeActivityFailed is the log code of a failed activity page.
const codeActivityFailed = "activity_failed"

// The number of entries that the page shows.
const (
	defaultActivityLimit = 50
	maxActivityLimit     = 200
)

// blankName is shown for a client name that is only blank characters.
const blankName = "(blank name)"

// activityPage is the data of the activity page.
type activityPage struct {
	Accounts []choice
	Tools    []choice
	Limit    int
	Entries  []activityRow
}

// choice is one value of a filter.
type choice struct {
	Value    string
	Label    string
	Selected bool
}

// activityRow is one entry of the log.
type activityRow struct {
	Time       string
	Account    string
	Client     string
	Tool       string
	Count      int
	Outcome    string
	Detail     string
	Recipients string
}

// handleActivity shows the log of the tenant of the session. It changes
// nothing.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok || s.d.Audit == nil {
		s.fail(w, r, codeActivityFailed, errNoService)
		return
	}
	accounts, err := s.d.Store.ListAccounts(r.Context(), sess.tenant)
	if err != nil {
		s.fail(w, r, codeActivityFailed, err)
		return
	}
	q := r.URL.Query()
	addresses := make(map[account.AccountID]string, len(accounts))
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		addresses[a.ID] = a.Address
		ids = append(ids, string(a.ID))
	}
	tools := service.AuditTools()
	filter := audit.Filter{
		Account: oneOf(q["account"], ids),
		Tool:    oneOf(q["tool"], tools),
		Limit:   activityLimit(q["limit"]),
	}
	entries, err := s.d.Audit.List(r.Context(), sess.tenant, filter)
	if err != nil {
		s.fail(w, r, codeActivityFailed, err)
		return
	}

	page := activityPage{Limit: filter.Limit, Entries: make([]activityRow, 0, len(entries))}
	for _, a := range accounts {
		page.Accounts = append(page.Accounts, choice{Value: string(a.ID), Label: a.Address, Selected: string(a.ID) == filter.Account})
	}
	for _, t := range tools {
		page.Tools = append(page.Tools, choice{Value: t, Label: t, Selected: t == filter.Tool})
	}
	for _, e := range entries {
		row := activityRow{
			Time:    formatTime(e.Time),
			Client:  clientLabel(e.Client),
			Tool:    e.Tool,
			Count:   e.Count,
			Outcome: string(e.Outcome),
			Detail:  e.Detail,
		}
		if e.Account != "" {
			row.Account = addresses[e.Account]
		}
		recipients := make([]string, len(e.Recipients))
		for i, rcpt := range e.Recipients {
			recipients[i] = mime.CleanUntrusted(rcpt)
		}
		row.Recipients = strings.Join(recipients, ", ")
		page.Entries = append(page.Entries, row)
	}
	s.render(w, r, http.StatusOK, pageActivity, page)
}

// oneOf returns the value of a filter when the address has it exactly once and
// it is one of the choices. Otherwise it returns "": no filter.
func oneOf(values, choices []string) string {
	if len(values) != 1 || !slices.Contains(choices, values[0]) {
		return ""
	}
	return values[0]
}

// activityLimit returns the number of entries to show: the value of the address
// clamped to 1 to 200, or 50 when the address has no single whole number.
func activityLimit(values []string) int {
	if len(values) != 1 {
		return defaultActivityLimit
	}
	n, err := strconv.Atoi(values[0])
	if err != nil {
		return defaultActivityLimit
	}
	return min(max(n, 1), maxActivityLimit)
}

// clientLabel returns the client name for the page: cleaned of hidden
// characters, or "(blank name)" when nothing visible is left.
func clientLabel(name string) string {
	clean := mime.CleanUntrusted(name)
	if strings.TrimSpace(clean) == "" {
		return blankName
	}
	return clean
}
