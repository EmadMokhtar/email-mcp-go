package mime

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
)

// msg joins lines with CRLF, the line ending that mail uses on the wire.
func msg(lines ...string) string {
	return strings.Join(lines, "\r\n")
}

func params(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestDecodeLatin1(t *testing.T) {
	got, err := DecodeText("text/plain", params("charset", "ISO-8859-1"), "quoted-printable", strings.NewReader("caf=E9"))
	require.NoError(t, err)
	require.Equal(t, "caf\U000000E9", got)
}

func TestUnknownCharsetFallsBack(t *testing.T) {
	p := params("charset", "x-unknown")

	got, err := DecodeText("text/plain", p, "8bit", strings.NewReader("plain ascii"))
	require.NoError(t, err)
	require.Equal(t, "plain ascii", got)

	// Bytes that are not valid UTF-8 become U+FFFD. Bytes that are valid
	// UTF-8 stay as they are.
	got, err = DecodeText("text/plain", p, "8bit", strings.NewReader("caf\xe9 ok"))
	require.NoError(t, err)
	require.Equal(t, "caf\U0000FFFD ok", got)

	got, err = DecodeText("text/plain", p, "8bit", strings.NewReader("caf\xc3\xa9"))
	require.NoError(t, err)
	require.Equal(t, "caf\U000000E9", got)
}

func TestDecodeTextCases(t *testing.T) {
	sjis := base64.StdEncoding.EncodeToString([]byte{0x93, 0xFA, 0x96, 0x7B, 0x8C, 0xEA})
	tests := []struct {
		name        string
		contentType string
		charset     string
		encoding    string
		body        string
		want        string
	}{
		{"windows-1252 smart quotes", "text/plain", "windows-1252", "8bit", "\x93hi\x94", "\U0000201Chi\U0000201D"},
		{"shift_jis in base64", "text/plain", "Shift_JIS", "base64", sjis, "\U000065E5\U0000672C\U00008A9E"},
		{"charset label ignores case", "text/html", "UTF-8", "7bit", "caf\xc3\xa9", "caf\U000000E9"},
		{"us-ascii label keeps utf-8 bytes", "text/plain", "us-ascii", "8bit", "caf\xc3\xa9", "caf\U000000E9"},
		{"invalid utf-8 is repaired", "text/plain", "utf-8", "8bit", "a\xffb", "a\U0000FFFDb"},
		{"no charset", "text/plain", "", "8bit", "plain", "plain"},
		{"empty content type", "", "iso-8859-1", "", "caf\xe9", "caf\U000000E9"},
		{"encoding ignores case", "text/plain", "utf-8", "BASE64", "SGVsbG8=", "Hello"},
		{"base64 with spaces and lines", "text/plain", "utf-8", "base64", "SGVs\r\nbG8g d29y\tbGQ=\r\n", "Hello world"},
		{"base64 without padding", "text/plain", "utf-8", "base64", "SGVsbG8", "Hello"},
		{"quoted-printable soft break", "text/plain", "utf-8", "Quoted-Printable", "a=\r\nb=3Dc", "ab=c"},
		{"unknown encoding means none", "text/plain", "utf-8", "x-uuencode", "abc", "abc"},
		{"binary encoding", "text/plain", "utf-8", "binary", "abc", "abc"},
		{"charset is only for text types", "application/json", "iso-8859-1", "8bit", "\xc3\xa9", "\U000000E9"},
		{"empty body", "text/plain", "utf-8", "base64", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeText(tc.contentType, params("charset", tc.charset), tc.encoding, strings.NewReader(tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDecodeTextNilParams(t *testing.T) {
	got, err := DecodeText("text/plain", nil, "", strings.NewReader("hi"))
	require.NoError(t, err)
	require.Equal(t, "hi", got)
}

func TestDecodeTextBadTransferEncodingKeepsDecodedPart(t *testing.T) {
	// "!!!!" is not base64. The text before it is kept, and there is no error.
	got, err := DecodeText("text/plain", params("charset", "utf-8"), "base64", strings.NewReader("SGVsbG8gd29y!!!!bGQ="))
	require.NoError(t, err)
	require.Equal(t, "Hello wor", got)

	// The same with a charset that needs conversion.
	good := base64.StdEncoding.EncodeToString([]byte("caf\xe9s!"))
	got, err = DecodeText("text/plain", params("charset", "iso-8859-1"), "base64", strings.NewReader(good+"!!!!"))
	require.NoError(t, err)
	require.Equal(t, "caf\U000000E9s!", got)
}

func TestDecodeTextReadFailureReturnsTextAndError(t *testing.T) {
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("partial text"), iotest.ErrReader(boom))
	got, err := DecodeText("text/plain", nil, "", r)
	require.ErrorIs(t, err, boom)
	require.Equal(t, "partial text", got)
}

func TestDecodeTextTruncatedInputIsNotAnError(t *testing.T) {
	// A part that stops early reports io.ErrUnexpectedEOF. That is damaged
	// mail, not a failure of the reader.
	r := io.MultiReader(strings.NewReader("cut off"), iotest.ErrReader(io.ErrUnexpectedEOF))
	got, err := DecodeText("text/plain", nil, "", r)
	require.NoError(t, err)
	require.Equal(t, "cut off", got)
}

func TestDecodeHeaderValue(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"base64 word", "=?UTF-8?B?w5xiZXI=?=", "\U000000DCber"},
		{"q word in latin-1", "=?ISO-8859-1?Q?caf=E9?=", "caf\U000000E9"},
		{"word between plain text", "Re: =?UTF-8?Q?caf=C3=A9?= and more", "Re: caf\U000000E9 and more"},
		{"charset ignores case", "=?iso-8859-1?q?caf=e9?=", "caf\U000000E9"},
		{"shift_jis word", "=?Shift_JIS?B?k/qWe4zq?=", "\U000065E5\U0000672C\U00008A9E"},
		{"plain ascii", "Hello", "Hello"},
		{"empty", "", ""},
		{"invalid utf-8 is repaired", "caf\xe9", "caf\U0000FFFD"},
		{"bad base64 word stays as it is", "=?UTF-8?B?!!!!?=", "=?UTF-8?B?!!!!?="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, DecodeHeaderValue(tc.in))
		})
	}
}

func TestDecodeHeaderValueUnknownCharsetReturnsRawValue(t *testing.T) {
	in := "=?x-unknown?Q?abc?= tail"
	require.Equal(t, in, DecodeHeaderValue(in))
}

func TestDecodeHeaderValueInvalidUTF8InWord(t *testing.T) {
	// The word says UTF-8 but holds a lone 0xE9 byte.
	got := DecodeHeaderValue("=?UTF-8?Q?caf=E9?=")
	require.True(t, utf8.ValidString(got))
	require.Contains(t, got, "caf")
}

// ---------------------------------------------------------------------------
// ParseMessage

func TestParseSinglePartMessage(t *testing.T) {
	raw := msg(
		"From: a@example.test",
		"Content-Type: text/plain; charset=iso-8859-1",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"caf=E9 time",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "caf\U000000E9 time", p.Text)
	require.Empty(t, p.HTML)
	require.Empty(t, p.Attachments)
	require.False(t, p.HasInvite)
}

func TestParseWithoutContentTypeIsPlainText(t *testing.T) {
	p, err := ParseMessage(strings.NewReader(msg("Subject: x", "", "hello")), 0)
	require.NoError(t, err)
	require.Equal(t, "hello", p.Text)
}

func TestParseAlternativeKeepsFirstOfEachKind(t *testing.T) {
	raw := msg(
		"Content-Type: multipart/alternative; boundary=\"alt\"",
		"",
		"preamble that readers skip",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain one",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain two",
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"<p>caf=C3=A9</p>",
		"--alt",
		"Content-Type: text/html",
		"",
		"<p>second html</p>",
		"--alt--",
		"epilogue",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "plain one", p.Text)
	require.Equal(t, "<p>caf\U000000E9</p>", p.HTML)
	require.Empty(t, p.Attachments)
}

func TestBrokenMultipartStillReturnsText(t *testing.T) {
	raw := msg(
		"Content-Type: multipart/mixed; boundary=\"b1\"",
		"",
		"--b1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"first part",
		"--b1",
		"Content-Type: text/plain",
		"",
		"second part, never closed",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Contains(t, p.Text, "first part")
}

func TestParseUnclosedLastPartKeepsItsText(t *testing.T) {
	raw := msg(
		"Content-Type: multipart/mixed; boundary=b1",
		"",
		"--b1",
		"Content-Type: text/plain",
		"Content-Transfer-Encoding: base64",
		"",
		"SGVsbG8gd29ybGQ=",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "Hello world", p.Text)
}

func TestParseBrokenStructuresDoNotFail(t *testing.T) {
	tests := map[string]string{
		"empty input":           "",
		"headers only":          "Content-Type: text/plain",
		"multipart no boundary": msg("Content-Type: multipart/mixed", "", "--x", "", "text"),
		"boundary never seen":   msg("Content-Type: multipart/mixed; boundary=b", "", "no boundary lines here"),
		"empty boundary":        msg("Content-Type: multipart/mixed; boundary=\"\"", "", "x"),
		"bad part header":       msg("Content-Type: multipart/mixed; boundary=b", "", "--b", "this line has no colon", "", "body", "--b--"),
		"binary noise":          "\x00\x01\x02\xff\xfe\r\n\r\n\x80\x81",
		"bad content type":      msg("Content-Type: ;;;==", "", "body"),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			p, err := ParseMessage(strings.NewReader(raw), 0)
			require.NoError(t, err)
			require.NotNil(t, p)
		})
	}
}

