package local

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Secrets is the function-facing secret port the local API routes to (ADR-0249): the keys and values of the Secrets
// the caller binds, checked on every read. As for KV, the caller's namespace and function come from the sandbox's
// fixed Ref, never from the request.
type Secrets interface {
	List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) ([]string, error)
	Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, key string) (value string, found bool, err error)
	// Put and Delete write one key of Secret binding, which the caller binds and may write (ADR-0327).
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key, value string) error
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error
}

// maxSecretBytes caps a write's body, as SQL's does (ADR-0327 Decision 4).
const maxSecretBytes = 1 << 20

// SecretFacet serves GET /secret and GET /secret/{key} over s (ADR-0249), and PUT and DELETE /secret/{binding}/{key}
// (ADR-0327).
func SecretFacet(s Secrets) Facet {
	return Facet{segment: "secret", register: func(mux facetMux, caller Ref, logger *slog.Logger) {
		registerSecrets(mux, caller, s, logger)
	}}
}

// secretKeys and secretValue are the bodies of GET /secret and GET /secret/{key}.
type secretKeys struct {
	Keys []string `json:"keys"`
}

type secretValue struct {
	Value string `json:"value"`
}

// registerSecrets adds GET /secret and GET /secret/{key}, and PUT and DELETE /secret/{binding}/{key}, routed to s with
// the sandbox's fixed namespace and function. A key no bound Secret holds is 404; a failed check is its problem
// document (403 on a deny). Values are never logged.
func registerSecrets(mux facetMux, caller Ref, s Secrets, logger *slog.Logger) {
	ns, fn := caller.Namespace, caller.Function
	mux.HandleFunc("GET /secret", func(w http.ResponseWriter, r *http.Request) {
		keys, err := s.List(r.Context(), ns, fn)
		if err != nil {
			logger.Warn("secret list denied/failed", "caller", caller.String(), "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		writeSecret(w, secretKeys{Keys: keys})
	})
	mux.HandleFunc("GET /secret/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		value, found, err := s.Get(r.Context(), ns, fn, key)
		if err == nil && !found {
			err = fault.NotFoundf("workernode.local.secret.get", "no Secret that %s binds holds key %q", fn, key)
		}
		if err != nil {
			logger.Warn("secret get denied/failed", "caller", caller.String(), "key", key, "err", err.Error())
			fault.WriteProblem(w, err)
			return
		}
		writeSecret(w, secretValue{Value: value})
	})
	mux.HandleFunc("PUT /secret/{binding}/{key}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.secret.put"
		binding, key := r.PathValue("binding"), r.PathValue("key")
		body, err := readBody(w, r, op, maxSecretBytes)
		var in secretValue
		if err == nil && json.Unmarshal(body, &in) != nil {
			err = fault.Invalidf(op, `the body must be {"value": "<text>"}`)
		}
		if err == nil {
			err = s.Put(r.Context(), ns, fn, binding, key, in.Value)
		}
		writeSecretChange(w, logger, caller, "put", binding, key, err)
	})
	mux.HandleFunc("DELETE /secret/{binding}/{key}", func(w http.ResponseWriter, r *http.Request) {
		binding, key := r.PathValue("binding"), r.PathValue("key")
		writeSecretChange(w, logger, caller, "delete", binding, key, s.Delete(r.Context(), ns, fn, binding, key))
	})
}

// writeSecretChange answers a write: 204, or its problem document, logged with no value.
func writeSecretChange(w http.ResponseWriter, logger *slog.Logger, caller Ref, verb, binding, key string, err error) {
	if err != nil {
		logger.Warn("secret "+verb+" denied/failed", "caller", caller.String(), "binding", binding, "key", key, "err", err.Error())
		fault.WriteProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeSecret writes a successful secret read.
func writeSecret[T secretKeys | secretValue](w http.ResponseWriter, v T) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
