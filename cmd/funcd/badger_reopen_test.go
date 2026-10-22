package main

import (
	"embed"
	"go/ast"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/testsrc"
)

// testSources is this package's tests as compiled, so a `go test -overlay` revert check reads the overlaid source.
//
//go:embed *_test.go
var testSources embed.FS

// Issue #1149: Badger releases a directory's lock only by closing its descriptor (dgraph-io/badger#2350), so a child
// process that a parallel test forks in that instant holds the lock until it execs, and an immediate reopen of the
// directory fails with "Cannot acquire directory lock". A test that opens a Badger directory again after closing it
// therefore runs serially: no parallel test forks while it runs.
func TestIssue1149_TestsThatReopenBadgerRunSerially(t *testing.T) {
	t.Parallel()
	opens := func(body *ast.BlockStmt) bool {
		return testsrc.Calls(body, "restoredKV", "kvbadger.Open", "badgerstore.Open") > 0
	}
	require.Empty(t, testsrc.ParallelTests(t, testSources, opens),
		"tests that reopen a Badger directory must not run in parallel with tests that fork")
}

// Issue #1178: a node's `restore run` opens and closes its Badger directories in this process, and its start opens them
// again, as a second start or a serve of its config does, so a test that opens one node's stores twice runs serially for
// the reason of #1149. Opens are counted per node variable, from the newNode it holds.
func TestIssue1178_TestsThatReopenANodeRunSerially(t *testing.T) {
	t.Parallel()
	require.Empty(t, testsrc.ParallelTests(t, testSources, reopensANode),
		"tests that open a node's Badger directories twice must not run in parallel with tests that fork")
}

// reopensANode reports whether body opens one node's stores twice, through n.restore("run", …), n.start(t) or
// serve(ctx, n.cfgPath, …).
func reopensANode(body *ast.BlockStmt) bool {
	opens := map[string]int{}
	reopened := false
	ast.Inspect(body, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.AssignStmt:
			if c, ok := x.Rhs[0].(*ast.CallExpr); ok && testsrc.Callee(c) == "newNode" {
				if n, ok := x.Lhs[0].(*ast.Ident); ok {
					opens[n.Name] = 0
				}
			}
		case *ast.CallExpr:
			if n := nodeOpened(x); n != "" {
				opens[n]++
				reopened = reopened || opens[n] > 1
			}
		}
		return true
	})
	return reopened
}

// nodeOpened names the node whose stores call opens, or "".
func nodeOpened(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		n, ok := sel.X.(*ast.Ident)
		if ok && (sel.Sel.Name == "start" || sel.Sel.Name == "restore" && len(call.Args) > 0 && testsrc.IsString(call.Args[0], "run")) {
			return n.Name
		}
	}
	if testsrc.Callee(call) == "serve" && len(call.Args) > 1 {
		if cfg, ok := call.Args[1].(*ast.SelectorExpr); ok && cfg.Sel.Name == "cfgPath" {
			if n, ok := cfg.X.(*ast.Ident); ok {
				return n.Name
			}
		}
	}
	return ""
}

// The node shapes reopensANode tells apart.
func TestReopensANode(t *testing.T) {
	t.Parallel()
	shapes := fstest.MapFS{"shapes_test.go": {Data: []byte(`package main

func restoreThenStart() { n := newNode(t, nil, ""); n.restore("run", "latest"); n.start(t) }
func startTwice() { n := newNode(t, nil, ""); n.start(t); n.start(t) }
func restoreThenServe() { n := newNode(t, nil, ""); n.restore("run", "latest"); serve(ctx, n.cfgPath, nil, nil) }
func reopenAcrossANode() { a := newNode(t, nil, ""); a.restore("run", "latest"); b := newNode(t, nil, ""); b.start(t); a.start(t) }
func twoNodesOnce() { a := newNode(t, nil, ""); a.start(t); b := newNode(t, nil, ""); b.start(t) }
func listThenRun() { n := newNode(t, nil, ""); n.restore("list"); n.restore("run", "latest") }
func newNodeBetween() { n := newNode(t, nil, ""); n.restore("run", "latest"); n = newNode(t, nil, ""); n.start(t) }
`)}}
	funcs := testsrc.Funcs(t, shapes)
	for name, want := range map[string]bool{"restoreThenStart": true, "startTwice": true, "restoreThenServe": true,
		"reopenAcrossANode": true, "twoNodesOnce": false, "listThenRun": false, "newNodeBetween": false} {
		require.Equal(t, want, reopensANode(funcs[name].Body), name)
	}
}
