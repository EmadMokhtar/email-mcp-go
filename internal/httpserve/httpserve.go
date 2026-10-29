// Package httpserve serves the MCP server over local, stateless Streamable HTTP.
//
// The server is for programs on the same computer. Each request goes through
// these checks, in this order:
//  1. Host: the Host header must be localhost, 127.0.0.1 or [::1] with the port
//     that the connection came in on, and the connection must have come in on a
//     loopback address. This stops a web page that uses DNS rebinding (a name of
//     the attacker that points to 127.0.0.1). Refused with 403.
//  2. Origin: a request with an Origin header comes from a browser. It is
//     refused with 403 unless its origin is in the allow-list, which is empty by
//     default. A request without Origin (a program, not a browser) passes.
//  3. Body size: a body of more than 15 MB is refused with 413.
//  4. Bearer token: the Authorization header must carry a valid token of the
//     tokens package. Refused with 401.
//
// So a page that uses DNS rebinding gets 403 before any token is looked at.
// Only then does the request reach the MCP handler of the SDK, in stateless
// mode: the server keeps no session between requests.
//
// Answers to refused requests have fixed texts. They never repeat a value of the
// request. The log gets a fixed code for each refusal. It never gets a token,
// the Authorization header or a request body.
package httpserve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
)

// MaxBodyBytes is the largest request body: 15 MB. A message may have 10 MB of
// attachments, and base64 makes them a third larger.
const MaxBodyBytes = 15 << 20

// The codes of refused requests, for the log.
const (
	codeBadHost      = "bad_host"
	codeBadOrigin    = "bad_origin"
	codeBodyTooLarge = "body_too_large"
	codeBadBody      = "body_unreadable"
	codeInvalidToken = "invalid_token"
	codeTokenCheck   = "token_check_failed"
)

// ErrNoToken means that a tool call came without a verified token. Principal
// returns it.
var ErrNoToken = errors.New("httpserve: the request has no verified token")

// errTokenCheck is the error for a token check that failed for another reason
// than a bad token, such as a database problem. The SDK writes its text to the
// client with status 500, so the text is fixed.
var errTokenCheck = errors.New("internal error")

// Option changes a setting of Handler.
type Option func(*config)

// config holds the settings of Handler.
type config struct {
	log *slog.Logger
}

// WithLogger sets the logger. It gets one line for each refused request and for
// a failed token check, with a fixed code. Without this option nothing is
// logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.log = l
		}
	}
}

// handler runs the Host, Origin and body size checks, and then next.
type handler struct {
	origins map[string]bool
	log     *slog.Logger
	next    http.Handler
}

// Handler returns the HTTP handler of the MCP server. See the package
// documentation for the checks and their order.
//
// newServer returns the MCP server for a request. The SDK may call it more than
// once for one request, so it should return a server that already exists. tm
// checks the tokens. allowedOrigins lists the browser origins that may call
// the server, written exactly as a browser sends them, such as
// "http://localhost:5173". It is usually empty.
//
// The port in the Host header is compared with the port of the connection's
// local address, which net/http puts in the request context
// (http.LocalAddrContextKey). So the handler needs no port setting, and it
// refuses every request that did not come through an http.Server on a
// loopback address.
//
// The handler sends no CORS headers. A browser page from an allowed origin is
// not refused, but the browser may still hide the answers from it.
//
// It panics when newServer or tm is nil: both are mistakes in the code that
// sets the program up.
func Handler(newServer func() *mcp.Server, tm *tokens.Manager, allowedOrigins []string, opts ...Option) http.Handler {
	if newServer == nil {
		panic("httpserve: Handler needs newServer")
	}
	if tm == nil {
		panic("httpserve: Handler needs a token manager")
	}
	cfg := config{log: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&cfg)
	}
	origins := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			origins[o] = true
		}
	}

	// The logger of the SDK is not set: the SDK writes texts of errors to it,
	// and this package logs only fixed codes. The SDK refuses a body over 4 MB
	// unless it is told the larger limit.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return newServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: MaxBodyBytes})
	bearer := auth.RequireBearerToken(verifier(tm, cfg.log), &auth.RequireBearerTokenOptions{
		// The tokens of this program do not expire. They stay valid until the user
		// revokes them.
		AllowMissingExpiration: true,
	})
	return &handler{
		origins: origins,
		log:     cfg.log,
		next:    bearer(readBody(cfg.log, mcpHandler)),
	}
}

// ServeHTTP runs the Host, Origin and body size checks, in this order, and then
// the token check and the MCP handler.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !hostAllowed(r) {
		refuse(w, h.log, http.StatusForbidden, codeBadHost)
		return
	}
	if !h.originAllowed(r) {
		refuse(w, h.log, http.StatusForbidden, codeBadOrigin)
		return
	}
	// A body that says it is too large is refused at once, without reading it.
	// Any other body is cut at the limit while it is read (see readBody).
	if r.ContentLength > MaxBodyBytes {
		refuse(w, h.log, http.StatusRequestEntityTooLarge, codeBodyTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	h.next.ServeHTTP(w, r)
}

// hostAllowed reports whether the request came in on a loopback address and
// its Host header names that server: localhost, 127.0.0.1 or [::1], with the
// port of the connection. Names and ports are compared exactly, so
// "LOCALHOST", "localhost." and a port with a leading zero are refused.
func hostAllowed(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return false
	}
	localAddr, err := netip.ParseAddrPort(local.String())
	if err != nil || !isLoopback(localAddr.Addr()) {
		return false
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		return false
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
	default:
		return false
	}
	return port == strconv.Itoa(int(localAddr.Port()))
}

