package mcpserver

// This file holds the write tools. Each one names the level it needs. The
// service checks the level, asks for a confirmation when the account wants one,
// and writes the audit entry. This layer reads the arguments, turns the answer
// of an elicitation into a service.ConfirmAnswer (elicit.go), and shapes the
// result.
//
// No result echoes the content of a message. A name that the mail server gives
// back (a folder or a tag) is cleaned like all other text from a third party.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// The names of the write tools. The service records the same names in the
// audit log.
const (
	toolSetFlags          = "set_flags"
	toolCreateFolder      = "create_folder"
	toolCreateTag         = "create_tag"
	toolTagMessages       = "tag_messages"
	toolMoveMessages      = "move_messages"
	toolCreateDraft       = "create_draft"
	toolDiscardDraft      = "discard_draft"
	toolSendMessage       = "send_message"
	toolCancelSend        = "cancel_send"
	toolTrashMessages     = "trash_messages"
	toolRequestPermission = "request_permission_change"
	toolConnectAccount    = "connect_account"
)

// errAttachmentsTooLarge is the failure for attachments that are larger than
// service.MaxAttachmentBytes together, after decoding.
var errAttachmentsTooLarge = failure(codeTooLarge, fmt.Sprintf(
	"The attachments are larger than %d MB together after base64 decoding. Use smaller or fewer attachments. %s",
	service.MaxAttachmentBytes>>20, noRepeat))

// ---------------------------------------------------------------- inputs

// AttachmentIn is one file to attach. The data comes as base64. A file path is
// never accepted: a path would let an agent that follows hidden instructions
// mail out any local file.
type AttachmentIn struct {
	Filename    string `json:"filename" jsonschema:"The file name that the recipients see."`
	ContentType string `json:"content_type,omitempty" jsonschema:"Media type, for example application/pdf. Found from the data when left out."`
	DataBase64  string `json:"data_base64" jsonschema:"The file data in standard base64 (RFC 4648). Not a file path."`
}

// ReplyIn names the message that a reply answers.
type ReplyIn struct {
	Ref string `json:"ref" jsonschema:"Ref of the message to answer. Pass it back unchanged."`
	All bool   `json:"all,omitempty" jsonschema:"true to reply to all: every original recipient except the user."`
}

// ComposeIn is a message to write. It mirrors mail.Compose. create_draft and
// send_message share it.
type ComposeIn struct {
	To                         []string       `json:"to,omitempty" jsonschema:"Recipient addresses."`
	Cc                         []string       `json:"cc,omitempty" jsonschema:"Copy addresses."`
	Bcc                        []string       `json:"bcc,omitempty" jsonschema:"Blind copy addresses. The other recipients do not see them."`
	Subject                    string         `json:"subject,omitempty"`
	Body                       string         `json:"body,omitempty" jsonschema:"The plain text body."`
	HTMLBody                   string         `json:"html_body,omitempty" jsonschema:"An HTML body, as well as the plain text body."`
	ReplyTo                    *ReplyIn       `json:"reply_to,omitempty" jsonschema:"Make the message a reply to this message."`
	ForwardOf                  string         `json:"forward_of,omitempty" jsonschema:"Ref of a message to forward."`
	IncludeOriginalAttachments bool           `json:"include_original_attachments,omitempty" jsonschema:"With forward_of: send the attachments of the original too."`
	Attachments                []AttachmentIn `json:"attachments,omitempty" jsonschema:"Files to attach, as base64 data. At most 10 MB together. File paths are not accepted."`
}

type setFlagsInput struct {
	Account string   `json:"account" jsonschema:"Account id from list_accounts."`
	Refs    []string `json:"refs" jsonschema:"Message refs from a search. Pass them back unchanged."`
	Seen    *bool    `json:"seen,omitempty" jsonschema:"true to mark as read, false to mark as unread."`
	Flagged *bool    `json:"flagged,omitempty" jsonschema:"true to flag, false to remove the flag."`
}

type createFolderInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	Name    string `json:"name" jsonschema:"Name of the new folder."`
	Parent  string `json:"parent,omitempty" jsonschema:"Folder name or alias to create it in. Leave it out for a top-level folder."`
}

type createTagInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	Name    string `json:"name" jsonschema:"Name of the new tag."`
}

