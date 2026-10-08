//go:build e2e

package funcd_test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// imageNeverStarts pushes an image whose module never finishes loading, so its worker never listens.
func (e *gcEnv) imageNeverStarts(t *testing.T) string {
	t.Helper()
	writeStep(t, e.src, "never", `await new Promise(() => setInterval(() => {}, 1000));
export async function handle() { return {}; }`)
	return pushStepImage(t, e.layout, e.src, "never")
}

// funcdctl builds the CLI once and returns a runner of it against e's control plane.
func funcdctl(t *testing.T, e *gcEnv) func(args ...string) (string, error) {
	t.Helper()
	bin := buildCmd(t, "funcdctl")
	return func(args ...string) (string, error) {
		out, err := exec.Command(bin, append([]string{"--server", e.api, "--token", funcd.DevToken}, args...)...).CombinedOutput()
		return string(out), err
	}
}

// waitRevisionPhase waits until AppRevision name exists and is in phase, and returns it.
func (e *gcEnv) waitRevisionPhase(t *testing.T, name string, phase v1.Phase, within time.Duration) *v1.AppRevision {
	t.Helper()
	var r *v1.AppRevision
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindAppRevision, "default", v1.ObjectName(name))
		if err != nil {
			return false
		}
		r = obj.(*v1.AppRevision)
		return r.Status.Phase == phase
	}, within, 20*time.Millisecond, "AppRevision/%s is %s", name, phase)
	return r
}

// waitStepServes waits until the step Function name serves its current Revision and that Revision runs img (ADR-0143).
func (e *gcEnv) waitStepServes(t *testing.T, name, img string) *v1.Function {
	t.Helper()
	var fn *v1.Function
	require.Eventually(t, func() bool {
		fn = e.object(t, v1.KindFunction, name).(*v1.Function)
		if fn.Spec.Image != img || fn.Status.CurrentRevision == "" || fn.Status.ServingRevision != fn.Status.CurrentRevision {
			return false
		}
		return e.object(t, v1.KindRevision, fn.Status.CurrentRevision).(*v1.Revision).Spec.Image == img
	}, appWithin, 50*time.Millisecond, "%s serves a Revision of %s", name, img)
	return fn
}

// failedPacing is app-failed-upgrade-keeps-serving's config: app.upgradeTimeout 20s, runtime.bootTimeout 10s and
// invoke.activationTimeout 5s.
func failedPacing() funcd.Option {
	return funcd.WithPacing(funcd.Pacing{
		AppUpgradeTimeout: 20 * time.Second, BootTimeout: 10 * time.Second, ActivationTimeout: 5 * time.Second,
	})
}

// failUpgrade drives app-failed-upgrade-keeps-serving up to its outcome: from todo-2 current, with todo-api on its
// second image, todo-api gets an image that never starts. It returns todo-3 once Failed and how long after its stamp
// the test first saw it Failed.
func failUpgrade(t *testing.T, e *gcEnv) (*v1.AppRevision, time.Duration) {
	t.Helper()
	a := installTodo(t, e)
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)

	a.Spec.Version = "3.0.0"
	a.Spec.Functions[0].Image = e.imageNeverStarts(t)
	e.apply(t, a)
	r3 := e.waitRevisionPhase(t, "todo-3", v1.PhaseFailed, 2*appWithin)
	seen := time.Now()
	require.NotNil(t, r3.Status.StartedAt)
	return r3, seen.Sub(time.Time(*r3.Status.StartedAt))
}

