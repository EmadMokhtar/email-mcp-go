// Package mail defines the provider-neutral mail types and the Backend
// interface.
//
// The IMAP and SMTP backend implements Backend. Later backends (for example
// Gmail and Microsoft Graph) will implement the same interface. The service
// layer calls only this interface, so nothing in this package may depend on one
// mail protocol or one provider.
//
// The types here are plain data. They have no network, storage or encryption
// code, and they have no JSON tags. The MCP layer defines its own output types.
package mail

import "time"

// Ref is an opaque reference to one message. A backend creates it and gives it
// to the caller. The caller must pass it back unchanged and must not parse it.
type Ref string

// Cursor is an opaque position in a folder, used to ask a backend for changes
// since that position. The caller must pass it back unchanged.
type Cursor string

// PageToken is an opaque token for the next page of search results. The
// caller must pass it back unchanged. An empty token means the first page.
type PageToken string

// The folder aliases. An alias names a well-known folder without depending on
// the folder name that one provider uses.
const (
	AliasInbox   = "@inbox"
	AliasArchive = "@archive"
	AliasSent    = "@sent"
	AliasDrafts  = "@drafts"
	AliasTrash   = "@trash"
	AliasJunk    = "@junk"
)

// IsAlias reports whether s is one of the six folder aliases. The check is
// exact: it is case-sensitive and it does not accept a real folder name such
// as "INBOX".
func IsAlias(s string) bool {
	switch s {
	case AliasInbox, AliasArchive, AliasSent, AliasDrafts, AliasTrash, AliasJunk:
		return true
	}
	return false
}

// Address is one mail address with an optional display name.
type Address struct {
	Name  string
	Email string
}

// Folder is a mailbox folder.
type Folder struct {
	// Name is the folder name as the provider knows it.
	Name string
	// Alias is the folder alias (for example AliasInbox), or empty when the
	// folder has no alias.
	Alias string
	// Unread and Total are message counts. A nil value means the backend does
	// not know the count.
	Unread, Total *int
	// Trash is true for a folder that holds deleted mail (for IMAP, a folder
	// with the \Trash attribute). More than one folder can have it, while only
	// one has the alias AliasTrash. A move to any of them is a move to Trash.
	Trash bool
	// Delimiter is the hierarchy delimiter of the folder: the character between
	// the name of the folder and the name of a folder inside it, such as '/' or
	// '.'. It is 0 when the server has no folder hierarchy.
	Delimiter rune
}

// Tag is a label on messages. A backend that supports tags reports it in
// Capabilities.
type Tag struct {
	Name string
}

// Capabilities tells the caller what a backend supports.
type Capabilities struct {
	// Tags is true when the backend supports tags.
	Tags bool
	// MaxBatch is the largest number of refs the backend accepts in one batch
	// call.
	MaxBatch int
}

// Query describes a message search. A zero field means "do not filter on
// this".
type Query struct {
	// Folder is a folder name or a folder alias.
	Folder string
	// From, To, Subject and Text are matched against the message.
	From, To, Subject, Text string
	// Since and Before limit the message date to a range.
	Since, Before *time.Time
	// Unread and Flagged filter on the message state. A nil value means any.
	Unread, Flagged *bool
	// TagsAny matches messages that have at least one of these tags.
	TagsAny []string
	// TagsNone matches messages that have none of these tags.
	TagsNone []string
	// Untagged matches messages that have no tags.
	Untagged bool
	// Limit is the largest number of results to return.
	Limit int
}

// Summary is the short form of a message, as returned by a search.
type Summary struct {
	Ref     Ref
	From    Address
	To      []Address
	Subject string
	Date    time.Time
	// Snippet is a short plain-text excerpt of the body.
	Snippet string
	Tags    []string
	// Seen is true when the message has been read.
	Seen bool
	// Flagged is true when the message is starred or flagged.
	Flagged bool
	// Draft is true when the message is a draft (for IMAP, it has the \Draft
	// flag). Only a draft may be sent as it is saved.
	Draft bool
	// HasAttachments is true when the message has at least one attachment.
	HasAttachments bool
	// HasInvite is true when the message carries a calendar invite.
	HasInvite bool
	// Size is the message size in bytes.
	Size int64
}

// AttachmentInfo describes one attachment without its data.
type AttachmentInfo struct {
	// ID identifies the attachment inside one message. Callers pass it to
	// Backend.GetAttachment.
	ID          string
	Filename    string
	ContentType string
	// Size is the attachment size in bytes.
	Size int64
}

// Attachment is one attachment with its data.
type Attachment struct {
	AttachmentInfo
	Data []byte
}

// Message is a full message: the summary plus headers, body and attachment
// list.
type Message struct {
	Summary
	Cc, ReplyTo []Address
	// MessageID, InReplyTo and References are the message identifier headers.
	MessageID, InReplyTo string
	References           []string
	// Text and HTML are the body parts. Only the part that BodyOptions asked
	// for is filled.
	Text, HTML  string
	Attachments []AttachmentInfo
	// Truncated is true when the body was cut at BodyOptions.MaxChars.
	Truncated bool
	// NextOffset is the offset to use in BodyOptions to read the next part of
	// a truncated body.
	NextOffset int
}

// Page is one page of results. Next is empty when there are no more pages.
type Page[T any] struct {
	Items []T
	Next  PageToken
}

// BodyFormat selects which body part a caller wants.
type BodyFormat string

// The body formats.
const (
	// BodyText asks for the plain-text body.
	BodyText BodyFormat = "text"
	// BodyHTML asks for the HTML body.
	BodyHTML BodyFormat = "html"
	// BodyNone asks for no body, only the headers and attachment list.
	BodyNone BodyFormat = "none"
)

// BodyOptions controls how much of the body GetMessage returns.
type BodyOptions struct {
	Format BodyFormat
	// MaxChars is the largest number of characters to return.
	MaxChars int
	// Offset is the number of characters to skip from the start of the body.
	Offset int
}

// FlagChange changes message flags. A nil field leaves that flag as it is.
type FlagChange struct {
	Seen, Flagged *bool
}

// RawMessage is a complete message ready to send or to save as a draft.
type RawMessage struct {
	// From is the envelope sender address.
	From string
	// Recipients are the envelope recipient addresses. They include Cc and Bcc
	// recipients.
	Recipients []string
	// Data is the message in RFC 5322 format (the internet message format).
	Data []byte
}

// Compose describes a message that the caller wants to write. The service
// layer turns it into a RawMessage.
type Compose struct {
	// To, Cc and Bcc are recipient addresses.
	To, Cc, Bcc []string
	Subject     string
	// Body is the plain-text body. HTMLBody is the optional HTML body.
	Body, HTMLBody string
	// ReplyTo is set when the message is a reply.
	ReplyTo *ReplyRef
	// ForwardOf is set when the message is a forward of another message.
	ForwardOf *Ref
	// IncludeOriginalAttachments copies the attachments of the original
	// message into a forward.
	IncludeOriginalAttachments bool
	Attachments                []OutgoingAttachment
}

// ReplyRef names the message that a reply answers.
type ReplyRef struct {
	Ref Ref
	// All is true to reply to all recipients, not only to the sender.
	All bool
}

// OutgoingAttachment is an attachment to add to a new message. The caller
// provides the data directly. A file path is never accepted.
type OutgoingAttachment struct {
	Filename, ContentType string
	Data                  []byte
}
