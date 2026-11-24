//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The scenarios of ADR-0347 on the process driver and the real Node shim. A handler that must run until the test lets
// it waits for a file, which gate names and open creates.

const runWithin = 10 * time.Second

// gate is a file a handler waits for.
type gate string

func newGate(t *testing.T) gate { return gate(filepath.Join(t.TempDir(), "open")) }

func (g gate) open(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile(string(g), nil, 0o600))
}

// waitsFor is a handler that appends a byte to calls when called, waits for g and returns ret.
func waitsFor(g gate, calls, ret string) string {
	return fmt.Sprintf(`import { appendFileSync, existsSync } from 'node:fs';
export async function handle() {
  appendFileSync(%q, 'x');
  while (!existsSync(%q)) await new Promise((r) => setTimeout(r, 20));
  return %s;
}
`, calls, string(g), ret)
}

func callsOf(t *testing.T, calls string) int {
	t.Helper()
	b, err := os.ReadFile(calls) //nolint:gosec // a test-owned path
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(b)
}

// deployJob pushes src, with the {input, output} contract ("" is {}), as funcdctl push does and applies Job name with
// approval.bypass, so its runs run unattended, unless edit turns it off (ADR-0348 Decision 1).
func (h *shimRig) deployJob(t *testing.T, name, src, input string, edit func(*v1.JobSpec)) {
	t.Helper()
	ref, digest := funcd.PushJob(t, h.layout, name, src, input)
	job := h.getJob(t, name)
	if job == nil {
		job = &v1.Job{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJob.GVK().APIVersion(), Kind: v1.KindJob},
			ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"}}
	}
	job.Spec.Runtime, job.Spec.Handler, job.Spec.Image, job.Spec.ImageDigest = "nodejs22", "handle", ref, digest
	job.Spec.Approval.Bypass = true
	if edit != nil {
		edit(&job.Spec)
	}
	_, err := h.c.Apply(context.Background(), job)
	require.NoError(t, err)
}

func (h *shimRig) getJob(t *testing.T, name string) *v1.Job {
	t.Helper()
	obj, err := h.c.Get(context.Background(), v1.KindJob, "default", v1.ObjectName(name))
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	require.NoError(t, err)
	return obj.(*v1.Job)
}

// waitJob waits until Job name is Ready at its generation.
func (h *shimRig) waitJob(t *testing.T, name string) *v1.Job {
	t.Helper()
	var job *v1.Job
	require.Eventually(t, func() bool {
		job = h.getJob(t, name)
		c, ok := job.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == job.Generation
	}, runWithin, 20*time.Millisecond, "Job %s is Ready", name)
	return job
}

// startRun creates a run of job with input, named by the server, and returns its name.
func (h *shimRig) startRun(t *testing.T, job, input string) string {
	t.Helper()
	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{GenerateName: job + "-", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.JobRunSpec{Job: v1.ObjectName(job), Input: json.RawMessage(input)}}
	created, err := h.c.Create(context.Background(), run)
	require.NoError(t, err)
	return string(created.GetName())
}

func (h *shimRig) getJobRun(t *testing.T, name string) *v1.JobRun {
	t.Helper()
	obj, err := h.c.Get(context.Background(), v1.KindJobRun, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.JobRun)
}

// waitRun waits until run name is in phase and returns it.
func (h *shimRig) waitRun(t *testing.T, name string, phase v1.JobRunPhase) *v1.JobRun {
	t.Helper()
	var run *v1.JobRun
	require.Eventually(t, func() bool {
		run = h.getJobRun(t, name)
		return run.Status.Phase == phase
	}, runWithin, 20*time.Millisecond, "run %s is %s", name, phase)
	return run
}

func runReason(run *v1.JobRun) (string, string) {
	c, _ := run.Status.Conditions.Get("Ready")
	return c.Reason, c.Message
}

// runWorkers counts the workers of kind JobRun that rt runs or keeps for run name.
func runWorkers(t *testing.T, rt runtime.Runtime, name string) int {
	t.Helper()
	insts, err := rt.List(context.Background(), "default")
	require.NoError(t, err)
	n := 0
	for _, in := range insts {
		if in.OwnerKind == v1.KindJobRun && in.Name == v1.ObjectName(name) {
			n++
		}
	}
	return n
}

