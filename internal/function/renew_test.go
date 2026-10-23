package function_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
)

const keyPeriod = 24 * time.Hour

// createdOf is when the worker id was last created.
func (f *fakeRuntime) createdOf(id runtime.InstanceID) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created[id]
}

// rotatingKeys is an S3 injection whose pairs name the window *w, with a 24h period and a renewal age of 20h.
func rotatingKeys(w *int) function.S3GatewayInjection {
	return function.S3GatewayInjection{
		Enabled: true, ListenAddr: "127.0.0.1:9000", Period: keyPeriod,
		Derive: func(_ v1.Kind, _, name string) (string, string) {
			return "AK-" + name + "-" + strconv.Itoa(*w), "SK-" + name + "-" + strconv.Itoa(*w)
		},
		RenewalAge: func(v1.Kind, string, string) time.Duration { return 20 * time.Hour },
	}
}

func withBlob(fn *v1.Function) {
	fn.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "raw", Bucket: "k", Prefix: "raw"}}
}

// scenario: workers-renewed (ADR-0270) — a busy solo worker is kept past half the period; at its renewal age it gets
// no further call, a call in flight finishes on it, and once idle it is renewed with the current window's pair.
func TestScenarioWorkerRenewed(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	calls := activator.NewCallTracker(nil)
	w := 0
	logs := &syncBuffer{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.Calls, d.S3Gateway, d.DrainGrace = clk, calls, rotatingKeys(&w), time.Minute
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "s").Status.Phase)
	creates, _ := h.rt.counts()

	w = 1
	id := runtime.NewInstanceID("default", "s", "s-1", 0)
	url, release := h.rt.serveWorker(t, id, "busy")
	answer := callInFlight(t, calls, url+"/function/s")
	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "s")
	now, _ := h.rt.counts()
	require.Equal(t, creates, now, "a busy worker past half the period is kept")

	clk.Advance(20*time.Hour - keyPeriod/2)
	h.reconcile(t, "s")
	now, _ = h.rt.counts()
	require.Equal(t, creates, now, "at its renewal age a busy worker drains first")
	_, ready := h.upstream(t, "s")
	require.False(t, ready, "a draining worker gets no new call")
	require.Equal(t, runtime.StateRunning, h.rt.stateOf(id), "a draining worker is not stopped as a boot crash")
	clk.Advance(30 * time.Second)
	h.reconcile(t, "s")
	require.Equal(t, runtime.StateRunning, h.rt.stateOf(id), "it keeps draining within DrainGrace")
	rc, _ := h.getFn(t, "s").Status.Conditions.Get("Ready")
	require.Empty(t, rc.Reason, "no boot crash is recorded")

	release()
	require.Equal(t, "busy", answer(), "the call in flight finishes on the old worker")
	settle()
	h.reconcile(t, "s")
	now, _ = h.rt.counts()
	require.Equal(t, creates+1, now, "once idle the drained worker is renewed")
	spec, ok := h.rt.specFor("s")
	require.True(t, ok)
	require.Equal(t, "AK-s-1", spec.Env["AWS_ACCESS_KEY_ID"], "the new worker carries the current window's pair")
	out := logs.String()
	require.Equal(t, 1, strings.Count(out, `msg="a worker drains for a fresh S3 key pair"`), out)
	require.Equal(t, 1, strings.Count(out, `msg="renewing a worker for a fresh S3 key pair"`), out)
	require.Contains(t, out, "how=drained")
	require.Contains(t, out, "instance=default/s/s-1/r0")
}

