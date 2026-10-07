//go:build e2e

package funcd_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// ADR-0143 end to end, on the real push → apply → invoke path with the process driver and the Node shim. greeter v1
// answers "Hello, funcd!"; each test pushes its variants under new tags and applies them as `funcdctl apply` does.

// redeployHarness is the platform plus the runtime it runs, so a test can see each revision's workers.
type redeployHarness struct {
	c      *sdk.Client
	dp     string
	rt     runtime.Runtime
	layout string
}

func newRedeployHarness(t *testing.T) *redeployHarness {
	t.Helper()
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the redeploy lane")
	}
	rt := process.New(nil)
	p, err := funcd.New(funcd.InMemory(), funcd.WithRuntime(rt), funcd.WithRuntimeShim(node, shim), funcd.WithArtifactStore(t.TempDir()))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return &redeployHarness{c: c, dp: "http://" + p.DataPlaneAddr(), rt: rt, layout: t.TempDir()}
}

// push pushes greeter's bundle, edited by edit, under tag (funcdctl push) and returns its ref.
func (h *redeployHarness) push(t *testing.T, tag string, edit func(string) string) string {
	t.Helper()
	src := tsExample(t, "fn-to-fn")
	mjs, err := os.ReadFile(filepath.Join(src, "greeter.mjs"))
	require.NoError(t, err)
	manifest, err := os.ReadFile(filepath.Join(src, "greeter.funcdctl.yaml"))
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, tag+".mjs"), []byte(edit(string(mjs))), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tag+".funcdctl.yaml"), manifest, 0o600))
	ref, _ := pushExampleFn(t, h.layout, dir, tag)
	return ref
}

const greeting = "return { greeting: `Hello, ${name}!` };"

func same(src string) string { return src }

// answer makes greeter answer word instead of Hello.
func answer(word string) func(string) string {
	return func(src string) string {
		return strings.Replace(src, greeting, "return { greeting: `"+word+", ${name}!` };", 1)
	}
}

// slowAnswer makes greeter answer word after d, so a call stays in flight.
func slowAnswer(word string, d time.Duration) func(string) string {
	return func(src string) string {
		return strings.Replace(src, greeting, fmt.Sprintf(
			"return new Promise((resolve) => setTimeout(() => resolve({ greeting: `%s, ${name}!` }), %d));", word, d.Milliseconds()), 1)
	}
}

// slowBoot makes greeter answer word, but its module takes d to load, so the revision boots slowly.
func slowBoot(word string, d time.Duration) func(string) string {
	return func(src string) string {
		return fmt.Sprintf("await new Promise((resolve) => setTimeout(resolve, %d));\n", d.Milliseconds()) + answer(word)(src)
	}
}

// apply applies greeter.yaml pointing at ref by tag, changed by change.
func (h *redeployHarness) apply(t *testing.T, ref string, change func(*v1.Function)) {
	t.Helper()
	fn := loadFn(t, "greeter.yaml", ref, "")
	change(fn)
	applyFnObj(t, h.c, fn)
}

func keep(*v1.Function) {}

func (h *redeployHarness) greeter(t *testing.T) *v1.Function {
	t.Helper()
	got, err := h.c.Get(context.Background(), v1.KindFunction, "default", "greeter")
	require.NoError(t, err)
	return got.(*v1.Function)
}

// workers returns greeter's listed workers by revision.
func (h *redeployHarness) workers(t *testing.T) map[v1.ObjectName][]runtime.Instance {
	t.Helper()
	insts, err := h.rt.List(context.Background(), "default")
	require.NoError(t, err)
	out := map[v1.ObjectName][]runtime.Instance{}
	for _, in := range insts {
		if in.Name == "greeter" {
			out[in.Revision] = append(out[in.Revision], in)
		}
	}
	return out
}

// left reports whether none of revision rev's workers is listed any more.
func (h *redeployHarness) left(t *testing.T, rev v1.ObjectName) bool {
	t.Helper()
	_, listed := h.workers(t)[rev]
	return !listed
}

