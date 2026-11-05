package e2e

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// Every tool call of an agent must be in the activity log, whatever it did and
// however it ended: the whole program, with a real MCP client, real mail servers
// and the real database. The user reads the log on the activity page, so the test
// ends there.
func TestEveryToolCallIsInTheActivityLog(t *testing.T) {
	h := newHarness(t)
	m := newMailServers(t)
	for range 3 {
		m.imap.Append("INBOX", incomingMail())
	}
	a := h.connect(localCtx(), accountAddress, m)
	h.update(a.ID, settingsOf(account.MailDelete))
	acct := string(a.ID)
	opts, _ := acceptingClient()
	cs := h.client(opts)
	db := h.sqlDB()
	calls := func() int { return count(t, db, `SELECT COUNT(*) FROM audit_log WHERE kind = 'call'`) }

	// requests returns the MCP requests of one tool call. This client answers a
	// confirmation in band, and that takes a second request.
	requests := func(tool string, args map[string]any) int {
		if tool == "send_message" || tool == "trash_messages" {
			return 2
		}
		return 1
	}
	// ask makes a tool call and checks that each of its requests wrote one call
	// entry for wantTool.
	ask := func(wantTool, tool string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		before := calls()
		res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		require.Equal(t, before+requests(tool, args), calls(), "tool %s", tool)
		require.Equal(t, 1, count(t, db, `SELECT COUNT(*) FROM audit_log WHERE id = (SELECT MAX(id) FROM audit_log
			WHERE kind = 'call') AND tool = ?`, wantTool), "tool %s", tool)
		return res
	}
	valid := map[string]bool{}
	run := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		valid[tool] = true
		res := ask(tool, tool, args)
		require.False(t, res.IsError, "%s: %s", tool, textOf(t, res))
		if res.StructuredContent == nil {
			return nil
		}
		return structured(t, res)
	}

	run("list_accounts", map[string]any{})
	run("list_folders", map[string]any{"account": acct})
	run("list_tags", map[string]any{"account": acct})
	found := run("search_messages", map[string]any{"account": acct, "folder": "@inbox"})
	msgs, _ := found["messages"].([]any)
	require.Len(t, msgs, 3)
	ref := func(i int) string { return msgs[i].(map[string]any)["ref"].(string) }
	run("list_new_messages", map[string]any{"account": acct})
	message := run("get_message", map[string]any{"account": acct, "ref": ref(0)})
	attachments, _ := message["attachments"].([]any)
	require.NotEmpty(t, attachments)
	run("get_attachment", map[string]any{"account": acct, "ref": ref(0), "attachment_id": attachments[0].(map[string]any)["id"]})
	run("set_flags", map[string]any{"account": acct, "refs": []any{ref(0)}, "seen": true})
	run("create_folder", map[string]any{"account": acct, "name": "Projects"})
	run("create_tag", map[string]any{"account": acct, "name": "work"})
	run("tag_messages", map[string]any{"account": acct, "refs": []any{ref(0)}, "add": []any{"work"}})
	run("move_messages", map[string]any{"account": acct, "refs": []any{ref(1)}, "folder": "@archive"})
	draft := run("create_draft", map[string]any{"account": acct, "to": []any{rcptAddress}, "subject": draftSubject, "body": draftBody})
	run("discard_draft", map[string]any{"account": acct, "ref": draft["ref"]})
	sent := run("send_message", sendArgs(a.ID))
	run("get_send_status", map[string]any{"account": acct, "send_id": sent["send_id"]})
	run("trash_messages", map[string]any{"account": acct, "refs": []any{ref(2)}})
	run("request_permission_change", map[string]any{"account": acct, "area": "mail", "level": 5})
	run("connect_account", map[string]any{})
	valid["cancel_send"] = true
	ask("cancel_send", "cancel_send", map[string]any{"account": acct, "send_id": sent["send_id"]}) // too late, and still logged

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		require.True(t, valid[tool.Name], "this test must call %s", tool.Name)
	}

	// The calls that end before the tool does anything.
	ask("set_flags", "set_flags", map[string]any{"account": acct, "refs": []any{}, "seen": true})
	ask("request_permission_change", "request_permission_change", map[string]any{"account": acct, "area": "mail", "level": 99})
	ask("set_flags", "set_flags", map[string]any{"account": acct, "refs": "not a list"})
	ask("unknown_tool", "no_such_tool", map[string]any{"text": "find this"})
	ask("search_messages", "search_messages", map[string]any{"account": "no-such-account"})

	// The calls that the permission level refuses.
	h.update(a.ID, settingsOf(account.MailOff))
	ask("search_messages", "search_messages", map[string]any{"account": acct})
	ask("get_message", "get_message", map[string]any{"account": acct, "ref": ref(0)})
	require.Equal(t, 2, count(t, db, `SELECT COUNT(*) FROM audit_log WHERE kind = 'call' AND account_id = ?
		AND outcome = 'refused' AND detail = 'level_too_low'`, acct))

	// A request with a wrong token has no tenant, so only the program log has it.
	before := calls()
	status, _ := postMCP(t, h.httpEndpoint(), toolCallBody(t, "list_accounts", map[string]any{}), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong-token")
	})
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, before, calls())
	require.Contains(t, h.logs.String(), "http request refused")

	// The user sees the request, and never the content of the message.
	b, _ := h.signIn()
	status, page := b.get("/activity?kind=call&tool=send_message")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, page, outSubject)
	require.Contains(t, page, rcptAddress)
	require.NotContains(t, page, outBody)
	require.NotContains(t, page, base64.StdEncoding.EncodeToString([]byte(outAttachBody)))
	require.NotContains(t, h.logs.String(), outSubject)
}
