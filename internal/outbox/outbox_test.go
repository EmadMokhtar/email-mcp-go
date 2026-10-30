package outbox

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the driver that the store uses, for the second connection

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// testStart is the time at which every test starts.
var testStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// clock is a time source that a test can move. It is safe for use by many
// goroutines.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// sentCall is one call of the fake sender.
type sentCall struct {
	tenant account.TenantID
	acct   account.AccountID
	msg    mail.RawMessage
	draft  mail.Ref
}

// fakeSender is a Sender that records its calls. It is safe for use by many
// goroutines.
type fakeSender struct {
	mu    sync.Mutex
	calls []sentCall
	// fn decides the result of call number n (the first call is 1). A nil fn
	// means that every call works.
	fn func(ctx context.Context, n int) error
	// delay is how long each call takes.
	delay time.Duration
}

func (s *fakeSender) send(ctx context.Context, tenant account.TenantID, acct account.AccountID, msg *mail.RawMessage, draft mail.Ref) error {
	s.mu.Lock()
	s.calls = append(s.calls, sentCall{tenant: tenant, acct: acct, msg: *msg, draft: draft})
	n := len(s.calls)
	fn, delay := s.fn, s.delay
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if fn != nil {
		return fn(ctx, n)
	}
	return nil
}

func (s *fakeSender) setFn(fn func(ctx context.Context, n int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *fakeSender) call(i int) sentCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

// bodies returns the body text of every call, in call order.
func (s *fakeSender) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		out = append(out, string(c.msg.Data))
	}
	return out
}

// fixture is an outbox on a real store and a real vault, with one account, a
// fake clock and a fake sender.
type fixture struct {
	t       *testing.T
	dir     string
	keyPath string
	store   *store.Store
	vault   *secrets.Vault
	clock   *clock
	sender  *fakeSender
	ob      *Outbox
	tenant  account.TenantID
	acct    account.AccountID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	a := account.NewIMAPAccount(tenant, "work@example.com", account.MailServer{}, testStart)
	require.NoError(t, st.CreateAccount(ctx, a))

	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))

	f := &fixture{
		t:       t,
		dir:     dir,
		keyPath: keyPath,
		store:   st,
		clock:   &clock{t: testStart},
		sender:  &fakeSender{},
		tenant:  tenant,
		acct:    a.ID,
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.vault = f.newVault(st)
	f.ob = f.newOutbox()
	return f
}

// newVault returns a vault on st that uses the master key of the fixture.
func (f *fixture) newVault(st *store.Store) *secrets.Vault {
	f.t.Helper()
	kek, err := secrets.NewFileKEK(f.keyPath)
	require.NoError(f.t, err)
	return secrets.NewVault(st, kek)
}

// newOutbox returns another outbox on the same store, vault, clock and sender.
func (f *fixture) newOutbox() *Outbox {
	return New(f.store, f.vault, f.sender.send, f.clock.Now)
}

// restart simulates a new start of the program: it closes the store, opens it
// again, and makes a new vault and a new outbox.
func (f *fixture) restart() {
	f.t.Helper()
	require.NoError(f.t, f.store.Close())
	st, err := store.Open(context.Background(), f.dir)
	require.NoError(f.t, err)
	f.store = st
	f.vault = f.newVault(st)
	f.ob = f.newOutbox()
}

func testMessage(body string) *mail.RawMessage {
	return &mail.RawMessage{
		From:       "me@example.com",
		Recipients: []string{"bob@example.org", "carol@example.org"},
		Data:       []byte("Subject: Quarterly figures for Zebra\r\n\r\n" + body),
	}
}

// enqueue queues a message in the account of the fixture.
func (f *fixture) enqueue(body string, window time.Duration) *Item {
	f.t.Helper()
	item, err := f.ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage(body), window, "")
	require.NoError(f.t, err)
	return item
}

// pass sends everything that is due now and returns. It is Drain with no
// waiting.
func (f *fixture) pass() {
	f.t.Helper()
	require.NoError(f.t, f.ob.Drain(context.Background(), 0))
}

// status returns the status of an item of the fixture tenant.
func (f *fixture) status(id string) Status {
	f.t.Helper()
	item, err := f.ob.Get(context.Background(), f.tenant, id)
	require.NoError(f.t, err)
	return item.Status
}

// run starts Run in a goroutine. The returned function stops it and checks that
// it ended with the error of the context. It also runs when the test ends.
func (f *fixture) run(ob *Outbox, tick time.Duration) (stop func()) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ob.Run(ctx, tick) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(f.t, err, context.Canceled)
			case <-time.After(10 * time.Second):
				f.t.Error("Run did not stop")
			}
		})
	}
	f.t.Cleanup(stop)
	return stop
}

// openDB opens a second connection to the database file.
func (f *fixture) openDB() *sql.DB {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	return db
}

// exec runs one statement on a second connection.
func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err := db.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err)
}

// rawItem is what the table holds for one item, read without the store.
type rawItem struct {
	status      string
	attempts    int
	errText     string
	messageNull bool
	draftNull   bool
	nextAttempt int64
	sentAt      sql.NullInt64
}

func (f *fixture) raw(id string) rawItem {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	var r rawItem
	err := db.QueryRowContext(context.Background(),
		`SELECT status, attempts, error_text, message IS NULL, draft_ref IS NULL, next_attempt_at, sent_at
		FROM outbox WHERE id = ?`, id).
		Scan(&r.status, &r.attempts, &r.errText, &r.messageNull, &r.draftNull, &r.nextAttempt, &r.sentAt)
	require.NoError(f.t, err)
	return r
}

// dataFiles returns the bytes of every file in the data directory: the database
// and its write-ahead log.
func (f *fixture) dataFiles() []byte {
	f.t.Helper()
	entries, err := os.ReadDir(f.dir)
	require.NoError(f.t, err)
	var all []byte
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		require.NoError(f.t, err)
		all = append(all, data...)
	}
	return all
}