// cancelRun sets spec.cancel of run name, as funcdctl job cancel does.
func (h *shimRig) cancelRun(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		run := h.getJobRun(t, name)
		run.Spec.Cancel = true
		_, err := h.c.Apply(context.Background(), run)
		return err == nil
	}, runWithin, 20*time.Millisecond, "cancel %s", name)
}

// scenario: job-run-succeeds — a run of hello with input {"name":"Ada"} is named hello-<id>, ends Succeeded with the
// handler's result on hello-v1, and its logs hold the handler's line.
func TestScenarioJobRunSucceeds(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "hello", "export function handle(ctx, event) {\n  console.log('greeting ' + event.data.name);\n"+
		"  return { greeting: 'hi ' + event.data.name };\n}\n", "", nil)
	h.waitJob(t, "hello")
	name := h.startRun(t, "hello", `{"name":"Ada"}`)
	require.Regexp(t, "^hello-[a-z0-9]+$", name)

	run := h.waitRun(t, name, v1.JobRunSucceeded)
	require.JSONEq(t, `{"greeting":"hi Ada"}`, string(run.Status.Result))
	require.Equal(t, v1.ObjectName("hello-v1"), run.Status.JobRevision)
	require.Eventually(t, func() bool {
		lines, err := h.c.JobRunLogs(context.Background(), "default", v1.ObjectName(name), sdk.LogsOptions{})
		require.NoError(t, err)
		for _, l := range lines {
			if strings.Contains(l.Body, "greeting Ada") {
				return true
			}
		}
		return false
	}, runWithin, 100*time.Millisecond, "funcdctl job logs shows the handler's line")
	require.Zero(t, runWorkers(t, h.rt, name), "no worker outlives the run")
}

// scenario: job-input-refused — a run created with {} against a contract requiring name gets 422, naming the path.
func TestScenarioJobInputRefused(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "typed", "export function handle() { return {}; }\n",
		`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`, nil)
	require.Eventually(t, func() bool { return h.waitJob(t, "typed").Status.Contract != nil }, runWithin, 20*time.Millisecond)
	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
		ObjectMeta: v1.ObjectMeta{Name: "typed-empty", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.JobRunSpec{Job: "typed", Input: json.RawMessage(`{}`)}}
	_, err := h.c.Create(context.Background(), run)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a 422 is Invalid to the SDK: %v", err)
	require.ErrorContains(t, err, "InputSchemaMismatch")
	require.ErrorContains(t, err, "body.spec.input.name")
}

// scenario: job-runs-wait-their-turn — with concurrency 1, the second of two runs stays Pending with Queued until the
// first ends, then runs.
func TestScenarioJobRunsWaitTheirTurn(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	h.deployJob(t, "turns", waitsFor(g, calls, "{ done: true }"), "", nil)
	h.waitJob(t, "turns")
	first := h.startRun(t, "turns", `{}`)
	h.waitRun(t, first, v1.JobRunRunning)
	second := h.startRun(t, "turns", `{}`)
	require.Eventually(t, func() bool {
		reason, _ := runReason(h.getJobRun(t, second))
		return reason == v1.JobRunQueued
	}, runWithin, 20*time.Millisecond, "the second run waits Queued")
	// oracle: docs/design/job/run.fizz#AtMostOneRunning
	stays(t, func() bool {
		return h.getJobRun(t, first).Status.Phase == v1.JobRunRunning && h.getJobRun(t, second).Status.Phase == v1.JobRunPending
	}, 500*time.Millisecond, 50*time.Millisecond, "one run of the Job holds a worker at once")

	g.open(t)
	h.waitRun(t, first, v1.JobRunSucceeded)
	h.waitRun(t, second, v1.JobRunSucceeded)
	require.Equal(t, 2, callsOf(t, calls))
}

