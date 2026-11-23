package function_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
)

// fakeLauncher is the run tests' executor: a call waits for the answer its test gives the run, or for its stop, which
// it answers late with a result the pass must not record.
type fakeLauncher struct {
	st      store.Store
	mu      sync.Mutex
	calls   map[v1.ObjectName]int
	kinds   map[v1.ObjectName][]function.RunCall // each call's kind, in order
	stored  map[v1.ObjectName]v1.JobRunPhase     // the run's stored phase when its executor started
	answers map[v1.ObjectName]chan function.RunAnswer
	stopped map[v1.ObjectName]bool
}

func (f *fakeLauncher) answerOf(name v1.ObjectName) chan function.RunAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.answers[name]
	if !ok {
		ch = make(chan function.RunAnswer, 1)
		f.answers[name] = ch
	}
	return ch
}

func (f *fakeLauncher) launch(ctx context.Context, run *v1.JobRun, _ *v1.Revision, call function.RunCall) function.RunAnswer {
	obj, err := f.st.Get(context.Background(), v1.KindJobRun.GVK(), run.Namespace, run.Name)
	ch := f.answerOf(run.Name)
	f.mu.Lock()
	f.calls[run.Name]++
	f.kinds[run.Name] = append(f.kinds[run.Name], call)
	if err == nil {
		f.stored[run.Name] = obj.(*v1.JobRun).Status.Phase
	}
	f.mu.Unlock()
	select {
	case a := <-ch:
		return a
	case <-ctx.Done():
		f.mu.Lock()
		f.stopped[run.Name] = true
		f.mu.Unlock()
		return function.NewRunAnswer(v1.JobRunSucceeded, "", "", json.RawMessage(`{"late":true}`))
	}
}

func (f *fakeLauncher) callsOf(name v1.ObjectName) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeLauncher) kindsOf(name v1.ObjectName) []function.RunCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]function.RunCall(nil), f.kinds[name]...)
}

func (f *fakeLauncher) storedOf(name v1.ObjectName) v1.JobRunPhase {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stored[name]
}

func (f *fakeLauncher) stoppedOf(name v1.ObjectName) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped[name]
}

// fakeHold is a platform hold and a namespace hold the tests turn on and off.
type fakeHold struct {
	mu       sync.Mutex
	platform bool
	ns       map[v1.NamespaceName]bool
}

func (f *fakeHold) set(platform bool, ns ...v1.NamespaceName) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.platform, f.ns = platform, map[v1.NamespaceName]bool{}
	for _, n := range ns {
		f.ns[n] = true
	}
}

func (f *fakeHold) Held() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.platform
}

func (f *fakeHold) HeldIn(ns v1.NamespaceName) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.platform || f.ns[ns]
}

func (*fakeHold) ReleasedAt() time.Time                   { return time.Time{} }
func (*fakeHold) ReleasedAtIn(v1.NamespaceName) time.Time { return time.Time{} }

type runHarness struct {
	*jobHarness
	clk   *clock.Manual
	exec  *fakeLauncher
	hold  *fakeHold
	ended chan v1.ObjectName
	runs  *function.JobRunReconciler
}

