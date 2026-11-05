package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// update returns the settings of a new account with the changes applied.
func update(change ...func(*SettingsUpdate)) SettingsUpdate {
	u := SettingsUpdate{
		MailLevel:      account.MailSend,
		ConfirmSends:   true,
		ConfirmDeletes: true,
		Limits:         account.DefaultLimits(),
	}
	for _, fn := range change {
		fn(&u)
	}
	return u
}

// bothOff turns both confirmations off.
func bothOff(u *SettingsUpdate) {
	u.ConfirmSends = false
	u.ConfirmDeletes = false
}

// acceptRisk ticks the box that accepts the risk of full autonomy.
func acceptRisk(u *SettingsUpdate) { u.AcceptAutonomyRisk = true }

func TestUpdateSettingsSavesEverything(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	limits := account.Limits{
		SendsPerHour: 5, SendsPerDay: 50, RecipientsPerMessage: 10, TrashPerDay: 20,
		UndoWindowAuto: 90 * time.Second, UndoWindowConfirmed: 15 * time.Second, NewRecipientCheck: true,
	}

	err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(func(u *SettingsUpdate) {
		u.MailLevel = account.MailOrganize
		u.ConfirmDeletes = false
		u.Limits = limits
		u.SaveSentCopy = true
	}))
	require.NoError(t, err)

	got := env.f.reload(a)
	require.Equal(t, account.MailOrganize, got.Level(account.AreaMail))
	require.True(t, got.ConfirmSends)
	require.False(t, got.ConfirmDeletes)
	require.Equal(t, limits, got.Limits)
	require.True(t, got.Mail.SaveSentCopy)
	require.Nil(t, got.AutonomyAcceptedAt, "one confirmation is still on")
	// What the form does not hold stays as it was.
	require.Equal(t, a.Address, got.Address)
	require.Equal(t, a.Mail.IMAPHost, got.Mail.IMAPHost)
	require.Equal(t, a.Status, got.Status)

	// The cached backend is not closed: a send or a read may use it right now.
	// The backend cache sees the change of SaveSentCopy in its fingerprint, and
	// retires the old backend with a grace time (see BackendCache).
	require.Empty(t, env.factory.forgot(), "the cached backend is not dropped")
	entries := env.entries()
	require.Len(t, entries, 1)
	requireEntry(t, entries[0], "update_settings", audit.OutcomeOK, "settings_changed")
	require.Equal(t, a.ID, entries[0].Account)
	require.Equal(t, "web", entries[0].Client)
	require.Empty(t, entries[0].Recipients)
}

// Turning off both confirmations is full autonomy. It needs the user to accept
// the risk, and a refusal changes nothing.
func TestUpdateSettingsRefusesAutonomyWithoutAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(bothOff, func(u *SettingsUpdate) {
		u.MailLevel = account.MailDelete
	}))
	require.ErrorIs(t, err, ErrAutonomyNotAccepted)

	require.Equal(t, a, env.f.reload(a), "the account is unchanged")
	require.Empty(t, env.factory.forgot())
	require.Empty(t, env.entries())
}

func TestUpdateSettingsRecordsTheAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(bothOff, acceptRisk)))

	got := env.f.reload(a)
	require.False(t, got.ConfirmSends)
	require.False(t, got.ConfirmDeletes)
	require.NotNil(t, got.AutonomyAcceptedAt)
	require.Equal(t, testStart, *got.AutonomyAcceptedAt)
	entries := env.entries()
	require.Len(t, entries, 2)
	tools := []string{entries[0].Detail, entries[1].Detail}
	require.ElementsMatch(t, []string{"settings_changed", "autonomy_accepted"}, tools)
	for _, e := range entries {
		require.Equal(t, "update_settings", e.Tool)
		require.Equal(t, audit.OutcomeOK, e.Outcome)
		require.Equal(t, a.ID, e.Account)
	}
}

