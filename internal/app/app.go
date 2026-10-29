// Package app builds the parts of the program and connects them: the store, the
// vault, the signer, the policy limiter, the confirm manager, the outbox, the
// audit log, the tokens, the backends, the web interface and the service. The
// commands of the binary open an App and serve it.
//
// An App also runs the background work of a long-running process (see Run) and
// stops it in an order that never sends a message twice and never loses one
// (see Close).
//
// The log of an App goes to standard error (see Config.LogOutput), never to
// standard output, which carries the MCP protocol on stdio. It has ids, counts
// and fixed codes only. It never has a secret, a setup URL, a token, an address,
// a subject or a body.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/confirm"
	"github.com/EmadMokhtar/email-mcp-go/internal/discovery"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/inflight"
	"github.com/EmadMokhtar/email-mcp-go/internal/mcpserver"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens"
	"github.com/EmadMokhtar/email-mcp-go/internal/version"
	"github.com/EmadMokhtar/email-mcp-go/internal/web"
)

// The environment variables that Config reads when a field is empty.
const (
	envDataDir = "EMAIL_MCP_DATA_DIR"
	envKEK     = "EMAIL_MCP_KEK"
)

// dataDirName is the name of the data directory inside the platform directory.
const dataDirName = "email-mcp"

// The times of the background work.
const (
	// outboxTick is how often the outbox worker looks for due items.
	outboxTick = time.Second
	// jobEvery is how often the daily jobs run. They also run when Run starts.
	jobEvery = 24 * time.Hour
	// pendingRetention is how long an ended pending action is kept.
	pendingRetention = 30 * 24 * time.Hour
	// auditRetention is how long an audit entry is kept.
	auditRetention = 90 * 24 * time.Hour
	// staleApprovedGrace is how long after the end of its approval time an
	// approved action with no result is expired. It is longer than a send can
	// run, so a program that still works on the action can finish it.
	staleApprovedGrace = outbox.SendDeadline + 2*time.Minute
	// backendIdle is how long a mail backend may go unused before the sweep
	// closes it. It is much longer than any use case runs with a backend.
	backendIdle = 30 * time.Minute
	// backendSweepEvery is how often the idle backends are swept.
	backendSweepEvery = 5 * time.Minute
	// drainWait is how long Close waits for queued messages whose undo window
	// ends soon. The longest undo window is 5 minutes.
	drainWait = 5 * time.Minute
	// slowStopNotice is how long Close runs before it logs that it waits.
	slowStopNotice = time.Second
)

// StopTimeout is the time budget of a whole stop of the program: give Close a
// context with this deadline, started when the stop starts.
//
// Within it, Close waits for the sends that run (a send has at most
// outbox.SendDeadline, 13 minutes, and the sends run at the same time), then
// sends the queued messages that are due within 5 minutes, as long as such a
// send can still end before the deadline. So a stop takes at most about 20
// minutes, and usually a few seconds. A send that has started is never cut off.
const StopTimeout = drainWait + outbox.SendDeadline + 2*time.Minute

// errStopping is the answer to a tool call that comes while the App stops.
var errStopping = errors.New("email-mcp is stopping and takes no new tool calls")

// Config is the setting of an App. Every field may be empty.
type Config struct {
	// DataDir is the directory of the database. Empty means $EMAIL_MCP_DATA_DIR,
	// or else the platform default: os.UserConfigDir()/email-mcp on macOS and
	// Windows, $XDG_DATA_HOME/email-mcp (or ~/.local/share/email-mcp) elsewhere.
	DataDir string
	// KEKSource says where the master key is: "keychain" or "file:<path>".
	// Empty means $EMAIL_MCP_KEK, or else "keychain".
	KEKSource string
	// Local is true for a local install. A mail server on a loopback address may
	// then be used without TLS.
	Local bool
	// LogOutput gets the log, as text. Nil means os.Stderr. It must never be
	// standard output: on stdio, standard output carries the protocol.
	LogOutput io.Writer
}