type tagInput struct {
	Account string   `json:"account" jsonschema:"Account id from list_accounts."`
	Refs    []string `json:"refs" jsonschema:"Message refs from a search. Pass them back unchanged."`
	Add     []string `json:"add,omitempty" jsonschema:"Tags to add."`
	Remove  []string `json:"remove,omitempty" jsonschema:"Tags to remove."`
}

type moveInput struct {
	Account string   `json:"account" jsonschema:"Account id from list_accounts."`
	Refs    []string `json:"refs" jsonschema:"Message refs from a search. Pass them back unchanged."`
	Folder  string   `json:"folder" jsonschema:"Folder name or alias. @trash is refused: use trash_messages."`
}

type draftInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	ComposeIn
}

type discardInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	Ref     string `json:"ref" jsonschema:"Ref of the draft. Pass it back unchanged."`
}

type sendInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	ComposeIn
	DraftRef string `json:"draft_ref,omitempty" jsonschema:"Ref of a saved draft to send as it is. Do not give message fields with it."`
}

type cancelInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	SendID  string `json:"send_id" jsonschema:"The send_id that send_message returned."`
}

type trashInput struct {
	Account string   `json:"account" jsonschema:"Account id from list_accounts."`
	Refs    []string `json:"refs" jsonschema:"Message refs from a search. Pass them back unchanged."`
}

type permissionInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	Area    string `json:"area" jsonschema:"The area of the account: mail."`
	Level   int    `json:"level" jsonschema:"The level to ask for: 0 Off, 1 Read, 2 Organize, 3 Draft, 4 Send, 5 Delete."`
}

// ---------------------------------------------------------------- outputs

// CountOut is the result of a change to a batch of messages.
type CountOut struct {
	Count int `json:"count" jsonschema:"The number of message refs in the request."`
}

// TagOut is a tag.
type TagOut struct {
	Name string `json:"name"`
}

// MoveOut is the result of move_messages.
type MoveOut struct {
	Refs []string `json:"refs" jsonschema:"The new refs, in the order of the request. The old refs no longer work."`
}

// DraftOut is the result of create_draft.
type DraftOut struct {
	Ref string `json:"ref" jsonschema:"Ref of the saved draft. Pass it to send_message as draft_ref, or to discard_draft."`
}

// DiscardOut is the result of discard_draft.
type DiscardOut struct {
	Discarded bool `json:"discarded" jsonschema:"True: the draft is in Trash."`
}

// CancelOut is the result of cancel_send.
type CancelOut struct {
	Status string `json:"status" jsonschema:"cancelled"`
	SendID string `json:"send_id"`
}

// SendResultOut is the result of send_message.
type SendResultOut struct {
	Status       string `json:"status" jsonschema:"sent, queued, pending_approval or declined."`
	SendID       string `json:"send_id" jsonschema:"Pass it to get_send_status or cancel_send."`
	SendAt       string `json:"send_at,omitempty" jsonschema:"When a queued send leaves the outbox, RFC 3339. Until then it can be cancelled."`
	ApprovalLink string `json:"approval_link,omitempty" jsonschema:"For pending_approval: the page where the user approves or rejects the send. Give it to the user."`
}

// TrashResultOut is the result of trash_messages.
type TrashResultOut struct {
	Status       string `json:"status" jsonschema:"done, pending_approval or declined."`
	Count        int    `json:"count" jsonschema:"The number of messages in the request. A ref given twice counts once."`
	ApprovalLink string `json:"approval_link,omitempty" jsonschema:"For pending_approval: the page where the user approves or rejects the move. Give it to the user."`
}

// LinkOut is the result of request_permission_change and connect_account.
type LinkOut struct {
	Changed bool   `json:"changed" jsonschema:"Always false: this tool changes nothing."`
	Link    string `json:"link" jsonschema:"The page for the user. Give it to the user."`
	Message string `json:"message"`
}

// ---------------------------------------------------------------- registration

// writeHints returns the annotations of a write tool. Every hint is set: MCP
// reads a missing destructiveHint and a missing openWorldHint as true.
func writeHints(destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
}

// outputSchema returns the JSON schema of T. It is for a tool whose handler
// returns any, because its first call may return input requests and no output.
// It panics on an error: the types are fixed in this file, so an error is a
// mistake in the code.
func outputSchema[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic("mcpserver: output schema: " + err.Error())
	}
	return s
}

