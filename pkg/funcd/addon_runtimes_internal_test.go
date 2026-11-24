package funcd

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ADR-0344: an Addon ships runtimes, at most one of which runs its kind.

// denoAddon is the Addon name, which ships deno2 for Functions and Jobs and adds no kind.
func denoAddon(name v1.ObjectName, runtime v1.RuntimeName) *v1.Addon {
	a := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: name}}
	a.Spec.Runtimes = []v1.ShippedRuntime{{Name: runtime, Image: "ghcr.io/example/funcd-deno" + addonDigest,
		Workloads: []v1.Workload{v1.WorkloadFunction, v1.WorkloadJob}}}
	return a
}

// requireNoAddon asserts that no Addon name is stored.
func (e *addonEnv) requireNoAddon(t *testing.T, name v1.ObjectName) {
	t.Helper()
	_, err := e.admin.Get(e.ctx, v1.KindAddon, "", name)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "nothing is stored: %v", err)
}

// scenario: addon-runtime-name-refused — a runtime named as one of funcd's, of a family funcd's names choose, or as a
// key of runtime.containerd.imageOverride, and two entries of one name, are refused 400; a name another Addon ships is
// refused 409; and nothing is stored.
func TestScenarioAddonRuntimeNameRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t, WithImageOverrides("ruby3"))
	for _, name := range []v1.RuntimeName{"nodejs22", "node24", "python-tofu", "python314", "duckdb", "rqlite", "ruby3"} {
		status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, denoAddon("deno", name))
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		require.Contains(t, detail, "spec.runtimes[0].name", name)
		e.requireNoAddon(t, "deno")
	}
	twice := denoAddon("deno", "deno2")
	twice.Spec.Runtimes = append(twice.Spec.Runtimes, twice.Spec.Runtimes[0])
	status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, twice)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "spec.runtimes[1].name")
	e.requireNoAddon(t, "deno")

	e.apply(t, e.admin, denoAddon("deno", "deno2"))
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, denoAddon("deno-copy", "deno2"))
	require.Equal(t, http.StatusConflict, status, detail)
	require.Contains(t, detail, `Addon "deno" ships the runtime "deno2"`)
	e.requireNoAddon(t, "deno-copy")
}

// scenario: addon-shape-refused — two entries that run a kind, an entry with no image, Engine beside another
// workload, a port on a Function's runtime and settings outside an engine's entry are each refused 400, naming the
// field, and nothing is stored.
func TestScenarioAddonShapeRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	for name, c := range map[string]struct {
		edit  func(a *v1.Addon)
		field string
	}{
		"two kind entries": {func(a *v1.Addon) {
			second := rauthyAddon("rauthy").Spec.Runtimes[0]
			second.Name, second.Attributes.Kind = "second", "SecondProvider"
			a.Spec.Runtimes = append(a.Spec.Runtimes, second)
		}, "spec.runtimes[2] runs a kind"},
		"an entry with no image":   {func(a *v1.Addon) { a.Spec.Runtimes[1].Image = "" }, "spec.runtimes[1].image"},
		"Engine beside a workload": {func(a *v1.Addon) { a.Spec.Runtimes[0].Workloads = append(a.Spec.Runtimes[0].Workloads, v1.WorkloadJob) }, "spec.runtimes[0].workloads"},
		"a port on a Function's runtime": {func(a *v1.Addon) {
			a.Spec.Runtimes[1].Attributes = &v1.AddonAttributes{Port: 8080}
		}, "spec.runtimes[1].attributes.port"},
		"settings outside an engine": {func(a *v1.Addon) {
			a.Spec.Runtimes[1].Attributes = &v1.AddonAttributes{Settings: rauthySettingsAddon("rauthy").Spec.Runtimes[0].Attributes.Settings}
		}, "spec.runtimes[1].attributes.settings"},
	} {
		a := rauthyAddon("rauthy")
		a.Spec.Runtimes = append(a.Spec.Runtimes, denoAddon("rauthy", "deno2").Spec.Runtimes...)
		c.edit(a)
		status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		require.Contains(t, detail, c.field, name)
		e.requireNoAddon(t, "rauthy")
	}
}

