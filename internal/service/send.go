package service

// This file holds sending: Send with its confirmation, the canonical payload of
// a send, the preview, the known recipients, CancelSend and SendStatus.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
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
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The statuses of a send. "queued", "sending", "sent", "failed" and "cancelled"
// are the states of the outbox item. The others are states of an action that
// waits for the user.
const (
	SendSent            = "sent"
	SendQueued          = "queued"
	SendSending         = "sending"
	SendFailed          = "failed"
	SendCancelled       = "cancelled"
	SendPendingApproval = "pending_approval"
	SendDeclined        = "declined"
	SendExpired         = "expired"
)

// The limits of building a message.
const (
	// maxDraftBytes is the largest draft that Send reads: 25 MB.
	maxDraftBytes = 25 << 20
	// maxForwardChars is the most characters of the original text that a forward
	// carries. It is the largest page that the backend returns.
	maxForwardChars = 100000
	// previewChars is the most characters of the body in a preview.
	previewChars = 500
	// previewParseChars is how much of the text and HTML of the message the
	// preview reads, so that HTML with many tags still gives 500 characters of
	// text.
	previewParseChars = 20000
	// sendPayloadVersion is the version of the send payload format.
	sendPayloadVersion = 1
	// knownRecipientsLabel makes the key of the known-recipient hashes from the
	// server key. A new label gives new keys, and the old hashes no longer match.
	knownRecipientsLabel = "known-recipients/v1"
)

// SendResult is the result of a send, or the status of one.
type SendResult struct {
	// Status is one of the Send* statuses.
	Status string
	// SendID names the send for CancelSend and SendStatus. It is the id of the
	// outbox item, or the id of the action while it waits for the user.
	SendID string
	// SendAt is the end of the undo window of a queued send. Until then the
	// send can be cancelled. It is nil for any other status.
	SendAt *time.Time
	// ApprovalLink is the page where the user approves a send that waits. It
	// never holds a token.
	ApprovalLink string
}

var (
	// ErrDraftNotFromAccount means that the draft is not from the address of the
	// account, so the service does not send it from that account.
	ErrDraftNotFromAccount = errors.New("service: the draft is not from the address of this account, so it is not sent")

	// ErrSendNotFound means that the account has no send with this id.
	ErrSendNotFound = errors.New("service: no send with this id for this account")
)

// sendDeps checks that every part that a send needs is set up (see confirmDeps).
func (s *Service) sendDeps() error {
	if err := s.confirmDeps(); err != nil {
		return err
	}
	if s.d.Outbox == nil || s.d.Vault == nil {
		return errors.New("service: the outbox and the vault must be set up to send")
	}
	return nil
}

