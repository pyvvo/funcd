package function_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// fakeRuntime is a controllable runtime.Runtime for the shim-mode reconciler tests
// (ADR-0030): it records the WorkerSpec it was asked to run and surfaces a configurable
// endpoint (IP:Port) + state so the readiness gate can be exercised without a real shim.
type fakeRuntime struct {
	mu      sync.Mutex
	specs   map[runtime.InstanceID]runtime.WorkerSpec
	state   map[runtime.InstanceID]runtime.State
	created map[runtime.InstanceID]time.Time
	ip      string
	port    int
	failed  bool // Start marks instances Failed (the shim exited on a shape error)
	creates int
	lists   int
}

func newFakeRuntime(ip string, port int) *fakeRuntime {
	return &fakeRuntime{
		specs:   map[runtime.InstanceID]runtime.WorkerSpec{},
		state:   map[runtime.InstanceID]runtime.State{},
		created: map[runtime.InstanceID]time.Time{},
		ip:      ip, port: port,
	}
}

func (f *fakeRuntime) Create(_ context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	id := runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Replica)
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.state[id]; ok && !st.Terminal() {
		return runtime.Instance{}, fault.Conflictf("fake.Create", "instance %q already exists", id)
	}
	f.specs[id] = spec
	f.state[id] = runtime.StateCreated
	f.created[id] = time.Now()
	f.creates++
	return f.snapshot(id), nil
}

func (f *fakeRuntime) Start(_ context.Context, id runtime.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed {
		f.state[id] = runtime.StateFailed
	} else {
		f.state[id] = runtime.StateRunning
	}
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, id runtime.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id] = runtime.StateStopped
	return nil
}

func (f *fakeRuntime) Status(_ context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot(id), nil
}

func (f *fakeRuntime) Logs(_ context.Context, _ runtime.InstanceID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeRuntime) Exec(_ context.Context, _ runtime.InstanceID, _ []string) error { return nil }

func (f *fakeRuntime) List(_ context.Context, ns v1.NamespaceName) ([]runtime.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	var out []runtime.Instance
	for id, spec := range f.specs {
		if spec.Namespace == ns {
			out = append(out, f.snapshot(id))
		}
	}
	return out, nil
}

func (f *fakeRuntime) Close() error { return nil }

// snapshot builds an Instance; caller holds f.mu. A running instance surfaces the endpoint.
func (f *fakeRuntime) snapshot(id runtime.InstanceID) runtime.Instance {
	spec := f.specs[id]
	in := runtime.Instance{
		ID: id, Namespace: spec.Namespace, Name: spec.Name, Replica: spec.Replica,
		State: f.state[id], CreatedAt: f.created[id],
	}
	if in.State == runtime.StateRunning {
		in.IP = f.ip
		in.Port = f.port
	}
	return in
}

// exit marks replica 0 of name as exited in state st, created age ago (a crash of a worker that ran that long).
func (f *fakeRuntime) exit(name v1.ObjectName, st runtime.State, age time.Duration) {
	id := runtime.NewInstanceID("default", name, 0)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id] = st
	f.created[id] = time.Now().Add(-age)
}

// setFailing makes later Starts fail (true) or succeed (false).
func (f *fakeRuntime) setFailing(failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = failing
}

// counts returns how many Creates and Lists the fake has served.
func (f *fakeRuntime) counts() (creates, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.lists
}

func (f *fakeRuntime) specFor(name v1.ObjectName) (runtime.WorkerSpec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, spec := range f.specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return runtime.WorkerSpec{}, false
}

// shimHarness wires the reconciler in shim mode (ADR-0030): a FileMaterializer over a real
// artifact file + a fake runtime whose endpoint points at a controllable readiness server.
type shimHarness struct {
	r        *function.Reconciler
	st       store.Store
	rt       *fakeRuntime
	gw       gateway.Gateway
	artifact string
}

func newShimHarness(t *testing.T, readyStatus int, runtimeFailed bool, opts ...func(*function.Deps)) *shimHarness {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/readiness" {
			w.WriteHeader(readyStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	artifact := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(artifact, []byte("export function handle() {}\n"), 0o600))

	st := store.New(memory.New())
	rt := newFakeRuntime(host, port)
	rt.failed = runtimeFailed
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	gw := embedded.New()
	deps := function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: function.NewFileMaterializer(),
		ShimCommand:  []string{"node", "/opt/funcd/shim.mjs"},
	}
	for _, opt := range opts {
		opt(&deps)
	}
	r, err := function.NewReconciler(deps)
	require.NoError(t, err)
	return &shimHarness{r: r, st: st, rt: rt, gw: gw, artifact: artifact}
}

func (h *shimHarness) createFn(t *testing.T, name string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name = v1.ObjectName(name)
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime = "nodejs22"
	fn.Spec.Handler = "handle"
	fn.Spec.Image = "file://" + h.artifact
	_, err := h.st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func (h *shimHarness) reconcile(t *testing.T, name string) controller.Result {
	t.Helper()
	res, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
	return res
}

func (h *shimHarness) getFn(t *testing.T, name string) *v1.Function {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function)
}