// scenario: addon-runtimes-only — two Addons whose entries serve only Functions are both stored and add no kind; one
// then gains an engine entry, whose object runs, and keeps its kind: the kind can neither change nor go (ADR-0344 Decision 10).
func TestScenarioAddonRuntimesOnly(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, denoAddon("deno", "deno2"))
	e.apply(t, e.admin, denoAddon("bun", "bun1"))
	for _, name := range []v1.ObjectName{"deno", "bun"} {
		obj, err := e.admin.Get(e.ctx, v1.KindAddon, "", name)
		require.NoError(t, err)
		require.Empty(t, obj.(*v1.Addon).Spec.Kinds(), "%s adds no kind", name)
	}

	gained := denoAddon("deno", "deno2")
	gained.Spec.Runtimes = append(gained.Spec.Runtimes, rauthyAddon("deno").Spec.Runtimes...)
	e.apply(t, e.admin, gained)
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")

	dropped := denoAddon("deno", "deno2")
	status, detail := e.send(t, http.MethodPut, addonsPath+"/deno", addonAdminToken, dropped)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "cannot go")
	changed := denoAddon("deno", "deno2")
	changed.Spec.Runtimes = append(changed.Spec.Runtimes, rauthyAddon("deno").Spec.Runtimes...)
	changed.Spec.Runtimes[1].Attributes.Kind = "OtherProvider"
	status, detail = e.send(t, http.MethodPut, addonsPath+"/deno", addonAdminToken, changed)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "cannot change")
}

// scenario: addon-engine-unchanged — rauthy's engine entry runs each IdentityProvider's engine from the AddonRevision
// it pins, with that entry's image and attributes (ADR-0339 Decision 2).
func TestScenarioAddonEngineUnchanged(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.waitCurrent(t, "rauthy-1")
	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k1"}))
	e.apply(t, e.dev, identityProvider("default", "rauthy-keys"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	e.pin(t, "default", "login", "rauthy-1", "ghcr.io/pyvvo/funcd-rauthy"+addonDigest)
	spec, _, _ := e.rt.state(ref("default", "login"))
	at := rauthyAddon("rauthy").Spec.KindAttributes()
	require.Equal(t, v1.Kind("IdentityProvider"), spec.ServedKind)
	require.Equal(t, at.Port, spec.Port)
	require.Equal(t, at.Readiness.Path, spec.Readiness.Path)
	require.Equal(t, at.Storage.Path, spec.Storage.Path)
	require.Equal(t, "https://login.example.com/auth/v1", spec.Env["PUB_URL"], "its env renders over the object's config")
}

// infraStackAddon is the Addon infrastack, whose engineless kind InfraStack its Function infrastack-reconciler
// reconciles (ADR-0345 Decision 1).
func infraStackAddon() *v1.Addon {
	a := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "infrastack"}}
	a.Spec.Runtimes = []v1.ShippedRuntime{{Name: "infrastack", Workloads: []v1.Workload{v1.WorkloadEngineless},
		Attributes: &v1.AddonAttributes{Kind: "InfraStack",
			Function: &v1.AddonFunction{Ref: "infrastack-reconciler"}}}}
	return a
}

// scenario: engineless-shape-refused — an Engineless entry with an image, an engine's attribute, settings, no
// function, or another workload beside it, and an object's spec.functions that names a kind that is neither a settings
// kind of its Addon nor its own engineless kind, are each refused 400, naming the field.
func TestScenarioEnginelessShapeRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	for name, c := range map[string]struct {
		edit  func(rt *v1.ShippedRuntime)
		field string
	}{
		"an image": {func(rt *v1.ShippedRuntime) { rt.Image = "ghcr.io/example/stack" + addonDigest }, "spec.runtimes[0].image"},
		"a port":   {func(rt *v1.ShippedRuntime) { rt.Attributes.Port = 8080 }, "spec.runtimes[0].attributes.port"},
		"settings": {func(rt *v1.ShippedRuntime) {
			rt.Attributes.Settings = rauthySettingsAddon("x").Spec.Runtimes[0].Attributes.Settings
		}, "spec.runtimes[0].attributes.settings"},
		"no function":      {func(rt *v1.ShippedRuntime) { rt.Attributes.Function = nil }, "spec.runtimes[0].attributes.function"},
		"another workload": {func(rt *v1.ShippedRuntime) { rt.Workloads = append(rt.Workloads, v1.WorkloadJob) }, "spec.runtimes[0].workloads"},
	} {
		a := infraStackAddon()
		c.edit(&a.Spec.Runtimes[0])
		status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		require.Contains(t, detail, c.field, name)
		e.requireNoAddon(t, "infrastack")
	}

	e.apply(t, e.admin, infraStackAddon())
	stacks := "/apis/funcd.io/v1alpha1/namespaces/default/infrastacks"
	own := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	own.Name, own.Namespace, own.ResourceGroup = "bunny-edge", "default", "rg1"
	own.Spec.Functions = []v1.AddonObjectFunction{{Kind: "InfraStack", Ref: "bunny-edge-reconciler"}}
	require.Eventually(t, func() bool {
		status, _ := e.send(t, http.MethodPost, stacks, addonDevToken, own)
		return status == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "an object names its own Function for its engineless kind")
	other := *own
	other.Name, other.Spec.Functions = "site", []v1.AddonObjectFunction{{Kind: "IdentityProvider", Ref: "x"}}
	status, detail := e.send(t, http.MethodPost, stacks, addonDevToken, &other)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "spec.functions[0].kind")
}