// Send sends a new message (c) or a saved draft (draft). Exactly one of them
// must be set. It needs Mail 4.
//
// The order is:
//  1. The permission (Decide), before anything else.
//  2. The message is built: from c with mime.Build (a reply or a forward reads
//     the original first), or from the draft with GetRaw (at most 25 MB) and
//     mime.FromDraft. A draft must be from the address of the account.
//  3. The limits that can never allow it: more recipients than the account
//     allows in one message (counted on the final envelope, after a reply to all
//     adds people), or a send limit of 0.
//  4. The confirmation: when the account has ConfirmSends, or when it has the
//     new-recipient check and a recipient was never sent to. A first call stores
//     the request and returns a NeedsConfirmation (in band) or
//     "pending_approval" with a link (fallback). A second call with an answer
//     acts only when the user accepted.
//  5. The limiter counts the send at the moment it goes to the outbox, so a send
//     that is declined or refused uses no quota.
//  6. The outbox gets the message with the undo window: UndoWindowConfirmed when
//     the user confirmed, UndoWindowAuto otherwise. A draft goes with its ref,
//     and the sender of the outbox moves it to Trash after the delivery.
//
// What the user confirms is the request: the payload of the action is a
// canonical JSON of c or of the draft ref (see newSendRequest). When the answer
// comes, the message is built again from the payload, and it is sent only if it
// still matches the preview that the user saw. Otherwise the error wraps
// confirm.ErrPayloadChanged.
//
// Every call is in the audit log, with the envelope recipients.
func (s *Service) Send(ctx context.Context, id account.AccountID, c *mail.Compose, draft *mail.Ref, mode ConfirmMode, ans *ConfirmAnswer) (*SendResult, *NeedsConfirmation, error) {
	if err := s.sendDeps(); err != nil {
		return nil, nil, err
	}
	acc, b, err := s.begin(ctx, id, policy.ActSend, toolSend)
	if err != nil {
		return nil, nil, err
	}
	req, err := newSendRequest(c, draft)
	if err == nil {
		err = checkMode(mode)
	}
	if err != nil {
		s.finish(ctx, acc, toolSend, 1, nil, "", err)
		return nil, nil, err
	}
	msg, preview, err := s.prepareSend(ctx, b, acc.Account, req)
	if err != nil {
		s.finish(ctx, acc, toolSend, 1, auditRecipients(msg), "", err)
		return nil, nil, err
	}
	if ans != nil {
		res, err := s.answerSend(ctx, acc, req, msg, preview, ans)
		return res, nil, err
	}

	lim := acc.Account.Limits
	reason := ""
	switch {
	case acc.Decision.NeedsConfirmation:
		reason = "confirm_sends"
	case lim.NewRecipientCheck:
		unknown, err := s.hasNewRecipient(ctx, acc, msg.Recipients)
		if err != nil {
			s.finish(ctx, acc, toolSend, 1, auditRecipients(msg), "", err)
			return nil, nil, err
		}
		if unknown {
			reason = "new_recipient"
		}
	}
	if reason == "" {
		res, err := s.deliver(ctx, acc, msg, req.draft, lim.UndoWindowAuto, "", detailNoConfirmation)
		return res, nil, err
	}

	a, err := s.ask(ctx, acc, toolSend, confirm.KindSend, req.payload, preview, mode, auditRecipients(msg), reason)
	if err != nil {
		return nil, nil, err
	}
	if mode == ConfirmInBand {
		return nil, &NeedsConfirmation{Pending: a.pending, RequestState: a.state}, nil
	}
	return &SendResult{Status: SendPendingApproval, SendID: a.pending.ID, ApprovalLink: a.link}, nil, nil
}

// answerSend handles the second call of an in-band confirmation. A "no" ends the
// action and sends nothing. A "yes" claims the action, checks that the message is
// still the one in the preview, and sends it with the confirmed window.
func (s *Service) answerSend(ctx context.Context, acc access, req *sendRequest, msg *mail.RawMessage, preview confirm.Preview, ans *ConfirmAnswer) (*SendResult, error) {
	recipients := auditRecipients(msg)
	if !ans.Accepted {
		p, err := s.declineAnswer(ctx, acc, req.payload, ans)
		if err != nil {
			s.finish(ctx, acc, toolSend, 1, recipients, "", err)
			return nil, err
		}
		s.recordOutcome(ctx, acc, toolSend, 1, recipients, audit.OutcomeDeclined, "declined")
		return &SendResult{Status: SendDeclined, SendID: p.ID}, nil
	}

	p, err := s.claimAnswer(ctx, acc, req.payload, ans)
	if err != nil {
		s.finish(ctx, acc, toolSend, 1, recipients, "", err)
		return nil, err
	}
	if !samePreview(p.Preview, preview) {
		s.endAction(ctx, acc.Principal.Tenant, p.ID, confirm.StateRejected, "")
		err := errMessageChanged()
		s.finish(ctx, acc, toolSend, 1, recipients, "", err)
		return nil, err
	}
	return s.deliver(ctx, acc, msg, req.draft, acc.Account.Limits.UndoWindowConfirmed, p.ID, detailInBand)
}

