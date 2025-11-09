package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestListMailboxesTool(t *testing.T) {
	tool := ListMailboxesTool()

	assert.Equal(t, "list_mailboxes", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)
}

func TestSearchEmailsTool(t *testing.T) {
	tool := SearchEmailsTool()

	assert.Equal(t, "search_emails", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "from")
	assert.Contains(t, props, "to")
	assert.Contains(t, props, "subject")
	assert.Contains(t, props, "since")
	assert.Contains(t, props, "before")
	assert.Contains(t, props, "unseen")
	assert.Contains(t, props, "seen")
	assert.Contains(t, props, "folder")
	assert.Contains(t, props, "limit")
}

func TestGetEmailTool(t *testing.T) {
	tool := GetEmailTool()

	assert.Equal(t, "get_email", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "id")
	assert.Contains(t, props, "folder")
	assert.Contains(t, props, "include_attachments")

	// Check required fields
	assert.Contains(t, tool.InputSchema.Required, "id")
}

func TestSendEmailTool(t *testing.T) {
	tool := SendEmailTool()

	assert.Equal(t, "send_email", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "to")
	assert.Contains(t, props, "subject")
	assert.Contains(t, props, "body")
	assert.Contains(t, props, "is_html")
	assert.Contains(t, props, "cc")
	assert.Contains(t, props, "bcc")

	// Check required fields
	assert.Contains(t, tool.InputSchema.Required, "to")
	assert.Contains(t, tool.InputSchema.Required, "subject")
	assert.Contains(t, tool.InputSchema.Required, "body")
}

func TestReplyToEmailTool(t *testing.T) {
	tool := ReplyToEmailTool()

	assert.Equal(t, "reply_to_email", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "email_id")
	assert.Contains(t, props, "folder")
	assert.Contains(t, props, "body")
	assert.Contains(t, props, "reply_all")
	assert.Contains(t, props, "is_html")

	// Check required fields
	assert.Contains(t, tool.InputSchema.Required, "email_id")
	assert.Contains(t, tool.InputSchema.Required, "body")
}

func TestForwardEmailTool(t *testing.T) {
	tool := ForwardEmailTool()

	assert.Equal(t, "forward_email", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "email_id")
	assert.Contains(t, props, "folder")
	assert.Contains(t, props, "to")
	assert.Contains(t, props, "message")

	// Check required fields
	assert.Contains(t, tool.InputSchema.Required, "email_id")
	assert.Contains(t, tool.InputSchema.Required, "to")
}

func TestMarkAsReadTool(t *testing.T) {
	tool := MarkAsReadTool()

	assert.Equal(t, "mark_as_read", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "email_ids")
}

func TestMarkAsUnreadTool(t *testing.T) {
	tool := MarkAsUnreadTool()

	assert.Equal(t, "mark_as_unread", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "email_ids")
}

func TestMoveEmailTool(t *testing.T) {
	tool := MoveEmailTool()

	assert.Equal(t, "move_email", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "email_id")
}

func TestDeleteEmailTool(t *testing.T) {
	tool := DeleteEmailTool()

	assert.Equal(t, "delete_email", tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Equal(t, "object", tool.InputSchema.Type)
	assert.NotNil(t, tool.InputSchema.Properties)

	// Check for expected properties
	props := tool.InputSchema.Properties
	assert.Contains(t, props, "email_id")
}
