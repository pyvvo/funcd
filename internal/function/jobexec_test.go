package function_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/volume"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// execRig is the run reconciler with its own executor on the shim harness: the worker is built on the fake runtime, and
// each run's worker can get an endpoint of its own (serveRun). Each executor's end is signalled on ended.
type execRig struct {
	*shimHarness
	runs    *function.JobRunReconciler
	ended   chan v1.ObjectName
	mu      sync.Mutex
	created []runtime.WorkerSpec // every spec the executors created a worker from, kept past its removal
}

func newExecRig(t *testing.T, payloadLimit int64, opts ...func(*function.Deps)) *execRig {
	t.Helper()
	return newExecRigWith(t, payloadLimit, nil, opts...)
}

// newExecRigWith is newExecRig whose run reconciler's config runs edits, when set.
func newExecRigWith(t *testing.T, payloadLimit int64, runs func(*function.RunsConfig), opts ...func(*function.Deps)) *execRig {
	t.Helper()
	h := newShimHarness(t, http.StatusOK, false, append([]func(*function.Deps){func(d *function.Deps) { d.BootTimeout = 2 * time.Second }}, opts...)...)
	rig := &execRig{shimHarness: h, ended: make(chan v1.ObjectName, 16)}
	h.rt.onCreate = func(spec runtime.WorkerSpec) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.created = append(rig.created, spec)
	}
	cfg := function.RunsConfig{MaxRunning: 4, PayloadLimit: payloadLimit, Retention: time.Hour,
		Enqueue: func(req controller.Request) { rig.ended <- req.Name }}
	if runs != nil {
		runs(&cfg)
	}
	rig.runs = h.r.Runs(cfg)
	t.Cleanup(rig.runs.Close)
	return rig
}

// createdFor is the specs of the workers created for revision rev under name.
func (h *execRig) createdFor(name string, rev v1.ObjectName) []runtime.WorkerSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []runtime.WorkerSpec
	for _, spec := range h.created {
		if spec.Name == v1.ObjectName(name) && spec.Revision == rev {
			out = append(out, spec)
		}
	}
	return out
}

// readyJob creates Job hello on the harness's artifact with approval.bypass, which edit may turn off, and passes it,
// so it is Ready at hello-v1.
func (h *execRig) readyJob(t *testing.T, edit func(*v1.JobSpec)) *v1.Job {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindJob)
	job := obj.(*v1.Job)
	job.Name, job.Namespace, job.ResourceGroup = "hello", "default", "rg1"
	job.Spec = v1.JobSpec{Runtime: "nodejs22", Handler: "handle", Image: "file://" + h.artifact,
		Approval: v1.JobApproval{Bypass: true}}
	if edit != nil {
		edit(&job.Spec)
	}
	_, err := h.st.Create(context.Background(), job)
	require.NoError(t, err)
	_, err = h.r.Jobs().Reconcile(context.Background(), controller.Request{GVK: v1.KindJob.GVK(), Namespace: "default", Name: "hello"})
	require.NoError(t, err)
	obj, err = h.st.Get(context.Background(), v1.KindJob.GVK(), "default", "hello")
	require.NoError(t, err)
	job = obj.(*v1.Job)
	require.Equal(t, v1.ConditionTrue, readyOf(t, job).Status, readyOf(t, job).Message)
	return job
}

func (h *execRig) run(t *testing.T, name, input string, edit func(*v1.JobRun)) *v1.JobRun {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindJobRun)
	run := obj.(*v1.JobRun)
	run.Name, run.Namespace, run.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	run.Spec.Job, run.Spec.Input = "hello", json.RawMessage(input)
	if edit != nil {
		edit(run)
	}
	created, err := h.st.Create(context.Background(), run)
	require.NoError(t, err)
	return created.(*v1.JobRun)
}

