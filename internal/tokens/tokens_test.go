package tokens

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// testNow is the clock of the manager in these tests.
var testNow = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// fixture is a manager over a real store in a temporary directory.
type fixture struct {
	t      *testing.T
	dir    string
	store  *store.Store
	tenant account.TenantID
	now    time.Time
	m      *Manager
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	f := &fixture{t: t, dir: dir, store: st, tenant: tenant, now: testNow}
	f.m = New(st, func() time.Time { return f.now })
	return f
}

// create makes a token of the local tenant and fails the test on an error.
func (f *fixture) create(name string, scopes ...string) string {
	f.t.Helper()
	token, err := f.m.Create(context.Background(), f.tenant, name, policy.Scopes(scopes))
	require.NoError(f.t, err)
	return token
}

// addTenant adds a tenant other than "local". The store has no method for it,
// so the test writes the row on a second connection.
func (f *fixture) addTenant(id account.TenantID) {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err = db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`, string(id), testNow.UnixMilli())
	require.NoError(f.t, err)
}

// dataFiles returns the bytes of every file in the data directory: the database
// and its write-ahead log.
func (f *fixture) dataFiles() []byte {
	f.t.Helper()
	entries, err := os.ReadDir(f.dir)
	require.NoError(f.t, err)
	var all []byte
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		require.NoError(f.t, err)
		all = append(all, data...)
	}
	return all
}

// tokenPattern is the form of a token: the prefix and 52 characters of
// lower-case base32 without padding (32 bytes).
var tokenPattern = regexp.MustCompile(`^emcp_[a-z2-7]{52}$`)

func TestTokenFormat(t *testing.T) {
	f := newFixture(t)
	first := f.create("laptop", "email:read")
	second := f.create("laptop", "email:read")

	require.Regexp(t, tokenPattern, first)
	require.Regexp(t, tokenPattern, second)
	require.NotEqual(t, first, second, "every token is random")

	raw, err := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).
		DecodeString(strings.TrimPrefix(first, "emcp_"))
	require.NoError(t, err)
	require.Len(t, raw, 32)
}

func TestTokenVerifyAndRevoke(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	token := f.create("laptop", "email:send", "email:read", "email:read")

	p, err := f.m.Verify(ctx, token)
	require.NoError(t, err)
	require.Equal(t, service.Principal{Tenant: f.tenant, Scopes: policy.Scopes{"email:read", "email:send"}}, p,
		"the scopes are sorted and each one is kept once")

	list, err := f.m.List(ctx, f.tenant)
	require.NoError(t, err)
	require.Len(t, list, 1)
	info := list[0]
	require.NotEmpty(t, info.ID)
	require.NotContains(t, token, info.ID, "the id is not a part of the token")
	require.Equal(t, Info{
		ID:        info.ID,
		Name:      "laptop",
		Scopes:    policy.Scopes{"email:read", "email:send"},
		CreatedAt: testNow,
	}, info)

	f.now = testNow.Add(time.Hour)
	require.NoError(t, f.m.Revoke(ctx, f.tenant, info.ID))
	_, err = f.m.Verify(ctx, token)
	require.ErrorIs(t, err, ErrInvalidToken, "a revoked token fails at once")

	list, err = f.m.List(ctx, f.tenant)
	require.NoError(t, err)
	require.Equal(t, testNow.Add(time.Hour), list[0].RevokedAt, "the list shows when the token was revoked")

	f.now = testNow.Add(2 * time.Hour)
	require.NoError(t, f.m.Revoke(ctx, f.tenant, info.ID), "a second revoke is not an error")
	list, err = f.m.List(ctx, f.tenant)
	require.NoError(t, err)
	require.Equal(t, testNow.Add(time.Hour), list[0].RevokedAt, "a second revoke keeps the first time")

	require.ErrorIs(t, f.m.Revoke(ctx, f.tenant, "no-such-token"), ErrNotFound)
}

func TestTokenRevokeIsScopedToTheTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addTenant("tenant-two")
	token := f.create("laptop", "email:read")
	list, err := f.m.List(ctx, f.tenant)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.ErrorIs(t, f.m.Revoke(ctx, "tenant-two", list[0].ID), ErrNotFound)
	theirs, err := f.m.List(ctx, "tenant-two")
	require.NoError(t, err)
	require.Empty(t, theirs, "a tenant does not see the tokens of another tenant")

	p, err := f.m.Verify(ctx, token)
	require.NoError(t, err, "the token is still valid")
	require.Equal(t, f.tenant, p.Tenant)
}

func TestTokenStoredHashed(t *testing.T) {
	f := newFixture(t)
	token := f.create("laptop", "email:read")
	body := strings.TrimPrefix(token, "emcp_")
	raw, err := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).DecodeString(body)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(token))

	all := f.dataFiles()
	require.NotEmpty(t, all)
	require.False(t, bytes.Contains(all, []byte(token)), "the token is not in the database or its log")
	require.False(t, bytes.Contains(all, []byte(body)), "the random part of the token is not stored")
	require.False(t, bytes.Contains(all, []byte(strings.ToUpper(body))), "nor in upper case")
	require.False(t, bytes.Contains(all, raw), "the random bytes of the token are not stored")
	require.True(t, bytes.Contains(all, sum[:]), "the SHA-256 of the token is stored")
}

func TestCreateRefusesBadScopes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for name, scopes := range map[string]policy.Scopes{
		"nil":             nil,
		"empty":           {},
		"unknown":         {"email:everything"},
		"known + unknown": {"email:read", "contacts:read"},
		"empty name":      {""},
		"other case":      {"EMAIL:READ"},
	} {
		t.Run(name, func(t *testing.T) {
			token, err := f.m.Create(ctx, f.tenant, "laptop", scopes)
			require.ErrorIs(t, err, ErrInvalidScopes)
			require.Empty(t, token)
		})
	}
	list, err := f.m.List(ctx, f.tenant)
	require.NoError(t, err)
	require.Empty(t, list, "no refused token was stored")
}

func TestCreateRefusesBadName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for name, tokenName := range map[string]string{
		"empty":         "",
		"only spaces":   "   ",
		"line break":    "laptop\nadmin",
		"escape":        "laptop\x1b[2J",
		"not UTF-8":     "laptop\xff",
		"too long":      strings.Repeat("n", 129),
		"bidi override": "laptop\u202e",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.m.Create(ctx, f.tenant, tokenName, policy.Scopes{"email:read"})
			require.ErrorIs(t, err, ErrInvalidName)
		})
	}
	_, err := f.m.Create(ctx, f.tenant, strings.Repeat("n", 128), policy.Scopes{"email:read"})
	require.NoError(t, err, "a name of 128 bytes is allowed")
}

func TestCreateNeedsAKnownTenant(t *testing.T) {
	f := newFixture(t)
	_, err := f.m.Create(context.Background(), "nobody", "laptop", policy.Scopes{"email:read"})
	require.Error(t, err)
	_, err = f.m.Create(context.Background(), "", "laptop", policy.Scopes{"email:read"})
	require.Error(t, err)
}

func TestVerifyUnknownToken(t *testing.T) {
	f := newFixture(t)
	f.create("laptop", "email:read")
	unknown := "emcp_" + strings.Repeat("a", 52)
	_, err := f.m.Verify(context.Background(), unknown)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// TestVerifyRejectsMalformedWithoutLookup closes the store first. A token that
// reached the database would get the error of the closed database, so an
// ErrInvalidToken shows that the check came before any lookup.
func TestVerifyRejectsMalformedWithoutLookup(t *testing.T) {
	f := newFixture(t)
	valid := f.create("laptop", "email:read")
	body := strings.TrimPrefix(valid, "emcp_")
	require.NoError(t, f.store.Close())
	ctx := context.Background()

	malformed := map[string]string{
		"empty":             "",
		"prefix only":       "emcp_",
		"no prefix":         body,
		"other prefix":      "emcx_" + body,
		"upper-case prefix": "EMCP_" + body,
		"upper-case body":   "emcp_" + strings.ToUpper(body),
		"one char short":    valid[:len(valid)-1],
		"one char long":     valid + "a",
		"padding":           valid[:len(valid)-1] + "=",
		"digit 1":           valid[:len(valid)-1] + "1",
		"digit 8":           valid[:len(valid)-1] + "8",
		"space":             valid[:len(valid)-1] + " ",
		"leading space":     " " + valid[1:],
		"bearer word":       "Bearer " + valid,
		"non-canonical end": "emcp_" + strings.Repeat("a", 51) + "b",
		"not ASCII":         valid[:len(valid)-2] + "é",
	}
	for name, token := range malformed {
		t.Run(name, func(t *testing.T) {
			_, err := f.m.Verify(ctx, token)
			require.ErrorIs(t, err, ErrInvalidToken)
		})
	}

	// A well-formed token does reach the closed database, so the test above
	// would see a database error if the format check were missing.
	_, err := f.m.Verify(ctx, valid)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInvalidToken, "a database error is not an invalid token")
}

func TestVerifyReturnsAFreshScopeList(t *testing.T) {
	f := newFixture(t)
	token := f.create("laptop", "email:read")
	p, err := f.m.Verify(context.Background(), token)
	require.NoError(t, err)
	require.NotNil(t, p.Scopes, "a token always has a scope list, never the nil that means no limit")
	p.Scopes[0] = "email:delete"

	again, err := f.m.Verify(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, policy.Scopes{"email:read"}, again.Scopes)
}
