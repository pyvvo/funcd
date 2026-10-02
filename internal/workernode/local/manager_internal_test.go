package local

import (
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIssue163_LocalAPIReapsIdleKeepAliveConns: a worker can send one request per connection and then
// hold each keep-alive connection idle; without an idle deadline every held connection pins a daemon
// goroutine and its buffers for as long as the worker lives.
func TestIssue163_LocalAPIReapsIdleKeepAliveConns(t *testing.T) {
	dir, err := os.MkdirTemp("", "i163") // short: a unix socket path is capped near 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	m := NewManager(dir, nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.Close)
	_, err = m.SocketFor("team-a", "a")
	require.NoError(t, err)

	m.mu.Lock()
	srv := m.active["team-a/a"].srv
	m.mu.Unlock()
	require.Positive(t, srv.IdleTimeout, "the local API must close a keep-alive connection left idle")
}

// packageSources is the package's compiled source, so a go test -overlay of a file is what the test reads.
//
//go:embed *.go
var packageSources embed.FS

// TestIssue357_LocalAPIHasOneListenerPath: the daemon binds a local API socket only through
// Manager.SocketFor. A second, uncalled binder (the exported Serve) had no test and had to be kept in
// step with it by hand.
func TestIssue357_LocalAPIHasOneListenerPath(t *testing.T) {
	isNetListen := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Listen" {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "net"
	}
	files, err := fs.Glob(packageSources, "*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var binders []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := packageSources.ReadFile(name)
		require.NoError(t, err)
		f, err := parser.ParseFile(fset, name, src, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if isNetListen(n) {
					binders = append(binders, fn.Name.Name)
				}
				return true
			})
		}
	}
	require.Equal(t, []string{"SocketFor"}, binders, "the local API socket must be bound in one place, Manager.SocketFor")
}
