//go:build e2e

package funcd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// lockedLog is a log sink the platform's goroutines share.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// imageHangs pushes a handler whose call blocks its event loop for good, so the shim stops answering /health/liveness
// while its process runs. While the file hold exists its module does not finish loading, so a replica that starts then
// stays unready until the test removes hold (#1120).
func (e *gcEnv) imageHangs(t *testing.T, hold string) string {
	t.Helper()
	writeStep(t, e.src, "hangs", fmt.Sprintf(`import { existsSync } from 'node:fs';
while (existsSync(%q)) { await new Promise((r) => setTimeout(r, 20)); }
export async function handle() { for (;;) {} }`, hold))
	return pushStepImage(t, e.layout, e.src, "hangs")
}

// functionWrites is the phase and Ready condition of each write of Function name among evs.
func functionWrites(evs []store.Event, name v1.ObjectName) []appWrite {
	var out []appWrite
	for _, ev := range evs {
		if fn, ok := ev.Object.(*v1.Function); ok && fn.Name == name {
			c, _ := fn.Status.Conditions.Get("Ready")
			out = append(out, appWrite{phase: fn.Status.Phase, ready: c})
		}
	}
	return out
}

// scenario: app-hung-worker-restarted
func TestScenarioAppHungWorkerRestarted(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	hungWorkerRestarted(t, st, st)
}

// Issue #1120: on a loaded host the App reconciler can read todo-api only after its replacement serves, so it never
// writes Degraded. The platform here sees each Function event 500 ms late, as such a host does.
func TestIssue1120_AppDegradedWhenItSeesTheRestartLate(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	hungWorkerRestarted(t, st, lateFunctionEvents{Store: st, delay: 500 * time.Millisecond})
}

// lateFunctionEvents is a store whose Function watch events each reach their watcher delay after they happen, in order.
type lateFunctionEvents struct {
	store.Store
	delay time.Duration
}

func (s lateFunctionEvents) Watch(ctx context.Context, gvk v1.GroupVersionKind, opts store.WatchOptions) (store.Watch, error) {
	w, err := s.Store.Watch(ctx, gvk, opts)
	if err != nil || gvk.Kind != v1.KindFunction {
		return w, err
	}
	type arrival struct {
		ev store.Event
		at time.Time
	}
	late := &lateWatch{inner: w, out: make(chan store.Event), done: make(chan struct{})}
	queued := make(chan arrival, 1024)
	go func() {
		defer close(queued)
		for ev := range w.ResultChan() {
			select {
			case queued <- arrival{ev: ev, at: time.Now()}:
			case <-late.done:
				return
			}
		}
	}()
	go func() {
		defer close(late.out)
		for a := range queued {
			select {
			case <-time.After(time.Until(a.at.Add(s.delay))):
			case <-late.done:
				return
			}
			select {
			case late.out <- a.ev:
			case <-late.done:
				return
			}
		}
	}()
	return late, nil
}

// lateWatch forwards a watch's events late; Stop ends the watch and its forwarding, so none waits on a gone reader.
type lateWatch struct {
	inner store.Watch
	out   chan store.Event
	done  chan struct{}
	once  sync.Once
}

func (w *lateWatch) ResultChan() <-chan store.Event { return w.out }

func (w *lateWatch) Stop() {
	w.once.Do(func() {
		close(w.done)
		w.inner.Stop()
	})
}

