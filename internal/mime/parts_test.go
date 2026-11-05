package mime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLayoutOf(t *testing.T) {
	text := func(id, subtype string) *PartNode { return &PartNode{ID: id, MediaType: "text/" + subtype} }
	empty := func(id, subtype string) *PartNode {
		n := text(id, subtype)
		n.Empty = true
		return n
	}
	named := func(id, subtype string) *PartNode {
		n := text(id, subtype)
		n.Named = true
		return n
	}
	withCID := func(n *PartNode, cid string) *PartNode {
		n.ContentID = cid
		return n
	}
	pdf := func(id string) *PartNode {
		return &PartNode{ID: id, MediaType: "application/pdf", Attachment: true, Named: true}
	}
	image := func(id string) *PartNode { return &PartNode{ID: id, MediaType: "image/png"} }
	multi := func(subtype string, children ...*PartNode) *PartNode {
		return &PartNode{MediaType: "multipart/" + subtype, Children: children}
	}
	related := func(start string, children ...*PartNode) *PartNode {
		n := multi("related", children...)
		n.Start = start
		return n
	}

	tests := []struct {
		name        string
		root        *PartNode
		shown       []string
		others      []string
		attachments []string
	}{
		{name: "no message", root: nil},
		{name: "one text part", root: text("1", "plain"), shown: []string{"1"}},
		{
			name:   "the last alternative is shown",
			root:   multi("alternative", text("1", "plain"), text("2", "html")),
			shown:  []string{"2"},
			others: []string{"1"},
		},
		{
			name:   "plain after html is shown",
			root:   multi("alternative", text("1", "html"), text("2", "plain")),
			shown:  []string{"2"},
			others: []string{"1"},
		},
		{
			name:   "the others go from the last to the first",
			root:   multi("alternative", text("1", "plain"), text("2", "plain"), text("3", "html"), text("4", "html")),
			shown:  []string{"4"},
			others: []string{"3", "2", "1"},
		},
		{
			name:   "an empty last alternative hides the text before it",
			root:   multi("alternative", text("1", "plain"), empty("2", "html")),
			others: []string{"1"},
		},
		{
			name:  "an empty first alternative is not a version",
			root:  multi("alternative", empty("1", "plain"), text("2", "html")),
			shown: []string{"2"},
		},
		{
			name:   "a calendar part cannot be shown as the body",
			root:   multi("alternative", text("1", "plain"), text("2", "html"), text("3", "calendar")),
			shown:  []string{"2"},
			others: []string{"1"},
		},
		{
			name:  "outside an alternative every text part is shown",
			root:  multi("mixed", text("1", "plain"), text("2", "html"), text("3", "plain")),
			shown: []string{"1", "2", "3"},
		},
		{
			name:  "an empty part in mixed is not shown",
			root:  multi("mixed", empty("1", "plain"), text("2", "html")),
			shown: []string{"2"},
		},
		{
			name: "an alternative and a footer",
			root: multi("mixed",
				multi("alternative", text("1.1", "plain"), text("1.2", "html")),
				text("2", "plain"), pdf("3")),
			shown:       []string{"1.2", "2"},
			others:      []string{"1.1"},
			attachments: []string{"3"},
		},
		{
			name: "two alternatives",
			root: multi("mixed",
				multi("alternative", text("1.1", "plain"), text("1.2", "html")),
				multi("alternative", text("2.1", "plain"), text("2.2", "html"))),
			shown:  []string{"1.2", "2.2"},
			others: []string{"1.1", "2.1"},
		},
		{
			name: "an empty alternative in mixed keeps the other version",
			root: multi("mixed",
				multi("alternative", text("1.1", "plain"), empty("1.2", "html")),
				pdf("2")),
			others:      []string{"1.1"},
			attachments: []string{"2"},
		},
		{
			name: "text after an empty alternative",
			root: multi("mixed",
				multi("alternative", text("1.1", "plain"), empty("1.2", "html")),
				text("2", "plain")),
			shown:  []string{"2"},
			others: []string{"1.1"},
		},
		{
			name:        "a named html part is not the body",
			root:        multi("mixed", text("1", "plain"), named("2", "html")),
			shown:       []string{"1"},
			attachments: []string{"2"},
		},
		{
			name:        "a named part before the body is not the body",
			root:        multi("mixed", named("1", "plain"), text("2", "plain")),
			shown:       []string{"2"},
			attachments: []string{"1"},
		},
		{
			name:  "a named part is the body when there is no other",
			root:  named("1", "plain"),
			shown: []string{"1"},
		},
		{
			name:        "of named parts only the first is the body",
			root:        multi("mixed", named("1", "plain"), named("2", "html")),
			shown:       []string{"1"},
			attachments: []string{"2"},
		},
		{
			name:   "a name does not count in an alternative",
			root:   multi("alternative", text("1", "plain"), named("2", "html")),
			shown:  []string{"2"},
			others: []string{"1"},
		},
		{
			name:        "a named part of another text type is an attachment",
			root:        multi("mixed", text("1", "plain"), named("2", "csv"), text("3", "csv")),
			shown:       []string{"1"},
			attachments: []string{"2"},
		},
		{
			name: "a text part with disposition attachment is never the body",
			root: multi("mixed",
				&PartNode{ID: "1", MediaType: "text/plain", Attachment: true}, text("2", "plain")),
			shown:       []string{"2"},
			attachments: []string{"1"},
		},
		{
			name:        "only a text attachment",
			root:        &PartNode{ID: "1", MediaType: "text/plain", Attachment: true},
			attachments: []string{"1"},
		},
		{
			name:        "a part that is not text is an attachment",
			root:        multi("mixed", text("1", "plain"), image("2")),
			shown:       []string{"1"},
			attachments: []string{"2"},
		},
		{
			name:        "related shows its first part",
			root:        multi("related", text("1", "html"), image("2")),
			shown:       []string{"1"},
			attachments: []string{"2"},
		},
		{
			name: "related shows the root part that it names",
			root: related("<b@x>",
				withCID(text("1", "html"), "<a@x>"), withCID(text("2", "html"), "<b@x>")),
			shown:  []string{"2"},
			others: []string{"1"},
		},
		{
			name: "the name of the root part needs no angle brackets",
			root: related("b@x",
				text("1", "html"), withCID(text("2", "html"), "<b@x>")),
			shown:  []string{"2"},
			others: []string{"1"},
		},
		{
			name: "a root part that does not exist",
			root: related("<c@x>",
				withCID(text("1", "html"), "<a@x>"), withCID(text("2", "html"), "<b@x>")),
			shown:  []string{"1"},
			others: []string{"2"},
		},
		{
			name:        "the first part of related cannot be shown",
			root:        multi("related", image("1"), text("2", "html")),
			shown:       []string{"2"},
			attachments: []string{"1"},
		},
		{
			name:        "related as the last alternative",
			root:        multi("alternative", text("1", "plain"), multi("related", text("2.1", "html"), image("2.2"))),
			shown:       []string{"2.1"},
			others:      []string{"1"},
			attachments: []string{"2.2"},
		},
		{
			name:        "an alternative in related",
			root:        multi("related", multi("alternative", text("1.1", "plain"), text("1.2", "html")), image("2")),
			shown:       []string{"1.2"},
			others:      []string{"1.1"},
			attachments: []string{"2"},
		},
		{
			name:   "a second text part in related is another version",
			root:   multi("related", text("1", "html"), text("2", "plain")),
			shown:  []string{"1"},
			others: []string{"2"},
		},
	}
	ids := func(nodes []*PartNode) []string {
		var out []string
		for _, n := range nodes {
			out = append(out, n.ID)
		}
		return out
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			layout := LayoutOf(tc.root)
			require.Equal(t, tc.shown, ids(layout.Shown), "shown")
			require.Equal(t, tc.others, ids(layout.Others), "others")
			require.Equal(t, tc.attachments, ids(layout.Attachments), "attachments")
		})
	}
}
