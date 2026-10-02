package provider_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/provider"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

// fakeRuntime is a containerrt.Runtime double: it records Create/Start/Stop and returns an Instance
// pointing at a caller-set test IP/Port (so the readiness probe can hit an httptest engine). It is
// the ONLY runtime the provider-runtime touches — the provider never uses a Function store.
type fakeRuntime struct {
	mu        sync.Mutex
	ip        string
	port      int
	state     containerrt.State // the state newly-created instances report
	startErr  error             // returned by the next Start, once
	created   []containerrt.WorkerSpec
	started   []containerrt.InstanceID
	stopped   []containerrt.InstanceID
	instances map[containerrt.InstanceID]containerrt.Instance
}

func newFakeRuntime(ip string, port int) *fakeRuntime {
	return &fakeRuntime{ip: ip, port: port, state: containerrt.StateRunning, instances: map[containerrt.InstanceID]containerrt.Instance{}}
}

func (f *fakeRuntime) Create(_ context.Context, spec containerrt.WorkerSpec) (containerrt.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	id := containerrt.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)
	in := containerrt.Instance{
		ID: id, Namespace: spec.Namespace, Name: spec.Name, Replica: spec.Replica,
		State: f.state, IP: f.ip, Port: f.port,
	}
	f.instances[id] = in
	return in, nil
}

func (f *fakeRuntime) Start(_ context.Context, id containerrt.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	if err := f.startErr; err != nil {
		f.startErr = nil
		return err
	}
	if in, ok := f.instances[id]; ok {
		in.State = containerrt.StateRunning
		f.instances[id] = in
	}
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, id containerrt.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	delete(f.instances, id)
	return nil
}

func (f *fakeRuntime) Status(_ context.Context, id containerrt.InstanceID) (containerrt.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.instances[id]
	if !ok {
		return containerrt.Instance{}, fault.NotFoundf("fakeRuntime.Status", "no instance %s", id)
	}
	return in, nil
}

func (f *fakeRuntime) List(_ context.Context, ns v1.NamespaceName) ([]containerrt.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]containerrt.Instance, 0, len(f.instances))
	for _, in := range f.instances {
		if in.Namespace == ns {
			out = append(out, in)
		}
	}
	return out, nil
}

func (f *fakeRuntime) Logs(context.Context, containerrt.InstanceID) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeRuntime) Exec(context.Context, containerrt.InstanceID, []string) error { return nil }
func (f *fakeRuntime) Remove(context.Context, containerrt.InstanceID) error         { return nil }
func (f *fakeRuntime) Close() error                                                 { return nil }

// stubGateway is a gateway.Gateway double recording ProgramRoutes and holding the live table.
type stubGateway struct {
	mu       sync.Mutex
	programs [][]gateway.Route
	live     []gateway.Route
}

func (g *stubGateway) ProgramRoutes(_ context.Context, routes []gateway.Route) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := make([]gateway.Route, len(routes))
	copy(cp, routes)
	g.programs = append(g.programs, cp)
	g.live = cp
	return nil
}
func (g *stubGateway) Routes(context.Context) ([]gateway.Route, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live, nil
}
func (g *stubGateway) Handler() http.Handler { return nil }
func (g *stubGateway) Close() error          { return nil }

// engineServer starts an httptest engine answering GET / with `status`, and returns its host+port.
func engineServer(t *testing.T, status int) (host string, port int, close func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	u := srv.URL[len("http://"):]
	h, p, err := net.SplitHostPort(u)
	require.NoError(t, err)
	pn, err := strconv.Atoi(p)
	require.NoError(t, err)
	return h, pn, srv.Close
}

func specFor(ip string, port int, route *provider.RouteSpec) provider.ProviderSpec {
	return provider.ProviderSpec{
		Ref:       provider.ProviderRef{Namespace: "default", Name: "lake"},
		Image:     "funcd/runtime-duckdb",
		Port:      port,
		Env:       map[string]string{"FUNCD_QUACK_PORT": strconv.Itoa(port), "QUACK_TOKEN": "tok"},
		Readiness: provider.ReadinessProbe{Path: "/", ExpectStatus: 200},
		Replicas:  1,
		Route:     route,
	}
}

