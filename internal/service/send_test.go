package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
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

	// No file of the database holds the address or its plain hash. The files
	// are read through an os.Root of the data directory, so no name can point
	// to a file outside it.
	dir, err := os.OpenRoot(env.f.dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })
	files, err := fs.Glob(dir.FS(), "email-mcp.db*")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 2, "the database and its WAL file")
	for _, name := range files {
		data, err := dir.ReadFile(name)
		require.NoError(t, err)
		require.False(t, bytes.Contains(bytes.ToLower(data), []byte("new@x")), "%s holds the address", name)
		require.False(t, bytes.Contains(data, plain[:]), "%s holds the plain hash", name)
	}
}

func TestSendDraftUsesDraftRecipientsAndTrashesDraft(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	env.backend.addMessage("d1", draftMail("me@example.com"), true)
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

// The payload of a send waits in the database until the user answers, so it
// has a size limit.
func TestSendPayloadTooLargeRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose("bob@example.com")
	c.Body = strings.Repeat("a", 15<<20)

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Contains(t, err.Error(), "15 MB")
	require.Nil(t, needs)
	require.Empty(t, env.waitingActions())
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "invalid_request")

	_, err = newSendRequest(&c, nil)
	require.ErrorIs(t, err, ErrInvalidRequest)
	c.Body = strings.Repeat("a", 14<<20)
	_, err = newSendRequest(&c, nil)
	require.NoError(t, err)
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

// The in-band answer and the web page can try to approve the same action at the
// same time, and an agent can repeat its answer. Exactly one of them acts. The
// web page refuses an in-band action at all (ErrAnswerInBand matches
// confirm.ErrNotPending), so one of the answers in the chat acts.
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

// A request state names an action of one kind. A Trash state must not send,
// and a send state must not move mail to Trash, even when the payload bytes are
// the same: an agent can pick a Trash ref that looks like a send payload. The
// try does not use the confirmation up.
func TestStateOfAnotherKindRefused(t *testing.T) {
	draft := mail.Ref("d1")
	req, err := newSendRequest(nil, &draft)
	require.NoError(t, err)
	sameBytes := []mail.Ref{mail.Ref(req.payload)}

	t.Run("a Trash state used to send", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account(level(account.MailDelete))
		env.backend.addMessage(draft, draftMail("me@example.com"), true)
		ctx := env.ctx()
		_, needs, err := env.svc.Trash(ctx, a.ID, sameBytes, ConfirmInBand, nil)
		require.NoError(t, err)

		_, _, err = env.svc.Send(ctx, a.ID, nil, &draft, ConfirmInBand, accept(needs))
		require.ErrorIs(t, err, confirm.ErrStateInvalid)
		_, _, err = env.svc.Send(ctx, a.ID, nil, &draft, ConfirmInBand, decline(needs))
		require.ErrorIs(t, err, confirm.ErrStateInvalid)

		require.Empty(t, env.sender.sent())
		require.Equal(t, confirm.StatePending, env.pending(needs.Pending.ID).State)
	})
	t.Run("a send state used to move mail to Trash", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account(level(account.MailDelete))
		env.backend.addMessage(draft, draftMail("me@example.com"), true)
		ctx := env.ctx()
		_, needs, err := env.svc.Send(ctx, a.ID, nil, &draft, ConfirmInBand, nil)
		require.NoError(t, err)

		_, _, err = env.svc.Trash(ctx, a.ID, sameBytes, ConfirmInBand, accept(needs))
		require.ErrorIs(t, err, confirm.ErrStateInvalid)
		_, _, err = env.svc.Trash(ctx, a.ID, sameBytes, ConfirmInBand, decline(needs))
		require.ErrorIs(t, err, confirm.ErrStateInvalid)

		require.Empty(t, env.backend.trashCalls())
		require.Equal(t, confirm.StatePending, env.pending(needs.Pending.ID).State)
	})
}

// ---------------------------------------------------------------- drafts

func TestSendDraftFromAnotherAddressRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	env.backend.addMessage("d1", draftMail("boss@example.com"), true)
	env.backend.addMessage("d2", draftMail("ME@Example.COM"), true)
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

