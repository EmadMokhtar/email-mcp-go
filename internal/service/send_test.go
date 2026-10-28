package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// compose returns a short new message to the given addresses.
func compose(to ...string) mail.Compose {
	return mail.Compose{To: to, Subject: "Plans", Body: "See you at noon."}
}

// accept and decline answer a confirmation in band.
func accept(n *NeedsConfirmation) *ConfirmAnswer {
	return &ConfirmAnswer{RequestState: n.RequestState, Accepted: true}
}

func decline(n *NeedsConfirmation) *ConfirmAnswer {
	return &ConfirmAnswer{RequestState: n.RequestState, Accepted: false}
}

// draftMail returns a saved draft from the given address, with a Bcc.
func draftMail(from string) []byte {
	return crlf("From: Me <" + from + ">\n" +
		"To: Ann <ann@example.com>\n" +
		"Bcc: hidden@example.com\n" +
		"Subject: Draft plans\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"Message-ID: <d1@example.com>\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" +
		"Draft body\n")
}

// original returns a received message that a reply can answer.
func original(replyTo ...mail.Address) *mail.Message {
	return &mail.Message{
		Summary: mail.Summary{
			Ref:     "o1",
			From:    mail.Address{Name: "Alice", Email: "alice@example.com"},
			To:      []mail.Address{{Email: "me@example.com"}, {Email: "dan@example.com"}},
			Subject: "Lunch",
		},
		Cc:        []mail.Address{{Email: "erin@example.com"}},
		ReplyTo:   replyTo,
		MessageID: "<o1@example.com>",
	}
}

// ---------------------------------------------------------------- the named tests

func TestSendRefusedBelowLevel4(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDraft))
	c := compose("bob@example.com")

	res, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	var perm *PermissionError
	require.ErrorAs(t, err, &perm)
	require.Equal(t, account.MailSend, perm.Decision.Required)
	require.Equal(t, account.MailDraft, perm.Decision.Current)
	require.Nil(t, res)
	require.Nil(t, needs)
	require.Empty(t, env.sender.sent())
	require.Empty(t, env.waitingActions())
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "level_too_low")
}

func TestSendInBandNeedsConfirmationThenSends(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account() // Mail 4, ConfirmSends on, confirmed window 0
	ctx := env.ctx()
	c := compose("Bob <bob@example.com>")

	res, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Nil(t, res)
	require.NotNil(t, needs)
	require.NotEmpty(t, needs.RequestState)
	require.Equal(t, confirm.KindSend, needs.Pending.Kind)
	require.Equal(t, confirm.Preview{
		Account:   "me@example.com",
		From:      "me@example.com",
		To:        []string{"bob@example.com"},
		Subject:   "Plans",
		BodyStart: "See you at noon.",
		Count:     1,
	}, needs.Pending.Preview)
	require.Empty(t, env.sender.sent(), "nothing is sent before the user confirms")
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomePendingApproval, "confirm_sends")

	pendingID := needs.Pending.ID
	res, needs, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, "sent", res.Status, "a confirmed send has a window of 0 and goes out at once")
	require.NotEmpty(t, res.SendID)
	require.Nil(t, res.SendAt)

	sent := env.sender.sent()
	require.Len(t, sent, 1)
	require.Equal(t, []string{"bob@example.com"}, sent[0].Msg.Recipients)
	require.Equal(t, "me@example.com", sent[0].Msg.From)
	require.Empty(t, sent[0].Draft)

	last := env.lastEntry()
	requireEntry(t, last, "send_message", audit.OutcomeOK, "confirmed_in_band")
	require.Equal(t, []string{"bob@example.com"}, last.Recipients)
	require.Equal(t, 1, last.Count)
	require.Equal(t, "test-client", last.Client)

	ended := env.pending(pendingID)
	require.Equal(t, confirm.StateDone, ended.State)
	require.Equal(t, res.SendID, ended.ResultID)
}

