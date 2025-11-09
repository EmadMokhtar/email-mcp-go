package server

import (
	"encoding/json"
	"testing"

	"github.com/EmadMokhtar/email-mcp-go/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestSearchEmailsArguments(t *testing.T) {
	t.Run("parse search criteria arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"from":    "sender@example.com",
			"to":      "recipient@example.com",
			"subject": "Important",
			"folder":  "INBOX",
			"limit":   float64(10), // JSON numbers are float64
			"unseen":  true,
		}

		var criteria models.SearchCriteria
		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &criteria)
		assert.NoError(t, err)

		assert.Equal(t, "sender@example.com", criteria.From)
		assert.Equal(t, "recipient@example.com", criteria.To)
		assert.Equal(t, "Important", criteria.Subject)
		assert.Equal(t, "INBOX", criteria.Folder)
		assert.Equal(t, 10, criteria.Limit)
		assert.True(t, criteria.Unseen)
	})

	t.Run("parse empty arguments", func(t *testing.T) {
		arguments := map[string]interface{}{}

		var criteria models.SearchCriteria
		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &criteria)
		assert.NoError(t, err)

		assert.Empty(t, criteria.From)
		assert.Empty(t, criteria.To)
		assert.Empty(t, criteria.Subject)
		assert.Equal(t, 0, criteria.Limit)
	})
}

func TestGetEmailArguments(t *testing.T) {
	t.Run("parse get email arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"id":                  float64(123), // JSON numbers are float64
			"folder":              "INBOX",
			"include_attachments": true,
		}

		var params struct {
			ID                 uint32 `json:"id"`
			Folder             string `json:"folder"`
			IncludeAttachments bool   `json:"include_attachments"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Equal(t, uint32(123), params.ID)
		assert.Equal(t, "INBOX", params.Folder)
		assert.True(t, params.IncludeAttachments)
	})

	t.Run("parse minimal get email arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"id": float64(42),
		}

		var params struct {
			ID                 uint32 `json:"id"`
			Folder             string `json:"folder"`
			IncludeAttachments bool   `json:"include_attachments"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Equal(t, uint32(42), params.ID)
		assert.Empty(t, params.Folder)
		assert.False(t, params.IncludeAttachments)
	})
}

func TestSendEmailArguments(t *testing.T) {
	t.Run("parse send email arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"to":      []interface{}{"recipient@example.com"},
			"subject": "Test Subject",
			"body":    "Test Body",
			"is_html": false,
		}

		var req models.SendEmailRequest
		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &req)
		assert.NoError(t, err)

		assert.Len(t, req.To, 1)
		assert.Equal(t, "recipient@example.com", req.To[0])
		assert.Equal(t, "Test Subject", req.Subject)
		assert.Equal(t, "Test Body", req.Body)
		assert.False(t, req.IsHTML)
	})

	t.Run("parse send email with cc and bcc", func(t *testing.T) {
		arguments := map[string]interface{}{
			"to":      []interface{}{"recipient@example.com"},
			"cc":      []interface{}{"cc@example.com"},
			"bcc":     []interface{}{"bcc@example.com"},
			"subject": "Test Subject",
			"body":    "Test Body",
			"is_html": true,
		}

		var req models.SendEmailRequest
		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &req)
		assert.NoError(t, err)

		assert.Len(t, req.To, 1)
		assert.Len(t, req.Cc, 1)
		assert.Len(t, req.Bcc, 1)
		assert.True(t, req.IsHTML)
	})
}

