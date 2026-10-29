package mcpserver

// This file holds the read tools. Each one needs Mail level 1 (read), except
// list_accounts, which needs no level.

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

const (
	// maxSearchLimit is the largest page size of a search.
	maxSearchLimit = 100
	// defaultFolder is the folder of list_new_messages when the caller gives none.
	defaultFolder = mail.AliasInbox
)

// The inputs of the read tools. A field without omitempty is required.

type noInput struct{}

type accountInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
}

type searchInput struct {
	Account   string   `json:"account" jsonschema:"Account id from list_accounts."`
	Folder    string   `json:"folder,omitempty" jsonschema:"Folder name or alias (@inbox, @archive, @sent, @drafts, @trash, @junk). Default @inbox."`
	From      string   `json:"from,omitempty" jsonschema:"Sender contains this text."`
	To        string   `json:"to,omitempty" jsonschema:"A recipient contains this text."`
	Subject   string   `json:"subject,omitempty" jsonschema:"Subject contains this text."`
	Text      string   `json:"text,omitempty" jsonschema:"Message text contains this text."`
	Since     string   `json:"since,omitempty" jsonschema:"Only messages on or after this time. RFC 3339 with an offset."`
	Before    string   `json:"before,omitempty" jsonschema:"Only messages before this time. RFC 3339 with an offset."`
	Unread    *bool    `json:"unread,omitempty" jsonschema:"true for unread messages only, false for read messages only."`
	Flagged   *bool    `json:"flagged,omitempty" jsonschema:"true for flagged messages only, false for unflagged only."`
	TagsAny   []string `json:"tags_any,omitempty" jsonschema:"Messages that have at least one of these tags."`
	TagsNone  []string `json:"tags_none,omitempty" jsonschema:"Messages that have none of these tags."`
	Untagged  bool     `json:"untagged,omitempty" jsonschema:"Only messages that have no tags."`
	Limit     int      `json:"limit,omitempty" jsonschema:"Page size, 1 to 100. Default 50."`
	PageToken string   `json:"page_token,omitempty" jsonschema:"Token of the next page from the previous result. Pass it back unchanged."`
}

type newMessagesInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	Folder  string `json:"folder,omitempty" jsonschema:"Folder name or alias. Default @inbox."`
	Cursor  string `json:"cursor,omitempty" jsonschema:"Cursor from the previous result. Leave it out to start tracking: the first call returns no messages and a cursor."`
}

type getMessageInput struct {
	Account    string `json:"account" jsonschema:"Account id from list_accounts."`
	Ref        string `json:"ref" jsonschema:"Message ref from a search. Pass it back unchanged."`
	BodyFormat string `json:"body_format,omitempty" jsonschema:"text (default), html (made safe: no scripts, remote content or hidden parts) or none."`
	MaxChars   int    `json:"max_chars,omitempty" jsonschema:"The most characters of the body to return. Default 20000."`
	Offset     int    `json:"offset,omitempty" jsonschema:"Characters to skip from the start of the body. Use next_offset of a truncated result."`
}

type getAttachmentInput struct {
	Account      string `json:"account" jsonschema:"Account id from list_accounts."`
	Ref          string `json:"ref" jsonschema:"Message ref from a search. Pass it back unchanged."`
	AttachmentID string `json:"attachment_id" jsonschema:"Attachment id from get_message."`
}

type sendStatusInput struct {
	Account string `json:"account" jsonschema:"Account id from list_accounts."`
	SendID  string `json:"send_id" jsonschema:"The send_id that a send tool returned."`
}

// The outputs of the read tools that are not messages.

// CapabilitiesOut is what the mail server of an account supports.
type CapabilitiesOut struct {
	Tags     bool `json:"tags" jsonschema:"True when the account can store tags."`
	MaxBatch int  `json:"max_batch" jsonschema:"The most messages a batch tool accepts in one call."`
}

// AccountOut is one account of the user.
type AccountOut struct {
	ID           string            `json:"id"`
	Address      string            `json:"address"`
	Provider     string            `json:"provider"`
	Areas        map[string]string `json:"areas" jsonschema:"The level of each enabled area, for example mail: Send."`
	Capabilities CapabilitiesOut   `json:"capabilities"`
	Status       string            `json:"status" jsonschema:"ok, or needs_reauth when the user must reconnect the account."`
}

