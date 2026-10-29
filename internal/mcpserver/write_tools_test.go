package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	gomail "github.com/emersion/go-message/mail"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// endUntilUser is the last sentence of an error that only the user can fix.
const endUntilUser = "Do not retry until the user has done this."

// hints are the four annotation hints of a tool.
type hints struct {
	readOnly, destructive, idempotent, openWorld bool
}

// wantTools are the twenty tools and their hints. A hint that the plan does not
// name is false.
var wantTools = map[string]hints{
	"list_accounts":             {readOnly: true},
	"list_folders":              {readOnly: true},
	"list_tags":                 {readOnly: true},
	"search_messages":           {readOnly: true},
	"list_new_messages":         {readOnly: true},
	"get_message":               {readOnly: true},
	"get_attachment":            {readOnly: true},
	"get_send_status":           {readOnly: true},
	"set_flags":                 {idempotent: true},
	"create_folder":             {idempotent: true},
	"create_tag":                {idempotent: true},
	"tag_messages":              {idempotent: true},
	"move_messages":             {idempotent: true},
	"create_draft":              {},
	"discard_draft":             {},
	"send_message":              {openWorld: true},
	"cancel_send":               {},
	"trash_messages":            {destructive: true},
	"request_permission_change": {},
	"connect_account":           {},
}

func TestToolsListFullSet(t *testing.T) {
	e := newEnv(t, envOptions{backends: &countingFactory{}})

	first, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	second, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(wantTools))
	for name := range wantTools {
		names = append(names, name)
	}
	require.Len(t, first.Tools, 20)
	require.ElementsMatch(t, names, toolNames(first.Tools), "the exact set of tools")
	require.Equal(t, toolNames(first.Tools), toolNames(second.Tools), "the order must be stable")
	for _, tool := range first.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			want := wantTools[tool.Name]
			a := tool.Annotations
			require.NotNil(t, a)
			require.Equal(t, want.readOnly, a.ReadOnlyHint, "readOnlyHint")
			require.NotNil(t, a.DestructiveHint, "MCP reads a missing destructiveHint as true, so it must be set")
			require.Equal(t, want.destructive, *a.DestructiveHint, "destructiveHint")
			require.Equal(t, want.idempotent, a.IdempotentHint, "idempotentHint")
			require.NotNil(t, a.OpenWorldHint, "MCP reads a missing openWorldHint as true, so it must be set")
			require.Equal(t, want.openWorld, *a.OpenWorldHint, "openWorldHint")
			require.NotNil(t, tool.InputSchema)
			require.NotNil(t, tool.OutputSchema, "every tool has an output schema")
			require.NotEmpty(t, tool.Description)
			requireNoPathArgument(t, tool.Name, tool.InputSchema)
		})
	}
}

// requireNoPathArgument fails when an input schema has a property, at any depth,
// whose name says that it holds a file path. Attachments come as base64 only.
func requireNoPathArgument(t *testing.T, tool string, schema any) {
	t.Helper()
	m, ok := schema.(map[string]any)
	if !ok {
		return
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for name, prop := range props {
			require.NotContains(t, strings.ToLower(name), "path", "%s must not take a file path (%s)", tool, name)
			requireNoPathArgument(t, tool, prop)
		}
	}
	if items, ok := m["items"]; ok {
		requireNoPathArgument(t, tool, items)
	}
}

// ---------------------------------------------------------------- helpers

// asker is an ElicitationHandler that records what the server asks and gives a
// fixed answer.
type asker struct {
	mu     sync.Mutex
	asked  []*mcp.ElicitParams
	answer mcp.ElicitResult
}

func (a *asker) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, req.Params)
	answer := a.answer
	if a.answer.Content != nil {
		answer.Content = map[string]any{}
		for k, v := range a.answer.Content {
			answer.Content[k] = v
		}
	}
	return &answer, nil
}

func (a *asker) requests() []*mcp.ElicitParams {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

// options returns client options that answer elicitations with a.
func (a *asker) options() *mcp.ClientOptions {
	return &mcp.ClientOptions{ElicitationHandler: a.handle}
}

// accepting returns an asker that accepts and confirms.
func accepting() *asker {
	return &asker{answer: mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
}

// mailSetup is an env with an account on the test IMAP and SMTP servers.
type mailSetup struct {
	e    *env
	a    *account.Account
	imap *imaptest.Server
	smtp *smtptest.Server
}

func newMailSetup(t *testing.T, opts envOptions, change ...func(*account.Account)) *mailSetup {
	t.Helper()
	imapSrv := imaptest.New(t)
	smtpSrv := smtptest.New(t)
	e := newEnv(t, opts)
	return &mailSetup{e: e, a: e.mailAccount(imapSrv, smtpSrv, change...), imap: imapSrv, smtp: smtpSrv}
}

// sendArgs are the arguments of a simple send.
func sendArgs(a *account.Account) map[string]any {
	return map[string]any{
		"account": string(a.ID),
		"to":      []any{"you@example.com"},
		"subject": "Lunch",
		"body":    "Shall we meet at noon?",
	}
}

// rawClient connects a client that declares form elicitation but does not
// answer input requests by itself, so a test can send its own answer.
func rawClient(t *testing.T, e *env) *mcp.ClientSession {
	t.Helper()
	return e.connect(&mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			t.Error("the raw client must not be asked directly")
			return &mcp.ElicitResult{Action: "decline"}, nil
		},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	}, nil)
}

// firstCall makes the first call of a tool on a raw client and checks that the
// result asks for the confirmation. It returns the request state.
func firstCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res := callOn(t, cs, &mcp.CallToolParams{Name: tool, Arguments: args})
	require.False(t, res.IsError)
	require.True(t, res.NeedsInput(), "the first call must ask for input")
	require.Empty(t, res.Content, "a result that asks for input has no content")
	require.Nil(t, res.StructuredContent, "a result that asks for input has no structured content")
	require.Len(t, res.InputRequests, 1)
	_, ok := res.InputRequests["confirm"].(*mcp.ElicitParams)
	require.True(t, ok, "the input request is an elicitation, got %T", res.InputRequests["confirm"])
	require.NotEmpty(t, res.RequestState)
	return res.RequestState
}

// answerCall is the second call with an answer and a state.
func answerCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, answers mcp.InputResponseMap, state string) *mcp.CallToolResult {
	t.Helper()
	return callOn(t, cs, &mcp.CallToolParams{Name: tool, Arguments: args, InputResponses: answers, RequestState: state})
}

// rootsAnswer is an answer that is not an elicitation result: on the wire it is
// the answer to a roots request, {"roots":[]}. The SDK type for it,
// mcp.ListRootsResult, is deprecated (the roots feature is deprecated as of
// protocol 2026-07-28), so the test writes the JSON itself. The server still
// decodes it as a roots answer, and the tool must take it as a no.
//
// mcp.InputResponse has an unexported method, so the type embeds an
// *mcp.ElicitResult to get it. Embedding also promotes the exported fields and
// methods of ElicitResult, so newRootsAnswer sets a non-nil, empty value: a
// call of a promoted method cannot panic. The MarshalJSON method of rootsAnswer
// is at depth 0, so it decides the bytes on the wire, not ElicitResult.
// TestRootsAnswerWireForm checks this.
type rootsAnswer struct{ *mcp.ElicitResult }