// scenario: provider-deploys — Converge Creates+Starts the engine from the curated image with an
// EMPTY Command (the image entrypoint IS the engine), the caller-assembled Env passed through.
func TestConverge_provider_deploys(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	pr, err := provider.NewRuntime(provider.Deps{Runtime: rt})
	require.NoError(t, err)

	_, err = pr.Converge(context.Background(), specFor(host, port, nil))
	require.NoError(t, err)

	require.Len(t, rt.created, 1, "the engine worker is created once")
	require.Len(t, rt.started, 1, "the engine worker is started")
	spec := rt.created[0]
	require.Empty(t, spec.Command, "Command is EMPTY — the curated image entrypoint is the engine (no artifact, no shape gate)")
	require.Equal(t, "funcd/runtime-duckdb", spec.Image)
	require.Equal(t, "tok", spec.Env["QUACK_TOKEN"], "the caller-assembled env is passed verbatim")
}

// scenario: provider-ready-on-http-probe — Ready ONLY once the engine answers ExpectStatus; a 503 ⇒
// not Ready (and no route).
func TestConverge_ready_on_http_probe(t *testing.T) {
	t.Run("answers 200 ⇒ Ready", func(t *testing.T) {
		host, port, closeFn := engineServer(t, 200)
		defer closeFn()
		rt := newFakeRuntime(host, port)
		pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt})
		st, err := pr.Converge(context.Background(), specFor(host, port, nil))
		require.NoError(t, err)
		require.True(t, st.Ready, "the engine answered 200 on the probe path")
		require.Equal(t, 1, st.Running)
	})
	t.Run("answers 503 ⇒ not Ready", func(t *testing.T) {
		host, port, closeFn := engineServer(t, 503)
		defer closeFn()
		rt := newFakeRuntime(host, port)
		pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt})
		st, err := pr.Converge(context.Background(), specFor(host, port, nil))
		require.NoError(t, err)
		require.False(t, st.Ready, "a 503 ⇒ not Ready")
		require.Equal(t, "EngineNotReady", st.Reason)
	})
}

// scenario: provider-exposed-via-gateway — Route != nil ⇒ ProgramRoutes called once Ready, with
// Upstream http://<IP>:<Port>.
func TestConverge_exposed_via_gateway(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	gw := &stubGateway{}
	pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt, Gateway: gw})

	route := &provider.RouteSpec{ID: "default/lake", PathPrefix: "/catalog/lake"}
	st, err := pr.Converge(context.Background(), specFor(host, port, route))
	require.NoError(t, err)
	require.True(t, st.Ready)
	require.Equal(t, "/catalog/lake", st.Endpoint)

	require.Len(t, gw.programs, 1, "the gateway is programmed once the engine is Ready")
	require.Len(t, gw.programs[0], 1)
	got := gw.programs[0][0]
	require.Equal(t, gateway.RouteID("default/lake"), got.ID)
	require.Equal(t, "/catalog/lake", got.PathPrefix)
	require.Equal(t, "http://"+host+":"+strconv.Itoa(port), got.Upstream, "the upstream is the engine's netns IP:Port")
}

// scenario: provider-internal-only — Route == nil ⇒ NO ProgramRoutes; status.Address is the netns
// host:port (the daemon's handle).
func TestConverge_internal_only(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	gw := &stubGateway{}
	pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt, Gateway: gw})

	st, err := pr.Converge(context.Background(), specFor(host, port, nil))
	require.NoError(t, err)
	require.Empty(t, gw.programs, "no Route declared ⇒ no gateway route programmed")
	require.Equal(t, host+":"+strconv.Itoa(port), st.Address, "the netns endpoint is published as status.Address")
	require.Empty(t, st.Endpoint, "internal-only ⇒ no ingress endpoint")
}

// scenario: provider-pinned-single-writer — Replicas=1 ⇒ exactly one engine worker; Replicas<=0 is
// rejected.
func TestConverge_pinned_single_writer(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt})

	_, err := pr.Converge(context.Background(), specFor(host, port, nil))
	require.NoError(t, err)
	require.Len(t, rt.created, 1, "exactly one replica (pinned single writer)")

	bad := specFor(host, port, nil)
	bad.Replicas = 0
	_, err = pr.Converge(context.Background(), bad)
	require.Error(t, err, "Replicas=0 is rejected (a provider is pinned, no scale-to-zero)")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// scenario: provider-not-a-function — the provider-runtime drives runtime.Runtime DIRECTLY: it never