// approveSend is ApprovePending for a send.
func (s *Service) approveSend(ctx context.Context, pending *confirm.Pending, payload []byte) error {
	acc, b, err := s.begin(ctx, pending.Account, policy.ActSend, toolSend)
	if err != nil {
		return err
	}
	t := acc.Principal.Tenant
	req, err := decodeSendPayload(payload)
	if err != nil {
		s.finish(ctx, acc, toolSend, 1, nil, "", err)
		return err
	}
	msg, preview, err := s.prepareSend(ctx, b, acc.Account, req)
	if err != nil {
		// The action keeps waiting: the problem may pass (a server that does not
		// answer), or the user may change a limit.
		s.finish(ctx, acc, toolSend, 1, auditRecipients(msg), "", err)
		return err
	}
	recipients := auditRecipients(msg)
	if !samePreview(pending.Preview, preview) {
		// The same payload will not give the message that the user saw, so the
		// action can never be carried out.
		s.endWaiting(ctx, t, pending.ID)
		err := errMessageChanged()
		s.finish(ctx, acc, toolSend, 1, recipients, "", err)
		return err
	}
	if err := s.d.Confirm.Transition(ctx, t, pending.ID, confirm.StatePending, confirm.StateApproved, ""); err != nil {
		s.finish(ctx, acc, toolSend, 1, recipients, "", err)
		return err
	}
	_, err = s.deliver(ctx, acc, msg, req.draft, acc.Account.Limits.UndoWindowConfirmed, pending.ID, detailOnWeb)
	return err
}

// errMessageChanged is the error for a message that, built again, is not the one
// that the preview showed.
func errMessageChanged() error {
	return fmt.Errorf("%w: built again, the message is not the one that the user saw (recipients, sender, subject or text changed). Ask again", confirm.ErrPayloadChanged)
}

// deliver counts the send against the send limit and gives the message to the
// outbox. pendingID is the approved action that this carries out, or "" when
// there was no confirmation. how is the audit code of a success.
//
// When the limiter refuses, or the outbox cannot store the message, nothing is
// sent and the action is rejected. Once the outbox has the message, it owns it,
// and the action is done with the id of the outbox item. The recipients become
// known when the outbox accepted the message ("sent" or "queued").
func (s *Service) deliver(ctx context.Context, acc access, msg *mail.RawMessage, draft mail.Ref, window time.Duration, pendingID, how string) (*SendResult, error) {
	t, id := acc.Principal.Tenant, acc.Account.ID
	recipients := auditRecipients(msg)
	if err := s.d.Limiter.Reserve(ctx, t, id, policy.CountSend, 1, acc.Account.Limits); err != nil {
		s.endAction(ctx, t, pendingID, confirm.StateRejected, "")
		s.finish(ctx, acc, toolSend, 1, recipients, "", err)
		return nil, err
	}
	item, err := s.d.Outbox.Enqueue(ctx, t, id, msg, window, draft)
	if err != nil {
		s.endAction(ctx, t, pendingID, confirm.StateRejected, "")
		s.finish(ctx, acc, toolSend, 1, recipients, "", err)
		return nil, err
	}
	if item.Status == outbox.StatusSent || item.Status == outbox.StatusQueued {
		s.rememberRecipients(ctx, acc, msg.Recipients)
	}
	s.endAction(ctx, t, pendingID, confirm.StateDone, item.ID)
	if item.Status == outbox.StatusFailed {
		s.recordOutcome(ctx, acc, toolSend, 1, recipients, audit.OutcomeError, "send_failed")
	} else {
		s.recordOutcome(ctx, acc, toolSend, 1, recipients, audit.OutcomeOK, how)
	}
	return resultOf(item), nil
}

// resultOf returns the result for an outbox item.
func resultOf(item *outbox.Item) *SendResult {
	res := &SendResult{Status: string(item.Status), SendID: item.ID}
	if item.Status == outbox.StatusQueued {
		at := item.SendAfter
		res.SendAt = &at
	}
	return res
}

