package policy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// clock is a time source that a test can move. It is safe for use by many
// goroutines.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *clock { return &clock{t: t} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// testDay is midnight UTC on the day that most tests use.
var testDay = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

// openStore opens a store in dir and closes it when the test ends.
func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// newAccountInStore creates one account in the local tenant of s.
func newAccountInStore(t *testing.T, s *store.Store, id account.AccountID) (account.TenantID, account.AccountID) {
	t.Helper()
	ctx := context.Background()
	tenant, err := s.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	a := account.NewIMAPAccount(tenant, string(id)+"@example.com", account.MailServer{}, testDay)
	a.ID = id
	require.NoError(t, s.CreateAccount(ctx, a))
	return tenant, id
}

// limiterFixture is a limiter on a real store with one account.
type limiterFixture struct {
	limiter *Limiter
	clock   *clock
	store   *store.Store
	tenant  account.TenantID
	acct    account.AccountID
}

// newFixture returns a limiter whose clock starts at start.
func newFixture(t *testing.T, start time.Time) *limiterFixture {
	t.Helper()
	s := openStore(t, t.TempDir())
	tenant, acct := newAccountInStore(t, s, "a1")
	c := newClock(start)
	return &limiterFixture{
		limiter: &Limiter{Store: s, Now: c.Now},
		clock:   c,
		store:   s,
		tenant:  tenant,
		acct:    acct,
	}
}

func (f *limiterFixture) reserve(kind CounterKind, n int, lim account.Limits) error {
	return f.limiter.Reserve(context.Background(), f.tenant, f.acct, kind, n, lim)
}

// requireRateLimit checks that err is a *RateLimitError and returns it.
func requireRateLimit(t *testing.T, err error) *RateLimitError {
	t.Helper()
	var rl *RateLimitError
	require.ErrorAs(t, err, &rl)
	return rl
}

// requireRefused checks that err is a *RateLimitError. Use it when the test does
// not look at the error.
func requireRefused(t *testing.T, err error) {
	t.Helper()
	_ = requireRateLimit(t, err)
}

func TestLimiterHourly(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 30, 15, 0, time.UTC)
	f := newFixture(t, start)
	lim := account.DefaultLimits()

	for i := 1; i <= 20; i++ {
		require.NoError(t, f.reserve(CountSend, 1, lim), "send %d", i)
	}
	err := f.reserve(CountSend, 1, lim)

	rl := requireRateLimit(t, err)
	require.Equal(t, CountSend, rl.Kind)
	// The hour window ends at 10:00:00. The clock says 09:30:15.
	require.Equal(t, 29*time.Minute+45*time.Second, rl.RetryAfter)

	// The refused send was not counted, and a new hour starts with a new count.
	f.clock.Set(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC))
	for i := 1; i <= 20; i++ {
		require.NoError(t, f.reserve(CountSend, 1, lim), "send %d in the next hour", i)
	}
	requireRefused(t, f.reserve(CountSend, 1, lim))
}

func TestLimiterHourWindowChangesOnTheHour(t *testing.T) {
	f := newFixture(t, time.Date(2026, 10, 27, 9, 59, 59, 0, time.UTC))
	lim := account.Limits{SendsPerHour: 1, SendsPerDay: 100}

	require.NoError(t, f.reserve(CountSend, 1, lim))
	rl := requireRateLimit(t, f.reserve(CountSend, 1, lim))
	require.Equal(t, time.Second, rl.RetryAfter)

	f.clock.Set(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC))
	require.NoError(t, f.reserve(CountSend, 1, lim))
}

