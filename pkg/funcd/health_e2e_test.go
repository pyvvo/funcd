//go:build e2e

package funcd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
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
// while its process runs.
func (e *gcEnv) imageHangs(t *testing.T) string {
	t.Helper()
	writeStep(t, e.src, "hangs", `export async function handle() { for (;;) {} }`)
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
	const restarting = "a replica stopped answering /health/liveness and is being replaced"
	st, rt, logs := store.New(memory.New()), process.New(nil), &lockedLog{}
	e := startGC(t, funcd.WithStore(st), funcd.WithRuntime(rt), funcd.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		funcd.WithPacing(funcd.Pacing{SupervisionPeriod: 500 * time.Millisecond, LivenessTimeout: time.Second}))
	a := todoApp(t, e)
	a.Spec.Functions[0].Image = e.imageHangs(t)
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	hung := todoWorker(t, rt)

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

// readiness asks w's shim for /health/readiness and returns its status code and the dependency report of a 503.
func readiness(t *testing.T, w runtime.Instance) (int, *local.DependencyReport) {
	t.Helper()
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://%s:%d/health/readiness", w.IP, w.Port))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusServiceUnavailable {
		return resp.StatusCode, nil
	}
	var rep local.DependencyReport
	require.NoError(t, json.Unmarshal(body, &rep), "a 503 carries a dependency report: %s", body)
	return resp.StatusCode, &rep
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
	a.Spec.Functions[0].Links = []v1.FunctionLink{{Alias: "mailer", Target: "mailer"}}
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
	a.Spec.Functions[0].KV = append(a.Spec.Functions[0].KV, v1.FunctionKV{Alias: "audit", Store: "todo-store", Table: "audit"})
	e.apply(t, a)

	var fn *v1.Function
	var rr v1.Condition
	require.Eventually(t, func() bool {
		fn = e.object(t, v1.KindFunction, "todo-api").(*v1.Function)
		rr, _ = fn.Status.Conditions.Get("RevisionReady")
		return rr.ObservedGeneration == fn.Generation && rr.Status == v1.ConditionFalse && rr.Reason == "DependencyNotReady"
	}, appWithin, 20*time.Millisecond, "todo-api's new Revision is not ready on a binding")
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
		var ready v1.Condition
		require.Eventually(t, func() bool {
			ready, _ = h.function(t, "b").Status.Conditions.Get("Ready")
			return ready.Status == v1.ConditionFalse && ready.Reason == "DependencyNotReady"
		}, 20*time.Second, 50*time.Millisecond, "b is not ready on its binding")
		require.NotEqual(t, v1.PhaseReady, h.function(t, "b").Status.Phase)
		require.True(t, strings.HasPrefix(ready.Message, `kv binding "t": `), ready.Message)
		require.Contains(t, ready.Message, `not authorized to kv::read table "t"`)
		h.samePool(t, "a", "b")
		sibling := h.function(t, "a")
		require.Equal(t, v1.PhaseReady, sibling.Status.Phase, "the sibling is unaffected")
		c, _ := sibling.Status.Conditions.Get("Ready")
		require.Equal(t, v1.ConditionTrue, c.Status)
		require.True(t, h.call(t, "a").OK)

		pid := h.settledPoolPID(t, l.runtime, "deps")
		require.NotZero(t, pid)
		require.Never(t, func() bool { return h.poolPID(t, l.runtime, "deps") != pid },
			3*time.Second, 250*time.Millisecond, "a member's failed check never restarts the pool")
	})
}
