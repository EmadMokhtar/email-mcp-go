package mcpserver

import (
	"context"
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

// userOnlyCalls are the service calls that only the user may make, on the web
// page: changing the permissions, removing or reconnecting an account,
// approving, rejecting and showing a pending action, and making or revoking an
// access token. A prompt-injected agent must never reach them.
var userOnlyCalls = []string{
	"UpdateSettings", "RemoveAccount", "ReconnectIMAPAccount", "ApprovePending", "RejectPending", "PendingDetail",
	"PendingApprovals", "CreateToken", "RevokeToken",
}

// userOnlyImports are the packages that only the user's side may use: the web
// interface, and the tokens package that makes access tokens.
var userOnlyImports = []string{
	"github.com/EmadMokhtar/email-mcp-go/internal/web",
	"github.com/EmadMokhtar/email-mcp-go/internal/tokens",
}

// forbiddenUses returns the user-only calls and imports in one Go file.
func forbiddenUses(t *testing.T, fset *token.FileSet, name string, src any) []string {
	t.Helper()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	require.NoError(t, err)
	var found []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		require.NoError(t, err)
		for _, bad := range userOnlyImports {
			if path == bad {
				found = append(found, "import "+path)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		for _, bad := range userOnlyCalls {
			if sel.Sel.Name == bad {
				found = append(found, fset.Position(sel.Pos()).String()+": "+bad)
			}
		}
		return true
	})
	return found
}

// No MCP tool may change the permissions, remove an account, approve, reject or
// show a pending action, or make an access token. The code of this package
// (not its tests) must not name these calls or import the packages that do
// them.
func TestNoToolReachesTheUserOnlyUseCases(t *testing.T) {
	t.Run("the check finds a use", func(t *testing.T) {
		src := `package x
import "github.com/EmadMokhtar/email-mcp-go/internal/tokens"
func f(s interface{ ApprovePending() }) { s.ApprovePending(); _ = tokens.New }`
		found := forbiddenUses(t, token.NewFileSet(), "x.go", src)
		require.Len(t, found, 2)
	})

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name) //nolint:gosec // G304: the names come from a glob of this package's own files
		require.NoError(t, err)
		require.Empty(t, forbiddenUses(t, fset, name, src), "%s reaches a use case that only the user may run", name)
		checked++
	}
	require.NotZero(t, checked)
}

// The list of tools that the server gives has no tool for these use cases
// either.
func TestNoToolIsNamedForAUserOnlyUseCase(t *testing.T) {
	e := newEnv(t, envOptions{backends: &countingFactory{}})
	tools, err := e.cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools.Tools)
	for _, tool := range tools.Tools {
		for _, bad := range []string{"settings", "remove", "approve", "reject", "token", "autonomy", "approval"} {
			require.NotContains(t, tool.Name, bad, "the tool %s", tool.Name)
		}
	}
}
