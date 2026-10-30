package e2e

// The tests of the security checklist of the spec, section 15.4, for M0a. The
// rows for Calendar 1 and for the contact read cap are not part of M0a.

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// ---------------------------------------------------------------- helpers of the tests

// The exact texts that the agent gets when a confirmation is refused
// (internal/mcpserver/errors.go). Each refusal has its own text, so a test can
// tell which check refused.
const (
	textStateInvalid = "The confirmation is not valid: its request state was changed, or it belongs to " +
		"another request. Nothing was done. Call the tool again to ask the user again. Do not repeat the same call."
	textStateExpired = "The confirmation has expired: the user must answer within 10 minutes. " +
		"Nothing was done. Call the tool again to ask the user again. Do not repeat the same call."
	textPayloadChanged = "The request is not the one that the user was asked about: the arguments " +
		"changed, or the message built again is different. Nothing was done. Call the tool again to ask the user again. " +
		"Do not repeat the same call."
	textNotPending = "The action no longer waits for the user: it was carried out, declined or cancelled, " +
		"or it expired. Check a send with get_send_status. Do not repeat the same call."
)

// crlf turns the line breaks of a test message into CRLF, as mail has them.
func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// incomingMail is a message in the inbox of the account, from a third party,
// with the test secrets in the sender, the subject, the body and an
// attachment.
func incomingMail() []byte {
	return crlf("From: " + inSenderName + " <" + inSenderAddress + ">\n" +
		"To: " + accountAddress + "\n" +
		"Subject: " + markedSubject(inSubject) + "\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"Message-ID: <in-1@example.org>\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\n" +
		"\n" +
		"--B\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" +
		markedBody(inBody) +
		"--B\n" +
		"Content-Type: text/plain; name=\"" + inAttachName + "\"\n" +
		"Content-Disposition: attachment; filename=\"" + inAttachName + "\"\n" +
		"Content-Transfer-Encoding: base64\n" +
		"\n" +
		base64.StdEncoding.EncodeToString([]byte(inAttachBody)) + "\n" +
		"--B--\n")
}

// otherMail is a plain message in the inbox.
func otherMail(n int) []byte {
	return crlf(fmt.Sprintf("From: someone@example.com\nTo: %s\nSubject: note %d\n"+
		"Date: Tue, 10 Mar 2026 11:00:00 +0000\nMIME-Version: 1.0\nContent-Type: text/plain\n\ntext %d\n", accountAddress, n, n))
}

// allSecrets are the test secrets that must never be in a log or, apart from
// the address of the account, in plain text in the database. Each test adds the
// tokens that it makes.
func allSecrets() map[string]string {
	return map[string]string{
		"password":                  secretPassword,
		"wrong password":            secretWrongPassword,
		"account address":           accountAddress,
		"sender address":            inSenderAddress,
		"sender display name":       inSenderName,
		"incoming subject":          inSubject,
		"incoming body":             inBody,
		"incoming attachment name":  inAttachName,
		"incoming attachment data":  inAttachBody,
		"recipient address":         rcptAddress,
		"recipient display name":    rcptName,
		"cc address":                ccAddress,
		"bcc address":               bccAddress,
		"refused recipient address": rejectedAddress,
		"outgoing subject":          outSubject,
		"outgoing body":             outBody,
		"outgoing attachment name":  outAttachName,
		"outgoing attachment data":  outAttachBody,
		"draft subject":             draftSubject,
		"draft body":                draftBody,
	}
}

// accountIDOf returns the id of the account with the address, in the local
// tenant.
func (h *harness) accountIDOf(address string) account.AccountID {
	h.t.Helper()
	views, err := h.app.Service.ListAccounts(localCtx())
	require.NoError(h.t, err)
	for _, v := range views {
		if v.Address == address {
			return v.ID
		}
	}
	h.t.Fatalf("no account with the test address")
	return ""
}

// settingsForm is the form of the settings page.
func settingsForm(level account.Level, confirmSends, confirmDeletes bool, undoAutoSeconds int) url.Values {
	form := url.Values{
		"mail_level":             {fmt.Sprint(int(level))},
		"sends_per_hour":         {"20"},
		"sends_per_day":          {"200"},
		"recipients_per_message": {"50"},
		"trash_per_day":          {"500"},
		"undo_window_auto":       {fmt.Sprint(undoAutoSeconds)},
		"undo_window_confirmed":  {"0"},
	}
	if confirmSends {
		form.Set("confirm_sends", "1")
	}
	if confirmDeletes {
		form.Set("confirm_deletes", "1")
	}
	return form
}

// checkpoint copies the write-ahead log into the database file and cuts the
// log to zero bytes (the TRUNCATE checkpoint of the daily jobs), from a second
// connection, as another process would.
func (h *harness) checkpoint() {
	h.t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, h.dataDir)
	require.NoError(h.t, err)
	defer func() { require.NoError(h.t, st.Close()) }()
	require.Eventually(h.t, func() bool {
		complete, err := st.CheckpointWAL(ctx, store.CheckpointTruncate)
		require.NoError(h.t, err)
		return complete
	}, 15*time.Second, 50*time.Millisecond, "the checkpoint copies the whole log")
}

// requireNoSessionPage checks a page that answers a request without a session: the
// status 401 and exactly the page that tells the user to run "email-mcp setup".
func requireNoSessionPage(t *testing.T, want string, status int, body, what string) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, status, what)
	require.Equal(t, want, body, "%s: the answer is the no-session page and nothing else", what)
}

// setupTokenOf returns the token of a setup link.
func setupTokenOf(t *testing.T, setupURL string) string {
	t.Helper()
	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	token := u.Query().Get("token")
	require.NotEmpty(t, token)
	return token
}