func (h *execRig) pass(t *testing.T, name string) *v1.JobRun {
	t.Helper()
	_, err := h.runs.Reconcile(context.Background(), controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
	obj, err := h.st.Get(context.Background(), v1.KindJobRun.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.JobRun)
}

// end waits for the executor of run name to end and passes the run once more, which writes its end.
func (h *execRig) end(t *testing.T, name string) *v1.JobRun {
	t.Helper()
	for {
		select {
		case got := <-h.ended:
			if got == v1.ObjectName(name) {
				return h.pass(t, name)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the executor of %s did not end", name)
		}
	}
}

// serveRun gives the worker of run name its own endpoint: its health paths answer 200 at once, its call runs answer.
func (h *execRig) serveRun(t *testing.T, name string, answer http.HandlerFunc) {
	t.Helper()
	h.serveRunWith(t, name, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, answer)
}

// serveRunWith serves run name's worker: health answers its probes, and answer every other call.
func (h *execRig) serveRunWith(t *testing.T, name string, health, answer http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/health/") {
			health(w, r)
			return
		}
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	p, err := strconv.Atoi(port)
	require.NoError(t, err)
	h.rt.mu.Lock()
	h.rt.idPort[runID(name)] = p
	h.rt.mu.Unlock()
}

func runID(name string) runtime.InstanceID {
	return runtime.NewInstanceID("default", v1.ObjectName(name), "hello-v1", 0)
}

// awaitHangUp holds a call until its caller hangs up, or for 10 s. The server notices a closed connection only once the
// body has been read.
func awaitHangUp(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
}

// answering answers every call with status and body, and records each request it got.
type answering struct {
	status int
	body   string
	mu     sync.Mutex
	got    []recorded
}

type recorded struct {
	header http.Header
	body   []byte
}

func (a *answering) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.got = append(a.got, recorded{header: r.Header.Clone(), body: b})
	a.mu.Unlock()
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

func (a *answering) calls() []recorded {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]recorded(nil), a.got...)
}

func requireEnd(t *testing.T, run *v1.JobRun, phase v1.JobRunPhase, reason string) v1.Condition {
	t.Helper()
	require.Equal(t, phase, run.Status.Phase)
	c, ok := run.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, reason, c.Reason, c.Message)
	return c
}

// ADR-0347 Decision 5: the executor builds one worker named after the run, of kind JobRun, on the pinned revision, with
// the four FUNCD_JOB* vars and no FUNCD_FUNCTION or local API socket; it calls it once with the run's CloudEvent and
// traceparent and no timeout header when the Job has no timeout, writes the answer, and removes the worker.
func TestJobRunExecutorCallsItsWorkerOnce(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10)
	h.readyJob(t, nil)
	run := h.run(t, "hello-a", `{"name":"Ada"}`, func(r *v1.JobRun) { r.Status.StartedBy = "alice" })
	worker := &answering{status: http.StatusOK, body: `{"greeting":"hi Ada"}`}
	h.serveRun(t, "hello-a", worker.ServeHTTP)
	started := h.pass(t, "hello-a")
	require.Equal(t, v1.JobRunRunning, started.Status.Phase)

	ended := h.end(t, "hello-a")
	requireEnd(t, ended, v1.JobRunSucceeded, "")
	require.JSONEq(t, `{"greeting":"hi Ada"}`, string(ended.Status.Result))
	specs := h.createdFor("hello-a", "hello-v1")
	require.Len(t, specs, 1, "one worker per run")
	spec := specs[0]
	require.Equal(t, v1.KindJobRun, spec.OwnerKind)
	require.Equal(t, v1.ObjectName("hello-a"), spec.Name)
	require.Equal(t, v1.ObjectName("hello-v1"), spec.Revision)
	for k, v := range map[string]string{"FUNCD_JOB": "hello", "FUNCD_JOB_RUN": "hello-a", "FUNCD_JOB_REVISION": "hello-v1",
		"FUNCD_JOB_STARTED_BY": "alice", "FUNCD_HANDLER": "handle"} {
		require.Equal(t, v, spec.Env[k], k)
	}
	_, ok := spec.Env["FUNCD_FUNCTION"]
	require.False(t, ok, "FUNCD_FUNCTION is not a run worker's")
	calls := worker.calls()
	require.Len(t, calls, 1, "the handler is called once")
	require.JSONEq(t, `{"specversion":"1.0","type":"funcd.job.run","source":"funcd://default/job/hello","id":"`+string(run.UID)+
		`","data":{"name":"Ada"}}`, string(calls[0].body))
	require.Equal(t, "application/json", calls[0].header.Get("Content-Type"))
	require.True(t, strings.HasPrefix(calls[0].header.Get("traceparent"), "00-"+started.Status.TraceID+"-"), calls[0].header.Get("traceparent"))
	require.Empty(t, calls[0].header.Get(activator.TimeoutHeader), "a Job with no timeout sets no deadline")
	require.True(t, h.rt.wasRemoved(runID("hello-a")), "the worker is removed at the run's end")
}

