package funcd

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/volume"
)

// TestIssue1204_LeftoverEmptyDirsGoOnceTheHoldLifts: a held boot keeps the last run's emptyDirs, which hold capacity;
// once the hold is released, the next Function pass releases them, as an unheld boot does (#1204).
func TestIssue1204_LeftoverEmptyDirsGoOnceTheHoldLifts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	root := t.TempDir()
	last, err := volume.New(volume.Options{Root: root})
	require.NoError(t, err)
	leftover := volume.ID{Namespace: "team", Function: "thumbs", Name: "cache", Worker: "a1b2c3d4e5f6"}
	_, err = last.Reserve(ctx, leftover, "", 1<<20, "")
	require.NoError(t, err)

	p, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithHold(h),
		WithFunctionVolumes(volume.Options{Root: root, CapacityBytes: 1 << 20}, false),
		WithPacing(Pacing{SupervisionPeriod: 50 * time.Millisecond}))
	require.NoError(t, err)
	seedObjects(t, p.cfg.store, &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: teamMeta("thumbs"),
		Spec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs"}})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	listed := func() bool {
		infos, err := p.volumes.List(ctx)
		require.NoError(t, err)
		return slices.ContainsFunc(infos, func(in volume.Info) bool { return in.ID == leftover })
	}

	time.Sleep(200 * time.Millisecond) // the held platform's passes run every 50 ms
	require.True(t, listed(), "a held boot keeps the last run's emptyDirs")
	require.NoError(t, h.Release(time.Now()))
	require.Eventually(t, func() bool { // a write to the Function starts its next pass
		obj, err := p.cfg.store.Get(ctx, v1.KindFunction.GVK(), "team", "thumbs")
		require.NoError(t, err)
		fn := obj.(*v1.Function)
		fn.Tags = v1.Tags{"pass": "after-release"}
		_, err = p.cfg.store.Update(ctx, fn)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return !listed() }, 5*time.Second, 20*time.Millisecond,
		"the last run's emptyDirs are released once the hold lifts")
}
