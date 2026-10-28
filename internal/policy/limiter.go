package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// CounterKind is something the limiter counts or checks.
type CounterKind string

// The kinds of limit.
const (
	// CountSend counts sent messages. The hour limit and the day limit both apply.
	CountSend CounterKind = "send"
	// CountTrash counts messages moved to Trash. The day limit applies.
	CountTrash CounterKind = "trash"
	// CountRecipients is the cap on recipients in one message. It is checked and
	// never counted.
	CountRecipients CounterKind = "recipients"
)

// retention is how long a counter is kept. It is two days: the longest window is
// one day, so older counters are never read again.
const retention = 48 * time.Hour

// CounterStore keeps the counters. *store.Store implements it.
type CounterStore interface {
	// ReserveCounters adds n to every counter in checks if none would pass its
	// limit, and deletes the counters of this account that start before
	// pruneBefore. The check and the change are one atomic step. If a counter
	// would pass its limit, it changes no counter and returns the indexes in
	// checks of the counters that are too full.
	ReserveCounters(ctx context.Context, tenant account.TenantID, acct account.AccountID, checks []store.CounterCheck, n int, pruneBefore time.Time) (full []int, err error)
}

// Limiter enforces the send and trash limits of an account.
//
// The windows are fixed, not sliding. The hour window starts at the top of each
// hour in UTC. The day window starts at 00:00 UTC. A new window starts with a
// count of zero.
type Limiter struct {
	Store CounterStore
	// Now returns the current time. A nil Now means time.Now.
	Now func() time.Time
}

// window is one limit that applies to a kind: a counter name, the length of its
// window and the highest total allowed in it.
type window struct {
	counter string
	length  time.Duration
	limit   int
}

// windows returns the windows that apply to kind. It returns nil for a kind that
// has no counted windows.
func windows(kind CounterKind, lim account.Limits) []window {
	switch kind {
	case CountSend:
		return []window{
			{"send_hour", time.Hour, lim.SendsPerHour},
			{"send_day", 24 * time.Hour, lim.SendsPerDay},
		}
	case CountTrash:
		return []window{
			{"trash_day", 24 * time.Hour, lim.TrashPerDay},
		}
	default:
		return nil
	}
}

// Reserve counts n uses of kind against the limits lim of an account. It either
// counts all n and returns nil, or counts nothing and returns an error.
//
// For CountSend, the new total must stay within SendsPerHour for the current
// hour and within SendsPerDay for the current day. For CountTrash, it must stay
// within TrashPerDay for the current day. A limit of 0 allows nothing.
//
// CountRecipients only checks that n is not more than RecipientsPerMessage. It
// never counts and does not use the store.
//
// When a limit would be passed, Reserve returns a *RateLimitError. Its
// RetryAfter is the time until the full window ends. When both the hour and the
// day are full, it is the time until the day ends. Any other error means that
// the limiter could not decide, for example because the store failed. The caller
// must then not do the action.
//
// The check and the count happen in one store transaction, so many goroutines,
// and many programs on the same database, cannot pass the limit together.
func (l *Limiter) Reserve(ctx context.Context, tenant account.TenantID, acct account.AccountID, kind CounterKind, n int, lim account.Limits) error {
	if n < 0 {
		return fmt.Errorf("policy: count for %q must not be negative", kind)
	}
	if kind == CountRecipients {
		if n > lim.RecipientsPerMessage {
			return &RateLimitError{Kind: CountRecipients}
		}
		return nil
	}
	ws := windows(kind, lim)
	if ws == nil {
		return fmt.Errorf("policy: unknown counter kind %q", kind)
	}
	if n == 0 {
		return nil
	}

	now := l.now().UTC()
	checks := make([]store.CounterCheck, len(ws))
	ends := make([]time.Time, len(ws))
	for i, w := range ws {
		// Truncate rounds down to a multiple of the length, counted from the
		// zero time (00:00 UTC). In UTC, a multiple of one hour is the top of an
		// hour and a multiple of 24 hours is a midnight.
		start := now.Truncate(w.length)
		checks[i] = store.CounterCheck{Kind: w.counter, WindowStart: start, Limit: w.limit}
		ends[i] = start.Add(w.length)
	}

	full, err := l.Store.ReserveCounters(ctx, tenant, acct, checks, n, now.Add(-retention))
	if err != nil {
		return fmt.Errorf("policy: reserve %s: %w", kind, err)
	}
	if len(full) == 0 {
		return nil
	}
	var retryAfter time.Duration
	for _, i := range full {
		retryAfter = max(retryAfter, ends[i].Sub(now))
	}
	return &RateLimitError{Kind: kind, RetryAfter: retryAfter}
}

// now returns the current time from l.Now, or from the clock when l.Now is nil.
func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// RateLimitError says that a limit was reached. The text is for the agent, so it
// has the kind of limit and the wait, and no account address.
type RateLimitError struct {
	Kind CounterKind
	// RetryAfter is the time until the full window ends. It is 0 for
	// CountRecipients, because waiting does not help.
	RetryAfter time.Duration
}

// Error returns a short sentence. The wait is in whole minutes, rounded up, so
// it is never "0 minutes".
func (e *RateLimitError) Error() string {
	if e.Kind == CountRecipients {
		return "Too many recipients in one message. Use fewer recipients."
	}
	name := "Send"
	if e.Kind == CountTrash {
		name = "Trash"
	}
	minutes := int((e.RetryAfter + time.Minute - 1) / time.Minute)
	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	return fmt.Sprintf("%s limit reached. Try again in %d %s.", name, minutes, unit)
}
