package imap

import (
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/EmadMokhtar/email-mcp-go/pkg/models"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-message/mail"
)

// maxSearchResults caps how many messages a single search returns.
const maxSearchResults = 50

func (c *Client) ListMailboxes() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

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

// clampLimit keeps a caller-supplied result limit inside 1..maxSearchResults.
// The tool schema accepts any integer, and a negative limit used directly
// would index past the end of the result slice and panic.
func clampLimit(limit int) int {
	if limit <= 0 || limit > maxSearchResults {
		return maxSearchResults
	}

	return limit
}

func (c *Client) SearchEmails(criteria *models.SearchCriteria) ([]*models.Email, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

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

	// Search by UID so the IDs we return stay valid across expunges.
	ids, err := c.client.UidSearch(searchCriteria)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	if len(ids) == 0 {
		return []*models.Email{}, nil
	}

	limit := clampLimit(criteria.Limit)
	if len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}

	// Fetch messages
	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)

	messages := make(chan *imap.Message, len(ids))
	done := make(chan error, 1)

	// Peek, so that searching does not set \Seen on the results. Without it
	// a search for unread mail would mark every match as read.
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchUid,
		imap.FetchRFC822Size,
		section.FetchItem(),
	}

	go func() {
		done <- c.client.UidFetch(seqset, items, messages)
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
	c.mu.Lock()
	defer c.mu.Unlock()

	// Select mailbox
	_, err := c.client.Select(folder, false)
	if err != nil {
		return nil, fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(id)

	messages := make(chan *imap.Message, 1)
	// Peek here too: reading an email is an explicit mark_as_read decision,
	// not a side effect of fetching it.
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchUid,
		imap.FetchRFC822Size,
		section.FetchItem(),
	}

	done := make(chan error, 1)
	go func() {
		done <- c.client.UidFetch(seqset, items, messages)
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
		// UID, not SeqNum. Sequence numbers are renumbered when a message is
		// expunged, so an ID handed to a caller would later point at a
		// different message.
		ID:      msg.Uid,
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
				body, _ := io.ReadAll(part.Body)

				if strings.HasPrefix(contentType, "text/plain") {
					email.TextBody = string(body)
				} else if strings.HasPrefix(contentType, "text/html") {
					email.HTMLBody = string(body)
				}

			case *mail.AttachmentHeader:
				if includeAttachments {
					filename, _ := h.Filename()
					contentType, _, _ := h.ContentType()
					data, _ := io.ReadAll(part.Body)

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
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.client.Select(folder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailIDs...)

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.SeenFlag}

	if err := c.client.UidStore(seqset, item, flags, nil); err != nil {
		return fmt.Errorf("failed to mark as read: %w", err)
	}

	return nil
}

func (c *Client) MarkAsUnread(emailIDs []uint32, folder string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.client.Select(folder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailIDs...)

	item := imap.FormatFlagsOp(imap.RemoveFlags, true)
	flags := []interface{}{imap.SeenFlag}

	if err := c.client.UidStore(seqset, item, flags, nil); err != nil {
		return fmt.Errorf("failed to mark as unread: %w", err)
	}

	return nil
}

func (c *Client) MoveEmail(emailID uint32, fromFolder, toFolder string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.client.Select(fromFolder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailID)

	return c.moveLocked(seqset, toFolder)
}

// moveLocked moves messages to dest. The caller must hold c.mu and must have
// selected the source mailbox.
//
// go-imap's UidMove quietly falls back to COPY + \Deleted + EXPUNGE when the
// server has no MOVE extension, and that EXPUNGE is mailbox-wide: it would
// also erase unrelated messages that happen to carry \Deleted. So require
// MOVE, and without it copy and flag the original without expunging.
func (c *Client) moveLocked(seqset *imap.SeqSet, dest string) error {
	hasMove, err := c.client.Support("MOVE")
	if err != nil {
		return fmt.Errorf("failed to check for MOVE support: %w", err)
	}

	if hasMove {
		if err := c.client.UidMove(seqset, dest); err != nil {
			return fmt.Errorf("failed to move email: %w", err)
		}

		return nil
	}

	if err := c.client.UidCopy(seqset, dest); err != nil {
		return fmt.Errorf("failed to copy email to %q: %w", dest, err)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := c.client.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return fmt.Errorf("failed to flag the original email as deleted: %w", err)
	}

	log.Println("ℹ️  Server has no MOVE extension: the email was copied and flagged deleted, not expunged")

	return nil
}

// trashMailboxLocked finds the Trash mailbox. Servers name it differently
// (Gmail uses "[Gmail]/Trash"), so prefer the \Trash special-use attribute
// from RFC 6154 and fall back to the common names. The caller must hold c.mu.
func (c *Client) trashMailboxLocked() (string, error) {
	mailboxes := make(chan *imap.MailboxInfo, 20)
	done := make(chan error, 1)

	go func() {
		done <- c.client.List("", "*", mailboxes)
	}()

	var names []string
	var special string
	for m := range mailboxes {
		names = append(names, m.Name)
		for _, attr := range m.Attributes {
			if strings.EqualFold(attr, imap.TrashAttr) {
				special = m.Name
			}
		}
	}

	if err := <-done; err != nil {
		return "", fmt.Errorf("failed to list mailboxes: %w", err)
	}

	if special != "" {
		return special, nil
	}

	for _, name := range names {
		switch strings.ToLower(name) {
		case "trash", "deleted items", "deleted messages", "[gmail]/trash":
			return name, nil
		}
	}

	return "", fmt.Errorf("no Trash mailbox found: delete permanently or move the email explicitly")
}

func (c *Client) DeleteEmail(emailID uint32, folder string, permanent bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.client.Select(folder, false)
	if err != nil {
		return fmt.Errorf("failed to select mailbox: %w", err)
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(emailID)

	if permanent {
		return c.expungeOneLocked(seqset, emailID)
	}

	// A non-permanent delete promises a move to Trash, so actually move it.
	// Only flagging it \Deleted would leave the email in place, to be removed
	// later by an unrelated expunge.
	trash, err := c.trashMailboxLocked()
	if err != nil {
		return err
	}

	if strings.EqualFold(trash, folder) {
		return nil
	}

	return c.moveLocked(seqset, trash)
}

// expungeOneLocked permanently removes exactly the message identified by uid.
//
// EXPUNGE cannot name a message: it removes every message in the mailbox
// carrying \Deleted, and this version of go-imap has no UID EXPUNGE. So clear
// \Deleted from any other message that has it, expunge, then put the flag
// back. c.mu is held throughout, so none of our own operations sees the gap.
func (c *Client) expungeOneLocked(seqset *imap.SeqSet, uid uint32) error {
	criteria := imap.NewSearchCriteria()
	criteria.WithFlags = []string{imap.DeletedFlag}

	flagged, err := c.client.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("failed to list messages already marked deleted: %w", err)
	}

	others := new(imap.SeqSet)
	otherCount := 0
	for _, other := range flagged {
		if other != uid {
			others.AddNum(other)
			otherCount++
		}
	}

	add := imap.FormatFlagsOp(imap.AddFlags, true)
	remove := imap.FormatFlagsOp(imap.RemoveFlags, true)
	deleted := []interface{}{imap.DeletedFlag}

	if otherCount > 0 {
		if err := c.client.UidStore(others, remove, deleted, nil); err != nil {
			return fmt.Errorf("failed to protect %d other deleted message(s): %w", otherCount, err)
		}

		// Restore the flag even if the expunge below fails.
		defer func() {
			if err := c.client.UidStore(others, add, deleted, nil); err != nil {
				log.Printf("⚠️  Failed to restore the deleted flag on %d message(s): %v", otherCount, err)
			}
		}()
	}

	if err := c.client.UidStore(seqset, add, deleted, nil); err != nil {
		return fmt.Errorf("failed to mark as deleted: %w", err)
	}

	if err := c.client.Expunge(nil); err != nil {
		return fmt.Errorf("failed to expunge: %w", err)
	}

	return nil
}
