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
	"time"

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
	// disconnected. A change of the settings or of the password needs no call:
	// Mail notices it by itself.
	Forget(id account.AccountID)
}

// passwordSecret is the kind of the stored secret that holds the account
// password.
const passwordSecret = "password"

// retireAfter is how long a replaced backend stays open. A use case that
// started before the replacement may still make calls on it.
const retireAfter = 2 * time.Minute

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

// slot is the place of one account in the cache. A call to Mail takes the slot
// for its whole run, so calls for the same account take turns and calls for
// other accounts do not wait for it.
type slot struct {
	// lock has room for one value. A call holds the slot while it has put its
	// value in lock. It is a channel and not a mutex, so a call that waits can
	// also stop when its context ends.
	lock chan struct{}
	// e, dead and lastUsed are read and written only by the holder of the slot.
	e *backendEntry
	// dead is true after Forget, Close or SweepIdle took the slot out of the
	// cache. A call that finds it set starts again with a new slot.
	dead bool
	// lastUsed is the time of the last call to Mail that got the backend of the
	// slot. SweepIdle compares it with the idle time.
	lastUsed time.Time
}

// retiredBackend is a backend that was replaced and waits to be closed.
type retiredBackend struct {
	key  backendKey
	stop func() bool // stops the timer
}

// BackendCache is the default BackendFactory. It builds one IMAP and SMTP
// backend per account and keeps it, so the connection pool of the account is
// reused between requests.
//
// Another process can change an account while this one runs. For example, the
// command line tool can set a new password, and it cannot call Forget in the
// server. So Mail reads the sealed password again on every call and compares a
// fingerprint of the server settings and the sealed password with the one of
// the cached backend. When they differ, Mail builds a new backend. It decrypts
// the password only when it builds a backend.
//
// The replaced backend is not closed at once, because a use case that started
// earlier may still make calls on it. It is retired: it closes after
// retireAfter. Forget (the account is deleted or disconnected) and Close (the
// program ends) close at once, retired backends included.
//
// Another process can also delete an account. Nothing asks this process for
// that account again, so its backend, and the password in it, would stay until
// the program ends. SweepIdle closes the backends that no call asked for in a
// while. The program calls it now and then.
//
// Each account has its own slot (see slot), so a slow build for one account does
// not stop the others. The cache lock c.mu protects only the two maps. Nobody
// holds it while waiting for a slot, or while reading the database.
//
// A BackendCache is safe for use by many goroutines.
type BackendCache struct {
	secrets SecretStore
	vault   *secrets.Vault
	signer  *signing.Signer
	local   bool
	logger  *slog.Logger

	// The four functions below are set by the constructor. Tests replace them.
	decrypt    func(ctx context.Context, tenant account.TenantID, keyVersion int, aad string, sealed []byte) ([]byte, error)
	newBackend func(cfg imapsmtp.Config, signer *signing.Signer) *imapsmtp.Backend
	afterFunc  func(d time.Duration, f func()) (stop func() bool)
	now        func() time.Time

	// mu protects slots and retired.
	mu      sync.Mutex
	slots   map[backendKey]*slot
	retired map[*imapsmtp.Backend]retiredBackend
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
		secrets:    st,
		vault:      v,
		signer:     signer,
		local:      local,
		logger:     logger,
		decrypt:    v.Decrypt,
		newBackend: imapsmtp.New,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
		now:     time.Now,
		slots:   make(map[backendKey]*slot),
		retired: make(map[*imapsmtp.Backend]retiredBackend),
	}
}

// Mail returns the cached backend of the account, or builds one. See
// BackendCache for when it builds a new one.
//
// If another call holds the account (it builds a backend), Mail waits for it,
// and stops waiting when ctx ends.
func (c *BackendCache) Mail(ctx context.Context, a *account.Account) (mail.Backend, error) {
	if a.Mail == nil {
		return nil, errors.New("service: the account has no mail server settings")
	}
	key := backendKey{tenant: a.TenantID, id: a.ID}
	for {
		b, again, err := c.mailOn(ctx, c.slotFor(key), key, a)
		if again {
			continue
		}
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}

// mailOn is one try of Mail on the slot s. It takes the slot, so it waits for a
// call that holds it, and stops waiting when ctx ends. It returns again = true
// when Forget or Close took the slot out of the cache while the call waited:
// the caller must then get the slot again and try again.
func (c *BackendCache) mailOn(ctx context.Context, s *slot, key backendKey, a *account.Account) (b *imapsmtp.Backend, again bool, err error) {
	select {
	case s.lock <- struct{}{}:
	case <-ctx.Done():
		return nil, false, fmt.Errorf("service: wait for the mail backend: %w", ctx.Err())
	}
	defer func() { <-s.lock }()
	if s.dead {
		return nil, true, nil
	}
	b, err = c.fill(ctx, s, key, a)
	if err == nil {
		s.lastUsed = c.now()
	}
	return b, false, err
}

// slotFor returns the slot of the key and makes it if there is none.
func (c *BackendCache) slotFor(key backendKey) *slot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.slots[key]
	if s == nil {
		s = &slot{lock: make(chan struct{}, 1)}
		c.slots[key] = s
	}
	return s
}