// AccountsOut is the result of list_accounts.
type AccountsOut struct {
	Accounts []AccountOut `json:"accounts"`
}

// FolderOut is one folder.
type FolderOut struct {
	Name   string `json:"name"`
	Alias  string `json:"alias,omitempty" jsonschema:"The folder alias, when the folder has one."`
	Unread *int   `json:"unread,omitempty"`
	Total  *int   `json:"total,omitempty"`
	Trash  bool   `json:"trash" jsonschema:"True for a folder that holds deleted mail."`
}

// FoldersOut is the result of list_folders.
type FoldersOut struct {
	Folders []FolderOut `json:"folders"`
}

// TagsOut is the result of list_tags.
type TagsOut struct {
	Supported bool     `json:"supported" jsonschema:"False when the account cannot store tags. Use folders then."`
	Tags      []string `json:"tags"`
}

// SearchOut is the result of search_messages.
type SearchOut struct {
	Messages      []SummaryOut `json:"messages"`
	NextPageToken string       `json:"next_page_token,omitempty" jsonschema:"Pass it as page_token to get the next page. Absent on the last page."`
}

// NewMessagesOut is the result of list_new_messages.
type NewMessagesOut struct {
	Messages []SummaryOut `json:"messages"`
	Cursor   string       `json:"cursor" jsonschema:"Pass it back unchanged to get the messages that come after these."`
	Reset    bool         `json:"reset" jsonschema:"True when the old cursor no longer fitted the folder. Tracking starts again, and no messages are returned."`
}

// AttachmentText is the text of an attachment that a sender controls.
type AttachmentText struct {
	Filename string `json:"filename"`
}

// AttachmentOut describes the attachment that get_attachment returns.
type AttachmentOut struct {
	ID          string                    `json:"id"`
	ContentType string                    `json:"content_type"`
	Size        int64                     `json:"size" jsonschema:"Size in bytes."`
	Untrusted   Untrusted[AttachmentText] `json:"untrusted"`
}

// SendStatusOut is the result of get_send_status.
type SendStatusOut struct {
	Status       string `json:"status" jsonschema:"queued, sending, sent, failed, cancelled, pending_approval, declined or expired."`
	SendID       string `json:"send_id"`
	SendAt       string `json:"send_at,omitempty" jsonschema:"When a queued send leaves the outbox, RFC 3339. Until then it can be cancelled."`
	ApprovalLink string `json:"approval_link,omitempty" jsonschema:"Where the user approves a send that waits."`
}

// readOnly returns the annotations of a tool that changes nothing and talks only
// to the mail account of the user. MCP reads a missing openWorldHint and a
// missing destructiveHint as true, so both are set to false.
func readOnly() *mcp.ToolAnnotations {
	closedWorld, destructive := false, false
	return &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive, OpenWorldHint: &closedWorld}
}

