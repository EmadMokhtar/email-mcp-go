package main

// This file holds the "serve" command. It serves the MCP server over stdio (the
// default), or over local HTTP with --http.
//
// stdio: standard output carries only MCP protocol bytes. The log goes to
// standard error. Every tool call runs as the local principal (the local tenant,
// no scope limit), and a client of an older protocol version may be asked for
// a confirmation in band.
//
// HTTP: the MCP endpoint is http://<address>/mcp, for example
// http://127.0.0.1:8765/mcp. The address must be a loopback address. Every
// request needs a bearer token ("email-mcp token create"), and runs with the
// tenant and the scopes of that token. The server is stateless, so it never
// asks a client in band with elicitation/create: such a client gets the
// approval link instead.
//
// On SIGINT or SIGTERM the command stops in the order of app.App.Close and
// exits with 0. That waits for sends in progress, which can take up to about 15
// minutes. A second signal ends the program at once (see main).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/httpserve"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// mcpPath is the path of the MCP endpoint of "serve --http".
const mcpPath = "/mcp"

// The limits of the HTTP server of "serve --http".
const (
	httpReadHeaderTimeout = 10 * time.Second
	// httpReadTimeout covers the whole request, with a body of at most 15 MB
	// that comes over the loopback interface.
	httpReadTimeout = time.Minute
	// httpWriteTimeout counts from the end of the request headers. A send without
	// an undo window runs inside its request for up to 13 minutes (the deadline
	// that the outbox gives a send), and the answer must still go out after it.
	httpWriteTimeout = 15 * time.Minute
	httpIdleTimeout  = 2 * time.Minute
	// httpMaxHeaderBytes is the most bytes of request headers.
	httpMaxHeaderBytes = 64 << 10
	// maxConcurrentRequests is how many requests may run at once. A request can
	// hold its body (up to 15 MB) twice: httpserve reads it once and the SDK
	// reads it again. Eight requests hold at most about 240 MB.
	maxConcurrentRequests = 8
	// requestQueueWait is how long a request over the limit waits for a free
	// place. After that it gets 503.
	requestQueueWait = 30 * time.Second
)

// stopTimeout limits the steps of a stop that can be cut short: the wait of the
// HTTP server for its requests, and the drain of the outbox and the shutdown of
// the web server in app.App.Close. A send in progress can take up to 13
// minutes.
const stopTimeout = 15 * time.Minute

// Fixed codes for the log.
const (
	codeTooManyRequests = "too_many_requests"
	codeHTTPServerError = "http_server_error"
)

// originList is the value of the repeatable flag --allow-origin.
type originList []string

func (o *originList) String() string { return strings.Join(*o, ",") }

func (o *originList) Set(v string) error {
	*o = append(*o, v)
	return nil
}

func runServe(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "the data directory (default: $EMAIL_MCP_DATA_DIR, or the data directory of the platform)")
	httpAddr := fs.String("http", "", "serve MCP over local HTTP on this loopback address, such as 127.0.0.1:8765, instead of stdio")
	var origins originList
	fs.Var(&origins, "allow-origin", "a browser origin that may call the HTTP server, such as http://localhost:5173 (repeat the flag for more)")
	if err := fs.Parse(args); err != nil {
		// The flag package has already printed the problem or the help text.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "serve: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	// The settings are checked before anything is opened.
	allowed, err := checkServeFlags(*httpAddr, origins)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitUsage
	}

	a, err := app.Open(ctx, app.Config{DataDir: *dataDir, Local: true, LogOutput: stderr})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
		return exitError
	}
	a.Run(ctx)

	var serveErr error
	if *httpAddr != "" {
		serveErr = listenAndServeHTTP(ctx, a, *httpAddr, allowed)
	} else {
		serveErr = serveStdio(ctx, a, stdin, stdout)
	}

	// The context of the command may have ended (on a signal), so the stop gets a
	// context of its own.
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	closeErr := a.Close(stopCtx)

	code := exitOK
	for _, err := range []error{serveErr, closeErr} {
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
			code = exitError
		}
	}
	return code
}

// checkServeFlags checks --http and --allow-origin. It returns the origins in
// their normal form, or an error that says what is wrong. httpserve.Handler
// panics on a bad origin, so a bad one must be refused here.
func checkServeFlags(httpAddr string, origins []string) ([]string, error) {
	if httpAddr == "" {
		if len(origins) > 0 {
			return nil, errors.New("--allow-origin needs --http: browser origins apply only to the HTTP server")
		}
		return nil, nil
	}
	if err := httpserve.CheckLoopbackAddr(httpAddr); err != nil {
		return nil, err
	}
	allowed := make([]string, 0, len(origins))
	for _, o := range origins {
		origin, err := httpserve.NormalizeOrigin(o)
		if err != nil {
			return nil, fmt.Errorf("--allow-origin: %w", err)
		}
		allowed = append(allowed, origin)
	}
	return allowed, nil
}

