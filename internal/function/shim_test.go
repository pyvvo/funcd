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
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/internal/testkit/realshim"
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
	removed []runtime.InstanceID
	// ADR-0143: a revision can get its own readiness endpoint, and its Starts can fail.
	revPort map[v1.ObjectName]int
	failRev map[v1.ObjectName]bool
	held    map[runtime.InstanceID]bool // a held instance runs without an endpoint, so it is never ready
	stopped map[runtime.InstanceID]bool // Stop released it, so Remove may forget it, as on the real drivers
	log     string                      // the captured stdout+stderr Logs returns for every instance
	// ADR-0149: a Create of an image in imageErr fails with its error; attempts counts every Create call.
	imageErr map[string]error
	attempts int
}

func newFakeRuntime(ip string, port int) *fakeRuntime {
	return &fakeRuntime{
		specs:   map[runtime.InstanceID]runtime.WorkerSpec{},
		state:   map[runtime.InstanceID]runtime.State{},
		created: map[runtime.InstanceID]time.Time{},
		ip:      ip, port: port,
		revPort:  map[v1.ObjectName]int{},
		failRev:  map[v1.ObjectName]bool{},
		held:     map[runtime.InstanceID]bool{},
		stopped:  map[runtime.InstanceID]bool{},
		imageErr: map[string]error{},
	}
}

func (f *fakeRuntime) Create(_ context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	if spec.OwnerKind == "" {
		return runtime.Instance{}, fault.Invalidf("fake.Create", "spec.OwnerKind must not be empty")
	}
	id := runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if err := f.imageErr[spec.Image]; err != nil {
		return runtime.Instance{}, err
	}
	if held, ok := f.specs[id]; ok && held.OwnerKind != spec.OwnerKind {
		return runtime.Instance{}, fault.Conflictf("fake.Create", "instance %q is held by a %s worker", id, held.OwnerKind)
	}
	if st, ok := f.state[id]; ok && !st.Terminal() {
		return runtime.Instance{}, fault.Conflictf("fake.Create", "instance %q already exists", id)
	}
	f.specs[id] = spec
	f.state[id] = runtime.StateCreated
	f.created[id] = time.Now()
	delete(f.stopped, id)
	f.creates++
	return f.snapshot(id), nil
}

func (f *fakeRuntime) Start(_ context.Context, id runtime.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed || f.failRev[f.specs[id].Revision] {
		f.state[id] = runtime.StateFailed
	} else {
		f.state[id] = runtime.StateRunning
	}
	delete(f.stopped, id)
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, id runtime.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id] = runtime.StateStopped
	f.stopped[id] = true
	return nil
}

func (f *fakeRuntime) Status(_ context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot(id), nil
}

func (f *fakeRuntime) Logs(_ context.Context, _ runtime.InstanceID) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(strings.NewReader(f.log)), nil
}

func (f *fakeRuntime) setLog(log string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = log
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

func (f *fakeRuntime) Remove(_ context.Context, id runtime.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.state[id]; ok && !f.stopped[id] {
		return fault.Conflictf("fake.Remove", "instance %q has not been stopped", id)
	}
	delete(f.specs, id)
	delete(f.state, id)
	delete(f.created, id)
	delete(f.stopped, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeRuntime) Close() error { return nil }

// snapshot builds an Instance; caller holds f.mu. A running instance surfaces the endpoint.
func (f *fakeRuntime) snapshot(id runtime.InstanceID) runtime.Instance {
	spec := f.specs[id]
	in := runtime.Instance{
		ID: id, Namespace: spec.Namespace, Name: spec.Name, OwnerKind: spec.OwnerKind, Revision: spec.Revision,
		Replica: spec.Replica, State: f.state[id], CreatedAt: f.created[id],
	}
	if in.State == runtime.StateRunning && !f.held[id] {
		in.IP = f.ip
		in.Port = f.port
		if p, ok := f.revPort[spec.Revision]; ok {
			in.Port = p
		}
	}
	return in
}

// hold keeps instance id unready while it runs (true), or lets it serve (false).
func (f *fakeRuntime) hold(id runtime.InstanceID, held bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held[id] = held
}

// forget drops every instance, as a daemon restart leaves the runtime listing none.
func (f *fakeRuntime) forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.specs)
	clear(f.state)
	clear(f.created)
	clear(f.stopped)
	clear(f.held)
}

// exit marks replica 0 of name — of whichever revision runs it — as exited in state st, created age ago (a crash of a
// worker that ran that long).
func (f *fakeRuntime) exit(name v1.ObjectName, st runtime.State, age time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, spec := range f.specs {
		if spec.Name == name && spec.Replica == 0 && !f.state[id].Terminal() {
			f.state[id] = st
			f.created[id] = time.Now().Add(-age)
		}
	}
}

