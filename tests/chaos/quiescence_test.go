package chaos

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scenario: ready-function-is-quiescent (ADR-0047) — a Ready function with no periodic writer must
// leave the control loop quiescent: its resourceVersion stops advancing (no reconcile→write→watch
// storm). Before the store's no-op-write coalescing, a stable Ready function bumped its RV on every
// reconcile and pinned a CPU (the demo defect); this guard fails if that regresses.
func TestQuiescence_ReadyFunctionDoesNotStorm(t *testing.T) {
	h := newHarness(t)
	h.deployReady(t, "quiet")

	// let any legitimate settle finish, then measure RV growth over a window at steady state.
	time.Sleep(500 * time.Millisecond)
	rv0 := h.rv(t, "quiet")
	time.Sleep(2 * time.Second)
	rv1 := h.rv(t, "quiet")

	growth := rv1 - rv0
	t.Logf("steady-state resourceVersion growth over 2s: %d (rv %d → %d)", growth, rv0, rv1)
	require.LessOrEqual(t, growth, uint64(3),
		"Ready function is NOT quiescent — resourceVersion climbed %d in 2s (a reconcile storm; the ADR-0047 coalescing regressed)", growth)
}
