package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The fixed texts of the settings page.
const (
	textAutonomyNeeded = "To turn off Confirm sends, to let the agent send with it off, or to keep both confirmations off, tick the box that accepts the risk."
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

// Turning off Confirm sends alone is the step that lets mail leave with no
// check, so it needs the box.
func TestSendsOffRequiresAcceptance(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, body := p.post(settingsPath(a), settingsForm("confirm_sends", ""))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textAutonomyNeeded)
	require.False(t, hasAttr(element(t, body, atom.Input, "accept_autonomy_risk"), "checked"), "the box is never ticked for the user")
	require.False(t, hasAttr(element(t, body, atom.Input, "confirm_sends"), "checked"), "the page keeps what the user chose")
	p.requireUnchanged(a)
	require.Empty(t, p.entries())
}

func TestSendsOffAcceptanceRecorded(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.clock.Set(t0.Add(time.Hour))

	resp, _ := p.post(settingsPath(a), settingsForm("confirm_sends", "", "accept_autonomy_risk", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	got, _ := p.reload(a)
	require.False(t, got.ConfirmSends)
	require.True(t, got.ConfirmDeletes)
	require.Equal(t, t0.Add(time.Hour), *got.AutonomyAcceptedAt)
	details := []string{}
	for _, e := range p.entries() {
		details = append(details, e.Detail)
	}
	require.ElementsMatch(t, []string{"settings_changed", "autonomy_accepted"}, details)

	_, body := p.get(settingsPath(a))
	require.Contains(t, visibleText(t, body), "You last accepted this risk on")
}

// The box sits next to the switch it is about, and it is in the page without
// any script.
func TestRiskBoxIsNextToConfirmSends(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	_, body := p.get(settingsPath(a))
	sends := strings.Index(body, `id="confirm_sends"`)
	risk := strings.Index(body, `id="accept_autonomy_risk"`)
	deletes := strings.Index(body, `id="confirm_deletes"`)
	require.Positive(t, sends)
	require.Greater(t, risk, sends)
	require.Greater(t, deletes, risk)

	for n := element(t, body, atom.Input, "accept_autonomy_risk"); n != nil; n = n.Parent {
		require.False(t, hasAttr(n, "hidden"), "the box shows without a script")
	}
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

// levelPath returns the address of the route that applies a requested level.
func levelPath(a *account.Account) string {
	return "/accounts/" + string(a.ID) + "/level"
}

// requireApplyButton checks that the page offers the level in a form of its
// own, which posts that level to the route of the button.
func requireApplyButton(t *testing.T, page string, a *account.Account, level string) {
	t.Helper()
	input := element(t, page, atom.Input, "apply_mail_level")
	require.NotNil(t, input, "the page has the button")
	require.Equal(t, level, attr(input, "value"))
	require.Equal(t, "hidden", attr(input, "type"))
	form := formOf(input)
	require.NotNil(t, form)
	require.Equal(t, levelPath(a), attr(form, "action"))
	require.Equal(t, "post", attr(form, "method"))
}

// formOf returns the form that holds an element, or nil.
func formOf(n *html.Node) *html.Node {
	for n != nil && n.DataAtom != atom.Form {
		n = n.Parent
	}
	return n
}

// The link of request_permission_change (chosen by the agent) only asks. It
// changes nothing, ticks no box, and the level in the form stays the level that
// the account has. A value that is not exactly a known area and a level from 0
// to 5 is ignored.
func TestSettingsLinkOnlyAsks(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, body := p.get(settingsPath(a) + "?area=mail&level=5&accept_autonomy_risk=1&confirm_sends=0&confirm_deletes=0")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "4", selectedLevel(t, body), "the form keeps the current level")
	text := visibleText(t, body)
	require.Contains(t, text, "Nothing has changed yet.")
	require.Contains(t, text, "Apply requested level 5")
	require.Contains(t, text, "lets the agent send mail and move mail to Trash", "the page names what the level allows")
	requireApplyButton(t, body, a, "5")
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
		require.Nil(t, element(t, body, atom.Input, "apply_mail_level"), query)
	}
	p.requireUnchanged(a)
	require.Empty(t, p.entries())
	require.Empty(t, p.backends.forgot())
}

// Each level has its own words on the page, and a request for the level that
// the account already has offers no button.
func TestSettingsLinkNamesWhatTheLevelAllows(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	allows := map[string]string{
		"0": "stops the agent from using this mail account",
		"1": "lets the agent read mail",
		"2": "lets the agent read and organize mail",
		"3": "lets the agent read and organize mail, and write drafts",
		"5": "lets the agent send mail and move mail to Trash",
	}
	for level, text := range allows {
		_, body := p.get(settingsPath(a) + "?area=mail&level=" + level)
		require.Contains(t, visibleText(t, body), text, level)
		requireApplyButton(t, body, a, level)
	}

	_, body := p.get(settingsPath(a) + "?area=mail&level=4")
	text := visibleText(t, body)
	require.Contains(t, text, "Nothing has changed yet.")
	require.Contains(t, text, "Mail is already at this level.")
	require.NotContains(t, text, "Apply requested level")
	require.Nil(t, element(t, body, atom.Input, "apply_mail_level"))
}

// The page says so when the requested level would act without asking, because
// a confirmation is off.
func TestSettingsLinkWarnsWhenAConfirmationIsOff(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	const noSendCheck = "Confirm sends is off, so the agent will send mail without asking you."
	const noTrashCheck = "Confirm deletes is off, so the agent will move mail to Trash without asking you."

	_, body := p.get(settingsPath(a) + "?area=mail&level=5")
	require.NotContains(t, visibleText(t, body), noSendCheck)
	require.NotContains(t, visibleText(t, body), noTrashCheck)

	resp, _ := p.post(settingsPath(a), settingsForm("mail_level", "1", "confirm_sends", "", "confirm_deletes", "",
		"accept_autonomy_risk", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	_, body = p.get(settingsPath(a) + "?area=mail&level=5")
	require.Contains(t, visibleText(t, body), noSendCheck)
	require.Contains(t, visibleText(t, body), noTrashCheck)

	_, body = p.get(settingsPath(a) + "?area=mail&level=3")
	require.NotContains(t, visibleText(t, body), noSendCheck, "level 3 does not send")
	require.NotContains(t, visibleText(t, body), noTrashCheck)
}

// A user who opens the link and presses Save does not raise the level. Save
// posts what the form shows, and the form shows the current level.
func TestSaveAfterASettingsLinkKeepsTheLevel(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	_, body := p.get(settingsPath(a) + "?area=mail&level=5")
	resp, _ := p.post(settingsPath(a), settingsForm("mail_level", selectedLevel(t, body), "sends_per_hour", "7"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	got, _ := p.reload(a)
	require.Equal(t, account.MailSend, got.Level(account.AreaMail))
	require.Equal(t, 7, got.Limits.SendsPerHour)
}

// The button of a request changes the level and nothing else, whatever else the
// form holds.
func TestApplyRequestedLevelChangesOnlyTheLevel(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	resp, _ := p.post(levelPath(a), url.Values{
		"mail_level":      {"5"},
		"confirm_deletes": {"1"},
		"sends_per_hour":  {"1"},
		"save_sent_copy":  {"1"},
	})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, settingsPath(a), resp.Header.Get("Location"))

	got, _ := p.reload(a)
	require.Equal(t, account.MailDelete, got.Level(account.AreaMail))
	want := *a
	want.Levels = map[account.Area]account.Level{account.AreaMail: account.MailDelete}
	require.Equal(t, &want, got, "nothing else changed")

	entries := p.entries()
	require.Len(t, entries, 1)
	require.Equal(t, "update_settings", entries[0].Tool)
	require.Equal(t, "settings_changed", entries[0].Detail)
	require.Equal(t, a.ID, entries[0].Account)
	require.Equal(t, "web", entries[0].Client)
}

// The button follows the rules of Save. With both confirmations off, a save
// needs the box, so the button does too, and the page offers the box.
func TestApplyRequestedLevelFollowsTheAcceptanceRule(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	resp, _ := p.post(settingsPath(a), settingsForm("mail_level", "1", "confirm_sends", "", "confirm_deletes", "",
		"accept_autonomy_risk", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	before, _ := p.reload(a)

	_, body := p.get(settingsPath(a) + "?area=mail&level=5")
	box := element(t, body, atom.Input, "apply_accept_autonomy_risk")
	require.NotNil(t, box, "the form of the button has the box")
	require.False(t, hasAttr(box, "checked"), "the box is never ticked for the user")

	entries := len(p.entries())
	resp, body = p.post(levelPath(a), url.Values{"mail_level": {"5"}})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textAutonomyNeeded)
	requireApplyButton(t, body, a, "5")
	require.Equal(t, "1", selectedLevel(t, body), "the form keeps the current level")
	p.requireUnchanged(before)
	require.Len(t, p.entries(), entries, "a refusal writes no entry")

	p.clock.Set(t0.Add(time.Hour))
	resp, _ = p.post(levelPath(a), url.Values{"mail_level": {"5"}, "accept_autonomy_risk": {"1"}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ := p.reload(a)
	require.Equal(t, account.MailDelete, got.Level(account.AreaMail))
	require.False(t, got.ConfirmSends)
	require.False(t, got.ConfirmDeletes)
	require.Equal(t, t0.Add(time.Hour), *got.AutonomyAcceptedAt)
}

// With one confirmation still on, a level change needs no acceptance, so the
// form of the button has no box.
func TestApplyRequestedLevelHasNoBoxWhenNoneIsNeeded(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()

	_, body := p.get(settingsPath(a) + "?area=mail&level=5")
	require.Nil(t, element(t, body, atom.Input, "apply_accept_autonomy_risk"))
}

func TestApplyRequestedLevelRefusesBadValues(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	for name, form := range map[string]url.Values{
		"level 6":      {"mail_level": {"6"}},
		"not a number": {"mail_level": {"x"}},
		"no level":     {},
		"two levels":   {"mail_level": {"5", "1"}},
		"two digits":   {"mail_level": {"05"}},
	} {
		resp, body := p.post(levelPath(a), form)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, name)
		require.Contains(t, visibleText(t, body), textBadLevel, name)
	}
	p.requireUnchanged(a)
	require.Empty(t, p.entries())
}

// sendsOffAt saves the account at a level with only Confirm sends off.
func (p *pageFixture) sendsOffAt(a *account.Account, level string) {
	p.t.Helper()
	resp, _ := p.post(settingsPath(a), settingsForm("mail_level", level, "confirm_sends", "", "accept_autonomy_risk", "1"))
	require.Equal(p.t, http.StatusSeeOther, resp.StatusCode)
}

// Raising the level to Send with Confirm sends off starts unattended sends, so
// Save needs the box.
func TestSaveRaisingTheLevelWithSendsOffNeedsAcceptance(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.sendsOffAt(a, "3")
	before, _ := p.reload(a)

	resp, body := p.post(settingsPath(a), settingsForm("mail_level", "4", "confirm_sends", ""))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textAutonomyNeeded)
	p.requireUnchanged(before)

	p.clock.Set(t0.Add(time.Hour))
	resp, _ = p.post(settingsPath(a), settingsForm("mail_level", "4", "confirm_sends", "", "accept_autonomy_risk", "1"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ := p.reload(a)
	require.Equal(t, account.MailSend, got.Level(account.AreaMail))
	require.Equal(t, t0.Add(time.Hour), *got.AutonomyAcceptedAt)
}

// The button follows the same rule. The box is beside the button, in the same
// form, also on the page that comes back after a refusal, so the user can
// always go on.
func TestApplyRaisingTheLevelWithSendsOffNeedsAcceptance(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.sendsOffAt(a, "3")
	before, _ := p.reload(a)
	requireBoxBesideButton := func(page string) {
		t.Helper()
		requireApplyButton(t, page, a, "4")
		box := element(t, page, atom.Input, "apply_accept_autonomy_risk")
		require.NotNil(t, box, "the form of the button has the box")
		require.False(t, hasAttr(box, "checked"), "the box is never ticked for the user")
		require.Equal(t, "accept_autonomy_risk", attr(box, "name"))
		require.Equal(t, levelPath(a), attr(formOf(box), "action"), "the box is in the form of the button")
		require.Equal(t, autonomyText, labelFor(t, page, "apply_accept_autonomy_risk"))
	}

	_, body := p.get(settingsPath(a) + "?area=mail&level=4")
	requireBoxBesideButton(body)
	require.Contains(t, visibleText(t, body), "Confirm sends is off, so the agent will send mail without asking you.")

	entries := len(p.entries())
	resp, body := p.post(levelPath(a), url.Values{"mail_level": {"4"}})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textAutonomyNeeded)
	requireBoxBesideButton(body)
	p.requireUnchanged(before)
	require.Len(t, p.entries(), entries, "a refusal writes no entry")

	p.clock.Set(t0.Add(time.Hour))
	resp, _ = p.post(levelPath(a), url.Values{"mail_level": {"4"}, "accept_autonomy_risk": {"1"}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ := p.reload(a)
	require.Equal(t, account.MailSend, got.Level(account.AreaMail))
	require.False(t, got.ConfirmSends)
	require.True(t, got.ConfirmDeletes)
	require.Equal(t, t0.Add(time.Hour), *got.AutonomyAcceptedAt)
}

// Below Send level the agent cannot send, so the button needs no box.
func TestApplyBelowSendWithSendsOffNeedsNoBox(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.sendsOffAt(a, "1")
	before, _ := p.reload(a)

	_, body := p.get(settingsPath(a) + "?area=mail&level=3")
	require.Nil(t, element(t, body, atom.Input, "apply_accept_autonomy_risk"))

	p.clock.Set(t0.Add(time.Hour))
	resp, _ := p.post(levelPath(a), url.Values{"mail_level": {"3"}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ := p.reload(a)
	require.Equal(t, account.MailDraft, got.Level(account.AreaMail))
	require.Equal(t, *before.AutonomyAcceptedAt, *got.AutonomyAcceptedAt)
}

// An account that already sends unattended gets no new prompt for a move from
// Send to Delete.
func TestApplyWhileUnattendedNeedsNoBox(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.sendsOffAt(a, "4")
	before, _ := p.reload(a)

	_, body := p.get(settingsPath(a) + "?area=mail&level=5")
	require.Nil(t, element(t, body, atom.Input, "apply_accept_autonomy_risk"))

	p.clock.Set(t0.Add(time.Hour))
	resp, _ := p.post(levelPath(a), url.Values{"mail_level": {"5"}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ := p.reload(a)
	require.Equal(t, account.MailDelete, got.Level(account.AreaMail))
	require.Equal(t, *before.AutonomyAcceptedAt, *got.AutonomyAcceptedAt)
}

// Staying at Send level with Confirm sends off and changing a limit asks
// nothing new.
func TestSaveStayingUnattendedNeedsNoAcceptance(t *testing.T) {
	p := newPageFixture(t)
	a := p.account()
	p.sendsOffAt(a, "4")
	before, _ := p.reload(a)

	p.clock.Set(t0.Add(time.Hour))
	resp, _ := p.post(settingsPath(a), settingsForm("confirm_sends", "", "sends_per_hour", "3"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	got, _ := p.reload(a)
	require.Equal(t, 3, got.Limits.SendsPerHour)
	require.Equal(t, *before.AutonomyAcceptedAt, *got.AutonomyAcceptedAt)
}

func TestApplyRequestedLevelOfAnotherTenantIsNotFound(t *testing.T) {
	p := newPageFixture(t)
	p.addTenant("other")
	theirs := p.addAccount("other", "them@example.com")

	resp, body := p.post(levelPath(theirs), url.Values{"mail_level": {"5"}})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textNoAccount)
	p.requireUnchanged(theirs)
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
	require.Equal(t, pathAccounts, resp.Header.Get("Location"))
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

	resp, _ = p.get("/accounts")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