func TestSendRetryWithChangedBodyRejected(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	changed := c
	changed.Body = "Send the money to account 1234."
	res, _, err := env.svc.Send(ctx, a.ID, &changed, nil, ConfirmInBand, accept(needs))

	require.ErrorIs(t, err, confirm.ErrPayloadChanged)
	require.Nil(t, res)
	require.Empty(t, env.sender.sent())
	require.Equal(t, confirm.StatePending, env.pending(needs.Pending.ID).State)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "payload_changed")
}

func TestSendDeclined(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	res, needs2, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, decline(needs))
	require.NoError(t, err)
	require.Nil(t, needs2)
	require.Equal(t, &SendResult{Status: "declined", SendID: needs.Pending.ID}, res)
	require.Empty(t, env.sender.sent())
	require.Equal(t, confirm.StateRejected, env.pending(needs.Pending.ID).State)
	last := env.lastEntry()
	requireEntry(t, last, "send_message", audit.OutcomeDeclined, "declined")
	require.Equal(t, []string{"bob@example.com"}, last.Recipients)

	// The declined state cannot be accepted later.
	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.ErrorIs(t, err, confirm.ErrNotPending)
	require.Empty(t, env.sender.sent())
}

func TestSendFallbackReturnsApprovalLink(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose("bob@example.com")

	res, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)

	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, "pending_approval", res.Status)
	require.NotEmpty(t, res.SendID)
	require.Equal(t, "https://mcp.test/approve/"+res.SendID, res.ApprovalLink)
	require.NotContains(t, res.ApprovalLink, "token=")
	require.Empty(t, env.sender.sent())
	waiting := env.waitingActions()
	require.Len(t, waiting, 1)
	require.Equal(t, res.SendID, waiting[0].ID)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomePendingApproval, "confirm_sends")
}

func TestApprovePendingSends(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")
	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	require.NoError(t, env.svc.ApprovePending(ctx, res.SendID))

	sent := env.sender.sent()
	require.Len(t, sent, 1, "the confirmed window is 0, so the message goes out at once")
	require.Equal(t, []string{"bob@example.com"}, sent[0].Msg.Recipients)
	p := env.pending(res.SendID)
	require.Equal(t, confirm.StateDone, p.State)
	require.NotEmpty(t, p.ResultID)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeOK, "approved_on_web")

	status, err := env.svc.SendStatus(ctx, a.ID, res.SendID)
	require.NoError(t, err)
	require.Equal(t, "sent", status.Status)
	require.Equal(t, p.ResultID, status.SendID)

	require.ErrorIs(t, env.svc.ApprovePending(ctx, res.SendID), confirm.ErrNotPending)
	require.Len(t, env.sender.sent(), 1)
}

func TestAutonomyUsesUndoWindow(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	c := compose("bob@example.com")

	res, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, "queued", res.Status)
	require.NotNil(t, res.SendAt)
	require.Equal(t, testStart.Add(30*time.Second), *res.SendAt)
	require.Empty(t, env.sender.sent(), "the message waits for its undo window")
	item, err := env.outbox.Get(context.Background(), env.f.tenant, res.SendID)
	require.NoError(t, err)
	require.Equal(t, outbox.StatusQueued, item.Status)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeOK, "no_confirmation")
}

