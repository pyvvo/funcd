package s3gateway

import (
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"path"
	"strings"
	"testing"
	"time"

	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
	"github.com/versity/versitygw/s3err"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
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
	m := newMultipartStore(1024)
	m.clock = clk
	at := uploadTarget{ns: "default", bucket: "lakehouse", key: "bronze/a.parquet"}
	etl := ownerRef("etl-svc")

	abandoned := m.create(at, etl, blob.PutOptions{})
	require.NoError(t, m.putPart(abandoned, at, 1, []byte("held")))
	live := m.create(at, etl, blob.PutOptions{})

	clk.t = clk.t.Add(multipartIdleExpiry / 2)
	idle := m.create(at, etl, blob.PutOptions{})

	clk.t = clk.t.Add(multipartIdleExpiry/2 - time.Second)
	require.NoError(t, m.putPart(live, at, 1, []byte("fresh")))

	clk.t = clk.t.Add(2 * time.Second)
	m.create(at, etl, blob.PutOptions{})

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

const unitM = 1024

func ownerRef(name string) authz.EntityRef {
	return authz.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: v1.ObjectName(name)}
}

func newUnitStore() (*multipartStore, *settableClock) {
	clk := &settableClock{t: time.Unix(1_700_000_000, 0)}
	m := newMultipartStore(unitM)
	m.clock = clk
	return m, clk
}

func unitTarget(key string) uploadTarget {
	return uploadTarget{ns: "default", bucket: "lakehouse", key: "bronze/" + key}
}

// fullUpload creates an upload owned by name and buffers one part of n bytes in it.
func fullUpload(t *testing.T, m *multipartStore, name string, n int) (string, uploadTarget) {
	t.Helper()
	at := unitTarget(name)
	id := m.create(at, ownerRef(name), blob.PutOptions{})
	require.NoError(t, m.putPart(id, at, 1, make([]byte, n)))
	return id, at
}

// completeAll assembles every buffered part of the upload, in order.
func completeAll(m *multipartStore, id string, at uploadTarget) ([]byte, error) {
	listed, _ := m.parts(id, at)
	mpu := &awstypes.CompletedMultipartUpload{}
	for _, p := range listed {
		mpu.Parts = append(mpu.Parts, awstypes.CompletedPart{PartNumber: ptr(int32(p.PartNumber)), ETag: ptr(p.ETag)})
	}
	data, _, err := m.assemble(id, at, mpu)
	return data, err
}

func requireS3Code(t *testing.T, err error, code s3err.ErrorCode) {
	t.Helper()
	require.Equal(t, s3err.GetAPIError(code), err)
}

// requireAccounting recomputes the buffered bytes and each owner's bytes from the uploads and checks the
// store's counters, including that owned holds no zero entry and the budget is kept.
func requireAccounting(t *testing.T, m *multipartStore, assembling int64) {
	t.Helper()
	var buffered int64
	owned := map[authz.EntityRef]int64{}
	for _, u := range m.uploads {
		var size int64
		for _, p := range u.parts {
			size += int64(len(p))
		}
		require.Equal(t, size, u.size)
		buffered += size
		if size != 0 {
			owned[u.owner] += size
		}
	}
	require.Equal(t, buffered, m.buffered)
	require.Equal(t, assembling, m.assembling)
	require.Equal(t, owned, m.owned)
	if m.budget != 0 {
		require.LessOrEqual(t, m.buffered, m.budget-m.maxUpload)
		require.LessOrEqual(t, m.buffered+m.assembling, m.budget)
	}
}

// scenario: multipart-budget-daemon-wide (ADR-0188) — two principals at full share hold the parts limit (2M), so
// a third principal's part answers SlowDown and buffers nothing until one of them aborts.
func TestScenarioMultipartBudgetDaemonWide(t *testing.T) {
	t.Parallel()
	m, _ := newUnitStore()
	a, aAt := fullUpload(t, m, "a", unitM)
	fullUpload(t, m, "b", unitM)
	cAt := unitTarget("c")
	c := m.create(cAt, ownerRef("c"), blob.PutOptions{})

	requireS3Code(t, m.putPart(c, cAt, 1, make([]byte, unitM)), s3err.ErrSlowDown)
	listed, ok := m.parts(c, cAt)
	require.True(t, ok)
	require.Empty(t, listed, "a refused part buffers nothing")
	requireAccounting(t, m, 0)

	require.True(t, m.abort(a, aAt))
	require.NoError(t, m.putPart(c, cAt, 1, make([]byte, unitM)), "the abort freed the parts limit")
	requireAccounting(t, m, 0)
}