func (h *shimHarness) routes(t *testing.T) []gateway.Route {
	t.Helper()
	rs, err := h.gw.Routes(context.Background())
	require.NoError(t, err)
	return rs
}

// scenario: shim-launches-with-artifact-and-handler + process-driver-unaffected (ADR-0032).
func TestScenarioShimLaunchesWithArtifactAndHandler(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.createFn(t, "echo")
	h.reconcile(t, "echo")

	spec, ok := h.rt.specFor("echo")
	require.True(t, ok, "a worker was created")
	require.Equal(t, []string{"node", "/opt/funcd/shim.mjs"}, spec.Command, "launches the shim, not a placeholder")
	require.Equal(t, h.artifact, spec.Env["FUNCD_ARTIFACT"], "materialized artifact path passed to the shim")
	require.Equal(t, "handle", spec.Env["FUNCD_HANDLER"], "handler export passed to the shim")
	// process-driver-unaffected (ADR-0032): no container mounts, no fixed port in process mode.
	require.Empty(t, spec.Mounts, "process mode uses no bind mounts")
	require.Empty(t, spec.Env["FUNCD_PORT"], "process mode uses the loopback+portfile handshake, not a fixed port")
}

// newContainerHarness wires the reconciler in container mode (ADR-0032): EndpointNetnsFixedPort
// + ImageFor, over the fake runtime + a controllable readiness endpoint.
func newContainerHarness(t *testing.T, readyStatus int) *shimHarness {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/readiness" {
			w.WriteHeader(readyStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	artifact := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(artifact, []byte("export function handle() {}\n"), 0o600))

	st := store.New(memory.New())
	rt := newFakeRuntime(host, port)
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	gw := embedded.New()
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: function.NewFileMaterializer(),
		EndpointMode: function.EndpointNetnsFixedPort,
		ImageFor:     func(rtName string) string { return "funcd/runtime-" + rtName + ":latest" },
	})
	require.NoError(t, err)
	return &shimHarness{r: r, st: st, rt: rt, gw: gw, artifact: artifact}
}

// scenario: spec-mounts-artifact (ADR-0032) — container mode builds the curated-image spec:
// the artifact is a read-only bind mount, FUNCD_ARTIFACT names the in-container path, the
// image comes from ImageFor, Command is empty (the shim is the image entrypoint), and
// FUNCD_PORT is the fixed netns port.
func TestScenarioContainerSpecMountsArtifact(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK)
	h.createFn(t, "echo")
	h.reconcile(t, "echo")

	spec, ok := h.rt.specFor("echo")
	require.True(t, ok, "a worker was created")
	require.Equal(t, "funcd/runtime-nodejs22:latest", spec.Image, "image from ImageFor(fn.Spec.Runtime)")
	require.Empty(t, spec.Command, "container mode runs the shim as the image entrypoint (no Command)")
	require.Len(t, spec.Mounts, 1, "the artifact is bind-mounted")
	require.Equal(t, filepath.Dir(h.artifact), spec.Mounts[0].Source)
	require.Equal(t, "/var/funcd/artifact", spec.Mounts[0].Target)
	require.True(t, spec.Mounts[0].ReadOnly, "the artifact mount is read-only")
	require.Equal(t, "/var/funcd/artifact/handler.mjs", spec.Env["FUNCD_ARTIFACT"], "FUNCD_ARTIFACT names the in-container path")
	require.Equal(t, "handle", spec.Env["FUNCD_HANDLER"])
	require.Equal(t, "8080", spec.Env["FUNCD_PORT"], "the shim binds the fixed netns port")
}

// scenario: container-readiness-gates (ADR-0032) — container mode gates Ready + route on the
// shim's readiness exactly like process mode (the addressing differs, the logic does not).
func TestScenarioContainerReadinessGates(t *testing.T) {
	t.Parallel()
	h := newContainerHarness(t, http.StatusOK)
	h.createFn(t, "echo")
	h.reconcile(t, "echo")

	require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
	require.Len(t, h.routes(t), 1, "a route was programmed to the container shim")
}

// scenario: curated-image-builds (ADR-0032, base rebased by ADR-0054) — the nodejs22
// Dockerfile carries node 22 + the shim and runs the shim as the entrypoint target. ADR-0054
// supersedes ADR-0039 on the *base only* (node 22 version kept): the base is now
// distroless/nodejs22, whose ENTRYPOINT is node, so the shim path is the CMD (node's arg).
func TestScenarioCuratedImageBuilds(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "runtime", "nodejs22", "Dockerfile"))
	require.NoError(t, err)
	df := string(data)
	require.Contains(t, df, "FROM gcr.io/distroless/nodejs22-debian12", "based on the distroless Node 22 runtime (ADR-0054)")
	require.Contains(t, df, "COPY --from=shim shim.mjs /opt/funcd/shim.mjs", "carries the funcd shim from the pinned module (ADR-0141)")
	require.Contains(t, df, `CMD ["/opt/funcd/shim.mjs"]`, "runs the shim as node's entrypoint target (distroless node ENTRYPOINT)")
}

