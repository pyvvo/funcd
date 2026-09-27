package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// maxBlobBytes caps a blob value buffered in memory (a DoS guard on the local API, ADR-0127). Larger
// than the KV cap (blob objects are parquet/PDF, not small values) but bounded — v1 is bytes-in-memory,
// so streaming (which lifts this cap) is the named v2 follow-up.
const maxBlobBytes = 64 << 20 // 64 MiB

// Blob is the function-facing blob port the worker-node local API routes to — the services/blob.Facade
// satisfies it. The caller's namespace + function are supplied by the handler (connection-scoped from the
// sandbox's fixed Ref), NEVER read from the request: a function can only reach its own namespace's blob,
// and its binding resolution is keyed by its trustworthy fixed function identity (ADR-0127/0073).
type Blob interface {
	Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) ([]byte, bool, error)
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, data []byte) error
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error
	List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, prefix string) ([]string, error)
	SignedURL(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, opts blob.SignOptions) (string, error)
}

// registerBlob adds the blob verbs to mux — GET/PUT/DELETE /blob/{binding}/{key...}, GET /blob/{binding}
// (list, ?prefix=…), and GET /blob/{binding}/{key...}?sign=1 (a presigned URL; ?method=GET|PUT|DELETE,
// ?expiry=<dur>) — routed to b with the sandbox's fixed namespace + function (ADR-0127/0073). Errors are
// RFC 9457 (binding/owner denial → 403, missing object → 404, over-cap/bad input → 413/422, engine → 500).
func registerBlob(mux *http.ServeMux, caller Ref, b Blob, logger *slog.Logger) {
	ns := caller.Namespace
	fn := caller.Function

	mux.HandleFunc("GET /blob/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.blob.get"
		binding := r.PathValue("binding")
		key := r.PathValue("key")
		if r.URL.Query().Get("sign") != "" {
			url, err := b.SignedURL(r.Context(), ns, fn, binding, key, signOptsFromQuery(r))
			if err != nil {
				logger.Warn("blob sign denied/failed", "caller", caller.String(), "binding", binding, "err", err.Error())
				fault.WriteProblem(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, url)
			return
		}
		v, found, err := b.Get(r.Context(), ns, fn, binding, key)
		if err != nil {
			logger.Warn("blob get denied/failed", "caller", caller.String(), "binding", binding, "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		if !found {
			fault.WriteProblem(w, fault.NotFoundf(op, "object %q not found in binding %q", key, binding))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(v)
	})

	mux.HandleFunc("PUT /blob/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.blob.put"
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBlobBytes))
		if err != nil {
			fault.WriteProblem(w, fault.Invalidf(op, "read object (max %d bytes): %v", maxBlobBytes, err))
			return
		}
		if err := b.Put(r.Context(), ns, fn, r.PathValue("binding"), r.PathValue("key"), body); err != nil {
			logger.Warn("blob put denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /blob/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Delete(r.Context(), ns, fn, r.PathValue("binding"), r.PathValue("key")); err != nil {
			logger.Warn("blob delete denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /blob/{binding}", func(w http.ResponseWriter, r *http.Request) {
		keys, err := b.List(r.Context(), ns, fn, r.PathValue("binding"), r.URL.Query().Get("prefix"))
		if err != nil {
			logger.Warn("blob list denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		if keys == nil {
			keys = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keys)
	})
}

// signOptsFromQuery derives blob.SignOptions from the ?method= + ?expiry= query params (a zero/absent
// method is a GET; a zero/unparseable expiry is the driver's default).
func signOptsFromQuery(r *http.Request) blob.SignOptions {
	opts := blob.SignOptions{}
	switch r.URL.Query().Get("method") {
	case "PUT":
		opts.Method = blob.SignPut
	case "DELETE":
		opts.Method = blob.SignDelete
	default:
		opts.Method = blob.SignGet
	}
	if e := r.URL.Query().Get("expiry"); e != "" {
		if d, err := time.ParseDuration(e); err == nil {
			opts.Expiry = d
		}
	}
	return opts
}
