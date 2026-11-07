package web

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The landing page of the docs site has a drawing of the approval page. The
// drawing is HTML in site/layouts/_partials/approval-picture.html, and the
// real page is templates/approval.html. The docs check of the site does not
// compare the two, so this test does. It requires each label of the drawing in
// both files, each in its own element. If a pull request renames a label of the
// portal, this test fails until the drawing says the same.
const (
	landingPicture = "../../site/layouts/_partials/approval-picture.html"
	portalApproval = "templates/approval.html"
	siteFolder     = "../../site"
)

// landingLabel is one word of the drawing, and where each file shows it.
type landingLabel struct {
	text string

	// inPortal matches the label as the text of its own element in
	// approval.html. A plain search for the text is not enough: "Reject" is
	// also in data-busy="Rejecting…", "From" is also in {{.From}}, and "Waiting
	// for you" is also in the title and in the back link. A rename of the real
	// label would still pass. So each label is anchored to its element.
	inPortal *regexp.Regexp

	// inPicture matches the label as the text of its own element in the
	// drawing. A plain search would still pass if the label moved to another
	// element, such as the caption, and the drawing then no longer shows it in
	// the place of the portal.
	inPicture *regexp.Regexp

	// from is a piece of approval.html that holds the label, and to is the
	// same piece with the label renamed. TestLandingPictureLabelsSeeARename
	// uses them on a copy in memory, as a rename in a pull request would. The
	// pieces start after the start tag, so a new attribute does not break them.
	from, to string
}

// inElement returns a pattern for a label between two pieces of the template.
// before and after are regular expressions. The label is matched as text, on
// one line: a label that a template breaks over two lines does not match, and
// the test then fails until the pattern is changed with it.
func inElement(before, label, after string) *regexp.Regexp {
	return regexp.MustCompile(before + regexp.QuoteMeta(label) + after)
}

// openTag returns a pattern for the start tag of an element with any
// attributes, in any order. With a class, the element must have that class in
// its class attribute, alone or with other classes. So a portal or a drawing
// that only adds an attribute or a class still passes.
func openTag(tag, class string) string {
	if class == "" {
		return `<` + tag + `(?:\s[^>]*)?>`
	}
	return `<` + tag + `(?:\s[^>]*?)?\sclass="(?:[^"]*\s)?` + regexp.QuoteMeta(class) + `(?:\s[^"]*)?"[^>]*>`
}

// landingLabels are the words that the drawing and the portal must share, each
// with the element that holds it in each file:
//
//   - "Waiting for you": the h1 of the portal, the title (pic-title) of the
//     drawing.
//   - "Reject": the Reject button of the portal, a pic-button of the drawing.
//   - "Approve and send": the Approve button of the portal, for a send, and the
//     pic-primary button of the drawing.
//   - "From", "To, every recipient (Bcc included)" and "Attachments": a dt in
//     both.
//   - "goes out" and "until you approve.": the two pieces of the note of the
//     action bar (actionbar-note), and the note of the drawing (pic-note). The
//     note "Nothing goes out until you approve." is built from pieces in the
//     portal template, so the test checks the two pieces and not the whole
//     note.
var landingLabels = []landingLabel{
	{
		text:      "Waiting for you",
		inPortal:  inElement(openTag("h1", ""), "Waiting for you", `</h1>`),
		inPicture: inElement(openTag("p", "pic-title"), "Waiting for you", `</p>`),
		from:      `>Waiting for you</h1>`,
		to:        `>Your queue</h1>`,
	},
	{
		text:      "Reject",
		inPortal:  inElement(openTag("button", ""), "Reject", `</button>`),
		inPicture: inElement(openTag("span", "pic-button"), "Reject", `</span>`),
		from:      `>Reject</button>`,
		to:        `>Decline</button>`,
	},
	{
		text:      "Approve and send",
		inPortal:  inElement(openTag("button", "")+`\{\{if \.Send\}\}`, "Approve and send", `\{\{else\}\}`),
		inPicture: inElement(openTag("span", "pic-primary"), "Approve and send", `</span>`),
		from:      `{{if .Send}}Approve and send{{else}}`,
		to:        `{{if .Send}}Send it{{else}}`,
	},
	{
		text:      "From",
		inPortal:  inElement(openTag("dt", ""), "From", `</dt>`),
		inPicture: inElement(openTag("dt", ""), "From", `</dt>`),
		from:      `>From</dt>`,
		to:        `>Sender</dt>`,
	},
	{
		text:      "To, every recipient (Bcc included)",
		inPortal:  inElement(openTag("dt", ""), "To, every recipient (Bcc included)", `</dt>`),
		inPicture: inElement(openTag("dt", ""), "To, every recipient (Bcc included)", `</dt>`),
		from:      `>To, every recipient (Bcc included)</dt>`,
		to:        `>To</dt>`,
	},
	{
		text:      "Attachments",
		inPortal:  inElement(openTag("dt", ""), "Attachments", `</dt>`),
		inPicture: inElement(openTag("dt", ""), "Attachments", `</dt>`),
		from:      `>Attachments</dt>`,
		to:        `>Files</dt>`,
	},
	{
		text:      "until you approve.",
		inPortal:  inElement(openTag("p", "actionbar-note")+`[^<]* `, "until you approve.", `</p>`),
		inPicture: inElement(openTag("p", "pic-note")+`[^<]* `, "until you approve.", `</p>`),
		from:      ` until you approve.</p>`,
		to:        ` until you say yes.</p>`,
	},
	{
		text:      "goes out",
		inPortal:  inElement(openTag("p", "actionbar-note")+`Nothing \{\{if \.Send\}\}`, "goes out", `\{\{else\}\}`),
		inPicture: inElement(openTag("p", "pic-note")+`Nothing `, "goes out", ` `),
		from:      `{{if .Send}}goes out{{else}}`,
		to:        `{{if .Send}}is sent{{else}}`,
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
	requireSite(t)

	picture := readVisibleText(t, landingPicture)
	portal := readVisibleText(t, portalApproval)

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			require.Regexp(t, label.inPortal, portal,
				"%s no longer shows %q as the text of its element. If the portal renamed it, change the picture to match. If a template broke the label over two lines, or an attribute changed, change the pattern of this test.",
				portalApproval, label.text)
			require.Regexp(t, label.inPicture, picture,
				"%s does not show %q as the text of its element. The portal has it, so the picture needs it too, in the same place.",
				landingPicture, label.text)
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

// startTag finds the start tag of each kind of element that holds a label.
// classValue finds a class attribute.
var (
	startTag   = regexp.MustCompile(`<(h1|button|dt|p|span)(\s|>)`)
	classValue = regexp.MustCompile(`class="([^"]*)"`)
)

// withMoreAttributes returns html with a new attribute on every element that
// can hold a label, and with a class before and after the classes of each
// class attribute: <dt> becomes <dt data-added="yes">, and class="pic-note"
// becomes class="added pic-note added-too".
func withMoreAttributes(html string) string {
	html = classValue.ReplaceAllString(html, `class="added $1 added-too"`)
	return startTag.ReplaceAllString(html, `<$1 data-added="yes"$2`)
}

// A new attribute or a new class is not a rename. On copies in memory, with an
// attribute added to every element of a label and more classes in every class
// attribute, each label must still be found in both files.
func TestLandingPictureLabelsAllowNewAttributes(t *testing.T) {
	requireSite(t)

	portal := withMoreAttributes(readVisibleText(t, portalApproval))
	picture := withMoreAttributes(readVisibleText(t, landingPicture))
	require.Contains(t, portal, `<dt data-added="yes">From</dt>`, "the copy must have the new attribute")
	require.Contains(t, portal, `class="added actionbar-note added-too"`, "the copy must have the new classes")

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			require.Regexp(t, label.inPortal, portal, "a portal with a new attribute or class must still pass")
			require.Regexp(t, label.inPicture, picture, "a picture with a new attribute or class must still pass")
		})
	}
}