// Each save that ends with both confirmations off needs the acceptance again,
// also when the user accepted before. Each accepted save records the time anew.
func TestUpdateSettingsNeedsTheAcceptanceOnEverySave(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(bothOff, acceptRisk)))
	before := env.f.reload(a)

	env.clock.Advance(time.Hour)
	err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(bothOff, func(u *SettingsUpdate) {
		u.Limits.SendsPerHour = 3
	}))
	require.ErrorIs(t, err, ErrAutonomyNotAccepted)
	require.Equal(t, before, env.f.reload(a), "a save without the tick changes nothing")

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(bothOff, acceptRisk, func(u *SettingsUpdate) {
		u.Limits.SendsPerHour = 3
	})))
	got := env.f.reload(a)
	require.Equal(t, 3, got.Limits.SendsPerHour)
	require.Equal(t, testStart.Add(time.Hour), *got.AutonomyAcceptedAt, "the new acceptance has the new time")
	accepted := 0
	for _, e := range env.entries() {
		if e.Detail == "autonomy_accepted" {
			accepted++
		}
	}
	require.Equal(t, 2, accepted, "each accepted save is in the log")

	// With one confirmation on, no acceptance is needed, and the time of the
	// last acceptance stays.
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(func(u *SettingsUpdate) {
		u.ConfirmDeletes = false
	})))
	require.Equal(t, testStart.Add(time.Hour), *env.f.reload(a).AutonomyAcceptedAt)
}

// sendsOff turns off only the send confirmation.
func sendsOff(u *SettingsUpdate) { u.ConfirmSends = false }

// Turning off send confirmation alone is what allows unattended mail, so it
// needs the same acceptance as full autonomy. A refusal changes nothing.
func TestUpdateSettingsRefusesSendsOffWithoutAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff))
	require.ErrorIs(t, err, ErrAutonomyNotAccepted)

	require.Equal(t, a, env.f.reload(a), "the account is unchanged")
	require.Empty(t, env.entries())
}

func TestUpdateSettingsRecordsTheAcceptanceOfSendsOff(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk)))

	got := env.f.reload(a)
	require.False(t, got.ConfirmSends)
	require.True(t, got.ConfirmDeletes)
	require.NotNil(t, got.AutonomyAcceptedAt)
	require.Equal(t, testStart, *got.AutonomyAcceptedAt)
	entries := env.entries()
	require.Len(t, entries, 2)
	require.ElementsMatch(t, []string{"settings_changed", "autonomy_accepted"},
		[]string{entries[0].Detail, entries[1].Detail})
}

// When send confirmation is already off and deletes are still confirmed, the
// risk does not change, so another change needs no new acceptance. The time of
// the last acceptance stays, and no new acceptance goes in the log.
func TestUpdateSettingsDoesNotAskAgainWhenSendsStayOff(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk)))

	env.clock.Advance(time.Hour)
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, func(u *SettingsUpdate) {
		u.Limits.SendsPerHour = 3
	})))

	got := env.f.reload(a)
	require.Equal(t, 3, got.Limits.SendsPerHour)
	require.Equal(t, testStart, *got.AutonomyAcceptedAt)
	accepted := 0
	for _, e := range env.entries() {
		if e.Detail == "autonomy_accepted" {
			accepted++
		}
	}
	require.Equal(t, 1, accepted)
}

// An old acceptance does not carry over. After the user turns send
// confirmation on again, the time stays as a record, and the next change to
// off needs the box again.
func TestUpdateSettingsAsksAgainAfterSendsWereOnAgain(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk)))

	env.clock.Advance(time.Hour)
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update()))
	got := env.f.reload(a)
	require.True(t, got.ConfirmSends)
	require.Equal(t, testStart, *got.AutonomyAcceptedAt, "the record of the last acceptance stays")

	require.ErrorIs(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff)), ErrAutonomyNotAccepted)
	require.True(t, env.f.reload(a).ConfirmSends)

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk)))
	require.Equal(t, testStart.Add(time.Hour), *env.f.reload(a).AutonomyAcceptedAt)
}

// Turning off delete confirmation alone does not let mail leave. It needs no
// acceptance, and it is not recorded as one.
func TestUpdateSettingsDeletesOffAloneNeedsNoAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(func(u *SettingsUpdate) {
		u.ConfirmDeletes = false
	})))

	got := env.f.reload(a)
	require.False(t, got.ConfirmDeletes)
	require.Nil(t, got.AutonomyAcceptedAt)
}