// scenario: job-cancel-stops-run — a Running run's cancel stops its worker within the driver's grace and ends it
// Cancelled; a late answer changes nothing.
func TestScenarioJobCancelStopsRun(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	h.deployJob(t, "stops", waitsFor(g, calls, "{ late: true }"), "", nil)
	h.waitJob(t, "stops")
	name := h.startRun(t, "stops", `{}`)
	h.waitRun(t, name, v1.JobRunRunning)
	require.Eventually(t, func() bool { return callsOf(t, calls) == 1 }, runWithin, 20*time.Millisecond, "the handler runs")

	h.cancelRun(t, name)
	run := h.waitRun(t, name, v1.JobRunCancelled)
	reason, _ := runReason(run)
	require.Equal(t, v1.JobRunCancelledReason, reason)
	require.Zero(t, runWorkers(t, h.rt, name), "the worker is stopped and removed")
	g.open(t)
	// oracle: docs/design/job/run.fizz#EndIsFinal
	stays(t, func() bool {
		r := h.getJobRun(t, name)
		return r.Status.Phase == v1.JobRunCancelled && len(r.Status.Result) == 0
	}, 500*time.Millisecond, 50*time.Millisecond, "an ended run stays as it ended")
}

// scenario: job-restart-interrupts — a Running run, when funcd restarts, is Failed with Interrupted, no worker of it
// runs, and its handler was called once.
func TestScenarioJobRestartInterrupts(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	boot := func() (*shimRig, func()) {
		h, opts := shimRigBase(t, "")
		p, err := funcd.New(append(opts, funcd.WithStore(st))...)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("platform Run did not return after cancel")
			}
		}
		t.Cleanup(stop)
		h.connect(t, p)
		return h, stop
	}
	h, stop := boot()
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	h.deployJob(t, "restarts", waitsFor(g, calls, "{ done: true }"), "", nil)
	h.waitJob(t, "restarts")
	name := h.startRun(t, "restarts", `{}`)
	h.waitRun(t, name, v1.JobRunRunning)
	require.Eventually(t, func() bool { return callsOf(t, calls) == 1 }, runWithin, 20*time.Millisecond, "the handler runs")
	stop()

	g.open(t)
	again, _ := boot()
	run := again.waitRun(t, name, v1.JobRunFailed)
	reason, msg := runReason(run)
	require.Equal(t, v1.JobRunInterrupted, reason, msg)
	require.Zero(t, runWorkers(t, again.rt, name), "no worker of it runs")
	// oracle: docs/design/job/run.fizz#NeverCalledTwice
	stays(t, func() bool { return callsOf(t, calls) == 1 }, 500*time.Millisecond, 50*time.Millisecond, "its handler was called once")
	// oracle: docs/design/job/run.fizz#EveryRunEnds
	require.True(t, again.getJobRun(t, name).Status.Phase.Ended(), "the cut run ended and stays ended")
}

// scenario: job-timeout — with timeout 2s, a handler that sleeps 10 s ends the run Failed with RunTimedOut, and its worker
// is gone.
func TestScenarioJobTimeout(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "sleeps", "export async function handle() {\n  await new Promise((r) => setTimeout(r, 10000));\n"+
		"  return { woke: true };\n}\n", "", func(s *v1.JobSpec) { s.Timeout = v1.Duration(2 * time.Second) })
	h.waitJob(t, "sleeps")
	name := h.startRun(t, "sleeps", `{}`)
	run := h.waitRun(t, name, v1.JobRunFailed)
	reason, msg := runReason(run)
	require.Equal(t, v1.JobRunTimedOut, reason, msg)
	require.Less(t, time.Time(run.Status.EndedAt).Sub(time.Time(run.Status.StartedAt)), 6*time.Second, "cut at the timeout, not the sleep")
	// oracle: docs/design/job/run.fizz#NoWorkerAfterEnd
	require.Zero(t, runWorkers(t, h.rt, name), "the worker is gone at the run's end")
}

