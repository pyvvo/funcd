// Package dataplane is the function-invocation data plane (ADR-0033): the HTTP handler
// that serves function traffic, separate from the control-plane API (ADR-0028). It
// resolves a request to its FunctionRef from the path /function/<name> (+ the
// X-Funcd-Namespace header, default "default"), validates the function exists in the
// store, and serves it through the activator — warm → proxy to the ready upstream now;
// cold → buffer, wake (ScaleTo 1), poll readiness, forward (ADR-0016). It does NOT use the
// gateway's route table or Handler() — resolving from the path + store keeps a
// scaled-to-zero function reachable while Idle without a programmed placeholder route.
package dataplane

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/activator"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// pathPrefix is the function-invocation route prefix: /function/<name>[/...].
const pathPrefix = "/function/"

// namespaceHeader carries the target namespace; absent → the default namespace.
const namespaceHeader = "X-Funcd-Namespace"

// Server serves function-invocation traffic through the activator.
type Server struct {
	store     store.Store
	activator *activator.Activator
	logger    *slog.Logger
}

// Handler builds the data-plane HTTP handler. The activator is the sole serving path;
// gateway.Handler() is not mounted here (ADR-0033).
func Handler(st store.Store, act *activator.Activator, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: st, activator: act, logger: logger.With("component", "dataplane")}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const op = "dataplane.ServeHTTP"
	name, rest, ok := parseFunctionPath(r.URL.Path)
	if !ok {
		fault.WriteProblem(w, fault.NotFoundf(op, "no function route for %q", r.URL.Path))
		return
	}
	ns := v1.NamespaceName(r.Header.Get(namespaceHeader))
	if ns == "" {
		ns = "default"
	}
	// Validate the function exists, so a wake never targets a phantom.
	obj, err := s.store.Get(r.Context(), v1.KindFunction.GVK(), ns, v1.ObjectName(name))
	if err != nil {
		fault.WriteProblem(w, fault.Wrapf(err, fault.KindOf(err), op, "function %s/%s", ns, name))
		return
	}
	// Address the function. A SOLO function's shim serves POST / (strip the prefix). A POOLED
	// function (spec.pooling.worker set, ADR-0046) shares a pool worker that routes by name at
	// POST /function/<name>, so the prefix is PRESERVED (Decision 5) and the pool routes by name.
	out := r.Clone(r.Context())
	if fn, ok := obj.(*v1.Function); ok && fn.Spec.Pooling.Worker != "" {
		// pool.mjs routes by /function/<name> (no trailing slash for the bare invocation);
		// rest is the shim-root remainder ("/" for the bare call), appended for sub-paths.
		out.URL.Path = pathPrefix + name
		if rest != "/" {
			out.URL.Path += rest
		}
	} else {
		out.URL.Path = rest
	}
	out = activator.WithFunction(out, activator.FunctionRef{Namespace: ns, Name: v1.ObjectName(name)})
	s.activator.ServeHTTP(w, out)
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
