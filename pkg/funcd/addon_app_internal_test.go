package funcd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ADR-0337: an App holds the objects of an add-on's kind in its addons section.

const appsPath = "/apis/funcd.io/v1alpha1/namespaces/%s/apps"

// loginApp is the App name of ns whose one addons entry is the IdentityProvider <name>-login, as the rauthy template
// renders it for an App of that name.
func loginApp(ns v1.NamespaceName, name v1.ObjectName) *v1.App {
	a := &v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: v1.ResourceGroupName(name)}}
	a.Spec.Addons = []v1.AppAddon{{Name: name + "-login", AddonObjectSpec: v1.AddonObjectSpec{
		Kind: "IdentityProvider", Config: json.RawMessage(`{"issuer":"https://` + string(name) + `.example.com/auth/v1","port":8080}`),
	}}}
	return a
}

// waitApp waits until the App ns/name is Ready with every child Ready, and returns it.
func (e *addonEnv) waitApp(t *testing.T, ns v1.NamespaceName, name v1.ObjectName) *v1.App {
	t.Helper()
	var app *v1.App
	require.Eventually(t, func() bool {
		obj, err := e.p.cfg.store.Get(e.ctx, v1.KindApp.GVK(), ns, name)
		if err != nil {
			return false
		}
		app = obj.(*v1.App)
		c, _ := app.Status.Conditions.Get("Ready")
		return app.Status.Phase == v1.PhaseReady && c.Status == v1.ConditionTrue && app.Status.CurrentRevision != ""
	}, 10*time.Second, 20*time.Millisecond, "App %s/%s is Ready", ns, name)
	return app
}

// scenario: addon-part-refused — an addons entry whose kind no Addon registers, whose config breaks its Addon's
// schema, or whose bindings.secret the App does not declare: 400 naming the entry, and for the schema the property and
// the keyword; nothing is stored.
func TestScenarioAddonPartRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	for name, c := range map[string]struct {
		edit func(*v1.AppAddon)
		want []string
	}{
		"a kind no Addon registers": {func(p *v1.AppAddon) { p.Kind = "NoSuchService" },
			[]string{"spec.addons[0] (NoSuchService/web-auth-login)", `no Addon registers the kind "NoSuchService"`}},
		"a config that breaks the schema": {func(p *v1.AppAddon) { p.Config = json.RawMessage(`{"issuer":5,"port":8080}`) },
			[]string{"spec.addons[0] (IdentityProvider/web-auth-login)", "spec.config.issuer", "string"}},
		"an undeclared Secret": {func(p *v1.AppAddon) { p.Bindings.Secret = []v1.ObjectName{"rauthy-keys"} },
			[]string{"spec.addons[0].bindings.secret[0]", `"rauthy-keys"`, "spec.secrets does not declare"}},
	} {
		a := loginApp("default", "web-auth")
		c.edit(&a.Spec.Addons[0])
		status, detail := e.send(t, http.MethodPost, fmt.Sprintf(appsPath, "default"), addonDevToken, a)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		for _, w := range c.want {
			require.Contains(t, detail, w, name)
		}
		_, err := e.p.cfg.store.Get(e.ctx, v1.KindApp.GVK(), "default", "web-auth")
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s: nothing is stored", name)
	}
}

// ADR-0337 Decisions 3 and 4: an addons part is written with the App's controller reference, its engine starts, and
// the App is Ready once the part is, its line naming the add-on's kind.
func TestScenarioAddonPartReady(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, loginApp("default", "web-auth"))
	app := e.waitApp(t, "default", "web-auth")
	require.Equal(t, []v1.AppChild{{Kind: "IdentityProvider", Name: "web-auth-login", State: v1.AppChildReady}}, app.Status.Children)
	part, ok := e.object(t, "default", "web-auth-login")
	require.True(t, ok)
	c, ok := v1.ControllerOf(part.OwnerReferences)
	require.True(t, ok)
	require.Equal(t, v1.ObjectRef{Kind: v1.KindApp, Namespace: "default", Name: "web-auth"}, c.ObjectRef)
	require.Equal(t, app.UID, c.UID)
	_, running, _ := e.rt.state(ref("default", "web-auth-login"))
	require.True(t, running)
}

// scenario: addon-template-twice — the template deployed as web-auth and as admin-auth in one namespace: two Apps, each
// owning its own part, each part with its own engine.
func TestScenarioAddonTemplateTwice(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	for _, name := range []v1.ObjectName{"web-auth", "admin-auth"} {
		e.apply(t, e.dev, loginApp("team", name))
	}
	for _, name := range []v1.ObjectName{"web-auth", "admin-auth"} {
		e.waitApp(t, "team", name)
		part, ok := e.object(t, "team", name+"-login")
		require.True(t, ok)
		c, _ := v1.ControllerOf(part.OwnerReferences)
		require.Equal(t, name, c.Name, "%s-login is %s's", name, name)
		spec, running, _ := e.rt.state(ref("team", name+"-login"))
		require.True(t, running, "%s-login has its own engine", name)
		require.Equal(t, "https://"+string(name)+".example.com/auth/v1", spec.Env["PUB_URL"])
	}
}

// scenario: addon-part-pruned — a revision that drops the addons entry deletes web-auth-login, and its engine goes.
func TestScenarioAddonPartPruned(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, loginApp("default", "web-auth"))
	e.waitApp(t, "default", "web-auth")

	a := loginApp("default", "web-auth")
	a.Spec.Addons = nil
	a.Spec.ConfigMaps = []v1.ConfigMapEntry{{Name: "web-auth-settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"k": "v"}}}}
	e.apply(t, e.dev, a)
	require.Eventually(t, func() bool {
		_, stored := e.object(t, "default", "web-auth-login")
		_, running, _ := e.rt.state(ref("default", "web-auth-login"))
		return !stored && !running
	}, 10*time.Second, 20*time.Millisecond, "the dropped part and its engine go")
}

// scenario: addon-app-delete — deleting web-auth deletes web-auth-login and its engine, and the Addon stays.
func TestScenarioAddonAppDelete(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, loginApp("default", "web-auth"))
	e.waitApp(t, "default", "web-auth")

	require.NoError(t, e.dev.Delete(e.ctx, v1.KindApp, "default", "web-auth"))
	require.Eventually(t, func() bool {
		_, stored := e.object(t, "default", "web-auth-login")
		_, running, _ := e.rt.state(ref("default", "web-auth-login"))
		return !stored && !running
	}, 10*time.Second, 20*time.Millisecond, "the App's part and its engine go")
	_, err := e.admin.Get(e.ctx, v1.KindAddon, "", "rauthy")
	require.NoError(t, err, "the Addon stays")
}
