package main

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// dev.go is embedded rather than read from disk so the test sees the file the build sees (incl. -overlay).
//
//go:embed dev.go
var devGoSource []byte

// A doc comment in dev.go starts with the name of the function under it and never with another declared
// function's name: a drifted comment leaves its own function undocumented and doubles up on a neighbour.
func TestIssue321_DevDocCommentsSitAboveTheirFunctions(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "dev.go", devGoSource, parser.ParseComments)
	require.NoError(t, err)

	docs := map[string]*ast.CommentGroup{}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			docs[fd.Name.Name] = fd.Doc
		}
	}
	for name, doc := range docs {
		if doc == nil {
			continue
		}
		for _, line := range strings.Split(doc.Text(), "\n") {
			first, _, _ := strings.Cut(line, " ")
			if _, isFunc := docs[first]; isFunc && first != name {
				t.Errorf("the doc comment of %s contains the doc comment of %s: %q", name, first, line)
			}
		}
	}
	for _, name := range []string{"bootDev", "catalogAliases", "devShimOptions", "resolveInterpreter"} {
		require.NotNil(t, docs[name], "%s has no doc comment", name)
		require.True(t, strings.HasPrefix(docs[name].Text(), name+" "), "the doc comment of %s does not describe it: %q", name, docs[name].Text())
	}
}