func TestNewRecipientCheckForcesConfirmation(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) { a.Limits.NewRecipientCheck = true })
	ctx := env.ctx()
	c := compose("new@x")

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.NotNil(t, needs, "new@x was never sent to, so the user must confirm")
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomePendingApproval, "new_recipient")

	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err)
	require.Equal(t, "sent", res.Status)

	res, needs, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Nil(t, needs, "new@x is known now")
	require.Equal(t, "queued", res.Status, "without a confirmation the auto window applies")

	// The address is compared without regard to case.
	upper := compose("NEW@X")
	_, needs, err = env.svc.Send(ctx, a.ID, &upper, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Nil(t, needs)

	// One known and one new address: the new one needs a confirmation.
	mixed := compose("new@x", "other@x")
	_, needs, err = env.svc.Send(ctx, a.ID, &mixed, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.NotNil(t, needs)
}

// With the check off, the recipients are still remembered, so turning it on
// later does not ask about people the account wrote to before.
func TestKnownRecipientsRecordedWhenTheCheckIsOff(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	ctx := env.ctx()
	c := compose("bob@example.com")
	_, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	a = env.f.reload(a)
	a.Limits.NewRecipientCheck = true
	require.NoError(t, env.f.store.UpdateAccount(ctx, a))

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Nil(t, needs)
}

func TestKnownRecipientsStoredHashed(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) {
		a.Limits.NewRecipientCheck = true
		a.Limits.UndoWindowAuto = 0
	})
	ctx := env.ctx()
	c := compose("new@x")
	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err)

	// The table has one row, and it is not the plain SHA-256 of the address.
	var rows int
	var hash []byte
	require.NoError(t, env.f.rawDB().QueryRowContext(ctx, `SELECT count(*), max(hash) FROM known_recipients`).Scan(&rows, &hash))
	require.Equal(t, 1, rows)
	plain := sha256.Sum256([]byte("new@x"))
	require.NotEqual(t, plain[:], hash, "a plain hash can be checked against a list of addresses")

	// No file of the database holds the address or its plain hash.
	files, err := filepath.Glob(filepath.Join(env.f.dir, "email-mcp.db*"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 2, "the database and its WAL file")
	for _, name := range files {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		require.False(t, bytes.Contains(bytes.ToLower(data), []byte("new@x")), "%s holds the address", filepath.Base(name))
		require.False(t, bytes.Contains(data, plain[:]), "%s holds the plain hash", filepath.Base(name))
	}
}

func TestSendDraftUsesDraftRecipientsAndTrashesDraft(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	env.backend.raws["d1"] = draftMail("me@example.com")
	draft := mail.Ref("d1")

	res, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Nil(t, needs)
	require.Equal(t, "queued", res.Status)
	require.Equal(t, int64(25<<20), env.backend.rawMax, "a draft is read up to 25 MB")

	// The outbox item carries the draft, so the sender can move it to Trash
	// after the delivery. The service itself does not discard it.
	row, err := env.f.store.GetOutbox(context.Background(), env.f.tenant, res.SendID)
	require.NoError(t, err)
	require.Equal(t, "d1", row.DraftRef)
	require.Empty(t, env.backend.discarded)

	// The message goes out with the recipients of the draft, Bcc included, and
	// without the Bcc header.
	env.clock.Advance(31 * time.Second)
	require.NoError(t, env.outbox.Drain(context.Background(), time.Second))
	sent := env.sender.sent()
	require.Len(t, sent, 1)
	require.Equal(t, []string{"ann@example.com", "hidden@example.com"}, sent[0].Msg.Recipients)
	require.Equal(t, draft, sent[0].Draft)
	require.NotContains(t, string(sent[0].Msg.Data), "Bcc:")
	require.NotContains(t, string(sent[0].Msg.Data), "hidden@example.com")
	require.Equal(t, []string{"ann@example.com", "hidden@example.com"}, env.lastEntry().Recipients)
}

func TestRateLimitedSendAudited(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) { a.Limits.SendsPerHour = 1 })
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	res, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	var rate *policy.RateLimitError
	require.ErrorAs(t, err, &rate)
	require.Equal(t, policy.CountSend, rate.Kind)
	require.Nil(t, res)
	require.Nil(t, needs)
	last := env.lastEntry()
	requireEntry(t, last, "send_message", audit.OutcomeRefused, "rate_limited")
	require.Equal(t, []string{"bob@example.com"}, last.Recipients)

	items, err := env.f.store.DueOutbox(ctx, testStart.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, items, 1, "only the first message is queued")
}

// ---------------------------------------------------------------- the payload

