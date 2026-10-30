package service

// This file holds the write use cases that do not send mail (flags, tags,
// folders, moves, drafts and Trash), the approvals of the web page, and what
// every write shares: the confirmation types, the limits, and the audit entry.
// Sending is in send.go.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// The names of the tools, as the audit log records them. The MCP layer gives
// its tools the same names.
const (
	toolSetFlags     = "set_flags"
	toolCreateFolder = "create_folder"
	toolCreateTag    = "create_tag"
	toolTag          = "tag_messages"
	toolMove         = "move_messages"
	toolCreateDraft  = "create_draft"
	toolDiscardDraft = "discard_draft"
	toolSend         = "send_message"
	toolCancelSend   = "cancel_send"
	toolTrash        = "trash_messages"
	// The approvals page uses these names only for an entry whose action could
	// not be found. Otherwise the entry has the tool of the action.
	toolApprovePending = "approve_pending"
	toolRejectPending  = "reject_pending"
)

// The audit codes that say how an action that was done got the go-ahead.
const (
	detailNoConfirmation = "no_confirmation"
	detailInBand         = "confirmed_in_band"
	detailOnWeb          = "approved_on_web"
)

// The results of a Trash call.
const (
	TrashDone            = "done"
	TrashPendingApproval = "pending_approval"
	TrashDeclined        = "declined"
)

// endTimeout is the time that the end of a pending action and the record of the
// known recipients get, when they run after the caller's context has ended.
const endTimeout = 30 * time.Second

// ConfirmMode says how the user confirms an action that needs a confirmation.
type ConfirmMode int

const (
	// ConfirmInBand means that the client can ask the user itself, in a channel
	// that the agent cannot answer (an elicitation). The first call returns a
	// NeedsConfirmation, and the client calls again with the answer.
	ConfirmInBand ConfirmMode = iota
	// ConfirmFallback means that the client cannot ask the user. The first call
	// returns "pending_approval" and a link to the web page where the user
	// approves or rejects the action.
	ConfirmFallback
)

// ConfirmAnswer is the user's answer that the client passes on in its second
// call. A nil *ConfirmAnswer means that the call is a first call.
type ConfirmAnswer struct {
	// RequestState is the state that the first call returned.
	RequestState string
	// Accepted must be true only when the user accepted in a channel that the
	// agent cannot answer, such as the form of an elicitation. The service acts
	// on it: this flag is the consent.
	Accepted bool
}

// NeedsConfirmation is the answer to a first call when the user must confirm in
// band. The client shows the preview of Pending to the user and calls again
// with RequestState and the answer.
type NeedsConfirmation struct {
	Pending      *confirm.Pending
	RequestState string
}

// TrashResult is the result of a Trash call.
type TrashResult struct {
	// Status is "done", "pending_approval" or "declined".
	Status string
	// Count is the number of messages in the request. A ref that is twice in the
	// request counts once.
	Count int
	// ApprovalLink is the page where the user approves the action. It is set for
	// "pending_approval" when there is a link. It never holds a token.
	ApprovalLink string
}

// The errors of the write use cases. Callers check them with errors.Is.
var (
	// ErrInvalidRequest means that the request itself is wrong, for example a
	// send with both a message and a draft. The text says what is wrong.
	ErrInvalidRequest = errors.New("service: the request is not valid")

	// ErrLimit means that a limit of the account can never allow the request as
	// it is: the limit is 0, or the request asks for more than the limit at
	// once. Waiting does not help. A full window gives a *policy.RateLimitError
	// instead, which says how long to wait.
	ErrLimit = errors.New("service: a limit of the account does not allow this")

	// ErrMoveToTrash means that a move would put mail into the Trash folder or
	// into a folder inside it, or that create_folder would make a folder inside
	// Trash. A move is organizing, and it would skip the Trash confirmation and
	// the Trash limit, so it is refused at every level. A folder inside Trash is
	// Trash too: mail clients and server rules empty Trash with its sub-folders.
	ErrMoveToTrash = errors.New("service: move_messages cannot move mail to the Trash folder or a folder inside it. " +
		"Use trash_messages, which asks the user when the account wants it and counts toward the Trash limit")

	// ErrAnswerInBand means that the action waits for the answer of the user in
	// the chat where the agent asked, so the web page cannot approve or reject
	// it. Only one channel may answer an action, so one message cannot be
	// approved twice. It matches confirm.ErrNotPending.
	ErrAnswerInBand = fmt.Errorf("%w: the user answers it in the chat where it was asked, not on the web page",
		confirm.ErrNotPending)

	// errNoAudit means that the service has no audit log. A write must not run
	// without its audit entry.
	errNoAudit = errors.New("service: the audit log is not set up, so no write is allowed")
)

// maxRecipients is the most recipients of one message, whatever the limit of
// the account says. The audit log keeps at most 200 recipients in one entry, and
// every send must be in the log with all its recipients.
const maxRecipients = 200

// limitError is the error for a request that the limits of the account can
// never allow as it is. It matches ErrLimit.
type limitError struct {
	kind  policy.CounterKind
	limit int // the limit that applies
	count int // what the request asks for
}

func (e *limitError) Is(target error) bool { return target == ErrLimit }

func (e *limitError) Error() string {
	switch {
	case e.kind == policy.CountRecipients:
		return fmt.Sprintf("service: the message has %d recipients, and this account allows at most %d in one message. "+
			"Use fewer recipients, or ask the user to raise the limit in the email-mcp settings", e.count, e.limit)
	case e.limit <= 0 && e.kind == policy.CountSend:
		return "service: sending is turned off for this account, because its send limit is 0. " +
			"The user can change the limits in the email-mcp settings"
	case e.limit <= 0:
		return "service: moving mail to Trash is turned off for this account, because its Trash limit is 0. " +
			"The user can change the limits in the email-mcp settings"
	default:
		return fmt.Sprintf("service: %d messages are more than this account may move to Trash in one day (%d). "+
			"Use fewer messages in one call", e.count, e.limit)
	}
}