// newRunHarness is the run reconciler on the Job harness and the fake runtime, with maxRunning node slots, a one-hour
// job.retention and its executor's ends signalled on ended.
func newRunHarness(t *testing.T, maxRunning int) *runHarness {
	t.Helper()
	in := &fakeInspector{}
	clk := clock.NewManual(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	h := newHarness(t, func(d *function.Deps) {
		d.Resolver = &fakeResolver{digest: jobDigest}
		d.Inspector = in
		d.Runtime = newFakeRuntime("127.0.0.1", 1)
		d.Clock = clk
	})
	rh := &runHarness{jobHarness: &jobHarness{harness: h, inspector: in}, clk: clk, hold: &fakeHold{},
		ended: make(chan v1.ObjectName, 16)}
	rh.exec = &fakeLauncher{st: h.st, calls: map[v1.ObjectName]int{}, kinds: map[v1.ObjectName][]function.RunCall{},
		stored:  map[v1.ObjectName]v1.JobRunPhase{},
		answers: map[v1.ObjectName]chan function.RunAnswer{}, stopped: map[v1.ObjectName]bool{}}
	rh.runs = function.RunsWith(h.r, function.RunsConfig{MaxRunning: maxRunning, Retention: time.Hour, Hold: rh.hold,
		NamespaceHold: rh.hold, Enqueue: func(req controller.Request) { rh.ended <- req.Name }}, rh.exec.launch)
	t.Cleanup(rh.runs.Close)
	return rh
}

// createJob creates Job name with approval.bypass, so its runs run as ADR-0347 decides; edit may turn it off.
func (h *runHarness) createJob(t *testing.T, name string, edit func(*v1.JobSpec)) *v1.Job {
	t.Helper()
	return h.jobHarness.createJob(t, name, func(s *v1.JobSpec) {
		s.Approval.Bypass = true
		if edit != nil {
			edit(s)
		}
	})
}

// readyJob creates Job name and passes it once, so it is Ready.
func (h *runHarness) readyJob(t *testing.T, name string, edit func(*v1.JobSpec)) *v1.Job {
	t.Helper()
	h.createJob(t, name, edit)
	h.reconcileJob(t, name)
	job := h.getJob(t, name)
	require.Equal(t, v1.ConditionTrue, readyOf(t, job).Status, readyOf(t, job).Message)
	return job
}

func (h *runHarness) run(t *testing.T, name, job, input string, edit func(*v1.JobRun)) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindJobRun)
	run := obj.(*v1.JobRun)
	run.Name, run.Namespace, run.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	run.Spec.Job = v1.ObjectName(job)
	if input != "" {
		run.Spec.Input = json.RawMessage(input)
	}
	if edit != nil {
		edit(run)
	}
	_, err := h.st.Create(context.Background(), run)
	require.NoError(t, err)
}

func (h *runHarness) reconcileRun(t *testing.T, name string) {
	t.Helper()
	_, err := h.runs.Reconcile(context.Background(), controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

func (h *runHarness) getRun(t *testing.T, name string) *v1.JobRun {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindJobRun.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.JobRun)
}

func (h *runHarness) editRun(t *testing.T, name string, edit func(*v1.JobRun)) {
	t.Helper()
	run := h.getRun(t, name)
	edit(run)
	_, err := h.st.Update(context.Background(), run)
	require.NoError(t, err)
}

// waitEnded waits until the executor of run name ended.
func (h *runHarness) waitEnded(t *testing.T, name string) {
	t.Helper()
	for {
		select {
		case got := <-h.ended:
			if got == v1.ObjectName(name) {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the executor of %s did not end", name)
		}
	}
}

// waitCalled waits until the executor of run name made its call.
func (h *runHarness) waitCalled(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool { return h.exec.callsOf(v1.ObjectName(name)) > 0 }, 5*time.Second, time.Millisecond)
}

func requireRun(t *testing.T, run *v1.JobRun, phase v1.JobRunPhase, reason string) v1.Condition {
	t.Helper()
	require.Equal(t, phase, run.Status.Phase)
	c, ok := run.Status.Conditions.Get("Ready")
	require.True(t, ok, "the run has a Ready condition")
	require.Equal(t, reason, c.Reason, c.Message)
	return c
}

// ADR-0347 Decisions 4 and 5: a run waits JobNotFound, then JobNotReady, and starts once its Job is Ready, with one
// write that stores it Running with its pin, its Job's UID, the digest, its start and a trace ID before its executor
// starts; the executor's answer ends it Succeeded with the result, and the handler is called once.
func TestJobRunStartsOnceItsJobIsReady(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.run(t, "hello-a", "hello", `{"name":"Ada"}`, nil)
	h.reconcileRun(t, "hello-a")
	requireRun(t, h.getRun(t, "hello-a"), v1.JobRunPending, v1.JobRunJobNotFound)

	h.createJob(t, "hello", nil)
	h.reconcileRun(t, "hello-a")
	requireRun(t, h.getRun(t, "hello-a"), v1.JobRunPending, v1.JobRunJobNotReady)

	h.reconcileJob(t, "hello")
	h.reconcileRun(t, "hello-a")
	run := h.getRun(t, "hello-a")
	require.Equal(t, v1.JobRunRunning, run.Status.Phase)
	require.Equal(t, v1.ObjectName("hello-v1"), run.Status.JobRevision)
	require.Equal(t, h.getJob(t, "hello").UID, run.Status.JobUID)
	require.Equal(t, jobDigest, run.Status.ImageDigest)
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), run.Status.StartedAt)
	require.Regexp(t, "^[0-9a-f]{32}$", run.Status.TraceID)
	h.waitCalled(t, "hello-a")
	require.Equal(t, v1.JobRunRunning, h.exec.storedOf("hello-a"), "the run is stored Running before its executor starts")

	h.clk.Advance(time.Minute)
	h.exec.answerOf("hello-a") <- function.NewRunAnswer(v1.JobRunSucceeded, "", "", json.RawMessage(`{"greeting":"hi Ada"}`))
	h.waitEnded(t, "hello-a")
	h.reconcileRun(t, "hello-a")
	run = h.getRun(t, "hello-a")
	c := requireRun(t, run, v1.JobRunSucceeded, "")
	require.Equal(t, v1.ConditionTrue, c.Status)
	require.JSONEq(t, `{"greeting":"hi Ada"}`, string(run.Status.Result))
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), run.Status.EndedAt)
	h.reconcileRun(t, "hello-a")
	require.Equal(t, 1, h.exec.callsOf("hello-a"), "a run's handler is called once")
}

