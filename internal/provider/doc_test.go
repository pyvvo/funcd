package provider_test

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The sources are embedded rather than read from disk so a `go test -overlay` revert check sees the
// overlaid source.
var (
	//go:embed runtime.go
	runtimeSrc string
	//go:embed spec.go
	specSrc string
)

func TestIssue458_TeardownDocsSayReplicasAreRemoved(t *testing.T) {
	const want = "Teardown stops and removes every engine replica"

	rt, err := parser.ParseFile(token.NewFileSet(), "runtime.go", runtimeSrc, parser.ParseComments)
	require.NoError(t, err)
	var ifaceDoc, methodDoc string
	for _, d := range rt.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == "Runtime" {
					ifaceDoc = d.Doc.Text()
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name == "Teardown" {
				methodDoc = d.Doc.Text()
			}
		}
	}
	spec, err := parser.ParseFile(token.NewFileSet(), "spec.go", specSrc, parser.ParseComments)
	require.NoError(t, err)

	for name, doc := range map[string]string{
		"engineRuntime.Teardown": methodDoc,
		"Runtime interface":      ifaceDoc,
		"package (spec.go)":      spec.Doc.Text(),
	} {
		require.Contains(t, strings.Join(strings.Fields(doc), " "), want, "%s doc must say Teardown removes the replicas, got:\n%s", name, doc)
	}
}