func TestLimiterWindowsAreInUTC(t *testing.T) {
	// 23:30 in UTC-5 is 04:30 UTC on the next day. The windows follow UTC, not
	// the zone of the clock.
	zone := time.FixedZone("UTC-5", -5*60*60)
	f := newFixture(t, time.Date(2026, 10, 27, 23, 30, 0, 0, zone))
	lim := account.Limits{SendsPerHour: 1, SendsPerDay: 1}

	require.NoError(t, f.reserve(CountSend, 1, lim))
	rl := requireRateLimit(t, f.reserve(CountSend, 1, lim))

	// The day ends at 00:00 UTC, which is 19:30 away from 04:30 UTC.
	require.Equal(t, 19*time.Hour+30*time.Minute, rl.RetryAfter)
}

func TestLimiterDaily(t *testing.T) {
	f := newFixture(t, testDay)
	lim := account.DefaultLimits() // 20 per hour, 200 per day

	// 10 hours with 20 sends each use the whole day.
	for hour := range 10 {
		f.clock.Set(testDay.Add(time.Duration(hour)*time.Hour + 5*time.Minute))
		for i := 1; i <= 20; i++ {
			require.NoError(t, f.reserve(CountSend, 1, lim), "hour %d send %d", hour, i)
		}
	}

	// Hour 10 has room, but the day does not.
	f.clock.Set(testDay.Add(10*time.Hour + 5*time.Minute))
	rl := requireRateLimit(t, f.reserve(CountSend, 1, lim))
	require.Equal(t, CountSend, rl.Kind)
	require.Equal(t, 13*time.Hour+55*time.Minute, rl.RetryAfter, "waits for the next day")

	// The next day starts at 00:00 UTC.
	f.clock.Set(testDay.Add(24 * time.Hour))
	require.NoError(t, f.reserve(CountSend, 1, lim))
}

func TestLimiterWhenBothWindowsAreFullTheLaterResetWins(t *testing.T) {
	f := newFixture(t, testDay.Add(9*time.Hour+30*time.Minute))
	lim := account.Limits{SendsPerHour: 2, SendsPerDay: 2}

	require.NoError(t, f.reserve(CountSend, 2, lim))
	rl := requireRateLimit(t, f.reserve(CountSend, 1, lim))

	// The hour resets in 30 minutes. The day resets in 14 hours 30 minutes.
	require.Equal(t, 14*time.Hour+30*time.Minute, rl.RetryAfter)
}

func TestLimiterRefusedReserveCountsNothing(t *testing.T) {
	f := newFixture(t, testDay.Add(time.Hour))
	lim := account.Limits{SendsPerHour: 4, SendsPerDay: 6}

	require.NoError(t, f.reserve(CountSend, 4, lim))
	// In the next hour the hour counter is empty, but the day has room for only
	// 2 more. A batch of 3 is refused, and it must not raise the hour counter.
	f.clock.Set(testDay.Add(2 * time.Hour))
	requireRefused(t, f.reserve(CountSend, 3, lim))

	// If the hour counter had gone up to 3, this batch of 2 would fail.
	require.NoError(t, f.reserve(CountSend, 2, lim))
	requireRefused(t, f.reserve(CountSend, 1, lim))
}

func TestLimiterBatchSizes(t *testing.T) {
	f := newFixture(t, testDay.Add(time.Hour))
	lim := account.DefaultLimits()

	require.NoError(t, f.reserve(CountSend, 15, lim))
	requireRefused(t, f.reserve(CountSend, 6, lim))
	require.NoError(t, f.reserve(CountSend, 5, lim), "15 + 5 is exactly the limit")
	requireRefused(t, f.reserve(CountSend, 1, lim))
}

func TestLimiterTrash(t *testing.T) {
	f := newFixture(t, testDay.Add(15*time.Hour))
	lim := account.DefaultLimits() // 500 per day

	require.NoError(t, f.reserve(CountTrash, 300, lim))
	rl := requireRateLimit(t, f.reserve(CountTrash, 201, lim))
	require.Equal(t, CountTrash, rl.Kind)
	require.Equal(t, 9*time.Hour, rl.RetryAfter)

	// The refused batch counted nothing.
	require.NoError(t, f.reserve(CountTrash, 200, lim))
	requireRefused(t, f.reserve(CountTrash, 1, lim))

	// The next day starts with an empty counter.
	f.clock.Set(testDay.Add(24 * time.Hour))
	require.NoError(t, f.reserve(CountTrash, 500, lim))
}