func TestParseMalformedHeaderLineIsSkipped(t *testing.T) {
	// A mbox "From " line and a line without a colon in the header block.
	raw := msg(
		"From sender@example.test Sat Jan  1 00:00:00 2000",
		"Subject: x",
		"this line is not a header",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>body</p>",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "<p>body</p>", p.HTML)
	require.Empty(t, p.Text)
}

func TestParseDamagedContentTypeParameters(t *testing.T) {
	// An unquoted boundary with "=" in it is not valid, but mailers send it.
	raw := msg(
		"Content-Type: multipart/mixed; boundary=----=_Part_1",
		"",
		"------=_Part_1",
		"Content-Type: text/plain",
		"",
		"first",
		"------=_Part_1--",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "first", p.Text)

	// A missing semicolon between two parameters.
	raw = msg(
		"Content-Type: text/plain; charset=iso-8859-1 format=flowed",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"caf=E9",
	)
	p, err = ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "caf\U000000E9", p.Text)

	// The method of an invite is found when another parameter is damaged.
	raw = msg("Content-Type: text/calendar; method=REQUEST; broken parameter", "", "BEGIN:VCALENDAR")
	p, err = ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.True(t, p.HasInvite)
}

func TestParseHeaderThatNeverEndsIsBounded(t *testing.T) {
	in := "Subject: " + strings.Repeat("x", 3<<20)
	p, err := ParseMessage(strings.NewReader(in), 0)
	require.NoError(t, err)
	// The header is read up to its limit. The rest is taken as the body.
	require.Less(t, len(p.Text), len(in))
}