// code returns the audit code of the error.
func (e *limitError) code() string {
	switch {
	case e.kind == policy.CountRecipients:
		return "too_many_recipients"
	case e.limit <= 0:
		return "limit_disabled"
	default:
		return "too_many_at_once"
	}
}

// checkLimit refuses n uses of kind when the limits can never allow them. It
// runs before the user is asked, and it counts nothing. The limiter counts at
// the moment of the action (see policy.Limiter.Reserve), so an action that is
// declined or refused uses no quota.
func checkLimit(kind policy.CounterKind, lim account.Limits, n int) error {
	var limit int
	switch kind {
	case policy.CountRecipients:
		limit = min(lim.RecipientsPerMessage, maxRecipients)
	case policy.CountSend:
		limit = min(lim.SendsPerHour, lim.SendsPerDay)
	case policy.CountTrash:
		limit = lim.TrashPerDay
	default:
		return fmt.Errorf("service: unknown limit %q", kind)
	}
	if n > limit {
		return &limitError{kind: kind, limit: limit, count: n}
	}
	return nil
}

// checkMode refuses a confirmation mode that the service does not know.
func checkMode(mode ConfirmMode) error {
	if mode != ConfirmInBand && mode != ConfirmFallback {
		return fmt.Errorf("%w: unknown confirmation mode", ErrInvalidRequest)
	}
	return nil
}

// ---------------------------------------------------------------- audit

// entry is what a write use case records in the audit log.
type entry struct {
	tool       string
	account    account.AccountID // empty for an entry that is not about one account
	count      int
	recipients []string
	outcome    audit.Outcome
	detail     string
}

// record writes an audit entry for the principal.
//
// A failure does not change the answer of the use case: the action has happened
// or has been refused, and the agent must learn that. The failure is not
// ignored either. It is logged with a fixed code, the tool and the outcome, and
// nothing else, because the error could hold a value of the entry.
func (s *Service) record(ctx context.Context, p Principal, e entry) {
	ae := audit.Entry{
		Tenant:     p.Tenant,
		Account:    e.account,
		Client:     p.Client,
		Tool:       e.tool,
		Count:      e.count,
		Recipients: e.recipients,
		Outcome:    e.outcome,
		Detail:     e.detail,
	}
	if e.account != "" {
		ae.Area = account.AreaMail
	}
	if err := s.d.Audit.Record(ctx, ae); err != nil {
		code := "audit_write_failed"
		if errors.Is(err, audit.ErrInvalidEntry) {
			code = "audit_entry_invalid"
		}
		s.d.Logger.Error("service: an audit entry was not written", "code", code, "tool", e.tool, "outcome", string(e.outcome))
	}
}

// recordOutcome records a call on the account of acc with a given outcome.
func (s *Service) recordOutcome(ctx context.Context, acc access, tool string, count int, recipients []string, outcome audit.Outcome, detail string) {
	s.record(ctx, acc.Principal, entry{
		tool: tool, account: acc.Account.ID, count: count, recipients: recipients, outcome: outcome, detail: detail,
	})
}

// finish records the end of a call on the account of acc: "ok" with detail when
// err is nil, and the outcome and the code of err otherwise. An error that says
// that the mail server refused the stored login marks the account for a
// reconnect (see noteBackendError).
func (s *Service) finish(ctx context.Context, acc access, tool string, count int, recipients []string, detail string, err error) {
	outcome := audit.OutcomeOK
	if err != nil {
		outcome, detail = auditCode(err)
		s.noteBackendError(ctx, acc.Account, err)
	}
	s.recordOutcome(ctx, acc, tool, count, recipients, outcome, detail)
}

// recordRefusal records a call that authorize refused. A call without a
// principal has no tenant, so it cannot be recorded. The entry names the account
// only when the account exists in the tenant of the caller.
func (s *Service) recordRefusal(ctx context.Context, id account.AccountID, tool string, err error) {
	p, perr := PrincipalFrom(ctx)
	if perr != nil {
		return
	}
	e := entry{tool: tool}
	e.outcome, e.detail = auditCode(err)
	var perm *PermissionError
	if errors.As(err, &perm) || errors.Is(err, ErrNeedsReauth) {
		e.account = id
	}
	s.record(ctx, p, e)
}

// auditCode returns the outcome and the code of an error for the audit log. A
// refusal by the server is "refused". A failure is "error".
func auditCode(err error) (audit.Outcome, string) {
	var (
		perm *PermissionError
		rate *policy.RateLimitError
		lim  *limitError
	)
	refused := func(code string) (audit.Outcome, string) { return audit.OutcomeRefused, code }
	failed := func(code string) (audit.Outcome, string) { return audit.OutcomeError, code }
	switch {
	case errors.Is(err, ErrStopping):
		return refused("stopping")
	case errors.As(err, &perm):
		if perm.TokenLimited {
			return refused("token_scope")
		}
		return refused("level_too_low")
	case errors.Is(err, ErrNeedsReauth):
		return refused("needs_reauth")
	case errors.Is(err, ErrAccountNotFound):
		return refused("account_not_found")
	case errors.As(err, &rate):
		return refused("rate_limited")
	case errors.As(err, &lim):
		return refused(lim.code())
	case errors.Is(err, ErrMoveToTrash):
		return refused("move_to_trash")
	case errors.Is(err, ErrDraftNotFromAccount):
		return refused("draft_from_mismatch")
	case errors.Is(err, ErrSendNotFound):
		return refused("send_not_found")
	// ErrHiddenAddress also matches ErrInvalidRequest, so it comes first.
	case errors.Is(err, ErrHiddenAddress):
		return refused("hidden_address")
	case errors.Is(err, ErrInvalidRequest), isMessageError(err):
		return refused("invalid_request")
	case errors.Is(err, confirm.ErrPayloadChanged):
		return refused("payload_changed")
	case errors.Is(err, confirm.ErrStateInvalid):
		return refused("state_invalid")
	case errors.Is(err, confirm.ErrStateExpired):
		return refused("state_expired")
	// ErrAnswerInBand also matches ErrNotPending, so it comes first.
	case errors.Is(err, ErrAnswerInBand):
		return refused("in_band_only")
	case errors.Is(err, confirm.ErrNotPending):
		return refused("not_pending")
	case errors.Is(err, confirm.ErrNotFound):
		return refused("not_found")
	// A partial change can also wrap one of the errors below, so it comes first.
	case errors.Is(err, mail.ErrPartial):
		return failed("partial")
	case errors.Is(err, outbox.ErrTooLate):
		return failed("too_late")
	case errors.Is(err, mail.ErrNotFound):
		return failed("not_found")
	case errors.Is(err, mail.ErrRefStale):
		return failed("stale_ref")
	case errors.Is(err, mail.ErrConflict):
		return failed("conflict")
	case errors.Is(err, mail.ErrTooLarge):
		return failed("too_large")
	case errors.Is(err, mail.ErrUnsupported):
		return failed("unsupported")
	default:
		return failed("failed")
	}
}