// webLinkPattern finds a link of the local web interface in a text.
var webLinkPattern = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+/[^\s"]*[^\s".]`)

// pendingIDs returns the ids of the pending actions in a state.
func pendingIDs(t *testing.T, h *harness, state confirm.State) []string {
	t.Helper()
	rows, err := h.sqlDB().Query(`SELECT id FROM pending_actions WHERE state = ? ORDER BY id`, string(state))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// stateOf returns the state of a pending action.
func stateOf(t *testing.T, h *harness, id string) string {
	t.Helper()
	var state string
	require.NoError(t, h.sqlDB().QueryRow(`SELECT state FROM pending_actions WHERE id = ?`, id).Scan(&state))
	return state
}

// ---------------------------------------------------------------- logs

// TestNoSecretsInLogs: the log of a full scenario of the whole program holds
// none of the test secrets and none of the tokens (spec 9.4, 15.4).
//
// Every line of every part of the program is captured, at Debug level, from
// the start of Open to the end of Close. The scenario signs a browser in,
// connects the account on the web page (with a wrong password first), and as
// an agent reads, searches, tags, moves, drafts, sends with an in-band
// confirmation, sends a draft, sends to a recipient that the server refuses,
// moves to Trash with a confirmation, sends through the approval page, sends
// through the outbox worker, cancels a send, and uses the HTTP endpoint with a
// token (and a wrong token). It also makes the web interface refuse requests
// without a session, with a wrong Host, and from another origin.
func TestNoSecretsInLogs(t *testing.T) {
	h := newHarness(t)
	h.app.Logger().Debug("e2e: the log has Debug lines")
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	h.app.Run(runCtx)

	m := newMailServers(t, smtptest.RejectRcpt(rejectedAddress))
	m.imap.Append("INBOX", incomingMail())
	secretsToFind := allSecrets()

	// The user signs the browser in with the one-time setup link.
	b, setupURL := h.signIn()
	secretsToFind["setup URL"] = setupURL
	secretsToFind["setup token"] = setupTokenOf(t, setupURL)
	secretsToFind["session id"] = b.cookie
	secretsToFind["CSRF token"] = b.csrf

	// The user connects the account on the web page. The test of the settings
	// shows the two certificates, and the user trusts them. A wrong password
	// comes first.
	form := connectForm(accountAddress, m, secretPassword)
	status, page := b.post("/connect/save", form)
	require.Equal(t, http.StatusOK, status)
	offers := offerPattern.FindAllStringSubmatch(page, -1)
	require.Len(t, offers, 2, "the page offers to trust the two certificates")
	for _, o := range offers {
		form.Set("trust_"+o[1], "1")
		form.Set(o[1]+"_offer", o[2])
		secretsToFind["certificate offer "+o[1]] = o[2]
	}
	wrong := cloneForm(form)
	wrong.Set("password", secretWrongPassword)
	status, _ = b.post("/connect/save", wrong)
	require.Equal(t, http.StatusUnprocessableEntity, status, "a wrong password is refused")
	status, _ = b.post("/connect/save", form)
	require.Equal(t, http.StatusSeeOther, status, "the account is connected")
	id := h.accountIDOf(accountAddress)
	acct := string(id)

	// The agent reads and organizes, over stdio. The person confirms each
	// question.
	opts, ask := acceptingClient()
	cs := h.client(opts)
	mustOK(t, cs, "list_accounts", nil)
	mustOK(t, cs, "list_folders", map[string]any{"account": acct})
	mustOK(t, cs, "list_tags", map[string]any{"account": acct})
	res := call(t, cs, "trash_messages", map[string]any{"account": acct, "refs": []any{"r1"}})
	require.True(t, res.IsError, "Mail 4 does not allow Trash")
	cursor := mustOK(t, cs, "list_new_messages", map[string]any{"account": acct})["cursor"]
	found := mustOK(t, cs, "search_messages", map[string]any{
		"account": acct, "from": inSenderAddress, "subject": inSubject, "text": inBody,
	})["messages"].([]any)
	require.Len(t, found, 1, "the search finds the message")
	ref := found[0].(map[string]any)["ref"]
	msg := mustOK(t, cs, "get_message", map[string]any{"account": acct, "ref": ref})
	attachments := msg["attachments"].([]any)
	require.Len(t, attachments, 1)
	mustOK(t, cs, "get_attachment", map[string]any{
		"account": acct, "ref": ref, "attachment_id": attachments[0].(map[string]any)["id"],
	})
	mustOK(t, cs, "create_tag", map[string]any{"account": acct, "name": "e2etag"})
	mustOK(t, cs, "tag_messages", map[string]any{"account": acct, "refs": []any{ref}, "add": []any{"e2etag"}})
	mustOK(t, cs, "set_flags", map[string]any{"account": acct, "refs": []any{ref}, "seen": true, "flagged": true})
	mustOK(t, cs, "create_folder", map[string]any{"account": acct, "name": "Projects"})
	moved := mustOK(t, cs, "move_messages", map[string]any{"account": acct, "refs": []any{ref}, "folder": "Projects"})["refs"]
	mustOK(t, cs, "list_new_messages", map[string]any{"account": acct, "cursor": cursor})

	// It drafts, and sends with an in-band confirmation: a new message, the
	// draft, and a message to a recipient that the server refuses.
	draft := mustOK(t, cs, "create_draft", map[string]any{
		"account": acct, "to": []any{rcptName + " <" + rcptAddress + ">"}, "subject": markedSubject(draftSubject), "body": markedBody(draftBody),
	})["ref"]
	sent := mustOK(t, cs, "send_message", sendArgs(id))
	require.Equal(t, "sent", sent["status"])
	mustOK(t, cs, "get_send_status", map[string]any{"account": acct, "send_id": sent["send_id"]})
	require.Equal(t, "sent", mustOK(t, cs, "send_message", map[string]any{"account": acct, "draft_ref": draft})["status"])
	refused := sendArgs(id)
	refused["to"] = []any{rejectedAddress}
	call(t, cs, "send_message", refused)
	require.Len(t, m.smtp.Messages(), 2, "the server refused the third message")

	// The user allows Trash on the settings page, and the agent moves the
	// message to Trash with a confirmation.
	status, _ = b.post("/accounts/"+acct+"/settings", settingsForm(account.MailDelete, true, true, 30))
	require.Equal(t, http.StatusSeeOther, status)
	require.Equal(t, "done", mustOK(t, cs, "trash_messages", map[string]any{"account": acct, "refs": moved})["status"])
	require.Equal(t, 4, ask.count(), "the person was asked for each send and for the move to Trash")

	// A client without elicitation: the send waits for the approval page.
	fallback := h.client(nil)
	waiting := mustOK(t, fallback, "send_message", sendArgs(id))
	require.Equal(t, "pending_approval", waiting["status"])
	approval := pathOf(t, waiting["approval_link"].(string))
	status, page = b.get(approval)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, page, outSubject, "the approval page shows the message to the user")
	status, _ = b.post(approval+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, status)
	require.Len(t, m.smtp.Messages(), 3)

	// A request state that the agent holds, and a "no" for it.
	raw := h.client(rawClient(t))
	state := firstCall(t, raw, "send_message", map[string]any{"account": acct, "to": []any{rcptAddress}, "subject": outSubject, "body": outBody})
	secretsToFind["request state"] = state
	res = answerCall(t, raw, "send_message", map[string]any{"account": acct, "to": []any{rcptAddress}, "subject": outSubject, "body": outBody},
		mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "decline"}}, state)
	require.False(t, res.IsError, textOf(t, res))

	// The agent asks for another level, and the user turns off the send
	// confirmation: a queued send is cancelled, and the next one goes out
	// through the outbox worker after its undo window.
	link := mustOK(t, cs, "request_permission_change", map[string]any{"account": acct, "area": "mail", "level": 4})["link"].(string)
	status, _ = b.get(pathOf(t, link))
	require.Equal(t, http.StatusOK, status)
	status, _ = b.post("/accounts/"+acct+"/settings", settingsForm(account.MailDelete, false, true, 300))
	require.Equal(t, http.StatusSeeOther, status)
	queued := mustOK(t, cs, "send_message", sendArgs(id))
	require.Equal(t, "queued", queued["status"])
	require.Equal(t, "cancelled", mustOK(t, cs, "cancel_send", map[string]any{"account": acct, "send_id": queued["send_id"]})["status"])
	status, _ = b.post("/accounts/"+acct+"/settings", settingsForm(account.MailDelete, false, true, 1))
	require.Equal(t, http.StatusSeeOther, status)
	require.Equal(t, "queued", mustOK(t, cs, "send_message", sendArgs(id))["status"])
	require.Eventually(t, func() bool { return len(m.smtp.Messages()) == 4 }, 15*time.Second, 50*time.Millisecond,
		"the outbox worker sends the queued message")

	// The user makes a token on the connections page, and a client uses it
	// over HTTP. A wrong token is refused. Then the user revokes the token.
	status, page = b.post("/connections", url.Values{"name": {"e2e"}, "scope": {"email:read"}})
	require.Equal(t, http.StatusOK, status)
	tokenMatch := tokenPattern.FindStringSubmatch(page)
	require.NotNil(t, tokenMatch, "the page shows the new token once")
	token := tokenMatch[1]
	secretsToFind["access token"] = token
	ts := h.httpEndpoint()
	hc := httpClient(t, ts, token)
	mustOK(t, hc, "list_accounts", nil)
	mustOK(t, hc, "search_messages", map[string]any{"account": acct, "subject": inSubject})
	wrongToken := token[:len(token)-4] + "AAAA"
	if wrongToken == token {
		wrongToken = token[:len(token)-4] + "BBBB"
	}
	secretsToFind["wrong access token"] = wrongToken
	status, _ = postMCP(t, ts, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+wrongToken) })
	require.Equal(t, http.StatusUnauthorized, status)
	status, page = b.get("/connections")
	require.Equal(t, http.StatusOK, status)
	revoke := regexp.MustCompile(`action="(/connections/[a-z0-9]+/revoke)"`).FindStringSubmatch(page)
	require.NotNil(t, revoke)
	status, _ = b.post(revoke[1], nil)
	require.Equal(t, http.StatusSeeOther, status)

	// The user looks at the activity and the approvals.
	for _, p := range []string{"/", "/activity", "/approvals", "/connections"} {
		status, _ = b.get(p)
		require.Equal(t, http.StatusOK, status, p)
	}

	// Requests that the web interface refuses: no session, a wrong Host, and a
	// post from another origin.
	status, _ = h.stranger().get(approval)
	require.Equal(t, http.StatusUnauthorized, status)
	req := b.request(http.MethodGet, approval, nil)
	req.Host = "evil.example"
	resp, _ := b.do(req)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	req = b.request(http.MethodPost, approval+"/approve", url.Values{"csrf_token": {b.csrf}})
	req.Header.Set("Origin", "http://evil.example")
	resp, _ = b.do(req)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// The background work logs too: the daily jobs run when Run starts, in a
	// goroutine of their own.
	require.Eventually(t, func() bool { return strings.Contains(h.logs.String(), "app: daily jobs done") },
		15*time.Second, 50*time.Millisecond, "the daily jobs log their line")

	// The whole stop of the program.
	stopRun()
	h.close()

	logs := h.logs.String()
	require.Contains(t, logs, "e2e: the log has Debug lines", "the log is captured at Debug level")
	for _, line := range []string{"web request refused", "http request refused"} {
		require.Contains(t, logs, line, "the log has the lines of all parts")
	}
	requireNoSecrets(t, "the log", []byte(logs), secretsToFind)
}

// ---------------------------------------------------------------- the database

// TestNoPlaintextSecretsInDB: the files of the database hold none of the test
// secrets in plain text (spec 9.2, 15.4): not the password, not a message, not
// an address of a third party, not a token.
//
// It reads the raw bytes of every file in the data directory (the database, its
// write-ahead log and its shared-memory file; the master key file is kept
// elsewhere). It reads them while sends wait (in the outbox for their undo
// window, and for approval) and after they are sent, each time before and
// after a checkpoint that cuts the log.
func TestNoPlaintextSecretsInDB(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t, smtptest.RejectRcpt(rejectedAddress))
	m.imap.Append("INBOX", incomingMail())
	a := h.connect(localCtx(), accountAddress, m)
	acct := string(a.ID)

	secretsToFind := allSecrets()
	// The address of the account is in plain text by design (the accounts
	// table, spec section 9.1). It is the only test secret that is left out.
	delete(secretsToFind, "account address")

	// A signed-in browser, a setup link that nobody used, and an access token.
	b, setupURL := h.signIn()
	unused, err := h.app.Web.NewSetupURL(context.Background())
	require.NoError(t, err)
	token, err := h.app.Service.CreateToken(localCtx(), "e2e", policy.Scopes{"email:read"})
	require.NoError(t, err)
	secretsToFind["used setup token"] = setupTokenOf(t, setupURL)
	secretsToFind["unused setup token"] = setupTokenOf(t, unused)
	secretsToFind["session id"] = b.cookie
	secretsToFind["access token"] = token

	// The agent reads the message and saves a draft. Then it sends with an
	// in-band confirmation: the message goes out at once.
	opts, _ := acceptingClient()
	cs := h.client(opts)
	ref := refsIn(t, cs, a.ID, "@inbox")[0]
	mustOK(t, cs, "get_message", map[string]any{"account": acct, "ref": ref})
	mustOK(t, cs, "create_draft", map[string]any{"account": acct, "to": []any{rcptAddress}, "subject": markedSubject(draftSubject), "body": markedBody(draftBody)})
	require.Equal(t, "sent", mustOK(t, cs, "send_message", sendArgs(a.ID))["status"])

	// A send that waits in band: the agent holds its request state.
	raw := h.client(rawClient(t))
	secretsToFind["request state"] = firstCall(t, raw, "send_message", sendArgs(a.ID))

	// A send that waits for the approval page.
	approval := mustOK(t, h.client(nil), "send_message", sendArgs(a.ID))
	require.Equal(t, "pending_approval", approval["status"])

	// Without the send confirmation: a send that the server refuses (its error
	// is stored), and a send that waits in the outbox for its undo window.
	u := settingsOf(account.MailSend)
	u.ConfirmSends = false
	u.Limits.UndoWindowAuto = 0
	h.update(a.ID, u)
	refusedArgs := sendArgs(a.ID)
	refusedArgs["to"] = []any{rejectedAddress}
	call(t, cs, "send_message", refusedArgs)
	u.Limits.UndoWindowAuto = 2 * time.Second
	h.update(a.ID, u)
	require.Equal(t, "queued", mustOK(t, cs, "send_message", sendArgs(a.ID))["status"])

	db := h.sqlDB()
	require.Equal(t, 1, count(t, db, `SELECT COUNT(*) FROM outbox WHERE status = 'queued' AND message IS NOT NULL`),
		"a message waits in the outbox")
	require.Equal(t, 2, count(t, db, `SELECT COUNT(*) FROM pending_actions WHERE state = 'pending' AND payload IS NOT NULL`),
		"two sends wait for the user")
	require.Equal(t, 1, count(t, db, `SELECT COUNT(*) FROM outbox WHERE status = 'failed'`), "the refused send failed")

	scan := func(stage string) {
		t.Helper()
		files := h.dataFiles()
		require.Contains(t, files, store.DBFileName)
		var all []byte
		for name, data := range files {
			requireNoSecrets(t, stage+": "+name, data, secretsToFind)
			all = append(all, data...)
		}
		// The scan reads the right bytes: the address of the account is there.
		require.Equal(t, "plain text", findSecret(t, all, accountAddress), "%s: the scan finds the address of the account", stage)
	}
	require.NotEmpty(t, h.dataFiles()[store.DBFileName+"-wal"], "the log has the recent writes")
	scan("while sends wait, before a checkpoint")
	h.checkpoint()
	require.Empty(t, h.dataFiles()[store.DBFileName+"-wal"], "the checkpoint cut the log")
	scan("while sends wait, after a checkpoint")

	// The user approves on the web page, and the outbox worker sends the
	// queued message.
	status, _ := b.post(pathOf(t, approval["approval_link"].(string))+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, status)
	runCtx, stopRun := context.WithCancel(context.Background())
	h.app.Run(runCtx)
	require.Eventually(t, func() bool { return len(m.smtp.Messages()) == 3 }, 15*time.Second, 50*time.Millisecond,
		"the approved send and the queued send go out")
	require.Eventually(t, func() bool {
		return count(t, db, `SELECT COUNT(*) FROM outbox WHERE message IS NOT NULL`) == 0
	}, 15*time.Second, 50*time.Millisecond, "a sent message is deleted from the outbox")
	stopRun()

	scan("after the sends, before a checkpoint")
	h.checkpoint()
	scan("after the sends, after a checkpoint")
}

// ---------------------------------------------------------------- levels

// requiredLevel is the mail level that each tool needs (spec section 6.3). Every
// tool that the server lists must have a row: a new tool fails
// TestEveryWriteToolRefusedBelowItsLevel until its level is written here.
// Level 0 means that the tool needs no level: list_accounts reads only the
// account list, and the two link tools change nothing (the test checks that).
var requiredLevel = map[string]account.Level{
	"list_accounts":             account.MailOff,
	"request_permission_change": account.MailOff,
	"connect_account":           account.MailOff,
	"list_folders":              account.MailRead,
	"list_tags":                 account.MailRead,
	"search_messages":           account.MailRead,
	"list_new_messages":         account.MailRead,
	"get_message":               account.MailRead,
	"get_attachment":            account.MailRead,
	"get_send_status":           account.MailRead,
	"set_flags":                 account.MailOrganize,
	"create_folder":             account.MailOrganize,
	"create_tag":                account.MailOrganize,
	"tag_messages":              account.MailOrganize,
	"move_messages":             account.MailOrganize,
	"create_draft":              account.MailDraft,
	"discard_draft":             account.MailDraft,
	"send_message":              account.MailSend,
	"cancel_send":               account.MailSend,
	"trash_messages":            account.MailDelete,
}

// sideEffects is what a refused call must leave unchanged:
//   - every command that reached the IMAP server, reads and logins too (a
//     refused call must not touch the mail server at all), and the SELECT and
//     EXAMINE counts;
//   - the connections and the messages of the SMTP server;
//   - the pending actions and the outbox.
type sideEffects struct {
	commands  []string
	readWrite int
	readOnly  int
	smtpConns int
	smtp      int
	pending   int
	outbox    string
}

func snapshotEffects(t *testing.T, h *harness, m mailServers) sideEffects {
	t.Helper()
	rw, ro := m.imap.SelectCounts()
	db := h.sqlDB()
	rows, err := db.Query(`SELECT id, status FROM outbox ORDER BY id`)
	require.NoError(t, err)
	var outboxRows []string
	for rows.Next() {
		var id, status string
		require.NoError(t, rows.Scan(&id, &status))
		outboxRows = append(outboxRows, id+"="+status)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return sideEffects{
		commands:  m.imap.Commands(),
		readWrite: rw,
		readOnly:  ro,
		smtpConns: m.smtp.Connections(),
		smtp:      len(m.smtp.Messages()),
		pending:   count(t, db, `SELECT COUNT(*) FROM pending_actions`),
		outbox:    strings.Join(outboxRows, ","),
	}
}

// TestEveryWriteToolRefusedBelowItsLevel: every tool that the server lists is
// refused at every level below the one it needs, with the permission error
// and its settings link, and the call reaches neither the mail servers nor the
// stored actions (spec 7.1, 15.4). The client accepts every confirmation, so
// a call that got past the level would act.
func TestEveryWriteToolRefusedBelowItsLevel(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t)
	m.imap.Append("INBOX", incomingMail())
	m.imap.Append("INBOX", otherMail(1))
	a := h.connect(localCtx(), accountAddress, m)
	acct := string(a.ID)
	opts, ask := acceptingClient()
	cs := h.client(opts)

	// At the highest level, without the send confirmation, the test makes what
	// the calls need: refs, an attachment id, a draft and a queued send.
	top := settingsOf(account.MailDelete)
	top.ConfirmSends = false
	top.Limits.UndoWindowAuto = 300 * time.Second
	h.update(a.ID, top)
	refs := refsIn(t, cs, a.ID, "@inbox")
	require.Len(t, refs, 2)
	var withAttachment any
	var attachmentID any
	for _, r := range refs {
		msg := mustOK(t, cs, "get_message", map[string]any{"account": acct, "ref": r})
		if atts, _ := msg["attachments"].([]any); len(atts) == 1 {
			withAttachment, attachmentID = r, atts[0].(map[string]any)["id"]
		}
	}
	require.NotNil(t, attachmentID)
	draftRef := mustOK(t, cs, "create_draft", map[string]any{"account": acct, "subject": markedSubject(draftSubject), "body": markedBody(draftBody)})["ref"]
	queued := mustOK(t, cs, "send_message", sendArgs(a.ID))
	require.Equal(t, "queued", queued["status"])

	args := map[string]map[string]any{
		"list_accounts":             nil,
		"request_permission_change": {"account": acct, "area": "mail", "level": 5},
		"connect_account":           nil,
		"list_folders":              {"account": acct},
		"list_tags":                 {"account": acct},
		"search_messages":           {"account": acct},
		"list_new_messages":         {"account": acct},
		"get_message":               {"account": acct, "ref": withAttachment},
		"get_attachment":            {"account": acct, "ref": withAttachment, "attachment_id": attachmentID},
		"get_send_status":           {"account": acct, "send_id": queued["send_id"]},
		"set_flags":                 {"account": acct, "refs": refs, "seen": true, "flagged": true},
		"create_folder":             {"account": acct, "name": "NewFolder"},
		"create_tag":                {"account": acct, "name": "newtag"},
		"tag_messages":              {"account": acct, "refs": refs, "add": []any{"newtag"}},
		"move_messages":             {"account": acct, "refs": refs, "folder": "Archive"},
		"create_draft":              {"account": acct, "to": []any{rcptAddress}, "subject": markedSubject(draftSubject), "body": markedBody(draftBody)},
		"discard_draft":             {"account": acct, "ref": draftRef},
		"send_message":              sendArgs(a.ID),
		"cancel_send":               {"account": acct, "send_id": queued["send_id"]},
		"trash_messages":            {"account": acct, "refs": refs},
	}

	list, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		_, ok := requiredLevel[tool.Name]
		require.True(t, ok, "the tool %s has no row in requiredLevel: write the level it needs there, so the test checks it", tool.Name)
		_, ok = args[tool.Name]
		require.True(t, ok, "the tool %s has no arguments in this test", tool.Name)
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		if !readOnly && requiredLevel[tool.Name] == account.MailOff {
			require.Contains(t, []string{"request_permission_change", "connect_account"}, tool.Name,
				"a tool that may change something needs a level")
		}
	}
	require.ElementsMatch(t, slices.Collect(maps.Keys(requiredLevel)), names, "requiredLevel lists exactly the tools of the server")
	slices.Sort(names)

	for _, name := range names {
		need := requiredLevel[name]
		for level := account.MailOff; level < need; level++ {
			t.Run(fmt.Sprintf("%s at level %d", name, level), func(t *testing.T) {
				h.update(a.ID, settingsOf(level))
				before := snapshotEffects(t, h, m)

				res := call(t, cs, name, args[name])

				require.True(t, res.IsError, "%s must be refused at level %d", name, level)
				text := textOf(t, res)
				require.Contains(t, text, fmt.Sprintf("/accounts/%s/settings?area=mail&level=%d", acct, need),
					"the refusal is the permission error, with the link to raise the level: %s", text)
				after := snapshotEffects(t, h, m)
				require.Equal(t, before.commands, after.commands, "a refused call sends no command to the IMAP server, not even a read")
				require.Zero(t, after.readOnly-before.readOnly, "a refused call opens no folder to read")
				require.Equal(t, before, after, "a refused call changes nothing")
			})
		}
	}

	t.Run("the tools that need no level change nothing", func(t *testing.T) {
		h.update(a.ID, settingsOf(account.MailOff))
		db := h.sqlDB()
		before, effects := dump(t, db), snapshotEffects(t, h, m)
		for _, name := range []string{"list_accounts", "request_permission_change", "connect_account"} {
			mustOK(t, cs, name, args[name])
		}
		require.Equal(t, effects, snapshotEffects(t, h, m), "the mail servers saw nothing")
		require.Equal(t, before, dump(t, db), "nothing changed in the database")
		views, err := h.app.Service.ListAccounts(localCtx())
		require.NoError(t, err)
		require.Equal(t, "Off", views[0].Areas[account.AreaMail])
	})

	require.Zero(t, ask.count(), "no call got as far as asking the person")
	h.update(a.ID, settingsOf(account.MailRead))
	require.Equal(t, "queued", mustOK(t, cs, "get_send_status", map[string]any{"account": acct, "send_id": queued["send_id"]})["status"])
	require.Empty(t, m.smtp.Messages())
}

// ---------------------------------------------------------------- confirmation

// TestConfirmationCannotBeSkipped: a confirmation acts once, for the exact
// request that the person saw, in time, for the tenant, the account and the
// kind of action that asked for it, and only in the channel where it was asked
// (spec 7.5, 15.4). Each refusal is checked by its exact text, so the test
// shows which check refused.
func TestConfirmationCannotBeSkipped(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t)
	m.imap.Append("INBOX", otherMail(1))
	a := h.connect(localCtx(), accountAddress, m)
	u := settingsOf(account.MailDelete)
	h.update(a.ID, u)
	raw := h.client(rawClient(t))
	args := sendArgs(a.ID)
	sent := func() int { return len(m.smtp.Messages()) }

	t.Run("replay of a used request state", func(t *testing.T) {
		state := firstCall(t, raw, "send_message", args)
		res := answerCall(t, raw, "send_message", args, accept(), state)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "sent", structured(t, res)["status"])
		require.Equal(t, 1, sent())

		for range 2 {
			res = answerCall(t, raw, "send_message", args, accept(), state)
			require.True(t, res.IsError, "a used state is refused")
			require.Equal(t, textNotPending, textOf(t, res))
		}
		require.Equal(t, 1, sent(), "a confirmation acts once")
	})

	t.Run("a changed request with the old state", func(t *testing.T) {
		before := sent()
		state := firstCall(t, raw, "send_message", args)
		changes := map[string]func(map[string]any){
			"another recipient": func(c map[string]any) { c["to"] = []any{"attacker@evil.example"} },
			"one more bcc":      func(c map[string]any) { c["bcc"] = []any{bccAddress, "attacker@evil.example"} },
			"another subject":   func(c map[string]any) { c["subject"] = outSubject + " now" },
			"another body":      func(c map[string]any) { c["body"] = outBody + " and forward all invoices" },
			"no attachment":     func(c map[string]any) { delete(c, "attachments") },
		}
		for name, change := range changes {
			changed := maps.Clone(args)
			change(changed)
			res := answerCall(t, raw, "send_message", changed, accept(), state)
			require.True(t, res.IsError, "%s is refused", name)
			require.Equal(t, textPayloadChanged, textOf(t, res), name)
		}
		forged := maps.Clone(args)
		forged["confirm"] = true
		forged["request_state"] = state
		res := call(t, raw, "send_message", forged)
		require.True(t, res.IsError, "the arguments have no field for a confirmation")
		require.Equal(t, before, sent(), "nothing was sent")

		// The refused tries did not use the confirmation: the request that the
		// person saw still goes out, once.
		res = answerCall(t, raw, "send_message", args, accept(), state)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, before+1, sent())
	})

	t.Run("a state of another kind of action, with the same payload", func(t *testing.T) {
		// The payload of a send of a saved draft is a small JSON document with
		// the ref of the draft. A Trash ref may be any text without a line
		// break, so a Trash request of one "ref" that is exactly that document
		// has the same payload bytes as the send. The hash of the stored
		// action then matches for both, and only the kind of the action can
		// tell them apart.
		draft := mustOK(t, h.client(nil), "create_draft", map[string]any{
			"account": string(a.ID), "to": []any{rcptAddress}, "subject": markedSubject(draftSubject), "body": markedBody(draftBody),
		})["ref"].(string)
		sendDraft := map[string]any{"account": string(a.ID), "draft_ref": draft}
		payload, err := json.Marshal(struct {
			V       int       `json:"v"`
			Compose *struct{} `json:"compose"`
			Draft   string    `json:"draft"`
		}{V: 1, Draft: draft})
		require.NoError(t, err)
		trashSame := map[string]any{"account": string(a.ID), "refs": []any{string(payload)}}

		before := sent()
		known := pendingIDs(t, h, confirm.StatePending)
		trashState := firstCall(t, raw, "trash_messages", trashSame)
		sendState := firstCall(t, raw, "send_message", sendDraft)
		var waiting []string
		for _, id := range pendingIDs(t, h, confirm.StatePending) {
			if !slices.Contains(known, id) {
				waiting = append(waiting, id)
			}
		}
		require.Len(t, waiting, 2)

		// The text is the one of a state that belongs to another request, not
		// the one of a changed payload: the payload hash matched.
		res := answerCall(t, raw, "send_message", sendDraft, accept(), trashState)
		require.True(t, res.IsError, "a Trash confirmation cannot send")
		require.Equal(t, textStateInvalid, textOf(t, res))
		res = answerCall(t, raw, "trash_messages", trashSame, accept(), sendState)
		require.True(t, res.IsError, "a send confirmation cannot move mail to Trash")
		require.Equal(t, textStateInvalid, textOf(t, res))
		for _, id := range waiting {
			require.Equal(t, string(confirm.StatePending), stateOf(t, h, id), "a refused try does not use up the action")
		}
		require.Equal(t, before, sent())
		require.Len(t, refsIn(t, h.client(nil), a.ID, "@inbox"), 1, "nothing moved")

		// The send state still sends the draft that the person saw.
		res = answerCall(t, raw, "send_message", sendDraft, accept(), sendState)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, before+1, sent())
	})

	t.Run("a state of another account, with the same payload", func(t *testing.T) {
		// The payload of a send holds the message, not the account, so the
		// same message for another account of the tenant has the same payload
		// hash. Only the account of the action can tell them apart.
		before := sent()
		m2 := newMailServers(t)
		other := h.connect(localCtx(), "other-q4z8@example.net", m2)
		h.update(other.ID, u)
		state := firstCall(t, raw, "send_message", args)

		res := answerCall(t, raw, "send_message", sendArgs(other.ID), accept(), state)
		require.True(t, res.IsError, "a confirmation of one account cannot send from another")
		require.Equal(t, textStateInvalid, textOf(t, res), "the payload hash matched; the account refused")
		require.Empty(t, m2.smtp.Messages())
		require.Equal(t, before, sent())

		// The refused try did not use the confirmation.
		res = answerCall(t, raw, "send_message", args, accept(), state)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, before+1, sent())
	})

	t.Run("a state of another tenant", func(t *testing.T) {
		before := sent()
		// A local install has one tenant. The test adds a second one, as hosted
		// mode will, and connects an account in it.
		_, err := h.sqlDB().Exec(`INSERT INTO tenants (id, created_at) VALUES ('tenant-b', 0)`)
		require.NoError(t, err)
		tenantB := service.Principal{Tenant: "tenant-b", Client: "e2e-b"}
		accountB := h.connect(service.WithPrincipal(context.Background(), tenantB), "tenant-b-k3v7@example.net", m)
		serverB := h.app.MCPServer(mcpserver.Options{
			InBandLegacyElicitation: true,
			Principal: func(context.Context, *mcp.CallToolRequest) (service.Principal, error) {
				return tenantB, nil
			},
		})
		rawB := connectClient(t, serverB, rawClient(t))
		argsB := sendArgs(accountB.ID)
		stateB := firstCall(t, rawB, "send_message", argsB)

		res := answerCall(t, raw, "send_message", args, accept(), stateB)
		require.True(t, res.IsError, "a state of tenant B cannot confirm a send of the local tenant")
		require.Equal(t, textStateInvalid, textOf(t, res))
		res = answerCall(t, raw, "send_message", argsB, accept(), stateB)
		require.True(t, res.IsError, "the local tenant cannot use the account or the state of tenant B")
		require.Equal(t, before, sent())

		// In tenant B, the state still works.
		res = answerCall(t, rawB, "send_message", argsB, accept(), stateB)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, before+1, sent())
	})

	t.Run("an in-band action approved on the web page", func(t *testing.T) {
		before := sent()
		known := pendingIDs(t, h, confirm.StatePending)
		state := firstCall(t, raw, "send_message", args)
		var id string
		for _, p := range pendingIDs(t, h, confirm.StatePending) {
			if !slices.Contains(known, p) {
				id = p
			}
		}
		require.NotEmpty(t, id)

		b, _ := h.signIn()
		status, page := b.get("/approvals")
		require.Equal(t, http.StatusOK, status)
		require.NotContains(t, page, id, "the approvals page does not list an action that waits in the chat")
		require.NotContains(t, page, outSubject)
		status, page = b.get("/approvals/" + id)
		require.Equal(t, http.StatusNotFound, status)
		require.NotContains(t, page, outSubject)
		for _, action := range []string{"approve", "reject"} {
			status, page = b.post("/approvals/"+id+"/"+action, nil)
			require.Equal(t, http.StatusConflict, status, action)
			require.Contains(t, page, "cannot be approved or rejected here", action)
			require.Equal(t, string(confirm.StatePending), stateOf(t, h, id), "%s on the web page changes nothing", action)
		}
		require.Equal(t, before, sent())

		// The answer in the chat still works.
		res := answerCall(t, raw, "send_message", args, accept(), state)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, before+1, sent())
	})

	t.Run("an expired request state", func(t *testing.T) {
		// The same program as above, with a clock that the test moves
		// (app.Config.Now), so the test does not wait 10 minutes.
		clock := &fakeClock{now: time.Now()}
		hc := newHarnessWith(t, harnessOptions{now: clock.Now})
		mc := newMailServers(t)
		ac := hc.connect(localCtx(), accountAddress, mc)
		rawC := hc.client(rawClient(t))
		argsC := sendArgs(ac.ID)

		state := firstCall(t, rawC, "send_message", argsC)
		clock.Advance(confirm.InBandLifetime + time.Second)
		res := answerCall(t, rawC, "send_message", argsC, accept(), state)
		require.True(t, res.IsError, "an expired state is refused")
		require.Equal(t, textStateExpired, textOf(t, res), "the request state itself has expired")
		require.Empty(t, mc.smtp.Messages())

		state = firstCall(t, rawC, "send_message", argsC)
		clock.Advance(confirm.InBandLifetime - time.Minute)
		res = answerCall(t, rawC, "send_message", argsC, accept(), state)
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "sent", structured(t, res)["status"])
		require.Len(t, mc.smtp.Messages(), 1)
	})
}

// fakeClock is a clock that a test moves.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---------------------------------------------------------------- Trash

// TestTrashMoveBlockedBelowLevel5: below Mail 5, no tool and no approval moves
// mail to Trash, by any path: move_messages to the Trash folder (by alias, by
// name, in another case), trash_messages, the system flag \Deleted, and
// discard_draft on a message that is not a draft (spec 6.3, 7.1, 15.4). At
// Mail 5, move_messages to Trash is still refused: only trash_messages moves
// mail there, with its confirmation and its daily limit.
func TestTrashMoveBlockedBelowLevel5(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t)
	m.imap.Append("INBOX", otherMail(1))
	m.imap.Append("INBOX", otherMail(2))
	a := h.connect(localCtx(), accountAddress, m)
	acct := string(a.ID)
	opts, ask := acceptingClient()
	cs := h.client(opts)
	refs := refsIn(t, cs, a.ID, "@inbox")
	require.Len(t, refs, 2)
	removing := func() []string {
		var out []string
		for _, c := range m.imap.WriteCommands() {
			if c == "MOVE" || c == "COPY" || c == "EXPUNGE" || c == "STORE" {
				out = append(out, c)
			}
		}
		return out
	}

	for _, level := range []account.Level{account.MailOrganize, account.MailDraft, account.MailSend} {
		t.Run(fmt.Sprintf("level %d", level), func(t *testing.T) {
			h.update(a.ID, settingsOf(level))
			for _, folder := range []string{"@trash", "Trash", "TRASH", "trash"} {
				res := call(t, cs, "move_messages", map[string]any{"account": acct, "refs": refs, "folder": folder})
				require.True(t, res.IsError, "a move to %s is refused", folder)
				require.Contains(t, textOf(t, res), "trash_messages", folder)
			}
			res := call(t, cs, "trash_messages", map[string]any{"account": acct, "refs": refs})
			require.True(t, res.IsError)
			require.Contains(t, textOf(t, res), "?area=mail&level=5")
			res = call(t, cs, "tag_messages", map[string]any{"account": acct, "refs": refs, "add": []any{`\Deleted`}})
			require.True(t, res.IsError, "the system flag \\Deleted is not a tag")
			if level >= account.MailDraft {
				res = call(t, cs, "discard_draft", map[string]any{"account": acct, "ref": refs[0]})
				require.True(t, res.IsError, "discard_draft moves only a draft to Trash")
			}
			require.Empty(t, removing(), "no command that moves or deletes mail reached the server")
		})
	}

	t.Run("level 5", func(t *testing.T) {
		h.update(a.ID, settingsOf(account.MailDelete))
		res := call(t, cs, "move_messages", map[string]any{"account": acct, "refs": refs, "folder": "@trash"})
		require.True(t, res.IsError, "move_messages never moves to Trash")
		require.Empty(t, removing())
	})

	t.Run("an approval after the level went down", func(t *testing.T) {
		h.update(a.ID, settingsOf(account.MailDelete))
		waiting := mustOK(t, h.client(nil), "trash_messages", map[string]any{"account": acct, "refs": refs})
		require.Equal(t, "pending_approval", waiting["status"])
		h.update(a.ID, settingsOf(account.MailSend))

		b, _ := h.signIn()
		status, page := b.post(pathOf(t, waiting["approval_link"].(string))+"/approve", nil)
		require.Equal(t, http.StatusForbidden, status)
		require.Contains(t, page, "does not allow this action any more")
		require.Empty(t, removing())
	})

	require.Zero(t, ask.count(), "no move to Trash got as far as asking the person")
	h.update(a.ID, settingsOf(account.MailRead))
	require.Len(t, refsIn(t, cs, a.ID, "@inbox"), 2, "the messages are still in the inbox")
	require.Empty(t, refsIn(t, cs, a.ID, "@trash"), "Trash is empty")
}

// ---------------------------------------------------------------- plain text

// TestPlainTextToRemoteRefused: a connection without TLS is allowed only to a
// mail server on this computer, on a local install. Plain text to any other
// host is refused before anything is sent, when an account is connected (by the
// service and on the web page) and when a stored account is used. A server that
// does not offer STARTTLS never gets the password (spec 8.5, 10.2, 15.4).
//
// The refused remote hosts are names and TEST-NET addresses (RFC 2606, RFC
// 5737). The checks never resolve a name, and they refuse before they dial, so
// no packet leaves this computer. Each call also has a short deadline. Where a
// test must prove that nothing was dialled, it uses a plain-text server on this
// computer that the rule refuses (a hosted install refuses plain text even
// there), and counts the connections that reach it.
func TestPlainTextToRemoteRefused(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t)
	n := 0
	tryConnect := func(t *testing.T, change func(*account.MailServer)) error {
		t.Helper()
		settings := m.settings()
		change(&settings)
		n++
		ctx, cancel := context.WithTimeout(localCtx(), 5*time.Second)
		defer cancel()
		_, err := h.app.Service.CreateIMAPAccount(ctx, fmt.Sprintf("plain-%d@example.net", n), settings, secrets.Secret(secretPassword))
		return err
	}

	remote := []string{"192.0.2.10", "mail.example.com", "127.0.0.1.example.com", "localhost.example.org", "0.0.0.0", "::", "198.51.100.7"}
	for _, host := range remote {
		t.Run("imap plain to "+host, func(t *testing.T) {
			err := tryConnect(t, func(s *account.MailServer) {
				s.IMAPHost, s.IMAPSecurity, s.PinnedIMAPCert = host, account.SecurityPlain, ""
			})
			require.ErrorIs(t, err, service.ErrInvalidSettings)
		})
		t.Run("smtp plain to "+host, func(t *testing.T) {
			err := tryConnect(t, func(s *account.MailServer) {
				s.SMTPHost, s.SMTPSecurity, s.PinnedSMTPCert = host, account.SecurityPlain, ""
			})
			require.ErrorIs(t, err, service.ErrInvalidSettings)
		})
	}
	views, err := h.app.Service.ListAccounts(localCtx())
	require.NoError(t, err)
	require.Empty(t, views, "no account was connected")

	t.Run("on the web page", func(t *testing.T) {
		b, _ := h.signIn()
		// One server is remote in plain text. The other one is a test server on
		// this computer: a connection test that ran anyway would reach it.
		for _, plain := range []string{"imap", "smtp"} {
			form := connectForm("plain-web@example.net", m, secretPassword)
			form.Set(plain+"_host", "192.0.2.10")
			form.Set(plain+"_port", "143")
			form.Set(plain+"_security", string(account.SecurityPlain))
			form.Set("plain_understand", "1")
			imapConns, smtpConns := m.imap.TotalConns(), m.smtp.Connections()

			status, page := b.post("/connect/save", form)

			require.Equal(t, http.StatusUnprocessableEntity, status, plain)
			require.Contains(t, page, template.HTMLEscapeString(connect.TextSettings),
				"%s: the page says that the settings are refused, not that a test failed", plain)
			require.NotContains(t, page, secretPassword, "the page never shows the password again")
			require.Equal(t, imapConns, m.imap.TotalConns(), "%s: nothing connected to the IMAP server", plain)
			require.Equal(t, smtpConns, m.smtp.Connections(), "%s: nothing connected to the SMTP server", plain)
		}
		views, err := h.app.Service.ListAccounts(localCtx())
		require.NoError(t, err)
		require.Empty(t, views)
	})

	t.Run("a stored account changed to plain text is never dialled", func(t *testing.T) {
		// A hosted install refuses plain text to every host. Its account is
		// connected with TLS, and then someone with access to the database
		// points it at plain-text servers on this computer. Those servers count
		// every connection, so the test proves that the program never dials.
		hosted := newHarnessWith(t, harnessOptions{hosted: true})
		a := hosted.connect(localCtx(), accountAddress, m)
		imapPlain := imaptest.New(t, imaptest.WithMode(imaptest.ModePlain), imaptest.WithLogin(testUser, secretPassword))
		smtpPlain := smtptest.New(t, smtptest.WithMode(smtptest.ModePlain), smtptest.WithLogin(testUser, secretPassword))
		_, err := hosted.sqlDB().Exec(`UPDATE accounts SET mail = json_set(mail,
			'$.IMAPHost', ?, '$.IMAPPort', ?, '$.IMAPSecurity', 'plain', '$.PinnedIMAPCert', '',
			'$.SMTPHost', ?, '$.SMTPPort', ?, '$.SMTPSecurity', 'plain', '$.PinnedSMTPCert', '')
			WHERE id = ?`, imapPlain.Host, imapPlain.Port, smtpPlain.Host, smtpPlain.Port, string(a.ID))
		require.NoError(t, err)
		// Saving the settings drops any backend that the program keeps, so the
		// next call reads the changed settings.
		u := settingsOf(account.MailSend)
		u.ConfirmSends = false
		u.Limits.UndoWindowAuto = 0
		hosted.update(a.ID, u)

		cs := hosted.client(nil)
		res := call(t, cs, "list_folders", map[string]any{"account": string(a.ID)})
		require.True(t, res.IsError, "the IMAP connection is refused")
		res = call(t, cs, "send_message", sendArgs(a.ID))
		if !res.IsError {
			require.NotEqual(t, "sent", structured(t, res)["status"], "the SMTP connection is refused")
		}
		require.Zero(t, imapPlain.TotalConns(), "nothing connected to the plain-text IMAP server")
		require.Zero(t, smtpPlain.Connections(), "nothing connected to the plain-text SMTP server")
		require.Empty(t, smtpPlain.Messages())
	})

	t.Run("a server without STARTTLS never gets the password", func(t *testing.T) {
		noTLS := smtptest.New(t, smtptest.WithMode(smtptest.ModeNoSTARTTLS), smtptest.WithLogin(testUser, secretPassword))
		err := tryConnect(t, func(s *account.MailServer) {
			s.SMTPHost, s.SMTPPort, s.SMTPSecurity, s.PinnedSMTPCert = noTLS.Host, noTLS.Port, account.SecuritySTARTTLS, ""
		})
		require.Error(t, err)
		require.Zero(t, noTLS.AuthAttempts(), "the password was not sent")
	})

	t.Run("plain text on this computer works on a local install", func(t *testing.T) {
		imapPlain := imaptest.New(t, imaptest.WithMode(imaptest.ModePlain), imaptest.WithLogin(testUser, secretPassword))
		smtpPlain := smtptest.New(t, smtptest.WithMode(smtptest.ModePlain), smtptest.WithLogin(testUser, secretPassword))
		err := tryConnect(t, func(s *account.MailServer) {
			*s = account.MailServer{
				IMAPHost: imapPlain.Host, IMAPPort: imapPlain.Port, IMAPSecurity: account.SecurityPlain,
				SMTPHost: smtpPlain.Host, SMTPPort: smtpPlain.Port, SMTPSecurity: account.SecurityPlain,
				Username: testUser,
			}
		})
		require.NoError(t, err)
	})

	t.Run("a hosted install refuses plain text to this computer too", func(t *testing.T) {
		hosted := newHarnessWith(t, harnessOptions{hosted: true})
		imapPlain := imaptest.New(t, imaptest.WithMode(imaptest.ModePlain), imaptest.WithLogin(testUser, secretPassword))
		smtpConns := m.smtp.Connections()
		_, err := hosted.app.Service.CreateIMAPAccount(localCtx(), "hosted@example.net", account.MailServer{
			IMAPHost: imapPlain.Host, IMAPPort: imapPlain.Port, IMAPSecurity: account.SecurityPlain,
			SMTPHost: m.smtp.Host, SMTPPort: m.smtp.Port, SMTPSecurity: account.SecurityTLS,
			Username: testUser, PinnedSMTPCert: m.smtp.CertSHA256,
		}, secrets.Secret(secretPassword))
		require.ErrorIs(t, err, service.ErrInvalidSettings)
		require.Zero(t, imapPlain.TotalConns(), "nothing connected to the plain-text server")
		require.Equal(t, smtpConns, m.smtp.Connections(), "the test of the settings did not run")
	})
}

// ---------------------------------------------------------------- certificates

// loginsOn counts the logins that reached the two servers: the IMAP LOGIN
// commands and the SMTP AUTH attempts.
func loginsOn(m mailServers) [2]int {
	n := 0
	for _, c := range m.imap.Commands() {
		if c == "LOGIN" {
			n++
		}
	}
	return [2]int{n, m.smtp.AuthAttempts()}
}

// TestTLSCertificateChecksEnforced: TLS is enforced with the certificate
// checks, so a server in the middle cannot get the password (spec 8.5, 10.2,
// 15.4). A pinned account is refused when the server shows another
// certificate. Without a pin, a certificate that no trusted authority signed
// is refused, and so is a certificate for another host name; the connect page
// never offers to trust a certificate for another host name, and an offer
// that it made for one host cannot pin the certificate for another. In every
// case no login reaches the server and no tool succeeds.
//
// The test servers each make their own self-signed certificate for 127.0.0.1.
// The "decoy" servers stand for the certificate that the user trusted; the
// servers of the account then show a different one, as an attacker would.
func TestTLSCertificateChecksEnforced(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t)
	decoy := newMailServers(t)
	n := 0
	tryConnect := func(t *testing.T, change func(*account.MailServer)) *service.CertificateError {
		t.Helper()
		settings := m.settings()
		change(&settings)
		n++
		ctx, cancel := context.WithTimeout(localCtx(), 15*time.Second)
		defer cancel()
		_, err := h.app.Service.CreateIMAPAccount(ctx, fmt.Sprintf("tls-%d@example.net", n), settings, secrets.Secret(secretPassword))
		var certErr *service.CertificateError
		require.ErrorAs(t, err, &certErr, "the connect is refused for the certificate")
		return certErr
	}

	t.Run("connect: the IMAP server shows another certificate than the pin", func(t *testing.T) {
		before := loginsOn(m)
		certErr := tryConnect(t, func(s *account.MailServer) { s.PinnedIMAPCert = decoy.imap.CertSHA256 })
		require.NotNil(t, certErr.IMAP)
		require.True(t, certErr.IMAP.Pinned, "the certificate does not match the pin")
		require.Equal(t, before[0], loginsOn(m)[0], "no LOGIN reached the IMAP server")
	})

	t.Run("connect: the SMTP server shows another certificate than the pin", func(t *testing.T) {
		before := loginsOn(m)
		certErr := tryConnect(t, func(s *account.MailServer) { s.PinnedSMTPCert = decoy.smtp.CertSHA256 })
		require.NotNil(t, certErr.SMTP)
		require.True(t, certErr.SMTP.Pinned, "the certificate does not match the pin")
		require.Equal(t, before[1], loginsOn(m)[1], "no AUTH reached the SMTP server")
	})

	t.Run("connect: no pin, and no trusted authority signed the certificate", func(t *testing.T) {
		before := loginsOn(m)
		certErr := tryConnect(t, func(s *account.MailServer) { s.PinnedIMAPCert, s.PinnedSMTPCert = "", "" })
		for name, c := range map[string]*imapsmtp.CertificateUnknownError{"imap": certErr.IMAP, "smtp": certErr.SMTP} {
			require.NotNil(t, c, name)
			require.False(t, c.Pinned, name)
			var unknown x509.UnknownAuthorityError
			require.ErrorAs(t, c.Reason, &unknown, name)
		}
		require.Equal(t, before, loginsOn(m), "no login reached the servers")
	})

	t.Run("connect: a certificate for another host name", func(t *testing.T) {
		// The certificates are for 127.0.0.1. The name localhost reaches the
		// same servers on this computer, but the certificates are not for it.
		before := loginsOn(m)
		certErr := tryConnect(t, func(s *account.MailServer) {
			s.IMAPHost, s.SMTPHost, s.PinnedIMAPCert, s.PinnedSMTPCert = "localhost", "localhost", "", ""
		})
		for name, c := range map[string]*imapsmtp.CertificateUnknownError{"imap": certErr.IMAP, "smtp": certErr.SMTP} {
			require.NotNil(t, c, name)
			var hostname x509.HostnameError
			require.ErrorAs(t, c.Reason, &hostname, name)
		}
		require.Equal(t, before, loginsOn(m), "no login reached the servers")
	})

	t.Run("on the web page: no trust for another host name", func(t *testing.T) {
		b, _ := h.signIn()
		before := loginsOn(m)
		form := connectForm("tls-web@example.net", m, secretPassword)
		form.Set("imap_host", "localhost")
		form.Set("smtp_host", "localhost")
		status, page := b.post("/connect/save", form)
		require.Equal(t, http.StatusOK, status, "the page shows the certificates")
		require.Contains(t, page, template.HTMLEscapeString(connect.TextCertWrongHost))
		require.Empty(t, offerPattern.FindAllStringSubmatch(page, -1), "no offer to trust a certificate for another host name")

		// The page offers to trust the certificates for 127.0.0.1 ...
		_, page = b.post("/connect/save", connectForm("tls-web@example.net", m, secretPassword))
		offers := offerPattern.FindAllStringSubmatch(page, -1)
		require.Len(t, offers, 2)
		// ... and those offers cannot pin the certificates for localhost.
		for _, o := range offers {
			form.Set("trust_"+o[1], "1")
			form.Set(o[1]+"_offer", o[2])
		}
		status, page = b.post("/connect/save", form)
		require.Equal(t, http.StatusUnprocessableEntity, status)
		require.Contains(t, page, "The choice to trust a certificate is not valid")
		require.Equal(t, before, loginsOn(m), "no login reached the servers")
	})

	t.Run("no account was connected", func(t *testing.T) {
		views, err := h.app.Service.ListAccounts(localCtx())
		require.NoError(t, err)
		require.Empty(t, views)
	})

	t.Run("a stored account whose certificate check fails", func(t *testing.T) {
		a := h.connect(localCtx(), accountAddress, m)
		u := settingsOf(account.MailSend)
		u.ConfirmSends = false
		u.Limits.UndoWindowAuto = 0
		cs := h.client(nil)
		for name, pins := range map[string][2]string{
			"another certificate than the pin": {decoy.imap.CertSHA256, decoy.smtp.CertSHA256},
			"no pin, no trusted authority":     {"", ""},
		} {
			t.Run(name, func(t *testing.T) {
				// Someone with access to the database changes the pins. Saving the
				// settings then drops the backend that the program keeps.
				_, err := h.sqlDB().Exec(`UPDATE accounts SET mail = json_set(mail, '$.PinnedIMAPCert', ?, '$.PinnedSMTPCert', ?)
					WHERE id = ?`, pins[0], pins[1], string(a.ID))
				require.NoError(t, err)
				h.update(a.ID, u)
				before, messages := loginsOn(m), len(m.smtp.Messages())

				res := call(t, cs, "list_folders", map[string]any{"account": string(a.ID)})
				require.True(t, res.IsError, "the IMAP connection is refused")
				res = call(t, cs, "send_message", sendArgs(a.ID))
				if !res.IsError {
					require.NotEqual(t, "sent", structured(t, res)["status"], "the SMTP connection is refused")
				}
				require.Equal(t, before, loginsOn(m), "no login reached the servers")
				require.Equal(t, messages, len(m.smtp.Messages()), "nothing was sent")
			})
		}
	})
}

// ---------------------------------------------------------------- the local web interface

// agentLinks makes the program give the agent each kind of link to the web
// interface, through the tools: the approval link of a send and of a move to
// Trash, the settings link of request_permission_change and of a permission
// error, and the connect link. It returns them by name, with the id of the
// pending send.
func agentLinks(t *testing.T, h *harness, a *account.Account) map[string]string {
	t.Helper()
	acct := string(a.ID)
	fallback := h.client(nil)
	links := map[string]string{}

	h.update(a.ID, settingsOf(account.MailDelete))
	send := mustOK(t, fallback, "send_message", sendArgs(a.ID))
	require.Equal(t, "pending_approval", send["status"])
	links["send approval"] = send["approval_link"].(string)
	refs := refsIn(t, fallback, a.ID, "@inbox")
	trash := mustOK(t, fallback, "trash_messages", map[string]any{"account": acct, "refs": refs})
	require.Equal(t, "pending_approval", trash["status"])
	links["trash approval"] = trash["approval_link"].(string)

	h.update(a.ID, settingsOf(account.MailRead))
	res := call(t, fallback, "set_flags", map[string]any{"account": acct, "refs": refs, "seen": true})
	require.True(t, res.IsError)
	links["settings link of a permission error"] = webLinkPattern.FindString(textOf(t, res))
	require.Contains(t, links["settings link of a permission error"], "?area=mail&level=2")

	h.update(a.ID, settingsOf(account.MailSend))
	links["settings link"] = mustOK(t, fallback, "request_permission_change",
		map[string]any{"account": acct, "area": "mail", "level": 5})["link"].(string)
	require.Contains(t, links["settings link"], "?area=mail&level=5")
	links["connect link"] = mustOK(t, fallback, "connect_account", nil)["link"].(string)
	for name, link := range links {
		require.NotContains(t, link, "token", name)
	}
	return links
}

// uiFixture is a program with an account at Mail 4 that has a send and a move
// to Trash waiting for the approval page, and the links that the agent got.
type uiFixture struct {
	h     *harness
	m     mailServers
	a     *account.Account
	links map[string]string
}

func newUIFixture(t *testing.T) *uiFixture {
	t.Helper()
	h := newHarness(t)
	m := newMailServers(t)
	m.imap.Append("INBOX", incomingMail())
	a := h.connect(localCtx(), accountAddress, m)
	return &uiFixture{h: h, m: m, a: a, links: agentLinks(t, h, a)}
}

// TestLocalUILinksRevealNothingWithoutSession: each link that the agent can
// get, opened without the browser session of the user, shows only the
// sentence that says to run "email-mcp setup", and changes nothing; a form
// post without the session, or without its CSRF token, changes nothing either
// (spec 8.2, 10.1, 15.4).
func TestLocalUILinksRevealNothingWithoutSession(t *testing.T) {
	f := newUIFixture(t)
	h, acct := f.h, string(f.a.ID)
	signedIn, _ := h.signIn()
	stranger := h.stranger()
	status, noSession := stranger.get("/")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Contains(t, noSession, "Run <code>email-mcp setup</code> in a terminal, then open this link again.")

	db := h.sqlDB()
	before := dump(t, db)
	writes := f.m.imap.WriteCommands()
	sendID := pathOf(t, f.links["send approval"])[len("/approvals/"):]
	trashID := pathOf(t, f.links["trash approval"])[len("/approvals/"):]

	cookies := map[string]string{
		"no cookie":       "",
		"unknown session": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"malformed":       "x",
	}
	for linkName, link := range f.links {
		for cookieName, cookie := range cookies {
			t.Run(linkName+", "+cookieName, func(t *testing.T) {
				b := h.stranger()
				b.cookie = cookie
				status, body := b.get(link)
				requireNoSessionPage(t, noSession, status, body, "GET "+linkName)
				resp, body := b.do(b.request(http.MethodHead, link, nil))
				require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
				require.Empty(t, body)
			})
		}
	}

	// Form posts without a session: every action of the pages.
	posts := map[string]url.Values{
		"/approvals/" + sendID + "/approve":  nil,
		"/approvals/" + sendID + "/reject":   nil,
		"/approvals/" + trashID + "/approve": nil,
		"/approvals/" + trashID + "/reject":  nil,
		"/accounts/" + acct + "/settings": func() url.Values {
			form := settingsForm(account.MailDelete, false, false, 0)
			form.Set("accept_autonomy_risk", "1")
			return form
		}(),
		"/accounts/" + acct + "/remove": {"confirm_remove": {"1"}},
		"/connections":                  {"name": {"agent"}, "scope": {"email:send"}},
		"/connect/save":                 connectForm("agent@example.net", f.m, secretPassword),
	}
	for path, form := range posts {
		t.Run("POST "+path+" without a session", func(t *testing.T) {
			for cookieName, cookie := range cookies {
				b := h.stranger()
				b.cookie = cookie
				withToken := cloneForm(form)
				withToken.Set("csrf_token", signedIn.csrf)
				status, body := b.post(path, withToken)
				requireNoSessionPage(t, noSession, status, body, cookieName)
			}
		})
		t.Run("POST "+path+" with a session but no valid CSRF token", func(t *testing.T) {
			for _, token := range []string{"", "x", signedIn.csrf + "x"} {
				b := *signedIn
				b.csrf = ""
				withToken := cloneForm(form)
				withToken.Set("csrf_token", token)
				status, body := b.post(path, withToken)
				require.Equal(t, http.StatusForbidden, status, "token %q", token)
				require.Equal(t, "Forbidden\n", body)
			}
		})
	}

	require.Equal(t, before, dump(t, db), "nothing changed in the database")
	require.Equal(t, writes, f.m.imap.WriteCommands(), "nothing changed on the mail server")
	require.Empty(t, f.m.smtp.Messages(), "nothing was sent")

	// The same links work in the browser of the user, and show what the
	// stranger did not see.
	status, page := signedIn.get(f.links["send approval"])
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, page, outSubject)
	require.Contains(t, page, rcptAddress)
	require.NotContains(t, noSession, outSubject)
	status, page = signedIn.get(f.links["settings link"])
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, page, "Delete")
	require.NotContains(t, noSession, "Delete")
	require.NotContains(t, noSession, acct)
}

// TestLocalUIRefusesWrongHost: a request whose Host header is not
// "127.0.0.1:<port>" or "localhost:<port>" is refused with 403 before anything
// else, even with the session cookie and the CSRF token: a page of an attacker
// that uses DNS rebinding gets nothing and changes nothing. The same holds for
// the local HTTP MCP endpoint with a valid token (spec 8.2, 10.2, 15.4).
func TestLocalUIRefusesWrongHost(t *testing.T) {
	f := newUIFixture(t)
	h := f.h
	b, _ := h.signIn()
	setupURL, err := h.app.Web.NewSetupURL(context.Background())
	require.NoError(t, err)
	port := strings.TrimPrefix(b.base, "http://127.0.0.1:")
	approval := pathOf(t, f.links["send approval"])
	db := h.sqlDB()
	before := dump(t, db)

	bad := []string{
		"evil.example",
		"evil.example:" + port,
		"127.0.0.1",
		"localhost",
		"127.0.0.2:" + port,
		"[::1]:" + port,
		"LOCALHOST:" + port,
		"localhost.:" + port,
		"127.0.0.1.:" + port,
		"0.0.0.0:" + port,
		"127.0.0.1:" + port + "0",
	}
	for _, host := range bad {
		t.Run(host, func(t *testing.T) {
			for _, req := range []*http.Request{
				b.request(http.MethodGet, approval, nil),
				b.request(http.MethodGet, pathOf(t, f.links["settings link"]), nil),
				b.request(http.MethodPost, approval+"/approve", url.Values{"csrf_token": {b.csrf}}),
				b.request(http.MethodPost, "/accounts/"+string(f.a.ID)+"/settings", func() url.Values {
					form := settingsForm(account.MailDelete, true, true, 0)
					form.Set("csrf_token", b.csrf)
					return form
				}()),
			} {
				req.Host = host
				req.Header.Set("Origin", b.base)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				resp, body := b.do(req)
				require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s", req.Method, req.URL.Path)
				require.Equal(t, "Forbidden\n", body)
			}

			// The setup link does not work for a page of an attacker either.
			req := h.stranger().request(http.MethodGet, setupURL, nil)
			req.Host = host
			resp, _ := b.do(req)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			require.Empty(t, resp.Header.Values("Set-Cookie"))
		})
	}
	require.Equal(t, before, dump(t, db), "nothing changed in the database")
	require.Empty(t, f.m.smtp.Messages(), "nothing was sent")

	// The refused requests did not use the setup link.
	resp, _ := b.do(h.stranger().request(http.MethodGet, setupURL, nil))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Values("Set-Cookie"))

	t.Run("the HTTP MCP endpoint", func(t *testing.T) {
		ts := h.httpEndpoint()
		token, err := h.app.Service.CreateToken(localCtx(), "e2e", policy.Scopes{"email:read", "email:send"})
		require.NoError(t, err)
		u, err := url.Parse(ts.URL)
		require.NoError(t, err)
		body := toolCallBody(t, "request_permission_change", map[string]any{"account": string(f.a.ID), "area": "mail", "level": 5})
		sendBody := toolCallBody(t, "send_message", map[string]any{
			"account": string(f.a.ID), "to": []any{rcptAddress}, "subject": outSubject, "body": outBody,
		})
		pending := count(t, db, `SELECT COUNT(*) FROM pending_actions`)
		for _, host := range []string{"evil.example:" + u.Port(), "localhost.:" + u.Port(), "127.0.0.2:" + u.Port()} {
			for _, payload := range []string{body, sendBody} {
				status, answer := postMCP(t, ts, payload, func(r *http.Request) {
					r.Host = host
					r.Header.Set("Authorization", "Bearer "+token)
				})
				require.Equal(t, http.StatusForbidden, status, host)
				require.Equal(t, "Forbidden\n", answer)
			}
		}
		require.Equal(t, pending, count(t, db, `SELECT COUNT(*) FROM pending_actions`), "no tool ran")

		// With the right Host, the same request runs.
		status, _ := postMCP(t, ts, sendBody, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, pending+1, count(t, db, `SELECT COUNT(*) FROM pending_actions`), "the send waits for approval")
	})
}

// TestLocalUIRefusesCrossOriginPost: a form post that a browser marks as coming
// from another origin is refused with 403, even with the session cookie and a
// valid CSRF token, and changes nothing. A browser origin that is not on the
// allow-list cannot call the HTTP MCP endpoint either, even with a valid token
// (spec 8.3, 10.2, 15.4).
func TestLocalUIRefusesCrossOriginPost(t *testing.T) {
	f := newUIFixture(t)
	h := f.h
	b, _ := h.signIn()
	approval := pathOf(t, f.links["send approval"])
	settings := "/accounts/" + string(f.a.ID) + "/settings"
	db := h.sqlDB()
	before := dump(t, db)

	refused := map[string]map[string]string{
		"cross-site":                  {"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"},
		"same-site, another port":     {"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:1"},
		"old browser, evil":           {"Origin": "http://evil.example"},
		"old browser, another port":   {"Origin": "http://127.0.0.1:1"},
		"old browser, null":           {"Origin": "null"},
		"old browser, localhost name": {"Origin": strings.Replace(b.base, "127.0.0.1", "localhost", 1)},
	}
	for name, headers := range refused {
		t.Run(name, func(t *testing.T) {
			forms := map[string]url.Values{
				approval + "/approve": {"csrf_token": {b.csrf}},
				approval + "/reject":  {"csrf_token": {b.csrf}},
				settings: func() url.Values {
					form := settingsForm(account.MailDelete, false, false, 0)
					form.Set("accept_autonomy_risk", "1")
					form.Set("csrf_token", b.csrf)
					return form
				}(),
				"/accounts/" + string(f.a.ID) + "/remove": {"confirm_remove": {"1"}, "csrf_token": {b.csrf}},
				"/connections": {"name": {"agent"}, "scope": {"email:send"}, "csrf_token": {b.csrf}},
			}
			for path, form := range forms {
				req := b.request(http.MethodPost, path, form)
				for k, v := range headers {
					req.Header.Set(k, v)
				}
				resp, body := b.do(req)
				require.Equal(t, http.StatusForbidden, resp.StatusCode, path)
				require.Equal(t, "Forbidden\n", body, path)
			}
		})
	}
	require.Equal(t, before, dump(t, db), "nothing changed in the database")
	require.Empty(t, f.m.smtp.Messages(), "nothing was sent")

	t.Run("the HTTP MCP endpoint", func(t *testing.T) {
		ts := h.httpEndpoint()
		token, err := h.app.Service.CreateToken(localCtx(), "e2e", policy.Scopes{"email:read", "email:send"})
		require.NoError(t, err)
		sendBody := toolCallBody(t, "send_message", map[string]any{
			"account": string(f.a.ID), "to": []any{rcptAddress}, "subject": outSubject, "body": outBody,
		})
		pending := count(t, db, `SELECT COUNT(*) FROM pending_actions`)
		for _, origin := range []string{"http://evil.example", "null", b.base, ts.URL} {
			status, answer := postMCP(t, ts, sendBody, func(r *http.Request) {
				r.Header.Set("Origin", origin)
				r.Header.Set("Authorization", "Bearer "+token)
			})
			require.Equal(t, http.StatusForbidden, status, origin)
			require.Equal(t, "Forbidden\n", answer)
		}
		require.Equal(t, pending, count(t, db, `SELECT COUNT(*) FROM pending_actions`), "no tool ran")
	})

	// The same post from the page itself works: the user approves.
	status, _ := b.post(approval+"/approve", nil)
	require.Equal(t, http.StatusSeeOther, status)
	require.Len(t, f.m.smtp.Messages(), 1)
}