// A worker still busy DrainGrace after its renewal age is renewed then (ADR-0270 Decision 6).
func TestScenarioBusyWorkerRenewedAfterDrainGrace(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	calls := activator.NewCallTracker(nil)
	w := 0
	logs := &syncBuffer{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.Calls, d.S3Gateway, d.DrainGrace = clk, calls, rotatingKeys(&w), time.Minute
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	creates, _ := h.rt.counts()
	url, release := h.rt.serveWorker(t, runtime.NewInstanceID("default", "s", "s-1", 0), "busy")
	t.Cleanup(release)
	_ = callInFlight(t, calls, url+"/function/s")
	clk.Advance(20 * time.Hour)
	h.reconcile(t, "s")
	now, _ := h.rt.counts()
	require.Equal(t, creates, now, "draining")
	clk.Advance(time.Minute)
	h.reconcile(t, "s")
	now, _ = h.rt.counts()
	require.Equal(t, creates+1, now, "DrainGrace into the drain the worker is renewed")
	require.Contains(t, logs.String(), "how=forced")
}

// scenario: workers-renewed (ADR-0270 Decision 6) — replicas renew one at a time: while the first replacement boots, the
// other replica is not renewed, however many passes run, and the Function stays Ready; once the replacement listens,
// the other renews.
func TestScenarioReplicasRenewRolling(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Clock, d.S3Gateway = clk, rotatingKeys(&w) })
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", func(fn *v1.Function) {
		withBlob(fn)
		fn.Spec.Replicas = 2
	})
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	creates, _ := h.rt.counts()
	h.rt.setHoldNew(true)
	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "s")
	now, _ := h.rt.counts()
	require.Equal(t, creates+1, now, "one replica renews")
	for range 3 {
		settle()
		h.reconcile(t, "s")
		now, _ = h.rt.counts()
		require.Equal(t, creates+1, now, "the other waits while the replacement boots")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "s").Status.Phase, "the listening replica keeps the Function Ready")
	}
	h.rt.setHoldNew(false)
	for i := range 2 {
		h.rt.hold(runtime.NewInstanceID("default", "s", "s-1", i), false)
	}
	h.reconcile(t, "s")
	now, _ = h.rt.counts()
	require.Equal(t, creates+2, now, "once the replacement listens the other renews")
}

// A lone replica renewed for its key pair reads Degraded with reason Renewing while its replacement boots, not as an
// exit, and Ready again once it listens (ADR-0270).
func TestScenarioRenewalStatusSolo(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Clock, d.S3Gateway = clk, rotatingKeys(&w) })
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	h.rt.setHoldNew(true)
	clk.Advance(keyPeriod/2 + time.Minute)
	for range 2 {
		h.reconcile(t, "s")
		fn := h.getFn(t, "s")
		require.Equal(t, v1.PhaseDegraded, fn.Status.Phase)
		c, _ := fn.Status.Conditions.Get("Ready")
		require.Equal(t, "Renewing", c.Reason, "a planned renewal is not an exit")
		settle()
	}
	h.rt.setHoldNew(false)
	h.rt.hold(runtime.NewInstanceID("default", "s", "s-1", 0), false)
	h.reconcile(t, "s")
	fn := h.getFn(t, "s")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	c, _ := fn.Status.Conditions.Get("Ready")
	require.Empty(t, c.Reason)
}

// A pool worker restarted for its members' key pairs leaves each member Degraded with reason Renewing while it boots
// (ADR-0270).
func TestScenarioRenewalStatusPool(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	dir := filepath.Join(t.TempDir(), "pool")
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.Clock, d.PoolManifestDir, d.S3Gateway = clk, dir, rotatingKeys(&w)
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) {
			fn.Spec.Pooling.Worker = "s3"
			withBlob(fn)
		})
	}
	h.pools(t, "a", "b")
	key, ok := pooling.ParsePool("default", h.getFn(t, "a").Status.Pool)
	require.True(t, ok)
	workers := h.rt.poolWorkers("default", v1.ObjectName("__pool__nodejs22__s3__"+key.AccessHash))
	require.Len(t, workers, 1)
	h.rt.setHoldNew(true)
	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	for _, n := range []string{"a", "b"} {
		fn := h.getFn(t, n)
		require.Equal(t, v1.PhaseDegraded, fn.Status.Phase, n)
		c, _ := fn.Status.Conditions.Get("Ready")
		require.Equal(t, "Renewing", c.Reason, n)
	}
	h.rt.setHoldNew(false)
	h.rt.hold(workers[0], false)
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)
}

// scenario: workers-renewed (ADR-0270) — the worker of a revision an open workflow run holds renews its key pair too.
func TestScenarioHeldRevisionWorkerRenewed(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	logs := &syncBuffer{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.S3Gateway = clk, rotatingKeys(&w)
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.create(t, "echo", withBlob)
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
	holdRun(t, h.st, "r1", h.getFn(t, "echo"), "echo-1")
	h.rt.serveRevision(t, "echo-2", http.StatusOK)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-2", fn.Status.ServingRevision)
	ref := activator.FunctionRef{Namespace: "default", Name: "echo", Revision: "echo-1", UID: fn.UID}
	require.NoError(t, storescaler.New(h.st).ScaleTo(context.Background(), ref, 0))
	h.revisionEvent(t, "echo-1")
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseReady, h.wakeHeld(t, ref).Status.Phase)
	held := runtime.NewInstanceID("default", "echo", "echo-1", 0)
	before := h.rt.createdOf(held)

	w = 1
	h.rt.setHoldNew(true)
	clk.Advance(keyPeriod/2 + time.Minute)
	h.revisionEvent(t, "echo-1")
	h.reconcile(t, "echo")
	rev := h.revisionEvent(t, "echo-1")
	require.Equal(t, v1.PhaseDegraded, rev.Status.Phase, "its replacement boots")
	c, _ := rev.Status.Conditions.Get("Ready")
	require.Equal(t, "Renewing", c.Reason, "a planned renewal is not an exit")
	h.rt.setHoldNew(false)
	h.rt.hold(held, false)
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	require.True(t, h.rt.createdOf(held).After(before), "the held revision's worker is renewed")
	require.Equal(t, "AK-echo-1", h.rt.specOf(held).Env["AWS_ACCESS_KEY_ID"], "with the current window's pair")
	require.True(t, slices.ContainsFunc(strings.Split(logs.String(), "\n"), func(l string) bool {
		return strings.Contains(l, `msg="renewing a worker for a fresh S3 key pair"`) && strings.Contains(l, "instance=default/echo/echo-1/r0")
	}), "by its key renewal: %s", logs.String())
}

