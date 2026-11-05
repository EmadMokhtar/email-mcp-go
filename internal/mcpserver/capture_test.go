package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// callEntries returns the call entries of the tenant, newest first.
func (e *env) callEntries() []audit.Entry {
	e.t.Helper()
	list, err := audit.New(e.store, e.vault, nil).List(context.Background(), e.tenant,
		audit.Filter{Kind: string(audit.KindCall), Limit: 1000})
	require.NoError(e.t, err)
	return list
}

// lastCall returns the newest call entry.
func (e *env) lastCall() audit.Entry {
	e.t.Helper()
	list := e.callEntries()
	require.NotEmpty(e.t, list, "a call entry was expected")
	return list[0]
}

// stubEnv is an env whose accounts use the backend b.
func stubEnv(t *testing.T, b *stubBackend) *env {
	t.Helper()
	return newEnv(t, envOptions{backends: &countingFactory{backend: b}})
}

func levelOf(l account.Level) func(*account.Account) {
	return func(a *account.Account) { a.Levels[account.AreaMail] = l }
}

func TestEveryToolHasOneCallEntryForEachCall(t *testing.T) {
	e := stubEnv(t, &stubBackend{})
	a := e.offlineAccount()
	tools, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools.Tools)

	for _, tool := range tools.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			args := requiredArguments(t, tool)
			if _, ok := args["account"]; ok {
				args["account"] = string(a.ID)
			}
			before := len(e.callEntries())

			e.call(tool.Name, args)

			require.Len(t, e.callEntries(), before+1)
			require.Equal(t, tool.Name, e.lastCall().Tool)
		})
	}
}

func TestOtherRequestsHaveNoCallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{})

	_, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	require.Empty(t, e.callEntries())
}

func TestAReadThatWorksHasACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{search: func(mail.Query) (mail.Page[mail.Summary], error) {
		return mail.Page[mail.Summary]{}, nil
	}})
	a := e.offlineAccount()

	res := e.call("search_messages", map[string]any{"account": string(a.ID), "subject": "quarterly numbers"})

	require.False(t, res.IsError, textOf(t, res))
	got := e.lastCall()
	require.Equal(t, "search_messages", got.Tool)
	require.Equal(t, a.ID, got.Account)
	require.Equal(t, account.AreaMail, got.Area)
	require.Equal(t, "test-client", got.Client)
	require.Equal(t, audit.OutcomeOK, got.Outcome)
	require.Empty(t, got.Detail)
	require.JSONEq(t, fmt.Sprintf(`{"account":%q,"subject":"quarterly numbers"}`, a.ID), string(got.Arguments))
}

func TestAToolWithoutArgumentsHasNoSealedArguments(t *testing.T) {
	e := stubEnv(t, &stubBackend{})

	e.call("list_accounts", map[string]any{})

	got := e.lastCall()
	require.Equal(t, "list_accounts", got.Tool)
	require.Nil(t, got.Arguments)
	require.Empty(t, got.Account)
}

func TestARefusedReadHasACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{})
	a := e.offlineAccount(levelOf(account.MailOff))

	res := e.call("get_message", map[string]any{"account": string(a.ID), "ref": "r1"})

	require.True(t, res.IsError)
	got := e.lastCall()
	require.Equal(t, audit.OutcomeRefused, got.Outcome)
	require.Equal(t, "permission", got.Detail)
	require.Equal(t, a.ID, got.Account)
	require.JSONEq(t, fmt.Sprintf(`{"account":%q,"ref":"r1"}`, a.ID), string(got.Arguments))
}

func TestACallForAnAccountThatIsNotThereHasACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{})

	e.call("get_message", map[string]any{"account": "no-such-account", "ref": "r1"})

	got := e.lastCall()
	require.Equal(t, audit.OutcomeRefused, got.Outcome)
	require.Equal(t, "account_not_found", got.Detail)
	require.Empty(t, got.Account)
	require.JSONEq(t, `{"account":"no-such-account","ref":"r1"}`, string(got.Arguments))
}

func TestBadArgumentsHaveACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{})
	a := e.offlineAccount()

	res := e.call("set_flags", map[string]any{"account": string(a.ID), "refs": []any{}, "seen": true})

	require.True(t, res.IsError)
	got := e.lastCall()
	require.Equal(t, audit.OutcomeRefused, got.Outcome)
	require.Equal(t, "bad_argument", got.Detail)
	require.Equal(t, a.ID, got.Account)
}

