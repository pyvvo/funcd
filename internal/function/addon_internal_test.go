package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
)

// ADR-0338 Decision 3: a Function that binds an add-on object gets, per alias, the add-on proxy's base URL for it,
// shared, and its own token there, never in the shared env, which a pool's members share.
func TestAddonBindingGivesTheProxyEnv(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	r.addonProxy = AddonProxy{Address: func() (string, error) { return "10.63.0.1:40123", nil }, Token: func(ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName) (string, error) {
		return "token-of-" + string(kind) + "-" + string(ns) + "-" + string(name), nil
	}}
	fn := sampleFn()
	fn.Name = "rauthy-settings"
	fn.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "idp", Object: "rauthy-login"}}
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "rauthy-login", fn.Namespace, "rg1", "IdentityProvider"
	_, err := r.store.Create(context.Background(), o)
	require.NoError(t, err)

	shared, endpoints, gate, err := addonHook{r}.check(context.Background(), functionWorkload(fn))
	require.NoError(t, err)
	require.Nil(t, gate)
	require.Equal(t, map[string]string{"FUNCD_ADDON_IDP_URL": "http://10.63.0.1:40123/addon/idp"}, shared)
	require.Equal(t, []string{"FUNCD_ADDON_IDP_URL"}, endpoints, "the hook marks its endpoint key (ADR-0346 Decision 5)")
	own, issue, g := addonHook{r}.own(functionWorkload(fn))
	require.Nil(t, g)
	require.Nil(t, issue, "the token is stable across workers")
	require.Equal(t, map[string]string{"FUNCD_ADDON_IDP_TOKEN": "token-of-Function-default-rauthy-settings"}, own)

	spec := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{})
	require.Equal(t, "http://10.63.0.1:40123/addon/idp", spec.Env["FUNCD_ADDON_IDP_URL"])
	require.Equal(t, "token-of-Function-default-rauthy-settings", spec.Env["FUNCD_ADDON_IDP_TOKEN"])

	r.addonProxy.Token = func(v1.NamespaceName, v1.Kind, v1.ObjectName) (string, error) {
		return "", fault.Unavailablef("test", "no master of namespace default yet")
	}
	_, _, g = addonHook{r}.own(functionWorkload(fn))
	require.NotNil(t, g, "a worker node without the namespace's master (ADR-0371)")
	require.Equal(t, reasonNamespaceKeyMissing, g.reason)
}

// scenario: addon-binding-missing — a Function that binds an object that does not exist waits Pending,
// AddonObjectNotFound.
func TestScenarioAddonBindingMissing(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	fn := sampleFn()
	fn.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "idp", Object: "nowhere"}}
	waiting, reason, msg, err := r.resolveDataReferences(context.Background(), fn)
	require.NoError(t, err)
	require.True(t, waiting)
	require.Equal(t, "AddonObjectNotFound", reason)
	require.Contains(t, msg, `spec.bindings.addon[idp] → add-on object "nowhere" not found`)
}