// auditRecipients returns the envelope recipients of msg for the audit log, or
// nil when there is no message or when it has more recipients than one entry
// can hold (such a message is refused).
func auditRecipients(msg *mail.RawMessage) []string {
	if msg == nil || len(msg.Recipients) > maxRecipients {
		return nil
	}
	return slices.Clone(msg.Recipients)
}

// ---------------------------------------------------------------- building

// prepareSend builds the message of a request, checks the limits that can never
// allow it, and makes its preview. When the message was built and a limit
// refuses it, the message is returned with the error, for the audit entry.
func (s *Service) prepareSend(ctx context.Context, b mail.Backend, a *account.Account, req *sendRequest) (*mail.RawMessage, confirm.Preview, error) {
	msg, err := s.buildSend(ctx, b, a, req)
	if err != nil {
		return nil, confirm.Preview{}, err
	}
	if err := checkLimit(policy.CountRecipients, a.Limits, len(msg.Recipients)); err != nil {
		return msg, confirm.Preview{}, err
	}
	if err := checkLimit(policy.CountSend, a.Limits, 1); err != nil {
		return msg, confirm.Preview{}, err
	}
	return msg, previewOf(a, msg), nil
}

// buildSend builds the message to send: from the Compose of the request, or from
// the saved draft. A draft must be from the address of the account.
func (s *Service) buildSend(ctx context.Context, b mail.Backend, a *account.Account, req *sendRequest) (*mail.RawMessage, error) {
	if req.compose != nil {
		return s.buildMessage(ctx, b, a, *req.compose, false)
	}
	raw, err := b.GetRaw(ctx, req.draft, maxDraftBytes)
	if err != nil {
		return nil, err
	}
	msg, err := mime.FromDraft(raw)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(msg.From), selfAddress(a)) {
		return nil, ErrDraftNotFromAccount
	}
	return msg, nil
}

// buildMessage builds a new message, a reply or a forward from c. A reply reads
// the headers of the original. A forward reads its text, and its attachments
// when c asks for them. forDraft keeps the Bcc header, for a draft.
//
// The sender is the account: its address, with its display name.
func (s *Service) buildMessage(ctx context.Context, b mail.Backend, a *account.Account, c mail.Compose, forDraft bool) (*mail.RawMessage, error) {
	bc := mime.BuildContext{
		From:     mail.Address{Name: a.DisplayName, Email: a.Address},
		Self:     []string{selfAddress(a)},
		Now:      s.d.Now(),
		ForDraft: forDraft,
	}
	switch {
	case c.ReplyTo != nil && c.ForwardOf != nil:
		return nil, mime.ErrReplyAndForward
	case c.ReplyTo != nil:
		orig, err := b.GetMessage(ctx, c.ReplyTo.Ref, mail.BodyOptions{Format: mail.BodyNone})
		if err != nil {
			return nil, err
		}
		bc.Original = orig
	case c.ForwardOf != nil:
		orig, err := b.GetMessage(ctx, *c.ForwardOf, mail.BodyOptions{Format: mail.BodyText, MaxChars: maxForwardChars})
		if err != nil {
			return nil, err
		}
		bc.Original = orig
		if c.IncludeOriginalAttachments {
			bc.OriginalAttachments, err = originalAttachments(ctx, b, *c.ForwardOf, orig.Attachments)
			if err != nil {
				return nil, err
			}
		}
	}
	return mime.Build(c, bc)
}

// originalAttachments reads the attachments of the original of a forward. All
// together they may be at most 10 MB, the limit of mime.Build. Each read asks for
// at most what is left of the 10 MB, so a server that reports small sizes cannot
// make the service read more.
func originalAttachments(ctx context.Context, b mail.Backend, ref mail.Ref, infos []mail.AttachmentInfo) ([]mail.Attachment, error) {
	tooLarge := fmt.Errorf("%w: the attachments of the original are larger than 10 MB together", mail.ErrTooLarge)
	var listed int64
	for _, info := range infos {
		listed += max(info.Size, 0)
	}
	if listed > maxAttachmentBytes {
		return nil, tooLarge
	}
	out := make([]mail.Attachment, 0, len(infos))
	var total int64
	for _, info := range infos {
		left := maxAttachmentBytes - total
		if left <= 0 {
			return nil, tooLarge
		}
		att, err := b.GetAttachment(ctx, ref, info.ID, left)
		if err != nil {
			return nil, err
		}
		total += int64(len(att.Data))
		if total > maxAttachmentBytes {
			return nil, tooLarge
		}
		out = append(out, *att)
	}
	return out, nil
}

