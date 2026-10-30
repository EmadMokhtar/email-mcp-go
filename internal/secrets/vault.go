package secrets

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// VaultStore is the part of the store that the Vault needs. *store.Store
// implements it.
//
// The create methods (PutTenantKey and PutServerKey) only insert, and return
// store.ErrExists when the key is there already. The Vault depends on this when
// two programs meet at the first use of a key. ReplaceWrappedKeys replaces ALL
// stored wrapped keys in one transaction: all of them, or none. It returns an
// error that matches store.ErrConflict if the stored keys are not exactly the
// keys it was given.
type VaultStore interface {
	LatestTenantKeyVersion(ctx context.Context, tenant account.TenantID) (int, error)
	TenantKey(ctx context.Context, tenant account.TenantID, version int) (wrapped []byte, kekID string, err error)
	PutTenantKey(ctx context.Context, tenant account.TenantID, version int, wrapped []byte, kekID string) error
	RewrapTenantKey(ctx context.Context, tenant account.TenantID, version int, wrapped []byte, kekID string) error
	ListTenantKeys(ctx context.Context) ([]store.TenantKeyRow, error)

	ServerKey(ctx context.Context) (wrapped []byte, kekID string, err error)
	PutServerKey(ctx context.Context, wrapped []byte, kekID string) error
	RewrapServerKey(ctx context.Context, wrapped []byte, kekID string) error

	ReplaceWrappedKeys(ctx context.Context, tenantKeys []store.TenantKeyRow, serverWrapped []byte, serverKEKID string) error
}

var _ VaultStore = (*store.Store)(nil)

// dekID names one tenant data key in the cache.
type dekID struct {
	tenant  account.TenantID
	version int
}

// Vault does envelope encryption. Each tenant has its own random data key
// (DEK). The DEK seals the secrets of that tenant. The DEK is stored only
// wrapped (encrypted) by the master key (KEK). The Vault also keeps one random
// server key, wrapped in the same way, for values that belong to the server and
// not to a tenant.
//
// A change of master key only wraps the DEKs and the server key again. The
// sealed values are not touched. See RewrapAll.
//
// A Vault is safe for use by many goroutines. It never prints key bytes. See
// Format.
type Vault struct {
	st VaultStore

	// rotation protects kek. Encrypt, Decrypt and ServerKey hold a read lock
	// for their whole run, so the master key cannot change while one of them
	// reads a stored key. RewrapAll holds the write lock.
	rotation sync.RWMutex
	kek      KEK

	// cacheMu protects the caches below. The caches hold key bytes in memory,
	// so that a call does not read and unwrap a key every time.
	cacheMu   sync.Mutex
	deks      map[dekID][]byte
	serverKey []byte
}

// NewVault returns a Vault that keeps keys in st and wraps them with kek.
func NewVault(st VaultStore, kek KEK) *Vault {
	return &Vault{st: st, kek: kek, deks: make(map[dekID][]byte)}
}

// Encrypt seals plaintext with the latest key of the tenant and returns the
// version of that key. Store the version next to the sealed value, because
// Decrypt needs it.
//
// The first time the Vault sees a tenant, it creates key version 1. The tenant
// must exist in the store.
//
// The aad (see AAD) is not stored. Decrypt needs the same aad.
func (v *Vault) Encrypt(ctx context.Context, tenant account.TenantID, aad string, plaintext []byte) (keyVersion int, sealed []byte, err error) {
	v.rotation.RLock()
	defer v.rotation.RUnlock()

	keyVersion, err = v.st.LatestTenantKeyVersion(ctx, tenant)
	if err != nil {
		return 0, nil, fmt.Errorf("secrets: find latest tenant key: %w", err)
	}
	var dek []byte
	if keyVersion == 0 {
		keyVersion = 1
		dek, err = v.createTenantKey(ctx, tenant, keyVersion)
	} else {
		dek, err = v.tenantKey(ctx, tenant, keyVersion)
	}
	if err != nil {
		return 0, nil, err
	}
	sealed, err = Seal(dek, plaintext, aad)
	if err != nil {
		return 0, nil, fmt.Errorf("secrets: encrypt: %w", err)
	}
	return keyVersion, sealed, nil
}

