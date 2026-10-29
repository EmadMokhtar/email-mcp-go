package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// webCtx returns the context of a web request of the local tenant: the
// principal has no scope limit and the client "web".
func (e *writeEnv) webCtx() context.Context {
	return WithPrincipal(context.Background(), Principal{Tenant: e.f.tenant, Client: "web"})
}

// The service stores the mode from the path that it took: the in-band path
// asks in the chat, the fallback path gives an approval link.
func TestAskStoresTheMode(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, confirm.ModeInBand, needs.Pending.Mode)
	require.Equal(t, confirm.ModeInBand, env.pending(needs.Pending.ID).Mode)

	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	require.Equal(t, confirm.ModeApproval, env.pending(res.SendID).Mode)

	_, tneeds, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"r1"}, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, confirm.ModeInBand, env.pending(tneeds.Pending.ID).Mode)
	tres, _, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"r2"}, ConfirmFallback, nil)
	require.NoError(t, err)
	require.Equal(t, TrashPendingApproval, tres.Status)
	modes := map[confirm.Mode]int{}
	for _, p := range env.waitingActions() {
		modes[p.Mode]++
	}
	require.Equal(t, map[confirm.Mode]int{confirm.ModeInBand: 2, confirm.ModeApproval: 2}, modes)
}

// An action that waits for an answer in the chat cannot be approved or
// rejected on the web page. The refusal is in the log, and the action still
// waits for its answer in the chat.
func TestApproveAndRejectRefuseAnInBandAction(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")
	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	id := needs.Pending.ID

	for name, call := range map[string]func(context.Context, string) error{
		"approve": env.svc.ApprovePending,
		"reject":  env.svc.RejectPending,
	} {
		err := call(env.webCtx(), id)
		require.ErrorIs(t, err, ErrAnswerInBand, name)
		require.ErrorIs(t, err, confirm.ErrNotPending, name)
		last := env.lastEntry()
		requireEntry(t, last, "send_message", audit.OutcomeRefused, "in_band_only")
		require.Equal(t, a.ID, last.Account)
	}
	require.Equal(t, confirm.StatePending, env.pending(id).State)
	require.Empty(t, env.sender.sent())

	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err, "the answer in the chat still works")
	require.Equal(t, SendSent, res.Status)
}

// SendStatus gives an approval link only for an action that the web page can
// approve.
func TestSendStatusGivesNoLinkForAnInBandAction(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose("bob@example.com")
	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	got, err := env.svc.SendStatus(env.ctx(), a.ID, needs.Pending.ID)
	require.NoError(t, err)
	require.Equal(t, SendPendingApproval, got.Status)
	require.Empty(t, got.ApprovalLink, "the web page cannot approve it")
}