// scenario: complete-copy-counts-and-always-fits (ADR-0188) — a Complete's copy counts against the budget, so of
// two full uploads completing at once one copy fits and the other answers SlowDown with its parts kept.
func TestScenarioCompleteCopyCountsAndAlwaysFits(t *testing.T) {
	t.Parallel()
	m, _ := newUnitStore()
	a, aAt := fullUpload(t, m, "a", unitM)
	b, bAt := fullUpload(t, m, "b", unitM)

	data, err := completeAll(m, a, aAt)
	require.NoError(t, err, "one Complete always fits beside the parts limit")
	require.Len(t, data, unitM)
	requireAccounting(t, m, unitM)

	_, err = completeAll(m, b, bAt)
	requireS3Code(t, err, s3err.ErrSlowDown)
	listed, ok := m.parts(b, bAt)
	require.True(t, ok)
	require.Len(t, listed, 1, "a refused Complete keeps its parts")
	require.False(t, m.uploads[b].completing)
	requireAccounting(t, m, unitM)

	m.release(a, aAt, int64(len(data)), true)
	requireAccounting(t, m, 0)
	data, err = completeAll(m, b, bAt)
	require.NoError(t, err, "the retried Complete fits once the first released its copy")
	m.release(b, bAt, int64(len(data)), true)
	require.Empty(t, m.uploads)
	requireAccounting(t, m, 0)
}

// scenario: expired-upload-frees-budget-on-part (ADR-0188) — a refused part sweeps the idle uploads, so it is
// admitted once they expire even when no CreateMultipartUpload runs the sweep.
func TestScenarioExpiredUploadFreesBudgetOnPart(t *testing.T) {
	t.Parallel()
	m, clk := newUnitStore()
	start := clk.t
	a, aAt := fullUpload(t, m, "a", unitM)
	b, bAt := fullUpload(t, m, "b", unitM)

	clk.t = start.Add(30 * time.Minute)
	cAt := unitTarget("c")
	c := m.create(cAt, ownerRef("c"), blob.PutOptions{})
	requireS3Code(t, m.putPart(c, cAt, 1, make([]byte, unitM)), s3err.ErrSlowDown)

	clk.t = start.Add(61 * time.Minute)
	require.NoError(t, m.putPart(c, cAt, 1, make([]byte, unitM)), "the refusal swept the idle uploads")
	_, ok := m.parts(a, aAt)
	require.False(t, ok)
	_, ok = m.parts(b, bAt)
	require.False(t, ok)
	requireAccounting(t, m, 0)
}

// scenario: non-growing-part-does-not-hold-budget (ADR-0188) — re-sending a part at the same size is admitted but
// does not refresh the upload, so the idle uploads still expire and free the budget for another principal.
func TestScenarioNonGrowingPartDoesNotHoldBudget(t *testing.T) {
	t.Parallel()
	m, clk := newUnitStore()
	start := clk.t
	a, aAt := fullUpload(t, m, "a", unitM)
	b, bAt := fullUpload(t, m, "b", unitM)
	resend := func() {
		require.NoError(t, m.putPart(a, aAt, 1, make([]byte, unitM)))
		require.NoError(t, m.putPart(b, bAt, 1, make([]byte, unitM)))
	}

	clk.t = start.Add(20 * time.Minute)
	resend()
	clk.t = start.Add(30 * time.Minute)
	cAt := unitTarget("c")
	c := m.create(cAt, ownerRef("c"), blob.PutOptions{})
	requireS3Code(t, m.putPart(c, cAt, 1, make([]byte, unitM)), s3err.ErrSlowDown)
	clk.t = start.Add(50 * time.Minute)
	resend()

	clk.t = start.Add(61 * time.Minute)
	require.NoError(t, m.putPart(c, cAt, 1, make([]byte, unitM)), "the retry swept both uploads")
	_, ok := m.parts(a, aAt)
	require.False(t, ok)
	_, ok = m.parts(b, bAt)
	require.False(t, ok)
	requireAccounting(t, m, 0)
}

// scenario: size-cap-stays-entity-too-large (ADR-0188) — the per-upload cap is checked before the share, so a part
// that passes both answers 400 EntityTooLarge (ADR-0148 §5), not 503.
func TestScenarioSizeCapStaysEntityTooLarge(t *testing.T) {
	t.Parallel()
	m, _ := newUnitStore()
	a, aAt := fullUpload(t, m, "a", unitM-1)
	requireS3Code(t, m.putPart(a, aAt, 2, make([]byte, 2)), s3err.ErrEntityTooLarge)
	requireAccounting(t, m, 0)
}

