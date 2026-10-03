// Package local is the per-sandbox worker-node local API (ADR-0064): an HTTP-over-UDS surface the
// runtime shim calls for the synchronous fn-to-fn invoke verb (context.invoke). One listener per
// sandbox; the caller Ref is fixed at provisioning, so identity is connection-scoped — a request
// body can never name a different caller. No published SDK — reached only through the built-in shim.
//
// It nests under the blueprint's internal/workernode node-agent component.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// maxInvokeBytes caps an invoke request body and the target's response (a DoS guard on the local API).
const maxInvokeBytes = 1 << 20 // 1 MiB

// Ref identifies a function replica's caller identity (namespace + function).
type Ref struct {
	Namespace v1.NamespaceName
	Function  v1.ObjectName
}

// String renders the ref as "<namespace>/<function>" for logs.
func (r Ref) String() string { return string(r.Namespace) + "/" + string(r.Function) }

// Resolver maps (caller, alias) → the link target, applying the link-as-grant rule (default-deny):
// fault.Forbidden when the caller declares no such link. Same-namespace only (V1.1).
type Resolver interface {
	Resolve(ctx context.Context, caller Ref, alias string) (target Ref, timeout time.Duration, err error)
}

// Invoker performs the synchronous call to target and returns its output bytes. On a target non-2xx
// (e.g. the shim's 422 input / 500 output contract failures, ADR-0058) it returns an *UpstreamError
// carrying that exact status+body to propagate verbatim; on a transport/cold-wake-timeout failure it
// returns a fault error (e.g. fault.Unavailable → 503).
type Invoker interface {
	Invoke(ctx context.Context, target Ref, input []byte, timeout time.Duration) ([]byte, error)
}

// UpstreamError carries a target's non-2xx response so the local API propagates it verbatim — the
// daemon does NOT re-validate the contract (the target's shim already did, ADR-0064).
type UpstreamError struct {
	Status int
	Body   []byte
}

func (e *UpstreamError) Error() string { return fmt.Sprintf("upstream returned %d", e.Status) }

