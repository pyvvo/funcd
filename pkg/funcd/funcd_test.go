package funcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/bus"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/network/egress"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/testkit/freeport"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: inmemory-boots — New(InMemory()) returns a platform with every port wired.
func TestScenarioInMemoryBoots(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)
	require.NotNil(t, p)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.NotNil(t, p.cfg.store)
	require.NotNil(t, p.cfg.blob)
	require.NotNil(t, p.cfg.bus)
	require.NotNil(t, p.cfg.runtime)
	require.NotNil(t, p.cfg.gateway)
	require.NotNil(t, p.cfg.logger)
	require.NotNil(t, p.cfg.telemetry)
}

// scenario: inmemory-ports-roundtrip — the booted platform's store, blob, bus, and
// gateway all work on real code paths (no root/containerd/network).
func TestScenarioInMemoryPortsRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(ctx) })

	// store: Create + Get a typed object.
	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cfg, ok := obj.(*v1.ConfigMap)
	require.True(t, ok)
	cfg.Name = "harness"
	cfg.Namespace = "default"
	cfg.ResourceGroup = "rg1"
	_, err = p.cfg.store.Create(ctx, cfg)
	require.NoError(t, err)
	got, err := p.cfg.store.Get(ctx, v1.KindConfigMap.GVK(), "default", "harness")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("harness"), got.GetObjectMeta().Name)

	// blob: Put + Get bytes.
	require.NoError(t, p.cfg.blob.Put(ctx, "k", []byte("v"), blob.PutOptions{}))
	data, err := p.cfg.blob.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, []byte("v"), data)

	// bus: Publish + Subscribe delivers.
	sub, err := p.cfg.bus.Subscribe(ctx, bus.Subject("harness.subj"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, p.cfg.bus.Publish(ctx, bus.Subject("harness.subj"), []byte("hi")))
	select {
	case msg := <-sub.C():
		require.Equal(t, []byte("hi"), msg.Data)
	case <-time.After(2 * time.Second):
		t.Fatal("bus did not deliver the published message")
	}

	// gateway: program a route + proxy to a test upstream.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	require.NoError(t, p.cfg.gateway.ProgramRoutes(ctx, []gateway.Route{
		{ID: "default/h", PathPrefix: "/h", Upstream: upstream.URL},
	}))
	rec := httptest.NewRecorder()
	p.cfg.gateway.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/h/x", nil))
	require.Equal(t, http.StatusOK, rec.Result().StatusCode)
}

// scenario: missing-required-dep — New() with no preset returns fault.Invalid naming
// the missing dependency and a nil *Platform (realizes ADR-0002 facade-missing-dep).
func TestScenarioMissingRequiredDep(t *testing.T) {
	t.Parallel()
	p, err := New()
	require.Error(t, err)
	require.Nil(t, p)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "store is required")
}

// Issue #41: an invoke socket dir too long for a Unix socket path fails New, instead of every function
// coming up Ready without its local API.
func TestIssue41_RejectsInvokeSocketDirOverUnixLimit(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithInvokeSocketDir(filepath.Join(t.TempDir(), strings.Repeat("d", 100))))
	if p != nil {
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	}
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "Unix socket limit")
}

// Issue #330: the temp invoke socket dir New creates when no WithInvokeSocketDir is set is removed by
// Shutdown, and by a New that fails after creating it.
func TestIssue330_TempInvokeSocketDirRemoved(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		p, err := New(InMemory())
		require.NoError(t, err)
		sock, err := p.invokeMgr.SocketFor("default", "fn")
		require.NoError(t, err)
		require.NoError(t, p.Shutdown(context.Background()))
		require.NoDirExists(t, filepath.Dir(sock))
	})
	t.Run("failed New", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
		require.NoError(t, os.Mkdir(tmp, 0o700))
		t.Setenv("TMPDIR", tmp)
		_, err := New(InMemory())
		require.ErrorContains(t, err, "Unix socket limit")
		left, err := filepath.Glob(filepath.Join(tmp, "funcd-invoke*"))
		require.NoError(t, err)
		require.Empty(t, left)
	})
}

