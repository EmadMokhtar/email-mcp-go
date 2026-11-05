package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/mime"
)

// lookAlike has a Cyrillic letter (U+0430) in place of the Latin "a" of example.
const lookAlike = "bob@ex\U00000430mple.com"

// foreignLocal is a real address form (SMTPUTF8) that has no ASCII spelling.
const foreignLocal = "jos\U000000e9@example.com"

// hiddenInForeign has a local part outside ASCII and a zero width space.
const hiddenInForeign = "jos\U000000e9\U0000200b@example.com"

// inexactDomains are domains that IDNA would change before it encodes them, or
// cannot encode at all.
var inexactDomains = map[string]string{
	"full-width letter": "bob@\U0000ff45xample.com",
	"capital letter":    "bob@B\U000000fccher.de",
	"no ascii form":     "bob@\U00000301name.example",
}

func TestPreviewWarnsAboutALookAlikeDomain(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose(lookAlike)

	_, needs, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

	require.NoError(t, err)
	require.NotNil(t, needs)
	require.Equal(t, []string{lookAlike}, needs.Pending.Preview.To, "the envelope address stays as it is")
	require.Equal(t, lookAlike+" (xn--exmple-4nf.com)", mime.ShownAddress(needs.Pending.Preview.To[0]))
	require.Len(t, needs.Pending.Preview.Warnings, 1)
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
	require.Equal(t, []string{lookAlike + " (xn--exmple-4nf.com)"}, detail.Recipients)
	require.Len(t, detail.Warnings, 1)
	require.Equal(t, []string{lookAlike + " (xn--exmple-4nf.com)"}, pending.Preview.To, "the list of the page shows it too")
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
	require.Empty(t, env.backend.saved)
}

// A draft cannot carry an address that a send would refuse. The user could send
// it from another mail program, where nothing checks it.
func TestCreateDraftRefusesWhatASendRefuses(t *testing.T) {
	cases := map[string]struct {
		address string
		want    error
		code    string
	}{
		"hidden and outside ASCII": {hiddenInForeign, ErrHiddenAddress, "hidden_address"},
		"hidden in plain text":     {"ann\U0000200b@example.com", ErrHiddenAddress, "hidden_address"},
		"a domain IDNA changes":    {"bob@\U0000ff45xample.com", ErrInexactDomain, "inexact_domain"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()

			_, err := env.svc.CreateDraft(env.ctx(), a.ID, mail.Compose{To: []string{tc.address}, Subject: "Hi", Body: "Hello"})

			require.ErrorIs(t, err, tc.want)
			require.Empty(t, env.backend.saved, "nothing is saved")
			requireEntry(t, env.lastEntry(), "create_draft", audit.OutcomeRefused, tc.code)
		})
	}
}

func TestCreateDraftRefusesAReplyToAHiddenAddress(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	env.backend.originals["o1"] = original(mail.Address{Email: "ann\U0000200b@example.com"})

	_, err := env.svc.CreateDraft(env.ctx(), a.ID, mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."})

	require.ErrorIs(t, err, ErrHiddenAddress)
	require.Empty(t, env.backend.saved)
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

// A hidden character is part of the real address. A reply is not sent to the
// address without it, which is another mailbox. It is refused instead.
func TestReplyNeverGoesToTheAddressWithoutItsHiddenCharacter(t *testing.T) {
	for name, address := range map[string]string{
		"zero width space":  "ceo\U0000200b@corp.example",
		"zero width joiner": "ceo\U0000200d@corp.example",
		"braille blank":     "ceo\U00002800@corp.example",
		"private use":       "ceo\U0000e000@corp.example",
	} {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()
			env.backend.originals["o1"] = original(mail.Address{Email: address})
			c := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Body: "Yes."}

			_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.ErrorIs(t, err, ErrHiddenAddress)
			env.requireNothingQueued()
		})
	}
}

// The form next to the address is the form of the domain in the envelope. When
// IDNA would change the domain first, the two differ, so the send is refused.
func TestSendRefusesADomainThatIDNAWouldChange(t *testing.T) {
	for name, address := range inexactDomains {
		t.Run(name, func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()
			c := compose(address)

			_, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)

			require.ErrorIs(t, err, ErrInexactDomain)
			require.ErrorIs(t, err, ErrInvalidRequest)
			requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "inexact_domain")
			env.requireNothingQueued()
		})
	}
}

