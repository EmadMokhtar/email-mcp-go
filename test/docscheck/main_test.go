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
		// The program embeds the migrations, so they change behaviour.
		{"migration", []string{"internal/store/migrations/0014_x.sql"}, nil, both},
		{"cmd test file", []string{"cmd/email-mcp/a_test.go"}, nil, nil},
		{"non-Go file in internal", []string{"internal/a/README.md"}, nil, nil},
		// Any file in templates counts, also a test file.
		{"test file in templates", []string{"internal/web/templates/a_test.go"}, nil, both},
		{"lookalike docs folders", []string{"internal/a/a.go", "site/content/manual-old/x.md", "site/content/technical/x.md"}, nil, both},
		{"other labels", []string{"internal/a/a.go"}, []string{"bug", "documentation"}, both},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, check(tt.paths, tt.labels))
		})
	}
}

// TestRunReadsPathsAndLabels feeds run the same input as the workflow: one
// path on each line, the labels as a JSON array, and the number of changed
// files of the pull request.
func TestRunReadsPathsAndLabels(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		labels  string
		changed string
		code    int
		out     []string
	}{
		{"no behaviour", "README.md\n", "[]", "1", 0, nil},
		{"behaviour, no docs", "internal/outbox/a.go\n", "[]", "1", 1, both},
		{"labels clear it", "internal/outbox/a.go\n", `["no-manual-change","no-tech-docs-change"]`, "1", 0, nil},
		// JSON may come on more than one line.
		{"labels on more lines", "internal/outbox/a.go\n", "[\n  \"no-manual-change\"\n]", "1", 1, techOnly},
		// The workflow always sets both variables. A local run may leave
		// them out.
		{"no variables", "internal/outbox/a.go\n", "", "", 1, both},
		{"empty lines and CR", "\r\ninternal/outbox/a.go\r\n\nsite/content/manual/x.md\r\n", "[]", "2", 1, techOnly},
		{"no final newline", "internal/outbox/a.go\nsite/content/tech/x.md", "[]", "2", 1, manualOnly},
		// The API lists 3000 files, so a pull request of 3000 files is
		// checked in full.
		{"3000 files", "README.md\n", "[]", "3000", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			code := run(strings.NewReader(tt.in), tt.labels, tt.changed, &out)
			require.Equal(t, tt.code, code)
			require.Equal(t, tt.out, lines(out.String()))
		})
	}
}

// TestRunRefusesTooManyFiles checks that a pull request with more files than
// the API lists fails. The check would see only part of the list.
func TestRunRefusesTooManyFiles(t *testing.T) {
	var out strings.Builder
	require.Equal(t, 2, run(strings.NewReader("README.md\n"), "[]", "3001", &out))
	require.Contains(t, out.String(), "3001 files")
	require.Contains(t, out.String(), "at most 3000")
}

// TestRunRefusesBadChangedFiles checks that a number of changed files that is
// not a whole number of 0 or more fails.
func TestRunRefusesBadChangedFiles(t *testing.T) {
	for _, changed := range []string{"x", "-1", "1.5", "null"} {
		var out strings.Builder
		require.Equal(t, 2, run(strings.NewReader("README.md\n"), "[]", changed, &out), "changed %q", changed)
		require.Contains(t, out.String(), "PR_CHANGED_FILES")
	}
}

// TestRunRefusesNoPaths checks that an empty list fails. If the list of files
// cannot be read, the check sees no path, and it must not pass.
func TestRunRefusesNoPaths(t *testing.T) {
	for _, in := range []string{"", "\n", " \n\r\n"} {
		var out strings.Builder
		require.Equal(t, 2, run(strings.NewReader(in), "[]", "", &out), "input %q", in)
		require.Contains(t, out.String(), "no changed files")
	}
}

// TestRunRefusesBadLabels checks that labels that are not a JSON array of
// strings fail, and do not count as no labels.
func TestRunRefusesBadLabels(t *testing.T) {
	for _, labels := range []string{"no-manual-change", "[", `{"a":1}`, "[1]"} {
		var out strings.Builder
		require.Equal(t, 2, run(strings.NewReader("README.md\n"), labels, "", &out), "labels %q", labels)
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