// A label that moves out of its element in the drawing must fail the check of
// the drawing, even when the file still has the words in another place. The
// test moves each label from its element into the caption, in a copy of the
// drawing in memory.
func TestLandingPictureLabelsSeeAMove(t *testing.T) {
	requireSite(t)

	picture := readVisibleText(t, landingPicture)
	require.Equal(t, 1, strings.Count(picture, "</figcaption>"), "the picture must have one caption")

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			at := label.inPicture.FindStringIndex(picture)
			require.NotNil(t, at, "the pattern must match the real picture first")

			element := picture[at[0]:at[1]]
			moved := picture[:at[0]] + strings.Replace(element, label.text, "", 1) + picture[at[1]:]
			moved = strings.Replace(moved, "</figcaption>", " "+label.text+"</figcaption>", 1)
			require.Contains(t, moved, label.text, "the words must still be in the copy, only in another place")
			require.NotRegexp(t, label.inPicture, moved,
				"a picture with %q moved into the caption must fail the check", label.text)
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

// siteMissing tells if the site folder is missing. A source archive may have no
// site/ folder, and then there is no drawing to check. In CI the folder must be
// there: a missing folder there means that it was renamed or moved, and a skip
// would turn this check off with no sign. So in CI a missing folder is an
// error. Any other problem with the folder is not handled here: the read of the
// drawing then fails the test.
func siteMissing(folder string, inCI bool) (bool, error) {
	if _, err := os.Stat(folder); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if inCI {
		return true, fmt.Errorf("%s is missing, and CI is set, so the landing picture cannot be checked. "+
			"If the site moved, change siteFolder and landingPicture in this test", folder)
	}
	return true, nil
}

// requireSite skips the test when the site folder is missing outside CI, and
// fails it when the folder is missing in CI (the variable CI is set, as GitHub
// Actions does).
func requireSite(t *testing.T) {
	t.Helper()
	missing, err := siteMissing(siteFolder, os.Getenv("CI") != "")
	require.NoError(t, err)
	if missing {
		t.Skip("the site/ folder is missing (for example in a source archive), so there is no landing picture to check")
	}
}

// A missing site folder is a skip outside CI and an error in CI. A folder that
// exists is never missing.
func TestLandingPictureSiteGuard(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "site")

	missing, err := siteMissing(gone, false)
	require.NoError(t, err)
	require.True(t, missing, "outside CI, a missing folder is a skip")

	missing, err = siteMissing(gone, true)
	require.Error(t, err, "in CI, a missing folder must fail the test")
	require.True(t, missing)

	missing, err = siteMissing(dir, true)
	require.NoError(t, err)
	require.False(t, missing)
}

// readVisibleText returns a template file without its comments and without
// its aria-label values.
func readVisibleText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: the paths are constants of this test
	require.NoError(t, err, "cannot read %s", path)
	return hiddenText.ReplaceAllString(string(data), "")
}
