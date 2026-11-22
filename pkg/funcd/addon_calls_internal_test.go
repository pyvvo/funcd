package funcd

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// ADR-0338: a Function calls an add-on object's engine through funcd.

// settingsFn is the Function rauthy-settings of ns, binding the add-on object object as idp.
func settingsFn(ns v1.NamespaceName, object v1.ObjectName) *v1.Function {
	fn := &v1.Function{TypeMeta: tmeta(v1.KindFunction)}
	fn.Name, fn.Namespace, fn.ResourceGroup = "rauthy-settings", ns, "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/settings.mjs"
	fn.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: "idp", Object: object}}
	return fn
}

// scenario: addon-binding-standalone — a Function that no App holds binds an add-on object of its namespace with no
// root entry, and Cedar's built-in permits its calls once the object is Ready.
func TestScenarioAddonBindingStandalone(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	e.apply(t, e.dev, settingsFn("default", "login"))

	var target local.AddonTarget
	require.Eventually(t, func() bool {
		var err error
		target, err = e.p.addon.calls.Target(e.ctx, "default", "rauthy-settings", "idp", "/auth/v1/password_policy")
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "the bound object's engine is reached")
	o, _ := e.object(t, "default", "login")
	require.Equal(t, local.AddonTarget{Address: o.Status.Address, Path: "/auth/v1/password_policy"}, target)
	_, err := e.p.addon.calls.Target(e.ctx, "default", "rauthy-settings", "idp", "/auth/v1/clients_dyn")
	require.True(t, errors.Is(err, local.ErrAddonPathRefused), "the object's private path: %v", err)
}

// scenario: addon-call-refused (Cedar) — a Policy that forbids addon::call refuses the call, which the built-in
// permitted.
func TestAddonCallForbiddenByAPolicy(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	e.apply(t, e.dev, settingsFn("default", "login"))
	pol := &v1.Policy{TypeMeta: tmeta(v1.KindPolicy)}
	pol.Name, pol.Namespace, pol.ResourceGroup = "no-calls", "default", "rg1"
	pol.Spec.Cedar = `forbid(principal == Function::"default/rauthy-settings", action == Action::"addon::call", resource == AddonObject::"default/login");`
	e.apply(t, e.dev, pol)
	require.Eventually(t, func() bool {
		_, err := e.p.addon.calls.Target(e.ctx, "default", "rauthy-settings", "idp", "/auth/v1/x")
		return errors.Is(err, local.ErrAddonForbidden)
	}, 5*time.Second, 20*time.Millisecond, "the forbid refuses the call")
}

// scenario: addon-binding-undeclared — an App's Function binds an add-on object that its App declares neither by name
// nor by ref: 400 naming the binding. Declared by ref, it is admitted.
func TestScenarioAddonBindingUndeclared(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	a := &v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: v1.ObjectMeta{Name: "settings", Namespace: "default", ResourceGroup: "settings"}}
	fn := settingsFn("default", "login")
	a.Spec.Functions = []v1.AppFunction{{Name: "rauthy-settings", FunctionSpec: fn.Spec}}
	status, detail := e.send(t, http.MethodPost, fmt.Sprintf(appsPath, "default"), addonDevToken, a)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "spec.bindings.addon[0]")
	require.Contains(t, detail, `"login"`)

	a.Spec.Addons = []v1.AppAddon{{Ref: "login"}}
	status, detail = e.send(t, http.MethodPost, fmt.Sprintf(appsPath, "default"), addonDevToken, a)
	require.Equal(t, http.StatusOK, status, "declared by ref: %s", detail)
}

// A bound add-on object is in use: a revision that drops it keeps it until the binding goes (ADR-0338 Decision 1, as
// ADR-0199 Decision 6).
func TestAddonBoundObjectIsInUseForPrune(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, loginApp("default", "web-auth"))
	e.waitApp(t, "default", "web-auth")
	e.apply(t, e.dev, settingsFn("default", "web-auth-login"))

	a := loginApp("default", "web-auth")
	a.Spec.Addons = nil
	a.Spec.ConfigMaps = []v1.ConfigMapEntry{{Name: "web-auth-settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"k": "v"}}}}
	e.apply(t, e.dev, a)
	require.True(t, poll.Never(func() bool {
		_, stored := e.object(t, "default", "web-auth-login")
		return !stored
	}, 500*time.Millisecond, 20*time.Millisecond), "a bound object is kept")
	require.NoError(t, e.dev.Delete(e.ctx, v1.KindFunction, "default", "rauthy-settings"))
	require.Eventually(t, func() bool {
		_, stored := e.object(t, "default", "web-auth-login")
		return !stored
	}, 10*time.Second, 20*time.Millisecond, "once no Function binds it, the prune takes it")
}
