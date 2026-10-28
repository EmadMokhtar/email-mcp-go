// Package confirm keeps the actions that wait for the user's confirmation, such
// as sending an email or moving mail to Trash.
//
// The flow has four steps:
//  1. The agent asks for an action. The server calls Create. Create stores the
//     exact request (the payload) and a preview of it, both encrypted.
//  2. The server gives the agent a RequestState and shows the preview to the
//     user in a way that the agent cannot use. The state is a signed token. It
//     names the action and holds the hash of the payload.
//  3. The user confirms. The agent calls the same tool again with the state and
//     the payload. The agent cannot confirm for the user, because it cannot use
//     the channel that the user confirms in.
//  4. The server calls Claim. Claim checks that the state is genuine, that it
//     belongs to this tenant, that the action still waits, and that the payload
//     is byte for byte the payload that the user saw. Only then it moves the
//     action to "approved", once. The server acts on the payload and then moves
//     the action to "done".
//
// An action that has ended (done, rejected or expired) keeps no secret. The
// statement that ends it deletes the encrypted payload and the encrypted
// preview. Prune deletes old rows that have ended.
//
// Claim proves four things: the payload is exactly the payload that was stored
// (a change of one byte changes the hash), the request is for this tenant, it is
// in time, and it happens once (two callers cannot both use one confirmation).
// Claim does not prove consent. A signed state shows only that the server made
// it, and the agent holds it. The caller must call Claim only after the user has
// accepted, in a channel that the agent cannot answer, such as the answer to an
// elicitation or the Approve button of the web page. This package does not know
// that channel.
package confirm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// Kind is the kind of action that waits for confirmation.
type Kind string

// The kinds of action.
const (
	// KindSend sends an email.
	KindSend Kind = "send"
	// KindTrash moves mail to Trash.
	KindTrash Kind = "trash"
)

// State is where an action is in its life.
type State string

// The states of an action.
const (
	// StatePending means that the action waits for the user.
	StatePending State = "pending"
	// StateApproved means that the user confirmed and the server took the
	// action (see Manager.Claim). The server is now acting on it.
	StateApproved State = "approved"
	// StateRejected means that the user, or the server, refused the action.
	StateRejected State = "rejected"
	// StateExpired means that the action waited too long.
	StateExpired State = "expired"
	// StateDone means that the server finished the action.
	StateDone State = "done"
)

// The limits and lifetimes of this package.
const (
	// storeTimeout is the time that a write gets when it must not be cancelled by
	// the context of the caller. See detached.
	storeTimeout = 30 * time.Second
	// pendingLifetime is how long an action waits for the user.
	pendingLifetime = 24 * time.Hour
	// requestStateLifetime is how long a RequestState works.
	requestStateLifetime = 10 * time.Minute
	// maxBodyStart is the most characters of the body that a preview keeps.
	maxBodyStart = 500
)

var (
	// ErrStateInvalid means that the request state cannot be used. It is not a
	// genuine state, it is for another tenant, or it names an action that does
	// not exist. The error does not say which, on purpose.
	ErrStateInvalid = errors.New("confirm: the request state is not valid")

	// ErrStateExpired means that the request state is genuine, but its 10
	// minutes are over. The caller starts again to get a new state.
	ErrStateExpired = errors.New("confirm: the request state has expired")

	// ErrPayloadChanged means that the payload is not the payload that the user
	// saw.
	ErrPayloadChanged = errors.New("confirm: the request is not the one that was approved")

	// ErrNotPending means that the action does not wait for the user. It was
	// used, refused, or it has expired. Transition returns it too when the
	// action is not in the state that the caller expected.
	ErrNotPending = errors.New("confirm: the action is not waiting for confirmation")

	// ErrNotFound means that the tenant has no action with this id.
	ErrNotFound = errors.New("confirm: pending action not found")

	// ErrCorrupted means that the stored data of an action does not match the
	// hash of the action, or that a waiting or approved action has lost its
	// payload or its preview. Someone changed the stored data, or the storage is
	// damaged. The calls that read the action return it and do not return the
	// payload.
	ErrCorrupted = errors.New("confirm: the stored action does not match its hash")
)

// Preview is what the user sees before they confirm. It is not the payload. It
// holds recipients, a subject and the start of the body, so it is secret and it
// is stored encrypted.
type Preview struct {
	Account string   `json:"account"`
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	// BodyStart is the first part of the body. Create keeps at most 500
	// characters.
	BodyStart string `json:"body_start"`
	// Count is the number of messages the action affects.
	Count int `json:"count"`
}

