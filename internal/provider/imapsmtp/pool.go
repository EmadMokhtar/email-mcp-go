package imapsmtp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// errPoolClosed is returned by a pool that was closed.
var errPoolClosed = errors.New("imap: pool is closed")

// abandonGrace is how long run waits for a call to return after it closed the
// connection under that call. A call that uses the connection correctly
// returns at once, because every command fails when the connection closes. A
// call that does not return in this time is stuck (see the rule on with), and
// run stops waiting for it.
const abandonGrace = 500 * time.Millisecond

// pooledConn is a logged-in connection and the network connection under it.
//
// The pool never calls Client.Close or Client.Logout. Both wait for the
// reader of the client, and that reader can wait for a call that never
// finishes reading a FETCH. Closing the network connection cannot wait.
type pooledConn struct {
	client *imapclient.Client
	raw    net.Conn
}

// forceClose closes the network connection at once. The client is then
// useless and the connection must not be used again. It is safe to call more
// than once.
func (pc *pooledConn) forceClose() { _ = pc.raw.Close() }

// pool keeps a few logged-in IMAP connections for one account.
//
// A call takes one slot. Only PoolSize calls can run at the same time, and
// the others wait. A call uses a connection that no other call uses, so the
// function can send commands without taking turns. When the call ends, the
// connection goes back to the idle list, unless it is no longer healthy.
//
// A pool is safe for use by many goroutines.
type pool struct {
	cfg Config
	// now is the clock. Tests replace it to test the idle timeout.
	now func() time.Time
	// slots has one place for each call that may run. A call holds one place
	// while it uses a connection. This also limits the number of connections:
	// a call dials only when there is no idle connection, so the connections
	// in use plus the idle ones never exceed the number of places.
	slots chan struct{}
	// done is closed by Close, to stop calls that wait for a slot.
	done chan struct{}

	mu     sync.Mutex
	idle   []idleConn // oldest first
	closed bool
}

// idleConn is a connection in the idle list.
type idleConn struct {
	conn  *pooledConn
	since time.Time
}

// newPool returns a pool. It applies the defaults to cfg. It does not connect
// until the first call.
func newPool(cfg Config) *pool {
	cfg.withDefaults()
	return &pool{
		cfg:   cfg,
		now:   time.Now,
		slots: make(chan struct{}, cfg.PoolSize),
		done:  make(chan struct{}),
	}
}

// with runs fn with a connection that only fn uses.
//
// If fn fails because the connection failed, with closes that connection,
// dials a new one and runs fn once more. So fn must be safe to run twice. The
// connection failed when the error is a network error (net.Error, io.EOF,
// io.ErrUnexpectedEOF, a closed connection), or when the connection has ended
// whatever the error says: imapclient hides the network error of some
// failures, such as a reset by the server, so the state of the connection
// decides. An error from the server (NO or BAD) is returned as it is and is
// never retried. A failed dial is not retried either.
//
// fn must Wait or Close every command it starts before it returns (for
// example, defer cmd.Close() for a FETCH). imapclient stops reading the
// connection while a FETCH body is unread, so a command that is left open
// blocks every later command on that connection. with does not hang on such a
// connection: it closes it when the command timeout passes. But the call that
// forgot to close the command is the one that loses its connection.
//
// fn must not keep the client after it returns. The mailbox that fn selected
// can still be selected on the next call, so every fn must select what it
// needs.
//
// fn runs in its own goroutine, and with returns the panic of fn to its
// caller. with ends the call when ctx ends, or when fn runs longer than
// CommandTimeout. In both cases it closes the connection so that fn stops,
// and it does not retry. The error then wraps ctx's error, or
// context.DeadlineExceeded for the timeout.
func (p *pool) with(ctx context.Context, fn func(c *imapclient.Client) error) error {
	return p.do(ctx, fn, true)
}

// withNoRetry is like with, but it never runs fn a second time. Use it for a
// command that is not safe to repeat, such as APPEND: after a dropped
// connection, the server may have stored the message already.
func (p *pool) withNoRetry(ctx context.Context, fn func(c *imapclient.Client) error) error {
	return p.do(ctx, fn, false)
}

func (p *pool) do(ctx context.Context, fn func(c *imapclient.Client) error, retry bool) error {
	if err := p.acquire(ctx); err != nil {
		return err
	}
	// The connection goes back to the idle list before this runs, so a call
	// that waits for the slot finds the connection.
	defer p.release()

	pc, err := p.takeOrDial(ctx)
	if err != nil {
		return err
	}
	res := p.run(ctx, pc, fn)
	p.finish(pc, res.reusable)
	if !retry || !res.connectionFailed || ctx.Err() != nil {
		return res.err
	}

	pc, err = p.dial(ctx)
	if err != nil {
		return err
	}
	res = p.run(ctx, pc, fn)
	p.finish(pc, res.reusable)
	return res.err
}

// runResult is what happened in one run.
type runResult struct {
	err error
	// reusable is true when the connection can serve another call.
	reusable bool
	// connectionFailed is true when err comes from a failed connection, so a
	// new connection may work. It is false for an answer from the server, and
	// for a call that ended because of ctx or the command timeout.
	connectionFailed bool
}

// callOutcome is what a call of fn did.
type callOutcome struct {
	err      error
	panicked bool
	panicVal any
}

