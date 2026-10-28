package confirm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the driver that the store uses, for addTenant

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// testStart is the time at which most tests start.
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

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// fixture is a manager on a real store and a real vault, with one account.
type fixture struct {
	t      *testing.T
	dir    string
	store  *store.Store
	vault  *secrets.Vault
	clock  *clock
	mgr    *Manager
	key    []byte // the signing key
	tenant account.TenantID
	acct   account.AccountID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	a := account.NewIMAPAccount(tenant, "work@example.com", account.MailServer{}, testStart)
	require.NoError(t, st.CreateAccount(ctx, a))

	keyPath := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, secrets.GenerateKeyFile(keyPath))
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	vault := secrets.NewVault(st, kek)

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	c := &clock{t: testStart}
	return &fixture{
		t:      t,
		dir:    dir,
		store:  st,
		vault:  vault,
		clock:  c,
		mgr:    NewManager(st, vault, signing.New(key), c.Now),
		key:    key,
		tenant: tenant,
		acct:   a.ID,
	}
}

// testPreview returns a preview with text that a test can look for.
func testPreview() Preview {
	return Preview{
		Account:   "work",
		From:      "me@example.com",
		To:        []string{"bob@example.org", "carol@example.org"},
		Subject:   "Quarterly figures for Zebra",
		BodyStart: "Hello Bob, the secret launch date is 5 May",
		Count:     2,
	}
}

// create makes a pending send in the fixture account.
func (f *fixture) create(payload string) *Pending {
	f.t.Helper()
	p, err := f.mgr.Create(context.Background(), f.tenant, f.acct, KindSend, []byte(payload), testPreview())
	require.NoError(f.t, err)
	return p
}

// state returns a request state for p.
func (f *fixture) state(p *Pending) string {
	f.t.Helper()
	s, err := f.mgr.RequestState(p)
	require.NoError(f.t, err)
	return s
}

// forge makes a token with the real key and the request-state purpose, so a
// test can build a token that the manager never made.
func (f *fixture) forge(v any) string {
	f.t.Helper()
	signer := signing.New(f.key)
	signer.Now = f.clock.Now
	token, err := signer.Seal(signing.PurposeRequestState, v, 10*time.Minute)
	require.NoError(f.t, err)
	return token
}

// addTenant adds a tenant with one account, for isolation tests. The store has
// no exported method that makes a tenant other than "local", so the test writes
// the row itself.
func (f *fixture) addTenant(id account.TenantID) account.AccountID {
	f.t.Helper()
	ctx := context.Background()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	require.NoError(f.t, err)

	a := account.NewIMAPAccount(id, "other@example.com", account.MailServer{}, testStart)
	require.NoError(f.t, f.store.CreateAccount(ctx, a))
	return a.ID
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

func TestResumeHappyPath(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("the exact message the user saw")

	created, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, payload, testPreview())
	require.NoError(t, err)
	require.Equal(t, StatePending, created.State)
	require.Equal(t, f.tenant, created.Tenant)
	require.Equal(t, f.acct, created.Account)
	require.Equal(t, KindSend, created.Kind)
	require.Equal(t, HashPayload(KindSend, payload), created.Hash)
	require.Equal(t, testPreview(), created.Preview)
	require.Equal(t, testStart, created.CreatedAt)
	require.Empty(t, created.ResultID)

	state, err := f.mgr.RequestState(created)
	require.NoError(t, err)
	require.NotEmpty(t, state)

	resumed, err := f.mgr.Resume(ctx, f.tenant, state, payload)
	require.NoError(t, err)
	require.Equal(t, created, resumed)

	// Resume only reads. The same state works again until something changes
	// the action.
	again, err := f.mgr.Resume(ctx, f.tenant, state, payload)
	require.NoError(t, err)
	require.Equal(t, created, again)
}

func TestResumePayloadChanged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("the exact message the user saw")
	state := f.state(p)

	changed := []byte("the exact message the user saW")
	_, err := f.mgr.Resume(ctx, f.tenant, state, changed)
	require.ErrorIs(t, err, ErrPayloadChanged)

	// A longer and a shorter payload are changed too, including an empty one.
	for _, other := range []string{"the exact message the user saw!", "the exact message the user sa", ""} {
		_, err = f.mgr.Resume(ctx, f.tenant, state, []byte(other))
		require.ErrorIs(t, err, ErrPayloadChanged, "payload %q", other)
	}

	// The wrong payloads did not use up the action.
	_, err = f.mgr.Resume(ctx, f.tenant, state, []byte("the exact message the user saw"))
	require.NoError(t, err)
}

