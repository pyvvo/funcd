package function_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
)

// revisionEvent reads Revision name and hands it to the Function reconciler's Revision watch mapper, as the
// controller does on its store event.
func (h *shimHarness) revisionEvent(t *testing.T, name v1.ObjectName) *v1.Revision {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", name)
	require.NoError(t, err)
	rev := obj.(*v1.Revision)
	reqs := h.r.MapRevision(context.Background(), rev)
	require.Len(t, reqs, 1)
	require.Equal(t, v1.ObjectName("echo"), reqs[0].Name, "a Revision event queues the Function that controls it")
	return rev
}

// A held revision's state lives on its Revision (ADR-0190 Decisions 5 and 6): its idle reclaim retires its worker,
// a pinned wake boots it solo beside the serving revision without touching the Function's phase, its readiness is
// written to its Revision's status, and the run's end releases it.
func TestHeldRevisionWakesSoloAndIsReclaimed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	url1, _ := h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.deployReady(t, "echo")
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
	sc := storescaler.New(h.st)

	require.NoError(t, sc.ScaleTo(ctx, ref, 0))
	require.Equal(t, v1.PhaseIdle, h.revisionEvent(t, "echo-1").Status.Phase)
	h.reconcile(t, "echo")
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"), "an idle held revision costs no worker")
	_, ready, err := pinnedUpstream(h, fn, "echo-1")
	require.NoError(t, err)
	require.False(t, ready)

	require.NoError(t, sc.ScaleTo(ctx, ref, 1))
	require.Equal(t, v1.PhaseDeploying, h.revisionEvent(t, "echo-1").Status.Phase)
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0], "the woken held revision runs one solo worker")
	rev := h.revisionEvent(t, "echo-1")
	require.Equal(t, v1.PhaseReady, rev.Status.Phase)
	c, ok := rev.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, c.Status)
	up, ready, err := pinnedUpstream(h, fn, "echo-1")
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, url1, up, "the pinned call gets the woken revision")
	fn = h.getFn(t, "echo")
	require.Equal(t, "echo-2", fn.Status.ServingRevision, "the current revision keeps serving")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)

	cancelRun(t, h.st, "r1")
	h.reconcile(t, "echo")
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"), "the run's end retires the held revision")
	require.Equal(t, v1.PhaseIdle, h.revisionEvent(t, "echo-1").Status.Phase, "and writes it Idle")
}

// heldAtZero serves echo-2 while run r1 holds echo-1, reclaimed to zero, and returns r1's ref pinned to echo-1.
func heldAtZero(t *testing.T, h *shimHarness) activator.FunctionRef {
	t.Helper()
	h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.deployReady(t, "echo")
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
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"))
	return ref
}

// wakeHeld wakes ref's held revision as a pinned call does, runs the passes that boot it and returns its Revision.
func (h *shimHarness) wakeHeld(t *testing.T, ref activator.FunctionRef) *v1.Revision {
	t.Helper()
	require.NoError(t, storescaler.New(h.st).ScaleTo(context.Background(), ref, 1))
	h.revisionEvent(t, ref.Revision)
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	return h.revisionEvent(t, ref.Revision)
}

// runningOf counts echo's running workers of revision rev.
func (h *shimHarness) runningOf(rev v1.ObjectName) int {
	n := 0
	for _, s := range h.rt.revisionStates("echo")[rev] {
		if s == runtime.StateRunning {
			n++
		}
	}
	return n
}

// A held wake from an Idle Function boots only the held revision (ADR-0190 Decision 6): the current revision stays at
// zero and the Function stays Idle.
func TestHeldWakeLeavesIdleFunctionAtZero(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	ref := heldAtZero(t, h)
	require.NoError(t, storescaler.New(h.st).ScaleTo(context.Background(), activator.FunctionRef{Namespace: "default", Name: "echo"}, 0))
	h.reconcile(t, "echo")
	require.Zero(t, h.runningOf("echo-2"), "the Function is scaled to zero")

	require.Equal(t, v1.PhaseReady, h.wakeHeld(t, ref).Status.Phase)
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0], "the held revision runs solo")
	require.Zero(t, h.runningOf("echo-2"), "the current revision stays at zero")
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "echo").Status.Phase, "the held wake writes no Function phase")
}