func TestParseNestedPartIDsAndAttachments(t *testing.T) {
	raw := msg(
		"Content-Type: multipart/mixed; boundary=outer",
		"",
		"--outer",
		"Content-Type: multipart/alternative; boundary=inner",
		"",
		"--inner",
		"Content-Type: text/plain",
		"",
		"the text",
		"--inner",
		"Content-Type: text/html",
		"",
		"<p>the html</p>",
		"--inner",
		"Content-Type: IMAGE/PNG; name=\"logo.png\"",
		"Content-Transfer-Encoding: base64",
		"",
		"SGVsbG8=",
		"--inner--",
		"--outer",
		"Content-Type: application/pdf",
		"Content-Disposition: attachment; filename=\"=?UTF-8?B?w5xiZXIucGRm?=\"",
		"Content-Transfer-Encoding: base64",
		"",
		"SGVsbG8gd29y",
		"bGQ=",
		"--outer--",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "the text", p.Text)
	require.Equal(t, "<p>the html</p>", p.HTML)
	require.Equal(t, []mail.AttachmentInfo{
		{ID: "1.3", Filename: "logo.png", ContentType: "image/png", Size: 5},
		{ID: "2", Filename: "\U000000DCber.pdf", ContentType: "application/pdf", Size: 11},
	}, p.Attachments)
}

func TestParseNonMultipartAttachmentIsPartOne(t *testing.T) {
	raw := msg(
		"Content-Type: application/pdf; name=report.pdf",
		"Content-Transfer-Encoding: base64",
		"",
		"SGVsbG8gd29ybGQ=",
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Empty(t, p.Text)
	require.Equal(t, []mail.AttachmentInfo{
		{ID: "1", Filename: "report.pdf", ContentType: "application/pdf", Size: 11},
	}, p.Attachments)
}

func TestParseAttachmentRules(t *testing.T) {
	body := func(parts ...string) string {
		lines := []string{"Content-Type: multipart/mixed; boundary=b", ""}
		for _, part := range parts {
			lines = append(lines, "--b", part)
		}
		lines = append(lines, "--b--")
		return msg(lines...)
	}
	tests := []struct {
		name      string
		raw       string
		wantText  string
		wantFiles []string // "id:filename:type"
	}{
		{
			name: "attachment disposition is never the body",
			raw: body(msg("Content-Type: text/plain", "Content-Disposition: attachment; filename=a.txt", "", "file text"),
				msg("Content-Type: text/plain", "", "real body")),
			wantText:  "real body",
			wantFiles: []string{"1:a.txt:text/plain"},
		},
		{
			name: "text part with a filename after the body is an attachment",
			raw: body(msg("Content-Type: text/plain", "", "real body"),
				msg("Content-Type: text/plain; name=notes.txt", "Content-Disposition: inline; filename=notes.txt", "", "notes")),
			wantText:  "real body",
			wantFiles: []string{"2:notes.txt:text/plain"},
		},
		{
			name:      "only text part with a filename is the body",
			raw:       body(msg("Content-Type: text/plain", "Content-Disposition: inline; filename=patch.txt", "", "diff text")),
			wantText:  "diff text",
			wantFiles: nil,
		},
		{
			name: "second plain part without a filename is dropped",
			raw: body(msg("Content-Type: text/plain", "", "one"),
				msg("Content-Type: text/plain", "", "two")),
			wantText:  "one",
			wantFiles: nil,
		},
		{
			name:      "other text type with a filename is an attachment",
			raw:       body(msg("Content-Type: text/csv; name=data.csv", "", "a,b")),
			wantFiles: []string{"1:data.csv:text/csv"},
		},
		{
			name:      "non-text part without a disposition is an attachment",
			raw:       body(msg("Content-Type: image/gif", "", "GIF89a")),
			wantFiles: []string{"1::image/gif"},
		},
		{
			name:      "forwarded message is an attachment",
			raw:       body(msg("Content-Type: message/rfc822", "", "Subject: inner", "", "inner body")),
			wantFiles: []string{"1::message/rfc822"},
		},
		{
			name:      "invalid content type becomes octet-stream",
			raw:       body(msg("Content-Type: bogus junk", "Content-Disposition: attachment", "", "data")),
			wantFiles: []string{"1::application/octet-stream"},
		},
		{
			name:      "content type with a bad parameter keeps the media type",
			raw:       body(msg("Content-Type: image/png; broken parameter", "", "data")),
			wantFiles: []string{"1::image/png"},
		},
		{
			name:      "hostile content type is not passed on",
			raw:       body(msg("Content-Type: ignore/previous<instructions>", "", "data")),
			wantFiles: []string{"1::application/octet-stream"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseMessage(strings.NewReader(tc.raw), 0)
			require.NoError(t, err)
			require.Equal(t, tc.wantText, p.Text)
			var got []string
			for _, a := range p.Attachments {
				got = append(got, fmt.Sprintf("%s:%s:%s", a.ID, a.Filename, a.ContentType))
			}
			require.Equal(t, tc.wantFiles, got)
		})
	}
}

func TestParseAttachmentFilenames(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
		contentType string
		want        string
	}{
		{"plain", `attachment; filename="plain.txt"`, "application/octet-stream", "plain.txt"},
		{"rfc 2047 word", `attachment; filename="=?UTF-8?B?w5xiZXIucGRm?="`, "application/octet-stream", "\U000000DCber.pdf"},
		{"rfc 2231 utf-8", `attachment; filename*=UTF-8''%C3%9Cber%20Plan.pdf`, "application/octet-stream", "\U000000DCber Plan.pdf"},
		{"rfc 2231 continuations", `attachment; filename*0*=UTF-8''%C3%9Cber; filename*1=".pdf"`, "application/octet-stream", "\U000000DCber.pdf"},
		{"name in content type", `attachment`, `application/octet-stream; name="from-type.bin"`, "from-type.bin"},
		{"disposition wins over content type", `attachment; filename=a.bin`, `application/octet-stream; name="b.bin"`, "a.bin"},
		{"invisible characters removed", "attachment; filename=\"a\U0000200Bb.txt\"", "application/octet-stream", "ab.txt"},
		{"bidi override removed from rfc 2231 name", `attachment; filename*=UTF-8''gp%E2%80%AEexe.txt`, "application/octet-stream", "gpexe.txt"},
		{"control characters removed", "attachment; filename=\"a\x01b.txt\"", "application/octet-stream", "ab.txt"},
		{"no name", `attachment`, "application/octet-stream", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := msg(
				"Content-Type: "+tc.contentType,
				"Content-Disposition: "+tc.disposition,
				"Content-Transfer-Encoding: base64",
				"",
				"SGVsbG8=",
			)
			p, err := ParseMessage(strings.NewReader(raw), 0)
			require.NoError(t, err)
			require.Len(t, p.Attachments, 1)
			require.Equal(t, tc.want, p.Attachments[0].Filename)
			require.Equal(t, int64(5), p.Attachments[0].Size)
		})
	}
}