// imports/uses a Function store or reconciler. (Structural: only the runtime double is touched; the
// engine worker carries no FUNCD_ARTIFACT/FUNCD_HANDLER shape-gate env.)
func TestConverge_not_a_function(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt})

	_, err := pr.Converge(context.Background(), specFor(host, port, nil))
	require.NoError(t, err)
	spec := rt.created[0]
	require.NotContains(t, spec.Env, "FUNCD_ARTIFACT", "a provider has no artifact (no Function shape gate)")
	require.NotContains(t, spec.Env, "FUNCD_HANDLER", "a provider has no handler (no Function shape gate)")
	require.Empty(t, spec.Command, "no shim command — the curated image entrypoint is the engine")
}

// scenario (supervision = re-convergence) — a crashed (Failed/Stopped) engine is recreated on the
// next Converge; an already-running engine is ADOPTED (not double-created).
func TestConverge_supervision_recreates_and_adopts(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt})
	ctx := context.Background()
	spec := specFor(host, port, nil)

	// first converge creates the engine.
	_, err := pr.Converge(ctx, spec)
	require.NoError(t, err)
	require.Len(t, rt.created, 1)

	// second converge with the engine still running ⇒ ADOPTED, not recreated.
	_, err = pr.Converge(ctx, spec)
	require.NoError(t, err)
	require.Len(t, rt.created, 1, "a running engine is adopted, not double-created")

	// mark the engine Failed ⇒ next converge recreates it (supervision = re-convergence).
	rt.mu.Lock()
	for id, in := range rt.instances {
		in.State = containerrt.StateFailed
		rt.instances[id] = in
	}
	rt.mu.Unlock()
	_, err = pr.Converge(ctx, spec)
	require.NoError(t, err)
	require.Len(t, rt.created, 2, "a crashed engine is recreated on the next Converge")
}

// scenario: provider-torn-down — Teardown Stops the engine and removes its programmed route.
func TestTeardown_stops_engine_and_removes_route(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	gw := &stubGateway{}
	pr, _ := provider.NewRuntime(provider.Deps{Runtime: rt, Gateway: gw})
	ctx := context.Background()

	route := &provider.RouteSpec{ID: "default/lake", PathPrefix: "/catalog/lake"}
	_, err := pr.Converge(ctx, specFor(host, port, route))
	require.NoError(t, err)
	require.Len(t, gw.programs, 1)

	err = pr.Teardown(ctx, provider.ProviderRef{Namespace: "default", Name: "lake"})
	require.NoError(t, err)
	require.NotEmpty(t, rt.stopped, "the engine worker is stopped on Teardown")
	// the route is removed: the last program is the table WITHOUT default/lake.
	last := gw.programs[len(gw.programs)-1]
	for _, r := range last {
		require.NotEqual(t, gateway.RouteID("default/lake"), r.ID, "the provider's route is removed on Teardown")
	}
}

// scenario (regression — the lima-duckdb live bug): a curated image-entrypoint engine binds the FIXED
// spec.Port in its netns (EndpointNetnsFixedPort), and its portfile-resolved Instance.Port is 0.
// Converge must probe spec.Port, NOT Instance.Port — else a real engine serving on :8080 (Instance.Port
// 0) is wrongly reported EngineNotReady. The in-process fake formerly returned Instance.Port == spec.Port
// and so missed this; here the fake returns Instance.Port 0 to mirror the real engine.
func TestConverge_uses_spec_port_not_instance_port(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, 0) // Instance.Port = 0 (portfile unresolved, like a real image engine)
	pr, err := provider.NewRuntime(provider.Deps{Runtime: rt})
	require.NoError(t, err)

	st, err := pr.Converge(context.Background(), specFor(host, port, nil)) // spec.Port = the actual engine port
	require.NoError(t, err)
	require.True(t, st.Ready, "Converge probes spec.Port (the fixed netns port), not the 0 Instance.Port")
	require.Equal(t, host+":"+strconv.Itoa(port), st.Address, "status.Address uses spec.Port, not Instance.Port")
}

