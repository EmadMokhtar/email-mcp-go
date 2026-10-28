// Package policy decides, on the server, what an agent may do with an account.
// It also limits how often the agent may send and delete mail.
//
// The agent never makes these decisions and cannot change them. The agent only
// receives the result: an answer, and a sentence that says why when the answer
// is no.
package policy

import (
	"fmt"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// Action is something an agent asks to do with the mail of an account.
type Action string

// The mail actions. Each one needs a mail level (see RequiredMailLevel).
const (
	// ActRead reads and searches messages and folders.
	ActRead Action = "read"
	// ActOrganize changes flags and tags, and moves messages to a folder that is
	// not Trash.
	ActOrganize Action = "organize"
	// ActDraft saves a draft.
	ActDraft Action = "draft"
	// ActSend sends a message.
	ActSend Action = "send"
	// ActTrash moves messages to the Trash folder.
	ActTrash Action = "trash"
)

// actionInfo is what policy knows about one action.
type actionInfo struct {
	level account.Level // the mail level that the action needs
	verb  string        // the start of the refusal sentence
}

// actions lists every known action. An action that is not here is refused.
var actions = map[Action]actionInfo{
	ActRead:     {account.MailRead, "Reading"},
	ActOrganize: {account.MailOrganize, "Organizing"},
	ActDraft:    {account.MailDraft, "Drafting"},
	ActSend:     {account.MailSend, "Sending"},
	ActTrash:    {account.MailDelete, "Moving mail to Trash"},
}

// RequiredMailLevel returns the mail level that an action needs: 1 for read, 2
// for organize, 3 for draft, 4 for send and 5 for trash.
//
// An action that policy does not know needs the highest level. Decide refuses it
// in every case.
func RequiredMailLevel(act Action) account.Level {
	if info, ok := actions[act]; ok {
		return info.level
	}
	return account.MailDelete
}

// Scopes are the permissions of an access token. Each scope is a string like
// "email:send". They limit what a token can do, on top of the account level.
//
// A nil Scopes means that there is no limit. A local install uses nil, because
// it has no tokens. A Scopes that is not nil but empty allows nothing. A scope
// that policy does not know has no effect.
type Scopes []string

// scopeLevels maps each mail scope to the mail level that it allows.
var scopeLevels = map[string]account.Level{
	"email:read":     account.MailRead,
	"email:organize": account.MailOrganize,
	"email:draft":    account.MailDraft,
	"email:send":     account.MailSend,
	"email:delete":   account.MailDelete,
}

// maxScopeLevel returns the highest mail level that the scopes allow. A nil
// Scopes allows every level. Scopes that are empty, or that are all unknown,
// allow level 0.
func maxScopeLevel(scopes Scopes) account.Level {
	if scopes == nil {
		return account.MailDelete
	}
	highest := account.MailOff
	for _, scope := range scopes {
		highest = max(highest, scopeLevels[scope])
	}
	return highest
}

// Decision is the answer to "may the agent do this?".
type Decision struct {
	// Allowed says whether the agent may do the action.
	Allowed bool
	// NeedsConfirmation says that the user must confirm the action first. It is
	// true only when the action is allowed.
	NeedsConfirmation bool
	// Required is the mail level that the action needs.
	Required account.Level
	// Current is the mail level that applies now: the account level, lowered by
	// the token scopes.
	Current account.Level
	// Reason is a sentence for the agent that says why the action is refused. It
	// is empty when the action is allowed.
	Reason string
}

// Decide says whether an agent may do act on the mail of account a. a must not
// be nil.
//
// The level that applies is the lower of the account's mail level and the
// highest level that the token scopes allow. The action is allowed when that
// level is at least the level that the action needs.
//
// For an allowed send, NeedsConfirmation is the account's ConfirmSends. For an
// allowed move to Trash, it is the account's ConfirmDeletes. No other action
// needs confirmation here.
func Decide(a *account.Account, act Action, scopes Scopes) Decision {
	current := min(a.Level(account.AreaMail), maxScopeLevel(scopes))
	required := RequiredMailLevel(act)
	info, known := actions[act]

	if !known {
		return Decision{
			Required: required,
			Current:  current,
			Reason:   fmt.Sprintf("Account %q: this action is not known.", accountLabel(a)),
		}
	}
	if current < required {
		return Decision{
			Required: required,
			Current:  current,
			Reason: fmt.Sprintf("Account %q: mail is at level %d (%s). %s needs level %d (%s).",
				accountLabel(a), current, current.MailName(), info.verb, required, required.MailName()),
		}
	}

	d := Decision{Allowed: true, Required: required, Current: current}
	switch act {
	case ActSend:
		d.NeedsConfirmation = a.ConfirmSends
	case ActTrash:
		d.NeedsConfirmation = a.ConfirmDeletes
	}
	return d
}

// accountLabel returns the name that the agent sees for an account: the display
// name, or the address when there is no display name.
//
// Decide prints the label with %q. The label is text from the account settings.
// %q escapes quotes and line breaks, so the label cannot end the quoted name
// early or add a line to the sentence.
func accountLabel(a *account.Account) string {
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Address
}

// IsTrashTarget reports whether a move to resolvedFolder is a move to Trash. The
// service uses it to turn a "move messages" request into ActTrash.
//
// Both names are resolved folder names, not aliases. They match only when they
// are exactly equal. An empty trashFolder (the account has no Trash folder)
// matches nothing.
func IsTrashTarget(resolvedFolder, trashFolder string) bool {
	return trashFolder != "" && resolvedFolder == trashFolder
}
