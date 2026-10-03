//go:build linux && integration

// These are the deferred Linux integration scenarios for the containerd/crun
// driver (ADR-0011 Test sequencing). They require a real Linux host with root,
// a running containerd, crun, and the CNI reference plugins — so they are gated
// behind the `integration` build tag AND FUNCD_IT=1, and never run in `just ci`
// (which is pure-Go on any OS). Run them on the Linux VM lane (P-S/F20):
//
//	FUNCD_IT=1 go test -tags integration ./internal/runtime/containerd/...
package containerd_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/containerd"
	"github.com/pyvvo/funcd/internal/runtime/runtimecontract"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("FUNCD_IT") != "1" {
		t.Skip("set FUNCD_IT=1 (Linux, root, containerd, crun, CNI) to run containerd integration tests")
	}
}

func integrationConfig() containerd.Config {
	return containerd.Config{
		Socket:      "/run/containerd/containerd.sock",
		Snapshotter: "overlayfs",
		CNIBinDir:   "/opt/cni/bin",
		CNIConfDir:  "/var/lib/funcd/cni/conf",
		SubnetCIDR:  "10.63.0.0/16",
	}
}

// scenario: containerd-worker-lifecycle — the containerd/crun driver satisfies
// the same runtimecontract assertions as the process driver, against real crun
// containers (deferred: Linux integration lane).
func TestScenarioContainerdWorkerLifecycle(t *testing.T) {
	requireIntegration(t)
	runtimecontract.RunContract(t, func(t *testing.T) runtime.Runtime {
		t.Helper()
		rt, err := containerd.New(integrationConfig())
		require.NoError(t, err)
		return rt
	})
}

// Issue 40: containerd keeps a worker's container and snapshot when funcd stops or dies, so the driver of the next
// funcd run must re-create the worker under the same name.
func TestIssue40_RestartReclaimsLeftoverWorker(t *testing.T) {
	requireIntegration(t)
	ctx := context.Background()
	spec := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "issue40",
		Revision:  "issue40-1",
		Image:     "docker.io/library/busybox:1.36",
		Command:   []string{"sleep", "60"},
		LogPath:   filepath.Join(t.TempDir(), "worker.log"),
	}

	previous, err := containerd.New(integrationConfig())
	require.NoError(t, err)
	t.Cleanup(func() { _ = previous.Close() })
	inst, err := previous.Create(ctx, spec)
	require.NoError(t, err)
	require.NoError(t, previous.Start(ctx, inst.ID))

	next, err := containerd.New(integrationConfig())
	require.NoError(t, err)
	t.Cleanup(func() { _ = next.Close() })
	again, err := next.Create(ctx, spec)
	require.NoError(t, err, "the worker the previous run left must not block its re-create")
	t.Cleanup(func() { _ = next.Stop(ctx, again.ID) })
	require.NoError(t, next.Start(ctx, again.ID))
	got, err := next.Status(ctx, again.ID)
	require.NoError(t, err)
	require.Equal(t, runtime.StateRunning, got.State)
}

// scenario: worker-lateral-deny — two containerd workers on their own netns
// cannot reach each other directly (default-deny lateral); function→function
// traffic is only via the gateway (deferred: Linux integration lane).
func TestScenarioWorkerLateralDeny(t *testing.T) {
	requireIntegration(t)
	t.Skip("lateral-deny assertion is wired on the Linux VM lane (P-S/F20): " +
		"start two workers, attempt a direct TCP dial between their netns IPs, expect refusal")
}
