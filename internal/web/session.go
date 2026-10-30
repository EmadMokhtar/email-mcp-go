package web

// This file holds the sign-in: the one-time setup link, the swap of its token
// for a session, and the check of the session cookie.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

const (
	// setupPath is the page that swaps a setup token for a session. It is the
	// only page that works without a session.
	setupPath = "/setup"
	// cookieName is the name of the session cookie.
	cookieName = "emcp_session"
	// setupTokenTTL is how long a setup link works.
	setupTokenTTL = 10 * time.Minute
	// sessionTTL is how long a session works.
	sessionTTL = 30 * 24 * time.Hour
	// randomBytes is the number of random bytes in a setup token and in a
	// session id: 256 bits.
	randomBytes = 32
)

// Fixed codes for the log.
const (
	codeBadSetupToken = "bad_setup_token"
	codeSetupFailed   = "setup_failed"
	codeSessionCheck  = "session_check_failed"
	codeSessionDelete = "session_delete_failed"
)

// randomTextLen is the length of a setup token or a session id: 32 bytes in
// base64url without padding.
var randomTextLen = base64.RawURLEncoding.EncodedLen(randomBytes)

// errNoSession means that the request has no valid session: no cookie, a
// malformed cookie, an unknown session or an expired one.
var errNoSession = errors.New("web: no valid session")

// session is the signed-in browser of a request.
type session struct {
	// hash is the SHA-256 of the session id: the key of the row in the store.
	hash   []byte
	tenant account.TenantID
}

// sessionKey is the context key of the session of a request.
type sessionKey struct{}

// withSession returns a copy of ctx that carries the session.
func withSession(ctx context.Context, sess session) context.Context {
	return context.WithValue(ctx, sessionKey{}, sess)
}

// sessionFrom returns the session of the request, if it has one. Every page
// except /setup and the no-session page has one: the guard puts it there.
func sessionFrom(ctx context.Context) (session, bool) {
	sess, ok := ctx.Value(sessionKey{}).(session)
	return sess, ok
}

// NewSetupURL starts the server if needed, makes a setup token for the local
// tenant, and returns the link that signs a browser in:
// "http://127.0.0.1:<port>/setup?token=<token>". The token works once, for 10
// minutes. The store keeps only its hash.
//
// Only the "email-mcp setup" command calls it, and it opens the link in the
// browser of the user. This link is the only URL with a secret in it. It is
// never given to the agent.
func (s *Server) NewSetupURL(ctx context.Context) (string, error) {
	base, err := s.EnsureStarted(ctx)
	if err != nil {
		return "", err
	}
	tenant, err := s.d.Store.EnsureLocalTenant(ctx)
	if err != nil {
		return "", fmt.Errorf("web: setup link: %w", err)
	}
	token := randomText()
	now := s.now()
	if err := s.d.Store.CreateWebSetupToken(ctx, tenant, hashText(token), now, now.Add(setupTokenTTL)); err != nil {
		return "", fmt.Errorf("web: setup link: %w", err)
	}
	return base + setupPath + "?" + url.Values{"token": {token}}.Encode(), nil
}

// handleSetup swaps the setup token in the query for a session. It sets the
// session cookie and redirects to "/" with 303, so the token leaves the address
// bar and the history entry of the page. A missing, malformed, used, expired or
// unknown token gets the no-session page, the same for all.
//
// It runs for GET and for HEAD, so a HEAD request also uses up the token. This
// is the one place where a safe method changes state (see the package doc).
//
// The guard has already set "Cache-Control: no-store" and
// "Referrer-Policy: no-referrer", so the browser neither keeps this answer nor
// sends the address with the token to another page.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()["token"]
	if len(values) != 1 || !wellFormed(values[0]) {
		s.noSession(w, r, codeBadSetupToken)
		return
	}
	id := randomText()
	now := s.now()
	_, err := s.d.Store.UseWebSetupToken(r.Context(), hashText(values[0]), hashText(id), now, now.Add(sessionTTL))
	if errors.Is(err, store.ErrNotFound) {
		s.noSession(w, r, codeBadSetupToken)
		return
	}
	if err != nil {
		s.fail(w, r, codeSetupFailed, err)
		return
	}
	// The cookie has no Domain, so it is sent only to the host 127.0.0.1. It is
	// not Secure, because the server speaks plain HTTP on the loopback address.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: not Secure on purpose, the server is plain HTTP on 127.0.0.1
		Name:     cookieName,
		Value:    id,
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// sessionOf checks the session cookie of the request. It returns errNoSession
// when the cookie is missing or malformed, when no session has its hash, or
// when the session expired. An expired session is deleted here. Any other error
// is a problem of the database.
func (s *Server) sessionOf(r *http.Request) (session, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || !wellFormed(cookie.Value) {
		return session{}, errNoSession
	}
	hash := hashText(cookie.Value)
	row, err := s.d.Store.WebSessionByHash(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		return session{}, errNoSession
	}
	if err != nil {
		return session{}, err
	}
	// The database found the row by the hash. The hash is compared here once
	// more, in constant time, so the answer does not depend on how the database
	// compares bytes.
	if subtle.ConstantTimeCompare(row.Hash, hash) != 1 {
		return session{}, errNoSession
	}
	if !s.now().Before(row.ExpiresAt) {
		// The session is refused either way. A failed delete is tried again on
		// the next request with the cookie, or when the next session is made.
		if err := s.d.Store.DeleteWebSession(r.Context(), hash); err != nil {
			s.log.Warn("expired web session not deleted", "code", codeSessionDelete,
				"error_types", errtype.Chain(err))
		}
		return session{}, errNoSession
	}
	return session{hash: hash, tenant: row.Tenant}, nil
}

// randomText returns 32 random bytes in base64url without padding: 43
// characters that need no escaping in a URL or a cookie.
func randomText() string {
	b := make([]byte, randomBytes)
	// crypto/rand.Read never returns an error. It ends the program when the
	// system has no random source.
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// wellFormed reports whether text has the form that randomText makes. Each
// value has one spelling: the text must be what encoding its bytes gives back.
// A value with another form is refused before any database lookup.
func wellFormed(text string) bool {
	if len(text) != randomTextLen {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(text)
	return err == nil && len(b) == randomBytes && base64.RawURLEncoding.EncodeToString(b) == text
}

// hashText returns the SHA-256 of the text: what the store keeps instead of a
// setup token or a session id.
func hashText(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}
