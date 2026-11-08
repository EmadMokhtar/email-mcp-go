// Command docscheck checks that a pull request that changes behaviour also
// changes the docs. The workflow docs-check.yml runs it.
//
// It reads the changed paths from standard input, one on each line. A moved
// file gives two lines: the new path and the old one. So a file that moves out
// of internal/ still counts. The labels of the pull request come from the
// environment variable PR_LABELS, as a JSON array of strings. When PR_LABELS
// is not set, there are no labels. The number of changed files of the pull
// request comes from PR_CHANGED_FILES. When it is not set, nothing compares
// it. A local run can use git. --no-renames
// lists a moved file as two paths, the old one and the new one, like the API:
//
//	git diff --name-only --no-renames origin/main... | go run ./test/docscheck
//
// The rules (see "The check on pull requests" in the foundation spec):
//
//   - A change counts as behaviour when it touches a Go file in cmd/ or
//     internal/ that is not a test file, or any file in
//     internal/web/templates/, internal/web/static/ or
//     internal/store/migrations/. Test files, internal/testutil, internal/e2e
//     and internal/thirdparty do not count.
//   - If behaviour changed, the pull request must change a file under
//     site/content/manual/ or carry the label no-manual-change.
//   - It must also change a file under site/content/tech/ or carry the label
//     no-tech-docs-change.
//   - The landing page (site/content/_index.md) does not count as docs.
//
// It prints one problem on each line and exits with 1 when it finds any. It
// exits with 2 when it cannot check: it cannot read the paths, it reads no
// path, PR_LABELS is not a JSON array of strings, PR_CHANGED_FILES is not a
// whole number, or the pull request has more files than the API lists. That
// must not look like a pass. "go run" reports both 1 and 2 as exit status 1,
// so in the workflow only the message tells them apart.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// The labels that clear each part of the check.
const (
	noManualLabel = "no-manual-change"
	noTechLabel   = "no-tech-docs-change"
)

// The folders of the two kinds of docs.
const (
	manualDir = "site/content/manual/"
	techDir   = "site/content/tech/"
)

// maxFiles is the most files that the API lists for one pull request.
const maxFiles = 3000

func main() {
	os.Exit(run(os.Stdin, os.Getenv("PR_LABELS"), os.Getenv("PR_CHANGED_FILES"), os.Stderr))
}

// run is main without the process: it reads the paths from in, the labels
// from labelsJSON and the number of changed files from changedFiles. It
// writes the problems to out and returns the exit code. A test can call it.
func run(in io.Reader, labelsJSON, changedFiles string, out io.Writer) int {
	var labels []string
	if labelsJSON != "" {
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
			_, _ = fmt.Fprintf(out, "docscheck: PR_LABELS is not a JSON array of strings: %v\n", err)
			return 2
		}
	}
	if changedFiles != "" {
		n, err := strconv.Atoi(changedFiles)
		if err != nil || n < 0 {
			_, _ = fmt.Fprintf(out, "docscheck: PR_CHANGED_FILES is not a whole number of 0 or more: %q\n", changedFiles)
			return 2
		}
		// The API stops at maxFiles with no error. Above that, the check
		// would judge only part of the list.
		if n > maxFiles {
			_, _ = fmt.Fprintf(out, "docscheck: the pull request changes %d files, and the API lists at most %d, "+
				"so the check cannot see every file: check the docs by hand\n", n, maxFiles)
			return 2
		}
	}
	var paths []string
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		if p := strings.TrimSpace(scanner.Text()); p != "" {
			paths = append(paths, p)
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = fmt.Fprintf(out, "docscheck: cannot read the paths: %v\n", err)
		return 2
	}
	// No path most likely means that the list of files was not read, and then
	// the check must not pass. A pull request with no changed files fails too,
	// but it has nothing to merge.
	if len(paths) == 0 {
		_, _ = fmt.Fprintln(out, "docscheck: no changed files on standard input "+
			"(the list of files was not read, or the pull request changes no file)")
		return 2
	}
	problems := check(paths, labels)
	for _, p := range problems {
		_, _ = fmt.Fprintln(out, p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// check returns one message for each part of the docs that a behaviour change
// leaves out, or nil when nothing is missing.
func check(paths, labels []string) []string {
	if !slices.ContainsFunc(paths, isBehaviour) {
		return nil
	}
	var problems []string
	if !changesDir(paths, manualDir) && !slices.Contains(labels, noManualLabel) {
		problems = append(problems, "behaviour changed, but no file under "+manualDir+
			" changed: update the user manual, or add the label "+noManualLabel)
	}
	if !changesDir(paths, techDir) && !slices.Contains(labels, noTechLabel) {
		problems = append(problems, "behaviour changed, but no file under "+techDir+
			" changed: update the tech docs, or add the label "+noTechLabel)
	}
	return problems
}

// isBehaviour reports whether a change to the file at path p can change what
// the program does.
func isBehaviour(p string) bool {
	// The program embeds the files of these folders, so they count, whatever
	// the file: the web pages, their styles and scripts, and the database
	// migrations.
	for _, dir := range []string{"internal/web/templates/", "internal/web/static/", "internal/store/migrations/"} {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	// Code that only the tests use, and code that is copied from other
	// projects.
	for _, dir := range []string{"internal/testutil/", "internal/e2e/", "internal/thirdparty/"} {
		if strings.HasPrefix(p, dir) {
			return false
		}
	}
	if !strings.HasPrefix(p, "cmd/") && !strings.HasPrefix(p, "internal/") {
		return false
	}
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
}

// changesDir reports whether one of the paths is in the folder dir.
func changesDir(paths []string, dir string) bool {
	return slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, dir) })
}