// An Addon's kind keeps its workload: each object keeps how it runs, so a replace that moves the kind's entry between
// Engine and Engineless is refused, as a change of the kind is (ADR-0344 Decision 10).
func TestEnginelessKindKeepsItsWorkload(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, infraStackAddon())
	moved := infraStackAddon()
	moved.Spec.Runtimes = rauthyAddon("infrastack").Spec.Runtimes
	moved.Spec.Runtimes[0].Attributes.Kind = "InfraStack"
	status, detail := e.send(t, http.MethodPut, addonsPath+"/infrastack", addonAdminToken, moved)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "cannot change between Engine and Engineless")

	e.apply(t, e.admin, rauthyAddon("rauthy"))
	back := infraStackAddon()
	back.Name, back.Spec.Runtimes[0].Name, back.Spec.Runtimes[0].Attributes.Kind = "rauthy", "rauthy", "IdentityProvider"
	status, detail = e.send(t, http.MethodPut, addonsPath+"/rauthy", addonAdminToken, back)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "cannot change between Engine and Engineless")
}

// ADR-0345 Decision 6: the DELETE of a held stack answers 202 with it, again 202 while it is held; a write to it is
// stored and leaves it deleting.
func TestEnginelessDeleteAnswersHeld(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, infraStackAddon())
	stacks := "/apis/funcd.io/v1alpha1/namespaces/default/infrastacks"
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Deletion = "bunny-edge", "default", "rg1", v1.DeletionDelete
	require.Eventually(t, func() bool {
		status, _ := e.send(t, http.MethodPost, stacks, addonDevToken, o)
		return status == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the kind is served")
	require.Eventually(t, func() bool {
		stored, ok := e.object(t, "default", "bunny-edge")
		return ok && stored.Status.Workload == v1.WorkloadEngineless
	}, 5*time.Second, 10*time.Millisecond, "its first pass stores the mark")
	for range 2 {
		status, body := e.send(t, http.MethodDelete, stacks+"/bunny-edge", addonDevToken, nil)
		require.Equal(t, http.StatusAccepted, status, body)
	}
	stored, ok := e.object(t, "default", "bunny-edge")
	require.True(t, ok)
	require.NotNil(t, stored.DeletionTime)
	edited := *stored
	edited.TypeMeta, edited.ResourceVersion = v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}, ""
	edited.Spec.Kind, edited.Spec.Config = "", json.RawMessage(`{"job":"other"}`)
	status, detail := e.send(t, http.MethodPut, stacks+"/bunny-edge", addonDevToken, &edited)
	require.Equal(t, http.StatusOK, status, detail)
	stored, _ = e.object(t, "default", "bunny-edge")
	require.NotNil(t, stored.DeletionTime, "a write keeps it deleting")
	require.JSONEq(t, `{"job":"other"}`, string(stored.Spec.Config))
}