// call invokes greeter once and returns the status and body.
func (h *redeployHarness) call() (int, string) {
	resp, err := http.Post(h.dp+"/function/greeter", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// caller calls greeter in a loop and records every answer, so a test can count the failed calls through a switch.
type caller struct {
	mu      sync.Mutex
	answers []string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (h *redeployHarness) startCaller(t *testing.T) *caller {
	t.Helper()
	c := &caller{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			select {
			case <-c.stop:
				return
			default:
			}
			status, body := h.call()
			if status != http.StatusOK {
				body = fmt.Sprintf("FAILED %d %s", status, body)
			}
			c.mu.Lock()
			c.answers = append(c.answers, body)
			c.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
	}()
	t.Cleanup(c.halt)
	return c
}

func (c *caller) halt() {
	c.once.Do(func() { close(c.stop) })
	<-c.done
}

func (c *caller) saw(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.answers {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}

// result stops the caller and returns its answers.
func (c *caller) result() []string {
	c.halt()
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.answers...)
}

// requireSwitched checks that no call failed and the answers moved from old to new exactly once.
func requireSwitched(t *testing.T, answers []string, old, new string) {
	t.Helper()
	require.NotEmpty(t, answers)
	switched := false
	for i, a := range answers {
		require.NotContains(t, a, "FAILED", "call %d failed during the redeploy", i)
		switch {
		case strings.Contains(a, new):
			switched = true
		case strings.Contains(a, old):
			require.False(t, switched, "call %d went back to the old revision", i)
		default:
			t.Fatalf("call %d: unexpected answer %q", i, a)
		}
	}
	require.True(t, switched, "the calls reached the new revision")
}

// scenario: redeploy-switches-to-new-revision (ADR-0143) — a new tag applied to an always-on Function: no call fails,
// the answers move from v1 to v2 once, and v1's worker leaves the runtime.
func TestScenarioE2ERedeploySwitchesToNewRevision(t *testing.T) {
	h := newRedeployHarness(t)
	h.apply(t, h.push(t, "greeter", same), keep)
	waitReady(t, h.c, "greeter")
	calls := h.startCaller(t)

	h.apply(t, h.push(t, "greeter-v2", answer("Bonjour")), keep)
	require.Eventually(t, func() bool { return calls.saw("Bonjour") }, 30*time.Second, 50*time.Millisecond, "v2 answers")
	require.Eventually(t, func() bool {
		fn := h.greeter(t)
		return fn.Status.ServingRevision == "greeter-2" && fn.Status.DrainingRevision == "" && h.left(t, "greeter-1")
	}, 30*time.Second, 100*time.Millisecond, "v1 drained and left the runtime")
	requireSwitched(t, calls.result(), "Hello", "Bonjour")
}

// scenario: failed-revision-keeps-old-serving (ADR-0143) — a revision that cannot run leaves v1 answering every call.
func TestScenarioE2EFailedRevisionKeepsOldServing(t *testing.T) {
	cases := []struct {
		name, reason string
		redeploy     func(t *testing.T, h *redeployHarness)
	}{
		{"unresolvable-tag", "ArtifactUnresolved", func(t *testing.T, h *redeployHarness) {
			h.apply(t, "oci-layout://"+h.layout+":greeter-missing", keep)
		}},
		{"unloadable-handler", "ShapeInvalid", func(t *testing.T, h *redeployHarness) {
			h.apply(t, h.push(t, "greeter-v2", answer("Bonjour")), func(fn *v1.Function) { fn.Spec.Handler = "nope" })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRedeployHarness(t)
			h.apply(t, h.push(t, "greeter", same), keep)
			waitReady(t, h.c, "greeter")
			calls := h.startCaller(t)

			tc.redeploy(t, h)
			require.Eventually(t, func() bool {
				c, ok := h.greeter(t).Status.Conditions.Get("RevisionReady")
				return ok && c.Status == v1.ConditionFalse && c.Reason == tc.reason
			}, 30*time.Second, 100*time.Millisecond, "RevisionReady reports %s", tc.reason)
			time.Sleep(time.Second) // keep calling while the failed revision stays failed
			fn := h.greeter(t)
			require.Equal(t, v1.PhaseReady, fn.Status.Phase)
			require.Equal(t, "greeter-1", fn.Status.ServingRevision)
			for i, a := range calls.result() {
				require.Contains(t, a, "Hello", "call %d is answered by v1", i)
			}
		})
	}
}

// scenario: in-flight-call-finishes-on-old-revision (ADR-0143) — a call in flight on v1 completes with v1's answer
// after the calls switch to v2.
func TestScenarioE2EInFlightCallFinishesOnOldRevision(t *testing.T) {
	h := newRedeployHarness(t)
	h.apply(t, h.push(t, "greeter", slowAnswer("Slow", 3*time.Second)), keep)
	waitReady(t, h.c, "greeter")
	type result struct {
		status int
		body   string
	}
	inflight := make(chan result, 1)
	go func() {
		status, body := h.call()
		inflight <- result{status, body}
	}()
	time.Sleep(300 * time.Millisecond)

	h.apply(t, h.push(t, "greeter-v2", answer("Bonjour")), keep)
	require.Eventually(t, func() bool {
		_, body := h.call()
		return strings.Contains(body, "Bonjour")
	}, 30*time.Second, 100*time.Millisecond, "new calls reach v2")
	got := <-inflight
	require.Equal(t, http.StatusOK, got.status, "the in-flight call was not cut: %s", got.body)
	require.Contains(t, got.body, "Slow, funcd!", "it completed on v1")
	require.Eventually(t, func() bool { return h.left(t, "greeter-1") }, 40*time.Second, 100*time.Millisecond,
		"v1 leaves once its call ended")
}

// scenario: idle-function-starts-new-revision-on-wake (ADR-0143) — a scale-to-zero Function that no call has woken
// boots nothing on a redeploy; the first call wakes v2.
func TestScenarioE2EIdleFunctionStartsNewRevisionOnWake(t *testing.T) {
	h := newRedeployHarness(t)
	idle := func(fn *v1.Function) { fn.Spec.Replicas, fn.Spec.Scaling = 0, v1.Scaling{} }
	h.apply(t, h.push(t, "greeter", same), idle)
	require.Eventually(t, func() bool { return phaseOf(t, h.c, "greeter") == v1.PhaseIdle }, 20*time.Second, 50*time.Millisecond)

	h.apply(t, h.push(t, "greeter-v2", answer("Bonjour")), idle)
	require.Eventually(t, func() bool { return h.greeter(t).Status.CurrentRevision == "greeter-2" }, 20*time.Second, 50*time.Millisecond)
	require.Empty(t, h.workers(t), "no worker boots while the Function is idle")
	status, body := h.call()
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, "Bonjour", "the first call wakes v2")
}

// scenario: replicas-change-switches-without-dropping (ADR-0143) — 3 replicas → 1: no call fails, and one v2 worker
// remains.
func TestScenarioE2EReplicasChangeSwitchesWithoutDropping(t *testing.T) {
	h := newRedeployHarness(t)
	ref := h.push(t, "greeter", same)
	h.apply(t, ref, func(fn *v1.Function) { fn.Spec.Replicas, fn.Spec.Scaling.MinReplicas = 3, 3 })
	waitReady(t, h.c, "greeter")
	require.Eventually(t, func() bool { return len(h.workers(t)["greeter-1"]) == 3 }, 20*time.Second, 50*time.Millisecond)
	calls := h.startCaller(t)

	h.apply(t, ref, func(fn *v1.Function) { fn.Spec.Replicas, fn.Spec.Scaling.MinReplicas = 1, 1 })
	require.Eventually(t, func() bool {
		w := h.workers(t)
		return len(w) == 1 && len(w["greeter-2"]) == 1 && h.greeter(t).Status.DrainingRevision == ""
	}, 30*time.Second, 100*time.Millisecond, "one v2 worker remains")
	for i, a := range calls.result() {
		require.Contains(t, a, "Hello", "call %d succeeded", i)
	}
}

// scenario: newer-apply-supersedes-booting-revision (ADR-0143) — v3 supersedes a slow-booting v2: v2 never answers,
// its worker leaves, and v1 serves until v3 is ready.
func TestScenarioE2ENewerApplySupersedesBootingRevision(t *testing.T) {
	h := newRedeployHarness(t)
	h.apply(t, h.push(t, "greeter", same), keep)
	waitReady(t, h.c, "greeter")
	calls := h.startCaller(t)

	h.apply(t, h.push(t, "greeter-v2", slowBoot("Slowboot", 5*time.Second)), keep)
	require.Eventually(t, func() bool { return len(h.workers(t)["greeter-2"]) == 1 }, 20*time.Second, 50*time.Millisecond, "v2 boots")
	h.apply(t, h.push(t, "greeter-v3", answer("Bonjour")), keep)
	require.Eventually(t, func() bool { return calls.saw("Bonjour") }, 30*time.Second, 50*time.Millisecond, "v3 answers")
	require.Eventually(t, func() bool { return h.left(t, "greeter-1") && h.left(t, "greeter-2") }, 30*time.Second, 100*time.Millisecond,
		"v1 and v2 left the runtime")
	answers := calls.result()
	for i, a := range answers {
		require.NotContains(t, a, "Slowboot", "call %d reached the superseded v2", i)
	}
	requireSwitched(t, answers, "Hello", "Bonjour")
}

// scenario: serving-worker-crash-during-switch-is-replaced (ADR-0143) — v1's only worker dies while a slow v2 boots;
// supervision replaces it from v1's Revision, v1 answers again, and v2 takes over once ready.
func TestScenarioE2EServingWorkerCrashDuringSwitchIsReplaced(t *testing.T) {
	h := newRedeployHarness(t)
	h.apply(t, h.push(t, "greeter", same), keep)
	waitReady(t, h.c, "greeter")
	h.apply(t, h.push(t, "greeter-v2", slowBoot("Bonjour", 20*time.Second)), keep)
	require.Eventually(t, func() bool { return len(h.workers(t)["greeter-2"]) == 1 }, 20*time.Second, 50*time.Millisecond, "v2 boots")

	v1 := h.workers(t)["greeter-1"][0]
	proc, err := os.FindProcess(v1.PID)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	require.Eventually(t, func() bool {
		w := h.workers(t)["greeter-1"]
		return len(w) == 1 && w[0].State == runtime.StateRunning && w[0].PID != v1.PID
	}, 30*time.Second, 100*time.Millisecond, "v1's worker is replaced")
	status, body := h.call()
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, "Hello", "v1 answers again while v2 still boots")
	require.Eventually(t, func() bool {
		_, b := h.call()
		return strings.Contains(b, "Bonjour")
	}, 40*time.Second, 200*time.Millisecond, "v2 takes over once ready")
}

// scenario: steady-function-stays-quiescent (ADR-0143) — once v1 has drained, a supervision pass writes nothing.
func TestScenarioE2ESteadyFunctionStaysQuiescent(t *testing.T) {
	h := newRedeployHarness(t)
	h.apply(t, h.push(t, "greeter", same), keep)
	waitReady(t, h.c, "greeter")
	h.apply(t, h.push(t, "greeter-v2", answer("Bonjour")), keep)
	require.Eventually(t, func() bool {
		fn := h.greeter(t)
		return fn.Status.ServingRevision == "greeter-2" && fn.Status.DrainingRevision == "" && h.left(t, "greeter-1")
	}, 30*time.Second, 100*time.Millisecond, "v1 drained")

	rv := h.greeter(t).ResourceVersion
	time.Sleep(controller.SupervisionPeriod + time.Second)
	fn := h.greeter(t)
	require.Equal(t, rv, fn.ResourceVersion, "a supervision pass wrote nothing")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
}
