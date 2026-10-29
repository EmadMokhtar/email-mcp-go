package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// isClosed reports whether ch is closed within d.
func isClosed(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// While the program stops, the service starts no new send, Trash move or
// approval: the store and the backends close soon. Nothing changes, so an
// action that waits for approval still waits. Undo (CancelSend), the status
// and reads keep working, because the outbox still drains.
func TestStopRefusesNewActionsButKeepsUndoAndReads(t *testing.T) {
	env := newWriteEnv(t)
	confirming := env.account()
	auto := env.f.addAccount(env.f.tenant, "auto@example.com", offlineSettings(), "pw", autonomy)
	ctx := env.ctx()
	c := compose("bob@example.com")
	waiting, _, err := env.svc.Send(ctx, confirming.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	queued, _, err := env.svc.Send(ctx, auto.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Status)

	idle := env.svc.Stop()
	require.True(t, isClosed(idle, time.Second), "no action runs")

	_, _, err = env.svc.Send(ctx, auto.ID, &c, nil, ConfirmInBand, nil)
	require.ErrorIs(t, err, ErrStopping)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "stopping")
	require.ErrorIs(t, env.svc.ApprovePending(ctx, waiting.SendID), ErrStopping)
	requireEntry(t, env.lastEntry(), "approve_pending", audit.OutcomeRefused, "stopping")
	require.Equal(t, confirm.StatePending, env.pending(waiting.SendID).State, "the action still waits")
	_, _, err = env.svc.Trash(ctx, auto.ID, []mail.Ref{"INBOX:1"}, ConfirmInBand, nil)
	require.ErrorIs(t, err, ErrStopping)
	requireEntry(t, env.lastEntry(), "trash_messages", audit.OutcomeRefused, "stopping")
	require.Empty(t, env.backend.trashCalls())
	require.Empty(t, env.sender.sent())

	require.NoError(t, env.svc.CancelSend(ctx, auto.ID, queued.SendID), "undo still works")
	status, err := env.svc.SendStatus(ctx, auto.ID, queued.SendID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", status.Status)
	folders, err := env.svc.ListFolders(ctx, auto.ID)
	require.NoError(t, err, "reads still work")
	require.NotEmpty(t, folders)

	require.Equal(t, idle, env.svc.Stop(), "a second Stop returns the same channel")
}

// Stop does not wait itself, but its channel closes only when the actions that
// run have ended: a send without an undo window runs inside its action.
func TestStopWaitsForARunningSend(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) { a.Limits.UndoWindowAuto = 0 })
	env.sender.started = make(chan struct{}, 1)
	env.sender.release = make(chan struct{})
	c := compose("bob@example.com")
	done := make(chan error, 1)
	go func() {
		_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		done <- err
	}()
	<-env.sender.started

	idle := env.svc.Stop()
	require.False(t, isClosed(idle, 100*time.Millisecond), "the send still runs")

	close(env.sender.release)
	require.NoError(t, <-done)
	require.True(t, isClosed(idle, time.Second))
	require.Len(t, env.sender.sent(), 1)
}