// Decrypt opens a value made by Encrypt. It uses the key version it is given.
// It never creates a key. If the tenant has no such version, the error matches
// store.ErrNotFound. If the key or the value cannot be opened, the error
// matches ErrDecrypt.
func (v *Vault) Decrypt(ctx context.Context, tenant account.TenantID, keyVersion int, aad string, sealed []byte) ([]byte, error) {
	v.rotation.RLock()
	defer v.rotation.RUnlock()

	dek, err := v.tenantKey(ctx, tenant, keyVersion)
	if err != nil {
		return nil, err
	}
	return Open(dek, sealed, aad)
}

// ServerKey returns the 32-byte server key. It creates a random key and stores
// it, wrapped, the first time it is called. The caller gets a copy.
func (v *Vault) ServerKey(ctx context.Context) ([]byte, error) {
	v.rotation.RLock()
	defer v.rotation.RUnlock()

	key, ok := v.cachedServerKey()
	if !ok {
		var err error
		key, err = v.readServerKey(ctx)
		if errors.Is(err, store.ErrNotFound) {
			key, err = v.createServerKey(ctx)
		}
		if err != nil {
			return nil, err
		}
	}
	return bytes.Clone(key), nil
}

// DerivedServerKey returns a 32-byte key for one use of the server key: the
// HMAC-SHA-256 of label under the server key. Each use has its own label, such
// as "signing/v1" for the token signer, so the key of one use tells nothing
// about the key of another, and a value made for one use is not valid for
// another. The raw server key is never used as a key itself.
//
// The caller gets its own copy and should clear it after use. The copy of the
// server key that this reads is cleared here.
func (v *Vault) DerivedServerKey(ctx context.Context, label string) ([]byte, error) {
	serverKey, err := v.ServerKey(ctx)
	if err != nil {
		return nil, err
	}
	defer clear(serverKey)
	m := hmac.New(sha256.New, serverKey)
	_, _ = m.Write([]byte(label)) // a write to a hash never fails
	return m.Sum(nil), nil
}

// RewrapAll wraps every tenant key and the server key again, with newKEK. The
// Vault then uses newKEK. The sealed values are not changed, because the DEKs
// stay the same.
//
// It is all or nothing. First it unwraps every key with the current master key,
// wraps it with newKEK, and checks that newKEK can unwrap the result. All of
// this happens in memory. Then it saves every new wrapped key in one database
// transaction, and the store checks in that transaction that the stored keys
// are exactly the keys that RewrapAll listed. If anything fails, the stored
// keys are as they were and the Vault keeps its current master key and cache.
//
// If a key was added after RewrapAll listed the keys (by another program, or by
// a second Vault), that key is wrapped by the old master key and RewrapAll did
// not see it. The store refuses the change, and RewrapAll returns the store's
// error, which matches store.ErrConflict. Run the rotation again.
//
// A nil result is the signal that every stored key is wrapped by newKEK and
// that the old master key is no longer needed. The caller must keep to these
// rules:
//   - Store the new master key durably BEFORE calling RewrapAll. After a
//     success, the old master key cannot open any stored key.
//   - Keep the old master key until RewrapAll returns nil. After an error the
//     stored keys are still wrapped by the old master key.
//   - Stop every other process that uses the database during the rotation, and
//     start them again afterwards. Their Vaults hold the old master key and
//     old cached keys. They cannot open the keys after a success. A running
//     process can also add a key during the rotation, which makes it fail with
//     store.ErrConflict.
//   - Use exactly one Vault per process. The lock that makes RewrapAll wait for
//     running calls belongs to one Vault, so a second Vault on the same
//     database is the same as another process.
//
// RewrapAll waits for running calls of this Vault to finish, and calls that
// start meanwhile wait for it.
func (v *Vault) RewrapAll(ctx context.Context, newKEK KEK) error {
	if newKEK == nil {
		return errors.New("secrets: rewrap: new master key is nil")
	}
	v.rotation.Lock()
	defer v.rotation.Unlock()

	rows, err := v.st.ListTenantKeys(ctx)
	if err != nil {
		return fmt.Errorf("secrets: rewrap: list tenant keys: %w", err)
	}
	newKEKID := newKEK.ID()
	newRows := make([]store.TenantKeyRow, len(rows))
	for i, row := range rows {
		wrapped, err := rewrapKey(ctx, v.kek, newKEK, row.Wrapped, "tenant key")
		if err != nil {
			return err
		}
		newRows[i] = store.TenantKeyRow{Tenant: row.Tenant, Version: row.Version, Wrapped: wrapped, KEKID: newKEKID}
	}

	// A server key that does not exist yet is not an error. There is nothing to
	// wrap again, and RewrapAll must not create it.
	var newServerWrapped []byte
	serverWrapped, _, err := v.st.ServerKey(ctx)
	switch {
	case err == nil:
		newServerWrapped, err = rewrapKey(ctx, v.kek, newKEK, serverWrapped, "server key")
		if err != nil {
			return err
		}
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("secrets: rewrap: read server key: %w", err)
	}

	// The error is returned as it is. The store's message already says what
	// happened, and for store.ErrConflict it says what to do.
	if err := v.st.ReplaceWrappedKeys(ctx, newRows, newServerWrapped, newKEKID); err != nil {
		return err
	}

	// The stored keys now belong to newKEK. From here on the Vault must use it.
	v.kek = newKEK
	v.clearCaches()
	return nil
}