// scenario: app-one-part-changes
func TestScenarioAppOnePartChanges(t *testing.T) {
	st, rt := store.New(memory.New()), process.New(nil)
	e := startGC(t, funcd.WithStore(st), funcd.WithRuntime(rt))
	gate := filepath.Join(shortDataDir(t), "gate")
	writeStep(t, e.src, "due-gated", fmt.Sprintf(`import { existsSync } from 'node:fs';
export const handle = async () => { while (!existsSync(%q)) await new Promise((r) => setTimeout(r, 50)); return {}; };`, gate))
	gated := pushStepImage(t, e.layout, e.src, "due-gated")
	a := todoV1(t, e)
	a.Spec.Workflows[0].Steps[0].Function = &v1.FunctionStep{Image: gated, Timeout: v1.Duration(time.Minute)}
	e.apply(t, a)
	e.waitCurrent(t, "todo-1")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)

	applyRun(t, e.c, "r1", "todo-plan")
	require.Eventually(t, func() bool { return runStepPhase(getRun(t, e.c, "r1"), "due") == v1.StepRunning },
		appWithin, 20*time.Millisecond, "r1 runs due")
	held := e.object(t, v1.KindFunction, "todo-plan-due").(*v1.Function).Status.CurrentRevision
	var others []todoPart
	for _, p := range todoV1Parts() {
		if p.kind != v1.KindWorkflow {
			others = append(others, p)
		}
	}
	before := e.settled(t, others)
	wfGen := e.object(t, v1.KindWorkflow, "todo-plan").GetObjectMeta().Generation
	worker := todoWorker(t, rt)

	writeStep(t, e.src, "due-v2", `export async function handle() { return { due: "v2" }; }`)
	next := pushStepImage(t, e.layout, e.src, "due-v2")
	a.Spec.Workflows[0].Steps[0].Function.Image = next
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, got.Spec, e.appRevision(t, "todo-2").Spec.Spec, "todo-2 holds the whole spec")
	require.Eventually(t, func() bool {
		return e.object(t, v1.KindFunction, "todo-plan-due").(*v1.Function).Status.CurrentRevision != held
	}, appWithin, 20*time.Millisecond, "todo-plan-due has a new Revision while r1 runs due")

	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	require.Eventually(t, func() bool { return getRun(t, e.c, "r1").Status.Phase == v1.RunSucceeded },
		appWithin, 50*time.Millisecond, "r1 finishes")
	run := getRun(t, e.c, "r1")
	pin := slices.IndexFunc(run.Status.Pins, func(p v1.RevisionPin) bool { return p.Function == "todo-plan-due" })
	require.GreaterOrEqual(t, pin, 0, "r1 pins todo-plan-due")
	require.Equal(t, v1.ObjectName(held), run.Status.Pins[pin].Revision, "r1 pinned todo-plan-due's Revision of before the change")
	require.Contains(t, runStepRevision(run, "due"), gated, "r1's due ran the Revision it pinned")
	require.NotContains(t, runStepRevision(run, "due"), next)

	fn := e.waitStepServes(t, "todo-plan-due", next)
	require.NotEqual(t, held, fn.Status.CurrentRevision)
	require.Equal(t, wfGen+1, e.object(t, v1.KindWorkflow, "todo-plan").GetObjectMeta().Generation,
		"the App writes Workflow todo-plan once")
	require.Equal(t, before, e.versions(t, others), "no other part is written")
	require.Equal(t, worker.PID, todoWorker(t, rt).PID, "todo-api does not restart")
}

// scenario: app-failed-upgrade-keeps-serving
func TestScenarioAppFailedUpgradeKeepsServing(t *testing.T) {
	e := startGC(t, failedPacing())
	r3, after := failUpgrade(t, e)
	require.GreaterOrEqual(t, after, 20*time.Second, "todo-3 fails no sooner than app.upgradeTimeout after its stamp")
	require.Less(t, after, 23*time.Second, "todo-3 fails at app.upgradeTimeout after its stamp")
	c := condition(r3, "ChildrenReady")
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "ChildNotReady", c.Reason)
	require.True(t, strings.HasPrefix(c.Message, "Function/todo-api"), "ChildrenReady names Function/todo-api: %s", c.Message)

	got := e.waitApp(t, "todo", v1.ConditionFalse, "ChildNotReady", appWithin)
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.True(t, strings.HasPrefix(readyCondition(got).Message, "Function/todo-api"), "Ready names Function/todo-api: %s",
		readyCondition(got).Message)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.CurrentRevision)
	require.Equal(t, v1.ObjectName("todo-3"), got.Status.LatestRevision)
	require.Equal(t, "2.0.0", got.Status.Version)
	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker, "calls answer from todo-2's image")
}