func newRootsAnswer() rootsAnswer { return rootsAnswer{ElicitResult: &mcp.ElicitResult{}} }

func (rootsAnswer) MarshalJSON() ([]byte, error) { return []byte(`{"roots":[]}`), nil }

// The subtest "not an elicitation result" of TestConfirmationGate tests
// something only when rootsAnswer is sent exactly as {"roots":[]} and the SDK
// decodes it as an answer that is not an elicitation result.
func TestRootsAnswerWireForm(t *testing.T) {
	wire, err := json.Marshal(mcp.InputResponseMap{"confirm": newRootsAnswer()})
	require.NoError(t, err)
	require.Equal(t, `{"confirm":{"roots":[]}}`, string(wire))

	var back mcp.InputResponseMap
	require.NoError(t, json.Unmarshal(wire, &back))
	require.NotNil(t, back["confirm"])
	_, isElicit := back["confirm"].(*mcp.ElicitResult)
	require.False(t, isElicit, "the SDK decoded the roots answer as an elicitation result")
}

// accept is the answer of a user who confirmed.
func accept() mcp.InputResponseMap {
	return mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
}

// callIn calls a tool on the client of e. A protocol error fails t, which may be
// a subtest.
func callIn(t *testing.T, e *env, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	return callOn(t, e.cs, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// searchRefs returns the refs of the messages in a folder.
func searchRefs(t *testing.T, e *env, a *account.Account, folder string) []any {
	t.Helper()
	res := callIn(t, e, "search_messages", map[string]any{"account": string(a.ID), "folder": folder})
	require.False(t, res.IsError, textOf(t, res))
	msgs := structured(t, res)["messages"].([]any)
	refs := make([]any, len(msgs))
	for i, m := range msgs {
		refs[i] = m.(map[string]any)["ref"]
	}
	return refs
}

// sendStatus returns the status of a send through get_send_status.
func sendStatus(t *testing.T, e *env, a *account.Account, sendID any) string {
	t.Helper()
	res := callIn(t, e, "get_send_status", map[string]any{"account": string(a.ID), "send_id": sendID})
	require.False(t, res.IsError, textOf(t, res))
	return structured(t, res)["status"].(string)
}

// requireNoToken fails when a result carries a token in a link.
func requireNoToken(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	require.NotContains(t, strings.ToLower(wireOf(t, res)), "token=")
}

// ---------------------------------------------------------------- send

func TestSendWithElicitationAccept(t *testing.T) {
	ask := accepting()
	m := newMailSetup(t, envOptions{client: ask.options()})

	res := callIn(t, m.e, "send_message", sendArgs(m.a))

	require.False(t, res.IsError, textOf(t, res))
	out := structured(t, res)
	require.Equal(t, "sent", out["status"], "a confirmed send has no undo window")
	require.NotEmpty(t, out["send_id"])
	require.Equal(t, "sent", sendStatus(t, m.e, m.a, out["send_id"]))
	requireNoToken(t, res)

	msgs := m.smtp.Messages()
	require.Len(t, msgs, 1, "the SMTP server got the message")
	require.Equal(t, []string{"you@example.com"}, msgs[0].Rcpt)
	require.Equal(t, "me@example.com", msgs[0].From)
	require.Contains(t, string(msgs[0].Data), "Subject: Lunch")

	asked := ask.requests()
	require.Len(t, asked, 1, "the user is asked once")
	require.Equal(t, "form", asked[0].Mode)
	require.Equal(t, "Send this email from me@example.com?\nTo: you@example.com\nSubject: Lunch\nAttachments: none\n\nBody:\nShall we meet at noon?", asked[0].Message)
	schema, err := json.Marshal(asked[0].RequestedSchema)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object","properties":{"confirm":{"type":"boolean","title":"Confirm","description":"Tick to go ahead. Leaving it empty does nothing.","default":false}}}`, string(schema),
		"no field is required, so a form sent empty takes the default: no")

	for _, secret := range []string{"you@example.com", "me@example.com", "Lunch", "noon"} {
		require.NotContains(t, m.e.logs.String(), secret, "the log holds no address, subject or body")
	}
}

func TestSendWithElicitationDecline(t *testing.T) {
	m := newMailSetup(t, envOptions{})
	answers := map[string]mcp.ElicitResult{
		"decline":                {Action: "decline"},
		"cancel":                 {Action: "cancel"},
		"accept without confirm": {Action: "accept", Content: map[string]any{"confirm": false}},
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			ask := &asker{answer: answer}
			cs := m.e.connect(ask.options(), nil)

			res := callOn(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: sendArgs(m.a)})

			require.False(t, res.IsError, textOf(t, res))
			out := structured(t, res)
			require.Equal(t, "declined", out["status"])
			require.NotEmpty(t, out["send_id"])
			require.Equal(t, "declined", sendStatus(t, m.e, m.a, out["send_id"]))
			require.Len(t, ask.requests(), 1)
		})
	}
	require.Empty(t, m.smtp.Messages(), "nothing is sent when the user does not confirm")
}

func TestSendWithoutElicitationCapability(t *testing.T) {
	m := newMailSetup(t, envOptions{})
	clients := map[string]func(ask *asker) *mcp.ClientOptions{
		"no capabilities": func(*asker) *mcp.ClientOptions {
			return &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}}
		},
		"only url elicitation": func(ask *asker) *mcp.ClientOptions {
			return &mcp.ClientOptions{
				ElicitationHandler: ask.handle,
				Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{
					URL: &mcp.URLElicitationCapabilities{},
				}},
			}
		},
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			ask := accepting()
			cs := m.e.connect(client(ask), nil)

			res := callOn(t, cs, &mcp.CallToolParams{Name: "send_message", Arguments: sendArgs(m.a)})

			require.False(t, res.IsError, textOf(t, res))
			out := structured(t, res)
			require.Equal(t, "pending_approval", out["status"])
			sendID, _ := out["send_id"].(string)
			require.NotEmpty(t, sendID)
			require.Equal(t, "https://mcp.test/approve/"+sendID, out["approval_link"])
			requireNoToken(t, res)
			require.Equal(t, "pending_approval", sendStatus(t, m.e, m.a, sendID))
			require.Empty(t, ask.requests(), "a client that cannot show a form is not asked")
		})
	}
	require.Empty(t, m.smtp.Messages(), "nothing is sent before the user approves")
}

// An older protocol version has no input requests. On a transport with a session
// (stdio), the SDK asks the client with elicitation/create. Over stateless HTTP
// it cannot, so such a client gets the fallback.
func TestSendFromAnOlderClient(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("in band for older clients %v", legacy), func(t *testing.T) {
			ask := accepting()
			m := newMailSetup(t, envOptions{
				client:            ask.options(),
				session:           &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"},
				legacyElicitation: legacy,
			})

			res := callIn(t, m.e, "send_message", sendArgs(m.a))

			require.False(t, res.IsError, textOf(t, res))
			if legacy {
				require.Equal(t, "sent", structured(t, res)["status"])
				require.Len(t, ask.requests(), 1)
				require.Len(t, m.smtp.Messages(), 1)
			} else {
				require.Equal(t, "pending_approval", structured(t, res)["status"])
				require.Empty(t, ask.requests())
				require.Empty(t, m.smtp.Messages())
			}
		})
	}
}

