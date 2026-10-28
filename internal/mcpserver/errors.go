package mcpserver

// This file maps the errors of the lower layers to the text that the agent
// sees. Spec section 6.7: every problem is a tool result with isError set, and
// the text says what to do.
//
// The rules:
//   - An error is matched only with errors.Is and errors.As, never by its text.
//   - A known error gets a fixed text. The text of the error itself is not
//     passed on, because it can hold text from the mail server or an account
//     name. The only parts that are taken from a known error are fields that
//     the service filled for the agent: the reason and the link of a
//     *service.PermissionError, and the link of ErrNeedsReauth.
//   - An unknown error becomes "internal error". Only its Go type is logged.
//   - Nothing here logs a tool error text.

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
)

// noRetry ends the text of every error that the same call cannot get past.
const noRetry = "Do not retry."

// The codes of the failures. They are for the log and for tests. A code never
// holds text from an error.
const (
	codeInternal        = "internal_error"
	codePanic           = "panic"
	codeNotAuthorized   = "not_authorized"
	codePermission      = "permission"
	codeTokenScope      = "token_scope"
	codeRateLimit       = "rate_limit"
	codeNeedsReauth     = "needs_reauth"
	codeAccountNotFound = "account_not_found"
	codeBadArgument     = "bad_argument"
	codePartial         = "partial"
	codeStaleRef        = "stale_ref"
	codeNotFound        = "not_found"
	codeTooLarge        = "too_large"
	codeUnsupported     = "unsupported"
	codeNotADraft       = "not_a_draft"
	codeInvalidRequest  = "invalid_request"
	codeSendNotFound    = "send_not_found"
	codeBadToken        = "bad_token"
)

// toolFailure is the error that a handler wrapper gives to the SDK. The SDK
// puts its text in a tool result with isError set. It has no structured
// content, so a client cannot mistake a failure for an empty result.
type toolFailure struct {
	// text is what the agent sees.
	text string
	// code names the kind of failure, for the log.
	code string
}

func (f *toolFailure) Error() string { return f.text }

// failure returns a toolFailure.
func failure(code, text string) *toolFailure { return &toolFailure{text: text, code: code} }

// The failures that no error maps to.
var (
	errInternal      = failure(codeInternal, "internal error")
	errNotAuthorized = failure(codeNotAuthorized, "not authorized")
)

// argError means that an argument of a tool call is wrong in a way that the JSON
// schema cannot show, for example a time that is not RFC 3339. The handler
// creates it with fixed text, never with text from the caller.
type argError struct {
	arg  string // the name of the argument
	hint string // how to write it
}

func (e *argError) Error() string { return "argument " + e.arg + " is not valid" }

