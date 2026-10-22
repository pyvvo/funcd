//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The ADR-0241 scenarios reuse the namespace-host harness (namespace_host_e2e_test.go): a reached Function of an open
// namespace answers a 5xx (it is never deployed), a refused one 401 or 404.

func withIngress(fn *v1.Function, ing *v1.Ingress) *v1.Function {
	fn.Spec.Ingress = ing
	return fn
}

// fleetSite is Site fleet of staging over a pushed bundle whose index reads "<tag>".
func fleetSite(t *testing.T, layout, tag string, ing v1.Ingress) *v1.Site {
	t.Helper()
	ref, _ := pushSiteBundle(t, layout, tag, map[string]string{"index.html": "<title>" + tag + "</title>"})
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = "fleet", "staging", "rg1"
	s.Spec = v1.SiteSpec{Image: ref, Buckets: []v1.BucketEntry{{Name: "reports"}}, Bindings: v1.SiteBindings{Blob: []v1.BlobRef{{Bucket: "reports", Prefix: "fleet"}}}, SPA: true, Ingress: v1.SiteIngress{Ingress: ing}}
	return s
}

func (h *nsHost) waitSiteReady(name v1.ObjectName) *v1.Site {
	h.t.Helper()
	var s *v1.Site
	require.Eventually(h.t, func() bool {
		obj, err := h.st.Get(context.Background(), v1.KindSite.GVK(), "staging", name)
		if err != nil {
			return false
		}
		s = obj.(*v1.Site)
		return s.Status.Phase == v1.PhaseReady && s.Status.ObservedGeneration == s.Generation
	}, 20*time.Second, 50*time.Millisecond, "site %s Ready", name)
	return s
}

func (h *nsHost) update(obj v1.Object) {
	h.t.Helper()
	_, err := h.st.Update(context.Background(), obj)
	require.NoError(h.t, err)
}

// post applies a raw JSON object through the control plane and returns the status code and body.
func (h *nsHost) post(plural string, body string) (int, string) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.api+"/apis/funcd.io/v1alpha1/namespaces/default/"+plural, strings.NewReader(body))
	require.NoError(h.t, err)
	req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	msg, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(msg)
}

func TestScenario_exposure_function_default(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{fnObject("staging", "fleet-api")})
	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.True(t, reachedOpen(code), "got %d", code)
}

func TestScenario_exposure_function_off(t *testing.T) {
	t.Parallel()
	rt := pageRoute("staging", "yours", "", "/yours")
	rt.Spec.Rules[0].Backend = v1.RouteBackend{Function: "fleet-api"}
	h := startNSHost(t, []v1.Object{withIngress(fnObject("staging", "fleet-api"), &v1.Ingress{Enabled: new(false)}), rt})
	h.waitRoute("staging", "yours", "Programmed")

	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.Equal(t, http.StatusNotFound, code)
	code, _ = h.get(http.MethodPost, "staging.localhost", "/yours", "")
	require.True(t, reachedOpen(code), "a Route you write still serves it, got %d", code)
}

func TestScenario_exposure_explicit(t *testing.T) {
	t.Parallel()
	layout := filepath.Join(t.TempDir(), "layout")
	off := fleetSite(t, layout, "off", v1.Ingress{})
	on := fleetSite(t, layout, "on", v1.Ingress{Enabled: new(true)})
	on.Name, on.Spec.Bindings.Blob[0].Prefix = "fleet-on", "fleet-on"
	on.Spec.Buckets[0].Name, on.Spec.Bindings.Blob[0].Bucket = "reports-on", "reports-on" // a Bucket of its own (ADR-0248)
	api := pageRoute("staging", "todo-api", "", "/todo")
	api.Spec.Rules[0].Backend = v1.RouteBackend{Function: "todo-api"}
	h := startNSHost(t, []v1.Object{
		explicitNS("staging"), off, on, api,
		fnObject("staging", "closed"), withIngress(fnObject("staging", "opened"), &v1.Ingress{Enabled: new(true)}), fnObject("staging", "todo-api"),
	})
	h.waitSiteReady("fleet")
	h.waitSiteReady("fleet-on")
	h.waitRoute("staging", "fleet", "Disabled")
	h.waitRoute("staging", "todo-api", "Programmed")

	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/closed", "")
	require.Equal(t, http.StatusNotFound, code)
	code, _ = h.get(http.MethodPost, "staging.localhost", "/function/opened", "")
	require.True(t, reachedOpen(code), "got %d", code)
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/site/fleet/"))
	require.Contains(t, h.page("staging.localhost", "/site/fleet-on/"), "<title>on</title>")
	code, _ = h.get(http.MethodPost, "staging.localhost", "/todo", "")
	require.True(t, reachedOpen(code), "the Route an App deploys is not owned, so it serves, got %d", code)
}