// A box that is ticked when no acceptance is needed records nothing.
func TestUpdateSettingsIgnoresAnAcceptanceThatIsNotNeeded(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(acceptRisk)))

	require.Nil(t, env.f.reload(a).AutonomyAcceptedAt)
	entries := env.entries()
	require.Len(t, entries, 1)
	require.Equal(t, "settings_changed", entries[0].Detail)
}

// levelTo sets the mail level of the update.
func levelTo(level account.Level) func(*SettingsUpdate) {
	return func(u *SettingsUpdate) { u.MailLevel = level }
}

// acceptedCount returns how many acceptances the audit log holds.
func (e *writeEnv) acceptedCount() int {
	accepted := 0
	for _, entry := range e.entries() {
		if entry.Detail == "autonomy_accepted" {
			accepted++
		}
	}
	return accepted
}

// Raising the level to Send with Confirm sends off starts unattended sends,
// just as turning the switch off at Send level does. It needs the acceptance,
// and a refusal changes nothing.
func TestUpdateSettingsRaisingToSendWithSendsOffNeedsAcceptance(t *testing.T) {
	for _, level := range []account.Level{account.MailSend, account.MailDelete} {
		t.Run(level.MailName(), func(t *testing.T) {
			env := newWriteEnv(t)
			a := env.account()
			require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID,
				update(sendsOff, acceptRisk, levelTo(account.MailDraft))))
			before := env.f.reload(a)
			entries := len(env.entries())

			env.clock.Advance(time.Hour)
			err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, levelTo(level)))
			require.ErrorIs(t, err, ErrAutonomyNotAccepted)
			require.Equal(t, before, env.f.reload(a), "a refusal changes nothing")
			require.Len(t, env.entries(), entries, "a refusal writes no entry")

			require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk, levelTo(level))))
			got := env.f.reload(a)
			require.Equal(t, level, got.Level(account.AreaMail))
			require.Equal(t, testStart.Add(time.Hour), *got.AutonomyAcceptedAt)
			require.Equal(t, 2, env.acceptedCount(), "each acceptance is in the log")
		})
	}
}

// Below Send level, the agent cannot send, so a level change with Confirm sends
// off asks nothing new.
func TestUpdateSettingsRaisingBelowSendNeedsNoAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID,
		update(sendsOff, acceptRisk, levelTo(account.MailRead))))

	env.clock.Advance(time.Hour)
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, levelTo(account.MailDraft))))

	got := env.f.reload(a)
	require.Equal(t, account.MailDraft, got.Level(account.AreaMail))
	require.Equal(t, testStart, *got.AutonomyAcceptedAt)
	require.Equal(t, 1, env.acceptedCount())
}

// Turning off Confirm sends below Send level still needs the acceptance, so
// the switch is never off without one.
func TestUpdateSettingsSendsOffBelowSendLevelNeedsAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(levelTo(account.MailDraft))))
	before := env.f.reload(a)

	err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, levelTo(account.MailDraft)))
	require.ErrorIs(t, err, ErrAutonomyNotAccepted)
	require.Equal(t, before, env.f.reload(a))

	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk, levelTo(account.MailDraft))))
	require.Equal(t, testStart, *env.f.reload(a).AutonomyAcceptedAt)
}

// An account that already sends unattended and stays that way gets no new
// prompt: not for a limit, and not for a move between Send and Delete.
func TestUpdateSettingsStayingUnattendedNeedsNoAcceptance(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk)))

	env.clock.Advance(time.Hour)
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, func(u *SettingsUpdate) {
		u.Limits.SendsPerHour = 3
	})))
	require.Equal(t, 3, env.f.reload(a).Limits.SendsPerHour)
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, levelTo(account.MailDelete))))
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, levelTo(account.MailSend))))

	require.Equal(t, testStart, *env.f.reload(a).AutonomyAcceptedAt)
	require.Equal(t, 1, env.acceptedCount())
}

// Leaving the unattended state ends the acceptance. Going back in needs a new
// one, although the switch stayed off.
func TestUpdateSettingsAsksAgainAfterTheLevelWasBelowSend(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, acceptRisk)))
	require.NoError(t, env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff, levelTo(account.MailDraft))))
	require.Equal(t, 1, env.acceptedCount(), "lowering the level needs no acceptance")

	err := env.svc.UpdateSettings(env.webCtx(), a.ID, update(sendsOff))
	require.ErrorIs(t, err, ErrAutonomyNotAccepted)
	require.Equal(t, account.MailDraft, env.f.reload(a).Level(account.AreaMail))
}