// An idle solo worker is renewed right after half the period (ADR-0270 Decision 6).
func TestScenarioIdleWorkerRenewedAtHalfThePeriod(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	logs := &syncBuffer{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.S3Gateway = clk, rotatingKeys(&w)
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	creates, _ := h.rt.counts()
	w = 1
	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "s")
	now, _ := h.rt.counts()
	require.Equal(t, creates+1, now)
	spec, ok := h.rt.specFor("s")
	require.True(t, ok)
	require.Equal(t, "AK-s-1", spec.Env["AWS_ACCESS_KEY_ID"])
	require.Contains(t, logs.String(), "how=idle")
	require.NotContains(t, logs.String(), "drains for a fresh S3 key pair", "an idle worker needs no drain")
}

// scenario: workers-renewed (ADR-0270 Decision 5) — a pool across a window boundary keeps its worker and signature
// until its own renewal age, then restarts with fresh pairs in its manifest.
func TestScenarioPoolKeepsItsWorkerAcrossAWindow(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	dir := filepath.Join(t.TempDir(), "pool")
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.Clock, d.PoolManifestDir, d.S3Gateway = clk, dir, rotatingKeys(&w)
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) {
			fn.Spec.Pooling.Worker = "s3"
			withBlob(fn)
		})
	}
	h.pools(t, "a", "b")
	key, ok := pooling.ParsePool("default", h.getFn(t, "a").Status.Pool)
	require.True(t, ok)
	pool := v1.ObjectName("__pool__nodejs22__s3__" + key.AccessHash)
	spec, ok := h.rt.specFor(pool)
	require.True(t, ok)
	sig := spec.Revision
	creates, _ := h.rt.counts()

	w = 1
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	now, _ := h.rt.counts()
	require.Equal(t, creates, now, "a new window rebuilds no pool")
	spec, _ = h.rt.specFor(pool)
	require.Equal(t, sig, spec.Revision, "the signature holds no pair")

	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "a")
	now, _ = h.rt.counts()
	require.Equal(t, creates+1, now, "past half the period the idle pool worker restarts")
	data, err := os.ReadFile(filepath.Join(dir, "default."+string(pool)+".json"))
	require.NoError(t, err)
	var rows []struct {
		Name string            `json:"name"`
		Env  map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal(data, &rows))
	for _, r := range rows {
		require.Equal(t, "AK-"+r.Name+"-1", r.Env["AWS_ACCESS_KEY_ID"], r.Name)
	}
}

// A pool worker at its renewal age drains like a solo one: a call in flight finishes on it, it gets no new call, and
// once idle it restarts with fresh pairs (ADR-0270 Decision 6).
func TestScenarioPoolWorkerDrainsForRenewal(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	calls := activator.NewCallTracker(nil)
	dir := filepath.Join(t.TempDir(), "pool")
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.Clock, d.Calls, d.PoolManifestDir, d.S3Gateway, d.DrainGrace = clk, calls, dir, rotatingKeys(&w), time.Minute
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) {
			fn.Spec.Pooling.Worker = "s3"
			withBlob(fn)
		})
	}
	h.pools(t, "a", "b")
	key, ok := pooling.ParsePool("default", h.getFn(t, "a").Status.Pool)
	require.True(t, ok)
	workers := h.rt.poolWorkers("default", v1.ObjectName("__pool__nodejs22__s3__"+key.AccessHash))
	require.Len(t, workers, 1)
	creates, _ := h.rt.counts()

	url, release := h.rt.serveWorker(t, workers[0], "busy")
	answer := callInFlight(t, calls, url+"/function/a")
	w = 1
	clk.Advance(20 * time.Hour)
	h.reconcile(t, "a")
	now, _ := h.rt.counts()
	require.Equal(t, creates, now, "a busy pool worker drains first")
	_, ready := h.upstream(t, "a")
	require.False(t, ready, "a draining pool worker gets no new call")

	release()
	require.Equal(t, "busy", answer())
	settle()
	h.reconcile(t, "a")
	now, _ = h.rt.counts()
	require.Equal(t, creates+1, now, "once idle the pool worker restarts with fresh pairs")
}

