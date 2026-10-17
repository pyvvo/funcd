//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The ADR-0240 scenarios run a real funcd with the edge PEP on. A Route serves a static page that names its
// namespace and Route; a Function is never deployed, so reaching it shows in the status code: 401 in a namespace
// whose edge stance is authenticated, a 5xx from the activator in an open one, and 404 when the namespace has no
// such Function.

type nsHost struct {
	t    *testing.T
	st   store.Store
	blob blob.Bucket
	base string
}

func nsObject(name string, mutate func(*v1.Namespace)) *v1.Namespace {
	n := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}}
	n.Name = v1.ObjectName(name)
	if mutate != nil {
		mutate(n)
	}
	return n
}

func authenticatedNS(name string) *v1.Namespace {
	return nsObject(name, func(n *v1.Namespace) {
		n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	})
}

func explicitNS(name string) *v1.Namespace {
	return nsObject(name, func(n *v1.Namespace) { n.Spec.DefaultExposure = v1.ExposureExplicit })
}

func fnObject(ns v1.NamespaceName, name string) *v1.Function {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	return fn
}

// pageRoute is a public static Route at path whose page reads "<ns>/<name>".
func pageRoute(ns v1.NamespaceName, name, host, path string) *v1.Route {
	r := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	r.Spec = v1.RouteSpec{Host: host, Rules: []v1.RouteRule{{
		Path:    path,
		Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "web", Prefix: name + "/", Index: "index.html", SPA: true, Public: true}},
	}}}
	return r
}

// startNSHost seeds objs, a Bucket web in every namespace a pageRoute names and its page, then runs funcd.
func startNSHost(t *testing.T, objs []v1.Object, opts ...funcd.Option) *nsHost {
	t.Helper()
	ctx := context.Background()
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	st := store.New(memory.New())
	buckets := map[v1.NamespaceName]bool{}
	for _, o := range objs {
		seed(t, st, o)
		r, ok := o.(*v1.Route)
		if !ok || r.Spec.Rules[0].Backend.Static == nil {
			continue
		}
		require.NoError(t, bucket.Put(ctx, "s3/"+string(r.Namespace)+"/web/"+string(r.Name)+"/index.html",
			[]byte(string(r.Namespace)+"/"+string(r.Name)), blob.PutOptions{}))
		if !buckets[r.Namespace] {
			buckets[r.Namespace] = true
			b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
			b.Name, b.Namespace, b.ResourceGroup = "web", r.Namespace, "rg1"
			seed(t, st, b)
		}
	}
	p, err := funcd.New(append([]funcd.Option{
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(st), funcd.WithRuntime(process.New(nil)),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"), funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithEdgeAuth(), funcd.WithArtifactStore(shortDataDir(t)),
	}, opts...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })
	return &nsHost{t: t, st: st, blob: bucket, base: "http://" + p.DataPlaneAddr()}
}

func (h *nsHost) route(ns v1.NamespaceName, name string) *v1.Route {
	h.t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindRoute.GVK(), ns, v1.ObjectName(name))
	require.NoError(h.t, err)
	return obj.(*v1.Route)
}

// waitRoute waits until Route ns/name has a Ready condition with reason ("Programmed" for Ready=True).
func (h *nsHost) waitRoute(ns v1.NamespaceName, name, reason string) *v1.Route {
	h.t.Helper()
	var rt *v1.Route
	require.Eventually(h.t, func() bool {
		obj, err := h.st.Get(context.Background(), v1.KindRoute.GVK(), ns, v1.ObjectName(name))
		if err != nil {
			return false
		}
		rt = obj.(*v1.Route)
		c, ok := rt.Status.Conditions.Get("Ready")
		return ok && c.Reason == reason
	}, 15*time.Second, 50*time.Millisecond, "route %s/%s reaches %s", ns, name, reason)
	return rt
}

func (h *nsHost) get(method, host, path, nsHeader string) (int, string) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, nil) //nolint:noctx // test client
	require.NoError(h.t, err)
	req.Host = host
	if nsHeader != "" {
		req.Header.Set("X-Funcd-Namespace", nsHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(body)
}

func (h *nsHost) page(host, path string) string {
	h.t.Helper()
	code, body := h.get(http.MethodGet, host, path, "")
	require.Equal(h.t, http.StatusOK, code, "GET %s%s", host, path)
	return body
}

func (h *nsHost) status(host, path string) int {
	h.t.Helper()
	code, _ := h.get(http.MethodGet, host, path, "")
	return code
}

// reachedOpen reports whether a by-name call reached an existing Function of an open namespace.
func reachedOpen(code int) bool { return code >= 500 }

func TestScenario_namespace_host_function(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{authenticatedNS("staging"), fnObject("staging", "fleet-api"), fnObject("default", "fleet-api")})

	for _, host := range []string{"staging.localhost", "api.staging.localhost"} {
		code, _ := h.get(http.MethodPost, host, "/function/fleet-api", "")
		require.Equal(t, http.StatusUnauthorized, code, "%s reaches staging's Function", host)
	}
	code, _ := h.get(http.MethodPost, "localhost", "/function/fleet-api", "")
	require.True(t, reachedOpen(code), "localhost reaches default's Function, got %d", code)
}

