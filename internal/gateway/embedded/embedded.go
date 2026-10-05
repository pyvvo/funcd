// Package embedded implements the gateway.Gateway port with net/http/httputil —
// a hand-written reverse proxy behind a longest-prefix router (ADR-0012). It is
// the cross-platform dev/e2e/CI driver (the InMemory() harness gateway); pure
// stdlib, no framework.
package embedded

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

type compiledRoute struct {
	route gateway.Route
	proxy *httputil.ReverseProxy
}

type driver struct {
	mu        sync.RWMutex
	routes    []gateway.Route
	table     []compiledRoute // sorted by prefix length, longest first
	transport *http.Transport // shared, pooled upstream transport (ADR-0041, ADR-0155)
}

// New returns an embedded reverse-proxy gateway (dev/e2e/CI). Its reverse proxies share one
// node-local upstream transport, so connections to the function workers are reused across
// requests (ADR-0041, ADR-0155).
func New() gateway.Gateway {
	return &driver{transport: httpx.NodeTransport()}
}

func (d *driver) ProgramRoutes(_ context.Context, routes []gateway.Route) error {
	const op = "gateway.embedded.ProgramRoutes"
	compiled := make([]compiledRoute, 0, len(routes))
	for _, r := range routes {
		if r.PathPrefix == "" {
			return fault.Invalidf(op, "route %q has an empty PathPrefix", r.ID)
		}
		target, err := url.Parse(r.Upstream)
		if err != nil || target.Scheme == "" || target.Host == "" {
			return fault.Invalidf(op, "route %q has an invalid upstream URL %q", r.ID, r.Upstream)
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		// Flush each write immediately so SSE / token streams pass through without
		// buffering (ADR-0013: streaming is first-class for agents/MCP).
		rp.FlushInterval = -1
		// Reuse pooled upstream connections instead of dialing per request (ADR-0041).
		rp.Transport = d.transport
		compiled = append(compiled, compiledRoute{route: r, proxy: rp})
	}
	sort.SliceStable(compiled, func(i, j int) bool {
		return len(compiled[i].route.PathPrefix) > len(compiled[j].route.PathPrefix)
	})

	cp := make([]gateway.Route, len(routes))
	copy(cp, routes)

	d.mu.Lock()
	d.routes = cp
	d.table = compiled
	d.mu.Unlock()
	return nil
}

func (d *driver) Routes(_ context.Context) ([]gateway.Route, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]gateway.Route, len(d.routes))
	copy(out, d.routes)
	return out, nil
}

func (d *driver) Handler() http.Handler {
	return http.HandlerFunc(d.serve)
}

func (d *driver) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	table := d.table
	d.mu.RUnlock()

	for _, cr := range table {
		if cr.route.Host != "" && cr.route.Host != r.Host {
			continue
		}
		if matchPrefix(r.URL.Path, cr.route.PathPrefix) {
			out := r.Clone(r.Context())
			out.URL.Path = stripPrefix(r.URL.Path, cr.route.PathPrefix)
			cr.proxy.ServeHTTP(w, out)
			return
		}
	}
	http.NotFound(w, r)
}

func (d *driver) Close() error { return nil }

// matchPrefix reports whether path is the prefix exactly or a sub-path of it
// (so "/function/echo" matches "/function/echo" and "/function/echo/x" but not
// "/function/echo-2").
func matchPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// stripPrefix removes the route prefix so the function is addressed at its root.
func stripPrefix(path, prefix string) string {
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "/"
	}
	return rest
}