// resolve fills the empty fields from the environment and the defaults. It does
// not check the KEK source; loadKEK does.
func (c Config) resolve() (Config, error) {
	if c.DataDir == "" {
		c.DataDir = os.Getenv(envDataDir)
	}
	if c.DataDir == "" {
		dir, err := defaultDataDir()
		if err != nil {
			return c, err
		}
		c.DataDir = dir
	}
	if c.KEKSource == "" {
		c.KEKSource = os.Getenv(envKEK)
	}
	if c.KEKSource == "" {
		c.KEKSource = kekKeychain
	}
	return c, nil
}

// defaultDataDir returns the platform data directory of the program.
func defaultDataDir() (string, error) {
	switch runtime.GOOS {
	case "darwin", "windows":
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("app: find the data directory: %w (set %s)", err, envDataDir)
		}
		return filepath.Join(base, dataDirName), nil
	default:
		// The XDG rules say to ignore a path that is not absolute.
		if base := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(base) {
			return filepath.Join(base, dataDirName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("app: find the data directory: %w (set %s)", err, envDataDir)
		}
		return filepath.Join(home, ".local", "share", dataDirName), nil
	}
}

// backendFactory is the part of the backend cache that the App uses.
// *service.BackendCache implements it. Tests replace it.
type backendFactory interface {
	service.BackendFactory
	// SweepIdle closes the backends that nobody used for more than maxIdle.
	SweepIdle(maxIdle time.Duration) int
	// Close closes every backend.
	Close()
}

var _ backendFactory = (*service.BackendCache)(nil)

// App holds the parts of the program. Make it with Open, start its background
// work with Run, and stop it with Close.
type App struct {
	// Service runs the use cases.
	Service *service.Service
	// Outbox queues and sends the outgoing mail.
	Outbox *outbox.Outbox
	// Web is the local web interface. It starts when a link needs it.
	Web *web.Server
	// Tokens makes and checks the access tokens of local HTTP.
	Tokens *tokens.Manager

	log      *slog.Logger
	store    *store.Store
	vault    *secrets.Vault
	confirm  *confirm.Manager
	audit    *audit.Log
	backends backendFactory
	sender   outbox.Sender

	// The intervals of the background work. Tests make them shorter before Run.
	jobEvery   time.Duration
	sweepEvery time.Duration

	// calls counts the tool calls that run, so Close can wait for them.
	calls inflight.Tracker

	// mu guards started, stopping and runCancel.
	mu        sync.Mutex
	started   bool
	stopping  bool
	runCancel context.CancelFunc
	// running counts the goroutines that Run started.
	running sync.WaitGroup

	// stopOnce runs the work of BeginStop. The fields after it are set there.
	stopOnce   sync.Once
	callsIdle  <-chan struct{}
	actingIdle <-chan struct{}
	notice     *time.Timer

	closeOnce sync.Once
	closeErr  error
}

// options are the settings of open that only tests change.
type options struct {
	// backends replaces the backend cache.
	backends backendFactory
}

// Open builds an App. In this order, it:
//  1. sets the default logger of the program to a text log on Config.LogOutput;
//  2. opens the store in the data directory and makes sure the local tenant
//     exists;
//  3. finds the master key, or makes it on the first run (under a lock, see
//     loadKEK), and builds the vault;
//  4. builds the signer from the server key of the vault;
//  5. builds the limiter, the confirm manager, the backend factory, the outbox,
//     the audit log, the tokens and the discovery (the backend factory comes
//     before the outbox, because the sender of the outbox uses it);
//  6. builds the web server, then the service with the links of the web
//     server, and then attaches the service to the web server.
//
// Open does not start any background work. See Run.
func Open(ctx context.Context, cfg Config) (*App, error) {
	return open(ctx, cfg, options{})
}