// isMessageError reports whether err says that the message cannot be built as
// the request asks.
func isMessageError(err error) bool {
	for _, target := range []error{
		mime.ErrInvalidAddress, mime.ErrNoRecipients, mime.ErrHeaderInjection, mime.ErrNoOriginal,
		mime.ErrReplyAndForward, mime.ErrInvalidContentType, mime.ErrHTMLTooComplex, mime.ErrInvalidFilename,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// agentError adds advice for the agent to an error after which the change was
// made in part or in full. Other errors are returned as they are.
func agentError(err error) error {
	if errors.Is(err, mail.ErrPartial) {
		return fmt.Errorf("%w. Some or all of the change was made before the error, so the refs may have changed. "+
			"Search again to see the current state, and do not repeat the same call", err)
	}
	return err
}

// ---------------------------------------------------------------- the start of a write

// authorizeWrite is authorize for a write use case. It refuses every write when
// the service has no audit log, and it records a refusal.
func (s *Service) authorizeWrite(ctx context.Context, id account.AccountID, act policy.Action, tool string) (access, error) {
	if s.d.Audit == nil {
		return access{}, errNoAudit
	}
	acc, err := s.authorize(ctx, id, act)
	if err != nil {
		s.recordRefusal(ctx, id, tool, err)
		return access{}, err
	}
	return acc, nil
}

// begin is authorizeWrite, and then it returns the mail backend of the account.
func (s *Service) begin(ctx context.Context, id account.AccountID, act policy.Action, tool string) (access, mail.Backend, error) {
	acc, err := s.authorizeWrite(ctx, id, act, tool)
	if err != nil {
		return access{}, nil, err
	}
	b, err := s.d.Backends.Mail(ctx, acc.Account)
	if err != nil {
		s.finish(ctx, acc, tool, 0, nil, "", err)
		return access{}, nil, err
	}
	return acc, b, nil
}

// confirmDeps checks that the parts of an action with confirmation are set up.
// A missing part is a mistake in the code that sets the program up. The action
// must not run without its limit, its confirmation or its audit entry.
func (s *Service) confirmDeps() error {
	if s.d.Audit == nil || s.d.Limiter == nil || s.d.Confirm == nil {
		return errors.New("service: the audit log, the limiter and the confirm manager must be set up for this action")
	}
	return nil
}

// ---------------------------------------------------------------- organize

// SetFlags changes the seen and flagged state of messages. It needs Mail 2. A
// mail.FlagChange can only say "seen" and "flagged", so no flag that removes
// mail (such as \Deleted) can be set.
func (s *Service) SetFlags(ctx context.Context, id account.AccountID, refs []mail.Ref, change mail.FlagChange) error {
	acc, b, err := s.begin(ctx, id, policy.ActOrganize, toolSetFlags)
	if err != nil {
		return err
	}
	err = b.SetFlags(ctx, refs, change)
	s.finish(ctx, acc, toolSetFlags, len(refs), nil, "", err)
	return agentError(err)
}

// CreateFolder creates a folder. It needs Mail 2.
//
// A parent that is a Trash folder, or a folder inside one, is refused at every
// level with ErrMoveToTrash (see Move): the new folder would be a place where
// move_messages could put mail into Trash.
func (s *Service) CreateFolder(ctx context.Context, id account.AccountID, name, parent string) (mail.Folder, error) {
	acc, b, err := s.begin(ctx, id, policy.ActOrganize, toolCreateFolder)
	if err != nil {
		return mail.Folder{}, err
	}
	if parent != "" {
		if err := refuseTrashTarget(ctx, b, parent); err != nil {
			s.finish(ctx, acc, toolCreateFolder, 1, nil, "", err)
			return mail.Folder{}, err
		}
	}
	folder, err := b.CreateFolder(ctx, name, parent)
	s.finish(ctx, acc, toolCreateFolder, 1, nil, "", err)
	return folder, err
}

// CreateTag creates a tag. It needs Mail 2.
func (s *Service) CreateTag(ctx context.Context, id account.AccountID, name string) (mail.Tag, error) {
	acc, b, err := s.begin(ctx, id, policy.ActOrganize, toolCreateTag)
	if err != nil {
		return mail.Tag{}, err
	}
	tag, err := b.CreateTag(ctx, name)
	s.finish(ctx, acc, toolCreateTag, 1, nil, "", err)
	return tag, err
}

// Tag adds and removes tags on messages. It needs Mail 2. The backend refuses a
// name that is not a tag, such as a system flag.
func (s *Service) Tag(ctx context.Context, id account.AccountID, refs []mail.Ref, add, remove []string) error {
	acc, b, err := s.begin(ctx, id, policy.ActOrganize, toolTag)
	if err != nil {
		return err
	}
	err = b.Tag(ctx, refs, add, remove)
	s.finish(ctx, acc, toolTag, len(refs), nil, "", err)
	return agentError(err)
}

// Move moves messages to a folder and returns their new refs. It needs Mail 2.
//
// A move to the Trash folder, or to a folder inside it, is refused at every
// level with ErrMoveToTrash: the agent must use Trash, which asks the user when
// the account wants it and counts toward the Trash limit. The Trash folder is
// the one that the backend's own Trash uses (the folder with the alias
// "@trash"), every other folder that the backend marks as Trash, and every
// folder with a well-known Trash name such as "Trash" or "Deleted Items", for a
// server that marks no folder. The target is resolved the way the backend
// resolves it, and its name is compared without regard to case, because some
// servers do not care about case (see inTrash).
func (s *Service) Move(ctx context.Context, id account.AccountID, refs []mail.Ref, folder string) ([]mail.Ref, error) {
	acc, b, err := s.begin(ctx, id, policy.ActOrganize, toolMove)
	if err != nil {
		return nil, err
	}
	if err := refuseTrashTarget(ctx, b, folder); err != nil {
		s.finish(ctx, acc, toolMove, len(refs), nil, "", err)
		return nil, err
	}
	moved, err := b.Move(ctx, refs, folder)
	s.finish(ctx, acc, toolMove, len(refs), nil, "", err)
	return moved, agentError(err)
}

// refuseTrashTarget returns ErrMoveToTrash when folder is a Trash folder of the
// account or a folder inside one.
func refuseTrashTarget(ctx context.Context, b mail.Backend, folder string) error {
	if folder == mail.AliasTrash {
		return ErrMoveToTrash
	}
	folders, err := b.ListFolders(ctx)
	if err != nil {
		return err
	}
	target, err := resolveFolder(folders, folder)
	if err != nil {
		return err
	}
	if inTrash(folders, target) {
		return ErrMoveToTrash
	}
	return nil
}

// inTrash reports whether the folder name is a Trash folder of the list or a
// folder inside one.
//
// Every folder that holds deleted mail counts, not only the one with the alias.
// A folder inside a Trash folder has a name that starts with the name of that
// folder and its hierarchy delimiter, such as "Trash/Old" for "Trash" and "/".
// A server with no hierarchy (delimiter 0) has no folder inside another.
//
// A folder with a well-known Trash name counts too, whatever its flags say (see
// hasTrashName). A server without SPECIAL-USE flags no folder as Trash, but the
// user's Trash is still there, under a name such as "Trash" or "Deleted Items".
// A move there would skip the Trash confirmation and the Trash limit. A normal
// folder with such a name is refused too, which is the safe side.
//
// Case does not count, in the whole name and in the prefix. IMAP says that only
// INBOX ignores case, and other names are exact. But some servers ignore case
// in every name, and policy.IsTrashTarget compares exactly, so here the safe
// side is to refuse more: a name that differs from Trash only in case is
// refused too.
func inTrash(folders []mail.Folder, name string) bool {
	for _, f := range folders {
		if !f.Trash && f.Alias != mail.AliasTrash {
			continue
		}
		if strings.EqualFold(name, f.Name) {
			return true
		}
		if f.Delimiter != 0 && hasPrefixFold(name, f.Name+string(f.Delimiter)) {
			return true
		}
	}
	return hasTrashName(folders, name)
}

// trashNames are the names that mail servers and clients commonly give to the
// Trash folder. They are compared without regard to case.
var trashNames = []string{"Trash", "Deleted", "Deleted Items", "Deleted Messages", "Bin", "Rubbish"}

// isTrashName reports whether name is one of trashNames, without regard to
// case. The whole name must match: "Trash Talk" and "Binders" do not.
func isTrashName(name string) bool {
	for _, t := range trashNames {
		if strings.EqualFold(name, t) {
			return true
		}
	}
	return false
}

// hasTrashName reports whether the folder name, or its first level, is a
// well-known Trash name (see trashNames).
//
// The first level is the first part of the name split by the hierarchy
// delimiter, after one leading INBOX part is removed. So "Trash", "Trash/Old",
// "INBOX.Trash" and "INBOX.Trash.Old" all count. The delimiter is the one that
// the list gives for the folder. A name that is not in the list (a new name)
// uses the delimiter of INBOX. The bare name is always checked too, so a server
// with no hierarchy is covered.
func hasTrashName(folders []mail.Folder, name string) bool {
	if isTrashName(name) {
		return true
	}
	delim, ok := folderDelimiter(folders, name)
	if !ok {
		delim, _ = folderDelimiter(folders, "INBOX")
	}
	if delim == 0 {
		return false
	}
	sep := string(delim)
	if rest, ok := cutPrefixFold(name, "INBOX"+sep); ok {
		name = rest
	}
	first, _, _ := strings.Cut(name, sep)
	return isTrashName(first)
}

// folderDelimiter returns the hierarchy delimiter of the folder with the name in
// the list. An exact match comes first, then a match without regard to case.
// ok is false when no folder in the list has the name.
func folderDelimiter(folders []mail.Folder, name string) (delim rune, ok bool) {
	for _, f := range folders {
		if f.Name == name {
			return f.Delimiter, true
		}
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			return f.Delimiter, true
		}
	}
	return 0, false
}

// hasPrefixFold reports whether s starts with prefix, without regard to case.
// It compares character by character, as strings.EqualFold does.
func hasPrefixFold(s, prefix string) bool {
	_, ok := cutPrefixFold(s, prefix)
	return ok
}

// cutPrefixFold returns s without prefix, and true, when s starts with prefix
// without regard to case. It compares character by character, as
// strings.EqualFold does, so the part it removes can have another length in
// bytes than prefix.
func cutPrefixFold(s, prefix string) (string, bool) {
	for _, p := range prefix {
		r, size := utf8.DecodeRuneInString(s)
		if size == 0 || !strings.EqualFold(string(r), string(p)) {
			return "", false
		}
		s = s[size:]
	}
	return s, true
}

// resolveFolder returns the name of the folder that nameOrAlias means, the way
// the backend resolves it: INBOX for "", "@inbox" and INBOX in any case, the
// folder with the alias for another alias, and the name itself otherwise.
//
// An alias that no folder in the list has gives an error that wraps
// mail.ErrNotFound. It is not passed on to the backend, because the backend
// could find a folder that the list shows under another alias, for example a
// folder that is both Junk and Trash (the list shows it as Trash).
func resolveFolder(folders []mail.Folder, nameOrAlias string) (string, error) {
	switch {
	case nameOrAlias == "" || nameOrAlias == mail.AliasInbox || strings.EqualFold(nameOrAlias, "INBOX"):
		return "INBOX", nil
	case mail.IsAlias(nameOrAlias):
		if name := aliasFolder(folders, nameOrAlias); name != "" {
			return name, nil
		}
		return "", fmt.Errorf("%w: the account has no %s folder", mail.ErrNotFound, nameOrAlias)
	default:
		return nameOrAlias, nil
	}
}

// aliasFolder returns the name of the first folder with the alias, or "".
func aliasFolder(folders []mail.Folder, alias string) string {
	for _, f := range folders {
		if f.Alias == alias {
			return f.Name
		}
	}
	return ""
}

// ---------------------------------------------------------------- drafts

// CreateDraft saves a new draft and returns its ref. It needs Mail 3. A reply or
// a forward reads the original message first. The draft keeps its Bcc header.
func (s *Service) CreateDraft(ctx context.Context, id account.AccountID, c mail.Compose) (mail.Ref, error) {
	acc, b, err := s.begin(ctx, id, policy.ActDraft, toolCreateDraft)
	if err != nil {
		return "", err
	}
	msg, _, err := s.buildMessage(ctx, b, acc.Account, c, true)
	if err != nil {
		s.finish(ctx, acc, toolCreateDraft, 1, nil, "", err)
		return "", err
	}
	ref, err := b.SaveDraft(ctx, msg)
	s.finish(ctx, acc, toolCreateDraft, 1, nil, "", err)
	return ref, agentError(err)
}

// DiscardDraft moves a draft to Trash. It needs Mail 3. The backend refuses a
// message that is not a draft, and it never deletes mail for good.
func (s *Service) DiscardDraft(ctx context.Context, id account.AccountID, ref mail.Ref) error {
	acc, b, err := s.begin(ctx, id, policy.ActDraft, toolDiscardDraft)
	if err != nil {
		return err
	}
	err = b.DiscardDraft(ctx, ref)
	s.finish(ctx, acc, toolDiscardDraft, 1, nil, "", err)
	return agentError(err)
}

// ---------------------------------------------------------------- confirmations

// asked is a pending action that waits for the user, and how the client gets
// the answer.
type asked struct {
	pending *confirm.Pending
	// state is the request state for the in-band mode. It is empty for the
	// fallback mode.
	state string
	// link is the approval page for the fallback mode. It may be empty when no
	// link could be made: the action still waits on the approvals page.
	link string
}

// ask stores an action that waits for the user, and records the
// "pending_approval" entry with the reason as its code.
//
// The action keeps the mode of the path that the service took. An in-band
// action can be answered only in the chat (the web page refuses it), and it
// ends with its request state. A fallback action waits for the web page.
func (s *Service) ask(ctx context.Context, acc access, tool string, kind confirm.Kind, payload []byte, preview confirm.Preview, mode ConfirmMode, recipients []string, reason string) (*asked, error) {
	t := acc.Principal.Tenant
	stored := confirm.ModeApproval
	if mode == ConfirmInBand {
		stored = confirm.ModeInBand
	}
	p, err := s.d.Confirm.Create(ctx, t, acc.Account.ID, kind, stored, payload, preview)
	if err != nil {
		s.finish(ctx, acc, tool, preview.Count, recipients, "", err)
		return nil, err
	}
	a := &asked{pending: p}
	if mode == ConfirmInBand {
		a.state, err = s.d.Confirm.RequestState(p)
		if err != nil {
			s.endWaiting(ctx, t, p.ID)
			s.finish(ctx, acc, tool, preview.Count, recipients, "", err)
			return nil, err
		}
	} else {
		a.link = s.approvalLink(ctx, p.ID)
	}
	s.recordOutcome(ctx, acc, tool, preview.Count, recipients, audit.OutcomePendingApproval, reason)
	return a, nil
}

// approvalLink returns the address of the approval page of an action, or "" when
// there is no Links or it fails. The action still waits on the approvals page.
func (s *Service) approvalLink(ctx context.Context, id string) string {
	if s.d.Links == nil {
		return ""
	}
	link, err := s.d.Links.Approval(ctx, id)
	if err != nil {
		return ""
	}
	return link
}

// declineAnswer records the user's "no" in band. The state must be genuine, the
// payload unchanged, and the action must be of this kind and belong to this
// account (Resume and the checks). Then the action moves from "pending" to
// "rejected". It acts on nothing.
func (s *Service) declineAnswer(ctx context.Context, acc access, kind confirm.Kind, payload []byte, ans *ConfirmAnswer) (*confirm.Pending, error) {
	t := acc.Principal.Tenant
	p, err := s.d.Confirm.Resume(ctx, t, ans.RequestState, payload)
	if err != nil {
		return nil, err
	}
	if !sameAction(p, acc, kind) {
		return nil, confirm.ErrStateInvalid
	}
	if err := s.d.Confirm.Transition(ctx, t, p.ID, confirm.StatePending, confirm.StateRejected, ""); err != nil {
		return nil, err
	}
	return p, nil
}

// claimAnswer takes the action that the state names, after the user accepted in
// band. Claim is the gate: it checks the state, the tenant and the payload, and
// moves the action from "pending" to "approved" in one compare-and-set, so only
// one caller can act on it.
//
// A state of another account of the tenant, or of another kind of action, is
// refused before the claim. So an agent cannot use the confirmation of one
// account to act on another one, or a Trash confirmation to send (the payload
// bytes of the two kinds can be the same, because a Trash ref is any text), and
// such a try does not use the confirmation up.
func (s *Service) claimAnswer(ctx context.Context, acc access, kind confirm.Kind, payload []byte, ans *ConfirmAnswer) (*confirm.Pending, error) {
	t := acc.Principal.Tenant
	checked, err := s.d.Confirm.Resume(ctx, t, ans.RequestState, payload)
	if err != nil {
		return nil, err
	}
	if !sameAction(checked, acc, kind) {
		return nil, confirm.ErrStateInvalid
	}
	p, err := s.d.Confirm.Claim(ctx, t, ans.RequestState, payload)
	if err != nil {
		return nil, err
	}
	if !sameAction(p, acc, kind) {
		// The account and the kind of an action never change, so this does not
		// happen.
		s.endAction(ctx, t, p.ID, confirm.StateRejected, "")
		return nil, confirm.ErrStateInvalid
	}
	return p, nil
}

// sameAction reports whether the action is of this kind and belongs to the
// account of acc.
func sameAction(p *confirm.Pending, acc access, kind confirm.Kind) bool {
	return p.Account == acc.Account.ID && p.Kind == kind
}

// endAction moves an approved action to a final state. An empty id does
// nothing: the action had no confirmation. The move runs even if ctx has ended.
// A failure is logged with a code: the action then stays approved, and the
// clean-up at start-up ends it.
func (s *Service) endAction(ctx context.Context, t account.TenantID, id string, to confirm.State, resultID string) {
	if id == "" {
		return
	}
	if err := s.d.Confirm.Transition(ctx, t, id, confirm.StateApproved, to, resultID); err != nil {
		s.d.Logger.Error("service: a pending action could not be ended", "code", "pending_end_failed")
	}
}

// endWaiting moves a waiting action to "rejected", when the service cannot give
// the client a way to answer it.
func (s *Service) endWaiting(ctx context.Context, t account.TenantID, id string) {
	if err := s.d.Confirm.Transition(ctx, t, id, confirm.StatePending, confirm.StateRejected, ""); err != nil {
		s.d.Logger.Error("service: a pending action could not be ended", "code", "pending_end_failed")
	}
}

// ---------------------------------------------------------------- Trash

// Trash moves messages to the Trash folder. It needs Mail 5. The backend never
// deletes mail for good.
//
// The order is:
//  1. The permission (Decide), before anything else.
//  2. The refs: at least one, none empty and none with a line break. A ref that
//     is twice in the request counts once.
//  3. The limits that can never allow the request: a Trash limit of 0, or more
//     messages than the limit of a day.
//  4. The confirmation, when the account has ConfirmDeletes. A first call
//     reads the head of each message (see trashLines), stores the request with
//     a preview that lists the messages, and returns a NeedsConfirmation (in
//     band) or "pending_approval" with a link (fallback). A second call with an
//     answer acts only when the user accepted.
//  5. The limiter counts the messages at the moment of the move, so a request
//     that is declined or refused uses no quota.
//
// When the backend moved some or all of the messages and then failed, the error
// wraps mail.ErrPartial and tells the agent to search again. The messages count
// toward the limit.
//
// While the program stops (see Stop), Trash returns ErrStopping before step 1
// and changes nothing.
func (s *Service) Trash(ctx context.Context, id account.AccountID, refs []mail.Ref, mode ConfirmMode, ans *ConfirmAnswer) (*TrashResult, *NeedsConfirmation, error) {
	if err := s.confirmDeps(); err != nil {
		return nil, nil, err
	}
	end, err := s.beginAction()
	if err != nil {
		s.recordRefusal(ctx, id, toolTrash, err)
		return nil, nil, err
	}
	defer end()
	acc, b, err := s.begin(ctx, id, policy.ActTrash, toolTrash)
	if err != nil {
		return nil, nil, err
	}
	refs, payload, err := trashRequest(refs)
	if err == nil {
		err = checkMode(mode)
	}
	if err == nil {
		err = checkLimit(policy.CountTrash, acc.Account.Limits, len(refs))
	}
	if err != nil {
		s.finish(ctx, acc, toolTrash, len(refs), nil, "", err)
		return nil, nil, err
	}
	n := len(refs)

	if ans != nil {
		if !ans.Accepted {
			if _, err := s.declineAnswer(ctx, acc, confirm.KindTrash, payload, ans); err != nil {
				s.finish(ctx, acc, toolTrash, n, nil, "", err)
				return nil, nil, err
			}
			s.recordOutcome(ctx, acc, toolTrash, n, nil, audit.OutcomeDeclined, "declined")
			return &TrashResult{Status: TrashDeclined, Count: n}, nil, nil
		}
		p, err := s.claimAnswer(ctx, acc, confirm.KindTrash, payload, ans)
		if err != nil {
			s.finish(ctx, acc, toolTrash, n, nil, "", err)
			return nil, nil, err
		}
		res, err := s.trashNow(ctx, acc, b, refs, p.ID, detailInBand)
		return res, nil, err
	}

	if !acc.Decision.NeedsConfirmation {
		res, err := s.trashNow(ctx, acc, b, refs, "", detailNoConfirmation)
		return res, nil, err
	}
	lines, err := trashLines(ctx, b, refs)
	if err != nil {
		s.finish(ctx, acc, toolTrash, n, nil, "", err)
		return nil, nil, err
	}
	preview := confirm.Preview{Account: acc.Account.Address, Count: n, Messages: lines}
	a, err := s.ask(ctx, acc, toolTrash, confirm.KindTrash, payload, preview, mode, nil, "confirm_deletes")
	if err != nil {
		return nil, nil, err
	}
	if mode == ConfirmInBand {
		return nil, &NeedsConfirmation{Pending: a.pending, RequestState: a.state}, nil
	}
	return &TrashResult{Status: TrashPendingApproval, Count: n, ApprovalLink: a.link}, nil, nil
}

// The limits of the preview of a Trash action.
const (
	// trashPreviewMessages is the most messages that the preview of a Trash
	// action lists. The preview says how many more there are.
	trashPreviewMessages = 20
	// trashPreviewSubjectChars is the most characters of a subject in the list.
	trashPreviewSubjectChars = 100
	// trashLineGone is the line of a message that was not found.
	trashLineGone = "(not found: it may have been moved or deleted already)"
)

// trashLines returns the lines of the preview of a Trash action: one line for
// each of the first 20 refs (see trashLine), so the user sees which messages go
// to Trash and not only how many.
//
// It reads the head of each message once, when the user is asked. The answer
// and the approval do not read them again and compare nothing: the action is
// bound to the refs themselves (the folder, the UIDVALIDITY and the UID of each
// message), which are its payload, and an IMAP message cannot change under its
// UID.
//
// A message that is not found (it was moved or deleted, or its ref is out of
// date) gets a line that says so, and the request goes on. Any other error (for
// example a server that does not answer) stops the request, and nothing is
// asked.
func trashLines(ctx context.Context, b mail.Backend, refs []mail.Ref) ([]string, error) {
	n := min(len(refs), trashPreviewMessages)
	lines := make([]string, 0, n)
	for _, ref := range refs[:n] {
		m, err := b.GetMessage(ctx, ref, mail.BodyOptions{Format: mail.BodyNone})
		switch {
		case errors.Is(err, mail.ErrNotFound), errors.Is(err, mail.ErrRefStale):
			lines = append(lines, trashLineGone)
		case err != nil:
			return nil, err
		default:
			lines = append(lines, trashLine(m.Summary))
		}
	}
	return lines, nil
}

// trashLine is one line of the preview of a Trash action: "date — sender —
// subject". The date is in the local time of this computer. The sender is the
// address only: the sender chooses the display name, so it could pose as
// someone else. Every part comes from a third party, so it is cleaned with
// mime.CleanUntrusted and put on one line, and the subject is cut.
func trashLine(s mail.Summary) string {
	date := "(no date)"
	if !s.Date.IsZero() {
		date = s.Date.Local().Format("2006-01-02 15:04 MST")
	}
	from := oneLineText(s.From.Email)
	if from == "" {
		from = "(no sender)"
	}
	subject := oneLineText(s.Subject)
	if short := firstRunes(subject, trashPreviewSubjectChars); len(short) < len(subject) {
		subject = short + "..."
	}
	if subject == "" {
		subject = "(no subject)"
	}
	return date + " — " + from + " — " + subject
}

// oneLineText cleans s with mime.CleanUntrusted and puts it on one line: every
// run of white space becomes one space, and the ends lose theirs.
func oneLineText(s string) string {
	return strings.Join(strings.Fields(mime.CleanUntrusted(s)), " ")
}

// trashNow counts the messages against the Trash limit and moves them. pendingID
// is the approved action that this carries out, or "" when there was no
// confirmation. how is the audit code of a success.
//
// Once the backend was called, the action ends: "done" when the move happened in
// full or in part, "rejected" when it failed before any change. It is never run
// again.
func (s *Service) trashNow(ctx context.Context, acc access, b mail.Backend, refs []mail.Ref, pendingID, how string) (*TrashResult, error) {
	t, n := acc.Principal.Tenant, len(refs)
	if err := s.d.Limiter.Reserve(ctx, t, acc.Account.ID, policy.CountTrash, n, acc.Account.Limits); err != nil {
		s.endAction(ctx, t, pendingID, confirm.StateRejected, "")
		s.finish(ctx, acc, toolTrash, n, nil, "", err)
		return nil, err
	}
	err := b.Trash(ctx, refs)
	if err == nil || errors.Is(err, mail.ErrPartial) {
		s.endAction(ctx, t, pendingID, confirm.StateDone, "")
	} else {
		s.endAction(ctx, t, pendingID, confirm.StateRejected, "")
	}
	s.finish(ctx, acc, toolTrash, n, nil, how, err)
	if err != nil {
		return nil, agentError(err)
	}
	return &TrashResult{Status: TrashDone, Count: n}, nil
}

// trashRequest checks the refs of a Trash request and returns them sorted and
// without repeats, and the payload of the confirmation: the refs joined with
// "\n".
//
// A ref must not be empty and must not have a line break. A ref with a line break
// would read back from the payload as two refs, so the user would approve one
// message and lose two.
func trashRequest(refs []mail.Ref) ([]mail.Ref, []byte, error) {
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("%w: give at least one message", ErrInvalidRequest)
	}
	out := make([]mail.Ref, 0, len(refs))
	for _, r := range refs {
		if r == "" || strings.ContainsAny(string(r), "\r\n") {
			return nil, nil, fmt.Errorf("%w: a message ref is empty or has a line break", ErrInvalidRequest)
		}
		out = append(out, r)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	parts := make([]string, len(out))
	for i, r := range out {
		parts[i] = string(r)
	}
	return out, []byte(strings.Join(parts, "\n")), nil
}

// trashRefs reads the refs back from the payload of a Trash action.
func trashRefs(payload []byte) []mail.Ref {
	parts := strings.Split(string(payload), "\n")
	refs := make([]mail.Ref, len(parts))
	for i, p := range parts {
		refs[i] = mail.Ref(p)
	}
	return refs
}

// ---------------------------------------------------------------- the approvals page

// ApprovePending carries out an action that waits for the user. The web page
// calls it when the user presses Approve, in the tenant of the principal.
//
// Only an action of the mode confirm.ModeApproval can be approved here. An
// in-band action gives ErrAnswerInBand (it matches confirm.ErrNotPending), and
// the refusal is in the audit log.
//
// The permission of the account is checked again, so an action that the level
// no longer allows is refused. The move from "pending" to "approved" is the
// compare-and-set gate: when two approvals (or an approval and the agent
// withdrawing the send) come at the same time, one acts and the others get
// confirm.ErrNotPending. A send uses the confirmed undo window. The action then
// moves to "done", with the id of the outbox item for a send.
//
// A send is built again from its payload, with the function that makes the
// approval page (see PendingDetail). When the message would not be the one that
// the preview showed (other recipients, sender, subject or body start), the
// action is rejected and the error wraps confirm.ErrPayloadChanged. When the
// whole message cannot be built now (a mail server that does not answer, or a
// recipient that cannot be shown exactly, ErrHiddenAddress), nothing is sent
// and the action keeps waiting: the page cannot show it in full either, so it
// is never approved from a partial view.
//
// An approval that cannot be carried out (no such action, or it does not wait
// any more) is in the audit log as "refused". A Trash approval needs no outbox.
//
// While the program stops (see Stop), it returns ErrStopping and changes
// nothing: the action keeps waiting, and the user can approve it after the
// restart.
func (s *Service) ApprovePending(ctx context.Context, pendingID string) error {
	if err := s.confirmDeps(); err != nil {
		return err
	}
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return err
	}
	end, err := s.beginAction()
	if err != nil {
		s.recordPendingRefusal(ctx, p, nil, toolApprovePending, err)
		return err
	}
	defer end()
	pending, payload, err := s.d.Confirm.Get(ctx, p.Tenant, pendingID)
	if err != nil {
		s.recordPendingRefusal(ctx, p, nil, toolApprovePending, err)
		return err
	}
	if pending.Mode != confirm.ModeApproval {
		s.recordPendingRefusal(ctx, p, pending, toolApprovePending, ErrAnswerInBand)
		return ErrAnswerInBand
	}
	if pending.State != confirm.StatePending || payload == nil {
		err := confirm.ErrNotPending
		s.recordPendingRefusal(ctx, p, pending, toolApprovePending, err)
		return err
	}
	switch pending.Kind {
	case confirm.KindSend:
		if err := s.sendDeps(); err != nil {
			return err
		}
		return s.approveSend(ctx, pending, payload)
	case confirm.KindTrash:
		return s.approveTrash(ctx, pending, payload)
	default:
		return fmt.Errorf("service: unknown kind of action %q", pending.Kind)
	}
}