// ADR-0347 Decision 5: a 2xx ends the run Succeeded with its body, PayloadLimitExceeded over job.payloadLimit; a 422
// InputSchemaMismatch; any other answer HandlerFailed with its cause's first line. A body that is no JSON cannot be
// the result.
func TestJobRunExecutorAnswers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status       int
		body         string
		phase        v1.JobRunPhase
		reason, says string
		result       string
	}{
		"ok":        {status: http.StatusOK, body: `[1,2]`, phase: v1.JobRunSucceeded, result: `[1,2]`},
		"empty":     {status: http.StatusNoContent, phase: v1.JobRunSucceeded},
		"over":      {status: http.StatusOK, body: `"` + strings.Repeat("x", 40) + `"`, phase: v1.JobRunFailed, reason: v1.JobRunPayloadLimitExceeded, says: "job.payloadLimit 32"},
		"not-json":  {status: http.StatusOK, body: `hi`, phase: v1.JobRunFailed, reason: v1.JobRunHandlerFailed, says: "the answer is not JSON"},
		"thrown":    {status: http.StatusInternalServerError, body: `{"error":"boom\nat handle"}`, phase: v1.JobRunFailed, reason: v1.JobRunHandlerFailed, says: "the handler answered 500: boom"},
		"off-input": {status: http.StatusUnprocessableEntity, body: `{"error":"event data does not match the input contract"}`, phase: v1.JobRunFailed, reason: v1.JobRunInputSchemaMismatch, says: "does not match"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newExecRig(t, 32)
			h.readyJob(t, nil)
			h.run(t, "hello-a", `{}`, nil)
			h.serveRun(t, "hello-a", (&answering{status: tc.status, body: tc.body}).ServeHTTP)
			h.pass(t, "hello-a")
			ended := h.end(t, "hello-a")
			c := requireEnd(t, ended, tc.phase, tc.reason)
			if tc.says != "" {
				require.Contains(t, c.Message, tc.says)
				require.NotContains(t, c.Message, "\n", "a message is one line")
			}
			if tc.result != "" {
				require.JSONEq(t, tc.result, string(ended.Status.Result))
			} else {
				require.Empty(t, ended.Status.Result)
			}
			require.True(t, h.rt.wasRemoved(runID("hello-a")))
		})
	}
}

// ADR-0347 Decision 6 and run.fizz NoWorkerAfterEnd: the Job's timeout, from the worker's start, cuts the call, which
// carried the time left in X-Funcd-Timeout-Ms, ends the run Failed with RunTimedOut and removes the worker.
func TestJobRunExecutorTimeout(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10)
	h.readyJob(t, func(s *v1.JobSpec) { s.Timeout = v1.Duration(300 * time.Millisecond) })
	h.run(t, "hello-a", `{}`, nil)
	left := make(chan string, 1)
	h.serveRun(t, "hello-a", func(w http.ResponseWriter, r *http.Request) {
		left <- r.Header.Get(activator.TimeoutHeader)
		awaitHangUp(r)
	})
	h.pass(t, "hello-a")
	c := requireEnd(t, h.end(t, "hello-a"), v1.JobRunFailed, v1.JobRunTimedOut)
	require.Contains(t, c.Message, "300ms")
	ms, err := strconv.Atoi(<-left)
	require.NoError(t, err)
	require.Positive(t, ms)
	require.LessOrEqual(t, ms, 300)
	require.True(t, h.rt.wasRemoved(runID("hello-a")))
}