// open is Open with the settings of tests.
func open(ctx context.Context, cfg Config, opts options) (_ *App, err error) {
	out := cfg.LogOutput
	if out == nil {
		out = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(out, nil))
	slog.SetDefault(log)

	cfg, err = cfg.resolve()
	if err != nil {
		return nil, err
	}
	if err := checkKEKSource(cfg.KEKSource); err != nil {
		return nil, err
	}

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = st.Close()
		}
	}()
	if _, err := st.EnsureLocalTenant(ctx); err != nil {
		return nil, err
	}

	kek, err := loadKEK(ctx, cfg.DataDir, cfg.KEKSource, st, log)
	if err != nil {
		return nil, err
	}
	vault := secrets.NewVault(st, kek)
	serverKey, err := vault.ServerKey(ctx)
	if err != nil {
		return nil, err
	}
	signer := signing.New(serverKey)
	clear(serverKey)

	limiter := &policy.Limiter{Store: st}
	confirmMgr := confirm.NewManager(st, vault, signer, nil)
	backends := opts.backends
	if backends == nil {
		backends = service.NewBackendFactory(st, vault, signer, cfg.Local, log)
	}
	sender := newSender(st, backends, log)
	ob := outbox.New(st, vault, sender, nil)
	ob.Logger = log
	auditLog := audit.New(st, vault, nil)
	tokenMgr := tokens.New(st, nil)
	finder := &discovery.Finder{}

	webServer := web.New(web.Deps{
		Store:     st,
		Signer:    signer,
		Discovery: finder,
		Tokens:    tokenMgr,
		Audit:     auditLog,
		Logger:    log,
	})
	svc := service.New(service.Deps{
		Store:    st,
		Vault:    vault,
		Signer:   signer,
		Backends: backends,
		Limiter:  limiter,
		Confirm:  confirmMgr,
		Outbox:   ob,
		Audit:    auditLog,
		Links:    webServer.Links(),
		Local:    cfg.Local,
		Logger:   log,
	})
	webServer.Attach(svc)

	return &App{
		Service:    svc,
		Outbox:     ob,
		Web:        webServer,
		Tokens:     tokenMgr,
		log:        log,
		store:      st,
		vault:      vault,
		confirm:    confirmMgr,
		audit:      auditLog,
		backends:   backends,
		sender:     sender,
		jobEvery:   jobEvery,
		sweepEvery: backendSweepEvery,
	}, nil
}

// Logger returns the logger of the App. It writes to Config.LogOutput.
func (a *App) Logger() *slog.Logger {
	return a.log
}

// MCPServer returns an MCP server with the tools of the program. An empty
// opts.Version becomes the version of the build, and a nil opts.Logger the
// logger of the App. (The mcpserver package keeps its logger for itself and
// does not give it to the SDK, whose logger stays the default that discards
// everything: the SDK logs error texts.)
//
// Close waits for the tool calls of the servers that this returns, and refuses
// new ones while it stops.
func (a *App) MCPServer(opts mcpserver.Options) *mcp.Server {
	if opts.Version == "" {
		opts.Version = version.Version
	}
	if opts.Logger == nil {
		opts.Logger = a.log
	}
	srv := mcpserver.New(a.Service, opts)
	srv.AddReceivingMiddleware(a.trackToolCalls)
	return srv
}

// trackToolCalls is a middleware of the MCP server. It counts the tool calls
// that run, so Close can wait for them: a send without an undo window runs
// inside its tool call. While the App stops, it refuses new tool calls with
// errStopping. Other methods are not counted.
func (a *App) trackToolCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		if !a.calls.Begin() {
			return nil, errStopping
		}
		defer a.calls.End()
		return next(ctx, method, req)
	}
}

