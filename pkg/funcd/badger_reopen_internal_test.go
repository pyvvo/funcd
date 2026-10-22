package funcd

import (
	"go/ast"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/testsrc"
)

// Issue #1178: Badger releases a directory's lock only by closing its descriptor (dgraph-io/badger#2350), so a child that
// a parallel test forks keeps the lock until it execs, and a platform built again on the same directories can fail with
// "Cannot acquire directory lock". A test that builds a platform twice while it names a persistent directory runs
// serially. testSources holds the e2e tests too, which the e2e binary runs beside these.
func TestIssue1178_TestsThatRebuildAPersistentPlatformRunSerially(t *testing.T) {
	t.Parallel()
	require.Empty(t, testsrc.ParallelTests(t, testSources, rebuildsPersistent),
		"tests that build a platform on persistent directories twice must not run in parallel with tests that fork")
}

// rebuildsPersistent reports whether body builds a platform twice, with New or fileStart, while it names a persistent
// directory: fileStart, or WithWorkflow or WithDeadLetterQueue with a directory. A call of a closure that builds one
// counts as a build where it is called.
func rebuildsPersistent(body *ast.BlockStmt) bool {
	builders := []string{"New", "fileStart"}
	closures := map[ast.Node]bool{}
	persistent := testsrc.Calls(body, "fileStart") > 0
	ast.Inspect(body, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.AssignStmt:
			if lit, ok := x.Rhs[0].(*ast.FuncLit); ok && testsrc.Calls(lit, "New", "fileStart") > 0 {
				if n, ok := x.Lhs[0].(*ast.Ident); ok {
					builders = append(builders, n.Name)
					closures[lit] = true
				}
			}
		case *ast.CallExpr:
			if name := testsrc.Callee(x); (name == "WithWorkflow" || name == "WithDeadLetterQueue") && len(x.Args) > 0 {
				persistent = persistent || !testsrc.IsString(x.Args[0], "")
			}
		}
		return true
	})
	builds := 0
	ast.Inspect(body, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok && slices.Contains(builders, testsrc.Callee(c)) {
			builds++
		}
		return !closures[x]
	})
	return persistent && builds > 1
}
