package addon_test

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// ADR-0338 Decisions 2, 3 and 5: the checks of a Function's call of an add-on object's engine.

// denyAll is an Authorizer that denies every request but those allow permits.
type denyAll struct{ allow func(auth.Request) bool }

func (d denyAll) Authorize(_ context.Context, r auth.Request) (auth.Decision, error) {
	if d.allow != nil && d.allow(r) {
		return auth.Decision{Allowed: true}, nil
	}
	return auth.Decision{Reason: "a Policy forbids it"}, nil
}

// callRig is the Function rauthy-settings of default, binding the IdentityProvider rauthy-login as idp, whose Addon
// serves /auth/v1 and keeps /auth/v1/clients_dyn private.
type callRig struct {
	ctx  context.Context
	st   store.Store
	held bool
}

func newCallRig(t *testing.T, ready bool) *callRig {
	t.Helper()
	g := &callRig{ctx: context.Background(), st: store.New(memory.New())}
	t.Cleanup(func() { _ = g.st.Close() })
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "rauthy"},
		Spec: v1.AddonSpec{Kind: "IdentityProvider", Port: 8080, BasePath: "/auth/v1", PrivatePaths: []string{"/auth/v1/clients_dyn"},
			Readiness: v1.AddonReadiness{Path: "/auth/v1/ping"},
			Image:     "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809"}}
	register(t, g.st, a)
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Kind = "rauthy-login", "default", "rg1", "IdentityProvider"
	created, err := g.st.Create(g.ctx, o)
	require.NoError(t, err)
	if ready {
		o = created.(*v1.AddonObject)
		o.Status.Address, o.Status.AddonRevision = "10.63.0.9:8080", "rauthy-1"
		o.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
		_, err = g.st.Update(g.ctx, o)
		require.NoError(t, err)
	}
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = "rauthy-settings", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x.mjs"
	fn.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "idp", Object: "rauthy-login"}}
	_, err = g.st.Create(g.ctx, fn)
	require.NoError(t, err)
	return g
}

func (g *callRig) calls(authz auth.Authorizer) *addon.Calls {
	return addon.NewCalls(g.st, authz, func(v1.NamespaceName) bool { return g.held })
}

// boundOnly permits addon::call on the objects the Function binds, as the built-in does.
func boundOnly() denyAll {
	return denyAll{allow: func(r auth.Request) bool {
		return r.Action == auth.ActionAddonCall && r.Resource != nil && r.Resource.Name == "rauthy-login"
	}}
}

func TestCallsTarget(t *testing.T) {
	t.Parallel()
	g := newCallRig(t, true)
	got, err := g.calls(boundOnly()).Target(g.ctx, "default", "rauthy-settings", "idp", "/auth/v1//password_policy/.")
	require.NoError(t, err)
	require.Equal(t, local.AddonTarget{Address: "10.63.0.9:8080", Path: "/auth/v1/password_policy"}, got)
}

// scenario: addon-call-path — a path outside basePath, under a private path, in any spelling, or with a .. segment is
// refused with 404, as the edge refuses it.
func TestScenarioAddonCallPath(t *testing.T) {
	t.Parallel()
	g := newCallRig(t, true)
	decoded := func(raw string) string {
		u, err := url.Parse("http://x" + raw)
		require.NoError(t, err)
		return u.Path
	}
	for _, p := range []string{"/admin", "/auth/v1/clients_dyn", "/auth/v1/clients_dyn/x", decoded("/auth/v1/%63lients_dyn"),
		"/auth/v1//clients_dyn", "/auth/v1/./clients_dyn", "/auth/v1/x/../../v2", "/auth/v2"} {
		_, err := g.calls(boundOnly()).Target(g.ctx, "default", "rauthy-settings", "idp", p)
		require.True(t, errors.Is(err, local.ErrAddonPathRefused), "%s: %v", p, err)
	}
}

// scenario: addon-call-refused (the funcd side) — an alias not bound, and a call Cedar denies, are refused before any
// path or engine is read.
func TestCallsRefuse(t *testing.T) {
	t.Parallel()
	g := newCallRig(t, true)
	_, err := g.calls(boundOnly()).Target(g.ctx, "default", "rauthy-settings", "other", "/auth/v1/x")
	require.True(t, errors.Is(err, local.ErrAddonNoBinding), "%v", err)
	_, err = g.calls(denyAll{}).Target(g.ctx, "default", "rauthy-settings", "idp", "/auth/v1/x")
	require.True(t, errors.Is(err, local.ErrAddonForbidden), "%v", err)
	require.ErrorContains(t, err, "a Policy forbids it")
}

// scenario: addon-call-no-engine — an object that is not Ready fails the call with 503, and the dependency check
// names it.
func TestScenarioAddonCallNoEngine(t *testing.T) {
	t.Parallel()
	g := newCallRig(t, false)
	_, err := g.calls(boundOnly()).Target(g.ctx, "default", "rauthy-settings", "idp", "/auth/v1/x")
	require.True(t, errors.Is(err, local.ErrAddonEngineUnavailable), "%v", err)
	err = g.calls(boundOnly()).CheckRead(g.ctx, "default", "rauthy-settings", "idp")
	require.True(t, errors.Is(err, local.ErrAddonEngineUnavailable), "%v", err)
	require.NoError(t, newCallRig(t, true).calls(boundOnly()).CheckRead(g.ctx, "default", "rauthy-settings", "idp"))
}

// A held namespace is refused just before the dial (ADR-0338 Decision 2), before the object's readiness: a held object
// has no engine, and the call gets the hold's answer.
func TestCallsRefuseAHeldNamespace(t *testing.T) {
	t.Parallel()
	for _, ready := range []bool{true, false} {
		g := newCallRig(t, ready)
		g.held = true
		_, err := g.calls(boundOnly()).Target(g.ctx, "default", "rauthy-settings", "idp", "/auth/v1/x")
		require.True(t, errors.Is(err, local.ErrAddonHeld), "ready %v: %v", ready, err)
		_, err = g.calls(boundOnly()).Target(g.ctx, "default", "rauthy-settings", "idp", "/auth/v1/clients_dyn")
		require.True(t, errors.Is(err, local.ErrAddonPathRefused), "the path is checked first: %v", err)
	}
}

// ADR-0338 Decision 3: a Function's proxy token names it, under the node master; a token under another master, a
// changed one or a lake token names no one.
func TestProxyToken(t *testing.T) {
	t.Parallel()
	master := []byte("node master")
	token := addon.Token(master, "default", "rauthy-settings")
	got, ok := addon.TokenCaller(master, token)
	require.True(t, ok)
	require.Equal(t, local.Ref{Namespace: "default", Function: "rauthy-settings"}, got)
	require.Equal(t, token, addon.Token(master, "default", "rauthy-settings"), "deterministic")
	for _, bad := range []string{"", "x", token + "a", addon.Token([]byte("another master"), "default", "rauthy-settings"),
		addon.Token(master, "default", "other")[:20] + token[20:]} {
		_, ok := addon.TokenCaller(master, bad)
		require.False(t, ok, "%q", bad)
	}
}
