package function_test

import (
	"bytes"
	"context"
	"encoding/binary"
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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/freeport"
)

// allowLakeQuery is a PDP that allows every lake::query, so the scenarios test the URL, not the policy.
type allowLakeQuery struct{}

func (allowLakeQuery) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}

// lakeEngine is a provider.Runtime whose engine answers at addr; with notReadyFirst its first Converge reports the
// engine not Ready yet, as an engine that is still starting.
type lakeEngine struct {
	mu            sync.Mutex
	addr          string
	notReadyFirst bool
	converges     int
}

func (e *lakeEngine) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.converges++
	return provider.ProviderStatus{Running: 1, Ready: !e.notReadyFirst || e.converges > 1, Address: e.addr}, nil
}

func (e *lakeEngine) Teardown(context.Context, provider.ProviderRef) error { return nil }

// recordingRuntime records the lake URL every worker of a Function is created with.
type recordingRuntime struct {
	*fakeRuntime
	mu   sync.Mutex
	urls map[v1.ObjectName][]string
}

func (r *recordingRuntime) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	in, err := r.fakeRuntime.Create(ctx, spec)
	if err == nil {
		r.mu.Lock()
		r.urls[spec.Name] = append(r.urls[spec.Name], spec.Env["FUNCD_LAKE_LAKE_URL"])
		r.mu.Unlock()
	}
	return in, err
}

func (r *recordingRuntime) created(name v1.ObjectName) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls[name]...)
}

// lakeWorld is what outlives a daemon run: the metastore, the node master, the engine and the workers' readiness
// endpoint.
type lakeWorld struct {
	st       store.Store
	master   []byte
	engine   string
	ready    string
	artifact string
}

func newLakeWorld(t *testing.T) *lakeWorld {
	t.Helper()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "[[42]]")
	}))
	t.Cleanup(engine.Close)
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(ready.Close)
	w := &lakeWorld{
		st:       store.New(memory.New()),
		master:   []byte("lake-url-test-master"),
		engine:   strings.TrimPrefix(engine.URL, "http://"),
		ready:    strings.TrimPrefix(ready.URL, "http://"),
		artifact: filepath.Join(t.TempDir(), "handler.mjs"),
	}
	require.NoError(t, os.WriteFile(w.artifact, []byte("export function handle() {}\n"), 0o600))
	b := &v1.Bucket{}
	b.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	b.Name, b.Namespace, b.ResourceGroup = "lakehouse", "default", "rg1"
	b.Spec.Prefixes = []v1.BucketPrefix{{Name: "gold", Owner: v1.LakeOwner("lake")}}
	_, err := w.st.Create(context.Background(), b)
	require.NoError(t, err)
	return w
}

func (w *lakeWorld) applyLake(t *testing.T) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindLake)
	require.True(t, ok)
	cs := obj.(*v1.Lake)
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", "default", "rg1"
	cs.Spec.Location = v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	cs.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}}
	_, err := w.st.Create(context.Background(), cs)
	require.NoError(t, err)
}

func (w *lakeWorld) lake(t *testing.T) *v1.Lake {
	t.Helper()
	obj, err := w.st.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	return obj.(*v1.Lake)
}

// applyFunction stores a Function with one warm replica; consumer binds lake lake under alias lake.
func (w *lakeWorld) applyFunction(t *testing.T, name string, consumer bool) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Scaling.MinReplicas = 1
	fn.Spec.Runtime = "nodejs22"
	fn.Spec.Handler = "handle"
	fn.Spec.Image = "file://" + w.artifact
	if consumer {
		fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: "lake", Lake: "lake"}}
	}
	_, err := w.st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func (w *lakeWorld) function(t *testing.T, name string) *v1.Function {
	t.Helper()
	obj, err := w.st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function)
}

// lakeRun is one daemon run over the world: a fresh lake proxy Manager, fresh reconcilers and fresh in-memory
// workers, as after a restart.
type lakeRun struct {
	w      *lakeWorld
	mgr    *lakegw.Manager
	fn     *function.Reconciler
	cat    *lakesvc.Reconciler
	rt     *recordingRuntime
	engine *lakeEngine
}