func TestTamperedRequestStateRejected(t *testing.T) {
	m := newMailSetup(t, envOptions{})
	raw := rawClient(t, m.e)
	args := sendArgs(m.a)
	state := firstCall(t, raw, "send_message", args)

	t.Run("a changed state", func(t *testing.T) {
		i := len(state) / 2
		swap := byte('A')
		if state[i] == 'A' {
			swap = 'B'
		}
		tampered := state[:i] + string(swap) + state[i+1:]

		res := answerCall(t, raw, "send_message", args, accept(), tampered)

		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "not valid")
		require.True(t, strings.HasSuffix(textOf(t, res), endNoRepeat), textOf(t, res))
		require.Nil(t, res.StructuredContent)
	})

	t.Run("a made-up state", func(t *testing.T) {
		res := answerCall(t, raw, "send_message", args, accept(), "eyJpZCI6IngifQ.c2lnbmF0dXJl")
		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "not valid")
	})

	t.Run("other arguments", func(t *testing.T) {
		other := sendArgs(m.a)
		other["to"] = []any{"attacker@evil.example"}

		res := answerCall(t, raw, "send_message", other, accept(), state)

		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "not the one")
	})

	require.Empty(t, m.smtp.Messages(), "a forged or changed answer sends nothing")

	t.Run("the genuine state still works", func(t *testing.T) {
		res := answerCall(t, raw, "send_message", args, accept(), state)

		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "sent", structured(t, res)["status"])
		require.Len(t, m.smtp.Messages(), 1)
	})

	t.Run("a state that was used", func(t *testing.T) {
		res := answerCall(t, raw, "send_message", args, accept(), state)

		require.True(t, res.IsError)
		require.Len(t, m.smtp.Messages(), 1, "a confirmation acts once")
	})
}

// Only an elicitation answer "accept" with confirm = true (a JSON boolean) is a
// yes. Every other answer is a no, and the action is declined.
func TestConfirmationGate(t *testing.T) {
	m := newMailSetup(t, envOptions{})
	raw := rawClient(t, m.e)
	args := sendArgs(m.a)

	noes := map[string]mcp.InputResponse{
		"confirm is the string true": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "true"}},
		"confirm is a number":        &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": 1}},
		"confirm is missing":         &mcp.ElicitResult{Action: "accept", Content: map[string]any{"ok": true}},
		"no content":                 &mcp.ElicitResult{Action: "accept"},
		"cancel":                     &mcp.ElicitResult{Action: "cancel", Content: map[string]any{"confirm": true}},
		"decline":                    &mcp.ElicitResult{Action: "decline", Content: map[string]any{"confirm": true}},
		"accept in capitals":         &mcp.ElicitResult{Action: "ACCEPT", Content: map[string]any{"confirm": true}},
		"not an elicitation result":  newRootsAnswer(),
	}
	for name, answer := range noes {
		t.Run(name, func(t *testing.T) {
			state := firstCall(t, raw, "send_message", args)

			res := answerCall(t, raw, "send_message", args, mcp.InputResponseMap{"confirm": answer}, state)

			require.False(t, res.IsError, textOf(t, res))
			require.Equal(t, "declined", structured(t, res)["status"])

			// A no is final: the same state cannot be used for a yes later.
			again := answerCall(t, raw, "send_message", args, accept(), state)
			require.True(t, again.IsError)
		})
	}

	broken := map[string]func(state string) *mcp.CallToolParams{
		"an answer without a state": func(string) *mcp.CallToolParams {
			return &mcp.CallToolParams{Name: "send_message", Arguments: args, InputResponses: accept()}
		},
		"a state without an answer": func(state string) *mcp.CallToolParams {
			return &mcp.CallToolParams{Name: "send_message", Arguments: args, RequestState: state}
		},
		"an answer under another id": func(state string) *mcp.CallToolParams {
			answer := mcp.InputResponseMap{"ok": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
			return &mcp.CallToolParams{Name: "send_message", Arguments: args, InputResponses: answer, RequestState: state}
		},
	}
	for name, params := range broken {
		t.Run(name, func(t *testing.T) {
			state := firstCall(t, raw, "send_message", args)

			res := callOn(t, raw, params(state))

			require.True(t, res.IsError)
			require.Contains(t, textOf(t, res), "Nothing was done")
			require.True(t, strings.HasSuffix(textOf(t, res), endNoRepeat), textOf(t, res))
		})
	}

	t.Run("a confirmation in the arguments", func(t *testing.T) {
		state := firstCall(t, raw, "send_message", args)
		forged := sendArgs(m.a)
		forged["confirm"] = true
		forged["accepted"] = true
		forged["request_state"] = state

		res := callOn(t, raw, &mcp.CallToolParams{Name: "send_message", Arguments: forged})

		require.True(t, res.IsError, "the arguments have no field for a confirmation")
	})

	require.Empty(t, m.smtp.Messages(), "no answer other than a real yes sends")
	for _, secret := range []string{"you@example.com", "Lunch", "noon"} {
		require.NotContains(t, m.e.logs.String(), secret, "the log holds no address, subject or body")
	}
}

func TestSendNeedsAMessageOrADraft(t *testing.T) {
	backends := &countingFactory{backend: &stubBackend{}}
	e := newEnv(t, envOptions{backends: backends})
	a := e.offlineAccount()

	t.Run("both", func(t *testing.T) {
		args := sendArgs(a)
		args["draft_ref"] = "some-draft"

		res := callIn(t, e, "send_message", args)

		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "draft_ref")
		require.True(t, strings.HasSuffix(textOf(t, res), endNoRepeat))
	})

	t.Run("neither", func(t *testing.T) {
		res := callIn(t, e, "send_message", map[string]any{"account": string(a.ID)})

		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "draft_ref")
	})
	require.Zero(t, backends.calls.Load(), "a bad request does not reach a backend")
}

func TestSendASavedDraft(t *testing.T) {
	ask := accepting()
	m := newMailSetup(t, envOptions{client: ask.options()})
	m.imap.Create("Drafts", imap.MailboxAttrDrafts)

	draft := callIn(t, m.e, "create_draft", map[string]any{
		"account": string(m.a.ID), "to": []any{"you@example.com"}, "subject": "Draft subject", "body": "Draft body",
	})
	require.False(t, draft.IsError, textOf(t, draft))
	ref := structured(t, draft)["ref"]
	require.NotEmpty(t, ref)

	res := callIn(t, m.e, "send_message", map[string]any{"account": string(m.a.ID), "draft_ref": ref})

	require.False(t, res.IsError, textOf(t, res))
	require.Equal(t, "sent", structured(t, res)["status"])
	require.Len(t, m.smtp.Messages(), 1)
	require.Contains(t, ask.requests()[0].Message, "Subject: Draft subject")
}

// ---------------------------------------------------------------- compose