func TestLimiterKindsDoNotShareCounters(t *testing.T) {
	f := newFixture(t, testDay.Add(time.Hour))
	lim := account.Limits{SendsPerHour: 1, SendsPerDay: 1, TrashPerDay: 1, RecipientsPerMessage: 5}

	require.NoError(t, f.reserve(CountSend, 1, lim))
	requireRefused(t, f.reserve(CountSend, 1, lim))
	require.NoError(t, f.reserve(CountTrash, 1, lim))
	requireRefused(t, f.reserve(CountTrash, 1, lim))
}

func TestLimiterAccountsDoNotShareCounters(t *testing.T) {
	f := newFixture(t, testDay.Add(time.Hour))
	_, second := newAccountInStore(t, f.store, "a2")
	lim := account.Limits{SendsPerHour: 1, SendsPerDay: 1}

	require.NoError(t, f.reserve(CountSend, 1, lim))
	requireRefused(t, f.reserve(CountSend, 1, lim))
	require.NoError(t, f.limiter.Reserve(context.Background(), f.tenant, second, CountSend, 1, lim))
}

func TestLimitOfZeroAllowsNothing(t *testing.T) {
	f := newFixture(t, testDay.Add(time.Hour))

	requireRefused(t, f.reserve(CountSend, 1, account.Limits{SendsPerHour: 0, SendsPerDay: 10}))
	requireRefused(t, f.reserve(CountSend, 1, account.Limits{SendsPerHour: 10, SendsPerDay: 0}))
	requireRefused(t, f.reserve(CountTrash, 1, account.Limits{TrashPerDay: 0}))
	rl := requireRateLimit(t, f.reserve(CountRecipients, 1, account.Limits{RecipientsPerMessage: 0}))
	require.Equal(t, CountRecipients, rl.Kind)
}

// countingStore is a CounterStore that records its calls.
type countingStore struct{ calls atomic.Int32 }

func (c *countingStore) ReserveCounters(context.Context, account.TenantID, account.AccountID, []store.CounterCheck, int, time.Time) ([]int, error) {
	c.calls.Add(1)
	return nil, nil
}

func TestRecipientsCap(t *testing.T) {
	cs := &countingStore{}
	l := &Limiter{Store: cs, Now: newClock(testDay).Now}
	lim := account.DefaultLimits() // 50 recipients per message
	ctx := context.Background()

	require.NoError(t, l.Reserve(ctx, "local", "a1", CountRecipients, 1, lim))
	require.NoError(t, l.Reserve(ctx, "local", "a1", CountRecipients, 50, lim))
	err := l.Reserve(ctx, "local", "a1", CountRecipients, 51, lim)

	rl := requireRateLimit(t, err)
	require.Equal(t, CountRecipients, rl.Kind)
	require.Zero(t, rl.RetryAfter, "waiting does not help")
	require.Zero(t, cs.calls.Load(), "recipients are checked, never counted")
}

func TestRecipientsCapRepeatedCallsDoNotAddUp(t *testing.T) {
	f := newFixture(t, testDay)
	lim := account.DefaultLimits()

	for range 5 {
		require.NoError(t, f.reserve(CountRecipients, 50, lim))
	}
}

func TestLimiterRejectsBadInput(t *testing.T) {
	cs := &countingStore{}
	l := &Limiter{Store: cs, Now: newClock(testDay).Now}
	lim := account.DefaultLimits()
	ctx := context.Background()

	err := l.Reserve(ctx, "local", "a1", CounterKind("bogus"), 1, lim)
	require.Error(t, err)
	require.NotErrorAs(t, err, new(*RateLimitError))

	// A negative count must never lower a counter.
	for _, kind := range []CounterKind{CountSend, CountTrash, CountRecipients} {
		err = l.Reserve(ctx, "local", "a1", kind, -1, lim)
		require.Error(t, err, kind)
		require.NotErrorAs(t, err, new(*RateLimitError))
	}
	require.Zero(t, cs.calls.Load())
}

