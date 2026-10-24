package restore_test

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// runSource is read through the embed so a `go test -overlay` revert check sees the overlaid source.
//
//go:embed run.go
var runSource string

// Issue #1244: a restore installs the escrowed master secret and puts the old one back on failure (ADR-0204), and the
// next start trusts the file. Both writes go through s3gateway.WriteMaster, which writes it durably
// (TestIssue1244_MasterSecretIsWrittenDurably); an os.WriteFile could leave it empty after a power loss, and
// LoadOrCreateMaster would then make a new master.
func TestIssue1244_RestoreWritesTheMasterDurably(t *testing.T) {
	t.Parallel()
	f, err := parser.ParseFile(token.NewFileSet(), "run.go", runSource, parser.SkipObjectResolution)
	require.NoError(t, err)
	calls := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok {
					calls[pkg.Name+"."+sel.Sel.Name]++
				}
			}
		}
		return true
	})
	require.Zero(t, calls["os.WriteFile"], "run.go writes a file with os.WriteFile")
	require.Equal(t, 2, calls["s3gateway.WriteMaster"], "the master's install and its rollback go through s3gateway.WriteMaster")
}
