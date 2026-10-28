package mail

import (
	"context"
	"errors"
)

// Errors that a Backend returns. Callers check them with errors.Is. A backend
// may wrap them with more detail.
var (
	// ErrNotFound means the message, folder or attachment does not exist.
	ErrNotFound = errors.New("mail: message not found")
	// ErrRefStale means a Ref is no longer valid, for example because the
	// message was moved or deleted outside this server. The caller should
	// search again to get fresh refs.
	ErrRefStale = errors.New("mail: message reference is stale")
	// ErrTooLarge means the data is larger than the limit the caller gave.
	ErrTooLarge = errors.New("mail: content is too large")
	// ErrUnsupported means the backend does not support the requested feature.
	ErrUnsupported = errors.New("mail: operation not supported")
	// ErrConflict means the change conflicts with the current state, for
	// example creating a folder or tag that already exists.
	ErrConflict = errors.New("mail: conflicting change")
	// ErrPartial means a call that changes mail failed after it had made some
	// or all of its change: for example some messages were moved, or the
	// messages were moved and their new refs are not known. The old refs may be
	// stale. The caller must not repeat the call, because a second try can make
	// copies or change other messages. It should search again to see the state.
	// A backend wraps the error with the cause, so errors.Is finds both.
	ErrPartial = errors.New("mail: the change was made in part or in full before the error")
)

// Backend is the interface that every mail provider implements. It uses no
// protocol-specific concepts, so an IMAP and SMTP backend, a Gmail backend and
// a Microsoft Graph backend can all implement it.
//
// Refs are opaque strings. A backend creates them. Callers pass them back
// unchanged and never parse them.
//
// Implementations must never permanently delete mail. Trash moves messages to
// the Trash folder and nothing more.
//
// Implementations hold no secrets in exported fields.
type Backend interface {
	// Capabilities reports what the backend supports. A backend may have to ask
	// the server, so the call takes a context: when the context ends, the call
	// ends and reports what it knows (for a server that did not answer, no
	// optional feature). It never returns an error.
	Capabilities(ctx context.Context) Capabilities

	// ListFolders returns the folders of the account.
	ListFolders(ctx context.Context) ([]Folder, error)
	// ListTags returns the tags of the account. A backend without tag support
	// returns ErrUnsupported.
	ListTags(ctx context.Context) ([]Tag, error)
	// CreateFolder creates a folder. The parent may be empty for a top-level
	// folder.
	CreateFolder(ctx context.Context, name, parent string) (Folder, error)
	// CreateTag creates a tag. A backend without tag support returns
	// ErrUnsupported.
	CreateTag(ctx context.Context, name string) (Tag, error)

	// Search returns one page of messages that match the query. The page token
	// comes from the previous page. An empty token asks for the first page.
	Search(ctx context.Context, query Query, page PageToken) (Page[Summary], error)
	// Changes returns the messages in a folder that changed since the cursor,
	// and the new cursor. The bool is true when the old cursor is no longer
	// valid (a reset). The returned cursor is the one to use next.
	Changes(ctx context.Context, folder string, since Cursor) ([]Summary, Cursor, bool, error)

	// GetMessage returns one message. The options control which body part is
	// returned and how much of it.
	GetMessage(ctx context.Context, ref Ref, opts BodyOptions) (*Message, error)
	// GetRaw returns the whole message in RFC 5322 format. It returns
	// ErrTooLarge when the message is larger than maxBytes.
	GetRaw(ctx context.Context, ref Ref, maxBytes int64) ([]byte, error)
	// GetAttachment returns one attachment of a message. It returns
	// ErrTooLarge when the attachment is larger than maxBytes.
	GetAttachment(ctx context.Context, ref Ref, attachmentID string, maxBytes int64) (*Attachment, error)

	// SetFlags changes the seen and flagged state of the messages.
	SetFlags(ctx context.Context, refs []Ref, change FlagChange) error
	// Tag adds and removes tags on the messages.
	Tag(ctx context.Context, refs []Ref, add, remove []string) error
	// Move moves the messages to a folder and returns their new refs, in the
	// same order as refs.
	Move(ctx context.Context, refs []Ref, folder string) ([]Ref, error)
	// Trash moves the messages to the Trash folder. It never deletes them
	// permanently.
	Trash(ctx context.Context, refs []Ref) error

	// SaveDraft saves a message in the Drafts folder and returns its ref.
	SaveDraft(ctx context.Context, msg *RawMessage) (Ref, error)
	// DiscardDraft moves a draft to Trash. Like every other method, it must not
	// delete mail permanently. It must refuse a message that is not a draft
	// (Summary.Draft is false) with ErrUnsupported, before it changes anything:
	// a caller that may only discard drafts must not be able to move other mail
	// to Trash with it.
	DiscardDraft(ctx context.Context, ref Ref) error
	// Send sends a message.
	Send(ctx context.Context, msg *RawMessage) error

	// Close releases the resources of the backend, such as open connections.
	Close() error
}