// scenario: app-rollback
func TestScenarioAppRollback(t *testing.T) {
	e := startGC(t, failedPacing())
	failUpgrade(t, e)
	cli := funcdctl(t, e)

	out, err := cli("app", "rollback", "todo", "2")
	require.NoError(t, err, out)
	e.waitCurrent(t, "todo-4")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, "2.0.0", got.Status.Version)
	require.Equal(t, e.appRevision(t, "todo-2").Spec.Spec, e.appRevision(t, "todo-4").Spec.Spec, "todo-4 holds todo-2's spec")
	e.waitRevisionPhase(t, "todo-4", v1.PhaseReady, appWithin)
	require.Eventually(t, func() bool { return condition(e.appRevision(t, "todo-2"), "Current").Reason == "Replaced" },
		appWithin, 20*time.Millisecond, "todo-2 is replaced by todo-4")
	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker)

	out, err = cli("app", "history", "todo")
	require.NoError(t, err, out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Equal(t, []string{"REVISION", "VERSION", "PHASE", "STAMPED"}, strings.Fields(lines[0]))
	want := [][]string{{"1", "1.0.0", "Ready"}, {"2", "2.0.0", "Ready"}, {"3", "3.0.0", "Failed"}, {"4", "2.0.0", "Ready"}}
	require.Len(t, lines, 1+len(want), out)
	for i, w := range want {
		stamped := e.appRevision(t, "todo-"+w[0]).CreationTime.String()
		require.Equal(t, append(w, stamped), strings.Fields(lines[1+i]), "history row %d", i+1)
	}
}

// scenario: app-upgrade-superseded
func TestScenarioAppUpgradeSuperseded(t *testing.T) {
	e := startGC(t)
	a := installTodo(t, e)
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageNeverStarts(t)
	e.apply(t, a)
	require.Eventually(t, func() bool { return e.app(t, "todo").Status.LatestRevision == "todo-2" },
		appWithin, 20*time.Millisecond, "todo-2 is stamped")
	require.Equal(t, v1.PhaseDeploying, e.appRevision(t, "todo-2").Status.Phase)

	a.Spec.Version = "3.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	r2 := e.waitRevisionPhase(t, "todo-2", v1.PhaseFailed, appWithin)
	c := condition(r2, "Current")
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "Superseded", c.Reason)
	require.Contains(t, c.Message, "todo-3")

	e.waitCurrent(t, "todo-3")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, "3.0.0", got.Status.Version)
	r3 := e.waitRevisionPhase(t, "todo-3", v1.PhaseReady, appWithin)
	require.Equal(t, v1.ConditionTrue, condition(r3, "Current").Status)
	require.Equal(t, v1.PhaseFailed, e.appRevision(t, "todo-2").Status.Phase, "a superseded revision stays Failed")
	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker)
}

// scenario: app-history-kept
func TestScenarioAppHistoryKept(t *testing.T) {
	e := startGC(t, funcd.WithAppRevisionHistory(2))
	a := installTodo(t, e)
	for n := int64(2); n <= 6; n++ {
		a.Spec.Version = strconv.FormatInt(n, 10) + ".0.0"
		e.apply(t, a)
		e.waitCurrent(t, v1.AppRevisionName("todo", n))
	}
	names := func() []v1.ObjectName {
		objs, err := e.c.List(e.ctx, v1.KindAppRevision, "default")
		require.NoError(t, err)
		out := make([]v1.ObjectName, 0, len(objs))
		for _, o := range objs {
			out = append(out, o.GetObjectMeta().Name)
		}
		slices.Sort(out)
		return out
	}
	kept := []v1.ObjectName{"todo-4", "todo-5", "todo-6"}
	require.Eventually(t, func() bool { return slices.Equal(kept, names()) }, gcWithin, 20*time.Millisecond,
		"only todo-4, todo-5 and todo-6 remain")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	before := e.app(t, "todo").ResourceVersion

	out, err := funcdctl(t, e)("app", "rollback", "todo", "1")
	require.Error(t, err, out)
	require.Contains(t, out, "todo-1")
	require.Never(t, func() bool { return e.exists(t, v1.KindAppRevision, "todo-7") }, time.Second, 50*time.Millisecond,
		"no AppRevision is stamped")
	require.Equal(t, before, e.app(t, "todo").ResourceVersion, "the App is not written")
	require.Equal(t, kept, names())
}