// scenario: shim-readiness-gates-ready-and-route.
func TestScenarioShimReadinessGatesReadyAndRoute(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.createFn(t, "echo")
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "shim reported readiness → Ready")
	c, ok := fn.Status.Conditions.Get("ShapeValid")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, c.Status)
	routes := h.routes(t)
	require.Len(t, routes, 1, "a route was programmed to the ready shim")
	require.Equal(t, "http://"+h.rt.ip+":"+strconv.Itoa(h.rt.port), routes[0].Upstream, "route targets the shim's resolved IP:Port")
}

// scenario: shim-not-ready-requeues.
func TestScenarioShimNotReadyRequeues(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusServiceUnavailable, false) // shim up but not serving yet
	h.createFn(t, "echo")
	res := h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseDeploying, fn.Status.Phase, "running but not ready → keep polling")
	require.Positive(t, res.RequeueAfter, "reconcile requeues to re-poll readiness")
	require.Empty(t, h.routes(t), "no route until the shim is ready")
}

// scenario: shim-shape-failure-blocks-ready.
func TestScenarioShimShapeFailureBlocksReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, true) // shim exits → instance Failed
	h.createFn(t, "echo")
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase, "a shim that cannot load the handler → Failed")
	c, ok := fn.Status.Conditions.Get("ShapeValid")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, c.Status, "ShapeValid: False on a runtime shape failure")
	require.Empty(t, h.routes(t), "no route to a shape-failed function")
}

// bringUpRealShim runs the real Node shim via the process driver against a file artifact
// and reconciles the function until it reports readiness — the shared node-gated fixture.
// It skips the test when the shim or node is unavailable (ADR-0030 node lane).
func bringUpRealShim(t *testing.T) (store.Store, *function.Reconciler, gateway.Gateway) {
	t.Helper()
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the node-gated shim lane")
	}

	artifact := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(artifact, []byte("export function handle(_, event) { return { echoed: event }; }\n"), 0o600))

	st := store.New(memory.New())
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	gw := embedded.New()
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: function.NewFileMaterializer(),
		ShimCommand:  []string{node, shim},
	})
	require.NoError(t, err)

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "echo", "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = "file://" + artifact
	_, err = st.Create(context.Background(), fn)
	require.NoError(t, err)

	// Reconcile until the shim boots + reports readiness (the process driver assigns the port).
	var phase v1.Phase
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, rerr := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "echo"})
		require.NoError(t, rerr)
		got, gerr := st.Get(context.Background(), v1.KindFunction.GVK(), "default", "echo")
		require.NoError(t, gerr)
		phase = got.(*v1.Function).Status.Phase
		if phase == v1.PhaseReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, v1.PhaseReady, phase, "the real Node shim booted and reported readiness")
	return st, r, gw
}

// scenario: shim-executes-handler + reconciler-materializes-and-runs (node-gated, ADR-0030
// node lane). The real Node shim boots from a file artifact, becomes Ready, and serves the
// handler over HTTP at the gateway-resolved upstream.
func TestScenarioShimEndToEndNode(t *testing.T) {
	_, _, gw := bringUpRealShim(t)

	rs, err := gw.Routes(context.Background())
	require.NoError(t, err)
	require.Len(t, rs, 1)
	resp, err := http.Post(rs[0].Upstream, "application/json", strings.NewReader(`{"hello":"world"}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the shim served the handler over HTTP")
}

// scenario: shim-fixed-port-bind (node-gated, ADR-0032) — with FUNCD_PORT set the shim binds
// the fixed port on all interfaces (container mode) instead of the loopback+portfile path.
func TestScenarioShimFixedPortBindNode(t *testing.T) {
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the node-gated shim lane")
	}

	artifact := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(artifact, []byte("export function handle() {}\n"), 0o600))

	// Pick a free port, then hand it to the shim via FUNCD_PORT.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, node, shim)
	cmd.Env = append(os.Environ(),
		"FUNCD_ARTIFACT="+artifact, "FUNCD_HANDLER=handle", "FUNCD_PORT="+strconv.Itoa(port))
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/health/readiness"
	var ok bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, gerr := http.Get(url) //nolint:gosec // url is a test-local loopback address
		if gerr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ok = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.True(t, ok, "the shim bound the fixed FUNCD_PORT and reported readiness")
}

// NOTE (ADR-0108): the former `timer-invokes-real-handler` e2e is removed — a v2 timer PUBLISHES a
// named event rather than invoking a function. The "timer → real handler" behavior is restored end-to-end
// by ADR-0109 as `timer event → Sensor function: action → invoke` (the F69 lane).
