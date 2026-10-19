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
}

// WithSecrets routes GET /secret and GET /secret/{key} to s (ADR-0249); without it there are no /secret routes.
func WithSecrets(s Secrets) Option { return func(o *options) { o.secrets = s } }

// secretKeys and secretValue are the bodies of GET /secret and GET /secret/{key}.
type secretKeys struct {
	Keys []string `json:"keys"`
}

type secretValue struct {
	Value string `json:"value"`
}

// registerSecrets adds GET /secret and GET /secret/{key}, routed to s with the sandbox's fixed namespace and function.
// A key no bound Secret holds is 404; a failed check is its problem document (403 on a deny). Values are never logged.
func registerSecrets(mux *http.ServeMux, caller Ref, s Secrets, logger *slog.Logger) {
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
}

// writeSecret writes a successful secret read.
func writeSecret[T secretKeys | secretValue](w http.ResponseWriter, v T) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