func TestScenario_exposure_mode_switch(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{nsObject("staging", nil), fnObject("staging", "fleet-api"),
		fleetSite(t, filepath.Join(t.TempDir(), "layout"), "v1", v1.Ingress{})},
		funcd.WithPacing(funcd.Pacing{RouteResyncInterval: 300 * time.Millisecond, ReferentPollInterval: 300 * time.Millisecond}))
	h.waitSiteReady("fleet")
	h.waitRoute("staging", "fleet", "Programmed")
	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.True(t, reachedOpen(code), "got %d", code)

	obj, err := h.st.Get(context.Background(), v1.KindNamespace.GVK(), "", "staging")
	require.NoError(t, err)
	ns := obj.(*v1.Namespace)
	ns.Spec.DefaultExposure = v1.ExposureExplicit
	h.update(ns)

	code, _ = h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.Equal(t, http.StatusNotFound, code, "by name at once")
	h.waitRoute("staging", "fleet", "Disabled")
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/site/fleet/"))
}

func TestScenario_exposure_function_address(t *testing.T) {
	t.Parallel()
	ing := &v1.Ingress{Host: "api.example.com", Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}}
	h := startNSHost(t, []v1.Object{withIngress(fnObject("staging", "fleet-api"), ing)})
	name := string(owned.FunctionRouteName("fleet-api"))
	rt := h.waitRoute("staging", name, "Programmed")
	require.Equal(t, v1.AuthAuthenticated, rt.Spec.Auth.Mode)

	code, _ := h.get(http.MethodPost, "api.example.com", "/", "")
	require.Equal(t, http.StatusUnauthorized, code, "the owned Route serves with the Function's stance")
	code, _ = h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.Equal(t, http.StatusNotFound, code, "a Function with a host is served only by its Route")

	obj, err := h.st.Get(context.Background(), v1.KindFunction.GVK(), "staging", "fleet-api")
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	fn.Spec.Ingress = nil
	h.update(fn)
	require.Eventually(t, func() bool {
		_, gerr := h.st.Get(context.Background(), v1.KindRoute.GVK(), "staging", v1.ObjectName(name))
		return gerr != nil
	}, 15*time.Second, 50*time.Millisecond, "dropping the host deletes the Route")
}

func TestScenario_exposure_function_auth(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		withIngress(fnObject("staging", "fleet-api"), &v1.Ingress{Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated}}),
		authenticatedNS("secure"), withIngress(fnObject("secure", "closed"), &v1.Ingress{Enabled: new(false)}),
	}, funcd.WithDevAuth(funcd.DevToken, "staging"))

	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = h.call(http.MethodPost, "staging.localhost", "/function/fleet-api", "", funcd.DevToken)
	require.True(t, reachedOpen(code), "a bearer call passes the PEP, got %d", code)

	for _, fn := range []string{"missing", "closed"} {
		code, _ = h.get(http.MethodPost, "secure.localhost", "/function/"+fn, "")
		require.Equal(t, http.StatusUnauthorized, code, "%s in an authenticated namespace", fn)
	}
	code, _ = h.get(http.MethodPost, "staging.localhost", "/function/missing", "")
	require.Equal(t, http.StatusNotFound, code, "a missing Function in an open namespace")
}

func TestScenario_exposure_site_off(t *testing.T) {
	t.Parallel()
	layout := filepath.Join(t.TempDir(), "layout")
	s := fleetSite(t, layout, "v1", v1.Ingress{Enabled: new(false)})
	h := startNSHost(t, []v1.Object{s})
	first := h.waitSiteReady("fleet")
	rt := h.waitRoute("staging", "fleet", "Disabled")
	require.NotNil(t, rt.Spec.Enabled)
	require.False(t, *rt.Spec.Enabled)
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/site/fleet/"))

	v2, _ := pushSiteBundle(t, layout, "v2", map[string]string{"index.html": "<title>v2</title>"})
	first.Spec.Image = v2
	h.update(first)
	require.Eventually(t, func() bool {
		r := h.route("staging", "fleet")
		s := h.waitSiteReady("fleet")
		for _, rule := range r.Spec.Rules {
			if rule.Backend.Static != nil && rule.Backend.Static.Prefix == s.Status.ServingPrefix && s.Status.Digest != first.Status.Digest {
				return true
			}
		}
		return false
	}, 20*time.Second, 100*time.Millisecond, "a new build moves the bundle rule")
}

