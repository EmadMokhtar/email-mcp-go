package confirm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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

// create makes a pending send in the fixture account that the user answers in
// band, so a request state can claim it. It lives 10 minutes.
func (f *fixture) create(payload string) *Pending {
	f.t.Helper()
	p, err := f.mgr.Create(context.Background(), f.tenant, f.acct, KindSend, ModeInBand, []byte(payload), testPreview())
	require.NoError(f.t, err)
	return p
}

// createApproval makes a pending send in the fixture account that the user
// answers on the web page. It lives 24 hours, and no request state can claim
// it.
func (f *fixture) createApproval(payload string) *Pending {
	f.t.Helper()
	p, err := f.mgr.Create(context.Background(), f.tenant, f.acct, KindSend, ModeApproval, []byte(payload), testPreview())
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

// openDB opens a second connection to the database file. The store has no
// exported method for some things that a test needs, such as a tenant other
// than "local" or a damaged row, so the test writes them itself. The caller
// closes the handle.
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

// addTenant adds a tenant with one account, for isolation tests.
func (f *fixture) addTenant(id account.TenantID) account.AccountID {
	f.t.Helper()
	f.exec(`INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	a := account.NewIMAPAccount(id, "other@example.com", account.MailServer{}, testStart)
	require.NoError(f.t, f.store.CreateAccount(context.Background(), a))
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

	created, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeInBand, payload, testPreview())
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
	p := f.createApproval(string(payload))
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

func TestClaimPutsTheHashOfThePayloadIntoTheClaimStatement(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	approved := []byte("the message the user approved")
	attack := []byte("a different message")
	p := f.create(string(approved))

	// Make the read in Claim stale: it shows the hash of the other payload, as
	// if the row had been changed after the read. A token that carries the same
	// hash gets past every check that works on the read.
	fake := HashPayload(KindSend, attack)
	stale := NewManager(staleReadStore{PendingStore: f.store, hash: fake[:]}, f.vault, signing.New(f.key), f.clock.Now)
	state := f.forge(map[string]string{
		"id": p.ID, "hash": hex.EncodeToString(fake[:]), "tenant": string(f.tenant),
	})
	_, err := stale.Resume(ctx, f.tenant, state, attack)
	require.NoError(t, err, "the stale read must get past the checks that work on the read")

	// The claim statement still refuses, because the stored hash is not the
	// hash of this payload. The action is not used up.
	_, err = stale.Claim(ctx, f.tenant, state, attack)
	require.ErrorIs(t, err, ErrNotPending)
	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, StatePending, got.State)

	// The real approval still works.
	_, err = f.mgr.Claim(ctx, f.tenant, f.state(p), approved)
	require.NoError(t, err)
}

// staleReadStore shows the hash of every pending action as hash.
type staleReadStore struct {
	PendingStore
	hash []byte
}

func (k staleReadStore) GetPending(ctx context.Context, tenant account.TenantID, id string) (*store.PendingRow, error) {
	row, err := k.PendingStore.GetPending(ctx, tenant, id)
	if row != nil {
		row.Hash = k.hash
	}
	return row, err
}

func TestGetRefusesAnActionWhoseStoredDataDoesNotMatchItsHash(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	other := KindTrash
	otherHash := HashPayload(KindSend, []byte("another payload"))

	for name, damage := range map[string]func(id string){
		"kind": func(id string) {
			f.exec(`UPDATE pending_actions SET kind = ? WHERE id = ?`, string(other), id)
		},
		"hash": func(id string) {
			f.exec(`UPDATE pending_actions SET hash = ? WHERE id = ?`, otherHash[:], id)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.create("the payload " + name)

			// Before the damage, Get works.
			_, payload, err := f.mgr.Get(ctx, f.tenant, p.ID)
			require.NoError(t, err)
			require.Equal(t, []byte("the payload "+name), payload)

			damage(p.ID)
			got, payload, err := f.mgr.Get(ctx, f.tenant, p.ID)
			require.ErrorIs(t, err, ErrCorrupted)
			require.Nil(t, got)
			require.Nil(t, payload, "a payload that does not match its hash must not be returned")
		})
	}
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
	foreign, err := f.mgr.Create(ctx, otherTenant, otherAcct, KindTrash, ModeApproval, []byte("5"), Preview{Count: 3})
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

	_, err := f.mgr.Create(ctx, f.tenant, f.acct, Kind("format_disk"), ModeApproval, []byte("x"), testPreview())
	require.Error(t, err)
	_, err = f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeApproval, nil, testPreview())
	require.Error(t, err)
	_, err = f.mgr.Create(ctx, f.tenant, "no-such-account", KindSend, ModeApproval, []byte("x"), testPreview())
	require.Error(t, err)
	_, err = f.mgr.Create(ctx, f.tenant, "a|b", KindSend, ModeApproval, []byte("x"), testPreview())
	require.Error(t, err)

	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Empty(t, listed, "a refused action must leave nothing behind")
}

// A Trash preview keeps at most 20 message lines. The count stays the number of
// messages, so a channel can say how many are not listed.
func TestCreateKeepsAtMost20MessageLines(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	lines := make([]string, 25)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	p, err := f.mgr.Create(ctx, f.tenant, f.acct, KindTrash, ModeApproval, []byte("r1"),
		Preview{Account: "work", Count: 30, Messages: lines})
	require.NoError(t, err)

	require.Equal(t, lines[:20], p.Preview.Messages)
	require.Equal(t, "… and 10 more not shown", p.Preview.MoreMessagesText())
	lines[0] = "changed by the caller"
	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, "line 0", got.Preview.Messages[0], "Create keeps its own copy, and stores it")
}

func TestCreateKeepsAtMost500CharactersOfTheBody(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	create := func(body string) *Pending {
		p := testPreview()
		p.BodyStart = body
		got, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeApproval, []byte("x"), p)
		require.NoError(t, err)
		return got
	}

	exact := strings.Repeat("é", 500)
	require.Equal(t, exact, create(exact).Preview.BodyStart, "500 characters are kept as they are")

	long := strings.Repeat("é", 600)
	p := create(long)
	require.Equal(t, exact, p.Preview.BodyStart, "characters count, not bytes")
	require.Equal(t, 100, p.Preview.BodyMore, "what Create cuts counts as not shown")
	require.Equal(t, "… and 100 more characters not shown", p.Preview.MoreCharsText())

	// What is stored is what Create returned.
	got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Equal(t, p, got)
}

// sealedColumns reads the sealed payload and the sealed preview of an action
// straight from the table. A column that is NULL is returned as nil.
func (f *fixture) sealedColumns(id string) (payload, preview []byte) {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	err := db.QueryRowContext(context.Background(),
		`SELECT payload, preview FROM pending_actions WHERE id = ?`, id).Scan(&payload, &preview)
	require.NoError(f.t, err)
	return payload, preview
}

// storedState reads the state column straight from the table.
func (f *fixture) storedState(id string) string {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	var state string
	require.NoError(f.t, db.QueryRowContext(context.Background(),
		`SELECT state FROM pending_actions WHERE id = ?`, id).Scan(&state))
	return state
}

// checkpoint moves the write-ahead log into the database file and empties the
// log. After it, a scan of the data directory sees the same data as the database
// file holds, and no older copy in the log.
func (f *fixture) checkpoint() {
	f.t.Helper()
	f.exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
}

// approve claims an action the way the server does, after the user accepted.
func (f *fixture) approve(p *Pending, payload string) {
	f.t.Helper()
	_, err := f.mgr.Claim(context.Background(), f.tenant, f.state(p), []byte(payload))
	require.NoError(f.t, err)
}

// An action that has ended keeps no secret. Each way to end an action sets the
// sealed payload and the sealed preview to NULL, and the bytes are gone from the
// data files too (not only unreachable through a query).
func TestEndedActionsKeepNoSecret(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// This action stays live. The scan must still see its sealed values after
	// the others are gone. Without this, "not found" would prove nothing.
	live := f.create("payload of the live action")
	livePayload, livePreview := f.sealedColumns(live.ID)
	require.NotEmpty(t, livePayload)
	require.NotEmpty(t, livePreview)

	ends := map[string]func(p *Pending, payload string){
		"rejected": func(p *Pending, _ string) {
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateRejected, ""))
		},
		"expired": func(p *Pending, _ string) {
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateExpired, ""))
		},
		"done": func(p *Pending, payload string) {
			f.approve(p, payload)
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "msg-1"))
		},
		"rejected after approval": func(p *Pending, payload string) {
			f.approve(p, payload)
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateRejected, ""))
		},
		"expired after approval": func(p *Pending, payload string) {
			f.approve(p, payload)
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateExpired, ""))
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			payload := "payload that ends as " + name
			p := f.create(payload)
			sealedPayload, sealedPreview := f.sealedColumns(p.ID)
			require.NotEmpty(t, sealedPayload)
			require.NotEmpty(t, sealedPreview)

			// While the action lives, the scan sees both sealed values.
			f.checkpoint()
			files := f.dataFiles()
			require.True(t, bytes.Contains(files, sealedPayload), "the scan does not see the sealed payload")
			require.True(t, bytes.Contains(files, sealedPreview), "the scan does not see the sealed preview")

			end(p, payload)

			gotPayload, gotPreview := f.sealedColumns(p.ID)
			require.Nil(t, gotPayload, "payload column")
			require.Nil(t, gotPreview, "preview column")
			f.checkpoint()
			files = f.dataFiles()
			require.False(t, bytes.Contains(files, sealedPayload), "the sealed payload is still in the data files")
			require.False(t, bytes.Contains(files, sealedPreview), "the sealed preview is still in the data files")
			require.True(t, bytes.Contains(files, livePayload), "the live action lost its payload")
			require.True(t, bytes.Contains(files, livePreview), "the live action lost its preview")
		})
	}

	// The live action is not touched.
	_, payload, err := f.mgr.Get(ctx, f.tenant, live.ID)
	require.NoError(t, err)
	require.Equal(t, []byte("payload of the live action"), payload)
}

// An action that is only approved is not at its end. The server still needs its
// payload.
func TestApprovedActionStillHasItsSecrets(t *testing.T) {
	f := newFixture(t)
	p := f.create("payload")
	f.approve(p, "payload")

	payload, preview := f.sealedColumns(p.ID)
	require.NotEmpty(t, payload)
	require.NotEmpty(t, preview)
}

// A waiting action that is past its time is recorded as expired, and its
// secrets are deleted, when a Claim or a Transition meets it. A read does not
// change it.
func TestClaimAndTransitionRecordTheExpiryAndClearTheSecrets(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("payload")

	attempts := map[string]func(p *Pending, state string) error{
		"claim": func(_ *Pending, state string) error {
			_, err := f.mgr.Claim(ctx, f.tenant, state, payload)
			return err
		},
		"transition to approved": func(p *Pending, _ string) error {
			return f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateApproved, "")
		},
		"transition to rejected": func(p *Pending, _ string) error {
			return f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateRejected, "")
		},
	}
	for name, attempt := range attempts {
		t.Run(name, func(t *testing.T) {
			f.clock.Set(testStart)
			p := f.create(string(payload))
			// A state made 5 minutes before the end is a good token after the end.
			f.clock.Set(p.ExpiresAt.Add(-5 * time.Minute))
			state := f.state(p)
			f.clock.Set(p.ExpiresAt.Add(time.Second))

			// Reading changes nothing.
			_, err := f.mgr.Resume(ctx, f.tenant, state, payload)
			require.ErrorIs(t, err, ErrNotPending)
			got, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
			require.NoError(t, err)
			require.Equal(t, StateExpired, got.State)
			_, err = f.mgr.ListPending(ctx, f.tenant)
			require.NoError(t, err)
			require.Equal(t, "pending", f.storedState(p.ID))
			storedPayload, storedPreview := f.sealedColumns(p.ID)
			require.NotEmpty(t, storedPayload)
			require.NotEmpty(t, storedPreview)

			// The attempt fails, and records the expiry.
			require.ErrorIs(t, attempt(p, state), ErrNotPending)
			require.Equal(t, "expired", f.storedState(p.ID))
			storedPayload, storedPreview = f.sealedColumns(p.ID)
			require.Nil(t, storedPayload)
			require.Nil(t, storedPreview)

			// The action reads back as expired, without a payload.
			got, gotPayload, err := f.mgr.Get(ctx, f.tenant, p.ID)
			require.NoError(t, err)
			require.Equal(t, StateExpired, got.State)
			require.Nil(t, gotPayload)
		})
	}
}

// A refused claim of a live action must not look like an expiry.
func TestAClaimThatFailsOnALiveActionKeepsIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("payload")

	_, err := f.mgr.Claim(ctx, f.tenant, f.state(p), []byte("another payload"))
	require.ErrorIs(t, err, ErrPayloadChanged)

	require.Equal(t, "pending", f.storedState(p.ID))
	payload, preview := f.sealedColumns(p.ID)
	require.NotEmpty(t, payload)
	require.NotEmpty(t, preview)
}

// Get of an action that has ended returns the action and no payload. It is not
// an error: the caller asks what happened to the action, and the answer is in
// State and ResultID. A caller that wants to act on the payload must check that
// the payload is not nil.
func TestGetOfAnEndedActionReturnsItWithoutAPayload(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("payload")
	state := f.state(p)
	f.approve(p, "payload")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "msg-1"))

	got, payload, err := f.mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Nil(t, payload)
	require.Equal(t, StateDone, got.State)
	require.Equal(t, "msg-1", got.ResultID)
	require.Equal(t, p.ID, got.ID)
	require.Equal(t, f.tenant, got.Tenant)
	require.Equal(t, f.acct, got.Account)
	require.Equal(t, KindSend, got.Kind)
	require.Equal(t, p.Hash, got.Hash)
	require.Equal(t, p.CreatedAt, got.CreatedAt)
	require.Equal(t, p.ExpiresAt, got.ExpiresAt)
	require.Equal(t, Preview{}, got.Preview, "the preview is deleted")

	// The other calls work on an ended action too, and refuse it.
	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Empty(t, listed)
	_, err = f.mgr.Resume(ctx, f.tenant, state, []byte("payload"))
	require.ErrorIs(t, err, ErrNotPending)
	_, err = f.mgr.Claim(ctx, f.tenant, state, []byte("payload"))
	require.ErrorIs(t, err, ErrNotPending)
}

// A waiting or approved action must have its payload and its preview. If one is
// gone, the storage is damaged. The calls say so with an error, and do not
// panic.
func TestAWaitingActionThatLostASecretIsCorrupted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	t.Run("preview", func(t *testing.T) {
		p := f.create("payload")
		f.execIgnoringChecks(`UPDATE pending_actions SET preview = NULL WHERE id = ?`, p.ID)

		_, _, err := f.mgr.Get(ctx, f.tenant, p.ID)
		require.ErrorIs(t, err, ErrCorrupted)
		_, err = f.mgr.ListPending(ctx, f.tenant)
		require.ErrorIs(t, err, ErrCorrupted)
		_, err = f.mgr.Resume(ctx, f.tenant, f.state(p), []byte("payload"))
		require.ErrorIs(t, err, ErrCorrupted)
		_, err = f.mgr.Claim(ctx, f.tenant, f.state(p), []byte("payload"))
		require.ErrorIs(t, err, ErrCorrupted)
		require.Equal(t, "pending", f.storedState(p.ID), "a failed claim leaves the action")
	})
	t.Run("payload", func(t *testing.T) {
		p := f.create("payload two")
		f.execIgnoringChecks(`UPDATE pending_actions SET payload = NULL WHERE id = ?`, p.ID)

		_, payload, err := f.mgr.Get(ctx, f.tenant, p.ID)
		require.ErrorIs(t, err, ErrCorrupted)
		require.Nil(t, payload)
	})
}

// Prune deletes the ended actions that are older than the given age, and the
// waiting actions that are past their time and older than the age. It keeps the
// rest.
func TestPruneDeletesOnlyOldEndedActions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	other := account.TenantID("tenant-b")
	otherAcct := f.addTenant(other)

	// Old actions: made at testStart.
	f.clock.Set(testStart)
	oldDone := f.create("old done")
	f.approve(oldDone, "old done")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, oldDone.ID, StateApproved, StateDone, "m"))
	oldRejected := f.create("old rejected")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, oldRejected.ID, StatePending, StateRejected, ""))
	oldExpired := f.create("old expired")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, oldExpired.ID, StatePending, StateExpired, ""))
	oldWaiting := f.create("old waiting") // nobody answers: it is past its time later
	oldApproved := f.create("old approved")
	f.approve(oldApproved, "old approved")
	otherOld, err := f.mgr.Create(ctx, other, otherAcct, KindSend, ModeApproval, []byte("other tenant"), testPreview())
	require.NoError(t, err)
	require.NoError(t, f.mgr.Transition(ctx, other, otherOld.ID, StatePending, StateRejected, ""))

	// Recent actions: made 72 hours later.
	f.clock.Set(testStart.Add(72 * time.Hour))
	newDone := f.create("new done")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, newDone.ID, StatePending, StateDone, ""))
	newWaiting := f.create("new waiting")

	// One hour later, delete what is older than 48 hours.
	f.clock.Set(testStart.Add(73 * time.Hour))
	n, err := f.mgr.Prune(ctx, 48*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 5, n, "old done, rejected, expired, waiting past its time, and the other tenant's")

	for _, gone := range []*Pending{oldDone, oldRejected, oldExpired, oldWaiting} {
		_, _, err := f.mgr.Get(ctx, f.tenant, gone.ID)
		require.ErrorIs(t, err, ErrNotFound, gone.ID)
	}
	_, _, err = f.mgr.Get(ctx, other, otherOld.ID)
	require.ErrorIs(t, err, ErrNotFound, "Prune works across tenants")
	for _, kept := range []*Pending{oldApproved, newDone, newWaiting} {
		_, _, err := f.mgr.Get(ctx, f.tenant, kept.ID)
		require.NoError(t, err, kept.ID)
	}

	n, err = f.mgr.Prune(ctx, 48*time.Hour)
	require.NoError(t, err)
	require.Zero(t, n)
}

// A waiting action that is old but still has time is not deleted: the user can
// still confirm it.
func TestPruneKeepsAWaitingActionThatStillHasTime(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	waiting := f.createApproval("waiting")
	ended := f.createApproval("ended")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, ended.ID, StatePending, StateRejected, ""))

	f.clock.Set(testStart.Add(2 * time.Hour))
	n, err := f.mgr.Prune(ctx, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	_, _, err = f.mgr.Get(ctx, f.tenant, waiting.ID)
	require.NoError(t, err)
	_, _, err = f.mgr.Get(ctx, f.tenant, ended.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPruneRefusesANegativeAge(t *testing.T) {
	f := newFixture(t)
	f.create("payload")

	_, err := f.mgr.Prune(context.Background(), -time.Second)
	require.Error(t, err)
	require.Equal(t, 1, len(f.rowIDs()))
}

// rowIDs returns the ids of all rows in the table.
func (f *fixture) rowIDs() []string {
	f.t.Helper()
	db := f.openDB()
	defer func() { require.NoError(f.t, db.Close()) }()
	rows, err := db.QueryContext(context.Background(), `SELECT id FROM pending_actions`)
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(f.t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(f.t, rows.Err())
	return ids
}

// ---- Fix round 1: false corruption reports, lost writes, failing expiry ----

// execIgnoringChecks runs one statement on a second connection that does not
// enforce the CHECK constraints of the tables. It makes the damaged rows that
// the table rules would refuse.
func (f *fixture) execIgnoringChecks(query string, args ...any) {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)&_pragma=ignore_check_constraints(1)")
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err = db.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err)
}

// betweenReadsStore runs between runs just before the payload of an action is
// read. Get reads the row first and the payload second, so this is the moment
// at which a concurrent call can change the action.
type betweenReadsStore struct {
	PendingStore
	between func()
}

func (s betweenReadsStore) GetPendingPayload(ctx context.Context, tenant account.TenantID, id string) (int, []byte, error) {
	s.between()
	return s.PendingStore.GetPendingPayload(ctx, tenant, id)
}

func (f *fixture) managerOn(st PendingStore) *Manager {
	return NewManager(st, f.vault, signing.New(f.key), f.clock.Now)
}

// An action can end between the two reads of Get. This is the normal flow: the
// server finishes the send and moves the action to "done" while another caller
// reads it. Get must not report damage then. It returns the action as it is
// now, without a payload.
func TestGetOfAnActionThatEndsBetweenTheReadsIsNotCorrupted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("payload")
	f.approve(p, "payload")

	var once sync.Once
	mgr := f.managerOn(betweenReadsStore{PendingStore: f.store, between: func() {
		once.Do(func() {
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "msg-1"))
		})
	}})

	got, payload, err := mgr.Get(ctx, f.tenant, p.ID)
	require.NoError(t, err)
	require.Nil(t, payload)
	require.Equal(t, StateDone, got.State, "the state of the second read")
	require.Equal(t, "msg-1", got.ResultID)
	require.Equal(t, p.Hash, got.Hash)
	require.Equal(t, Preview{}, got.Preview)
}

func TestGetOfAnActionThatIsDeletedBetweenTheReadsIsNotFound(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	old := f.create("old")

	var once sync.Once
	mgr := f.managerOn(betweenReadsStore{PendingStore: f.store, between: func() {
		once.Do(func() { f.exec(`DELETE FROM pending_actions WHERE id = ?`, old.ID) })
	}})

	_, _, err := mgr.Get(ctx, f.tenant, old.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

// A waiting action that has lost its payload is damaged, also when Get reads it
// again after it finds the payload missing.
func TestGetOfAWaitingActionWithoutAPayloadIsStillCorrupted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := f.create("payload")
	f.approve(p, "payload")

	var once sync.Once
	mgr := f.managerOn(betweenReadsStore{PendingStore: f.store, between: func() {
		once.Do(func() { f.execIgnoringChecks(`UPDATE pending_actions SET payload = NULL WHERE id = ?`, p.ID) })
	}})

	_, payload, err := mgr.Get(ctx, f.tenant, p.ID)
	require.ErrorIs(t, err, ErrCorrupted)
	require.Nil(t, payload)
}

// commitThenCancelStore commits a claim or a move of state and then ends the
// context of the caller, before the call returns. It returns the error of the
// context, as the driver does when the context ends after the statement ran and
// before the answer came back. The change is committed, and the call says it
// failed.
type commitThenCancelStore struct {
	PendingStore
	cancel context.CancelFunc
}

func (s commitThenCancelStore) ClaimPending(ctx context.Context, tenant account.TenantID, id string, hash []byte, now time.Time) error {
	err := s.PendingStore.ClaimPending(context.WithoutCancel(ctx), tenant, id, hash, now)
	return s.report(ctx, err)
}

func (s commitThenCancelStore) TransitionPending(ctx context.Context, tenant account.TenantID, id, from, to, resultID string, now time.Time) error {
	err := s.PendingStore.TransitionPending(context.WithoutCancel(ctx), tenant, id, from, to, resultID, now)
	return s.report(ctx, err)
}

func (s commitThenCancelStore) report(ctx context.Context, err error) error {
	s.cancel()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// If the context ends while the claim statement runs, the claim may be
// committed. Claim must not report a failure for it: the action would stay
// "approved" with its payload and preview for ever, and the caller would not
// act on it.
func TestClaimIsNotLostWhenTheContextEndsDuringIt(t *testing.T) {
	f := newFixture(t)
	p := f.create("payload")
	state := f.state(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := f.managerOn(commitThenCancelStore{PendingStore: f.store, cancel: cancel})

	got, err := mgr.Claim(ctx, f.tenant, state, []byte("payload"))
	require.NoError(t, err)
	require.Equal(t, StateApproved, got.State)
	require.Equal(t, p.ID, got.ID)
	require.Equal(t, "approved", f.storedState(p.ID))
}

// The move to a final state is the same: when the statement ran, the caller
// learns it.
func TestTransitionIsNotLostWhenTheContextEndsDuringIt(t *testing.T) {
	f := newFixture(t)
	p := f.create("payload")
	f.approve(p, "payload")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := f.managerOn(commitThenCancelStore{PendingStore: f.store, cancel: cancel})

	require.NoError(t, mgr.Transition(ctx, f.tenant, p.ID, StateApproved, StateDone, "msg-1"))
	require.Equal(t, "done", f.storedState(p.ID))
	payload, preview := f.sealedColumns(p.ID)
	require.Nil(t, payload)
	require.Nil(t, preview)
}

// A caller whose context has ended must not approve an action. A move to
// "approved" is refused before it starts, and nothing changes.
func TestApproveWithAnEndedContextChangesNothing(t *testing.T) {
	f := newFixture(t)
	p := f.create("payload")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateApproved, "")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "pending", f.storedState(p.ID))
}

// A move to a final state always runs, also after the context of the caller has
// ended. The usual flow is: Claim, send the message (which can take minutes),
// then move the action to "done". If the client leaves during the send, the
// action must still end, and the payload and the preview must be deleted. An
// action that stays "approved" keeps its secrets for ever, because Prune never
// deletes it.
func TestFinalMoveRunsAfterTheContextHasEnded(t *testing.T) {
	cases := map[string]struct {
		from, to State
		resultID string
	}{
		"approved to done":     {StateApproved, StateDone, "msg-1"},
		"approved to rejected": {StateApproved, StateRejected, ""},
		"approved to expired":  {StateApproved, StateExpired, ""},
		"pending to rejected":  {StatePending, StateRejected, ""},
		"pending to expired":   {StatePending, StateExpired, ""},
		"pending to done":      {StatePending, StateDone, "msg-2"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			p := f.create("payload")
			if c.from == StateApproved {
				f.approve(p, "payload")
			}
			sealedPayload, sealedPreview := f.sealedColumns(p.ID)
			require.NotEmpty(t, sealedPayload)
			require.NotEmpty(t, sealedPreview)

			// The client has left: the context of the call has ended already.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, c.from, c.to, c.resultID))

			require.Equal(t, string(c.to), f.storedState(p.ID))
			payload, preview := f.sealedColumns(p.ID)
			require.Nil(t, payload, "payload column")
			require.Nil(t, preview, "preview column")
			got, gotPayload, err := f.mgr.Get(context.Background(), f.tenant, p.ID)
			require.NoError(t, err)
			require.Nil(t, gotPayload)
			require.Equal(t, c.to, got.State)
			require.Equal(t, c.resultID, got.ResultID)
		})
	}
}

// expireFailsStore cannot record an expiry.
type expireFailsStore struct{ PendingStore }

func (expireFailsStore) ExpirePending(context.Context, account.TenantID, string, time.Time) error {
	return errors.New("the database failed")
}

// An action past its time is "not pending" for a Claim or a Transition, also
// when the expiry cannot be recorded. The caller gets ErrNotPending, not a
// generic database error. The recording is housekeeping.
func TestAnExpiryThatCannotBeRecordedStillGivesErrNotPending(t *testing.T) {
	ctx := context.Background()
	payload := []byte("payload")

	t.Run("Claim, the call of the manager fails", func(t *testing.T) {
		f := newFixture(t)
		p := f.create(string(payload))
		f.clock.Set(p.ExpiresAt.Add(-5 * time.Minute))
		state := f.state(p)
		f.clock.Set(p.ExpiresAt.Add(time.Second))
		mgr := f.managerOn(expireFailsStore{f.store})

		_, err := mgr.Claim(ctx, f.tenant, state, payload)
		require.ErrorIs(t, err, ErrNotPending)
	})

	// Here the store itself fails to record the expiry: a trigger refuses the
	// UPDATE to the state "expired".
	for name, attempt := range map[string]func(f *fixture, p *Pending, state string) error{
		"Claim": func(f *fixture, _ *Pending, state string) error {
			_, err := f.mgr.Claim(ctx, f.tenant, state, payload)
			return err
		},
		"Transition": func(f *fixture, p *Pending, _ string) error {
			return f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateApproved, "")
		},
	} {
		t.Run(name+", the store fails", func(t *testing.T) {
			f := newFixture(t)
			p := f.create(string(payload))
			f.clock.Set(p.ExpiresAt.Add(-5 * time.Minute))
			state := f.state(p)
			f.clock.Set(p.ExpiresAt.Add(time.Second))
			f.exec(`CREATE TRIGGER no_expire BEFORE UPDATE ON pending_actions
				WHEN NEW.state = 'expired' BEGIN SELECT RAISE(ABORT, 'expire failed'); END`)

			err := attempt(f, p, state)

			require.ErrorIs(t, err, ErrNotPending)
			require.Equal(t, "pending", f.storedState(p.ID), "nothing was recorded")
		})
	}
}

// An action that the user confirms in band lives as long as its request state.
// After that it cannot be approved on the web page either, so a second action
// for the same message cannot be approved next to it. An action for the web page
// keeps its 24 hours.
func TestCreateLifetime(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	require.Equal(t, 24*time.Hour, ApprovalLifetime)
	require.Equal(t, 10*time.Minute, InBandLifetime)

	web, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeApproval, []byte("web"), testPreview())
	require.NoError(t, err)
	require.Equal(t, web.CreatedAt.Add(24*time.Hour), web.ExpiresAt)
	inBand, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeInBand, []byte("in band"), testPreview())
	require.NoError(t, err)
	require.Equal(t, inBand.CreatedAt.Add(10*time.Minute), inBand.ExpiresAt)
	state := f.state(inBand)

	f.clock.Set(testStart.Add(10*time.Minute + time.Millisecond))

	got, _, err := f.mgr.Get(ctx, f.tenant, inBand.ID)
	require.NoError(t, err)
	require.Equal(t, StateExpired, got.State)
	require.ErrorIs(t, f.mgr.Transition(ctx, f.tenant, inBand.ID, StatePending, StateApproved, ""), ErrNotPending,
		"the approvals page cannot approve it any more")
	_, err = f.mgr.Claim(ctx, f.tenant, state, []byte("in band"))
	require.Error(t, err)

	got, _, err = f.mgr.Get(ctx, f.tenant, web.ID)
	require.NoError(t, err)
	require.Equal(t, StatePending, got.State, "an action for the web page still waits")
	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, web.ID, listed[0].ID)

	// A test may make an action that lives shorter than its mode allows, never
	// longer.
	short, err := f.mgr.createWithLifetime(ctx, f.tenant, f.acct, KindSend, ModeApproval, []byte("short"), testPreview(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, short.CreatedAt.Add(time.Minute), short.ExpiresAt)
	cases := map[Mode][]time.Duration{
		ModeApproval: {0, -time.Minute, ApprovalLifetime + time.Millisecond},
		ModeInBand:   {0, -time.Minute, InBandLifetime + time.Millisecond, ApprovalLifetime},
	}
	for mode, lifetimes := range cases {
		for _, bad := range lifetimes {
			_, err := f.mgr.createWithLifetime(ctx, f.tenant, f.acct, KindSend, mode, []byte("x"), testPreview(), bad)
			require.Error(t, err, "mode %s, lifetime %s", mode, bad)
		}
	}
}

// The mode says how the user answers an action. Create stores it, and every
// read gives it back. A mode that the package does not know is refused.
func TestCreateStoresTheMode(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	for _, mode := range []Mode{ModeInBand, ModeApproval} {
		created, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, mode, []byte(string(mode)), testPreview())
		require.NoError(t, err)
		require.Equal(t, mode, created.Mode)
		got, _, err := f.mgr.Get(ctx, f.tenant, created.ID)
		require.NoError(t, err)
		require.Equal(t, mode, got.Mode)
	}
	listed, err := f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.ElementsMatch(t, []Mode{ModeInBand, ModeApproval}, []Mode{listed[0].Mode, listed[1].Mode})

	for _, bad := range []Mode{"", "web", "APPROVAL"} {
		_, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, bad, []byte("x"), testPreview())
		require.Error(t, err, "mode %q", bad)
	}
	listed, err = f.mgr.ListPending(ctx, f.tenant)
	require.NoError(t, err)
	require.Len(t, listed, 2, "a refused mode leaves nothing behind")
}

func TestPreviewKeepsTheAttachmentNames(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	p := testPreview()
	p.Attachments = []string{"figures.xlsx", "photo.png"}

	created, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeApproval, []byte("x"), p)
	require.NoError(t, err)
	p.Attachments[0] = "changed.exe"

	require.Equal(t, []string{"figures.xlsx", "photo.png"}, created.Preview.Attachments, "Create keeps its own copy")
	got, _, err := f.mgr.Get(ctx, f.tenant, created.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"figures.xlsx", "photo.png"}, got.Preview.Attachments, "the names are stored with the preview")
}

// An action that the server approved and never ended (the program stopped in
// between) is expired once its time to be approved is over by more than the
// grace time. A live send can still finish it before that.
func TestExpireStaleApprovedEndsOnlyApprovalsThatNothingCanFinish(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// In band: it lives 10 minutes, and the user approves it at once.
	f.clock.Set(testStart)
	inBand, err := f.mgr.Create(ctx, f.tenant, f.acct, KindSend, ModeInBand, []byte("in band"), testPreview())
	require.NoError(t, err)
	f.approve(inBand, "in band")
	finished := f.create("finished")
	f.approve(finished, "finished")
	require.NoError(t, f.mgr.Transition(ctx, f.tenant, finished.ID, StateApproved, StateDone, "item-1"))
	waiting := f.createApproval("waiting")

	// 10 minutes of life and 15 minutes of grace have not passed yet.
	f.clock.Set(testStart.Add(InBandLifetime + 15*time.Minute))
	n, err := f.mgr.ExpireStaleApproved(ctx, 15*time.Minute)
	require.NoError(t, err)
	require.Zero(t, n)

	f.clock.Set(testStart.Add(InBandLifetime + 15*time.Minute + time.Millisecond))
	n, err = f.mgr.ExpireStaleApproved(ctx, 15*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, payload, err := f.mgr.Get(ctx, f.tenant, inBand.ID)
	require.NoError(t, err)
	require.Equal(t, StateExpired, got.State)
	require.Nil(t, payload, "an expired action keeps no payload")

	got, _, err = f.mgr.Get(ctx, f.tenant, finished.ID)
	require.NoError(t, err)
	require.Equal(t, StateDone, got.State)
	require.Equal(t, "item-1", got.ResultID)
	got, _, err = f.mgr.Get(ctx, f.tenant, waiting.ID)
	require.NoError(t, err)
	require.Equal(t, StatePending, got.State, "a waiting action is not touched")

	_, err = f.mgr.ExpireStaleApproved(ctx, -time.Second)
	require.Error(t, err, "a negative grace time is refused")
}

// One action has one channel. An action of the approval mode is answered on the
// web page only: a request state cannot resume or claim it, even a genuine one
// for its id and hash. The action still waits for the web page.
func TestClaimRefusesAnApprovalAction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	payload := []byte("for the web page")
	p := f.createApproval(string(payload))
	state := f.state(p)

	_, err := f.mgr.Resume(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrStateInvalid)
	_, err = f.mgr.Claim(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrStateInvalid)
	require.Equal(t, "pending", f.storedState(p.ID), "the refused claim changed nothing")

	require.NoError(t, f.mgr.Transition(ctx, f.tenant, p.ID, StatePending, StateApproved, ""),
		"the web page can still approve it")

	inBand := f.create("in the chat")
	_, err = f.mgr.Claim(ctx, f.tenant, f.state(inBand), []byte("in the chat"))
	require.NoError(t, err, "an in-band action is claimed as before")
}

// errExpiryWrite is the error of a write of an expiry that failed. Its text
// must never reach the log.
var errExpiryWrite = errors.New("disk full at /private/secret/path")

// expiryFailStore is a store whose writes of an expiry fail. The real
// TransitionPending and ClaimPending join such a failure to store.ErrNotFound
// (see store.requireOneRowOrExpire). This store does the same.
type expiryFailStore struct {
	PendingStore
}

func (s expiryFailStore) ExpirePending(context.Context, account.TenantID, string, time.Time) error {
	return errExpiryWrite
}

func (s expiryFailStore) TransitionPending(ctx context.Context, tenant account.TenantID, id, from, to, resultID string, now time.Time) error {
	err := s.PendingStore.TransitionPending(ctx, tenant, id, from, to, resultID, now)
	if errors.Is(err, store.ErrNotFound) {
		return errors.Join(store.ErrNotFound, fmt.Errorf("store: transition pending action: %w", errExpiryWrite))
	}
	return err
}

// The answer to the caller stays ErrNotPending when the expiry of an action
// cannot be written. But the failed write is not silent: it is logged with a
// fixed code and the Go types of the error, never its text.
func TestAFailedWriteOfAnExpiryIsLogged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	logs := &bytes.Buffer{}
	m := NewManager(expiryFailStore{PendingStore: f.store}, f.vault, signing.New(f.key), f.clock.Now)
	m.Logger = slog.New(slog.NewTextHandler(logs, nil))
	payload := []byte("payload")
	// The action lives one minute and its state ten, so a claim after two
	// minutes finds an action that waits but has expired.
	p, err := m.createWithLifetime(ctx, f.tenant, f.acct, KindSend, ModeInBand, payload, testPreview(), time.Minute)
	require.NoError(t, err)
	state, err := m.RequestState(p)
	require.NoError(t, err)
	f.clock.Set(testStart.Add(2 * time.Minute))

	_, err = m.Claim(ctx, f.tenant, state, payload)
	require.ErrorIs(t, err, ErrNotPending)
	require.Equal(t, 1, strings.Count(logs.String(), "code=pending_expire_failed"), "the claim logs it")

	err = m.Transition(ctx, f.tenant, p.ID, StatePending, StateRejected, "")
	require.ErrorIs(t, err, ErrNotPending)
	require.Equal(t, 2, strings.Count(logs.String(), "code=pending_expire_failed"), "the transition logs it")

	require.Contains(t, logs.String(), "error_types=")
	require.NotContains(t, logs.String(), "disk full", "the text of the error is never logged")
	require.NotContains(t, logs.String(), "Zebra")
}
