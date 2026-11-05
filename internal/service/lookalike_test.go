package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
)

// lookAlike has a Cyrillic letter (U+0430) in place of the Latin "a" of example.
const lookAlike = "bob@ex\u0430mple.com"

// foreignLocal is a real address form (SMTPUTF8) that has no ASCII spelling.
const foreignLocal = "jos\u00e9@example.com"

func TestPreviewWarnsAboutALookAlikeDomain(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose(lookAlike)

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.NoError(t, err)
	require.NotNil(t, needs)
	require.Equal(t, []string{lookAlike}, needs.Pending.Preview.To, "the envelope address stays as it is")
	require.Len(t, needs.Pending.Preview.Warnings, 1)
	require.Contains(t, needs.Pending.Preview.Warnings[0], "xn--exmple-4nf.com")
	require.Contains(t, needs.Pending.Preview.Warnings[0], "Cyrillic and Latin")
}

func TestApprovalDetailShowsTheASCIIForm(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose(lookAlike)
	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	pending, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)

	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Equal(t, []string{mime.ShownAddress(lookAlike)}, detail.Recipients)
	require.Contains(t, detail.Recipients[0], "xn--exmple-4nf.com")
	require.Len(t, detail.Warnings, 1)
	require.Equal(t, []string{mime.ShownAddress(lookAlike)}, pending.Preview.To, "the list of the page shows it too")
	require.Equal(t, detail.Warnings, pending.Preview.Warnings)
}

// An address outside printable ASCII always counts as new. A look-alike that
// was sent to once must not pass as known later, and the switch of the check
// must not turn this off.
func TestNonASCIIRecipientAlwaysAsks(t *testing.T) {
	cases := map[string]bool{"check off": false, "check on": true}
	for name, check := range cases {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(autonomy, func(a *account.Account) { a.Limits.NewRecipientCheck = check })
			ctx := env.ctx()
			c := compose(lookAlike)

			_, needs, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
			require.NoError(t, err)
			require.NotNil(t, needs, "the user must see this address, with the check on or off")
			requireEntry(t, env.lastEntry(), "send_message", audit.OutcomePendingApproval, "new_recipient")

			res, _, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, accept(needs))
			require.NoError(t, err)
			require.NotEmpty(t, res.SendID)

			_, again, err := env.svc.Send(ctx, a.ID, &c, nil, ConfirmInBand, nil)
			require.NoError(t, err)
			require.NotNil(t, again, "a second send asks again")
		})
	}
}

func TestPlainRecipientDoesNotAskWithoutTheCheck(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	c := compose("bob@xn--exmple-4nf.com")

	res, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.NoError(t, err)
	require.Nil(t, needs, "an address in plain ASCII shows exactly what it is")
	require.NotEmpty(t, res.SendID)
}

func TestSendRefusesATypedLocalPartOutsideASCII(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose(foreignLocal)

	_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.ErrorIs(t, err, mime.ErrNonASCIILocalPart)
	require.Empty(t, env.waitingActions())
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "invalid_request")
}

func TestCreateDraftRefusesATypedLocalPartOutsideASCII(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	_, err := env.svc.CreateDraft(env.ctx(), a.ID, mail.Compose{To: []string{foreignLocal}, Subject: "Hi", Body: "Hello"})

	require.ErrorIs(t, err, mime.ErrNonASCIILocalPart)
}

// A reply answers a person of the thread, so the address was not typed by the
// agent. It is not refused. The user is always asked, and the preview says what
// is special about the address.
func TestReplyToALocalPartOutsideASCIIAsksWithAWarning(t *testing.T) {
	cases := map[string]mail.ReplyRef{
		"reply":        {Ref: "o1"},
		"reply to all": {Ref: "o1", All: true},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account(autonomy)
			env.backend.originals["o1"] = original(mail.Address{Email: foreignLocal})
			c := mail.Compose{ReplyTo: &ref, Body: "Yes."}

			_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.NoError(t, err)
			require.NotNil(t, needs, "the user must see this address")
			require.Contains(t, needs.Pending.Preview.To, foreignLocal)
			require.Len(t, needs.Pending.Preview.Warnings, 1)
			require.Contains(t, needs.Pending.Preview.Warnings[0], "before the @")
		})
	}
}

func TestSendRefusesADomainWithNoASCIIForm(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.originals["o1"] = original(mail.Address{Email: "bob@\u0301name.example"})
	c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}

	_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.ErrorIs(t, err, ErrHiddenAddress)
	env.requireNothingQueued()
}

func TestSamePreviewComparesTheWarnings(t *testing.T) {
	stored := confirm.Preview{From: "me@example.com", To: []string{lookAlike}, Warnings: []string{"one"}}
	rebuilt := stored
	require.True(t, samePreview(stored, rebuilt))

	rebuilt.Warnings = nil
	require.False(t, samePreview(stored, rebuilt))
	rebuilt.Warnings = []string{"two"}
	require.False(t, samePreview(stored, rebuilt))
}
