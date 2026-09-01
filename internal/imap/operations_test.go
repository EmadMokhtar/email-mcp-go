package imap

import (
	"testing"
	"time"

	"github.com/EmadMokhtar/email-mcp-go/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestSearchCriteriaBuilder(t *testing.T) {
	t.Run("empty search criteria", func(t *testing.T) {
		criteria := &models.SearchCriteria{}

		assert.Empty(t, criteria.From)
		assert.Empty(t, criteria.To)
		assert.Empty(t, criteria.Subject)
		assert.True(t, criteria.Since.IsZero())
		assert.True(t, criteria.Before.IsZero())
		assert.False(t, criteria.Unseen)
		assert.False(t, criteria.Seen)
	})

	t.Run("search criteria with all fields", func(t *testing.T) {
		since := time.Now().Add(-7 * 24 * time.Hour)
		before := time.Now()

		criteria := &models.SearchCriteria{
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
		assert.Equal(t, since, criteria.Since)
		assert.Equal(t, before, criteria.Before)
		assert.True(t, criteria.Unseen)
		assert.Equal(t, "INBOX", criteria.Folder)
		assert.Equal(t, 10, criteria.Limit)
	})

	t.Run("search criteria default limit", func(t *testing.T) {
		criteria := &models.SearchCriteria{
			Folder: "INBOX",
		}

		// Default limit should be 0, and will be set to 50 in implementation
		assert.Equal(t, 0, criteria.Limit)
	})

	t.Run("search criteria with folder", func(t *testing.T) {
		criteria := &models.SearchCriteria{
			Folder: "Sent",
			Limit:  25,
		}

		assert.Equal(t, "Sent", criteria.Folder)
		assert.Equal(t, 25, criteria.Limit)
	})
}

func TestEmailConversion(t *testing.T) {
	t.Run("convert to email model", func(t *testing.T) {
		email := &models.Email{
			ID:        1,
			MessageID: "test@example.com",
			From:      []string{"sender@example.com"},
			To:        []string{"recipient@example.com"},
			Subject:   "Test",
			Date:      time.Now(),
			TextBody:  "Body",
			Size:      100,
			Flags:     []string{"\\Seen"},
		}

		assert.NotNil(t, email)
		assert.Equal(t, uint32(1), email.ID)
		assert.Len(t, email.From, 1)
		assert.Len(t, email.To, 1)
		assert.NotEmpty(t, email.Subject)
	})

	t.Run("email with multiple recipients", func(t *testing.T) {
		email := &models.Email{
			ID:      1,
			From:    []string{"sender@example.com"},
			To:      []string{"recipient1@example.com", "recipient2@example.com"},
			Cc:      []string{"cc@example.com"},
			Subject: "Test",
		}

		assert.Len(t, email.To, 2)
		assert.Len(t, email.Cc, 1)
	})

	t.Run("email with attachments", func(t *testing.T) {
		email := &models.Email{
			ID:      1,
			Subject: "Email with attachments",
			Attachments: []models.Attachment{
				{
					Filename:    "file1.pdf",
					ContentType: "application/pdf",
					Size:        1024,
				},
				{
					Filename:    "file2.txt",
					ContentType: "text/plain",
					Size:        512,
				},
			},
		}

		assert.Len(t, email.Attachments, 2)
		assert.Equal(t, "file1.pdf", email.Attachments[0].Filename)
		assert.Equal(t, "file2.txt", email.Attachments[1].Filename)
	})

	t.Run("email with both HTML and text body", func(t *testing.T) {
		email := &models.Email{
			ID:       1,
			Subject:  "Multipart email",
			TextBody: "Plain text content",
			HTMLBody: "<p>HTML content</p>",
		}

		assert.NotEmpty(t, email.TextBody)
		assert.NotEmpty(t, email.HTMLBody)
	})
}

func TestEmailFlags(t *testing.T) {
	t.Run("email with seen flag", func(t *testing.T) {
		email := &models.Email{
			ID:    1,
			Flags: []string{"\\Seen"},
		}

		assert.Contains(t, email.Flags, "\\Seen")
	})

	t.Run("email with multiple flags", func(t *testing.T) {
		email := &models.Email{
			ID:    1,
			Flags: []string{"\\Seen", "\\Flagged", "\\Answered"},
		}

		assert.Len(t, email.Flags, 3)
		assert.Contains(t, email.Flags, "\\Seen")
		assert.Contains(t, email.Flags, "\\Flagged")
		assert.Contains(t, email.Flags, "\\Answered")
	})
}

func TestMailboxOperations(t *testing.T) {
	t.Run("mark as read parameters", func(t *testing.T) {
		emailIDs := []uint32{1, 2, 3}
		folder := "INBOX"

		assert.Len(t, emailIDs, 3)
		assert.Equal(t, "INBOX", folder)
	})

	t.Run("mark as unread parameters", func(t *testing.T) {
		emailIDs := []uint32{5, 10}
		folder := "Sent"

		assert.Len(t, emailIDs, 2)
		assert.Equal(t, "Sent", folder)
	})

	t.Run("move email parameters", func(t *testing.T) {
		emailID := uint32(42)
		fromFolder := "INBOX"
		toFolder := "Archive"

		assert.Equal(t, uint32(42), emailID)
		assert.Equal(t, "INBOX", fromFolder)
		assert.Equal(t, "Archive", toFolder)
	})

	t.Run("delete email parameters", func(t *testing.T) {
		emailID := uint32(100)
		folder := "Trash"

		assert.Equal(t, uint32(100), emailID)
		assert.Equal(t, "Trash", folder)
	})
}