func TestScenario_namespace_host_routes(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{pageRoute("staging", "api", "", "/api"), pageRoute("prod", "api", "", "/api")})
	h.waitRoute("staging", "api", "Programmed")
	h.waitRoute("prod", "api", "Programmed")

	require.Equal(t, "staging/api", h.page("staging.localhost", "/api"))
	require.Equal(t, "prod/api", h.page("prod.localhost", "/api"))
}

func TestScenario_namespace_host_site(t *testing.T) {
	t.Parallel()
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "fleet", map[string]string{"index.html": "<title>fleet</title>"})
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = "fleet", "staging", "rg1"
	s.Spec = v1.SiteSpec{Image: ref, Bucket: v1.SiteBucket{Name: "reports"}, Prefix: "fleet", Ingress: v1.SiteIngress{Public: true}}
	h := startNSHost(t, []v1.Object{s})
	require.Eventually(t, func() bool {
		obj, err := h.st.Get(context.Background(), v1.KindSite.GVK(), "staging", "fleet")
		return err == nil && obj.(*v1.Site).Status.Phase == v1.PhaseReady
	}, 20*time.Second, 50*time.Millisecond)

	require.Contains(t, h.page("staging.localhost", "/site/fleet/"), "<title>fleet</title>")
	require.Equal(t, http.StatusNotFound, h.status("localhost", "/site/fleet/"))
	require.Equal(t, "staging.localhost", h.route("staging", "fleet").Status.Host, "the Site's own Route shows where it answers")
}

func TestScenario_namespace_host_default(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{pageRoute("default", "home", "", "/home"), fnObject("default", "fleet-api")})
	h.waitRoute("default", "home", "Programmed")

	for _, host := range []string{"localhost", "127.0.0.1", "fleet.localhost"} {
		require.Equal(t, "default/home", h.page(host, "/home"), host)
	}
	for _, header := range []string{"", "default"} {
		code, _ := h.get(http.MethodPost, "fleet.localhost", "/function/fleet-api", header)
		require.Equal(t, http.StatusNotFound, code, "fleet.localhost names namespace fleet, which has no Function (header %q)", header)
	}
}

func TestScenario_namespace_host_isolated(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		pageRoute("staging", "api", "", "/api"),
		pageRoute("default", "root", "", "/"),
		pageRoute("default", "v2", "", "/api/v2"),
	})
	for _, r := range []struct {
		ns   v1.NamespaceName
		name string
	}{{"staging", "api"}, {"default", "root"}, {"default", "v2"}} {
		h.waitRoute(r.ns, r.name, "Programmed")
	}

	require.Equal(t, "staging/api", h.page("staging.localhost", "/api"))
	require.Equal(t, "staging/api", h.page("staging.localhost", "/api/v2"))
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/"))
	require.Equal(t, http.StatusNotFound, h.status("x.staging.localhost", "/"))
	require.Equal(t, "default/v2", h.page("localhost", "/api/v2"))
}

func TestScenario_namespace_host_function_path(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		authenticatedNS("staging"), fnObject("staging", "fleet-api"),
		pageRoute("prod", "root", "staging.localhost", "/"),
		pageRoute("prod", "fn", "staging.localhost", "/function/fleet-api"),
	})
	h.waitRoute("prod", "root", "Programmed")
	rt := h.waitRoute("prod", "fn", "HostReserved")
	require.Equal(t, v1.PhasePending, rt.Status.Phase)

	require.Equal(t, "prod/root", h.page("staging.localhost", "/"))
	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.Equal(t, http.StatusUnauthorized, code, "staging's Function, not prod's / Route")
}

func TestScenario_namespace_host_explicit_mode(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		explicitNS("staging"), explicitNS("default"), fnObject("staging", "fleet-api"),
		pageRoute("staging", "api", "", "/api"),
		pageRoute("default", "api", "", "/api"),
	})
	h.waitRoute("staging", "api", "Programmed")
	h.waitRoute("default", "api", "HostRequired")

	require.Equal(t, "staging/api", h.page("staging.localhost", "/api"))
	code, _ := h.get(http.MethodPost, "staging.localhost", "/function/fleet-api", "")
	require.Equal(t, http.StatusNotFound, code, "refused before the activator, so nothing is woken")
}

