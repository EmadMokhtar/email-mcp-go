package mcpserver

// This file writes a call entry in the audit log for every tools/call request:
// every tool and every outcome, also a read, a refusal and a call that never
// reached a handler.
//
// The work is in a receiving middleware of the SDK, and not in safe, because the
// SDK checks the name of the tool and the schema of the arguments before it calls
// the handler. Only the middleware sees those calls. safe tells the middleware
// what it knows through a callTrace in the context: the caller and the code of a
// failure.
//
// The rules:
//   - A failure to write the entry does not change the answer of the call. See
//     service.RecordToolCall.
//   - The log keeps the values of the arguments only for a call that passed the
//     schema of its tool, without the content of a message (see cleanArguments).
//     No tool accepts an argument that it does not have, so such a call holds only
//     known arguments. A call that did not pass can hold the text of a message
//     under any name, so the log keeps only its names and sizes (see
//     argumentSizes). The service seals both.
//   - The arguments go nowhere else: never to the program log.
//   - There is no limit on the number of entries, on purpose: a limit would let a
//     flood of calls hide what an agent did. A write has its own short time limit
//     (see audit.Record), and a call that did not pass the schema needs little
//     room (see maxRejectedArgumentBytes).
//   - The table needs a tenant, so a call without a caller has no entry. The
//     refusal is in the program log, with a fixed code.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// methodCallTool is the MCP method of a tool call.
const methodCallTool = "tools/call"

// The details of a call that has no failure code, because safe did not see it.
const (
	detailApprovalRequired = "approval_required"
	detailDeclined         = "declined"
	detailInputRequired    = "input_required"
	detailInvalidArguments = "invalid_arguments"
	detailProtocolError    = "protocol_error"
	detailUnknownTool      = "unknown_tool"
)

// maxArgumentText is the most bytes that the log keeps of one text in the
// arguments. A ref, a cursor and a page token are all shorter.
const maxArgumentText = 2 << 10

// maxRejectedArgumentBytes is the most bytes that the log keeps of the arguments
// of a call that did not pass the schema. Only names and sizes stay of such a
// call, and it costs an agent nothing to make one.
const maxRejectedArgumentBytes = 4 << 10

// omittedArguments are the arguments of the tools that the log never keeps: the
// content of a message. A test makes sure that every argument of every tool is
// either here or on purpose not here.
var omittedArguments = map[string]bool{
	"body":        true,
	"html_body":   true,
	"data_base64": true,
}

// refusedCodes are the failure codes of this layer that mean that the server
// refused the request. Every other code means that the tool failed. A failure of
// the service gets its outcome from service.AuditCode, not from here.
var refusedCodes = map[string]bool{
	codeNotAuthorized: true,
	codeBadArgument:   true,
	codeBadToken:      true,
	codeConfirmation:  true,
}

// failureOutcome returns the outcome and the detail of a failure. A failure of the
// service has the words of its own entry, so one refusal reads the same in the two
// rows of a call. code is the code of the failure in this layer.
func failureOutcome(err error, code string) (audit.Outcome, string) {
	if outcome, detail, known := service.AuditCode(err); known {
		return outcome, detail
	}
	return outcomeOfCode(code), code
}

// outcomeOfCode returns the outcome of a failure code.
func outcomeOfCode(code string) audit.Outcome {
	if refusedCodes[code] {
		return audit.OutcomeRefused
	}
	return audit.OutcomeError
}

// callTrace is what safe learns during one tool call. A nil *callTrace is valid
// and keeps nothing: safe can run without the middleware.
type callTrace struct {
	// caller is the caller that safe found. It is nil when safe did not run.
	caller *service.Principal
	// noCaller is true when safe refused the caller.
	noCaller bool
	// outcome and detail say how the call ended, when that is not a plain success.
	// detail is empty otherwise.
	outcome audit.Outcome
	detail  string
}

// setCaller remembers the caller of the call.
func (t *callTrace) setCaller(p service.Principal) {
	if t != nil {
		t.caller = &p
	}
}

// refuseCaller remembers that the call has no caller.
func (t *callTrace) refuseCaller() {
	if t != nil {
		t.noCaller = true
	}
}

// end remembers how the call ended.
func (t *callTrace) end(outcome audit.Outcome, detail string) {
	if t != nil {
		t.outcome, t.detail = outcome, detail
	}
}

// noteStatus remembers the status of a send or a move to Trash. The tool answers
// without an error when the action waits for the user or was declined, and yet
// nothing happened, so such a call must not read as a success. The send and the
// move use the same words for these two states.
func (t *callTrace) noteStatus(status string) {
	switch status {
	case service.SendPendingApproval:
		t.end(audit.OutcomePendingApproval, detailApprovalRequired)
	case service.SendDeclined:
		t.end(audit.OutcomeDeclined, detailDeclined)
	}
}

