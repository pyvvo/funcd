package oidc

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
)

// ADR-0335 Decision 10: a Namespace whose edgeDefaults.auth names no OIDCProvider shows ProviderNotFound until the
// provider exists, and the provider's events reach the Namespace check.
func TestNamespaceShowsAMissingProvider(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	n := h.nsObject()
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthOIDC, Provider: sso}}
	h.update(n)
	check := NewNamespaceCheck(h.st, nil)
	req := controller.Request{GVK: v1.KindNamespace.GVK(), Name: v1.ObjectName(staging)}
	_, err := check.Reconcile(h.ctx, req)
	require.NoError(t, err)
	c, ok := h.nsObject().Status.Conditions.Get(CondEdgeDefaultsReady)
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, router.ReasonProviderNotFound, c.Reason)

	p := h.withClient(nil)
	require.Equal(t, []controller.Request{req}, MapProviderNamespace(h.ctx, p))
	require.Equal(t, []controller.Request{{GVK: v1.KindOIDCProvider.GVK(), Namespace: staging, Name: sso}}, MapProviders(h.st)(h.ctx, h.nsObject()))
	_, err = check.Reconcile(h.ctx, req)
	require.NoError(t, err)
	_, ok = h.nsObject().Status.Conditions.Get(CondEdgeDefaultsReady)
	require.False(t, ok, "the provider exists")
}