func TestSendPayloadIsCanonical(t *testing.T) {
	a := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", Body: "B"}
	b := mail.Compose{To: []string{"bob@example.com"}, Cc: []string{}, Bcc: nil, Subject: "S", Body: "B", Attachments: []mail.OutgoingAttachment{}}

	ra, err := newSendRequest(&a, nil)
	require.NoError(t, err)
	rb, err := newSendRequest(&b, nil)
	require.NoError(t, err)
	require.Equal(t, string(ra.payload), string(rb.payload), "nil and empty lists are the same request")
	require.True(t, strings.HasPrefix(string(ra.payload), `{"v":1,"compose":{`), string(ra.payload))
	require.True(t, strings.HasSuffix(string(ra.payload), `,"draft":""}`), string(ra.payload))

	draft := mail.Ref("d1")
	rd, err := newSendRequest(nil, &draft)
	require.NoError(t, err)
	require.Equal(t, `{"v":1,"compose":null,"draft":"d1"}`, string(rd.payload))

	// The payload reads back as the same request.
	back, err := decodeSendPayload(ra.payload)
	require.NoError(t, err)
	require.Equal(t, ra.compose, back.compose)
	back, err = decodeSendPayload(rd.payload)
	require.NoError(t, err)
	require.Equal(t, draft, back.draft)
	require.Nil(t, back.compose)

	for name, bad := range map[string]string{
		"other version":  `{"v":2,"compose":null,"draft":"d1"}`,
		"both":           `{"v":1,"compose":{},"draft":"d1"}`,
		"neither":        `{"v":1,"compose":null,"draft":""}`,
		"unknown field":  `{"v":1,"compose":null,"draft":"d1","to":"x"}`,
		"trailing data":  `{"v":1,"compose":null,"draft":"d1"}{}`,
		"not json":       `send it`,
		"compose fields": `{"v":1,"compose":{"to":["a@x"],"evil":true},"draft":""}`,
	} {
		_, err := decodeSendPayload([]byte(bad))
		require.Error(t, err, name)
	}
}

func TestSendNeedsExactlyOneOfComposeAndDraft(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose("bob@example.com")
	draft := mail.Ref("d1")
	empty := mail.Ref("")

	for name, call := range map[string]func() error{
		"neither":     func() error { _, _, err := env.svc.Send(env.ctx(), a.ID, nil, nil, ConfirmInBand, nil); return err },
		"both":        func() error { _, _, err := env.svc.Send(env.ctx(), a.ID, &c, &draft, ConfirmInBand, nil); return err },
		"empty draft": func() error { _, _, err := env.svc.Send(env.ctx(), a.ID, nil, &empty, ConfirmInBand, nil); return err },
		"bad mode":    func() error { _, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmMode(7), nil); return err },
	} {
		require.ErrorIs(t, call(), ErrInvalidRequest, name)
	}
	require.Empty(t, env.waitingActions())
}

// The message is built again from the payload when the user's answer comes.
// When the new message would go to other people than the preview showed (here
// the Reply-To of the original changed), it is not sent.
func TestSendRebuiltMessageMustMatchThePreview(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	env.backend.nextOriginal = func(_ mail.Ref, call int) *mail.Message {
		if call == 1 {
			return original()
		}
		return original(mail.Address{Email: "evil@example.com"})
	}
	c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"alice@example.com"}, needs.Pending.Preview.To)

	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.ErrorIs(t, err, confirm.ErrPayloadChanged)
	require.Empty(t, env.sender.sent())
	require.Equal(t, confirm.StateRejected, env.pending(needs.Pending.ID).State)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "payload_changed")
}

func TestApprovePendingRefusesAChangedMessage(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	env.backend.nextOriginal = func(_ mail.Ref, call int) *mail.Message {
		if call == 1 {
			return original()
		}
		return original(mail.Address{Email: "evil@example.com"})
	}
	c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}
	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	require.ErrorIs(t, env.svc.ApprovePending(ctx, res.SendID), confirm.ErrPayloadChanged)
	require.Empty(t, env.sender.sent())
	require.Equal(t, confirm.StateRejected, env.pending(res.SendID).State)
}