// scenario: run-shutdown-lifecycle — Run returns nil on ctx cancel; concurrent
// Shutdown is idempotent (sync.Once) and the bus is closed afterwards.
func TestScenarioRunShutdownLifecycle(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "Run returns nil on graceful ctx cancel")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	// Second Shutdown is idempotent (no panic, no double-close).
	require.NoError(t, p.Shutdown(context.Background()))

	// The bus is closed: a Publish on the closed bus now fails.
	require.Error(t, p.cfg.bus.Publish(context.Background(), bus.Subject("x"), []byte("y")))
}

// removeProbe is a network.Manager whose Apply returns applyErr and that records whether Shutdown removed it.
type removeProbe struct {
	applyErr error
	removed  bool
}

func (r *removeProbe) Apply(context.Context, network.Policy) error { return r.applyErr }
func (r *removeProbe) Remove(context.Context) error                { r.removed = true; return nil }

// Shutdown removes the egress fence (ADR-0115) and closes the egress gateway (ADR-0117) only after the runtime's Close
// has stopped the workers, so no worker runs without them while the daemon stops.
func TestShutdown_KeepsEgressFenceUntilRuntimeClosed(t *testing.T) {
	probe := &removeProbe{}
	rt := &fenceAtClose{fence: probe}
	p, err := New(InMemory(), WithoutLogCompaction(), WithEgressIsolation(probe, network.Policy{}), func(c *config) error {
		rt.Runtime, c.runtime = c.runtime, rt
		return nil
	})
	require.NoError(t, err)
	gw := &gatewayAtClose{rt: rt}
	p.egressGateway = gw
	require.NoError(t, p.Shutdown(context.Background()))
	require.True(t, rt.closed, "Shutdown must close the runtime")
	require.False(t, rt.fenceGone, "Shutdown removed the egress fence while the runtime's workers were still running")
	require.True(t, probe.removed, "Shutdown must remove the egress fence")
	require.True(t, gw.closed, "Shutdown must close the egress gateway")
	require.True(t, gw.afterRuntime, "Shutdown closed the egress gateway while the runtime's workers were still running")
}

// When the runtime's Close fails a worker may still run, so Shutdown leaves the egress fence for the next start's Apply.
func TestShutdown_KeepsEgressFenceWhenRuntimeCloseFails(t *testing.T) {
	probe := &removeProbe{}
	rt := &fenceAtClose{fence: probe, err: errors.New("worker still running")}
	p, err := New(InMemory(), WithoutLogCompaction(), WithEgressIsolation(probe, network.Policy{}), func(c *config) error {
		rt.Runtime, c.runtime = c.runtime, rt
		return nil
	})
	require.NoError(t, err)
	require.ErrorIs(t, p.Shutdown(context.Background()), rt.err)
	require.False(t, probe.removed, "Shutdown removed the egress fence although the runtime could not stop its workers")
}

// gatewayAtClose is an egress gateway that records whether the runtime was already closed when Close ran.
type gatewayAtClose struct {
	egress.Gateway
	rt                   *fenceAtClose
	closed, afterRuntime bool
}

func (g *gatewayAtClose) Close() error {
	g.closed, g.afterRuntime = true, g.rt.closed
	return nil
}

// fenceAtClose is a runtime that records whether the egress fence was already removed when Close ran.
type fenceAtClose struct {
	runtime.Runtime
	fence             *removeProbe
	err               error // when set, Close fails with it
	closed, fenceGone bool
}

func (r *fenceAtClose) Close() error {
	r.closed, r.fenceGone = true, r.fence.removed
	return errors.Join(r.Runtime.Close(), r.err)
}

