package fault

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Embedded rather than read from disk so the test sees the source the build compiled.
//
//go:embed problem.go
var problemSource []byte

func TestIssue327_ToProblemHasNoDeadVariable(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "problem.go", problemSource, 0)
	if err != nil {
		t.Fatalf("parse problem.go: %v", err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ToProblem" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == "_" {
					t.Errorf("%s: ToProblem assigns to the blank identifier, keeping a dead variable alive", fset.Position(as.Pos()))
				}
			}
			return true
		})
		return
	}
	t.Fatal("ToProblem not found in problem.go")
}