// ADR-0347 Decision 5: a worker that does not listen and become ready within runtime.bootTimeout, or that ends first,
// ends the run Failed with StartFailed, and is removed.
func TestJobRunExecutorStartFailed(t *testing.T) {
	t.Parallel()
	t.Run("never-ready", func(t *testing.T) {
		t.Parallel()
		h := newExecRig(t, 1<<10, func(d *function.Deps) { d.BootTimeout = 200 * time.Millisecond })
		h.readyJob(t, nil)
		h.rt.setHoldNew(true)
		h.run(t, "hello-a", `{}`, nil)
		h.pass(t, "hello-a")
		c := requireEnd(t, h.end(t, "hello-a"), v1.JobRunFailed, v1.JobRunStartFailed)
		require.True(t, strings.HasSuffix(c.Message, "within runtime.bootTimeout 200ms"), "no report, the timeout alone: %s", c.Message)
		require.True(t, h.rt.wasRemoved(runID("hello-a")))
	})
	t.Run("exits", func(t *testing.T) {
		t.Parallel()
		h := newExecRig(t, 1<<10)
		h.readyJob(t, nil)
		h.rt.mu.Lock()
		h.rt.startExit[runID("hello-a")] = runtime.Exit{Cause: runtime.ExitByCode, Code: 1}
		h.rt.mu.Unlock()
		h.run(t, "hello-a", `{}`, nil)
		h.pass(t, "hello-a")
		c := requireEnd(t, h.end(t, "hello-a"), v1.JobRunFailed, v1.JobRunStartFailed)
		require.Contains(t, c.Message, "exit code 1")
	})
	// ADR-0215: a worker whose last readiness answer was a dependency report fails with it too, its kind, binding,
	// message and reason, as a Function's DependencyNotReady shows it.
	t.Run("dependency-report", func(t *testing.T) {
		t.Parallel()
		h := newExecRig(t, 1<<10, func(d *function.Deps) { d.BootTimeout = 300 * time.Millisecond })
		h.readyJob(t, nil)
		h.serveRunWith(t, "hello-a", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health/readiness" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"kind":"secret","binding":"api-keys","reason":"Forbidden","message":"read denied by policy default/no-reads"}`)
		}, func(http.ResponseWriter, *http.Request) {})
		h.run(t, "hello-a", `{}`, nil)
		h.pass(t, "hello-a")
		c := requireEnd(t, h.end(t, "hello-a"), v1.JobRunFailed, v1.JobRunStartFailed)
		require.Contains(t, c.Message, `within runtime.bootTimeout 300ms: secret binding "api-keys": read denied by policy default/no-reads (Forbidden)`)
	})
}

// recordingIndex is an egress worker index that keeps its entries.
type recordingIndex struct {
	mu      sync.Mutex
	entries map[netip.Addr]auth.EntityRef
}

func (x *recordingIndex) Add(ip netip.Addr, ref auth.EntityRef) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.entries[ip] = ref
}

func (x *recordingIndex) Remove(ip netip.Addr) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.entries, ip)
}

func (x *recordingIndex) lookup(ip netip.Addr) (auth.EntityRef, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	ref, ok := x.entries[ip]
	return ref, ok
}

