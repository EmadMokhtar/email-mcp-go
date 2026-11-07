package web

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The landing page of the docs site has a drawing of the approval page. The
// drawing is HTML in site/layouts/_partials/approval-picture.html, and the
// real page is templates/approval.html. The docs check of the site does not
// compare the two, so this test does. It requires each label of the drawing in
// both files. If a pull request renames a label of the portal, this test fails
// until the drawing says the same.
const (
	landingPicture = "../../site/layouts/_partials/approval-picture.html"
	portalApproval = "templates/approval.html"
	siteFolder     = "../../site"
)

// landingLabel is one word of the drawing, and where the portal shows it.
type landingLabel struct {
	text string

	// inPortal matches the label as the text of its own element in
	// approval.html. A plain search for the text is not enough: "Reject" is
	// also in data-busy="Rejecting…", "From" is also in {{.From}}, and "Waiting
	// for you" is also in the title and in the back link. A rename of the real
	// label would still pass. So each label is anchored to its element.
	inPortal *regexp.Regexp

	// from is a piece of approval.html that holds the label, and to is the
	// same piece with the label renamed. TestLandingPictureLabelsSeeARename
	// uses them on a copy in memory, as a rename in a pull request would.
	from, to string
}

// inElement returns a pattern for a label between two pieces of the template.
// before and after are regular expressions. The label is matched as text, on
// one line: a label that a template breaks over two lines does not match, and
// the test then fails until the pattern is changed with it.
func inElement(before, label, after string) *regexp.Regexp {
	return regexp.MustCompile(before + regexp.QuoteMeta(label) + after)
}

// landingLabels are the words that the drawing and the portal must share, each
// with the element of approval.html that it is anchored to:
//
//   - "Waiting for you": the h1 of the page.
//   - "Reject": the text of the Reject button.
//   - "Approve and send": the text of the Approve button, for a send.
//   - "From", "To, every recipient (Bcc included)" and "Attachments": a dt.
//   - "goes out" and "until you approve.": the two pieces of the note of the
//     action bar. The note "Nothing goes out until you approve." is built from
//     pieces in the portal template, so the test checks the two pieces and not
//     the whole note.
var landingLabels = []landingLabel{
	{
		text:     "Waiting for you",
		inPortal: inElement(`<h1[^>]*>`, "Waiting for you", `</h1>`),
		from:     `<h1 id="queue-title">Waiting for you</h1>`,
		to:       `<h1 id="queue-title">Your queue</h1>`,
	},
	{
		text:     "Reject",
		inPortal: inElement(`<button[^>]*>`, "Reject", `</button>`),
		from:     `>Reject</button>`,
		to:       `>Decline</button>`,
	},
	{
		text:     "Approve and send",
		inPortal: inElement(`<button[^>]*>\{\{if \.Send\}\}`, "Approve and send", `\{\{else\}\}`),
		from:     `{{if .Send}}Approve and send{{else}}`,
		to:       `{{if .Send}}Send it{{else}}`,
	},
	{
		text:     "From",
		inPortal: inElement(`<dt>`, "From", `</dt>`),
		from:     `<dt>From</dt>`,
		to:       `<dt>Sender</dt>`,
	},
	{
		text:     "To, every recipient (Bcc included)",
		inPortal: inElement(`<dt>`, "To, every recipient (Bcc included)", `</dt>`),
		from:     `<dt>To, every recipient (Bcc included)</dt>`,
		to:       `<dt>To</dt>`,
	},
	{
		text:     "Attachments",
		inPortal: inElement(`<dt>`, "Attachments", `</dt>`),
		from:     `<dt>Attachments</dt>`,
		to:       `<dt>Files</dt>`,
	},
	{
		text:     "until you approve.",
		inPortal: inElement(`<p class="actionbar-note"[^>]*>[^<]* `, "until you approve.", `</p>`),
		from:     ` until you approve.</p>`,
		to:       ` until you say yes.</p>`,
	},
	{
		text:     "goes out",
		inPortal: inElement(`<p class="actionbar-note"[^>]*>Nothing \{\{if \.Send\}\}`, "goes out", `\{\{else\}\}`),
		from:     `{{if .Send}}goes out{{else}}`,
		to:       `{{if .Send}}is sent{{else}}`,
	},
}

// Some text of a template is not shown as a label: the comments of Hugo and Go
// templates ({{/* ... */}}) and of HTML, and the description of the drawing in
// aria-label, which says "Reject" and "Approve and send" in a sentence. The
// value of aria-label may have double or single quotes. A label that is only in
// such text must not count. So this text is removed before the check, and what
// is left is the text that a person sees.
var hiddenText = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}|<!--.*?-->|aria-label=(?:"[^"]*"|'[^']*')`)

func TestLandingPictureLabelsMatchPortal(t *testing.T) {
	// A source archive may have no site/ folder. Then there is no drawing to
	// check. Any other missing file is a failure: with site/ present, the
	// drawing must exist.
	if _, err := os.Stat(siteFolder); errors.Is(err, fs.ErrNotExist) {
		t.Skip("the site/ folder is missing (for example in a source archive), so there is no landing picture to check")
	}

	picture := readVisibleText(t, landingPicture)
	portal := readVisibleText(t, portalApproval)

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			require.Regexp(t, label.inPortal, portal,
				"%s no longer shows %q as the text of its element. If the portal renamed it, change the picture to match. If a template broke the label over two lines, change the pattern of this test.",
				portalApproval, label.text)
			require.Contains(t, picture, label.text,
				"%s does not show %q. The portal has it, so the picture needs it too.", landingPicture, label.text)
		})
	}
}

// A rename of a label in the portal must fail the check of the portal, for each
// label. The test renames the label in a copy of approval.html in memory. The
// real file does not change. This is the proof that the patterns above are not
// satisfied by the same words in another place, such as the title of the page,
// data-busy or a field of the template.
func TestLandingPictureLabelsSeeARename(t *testing.T) {
	portal := readVisibleText(t, portalApproval)

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			require.Regexp(t, label.inPortal, portal, "the pattern must match the real portal first")
			require.Equal(t, 1, strings.Count(portal, label.from),
				"the piece %q must be in approval.html exactly once", label.from)

			renamed := strings.Replace(portal, label.from, label.to, 1)
			require.NotEqual(t, portal, renamed)
			require.NotRegexp(t, label.inPortal, renamed,
				"a portal with %q renamed must fail the check", label.text)
		})
	}
}

// Comments and aria-label values are removed before the check, in both quote
// styles.
func TestLandingPictureLabelsIgnoreHiddenText(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"hugo comment":        {`a{{- /* Reject */ -}}b`, `ab`},
		"go template comment": {`a{{/* Reject */}}b`, `ab`},
		"html comment":        {`a<!-- Reject -->b`, `ab`},
		"double quotes":       {`<div aria-label="Reject, and Thursday's">x</div>`, `<div >x</div>`},
		"single quotes":       {`<div aria-label='Reject, and "Approve"'>x</div>`, `<div >x</div>`},
		"aria-labelledby":     {`<h1 aria-labelledby="Reject">`, `<h1 aria-labelledby="Reject">`},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, hiddenText.ReplaceAllString(tc.in, ""))
		})
	}
}

// readVisibleText returns a template file without its comments and without
// its aria-label values.
func readVisibleText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: the paths are constants of this test
	require.NoError(t, err, "cannot read %s", path)
	return hiddenText.ReplaceAllString(string(data), "")
}
