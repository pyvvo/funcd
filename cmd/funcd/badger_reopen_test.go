package main

import (
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	reopens := regexp.MustCompile(`\brestoredKV\(|\bkvbadger\.Open\(|\bbadgerstore\.Open\(`)
	require.Empty(t, parallelTests(t, reopens.MatchString),
		"tests that reopen a Badger directory must not run in parallel with tests that fork")
}

// Issue #1178: a node's restore opens and closes its Badger directories in this process and its start opens them again,
// as a second start does, so a test that opens one node's stores twice runs serially for the reason of #1149. Opens are
// counted per node, from one newNode to the next.
func TestIssue1178_TestsThatReopenANodeRunSerially(t *testing.T) {
	t.Parallel()
	opens := regexp.MustCompile(`\b\w+\.restore\(|\b\w+\.start\(t\)|\bserve\([^,]+, \w+\.cfgPath`)
	reopens := func(body string) bool {
		for _, node := range strings.Split(body, "newNode(") {
			if len(opens.FindAllString(node, -1)) > 1 {
				return true
			}
		}
		return false
	}
	require.Empty(t, parallelTests(t, reopens),
		"tests that open a node's Badger directories twice must not run in parallel with tests that fork")
}

// parallelTests names this package's tests that call t.Parallel() and whose body matches.
func parallelTests(t *testing.T, matches func(body string) bool) []string {
	t.Helper()
	files, err := testSources.ReadDir(".")
	require.NoError(t, err)
	var parallel []string
	for _, f := range files {
		src, err := testSources.ReadFile(f.Name())
		require.NoError(t, err)
		file, err := parser.ParseFile(token.NewFileSet(), f.Name(), src, 0)
		require.NoError(t, err)
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			body := string(src[fn.Body.Pos()-file.FileStart : fn.Body.End()-file.FileStart])
			if matches(body) && strings.Contains(body, "t.Parallel()") {
				parallel = append(parallel, fn.Name.Name)
			}
		}
	}
	return parallel
}
