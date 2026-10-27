package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the driver that the store uses, for addTenant

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// --- helpers ---

// vaultFixture is a store in a temporary directory, with the tenant "local"
// and a new file master key.
type vaultFixture struct {
	t   *testing.T
	dir string
	st  *store.Store
	kek KEK
}

func newVaultFixture(t *testing.T) *vaultFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	_, err = st.EnsureLocalTenant(context.Background())
	require.NoError(t, err)
	return &vaultFixture{t: t, dir: dir, st: st, kek: newTestFileKEK(t)}
}

// newTestFileKEK returns a file KEK with a new random key. Each call gives a
// different key and a different ID.
func newTestFileKEK(t *testing.T) KEK {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, GenerateKeyFile(path))
	kek, err := NewFileKEK(path)
	require.NoError(t, err)
	return kek
}

// addTenant creates a tenant other than "local". The store has no exported
// method for it yet, so the test writes the row itself. "email-mcp.db" is the
// database file name that the store uses.
func (f *vaultFixture) addTenant(id account.TenantID) {
	f.t.Helper()
	uriPath := filepath.ToSlash(filepath.Join(f.dir, "email-mcp.db"))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	db, err := sql.Open("sqlite", "file:"+uriPath+"?_pragma=busy_timeout(5000)")
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, db.Close()) }()
	_, err = db.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, 0)`, string(id))
	require.NoError(f.t, err)
}

// putTenantKey stores a new random key for a tenant, wrapped by kek, and
// returns the unwrapped key.
func (f *vaultFixture) putTenantKey(tenant account.TenantID, version int, kek KEK) []byte {
	f.t.Helper()
	dek := randomBytes(f.t, keySize)
	wrapped, err := kek.Wrap(context.Background(), dek)
	require.NoError(f.t, err)
	require.NoError(f.t, f.st.PutTenantKey(context.Background(), tenant, version, wrapped, kek.ID()))
	return dek
}

// storedDEK unwraps the stored key of a tenant with kek.
func (f *vaultFixture) storedDEK(tenant account.TenantID, version int, kek KEK) []byte {
	f.t.Helper()
	wrapped, _, err := f.st.TenantKey(context.Background(), tenant, version)
	require.NoError(f.t, err)
	dek, err := kek.Unwrap(context.Background(), wrapped)
	require.NoError(f.t, err)
	return dek
}

// keySnapshot is a copy of every stored wrapped key.
type keySnapshot struct {
	tenantKeys    []store.TenantKeyRow
	serverWrapped []byte
	serverKEKID   string
}

func snapshotKeys(t *testing.T, st *store.Store) keySnapshot {
	t.Helper()
	ctx := context.Background()
	rows, err := st.ListTenantKeys(ctx)
	require.NoError(t, err)
	snap := keySnapshot{tenantKeys: rows}
	snap.serverWrapped, snap.serverKEKID, err = st.ServerKey(ctx)
	if err != nil {
		require.ErrorIs(t, err, store.ErrNotFound)
	}
	return snap
}

// requireNoKeyMaterial checks that the text of err holds none of the given
// secrets, as raw text, hex or base64.
func requireNoKeyMaterial(t *testing.T, err error, secrets ...[]byte) {
	t.Helper()
	require.Error(t, err)
	text := err.Error()
	for _, s := range secrets {
		require.NotEmpty(t, s)
		for _, form := range []string{string(s), hex.EncodeToString(s), base64.StdEncoding.EncodeToString(s), fmt.Sprintf("%v", s)} {
			require.NotContains(t, text, form)
		}
	}
}

// barrier holds callers until n of them have arrived. It makes a race happen
// on purpose: every caller sees "no key yet" before any caller creates one.
type barrier struct {
	n       int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newBarrier(n int) *barrier {
	return &barrier{n: n, release: make(chan struct{})}
}

func (b *barrier) wait() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == b.n {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-time.After(10 * time.Second):
	}
}

// spyStore wraps a VaultStore. It counts calls and can change what the store
// does, to test races and failures.
type spyStore struct {
	VaultStore

	// barrier, if set, holds every call that finds no tenant key or no server
	// key, until all the expected callers have found the same.
	barrier *barrier
	// listHook, if set, changes the result of ListTenantKeys. It runs after the
	// real call, so it can change the database after the rows were read.
	listHook func([]store.TenantKeyRow) []store.TenantKeyRow
	// serverKeyHook, if set, runs after the real ServerKey call and gets its
	// error.
	serverKeyHook func(err error)
	// tenantKeyGate, if set, is received from before TenantKey returns, and
	// tenantKeyEntered is closed when the first call is waiting for it.
	tenantKeyGate    chan struct{}
	tenantKeyEntered chan struct{}
	enteredOnce      sync.Once

	mu             sync.Mutex
	tenantKeyReads int
	serverKeyReads int
	conflicts      int
}

func (s *spyStore) LatestTenantKeyVersion(ctx context.Context, tenant account.TenantID) (int, error) {
	version, err := s.VaultStore.LatestTenantKeyVersion(ctx, tenant)
	if err == nil && version == 0 && s.barrier != nil {
		s.barrier.wait()
	}
	return version, err
}

func (s *spyStore) TenantKey(ctx context.Context, tenant account.TenantID, version int) ([]byte, string, error) {
	s.mu.Lock()
	s.tenantKeyReads++
	s.mu.Unlock()
	if s.tenantKeyGate != nil {
		s.enteredOnce.Do(func() { close(s.tenantKeyEntered) })
		<-s.tenantKeyGate
	}
	return s.VaultStore.TenantKey(ctx, tenant, version)
}

func (s *spyStore) PutTenantKey(ctx context.Context, tenant account.TenantID, version int, wrapped []byte, kekID string) error {
	err := s.VaultStore.PutTenantKey(ctx, tenant, version, wrapped, kekID)
	s.countConflict(err)
	return err
}

func (s *spyStore) ServerKey(ctx context.Context) ([]byte, string, error) {
	s.mu.Lock()
	s.serverKeyReads++
	s.mu.Unlock()
	wrapped, kekID, err := s.VaultStore.ServerKey(ctx)
	if errors.Is(err, store.ErrNotFound) && s.barrier != nil {
		s.barrier.wait()
	}
	if s.serverKeyHook != nil {
		s.serverKeyHook(err)
	}
	return wrapped, kekID, err
}

func (s *spyStore) PutServerKey(ctx context.Context, wrapped []byte, kekID string) error {
	err := s.VaultStore.PutServerKey(ctx, wrapped, kekID)
	s.countConflict(err)
	return err
}

func (s *spyStore) ListTenantKeys(ctx context.Context) ([]store.TenantKeyRow, error) {
	rows, err := s.VaultStore.ListTenantKeys(ctx)
	if err == nil && s.listHook != nil {
		rows = s.listHook(rows)
	}
	return rows, err
}

func (s *spyStore) countConflict(err error) {
	if errors.Is(err, store.ErrExists) {
		s.mu.Lock()
		s.conflicts++
		s.mu.Unlock()
	}
}

func (s *spyStore) counts() (tenantKeyReads, serverKeyReads, conflicts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tenantKeyReads, s.serverKeyReads, s.conflicts
}

// faultyKEK is a KEK that can fail on purpose. It uses a real KEK underneath.
type faultyKEK struct {
	KEK
	failWrapAt   int    // fail the Nth call of Wrap (1 is the first); 0 means never
	unwrapErr    error  // if set, Unwrap returns it
	unwrapResult []byte // if set, Unwrap returns this instead of the real key

	mu        sync.Mutex
	wrapCalls int
}

func (k *faultyKEK) Wrap(ctx context.Context, dek []byte) ([]byte, error) {
	k.mu.Lock()
	k.wrapCalls++
	fail := k.failWrapAt != 0 && k.wrapCalls == k.failWrapAt
	k.mu.Unlock()
	if fail {
		return nil, errors.New("wrap failed on purpose")
	}
	return k.KEK.Wrap(ctx, dek)
}

func (k *faultyKEK) Unwrap(ctx context.Context, wrapped []byte) ([]byte, error) {
	if k.unwrapErr != nil {
		return nil, k.unwrapErr
	}
	if k.unwrapResult != nil {
		return bytes.Clone(k.unwrapResult), nil
	}
	return k.KEK.Unwrap(ctx, wrapped)
}

// --- Encrypt and Decrypt ---

func TestVaultEncryptDecrypt(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")

	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)
	require.Equal(t, 1, version, "the first key of a tenant is version 1")
	require.NotContains(t, string(sealed), "hunter2")

	got, err := v.Decrypt(ctx, "local", version, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)

	// The stored key is wrapped by the master key, and the master key's ID is
	// stored with it. Unwrapped, it is a 32-byte key that opens the value.
	wrapped, kekID, err := f.st.TenantKey(ctx, "local", 1)
	require.NoError(t, err)
	require.Equal(t, f.kek.ID(), kekID)
	dek, err := f.kek.Unwrap(ctx, wrapped)
	require.NoError(t, err)
	require.Len(t, dek, 32)
	direct, err := Open(dek, sealed, aad)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), direct)

	// A second value uses the same key but a new nonce.
	version2, sealed2, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)
	require.Equal(t, 1, version2)
	require.NotEqual(t, sealed, sealed2)
	rows, err := f.st.ListTenantKeys(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the tenant key is created once")

	// A new Vault on the same store and master key reads the same key.
	got, err = NewVault(f.st, f.kek).Decrypt(ctx, "local", version, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
}

func TestVaultDecryptRejectsWrongInput(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")
	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)

	// Wrong associated data.
	_, err = v.Decrypt(ctx, "local", version, AAD("local", "acct-2", "password"), sealed)
	require.ErrorIs(t, err, ErrDecrypt)

	// A changed byte.
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01
	_, err = v.Decrypt(ctx, "local", version, aad, tampered)
	require.ErrorIs(t, err, ErrDecrypt)

	// Too short to hold a nonce and a tag.
	_, err = v.Decrypt(ctx, "local", version, aad, []byte("short"))
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestVaultTenantsUseDifferentKeys(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	f.addTenant("other")
	v := NewVault(f.st, f.kek)

	// The same associated data for both tenants: only the key can tell them
	// apart.
	aad := AAD("local", "acct-1", "password")
	versionA, sealedA, err := v.Encrypt(ctx, "local", aad, []byte("secret of A"))
	require.NoError(t, err)
	versionB, sealedB, err := v.Encrypt(ctx, "other", aad, []byte("secret of B"))
	require.NoError(t, err)
	require.Equal(t, 1, versionA)
	require.Equal(t, 1, versionB)

	_, err = v.Decrypt(ctx, "other", versionA, aad, sealedA)
	require.ErrorIs(t, err, ErrDecrypt, "data of tenant A must not open as tenant B")
	_, err = v.Decrypt(ctx, "local", versionB, aad, sealedB)
	require.ErrorIs(t, err, ErrDecrypt, "data of tenant B must not open as tenant A")

	gotA, err := v.Decrypt(ctx, "local", versionA, aad, sealedA)
	require.NoError(t, err)
	require.Equal(t, []byte("secret of A"), gotA)
	gotB, err := v.Decrypt(ctx, "other", versionB, aad, sealedB)
	require.NoError(t, err)
	require.Equal(t, []byte("secret of B"), gotB)

	require.NotEqual(t, f.storedDEK("local", 1, f.kek), f.storedDEK("other", 1, f.kek))
}

func TestVaultEncryptUsesLatestVersion(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")

	version1, sealed1, err := v.Encrypt(ctx, "local", aad, []byte("old value"))
	require.NoError(t, err)
	require.Equal(t, 1, version1)

	// Another program adds version 2 while this Vault is running.
	f.putTenantKey("local", 2, f.kek)

	version2, sealed2, err := v.Encrypt(ctx, "local", aad, []byte("new value"))
	require.NoError(t, err)
	require.Equal(t, 2, version2, "Encrypt uses the latest version")

	// Decrypt uses the version it is given.
	got, err := v.Decrypt(ctx, "local", version1, aad, sealed1)
	require.NoError(t, err)
	require.Equal(t, []byte("old value"), got)
	got, err = v.Decrypt(ctx, "local", version2, aad, sealed2)
	require.NoError(t, err)
	require.Equal(t, []byte("new value"), got)

	// The wrong version is a different key.
	_, err = v.Decrypt(ctx, "local", 1, aad, sealed2)
	require.ErrorIs(t, err, ErrDecrypt)
	_, err = v.Decrypt(ctx, "local", 2, aad, sealed1)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestVaultDecryptDoesNotCreateKeys(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)

	_, err := v.Decrypt(ctx, "local", 1, AAD("local", "a", "f"), bytes.Repeat([]byte{1}, 64))
	require.ErrorIs(t, err, store.ErrNotFound)

	latest, err := f.st.LatestTenantKeyVersion(ctx, "local")
	require.NoError(t, err)
	require.Zero(t, latest, "Decrypt must not create a key")

	// A version that does not exist for a tenant that has other versions.
	_, _, err = v.Encrypt(ctx, "local", AAD("local", "a", "f"), []byte("x"))
	require.NoError(t, err)
	_, err = v.Decrypt(ctx, "local", 7, AAD("local", "a", "f"), bytes.Repeat([]byte{1}, 64))
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestVaultEncryptFailsForUnknownTenant(t *testing.T) {
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)

	// The store refuses a key for a tenant that does not exist.
	_, _, err := v.Encrypt(context.Background(), "ghost", AAD("ghost", "a", "f"), []byte("x"))
	require.Error(t, err)
	rows, err := f.st.ListTenantKeys(context.Background())
	require.NoError(t, err)
	require.Empty(t, rows)
}

// --- server key ---

func TestVaultServerKeyStable(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)

	first, err := v.ServerKey(ctx)
	require.NoError(t, err)
	require.Len(t, first, 32)
	second, err := v.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, first, second)

	// A new Vault on the same store returns the same key.
	again, err := NewVault(f.st, f.kek).ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, first, again)

	// The stored copy is wrapped, and names the master key.
	wrapped, kekID, err := f.st.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, f.kek.ID(), kekID)
	require.NotEqual(t, first, wrapped)
	unwrapped, err := f.kek.Unwrap(ctx, wrapped)
	require.NoError(t, err)
	require.Equal(t, first, unwrapped)
}

func TestVaultServerKeyIsACopy(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)

	first, err := v.ServerKey(ctx)
	require.NoError(t, err)
	want := bytes.Clone(first)

	// A caller that changes the bytes it got must not change the key that the
	// Vault keeps.
	clear(first)
	second, err := v.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, want, second)
	clear(second)
	third, err := v.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, want, third)
}

// --- cache ---

func TestVaultCachesKeys(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	spy := &spyStore{VaultStore: f.st}
	v := NewVault(spy, f.kek)
	aad := AAD("local", "acct-1", "password")

	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("x"))
	require.NoError(t, err)
	for range 5 {
		_, err := v.Decrypt(ctx, "local", version, aad, sealed)
		require.NoError(t, err)
	}
	for range 5 {
		_, _, err := v.Encrypt(ctx, "local", aad, []byte("x"))
		require.NoError(t, err)
	}
	tenantReads, _, _ := spy.counts()
	require.Zero(t, tenantReads, "a key made by this Vault is cached, so it is never read back")

	// A Vault that did not make the key reads it once.
	fresh := NewVault(spy, f.kek)
	for range 5 {
		_, err := fresh.Decrypt(ctx, "local", version, aad, sealed)
		require.NoError(t, err)
	}
	tenantReads, _, _ = spy.counts()
	require.Equal(t, 1, tenantReads)

	// The server key is read once, too.
	for range 5 {
		_, err := v.ServerKey(ctx)
		require.NoError(t, err)
	}
	_, serverReads, _ := spy.counts()
	require.Equal(t, 1, serverReads, "only the first call looks in the store")
}

// --- wrong master key and bad stored keys ---

func TestVaultWrongMasterKey(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")
	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)
	serverKey, err := v.ServerKey(ctx)
	require.NoError(t, err)
	before := snapshotKeys(t, f.st)
	dek := f.storedDEK("local", 1, f.kek)

	wrong := NewVault(f.st, newTestFileKEK(t))

	_, err = wrong.Decrypt(ctx, "local", version, aad, sealed)
	require.ErrorIs(t, err, ErrDecrypt)
	require.ErrorContains(t, err, "wrong master key or corrupted data")
	requireNoKeyMaterial(t, err, dek, before.tenantKeys[0].Wrapped)

	_, _, err = wrong.Encrypt(ctx, "local", aad, []byte("x"))
	require.ErrorIs(t, err, ErrDecrypt)
	require.ErrorContains(t, err, "wrong master key or corrupted data")

	_, err = wrong.ServerKey(ctx)
	require.ErrorIs(t, err, ErrDecrypt)
	require.ErrorContains(t, err, "wrong master key or corrupted data")
	requireNoKeyMaterial(t, err, serverKey, before.serverWrapped)

	require.Equal(t, before, snapshotKeys(t, f.st), "a wrong master key must change nothing")
}

func TestVaultUnwrapsWithConfiguredKEKWhateverTheStoredID(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)

	// The stored ID is bookkeeping. It does not say which key to use.
	dek := randomBytes(t, keySize)
	wrapped, err := f.kek.Wrap(ctx, dek)
	require.NoError(t, err)
	require.NoError(t, f.st.PutTenantKey(ctx, "local", 1, wrapped, "an-id-that-matches-no-key"))
	aad := AAD("local", "acct-1", "password")
	sealed, err := Seal(dek, []byte("hunter2"), aad)
	require.NoError(t, err)

	got, err := NewVault(f.st, f.kek).Decrypt(ctx, "local", 1, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
}

func TestVaultCorruptedStoredKeys(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	aad := AAD("local", "acct-1", "password")

	t.Run("changed wrapped bytes", func(t *testing.T) {
		v := NewVault(f.st, f.kek)
		_, sealed, err := v.Encrypt(ctx, "local", aad, []byte("x"))
		require.NoError(t, err)
		wrapped, kekID, err := f.st.TenantKey(ctx, "local", 1)
		require.NoError(t, err)
		bad := bytes.Clone(wrapped)
		bad[len(bad)-1] ^= 0x01
		require.NoError(t, f.st.RewrapTenantKey(ctx, "local", 1, bad, kekID))

		_, err = NewVault(f.st, f.kek).Decrypt(ctx, "local", 1, aad, sealed)
		require.ErrorIs(t, err, ErrDecrypt)
		require.ErrorContains(t, err, "wrong master key or corrupted data")
		requireNoKeyMaterial(t, err, bad, wrapped)
	})

	t.Run("key of the wrong length", func(t *testing.T) {
		short := randomBytes(t, 16)
		wrapped, err := f.kek.Wrap(ctx, short)
		require.NoError(t, err)
		require.NoError(t, f.st.PutTenantKey(ctx, "local", 2, wrapped, f.kek.ID()))

		_, err = NewVault(f.st, f.kek).Decrypt(ctx, "local", 2, aad, bytes.Repeat([]byte{1}, 64))
		require.ErrorContains(t, err, "corrupted")
		requireNoKeyMaterial(t, err, short)
	})

	t.Run("server key of the wrong length", func(t *testing.T) {
		short := randomBytes(t, 16)
		wrapped, err := f.kek.Wrap(ctx, short)
		require.NoError(t, err)
		require.NoError(t, f.st.PutServerKey(ctx, wrapped, f.kek.ID()))

		_, err = NewVault(f.st, f.kek).ServerKey(ctx)
		require.ErrorContains(t, err, "corrupted")
		requireNoKeyMaterial(t, err, short)
	})
}

func TestVaultReportsKEKErrorsAsTheyAre(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	aad := AAD("local", "acct-1", "password")
	_, sealed, err := NewVault(f.st, f.kek).Encrypt(ctx, "local", aad, []byte("x"))
	require.NoError(t, err)

	// A remote master key can fail for a reason that is not "wrong key", for
	// example no network. The error must not say the key is wrong.
	down := errors.New("key service is unreachable")
	v := NewVault(f.st, &faultyKEK{KEK: f.kek, unwrapErr: down})
	_, err = v.Decrypt(ctx, "local", 1, aad, sealed)
	require.ErrorIs(t, err, down)
	require.NotErrorIs(t, err, ErrDecrypt)
	require.NotContains(t, err.Error(), "wrong master key")
}

// --- printing ---

func TestVaultNeverPrintsKeys(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	_, _, err := v.Encrypt(ctx, "local", AAD("local", "a", "f"), []byte("x"))
	require.NoError(t, err)
	serverKey, err := v.ServerKey(ctx)
	require.NoError(t, err)
	dek := f.storedDEK("local", 1, f.kek)

	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	logger.Info("vault", "vault", v)
	logger.Info("vault", slog.Any("vault", v))

	outputs := []string{
		fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v),
		fmt.Sprintf("%s", v), fmt.Sprintf("%d", v), fmt.Sprintf("%x", v),
		logged.String(),
	}
	for _, out := range outputs {
		for _, key := range [][]byte{dek, serverKey} {
			require.NotContains(t, out, hex.EncodeToString(key))
			require.NotContains(t, out, base64.StdEncoding.EncodeToString(key))
			require.NotContains(t, out, fmt.Sprintf("%v", key))
			require.NotContains(t, out, fmt.Sprintf("%v", key[:8]))
		}
		require.Contains(t, out, "redacted")
	}
}

// --- concurrent first use ---

// TestVaultConcurrentFirstUse makes many callers across two Vaults find no key
// at the same moment. Exactly one creates it. The others must discard their
// own key and use the stored one. Every value must open with a third Vault.
func TestVaultConcurrentFirstUse(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	const workers = 8
	spy := &spyStore{VaultStore: f.st, barrier: newBarrier(workers)}
	vaults := []*Vault{NewVault(spy, f.kek), NewVault(spy, f.kek)}
	aad := AAD("local", "acct-1", "password")
	secret := func(i int) []byte { return fmt.Appendf(nil, "secret-%d", i) }

	type result struct {
		version int
		sealed  []byte
		err     error
	}
	results := make([]result, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &results[i]
			r.version, r.sealed, r.err = vaults[i%2].Encrypt(ctx, "local", aad, secret(i))
		}()
	}
	wg.Wait()

	_, _, conflicts := spy.counts()
	require.Equal(t, workers-1, conflicts, "all callers but one must lose the race")

	fresh := NewVault(f.st, f.kek)
	for i, r := range results {
		require.NoError(t, r.err)
		require.Equal(t, 1, r.version)
		got, err := fresh.Decrypt(ctx, "local", r.version, aad, r.sealed)
		require.NoError(t, err, "value %d must open with the stored key", i)
		require.Equal(t, secret(i), got)
	}
	rows, err := f.st.ListTenantKeys(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestVaultConcurrentServerKeyFirstUse(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	const workers = 8
	spy := &spyStore{VaultStore: f.st, barrier: newBarrier(workers)}
	vaults := []*Vault{NewVault(spy, f.kek), NewVault(spy, f.kek)}

	keys := make([][]byte, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], errs[i] = vaults[i%2].ServerKey(ctx)
		}()
	}
	wg.Wait()

	_, _, conflicts := spy.counts()
	require.Equal(t, workers-1, conflicts, "all callers but one must lose the race")

	stored, err := NewVault(f.st, f.kek).ServerKey(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 32)
	for i := range workers {
		require.NoError(t, errs[i])
		require.Equal(t, stored, keys[i], "caller %d got a key that was not stored", i)
	}
}

// TestVaultConcurrentUse runs the same race on the real store without a
// barrier, as two programs would meet it.
func TestVaultConcurrentUse(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	const workers = 8
	vaults := []*Vault{NewVault(f.st, f.kek), NewVault(f.st, f.kek)}
	aad := AAD("local", "acct-1", "password")

	sealed := make([][]byte, workers)
	serverKeys := make([][]byte, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := vaults[i%2]
			var version int
			version, sealed[i], errs[i] = v.Encrypt(ctx, "local", aad, fmt.Appendf(nil, "secret-%d", i))
			if errs[i] == nil && version != 1 {
				errs[i] = fmt.Errorf("unexpected key version %d", version)
			}
			if errs[i] == nil {
				serverKeys[i], errs[i] = v.ServerKey(ctx)
			}
		}()
	}
	wg.Wait()

	fresh := NewVault(f.st, f.kek)
	stored, err := fresh.ServerKey(ctx)
	require.NoError(t, err)
	for i := range workers {
		require.NoError(t, errs[i])
		got, err := fresh.Decrypt(ctx, "local", 1, aad, sealed[i])
		require.NoError(t, err)
		require.Equal(t, fmt.Appendf(nil, "secret-%d", i), got)
		require.Equal(t, stored, serverKeys[i])
	}
}

// --- RewrapAll ---

func TestRewrapAll(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")
	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)
	serverKey, err := v.ServerKey(ctx)
	require.NoError(t, err)
	before := snapshotKeys(t, f.st)
	newKEK := newTestFileKEK(t)
	require.NotEqual(t, f.kek.ID(), newKEK.ID())

	require.NoError(t, v.RewrapAll(ctx, newKEK))

	// The stored keys are wrapped again, and name the new master key.
	after := snapshotKeys(t, f.st)
	require.Len(t, after.tenantKeys, 1)
	require.Equal(t, newKEK.ID(), after.tenantKeys[0].KEKID)
	require.NotEqual(t, before.tenantKeys[0].Wrapped, after.tenantKeys[0].Wrapped)
	require.Equal(t, newKEK.ID(), after.serverKEKID)
	require.NotEqual(t, before.serverWrapped, after.serverWrapped)

	// A new Vault that knows only the new master key opens the old value. The
	// value itself was not changed.
	fresh := NewVault(f.st, newKEK)
	got, err := fresh.Decrypt(ctx, "local", version, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
	gotServerKey, err := fresh.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, serverKey, gotServerKey)

	// The old master key no longer opens the keys.
	_, err = NewVault(f.st, f.kek).Decrypt(ctx, "local", version, aad, sealed)
	require.ErrorIs(t, err, ErrDecrypt)
	_, err = NewVault(f.st, f.kek).ServerKey(ctx)
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestRewrapAllCoversEveryKey(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	f.addTenant("other")
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")

	// Two tenants, and two versions for one of them.
	localV1, sealedLocalV1, err := v.Encrypt(ctx, "local", aad, []byte("local v1"))
	require.NoError(t, err)
	f.putTenantKey("local", 2, f.kek)
	localV2, sealedLocalV2, err := v.Encrypt(ctx, "local", aad, []byte("local v2"))
	require.NoError(t, err)
	otherV1, sealedOther, err := v.Encrypt(ctx, "other", aad, []byte("other v1"))
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 1}, []int{localV1, localV2, otherV1})
	before := snapshotKeys(t, f.st)
	require.Len(t, before.tenantKeys, 3)

	newKEK := newTestFileKEK(t)
	require.NoError(t, v.RewrapAll(ctx, newKEK))

	after := snapshotKeys(t, f.st)
	require.Len(t, after.tenantKeys, 3)
	for i, row := range after.tenantKeys {
		require.Equal(t, before.tenantKeys[i].Tenant, row.Tenant)
		require.Equal(t, before.tenantKeys[i].Version, row.Version)
		require.Equal(t, newKEK.ID(), row.KEKID)
		require.NotEqual(t, before.tenantKeys[i].Wrapped, row.Wrapped)
	}

	fresh := NewVault(f.st, newKEK)
	for _, tc := range []struct {
		tenant  account.TenantID
		version int
		sealed  []byte
		want    string
	}{
		{"local", 1, sealedLocalV1, "local v1"},
		{"local", 2, sealedLocalV2, "local v2"},
		{"other", 1, sealedOther, "other v1"},
	} {
		got, err := fresh.Decrypt(ctx, tc.tenant, tc.version, aad, tc.sealed)
		require.NoError(t, err, "%s version %d", tc.tenant, tc.version)
		require.Equal(t, []byte(tc.want), got)
	}
}

func TestRewrapAllKeepsTheVaultWorking(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	f.addTenant("other")
	spy := &spyStore{VaultStore: f.st}
	v := NewVault(spy, f.kek)
	aad := AAD("local", "acct-1", "password")
	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)
	_, err = v.ServerKey(ctx)
	require.NoError(t, err)

	newKEK := newTestFileKEK(t)
	require.NoError(t, v.RewrapAll(ctx, newKEK))

	// The cache is empty, so the next use reads the store and unwraps with the
	// new master key.
	readsBefore, _, _ := spy.counts()
	got, err := v.Decrypt(ctx, "local", version, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
	readsAfter, _, _ := spy.counts()
	require.Equal(t, readsBefore+1, readsAfter, "the DEK cache must be cleared")
	_, err = v.ServerKey(ctx)
	require.NoError(t, err)

	// A key that the Vault creates after the change uses the new master key.
	otherVersion, otherSealed, err := v.Encrypt(ctx, "other", aad, []byte("other secret"))
	require.NoError(t, err)
	_, kekID, err := f.st.TenantKey(ctx, "other", otherVersion)
	require.NoError(t, err)
	require.Equal(t, newKEK.ID(), kekID)
	got, err = NewVault(f.st, newKEK).Decrypt(ctx, "other", otherVersion, aad, otherSealed)
	require.NoError(t, err)
	require.Equal(t, []byte("other secret"), got)
}

func TestRewrapAllWithNothingStored(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	newKEK := newTestFileKEK(t)

	require.NoError(t, v.RewrapAll(ctx, newKEK))

	// Keys that are created later use the new master key, both kinds.
	aad := AAD("local", "acct-1", "password")
	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("x"))
	require.NoError(t, err)
	serverKey, err := v.ServerKey(ctx)
	require.NoError(t, err)

	fresh := NewVault(f.st, newKEK)
	got, err := fresh.Decrypt(ctx, "local", version, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("x"), got)
	gotServerKey, err := fresh.ServerKey(ctx)
	require.NoError(t, err)
	require.Equal(t, serverKey, gotServerKey)
	snap := snapshotKeys(t, f.st)
	require.Equal(t, newKEK.ID(), snap.tenantKeys[0].KEKID)
	require.Equal(t, newKEK.ID(), snap.serverKEKID)
}

func TestRewrapAllWithoutServerKey(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	aad := AAD("local", "acct-1", "password")
	version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)

	newKEK := newTestFileKEK(t)
	require.NoError(t, v.RewrapAll(ctx, newKEK))

	// RewrapAll did not create a server key.
	_, _, err = f.st.ServerKey(ctx)
	require.ErrorIs(t, err, store.ErrNotFound)

	fresh := NewVault(f.st, newKEK)
	got, err := fresh.Decrypt(ctx, "local", version, aad, sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
}

func TestRewrapAllRejectsNilKEK(t *testing.T) {
	ctx := context.Background()
	f := newVaultFixture(t)
	v := NewVault(f.st, f.kek)
	_, _, err := v.Encrypt(ctx, "local", AAD("local", "a", "f"), []byte("x"))
	require.NoError(t, err)
	before := snapshotKeys(t, f.st)

	require.Error(t, v.RewrapAll(ctx, nil))
	require.Equal(t, before, snapshotKeys(t, f.st))
}

// rewrapFixture has two tenants, with a key each, and a server key. All are
// wrapped by f.kek.
type rewrapFixture struct {
	*vaultFixture
	spy    *spyStore
	vault  *Vault
	aad    string
	sealed []byte
	before keySnapshot
}

func newRewrapFixture(t *testing.T) *rewrapFixture {
	t.Helper()
	ctx := context.Background()
	f := newVaultFixture(t)
	f.addTenant("other")
	spy := &spyStore{VaultStore: f.st}
	v := NewVault(spy, f.kek)
	aad := AAD("local", "acct-1", "password")
	_, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
	require.NoError(t, err)
	f.putTenantKey("local", 2, f.kek)
	_, _, err = v.Encrypt(ctx, "other", aad, []byte("other secret"))
	require.NoError(t, err)
	_, err = v.ServerKey(ctx)
	require.NoError(t, err)
	return &rewrapFixture{vaultFixture: f, spy: spy, vault: v, aad: aad, sealed: sealed, before: snapshotKeys(t, f.st)}
}

// requireUnchangedAndWorking checks that a failed RewrapAll left the stored
// keys as they were in the fixture, and that the Vault still works with its old
// master key.
func (r *rewrapFixture) requireUnchangedAndWorking(t *testing.T, newKEK KEK) {
	t.Helper()
	r.requireUnchangedSince(t, newKEK, r.before)
}

// requireUnchangedSince is the same check against a snapshot that the test took
// itself, for example after it added a key.
func (r *rewrapFixture) requireUnchangedSince(t *testing.T, newKEK KEK, want keySnapshot) {
	t.Helper()
	ctx := context.Background()
	require.Equal(t, want, snapshotKeys(t, r.st), "a failed RewrapAll must change nothing")

	got, err := r.vault.Decrypt(ctx, "local", 1, r.aad, r.sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
	got, err = NewVault(r.st, r.kek).Decrypt(ctx, "local", 1, r.aad, r.sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
	_, err = NewVault(r.st, newKEK).Decrypt(ctx, "local", 1, r.aad, r.sealed)
	require.ErrorIs(t, err, ErrDecrypt, "no key may be wrapped by the new master key")
}

// TestRewrapAllFailsPartWayChangesNothing makes one stored row disappear
// between the listing and the replace, with rows both before and after it. The
// store must undo everything, and the Vault must stay on its old master key.
func TestRewrapAllFailsPartWayChangesNothing(t *testing.T) {
	ctx := context.Background()
	r := newRewrapFixture(t)
	require.Len(t, r.before.tenantKeys, 3)

	// The row exists in the listing (and is wrapped by the old master key), but
	// not in the database, like a row that another program deleted.
	phantomWrapped, err := r.kek.Wrap(ctx, randomBytes(t, keySize))
	require.NoError(t, err)
	r.spy.listHook = func(rows []store.TenantKeyRow) []store.TenantKeyRow {
		phantom := store.TenantKeyRow{Tenant: "local", Version: 99, Wrapped: phantomWrapped, KEKID: r.kek.ID()}
		return []store.TenantKeyRow{rows[0], phantom, rows[1], rows[2]}
	}

	newKEK := newTestFileKEK(t)
	err = r.vault.RewrapAll(ctx, newKEK)

	require.ErrorIs(t, err, store.ErrNotFound)
	r.requireUnchangedAndWorking(t, newKEK)
	// The Vault is still on the old master key: a key it creates now uses it.
	_, _, err = r.vault.Encrypt(ctx, "other", r.aad, []byte("x"))
	require.NoError(t, err)
}

func TestRewrapAllNewKEKFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		kek  func(real KEK) *faultyKEK
	}{
		{"wrap fails for the second key", func(real KEK) *faultyKEK { return &faultyKEK{KEK: real, failWrapAt: 2} }},
		{"wrap fails for the server key", func(real KEK) *faultyKEK { return &faultyKEK{KEK: real, failWrapAt: 4} }},
		{"new key cannot unwrap what it wrapped", func(real KEK) *faultyKEK { return &faultyKEK{KEK: real, unwrapErr: ErrDecrypt} }},
		{"new key unwraps to a different key", func(real KEK) *faultyKEK {
			return &faultyKEK{KEK: real, unwrapResult: bytes.Repeat([]byte{7}, keySize)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRewrapFixture(t)
			dek := r.storedDEK("local", 1, r.kek)
			realNew := newTestFileKEK(t)

			err := r.vault.RewrapAll(ctx, tc.kek(realNew))

			require.Error(t, err)
			requireNoKeyMaterial(t, err, dek)
			r.requireUnchangedAndWorking(t, realNew)
		})
	}
}

// TestRewrapAllRefusesWhenTheKeySetChanges covers the race that would lose
// data. Another program (or a second Vault) adds a key after RewrapAll has
// listed the keys. That key is wrapped by the old master key. If RewrapAll
// reported success, the operator would destroy the old master key, and the new
// key could never be opened. RewrapAll must refuse and change nothing.
func TestRewrapAllRefusesWhenTheKeySetChanges(t *testing.T) {
	ctx := context.Background()

	t.Run("a tenant key is added after the listing", func(t *testing.T) {
		r := newRewrapFixture(t)
		newKEK := newTestFileKEK(t)
		var addedDEK []byte
		var afterInsert keySnapshot
		r.spy.listHook = func(listed []store.TenantKeyRow) []store.TenantKeyRow {
			// RewrapAll has the rows. Now another program adds a key.
			addedDEK = r.putTenantKey("other", 2, r.kek)
			afterInsert = snapshotKeys(t, r.st)
			return listed
		}

		err := r.vault.RewrapAll(ctx, newKEK)

		require.ErrorIs(t, err, store.ErrConflict)
		require.ErrorContains(t, err, "run the rotation again")
		// The error is the one from the store, not changed by the Vault.
		require.True(t, strings.HasPrefix(err.Error(), "store: replace wrapped keys:"), err.Error())
		require.Len(t, afterInsert.tenantKeys, len(r.before.tenantKeys)+1)
		readsBefore, _, _ := r.spy.counts()
		r.requireUnchangedSince(t, newKEK, afterInsert)
		readsAfter, _, _ := r.spy.counts()
		require.Equal(t, readsBefore, readsAfter, "the Vault must keep its cache")

		// The Vault is still on the old master key: a key it creates uses it.
		f := r.vaultFixture
		f.addTenant("third")
		_, _, err = r.vault.Encrypt(ctx, "third", r.aad, []byte("x"))
		require.NoError(t, err)
		_, kekID, err := f.st.TenantKey(ctx, "third", 1)
		require.NoError(t, err)
		require.Equal(t, r.kek.ID(), kekID)

		// The operator runs the rotation again. The key set is stable now, and
		// the key that was added is wrapped again like the others.
		r.spy.listHook = nil
		require.NoError(t, r.vault.RewrapAll(ctx, newKEK))
		sealed, err := Seal(addedDEK, []byte("added later"), r.aad)
		require.NoError(t, err)
		fresh := NewVault(r.st, newKEK)
		got, err := fresh.Decrypt(ctx, "other", 2, r.aad, sealed)
		require.NoError(t, err)
		require.Equal(t, []byte("added later"), got)
		for _, row := range snapshotKeys(t, r.st).tenantKeys {
			require.Equal(t, newKEK.ID(), row.KEKID)
		}
	})

	t.Run("a server key is created after RewrapAll found none", func(t *testing.T) {
		f := newVaultFixture(t)
		spy := &spyStore{VaultStore: f.st}
		v := NewVault(spy, f.kek)
		aad := AAD("local", "acct-1", "password")
		version, sealed, err := v.Encrypt(ctx, "local", aad, []byte("hunter2"))
		require.NoError(t, err)
		_, _, err = f.st.ServerKey(ctx)
		require.ErrorIs(t, err, store.ErrNotFound, "this test needs a store without a server key")

		var addedKey []byte
		var afterInsert keySnapshot
		spy.serverKeyHook = func(err error) {
			if !errors.Is(err, store.ErrNotFound) {
				return
			}
			// RewrapAll saw no server key. Now another program creates one.
			addedKey = randomBytes(t, keySize)
			wrapped, wrapErr := f.kek.Wrap(ctx, addedKey)
			require.NoError(t, wrapErr)
			require.NoError(t, f.st.PutServerKey(ctx, wrapped, f.kek.ID()))
			afterInsert = snapshotKeys(t, f.st)
		}
		newKEK := newTestFileKEK(t)

		err = v.RewrapAll(ctx, newKEK)

		require.ErrorIs(t, err, store.ErrConflict)
		require.Equal(t, afterInsert, snapshotKeys(t, f.st), "a refused rotation must change nothing")
		// Still on the old master key: it opens the key that was added.
		spy.serverKeyHook = nil
		serverKey, err := v.ServerKey(ctx)
		require.NoError(t, err)
		require.Equal(t, addedKey, serverKey)
		got, err := v.Decrypt(ctx, "local", version, aad, sealed)
		require.NoError(t, err)
		require.Equal(t, []byte("hunter2"), got)
		_, err = NewVault(f.st, newKEK).Decrypt(ctx, "local", version, aad, sealed)
		require.ErrorIs(t, err, ErrDecrypt, "no key may be wrapped by the new master key")

		// Run again: now the server key is rotated too.
		require.NoError(t, v.RewrapAll(ctx, newKEK))
		gotServerKey, err := NewVault(f.st, newKEK).ServerKey(ctx)
		require.NoError(t, err)
		require.Equal(t, addedKey, gotServerKey)
	})
}

func TestRewrapAllWithWrongOldKEK(t *testing.T) {
	ctx := context.Background()
	r := newRewrapFixture(t)
	dek := r.storedDEK("local", 1, r.kek)
	realNew := newTestFileKEK(t)

	// A Vault that has the wrong current master key cannot unwrap, so it must
	// not change anything.
	wrong := NewVault(r.st, newTestFileKEK(t))
	err := wrong.RewrapAll(ctx, realNew)

	require.ErrorIs(t, err, ErrDecrypt)
	require.ErrorContains(t, err, "wrong master key or corrupted data")
	requireNoKeyMaterial(t, err, dek)
	r.requireUnchangedAndWorking(t, realNew)
}

// TestRewrapAllWaitsForRunningCalls checks that RewrapAll does not change the
// master key while another call is still reading and unwrapping a key. Without
// that, the call could read a row wrapped by the new key and try to unwrap it
// with the old one.
func TestRewrapAllWaitsForRunningCalls(t *testing.T) {
	ctx := context.Background()
	r := newRewrapFixture(t)
	newKEK := newTestFileKEK(t)

	// The first TenantKey call stops inside the store until the test lets it go.
	gate := make(chan struct{})
	spy := &spyStore{VaultStore: r.st, tenantKeyGate: gate, tenantKeyEntered: make(chan struct{})}
	reader := NewVault(spy, r.kek) // a Vault with an empty cache

	decryptErr := make(chan error, 1)
	go func() {
		_, err := reader.Decrypt(ctx, "local", 1, r.aad, r.sealed)
		decryptErr <- err
	}()
	<-spy.tenantKeyEntered

	rewrapErr := make(chan error, 1)
	go func() { rewrapErr <- reader.RewrapAll(ctx, newKEK) }()

	select {
	case err := <-rewrapErr:
		t.Fatalf("RewrapAll finished while a call was still running (error: %v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(gate)
	require.NoError(t, <-decryptErr)
	require.NoError(t, <-rewrapErr)

	got, err := NewVault(r.st, newKEK).Decrypt(ctx, "local", 1, r.aad, r.sealed)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got)
}
