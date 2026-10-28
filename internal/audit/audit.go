// Package audit keeps the log of what agents did: for each tool call, which
// tool, which account and area, how many items, and the outcome. The Activity
// page of the web interface reads it.
//
// The log holds no mail content. The one personal field is the list of
// recipients, and it is stored encrypted (sealed) with the key of the tenant.
// The other fields are checked on the way in: Detail is a short code, and Tool
// and Client are short text without control characters. Nothing in this package
// writes recipients, details or mail content to a log or into an error.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// Outcome says how a tool call ended.
type Outcome string

// The outcomes.
const (
	// OutcomeOK means that the tool did what the agent asked.
	OutcomeOK Outcome = "ok"
	// OutcomeRefused means that the server refused, for example because of the
	// permission level or a rate limit. Detail says why.
	OutcomeRefused Outcome = "refused"
	// OutcomeDeclined means that the user declined the action.
	OutcomeDeclined Outcome = "declined"
	// OutcomePendingApproval means that the action waits for the user.
	OutcomePendingApproval Outcome = "pending_approval"
	// OutcomeError means that the tool failed.
	OutcomeError Outcome = "error"
)

// valid reports whether o is one of the outcomes.
func (o Outcome) valid() bool {
	switch o {
	case OutcomeOK, OutcomeRefused, OutcomeDeclined, OutcomePendingApproval, OutcomeError:
		return true
	}
	return false
}

// The limits of this package.
const (
	// maxText is the most bytes of a Tool or a Client.
	maxText = 128
	// defaultLimit is the number of entries that List returns when the filter
	// has no limit.
	defaultLimit = 100
	// maxLimit is the most entries that List returns.
	maxLimit = 1000
	// storeTimeout is the time that Record gives to its database work. Record
	// does not use the context of the caller for it. See detached.
	storeTimeout = 30 * time.Second
)

// recipientsField is the field name of the recipients in the associated data of
// the encryption.
const recipientsField = "audit:recipients"

// detailPattern is the form of Detail: a code of 0 to 64 characters. In Go, "$"
// matches only at the end of the text, so a code with a newline at the end does
// not match.
var detailPattern = regexp.MustCompile(`^[a-z0-9_]{0,64}$`)

// Entry is one tool call of an agent.
type Entry struct {
	// ID is assigned by the log. Record ignores the ID of the entry.
	ID int64
	// Time is the time of the call. Record sets it from the clock of the log and
	// ignores the time of the entry. It is kept as whole milliseconds, in UTC.
	Time   time.Time
	Tenant account.TenantID
	// Account is empty for an entry that is not about one account. Then Area may
	// be empty too.
	Account account.AccountID
	Area    account.Area
	// Client is the name that the MCP client gave for itself. It is not trusted:
	// Record accepts at most 128 bytes of valid UTF-8 without control characters.
	Client string
	// Tool is the name of the tool, with the same rules as Client.
	Tool string
	// Count is the number of items the call affected. It is not negative.
	Count int
	// Recipients are the addresses the call sent to or acted on. They are stored
	// encrypted. A call without recipients has none.
	Recipients []string
	Outcome    Outcome
	// Detail is a short code that says more about the outcome, such as
	// "rate_limited" or "level_too_low". It is 0 to 64 characters from a-z, 0-9
	// and "_". It is never free text and never holds mail content.
	Detail string
}

// Filter selects entries. A field that is empty means "any". Account, Area and
// Tool must match exactly when they are set.
type Filter struct {
	Account string
	Area    string
	Tool    string
	// Limit is the most entries to return. 0 means 100. A value above 1000 is
	// treated as 1000. A negative value is an error.
	Limit int
}

// AuditStore is the part of the store that the Log needs. *store.Store
// implements it.
//
// The rows hold the recipients sealed. The store does not look inside them.
// PruneAudit works on the entries of all tenants.
type AuditStore interface {
	CreateAudit(ctx context.Context, row *store.AuditRow) error
	ListAudit(ctx context.Context, tenant account.TenantID, f store.AuditFilter) ([]*store.AuditRow, error)
	PruneAudit(ctx context.Context, before time.Time) (int, error)
}

var _ AuditStore = (*store.Store)(nil)

// Log is the audit log. It is safe for use by many goroutines.
type Log struct {
	st    AuditStore
	vault *secrets.Vault
	now   func() time.Time
}

// New returns a Log. The vault seals the recipients. now is the clock, and a nil
// now means time.Now.
func New(st AuditStore, v *secrets.Vault, now func() time.Time) *Log {
	if now == nil {
		now = time.Now
	}
	return &Log{st: st, vault: v, now: now}
}

// Record adds an entry. It returns an error and stores nothing if the entry is
// not valid: see the fields of Entry. The account of the entry must belong to
// the tenant.
//
// The time of the entry is the time of the clock of the log. The time and the ID
// that the caller set are ignored.
//
// The entry is for an action that happened already, so the write does not use
// the cancellation of ctx. If the caller leaves while the tool runs, the entry is
// still written, and the call does not report an error for a write that
// happened. The write has a time limit of 30 seconds.
func (l *Log) Record(ctx context.Context, e Entry) error {
	if err := validate(e); err != nil {
		return err
	}
	row := &store.AuditRow{
		Time:    l.now().UTC().Truncate(time.Millisecond),
		Tenant:  e.Tenant,
		Account: e.Account,
		Area:    e.Area,
		Client:  e.Client,
		Tool:    e.Tool,
		Count:   e.Count,
		Outcome: string(e.Outcome),
		Detail:  e.Detail,
	}

	wctx, cancel := detached(ctx)
	defer cancel()
	// A call without recipients keeps NULL in the table: nothing to seal.
	if len(e.Recipients) > 0 {
		plain, err := json.Marshal(e.Recipients)
		if err != nil {
			// Strings always encode, so this does not happen. The message must not
			// hold the recipients in any case.
			return errors.New("audit: encode recipients")
		}
		version, sealed, err := l.vault.Encrypt(wctx, e.Tenant, recipientsAAD(e.Tenant, e.Account), plain)
		if err != nil {
			return fmt.Errorf("audit: encrypt recipients: %w", err)
		}
		row.Recipients, row.RecipientsKeyVersion = sealed, version
	}
	if err := l.st.CreateAudit(wctx, row); err != nil {
		return fmt.Errorf("audit: save entry: %w", err)
	}
	return nil
}

