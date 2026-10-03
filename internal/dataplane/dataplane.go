// Package dataplane is the function-invocation data plane (ADR-0033): the HTTP handler
// that serves function traffic, separate from the control-plane API (ADR-0028).
//
// Front door (ADR-0110, F79): it first consults the edge Router — a compiled Route matcher —
// to resolve a request to a (namespace, function). On a hit it runs the existing activator hop
// (it resolves, it does not proxy — scale-to-zero is preserved). On a miss it falls back to the
// path form /function/<name> (+ X-Funcd-Namespace, default "default"): if the target namespace
// is `explicit` the request is 404ed BEFORE any activator call (default-deny ingress, no wake);
// otherwise it is served by name exactly as before. Internal fn-to-fn invoke (ADR-0064) reuses this
// same in-process handler but marks its context WithInternal, which bypasses the Route front door and
// the exposure gate — internal invocation is by name and is never gated (spoof-proof: the marker is a
// context value set in-process, unreachable from the public listener).
package dataplane

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/edge/authn"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/store"
)

// internalKey marks a request context as originating from an internal caller.
type internalKey struct{}

// WithInternal marks ctx as an internal (fn-to-fn, ADR-0064) invocation so the exposure gate is
// bypassed — internal invocation is never gated by a namespace's exposure mode (ADR-0110). It is a
// context value set only in-process by the worker-node local API broker; an external request on the
// public listener gets a fresh context and can never spoof it.
func WithInternal(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalKey{}, true)
}

func isInternal(ctx context.Context) bool { v, _ := ctx.Value(internalKey{}).(bool); return v }

// pathPrefix is the function-invocation route prefix: /function/<name>[/...].
const pathPrefix = "/function/"

// namespaceHeader carries the target namespace; absent → the default namespace.
const namespaceHeader = "X-Funcd-Namespace"

// Server serves function-invocation traffic through the activator.
type Server struct {
	store     store.Store
	activator *activator.Activator
	router    router.Router
	enforcer  *authn.Enforcer
	static    *static.Handler
	logger    *slog.Logger
}

// Handler builds the data-plane HTTP handler. The activator is the sole serving path;
// gateway.Handler() is not mounted here (ADR-0033). rtr is the F79 edge router (may be nil, in
// which case only the /function/<name> path is served — implicit-only, pre-F79 behavior). enf is the
// F77 edge authn PEP (may be nil ⇒ no enforcement for `open`; an `authenticated` stance fails closed).
// stat is the F82 static-asset handler (may be nil ⇒ a static Route match 404s).
func Handler(st store.Store, act *activator.Activator, rtr router.Router, enf *authn.Enforcer, stat *static.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: st, activator: act, router: rtr, enforcer: enf, static: stat, logger: logger.With("component", "dataplane")}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const op = "dataplane.ServeHTTP"

	// Internal fn-to-fn invoke (ADR-0064) is addressed by name and is NEVER gated or re-routed by
	// exposure (ADR-0110): it skips the Route front door entirely and serves the /function/<name> form.
	internal := isInternal(r.Context())

	// Front door: try the Route matcher first (both exposure modes) — public requests only.
	if s.router != nil && !internal {
		if m, ok := s.router.Resolve(r.Host, r.URL.Path, r.Method); ok {
			if m.Static != nil {
				// Static backend (ADR-0120, F82): serve the Bucket prefix directly — no activator hop.
				s.serveStatic(w, r, m, op)
				return
			}
			if m.Upstream != "" {
				// Node-private upstream backend (ADR-0138): reverse-proxy to an in-daemon target (the
				// CatalogService's catalog::query PEP proxy). No activator hop, no Function resolve. The
				// upstream does its OWN authz (the proxy PEPs catalog::query on the Quack handshake token),
				// so the edge honors the entry's own stance (open for a catalog) rather than gating here.
				s.serveUpstream(w, r, m, op)
				return
			}
			stance := s.authStance(r, m.Auth, m.Namespace)
			s.serveFunction(w, r, m.Namespace, m.Function, stripMatched(r.URL.Path, m.StripPrefix), stance, internal, op)
			return
		}
	}

	// The /function/<name> path form. Gate on the target namespace's exposure mode (public only).
	name, rest, ok := parseFunctionPath(r.URL.Path)
	if !ok {
		fault.WriteProblem(w, fault.NotFoundf(op, "no route for %q", r.URL.Path))
		return
	}
	ns := v1.NamespaceName(r.Header.Get(namespaceHeader))
	if ns == "" {
		ns = "default"
	}
	if s.router != nil && !internal && s.exposureMode(r, ns) == v1.ExposureExplicit {
		// Default-deny ingress: refuse BEFORE any activator call — no sandbox is woken.
		fault.WriteProblem(w, fault.NotFoundf(op, "no route exposes %s/%s (namespace is explicit)", ns, name))
		return
	}
	stance := s.authStance(r, "", ns)
	s.serveFunction(w, r, ns, v1.ObjectName(name), rest, stance, internal, op)
}

