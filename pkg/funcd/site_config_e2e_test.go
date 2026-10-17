//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// configWithin is the "within 5 s" of ADR-0239's Scenarios; a first deploy pulls and unpacks the bundle, so it gets
// deployWithin.
const (
	configWithin = 5 * time.Second
	deployWithin = 20 * time.Second
)

// configEnv is a real funcd over a memory store and a mem:// blob substrate, with the process runtime and the
// embedded edge, and the dev token scoped to the test's namespaces.
type configEnv struct {
	st   store.Store
	blob blob.Bucket
	api  string
	dp   string
}

func startConfigPlatform(t *testing.T, namespaces []string, objs ...v1.Object) *configEnv {
	t.Helper()
	ctx := context.Background()
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	st := store.New(memory.New())
	for _, o := range objs {
		seed(t, st, o)
	}
	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(st), funcd.WithRuntime(process.New(nil)),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, namespaces...),
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &configEnv{st: st, blob: bucket, api: "http://" + p.Addr(), dp: "http://" + p.DataPlaneAddr()}
}

// configSite is a Site on Bucket web and prefix web with spa on; mutate sets its ingress and runtimeConfig.
func configSite(name string, ns v1.NamespaceName, ref string, mutate func(*v1.Site)) *v1.Site {
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	s.Spec = v1.SiteSpec{Image: ref, Bucket: v1.SiteBucket{Name: "web"}, Prefix: "web", SPA: true}
	mutate(s)
	return s
}

func configMapOf(name string, ns v1.NamespaceName, data map[string]string) *v1.ConfigMap {
	cm := &v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}}
	cm.Name, cm.Namespace, cm.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	cm.Spec.Data = data
	return cm
}

func (e *configEnv) site(t *testing.T, ns v1.NamespaceName, name string) *v1.Site {
	t.Helper()
	obj, err := e.st.Get(context.Background(), v1.KindSite.GVK(), ns, v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Site)
}

// waitSite waits until the Site's Ready condition, for its current generation, has the status and reason.
func (e *configEnv) waitSite(t *testing.T, ns v1.NamespaceName, name string, status v1.ConditionStatus, reason string, within time.Duration) *v1.Site {
	t.Helper()
	require.Eventually(t, func() bool {
		s := e.site(t, ns, name)
		c, ok := s.Status.Conditions.Get("Ready")
		return ok && c.Status == status && c.Reason == reason && c.ObservedGeneration == s.Generation
	}, within, 20*time.Millisecond, "site %s/%s Ready=%s %s; last %+v", ns, name, status, reason, e.site(t, ns, name).Status)
	return e.site(t, ns, name)
}

func (e *configEnv) update(t *testing.T, obj v1.Object) {
	t.Helper()
	_, err := e.st.Update(context.Background(), obj)
	require.NoError(t, err)
}

// generated reports whether the generated config.json of a configSite exists in the substrate.
func (e *configEnv) generated(t *testing.T, ns v1.NamespaceName, site string) bool {
	t.Helper()
	ok, err := e.blob.Exists(context.Background(), "s3/"+string(ns)+"/web/web/_runtime/"+site+"/config.json")
	require.NoError(t, err)
	return ok
}

// edgeGet sends a GET to the data plane dp with the host and headers, and returns the status, body and headers.
func edgeGet(t *testing.T, dp, host, path string, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, dp+path, nil)
	require.NoError(t, err)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode, string(body), resp.Header
}

// waitBody waits until GET host/path answers 200 with body.
func waitBody(t *testing.T, dp, host, path, body string, within time.Duration) {
	t.Helper()
	var last string
	require.Eventually(t, func() bool {
		code, got, _ := edgeGet(t, dp, host, path, nil)
		last = got
		return code == http.StatusOK && got == body
	}, within, 20*time.Millisecond, "GET %s%s answers %s; last %s", host, path, body, last)
}

const fleetIndex = "<!doctype html><title>fleet</title>"