func TestResumeExpiredState(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	state := f.state(f.create(string(payload)))

	f.clock.Set(testStart.Add(10*time.Minute - time.Second))
	_, err := f.mgr.Resume(ctx, f.tenant, state, payload)
	require.NoError(t, err, "a state is valid for its 10 minutes")

	f.clock.Set(testStart.Add(10*time.Minute + time.Second))
	_, err = f.mgr.Resume(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrStateExpired)
	_, err = f.mgr.Claim(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrStateExpired)
}

func TestResumeOtherTenantInvalid(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	other := account.TenantID("tenant-b")
	f.addTenant(other)
	payload := []byte("payload")
	p := f.create(string(payload))
	state := f.state(p)

	_, err := f.mgr.Resume(ctx, other, state, payload)
	require.ErrorIs(t, err, ErrStateInvalid)
	_, err = f.mgr.Claim(ctx, other, state, payload)
	require.ErrorIs(t, err, ErrStateInvalid)

	// A token that names tenant B and the id of an action of tenant A is signed
	// and has the right tenant, but tenant B has no such action.
	forged := f.forge(map[string]string{
		"id": p.ID, "hash": hex.EncodeToString(p.Hash[:]), "tenant": string(other),
	})
	_, err = f.mgr.Resume(ctx, other, forged, payload)
	require.ErrorIs(t, err, ErrStateInvalid)
	_, err = f.mgr.Claim(ctx, other, forged, payload)
	require.ErrorIs(t, err, ErrStateInvalid)

	// The failed tries changed nothing for the real tenant.
	claimed, err := f.mgr.Claim(ctx, f.tenant, state, payload)
	require.NoError(t, err)
	require.Equal(t, StateApproved, claimed.State)
}

func TestResumeTwiceFails(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	p := f.create(string(payload))
	state := f.state(p)

	_, err := f.mgr.Resume(ctx, f.tenant, state, payload)
	require.NoError(t, err)
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateDone, "sent-1"))

	_, err = f.mgr.Resume(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrNotPending)
}

func TestPendingExpiresAfter24h(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	p := f.create(string(payload))
	require.Equal(t, testStart.Add(24*time.Hour), p.ExpiresAt)

	// Just before the end the action is still waiting.
	f.clock.Set(testStart.Add(24*time.Hour - time.Second))
	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, StatePending, got.State)
	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	// A state made 5 minutes before the end is still a good token after the
	// end. Only the action has expired.
	f.clock.Set(testStart.Add(24*time.Hour - 5*time.Minute))
	state := f.state(p)
	f.clock.Set(testStart.Add(24*time.Hour + time.Second))

	got, gotPayload, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, StateExpired, got.State)
	require.Equal(t, payload, gotPayload)

	listed, err = f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Empty(t, listed)

	_, err = f.mgr.Resume(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrNotPending)
	_, err = f.mgr.Claim(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrNotPending)
	err = f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateApproved, "")
	require.ErrorIs(t, err, ErrNotPending)
	err = f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateExpired, "")
	require.ErrorIs(t, err, ErrNotPending)
}

func TestPayloadEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := "RAW-MIME wire 9876 euro to Zebra Corp, account GB29NWBK60161331926819"
	p := f.create(payload)

	files := f.dataFiles()
	// The scan must be able to see data, or "not found" would mean nothing.
	// The account address is stored as plain text.
	require.True(t, bytes.Contains(files, []byte("work@example.com")), "the scan does not see the data files")

	require.False(t, bytes.Contains(files, []byte(payload)))
	require.False(t, bytes.Contains(files, []byte("GB29NWBK60161331926819")))

	// The payload is still there for the code that is allowed to read it.
	_, got, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, []byte(payload), got)
}

func TestPreviewEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("payload")

	files := f.dataFiles()
	require.True(t, bytes.Contains(files, []byte("work@example.com")), "the scan does not see the data files")
	for _, secret := range []string{
		"bob@example.org", "carol@example.org", "Quarterly figures for Zebra",
		"the secret launch date", "me@example.com",
	} {
		require.False(t, bytes.Contains(files, []byte(secret)), "%q is stored as plain text", secret)
	}

	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, testPreview(), got.Preview)
}

