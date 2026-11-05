package service

// This file holds what the user can do with the sends that wait in the outbox.
// The user, never the agent, calls these use cases from the activity page and
// from the "outbox" command: no MCP tool reaches them.
//
// The agent has cancel_send, which needs Mail 4. These use cases need no level:
// the user must be able to stop a send after lowering the level. They work on
// the tenant of the caller, and only on what the outbox keeps in plain form. The
// sealed message is never opened, so the list has no subject and no recipients.

import (
	"context"
	"errors"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
)

// MaxQueuedSends is the most sends that QueuedSends returns.
const MaxQueuedSends = 200

// detailCancelledByUser is the audit detail of a send that the user cancelled.
const detailCancelledByUser = "cancelled_by_user"

// errNoQueue means that the service has no outbox or no audit log. A send must
// not be cancelled without its audit entry.
var errNoQueue = errors.New("service: the outbox and the audit log must be set up to use the queue of sends")

// QueuedSend is a send that waits in the outbox and can still be cancelled.
type QueuedSend struct {
	ID      string
	Account account.AccountID
	// Address is the address of the account.
	Address string
	// GoesOut is the earliest time at which the message can go out.
	GoesOut time.Time
	// Attempts is the number of tries that failed. A send with failed tries
	// waits for its next try, not for its undo window.
	Attempts int
}

// QueuedSends returns the sends of the tenant of the caller that wait in the
// outbox, the one that goes out first at the start, and at most MaxQueuedSends.
func (s *Service) QueuedSends(ctx context.Context) ([]QueuedSend, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Outbox == nil || s.d.Audit == nil {
		return nil, errNoQueue
	}
	items, err := s.d.Outbox.ListQueued(ctx, p.Tenant, MaxQueuedSends)
	if err != nil {
		return nil, err
	}
	accounts, err := s.d.Store.ListAccounts(ctx, p.Tenant)
	if err != nil {
		return nil, err
	}
	addresses := make(map[account.AccountID]string, len(accounts))
	for _, a := range accounts {
		addresses[a.ID] = a.Address
	}
	list := make([]QueuedSend, 0, len(items))
	for _, item := range items {
		list = append(list, QueuedSend{
			ID: item.ID, Account: item.Account, Address: addresses[item.Account],
			GoesOut: item.NextTry, Attempts: item.Attempts,
		})
	}
	return list, nil
}

// CancelQueuedSend cancels a send of the tenant of the caller that waits in the
// outbox. The send can be of any account of the tenant, at any level.
//
// A send that does not exist in the tenant gives ErrSendNotFound, and the entry
// in the log then names no account. outbox.ErrTooLate means that the send is
// being sent, was sent, or failed. A send that is cancelled already is left as
// it is, with no new entry in the log.
func (s *Service) CancelQueuedSend(ctx context.Context, sendID string) error {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return err
	}
	if s.d.Outbox == nil || s.d.Audit == nil {
		return errNoQueue
	}
	item, err := s.d.Outbox.Get(ctx, p.Tenant, sendID)
	switch {
	case isMissingItem(err):
		s.recordUserCancel(ctx, p, "", ErrSendNotFound)
		return ErrSendNotFound
	case err != nil:
		return err
	case item.Status == outbox.StatusCancelled:
		return nil
	}
	err = s.d.Outbox.Cancel(ctx, p.Tenant, sendID)
	if isMissingItem(err) {
		// The account was removed between the two calls, and its queue with it.
		err = ErrSendNotFound
	}
	s.recordUserCancel(ctx, p, item.Account, err)
	return err
}

// recordUserCancel records the result of a cancel of the user. The cancel has
// happened by now, so a caller that left does not lose the entry.
func (s *Service) recordUserCancel(ctx context.Context, p Principal, id account.AccountID, err error) {
	e := entry{tool: toolCancelSend, account: id, count: 1, outcome: audit.OutcomeOK, detail: detailCancelledByUser}
	if err != nil {
		e.outcome, e.detail = auditCode(err)
	}
	s.record(context.WithoutCancel(ctx), p, e)
}