// approveTrash is ApprovePending for a Trash action.
func (s *Service) approveTrash(ctx context.Context, pending *confirm.Pending, payload []byte) error {
	acc, b, err := s.begin(ctx, pending.Account, policy.ActTrash, toolTrash)
	if err != nil {
		return err
	}
	refs := trashRefs(payload)
	if err := checkLimit(policy.CountTrash, acc.Account.Limits, len(refs)); err != nil {
		s.finish(ctx, acc, toolTrash, len(refs), nil, "", err)
		return err
	}
	err = s.d.Confirm.Transition(ctx, acc.Principal.Tenant, pending.ID, confirm.StatePending, confirm.StateApproved, "")
	if err != nil {
		s.finish(ctx, acc, toolTrash, len(refs), nil, "", err)
		return err
	}
	_, err = s.trashNow(ctx, acc, b, refs, pending.ID, detailOnWeb)
	return err
}

// RejectPending rejects an action that waits for the user. The web page calls it
// when the user presses Reject, in the tenant of the principal. It returns
// confirm.ErrNotPending when the action does not wait any more, and
// ErrAnswerInBand (it matches confirm.ErrNotPending) for an action that the user
// answers in the chat. A rejection that cannot be carried out is in the audit
// log as "refused".
func (s *Service) RejectPending(ctx context.Context, pendingID string) error {
	if err := s.confirmDeps(); err != nil {
		return err
	}
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return err
	}
	pending, _, err := s.d.Confirm.Get(ctx, p.Tenant, pendingID)
	if err != nil {
		s.recordPendingRefusal(ctx, p, nil, toolRejectPending, err)
		return err
	}
	if pending.Mode != confirm.ModeApproval {
		s.recordPendingRefusal(ctx, p, pending, toolRejectPending, ErrAnswerInBand)
		return ErrAnswerInBand
	}
	if err := s.d.Confirm.Transition(ctx, p.Tenant, pendingID, confirm.StatePending, confirm.StateRejected, ""); err != nil {
		s.recordPendingRefusal(ctx, p, pending, toolRejectPending, err)
		return err
	}
	s.record(ctx, p, entry{
		tool: toolOf(pending.Kind), account: pending.Account, count: pending.Preview.Count, recipients: pending.Preview.To,
		outcome: audit.OutcomeDeclined, detail: "rejected_on_web",
	})
	return nil
}

// recordPendingRefusal records an approval or a rejection that could not be
// carried out. When the action is known, the entry has the tool of the action
// and its account. Otherwise it is an entry of the tenant under fallbackTool.
func (s *Service) recordPendingRefusal(ctx context.Context, p Principal, pending *confirm.Pending, fallbackTool string, err error) {
	e := entry{tool: fallbackTool}
	if pending != nil {
		e.tool, e.account = toolOf(pending.Kind), pending.Account
	}
	e.outcome, e.detail = auditCode(err)
	s.record(ctx, p, e)
}

// toolOf returns the tool of a kind of action.
func toolOf(kind confirm.Kind) string {
	if kind == confirm.KindSend {
		return toolSend
	}
	return toolTrash
}
