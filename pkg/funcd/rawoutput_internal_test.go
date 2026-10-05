package funcd

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// outputRuntime is captureRuntime with the platform's raw-output hook held too, so a test hands it a run's output.
type outputRuntime struct {
	captureRuntime
	outputs runtime.OutputCaptureFunc
}

func (o *outputRuntime) SetOutputCapture(fn runtime.OutputCaptureFunc) { o.outputs = fn }

// logKeysBucket records the key of every log segment Put; its Close leaves the inner bucket to the test.
type logKeysBucket struct {
	blob.Bucket
	mu   sync.Mutex
	keys []string
}

func (b *logKeysBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	if strings.HasPrefix(key, "logs/") {
		b.mu.Lock()
		b.keys = append(b.keys, key)
		b.mu.Unlock()
	}
	return b.Bucket.Put(ctx, key, data, opts)
}

func (*logKeysBucket) Close() error { return nil }

func (b *logKeysBucket) stored(prefix string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.ContainsFunc(b.keys, func(k string) bool { return strings.HasPrefix(k, prefix) })
}

// Only a Function worker's raw stdout and stderr are stored in its logs; an engine's worker keeps its tail only
// (ADR-0168).
func TestEngineWorkerRawOutputIsNotStored(t *testing.T) {
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	bucket := &logKeysBucket{Bucket: mem}
	rt := &outputRuntime{captureRuntime: captureRuntime{Runtime: process.New()}}
	p, err := New(InMemory(), WithRuntime(rt), WithBlob(bucket), WithoutLogCompaction())
	require.NoError(t, err)
	require.NotNil(t, rt.outputs, "the platform installs its raw-output hook on an OutputCapturer runtime")

	run := func(kind v1.Kind, name v1.ObjectName) *workerpipe.Output {
		out := workerpipe.New(workerpipe.Options{})
		rt.outputs(runtime.WorkerSpec{Namespace: "default", Name: name, OwnerKind: kind}, out)
		_, _ = out.Writer(workerpipe.Stdout).Write([]byte("out\n"))
		_, _ = out.Writer(workerpipe.Stderr).Write([]byte("err\n"))
		out.Close()
		return out
	}
	engine := run(v1.KindCatalogService, "engine")
	run(v1.KindFunction, "fn")
	runThenCancel(t, p)

	require.True(t, bucket.stored("logs/default/fn/"), "a Function worker's raw output is stored")
	require.False(t, bucket.stored("logs/default/engine/"), "an engine's raw output must not be stored")
	tail, err := io.ReadAll(engine.Tail())
	require.NoError(t, err)
	require.Equal(t, "out\nerr\n", string(tail), "an engine keeps its tail")
}