// traceKey is the context key of the callTrace.
type traceKey struct{}

// traceFrom returns the callTrace of the context, or nil.
func traceFrom(ctx context.Context) *callTrace {
	t, _ := ctx.Value(traceKey{}).(*callTrace)
	return t
}

// capture is the middleware that records the tool calls. Other requests pass
// through it.
func (d *deps) capture(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		call, ok := req.(*mcp.CallToolRequest)
		if !ok || method != methodCallTool {
			return next(ctx, method, req)
		}
		trace := &callTrace{}
		traced, found := service.WithAccountNote(context.WithValue(ctx, traceKey{}, trace))
		res, err := next(traced, method, req)
		d.recordCall(ctx, call, trace, *found, res, err)
		return res, err
	}
}

// recordCall writes the call entry of a finished tool call. found is the account
// that a use case found, if any.
func (d *deps) recordCall(ctx context.Context, req *mcp.CallToolRequest, trace *callTrace, found account.AccountID, res mcp.Result, err error) {
	name := toolName(req)
	known := d.tools[name]
	tool := name
	if !known {
		tool = service.ToolUnknown
	}
	caller, ok := d.callerOf(ctx, req, trace, tool)
	if !ok {
		return
	}

	var raw json.RawMessage
	if req.Params != nil {
		raw = req.Params.Arguments
	}
	args := decodeArguments(raw)
	c := service.ToolCall{Tool: tool, Account: accountArgument(args)}
	if found != "" {
		c.Account, c.AccountFound = found, true
	}
	c.Outcome, c.Detail = callOutcome(known, trace, res, err)
	switch {
	case trace.caller != nil:
		// safe ran, and the SDK calls it only after the schema check.
		c.Arguments = cleanArguments(args)
	case known:
		c.Arguments = encodeKept(argumentSizes(args), maxRejectedArgumentBytes)
	default:
		// The name is text from the agent, so it goes in the sealed arguments.
		kept := map[string]any{"requested_tool": cutText(name), "arguments": argumentSizes(args)}
		c.Arguments = encodeKept(kept, maxRejectedArgumentBytes)
	}
	d.svc.RecordToolCall(ctx, caller, c)
}

// callerOf returns the caller of a finished tool call. safe found it for a call
// that reached a handler. For a call that stopped before that, it asks
// Options.Principal, and a panic of Principal means that there is no caller, as
// it does in safe. tool goes in the log, so it must never be the text of the
// agent.
func (d *deps) callerOf(ctx context.Context, req *mcp.CallToolRequest, trace *callTrace, tool string) (caller service.Principal, ok bool) {
	switch {
	case trace.caller != nil:
		return *trace.caller, true
	case trace.noCaller:
		return service.Principal{}, false
	}
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("tool call failed", "tool", tool, "code", codePanic, "panic_type", fmt.Sprintf("%T", r))
			caller, ok = service.Principal{}, false
		}
	}()
	p, err := d.opts.Principal(ctx, req)
	if err != nil || p.Tenant == "" {
		d.log.Warn("tool call refused", "tool", tool, "code", codeNotAuthorized)
		return service.Principal{}, false
	}
	return p, true
}

// callOutcome returns the outcome and the detail of a finished tool call.
func callOutcome(known bool, trace *callTrace, res mcp.Result, err error) (audit.Outcome, string) {
	if !known {
		return audit.OutcomeRefused, detailUnknownTool
	}
	if trace.detail != "" {
		return trace.outcome, trace.detail
	}
	result, ok := res.(*mcp.CallToolResult)
	switch {
	case err != nil, !ok, result == nil:
		return audit.OutcomeError, detailProtocolError
	case result.IsError && trace.caller == nil:
		// Only the check of the SDK before the handler gives this result.
		return audit.OutcomeRefused, detailInvalidArguments
	case result.IsError:
		return audit.OutcomeError, codeInternal
	case len(result.InputRequests) > 0:
		return audit.OutcomePendingApproval, detailInputRequired
	}
	return audit.OutcomeOK, ""
}

// stripOmitted returns raw with the text of each argument in omittedArguments
// replaced by its size, as a number. It keeps the JSON decoder from copying
// megabytes of attachment data that the log drops anyway.
//
// It is only a saving, and the log does not depend on it: cleanValue leaves out an
// argument that this scan does not see, such as a name that is written with an
// escape. In valid JSON only a name has a colon after it, so a text that looks like
// a name is not touched. For invalid JSON the result does not matter, because the
// decoder refuses it.
func stripOmitted(raw []byte) []byte {
	var out []byte
	copied := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		end := stringEnd(raw, i)
		if end < 0 {
			break
		}
		colon := skipSpace(raw, end+1)
		value := skipSpace(raw, colon+1)
		if colon < len(raw) && raw[colon] == ':' && value < len(raw) && raw[value] == '"' && omittedArguments[string(raw[i+1:end])] {
			valueEnd := stringEnd(raw, value)
			if valueEnd < 0 {
				break
			}
			out = append(out, raw[copied:value]...)
			out = strconv.AppendInt(out, int64(valueEnd-value-1), 10)
			copied = valueEnd + 1
			end = valueEnd
		}
		i = end
	}
	if out == nil {
		return raw
	}
	return append(out, raw[copied:]...)
}