// Only a real draft is sent as it is saved. A received message with a From
// forged to the user's own address, or an old message in Sent, has the right
// From but is not a draft. It is refused before it is read.
func TestSendRefusesAMessageThatIsNotADraft(t *testing.T) {
	for name, ref := range map[string]mail.Ref{"inbox message": "in1", "sent message": "sent1"} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(autonomy, func(a *account.Account) { a.Limits.UndoWindowAuto = 0 })
			env.backend.addMessage(ref, draftMail("me@example.com"), false)

			res, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &ref, ConfirmInBand, nil)

			require.ErrorIs(t, err, ErrNotADraft)
			require.ErrorIs(t, err, ErrInvalidRequest)
			require.Contains(t, err.Error(), "not a draft")
			require.Nil(t, res)
			require.Nil(t, needs)
			require.Zero(t, env.backend.rawMax, "the message is not read")
			require.Empty(t, env.sender.sent())
			require.Empty(t, env.waitingActions())
			due, err := env.f.store.DueOutbox(context.Background(), testStart.Add(time.Hour), 10)
			require.NoError(t, err)
			require.Empty(t, due, "nothing is queued")
			requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "invalid_request")
		})
	}
}

func TestSendDraftWithConfirmation(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.addMessage("d1", draftMail("me@example.com"), true)
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

	got, err := env.svc.SendStatus(ctx, a.ID, res.SendID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.Status, "the agent withdrew it: it was not declined by the user")
}

// ---------------------------------------------------------------- failed sends

func TestFailedInlineSendIsReportedAsFailed(t *testing.T) {
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

// ---------------------------------------------------------------- what the preview shows

// forwardWithAttachments sets up an original message o1 with the attachments,
// and returns a forward of it that carries them and one attachment of its own.
func forwardWithAttachments(b *recBackend, names ...string) mail.Compose {
	orig := original()
	for i, name := range names {
		id := fmt.Sprint(i + 2)
		info := mail.AttachmentInfo{ID: id, Filename: name, ContentType: "application/pdf", Size: 3}
		orig.Attachments = append(orig.Attachments, info)
		b.attachments[id] = &mail.Attachment{AttachmentInfo: info, Data: []byte("pdf")}
	}
	b.originals["o1"] = orig
	ref := mail.Ref("o1")
	return mail.Compose{
		ForwardOf: &ref, To: []string{"bob@example.com"}, Body: "FYI", IncludeOriginalAttachments: true,
		Attachments: []mail.OutgoingAttachment{{Filename: "own.txt", ContentType: "text/plain", Data: []byte("mine")}},
	}
}

// draftWithAttachment is a saved draft from me@example.com with one attachment.
// extraHeader is added to the header of the attachment part.
func draftWithAttachment(extraHeader string) []byte {
	return crlf("From: Me <me@example.com>\n" +
		"To: Ann <ann@example.com>\n" +
		"Subject: Draft with a file\n" +
		"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\n" +
		"\n" +
		"--B\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" +
		"See the file.\n" +
		"--B\n" +
		"Content-Type: application/pdf\n" +
		extraHeader +
		"Content-Disposition: attachment; filename=\"notes.pdf\"\n" +
		"\n" +
		"pdf\n" +
		"--B--\n")
}

func TestPreviewListsTheAttachments(t *testing.T) {
	t.Run("a new message", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := compose("bob@example.com")
		c.Attachments = []mail.OutgoingAttachment{
			{Filename: "figures.xlsx", ContentType: "application/octet-stream", Data: []byte("x")},
			{Data: []byte("no name")},
		}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Equal(t, []string{"figures.xlsx", "attachment"}, needs.Pending.Preview.Attachments,
			"an attachment without a name is sent as \"attachment\"")
	})

	t.Run("a forward with the attachments of the original", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := forwardWithAttachments(env.backend, "plan.pdf")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Equal(t, []string{"plan.pdf", "own.txt"}, needs.Pending.Preview.Attachments,
			"the attachments of the original come first, as in the message")
	})

	t.Run("a saved draft", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		env.backend.addMessage("d1", draftWithAttachment(""), true)
		draft := mail.Ref("d1")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Equal(t, []string{"notes.pdf"}, needs.Pending.Preview.Attachments)
	})

	t.Run("no attachments", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := compose("bob@example.com")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Nil(t, needs.Pending.Preview.Attachments)
	})
}

