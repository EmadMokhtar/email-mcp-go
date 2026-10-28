package imapsmtp

import (
	"context"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
)

// maxBatch is the largest number of refs that one batch call accepts.
const maxBatch = 500

// Backend is the IMAP and SMTP implementation of mail.Backend.
//
// It keeps a small pool of IMAP connections. Every operation takes one
// connection for its whole run and selects the folder it needs, so the
// operations of many goroutines do not disturb each other.
//
// Reads open a folder read-only (EXAMINE) and fetch message data with
// BODY.PEEK. So reading never sets \Seen and never changes the folder.
//
// A Backend is safe for use by many goroutines.
type Backend struct {
	cfg    Config
	signer *signing.Signer
	pool   *pool
}

var _ mail.Backend = (*Backend)(nil)

// The panic messages of New.
const (
	panicNoScope  = "imapsmtp: New needs a non-empty cfg.TokenScope: without it, page tokens and cursors of one account would open for another account"
	panicNoSigner = "imapsmtp: New needs a signer: without it, the backend cannot make or check page tokens and cursors"
)

// New returns a Backend. It does not connect until the first call.
//
// The signer makes and checks the page tokens and cursors. Those are bound to
// cfg.TokenScope, so a token of one account does not work for another.
//
// New panics when cfg.TokenScope is empty or signer is nil. Both are mistakes
// in the code that sets the backend up, not errors of a user request. An empty
// scope would let a token of one account open for another account.
func New(cfg Config, signer *signing.Signer) *Backend {
	if cfg.TokenScope == "" {
		panic(panicNoScope)
	}
	if signer == nil {
		panic(panicNoSigner)
	}
	cfg.withDefaults()
	return &Backend{cfg: cfg, signer: signer, pool: newPool(cfg)}
}

// Close closes the connections of the backend. Calls that follow fail.
func (b *Backend) Close() error {
	b.pool.Close()
	return nil
}

// Capabilities reports what the backend supports.
func (b *Backend) Capabilities() mail.Capabilities {
	return mail.Capabilities{Tags: false, MaxBatch: maxBatch}
}

// The methods below belong to later tasks (messages, flags, folders and
// drafts, and sending). Until then they report that they are not supported.

// ListTags returns the tags of the account. IMAP keywords are not supported as
// tags yet.
func (b *Backend) ListTags(context.Context) ([]mail.Tag, error) {
	return nil, mail.ErrUnsupported
}

func (b *Backend) CreateFolder(context.Context, string, string) (mail.Folder, error) {
	return mail.Folder{}, mail.ErrUnsupported
}

func (b *Backend) CreateTag(context.Context, string) (mail.Tag, error) {
	return mail.Tag{}, mail.ErrUnsupported
}

func (b *Backend) GetMessage(context.Context, mail.Ref, mail.BodyOptions) (*mail.Message, error) {
	return nil, mail.ErrUnsupported
}

func (b *Backend) GetRaw(context.Context, mail.Ref, int64) ([]byte, error) {
	return nil, mail.ErrUnsupported
}

func (b *Backend) GetAttachment(context.Context, mail.Ref, string, int64) (*mail.Attachment, error) {
	return nil, mail.ErrUnsupported
}

func (b *Backend) SetFlags(context.Context, []mail.Ref, mail.FlagChange) error {
	return mail.ErrUnsupported
}

func (b *Backend) Tag(context.Context, []mail.Ref, []string, []string) error {
	return mail.ErrUnsupported
}

func (b *Backend) Move(context.Context, []mail.Ref, string) ([]mail.Ref, error) {
	return nil, mail.ErrUnsupported
}

func (b *Backend) Trash(context.Context, []mail.Ref) error {
	return mail.ErrUnsupported
}

func (b *Backend) SaveDraft(context.Context, *mail.RawMessage) (mail.Ref, error) {
	return "", mail.ErrUnsupported
}

func (b *Backend) DiscardDraft(context.Context, mail.Ref) error {
	return mail.ErrUnsupported
}

func (b *Backend) Send(context.Context, *mail.RawMessage) error {
	return mail.ErrUnsupported
}
