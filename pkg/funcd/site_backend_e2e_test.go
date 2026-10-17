//go:build e2e

package funcd_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The ADR-0242 scenarios: Site fleet of staging has a build, a runtimeConfig, ingress.enabled false and ingress.path
// "/" unless a scenario says otherwise; the harness is the namespace-host one.

const fleetConfig = `{"API_URL":"https://api.example"}`

func offFleet(t *testing.T, layout, tag string) *v1.Site {
	t.Helper()
	s := fleetSite(t, layout, tag, v1.Ingress{Enabled: new(false), Path: "/"})
	s.Spec.RuntimeConfig = &v1.SiteRuntimeConfig{Values: map[string]string{"API_URL": "https://api.example"}}
	return s
}

// siteRoute is your Route on host with / → site (open) and, when api is set, /api → Function fleet-api (authenticated).
func siteRoute(name, host, path string, site v1.ObjectName, api bool) *v1.Route {
	r := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), "staging", "rg1"
	r.Spec = v1.RouteSpec{Host: host, Rules: []v1.RouteRule{{Path: path, Backend: v1.RouteBackend{Site: site}, Auth: &v1.RouteAuth{Mode: v1.AuthOpen}}}}
	if api {
		r.Spec.Rules = append(r.Spec.Rules, v1.RouteRule{Path: "/api", Backend: v1.RouteBackend{Function: "fleet-api"},
			Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}})
	}
	return r
}

func TestScenario_site_backend_app(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{offFleet(t, filepath.Join(t.TempDir(), "layout"), "v1"), fnObject("staging", "fleet-api"),
		siteRoute("app", "fleet.example.com", "/", "fleet", true)}, funcd.WithDevAuth(funcd.DevToken, "staging"))
	h.waitSiteReady("fleet")
	h.waitRoute("staging", "app", "Programmed")

	require.Contains(t, h.page("fleet.example.com", "/"), "<title>v1</title>")
	require.Contains(t, h.page("fleet.example.com", "/vehicles/42"), "<title>v1</title>", "the SPA fallback")
	require.Equal(t, fleetConfig, h.page("fleet.example.com", "/config.json"))
	code, _ := h.get(http.MethodGet, "fleet.example.com", "/api/vehicles", "")
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = h.call(http.MethodGet, "fleet.example.com", "/api/vehicles", "", funcd.DevToken)
	require.True(t, reachedOpen(code), "with a token the Function is reached, got %d", code)
}

func TestScenario_site_backend_follows_build(t *testing.T) {
	t.Parallel()
	layout := filepath.Join(t.TempDir(), "layout")
	h := startNSHost(t, []v1.Object{offFleet(t, layout, "v1"), siteRoute("app", "fleet.example.com", "/", "fleet", false)})
	s := h.waitSiteReady("fleet")
	h.waitRoute("staging", "app", "Programmed")
	require.Contains(t, h.page("fleet.example.com", "/"), "<title>v1</title>")

	v2, _ := pushSiteBundle(t, layout, "v2", map[string]string{"index.html": "<title>v2</title>"})
	s.Spec.Image = v2
	h.update(s)
	require.Eventually(t, func() bool {
		code, body := h.get(http.MethodGet, "fleet.example.com", "/", "")
		return code == http.StatusOK && strings.Contains(body, "<title>v2</title>")
	}, 20*time.Second, 50*time.Millisecond, "the Route serves the new build with no edit")
}