// TestMultipartBytesReturned: every way an upload or a copy ends returns its bytes, so buffered, assembling and
// owned stay equal to what the uploads hold (ADR-0188 Decision 6-7).
func TestMultipartBytesReturned(t *testing.T) {
	t.Parallel()
	t.Run("abort", func(t *testing.T) {
		m, _ := newUnitStore()
		a, aAt := fullUpload(t, m, "a", 100)
		require.True(t, m.abort(a, aAt))
		require.Empty(t, m.owned)
		requireAccounting(t, m, 0)
	})
	t.Run("successful release drops the upload", func(t *testing.T) {
		m, _ := newUnitStore()
		a, aAt := fullUpload(t, m, "a", unitM)
		data, err := completeAll(m, a, aAt)
		require.NoError(t, err)
		requireAccounting(t, m, unitM)
		m.release(a, aAt, int64(len(data)), true)
		require.Empty(t, m.uploads)
		requireAccounting(t, m, 0)
	})
	t.Run("refused release keeps the parts and touched", func(t *testing.T) {
		m, clk := newUnitStore()
		a, aAt := fullUpload(t, m, "a", unitM)
		touched := m.uploads[a].touched
		clk.t = clk.t.Add(10 * time.Minute)
		data, err := completeAll(m, a, aAt)
		require.NoError(t, err)
		m.release(a, aAt, int64(len(data)), false)
		require.False(t, m.uploads[a].completing)
		require.Equal(t, touched, m.uploads[a].touched)
		requireAccounting(t, m, 0)
		_, err = completeAll(m, a, aAt)
		require.NoError(t, err, "the kept upload completes on a retry")
	})
	t.Run("release after an abort returns only the copy", func(t *testing.T) {
		m, _ := newUnitStore()
		a, aAt := fullUpload(t, m, "a", unitM)
		data, err := completeAll(m, a, aAt)
		require.NoError(t, err)
		require.True(t, m.abort(a, aAt))
		requireAccounting(t, m, unitM)
		m.release(a, aAt, int64(len(data)), true)
		requireAccounting(t, m, 0)
	})
	t.Run("sweep returns the bytes and skips a completing upload", func(t *testing.T) {
		m, clk := newUnitStore()
		a, aAt := fullUpload(t, m, "a", 100)
		b, bAt := fullUpload(t, m, "b", 200)
		data, err := completeAll(m, b, bAt)
		require.NoError(t, err)
		clk.t = clk.t.Add(2 * multipartIdleExpiry)
		m.create(unitTarget("c"), ownerRef("c"), blob.PutOptions{})
		_, ok := m.parts(a, aAt)
		require.False(t, ok, "the idle upload is swept")
		_, ok = m.parts(b, bAt)
		require.True(t, ok, "the sweep skips an upload a Complete holds")
		requireAccounting(t, m, 200)
		m.release(b, bAt, int64(len(data)), true)
		requireAccounting(t, m, 0)
	})
	t.Run("a smaller or regrown part at the limit is admitted and keeps touched", func(t *testing.T) {
		m, clk := newUnitStore()
		a, aAt := fullUpload(t, m, "a", unitM)
		fullUpload(t, m, "b", unitM)
		touched := m.uploads[a].touched
		clk.t = clk.t.Add(10 * time.Minute)
		require.NoError(t, m.putPart(a, aAt, 1, make([]byte, unitM/2)))
		requireAccounting(t, m, 0)
		require.NoError(t, m.putPart(a, aAt, 1, make([]byte, unitM)))
		require.Equal(t, touched, m.uploads[a].touched, "regrowing up to the peak does not refresh touched")
		require.NoError(t, m.putPart(a, aAt, 1, nil))
		require.NotContains(t, m.owned, ownerRef("a"), "an owner at 0 leaves owned")
		requireAccounting(t, m, 0)
	})
	t.Run("the budget is off when 3 × maxUpload overflows", func(t *testing.T) {
		m := newMultipartStore(math.MaxInt64)
		require.Zero(t, m.budget)
		at := unitTarget("a")
		id := m.create(at, ownerRef("a"), blob.PutOptions{})
		require.NoError(t, m.putPart(id, at, 1, make([]byte, unitM)))
		requireAccounting(t, m, 0)
	})
}
