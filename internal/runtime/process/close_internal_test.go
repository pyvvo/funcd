package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// Close stops the running instances at once: each one that ignores SIGTERM holds its full stop grace, so stopping
// them one after another made a daemon with N such workers exit N stop graces after SIGTERM, past the 15 s
// shutdown bound (issue #143).
func TestIssue143_CloseStopsInstancesInParallel(t *testing.T) {
	const workers = 3
	ctx := context.Background()
	d := New()
	dir := t.TempDir()
	for i := range workers {
		ready := filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		spec := runtime.WorkerSpec{
			Namespace: "default", OwnerKind: v1alpha1.KindFunction, Name: "stubborn", Replica: i,
			Command: []string{"sh", "-c", `trap "" TERM; touch "$0"; exec sleep 60`, ready},
		}
		inst, err := d.Create(ctx, spec)
		require.NoError(t, err)
		require.NoError(t, d.Start(ctx, inst.ID))
		require.Eventually(t, func() bool { _, err := os.Stat(ready); return err == nil }, 10*time.Second, 10*time.Millisecond,
			"worker %d ignores SIGTERM", i)
	}

	start := time.Now()
	require.NoError(t, d.Close())
	elapsed := time.Since(start)
	require.Less(t, elapsed, 2*defaultStopGrace, "Close took %s for %d workers that ignore SIGTERM: it stops them one at a time", elapsed, workers)
}