// A worker that crashes while it drains is created again once, and its renewal mark does not retire the new worker
// of the same id (ADR-0270).
func TestScenarioStaleRenewalMarkSparesTheNextWorker(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	calls := activator.NewCallTracker(nil)
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.Calls, d.S3Gateway, d.DrainGrace = clk, calls, rotatingKeys(&w), time.Minute
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	creates, _ := h.rt.counts()
	url, release := h.rt.serveWorker(t, runtime.NewInstanceID("default", "s", "s-1", 0), "busy")
	answer := callInFlight(t, calls, url+"/function/s")
	clk.Advance(20 * time.Hour)
	h.reconcile(t, "s")
	release()
	_ = answer()
	h.rt.exit("s", runtime.StateFailed, time.Hour)
	for range 4 {
		settle()
		h.reconcile(t, "s")
	}
	now, _ := h.rt.counts()
	require.Equal(t, creates+1, now, "the crashed worker is created again once, and its successor is not retired")
	_, ready := h.upstream(t, "s")
	require.True(t, ready, "the successor is handed calls")
}

// scenario: workers-renewed (ADR-0270) — a replica that never comes back holds the other replica's early renewal, but
// not its renewal at its renewal age: the healthy replica renews then, so its pair never expires beside a broken one.
// oracle: docs/design/function/key-renewal.fizz#NoWorkerServesAnExpiredPair
func TestScenarioBrokenReplicaDoesNotBlockRenewal(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.S3Gateway, d.DrainGrace = clk, rotatingKeys(&w), time.Minute
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", func(fn *v1.Function) {
		withBlob(fn)
		fn.Spec.Replicas = 2
	})
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	healthy := runtime.NewInstanceID("default", "s", "s-1", 0)
	before := h.rt.createdOf(healthy)
	h.rt.setHoldNew(true)
	h.rt.exitRevision("s", "s-1", 1, runtime.StateFailed, time.Minute)
	for hour := 1; hour <= 21; hour++ {
		clk.Advance(time.Hour)
		for range 3 {
			settle()
			h.reconcile(t, "s")
		}
		if hour < 20 {
			require.Equal(t, before, h.rt.createdOf(healthy), "hour %d: the early renewal waits for the broken replica", hour)
		}
	}
	require.True(t, h.rt.createdOf(healthy).After(before), "at its renewal age the healthy replica renews")
}

// A renewal's replacement that crashes before it listens reads CrashLoopBackOff, not Renewing, through its re-creation
// (ADR-0270): a real failure wins over a planned renewal.
func TestScenarioRenewalCrashLoopReadsCrashLoop(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Clock, d.S3Gateway = clk, rotatingKeys(&w) })
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	h.rt.endStarts(replicaID("s", 1, 0), sigkill(), true)
	clk.Advance(keyPeriod/2 + time.Minute)
	for range 4 {
		h.reconcile(t, "s")
		c := h.condition(t, "s", "Ready")
		require.Equal(t, "CrashLoopBackOff", c.Reason, "the crash of the renewed replica's replacement")
		clk.Advance(time.Minute)
		settle()
	}
}

// A held revision's worker that drains for its renewal brings the Function's pass back after DrainPollInterval, not the
// supervision period (ADR-0270).
func TestScenarioHeldRevisionDrainPolls(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	calls := activator.NewCallTracker(nil)
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Clock, d.Calls, d.S3Gateway = clk, calls, rotatingKeys(&w)
		d.SupervisionPeriod, d.DrainPollInterval, d.DrainGrace = time.Minute, 200*time.Millisecond, 10*time.Minute
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.create(t, "echo", withBlob)
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	holdRun(t, h.st, "r1", h.getFn(t, "echo"), "echo-1")
	h.rt.serveRevision(t, "echo-2", http.StatusOK)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-2", fn.Status.ServingRevision)
	ref := activator.FunctionRef{Namespace: "default", Name: "echo", Revision: "echo-1", UID: fn.UID}
	require.NoError(t, storescaler.New(h.st).ScaleTo(context.Background(), ref, 0))
	h.revisionEvent(t, "echo-1")
	h.reconcile(t, "echo")
	clk.Advance(time.Hour)
	require.Equal(t, v1.PhaseReady, h.wakeHeld(t, ref).Status.Phase)
	clk.Advance(keyPeriod/2 - time.Hour + time.Minute)
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")

	held := runtime.NewInstanceID("default", "echo", "echo-1", 0)
	url, release := h.rt.serveWorker(t, held, "busy")
	t.Cleanup(release)
	_ = callInFlight(t, calls, url+"/function/echo")
	clk.Advance(9*time.Hour + time.Minute) // the held worker, woken an hour after the rest, is now past its 20 h renewal age
	res := h.reconcile(t, "echo")
	require.LessOrEqual(t, res.RequeueAfter, 200*time.Millisecond, "a draining held worker is polled back soon")
}