func TestParseAttachmentSizeIsDecodedSize(t *testing.T) {
	data := strings.Repeat("0123456789", 1000)
	encoded := base64.StdEncoding.EncodeToString([]byte(data))
	var wrapped []string
	for len(encoded) > 76 {
		wrapped = append(wrapped, encoded[:76])
		encoded = encoded[76:]
	}
	wrapped = append(wrapped, encoded)

	lines := append([]string{"Content-Type: application/octet-stream", "Content-Transfer-Encoding: base64", ""}, wrapped...)
	p, err := ParseMessage(strings.NewReader(msg(lines...)), 0)
	require.NoError(t, err)
	require.Len(t, p.Attachments, 1)
	require.Equal(t, int64(len(data)), p.Attachments[0].Size)

	// Quoted-printable counts the decoded bytes too.
	raw := msg(
		"Content-Type: application/x-thing",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"a=3Db=C3=A9",
	)
	p, err = ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Len(t, p.Attachments, 1)
	require.Equal(t, int64(5), p.Attachments[0].Size) // "a=b" and two bytes for e-acute
}

func TestParseBodyLargerThanTheHeaderLimit(t *testing.T) {
	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = byte(i)
	}
	raw := msg(
		"Content-Type: application/octet-stream",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString(data),
	)
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Len(t, p.Attachments, 1)
	require.Equal(t, int64(len(data)), p.Attachments[0].Size)
}

func TestParseCutsTextToMaxRunes(t *testing.T) {
	raw := msg(
		"Content-Type: multipart/alternative; boundary=b",
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"h\xc3\xa9llo w\xc3\xb6rld",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>h\xc3\xa9llo</p>",
		"--b--",
	)
	p, err := ParseMessage(strings.NewReader(raw), 5)
	require.NoError(t, err)
	require.Equal(t, "h\U000000E9llo", p.Text)
	require.Equal(t, "<p>h\U000000E9", p.HTML)

	for _, limit := range []int{0, -1} {
		p, err = ParseMessage(strings.NewReader(raw), limit)
		require.NoError(t, err)
		require.Equal(t, "h\U000000E9llo w\U000000F6rld", p.Text)
		require.Equal(t, "<p>h\U000000E9llo</p>", p.HTML)
	}

	// A limit that is larger than the text changes nothing.
	p, err = ParseMessage(strings.NewReader(raw), 1000)
	require.NoError(t, err)
	require.Equal(t, "h\U000000E9llo w\U000000F6rld", p.Text)
}

// countingReader counts the bytes that are read from it.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestParseReadsOnlyWhatMaxTextNeeds(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 1<<18) // 4 MiB
	for _, enc := range []string{"", "base64"} {
		body := big
		if enc == "base64" {
			body = base64.StdEncoding.EncodeToString([]byte(big))
		}
		raw := msg("Content-Type: text/plain; charset=utf-8", "Content-Transfer-Encoding: "+enc, "", body)
		src := &countingReader{r: strings.NewReader(raw)}
		p, err := ParseMessage(src, 100)
		require.NoError(t, err)
		require.Equal(t, big[:100], p.Text)
		require.Less(t, src.n, 64<<10, "encoding %q: the whole body was read", enc)
	}
}

