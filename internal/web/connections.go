package web

// This file holds the connections page: the access tokens of the local HTTP
// server. An MCP client that connects over HTTP sends one.
//
// A new token is shown once, on the answer to the POST that makes it, with
// "Cache-Control: no-store". It is never put in an address, a redirect or the
// log, and the store keeps only its hash. Making and revoking go through the
// service, which writes an audit entry of the tenant without the token or its
// name. A token always has an explicit list
// of known scopes. The form offers each known scope and "all", which stands for
// the whole list. Revoking is a POST with the CSRF token, for a token of the
// tenant of the session only.
//
// The page also lists the browsers that are signed in, so the user can sign out
// one that is not in use. The list never holds a session id or its hash: a row
// is named by its number, and every use of that number is limited to the tenant
// of the session.

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// The name of the page of this file.
const pageConnections = "connections"

// codeConnectionsFailed is the log code of a failed connections page.
const codeConnectionsFailed = "connections_failed"

// scopeAll is the choice of the form that stands for every known scope.
const scopeAll = "all"

// The fixed texts of the connections page.
const (
	titleTokenAbsent = "Token not found"
	msgNoToken       = "This token was not found."
	titleNoSession   = "Browser not found"
	msgNoBrowser     = "This browser was not found. It may already be signed out."
	msgNoScope       = "Choose at least one permission for the token."
	msgBadTokenName  = "The name must be 1 to 128 characters of text."
	msgCopyOnce      = "Copy this token now. It is shown only once."
)

// connectionsPage is the data of the connections page.
type connectionsPage struct {
	Error string
	// NewToken is the token that was just made. It is set only on the answer to
	// the POST that made it.
	NewToken string
	CopyOnce string
	// Name is the name in the form, after a refusal.
	Name   string
	Scopes []choice
	Tokens []tokenRow
	// Sessions are the browsers that are signed in.
	Sessions []sessionRow
}

// tokenRow is one token in the list. It never holds the token.
type tokenRow struct {
	ID      string
	Name    string
	Scopes  string
	Created string
	Revoked string
}

// sessionRow is one signed-in browser in the list. It never holds the session
// id or its hash.
type sessionRow struct {
	ID       int64
	Created  string
	LastUsed string
	Ends     string
	// Current is true for the browser that shows the page.
	Current bool
}

// handleConnections lists the tokens of the tenant of the session. It changes
// nothing.
func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	page := connectionsPage{Scopes: scopeChoices(nil)}
	s.renderConnections(w, r, http.StatusOK, page)
}

// handleCreateToken makes a token with the posted name and scopes with
// Service.CreateToken (which writes the audit entry), and shows it once on the
// answer. It does not redirect, so the token is never in an address.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeConnectionsFailed, errNoService)
		return
	}
	page := connectionsPage{Name: r.PostForm.Get("name"), Scopes: scopeChoices(r.PostForm["scope"])}
	scopes, ok := readScopes(r.PostForm["scope"])
	if !ok {
		page.Error = msgNoScope
		s.renderConnections(w, r, http.StatusUnprocessableEntity, page)
		return
	}
	token, err := svc.CreateToken(ctx, page.Name, scopes)
	switch {
	case errors.Is(err, tokens.ErrInvalidName):
		page.Error = msgBadTokenName
		s.renderConnections(w, r, http.StatusUnprocessableEntity, page)
		return
	case errors.Is(err, tokens.ErrInvalidScopes):
		page.Error = msgNoScope
		s.renderConnections(w, r, http.StatusUnprocessableEntity, page)
		return
	case err != nil:
		s.fail(w, r, codeConnectionsFailed, err)
		return
	}
	// The guard sets this header on every answer. It is set here again, because
	// this answer must never be kept: it holds the token.
	w.Header().Set("Cache-Control", "no-store")
	s.renderConnections(w, r, http.StatusOK, connectionsPage{
		NewToken: token, CopyOnce: msgCopyOnce, Scopes: scopeChoices(nil),
	})
}

