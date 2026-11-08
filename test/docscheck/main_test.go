package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The messages of a pull request that changes behaviour. Each one names the
// missing part and the label that would clear it.
const (
	manualMsg = "behaviour changed, but no file under site/content/manual/ changed: " +
		"update the user manual, or add the label no-manual-change"
	techMsg = "behaviour changed, but no file under site/content/tech/ changed: " +
		"update the tech docs, or add the label no-tech-docs-change"
)

var (
	both       = []string{manualMsg, techMsg}
	techOnly   = []string{techMsg}
	manualOnly = []string{manualMsg}
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		paths  []string
		labels []string
		want   []string
	}{
		{"no code", []string{"README.md"}, nil, nil},
		{"test file only", []string{"internal/web/a_test.go"}, nil, nil},
		{"e2e", []string{"internal/e2e/x.go"}, nil, nil},
		{"testutil", []string{"internal/testutil/x/y.go"}, nil, nil},
		{"thirdparty", []string{"internal/thirdparty/x/y.go"}, nil, nil},
		{"release PR", []string{"CHANGELOG.md", ".release-please-manifest.json", "server.json"}, nil, nil},
		{"dependabot", []string{"go.mod", "go.sum"}, nil, nil},
		{"code, no docs", []string{"internal/outbox/a.go"}, nil, both},
		{"cmd", []string{"cmd/email-mcp/a.go"}, nil, both},
		{"template", []string{"internal/web/templates/a.html"}, nil, both},
		{"static", []string{"internal/web/static/app.css"}, nil, both},
		{"moved out", []string{"other/a.go", "internal/outbox/a.go"}, nil, both},
		{"manual only", []string{"internal/a/a.go", "site/content/manual/x.md"}, nil, techOnly},
		{"tech only", []string{"internal/a/a.go", "site/content/tech/x.md"}, nil, manualOnly},
		{"both docs", []string{"internal/a/a.go", "site/content/manual/x.md", "site/content/tech/x.md"}, nil, nil},
		{"both labels", []string{"internal/a/a.go"}, []string{"no-manual-change", "no-tech-docs-change"}, nil},
		{"one label", []string{"internal/a/a.go"}, []string{"no-manual-change"}, techOnly},
		{"landing only", []string{"internal/a/a.go", "site/content/_index.md"}, nil, both},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, check(tt.paths, tt.labels))
		})
	}
}

// TestRunReadsPathsAndLabels feeds run the same input as the workflow: one
// path on each line, and the labels as a JSON array.
func TestRunReadsPathsAndLabels(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		labels string
		code   int
		out    []string
	}{
		{"no behaviour", "README.md\n", "[]", 0, nil},
		{"behaviour, no docs", "internal/outbox/a.go\n", "[]", 1, both},
		{"labels clear it", "internal/outbox/a.go\n", `["no-manual-change","no-tech-docs-change"]`, 0, nil},
		// toJSON in the workflow prints an array that is not empty on more
		// than one line.
		{"labels as toJSON prints them", "internal/outbox/a.go\n", "[\n  \"no-manual-change\"\n]", 1, techOnly},
		// The workflow always sets PR_LABELS. A local run may leave it out.
		{"no labels variable", "internal/outbox/a.go\n", "", 1, both},
		{"empty lines and CR", "\r\ninternal/outbox/a.go\r\n\nsite/content/manual/x.md\r\n", "[]", 1, techOnly},
		{"no final newline", "internal/outbox/a.go\nsite/content/tech/x.md", "[]", 1, manualOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			code := run(strings.NewReader(tt.in), tt.labels, &out)
			require.Equal(t, tt.code, code)
			require.Equal(t, tt.out, lines(out.String()))
		})
	}
}

// TestRunRefusesNoPaths checks that an empty list fails. If the list of files
// cannot be read, the check sees no path, and it must not pass.
func TestRunRefusesNoPaths(t *testing.T) {
	for _, in := range []string{"", "\n", " \n\r\n"} {
		var out strings.Builder
		require.Equal(t, 2, run(strings.NewReader(in), "[]", &out), "input %q", in)
		require.Contains(t, out.String(), "no changed files")
	}
}

// TestRunRefusesBadLabels checks that labels that are not a JSON array of
// strings fail, and do not count as no labels.
func TestRunRefusesBadLabels(t *testing.T) {
	for _, labels := range []string{"no-manual-change", "[", `{"a":1}`, "[1]"} {
		var out strings.Builder
		require.Equal(t, 2, run(strings.NewReader("README.md\n"), labels, &out), "labels %q", labels)
		require.Contains(t, out.String(), "PR_LABELS")
	}
}

// lines splits the output into its lines, and returns nil for no output.
func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
