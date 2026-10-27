package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// LocalTenantID is the id of the one tenant of a local install.
const LocalTenantID account.TenantID = "local"

// ErrInvalidID means that a tenant id or an account id is empty or contains
// "|". Every write checks its ids first.
//
// The encryption package builds associated data from the ids as
// "tenant:<id>|account:<id>" and does not escape them. An id with "|" in it
// could make the data for two different accounts equal.
var ErrInvalidID = errors.New("invalid id")

// validateID returns an error that wraps ErrInvalidID if id is empty or
// contains "|". The kind ("tenant" or "account") is used in the message. The
// message does not contain the id.
func validateID(kind, id string) error {
	if id == "" || strings.Contains(id, "|") {
		return fmt.Errorf("%w: %s id must not be empty or contain '|'", ErrInvalidID, kind)
	}
	return nil
}

// EnsureLocalTenant creates the tenant "local" if it does not exist, and
// returns its id.
func (s *Store) EnsureLocalTenant(ctx context.Context) (account.TenantID, error) {
	return s.ensureTenant(ctx, LocalTenantID)
}

// ensureTenant creates the tenant if it does not exist, and returns its id.
func (s *Store) ensureTenant(ctx context.Context, id account.TenantID) (account.TenantID, error) {
	if err := validateID("tenant", string(id)); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO tenants (id, created_at) VALUES (?, ?)`,
		string(id), time.Now().UnixMilli())
	if err != nil {
		return "", fmt.Errorf("store: ensure tenant: %w", err)
	}
	return id, nil
}
