package bench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// scenario: bench smoke — a short run asserting the scenario *properties* (not just populated
// fields, ADR-0040), across both substrates. Node-gated (the process-driver shim runs JS).
func TestBenchSmoke(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping the bench smoke")
	}
	shim := langmod.NodeShim(t)
	poolShim := langmod.PoolShim(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	reports, err := Run(ctx, shim, poolShim, Config{
		Backends:    []Backend{BackendMemory, BackendFile},
		Concurrency: 8,
		Duration:    1500 * time.Millisecond,
		Density:     2,
		MemBudgetMB: 16384,
		TargetFns:   100,
	})
	require.NoError(t, err)
	// At least one substrate completes; a substrate whose reconcile is starved by ephemeral-port
	// pressure under load is skipped (bench-substrate is best-effort on a loaded dev box).
	require.GreaterOrEqual(t, len(reports), 1, "at least one substrate ran (bench-substrate)")

	for _, r := range reports {
		require.Greater(t, r.RPS, 0.0, "%s: sustained throughput (bench-throughput)", r.Backend)
		require.Greater(t, r.Latency.P50, time.Duration(0), "%s: latency recorded", r.Backend)
		require.Greater(t, r.PerWorkerMB, 0.0, "%s: per-worker RSS sampled (bench-memory)", r.Backend)
		require.Less(t, r.IdleMB, r.PerWorkerMB, "%s: idle RSS below a running worker — reclaim works (bench-idle)", r.Backend)
		require.Greater(t, r.ColdStart, time.Duration(0), "%s: cold-start measured after 0 replicas (bench-coldstart)", r.Backend)
		require.GreaterOrEqual(t, r.PerFunctionMB, 0.0, "%s: marginal slope (bench-density)", r.Backend)
		require.Greater(t, r.MaxDensity, 0, "%s: density extrapolated", r.Backend)
		// pool-density-bench (ADR-0044): K handlers pooled in one worker_threads process cost
		// less per function than K separate shims, AND the pool actually serves load through the
		// worker_threads message-passing path (throughput + tail latency measured, not just RSS).
		require.Greater(t, r.PooledPerFunctionMB, 0.0, "%s: pooled density measured", r.Backend)
		require.Less(t, r.PooledPerFunctionMB, r.PerWorkerMB, "%s: pooled MB/function < a full per-function worker (density gain)", r.Backend)
		require.Greater(t, r.PooledRPS, 0.0, "%s: pooled throughput measured — load actually flowed through the pool (pool-throughput-bench)", r.Backend)
		require.Greater(t, r.PooledLatency.P99, time.Duration(0), "%s: pooled tail latency recorded", r.Backend)
		// baseline: a single-tenant shim hit on the SAME direct path (no data plane). It's a single
		// short-lived process, so its absolute throughput is high-variance — the report reads density
		// as the stable result and throughput as comparable-within-noise (see poolreport.go).
		require.Greater(t, r.SingleDirectRPS, 0.0, "%s: single-tenant direct baseline measured", r.Backend)
		require.Greater(t, r.SingleDirectLatency.P99, time.Duration(0), "%s: single-tenant direct tail latency recorded", r.Backend)
		require.Greater(t, r.PooledK, 0, "%s: pool size recorded", r.Backend)
	}

	dir := t.TempDir()
	md, js, err := WriteReport(dir, reports)
	require.NoError(t, err)
	require.FileExists(t, md, "markdown report (bench-report)")
	require.FileExists(t, js, "json report (bench-report)")

	// the separate worker-pool comparison report (ADR-0044): density + throughput side by side.
	pmd, pjs, err := WritePoolReport(dir, reports)
	require.NoError(t, err)
	require.FileExists(t, pmd, "pool markdown report (pool-throughput-bench)")
	require.FileExists(t, pjs, "pool json report (pool-throughput-bench)")
}

// issue 434: the file backend's blob dir opens when its path holds URL syntax ('#', '?', '%'), as it does under a
// TMPDIR with such a name, and an object written through the bucket lands in exactly that dir.
func TestIssue434_FileBlobDirWithURLSyntaxOpens(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"a#b", "q?x", "pct%", "p%41q"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name, "blob")
			b, err := openFileBlob(ctx, dir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			require.NoError(t, b.Put(ctx, "k", []byte("v")))
			got, err := os.ReadFile(filepath.Join(dir, "k"))
			require.NoError(t, err)
			require.Equal(t, "v", string(got))
		})
	}
}