func TestParseCutsFourByteRunesAndLargeLimits(t *testing.T) {
	grin := "\U0001F600"
	raw := msg("Content-Type: text/plain; charset=utf-8", "", strings.Repeat(grin, 10))
	p, err := ParseMessage(strings.NewReader(raw), 3)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat(grin, 3), p.Text)

	// The limit is multiplied inside. A huge value must not overflow.
	p, err = ParseMessage(strings.NewReader(raw), int(^uint(0)>>1))
	require.NoError(t, err)
	require.Equal(t, strings.Repeat(grin, 10), p.Text)
}

func TestParseCutsRepairedText(t *testing.T) {
	// Each bad byte becomes one U+FFFD, and the cut counts runes after that.
	raw := msg("Content-Type: text/plain; charset=utf-8", "", "\xff\xfe\xfd\xfc\xfb\xfa")
	p, err := ParseMessage(strings.NewReader(raw), 4)
	require.NoError(t, err)
	require.LessOrEqual(t, utf8.RuneCountInString(p.Text), 4)
	require.True(t, utf8.ValidString(p.Text))
}

func TestParseDetectsInvite(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "calendar part in alternative",
			raw: msg("Content-Type: multipart/alternative; boundary=b", "",
				"--b", "Content-Type: text/plain", "", "Meeting",
				"--b", "Content-Type: text/calendar; charset=utf-8; method=REQUEST", "", "BEGIN:VCALENDAR",
				"--b--"),
			want: true,
		},
		{
			name: "calendar type without a method",
			raw:  msg("Content-Type: text/calendar", "", "BEGIN:VCALENDAR"),
			want: true,
		},
		{
			name: "method parameter on another type",
			raw: msg("Content-Type: multipart/mixed; boundary=b", "",
				"--b", "Content-Type: application/ics; method=REQUEST; name=invite.ics", "", "BEGIN:VCALENDAR",
				"--b--"),
			want: true,
		},
		{
			name: "calendar attached deep inside",
			raw: msg("Content-Type: multipart/mixed; boundary=a", "",
				"--a", "Content-Type: multipart/alternative; boundary=b", "",
				"--b", "Content-Type: text/plain", "", "x",
				"--b", "Content-Type: text/calendar", "", "BEGIN:VCALENDAR",
				"--b--", "--a--"),
			want: true,
		},
		{
			name: "plain mail",
			raw:  msg("Content-Type: text/plain", "", "no invite"),
			want: false,
		},
		{
			name: "calendar word in the text only",
			raw:  msg("Content-Type: text/plain; charset=utf-8", "", "text/calendar; method=REQUEST"),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseMessage(strings.NewReader(tc.raw), 0)
			require.NoError(t, err)
			require.Equal(t, tc.want, p.HasInvite)
		})
	}
}

func TestParseCalendarPartWithFilenameIsListed(t *testing.T) {
	raw := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b", "Content-Type: text/plain", "", "see invite",
		"--b", "Content-Type: text/calendar; name=invite.ics; method=REQUEST", "", "BEGIN:VCALENDAR",
		"--b--")
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.True(t, p.HasInvite)
	require.Equal(t, "see invite", p.Text)
	require.Equal(t, []mail.AttachmentInfo{
		{ID: "2", Filename: "invite.ics", ContentType: "text/calendar", Size: 15},
	}, p.Attachments)
}

func TestParseKeepsWorkingAfterUnknownCharsetAndEncoding(t *testing.T) {
	raw := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b", "Content-Type: text/plain; charset=x-unknown", "Content-Transfer-Encoding: x-weird", "", "caf\xe9",
		"--b", "Content-Type: text/html; charset=utf-8", "", "<p>ok</p>",
		"--b--")
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "caf\U0000FFFD", p.Text)
	require.Equal(t, "<p>ok</p>", p.HTML)
}

func TestParseBadBase64KeepsTextSoFar(t *testing.T) {
	raw := msg("Content-Type: text/plain", "Content-Transfer-Encoding: base64", "", "SGVsbG8gd29y!!!!bGQ=")
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "Hello wor", p.Text)
}

// nestedMultipart builds a message with depth multipart levels. The only text
// part is at the bottom.
func nestedMultipart(depth int) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\ndeep text")
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "\r\n--b%d--\r\n", i)
	}
	return b.String()
}

func TestParseNestingIsBounded(t *testing.T) {
	p, err := ParseMessage(strings.NewReader(nestedMultipart(10)), 0)
	require.NoError(t, err)
	require.Equal(t, "deep text", p.Text)

	// Far deeper than any real mail. The parser must stop going down, and
	// must not panic.
	p, recovered, err := parseMessage(strings.NewReader(nestedMultipart(5000)), 0)
	require.NoError(t, err)
	require.Nil(t, recovered)
	require.NotNil(t, p)
	require.Empty(t, p.Text)
}

func TestParseStopsAfterMaxParts(t *testing.T) {
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=b\r\n\r\n")
	for i := 0; i < maxParts+500; i++ {
		b.WriteString("--b\r\nContent-Type: image/png\r\n\r\nx\r\n")
	}
	b.WriteString("--b--\r\n")

	p, err := ParseMessage(strings.NewReader(b.String()), 0)
	require.NoError(t, err)
	require.Len(t, p.Attachments, maxParts)
	require.Equal(t, fmt.Sprint(maxParts), p.Attachments[maxParts-1].ID)
}

func TestParseReadFailureReturnsPartAndError(t *testing.T) {
	boom := errors.New("connection reset")
	head := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b", "Content-Type: text/plain", "", "first part",
		"--b", "Content-Type: text/html", "", "<p>cut")
	p, err := ParseMessage(io.MultiReader(strings.NewReader(head), iotest.ErrReader(boom)), 0)
	require.ErrorIs(t, err, boom)
	require.NotNil(t, p)
	require.Equal(t, "first part", p.Text)
}