// originAllowed reports whether the request has no Origin header, or exactly one
// whose value is in the allow-list. An empty value or "null" counts as a value,
// so it is refused unless it is listed.
func (h *handler) originAllowed(r *http.Request) bool {
	values := r.Header.Values("Origin")
	switch len(values) {
	case 0:
		return true
	case 1:
		return h.origins[values[0]]
	default:
		return false
	}
}

// readBody reads the whole body before next runs. A body over the limit of
// http.MaxBytesReader is refused with 413, so a request that is too large never
// reaches the MCP handler, also when its length was not given in advance. It
// runs after the token check, so a caller without a token cannot make the
// server read a large body.
func readBody(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				refuse(w, log, http.StatusRequestEntityTooLarge, codeBodyTooLarge)
				return
			}
			refuse(w, log, http.StatusBadRequest, codeBadBody)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// refuse logs the code and writes the status with its standard text, such as
// "Forbidden". The answer repeats nothing of the request.
func refuse(w http.ResponseWriter, log *slog.Logger, status int, code string) {
	log.Warn("http request refused", "code", code, "status", status)
	http.Error(w, http.StatusText(status), status)
}

// verifier returns the token check for auth.RequireBearerToken. A valid token
// becomes a TokenInfo with the tenant as UserID and the scopes of the token. A
// bad token gives auth.ErrInvalidToken, which the SDK answers with 401. Any
// other failure gives errTokenCheck, which the SDK answers with 500 and the
// fixed text "internal error".
func verifier(tm *tokens.Manager, log *slog.Logger) auth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		p, err := tm.Verify(ctx, token)
		switch {
		case err == nil:
			return &auth.TokenInfo{UserID: string(p.Tenant), Scopes: p.Scopes}, nil
		case errors.Is(err, tokens.ErrInvalidToken):
			log.Warn("http request refused", "code", codeInvalidToken, "status", http.StatusUnauthorized)
			return nil, auth.ErrInvalidToken
		default:
			log.Error("token check failed", "code", codeTokenCheck, "error_types", errorTypes(err))
			return nil, errTokenCheck
		}
	}
}

// Principal finds the caller of a tool call that came over HTTP. It is the
// mcpserver.Options.Principal of the HTTP server.
//
// It reads the TokenInfo that the token check put on the request: the UserID is
// the tenant and the scopes are the scopes of the token. A call without a
// TokenInfo or without a UserID is refused with ErrNoToken. The scopes are a new
// list that is never nil, so a token can never mean "no limit". The client name
// comes from the request. It is not trusted: the audit log cleans it.
func Principal(_ context.Context, req *mcp.CallToolRequest) (service.Principal, error) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return service.Principal{}, ErrNoToken
	}
	info := req.Extra.TokenInfo
	var client string
	if impl := req.ClientInfo(); impl != nil {
		client = impl.Name
	}
	return service.Principal{
		Tenant: account.TenantID(info.UserID),
		Scopes: append(policy.Scopes{}, info.Scopes...),
		Client: client,
	}, nil
}

// CheckLoopbackAddr returns an error unless addr is a loopback address with a
// port: an IPv4 address in 127.0.0.0/8, [::1] or localhost, then a port from 0
// to 65535. The HTTP server must not listen on any other address, such as
// 0.0.0.0, so "serve --http" calls this before it listens.
func CheckLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("httpserve: the address %q must be host:port", addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("httpserve: the port of %q must be a number from 0 to 65535", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !isLoopback(ip) {
		return fmt.Errorf("httpserve: %q is not a loopback address: the HTTP server listens only on "+
			"127.0.0.1, [::1] or localhost", addr)
	}
	return nil
}

// isLoopback reports whether ip is an IPv4 address in 127.0.0.0/8, or ::1
// without a zone. An IPv4 address inside an IPv6 address is not accepted.
func isLoopback(ip netip.Addr) bool {
	return (ip.Is4() && ip.IsLoopback()) || ip == netip.IPv6Loopback()
}

// errorTypes returns the Go types of an error and of the errors it wraps, for
// example "*fmt.wrapError>*errors.errorString". It is for the log. A type name
// holds no text from the error itself.
func errorTypes(err error) string {
	const maxDepth = 6
	var types []string
	for i := 0; err != nil && i < maxDepth; i++ {
		types = append(types, fmt.Sprintf("%T", err))
		err = errors.Unwrap(err)
	}
	return strings.Join(types, ">")
}