// One bad domain in the thread stops a reply to all: the agent does not choose
// these recipients, and leaving one out without a word would change who the
// message goes to. The error names the rule, and a plain reply still works.
func TestReplyToAllFailsClosedOnABadDomain(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	message := original()
	message.Cc = append(message.Cc, mail.Address{Email: "bob@\U0000ff45xample.com"})
	env.backend.originals["o1"] = message

	all := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1", All: true}, Body: "Yes."}
	_, _, err := env.svc.Send(env.ctx(), a.ID, &all, nil, ConfirmInBand, nil)
	require.ErrorIs(t, err, ErrInexactDomain)
	requireEntry(t, env.lastEntry(), "send_message", audit.OutcomeRefused, "inexact_domain")
	env.requireNothingQueued()

	plain := mail.Compose{ReplyTo: &mail.ReplyRef{Ref: "o1"}, Cc: []string{"dan@example.com"}, Body: "Yes."}
	_, needs, err := env.svc.Send(env.ctx(), a.ID, &plain, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"alice@example.com", "dan@example.com"}, needs.Pending.Preview.To)
}

func TestTrashLineMarksAHiddenCharacterInAnAddress(t *testing.T) {
	line := trashLine(mail.Summary{From: mail.Address{Email: "ceo\U0000200b@corp.example"}, Subject: "Hi"})

	require.Contains(t, line, "ceo\U0000fffd@corp.example")
	require.False(t, strings.Contains(line, "ceo@corp.example"))
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

// The approval page shows what PR 37 and PR 34 both add to a recipient: the
// display name after the address, in quotes, and the ASCII form of the domain
// between the two. A name cannot end its quotes, so it cannot pose as another
// address.
func TestApprovalDetailShowsTheASCIIFormAndTheDisplayName(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	c := compose(`"Bob \"x\", evil@corp.example" <` + lookAlike + `>`)
	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmFallback, nil)
	require.NoError(t, err)

	pending, detail, err := env.svc.PendingDetail(env.webCtx(), res.SendID)

	require.NoError(t, err)
	require.NotNil(t, detail)
	want := lookAlike + ` (xn--exmple-4nf.com) ("Bob 'x', evil@corp.example")`
	require.Equal(t, []string{want}, detail.Recipients)
	require.Equal(t, 2, strings.Count(detail.Recipients[0], `"`), "the name stays inside its quotes")
	require.Len(t, detail.Warnings, 1)
	require.Equal(t, detail.Recipients, pending.Preview.Recipients(), "the page without the whole message shows the same")
	require.Equal(t, detail.Warnings, pending.Preview.Warnings)
}

// samePreview compares what both fixes added: the display names, the types of
// the attachments, the hash of what goes out, and the warnings about the
// recipients. A change in any of them is a changed message.
func TestSamePreviewComparesWhatBothFixesAdded(t *testing.T) {
	base := confirm.Preview{
		From:            "me@example.com",
		To:              []string{lookAlike, "ann@example.com"},
		Names:           []string{"Bob", "Ann"},
		NamesMore:       2,
		Subject:         "Plans",
		BodyStart:       "Hello",
		BodyMore:        3,
		BodyHash:        "hash",
		Attachments:     []string{"plan.pdf"},
		AttachmentTypes: []string{"application/pdf"},
		Warnings:        []string{"Look-alike warning for " + lookAlike},
	}
	require.True(t, samePreview(base, base))

	changed := map[string]func(p *confirm.Preview){
		"a display name":      func(p *confirm.Preview) { p.Names = []string{"Bob", "Eve"} },
		"the names left out":  func(p *confirm.Preview) { p.NamesMore = 3 },
		"an attachment type":  func(p *confirm.Preview) { p.AttachmentTypes = []string{"application/x-msdownload"} },
		"the hash":            func(p *confirm.Preview) { p.BodyHash = "other" },
		"a warning":           func(p *confirm.Preview) { p.Warnings = []string{"another warning"} },
		"no warning":          func(p *confirm.Preview) { p.Warnings = nil },
		"the recipients":      func(p *confirm.Preview) { p.To = []string{lookAlike, "eve@example.com"} },
		"the count after it":  func(p *confirm.Preview) { p.BodyMore = 4 },
		"an attachment name":  func(p *confirm.Preview) { p.Attachments = []string{"other.pdf"} },
		"the start of a body": func(p *confirm.Preview) { p.BodyStart = "Hallo" },
	}
	for name, change := range changed {
		t.Run(name, func(t *testing.T) {
			rebuilt := base
			change(&rebuilt)
			require.False(t, samePreview(base, rebuilt))
		})
	}

	// Order and the case of an address do not count, for the recipients with
	// their names, and for the warnings.
	reordered := base
	reordered.To = []string{"ANN@example.com", lookAlike}
	reordered.Names = []string{"Ann", "Bob"}
	require.True(t, samePreview(base, reordered))
}