// stringEnd returns the index of the quote that ends the JSON text that starts at
// raw[start], or -1 when it does not end.
func stringEnd(raw []byte, start int) int {
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// skipSpace returns the index of the first byte at or after i that is not JSON
// white space.
func skipSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	return i
}

// decodeArguments reads the JSON of the arguments of a call. It returns nil when
// there are none. Numbers stay as they are written.
func decodeArguments(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(stripOmitted(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// Without a parse nothing can be said about the content, so none is kept.
		return omittedMark("unreadable", len(raw))
	}
	return v
}

// accountArgument returns the account of decoded arguments, or an empty id.
func accountArgument(args any) account.AccountID {
	m, _ := args.(map[string]any)
	id, _ := m["account"].(string)
	return account.AccountID(id)
}

// cleanArguments returns the JSON that the log keeps of the decoded arguments of a
// call that passed the schema, or nil when there are none. Each argument in
// omittedArguments becomes a short mark with its size, at any depth, and a long
// text is cut (see cutText).
func cleanArguments(args any) json.RawMessage {
	if noArguments(args) {
		return nil
	}
	return encodeKept(cleanValue(args), audit.MaxArgumentBytes)
}

// argumentSizes returns what the log keeps of the decoded arguments of a call that
// did not pass the schema: the name and the size of each argument, and no value.
// It returns nil when there are none.
func argumentSizes(args any) any {
	if noArguments(args) {
		return nil
	}
	m, ok := args.(map[string]any)
	if !ok {
		return omittedMark("not_an_object", sizeOf(args))
	}
	out := make(map[string]any, len(m))
	for name, value := range m {
		out[cutText(name)] = omittedNote(name, value)
	}
	return out
}

// noArguments reports whether decoded arguments hold nothing.
func noArguments(args any) bool {
	m, ok := args.(map[string]any)
	return args == nil || (ok && len(m) == 0)
}

// encodeKept returns the JSON of v, or nil when v is nil. The result is at most
// limit bytes. When it would be more, only a mark with the size is left, because
// an entry must never be lost for its arguments.
func encodeKept(v any, limit int) json.RawMessage {
	if v == nil {
		return nil
	}
	out, err := encodeArguments(v)
	if err != nil {
		// Decoded JSON always encodes, so this does not happen.
		out, _ = encodeArguments(omittedMark("unreadable", 0))
	}
	if len(out) > limit {
		out, _ = encodeArguments(omittedMark("too_large", len(out)))
	}
	return out
}

// omittedMark is what stays of arguments that the log does not keep at all.
func omittedMark(reason string, size int) map[string]any {
	return map[string]any{"omitted": reason, "bytes": size}
}

// encodeArguments returns v as JSON. The text stays as it is written: the page
// that shows it escapes it.
func encodeArguments(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b.Bytes()), nil
}

// cleanValue returns a copy of a decoded JSON value without its message content.
func cleanValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for name, value := range t {
			if omittedArguments[name] {
				out[name] = omittedNote(name, value)
				continue
			}
			out[name] = cleanValue(value)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, value := range t {
			out[i] = cleanValue(value)
		}
		return out
	case string:
		return cutText(t)
	default:
		return v
	}
}

// omittedNote is what stays of the value of one argument that the log does not
// keep. A number under a name of omittedArguments is the size that stripOmitted
// left there.
func omittedNote(name string, v any) string {
	if size, ok := v.(json.Number); ok && omittedArguments[name] {
		return fmt.Sprintf("[omitted: %s bytes]", size)
	}
	return fmt.Sprintf("[omitted: %d bytes]", sizeOf(v))
}

// sizeOf returns the size of an omitted value: the bytes of a text, and the bytes
// of the JSON of anything else.
func sizeOf(v any) int {
	if s, ok := v.(string); ok {
		return len(s)
	}
	out, err := encodeArguments(v)
	if err != nil {
		return 0
	}
	return len(out)
}

// cutText returns s, or the first part of a long s with a mark that says how long
// it was. The cut falls at the end of a character.
func cutText(s string) string {
	if len(s) <= maxArgumentText {
		return s
	}
	cut := maxArgumentText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s[cut: %d bytes in all]", s[:cut], len(s))
}
