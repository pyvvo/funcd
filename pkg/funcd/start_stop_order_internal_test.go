package funcd

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

// Run applies the egress fence before it starts any loop, so no reconcile can create a worker that runs unfenced
// (ADR-0115). The fence's Apply waits a while for a reconcile: a loop started before it would reconcile meanwhile.
func TestRun_AppliesTheEgressFenceBeforeAnyLoop(t *testing.T) {
	probe := &applyWaitProbe{reconciled: make(chan struct{})}
	p, err := New(InMemory(), WithoutLogCompaction(), WithEgressIsolation(probe, network.Policy{}))
	require.NoError(t, err)
	rec := &fenceSeen{probe: probe}
	p.controller.Register(v1.KindConfigMap.GVK(), rec)
	p.controller.Enqueue(controller.Request{GVK: v1.KindConfigMap.GVK(), Namespace: "default", Name: "cm"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	select {
	case <-probe.reconciled:
	case <-time.After(10 * time.Second):
		t.Fatal("the controller never reconciled the queued request")
	}
	cancel()
	require.NoError(t, <-done)
	require.True(t, rec.sawFence(), "a reconcile ran before Run applied the egress fence")
}

// applyWaitProbe is a network.Manager whose Apply waits up to 300 ms for a reconcile before the fence counts as applied.
type applyWaitProbe struct {
	reconciled chan struct{}
	mu         sync.Mutex
	applied    bool
}

func (a *applyWaitProbe) Apply(context.Context, network.Policy) error {
	select {
	case <-a.reconciled:
	case <-time.After(300 * time.Millisecond):
	}
	a.mu.Lock()
	a.applied = true
	a.mu.Unlock()
	return nil
}

func (a *applyWaitProbe) Remove(context.Context) error { return nil }

func (a *applyWaitProbe) isApplied() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.applied
}

// fenceSeen is a reconciler that records whether the fence was applied when it first ran.
type fenceSeen struct {
	probe *applyWaitProbe
	once  sync.Once
	mu    sync.Mutex
	saw   bool
}

func (f *fenceSeen) Reconcile(context.Context, controller.Request) (controller.Result, error) {
	f.once.Do(func() {
		f.mu.Lock()
		f.saw = f.probe.isApplied()
		f.mu.Unlock()
		close(f.probe.reconciled)
	})
	return controller.Result{}, nil
}

func (f *fenceSeen) sawFence() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saw
}

// A taken S3 gateway address fails Run, and Run rolls back what had started before it returns (issue #497, ADR-0028):
// the runtime closes before the fence comes down, and the bus and the control-plane listener close.
func TestRun_AFailedS3BindRollsBackWhatStarted(t *testing.T) {
	p, rt, probe, err := runWithTakenS3Port(t)
	require.ErrorIs(t, err, syscall.EADDRINUSE)
	require.True(t, rt.closed, "the runtime is still open after Run failed")
	require.True(t, probe.removed, "the egress fence is still up after Run failed")
	require.False(t, rt.fenceGone, "the fence came down before the runtime stopped its workers")
	require.Error(t, p.cfg.bus.Publish(context.Background(), bus.Subject("x"), []byte("y")), "the bus is still open")
	if conn, derr := net.Dial("tcp", p.Addr()); derr == nil {
		_ = conn.Close()
		t.Fatal("the control-plane listener is still open after Run failed")
	}
}

// scenario: a-failed-start-stops-everything
// Run's error names the provider whose Start failed (ADR-0305 Decision 4).
func TestRun_AFailedStartNamesItsProvider(t *testing.T) {
	_, _, _, err := runWithTakenS3Port(t)
	require.ErrorContains(t, err, "start blob")
}

// runWithTakenS3Port runs a platform whose S3 gateway address is taken and returns Run's error.
func runWithTakenS3Port(t *testing.T) (*Platform, *fenceAtClose, *removeProbe, error) {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	dataDir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	probe := &removeProbe{}
	rt := &fenceAtClose{fence: probe}
	p, err := New(InMemory(), WithoutLogCompaction(), WithEgressIsolation(probe, network.Policy{}),
		WithS3Gateway(held.Addr().String(), "", 0, "", dataDir), func(c *config) error {
			rt.Runtime, c.runtime = process.New(c.logger), rt
			return nil
		})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run kept serving without its S3 gateway")
	}
	return p, rt, probe, err
}

// Shutdown closes the lake PEP proxies before the runtime stops the engines behind them (ADR-0137), so no query is
// proxied to an engine that is stopping.
func TestShutdown_ClosesLakeProxiesBeforeTheRuntime(t *testing.T) {
	rt := &proxyAtClose{}
	p, err := New(InMemory(), WithoutLogCompaction(), func(c *config) error {
		rt.Runtime, c.runtime = process.New(c.logger), rt
		return nil
	})
	require.NoError(t, err)
	port, _, err := p.lakeProxy.Listen("default", "lk", 0)
	require.NoError(t, err)
	rt.addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", rt.addr, time.Second)
	require.NoError(t, err, "the lake proxy listens before Shutdown")
	_ = conn.Close()

	require.NoError(t, p.Shutdown(context.Background()))
	require.True(t, rt.closed, "Shutdown must close the runtime")
	require.False(t, rt.proxyServed, "the runtime stopped the engines while a lake proxy still accepted queries")
}

// proxyAtClose is a runtime that records whether a lake proxy still accepted connections when Close ran.
type proxyAtClose struct {
	runtime.Runtime
	addr                string
	closed, proxyServed bool
}

