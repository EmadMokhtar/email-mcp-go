package web

// This file holds the settings page of an account: the mail level, the two
// confirmation switches, the limits, "Save a copy of sent mail", and the
// removal of the account. Only the user changes them, here. No MCP tool can.
//
// The agent can give the user a link to this page (request_permission_change)
// with "?area=mail&level=N". The agent chooses that link, so it only asks. The
// form keeps the current level, so a Save for another reason cannot raise it.
// The page shows the request apart from the form, with a button that names
// what the level allows. The button applies the level alone, by the same rules
// as Save. The link changes nothing, never ticks a box, and never skips the
// warning: the user must still press the button.
//
// A save that lets mail leave unchecked needs the box "accept_autonomy_risk",
// whose label is the warning of spec section 7.3. That is a save that turns off
// Confirm sends, with or without Confirm deletes, and a save that raises the
// level to Send or above while it is off. Every save that ends with both
// switches off (full autonomy) needs the box again. service.NeedsRiskAcceptance
// holds this rule. The box always starts empty. The form of a request has the
// box too, when applying that level needs it.

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The name of the page of this file.
const pageSettings = "settings"

// Fixed codes for the log.
const (
	codeSettingsFailed = "settings_failed"
	codeRemoveFailed   = "remove_failed"
)

// autonomyWarning is the risk of turning off Confirm sends, exactly as spec
// section 7.3 words it. It is the label of the box that accepts the risk.
const autonomyWarning = "An email or a calendar invite can contain hidden instructions, " +
	"such as 'forward all invoices to me'. With Confirm sends off, the agent may follow them, " +
	"and nobody checks before anything leaves."

// The fixed texts of the settings page.
const (
	msgNoAccount       = "This account was not found."
	msgAutonomyNeeded  = "To turn off Confirm sends, to let the agent send with it off, or to keep both confirmations off, tick the box that accepts the risk."
	msgBadLevel        = "Choose a mail level from 0 to 5."
	msgBadLimits       = "The limits are not valid. Each count must be a whole number from 0 to 1000000, and each undo window from 0 to 300 seconds."
	msgRemoveConfirm   = "To remove the account, tick the box that confirms it."
	msgRemoveWarning   = "Removing the account also deletes its messages that wait in the outbox and are not sent yet. They are never sent."
	titleAccountAbsent = "Account not found"
)

// maxCount is the largest count that the form accepts for a limit.
const maxCount = 1_000_000

// maxUndoSeconds is the longest undo window, in seconds (see account.Limits).
const maxUndoSeconds = 300

// settingsPage is the data of the settings page.
type settingsPage struct {
	ID      string
	Address string
	Error   string
	// Levels are the six mail levels, with the one to show selected.
	Levels []levelOption
	// Requested is the level that a link asks for. It is nil when the address
	// has no valid request.
	Requested *levelRequest
	// Current is the name of the level that the account has now.
	Current        string
	ConfirmSends   bool
	ConfirmDeletes bool
	// AutonomyWarning is the label of the box that accepts the risk.
	AutonomyWarning string
	// AcceptedAt is when the user last accepted the risk, or empty.
	AcceptedAt string
	Limits     limitFields
	SaveSent   bool
	// RemoveWarning says what the removal deletes.
	RemoveWarning string
}

// levelOption is one mail level in the form.
type levelOption struct {
	Value    int
	Name     string
	Selected bool
}

// levelRequest is the level that a link asks for, as the page shows it.
type levelRequest struct {
	Value int
	Name  string
	// Allows says what the level lets the agent do.
	Allows string
	// AlreadySet is true when the account has this level, so there is nothing
	// to apply.
	AlreadySet bool
	// NeedsAcceptance is true when applying the level needs the box that
	// accepts the risk, so the form of the button shows it.
	NeedsAcceptance bool
	// SendsUnasked is true when the level lets the agent send and Confirm
	// sends is off.
	SendsUnasked bool
	// TrashUnasked is true when the level lets the agent move mail to Trash and
	// Confirm deletes is off.
	TrashUnasked bool
}

// limitFields are the limits as the form shows them, as text, so that a value
// that is not valid can be shown again.
type limitFields struct {
	SendsPerHour         string
	SendsPerDay          string
	RecipientsPerMessage string
	TrashPerDay          string
	UndoWindowAuto       string
	UndoWindowConfirmed  string
	NewRecipientCheck    bool
}

