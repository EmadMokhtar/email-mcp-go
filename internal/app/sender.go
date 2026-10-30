package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/errtype"
	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
)

// Fixed codes for the log of the sender.
const (
	codeDraftDiscardFailed  = "draft_discard_failed"
	codeDraftNotADraft      = "not_a_draft"
	codeDraftOther          = "other"
	codeNeedsReauthSet      = "needs_reauth_set"
	codeNeedsReauthNotSaved = "needs_reauth_write_failed"
)

// statusTimeout is the time that the write of the account status gets after a
// refused login. It runs even when the send has run out of time.
const statusTimeout = 30 * time.Second

// accountReader reads one account and sets its status. *store.Store implements
// it.
type accountReader interface {
	GetAccount(ctx context.Context, tenant account.TenantID, id account.AccountID) (*account.Account, error)
	SetAccountStatus(ctx context.Context, tenant account.TenantID, id account.AccountID, status account.Status) error
}

// newSender returns the Sender of the outbox. It loads the account, gets its
// mail backend from the factory, and sends. The Service is not involved: the
// outbox must exist before the Service does.
//
// After a send that the server accepted, it moves the draft that the message
// was made from (if any) to Trash. A failed discard is logged with a fixed code
// and changes nothing: the message was sent, and an error would make the outbox
// send it again.
//
// The outbox retries every error that is not permanent, so the sender marks as
// permanent each error after which a retry could send the message twice, or
// could never work. See sendError.
//
// When the mail server refuses the stored login (imapsmtp.ErrAuthFailed), the
// sender marks the account as needing a reconnect, as the service does for its
// own calls. The error stays one that the outbox retries: the user may fix the
// account before the next try.
func newSender(accounts accountReader, backends service.BackendFactory, log *slog.Logger) outbox.Sender {
	return func(ctx context.Context, tenant account.TenantID, acct account.AccountID, msg *mail.RawMessage, draft mail.Ref) error {
		a, err := accounts.GetAccount(ctx, tenant, acct)
		if errors.Is(err, store.ErrNotFound) {
			return outbox.Permanent(errors.New("the account no longer exists"))
		}
		if err != nil {
			return fmt.Errorf("read the account: %w", err)
		}
		// The service never calls the mail server for such an account, so the
		// outbox does not either. The user may reconnect the account before the
		// next try, so the error is not permanent.
		if a.Status == account.StatusNeedsReauth {
			return errors.New("the account needs to be reconnected in the email-mcp settings")
		}
		b, err := backends.Mail(ctx, a)
		if err != nil {
			return err
		}
		if err := b.Send(ctx, msg); err != nil {
			if errors.Is(err, imapsmtp.ErrAuthFailed) {
				markNeedsReauth(ctx, accounts, a, log)
			}
			return sendError(err)
		}
		if draft != "" {
			if err := b.DiscardDraft(ctx, draft); err != nil {
				log.Warn("outbox: the draft was not moved to Trash after the send",
					"code", codeDraftDiscardFailed, "reason", discardReason(err), "error_types", errtype.Chain(err))
			}
		}
		return nil
	}
}

// markNeedsReauth sets the status of the account to needs_reauth after the mail
// server refused its stored login. The service then refuses every call on the
// account until a test of the account works again. A failure is logged with a
// fixed code.
func markNeedsReauth(ctx context.Context, accounts accountReader, a *account.Account, log *slog.Logger) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
	defer cancel()
	if err := accounts.SetAccountStatus(wctx, a.TenantID, a.ID, account.StatusNeedsReauth); err != nil {
		log.Error("outbox: the account could not be marked for a reconnect", "code", codeNeedsReauthNotSaved,
			"error_types", errtype.Chain(err))
		return
	}
	log.Warn("outbox: the mail server refused the stored login, so the account needs to be reconnected",
		"code", codeNeedsReauthSet)
}

// sendError marks the errors of Send that the outbox must not retry:
//   - imapsmtp.ErrDeliveryUnknown: the data was sent and the server did not
//     confirm it, so the server may have the message. A retry could send it
//     twice.
//   - a context error (the time limit of the send, or a cancel). The error does
//     not say whether it came before or after the data was sent, so it is
//     treated as unknown too. (The provider wraps the case after the end of the
//     data in ErrDeliveryUnknown already. This keeps the rule even if it did
//     not.)
//   - a *imapsmtp.RecipientRejectedError with a 5xx code: the server refused a
//     recipient for good, and a retry gets the same answer.
//   - imapsmtp.ErrInvalidMessage: the provider refused the message before it
//     connected (an address that cannot go into an SMTP command). A retry gets
//     the same answer.
//
// Any other error means that the server did not take the message, and a retry
// is safe.
func sendError(err error) error {
	var rejected *imapsmtp.RecipientRejectedError
	switch {
	case errors.Is(err, imapsmtp.ErrDeliveryUnknown),
		errors.Is(err, imapsmtp.ErrInvalidMessage),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &rejected) && rejected.Code >= 500 && rejected.Code <= 599:
		return outbox.Permanent(err)
	default:
		return err
	}
}

// discardReason returns a fixed code for a failed discard. The text of the error
// can hold text from the mail server, so it is never logged.
func discardReason(err error) string {
	if errors.Is(err, mail.ErrUnsupported) {
		return codeDraftNotADraft
	}
	return codeDraftOther
}
