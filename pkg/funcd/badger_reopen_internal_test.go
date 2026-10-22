package funcd

import (
	"go/ast"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/testsrc"
)

// Issue #1178: Badger releases a directory's lock only by closing its descriptor (dgraph-io/badger#2350), so a child that
// a parallel test forks keeps the lock until it execs, and a Badger store opened again in this process can fail with
// "Cannot acquire directory lock". A test that opens one store twice runs serially: directly, through a closure or a
// helper, or by building a platform twice while it names a persistent directory. testSources holds the e2e tests too,
// which the e2e binary runs beside these.
func TestIssue1178_TestsThatReopenBadgerRunSerially(t *testing.T) {
	t.Parallel()
	require.Empty(t, testsrc.ParallelTests(t, testSources, newBadgerOpeners(testsrc.Funcs(t, testSources)).reopen),
		"tests that open a Badger store twice must not run in parallel with tests that fork")
}

// badgerOpeners names what opens Badger stores: the direct opens (bstore.Open, kvbadger.Open and the helpers that call
// one), and the platform builds on persistent directories (New when a test names one, and the helpers that build).
type badgerOpeners struct{ direct, builds, persistentBuilds map[string]bool }

func newBadgerOpeners(funcs map[string]*ast.FuncDecl) badgerOpeners {
	return badgerOpeners{
		direct:           grow(funcs, map[string]bool{"bstore.Open": true, "kvbadger.Open": true}, false),
		builds:           grow(funcs, map[string]bool{}, true),
		persistentBuilds: grow(funcs, map[string]bool{"New": true}, true),
	}
}

// grow adds to set each helper of funcs that calls a member, or, when builds is set, that builds a platform while it
// names a persistent directory.
func grow(funcs map[string]*ast.FuncDecl, set map[string]bool, builds bool) map[string]bool {
	for grown := true; grown; {
		grown = false
		for name, fn := range funcs {
			if !set[name] && (calls(fn.Body, set) || builds && buildsPersistent(fn.Body)) {
				set[name], grown = true, true
			}
		}
	}
	return set
}

// reopen reports whether body, or one of its t.Run subtests, opens directly twice or builds a platform on persistent
// directories twice. A call inside a loop counts twice, a subtest counts on its own, and a closure of body that opens or
// builds does so where it is called. Two stores opened directly once each count too: the guard cannot tell their
// directories apart.
func (o badgerOpeners) reopen(body *ast.BlockStmt) bool {
	builds := o.builds
	if namesPersistentDir(body) {
		builds = o.persistentBuilds
	}
	direct, builds := maps.Clone(o.direct), maps.Clone(builds)
	closures := map[ast.Node]bool{}
	ast.Inspect(body, func(x ast.Node) bool {
		if a, ok := x.(*ast.AssignStmt); ok {
			if lit, ok := a.Rhs[0].(*ast.FuncLit); ok {
				if n, ok := a.Lhs[0].(*ast.Ident); ok {
					direct[n.Name], builds[n.Name] = calls(lit.Body, direct), calls(lit.Body, builds) || buildsPersistent(lit.Body)
					closures[lit] = direct[n.Name] || builds[n.Name]
				}
			}
		}
		return true
	})
	return opensTwice(body, direct, closures) || opensTwice(body, builds, closures)
}

// opensTwice reports whether the scope n calls openers twice, or one of its t.Run subtests does.
func opensTwice(n ast.Node, openers map[string]bool, closures map[ast.Node]bool) bool {
	opened := 0
	var subtests []ast.Node
	var walk func(n ast.Node, mult int)
	walk = func(n ast.Node, mult int) {
		ast.Inspect(n, func(x ast.Node) bool {
			switch x := x.(type) {
			case *ast.ForStmt:
				walk(x.Body, 2*mult)
				return false
			case *ast.RangeStmt:
				walk(x.Body, 2*mult)
				return false
			case *ast.CallExpr:
				if lit, ok := subtest(x); ok {
					subtests = append(subtests, lit.Body)
					return false
				}
				if openers[callee(x)] {
					opened += mult
				}
			}
			return !closures[x]
		})
	}
	walk(n, 1)
	return opened > 1 || slices.ContainsFunc(subtests, func(s ast.Node) bool { return opensTwice(s, openers, closures) })
}