// fleetTemplate is the files of an App template with one Site whose runtimeConfig takes API_URL from the values and
// the other keys from the App's ConfigMap fleet-flags (ADR-0239 Decision 5); values/e2e.yaml names registry.
func fleetTemplate(registry string) map[string]string {
	return map[string]string{"values/e2e.yaml": "registry: " + registry + "\n", "app/app.yaml": `name: fleet
version: 1.0.0
registry: ${{ values.registry }}
images:
  web: fleet-web:1.0.0
valuesSchema:
  type: object
  required:
    - registry
    - host
    - api
  properties:
    registry:
      type: string
    host:
      type: string
    api:
      type: object
      required:
        - url
      properties:
        url:
          type: string
    newDashboard:
      type: string
      default: "false"
`,
		"app/resources/site.yaml": `sites:
  - name: fleet-web
    image: ${{ images.web }}
    bucket:
      name: fleet-web
    prefix: web
    spa: true
    ingress:
      host: ${{ values.host }}
      public: true
    runtimeConfig:
      values:
        API_URL: ${{ values.api.url }}
      configMap: fleet-flags
`,
		"app/resources/flags.yaml": `configMaps:
  - name: fleet-flags
    data:
      NEW_DASHBOARD: ${{ values.newDashboard }}
`,
		"values/staging.yaml": `host: fleet.staging.test
api:
  url: https://api.staging.example
`,
		"values/prod.yaml": `host: fleet.prod.test
api:
  url: https://api.example
`,
		"values/new-dashboard.yaml": `newDashboard: "true"
`,
	}
}