// selfAddress returns the bare address of the account, without spaces.
func selfAddress(a *account.Account) string {
	return strings.TrimSpace(a.Address)
}

// ---------------------------------------------------------------- the payload

// sendRequest is a send request in its canonical form.
type sendRequest struct {
	// payload is what the user confirms: a canonical JSON of the request (see
	// sendPayload).
	payload []byte
	// compose is the message to build, read back from the payload, or nil for a
	// draft.
	compose *mail.Compose
	// draft is the draft to send, or empty.
	draft mail.Ref
}

// sendPayload is the stored form of a send request, version 1:
//
//	{"v":1,"compose":<the message or null>,"draft":<the draft ref or "">}
//
// The encoding is the same for the same request: the fields have a fixed order,
// and a list is always written, empty when there is none. mime.Build adds a new
// Message-ID and Date each time, so the built message cannot be the payload: it
// would never match on the second call.
type sendPayload struct {
	V       int             `json:"v"`
	Compose *composePayload `json:"compose"`
	Draft   string          `json:"draft"`
}

// composePayload is mail.Compose in the payload.
type composePayload struct {
	To                         []string            `json:"to"`
	Cc                         []string            `json:"cc"`
	Bcc                        []string            `json:"bcc"`
	Subject                    string              `json:"subject"`
	Body                       string              `json:"body"`
	HTMLBody                   string              `json:"html_body"`
	ReplyTo                    *replyPayload       `json:"reply_to"`
	ForwardOf                  *string             `json:"forward_of"`
	IncludeOriginalAttachments bool                `json:"include_original_attachments"`
	Attachments                []attachmentPayload `json:"attachments"`
}

type replyPayload struct {
	Ref string `json:"ref"`
	All bool   `json:"all"`
}

type attachmentPayload struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}

// newSendRequest checks a send request and returns its canonical form. Exactly
// one of c and draft must be set, and a draft ref must not be empty.
//
// The message is built later from the Compose that is read back from the
// payload, not from c. So the first call, the second call and the approval page
// all build from the same request, even when c has text that JSON cannot keep
// as it is (invalid UTF-8).
func newSendRequest(c *mail.Compose, draft *mail.Ref) (*sendRequest, error) {
	p := sendPayload{V: sendPayloadVersion}
	switch {
	case c == nil && draft == nil:
		return nil, fmt.Errorf("%w: give a message or a draft to send", ErrInvalidRequest)
	case c != nil && draft != nil:
		return nil, fmt.Errorf("%w: give a message or a draft to send, not both", ErrInvalidRequest)
	case draft != nil && *draft == "":
		return nil, fmt.Errorf("%w: the draft ref is empty", ErrInvalidRequest)
	case c != nil:
		p.Compose = toComposePayload(*c)
	default:
		p.Draft = string(*draft)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: the message cannot be stored", ErrInvalidRequest)
	}
	return decodeSendPayload(data)
}

// decodeSendPayload reads a send payload. It refuses another version, unknown
// fields, data after the JSON, and a payload with both or neither of a message
// and a draft.
func decodeSendPayload(data []byte) (*sendRequest, error) {
	bad := fmt.Errorf("%w: the send payload cannot be read", ErrInvalidRequest)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p sendPayload
	if err := dec.Decode(&p); err != nil {
		return nil, bad
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, bad
	}
	if p.V != sendPayloadVersion || (p.Compose == nil) == (p.Draft == "") {
		return nil, bad
	}
	req := &sendRequest{payload: data, draft: mail.Ref(p.Draft)}
	if p.Compose != nil {
		c := p.Compose.compose()
		req.compose = &c
	}
	return req, nil
}