// ADR-0351 Decision 6 with ADR-0117 §5: with egress on, a run's worker is in the egress index as the Job its run names
// from before its one call, so the gateway knows its first connection, and is out of it once the worker is removed.
func TestJobRunWorkerIsInTheEgressIndexForItsCall(t *testing.T) {
	t.Parallel()
	index := &recordingIndex{entries: map[netip.Addr]auth.EntityRef{}}
	h := newExecRigWith(t, 1<<10, func(c *function.RunsConfig) { c.Egress = index })
	h.readyJob(t, nil)
	h.run(t, "hello-a", `{}`, nil)
	addr := netip.MustParseAddr(h.rt.ip)
	during := make(chan auth.EntityRef, 1)
	h.serveRun(t, "hello-a", func(w http.ResponseWriter, _ *http.Request) {
		ref, _ := index.lookup(addr)
		during <- ref
		_, _ = io.WriteString(w, `{}`)
	})
	h.pass(t, "hello-a")
	requireEnd(t, h.end(t, "hello-a"), v1.JobRunSucceeded, "")
	require.Equal(t, auth.EntityRef{Type: v1.KindJob, Namespace: "default", Name: "hello"}, <-during, "the Job, during the call")
	_, ok := index.lookup(addr)
	require.False(t, ok, "out of the index once the worker is removed")
	require.True(t, h.rt.wasRemoved(runID("hello-a")))
}

// socketLog is a local API socket provider that records each socket bound and removed, in order.
type socketLog struct {
	mu     sync.Mutex
	events []string
}

func (s *socketLog) SocketFor(ref local.Ref) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "bind "+ref.String())
	return "/absent/" + string(ref.Run) + ".sock", nil
}

func (*socketLog) PoolSocketFor(v1.NamespaceName, v1.ObjectName, []v1.ObjectName) (string, error) {
	return "", nil
}

func (s *socketLog) Remove(ref local.Ref) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "remove "+ref.String())
}

func (s *socketLog) ListenerFor(ref local.Ref, id runtime.InstanceID) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "listen "+ref.String()+" "+string(id))
	return "http://127.0.0.1:40001", "token-of-" + string(id), nil
}

func (s *socketLog) ReleaseListener(id runtime.InstanceID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "release "+string(id))
}

func (s *socketLog) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// heldIndex is an egress worker index whose first removal waits until free is called.
type heldIndex struct {
	once, freed  sync.Once
	hit, release chan struct{}
}

func (x *heldIndex) free() { x.freed.Do(func() { close(x.release) }) }

func (*heldIndex) Add(netip.Addr, auth.EntityRef) {}

func (x *heldIndex) Remove(netip.Addr) {
	first := false
	x.once.Do(func() { first = true; close(x.hit) })
	if first {
		<-x.release
	}
}

// releaseLog is a volume manager that records, at each release, whether the run's worker still exists.
type releaseLog struct {
	volume.Manager
	present func() bool
	mu      sync.Mutex
	at      []bool
}

func (v *releaseLog) Release(ctx context.Context, id volume.ID) error {
	v.mu.Lock()
	v.at = append(v.at, v.present())
	v.mu.Unlock()
	return v.Manager.Release(ctx, id)
}

// ADR-0351 Decision 2: a run re-created under the name of a run whose executor still clears its worker meets nothing of
// the old run. The old executor removes the run's socket, its egress index entry and its emptyDirs before its worker,
// which the new run waits for: the new run binds a socket only once the old one is gone, and the old run's emptyDir is
// released while its worker still exists.
func TestJobRunReusedNameMeetsNothingOfTheOldRun(t *testing.T) {
	t.Parallel()
	socks := &socketLog{}
	idx := &heldIndex{hit: make(chan struct{}), release: make(chan struct{})}
	defer idx.free() // before the rig's cleanups: a failed assertion leaves no executor waiting
	vols := &releaseLog{}
	h := newExecRigWith(t, 1<<10, func(c *function.RunsConfig) { c.Egress = idx }, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir(), CapacityBytes: 2 << 20})
		require.NoError(t, err)
		vols.Manager = m
		d.Volumes, d.InvokeSockets = vols, socks
	})
	present := func() bool {
		all, _ := h.rt.List(context.Background(), "default")
		return slices.ContainsFunc(all, func(in runtime.Instance) bool { return in.ID == runID("hello-a") })
	}
	vols.present = present
	h.readyJob(t, func(s *v1.JobSpec) {
		s.Volumes = []v1.VolumeEntry{{Name: "scratch", Type: v1.VolumeEmptyDir, SizeBytes: 1 << 20}}
	})
	h.run(t, "hello-a", `{}`, nil)
	h.serveRun(t, "hello-a", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) })
	h.pass(t, "hello-a")
	<-idx.hit
	require.True(t, present(), "the old run's worker is removed last")
	require.NoError(t, h.st.Delete(context.Background(), v1.KindJobRun.GVK(), "default", "hello-a", ""))
	h.run(t, "hello-a", `{}`, nil)
	h.pass(t, "hello-a")
	time.Sleep(300 * time.Millisecond) // the new run's executor gets as far as the old worker lets it
	const ref = "default/hello/hello-a"
	require.Equal(t, []string{"bind " + ref, "remove " + ref}, socks.log(), "no bind while the old worker exists")
	idx.free()
	require.Eventually(t, func() bool { return len(socks.log()) >= 3 }, 5*time.Second, 10*time.Millisecond,
		"the new run binds once the old worker is gone")
	require.Equal(t, "bind "+ref, socks.log()[2])
	vols.mu.Lock()
	defer vols.mu.Unlock()
	require.NotEmpty(t, vols.at)
	require.True(t, vols.at[0], "the old run's emptyDir goes while its worker still exists")
}

