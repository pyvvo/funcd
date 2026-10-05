package funclog_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// gatedBucket holds every Put until release is closed, and closes entered when the first Put begins.
type gatedBucket struct {
	blob.Bucket
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.Bucket.Put(ctx, key, data, opts)
}

// A Flush takes its segment out of the sink before it Puts it, so Close must wait for that Put: Shutdown closes
// the blob as soon as Close returns, and a segment still being Put is then lost (issue #33, ADR-0081).
func TestIssue33_CloseWaitsForInFlightFlush(t *testing.T) {
	deps := func(b blob.Bucket) funclog.Deps {
		return funclog.Deps{Bucket: b, SegmentMaxBytes: 1 << 20, SegmentMaxAge: time.Hour, Clock: clock.Fake(time.Unix(1_700_000_000, 0))}
	}
	cases := []struct {
		name   string
		prefix string
		open   func(t *testing.T, b blob.Bucket) (appendOne, flush func() error, closeSink func() error)
	}{
		{name: "logs", prefix: "logs/", open: func(t *testing.T, b blob.Bucket) (func() error, func() error, func() error) {
			s, err := funclog.NewBlobSink(deps(b))
			require.NoError(t, err)
			return func() error {
					return s.Append(context.Background(), defaultRes(), funclog.Entry{Body: "last words", Severity: funclog.SevInfo})
				},
				func() error { _, err := s.Flush(context.Background(), defaultRes()); return err },
				s.Close
		}},
		{name: "traces", prefix: "traces/", open: func(t *testing.T, b blob.Bucket) (func() error, func() error, func() error) {
			s, err := funclog.NewBlobTraceSink(deps(b))
			require.NoError(t, err)
			return func() error { return s.AppendSpan(context.Background(), defaultRes(), serverSpan()) },
				func() error { _, err := s.Flush(context.Background(), defaultRes()); return err },
				s.Close
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := memBucket(t)
			b := &gatedBucket{Bucket: mem, entered: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(b.release) })
			t.Cleanup(release)
			appendOne, flush, closeSink := tc.open(t, b)
			require.NoError(t, appendOne())

			flushed := make(chan error, 1)
			go func() { flushed <- flush() }()
			<-b.entered

			closed := make(chan error, 1)
			go func() { closed <- closeSink() }()
			select {
			case err := <-closed:
				require.NoError(t, err)
				objs, lerr := mem.List(context.Background(), tc.prefix)
				require.NoError(t, lerr)
				require.Len(t, objs, 1, "Close returned while a Flush was still putting its segment: the segment is lost when the blob closes next")
			case <-time.After(200 * time.Millisecond):
			}
			release()
			require.NoError(t, <-flushed)
			require.NoError(t, <-closed)
			objs, err := mem.List(context.Background(), tc.prefix)
			require.NoError(t, err)
			require.Len(t, objs, 1, "the in-flight segment is persisted by the time Close returns")
		})
	}
}

// flakyBucket fails its next `failing` Puts, as a bucket does on a transient error, then delegates.
type flakyBucket struct {
	blob.Bucket
	failing atomic.Int32
	puts    atomic.Int32
}

func (b *flakyBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	b.puts.Add(1)
	if b.failing.Add(-1) >= 0 {
		return errors.New("transient: 503 SlowDown")
	}
	return b.Bucket.Put(ctx, key, data, opts)
}

// persistedNames reads every segment under prefix and returns its log bodies or span names.
func persistedNames(t *testing.T, b blob.Bucket, prefix string) []string {
	t.Helper()
	objs, err := b.List(context.Background(), prefix)
	require.NoError(t, err)
	var names []string
	for _, o := range objs {
		data, err := b.Get(context.Background(), o.Key)
		require.NoError(t, err)
		line := []byte(strings.TrimSpace(string(data)))
		if prefix == "logs/" {
			var u plog.JSONUnmarshaler
			logs, err := u.UnmarshalLogs(line)
			require.NoError(t, err)
			names = append(names, logBodies(logs)...)
			continue
		}
		var u ptrace.JSONUnmarshaler
		tr, err := u.UnmarshalTraces(line)
		require.NoError(t, err)
		spans := tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
		for i := range spans.Len() {
			names = append(names, spans.At(i).Name())
		}
	}
	return names
}

// A segment whose Put fails stays buffered, so the next Flush or Close writes it: a transient bucket error while
// the process lives loses no record (issue #705, ADR-0081, ADR-0101). What a long outage keeps is capped at twice
// the segment size, oldest records dropped first, and a failed segment is not retried on every Append.
func TestIssue705_FailedPutKeepsSegment(t *testing.T) {
	pad := map[string]string{"pad": strings.Repeat("x", 1000)}
	sinks := []struct {
		name   string
		prefix string
		open   func(t *testing.T, b blob.Bucket, maxBytes int) (add func(name string) error, flush, closeSink func() error)
	}{
		{name: "logs", prefix: "logs/", open: func(t *testing.T, b blob.Bucket, maxBytes int) (func(string) error, func() error, func() error) {
			s := newSink(t, b, maxBytes)
			return func(name string) error {
					return s.Append(context.Background(), defaultRes(), funclog.Entry{Body: name, Attrs: pad})
				},
				func() error { _, err := s.Flush(context.Background(), defaultRes()); return err },
				s.Close
		}},
		{name: "traces", prefix: "traces/", open: func(t *testing.T, b blob.Bucket, maxBytes int) (func(string) error, func() error, func() error) {
			s := newTraceSink(t, b, maxBytes)
			return func(name string) error {
					sp := serverSpan()
					sp.Name, sp.Attrs = name, pad
					return s.AppendSpan(context.Background(), defaultRes(), sp)
				},
				func() error { _, err := s.Flush(context.Background(), defaultRes()); return err },
				s.Close
		}},
	}
	for _, sk := range sinks {
		t.Run(sk.name+"/flush", func(t *testing.T) {
			mem := memBucket(t)
			b := &flakyBucket{Bucket: mem}
			b.failing.Store(1)
			add, flush, closeSink := sk.open(t, b, 1<<20)
			require.NoError(t, add("first"))
			require.Error(t, flush())
			require.NoError(t, add("second"))
			require.NoError(t, closeSink())
			require.Equal(t, []string{"first", "second"}, persistedNames(t, mem, sk.prefix))
		})
		t.Run(sk.name+"/append", func(t *testing.T) {
			mem := memBucket(t)
			b := &flakyBucket{Bucket: mem}
			b.failing.Store(1)
			add, _, closeSink := sk.open(t, b, 1500)
			require.NoError(t, add("first"))
			require.Error(t, add("second"), "the second record fills the segment, and its Put fails")
			require.NoError(t, closeSink())
			require.Equal(t, []string{"first", "second"}, persistedNames(t, mem, sk.prefix))
		})
		t.Run(sk.name+"/cap", func(t *testing.T) {
			mem := memBucket(t)
			b := &flakyBucket{Bucket: mem}
			b.failing.Store(2)
			add, _, closeSink := sk.open(t, b, 1500)
			require.NoError(t, add("r0"))
			require.Error(t, add("r1"))
			require.NoError(t, add("r2"), "a kept segment is retried after another full segment, not on every Append")
			require.Error(t, add("r3"))
			require.Equal(t, int32(2), b.puts.Load())
			require.NoError(t, closeSink())
			require.Equal(t, []string{"r2", "r3"}, persistedNames(t, mem, sk.prefix), "past twice the segment size, the oldest records go")
		})
	}
}
