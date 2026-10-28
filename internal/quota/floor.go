package quota

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// floorReadEvery bounds how often a Floor reads its filesystem's free space (ADR-0296 Decision 2).
const floorReadEvery = time.Second

// FreeFunc returns the bytes a filesystem can still allocate.
type FreeFunc func() (int64, error)

// DiskFree is the FreeFunc of the filesystem holding path: statfs's available blocks times their size.
func DiskFree(path string) FreeFunc {
	return func() (int64, error) {
		var st unix.Statfs_t
		if err := unix.Statfs(path, &st); err != nil {
			return 0, err
		}
		return int64(st.Bavail) * int64(st.Bsize), nil // Bsize is int64 on Linux, uint32 on darwin
	}
}

// Floor is the free-space floor of the data filesystem (ADR-0296): it refuses a write that the last reading, less the
// bytes admitted since, cannot hold above minFree. A reading that fails admits writes and is logged.
type Floor struct {
	free    FreeFunc
	minFree int64
	clk     clock.Clock
	logger  *slog.Logger

	mu       sync.Mutex
	readAt   time.Time
	avail    int64
	admitted int64 // bytes admitted since the last reading
	below    bool  // the last reading was below the floor
	failed   bool  // the last reading failed
}

// NewFloor returns the floor of minFreeBytes over free; nil logger is slog.Default.
func NewFloor(free FreeFunc, minFreeBytes int64, clk clock.Clock, logger *slog.Logger) *Floor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Floor{free: free, minFree: minFreeBytes, clk: clk, logger: logger}
}

// Check admits a write of bytes, or Unknown, while the filesystem holds it above the floor; else it returns
// fault.InsufficientStorage. An admitted size counts against the reading until the next one.
func (f *Floor) Check(_ context.Context, bytes int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if now := f.clk.Now(); f.readAt.IsZero() || now.Sub(f.readAt) >= floorReadEvery {
		f.readAt = now
		f.read()
	}
	room := f.avail - f.admitted - f.minFree
	switch {
	case f.failed:
		return nil
	case bytes == Unknown && room >= 0:
		return nil
	case bytes != Unknown && room >= bytes:
		f.admitted += bytes
		return nil
	}
	return fault.InsufficientStoragef("quota.Floor",
		"the data filesystem is below storage.minFreeSize, or this write would take it there: writes are refused until space is freed")
}

// read takes a new reading and logs each change across the floor; the caller holds f.mu.
func (f *Floor) read() {
	avail, err := f.free()
	if err != nil {
		if !f.failed {
			f.logger.Warn("quota: cannot read the data filesystem's free space; the floor admits writes", "error", err)
		}
		f.failed = true
		return
	}
	below := avail < f.minFree
	switch {
	case below && (!f.below || f.failed):
		f.logger.Warn("quota: the data filesystem is below storage.minFreeSize; workload writes are refused",
			"free", humanize.IBytes(uint64(max(avail, 0))), "floor", humanize.IBytes(uint64(f.minFree)))
	case !below && f.below:
		f.logger.Info("quota: the data filesystem is back above storage.minFreeSize; workload writes are admitted",
			"free", humanize.IBytes(uint64(avail)), "floor", humanize.IBytes(uint64(f.minFree)))
	}
	f.avail, f.admitted, f.below, f.failed = avail, 0, below, false
}

// Guard returns the Checker that asks f with a write's size before next, for every Request but a delete, at the
// subject without a prefix: a Bucket prefix's write is asked again at its Bucket or the zero Subject (blob.Quoted), so
// the floor counts each write once.
func (f *Floor) Guard(next Checker) Checker { return guarded{floor: f, next: next} }

type guarded struct {
	floor *Floor
	next  Checker
}

func (g guarded) Admit(ctx context.Context, r Request) (Settle, error) {
	if !r.Deletes && r.Subject.Prefix == "" {
		if err := g.floor.Check(ctx, r.Bytes); err != nil {
			return nil, err
		}
	}
	return g.next.Admit(ctx, r)
}
