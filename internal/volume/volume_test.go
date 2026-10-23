package volume

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func newManager(t *testing.T, o Options) *manager {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*manager)
}

func local(fn, name v1.ObjectName) ID { return ID{Namespace: "shop", Function: fn, Name: name} }

// TestScenarioCapacityNotOvercommitted: with 10 units of capacity and 9 reserved, a 2-unit booking is refused
// VolumeCapacity; an emptyDir that does not fit fails its Reserve the same way.
// scenario: capacity-not-overcommitted
func TestScenarioCapacityNotOvercommitted(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{CapacityBytes: 10})
	if _, err := m.Reserve(ctx, local("cart", "state"), "u1", 9, v1.DeletionRetain); err != nil {
		t.Fatal(err)
	}
	err := m.Book(ctx, local("thumbs", "state"), 2)
	if fault.KindOf(err) != fault.Conflict || !errors.Is(err, ErrCapacity) {
		t.Fatalf("Book over capacity = %v, want Conflict VolumeCapacity", err)
	}
	emptyDir := ID{Namespace: "shop", Function: "thumbs", Name: "cache", Worker: "w1"}
	if _, err := m.Reserve(ctx, emptyDir, "", 2, ""); !errors.Is(err, ErrCapacity) {
		t.Fatalf("emptyDir Reserve over capacity = %v, want VolumeCapacity", err)
	}
	if err := m.Book(ctx, local("thumbs", "state"), 1); err != nil {
		t.Fatalf("a fitting booking = %v", err)
	}
}

// TestScenarioNamespaceBudget: a 4-unit namespace budget with 3 reserved refuses 2 more, VolumeBudget.
// scenario: namespace-budget
func TestScenarioNamespaceBudget(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{CapacityBytes: 100, Budget: func(context.Context, v1.NamespaceName) (int64, error) { return 4, nil }})
	if err := m.Book(ctx, local("cart", "state"), 3); err != nil {
		t.Fatal(err)
	}
	err := m.Book(ctx, local("orders", "state"), 2)
	if fault.KindOf(err) != fault.Conflict || !errors.Is(err, ErrBudget) {
		t.Fatalf("Book over budget = %v, want Conflict VolumeBudget", err)
	}
	other := ID{Namespace: "lab", Function: "etl", Name: "state"}
	if err := m.Book(ctx, other, 2); err != nil {
		t.Fatalf("another namespace's booking = %v", err)
	}
}

// TestBookIdempotentAndNoResize (Decision 5): the same id and size books once; another size is Invalid.
func TestBookIdempotentAndNoResize(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{CapacityBytes: 10})
	for range 3 {
		if err := m.Book(ctx, local("cart", "state"), 6); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Book(ctx, local("cart", "state"), 7); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a changed size = %v, want Invalid", err)
	}
	if _, err := m.Reserve(ctx, local("cart", "state"), "u1", 6, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve(ctx, local("cart", "state"), "u1", 5, ""); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("Reserve with a changed size = %v, want Invalid", err)
	}
}

// TestStaleBookingDropped: a booking no Reserve backs is dropped after BookingTTL, freeing its space.
func TestStaleBookingDropped(t *testing.T) {
	ctx := t.Context()
	now := time.Unix(1000, 0)
	m := newManager(t, Options{CapacityBytes: 10, BookingTTL: time.Minute, Clock: func() time.Time { return now }})
	if err := m.Book(ctx, local("ghost", "state"), 10); err != nil {
		t.Fatal(err)
	}
	if err := m.Book(ctx, local("cart", "state"), 1); !errors.Is(err, ErrCapacity) {
		t.Fatalf("before the TTL = %v, want VolumeCapacity", err)
	}
	now = now.Add(2 * time.Minute)
	if err := m.Book(ctx, local("cart", "state"), 1); err != nil {
		t.Fatalf("after the TTL = %v", err)
	}
}