// Pending is an action that needs the user's confirmation. It does not hold the
// payload. Get returns the payload.
//
// After the action has ended (State is done, rejected or expired, and the
// expiry has been recorded), the payload and the preview are deleted. Preview is
// then the zero value.
type Pending struct {
	ID      string
	Tenant  account.TenantID
	Account account.AccountID
	Kind    Kind
	// Hash is HashPayload of the kind and the payload that the user confirms.
	Hash    [32]byte
	Preview Preview
	// State is the state now. A waiting action whose time has passed has the
	// state "expired".
	State State
	// CreatedAt is kept as whole milliseconds. ExpiresAt is 24 hours later.
	CreatedAt, ExpiresAt time.Time
	// ResultID says what the action made, for example the id of the sent
	// message. It is empty until the server sets it with Transition.
	ResultID string
}

// HashPayload returns the SHA-256 of the kind, a zero byte and the payload. The
// kind is part of the hash, so a payload that was approved for one kind cannot
// be used for another.
func HashPayload(kind Kind, payload []byte) [32]byte {
	h := sha256.New()
	// A write to a hash never fails.
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

// PendingStore is the part of the store that the Manager needs. *store.Store
// implements it.
//
// The store compares times in whole milliseconds and does not know what the
// states and kinds mean, except that an action which has ended keeps no payload
// and no preview. TransitionPending and ClaimPending are compare-and-set
// statements that return store.ErrNotFound when they change nothing.
// PrunePending works on the actions of all tenants.
type PendingStore interface {
	CreatePending(ctx context.Context, row *store.PendingRow, payload []byte, payloadKeyVersion int) error
	GetPending(ctx context.Context, tenant account.TenantID, id string) (*store.PendingRow, error)
	GetPendingPayload(ctx context.Context, tenant account.TenantID, id string) (keyVersion int, sealed []byte, err error)
	ListPending(ctx context.Context, tenant account.TenantID, now time.Time) ([]*store.PendingRow, error)
	TransitionPending(ctx context.Context, tenant account.TenantID, id, from, to, resultID string, now time.Time) error
	ClaimPending(ctx context.Context, tenant account.TenantID, id string, hash []byte, now time.Time) error
	ExpirePending(ctx context.Context, tenant account.TenantID, id string, now time.Time) error
	PrunePending(ctx context.Context, createdBefore, now time.Time) (int, error)
}

var _ PendingStore = (*store.Store)(nil)

// Manager creates pending actions and checks the confirmation of the user. It is
// safe for use by many goroutines.
type Manager struct {
	st     PendingStore
	vault  *secrets.Vault
	signer *signing.Signer
	now    func() time.Time
}

// NewManager returns a Manager. The vault encrypts the payloads and the
// previews. The signer makes the request states. now is the clock, and a nil
// now means time.Now.
//
// The Manager uses now for the request states too, so that the states and the
// actions follow one clock. For this it keeps its own copy of s with the clock
// set to now. The Now field of s itself is not used and not changed.
func NewManager(st PendingStore, v *secrets.Vault, s *signing.Signer, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	clocked := *s
	clocked.Now = now
	return &Manager{st: st, vault: v, signer: &clocked, now: now}
}

// Create stores an action that waits for the user, and returns it in the state
// "pending". The payload and the preview are stored encrypted. Create keeps at
// most 500 characters of the preview body.
//
// The account must belong to the tenant. The payload must not be empty.
func (m *Manager) Create(ctx context.Context, tenant account.TenantID, acct account.AccountID, kind Kind, payload []byte, p Preview) (*Pending, error) {
	if kind != KindSend && kind != KindTrash {
		return nil, fmt.Errorf("confirm: unknown kind %q", kind)
	}
	if len(payload) == 0 {
		return nil, errors.New("confirm: the payload is empty")
	}

	// Own the preview, so a later change by the caller does not change it.
	p.To = slices.Clone(p.To)
	p.BodyStart = firstChars(p.BodyStart, maxBodyStart)

	id := strings.ToLower(rand.Text())
	created := m.now().UTC().Truncate(time.Millisecond)
	pending := &Pending{
		ID:        id,
		Tenant:    tenant,
		Account:   acct,
		Kind:      kind,
		Hash:      HashPayload(kind, payload),
		Preview:   p,
		State:     StatePending,
		CreatedAt: created,
		ExpiresAt: created.Add(pendingLifetime),
	}

	previewJSON, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("confirm: encode preview: %w", err)
	}
	payloadVersion, sealedPayload, err := m.vault.Encrypt(ctx, tenant, fieldAAD(tenant, acct, payloadField(id)), payload)
	if err != nil {
		return nil, fmt.Errorf("confirm: encrypt payload: %w", err)
	}
	previewVersion, sealedPreview, err := m.vault.Encrypt(ctx, tenant, fieldAAD(tenant, acct, previewField(id)), previewJSON)
	if err != nil {
		return nil, fmt.Errorf("confirm: encrypt preview: %w", err)
	}

	row := &store.PendingRow{
		ID:                id,
		Tenant:            tenant,
		Account:           acct,
		Kind:              string(kind),
		Hash:              pending.Hash[:],
		State:             string(StatePending),
		CreatedAt:         pending.CreatedAt,
		ExpiresAt:         pending.ExpiresAt,
		Preview:           sealedPreview,
		PreviewKeyVersion: previewVersion,
	}
	if err := m.st.CreatePending(ctx, row, sealedPayload, payloadVersion); err != nil {
		return nil, fmt.Errorf("confirm: save pending action: %w", err)
	}
	return pending, nil
}

