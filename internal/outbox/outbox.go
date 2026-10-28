// Package outbox queues outgoing email and sends it from a background worker.
//
// A send waits in the queue for an undo window. During the window the user can
// cancel it. When the window ends, a worker sends the message. The message is
// stored encrypted, so a restart does not lose it, and it is deleted from the
// database as soon as the item has ended (sent, failed or cancelled).
//
// Two rules matter more than all others:
//
//   - A message is never sent twice. A send starts only after an atomic claim
//     in the database (queued to sending). Two goroutines, two Outbox values or
//     two programs cannot both win the claim. The network send runs outside any
//     database transaction. An item that is "sending" when a program stops is
//     not sent again, because the server may have it already. After 15 minutes
//     the item is marked failed. See RecoverInterrupted.
//   - A queued message is never dropped silently. It stays in the database until
//     it is sent, cancelled, or marked failed with an error text.
//
// The life of an item:
//
//	queued --claim--> sending --> sent
//	  |                  |------> failed       (permanent error, or the 4th failed try)
//	  |                  '------> queued       (transient error: try again later)
//	  '--cancel--> cancelled
package outbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// Sender sends one message through the mail server of an account. The draft is
// the draft that the message was made from, or empty. The Sender sends the
// message and then discards the draft itself. The outbox does not.
//
// The result decides what happens to the item:
//   - nil means that the server accepted the message. The item is "sent".
//   - An error made with Permanent means that the message must not be sent again.
//     The item is "failed" at once.
//   - Any other error is transient. The Sender promises that the server did NOT
//     accept the message. The outbox tries again after 30 seconds, then 2
//     minutes, then 10 minutes. If the fourth try fails, the item is "failed".
//
// A Sender that cannot tell whether the server accepted the message (for example
// the connection broke after the message data was sent) must return a Permanent
// error. A retry could send the message twice.
//
// The outbox stores the text of the error (at most 300 characters) and shows it
// to the user. The Sender must not put addresses, subjects or message text in
// it.
//
// If the context ends while the Sender runs and the Sender returns an error that
// is not Permanent, the outbox treats the result as unknown. The item is marked
// "failed" and is not tried again.
//
// The Sender must return in less than 15 minutes (it should have a deadline of
// its own, much shorter). After 15 minutes another program that uses the same
// database may take the item for a send that was interrupted and mark it failed.
type Sender func(ctx context.Context, tenant account.TenantID, acct account.AccountID, msg *mail.RawMessage, draft mail.Ref) error

// Status is the state of an item.
type Status string

// The states of an item.
const (
	// StatusQueued means that the item waits for the end of its undo window, or
	// for the time of its next try.
	StatusQueued Status = "queued"
	// StatusSending means that a worker has claimed the item and sends it now.
	StatusSending Status = "sending"
	// StatusSent means that the server accepted the message.
	StatusSent Status = "sent"
	// StatusFailed means that the message was not sent and will not be. Error
	// says why.
	StatusFailed Status = "failed"
	// StatusCancelled means that the user cancelled the item in the undo window.
	StatusCancelled Status = "cancelled"
)

// Item is a message in the outbox, without the message itself.
type Item struct {
	ID      string
	Tenant  account.TenantID
	Account account.AccountID
	Status  Status
	// SendAfter is the end of the undo window. It is kept as whole milliseconds.
	SendAfter time.Time
	// Error is the text of the last error of the Sender, cut to 300 characters.
	// It is empty if no try has failed, and after the item is sent.
	Error string
}

var (
	// ErrTooLate means that Cancel came too late: the item is being sent, it was
	// sent, or it failed.
	ErrTooLate = errors.New("outbox: too late to cancel, the message is being sent or was sent")

	// ErrNotFound means that the tenant has no item with this id. It is also
	// returned when the item belongs to another tenant. It is the same error as
	// store.ErrNotFound.
	ErrNotFound = store.ErrNotFound
)