// run calls fn on pc, in its own goroutine, and waits for it. When ctx ends or
// CommandTimeout passes first, run closes the connection and gives fn a short
// time to return. It stops waiting for a stuck fn.
func (p *pool) run(ctx context.Context, pc *pooledConn, fn func(c *imapclient.Client) error) runResult {
	// imapclient sets and clears the read and write deadlines of the
	// connection itself, so a deadline set from here would not last.
	// Closing the connection is the way to stop a command.
	opCtx, cancel := context.WithTimeout(ctx, p.cfg.CommandTimeout)
	defer cancel()

	// The channel has room for the result, so a call that returns after run
	// stopped waiting does not block forever.
	done := make(chan callOutcome, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- callOutcome{panicked: true, panicVal: fmt.Sprintf("%v\n%s", v, debug.Stack())}
			}
		}()
		done <- callOutcome{err: fn(pc.client)}
	}()

	select {
	case out := <-done:
		return p.classify(pc, out)
	case <-opCtx.Done():
	}

	// The call is still running, and it ran too long, or ctx ended. Closing the
	// connection makes every command of the call fail, unless the call is stuck.
	pc.forceClose()
	timer := time.NewTimer(abandonGrace)
	defer timer.Stop()
	select {
	case out := <-done:
		if out.panicked {
			panic(out.panicVal)
		}
		if out.err == nil {
			// The call finished just as time ran out. Its result is valid.
			return runResult{}
		}
		if isServerError(out.err) {
			return runResult{err: out.err}
		}
	case <-timer.C:
	}
	return runResult{err: p.interrupted(ctx)}
}

// classify turns the outcome of a call that ended by itself into a result.
func (p *pool) classify(pc *pooledConn, out callOutcome) runResult {
	if out.panicked {
		pc.forceClose()
		// A panic of fn goes on to the caller of with, as in a normal call.
		panic(out.panicVal)
	}
	failed := connectionFailed(pc, out.err)
	return runResult{
		err:              out.err,
		reusable:         !failed && !isDead(pc.client),
		connectionFailed: failed,
	}
}

// interrupted returns the error for a call that was stopped by ctx or by the
// command timeout.
func (p *pool) interrupted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("imap: command stopped: %w", err)
	}
	return fmt.Errorf("imap: command took longer than %s: %w", p.cfg.CommandTimeout, context.DeadlineExceeded)
}

// isServerError reports whether err holds an answer of the server (NO, BAD).
func isServerError(err error) bool {
	var serverErr *imap.Error
	return errors.As(err, &serverErr)
}

// connectionFailed reports whether err means that the connection failed, as
// opposed to the server saying no to a command. An error that is not an
// answer of the server counts when it has a network error type, or when the
// connection has ended.
func connectionFailed(pc *pooledConn, err error) bool {
	if err == nil {
		return false
	}
	if isServerError(err) {
		return false
	}
	return isNetworkError(err) || isDead(pc.client)
}

// isNetworkError reports whether the type of err is a network failure.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// context.DeadlineExceeded has the methods of net.Error. It is not a
	// failure of the connection.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed)
}

// isDead reports whether the connection of c has ended or is ending.
//
// The state is the reliable signal. When the reader of imapclient fails, it
// sets the state to logout before it completes the pending commands, so a
// call that got the error already sees it. The Closed channel is closed a
// little later, so it alone can still say "open".
func isDead(c *imapclient.Client) bool {
	select {
	case <-c.Closed():
		return true
	default:
	}
	return c.State() == imap.ConnStateLogout
}

// acquire waits for a free slot, or for ctx to end, or for the pool to close.
func (p *pool) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-p.done:
		return errPoolClosed
	default:
	}
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errPoolClosed
	}
}

// release gives the slot back.
func (p *pool) release() { <-p.slots }

// takeOrDial returns an idle connection, or a new one when there is none.
func (p *pool) takeOrDial(ctx context.Context) (*pooledConn, error) {
	if pc := p.takeIdle(); pc != nil {
		return pc, nil
	}
	return p.dial(ctx)
}

// takeIdle removes and returns the newest healthy idle connection. It closes
// every idle connection that is too old or already ended. It returns nil when
// nothing is left.
func (p *pool) takeIdle() *pooledConn {
	var stale []*pooledConn
	var found *pooledConn

	p.mu.Lock()
	now := p.now()
	var keep []idleConn
	for _, ic := range p.idle {
		if now.Sub(ic.since) > p.cfg.IdleTimeout || isDead(ic.conn.client) {
			stale = append(stale, ic.conn)
			continue
		}
		keep = append(keep, ic)
	}
	if n := len(keep); n > 0 {
		found = keep[n-1].conn
		keep = keep[:n-1]
	}
	p.idle = keep
	p.mu.Unlock()

	for _, pc := range stale {
		pc.forceClose()
	}
	return found
}

// dial opens a new connection. A closed pool does not dial.
func (p *pool) dial(ctx context.Context) (*pooledConn, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errPoolClosed
	}
	client, raw, err := dialWithConn(ctx, p.cfg)
	if err != nil {
		return nil, err
	}
	return &pooledConn{client: client, raw: raw}, nil
}

// finish puts a reusable connection in the idle list, and closes any other
// one. A connection that has ended never goes to the idle list. After Close,
// no connection goes there.
func (p *pool) finish(pc *pooledConn, reusable bool) {
	if reusable {
		p.mu.Lock()
		if !p.closed && !isDead(pc.client) {
			p.idle = append(p.idle, idleConn{conn: pc, since: p.now()})
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}
	pc.forceClose()
}

// Close closes all idle connections and stops new calls. A connection that
// is in use is closed when its call ends. Close can be called more than once.
// It does not wait for any connection.
func (p *pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	close(p.done)
	for _, ic := range idle {
		ic.conn.forceClose()
	}
}