// requestState is the content of a RequestState token. A token is readable by
// whoever holds it, so it must hold nothing secret. The hash shows nothing of
// the payload that the agent does not know already.
type requestState struct {
	ID     string           `json:"id"`
	Hash   string           `json:"hash"` // hex
	Tenant account.TenantID `json:"tenant"`
}

// RequestState returns the signed token that the agent sends back with the
// payload. It works for 10 minutes, for the tenant and the hash of p only.
func (m *Manager) RequestState(p *Pending) (string, error) {
	if p == nil {
		return "", errors.New("confirm: no pending action for the request state")
	}
	state, err := m.signer.Seal(signing.PurposeRequestState, requestState{
		ID:     p.ID,
		Hash:   hex.EncodeToString(p.Hash[:]),
		Tenant: p.Tenant,
	}, requestStateLifetime)
	if err != nil {
		return "", fmt.Errorf("confirm: sign request state: %w", err)
	}
	return state, nil
}

// Resume checks a confirmation and does not change anything. It returns the
// action if the state is genuine and not expired, the state belongs to the
// tenant, the action still waits, and the payload is the payload that the user
// saw. Otherwise it returns ErrStateInvalid, ErrStateExpired, ErrNotPending or
// ErrPayloadChanged, and it does not say which part of the state was wrong.
//
// Resume cannot stop two callers from using one confirmation. To act on a
// confirmation, call Claim.
func (m *Manager) Resume(ctx context.Context, tenant account.TenantID, state string, payload []byte) (*Pending, error) {
	now := m.now()
	row, _, err := m.verify(ctx, now, tenant, state, payload)
	if err != nil {
		return nil, err
	}
	return m.pendingFromRow(ctx, row, now)
}