func TestComposeAttachmentsBase64(t *testing.T) {
	m := newMailSetup(t, envOptions{}, func(a *account.Account) {
		a.ConfirmSends = false
		a.Limits.UndoWindowAuto = 0
	})
	m.imap.Create("Drafts", imap.MailboxAttrDrafts)
	data := make([]byte, 300)
	for i := range data {
		data[i] = byte(i)
	}

	t.Run("invalid base64 names the attachment", func(t *testing.T) {
		args := sendArgs(m.a)
		args["attachments"] = []any{
			map[string]any{"filename": "a.txt", "content_type": "text/plain", "data_base64": base64.StdEncoding.EncodeToString([]byte("ok"))},
			map[string]any{"filename": "b.bin", "data_base64": "not base64!"},
		}
		for _, tool := range []string{"send_message", "create_draft"} {
			res := callIn(t, m.e, tool, args)

			require.True(t, res.IsError, tool)
			text := textOf(t, res)
			require.Contains(t, text, "attachments[1]")
			require.NotContains(t, text, "not base64!", "the text is fixed, not an echo of the input")
			require.True(t, strings.HasSuffix(text, endNoRepeat), text)
		}
		require.Empty(t, m.smtp.Messages())
	})

	t.Run("valid base64 reaches the server as the same bytes", func(t *testing.T) {
		args := sendArgs(m.a)
		args["attachments"] = []any{
			map[string]any{"filename": "data.bin", "content_type": "application/octet-stream", "data_base64": base64.StdEncoding.EncodeToString(data)},
		}

		res := callIn(t, m.e, "send_message", args)

		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "sent", structured(t, res)["status"])
		msgs := m.smtp.Messages()
		require.Len(t, msgs, 1)
		require.Equal(t, data, attachmentOf(t, msgs[0].Data, "data.bin"))
	})

	t.Run("more than 10 MB together", func(t *testing.T) {
		before := len(m.smtp.Messages())
		// One byte over the limit. The SDK reads at most 16 MB in one request, so
		// the base64 of the request stays below that.
		args := sendArgs(m.a)
		args["attachments"] = []any{
			map[string]any{"filename": "a.bin", "data_base64": base64.StdEncoding.EncodeToString(make([]byte, service.MaxAttachmentBytes-1))},
			map[string]any{"filename": "b.bin", "data_base64": base64.StdEncoding.EncodeToString(make([]byte, 2))},
		}

		res := callOn(t, m.e.cs, &mcp.CallToolParams{Name: "send_message", Arguments: args})

		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "10 MB")
		require.True(t, strings.HasSuffix(textOf(t, res), endNoRepeat))
		require.Len(t, m.smtp.Messages(), before, "nothing more was sent")
	})
}

// attachmentOf returns the data of the attachment with the file name.
func attachmentOf(t *testing.T, raw []byte, filename string) []byte {
	t.Helper()
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	require.NoError(t, err)
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if h, ok := part.Header.(*gomail.AttachmentHeader); ok {
			if name, _ := h.Filename(); name == filename {
				data, err := io.ReadAll(part.Body)
				require.NoError(t, err)
				return data
			}
		}
	}
	t.Fatalf("the message has no attachment %q", filename)
	return nil
}

func TestComposeAttachmentLimit(t *testing.T) {
	b64 := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }

	exact := ComposeIn{To: []string{"you@example.com"}, Attachments: []AttachmentIn{
		{Filename: "a", DataBase64: b64(service.MaxAttachmentBytes - 1)},
		{Filename: "b", DataBase64: b64(1)},
	}}
	c, err := exact.toCompose()
	require.NoError(t, err, "exactly 10 MB is allowed")
	require.Len(t, c.Attachments, 2)
	require.Len(t, c.Attachments[0].Data, service.MaxAttachmentBytes-1)

	overs := map[string][]AttachmentIn{
		// "AAAA" may decode to as little as 1 byte by its length, so only the
		// decoded size shows that this is 2 bytes over.
		"over after decoding": {
			{Filename: "a", DataBase64: b64(service.MaxAttachmentBytes - 1)},
			{Filename: "b", DataBase64: b64(3)},
		},
		"surely over before decoding": {
			{Filename: "a", DataBase64: b64(service.MaxAttachmentBytes + 4)},
		},
	}
	for name, atts := range overs {
		t.Run(name, func(t *testing.T) {
			_, err := ComposeIn{To: []string{"you@example.com"}, Attachments: atts}.toCompose()
			require.Error(t, err)
			fail := toolError("send_message", err)
			require.Equal(t, codeTooLarge, fail.code)
			require.Contains(t, fail.text, "10 MB")
			require.Contains(t, fail.text, "base64 decoding", "the text of this layer, not the one of mime.Build")
		})
	}

	bad := ComposeIn{Attachments: []AttachmentIn{{Filename: "a", DataBase64: b64(3)}, {Filename: "b", DataBase64: "%%%"}}}
	_, err = bad.toCompose()
	require.Error(t, err)
	require.Contains(t, toolError("create_draft", err).text, "attachments[1].data_base64")
}

func TestComposeFields(t *testing.T) {
	in := ComposeIn{
		To: []string{"a@example.com"}, Cc: []string{"b@example.com"}, Bcc: []string{"c@example.com"},
		Subject: "S", Body: "B", HTMLBody: "<p>B</p>",
		ReplyTo: &ReplyIn{Ref: "r1", All: true}, IncludeOriginalAttachments: true,
		Attachments: []AttachmentIn{{Filename: "f.txt", ContentType: "text/plain", DataBase64: base64.StdEncoding.EncodeToString([]byte("hi"))}},
	}

	c, err := in.toCompose()

	require.NoError(t, err)
	require.Equal(t, mail.Compose{
		To: []string{"a@example.com"}, Cc: []string{"b@example.com"}, Bcc: []string{"c@example.com"},
		Subject: "S", Body: "B", HTMLBody: "<p>B</p>",
		ReplyTo: &mail.ReplyRef{Ref: "r1", All: true}, IncludeOriginalAttachments: true,
		Attachments: []mail.OutgoingAttachment{{Filename: "f.txt", ContentType: "text/plain", Data: []byte("hi")}},
	}, c)

	fwd, err := ComposeIn{To: []string{"a@example.com"}, ForwardOf: "r2"}.toCompose()
	require.NoError(t, err)
	require.Equal(t, mail.Ref("r2"), *fwd.ForwardOf)

	_, err = ComposeIn{To: []string{"a@example.com"}, ReplyTo: &ReplyIn{}}.toCompose()
	require.Error(t, err, "a reply needs the ref of the original")
}

// ---------------------------------------------------------------- trash

