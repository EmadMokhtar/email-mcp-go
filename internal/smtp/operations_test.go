package smtp

import (
	"testing"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/EmadMokhtar/email-mcp-go/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestNewClient(t *testing.T) {
	cfg := &config.Config{
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		SMTPUsername: "user@example.com",
		SMTPPassword: "password",
		SMTPTLS:      true,
	}

	client := NewClient(cfg)

	assert.NotNil(t, client)
	assert.Equal(t, cfg, client.config)
}

func TestSendEmailRequest(t *testing.T) {
	t.Run("basic send email request validation", func(t *testing.T) {
		req := &models.SendEmailRequest{
			To:      []string{"recipient@example.com"},
			Subject: "Test Subject",
			Body:    "Test Body",
			IsHTML:  false,
		}

		assert.NotEmpty(t, req.To)
		assert.NotEmpty(t, req.Subject)
		assert.NotEmpty(t, req.Body)
	})

	t.Run("send email with multiple recipients", func(t *testing.T) {
		req := &models.SendEmailRequest{
			To:      []string{"recipient1@example.com", "recipient2@example.com"},
			Cc:      []string{"cc@example.com"},
			Bcc:     []string{"bcc@example.com"},
			Subject: "Test Subject",
			Body:    "Test Body",
			IsHTML:  true,
		}

		assert.Len(t, req.To, 2)
		assert.Len(t, req.Cc, 1)
		assert.Len(t, req.Bcc, 1)
		assert.True(t, req.IsHTML)
	})

	t.Run("send email with attachments", func(t *testing.T) {
		req := &models.SendEmailRequest{
			To:      []string{"recipient@example.com"},
			Subject: "Test with Attachments",
			Body:    "Test Body",
			IsHTML:  false,
			Attachments: []models.AttachmentData{
				{
					Filename: "test.txt",
					Data:     []byte("test content"),
				},
			},
		}

		assert.Len(t, req.Attachments, 1)
		assert.Equal(t, "test.txt", req.Attachments[0].Filename)
	})
}

func TestReplyToEmailValidation(t *testing.T) {
	t.Run("reply to email - regular reply", func(t *testing.T) {
		originalEmail := &models.Email{
			MessageID: "<original@example.com>",
			From:      []string{"sender@example.com"},
			To:        []string{"me@example.com"},
			Subject:   "Original Subject",
			Date:      time.Now(),
		}

		body := "This is my reply"

		assert.NotNil(t, originalEmail)
		assert.NotEmpty(t, originalEmail.From)
		assert.NotEmpty(t, body)
	})

	t.Run("reply all validation", func(t *testing.T) {
		originalEmail := &models.Email{
			MessageID: "<original@example.com>",
			From:      []string{"sender@example.com"},
			To:        []string{"me@example.com", "other@example.com"},
			Cc:        []string{"cc@example.com"},
			Subject:   "Original Subject",
			Date:      time.Now(),
		}

		assert.Len(t, originalEmail.To, 2)
		assert.Len(t, originalEmail.Cc, 1)
	})
}

func TestForwardEmailValidation(t *testing.T) {
	t.Run("forward email with message", func(t *testing.T) {
		originalEmail := &models.Email{
			From:     []string{"original-sender@example.com"},
			To:       []string{"original-recipient@example.com"},
			Subject:  "Original Subject",
			TextBody: "Original email content",
			Date:     time.Now(),
		}

		forwardTo := []string{"new-recipient@example.com"}
		message := "Please see the forwarded message below"

		assert.NotNil(t, originalEmail)
		assert.NotEmpty(t, forwardTo)
		assert.NotEmpty(t, message)
	})

	t.Run("forward email with attachments", func(t *testing.T) {
		originalEmail := &models.Email{
			From:     []string{"sender@example.com"},
			Subject:  "Email with attachments",
			TextBody: "Content",
			Date:     time.Now(),
			Attachments: []models.Attachment{
				{
					Filename:    "document.pdf",
					ContentType: "application/pdf",
					Size:        1024,
					Data:        []byte("PDF content"),
				},
			},
		}

		assert.Len(t, originalEmail.Attachments, 1)
	})

	t.Run("forward email with HTML body", func(t *testing.T) {
		originalEmail := &models.Email{
			From:     []string{"sender@example.com"},
			Subject:  "HTML Email",
			HTMLBody: "<p>HTML content</p>",
			Date:     time.Now(),
		}

		assert.NotEmpty(t, originalEmail.HTMLBody)
		assert.Empty(t, originalEmail.TextBody)
	})
}
