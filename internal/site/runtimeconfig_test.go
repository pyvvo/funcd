package site_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
)

// configKey is the generated config.json of Site name on the default prefix "bi" (ADR-0239 Decision 3).
func configKey(name string) string { return "bi/_runtime/" + name + "/config.json" }

func withConfig(values map[string]string, configMap v1.ObjectName) func(*v1.Site) {
	return func(s *v1.Site) { s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: values, ConfigMap: configMap} }
}

// putConfigMap creates the ConfigMap name with data, or replaces its data.
func (h *harness) putConfigMap(name string, data map[string]string) *v1.ConfigMap {
	h.t.Helper()
	if obj, err := h.st.Get(h.ctx, v1.KindConfigMap.GVK(), ns, v1.ObjectName(name)); err == nil {
		cm := obj.(*v1.ConfigMap)
		cm.Spec.Data = data
		out, uerr := h.st.Update(h.ctx, cm)
		require.NoError(h.t, uerr)
		return out.(*v1.ConfigMap)
	}
	out, err := h.st.Create(h.ctx, &v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: ns, ResourceGroup: "rg1"}, Spec: v1.ConfigMapSpec{Data: data}})
	require.NoError(h.t, err)
	return out.(*v1.ConfigMap)
}

func (h *harness) body(key string) string {
	h.t.Helper()
	b, err := h.shared.Get(h.ctx, "s3/"+string(ns)+"/"+bucket+"/"+key)
	require.NoError(h.t, err)
	return string(b)
}

// configRule finds Site name's config.json rule on rt by its key prefix; nil when rt has none.
func configRule(rt *v1.Route, name string) *v1.RouteRule {
	for i := range rt.Spec.Rules {
		if st := rt.Spec.Rules[i].Backend.Static; st != nil && strings.HasSuffix(st.Prefix, "/_runtime/"+name+"/") {
			return &rt.Spec.Rules[i]
		}
	}
	return nil
}

// The edits after a deploy change the Route's spec, so each one needs the Route programmed again.
func (h *harness) redeploy(name string) {
	h.t.Helper()
	h.reconcile(name)
	h.programRoute(name, true, "Programmed")
	h.reconcile(name)
}

// Decisions 3 and 7: the file is written under the Site's own key in the same pass that compiles its exact GET/HEAD
// rule, an index of its own prefix; the bundle rule and the serving digest are the bundle's; an unchanged body is not
// written again.
func TestSiteRuntimeConfigWrittenThenRouted(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	h.seedSite("bi", "v1", withConfig(map[string]string{"API_URL": "https://api.example"}, ""))

	h.reconcile("bi")
	require.Equal(t, `{"API_URL":"https://api.example"}`, h.body(configKey("bi")))
	rule := configRule(h.route("bi"), "bi")
	require.NotNil(t, rule, "the first Route already carries the rule")
	require.Equal(t, v1.RouteRule{Path: "/config.json", PathType: v1.PathTypeExact, Methods: []v1.HTTPMethod{v1.MethodGet, v1.MethodHead},
		Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: bucket, Prefix: "bi/_runtime/bi/", Index: "config.json", Public: true}}}, *rule)
	require.Contains(t, bundleRule(t, h.route("bi")).Prefix, slug(digest))

	h.programRoute("bi", true, "Programmed")
	h.reconcile("bi")
	s := h.site("bi")
	require.Equal(t, v1.PhaseReady, s.Status.Phase)
	require.Equal(t, digest, s.Status.Digest, "_runtime is never the serving digest")

	h.shared.reset()
	h.reconcile("bi")
	require.Empty(t, h.shared.puts, "an unchanged body is not written again")
}

// scenario: site-config-path-mounted (the reconciler half) — the rule moves with ingress.path.
func TestScenarioSiteConfigPathMountedRule(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", func(s *v1.Site) {
		withConfig(map[string]string{"API_URL": "https://api.example"}, "")(s)
		s.Spec.Ingress.Path = "/bi"
	})
	h.deploy("bi")
	require.Equal(t, "/bi/config.json", configRule(h.route("bi"), "bi").Path)
}

// Decision 1: the ConfigMap's keys and the values merge, a key in values winning.
func TestSiteRuntimeConfigValuesWinOverConfigMap(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.putConfigMap("web-config", map[string]string{"API_URL": "https://from.configmap", "FLAG": "on"})
	h.seedSite("bi", "v1", withConfig(map[string]string{"API_URL": "https://from.values"}, "web-config"))
	h.deploy("bi")
	require.Equal(t, `{"API_URL":"https://from.values","FLAG":"on"}`, h.body(configKey("bi")))
}