// The preview shows every envelope recipient, so a Reply-To that a stranger set,
// and the people that a reply to all adds, are visible to the user.
func TestPreviewShowsTheRealRecipientsOfAReply(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.originals["o1"] = original(mail.Address{Email: "collector@evil.test"})
	c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1", All: true}, Bcc: []string{"boss@example.com"}, Body: "Fine."}

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Equal(t, []string{"collector@evil.test", "dan@example.com", "erin@example.com", "boss@example.com"},
		needs.Pending.Preview.To, "the Reply-To, the people of the reply to all, and the Bcc; not the account itself")
	require.Equal(t, "Re: Lunch", needs.Pending.Preview.Subject)
}

// ---------------------------------------------------------------- limits

func TestTooManyRecipientsRefusedAfterReplyAll(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(func(a *account.Account) { a.Limits.RecipientsPerMessage = 2 })
	env.backend.originals["o1"] = original()
	// The agent names one person, but the reply to all adds three.
	c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1", All: true}, To: []string{"bob@example.com"}, Body: "All of you."}

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.ErrorIs(t, err, ErrLimit)
	require.Contains(t, err.Error(), "4 recipients")
	require.Contains(t, err.Error(), "at most 2")
	require.Nil(t, needs)
	require.Empty(t, env.waitingActions(), "the user is not asked for a send that cannot happen")
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "too_many_recipients")
}

func TestSendLimitZeroIsDisabled(t *testing.T) {
	for name, change := range map[string]func(*account.Account){
		"hour": func(a *account.Account) { a.Limits.SendsPerHour = 0 },
		"day":  func(a *account.Account) { a.Limits.SendsPerDay = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(change)
			c := compose("bob@example.com")

			_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.ErrorIs(t, err, ErrLimit)
			require.Contains(t, err.Error(), "turned off")
			var rate *policy.RateLimitError
			require.NotErrorAs(t, err, &rate, "waiting does not help, so there is no 'try again'")
			require.Nil(t, needs)
			require.Empty(t, env.waitingActions())
			requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "limit_disabled")
		})
	}
}

// A send that the user declined, or that the server refused, uses none of the
// quota. Only a send that reaches the outbox counts.
func TestDeclinedAndRefusedSendsUseNoQuota(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(func(a *account.Account) {
		a.Limits.SendsPerHour = 1
		a.Limits.RecipientsPerMessage = 1
	})
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, decline(needs))
	require.NoError(t, err)

	tooMany := compose("bob@example.com", "carol@example.com")
	_, _, err = env.svc.Send(ctx, a.ID, &tooMany, nil, ConfirmInBand, nil)
	require.ErrorIs(t, err, ErrLimit)

	changed := c
	changed.Body = "other"
	_, needs, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Send(ctx, a.ID, &changed, nil, ConfirmInBand, accept(needs))
	require.ErrorIs(t, err, confirm.ErrPayloadChanged)

	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err, "the declined and refused calls must not have used the only send of the hour")
	require.Equal(t, "sent", res.Status)

	_, needs, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	var rate *policy.RateLimitError
	require.ErrorAs(t, err, &rate, "the one real send used the quota")
	require.Len(t, env.sender.sent(), 1)
}

// ---------------------------------------------------------------- one approval acts once

// An agent that repeats an accepted answer does not send the message twice.
func TestSendAcceptedTwiceSendsOnce(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")
	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err)
	_, _, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.ErrorIs(t, err, confirm.ErrNotPending)
	require.Len(t, env.sender.sent(), 1)

	// The same for Trash.
	b := env.account(level(account.MailDelete))
	_, tneeds, err := env.svc.Trash(ctx, b.ID, []mail.Ref{"r1"}, ConfirmInBand, nil)
	require.NoError(t, err)
	_, _, err = env.svc.Trash(ctx, b.ID, []mail.Ref{"r1"}, ConfirmInBand, accept(tneeds))
	require.NoError(t, err)
	_, _, err = env.svc.Trash(ctx, b.ID, []mail.Ref{"r1"}, ConfirmInBand, accept(tneeds))
	require.ErrorIs(t, err, confirm.ErrNotPending)
	require.Len(t, env.backend.trashCalls(), 1)
}

