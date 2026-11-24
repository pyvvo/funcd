// Package local is the per-sandbox worker-node local API (ADR-0064): an HTTP-over-UDS surface the
// runtime shim calls for the synchronous fn-to-fn invoke verb (context.invoke). One listener per
// sandbox; the caller Ref is fixed at provisioning, so identity is connection-scoped — a request
// body can never name a different caller. A pool worker's listener serves the pool's members, each
// request as the member it names in the X-Funcd-Member header. No published SDK — reached only through the built-in shim.
//
// It nests under the blueprint's internal/workernode node-agent component.
package local

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/edge/oidc"
)

const (
	serverHeaderTimeout = 5 * time.Second
	serverIdleTimeout   = 30 * time.Second
)

// maxInvokeBytes caps an invoke request body and the target's response (a DoS guard on the local API).
const maxInvokeBytes = 1 << 20 // 1 MiB

// Ref is a worker's caller identity, fixed per listener (ADR-0351 Decision 2): its namespace, its kind, Function or Job,
// the workload's name and, for a Job's worker, its run. A ref of any other kind is refused.
type Ref struct {
	Namespace v1.NamespaceName
	Kind      v1.Kind
	Name      v1.ObjectName
	Run       v1.ObjectName
}

// FunctionRef is Function name of ns as a caller, or as a link's target.
func FunctionRef(ns v1.NamespaceName, name v1.ObjectName) Ref {
	return Ref{Namespace: ns, Kind: v1.KindFunction, Name: name}
}

// Workload is the Function or the Job the ref acts as, whose bindings are its grants.
func (r Ref) Workload() v1.WorkloadRef { return v1.WorkloadRef{Kind: r.Kind, Name: r.Name} }

// Valid reports whether the ref is a Function's, with no run, or a Job's run's.
func (r Ref) Valid() bool {
	if r.Namespace == "" || r.Name == "" {
		return false
	}
	return (r.Kind == v1.KindFunction && r.Run == "") || (r.Kind == v1.KindJob && r.Run != "")
}

// String renders the ref for logs: "<namespace>/<function>" for a Function, "<namespace>/<job>/<run>" for a Job's run.
func (r Ref) String() string {
	if r.Kind == v1.KindJob {
		return string(r.Namespace) + "/" + string(r.Name) + "/" + string(r.Run)
	}
	return string(r.Namespace) + "/" + string(r.Name)
}

// key names the ref's listener: a Function's and a pool worker's by namespace and name, a run's by its kind too, so a
// Job and a Function of one name never share one.
func (r Ref) key() string {
	if r.Kind == v1.KindJob {
		return string(r.Namespace) + "/" + string(r.Kind) + "/" + string(r.Name) + "/" + string(r.Run)
	}
	return string(r.Namespace) + "/" + string(r.Name)
}

// traceparentHeader is the W3C trace-context header (ADR-0165). The handler carries it in the ctx it
// passes to Invoker.Invoke, so the Invoker contract (ADR-0064) keeps its signature and a wrapping
// Invoker (ADR-0147) passes it through.
const traceparentHeader = "traceparent"

type traceparentKey struct{}

// withTraceparent returns ctx carrying tp; an empty tp leaves ctx unchanged.
func withTraceparent(ctx context.Context, tp string) context.Context {
	if tp == "" {
		return ctx
	}
	return context.WithValue(ctx, traceparentKey{}, tp)
}

// traceparentFrom returns the traceparent withTraceparent put in ctx, "" when absent.
func traceparentFrom(ctx context.Context) string {
	tp, _ := ctx.Value(traceparentKey{}).(string)
	return tp
}

// Resolver maps (caller, alias) → the link target, applying the link-as-grant rule (default-deny):
// fault.Forbidden when the caller declares no such link. Same-namespace only (V1.1).
type Resolver interface {
	// Resolve returns the link's target, how it is called (ADR-0243) and the link's timeout.
	Resolve(ctx context.Context, caller Ref, alias string) (target Ref, protocol v1.FunctionProtocol, timeout time.Duration, err error)
}