// permanentError marks an error as permanent. It keeps the text and the cause of
// the original error.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as an error that the outbox must not retry. A Sender
// returns it for a refusal that a new try cannot change, and when it cannot tell
// if the message was delivered. errors.Is and errors.As still see the original
// error. Permanent(nil) is nil.
func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or an error that it wraps, was made with
// Permanent.
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// OutboxStore is the part of the store that the Outbox needs. *store.Store
// implements it.
//
// ClaimOutbox is the compare-and-set that starts a send: it moves one item from
// "queued" to "sending" and returns store.ErrNotFound if it did not do it. The
// calls that write a result (FinishOutboxSent, FailOutbox, RetryOutbox and
// ReleaseOutbox) are compare-and-set from "sending". CancelOutbox is the
// compare-and-set from "queued".
//
// DueOutbox, NextOutboxDue and FailSendingOutbox look at the items of all
// tenants. The worker serves every tenant, so this is on purpose.
// FailSendingOutbox changes only the items that were claimed before the given
// time.
type OutboxStore interface {
	CreateOutbox(ctx context.Context, row *store.OutboxRow, sealed []byte, keyVersion int) error
	GetOutbox(ctx context.Context, tenant account.TenantID, id string) (*store.OutboxRow, error)
	GetOutboxMessage(ctx context.Context, tenant account.TenantID, id string) (keyVersion int, sealed []byte, err error)
	DueOutbox(ctx context.Context, now time.Time, limit int) ([]*store.OutboxRow, error)
	NextOutboxDue(ctx context.Context) (time.Time, bool, error)
	ClaimOutbox(ctx context.Context, tenant account.TenantID, id string, now time.Time) (*store.OutboxRow, error)
	ReleaseOutbox(ctx context.Context, tenant account.TenantID, id string) error
	FinishOutboxSent(ctx context.Context, tenant account.TenantID, id string, now time.Time) error
	FailOutbox(ctx context.Context, tenant account.TenantID, id, errText string) error
	RetryOutbox(ctx context.Context, tenant account.TenantID, id string, nextAttemptAt time.Time, errText string) error
	CancelOutbox(ctx context.Context, tenant account.TenantID, id string) error
	FailSendingOutbox(ctx context.Context, errText string, claimedBefore time.Time) (int, error)
}

var _ OutboxStore = (*store.Store)(nil)

// The limits and times of this package.
const (
	// maxErrorText is the most characters of an error text that the outbox keeps.
	maxErrorText = 300

	// dueBatch is how many due items one query reads.
	dueBatch = 20

	// defaultPoll is how often Drain looks at the clock while it waits.
	defaultPoll = time.Second

	// recoverAfter is the age at which an item in the state "sending" counts as
	// interrupted. It must be longer than any send can run, so that a program
	// never marks the send of another running program as failed.
	recoverAfter = 15 * time.Minute

	// recoverEvery is how often Run looks for interrupted items. It uses the
	// clock of the outbox.
	recoverEvery = time.Minute

	// storeTimeout is the time that the outbox gives to a database call that it
	// makes after the end of a send, or after a claim. The call does not use the
	// context of the caller, because the caller may have stopped it already. See
	// detached.
	storeTimeout = 30 * time.Second
)

// retryDelays are the waits before the next try after the first, second and
// third failed try. A fourth failed try ends the item.
var retryDelays = [...]time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// The error texts that the outbox writes itself. They hold no mail content.
const (
	errTextInterrupted = "delivery unknown: the process stopped while sending"
	errTextStopped     = "delivery unknown: the send was stopped before it finished"
	errTextUnreadable  = "the stored message could not be read"
)

// Outbox queues messages and sends them. It is safe for use by many goroutines.
// Several Outbox values, in one program or in several, can use one database. The
// claim in the database makes sure that each message is sent once.
type Outbox struct {
	// Logger gets a line when the outbox cannot do something in the background,
	// for example when a database call fails during a pass of Run. The lines hold
	// item ids and error messages of the store, and no mail content. A nil Logger
	// logs nothing. Set it before the first call.
	Logger *slog.Logger

	st    OutboxStore
	vault *secrets.Vault
	send  Sender
	now   func() time.Time
	// poll is how often Drain looks at the clock while it waits. A test can make
	// it shorter.
	poll time.Duration
}

