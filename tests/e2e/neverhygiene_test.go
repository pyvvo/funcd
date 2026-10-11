package e2e_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issue 950: testify's Never runs each check in a goroutine and returns on its timer without waiting for it, so a
// check that uses the test's t (a helper that calls require, reads through the test's platform) can fail the test
// after its window, once the cleanup has stopped the platform. Such a check goes through poll.Never, which checks in
// the caller's goroutine, and a Never condition is a function literal, so the guard can read it.
func TestIssue950_NeverChecksDoNotUseTheTest(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var hits []string
	for _, f := range trackedFiles(t, root, "*_test.go") {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, f), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Never" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || (pkg.Name != "require" && pkg.Name != "assert") {
				return true
			}
			// a condition that is not a function literal cannot be read here, so it is refused as one that uses t
			if cond, ok := call.Args[1].(*ast.FuncLit); !ok || usesIdent(cond.Body, "t") {
				hits = append(hits, fmt.Sprintf("%s:%d", f, fset.Position(call.Pos()).Line))
			}
			return true
		})
	}
	require.Empty(t, hits, "a require.Never or assert.Never check uses the test's t, or is not a function literal; check with poll.Never instead")
}

func usesIdent(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if id, ok := m.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}