func TestNeedsRiskAcceptance(t *testing.T) {
	const (
		on  = true
		off = false
	)
	cases := []struct {
		name                 string
		level, newLevel      account.Level
		sends, deletes       bool
		newSends, newDeletes bool
		want                 bool
	}{
		{"nothing changes", account.MailSend, account.MailSend, on, on, on, on, false},
		{"sends off at Send level", account.MailSend, account.MailSend, on, on, off, on, true},
		{"sends off below Send level", account.MailDraft, account.MailDraft, on, on, off, on, true},
		{"sends off and the level up", account.MailDraft, account.MailSend, on, on, off, on, true},
		{"level up to Send with sends off", account.MailDraft, account.MailSend, off, on, off, on, true},
		{"level up to Delete with sends off", account.MailRead, account.MailDelete, off, on, off, on, true},
		{"level up with sends on again", account.MailDraft, account.MailSend, off, on, on, on, false},
		{"level up below Send with sends off", account.MailRead, account.MailDraft, off, on, off, on, false},
		{"level up with sends on", account.MailDraft, account.MailDelete, on, on, on, on, false},
		{"stays at Send with sends off", account.MailSend, account.MailSend, off, on, off, on, false},
		{"Send to Delete with sends off", account.MailSend, account.MailDelete, off, on, off, on, false},
		{"Delete to Send with sends off", account.MailDelete, account.MailSend, off, on, off, on, false},
		{"level down from Send with sends off", account.MailSend, account.MailDraft, off, on, off, on, false},
		{"sends on again", account.MailSend, account.MailSend, off, on, on, on, false},
		{"deletes off alone", account.MailSend, account.MailSend, on, on, on, off, false},
		{"both off", account.MailSend, account.MailSend, on, on, off, off, true},
		{"stays with both off", account.MailSend, account.MailSend, off, off, off, off, true},
		{"both off, level down", account.MailDelete, account.MailRead, off, off, off, off, true},
		{"deletes off with sends already off", account.MailSend, account.MailSend, off, on, off, off, true},
		{"a damaged level counts as Off", account.Level(9), account.MailSend, off, on, off, on, true},
		{"a level below 0 counts as Off", account.Level(-1), account.MailSend, off, on, off, on, true},
	}
	for _, c := range cases {
		a := &account.Account{
			Levels:         map[account.Area]account.Level{account.AreaMail: c.level},
			ConfirmSends:   c.sends,
			ConfirmDeletes: c.deletes,
		}
		u := SettingsUpdate{MailLevel: c.newLevel, ConfirmSends: c.newSends, ConfirmDeletes: c.newDeletes}
		require.Equal(t, c.want, NeedsRiskAcceptance(a, u), c.name)
	}
}

func TestUpdateSettingsRefusesBadValues(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	cases := map[string]struct {
		u    SettingsUpdate
		want error
	}{
		"level above 5":        {update(func(u *SettingsUpdate) { u.MailLevel = 6 }), ErrInvalidLevel},
		"negative level":       {update(func(u *SettingsUpdate) { u.MailLevel = -1 }), ErrInvalidLevel},
		"negative limit":       {update(func(u *SettingsUpdate) { u.Limits.SendsPerDay = -1 }), ErrInvalidLimits},
		"undo window too long": {update(func(u *SettingsUpdate) { u.Limits.UndoWindowAuto = 301 * time.Second }), ErrInvalidLimits},
	}
	for name, c := range cases {
		require.ErrorIs(t, env.svc.UpdateSettings(env.webCtx(), a.ID, c.u), c.want, name)
	}
	require.Equal(t, a, env.f.reload(a))
	require.Empty(t, env.entries())
}

func TestUpdateSettingsIsScopedToTheTenant(t *testing.T) {
	env := newWriteEnv(t)
	env.f.addTenant("other")
	theirs := env.f.addAccount("other", "them@example.com", offlineSettings(), "pw")

	err := env.svc.UpdateSettings(env.webCtx(), theirs.ID, update(func(u *SettingsUpdate) { u.MailLevel = account.MailDelete }))
	require.ErrorIs(t, err, ErrAccountNotFound)
	require.Equal(t, theirs, env.f.reload(theirs))

	require.ErrorIs(t, env.svc.UpdateSettings(context.Background(), theirs.ID, update()), ErrNoPrincipal)
}