func TestTrashWithElicitation(t *testing.T) {
	imapSrv := imaptest.New(t)
	imapSrv.Create("Trash", imap.MailboxAttrTrash)
	imapSrv.Append("INBOX", plainMail("One", "first"))
	imapSrv.Append("INBOX", plainMail("Two", "second"))
	smtpSrv := smtptest.New(t)
	e := newEnv(t, envOptions{})
	a := e.mailAccount(imapSrv, smtpSrv, func(a *account.Account) { a.Levels[account.AreaMail] = account.MailDelete })
	refs := searchRefs(t, e, a, "@inbox")
	require.Len(t, refs, 2)
	args := map[string]any{"account": string(a.ID), "refs": refs}

	t.Run("decline", func(t *testing.T) {
		ask := &asker{answer: mcp.ElicitResult{Action: "decline"}}
		cs := e.connect(ask.options(), nil)

		res := callOn(t, cs, &mcp.CallToolParams{Name: "trash_messages", Arguments: args})

		require.False(t, res.IsError, textOf(t, res))
		out := structured(t, res)
		require.Equal(t, "declined", out["status"])
		require.EqualValues(t, 2, out["count"])
		asked := ask.requests()
		require.Len(t, asked, 1)
		require.Equal(t, "Move 2 messages in me@example.com to Trash?", asked[0].Message)
		require.Len(t, searchRefs(t, e, a, "@inbox"), 2, "nothing moved")
	})

	t.Run("no elicitation", func(t *testing.T) {
		res := callIn(t, e, "trash_messages", args)

		require.False(t, res.IsError, textOf(t, res))
		out := structured(t, res)
		require.Equal(t, "pending_approval", out["status"])
		require.True(t, strings.HasPrefix(out["approval_link"].(string), "https://mcp.test/approve/"))
		requireNoToken(t, res)
		require.Len(t, searchRefs(t, e, a, "@inbox"), 2, "nothing moved")
	})

	t.Run("accept", func(t *testing.T) {
		ask := accepting()
		cs := e.connect(ask.options(), nil)

		res := callOn(t, cs, &mcp.CallToolParams{Name: "trash_messages", Arguments: args})

		require.False(t, res.IsError, textOf(t, res))
		out := structured(t, res)
		require.Equal(t, "done", out["status"])
		require.EqualValues(t, 2, out["count"])
		require.Len(t, ask.requests(), 1)
		require.Empty(t, searchRefs(t, e, a, "@inbox"))
		require.Len(t, searchRefs(t, e, a, "@trash"), 2)
	})
}

func TestTrashTamperedRequestState(t *testing.T) {
	imapSrv := imaptest.New(t)
	imapSrv.Create("Trash", imap.MailboxAttrTrash)
	imapSrv.Append("INBOX", plainMail("One", "first"))
	imapSrv.Append("INBOX", plainMail("Two", "second"))
	e := newEnv(t, envOptions{})
	a := e.mailAccount(imapSrv, smtptest.New(t), func(a *account.Account) { a.Levels[account.AreaMail] = account.MailDelete })
	refs := searchRefs(t, e, a, "@inbox")
	raw := rawClient(t, e)
	state := firstCall(t, raw, "trash_messages", map[string]any{"account": string(a.ID), "refs": refs[:1]})

	// The user confirmed one message. The same state must not move both.
	res := answerCall(t, raw, "trash_messages", map[string]any{"account": string(a.ID), "refs": refs}, accept(), state)

	require.True(t, res.IsError)
	require.Contains(t, textOf(t, res), "not the one")
	require.Len(t, searchRefs(t, e, a, "@inbox"), 2, "nothing moved")
}

// ---------------------------------------------------------------- links

func TestRequestPermissionChangeChangesNothing(t *testing.T) {
	backends := &countingFactory{backend: &stubBackend{}}
	e := newEnv(t, envOptions{backends: backends})
	a := e.offlineAccount()
	before, err := e.store.GetAccount(context.Background(), e.tenant, a.ID)
	require.NoError(t, err)

	res := callIn(t, e, "request_permission_change", map[string]any{"account": string(a.ID), "area": "mail", "level": 5})

	require.False(t, res.IsError, textOf(t, res))
	link := "https://mcp.test/settings/" + string(a.ID) + "?area=mail&level=5"
	out := structured(t, res)
	require.Equal(t, link, out["link"])
	require.Equal(t, false, out["changed"])
	require.Contains(t, textOf(t, res), link)
	requireNoToken(t, res)

	after, err := e.store.GetAccount(context.Background(), e.tenant, a.ID)
	require.NoError(t, err)
	require.Equal(t, before.Levels, after.Levels, "the level is unchanged")
	require.Equal(t, account.MailSend, after.Level(account.AreaMail))
	require.Equal(t, before.ConfirmSends, after.ConfirmSends)
	require.Equal(t, before.ConfirmDeletes, after.ConfirmDeletes)
	require.Equal(t, before.Limits, after.Limits)
	require.Zero(t, backends.calls.Load(), "a link needs no mail server")

	t.Run("bad arguments", func(t *testing.T) {
		cases := map[string]struct {
			args map[string]any
			want string
		}{
			"an area that does not exist": {map[string]any{"account": string(a.ID), "area": "files", "level": 2}, `"area"`},
			"a level that does not exist": {map[string]any{"account": string(a.ID), "area": "mail", "level": 9}, `"level"`},
			"an unknown account":          {map[string]any{"account": "nope", "area": "mail", "level": 2}, "list_accounts"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				res := callIn(t, e, "request_permission_change", tc.args)
				require.True(t, res.IsError)
				require.Contains(t, textOf(t, res), tc.want)
			})
		}
	})
}

func TestConnectAccountReturnsTheLink(t *testing.T) {
	e := newEnv(t, envOptions{backends: &countingFactory{}})

	res := callIn(t, e, "connect_account", nil)

	require.False(t, res.IsError, textOf(t, res))
	out := structured(t, res)
	require.Equal(t, "https://mcp.test/connect", out["link"])
	require.Equal(t, false, out["changed"])
	require.Contains(t, textOf(t, res), "https://mcp.test/connect")
	requireNoToken(t, res)
}

// A client that can open a link for the user gets the link as a URL
// elicitation. After it, the tool gives the same text as without one.
func TestLinksByURLElicitation(t *testing.T) {
	urlClient := func(ask *asker) *mcp.ClientOptions {
		return &mcp.ClientOptions{
			ElicitationHandler: ask.handle,
			Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{
				URL: &mcp.URLElicitationCapabilities{},
			}},
		}
	}
	e := newEnv(t, envOptions{backends: &countingFactory{}})
	a := e.offlineAccount()
	settings := "https://mcp.test/settings/" + string(a.ID) + "?area=mail&level=5"

	cases := map[string]struct {
		tool string
		args map[string]any
		link string
	}{
		"request_permission_change": {"request_permission_change", map[string]any{"account": string(a.ID), "area": "mail", "level": 5}, settings},
		"connect_account":           {"connect_account", nil, "https://mcp.test/connect"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, action := range []string{"accept", "decline", "cancel"} {
				ask := &asker{answer: mcp.ElicitResult{Action: action}}
				cs := e.connect(urlClient(ask), nil)

				res := callOn(t, cs, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})

				require.False(t, res.IsError, textOf(t, res))
				require.Equal(t, tc.link, structured(t, res)["link"])
				asked := ask.requests()
				require.Len(t, asked, 1)
				require.Equal(t, "url", asked[0].Mode)
				require.Equal(t, tc.link, asked[0].URL)
				require.NotEmpty(t, asked[0].ElicitationID)
				require.NotEmpty(t, asked[0].Message)
				require.Nil(t, asked[0].RequestedSchema)
			}

			// A client that can only show forms gets the text at once.
			ask := accepting()
			cs := e.connect(ask.options(), nil)
			res := callOn(t, cs, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			require.False(t, res.IsError, textOf(t, res))
			require.Equal(t, tc.link, structured(t, res)["link"])
			require.Empty(t, ask.requests())
		})
	}
	stored, err := e.store.GetAccount(context.Background(), e.tenant, a.ID)
	require.NoError(t, err)
	require.Equal(t, account.MailSend, stored.Level(account.AreaMail))
}