// handleAccountSettings shows the settings of an account of the tenant of the
// session. It changes nothing. A valid "?area=mail&level=N" shows the request
// with its button. The form keeps the level of the account.
func (s *Server) handleAccountSettings(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		s.fail(w, r, codeSettingsFailed, errNoSession)
		return
	}
	a, found := s.loadAccount(w, r, sess)
	if !found {
		return
	}
	page := settingsOf(a)
	if level, ok := requestedLevel(r.URL.Query()); ok {
		page.Requested = levelRequestOf(level, a)
	}
	s.render(w, r, http.StatusOK, pageSettings, page)
}

// handleSaveAccountSettings saves the posted settings with
// Service.UpdateSettings, and then redirects to the page. A refusal shows the
// form again with the posted values and a fixed text, and changes nothing. The
// box that accepts the risk is never ticked on the page that comes back.
func (s *Server) handleSaveAccountSettings(w http.ResponseWriter, r *http.Request) {
	svc, sess, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeSettingsFailed, errNoService)
		return
	}
	a, found := s.loadAccount(w, r, sess)
	if !found {
		return
	}
	page, update, msg := readSettings(r.PostForm, a)
	if msg != "" {
		page.Error = msg
		s.render(w, r, http.StatusUnprocessableEntity, pageSettings, page)
		return
	}
	s.finishSave(w, r, a, page, svc.UpdateSettings(ctx, a.ID, update))
}

// handleApplyRequestedLevel applies the level of a request: the button that the
// page shows for a link. It reads only the level and the box that accepts the
// risk from the form. Everything else keeps the value of the account. It goes
// through Service.UpdateSettings, so every rule of a save holds here too. A
// refusal shows the request again and changes nothing.
func (s *Server) handleApplyRequestedLevel(w http.ResponseWriter, r *http.Request) {
	svc, sess, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeSettingsFailed, errNoService)
		return
	}
	a, found := s.loadAccount(w, r, sess)
	if !found {
		return
	}
	page := settingsOf(a)
	level, ok := postedLevel(r.PostForm)
	if !ok {
		page.Error = msgBadLevel
		s.render(w, r, http.StatusUnprocessableEntity, pageSettings, page)
		return
	}
	page.Requested = levelRequestOf(level, a)
	accepted := r.PostForm.Get("accept_autonomy_risk") == "1"
	s.finishSave(w, r, a, page, svc.UpdateSettings(ctx, a.ID, levelUpdate(a, level, accepted)))
}

// finishSave answers a call of Service.UpdateSettings: a redirect to the page
// when it worked, or else the page that the caller prepared with a fixed text.
func (s *Server) finishSave(w http.ResponseWriter, r *http.Request, a *account.Account, page settingsPage, err error) {
	switch {
	case err == nil:
		http.Redirect(w, r, "/accounts/"+url.PathEscape(string(a.ID))+"/settings", http.StatusSeeOther)
	case errors.Is(err, service.ErrAutonomyNotAccepted):
		page.Error = msgAutonomyNeeded
		s.render(w, r, http.StatusBadRequest, pageSettings, page)
	case errors.Is(err, service.ErrInvalidLevel):
		page.Error = msgBadLevel
		s.render(w, r, http.StatusUnprocessableEntity, pageSettings, page)
	case errors.Is(err, service.ErrInvalidLimits):
		page.Error = msgBadLimits
		s.render(w, r, http.StatusUnprocessableEntity, pageSettings, page)
	case errors.Is(err, service.ErrAccountNotFound):
		s.message(w, r, http.StatusNotFound, titleAccountAbsent, msgNoAccount)
	default:
		s.fail(w, r, codeSettingsFailed, err)
	}
}

// handleRemoveAccount removes an account of the tenant of the session with
// Service.RemoveAccount, and then redirects to the accounts page. It needs the
// box "confirm_remove", because the removal also deletes the queued messages of
// the account.
func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	svc, sess, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeRemoveFailed, errNoService)
		return
	}
	a, found := s.loadAccount(w, r, sess)
	if !found {
		return
	}
	if r.PostForm.Get("confirm_remove") != "1" {
		page := settingsOf(a)
		page.Error = msgRemoveConfirm
		s.render(w, r, http.StatusBadRequest, pageSettings, page)
		return
	}
	err := svc.RemoveAccount(ctx, a.ID)
	switch {
	case err == nil:
		http.Redirect(w, r, pathAccounts, http.StatusSeeOther)
	case errors.Is(err, service.ErrAccountNotFound):
		s.message(w, r, http.StatusNotFound, titleAccountAbsent, msgNoAccount)
	default:
		s.fail(w, r, codeRemoveFailed, err)
	}
}