func (r *proxyAtClose) Close() error {
	r.closed = true
	if conn, err := net.DialTimeout("tcp", r.addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		r.proxyServed = true
	}
	return r.Runtime.Close()
}

// The log Routes drain only after the runtime has stopped the workers, so the lines a worker writes on its way out are
// kept (ADR-0081): a drain that half-closed the channels first would end each Route before those lines exist.
func TestShutdown_KeepsTheLinesAWorkerWritesWhileStopping(t *testing.T) {
	const lines = 50
	var last strings.Builder
	for i := range lines {
		last.WriteString(`{"ts":` + strconv.Itoa(1_700_000_000_000_000_000+i) + `,"sev":"INFO","body":"bye ` +
			strconv.Itoa(i) + `","funcd.source":"console"}` + "\n")
	}
	ch := &lastWordsChannel{lines: strings.NewReader(last.String()), stopping: make(chan struct{}), halfShut: make(chan struct{})}
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	bucket := &closeOrderBucket{Bucket: mem, onClose: func() { _ = ch.CloseRead() }}
	rt := &lastWordsRuntime{captureRuntime: &captureRuntime{Runtime: process.New(nil)}, ch: ch}
	p, err := New(InMemory(), WithRuntime(rt), WithBlob(bucket), WithoutLogCompaction())
	require.NoError(t, err)
	require.NotNil(t, rt.capture, "the platform installs its capture hook on a LogCapturer runtime")
	rt.capture(runtime.WorkerSpec{Namespace: "default", Name: "leaving"}, ch)

	runThenCancel(t, p)
	before, after := bucket.counts()
	require.Equal(t, lines, before, "the lines a worker wrote while the runtime stopped it are kept (%d were Put after the blob closed)", after)
}

// lastWordsRuntime is the capture runtime whose Close lets the worker's channel speak: the worker logs as it stops.
type lastWordsRuntime struct {
	*captureRuntime
	ch *lastWordsChannel
}

func (r *lastWordsRuntime) Close() error {
	r.ch.stop()
	return r.captureRuntime.Close()
}

// lastWordsChannel is a worker log channel that stays silent until the worker is stopped, then yields its lines and
// waits for the half-close. A half-close that comes first ends it before the lines exist.
type lastWordsChannel struct {
	lines              *strings.Reader
	stopping, halfShut chan struct{}
	stopOnce, shutOnce sync.Once
}

func (l *lastWordsChannel) Read(p []byte) (int, error) {
	select {
	case <-l.stopping:
	case <-l.halfShut:
		select {
		case <-l.stopping:
		default:
			return 0, io.EOF
		}
	}
	if n, err := l.lines.Read(p); n > 0 {
		return n, err
	}
	<-l.halfShut
	return 0, io.EOF
}

func (l *lastWordsChannel) CloseRead() error { l.shutOnce.Do(func() { close(l.halfShut) }); return nil }
func (l *lastWordsChannel) Close() error     { return l.CloseRead() }
func (l *lastWordsChannel) stop()            { l.stopOnce.Do(func() { close(l.stopping) }) }

// Shutdown removes the invoke socket dir and the pool manifest dir only after the runtime has stopped the workers that
// use them (issue #330, ADR-0064, ADR-0050).
func TestShutdown_KeepsWorkerDirsUntilTheWorkersStop(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "fo")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)
	rt := &dirsAtClose{dir: tmp}
	p, err := New(InMemory(), WithoutLogCompaction(), func(c *config) error {
		rt.Runtime, c.runtime = process.New(c.logger), rt
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))
	require.True(t, rt.closed, "Shutdown must close the runtime")
	require.Equal(t, []string{"funcd-invoke", "funcd-pool"}, rt.seen, "a worker dir was removed while the runtime's workers still ran")
	left, err := filepath.Glob(filepath.Join(tmp, "funcd-*"))
	require.NoError(t, err)
	require.Empty(t, left, "Shutdown leaves no worker dir behind")
}

// dirsAtClose is a runtime that records which worker dirs still existed when Close ran.
type dirsAtClose struct {
	runtime.Runtime
	dir    string
	closed bool
	seen   []string
}

func (r *dirsAtClose) Close() error {
	r.closed = true
	for _, prefix := range []string{"funcd-invoke", "funcd-pool"} {
		if found, _ := filepath.Glob(filepath.Join(r.dir, prefix+"*")); len(found) > 0 {
			r.seen = append(r.seen, prefix)
		}
	}
	return r.Runtime.Close()
}

// scenario: workers-keep-their-services-while-stopping
// Shutdown keeps the local API until the runtime has stopped the workers that call it (ADR-0305 Decision 5).
func TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop(t *testing.T) {
	rt := &apiAtClose{}
	p, err := New(InMemory(), WithoutLogCompaction(), func(c *config) error {
		rt.Runtime, c.runtime = process.New(c.logger), rt
		return nil
	})
	require.NoError(t, err)
	rt.sock, err = p.invokeMgr.SocketFor("default", "fn")
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))
	require.True(t, rt.closed, "Shutdown must close the runtime")
	require.NoError(t, rt.callErr, "a stopping worker could not reach its local API while the runtime closed")
}

// apiAtClose is a runtime that calls a worker's local API when Close runs, as a stopping worker may.
type apiAtClose struct {
	runtime.Runtime
	sock    string
	closed  bool
	callErr error
}

func (r *apiAtClose) Close() error {
	r.closed = true
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", r.sock)
	}}}
	defer c.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://local/health/dependencies", http.NoBody)
	if err == nil {
		var resp *http.Response
		if resp, err = c.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	r.callErr = err
	return r.Runtime.Close()
}
