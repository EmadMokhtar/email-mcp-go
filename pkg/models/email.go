package models

import "time"

type Email struct {
	ID          uint32            `json:"id"`
	MessageID   string            `json:"message_id"`
	From        []string          `json:"from"`
	To          []string          `json:"to"`
	Cc          []string          `json:"cc,omitempty"`
	Bcc         []string          `json:"bcc,omitempty"`
	Subject     string            `json:"subject"`
	Date        time.Time         `json:"date"`
	TextBody    string            `json:"text_body,omitempty"`
	HTMLBody    string            `json:"html_body,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Flags       []string          `json:"flags,omitempty"`
	Size        uint32            `json:"size"`
}

type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Data        []byte `json:"data,omitempty"`
}

type SearchCriteria struct {
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Since   time.Time `json:"since,omitempty"`
	Before  time.Time `json:"before,omitempty"`
	Unseen  bool      `json:"unseen,omitempty"`
	Seen    bool      `json:"seen,omitempty"`
	Folder  string    `json:"folder,omitempty"`
	Limit   int       `json:"limit,omitempty"`
}

type SendEmailRequest struct {
	To          []string         `json:"to"`
	Cc          []string         `json:"cc,omitempty"`
	Bcc         []string         `json:"bcc,omitempty"`
	Subject     string           `json:"subject"`
	Body        string           `json:"body"`
	IsHTML      bool             `json:"is_html"`
	Attachments []AttachmentData `json:"attachments,omitempty"`
}

type AttachmentData struct {
	Filename string `json:"filename"`
	Data     []byte `json:"data"`
}