// addTenant adds a tenant with one account, for isolation tests.
func (f *fixture) addTenant(id account.TenantID) account.AccountID {
	f.t.Helper()
	f.exec(`INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	a := account.NewIMAPAccount(id, "other@example.com", account.MailServer{}, testStart)
	require.NoError(f.t, f.store.CreateAccount(context.Background(), a))
	return a.ID
}

// ---- The tests of the brief ----

func TestEnqueueWindowZeroSendsInline(t *testing.T) {
	f := newFixture(t)

	item, err := f.ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("inline body"), 0, "")
	require.NoError(t, err)

	require.Equal(t, StatusSent, item.Status)
	require.Empty(t, item.Error)
	require.Equal(t, f.tenant, item.Tenant)
	require.Equal(t, f.acct, item.Account)
	require.Equal(t, testStart, item.SendAfter)
	require.Equal(t, 1, f.sender.count(), "the message is sent before Enqueue returns")
	call := f.sender.call(0)
	require.Equal(t, f.tenant, call.tenant)
	require.Equal(t, f.acct, call.acct)
	require.Equal(t, *testMessage("inline body"), call.msg)

	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, item, got)
	r := f.raw(item.ID)
	require.True(t, r.sentAt.Valid)
	require.Equal(t, testStart.UnixMilli(), r.sentAt.Int64)

	// A later pass does not send it again.
	f.clock.Advance(time.Hour)
	f.pass()
	require.Equal(t, 1, f.sender.count())
}

func TestCancelWithinWindow(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("changed my mind", 30*time.Second)
	require.Equal(t, StatusQueued, item.Status)
	require.Equal(t, testStart.Add(30*time.Second), item.SendAfter)

	f.clock.Advance(10 * time.Second)
	require.NoError(t, f.ob.Cancel(context.Background(), f.tenant, item.ID))

	require.Equal(t, StatusCancelled, f.status(item.ID))
	r := f.raw(item.ID)
	require.True(t, r.messageNull, "a cancelled message is deleted")

	// The window ends, and nothing is sent.
	f.clock.Advance(time.Hour)
	f.pass()
	require.Zero(t, f.sender.count())
	require.Equal(t, StatusCancelled, f.status(item.ID))
}

func TestCancelAfterSendTooLate(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("gone", 0)
	require.Equal(t, StatusSent, item.Status)

	err := f.ob.Cancel(context.Background(), f.tenant, item.ID)
	require.ErrorIs(t, err, ErrTooLate)
	require.Equal(t, StatusSent, f.status(item.ID))
}

func TestRunSendsWhenDue(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("later", 30*time.Second)
	stop := f.run(f.ob, 2*time.Millisecond)

	// Not due yet: give the worker time to prove that it does not send.
	f.clock.Advance(29*time.Second + 999*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	require.Zero(t, f.sender.count())
	require.Equal(t, StatusQueued, f.status(item.ID))

	// Due at exactly send_after.
	f.clock.Advance(time.Millisecond)
	require.Eventually(t, func() bool { return f.status(item.ID) == StatusSent }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.sender.count())
	stop()
	require.Equal(t, 1, f.sender.count())
}

func TestTwoWorkersSendOnce(t *testing.T) {
	f := newFixture(t)
	f.sender.delay = 2 * time.Millisecond // keeps the claim window open
	const items = 30
	ids := make([]string, items)
	for i := range ids {
		ids[i] = f.enqueue(fmt.Sprintf("body-%02d", i), 10*time.Second).ID
	}
	// Two outbox instances on the same store.
	f.run(f.newOutbox(), time.Millisecond)
	f.run(f.newOutbox(), time.Millisecond)
	f.clock.Advance(10 * time.Second)

	require.Eventually(t, func() bool {
		for _, id := range ids {
			if f.status(id) != StatusSent {
				return false
			}
		}
		return true
	}, 20*time.Second, 5*time.Millisecond)

	// Let a late duplicate show itself before we count.
	time.Sleep(50 * time.Millisecond)
	seen := make(map[string]int)
	for _, body := range f.sender.bodies() {
		seen[body]++
	}
	require.Len(t, seen, items)
	for body, n := range seen {
		require.Equal(t, 1, n, "%q was sent %d times", body, n)
	}
}

// TestTwoProcessesSendOnce is the same as TestTwoWorkersSendOnce, with two
// store handles on one database file, as two programs would have.
func TestTwoProcessesSendOnce(t *testing.T) {
	f := newFixture(t)
	f.sender.delay = 2 * time.Millisecond
	st2, err := store.Open(context.Background(), f.dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st2.Close() })
	other := New(st2, f.newVault(st2), f.sender.send, f.clock.Now)

	const items = 20
	ids := make([]string, items)
	for i := range ids {
		ids[i] = f.enqueue(fmt.Sprintf("body-%02d", i), 10*time.Second).ID
	}
	f.run(f.ob, time.Millisecond)
	f.run(other, time.Millisecond)
	f.clock.Advance(10 * time.Second)

	require.Eventually(t, func() bool {
		for _, id := range ids {
			if f.status(id) != StatusSent {
				return false
			}
		}
		return true
	}, 20*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	seen := make(map[string]int)
	for _, body := range f.sender.bodies() {
		seen[body]++
	}
	require.Len(t, seen, items)
	for body, n := range seen {
		require.Equal(t, 1, n, "%q was sent %d times", body, n)
	}
}

func TestDrainSendsPendingBeforeExit(t *testing.T) {
	f := newFixture(t)
	f.poll(f.ob, 5*time.Millisecond)
	dueNow := f.enqueue("due now", 10*time.Second)
	soon := f.enqueue("soon", 40*time.Second)
	late := f.enqueue("late", 10*time.Minute)
	f.clock.Advance(10 * time.Second)

	done := make(chan error, 1)
	go func() { done <- f.ob.Drain(context.Background(), time.Minute) }()
	// The clock moves while Drain waits: "soon" becomes due inside maxWait.
	time.Sleep(30 * time.Millisecond)
	f.clock.Advance(30 * time.Second)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Drain did not return")
	}
	require.Equal(t, StatusSent, f.status(dueNow.ID))
	require.Equal(t, StatusSent, f.status(soon.ID))
	require.Equal(t, []string{"due now", "soon"}, bodiesAfterHeaders(f.sender.bodies()))
	// The item that is due after maxWait is kept: not sent, not cancelled, and
	// its message is still there.
	require.Equal(t, StatusQueued, f.status(late.ID))
	require.False(t, f.raw(late.ID).messageNull)
}

func TestQueuedSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("survives", 30*time.Second)
	r := f.raw(item.ID)
	require.Equal(t, "queued", r.status)

	f.restart()
	f.clock.Advance(30 * time.Second)
	f.pass()

	require.Equal(t, 1, f.sender.count())
	require.Equal(t, "survives", bodiesAfterHeaders(f.sender.bodies())[0])
	require.Equal(t, StatusSent, f.status(item.ID))
}

func TestMessageBodyDeletedAfterSend(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("the body to delete", 10*time.Second)
	require.False(t, f.raw(item.ID).messageNull, "the message is kept while it waits")

	f.clock.Advance(10 * time.Second)
	f.pass()

	require.Equal(t, StatusSent, f.status(item.ID))
	r := f.raw(item.ID)
	require.True(t, r.messageNull)
	require.True(t, r.draftNull)
	// The sender got the whole message before it was deleted.
	require.Equal(t, "the body to delete", bodiesAfterHeaders(f.sender.bodies())[0])
}

// ---- The tests of the rulings ----

func TestPermanentErrorWrapping(t *testing.T) {
	require.Nil(t, Permanent(nil))
	require.False(t, IsPermanent(nil))
	require.False(t, IsPermanent(errors.New("plain")))

	base := errors.New("recipient refused")
	perm := Permanent(base)
	require.True(t, IsPermanent(perm))
	require.ErrorIs(t, perm, base)
	require.Equal(t, "recipient refused", perm.Error(), "the text is the text of the original error")

	// It works through further wrapping, and errors.As still finds the cause.
	wrapped := fmt.Errorf("send: %w", perm)
	require.True(t, IsPermanent(wrapped))
	require.ErrorIs(t, wrapped, base)
	var target *os.PathError
	pathErr := &os.PathError{Op: "open", Path: "x", Err: errors.New("denied")}
	require.True(t, errors.As(Permanent(pathErr), &target))
	require.Same(t, pathErr, target)

	// Wrapping twice changes nothing.
	require.Same(t, perm, Permanent(perm))
}

func TestPermanentErrorFailsAtOnce(t *testing.T) {
	f := newFixture(t)
	f.sender.setFn(func(context.Context, int) error {
		return Permanent(errors.New("550 mailbox does not exist"))
	})
	item := f.enqueue("rejected", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	f.pass()

	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "550 mailbox does not exist", got.Error)
	r := f.raw(item.ID)
	require.Equal(t, 1, r.attempts)
	require.True(t, r.messageNull)

	// No retry, however long we wait.
	f.clock.Advance(24 * time.Hour)
	f.pass()
	require.Equal(t, 1, f.sender.count())
}

func TestTransientErrorIsRetriedWithBackoff(t *testing.T) {
	f := newFixture(t)
	f.sender.setFn(func(context.Context, int) error { return errors.New("451 try again later") })
	item := f.enqueue("retry me", 10*time.Second)
	f.clock.Advance(10 * time.Second)

	// check runs a pass and checks the state after it.
	check := func(calls int, status string, attempts int, next time.Duration) {
		t.Helper()
		f.pass()
		require.Equal(t, calls, f.sender.count(), "calls")
		r := f.raw(item.ID)
		require.Equal(t, status, r.status)
		require.Equal(t, attempts, r.attempts)
		if status == "queued" {
			require.Equal(t, f.clock.Now().Add(next).UnixMilli(), r.nextAttempt)
			require.Equal(t, "451 try again later", r.errText)
			require.False(t, r.messageNull, "a message that will be retried is kept")
		}
	}

	// First failure: retry in 30 s.
	check(1, "queued", 1, 30*time.Second)
	f.clock.Advance(30*time.Second - time.Millisecond)
	f.pass()
	require.Equal(t, 1, f.sender.count(), "not due 1 ms early")

	// Second failure: retry in 2 minutes.
	f.clock.Advance(time.Millisecond)
	check(2, "queued", 2, 2*time.Minute)
	f.clock.Advance(2*time.Minute - time.Millisecond)
	f.pass()
	require.Equal(t, 2, f.sender.count())

	// Third failure: retry in 10 minutes.
	f.clock.Advance(time.Millisecond)
	check(3, "queued", 3, 10*time.Minute)
	f.clock.Advance(10*time.Minute - time.Millisecond)
	f.pass()
	require.Equal(t, 3, f.sender.count())

	// The fourth failure is the last one.
	f.clock.Advance(time.Millisecond)
	f.pass()
	require.Equal(t, 4, f.sender.count())
	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "451 try again later", got.Error)
	r := f.raw(item.ID)
	require.Equal(t, 4, r.attempts)
	require.True(t, r.messageNull)

	f.clock.Advance(24 * time.Hour)
	f.pass()
	require.Equal(t, 4, f.sender.count(), "a failed item is never sent again")
}

func TestTransientErrorThenSuccess(t *testing.T) {
	f := newFixture(t)
	f.sender.setFn(func(_ context.Context, n int) error {
		if n == 1 {
			return errors.New("connection reset")
		}
		return nil
	})
	item := f.enqueue("second time works", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	f.pass()
	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, got.Status)
	require.Equal(t, "connection reset", got.Error)

	f.clock.Advance(30 * time.Second)
	f.pass()
	got, err = f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSent, got.Status)
	require.Empty(t, got.Error, "the error of an earlier try is cleared by a success")
	require.Equal(t, 2, f.sender.count())
}

// A transient failure with window 0 is not reported as a failure. The item
// stays queued and the worker retries it.
func TestWindowZeroTransientFailureStaysQueued(t *testing.T) {
	f := newFixture(t)
	f.sender.setFn(func(_ context.Context, n int) error {
		if n == 1 {
			return errors.New("421 service not available")
		}
		return nil
	})

	item, err := f.ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("inline retry"), 0, "")
	require.NoError(t, err)
	require.Equal(t, StatusQueued, item.Status)
	require.Equal(t, "421 service not available", item.Error)
	require.Equal(t, 1, f.sender.count())

	f.pass()
	require.Equal(t, 1, f.sender.count(), "not due before the backoff has passed")
	f.clock.Advance(30 * time.Second)
	f.pass()
	require.Equal(t, 2, f.sender.count())
	require.Equal(t, StatusSent, f.status(item.ID))
}

func TestWindowZeroPermanentFailureReturnsFailed(t *testing.T) {
	f := newFixture(t)
	f.sender.setFn(func(context.Context, int) error { return Permanent(errors.New("550 no such user")) })

	item, err := f.ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("inline fail"), 0, "")
	require.NoError(t, err)
	require.Equal(t, StatusFailed, item.Status)
	require.Equal(t, "550 no such user", item.Error)
	require.True(t, f.raw(item.ID).messageNull)
}

func TestRecoverInterruptedMarksSendingItemsFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	otherTenant := account.TenantID("other")
	otherAcct := f.addTenant(otherTenant)

	stuck := f.enqueue("stuck", 10*time.Second)
	stuckToo, err := f.ob.Enqueue(ctx, otherTenant, otherAcct, testMessage("stuck in another tenant"), 10*time.Second, "drafts:7")
	require.NoError(t, err)
	waiting := f.enqueue("still waiting", time.Hour)
	f.clock.Advance(10 * time.Second)
	// The process "stopped" with two items in the state sending.
	_, err = f.store.ClaimOutbox(ctx, f.tenant, stuck.ID, f.clock.Now())
	require.NoError(t, err)
	_, err = f.store.ClaimOutbox(ctx, otherTenant, stuckToo.ID, f.clock.Now())
	require.NoError(t, err)
	// Sixteen minutes later nothing can still be sending them.
	f.clock.Advance(16 * time.Minute)

	n, err := f.ob.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n, "it counts the items of every tenant")

	const wantText = "delivery unknown: the process stopped while sending"
	for _, id := range []string{stuck.ID, stuckToo.ID} {
		r := f.raw(id)
		require.Equal(t, "failed", r.status)
		require.Equal(t, wantText, r.errText)
		require.True(t, r.messageNull)
		require.True(t, r.draftNull)
	}
	got, err := f.ob.Get(ctx, f.tenant, stuck.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, wantText, got.Error)
	require.Equal(t, StatusQueued, f.status(waiting.ID), "queued items are not touched")

	// Run never sends them.
	f.clock.Advance(24 * time.Hour)
	f.pass()
	require.Equal(t, []string{"still waiting"}, bodiesAfterHeaders(f.sender.bodies()), "only the queued item is sent")

	n, err = f.ob.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

// claimAt puts a due item in the state sending, as if a worker had claimed it at
// the given time.
func (f *fixture) claimAt(id string, at time.Time) {
	f.t.Helper()
	_, err := f.store.ClaimOutbox(context.Background(), f.tenant, id, at)
	require.NoError(f.t, err)
}

// Several programs share one database. A program that starts must not mark the
// send of another live program as failed. Only an item that was claimed so long
// ago that no send can still run is recovered.
func TestRecoverInterruptedLeavesRecentItemsAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	recent := f.enqueue("sent a minute ago", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	claimed := f.clock.Now()
	f.claimAt(recent.ID, claimed)

	// One minute later.
	f.clock.Advance(time.Minute)
	n, err := f.ob.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "claimed 1 minute ago")
	require.Equal(t, StatusSending, f.status(recent.ID))
	require.False(t, f.raw(recent.ID).messageNull)

	// Exactly 15 minutes: the item must be OLDER than that.
	f.clock.Advance(recoverAfter - time.Minute)
	require.Equal(t, claimed.Add(recoverAfter), f.clock.Now())
	n, err = f.ob.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "claimed exactly 15 minutes ago")
	require.Equal(t, StatusSending, f.status(recent.ID))

	// One millisecond more.
	f.clock.Advance(time.Millisecond)
	n, err = f.ob.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := f.ob.Get(ctx, f.tenant, recent.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "delivery unknown: the process stopped while sending", got.Error)
}

// Two items in the state sending: only the old one is recovered.
func TestRecoverInterruptedTakesOnlyTheStaleOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	stale := f.enqueue("stale", 10*time.Second)
	live := f.enqueue("live", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	f.claimAt(stale.ID, f.clock.Now())
	f.clock.Advance(15 * time.Minute)
	f.claimAt(live.ID, f.clock.Now())
	f.clock.Advance(time.Minute) // stale: 16 minutes. live: 1 minute.

	n, err := f.ob.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, StatusFailed, f.status(stale.ID))
	require.True(t, f.raw(stale.ID).messageNull)
	require.Equal(t, StatusSending, f.status(live.ID))
	require.False(t, f.raw(live.ID).messageNull, "the live send keeps its message")

	// Run never sends either of them.
	f.clock.Advance(24 * time.Hour)
	f.pass()
	require.Zero(t, f.sender.count())
}

// Run recovers stale items itself, about once a minute, so a program that died
// while sending is found even though the other programs never restart.
func TestRunRecoversStaleItemsWithoutARestart(t *testing.T) {
	f := newFixture(t)
	stale := f.enqueue("stale", 10*time.Second)
	live := f.enqueue("live", 10*time.Second)
	f.run(f.ob, 2*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // Run has made its first pass

	// The other program claimed the two items at the (future) times of the
	// claim. The clock of this program moves 16 minutes on.
	start := f.clock.Now()
	f.claimAt(stale.ID, start.Add(10*time.Second))
	f.claimAt(live.ID, start.Add(15*time.Minute+10*time.Second))
	f.clock.Advance(16*time.Minute + 10*time.Second)

	require.Eventually(t, func() bool { return f.status(stale.ID) == StatusFailed }, 5*time.Second, 5*time.Millisecond)
	got, err := f.ob.Get(context.Background(), f.tenant, stale.ID)
	require.NoError(t, err)
	require.Equal(t, "delivery unknown: the process stopped while sending", got.Error)
	require.True(t, f.raw(stale.ID).messageNull)

	// The newer one is still being sent, as far as this program can tell.
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, StatusSending, f.status(live.ID))
	require.False(t, f.raw(live.ID).messageNull)
	require.Zero(t, f.sender.count(), "recovered items are never sent")
}

// The first pass of Run also recovers, so a stale item that was there when the
// program started is found without waiting a minute.
func TestRunRecoversAtItsFirstPass(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("stale at start", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	f.claimAt(item.ID, f.clock.Now())
	f.clock.Advance(recoverAfter + time.Second)

	f.run(f.ob, 2*time.Millisecond)
	require.Eventually(t, func() bool { return f.status(item.ID) == StatusFailed }, 5*time.Second, 5*time.Millisecond)
	require.Zero(t, f.sender.count())
}

func TestCancelDuringSendingIsTooLate(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	f.sender.setFn(func(context.Context, int) error {
		close(started)
		<-release
		return nil
	})
	item := f.enqueue("in flight", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.pass()
	}()
	<-started

	require.Equal(t, StatusSending, f.status(item.ID))
	err := f.ob.Cancel(context.Background(), f.tenant, item.ID)
	require.ErrorIs(t, err, ErrTooLate)

	close(release)
	<-done
	require.Equal(t, StatusSent, f.status(item.ID))
}

func TestCancelResults(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	t.Run("failed is too late", func(t *testing.T) {
		f.sender.setFn(func(context.Context, int) error { return Permanent(errors.New("no")) })
		item := f.enqueue("failed", 0)
		require.Equal(t, StatusFailed, item.Status)
		require.ErrorIs(t, f.ob.Cancel(ctx, f.tenant, item.ID), ErrTooLate)
		require.Equal(t, StatusFailed, f.status(item.ID))
		f.sender.setFn(nil)
	})

	t.Run("a second cancel works again", func(t *testing.T) {
		item := f.enqueue("twice", time.Minute)
		require.NoError(t, f.ob.Cancel(ctx, f.tenant, item.ID))
		require.NoError(t, f.ob.Cancel(ctx, f.tenant, item.ID))
		require.Equal(t, StatusCancelled, f.status(item.ID))
	})

	t.Run("missing", func(t *testing.T) {
		err := f.ob.Cancel(ctx, f.tenant, "aaaaaaaaaaaaaaaaaaaaaaaaaa")
		require.ErrorIs(t, err, ErrNotFound)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("another tenant", func(t *testing.T) {
		item := f.enqueue("mine", time.Minute)
		other := account.TenantID("other")
		f.addTenant(other)
		require.ErrorIs(t, f.ob.Cancel(ctx, other, item.ID), ErrNotFound)
		_, err := f.ob.Get(ctx, other, item.ID)
		require.ErrorIs(t, err, ErrNotFound)
		require.Equal(t, StatusQueued, f.status(item.ID), "the other tenant changed nothing")
	})

	t.Run("bad ids", func(t *testing.T) {
		for _, id := range []string{"", "a|b"} {
			require.ErrorIs(t, f.ob.Cancel(ctx, f.tenant, id), store.ErrInvalidID)
			_, err := f.ob.Get(ctx, f.tenant, id)
			require.ErrorIs(t, err, store.ErrInvalidID)
		}
		require.ErrorIs(t, f.ob.Cancel(ctx, "a|b", "x"), store.ErrInvalidID)
	})
}

func TestMessageAndDraftAreDeletedAtEveryEnd(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	draft := mail.Ref("drafts:42")
	enqueue := func(body string, window time.Duration) *Item {
		item, err := f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage(body), window, draft)
		require.NoError(t, err)
		return item
	}

	// While an item waits, it keeps both.
	waiting := enqueue("waiting", time.Hour)
	r := f.raw(waiting.ID)
	require.False(t, r.messageNull)
	require.False(t, r.draftNull)

	sent := enqueue("sent", 0)
	require.Equal(t, StatusSent, sent.Status)

	cancelled := enqueue("cancelled", time.Hour)
	require.NoError(t, f.ob.Cancel(ctx, f.tenant, cancelled.ID))

	f.sender.setFn(func(context.Context, int) error { return Permanent(errors.New("refused")) })
	failed := enqueue("failed", 0)
	require.Equal(t, StatusFailed, failed.Status)

	for name, id := range map[string]string{"sent": sent.ID, "cancelled": cancelled.ID, "failed": failed.ID} {
		r := f.raw(id)
		require.True(t, r.messageNull, "%s: the message must be deleted", name)
		require.True(t, r.draftNull, "%s: the draft reference must be deleted", name)
	}
	r = f.raw(waiting.ID)
	require.False(t, r.messageNull)
	require.False(t, r.draftNull)
}

func TestDraftRefReachesTheSender(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Window 0: inline.
	_, err := f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage("inline"), 0, "drafts:1")
	require.NoError(t, err)
	// A window: sent by the worker, so the reference came out of the store.
	_, err = f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage("queued"), 10*time.Second, "drafts:2")
	require.NoError(t, err)
	// No draft. A different window gives a fixed sending order.
	_, err = f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage("no draft"), 11*time.Second, "")
	require.NoError(t, err)
	f.clock.Advance(11 * time.Second)
	f.pass()

	require.Equal(t, 3, f.sender.count())
	require.Equal(t, mail.Ref("drafts:1"), f.sender.call(0).draft)
	require.Equal(t, mail.Ref("drafts:2"), f.sender.call(1).draft)
	require.Equal(t, mail.Ref(""), f.sender.call(2).draft)
}

func TestMessageIsEncryptedAtRest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	item := f.enqueue("the secret launch date is 5 May", time.Hour)

	files := f.dataFiles()
	require.Contains(t, string(files), "work@example.com", "positive control: the scan sees plain data")
	for _, secret := range []string{
		"the secret launch date is 5 May",
		"Quarterly figures for Zebra",
		"bob@example.org",
		"carol@example.org",
		"me@example.com",
	} {
		require.NotContains(t, string(files), secret)
	}

	// The message is sealed for this item: the associated data names the item.
	version, sealed, err := f.store.GetOutboxMessage(ctx, f.tenant, item.ID)
	require.NoError(t, err)
	plain, err := f.vault.Decrypt(ctx, f.tenant, version, secrets.AAD(string(f.tenant), string(f.acct), "outbox:"+item.ID), sealed)
	require.NoError(t, err)
	require.Contains(t, string(plain), "bob@example.org")
	_, err = f.vault.Decrypt(ctx, f.tenant, version, secrets.AAD(string(f.tenant), string(f.acct), "outbox:"+item.ID+"x"), sealed)
	require.ErrorIs(t, err, secrets.ErrDecrypt)
}

func TestItemIDs(t *testing.T) {
	f := newFixture(t)
	pattern := regexp.MustCompile(`^[a-z2-7]{26}$`)
	seen := make(map[string]bool)
	for i := 0; i < 5; i++ {
		item := f.enqueue(fmt.Sprintf("id %d", i), time.Hour)
		require.Regexp(t, pattern, item.ID)
		require.False(t, seen[item.ID])
		seen[item.ID] = true
	}
}

func TestEnqueueRefusesBadInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, err := f.ob.Enqueue(ctx, f.tenant, f.acct, nil, time.Minute, "")
	require.Error(t, err)
	_, err = f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage("x"), -time.Second, "")
	require.Error(t, err)
	_, err = f.ob.Enqueue(ctx, "a|b", f.acct, testMessage("x"), time.Minute, "")
	require.ErrorIs(t, err, store.ErrInvalidID)
	_, err = f.ob.Enqueue(ctx, f.tenant, "", testMessage("x"), time.Minute, "")
	require.ErrorIs(t, err, store.ErrInvalidID)

	// An account that does not exist: nothing is stored and nothing is sent,
	// even with window 0.
	_, err = f.ob.Enqueue(ctx, f.tenant, account.NewAccountID(), testMessage("x"), 0, "")
	require.Error(t, err)

	require.Zero(t, f.sender.count())
	db := f.openDB()
	defer func() { require.NoError(t, db.Close()) }()
	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM outbox`).Scan(&rows))
	require.Zero(t, rows)
}

