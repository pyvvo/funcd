package main

import (
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/stopsignal"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/quota"
)

// spaceWait is how often a start that waits for disk space reads the free space again (ADR-0297 Decision 2).
const spaceWait = 10 * time.Second

// storesOpenBytes is what the four Badger stores' newest files take (ADR-0297 Decision 2): each database's memtable
// file is twice its 16 MiB memtable, and its value-log file twice its 64 MiB value log (internal/store/badger,
// internal/kvstore/badger, internal/workflow/runstate/badger, internal/eventing/eventstore).
const storesOpenBytes = 4 * (2*(16<<20) + 2*(64<<20))

// spaceWaiter runs a start step once the data filesystem has need bytes free, and again while the step fails with
// ENOSPC, logging the free space every spaceWait; ctx ends the wait. With no free, it runs the step once.
type spaceWaiter struct {
	log   *slog.Logger
	free  quota.FreeFunc
	need  int64
	sleep func(ctx context.Context, d time.Duration) error
}

func (w spaceWaiter) run(ctx context.Context, step func() error) error {
	if w.free == nil {
		return step()
	}
	for {
		if err := w.waitFree(ctx); err != nil {
			return err
		}
		err := step()
		if !errors.Is(err, syscall.ENOSPC) {
			return err
		}
		w.log.WarnContext(ctx, "funcd: the data filesystem is full: the start waits for space", "error", err)
		if err := w.sleep(ctx, spaceWait); err != nil {
			return err
		}
	}
}

// waitFree returns once the free space reaches need, or at once when it cannot be read.
func (w spaceWaiter) waitFree(ctx context.Context) error {
	for {
		free, err := w.free()
		if err != nil || free >= w.need {
			return nil
		}
		w.log.WarnContext(ctx, "funcd: waiting for space on the data filesystem before opening the stores",
			"free", humanize.IBytes(uint64(max(free, 0))), "need", humanize.IBytes(uint64(w.need)))
		if err := w.sleep(ctx, spaceWait); err != nil {
			return err
		}
	}
}

// startSpace removes the Lake scratch an earlier run left and returns the start's waiter (ADR-0297 Decision 2); in
// memory mode no store is on disk, and the waiter waits for nothing.
func startSpace(ctx context.Context, cfg config.Config, log *slog.Logger) spaceWaiter {
	if cfg.Storage.Mode == "memory" {
		return spaceWaiter{}
	}
	if cfg.Runtime.Mode == "containerd" {
		if err := provider.RemoveLeftoverScratch(lakeScratchRoots(cfg)); err != nil {
			log.WarnContext(ctx, "funcd: could not remove the Lake scratch an earlier run left", "error", err)
		}
	}
	return spaceWaiter{log: log, free: quota.DiskFree(cfg.Storage.DataDir), need: storesOpenBytes, sleep: sleepUntilStopped}
}

// stoppedWaiting reports whether a stop signal ended a start's wait for space, while parent goes on.
func stoppedWaiting(parent context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && parent.Err() == nil
}

// sleepUntilStopped sleeps d, or less when ctx ends or a stop signal arrives: then it returns the context's error.
func sleepUntilStopped(ctx context.Context, d time.Duration) error {
	ctx, stop := signal.NotifyContext(ctx, stopsignal.Signals()...)
	defer stop()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
