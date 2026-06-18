// Package gatewaycontract is the shared conformance suite for the gateway.Gateway
// port (ADR-0012). RunContract executes the same observable assertions against any
// driver — the embedded reverse-proxy driver calls it (the sole driver since
// ADR-0029 dropped Lura) — proxying to a real httptest upstream.
package gatewaycontract

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/gateway"
)

// RunContract runs every scenario assertion against the gateway from newGateway.
func RunContract(t *testing.T, newGateway func(t *testing.T) gateway.Gateway) {
	t.Helper()

	// upstream echoes the path it received so prefix-strip is observable.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "upstream-path="+r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	serve := func(t *testing.T, gw gateway.Gateway, path string) *http.Response {
		t.Helper()
		rec := httptest.NewRecorder()
		gw.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Result()
	}

	t.Run("route-proxies-to-upstream", func(t *testing.T) {
		ctx := context.Background()
		gw := newGateway(t)
		t.Cleanup(func() { _ = gw.Close() })
		require.NoError(t, gw.ProgramRoutes(ctx, []gateway.Route{
			{ID: "default/echo", PathPrefix: "/function/echo", Upstream: upstream.URL},
		}))

		resp := serve(t, gw, "/function/echo/x")
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), "upstream-path=")
	})

	t.Run("route-strips-prefix", func(t *testing.T) {
		ctx := context.Background()
		gw := newGateway(t)
		t.Cleanup(func() { _ = gw.Close() })
		require.NoError(t, gw.ProgramRoutes(ctx, []gateway.Route{
			{ID: "default/echo", PathPrefix: "/function/echo", Upstream: upstream.URL},
		}))

		resp := serve(t, gw, "/function/echo/sub/path")
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), "upstream-path=/sub/path",
			"the upstream sees the path with the route prefix stripped")
	})

	t.Run("unprogrammed-path-404", func(t *testing.T) {
		ctx := context.Background()
		gw := newGateway(t)
		t.Cleanup(func() { _ = gw.Close() })
		require.NoError(t, gw.ProgramRoutes(ctx, []gateway.Route{
			{ID: "default/echo", PathPrefix: "/function/echo", Upstream: upstream.URL},
		}))

		resp := serve(t, gw, "/function/nope")
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("reprogram-replaces-routes", func(t *testing.T) {
		ctx := context.Background()
		gw := newGateway(t)
		t.Cleanup(func() { _ = gw.Close() })

		require.NoError(t, gw.ProgramRoutes(ctx, []gateway.Route{
			{ID: "default/a", PathPrefix: "/function/a", Upstream: upstream.URL},
			{ID: "default/b", PathPrefix: "/function/b", Upstream: upstream.URL},
		}))
		require.Equal(t, http.StatusOK, serve(t, gw, "/function/a").StatusCode)
		require.Equal(t, http.StatusOK, serve(t, gw, "/function/b").StatusCode)

		// Declarative replace: drop B.
		require.NoError(t, gw.ProgramRoutes(ctx, []gateway.Route{
			{ID: "default/a", PathPrefix: "/function/a", Upstream: upstream.URL},
		}))
		require.Equal(t, http.StatusOK, serve(t, gw, "/function/a").StatusCode)
		require.Equal(t, http.StatusNotFound, serve(t, gw, "/function/b").StatusCode,
			"a dropped route is no longer served after a declarative re-program")

		routes, err := gw.Routes(ctx)
		require.NoError(t, err)
		require.Len(t, routes, 1)
	})
}
