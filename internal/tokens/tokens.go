// Package tokens makes and checks the access tokens of the local HTTP server.
//
// A token is "emcp_" and 52 characters: the 32 random bytes of the token in
// lower-case base32 (the alphabet a-z and 2-7) without padding. The store keeps
// only the SHA-256 of the token text. The plain token exists only in the answer
// of Create, so the user must copy it then.
//
// A token always has an explicit list of known scopes, such as "email:read". A
// nil list, which means "no limit" to the policy package, is only for the local
// stdio caller, which has no token.
//
// A new token has an end date (90 days by default), and it may answer a
// confirmation in its own window only when the user chose that. A token from
// before these two rules has no end date and may not.
//
// Nothing here logs a token or its hash.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

const (
	// prefix starts every token, so a token is easy to recognize, for example by
	// a tool that looks for secrets in code.
	prefix = "emcp_"
	// randomBytes is the number of random bytes in a token: 256 bits.
	randomBytes = 32
	// maxNameBytes is the longest name of a token, in bytes.
	maxNameBytes = 128
	// DefaultLifetime is how long a token stays valid when Create gets no lifetime.
	DefaultLifetime = 90 * 24 * time.Hour
	// MinLifetime and MaxLifetime are the shortest and the longest lifetime that
	// Create takes.
	MinLifetime = time.Minute
	MaxLifetime = 3650 * 24 * time.Hour
	// useGap is the shortest time between two writes of the last use of a token.
	// A check of a token must not write to the database on every call.
	useGap = time.Minute
)

// encoding is lower-case base32 without padding.
var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// tokenLen is the length of a token: the prefix and 52 characters.
var tokenLen = len(prefix) + encoding.EncodedLen(randomBytes)

var (
	// ErrInvalidToken means that a token is not valid: it has the wrong form, no
	// token has its hash, it was revoked, or it expired. The error does not say
	// which, so a caller learns nothing from it.
	ErrInvalidToken = errors.New("tokens: invalid token")
	// ErrInvalidScopes means that Create got no scopes, or a scope that the
	// policy package does not know.
	ErrInvalidScopes = errors.New("tokens: a token needs one or more known scopes")
	// ErrInvalidName means that the name of a new token is empty, longer than
	// 128 bytes, not valid UTF-8, or has a control or format character.
	ErrInvalidName = errors.New("tokens: the name must be 1 to 128 bytes of text without control or format characters")
	// ErrInvalidLifetime means that the lifetime of a new token is shorter than a
	// minute or longer than ten years.
	ErrInvalidLifetime = errors.New("tokens: the lifetime must be from one minute to ten years")
	// ErrNotFound means that the tenant has no token with the id.
	ErrNotFound = errors.New("tokens: no such token")
)

// Info describes a token for a list. It never holds the token or its hash.
type Info struct {
	ID     string
	Name   string
	Scopes policy.Scopes
	// InBandConfirm says that the client of the token may answer a confirmation
	// in its own window.
	InBandConfirm bool
	// CreatedAt is when the token was made. RevokedAt is when it was revoked, or
	// zero while it is not revoked. ExpiresAt is when it stops working, or zero for
	// a token that has no end date. LastUsedAt is about when it was last used, or
	// zero when it was not used yet. All are in UTC, in whole milliseconds.
	CreatedAt  time.Time
	RevokedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
}

// Expired reports whether the token is past its end date at the time now.
func (i Info) Expired(now time.Time) bool {
	return expired(i.ExpiresAt, now)
}

// expired reports whether end is set and has come at the time now.
func expired(end, now time.Time) bool {
	return !end.IsZero() && !now.Before(end)
}

// Manager is the TokenMaker of the service.
var _ service.TokenMaker = (*Manager)(nil)

// Manager makes, checks, lists and revokes tokens. It is safe for use by many
// goroutines. It keeps no cache, so a revoke takes effect on the next check.
type Manager struct {
	store *store.Store
	now   func() time.Time
}

// New returns a Manager over the store. now is the clock. A nil now uses
// time.Now.
func New(st *store.Store, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{store: st, now: now}
}

// Create makes a token for the tenant and returns it. The store keeps only its
// hash, so this is the only time the token is seen.
//
// name is a label for the user. It is trimmed of spaces at both ends. scopes
// must have at least one scope, and every scope must be known to the policy
// package. The stored list is sorted and has each scope once.
//
// opts.ExpiresIn must be zero, which means DefaultLifetime, or a time from
// MinLifetime to MaxLifetime.
func (m *Manager) Create(ctx context.Context, tenant account.TenantID, name string, scopes policy.Scopes, opts service.TokenOptions) (string, error) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return "", ErrInvalidName
	}
	clean, err := cleanScopes(scopes)
	if err != nil {
		return "", err
	}
	lifetime := opts.ExpiresIn
	if lifetime == 0 {
		lifetime = DefaultLifetime
	}
	if lifetime < MinLifetime || lifetime > MaxLifetime {
		return "", ErrInvalidLifetime
	}

	raw := make([]byte, randomBytes)
	// crypto/rand.Read never returns an error. It ends the program when the
	// system has no random source.
	_, _ = rand.Read(raw)
	token := prefix + encoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	now := m.now()
	row := &store.APITokenRow{
		ID:            strings.ToLower(rand.Text()),
		Tenant:        tenant,
		Name:          name,
		Hash:          sum[:],
		Scopes:        clean,
		InBandConfirm: opts.InBandConfirm,
		CreatedAt:     now,
		ExpiresAt:     now.Add(lifetime),
	}
	if err := m.store.CreateAPIToken(ctx, row); err != nil {
		return "", fmt.Errorf("tokens: create: %w", err)
	}
	return token, nil
}

