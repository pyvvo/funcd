package addon_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const schema = `{"type":"object","properties":{"issuer":{"type":"string"},"port":{"type":"integer"},` +
	`"tags":{"type":"array","items":{"type":"string"}}},"required":["issuer","port","tags"],"additionalProperties":false}`

// ADR-0332 Decision 4: each value is a literal, kept, or one ${{ }} expression over config, its string or, when it is
// no string, its JSON.
func TestRenderEnv(t *testing.T) {
	t.Parallel()
	docs := map[string]json.RawMessage{"config": json.RawMessage(`{"issuer":"https://login.example.com","port":8080,"tags":["a","b"]}`)}
	schemas := map[string]json.RawMessage{"config": json.RawMessage(schema)}
	env, err := addon.RenderEnv(map[string]string{
		"PUB_URL": "${{ config.issuer }}", "PORT": "${{ config.port }}", "TAGS": "${{ config.tags }}", "MODE": "prod",
		"URL": "${{ config.issuer + '/auth' }}",
	}, docs, schemas)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"PUB_URL": "https://login.example.com", "PORT": "8080", "TAGS": `["a","b"]`,
		"MODE": "prod", "URL": "https://login.example.com/auth"}, env)

	_, err = addon.RenderEnv(map[string]string{"X": "${{ config.realm }}"}, docs, schemas)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a property the schema lacks")
}

// ADR-0332 Decision 4: at apply, a value that holds an expression must be one ${{ }} expression that the schema types.
func TestCheckEnv(t *testing.T) {
	t.Parallel()
	require.NoError(t, addon.CheckEnv(map[string]string{"A": "${{ config.issuer }}", "B": "literal"}, json.RawMessage(schema)))
	for name, env := range map[string]map[string]string{
		"a property the schema lacks": {"A": "${{ config.adminEmail }}"},
		"text around the expression":  {"A": "x${{ config.issuer }}"},
		"another document":            {"A": "${{ input.x }}"},
	} {
		require.Error(t, addon.CheckEnv(env, json.RawMessage(schema)), name)
	}
	require.Error(t, addon.CheckEnv(map[string]string{"A": "${{ config.issuer }}"}, nil), "no schema types no property")
	require.NoError(t, addon.CheckEnv(map[string]string{"A": "x"}, nil), "a literal needs no schema")
}

// noRuntime runs no engine; the mappings below never converge.
type noRuntime struct{}

func (noRuntime) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{}, fault.Internalf("noRuntime", "no engine")
}
func (noRuntime) Teardown(context.Context, provider.ProviderRef) error { return nil }

// ADR-0332 Decisions 2 and 4: an Addon's change reaches every object of its kind, in every namespace, whatever the
// Addon's name; a Secret's change reaches the objects of its namespace that bind it.
func TestMapAddonAndSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	seed := func(ns v1.NamespaceName, name v1.ObjectName, kind string, secrets ...v1.ObjectName) {
		o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
		o.Name, o.Namespace, o.ResourceGroup = name, ns, "rg1"
		o.Spec.Kind, o.Spec.Bindings.Secret = kind, secrets
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	seed("a", "login", "IdentityProvider", "keys")
	seed("b", "login", "IdentityProvider")
	seed("a", "echo", "EchoService", "keys")
	seed("a", "other", "EchoService", "other-keys")
	r, err := addon.NewReconciler(addon.Deps{Store: st, Provider: noRuntime{}})
	require.NoError(t, err)

	req := func(ns v1.NamespaceName, name v1.ObjectName) controller.Request {
		return controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: ns, Name: name}
	}
	idp := &v1.Addon{ObjectMeta: v1.ObjectMeta{Name: "any-name"}, Spec: v1.AddonSpec{Kind: "IdentityProvider"}}
	require.ElementsMatch(t, []controller.Request{req("a", "login"), req("b", "login")}, r.MapAddon(ctx, idp))
	keys := &v1.Secret{ObjectMeta: v1.ObjectMeta{Name: "keys", Namespace: "a"}}
	require.ElementsMatch(t, []controller.Request{req("a", "login"), req("a", "echo")}, r.MapSecret(ctx, keys))
}

// unavailableRuntime can run no engine, as funcdctl dev's with no container engine (ADR-0334 Decision 1).
type unavailableRuntime struct{}

func (unavailableRuntime) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{Reason: "RuntimeUnavailable"}, fault.Unavailablef("funcdctl dev", "no Docker engine answers, and nerdctl is not on PATH")
}
func (unavailableRuntime) Teardown(context.Context, provider.ProviderRef) error { return nil }

// ADR-0334 Decision 1: an object on a runtime that can run no engine waits Pending, RuntimeUnavailable, with the
// runtime's own words, and no retry: the runtime is chosen at dev's start.
func TestRuntimeUnavailableNamesWhy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}}
	a.Name, a.Spec = "whoami", v1.AddonSpec{Kind: "EchoService", Image: "traefik/whoami@sha256:" + strings.Repeat("a", 64), Port: 80,
		Readiness: v1.AddonReadiness{Path: "/health"}}
	_, err := st.Create(ctx, a)
	require.NoError(t, err)
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "echo", "default", "rg1", "EchoService"
	_, err = st.Create(ctx, o)
	require.NoError(t, err)
	r, err := addon.NewReconciler(addon.Deps{Store: st, Provider: unavailableRuntime{}, Modes: []string{v1.AddonRuntimeContainerd}})
	require.NoError(t, err)

	res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "echo"})
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter, "no retry")
	got, err := st.Get(ctx, v1.KindAddonObject.GVK(), "default", "echo")
	require.NoError(t, err)
	status := got.(*v1.AddonObject).Status
	require.Equal(t, v1.PhasePending, status.Phase)
	ready := status.Conditions[0]
	require.Equal(t, "RuntimeUnavailable", ready.Reason)
	require.Contains(t, ready.Message, "nerdctl is not on PATH")
}
