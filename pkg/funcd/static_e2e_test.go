package funcd_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
)

// scenarios: serves-index-and-asset + range-request + public-route-skips-authn (e2e, F82/ADR-0120) —
// a real funcd with WithEdgeAuth serving a prebuilt static site straight off the Bucket substrate over
// the live edge, with NO function code and no activator hop. Proves the wiring end to end: the Route
// reconciler programs a static backend, the data-plane front door dispatches it to internal/edge/static,
// and the §4 public-vs-authenticated precedence holds (public served anonymously; a non-public route in
// an authenticated namespace 401s anon and serves with a valid namespace-scoped bearer).
func TestScenarioE2EStaticServing(t *testing.T) {
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	ctx := context.Background()

	// Seed the site bytes into the SAME per-namespace substrate view external S3 writes land in
	// (s3/<ns>/<bucket>/<prefix><key>) — one substrate, a third read surface (ADR-0120 §Context).
	indexHTML := []byte("<!doctype html><title>bi</title><div id=app></div>")
	logoPNG := []byte("\x89PNG\r\n\x1a\nlogo-bytes")
	big := make([]byte, 1000)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	put := func(ns, key string, v []byte) { require.NoError(t, bucket.Put(ctx, "s3/"+ns+"/reports/bi/"+key, v)) }
	put("openteam", "index.html", indexHTML)
	put("openteam", "img/logo.png", logoPNG)
	put("openteam", "big.bin", big)
	put("team", "index.html", indexHTML)

	st := store.New(memory.New())
	// Namespace `team`: an authenticated edge stance (seeded directly — cluster-scoped, off-limits to
	// dev-auth over the SDK). `openteam` is left absent ⇒ its stance resolves to the phased `open`.
	nteam := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}}
	nteam.Name = "team"
	nteam.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	seed(t, st, nteam)
	// The Bucket resources whose existence the reconciler + s3BucketFor resolve.
	for _, ns := range []v1.NamespaceName{"openteam", "team"} {
		b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
		b.Name, b.Namespace, b.ResourceGroup = "reports", ns, "rg1"
		seed(t, st, b)
	}
	// The public BI site (openteam, public:true) + the authenticated docs site (team, public omitted).
	staticRoute := func(name string, ns v1.NamespaceName, host string, public bool) *v1.Route {
		r := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
		r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
		r.Spec = v1.RouteSpec{Host: host, Rules: []v1.RouteRule{{
			Path:    "/",
			Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "reports", Prefix: "bi/", Index: "index.html", SPA: true, Public: public}},
		}}}
		return r
	}
	seed(t, st, staticRoute("bi", "openteam", "bi.example.com", true))
	seed(t, st, staticRoute("docs", "team", "docs.example.com", false))

	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(st), funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, "team"), // a team-scoped bearer authorizes the team docs site
		funcd.WithEdgeAuth(),                      // enable the F77 PEP so public-vs-authenticated is exercised
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	for _, r := range []struct{ ns, name string }{{"openteam", "bi"}, {"team", "docs"}} {
		require.Eventually(t, func() bool {
			obj, gerr := st.Get(ctx, v1.KindRoute.GVK(), v1.NamespaceName(r.ns), v1.ObjectName(r.name))
			if gerr != nil {
				return false
			}
			cnd, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
			return ok && cnd.Status == v1.ConditionTrue
		}, 15*time.Second, 100*time.Millisecond, "static route %s/%s programmed", r.ns, r.name)
	}

	base := "http://" + p.DataPlaneAddr()
	get := func(host, path, bearer, rangeHdr string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.Host = host
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		resp, gerr := http.DefaultClient.Do(req)
		require.NoError(t, gerr)
		return resp
	}

	// public-route-skips-authn (positive) + serves-index-and-asset: the public BI site is served
	// anonymously through the edge — the index and a real asset with its content-type + a weak ETag.
	resp := get("bi.example.com", "/", "", "")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "public static route served anonymously")
	require.Contains(t, string(body), "<title>bi</title>")

	resp = get("bi.example.com", "/img/logo.png", "", "")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	require.Contains(t, resp.Header.Get("ETag"), "W/", "a weak (ModTime,Size) ETag")
	require.Equal(t, logoPNG, body)

	// range-request: a Range header yields 206 Partial Content off the large object.
	resp = get("bi.example.com", "/big.bin", "", "bytes=0-99")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	require.Equal(t, "bytes 0-99/1000", resp.Header.Get("Content-Range"))
	require.Equal(t, big[:100], body)

	// public-route-skips-authn (negative): the non-public docs site in the authenticated `team`
	// namespace 401s an anonymous request BEFORE any byte is read (public:false inherits the stance).
	resp = get("docs.example.com", "/", "", "")
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "public:false in an authenticated namespace enforces the PEP")

	// ...and serves with a valid namespace-scoped bearer (authenticated static route authorizes at
	// namespace scope, then serves the bytes — no function to wake).
	resp = get("docs.example.com", "/", funcd.DevToken, "")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a team-scoped bearer authorizes the authenticated static site")
	require.Contains(t, string(body), "<title>bi</title>")
}
