package imapsmtp

import (
	"context"
	"time"

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
// The calls that change messages (flags, tags, move and trash) first check
// every reference of the call against the server, and change nothing when one
// is stale or missing. They never delete a message for good: a message goes to
// another folder, and only the UID of a message that was copied by the
// fallback of Move is expunged.
//
// Drafts are stored with APPEND, and Send makes its own SMTP connection for
// every message (see Backend.Send). APPEND and SMTP are never repeated after a
// failure, because a second try could store or send a message twice.
//
// A Backend is safe for use by many goroutines.
type Backend struct {
	cfg    Config
	signer *signing.Signer
	pool   *pool
	// tags is what the backend knows about tag support (see tagSupport).
	tags tagSupport
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

// capabilitiesTimeout is the longest time that Capabilities may take, whatever
// the context of the caller allows.
const capabilitiesTimeout = 15 * time.Second

// Capabilities reports what the backend supports.
//
// Tags is true when the inbox lets clients make new keywords (the wildcard \*
// in its PERMANENTFLAGS). The backend notes this whenever it opens the inbox.
// When it does not know yet, Capabilities asks the server once and remembers
// the answer. If the server cannot be asked, it reports Tags false and does not
// remember that, so a later call asks again.
//
// The question ends when ctx ends, and after 15 seconds at the latest. Then
// Capabilities reports Tags false, as for a server that cannot be reached.
func (b *Backend) Capabilities(ctx context.Context) mail.Capabilities {
	ctx, cancel := context.WithTimeout(ctx, capabilitiesTimeout)
	defer cancel()
	supported, err := b.tagsSupported(ctx)
	return mail.Capabilities{Tags: err == nil && supported, MaxBatch: maxBatch}
}
