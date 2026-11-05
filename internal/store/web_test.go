package store

import (
	"bytes"
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
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
	require.NotZero(t, session.ID)
	require.Equal(t, &WebSessionRow{
		ID:         session.ID,
		Hash:       webHash(2),
		Tenant:     tenant,
		CreatedAt:  testNow.Add(time.Minute),
		ExpiresAt:  sessionExpiry,
		LastSeenAt: testNow.Add(time.Minute),
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
		`INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ` +
			strconv.FormatInt(testNow.UnixMilli(), 10) + `)`,
	}
	for name, hash := range map[string]any{"short": webHash(5)[:31], "long": bytes.Repeat([]byte{5}, 33), "text": "abc"} {
		for _, insert := range inserts {
			_, err := s.db.ExecContext(ctx, insert, hash, string(tenant), testNow.UnixMilli(), expires.UnixMilli())
			require.Error(t, err, "%s hash: %s", name, insert)
		}
	}
	const insertSession = `INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, insertSession,
		webHash(2), string(tenant), testNow.UnixMilli(), expires.UnixMilli(), testNow.UnixMilli())
	require.Error(t, err, "a session hash is unique")
	_, err = s.db.ExecContext(ctx, insertSession,
		webHash(7), string(tenant), testNow.UnixMilli(), expires.UnixMilli(), testNow.Add(-time.Second).UnixMilli())
	require.Error(t, err, "a session is not seen before it was made")
}

// newWebSession makes a session of the tenant with this hash, made at at, valid
// for 24 hours.
func newWebSession(t *testing.T, s *Store, tenant account.TenantID, fill byte, at time.Time) {
	t.Helper()
	ctx := context.Background()
	token := webHash(fill + 100)
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, token, at, at.Add(10*time.Minute)))
	_, err := s.UseWebSetupToken(ctx, token, webHash(fill), at, at.Add(24*time.Hour))
	require.NoError(t, err)
}

// otherTenant adds a second tenant.
func otherTenant(t *testing.T, s *Store) account.TenantID {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO tenants (id, created_at) VALUES ('other', 0)`)
	require.NoError(t, err)
	return "other"
}

func TestTouchWebSession(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	other := otherTenant(t, s)
	newWebSession(t, s, tenant, 1, testNow)

	later := testNow.Add(time.Hour)
	require.NoError(t, s.TouchWebSession(ctx, tenant, webHash(1), later))
	row, err := s.WebSessionByHash(ctx, webHash(1))
	require.NoError(t, err)
	require.Equal(t, later, row.LastSeenAt)

	require.NoError(t, s.TouchWebSession(ctx, tenant, webHash(1), testNow.Add(time.Minute)))
	row, err = s.WebSessionByHash(ctx, webHash(1))
	require.NoError(t, err)
	require.Equal(t, later, row.LastSeenAt, "the time never moves back")

	require.NoError(t, s.TouchWebSession(ctx, other, webHash(1), later.Add(time.Hour)))
	row, err = s.WebSessionByHash(ctx, webHash(1))
	require.NoError(t, err)
	require.Equal(t, later, row.LastSeenAt, "another tenant cannot touch the session")

	require.NoError(t, s.TouchWebSession(ctx, tenant, webHash(9), later), "a deleted session is not an error")
}

func TestListWebSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	other := otherTenant(t, s)

	newWebSession(t, s, tenant, 1, testNow)
	newWebSession(t, s, tenant, 2, testNow.Add(time.Minute))
	newWebSession(t, s, other, 3, testNow.Add(2*time.Minute))
	newWebSession(t, s, tenant, 4, testNow.Add(-30*time.Hour))
	require.NoError(t, s.TouchWebSession(ctx, tenant, webHash(1), testNow.Add(3*time.Minute)))

	now := testNow.Add(10 * time.Minute)
	got, err := s.ListWebSessions(ctx, tenant, now, testNow.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 2, "the session of the other tenant and the expired one are not listed")
	require.Equal(t, testNow.Add(3*time.Minute), got[0].LastSeenAt, "the one last used comes first")
	require.Equal(t, testNow.Add(time.Minute), got[1].CreatedAt)
	for _, row := range got {
		require.Empty(t, row.Hash, "the list never holds a hash")
		require.Equal(t, tenant, row.Tenant)
	}

	got, err = s.ListWebSessions(ctx, tenant, now, testNow.Add(2*time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 1, "a session not seen since the limit is idle")
}