// ADR-0347 Decision 4 and run.fizz AtMostOneRunning: a slot needs fewer of the Job's runs Running than its concurrency
// and fewer of the node's than job.maxRunning; a Job at its limit holds no other back, and a freed node slot goes to
// the oldest waiting run whose Job has a free slot.
func TestJobRunSlots(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 2)
	h.readyJob(t, "a", nil)
	h.readyJob(t, "b", func(s *v1.JobSpec) { s.Concurrency = 2 })
	for _, r := range []struct{ name, job string }{{"a-1", "a"}, {"a-2", "a"}, {"b-1", "b"}, {"b-2", "b"}} {
		h.run(t, r.name, r.job, "", nil)
	}
	h.reconcileRun(t, "a-1")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "a-1").Status.Phase)
	h.reconcileRun(t, "a-2")
	c := requireRun(t, h.getRun(t, "a-2"), v1.JobRunPending, v1.JobRunQueued)
	require.Contains(t, c.Message, "concurrency")
	h.reconcileRun(t, "b-1")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "b-1").Status.Phase, "a Job at its limit holds no other back")
	h.reconcileRun(t, "b-2")
	c = requireRun(t, h.getRun(t, "b-2"), v1.JobRunPending, v1.JobRunQueued)
	require.Contains(t, c.Message, "job.maxRunning")

	h.exec.answerOf("a-1") <- function.NewRunAnswer(v1.JobRunSucceeded, "", "", nil)
	h.waitEnded(t, "a-1")
	h.reconcileRun(t, "a-1")
	ended := h.getRun(t, "a-1")
	require.Equal(t, v1.JobRunSucceeded, ended.Status.Phase)
	require.ElementsMatch(t, []controller.Request{
		{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "a-2"},
		{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "b-2"},
	}, h.runs.MapRun(context.Background(), ended), "an ended run reaches the runs that wait")
	h.reconcileRun(t, "b-2")
	requireRun(t, h.getRun(t, "b-2"), v1.JobRunPending, v1.JobRunQueued)
	h.reconcileRun(t, "a-2")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "a-2").Status.Phase, "the freed node slot goes to the oldest run")
}

// ADR-0347 Decision 4: an input off the pinned revision's contract ends the run Failed with InputSchemaMismatch at its
// start, with no Running write and no call.
func TestJobRunInputOffTheContractFailsAtStart(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.inspector.blob = []byte(`{"input":{"type":"object","required":["name"]}}`)
	h.readyJob(t, "hello", func(s *v1.JobSpec) { s.Concurrency = 2 })
	h.run(t, "hello-a", "hello", `{}`, nil)
	h.run(t, "hello-b", "hello", `{"name":"Ada"}`, nil)
	h.reconcileRun(t, "hello-a")
	run := h.getRun(t, "hello-a")
	c := requireRun(t, run, v1.JobRunFailed, v1.JobRunInputSchemaMismatch)
	require.Contains(t, c.Message, `"hello-v1"`)
	require.True(t, run.Status.StartedAt.IsZero(), "the run never stored Running")
	h.reconcileRun(t, "hello-b")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "hello-b").Status.Phase)
	h.waitCalled(t, "hello-b")
	require.Zero(t, h.exec.callsOf("hello-a"))
}

