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
	//go:embed provider.go
	providerSrc string
	//go:embed env.go
	envSrc string
	//go:embed probe.go
	probeSrc string
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

func TestIssue516_PackageHasOneDocComment(t *testing.T) {
	var docs []string
	for name, src := range map[string]string{
		"env.go":      envSrc,
		"probe.go":    probeSrc,
		"provider.go": providerSrc,
		"runtime.go":  runtimeSrc,
		"spec.go":     specSrc,
	} {
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ParseComments|parser.PackageClauseOnly)
		require.NoError(t, err)
		if f.Doc != nil {
			docs = append(docs, name+":\n"+f.Doc.Text())
		}
	}
	require.Len(t, docs, 1, "package provider must have exactly one package doc comment, got:\n%s", strings.Join(docs, "\n"))

	doc := strings.Join(strings.Fields(docs[0]), " ")
	require.Contains(t, doc, "ADR-0082", "the package doc must describe the provider catalog")
	require.Contains(t, doc, "ADR-0087", "the package doc must describe the add-on-provider runtime")
	require.NotContains(t, doc, "leaf", "the package imports internal/gateway and internal/runtime, so it is not a leaf")
	require.NotContains(t, doc, "no runtime behavior", "the package runs engine containers")
}
