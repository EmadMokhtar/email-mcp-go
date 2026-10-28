// Package service is the layer between the MCP tool handlers and the lower
// layers (the store, the vault, the policy engine and the mail backends).
//
// For each request it finds the caller's tenant, finds the account in that
// tenant, checks the permission, and only then calls the mail backend. A
// handler never reaches the store or a backend without passing through here.
//
// This file holds the Service type, its dependencies and its errors. The read
// use cases are in read.go. The backend cache is in backends.go.
//
// The package writes no log lines. A log line could carry a subject, an
// address, a name or a token, and the rules of this project forbid that.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
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
)

// PermissionError means that the policy refused an action. The message is for
// the agent: it says why and where the user can change the level.
type PermissionError struct {
	// Decision is the answer of the policy engine.
	Decision policy.Decision
	// Link is the address of the settings page where the user can change the
	// level. It is empty when the service could not build a link. It never
	// holds a token.
	Link string
}

// Error returns the reason of the decision and a sentence about the link.
func (e *PermissionError) Error() string {
	if e.Link == "" {
		return e.Decision.Reason + " The user can change the level in the email-mcp settings."
	}
	return e.Decision.Reason + " The user can change the level here: " + e.Link
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
}

// Service runs the use cases. It is safe for use by many goroutines.
type Service struct {
	d Deps
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
	return &Service{d: d}
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
	// service did not ask the backend: the account needs to be reconnected, the
	// caller may not read it, or the backend could not be set up.
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
//   - an error that wraps ErrNeedsReauth when the account must be reconnected.
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
		// If the link cannot be built, the refusal still stands. It only has no
		// link. The action must never go ahead because of a link problem.
		link := s.settingsLink(ctx, a.ID, account.AreaMail, d.Required)
		return access{}, &PermissionError{Decision: d, Link: link}
	}
	if a.Status == account.StatusNeedsReauth {
		return access{}, s.needsReauthError(ctx, a)
	}
	return access{Principal: p, Account: a, Decision: d}, nil
}

// mailBackend authorizes act and returns the mail backend of the account.
func (s *Service) mailBackend(ctx context.Context, id account.AccountID, act policy.Action) (mail.Backend, error) {
	acc, err := s.authorize(ctx, id, act)
	if err != nil {
		return nil, err
	}
	b, err := s.d.Backends.Mail(ctx, acc.Account)
	if err != nil {
		return nil, fmt.Errorf("service: open mail backend: %w", err)
	}
	return b, nil
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
	if link == "" {
		return fmt.Errorf("%w. Ask the user to reconnect it in the email-mcp settings", ErrNeedsReauth)
	}
	return fmt.Errorf("%w. Ask the user to reconnect it here: %s", ErrNeedsReauth, link)
}
