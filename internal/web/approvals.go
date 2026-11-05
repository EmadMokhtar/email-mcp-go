package web

// This file holds the approvals pages: the actions that wait for the user on
// the web page, shown as a list beside the action that is open, with its
// Approve and Reject buttons, and the result of an action.
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
	"sync"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The names of the pages of this file.
const (
	pageApprovals = "approvals"
	pageApproval  = "approval"
)

// pathApprovals is the address of the list of the actions that wait.
const pathApprovals = "/approvals"

// approvalOpenTimeout limits how long the list of the actions waits for the
// first action. A reply or a forward reads its original from the mail server,
// which must not hold back the list. Past the limit, the action shows what was
// stored when the agent asked, and only Reject.
const approvalOpenTimeout = 3 * time.Second

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
	msgActionUnavailable = "This action cannot be shown now. Try again later."
	msgDetailUnavailable = "The whole message cannot be shown now, so it cannot be approved. This is the start of the message " +
		"that was stored when the agent asked. Reload this page when the mail server answers, or reject the action."
	textNoSubject = "(no subject)"
)

// The tones of an outcome. The template uses them in a class name.
const (
	toneGood = "good"
	toneBad  = "bad"
	toneInfo = "info"
)

// The result of an action that has ended or is running. It tells the truth of
// the send: an approved send that the mail server refused is "Not sent".
var (
	outcomeRunning = outcome{"Approved", "It is being carried out.", toneInfo}
	// outcomeUnknown is for a send whose status cannot be read now.
	outcomeUnknown  = outcome{"Approved", "The result is not known yet. Check Activity.", toneInfo}
	outcomeRejected = outcome{"Rejected", "Nothing was done.", toneInfo}
	outcomeExpired  = outcome{"Expired", "It can no longer be approved.", toneInfo}
	outcomeTrashed  = outcome{"Moved to Trash", "The messages are in the Trash folder of the account.", toneGood}
	outcomeSent     = outcome{"Sent", "The mail server accepted the message.", toneGood}
	outcomeSending  = outcome{"Sending", "The mail server is still working on it. Reload this page to see the result.", toneInfo}
	outcomeFailed   = outcome{"Not sent", "The mail server did not take the message, and it will not try again. Ask the agent to send it again.", toneBad}
	outcomeCanceled = outcome{"Cancelled", "The message was cancelled before it went out.", toneInfo}
)

// outcome is what happened to an action that does not wait any more.
type outcome struct {
	Title string
	Text  string
	Tone  string
}

// approvalRow is one action in the list of the actions that wait.
type approvalRow struct {
	ID      string
	Account string
	// Title is the subject of a send, or what a Trash action does.
	Title string
	// To is the text of the recipients of a send, and ToShort is the same for
	// the list, such as "3 people". They are empty for a Trash action. The page
	// of the action always shows every recipient.
	To      string
	ToShort string
	// Summary names the action on the page of its result.
	Summary string
	// Left says how long the user has to answer, such as "23 h left".
	Left string
	// Current is true for the action that the page shows.
	Current bool
}

// approvalPage is the data of the page of one action. Every text is cleaned.
type approvalPage struct {
	ID    string
	Error string
	// Waiting is true while the action waits: the page then has the Reject
	// button.
	Waiting bool
	// CanApprove is true when the page also has the Approve button: the action
	// waits, and the page shows all of it. A send whose whole message cannot be
	// built now is never approved from the stored start alone.
	CanApprove bool
	// Outcome says what happened to an action that does not wait. It is nil
	// while the action waits.
	Outcome *outcome
	// Note says what an action was that the user just answered, or it is nil.
	Note *answerNote
	// Queue lists the actions that wait, and Next is the first of them for the
	// page of an action that has ended.
	Queue []approvalRow
	Next  *approvalRow
	// Opened is true when the address names this action. A small screen then
	// shows the action and not the list.
	Opened  bool
	Send    bool
	Account string
	Count   int
	Asked   string
	Expires string

	// The message of a send.
	From       string
	Recipients []string
	// MoreNames says how much of the display names Recipients leaves out, or is
	// empty.
	MoreNames string
	// Warnings are lines about recipients that can look like another address.
	Warnings    []string
	Subject     string
	Attachments []string
	Body        string
	// BodyNote says that Body is only the stored start of the text, because
	// the whole message could not be built. It is empty otherwise.
	BodyNote string

	// The messages of a Trash action: one line for each (date, sender address,
	// subject), at most 20, and a line that says how many more there are.
	Messages     []string
	MoreMessages string
}

// How long the page of a result remembers what the action was, and how many
// actions it remembers.
const (
	answerNoteTTL  = 10 * time.Minute
	maxAnswerNotes = 64
)

// answerNote says what an action was. The service deletes the preview of an
// action when it ends, so the page of the result can no longer read it. The
// note lives only in the memory of this process, for a short time. When it is
// gone, the page shows the result without it.
type answerNote struct {
	What string
	To   string
	at   time.Time
}