// Verify checks a token and returns its caller: the tenant, the scopes, the id of
// the token and whether it may confirm in band. The Client of the principal is
// empty: the HTTP layer fills it in.
//
// A token with the wrong form is refused before any lookup. For a token with the
// right form, it looks up the SHA-256 of the token and compares the stored hash
// again in constant time. A revoked token and an expired token are refused. Every
// refusal is ErrInvalidToken. Any other error is a problem of the database, and
// it holds no part of the token.
//
// The scopes of the principal are a new list that is never nil, so a token can
// never mean "no limit".
//
// A valid token notes its last use, at most about once a minute. A failure of that
// write does not refuse the token: the time is only for the user to read.
func (m *Manager) Verify(ctx context.Context, token string) (service.Principal, error) {
	if !wellFormed(token) {
		return service.Principal{}, ErrInvalidToken
	}
	sum := sha256.Sum256([]byte(token))
	row, err := m.store.APITokenByHash(ctx, sum[:])
	if errors.Is(err, store.ErrNotFound) {
		return service.Principal{}, ErrInvalidToken
	}
	if err != nil {
		return service.Principal{}, fmt.Errorf("tokens: verify: %w", err)
	}
	// The database found the row by the hash. The hash is compared here once
	// more, in constant time, so the answer does not depend on how the database
	// compares bytes.
	now := m.now()
	if subtle.ConstantTimeCompare(row.Hash, sum[:]) != 1 || !row.RevokedAt.IsZero() ||
		expired(row.ExpiresAt, now) || len(row.Scopes) == 0 {
		return service.Principal{}, ErrInvalidToken
	}
	if now.Sub(row.LastUsedAt) >= useGap {
		_ = m.store.NoteAPITokenUse(ctx, row.Tenant, row.ID, now, useGap)
	}
	return service.Principal{
		Tenant:        row.Tenant,
		Scopes:        policy.Scopes(slices.Clone(row.Scopes)),
		TokenID:       row.ID,
		InBandConfirm: row.InBandConfirm,
	}, nil
}

// List returns the tokens of the tenant, revoked ones too, oldest first.
func (m *Manager) List(ctx context.Context, tenant account.TenantID) ([]Info, error) {
	rows, err := m.store.ListAPITokens(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("tokens: list: %w", err)
	}
	list := make([]Info, 0, len(rows))
	for _, r := range rows {
		list = append(list, Info{
			ID:            r.ID,
			Name:          r.Name,
			Scopes:        policy.Scopes(r.Scopes),
			InBandConfirm: r.InBandConfirm,
			CreatedAt:     r.CreatedAt,
			RevokedAt:     r.RevokedAt,
			ExpiresAt:     r.ExpiresAt,
			LastUsedAt:    r.LastUsedAt,
		})
	}
	return list, nil
}

// Revoke revokes the token of the tenant with the id. The next Verify of the
// token fails. Revoking a token that is already revoked succeeds and keeps the
// first time. It returns ErrNotFound when the tenant has no token with the id,
// also when another tenant has one.
func (m *Manager) Revoke(ctx context.Context, tenant account.TenantID, id string) error {
	err := m.store.RevokeAPIToken(ctx, tenant, id, m.now())
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("tokens: revoke: %w", err)
	}
	return nil
}

// wellFormed reports whether token has the form of a token: the prefix, then
// exactly the base32 text of 32 bytes. Each token has only one spelling: the
// text must be what encoding the decoded bytes gives back. That refuses upper
// case, padding, line breaks, and a last character with bits that are not zero.
func wellFormed(token string) bool {
	if len(token) != tokenLen || !strings.HasPrefix(token, prefix) {
		return false
	}
	body := token[len(prefix):]
	raw, err := encoding.DecodeString(body)
	if err != nil || len(raw) != randomBytes {
		return false
	}
	return encoding.EncodeToString(raw) == body
}

// validName reports whether name can be the name of a token: 1 to 128 bytes of
// valid UTF-8, without a control character (Cc), a format character (Cf, such as
// the right-to-left override) or a line or paragraph separator (Zl, Zp). The
// name is shown in a terminal, and such characters could change how the list
// looks there.
func validName(name string) bool {
	if name == "" || len(name) > maxNameBytes || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// cleanScopes returns the scopes sorted and with each scope once. It returns
// ErrInvalidScopes when there are none or when one is not known.
func cleanScopes(scopes policy.Scopes) ([]string, error) {
	if len(scopes) == 0 {
		return nil, ErrInvalidScopes
	}
	for _, s := range scopes {
		if !policy.KnownScope(s) {
			return nil, ErrInvalidScopes
		}
	}
	clean := slices.Clone([]string(scopes))
	slices.Sort(clean)
	return slices.Compact(clean), nil
}
