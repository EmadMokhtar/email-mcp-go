// Package noracecheck checks the packages whose tests run without the race
// detector. "make test" runs the packages of NORACE_PKGS in the Makefile
// without -race. That is safe only while their code starts no goroutine.
package noracecheck

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// module is the module path. NORACE_PKGS names full import paths.
const module = "github.com/EmadMokhtar/email-mcp-go/"

// root is the folder of the module, seen from the folder of this test.
const root = "../.."

// TestNoRacePackagesStartNoGoroutine fails when the code of a package that
// runs its tests without the race detector starts a goroutine, or imports
// sync or sync/atomic. The test files are not checked: a test may start a
// goroutine, for example to sample the heap while a call runs.
//
// If the code needs a goroutine, take the package out of NORACE_PKGS, so its
// tests run with -race again.
func TestNoRacePackagesStartNoGoroutine(t *testing.T) {
	pkgs := noRacePackages(t)
	require.NotEmpty(t, pkgs, "NORACE_PKGS in the Makefile is empty")

	fset := token.NewFileSet()
	for _, pkg := range pkgs {
		require.True(t, strings.HasPrefix(pkg, module), "%s is not a package of this module", pkg)
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(pkg, module)))
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)

		checked := 0
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			require.NoError(t, err)
			checked++
			for _, imp := range f.Imports {
				name, err := strconv.Unquote(imp.Path.Value)
				require.NoError(t, err)
				if name == "sync" || name == "sync/atomic" {
					t.Errorf("%s imports %s: the tests of %s must run with -race again", fset.Position(imp.Pos()), name, pkg)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if g, ok := n.(*ast.GoStmt); ok {
					t.Errorf("%s starts a goroutine: the tests of %s must run with -race again", fset.Position(g.Pos()), pkg)
				}
				return true
			})
		}
		require.NotZero(t, checked, "%s has no Go files in %s", pkg, dir)
	}
}

// noRacePackages returns the import paths of NORACE_PKGS in the Makefile.
func noRacePackages(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	const prefix = "NORACE_PKGS :="
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), prefix); ok {
			require.False(t, strings.HasSuffix(line, `\`), "keep NORACE_PKGS on one line")
			return strings.Fields(line)
		}
	}
	require.NoError(t, sc.Err())
	t.Fatalf("the Makefile has no line that starts with %q", prefix)
	return nil
}
