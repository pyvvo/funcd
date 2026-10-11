//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The ADR-0226 legs on the real shims. The shims' delays are constants, not settings (ADR-0226 Decision 4): a stop
// comes 1 s after the deadline, an async call's raise 1 s after its cancel, and the stuck delay, also the TypeScript
// ping window, is 10 s, so the three tests that wait it out take over 10 s. Each parallel test has its own pool worker
// name: the pool manifest file is per host, keyed by namespace and worker.
const stuckTimeout = time.Second

const (
	pyStuck = `import logging
import os
import time

import funcd_shim


def handle(ctx, event):
    mode = (event.get("data") or {}).get("mode")
    if mode == "loop":
        try:
            while True:
                pass
        except funcd_shim.DeadlineExceeded:
            logging.warning("caught DeadlineExceeded")
            raise
    if mode == "sleep":
        time.sleep(3600)
    return {"pid": os.getpid()}
`
	pyStuckAsync = `import asyncio
import logging
import os

import funcd_shim


async def handle(ctx, event):
    mode = (event.get("data") or {}).get("mode")
    if mode == "await":
        try:
            await asyncio.sleep(3600)
        except asyncio.CancelledError:
            logging.warning("caught CancelledError")
            raise
    if mode == "spin":
        try:
            while True:
                pass
        except funcd_shim.DeadlineExceeded:
            logging.warning("caught DeadlineExceeded")
            raise
    return {"pid": os.getpid()}
`
	nodeStuck = `import { threadId } from 'node:worker_threads';

export async function handle(_ctx, e) {
  const d = e.data ?? {};
  if (d.stuck) for (;;) {}
  if (d.ms) await new Promise((r) => setTimeout(r, d.ms));
  return { pid: process.pid, thread: threadId };
}
`
)

// lateCall invokes name with body, requires funcd's 504 at stuckTimeout and returns the call's deadline.
func (h *shimRig) lateCall(t *testing.T, name, body string) time.Time {
	t.Helper()
	start := time.Now()
	r := h.postWithin(name, body, 10*time.Second)
	requireDeadline504(t, r, time.Since(start), stuckTimeout)
	return start.Add(stuckTimeout)
}

// served requires r to be a 200 and returns its fields.
func served(t *testing.T, r reply) replyFields {
	t.Helper()
	require.Equalf(t, http.StatusOK, r.status, "the call is served: %s", r.body)
	return decodeReply(t, r)
}

// logTime waits for fn's stored log line whose body is body and returns when the handler logged it.
func (h *shimRig) logTime(t *testing.T, fn, body string) time.Time {
	t.Helper()
	var at time.Time
	require.Eventually(t, func() bool {
		lines, err := h.c.Logs(context.Background(), "default", v1.ObjectName(fn), sdk.LogsOptions{})
		i := slices.IndexFunc(lines, func(l logread.Line) bool { return l.Body == body })
		if err != nil || i < 0 {
			return false
		}
		at = time.Time(lines[i].Time)
		return true
	}, 10*time.Second, 100*time.Millisecond, "%s logs %q", fn, body)
	return at
}

// soloWorker waits for a listening worker of Function name's current Revision and returns it.
func (h *shimRig) soloWorker(t *testing.T, name string) runtime.Instance {
	t.Helper()
	return revisionWorker(t, h.rt, v1.ObjectName(name), v1.ObjectName(h.function(t, name).Status.CurrentRevision))
}

