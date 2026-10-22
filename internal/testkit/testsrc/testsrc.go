// Package testsrc reads a package's own test sources, for the guard tests that check how its tests are written.
package testsrc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ParallelTests names the tests in sources, a package's embedded *_test.go files, that call t.Parallel() and whose body
// match reports true for.
func ParallelTests(t testing.TB, sources fs.FS, match func(body *ast.BlockStmt) bool) []string {
	t.Helper()
	var parallel []string
	for _, file := range parse(t, sources) {
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Body != nil && strings.HasPrefix(fn.Name.Name, "Test") && Calls(fn.Body, "t.Parallel") > 0 &&
				match(fn.Body) {
				parallel = append(parallel, fn.Name.Name)
			}
		}
	}
	return parallel
}

// Funcs indexes the top-level functions of sources, a package's embedded *_test.go files, by name.
func Funcs(t testing.TB, sources fs.FS) map[string]*ast.FuncDecl {
	t.Helper()
	funcs := map[string]*ast.FuncDecl{}
	for _, file := range parse(t, sources) {
		for _, d := range file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	return funcs
}

func parse(t testing.TB, sources fs.FS) []*ast.File {
	t.Helper()
	names, err := fs.Glob(sources, "*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*ast.File, 0, len(names))
	for _, name := range names {
		src, err := fs.ReadFile(sources, name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}

// Callee names the function that call calls: "f" for f(…), "x.f" for x.f(…), and "" for any other form.
func Callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	}
	return ""
}

// Calls counts the calls under n whose Callee is one of names.
func Calls(n ast.Node, names ...string) int {
	k := 0
	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok && slices.Contains(names, Callee(c)) {
			k++
		}
		return true
	})
	return k
}

// IsString reports whether e is the string literal s.
func IsString(e ast.Expr, s string) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && lit.Value == strconv.Quote(s)
}