// Invoker performs the synchronous call to target and returns its output bytes. On a target non-2xx
// (e.g. the shim's 422 input / 500 output contract failures, ADR-0058) it returns an *UpstreamError
// carrying that exact status+body to propagate verbatim; on a transport/cold-wake-timeout failure it
// returns a fault error (e.g. fault.Unavailable → 503).
type Invoker interface {
	Invoke(ctx context.Context, target Ref, input []byte, timeout time.Duration) ([]byte, error)
	// Fetch sends req to target, a Function that serves HTTP, and returns its whole reply (ADR-0243 Decision 6).
	Fetch(ctx context.Context, target Ref, req FetchRequest, timeout time.Duration) (FetchReply, error)
}

// FetchRequest is a context.fetch call: the request the caller's shim sent, its path relative to the target.
type FetchRequest struct {
	Method string
	// Path is percent-encoded as the caller sent it, so an escaped space, % or ? reaches the target as sent.
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// FetchReply is the target's whole reply to a FetchRequest, at most maxInvokeBytes of body.
type FetchReply struct {
	Status int
	Header http.Header
	Body   []byte
}

// UpstreamError carries a target's non-2xx response so the local API propagates it verbatim — the
// daemon does NOT re-validate the contract (the target's shim already did, ADR-0064).
type UpstreamError struct {
	Status int
	Body   []byte
}

func (e *UpstreamError) Error() string { return fmt.Sprintf("upstream returned %d", e.Status) }

// DependencyReport is the first binding of a sandbox's caller that fails its dependency check (ADR-0215 Decision 3),
// the body of a 503 from GET /health/dependencies and, passed through by the shim, from its /health/readiness.
type DependencyReport struct {
	Kind    string `json:"kind"`    // kv | blob | sql | secret | link | socket
	Binding string `json:"binding"` // the alias
	Reason  string `json:"reason"`  // Forbidden | NotFound | NotReady | StorageUnreachable | Timeout | Unreachable | EngineNotReady
	Message string `json:"message"`
}

// The kinds and reasons of a DependencyReport (ADR-0215 Contracts).
const (
	DependencyKV     = "kv"
	DependencyBlob   = "blob"
	DependencyLink   = "link"
	DependencySocket = "socket"
	DependencySecret = "secret" // a bound Secret's secret::read (ADR-0321)

	ReasonForbidden          = "Forbidden"
	ReasonNotFound           = "NotFound"
	ReasonNotReady           = "NotReady"
	ReasonStorageUnreachable = "StorageUnreachable"
	ReasonTimeout            = "Timeout"
	ReasonUnreachable        = "Unreachable"
)

// DependencyChecker checks the bindings of caller (ADR-0215 Decision 3): nil when all pass, else the first failure.
type DependencyChecker interface {
	Check(ctx context.Context, caller Ref) *DependencyReport
}

// DependencyCheckBudget bounds one dependency check: half the reconciler's 100 ms readiness probe, and a pool host's
// bound for all its members (ADR-0215 Decision 4).
const DependencyCheckBudget = 50 * time.Millisecond

// dependenciesPath is the dependency check a shim's readiness asks over its invoke socket (ADR-0215 Decision 3).
const dependenciesPath = "/health/dependencies"

// Option adds an optional part to the local API: the providers' facets (ADR-0310).
type Option func(*options)

type options struct {
	facets []Facet
}

// NewHandler builds the per-sandbox local API handler: POST /invoke/{alias} (ADR-0064/0075), /fetch/{alias}/…
// (ADR-0243) and each facet WithFacets passes, under its own segment (ADR-0310). caller is the fixed
// sandbox identity (connection-scoped) — the handler never reads a caller from the request. authz is the
// PDP (ADR-0075): after the Resolver maps the alias→target (naming; unknown alias ⇒ Forbidden), the
// handler asks it `link::invoke` on the target Function — a deny (a forbid Policy revoking a declared
// link) ⇒ Forbidden, before forwarding. The caller principal is built from the fixed Ref, never the
// request. Every invoke is logged through logger (the broker is the audit point): an allowed call at
// Info, a denial / upstream error at Warn. A nil logger defaults to slog.Default(). deps non-nil registers GET
// /health/dependencies (ADR-0215 Decision 3); nil registers no route, so a shim reads 404 as a pass.
func NewHandler(caller Ref, res Resolver, inv Invoker, authz auth.Authorizer, deps DependencyChecker,
	logger *slog.Logger, opts ...Option) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if !caller.Valid() {
		// a ref of another kind, the zero value included, is no caller (ADR-0351 Decision 2)
		return wrap(logger, "/", nil, func(_ http.ResponseWriter, r *http.Request) error {
			logger.WarnContext(r.Context(), "local API call refused: the caller is no Function and no Job's run", "caller", caller.String(), "kind", caller.Kind)
			return fault.Forbiddenf("workernode.local", "caller %q of kind %q is no Function and no Job's run", caller, caller.Kind)
		})
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	// callerIdentity is connection-scoped: the Cedar principal is the fixed sandbox Ref (ADR-0075),
	// so a request body can never name a different caller.
	callerIdentity := auth.Identity{
		Subject:   caller.String(),
		Principal: &auth.EntityRef{Type: caller.Kind, Namespace: caller.Namespace, Name: caller.Name},
	}
	mux := http.NewServeMux()
	for _, f := range o.facets {
		if f.register != nil {
			f.register(mux, caller, logger)
		}
	}
	if deps != nil {
		registerHealth(mux, logger, caller, deps)
	}
	registerInvoke(mux, logger, caller, callerIdentity, res, inv, authz)
	api := opaqueKeys(mux)
	fetch := wrap(logger, fetchPattern, fetchCauses(), func(w http.ResponseWriter, r *http.Request) error {
		return serveFetch(w, r, caller, callerIdentity, res, inv, authz, logger)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A fetch path is the target's (ADR-0243 Decision 6): it goes around ServeMux, which would clean and decode it.
		if strings.HasPrefix(r.URL.EscapedPath(), fetchPattern) {
			fetch(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

// registerHealth adds GET /health/dependencies (ADR-0215 Decision 3), which answers no error: its 503 carries a
// dependency report, not a problem.
func registerHealth(mux facetMux, logger *slog.Logger, caller Ref, deps DependencyChecker) {
	handle(mux, logger, "GET "+dependenciesPath, []*fault.KnownCause{}, func(w http.ResponseWriter, r *http.Request) error {
		serveDependencies(w, r, caller, deps)
		return nil
	})
}

// registerInvoke adds POST /invoke/{alias} (ADR-0064/0075): the caller's link to alias resolves its target, the PDP
// checks it, and the target's answer comes back, a non-2xx one verbatim, as no error of the endpoint.
func registerInvoke(mux facetMux, logger *slog.Logger, caller Ref, identity auth.Identity, res Resolver, inv Invoker,
	authz auth.Authorizer) {
	causes := slices.Concat(callCauses(), []*fault.KnownCause{ErrTargetServesHTTP})
	handle(mux, logger, "POST /invoke/{alias}", causes, func(w http.ResponseWriter, r *http.Request) error {
		const op = "workernode.local.invoke"
		start := time.Now()
		alias := r.PathValue("alias")
		input, err := readBody(w, r, op, maxInvokeBytes)
		if err != nil {
			return err
		}
		target, protocol, timeout, err := res.Resolve(r.Context(), caller, alias)
		if err != nil {
			logger.Warn("fn-to-fn invoke denied", "caller", caller.String(), "alias", alias, "reason", err.Error())
			return err // Forbidden (no link) / NotFound (unknown target)
		}
		if protocol == v1.ProtocolHTTP {
			return ErrTargetServesHTTP.Errorf(op, "%s serves HTTP; call it with context.fetch, not invoke", target)
		}
		if err := authorizeLink(r.Context(), authz, identity, target, op); err != nil {
			logger.Warn("fn-to-fn invoke refused", "caller", caller.String(), "alias", alias, "target", target.String(), "err", err.Error())
			return err
		}
		out, err := inv.Invoke(withTraceparent(r.Context(), r.Header.Get(traceparentHeader)), target, input, timeout)
		durMs := time.Since(start).Milliseconds()
		if err != nil {
			var ue *UpstreamError
			if errors.As(err, &ue) { // propagate the target shim's status+body verbatim (422/500)
				logger.Warn("fn-to-fn invoke upstream error", "caller", caller.String(), "alias", alias,
					"target", target.String(), "status", ue.Status, "durationMs", durMs)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(ue.Status)
				_, _ = w.Write(ue.Body)
				return nil
			}
			if fe := (*fault.Error)(nil); errors.As(err, &fe) && fe.Op == NestedCapOp {
				logger.Warn("fn-to-fn invoke refused: nested in-flight cap", "caller", caller.String(), "alias", alias,
					"target", target.String(), "err", err.Error())
				return err // 429, no Retry-After (ADR-0147)
			}
			logger.Warn("fn-to-fn invoke failed", "caller", caller.String(), "alias", alias,
				"target", target.String(), "durationMs", durMs, "err", err.Error())
			return err // transport / cold-wake timeout → 503 etc.
		}
		logger.Info("fn-to-fn invoke", "caller", caller.String(), "alias", alias,
			"target", target.String(), "durationMs", durMs)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
		return nil
	})
}

// callCauses are the causes that invoke and fetch both answer, which funcd raises itself: the target's reply, whatever
// its status, is no error of theirs.
func callCauses() []*fault.KnownCause {
	return []*fault.KnownCause{ErrBodyTooLarge, ErrBodyUnreadable, ErrNoLink, ErrLinkTargetMissing, ErrLinkDenied,
		ErrNestedCapReached, ErrReplyCutOff, ErrReplyTooLarge}
}

// FetchReplyHeader marks a context.fetch answer that is the target's reply, so the shim tells it from a call the local
// API refused before it reached the target (ADR-0243 Decision 6).
const FetchReplyHeader = "X-Funcd-Fetch-Reply"

// serveFetch answers a context.fetch call, /fetch/<alias><path> (ADR-0243 Decision 6): the same link, PDP and
// nested-cap checks as invoke, a target that serves HTTP, and the target's reply written back whole with its status
// and headers. The path reaches the target percent-encoded as the caller sent it.
func serveFetch(w http.ResponseWriter, r *http.Request, caller Ref, identity auth.Identity, res Resolver,
	inv Invoker, authz auth.Authorizer, logger *slog.Logger) error {
	const op = "workernode.local.fetch"
	rawAlias, path, found := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), fetchPattern), "/")
	alias, aerr := url.PathUnescape(rawAlias)
	if !found || aerr != nil || alias == "" {
		return ErrFetchPathMalformed.Errorf(op, "no fetch route %s: want /fetch/<alias>/<path>", r.URL.EscapedPath())
	}
	body, err := readBody(w, r, op, maxInvokeBytes)
	if err != nil {
		return err
	}
	target, protocol, timeout, err := res.Resolve(r.Context(), caller, alias)
	if err != nil {
		logger.Warn("fn-to-fn fetch denied", "caller", caller.String(), "alias", alias, "reason", err.Error())
		return err
	}
	if protocol != v1.ProtocolHTTP {
		return ErrTargetNotHTTP.Errorf(op, "%s does not serve HTTP; call it with context.invoke, not fetch", target)
	}
	if err := authorizeLink(r.Context(), authz, identity, target, op); err != nil {
		logger.Warn("fn-to-fn fetch refused", "caller", caller.String(), "alias", alias, "target", target.String(), "err", err.Error())
		return err
	}
	header := r.Header.Clone()
	header.Del(MemberHeader)
	oidc.Strip(header) // only the edge's sign-in sets the principal headers (ADR-0335 Decision 8)
	reply, err := inv.Fetch(withTraceparent(r.Context(), r.Header.Get(traceparentHeader)), target, FetchRequest{
		Method: r.Method, Path: "/" + path, RawQuery: r.URL.RawQuery, Header: header, Body: body,
	}, timeout)
	if err != nil {
		logger.Warn("fn-to-fn fetch failed", "caller", caller.String(), "alias", alias, "target", target.String(), "err", err.Error())
		return err
	}
	for key, values := range reply.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.Header().Set(FetchReplyHeader, "1")
	w.WriteHeader(reply.Status)
	_, _ = w.Write(reply.Body)
	return nil
}

// authorizeLink is the PDP gate of a link call (ADR-0075): a declared link is permitted by the built-in unless a
// forbid Policy revokes it. The principal is connection-scoped, the resource is the target. A nil authz allows it.
func authorizeLink(ctx context.Context, authz auth.Authorizer, identity auth.Identity, target Ref, op string) error {
	if authz == nil {
		return nil
	}
	dec, err := authz.Authorize(ctx, auth.Request{
		Identity: identity,
		Action:   auth.ActionLinkInvoke,
		Resource: &auth.EntityRef{Type: v1.KindFunction, Namespace: target.Namespace, Name: target.Name},
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "authorize link::invoke")
	}
	if !dec.Allowed {
		return ErrLinkDenied.Errorf(op, "caller %s is not authorized to invoke %s: %s", identity.Subject, target.String(), dec.Reason)
	}
	return nil
}

// serveDependencies answers caller's dependency check within DependencyCheckBudget: 200, or 503 with the report; a
// check that runs past the budget is a 503 Timeout.
func serveDependencies(w http.ResponseWriter, r *http.Request, caller Ref, deps DependencyChecker) {
	ctx, cancel := context.WithTimeout(r.Context(), DependencyCheckBudget)
	defer cancel()
	done := make(chan *DependencyReport, 1)
	go func() { done <- deps.Check(ctx, caller) }()
	var report *DependencyReport
	select {
	case report = <-done:
	case <-ctx.Done():
		report = &DependencyReport{Kind: DependencySocket, Reason: ReasonTimeout,
			Message: "the dependency check did not finish within " + DependencyCheckBudget.String()}
	}
	if report == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(report)
}

// opaqueKeys keeps http.ServeMux from cleaning a KV or blob key: the shims send a key's "/" separators
// raw, and ServeMux redirects a path with an empty or dot segment ("/kv/b/a//b", "/kv/b/..") before any
// handler runs (issue #100). Keys are opaque strings, so the key part of the path is escaped into one
// canonical segment, which the {key...} wildcard unescapes back verbatim.
func opaqueKeys(mux http.Handler) http.Handler {
	keyEscaper := strings.NewReplacer("/", "%2F", ".", "%2E")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, route := range []string{"/kv/", "/blob/"} {
			rest, ok := strings.CutPrefix(r.URL.EscapedPath(), route)
			if !ok {
				continue
			}
			if binding, key, ok := strings.Cut(rest, "/"); ok {
				rawPath := route + binding + "/" + keyEscaper.Replace(key)
				if path, err := url.PathUnescape(rawPath); err == nil {
					r2 := new(http.Request)
					*r2 = *r
					r2.URL = new(url.URL)
					*r2.URL = *r.URL
					r2.URL.Path, r2.URL.RawPath = path, rawPath
					r = r2
				}
			}
			break
		}
		mux.ServeHTTP(w, r)
	})
}

// readBody reads r's body capped at limit bytes (a DoS guard). An over-cap body is PayloadTooLarge
// (413, as on the data plane, ADR-0134); any other read failure is Invalid. Every client shares both causes.
func readBody(w http.ResponseWriter, r *http.Request, op string, limit int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err == nil {
		return body, nil
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, ErrBodyTooLarge.Errorf(op, "request body exceeds %d bytes", limit)
	}
	return nil, ErrBodyUnreadable.Errorf(op, "could not read request body")
}

// newServer builds the local API's http.Server. IdleTimeout closes a keep-alive connection left idle
// between requests (a DoS guard on the local API: each held connection pins a daemon goroutine); it
// outlasts the TypeScript shim's 5 s client keep-alive, so the client closes an idle connection first.
// ErrorLog sends net/http's own errors through logger, not the stdlib log package (#454).
func newServer(h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: serverHeaderTimeout, IdleTimeout: serverIdleTimeout,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn)}
}

