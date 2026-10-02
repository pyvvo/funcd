package function

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime"
)

// readyReplicas judges only the replicas below its bound (ADR-0142): a Failed replica that is being scaled away is
// not a shape failure.
func TestReadyReplicasIgnoresReplicasAtOrAboveBound(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	ctx := context.Background()
	inst, err := r.runtime.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", Name: "gone", Revision: "gone-1", Replica: 1,
		Command: []string{"sh", "-c", "exit 3"}, LogPath: filepath.Join(t.TempDir(), "w.log"),
	})
	require.NoError(t, err)
	require.NoError(t, r.runtime.Start(ctx, inst.ID))
	require.Eventually(t, func() bool {
		in, serr := r.runtime.Status(ctx, inst.ID)
		return serr == nil && in.State == runtime.StateFailed
	}, 5*time.Second, 10*time.Millisecond)

	_, failed := r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 1, readinessPath, bootTimeout)
	require.False(t, failed, "replica 1 is at the bound, so it is not judged")
	_, failed = r.readyReplicas(ctx, "default", "gone", "gone-1", 0, 2, readinessPath, bootTimeout)
	require.True(t, failed, "inside the bound, a Failed replica is a shape failure")
}