// TestScenarioRetainedNotTakenOver: a retained volume refuses Reserve by another UID (StoreNotOwned); Adopt by the
// re-created Function stamps its UID, and Reserve then succeeds with the old data.
// scenario: retained-not-taken-over
func TestScenarioRetainedNotTakenOver(t *testing.T) {
	ctx := t.Context()
	holder := v1.UID("old")
	m := newManager(t, Options{Holder: func(context.Context, v1.NamespaceName, v1.ObjectName) (v1.UID, bool, error) {
		return holder, holder != "", nil
	}})
	id := local("cart", "state")
	dir, err := m.Reserve(ctx, id, "old", 8, v1.DeletionRetain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(ctx, id, "new"); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("Adopt while the old owner is live = %v, want Conflict", err)
	}
	holder = "new" // the old cart was deleted and cart applied again
	if _, err := m.Reserve(ctx, id, "new", 8, v1.DeletionRetain); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("Reserve by name over another UID = %v, want StoreNotOwned", err)
	}
	if err := m.Adopt(ctx, id, "new"); err != nil {
		t.Fatalf("Adopt by ref = %v", err)
	}
	dir, err = m.Reserve(ctx, id, "new", 8, v1.DeletionRetain)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "f")); err != nil || string(b) != "data" {
		t.Fatalf("the old data = %q, %v", b, err)
	}
}

// TestRetainedReleaseAndRebuild: Adopt "" retains; Retained lists it; a new manager on the same root rebuilds its
// booking; Release removes the leaf and frees the space.
func TestRetainedReleaseAndRebuild(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	m := newManager(t, Options{Root: root, CapacityBytes: 10})
	id := local("cart", "state")
	if _, err := m.Reserve(ctx, id, "u1", 6, v1.DeletionRetain); err != nil {
		t.Fatal(err)
	}
	if err := m.Adopt(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Retained(ctx); err != nil || len(got) != 1 || got[0] != id {
		t.Fatalf("Retained = %v, %v", got, err)
	}
	again := newManager(t, Options{Root: root, CapacityBytes: 10})
	if err := again.Book(ctx, local("thumbs", "state"), 5); !errors.Is(err, ErrCapacity) {
		t.Fatalf("after a restart the retained volume's space = %v, want VolumeCapacity", err)
	}
	if err := again.Release(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "shop", "cart", "state")); !os.IsNotExist(err) {
		t.Fatalf("the leaf after Release: %v", err)
	}
	if err := again.Book(ctx, local("thumbs", "state"), 5); err != nil {
		t.Fatalf("after Release = %v", err)
	}
}

// TestLeafRefusesSymlink (Decision 4): a symlink where the leaf should be is refused, and nothing is created through it.
func TestLeafRefusesSymlink(t *testing.T) {
	ctx := t.Context()
	root, target := t.TempDir(), t.TempDir()
	m := newManager(t, Options{Root: root})
	if err := os.MkdirAll(filepath.Join(root, "shop", "cart"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "shop", "cart", "state")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve(ctx, local("cart", "state"), "u1", 1, ""); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("Reserve over a symlink = %v, want Invalid", err)
	}
}