// A draft that the parser cannot read in full could hide an attachment from the
// preview, so it is not sent.
func TestDraftThatCannotBeReadInFullIsNotSent(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	big := "X-Big: " + strings.Repeat("a", 1<<20) + "\n"
	env.backend.addMessage("d1", draftWithAttachment(big), true)
	draft := mail.Ref("d1")

	_, _, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

	require.ErrorIs(t, err, ErrDraftUnreadable)
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Empty(t, env.sender.sent())
	require.Empty(t, env.waitingActions())
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "invalid_request")
}

// The attachments are part of what the user approves: when the message built
// again has other attachments, nothing is sent.
func TestChangedAttachmentsAreAChangedMessage(t *testing.T) {
	setup := func(t *testing.T) (*writeEnv, *account.Account, mail.Compose) {
		env := newWriteEnv(t)
		a := env.account()
		c := forwardWithAttachments(env.backend, "plan.pdf", "secret.txt")
		first := original()
		first.Attachments = env.backend.originals["o1"].Attachments[:1]
		second := env.backend.originals["o1"]
		env.backend.nextOriginal = func(_ mail.Ref, call int) *mail.Message {
			if call == 1 {
				return first
			}
			return second
		}
		return env, a, c
	}

	t.Run("in band", func(t *testing.T) {
		env, a, c := setup(t)
		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"plan.pdf", "own.txt"}, needs.Pending.Preview.Attachments)

		_, _, err = env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, accept(needs))

		require.ErrorIs(t, err, confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	t.Run("on the web page", func(t *testing.T) {
		env, a, c := setup(t)
		res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
		require.NoError(t, err)

		require.ErrorIs(t, env.svc.ApprovePending(env.ctx(), res.SendID), confirm.ErrPayloadChanged)
		require.Empty(t, env.sender.sent())
	})

	require.False(t, samePreview(confirm.Preview{Attachments: []string{"a.pdf"}}, confirm.Preview{Attachments: []string{"b.pdf"}}))
	require.False(t, samePreview(confirm.Preview{Attachments: []string{"a.pdf"}}, confirm.Preview{}))
	require.True(t, samePreview(confirm.Preview{}, confirm.Preview{Attachments: []string{}}))
}

// Most mail programs show the HTML of a message. A message with both a body and
// an HTML body could show the user one text and the recipient another, so it is
// refused. The text part is made from the HTML.
func TestBodyAndHTMLBodyTogetherRefused(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose("bob@example.com")
	c.HTMLBody = "<p>Something else</p>"

	_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
	require.ErrorIs(t, err, ErrBodyAndHTML)
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Empty(t, env.waitingActions())

	_, err = env.svc.CreateDraft(env.ctx(), a.ID, c)
	require.ErrorIs(t, err, ErrBodyAndHTML)
	require.Empty(t, env.backend.saved)
}

func TestPreviewShowsTheTextOfTheHTML(t *testing.T) {
	t.Run("a new message with only HTML", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S",
			HTMLBody: `<p>Hello <b>Bob</b></p><div style="display:none">hidden words</div>`}

		_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Contains(t, needs.Pending.Preview.BodyStart, "Hello Bob")
		require.NotContains(t, needs.Pending.Preview.BodyStart, "hidden words")
	})

	t.Run("a draft with a text part and another HTML part", func(t *testing.T) {
		env := newWriteEnv(t)
		a := env.account()
		env.backend.addMessage("d1", crlf("From: Me <me@example.com>\n"+
			"To: Ann <ann@example.com>\n"+
			"Subject: Two bodies\n"+
			"MIME-Version: 1.0\n"+
			"Content-Type: multipart/alternative; boundary=\"A\"\n"+
			"\n"+
			"--A\n"+
			"Content-Type: text/plain; charset=utf-8\n"+
			"\n"+
			"Harmless text part.\n"+
			"--A\n"+
			"Content-Type: text/html; charset=utf-8\n"+
			"\n"+
			"<p>Please wire the money today.</p>\n"+
			"--A--\n"), true)
		draft := mail.Ref("d1")

		_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

		require.NoError(t, err)
		require.Contains(t, needs.Pending.Preview.BodyStart, "Please wire the money today.",
			"the preview shows what most mail programs show")
		require.NotContains(t, needs.Pending.Preview.BodyStart, "Harmless")
	})
}