// Claim does everything that Resume checks and moves the action from "pending"
// to "approved". The move is one statement in the store. It changes the action
// only if the action of this tenant still waits, its time has not passed, and
// its stored hash is the hash of the payload in this call. If two callers claim
// one action at the same time, one succeeds and the other gets ErrNotPending.
// The caller that succeeds is the only one that may act on the payload.
//
// Claim proves that the payload is exactly the stored payload, that the request
// is for this tenant and in time, and that it happens once. It does NOT prove
// consent. The state is signed by the server, but the agent holds it, so the
// agent can call Claim. The caller must call Claim only after the user has
// accepted in a channel that the agent cannot answer, such as the answer to an
// elicitation or the Approve button of the web page.
//
// A claim that fails leaves the action as it was. In particular, a payload that
// has changed does not use up the confirmation. There is one exception: if the
// action waits and its time has passed, the claim fails with ErrNotPending, and
// Claim records the expiry and deletes the payload and the preview.
//
// The context stops Claim before the claim statement starts (the checks before
// it read the store with it). The statement itself runs on a context that the
// caller cannot cancel, so a committed claim is never reported as a failure.
func (m *Manager) Claim(ctx context.Context, tenant account.TenantID, state string, payload []byte) (*Pending, error) {
	now := m.now()
	// The read in verify chooses the error for the caller. It does not decide
	// the claim, because the row can change after the read.
	row, sum, err := m.verify(ctx, now, tenant, state, payload)
	if err != nil {
		if errors.Is(err, ErrNotPending) && row != nil && State(row.State) == StatePending {
			// The action waits in the store, but its time has passed. Record the
			// expiry and delete the secrets. The claim has failed in any case, so
			// an error here is not worth another answer. The next attempt, or
			// Prune, does it again.
			wctx, cancel := detached(ctx)
			defer cancel()
			_ = m.st.ExpirePending(wctx, tenant, row.ID, now)
		}
		return nil, err
	}
	// Open the preview before the move. If this fails, the action still waits.
	pending, err := m.pendingFromRow(ctx, row, now)
	if err != nil {
		return nil, err
	}
	// The statement checks the state, the time and the hash again, at the time
	// of the move. The hash is the hash of the payload that the caller passed.
	//
	// It runs on a context that ctx cannot cancel. The driver can commit the
	// statement and still return the error of a context that ended just then.
	// The claim would be done, the caller would be told it failed, and the action
	// would stay "approved" for ever.
	wctx, cancel := detached(ctx)
	defer cancel()
	err = m.st.ClaimPending(wctx, tenant, row.ID, sum[:], now)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotPending
	}
	if err != nil {
		return nil, fmt.Errorf("confirm: claim pending action: %w", err)
	}
	pending.State = StateApproved
	return pending, nil
}

// verify does the checks of Resume and Claim, in this order:
//  1. The signature, the purpose and the lifetime of the state.
//  2. The tenant in the state is the tenant of the caller.
//  3. The action exists for that tenant, and the hash in the state is its hash.
//  4. The action still waits and its time has not passed.
//  5. The hash of the payload is the hash of the action.
//
// Errors before step 4 do not say which check failed. They are
// ErrStateInvalid, except for an expired state.
//
// verify also returns the hash of the payload, so that Claim can put it into
// the claim statement. When it returns ErrNotPending, it also returns the row,
// so that Claim can see whether the action only waited too long.
func (m *Manager) verify(ctx context.Context, now time.Time, tenant account.TenantID, state string, payload []byte) (*store.PendingRow, [32]byte, error) {
	var none [32]byte
	var token requestState
	if err := m.signer.Open(signing.PurposeRequestState, state, &token); err != nil {
		switch {
		case errors.Is(err, signing.ErrExpired):
			return nil, none, ErrStateExpired
		case errors.Is(err, signing.ErrInvalid):
			return nil, none, ErrStateInvalid
		default:
			return nil, none, fmt.Errorf("confirm: open request state: %w", err)
		}
	}
	if token.Tenant != tenant {
		return nil, none, ErrStateInvalid
	}
	tokenHash, err := hex.DecodeString(token.Hash)
	if err != nil || len(tokenHash) != sha256.Size {
		return nil, none, ErrStateInvalid
	}

	row, err := m.st.GetPending(ctx, tenant, token.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, none, ErrStateInvalid
	}
	if err != nil {
		return nil, none, fmt.Errorf("confirm: load pending action: %w", err)
	}
	if subtle.ConstantTimeCompare(tokenHash, row.Hash) != 1 {
		return nil, none, ErrStateInvalid
	}

	if stateOf(row, now) != StatePending {
		return row, none, ErrNotPending
	}
	// The kind of the stored action is part of the hash.
	sum := HashPayload(Kind(row.Kind), payload)
	if subtle.ConstantTimeCompare(sum[:], row.Hash) != 1 {
		return nil, none, ErrPayloadChanged
	}
	return row, sum, nil
}

