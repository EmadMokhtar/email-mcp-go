package tools

import (
	"github.com/mark3labs/mcp-go/mcp"
)

func ListMailboxesTool() mcp.Tool {
	return mcp.Tool{
		Name:        "list_mailboxes",
		Description: "List all mailboxes/folders in the email account",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}
}

func SearchEmailsTool() mcp.Tool {
	return mcp.Tool{
		Name:        "search_emails",
		Description: "Search for emails based on various criteria",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"from": map[string]interface{}{
					"type":        "string",
					"description": "Filter by sender email address",
				},
				"to": map[string]interface{}{
					"type":        "string",
					"description": "Filter by recipient email address",
				},
				"subject": map[string]interface{}{
					"type":        "string",
					"description": "Filter by subject keywords",
				},
				"since": map[string]interface{}{
					"type":        "string",
					"description": "Emails after this date (RFC3339 format)",
				},
				"before": map[string]interface{}{
					"type":        "string",
					"description": "Emails before this date (RFC3339 format)",
				},
				"unseen": map[string]interface{}{
					"type":        "boolean",
					"description": "Only unread emails",
				},
				"seen": map[string]interface{}{
					"type":        "boolean",
					"description": "Only read emails",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Search in specific folder (default: INBOX)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of results (default: 50)",
				},
			},
		},
	}
}

func GetEmailTool() mcp.Tool {
	return mcp.Tool{
		Name:        "get_email",
		Description: "Retrieve full email content by ID",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"id": map[string]interface{}{
					"type":        "integer",
					"description": "Email sequence number",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Mailbox name (default: INBOX)",
				},
				"include_attachments": map[string]interface{}{
					"type":        "boolean",
					"description": "Download attachments",
				},
			},
			Required: []string{"id"},
		},
	}
}

func SendEmailTool() mcp.Tool {
	return mcp.Tool{
		Name:        "send_email",
		Description: "Send a new email",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"to": map[string]interface{}{
					"type":        "array",
					"items":       map[string]string{"type": "string"},
					"description": "Recipient email addresses",
				},
				"subject": map[string]interface{}{
					"type":        "string",
					"description": "Email subject",
				},
				"body": map[string]interface{}{
					"type":        "string",
					"description": "Email body content",
				},
				"is_html": map[string]interface{}{
					"type":        "boolean",
					"description": "Send as HTML (default: false)",
				},
				"cc": map[string]interface{}{
					"type":        "array",
					"items":       map[string]string{"type": "string"},
					"description": "CC recipients",
				},
				"bcc": map[string]interface{}{
					"type":        "array",
					"items":       map[string]string{"type": "string"},
					"description": "BCC recipients",
				},
			},
			Required: []string{"to", "subject", "body"},
		},
	}
}

func ReplyToEmailTool() mcp.Tool {
	return mcp.Tool{
		Name:        "reply_to_email",
		Description: "Reply to an existing email",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"email_id": map[string]interface{}{
					"type":        "integer",
					"description": "ID of email to reply to",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Folder containing the email (default: INBOX)",
				},
				"body": map[string]interface{}{
					"type":        "string",
					"description": "Reply message body",
				},
				"reply_all": map[string]interface{}{
					"type":        "boolean",
					"description": "Reply to all recipients (default: false)",
				},
				"is_html": map[string]interface{}{
					"type":        "boolean",
					"description": "Send as HTML (default: false)",
				},
			},
			Required: []string{"email_id", "body"},
		},
	}
}

func ForwardEmailTool() mcp.Tool {
	return mcp.Tool{
		Name:        "forward_email",
		Description: "Forward an email to other recipients",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"email_id": map[string]interface{}{
					"type":        "integer",
					"description": "ID of email to forward",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Folder containing the email (default: INBOX)",
				},
				"to": map[string]interface{}{
					"type":        "array",
					"items":       map[string]string{"type": "string"},
					"description": "Forward recipients",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Additional message to include",
				},
			},
			Required: []string{"email_id", "to"},
		},
	}
}

func MarkAsReadTool() mcp.Tool {
	return mcp.Tool{
		Name:        "mark_as_read",
		Description: "Mark emails as read",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"email_ids": map[string]interface{}{
					"type":        "array",
					"items":       map[string]string{"type": "integer"},
					"description": "Array of email IDs",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Mailbox name (default: INBOX)",
				},
			},
			Required: []string{"email_ids"},
		},
	}
}

func MarkAsUnreadTool() mcp.Tool {
	return mcp.Tool{
		Name:        "mark_as_unread",
		Description: "Mark emails as unread",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"email_ids": map[string]interface{}{
					"type":        "array",
					"items":       map[string]string{"type": "integer"},
					"description": "Array of email IDs",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Mailbox name (default: INBOX)",
				},
			},
			Required: []string{"email_ids"},
		},
	}
}

func MoveEmailTool() mcp.Tool {
	return mcp.Tool{
		Name:        "move_email",
		Description: "Move email to a different folder",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"email_id": map[string]interface{}{
					"type":        "integer",
					"description": "Email ID to move",
				},
				"from_folder": map[string]interface{}{
					"type":        "string",
					"description": "Source folder",
				},
				"to_folder": map[string]interface{}{
					"type":        "string",
					"description": "Destination folder",
				},
			},
			Required: []string{"email_id", "to_folder"},
		},
	}
}

func DeleteEmailTool() mcp.Tool {
	return mcp.Tool{
		Name:        "delete_email",
		Description: "Delete an email",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"email_id": map[string]interface{}{
					"type":        "integer",
					"description": "Email ID to delete",
				},
				"folder": map[string]interface{}{
					"type":        "string",
					"description": "Mailbox name (default: INBOX)",
				},
				"permanent": map[string]interface{}{
					"type":        "boolean",
					"description": "Permanently delete vs move to trash (default: false)",
				},
			},
			Required: []string{"email_id"},
		},
	}
}
