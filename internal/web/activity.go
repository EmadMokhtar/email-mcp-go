package web

// This file holds the activity page: the audit log of the tenant, newest
// first, with filters by account, by tool and by kind.
//
// Each filter takes a value only from a fixed list: the accounts of the tenant,
// the tools that the log can hold, and the two kinds. A value that is not in the
// list is ignored, as is a filter that is given twice. The number of entries is
// clamped to 1 to 200 (50 when it is missing or not a number) before the log is
// read.
//
// The recipients and the arguments are decrypted only to show them here. Every
// text is written as text. The client name is the name that an MCP client gave
// for itself, so it is not trusted: it is cleaned, and a name that is only blank
// characters is shown as "(blank name)". The arguments come from the agent, so
// they are not trusted either, and they are cleaned too.

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// maxArgumentsShown is the most bytes of the arguments of one entry that the page
// shows. A page has up to 200 entries, and the arguments of one can be 256 KB.
const maxArgumentsShown = 4 << 10

// unreadableArguments is shown for arguments that cannot be decrypted.
const unreadableArguments = "These arguments cannot be read."

// activityPage is the data of the activity page.
type activityPage struct {
	Accounts []choice
	Tools    []choice
	Kinds    []choice
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
	// Time is short, and FullTime has the year and the time zone.
	Time     string
	FullTime string
	Kind     string
	Account  string
	Client   string
	// TokenID is the id of the access token of the call, or empty.
	TokenID    string
	Tool       string
	Count      int
	Outcome    string
	Detail     string
	Recipients string
	Arguments  string
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
		Kind:    oneOf(q["kind"], []string{string(audit.KindCall), string(audit.KindAction)}),
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
		page.Tools = append(page.Tools, choice{Value: t, Label: plainWords(t), Selected: t == filter.Tool})
	}
	for _, k := range []audit.Kind{audit.KindCall, audit.KindAction} {
		page.Kinds = append(page.Kinds, choice{Value: string(k), Label: kindLabel(k) + "s", Selected: string(k) == filter.Kind})
	}
	for _, e := range entries {
		row := activityRow{
			Time:      logTime(e.Time, s.now()),
			FullTime:  formatTime(e.Time),
			Kind:      kindLabel(e.Kind),
			Client:    clientLabel(e.Client),
			TokenID:   e.TokenID,
			Tool:      plainWords(e.Tool),
			Count:     e.Count,
			Outcome:   plainWords(string(e.Outcome)),
			Detail:    plainWords(e.Detail),
			Arguments: argumentsText(e),
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

// argumentsText returns the arguments of an entry for the page: cleaned of hidden
// characters, and cut at the end of a character when they are long.
func argumentsText(e audit.Entry) string {
	if e.ArgumentsUnreadable {
		return unreadableArguments
	}
	text := mime.CleanUntrusted(string(e.Arguments))
	if len(text) <= maxArgumentsShown {
		return text
	}
	cut := maxArgumentsShown
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\u2026 (%d bytes in all)", text[:cut], len(text))
}

// kindLabel returns the name of a kind for the page.
func kindLabel(k audit.Kind) string {
	if k == audit.KindCall {
		return "Request"
	}
	return "Action"
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

// logTime writes the time of an entry for a person. An entry of another year
// has its year.
func logTime(t, now time.Time) string {
	if t.Local().Year() == now.Local().Year() {
		return friendlyTime(t)
	}
	return t.Local().Format("2 Jan 2006 at 15:04")
}

// plainWords writes a fixed code of the log as words: "send_message" gives
// "Send message". The log keeps the code.
func plainWords(code string) string {
	if code == "ok" {
		return "OK"
	}
	text := strings.ReplaceAll(code, "_", " ")
	first, size := utf8.DecodeRuneInString(text)
	if size == 0 {
		return ""
	}
	return string(unicode.ToUpper(first)) + text[size:]
}