// toComposePayload turns c into its payload form. Lists that are nil become
// empty, so nil and empty give the same payload.
func toComposePayload(c mail.Compose) *composePayload {
	list := func(in []string) []string {
		if in == nil {
			return []string{}
		}
		return in
	}
	p := &composePayload{
		To:                         list(c.To),
		Cc:                         list(c.Cc),
		Bcc:                        list(c.Bcc),
		Subject:                    c.Subject,
		Body:                       c.Body,
		HTMLBody:                   c.HTMLBody,
		IncludeOriginalAttachments: c.IncludeOriginalAttachments,
		Attachments:                make([]attachmentPayload, len(c.Attachments)),
	}
	if c.ReplyTo != nil {
		p.ReplyTo = &replyPayload{Ref: string(c.ReplyTo.Ref), All: c.ReplyTo.All}
	}
	if c.ForwardOf != nil {
		ref := string(*c.ForwardOf)
		p.ForwardOf = &ref
	}
	for i, a := range c.Attachments {
		data := a.Data
		if data == nil {
			data = []byte{}
		}
		p.Attachments[i] = attachmentPayload{Filename: a.Filename, ContentType: a.ContentType, Data: data}
	}
	return p
}

// compose turns the payload form back into a mail.Compose.
func (p *composePayload) compose() mail.Compose {
	c := mail.Compose{
		To:                         p.To,
		Cc:                         p.Cc,
		Bcc:                        p.Bcc,
		Subject:                    p.Subject,
		Body:                       p.Body,
		HTMLBody:                   p.HTMLBody,
		IncludeOriginalAttachments: p.IncludeOriginalAttachments,
	}
	if p.ReplyTo != nil {
		c.ReplyTo = &mail.ReplyRef{Ref: mail.Ref(p.ReplyTo.Ref), All: p.ReplyTo.All}
	}
	if p.ForwardOf != nil {
		ref := mail.Ref(*p.ForwardOf)
		c.ForwardOf = &ref
	}
	for _, a := range p.Attachments {
		c.Attachments = append(c.Attachments, mail.OutgoingAttachment{Filename: a.Filename, ContentType: a.ContentType, Data: a.Data})
	}
	return c
}

// ---------------------------------------------------------------- the preview

// previewOf makes the preview of a send from the built message: the envelope
// sender, every envelope recipient (so a Reply-To that a stranger set, the people
// that a reply to all adds, and the Bcc are all visible), the subject and the
// start of the text. The subject and the text are cleaned of characters that a
// reader cannot see. Every value is valid UTF-8, so the stored preview reads back
// the same, and the preview of the same message built again is equal to it.
func previewOf(a *account.Account, msg *mail.RawMessage) confirm.Preview {
	subject, body := previewText(msg.Data)
	to := make([]string, len(msg.Recipients))
	for i, r := range msg.Recipients {
		to[i] = validText(r)
	}
	return confirm.Preview{
		Account:   validText(a.Address),
		From:      validText(msg.From),
		To:        to,
		Subject:   subject,
		BodyStart: firstRunes(body, previewChars),
		Count:     1,
	}
}

// previewText returns the subject and the text of a message. A message with only
// HTML gives the text of its HTML.
func previewText(data []byte) (subject, body string) {
	if m, err := netmail.ReadMessage(bytes.NewReader(data)); err == nil {
		subject = mime.CleanUntrusted(mime.DecodeHeaderValue(m.Header.Get("Subject")))
	}
	parsed, _ := mime.ParseMessage(bytes.NewReader(data), previewParseChars)
	if parsed == nil {
		return subject, ""
	}
	text := parsed.Text
	if text == "" && parsed.HTML != "" {
		if t, err := mime.HTMLToText(parsed.HTML); err == nil {
			text = t
		}
	}
	return subject, mime.CleanUntrusted(text)
}