// NewHandler builds the per-sandbox local API handler: POST /invoke/{alias} (ADR-0064/0075) plus, when
// kv is non-nil, the KV verbs GET/PUT/DELETE /kv/{binding}/{key} + list (ADR-0069). caller is the fixed
// sandbox identity (connection-scoped) — the handler never reads a caller from the request. authz is the
// PDP (ADR-0075): after the Resolver maps the alias→target (naming; unknown alias ⇒ Forbidden), the
// handler asks it `link::invoke` on the target Function — a deny (a forbid Policy revoking a declared
// link) ⇒ Forbidden, before forwarding. The caller principal is built from the fixed Ref, never the
// request. Every invoke is logged through logger (the broker is the audit point): an allowed call at
// Info, a denial / upstream error at Warn. A nil logger defaults to slog.Default().
func NewHandler(caller Ref, res Resolver, inv Invoker, authz auth.Authorizer, kv KV, blob Blob, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	// callerIdentity is connection-scoped: the Cedar principal is the fixed sandbox Ref (ADR-0075),
	// so a request body can never name a different caller.
	callerIdentity := auth.Identity{
		Subject:   caller.String(),
		Principal: &auth.EntityRef{Type: v1.KindFunction, Namespace: caller.Namespace, Name: caller.Function},
	}
	mux := http.NewServeMux()
	if kv != nil {
		registerKV(mux, caller, kv, logger)
	}
	if blob != nil {
		registerBlob(mux, caller, blob, logger)
	}
	mux.HandleFunc("POST /invoke/{alias}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.invoke"
		start := time.Now()
		alias := r.PathValue("alias")
		input, err := readBody(w, r, op, maxInvokeBytes)
		if err != nil {
			fault.WriteProblem(w, err)
			return
		}
		target, timeout, err := res.Resolve(r.Context(), caller, alias)
		if err != nil {
			logger.Warn("fn-to-fn invoke denied", "caller", caller.String(), "alias", alias, "reason", err.Error())
			fault.WriteProblem(w, err) // Forbidden (no link) / NotFound (unknown target)
			return
		}
		// PDP gate (ADR-0075): a declared link is permitted by the built-in unless a forbid Policy
		// revokes it. The principal is connection-scoped (callerIdentity), the resource is the target.
		if authz != nil {
			dec, derr := authz.Authorize(r.Context(), auth.Request{
				Identity: callerIdentity,
				Action:   auth.ActionLinkInvoke,
				Resource: &auth.EntityRef{Type: v1.KindFunction, Namespace: target.Namespace, Name: target.Function},
			})
			if derr != nil {
				logger.Warn("fn-to-fn invoke authz error", "caller", caller.String(), "alias", alias,
					"target", target.String(), "err", derr.Error())
				fault.WriteProblem(w, fault.Wrapf(derr, fault.KindOf(derr), op, "authorize link::invoke"))
				return
			}
			if !dec.Allowed {
				logger.Warn("fn-to-fn invoke denied by policy", "caller", caller.String(), "alias", alias,
					"target", target.String(), "reason", dec.Reason)
				fault.WriteProblem(w, fault.Forbiddenf(op, "caller %s is not authorized to invoke %s: %s", caller.String(), target.String(), dec.Reason))
				return
			}
		}
		out, err := inv.Invoke(r.Context(), target, input, timeout)
		durMs := time.Since(start).Milliseconds()
		if err != nil {
			var ue *UpstreamError
			if errors.As(err, &ue) { // propagate the target shim's status+body verbatim (422/500)
				logger.Warn("fn-to-fn invoke upstream error", "caller", caller.String(), "alias", alias,
					"target", target.String(), "status", ue.Status, "durationMs", durMs)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(ue.Status)
				_, _ = w.Write(ue.Body)
				return
			}
			logger.Warn("fn-to-fn invoke failed", "caller", caller.String(), "alias", alias,
				"target", target.String(), "durationMs", durMs, "err", err.Error())
			fault.WriteProblem(w, err) // transport / cold-wake timeout → 503 etc.
			return
		}
		logger.Info("fn-to-fn invoke", "caller", caller.String(), "alias", alias,
			"target", target.String(), "durationMs", durMs)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	return opaqueKeys(mux)
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
// (413, as on the data plane, ADR-0134); any other read failure is Invalid.
func readBody(w http.ResponseWriter, r *http.Request, op string, limit int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err == nil {
		return body, nil
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, fault.PayloadTooLargef(op, "request body exceeds %d bytes", limit)
	}
	return nil, fault.Invalidf(op, "read request body: %v", err)
}

// newServer builds the local API's http.Server. IdleTimeout closes a keep-alive connection left idle
// between requests (a DoS guard on the local API: each held connection pins a daemon goroutine); it
// outlasts the TypeScript shim's 5 s client keep-alive, so the client closes an idle connection first.
// ErrorLog sends net/http's own errors through logger, not the stdlib log package (#454).
func newServer(h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn)}
}

// listen binds the local API's Unix socket at path, after clearing a stale socket file from a prior
// sandbox, and builds h's server logging through logger, closed once ctx is done; the caller runs
// srv.Serve(ln). Serve and Manager.SocketFor both bind through it, so the two cannot drift (issue #357).
func listen(ctx context.Context, op, path string, h http.Handler, logger *slog.Logger) (net.Listener, *http.Server, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Unavailable, op, "listen on unix socket %q", path)
	}
	srv := newServer(h, logger)
	context.AfterFunc(ctx, func() { _ = srv.Close() })
	return ln, srv, nil
}

// Serve runs h on a Unix domain socket at path (bind-mounted into the sandbox) until ctx is done.
// A stale socket file at path is removed first. The server's own errors are logged through slog.Default().
func Serve(ctx context.Context, path string, h http.Handler) error {
	const op = "workernode.local.Serve"
	ln, srv, err := listen(ctx, op, path, h, slog.Default())
	if err != nil {
		return err
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fault.Wrapf(err, fault.Unavailable, op, "serve local API on %q", path)
	}
	return nil
}