// Issue #489: a setup error in Run (egress isolation, TLS) stops the loops Run started and shuts the platform down
// before Run returns it, so the daemon does not exit with its stores, listeners and runtime still open (ADR-0028).
func TestIssue489_RunShutsDownOnSetupError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pem")
	cases := []struct {
		name  string
		probe *removeProbe
		opts  []Option
		want  string
	}{
		{
			name:  "egress isolation",
			probe: &removeProbe{applyErr: errors.New("program nftables: permission denied")},
			want:  "apply worker egress isolation",
		},
		{
			name:  "tls provisioning",
			probe: &removeProbe{},
			opts:  []Option{WithTLS(edgetls.Spec{Mode: edgetls.ModeProvided, CertFile: missing, KeyFile: missing})},
			want:  "provision tls certs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(append([]Option{InMemory(), WithoutLogCompaction(), WithEgressIsolation(tc.probe, network.Policy{})}, tc.opts...)...)
			require.NoError(t, err)
			t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			done := make(chan error, 1)
			go func() { done <- p.Run(ctx) }()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return on its setup error")
			}
			require.ErrorContains(t, err, tc.want)

			require.True(t, tc.probe.removed, "Shutdown ran before Run returned")
			require.Error(t, p.cfg.bus.Publish(context.Background(), bus.Subject("x"), []byte("y")), "the bus is closed")
			if conn, derr := net.Dial("tcp", p.Addr()); derr == nil {
				_ = conn.Close()
				t.Fatal("the control-plane listener is still open")
			}
		})
	}
}

// A selfsigned TLS platform with no StorageDir and no artifact store fails Run with Invalid, instead of
// keeping its TLS key in the shared, predictable <os.TempDir()>/funcd-tls that another local user can
// create first and fill with a key pair of their own.
func TestRunRefusesTLSWithoutStorageDir(t *testing.T) {
	tmp, err := os.MkdirTemp("", "ftls")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)

	p, err := New(InMemory(), WithoutLogCompaction(), WithInvokeSocketDir(tmp), WithTLS(edgetls.Spec{Hosts: []string{"127.0.0.1"}}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		_, serr := os.Stat(filepath.Join(tmp, "funcd-tls", "key.pem"))
		t.Fatalf("Run served TLS without a storage dir (key in the shared temp dir: %t)", serr == nil)
	}
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "storage dir")
	require.NoDirExists(t, filepath.Join(tmp, "funcd-tls"))
}

// The control and data planes serve TLS from one provider, but each server holds its own *tls.Config: ServeTLS writes
// its server's config (the HTTP/2 setup), so a shared one was written by both serving goroutines at once.
func TestRunGivesEachTLSServerItsOwnConfig(t *testing.T) {
	tmp, err := os.MkdirTemp("", "ftls")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	dir := filepath.Join(tmp, "tls")
	p, err := New(InMemory(), WithoutLogCompaction(), WithInvokeSocketDir(tmp), WithListenAddr("127.0.0.1:0"),
		WithDataPlaneAddr("127.0.0.1:0"), WithTLS(edgetls.Spec{Mode: edgetls.ModeSelfSigned, Hosts: []string{"127.0.0.1"}, StorageDir: dir}))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	pool := x509.NewCertPool()
	require.Eventually(t, func() bool {
		pem, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
		return err == nil && pool.AppendCertsFromPEM(pem)
	}, 10*time.Second, 50*time.Millisecond, "selfsigned cert persisted")
	for _, addr := range []string{p.Addr(), p.DataPlaneAddr()} {
		require.Eventually(t, func() bool {
			c, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12})
			if err != nil {
				return false
			}
			_ = c.Close()
			return true
		}, 10*time.Second, 50*time.Millisecond, "%s serves TLS", addr)
	}
	cancel()
	<-done
	require.False(t, p.httpServer.TLSConfig == p.dataPlaneServer.TLSConfig, "the two servers share one *tls.Config")
}

// WithNodePlatform (ADR-0145) sets the platform the scheduler and the materializer share; the default is the
// daemon's own, and a malformed one is refused.
func TestWithNodePlatform(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithNodePlatform(v1.PlatformLinuxAMD64))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, v1.PlatformLinuxAMD64, p.cfg.nodePlatform)

	d, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	require.Equal(t, v1.HostPlatform(), d.cfg.nodePlatform)

	_, err = New(InMemory(), WithNodePlatform("linux"))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// Issue #350: a platform built without WithWorkflow (funcdctl dev, any InMemory embedding) runs the workflow