// scenario: site-config-configmap-change (the reconciler half) — a changed ConfigMap maps to the Sites that name it,
// and their next pass rewrites only the file.
func TestScenarioSiteConfigConfigMapChangeRewritesOnlyTheFile(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	h.putConfigMap("web-config", map[string]string{"FLAG": "off"})
	h.seedSite("bi", "v1", withConfig(nil, "web-config"))
	h.seedSite("plain", "v1", func(s *v1.Site) { s.Spec.Ingress.Host = "plain.example.com" })
	h.seedSite("other", "v1", func(s *v1.Site) {
		withConfig(nil, "other-config")(s)
		s.Spec.Ingress.Host = "other.example.com"
	})
	h.deploy("bi")

	cm := h.putConfigMap("web-config", map[string]string{"FLAG": "on"})
	require.Equal(t, []controller.Request{{GVK: v1.KindSite.GVK(), Namespace: ns, Name: "bi"}}, h.r.MapConfigMap(h.ctx, cm))
	h.shared.reset()
	h.reconcile("bi")
	require.Equal(t, []string{"s3/" + string(ns) + "/" + bucket + "/" + configKey("bi")}, h.shared.puts)
	require.Equal(t, `{"FLAG":"on"}`, h.body(configKey("bi")))
	s := h.site("bi")
	require.Equal(t, v1.PhaseReady, s.Status.Phase, "the Route's spec is unchanged")
	require.Equal(t, digest, s.Status.Digest)
}

// scenario: site-config-configmap-missing (the reconciler half) — a fresh Site serves its bundle without the rule;
// a serving Site keeps its last file and rule; both report ConfigMapNotFound until the ConfigMap exists.
func TestScenarioSiteConfigConfigMapMissing(t *testing.T) {
	t.Run("a fresh Site", func(t *testing.T) {
		h := newHarness(t)
		h.push("v1", siteA())
		h.seedSite("bi", "v1", withConfig(nil, "web-config"))
		h.redeploy("bi")
		c := readyCond(t, h.site("bi"))
		require.Equal(t, "ConfigMapNotFound", c.Reason)
		require.Contains(t, c.Message, `"web-config"`)
		require.Nil(t, configRule(h.route("bi"), "bi"), "no object, no rule")
		bundleRule(t, h.route("bi"))
		require.False(t, h.exists(configKey("bi")))

		h.putConfigMap("web-config", map[string]string{"FLAG": "on"})
		h.redeploy("bi")
		require.Equal(t, v1.PhaseReady, h.site("bi").Status.Phase)
		require.Equal(t, `{"FLAG":"on"}`, h.body(configKey("bi")))
		require.NotNil(t, configRule(h.route("bi"), "bi"))
	})
	t.Run("a Site serving its last file", func(t *testing.T) {
		h := newHarness(t)
		h.push("v1", siteA())
		h.seedSite("bi", "v1", withConfig(map[string]string{"API_URL": "https://api.example"}, ""))
		h.deploy("bi")
		h.apply("bi", withConfig(map[string]string{"API_URL": "https://api.example"}, "web-config"))
		h.reconcile("bi")
		require.Equal(t, "ConfigMapNotFound", readyCond(t, h.site("bi")).Reason)
		require.Equal(t, `{"API_URL":"https://api.example"}`, h.body(configKey("bi")), "nothing is rewritten")
		require.NotNil(t, configRule(h.route("bi"), "bi"), "the last good file keeps serving")
	})
}

// scenario: site-config-configmap-deleted (the reconciler half) — the deletion maps to the Site, which keeps its last
// file and reports ConfigMapNotFound.
func TestScenarioSiteConfigConfigMapDeleted(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	cm := h.putConfigMap("web-config", map[string]string{"FLAG": "on"})
	h.seedSite("bi", "v1", withConfig(nil, "web-config"))
	h.deploy("bi")
	require.NoError(t, h.st.Delete(h.ctx, v1.KindConfigMap.GVK(), ns, "web-config", ""))
	require.Equal(t, []controller.Request{{GVK: v1.KindSite.GVK(), Namespace: ns, Name: "bi"}}, h.r.MapConfigMap(h.ctx, cm))
	h.reconcile("bi")
	require.Equal(t, "ConfigMapNotFound", readyCond(t, h.site("bi")).Reason)
	require.Equal(t, `{"FLAG":"on"}`, h.body(configKey("bi")))
	require.NotNil(t, configRule(h.route("bi"), "bi"))
}

