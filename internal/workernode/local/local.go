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
	"net"
	"net/http"
	"os"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// maxInvokeBytes caps an invoke request body (a DoS guard on the local API).
const maxInvokeBytes = 1 << 20 // 1 MiB

// Ref identifies a function replica's caller identity (namespace + function).
type Ref struct {
	Namespace v1.NamespaceName
	Function  v1.ObjectName
}

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

// NewHandler builds the per-sandbox local API handler: POST /invoke/{alias}. caller is the fixed
// sandbox identity (connection-scoped) — the handler never reads a caller from the request.
func NewHandler(caller Ref, res Resolver, inv Invoker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /invoke/{alias}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.invoke"
		alias := r.PathValue("alias")
		input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInvokeBytes))
		if err != nil {
			fault.WriteProblem(w, fault.Invalidf(op, "read request body: %v", err))
			return
		}
		target, timeout, err := res.Resolve(r.Context(), caller, alias)
		if err != nil {
			fault.WriteProblem(w, err) // Forbidden (no link) / NotFound (unknown target)
			return
		}
		out, err := inv.Invoke(r.Context(), target, input, timeout)
		if err != nil {
			var ue *UpstreamError
			if errors.As(err, &ue) { // propagate the target shim's status+body verbatim (422/500)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(ue.Status)
				_, _ = w.Write(ue.Body)
				return
			}
			fault.WriteProblem(w, err) // transport / cold-wake timeout → 503 etc.
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	return mux
}

// Serve runs h on a Unix domain socket at path (bind-mounted into the sandbox) until ctx is done.
// A stale socket file at path is removed first.
func Serve(ctx context.Context, path string, h http.Handler) error {
	const op = "workernode.local.Serve"
	_ = os.Remove(path) // clear a stale socket from a prior sandbox
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "listen on unix socket %q", path)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fault.Wrapf(err, fault.Unavailable, op, "serve local API on %q", path)
	}
	return nil
}
