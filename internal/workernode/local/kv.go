package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// maxKVBytes caps a KV value (a DoS guard on the local API, matching the invoke cap).
const maxKVBytes = 1 << 20 // 1 MiB

// KV is the function-facing KV port the worker-node local API routes to — the services/kv.Facade
// satisfies it. The caller's namespace + identity are supplied by the handler (connection-scoped from
// the sandbox's fixed Ref), NEVER read from the request: a function can only reach its own namespace's KV.
type KV interface {
	Get(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) ([]byte, bool, error)
	Put(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, value []byte) error
	Delete(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) error
	List(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, prefix string) ([]string, error)
}

// sandboxIdentity derives the connection-scoped identity for a sandbox from its caller Ref: a
// namespace-scoped developer identity (V1.1 — fine-grained per-workload `Grant` authz is V2, ADR-0018/0019).
func sandboxIdentity(caller Ref) auth.Identity {
	return auth.Identity{
		Subject:    "function:" + caller.String(),
		Role:       auth.RoleDeveloper,
		Namespaces: []v1.NamespaceName{caller.Namespace},
	}
}

// registerKV adds the KV verbs to mux — GET/PUT/DELETE /kv/{binding}/{key...} and GET /kv/{binding} (list,
// ?prefix=…) — routed to kv with the sandbox's fixed identity + namespace (ADR-0069). Errors are RFC 9457
// (the Facade's PDP denial → 403, missing key → 404, bad input → 422, engine error → 500).
func registerKV(mux *http.ServeMux, caller Ref, kv KV, logger *slog.Logger) {
	id := sandboxIdentity(caller)
	ns := caller.Namespace

	mux.HandleFunc("GET /kv/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.kv.get"
		v, found, err := kv.Get(r.Context(), id, ns, r.PathValue("binding"), r.PathValue("key"))
		if err != nil {
			logger.Warn("kv get denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		if !found {
			fault.WriteProblem(w, fault.NotFoundf(op, "key %q not found in binding %q", r.PathValue("key"), r.PathValue("binding")))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(v)
	})

	mux.HandleFunc("PUT /kv/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.kv.put"
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxKVBytes))
		if err != nil {
			fault.WriteProblem(w, fault.Invalidf(op, "read value: %v", err))
			return
		}
		if err := kv.Put(r.Context(), id, ns, r.PathValue("binding"), r.PathValue("key"), body); err != nil {
			logger.Warn("kv put denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /kv/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		if err := kv.Delete(r.Context(), id, ns, r.PathValue("binding"), r.PathValue("key")); err != nil {
			logger.Warn("kv delete denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /kv/{binding}", func(w http.ResponseWriter, r *http.Request) {
		keys, err := kv.List(r.Context(), id, ns, r.PathValue("binding"), r.URL.Query().Get("prefix"))
		if err != nil {
			logger.Warn("kv list denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
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