// samePreview reports whether a message built again is the message of the
// stored preview: the same sender, the same recipients (compared without case
// and order), the same subject and the same start of the text.
func samePreview(stored, rebuilt confirm.Preview) bool {
	return stored.From == rebuilt.From &&
		stored.Subject == rebuilt.Subject &&
		stored.BodyStart == rebuilt.BodyStart &&
		slices.Equal(addressSet(stored.To), addressSet(rebuilt.To))
}

// addressSet returns the addresses in lower case, sorted.
func addressSet(list []string) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = strings.ToLower(strings.TrimSpace(a))
	}
	slices.Sort(out)
	return out
}

// validText returns s with each run of invalid UTF-8 replaced by U+FFFD.
func validText(s string) string {
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// firstRunes returns the first n characters of s, or s if it is not longer.
func firstRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// ---------------------------------------------------------------- known recipients

// recipientHashes returns the keyed hash of each address, each address once.
// The hash is HMAC-SHA-256 of the lower-cased address, with a key that is the
// HMAC-SHA-256 of knownRecipientsLabel under the server key. Without the server
// key, nobody can test an address against the stored hashes.
func (s *Service) recipientHashes(ctx context.Context, addresses []string) ([][]byte, error) {
	serverKey, err := s.d.Vault.ServerKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: read the server key: %w", err)
	}
	defer clear(serverKey)
	km := hmac.New(sha256.New, serverKey)
	_, _ = km.Write([]byte(knownRecipientsLabel)) // a write to a hash never fails
	key := km.Sum(nil)
	defer clear(key)

	seen := make(map[string]bool, len(addresses))
	hashes := make([][]byte, 0, len(addresses))
	for _, addr := range addresses {
		norm := strings.ToLower(strings.TrimSpace(addr))
		if seen[norm] {
			continue
		}
		seen[norm] = true
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(norm))
		hashes = append(hashes, h.Sum(nil))
	}
	return hashes, nil
}

// hasNewRecipient reports whether the account has never sent to one of the
// addresses.
func (s *Service) hasNewRecipient(ctx context.Context, acc access, addresses []string) (bool, error) {
	hashes, err := s.recipientHashes(ctx, addresses)
	if err != nil {
		return false, err
	}
	known, err := s.d.Store.KnownRecipients(ctx, acc.Principal.Tenant, acc.Account.ID, hashes)
	if err != nil {
		return false, fmt.Errorf("service: look up the known recipients: %w", err)
	}
	return slices.Contains(known, false), nil
}

// rememberRecipients records that the account has sent to the addresses. It
// runs after the outbox has accepted the message, also when the caller has left,
// and whether or not the new-recipient check is on (so turning it on later does
// not ask about people the account wrote to before). A failure is logged with a
// code: the next send to the address then asks again, which is the safe side.
func (s *Service) rememberRecipients(ctx context.Context, acc access, addresses []string) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endTimeout)
	defer cancel()
	hashes, err := s.recipientHashes(wctx, addresses)
	if err == nil {
		err = s.d.Store.AddKnownRecipients(wctx, acc.Principal.Tenant, acc.Account.ID, hashes, s.d.Now())
	}
	if err != nil {
		s.d.Logger.Error("service: the recipients of a send were not recorded", "code", "known_recipients_write_failed")
	}
}

// ---------------------------------------------------------------- cancel and status