// listen binds the local API on network at addr, a Unix socket's path after clearing a stale socket file from a
// prior sandbox, or a dev container worker's "127.0.0.1:0" (ADR-0346 Decision 6), and builds h's server logging
// through logger, closed once ctx is done; the caller runs srv.Serve(ln). Serve, Manager.SocketFor and
// Manager.ListenerFor all bind through it, so they cannot drift (issue #357).
func listen(ctx context.Context, op, network, addr string, h http.Handler, logger *slog.Logger) (net.Listener, *http.Server, error) {
	if network == "unix" {
		_ = os.Remove(addr)
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Unavailable, op, "listen on %s socket %q", network, addr)
	}
	srv := newServer(h, logger)
	context.AfterFunc(ctx, func() { _ = srv.Close() })
	return ln, srv, nil
}

// Serve runs h on a Unix domain socket at path (bind-mounted into the sandbox) until ctx is done.
// A stale socket file at path is removed first. The server's own errors are logged through logger (nil means
// slog.Default()).
func Serve(ctx context.Context, path string, h http.Handler, logger *slog.Logger) error {
	const op = "workernode.local.Serve"
	ln, srv, err := listen(ctx, op, "unix", path, h, cmp.Or(logger, slog.Default()))
	if err != nil {
		return err
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fault.Wrapf(err, fault.Unavailable, op, "serve local API on %q", path)
	}
	return nil
}
