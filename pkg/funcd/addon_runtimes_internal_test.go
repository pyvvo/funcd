package funcd

import (
	"net/http"
	"testing"

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
