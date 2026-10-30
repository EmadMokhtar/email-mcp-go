package service

// This file holds what the approvals page of the web interface shows: the list
// of the actions that wait for the user there, and the detail of one action.
// Approving and rejecting are in write.go.

import (
	"bytes"
	"context"
	"errors"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// SendDetail is the whole message of a send that waits for approval, as the
// approvals page shows it. It is built from the stored request in the same way
// as the approval builds it. Every text is cleaned with mime.CleanUntrusted.
// The page must show each one as text, and never the HTML of the message.
type SendDetail struct {
	// From is the envelope sender.
	From string
	// Recipients are the envelope recipients: To, Cc and Bcc, and the people
	// that a reply to all adds.
	Recipients []string
	Subject    string
	// Attachments are the file names of the attachments, in the order of the
	// message.
	Attachments []string
	// Body is the whole text that the recipient sees: the text of the HTML
	// when the message has HTML.
	Body string
}

// PendingApprovals returns the actions of the tenant of the principal that wait
// for the user on the web page, newest first: only the actions of the mode
// confirm.ModeApproval that still wait. An action that the user answers in the
// chat, and an action that has ended or expired, is not in the list. The texts
// of the previews are cleaned with mime.CleanUntrusted.
//
// Only the web page may call it. No MCP tool may show these actions to the
// agent.
func (s *Service) PendingApprovals(ctx context.Context) ([]*confirm.Pending, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Confirm == nil {
		return nil, errors.New("service: the confirm manager must be set up to list the approvals")
	}
	all, err := s.d.Confirm.ListPending(ctx, p.Tenant)
	if err != nil {
		return nil, err
	}
	list := make([]*confirm.Pending, 0, len(all))
	for _, pending := range all {
		if pending.Mode != confirm.ModeApproval || pending.State != confirm.StatePending {
			continue
		}
		pending.Preview = cleanPreview(pending.Preview)
		list = append(list, pending)
	}
	return list, nil
}

// PendingDetail returns an action of the tenant of the principal, for the
// approval page. It changes nothing and sends nothing: it is only for display.
//
// It returns confirm.ErrNotFound when the tenant has no such action, and also
// for an action of the mode confirm.ModeInBand: the user answers that one in the
// chat, so the web page does not show it. An action that has ended is returned
// with its state and without its preview.
//
// For a send that still waits, it also returns the whole message (see
// SendDetail). The service builds the message again from the stored request
// with the function that Approve uses, and returns it only when it is still the
// message of the stored preview. When it cannot build it (for example the mail
// server does not answer for a reply, or a recipient cannot be shown exactly),
// the detail is nil: the page then shows the stored preview, and Approve fails
// in the same way, so nothing is approved from a partial view. The texts of the
// preview are cleaned with mime.CleanUntrusted too.
//
// Only the web page may call it. No MCP tool may show the detail to the agent.
func (s *Service) PendingDetail(ctx context.Context, id string) (*confirm.Pending, *SendDetail, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return nil, nil, err
	}
	if s.d.Confirm == nil {
		return nil, nil, errors.New("service: the confirm manager must be set up to show an approval")
	}
	pending, payload, err := s.d.Confirm.Get(ctx, p.Tenant, id)
	if err != nil {
		return nil, nil, err
	}
	if pending.Mode != confirm.ModeApproval {
		return nil, nil, confirm.ErrNotFound
	}
	// The message built again is compared with the preview as it was stored.
	// Only the copy for the page is cleaned.
	stored := pending.Preview
	pending.Preview = cleanPreview(stored)
	if pending.Kind != confirm.KindSend || pending.State != confirm.StatePending || payload == nil {
		return pending, nil, nil
	}
	detail, err := s.sendDetail(ctx, pending.Account, stored, payload)
	clear(payload)
	if err != nil {
		// The page still shows the stored preview, so the user can reject the
		// action. The log gets a fixed code and the Go types of the error only.
		s.d.Logger.Warn("service: the message of an approval could not be built",
			"code", "pending_detail_failed", "error_types", errtype.Chain(err))
		return pending, nil, nil
	}
	return pending, detail, nil
}

// sendDetail builds the message of a send action again with rebuildApproval,
// the function that Approve uses too, and returns what the approval page shows
// of it. stored is the preview as it was stored. It needs the account to allow
// reading, because a reply or a forward reads the original, and a draft is read
// in full.
func (s *Service) sendDetail(ctx context.Context, id account.AccountID, stored confirm.Preview, payload []byte) (*SendDetail, error) {
	acc, err := s.authorize(ctx, id, policy.ActRead)
	if err != nil {
		return nil, err
	}
	b, err := s.d.Backends.Mail(ctx, acc.Account)
	if err != nil {
		return nil, err
	}
	_, _, detail, err := s.rebuildApproval(ctx, b, acc.Account, stored, payload)
	s.noteBackendError(ctx, acc.Account, err)
	return detail, err
}

// detailOf returns what the approval page shows of a built message: every text
// cleaned with mime.CleanUntrusted. The body is the whole text, as the
// recipient sees it.
func detailOf(built *builtSend) *SendDetail {
	msg := built.msg
	return &SendDetail{
		From:        mime.CleanUntrusted(msg.From),
		Recipients:  cleanAll(msg.Recipients),
		Subject:     previewSubject(msg.Data),
		Attachments: cleanAll(built.content.attachments),
		Body:        mime.CleanUntrusted(built.content.body),
	}
}

// composedText returns the whole text part of a message that mime.Build made,
// cleaned with mime.CleanUntrusted as a whole.
func composedText(data []byte) string {
	parsed, _ := mime.ParseMessage(bytes.NewReader(data), 0)
	if parsed == nil {
		return ""
	}
	return mime.CleanUntrusted(parsed.Text)
}

// cleanPreview returns the preview with each text cleaned with
// mime.CleanUntrusted.
func cleanPreview(p confirm.Preview) confirm.Preview {
	p.Account = mime.CleanUntrusted(p.Account)
	p.From = mime.CleanUntrusted(p.From)
	p.To = cleanAll(p.To)
	p.Subject = mime.CleanUntrusted(p.Subject)
	p.BodyStart = mime.CleanUntrusted(p.BodyStart)
	p.Attachments = cleanAll(p.Attachments)
	p.Messages = cleanAll(p.Messages)
	return p
}

// cleanAll returns a new list with each text cleaned with mime.CleanUntrusted. A
// nil list stays nil.
func cleanAll(list []string) []string {
	if list == nil {
		return nil
	}
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = mime.CleanUntrusted(v)
	}
	return out
}
