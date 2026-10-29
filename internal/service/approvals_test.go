package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// The approvals page lists only the actions of the tenant that wait for the
// user on the web page.
func TestPendingApprovalsListsOnlyWaitingApprovals(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()
	c := compose("bob@example.com")

	old, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	env.clock.Advance(confirm.ApprovalLifetime + time.Millisecond)
	waiting, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Trash(ctx, a.ID, []mail.Ref{"r1"}, ConfirmFallback, nil)
	require.NoError(t, err)
	trash := env.svc.mustOnlyApproval(t, ctx, confirm.KindTrash)
	require.NoError(t, env.svc.RejectPending(env.webCtx(), trash))
	env.f.addTenant("other")
	theirs := env.f.addAccount("other", "them@example.com", offlineSettings(), "pw")
	otherCtx := WithPrincipal(context.Background(), Principal{Tenant: "other", Client: "web"})
	_, _, err = env.svc.Send(otherCtx, theirs.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	list, err := env.svc.PendingApprovals(env.webCtx())
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, waiting.SendID, list[0].ID)
	require.NotEqual(t, old.SendID, list[0].ID, "an expired action is not listed")

	_, err = env.svc.PendingApprovals(context.Background())
	require.ErrorIs(t, err, ErrNoPrincipal)
}

// mustOnlyApproval returns the id of the one waiting approval of the kind.
func (s *Service) mustOnlyApproval(t *testing.T, ctx context.Context, kind confirm.Kind) string {
	t.Helper()
	list, err := s.PendingApprovals(ctx)
	require.NoError(t, err)
	var ids []string
	for _, p := range list {
		if p.Kind == kind {
			ids = append(ids, p.ID)
		}
	}
	require.Len(t, ids, 1)
	return ids[0]
}

// The approval page shows the whole message that the user approves: every
// envelope recipient (the Bcc too), the subject, every attachment and the whole
// text that the builder makes, all cleaned of hidden characters.
func TestPendingDetailShowsTheWholeMessage(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	long := strings.Repeat("All of this text is part of the message. ", 40) + "THE-END"
	c := mail.Compose{
		To:      []string{"bob@example.com"},
		Cc:      []string{"carol@example.com"},
		Bcc:     []string{"hidden@example.com"},
		Subject: "Plans\u202e for the week",
		Body:    "Hello\u200b Bob.\n" + long,
		Attachments: []mail.OutgoingAttachment{
			{Filename: "figures.xlsx", ContentType: "application/octet-stream", Data: []byte("1,2,3")},
			{Filename: "", ContentType: "text/plain", Data: []byte("x")},
		},
	}
	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	p, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)
	require.NoError(t, err)
	require.Equal(t, res.SendID, p.ID)
	require.Equal(t, confirm.StatePending, p.State)
	require.NotNil(t, detail)
	require.Equal(t, "me@example.com", detail.From)
	require.ElementsMatch(t, []string{"bob@example.com", "carol@example.com", "hidden@example.com"}, detail.Recipients)
	require.Equal(t, "Plans for the week", detail.Subject)
	require.Equal(t, []string{"figures.xlsx", "attachment"}, detail.Attachments)
	require.Contains(t, detail.Body, "Hello Bob.")
	require.Contains(t, detail.Body, "THE-END", "the whole text, not the first 500 characters")
	require.Greater(t, len([]rune(detail.Body)), 500)
	require.NotContains(t, detail.Body, "\u200b")
	require.Equal(t, "Plans for the week", p.Preview.Subject)
}

// The text of an HTML message is the text that the recipient sees. No markup is
// left in it.
func TestPendingDetailOfAnHTMLMessageIsText(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := mail.Compose{
		To:       []string{"bob@example.com"},
		Subject:  "Hi",
		HTMLBody: `<p>Hello <b>Bob</b></p><script>alert("x")</script><img src="https://tracker.example/p.png">`,
	}
	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	_, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)
	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Contains(t, detail.Body, "Hello Bob")
	for _, markup := range []string{"<p>", "<b>", "<script", "alert", "<img", "tracker.example"} {
		require.NotContains(t, detail.Body, markup)
	}
}

// The detail is only for the actions of the tenant that wait for the web page.
func TestPendingDetailIsOnlyForApprovalsOfTheTenant(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.PendingDetail(env.webCtx(), needs.Pending.ID)
	require.ErrorIs(t, err, confirm.ErrNotFound, "an in-band action is not shown")

	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	env.f.addTenant("other")
	otherCtx := WithPrincipal(context.Background(), Principal{Tenant: "other", Client: "web"})
	_, _, err = env.svc.PendingDetail(otherCtx, res.SendID)
	require.ErrorIs(t, err, confirm.ErrNotFound)
	_, _, err = env.svc.PendingDetail(env.webCtx(), "no-such-action")
	require.ErrorIs(t, err, confirm.ErrNotFound)
	_, _, err = env.svc.PendingDetail(context.Background(), res.SendID)
	require.ErrorIs(t, err, ErrNoPrincipal)

	// A Trash action has no message. An action that has ended has no preview.
	_, _, err = env.svc.Trash(env.ctx(), a.ID, []mail.Ref{"r1"}, ConfirmFallback, nil)
	require.NoError(t, err)
	trash := env.svc.mustOnlyApproval(t, env.webCtx(), confirm.KindTrash)
	p, detail, err := env.svc.PendingDetail(env.webCtx(), trash)
	require.NoError(t, err)
	require.Nil(t, detail)
	require.Equal(t, 1, p.Preview.Count)

	require.NoError(t, env.svc.RejectPending(env.webCtx(), res.SendID))
	p, detail, err = env.svc.PendingDetail(env.webCtx(), res.SendID)
	require.NoError(t, err)
	require.Nil(t, detail)
	require.Equal(t, confirm.StateRejected, p.State)
	require.Empty(t, env.sender.sent(), "showing an action never sends it")
}