func TestLimiterZeroCountChangesNothing(t *testing.T) {
	cs := &countingStore{}
	l := &Limiter{Store: cs, Now: newClock(testDay).Now}

	// Even with a limit of 0, nothing is asked for, so nothing is refused.
	require.NoError(t, l.Reserve(context.Background(), "local", "a1", CountSend, 0, account.Limits{}))
	require.NoError(t, l.Reserve(context.Background(), "local", "a1", CountTrash, 0, account.Limits{}))
	require.Zero(t, cs.calls.Load())
}

func TestLimiterFailsClosedWhenTheStoreFails(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	tenant, acct := newAccountInStore(t, s, "a1")
	require.NoError(t, s.Close())
	l := &Limiter{Store: s, Now: newClock(testDay).Now}

	err = l.Reserve(context.Background(), tenant, acct, CountSend, 1, account.DefaultLimits())

	require.Error(t, err, "a failed store must not look like permission")
	require.NotErrorAs(t, err, new(*RateLimitError))
}

func TestLimiterFailsForAnUnknownAccount(t *testing.T) {
	f := newFixture(t, testDay)

	err := f.limiter.Reserve(context.Background(), f.tenant, "missing", CountSend, 1, account.DefaultLimits())

	require.Error(t, err)
	require.NotErrorAs(t, err, new(*RateLimitError))
}

func TestLimiterUsesTheWallClockByDefault(t *testing.T) {
	s := openStore(t, t.TempDir())
	tenant, acct := newAccountInStore(t, s, "a1")
	l := &Limiter{Store: s} // no Now function
	lim := account.Limits{SendsPerHour: 1, SendsPerDay: 1}

	require.NoError(t, l.Reserve(context.Background(), tenant, acct, CountSend, 1, lim))
	rl := requireRateLimit(t, l.Reserve(context.Background(), tenant, acct, CountSend, 1, lim))

	// The day window ends at the next midnight UTC, so the wait is more than 0
	// and not more than 24 hours.
	require.Positive(t, rl.RetryAfter)
	require.LessOrEqual(t, rl.RetryAfter, 24*time.Hour)
}