// exitRevision marks replica i of revision rev of name as exited in state st, created age ago.
func (f *fakeRuntime) exitRevision(name, rev v1.ObjectName, i int, st runtime.State, age time.Duration) {
	id := runtime.NewInstanceID("default", name, rev, i)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id] = st
	f.created[id] = time.Now().Add(-age)
}

// serveRevision gives revision rev its own readiness endpoint, answering status (ADR-0143 tests tell the revisions'
// workers apart by it); setStatus changes the answer.
func (f *fakeRuntime) serveRevision(t *testing.T, rev v1.ObjectName, status int) (url string, setStatus func(int)) {
	t.Helper()
	var mu sync.Mutex
	cur := status
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		code := cur
		mu.Unlock()
		if r.URL.Path == "/health/readiness" {
			w.WriteHeader(code)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	f.mu.Lock()
	f.revPort[rev] = port
	f.mu.Unlock()
	return srv.URL, func(code int) {
		mu.Lock()
		cur = code
		mu.Unlock()
	}
}

// failRevision makes later Starts of revision rev fail (true) or succeed (false).
func (f *fakeRuntime) failRevision(rev v1.ObjectName, failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failRev[rev] = failing
}

// revisionStates returns the state of each listed instance of name, by revision and replica.
func (f *fakeRuntime) revisionStates(name v1.ObjectName) map[v1.ObjectName]map[int]runtime.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[v1.ObjectName]map[int]runtime.State{}
	for id, spec := range f.specs {
		if spec.Name != name {
			continue
		}
		if out[spec.Revision] == nil {
			out[spec.Revision] = map[int]runtime.State{}
		}
		out[spec.Revision][spec.Replica] = f.state[id]
	}
	return out
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
	sch, err := singlenode.New("local", v1.HostPlatform())
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
func newContainerHarness(t *testing.T, readyStatus int, opts ...func(*function.Deps)) *shimHarness {
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
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	gw := embedded.New()
	deps := function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
		Materializer: function.NewFileMaterializer(),
		EndpointMode: function.EndpointNetnsFixedPort,
		ImageFor:     func(rtName string) string { return "funcd/runtime-" + rtName + ":latest" },
	}
	for _, opt := range opts {
		opt(&deps)
	}
	r, err := function.NewReconciler(deps)
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

// The shims name each invocation span from FUNCD_FUNCTION and fall back to "invoke" (ADR-0101), so a
// solo worker's env must carry its Function's name in process and container mode alike.
func TestSpanNameEnv_SoloWorkerCarriesFunctionName(t *testing.T) {
	t.Parallel()
	for mode, harness := range map[string]func(*testing.T) *shimHarness{
		"process":   func(t *testing.T) *shimHarness { return newShimHarness(t, http.StatusOK, false) },
		"container": func(t *testing.T) *shimHarness { return newContainerHarness(t, http.StatusOK) },
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			h := harness(t)
			h.createFn(t, "echo")
			h.reconcile(t, "echo")
			spec, ok := h.rt.specFor("echo")
			require.True(t, ok, "a worker was created")
			require.Equal(t, "echo", spec.Env["FUNCD_FUNCTION"], "the shim names its spans after the Function")
		})
	}
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

// A booting replica that accepts connections but never answers its readiness probe must not hold the pass — and with
// it the engine's shared worker — for longer than the 200 ms poll it is repeated at.
func TestIssue75_HungBootingReplicaDoesNotHoldThePass(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	hung, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hung.Close() })
	h.rt.mu.Lock()
	h.rt.revPort["hung-1"] = hung.Addr().(*net.TCPAddr).Port
	h.rt.mu.Unlock()
	h.createFn(t, "hung")

	start := time.Now()
	res := h.reconcile(t, "hung")
	elapsed := time.Since(start)

	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "hung").Status.Phase)
	require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "a booting replica is polled")
	require.Less(t, elapsed, res.RequeueAfter, "the pass waited %s on a replica that does not answer", elapsed)
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

// Issue 79: ShapeValid=False carries the error the shim wrote before it exited (ADR-0030 §4b), not a fixed message,
// both on a first deploy and for a redeploy that fails beside the serving revision (ADR-0143).
func TestIssue79_ShapeValidCarriesShimLoadError(t *testing.T) {
	t.Parallel()
	const shimErr = `funcd-shim: shape error: export "handle" is not a function`
	t.Run("first-deploy", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, true)
		h.rt.setLog("booting\n" + shimErr + "\n")
		h.createFn(t, "echo")
		h.reconcile(t, "echo")

		require.Equal(t, v1.PhaseFailed, h.getFn(t, "echo").Status.Phase)
		c := h.condition(t, "echo", "ShapeValid")
		require.Equal(t, v1.ConditionFalse, c.Status)
		require.Equal(t, shimErr, c.Message)
	})
	t.Run("redeploy-beside-serving", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch)
		h.deployReady(t, "echo")
		h.rt.setLog(shimErr + "\n")
		h.rt.failRevision("echo-2", true)
		h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "broken" })
		h.reconcile(t, "echo")
		h.reconcile(t, "echo")

		require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
		c := h.condition(t, "echo", "ShapeValid")
		require.Equal(t, v1.ConditionFalse, c.Status)
		require.Equal(t, shimErr, c.Message)
	})
}