// A gate that stops the current revision never strands a held wake (ADR-0190 Decisions 6 and 9): a gate on the current
// revision's artifact, shape or runtime leaves the held revision booting its own; a failed binding fails it with the
// binding's reason, and a binding that waits keeps it Deploying with the gate's reason.
func TestHeldWakeUnderCurrentRevisionGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		phase  v1.Phase
		reason string
		change func(*v1.Function)
	}{
		{"artifact", v1.PhaseReady, "", func(fn *v1.Function) { fn.Spec.Image = "oci://example/missing:v3" }},
		{"shape", v1.PhaseReady, "", func(fn *v1.Function) { fn.Spec.Handler = "" }},
		{"runtime", v1.PhaseReady, "", func(fn *v1.Function) { fn.Spec.Runtime = "ruby3" }},
		{"secret", v1.PhaseFailed, "SecretResolveFailed", func(fn *v1.Function) { fn.Spec.Secrets = []v1.ObjectName{"db"} }},
		{"config", v1.PhaseFailed, "ConfigResolveFailed", func(fn *v1.Function) { fn.Spec.Config = []v1.ObjectName{"missing"} }},
		{"data-reference", v1.PhaseDeploying, "KVStoreNotFound", func(fn *v1.Function) {
			fn.Spec.KV = []v1.FunctionKV{{Alias: "cache", Store: "nostore", Table: "t"}}
		}},
		{"catalog", v1.PhaseDeploying, "CatalogNotReady", func(fn *v1.Function) {
			fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "nolake"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
				d.Resolver = resolverFailing{bad: "oci://example/missing:v3"}
			})
			ref := heldAtZero(t, h)
			h.apply(t, "echo", tc.change)
			h.reconcile(t, "echo")

			rev := h.wakeHeld(t, ref)
			require.Equal(t, tc.phase, rev.Status.Phase)
			ready, ok := rev.Status.Conditions.Get("Ready")
			require.True(t, ok)
			if tc.reason == "" {
				require.Equal(t, v1.ConditionTrue, ready.Status)
				require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0], "the held revision boots its own artifact")
				return
			}
			require.Equal(t, tc.reason, ready.Reason, "the binding gate stops the held revision with its reason")
			require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"))
		})
	}
}

// downMaterializer fails every artifact of handler while down, as a registry outage does.
type downMaterializer struct {
	next    function.Materializer
	handler string
	down    atomic.Bool
}

func (m *downMaterializer) Materialize(ctx context.Context, fn *v1.Function) (string, error) {
	if m.down.Load() && fn.Spec.Handler == m.handler {
		return "", errors.New("registry down")
	}
	return m.next.Materialize(ctx, fn)
}

// A held revision's boot error is its own and is retried (ADR-0190 Decisions 5 and 6): it is written to the
// Revision's status, never to the Function's, the Function's pass goes on, and a later pass boots the revision once
// the cause clears.
func TestHeldWakeRetriesBootError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reason string
		fail   func(h *shimHarness, m *downMaterializer, down bool)
	}{
		{"materialize", "StartFailed", func(_ *shimHarness, m *downMaterializer, down bool) { m.down.Store(down) }},
		{"image", "RuntimeUnavailable", func(h *shimHarness, _ *downMaterializer, down bool) {
			spec, ok := h.rt.specFor("echo")
			require.True(t, ok)
			var err error
			if down {
				err = notEmbedded(spec.Image)
			}
			h.rt.failImage(spec.Image, err)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mat := &downMaterializer{handler: "handle"}
			h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
				mat.next = d.Materializer
				d.Materializer = mat
			})
			ref := heldAtZero(t, h)
			tc.fail(h, mat, true)

			require.NoError(t, storescaler.New(h.st).ScaleTo(context.Background(), ref, 1))
			h.revisionEvent(t, ref.Revision)
			h.reconcile(t, "echo")
			rev := h.revisionEvent(t, ref.Revision)
			require.Equal(t, v1.PhaseDeploying, rev.Status.Phase)
			ready, ok := rev.Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, tc.reason, ready.Reason, "the boot error is written to the held revision")
			h.reconcile(t, "echo")
			fn := h.getFn(t, "echo")
			require.Equal(t, v1.PhaseReady, fn.Status.Phase)
			cur, ok := fn.Status.Conditions.Get("RevisionReady")
			require.True(t, ok)
			require.Equal(t, v1.ConditionTrue, cur.Status, "and not to the Function's status")

			tc.fail(h, mat, false)
			h.reconcile(t, "echo")
			settle()
			h.reconcile(t, "echo")
			obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", ref.Revision)
			require.NoError(t, err)
			require.Equal(t, v1.PhaseReady, obj.(*v1.Revision).Status.Phase, "a later pass boots the held revision, with no Revision event")
			require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0])
		})
	}
}
