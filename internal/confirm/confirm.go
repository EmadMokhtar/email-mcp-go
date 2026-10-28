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
// The agent cannot approve an action by itself and cannot change what the user
// approved. A change of one byte changes the hash. Because Claim is a
// compare-and-set, two callers cannot both use one confirmation.
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
// states and kinds mean. TransitionPending is a compare-and-set that returns
// store.ErrNotFound when it changes nothing.
type PendingStore interface {
	CreatePending(ctx context.Context, row *store.PendingRow, payload []byte, payloadKeyVersion int) error
	GetPending(ctx context.Context, tenant account.TenantID, id string) (*store.PendingRow, error)
	GetPendingPayload(ctx context.Context, tenant account.TenantID, id string) (keyVersion int, sealed []byte, err error)
	ListPending(ctx context.Context, tenant account.TenantID, now time.Time) ([]*store.PendingRow, error)
	TransitionPending(ctx context.Context, tenant account.TenantID, id, from, to, resultID string, now time.Time) error
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
	row, err := m.verify(ctx, now, tenant, state, payload)
	if err != nil {
		return nil, err
	}
	return m.pendingFromRow(ctx, row, now)
}

// Claim does everything that Resume checks and moves the action from "pending"
// to "approved". The move is a compare-and-set in the store. If two callers
// claim one action at the same time, one succeeds and the other gets
// ErrNotPending. The caller that succeeds is the only one that may act on the
// payload.
//
// A claim that fails leaves the action as it was. In particular, a payload that
// has changed does not use up the confirmation.
func (m *Manager) Claim(ctx context.Context, tenant account.TenantID, state string, payload []byte) (*Pending, error) {
	now := m.now()
	row, err := m.verify(ctx, now, tenant, state, payload)
	if err != nil {
		return nil, err
	}
	// Open the preview before the move. If this fails, the action still waits.
	pending, err := m.pendingFromRow(ctx, row, now)
	if err != nil {
		return nil, err
	}
	// The hash in the row never changes, so a check before the move is as good
	// as a check in the same statement. The move itself decides who wins.
	err = m.st.TransitionPending(ctx, tenant, row.ID, string(StatePending), string(StateApproved), "", now)
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
func (m *Manager) verify(ctx context.Context, now time.Time, tenant account.TenantID, state string, payload []byte) (*store.PendingRow, error) {
	var token requestState
	if err := m.signer.Open(signing.PurposeRequestState, state, &token); err != nil {
		switch {
		case errors.Is(err, signing.ErrExpired):
			return nil, ErrStateExpired
		case errors.Is(err, signing.ErrInvalid):
			return nil, ErrStateInvalid
		default:
			return nil, fmt.Errorf("confirm: open request state: %w", err)
		}
	}
	if token.Tenant != tenant {
		return nil, ErrStateInvalid
	}
	tokenHash, err := hex.DecodeString(token.Hash)
	if err != nil || len(tokenHash) != sha256.Size {
		return nil, ErrStateInvalid
	}

	row, err := m.st.GetPending(ctx, tenant, token.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrStateInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("confirm: load pending action: %w", err)
	}
	if subtle.ConstantTimeCompare(tokenHash, row.Hash) != 1 {
		return nil, ErrStateInvalid
	}

	if stateOf(row, now) != StatePending {
		return nil, ErrNotPending
	}
	// The kind of the stored action is part of the hash.
	sum := HashPayload(Kind(row.Kind), payload)
	if subtle.ConstantTimeCompare(sum[:], row.Hash) != 1 {
		return nil, ErrPayloadChanged
	}
	return row, nil
}

// Get returns an action of the tenant and its payload. It returns ErrNotFound if
// the tenant has no action with this id. An action that has expired is still
// returned, with the state "expired".
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

	version, sealed, err := m.st.GetPendingPayload(ctx, tenant, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: load payload: %w", err)
	}
	payload, err := m.vault.Decrypt(ctx, tenant, version, fieldAAD(row.Tenant, row.Account, payloadField(row.ID)), sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("confirm: decrypt payload: %w", err)
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

	err := m.st.TransitionPending(ctx, tenant, id, string(from), string(to), resultID, m.now())
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
	previewJSON, err := m.vault.Decrypt(ctx, row.Tenant, row.PreviewKeyVersion, fieldAAD(row.Tenant, row.Account, previewField(row.ID)), row.Preview)
	if err != nil {
		return nil, fmt.Errorf("confirm: decrypt preview: %w", err)
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
	if err := json.Unmarshal(previewJSON, &pending.Preview); err != nil {
		return nil, fmt.Errorf("confirm: decode preview: %w", err)
	}
	return pending, nil
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