func TestRateLimitErrorText(t *testing.T) {
	tests := []struct {
		name string
		err  *RateLimitError
		want string
	}{
		{"send", &RateLimitError{Kind: CountSend, RetryAfter: 29*time.Minute + 45*time.Second}, "Send limit reached. Try again in 30 minutes."},
		{"send exact minutes", &RateLimitError{Kind: CountSend, RetryAfter: 10 * time.Minute}, "Send limit reached. Try again in 10 minutes."},
		{"one minute", &RateLimitError{Kind: CountSend, RetryAfter: time.Minute}, "Send limit reached. Try again in 1 minute."},
		{"a short wait is one minute", &RateLimitError{Kind: CountSend, RetryAfter: time.Second}, "Send limit reached. Try again in 1 minute."},
		{"trash", &RateLimitError{Kind: CountTrash, RetryAfter: 9 * time.Hour}, "Trash limit reached. Try again in 540 minutes."},
		{"recipients", &RateLimitError{Kind: CountRecipients}, "Too many recipients in one message. Use fewer recipients."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestRateLimitErrorHasNoAccountAddress(t *testing.T) {
	f := newFixture(t, testDay.Add(time.Hour))
	require.NoError(t, f.reserve(CountSend, 1, account.Limits{SendsPerHour: 1, SendsPerDay: 1}))

	err := f.reserve(CountSend, 1, account.Limits{SendsPerHour: 1, SendsPerDay: 1})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "a1")
	require.NotContains(t, err.Error(), "example.com")
	require.NotContains(t, err.Error(), string(f.tenant))
}

// runConcurrentSends starts many goroutines. Each one makes one reserve call on
// one of the limiters. It returns how many succeeded.
func runConcurrentSends(t *testing.T, limiters []*Limiter, tenant account.TenantID, acct account.AccountID, lim account.Limits, calls int) int {
	t.Helper()
	var (
		wg        sync.WaitGroup
		succeeded atomic.Int32
		start     = make(chan struct{})
		errs      = make(chan error, calls)
	)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // let all goroutines begin together
			err := limiters[i%len(limiters)].Reserve(context.Background(), tenant, acct, CountSend, 1, lim)
			var rl *RateLimitError
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.As(err, &rl):
				// A refusal is a normal result here.
			default:
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	return int(succeeded.Load())
}

// The concurrency tests use a small hourly limit and three times as many calls
// as the limit. Every call needs the database write lock, and SQLite makes
// waiting callers poll for it. A small number of calls keeps the tests fast and
// far from the 5 second busy timeout, and still shows that no call gets in
// beyond the limit.
const (
	concurrentHourlyLimit = 10
	concurrentCalls       = 3 * concurrentHourlyLimit
)

func TestLimiterConcurrentReservesNeverPassTheHourlyLimit(t *testing.T) {
	now := newClock(testDay.Add(9*time.Hour + 30*time.Minute)).Now
	lim := account.Limits{SendsPerHour: concurrentHourlyLimit, SendsPerDay: 200}

	t.Run("many goroutines and two limiters on one store", func(t *testing.T) {
		s := openStore(t, t.TempDir())
		tenant, acct := newAccountInStore(t, s, "a1")
		limiters := []*Limiter{{Store: s, Now: now}, {Store: s, Now: now}}

		got := runConcurrentSends(t, limiters, tenant, acct, lim, concurrentCalls)

		require.Equal(t, concurrentHourlyLimit, got, "exactly the hourly limit is allowed")
	})

	t.Run("two store handles on one database file", func(t *testing.T) {
		// Two handles on one file behave like two programs. The database lock
		// must give the same result as a lock inside one program.
		dir := t.TempDir()
		first := openStore(t, dir)
		second := openStore(t, dir)
		tenant, acct := newAccountInStore(t, first, "a1")
		limiters := []*Limiter{{Store: first, Now: now}, {Store: second, Now: now}}

		got := runConcurrentSends(t, limiters, tenant, acct, lim, concurrentCalls)

		require.Equal(t, concurrentHourlyLimit, got, "exactly the hourly limit is allowed")
	})
}

func TestLimiterConcurrentBatchesNeverPassTheLimit(t *testing.T) {
	now := newClock(testDay.Add(9*time.Hour + 30*time.Minute)).Now
	s := openStore(t, t.TempDir())
	tenant, acct := newAccountInStore(t, s, "a1")
	l := &Limiter{Store: s, Now: now}
	lim := account.Limits{SendsPerHour: 10, SendsPerDay: 200}

	var (
		wg      sync.WaitGroup
		granted atomic.Int32
	)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := i%4 + 1 // batches of 1 to 4
			if err := l.Reserve(context.Background(), tenant, acct, CountSend, n, lim); err == nil {
				granted.Add(int32(n))
			} else {
				var rl *RateLimitError
				if !errors.As(err, &rl) {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, int(granted.Load()), 10)
	// Fill what is left. This works only if the refused batches left nothing
	// behind in the counter. Then the window must be exactly full.
	left := 10 - int(granted.Load())
	if left > 0 {
		require.NoError(t, l.Reserve(context.Background(), tenant, acct, CountSend, left, lim))
	}
	requireRefused(t, l.Reserve(context.Background(), tenant, acct, CountSend, 1, lim))
}