// TestEmptyDirLeaves: each worker gets its own empty leaf, and List names both kinds.
func TestEmptyDirLeaves(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{})
	a := ID{Namespace: "shop", Function: "thumbs", Name: "cache", Worker: "w1"}
	b := a
	b.Worker = "w2"
	da, err := m.Reserve(ctx, a, "", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(da, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := m.Reserve(ctx, b, "", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(db); len(ents) != 0 {
		t.Fatalf("the second worker's emptyDir holds %d entries", len(ents))
	}
	if _, err := m.Reserve(ctx, local("thumbs", "state"), "u1", 1, ""); err != nil {
		t.Fatal(err)
	}
	infos, err := m.List(ctx)
	if err != nil || len(infos) != 3 {
		t.Fatalf("List = %v, %v; want 3 volumes", infos, err)
	}
}

// TestTarUntarRoundTrip (Decision 11): a volume's files and links travel; Untar refuses a non-empty volume and an
// entry that leaves the volume.
func TestTarUntarRoundTrip(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{})
	src, dst := local("cart", "state"), local("cart2", "state")
	sd, err := m.Reserve(ctx, src, "u1", 1<<20, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sd, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "a", "b", "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b/f.txt", filepath.Join(sd, "a", "link")); err != nil {
		t.Fatal(err)
	}
	rc, err := m.Tar(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	dd, err := m.Reserve(ctx, dst, "u2", 1<<20, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Untar(ctx, dst, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dd, "a", "link")); err != nil || string(b) != "hello" {
		t.Fatalf("restored link = %q, %v", b, err)
	}
	if err := m.Untar(ctx, dst, bytes.NewReader(archive)); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("Untar into a non-empty volume = %v, want Conflict", err)
	}
	var evil bytes.Buffer
	tw := tar.NewWriter(&evil)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	third := local("cart3", "state")
	if _, err := m.Reserve(ctx, third, "u3", 1<<20, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Untar(ctx, third, &evil); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("Untar of ../escape = %v, want Invalid", err)
	}
}

// TestScenarioDevMeasures: without an enforcing Limiter a volume past its size is logged once a measurement runs,
// naming the Function, the volume and both sizes, and the write itself succeeded.
// scenario: dev-measures
func TestScenarioDevMeasures(t *testing.T) {
	ctx := t.Context()
	var logs bytes.Buffer
	m := newManager(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	dir, err := m.Reserve(ctx, local("cart", "state"), "u1", 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("the write past the size = %v, want success in dev", err)
	}
	m.measure()
	got := logs.String()
	for _, want := range []string{"volume past its size", "function=cart", "volume=state", "sizeBytes=4", "usedBytes=10"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q lacks %q", got, want)
		}
	}
}

// TestConcurrentBookingsNeverOvercommit (review checklist): twenty concurrent one-unit bookings against ten units of
// capacity leave exactly ten booked.
func TestConcurrentBookingsNeverOvercommit(t *testing.T) {
	ctx := t.Context()
	m := newManager(t, Options{CapacityBytes: 10})
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Book(ctx, ID{Namespace: "shop", Function: v1.ObjectName(fmt.Sprintf("f%d", i)), Name: "state"}, 1) == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 10 {
		t.Fatalf("%d bookings succeeded, want 10", ok)
	}
}

// spaceLimiter enforces like the volume image and reports its free space and each project's use.
type spaceLimiter struct {
	free int64
	used map[uint32]int64
}

func (*spaceLimiter) Limit(string, uint32, int64) error    { return nil }
func (*spaceLimiter) Enforced() bool                       { return true }
func (l *spaceLimiter) Free() (int64, error)               { return l.free, nil }
func (l *spaceLimiter) Used(project uint32) (int64, error) { return l.used[project], nil }

// TestIssue1201_BookingsFitTheFilesystem: the volume image's filesystem keeps part of the image for its metadata, so a
// booking fits only when the free space holds it beside what the booked volumes may still write, not only under
// capacityBytes (#1201).
func TestIssue1201_BookingsFitTheFilesystem(t *testing.T) {
	ctx := t.Context()
	const mib = 1 << 20
	fs := &spaceLimiter{free: 54 * mib, used: map[uint32]int64{}}
	m := newManager(t, Options{CapacityBytes: 64 * mib, Limiter: fs})
	if _, err := m.Reserve(ctx, local("first", "state"), "u1", 32*mib, v1.DeletionRetain); err != nil {
		t.Fatal(err)
	}
	if err := m.Book(ctx, local("second", "state"), 32*mib); fault.KindOf(err) != fault.Conflict || !errors.Is(err, ErrCapacity) {
		t.Fatalf("Book of 32 MiB beside 32 MiB promised, on 54 MiB free = %v, want Conflict VolumeCapacity", err)
	}
	fs.used[1], fs.free = 30*mib, 24*mib // first wrote 30 MiB of its 32
	if err := m.Book(ctx, local("second", "state"), 20*mib); err != nil {
		t.Fatalf("Book of 20 MiB beside 2 MiB promised, on 24 MiB free = %v, want nil", err)
	}
}

// TestIssue1202_SpecialFilesStayOutOfABackup: a FIFO and a unix socket are a running process's state; a volume holding
// them backs up its files and directories alone, and the backup restores (#1202).
func TestIssue1202_SpecialFilesStayOutOfABackup(t *testing.T) {
	ctx := t.Context()
	root, err := os.MkdirTemp("", "vol") // short: a socket's path holds at most 104 bytes on darwin
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	m := newManager(t, Options{Root: root})
	src, dst := local("cart", "state"), local("copy", "state")
	dir, err := m.Reserve(ctx, src, "u1", 1<<20, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(dir, "app.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	rc, err := m.Tar(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("the backup of a volume holding a FIFO and a socket: %v", err)
	}
	restored, err := m.Reserve(ctx, dst, "u2", 1<<20, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Untar(ctx, dst, bytes.NewReader(archive)); err != nil {
		t.Fatalf("the restore of that backup: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(restored, "data")); err != nil || string(b) != "kept" {
		t.Fatalf("the restored file = %q, %v", b, err)
	}
	ents, err := os.ReadDir(restored)
	if err != nil || len(ents) != 1 {
		t.Fatalf("the restored volume holds %v (%v), want the file alone", ents, err)
	}
}
