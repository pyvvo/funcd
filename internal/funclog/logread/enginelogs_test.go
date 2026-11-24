package logread_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// getsBucket records the key of each Get.
type getsBucket struct {
	blob.Bucket
	mu   sync.Mutex
	gets []string
}

func (b *getsBucket) Get(ctx context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	b.gets = append(b.gets, key)
	b.mu.Unlock()
	return b.Bucket.Get(ctx, key)
}

// ADR-0342 Decision 2: an engine's segments live under <plural>.<name>, which no Function's name can be, with the engine
// resource; its read fills the kind and the name; a run's read lists none of them.
func TestEngineSegmentsAndReads(t *testing.T) {
	t.Parallel()
	require.Equal(t, "identityproviders.login", compact.EngineSegment("IdentityProvider", "login"))
	require.True(t, compact.IsEngineSegment("lakes.sales"))
	require.False(t, compact.IsEngineSegment("login"), "a Function's segment, a DNS label, has no dot")

	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	sink, err := funclog.NewBlobSink(funclog.Deps{Bucket: mem, Clock: clock.System()})
	require.NoError(t, err)
	engine := funclog.Resource{Namespace: "default", Kind: "IdentityProvider", Name: "login", Replica: "0"}
	fn := funclog.Resource{Namespace: "default", Function: "login", Replica: "0"}
	const trace = "0af7651916cd43dd8448eb211c80319c"
	require.NoError(t, sink.Append(ctx, engine, funclog.Entry{Time: time.Now(), Severity: funclog.SevInfo, Body: "engine line",
		Source: funclog.SourceStderr}))
	require.NoError(t, sink.Append(ctx, fn, funclog.Entry{Time: time.Now(), Severity: funclog.SevInfo, Body: "function line",
		Source: funclog.SourceStdout, TraceID: trace}))
	key, err := sink.Flush(ctx, engine)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key, "logs/default/identityproviders.login/"), key)
	raw, err := mem.Get(ctx, key)
	require.NoError(t, err)
	for _, want := range []string{`{"key":"source","value":{"stringValue":"engine"}}`, `{"key":"kind","value":{"stringValue":"IdentityProvider"}}`,
		`{"key":"name","value":{"stringValue":"login"}}`} {
		require.Contains(t, string(raw), want)
	}
	require.NotContains(t, string(raw), `"key":"function"`)
	require.NoError(t, sink.Close())

	gets := &getsBucket{Bucket: mem}
	r := logread.NewBlobReader(gets)
	lines, err := r.Read(ctx, logread.Query{Namespace: "default", Kind: "IdentityProvider", Name: "login"})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Equal(t, []string{"engine line", "IdentityProvider", "login", ""}, []string{lines[0].Body, lines[0].Kind, lines[0].Name,
		lines[0].Function})

	gets.mu.Lock()
	gets.gets = nil
	gets.mu.Unlock()
	lines, err = r.Read(ctx, logread.Query{Namespace: "default", TraceID: trace})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Equal(t, "function line", lines[0].Body)
	gets.mu.Lock()
	defer gets.mu.Unlock()
	for _, k := range gets.gets {
		require.NotContains(t, k, "identityproviders.login", "a run's read lists no engine segment")
	}

	_, err = r.Read(ctx, logread.Query{Namespace: "default", Kind: "Lake"})
	require.ErrorContains(t, err, "names its kind and its name")
}

// ADR-0342 Decision 2 with ADR-0083: an engine's JSONL segment compacts to Parquet under the same owner segment, the
// compacted Row unchanged, and the read still names the kind and the object.
func TestEngineSegmentsCompactToParquet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	start := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	clk := clock.NewManual(start)
	sink, err := funclog.NewBlobSink(funclog.Deps{Bucket: mem, Clock: clk})
	require.NoError(t, err)
	engine := funclog.Resource{Namespace: "default", Kind: "Lake", Name: "sales", Replica: "0"}
	require.NoError(t, sink.Append(ctx, engine, funclog.Entry{Time: start, Severity: funclog.SevInfo, Body: "lake line",
		Source: funclog.SourceStderr}))
	_, err = sink.Flush(ctx, engine)
	require.NoError(t, err)
	require.NoError(t, sink.Close())

	clk.Advance(2 * time.Hour)
	c, err := compact.New(compact.Deps{Bucket: mem, Clock: clk, Window: time.Hour})
	require.NoError(t, err)
	stats, err := c.CompactOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Windows)
	objs, err := mem.List(ctx, "logs/default/lakes.sales/")
	require.NoError(t, err)
	require.Len(t, objs, 1)
	require.True(t, strings.HasSuffix(objs[0].Key, ".parquet"), objs[0].Key)

	lines, err := logread.NewBlobReader(mem).Read(ctx, logread.Query{Namespace: "default", Kind: "Lake", Name: "sales"})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Equal(t, []string{"lake line", "stderr", "Lake", "sales", ""}, []string{lines[0].Body, lines[0].Source,
		lines[0].Kind, lines[0].Name, lines[0].Function})
}

// ADR-0347 Decision 8: a run's shim worker's records are keyed by the kind JobRun and the run, apart from a same-named
// Function's, under a shim worker's source label, not an engine's; the run's read finds them.
func TestRunRecordsCarryAShimLabel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	sink, err := funclog.NewBlobSink(funclog.Deps{Bucket: mem, Clock: clock.System()})
	require.NoError(t, err)
	run := funclog.Resource{Namespace: "default", Kind: "JobRun", Name: "hello-x1y2z", Replica: "0"}
	require.NoError(t, sink.Append(ctx, run, funclog.Entry{Time: time.Now(), Severity: funclog.SevInfo, Body: "run line",
		Source: funclog.SourceStdout}))
	key, err := sink.Flush(ctx, run)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key, "logs/default/jobruns.hello-x1y2z/"), key)
	raw, err := mem.Get(ctx, key)
	require.NoError(t, err)
	for _, want := range []string{`{"key":"source","value":{"stringValue":"function"}}`, `{"key":"kind","value":{"stringValue":"JobRun"}}`,
		`{"key":"name","value":{"stringValue":"hello-x1y2z"}}`} {
		require.Contains(t, string(raw), want)
	}
	require.NoError(t, sink.Close())
	lines, err := logread.NewBlobReader(mem).Read(ctx, logread.Query{Namespace: "default", Kind: "JobRun", Name: "hello-x1y2z"})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Equal(t, "run line", lines[0].Body)
}