// serveFunction addresses the resolved function and hands off to the activator (the existing
// warm-proxy / cold-wake path). remainder is the function-relative path (matched prefix already
// stripped for a Route hit; the /function/<name> remainder for the path form). The F77 edge authn
// PEP is enforced FIRST — before store.Get (no function-enumeration oracle) and before the activator
// (no wake) — unless the request is internal fn-to-fn (ADR-0064), which is never edge-gated.
func (s *Server) serveFunction(w http.ResponseWriter, r *http.Request, ns v1.NamespaceName, name v1.ObjectName, remainder string, stance v1.AuthMode, internal bool, op string) {
	if remainder == "" {
		remainder = "/"
	}
	// Fill the F76 observability holder (if any) with the resolved target — so the metric/access-log
	// function label is correct even for a Route hit (observ runs outside this handler, ADR-0114).
	if t, ok := observ.TargetFrom(r.Context()); ok {
		t.Namespace, t.Function = string(ns), string(name)
	}
	if !internal {
		if s.enforcer != nil {
			if err := s.enforcer.Enforce(r.Context(), r, activator.FunctionRef{Namespace: ns, Name: name}, stance); err != nil {
				fault.WriteProblem(w, err)
				return
			}
		} else if stance == v1.AuthAuthenticated {
			// Fail-closed: an authenticated stance with no PEP wired cannot authenticate ⇒ 401.
			fault.WriteProblem(w, fault.Unauthorizedf(op, "authentication required but no authenticator is configured"))
			return
		}
	}
	if _, err := s.store.Get(r.Context(), v1.KindFunction.GVK(), ns, name); err != nil {
		fault.WriteProblem(w, fault.Wrapf(err, fault.KindOf(err), op, "function %s/%s", ns, name))
		return
	}
	out := r.Clone(r.Context())
	// ADR-0134: for an EXTERNAL invoke, build the CloudEvent envelope from the request body so a
	// caller sends plain data (or nothing) and never hand-writes {"data":…}. Internal producers
	// (fn-to-fn/workflow/sensor) already emit a v1.0 envelope and bypass this — leave them streamed.
	// The read is independently bounded (maxNormalizeBytes) because normalization buffers.
	if !internal {
		body, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNormalizeBytes))
		if rerr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(rerr, &tooLarge) {
				fault.WriteProblem(w, fault.PayloadTooLargef(op, "request body exceeds %d bytes", maxNormalizeBytes))
			} else {
				fault.WriteProblem(w, fault.Invalidf(op, "reading request body: %v", rerr))
			}
			return
		}
		body = InvokeEnvelope(ns, name, body)
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		out.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	// The remainder is relative to the function's upstream: a solo shim serves POST /, and a pooled
	// function's upstream carries the /function/<name> its pool worker routes by (ADR-0046), set by
	// the reconciler, which decides whether the function is pooled.
	out.URL.Path = remainder
	out = activator.WithFunction(out, activator.FunctionRef{Namespace: ns, Name: name})
	s.activator.ServeHTTP(w, out)
}

// serveStatic serves a static Bucket-prefix backend (ADR-0120, F82). It resolves the auth stance
// with the §4 public-vs-authenticated precedence (an explicit `authenticated` wins; `public: true`
// otherwise relaxes an unset/default stance to `open` — public NEVER silently opens an explicit
// authenticated site), enforces the PEP BEFORE any byte is read (namespace-scope authz, no function
// to name), fills the F76 observ Target (function label carries the bucket — an intentional V1
// overload), and dispatches to the static handler with the stripped remainder — no activator hop.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, m router.Match, op string) {
	ns := m.Namespace
	stance := s.authStance(r, m.Auth, ns)
	if m.Static.Public && stance != v1.AuthAuthenticated {
		stance = v1.AuthOpen
	}
	// Fill the F76 observability holder with (ns, bucket) — the function label is overloaded with the
	// bucket name for a static route (a distinct backend/kind label is a named follow-on, ADR-0120 §5).
	if t, ok := observ.TargetFrom(r.Context()); ok {
		t.Namespace, t.Function = string(ns), string(m.Static.Bucket)
	}
	// Enforce reject-before-read: the PEP runs before any blob Get (no existence oracle, mirroring
	// ADR-0113). A static route has no function, so authz is namespace-scoped (FunctionRef{Namespace: ns}).
	if s.enforcer != nil {
		if err := s.enforcer.Enforce(r.Context(), r, activator.FunctionRef{Namespace: ns}, stance); err != nil {
			fault.WriteProblem(w, err)
			return
		}
	} else if stance == v1.AuthAuthenticated {
		fault.WriteProblem(w, fault.Unauthorizedf(op, "authentication required but no authenticator is configured"))
		return
	}
	if s.static == nil {
		fault.WriteProblem(w, fault.NotFoundf(op, "static serving is not configured"))
		return
	}
	s.static.Serve(w, r, ns, m.Static, stripMatched(r.URL.Path, m.StripPrefix))
}

