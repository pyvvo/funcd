package function_test

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// functionSrc is embedded rather than read from disk so a `go test -overlay` revert check sees the
// overlaid source.
//
//go:embed function.go
var functionSrc string

// funcDoc returns the doc comment of the function.go func named name.
func funcDoc(t *testing.T, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "function.go", functionSrc, parser.ParseComments)
	require.NoError(t, err)
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd.Doc.Text()
		}
	}
	t.Fatalf("func %s not found in function.go", name)
	return ""
}

func TestIssue352_ShimForDocDescribesOnlyShimFor(t *testing.T) {
	doc := funcDoc(t, "shimFor")
	require.True(t, strings.HasPrefix(doc, "shimFor "), "shimFor doc must start with its name, got:\n%s", doc)
	require.NotContains(t, doc, "workerSpec")
}

func TestIssue450_AddInvokeSocketDocDescribesOnlyAddInvokeSocket(t *testing.T) {
	doc := funcDoc(t, "addInvokeSocket")
	require.True(t, strings.HasPrefix(doc, "addInvokeSocket "), "addInvokeSocket doc must start with its name, got:\n%s", doc)
	require.NotContains(t, doc, "workerSpec")
	require.NotContains(t, funcDoc(t, "workerSpec"), "is pure", "workerSpec provisions the local API socket, so it is not pure")
}