// The in-band answer and the web page can approve the same action at the same
// time, and an agent can repeat its answer. Exactly one of them acts.
func TestConcurrentApprovalsActOnce(t *testing.T) {
	for name, webApprovals := range map[string]bool{"in band and web": true, "in band only": false} {
		t.Run(name, func(t *testing.T) {
			for round := range 5 {
				env := newWriteEnv(t)
				a := env.account()
				ctx := env.ctx()
				c := compose("bob@example.com")
				_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
				require.NoError(t, err)

				start := make(chan struct{})
				var wg sync.WaitGroup
				errs := make([]error, 4)
				for i := range errs {
					wg.Go(func() {
						<-start
						if webApprovals && i%2 == 1 {
							errs[i] = env.svc.ApprovePending(ctx, needs.Pending.ID)
							return
						}
						_, _, errs[i] = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
					})
				}
				close(start)
				wg.Wait()

				won := 0
				for _, err := range errs {
					if err == nil {
						won++
						continue
					}
					require.ErrorIs(t, err, confirm.ErrNotPending, "round %d", round)
				}
				require.Equal(t, 1, won, "round %d", round)
				require.Len(t, env.sender.sent(), 1, "round %d: the message is sent once", round)
			}
		})
	}
}

// A request state belongs to the account it was made for. An agent cannot use
// the confirmation of one account to send from another one.
func TestRequestStateOfAnotherAccountRefused(t *testing.T) {
	env := newWriteEnv(t)
	first := env.account()
	second := env.f.addAccount(env.f.tenant, "other@example.com", offlineSettings(), "pw")
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(ctx, first.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)

	_, _, err = env.svc.Send(ctx, second.ID, &c, nil, ConfirmInBand, accept(needs))
	require.ErrorIs(t, err, confirm.ErrStateInvalid)
	require.Empty(t, env.sender.sent())

	_, _, err = env.svc.Send(ctx, second.ID, &c, nil, ConfirmInBand, decline(needs))
	require.ErrorIs(t, err, confirm.ErrStateInvalid)
}

// ---------------------------------------------------------------- drafts

func TestSendDraftFromAnotherAddressRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	env.backend.raws["d1"] = draftMail("boss@example.com")
	env.backend.raws["d2"] = draftMail("ME@Example.COM")
	ctx := env.ctx()

	d1 := mail.Ref("d1")
	_, _, err := env.svc.Send(ctx, a.ID, nil, &d1, ConfirmInBand, nil)
	require.ErrorIs(t, err, ErrDraftNotFromAccount)
	require.Empty(t, env.waitingActions())
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "draft_from_mismatch")

	d2 := mail.Ref("d2")
	res, _, err := env.svc.Send(ctx, a.ID, nil, &d2, ConfirmInBand, nil)
	require.NoError(t, err, "the address is compared without regard to case")
	require.Equal(t, "queued", res.Status)
}

func TestSendDraftWithConfirmation(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.raws["d1"] = draftMail("me@example.com")
	ctx := env.ctx()
	draft := mail.Ref("d1")

	_, needs, err := env.svc.Send(ctx, a.ID, nil, &draft, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"ann@example.com", "hidden@example.com"}, needs.Pending.Preview.To)
	require.Equal(t, "Draft plans", needs.Pending.Preview.Subject)
	require.Equal(t, "Draft body\n", needs.Pending.Preview.BodyStart)

	res, _, err := env.svc.Send(ctx, a.ID, nil, &draft, ConfirmInBand, accept(needs))
	require.NoError(t, err)
	require.Equal(t, "sent", res.Status)
	require.Equal(t, draft, env.sender.sent()[0].Draft)
}