// answerNotes are the notes of the actions that the user just answered, by the
// id of the action. An id is read only after the service found the action in
// the tenant of the session.
type answerNotes struct {
	mu    sync.Mutex
	items map[string]answerNote
}

// put stores a note, and drops the notes that are too old.
func (n *answerNotes) put(id string, note answerNote) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, old := range n.items {
		if note.at.Sub(old.at) >= answerNoteTTL {
			delete(n.items, key)
		}
	}
	if len(n.items) >= maxAnswerNotes {
		clear(n.items)
	}
	n.items[id] = note
}

// get returns the note of an action, or nil.
func (n *answerNotes) get(id string, now time.Time) *answerNote {
	n.mu.Lock()
	defer n.mu.Unlock()
	note, ok := n.items[id]
	if !ok || now.Sub(note.at) >= answerNoteTTL {
		return nil
	}
	return &note
}

// queueOf lists the actions that wait for the user here.
func queueOf(svc *service.Service, ctx context.Context, now time.Time) ([]approvalRow, error) {
	list, err := svc.PendingApprovals(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]approvalRow, 0, len(list))
	for _, p := range list {
		row := approvalRow{ID: p.ID, Account: p.Preview.Account, Left: timeLeft(p.ExpiresAt.Sub(now))}
		if p.Kind == confirm.KindSend {
			row.Title = p.Preview.Subject
			if row.Title == "" {
				row.Title = textNoSubject
			}
			row.To = strings.Join(p.Preview.To, ", ")
			row.ToShort = row.To
			if n := len(p.Preview.To); n > 1 {
				row.ToShort = fmt.Sprintf("%d people", n)
			}
			row.Summary = row.Title
		} else {
			row.Title = trashText(p.Preview.Count)
			row.Summary = messagesText(p.Preview.Count) + " of " + p.Preview.Account
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// waitingCount returns the number of actions that wait, for the menu. A failure
// only hides the number: the page itself still works.
func (s *Server) waitingCount(r *http.Request) int {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		return 0
	}
	list, err := svc.PendingApprovals(ctx)
	if err != nil {
		return 0
	}
	return len(list)
}

// handleApprovals shows the actions of the tenant of the session that wait for
// the user here. The first one opens beside the list, so the user can answer it
// at once. It changes nothing.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeApprovalsFailed, errNoService)
		return
	}
	queue, err := queueOf(svc, ctx, s.now())
	if err != nil {
		s.fail(w, r, codeApprovalsFailed, err)
		return
	}
	if len(queue) == 0 {
		s.render(w, r, http.StatusOK, pageApprovals, nil)
		return
	}
	openCtx, cancel := context.WithTimeout(ctx, approvalOpenTimeout)
	defer cancel()
	page, found, err := approvalOf(svc, openCtx, queue[0].ID)
	switch {
	case err != nil:
		// One action that cannot be read must not hide the others.
		s.log.Warn("web approval not shown", "route", r.Pattern, "code", codeApprovalsFailed,
			"error_types", errtype.Chain(err))
		page = approvalPage{ID: queue[0].ID, Error: msgActionUnavailable}
	case !found:
		// The action ended after the list was read.
		page = approvalPage{}
	}
	s.showApproval(w, r, http.StatusOK, page, queue)
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
		page.Opened = true
		s.showApprovalWithQueue(w, r, http.StatusOK, page)
	}
}

// showApprovalWithQueue reads the list of the actions that wait, and writes the
// page of an action with it.
func (s *Server) showApprovalWithQueue(w http.ResponseWriter, r *http.Request, status int, page approvalPage) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeApprovalsFailed, errNoService)
		return
	}
	queue, err := queueOf(svc, ctx, s.now())
	if err != nil {
		s.fail(w, r, codeApprovalsFailed, err)
		return
	}
	s.showApproval(w, r, status, page, queue)
}

