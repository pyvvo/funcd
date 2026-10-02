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

func TestIssue352_ShimForDocDescribesOnlyShimFor(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "function.go", functionSrc, parser.ParseComments)
	require.NoError(t, err)
	var doc string
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "shimFor" {
			doc = fd.Doc.Text()
		}
	}
	require.True(t, strings.HasPrefix(doc, "shimFor "), "shimFor doc must start with its name, got:\n%s", doc)
	require.NotContains(t, doc, "workerSpec")
}