// ADR-0345 Decisions 3 and 6: an engineless object runs no engine, so an engine field is refused 400, as on a settings
// object, while spec.deletion and spec.functions stay its own.
func TestEnginelessObjectRefusesEngineFields(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, infraStackAddon())
	stacks := "/apis/funcd.io/v1alpha1/namespaces/default/infrastacks"
	stack := func(change func(*v1.AddonObject)) *v1.AddonObject {
		o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
		o.Name, o.Namespace, o.ResourceGroup, o.Spec.Deletion = "site", "default", "rg1", v1.DeletionDelete
		change(o)
		return o
	}
	require.Eventually(t, func() bool {
		status, _ := e.send(t, http.MethodPost, stacks, addonDevToken, stack(func(o *v1.AddonObject) { o.Name = "probe" }))
		return status == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the kind is served")
	for name, change := range map[string]func(*v1.AddonObject){
		"secret":    func(o *v1.AddonObject) { o.Spec.Bindings.Secret = []v1.ObjectName{"keys"} },
		"resources": func(o *v1.AddonObject) { o.Spec.Resources.Memory = "64Mi" },
		"ingress":   func(o *v1.AddonObject) { o.Spec.Ingress = &v1.Ingress{Host: "site.example.com"} },
	} {
		status, detail := e.send(t, http.MethodPost, stacks, addonDevToken, stack(change))
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		require.Contains(t, detail, "runs no engine", name)
	}
	status, detail := e.send(t, http.MethodPost, stacks, addonDevToken, stack(func(o *v1.AddonObject) {
		o.Spec.Functions = []v1.AddonObjectFunction{{Kind: "InfraStack", Ref: "site-reconciler"}}
	}))
	require.Equal(t, http.StatusOK, status, detail)
}

// ADR-0345 Decision 1 across an Addon's deletion: an Addon created again for a kind whose marked object ran no engine
// is refused 409 as an Engine kind, as a replace that moves the kind is refused 400, and stored as an Engineless one.
func TestEnginelessKindKeepsItsWorkloadAcrossDeletion(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, infraStackAddon())
	stacks := "/apis/funcd.io/v1alpha1/namespaces/default/infrastacks"
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	o.Name, o.Namespace, o.ResourceGroup, o.Spec.Deletion = "bunny-edge", "default", "rg1", v1.DeletionRetain
	require.Eventually(t, func() bool {
		status, _ := e.send(t, http.MethodPost, stacks, addonDevToken, o)
		return status == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the kind is served")
	require.Eventually(t, func() bool {
		stored, ok := e.object(t, "default", "bunny-edge")
		return ok && stored.Status.Workload == v1.WorkloadEngineless
	}, 5*time.Second, 10*time.Millisecond, "its first pass stores the mark")
	status, detail := e.send(t, http.MethodDelete, addonsPath+"/infrastack", addonAdminToken, nil)
	require.Equal(t, http.StatusNoContent, status, detail)

	engine := rauthyAddon("infrastack")
	engine.Spec.Runtimes[0].Attributes.Kind = "InfraStack"
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, engine)
	require.Equal(t, http.StatusConflict, status, detail)
	require.Contains(t, detail, `default/bunny-edge ran no engine`)
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, infraStackAddon())
	require.Equal(t, http.StatusOK, status, detail)
}

// ADR-0345 Decision 1 the other way: an object whose engine ran keeps its Engine mark once its Addon and the Addon's
// revisions are gone, so an Addon created again for its kind as an Engineless kind is refused 409.
func TestEngineKindKeepsItsWorkloadAcrossDeletion(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	logins := "/apis/funcd.io/v1alpha1/namespaces/default/identityproviders"
	require.Eventually(t, func() bool {
		status, _ := e.send(t, http.MethodPost, logins, addonDevToken, identityProvider("default"))
		return status == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the kind is served")
	require.Eventually(t, func() bool {
		stored, ok := e.object(t, "default", "login")
		return ok && stored.Status.Workload == v1.WorkloadEngine
	}, 5*time.Second, 10*time.Millisecond, "its engine's pass stores the mark")
	status, detail := e.send(t, http.MethodDelete, addonsPath+"/rauthy", addonAdminToken, nil)
	require.Equal(t, http.StatusNoContent, status, detail)

	engineless := infraStackAddon()
	engineless.Name, engineless.Spec.Runtimes[0].Name, engineless.Spec.Runtimes[0].Attributes.Kind = "rauthy", "rauthy", "IdentityProvider"
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, engineless)
	require.Equal(t, http.StatusConflict, status, detail)
	require.Contains(t, detail, "default/login ran an engine")
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, rauthyAddon("rauthy"))
	require.Equal(t, http.StatusOK, status, detail)
}