func TestErrorTextIsCutTo300Characters(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("é", 500)
	f.sender.setFn(func(context.Context, int) error { return Permanent(errors.New(long)) })

	item, err := f.ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("x"), 0, "")
	require.NoError(t, err)
	require.Equal(t, StatusFailed, item.Status)
	require.Equal(t, strings.Repeat("é", 300), item.Error)
	require.Equal(t, strings.Repeat("é", 300), f.raw(item.ID).errText)
}

func TestRunSendsOldestFirst(t *testing.T) {
	f := newFixture(t)
	f.enqueue("third", 30*time.Second)
	f.enqueue("first", 10*time.Second)
	f.enqueue("second", 20*time.Second)
	f.clock.Advance(time.Minute)
	f.pass()
	require.Equal(t, []string{"first", "second", "third"}, bodiesAfterHeaders(f.sender.bodies()))
}

func TestRunRefusesNoTick(t *testing.T) {
	f := newFixture(t)
	require.Error(t, f.ob.Run(context.Background(), 0))
	require.Error(t, f.ob.Run(context.Background(), -time.Second))
}

// The caller of Run may cancel its context while a send is running. If the send
// works, the result is still written.
func TestResultIsRecordedAfterTheContextIsCancelled(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("cancel at the end", 10*time.Second)
	f.clock.Advance(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	f.sender.setFn(func(context.Context, int) error {
		cancel()
		return nil
	})
	err := f.ob.Drain(ctx, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, StatusSent, f.status(item.ID), "a sent message must not stay in the state sending")
	require.True(t, f.raw(item.ID).messageNull)
}

// If a send runs into its own deadline and fails, the server may or may not
// have the message. The item is not retried.
func TestSendThatRunsIntoItsDeadlineIsNotRetried(t *testing.T) {
	f := newFixture(t)
	f.ob.sendTimeout = 20 * time.Millisecond
	f.ob.minBudget = 0 // this test needs a deadline that is shorter than the usual minimum
	item := f.enqueue("too slow", 10*time.Second)
	f.clock.Advance(10 * time.Second)

	f.sender.setFn(func(ctx context.Context, _ int) error {
		<-ctx.Done()
		return ctx.Err()
	})
	f.pass()

	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.True(t, strings.HasPrefix(got.Error, "delivery unknown:"), got.Error)
	require.True(t, f.raw(item.ID).messageNull)

	f.sender.setFn(nil)
	f.clock.Advance(24 * time.Hour)
	f.pass()
	require.Equal(t, 1, f.sender.count())
}

// A permanent error wins over a cancelled context: the sender said what
// happened.
func TestPermanentErrorWinsOverCancelledContext(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("refused", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	f.sender.setFn(func(context.Context, int) error {
		cancel()
		return Permanent(errors.New("550 refused"))
	})
	require.ErrorIs(t, f.ob.Drain(ctx, time.Minute), context.Canceled)
	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, "550 refused", got.Error)
}

// releasingStore cancels a context right after a claim, to stop the worker in
// the short time between the claim and the send.
type releasingStore struct {
	*store.Store
	cancel context.CancelFunc
}

func (s *releasingStore) ClaimOutbox(ctx context.Context, tenant account.TenantID, id string, now time.Time) (*store.OutboxRow, error) {
	row, err := s.Store.ClaimOutbox(ctx, tenant, id, now)
	s.cancel()
	return row, err
}

// If the program is told to stop after the claim and before the send, nothing
// was sent. The item goes back to the queue, and it is not a failed try.
func TestStopBetweenClaimAndSendReleasesTheItem(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("not sent yet", 10*time.Second)
	f.clock.Advance(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	ob := New(&releasingStore{Store: f.store, cancel: cancel}, f.vault, f.sender.send, f.clock.Now)
	require.ErrorIs(t, ob.Drain(ctx, time.Minute), context.Canceled)

	require.Zero(t, f.sender.count())
	r := f.raw(item.ID)
	require.Equal(t, "queued", r.status)
	require.Equal(t, 0, r.attempts)
	require.False(t, r.messageNull)

	f.pass()
	require.Equal(t, 1, f.sender.count())
	require.Equal(t, StatusSent, f.status(item.ID))
}

// passThroughBackoff runs a pass at each retry time of an item that fails every
// try: after 30 seconds, 2 minutes and 10 minutes.
func (f *fixture) passThroughBackoff() {
	f.t.Helper()
	for _, wait := range retryDelays {
		f.clock.Advance(wait)
		f.pass()
	}
}

// An item whose stored message cannot be opened is not sent. It follows the same
// steps as any other failed try, so a key that is wrong for a moment does not
// destroy the message. The message is deleted only when the item fails for good.
func TestUnreadableStoredMessageIsRetriedThenFailed(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("damaged", 10*time.Second)
	f.exec(`UPDATE outbox SET message = ? WHERE id = ?`, []byte("this is not a sealed message at all"), item.ID)
	f.clock.Advance(10 * time.Second)
	f.pass()

	got, err := f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusQueued, got.Status)
	require.Equal(t, "the stored message could not be read", got.Error)
	require.False(t, f.raw(item.ID).messageNull, "the message is kept while a retry is left")

	f.clock.Advance(30 * time.Second)
	f.pass()
	f.clock.Advance(2 * time.Minute)
	f.pass()
	require.Equal(t, StatusQueued, f.status(item.ID))
	f.clock.Advance(10 * time.Minute)
	f.pass()

	got, err = f.ob.Get(context.Background(), f.tenant, item.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, got.Status)
	require.Equal(t, "the stored message could not be read", got.Error)
	require.True(t, f.raw(item.ID).messageNull)
	require.Zero(t, f.sender.count(), "a message that cannot be read is never sent")
}

// A sealed message that is copied to another item cannot be opened there.
func TestStoredMessageIsBoundToItsItem(t *testing.T) {
	f := newFixture(t)
	a := f.enqueue("message of a", 10*time.Second)
	b := f.enqueue("message of b", 10*time.Second)
	f.exec(`UPDATE outbox SET message = (SELECT message FROM outbox WHERE id = ?) WHERE id = ?`, a.ID, b.ID)
	f.clock.Advance(10 * time.Second)
	f.pass()

	require.Equal(t, StatusSent, f.status(a.ID))
	require.Equal(t, StatusQueued, f.status(b.ID), "b waits for a retry")
	f.passThroughBackoff()
	require.Equal(t, StatusFailed, f.status(b.ID))
	require.Equal(t, []string{"message of a"}, bodiesAfterHeaders(f.sender.bodies()), "the copy is never sent")
}

// flakyStore fails its first DueOutbox call, as a busy database would.
type flakyStore struct {
	*store.Store
	mu    sync.Mutex
	calls int
}

func (s *flakyStore) DueOutbox(ctx context.Context, now time.Time, limit int) ([]*store.OutboxRow, error) {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first {
		return nil, errors.New("database is locked")
	}
	return s.Store.DueOutbox(ctx, now, limit)
}

// syncBuffer is a log destination that many goroutines can write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A database error in one pass of Run is logged and does not stop the worker.
// The log has a fixed code and the Go types of the error, never its text, as in
// every other package: a store error can quote a value.
func TestRunLogsAPassErrorAndGoesOn(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("private body text", 10*time.Second)
	f.clock.Advance(10 * time.Second)

	logs := &syncBuffer{}
	ob := New(&flakyStore{Store: f.store}, f.vault, f.sender.send, f.clock.Now)
	ob.Logger = slog.New(slog.NewTextHandler(logs, nil))
	f.run(ob, 2*time.Millisecond)

	require.Eventually(t, func() bool { return f.status(item.ID) == StatusSent }, 5*time.Second, 5*time.Millisecond)
	require.Contains(t, logs.String(), "a pass failed")
	require.Contains(t, logs.String(), "code=pass_failed")
	require.Contains(t, logs.String(), "error_types=")
	require.NotContains(t, logs.String(), "database is locked", "the text of the error is never logged")
	for _, secret := range []string{"private body text", "bob@example.org", "Zebra"} {
		require.NotContains(t, logs.String(), secret)
	}
}

func TestDrainReturnsAtMaxWaitAndLeavesLaterItemsQueued(t *testing.T) {
	f := newFixture(t)
	f.poll(f.ob, 5*time.Millisecond)
	later := f.enqueue("much later", 10*time.Minute)

	// Nothing becomes due inside maxWait: Drain returns at once.
	start := time.Now()
	require.NoError(t, f.ob.Drain(context.Background(), time.Minute))
	require.Less(t, time.Since(start), 5*time.Second)
	require.Zero(t, f.sender.count())
	require.Equal(t, StatusQueued, f.status(later.ID))
	require.False(t, f.raw(later.ID).messageNull)

	// An item is due inside maxWait by the clock, but the clock does not move.
	// Drain still ends when maxWait has passed in real time.
	soon := f.enqueue("soon but never", 20*time.Millisecond)
	start = time.Now()
	require.NoError(t, f.ob.Drain(context.Background(), 150*time.Millisecond))
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 140*time.Millisecond)
	require.Less(t, elapsed, 5*time.Second)
	require.Zero(t, f.sender.count())
	require.Equal(t, StatusQueued, f.status(soon.ID))
	require.False(t, f.raw(soon.ID).messageNull)

	// Both are still queued for the next start.
	f.restart()
	f.clock.Advance(20 * time.Minute)
	f.pass()
	require.Equal(t, 2, f.sender.count())
}