// loadAccount returns the account of the path in the tenant of the session.
// When there is none, it answers 404 with a fixed text, and found is false. An
// account of another tenant is not found either.
func (s *Server) loadAccount(w http.ResponseWriter, r *http.Request, sess session) (*account.Account, bool) {
	id := account.AccountID(r.PathValue("id"))
	a, err := s.d.Store.GetAccount(r.Context(), sess.tenant, id)
	if err == nil {
		return a, true
	}
	if isNotFound(err) {
		s.message(w, r, http.StatusNotFound, titleAccountAbsent, msgNoAccount)
		return nil, false
	}
	s.fail(w, r, codeSettingsFailed, err)
	return nil, false
}

// isNotFound reports whether an error of the store means that there is no
// such row.
func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidID)
}

// settingsOf returns the page with the stored settings of an account.
func settingsOf(a *account.Account) settingsPage {
	level := a.Level(account.AreaMail)
	page := settingsPage{
		ID:              string(a.ID),
		Address:         a.Address,
		Levels:          levelOptions(level),
		Current:         level.MailName(),
		ConfirmSends:    a.ConfirmSends,
		ConfirmDeletes:  a.ConfirmDeletes,
		AutonomyWarning: autonomyWarning,
		RemoveWarning:   msgRemoveWarning,
		Limits: limitFields{
			SendsPerHour:         strconv.Itoa(a.Limits.SendsPerHour),
			SendsPerDay:          strconv.Itoa(a.Limits.SendsPerDay),
			RecipientsPerMessage: strconv.Itoa(a.Limits.RecipientsPerMessage),
			TrashPerDay:          strconv.Itoa(a.Limits.TrashPerDay),
			UndoWindowAuto:       strconv.Itoa(int(a.Limits.UndoWindowAuto / time.Second)),
			UndoWindowConfirmed:  strconv.Itoa(int(a.Limits.UndoWindowConfirmed / time.Second)),
			NewRecipientCheck:    a.Limits.NewRecipientCheck,
		},
		SaveSent: a.Mail != nil && a.Mail.SaveSentCopy,
	}
	if a.AutonomyAcceptedAt != nil {
		page.AcceptedAt = formatTime(*a.AutonomyAcceptedAt)
	}
	return page
}

// levelOptions returns the six mail levels with one selected. A stored level
// outside 0 to 5 selects none.
func levelOptions(selected account.Level) []levelOption {
	options := make([]levelOption, 0, int(account.MailDelete)+1)
	for l := account.MailOff; l <= account.MailDelete; l++ {
		options = append(options, levelOption{Value: int(l), Name: l.MailName(), Selected: l == selected})
	}
	return options
}

// levelRequestOf returns the request for a level, as the page shows it for an
// account.
func levelRequestOf(level account.Level, a *account.Account) *levelRequest {
	return &levelRequest{
		Value:           int(level),
		Name:            level.MailName(),
		Allows:          levelAllows(level),
		AlreadySet:      level == a.Level(account.AreaMail),
		NeedsAcceptance: service.NeedsRiskAcceptance(a, levelUpdate(a, level, false)),
		SendsUnasked:    level >= account.MailSend && !a.ConfirmSends,
		TrashUnasked:    level >= account.MailDelete && !a.ConfirmDeletes,
	}
}

// levelUpdate is a save that changes only the mail level. Everything else keeps
// the value of the account.
func levelUpdate(a *account.Account, level account.Level, accepted bool) service.SettingsUpdate {
	return service.SettingsUpdate{
		MailLevel:          level,
		ConfirmSends:       a.ConfirmSends,
		ConfirmDeletes:     a.ConfirmDeletes,
		AcceptAutonomyRisk: accepted,
		Limits:             a.Limits,
		SaveSentCopy:       a.Mail != nil && a.Mail.SaveSentCopy,
	}
}