// ADR-0347 Decision 6 and run.fizz EndIsFinal: a cancel stops the call and the worker of a Running run, which ends
// Cancelled; the handler's late answer changes nothing.
func TestJobRunExecutorCancelStopsTheWorker(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10)
	h.readyJob(t, nil)
	h.run(t, "hello-a", `{}`, nil)
	in, cut := make(chan struct{}), make(chan struct{})
	h.serveRun(t, "hello-a", func(w http.ResponseWriter, r *http.Request) {
		close(in)
		awaitHangUp(r)
		close(cut)
		_, _ = io.WriteString(w, `{"late":true}`)
	})
	h.pass(t, "hello-a")
	<-in
	run := h.pass(t, "hello-a")
	run.Spec.Cancel = true
	_, err := h.st.Update(context.Background(), run)
	require.NoError(t, err)
	h.pass(t, "hello-a")
	ended := h.end(t, "hello-a")
	requireEnd(t, ended, v1.JobRunCancelled, v1.JobRunCancelledReason)
	require.Empty(t, ended.Status.Result)
	<-cut
	require.True(t, h.rt.wasRemoved(runID("hello-a")))
}

// ADR-0347 Decision 6: no worker of a run is created while a worker of its name remains, such as a deleted run's that
// is still stopping.
func TestJobRunExecutorWaitsForItsName(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10)
	h.readyJob(t, nil)
	stale := runtime.NewInstanceID("default", "hello-a", "hello-v0", 0)
	_, err := h.rt.Create(context.Background(), runtime.WorkerSpec{Namespace: "default", Name: "hello-a", OwnerKind: v1.KindJobRun, Revision: "hello-v0"})
	require.NoError(t, err)
	require.NoError(t, h.rt.Start(context.Background(), stale))
	h.run(t, "hello-a", `{}`, nil)
	h.pass(t, "hello-a")
	time.Sleep(200 * time.Millisecond)
	require.Empty(t, h.createdFor("hello-a", "hello-v1"), "no worker while one of its name remains")

	require.NoError(t, h.rt.Stop(context.Background(), stale))
	require.NoError(t, h.rt.Remove(context.Background(), stale))
	requireEnd(t, h.end(t, "hello-a"), v1.JobRunSucceeded, "")
	require.Len(t, h.createdFor("hello-a", "hello-v1"), 1)
}