// New returns an Outbox. The vault encrypts the stored messages. send is called
// for each message that is due. now is the clock, and a nil now means time.Now.
// New panics if st, v or send is nil, because the outbox could not do its work
// and would leave items in the state "sending".
func New(st OutboxStore, v *secrets.Vault, send Sender, now func() time.Time) *Outbox {
	if st == nil || v == nil || send == nil {
		panic("outbox: New needs a store, a vault and a sender")
	}
	if now == nil {
		now = time.Now
	}
	return &Outbox{st: st, vault: v, send: send, now: now, poll: defaultPoll}
}

// storedMessage is the form in which a message is stored, before it is
// encrypted. The names are the stored format.
type storedMessage struct {
	From       string   `json:"from"`
	Recipients []string `json:"recipients"`
	Data       []byte   `json:"data"`
}

// messageAAD returns the associated data of the stored message of an item. It
// names the item, so a sealed message that is copied to another item cannot be
// opened there.
func messageAAD(tenant account.TenantID, acct account.AccountID, id string) string {
	return secrets.AAD(string(tenant), string(acct), "outbox:"+id)
}

// Enqueue stores a message and returns its item. The message is sent when the
// window has passed, unless Cancel comes first. The draft is the reference of
// the draft that the message was made from. It is empty if there is none.
//
// With window 0 the message is sent at once, before Enqueue returns. The item
// that Enqueue returns shows what happened: "sent", or "failed" with the error
// text. If the Sender fails with a transient error, the item is "queued" for a
// new try after 30 seconds, and Error holds the text. Enqueue does not return an
// error for a failed send.
//
// Once Enqueue has stored the message, the outbox owns it. From then on Enqueue
// returns no error, so the caller never queues the same message again. If the
// outbox cannot do something afterwards (for example it cannot claim the item
// for the inline send), it logs this and leaves the item "queued" for the worker.
//
// The account must belong to the tenant.
func (o *Outbox) Enqueue(ctx context.Context, tenant account.TenantID, acct account.AccountID, msg *mail.RawMessage, window time.Duration, draft mail.Ref) (*Item, error) {
	if msg == nil {
		return nil, errors.New("outbox: no message to queue")
	}
	if window < 0 {
		return nil, errors.New("outbox: the undo window must not be negative")
	}
	// Check the ids first. The tenant key and the associated data are made from
	// them.
	if err := checkIDs(tenant, acct); err != nil {
		return nil, err
	}

	id := strings.ToLower(rand.Text())
	now := o.now().UTC().Truncate(time.Millisecond)
	row := &store.OutboxRow{
		ID:            id,
		Tenant:        tenant,
		Account:       acct,
		Status:        string(StatusQueued),
		SendAfter:     now.Add(window).Truncate(time.Millisecond),
		NextAttemptAt: now.Add(window).Truncate(time.Millisecond),
		CreatedAt:     now,
		DraftRef:      string(draft),
	}

	plain, err := json.Marshal(storedMessage{From: msg.From, Recipients: msg.Recipients, Data: msg.Data})
	if err != nil {
		return nil, fmt.Errorf("outbox: encode message: %w", err)
	}
	defer clear(plain)
	version, sealed, err := o.vault.Encrypt(ctx, tenant, messageAAD(tenant, acct, id), plain)
	if err != nil {
		return nil, fmt.Errorf("outbox: encrypt message: %w", err)
	}
	if err := o.st.CreateOutbox(ctx, row, sealed, version); err != nil {
		return nil, fmt.Errorf("outbox: save message: %w", err)
	}

	if window > 0 {
		return itemFromRow(row), nil
	}
	return o.sendInline(ctx, row, msg), nil
}

// sendInline claims a new item and sends it before Enqueue returns. It returns
// the item as the store has it after the try. It never fails: the message is
// stored already.
func (o *Outbox) sendInline(ctx context.Context, row *store.OutboxRow, msg *mail.RawMessage) *Item {
	status := StatusQueued
	claimed, err := o.st.ClaimOutbox(ctx, row.Tenant, row.ID, o.now())
	switch {
	case err == nil:
		status = StatusSending
		if err := o.deliver(ctx, claimed, msg); err != nil {
			o.warn("outbox: could not finish an inline send", "id", row.ID, "error", err)
		}
	case errors.Is(err, store.ErrNotFound):
		// Another worker claimed it first and sends it. That is correct.
	default:
		// The item stays queued, and the worker sends it later.
		o.warn("outbox: could not claim an item for an inline send", "id", row.ID, "error", err)
	}

	// Read the result without the context of the caller, which may have ended.
	rctx, cancel := detached(ctx)
	defer cancel()
	got, err := o.Get(rctx, row.Tenant, row.ID)
	if err != nil {
		o.warn("outbox: could not read an item after an inline send", "id", row.ID, "error", err)
		item := itemFromRow(row)
		item.Status = status
		return item
	}
	return got
}