// scenario: job-run-pins-revision — a run Running on its first revision ends on it after the Job's image changes, the
// next run pins the second, and the first is deleted once no open run pins it.
func TestScenarioJobRunPinsRevision(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	h.deployJob(t, "pins", waitsFor(g, calls, "{ v: 1 }"), "", nil)
	h.waitJob(t, "pins")
	first := h.startRun(t, "pins", `{}`)
	require.Equal(t, v1.ObjectName("pins-v1"), h.waitRun(t, first, v1.JobRunRunning).Status.JobRevision)

	h.deployJob(t, "pins", "export function handle() { return { v: 2 }; }\n", "", nil)
	require.Eventually(t, func() bool {
		job := h.waitJob(t, "pins")
		return job.Status.CurrentRevision == "pins-v2"
	}, runWithin, 20*time.Millisecond, "the Job stamps pins-v2")
	// oracle: docs/design/job/run.fizz#PinnedRevisionKept
	stays(t, func() bool {
		_, err := h.c.Get(context.Background(), v1.KindRevision, "default", "pins-v1")
		return err == nil
	}, 300*time.Millisecond, 50*time.Millisecond, "the Running run keeps the revision it pinned")

	g.open(t)
	ended := h.waitRun(t, first, v1.JobRunSucceeded)
	require.JSONEq(t, `{"v":1}`, string(ended.Status.Result))
	require.Equal(t, v1.ObjectName("pins-v1"), ended.Status.JobRevision)
	next := h.waitRun(t, h.startRun(t, "pins", `{}`), v1.JobRunSucceeded)
	require.Equal(t, v1.ObjectName("pins-v2"), next.Status.JobRevision)
	require.JSONEq(t, `{"v":2}`, string(next.Status.Result))
	require.Eventually(t, func() bool {
		_, err := h.c.Get(context.Background(), v1.KindRevision, "default", "pins-v1")
		return fault.KindOf(err) == fault.NotFound
	}, runWithin, 20*time.Millisecond, "pins-v1 goes once no open run pins it")
}

// scenario: job-handler-fails — a handler that throws ends the run Failed with HandlerFailed and its first line.
func TestScenarioJobHandlerFails(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deployJob(t, "throws", "export function handle() { throw new Error('boom\\nat the second line'); }\n", "", nil)
	h.waitJob(t, "throws")
	run := h.waitRun(t, h.startRun(t, "throws", `{}`), v1.JobRunFailed)
	reason, msg := runReason(run)
	require.Equal(t, v1.JobRunHandlerFailed, reason, msg)
	require.Contains(t, msg, "boom")
	require.NotContains(t, msg, "second line", "only the first line")
}

// scenario: job-deleted-cancels-run — a Running run whose Job is deleted ends Cancelled with JobDeleted and stays listed.
func TestScenarioJobDeletedCancelsRun(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	g, calls := newGate(t), filepath.Join(t.TempDir(), "calls")
	t.Cleanup(func() { g.open(t) })
	h.deployJob(t, "doomed", waitsFor(g, calls, "{ done: true }"), "", nil)
	h.waitJob(t, "doomed")
	name := h.startRun(t, "doomed", `{}`)
	h.waitRun(t, name, v1.JobRunRunning)
	require.NoError(t, h.c.Delete(context.Background(), v1.KindJob, "default", "doomed"))
	run := h.waitRun(t, name, v1.JobRunCancelled)
	reason, msg := runReason(run)
	require.Equal(t, v1.JobRunJobDeleted, reason, msg)
	require.Zero(t, runWorkers(t, h.rt, name))
	stays(t, func() bool {
		_, err := h.c.Get(context.Background(), v1.KindJobRun, "default", v1.ObjectName(name))
		return err == nil
	}, 300*time.Millisecond, 50*time.Millisecond, "the run stays listed until job.retention")
}

// scenario: job-start-waits-while-held — a run created while its namespace is held stays Pending, its status untouched,
// until the release, then runs.
func TestScenarioJobStartWaitsWhileHeld(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	held, err := hold.Open(dir)
	require.NoError(t, err)
	h := newShimRig(t, "", funcd.WithHold(held), funcd.WithPacing(funcd.Pacing{ReferentPollInterval: 100 * time.Millisecond}))
	h.deployJob(t, "waits", "export function handle() { return { ran: true }; }\n", "", nil)
	h.waitJob(t, "waits")
	require.NoError(t, held.HoldNamespace("default", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))

	name := h.startRun(t, "waits", `{}`)
	before := h.getJobRun(t, name)
	stays(t, func() bool { return h.getJobRun(t, name).ResourceVersion == before.ResourceVersion },
		500*time.Millisecond, 50*time.Millisecond, "a held run's status is untouched")
	require.Empty(t, before.Status.Phase)

	require.NoError(t, held.ReleaseNamespace("default", hold.ReasonData))
	run := h.waitRun(t, name, v1.JobRunSucceeded)
	require.JSONEq(t, `{"ran":true}`, string(run.Status.Result))
}
