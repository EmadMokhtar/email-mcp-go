package mcpserver

// This file holds the confirmation of a person (spec section 7.5), in band with
// MCP's multi-round-trip requests (MRTR):
//
//  1. The first call of send_message or trash_messages returns no result.
//     It returns an input request "confirm" (a form elicitation with one boolean
//     field) and the RequestState of the service.
//  2. The client shows the form to the person, and calls the same tool again
//     with the answer in InputResponses["confirm"] and the same RequestState.
//  3. The answer is a yes only when it is an elicitation result with the action
//     "accept" and the field confirm set to the JSON boolean true. Every other
//     answer is a no. Nothing in the arguments of a tool can say yes.
//
// The service checks the RequestState (signature, expiry, tenant, account,
// kind and the hash of the request) and acts once. This layer only turns the
// MCP request into a service.ConfirmAnswer and the service answer into an MCP
// result.
//
// A client that cannot show a form gets the fallback of the service:
// "pending_approval" and a link to the approval page.
//
// The links of request_permission_change and connect_account can go the same
// way, as a URL elicitation, when the client can open a link for the person.

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

const (
	// mrtrProtocolVersion is the first protocol version with input requests in a
	// result. Protocol versions are dates, so they compare as strings.
	mrtrProtocolVersion = "2026-07-28"
	// confirmKey is the id of the input request that asks for a confirmation.
	confirmKey = "confirm"
	// confirmField is the boolean field of the confirmation form.
	confirmField = "confirm"
	// linkKey is the id of the input request that opens a link for the user.
	linkKey = "open_link"
	// The modes of an elicitation.
	modeForm = "form"
	modeURL  = "url"
	// actionAccept is the action of an elicitation answer that the person
	// submitted.
	actionAccept = "accept"
	// previewBodyChars is the most characters of the body in a preview.
	previewBodyChars = 500
)

// errBadAnswer is the failure for a second call that has only one of the two
// parts of an answer: the answer and the request state.
var errBadAnswer = failure(codeConfirmation, `The confirmation answer is incomplete: it needs both the request state `+
	`and the input response "confirm". Nothing was done. Call the tool again without them to ask the user. `+noRepeat)

// canElicitInBand reports whether the client can show the confirmation form to
// the person in this call. It needs:
//   - form elicitation in the capabilities of the client. An elicitation
//     capability with neither form nor url counts as form, as the protocol says
//     for older clients;
//   - and a way to ask: the protocol version 2026-07-28 or later (input
//     requests in the result), or opts.InBandLegacyElicitation (stdio, where the
//     SDK asks an older client with elicitation/create in the same session).
func canElicitInBand(req *mcp.CallToolRequest, opts Options) bool {
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	e := caps.Elicitation
	if e.Form == nil && e.URL != nil {
		return false
	}
	return canAsk(req, opts)
}

// canOpenLink reports whether the client can open a link for the person in this
// call (URL elicitation), with the same rule for the protocol as
// canElicitInBand.
func canOpenLink(req *mcp.CallToolRequest, opts Options) bool {
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil || caps.Elicitation.URL == nil {
		return false
	}
	return canAsk(req, opts)
}

// canAsk reports whether the server can ask the client for input in this call.
func canAsk(req *mcp.CallToolRequest, opts Options) bool {
	return req.ProtocolVersion() >= mrtrProtocolVersion || opts.InBandLegacyElicitation
}

// confirmMode returns the confirmation mode of a first call.
func confirmMode(req *mcp.CallToolRequest, opts Options) service.ConfirmMode {
	if canElicitInBand(req, opts) {
		return service.ConfirmInBand
	}
	return service.ConfirmFallback
}

// confirmAnswer reads the answer of a second call. It returns nil for a first
// call: no input responses and no request state. A call with only one of the
// two is refused with errBadAnswer, and nothing acts.
//
// Accepted is true only for an elicitation result with the action "accept" and
// the field confirm set to the JSON boolean true (see accepted).
func confirmAnswer(req *mcp.CallToolRequest) (*service.ConfirmAnswer, error) {
	p := req.Params
	answer, hasAnswer := p.InputResponses[confirmKey]
	hasState := p.RequestState != ""
	switch {
	case len(p.InputResponses) == 0 && !hasState:
		return nil, nil
	case !hasAnswer || !hasState:
		return nil, errBadAnswer
	}
	return &service.ConfirmAnswer{RequestState: p.RequestState, Accepted: accepted(answer)}, nil
}

// accepted reports whether an answer is a yes. "decline", "cancel", an action in
// another case, a missing field, and a field that is not the boolean true (the
// string "true" too) are all a no.
func accepted(answer mcp.InputResponse) bool {
	r, ok := answer.(*mcp.ElicitResult)
	if !ok || r == nil || r.Action != actionAccept {
		return false
	}
	yes, ok := r.Content[confirmField].(bool)
	return ok && yes
}

// confirmSchema is the form of a confirmation: one boolean field. Its default is
// false, so a form that is sent without a value is a no.
func confirmSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			confirmField: {
				Type:        "boolean",
				Title:       "Confirm",
				Description: "true to go ahead. Anything else does nothing.",
				Default:     json.RawMessage("false"),
			},
		},
		Required: []string{confirmField},
	}
}

// confirmationRequest is the result of a first call that needs the person: the
// form and the request state, with no content and no structured output (the SDK
// refuses a result that has both input requests and content).
func confirmationRequest(nc *service.NeedsConfirmation, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{confirmKey: &mcp.ElicitParams{
			Mode:            modeForm,
			Message:         message,
			RequestedSchema: confirmSchema(),
		}},
		RequestState: nc.RequestState,
	}
}

// linkRequest is the result of a first call that opens a link for the person
// with a URL elicitation. It has no request state: the second call returns the
// same text as a call without elicitation, whatever the answer.
func linkRequest(message, link string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{linkKey: &mcp.ElicitParams{
			Mode:          modeURL,
			Message:       message,
			URL:           link,
			ElicitationID: rand.Text(),
		}},
	}
}

// sendPreview is the text of the form for a send. Every value comes from the
// message, so each one is cleaned of characters that hide or reorder text. The
// one-line values also lose their line breaks, so a subject cannot add a line
// that looks like another recipient.
//
//	Send this email from <from>?
//	To: <to, comma-separated>
//	Subject: <subject>
//	<first 500 chars of body>
func sendPreview(p confirm.Preview) string {
	to := make([]string, 0, len(p.To))
	for _, addr := range p.To {
		to = append(to, oneLine(addr))
	}
	body := firstChars(clean(p.BodyStart), previewBodyChars)
	return fmt.Sprintf("Send this email from %s?\nTo: %s\nSubject: %s\n%s",
		oneLine(p.From), strings.Join(to, ", "), oneLine(p.Subject), body)
}

// trashPreview is the text of the form for a move to Trash.
func trashPreview(p confirm.Preview) string {
	return fmt.Sprintf("Move %d messages in %s to Trash?", p.Count, oneLine(p.Account))
}

// oneLine cleans s and puts it on one line: each line break, tab, line separator
// and paragraph separator becomes a space, and the spaces at the ends go.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t', '\u2028', '\u2029':
			return ' '
		}
		return r
	}, clean(s))
	return strings.TrimSpace(s)
}

// firstChars returns the first n characters of s, or s if it is not longer.
func firstChars(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
