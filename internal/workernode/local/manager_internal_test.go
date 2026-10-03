package local

import (
	"bytes"
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
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

// TestIssue454_LocalAPIServerErrorsUseTheManagerLogger: net/http logs its own server errors (an accept
// error, a recovered panic) through the http.Server's ErrorLog, and through the stdlib log package when it
// is nil, so they bypassed the Manager's logger and its format.
func TestIssue454_LocalAPIServerErrorsUseTheManagerLogger(t *testing.T) {
	dir, err := os.MkdirTemp("", "i454") // short: a unix socket path is capped near 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	var logs bytes.Buffer
	m := NewManager(dir, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(m.Close)
	_, err = m.SocketFor("team-a", "a")
	require.NoError(t, err)

	m.mu.Lock()
	srv := m.active["team-a/a"].srv
	m.mu.Unlock()
	require.NotNil(t, srv.ErrorLog, "the local API's net/http errors must go through the Manager's logger")
	srv.ErrorLog.Print("issue-454 probe")
	require.Contains(t, logs.String(), `"level":"WARN","msg":"issue-454 probe","component":"workernode.local"`)
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
