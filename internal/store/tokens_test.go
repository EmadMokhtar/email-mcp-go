package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// testTokenRow returns a valid token row of the tenant. fill is the byte that
// the hash is made of, so two rows with different fills have different hashes.
func testTokenRow(tenant account.TenantID, id string, fill byte) *APITokenRow {
	return &APITokenRow{
		ID:        id,
		Tenant:    tenant,
		Name:      "laptop",
		Hash:      bytes.Repeat([]byte{fill}, 32),
		Scopes:    []string{"email:read", "email:send"},
		CreatedAt: testNow,
	}
}

func TestAPITokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	first := testTokenRow(tenant, "tok1", 1)
	second := testTokenRow(tenant, "tok2", 2)
	second.Name = "desktop"
	second.Scopes = []string{"email:read"}
	second.CreatedAt = testNow.Add(time.Second)
	require.NoError(t, s.CreateAPIToken(ctx, first))
	require.NoError(t, s.CreateAPIToken(ctx, second))

	got, err := s.APITokenByHash(ctx, first.Hash)
	require.NoError(t, err)
	require.Equal(t, first, got)

	_, err = s.APITokenByHash(ctx, bytes.Repeat([]byte{9}, 32))
	require.ErrorIs(t, err, ErrNotFound)

	list, err := s.ListAPITokens(ctx, tenant)
	require.NoError(t, err)
	require.Len(t, list, 2)
	// The list is ordered by creation time and never holds the hash.
	for i, want := range []*APITokenRow{first, second} {
		expected := *want
		expected.Hash = nil
		require.Equal(t, &expected, list[i])
	}

	revokedAt := testNow.Add(time.Minute)
	require.NoError(t, s.RevokeAPIToken(ctx, tenant, "tok1", revokedAt))
	got, err = s.APITokenByHash(ctx, first.Hash)
	require.NoError(t, err)
	require.Equal(t, revokedAt, got.RevokedAt)

	// A second revoke succeeds and keeps the first time.
	require.NoError(t, s.RevokeAPIToken(ctx, tenant, "tok1", revokedAt.Add(time.Hour)))
	got, err = s.APITokenByHash(ctx, first.Hash)
	require.NoError(t, err)
	require.Equal(t, revokedAt, got.RevokedAt)
}

func TestAPITokenIsolatedByTenant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	local, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	ensureTestTenant(t, s, "tenant-two")

	require.NoError(t, s.CreateAPIToken(ctx, testTokenRow(local, "tok1", 1)))
	require.NoError(t, s.CreateAPIToken(ctx, testTokenRow("tenant-two", "tok2", 2)))

	theirs, err := s.ListAPITokens(ctx, "tenant-two")
	require.NoError(t, err)
	require.Len(t, theirs, 1)
	require.Equal(t, "tok2", theirs[0].ID)

	require.ErrorIs(t, s.RevokeAPIToken(ctx, "tenant-two", "tok1", testNow), ErrNotFound,
		"a tenant cannot revoke the token of another tenant")
	got, err := s.APITokenByHash(ctx, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	require.True(t, got.RevokedAt.IsZero())
	require.ErrorIs(t, s.RevokeAPIToken(ctx, local, "nope", testNow), ErrNotFound)
}

func TestAPITokenTableRules(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateAPIToken(ctx, testTokenRow(tenant, "tok1", 1)))

	bad := map[string]func(r *APITokenRow){
		"short hash":     func(r *APITokenRow) { r.Hash = bytes.Repeat([]byte{3}, 31) },
		"same hash":      func(r *APITokenRow) { r.Hash = bytes.Repeat([]byte{1}, 32) },
		"same id":        func(r *APITokenRow) { r.ID = "tok1" },
		"empty id":       func(r *APITokenRow) { r.ID = "" },
		"no scopes":      func(r *APITokenRow) { r.Scopes = nil },
		"empty name":     func(r *APITokenRow) { r.Name = "" },
		"long name":      func(r *APITokenRow) { r.Name = string(bytes.Repeat([]byte{'n'}, 129)) },
		"unknown tenant": func(r *APITokenRow) { r.Tenant = "nobody" },
	}
	for name, change := range bad {
		t.Run(name, func(t *testing.T) {
			row := testTokenRow(tenant, "tok-new", 3)
			change(row)
			require.Error(t, s.CreateAPIToken(ctx, row))
		})
	}
	// The table itself refuses a scope list that is not a JSON array with at
	// least one element, also when a write does not go through CreateAPIToken.
	for _, scopes := range []string{`[]`, `{}`, `"email:read"`, `not json`, ``} {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO api_tokens (tenant_id, id, name, hash, scopes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			string(tenant), "tok-raw", "raw", bytes.Repeat([]byte{4}, 32), scopes, testNow.UnixMilli())
		require.Error(t, err, "scopes %q", scopes)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = created_at - 1 WHERE id = 'tok1'`)
	require.Error(t, err, "a token is not revoked before it was made")

	list, err := s.ListAPITokens(ctx, tenant)
	require.NoError(t, err)
	require.Len(t, list, 1, "no refused row was stored")
}

func TestRevokeAPITokenNeverBeforeCreation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	row := testTokenRow(tenant, "tok1", 1)
	require.NoError(t, s.CreateAPIToken(ctx, row))

	// A clock that went back still revokes the token.
	require.NoError(t, s.RevokeAPIToken(ctx, tenant, "tok1", testNow.Add(-time.Hour)))
	got, err := s.APITokenByHash(ctx, row.Hash)
	require.NoError(t, err)
	require.Equal(t, testNow, got.RevokedAt)
}