// hungWorkerRestarted runs app-hung-worker-restarted on a platform over platformStore, watching the writes on st.
func hungWorkerRestarted(t *testing.T, st, platformStore store.Store) {
	const restarting = "a replica stopped answering /health/liveness and is being replaced"
	rt, logs := process.New(nil), &lockedLog{}
	e := startGC(t, funcd.WithStore(platformStore), funcd.WithRuntime(rt), funcd.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		funcd.WithPacing(funcd.Pacing{SupervisionPeriod: 500 * time.Millisecond, LivenessTimeout: time.Second}))
	// The replacement is held unready until the App's Degraded write is seen: the App reads its children whenever it
	// runs, so on a loaded host a restart shorter than that delay would leave no Degraded write to see (#1120).
	hold := filepath.Join(t.TempDir(), "hold")
	a := todoApp(t, e)
	a.Spec.Functions[0].Image = e.imageHangs(t, hold)
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	hung := todoWorker(t, rt)
	require.NoError(t, os.WriteFile(hold, nil, 0o600))

	fw, aw := watchKind(t, st, v1.KindFunction), watchKind(t, st, v1.KindApp)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond) // gone before the replacement serves
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.dp+"/api", strings.NewReader(`{"data":{}}`))
		if err != nil {
			return
		}
		req.Host = todoHost
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()

	require.Eventually(t, func() bool {
		return slices.ContainsFunc(functionWrites(fw.seen(), "todo-api"), func(wr appWrite) bool {
			return wr.phase == v1.PhaseDegraded && wr.ready.Status == v1.ConditionFalse && wr.ready.Reason == "Restarting" &&
				wr.ready.Message == restarting
		})
	}, appWithin, 20*time.Millisecond, "todo-api is Degraded, Ready=False Restarting")
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(appWrites(aw.seen()), func(wr appWrite) bool {
			return wr.phase == v1.PhaseDegraded && wr.ready.Reason == "ChildNotReady" &&
				wr.ready.Message == "Function/todo-api: Restarting: "+restarting
		})
	}, appWithin, 20*time.Millisecond, "the App is Degraded, naming Function/todo-api")
	require.NoError(t, os.Remove(hold))
	require.Contains(t, logs.String(), `msg="restarting a replica silent on its liveness" component=function namespace=default function=todo-api replica=0`)

	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.waitPhase(t, "todo-api", v1.PhaseReady)
	again := todoWorker(t, rt)
	require.NotEqual(t, hung.PID, again.PID, "the hung replica was replaced")
	require.Equal(t, hung.ID, again.ID, "at the same index")
}

// forbidRead is Policy name in default/rg1, forbidding Function fn's kv::read on table table of KVStore kvs.
func forbidRead(name string, fn, kvs v1.ObjectName, table string) *v1.Policy {
	obj, _ := v1.NewObject(v1.KindPolicy)
	p := obj.(*v1.Policy)
	p.Name, p.Namespace, p.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	p.Spec.Cedar = fmt.Sprintf(`forbid(principal == Function::"default/%s", action == Action::"kv::read", resource == KVTable::"default/%s/%s");`,
		fn, kvs, table)
	return p
}

// forbidReadOf is Policy name in default/rg1, forbidding Function victim's kv::read on table table of KVStore kvs
// while its one statement also names Function spared. A Policy that names a Function is part of the access its pool is
// keyed by (poolKeyFor), so one statement naming both lets them share a pool key; it must exist before the pool does.
func forbidReadOf(name string, victim, spared, kvs v1.ObjectName, table string) *v1.Policy {
	p := forbidRead(name, victim, kvs, table)
	p.Spec.Cedar = strings.TrimSuffix(p.Spec.Cedar, ";") + fmt.Sprintf(` unless { principal == Function::"default/%s" };`, spared)
	return p
}

// revisionWorker waits for a listening worker of Function name's Revision rev on rt and returns it.
func revisionWorker(t *testing.T, rt runtime.Runtime, name, rev v1.ObjectName) runtime.Instance {
	t.Helper()
	var w runtime.Instance
	require.Eventually(t, func() bool {
		insts, err := rt.List(context.Background(), "default")
		require.NoError(t, err)
		i := slices.IndexFunc(insts, func(in runtime.Instance) bool {
			return in.Name == name && in.Revision == rev && in.State == runtime.StateRunning && in.Port != 0
		})
		if i >= 0 {
			w = insts[i]
		}
		return i >= 0
	}, appWithin, 50*time.Millisecond, "%s runs a worker of %s", name, rev)
	return w
}

// readiness asks w's shim for /health/readiness and returns its status code and the dependency report of a 503. A
// socket/Timeout report is the shim's own 50 ms budget running out on a loaded host (ADR-0215, #1082), not funcd's
// answer, so it asks again until it gets one.
func readiness(t *testing.T, w runtime.Instance) (int, *local.DependencyReport) {
	t.Helper()
	c := &http.Client{Timeout: 2 * time.Second}
	var code int
	var rep *local.DependencyReport
	require.Eventually(t, func() bool {
		resp, err := c.Get(fmt.Sprintf("http://%s:%d/health/readiness", w.IP, w.Port))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		code, rep = resp.StatusCode, nil
		if code != http.StatusServiceUnavailable {
			return true
		}
		rep = &local.DependencyReport{}
		require.NoError(t, json.Unmarshal(body, rep), "a 503 carries a dependency report: %s", body)
		return rep.Kind != "socket" || rep.Reason != "Timeout"
	}, appWithin, 20*time.Millisecond, "the shim answers with funcd's dependency report")
	return code, rep
}