// engine with the daemon's ADR-0094 defaults, so a step that never answers times out instead of hanging.
func TestIssue350_WorkflowDefaultsWithoutWithWorkflow(t *testing.T) {
	t.Parallel()
	daemon, err := platformconfig.Load("", platformconfig.Flags{})
	require.NoError(t, err)
	stepTimeout, err := time.ParseDuration(daemon.Workflow.DefaultStepTimeout)
	require.NoError(t, err)
	retention, err := time.ParseDuration(daemon.Workflow.Retention)
	require.NoError(t, err)

	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, stepTimeout, p.cfg.workflowStepTimeout)
	require.Equal(t, retention, p.workflowRetention)
	require.Equal(t, daemon.Workflow.PayloadLimit, p.cfg.workflowPayloadLimit)
	require.Equal(t, daemon.Workflow.DefaultRetry, p.cfg.workflowDefaultRetry)

	p, err = New(WithWorkflow("", 0, 0, 1, 0), InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Zero(t, p.cfg.workflowStepTimeout, "an explicit zero still means no step timeout, in any option order")
}

// Issue #446: a platform built without WithDeadLetterQueue (funcdctl dev, any InMemory embedding) bounds the
// eventing DLQ with the daemon's ADR-0118 retention and per-namespace cap, so the retention sweep runs.
func TestIssue446_DeadLetterDefaultsWithoutWithDeadLetterQueue(t *testing.T) {
	t.Parallel()
	daemon, err := platformconfig.Load("", platformconfig.Flags{})
	require.NoError(t, err)
	retention, err := time.ParseDuration(daemon.Eventing.Deadletter.Retention)
	require.NoError(t, err)

	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, retention, p.deadletterRetention)
	require.Equal(t, daemon.Eventing.Deadletter.MaxEntries, p.deadletterMaxEntries)

	p, err = New(WithDeadLetterQueue("", 3, 0, 0), InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Zero(t, p.deadletterRetention, "an explicit zero still means no TTL")
	require.Zero(t, p.deadletterMaxEntries, "an explicit zero still means no cap")
}

// Issue #94: a failed New releases what the options and the build acquired (ADR-0014: never a partial
// platform), so the caller can fix the cause and call New again with the same data dirs.
func TestIssue94_FailedNewReleasesResources(t *testing.T) {
	t.Parallel()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	// The test binds controlAddr again after New, and another test process's :0 bind could take an ephemeral port (#758).
	controlAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(freeport.Port(t)))

	persist := []Option{
		WithWorkflow(t.TempDir(), time.Second, 0, 1, 0),
		WithDeadLetterQueue(t.TempDir(), 1, 0, 0),
	}
	var cfg *config
	capture := func(c *config) error { cfg = c; return nil }
	publish := func() error { return cfg.bus.Publish(context.Background(), bus.Subject("x"), []byte("y")) }

	p, err := New(append([]Option{InMemory(), capture, WithListenAddr(controlAddr), WithDataPlaneAddr(busy.Addr().String())}, persist...)...)
	require.ErrorContains(t, err, "bind data-plane listener")
	require.Nil(t, p)
	ln, err := net.Listen("tcp", controlAddr)
	require.NoError(t, err, "the control-plane port is released")
	require.NoError(t, ln.Close())
	require.Error(t, publish(), "the preset's bus is closed")

	_, err = New(InMemory(), capture, func(*config) error { return fault.Invalidf("test", "bad option") })
	require.Error(t, err)
	require.Error(t, publish(), "a failed option releases the preset's bus")

	p, err = New(append([]Option{InMemory()}, persist...)...)
	require.NoError(t, err, "a retry with the same data dirs succeeds")
	require.NoError(t, p.Shutdown(context.Background()))
}

const containerPoolingRefused = "worker pooling is not supported with container execution: " +
	"WithPoolShim and WithPoolShimFor cannot be combined with WithContainerExecution"

func testImageFor(rt string) string { return "registry.test/funcd/" + rt + ":v1" }

