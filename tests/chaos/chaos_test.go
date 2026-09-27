package chaos

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: chaos-worker-recovers-quiescent (ADR-0047) — killing a Ready function's worker process
// must drive a re-provision back to Ready, AND the loop must return to quiescence afterward (the
// recovery itself does not become a reconcile storm).
func TestChaos_WorkerKilledRecoversAndRequiesces(t *testing.T) {
	h := newHarness(t)
	h.deployReady(t, "chaos")

	// find + SIGKILL the function's worker process.
	insts, err := h.rt.List(context.Background(), "default")
	require.NoError(t, err)
	pid := 0
	for _, in := range insts {
		if string(in.Name) == "chaos" && in.PID > 0 {
			pid = in.PID
			break
		}
	}
	require.NotZero(t, pid, "no running worker found for the function")
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))

	// the reconciler re-provisions the worker back to Ready.
	require.Eventually(t, func() bool { return h.phase(t, "chaos") == v1.PhaseReady },
		30*time.Second, 100*time.Millisecond, "worker did not recover to Ready after the kill")

	// and the loop re-quiesces — recovery did not leave a storm behind.
	time.Sleep(500 * time.Millisecond)
	rv0 := h.rv(t, "chaos")
	time.Sleep(2 * time.Second)
	growth := h.rv(t, "chaos") - rv0
	t.Logf("post-recovery resourceVersion growth over 2s: %d", growth)
	require.LessOrEqual(t, growth, uint64(3), "the control loop did not re-quiesce after chaos recovery (storm during/after recovery)")
}