// registerWriteTools adds the write tools to the server, with addTool.
// ApprovePending and RejectPending of the service are not tools: only the user
// approves, on the web page.
func registerWriteTools(srv *mcp.Server, d *deps) {
	organize := writeHints(false, true, false)
	addTool(srv, d, &mcp.Tool{
		Name:        toolSetFlags,
		Description: "Mark messages as read or unread, and as flagged or not flagged. Give seen, flagged or both. Needs Mail 2 (organize).",
		Annotations: organize,
	}, d.setFlags)
	addTool(srv, d, &mcp.Tool{
		Name:        toolCreateFolder,
		Description: "Create a folder. There is no tool to delete a folder. Needs Mail 2 (organize).",
		Annotations: organize,
	}, d.createFolder)
	addTool(srv, d, &mcp.Tool{
		Name:        toolCreateTag,
		Description: "Create a tag. list_tags says whether the account can store tags. Needs Mail 2 (organize).",
		Annotations: organize,
	}, d.createTag)
	addTool(srv, d, &mcp.Tool{
		Name:        toolTagMessages,
		Description: "Add tags to messages and remove tags from them. Needs Mail 2 (organize).",
		Annotations: organize,
	}, d.tagMessages)
	addTool(srv, d, &mcp.Tool{
		Name: toolMoveMessages,
		Description: "Move messages to a folder. Returns the new refs; the old refs no longer work. " +
			"A move to Trash is refused: use trash_messages. Needs Mail 2 (organize).",
		Annotations: organize,
	}, d.moveMessages)
	addTool(srv, d, &mcp.Tool{
		Name: toolCreateDraft,
		Description: "Save a new draft: a new message, a reply (reply_to) or a forward (forward_of). " +
			"Attachments are base64 data, never file paths, at most 10 MB together. Returns the ref of the draft. Needs Mail 3 (draft).",
		Annotations: writeHints(false, false, false),
	}, d.createDraft)
	addTool(srv, d, &mcp.Tool{
		Name:        toolDiscardDraft,
		Description: "Move a draft to Trash. Only a draft. Needs Mail 3 (draft).",
		Annotations: writeHints(false, false, false),
	}, d.discardDraft)
	addTool(srv, d, &mcp.Tool{
		Name: toolSendMessage,
		Description: "Send a message: the message fields (as in create_draft), or the draft_ref of a saved draft, not both. " +
			"When the account wants a confirmation, your client asks the user, or the status is pending_approval with an " +
			"approval_link: give the link to the user and check get_send_status later. Never answer a confirmation for the user. " +
			"A queued send can be cancelled with cancel_send until send_at. Needs Mail 4 (send).",
		Annotations:  writeHints(false, false, true),
		OutputSchema: outputSchema[SendResultOut](),
	}, d.sendMessage)
	addTool(srv, d, &mcp.Tool{
		Name:        toolCancelSend,
		Description: "Cancel a send: a queued send before its send_at, or a send that waits for approval. Needs Mail 4 (send).",
		Annotations: writeHints(false, false, false),
	}, d.cancelSend)
	addTool(srv, d, &mcp.Tool{
		Name: toolTrashMessages,
		Description: "Move messages to Trash. Mail is never deleted for good. When the account wants a confirmation, " +
			"your client asks the user, or the status is pending_approval with an approval_link: give the link to the user. " +
			"Never answer a confirmation for the user. Needs Mail 5 (delete).",
		Annotations:  writeHints(true, false, false),
		OutputSchema: outputSchema[TrashResultOut](),
	}, d.trashMessages)
	addTool(srv, d, &mcp.Tool{
		Name: toolRequestPermission,
		Description: "Get a link where the user can change the access level of an account. It changes nothing: only the user " +
			"can change a level, on that page. Needs no level.",
		Annotations: writeHints(false, false, false),
	}, d.requestPermissionChange)
	addTool(srv, d, &mcp.Tool{
		Name:        toolConnectAccount,
		Description: "Get a link where the user can connect a new email account. It changes nothing. Needs no level.",
		Annotations: writeHints(false, false, false),
	}, d.connectAccount)
}

// ---------------------------------------------------------------- organize

