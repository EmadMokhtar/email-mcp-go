package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// hiddenAddresses are envelope addresses with a character that
// mime.CleanUntrusted removes: a confirmation could not show them exactly.
var hiddenAddresses = []string{
	"carol\u3164@example.com", // Hangul filler
	"bob@exam\u00adple.com",   // soft hyphen
	"dan\u200b@example.com",   // zero width space
	"erin\u034f@example.com",  // combining grapheme joiner
	"fay@example.com\u2060",   // word joiner
	// Removed since the review of PR #3 (item 3).
	"gus\U0001D173@example.com", // musical symbol begin beam (Cf)
	"hal\u180b@example.com",     // Mongolian free variation selector
	"ivy\ufffc@example.com",     // object replacement character
}

// requireNothingQueued checks that no action waits, no message was sent, and
// the outbox holds nothing.
func (e *writeEnv) requireNothingQueued() {
	e.t.Helper()
	require.Empty(e.t, e.waitingActions(), "no pending action")
	require.Empty(e.t, e.sender.sent(), "nothing sent")
	var items int
	require.NoError(e.t, e.f.rawDB().QueryRowContext(context.Background(), `SELECT count(*) FROM outbox`).Scan(&items))
	require.Zero(e.t, items, "nothing queued")
}

// A send to an address that a confirmation could not show exactly is refused
// before the user is asked, on every path: in band, on the web page, and
// without a confirmation.
func TestSendRefusesAnAddressThatCannotBeShown(t *testing.T) {
	paths := map[string]struct {
		mode   ConfirmMode
		change []func(*account.Account)
	}{
		"in band":         {ConfirmInBand, nil},
		"web page":        {ConfirmFallback, nil},
		"no confirmation": {ConfirmInBand, []func(*account.Account){autonomy}},
	}
	for name, path := range paths {
		for _, field := range []string{"to", "cc", "bcc"} {
			for i, hidden := range hiddenAddresses {
				// The name has no "#": rawDB opens the database by a file URI, and
				// a "#" in the temporary path would end it.
				t.Run(fmt.Sprintf("%s/%s/%d", name, field, i), func(t *testing.T) {
					env := newWriteEnv(t)
					a := env.account(path.change...)
					c := mail.Compose{To: []string{"bob@example.com"}, Subject: "Hi", Body: "Hello"}
					switch field {
					case "to":
						c.To = append(c.To, hidden)
					case "cc":
						c.Cc = []string{hidden}
					default:
						c.Bcc = []string{hidden}
					}

					res, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, path.mode, nil)
					require.ErrorIs(t, err, ErrHiddenAddress)
					require.ErrorIs(t, err, ErrInvalidRequest)
					require.Nil(t, res)
					require.Nil(t, needs)
					env.requireNothingQueued()
					requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "hidden_address")
				})
			}
		}
	}
}

// The address that a reply adds comes from the original message, which a
// stranger wrote. It is checked too.
func TestSendRefusesAReplyToAnAddressThatCannotBeShown(t *testing.T) {
	for _, mode := range []ConfirmMode{ConfirmInBand, ConfirmFallback} {
		env := newWriteEnv(t)
		a := env.account()
		env.backend.originals["o1"] = original(mail.Address{Email: "collector\u3164@example.com"})
		c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}

		_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, mode, nil)
		require.ErrorIs(t, err, ErrHiddenAddress)
		env.requireNothingQueued()
	}
}

// A saved draft is checked when it is sent.
func TestSendRefusesADraftToAnAddressThatCannotBeShown(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.addMessage("d1", crlf("From: Me <me@example.com>\n"+
		"To: ann@example.com\n"+
		"Bcc: hidden\u3164@example.com\n"+
		"Subject: Draft\n"+
		"MIME-Version: 1.0\n"+
		"Content-Type: text/plain; charset=utf-8\n"+
		"\n"+
		"Hello.\n"), true)
	draft := mail.Ref("d1")

	for _, mode := range []ConfirmMode{ConfirmInBand, ConfirmFallback} {
		_, _, err := env.svc.Send(env.ctx(), a.ID, nil, &draft, mode, nil)
		require.ErrorIs(t, err, ErrHiddenAddress)
	}
	env.requireNothingQueued()
}

// A plain address that only looks unusual is accepted: the check refuses only
// what the confirmation would change.
func TestSendAcceptsAPlainAddress(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := mail.Compose{To: []string{"Bob.Smith+news@example.com", "ann@xn--bcher-kva.example"}, Subject: "Hi", Body: "Hello"}

	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)
	require.Equal(t, SendPendingApproval, res.Status)
}