func (w *lakeWorld) run(t *testing.T, engineNotReadyFirst bool) *lakeRun {
	t.Helper()
	host, portStr, err := net.SplitHostPort(w.ready)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	rt := &recordingRuntime{fakeRuntime: newFakeRuntime(host, port), urls: map[v1.ObjectName][]string{}}
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(w.master, w.st), allowLakeQuery{}, nil)
	t.Cleanup(mgr.Shutdown)
	fn, err := function.NewReconciler(function.Deps{
		Store: w.st, Runtime: rt, Scheduler: sch, Gateway: embedded.New(), Validator: function.NewBasicValidator(),
		Materializer: function.NewFileMaterializer(), ShimCommand: []string{"node", "/opt/funcd/shim.mjs"},
		LakeMaster: w.master, LakeProxies: mgr, SupervisionPeriod: testPeriod, HandOutSettle: time.Millisecond,
	})
	require.NoError(t, err)
	engine := &lakeEngine{addr: w.engine, notReadyFirst: engineNotReadyFirst}
	cat, err := lakesvc.NewReconciler(lakesvc.ReconcilerDeps{
		Store: w.st, Provider: engine, Proxy: mgr, SupervisionPeriod: testPeriod,
		ImageFor:     func(rt string) string { return "funcd/runtime-" + rt },
		LocalCatalog: true, // the fake engine serves no save endpoint (ADR-0279)
	})
	require.NoError(t, err)
	return &lakeRun{w: w, mgr: mgr, fn: fn, cat: cat, rt: rt, engine: engine}
}

// lakePass runs lake's reconcile, then the Function passes its Lake watch maps it to (obj is the event's
// object: the stored lake, or its last state once deleted).
func (r *lakeRun) lakePass(t *testing.T, obj v1.Object) {
	t.Helper()
	ctx := context.Background()
	_, err := r.cat.Reconcile(ctx, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
	require.NoError(t, err)
	for _, req := range r.fn.MapLake(ctx, obj) {
		_, err := r.fn.Reconcile(ctx, req)
		require.NoError(t, err)
	}
}

// lakeUntilReady runs lake's passes until it is Ready, as its requeue does while the engine starts.
func (r *lakeRun) lakeUntilReady(t *testing.T) {
	t.Helper()
	for range 3 {
		r.lakePass(t, r.w.lake(t))
		if r.w.lake(t).Status.Phase == v1.PhaseReady {
			return
		}
	}
	t.Fatalf("lake lake is not Ready: %+v", r.w.lake(t).Status)
}

// fnPass runs one Function pass, then the lake passes its Function watch maps it to (obj is the event's object).
func (r *lakeRun) fnPass(t *testing.T, name string, obj v1.Object) {
	t.Helper()
	ctx := context.Background()
	_, err := r.fn.Reconcile(ctx, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
	for _, req := range r.cat.MapFunction(ctx, obj) {
		_, err := r.cat.Reconcile(ctx, req)
		require.NoError(t, err)
	}
}

func (r *lakeRun) fnPassStored(t *testing.T, name string) {
	t.Helper()
	r.fnPass(t, name, r.w.function(t, name))
}

// env returns the lake URL and token replica 0 of name's current revision was started with.
func (r *lakeRun) env(t *testing.T, name string) (url, token string) {
	t.Helper()
	fn := r.w.function(t, name)
	spec := r.rt.specOf(runtime.NewInstanceID("default", fn.Name, v1.ObjectName(fn.Status.CurrentRevision), 0))
	return spec.Env["FUNCD_LAKE_LAKE_URL"], spec.Env["FUNCD_LAKE_LAKE_TOKEN"]
}

// lakeQuery sends a Quack handshake carrying token to the lake URL a consumer was injected with, on a connection
// of its own, as a worker started in a new run dials one: a pooled connection of the previous run is closed, and a
// POST written onto it before the client notices fails with EOF (#931).
func lakeQuery(t *testing.T, url, token string) (int, string) {
	t.Helper()
	body := make([]byte, 8)
	body = append(body, 0x01, 0x00)
	body = binary.AppendUvarint(body, uint64(len(token)))
	body = append(body, token...)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(out)
}

func requireDialRefused(t *testing.T, url string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", url, time.Second)
	if err == nil {
		_ = conn.Close()
	}
	require.True(t, errors.Is(err, syscall.ECONNREFUSED), "the listener at %s is closed, got %v", url, err)
}

// readyPair applies lake and reader and brings both to Ready in run r; it returns reader's URL and token. lake's
// listener is bound first on a freeport port: the tests that free it and bind or dial it again would lose an ephemeral
// one to another test process's :0 bind (#758).
func readyPair(t *testing.T, r *lakeRun) (url, token string) {
	t.Helper()
	_, _, err := r.mgr.Listen("default", "lake", freeport.Port(t))
	require.NoError(t, err)
	r.w.applyLake(t)
	r.w.applyFunction(t, "reader", true)
	r.lakeUntilReady(t)
	r.fnPassStored(t, "reader")
	require.Equal(t, v1.PhaseReady, r.w.function(t, "reader").Status.Phase)
	url, token = r.env(t, "reader")
	require.Equal(t, r.w.lake(t).Status.Endpoint, url)
	code, body := lakeQuery(t, url, token)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "[[42]]", body)
	return url, token
}

// scenario: lake-recreate-keeps-url
func TestScenarioLakeRecreateKeepsURL(t *testing.T) {
	t.Parallel()
	w := newLakeWorld(t)
	r := w.run(t, false)
	url, token := readyPair(t, r)
	endpoint := w.lake(t).Status.Endpoint

	last := w.lake(t)
	require.NoError(t, w.st.Delete(context.Background(), v1.KindLake.GVK(), "default", "lake", ""))
	r.lakePass(t, last)
	w.applyLake(t)
	r.lakeUntilReady(t)

	require.Equal(t, endpoint, w.lake(t).Status.Endpoint, "the re-created lake keeps its endpoint")
	r.fnPassStored(t, "reader")
	require.Len(t, r.rt.created("reader"), 1, "reader's worker is not re-created")
	code, body := lakeQuery(t, url, token)
	require.Equal(t, http.StatusOK, code, "the URL reader was started with reaches the re-created lake")
	require.Equal(t, "[[42]]", body)
}

// scenario: missing-lake-shows-not-ready
func TestScenarioMissingLakeShowsNotReady(t *testing.T) {
	t.Parallel()
	w := newLakeWorld(t)
	r := w.run(t, false)
	url, token := readyPair(t, r)

	last := w.lake(t)
	require.NoError(t, w.st.Delete(context.Background(), v1.KindLake.GVK(), "default", "lake", ""))
	r.lakePass(t, last)
	fn := w.function(t, "reader")
	rr, ok := fn.Status.Conditions.Get("RevisionReady")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "LakeNotReady", rr.Reason)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "reader keeps serving")
	in, err := r.rt.Status(context.Background(), runtime.NewInstanceID("default", "reader", v1.ObjectName(fn.Status.CurrentRevision), 0))
	require.NoError(t, err)
	require.Equal(t, runtime.StateRunning, in.State, "reader's worker runs")
	code, _ := lakeQuery(t, url, token)
	require.Equal(t, http.StatusServiceUnavailable, code)

	w.applyLake(t)
	r.lakeUntilReady(t)
	rr, ok = w.function(t, "reader").Status.Conditions.Get("RevisionReady")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, rr.Status, "RevisionReady returns to True once lake is Ready again")
}