// The SDK checks the arguments against the schema of the tool before it calls the
// handler, so the wrapper of the handler never sees this call.
func TestArgumentsThatFailTheSchemaHaveACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{})
	a := e.offlineAccount()
	before := e.principals.Load()

	res := e.call("set_flags", map[string]any{"account": string(a.ID), "refs": "not a list"})

	require.True(t, res.IsError)
	got := e.lastCall()
	require.Equal(t, "set_flags", got.Tool)
	require.Equal(t, audit.OutcomeRefused, got.Outcome)
	require.Equal(t, "invalid_arguments", got.Detail)
	require.Equal(t, a.ID, got.Account)
	require.Equal(t, "test-client", got.Client)
	require.JSONEq(t, fmt.Sprintf(`{"account":%q,"refs":"not a list"}`, a.ID), string(got.Arguments))
	require.Equal(t, before+1, e.principals.Load())
}

// The name of a tool that does not exist comes from the agent, so it can be
// anything. The entry must be written, and the name must not reach the column.
func TestAnUnknownToolHasACallEntry(t *testing.T) {
	longName := strings.Repeat("x", 5000)
	cases := map[string]struct{ name, want string }{
		"a plain name":  {"no_such_tool", "no_such_tool"},
		"control chars": {"bad\nname", "bad\nname"},
		"a long name":   {longName, strings.Repeat("x", 2048) + "[cut: 5000 bytes in all]"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			e := stubEnv(t, &stubBackend{})

			_, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: tc.name, Arguments: map[string]any{"text": "find this"},
			})

			require.Error(t, err)
			got := e.lastCall()
			require.Equal(t, "unknown_tool", got.Tool)
			require.Equal(t, audit.OutcomeRefused, got.Outcome)
			require.Equal(t, "unknown_tool", got.Detail)
			var args struct {
				RequestedTool string         `json:"requested_tool"`
				Arguments     map[string]any `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal(got.Arguments, &args))
			require.Equal(t, tc.want, args.RequestedTool)
			require.Equal(t, map[string]any{"text": "find this"}, args.Arguments)
		})
	}
}

func TestAPanicHasACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{}) // folders is nil, so the call panics
	a := e.offlineAccount()

	res := e.call("list_folders", map[string]any{"account": string(a.ID)})

	require.True(t, res.IsError)
	got := e.lastCall()
	require.Equal(t, audit.OutcomeError, got.Outcome)
	require.Equal(t, "panic", got.Detail)
}

func TestAMailServerFailureHasACallEntry(t *testing.T) {
	e := stubEnv(t, &stubBackend{folders: func() ([]mail.Folder, error) {
		return nil, context.DeadlineExceeded
	}})
	a := e.offlineAccount()

	e.call("list_folders", map[string]any{"account": string(a.ID)})

	got := e.lastCall()
	require.Equal(t, audit.OutcomeError, got.Outcome)
	require.Equal(t, "timeout", got.Detail)
}

// A confirmation in band is two requests, so it has two call entries.
func TestAFirstCallThatAsksForInputHasACallEntry(t *testing.T) {
	m := newMailSetup(t, envOptions{})
	cs := rawClient(t, m.e)

	state := firstCall(t, cs, "send_message", sendArgs(m.a))

	got := m.e.lastCall()
	require.Equal(t, "send_message", got.Tool)
	require.Equal(t, audit.OutcomePendingApproval, got.Outcome)
	require.Equal(t, "input_required", got.Detail)

	answerCall(t, cs, "send_message", sendArgs(m.a), mcp.InputResponseMap{
		"confirm": &mcp.ElicitResult{Action: "decline"},
	}, state)

	require.Len(t, m.e.callEntries(), 2)
}

// The table needs a tenant, so a call without a caller has no row. The refusal is
// in the program log. Such a call must not stop the server.
func TestACallerThatIsNotKnownHasNoCallEntry(t *testing.T) {
	principals := map[string]func(account.TenantID, *mcp.CallToolRequest) (service.Principal, error){
		"an error": func(account.TenantID, *mcp.CallToolRequest) (service.Principal, error) {
			return service.Principal{}, errors.New("no")
		},
		"no tenant": func(account.TenantID, *mcp.CallToolRequest) (service.Principal, error) {
			return service.Principal{Client: "c"}, nil
		},
		"a panic": func(account.TenantID, *mcp.CallToolRequest) (service.Principal, error) {
			panic("no caller")
		},
	}
	for label, principal := range principals {
		t.Run(label, func(t *testing.T) {
			e := newEnv(t, envOptions{backends: &countingFactory{backend: &stubBackend{}}, principal: principal})

			e.call("list_accounts", map[string]any{})                        // reaches the wrapper
			e.call("set_flags", map[string]any{"account": "a", "refs": "x"}) // stops at the schema
			_, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"})
			require.Error(t, err)

			require.Empty(t, e.callEntries())
			require.Contains(t, e.logs.String(), "unknown_tool", "the log names a fixed tool, not the text of the agent")
			require.NotContains(t, e.logs.String(), "no_such_tool")
			_, err = e.cs.ListTools(context.Background(), nil)
			require.NoError(t, err, "the server still runs")
		})
	}
}

func TestBodiesAndAttachmentDataAreNotCaptured(t *testing.T) {
	const data = "RGF0YU1hcmtlclE3djNLcA=="
	e := stubEnv(t, &stubBackend{})
	a := e.offlineAccount(levelOf(account.MailRead))

	res := e.call("send_message", map[string]any{
		"account": string(a.ID),
		"to":      []any{"bob@example.org"},
		"subject": "SubjectMarker",
		"body":    "BodyMarkerQ7v3Kp",
		"attachments": []any{map[string]any{
			"filename": "plan.pdf", "content_type": "application/pdf", "data_base64": data,
		}},
	})

	require.True(t, res.IsError)
	got := e.lastCall()
	require.JSONEq(t, fmt.Sprintf(`{
		"account": %q,
		"to": ["bob@example.org"],
		"subject": "SubjectMarker",
		"body": "[omitted: 16 bytes]",
		"attachments": [{
			"filename": "plan.pdf", "content_type": "application/pdf", "data_base64": "[omitted: 24 bytes]"
		}]
	}`, a.ID), string(got.Arguments))
	require.NotContains(t, e.logs.String(), "SubjectMarker", "arguments never reach the program log")
}

func TestCleanArguments(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"no arguments":    {``, ``},
		"an empty object": {`{}`, ``},
		"JSON null":       {`null`, ``},
		"plain fields are kept": {
			`{"account":"a1","refs":["r1","r2"],"seen":true,"level":5}`,
			`{"account":"a1","level":5,"refs":["r1","r2"],"seen":true}`,
		},
		"a body":       {`{"body":"hello"}`, `{"body":"[omitted: 5 bytes]"}`},
		"an HTML body": {`{"html_body":"<p>hi</p>"}`, `{"html_body":"[omitted: 9 bytes]"}`},
		"attachment data, nested": {
			`{"attachments":[{"filename":"a.txt","data_base64":"QUJD"}]}`,
			`{"attachments":[{"data_base64":"[omitted: 4 bytes]","filename":"a.txt"}]}`,
		},
		"another case of a body": {`{"Body":"hello"}`, `{"Body":"[omitted: 5 bytes]"}`},
		"a body that is no text": {`{"body":{"text":"hello"}}`, `{"body":"[omitted: 16 bytes]"}`},
		"a body in a call that is not an object": {
			`[{"body":"hello"}]`, `[{"body":"[omitted: 5 bytes]"}]`,
		},
		"a large number stays exact": {`{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
		"text stays readable":        {`{"text":"a<b&c"}`, `{"text":"a<b&c"}`},
		"JSON that cannot be read":   {`{"a":`, `{"bytes":5,"omitted":"unreadable"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := cleanArguments(decodeArguments(json.RawMessage(tc.in)))
			require.Equal(t, tc.want, string(got))
		})
	}
}

func TestCleanArgumentsCutsLongText(t *testing.T) {
	long := strings.Repeat("a", 3000)
	got := cleanArguments(decodeArguments(json.RawMessage(`{"subject":"` + long + `"}`)))
	require.Equal(t, `{"subject":"`+strings.Repeat("a", 2048)+`[cut: 3000 bytes in all]"}`, string(got))

	// The cut must not fall inside a character: 2048 is in the middle of one here.
	wide := "x" + strings.Repeat("é", 1500)
	got = cleanArguments(decodeArguments(json.RawMessage(`{"subject":"` + wide + `"}`)))
	require.Equal(t, `{"subject":"x`+strings.Repeat("é", 1023)+`[cut: 3001 bytes in all]"}`, string(got))
}

func TestCleanArgumentsKeepsALargeBatchOfRefs(t *testing.T) {
	refs := make([]string, 500)
	for i := range refs {
		refs[i] = strings.Repeat("r", 300)
	}
	raw, err := json.Marshal(map[string]any{"refs": refs})
	require.NoError(t, err)

	var got struct {
		Refs []string `json:"refs"`
	}
	require.NoError(t, json.Unmarshal(cleanArguments(decodeArguments(raw)), &got))
	require.Len(t, got.Refs, 500)
}

func TestCleanArgumentsNeverPassesTheLimitOfTheLog(t *testing.T) {
	refs := make([]string, 2000)
	for i := range refs {
		refs[i] = strings.Repeat("r", 300)
	}
	raw, err := json.Marshal(map[string]any{"refs": refs})
	require.NoError(t, err)

	got := cleanArguments(decodeArguments(raw))

	require.LessOrEqual(t, len(got), audit.MaxArgumentBytes)
	var out struct {
		Omitted string `json:"omitted"`
		Bytes   int    `json:"bytes"`
	}
	require.NoError(t, json.Unmarshal(got, &out))
	require.Equal(t, "too_large", out.Omitted)
	require.Greater(t, out.Bytes, audit.MaxArgumentBytes)
}

func TestOutcomeOfACode(t *testing.T) {
	cases := map[string]audit.Outcome{
		"permission":     audit.OutcomeRefused,
		"token_scope":    audit.OutcomeRefused,
		"rate_limit":     audit.OutcomeRefused,
		"bad_argument":   audit.OutcomeRefused,
		"internal_error": audit.OutcomeError,
		"timeout":        audit.OutcomeError,
		"partial":        audit.OutcomeError,
		"a_new_code":     audit.OutcomeError,
	}
	for code, want := range cases {
		require.Equal(t, want, outcomeOfCode(code), code)
	}
}

// keptArguments are the arguments that the log keeps, sealed. Add a new argument
// here or to omittedArguments on purpose: the log must not start to hold message
// content by accident.
var keptArguments = []string{
	"account", "add", "all", "area", "attachment_id", "attachments", "bcc", "before", "body_format", "cc", "content_type",
	"cursor", "draft_ref", "filename", "flagged", "folder", "forward_of", "from", "include_original_attachments", "level",
	"limit", "max_chars", "name", "offset", "page_token", "parent", "ref", "refs", "remove", "reply_to", "seen", "send_id",
	"since", "subject", "tags_any", "tags_none", "text", "to", "unread", "untagged",
}

func TestEveryArgumentOfEveryToolIsKeptOrOmitted(t *testing.T) {
	e := stubEnv(t, &stubBackend{})
	tools, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	for _, tool := range tools.Tools {
		for _, name := range propertyNames(t, tool.InputSchema) {
			if !omittedArguments[name] {
				require.Contains(t, keptArguments, name, "tool %s: decide whether the log may keep this argument", tool.Name)
			}
		}
	}
}

// propertyNames returns the names of the properties of a JSON schema, also those
// of nested objects.
func propertyNames(t *testing.T, schema any) []string {
	t.Helper()
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	var node struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Items      json.RawMessage            `json:"items"`
	}
	require.NoError(t, json.Unmarshal(raw, &node))
	var names []string
	for name, sub := range node.Properties {
		names = append(names, name)
		names = append(names, propertyNames(t, sub)...)
	}
	if node.Items != nil {
		names = append(names, propertyNames(t, node.Items)...)
	}
	return names
}

// The activity page offers a filter of the tools, so every tool must be in it.
func TestEveryToolIsInTheFilterOfTheActivityPage(t *testing.T) {
	e := stubEnv(t, &stubBackend{})
	tools, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	for _, tool := range tools.Tools {
		require.Contains(t, service.AuditTools(), tool.Name)
	}
}
