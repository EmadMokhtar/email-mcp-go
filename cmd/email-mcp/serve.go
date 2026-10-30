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
// On SIGINT or SIGTERM (or, on stdio, when the client closes standard input)
// the command stops in the order of app.App.Close and exits with 0. The whole
// stop has one time budget, app.StopTimeout: it waits for sends in progress
// and sends the queued messages that are due within 5 minutes, so it takes at
// most about 20 minutes, and usually a few seconds. Queued messages that are
// not sent in that time stay queued for the next start, and that is still a
// clean stop. A second signal ends the program at once (see main).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/httpserve"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
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
	httpIdleTimeout = 2 * time.Minute
	// httpMaxHeaderBytes is the most bytes of request headers.
	httpMaxHeaderBytes = 64 << 10
	// maxConcurrentRequests is how many requests may run at once. A request can
	// hold its body (up to 15 MB) twice: httpserve reads it once and the SDK
	// reads it again. Eight requests hold at most about 240 MB.
	maxConcurrentRequests = 8
	// requestQueueWait is how long a request over the limit waits for a free
	// place. After that it gets 503.
	requestQueueWait = 30 * time.Second
	// httpPrepareMargin is the time that a tool call may need before its send:
	// the checks, and the calls to the mail server that a reply or a forward
	// makes (each one has a time limit of 60 seconds).
	httpPrepareMargin = 5 * time.Minute
	// httpWriteTimeout is the time to write the answer, counted from the end of
	// the request headers. Before the answer, the request may wait for a place,
	// read its body, prepare the message, and send it without an undo window,
	// for up to outbox.SendDeadline. A client that never gets the answer of a
	// send that went out could send it again, so the answer must still go out.
	httpWriteTimeout = requestQueueWait + httpReadTimeout + outbox.SendDeadline + httpPrepareMargin
)

// codeTooManyRequests is the log code of a request that limitRequests refused.
const codeTooManyRequests = "too_many_requests"

// stopBudget is the one time budget of the whole stop of the command,
// app.StopTimeout. It starts when the stop starts: at the first call of
// context. Every part of the stop (the HTTP server, then app.App.Close) gets the
// same deadline, so the parts do not add up.
type stopBudget struct {
	parent context.Context
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

// newStopBudget returns the budget of a stop. parent gives the values of the
// context. Its end (a signal) does not end the budget.
func newStopBudget(parent context.Context) *stopBudget {
	return &stopBudget{parent: parent}
}

// context returns the context of the stop. The first call starts the budget.
func (b *stopBudget) context() context.Context {
	b.once.Do(func() {
		b.ctx, b.cancel = context.WithTimeout(context.WithoutCancel(b.parent), app.StopTimeout)
	})
	return b.ctx
}

// release frees the timer of the budget. The caller calls it when the stop is
// done.
func (b *stopBudget) release() {
	b.context()
	b.cancel()
}

// originList is the value of the repeatable flag --allow-origin.
type originList []string

func (o *originList) String() string { return strings.Join(*o, ",") }

func (o *originList) Set(v string) error {
	*o = append(*o, v)
	return nil
}

func runServe(ctx context.Context, c *cli, args []string) int {
	stdin, stdout, stderr := c.stdin, c.stdout, c.stderr
	fs := c.newFlags("serve")
	dataDir := fs.String("data-dir", "", "the data directory (default: $EMAIL_MCP_DATA_DIR, or the data directory of the platform)")
	httpAddr := fs.String("http", "", "serve MCP over local HTTP on this loopback address, such as 127.0.0.1:8765, instead of stdio")
	var origins originList
	fs.Var(&origins, "allow-origin", "a browser origin that may call the HTTP server, such as http://localhost:5173 (repeat the flag for more)")
	// A bad flag gets a fixed text, never the text that was typed (see
	// parseOnce). The help goes to standard error.
	if code, ok := c.parseOnce("serve", fs, args, stderr); !ok {
		return code
	}
	if fs.NArg() > 0 {
		return c.unexpected("serve")
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

	budget := newStopBudget(ctx)
	defer budget.release()
	// A signal starts the stop: nothing new starts, and the budget runs from now,
	// also while the server waits for the calls that run.
	stopOnSignal := context.AfterFunc(ctx, func() {
		a.BeginStop()
		budget.context()
	})
	defer stopOnSignal()
	var serveErr error
	if *httpAddr != "" {
		serveErr = listenAndServeHTTP(ctx, a, *httpAddr, allowed, budget)
	} else {
		serveErr = serveStdio(ctx, a, stdin, stdout)
	}
	// The budget started at the signal, or it starts now (the stdio client left).
	closeErr := a.Close(budget.context())

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
func listenAndServeHTTP(ctx context.Context, a *app.App, addr string, origins []string, budget *stopBudget) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	return serveHTTP(ctx, a, ln, origins, budget)
}

// serveHTTP serves MCP over stateless HTTP on ln, at mcpPath, until ctx ends.
// Then it starts the stop of the App (no new send, approval or tool call
// starts), stops taking connections, and waits for the requests that run,
// within the stop budget. It closes ln.
func serveHTTP(ctx context.Context, a *app.App, ln net.Listener, origins []string, budget *stopBudget) error {
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
	a.BeginStop()
	err := hs.Shutdown(budget.context())
	if err != nil {
		// The requests did not finish in time. Closing the connections ends their
		// contexts. A send that has started still finishes in the outbox.
		_ = hs.Close()
	}
	serveErr := <-served
	if err != nil {
		return fmt.Errorf("stop the HTTP server: %w", err)
	}
	// After Shutdown, Serve returns http.ErrServerClosed. Any other error is a
	// real failure of the server.
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", serveErr)
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
		ErrorLog: httpserve.ErrorLog(logger, "http server error"),
	}
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
