package funclog_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