// List returns the entries of a tenant that match the filter, newest first.
// Entries with the same time come in the order of their IDs, largest first.
//
// If the recipients of any entry in the result cannot be decrypted, List returns
// an error and no entries. It never leaves recipients out without a sign.
func (l *Log) List(ctx context.Context, tenant account.TenantID, f Filter) ([]Entry, error) {
	limit := f.Limit
	switch {
	case limit < 0:
		return nil, errors.New("audit: the limit must not be negative")
	case limit == 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	rows, err := l.st.ListAudit(ctx, tenant, store.AuditFilter{
		Account: account.AccountID(f.Account),
		Area:    account.Area(f.Area),
		Tool:    f.Tool,
		Limit:   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("audit: list entries: %w", err)
	}

	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		e := Entry{
			ID:      row.ID,
			Time:    row.Time,
			Tenant:  row.Tenant,
			Account: row.Account,
			Area:    row.Area,
			Client:  row.Client,
			Tool:    row.Tool,
			Count:   row.Count,
			Outcome: Outcome(row.Outcome),
			Detail:  row.Detail,
		}
		if row.Recipients != nil {
			e.Recipients, err = l.openRecipients(ctx, row)
			if err != nil {
				return nil, err
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// openRecipients decrypts and decodes the recipients of a row. The errors name
// the ID of the entry and never the content.
func (l *Log) openRecipients(ctx context.Context, row *store.AuditRow) ([]string, error) {
	plain, err := l.vault.Decrypt(ctx, row.Tenant, row.RecipientsKeyVersion,
		recipientsAAD(row.Tenant, row.Account), row.Recipients)
	if err != nil {
		return nil, fmt.Errorf("audit: decrypt the recipients of entry %d: %w", row.ID, err)
	}
	var recipients []string
	if err := json.Unmarshal(plain, &recipients); err != nil {
		// The text of a JSON error can show a character of the content, so it is
		// not part of this error.
		return nil, fmt.Errorf("audit: the recipients of entry %d are not a list of addresses", row.ID)
	}
	return recipients, nil
}

// Prune deletes the entries that are older than olderThan, and returns how many
// it deleted. An entry is deleted when it was recorded strictly before now less
// olderThan. The app calls it every day with 90 days.
//
// Prune works on the entries of ALL tenants, on purpose. It is housekeeping for
// the whole program, and no call that is made for one user may use it.
// olderThan must not be negative.
func (l *Log) Prune(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan < 0 {
		return 0, errors.New("audit: the age for Prune must not be negative")
	}
	n, err := l.st.PruneAudit(ctx, l.now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("audit: prune entries: %w", err)
	}
	return n, nil
}

// recipientsAAD returns the associated data for the recipients of an entry.
//
// The ID of the entry is not part of it, because the database assigns the ID
// when it adds the row, after the recipients are sealed. So a sealed list that is
// copied to another entry of the same tenant and account opens there. A person
// who can write to the database can change the other columns of the row as well,
// so the ID in the associated data would add little.
func recipientsAAD(tenant account.TenantID, acct account.AccountID) string {
	return secrets.AAD(string(tenant), string(acct), recipientsField)
}

// validate checks an entry. The errors do not hold the values of the fields: the
// client is not trusted, and the other fields may hold personal data.
func validate(e Entry) error {
	if e.Tenant == "" {
		return errors.New("audit: the tenant is required")
	}
	// The associated data has no escaping, so an id with "|" is not allowed.
	if strings.Contains(string(e.Tenant), "|") || strings.Contains(string(e.Account), "|") {
		return errors.New("audit: the tenant id and the account id must not contain '|'")
	}
	if e.Account != "" && e.Area == "" {
		return errors.New("audit: the area is required when the entry has an account")
	}
	if !e.Outcome.valid() {
		return errors.New("audit: the outcome is not one of the known outcomes")
	}
	if e.Count < 0 {
		return errors.New("audit: the count must not be negative")
	}
	if !detailPattern.MatchString(e.Detail) {
		return errors.New("audit: the detail must be a code of at most 64 characters from a-z, 0-9 and _")
	}
	if !validText(e.Tool) {
		return errors.New("audit: the tool must be at most 128 bytes of valid UTF-8 without control characters")
	}
	if !validText(e.Client) {
		return errors.New("audit: the client must be at most 128 bytes of valid UTF-8 without control characters")
	}
	return nil
}

// validText reports whether s is at most 128 bytes of valid UTF-8 without
// control characters. Besides the control characters (Cc), it refuses the
// format characters (Cf, such as the right-to-left override, which makes a name
// read backwards) and the line and paragraph separators (Zl and Zp). A name with
// these characters can change how a line of the log looks on a page.
func validText(s string) bool {
	if len(s) > maxText || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// detached returns a context that ctx cannot cancel, with a time limit. It keeps
// the values of ctx. The driver of the database can commit a statement and still
// return the error of a context that ended just then. For a write that must not
// be lost, or reported as lost, the caller uses this context.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
}