// ---------------------------------------------------------------- organize, drafts, cancel

func TestOrganizeTools(t *testing.T) {
	imapSrv := imaptest.New(t)
	imapSrv.Create("Trash", imap.MailboxAttrTrash)
	imapSrv.Append("INBOX", plainMail("One", "first"))
	e := newEnv(t, envOptions{})
	a := e.mailAccount(imapSrv, smtptest.New(t), func(a *account.Account) { a.Levels[account.AreaMail] = account.MailOrganize })
	id := string(a.ID)
	refs := searchRefs(t, e, a, "@inbox")
	require.Len(t, refs, 1)

	t.Run("set_flags", func(t *testing.T) {
		res := callIn(t, e, "set_flags", map[string]any{"account": id, "refs": refs, "seen": true, "flagged": true})

		require.False(t, res.IsError, textOf(t, res))
		require.EqualValues(t, 1, structured(t, res)["count"])
		search := callIn(t, e, "search_messages", map[string]any{"account": id, "flagged": true})
		msgs := structured(t, search)["messages"].([]any)
		require.Len(t, msgs, 1)
		require.Equal(t, true, msgs[0].(map[string]any)["seen"])
	})

	t.Run("set_flags without a change", func(t *testing.T) {
		res := callIn(t, e, "set_flags", map[string]any{"account": id, "refs": refs})
		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "seen")
	})

	t.Run("create_tag and tag_messages", func(t *testing.T) {
		res := callIn(t, e, "create_tag", map[string]any{"account": id, "name": "urgent"})
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "urgent", structured(t, res)["name"])

		res = callIn(t, e, "tag_messages", map[string]any{"account": id, "refs": refs, "add": []any{"urgent"}})
		require.False(t, res.IsError, textOf(t, res))
		require.EqualValues(t, 1, structured(t, res)["count"])
		search := callIn(t, e, "search_messages", map[string]any{"account": id, "tags_any": []any{"urgent"}})
		require.Len(t, structured(t, search)["messages"], 1)
	})

	t.Run("create_folder and move_messages", func(t *testing.T) {
		res := callIn(t, e, "create_folder", map[string]any{"account": id, "name": "Projects"})
		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "Projects", structured(t, res)["name"])

		res = callIn(t, e, "move_messages", map[string]any{"account": id, "refs": refs, "folder": "Projects"})
		require.False(t, res.IsError, textOf(t, res))
		moved := structured(t, res)["refs"].([]any)
		require.Len(t, moved, 1)
		require.NotEqual(t, refs[0], moved[0], "a move gives a new ref")
		require.Len(t, searchRefs(t, e, a, "Projects"), 1)
		require.Empty(t, searchRefs(t, e, a, "@inbox"))
	})

	t.Run("move_messages to Trash is refused", func(t *testing.T) {
		moved := searchRefs(t, e, a, "Projects")
		res := callIn(t, e, "move_messages", map[string]any{"account": id, "refs": moved, "folder": "@trash"})

		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), "trash_messages")
		require.Len(t, searchRefs(t, e, a, "Projects"), 1)
	})

	t.Run("refs are needed", func(t *testing.T) {
		res := callIn(t, e, "tag_messages", map[string]any{"account": id, "refs": []any{}, "add": []any{"x"}})
		require.True(t, res.IsError)
		require.Contains(t, textOf(t, res), `"refs"`)
	})
}

func TestWriteToolRefusedBelowItsLevel(t *testing.T) {
	backends := &countingFactory{backend: &stubBackend{}}
	e := newEnv(t, envOptions{backends: backends})
	a := e.offlineAccount(func(a *account.Account) { a.Levels[account.AreaMail] = account.MailRead })

	res := callIn(t, e, "set_flags", map[string]any{"account": string(a.ID), "refs": []any{"r1"}, "seen": true})

	require.True(t, res.IsError)
	text := textOf(t, res)
	require.Contains(t, text, "https://mcp.test/settings/"+string(a.ID)+"?area=mail&level=2")
	require.True(t, strings.HasSuffix(text, endUntilUser), text)
	require.Zero(t, backends.calls.Load())
}

func TestDraftTools(t *testing.T) {
	imapSrv := imaptest.New(t)
	imapSrv.Create("Drafts", imap.MailboxAttrDrafts)
	imapSrv.Create("Trash", imap.MailboxAttrTrash)
	e := newEnv(t, envOptions{})
	a := e.mailAccount(imapSrv, smtptest.New(t), func(a *account.Account) { a.Levels[account.AreaMail] = account.MailDraft })
	id := string(a.ID)

	res := callIn(t, e, "create_draft", map[string]any{"account": id, "to": []any{"you@example.com"}, "subject": "Plan", "body": "Text"})

	require.False(t, res.IsError, textOf(t, res))
	ref := structured(t, res)["ref"]
	require.NotEmpty(t, ref)
	require.Len(t, searchRefs(t, e, a, "@drafts"), 1)

	res = callIn(t, e, "discard_draft", map[string]any{"account": id, "ref": ref})

	require.False(t, res.IsError, textOf(t, res))
	require.Equal(t, true, structured(t, res)["discarded"])
	require.Empty(t, searchRefs(t, e, a, "@drafts"))

	t.Run("a draft may have no recipients yet", func(t *testing.T) {
		res := callIn(t, e, "create_draft", map[string]any{"account": id, "subject": "Later"})
		require.False(t, res.IsError, textOf(t, res))
	})
}

func TestCancelSend(t *testing.T) {
	m := newMailSetup(t, envOptions{}, func(a *account.Account) { a.ConfirmSends = false })

	t.Run("a queued send", func(t *testing.T) {
		res := callIn(t, m.e, "send_message", sendArgs(m.a))
		require.False(t, res.IsError, textOf(t, res))
		out := structured(t, res)
		require.Equal(t, "queued", out["status"])
		require.NotEmpty(t, out["send_at"])

		res = callIn(t, m.e, "cancel_send", map[string]any{"account": string(m.a.ID), "send_id": out["send_id"]})

		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "cancelled", structured(t, res)["status"])
		require.Equal(t, "cancelled", sendStatus(t, m.e, m.a, out["send_id"]))
	})

	t.Run("a send that waits for approval", func(t *testing.T) {
		waiting := newMailSetup(t, envOptions{})
		res := callIn(t, waiting.e, "send_message", sendArgs(waiting.a))
		sendID := structured(t, res)["send_id"]

		res = callIn(t, waiting.e, "cancel_send", map[string]any{"account": string(waiting.a.ID), "send_id": sendID})

		require.False(t, res.IsError, textOf(t, res))
		require.Equal(t, "cancelled", structured(t, res)["status"])
		require.Equal(t, "cancelled", sendStatus(t, waiting.e, waiting.a, sendID), "the status says what the tool said")

		res = callIn(t, waiting.e, "cancel_send", map[string]any{"account": string(waiting.a.ID), "send_id": sendID})
		require.True(t, res.IsError, "it does not wait any more")
		require.Contains(t, textOf(t, res), "get_send_status")
	})
	require.Empty(t, m.smtp.Messages())
}

