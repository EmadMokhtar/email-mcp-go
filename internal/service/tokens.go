package service

// This file holds the use cases that make and revoke the access tokens of the
// local HTTP server. The user, never the agent, calls them from the connections
// page: no MCP tool reaches them.

import (
	"context"
	"errors"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// The audit entries of the token use cases.
const (
	toolCreateToken    = "create_token"
	detailTokenCreated = "token_created"
	toolRevokeToken    = "revoke_token"
	detailTokenRevoked = "token_revoked"
)

// TokenMaker makes and revokes the access tokens of the local HTTP server.
// *tokens.Manager implements it. The tokens package uses this package, so this
// package names only the calls that it needs.
type TokenMaker interface {
	// Create makes a token for the tenant and returns it. This is the only time
	// the token is seen.
	Create(ctx context.Context, tenant account.TenantID, name string, scopes policy.Scopes) (string, error)
	// Revoke revokes the token of the tenant with the id.
	Revoke(ctx context.Context, tenant account.TenantID, id string) error
}

// errNoTokens means that the service has no TokenMaker or no audit log. A token
// must not change without its audit entry.
var errNoTokens = errors.New("service: the token maker and the audit log must be set up to change a token")

// CreateToken makes an access token for the tenant of the caller and returns
// it. The errors of the TokenMaker (a name or scopes that are not valid) come
// back as they are.
//
// The audit entry "token_created" is an entry of the tenant. It never holds the
// token or its name. A token that was not made writes no entry.
func (s *Service) CreateToken(ctx context.Context, name string, scopes policy.Scopes) (string, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return "", err
	}
	if s.d.Tokens == nil || s.d.Audit == nil {
		return "", errNoTokens
	}
	token, err := s.d.Tokens.Create(ctx, p.Tenant, name, scopes)
	if err != nil {
		return "", err
	}
	s.record(ctx, p, entry{tool: toolCreateToken, outcome: audit.OutcomeOK, detail: detailTokenCreated})
	return token, nil
}

// RevokeToken revokes an access token of the tenant of the caller. The errors
// of the TokenMaker (no such token in the tenant) come back as they are.
//
// The audit entry "token_revoked" is an entry of the tenant. It holds neither
// the id nor the name of the token. A revoke that failed writes no entry.
func (s *Service) RevokeToken(ctx context.Context, id string) error {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return err
	}
	if s.d.Tokens == nil || s.d.Audit == nil {
		return errNoTokens
	}
	if err := s.d.Tokens.Revoke(ctx, p.Tenant, id); err != nil {
		return err
	}
	s.record(ctx, p, entry{tool: toolRevokeToken, outcome: audit.OutcomeOK, detail: detailTokenRevoked})
	return nil
}