// serveStdio serves MCP on stdin and stdout until the client leaves (stdin
// ends) or ctx ends. It returns when the tool calls that run have finished.
func serveStdio(ctx context.Context, a *app.App, stdin io.Reader, stdout io.Writer) error {
	srv := a.MCPServer(mcpserver.Options{InBandLegacyElicitation: true, Principal: localPrincipal})
	transport := &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}}
	err := srv.Run(ctx, transport)
	if ctx.Err() != nil {
		// A signal stopped the server. That is a clean stop.
		return nil
	}
	if err != nil {
		return fmt.Errorf("the MCP session ended with an error: %w", err)
	}
	return nil
}

// localPrincipal is the caller of every tool call on stdio: the local tenant,
// with no scope limit (a local install has no tokens on stdio), and the name
// that the client gave. It is never used for HTTP, where the token decides.
func localPrincipal(_ context.Context, req *mcp.CallToolRequest) (service.Principal, error) {
	var client string
	if req != nil {
		if info := req.ClientInfo(); info != nil {
			client = info.Name
		}
	}
	return service.Principal{Tenant: store.LocalTenantID, Client: client}, nil
}

// nopWriteCloser is an io.WriteCloser whose Close does nothing. The transport
// closes its writer when the session ends, and standard output must stay open.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// listenAndServeHTTP listens on addr and serves MCP over HTTP until ctx ends.
// The caller has checked that addr is a loopback address.
func listenAndServeHTTP(ctx context.Context, a *app.App, addr string, origins []string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	return serveHTTP(ctx, a, ln, origins)
}

// serveHTTP serves MCP over stateless HTTP on ln, at mcpPath, until ctx ends.
// Then it stops taking connections and waits for the requests that run, for
// at most stopTimeout. It closes ln.
func serveHTTP(ctx context.Context, a *app.App, ln net.Listener, origins []string) error {
	logger := a.Logger()
	// One server for all requests. The SDK asks for the server more than once per
	// request. Over HTTP, the caller is the token of the request.
	srv := a.MCPServer(mcpserver.Options{InBandLegacyElicitation: false, Principal: httpserve.Principal})
	mux := http.NewServeMux()
	mux.Handle(mcpPath, httpserve.Handler(func() *mcp.Server { return srv }, a.Tokens, origins, httpserve.WithLogger(logger)))
	hs := newHTTPServer(limitRequests(mux, maxConcurrentRequests, requestQueueWait, logger), logger)

	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()
	logger.Info("serving MCP over local HTTP", "address", ln.Addr().String(), "path", mcpPath)

	select {
	case err := <-served:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	err := hs.Shutdown(stopCtx)
	if err != nil {
		// The requests did not finish in time. Closing the connections ends their
		// contexts. A send that has started still finishes in the outbox.
		_ = hs.Close()
	}
	<-served
	if err != nil {
		return fmt.Errorf("stop the HTTP server: %w", err)
	}
	return nil
}

// newHTTPServer returns the HTTP server of "serve --http", with the time and
// size limits.
func newHTTPServer(h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
		// net/http writes some of its own errors to this logger, and such a text
		// can hold a part of a request. Each line becomes a fixed code.
		ErrorLog: log.New(fixedCodeWriter{log: logger}, "", 0),
	}
}

// fixedCodeWriter is the writer of the error log of the HTTP server. It logs a
// fixed code for each line and drops the text.
type fixedCodeWriter struct{ log *slog.Logger }

func (w fixedCodeWriter) Write(p []byte) (int, error) {
	w.log.Warn("http server error", "code", codeHTTPServerError)
	return len(p), nil
}

// limitRequests lets at most n requests run at once. A request over the limit
// waits up to wait for a free place, and then gets 503 with Retry-After. It
// waits before the body is read, so a waiting request holds no body.
func limitRequests(next http.Handler, n int, wait time.Duration, logger *slog.Logger) http.Handler {
	places := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case places <- struct{}{}:
		case <-timer.C:
			logger.Warn("http request refused", "code", codeTooManyRequests, "status", http.StatusServiceUnavailable)
			w.Header().Set("Retry-After", "1")
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		case <-r.Context().Done():
			return
		}
		defer func() { <-places }()
		next.ServeHTTP(w, r)
	})
}
