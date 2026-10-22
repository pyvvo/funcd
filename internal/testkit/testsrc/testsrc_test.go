package testsrc_test

import (
	"go/ast"
	"go/parser"
	"maps"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/testsrc"
)

func TestParallelTests(t *testing.T) {
	t.Parallel()
	sources := fstest.MapFS{
		"a_test.go": {Data: []byte("package a\n\nfunc TestSerial(t *testing.T) { open() }\n\n" +
			"func TestParallel(t *testing.T) {\n\tt.Parallel()\n\tx.open()\n\topen()\n}\n\n" +
			"func TestQuiet(t *testing.T) { t.Parallel() }\n\nfunc helper(t *testing.T) { t.Parallel(); open() }\n")},
		"a.go": {Data: []byte("package a\n\nfunc TestNotATestFile(t *testing.T) { t.Parallel(); open() }\n")},
	}
	opens := func(body *ast.BlockStmt) bool { return testsrc.Calls(body, "open") > 0 }
	require.Equal(t, []string{"TestParallel"}, testsrc.ParallelTests(t, sources, opens))
	both := func(body *ast.BlockStmt) bool { return testsrc.Calls(body, "open", "x.open") == 2 }
	require.Equal(t, []string{"TestParallel"}, testsrc.ParallelTests(t, sources, both))

	funcs := testsrc.Funcs(t, sources)
	require.ElementsMatch(t, []string{"TestSerial", "TestParallel", "TestQuiet", "helper"}, slices.Collect(maps.Keys(funcs)))
	require.Equal(t, 1, testsrc.Calls(funcs["helper"].Body, "open"))
}

func TestCalleeAndIsString(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]string{`f("", "run", run)`: "f", `x.f()`: "x.f", `x.y.f()`: "", `f()()`: ""} {
		e, err := parser.ParseExpr(src)
		require.NoError(t, err)
		require.Equal(t, want, testsrc.Callee(e.(*ast.CallExpr)), src)
	}
	e, err := parser.ParseExpr(`f("", "run", run)`)
	require.NoError(t, err)
	args := e.(*ast.CallExpr).Args
	require.True(t, testsrc.IsString(args[0], ""))
	require.True(t, testsrc.IsString(args[1], "run"))
	require.False(t, testsrc.IsString(args[2], "run"))
}
