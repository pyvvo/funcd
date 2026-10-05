package s3gateway

import (
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// settableClock is a clock.Clock the test moves by hand.
type settableClock struct{ t time.Time }

func (c *settableClock) Now() time.Time { return c.t }

var _ clock.Clock = (*settableClock)(nil)

// TestIssue30_AbandonedMultipartUploadExpires: an upload whose client never completes or
// aborts it (a crashed DuckDB) is dropped once it has gone multipartIdleExpiry without a
// part, so its buffered parts do not stay in daemon RAM until a restart. A part refreshes
// the window, so a slow upload that keeps sending parts is never dropped mid-transfer.
func TestIssue30_AbandonedMultipartUploadExpires(t *testing.T) {
	t.Parallel()
	clk := &settableClock{t: time.Unix(1_700_000_000, 0)}
	m := newMultipartStore()
	m.clock = clk
	at := uploadTarget{ns: "default", bucket: "lakehouse", key: "bronze/a.parquet"}

	abandoned := m.create(at, blob.PutOptions{})
	require.NoError(t, m.putPart(abandoned, at, 1, []byte("held"), 1024))
	live := m.create(at, blob.PutOptions{})

	clk.t = clk.t.Add(multipartIdleExpiry / 2)
	idle := m.create(at, blob.PutOptions{})

	clk.t = clk.t.Add(multipartIdleExpiry/2 - time.Second)
	require.NoError(t, m.putPart(live, at, 1, []byte("fresh"), 1024))

	clk.t = clk.t.Add(2 * time.Second)
	m.create(at, blob.PutOptions{})

	_, ok := m.parts(abandoned, at)
	require.False(t, ok, "an upload idle past multipartIdleExpiry must be dropped")
	_, ok = m.parts(live, at)
	require.True(t, ok, "a part within the window refreshes it, even when the upload was created before it")
	_, ok = m.parts(idle, at)
	require.True(t, ok, "an upload created within the window is kept")
}

// packageSources is read through the embed so a `go test -overlay` revert check sees the overlaid source.
//
//go:embed *.go
var packageSources embed.FS

// TestIssue381_NoBlankVarKeepsImportAlive: no package source holds a `var _ = pkg.Name`
// whose only job is to keep an otherwise unused import compiling.
func TestIssue381_NoBlankVarKeepsImportAlive(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(packageSources, "*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := packageSources.ReadFile(name)
		require.NoError(t, err)
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		require.NoError(t, err)
		imported := map[string]bool{}
		for _, imp := range f.Imports {
			local := path.Base(strings.Trim(imp.Path.Value, `"`))
			if imp.Name != nil {
				local = imp.Name.Name
			}
			imported[local] = true
		}
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok || spec.Type != nil || len(spec.Names) != 1 || spec.Names[0].Name != "_" {
				return true
			}
			for _, v := range spec.Values {
				sel, ok := v.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && imported[pkg.Name] {
					t.Errorf("%s: blank var keeps import %q alive", fset.Position(spec.Pos()), pkg.Name)
				}
			}
			return true
		})
	}
}

// ADR-0159: an object without a digest has no ETag, and a digest's ETag is the form etag gives its bytes.
func TestObjectETagEmptyWithoutDigest(t *testing.T) {
	t.Parallel()
	require.Empty(t, objectETag(nil))
	require.Empty(t, objectETag([]byte{}))
	require.Equal(t, `"00ff"`, objectETag([]byte{0x00, 0xff}))
	require.Equal(t, `"5d41402abc4b2a76b9719d911017c592"`, etag([]byte("hello")))
}
