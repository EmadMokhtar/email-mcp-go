package web

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The fixed texts of the settings page.
const (
	textAutonomyNeeded = "To turn off both confirmations, tick the box that accepts the risk."
	textRemoveWarning  = "Removing the account also deletes its messages that wait in the outbox and are not sent yet. They are never sent."
	textRemoveConfirm  = "To remove the account, tick the box that confirms it."
	textBadLevel       = "Choose a mail level from 0 to 5."
	textBadLimits      = "The limits are not valid."
	textNoAccount      = "This account was not found."
)

// settingsForm is the form of the settings page for the defaults of a new
// account, with the changes as name and value pairs. An empty value removes
// the field, as an unticked box does.
func settingsForm(kv ...string) url.Values {
	form := url.Values{
		"mail_level":             {"4"},
		"confirm_sends":          {"1"},
		"confirm_deletes":        {"1"},
		"sends_per_hour":         {"20"},
		"sends_per_day":          {"200"},
		"recipients_per_message": {"50"},
		"trash_per_day":          {"500"},
		"undo_window_auto":       {"30"},
		"undo_window_confirmed":  {"0"},
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			form.Del(kv[i])
			continue
		}
		form.Set(kv[i], kv[i+1])
	}
	return form
}

func settingsPath(a *account.Account) string {
	return "/accounts/" + string(a.ID) + "/settings"
}

// requireUnchanged checks that the account is as it was.
func (p *pageFixture) requireUnchanged(a *account.Account) {
	p.t.Helper()
	got, ok := p.reload(a)
	require.True(p.t, ok, "the account still exists")
	require.Equal(p.t, a, got, "the account is unchanged")
}

func TestSettingsPageShowsTheAccount(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, body := p.get(settingsPath(a))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, "me@example.com")
	require.Contains(t, text, textRemoveWarning)
	require.Equal(t, autonomyText, labelFor(t, body, "accept_autonomy_risk"), "the label is the warning of spec 7.3")
	risk := element(t, body, atom.Input, "accept_autonomy_risk")
	require.NotNil(t, risk)
	require.False(t, hasAttr(risk, "checked"))
	require.Equal(t, "4", selectedLevel(t, body))
	require.True(t, hasAttr(element(t, body, atom.Input, "confirm_sends"), "checked"))
	require.True(t, hasAttr(element(t, body, atom.Input, "confirm_deletes"), "checked"))
	require.Equal(t, "30", attr(element(t, body, atom.Input, "undo_window_auto"), "value"))
	require.Empty(t, p.entries(), "showing the page changes nothing")
}

// selectedLevel returns the value of the selected option of the mail level.
func selectedLevel(t *testing.T, page string) string {
	t.Helper()
	sel := element(t, page, atom.Select, "mail_level")
	require.NotNil(t, sel)
	selected := ""
	for n := range sel.Descendants() {
		if n.DataAtom == atom.Option && hasAttr(n, "selected") {
			require.Empty(t, selected, "one option is selected")
			selected = attr(n, "value")
		}
	}
	return selected
}

func TestAutonomyRequiresAcceptance(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, body := p.post(settingsPath(a), settingsForm("confirm_sends", "", "confirm_deletes", "", "mail_level", "5"))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textAutonomyNeeded)
	require.False(t, hasAttr(element(t, body, atom.Input, "accept_autonomy_risk"), "checked"), "the box is never ticked for the user")
	p.requireUnchanged(a)
	require.Empty(t, p.backends.forgot())
	require.Empty(t, p.entries())
}

