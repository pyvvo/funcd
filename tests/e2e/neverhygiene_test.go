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
// after its window, once the cleanup has stopped the platform. The t can sit one call away, in a closure, a helper or
// a rig method no syntax check reads, so every Never check goes through poll.Never, which checks in the caller's
// goroutine: any other Never or Neverf call with arguments is refused, an aliased testify import's and require.New(t)'s
// included.
func TestIssue950_NeverChecksDoNotUseTheTest(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var hits []string
	for _, f := range trackedFiles(t, root, "*.go") {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, f), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) == 0 || (sel.Sel.Name != "Never" && sel.Sel.Name != "Neverf") {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "poll" {
				hits = append(hits, fmt.Sprintf("%s:%d", f, fset.Position(call.Pos()).Line))
			}
			return true
		})
	}
	require.Empty(t, hits, "a Never check other than poll.Never: check with poll.Never instead")
}