func TestScenario_namespace_host_reserved(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		pageRoute("staging", "web", "", "/"),
		pageRoute("prod", "take", "staging.localhost", "/x"),
		pageRoute("prod", "under", "api.staging.localhost", "/x"),
		pageRoute("default", "fleet", "fleet.localhost", "/"),
	})
	h.waitRoute("staging", "web", "Programmed")
	h.waitRoute("prod", "take", "HostReserved")
	h.waitRoute("prod", "under", "HostReserved")
	h.waitRoute("default", "fleet", "Programmed")
	require.Equal(t, "staging/web", h.page("staging.localhost", "/x"), "staging keeps serving")
	require.Equal(t, "default/fleet", h.page("fleet.localhost", "/"))

	require.NoError(t, h.st.Delete(context.Background(), v1.KindRoute.GVK(), "staging", "web", ""))
	h.waitRoute("prod", "take", "Programmed")
	h.waitRoute("prod", "under", "Programmed")
	require.Equal(t, "prod/take", h.page("staging.localhost", "/x"))
	require.Equal(t, "prod/under", h.page("api.staging.localhost", "/x"))
}

func TestScenario_namespace_host_status(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{
		pageRoute("staging", "a", "", "/a"),
		pageRoute("staging", "b", "fleet.example.com", "/b"),
		pageRoute("default", "c", "", "/c"),
	})
	require.Equal(t, "staging.localhost", h.waitRoute("staging", "a", "Programmed").Status.Host)
	require.Equal(t, "fleet.example.com", h.waitRoute("staging", "b", "Programmed").Status.Host)
	require.Empty(t, h.waitRoute("default", "c", "Programmed").Status.Host)

	require.Equal(t, "staging/b", h.page("fleet.example.com", "/b"))
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/b"))
}

func TestScenario_namespace_host_header(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{authenticatedNS("staging"), fnObject("staging", "fleet-api"), fnObject("prod", "fleet-api")})

	code, _ := h.get(http.MethodPost, "localhost", "/function/fleet-api", "staging")
	require.Equal(t, http.StatusUnauthorized, code, "the header names staging on the base domain")
	code, _ = h.get(http.MethodPost, "prod.localhost", "/function/fleet-api", "staging")
	require.True(t, reachedOpen(code), "prod.localhost names prod whatever the header, got %d", code)
}

func TestScenario_namespace_host_base_domain(t *testing.T) {
	t.Parallel()
	h := startNSHost(t, []v1.Object{pageRoute("staging", "api", "", "/api")}, funcd.WithBaseDomain("apps.example.com"))
	require.Equal(t, "staging.apps.example.com", h.waitRoute("staging", "api", "Programmed").Status.Host)
	require.Equal(t, "staging/api", h.page("staging.apps.example.com", "/api"))
	require.Equal(t, http.StatusNotFound, h.status("staging.localhost", "/api"))

	long := "a"
	for len(long) <= 189 {
		long += ".abcdefgh"
	}
	refused := map[string][]funcd.Option{
		"localhost:8081":  {funcd.WithBaseDomain("localhost:8081")},
		"apps..example":   {funcd.WithBaseDomain("apps..example")},
		"Apps.example":    {funcd.WithBaseDomain("Apps.example")},
		"10.0.0.1":        {funcd.WithBaseDomain("10.0.0.1")},
		"over 189":        {funcd.WithBaseDomain(long)},
		"acme, localhost": {funcd.WithTLS(edgetls.Spec{Mode: edgetls.ModeACME, Email: "ops@example.com", StorageDir: t.TempDir()})},
	}
	for name, opts := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bucket, err := gocloud.Open(context.Background(), "mem://")
			require.NoError(t, err)
			messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
			require.NoError(t, err)
			p, err := funcd.New(append([]funcd.Option{
				funcd.WithBlob(bucket), funcd.WithBus(messaging), funcd.WithStore(store.New(memory.New())), funcd.WithRuntime(process.New(nil)),
				funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
				funcd.WithDataPlaneAddr("127.0.0.1:0"), funcd.WithDevAuth(funcd.DevToken, "default"),
				funcd.WithArtifactStore(shortDataDir(t)),
			}, opts...)...)
			require.NoError(t, err)
			err = p.Run(context.Background())
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
		})
	}
}
