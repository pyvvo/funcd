package funcd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

// captureRuntime is the process runtime with the platform's capture hook held, so a test opens a worker's log
// channel itself.
type captureRuntime struct {
	runtime.Runtime
	capture runtime.LogCaptureFunc
}

func (c *captureRuntime) SetLogCapture(fn runtime.LogCaptureFunc) { c.capture = fn }

// heldChannel is a worker log channel whose backlog the daemon has not read yet: Read blocks until the channel is
// released, then yields the backlog and EOF. Half-closing its read side (CloseRead, as on a socket) releases it.
type heldChannel struct {
	backlog *strings.Reader
	open    chan struct{}
	once    sync.Once
}

func (h *heldChannel) Read(p []byte) (int, error) {
	<-h.open
	return h.backlog.Read(p)
}

func (h *heldChannel) CloseRead() error { h.release(); return nil }
func (h *heldChannel) Close() error     { h.release(); return nil }
func (h *heldChannel) release()         { h.once.Do(func() { close(h.open) }) }

// closeOrderBucket counts the log records Put before and after its Close. Close leaves the inner bucket open, so a
// Put that comes too late is still counted, and runs onClose.
type closeOrderBucket struct {
	blob.Bucket
	onClose func()

	mu            sync.Mutex
	closed        bool
	before, after int
}

func (b *closeOrderBucket) Put(ctx context.Context, key string, data []byte) error {
	if strings.HasPrefix(key, "logs/") {
		var u plog.JSONUnmarshaler
		if logs, err := u.UnmarshalLogs(bytes.TrimSpace(data)); err == nil {
			b.mu.Lock()
			if b.closed {
				b.after += logs.LogRecordCount()
			} else {
				b.before += logs.LogRecordCount()
			}
			b.mu.Unlock()
		}
	}
	return b.Bucket.Put(ctx, key, data)
}

func (b *closeOrderBucket) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.onClose()
	return nil
}

func (b *closeOrderBucket) counts() (before, after int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.before, b.after
}

// Shutdown must let every capture Route read its channel to the end and seal its segment before the blob closes:
// records a worker wrote before shutdown and the daemon had not read yet are otherwise Put after blob.Close, or
// never (issue #33, ADR-0081). The channel here is released only by a half-close, as a containerd worker's socket
// is: that runtime's Close leaves its workers running, so waiting for their EOF would hold shutdown to its bound.
func TestIssue33_ShutdownDrainsLogRoutes(t *testing.T) {
	const lines = 200
	var backlog strings.Builder
	for i := range lines {
		fmt.Fprintf(&backlog, `{"ts":%d,"sev":"INFO","body":"line %d","funcd.source":"console"}`+"\n", 1_700_000_000_000_000_000+i, i)
	}
	ch := &heldChannel{backlog: strings.NewReader(backlog.String()), open: make(chan struct{})}
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	bucket := &closeOrderBucket{Bucket: mem, onClose: ch.release}
	rt := &captureRuntime{Runtime: process.New()}

	p, err := New(InMemory(), WithRuntime(rt), WithBlob(bucket), WithoutLogCompaction())
	require.NoError(t, err)
	require.NotNil(t, rt.capture, "the platform installs its capture hook on a LogCapturer runtime")
	rt.capture(runtime.WorkerSpec{Namespace: "default", Name: "chatty"}, ch)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after ctx cancel: shutdown waited on a log channel the runtime left open")
	}

	before, after := bucket.counts()
	require.Equal(t, lines, before, "every record written before shutdown is persisted before the blob closes (%d were Put after it)", after)
}
