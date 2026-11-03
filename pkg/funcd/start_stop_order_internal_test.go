package funcd

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// scenario: providers-boot-in-order
// Run applies the egress fence before it starts any loop, so no reconcile can create a worker that runs unfenced
// (ADR-0115). The fence's Apply waits a while for a reconcile: a loop started before it would reconcile meanwhile.
// oracle: docs/design/platform/start-stop.fizz#NoUnfencedWorker
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

// scenario: a-failed-start-stops-everything
// A taken S3 gateway address fails Run, and Run rolls back what had started before it returns (issue #497, ADR-0028):
// the runtime closes before the fence comes down, and the bus and the control-plane listener close.
// oracle: docs/design/platform/start-stop.fizz#RuntimeClosedAtExit
func TestRun_AFailedS3BindRollsBackWhatStarted(t *testing.T) {
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
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	p, err := New(InMemory(), WithoutLogCompaction(), WithS3Gateway(held.Addr().String(), "", 0, "", t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.ErrorContains(t, p.Run(ctx), "start blob", "Run kept serving without its S3 gateway, or its error names no provider")
}

// scenario: workers-keep-their-services-while-stopping
// Shutdown closes the lake PEP proxies before the runtime stops the engines behind them (ADR-0137), so no query is
// proxied to an engine that is stopping.
func TestShutdown_ClosesLakeProxiesBeforeTheRuntime(t *testing.T) {
	rt := &proxyAtClose{}
	p, err := New(InMemory(), WithoutLogCompaction(), func(c *config) error {
		rt.Runtime, c.runtime = process.New(c.logger), rt
		return nil
	})
	require.NoError(t, err)
	port, _, err := p.lake.proxy.Listen("default", "lk", 0)
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

// scenario: workers-keep-their-services-while-stopping
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

// scenario: workers-keep-their-services-while-stopping
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

// scenario: a-worker-reaches-each-binding-on-its-local-api
// scenario: a-stopping-worker-keeps-its-local-api
// scenario: workers-keep-their-services-while-stopping
// Shutdown keeps the local API until the runtime has stopped the workers that call it (ADR-0305 Decision 5): a
// stopping worker's KV write succeeds, and its call to a cold linked function starts no worker: Run's drain has
// stopped the activator, which refuses the wake at once (503).
func TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop(t *testing.T) {
	rt := &apiAtClose{recordingRuntime: &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}}
	p, err := New(InMemory(), WithoutLogCompaction(), WithRuntime(rt))
	require.NoError(t, err)
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	ks.Name, ks.Namespace, ks.ResourceGroup = "notes", "default", "rg1"
	ks.Spec.Tables = []v1.KVTable{{Name: "last", Owner: "leaving"}}
	create(t, p.cfg.store, ks)
	for name, bindings := range map[v1.ObjectName]v1.FunctionBindings{
		"cold": {},
		"leaving": {
			KV:    []v1.FunctionKV{{Alias: "notes", Store: "notes", Table: "last"}},
			Links: []v1.FunctionLink{{Alias: "cold", Target: "cold", Timeout: v1.Duration(2 * time.Second)}},
		},
	} {
		fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
		fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
		fn.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://" + string(name) + ":1",
			Bindings: bindings}
		create(t, p.cfg.store, fn)
	}
	rt.sock, err = p.exec.sockets.SocketFor("default", "leaving")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	require.Eventually(t, func() bool {
		obj, err := p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", "cold")
		return err == nil && obj.(*v1.Function).Status.Phase == v1.PhaseIdle
	}, 10*time.Second, 5*time.Millisecond, "the controller reconciles the cold function before the daemon stops")
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	require.True(t, rt.closing.Load(), "Shutdown must close the runtime")
	require.NoError(t, rt.put.err, "a stopping worker could not reach its local API while the runtime closed")
	require.Equal(t, http.StatusNoContent, rt.put.status, "a stopping worker's KV write failed while the runtime closed")
	require.NoError(t, rt.call.err, "a stopping worker could not reach its local API while the runtime closed")
	require.Equal(t, http.StatusServiceUnavailable, rt.call.status, "a call to a cold function was not refused while the runtime closed")
	require.Empty(t, rt.lateCalls(), "a call made while the runtime closed started a worker")
}

// apiAtClose is a runtime whose Close calls a worker's local API, as a stopping worker may, and that records each
// worker created or started once Close began.
type apiAtClose struct {
	*recordingRuntime
	sock      string
	closing   atomic.Bool
	put, call reply

	lateMu sync.Mutex
	late   []string
}

// reply is a local API call's status, or the error that kept it from an answer.
type reply struct {
	status int
	err    error
}

func (r *apiAtClose) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	r.record("create " + string(spec.Name))
	return r.recordingRuntime.Create(ctx, spec)
}

func (r *apiAtClose) Start(ctx context.Context, id runtime.InstanceID) error {
	r.record("start " + string(id))
	return r.recordingRuntime.Start(ctx, id)
}

// record keeps call once Close has begun.
func (r *apiAtClose) record(call string) {
	if !r.closing.Load() {
		return
	}
	r.lateMu.Lock()
	defer r.lateMu.Unlock()
	r.late = append(r.late, call)
}

func (r *apiAtClose) lateCalls() []string {
	r.lateMu.Lock()
	defer r.lateMu.Unlock()
	return slices.Clone(r.late)
}

func (r *apiAtClose) Close() error {
	r.closing.Store(true)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", r.sock)
	}}}
	defer c.CloseIdleConnections()
	r.put = callLocalAPI(c, http.MethodPut, "/kv/notes/bye", "the last note")
	r.call = callLocalAPI(c, http.MethodPost, "/invoke/cold", "{}")
	return r.recordingRuntime.Close()
}

// callLocalAPI sends one request to a worker's local API through c.
func callLocalAPI(c *http.Client, method, path, body string) reply {
	req, err := http.NewRequestWithContext(context.Background(), method, "http://local"+path, strings.NewReader(body))
	if err != nil {
		return reply{err: err}
	}
	resp, err := c.Do(req)
	if err != nil {
		return reply{err: err}
	}
	_ = resp.Body.Close()
	return reply{status: resp.StatusCode}
}