func TestSealedValuesAreBoundToTheirPendingAction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("payload")
	other := f.create("another payload")

	row, err := f.store.GetPending(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	payloadVersion, sealedPayload, err := f.store.GetPendingPayload(ctx, f.tenant, p.ID)
	require.NoError(t, err)

	aad := func(tenant account.TenantID, acct account.AccountID, field string) string {
		return secrets.AAD(string(tenant), string(acct), field)
	}
	opened, err := f.vault.Decrypt(ctx, f.tenant, payloadVersion, aad(f.tenant, f.acct, "pending:"+p.ID), sealedPayload)
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), opened)
	preview, err := f.vault.Decrypt(ctx, f.tenant, row.PreviewKeyVersion, aad(f.tenant, f.acct, "pending-preview:"+p.ID), row.Preview)
	require.NoError(t, err)
	require.Contains(t, string(preview), "Quarterly figures for Zebra")

	// Neither value opens under the field of the other value, the field of
	// another action, or another account.
	for _, wrong := range []string{
		aad(f.tenant, f.acct, "pending-preview:"+p.ID),
		aad(f.tenant, f.acct, "pending:"+other.ID),
		aad(f.tenant, "someone-else", "pending:"+p.ID),
		aad("someone-else", f.acct, "pending:"+p.ID),
	} {
		_, err = f.vault.Decrypt(ctx, f.tenant, payloadVersion, wrong, sealedPayload)
		require.ErrorIs(t, err, secrets.ErrDecrypt, "aad %q", wrong)
	}
	_, err = f.vault.Decrypt(ctx, f.tenant, row.PreviewKeyVersion, aad(f.tenant, f.acct, "pending:"+p.ID), row.Preview)
	require.ErrorIs(t, err, secrets.ErrDecrypt)
}

func TestClaimMovesTheActionToApprovedOnce(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	p := f.create(string(payload))
	state := f.state(p)

	claimed, err := f.mgr.Claim(ctx, f.tenant, state, payload)
	require.NoError(t, err)
	want := *p
	want.State = StateApproved
	require.Equal(t, &want, claimed)

	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, StateApproved, got.State)

	// The action is used up. Claim, Resume and a list do not find it any more.
	_, err = f.mgr.Claim(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrNotPending)
	_, err = f.mgr.Resume(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrNotPending)
	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Empty(t, listed)

	// The caller finishes it with a compare-and-set from "approved".
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "sent-1"))
	got, _, err = f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, StateDone, got.State)
	require.Equal(t, "sent-1", got.ResultID)
}

func TestClaimWithChangedPayloadDoesNotUseUpTheAction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	p := f.create(string(payload))
	state := f.state(p)

	_, err := f.mgr.Claim(ctx, f.tenant, state, []byte("payload, but different"))
	require.ErrorIs(t, err, ErrPayloadChanged)
	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, StatePending, got.State)

	_, err = f.mgr.Claim(ctx, f.tenant, state, payload)
	require.NoError(t, err)
}

func TestClaimConcurrentCallersExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	p := f.create(string(payload))
	state := f.state(p)

	const callers = 16
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = f.mgr.Claim(ctx, f.tenant, state, payload)
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		require.ErrorIs(t, err, ErrNotPending)
	}
	require.Equal(t, 1, wins, "exactly one caller may claim the action")
}

func TestKindIsPartOfTheHash(t *testing.T) {
	ctx := context.Background()
	payload := []byte("same bytes")

	// The spec: SHA-256 over kind, a zero byte and the payload.
	require.Equal(t, sha256.Sum256([]byte("send\x00same bytes")), HashPayload(KindSend, payload))
	require.NotEqual(t, HashPayload(KindSend, payload), HashPayload(KindTrash, payload))
	// The zero byte keeps a kind and a payload from sliding into each other.
	require.NotEqual(t, HashPayload("send", []byte("x")), HashPayload("sendx", nil))

	f := newFixture(t)
	p := f.create(string(payload))
	state := f.state(p)

	// A manager that reads the same action as a "trash" action does not accept
	// the bytes that were approved as a "send".
	trashView := NewManager(kindFlipStore{PendingStore: f.store, kind: string(KindTrash)}, f.vault, signing.New(f.key), f.clock.Now)
	_, err := trashView.Resume(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrPayloadChanged)
	_, err = trashView.Claim(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrPayloadChanged)

	// The real manager accepts it.
	_, err = f.mgr.Resume(ctx, f.tenant, state, payload)
	require.NoError(t, err)
}

// kindFlipStore reports the kind of every pending action as kind.
type kindFlipStore struct {
	PendingStore
	kind string
}

func (k kindFlipStore) GetPending(ctx context.Context, tenant account.TenantID, id string) (*store.PendingRow, error) {
	row, err := k.PendingStore.GetPending(ctx, tenant, id)
	if row != nil {
		row.Kind = k.kind
	}
	return row, err
}