// CancelSend cancels a send. It needs Mail 4.
//
// A queued send is cancelled in its undo window. outbox.ErrTooLate means that it
// is being sent, was sent, or failed. A send that waits for the user is
// withdrawn (rejected); confirm.ErrNotPending means that it does not wait any
// more. A send of another account, or an id that does not exist, gives
// ErrSendNotFound.
func (s *Service) CancelSend(ctx context.Context, id account.AccountID, sendID string) error {
	if err := s.sendDeps(); err != nil {
		return err
	}
	acc, err := s.authorizeWrite(ctx, id, policy.ActSend, toolCancelSend)
	if err != nil {
		return err
	}
	t := acc.Principal.Tenant

	item, err := s.d.Outbox.Get(ctx, t, sendID)
	switch {
	case err == nil && item.Account == id:
		err = s.d.Outbox.Cancel(ctx, t, sendID)
		s.finish(ctx, acc, toolCancelSend, 1, nil, "", err)
		return err
	case err != nil && !isMissingItem(err):
		s.finish(ctx, acc, toolCancelSend, 1, nil, "", err)
		return err
	}

	// Not an outbox item of this account: it may be a send that waits for the
	// user. The agent may withdraw it.
	if _, err = s.pendingSend(ctx, t, id, sendID); err == nil {
		err = s.d.Confirm.Transition(ctx, t, sendID, confirm.StatePending, confirm.StateRejected, "")
	}
	s.finish(ctx, acc, toolCancelSend, 1, nil, "", err)
	return err
}

// SendStatus returns the status of a send. It needs Mail 1.
//
// The id may name an outbox item or an action that waits for the user. An
// action gives "pending_approval" (with the approval link), "sending" while the
// server carries it out, "declined", "expired", or, when it is done, the status
// of its outbox item. A send of another account, or an id that does not exist,
// gives ErrSendNotFound.
func (s *Service) SendStatus(ctx context.Context, id account.AccountID, sendID string) (*SendResult, error) {
	acc, err := s.authorize(ctx, id, policy.ActRead)
	if err != nil {
		return nil, err
	}
	if s.d.Outbox == nil || s.d.Confirm == nil {
		return nil, errors.New("service: the outbox and the confirm manager must be set up to read a send status")
	}
	t := acc.Principal.Tenant

	if res, err := s.outboxStatus(ctx, t, id, sendID); !errors.Is(err, ErrSendNotFound) {
		return res, err
	}
	p, err := s.pendingSend(ctx, t, id, sendID)
	if err != nil {
		return nil, err
	}
	switch p.State {
	case confirm.StatePending:
		return &SendResult{Status: SendPendingApproval, SendID: p.ID, ApprovalLink: s.approvalLink(ctx, p.ID)}, nil
	case confirm.StateApproved:
		return &SendResult{Status: SendSending, SendID: p.ID}, nil
	case confirm.StateRejected:
		return &SendResult{Status: SendDeclined, SendID: p.ID}, nil
	case confirm.StateExpired:
		return &SendResult{Status: SendExpired, SendID: p.ID}, nil
	default: // done
		return s.outboxStatus(ctx, t, id, p.ResultID)
	}
}

// outboxStatus returns the result of an outbox item of the account, or
// ErrSendNotFound.
func (s *Service) outboxStatus(ctx context.Context, t account.TenantID, id account.AccountID, itemID string) (*SendResult, error) {
	item, err := s.d.Outbox.Get(ctx, t, itemID)
	switch {
	case err == nil && item.Account == id:
		return resultOf(item), nil
	case err == nil || isMissingItem(err):
		return nil, ErrSendNotFound
	default:
		return nil, err
	}
}

// pendingSend returns the send action of the account with this id, or
// ErrSendNotFound.
func (s *Service) pendingSend(ctx context.Context, t account.TenantID, id account.AccountID, pendingID string) (*confirm.Pending, error) {
	p, _, err := s.d.Confirm.Get(ctx, t, pendingID)
	switch {
	case errors.Is(err, confirm.ErrNotFound):
		return nil, ErrSendNotFound
	case err != nil:
		return nil, err
	case p.Account != id || p.Kind != confirm.KindSend:
		return nil, ErrSendNotFound
	}
	return p, nil
}

// isMissingItem reports whether an error of the outbox means that there is no
// such item: none with this id, or an id that cannot exist.
func isMissingItem(err error) bool {
	return errors.Is(err, outbox.ErrNotFound) || errors.Is(err, store.ErrInvalidID)
}