// scenarios: site-config-two-environments + site-config-revalidates + site-config-ahead-of-spa +
// site-config-app-configmap (e2e, F152/ADR-0239) — one bundle pushed once and one App template deployed into staging
// and prod: each Site serves its own config.json from the values and the App's ConfigMap, both serve the bundle's one
// digest and bytes; config.json is no-cache JSON with an ETag that answers 304 and never the SPA's index; a new value
// of the App's ConfigMap reaches config.json, and the App keeps the old ConfigMap while another Site names it.
func TestScenarioE2ESiteConfigTemplate(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithDevAuth(funcd.DevToken, "staging", "prod"))
	cli := funcdctl(t, e)
	dir, reg := t.TempDir(), shortDataDir(t)
	for name, body := range fleetTemplate("oci-layout://" + reg) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	ref, digest := pushSiteBundle(t, filepath.Join(reg, "fleet-web"), "1.0.0", map[string]string{"index.html": fleetIndex})
	app := filepath.Join(dir, "app")
	values := func(name string) string { return filepath.Join(dir, "values", name+".yaml") }
	out, err := cli("app", "lock", app, "-f", values("e2e"))
	require.NoError(t, err, out)
	deploy := func(ns string, extra ...string) {
		t.Helper()
		out, err := cli(append([]string{"app", "deploy", app, "--name", "fleet", "-n", ns, "-f", values("e2e"), "-f", values(ns)}, extra...)...)
		require.NoError(t, err, out)
	}
	deploy("staging")
	deploy("prod")
	site := func(ns v1.NamespaceName, name v1.ObjectName) *v1.Site {
		t.Helper()
		obj, err := e.c.Get(e.ctx, v1.KindSite, ns, name)
		require.NoError(t, err)
		return obj.(*v1.Site)
	}
	require.Equal(t, digest, site("staging", "fleet-web").Status.Digest)
	require.Equal(t, digest, site("prod", "fleet-web").Status.Digest)
	flagsOff := v1.AppConfigMapName("fleet-flags", v1.ConfigMapSpec{Data: map[string]string{"NEW_DASHBOARD": "false"}})
	require.Equal(t, flagsOff, site("staging", "fleet-web").Spec.RuntimeConfig.ConfigMap, "the App points its Site at the stored name")

	code, body, hdr := edgeGet(t, e.dp, "fleet.staging.test", "/config.json", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, `{"API_URL":"https://api.staging.example","NEW_DASHBOARD":"false"}`, body)
	require.Equal(t, "application/json", hdr.Get("Content-Type"))
	require.Equal(t, "no-cache", hdr.Get("Cache-Control"))
	etag := hdr.Get("ETag")
	require.True(t, strings.HasPrefix(etag, `W/"`), "etag %q", etag)
	code, _, _ = edgeGet(t, e.dp, "fleet.staging.test", "/config.json", map[string]string{"If-None-Match": etag})
	require.Equal(t, http.StatusNotModified, code)
	code, body, _ = edgeGet(t, e.dp, "fleet.prod.test", "/config.json", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, `{"API_URL":"https://api.example","NEW_DASHBOARD":"false"}`, body)

	for _, host := range []string{"fleet.staging.test", "fleet.prod.test"} {
		code, body, _ = edgeGet(t, e.dp, host, "/", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, fleetIndex, body, "%s serves the bundle's bytes", host)
	}
	code, body, _ = edgeGet(t, e.dp, "fleet.staging.test", "/vehicles/42", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, fleetIndex, body, "an app path is the SPA's")

	audit := configSite("audit", "staging", ref, func(s *v1.Site) {
		s.Spec.Bucket.Name = "audit"
		s.Spec.Ingress = v1.SiteIngress{Host: "audit.staging.test", Public: true}
		s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{ConfigMap: flagsOff}
	})
	_, err = e.c.Apply(e.ctx, audit)
	require.NoError(t, err)
	waitBody(t, e.dp, "audit.staging.test", "/config.json", `{"NEW_DASHBOARD":"false"}`, deployWithin)

	deploy("staging", "-f", values("new-dashboard"))
	waitBody(t, e.dp, "fleet.staging.test", "/config.json", `{"API_URL":"https://api.staging.example","NEW_DASHBOARD":"true"}`, configWithin)
	require.Equal(t, digest, site("staging", "fleet-web").Status.Digest)
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindApp, "staging", "fleet")
		require.NoError(t, err)
		for _, c := range obj.(*v1.App).Status.Children {
			if c.Kind == v1.KindConfigMap && c.Name == flagsOff {
				return c.State == v1.AppChildPruning && c.Reason == "InUse: Site/audit"
			}
		}
		return false
	}, configWithin, 20*time.Millisecond, "the App keeps %s while Site/audit names it", flagsOff)
	waitBody(t, e.dp, "audit.staging.test", "/config.json", `{"NEW_DASHBOARD":"false"}`, configWithin)
}