func TestReplyToEmailArguments(t *testing.T) {
	t.Run("parse reply arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"email_id":  float64(100),
			"body":      "This is my reply",
			"reply_all": true,
			"is_html":   false,
			"folder":    "INBOX",
		}

		var params struct {
			EmailID  uint32 `json:"email_id"`
			Body     string `json:"body"`
			ReplyAll bool   `json:"reply_all"`
			IsHTML   bool   `json:"is_html"`
			Folder   string `json:"folder"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Equal(t, uint32(100), params.EmailID)
		assert.Equal(t, "This is my reply", params.Body)
		assert.True(t, params.ReplyAll)
		assert.False(t, params.IsHTML)
		assert.Equal(t, "INBOX", params.Folder)
	})
}

func TestForwardEmailArguments(t *testing.T) {
	t.Run("parse forward arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"email_id": float64(200),
			"to":       []interface{}{"forward@example.com"},
			"message":  "Please review",
			"folder":   "Sent",
		}

		var params struct {
			EmailID uint32   `json:"email_id"`
			To      []string `json:"to"`
			Message string   `json:"message"`
			Folder  string   `json:"folder"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Equal(t, uint32(200), params.EmailID)
		assert.Len(t, params.To, 1)
		assert.Equal(t, "forward@example.com", params.To[0])
		assert.Equal(t, "Please review", params.Message)
		assert.Equal(t, "Sent", params.Folder)
	})
}

func TestMarkEmailsArguments(t *testing.T) {
	t.Run("parse mark as read arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"email_ids": []interface{}{float64(1), float64(2), float64(3)},
			"folder":    "INBOX",
		}

		var params struct {
			EmailIDs []uint32 `json:"email_ids"`
			Folder   string   `json:"folder"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Len(t, params.EmailIDs, 3)
		assert.Equal(t, uint32(1), params.EmailIDs[0])
		assert.Equal(t, uint32(2), params.EmailIDs[1])
		assert.Equal(t, uint32(3), params.EmailIDs[2])
		assert.Equal(t, "INBOX", params.Folder)
	})
}

func TestMoveEmailArguments(t *testing.T) {
	t.Run("parse move email arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"email_id":    float64(42),
			"from_folder": "INBOX",
			"to_folder":   "Archive",
		}

		var params struct {
			EmailID    uint32 `json:"email_id"`
			FromFolder string `json:"from_folder"`
			ToFolder   string `json:"to_folder"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Equal(t, uint32(42), params.EmailID)
		assert.Equal(t, "INBOX", params.FromFolder)
		assert.Equal(t, "Archive", params.ToFolder)
	})
}

func TestDeleteEmailArguments(t *testing.T) {
	t.Run("parse delete email arguments", func(t *testing.T) {
		arguments := map[string]interface{}{
			"email_id":  float64(99),
			"folder":    "Trash",
			"permanent": true,
		}

		var params struct {
			EmailID   uint32 `json:"email_id"`
			Folder    string `json:"folder"`
			Permanent bool   `json:"permanent"`
		}

		argBytes, err := json.Marshal(arguments)
		assert.NoError(t, err)

		err = json.Unmarshal(argBytes, &params)
		assert.NoError(t, err)

		assert.Equal(t, uint32(99), params.EmailID)
		assert.Equal(t, "Trash", params.Folder)
		assert.True(t, params.Permanent)
	})
}

func TestJSONMarshaling(t *testing.T) {
	t.Run("marshal email list", func(t *testing.T) {
		emails := []*models.Email{
			{
				ID:      1,
				Subject: "Email 1",
				From:    []string{"sender1@example.com"},
			},
			{
				ID:      2,
				Subject: "Email 2",
				From:    []string{"sender2@example.com"},
			},
		}

		result, err := json.Marshal(emails)
		assert.NoError(t, err)
		assert.NotEmpty(t, result)

		var unmarshaled []*models.Email
		err = json.Unmarshal(result, &unmarshaled)
		assert.NoError(t, err)
		assert.Len(t, unmarshaled, 2)
		assert.Equal(t, "Email 1", unmarshaled[0].Subject)
	})

	t.Run("marshal mailbox list", func(t *testing.T) {
		mailboxes := []string{"INBOX", "Sent", "Drafts", "Archive"}

		result, err := json.Marshal(mailboxes)
		assert.NoError(t, err)
		assert.NotEmpty(t, result)

		var unmarshaled []string
		err = json.Unmarshal(result, &unmarshaled)
		assert.NoError(t, err)
		assert.Len(t, unmarshaled, 4)
		assert.Contains(t, unmarshaled, "INBOX")
	})
}