func TestParseReadFailureInHeader(t *testing.T) {
	boom := errors.New("connection reset")
	p, err := ParseMessage(io.MultiReader(strings.NewReader("Subject: x\r\n"), iotest.ErrReader(boom)), 0)
	require.ErrorIs(t, err, boom)
	require.NotNil(t, p)
}

// panicReader panics on every read. It stands for a library bug.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom") }

func TestParseRecoversFromPanic(t *testing.T) {
	p, err := ParseMessage(panicReader{}, 0)
	require.NoError(t, err)
	require.NotNil(t, p)

	// What was decoded before the panic is kept.
	head := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b", "Content-Type: text/plain", "", "first part",
		"--b", "")
	r := io.MultiReader(strings.NewReader(head), panicReader{})
	p, recovered, err := parseMessage(r, 0)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, "first part", p.Text)
}

func TestParsedStringsAreValidUTF8(t *testing.T) {
	raw := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b", "Content-Type: text/plain; charset=utf-8", "", "bad \xff\xfe text",
		"--b", "Content-Type: application/x-\xff; name=\"bad\xff.bin\"", "", "x",
		"--b--")
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.True(t, utf8.ValidString(p.Text))
	for _, a := range p.Attachments {
		require.True(t, utf8.ValidString(a.Filename))
		require.True(t, utf8.ValidString(a.ContentType))
	}
}

// ---------------------------------------------------------------------------
// Fix round 1

// allocated returns how many bytes f allocates in total. This is an upper
// bound of the heap that f needs at one time.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A part header that is far too big must not use much memory, and the parts
// after it must still be read.
func TestParsePartHeaderSizeIsBounded(t *testing.T) {
	headers := map[string]string{
		"millions of tiny fields": strings.Repeat("X: y\r\n", 3_000_000),
		"one huge line":           "X: " + strings.Repeat("a", 32<<20) + "\r\n",
		"huge folded field":       "X: a\r\n" + strings.Repeat(" b\r\n", 3_000_000),
	}
	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
				"--b\r\nContent-Type: text/plain\r\n\r\nfirst\r\n" +
				"--b\r\n" + header + "\r\nignored body\r\n" +
				"--b\r\nContent-Type: text/html\r\n\r\n<p>after</p>\r\n" +
				"--b\r\nContent-Type: image/png\r\n\r\nxyz\r\n" +
				"--b--\r\n"

			var p *Parsed
			var err error
			grown := allocated(func() { p, err = ParseMessage(strings.NewReader(raw), 0) })

			require.NoError(t, err)
			require.Less(t, grown, uint64(24<<20), "the part header used too much memory")
			require.Equal(t, "first", p.Text)
			require.Equal(t, "<p>after</p>", p.HTML)
			// The part with the big header is skipped, but it still has a
			// place in the numbering.
			require.Equal(t, []mail.AttachmentInfo{
				{ID: "4", ContentType: "image/png", Size: 3},
			}, p.Attachments)
		})
	}
}

func TestParseMessageHeaderSizeIsBounded(t *testing.T) {
	headers := map[string]string{
		"millions of tiny fields": strings.Repeat("X: y\r\n", 3_000_000),
		"one huge line":           "X: " + strings.Repeat("a", 32<<20) + "\r\n",
	}
	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			raw := header + "\r\nbody text"
			var err error
			grown := allocated(func() { _, err = ParseMessage(strings.NewReader(raw), 100) })
			require.NoError(t, err)
			require.Less(t, grown, uint64(24<<20), "the header used too much memory")
		})
	}
}

func TestParsePartHeaderFieldLimit(t *testing.T) {
	build := func(fields int) string {
		return "Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
			"--b\r\nContent-Type: image/png\r\n" + strings.Repeat("X: y\r\n", fields-1) + "\r\nabc\r\n" +
			"--b--\r\n"
	}
	p, err := ParseMessage(strings.NewReader(build(maxHeaderFields)), 0)
	require.NoError(t, err)
	require.Len(t, p.Attachments, 1)

	p, err = ParseMessage(strings.NewReader(build(maxHeaderFields+1)), 0)
	require.NoError(t, err)
	require.Empty(t, p.Attachments)
}

func TestParseBadLineInPartHeaderKeepsLaterParts(t *testing.T) {
	raw := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b",
		"Content-Type: text/plain",
		"this line has no colon",
		"Content-Transfer-Encoding: base64",
		"",
		"SGVsbG8=",
		"--b",
		"Content-Type: text/html",
		"",
		"<p>second</p>",
		"--b--")
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	// The field after the bad line is still used.
	require.Equal(t, "Hello", p.Text)
	require.Equal(t, "<p>second</p>", p.HTML)
}

func TestDecodeTextLongQuotedPrintableLines(t *testing.T) {
	for _, n := range []int{5000, 200000} {
		for name, tc := range map[string]struct{ in, want string }{
			"plain":   {strings.Repeat("a", n), strings.Repeat("a", n)},
			"escapes": {strings.Repeat("a=3D", n/4), strings.Repeat("a=", n/4)},
		} {
			got, err := DecodeText("text/plain", nil, "quoted-printable", strings.NewReader(tc.in))
			require.NoError(t, err)
			require.Equal(t, len(tc.want), len(got), "%s, %d bytes", name, n)
			require.True(t, got == tc.want, "%s, %d bytes: text differs", name, n)
		}
	}
}