func TestScenario_site_backend_two_hosts(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{offFleet(t, filepath.Join(t.TempDir(), "layout"), "v1"),
		siteRoute("a", "a.example.com", "/", "fleet", false), siteRoute("b", "b.example.com", "/", "fleet", false)})
	h.waitSiteReady("fleet")
	h.waitRoute("staging", "a", "Programmed")
	h.waitRoute("staging", "b", "Programmed")
	require.Equal(t, h.page("a.example.com", "/"), h.page("b.example.com", "/"))

	attrs, err := h.blob.List(context.Background(), "s3/staging/reports/fleet/")
	require.NoError(t, err)
	folders := map[string]bool{}
	for _, a := range attrs {
		if rest := strings.TrimPrefix(a.Key, "s3/staging/reports/fleet/"); strings.HasPrefix(rest, "sha256-") {
			folders[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	require.Len(t, folders, 1, "one bundle folder serves both hosts")
}

func TestScenario_site_backend_missing(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{siteRoute("app", "fleet.example.com", "/", "ghost", false)})
	rt := h.waitRoute("staging", "app", "SiteNotFound")
	c, _ := rt.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, `"ghost"`)

	ghost := offFleet(t, filepath.Join(t.TempDir(), "layout"), "v1")
	ghost.Name = "ghost"
	_, err := h.st.Create(context.Background(), ghost)
	require.NoError(t, err)
	h.waitRoute("staging", "app", "Programmed")
	require.Contains(t, h.page("fleet.example.com", "/"), "<title>v1</title>")
}

func TestScenario_site_backend_no_build(t *testing.T) {
	t.Parallel()
	s := offFleet(t, filepath.Join(t.TempDir(), "layout"), "v1")
	s.Spec.Image = "oci-layout://" + filepath.Join(t.TempDir(), "empty") + ":v1"
	h := startNSHost(t, []v1.Object{s, siteRoute("app", "fleet.example.com", "/", "fleet", false)})
	h.waitRoute("staging", "app", "SiteNotReady")
}

func TestScenario_site_backend_deleted(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{offFleet(t, filepath.Join(t.TempDir(), "layout"), "v1"), siteRoute("app", "fleet.example.com", "/", "fleet", false)},
		funcd.WithPacing(funcd.Pacing{RouteResyncInterval: 300 * time.Millisecond, ReferentPollInterval: 300 * time.Millisecond}))
	h.waitSiteReady("fleet")
	h.waitRoute("staging", "app", "Programmed")

	require.NoError(t, h.st.Delete(context.Background(), v1.KindSite.GVK(), "staging", "fleet", ""))
	h.waitRoute("staging", "app", "SiteNotFound")
	require.Equal(t, http.StatusNotFound, h.status("fleet.example.com", "/"))
}

func TestScenario_site_backend_sub_path(t *testing.T) {
	t.Parallel()
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": relIndex})
	s := offFleet(t, filepath.Join(t.TempDir(), "layout2"), "v1")
	s.Spec.Image = ref
	s.Spec.Ingress.Path, s.Spec.Ingress.BaseHref = "/app", true
	h := startNSHost(t, []v1.Object{s, siteRoute("app", "fleet.example.com", "/app", "fleet", false)})
	h.waitSiteReady("fleet")
	h.waitRoute("staging", "app", "Programmed")

	require.Contains(t, h.page("fleet.example.com", "/app/vehicles/42"), `<base href="/app/">`)
	require.Equal(t, fleetConfig, h.page("fleet.example.com", "/app/config.json"))
}

func TestScenario_site_backend_invalid(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, nil)
	meta := func(name string) string {
		return `"metadata":{"name":"` + name + `","namespace":"default","resourceGroup":"rg1"}`
	}
	for name, body := range map[string]string{
		"site and function": `{"apiVersion":"funcd.io/v1alpha1","kind":"Route",` + meta("app") + `,"spec":{"rules":[{"path":"/","backend":{"site":"fleet","function":"fn"}}]}}`,
		"exact":             `{"apiVersion":"funcd.io/v1alpha1","kind":"Route",` + meta("app") + `,"spec":{"rules":[{"path":"/","pathType":"Exact","backend":{"site":"fleet"}}]}}`,
		"own name":          `{"apiVersion":"funcd.io/v1alpha1","kind":"Route",` + meta("fleet") + `,"spec":{"rules":[{"path":"/","backend":{"site":"fleet"}}]}}`,
	} {
		code, msg := h.post("routes", body)
		require.Equal(t, http.StatusBadRequest, code, "%s: %s", name, msg)
	}
}