// showApproval writes the page of an action, with the list of the actions that
// wait beside it.
func (s *Server) showApproval(w http.ResponseWriter, r *http.Request, status int, page approvalPage, queue []approvalRow) {
	for i := range queue {
		queue[i].Current = queue[i].ID == page.ID
	}
	page.Queue = queue
	if page.Outcome != nil {
		page.Note = s.notes.get(page.ID, s.now())
		if len(queue) > 0 {
			page.Next = &queue[0]
		}
	}
	s.render(w, r, status, pageApproval, page)
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
	// The preview is gone when the action ends, so the note is made first.
	note := noteOf(svc, ctx, id, s.now())
	err := call(svc, ctx, id)
	if err == nil {
		if note != nil {
			s.notes.put(id, *note)
		}
		http.Redirect(w, r, pathApprovals+"/"+url.PathEscape(id), http.StatusSeeOther)
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
	page.Opened = true
	s.showApprovalWithQueue(w, r, status, page)
}

// noteOf returns a note of an action that waits, or nil.
func noteOf(svc *service.Service, ctx context.Context, id string, now time.Time) *answerNote {
	queue, err := queueOf(svc, ctx, now)
	if err != nil {
		return nil
	}
	for _, row := range queue {
		if row.ID == id {
			return &answerNote{What: row.Summary, To: row.To, at: now}
		}
	}
	return nil
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
		Outcome: outcomeOf(svc, ctx, p),
		Send:    p.Kind == confirm.KindSend,
		Account: p.Preview.Account,
		Count:   p.Preview.Count,
		Asked:   friendlyTime(p.CreatedAt),
		Expires: friendlyTime(p.ExpiresAt),
	}
	if !page.Waiting || !page.Send {
		page.CanApprove = page.Waiting
		if page.Waiting {
			page.Messages, page.MoreMessages = p.Preview.Messages, p.Preview.MoreMessagesText()
		}
		return page, true, nil
	}
	if detail != nil {
		page.From, page.Recipients, page.MoreNames, page.Subject = detail.From, detail.Recipients, detail.MoreNames, detail.Subject
		page.Warnings = detail.Warnings
		page.Attachments, page.Body = detail.Attachments, detail.Body
		if detail.MoreChars != "" {
			page.Body += "\n" + detail.MoreChars
		}
		page.CanApprove = true
		return page, true, nil
	}
	// The service refuses Approve in this case too (it builds the message in
	// the same way), so the page offers only Reject.
	page.From, page.Recipients, page.MoreNames, page.Subject = p.Preview.From, p.Preview.Recipients(), p.Preview.MoreNamesText(), p.Preview.Subject
	page.Warnings = p.Preview.Warnings
	page.Attachments, page.Body, page.BodyNote = p.Preview.AttachmentLines(), p.Preview.BodyStart, msgDetailUnavailable
	if more := p.Preview.MoreCharsText(); more != "" {
		page.Body += "\n" + more
	}
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
		errors.Is(err, mail.ErrTooLarge), errors.Is(err, mail.ErrUnsupported), errors.Is(err, outbox.ErrTooLate),
		errors.Is(err, context.DeadlineExceeded), errors.Is(err, imapsmtp.ErrServerUnreachable),
		errors.Is(err, imapsmtp.ErrConnectionLost):
		return http.StatusBadGateway, msgMailServer, true
	}
	return 0, "", false
}

// trashText says what a Trash action does.
func trashText(count int) string {
	return "Move " + messagesText(count) + " to Trash"
}

// messagesText says how many messages there are.
func messagesText(count int) string {
	if count == 1 {
		return "1 message"
	}
	return fmt.Sprintf("%d messages", count)
}

// outcomeOf returns what happened to an action, or nil while it waits. An
// approved send is done for the service when the outbox took it, so the result
// of a send is read from the outbox.
func outcomeOf(svc *service.Service, ctx context.Context, p *confirm.Pending) *outcome {
	var result outcome
	switch {
	case p.State == confirm.StatePending:
		return nil
	case p.State == confirm.StateApproved:
		result = outcomeRunning
	case p.State == confirm.StateRejected:
		result = rejectedOutcome(svc, ctx, p)
	case p.State == confirm.StateExpired:
		result = outcomeExpired
	case p.Kind != confirm.KindSend:
		result = outcomeTrashed
	default:
		result = sendOutcome(svc, ctx, p)
	}
	return &result
}

// rejectedOutcome returns the result of an action that will not run. The agent
// can withdraw a send that waits, and then the user did not reject it.
func rejectedOutcome(svc *service.Service, ctx context.Context, p *confirm.Pending) outcome {
	if p.Kind == confirm.KindSend {
		res, err := svc.SendStatus(ctx, p.Account, p.ID)
		if err == nil && res.Status == service.SendCancelled {
			return outcomeCanceled
		}
	}
	return outcomeRejected
}

// sendOutcome returns the result of a send that the outbox took.
func sendOutcome(svc *service.Service, ctx context.Context, p *confirm.Pending) outcome {
	res, err := svc.SendStatus(ctx, p.Account, p.ID)
	if err != nil {
		return outcomeUnknown
	}
	switch res.Status {
	case service.SendSent:
		return outcomeSent
	case service.SendSending:
		return outcomeSending
	case service.SendFailed:
		return outcomeFailed
	case service.SendCancelled:
		return outcomeCanceled
	case service.SendQueued:
		text := "It waits to be sent."
		if res.SendAt != nil {
			text = "It goes out at " + res.SendAt.Local().Format("15:04:05") + "."
		}
		return outcome{"Approved", text, toneInfo}
	}
	return outcomeUnknown
}

// timeLeft says how long the user has to answer an action.
func timeLeft(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "Under 1 min left"
	case d < time.Hour:
		return fmt.Sprintf("%d min left", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h left", int(d.Hours()))
}

// friendlyTime writes a time near today for a person, such as "5 Nov at 16:01".
func friendlyTime(t time.Time) string {
	return t.Local().Format("2 Jan at 15:04")
}