// Issue #1082: under load one readiness call can outlast the shim's 50 ms dependency budget (ADR-0215) and report
// socket/Timeout; readiness asks again until it gets funcd's own answer, and returns any other report at once.
func TestIssue1082_ReadinessWaitsOutASocketTimeout(t *testing.T) {
	t.Parallel()
	timeout := local.DependencyReport{Kind: "socket", Reason: "Timeout", Message: "late"}
	denied := local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Forbidden", Message: "denied"}
	for name, tc := range map[string]struct {
		first, want local.DependencyReport
	}{
		"socket-timeout-asks-again": {first: timeout, want: denied},
		"other-socket-reason-kept":  {first: local.DependencyReport{Kind: "socket", Reason: "Unreachable"}, want: local.DependencyReport{Kind: "socket", Reason: "Unreachable"}},
		"binding-timeout-kept":      {first: local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Timeout"}, want: local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Timeout"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				rep := denied
				if calls.Add(1) == 1 {
					rep = tc.first
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				require.NoError(t, json.NewEncoder(w).Encode(rep))
			}))
			t.Cleanup(srv.Close)
			host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
			require.NoError(t, err)
			p, err := strconv.Atoi(port)
			require.NoError(t, err)

			code, rep := readiness(t, runtime.Instance{IP: host, Port: p})
			require.Equal(t, http.StatusServiceUnavailable, code)
			require.Equal(t, tc.want, *rep)
		})
	}
}

// dependencyReport waits until read's condition is False with reason DependencyNotReady and carries funcd's report for
// one binding, the message starting with report, and returns it. Under load the reconciler's own probe can outlast the
// shim's 50 ms budget (ADR-0215) and carry the shim's socket timeout under the same reason first (#1102).
func dependencyReport(t *testing.T, within time.Duration, read func() v1.Condition, report string) v1.Condition {
	t.Helper()
	var c v1.Condition
	require.Eventually(t, func() bool {
		c = read()
		return c.Status == v1.ConditionFalse && c.Reason == "DependencyNotReady" && strings.HasPrefix(c.Message, report)
	}, within, 20*time.Millisecond, "the condition carries funcd's report %q…", report)
	return c
}

// Issue #1102: the reconciler's probe can report the shim's socket timeout before funcd's report, under the same
// reason; dependencyReport waits for funcd's.
func TestIssue1102_DependencyReportWaitsOutASocketTimeout(t *testing.T) {
	t.Parallel()
	seq := []v1.Condition{
		{Status: v1.ConditionFalse, Reason: "DependencyNotReady", Message: "socket: GET /health/dependencies did not answer within 50 ms"},
		{Status: v1.ConditionFalse, Reason: "DependencyNotReady", Message: `kv binding "audit": not authorized to kv::read table "audit"`},
	}
	var reads atomic.Int32
	got := dependencyReport(t, 5*time.Second, func() v1.Condition {
		return seq[min(int(reads.Add(1))-1, len(seq)-1)]
	}, `kv binding "audit": `)
	require.Equal(t, seq[1], got)
}

