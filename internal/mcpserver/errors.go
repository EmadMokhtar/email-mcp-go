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
//     *service.PermissionError, and the link of a *service.ReauthError.
//   - An unknown error becomes "internal error". Only its Go type is logged.
//   - Nothing here logs a tool error text.
//   - A text ends in one of three sentences. "Do not retry." is for an error
//     that nothing the agent can do will fix. "Do not retry until the user has
//     done this." is for an error that the user can fix, for example on the page
//     of a link in the text. "Do not repeat the same call." is for an error where
//     the text gives a next step, so the agent does the next step and not the
//     same call again.
//   - The text of mail.ErrTooLarge depends on the tool: a tool that builds a
//     message talks about its attachments, a read tool about the body.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
)

// The three closing sentences of an error text (see the top of this file).
const (
	noRetry          = "Do not retry."
	noRetryUntilUser = "Do not retry until the user has done this."
	noRepeat         = "Do not repeat the same call."
)

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
	codeTimeout         = "timeout"
	codeCanceled        = "canceled"
	// The codes of the write tools.
	codeConfirmation        = "confirmation"
	codeConfirmationExpired = "confirmation_expired"
	codePayloadChanged      = "payload_changed"
	codeNotPending          = "not_pending"
	codeLimit               = "limit"
	codeMoveToTrash         = "move_to_trash"
	codeDraftFrom           = "draft_from_mismatch"
	codeBadMessage          = "bad_message"
	codeConflict            = "conflict"
	codeTooLate             = "too_late"
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
// the agent sees. tool is the name of the tool that failed. It never returns
// nil.
func toolError(tool string, err error) *toolFailure {
	var (
		ready  *toolFailure
		perm   *service.PermissionError
		rate   *policy.RateLimitError
		reauth *service.ReauthError
		arg    *argError
	)
	switch {
	case errors.As(err, &ready): // a handler made the failure itself
		return ready
	case errors.As(err, &perm):
		return permissionFailure(perm)
	case errors.As(err, &rate):
		return rateLimitFailure(rate)
	case errors.As(err, &reauth):
		return reauthFailure(reauth.Link)
	case errors.Is(err, service.ErrNeedsReauth): // an error without the typed link
		return reauthFailure("")
	case errors.Is(err, service.ErrNoPrincipal):
		return errNotAuthorized
	case errors.Is(err, service.ErrAccountNotFound):
		return failure(codeAccountNotFound,
			"There is no such account. Call list_accounts and use one of its account ids. "+noRepeat)
	case errors.As(err, &arg):
		return failure(codeBadArgument, fmt.Sprintf("The argument %q is not valid. %s %s", arg.arg, arg.hint, noRepeat))
	// A partial change can wrap the error that stopped it, so it comes before the
	// errors below.
	case errors.Is(err, mail.ErrPartial):
		return failure(codePartial, "The change was made in part or in full before an error stopped it, "+
			"so the refs may have changed. Search again to see the current state. "+noRepeat)
	case errors.Is(err, mail.ErrRefStale):
		return failure(codeStaleRef, "The message ref is out of date: the message may have been moved or deleted. "+
			"Search again to get a fresh ref. "+noRepeat)
	case errors.Is(err, mail.ErrNotFound):
		return failure(codeNotFound, "The message, folder or attachment was not found. "+
			"Check the ref, the folder name or the attachment id, and search again if the ref is old. "+noRepeat)
	case errors.Is(err, mail.ErrTooLarge):
		return tooLargeFailure(tool)
	case errors.Is(err, mail.ErrUnsupported):
		return failure(codeUnsupported, "This account or mail server does not support this operation. "+noRetry)
	case isMessageError(err):
		return messageFailure(err)
	// ErrNotADraft matches ErrInvalidRequest too, so it comes first.
	case errors.Is(err, service.ErrNotADraft):
		return failure(codeNotADraft, "The message is not a draft, so it is not sent. "+
			"Send a saved draft, or write a new message. "+noRepeat)
	case errors.Is(err, service.ErrInvalidRequest):
		return failure(codeInvalidRequest, "The request is not valid. Check the arguments against the description of the tool. "+noRepeat)
	case errors.Is(err, service.ErrSendNotFound):
		return failure(codeSendNotFound, "No send with this id for this account. Check the send id. "+noRepeat)
	case errors.Is(err, confirm.ErrStateInvalid), errors.Is(err, confirm.ErrNotFound):
		return failure(codeConfirmation, "The confirmation is not valid: its request state was changed, or it belongs to "+
			"another request. Nothing was done. Call the tool again to ask the user again. "+noRepeat)
	case errors.Is(err, confirm.ErrStateExpired):
		return failure(codeConfirmationExpired, "The confirmation has expired: the user must answer within 10 minutes. "+
			"Nothing was done. Call the tool again to ask the user again. "+noRepeat)
	case errors.Is(err, confirm.ErrPayloadChanged):
		return failure(codePayloadChanged, "The request is not the one that the user was asked about: the arguments "+
			"changed, or the message built again is different. Nothing was done. Call the tool again to ask the user again. "+noRepeat)
	case errors.Is(err, confirm.ErrNotPending):
		return failure(codeNotPending, "The action no longer waits for the user: it was carried out, declined or cancelled, "+
			"or it expired. Check a send with get_send_status. "+noRepeat)
	case errors.Is(err, service.ErrLimit):
		return failure(codeLimit, "A limit of this account does not allow this request as it is: too many recipients in "+
			"one message, more messages than the Trash limit of a day, or a limit of 0. Use fewer recipients or messages, "+
			"or ask the user to change the limits in the email-mcp settings. "+noRepeat)
	case errors.Is(err, service.ErrMoveToTrash):
		return failure(codeMoveToTrash, "move_messages cannot move mail to Trash. Use trash_messages, which asks the user "+
			"when the account wants it. "+noRepeat)
	case errors.Is(err, service.ErrDraftNotFromAccount):
		return failure(codeDraftFrom, "The draft is not from the address of this account, so it is not sent. "+
			"Send a draft that this account saved, or write a new message. "+noRepeat)
	case errors.Is(err, outbox.ErrTooLate):
		return failure(codeTooLate, "It is too late to cancel: the message is being sent, was sent, or failed. "+
			"Check it with get_send_status. "+noRepeat)
	case errors.Is(err, mail.ErrConflict):
		return failure(codeConflict, "The folder or tag already exists, or the change conflicts with the current state. "+
			"List the folders or tags to see the current state. "+noRepeat)
	case errors.Is(err, signing.ErrInvalid), errors.Is(err, signing.ErrExpired):
		return failure(codeBadToken, "The page token or cursor is not valid, or it has expired. "+
			"Start again without it, and pass a token or cursor back only as the tool gave it. "+noRepeat)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return failure(codeTimeout, "The mail server did not answer in time. Try again later.")
	case errors.Is(err, context.Canceled):
		return failure(codeCanceled, "The request was cancelled.")
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
		return failure(codePermission, reason+" The user can change the level at "+p.Link+". "+noRetryUntilUser)
	default:
		return failure(codePermission, reason+" The user can change the level in the email-mcp settings. "+noRetryUntilUser)
	}
}