func TestDrainRespectsTheContext(t *testing.T) {
	f := newFixture(t)
	f.poll(f.ob, 5*time.Millisecond)
	item := f.enqueue("waiting", 20*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := f.ob.Drain(ctx, time.Hour)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
	require.Equal(t, StatusQueued, f.status(item.ID))
	require.False(t, f.raw(item.ID).messageNull)

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	require.ErrorIs(t, f.ob.Drain(cancelled, time.Hour), context.Canceled)
	require.Zero(t, f.sender.count())
}

func TestDrainWithNothingQueuedReturnsAtOnce(t *testing.T) {
	f := newFixture(t)
	start := time.Now()
	require.NoError(t, f.ob.Drain(context.Background(), time.Hour))
	require.Less(t, time.Since(start), 5*time.Second)
}

// bodiesAfterHeaders returns the part of each message after the header block.
func bodiesAfterHeaders(messages []string) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		_, body, _ := strings.Cut(m, "\r\n\r\n")
		out[i] = body
	}
	return out
}

// poll sets how often Drain looks at the clock while it waits.
func (f *fixture) poll(ob *Outbox, d time.Duration) {
	f.t.Helper()
	ob.poll = d
}

// ---- Hardening: claim, shutdown, result writes, window 0 ----

// claimThenCancelStore commits a claim and then ends the context of the worker,
// before the claim returns. This is what happens when the context ends after the
// database ran the UPDATE and before the driver read the row: the claim is
// committed, and the call returns the error of the context.
type claimThenCancelStore struct {
	*store.Store
	cancel context.CancelFunc
}