func TestRequestStateIsSignedAndHoldsOnlyIdHashAndTenant(t *testing.T) {
	f := newFixture(t)
	p := f.create("payload")
	state := f.state(p)

	signer := signing.New(f.key)
	signer.Now = f.clock.Now
	var body map[string]any
	require.NoError(t, signer.Open(signing.PurposeRequestState, state, &body))
	require.Equal(t, map[string]any{
		"id":     p.ID,
		"hash":   hex.EncodeToString(p.Hash[:]),
		"tenant": string(p.Tenant),
	}, body)
	// It is only for confirmations.
	require.ErrorIs(t, signer.Open(signing.PurposePage, state, nil), signing.ErrInvalid)

	// A token is readable by the agent, so nothing secret may be in it.
	decoded, err := base64.RawURLEncoding.DecodeString(state[:strings.LastIndexByte(state, '.')])
	require.NoError(t, err)
	for _, secret := range []string{"Zebra", "bob@example.org", "launch date", "payload"} {
		require.NotContains(t, string(decoded), secret)
	}

	_, err = f.mgr.RequestState(nil)
	require.Error(t, err)
}

func TestResumeRefusesBadStates(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")
	p := f.create(string(payload))
	good := f.state(p)
	hash := hex.EncodeToString(p.Hash[:])
	other := HashPayload(KindSend, []byte("other"))
	otherHash := hex.EncodeToString(other[:])

	// The same token with its last character changed.
	changed := good[:len(good)-1] + "A"
	if strings.HasSuffix(good, "A") {
		changed = good[:len(good)-1] + "B"
	}

	// A token with the right key for another job.
	signer := signing.New(f.key)
	signer.Now = f.clock.Now
	otherPurpose, err := signer.Seal(signing.PurposePage, map[string]string{"id": p.ID, "hash": hash, "tenant": string(f.tenant)}, time.Minute)
	require.NoError(t, err)

	tests := map[string]string{
		"empty":              "",
		"not a token":        "hello",
		"changed":            changed,
		"other purpose":      otherPurpose,
		"unknown action":     f.forge(map[string]string{"id": "nope", "hash": hash, "tenant": string(f.tenant)}),
		"empty id":           f.forge(map[string]string{"id": "", "hash": hash, "tenant": string(f.tenant)}),
		"id with a bar":      f.forge(map[string]string{"id": "a|b", "hash": hash, "tenant": string(f.tenant)}),
		"hash is not hex":    f.forge(map[string]string{"id": p.ID, "hash": "zz", "tenant": string(f.tenant)}),
		"hash is too short":  f.forge(map[string]string{"id": p.ID, "hash": "abcd", "tenant": string(f.tenant)}),
		"hash of another":    f.forge(map[string]string{"id": p.ID, "hash": otherHash, "tenant": string(f.tenant)}),
		"no fields":          f.forge(map[string]string{}),
		"json of other kind": f.forge([]int{1, 2, 3}),
	}
	for name, state := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := f.mgr.Resume(ctx, f.tenant, state, payload)
			require.ErrorIs(t, err, ErrStateInvalid)
			_, err = f.mgr.Claim(ctx, f.tenant, state, payload)
			require.ErrorIs(t, err, ErrStateInvalid)
		})
	}

	// None of this used up the action.
	_, err = f.mgr.Claim(ctx, f.tenant, good, payload)
	require.NoError(t, err)
}

func TestTransition(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	other := account.TenantID("tenant-b")
	f.addTenant(other)

	state := func(t *testing.T, id string) State {
		t.Helper()
		p, _, err := f.mgr.Get(ctx, f.tenant, id)
		require.NoError(t, err)
		return p.State
	}

	t.Run("compare and set", func(t *testing.T) {
		p := f.create("a")
		err := f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "")
		require.ErrorIs(t, err, ErrNotPending, "a wrong from state")
		require.Equal(t, StatePending, state(t, p.ID))

		require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateRejected, ""))
		require.Equal(t, StateRejected, state(t, p.ID))
		err = f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateRejected, "")
		require.ErrorIs(t, err, ErrNotPending, "the same change twice")
	})

	t.Run("result id", func(t *testing.T) {
		p := f.create("b")
		require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateApproved, ""))
		got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
		require.NoError(t, err)
		require.Empty(t, got.ResultID)

		require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "msg-7"))
		got, _, err = f.mgr.Get(ctx, f.tenant, p.ID)
		require.NoError(t, err)
		require.Equal(t, "msg-7", got.ResultID)
	})

	t.Run("an action that does not exist", func(t *testing.T) {
		err := f.mgr.Transition(ctx, f.tenant, "missing", StatePending, StateDone, "")
		require.ErrorIs(t, err, ErrNotPending)
		err = f.mgr.Transition(ctx, f.tenant, "a|b", StatePending, StateDone, "")
		require.ErrorIs(t, err, ErrNotPending)
	})

	t.Run("another tenant", func(t *testing.T) {
		p := f.create("c")
		err := f.mgr.Transition(ctx, other, p.ID, StatePending, StateDone, "")
		require.ErrorIs(t, err, ErrNotPending)
		require.Equal(t, StatePending, state(t, p.ID))
	})

	t.Run("targets", func(t *testing.T) {
		p := f.create("d")
		// "pending" is not a place to go to, and an unknown state is not either.
		require.Error(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StatePending, ""))
		require.Error(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, State("sent"), ""))
		require.Error(t, f.mgr.Transition(ctx, f.tenant, p.ID, State("sent"), StateDone, ""))
		// Going nowhere is not a change.
		require.Error(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateApproved, ""))
		require.Equal(t, StatePending, state(t, p.ID))

		// A finished action cannot be brought back, even by a caller that knows
		// its state.
		require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateDone, "r"))
		for _, to := range []State{StateApproved, StateRejected, StateExpired} {
			err := f.mgr.Transition(ctx, f.tenant, p.ID, StateDone, to, "")
			require.ErrorIs(t, err, ErrNotPending)
		}
		require.Equal(t, StateDone, state(t, p.ID))
	})
}

