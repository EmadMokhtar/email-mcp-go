package web

// This file holds the checks that run before every page: the security headers,
// the Host check, the cross-origin check, the session check and the form token
// (CSRF) check. The package documentation lists their order.

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/EmadMokhtar/email-mcp-go/internal/httpserve"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
)

// contentSecurityPolicy lets a page load only from its own origin, runs no
// script at all, sends forms only to its own origin, and cannot be put in a
// frame.
const contentSecurityPolicy = "default-src 'self'; script-src 'none'; form-action 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// csrfField is the name of the hidden form field with the CSRF token.
const csrfField = "csrf_token"

// Fixed codes for the log.
const (
	codeBadHost      = "bad_host"
	codeCrossOrigin  = "cross_origin"
	codeNoSession    = "no_session"
	codeBadCSRF      = "bad_csrf"
	codeBadForm      = "bad_form"
	codeBodyTooLarge = "body_too_large"
)

// serve runs the checks of every request, in the order of the package
// documentation, and then the route of the page.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if !hostAllowed(r) {
		s.refuse(w, http.StatusForbidden, codeBadHost)
		return
	}
	// Check lets GET, HEAD and OPTIONS pass, and refuses any other request that
	// a browser marks as cross-origin.
	if err := s.cop.Check(r); err != nil {
		s.refuse(w, http.StatusForbidden, codeCrossOrigin)
		return
	}
	if r.URL.Path == setupPath {
		s.mux.ServeHTTP(w, r)
		return
	}

	sess, err := s.sessionOf(r)
	if errors.Is(err, errNoSession) {
		s.noSession(w, r, codeNoSession)
		return
	}
	if err != nil {
		s.fail(w, r, codeSessionCheck, err)
		return
	}
	r = r.WithContext(withSession(r.Context(), sess))

	if r.Method != http.MethodGet && !s.checkForm(w, r, sess) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// setSecurityHeaders sets the headers of every answer. The pages hold private
// data, so no answer is kept in a cache, and no address is sent to another site
// as a Referer.
func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

// hostAllowed reports whether the Host header is exactly "127.0.0.1:<port>" or
// "localhost:<port>", where <port> is the port of the connection, and the
// connection came in on a loopback address. A page of an attacker that uses
// DNS rebinding (a name of the attacker that points to 127.0.0.1) sends its
// own name, so it is refused. Any other spelling is refused too, such as
// "[::1]", upper case, a final dot or a port with a leading zero: the server
// listens only on 127.0.0.1.
func hostAllowed(r *http.Request) bool {
	port, ok := httpserve.LoopbackPort(r)
	if !ok {
		return false
	}
	return r.Host == "127.0.0.1:"+port || r.Host == "localhost:"+port
}

// checkForm checks the CSRF token of a request that is not GET, and answers the
// request when the check fails. The token must be in the url-encoded form body,
// never in the address, and it must belong to the session of the request.
//
// The body is read here, up to 15 MB. It is read only after the session check,
// so a browser without a session cannot make the server read a body. The page
// handler finds the parsed form in r.PostForm.
func (s *Server) checkForm(w http.ResponseWriter, r *http.Request, sess session) bool {
	r.Body = http.MaxBytesReader(w, r.Body, httpserve.MaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.refuse(w, http.StatusRequestEntityTooLarge, codeBodyTooLarge)
			return false
		}
		s.refuse(w, http.StatusBadRequest, codeBadForm)
		return false
	}
	if !s.csrfValid(sess, r.PostForm.Get(csrfField)) {
		s.refuse(w, http.StatusForbidden, codeBadCSRF)
		return false
	}
	return true
}

// csrfToken returns the form token of the session: the SHA-256 of the session
// id, signed for the purpose "csrf", valid as long as a session. A page puts it
// in each form with {{template "csrf_field" .CSRFToken}}; render fills it in.
func (s *Server) csrfToken(sess session) (string, error) {
	return s.d.Signer.Seal(signing.PurposeCSRF, sess.hash, sessionTTL)
}

// csrfValid reports whether token is a genuine form token of this session. A
// token of another session, a changed token, an expired one, or one made for
// another purpose or with another key is refused.
func (s *Server) csrfValid(sess session, token string) bool {
	if token == "" {
		return false
	}
	var hash []byte
	if err := s.d.Signer.Open(signing.PurposeCSRF, token, &hash); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(hash, sess.hash) == 1
}

// noSession answers with the no-session page and 401. The page holds only the
// sentence that tells the user to run "email-mcp setup", and nothing of the
// page that was asked for.
func (s *Server) noSession(w http.ResponseWriter, r *http.Request, code string) {
	s.log.Info("web request refused", "code", code, "status", http.StatusUnauthorized)
	s.render(w, r, http.StatusUnauthorized, pageNoSession, nil)
}

// refuse logs the code and answers the status with its standard text, such as
// "Forbidden". The answer repeats nothing of the request.
func (s *Server) refuse(w http.ResponseWriter, status int, code string) {
	s.log.Warn("web request refused", "code", code, "status", status)
	http.Error(w, http.StatusText(status), status)
}
