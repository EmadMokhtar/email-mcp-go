package service

import (
	"context"
	"errors"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// ErrNoPrincipal means that the request has no principal (no known caller).
// The service refuses such a request. The transport layer must put a Principal
// in the context with WithPrincipal before it calls the service.
var ErrNoPrincipal = errors.New("service: the request has no principal")

// Principal is the caller of a request: the tenant it belongs to, what its
// access token allows, and the name of the client program.
type Principal struct {
	// Tenant is the tenant whose accounts the caller may use.
	Tenant account.TenantID
	// Scopes are the permissions of the access token. A nil value means no
	// limit, which is what a local install uses. See policy.Scopes.
	Scopes policy.Scopes
	// Client is the name that the MCP client gave for itself. It is for the
	// audit log only. It is not trusted.
	Client string
}

// principalKey is the context key for the Principal. It is a private type, so
// no other package can set or replace the value by accident.
type principalKey struct{}

// WithPrincipal returns a context that carries p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal of the context. It returns ErrNoPrincipal
// when there is none, or when its tenant is empty: a principal without a tenant
// cannot be matched to any account, so it counts as no principal.
func PrincipalFrom(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	if !ok || p.Tenant == "" {
		return Principal{}, ErrNoPrincipal
	}
	return p, nil
}
