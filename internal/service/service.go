// Package service is the layer between the MCP tool handlers and the lower
// layers (the store, the vault, the policy engine and the mail backends).
//
// For each request it finds the caller's tenant, finds the account in that
// tenant, checks the permission, and only then calls the mail backend. A
// handler never reaches the store or a backend without passing through here.
//
// This file holds the Service type, its dependencies and its errors. The read
// use cases are in read.go. The write use cases are in write.go, and sending is
// in send.go. The backend cache is in backends.go.
//
// The package logs only fixed codes (for example "audit_write_failed"), with
// the name of the tool and the outcome at most. It never logs an error text, a
// subject, an address, a name, a body or a token, because the rules of this
// project forbid that and an error text can hold any of them.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/inflight"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// The errors that the use cases return. Callers check them with errors.Is.
var (
	// ErrAccountNotFound means that the tenant of the caller has no such
	// account. A caller gets the same error for an account that does not exist
	// and for an account of another tenant, so it cannot find out which ids
	// exist.
	ErrAccountNotFound = errors.New("service: account not found")

	// ErrNeedsReauth means that the account must be connected again (for
	// example because the mail server refused the stored password). The service
	// does not call the mail server for such an account.
	ErrNeedsReauth = errors.New("service: the account needs to be reconnected")

	// ErrStopping means that the program is stopping, so the service starts no
	// use case that sends mail or moves it to Trash. Nothing was done: an action
	// that waits for approval still waits. See Stop.
	ErrStopping = errors.New("service: the server is stopping, so nothing was sent or changed. " +
		"Approve or send again after it restarts")
)

// ReauthError is the error for an account that must be reconnected. It matches
// ErrNeedsReauth with errors.Is. A caller that wants the settings link reads
// Link with errors.As, and does not parse the text.
type ReauthError struct {
	// Link is the address of the settings page of the account, where the user
	// reconnects it. It is empty when the service could not build a link. It never
	// holds a token.
	Link string
}

// Error tells the user to reconnect the account, with the link when there is one.
func (e *ReauthError) Error() string {
	if e.Link == "" {
		return ErrNeedsReauth.Error() + ". Ask the user to reconnect it in the email-mcp settings"
	}
	return ErrNeedsReauth.Error() + ". Ask the user to reconnect it here: " + e.Link
}

// Unwrap makes errors.Is(err, ErrNeedsReauth) true.
func (e *ReauthError) Unwrap() error { return ErrNeedsReauth }

// PermissionError means that the policy refused an action. The message is for
// the agent: it says why and, when the user can fix it, where.
type PermissionError struct {
	// Decision is the answer of the policy engine.
	Decision policy.Decision
	// Link is the address of the settings page where the user can change the
	// level. It is empty when the service could not build a link, and when the
	// settings cannot help (see TokenLimited). It never holds a token.
	Link string
	// TokenLimited is true when the level of the account would allow the action
	// and the scopes of the access token lowered it. The settings of the account
	// cannot help then, so there is no link.
	TokenLimited bool
}

// Error returns the reason of the decision and a sentence about what can be
// done: the link, or the settings in general, or the access token.
func (e *PermissionError) Error() string {
	switch {
	case e.TokenLimited:
		return e.Decision.Reason + " The access token does not allow this action."
	case e.Link == "":
		return e.Decision.Reason + " The user can change the level in the email-mcp settings."
	default:
		return e.Decision.Reason + " The user can change the level here: " + e.Link
	}
}

// Links builds the web addresses that the service puts in its answers. The web
// package implements it.
//
// An address must never carry a secret. In particular it must not carry a token.
type Links interface {
	// Connect returns the address of the page where the user connects an
	// account.
	Connect(ctx context.Context) (string, error)
	// Settings returns the address of the settings page of an account, where the
	// user can set the level of an area.
	Settings(ctx context.Context, id account.AccountID, area account.Area, level account.Level) (string, error)
	// Approval returns the address of the page where the user approves or
	// declines a pending action.
	Approval(ctx context.Context, pendingID string) (string, error)
}

// Deps are the parts that a Service needs.
type Deps struct {
	Store    *store.Store
	Vault    *secrets.Vault
	Signer   *signing.Signer
	Backends BackendFactory
	Limiter  *policy.Limiter
	Confirm  *confirm.Manager
	Outbox   *outbox.Outbox
	Audit    *audit.Log
	Links    Links
	// Now returns the current time. A nil Now means time.Now.
	Now func() time.Time
	// Logger gets a line with a fixed code when the service cannot do a step
	// that does not change the answer, for example when an audit entry cannot be
	// written. A nil Logger logs nothing.
	Logger *slog.Logger
}

// Service runs the use cases. It is safe for use by many goroutines.
type Service struct {
	d Deps
	// acting counts the use cases that run and may send mail or move it to
	// Trash: Send, Trash and ApprovePending. Stop refuses new ones.
	acting inflight.Tracker
	// capabilitiesBudget is how long ListAccounts waits for the answers of the
	// backends. A test lowers it.
	capabilitiesBudget time.Duration
	// beforeCollect is a hook for tests. fillCapabilities calls it when it has
	// started all the questions and before it collects the answers. ready returns
	// the number of answers that wait in the channel. It is nil in production.
	beforeCollect func(ready func() int)
}