// tooLargeFailure builds the text for mail.ErrTooLarge. A tool that builds a
// message gets it for its attachments or for a saved draft. A read tool gets it
// for an attachment or a body that is too large to return.
func tooLargeFailure(tool string) *toolFailure {
	if buildsMessage(tool) {
		return failure(codeTooLarge, "The message is too large: the attachments may be at most 10 MB together "+
			"(new and forwarded), and a saved draft at most 25 MB. Use smaller or fewer attachments. "+noRepeat)
	}
	return failure(codeTooLarge, "The content is too large to return. An attachment may be at most 10 MB. "+
		`For a message, ask for body_format "none", or read the body in parts with max_chars and offset. `+noRepeat)
}

// buildsMessage reports whether a tool builds a message from a Compose input.
func buildsMessage(tool string) bool {
	return tool == toolSendMessage || tool == toolCreateDraft
}

// messageErrors are the errors of mime.Build that mean the message cannot be
// built as the request asks, with the text for each. mime.ErrNoOriginal is not
// here: the service always gives the original, so it is an internal error.
var messageErrors = []struct {
	err  error
	text string
}{
	{mime.ErrInvalidAddress, "An address is not valid, or it cannot be sent as it is (for example a quoted name before " +
		"the @, or an IP address after it). Check to, cc and bcc."},
	{mime.ErrNoRecipients, "The message has no recipients. Give at least one address in to, cc or bcc."},
	{mime.ErrHeaderInjection, "A value that goes into a header (an address, the subject or an attachment file name) " +
		"has a line break or a NUL character. Remove it."},
	{mime.ErrReplyAndForward, "A message cannot be a reply and a forward. Give reply_to or forward_of, not both."},
	{mime.ErrInvalidContentType, "The content type of an attachment is not valid. Use one type such as " +
		"application/pdf: not multipart, and with no charset other than UTF-8."},
	{mime.ErrHTMLTooComplex, "The HTML body is too complex to make its text form. Give a plain text body too, or use simpler HTML."},
}

// isMessageError reports whether err is one of messageErrors.
func isMessageError(err error) bool {
	for _, m := range messageErrors {
		if errors.Is(err, m.err) {
			return true
		}
	}
	return false
}

// messageFailure builds the text for one of messageErrors.
func messageFailure(err error) *toolFailure {
	for _, m := range messageErrors {
		if errors.Is(err, m.err) {
			return failure(codeBadMessage, m.text+" "+noRepeat)
		}
	}
	return errInternal
}

// rateLimitFailure builds the text for a reached limit. The text of a
// *policy.RateLimitError is a fixed sentence with the kind of limit and the
// wait, and it names no account.
func rateLimitFailure(r *policy.RateLimitError) *toolFailure {
	return failure(codeRateLimit, r.Error())
}

// reauthFailure builds the text for an account that must be reconnected. link is
// the field of the *service.ReauthError, or empty. All text of the error itself
// is left out.
func reauthFailure(link string) *toolFailure {
	text := "The account needs to be reconnected. Ask the user to reconnect it in the email-mcp settings"
	if link != "" {
		text += " at " + link
	}
	return failure(codeNeedsReauth, text+". "+noRetryUntilUser)
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