func TestDeleteWebSessionByID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	other := otherTenant(t, s)
	newWebSession(t, s, tenant, 1, testNow)
	row, err := s.WebSessionByHash(ctx, webHash(1))
	require.NoError(t, err)

	require.ErrorIs(t, s.DeleteWebSessionByID(ctx, other, row.ID), ErrNotFound, "another tenant cannot delete it")
	_, err = s.WebSessionByHash(ctx, webHash(1))
	require.NoError(t, err)

	require.NoError(t, s.DeleteWebSessionByID(ctx, tenant, row.ID))
	_, err = s.WebSessionByHash(ctx, webHash(1))
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteWebSessionByID(ctx, tenant, row.ID), ErrNotFound)
}

// A revoke deletes every session and every unused setup token of the tenant.
// It counts only the sessions that still worked, by the same rule as
// ListWebSessions.
func TestRevokeWebSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	other := otherTenant(t, s)
	now := testNow.Add(3 * time.Hour)
	idleSince := now.Add(-2 * time.Hour)

	newWebSession(t, s, tenant, 1, now.Add(-time.Minute))
	newWebSession(t, s, tenant, 2, now.Add(-time.Hour))
	newWebSession(t, s, other, 3, now.Add(-time.Minute))
	newWebSession(t, s, tenant, 4, idleSince.Add(-time.Hour))
	newWebSession(t, s, tenant, 5, idleSince)
	newWebSession(t, s, tenant, 6, now.Add(-30*time.Hour))

	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(7), now, now.Add(10*time.Minute)))
	require.NoError(t, s.CreateWebSetupToken(ctx, other, webHash(8), now, now.Add(10*time.Minute)))

	n, err := s.RevokeWebSessions(ctx, tenant, now, idleSince)
	require.NoError(t, err)
	require.EqualValues(t, 2, n, "an idle or an expired session did not work any more")

	for _, fill := range []byte{1, 2, 4, 5, 6} {
		_, err = s.WebSessionByHash(ctx, webHash(fill))
		require.ErrorIs(t, err, ErrNotFound, "every session of the tenant is deleted, also one that did not work")
	}
	_, err = s.UseWebSetupToken(ctx, webHash(7), webHash(9), now, now.Add(time.Hour))
	require.ErrorIs(t, err, ErrNotFound, "an unused setup token no longer works")

	_, err = s.WebSessionByHash(ctx, webHash(3))
	require.NoError(t, err, "the session of the other tenant stays")
	_, err = s.UseWebSetupToken(ctx, webHash(8), webHash(10), now, now.Add(time.Hour))
	require.NoError(t, err, "the setup token of the other tenant stays")

	n, err = s.RevokeWebSessions(ctx, tenant, now, idleSince)
	require.NoError(t, err)
	require.Zero(t, n)
}

// A failed revoke changes nothing: no session goes while a setup token stays.
func TestRevokeWebSessionsIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	newWebSession(t, s, tenant, 1, testNow)
	require.NoError(t, s.CreateWebSetupToken(ctx, tenant, webHash(2), testNow, testNow.Add(10*time.Minute)))
	_, err = s.db.ExecContext(ctx,
		`CREATE TRIGGER keep_tokens BEFORE DELETE ON web_setup_tokens BEGIN SELECT RAISE(ABORT, 'kept'); END`)
	require.NoError(t, err)

	_, err = s.RevokeWebSessions(ctx, tenant, testNow, testNow.Add(-time.Hour))
	require.Error(t, err)
	_, err = s.WebSessionByHash(ctx, webHash(1))
	require.NoError(t, err, "the session delete was rolled back")
}

// The migration signs every browser out: a session of the old program has a
// long life and no idle limit, and its cookie went to every local port.
func TestMigrationDeletesOldWebSessions(t *testing.T) {
	ctx := context.Background()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	require.NoError(t, err)
	before := fstest.MapFS{}
	all := fstest.MapFS{}
	for _, e := range entries {
		data, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		require.NoError(t, err)
		all[e.Name()] = &fstest.MapFile{Data: data}
		if strings.Compare(e.Name(), "0012") < 0 {
			before[e.Name()] = &fstest.MapFile{Data: data}
		}
	}
	db := newRawDB(t)
	require.NoError(t, migrate(ctx, db, before))
	_, err = db.ExecContext(ctx, `INSERT INTO tenants (id, created_at) VALUES ('local', 0)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO web_sessions (hash, tenant_id, created_at, expires_at) VALUES (?, 'local', 1, 2)`,
		webHash(1))
	require.NoError(t, err)

	require.NoError(t, migrate(ctx, db, all))

	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM web_sessions`).Scan(&rows))
	require.Zero(t, rows)
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
