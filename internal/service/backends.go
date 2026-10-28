package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// BackendFactory gives the service the mail backend of an account.
type BackendFactory interface {
	// Mail returns the mail backend of the account. The caller must not close
	// it: the factory owns it.
	Mail(ctx context.Context, a *account.Account) (mail.Backend, error)
	// Forget closes and drops the backends of the account with this id. The
	// next call to Mail builds a new one. Call it when an account is deleted or
	// changed by the same process.
	Forget(id account.AccountID)
}

// passwordSecret is the kind of the stored secret that holds the account
// password.
const passwordSecret = "password"

// SecretStore reads the sealed secrets of accounts. *store.Store implements it.
type SecretStore interface {
	GetAccountSecret(ctx context.Context, tenant account.TenantID, acct account.AccountID, kind string) (keyVersion int, sealed []byte, err error)
}

var _ SecretStore = (*store.Store)(nil)

// backendKey names one cached backend.
type backendKey struct {
	tenant account.TenantID
	id     account.AccountID
}

// backendEntry is a cached backend and the fingerprint of the settings it was
// built from.
type backendEntry struct {
	backend     *imapsmtp.Backend
	fingerprint [sha256.Size]byte
}

// BackendCache is the default BackendFactory. It builds one IMAP and SMTP
// backend per account and keeps it, so the connection pool of the account is
// reused between requests.
//
// Another process can change an account while this one runs. For example, the
// command line tool can set a new password, and it cannot call Forget in the
// server. So Mail reads the sealed password again on every call and compares a
// fingerprint of the server settings and the sealed password with the one of
// the cached backend. When they differ, Mail closes the cached backend and
// builds a new one. Mail decrypts the password only when it builds a backend.
//
// A BackendCache is safe for use by many goroutines.
type BackendCache struct {
	secrets SecretStore
	vault   *secrets.Vault
	signer  *signing.Signer
	local   bool
	logger  *slog.Logger

	// mu protects entries. Mail holds it for its whole run, including the read
	// of the sealed password. That read is one local database lookup, and a
	// single lock is what makes it certain that two calls for the same account
	// never leave two live backends. Closing a backend does not wait for
	// anything, so it is safe under the lock.
	mu      sync.Mutex
	entries map[backendKey]*backendEntry
}

var _ BackendFactory = (*BackendCache)(nil)

// NewBackendFactory returns the default BackendFactory.
//
// st gives the sealed password of an account. The vault decrypts it. The signer
// makes the page tokens and cursors of the backends. local is true for a local
// install, which allows a plain connection to a loopback host. logger may be
// nil.
//
// It panics when st, v or signer is nil, because the factory could not work.
func NewBackendFactory(st SecretStore, v *secrets.Vault, signer *signing.Signer, local bool, logger *slog.Logger) *BackendCache {
	if st == nil || v == nil || signer == nil {
		panic("service: NewBackendFactory needs a secret store, a vault and a signer")
	}
	return &BackendCache{
		secrets: st,
		vault:   v,
		signer:  signer,
		local:   local,
		logger:  logger,
		entries: make(map[backendKey]*backendEntry),
	}
}

// Mail returns the cached backend of the account, or builds one. See
// BackendCache for when it builds a new one.
func (c *BackendCache) Mail(ctx context.Context, a *account.Account) (mail.Backend, error) {
	if a.Mail == nil {
		return nil, errors.New("service: the account has no mail server settings")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := backendKey{tenant: a.TenantID, id: a.ID}
	keyVersion, sealed, err := c.secrets.GetAccountSecret(ctx, a.TenantID, a.ID, passwordSecret)
	if errors.Is(err, store.ErrNotFound) {
		// The password is gone, so a backend that was built with it must go too.
		c.dropLocked(key)
		// Do not pass store.ErrNotFound on. A caller could take it for "account
		// not found", and that is a different case.
		return nil, errors.New("service: the account has no stored password")
	}
	if err != nil {
		return nil, fmt.Errorf("service: read the account password: %w", err)
	}
	fingerprint, err := fingerprintOf(a.Mail, keyVersion, sealed)
	if err != nil {
		return nil, err
	}

	if cached := c.entries[key]; cached != nil {
		if cached.fingerprint == fingerprint {
			return cached.backend, nil
		}
		// The settings or the password changed. Close the old backend now, so it
		// does not stay open if the new one cannot be built. A call that still
		// uses the old backend finishes. Calls that start later on it fail.
		c.dropLocked(key)
	}

	b, err := c.build(ctx, a, keyVersion, sealed)
	if err != nil {
		return nil, err
	}
	c.entries[key] = &backendEntry{backend: b, fingerprint: fingerprint}
	return b, nil
}

// build decrypts the password and makes a backend. It does not connect.
func (c *BackendCache) build(ctx context.Context, a *account.Account, keyVersion int, sealed []byte) (*imapsmtp.Backend, error) {
	aad := secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret)
	plain, err := c.vault.Decrypt(ctx, a.TenantID, keyVersion, aad, sealed)
	if err != nil {
		return nil, fmt.Errorf("service: decrypt the account password: %w", err)
	}
	password := secrets.Secret(plain)
	// The Secret holds its own copy of the text. Clear the bytes of the
	// plaintext.
	clear(plain)

	return imapsmtp.New(imapsmtp.Config{
		Server:     *a.Mail,
		Password:   password,
		Local:      c.local,
		TokenScope: string(a.TenantID) + "/" + string(a.ID),
		Logger:     c.logger,
	}, c.signer), nil
}

// dropLocked closes and removes the cached backend for key, if there is one.
// The caller must hold c.mu.
func (c *BackendCache) dropLocked(key backendKey) {
	if e, ok := c.entries[key]; ok {
		// Close only releases connections, and it never fails.
		_ = e.backend.Close()
		delete(c.entries, key)
	}
}

// Forget closes and removes every cached backend whose account has this id.
func (c *BackendCache) Forget(id account.AccountID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.id == id {
			c.dropLocked(key)
		}
	}
}

// Close closes every cached backend. Later calls to Mail build new backends.
func (c *BackendCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		c.dropLocked(key)
	}
}

// fingerprintOf returns a SHA-256 hash over the server settings, the version of
// the key and the sealed password. If any of them changes, the hash changes.
//
// The settings are hashed as JSON, so a field that is added to
// account.MailServer later is part of the hash without a change here. The
// parts are written with their lengths, so two different inputs cannot give the
// same bytes.
func fingerprintOf(server *account.MailServer, keyVersion int, sealed []byte) ([sha256.Size]byte, error) {
	settings, err := json.Marshal(server)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("service: encode the mail settings: %w", err)
	}
	h := sha256.New()
	for _, part := range [][]byte{settings, []byte(strconv.Itoa(keyVersion)), sealed} {
		// Writing to a hash never fails.
		_, _ = h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(part))))
		_, _ = h.Write(part)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum, nil
}