func TestParseLongQuotedPrintableLines(t *testing.T) {
	line := strings.Repeat("ab=3D", 40000) // 200,000 bytes
	want := strings.Repeat("ab=", 40000)   // 120,000 bytes
	qp := []string{"Content-Transfer-Encoding: quoted-printable", ""}

	single := msg(append([]string{"Content-Type: text/plain; charset=utf-8"}, append(qp, line)...)...)
	p, err := ParseMessage(strings.NewReader(single), 0)
	require.NoError(t, err)
	require.True(t, p.Text == want, "single part text differs (%d bytes)", len(p.Text))

	multi := msg("Content-Type: multipart/mixed; boundary=b", "",
		"--b", "Content-Type: text/plain", qp[0], "", line,
		"--b", "Content-Type: application/x-thing", qp[0], "", line,
		"--b--")
	p, err = ParseMessage(strings.NewReader(multi), 0)
	require.NoError(t, err)
	require.True(t, p.Text == want, "multipart text differs (%d bytes)", len(p.Text))
	require.Len(t, p.Attachments, 1)
	require.Equal(t, int64(len(want)), p.Attachments[0].Size)

	// A non-multipart attachment.
	raw := msg("Content-Type: application/x-thing", qp[0], "", line)
	p, err = ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Len(t, p.Attachments, 1)
	require.Equal(t, int64(len(want)), p.Attachments[0].Size)
}

func TestParseRecoversDamagedFilenames(t *testing.T) {
	arabic := "\U00000645\U00000644\U00000641" // three Arabic letters
	tests := []struct {
		name        string
		disposition string
		contentType string
		want        string
	}{
		{"rfc 2231 latin-1", `attachment; filename*=iso-8859-1''caf%E9.pdf`, "application/pdf", "caf\U000000E9.pdf"},
		{
			"rfc 2231 windows-1256 in two segments",
			`attachment; filename*0*=windows-1256''%C7%E1; filename*1*=%CA%DE.pdf`,
			"application/pdf",
			"\U00000627\U00000644\U0000062A\U00000642.pdf",
		},
		{"rfc 2231 with a language", `attachment; filename*=windows-1256'ar'%C7%E1.pdf`, "application/pdf", "\U00000627\U00000644.pdf"},
		{"rfc 2231 unknown charset", `attachment; filename*=x-unknown''abc%FFdef.txt`, "application/pdf", "abc\U0000FFFDdef.txt"},
		{"unquoted with a space", `attachment; filename=my report.pdf`, "application/pdf", "my report.pdf"},
		{"unquoted with several spaces and a later parameter", `attachment; filename=my  big report.pdf ; size=10`, "application/pdf", "my  big report.pdf"},
		{"unquoted raw utf-8", "attachment; filename=" + arabic + ".pdf", "application/pdf", arabic + ".pdf"},
		{"unquoted raw utf-8 with a space", "attachment; filename=" + arabic + " 2.pdf", "application/pdf", arabic + " 2.pdf"},
		{"unquoted rfc 2047 word", `attachment; filename==?UTF-8?B?w5xiZXIucGRm?=`, "application/pdf", "\U000000DCber.pdf"},
		{"quoted with a semicolon and a bad parameter", `attachment; filename="a;b c.pdf"; bad parameter`, "application/pdf", "a;b c.pdf"},
		{"quoted with an escaped quote and a bad parameter", `attachment; filename="a\"b.pdf"; bad parameter`, "application/pdf", `a"b.pdf`},
		{"name in content type, unquoted with a space", `attachment`, `application/pdf; name=my report.pdf`, "my report.pdf"},
		{"name in content type, rfc 2231", `attachment`, `application/pdf; name*=iso-8859-1''caf%E9.pdf`, "caf\U000000E9.pdf"},
		{"invisible characters are removed", "attachment; filename=a\U0000200Bb c.txt", "application/pdf", "ab c.txt"},
		// Names that the standard parser reads must not change.
		{"quoted name", `attachment; filename="plain name.pdf"`, "application/pdf", "plain name.pdf"},
		{"rfc 2231 utf-8", `attachment; filename*=UTF-8''%C3%9Cber%20Plan.pdf`, "application/pdf", "\U000000DCber Plan.pdf"},
		{"rfc 2231 utf-8 in two segments", `attachment; filename*0*=UTF-8''%C3%9Cber; filename*1=".pdf"`, "application/pdf", "\U000000DCber.pdf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := msg(
				"Content-Type: "+tc.contentType,
				"Content-Disposition: "+tc.disposition,
				"Content-Transfer-Encoding: base64",
				"",
				"SGVsbG8=",
			)
			p, err := ParseMessage(strings.NewReader(raw), 0)
			require.NoError(t, err)
			require.Len(t, p.Attachments, 1)
			require.Equal(t, tc.want, p.Attachments[0].Filename)
		})
	}
}

// ---------------------------------------------------------------------------
// Fix round 2

