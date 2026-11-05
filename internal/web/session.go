package web

// This file holds the sign-in: the one-time setup link, the swap of its token
// for a session, the check of the session cookie, and the sign-out.

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
	// sessionTTL is the longest time that a session works, in use or not. The
	// user runs "email-mcp setup" to sign in again.
	sessionTTL = 24 * time.Hour
	// sessionIdleTTL is how long a session works without a request. Each request
	// starts this time again.
	sessionIdleTTL = 2 * time.Hour
	// sessionTouchStep is the shortest time between two writes of the last use
	// of a session, so one page does not write to the database many times. The
	// idle limit can be up to this much shorter than sessionIdleTTL.
	sessionTouchStep = time.Minute
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
	codeSessionTouch  = "session_touch_failed"
	codeSignOutFailed = "sign_out_failed"
)

// randomTextLen is the length of a setup token or a session id: 32 bytes in
// base64url without padding.
var randomTextLen = base64.RawURLEncoding.EncodedLen(randomBytes)

// errNoSession means that the request has no valid session: no cookie, a
// malformed cookie, an unknown session or an expired one.
var errNoSession = errors.New("web: no valid session")

// session is the signed-in browser of a request.
type session struct {
	// id is the number of the row, which the Connections page shows.
	id int64
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
// "http://email-mcp.localhost:<port>/setup?token=<token>". The token works
// once, for 10 minutes. The store keeps only its hash.
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

// SignInHelpURL starts the server if needed, and returns the address of the page
// that helps a browser that cannot find the name of the web interface:
// "http://127.0.0.1:<port>/setup". It holds no token, and no session starts
// there.
func (s *Server) SignInHelpURL(ctx context.Context) (string, error) {
	base, err := s.EnsureStarted(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("web: help link: %w", err)
	}
	return "http://" + fallbackHost + ":" + u.Port() + setupPath, nil
}

// RevokeSessions deletes every session and every unused setup token of the local
// tenant, so no browser stays signed in and no sign-in link works. It returns how
// many sessions still worked. Every server reads the database on each request, so
// it also signs out the browsers of a server in another process.
func (s *Server) RevokeSessions(ctx context.Context) (int64, error) {
	tenant, err := s.d.Store.EnsureLocalTenant(ctx)
	if err != nil {
		return 0, fmt.Errorf("web: revoke sessions: %w", err)
	}
	now := s.now()
	n, err := s.d.Store.RevokeWebSessions(ctx, tenant, now, now.Add(-sessionIdleTTL))
	if err != nil {
		return 0, fmt.Errorf("web: revoke sessions: %w", err)
	}
	return n, nil
}

// handleSetup swaps the setup token in the query for a session. It sets the
// session cookie and redirects to "/" with 303, so the token leaves the address
// bar and the history entry of the page. A missing, malformed, used, expired or
// unknown token gets the no-session page, the same for all.
//
// The guard calls it only on the name of the web interface, so the cookie is
// never set for 127.0.0.1 or localhost.
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
	setSessionCookie(w, id, int(sessionTTL/time.Second))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleSignOut deletes the session of the request and clears its cookie. Then
// the browser shows the no-session page.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		s.fail(w, r, codeSignOutFailed, errNoSession)
		return
	}
	if err := s.d.Store.DeleteWebSession(r.Context(), sess.hash); err != nil {
		s.fail(w, r, codeSignOutFailed, err)
		return
	}
	setSessionCookie(w, "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// setSessionCookie sets the session cookie. A negative maxAge clears it.
//
// A browser sends a cookie to every port of a host. This cookie has no Domain,
// so it goes only to the name of the web interface, which no other local program
// uses. On 127.0.0.1 every local web service would get it. It is not Secure,
// because the server speaks plain HTTP on the loopback address.
//
// SameSite is Lax, so a link to a page that the user clicks in a web chat or in
// a webmail opens the page. The cookie goes only with a top-level GET from
// another site, and no GET route changes anything (see routes). Every POST needs
// the form token of the session.
func setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: not Secure on purpose, the server is plain HTTP on a loopback address
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// sessionOf checks the session cookie of the request. It returns errNoSession
// when the cookie is missing or malformed, when no session has its hash, or
// when the session expired or was idle too long. Such a session is deleted here.
// Any other error is a problem of the database. A good session counts as used
// now (see sessionTouchStep).
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
	now := s.now()
	if !now.Before(row.ExpiresAt) || !now.Before(row.LastSeenAt.Add(sessionIdleTTL)) {
		// The session is refused either way. A failed delete is tried again on
		// the next request with the cookie, or when the next session is made.
		if err := s.d.Store.DeleteWebSession(r.Context(), hash); err != nil {
			s.log.Warn("expired web session not deleted", "code", codeSessionDelete,
				"error_types", errtype.Chain(err))
		}
		return session{}, errNoSession
	}
	if now.Sub(row.LastSeenAt) >= sessionTouchStep {
		// The user is at the page, so a failed write does not stop the request.
		// The next request tries again.
		if err := s.d.Store.TouchWebSession(r.Context(), row.Tenant, hash, now); err != nil {
			s.log.Warn("web session use not recorded", "code", codeSessionTouch,
				"error_types", errtype.Chain(err))
		}
	}
	return session{id: row.ID, hash: hash, tenant: row.Tenant}, nil
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
