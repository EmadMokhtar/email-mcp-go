package web

// This file holds the approvals pages: the list of the actions that wait for
// the user on the web page, the page of one action, and its Approve and Reject
// buttons.
//
// Only an action of the approval mode is shown or answered here: an action for
// a client that could not ask the user in its chat. An action that the user
// answers in the chat (in band) is not shown, and the service refuses to
// approve or reject it here, so the same message cannot be approved twice.
//
// Approve and Reject call Service.ApprovePending and Service.RejectPending only
// from a POST with the CSRF token: the user pressed the button.
//
// Every text of a message (addresses, subject, file names, body) comes cleaned
// with mime.CleanUntrusted from the service, and html/template writes it as
// text. The HTML of a message is never put in a page: the page shows the text
// that the recipient sees.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The names of the pages of this file.
const (
	pageApprovals = "approvals"
	pageApproval  = "approval"
)

// Fixed codes for the log.
const (
	codeApprovalsFailed = "approvals_failed"
	codeApproveFailed   = "approve_failed"
)

// The fixed texts of the approval pages.
const (
	titleApprovalAbsent  = "Approval not found"
	msgApprovalNotFound  = "This approval was not found."
	msgNotWaiting        = "This action is no longer waiting for approval: it was approved, rejected, or it expired."
	msgAnswerInChat      = "This action waits for an answer in the chat where the agent asked. It cannot be approved or rejected here."
	msgMessageChanged    = "The message would not be the one shown here, so nothing was sent. Ask the agent to send it again."
	msgLevelTooLow       = "The permission level of the account does not allow this action any more. Raise it in the settings of the account, or reject the action."
	msgReconnect         = "The account must be connected again before this action can run."
	msgAccountGone       = "The account of this action was not found."
	msgStopping          = "The server is stopping, so nothing was sent or changed. Approve again after it restarts."
	msgRateLimited       = "A limit of the account does not allow this now. Try again later."
	msgLimit             = "A limit of the account does not allow this action. Change the limits in the settings of the account, or reject the action."
	msgInvalidRequest    = "The stored request cannot be carried out. Reject it, and ask the agent again."
	msgPartial           = "Some of the change was made before an error. Check the mailbox."
	msgMailServer        = "The mail server could not do this. Try again later."
	msgDetailUnavailable = "The whole message could not be loaded now. This is the start of the message that was stored when the agent asked."
)

// The text of each state of an action that has ended or is running.
var stateTexts = map[confirm.State]string{
	confirm.StateApproved: "Approved. It is being carried out.",
	confirm.StateDone:     "Approved and done.",
	confirm.StateRejected: "Rejected. Nothing was done.",
	confirm.StateExpired:  "Expired. It can no longer be approved.",
}

// approvalsPage is the data of the list of approvals.
type approvalsPage struct {
	Items []approvalRow
}

// approvalRow is one action in the list.
type approvalRow struct {
	ID      string
	Account string
	// What says what the action does, such as "Send “Lunch”".
	What    string
	To      string
	Created string
	Expires string
}

// approvalPage is the data of the page of one action. Every text is cleaned.
type approvalPage struct {
	ID    string
	Error string
	// Waiting is true while the action waits: the page then has the buttons.
	Waiting bool
	// State says what happened to an action that does not wait.
	State   string
	Send    bool
	Account string
	Count   int
	Expires string

	// The message of a send.
	From        string
	Recipients  []string
	Subject     string
	Attachments []string
	Body        string
	// BodyNote says that Body is only the stored start of the text, because
	// the whole message could not be built. It is empty otherwise.
	BodyNote string
}

// handleApprovals lists the actions of the tenant of the session that wait for
// the user here. It changes nothing.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeApprovalsFailed, errNoService)
		return
	}
	list, err := svc.PendingApprovals(ctx)
	if err != nil {
		s.fail(w, r, codeApprovalsFailed, err)
		return
	}
	page := approvalsPage{Items: make([]approvalRow, 0, len(list))}
	for _, p := range list {
		row := approvalRow{
			ID:      p.ID,
			Account: p.Preview.Account,
			Created: formatTime(p.CreatedAt),
			Expires: formatTime(p.ExpiresAt),
		}
		if p.Kind == confirm.KindSend {
			row.What = "Send “" + p.Preview.Subject + "”"
			row.To = strings.Join(p.Preview.To, ", ")
		} else {
			row.What = trashText(p.Preview.Count)
		}
		page.Items = append(page.Items, row)
	}
	s.render(w, r, http.StatusOK, pageApprovals, page)
}

// handleApproval shows one action of the tenant of the session. It changes
// nothing and sends nothing.
func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeApprovalsFailed, errNoService)
		return
	}
	page, found, err := approvalOf(svc, ctx, r.PathValue("id"))
	switch {
	case err != nil:
		s.fail(w, r, codeApprovalsFailed, err)
	case !found:
		s.message(w, r, http.StatusNotFound, titleApprovalAbsent, msgApprovalNotFound)
	default:
		s.render(w, r, http.StatusOK, pageApproval, page)
	}
}