// An action that the user confirms in band ends with its request state. After
// that it cannot be approved on the web page, so the agent cannot ask again and
// get the same message sent twice. An action for the web page keeps 24 hours.
func TestInBandActionExpiresWithItsRequestState(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(level(account.MailDelete))
	ctx := env.ctx()
	c := compose("bob@example.com")

	_, inBand, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, inBand.Pending.CreatedAt.Add(confirm.InBandLifetime), inBand.Pending.ExpiresAt)
	_, trash, err := env.svc.Trash(ctx, a.ID, []mail.Ref{"m1"}, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, trash.Pending.CreatedAt.Add(confirm.InBandLifetime), trash.Pending.ExpiresAt)
	web, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	env.clock.Advance(confirm.InBandLifetime + time.Second)

	require.ErrorIs(t, env.svc.ApprovePending(ctx, inBand.Pending.ID), confirm.ErrNotPending)
	require.ErrorIs(t, env.svc.ApprovePending(ctx, trash.Pending.ID), confirm.ErrNotPending)
	require.Empty(t, env.sender.sent())
	require.Empty(t, env.backend.trashCalls())

	require.NoError(t, env.svc.ApprovePending(ctx, web.SendID), "an action for the web page still waits")
	require.Len(t, env.sender.sent(), 1)
}

// ---------------------------------------------------------------- the body that the user sees (fix round 2)

// hiddenFiller is HTML that hides more than previewParseChars characters, so
// the text after it is beyond any cut of the HTML.
var hiddenFiller = `<div style="display:none">` + strings.Repeat("x", previewParseChars+1) + `</div>`

// lateStyle is HTML whose style rule, after the filler, hides the decoy that
// comes first.
var lateStyle = `<p class="decoy">Decoy: nothing to do here.</p>` + hiddenFiller +
	`<style>.decoy{display:none}</style><p>Please wire the money today.</p>`

// draftWithHTML is a saved draft with a harmless text part and the given HTML
// part.
func draftWithHTML(html string) []byte {
	return crlf("From: Me <me@example.com>\n" +
		"To: Ann <ann@example.com>\n" +
		"Subject: Two bodies\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/alternative; boundary=\"A\"\n" +
		"\n" +
		"--A\n" +
		"Content-Type: text/plain; charset=utf-8\n" +
		"\n" +
		"Harmless text part.\n" +
		"--A\n" +
		"Content-Type: text/html; charset=utf-8\n" +
		"\n" +
		html + "\n" +
		"--A--\n")
}

// The preview shows the text that comes from the whole HTML, as the recipient
// sees it, also when the HTML is longer than the part of it that a cut would
// keep.
func TestPreviewUsesTheWholeHTML(t *testing.T) {
	cases := map[string]string{
		"text after a long hidden part": hiddenFiller + `<p>Please wire the money today.</p>`,
		"a style rule after the cut":    lateStyle,
	}
	for name, html := range cases {
		t.Run("a new message: "+name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()
			c := mail.Compose{To: []string{"bob@example.com"}, Subject: "S", HTMLBody: html}

			_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.NoError(t, err)
			require.Contains(t, needs.Pending.Preview.BodyStart, "Please wire the money today.")
			require.NotContains(t, needs.Pending.Preview.BodyStart, "Decoy")
		})

		t.Run("a saved draft: "+name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()
			env.backend.addMessage("d1", draftWithHTML(html), true)
			draft := mail.Ref("d1")

			_, needs, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

			require.NoError(t, err)
			require.Contains(t, needs.Pending.Preview.BodyStart, "Please wire the money today.")
			require.NotContains(t, needs.Pending.Preview.BodyStart, "Decoy")
			require.NotContains(t, needs.Pending.Preview.BodyStart, "Harmless")
		})
	}
}

// A draft whose HTML cannot be turned into text is not sent. Its text part is
// not shown instead: it can say something else than the HTML.
func TestDraftWithHTMLTooComplexIsNotSent(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.addMessage("d1", draftWithHTML(strings.Repeat("<div>", 1000)+"Please wire the money today."), true)
	draft := mail.Ref("d1")

	_, _, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, ConfirmInBand, nil)

	require.ErrorIs(t, err, ErrDraftUnreadable)
	require.Empty(t, env.waitingActions())
	require.Empty(t, env.sender.sent())
}