// scenario: restart-keeps-lake-port
//
// Not parallel, nor its subtests: a port bound in parallel could take the recorded port between run1's Shutdown and
// run2's rebind.
func TestScenarioRestartKeepsLakePort(t *testing.T) {
	for _, consumerFirst := range []bool{true, false} {
		for _, engineNotReadyFirst := range []bool{true, false} {
			name := "lake-first"
			if consumerFirst {
				name = "consumer-first"
			}
			if engineNotReadyFirst {
				name += "/engine-starting"
			}
			t.Run(name, func(t *testing.T) {
				w := newLakeWorld(t)
				run1 := w.run(t, false)
				url, _ := readyPair(t, run1)
				port := w.lake(t).Status.ProxyPort
				require.NotZero(t, port)
				run1.mgr.Shutdown()

				run2 := w.run(t, engineNotReadyFirst)
				if consumerFirst {
					run2.fnPassStored(t, "reader")
					require.Empty(t, run2.rt.created("reader"), "no worker starts before lake's first pass in this run")
				}
				run2.lakePass(t, w.lake(t))
				if engineNotReadyFirst {
					run2.fnPassStored(t, "reader")
					require.Empty(t, run2.rt.created("reader"), "no worker starts while lake is not Ready")
				}
				run2.lakeUntilReady(t)
				run2.fnPassStored(t, "reader")

				require.Equal(t, port, w.lake(t).Status.ProxyPort, "lake keeps its port")
				require.Equal(t, []string{url}, run2.rt.created("reader"), "reader's new worker holds the old URL, and no other")
				newURL, token := run2.env(t, "reader")
				code, body := lakeQuery(t, newURL, token)
				require.Equal(t, http.StatusOK, code)
				require.Equal(t, "[[42]]", body)
			})
		}
	}
}

