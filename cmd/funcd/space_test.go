package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// scenario: start-waits-for-space — the start waits, logging the free space, until the stores' newest files fit, and
// runs a step that fails with ENOSPC again.
func TestScenarioStartWaitsForSpace(t *testing.T) {
	t.Parallel()
	var free atomic.Int64
	free.Store(100 << 20)
	var logs bytes.Buffer
	sleeps := 0
	w := spaceWaiter{
		log:  slog.New(slog.NewTextHandler(&logs, nil)),
		free: func() (int64, error) { return free.Load(), nil },
		need: storesOpenBytes,
		sleep: func(_ context.Context, d time.Duration) error {
			require.Equal(t, spaceWait, d)
			if sleeps++; sleeps == 2 {
				free.Store(storesOpenBytes)
			}
			return nil
		},
	}
	steps := 0
	err := w.run(context.Background(), func() error {
		if steps++; steps == 1 {
			return fault.Wrapf(syscall.ENOSPC, fault.Internal, "safemode.Begin", "write safemode.json")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, steps, "a step that fails with ENOSPC runs again")
	require.Equal(t, 3, sleeps, "two sleeps for space, one after the ENOSPC")
	require.Contains(t, logs.String(), "waiting for space on the data filesystem")
	require.Contains(t, logs.String(), `free="100 MiB" need="640 MiB"`)
}

func TestSpaceWaiterStops(t *testing.T) {
	t.Parallel()
	w := spaceWaiter{
		log:   slog.New(slog.DiscardHandler),
		free:  func() (int64, error) { return 0, nil },
		need:  storesOpenBytes,
		sleep: func(context.Context, time.Duration) error { return context.Canceled },
	}
	err := w.run(context.Background(), func() error { t.Fatal("the step ran without space"); return nil })
	require.True(t, stoppedWaiting(context.Background(), err), "a stop signal ends the wait: %v", err)

	other := errors.New("bad config")
	w.free = func() (int64, error) { return storesOpenBytes, nil }
	require.ErrorIs(t, w.run(context.Background(), func() error { return other }), other, "only ENOSPC waits")

	w.free = func() (int64, error) { return 0, errors.New("statfs failed") }
	require.NoError(t, w.run(context.Background(), func() error { return nil }), "a failed read does not wait")
	require.NoError(t, spaceWaiter{}.run(context.Background(), func() error { return nil }), "memory mode: no wait")
}

// scenario: start-frees-leftover-scratch — a Lake scratch image an earlier run left, which no engine holds, is removed
// before the start waits, so the start does not wait for space it holds itself.
func TestScenarioStartFreesLeftoverScratch(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	cfg.Storage.DataDir = t.TempDir()
	cfg.Runtime.Mode = "containerd"
	cfg.Runtime.Containerd.StateDir = filepath.Join(cfg.Storage.DataDir, "run")
	images, mounts := lakeScratchRoots(cfg)
	image := filepath.Join(images, "default", "lake.img")
	require.NoError(t, os.MkdirAll(filepath.Dir(image), 0o700))
	require.NoError(t, os.WriteFile(image, []byte("1GiB of scratch"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(mounts, "default", "lake"), 0o700))

	w := startSpace(context.Background(), cfg, slog.New(slog.DiscardHandler))
	require.NoFileExists(t, image)
	w.free = func() (int64, error) {
		if _, err := os.Stat(image); err == nil {
			return storesOpenBytes - 1, nil
		}
		return storesOpenBytes, nil
	}
	w.sleep = func(context.Context, time.Duration) error {
		t.Fatal("the start waited for its own scratch")
		return nil
	}
	require.NoError(t, w.run(context.Background(), func() error { return nil }))
}
