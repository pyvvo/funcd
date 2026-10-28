package quota_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/quota"
)

const (
	mib = int64(1) << 20
	gib = int64(1) << 30
)

// fakeDisk is a FreeFunc whose free space a test sets.
type fakeDisk struct {
	free atomic.Int64
	err  atomic.Pointer[error]
}

func (d *fakeDisk) read() (int64, error) {
	if e := d.err.Load(); e != nil {
		return 0, *e
	}
	return d.free.Load(), nil
}

func newFloor(t *testing.T, free int64) (*quota.Floor, *fakeDisk, *clock.Manual, *bytes.Buffer) {
	t.Helper()
	d := &fakeDisk{}
	d.free.Store(free)
	clk := clock.NewManual(time.Unix(0, 0))
	var logs bytes.Buffer
	return quota.NewFloor(d.read, 2*gib, clk, slog.New(slog.NewTextHandler(&logs, nil))), d, clk, &logs
}

// scenario: floor-refuses-workload-writes — below the floor a write gets 507 with a message that names no number.
func TestScenarioFloorRefusesWorkloadWrites(t *testing.T) {
	t.Parallel()
	f, _, _, logs := newFloor(t, 1536*mib)
	err := f.Check(context.Background(), 10)
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "the data filesystem is below storage.minFreeSize, or this write would take it there")
	require.False(t, strings.ContainsAny(err.Error(), "0123456789"), "the refusal names no number: %v", err)
	require.Contains(t, logs.String(), `free="1.5 GiB" floor="2.0 GiB"`, "the numbers go to the log")
	require.Error(t, f.Check(context.Background(), quota.Unknown), "a write of unknown size below the floor")
}

// scenario: floor-counts-the-write — a write is refused when its size would take the filesystem below the floor, and
// the bytes admitted since the last reading count against it.
func TestScenarioFloorCountsTheWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, _, _, _ := newFloor(t, 2560*mib)
	require.Error(t, f.Check(ctx, 600*mib), "512 MiB of room cannot hold 600 MiB")
	require.NoError(t, f.Check(ctx, 100*mib))
	require.NoError(t, f.Check(ctx, 400*mib), "412 MiB of room left")
	require.Error(t, f.Check(ctx, 20*mib), "12 MiB of room left")
	require.NoError(t, f.Check(ctx, quota.Unknown), "an unknown size is admitted while the room is not negative")
}

// scenario: floor-lifts — once space is freed, the next reading, at most a second later, admits writes again; the log
// says once when the floor starts refusing and once when it stops.
func TestScenarioFloorLifts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, d, clk, logs := newFloor(t, gib)
	require.Error(t, f.Check(ctx, 1))
	require.Error(t, f.Check(ctx, 1))
	d.free.Store(3 * gib)
	require.Error(t, f.Check(ctx, 1), "the reading is at most a second old")
	clk.Advance(time.Second)
	require.NoError(t, f.Check(ctx, 1))
	require.Equal(t, 1, strings.Count(logs.String(), "below storage.minFreeSize"), logs.String())
	require.Equal(t, 1, strings.Count(logs.String(), "back above storage.minFreeSize"), logs.String())
}

func TestFloorAdmitsWhenItCannotRead(t *testing.T) {
	t.Parallel()
	f, d, clk, logs := newFloor(t, 0)
	e := errors.New("statfs failed")
	d.err.Store(&e)
	require.NoError(t, f.Check(context.Background(), gib))
	clk.Advance(time.Second)
	require.NoError(t, f.Check(context.Background(), gib))
	require.Equal(t, 1, strings.Count(logs.String(), "cannot read the data filesystem's free space"), logs.String())
}

// admitted records the Requests a Checker was asked.
type admitted struct{ got []quota.Request }

func (a *admitted) Admit(_ context.Context, r quota.Request) (quota.Settle, error) {
	a.got = append(a.got, r)
	return func(bool) {}, nil
}

func TestFloorGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, _, _, _ := newFloor(t, 3*gib)
	next := &admitted{}
	g := f.Guard(next)
	_, err := g.Admit(ctx, quota.Request{Bytes: 2 * gib})
	require.Error(t, err, "the write's size is asked")
	_, err = g.Admit(ctx, quota.Request{Bytes: mib})
	require.NoError(t, err)
	_, err = g.Admit(ctx, quota.Request{Deletes: true})
	require.NoError(t, err)
	_, err = g.Admit(ctx, quota.Request{Subject: quota.Subject{Prefix: "gold"}, Bytes: 2 * gib})
	require.NoError(t, err, "a prefix's write meets the floor at its Bucket or the zero Subject, not here")
	require.Len(t, next.got, 3, "a refused write never reaches the next Checker")
	below, _, _, _ := newFloor(t, 0)
	_, err = below.Guard(next).Admit(ctx, quota.Request{Deletes: true})
	require.NoError(t, err, "a delete is admitted below the floor")
}