// scenario: pool-shim-with-container-execution-refused — a pool shim with container execution fails New
// with fault.Invalid, op funcd.New, and no platform.
func TestScenarioPoolShimWithContainerExecutionRefused(t *testing.T) {
	t.Parallel()
	cases := map[string][]Option{
		"WithPoolShim":    {WithPoolShim("node", "pool.mjs")},
		"WithPoolShimFor": {WithPoolShimFor("python", "python3.14", "pool.py")},
		"both":            {WithPoolShim("node", "pool.mjs"), WithPoolShimFor("python", "python3.14", "pool.py")},
	}
	for name, pool := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, err := New(append([]Option{InMemory(), WithContainerExecution(testImageFor)}, pool...)...)
			require.Nil(t, p)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			var fe *fault.Error
			require.ErrorAs(t, err, &fe)
			require.Equal(t, "funcd.New", fe.Op)
			require.Equal(t, containerPoolingRefused, fe.Msg)
		})
	}
}

// scenario: container-execution-without-pool-shim-accepted — container execution alone passes the check.
func TestScenarioContainerExecutionWithoutPoolShimAccepted(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithContainerExecution(testImageFor))
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))
}

// recordingRuntime runs no process: it records every WorkerSpec the reconciler creates.
type recordingRuntime struct {
	mu    sync.Mutex
	specs []runtime.WorkerSpec
	insts map[runtime.InstanceID]runtime.Instance
}

func (r *recordingRuntime) Create(_ context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in := runtime.Instance{
		ID:        runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica),
		Namespace: spec.Namespace, Name: spec.Name, OwnerKind: spec.OwnerKind, Revision: spec.Revision,
		Replica: spec.Replica, State: runtime.StateCreated, CreatedAt: time.Now(),
	}
	r.specs = append(r.specs, spec)
	r.insts[in.ID] = in
	return in, nil
}

func (r *recordingRuntime) setState(id runtime.InstanceID, s runtime.State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.insts[id]
	if !ok {
		return fault.NotFoundf("recordingRuntime", "instance %s", id)
	}
	in.State = s
	r.insts[id] = in
	return nil
}

func (r *recordingRuntime) Start(_ context.Context, id runtime.InstanceID) error {
	return r.setState(id, runtime.StateRunning)
}

func (r *recordingRuntime) Stop(_ context.Context, id runtime.InstanceID) error {
	_ = r.setState(id, runtime.StateStopped)
	return nil
}

func (r *recordingRuntime) Status(_ context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.insts[id]
	if !ok {
		return runtime.Instance{}, fault.NotFoundf("recordingRuntime", "instance %s", id)
	}
	return in, nil
}

func (r *recordingRuntime) Logs(context.Context, runtime.InstanceID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (r *recordingRuntime) Exec(context.Context, runtime.InstanceID, []string) error { return nil }

func (r *recordingRuntime) List(_ context.Context, ns v1.NamespaceName) ([]runtime.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []runtime.Instance
	for _, in := range r.insts {
		if in.Namespace == ns {
			out = append(out, in)
		}
	}
	return out, nil
}

func (r *recordingRuntime) Remove(_ context.Context, id runtime.InstanceID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.insts, id)
	return nil
}

func (r *recordingRuntime) Close() error { return nil }

// created returns the recorded specs by worker name.
func (r *recordingRuntime) created() map[v1.ObjectName][]runtime.WorkerSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[v1.ObjectName][]runtime.WorkerSpec{}
	for _, s := range r.specs {
		out[s.Name] = append(out[s.Name], s)
	}
	return out
}

