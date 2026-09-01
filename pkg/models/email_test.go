package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEmail(t *testing.T) {
	t.Run("create email with all fields", func(t *testing.T) {
		email := &Email{
			ID:        1,
			MessageID: "test@example.com",
			From:      []string{"sender@example.com"},
			To:        []string{"recipient@example.com"},
			Cc:        []string{"cc@example.com"},
			Bcc:       []string{"bcc@example.com"},
			Subject:   "Test Subject",
			Date:      time.Now(),
			TextBody:  "Text content",
			HTMLBody:  "<p>HTML content</p>",
			Size:      1024,
			Flags:     []string{"\\Seen"},
			Headers: map[string]string{
				"X-Custom": "value",
			},
		}

		assert.NotNil(t, email)
		assert.Equal(t, uint32(1), email.ID)
		assert.Equal(t, "Test Subject", email.Subject)
		assert.Len(t, email.From, 1)
		assert.Len(t, email.To, 1)
	})

	t.Run("email with attachments", func(t *testing.T) {
		email := &Email{
			ID:      1,
			Subject: "Email with attachments",
			Attachments: []Attachment{
				{
					Filename:    "test.pdf",
					ContentType: "application/pdf",
					Size:        2048,
					Data:        []byte("test data"),
				},
			},
		}

		assert.Len(t, email.Attachments, 1)
		assert.Equal(t, "test.pdf", email.Attachments[0].Filename)
		assert.Equal(t, int64(2048), email.Attachments[0].Size)
	})
}

func TestSearchCriteria(t *testing.T) {
	t.Run("default search criteria", func(t *testing.T) {
		criteria := &SearchCriteria{}

		assert.Empty(t, criteria.From)
		assert.Empty(t, criteria.To)
		assert.Empty(t, criteria.Subject)
		assert.Equal(t, 0, criteria.Limit)
		assert.False(t, criteria.Unseen)
		assert.False(t, criteria.Seen)
	})

	t.Run("search criteria with filters", func(t *testing.T) {
		since := time.Now().Add(-24 * time.Hour)
		before := time.Now()

		criteria := &SearchCriteria{
			From:    "sender@example.com",
			To:      "recipient@example.com",
			Subject: "Important",
			Since:   since,
			Before:  before,
			Unseen:  true,
			Folder:  "INBOX",
			Limit:   10,
		}

		assert.Equal(t, "sender@example.com", criteria.From)
		assert.Equal(t, "recipient@example.com", criteria.To)
		assert.Equal(t, "Important", criteria.Subject)
		assert.True(t, criteria.Unseen)
		assert.Equal(t, 10, criteria.Limit)
		assert.Equal(t, "INBOX", criteria.Folder)
	})
}

func TestSendEmailRequest(t *testing.T) {
	t.Run("basic send email request", func(t *testing.T) {
		req := &SendEmailRequest{
			To:      []string{"recipient@example.com"},
			Subject: "Test Email",
			Body:    "This is a test",
			IsHTML:  false,
		}

		assert.Len(t, req.To, 1)
		assert.Equal(t, "Test Email", req.Subject)
		assert.False(t, req.IsHTML)
	})

	t.Run("send email with cc and bcc", func(t *testing.T) {
		req := &SendEmailRequest{
			To:      []string{"recipient@example.com"},
			Cc:      []string{"cc@example.com"},
			Bcc:     []string{"bcc@example.com"},
			Subject: "Test Email",
			Body:    "This is a test",
			IsHTML:  true,
		}

		assert.Len(t, req.To, 1)
		assert.Len(t, req.Cc, 1)
		assert.Len(t, req.Bcc, 1)
		assert.True(t, req.IsHTML)
	})

	t.Run("send email with attachments", func(t *testing.T) {
		req := &SendEmailRequest{
			To:      []string{"recipient@example.com"},
			Subject: "Test Email",
			Body:    "This is a test",
			Attachments: []AttachmentData{
				{
					Filename: "document.pdf",
					Data:     []byte("PDF content"),
				},
			},
		}

		assert.Len(t, req.Attachments, 1)
		assert.Equal(t, "document.pdf", req.Attachments[0].Filename)
	})
}

func TestAttachment(t *testing.T) {
	t.Run("create attachment", func(t *testing.T) {
		att := Attachment{
			Filename:    "test.txt",
			ContentType: "text/plain",
			Size:        100,
			Data:        []byte("test content"),
		}

		assert.Equal(t, "test.txt", att.Filename)
		assert.Equal(t, "text/plain", att.ContentType)
		assert.Equal(t, int64(100), att.Size)
		assert.NotEmpty(t, att.Data)
	})
}

func TestAttachmentData(t *testing.T) {
	t.Run("create attachment data", func(t *testing.T) {
		attData := AttachmentData{
			Filename: "report.pdf",
			Data:     []byte("PDF binary data"),
		}

		assert.Equal(t, "report.pdf", attData.Filename)
		assert.NotEmpty(t, attData.Data)
	})
}