// handleRevokeToken revokes a token of the tenant of the session with
// Service.RevokeToken (which writes the audit entry), and then redirects to the
// page. A token of another tenant is not found.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	svc, _, ctx, ok := s.userCall(r)
	if !ok {
		s.fail(w, r, codeConnectionsFailed, errNoService)
		return
	}
	err := svc.RevokeToken(ctx, r.PathValue("id"))
	switch {
	case err == nil:
		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	case errors.Is(err, tokens.ErrNotFound):
		s.message(w, r, http.StatusNotFound, titleTokenAbsent, msgNoToken)
	default:
		s.fail(w, r, codeConnectionsFailed, err)
	}
}

// handleRevokeSession signs out one browser of the tenant of the session, and
// redirects to the page. A session of another tenant is not found. When the
// session is the one of this request, the cookie is cleared too, and the browser
// goes to the no-session page.
func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		s.fail(w, r, codeConnectionsFailed, errNoService)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.message(w, r, http.StatusNotFound, titleNoSession, msgNoBrowser)
		return
	}
	err = s.d.Store.DeleteWebSessionByID(r.Context(), sess.tenant, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.message(w, r, http.StatusNotFound, titleNoSession, msgNoBrowser)
	case err != nil:
		s.fail(w, r, codeConnectionsFailed, err)
	case id == sess.id:
		setSessionCookie(w, "", -1)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	}
}

// renderConnections adds the lists of the tokens and the browsers to the page
// and renders it.
func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, status int, page connectionsPage) {
	sess, ok := sessionFrom(r.Context())
	if !ok || s.d.Tokens == nil {
		s.fail(w, r, codeConnectionsFailed, errNoService)
		return
	}
	list, err := s.d.Tokens.List(r.Context(), sess.tenant)
	if err != nil {
		s.fail(w, r, codeConnectionsFailed, err)
		return
	}
	for _, t := range list {
		row := tokenRow{
			ID:      t.ID,
			Name:    t.Name,
			Scopes:  strings.Join(t.Scopes, ", "),
			Created: formatTime(t.CreatedAt),
		}
		if !t.RevokedAt.IsZero() {
			row.Revoked = formatTime(t.RevokedAt)
		}
		page.Tokens = append(page.Tokens, row)
	}
	now := s.now()
	sessions, err := s.d.Store.ListWebSessions(r.Context(), sess.tenant, now, now.Add(-sessionIdleTTL))
	if err != nil {
		s.fail(w, r, codeConnectionsFailed, err)
		return
	}
	for _, row := range sessions {
		page.Sessions = append(page.Sessions, sessionRow{
			ID:       row.ID,
			Created:  formatTime(row.CreatedAt),
			LastUsed: formatTime(row.LastSeenAt),
			Ends:     formatTime(row.ExpiresAt),
			Current:  row.ID == sess.id,
		})
	}
	s.render(w, r, status, pageConnections, page)
}

// readScopes returns the scopes of the form: each known scope once, in the
// order of policy.KnownScopes. "all" stands for every known scope. ok is false
// when there is none, or when one is not known.
func readScopes(values []string) (policy.Scopes, bool) {
	if len(values) == 0 {
		return nil, false
	}
	known := policy.KnownScopes()
	chosen := map[string]bool{}
	for _, v := range values {
		switch {
		case v == scopeAll:
			for _, k := range known {
				chosen[k] = true
			}
		case policy.KnownScope(v):
			chosen[v] = true
		default:
			return nil, false
		}
	}
	var scopes policy.Scopes
	for _, k := range known {
		if chosen[k] {
			scopes = append(scopes, k)
		}
	}
	return scopes, true
}

// scopeChoices returns the choices of the form: "all" and each known scope,
// ticked when the posted values have it.
func scopeChoices(posted []string) []choice {
	out := []choice{{Value: scopeAll, Label: "All of them", Selected: slices.Contains(posted, scopeAll)}}
	for _, k := range policy.KnownScopes() {
		out = append(out, choice{Value: k, Label: k, Selected: slices.Contains(posted, k)})
	}
	return out
}