// Get returns an action of the tenant and its payload. It returns ErrNotFound if
// the tenant has no action with this id. An action that has expired is still
// returned, with the state "expired".
//
// An action that has ended has no payload any more (see Transition). For it, Get
// returns the action with a nil payload and no error. The state and the result
// id say what happened. A caller that acts on the payload must check the state,
// or check that the payload is not nil. A waiting action that is past its time
// but whose expiry nobody recorded yet still has its payload.
//
// Get hashes the decrypted payload with the stored kind and compares the result
// with the stored hash. If they differ, it returns ErrCorrupted and no payload.
// It returns ErrCorrupted too if a waiting or approved action has lost its
// payload or its preview.
func (m *Manager) Get(ctx context.Context, tenant account.TenantID, id string) (*Pending, []byte, error) {
	row, err := m.st.GetPending(ctx, tenant, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: load pending action: %w", err)
	}
	pending, err := m.pendingFromRow(ctx, row, m.now())
	if err != nil {
		return nil, nil, err
	}
	if hasEnded(State(row.State)) {
		return pending, nil, nil
	}

	version, sealed, err := m.st.GetPendingPayload(ctx, tenant, id)
	if errors.Is(err, store.ErrNotFound) {
		// The row has no payload. Read it again to see why:
		//  - Prune deleted the row after the first read: not found.
		//  - A call moved the action to a final state after the first read, and
		//    the same statement deleted the payload. This is the normal flow, for
		//    example when the server finishes the send. Return the action as it is
		//    now, without a payload.
		//  - The action still waits or is approved: it lost its payload, so the
		//    storage is damaged.
		reread, readErr := m.st.GetPending(ctx, tenant, id)
		switch {
		case errors.Is(readErr, store.ErrNotFound):
			return nil, nil, ErrNotFound
		case readErr != nil:
			return nil, nil, fmt.Errorf("confirm: load pending action: %w", readErr)
		case hasEnded(State(reread.State)):
			ended, err := m.pendingFromRow(ctx, reread, m.now())
			if err != nil {
				return nil, nil, err
			}
			return ended, nil, nil
		}
		return nil, nil, ErrCorrupted
	}
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: load payload: %w", err)
	}
	payload, err := m.vault.Decrypt(ctx, tenant, version, fieldAAD(row.Tenant, row.Account, payloadField(row.ID)), sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: decrypt payload: %w", err)
	}
	sum := HashPayload(Kind(row.Kind), payload)
	if subtle.ConstantTimeCompare(sum[:], row.Hash) != 1 {
		clear(payload)
		return nil, nil, ErrCorrupted
	}
	return pending, payload, nil
}

// Transition moves an action from one state to another. It is a compare-and-set:
// it changes the action only if the action of this tenant is in the state from.
// Otherwise it returns ErrNotPending. This includes an action that does not
// exist and an action that waits but has expired.
//
// The target must be "approved", "rejected", "expired" or "done". The old state
// must be "pending" or "approved", because the other states are final. The
// result id is saved when it is not empty.
//
// A move to "rejected", "expired" or "done" ends the action. The same statement
// deletes the encrypted payload and the encrypted preview.
//
// If the action waits and its time has passed, the move fails with ErrNotPending,
// and Transition records the expiry and deletes the payload and the preview.
//
// The statement runs on a context that the caller cannot cancel, so a committed
// move is never reported as a failure. A move to a final state ("rejected",
// "expired" or "done") always runs, also when the context of the caller has
// ended. The usual flow is: Claim, send the message (this can take minutes), move
// the action to "done". If the client leaves during the send, the action must
// still end and lose its payload and preview, because Prune never deletes an
// action that stays "approved". Only a move to "approved" is refused when the
// context has ended: a caller that has left must not approve an action.
func (m *Manager) Transition(ctx context.Context, tenant account.TenantID, id string, from, to State, resultID string) error {
	switch {
	case !isState(from):
		return fmt.Errorf("confirm: unknown state %q", from)
	case to != StateApproved && to != StateRejected && to != StateExpired && to != StateDone:
		return fmt.Errorf("confirm: an action cannot move to the state %q", to)
	case from == to:
		return fmt.Errorf("confirm: the action is already in the state %q", from)
	case from != StatePending && from != StateApproved:
		return ErrNotPending
	}

	if to == StateApproved {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	wctx, cancel := detached(ctx)
	defer cancel()
	err := m.st.TransitionPending(wctx, tenant, id, string(from), string(to), resultID, m.now())
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidID) {
		return ErrNotPending
	}
	if err != nil {
		return fmt.Errorf("confirm: change state: %w", err)
	}
	return nil
}

// ListPending returns the actions of the tenant that wait for the user, newest
// first. It leaves out the actions that have expired.
func (m *Manager) ListPending(ctx context.Context, tenant account.TenantID) ([]*Pending, error) {
	now := m.now()
	rows, err := m.st.ListPending(ctx, tenant, now)
	if err != nil {
		return nil, fmt.Errorf("confirm: list pending actions: %w", err)
	}
	list := make([]*Pending, 0, len(rows))
	for _, row := range rows {
		pending, err := m.pendingFromRow(ctx, row, now)
		if err != nil {
			return nil, err
		}
		list = append(list, pending)
	}
	return list, nil
}