// registerReadTools adds the read tools to the server, with addTool. The SDK
// lists tools in the order of their names, so the order here is only for the
// reader.
func registerReadTools(srv *mcp.Server, d *deps) {
	addTool(srv, d, &mcp.Tool{
		Name:        "list_accounts",
		Description: "List the email accounts of the user: id, address, access level, capabilities and status. Call this first. Needs no level.",
		Annotations: readOnly(),
	}, d.listAccounts)
	addTool(srv, d, &mcp.Tool{
		Name:        "list_folders",
		Description: "List the folders of an account with their aliases and counts. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.listFolders)
	addTool(srv, d, &mcp.Tool{
		Name:        "list_tags",
		Description: "List the tags of an account. supported is false when the account cannot store tags; use folders then. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.listTags)
	addTool(srv, d, &mcp.Tool{
		Name:        "search_messages",
		Description: "Search the messages of one folder. Returns summaries (at most 100 per page) and a page token. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.searchMessages)
	addTool(srv, d, &mcp.Tool{
		Name:        "list_new_messages",
		Description: "List the messages that arrived in a folder since a cursor, and a new cursor. Pass the cursor back unchanged. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.listNewMessages)
	addTool(srv, d, &mcp.Tool{
		Name:        "get_message",
		Description: "Get one message: headers, body and attachment list. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.getMessage)
	addTool(srv, d, &mcp.Tool{
		Name:        "get_attachment",
		Description: "Get one attachment of a message as an embedded resource. At most 10 MB. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.getAttachment)
	addTool(srv, d, &mcp.Tool{
		Name:        "get_send_status",
		Description: "Get the status of a send: queued, sent, failed, waiting for approval and so on. Needs Mail 1 (read).",
		Annotations: readOnly(),
	}, d.getSendStatus)
}

func (d *deps) listAccounts(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, AccountsOut, error) {
	views, err := d.svc.ListAccounts(ctx)
	if err != nil {
		return nil, AccountsOut{}, err
	}
	out := AccountsOut{Accounts: make([]AccountOut, len(views))}
	for i, v := range views {
		areas := make(map[string]string, len(v.Areas))
		for area, level := range v.Areas {
			areas[string(area)] = level
		}
		out.Accounts[i] = AccountOut{
			ID:           string(v.ID),
			Address:      v.Address,
			Provider:     v.Provider,
			Areas:        areas,
			Capabilities: CapabilitiesOut{Tags: v.Capabilities.Tags, MaxBatch: v.Capabilities.MaxBatch},
			Status:       string(v.Status),
		}
	}
	return success(out)
}

func (d *deps) listFolders(ctx context.Context, _ *mcp.CallToolRequest, in accountInput) (*mcp.CallToolResult, FoldersOut, error) {
	folders, err := d.svc.ListFolders(ctx, account.AccountID(in.Account))
	if err != nil {
		return nil, FoldersOut{}, err
	}
	out := FoldersOut{Folders: make([]FolderOut, len(folders))}
	for i, f := range folders {
		out.Folders[i] = FolderOut{Name: clean(f.Name), Alias: f.Alias, Unread: f.Unread, Total: f.Total, Trash: f.Trash}
	}
	return success(out)
}

func (d *deps) listTags(ctx context.Context, _ *mcp.CallToolRequest, in accountInput) (*mcp.CallToolResult, TagsOut, error) {
	tags, supported, err := d.svc.ListTags(ctx, account.AccountID(in.Account))
	if err != nil {
		return nil, TagsOut{}, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return success(TagsOut{Supported: supported, Tags: cleanAll(names)})
}

func (d *deps) searchMessages(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, SearchOut, error) {
	q, err := in.query()
	if err != nil {
		return nil, SearchOut{}, err
	}
	page, err := d.svc.Search(ctx, account.AccountID(in.Account), q, mail.PageToken(in.PageToken))
	if err != nil {
		return nil, SearchOut{}, err
	}
	return success(SearchOut{Messages: summariesOut(page.Items), NextPageToken: string(page.Next)})
}

// query turns the arguments of search_messages into a mail.Query.
func (in searchInput) query() (mail.Query, error) {
	q := mail.Query{
		Folder: in.Folder, From: in.From, To: in.To, Subject: in.Subject, Text: in.Text,
		Unread: in.Unread, Flagged: in.Flagged,
		TagsAny: in.TagsAny, TagsNone: in.TagsNone, Untagged: in.Untagged,
	}
	var err error
	if q.Since, err = parseTime("since", in.Since); err != nil {
		return mail.Query{}, err
	}
	if q.Before, err = parseTime("before", in.Before); err != nil {
		return mail.Query{}, err
	}
	if in.Limit < 0 {
		return mail.Query{}, &argError{arg: "limit", hint: "Use a number from 1 to 100."}
	}
	q.Limit = min(in.Limit, maxSearchLimit)
	return q, nil
}

// parseTime reads an optional RFC 3339 time. An empty string gives nil.
func parseTime(arg, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, &argError{arg: arg, hint: "Use RFC 3339 with an offset, for example 2026-03-10T10:00:00+01:00."}
	}
	return &t, nil
}

func (d *deps) listNewMessages(ctx context.Context, _ *mcp.CallToolRequest, in newMessagesInput) (*mcp.CallToolResult, NewMessagesOut, error) {
	folder := in.Folder
	if folder == "" {
		folder = defaultFolder
	}
	items, cursor, reset, err := d.svc.NewMessages(ctx, account.AccountID(in.Account), folder, mail.Cursor(in.Cursor))
	if err != nil {
		return nil, NewMessagesOut{}, err
	}
	return success(NewMessagesOut{Messages: summariesOut(items), Cursor: string(cursor), Reset: reset})
}

func (d *deps) getMessage(ctx context.Context, _ *mcp.CallToolRequest, in getMessageInput) (*mcp.CallToolResult, MessageOut, error) {
	opts, err := in.bodyOptions()
	if err != nil {
		return nil, MessageOut{}, err
	}
	msg, err := d.svc.GetMessage(ctx, account.AccountID(in.Account), mail.Ref(in.Ref), opts)
	if err != nil {
		return nil, MessageOut{}, err
	}
	if msg == nil {
		return nil, MessageOut{}, mail.ErrNotFound
	}
	return success(messageOut(msg))
}

// bodyOptions turns the arguments of get_message into mail.BodyOptions. The
// backend makes the HTML safe, and no other path returns HTML.
func (in getMessageInput) bodyOptions() (mail.BodyOptions, error) {
	format := mail.BodyFormat(in.BodyFormat)
	switch format {
	case "":
		format = mail.BodyText
	case mail.BodyText, mail.BodyHTML, mail.BodyNone:
	default:
		return mail.BodyOptions{}, &argError{arg: "body_format", hint: "Use text, html or none."}
	}
	if in.MaxChars < 0 {
		return mail.BodyOptions{}, &argError{arg: "max_chars", hint: "Use a positive number."}
	}
	if in.Offset < 0 {
		return mail.BodyOptions{}, &argError{arg: "offset", hint: "Use zero or a positive number."}
	}
	return mail.BodyOptions{Format: format, MaxChars: in.MaxChars, Offset: in.Offset}, nil
}

func (d *deps) getAttachment(ctx context.Context, _ *mcp.CallToolRequest, in getAttachmentInput) (*mcp.CallToolResult, AttachmentOut, error) {
	// The service asks the backend for at most service.MaxAttachmentBytes. The size
	// is checked again here, because the limit must hold whatever a backend does.
	att, err := d.svc.GetAttachment(ctx, account.AccountID(in.Account), mail.Ref(in.Ref), in.AttachmentID)
	if err != nil {
		return nil, AttachmentOut{}, err
	}
	if att == nil {
		return nil, AttachmentOut{}, mail.ErrNotFound
	}
	if len(att.Data) > service.MaxAttachmentBytes {
		return nil, AttachmentOut{}, mail.ErrTooLarge
	}
	mimeType := contentType(att.ContentType)
	out := AttachmentOut{
		ID:          att.ID,
		ContentType: mimeType,
		Size:        int64(len(att.Data)),
		Untrusted:   markUntrusted(AttachmentText{Filename: clean(att.Filename)}),
	}
	content := append(textCopy(out), &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
		URI:      attachmentURI(in.Ref, att.ID),
		MIMEType: mimeType,
		Blob:     att.Data,
	}})
	return &mcp.CallToolResult{Content: content}, out, nil
}

// attachmentURI names an attachment for the embedded resource. The URI only
// identifies the resource. Nothing fetches it.
func attachmentURI(ref, attachmentID string) string {
	return "email-mcp://attachment/" + url.PathEscape(ref) + "/" + url.PathEscape(attachmentID)
}

func (d *deps) getSendStatus(ctx context.Context, _ *mcp.CallToolRequest, in sendStatusInput) (*mcp.CallToolResult, SendStatusOut, error) {
	res, err := d.svc.SendStatus(ctx, account.AccountID(in.Account), in.SendID)
	if err != nil {
		return nil, SendStatusOut{}, err
	}
	if res == nil {
		return nil, SendStatusOut{}, errors.New("mcpserver: send status is empty")
	}
	out := SendStatusOut{Status: res.Status, SendID: res.SendID, ApprovalLink: res.ApprovalLink}
	if res.SendAt != nil {
		out.SendAt = formatTime(*res.SendAt)
	}
	return success(out)
}