// Run starts the background work of a long-running process. It does not wait
// for that work. Close stops it. Run does nothing after the first call, or
// after BeginStop.
//
// Before anything else, and before Run returns, it cleans up after a program
// that stopped in the middle of its work:
//   - Outbox.RecoverInterrupted marks every item that has been "sending" for
//     more than 15 minutes as failed ("delivery unknown"). Such an item is never
//     sent again: the server may have it.
//   - The approved actions with no result whose approval time ended more than
//     15 minutes ago are expired. Nothing was finished for them, and nothing
//     will be.
//
// Then it starts, in goroutines:
//   - the outbox worker (Outbox.Run, every second);
//   - the daily jobs, now (with a passive checkpoint) and then once a day (see
//     dailyJobs);
//   - the sweep of the idle mail backends, every 5 minutes.
//
// The work stops when ctx ends, or when Close stops it.
func (a *App) Run(ctx context.Context) {
	a.mu.Lock()
	if a.started || a.stopping {
		a.mu.Unlock()
		return
	}
	a.started = true
	runCtx, cancel := context.WithCancel(ctx)
	a.runCancel = cancel
	// Hold the count above zero until the goroutines are added, so Close cannot
	// finish its wait in between.
	a.running.Add(1)
	a.mu.Unlock()
	defer a.running.Done()

	// The recovery must be done before the worker takes any item.
	if n, err := a.Outbox.RecoverInterrupted(runCtx); err != nil {
		a.log.Error("app: could not look for interrupted sends", "code", "recover_failed", "error_types", errtype.Chain(err))
	} else if n > 0 {
		a.log.Warn("app: marked interrupted sends as failed (delivery unknown)", "count", n)
	}
	a.expireStaleApproved(runCtx)

	a.running.Add(3)
	go func() {
		defer a.running.Done()
		// Run returns ctx.Err() when it is stopped. Any other error is a bad tick,
		// which outboxTick is not.
		if err := a.Outbox.Run(runCtx, outboxTick); err != nil && runCtx.Err() == nil {
			a.log.Error("app: the outbox worker stopped", "code", "outbox_stopped", "error_types", errtype.Chain(err))
		}
	}()
	go func() {
		defer a.running.Done()
		// At the start, a passive checkpoint: a TRUNCATE checkpoint would hold up
		// the writes of the other programs that use the database, and every
		// program runs this when it starts.
		a.dailyJobs(runCtx, store.CheckpointPassive)
		repeat(runCtx, a.jobEvery, func(ctx context.Context) { a.dailyJobs(ctx, store.CheckpointTruncate) })
	}()
	go func() {
		defer a.running.Done()
		repeat(runCtx, a.sweepEvery, a.sweepBackends)
	}()
}

// repeat runs job every interval until ctx ends.
func repeat(ctx context.Context, every time.Duration, job func(context.Context)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job(ctx)
		}
	}
}

// dailyJobs is the housekeeping of the whole database, for all tenants:
//  1. expire the stale approved actions (see Run);
//  2. delete the ended pending actions older than 30 days;
//  3. delete the audit entries older than 90 days;
//  4. checkpoint the write-ahead log in the given mode. The daily run uses
//     store.CheckpointTruncate, which cuts the log: secure_delete does not
//     clear old frames of the log, so deleted data could stay there.
//
// A failed step is logged and the next steps still run.
func (a *App) dailyJobs(ctx context.Context, checkpoint store.CheckpointMode) {
	a.expireStaleApproved(ctx)
	pending, err := a.confirm.Prune(ctx, pendingRetention)
	if err != nil {
		a.logJobError(ctx, "prune_pending", err)
	}
	entries, err := a.audit.Prune(ctx, auditRetention)
	if err != nil {
		a.logJobError(ctx, "prune_audit", err)
	}
	complete, err := a.store.CheckpointWAL(ctx, checkpoint)
	if err != nil {
		a.logJobError(ctx, "checkpoint", err)
	}
	if ctx.Err() == nil {
		a.log.Info("app: daily jobs done", "pending_deleted", pending, "audit_deleted", entries,
			"checkpoint", strings.ToLower(string(checkpoint)), "checkpoint_complete", complete)
	}
}

// expireStaleApproved expires the approved actions that nothing will finish.
func (a *App) expireStaleApproved(ctx context.Context) {
	n, err := a.confirm.ExpireStaleApproved(ctx, staleApprovedGrace)
	if err != nil {
		a.logJobError(ctx, "expire_approved", err)
		return
	}
	if n > 0 {
		a.log.Warn("app: expired approved actions that were never finished", "count", n)
	}
}

// sweepBackends closes the mail backends that nobody used for a while.
func (a *App) sweepBackends(context.Context) {
	if n := a.backends.SweepIdle(backendIdle); n > 0 {
		a.log.Info("app: closed idle mail backends", "count", n)
	}
}