func (s *claimThenCancelStore) ClaimOutbox(ctx context.Context, tenant account.TenantID, id string, now time.Time) (*store.OutboxRow, error) {
	// The UPDATE itself runs and commits.
	row, err := s.Store.ClaimOutbox(context.WithoutCancel(ctx), tenant, id, now)
	s.cancel()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return row, err
}

// If the context of the worker ends while a claim runs, a committed claim must
// not be lost. The worker claims on a context that its caller cannot cancel, then
// sees that it must stop, and releases the item. The item ends "queued", not
// stuck in "sending" until the recovery fails it.
func TestClaimIsNotLostWhenTheContextEndsDuringIt(t *testing.T) {
	for name, run := range map[string]func(ob *Outbox, ctx context.Context) error{
		"Drain": func(ob *Outbox, ctx context.Context) error { return ob.Drain(ctx, time.Minute) },
		"Run":   func(ob *Outbox, ctx context.Context) error { return ob.Run(ctx, time.Millisecond) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			item := f.enqueue("claimed while stopping", 10*time.Second)
			f.clock.Advance(10 * time.Second)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ob := New(&claimThenCancelStore{Store: f.store, cancel: cancel}, f.vault, f.sender.send, f.clock.Now)
			require.ErrorIs(t, run(ob, ctx), context.Canceled)

			require.Zero(t, f.sender.count(), "nothing was sent")
			r := f.raw(item.ID)
			require.Equal(t, "queued", r.status, "the claim must not stay in the state sending")
			require.Equal(t, 0, r.attempts, "a release is not a failed try")
			require.False(t, r.messageNull)

			f.pass()
			require.Equal(t, 1, f.sender.count())
			require.Equal(t, StatusSent, f.status(item.ID))
		})
	}
}

// slowSend makes the fake sender wait until release is closed. It sends a signal
// on entered when a call starts. The call returns the error of its own context,
// which is nil unless something cancelled the context of the send.
func slowSend(f *fixture) (entered chan struct{}, release chan struct{}, sawCancel *atomic.Bool) {
	entered = make(chan struct{}, 8)
	release = make(chan struct{})
	sawCancel = &atomic.Bool{}
	f.sender.setFn(func(ctx context.Context, _ int) error {
		entered <- struct{}{}
		<-release
		if ctx.Err() != nil {
			sawCancel.Store(true)
		}
		return ctx.Err()
	})
	return entered, release, sawCancel
}

// The context of Run (and of Drain) stops new claims. It does not abort a send
// that is running. The send finishes, its result is written, and only then Run
// returns. No other item is claimed after the context ends.
func TestShutdownLetsTheSendInFlightFinish(t *testing.T) {
	for name, start := range map[string]func(ob *Outbox, ctx context.Context) error{
		"Run":   func(ob *Outbox, ctx context.Context) error { return ob.Run(ctx, time.Millisecond) },
		"Drain": func(ob *Outbox, ctx context.Context) error { return ob.Drain(ctx, time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			first := f.enqueue("first", 10*time.Second)
			second := f.enqueue("second", 10*time.Second)
			f.clock.Advance(10 * time.Second)
			entered, release, sawCancel := slowSend(f)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- start(f.ob, ctx) }()

			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the send did not start")
			}
			cancel() // the program is told to stop while the send runs

			select {
			case err := <-done:
				t.Fatalf("returned before the send finished: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(10 * time.Second):
				t.Fatal("did not return after the send finished")
			}

			require.False(t, sawCancel.Load(), "the sender must not see the cancel of the worker")
			// One of the two items was in flight. It is sent. The other is not claimed.
			require.Equal(t, 1, f.sender.count(), "no new item is claimed after the cancel")
			statuses := []Status{f.status(first.ID), f.status(second.ID)}
			require.ElementsMatch(t, []Status{StatusSent, StatusQueued}, statuses)
		})
	}
}

// The sender gets a deadline of its own that is shorter than the age at which
// another program may fail a "sending" item.
func TestSenderGetsADeadlineShorterThanTheRecoveryAge(t *testing.T) {
	require.Less(t, sendDeadline, recoverAfter)

	f := newFixture(t)
	var deadline time.Time
	var hasDeadline bool
	f.sender.setFn(func(ctx context.Context, _ int) error {
		deadline, hasDeadline = ctx.Deadline()
		return nil
	})
	f.enqueue("with a deadline", 0)

	require.True(t, hasDeadline)
	left := time.Until(deadline)
	require.Greater(t, left, sendDeadline-time.Minute)
	require.LessOrEqual(t, left, sendDeadline)
}

// With window 0 the caller waits for the send. If its context ends during the
// send, the send still finishes and the result is written.
func TestWindowZeroSendIsNotAbortedByTheContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sawCancel atomic.Bool
	f.sender.setFn(func(sendCtx context.Context, _ int) error {
		cancel()
		if sendCtx.Err() != nil {
			sawCancel.Store(true)
		}
		return sendCtx.Err()
	})

	item, err := f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage("inline"), 0, "")
	require.NoError(t, err)

	require.False(t, sawCancel.Load())
	require.Equal(t, StatusSent, item.Status)
}

