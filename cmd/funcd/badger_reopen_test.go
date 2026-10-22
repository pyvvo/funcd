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
			if reopens.MatchString(body) && strings.Contains(body, "t.Parallel()") {
				parallel = append(parallel, fn.Name.Name)
			}
		}
	}
	require.Empty(t, parallel, "tests that reopen a Badger directory must not run in parallel with tests that fork")
}
