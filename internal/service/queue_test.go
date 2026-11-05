package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
)

// queueSend queues a message of the account in the outbox.
func (e *writeEnv) queueSend(tenant account.TenantID, a *account.Account) string {
	e.t.Helper()
	item, err := e.outbox.Enqueue(context.Background(), tenant, a.ID, &mail.RawMessage{
		From: a.Address, Recipients: []string{"secret-rcpt@example.com"}, Data: []byte("Subject: secret-subject\r\n\r\nhello\r\n"),
	}, time.Minute, "")
	require.NoError(e.t, err)
	return item.ID
}

// The user stops a queued send after the level went down. The agent cannot do
// this any more at that level, so the call must not depend on the level.
func TestUserCancelsAQueuedSendAtAnyLevel(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	id := env.queueSend(env.f.tenant, a)
	a.Levels[account.AreaMail] = account.MailOff
	require.NoError(t, env.f.store.UpdateAccount(context.Background(), a))

	require.NoError(t, env.svc.CancelQueuedSend(env.webCtx(), id))

	requireEntry(t, env.lastEntry(), "cancel_send", audit.OutcomeOK, "cancelled_by_user")
	require.Equal(t, a.ID, env.lastEntry().Account)
	require.Equal(t, "web", env.lastEntry().Client)
	item, err := env.outbox.Get(context.Background(), env.f.tenant, id)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusCancelled, item.Status)

	env.clock.Advance(time.Hour)
	require.NoError(t, env.outbox.Drain(context.Background(), time.Second))
	require.Empty(t, env.sender.sent(), "nothing goes out")
}

// A second press of the button changes nothing and writes no second entry.
func TestCancelQueuedSendTwiceWritesOneEntry(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	id := env.queueSend(env.f.tenant, a)
	require.NoError(t, env.svc.CancelQueuedSend(env.webCtx(), id))
	before := len(env.entries())

	require.NoError(t, env.svc.CancelQueuedSend(env.webCtx(), id))

	require.Len(t, env.entries(), before)
}

// The send of another tenant answers exactly like an id that does not exist,
// and it stays queued.
func TestCancelQueuedSendOfAnotherTenantLooksMissing(t *testing.T) {
	env := newWriteEnv(t)
	env.f.addTenant("t2")
	theirs := env.f.addAccount("t2", "them@example.com", offlineSettings(), "pw", autonomy)
	id := env.queueSend("t2", theirs)
	ctx := env.webCtx()

	otherErr := env.svc.CancelQueuedSend(ctx, id)
	missingErr := env.svc.CancelQueuedSend(ctx, "no-such-id")

	require.ErrorIs(t, otherErr, ErrSendNotFound)
	require.ErrorIs(t, missingErr, ErrSendNotFound)
	require.Equal(t, missingErr.Error(), otherErr.Error())
	requireEntry(t, env.lastEntry(), "cancel_send", audit.OutcomeRefused, "send_not_found")
	require.Empty(t, env.lastEntry().Account, "the entry does not name an account of another tenant")
	item, err := env.outbox.Get(context.Background(), "t2", id)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusQueued, item.Status)

	require.ErrorIs(t, env.svc.CancelQueuedSend(ctx, "bad|id"), ErrSendNotFound)
}

func TestCancelQueuedSendTooLate(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) { a.Limits.UndoWindowAuto = 0 })
	c := compose("bob@example.com")
	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "sent", res.Status)

	err = env.svc.CancelQueuedSend(env.webCtx(), res.SendID)

	require.ErrorIs(t, err, outbox.ErrTooLate)
	requireEntry(t, env.lastEntry(), "cancel_send", audit.OutcomeError, "too_late")
	require.Equal(t, a.ID, env.lastEntry().Account)
}

func TestCancelQueuedSendNeedsAPrincipal(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	id := env.queueSend(env.f.tenant, a)

	require.ErrorIs(t, env.svc.CancelQueuedSend(context.Background(), id), ErrNoPrincipal)

	item, err := env.outbox.Get(context.Background(), env.f.tenant, id)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusQueued, item.Status)
}

// While the program stops, the user can still cancel: the worker drains the
// queue, and a cancelled send is not part of it.
func TestUserCancelsAQueuedSendWhileTheProgramStops(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	id := env.queueSend(env.f.tenant, a)
	env.svc.Stop()

	require.NoError(t, env.svc.CancelQueuedSend(env.webCtx(), id))

	item, err := env.outbox.Get(context.Background(), env.f.tenant, id)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusCancelled, item.Status)
}

// The list holds the sends of the tenant that wait, with the address of the
// account. It holds nothing from the sealed message.
func TestQueuedSendsListsTheSendsOfTheTenant(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	id := env.queueSend(env.f.tenant, a)
	done := env.queueSend(env.f.tenant, a)
	require.NoError(t, env.svc.CancelQueuedSend(env.webCtx(), done))
	env.f.addTenant("t2")
	env.queueSend("t2", env.f.addAccount("t2", "them@example.com", offlineSettings(), "pw", autonomy))

	list, err := env.svc.QueuedSends(env.webCtx())

	require.NoError(t, err)
	require.Equal(t, []QueuedSend{{
		ID: id, Account: a.ID, Address: "me@example.com", GoesOut: testStart.Add(time.Minute),
	}}, list)
	_, err = env.svc.QueuedSends(context.Background())
	require.ErrorIs(t, err, ErrNoPrincipal)
}

// The limit is the service's own, so a list cannot grow without end.
func TestQueuedSendsHasALimit(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	for range MaxQueuedSends + 1 {
		env.queueSend(env.f.tenant, a)
	}

	list, err := env.svc.QueuedSends(env.webCtx())

	require.NoError(t, err)
	require.Len(t, list, MaxQueuedSends)
}
