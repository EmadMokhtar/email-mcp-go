package imapsmtp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// errPoolClosed is returned by a pool that was closed.
var errPoolClosed = errors.New("imap: pool is closed")

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
	client *imapclient.Client
	since  time.Time
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
// If fn fails with a network error (a dropped connection, a timeout of the
// socket, an end of file), with closes that connection, dials a new one and
// runs fn once more. So fn must be safe to run twice. An error from the
// server (NO or BAD) is returned as it is and is never retried. A failed dial
// is not retried either.
//
// fn must not keep the client after it returns. The mailbox that fn selected
// can still be selected on the next call, so every fn must select what it
// needs.
//
// with ends the call when ctx ends, or when fn runs longer than
// CommandTimeout. In both cases it closes the connection so that fn stops, and
// it does not retry. The error then wraps ctx's error, or
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

	c, err := p.takeOrDial(ctx)
	if err != nil {
		return err
	}
	healthy, err := p.run(ctx, c, fn)
	p.finish(c, healthy)
	if err == nil || !retry || !shouldRetry(ctx, err) {
		return err
	}

	c, err = p.dial(ctx)
	if err != nil {
		return err
	}
	healthy, err = p.run(ctx, c, fn)
	p.finish(c, healthy)
	return err
}

// run calls fn on c. It closes c if ctx ends or CommandTimeout passes first.
// healthy is true when c can serve another call.
func (p *pool) run(ctx context.Context, c *imapclient.Client, fn func(c *imapclient.Client) error) (healthy bool, err error) {
	// imapclient sets and clears the read and write deadlines of the
	// connection itself, so a deadline set from here would not last.
	// Closing the connection is the way to stop a command.
	opCtx, cancel := context.WithTimeout(ctx, p.cfg.CommandTimeout)
	defer cancel()
	stop := context.AfterFunc(opCtx, func() { _ = c.Close() })

	err = fn(c)

	if !stop() {
		// The connection was closed under fn. If fn failed, the failure comes
		// from that close, so report the real reason.
		if err != nil && isNetworkError(err) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = fmt.Errorf("imap: command stopped: %w", ctxErr)
			} else {
				err = fmt.Errorf("imap: command took longer than %s: %w", p.cfg.CommandTimeout, context.DeadlineExceeded)
			}
		}
		return false, err
	}
	return !isNetworkError(err) && !isClosed(c), err
}

// shouldRetry reports whether a failed call may run again on a new connection.
func shouldRetry(ctx context.Context, err error) bool {
	// A call that ended because of ctx or the command timeout is not retried:
	// the retry would wait for the same time again.
	return ctx.Err() == nil && isNetworkError(err)
}

// isNetworkError reports whether err means that the connection failed, as
// opposed to the server saying no to a command.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var serverErr *imap.Error
	if errors.As(err, &serverErr) {
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

// isClosed reports whether the connection of c has ended. imapclient reads the
// connection all the time, so it finds out soon when the server closes it.
func isClosed(c *imapclient.Client) bool {
	select {
	case <-c.Closed():
		return true
	default:
		return false
	}
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
func (p *pool) takeOrDial(ctx context.Context) (*imapclient.Client, error) {
	if c := p.takeIdle(); c != nil {
		return c, nil
	}
	return p.dial(ctx)
}

// takeIdle removes and returns the newest healthy idle connection. It closes
// every idle connection that is too old or already ended. It returns nil when
// nothing is left.
func (p *pool) takeIdle() *imapclient.Client {
	var stale []*imapclient.Client
	var found *imapclient.Client

	p.mu.Lock()
	now := p.now()
	var keep []idleConn
	for _, ic := range p.idle {
		if now.Sub(ic.since) > p.cfg.IdleTimeout || isClosed(ic.client) {
			stale = append(stale, ic.client)
			continue
		}
		keep = append(keep, ic)
	}
	if n := len(keep); n > 0 {
		found = keep[n-1].client
		keep = keep[:n-1]
	}
	p.idle = keep
	p.mu.Unlock()

	// Closing waits for the connection to stop, so do it outside the lock.
	for _, c := range stale {
		_ = c.Close()
	}
	return found
}

// dial opens a new connection. A closed pool does not dial.
func (p *pool) dial(ctx context.Context) (*imapclient.Client, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errPoolClosed
	}
	return dialIMAP(ctx, p.cfg)
}

// finish puts a healthy connection in the idle list, and closes any other
// one. A connection that has ended never goes to the idle list. After Close,
// no connection goes there.
func (p *pool) finish(c *imapclient.Client, healthy bool) {
	if healthy {
		p.mu.Lock()
		if !p.closed && !isClosed(c) {
			p.idle = append(p.idle, idleConn{client: c, since: p.now()})
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}
	_ = c.Close()
}

// Close closes all idle connections and stops new calls. A connection that
// is in use is closed when its call ends. Close can be called more than once.
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
		_ = ic.client.Close()
	}
}