// scenarios: site-config-configmap-missing + site-config-configmap-change (e2e, F152/ADR-0239) — a Site that names a missing ConfigMap is Ready=False ConfigMapNotFound while its last file keeps
// serving; once created, the ConfigMap's keys join config.json, values winning; a changed key reaches config.json with
// a new ETag and the same bundle digest; the ConfigMap deleted, the Site is ConfigMapNotFound and its last file serves.
func TestScenarioE2ESiteConfigConfigMap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref, digest := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": fleetIndex})
	e := startConfigPlatform(t, []string{"staging"}, configSite("fleet", "staging", ref, func(s *v1.Site) {
		s.Spec.Ingress = v1.SiteIngress{Host: "fleet.test", Public: true}
		s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}}
	}))
	e.waitSite(t, "staging", "fleet", v1.ConditionTrue, "Materialized", deployWithin)

	s := e.site(t, "staging", "fleet")
	s.Spec.RuntimeConfig.ConfigMap = "fleet-flags"
	e.update(t, s)
	e.waitSite(t, "staging", "fleet", v1.ConditionFalse, "ConfigMapNotFound", configWithin)
	waitBody(t, e.dp, "fleet.test", "/config.json", `{"API_URL":"https://api.example"}`, configWithin)
	waitBody(t, e.dp, "fleet.test", "/", fleetIndex, configWithin)

	_, err := e.st.Create(ctx, configMapOf("fleet-flags", "staging", map[string]string{"NEW_DASHBOARD": "false", "API_URL": "https://ignored.example"}))
	require.NoError(t, err)
	e.waitSite(t, "staging", "fleet", v1.ConditionTrue, "Materialized", configWithin)
	waitBody(t, e.dp, "fleet.test", "/config.json", `{"API_URL":"https://api.example","NEW_DASHBOARD":"false"}`, configWithin)
	_, _, hdr := edgeGet(t, e.dp, "fleet.test", "/config.json", nil)
	etag := hdr.Get("ETag")

	obj, err := e.st.Get(ctx, v1.KindConfigMap.GVK(), "staging", "fleet-flags")
	require.NoError(t, err)
	cm := obj.(*v1.ConfigMap)
	cm.Spec.Data["NEW_DASHBOARD"] = "true"
	e.update(t, cm)
	waitBody(t, e.dp, "fleet.test", "/config.json", `{"API_URL":"https://api.example","NEW_DASHBOARD":"true"}`, configWithin)
	_, _, hdr = edgeGet(t, e.dp, "fleet.test", "/config.json", nil)
	require.NotEqual(t, etag, hdr.Get("ETag"))
	require.Equal(t, digest, e.site(t, "staging", "fleet").Status.Digest)

	require.NoError(t, e.st.Delete(ctx, v1.KindConfigMap.GVK(), "staging", "fleet-flags", ""))
	e.waitSite(t, "staging", "fleet", v1.ConditionFalse, "ConfigMapNotFound", configWithin)
	waitBody(t, e.dp, "fleet.test", "/config.json", `{"API_URL":"https://api.example","NEW_DASHBOARD":"true"}`, configWithin)
	waitBody(t, e.dp, "fleet.test", "/", fleetIndex, configWithin)
}

// scenario: site-config-path-mounted (e2e, F152/ADR-0239) — a Site mounted at /bi serves /bi/config.json and no
// /config.json.
func TestScenarioE2ESiteConfigPathMounted(t *testing.T) {
	t.Parallel()
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": fleetIndex})
	e := startConfigPlatform(t, []string{"openteam"}, configSite("bi", "openteam", ref, func(s *v1.Site) {
		s.Spec.Ingress = v1.SiteIngress{Path: "/bi", Public: true}
		s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}}
	}))
	e.waitSite(t, "openteam", "bi", v1.ConditionTrue, "Materialized", deployWithin)
	waitBody(t, e.dp, "openteam.localhost", "/bi/config.json", `{"API_URL":"https://api.example"}`, configWithin)
	code, _, _ := edgeGet(t, e.dp, "openteam.localhost", "/config.json", nil)
	require.Equal(t, http.StatusNotFound, code)
}

// scenarios: site-config-shadows-bundle-file + site-config-removed (e2e, F152/ADR-0239) — the generated file shadows
// the bundle's own root config.json; removing runtimeConfig deletes the generated object, and /config.json answers
// the bundle's file again.
func TestScenarioE2ESiteConfigShadowsThenRemoved(t *testing.T) {
	t.Parallel()
	const baked = `{"API_URL":"https://baked-in.example"}`
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": fleetIndex, "config.json": baked})
	e := startConfigPlatform(t, []string{"staging"}, configSite("fleet", "staging", ref, func(s *v1.Site) {
		s.Spec.Ingress = v1.SiteIngress{Host: "fleet.test", Public: true}
		s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}}
	}))
	e.waitSite(t, "staging", "fleet", v1.ConditionTrue, "Materialized", deployWithin)
	waitBody(t, e.dp, "fleet.test", "/config.json", `{"API_URL":"https://api.example"}`, configWithin)
	require.True(t, e.generated(t, "staging", "fleet"))

	s := e.site(t, "staging", "fleet")
	s.Spec.RuntimeConfig = nil
	e.update(t, s)
	waitBody(t, e.dp, "fleet.test", "/config.json", baked, configWithin)
	require.False(t, e.generated(t, "staging", "fleet"), "the generated object is deleted")
	e.waitSite(t, "staging", "fleet", v1.ConditionTrue, "Materialized", configWithin)
}