// A part header that runs into a boundary line ends there. The next part must
// not be read as a part of this header.
func TestParsePartHeaderStopsAtABoundary(t *testing.T) {
	t.Run("text header then html part", func(t *testing.T) {
		raw := msg("Content-Type: multipart/alternative; boundary=b", "",
			"--b", "Content-Type: text/plain",
			"--b", "Content-Type: text/html", "", "<p>real html</p>",
			"--b--")
		p, err := ParseMessage(strings.NewReader(raw), 0)
		require.NoError(t, err)
		require.Equal(t, "<p>real html</p>", p.HTML)
		require.Empty(t, p.Text)
		require.Empty(t, p.Attachments)
	})
	t.Run("attachment header then text part", func(t *testing.T) {
		raw := msg("Content-Type: multipart/mixed; boundary=b", "",
			"--b", "Content-Type: image/png", "Content-Disposition: attachment; filename=x.png",
			"--b", "Content-Type: text/plain", "", "real text",
			"--b--")
		p, err := ParseMessage(strings.NewReader(raw), 0)
		require.NoError(t, err)
		require.Equal(t, "real text", p.Text)
		require.Equal(t, []mail.AttachmentInfo{
			{ID: "1", Filename: "x.png", ContentType: "image/png", Size: 0},
		}, p.Attachments)
	})
	t.Run("base64 header then plain part", func(t *testing.T) {
		// The next part is not base64. Its text must stay as it is.
		raw := msg("Content-Type: multipart/mixed; boundary=b", "",
			"--b", "Content-Type: text/plain", "Content-Transfer-Encoding: base64",
			"--b", "Content-Type: text/plain", "", "aGlkZGVu",
			"--b--")
		p, err := ParseMessage(strings.NewReader(raw), 0)
		require.NoError(t, err)
		require.Equal(t, "aGlkZGVu", p.Text)
	})
	t.Run("boundary right after a boundary keeps the numbers", func(t *testing.T) {
		raw := msg("Content-Type: multipart/mixed; boundary=b", "",
			"--b",
			"--b", "Content-Type: image/png", "", "xyz",
			"--b", "Content-Type: image/gif", "", "abcd",
			"--b--")
		p, err := ParseMessage(strings.NewReader(raw), 0)
		require.NoError(t, err)
		require.Equal(t, []mail.AttachmentInfo{
			{ID: "2", ContentType: "image/png", Size: 3},
			{ID: "3", ContentType: "image/gif", Size: 4},
		}, p.Attachments)
	})
	t.Run("header ends at the closing boundary", func(t *testing.T) {
		raw := msg("Content-Type: multipart/mixed; boundary=b", "",
			"--b", "Content-Type: text/plain", "", "text",
			"--b", "Content-Type: image/png",
			"--b--",
			"--b", "Content-Type: image/gif", "", "not a part")
		p, err := ParseMessage(strings.NewReader(raw), 0)
		require.NoError(t, err)
		require.Equal(t, "text", p.Text)
		require.Equal(t, []mail.AttachmentInfo{
			{ID: "2", ContentType: "image/png", Size: 0},
		}, p.Attachments)
	})
	t.Run("nested multipart", func(t *testing.T) {
		raw := msg("Content-Type: multipart/mixed; boundary=a", "",
			"--a", "Content-Type: multipart/alternative; boundary=b", "",
			"--b", "Content-Type: text/plain",
			"--b", "Content-Type: text/html", "", "<p>x</p>",
			"--b--",
			"--a", "Content-Type: image/png", "", "xyz",
			"--a--")
		p, err := ParseMessage(strings.NewReader(raw), 0)
		require.NoError(t, err)
		require.Equal(t, "<p>x</p>", p.HTML)
		require.Equal(t, []mail.AttachmentInfo{{ID: "2", ContentType: "image/png", Size: 3}}, p.Attachments)
	})
}

// A part that has no text is not the text of the message: a later part can be.
func TestParseEmptyTextPartDoesNotTakeThePlace(t *testing.T) {
	raw := msg("Content-Type: multipart/alternative; boundary=b", "",
		"--b", "Content-Type: text/plain", "",
		"--b", "Content-Type: text/plain", "", "the text",
		"--b", "Content-Type: text/html", "",
		"--b", "Content-Type: text/html", "", "<p>the html</p>",
		"--b--")
	p, err := ParseMessage(strings.NewReader(raw), 0)
	require.NoError(t, err)
	require.Equal(t, "the text", p.Text)
	require.Equal(t, "<p>the html</p>", p.HTML)
	require.Empty(t, p.Attachments)
}

// nestedBoundaries builds a message with depth multipart levels. The body of
// the innermost part is inner.
func nestedBoundaries(depth int, inner string) string {
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=b0\r\n\r\n")
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&b, "--b%d\r\nContent-Type: multipart/mixed; boundary=b%d\r\n\r\n", i, i+1)
	}
	fmt.Fprintf(&b, "--b%d\r\nContent-Type: application/octet-stream\r\n\r\n", depth)
	b.WriteString(inner)
	return b.String()
}

// Every level of nesting passes the body up. The time must grow with the size
// of the body and the depth, not with the number of lines. This is a time
// test, so it is skipped with -short and with the race detector.
// TestMultipartReaderMovesBodiesInLargeChunks tests the same without a clock.
func TestParseDeeplyNestedBodyOfLineEndingsIsFast(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("time test")
	}
	raw := nestedBoundaries(maxDepth-1, strings.Repeat("\n", 25<<20))
	start := time.Now()
	p, recovered, err := parseMessage(strings.NewReader(raw), 0)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Nil(t, recovered)
	require.Len(t, p.Attachments, 1)
	require.Equal(t, int64(25<<20), p.Attachments[0].Size)
	// About 0.3 s on a laptop. Before the fix, this took 38 s.
	require.Less(t, elapsed, 3*time.Second)
}