// Cancel cancels an item that waits in its undo window, and deletes its message.
// It returns ErrTooLate if the item is being sent, was sent, or failed. It
// returns ErrNotFound if the tenant has no item with this id. A second Cancel of
// an item that is cancelled already does nothing and returns no error.
func (o *Outbox) Cancel(ctx context.Context, tenant account.TenantID, id string) error {
	err := o.st.CancelOutbox(ctx, tenant, id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotQueued):
		return ErrTooLate
	default:
		return fmt.Errorf("outbox: cancel: %w", err)
	}
}

// Get returns one item of the tenant. It returns ErrNotFound if the tenant has
// no item with this id.
func (o *Outbox) Get(ctx context.Context, tenant account.TenantID, id string) (*Item, error) {
	row, err := o.st.GetOutbox(ctx, tenant, id)
	if err != nil {
		return nil, fmt.Errorf("outbox: get: %w", err)
	}
	return itemFromRow(row), nil
}

// RecoverInterrupted marks the interrupted items as failed, with an error text
// that says the delivery is unknown. It returns the number of items. It looks at
// the items of all tenants.
//
// An item is interrupted when it has been in the state "sending" for more than
// 15 minutes. The program that claimed it stopped before it wrote the result.
// The server may have the message, and a second send would deliver it twice, so
// RecoverInterrupted never sends an item again. The user sees the failed item and
// decides.
//
// An item that is "sending" for a shorter time is left alone. Several programs
// (for example one for each client of the user) use one database. A program that
// starts must not fail the send that another running program is doing now. No
// send can run for 15 minutes, so a newer item may be a live send.
//
// Call it once at the start of the program, before Run. Run also calls it about
// once a minute, so a program that died while sending is found while the other
// programs keep running.
func (o *Outbox) RecoverInterrupted(ctx context.Context) (int, error) {
	n, err := o.st.FailSendingOutbox(ctx, errTextInterrupted, o.now().Add(-recoverAfter))
	if err != nil {
		return 0, fmt.Errorf("outbox: recover interrupted items: %w", err)
	}
	return n, nil
}