// ADR-0347 Decision 6 and run.fizz EndIsFinal: a cancel ends a Pending run Cancelled with no call; a Running one has its
// executor stopped first, then ends Cancelled, and the late answer changes nothing.
func TestJobRunCancel(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.run(t, "hello-p", "hello", "", nil)
	h.reconcileRun(t, "hello-p")
	h.editRun(t, "hello-p", func(r *v1.JobRun) { r.Spec.Cancel = true })
	h.reconcileRun(t, "hello-p")
	run := h.getRun(t, "hello-p")
	requireRun(t, run, v1.JobRunCancelled, v1.JobRunCancelledReason)
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), run.Status.EndedAt)

	h.readyJob(t, "hello", nil)
	h.run(t, "hello-r", "hello", "", nil)
	h.reconcileRun(t, "hello-r")
	h.waitCalled(t, "hello-r")
	h.editRun(t, "hello-r", func(r *v1.JobRun) { r.Spec.Cancel = true })
	h.reconcileRun(t, "hello-r")
	h.waitEnded(t, "hello-r")
	h.reconcileRun(t, "hello-r")
	run = h.getRun(t, "hello-r")
	requireRun(t, run, v1.JobRunCancelled, v1.JobRunCancelledReason)
	require.True(t, h.exec.stoppedOf("hello-r"), "the executor was stopped")
	require.Empty(t, run.Status.Result, "the late answer changes nothing")
	h.reconcileRun(t, "hello-r")
	require.Equal(t, run.ResourceVersion, h.getRun(t, "hello-r").ResourceVersion, "an ended run stays as it ended")
	require.Zero(t, h.exec.callsOf("hello-p"))
}

// ADR-0347 Decision 6: a run whose Job is deleted, re-created under another UID, or whose pin is gone ends Cancelled
// with JobDeleted, a Running one once its executor stopped.
func TestJobRunJobDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newRunHarness(t, 4)
	h.readyJob(t, "hello", func(s *v1.JobSpec) { s.Concurrency = 2 })
	h.run(t, "hello-a", "hello", "", nil)
	h.run(t, "hello-b", "hello", "", nil)
	h.reconcileRun(t, "hello-a")
	h.reconcileRun(t, "hello-b")
	h.waitCalled(t, "hello-a")
	h.waitCalled(t, "hello-b")

	_, err := h.st.Get(ctx, v1.KindRevision.GVK(), "default", "hello-v1")
	require.NoError(t, err)
	require.NoError(t, h.st.Delete(ctx, v1.KindRevision.GVK(), "default", "hello-v1", ""))
	h.reconcileRun(t, "hello-a")
	h.waitEnded(t, "hello-a")
	h.reconcileRun(t, "hello-a")
	c := requireRun(t, h.getRun(t, "hello-a"), v1.JobRunCancelled, v1.JobRunJobDeleted)
	require.Contains(t, c.Message, "hello-v1", "a run whose pin is gone")

	require.NoError(t, h.st.Delete(ctx, v1.KindJob.GVK(), "default", "hello", ""))
	h.reconcileRun(t, "hello-b")
	h.waitEnded(t, "hello-b")
	h.reconcileRun(t, "hello-b")
	requireRun(t, h.getRun(t, "hello-b"), v1.JobRunCancelled, v1.JobRunJobDeleted)
	require.True(t, h.exec.stoppedOf("hello-b"))

	h.readyJob(t, "hello", nil)
	h.run(t, "hello-c", "hello", "", func(r *v1.JobRun) { r.Status.JobUID = "the-first-hello" })
	h.reconcileRun(t, "hello-c")
	requireRun(t, h.getRun(t, "hello-c"), v1.JobRunCancelled, v1.JobRunJobDeleted)
	require.Zero(t, h.exec.callsOf("hello-c"), "a run of a re-created Job never starts")
}

// ADR-0347 Decision 6: a deleted run has its executor, and so its worker, stopped.
func TestJobRunDeletedStopsItsExecutor(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "hello", nil)
	h.run(t, "hello-a", "hello", "", nil)
	h.reconcileRun(t, "hello-a")
	h.waitCalled(t, "hello-a")
	require.NoError(t, h.st.Delete(context.Background(), v1.KindJobRun.GVK(), "default", "hello-a", ""))
	h.reconcileRun(t, "hello-a")
	h.waitEnded(t, "hello-a")
	require.True(t, h.exec.stoppedOf("hello-a"))
}