func TestScenario_exposure_route_off(t *testing.T) {
	t.Parallel()
	rt := pageRoute("staging", "web", "", "/")
	rt.Spec.Enabled = new(false)
	h := startNSHost(t, []v1.Object{rt})
	require.Equal(t, "staging.localhost", h.waitRoute("staging", "web", "Disabled").Status.Host)
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/"))
}

func TestScenario_exposure_your_route_wins(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		fleetSite(t, filepath.Join(t.TempDir(), "layout"), "v1", v1.Ingress{Host: "fleet.example.com"}),
		pageRoute("staging", "zz-app", "fleet.example.com", "/"),
	})
	h.waitRoute("staging", "zz-app", "Programmed")
	rt := h.waitRoute("staging", "fleet", "RouteConflict")
	c, _ := rt.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "staging/zz-app")
	require.Equal(t, "staging/zz-app", h.page("fleet.example.com", "/"))
}

func TestScenario_exposure_rule_auth(t *testing.T) {
	t.Parallel()
	mixed := func(ns v1.NamespaceName) *v1.Route {
		r := pageRoute(ns, "app", "", "/")
		r.Spec.Auth = nil
		r.Spec.Rules[0].Auth = &v1.RouteAuth{Mode: v1.AuthOpen}
		r.Spec.Rules = append(r.Spec.Rules, v1.RouteRule{Path: "/api", Auth: &v1.RouteAuth{Mode: v1.AuthAuthenticated},
			Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "web", Prefix: "app/", Index: "index.html"}}})
		return r
	}
	h := startNSHost(t, []v1.Object{authenticatedNS("secure"), mixed("staging"), mixed("secure")})
	for _, ns := range []string{"staging", "secure"} {
		h.waitRoute(v1.NamespaceName(ns), "app", "Programmed")
		require.Equal(t, ns+"/app", h.page(ns+".localhost", "/"), "%s: / answers anonymously", ns)
		require.Equal(t, http.StatusUnauthorized, h.status(ns+".localhost", "/api"), "%s: /api needs a token", ns)
	}
}

// The default path of an enabled lake ingress is proven against the edge in
// internal/services/lake (TestScenario_exposure_lake_default_path): this e2e has no lake engine.
func TestScenario_exposure_lake(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, nil)
	body := `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake","metadata":{"name":"bi","namespace":"default","resourceGroup":"rg1"},` +
		`"spec":{"location":{"bucket":"lakehouse","prefix":"gold"},"bindings":{"blob":[{"alias":"lake","bucket":"lakehouse","prefix":"gold"}]},` +
		`"ingress":{"enabled":true,"auth":{"mode":"open"}}}}`
	code, msg := h.post("lakes", body)
	require.Equal(t, http.StatusBadRequest, code, msg)
	require.Contains(t, msg, "spec.ingress.auth")
}

func TestScenario_exposure_removed_keys(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, nil)
	meta := `"metadata":{"name":"x","namespace":"default","resourceGroup":"rg1"}`
	for plural, body := range map[string]string{
		"sites":  `{"apiVersion":"funcd.io/v1alpha1","kind":"Site",` + meta + `,"spec":{"image":"oci-layout:///x:v1","bucket":{"name":"web"},"prefix":"web","ingress":{"public":true}}}`,
		"routes": `{"apiVersion":"funcd.io/v1alpha1","kind":"Route",` + meta + `,"spec":{"rules":[{"path":"/","backend":{"static":{"bucket":"web","public":true}}}]}}`,
		"lakes":  `{"apiVersion":"funcd.io/v1alpha1","kind":"Lake",` + meta + `,"spec":{"location":{"bucket":"lakehouse","prefix":"gold"},"bindings":{"blob":[{"alias":"lake","bucket":"lakehouse","prefix":"gold"}]},"ingress":{"pathPrefix":"/lake/lake"}}}`,
	} {
		code, msg := h.post(plural, body)
		require.Equal(t, http.StatusUnprocessableEntity, code, "%s: %s", plural, msg)
	}
}
