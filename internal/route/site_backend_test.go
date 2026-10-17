package route_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/internal/store"
)

const digestA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func seedSite(t *testing.T, st store.Store, name v1.ObjectName) *v1.Site {
	t.Helper()
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = name, "staging", "rg1"
	s.Spec = v1.SiteSpec{Image: "oci-layout:///x:v1", Bucket: v1.SiteBucket{Name: "web"}, Prefix: "web"}
	obj, err := st.Create(context.Background(), s)
	require.NoError(t, err)
	return obj.(*v1.Site)
}

// serveBuild writes the Site's own Route as the Site reconciler would: the bundle at its digest folder, and the
// config.json rule when config is true.
func serveBuild(t *testing.T, st store.Store, s *v1.Site, digest string, config bool) {
	t.Helper()
	rules := []v1.RouteRule{{Path: "/site/" + string(s.Name), Backend: v1.RouteBackend{Static: &v1.StaticBackend{
		Bucket: "web", Prefix: "web/" + strings.Replace(digest, ":", "-", 1) + "/", Index: "index.html", SPA: true}}}}
	if config {
		rules = append(rules, v1.RouteRule{Path: "/site/" + string(s.Name) + "/config.json", PathType: v1.PathTypeExact,
			Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "web", Prefix: s.RuntimeConfigPrefix(), Index: "config.json"}}})
	}
	_, err := owned.Ensure(context.Background(), st, s, s.Name, v1.RouteSpec{Rules: rules, Enabled: new(false)})
	require.NoError(t, err)
}

func seedSiteRoute(t *testing.T, st store.Store, site v1.ObjectName, path string) {
	t.Helper()
	seedFunction(t, st, "staging", "fleet-api")
	rt := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	rt.Name, rt.Namespace, rt.ResourceGroup = "app", "staging", "rg1"
	rt.Spec = v1.RouteSpec{Host: "fleet.example.com", Rules: []v1.RouteRule{
		{Path: path, Backend: v1.RouteBackend{Site: site}, Auth: &v1.RouteAuth{Mode: v1.AuthOpen}},
		{Path: "/api", Backend: v1.RouteBackend{Function: "fleet-api"}, Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}},
	}}
	_, err := st.Create(context.Background(), rt)
	require.NoError(t, err)
}

func readyMessage(t *testing.T, st store.Store, name v1.ObjectName) string {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindRoute.GVK(), "staging", name)
	require.NoError(t, err)
	c, _ := obj.(*v1.Route).Status.Conditions.Get("Ready")
	return c.Message
}

// ADR-0242 Decision 2: a site rule serves the Site's bundle and config.json at its own path with its own stance,
// beside the Route's written rules.
func TestSiteBackend_ServesTheBuildAndConfig(t *testing.T) {
	t.Parallel()
	_, st, rtr, rec := setup(t)
	s := seedSite(t, st, "fleet")
	serveBuild(t, st, s, digestA, true)
	seedSiteRoute(t, st, "fleet", "/")
	reconcile(t, rec, "staging", "app")
	status, _ := readyCond(t, st, "staging", "app")
	require.Equal(t, v1.ConditionTrue, status)

	m, ok := rtr.Resolve("fleet.example.com", "/vehicles/42", "GET")
	require.True(t, ok)
	require.Equal(t, "web/sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/", m.Static.Prefix)
	require.True(t, m.Static.SPA)
	require.Equal(t, v1.AuthOpen, m.Auth)
	m, ok = rtr.Resolve("fleet.example.com", "/config.json", "GET")
	require.True(t, ok)
	require.Equal(t, s.RuntimeConfigPrefix(), m.Static.Prefix)
	_, ok = rtr.Resolve("fleet.example.com", "/config.json", "POST")
	require.True(t, ok, "a POST falls to the bundle row, not the config row")
	m, ok = rtr.Resolve("fleet.example.com", "/api/vehicles", "GET")
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("fleet-api"), m.Function)
	require.Equal(t, v1.AuthAuthenticated, m.Auth)
}

// ADR-0242 Decision 4: no Site is SiteNotFound, a Site with no build is SiteNotReady, both naming it; a build makes
// the Route Ready, and a new build is served with no edit.
func TestSiteBackend_StatusesAndBuildChange(t *testing.T) {
	t.Parallel()
	_, st, rtr, rec := setup(t)
	seedSiteRoute(t, st, "ghost", "/")
	reconcile(t, rec, "staging", "app")
	status, reason := readyCond(t, st, "staging", "app")
	require.Equal(t, v1.ConditionFalse, status)
	require.Equal(t, "SiteNotFound", reason)
	require.Contains(t, readyMessage(t, st, "app"), `"ghost"`)
	_, ok := rtr.Resolve("fleet.example.com", "/api", "GET")
	require.False(t, ok, "the whole Route serves nothing")

	s := seedSite(t, st, "ghost")
	reconcile(t, rec, "staging", "app")
	_, reason = readyCond(t, st, "staging", "app")
	require.Equal(t, "SiteNotReady", reason)

	serveBuild(t, st, s, digestA, false)
	reconcile(t, rec, "staging", "app")
	status, _ = readyCond(t, st, "staging", "app")
	require.Equal(t, v1.ConditionTrue, status)
	_, ok = rtr.Resolve("fleet.example.com", "/config.json", "GET")
	require.True(t, ok)

	digestB := "sha256:" + strings.Repeat("b", 64)
	serveBuild(t, st, s, digestB, false)
	reconcile(t, rec, "staging", "app")
	m, ok := rtr.Resolve("fleet.example.com", "/", "GET")
	require.True(t, ok)
	require.Contains(t, m.Static.Prefix, strings.Repeat("b", 64))
}

// Only a Route the Site controls with its current UID is read (ADR-0242 Decision 3).
func TestSiteBackend_ForeignRouteIsNotRead(t *testing.T) {
	t.Parallel()
	_, st, _, rec := setup(t)
	s := seedSite(t, st, "fleet")
	older := *s
	older.UID = "an-older-incarnation"
	serveBuild(t, st, &older, digestA, false)
	seedSiteRoute(t, st, "fleet", "/")
	reconcile(t, rec, "staging", "app")
	_, reason := readyCond(t, st, "staging", "app")
	require.Equal(t, "SiteNotReady", reason)
}
