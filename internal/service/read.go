package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

// maxAttachmentBytes is the largest attachment that GetAttachment returns:
// 10 MB. The backend refuses a larger one.
const maxAttachmentBytes = 10 << 20

// The limits of ListAccounts when it asks the backends what they support.
const (
	// capabilitiesBudget is the longest time that ListAccounts waits for the
	// answers.
	capabilitiesBudget = 5 * time.Second
	// maxCapabilityCalls is the largest number of questions at the same time.
	maxCapabilityCalls = 8
)

// ListAccounts returns the accounts of the tenant of the caller. It never
// returns an account of another tenant.
//
// It needs no permission check, because the view holds no mail content. For an
// account that the caller may read and that does not need to be reconnected, it
// also asks the backend what it supports. That can open a connection to the
// mail server, so the questions are limited: at most 8 at the same time, and the
// list waits for the answers at most 5 seconds, or until ctx ends. An account
// whose backend did not answer in time (or could not be set up) has empty
// capabilities, and the list still succeeds.
func (s *Service) ListAccounts(ctx context.Context) ([]AccountView, error) {
	p, err := PrincipalFrom(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.d.Store.ListAccounts(ctx, p.Tenant)
	if err != nil {
		return nil, fmt.Errorf("service: list accounts: %w", err)
	}

	// An empty list is an empty slice and not nil, so it encodes as [].
	views := make([]AccountView, len(accounts))
	var ask []int // the indexes of the accounts to ask
	for i, a := range accounts {
		views[i] = AccountView{
			ID:       a.ID,
			Address:  a.Address,
			Provider: string(a.Provider),
			Areas:    map[account.Area]string{account.AreaMail: a.Level(account.AreaMail).MailName()},
			Status:   a.Status,
		}
		if s.worthAsking(a, p) {
			ask = append(ask, i)
		}
	}
	s.fillCapabilities(ctx, accounts, views, ask)
	return views, nil
}

// fillCapabilities asks the backends of the accounts at the indexes in ask and
// puts the answers into views.
//
// It returns when every answer is in, or when the budget or ctx ends. Only this
// function writes to views. A question that is still running when it returns
// puts its answer in a buffered channel that nobody reads, so a backend that
// ignores its context cannot hold up the list or cause a data race.
func (s *Service) fillCapabilities(ctx context.Context, accounts []*account.Account, views []AccountView, ask []int) {
	if len(ask) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, s.capabilitiesBudget)
	defer cancel()

	type answer struct {
		index int
		caps  mail.Capabilities
	}
	answers := make(chan answer, len(ask)) // room for every answer, so no sender ever blocks
	running := make(chan struct{}, maxCapabilityCalls)
	for _, i := range ask {
		go func() {
			select {
			case running <- struct{}{}:
				defer func() { <-running }()
			case <-ctx.Done():
				return
			}
			answers <- answer{index: i, caps: s.capabilities(ctx, accounts[i])}
		}()
	}

	if s.beforeCollect != nil {
		s.beforeCollect(func() int { return len(answers) })
	}
	for received := 0; received < len(ask); {
		select {
		case a := <-answers:
			views[a.index].Capabilities = a.caps
			received++
		case <-ctx.Done():
			// Keep the answers that are ready, and stop waiting for the others.
			for {
				select {
				case a := <-answers:
					views[a.index].Capabilities = a.caps
				default:
					return
				}
			}
		}
	}
}

// worthAsking reports whether ListAccounts should ask the backend of a for its
// capabilities. It does not when the caller may not read the account or when the
// account must be reconnected, because the answer would be of no use and the
// question would open a connection.
func (s *Service) worthAsking(a *account.Account, p Principal) bool {
	return a.Status != account.StatusNeedsReauth && policy.Decide(a, policy.ActRead, p.Scopes).Allowed
}

// capabilities returns what the backend of a supports, or empty capabilities
// when the backend cannot be set up.
func (s *Service) capabilities(ctx context.Context, a *account.Account) mail.Capabilities {
	b, err := s.d.Backends.Mail(ctx, a)
	if err != nil {
		return mail.Capabilities{}
	}
	return b.Capabilities(ctx)
}

// ListFolders returns the folders of the account.
func (s *Service) ListFolders(ctx context.Context, id account.AccountID) ([]mail.Folder, error) {
	b, err := s.mailBackend(ctx, id, policy.ActRead)
	if err != nil {
		return nil, err
	}
	return b.ListFolders(ctx)
}

// ListTags returns the tags of the account. The bool says whether the account
// supports tags. When it does not, the list is empty and there is no error.
func (s *Service) ListTags(ctx context.Context, id account.AccountID) ([]mail.Tag, bool, error) {
	b, err := s.mailBackend(ctx, id, policy.ActRead)
	if err != nil {
		return nil, false, err
	}
	tags, err := b.ListTags(ctx)
	if errors.Is(err, mail.ErrUnsupported) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return tags, true, nil
}

// Search returns one page of the messages that match the query. The backend
// limits the number of results of a page.
func (s *Service) Search(ctx context.Context, id account.AccountID, q mail.Query, page mail.PageToken) (mail.Page[mail.Summary], error) {
	b, err := s.mailBackend(ctx, id, policy.ActRead)
	if err != nil {
		return mail.Page[mail.Summary]{}, err
	}
	return b.Search(ctx, q, page)
}

// NewMessages returns the messages of a folder that changed since the cursor,
// the cursor to use next, and whether the old cursor was reset. See
// mail.Backend.Changes.
func (s *Service) NewMessages(ctx context.Context, id account.AccountID, folder string, c mail.Cursor) ([]mail.Summary, mail.Cursor, bool, error) {
	b, err := s.mailBackend(ctx, id, policy.ActRead)
	if err != nil {
		return nil, "", false, err
	}
	return b.Changes(ctx, folder, c)
}

// GetMessage returns one message. The options control which body part is
// returned and how much of it.
func (s *Service) GetMessage(ctx context.Context, id account.AccountID, ref mail.Ref, opts mail.BodyOptions) (*mail.Message, error) {
	b, err := s.mailBackend(ctx, id, policy.ActRead)
	if err != nil {
		return nil, err
	}
	return b.GetMessage(ctx, ref, opts)
}

// GetAttachment returns one attachment of a message. It asks the backend for at
// most 10 MB. For a larger attachment it returns the error of the backend as it
// is (it matches mail.ErrTooLarge).
func (s *Service) GetAttachment(ctx context.Context, id account.AccountID, ref mail.Ref, attachmentID string) (*mail.Attachment, error) {
	b, err := s.mailBackend(ctx, id, policy.ActRead)
	if err != nil {
		return nil, err
	}
	return b.GetAttachment(ctx, ref, attachmentID, maxAttachmentBytes)
}