// A renewal's replacement whose Start fails reads the start failure, not Renewing (ADR-0270).
func TestScenarioRenewalStartFailureReadsIt(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	sf := &startFailer{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Clock, d.S3Gateway = clk, rotatingKeys(&w) }, sf.wrap)
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	sf.failing.Store(true)
	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "s")
	c := h.condition(t, "s", "Ready")
	require.NotEqual(t, "Renewing", c.Reason)
	require.Contains(t, c.Message, "could not start")
}

// A renewal's replacement that crashed once is recovered as a crash, not as a renewal: its re-creation, booting, does
// not read Renewing (ADR-0270).
func TestScenarioCrashAfterRenewalIsNotARenewal(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	w := 0
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Clock, d.S3Gateway = clk, rotatingKeys(&w) })
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	h.create(t, "s", withBlob)
	h.reconcile(t, "s")
	h.reconcile(t, "s")
	id := replicaID("s", 1, 0)
	h.rt.endStarts(id, sigkill(), true)
	clk.Advance(keyPeriod/2 + time.Minute)
	h.reconcile(t, "s")
	require.Equal(t, "CrashLoopBackOff", h.condition(t, "s", "Ready").Reason)
	h.rt.endStarts(id, runtime.Exit{}, false)
	h.rt.setHoldNew(true)
	for range 6 {
		clk.Advance(time.Minute)
		h.reconcile(t, "s")
		require.NotEqual(t, "Renewing", h.condition(t, "s", "Ready").Reason, "a crash's recovery is not a renewal")
	}
}

// scenario: workers-renewed (ADR-0270) — a replica at its renewal age waits while the other replica's renewal
// replacement boots, then renews once it listens; a replacement still booting holds it only until halfway to the end of
// its pair's validity (22 h of a 24 h period, at a 20 h renewal age), and one past the boot timeout, stopped as a boot
// crash, not at all.
func TestScenarioRenewalWaitsForABootingReplacement(t *testing.T) {
	t.Parallel()
	for _, listens := range []bool{true, false} {
		t.Run(fmt.Sprintf("listens=%v", listens), func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManual(time.Now())
			w := 0
			h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
				// a boot timeout past the test's hours keeps the held replacement a legitimate boot, not a boot crash
				d.Clock, d.S3Gateway, d.BootTimeout = clk, rotatingKeys(&w), 48*time.Hour
			})
			h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
			h.create(t, "s", func(fn *v1.Function) {
				withBlob(fn)
				fn.Spec.Replicas = 2
			})
			h.reconcile(t, "s")
			h.reconcile(t, "s")
			ids := []runtime.InstanceID{replicaID("s", 1, 0), replicaID("s", 1, 1)}
			start := []time.Time{h.rt.createdOf(ids[0]), h.rt.createdOf(ids[1])}
			h.rt.setHoldNew(true)
			clk.Advance(keyPeriod/2 + time.Minute)
			h.reconcile(t, "s")
			early, other := 0, 1
			if h.rt.createdOf(ids[1]).After(start[1]) {
				early, other = 1, 0
			}
			require.True(t, h.rt.createdOf(ids[early]).After(start[early]), "one replica renews early; its replacement boots")
			for _, at := range []time.Duration{20*time.Hour + time.Minute, 21 * time.Hour, 21*time.Hour + 59*time.Minute} {
				clk.Advance(at - clk.Now().Sub(start[other]))
				h.reconcile(t, "s")
				require.Equal(t, start[other], h.rt.createdOf(ids[other]), "at %v the other waits for the booting replacement", at)
			}
			if listens {
				h.rt.hold(ids[early], false)
				h.reconcile(t, "s")
				require.True(t, h.rt.createdOf(ids[other]).After(start[other]), "once the replacement listens the other renews")
				return
			}
			clk.Advance(2 * time.Minute)
			h.reconcile(t, "s")
			require.True(t, h.rt.createdOf(ids[other]).After(start[other]), "at 22 h it renews though the replacement still boots")
		})
	}
}
