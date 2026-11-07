package web

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The landing page of the docs site has a drawing of the approval page. The
// drawing is HTML in site/layouts/_partials/approval-picture.html. The docs
// check of the site does not compare it with the real page, so this test does.
// It requires each label of the drawing in both, each as the whole text of its
// own element. If a pull request renames a label of the portal, this test
// fails until the drawing says the same.
//
// The real page is the approval page of a waiting send, as the server renders
// it in the tests of this package, so the labels are the words that a person
// sees. The drawing is the file of the partial. Both are parsed as HTML, so an
// attribute, a comment or the description in aria-label is never taken for a
// label. The tests do not run Hugo.
const (
	landingPicture = "../../site/layouts/_partials/approval-picture.html"
	siteFolder     = "../../site"
)

// labelPlace names a kind of element: its tag, and a class that it must have
// among its classes, or "" for any.
type labelPlace struct {
	tag   atom.Atom
	class string
}

func (e labelPlace) String() string {
	if e.class == "" {
		return "<" + e.tag.String() + ">"
	}
	return fmt.Sprintf("<%s class=%q>", e.tag, e.class)
}

// landingLabel is one label of the drawing, and the element that shows it in
// the portal and in the drawing.
type landingLabel struct {
	text    string
	portal  labelPlace
	picture labelPlace
}

// landingLabels are the words that the drawing and the portal must share:
//
//   - "Waiting for you": the h1 of the portal, the title (pic-title) of the
//     drawing.
//   - "Reject": the Reject button, and a pic-button of the drawing.
//   - "Approve and send": the Approve button of a send, and the pic-primary
//     button of the drawing.
//   - "From", "To, every recipient (Bcc included)" and "Attachments": a dt in
//     both.
//   - "Nothing goes out until you approve.": the note of the action bar
//     (actionbar-note), and the note of the drawing (pic-note).
var landingLabels = []landingLabel{
	{"Waiting for you", labelPlace{atom.H1, ""}, labelPlace{atom.P, "pic-title"}},
	{"Reject", labelPlace{atom.Button, ""}, labelPlace{atom.Span, "pic-button"}},
	{"Approve and send", labelPlace{atom.Button, ""}, labelPlace{atom.Span, "pic-primary"}},
	{"From", labelPlace{atom.Dt, ""}, labelPlace{atom.Dt, ""}},
	{"To, every recipient (Bcc included)", labelPlace{atom.Dt, ""}, labelPlace{atom.Dt, ""}},
	{"Attachments", labelPlace{atom.Dt, ""}, labelPlace{atom.Dt, ""}},
	{"Nothing goes out until you approve.", labelPlace{atom.P, "actionbar-note"}, labelPlace{atom.P, "pic-note"}},
}

// findLabel returns the first element of the kind e whose whole text is label,
// or nil. Text means the text nodes inside the element, with each run of white
// space made one space. Attributes are not text.
func findLabel(doc *html.Node, e labelPlace, label string) *html.Node {
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.DataAtom != e.tag {
			continue
		}
		if e.class != "" && !slices.Contains(strings.Fields(attr(n, "class")), e.class) {
			continue
		}
		if textOf(n) == label {
			return n
		}
	}
	return nil
}

func TestLandingPictureLabelsMatchPortal(t *testing.T) {
	requireSite(t)
	portal := parsePage(t, renderedApproval(t))
	picture := parsePage(t, readPicture(t))

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			require.NotNil(t, findLabel(portal, label.portal, label.text),
				"the approval page no longer shows %q as the text of a %s. If the portal renamed it, change the picture to match. If the element, its class or an attribute changed, change this test.",
				label.text, label.portal)
			require.NotNil(t, findLabel(picture, label.picture, label.text),
				"%s does not show %q as the text of a %s. The portal has it, so the picture needs it too, in the same place.",
				landingPicture, label.text, label.picture)
		})
	}
}

// A new attribute or a new class is not a rename. In copies of both pages,
// every element gets a new attribute, and every class attribute gets a class
// before and after its classes. Each label must still be found.
func TestLandingPictureLabelsAllowNewAttributes(t *testing.T) {
	requireSite(t)
	portal := parsePage(t, renderedApproval(t))
	picture := parsePage(t, readPicture(t))
	addAttributes(portal)
	addAttributes(picture)

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			require.NotNil(t, findLabel(portal, label.portal, label.text), "a portal with a new attribute or class must still pass")
			require.NotNil(t, findLabel(picture, label.picture, label.text), "a picture with a new attribute or class must still pass")
		})
	}
}

// addAttributes gives every element of doc the attribute data-added="yes",
// and the classes "added" and "added-too" around the classes that it has.
func addAttributes(doc *html.Node) {
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		for i, a := range n.Attr {
			if a.Key == "class" {
				n.Attr[i].Val = "added " + a.Val + " added-too"
			}
		}
		n.Attr = append(n.Attr, html.Attribute{Key: "data-added", Val: "yes"})
	}
}

// A rename of a label in the portal must fail the check, for each label, even
// though the same words can stay in another place of the page: the title of
// the tab, the menu, or data-busy. The test renames the label in its element,
// in a copy of the rendered page.
func TestLandingPictureLabelsSeeARename(t *testing.T) {
	requireSite(t)
	page := renderedApproval(t)

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			portal := parsePage(t, page)
			found := findLabel(portal, label.portal, label.text)
			require.NotNil(t, found, "the check must pass on the real page first")

			setText(found, "Renamed")
			require.Nil(t, findLabel(portal, label.portal, label.text),
				"a portal with %q renamed must fail the check", label.text)
		})
	}
}

// A label that moves out of its element in the drawing must fail the check,
// even when the file still has the words in another place. The test moves
// each label from its element into the caption, in a copy of the drawing.
func TestLandingPictureLabelsSeeAMove(t *testing.T) {
	requireSite(t)
	source := readPicture(t)

	for _, label := range landingLabels {
		t.Run(label.text, func(t *testing.T) {
			picture := parsePage(t, source)
			found := findLabel(picture, label.picture, label.text)
			require.NotNil(t, found, "the check must pass on the real drawing first")
			caption := firstElement(picture, atom.Figcaption)
			require.NotNil(t, caption, "the drawing must have a caption")

			setText(found, "")
			caption.AppendChild(&html.Node{Type: html.TextNode, Data: " " + label.text})
			require.Contains(t, textOf(caption), label.text, "the words must still be in the copy, in the caption")
			require.Nil(t, findLabel(picture, label.picture, label.text),
				"a picture with %q moved into the caption must fail the check", label.text)
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

// renderedApproval returns the approval page of a send that waits for the
// person, as the server renders it for the browser. The send is a new message,
// so the page shows the whole text and offers Approve and send.
func renderedApproval(t *testing.T) string {
	t.Helper()
	p := newPageFixture(t)
	id := p.fallbackSend(p.tenant, p.account(), plainCompose())
	resp, body := p.get(approvalPath(id))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return body
}

// readPicture returns the file of the drawing.
func readPicture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(landingPicture)
	require.NoError(t, err, "cannot read %s", landingPicture)
	return string(data)
}

// parsePage parses a page or a part of one.
func parsePage(t *testing.T, page string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	return doc
}

// firstElement returns the first element with the tag, or nil.
func firstElement(doc *html.Node, tag atom.Atom) *html.Node {
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == tag {
			return n
		}
	}
	return nil
}

// setText replaces everything inside n with the text.
func setText(n *html.Node, text string) {
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
}