// Format makes every fmt verb print "[redacted]". The Vault holds keys in its
// caches, and printing the struct would show them.
func (*Vault) Format(f fmt.State, _ rune) {
	// A write error to a fmt buffer cannot be reported to the caller of Sprintf.
	_, _ = io.WriteString(f, redacted)
}

// LogValue makes log/slog log "[redacted]" for a Vault.
func (*Vault) LogValue() slog.Value { return slog.StringValue(redacted) }

// tenantKey returns the unwrapped DEK of one version. The caller holds the
// read lock. The caller must not change the returned bytes.
func (v *Vault) tenantKey(ctx context.Context, tenant account.TenantID, version int) ([]byte, error) {
	id := dekID{tenant: tenant, version: version}
	if dek, ok := v.cachedDEK(id); ok {
		return dek, nil
	}
	wrapped, _, err := v.st.TenantKey(ctx, tenant, version)
	if err != nil {
		return nil, fmt.Errorf("secrets: read tenant key version %d: %w", version, err)
	}
	// The stored key ID is not used. Only the configured master key unwraps.
	// If it is the wrong key, unwrapKey fails.
	dek, err := unwrapKey(ctx, v.kek, wrapped, "tenant key")
	if err != nil {
		return nil, err
	}
	return v.cacheDEK(id, dek), nil
}

// createTenantKey makes a new DEK for a tenant that has none and stores it.
//
// Two programs can reach this at the same time. The store keeps the first key
// and returns store.ErrExists to the other. That program drops its own key and
// uses the stored one, because values sealed with its own key could never be
// opened again.
func (v *Vault) createTenantKey(ctx context.Context, tenant account.TenantID, version int) ([]byte, error) {
	dek, wrapped, err := v.newWrappedKey(ctx, "tenant key")
	if err != nil {
		return nil, err
	}
	err = v.st.PutTenantKey(ctx, tenant, version, wrapped, v.kek.ID())
	switch {
	case err == nil:
		return v.cacheDEK(dekID{tenant: tenant, version: version}, dek), nil
	case errors.Is(err, store.ErrExists):
		clear(dek)
		return v.tenantKey(ctx, tenant, version)
	default:
		clear(dek)
		return nil, fmt.Errorf("secrets: store tenant key version %d: %w", version, err)
	}
}

// readServerKey reads, unwraps and caches the stored server key. It returns an
// error that matches store.ErrNotFound if there is none.
func (v *Vault) readServerKey(ctx context.Context) ([]byte, error) {
	wrapped, _, err := v.st.ServerKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("secrets: read server key: %w", err)
	}
	key, err := unwrapKey(ctx, v.kek, wrapped, "server key")
	if err != nil {
		return nil, err
	}
	return v.cacheServerKey(key), nil
}

// createServerKey makes the server key and stores it. If another program stored
// one first, it uses the stored key. See createTenantKey.
func (v *Vault) createServerKey(ctx context.Context) ([]byte, error) {
	key, wrapped, err := v.newWrappedKey(ctx, "server key")
	if err != nil {
		return nil, err
	}
	err = v.st.PutServerKey(ctx, wrapped, v.kek.ID())
	switch {
	case err == nil:
		return v.cacheServerKey(key), nil
	case errors.Is(err, store.ErrExists):
		clear(key)
		return v.readServerKey(ctx)
	default:
		clear(key)
		return nil, fmt.Errorf("secrets: store server key: %w", err)
	}
}

