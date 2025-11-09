package imap

import (
	"fmt"
	"io"
	"io/ioutil"
	"strings"
	_ "time"

	"github.com/EmadMokhtar/email-mcp-go/pkg/models"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-message/mail"
)

func (c *Client) ListMailboxes() ([]string, error) {
	mailboxes := make(chan *imap.MailboxInfo, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.client.List("", "*", mailboxes)
	}()

	var result []string
	for m := range mailboxes {
		result = append(result, m.Name)
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to list mailboxes: %w", err)
	}

	return result, nil
}

func (c *Client) SearchEmails(criteria *models.SearchCriteria) ([]*models.Email, error) {
	// Select mailbox
	folder := criteria.Folder
	if folder == "" {
		folder = "INBOX"
	}

	_, err := c.client.Select(folder, false)
	if err != nil {
		return nil, fmt.Errorf("failed to select mailbox: %w", err)
	}

	// Build search criteria
	searchCriteria := imap.NewSearchCriteria()

	if criteria.From != "" {
		searchCriteria.Header.Add("From", criteria.From)
	}
	if criteria.To != "" {
		searchCriteria.Header.Add("To", criteria.To)
	}
	if criteria.Subject != "" {
		searchCriteria.Header.Add("Subject", criteria.Subject)
	}
	if !criteria.Since.IsZero() {
		searchCriteria.Since = criteria.Since
	}
	if !criteria.Before.IsZero() {
		searchCriteria.Before = criteria.Before
	}
	if criteria.Unseen {
		searchCriteria.WithoutFlags = []string{imap.SeenFlag}
	}
	if criteria.Seen {
		searchCriteria.WithFlags = []string{imap.SeenFlag}
	}

	// Search
	ids, err := c.client.Search(searchCriteria)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	if len(ids) == 0 {
		return []*models.Email{}, nil
	}

	// Apply limit
	limit := criteria.Limit
	if limit == 0 || limit > 50 {
		limit = 50
	}
	if len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}

	// Fetch messages
	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)

	messages := make(chan *imap.Message, len(ids))
	done := make(chan error, 1)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchUid,
		imap.FetchRFC822Size,
		section.FetchItem(),
	}

	go func() {
		done <- c.client.Fetch(seqset, items, messages)
	}()

	var emails []*models.Email
	for msg := range messages {
		email := c.messageToEmail(msg, false)
		emails = append(emails, email)
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch messages: %w", err)
	}

	// Reverse to show newest first
	for i := len(emails)/2 - 1; i >= 0; i-- {
		opp := len(emails) - 1 - i
		emails[i], emails[opp] = emails[opp], emails[i]
	}

	return emails, nil
}

func (c *Client) GetEmail(id uint32, folder string, includeAttachments bool) (*models.Email, error) {
	// Select mailbox
	_, err := c.client.Select(folder, false)
	if err != nil {
		return nil, fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(id)

	messages := make(chan *imap.Message, 1)
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchUid,
		imap.FetchRFC822Size,
		section.FetchItem(),
	}

	done := make(chan error, 1)
	go func() {
		done <- c.client.Fetch(seqset, items, messages)
	}()

	msg := <-messages
	if msg == nil {
		return nil, fmt.Errorf("email not found")
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}

	return c.messageToEmail(msg, includeAttachments), nil
}

func (c *Client) messageToEmail(msg *imap.Message, includeAttachments bool) *models.Email {
	email := &models.Email{
		ID:      msg.SeqNum,
		Size:    msg.Size,
		Flags:   msg.Flags,
		Headers: make(map[string]string),
	}

	if msg.Envelope != nil {
		email.Subject = msg.Envelope.Subject
		email.Date = msg.Envelope.Date
		email.MessageID = msg.Envelope.MessageId

		for _, addr := range msg.Envelope.From {
			email.From = append(email.From, addr.Address())
		}
		for _, addr := range msg.Envelope.To {
			email.To = append(email.To, addr.Address())
		}
		for _, addr := range msg.Envelope.Cc {
			email.Cc = append(email.Cc, addr.Address())
		}
		for _, addr := range msg.Envelope.Bcc {
			email.Bcc = append(email.Bcc, addr.Address())
		}
	}

	// Parse body
	for _, literal := range msg.Body {
		mr, err := mail.CreateReader(literal)
		if err != nil {
			continue
		}

		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			switch h := part.Header.(type) {
			case *mail.InlineHeader:
				contentType, _, _ := h.ContentType()
				body, _ := ioutil.ReadAll(part.Body)

				if strings.HasPrefix(contentType, "text/plain") {
					email.TextBody = string(body)
				} else if strings.HasPrefix(contentType, "text/html") {
					email.HTMLBody = string(body)
				}

			case *mail.AttachmentHeader:
				if includeAttachments {
					filename, _ := h.Filename()
					contentType, _, _ := h.ContentType()
					data, _ := ioutil.ReadAll(part.Body)

					email.Attachments = append(email.Attachments, models.Attachment{
						Filename:    filename,
						ContentType: contentType,
						Size:        int64(len(data)),
						Data:        data,
					})
				}
			}
		}
	}

	return email
}

func (c *Client) MarkAsRead(emailIDs []uint32, folder string) error {
	_, err := c.client.Select(folder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailIDs...)

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.SeenFlag}

	if err := c.client.Store(seqset, item, flags, nil); err != nil {
		return fmt.Errorf("failed to mark as read: %w", err)
	}

	return nil
}

func (c *Client) MarkAsUnread(emailIDs []uint32, folder string) error {
	_, err := c.client.Select(folder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailIDs...)

	item := imap.FormatFlagsOp(imap.RemoveFlags, true)
	flags := []interface{}{imap.SeenFlag}

	if err := c.client.Store(seqset, item, flags, nil); err != nil {
		return fmt.Errorf("failed to mark as unread: %w", err)
	}

	return nil
}

func (c *Client) MoveEmail(emailID uint32, fromFolder, toFolder string) error {
	_, err := c.client.Select(fromFolder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailID)

	if err := c.client.Move(seqset, toFolder); err != nil {
		return fmt.Errorf("failed to move email: %w", err)
	}

	return nil
}

func (c *Client) DeleteEmail(emailID uint32, folder string, permanent bool) error {
	_, err := c.client.Select(folder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailID)

	// Mark as deleted
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.DeletedFlag}

	if err := c.client.Store(seqset, item, flags, nil); err != nil {
		return fmt.Errorf("failed to mark as deleted: %w", err)
	}

	// Expunge if permanent delete
	if permanent {
		if err := c.client.Expunge(nil); err != nil {
			return fmt.Errorf("failed to expunge: %w", err)
		}
	}

	return nil
}