// bringUpRealShim brings Function "echo" up to Ready on the real Node shim from a file artifact holding handler src.
func bringUpRealShim(t *testing.T, src string) gateway.Gateway {
	t.Helper()
	artifact := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(artifact, []byte(src), 0o600))
	return realshim.Ready(t, function.NewFileMaterializer(), "file://"+artifact, "")
}

// scenario: shim-executes-handler + reconciler-materializes-and-runs (node-gated, ADR-0030
// node lane). The real Node shim boots from a file artifact, becomes Ready, and serves the
// handler over HTTP at the gateway-resolved upstream.
func TestScenarioShimEndToEndNode(t *testing.T) {
	gw := bringUpRealShim(t, "export function handle(_, event) { return { echoed: event }; }\n")

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
	require.True(t, shimReadyOnFixedPort(t, "export function handle() {}\n"), "the shim bound the fixed FUNCD_PORT and reported readiness")
}

// A real Node shim that boots slower than the former fixed 5 s waits, as on a loaded CI runner, still reaches
// readiness, both under the reconciler and on a fixed port: the waits are bounded by funcd's own boot timeout.
func TestIssue452_RealShimWaitsOutASlowBoot(t *testing.T) {
	const slow = "await new Promise((resolve) => setTimeout(resolve, 6000));\nexport function handle() {}\n"
	t.Run("reconciled", func(t *testing.T) {
		t.Parallel()
		bringUpRealShim(t, slow)
	})
	t.Run("fixed port", func(t *testing.T) {
		t.Parallel()
		require.True(t, shimReadyOnFixedPort(t, slow), "a shim that boots in 6 s bound the fixed FUNCD_PORT and reported readiness")
	})
}

// TestIssue505_FixedPortShimStartsWhenItsReservedPortIsTaken: shimReadyOnFixedPort releases the port it reserves
// before the shim binds it, so another process can take it first. The helper must still bring the shim up.
func TestIssue505_FixedPortShimStartsWhenItsReservedPortIsTaken(t *testing.T) {
	taken := false
	takeReservedPort := func(port int) {
		if taken {
			return
		}
		l, err := net.Listen("tcp4", "0.0.0.0:"+strconv.Itoa(port))
		require.NoError(t, err)
		t.Cleanup(func() { _ = l.Close() })
		taken = true
	}
	require.True(t, shimReadyOnFixedPort(t, "export function handle() {}\n", takeReservedPort),
		"the shim reported readiness although another listener took its first port")
	require.True(t, taken, "the reserved port was taken before the shim started")
}

// shimReadyOnFixedPort starts the real Node shim on handler src with FUNCD_PORT set and reports whether it bound
// that port and reported readiness. It skips the test when the shim or node is unavailable (ADR-0030 node lane).
// The shim binds FUNCD_PORT itself, so the helper can only reserve a free port and release it, and another process
// can take it first (#505): a shim that exits on that bind collision starts again on a fresh port. beforeStart runs
// with each port before the shim starts.
func shimReadyOnFixedPort(t *testing.T, src string, beforeStart ...func(port int)) bool {
	t.Helper()
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the node-gated shim lane")
	}

	artifact := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(artifact, []byte(src), 0o600))

	const attempts = 5
	for i := 1; ; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := l.Addr().(*net.TCPAddr).Port
		require.NoError(t, l.Close())
		for _, f := range beforeStart {
			f(port)
		}
		ready, stderr := runShimOnPort(t, node, shim, artifact, port)
		if ready || !strings.Contains(stderr, "EADDRINUSE") || i == attempts {
			return ready
		}
	}
}

// runShimOnPort runs the shim with FUNCD_PORT=port until it reports readiness, exits, or outlasts the boot budget
// the reconciler gives a replica (ADR-0030 §4b). It returns whether the shim became ready and, when it exited, what
// it wrote to stderr.
func runShimOnPort(t *testing.T, node, shim, artifact string, port int) (bool, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, node, shim)
	cmd.Env = append(os.Environ(),
		"FUNCD_ARTIFACT="+artifact, "FUNCD_HANDLER=handle", "FUNCD_PORT="+strconv.Itoa(port))
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	// A listener that took the port may never answer, so each probe is bounded.
	probe := &http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/health/readiness"
	deadline := time.Now().Add(function.BootTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return false, stderr.String()
		default:
		}
		resp, gerr := probe.Get(url)
		if gerr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true, ""
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, ""
}

// NOTE (ADR-0108): the former `timer-invokes-real-handler` e2e is removed — a v2 timer PUBLISHES a
// named event rather than invoking a function. The "timer → real handler" behavior is restored end-to-end
// by ADR-0109 as `timer event → Sensor function: action → invoke` (the F69 lane).