// Run sends the items that are due, now and then every tick, until ctx ends. It
// then returns ctx.Err(); that is the normal way to stop it. It returns another
// error only if tick is not positive.
//
// In its first pass and then about once a minute (by the clock of the outbox),
// Run also calls RecoverInterrupted. A program that died while it sent is found
// while the other programs keep running.
//
// A database error during a pass is logged (see Logger) and the next pass tries
// again. Items are not lost, because they stay queued.
//
// Run takes the items of all tenants.
//
// To stop the program: cancel the context of Run, wait until Run has returned,
// and only then call Drain with a new context. A send that is running when the
// context of Run ends is stopped, and the item is marked failed (the result is
// unknown). See Sender.
func (o *Outbox) Run(ctx context.Context, tick time.Duration) error {
	if tick <= 0 {
		return errors.New("outbox: the tick must be positive")
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var lastRecover time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if now := o.now(); lastRecover.IsZero() || now.Sub(lastRecover) >= recoverEvery {
			lastRecover = now
			if n, err := o.RecoverInterrupted(ctx); err != nil {
				if ctx.Err() == nil {
					o.warn("outbox: could not look for interrupted items", "error", err)
				}
			} else if n > 0 {
				o.warn("outbox: marked interrupted sends as failed", "count", n)
			}
		}
		if _, err := o.sendDue(ctx); err != nil && ctx.Err() == nil {
			o.warn("outbox: a pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Drain is for the end of the program. It sends every item that is due, then
// waits for the items that become due within maxWait and sends them. It returns
// nil when no item is left that can become due inside maxWait, or when maxWait
// has passed. It returns ctx.Err() if ctx ends first.
//
// It never cancels or deletes anything. Items that are not sent stay queued, and
// the next start of the program sends them.
//
// Drain does not wait for a send that another goroutine is doing now. When the
// program stops, cancel the context of Run and wait for Run to return before
// Drain.
//
// While it waits, Drain looks at the clock at least once a second. maxWait is
// also a limit in real time, so Drain returns when maxWait is over even if the
// clock of the outbox does not move.
func (o *Outbox) Drain(ctx context.Context, maxWait time.Duration) error {
	if maxWait < 0 {
		maxWait = 0
	}
	timeout := time.NewTimer(maxWait)
	defer timeout.Stop()
	deadline := o.now().Add(maxWait)

	for {
		_, sendErr := o.sendDue(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if sendErr != nil {
			return sendErr
		}

		next, ok, err := o.st.NextOutboxDue(ctx)
		if err != nil {
			return fmt.Errorf("outbox: drain: %w", err)
		}
		if !ok || next.After(deadline) {
			return nil
		}
		select {
		case <-timeout.C:
			return nil
		default:
		}

		wait := next.Sub(o.now())
		if wait <= 0 || wait > o.poll {
			wait = o.poll
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return nil
		case <-time.After(wait):
		}
	}
}

// sendDue sends the items that are due now and returns how many it claimed. It
// stops when no due item is left, or when ctx ends. It returns an error only if
// a database call that finds or claims items fails. A failure of one item is
// logged, and the next item is still sent.
func (o *Outbox) sendDue(ctx context.Context) (int, error) {
	total := 0
	for ctx.Err() == nil {
		rows, err := o.st.DueOutbox(ctx, o.now(), dueBatch)
		if err != nil {
			return total, fmt.Errorf("outbox: find due items: %w", err)
		}
		claimedNow := 0
		for _, row := range rows {
			if ctx.Err() != nil {
				return total + claimedNow, nil
			}
			claimed, err := o.st.ClaimOutbox(ctx, row.Tenant, row.ID, o.now())
			if errors.Is(err, store.ErrNotFound) {
				// Another worker claimed it first, or the user cancelled it.
				continue
			}
			if err != nil {
				return total + claimedNow, fmt.Errorf("outbox: claim item: %w", err)
			}
			claimedNow++
			if err := o.deliver(ctx, claimed, nil); err != nil {
				o.warn("outbox: could not finish a send", "id", row.ID, "error", err)
			}
		}
		total += claimedNow
		// Stop when this query found no item to claim (the others are taken), or
		// when it found fewer than a full batch (there is no more).
		if claimedNow == 0 || len(rows) < dueBatch {
			break
		}
	}
	return total, nil
}

// deliver sends one claimed item and writes the result. The item is "sending".
// msg is the message, or nil to read it from the store.
//
// Every database call after the claim uses a context that ctx cannot cancel.
// Without this, a shutdown right after the send would leave a message that was
// sent in the state "sending".
//
// The error it returns is a database error. It means that the result may not be
// written. The item then stays "sending", and RecoverInterrupted marks it failed
// after 15 minutes. It is never sent again.
func (o *Outbox) deliver(ctx context.Context, row *store.OutboxRow, msg *mail.RawMessage) error {
	if msg == nil {
		var err error
		msg, err = o.open(ctx, row)
		if err != nil {
			o.warn("outbox: could not open a stored message", "id", row.ID, "error", err)
			// Nothing was sent, and a new try may work: the key service may be
			// down for a short time. The usual retry steps apply. The message is
			// deleted only if the fourth try fails.
			return o.retryOrFail(ctx, row, errors.New(errTextUnreadable))
		}
	}

	if ctx.Err() != nil {
		// The program was told to stop after the claim and before the send. Nothing
		// was sent, so the item goes back to the queue. It is not a failed try.
		rctx, cancel := detached(ctx)
		defer cancel()
		if err := o.st.ReleaseOutbox(rctx, row.Tenant, row.ID); err != nil {
			return fmt.Errorf("outbox: release item: %w", err)
		}
		return nil
	}

	sendErr := o.send(ctx, row.Tenant, row.Account, msg, mail.Ref(row.DraftRef))
	return o.record(ctx, row, sendErr)
}

// open reads the stored message of a claimed item and decrypts it.
func (o *Outbox) open(ctx context.Context, row *store.OutboxRow) (*mail.RawMessage, error) {
	rctx, cancel := detached(ctx)
	defer cancel()
	version, sealed, err := o.st.GetOutboxMessage(rctx, row.Tenant, row.ID)
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	plain, err := o.vault.Decrypt(rctx, row.Tenant, version, messageAAD(row.Tenant, row.Account, row.ID), sealed)
	if err != nil {
		return nil, fmt.Errorf("decrypt message: %w", err)
	}
	defer clear(plain)
	var stored storedMessage
	if err := json.Unmarshal(plain, &stored); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return &mail.RawMessage{From: stored.From, Recipients: stored.Recipients, Data: stored.Data}, nil
}

// record writes the result of a send. See Sender for what each result means.
func (o *Outbox) record(ctx context.Context, row *store.OutboxRow, sendErr error) error {
	rctx, cancel := detached(ctx)
	defer cancel()

	switch {
	case sendErr == nil:
		return resultError(o.st.FinishOutboxSent(rctx, row.Tenant, row.ID, o.now()))
	case IsPermanent(sendErr):
		return resultError(o.st.FailOutbox(rctx, row.Tenant, row.ID, errorText(sendErr)))
	case ctx.Err() != nil:
		// The context ended during the send. The server may have the message, so
		// a retry could send it twice.
		return resultError(o.st.FailOutbox(rctx, row.Tenant, row.ID, errTextStopped))
	default:
		return o.retryOrFail(ctx, row, sendErr)
	}
}

// retryOrFail handles a transient failure. The first three failures put the item
// back in the queue with a later time. The fourth one ends it.
func (o *Outbox) retryOrFail(ctx context.Context, row *store.OutboxRow, err error) error {
	rctx, cancel := detached(ctx)
	defer cancel()

	failed := row.Attempts + 1
	if failed > len(retryDelays) {
		return resultError(o.st.FailOutbox(rctx, row.Tenant, row.ID, errorText(err)))
	}
	next := o.now().Add(retryDelays[failed-1])
	return resultError(o.st.RetryOutbox(rctx, row.Tenant, row.ID, next, errorText(err)))
}

// resultError turns the error of a result call into the error of deliver.
// store.ErrNotFound means that the item was not in the state "sending" any more:
// something else changed it, for example RecoverInterrupted in another program.
func resultError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("outbox: the item changed while it was sending: %w", err)
	default:
		return fmt.Errorf("outbox: write the result: %w", err)
	}
}

// detached returns a context that ctx cannot cancel, with a time limit. It keeps
// the values of ctx.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
}

// errorText returns the text of err, cut to 300 characters (not bytes), as valid
// UTF-8.
func errorText(err error) string {
	text := strings.ToValidUTF8(err.Error(), "�")
	if utf8.RuneCountInString(text) <= maxErrorText {
		return text
	}
	return string([]rune(text)[:maxErrorText])
}

// itemFromRow returns the public form of a row.
func itemFromRow(row *store.OutboxRow) *Item {
	return &Item{
		ID:        row.ID,
		Tenant:    row.Tenant,
		Account:   row.Account,
		Status:    Status(row.Status),
		SendAfter: row.SendAfter,
		Error:     row.Error,
	}
}

// checkIDs refuses an empty id and an id with "|". The ids are part of the
// associated data of the encryption, which has no escaping. The store makes the
// same check on its own calls.
func checkIDs(tenant account.TenantID, acct account.AccountID) error {
	for _, id := range []struct{ kind, value string }{{"tenant", string(tenant)}, {"account", string(acct)}} {
		if id.value == "" || strings.Contains(id.value, "|") {
			return fmt.Errorf("%w: %s id must not be empty or contain '|'", store.ErrInvalidID, id.kind)
		}
	}
	return nil
}

// warn logs a line if the Outbox has a Logger.
func (o *Outbox) warn(msg string, args ...any) {
	if o.Logger != nil {
		o.Logger.Warn(msg, args...)
	}
}