// scenario: app-dependency-check — on the real node shim, whose readiness asks funcd's GET /health/dependencies.
// The rollout deadline, app.upgradeTimeout 6s, falls past runtime.bootTimeout 3s.
func TestScenarioAppDependencyCheck(t *testing.T) {
	t.Parallel()
	st, rt := store.New(memory.New()), process.New(nil)
	e := startGC(t, funcd.WithStore(st), funcd.WithRuntime(rt), funcd.WithPacing(funcd.Pacing{
		SupervisionPeriod: 500 * time.Millisecond, ActivationTimeout: 2 * time.Second, BootTimeout: 3 * time.Second,
		AppUpgradeTimeout: 6 * time.Second,
	}))
	echo := e.image(t)
	obj, _ := v1.NewObject(v1.KindFunction)
	mailer := obj.(*v1.Function)
	mailer.Name, mailer.Namespace, mailer.ResourceGroup = "mailer", "default", "rg1"
	mailer.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: echo}
	e.apply(t, mailer)
	e.waitPhase(t, "mailer", v1.PhaseIdle)

	a := todoV1(t, e)
	a.Spec.Functions[0].Bindings.Links = []v1.FunctionLink{{Alias: "mailer", Target: "mailer"}}
	e.apply(t, a)
	e.waitCurrent(t, "todo-1")
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	serving := e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Status.ServingRevision

	e.apply(t, forbidRead("forbid-audit", "todo-api", "todo-store", "audit"))
	a.Spec.Version = "3.0.0"
	a.Spec.KV[0].Tables = append(a.Spec.KV[0].Tables, v1.KVTable{Name: "audit"})
	a.Spec.Functions[0].Image = echo
	a.Spec.Functions[0].Bindings.KV = append(a.Spec.Functions[0].Bindings.KV, v1.FunctionKV{Alias: "audit", Store: "todo-store", Table: "audit"})
	e.apply(t, a)

	var fn *v1.Function
	rr := dependencyReport(t, appWithin, func() v1.Condition {
		fn = e.object(t, v1.KindFunction, "todo-api").(*v1.Function)
		c, _ := fn.Status.Conditions.Get("RevisionReady")
		if c.ObservedGeneration != fn.Generation {
			return v1.Condition{}
		}
		return c
	}, `kv binding "audit": `)
	require.NotEqual(t, serving, fn.Status.CurrentRevision)
	code, rep := readiness(t, revisionWorker(t, rt, "todo-api", v1.ObjectName(fn.Status.CurrentRevision)))
	require.Equal(t, http.StatusServiceUnavailable, code, "the new replica's readiness")
	require.Equal(t, local.DependencyReport{Kind: "kv", Binding: "audit", Reason: "Forbidden", Message: rep.Message}, *rep)
	require.Contains(t, rep.Message, `not authorized to kv::read table "audit"`)
	require.Equal(t, `kv binding "audit": `+rep.Message, rr.Message)
	require.Equal(t, serving, fn.Status.ServingRevision, "the old Revision keeps serving")
	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker, "calls answer from todo-2's image")

	r3 := e.waitRevisionPhase(t, "todo-3", v1.PhaseFailed, appWithin)
	require.Equal(t, "Function/todo-api: DependencyNotReady: "+rr.Message, condition(r3, "ChildrenReady").Message,
		"past runtime.bootTimeout todo-3 fails on the report, not on a shape failure")
	require.Equal(t, v1.ObjectName("todo-2"), e.app(t, "todo").Status.CurrentRevision)
	require.Equal(t, v1.PhaseIdle, e.object(t, v1.KindFunction, "mailer").(*v1.Function).Status.Phase)
	insts, err := rt.List(context.Background(), "default")
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(insts, func(in runtime.Instance) bool { return in.Name == "mailer" }),
		"no check wakes the link target mailer")
}

// scenario: health-pool-member-dependency — on the real node and python pool shims, whose /health/members asks funcd's
// GET /health/dependencies for each member. A Policy forbidding b's binding exists before the pool, since changing a
// Policy that names a member changes the pool's key.
func TestScenarioHealthPoolMemberDependency(t *testing.T) {
	t.Parallel()
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		h := newShimRig(t, l.python, funcd.WithPacing(funcd.Pacing{SupervisionPeriod: 500 * time.Millisecond}))
		h.applyObj(t, ownedKVStore(""))
		h.applyObj(t, forbidReadOf("forbid-b", "b", "a", "s", "t"))
		for _, n := range []string{"a", "b"} {
			h.deploy(t, n, l.fn(l.quiet).pooled("deps").with(bindTable))
		}
		waitReady(t, h.c, "a")
		ready := dependencyReport(t, 20*time.Second, func() v1.Condition {
			c, _ := h.function(t, "b").Status.Conditions.Get("Ready")
			return c
		}, `kv binding "t": `)
		require.NotEqual(t, v1.PhaseReady, h.function(t, "b").Status.Phase)
		require.Contains(t, ready.Message, `not authorized to kv::read table "t"`)
		h.samePool(t, "a", "b")
		sibling := h.function(t, "a")
		require.Equal(t, v1.PhaseReady, sibling.Status.Phase, "the sibling is unaffected")
		c, _ := sibling.Status.Conditions.Get("Ready")
		require.Equal(t, v1.ConditionTrue, c.Status)
		require.True(t, h.call(t, "a").OK)

		pid := h.settledPoolPID(t, l.runtime, "deps")
		require.NotZero(t, pid)
		require.True(t, poll.Never(func() bool { return h.poolPID(t, l.runtime, "deps") != pid },
			3*time.Second, 250*time.Millisecond), "a member's failed check never restarts the pool")
	})
}