// liveness asks w's shim for /health/liveness and returns its status code and body, or -1 and the error.
func liveness(w runtime.Instance) (int, string) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://%s:%d/health/liveness", w.IP, w.Port))
	if err != nil {
		return -1, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// keepCalling calls name every 100 ms on another goroutine; the returned func stops it and returns the replies.
func (h *shimRig) keepCalling(t *testing.T, name string) func() []reply {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	out := make(chan []reply, 1)
	go func() {
		var got []reply
		for ctx.Err() == nil {
			got = append(got, h.postWithin(name, `{}`, 5*time.Second))
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		out <- got
	}()
	return func() []reply {
		cancel()
		return <-out
	}
}

// scenario: handler-reads-deadline — a Ready replica of each language returns its context's remaining time.
func TestScenarioHandlerReadsDeadline(t *testing.T) {
	t.Parallel()
	src := map[string]string{
		"nodejs22":  "export function handle(ctx) { return { remaining: ctx.remainingMs() }; }\n",
		"python314": "def handle(ctx, event):\n    return {\"remaining\": ctx.remaining_ms()}\n",
	}
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		t.Parallel()
		h := newShimRig(t, l.python)
		h.deploy(t, "remaining", l.fn(src[l.runtime]).withTimeout(5*time.Second))
		waitReady(t, h.c, "remaining")
		r := h.post("remaining", `{}`)
		require.Equal(t, http.StatusOK, r.status, r.body)
		var out struct {
			Remaining json.Number `json:"remaining"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.body), &out), r.body)
		ms, err := out.Remaining.Int64()
		require.NoError(t, err, "a whole number of milliseconds: %s", r.body)
		require.Greater(t, ms, int64(3000), r.body)
		require.LessOrEqual(t, ms, int64(5000), r.body)
	})
}

// scenario: stuck-python-call-stopped (ADR-0226, #928) — a solo Python handler looping in Python code sees
// DeadlineExceeded after funcd's 504, and its replica serves on from the same process.
func TestIssue928_StuckPythonCallStops(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, requirePython(t, false))
	h.deploy(t, "loops", pythonFn(pyStuck).withTimeout(stuckTimeout))
	waitReady(t, h.c, "loops")
	pid := served(t, h.post("loops", `{}`)).PID
	w := h.soloWorker(t, "loops")

	deadline := h.lateCall(t, "loops", `{"mode":"loop"}`)
	require.WithinDuration(t, deadline, h.logTime(t, "loops", "caught DeadlineExceeded"), 2*time.Second,
		"the handler sees DeadlineExceeded within 2 s of its deadline")
	code, body := liveness(w)
	require.Equal(t, http.StatusOK, code, "the replica answers liveness: %s", body)
	require.Equal(t, pid, served(t, h.post("loops", `{}`)).PID, "the replica keeps its process and serves the next call")
}

// scenario: async-python-call-cancelled
func TestScenarioAsyncPythonCallCancelled(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, requirePython(t, false))
	h.deploy(t, "awaits", pythonFn(pyStuckAsync).withTimeout(stuckTimeout))
	waitReady(t, h.c, "awaits")
	pid := served(t, h.post("awaits", `{}`)).PID

	deadline := h.lateCall(t, "awaits", `{"mode":"await"}`)
	require.WithinDuration(t, deadline, h.logTime(t, "awaits", "caught CancelledError"), 2*time.Second,
		"the task is cancelled within 2 s of its deadline")
	require.Equal(t, pid, served(t, h.post("awaits", `{}`)).PID, "the replica keeps its process")
}

// scenario: spinning-async-python-call-stopped
func TestScenarioSpinningAsyncPythonCallStopped(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, requirePython(t, false))
	h.deploy(t, "spins", pythonFn(pyStuckAsync).withTimeout(stuckTimeout))
	waitReady(t, h.c, "spins")
	pid := served(t, h.post("spins", `{}`)).PID

	deadline := h.lateCall(t, "spins", `{"mode":"spin"}`)
	require.WithinDuration(t, deadline, h.logTime(t, "spins", "caught DeadlineExceeded"), 3*time.Second,
		"the handler sees DeadlineExceeded within 3 s of its deadline")
	require.Equal(t, pid, served(t, h.post("spins", `{}`)).PID, "the replica keeps its process")
}

// scenario: stuck-pooled-python-member-stopped
func TestScenarioStuckPooledPythonMemberStopped(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, requirePython(t, true))
	for _, n := range []string{"a", "b"} {
		h.deploy(t, n, pythonFn(pyStuck).pooled("w226p").withTimeout(stuckTimeout))
	}
	waitReady(t, h.c, "a", "b")
	h.samePool(t, "a", "b")
	h.settledPoolPID(t, "python314", "w226p")
	pid := served(t, h.post("a", `{}`)).PID
	b := h.keepCalling(t, "b")

	deadline := h.lateCall(t, "a", `{"mode":"loop"}`)
	require.WithinDuration(t, deadline, h.logTime(t, "a", "caught DeadlineExceeded"), 2*time.Second,
		"a's call ends with DeadlineExceeded within 2 s of its deadline")
	require.Equal(t, pid, served(t, h.post("a", `{}`)).PID, "a serves its next call in the pool host's process")
	for _, r := range b() {
		require.Equal(t, pid, served(t, r).PID, "b serves throughout in the pool host's process")
	}
}

// scenario: stuck-pooled-node-member-restarted — over 10 s: the host ends a's worker only once it has missed a ping
// for the 10 s window.
func TestScenarioStuckPooledNodeMemberRestarted(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	for _, n := range []string{"a", "b"} {
		h.deploy(t, n, nodeFn(nodeStuck).pooled("w226n").withTimeout(stuckTimeout))
	}
	waitReady(t, h.c, "a", "b")
	h.samePool(t, "a", "b")
	h.settledPoolPID(t, "nodejs22", "w226n")
	before := served(t, h.post("a", `{}`))
	b := h.keepCalling(t, "b")

	deadline := h.lateCall(t, "a", `{"stuck":true}`)
	// a's calls wait on its blocked worker until funcd cuts them; the first other answer comes once that worker ends.
	var ended time.Time
	var again replyFields
	require.Eventually(t, func() bool {
		r := h.postWithin("a", `{}`, 5*time.Second)
		if r.status != http.StatusGatewayTimeout && ended.IsZero() {
			ended = time.Now()
		}
		return r.status == http.StatusOK && json.Unmarshal([]byte(r.body), &again) == nil
	}, 20*time.Second, 20*time.Millisecond, "a serves again")
	require.WithinDuration(t, deadline, ended, 12*time.Second, "a's worker is ended within 12 s of its deadline")
	require.NotEqual(t, before.Thread, again.Thread, "a's worker was restarted")
	require.Equal(t, before.PID, again.PID, "the host keeps its process")
	for _, r := range b() {
		require.Equal(t, before.PID, served(t, r).PID, "b serves throughout in the host's process")
	}
}

// scenario: late-await-keeps-node-worker — over 10 s: the second calls run until the ping window has passed.
func TestScenarioLateAwaitKeepsNodeWorker(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "")
	h.deploy(t, "late", nodeFn(nodeStuck).pooled("w226l").withTimeout(stuckTimeout))
	waitReady(t, h.c, "late")
	h.settledPoolPID(t, "nodejs22", "w226l")
	before := served(t, h.post("late", `{}`))

	start := time.Now()
	var lateReply reply
	took := make(chan time.Duration, 1)
	go func() {
		lateReply = h.postWithin("late", `{"ms":5000}`, 10*time.Second)
		took <- time.Since(start)
	}()
	for deadline := start.Add(stuckTimeout); time.Since(deadline) < 12*time.Second; {
		got := served(t, h.post("late", `{"ms":300}`))
		require.Equal(t, before.Thread, got.Thread, "the worker is not ended")
	}
	elapsed := <-took
	requireDeadline504(t, lateReply, elapsed, stuckTimeout)
}

// scenario: unstoppable-call-restarts-replica — over 10 s: liveness fails only 10 s after the stop (the stuck delay);
// runtime.livenessTimeout is 1 s.
func TestScenarioUnstoppableCallRestartsReplica(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, requirePython(t, false),
		funcd.WithPacing(funcd.Pacing{SupervisionPeriod: 500 * time.Millisecond, LivenessTimeout: time.Second}))
	h.deploy(t, "sleeps", pythonFn(pyStuck).withTimeout(stuckTimeout))
	waitReady(t, h.c, "sleeps")
	pid := served(t, h.post("sleeps", `{}`)).PID
	w := h.soloWorker(t, "sleeps")

	deadline := h.lateCall(t, "sleeps", `{"mode":"sleep"}`)
	var failed time.Time
	var code int
	var body string
	require.Eventually(t, func() bool {
		code, body = liveness(w)
		if code == http.StatusOK {
			return false
		}
		failed = time.Now()
		return true
	}, 15*time.Second, 100*time.Millisecond, "liveness stops answering 200")
	require.Equal(t, http.StatusServiceUnavailable, code, body)
	require.Contains(t, body, "stuck call")
	require.GreaterOrEqual(t, failed.Sub(deadline), 11*time.Second, "liveness answers 200 until 11 s after the deadline")
	require.Less(t, failed.Sub(deadline), 12*time.Second, "liveness answers 503 from 11 s after the deadline")
	require.Eventually(t, func() bool {
		var out replyFields
		r := h.post("sleeps", `{}`)
		return r.status == http.StatusOK && json.Unmarshal([]byte(r.body), &out) == nil && out.PID != pid
	}, 15*time.Second, 100*time.Millisecond, "funcd restarts the replica: a new process serves")
}