// Decision 7: a file over the cap, or one the Bucket refuses for its size, rewrites nothing and reports
// RuntimeConfigTooLarge, while the bundle still rolls out.
func TestSiteRuntimeConfigTooLarge(t *testing.T) {
	t.Run("over the cap", func(t *testing.T) {
		h := newHarness(t)
		h.push("v1", siteA())
		h.putConfigMap("web-config", map[string]string{"FLAG": "on"})
		h.seedSite("bi", "v1", withConfig(nil, "web-config"))
		h.deploy("bi")
		h.putConfigMap("web-config", map[string]string{"FLAG": "on", "BIG": strings.Repeat("x", v1.SiteRuntimeConfigMaxBytes)})
		h.reconcile("bi")
		c := readyCond(t, h.site("bi"))
		require.Equal(t, "RuntimeConfigTooLarge", c.Reason)
		require.Contains(t, c.Message, "32768-byte cap")
		require.Equal(t, `{"FLAG":"on"}`, h.body(configKey("bi")))
		require.NotNil(t, configRule(h.route("bi"), "bi"))
	})
	t.Run("over the Bucket's maxObjectBytes", func(t *testing.T) {
		h := newHarness(t)
		h.push("v1", siteA())
		pre := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
		pre.Name, pre.Namespace, pre.ResourceGroup = bucket, ns, "rg1"
		pre.Spec.MaxObjectBytes = 64
		_, err := h.st.Create(h.ctx, pre)
		require.NoError(t, err)
		h.seedSite("bi", "v1", withConfig(map[string]string{"API_URL": strings.Repeat("x", 80)}, ""))
		h.redeploy("bi")
		c := readyCond(t, h.site("bi"))
		require.Equal(t, "RuntimeConfigTooLarge", c.Reason)
		require.Contains(t, c.Message, "maxObjectBytes (64)")
		bundleRule(t, h.route("bi"))
		require.Nil(t, configRule(h.route("bi"), "bi"))
	})
}

// Decision 7: a Route reason wins over a config reason, since it decides whether anything is served
// (scenario site-config-route-conflict, the Site half).
func TestSiteRuntimeConfigRouteReasonWins(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", withConfig(nil, "web-config"))
	h.reconcile("bi")
	require.Equal(t, "RouteNotReady", readyCond(t, h.site("bi")).Reason)
	h.programRoute("bi", false, "RouteConflict")
	h.reconcile("bi")
	c := readyCond(t, h.site("bi"))
	require.Equal(t, "RouteNotReady", c.Reason)
	require.Contains(t, c.Message, "RouteConflict")
}

type failingDelete struct{ blob.Bucket }

func (failingDelete) Delete(context.Context, string) error {
	return fault.Unavailablef("test", "the substrate is down")
}

// scenario: site-config-removed (the reconciler half) — removal deletes the object before it drops the rule: while the
// delete fails the Route keeps the rule, so the next pass still knows to delete.
func TestScenarioSiteConfigRemovedDeletesThenDrops(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", withConfig(map[string]string{"API_URL": "https://api.example"}, ""))
	h.deploy("bi")
	h.apply("bi", func(s *v1.Site) { s.Spec.RuntimeConfig = nil })

	healthy := h.shared.Bucket
	h.shared.Bucket = failingDelete{healthy}
	_, err := h.r.Reconcile(h.ctx, controller.Request{GVK: v1.KindSite.GVK(), Namespace: ns, Name: "bi"})
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.NotNil(t, configRule(h.route("bi"), "bi"), "the rule stays until the object is gone")
	require.True(t, h.exists(configKey("bi")))

	h.shared.Bucket = healthy
	h.redeploy("bi")
	require.False(t, h.exists(configKey("bi")))
	require.Nil(t, configRule(h.route("bi"), "bi"))
	require.Equal(t, v1.PhaseReady, h.site("bi").Status.Phase)
}

// scenario: site-config-shared-prefix (the reconciler half) — two Sites on one Bucket and prefix keep their own files;
// removing runtimeConfig from one leaves the other's.
func TestScenarioSiteConfigSharedPrefix(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", withConfig(map[string]string{"ENV": "one"}, ""))
	h.seedSite("bi2", "v1", func(s *v1.Site) {
		withConfig(map[string]string{"ENV": "two"}, "")(s)
		s.Spec.Ingress.Host = "bi2.example.com"
	})
	h.deploy("bi")
	h.deploy("bi2")
	require.Equal(t, `{"ENV":"one"}`, h.body(configKey("bi")))
	require.Equal(t, `{"ENV":"two"}`, h.body(configKey("bi2")))

	h.apply("bi", func(s *v1.Site) { s.Spec.RuntimeConfig = nil })
	h.redeploy("bi")
	require.False(t, h.exists(configKey("bi")))
	require.Equal(t, `{"ENV":"two"}`, h.body(configKey("bi2")))
	require.NotNil(t, configRule(h.route("bi2"), "bi2"))
}
