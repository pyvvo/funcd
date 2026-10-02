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

// TestIssue357_LocalAPIHasOneListenerPath: the local API binds its socket in one place, shared by Serve
// and Manager.SocketFor. A second binder had no test and had to be kept in step with the first by hand.
// Any use of the net package's listen family (Listen, ListenUnix, ListenConfig, FileListener, …)
// counts as a binder.
func TestIssue357_LocalAPIHasOneListenerPath(t *testing.T) {
	files, err := fs.Glob(packageSources, "*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	binders := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := packageSources.ReadFile(name)
		require.NoError(t, err)
		f, err := parser.ParseFile(fset, name, src, 0)
		require.NoError(t, err)
		netName := ""
		for _, imp := range f.Imports {
			if imp.Path.Value == `"net"` {
				netName = "net"
				if imp.Name != nil {
					netName = imp.Name.Name
				}
			}
		}
		if netName == "" {
			continue
		}
		for _, decl := range f.Decls {
			owner := name + " (package scope)"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if ok && pkg.Name == netName && (strings.HasPrefix(sel.Sel.Name, "Listen") || sel.Sel.Name == "FileListener") {
					binders[owner] = true
				}
				return true
			})
		}
	}
	require.Len(t, binders, 1, "the local API socket must be bound in one place, shared by Serve and Manager.SocketFor; bound in %v", binders)
}
