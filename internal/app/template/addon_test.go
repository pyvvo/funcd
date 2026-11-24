package template

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// rauthyAddonYAML is a template's addon.yaml, whose env holds an expression over values: Load keeps it as written.
const rauthyAddonYAML = `apiVersion: funcd.io/v1alpha1
kind: Addon
metadata:
  name: rauthy
spec:
  runtimes:
    - name: identityprovider
      image: ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809
      workloads:
        - Engine
      attributes:
        kind: IdentityProvider
        port: 8080
        readiness:
          path: /auth/v1/ping
        env:
          PUB_URL: ${{ values.publicHost }}
`

// runtimesOnlyAddonYAML is an addon.yaml whose Addon ships a runtime for Functions and adds no kind (ADR-0344
// Decision 10).
const runtimesOnlyAddonYAML = `apiVersion: funcd.io/v1alpha1
kind: Addon
metadata:
  name: deno
spec:
  runtimes:
    - name: deno2
      image: ghcr.io/example/funcd-deno@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809
      workloads:
        - Function
`

const loginFragment = `addons:
  - name: ${{ app.name + "-login" }}
    kind: IdentityProvider
    config:
      issuer: https://login.example.com/auth/v1
`

// ADR-0337 Decision 1: Load reads addon.yaml, one Addon with no namespace, and keeps its text unchanged.
func TestLoadReadsTheAddon(t *testing.T) {
	t.Parallel()
	dir := writeTemplate(t, baseApp, map[string]string{"resources/api.yaml": apiFragment, AddonFile: rauthyAddonYAML})
	tpl, err := Load(dir)
	require.NoError(t, err)
	require.NotNil(t, tpl.Addon)
	require.Equal(t, v1.ObjectName("rauthy"), tpl.Addon.Name)
	require.Equal(t, "IdentityProvider", tpl.Addon.Spec.Kind())
	require.Equal(t, "${{ values.publicHost }}", tpl.Addon.Spec.Runtimes[0].Attributes.Env["PUB_URL"], "scenario: addon-yaml-as-written")

	plain, err := Load(writeTemplate(t, baseApp, map[string]string{"resources/api.yaml": apiFragment}))
	require.NoError(t, err)
	require.Nil(t, plain.Addon, "a template without addon.yaml has no Addon")
}

// ADR-0337 Decision 1: addon.yaml holds exactly one Addon, which names no namespace.
func TestLoadRefusesABadAddon(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ body, want string }{
		"two documents": {rauthyAddonYAML + "---\n" + rauthyAddonYAML, "holds 2 documents"},
		"a namespace": {rauthyAddonYAML[:len("apiVersion: funcd.io/v1alpha1\nkind: Addon\nmetadata:\n  name: rauthy\n")] +
			"  namespace: default\n" + rauthyAddonYAML[len("apiVersion: funcd.io/v1alpha1\nkind: Addon\nmetadata:\n  name: rauthy\n"):],
			"names no namespace"},
		"another kind": {"apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: x\nspec:\n  data:\n    k: v\n",
			"holds a ConfigMap"},
		"no kind": {runtimesOnlyAddonYAML, "addon.yaml: its Addon adds no kind"},
	} {
		dir := writeTemplate(t, baseApp, map[string]string{"resources/api.yaml": apiFragment, AddonFile: c.body})
		_, err := Load(dir)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.ErrorContains(t, err, c.want, name)
	}
}

// scenario: addon-template-render — render evaluates the App's fragments and never addon.yaml: the App alone, its
// addons part named after the App.
func TestScenarioAddonTemplateRender(t *testing.T) {
	t.Parallel()
	dir := writeTemplate(t, baseApp, map[string]string{"resources/api.yaml": apiFragment, "resources/login.yaml": loginFragment,
		AddonFile: rauthyAddonYAML})
	tpl, err := Load(dir)
	require.NoError(t, err)
	pin(t, tpl)
	for _, name := range []v1.ObjectName{"web-auth", "admin-auth"} {
		app, err := Render(tpl, RenderInput{Name: name, Namespace: "shop"})
		require.NoError(t, err)
		require.Len(t, app.Spec.Addons, 1)
		require.Equal(t, name+"-login", app.Spec.Addons[0].Name)
		require.Equal(t, "IdentityProvider", app.Spec.Addons[0].Kind)
	}
	require.Equal(t, "${{ values.publicHost }}", tpl.Addon.Spec.Runtimes[0].Attributes.Env["PUB_URL"], "render leaves the Addon as written")
}
