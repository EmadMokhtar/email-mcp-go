package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// webHash returns a 32-byte hash made of one byte, so two fills give two
// different hashes.
func webHash(fill byte) []byte { return bytes.Repeat([]byte{fill}, 32) }

func TestWebSetupTokenBecomesSession(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(1), testNow, testNow.Add(10*time.Minute)))

	sessionExpiry := testNow.Add(30 * 24 * time.Hour)
	got, err := s.UseWebSetupToken(ctx, webHash(1), webHash(2), testNow.Add(time.Minute), sessionExpiry)
	require.NoError(t, err)
	require.Equal(t, tenant, got)

	session, err := s.WebSessionByHash(ctx, webHash(2))
	require.NoError(t, err)
	require.Equal(t, &WebSessionRow{
		Hash:      webHash(2),
		Tenant:    tenant,
		CreatedAt: testNow.Add(time.Minute),
		ExpiresAt: sessionExpiry,
	}, session)

	// The token works once. The second use finds nothing and makes no session.
	_, err = s.UseWebSetupToken(ctx, webHash(1), webHash(3), testNow.Add(time.Minute), sessionExpiry)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.WebSessionByHash(ctx, webHash(3))
	require.ErrorIs(t, err, ErrNotFound)

	// An unknown token is refused the same way.
	_, err = s.UseWebSetupToken(ctx, webHash(9), webHash(4), testNow, sessionExpiry)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.DeleteWebSession(ctx, webHash(2)))
	_, err = s.WebSessionByHash(ctx, webHash(2))
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, s.DeleteWebSession(ctx, webHash(2)), "deleting a missing session is not an error")
}

func TestWebSetupTokenExpires(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	expires := testNow.Add(10 * time.Minute)
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(1), testNow, expires))

	// At the expiry time the token is refused, and the use deletes it.
	_, err = s.UseWebSetupToken(ctx, webHash(1), webHash(2), expires, expires.Add(time.Hour))
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.WebSessionByHash(ctx, webHash(2))
	require.ErrorIs(t, err, ErrNotFound)
	var tokens int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM web_setup_tokens`).Scan(&tokens))
	require.Zero(t, tokens, "an expired token is deleted when it is used")

	// One millisecond before the expiry time it still works.
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(3), testNow, expires))
	_, err = s.UseWebSetupToken(ctx, webHash(3), webHash(4), expires.Add(-time.Millisecond), expires.Add(time.Hour))
	require.NoError(t, err)
}

func TestWebRowsCleanedUp(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)

	// An old token and an old session, both expired before the next writes.
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(1), testNow, testNow.Add(10*time.Minute)))
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(2), testNow, testNow.Add(10*time.Minute)))
	_, err = s.UseWebSetupToken(ctx, webHash(2), webHash(3), testNow, testNow.Add(time.Hour))
	require.NoError(t, err)

	later := testNow.Add(2 * time.Hour)
	// A new setup token deletes the expired setup tokens.
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(4), later, later.Add(10*time.Minute)))
	var tokens int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM web_setup_tokens`).Scan(&tokens))
	require.Equal(t, 1, tokens, "only the new setup token is left")

	// A new session deletes the expired sessions.
	_, err = s.UseWebSetupToken(ctx, webHash(4), webHash(5), later, later.Add(time.Hour))
	require.NoError(t, err)
	_, err = s.WebSessionByHash(ctx, webHash(3))
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.WebSessionByHash(ctx, webHash(5))
	require.NoError(t, err)
}

func TestWebTableRules(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	expires := testNow.Add(10 * time.Minute)
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(1), testNow, expires))

	require.Error(t, s.CreateWebSetupToken(ctx, tenant, webHash(1), testNow, expires), "the hash is unique")
	require.Error(t, s.CreateWebSetupToken(ctx, tenant, webHash(2)[:31], testNow, expires), "the hash is 32 bytes")
	require.Error(t, s.CreateWebSetupToken(ctx, tenant, webHash(2), testNow, testNow), "it expires after it was made")
	require.Error(t, s.CreateWebSetupToken(ctx, "nobody", webHash(2), testNow, expires), "the tenant must exist")
	require.ErrorIs(t, s.CreateWebSetupToken(ctx, "", webHash(2), testNow, expires), ErrInvalidID)

	_, err = s.UseWebSetupToken(ctx, webHash(1), webHash(2)[:31], testNow, expires)
	require.Error(t, err, "a session hash is 32 bytes")
	_, err = s.UseWebSetupToken(ctx, webHash(1), webHash(2), testNow, testNow)
	require.Error(t, err, "a session expires after it was made")
	// The failed uses did not use the token up.
	_, err = s.UseWebSetupToken(ctx, webHash(1), webHash(2), testNow, expires)
	require.NoError(t, err)

	// The tables refuse rows that do not go through the methods, too.
	inserts := []string{
		`INSERT INTO web_setup_tokens (hash, tenant_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		`INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
	}
	for name, hash := range map[string]any{"short": webHash(5)[:31], "long": bytes.Repeat([]byte{5}, 33), "text": "abc"} {
		for _, insert := range inserts {
			_, err := s.db.ExecContext(ctx, insert, hash, string(tenant), testNow.UnixMilli(), expires.UnixMilli())
			require.Error(t, err, "%s hash: %s", name, insert)
		}
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		webHash(2), string(tenant), testNow.UnixMilli(), expires.UnixMilli())
	require.Error(t, err, "a session hash is unique")
}

func TestWebRowsDeletedWithTenant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenants (id, created_at) VALUES ('other', 0)`)
	require.NoError(t, err)
	require.NoError(t, s.CreateWebSetupToken(ctx, "other", webHash(1), testNow, testNow.Add(time.Minute)))
	require.NoError(t, s.CreateWebSetupToken(ctx, "other", webHash(2), testNow, testNow.Add(time.Minute)))
	_, err = s.UseWebSetupToken(ctx, webHash(2), webHash(3), testNow, testNow.Add(time.Hour))
	require.NoError(t, err)

	_, err = s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = 'other'`)
	require.NoError(t, err)
	_, err = s.WebSessionByHash(ctx, webHash(3))
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.UseWebSetupToken(ctx, webHash(1), webHash(4), testNow, testNow.Add(time.Hour))
	require.ErrorIs(t, err, ErrNotFound)
}