// ADR-0347 Decision 6: a worker of the run's name that is still there runtime.bootTimeout after the run started ends it
// StartFailed, the bound its own worker's start has, and no worker of it is created.
func TestJobRunExecutorGivesUpOnItsName(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10, func(d *function.Deps) { d.BootTimeout = 300 * time.Millisecond })
	h.readyJob(t, nil)
	stale := runtime.NewInstanceID("default", "hello-a", "hello-v0", 0)
	_, err := h.rt.Create(context.Background(), runtime.WorkerSpec{Namespace: "default", Name: "hello-a", OwnerKind: v1.KindJobRun, Revision: "hello-v0"})
	require.NoError(t, err)
	require.NoError(t, h.rt.Start(context.Background(), stale))
	h.run(t, "hello-a", `{}`, nil)
	h.pass(t, "hello-a")
	c := requireEnd(t, h.end(t, "hello-a"), v1.JobRunFailed, v1.JobRunStartFailed)
	require.Contains(t, c.Message, "remains after runtime.bootTimeout 300ms")
	require.Empty(t, h.createdFor("hello-a", "hello-v1"))
}

// ADR-0347 Decision 5: the contract plugins read a run's call by its pinned revision, which names its Job in spec.job:
// the revision's delivered contract, never a same-named Function's.
func TestContractOfAJobRevision(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(h.artifact), "__funcd_contract.json"),
		[]byte(`{"input":{"type":"object","required":["name"]},"output":{}}`), 0o600))
	job := h.readyJob(t, nil)
	pair, err := h.r.ContractOf(context.Background(), activator.FunctionRef{Namespace: "default", Name: "hello", Revision: "hello-v1", UID: job.UID}, nil)
	require.NoError(t, err)
	require.NotNil(t, pair)
	details, err := pair.Input.Validate([]byte(`{}`))
	require.NoError(t, err)
	require.NotEmpty(t, details, "the input is checked against the Job revision's contract")
	none, err := h.r.ContractOf(context.Background(), activator.FunctionRef{Namespace: "default", Name: "other", Revision: "hello-v1"}, nil)
	require.NoError(t, err)
	require.Nil(t, none, "a revision of another Job serves no call to this one")

	gets := &countingStore{Store: h.st}
	cached := function.ReconcilerWithStore(t, h.deps, gets)
	ref := activator.FunctionRef{Namespace: "default", Name: "hello", Revision: "hello-v1", UID: job.UID}
	for range 3 {
		_, err := cached.ContractOf(context.Background(), ref, nil)
		require.NoError(t, err)
	}
	require.Equal(t, 1, gets.revisions(), "a stamped revision is read once, not on each call")
}

// countingStore counts the Revision reads it serves.
type countingStore struct {
	store.Store
	mu  sync.Mutex
	got int
}

func (s *countingStore) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindRevision {
		s.mu.Lock()
		s.got++
		s.mu.Unlock()
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

func (s *countingStore) revisions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got
}

// ADR-0348 Decision 2: a run of a Job without approval.bypass first calls a worker of its pin with the event type
// funcd.job.preview; a 2xx JSON answer within job.payloadLimit is its preview, digest sha256: of the answer's bytes and
// an empty answer the document null, and its worker is removed; an approval then calls a second worker of the same
// revision, in the same trace, with funcd.job.run.
func TestJobRunExecutorPreviewsThenApplies(t *testing.T) {
	t.Parallel()
	h := newExecRig(t, 1<<10)
	h.readyJob(t, func(s *v1.JobSpec) { s.Approval.Bypass = false })
	run := h.run(t, "hello-a", `{"name":"Ada"}`, nil)
	worker := &answering{status: http.StatusOK, body: `{"summary":"greet Ada"}`}
	h.serveRun(t, "hello-a", worker.ServeHTTP)
	previewing := h.pass(t, "hello-a")
	require.Equal(t, v1.JobRunPreviewing, previewing.Status.Phase)
	awaiting := h.end(t, "hello-a")
	require.Equal(t, v1.JobRunAwaitingApproval, awaiting.Status.Phase)
	sum := sha256.Sum256([]byte(`{"summary":"greet Ada"}`))
	require.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), awaiting.Status.Preview.Digest)
	require.JSONEq(t, `{"summary":"greet Ada"}`, string(awaiting.Status.Preview.Document))
	require.True(t, h.rt.wasRemoved(runID("hello-a")), "the preview's worker stops")
	calls := worker.calls()
	require.Len(t, calls, 1)
	require.JSONEq(t, `{"specversion":"1.0","type":"funcd.job.preview","source":"funcd://default/job/hello","id":"`+
		string(run.UID)+`","data":{"name":"Ada"}}`, string(calls[0].body))

	worker.body = `{"greeting":"hi Ada"}`
	ctx := context.Background()
	obj, err := h.st.Get(ctx, v1.KindJobRun.GVK(), "default", "hello-a")
	require.NoError(t, err)
	approved := obj.(*v1.JobRun)
	approved.Spec.Approve, approved.Status.ApprovedBy = awaiting.Status.Preview.Digest, "bob"
	approved.Status.ApprovedAt = awaiting.Status.Preview.PreviewedAt
	_, err = h.st.Update(ctx, approved)
	require.NoError(t, err)
	require.Equal(t, v1.JobRunRunning, h.pass(t, "hello-a").Status.Phase)
	ended := h.end(t, "hello-a")
	requireEnd(t, ended, v1.JobRunSucceeded, "")
	require.JSONEq(t, `{"greeting":"hi Ada"}`, string(ended.Status.Result))
	calls = worker.calls()
	require.Len(t, calls, 2, "one preview call, one apply call")
	require.Contains(t, string(calls[1].body), `"type":"funcd.job.run"`)
	for _, c := range calls {
		require.True(t, strings.HasPrefix(c.header.Get("traceparent"), "00-"+previewing.Status.TraceID+"-"), "one trace")
	}
	require.Len(t, h.createdFor("hello-a", "hello-v1"), 2, "a worker for the preview and one for the apply, of one revision")
}