// New returns a Service.
//
// New panics when d.Store or d.Backends is nil. Both are mistakes in the code
// that sets the program up, and every use case needs them.
func New(d Deps) *Service {
	if d.Store == nil || d.Backends == nil {
		panic("service: New needs a Store and a BackendFactory")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d, capabilitiesBudget: capabilitiesBudget}
}

// Stop makes the service refuse the use cases that send mail or move it to
// Trash: Send, Trash and ApprovePending return ErrStopping, and change nothing.
// An action that waits for approval keeps waiting. The program calls Stop when
// it starts to stop, because the store and the mail backends close soon: an
// approval would send the message inside its web request, and nothing would
// wait for that send.
//
// Reads, CancelSend, SendStatus and RejectPending keep working, so the user can
// still undo a queued send while the program sends its last queued messages.
//
// Stop does not wait. It returns a channel that is closed when the use cases of
// that kind that run now have ended (a send without an undo window can take some
// minutes). A second call returns the same channel.
func (s *Service) Stop() <-chan struct{} {
	return s.acting.Stop()
}

// beginAction counts a use case that may send mail or move it to Trash. It
// returns ErrStopping after Stop. The caller calls end when the use case ends.
func (s *Service) beginAction() (end func(), err error) {
	if !s.acting.Begin() {
		return nil, ErrStopping
	}
	return s.acting.End, nil
}

// AccountView is what a caller may see of an account. It holds no secret and no
// server setting.
type AccountView struct {
	ID       account.AccountID
	Address  string
	Provider string
	// Areas maps each area to the name of its level, for example "Read".
	Areas map[account.Area]string
	// Capabilities is what the mail backend supports. It is empty when the
	// service did not ask the backend (the account needs to be reconnected, or
	// the caller may not read it), or when the backend could not be set up or did
	// not answer in time.
	Capabilities mail.Capabilities
	Status       account.Status
}

// access is the result of authorize: the caller, the account and the decision.
type access struct {
	Principal Principal
	Account   *account.Account
	Decision  policy.Decision
}

// authorize finds the account of the caller and checks that the caller may do
// act. It runs before every use case, and it runs before any call to a mail
// backend.
//
// It returns, in this order:
//   - ErrNoPrincipal when the request has no principal;
//   - ErrAccountNotFound when the tenant of the caller has no such account;
//   - a *PermissionError when the policy refuses the action;
//   - a *ReauthError (it matches ErrNeedsReauth) when the account must be
//     reconnected.
//
// A refusal by permission comes before the reconnect error, so a caller that
// may not do the action does not learn more about the account than needed.
func (s *Service) authorize(ctx context.Context, id account.AccountID, act policy.Action) (access, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return access{}, err
	}
	a, err := s.d.Store.GetAccount(ctx, p.Tenant, id)
	if errors.Is(err, store.ErrNotFound) {
		return access{}, ErrAccountNotFound
	}
	if err != nil {
		return access{}, fmt.Errorf("service: look up account: %w", err)
	}

	d := policy.Decide(a, act, p.Scopes)
	if !d.Allowed {
		return access{}, s.permissionError(ctx, a, d)
	}
	if a.Status == account.StatusNeedsReauth {
		return access{}, s.needsReauthError(ctx, a)
	}
	return access{Principal: p, Account: a, Decision: d}, nil
}

// permissionError builds the error for a refused decision.
//
// When the level of the account is already high enough and the scopes of the
// token lowered the level (d.Current is below the account level), only the token
// can be the reason. A link to the settings would be wrong advice then, so the
// error has none. A damaged account level (outside 0 to 5) counts as Off, as in
// the policy. An action that the policy does not know is refused whatever the
// level is, and the scopes are not the reason either.
//
// Otherwise the link may fail. The refusal still stands, with an empty link:
// the action must never go ahead because of a link problem.
func (s *Service) permissionError(ctx context.Context, a *account.Account, d policy.Decision) *PermissionError {
	level := a.Level(account.AreaMail)
	validLevel := level >= account.MailOff && level <= account.MailDelete
	if validLevel && level >= d.Required && d.Current < level {
		return &PermissionError{Decision: d, TokenLimited: true}
	}
	return &PermissionError{Decision: d, Link: s.settingsLink(ctx, a.ID, account.AreaMail, d.Required)}
}

// mailBackend authorizes act and returns the mail backend of the account.
func (s *Service) mailBackend(ctx context.Context, id account.AccountID, act policy.Action) (mail.Backend, error) {
	acc, err := s.authorize(ctx, id, act)
	if err != nil {
		return nil, err
	}
	// The factory errors already name this package. Wrapping them again would
	// give "service: ... service: ...".
	return s.d.Backends.Mail(ctx, acc.Account)
}

// settingsLink returns the settings address for an area and a level, or an
// empty string when there is no Links or it fails.
func (s *Service) settingsLink(ctx context.Context, id account.AccountID, area account.Area, level account.Level) string {
	if s.d.Links == nil {
		return ""
	}
	link, err := s.d.Links.Settings(ctx, id, area, level)
	if err != nil {
		return ""
	}
	return link
}

// needsReauthError returns the error for an account that must be reconnected.
// The message tells the user to reconnect and has the settings link when there
// is one.
func (s *Service) needsReauthError(ctx context.Context, a *account.Account) error {
	// The level is the one the account has now, so the link asks for no change.
	link := s.settingsLink(ctx, a.ID, account.AreaMail, a.Level(account.AreaMail))
	return &ReauthError{Link: link}
}
