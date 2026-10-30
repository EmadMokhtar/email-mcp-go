// Package web serves the local web interface. The user, never the agent, uses
// it to connect accounts, change permission levels and approve pending actions.
//
// The agent can get links to the pages (see Links), but it must never be able
// to act on them. So a link carries no secret, and every page needs a browser
// session that only the user can make: the user runs "email-mcp setup" in a
// terminal, which opens a one-time setup link (NewSetupURL). The setup token in
// that link is the only secret that is ever in a URL. Opening it swaps the token
// for a session cookie. A browser without a session sees only one sentence that
// tells the user to run "email-mcp setup".
//
// The server listens on 127.0.0.1 on a random port. A stdio process starts it
// the first time it needs a link (EnsureStarted). The sessions are in the
// database, and a cookie is not bound to a port, so a session made by one
// process works in every later process.
//
// Every request goes through these checks, in this order (see guard.go):
//  1. The security headers are set on every answer.
//  2. Host: the Host header must be "127.0.0.1:<port>" or "localhost:<port>",
//     where <port> is the port of the connection. This stops DNS rebinding.
//     Refused with 403.
//  3. Origin: http.CrossOriginProtection refuses a cross-origin request that is
//     not GET, HEAD or OPTIONS. Refused with 403.
//  4. Session: every path except /setup needs a valid session cookie. Without
//     one, the answer is the no-session page with 401. /setup takes only GET
//     and HEAD, and the guard answers it itself, never through the routes:
//     any other method on /setup is refused with 405.
//  5. Form token: every request that is not GET or HEAD needs the CSRF token of
//     its session in the url-encoded form body. Refused with 403. HEAD is a
//     safe method like GET, and the routes answer it with their GET page, so
//     no route may change anything on GET. The one exception is the setup
//     link: GET and also HEAD on /setup use up its single-use token and set the
//     session cookie, so a HEAD request on the link signs the browser in like a
//     GET does. The link goes only to the browser of the user (NewSetupURL).
//
// The pages are plain HTML forms, with no JavaScript. The log gets the route
// pattern, the status and a fixed code, never a token, a cookie, a session id,
// a CSRF token, a path or a query string.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/discovery"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/httpserve"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// listenAddr is where the server listens: the IPv4 loopback address, on a port
// that the system picks.
const listenAddr = "127.0.0.1:0"

// The names of the pages. Each is a file in templates/ without ".html".
const (
	pageNoSession = "no_session"
	pageMessage   = "message"
)

// Fixed codes for the log.
const (
	codeServeFailed  = "serve_failed"
	codeRenderFailed = "render_failed"
)

// ErrClosed means that the server was shut down. It does not start again.
var ErrClosed = errors.New("web: the server is shut down")

//go:embed templates/*.html
var templateFS embed.FS

// Deps are the parts that a Server needs. Store and Signer are required.
type Deps struct {
	Store     *store.Store
	Signer    *signing.Signer
	Discovery *discovery.Finder
	Tokens    *tokens.Manager
	Audit     *audit.Log
	// Now returns the current time. A nil Now means time.Now.
	Now func() time.Time
	// Logger gets a line with a fixed code for each refused or failed request.
	// A nil Logger logs nothing.
	Logger *slog.Logger
}

// Server is the web interface. Make it with New. It is safe for use by many
// goroutines.
type Server struct {
	d   Deps
	now func() time.Time
	log *slog.Logger
	cop *http.CrossOriginProtection
	mux *http.ServeMux
	// layout is the parsed layout.html. Each page is a clone of it with the
	// page's own file added.
	layout *template.Template
	pages  map[string]*template.Template
	svc    atomic.Pointer[service.Service]
	// listen opens the listener of EnsureStarted. Tests replace it.
	listen func(ctx context.Context, network, address string) (net.Listener, error)

	// mu guards the fields below.
	mu      sync.Mutex
	baseURL string // empty until the server listens
	srv     *http.Server
	closed  bool
}

// New returns a server that does not listen yet. EnsureStarted starts it.
//
// It panics when the Store or the Signer is missing, or when an embedded
// template does not parse. All are mistakes in the program, not in its input.
func New(d Deps) *Server {
	if d.Store == nil || d.Signer == nil {
		panic("web: New needs a store and a signer")
	}
	s := &Server{
		d:   d,
		now: d.Now,
		log: d.Logger,
		cop: http.NewCrossOriginProtection(),
		mux: http.NewServeMux(),
	}
	var lc net.ListenConfig
	s.listen = lc.Listen
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	var err error
	if s.layout, s.pages, err = parsePages(); err != nil {
		panic(fmt.Sprintf("web: %v", err))
	}
	s.addRoutes()
	return s
}

// route is one page of the server: a pattern with its method, and the method
// of Server that answers it.
type route struct {
	pattern string
	handle  func(*Server, http.ResponseWriter, *http.Request)
}

// routes are the pages of the server. The checks of guard.go run before any of
// them. /setup is not here: the guard answers it (see serve).
//
// Every pattern has its method. A GET route only reads, and every change is a
// POST of a url-encoded form with the CSRF token. "GET /{$}" is the page "/"
// alone, not every path. The tests go through this list, so a new page is
// checked for both rules.
var routes = []route{
	{"GET /{$}", (*Server).handleAccounts},
	{"GET /connect", (*Server).handleConnect},
	{"POST /connect/discover", (*Server).handleDiscover},
	{"POST /connect/settings", (*Server).handleSettings},
	{"POST /connect/save", (*Server).handleSave},
	{"GET /accounts/{id}/settings", (*Server).handleAccountSettings},
	{"POST /accounts/{id}/settings", (*Server).handleSaveAccountSettings},
	{"POST /accounts/{id}/remove", (*Server).handleRemoveAccount},
	{"GET /approvals", (*Server).handleApprovals},
	{"GET /approvals/{id}", (*Server).handleApproval},
	{"POST /approvals/{id}/approve", (*Server).handleApprove},
	{"POST /approvals/{id}/reject", (*Server).handleReject},
	{"GET /activity", (*Server).handleActivity},
	{"GET /connections", (*Server).handleConnections},
	{"POST /connections", (*Server).handleCreateToken},
	{"POST /connections/{id}/revoke", (*Server).handleRevokeToken},
}

