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
	"os"
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

// scenario: worker-lateral-deny — two containerd workeres on their own netns
// cannot reach each other directly (default-deny lateral); function→function
// traffic is only via the gateway (deferred: Linux integration lane).
func TestScenarioWorkerLateralDeny(t *testing.T) {
	requireIntegration(t)
	t.Skip("lateral-deny assertion is wired on the Linux VM lane (P-S/F20): " +
		"start two workeres, attempt a direct TCP dial between their netns IPs, expect refusal")
}