// toolError maps an error from the service or a mail backend to the failure that
// the agent sees. It never returns nil.
func toolError(err error) *toolFailure {
	var (
		ready *toolFailure
		perm  *service.PermissionError
		rate  *policy.RateLimitError
		arg   *argError
	)
	switch {
	case errors.As(err, &ready): // a handler made the failure itself
		return ready
	case errors.As(err, &perm):
		return permissionFailure(perm)
	case errors.As(err, &rate):
		return rateLimitFailure(rate)
	case errors.Is(err, service.ErrNeedsReauth):
		return reauthFailure(err)
	case errors.Is(err, service.ErrAccountNotFound):
		return failure(codeAccountNotFound,
			"There is no such account. Call list_accounts and use one of its account ids. "+noRetry)
	case errors.As(err, &arg):
		return failure(codeBadArgument, fmt.Sprintf("The argument %q is not valid. %s %s", arg.arg, arg.hint, noRetry))
	// A partial change can wrap the error that stopped it, so it comes before the
	// errors below.
	case errors.Is(err, mail.ErrPartial):
		return failure(codePartial, "The change was made in part or in full before an error stopped it, "+
			"so the refs may have changed. Search again to see the current state. "+noRetry)
	case errors.Is(err, mail.ErrRefStale):
		return failure(codeStaleRef, "The message ref is out of date: the message may have been moved or deleted. "+
			"Search again to get a fresh ref. "+noRetry)
	case errors.Is(err, mail.ErrNotFound):
		return failure(codeNotFound, "The message, folder or attachment was not found. "+
			"Check the ref, the folder name or the attachment id, and search again if the ref is old. "+noRetry)
	case errors.Is(err, mail.ErrTooLarge):
		return failure(codeTooLarge, "The content is too large to return (an attachment may be at most 10 MB). "+noRetry)
	case errors.Is(err, mail.ErrUnsupported):
		return failure(codeUnsupported, "This account or mail server does not support this operation. "+noRetry)
	// ErrNotADraft matches ErrInvalidRequest too, so it comes first.
	case errors.Is(err, service.ErrNotADraft):
		return failure(codeNotADraft, "The message is not a draft, so it is not sent. "+
			"Send a saved draft, or write a new message. "+noRetry)
	case errors.Is(err, service.ErrInvalidRequest):
		return failure(codeInvalidRequest, "The request is not valid. Check the arguments against the description of the tool. "+noRetry)
	case errors.Is(err, service.ErrSendNotFound):
		return failure(codeSendNotFound, "No send with this id for this account. Check the send id. "+noRetry)
	case errors.Is(err, signing.ErrInvalid), errors.Is(err, signing.ErrExpired):
		return failure(codeBadToken, "The page token or cursor is not valid, or it has expired. "+
			"Start again without it, and pass a token or cursor back only as the tool gave it. "+noRetry)
	default:
		return errInternal
	}
}

// permissionFailure builds the text for a refused permission. The reason is a
// sentence that the policy wrote for the agent. It names the account and the
// level that the action needs.
func permissionFailure(p *service.PermissionError) *toolFailure {
	reason := strings.TrimSpace(mime.CleanUntrusted(p.Decision.Reason))
	if reason == "" {
		reason = "This action is not allowed."
	}
	switch {
	case p.TokenLimited:
		return failure(codeTokenScope, reason+" The access token does not allow this action. "+noRetry)
	case p.Link != "":
		return failure(codePermission, reason+" The user can change the level at "+p.Link+". "+noRetry)
	default:
		return failure(codePermission, reason+" The user can change the level in the email-mcp settings. "+noRetry)
	}
}

// rateLimitFailure builds the text for a reached limit. The text of a
// *policy.RateLimitError is a fixed sentence with the kind of limit and the
// wait, and it names no account.
func rateLimitFailure(r *policy.RateLimitError) *toolFailure {
	return failure(codeRateLimit, r.Error())
}

// reauthLinkMarker comes just before the link in the text of the error that the
// service makes for an account that must be reconnected.
const reauthLinkMarker = "reconnect it here: "

// reauthFailure builds the text for an account that must be reconnected. The
// service puts the link in the text of the error and has no field for it, so
// the link is read from the end of the text. It is used only if it is one
// http or https address without spaces or a user name. All other text of the
// error is left out.
func reauthFailure(err error) *toolFailure {
	text := "The account needs to be reconnected. Ask the user to reconnect it in the email-mcp settings"
	if link := reauthLink(err); link != "" {
		text += " at " + link
	}
	return failure(codeNeedsReauth, text+". "+noRetry)
}

// reauthLink returns the settings link in the error, or an empty string.
func reauthLink(err error) string {
	msg := err.Error()
	i := strings.LastIndex(msg, reauthLinkMarker)
	if i < 0 {
		return ""
	}
	link := msg[i+len(reauthLinkMarker):]
	if link == "" || strings.ContainsAny(link, " \t\r\n") {
		return ""
	}
	u, parseErr := url.Parse(link)
	if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return link
}

// errorTypes returns the Go types of an error and of the errors it wraps, for
// example "*fmt.wrapError>*errors.errorString". It is for the log. A type name
// holds no text from the error itself.
func errorTypes(err error) string {
	const maxDepth = 6
	var types []string
	for i := 0; err != nil && i < maxDepth; i++ {
		types = append(types, fmt.Sprintf("%T", err))
		err = errors.Unwrap(err)
	}
	return strings.Join(types, ">")
}