// pendingFromRow decrypts the preview of a row and returns the action.
func (m *Manager) pendingFromRow(ctx context.Context, row *store.PendingRow, now time.Time) (*Pending, error) {
	if len(row.Hash) != sha256.Size {
		return nil, errors.New("confirm: the stored hash has the wrong size")
	}
	pending := &Pending{
		ID:        row.ID,
		Tenant:    row.Tenant,
		Account:   row.Account,
		Kind:      Kind(row.Kind),
		State:     stateOf(row, now),
		CreatedAt: row.CreatedAt,
		ExpiresAt: row.ExpiresAt,
		ResultID:  row.ResultID,
	}
	copy(pending.Hash[:], row.Hash)

	if len(row.Preview) == 0 {
		// An action that has ended has no preview. For any other action, a missing
		// preview means that the storage is damaged.
		if hasEnded(State(row.State)) {
			return pending, nil
		}
		return nil, ErrCorrupted
	}
	previewJSON, err := m.vault.Decrypt(ctx, row.Tenant, row.PreviewKeyVersion, fieldAAD(row.Tenant, row.Account, previewField(row.ID)), row.Preview)
	if err != nil {
		return nil, fmt.Errorf("confirm: decrypt preview: %w", err)
	}
	if err := json.Unmarshal(previewJSON, &pending.Preview); err != nil {
		return nil, fmt.Errorf("confirm: decode preview: %w", err)
	}
	return pending, nil
}

// Prune deletes the old actions that have ended, and returns how many it
// deleted. An action is deleted when it was created more than olderThan ago and
// either its state is "done", "rejected" or "expired", or it waits and its time
// has passed.
//
// It keeps an action that is "approved": the server took it and has not
// finished, so the outcome is not known. It keeps a waiting action that still
// has time, because the user can still confirm it. An action whose state is final
// has lost its payload and preview already. Prune removes the rest of the row,
// and the rows of waiting actions that nobody answered.
//
// Prune works on the actions of ALL tenants, on purpose. It is housekeeping for
// the whole program, and no call that is made for one user may use it.
// olderThan must not be negative.
func (m *Manager) Prune(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan < 0 {
		return 0, errors.New("confirm: the age for Prune must not be negative")
	}
	now := m.now()
	n, err := m.st.PrunePending(ctx, now.Add(-olderThan), now)
	if err != nil {
		return 0, fmt.Errorf("confirm: prune pending actions: %w", err)
	}
	return n, nil
}

// detached returns a context that ctx cannot cancel, with a time limit. It keeps
// the values of ctx. The driver of the database can commit a statement and still
// return the error of a context that ended just then. For a write that must not
// be lost, or reported as lost, the caller uses this context.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
}

// hasEnded reports whether s is a state in which an action has ended: done,
// rejected or expired. Such an action keeps no payload and no preview.
func hasEnded(s State) bool {
	return s == StateDone || s == StateRejected || s == StateExpired
}

// stateOf returns the state of a row at now. A waiting action is expired when
// now is later than its ExpiresAt. The comparison uses whole milliseconds, the
// same as the store. The row in the store is not changed.
func stateOf(row *store.PendingRow, now time.Time) State {
	state := State(row.State)
	if state == StatePending && now.UnixMilli() > row.ExpiresAt.UnixMilli() {
		return StateExpired
	}
	return state
}

// isState reports whether s is one of the five states.
func isState(s State) bool {
	switch s {
	case StatePending, StateApproved, StateRejected, StateExpired, StateDone:
		return true
	}
	return false
}

// payloadField and previewField are the field names in the associated data of
// the encrypted values. Each value is bound to its own action, so a copied row
// cannot be opened as another one.
func payloadField(id string) string { return "pending:" + id }
func previewField(id string) string { return "pending-preview:" + id }

// fieldAAD returns the associated data for one field of an action of an account.
func fieldAAD(tenant account.TenantID, acct account.AccountID, field string) string {
	return secrets.AAD(string(tenant), string(acct), field)
}

// firstChars returns the first n characters (runes) of s, or s if it is not
// longer.
func firstChars(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