// fill returns the backend of the slot. It builds a new one when the slot is
// empty or when the settings or the password changed. The caller holds the
// slot.
func (c *BackendCache) fill(ctx context.Context, s *slot, key backendKey, a *account.Account) (*imapsmtp.Backend, error) {
	keyVersion, sealed, err := c.secrets.GetAccountSecret(ctx, a.TenantID, a.ID, passwordSecret)
	if errors.Is(err, store.ErrNotFound) {
		// The password is gone, so a backend that was built with it must go too.
		c.closeEntry(s)
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

	if s.e != nil {
		if s.e.fingerprint == fingerprint {
			return s.e.backend, nil
		}
		// The settings or the password changed. Calls that already use the old
		// backend finish on it. It closes later.
		c.retire(key, s.e.backend)
		s.e = nil
	}

	b, err := c.build(ctx, a, keyVersion, sealed)
	if err != nil {
		return nil, err
	}
	s.e = &backendEntry{backend: b, fingerprint: fingerprint}
	return b, nil
}

// build decrypts the password and makes a backend. It does not connect.
func (c *BackendCache) build(ctx context.Context, a *account.Account, keyVersion int, sealed []byte) (*imapsmtp.Backend, error) {
	aad := secrets.AAD(string(a.TenantID), string(a.ID), passwordSecret)
	plain, err := c.decrypt(ctx, a.TenantID, keyVersion, aad, sealed)
	if err != nil {
		return nil, fmt.Errorf("service: decrypt the account password: %w", err)
	}
	password := secrets.Secret(plain)
	// The Secret holds its own copy of the text. Clear the bytes of the
	// plaintext.
	clear(plain)

	return c.newBackend(imapsmtp.Config{
		Server:     *a.Mail,
		Password:   password,
		Local:      c.local,
		TokenScope: string(a.TenantID) + "/" + string(a.ID),
		Logger:     c.logger,
	}, c.signer), nil
}

// closeEntry closes the backend of the slot at once and empties the slot. The
// caller holds the slot.
func (c *BackendCache) closeEntry(s *slot) {
	if s.e != nil {
		// Close only releases connections, and it never fails.
		_ = s.e.backend.Close()
		s.e = nil
	}
}

// retire starts the timer that closes a replaced backend.
func (c *BackendCache) retire(key backendKey, b *imapsmtp.Backend) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stop := c.afterFunc(retireAfter, func() { c.closeRetired(b) })
	c.retired[b] = retiredBackend{key: key, stop: stop}
}

// closeRetired closes a retired backend when its timer fires. It does nothing
// when Forget or Close has closed the backend already.
func (c *BackendCache) closeRetired(b *imapsmtp.Backend) {
	c.mu.Lock()
	_, waiting := c.retired[b]
	delete(c.retired, b)
	c.mu.Unlock()
	if waiting {
		_ = b.Close()
	}
}

// Forget closes and removes every cached backend whose account has this id, and
// the retired backends of that account. It waits for a call that is building a
// backend for the account, so the backend of that call is closed too.
func (c *BackendCache) Forget(id account.AccountID) {
	c.remove(func(key backendKey) bool { return key.id == id })
}

// SweepIdle closes and removes the cached backends whose last call to Mail was
// more than maxIdle ago, and returns how many it closed. It also removes the
// empty slots of such accounts. A slot that a call holds right now (it builds a
// backend) is in use: SweepIdle skips it and does not wait for it. The retired
// backends are not touched: their timers close them.
//
// The idle time must be much longer than any use case runs with a backend that
// it got from Mail (a send can take some minutes), because a backend that is
// closed here fails every later call on it.
func (c *BackendCache) SweepIdle(maxIdle time.Duration) int {
	cutoff := c.now().Add(-maxIdle)
	c.mu.Lock()
	slots := make(map[backendKey]*slot, len(c.slots))
	for key, s := range c.slots {
		slots[key] = s
	}
	c.mu.Unlock()

	closed := 0
	for key, s := range slots {
		select {
		case s.lock <- struct{}{}:
		default:
			continue // a call holds the slot, so it is in use
		}
		if !s.dead && s.lastUsed.Before(cutoff) {
			c.mu.Lock()
			if c.slots[key] == s {
				delete(c.slots, key)
			}
			c.mu.Unlock()
			if s.e != nil {
				c.closeEntry(s)
				closed++
			}
			s.dead = true
		}
		<-s.lock
	}
	return closed
}

// Close closes every cached backend and every retired backend. Later calls to
// Mail build new backends.
func (c *BackendCache) Close() {
	c.remove(func(backendKey) bool { return true })
}

// remove closes and removes the slots and the retired backends whose key
// matches.
func (c *BackendCache) remove(match func(backendKey) bool) {
	// Take the slots out of the map first, and wait for them after c.mu is free.
	// Nobody may wait for a slot while holding c.mu.
	c.mu.Lock()
	var slots []*slot
	for key, s := range c.slots {
		if match(key) {
			slots = append(slots, s)
			delete(c.slots, key)
		}
	}
	c.mu.Unlock()

	// Close the retired backends now, before the wait for the slots.
	c.closeRetiredMatching(match)
	for _, s := range slots {
		s.lock <- struct{}{} // wait for the call that holds the slot
		c.closeEntry(s)
		s.dead = true
		<-s.lock
	}
	// A call that held a slot while we waited may have retired a backend after
	// the first look. Every such call has ended now, because we held its slot
	// and marked it dead. Look again, so that this backend is closed now and does
	// not wait for its timer.
	c.closeRetiredMatching(match)
}

// closeRetiredMatching closes the retired backends whose key matches and stops
// their timers. A backend leaves the map before it is closed, so it is closed
// once: by this function or by its timer, never by both.
func (c *BackendCache) closeRetiredMatching(match func(backendKey) bool) {
	c.mu.Lock()
	var retired []*imapsmtp.Backend
	for b, r := range c.retired {
		if match(r.key) {
			r.stop()
			retired = append(retired, b)
			delete(c.retired, b)
		}
	}
	c.mu.Unlock()
	for _, b := range retired {
		_ = b.Close()
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