// scenario: taken-port-moves-consumers
//
// Not parallel, nor its subtests, for the window TestScenarioRestartKeepsLakePort names: here the holder must get
// the freed port.
func TestScenarioTakenPortMovesConsumers(t *testing.T) {
	for _, consumerFirst := range []bool{true, false} {
		name := "lake-first"
		if consumerFirst {
			name = "consumer-first"
		}
		t.Run(name, func(t *testing.T) {
			w := newLakeWorld(t)
			run1 := w.run(t, false)
			oldURL, _ := readyPair(t, run1)
			w.applyFunction(t, "reader2", true)
			w.applyFunction(t, "plain", false)
			run1.fnPassStored(t, "reader2")
			run1.fnPassStored(t, "plain")
			port := w.lake(t).Status.ProxyPort
			run1.mgr.Shutdown()
			holder, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			require.NoError(t, err)
			t.Cleanup(func() { _ = holder.Close() })

			run2 := w.run(t, false)
			fns := []string{"reader", "reader2", "plain"}
			if consumerFirst {
				for _, n := range fns {
					run2.fnPassStored(t, n)
				}
			}
			run2.lakeUntilReady(t)
			for _, n := range fns {
				run2.fnPassStored(t, n)
			}

			moved := w.lake(t).Status.ProxyPort
			require.NotEqual(t, port, moved, "lake serves on a new port")
			newURL := net.JoinHostPort("127.0.0.1", strconv.Itoa(moved))
			require.Equal(t, newURL, w.lake(t).Status.Endpoint)
			require.NotEqual(t, oldURL, newURL)
			for _, n := range []v1.ObjectName{"reader", "reader2"} {
				require.Equal(t, []string{newURL}, run2.rt.created(n), "%s's worker is created once, on the new port", n)
			}
			require.Equal(t, []string{""}, run2.rt.created("plain"), "plain's worker is created once")
			url, token := run2.env(t, "reader2")
			code, body := lakeQuery(t, url, token)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, "[[42]]", body)
		})
	}
}

// scenario: last-binder-closes-listener
//
// Not parallel, nor its subtests: a port bound in parallel could take the closed listener's port, and the dial would
// connect.
func TestScenarioLastBinderClosesListener(t *testing.T) {
	cases := map[string]func(t *testing.T, r *lakeRun){
		"reader-deleted": func(t *testing.T, r *lakeRun) {
			last := r.w.function(t, "reader")
			require.NoError(t, r.w.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "reader", ""))
			r.fnPass(t, "reader", last)
		},
		"binding-dropped": func(t *testing.T, r *lakeRun) {
			fn := r.w.function(t, "reader")
			fn.Spec.Bindings.Lake = nil
			_, err := r.w.st.Update(context.Background(), fn)
			require.NoError(t, err)
			for range 10 {
				r.fnPassStored(t, "reader")
				fn = r.w.function(t, "reader")
				if fn.Status.ObservedGeneration == fn.Generation && fn.Status.ServingRevision == fn.Status.CurrentRevision &&
					fn.Status.DrainingRevision == "" {
					return
				}
				_, bound := r.mgr.ProxyURL("default", "lake")
				require.True(t, bound, "the listener stays open while reader switches revisions")
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatalf("reader's revision switch did not complete: %+v", fn.Status)
		},
	}
	for name, unbind := range cases {
		t.Run(name, func(t *testing.T) {
			w := newLakeWorld(t)
			r := w.run(t, false)
			url, token := readyPair(t, r)

			last := w.lake(t)
			require.NoError(t, w.st.Delete(context.Background(), v1.KindLake.GVK(), "default", "lake", ""))
			r.lakePass(t, last)
			code, _ := lakeQuery(t, url, token)
			require.Equal(t, http.StatusServiceUnavailable, code, "the listener stays open while reader binds lake")

			unbind(t, r)
			requireDialRefused(t, url)
		})
	}
}

// servedKey keys a connection's served flag in its context.
type servedKey struct{}

// TestIssue931_QueryOpensItsOwnConnection: a server that serves one request per connection, as a restarted daemon
// serves none of its previous run's, answers every lakeQuery: a query is never written onto an earlier query's
// connection, which net/http does not retry for a POST once the server has closed it.
func TestIssue931_QueryOpensItsOwnConnection(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if served, _ := r.Context().Value(servedKey{}).(*atomic.Bool); served.Swap(true) {
			panic(http.ErrAbortHandler)
		}
		_, _ = io.WriteString(w, "[[42]]")
	}))
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, servedKey{}, new(atomic.Bool))
	}
	srv.Start()
	t.Cleanup(srv.Close)
	url := strings.TrimPrefix(srv.URL, "http://")
	for range 2 {
		code, body := lakeQuery(t, url, "token")
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "[[42]]", body)
	}
}