// handleApprove carries out an action when the user pressed Approve, and then
// redirects to the page of the action. A refusal shows the page of the action
// with a fixed text.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	s.answer(w, r, (*service.Service).ApprovePending)
}

// handleReject rejects an action when the user pressed Reject, and then
// redirects to the page of the action.
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	s.answer(w, r, (*service.Service).RejectPending)
}

// answer runs an answer of the user (approve or reject) on the action of the
// path.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, call func(*service.Service, context.Context, string) error) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeApproveFailed, errNoService)
		return
	}
	id := r.PathValue("id")
	err := call(svc, ctx, id)
	if err == nil {
		http.Redirect(w, r, "/approvals/"+url.PathEscape(id), http.StatusSeeOther)
		return
	}
	status, text, known := answerErrorText(err)
	if !known {
		s.fail(w, r, codeApproveFailed, err)
		return
	}
	s.log.Info("web answer refused", "route", r.Pattern, "status", status)
	if status == http.StatusNotFound {
		s.message(w, r, status, titleApprovalAbsent, text)
		return
	}
	page, found, err := approvalOf(svc, ctx, id)
	if err != nil || !found {
		// The action cannot be shown (an in-band action is not shown). The
		// fixed text is enough.
		page = approvalPage{ID: id}
	}
	page.Error = text
	s.render(w, r, status, pageApproval, page)
}

// approvalOf returns the page of an action of the tenant of ctx. found is false
// when the service does not show it.
func approvalOf(svc *service.Service, ctx context.Context, id string) (approvalPage, bool, error) {
	p, detail, err := svc.PendingDetail(ctx, id)
	if errors.Is(err, confirm.ErrNotFound) {
		return approvalPage{}, false, nil
	}
	if err != nil {
		return approvalPage{}, false, err
	}
	page := approvalPage{
		ID:      p.ID,
		Waiting: p.State == confirm.StatePending,
		State:   stateTexts[p.State],
		Send:    p.Kind == confirm.KindSend,
		Account: p.Preview.Account,
		Count:   p.Preview.Count,
		Expires: formatTime(p.ExpiresAt),
	}
	if !page.Waiting || !page.Send {
		return page, true, nil
	}
	if detail != nil {
		page.From, page.Recipients, page.Subject = detail.From, detail.Recipients, detail.Subject
		page.Attachments, page.Body = detail.Attachments, detail.Body
		return page, true, nil
	}
	page.From, page.Recipients, page.Subject = p.Preview.From, p.Preview.To, p.Preview.Subject
	page.Attachments, page.Body, page.BodyNote = p.Preview.Attachments, p.Preview.BodyStart, msgDetailUnavailable
	return page, true, nil
}

// answerErrorText returns the status and the fixed text for an error of
// ApprovePending or RejectPending, by its type. known is false for an error
// that is not about the action, such as a failure of the database.
func answerErrorText(err error) (status int, text string, known bool) {
	var (
		perm *service.PermissionError
		rate *policy.RateLimitError
	)
	switch {
	case errors.Is(err, confirm.ErrNotFound):
		return http.StatusNotFound, msgApprovalNotFound, true
	case errors.Is(err, service.ErrAnswerInBand):
		return http.StatusConflict, msgAnswerInChat, true
	case errors.Is(err, confirm.ErrNotPending):
		return http.StatusConflict, msgNotWaiting, true
	case errors.Is(err, confirm.ErrPayloadChanged):
		return http.StatusConflict, msgMessageChanged, true
	case errors.Is(err, service.ErrStopping):
		return http.StatusServiceUnavailable, msgStopping, true
	case errors.As(err, &perm):
		return http.StatusForbidden, msgLevelTooLow, true
	case errors.Is(err, service.ErrNeedsReauth):
		return http.StatusConflict, msgReconnect, true
	case errors.Is(err, service.ErrAccountNotFound):
		return http.StatusNotFound, msgAccountGone, true
	case errors.As(err, &rate):
		return http.StatusTooManyRequests, msgRateLimited, true
	case errors.Is(err, service.ErrLimit):
		return http.StatusConflict, msgLimit, true
	case errors.Is(err, service.ErrInvalidRequest):
		return http.StatusUnprocessableEntity, msgInvalidRequest, true
	case errors.Is(err, mail.ErrPartial):
		return http.StatusBadGateway, msgPartial, true
	case errors.Is(err, mail.ErrNotFound), errors.Is(err, mail.ErrRefStale), errors.Is(err, mail.ErrConflict),
		errors.Is(err, mail.ErrTooLarge), errors.Is(err, mail.ErrUnsupported), errors.Is(err, outbox.ErrTooLate):
		return http.StatusBadGateway, msgMailServer, true
	}
	return 0, "", false
}

// trashText says what a Trash action does.
func trashText(count int) string {
	if count == 1 {
		return "Move 1 message to Trash"
	}
	return fmt.Sprintf("Move %d messages to Trash", count)
}
