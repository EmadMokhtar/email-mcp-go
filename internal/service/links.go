package service

// This file holds the links that the agent passes on to the user: the settings
// page of an account and the page that connects a new account. They change
// nothing. Only the user changes a level or adds an account, on a page that the
// agent cannot operate (spec section 7.3).

import (
	"context"
	"errors"
	"fmt"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// errNoLink means that the service could not build a link: it has no Links, or
// Links failed. The error of Links is not passed on, because it could hold a
// setting of the program.
var errNoLink = errors.New("service: the link could not be built")

// PermissionLink returns the address of the settings page where the user can set
// the level of an area of an account. It changes nothing, and it needs no level:
// the agent may always ask the user for more access, and the user decides on the
// page.
//
// The account must be in the tenant of the caller (ErrAccountNotFound
// otherwise). The area must be a known area and the level must be 0 to 5
// (ErrInvalidRequest otherwise). The address never holds a token.
func (s *Service) PermissionLink(ctx context.Context, id account.AccountID, area account.Area, level account.Level) (string, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return "", err
	}
	switch area {
	case account.AreaMail, account.AreaCalendar, account.AreaContacts:
	default:
		return "", fmt.Errorf("%w: unknown area", ErrInvalidRequest)
	}
	if level < account.MailOff || level > account.MailDelete {
		return "", fmt.Errorf("%w: the level must be 0 to 5", ErrInvalidRequest)
	}
	if _, err := s.d.Store.GetAccount(ctx, p.Tenant, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrAccountNotFound
		}
		return "", fmt.Errorf("service: look up account: %w", err)
	}
	if s.d.Links == nil {
		return "", errNoLink
	}
	link, err := s.d.Links.Settings(ctx, id, area, level)
	if err != nil || link == "" {
		return "", errNoLink
	}
	return link, nil
}

// ConnectLink returns the address of the page where the user connects a new
// account. It changes nothing. The caller must have a principal.
func (s *Service) ConnectLink(ctx context.Context) (string, error) {
	if _, err := PrincipalFrom(ctx); err != nil {
		return "", err
	}
	if s.d.Links == nil {
		return "", errNoLink
	}
	link, err := s.d.Links.Connect(ctx)
	if err != nil || link == "" {
		return "", errNoLink
	}
	return link, nil
}