// ADR-0348 Decision 2: a preview that answers an error ends the run Failed with PreviewFailed, a larger one with
// PayloadLimitExceeded, a 2xx that is no JSON PreviewFailed; an empty answer is the document null.
func TestJobRunExecutorPreviewAnswers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status       int
		body         string
		phase        v1.JobRunPhase
		reason, says string
		document     string
	}{
		"ok":       {status: http.StatusOK, body: `[1,2]`, phase: v1.JobRunAwaitingApproval, document: `[1,2]`},
		"empty":    {status: http.StatusNoContent, phase: v1.JobRunAwaitingApproval, document: `null`},
		"over":     {status: http.StatusOK, body: `"` + strings.Repeat("x", 40) + `"`, phase: v1.JobRunFailed, reason: v1.JobRunPayloadLimitExceeded, says: "the preview exceeds job.payloadLimit 32"},
		"not-json": {status: http.StatusOK, body: `hi`, phase: v1.JobRunFailed, reason: v1.JobRunPreviewFailed, says: "the answer is not JSON"},
		"thrown":   {status: http.StatusInternalServerError, body: `{"error":"boom\nat preview"}`, phase: v1.JobRunFailed, reason: v1.JobRunPreviewFailed, says: "the preview answered 500: boom"},
		"refused":  {status: http.StatusUnprocessableEntity, body: `{"error":"event data does not match the input contract"}`, phase: v1.JobRunFailed, reason: v1.JobRunPreviewFailed, says: "422"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newExecRig(t, 32)
			h.readyJob(t, func(s *v1.JobSpec) { s.Approval.Bypass = false })
			h.run(t, "hello-a", `{}`, nil)
			h.serveRun(t, "hello-a", (&answering{status: tc.status, body: tc.body}).ServeHTTP)
			require.Equal(t, v1.JobRunPreviewing, h.pass(t, "hello-a").Status.Phase)
			ended := h.end(t, "hello-a")
			require.Equal(t, tc.phase, ended.Status.Phase)
			if tc.document != "" {
				require.JSONEq(t, tc.document, string(ended.Status.Preview.Document))
				return
			}
			c := requireEnd(t, ended, tc.phase, tc.reason)
			require.Contains(t, c.Message, tc.says)
			require.NotContains(t, c.Message, "\n", "a message is one line")
			require.Nil(t, ended.Status.Preview)
			require.True(t, h.rt.wasRemoved(runID("hello-a")))
		})
	}
}
