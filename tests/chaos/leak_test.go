package chaos

import (
	"context"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: no-goroutine-leak (ADR-0047) — deploying then deleting N functions must return the
// goroutine count to ~baseline; a per-resource goroutine that is never released would grow RAM
// unboundedly over a platform's lifetime.
func TestLeak_DeployDeleteDoesNotLeakGoroutines(t *testing.T) {
	h := newHarness(t)

	settle := func() {
		for range 5 {
			runtime.GC()
			time.Sleep(150 * time.Millisecond)
		}
	}
	settle()
	base := runtime.NumGoroutine()

	const n = 6
	for i := range n {
		name := "leak" + strconv.Itoa(i)
		h.deployReady(t, name)
		require.NoError(t, h.c.Delete(context.Background(), v1.KindFunction, "default", v1.ObjectName(name)))
	}
	settle()
	after := runtime.NumGoroutine()

	t.Logf("goroutines: baseline %d → after %d deploy/delete cycles %d", base, n, after)
	// Modest slack (pooled conns, watch buffers). A per-function goroutine leak would be ~n× the slack.
	require.LessOrEqual(t, after, base+8,
		"goroutine leak: %d → %d after %d deploy/delete cycles (a per-resource goroutine is not released)", base, after, n)
}
