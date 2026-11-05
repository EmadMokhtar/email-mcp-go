package service

// This file records the tool calls of agents in the audit log. The MCP layer
// calls RecordToolCall once for each tools/call request: every tool and every
// outcome, also a read and a call that never reached a use case. The entries of
// the use cases (see write.go) are separate: they say what was done, and a call
// entry says what the agent asked for.

import (
	"context"
	"encoding/json"

	"github.com/EmadMokhtar/email-mcp-go/internal/audit"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

// The tools that have no use case that writes an audit entry: a call entry is
// all that the log has of them.
const (
	toolListAccounts      = "list_accounts"
	toolListFolders       = "list_folders"
	toolListTags          = "list_tags"
	toolSearchMessages    = "search_messages"
	toolListNewMessages   = "list_new_messages"
	toolGetMessage        = "get_message"
	toolGetAttachment     = "get_attachment"
	toolGetSendStatus     = "get_send_status"
	toolRequestPermission = "request_permission_change"
)

// ToolUnknown is the tool of a call entry for a tool that does not exist. The
// name that the agent asked for is not trusted, so it goes in the sealed
// arguments and not in the column.
const ToolUnknown = "unknown_tool"

// ToolCall is one tools/call request of an agent.
type ToolCall struct {
	// Tool is the name of the tool. It comes from the server: a tool of the
	// server, or ToolUnknown.
	Tool string
	// Account is the account of the arguments. It is text from the agent, so it
	// may be no account at all. RecordToolCall checks it.
	Account account.AccountID
	Outcome audit.Outcome
	// Detail is a short code, as in audit.Entry.
	Detail string
	// Arguments is the JSON of the arguments, as the MCP layer cleaned them. It
	// holds no message body and no attachment data.
	Arguments json.RawMessage
}

// RecordToolCall writes a call entry for the caller p.
//
// The entry names the account only when it is an account of the tenant of p.
// Otherwise it is an entry of the tenant, because the table cannot hold a row for
// an account that does not exist. The sealed arguments still show what the agent
// asked for.
//
// A failure does not change the answer of the call. See record.
func (s *Service) RecordToolCall(ctx context.Context, p Principal, c ToolCall) {
	if s.d.Audit == nil {
		return
	}
	s.record(ctx, p, entry{
		kind:      audit.KindCall,
		tool:      c.Tool,
		account:   s.accountOfTenant(ctx, p, c.Account),
		outcome:   c.Outcome,
		detail:    c.Detail,
		arguments: c.Arguments,
	})
}

// accountOfTenant returns id when it is an account of the tenant of p, and an
// empty id otherwise. The check does not use the cancellation of ctx: the agent
// may have left, and the entry must still name the account.
func (s *Service) accountOfTenant(ctx context.Context, p Principal, id account.AccountID) account.AccountID {
	if id == "" {
		return ""
	}
	if _, err := s.d.Store.GetAccount(context.WithoutCancel(ctx), p.Tenant, id); err != nil {
		return ""
	}
	return id
}