// newWrappedKey makes a random 32-byte key and wraps it with the master key.
func (v *Vault) newWrappedKey(ctx context.Context, what string) (key, wrapped []byte, err error) {
	key = make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, fmt.Errorf("secrets: create %s: %w", what, err)
	}
	wrapped, err = v.kek.Wrap(ctx, key)
	if err != nil {
		clear(key)
		return nil, nil, fmt.Errorf("secrets: wrap %s: %w", what, err)
	}
	return key, wrapped, nil
}

func (v *Vault) cachedDEK(id dekID) ([]byte, bool) {
	v.cacheMu.Lock()
	defer v.cacheMu.Unlock()
	dek, ok := v.deks[id]
	return dek, ok
}

// cacheDEK keeps a DEK and returns it. The caller got the DEK from the store,
// so it replaces an older entry. Calls that already use the older slice are not
// affected, because nothing changes its bytes while they hold the read lock.
func (v *Vault) cacheDEK(id dekID, dek []byte) []byte {
	v.cacheMu.Lock()
	defer v.cacheMu.Unlock()
	v.deks[id] = dek
	return dek
}

func (v *Vault) cachedServerKey() ([]byte, bool) {
	v.cacheMu.Lock()
	defer v.cacheMu.Unlock()
	return v.serverKey, v.serverKey != nil
}

// cacheServerKey is the same as cacheDEK, for the server key.
func (v *Vault) cacheServerKey(key []byte) []byte {
	v.cacheMu.Lock()
	defer v.cacheMu.Unlock()
	v.serverKey = key
	return key
}

// clearCaches forgets all cached keys and overwrites their bytes. The caller
// holds the write lock, so no call is using the bytes.
func (v *Vault) clearCaches() {
	v.cacheMu.Lock()
	defer v.cacheMu.Unlock()
	for id, dek := range v.deks {
		clear(dek)
		delete(v.deks, id)
	}
	clear(v.serverKey)
	v.serverKey = nil
}

// unwrapKey unwraps a stored key with kek and checks its size. The what text
// names the key in error messages. The errors never contain key bytes.
func unwrapKey(ctx context.Context, kek KEK, wrapped []byte, what string) ([]byte, error) {
	key, err := kek.Unwrap(ctx, wrapped)
	if err != nil {
		if errors.Is(err, ErrDecrypt) {
			return nil, fmt.Errorf("secrets: unwrap %s: wrong master key or corrupted data: %w", what, err)
		}
		// Another error, for example from a remote master key that cannot be
		// reached. It does not say that the key is wrong.
		return nil, fmt.Errorf("secrets: unwrap %s: %w", what, err)
	}
	if len(key) != keySize {
		clear(key)
		return nil, fmt.Errorf("secrets: unwrap %s: corrupted data: the key has the wrong size", what)
	}
	return key, nil
}

// rewrapKey unwraps a stored key with oldKEK and wraps it with newKEK. It
// then unwraps the result with newKEK and checks that the key is the same. A
// master key that cannot open what it wrapped must not replace the stored
// keys: the data would be lost.
func rewrapKey(ctx context.Context, oldKEK, newKEK KEK, wrapped []byte, what string) ([]byte, error) {
	key, err := unwrapKey(ctx, oldKEK, wrapped, what)
	if err != nil {
		return nil, fmt.Errorf("secrets: rewrap: %w", err)
	}
	defer clear(key)

	rewrapped, err := newKEK.Wrap(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("secrets: rewrap: wrap %s with the new master key: %w", what, err)
	}
	check, err := newKEK.Unwrap(ctx, rewrapped)
	if err != nil {
		return nil, fmt.Errorf("secrets: rewrap: the new master key cannot unwrap the %s it wrapped: %w", what, err)
	}
	defer clear(check)
	if subtle.ConstantTimeCompare(check, key) != 1 {
		return nil, fmt.Errorf("secrets: rewrap: the new master key unwrapped the %s to a different key", what)
	}
	return rewrapped, nil
}
