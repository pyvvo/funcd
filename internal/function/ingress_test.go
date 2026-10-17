package function_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/route/owned"
)

func (h *harness) setIngress(t *testing.T, name string, ing *v1.Ingress) {
	t.Helper()
	fn := h.getFn(t, name)
	fn.Spec.Ingress = ing
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
}

func (h *harness) ingressReady(t *testing.T, name string) (v1.Condition, bool) {
	t.Helper()
	return h.getFn(t, name).Status.Conditions.Get("IngressReady")
}

// setRouteReady writes the Ready condition the Route reconciler would on Route name.
func (h *harness) setRouteReady(t *testing.T, name v1.ObjectName, status v1.ConditionStatus, reason string) {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindRoute.GVK(), "default", name)
	require.NoError(t, err)
	rt := obj.(*v1.Route)
	rt.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: status, Reason: reason, ObservedGeneration: rt.Generation})
	_, err = h.st.Update(context.Background(), rt)
	require.NoError(t, err)
}

// ADR-0241 Decision 5: a Function with an address of its own writes its Route and reports it as IngressReady; a
// Disabled Route is off by choice; a foreign Route of that name is RouteNotOwned; dropping the address deletes it.
func TestFunctionIngress_OwnedRouteOutcomes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "api", 0, false)
	h.setIngress(t, "api", &v1.Ingress{Host: "api.example.com", Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}})
	h.reconcile(t, "api")

	name := owned.FunctionRouteName("api")
	obj, err := h.st.Get(context.Background(), v1.KindRoute.GVK(), "default", name)
	require.NoError(t, err)
	rt := obj.(*v1.Route)
	require.True(t, v1.ControlledBy(rt.OwnerReferences, v1.KindFunction, h.getFn(t, "api").UID))
	require.Equal(t, "api.example.com", rt.Spec.Host)
	require.Equal(t, "/", rt.Spec.Rules[0].Path)
	require.Equal(t, v1.AuthAuthenticated, rt.Spec.Auth.Mode)
	c, ok := h.ingressReady(t, "api")
	require.True(t, ok)
	require.Equal(t, "RouteNotReady", c.Reason, "the Route has no verdict yet")

	for _, tc := range []struct {
		status v1.ConditionStatus
		reason string
		want   v1.ConditionStatus
		got    string
	}{
		{v1.ConditionTrue, "Programmed", v1.ConditionTrue, "Programmed"},
		{v1.ConditionFalse, "Disabled", v1.ConditionTrue, "Disabled"},
		{v1.ConditionFalse, "RouteConflict", v1.ConditionFalse, "RouteNotReady"},
	} {
		h.setRouteReady(t, name, tc.status, tc.reason)
		h.reconcile(t, "api")
		c, _ = h.ingressReady(t, "api")
		require.Equal(t, tc.want, c.Status, tc.reason)
		require.Equal(t, tc.got, c.Reason, tc.reason)
	}

	h.setIngress(t, "api", nil)
	h.reconcile(t, "api")
	_, err = h.st.Get(context.Background(), v1.KindRoute.GVK(), "default", name)
	require.Error(t, err, "dropping the address deletes the Route")
	_, ok = h.ingressReady(t, "api")
	require.False(t, ok)
}

func TestFunctionIngress_ForeignRouteIsNotOwned(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	foreign := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	foreign.Name, foreign.Namespace, foreign.ResourceGroup = owned.FunctionRouteName("api"), "default", "rg1"
	foreign.Spec = v1.RouteSpec{Rules: []v1.RouteRule{{Path: "/yours", Backend: v1.RouteBackend{Function: "api"}}}}
	_, err := h.st.Create(context.Background(), foreign)
	require.NoError(t, err)
	h.createFn(t, "api", 0, false)
	h.setIngress(t, "api", &v1.Ingress{Path: "/api"})
	h.reconcile(t, "api")

	c, ok := h.ingressReady(t, "api")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "RouteNotOwned", c.Reason)
	obj, err := h.st.Get(context.Background(), v1.KindRoute.GVK(), "default", foreign.Name)
	require.NoError(t, err)
	require.Equal(t, "/yours", obj.(*v1.Route).Spec.Rules[0].Path, "a foreign Route is left untouched")
}