func (d *deps) setFlags(ctx context.Context, _ *mcp.CallToolRequest, in setFlagsInput) (*mcp.CallToolResult, CountOut, error) {
	refs, err := refsOf(in.Refs)
	if err != nil {
		return nil, CountOut{}, err
	}
	if in.Seen == nil && in.Flagged == nil {
		return nil, CountOut{}, &argError{arg: "seen", hint: "Give seen, flagged or both."}
	}
	err = d.svc.SetFlags(ctx, account.AccountID(in.Account), refs, mail.FlagChange{Seen: in.Seen, Flagged: in.Flagged})
	if err != nil {
		return nil, CountOut{}, err
	}
	return success(CountOut{Count: len(refs)})
}

func (d *deps) createFolder(ctx context.Context, _ *mcp.CallToolRequest, in createFolderInput) (*mcp.CallToolResult, FolderOut, error) {
	f, err := d.svc.CreateFolder(ctx, account.AccountID(in.Account), in.Name, in.Parent)
	if err != nil {
		return nil, FolderOut{}, err
	}
	return success(FolderOut{Name: clean(f.Name), Alias: f.Alias, Unread: f.Unread, Total: f.Total, Trash: f.Trash})
}

func (d *deps) createTag(ctx context.Context, _ *mcp.CallToolRequest, in createTagInput) (*mcp.CallToolResult, TagOut, error) {
	tag, err := d.svc.CreateTag(ctx, account.AccountID(in.Account), in.Name)
	if err != nil {
		return nil, TagOut{}, err
	}
	return success(TagOut{Name: clean(tag.Name)})
}

func (d *deps) tagMessages(ctx context.Context, _ *mcp.CallToolRequest, in tagInput) (*mcp.CallToolResult, CountOut, error) {
	refs, err := refsOf(in.Refs)
	if err != nil {
		return nil, CountOut{}, err
	}
	if len(in.Add) == 0 && len(in.Remove) == 0 {
		return nil, CountOut{}, &argError{arg: "add", hint: "Give tags to add, tags to remove, or both."}
	}
	if err := d.svc.Tag(ctx, account.AccountID(in.Account), refs, in.Add, in.Remove); err != nil {
		return nil, CountOut{}, err
	}
	return success(CountOut{Count: len(refs)})
}

func (d *deps) moveMessages(ctx context.Context, _ *mcp.CallToolRequest, in moveInput) (*mcp.CallToolResult, MoveOut, error) {
	refs, err := refsOf(in.Refs)
	if err != nil {
		return nil, MoveOut{}, err
	}
	moved, err := d.svc.Move(ctx, account.AccountID(in.Account), refs, in.Folder)
	if err != nil {
		return nil, MoveOut{}, err
	}
	out := MoveOut{Refs: make([]string, len(moved))}
	for i, r := range moved {
		out.Refs[i] = string(r)
	}
	return success(out)
}

// refsOf turns the refs of a batch tool into mail refs. A batch needs at least
// one ref.
func refsOf(in []string) ([]mail.Ref, error) {
	if len(in) == 0 {
		return nil, &argError{arg: "refs", hint: "Give at least one message ref from a search."}
	}
	out := make([]mail.Ref, len(in))
	for i, r := range in {
		out[i] = mail.Ref(r)
	}
	return out, nil
}

// ---------------------------------------------------------------- drafts

func (d *deps) createDraft(ctx context.Context, _ *mcp.CallToolRequest, in draftInput) (*mcp.CallToolResult, DraftOut, error) {
	c, err := in.toCompose()
	if err != nil {
		return nil, DraftOut{}, err
	}
	ref, err := d.svc.CreateDraft(ctx, account.AccountID(in.Account), c)
	if err != nil {
		return nil, DraftOut{}, err
	}
	return success(DraftOut{Ref: string(ref)})
}

func (d *deps) discardDraft(ctx context.Context, _ *mcp.CallToolRequest, in discardInput) (*mcp.CallToolResult, DiscardOut, error) {
	if err := d.svc.DiscardDraft(ctx, account.AccountID(in.Account), mail.Ref(in.Ref)); err != nil {
		return nil, DiscardOut{}, err
	}
	return success(DiscardOut{Discarded: true})
}

