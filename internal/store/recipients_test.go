package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// recipientHash returns a 32-byte value that stands for the keyed hash of an
// address. The store does not care how it was made.
func recipientHash(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestKnownRecipientsAddAndLookup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	known, err := s.KnownRecipients(ctx, tenant, acct, [][]byte{recipientHash(1)})
	require.NoError(t, err)
	require.Equal(t, []bool{false}, known, "a new account knows nobody")

	require.NoError(t, s.AddKnownRecipients(ctx, tenant, acct, [][]byte{recipientHash(1), recipientHash(2)}, testNow))
	// A second add of the same hash changes nothing and is no error.
	require.NoError(t, s.AddKnownRecipients(ctx, tenant, acct, [][]byte{recipientHash(2), recipientHash(2)}, testNow))

	known, err = s.KnownRecipients(ctx, tenant, acct, [][]byte{recipientHash(2), recipientHash(3), recipientHash(1)})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, true}, known, "the answer is in the order of the question")

	var rows int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM known_recipients`).Scan(&rows))
	require.Equal(t, 2, rows)
}

func TestKnownRecipientsArePerAccountAndTenant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	_, other := newPendingAccount(t, s, "a2")
	ensureTestTenant(t, s, "t2")
	require.NoError(t, s.CreateAccount(ctx, testAccount("t2", "a1", testNow)))

	require.NoError(t, s.AddKnownRecipients(ctx, tenant, acct, [][]byte{recipientHash(1)}, testNow))

	known, err := s.KnownRecipients(ctx, tenant, other, [][]byte{recipientHash(1)})
	require.NoError(t, err)
	require.Equal(t, []bool{false}, known, "another account of the tenant does not share the list")

	known, err = s.KnownRecipients(ctx, "t2", "a1", [][]byte{recipientHash(1)})
	require.NoError(t, err)
	require.Equal(t, []bool{false}, known, "the same account id in another tenant does not share the list")
}

func TestKnownRecipientsRefuseBadInput(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	for name, hashes := range map[string][][]byte{
		"short hash": {make([]byte, 31)},
		"long hash":  {make([]byte, 33)},
		"nil hash":   {nil},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.AddKnownRecipients(ctx, tenant, acct, hashes, testNow))
			_, err := s.KnownRecipients(ctx, tenant, acct, hashes)
			require.Error(t, err)
		})
	}

	require.ErrorIs(t, s.AddKnownRecipients(ctx, "a|b", acct, [][]byte{recipientHash(1)}, testNow), ErrInvalidID)
	_, err := s.KnownRecipients(ctx, tenant, "a|b", [][]byte{recipientHash(1)})
	require.ErrorIs(t, err, ErrInvalidID)

	// An account that does not exist cannot get a list.
	require.Error(t, s.AddKnownRecipients(ctx, tenant, "missing", [][]byte{recipientHash(1)}, testNow))

	// Nothing to add or to look up is no error.
	require.NoError(t, s.AddKnownRecipients(ctx, tenant, acct, nil, testNow))
	known, err := s.KnownRecipients(ctx, tenant, acct, nil)
	require.NoError(t, err)
	require.Empty(t, known)
}

func TestKnownRecipientsDeletedWithTheAccount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")
	require.NoError(t, s.AddKnownRecipients(ctx, tenant, acct, [][]byte{recipientHash(1)}, testNow))

	require.NoError(t, s.DeleteAccount(ctx, tenant, acct))

	var rows int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM known_recipients`).Scan(&rows))
	require.Zero(t, rows)
}

func TestKnownRecipientsTableRefusesRowsThatBreakItsRules(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	tenant, acct := newPendingAccount(t, s, "a1")

	insert := func(hash any) error {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO known_recipients (tenant_id, account_id, hash, created_at) VALUES (?, ?, ?, 0)`,
			string(tenant), string(acct), hash)
		return err
	}
	require.Error(t, insert(make([]byte, 16)), "a hash must be 32 bytes")
	require.Error(t, insert("00000000000000000000000000000000"), "a hash must be a blob, not text")
	require.NoError(t, insert(make([]byte, 32)))

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO known_recipients (tenant_id, account_id, hash, created_at) VALUES (?, ?, ?, 0)`,
		string(tenant), "nobody", make([]byte, 32))
	require.Error(t, err, "the account must exist")
}