// A context that has ended before Enqueue stores anything stops it. Nothing is
// stored and nothing is sent, also with window 0.
func TestEnqueueWithAnEndedContextStoresNothing(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, window := range []time.Duration{0, time.Minute} {
		_, err := f.ob.Enqueue(ctx, f.tenant, f.acct, testMessage("x"), window, "")
		require.ErrorIs(t, err, context.Canceled, "window %v", window)
	}

	require.Zero(t, f.sender.count())
	db := f.openDB()
	defer func() { require.NoError(t, db.Close()) }()
	var rows int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM outbox`).Scan(&rows))
	require.Zero(t, rows)
}

// busyCode is an error with the Code method of a SQLite error. Code 5 is
// SQLITE_BUSY and code 6 is SQLITE_LOCKED.
type busyCode struct{ code int }

func (e busyCode) Error() string { return "database is locked" }
func (e busyCode) Code() int     { return e.code }

// resultFaultStore makes the first calls of each result method fail with err. It
// counts the calls of each method.
type resultFaultStore struct {
	OutboxStore
	err      error
	failures int // how many calls of each method fail

	mu    sync.Mutex
	calls map[string]int
}

func newResultFaultStore(inner OutboxStore, err error, failures int) *resultFaultStore {
	return &resultFaultStore{OutboxStore: inner, err: err, failures: failures, calls: make(map[string]int)}
}

// fault counts a call of method and returns the injected error while there are
// failures left.
func (s *resultFaultStore) fault(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[method]++
	if s.calls[method] <= s.failures {
		return fmt.Errorf("store: %s: %w", method, s.err)
	}
	return nil
}

func (s *resultFaultStore) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *resultFaultStore) FinishOutboxSent(ctx context.Context, tenant account.TenantID, id string, now time.Time) error {
	if err := s.fault("finish"); err != nil {
		return err
	}
	return s.OutboxStore.FinishOutboxSent(ctx, tenant, id, now)
}

func (s *resultFaultStore) FailOutbox(ctx context.Context, tenant account.TenantID, id, errText string) error {
	if err := s.fault("fail"); err != nil {
		return err
	}
	return s.OutboxStore.FailOutbox(ctx, tenant, id, errText)
}

func (s *resultFaultStore) RetryOutbox(ctx context.Context, tenant account.TenantID, id string, next time.Time, errText string) error {
	if err := s.fault("retry"); err != nil {
		return err
	}
	return s.OutboxStore.RetryOutbox(ctx, tenant, id, next, errText)
}

func (s *resultFaultStore) ReleaseOutbox(ctx context.Context, tenant account.TenantID, id string) error {
	if err := s.fault("release"); err != nil {
		return err
	}
	return s.OutboxStore.ReleaseOutbox(ctx, tenant, id)
}

// resultCase is one way for a claimed item to end, and the store method that
// writes it.
type resultCase struct {
	method string
	// sendFn is the result of the sender.
	sendFn func(ctx context.Context, n int) error
	// stop is true for the case where the worker stops between claim and send.
	stop   bool
	status string // the state of the item when the write works
}

func resultCases() map[string]resultCase {
	return map[string]resultCase{
		"sent": {method: "finish", status: "sent"},
		"failed for good": {
			method: "fail", status: "failed",
			sendFn: func(context.Context, int) error { return Permanent(errors.New("550 refused")) },
		},
		"failed, try again later": {
			method: "retry", status: "queued",
			sendFn: func(context.Context, int) error { return errors.New("connection refused") },
		},
		"released": {method: "release", status: "queued", stop: true},
	}
}

// runResultCase makes the item of the case end, with a store that fails some
// result calls. It returns the fault store and the id of the item.
func (f *fixture) runResultCase(c resultCase, err error, failures int) (*resultFaultStore, string) {
	f.t.Helper()
	item := f.enqueue("result write", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	f.sender.setFn(c.sendFn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var inner OutboxStore = f.store
	if c.stop {
		inner = &releasingStore{Store: f.store, cancel: cancel}
	}
	faults := newResultFaultStore(inner, err, failures)
	ob := New(faults, f.vault, f.sender.send, f.clock.Now)
	ob.resultBackoff = time.Millisecond
	_ = ob.Drain(ctx, 0)
	return faults, item.ID
}

// A result write that finds the database busy is tried again a few times. The
// outcome of the send is known, so the item must not stay in "sending".
func TestResultWriteIsTriedAgainWhenTheDatabaseIsBusy(t *testing.T) {
	for name, c := range resultCases() {
		for _, code := range []int{5, 6, 261, 517} { // BUSY, LOCKED, BUSY_RECOVERY, BUSY_SNAPSHOT
			t.Run(fmt.Sprintf("%s/code %d", name, code), func(t *testing.T) {
				f := newFixture(t)

				faults, id := f.runResultCase(c, busyCode{code}, 2)

				require.Equal(t, 3, faults.count(c.method), "two busy answers, then it works")
				require.Equal(t, c.status, f.raw(id).status)
			})
		}
	}
}

// The tries end after a few attempts. The item then stays "sending", and the
// recovery fails it later. It is never sent again.
func TestResultWriteGivesUpWhenTheDatabaseStaysBusy(t *testing.T) {
	f := newFixture(t)

	faults, id := f.runResultCase(resultCases()["sent"], busyCode{5}, 1000)

	require.Equal(t, resultAttempts, faults.count("finish"))
	require.Equal(t, "sending", f.raw(id).status)
	require.Equal(t, 1, f.sender.count())
}

// Only a busy database is tried again. A write that finds the item in another
// state (a miss of the compare-and-set) or that fails for another reason is
// tried once.
func TestResultWriteIsNotTriedAgainForOtherErrors(t *testing.T) {
	for name, err := range map[string]error{
		"a miss of the compare-and-set":   store.ErrNotFound,
		"another database error":          errors.New("disk I/O error"),
		"a SQLite error that is not busy": busyCode{1},
		"the context":                     context.Canceled,
	} {
		for caseName, c := range resultCases() {
			t.Run(name+"/"+caseName, func(t *testing.T) {
				f := newFixture(t)

				faults, id := f.runResultCase(c, err, 1000)

				require.Equal(t, 1, faults.count(c.method))
				require.Equal(t, "sending", f.raw(id).status, "nothing changed the item")
			})
		}
	}
}

// otherWorkerStore claims every new queued item at once, as a running worker
// could do between the insert and the claim of the caller. It leaves the item
// "sending", as a worker in the middle of a send does.
type otherWorkerStore struct {
	*store.Store
	now func() time.Time
}

func (s *otherWorkerStore) CreateOutbox(ctx context.Context, row *store.OutboxRow, sealed []byte, keyVersion int) error {
	if err := s.Store.CreateOutbox(ctx, row, sealed, keyVersion); err != nil {
		return err
	}
	_, err := s.ClaimOutbox(ctx, row.Tenant, row.ID, s.now())
	return err
}

// A window-0 item is never "queued": no worker can take it before the caller
// sends it. Enqueue returns "sent", not "sending".
func TestWindowZeroItemCannotBeClaimedByAWorker(t *testing.T) {
	f := newFixture(t)
	ob := New(&otherWorkerStore{Store: f.store, now: f.clock.Now}, f.vault, f.sender.send, f.clock.Now)

	item, err := ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("window zero"), 0, "")
	require.NoError(t, err)

	require.Equal(t, StatusSent, item.Status)
	require.Equal(t, 1, f.sender.count())
}

// countingStore counts the calls that create or claim an item.
type countingStore struct {
	*store.Store
	creates, claims atomic.Int32
}

func (s *countingStore) CreateOutbox(ctx context.Context, row *store.OutboxRow, sealed []byte, keyVersion int) error {
	s.creates.Add(1)
	return s.Store.CreateOutbox(ctx, row, sealed, keyVersion)
}

func (s *countingStore) ClaimOutbox(ctx context.Context, tenant account.TenantID, id string, now time.Time) (*store.OutboxRow, error) {
	s.claims.Add(1)
	return s.Store.ClaimOutbox(ctx, tenant, id, now)
}

// The row of a window-0 item is stored as "sending", with the claim time, in one
// insert. There is no claim afterwards, and while the sender runs a worker does
// not see the item as due.
func TestWindowZeroRowIsInsertedAsSending(t *testing.T) {
	f := newFixture(t)
	counting := &countingStore{Store: f.store}
	ob := New(counting, f.vault, f.sender.send, f.clock.Now)
	var (
		status    string
		claimedAt sql.NullInt64
		due       []*store.OutboxRow
	)
	f.sender.setFn(func(ctx context.Context, _ int) error {
		db := f.openDB()
		defer func() { _ = db.Close() }()
		if err := db.QueryRowContext(ctx, `SELECT status, claimed_at FROM outbox`).Scan(&status, &claimedAt); err != nil {
			return err
		}
		var err error
		due, err = f.store.DueOutbox(ctx, f.clock.Now(), 10)
		return err
	})

	item, err := ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("window zero"), 0, "")
	require.NoError(t, err)

	require.Equal(t, StatusSent, item.Status)
	require.Equal(t, "sending", status)
	require.True(t, claimedAt.Valid)
	require.Equal(t, testStart.UnixMilli(), claimedAt.Int64)
	require.Empty(t, due, "a worker must not see the item as due")
	require.Zero(t, counting.creates.Load(), "the row is not first stored as queued")
	require.Zero(t, counting.claims.Load(), "there is no separate claim")

	// A window above 0 still goes through the queue.
	_, err = ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage("window"), time.Minute, "")
	require.NoError(t, err)
	require.Equal(t, int32(1), counting.creates.Load())
}

// Many window-0 sends run while workers run. Each Enqueue returns "sent" (never
// "sending" or "queued"), and each message is sent exactly once.
func TestWindowZeroEnqueuesWithRunningWorkersSendEachMessageOnce(t *testing.T) {
	f := newFixture(t)
	f.run(f.newOutbox(), 100*time.Microsecond)
	f.run(f.newOutbox(), 100*time.Microsecond)

	const messages = 60
	statuses := make([]Status, messages)
	var wg sync.WaitGroup
	for i := range messages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := f.ob.Enqueue(context.Background(), f.tenant, f.acct, testMessage(fmt.Sprintf("body-%02d", i)), 0, "")
			if err == nil {
				statuses[i] = item.Status
			}
		}()
	}
	wg.Wait()

	for i, status := range statuses {
		require.Equal(t, StatusSent, status, "message %d", i)
	}
	// Let a late duplicate show itself before we count.
	time.Sleep(50 * time.Millisecond)
	seen := make(map[string]int)
	for _, body := range f.sender.bodies() {
		seen[body]++
	}
	require.Len(t, seen, messages)
	for body, n := range seen {
		require.Equal(t, 1, n, "%q was sent %d times", body, n)
	}
}

// ---- Fix round 1: inserts, the send deadline, window 0 and a stop ----

// insertThenCancelStore commits the insert of a new item and then ends the
// context of the caller. With reportError it also returns the error of the
// context, as the driver does when the context ends after the INSERT ran and
// before the answer came back: the row is committed and the call says it
// failed.
type insertThenCancelStore struct {
	*store.Store
	cancel      context.CancelFunc
	reportError bool
}

func (s *insertThenCancelStore) CreateOutbox(ctx context.Context, row *store.OutboxRow, sealed []byte, keyVersion int) error {
	err := s.Store.CreateOutbox(context.WithoutCancel(ctx), row, sealed, keyVersion)
	return s.after(ctx, err)
}

func (s *insertThenCancelStore) CreateOutboxSending(ctx context.Context, row *store.OutboxRow, sealed []byte, keyVersion int, claimedAt time.Time) error {
	err := s.Store.CreateOutboxSending(context.WithoutCancel(ctx), row, sealed, keyVersion, claimedAt)
	return s.after(ctx, err)
}

func (s *insertThenCancelStore) after(ctx context.Context, err error) error {
	s.cancel()
	if s.reportError {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return err
}

// rowCount returns the number of items in the table.
func (f *fixture) rowCount() int {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	var n int
	require.NoError(f.t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n))
	return n
}

// If the context ends while the insert runs, the row may be committed. Enqueue
// must not return an error then: the caller would queue the message again, and
// the user would get two copies. Both inserts use a context that the caller
// cannot cancel.
func TestEnqueueKeepsAnItemWhenTheContextEndsDuringTheInsert(t *testing.T) {
	for name, window := range map[string]time.Duration{"with a window": time.Minute, "window 0": 0} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ob := New(&insertThenCancelStore{Store: f.store, cancel: cancel, reportError: true}, f.vault, f.sender.send, f.clock.Now)

			item, err := ob.Enqueue(ctx, f.tenant, f.acct, testMessage("one copy"), window, "")

			require.NoError(t, err)
			require.NotNil(t, item)
			require.Equal(t, 1, f.rowCount(), "exactly one item is stored")
			require.Equal(t, StatusQueued, item.Status, "queued, or released to the queue before the send")
			require.Zero(t, f.sender.count())
		})
	}
}

// With window 0 the caller may stop between the insert and the start of the
// send. Nothing was sent. The item is released to the queue and a worker sends
// it later, once. (This is why Enqueue does not promise "sent" for window 0.)
func TestWindowZeroItemIsReleasedWhenTheContextEndsBeforeTheSend(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ob := New(&insertThenCancelStore{Store: f.store, cancel: cancel}, f.vault, f.sender.send, f.clock.Now)

	item, err := ob.Enqueue(ctx, f.tenant, f.acct, testMessage("not yet"), 0, "")
	require.NoError(t, err)

	require.Equal(t, StatusQueued, item.Status)
	require.Zero(t, f.sender.count())
	r := f.raw(item.ID)
	require.Equal(t, "queued", r.status)
	require.Equal(t, 0, r.attempts, "a release is not a failed try")
	require.False(t, r.messageNull)

	f.pass()
	require.Equal(t, 1, f.sender.count())
	require.Equal(t, StatusSent, f.status(item.ID))
	f.pass()
	require.Equal(t, 1, f.sender.count(), "sent once")
}

// slowOpenStore moves the clock when the stored message is read, as a slow key
// service or a slow disk would.
type slowOpenStore struct {
	*store.Store
	clock *clock
	delay time.Duration
}

func (s *slowOpenStore) GetOutboxMessage(ctx context.Context, tenant account.TenantID, id string) (int, []byte, error) {
	s.clock.Advance(s.delay)
	return s.Store.GetOutboxMessage(ctx, tenant, id)
}

// The deadline of the Sender counts from the claim, not from the start of the
// send. The steps before the send (reading and opening the message) use up a
// part of it. Without this, claim + open + send + result could pass the 15
// minutes after which another program fails the item.
func TestSendDeadlineCountsFromTheClaim(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("slow to open", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	var deadline time.Time
	var hasDeadline bool
	f.sender.setFn(func(ctx context.Context, _ int) error {
		deadline, hasDeadline = ctx.Deadline()
		return nil
	})
	ob := New(&slowOpenStore{Store: f.store, clock: f.clock, delay: 2 * time.Minute}, f.vault, f.sender.send, f.clock.Now)

	require.NoError(t, ob.Drain(context.Background(), 0))

	require.True(t, hasDeadline)
	require.Equal(t, StatusSent, f.status(item.ID))
	// The open step took 2 minutes of the clock. 13 - 2 = 11 minutes are left.
	left := time.Until(deadline)
	require.Greater(t, left, 11*time.Minute-10*time.Second)
	require.LessOrEqual(t, left, 11*time.Minute)
}

// If the steps before the send have used the whole time, nothing is sent: a send
// with a deadline that is over would fail at once and mark an unsent message as
// "delivery unknown". The item goes back to the queue instead.
func TestNoSendWhenTheTimeIsUsedUpBeforeTheSend(t *testing.T) {
	f := newFixture(t)
	item := f.enqueue("far too slow", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	slow := New(&slowOpenStore{Store: f.store, clock: f.clock, delay: sendDeadline + time.Second}, f.vault, f.sender.send, f.clock.Now)

	require.NoError(t, slow.Drain(context.Background(), 0))

	require.Zero(t, f.sender.count())
	r := f.raw(item.ID)
	require.Equal(t, "queued", r.status)
	require.Equal(t, 0, r.attempts)
	require.False(t, r.messageNull)

	f.pass()
	require.Equal(t, 1, f.sender.count())
	require.Equal(t, StatusSent, f.status(item.ID))
}

// ---- Fix round 2: a minimum time budget for the send ----

// A send needs a minimum of time. If less than minSendBudget is left after the
// steps before the send, nothing is sent and the item is released. A tiny rest
// (for example after a jump of the wall clock) would make the Sender fail at
// once, and a message that was never sent would be marked "delivery unknown".
func TestNoSendWhenLessThanTheMinimumBudgetIsLeft(t *testing.T) {
	require.Equal(t, 30*time.Second, minSendBudget)

	for name, c := range map[string]struct {
		left     time.Duration
		wantSent bool
	}{
		"1 millisecond left":           {left: time.Millisecond},
		"one millisecond under budget": {left: minSendBudget - time.Millisecond},
		"exactly the budget":           {left: minSendBudget, wantSent: true},
		"plenty":                       {left: 5 * time.Minute, wantSent: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			item := f.enqueue("short of time", 10*time.Second)
			f.clock.Advance(10 * time.Second)
			// The open step uses sendDeadline - left of the clock.
			ob := New(&slowOpenStore{Store: f.store, clock: f.clock, delay: sendDeadline - c.left}, f.vault, f.sender.send, f.clock.Now)

			require.NoError(t, ob.Drain(context.Background(), 0))

			if c.wantSent {
				require.Equal(t, 1, f.sender.count())
				require.Equal(t, StatusSent, f.status(item.ID))
				return
			}
			require.Zero(t, f.sender.count(), "the Sender is not called")
			r := f.raw(item.ID)
			require.Equal(t, "queued", r.status, "released, not failed")
			require.Equal(t, 0, r.attempts, "a release is not a failed try")
			require.Empty(t, r.errText)
			require.False(t, r.messageNull)

			f.pass()
			require.Equal(t, 1, f.sender.count())
			require.Equal(t, StatusSent, f.status(item.ID))
		})
	}
}

// ---- Parallel sends (review of PR #3, item 6) ----

// gatedSender is a Sender that can hold the sends of an account until the test
// releases them. It counts the sends in progress, in all and for each account,
// and keeps the highest counts. It records the body of each send by account.
type gatedSender struct {
	mu          sync.Mutex
	gates       map[account.AccountID]chan struct{}
	inFlight    int
	peak        int
	accountNow  map[account.AccountID]int
	accountPeak map[account.AccountID]int
	bodies      map[account.AccountID][]string
	finished    int
	delay       time.Duration
}

func newGatedSender() *gatedSender {
	return &gatedSender{
		gates:       map[account.AccountID]chan struct{}{},
		accountNow:  map[account.AccountID]int{},
		accountPeak: map[account.AccountID]int{},
		bodies:      map[account.AccountID][]string{},
	}
}

// hold makes the sends of the account wait until release is called. release
// may be called more than once.
func (s *gatedSender) hold(acct account.AccountID) (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.gates[acct] = gate
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (s *gatedSender) send(_ context.Context, _ account.TenantID, acct account.AccountID, msg *mail.RawMessage, _ mail.Ref) error {
	s.mu.Lock()
	s.inFlight++
	s.peak = max(s.peak, s.inFlight)
	s.accountNow[acct]++
	s.accountPeak[acct] = max(s.accountPeak[acct], s.accountNow[acct])
	gate, delay := s.gates[acct], s.delay
	s.mu.Unlock()

	if gate != nil {
		<-gate
	}
	if delay > 0 {
		time.Sleep(delay)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	s.accountNow[acct]--
	s.finished++
	_, body, _ := strings.Cut(string(msg.Data), "\r\n\r\n")
	s.bodies[acct] = append(s.bodies[acct], body)
	return nil
}

// counts returns the sends in progress now, the highest number at once, and
// the number of sends that have finished.
func (s *gatedSender) counts() (now, peak, finished int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight, s.peak, s.finished
}

// inFlightFor returns the number of sends of the account in progress now.
func (s *gatedSender) inFlightFor(acct account.AccountID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accountNow[acct]
}

// highestForOneAccount returns the highest number of sends of one account that
// were in progress at once.
func (s *gatedSender) highestForOneAccount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	highest := 0
	for _, n := range s.accountPeak {
		highest = max(highest, n)
	}
	return highest
}

// addAccount adds an account to the tenant of the fixture.
func (f *fixture) addAccount(address string) account.AccountID {
	f.t.Helper()
	a := account.NewIMAPAccount(f.tenant, address, account.MailServer{}, testStart)
	require.NoError(f.t, f.store.CreateAccount(context.Background(), a))
	return a.ID
}

// enqueueFor queues a message in an account of the fixture tenant.
func (f *fixture) enqueueFor(acct account.AccountID, body string, window time.Duration) *Item {
	f.t.Helper()
	item, err := f.ob.Enqueue(context.Background(), f.tenant, acct, testMessage(body), window, "")
	require.NoError(f.t, err)
	return item
}

// drainAsync starts Drain with no waiting in a goroutine. The channel gets its
// result.
func drainAsync(ctx context.Context, ob *Outbox) <-chan error {
	done := make(chan error, 1)
	go func() { done <- ob.Drain(ctx, 0) }()
	return done
}

// A stalled mail server of one account must not hold up the mail of another
// account: the due message of account B is sent while the send of account A
// waits.
func TestSendDueSendsAnotherAccountWhileOneIsStalled(t *testing.T) {
	f := newFixture(t)
	acctB := f.addAccount("home@example.com")
	gs := newGatedSender()
	releaseA := gs.hold(f.acct)
	t.Cleanup(releaseA)
	ob := New(f.store, f.vault, gs.send, f.clock.Now)

	a := f.enqueueFor(f.acct, "for A", 5*time.Second) // due first
	b := f.enqueueFor(acctB, "for B", 10*time.Second)
	f.clock.Advance(10 * time.Second)
	done := drainAsync(context.Background(), ob)

	require.Eventually(t, func() bool { return gs.inFlightFor(f.acct) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the send of A starts and is held")
	require.Eventually(t, func() bool { return f.status(b.ID) == StatusSent }, 5*time.Second, 5*time.Millisecond,
		"the message of B is sent while the send of A is held")
	require.Equal(t, StatusSending, f.status(a.ID))
	require.Equal(t, 1, gs.inFlightFor(f.acct), "A is still held")
	select {
	case err := <-done:
		t.Fatalf("sendDue returned while a send that it started was running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseA()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Drain did not return")
	}
	require.Equal(t, StatusSent, f.status(a.ID))
}

// At most maxParallelSends sends run at once, and only the items that are
// being sent are claimed: the others stay "queued" until their own send
// starts. A context that ends stops new claims, and the sends that started
// finish first.
func TestSendDueCapsTheSendsInProgress(t *testing.T) {
	f := newFixture(t)
	gs := newGatedSender()
	accounts := []account.AccountID{f.acct}
	for i := range 5 {
		accounts = append(accounts, f.addAccount(fmt.Sprintf("a%d@example.com", i)))
	}
	var releases []func()
	for _, acct := range accounts {
		releases = append(releases, gs.hold(acct))
	}
	releaseAll := func() {
		for _, r := range releases {
			r()
		}
	}
	t.Cleanup(releaseAll)
	ob := New(f.store, f.vault, gs.send, f.clock.Now)

	var ids []string
	for i, acct := range accounts {
		for j := range 2 {
			ids = append(ids, f.enqueueFor(acct, fmt.Sprintf("body %d-%d", i, j), 10*time.Second).ID)
		}
	}
	f.clock.Advance(10 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := drainAsync(ctx, ob)

	require.Eventually(t, func() bool {
		now, _, _ := gs.counts()
		return now == maxParallelSends
	}, 5*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // a fifth send would start now
	now, peak, _ := gs.counts()
	require.Equal(t, maxParallelSends, now)
	require.Equal(t, maxParallelSends, peak, "never more than the cap")

	statuses := map[Status]int{}
	for _, id := range ids {
		statuses[f.status(id)]++
	}
	require.Equal(t, map[Status]int{StatusSending: maxParallelSends, StatusQueued: len(ids) - maxParallelSends}, statuses,
		"an item is claimed right before its send, never early")

	// The end of the context stops new claims. The held sends finish first.
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Drain returned while sends were running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseAll()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Drain did not return")
	}
	_, peak, finished := gs.counts()
	require.Equal(t, maxParallelSends, finished, "no send starts after the context ended")
	require.Equal(t, maxParallelSends, peak)
	require.Equal(t, 1, gs.highestForOneAccount())
	statuses = map[Status]int{}
	for _, id := range ids {
		statuses[f.status(id)]++
	}
	require.Equal(t, map[Status]int{StatusSent: maxParallelSends, StatusQueued: len(ids) - maxParallelSends}, statuses)
}

// failClaimStore fails the claim of one item with a database error. The claim
// waits until ready is closed, so a test can choose when it fails.
type failClaimStore struct {
	*store.Store
	failID string
	ready  chan struct{}
	err    error
}

func (s *failClaimStore) ClaimOutbox(ctx context.Context, tenant account.TenantID, id string, now time.Time) (*store.OutboxRow, error) {
	if id == s.failID {
		<-s.ready
		return nil, s.err
	}
	return s.Store.ClaimOutbox(ctx, tenant, id, now)
}

// A database error of a claim in one account stops new claims in every
// account. A send that has started in another account still finishes, and
// sendDue returns the error only after it.
func TestSendDueStopsAllClaimsWhenAClaimFails(t *testing.T) {
	f := newFixture(t)
	acctB := f.addAccount("home@example.com")
	gs := newGatedSender()
	releaseA := gs.hold(f.acct)
	t.Cleanup(releaseA)
	dbErr := errors.New("database is broken")
	failing := &failClaimStore{Store: f.store, ready: make(chan struct{}), err: dbErr}
	var once sync.Once
	letClaimFail := func() { once.Do(func() { close(failing.ready) }) }
	t.Cleanup(letClaimFail)
	ob := New(failing, f.vault, gs.send, f.clock.Now)

	a1 := f.enqueueFor(f.acct, "first for A", 5*time.Second)
	a2 := f.enqueueFor(f.acct, "second for A", 6*time.Second)
	b := f.enqueueFor(acctB, "for B", 7*time.Second)
	failing.failID = b.ID
	f.clock.Advance(10 * time.Second)
	done := drainAsync(context.Background(), ob)

	require.Eventually(t, func() bool { return gs.inFlightFor(f.acct) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the first send of A starts and is held")
	letClaimFail()
	select {
	case err := <-done:
		t.Fatalf("sendDue returned while a send that it started was running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseA()
	select {
	case err := <-done:
		require.ErrorIs(t, err, dbErr)
	case <-time.After(10 * time.Second):
		t.Fatal("Drain did not return")
	}
	require.Equal(t, StatusSent, f.status(a1.ID), "the send that started finished")
	require.Equal(t, StatusQueued, f.status(a2.ID), "no claim after the failed claim")
	require.Equal(t, StatusQueued, f.status(b.ID))
	_, _, finished := gs.counts()
	require.Equal(t, 1, finished)
}

// Two items of one account are never sent at the same time, and they go out
// in their order. Accounts send in parallel up to the cap.
func TestSendDueSendsOneAtATimeForEachAccount(t *testing.T) {
	f := newFixture(t)
	gs := newGatedSender()
	gs.delay = 5 * time.Millisecond
	accounts := []account.AccountID{f.acct, f.addAccount("b@example.com"), f.addAccount("c@example.com")}
	ob := New(f.store, f.vault, gs.send, f.clock.Now)
	for j := range 4 {
		for i, acct := range accounts {
			f.enqueueFor(acct, fmt.Sprintf("%d-%d", i, j), time.Duration(j+1)*time.Second)
		}
	}
	f.clock.Advance(time.Minute)

	require.NoError(t, ob.Drain(context.Background(), 0))

	_, peak, finished := gs.counts()
	require.Equal(t, 12, finished)
	require.Equal(t, 1, gs.highestForOneAccount(), "one send at a time for each account")
	require.LessOrEqual(t, peak, maxParallelSends)
	for i, acct := range accounts {
		gs.mu.Lock()
		got := slices.Clone(gs.bodies[acct])
		gs.mu.Unlock()
		want := []string{fmt.Sprintf("%d-0", i), fmt.Sprintf("%d-1", i), fmt.Sprintf("%d-2", i), fmt.Sprintf("%d-3", i)}
		require.Equal(t, want, got, "the mail of one account goes out in its order")
	}
}
