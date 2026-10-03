package funcd

import (
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// optionsSrc is embedded rather than read from disk so a `go test -overlay` revert check sees the
// overlaid source.
//
//go:embed options.go
var optionsSrc string

// optionDoc returns the doc comment of the options.go func named name.
func optionDoc(t *testing.T, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "options.go", optionsSrc, parser.ParseComments)
	require.NoError(t, err)
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd.Doc.Text()
		}
	}
	t.Fatalf("func %s not found in options.go", name)
	return ""
}

func TestIssue515_WithWorkflowDocStatesDefaultPayloadCap(t *testing.T) {
	doc := strings.Join(strings.Fields(optionDoc(t, "WithWorkflow")), " ")
	require.Contains(t, doc, fmt.Sprintf("%d KiB payload cap", defaultWorkflowPayloadLimit>>10))
	require.NotContains(t, doc, "1 MiB")
}
