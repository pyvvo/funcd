//go:build e2e

package funcd_test

import (
	"context"
	"net/http"
	"os"
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
	"github.com/pyvvo/funcd/pkg/funcd"
)

// appWrite is the App todo's phase and Ready condition as one write left them.
type appWrite struct {
	phase v1.Phase
	ready v1.Condition
}

// appWatch records every write of the App todo from the store's own watch, so a scenario sees each status the App
// publishes rather than a sample of them.
type appWatch struct {
	mu      sync.Mutex
	got     []appWrite
	dropped bool
	stop    func()
}

func watchApp(t *testing.T, st store.Store) *appWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := st.Watch(ctx, v1.KindApp.GVK(), store.WatchOptions{Namespace: "default"})
	require.NoError(t, err)
	r := &appWatch{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range w.ResultChan() {
			if a, ok := ev.Object.(*v1.App); ok && a.Name == "todo" {
				r.mu.Lock()
				r.got = append(r.got, appWrite{phase: a.Status.Phase, ready: readyCondition(a)})
				r.mu.Unlock()
			}
		}
		r.mu.Lock()
		r.dropped = ctx.Err() == nil
		r.mu.Unlock()
	}()
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			cancel()
			w.Stop()
			<-done
		})
	}
	t.Cleanup(r.stop)
	return r
}

func (r *appWatch) saw(match func(appWrite) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.got, match)
}

// writes stops the watch and returns what it recorded; a watch the store dropped as slow fails the test.
func (r *appWatch) writes(t *testing.T) []appWrite {
	t.Helper()
	r.stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.False(t, r.dropped, "the store dropped the App watch")
	return r.got
}

// idleTodo is the fixture with todo-api scaled to zero (minReplicas 0); idle 0 never reclaims it once woken.
func idleTodo(t *testing.T, e *gcEnv, idle time.Duration) *v1.App {
	t.Helper()
	a := todoApp(t, e)
	a.Spec.Functions[0].Scaling = v1.Scaling{IdleTimeout: v1.Duration(idle)}
	return a
}

// appPacing supervises Functions every 500 ms and reclaims idle ones every reclaim (0 keeps the default), so a scenario
// does not wait the default 10 s period for an exited worker to be noticed or a wake after a reclaim to be served.
func appPacing(reclaim time.Duration) funcd.Option {
	return funcd.WithPacing(funcd.Pacing{SupervisionPeriod: 500 * time.Millisecond, ReclaimInterval: reclaim})
}

func (e *gcEnv) waitPhase(t *testing.T, fn string, phase v1.Phase) {
	t.Helper()
	require.Eventually(t, func() bool {
		return e.object(t, v1.KindFunction, fn).(*v1.Function).Status.Phase == phase
	}, appWithin, 20*time.Millisecond, "Function/%s is %s", fn, phase)
}

// scenario: app-idle-function-stays-current
func TestScenarioAppIdleFunctionStaysCurrent(t *testing.T) {
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st), appPacing(100*time.Millisecond))
	e.apply(t, idleTodo(t, e, time.Second))
	e.waitApp(t, "todo", v1.ConditionUnknown, "NotStarted", appWithin)
	require.Equal(t, http.StatusOK, e.routed(t, todoHost, "/api"))
	before := readyCondition(e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin))

	w := watchApp(t, st)
	e.waitPhase(t, "todo-api", v1.PhaseIdle)
	require.Never(t, func() bool { return readyCondition(e.app(t, "todo")).Status != v1.ConditionTrue }, time.Second, 20*time.Millisecond,
		"the App stays Ready while todo-api is scaled to zero")
	require.Equal(t, http.StatusOK, e.routed(t, todoHost, "/api"), "a call wakes todo-api")
	e.waitPhase(t, "todo-api", v1.PhaseReady)
	require.Never(t, func() bool { return readyCondition(e.app(t, "todo")).Status != v1.ConditionTrue }, time.Second, 20*time.Millisecond,
		"the App stays Ready once todo-api serves again")

	for i, wr := range w.writes(t) {
		require.Equal(t, v1.ConditionTrue, wr.ready.Status, "write %d of App/todo: %s %s", i, wr.ready.Reason, wr.ready.Message)
		require.Equal(t, v1.PhaseReady, wr.phase, "write %d of App/todo", i)
	}
	require.Equal(t, before.LastTransitionTime, readyCondition(e.app(t, "todo")).LastTransitionTime, "Ready never left True")
}

// scenario: app-scale-to-zero-not-started
func TestScenarioAppScaleToZeroNotStarted(t *testing.T) {
	e := startGC(t)
	e.apply(t, idleTodo(t, e, 0))
	a := e.waitApp(t, "todo", v1.ConditionUnknown, "NotStarted", appWithin)
	require.Contains(t, readyCondition(a).Message, "Function/todo-api")
	for _, c := range a.Status.Children {
		want := v1.AppChildReady
		if c.Kind == v1.KindFunction && c.Name == "todo-api" {
			want = v1.AppChildNotStarted
		}
		require.Equal(t, want, c.State, "%s/%s", c.Kind, c.Name)
	}

	require.Equal(t, http.StatusOK, e.routed(t, todoHost, "/api"), "the first call wakes todo-api")
	a = e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, a.Status.Phase)
}

// todoWorker returns todo-api's only running worker.
func todoWorker(t *testing.T, rt runtime.Runtime) runtime.Instance {
	t.Helper()
	var w runtime.Instance
	require.Eventually(t, func() bool {
		insts, err := rt.List(context.Background(), "default")
		require.NoError(t, err)
		var running []runtime.Instance
		for _, in := range insts {
			if in.Name == "todo-api" && in.State == runtime.StateRunning {
				running = append(running, in)
			}
		}
		if len(running) != 1 {
			return false
		}
		w = running[0]
		return true
	}, appWithin, 50*time.Millisecond, "todo-api runs one worker")
	return w
}

// scenario: app-degraded-recovers
func TestScenarioAppDegradedRecovers(t *testing.T) {
	st, rt := store.New(memory.New()), process.New(nil)
	e := startGC(t, funcd.WithStore(st), funcd.WithRuntime(rt), appPacing(0))
	e.apply(t, todoApp(t, e))
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	dead := todoWorker(t, rt)

	w := watchApp(t, st)
	proc, err := os.FindProcess(dead.PID)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	require.Eventually(t, func() bool {
		return w.saw(func(wr appWrite) bool {
			return wr.phase == v1.PhaseDegraded && wr.ready.Status == v1.ConditionFalse && wr.ready.Reason == "ChildNotReady" &&
				strings.HasPrefix(wr.ready.Message, "Function/todo-api: Restarting")
		})
	}, appWithin, 20*time.Millisecond, "the App is Degraded, naming Function/todo-api: Restarting")

	a := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, a.Status.Phase)
	require.NotEqual(t, dead.PID, todoWorker(t, rt).PID, "the worker was replaced")
	require.Equal(t, http.StatusOK, e.routed(t, todoHost, "/api"))
}