// addRoutes adds the routes to the mux.
func (s *Server) addRoutes() {
	for _, r := range routes {
		handle := r.handle
		s.mux.HandleFunc(r.pattern, func(w http.ResponseWriter, req *http.Request) { handle(s, w, req) })
	}
}

// Attach gives the server the service that the pages use. The service needs the
// Links of the server, so the program makes the server first, then the service,
// and then calls Attach.
func (s *Server) Attach(svc *service.Service) {
	s.svc.Store(svc)
}

// EnsureStarted starts the server if it does not run yet, and returns its base
// URL, such as "http://127.0.0.1:53123". It listens once per Server: every
// later call, also from another goroutine at the same time, gets the same URL.
//
// The URL always uses 127.0.0.1, never localhost. The session cookie is set for
// the host 127.0.0.1, and a browser would not send it to localhost.
//
// ctx limits only the start. The server runs until Shutdown. After Shutdown it
// returns ErrClosed. When the listen fails, it returns the error, and the next
// call tries again. When the server stops by itself (its listener fails), it is
// closed, and the next call starts a new listener, maybe on another port.
func (s *Server) EnsureStarted(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	if s.baseURL != "" {
		return s.baseURL, nil
	}

	ln, err := s.listen(ctx, "tcp", listenAddr)
	if err != nil {
		return "", fmt.Errorf("web: listen: %w", err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return "", fmt.Errorf("web: listen: unexpected address type %T", ln.Addr())
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(s.serve),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		// net/http writes some of its own errors to this logger, and such a text
		// can hold a part of a request. Each line becomes a fixed code.
		ErrorLog: httpserve.ErrorLog(s.log, "web server error"),
	}
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		s.log.Error("web server stopped", "code", codeServeFailed, "error_types", errtype.Chain(err))
		// The listener is gone. Forget this server, so the next EnsureStarted
		// starts a new listener and does not give out a dead address.
		s.mu.Lock()
		if s.srv == srv {
			s.srv = nil
			s.baseURL = ""
		}
		s.mu.Unlock()
		// Close it too. Shutdown no longer knows it, so without this its open
		// connections would keep running after Shutdown.
		_ = srv.Close()
	}()
	s.srv = srv
	s.baseURL = "http://127.0.0.1:" + strconv.Itoa(addr.Port)
	return s.baseURL, nil
}

// Shutdown stops the server: it stops listening, waits for the requests that
// run (until ctx ends), and closes the connections. The server does not start
// again; EnsureStarted then returns ErrClosed. A second call, or a call on a
// server that never started, does nothing.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	srv := s.srv
	s.mu.Unlock()

	if srv == nil {
		return nil
	}
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("web: shut down: %w", err)
	}
	return nil
}

// view is the data of every page template.
type view struct {
	// CSRFToken is the form token of the session, for the "csrf_field"
	// template. It is empty when the request has no session.
	CSRFToken string
	// Data is the data of the page itself.
	Data any
}

// render writes a page with the status. It runs the template into a buffer
// first, so a template error gives a clean 500 and not half a page. When the
// request has a session, the page gets its CSRF token.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	tmpl, ok := s.pages[page]
	if !ok {
		s.fail(w, r, codeRenderFailed, fmt.Errorf("web: no page %q", page))
		return
	}
	v := view{Data: data}
	if sess, ok := sessionFrom(r.Context()); ok {
		token, err := s.csrfToken(sess)
		if err != nil {
			s.fail(w, r, codeRenderFailed, err)
			return
		}
		v.CSRFToken = token
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.fail(w, r, codeRenderFailed, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// messagePage is the data of the message page: a title and one fixed text.
type messagePage struct {
	Title string
	Text  string
}

// message answers with the message page: a fixed title and a fixed text, and a
// link back to the accounts. It shows nothing of the request.
func (s *Server) message(w http.ResponseWriter, r *http.Request, status int, title, text string) {
	s.render(w, r, status, pageMessage, messagePage{Title: title, Text: text})
}

// fail answers 500 with a fixed text, and logs the route pattern, the code and
// the Go types of the error. It never logs the text of the error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, code string, err error) {
	s.log.Error("web request failed", "route", r.Pattern, "code", code,
		"status", http.StatusInternalServerError, "error_types", errtype.Chain(err))
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// parsePages parses layout.html, and then each other template file as a clone
// of the layout with that file added. The key of a page is its file name
// without ".html".
func parsePages() (*template.Template, map[string]*template.Template, error) {
	layout, err := template.ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, nil, fmt.Errorf("parse layout: %w", err)
	}
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, nil, fmt.Errorf("list templates: %w", err)
	}
	pages := make(map[string]*template.Template)
	for _, file := range files {
		name := strings.TrimSuffix(path.Base(file), ".html")
		if name == "layout" {
			continue
		}
		clone, err := layout.Clone()
		if err != nil {
			return nil, nil, fmt.Errorf("clone layout: %w", err)
		}
		if pages[name], err = clone.ParseFS(templateFS, file); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", file, err)
		}
	}
	return layout, pages, nil
}