// ADR-0347 Decision 7 and run.fizz NeverCalledTwice: Boot ends every run stored Running Failed with Interrupted and
// leaves the others; a pass of a run stored Running that no executor of this process holds ends it the same way, and
// neither calls the handler.
func TestJobRunBootInterrupts(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	h.readyJob(t, "hello", func(s *v1.JobSpec) { s.Concurrency = 2 })
	h.run(t, "hello-r", "hello", "", func(r *v1.JobRun) { r.Status.Phase = v1.JobRunRunning })
	h.run(t, "hello-p", "hello", "", nil)
	h.clk.Advance(time.Minute)
	require.NoError(t, h.runs.Boot(context.Background()))
	run := h.getRun(t, "hello-r")
	c := requireRun(t, run, v1.JobRunFailed, v1.JobRunInterrupted)
	require.Contains(t, c.Message, "does not start again")
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), run.Status.EndedAt)
	require.Empty(t, h.getRun(t, "hello-p").Status.Phase, "Boot touches no run that was not Running")

	h.reconcileRun(t, "hello-p")
	h.waitCalled(t, "hello-p")
	restarted := function.RunsWith(h.r, function.RunsConfig{MaxRunning: 4}, h.exec.launch)
	_, err := restarted.Reconcile(context.Background(), controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "hello-p"})
	require.NoError(t, err)
	requireRun(t, h.getRun(t, "hello-p"), v1.JobRunFailed, v1.JobRunInterrupted)
	h.reconcileRun(t, "hello-r")
	require.Zero(t, h.exec.callsOf("hello-r"))
	require.Equal(t, 1, h.exec.callsOf("hello-p"))
}

// ADR-0347 Decisions 4 and 8: while the platform or the run's namespace is held, the run reconciler leaves every run as
// it is, starts none and deletes none; after the release the run starts.
func TestJobRunHeldActsOnNoRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newRunHarness(t, 4)
	h.readyJob(t, "hello", nil)
	h.run(t, "hello-a", "hello", "", nil)
	h.run(t, "hello-old", "hello", "", func(r *v1.JobRun) {
		r.Status.End(v1.JobRunSucceeded, "", "", v1.NewTimestamp(h.clk.Now().Add(-2*time.Hour)))
	})
	for _, held := range []func(){func() { h.hold.set(true) }, func() { h.hold.set(false, "default") }} {
		held()
		before := h.getRun(t, "hello-a").ResourceVersion
		h.reconcileRun(t, "hello-a")
		require.Equal(t, before, h.getRun(t, "hello-a").ResourceVersion, "a held run's status is untouched")
		n, err := h.runs.SweepExpired(ctx)
		require.NoError(t, err)
		require.Zero(t, n, "a held run is not swept")
	}
	require.Zero(t, h.exec.callsOf("hello-a"))
	h.hold.set(false)
	h.reconcileRun(t, "hello-a")
	require.Equal(t, v1.JobRunRunning, h.getRun(t, "hello-a").Status.Phase)
}

// ADR-0347 Decision 8: the sweep deletes a run job.retention after its end, and keeps younger and open ones.
func TestJobRunRetention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newRunHarness(t, 4)
	h.run(t, "hello-old", "hello", "", func(r *v1.JobRun) {
		r.Status.End(v1.JobRunFailed, v1.JobRunHandlerFailed, "boom", v1.NewTimestamp(h.clk.Now().Add(-time.Hour)))
	})
	h.run(t, "hello-new", "hello", "", func(r *v1.JobRun) {
		r.Status.End(v1.JobRunSucceeded, "", "", v1.NewTimestamp(h.clk.Now().Add(-time.Hour+time.Second)))
	})
	h.run(t, "hello-open", "hello", "", nil)
	n, err := h.runs.SweepExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = h.st.Get(ctx, v1.KindJobRun.GVK(), "default", "hello-old")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	h.getRun(t, "hello-new")
	h.getRun(t, "hello-open")
}

// A Job's change reaches its open runs only.
func TestJobRunMapJob(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t, 4)
	job := h.readyJob(t, "hello", nil)
	h.run(t, "hello-a", "hello", "", nil)
	h.run(t, "hello-done", "hello", "", func(r *v1.JobRun) { r.Status.End(v1.JobRunSucceeded, "", "", v1.NewTimestamp(h.clk.Now())) })
	h.run(t, "other-a", "other", "", nil)
	require.Equal(t, []controller.Request{{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: "hello-a"}},
		h.runs.MapJob(context.Background(), job))
}