// isEmpty reports whether no field of the message is set.
func (in ComposeIn) isEmpty() bool {
	return len(in.To) == 0 && len(in.Cc) == 0 && len(in.Bcc) == 0 && in.Subject == "" && in.Body == "" &&
		in.HTMLBody == "" && in.ReplyTo == nil && in.ForwardOf == "" && !in.IncludeOriginalAttachments &&
		len(in.Attachments) == 0
}

// toCompose turns the input into a mail.Compose. It decodes the attachments
// (see decodeAttachments). A reply needs the ref of the original.
func (in ComposeIn) toCompose() (mail.Compose, error) {
	c := mail.Compose{
		To:                         in.To,
		Cc:                         in.Cc,
		Bcc:                        in.Bcc,
		Subject:                    in.Subject,
		Body:                       in.Body,
		HTMLBody:                   in.HTMLBody,
		IncludeOriginalAttachments: in.IncludeOriginalAttachments,
	}
	if in.ReplyTo != nil {
		if in.ReplyTo.Ref == "" {
			return mail.Compose{}, &argError{arg: "reply_to.ref", hint: "Give the ref of the message to answer."}
		}
		c.ReplyTo = &mail.ReplyRef{Ref: mail.Ref(in.ReplyTo.Ref), All: in.ReplyTo.All}
	}
	if in.ForwardOf != "" {
		ref := mail.Ref(in.ForwardOf)
		c.ForwardOf = &ref
	}
	atts, err := decodeAttachments(in.Attachments)
	if err != nil {
		return mail.Compose{}, err
	}
	c.Attachments = atts
	return c, nil
}

// decodeAttachments decodes the base64 data of the attachments with the standard
// encoding. The decoded data may be at most service.MaxAttachmentBytes together.
// Data that is not valid base64 gives an argument error that names the index of
// the attachment.
//
// An attachment that would surely go over the limit is refused before it is
// decoded: base64 data of n characters (line breaks not counted) decodes to at
// least n/4*3 - 2 bytes.
func decodeAttachments(in []AttachmentIn) ([]mail.OutgoingAttachment, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]mail.OutgoingAttachment, 0, len(in))
	total := 0
	for i, a := range in {
		n := len(a.DataBase64) - strings.Count(a.DataBase64, "\n") - strings.Count(a.DataBase64, "\r")
		if total+n/4*3-2 > service.MaxAttachmentBytes {
			return nil, errAttachmentsTooLarge
		}
		data, err := base64.StdEncoding.DecodeString(a.DataBase64)
		if err != nil {
			return nil, &argError{
				arg:  fmt.Sprintf("attachments[%d].data_base64", i),
				hint: "Use standard base64 (RFC 4648, with + and / and = padding).",
			}
		}
		total += len(data)
		if total > service.MaxAttachmentBytes {
			return nil, errAttachmentsTooLarge
		}
		out = append(out, mail.OutgoingAttachment{Filename: a.Filename, ContentType: a.ContentType, Data: data})
	}
	return out, nil
}

// ---------------------------------------------------------------- send

// sendMessage sends a message or a saved draft. The first call may return the
// confirmation form (elicit.go). The second call carries the answer.
func (d *deps) sendMessage(ctx context.Context, req *mcp.CallToolRequest, in sendInput) (*mcp.CallToolResult, any, error) {
	ans, err := confirmAnswer(req)
	if err != nil {
		return nil, nil, err
	}
	var (
		c     *mail.Compose
		draft *mail.Ref
	)
	switch {
	case in.DraftRef != "" && !in.isEmpty():
		return nil, nil, &argError{arg: "draft_ref", hint: "Give the message fields or a draft_ref, not both."}
	case in.DraftRef != "":
		ref := mail.Ref(in.DraftRef)
		draft = &ref
	case in.isEmpty():
		return nil, nil, &argError{arg: "draft_ref", hint: "Give the message fields (at least one recipient) or a draft_ref."}
	default:
		built, err := in.toCompose()
		if err != nil {
			return nil, nil, err
		}
		c = &built
	}

	res, nc, err := d.svc.Send(ctx, account.AccountID(in.Account), c, draft, d.modeFor(req, ans), ans)
	if err != nil {
		return nil, nil, err
	}
	if nc != nil {
		if nc.Pending == nil {
			return nil, nil, errors.New("mcpserver: the confirmation has no pending action")
		}
		return confirmationRequest(nc, sendPreview(nc.Pending.Preview)), nil, nil
	}
	if res == nil {
		return nil, nil, errors.New("mcpserver: the send result is empty")
	}
	out := SendResultOut{Status: res.Status, SendID: res.SendID, ApprovalLink: res.ApprovalLink}
	if res.SendAt != nil {
		out.SendAt = formatTime(*res.SendAt)
	}
	return success[any](out)
}