// scenario: site-config-route-conflict (e2e, F152/ADR-0239) — a Route that claims GET /config.json on a Site's host
// conflicts with the Site's rule: when it sorts first, the Site's Route is RouteConflict and the Site RouteNotReady;
// when it sorts after, it is that Route that is RouteConflict, and the Site serves its file.
func TestScenarioE2ESiteConfigRouteConflict(t *testing.T) {
	t.Parallel()
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": fleetIndex})
	claim := func(ns v1.NamespaceName, name, host string) *v1.Route {
		rt := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
		rt.Name, rt.Namespace, rt.ResourceGroup = v1.ObjectName(name), ns, "rg1"
		rt.Spec = v1.RouteSpec{Host: host, Rules: []v1.RouteRule{{Path: "/config.json", Methods: []v1.HTTPMethod{v1.MethodGet},
			Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "web", Prefix: "other/", Public: true}}}}}
		return rt
	}
	site := func(ns v1.NamespaceName, host string) *v1.Site {
		return configSite("fleet", ns, ref, func(s *v1.Site) {
			s.Spec.Ingress = v1.SiteIngress{Host: host, Public: true}
			s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}}
		})
	}
	e := startConfigPlatform(t, []string{"early", "late"},
		claim("early", "a-config", "fleet.early.test"), site("early", "fleet.early.test"),
		site("late", "fleet.late.test"), claim("late", "z-config", "fleet.late.test"))

	require.Eventually(t, func() bool {
		c, ok := e.site(t, "early", "fleet").Status.Conditions.Get("Ready")
		return ok && c.Reason == "RouteNotReady" && strings.Contains(c.Message, "RouteConflict")
	}, deployWithin, 20*time.Millisecond, "the Site's Route sorts after a-config")

	e.waitSite(t, "late", "fleet", v1.ConditionTrue, "Materialized", deployWithin)
	waitBody(t, e.dp, "fleet.late.test", "/config.json", `{"API_URL":"https://api.example"}`, configWithin)
	require.Eventually(t, func() bool {
		obj, err := e.st.Get(context.Background(), v1.KindRoute.GVK(), "late", "z-config")
		require.NoError(t, err)
		c, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionFalse && c.Reason == "RouteConflict"
	}, configWithin, 20*time.Millisecond, "z-config sorts after the Site's Route")
}

// scenario: site-config-shared-prefix (e2e, F152/ADR-0239) — two Sites on one Bucket and prefix serve their own
// config.json; removing runtimeConfig from one leaves the other's file.
func TestScenarioE2ESiteConfigSharedPrefix(t *testing.T) {
	t.Parallel()
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": fleetIndex})
	site := func(name, env string) *v1.Site {
		return configSite(name, "staging", ref, func(s *v1.Site) {
			s.Spec.Ingress = v1.SiteIngress{Host: name + ".test", Public: true}
			s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: map[string]string{"ENV": env}}
		})
	}
	e := startConfigPlatform(t, []string{"staging"}, site("one", "first"), site("two", "second"))
	e.waitSite(t, "staging", "one", v1.ConditionTrue, "Materialized", deployWithin)
	e.waitSite(t, "staging", "two", v1.ConditionTrue, "Materialized", deployWithin)
	waitBody(t, e.dp, "one.test", "/config.json", `{"ENV":"first"}`, configWithin)
	waitBody(t, e.dp, "two.test", "/config.json", `{"ENV":"second"}`, configWithin)

	s := e.site(t, "staging", "one")
	s.Spec.RuntimeConfig = nil
	e.update(t, s)
	require.Eventually(t, func() bool { return !e.generated(t, "staging", "one") }, configWithin, 20*time.Millisecond)
	waitBody(t, e.dp, "one.test", "/config.json", fleetIndex, configWithin)
	require.True(t, e.generated(t, "staging", "two"))
	waitBody(t, e.dp, "two.test", "/config.json", `{"ENV":"second"}`, configWithin)
}

