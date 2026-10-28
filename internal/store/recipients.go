package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// recipientHashSize is the size of a known-recipient hash: an HMAC-SHA-256.
const recipientHashSize = 32

// AddKnownRecipients remembers that the account has sent mail to the addresses
// whose hashes are given. A hash that is known already is left as it is. The
// store never sees an address: the caller hashes it (see migration 0006). Every
// hash must be 32 bytes. now is the time of the send.
//
// All hashes are added in one transaction, or none is. It fails if the tenant has
// no account with this id.
func (s *Store) AddKnownRecipients(ctx context.Context, tenant account.TenantID, acct account.AccountID, hashes [][]byte, now time.Time) error {
	if err := validateAccountIDs(tenant, acct); err != nil {
		return err
	}
	if err := checkRecipientHashes(hashes); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: add known recipients: %w", err)
	}
	// Rollback after a successful Commit does nothing.
	defer func() { _ = tx.Rollback() }()
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO known_recipients (tenant_id, account_id, hash, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (tenant_id, account_id, hash) DO NOTHING`,
			string(tenant), string(acct), h, now.UnixMilli()); err != nil {
			return fmt.Errorf("store: add known recipients: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: add known recipients: %w", err)
	}
	return nil
}

// KnownRecipients reports, for each hash, whether the account has sent mail to
// that address. The answer has one value for each hash, in the same order.
// Every hash must be 32 bytes. The lookups run in one read transaction, so they
// see one state of the table.
func (s *Store) KnownRecipients(ctx context.Context, tenant account.TenantID, acct account.AccountID, hashes [][]byte) ([]bool, error) {
	if err := validateAccountIDs(tenant, acct); err != nil {
		return nil, err
	}
	if err := checkRecipientHashes(hashes); err != nil {
		return nil, err
	}
	known := make([]bool, len(hashes))
	if len(hashes) == 0 {
		return known, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: look up known recipients: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for i, h := range hashes {
		err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM known_recipients
				WHERE tenant_id = ? AND account_id = ? AND hash = ?)`,
			string(tenant), string(acct), h).Scan(&known[i])
		if err != nil {
			return nil, fmt.Errorf("store: look up known recipients: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: look up known recipients: %w", err)
	}
	return known, nil
}

// checkRecipientHashes refuses a hash that is not 32 bytes. The message does not
// show the hash.
func checkRecipientHashes(hashes [][]byte) error {
	for _, h := range hashes {
		if len(h) != recipientHashSize {
			return errors.New("store: a known-recipient hash must be 32 bytes")
		}
	}
	return nil
}