// modeFor returns the confirmation mode of a call. A second call carries the
// answer, so its mode is in band.
func (d *deps) modeFor(req *mcp.CallToolRequest, ans *service.ConfirmAnswer) service.ConfirmMode {
	if ans != nil {
		return service.ConfirmInBand
	}
	return confirmMode(req, d.opts)
}

func (d *deps) cancelSend(ctx context.Context, _ *mcp.CallToolRequest, in cancelInput) (*mcp.CallToolResult, CancelOut, error) {
	if err := d.svc.CancelSend(ctx, account.AccountID(in.Account), in.SendID); err != nil {
		return nil, CancelOut{}, err
	}
	return success(CancelOut{Status: service.SendCancelled, SendID: in.SendID})
}

// ---------------------------------------------------------------- Trash

// trashMessages moves messages to Trash, with the same confirmation as
// sendMessage.
func (d *deps) trashMessages(ctx context.Context, req *mcp.CallToolRequest, in trashInput) (*mcp.CallToolResult, any, error) {
	ans, err := confirmAnswer(req)
	if err != nil {
		return nil, nil, err
	}
	refs, err := refsOf(in.Refs)
	if err != nil {
		return nil, nil, err
	}
	res, nc, err := d.svc.Trash(ctx, account.AccountID(in.Account), refs, d.modeFor(req, ans), ans)
	if err != nil {
		return nil, nil, err
	}
	if nc != nil {
		if nc.Pending == nil {
			return nil, nil, errors.New("mcpserver: the confirmation has no pending action")
		}
		return confirmationRequest(nc, trashPreview(nc.Pending.Preview)), nil, nil
	}
	if res == nil {
		return nil, nil, errors.New("mcpserver: the trash result is empty")
	}
	return success[any](TrashResultOut{Status: res.Status, Count: res.Count, ApprovalLink: res.ApprovalLink})
}

// ---------------------------------------------------------------- links

func (d *deps) requestPermissionChange(ctx context.Context, req *mcp.CallToolRequest, in permissionInput) (*mcp.CallToolResult, LinkOut, error) {
	area := account.Area(in.Area)
	if area != account.AreaMail {
		return nil, LinkOut{}, &argError{arg: "area", hint: "Use mail."}
	}
	level := account.Level(in.Level)
	if level < account.MailOff || level > account.MailDelete {
		return nil, LinkOut{}, &argError{arg: "level", hint: "Use a number from 0 (Off) to 5 (Delete)."}
	}
	link, err := d.svc.PermissionLink(ctx, account.AccountID(in.Account), area, level)
	if err != nil {
		return nil, LinkOut{}, err
	}
	prompt := fmt.Sprintf("An agent asks for mail access at level %d (%s) for an account. "+
		"Open the settings page to see the risk and decide. Nothing changes unless you change it there.", level, level.MailName())
	return d.linkResult(req, prompt, LinkOut{
		Link: link,
		Message: "Nothing was changed. Only the user can change the level, on the settings page. " +
			"Give the user this link: " + link + ". " + noRetryUntilUser,
	})
}

func (d *deps) connectAccount(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, LinkOut, error) {
	link, err := d.svc.ConnectLink(ctx)
	if err != nil {
		return nil, LinkOut{}, err
	}
	return d.linkResult(req, "Open this page to connect an email account. Nothing changes unless you do it there.", LinkOut{
		Link:    link,
		Message: "Nothing was changed. The user connects an account on this page: " + link + ". " + noRetryUntilUser,
	})
}

// linkResult returns the link for the user. A client that can open a link for
// the person gets it as a URL elicitation first. The second call, whatever the
// answer, and every call of a client that cannot, get the text with the link.
func (d *deps) linkResult(req *mcp.CallToolRequest, prompt string, out LinkOut) (*mcp.CallToolResult, LinkOut, error) {
	if len(req.Params.InputResponses) == 0 && canOpenLink(req, d.opts) {
		return linkRequest(prompt, out.Link), LinkOut{}, nil
	}
	return success(out)
}