// scenario: site-config-refused (e2e, F152/ADR-0239) — the API refuses with 400 a key that is not an identifier, an
// ingress.rules path equal to the file's path with or without a trailing slash, and values that render over the
// 32 KiB cap, naming each.
func TestScenarioE2ESiteConfigRefused(t *testing.T) {
	t.Parallel()
	e := startConfigPlatform(t, []string{"openteam"})
	post := func(spec string) (int, string) {
		t.Helper()
		body := `{"apiVersion":"funcd.io/v1alpha1","kind":"Site","metadata":{"name":"web","namespace":"openteam","resourceGroup":"rg1"},"spec":` + spec + `}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.api+"/apis/funcd.io/v1alpha1/namespaces/openteam/sites", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		msg, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode, string(msg)
	}
	const base = `"image":"registry.example/web:1","bucket":{"name":"web"},"prefix":"web","ingress":{"host":"web.test"`
	for _, tc := range []struct{ spec, want string }{
		{`{` + base + `},"runtimeConfig":{"values":{"api-url":"x"}}}`, "api-url"},
		{`{` + base + `,"rules":[{"path":"/config.json","prefix":"data"}]},"runtimeConfig":{}}`, `/config.json\"`},
		{`{` + base + `,"rules":[{"path":"/config.json/","prefix":"data"}]},"runtimeConfig":{}}`, `/config.json/\"`},
		{`{` + base + `},"runtimeConfig":{"values":{"BIG":"` + strings.Repeat("x", v1.SiteRuntimeConfigMaxBytes) + `"}}}`, strconv.Itoa(v1.SiteRuntimeConfigMaxBytes)},
	} {
		code, msg := post(tc.spec)
		require.Equal(t, http.StatusBadRequest, code, msg)
		require.Contains(t, msg, tc.want)
	}
}

// relIndex is the index of a bundle built with relative URLs (Vite base './') that carries its own base, as an Angular
// build's does.
const relIndex = `<!doctype html><html><head><base href="/"><title>fleet</title><script type="module" src="assets/app.js"></script></head><body></body></html>`

func baseHrefSite(name string, ns v1.NamespaceName, ref string, ingress v1.SiteIngress, rc *v1.SiteRuntimeConfig) *v1.Site {
	return configSite(name, ns, ref, func(s *v1.Site) {
		ingress.BaseHref, ingress.Public = true, true
		s.Spec.Ingress, s.Spec.RuntimeConfig = ingress, rc
	})
}

// scenarios: site-config-base-href + site-config-one-digest-two-mounts (e2e, F152/ADR-0239 Decision 10) — one bundle
// digest, built with relative URLs and carrying <base href="/">, on two Sites of one Bucket and prefix, at / and at /bi:
// a direct deep link on each serves the index with exactly one base, its own, no-cache; the entry script and
// config.json resolve under the mount; no bundle file changes.
func TestScenarioE2ESiteConfigBaseHref(t *testing.T) {
	t.Parallel()
	ref, digest := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": relIndex, "assets/app.js": "export {};"})
	rc := &v1.SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}}
	e := startConfigPlatform(t, []string{"staging"},
		baseHrefSite("root", "staging", ref, v1.SiteIngress{Host: "fleet.test"}, rc),
		baseHrefSite("bi", "staging", ref, v1.SiteIngress{Path: "/bi"}, rc))
	require.Equal(t, digest, e.waitSite(t, "staging", "root", v1.ConditionTrue, "Materialized", deployWithin).Status.Digest)
	require.Equal(t, digest, e.waitSite(t, "staging", "bi", v1.ConditionTrue, "Materialized", deployWithin).Status.Digest)

	for _, tc := range []struct{ host, mount string }{{"fleet.test", "/"}, {"staging.localhost", "/bi/"}} {
		code, body, hdr := edgeGet(t, e.dp, tc.host, tc.mount+"devices/42", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, body, `<head><base href="`+tc.mount+`"><title>fleet</title>`)
		require.Equal(t, 1, strings.Count(body, "<base "), "exactly one base")
		require.Equal(t, "no-cache", hdr.Get("Cache-Control"))
		code, body, hdr = edgeGet(t, e.dp, tc.host, tc.mount+"assets/app.js", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "export {};", body)
		require.Contains(t, hdr.Get("Content-Type"), "javascript")
		waitBody(t, e.dp, tc.host, tc.mount+"config.json", `{"API_URL":"https://api.example"}`, configWithin)
	}
	stored, err := e.blob.Get(context.Background(), "s3/staging/web/web/"+strings.ReplaceAll(digest, ":", "-")+"/index.html")
	require.NoError(t, err)
	require.Equal(t, relIndex, string(stored), "no bundle file changes")
}

