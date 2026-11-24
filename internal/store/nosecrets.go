package store

import (
	"bytes"
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// WithoutSecrets is src, a worker node's metastore, as the node's backups read it (FEAT-0002 F251): every record but the
// Secrets. They are copies the control plane holds, sealed with the node's own key, so a node restored on a new
// machine takes them again from the control plane instead of from a backup it could not open.
func WithoutSecrets(src snapshot.Source) snapshot.Source { return withoutSecrets{src} }

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
