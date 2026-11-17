package funcd

import (
	"bytes"
	"context"
	"net/http"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// readOnly answers only the reads of a worker node's own API (FEAT-0002): a node's namespaces are written on the
// control plane, and a write here would be undone by the next change the node follows.
func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "this is a worker node: its namespaces are written on the control plane", http.StatusMethodNotAllowed)
		}
	})
}

// withoutSecrets is a worker node's metastore as its platform backup reads it (FEAT-0002 F251): every record but the
// Secrets. They are copies the control plane holds, sealed with the node's own key, so a node restored on a new
// machine takes them again from the control plane instead of from a backup it could not open.
type withoutSecrets struct{ snapshot.Source }

// secretRecords is the start of every Secret's record key: its bucket, then NUL (ADR-0202).
var secretRecords = []byte(v1.KindSecret.GVK().String() + "\x00") //nolint:gochecknoglobals // a fixed key prefix

func (w withoutSecrets) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	return w.Source.Snapshot(ctx, func(r snapshot.Record) error {
		if bytes.HasPrefix(r.Key, secretRecords) {
			return nil
		}
		return emit(r)
	})
}

// ClusterAddr is the node API's bound address on a control plane that worker nodes join (WithCluster); "" ⇒ none.
func (p *Platform) ClusterAddr() string {
	if p.cluster == nil {
		return ""
	}
	return p.cluster.Addr()
}