// serveUpstream reverse-proxies a matched request to a node-private in-daemon upstream (ADR-0138) —
// the CatalogService's catalog::query PEP proxy. The matched rule prefix is stripped so the upstream
// is addressed at its own root, mirroring the gateway's PathPrefix strip. Streaming-native
// (httputil.ReverseProxy). The upstream is trusted (set only by an in-daemon reconciler, never a user
// Route) and does its own authz, so no edge PEP runs here. A malformed upstream is a 502 (a
// reconciler bug, not a client error).
func (s *Server) serveUpstream(w http.ResponseWriter, r *http.Request, m router.Match, op string) {
	target, err := url.Parse(m.Upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		fault.WriteProblem(w, fault.Unavailablef(op, "edge upstream %q is not a valid URL", m.Upstream))
		return
	}
	if t, ok := observ.TargetFrom(r.Context()); ok {
		t.Namespace, t.Function = string(m.Namespace), m.Upstream
	}
	remainder := stripMatched(r.URL.Path, m.StripPrefix)
	if remainder == "" {
		remainder = "/"
	}
	logger := s.logger.With("upstream", m.Upstream)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
	proxy.ErrorHandler = func(w http.ResponseWriter, pr *http.Request, perr error) {
		logger.WarnContext(pr.Context(), "edge upstream call failed", "error", perr)
		fault.WriteProblem(w, fault.Unavailablef(op, "edge upstream unreachable: %v", perr))
	}
	activator.KeepEdgeHeaders(proxy, w)
	// Address the upstream at its own root: replace the request path with the stripped remainder
	// (NewSingleHostReverseProxy's default Director would join the target path with the full request
	// path, keeping the matched prefix — we want it stripped).
	r.URL.Path = remainder
	r.URL.RawPath = ""
	proxy.ServeHTTP(w, r)
}

// authStance resolves the F77 auth stance for a request: the matched Route's mode if set, else the
// namespace's edgeDefaults.auth.mode, else `open` (the phased default). routeMode is "" for the path
// form or a Route with no auth set. It short-circuits without a store read when routeMode is set.
func (s *Server) authStance(r *http.Request, routeMode v1.AuthMode, ns v1.NamespaceName) v1.AuthMode {
	if routeMode != "" {
		return routeMode
	}
	obj, err := s.store.Get(r.Context(), v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
	if err != nil {
		return v1.AuthOpen
	}
	if n, ok := obj.(*v1.Namespace); ok && n.Spec.EdgeDefaults != nil && n.Spec.EdgeDefaults.Auth != nil && n.Spec.EdgeDefaults.Auth.Mode != "" {
		return n.Spec.EdgeDefaults.Auth.Mode
	}
	return v1.AuthOpen
}

// exposureMode reads the target namespace's normalized exposure mode; an absent Namespace (or a
// read error) is implicit — so a Spec-less namespace still serves by name.
func (s *Server) exposureMode(r *http.Request, ns v1.NamespaceName) v1.ExposureMode {
	obj, err := s.store.Get(r.Context(), v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
	if err != nil {
		return v1.ExposureImplicit
	}
	if n, ok := obj.(*v1.Namespace); ok {
		return n.Spec.DefaultExposure.Normalized()
	}
	return v1.ExposureImplicit
}

// stripMatched removes the matched route prefix to get the function-relative remainder,
// mirroring the gateway stripPrefix ("/orders" + "/orders/x" → "/x"; exact ⇒ prefix "" ⇒ "/").
func stripMatched(path, prefix string) string {
	if prefix == "" {
		return "/"
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "/"
	}
	return rest
}

// parseFunctionPath splits "/function/<name>[/rest]" into the name and the remainder path
// (the shim's view, always starting "/"). ok=false if the path is not a function route.
func parseFunctionPath(path string) (name, rest string, ok bool) {
	if !strings.HasPrefix(path, pathPrefix) {
		return "", "", false
	}
	tail := strings.TrimPrefix(path, pathPrefix)
	if tail == "" {
		return "", "", false
	}
	name = tail
	rest = "/"
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		name = tail[:i]
		rest = tail[i:]
	}
	if name == "" {
		return "", "", false
	}
	return name, rest, true
}