func TestAutonomyAcceptedRecorded(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.clock.Set(t0.Add(time.Hour))

	resp, _ := p.post(settingsPath(a), settingsForm("confirm_sends", "", "confirm_deletes", "", "accept_autonomy_risk", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, settingsPath(a), resp.Header.Get("Location"))

	got, _ := p.reload(a)
	require.False(t, got.ConfirmSends)
	require.False(t, got.ConfirmDeletes)
	require.NotNil(t, got.AutonomyAcceptedAt)
	require.Equal(t, t0.Add(time.Hour), *got.AutonomyAcceptedAt)
	var accepted []audit.Entry
	for _, e := range p.entries() {
		if e.Detail == "autonomy_accepted" {
			accepted = append(accepted, e)
		}
	}
	require.Len(t, accepted, 1)
	require.Equal(t, a.ID, accepted[0].Account)
	require.Equal(t, "web", accepted[0].Client)

	// The page shows the acceptance, and the box still starts empty.
	resp, body := p.get(settingsPath(a))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.False(t, hasAttr(element(t, body, atom.Input, "accept_autonomy_risk"), "checked"))
	require.False(t, hasAttr(element(t, body, atom.Input, "confirm_sends"), "checked"))

	// Every save that keeps both confirmations off needs the box again.
	before, _ := p.reload(a)
	resp, _ = p.post(settingsPath(a), settingsForm("confirm_sends", "", "confirm_deletes", "", "sends_per_hour", "3"))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	p.requireUnchanged(before)
	p.clock.Set(t0.Add(2 * time.Hour))
	resp, _ = p.post(settingsPath(a), settingsForm("confirm_sends", "", "confirm_deletes", "", "sends_per_hour", "3",
		"accept_autonomy_risk", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ = p.reload(a)
	require.Equal(t, 3, got.Limits.SendsPerHour)
	require.Equal(t, t0.Add(2*time.Hour), *got.AutonomyAcceptedAt, "the new acceptance has the new time")
}

func TestLevelChangeAudited(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, _ := p.post(settingsPath(a), settingsForm("mail_level", "2", "confirm_deletes", "",
		"undo_window_confirmed", "15", "new_recipient_check", "1", "save_sent_copy", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	got, _ := p.reload(a)
	require.Equal(t, account.MailOrganize, got.Level(account.AreaMail))
	require.True(t, got.ConfirmSends)
	require.False(t, got.ConfirmDeletes)
	require.Equal(t, 15*time.Second, got.Limits.UndoWindowConfirmed)
	require.True(t, got.Limits.NewRecipientCheck)
	require.True(t, got.Mail.SaveSentCopy)
	require.Nil(t, got.AutonomyAcceptedAt)
	require.Empty(t, p.backends.forgot(), "a save does not close a backend that a call may use")

	entries := p.entries()
	require.Len(t, entries, 1)
	require.Equal(t, "update_settings", entries[0].Tool)
	require.Equal(t, "settings_changed", entries[0].Detail)
	require.Equal(t, audit.OutcomeOK, entries[0].Outcome)
	require.Equal(t, a.ID, entries[0].Account)
	require.Equal(t, "web", entries[0].Client)

	resp, body := p.get(settingsPath(a))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "2", selectedLevel(t, body))
}

// The link of request_permission_change (chosen by the agent) only pre-selects
// the level. It changes nothing, ticks no box, and a value that is not exactly
// a known area and a level from 0 to 5 is ignored.
func TestSettingsLinkOnlyPreselectsTheLevel(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, body := p.get(settingsPath(a) + "?area=mail&level=5&accept_autonomy_risk=1&confirm_sends=0&confirm_deletes=0")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "5", selectedLevel(t, body))
	require.Contains(t, visibleText(t, body), "Nothing has changed yet.")
	require.False(t, hasAttr(element(t, body, atom.Input, "accept_autonomy_risk"), "checked"))
	require.True(t, hasAttr(element(t, body, atom.Input, "confirm_sends"), "checked"))
	require.True(t, hasAttr(element(t, body, atom.Input, "confirm_deletes"), "checked"))

	for _, query := range []string{
		"?area=calendar&level=5", "?area=mail&level=9", "?area=mail&level=-1", "?area=mail&level=04",
		"?area=mail&level=4x", "?area=mail&level=5&level=1", "?area=mail&area=mail&level=5", "?level=5",
		"?area=MAIL&level=5", "?area=mail&level=%205",
	} {
		resp, body := p.get(settingsPath(a) + query)
		require.Equal(t, http.StatusOK, resp.StatusCode, query)
		require.Equal(t, "4", selectedLevel(t, body), "%s: the current level stays selected", query)
		require.NotContains(t, visibleText(t, body), "Nothing has changed yet.", query)
	}
	p.requireUnchanged(a)
	require.Empty(t, p.entries())
	require.Empty(t, p.backends.forgot())
}

func TestSettingsRefusesBadValues(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	cases := map[string]struct {
		form url.Values
		text string
	}{
		"level 6":            {settingsForm("mail_level", "6"), textBadLevel},
		"level not a number": {settingsForm("mail_level", "x"), textBadLevel},
		"no level":           {settingsForm("mail_level", ""), textBadLevel},
		"negative limit":     {settingsForm("sends_per_hour", "-1"), textBadLimits},
		"limit not a number": {settingsForm("trash_per_day", "many"), textBadLimits},
		"huge limit":         {settingsForm("sends_per_day", strconv.Itoa(1_000_001)), textBadLimits},
		"undo too long":      {settingsForm("undo_window_auto", "301"), textBadLimits},
		"undo overflow":      {settingsForm("undo_window_auto", "9223372037"), textBadLimits},
	}
	for name, c := range cases {
		resp, body := p.post(settingsPath(a), c.form)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, name)
		require.Contains(t, visibleText(t, body), c.text, name)
	}
	p.requireUnchanged(a)
	require.Empty(t, p.entries())
}

func TestSettingsOfAnotherTenantAreNotFound(t *testing.T) {
	p := newPageFixture(t)
	p.addTenant("other")
	theirs := p.addAccount("other", "them@example.com")

	resp, body := p.get(settingsPath(theirs))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textNoAccount)
	require.NotContains(t, body, "them@example.com")
	resp, _ = p.post(settingsPath(theirs), settingsForm("mail_level", "5"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = p.post("/accounts/"+string(theirs.ID)+"/remove", url.Values{"confirm_remove": {"1"}})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	p.requireUnchanged(theirs)
	resp, _ = p.get("/accounts/no-such-account/settings")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRemoveAccount(t *testing.T) {
	p := newPageFixture(t)
	a := p.account(func(a *account.Account) { a.ConfirmSends, a.ConfirmDeletes = false, false })
	keep := p.addAccount(p.tenant, "keep@example.com")
	c := mail.Compose{To: []string{"bob@example.com"}, Subject: "Hi", Body: "Hello"}
	res, _, err := p.svc.Send(agent(p.tenant), a.ID, &c, nil, service.ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, "queued", res.Status, "the message waits for its undo window")
	removePath := "/accounts/" + string(a.ID) + "/remove"

	resp, body := p.post(removePath, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textRemoveConfirm)
	_, ok := p.reload(a)
	require.True(t, ok, "without the tick nothing is removed")

	resp, _ = p.post(removePath, url.Values{"confirm_remove": {"1"}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"))
	_, ok = p.reload(a)
	require.False(t, ok)
	_, _, err = p.store.GetAccountSecret(t.Context(), p.tenant, a.ID, "password")
	require.Error(t, err, "the password is deleted")
	_, err = p.outbox.Get(t.Context(), p.tenant, res.SendID)
	require.Error(t, err, "the queued message is deleted")
	_, ok = p.reload(keep)
	require.True(t, ok)
	require.Equal(t, []account.AccountID{a.ID}, p.backends.forgot())
	last := p.entries()[0]
	require.Equal(t, "remove_account", last.Tool)
	require.Equal(t, "account_removed", last.Detail)
	require.Empty(t, last.Account)
	require.Empty(t, last.Recipients)
	require.Empty(t, p.sender.sent(), "the queued message is never sent")

	resp, _ = p.get("/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
