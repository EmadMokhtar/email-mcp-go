package web

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
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

// landingLabels are the words that the drawing and the portal must share.
// The note "Nothing goes out until you approve." is built from pieces in the
// portal template, so the test checks the two pieces and not the whole note.
var landingLabels = []string{
	"Waiting for you",
	"Reject",
	"Approve and send",
	"From",
	"To, every recipient (Bcc included)",
	"Attachments",
	"until you approve.",
	"goes out",
}

// Some text of a template is not shown as a label: the comments of Hugo and Go
// templates ({{/* ... */}}) and of HTML, and the description of the drawing in
// aria-label, which says "Reject" and "Approve and send" in a sentence. A label
// that is only in such text must not count. So this text is removed before the
// check, and what is left is the text that a person sees.
var hiddenText = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}|<!--.*?-->|aria-label="[^"]*"`)

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
		t.Run(label, func(t *testing.T) {
			require.Contains(t, portal, label,
				"%s no longer has %q. Change the picture to match the portal.", portalApproval, label)
			require.Contains(t, picture, label,
				"%s does not show %q. The portal has it, so the picture needs it too.", landingPicture, label)
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
