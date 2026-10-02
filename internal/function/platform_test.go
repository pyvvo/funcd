package function_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/scheduler"
)

// ADR-0145 tests. The harness's single-node scheduler runs on the host platform; an artifact that provides only
// "plan9/mips" fits no node.

const (
	digestHere      = "sha256:here"
	digestElsewhere = "sha256:elsewhere"
	digestOutage    = "sha256:outage"
)

// fakePlatforms is a function.PlatformResolver keyed by digest.
type fakePlatforms struct {
	mu    sync.Mutex
	calls int
}

func (f *fakePlatforms) Platforms(_ context.Context, _, digest string) ([]v1.OCIPlatform, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	switch digest {
	case digestHere:
		return []v1.OCIPlatform{"plan9/mips", v1.HostPlatform()}, nil
	case digestElsewhere:
		return []v1.OCIPlatform{"plan9/mips"}, nil
	case digestOutage:
		return nil, fault.Unavailablef("fake.Platforms", "registry unreachable")
	}
	return nil, nil
}

func withPlatforms(p *fakePlatforms) func(*function.Deps) {
	return func(d *function.Deps) { d.Platforms = p }
}

// scenario: function-no-matching-platform (ADR-0145) — a new Function whose artifact provides no platform the node
// runs is Failed with NoMatchingPlatform, naming both sides, and no worker is created.
func TestScenarioFunctionNoMatchingPlatform(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestElsewhere })
	h.reconcile(t, "reader")

	fn := h.getFn(t, "reader")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	ready := h.condition(t, "reader", "Ready")
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "NoMatchingPlatform", ready.Reason)
	require.Equal(t, "artifact provides [plan9/mips]; node local runs "+string(v1.HostPlatform()), ready.Message)
	creates, _ := h.rt.counts()
	require.Zero(t, creates, "no worker is created")
}

// An index that provides the node's platform among others reconciles Ready.
func TestIndexWithTheNodePlatformIsReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)
}

// scenario: redeploy-no-matching-platform-keeps-serving (ADR-0145) — a redeploy to an artifact no node can run leaves
// the serving revision serving: Ready stays True, RevisionReady is False with the reason.
func TestScenarioRedeployNoMatchingPlatformKeepsServing(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)
	creates, _ := h.rt.counts()

	h.apply(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestElsewhere })
	h.reconcile(t, "reader")

	fn := h.getFn(t, "reader")
	require.Equal(t, "reader-1", fn.Status.ServingRevision, "the old revision keeps the calls")
	require.Equal(t, v1.ConditionTrue, h.condition(t, "reader", "Ready").Status)
	rr := h.condition(t, "reader", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "NoMatchingPlatform", rr.Reason)
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "no worker of the new revision is created")
}

// A resolver error (a registry outage) is a reconcile error, retried with backoff — never a status.
func TestPlatformResolverErrorRequeues(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestOutage })
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "reader"})
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	_, ok := h.getFn(t, "reader").Status.Conditions.Get("Ready")
	require.False(t, ok, "no status is written for an outage")
}

// scenario: pooled-member-no-matching-platform (ADR-0145) — of two pooled Functions sharing a pool key, the one whose
// artifact no node can run is Failed with NoMatchingPlatform, and the pool serves the other; with the pool at its cap
// of one, the mismatched member is not counted, so the healthy member is admitted.
func TestScenarioPooledMemberNoMatchingPlatform(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 1} {
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}), func(d *function.Deps) {
			d.PoolShimCommand = []string{"node", "/opt/funcd/pool.mjs"}
			d.PoolLimit = limit
		})
		// "a-elsewhere" sorts first, so at a cap of one it would take the only slot if it were counted.
		h.create(t, "a-elsewhere", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1"; fn.Spec.ImageDigest = digestElsewhere })
		h.create(t, "b-here", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1"; fn.Spec.ImageDigest = digestHere })
		h.reconcile(t, "a-elsewhere")
		h.reconcile(t, "b-here")

		require.Equal(t, "NoMatchingPlatform", h.condition(t, "a-elsewhere", "Ready").Reason, "limit %d", limit)
		require.Equal(t, v1.PhaseReady, h.getFn(t, "b-here").Status.Phase, "limit %d: the pool serves the healthy member", limit)
		_, poolFull := h.getFn(t, "b-here").Status.Conditions.Get("PoolFull")
		if poolFull {
			require.NotEqual(t, v1.ConditionTrue, h.condition(t, "b-here", "PoolFull").Status, "limit %d", limit)
		}
	}
}

// recordingScheduler records every placement request the reconciler makes.
type recordingScheduler struct {
	scheduler.Scheduler
	mu   sync.Mutex
	reqs []scheduler.Request
}

func (s *recordingScheduler) Schedule(ctx context.Context, req scheduler.Request) (scheduler.Placement, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	return s.Scheduler.Schedule(ctx, req)
}

// Every solo replica's Schedule call carries the artifact's platforms (ADR-0145 Decision 5), not only the gate's.
func TestSoloScheduleCallsCarryPlatforms(t *testing.T) {
	t.Parallel()
	rec := &recordingScheduler{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}), func(d *function.Deps) {
		rec.Scheduler = d.Scheduler
		d.Scheduler = rec
	})
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere; fn.Spec.Replicas = 2 })
	h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.reqs), 3, "the gate plus one call per replica")
	for _, req := range rec.reqs {
		require.Equal(t, []v1.OCIPlatform{"plan9/mips", v1.HostPlatform()}, req.Platforms, "replica %d", req.Replica)
	}
}
