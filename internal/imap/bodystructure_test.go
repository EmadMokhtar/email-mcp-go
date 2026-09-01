package imap

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap"
	"github.com/stretchr/testify/assert"
)

func TestTextParts(t *testing.T) {
	tests := []struct {
		name      string
		structure *imap.BodyStructure
		want      [][]int
	}{
		{
			// A plain message with no multipart wrapper is part 1.
			name:      "single text part",
			structure: &imap.BodyStructure{MIMEType: "text", MIMESubType: "plain"},
			want:      [][]int{{1}},
		},
		{
			name: "alternative text and html",
			structure: &imap.BodyStructure{
				MIMEType: "multipart", MIMESubType: "alternative",
				Parts: []*imap.BodyStructure{
					{MIMEType: "text", MIMESubType: "plain"},
					{MIMEType: "text", MIMESubType: "html"},
				},
			},
			want: [][]int{{1}, {2}},
		},
		{
			// The PDF must not be selected: leaving it on the server is the
			// whole point of the text-only fetch.
			name: "mixed with an attachment",
			structure: &imap.BodyStructure{
				MIMEType: "multipart", MIMESubType: "mixed",
				Parts: []*imap.BodyStructure{
					{MIMEType: "text", MIMESubType: "plain"},
					{MIMEType: "application", MIMESubType: "pdf", Disposition: "attachment"},
				},
			},
			want: [][]int{{1}},
		},
		{
			// A text file sent as an attachment is not body text either.
			name: "text part marked as an attachment",
			structure: &imap.BodyStructure{
				MIMEType: "multipart", MIMESubType: "mixed",
				Parts: []*imap.BodyStructure{
					{MIMEType: "text", MIMESubType: "html"},
					{MIMEType: "text", MIMESubType: "plain", Disposition: "attachment"},
				},
			},
			want: [][]int{{1}},
		},
		{
			name: "nested multipart",
			structure: &imap.BodyStructure{
				MIMEType: "multipart", MIMESubType: "mixed",
				Parts: []*imap.BodyStructure{
					{
						MIMEType: "multipart", MIMESubType: "alternative",
						Parts: []*imap.BodyStructure{
							{MIMEType: "text", MIMESubType: "plain"},
							{MIMEType: "text", MIMESubType: "html"},
						},
					},
					{MIMEType: "image", MIMESubType: "png", Disposition: "attachment"},
				},
			},
			want: [][]int{{1, 1}, {1, 2}},
		},
		{
			name: "attachments only",
			structure: &imap.BodyStructure{
				MIMEType: "multipart", MIMESubType: "mixed",
				Parts: []*imap.BodyStructure{
					{MIMEType: "application", MIMESubType: "pdf", Disposition: "attachment"},
				},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, textParts(tt.structure, nil))
		})
	}
}

func TestDecodePart(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		body        string
		wantType    string
		wantContent string
	}{
		{
			name:        "quoted-printable is decoded",
			header:      "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n",
			body:        "caf=C3=A9 and =3D signs",
			wantType:    "text/plain",
			wantContent: "café and = signs",
		},
		{
			name:        "base64 is decoded",
			header:      "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n",
			body:        "PHA+aGVsbG88L3A+",
			wantType:    "text/html",
			wantContent: "<p>hello</p>",
		},
		{
			name:        "plain text passes through",
			header:      "Content-Type: text/plain; charset=utf-8\r\n",
			body:        "just text",
			wantType:    "text/plain",
			wantContent: "just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType, content, err := decodePart(
				stringReader(tt.header),
				stringReader(tt.body),
			)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantType, contentType)
			assert.Equal(t, tt.wantContent, content)
		})
	}
}

func stringReader(s string) *strings.Reader { return strings.NewReader(s) }