// logJobError logs a failed background job, unless the work is stopping.
func (a *App) logJobError(ctx context.Context, job string, err error) {
	if ctx.Err() != nil {
		return
	}
	a.log.Error("app: a background job failed", "job", job, "error_types", errtype.Chain(err))
}

// BeginStop starts the stop of the App and returns at once. From now on:
//   - the service refuses new sends, Trash moves and approvals
//     (service.ErrStopping); an action that waits for approval keeps waiting,
//     and undo (cancel_send) still works;
//   - new tool calls of the servers of MCPServer are refused;
//   - Run does nothing;
//   - if the stop takes longer than a second, one log line says so.
//
// Close calls it first. A command calls it earlier when it learns that it must
// stop (a signal), so that nothing new starts while it waits for its own server
// to stop. A second call does nothing.
func (a *App) BeginStop() {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.stopping = true
		a.mu.Unlock()
		a.actingIdle = a.Service.Stop()
		a.callsIdle = a.calls.Stop()
		a.notice = time.AfterFunc(slowStopNotice, func() {
			a.log.Warn("app: stopping. Waiting for sends in progress, and sending the queued messages that are due " +
				"within 5 minutes. This takes at most about 20 minutes")
		})
	})
}

// Close stops the App, in this order:
//  1. BeginStop: no new send, Trash move, approval or tool call starts.
//  2. It stops the background work of Run and waits for it. The outbox worker
//     finishes the send that it runs and writes its result.
//  3. It waits for the tool calls and the service actions that run (a send
//     without an undo window runs inside its tool call or its web approval).
//  4. Outbox.Drain for 5 minutes: it sends the queued messages that are due, or
//     that become due within 5 minutes. The web server is still up, so the user
//     can still undo a queued send. When ctx has a deadline, the drain starts no
//     send later than outbox.SendDeadline before it, so every send it starts
//     ends in time. When that time is over, the messages that are left stay
//     queued for the next start: the stop is still clean, and Close logs it.
//  5. It shuts down the web server.
//  6. It closes the mail backends and the store.
//
// Steps 2 and 3 do not stop at the end of ctx: a send that has started must
// finish and record its result, or it would end as "delivery unknown". Give ctx
// the deadline StopTimeout from the start of the stop. A second call does
// nothing and returns the result of the first.
func (a *App) Close(ctx context.Context) error {
	a.closeOnce.Do(func() { a.closeErr = a.close(ctx) })
	return a.closeErr
}

// close is the work of Close.
func (a *App) close(ctx context.Context) error {
	a.BeginStop()
	defer a.notice.Stop()

	a.mu.Lock()
	cancel := a.runCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.running.Wait()
	<-a.callsIdle
	<-a.actingIdle

	var errs []error
	if err := a.drain(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := a.Web.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	a.backends.Close()
	if err := a.store.Close(); err != nil {
		errs = append(errs, fmt.Errorf("app: close the store: %w", err))
	}
	return errors.Join(errs...)
}

// drain is step 4 of Close. The end of its time is a clean stop: it is logged,
// and it is no error.
func (a *App) drain(ctx context.Context) error {
	dctx, cancel := drainContext(ctx)
	defer cancel()
	err := a.Outbox.Drain(dctx, drainWait)
	switch {
	case err == nil:
		return nil
	case dctx.Err() != nil && errors.Is(err, dctx.Err()):
		a.log.Warn("app: the time to stop is over. Queued messages that are not sent yet stay queued, "+
			"and go out at the next start", "code", "drain_stopped")
		return nil
	default:
		return fmt.Errorf("app: drain the outbox: %w", err)
	}
}

// drainContext returns the context of the drain. When ctx has a deadline, the
// drain stops starting sends outbox.SendDeadline before it: a send that has
// started runs up to that long, and must end before the deadline.
func drainContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(ctx, deadline.Add(-outbox.SendDeadline))
	}
	return context.WithCancel(ctx)
}