func TestRemoveAccount(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account(autonomy)
	keep := env.f.addAccount(env.f.tenant, "keep@example.com", offlineSettings(), "pw")
	c := compose("bob@example.com")
	res, _, err := env.svc.Send(env.ctx(), a.ID, &c, nil, ConfirmInBand, nil)
	require.NoError(t, err)
	require.Equal(t, SendQueued, res.Status, "the message waits for its undo window")
	require.NotEmpty(t, env.entries())

	require.NoError(t, env.svc.RemoveAccount(env.webCtx(), a.ID))

	ctx := context.Background()
	_, err = env.f.store.GetAccount(ctx, env.f.tenant, a.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, _, err = env.f.store.GetAccountSecret(ctx, env.f.tenant, a.ID, passwordSecret)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = env.outbox.Get(ctx, env.f.tenant, res.SendID)
	require.ErrorIs(t, err, outbox.ErrNotFound, "the queued message is deleted with the account")
	_, err = env.f.store.GetAccount(ctx, env.f.tenant, keep.ID)
	require.NoError(t, err, "the other account stays")
	require.Equal(t, []account.AccountID{a.ID}, env.factory.forgot())

	// The entries of the account went with it. The removal is an entry of the
	// tenant, with a code and no address.
	entries := env.entries()
	require.Len(t, entries, 1, "only the removal is left")
	last := entries[0]
	requireEntry(t, last, "remove_account", audit.OutcomeOK, "account_removed")
	require.Empty(t, last.Account)
	require.Empty(t, last.Recipients)
	require.Equal(t, "web", last.Client)
	require.Empty(t, env.sender.sent(), "the queued message was never sent")
}

func TestRemoveAccountIsScopedToTheTenant(t *testing.T) {
	env := newWriteEnv(t)
	env.f.addTenant("other")
	theirs := env.f.addAccount("other", "them@example.com", offlineSettings(), "pw")

	require.ErrorIs(t, env.svc.RemoveAccount(env.webCtx(), theirs.ID), ErrAccountNotFound)
	require.Equal(t, theirs, env.f.reload(theirs))
	require.Empty(t, env.factory.forgot())
	require.Empty(t, env.entries())
	require.ErrorIs(t, env.svc.RemoveAccount(context.Background(), theirs.ID), ErrNoPrincipal)
}

// When the store cannot delete the account, nothing is deleted, the cached
// backend stays and nothing is in the log.
func TestRemoveAccountFailureLeavesEverything(t *testing.T) {
	env := newWriteEnv(t)
	a := env.account()
	_, err := env.f.rawDB().ExecContext(context.Background(), `CREATE TRIGGER refuse_account_delete
		BEFORE DELETE ON accounts BEGIN SELECT RAISE(ABORT, 'refused by the test'); END`)
	require.NoError(t, err)

	require.Error(t, env.svc.RemoveAccount(env.webCtx(), a.ID))

	require.Equal(t, a, env.f.reload(a))
	_, _, err = env.f.store.GetAccountSecret(context.Background(), env.f.tenant, a.ID, passwordSecret)
	require.NoError(t, err, "the password stays with its account")
	require.Empty(t, env.factory.forgot())
	require.Empty(t, env.entries())
}

// The activity page offers a filter of the tools that the log can hold.
func TestAuditToolsListsEveryRecordedTool(t *testing.T) {
	tools := AuditTools()
	for _, want := range []string{
		"set_flags", "create_folder", "create_tag", "tag_messages", "move_messages", "create_draft",
		"discard_draft", "send_message", "cancel_send", "trash_messages", "approve_pending",
		"reject_pending", "connect_account", "update_settings", "remove_account", "create_token", "revoke_token",
	} {
		require.Contains(t, tools, want)
	}
	require.IsIncreasing(t, tools, "sorted, each once")
	tools[0] = "changed"
	require.NotEqual(t, "changed", AuditTools()[0], "the caller gets its own copy")
}