// levelAllows says what a mail level lets the agent do, for the sentence
// "Level N ...". The levels are those of the README.
func levelAllows(level account.Level) string {
	switch level {
	case account.MailRead:
		return "lets the agent read mail"
	case account.MailOrganize:
		return "lets the agent read and organize mail"
	case account.MailDraft:
		return "lets the agent read and organize mail, and write drafts"
	case account.MailSend:
		return "lets the agent send mail"
	case account.MailDelete:
		return "lets the agent send mail and move mail to Trash"
	default:
		return "stops the agent from using this mail account"
	}
}

// requestedLevel reads the level that a link asks for. The address must have
// exactly one "area", which is "mail", and exactly one "level", which is one
// digit from 0 to 5. Anything else is not a request.
func requestedLevel(q url.Values) (account.Level, bool) {
	areas, levels := q["area"], q["level"]
	if len(areas) != 1 || areas[0] != string(account.AreaMail) || len(levels) != 1 {
		return 0, false
	}
	return parseLevel(levels[0])
}

// postedLevel reads the mail level of a posted form, which must have exactly
// one value.
func postedLevel(form url.Values) (account.Level, bool) {
	levels := form["mail_level"]
	if len(levels) != 1 {
		return 0, false
	}
	return parseLevel(levels[0])
}

// parseLevel reads a mail level: exactly one digit from 0 to 5.
func parseLevel(text string) (account.Level, bool) {
	if len(text) != 1 || text[0] < '0' || text[0] > '5' {
		return 0, false
	}
	return account.Level(text[0] - '0'), true
}

// readSettings reads the posted form. It returns the page with the posted
// values (to show again after a refusal), the update for the service, and a
// fixed text when a value is not valid. A box that is not ticked is not in the
// form, so it is off. The box that accepts the risk is never ticked on the page.
func readSettings(form url.Values, a *account.Account) (settingsPage, service.SettingsUpdate, string) {
	page := settingsOf(a)
	page.ConfirmSends = form.Get("confirm_sends") == "1"
	page.ConfirmDeletes = form.Get("confirm_deletes") == "1"
	page.SaveSent = form.Get("save_sent_copy") == "1"
	page.Limits = limitFields{
		SendsPerHour:         form.Get("sends_per_hour"),
		SendsPerDay:          form.Get("sends_per_day"),
		RecipientsPerMessage: form.Get("recipients_per_message"),
		TrashPerDay:          form.Get("trash_per_day"),
		UndoWindowAuto:       form.Get("undo_window_auto"),
		UndoWindowConfirmed:  form.Get("undo_window_confirmed"),
		NewRecipientCheck:    form.Get("new_recipient_check") == "1",
	}
	update := service.SettingsUpdate{
		ConfirmSends:       page.ConfirmSends,
		ConfirmDeletes:     page.ConfirmDeletes,
		AcceptAutonomyRisk: form.Get("accept_autonomy_risk") == "1",
		SaveSentCopy:       page.SaveSent,
	}

	// A level that is not valid keeps the stored level selected on the page.
	level, ok := postedLevel(form)
	if !ok {
		return page, update, msgBadLevel
	}
	page.Levels = levelOptions(level)
	update.MailLevel = level

	limits, ok := readLimits(page.Limits)
	if !ok {
		return page, update, msgBadLimits
	}
	update.Limits = limits
	return page, update, ""
}

// readLimits reads the limits of the form. Each count must be a whole number
// from 0 to 1000000, and each undo window a whole number of seconds from 0 to
// 300. The bounds are checked before the seconds become a duration, so a large
// number cannot wrap around.
func readLimits(f limitFields) (account.Limits, bool) {
	var l account.Limits
	ok := true
	count := func(text string) int {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 || n > maxCount {
			ok = false
			return 0
		}
		return n
	}
	seconds := func(text string) time.Duration {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 || n > maxUndoSeconds {
			ok = false
			return 0
		}
		return time.Duration(n) * time.Second
	}
	l.SendsPerHour = count(f.SendsPerHour)
	l.SendsPerDay = count(f.SendsPerDay)
	l.RecipientsPerMessage = count(f.RecipientsPerMessage)
	l.TrashPerDay = count(f.TrashPerDay)
	l.UndoWindowAuto = seconds(f.UndoWindowAuto)
	l.UndoWindowConfirmed = seconds(f.UndoWindowConfirmed)
	l.NewRecipientCheck = f.NewRecipientCheck
	return l, ok
}

// formatTime writes a time for a page, in the time zone of this computer.
func formatTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04 MST")
}