func TestGet(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	other := account.TenantID("tenant-b")
	f.addTenant(other)
	p := f.create("the payload")

	got, payload, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, p, got)
	require.Equal(t, []byte("the payload"), payload)

	for name, call := range map[string]func() error{
		"unknown id": func() error { _, _, err := f.mgr.Get(ctx, f.tenant, "missing"); return err },
		"bad id":     func() error { _, _, err := f.mgr.Get(ctx, f.tenant, "a|b"); return err },
		"empty id":   func() error { _, _, err := f.mgr.Get(ctx, f.tenant, ""); return err },
		"tenant":     func() error { _, _, err := f.mgr.Get(ctx, other, p.ID); return err },
	} {
		require.ErrorIs(t, call(), ErrNotFound, name)
	}
}

func TestListPendingIsNewestFirstAndScopedToTheTenant(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	otherTenant := account.TenantID("tenant-b")
	otherAcct := f.addTenant(otherTenant)

	first := f.create("1")
	f.clock.Set(testStart.Add(time.Minute))
	second := f.create("2")
	f.clock.Set(testStart.Add(2 * time.Minute))
	finished := f.create("3")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, finished.ID, StatePending, StateDone, ""))
	f.clock.Set(testStart.Add(3 * time.Minute))
	third := f.create("4")
	foreign, err := f.mgr.Create(ctx, otherTenant, otherAcct, KindTrash, []byte("5"), Preview{Count: 3})
	require.NoError(t, err)

	got, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Equal(t, []*Pending{third, second, first}, got)

	got, err = f.mgr.ListPending(ctx, otherTenant)
	require.NoError(t, err)
	require.Equal(t, []*Pending{foreign}, got)

	got, err = f.mgr.ListPending(ctx, "nobody")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestCreateIDs(t *testing.T) {
	f := newFixture(t)
	seen := make(map[string]bool)
	for range 20 {
		id := f.create("x").ID
		require.Regexp(t, `^[a-z2-7]{26}$`, id)
		require.False(t, seen[id], "ids must not repeat")
		seen[id] = true
	}
}

func TestCreateRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	_, err := f.mgr.Create(ctx, f.tenant, f.acct, Kind("format_disk"), []byte("x"), testPreview())
	require.Error(t, err)
	_, err = f.mgr.Create(ctx, f.tenant, f.acct, KindSend, nil, testPreview())
	require.Error(t, err)
	_, err = f.mgr.Create(ctx, f.tenant, "no-such-account", KindSend, []byte("x"), testPreview())
	require.Error(t, err)
	_, err = f.mgr.Create(ctx, f.tenant, "a|b", KindSend, []byte("x"), testPreview())
	require.Error(t, err)

	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Empty(t, listed, "a refused action must leave nothing behind")
}

func TestCreateKeepsAtMost500CharactersOfTheBody(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	create := func(body string) *Pending {
		p := testPreview()
		p.BodyStart = body
		got, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, []byte("x"), p)
		require.NoError(t, err)
		return got
	}

	exact := strings.Repeat("é", 500)
	require.Equal(t, exact, create(exact).Preview.BodyStart, "500 characters are kept as they are")

	long := strings.Repeat("é", 600)
	p := create(long)
	require.Equal(t, exact, p.Preview.BodyStart, "characters count, not bytes")

	// What is stored is what Create returned.
	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, p, got)
}
