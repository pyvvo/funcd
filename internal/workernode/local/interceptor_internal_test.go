package local

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

//nolint:gochecknoglobals // the -update flag of this package's tests
var update = flag.Bool("update", false, "rewrite testdata/undescribed-endpoints.txt from Endpoints")

const endpointsList = "testdata/undescribed-endpoints.txt"

//nolint:gochecknoglobals // numbers each run of a test whose endpoint must be new to the process, as under -count
var runs atomic.Int64

// scenario: undeclared-cause-logged
// A described endpoint that answers a cause it does not declare, an error with no cause, or a cause under another kind
// answers the same problem, and funcd logs one warning for each, however many handlers of the process answer it; a
// declared cause and an internal error log none. The recorder gets every error.
func TestScenarioUndeclaredCauseLogged(t *testing.T) {
	declared := fault.NewKnownCause(fault.NotFound, "test.declared", "It is gone.", "Make it.")
	other := fault.NewKnownCause(fault.Forbidden, "test.other", "It is not yours.", "Ask for it.")
	errs := map[string]error{
		"declared":   declared.Errorf("test.get", "no such thing"),
		"undeclared": other.Errorf("test.get", "not yours"),
		"none":       fault.Invalidf("test.get", "bad input"),
		"rewrapped":  fault.Wrapf(declared.Errorf("test.get", "no such thing"), fault.Conflict, "test.call", "call it"),
		"internal":   fault.Internalf("test.get", "it broke"),
	}
	prefix := fmt.Sprintf("/undeclared-%d/", runs.Add(1))
	pattern := "GET " + prefix + "{what}"
	var mu sync.Mutex
	var recorded []string
	SetErrorRecorderForTest(func(p string, c *fault.KnownCause, k fault.Kind) {
		if p != pattern {
			return
		}
		name := "none"
		if c != nil {
			name = c.Name()
		}
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, name+" "+string(k))
	})
	t.Cleanup(func() { SetErrorRecorderForTest(nil) })

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	for range 2 { // two sandboxes' handlers in one process
		mux := http.NewServeMux()
		handle(mux, logger, pattern, []*fault.KnownCause{declared}, func(_ http.ResponseWriter, r *http.Request) error {
			return errs[r.PathValue("what")]
		})
		srv := httptest.NewServer(mux)
		for range 2 {
			for name, err := range errs {
				require.Equal(t, problemOf(err), answerOf(t, srv.Client(), srv.URL+prefix+name), "%s: the answer is the problem", name)
			}
		}
		srv.Close()
	}

	type warning struct{ Msg, Endpoint, Cause, Kind string }
	var warnings []warning
	for line := range strings.Lines(logs.String()) {
		var w warning
		require.NoError(t, json.Unmarshal([]byte(line), &w))
		warnings = append(warnings, w)
	}
	require.ElementsMatch(t, []warning{
		{"the local API answered a known cause that its endpoint does not declare", pattern, "test.other", "forbidden"},
		{"the local API answered an error with no known cause", pattern, "", "invalid"},
		{"the local API answered a known cause with another kind than its own", pattern, "test.declared", "conflict"},
	}, warnings, "one warning for each, in the process")
	require.Len(t, recorded, 4*len(errs), "the recorder gets every error the interceptor answers")
	require.Contains(t, recorded, "test.declared not_found")
	require.Contains(t, recorded, "none internal")
}

// problemOf is the answer fault.WriteProblem gives err: its status, content type and body.
func problemOf(err error) string {
	rec := httptest.NewRecorder()
	fault.WriteProblem(rec, err)
	return fmt.Sprintf("%d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
}

func answerOf(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return fmt.Sprintf("%d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

// scenario: one-error-writer
// No function of the package writes a problem but the interceptor and the pool's member refusal, which answers before
// any endpoint is chosen; a function that does is named with its file.
func TestScenarioOneErrorWriter(t *testing.T) {
	require.Empty(t, problemWriters(t, packageSources), "each endpoint answers its errors through handle or wrap")
	stray := fstest.MapFS{
		"stray.go": {Data: []byte("package local\n\nfunc serveStray(w http.ResponseWriter) {\n" +
			"\tgo func() { fault.WriteProblem(w, nil) }()\n}\n")},
		"stray_test.go": {Data: []byte("package local\n\nfunc helper(w http.ResponseWriter) { fault.WriteProblem(w, nil) }\n")},
	}
	require.Equal(t, []string{"stray.go: serveStray"}, problemWriters(t, stray))
}

// problemWriters names, as "<file>: <function>", each function of the non-test files of sources that calls
// fault.WriteProblem, but wrap and poolMembers.ServeHTTP.
func problemWriters(t *testing.T, sources fs.FS) []string {
	t.Helper()
	files, err := fs.Glob(sources, "*.go")
	require.NoError(t, err)
	var out []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := fs.ReadFile(sources, name)
		require.NoError(t, err)
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			owner := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				recv := fn.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok {
					owner = id.Name + "." + owner
				}
			}
			if owner == "wrap" || owner == "poolMembers.ServeHTTP" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WriteProblem" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fault" {
						out = append(out, name+": "+owner)
					}
				}
				return true
			})
		}
	}
	return slices.Compact(out)
}

