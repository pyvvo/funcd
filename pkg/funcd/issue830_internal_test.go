package funcd

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// Issue 830: the process runtime of InMemory logs through the platform logger, one a WithLogger after InMemory sets
// included, not through slog's default, which funcd never configures.
func TestIssue830_InMemoryRuntimeLogsThroughThePlatformLogger(t *testing.T) {
	var leaked, logs lockedWriter
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&leaked, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	p, err := New(InMemory(), WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	// A stdout reader that never reads makes the run drop lines, which its output warns about once it closes.
	rt := p.cfg.runtime
	rt.(runtime.OutputCapturer).SetOutputCapture(func(_ runtime.WorkerSpec, out *workerpipe.Output) {
		r := out.Reader(workerpipe.Stdout)
		t.Cleanup(func() { _ = r.Close() })
	})
	ctx := context.Background()
	inst, err := rt.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", OwnerKind: v1.KindFunction, Name: "flood",
		Command: []string{"sh", "-c", "yes x | head -n 100000"},
	})
	require.NoError(t, err)
	require.NoError(t, rt.Start(ctx, inst.ID))

	const warn = "worker output dropped"
	require.Eventually(t, func() bool { return strings.Contains(logs.String()+leaked.String(), warn) }, 10*time.Second, 10*time.Millisecond)
	require.Contains(t, logs.String(), warn)
	require.NotContains(t, leaked.String(), warn)
}