// scenario: site-config-base-href-paths (e2e, F152/ADR-0239 Decision 10) — with baseHref and no runtimeConfig, a
// sub-directory and config.json answer the bundle's own files; a new ingress.path moves the base within 5 s.
func TestScenarioE2ESiteConfigBaseHrefPaths(t *testing.T) {
	t.Parallel()
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{
		"index.html": relIndex, "docs/index.html": "docs", "config.json": `{"OWN":"1"}`,
	})
	e := startConfigPlatform(t, []string{"staging"}, baseHrefSite("bi", "staging", ref, v1.SiteIngress{Path: "/bi"}, nil))
	e.waitSite(t, "staging", "bi", v1.ConditionTrue, "Materialized", deployWithin)
	waitBody(t, e.dp, "staging.localhost", "/bi/docs/", "docs", configWithin)
	waitBody(t, e.dp, "staging.localhost", "/bi/config.json", `{"OWN":"1"}`, configWithin)

	s := e.site(t, "staging", "bi")
	s.Spec.Ingress.Path = "/b2"
	e.update(t, s)
	require.Eventually(t, func() bool {
		code, body, _ := edgeGet(t, e.dp, "staging.localhost", "/b2/devices/42", nil)
		return code == http.StatusOK && strings.Contains(body, `<base href="/b2/">`)
	}, configWithin, 20*time.Millisecond, "the base moves with the mount")
}

// scenario: site-config-runtime-reserved (e2e, F152/ADR-0239 Decision 10) — a bundle with a root _runtime entry is
// MaterializeFailed, naming it, and the previous digest keeps serving.
func TestScenarioE2ESiteConfigRuntimeReserved(t *testing.T) {
	t.Parallel()
	layout := filepath.Join(t.TempDir(), "layout")
	ref1, digest1 := pushSiteBundle(t, layout, "v1", map[string]string{"index.html": fleetIndex})
	ref2, _ := pushSiteBundle(t, layout, "v2", map[string]string{"index.html": "<title>v2</title>", "_runtime/x.txt": "x"})
	e := startConfigPlatform(t, []string{"staging"}, configSite("fleet", "staging", ref1, func(s *v1.Site) {
		s.Spec.Ingress = v1.SiteIngress{Host: "fleet.test", Public: true}
	}))
	e.waitSite(t, "staging", "fleet", v1.ConditionTrue, "Materialized", deployWithin)
	s := e.site(t, "staging", "fleet")
	s.Spec.Image = ref2
	e.update(t, s)
	s = e.waitSite(t, "staging", "fleet", v1.ConditionFalse, "MaterializeFailed", configWithin)
	c, _ := s.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, `"_runtime/x.txt"`)
	require.Equal(t, digest1, s.Status.Digest)
	waitBody(t, e.dp, "fleet.test", "/", fleetIndex, configWithin)
}