// subtest returns the function that a t.Run call runs.
func subtest(c *ast.CallExpr) (*ast.FuncLit, bool) {
	if callee(c) != "t.Run" || len(c.Args) != 2 {
		return nil, false
	}
	lit, ok := c.Args[1].(*ast.FuncLit)
	return lit, ok
}

// calls reports whether n calls a member of set.
func calls(n ast.Node, set map[string]bool) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok {
			found = found || set[callee(c)]
		}
		return !found
	})
	return found
}

// buildsPersistent reports whether n builds a platform while it names a persistent directory.
func buildsPersistent(n ast.Node) bool {
	return calls(n, map[string]bool{"New": true}) && namesPersistentDir(n)
}

// namesPersistentDir reports whether n passes WithWorkflow or WithDeadLetterQueue a directory.
func namesPersistentDir(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok && (callee(c) == "WithWorkflow" || callee(c) == "WithDeadLetterQueue") {
			found = found || len(c.Args) > 0 && !testsrc.IsString(c.Args[0], "")
		}
		return !found
	})
	return found
}

// callee is testsrc.Callee with the package's own qualifier dropped, as the external tests call funcd.New.
func callee(c *ast.CallExpr) string {
	return strings.TrimPrefix(testsrc.Callee(c), "funcd.")
}

// The shapes badgerOpeners tells apart.
func TestBadgerOpenersReopen(t *testing.T) {
	t.Parallel()
	shapes := fstest.MapFS{"shapes_test.go": {Data: []byte(`package funcd

func openStore(dir string) { bstore.Open(dir) }
func build(dir string) { New(WithDeadLetterQueue(dir, 1, 0, 0)) }
func directTwice() { bstore.Open(d); bstore.Open(d) }
func twoStoresOnce() { bstore.Open(a); kvbadger.Open(b) }
func closureTwice() { open := func() { bstore.Open(d) }; open(); open() }
func closureOnce() { open := func() { bstore.Open(d) }; open() }
func helperTwice() { openStore(d); openStore(d) }
func persistentHelperTwice() { build(d); build(d) }
func qualifiedTwice() { funcd.New(funcd.WithDeadLetterQueue(d, 1, 0, 0)); funcd.New(funcd.WithDeadLetterQueue(d, 1, 0, 0)) }
func persistentClosureTwice() { dlq := WithDeadLetterQueue(d, 1, 0, 0); run := func() { New(dlq) }; run(); run() }
func inMemoryTwice() { New(InMemory()); New(WithWorkflow("", 0, 0, 1, 0), InMemory()) }
func helperInALoop() { for range 2 { openStore(d) } }
func persistentHelperInALoop() { for range 2 { build(d) } }
func reopenStore(dir string) { openStore(dir) }
func subtestsOnceEach() { t.Run("a", func(t *testing.T) { openStore(d) }); t.Run("b", func(t *testing.T) { openStore(e) }) }
func subtestsInALoop() { for range 2 { t.Run("x", func(t *testing.T) { openStore(d) }) } }
func subtestReopen() { t.Run("a", func(t *testing.T) { openStore(d); reopenStore(d) }) }
func storeAndBuildOnce() { bstore.Open(a); New(WithDeadLetterQueue(b, 1, 0, 0)) }
`)}}
	o := newBadgerOpeners(testsrc.Funcs(t, shapes))
	funcs := testsrc.Funcs(t, shapes)
	for name, want := range map[string]bool{"directTwice": true, "twoStoresOnce": true, "closureTwice": true,
		"closureOnce": false, "helperTwice": true, "persistentHelperTwice": true, "qualifiedTwice": true,
		"persistentClosureTwice": true, "inMemoryTwice": false, "helperInALoop": true, "persistentHelperInALoop": true,
		"subtestsOnceEach": false, "subtestsInALoop": false, "subtestReopen": true, "storeAndBuildOnce": false} {
		require.Equal(t, want, o.reopen(funcs[name].Body), name)
	}
}