// applySharedWorker runs a platform on a recordingRuntime and applies alpha and beta, both declaring
// spec.pooling.worker "shared" with one always-on replica.
func applySharedWorker(t *testing.T, opts ...Option) *recordingRuntime {
	t.Helper()
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	p, err := New(append([]Option{InMemory(), WithRuntime(rt)}, opts...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	dir := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		art := filepath.Join(dir, name+".mjs")
		require.NoError(t, os.WriteFile(art, []byte("export function handle() { return {}; }\n"), 0o600))
		obj, _ := v1.NewObject(v1.KindFunction)
		fn := obj.(*v1.Function)
		fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
		fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+art
		fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
		fn.Spec.Pooling.Worker = "shared"
		_, err = c.Apply(context.Background(), fn)
		require.NoError(t, err)
	}
	return rt
}

// scenario: process-mode-pooling-unchanged — without container execution, two Functions sharing a
// spec.pooling.worker run in one pool worker whose manifest lists both, and neither gets a solo worker.
func TestScenarioProcessModePoolingUnchanged(t *testing.T) {
	t.Parallel()
	rt := applySharedWorker(t, WithRuntimeShim("node", "shim.mjs"), WithPoolShim("node", "pool.mjs"))
	var pool v1.ObjectName
	require.Eventually(t, func() bool {
		for name, specs := range rt.created() {
			if !strings.HasPrefix(string(name), "__pool__nodejs22__shared__") || len(specs) == 0 {
				continue
			}
			manifest, err := os.ReadFile(specs[len(specs)-1].Env["FUNCD_POOL_MANIFEST"])
			if err == nil && strings.Contains(string(manifest), `"alpha"`) && strings.Contains(string(manifest), `"beta"`) {
				pool = name
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "one pool worker hosts alpha and beta; created %v", rt.created())
	created := rt.created()
	require.Len(t, created, 1, "only the pool worker is created")
	require.Equal(t, []string{"node", "pool.mjs"}, created[pool][0].Command)
}

// scenario: pooled-function-runs-solo-in-container-mode — with container execution, two Functions sharing
// a spec.pooling.worker each run in their own worker from imageFor(runtime), and no pool worker exists.
func TestScenarioPooledFunctionRunsSoloInContainerMode(t *testing.T) {
	t.Parallel()
	rt := applySharedWorker(t, WithContainerExecution(testImageFor), WithMaterializer(function.NewFileMaterializer()))
	require.Eventually(t, func() bool {
		created := rt.created()
		return len(created["alpha"]) > 0 && len(created["beta"]) > 0
	}, 10*time.Second, 20*time.Millisecond, "alpha and beta each get a solo worker; created %v", rt.created())
	created := rt.created()
	require.Len(t, created, 2, "no worker besides alpha's and beta's")
	for _, name := range []v1.ObjectName{"alpha", "beta"} {
		for _, spec := range created[name] {
			require.Equal(t, testImageFor("nodejs22"), spec.Image, name)
			require.Empty(t, spec.Command, name)
			require.NotContains(t, spec.Env, "FUNCD_POOL_MANIFEST", name)
		}
	}
}

// WithSensorDelivery (ADR-0156) carries the delivery queue sizes into the sensor.Deps fields; a size below 1
// or a per-target cap above the workers is Invalid.
func TestWithSensorDelivery(t *testing.T) {
	t.Parallel()
	p, err := New(WithSensorDelivery(5, 2, 10), InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, []int{5, 2, 10}, []int{p.cfg.sensorMaxDeliveriesInFlight, p.cfg.sensorMaxInFlightPerTarget, p.cfg.sensorMaxQueuedPerSensor})
	for _, bad := range [][3]int{{0, 1, 1}, {4, 0, 1}, {4, 4, 0}, {4, 5, 1}} {
		_, err := New(WithSensorDelivery(bad[0], bad[1], bad[2]), InMemory())
		require.Equal(t, fault.Invalid, fault.KindOf(err), "WithSensorDelivery%v", bad)
	}
}

// WithNestedInFlightCap (ADR-0147): a negative cap is Invalid from New; 0 keeps the default.
func TestWithNestedInFlightCapValidated(t *testing.T) {
	_, err := New(WithNestedInFlightCap(-1))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	c := &config{}
	require.NoError(t, WithNestedInFlightCap(0)(c))
	require.Zero(t, c.nestedInFlightCap, "0 means the default")
	require.Equal(t, 10, defaultNestedInFlightCap)
}