// TestEveryEndpointIsDescribedOrListed holds every endpoint to its causes, but those that
// testdata/undescribed-endpoints.txt lists, and the list only shrinks (ADR-0330 Decision 9): it fails on an endpoint
// with nil causes that the list does not hold, and on a listed one that is described or gone. go test -run
// TestEveryEndpointIsDescribedOrListed -update rewrites the list. A declared cause is never internal, is declared once
// and has a name, a when and a fix.
func TestEveryEndpointIsDescribedOrListed(t *testing.T) {
	name := regexp.MustCompile(`^[a-z]+\.[a-z0-9]+(-[a-z0-9]+)*$`)
	var undescribed []string
	seen := map[string]bool{}
	for _, e := range Endpoints() {
		require.False(t, seen[e.Pattern], "%s: one endpoint per pattern", e.Pattern)
		seen[e.Pattern] = true
		if e.Causes == nil {
			undescribed = append(undescribed, e.Pattern)
			continue
		}
		declared := map[*fault.KnownCause]bool{}
		for _, c := range e.Causes {
			require.NotEqual(t, fault.Internal, c.Kind(), "%s declares %s: an internal error is never declared", e.Pattern, c)
			require.False(t, declared[c], "%s declares %s twice", e.Pattern, c)
			declared[c] = true
			require.Regexp(t, name, c.Name(), "%s: a cause is named <package>.<name>", e.Pattern)
			require.NotEmpty(t, c.When(), "%s: %s says when", e.Pattern, c)
			require.NotEmpty(t, c.Fix(), "%s: %s says what to do", e.Pattern, c)
		}
	}
	slices.Sort(undescribed)
	if *update {
		require.NoError(t, os.WriteFile(endpointsList, []byte(strings.Join(undescribed, "\n")+"\n"), 0o644))
		return
	}
	b, err := os.ReadFile(endpointsList)
	require.NoError(t, err)
	listed := slices.DeleteFunc(strings.Split(strings.TrimSpace(string(b)), "\n"), func(s string) bool { return s == "" })
	missing := slices.DeleteFunc(slices.Clone(undescribed), func(p string) bool { return slices.Contains(listed, p) })
	require.Empty(t, missing, "endpoints with nil causes, which need their causes declared")
	stale := slices.DeleteFunc(slices.Clone(listed), func(p string) bool { return slices.Contains(undescribed, p) })
	require.Empty(t, stale, "listed endpoints that are described or gone, to remove from %s", endpointsList)
}

// Only a test sets the error recorder (ADR-0330 Decision 7): a scan of the module refuses a call of
// SetErrorRecorderForTest from a file that is not a test, and names the file.
func TestErrorRecorderIsSetOnlyByTests(t *testing.T) {
	root, err := filepath.Abs(".")
	require.NoError(t, err)
	for _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil; _, err = os.Stat(filepath.Join(root, "go.mod")) {
		require.NotEqual(t, root, filepath.Dir(root), "no go.mod above the package")
		root = filepath.Dir(root)
	}
	require.Empty(t, recorderCallers(t, os.DirFS(root)), "only a test calls SetErrorRecorderForTest")
	fixture := fstest.MapFS{
		"cmd/x/main.go":         {Data: []byte("package main\n\nfunc main() { local.SetErrorRecorderForTest(nil) }\n")},
		"pkg/y/y_test.go":       {Data: []byte("package y\n\nfunc TestY(t *testing.T) { local.SetErrorRecorderForTest(nil) }\n")},
		"internal/z/z.go":       {Data: []byte("package z\n\n// SetErrorRecorderForTest is named here, not called.\n")},
		".modcopy/w/w.go":       {Data: []byte("package w\n\nfunc init() { local.SetErrorRecorderForTest(nil) }\n")},
		"bench/v/go.mod":        {Data: []byte("module v\n")},
		"bench/v/v.go":          {Data: []byte("package v\n\nfunc init() { local.SetErrorRecorderForTest(nil) }\n")},
		"internal/q/testdata/q": {Data: []byte("not Go")},
	}
	require.Equal(t, []string{"cmd/x/main.go"}, recorderCallers(t, fixture))
}

// recorderCallers returns, sorted, each Go file of the module under root that is not a test and calls
// SetErrorRecorderForTest. It skips dot, underscore and testdata directories and nested modules, as go build does.
func recorderCallers(t *testing.T, root fs.FS) []string {
	t.Helper()
	const hook = "SetErrorRecorderForTest"
	var out []string
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "." {
				return nil
			}
			if base := d.Name(); strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") || base == "testdata" {
				return fs.SkipDir
			}
			if _, err := fs.Stat(root, path+"/go.mod"); err == nil {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := fs.ReadFile(root, path)
		if err != nil || !bytes.Contains(src, []byte(hook)) {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				ok = fun.Name == hook
			case *ast.SelectorExpr:
				ok = fun.Sel.Name == hook
			default:
				ok = false
			}
			if ok && !slices.Contains(out, path) {
				out = append(out, path)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	slices.Sort(out)
	return out
}