// A failed Start leaves the engine Created (the containerd task stays Created); the next pass must
// start it instead of adopting it as not-terminal and reporting EngineNotReady forever (ADR-0142).
func TestIssue106_StartsEngineLeftInCreated(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	rt.state = containerrt.StateCreated
	rt.startErr = fault.Unavailablef("fakeRuntime.Start", "transient start failure")
	pr, err := provider.NewRuntime(provider.Deps{Runtime: rt})
	require.NoError(t, err)
	ctx := context.Background()
	spec := specFor(host, port, nil)

	_, err = pr.Converge(ctx, spec)
	require.Error(t, err, "the first Start fails")

	_, err = pr.Converge(ctx, spec)
	require.NoError(t, err)
	require.Len(t, rt.created, 1, "the Created engine is adopted, not recreated")
	require.Len(t, rt.started, 2, "the engine left Created is started on the next pass")

	st, err := pr.Converge(ctx, spec)
	require.NoError(t, err)
	require.True(t, st.Ready, "the started engine becomes Ready")
	require.Equal(t, 1, st.Running)
}

// processEngine runs the real process driver with a stand-in engine command (the driver needs a Command; a provider's
// engine is its image entrypoint) and records the Create and Remove calls the provider makes.
type processEngine struct {
	containerrt.Runtime
	mu    sync.Mutex
	calls []string
}

func (p *processEngine) Create(ctx context.Context, spec containerrt.WorkerSpec) (containerrt.Instance, error) {
	p.record("create")
	spec.Command = []string{"sleep", "60"}
	return p.Runtime.Create(ctx, spec)
}

func (p *processEngine) Remove(ctx context.Context, id containerrt.InstanceID) error {
	p.record("remove")
	return p.Runtime.Remove(ctx, id)
}

func (p *processEngine) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

// A stopped engine instance is removed with its files: on Teardown, and before a terminal replica is recreated
// (ADR-0143), so the driver neither lists it nor keeps its log and port files.
func TestIssue373_RemovesStoppedEngine(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	ctx := context.Background()
	spec := specFor(host, port, nil)
	newEngine := func(t *testing.T) (*processEngine, provider.Runtime) {
		t.Helper()
		rt := &processEngine{Runtime: process.New()}
		t.Cleanup(func() { _ = rt.Close() })
		pr, err := provider.NewRuntime(provider.Deps{Runtime: rt})
		require.NoError(t, err)
		_, err = pr.Converge(ctx, spec)
		require.NoError(t, err)
		return rt, pr
	}

	t.Run("teardown", func(t *testing.T) {
		rt, pr := newEngine(t)
		insts, err := rt.List(ctx, spec.Ref.Namespace)
		require.NoError(t, err)
		require.Len(t, insts, 1)
		logs, err := rt.Logs(ctx, insts[0].ID)
		require.NoError(t, err)
		logFile, ok := logs.(*os.File)
		require.True(t, ok)
		logPath := logFile.Name()
		require.NoError(t, logs.Close())

		require.NoError(t, pr.Teardown(ctx, spec.Ref))
		insts, err = rt.List(ctx, spec.Ref.Namespace)
		require.NoError(t, err)
		require.Empty(t, insts, "a torn-down provider leaves no stopped engine instance")
		require.NoFileExists(t, logPath, "the stopped engine's log file is removed")
	})

	t.Run("recreate", func(t *testing.T) {
		rt, pr := newEngine(t)
		insts, err := rt.List(ctx, spec.Ref.Namespace)
		require.NoError(t, err)
		require.Len(t, insts, 1)
		require.NoError(t, rt.Stop(ctx, insts[0].ID))

		_, err = pr.Converge(ctx, spec)
		require.NoError(t, err)
		rt.mu.Lock()
		calls := append([]string(nil), rt.calls...)
		rt.mu.Unlock()
		require.Equal(t, []string{"create", "remove", "create"}, calls, "the stopped replica is removed before it is recreated")
	})
}

// Repeated readiness probes to one engine reuse a keep-alive connection, so the converge loop does not churn
// ephemeral ports into TIME_WAIT (ADR-0041).
func TestIssue376_ReadinessProbeReusesConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	host := addr.IP.String()

	pr, err := provider.NewRuntime(provider.Deps{Runtime: newFakeRuntime(host, addr.Port)})
	require.NoError(t, err)
	spec := specFor(host, addr.Port, nil)
	for range 10 {
		st, cerr := pr.Converge(context.Background(), spec)
		require.NoError(t, cerr)
		require.True(t, st.Ready)
	}
	require.EqualValues(t, 1, conns.Load(), "10 probes must share one keep-alive connection")
}
