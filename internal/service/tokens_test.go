package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// fakeTokens is the TokenMaker of the tests. It records the calls.
type fakeTokens struct {
	mu      sync.Mutex
	created []string
	revoked []string
	err     error
}

func (f *fakeTokens) Create(_ context.Context, tenant account.TenantID, name string, _ policy.Scopes) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.created = append(f.created, string(tenant)+"/"+name)
	return "emcp_secret-token-text", nil
}

func (f *fakeTokens) Revoke(_ context.Context, tenant account.TenantID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.revoked = append(f.revoked, string(tenant)+"/"+id)
	return nil
}

// requireNoTokenData checks that an audit entry holds neither the token nor
// its name.
func requireNoTokenData(t *testing.T, e audit.Entry, values ...string) {
	t.Helper()
	fields := strings.Join(append([]string{string(e.Account), string(e.Area), e.Client, e.Tool, e.Detail}, e.Recipients...), "|")
	for _, v := range values {
		require.NotContains(t, fields, v)
	}
}

// A new token and a revoked token are in the log, as entries of the tenant with
// a fixed code. The entry never holds the token or its name.
func TestTokenChangesAreAudited(t *testing.T) {
	toks := &fakeTokens{}
	env := newWriteEnv(t, func(d *Deps) { d.Tokens = toks })

	token, err := env.svc.CreateToken(env.webCtx(), "my laptop", policy.Scopes{"email:read"})
	require.NoError(t, err)
	require.Equal(t, "emcp_secret-token-text", token)
	require.Equal(t, []string{string(env.f.tenant) + "/my laptop"}, toks.created, "the token is made for the tenant of the caller")
	last := env.lastEntry()
	requireEntry(t, last, "create_token", audit.OutcomeOK, "token_created")
	require.Empty(t, last.Account)
	require.Equal(t, "web", last.Client)
	requireNoTokenData(t, last, token, "my laptop", "laptop")

	require.NoError(t, env.svc.RevokeToken(env.webCtx(), "tok-1"))
	require.Equal(t, []string{string(env.f.tenant) + "/tok-1"}, toks.revoked)
	last = env.lastEntry()
	requireEntry(t, last, "revoke_token", audit.OutcomeOK, "token_revoked")
	require.Empty(t, last.Account)
	requireNoTokenData(t, last, "tok-1")
	require.Len(t, env.entries(), 2)
}

// A change that fails writes no entry. A call without a principal changes
// nothing.
func TestFailedTokenChangesAreNotAudited(t *testing.T) {
	toks := &fakeTokens{err: errors.New("the database failed")}
	env := newWriteEnv(t, func(d *Deps) { d.Tokens = toks })

	_, err := env.svc.CreateToken(env.webCtx(), "x", policy.Scopes{"email:read"})
	require.Error(t, err)
	require.Error(t, env.svc.RevokeToken(env.webCtx(), "tok-1"))
	require.Empty(t, env.entries())

	toks.err = nil
	_, err = env.svc.CreateToken(context.Background(), "x", policy.Scopes{"email:read"})
	require.ErrorIs(t, err, ErrNoPrincipal)
	require.ErrorIs(t, env.svc.RevokeToken(context.Background(), "tok-1"), ErrNoPrincipal)
	require.Empty(t, toks.created)
	require.Empty(t, toks.revoked)
}