// ---------------------------------------------------------------- the confirmation parts

func TestCanElicitInBand(t *testing.T) {
	form := map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}
	empty := map[string]any{"elicitation": map[string]any{}}
	urlOnly := map[string]any{"elicitation": map[string]any{"url": map[string]any{}}}
	none := map[string]any{}
	tests := []struct {
		name    string
		version string
		caps    map[string]any
		legacy  bool
		want    bool
	}{
		{"new protocol with form", "2026-07-28", form, false, true},
		{"new protocol with an empty elicitation", "2026-07-28", empty, false, true},
		{"new protocol with only url", "2026-07-28", urlOnly, false, false},
		{"new protocol without elicitation", "2026-07-28", none, false, false},
		{"new protocol without capabilities", "2026-07-28", nil, false, false},
		{"older protocol with form", "2025-11-25", form, false, false},
		{"older protocol with form on stdio", "2025-11-25", form, true, true},
		{"oldest protocol with an empty elicitation on stdio", "2025-06-18", empty, true, true},
		{"older protocol without elicitation on stdio", "2025-11-25", none, true, false},
		{"no version", "", form, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := mcp.Meta{}
			if tc.version != "" {
				meta[mcp.MetaKeyProtocolVersion] = tc.version
			}
			if tc.caps != nil {
				meta[mcp.MetaKeyClientCapabilities] = tc.caps
			}
			req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: meta, Name: "send_message"}}

			require.Equal(t, tc.want, canElicitInBand(req, Options{InBandLegacyElicitation: tc.legacy}))
		})
	}
}

func TestPreviewText(t *testing.T) {
	p := confirm.Preview{
		Account:     "me@example.com",
		From:        "me@example.com" + rlo,
		To:          []string{"a@example.com", "b" + zwsp + "@example.com"},
		Subject:     "Invoice " + rlo + "fdp.exe\nTo: boss@corp.example" + tagA,
		Attachments: []string{"plan.pdf", "photo" + rlo + "gnp.exe"},
		BodyStart:   "Cc: ceo@corp.example\n" + strings.Repeat("é", 600),
	}

	got := sendPreview(p)

	requireNoHiddenText(t, got)
	lines := strings.SplitN(got, "\n", 7)
	require.Len(t, lines, 7)
	require.Equal(t, "Send this email from me@example.com?", lines[0])
	require.Equal(t, "To: a@example.com, b@example.com", lines[1])
	require.Equal(t, "Subject: Invoice fdp.exe To: boss@corp.example", lines[2], "a line break in a one-line field cannot add a line")
	require.Equal(t, "Attachments: 2 (plan.pdf, photognp.exe)", lines[3])
	require.Equal(t, "", lines[4], "a blank line before the body")
	require.Equal(t, "Body:", lines[5], "a label, so the body cannot pose as a header")
	require.True(t, strings.HasPrefix(lines[6], "Cc: ceo@corp.example\n"))
	require.Equal(t, 500, utf8.RuneCountInString(lines[6]), "at most 500 characters of the body")
}

func TestPreviewTextOneLineFields(t *testing.T) {
	t.Run("a run of whitespace is one space", func(t *testing.T) {
		got := sendPreview(confirm.Preview{From: "me@example.com", To: []string{"a@example.com"},
			Subject: "Invoice" + strings.Repeat(" ", 150) + "To:\t\u00a0 boss@corp.example"})
		require.Contains(t, got, "\nSubject: Invoice To: boss@corp.example\n")
	})

	t.Run("a long subject is cut", func(t *testing.T) {
		got := sendPreview(confirm.Preview{From: "me@example.com", To: []string{"a@example.com"},
			Subject: strings.Repeat("x", 300)})
		subject := strings.Split(got, "\n")[2]
		require.Equal(t, "Subject: "+strings.Repeat("x", 200)+"...", subject)
	})

	t.Run("no attachments", func(t *testing.T) {
		got := sendPreview(confirm.Preview{From: "me@example.com", To: []string{"a@example.com"}})
		require.Equal(t, "Attachments: none", strings.Split(got, "\n")[3])
	})

	t.Run("many attachments", func(t *testing.T) {
		names := make([]string, 12)
		for i := range names {
			names[i] = fmt.Sprintf("f%d.pdf", i+1)
		}
		got := sendPreview(confirm.Preview{From: "me@example.com", To: []string{"a@example.com"}, Attachments: names})
		require.Equal(t, "Attachments: 12 (f1.pdf, f2.pdf, f3.pdf, f4.pdf, f5.pdf, f6.pdf, f7.pdf, f8.pdf, f9.pdf, f10.pdf and 2 more)",
			strings.Split(got, "\n")[3])
	})

	t.Run("a long or empty attachment name", func(t *testing.T) {
		got := sendPreview(confirm.Preview{From: "me@example.com", To: []string{"a@example.com"},
			Attachments: []string{strings.Repeat("n", 150), "  "}})
		require.Equal(t, "Attachments: 2 ("+strings.Repeat("n", 100)+"..., (no name))", strings.Split(got, "\n")[3])
	})

	t.Run("trash", func(t *testing.T) {
		require.Equal(t, "Move 3 messages in me@example.com to Trash?",
			trashPreview(confirm.Preview{Account: "me@example.com" + rlo + "\n", Count: 3}))
		require.Equal(t, "Move 1 message in me@example.com to Trash?",
			trashPreview(confirm.Preview{Account: "me@example.com", Count: 1}))
	})
}

// ---------------------------------------------------------------- errors of the write tools

// policyReason returns a refused decision with the reason.
func policyReason(reason string) policy.Decision { return policy.Decision{Reason: reason} }

func TestWriteToolErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		err      error
		code     string
		contains []string
		hasNo    []string
		ends     string
	}{
		{"a state that is not valid", "send_message", fmt.Errorf("claim: %w", confirm.ErrStateInvalid), "confirmation",
			[]string{"not valid", "Nothing was done"}, nil, endNoRepeat},
		{"an unknown action", "trash_messages", confirm.ErrNotFound, "confirmation", []string{"not valid"}, nil, endNoRepeat},
		{"a state that expired", "send_message", confirm.ErrStateExpired, "confirmation_expired",
			[]string{"expired", "10 minutes"}, nil, endNoRepeat},
		{"other arguments", "send_message", fmt.Errorf("%w: hash 3f9a", confirm.ErrPayloadChanged), "payload_changed",
			[]string{"not the one", "Nothing was done"}, []string{"3f9a"}, endNoRepeat},
		{"not pending", "cancel_send", confirm.ErrNotPending, "not_pending", []string{"get_send_status"}, nil, endNoRepeat},
		{"a limit", "trash_messages", fmt.Errorf("%w: 600 messages", service.ErrLimit), "limit",
			[]string{"limit"}, []string{"600"}, endNoRepeat},
		{"a move to Trash", "move_messages", service.ErrMoveToTrash, "move_to_trash",
			[]string{"trash_messages", "inside Trash"}, []string{"create_folder"}, endNoRepeat},
		{"a folder inside Trash", "create_folder", service.ErrMoveToTrash, "move_to_trash",
			[]string{"create_folder cannot make a folder inside Trash", "trash_messages"}, []string{"move_messages cannot"}, endNoRepeat},
		{"a draft of another address", "send_message", service.ErrDraftNotFromAccount, "draft_from_mismatch",
			[]string{"not from the address"}, nil, endNoRepeat},
		{"a bad address", "send_message", fmt.Errorf("%w: to", mime.ErrInvalidAddress), "bad_message",
			[]string{"address"}, nil, endNoRepeat},
		{"no recipients", "send_message", mime.ErrNoRecipients, "bad_message", []string{"no recipients"}, nil, endNoRepeat},
		{"a line break in a header", "create_draft", mime.ErrHeaderInjection, "bad_message", []string{"line break"}, nil, endNoRepeat},
		{"a reply and a forward", "send_message", mime.ErrReplyAndForward, "bad_message", []string{"reply_to or forward_of"}, nil, endNoRepeat},
		{"a bad content type", "send_message", mime.ErrInvalidContentType, "bad_message", []string{"content type"}, nil, endNoRepeat},
		{"a hidden character in a file name", "send_message", fmt.Errorf("%w: attachment file name", mime.ErrInvalidFilename),
			"bad_message", []string{"file name", "U+202E"}, nil, endNoRepeat},
		{"body and html body", "send_message", service.ErrBodyAndHTML, "body_and_html", []string{"body or html_body"}, nil, endNoRepeat},
		{"an address that cannot be shown", "send_message", service.ErrHiddenAddress, "hidden_address",
			[]string{"address", "hidden", "nothing was sent"}, nil, endNoRepeat},
		{"a draft that cannot be read", "send_message", service.ErrDraftUnreadable, "draft_unreadable",
			[]string{"draft", "new message"}, nil, endNoRepeat},
		{"html too complex", "send_message", fmt.Errorf("%w: 100000 nodes", mime.ErrHTMLTooComplex), "bad_message",
			[]string{"HTML"}, []string{"100000"}, endNoRepeat},
		{"a conflict", "create_folder", fmt.Errorf("%w: ALREADYEXISTS secret-folder", mail.ErrConflict), "conflict",
			[]string{"already exists"}, []string{"secret-folder"}, endNoRepeat},
		{"too late to cancel", "cancel_send", outbox.ErrTooLate, "too_late", []string{"too late", "get_send_status"}, nil, endNoRepeat},
		{"attachments too large to send", "send_message", fmt.Errorf("%w: attachments", mail.ErrTooLarge), "too_large",
			[]string{"10 MB", "attachments"}, []string{"body_format"}, endNoRepeat},
		{"attachments too large for a draft", "create_draft", mail.ErrTooLarge, "too_large",
			[]string{"10 MB", "attachments"}, []string{"body_format"}, endNoRepeat},
		{"a body too large to read", "get_message", mail.ErrTooLarge, "too_large", []string{`body_format "none"`}, nil, endNoRepeat},
		{"permission", "set_flags", &service.PermissionError{
			Decision: policyReason("Organizing needs level 2 (Organize)."), Link: "https://mcp.test/settings/a1?area=mail&level=2",
		}, "permission", []string{"needs level 2", "https://mcp.test/settings/a1?area=mail&level=2"}, nil, endUntilUser},
		{"permission without a link", "set_flags", &service.PermissionError{Decision: policyReason("Organizing needs level 2.")},
			"permission", []string{"email-mcp settings"}, nil, endUntilUser},
		{"reauth", "send_message", &service.ReauthError{Link: "https://mcp.test/settings/a1?area=mail&level=4"},
			"needs_reauth", []string{"reconnect"}, nil, endUntilUser},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toolError(tc.tool, tc.err)

			require.Equal(t, tc.code, got.code)
			for _, want := range tc.contains {
				require.Contains(t, got.text, want)
			}
			for _, bad := range tc.hasNo {
				require.NotContains(t, got.text, bad)
			}
			require.True(t, strings.HasSuffix(got.text, tc.ends), got.text)
		})
	}
}

// ---------------------------------------------------------------- fix round 1

// The person sees the attachments before a send. Data that an agent read and
// attaches again cannot leave without the person seeing it.
func TestSendFormShowsTheAttachments(t *testing.T) {
	ask := accepting()
	m := newMailSetup(t, envOptions{client: ask.options()})
	args := sendArgs(m.a)
	args["attachments"] = []any{
		map[string]any{"filename": "report.pdf", "content_type": "application/pdf", "data_base64": base64.StdEncoding.EncodeToString([]byte("%PDF"))},
	}

	res := callIn(t, m.e, "send_message", args)

	require.False(t, res.IsError, textOf(t, res))
	asked := ask.requests()
	require.Len(t, asked, 1)
	require.Contains(t, asked[0].Message, "\nAttachments: 1 (report.pdf)\n")
}

func TestSendRefusesBodyAndHTMLBody(t *testing.T) {
	m := newMailSetup(t, envOptions{client: accepting().options()})
	args := sendArgs(m.a)
	args["html_body"] = "<p>Another text</p>"

	for _, tool := range []string{"send_message", "create_draft"} {
		res := callIn(t, m.e, tool, args)

		require.True(t, res.IsError, tool)
		require.Contains(t, textOf(t, res), "body or html_body")
	}
	require.Empty(t, m.smtp.Messages())
}

// On the path for older clients the SDK asks with elicitation/create and applies
// the default of the form: a form sent empty is a no.
func TestOlderClientEmptyAnswerIsANo(t *testing.T) {
	answers := map[string]mcp.ElicitResult{
		"accept with an empty form": {Action: "accept", Content: map[string]any{}},
		"accept with no content":    {Action: "accept"},
		"decline":                   {Action: "decline"},
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			ask := &asker{answer: answer}
			m := newMailSetup(t, envOptions{
				client:            ask.options(),
				session:           &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"},
				legacyElicitation: true,
			})

			res := callIn(t, m.e, "send_message", sendArgs(m.a))

			require.False(t, res.IsError, textOf(t, res))
			require.Equal(t, "declined", structured(t, res)["status"])
			require.Len(t, ask.requests(), 1)
			require.Empty(t, m.smtp.Messages())
		})
	}
}

// ---------------------------------------------------------------- fix round 2

// The read path wraps mime.ErrHTMLTooComplex in mail.ErrTooLarge. A read tool
// must give the read advice, not the text for a message that is being written.
func TestGetMessageWithHTMLTooComplex(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", htmlMail("Deep", strings.Repeat("<div>", 1000)+"text"))
	e := newEnv(t, envOptions{})
	a := e.imapAccount(srv)
	refs := searchRefs(t, e, a, "@inbox")
	require.Len(t, refs, 1)

	res := callIn(t, e, "get_message", map[string]any{"account": string(a.ID), "ref": refs[0]})

	require.True(t, res.IsError)
	require.Contains(t, textOf(t, res), `body_format "none"`)
	require.NotContains(t, textOf(t, res), "simpler HTML")

	wrapped := fmt.Errorf("%w: the HTML body is too complex (%w)", mail.ErrTooLarge, mime.ErrHTMLTooComplex)
	require.Equal(t, codeTooLarge, toolError("get_message", wrapped).code)
	require.Equal(t, codeBadMessage, toolError("send_message", mime.ErrHTMLTooComplex).code)
}
