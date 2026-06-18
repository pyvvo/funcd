// Package gateway is the ingress / API gateway port (ADR-0012): program routes
// declaratively and serve them as an http.Handler that reverse-proxies a matched
// public path to a function's worker upstream.
//
// The port is driver-independent and imports no gateway-framework library. A single
// in-process driver implements it: the embedded net/http/httputil reverse-proxy
// (internal/gateway/embedded) — pure-Go, streaming-native, and the production and
// in-memory driver alike (ADR-0013 made it primary; ADR-0029 dropped the optional
// Lura driver — an external-gateway driver is the V2 second driver). The activator
// (scale-from-zero buffering, ADR-0016) is a separate component that plugs in as a
// Route.Upstream.
package gateway

import (
	"context"
	"net/http"
)

// RouteID is a stable identifier for a programmed route (e.g. "<ns>/<name>").
type RouteID string

// Route maps a public path prefix (optionally host-qualified) to a function
// upstream. The gateway reverse-proxies a matched request to Upstream, stripping
// PathPrefix so the function is addressed at its own root.
type Route struct {
	ID         RouteID
	Host       string // optional host match ("" = any host)
	PathPrefix string // e.g. "/function/echo"
	Upstream   string // upstream base URL, e.g. "http://10.63.0.5:8080"
}

// Gateway is the ingress port: program routes declaratively and serve them as an
// http.Handler. Errors are api/fault; methods are ctx-first; the port imports no
// gateway-framework library. A route matches all HTTP methods in V1.
type Gateway interface {
	// ProgramRoutes replaces the live route table with the desired set.
	ProgramRoutes(ctx context.Context, routes []Route) error
	// Routes returns the currently programmed routes.
	Routes(ctx context.Context) ([]Route, error)
	// Handler returns the stable HTTP handler serving the programmed routes; it
	// survives re-programs, so callers mount it once.
	Handler() http.Handler
	// Close releases the driver.
	Close() error
}