// ---------------------------------------------------------------- status and cancel

func TestSendStatusResolvesPendingIDs(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")

	waiting, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	got, err := env.svc.SendStatus(ctx, a.ID, waiting.SendID)
	require.NoError(t, err)
	require.Equal(t, "pending_approval", got.Status)
	require.Equal(t, waiting.ApprovalLink, got.ApprovalLink)

	require.NoError(t, env.svc.RejectPending(ctx, waiting.SendID))
	got, err = env.svc.SendStatus(ctx, a.ID, waiting.SendID)
	require.NoError(t, err)
	require.Equal(t, "declined", got.Status)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeDeclined, "rejected_on_web")

	old, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	env.clock.Advance(25 * time.Hour)
	got, err = env.svc.SendStatus(ctx, a.ID, old.SendID)
	require.NoError(t, err)
	require.Equal(t, "expired", got.Status)
	require.ErrorIs(t, env.svc.ApprovePending(ctx, old.SendID), confirm.ErrNotPending)

	_, err = env.svc.SendStatus(ctx, a.ID, "no-such-id")
	require.ErrorIs(t, err, ErrSendNotFound)
	_, err = env.svc.SendStatus(ctx, a.ID, "bad|id")
	require.ErrorIs(t, err, ErrSendNotFound)
}

func TestCancelSend(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	other := env.f.addAccount(env.f.tenant, "other@example.com", offlineSettings(), "pw", autonomy)
	ctx := env.ctx()
	c := compose("bob@example.com")

	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "queued", res.Status)

	require.ErrorIs(t, env.svc.CancelSend(ctx, other.ID, res.SendID), ErrSendNotFound,
		"a send of one account cannot be cancelled through another")
	_, err = env.svc.SendStatus(ctx, other.ID, res.SendID)
	require.ErrorIs(t, err, ErrSendNotFound)

	require.NoError(t, env.svc.CancelSend(ctx, a.ID, res.SendID))
	requireEntry(t, env.lastEntry(), "cancel_send", audit.OutcomeOK, "")
	got, err := env.svc.SendStatus(ctx, a.ID, res.SendID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Status)

	env.clock.Advance(time.Minute)
	require.NoError(t, env.outbox.Drain(context.Background(), time.Second))
	require.Empty(t, env.sender.sent())
}

func TestCancelSendTooLate(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) { a.Limits.UndoWindowAuto = 0 })
	ctx := env.ctx()
	c := compose("bob@example.com")
	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "sent", res.Status)

	err = env.svc.CancelSend(ctx, a.ID, res.SendID)
	require.ErrorIs(t, err, outbox.ErrTooLate)
	requireEntry(t, env.lastEntry(), "cancel_send", audit.OutcomeError, "too_late")
}

func TestCancelSendWithdrawsAWaitingApproval(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	ctx := env.ctx()
	c := compose("bob@example.com")
	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	require.NoError(t, env.svc.CancelSend(ctx, a.ID, res.SendID))
	require.Equal(t, confirm.StateRejected, env.pending(res.SendID).State)
	require.ErrorIs(t, env.svc.ApprovePending(ctx, res.SendID), confirm.ErrNotPending)
	require.Empty(t, env.sender.sent())
}

// ---------------------------------------------------------------- failed sends

func TestFailedInlineSendIsAnError(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy, func(a *account.Account) {
		a.Limits.UndoWindowAuto = 0
		a.Limits.NewRecipientCheck = true
	})
	env.sender.err = outbox.Permanent(errors.New("550 mailbox unavailable"))
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
	require.NoError(t, err, "the outbox owns the message, so the send is not repeated")
	require.Equal(t, "failed", res.Status)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeError, "send_failed")

	// A failed send does not make the address known.
	_, needs, err = env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.NotNil(t, needs)
}
