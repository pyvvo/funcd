package function

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/volume"
)

// lockedBuffer is a log sink the reconciler's goroutines share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func cartRef() quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "shop", Name: "cart"}, Volume: "state"}
}

// reserveOrphan reserves volume shop/<fn>/state for owner with deletion, a file written in it, as a Function no longer
// stored left it.
func reserveOrphan(t *testing.T, r *Reconciler, fn v1.ObjectName, deletion v1.DeletionPolicy, body string) volume.ID {
	t.Helper()
	id := volume.ID{Namespace: "shop", Function: fn, Name: "state"}
	dir, err := r.volumes.Reserve(t.Context(), id, "uid-gone", 1<<20, deletion)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.db"), []byte(body), 0o600))
	return id
}

// TestReclaimVolumesQuarantinesDeletePolicy (ADR-0288 Decision 1): at an unheld boot, a deletion: delete volume of a
// Function no longer stored moves into the quarantine, with one Warn line naming it and its until; a retain one stays
// in place, unowned, as before.
func TestReclaimVolumesQuarantinesDeletePolicy(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	root := withVolumes(t, r)
	logs := &lockedBuffer{}
	r.logger = slog.New(slog.NewTextHandler(logs, nil))
	r.quarantinePeriod = 168 * time.Hour
	gone := reserveOrphan(t, r, "cart", v1.DeletionDelete, "rows")
	kept := reserveOrphan(t, r, "thumbs", v1.DeletionRetain, "pics")

	require.NoError(t, r.ReclaimVolumes(t.Context()))
	entries, err := r.Quarantine().List(t.Context())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, cartRef(), entries[0].Ref)
	require.Equal(t, quarantine.Kept, entries[0].State)
	b, err := os.ReadFile(filepath.Join(root, ".quarantine", "volume", "shop", "cart", "state", quarantine.Stamp(entries[0].Since), "data", "orders.db"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(b))
	_, err = r.volumes.Empty(t.Context(), gone)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "the leaf moved")
	require.Contains(t, logs.String(), `msg="moved the data of a Function with no object into the quarantine" namespace=shop name=cart volume=state until=`)

	retained, err := r.volumes.Retained(t.Context())
	require.NoError(t, err)
	require.Equal(t, []volume.ID{kept}, retained, "a retain volume stays in place, unowned")
}

// TestReturnVolume (ADR-0288 Decision 6): Return moves the newest kept entry back into the stored Function's empty
// volume and deletes the entry; without an entry it is NotFound; without the Function, or into a volume that holds a
// file, it is Conflict and moves nothing; a returning entry resumes, the partial copy in the volume removed first.
func TestReturnVolume(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	ctx := t.Context()
	q := r.Quarantine().(quarantine.Returner)
	require.Equal(t, fault.NotFound, fault.KindOf(q.Return(ctx, cartRef(), time.Time{})), "no entry")

	id := reserveOrphan(t, r, "cart", v1.DeletionDelete, "rows")
	_, err := r.Quarantine().Move(ctx, cartRef(), time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, fault.Conflict, fault.KindOf(q.Return(ctx, cartRef(), time.Time{})), "the Function is not stored")

	created, err := r.store.Create(ctx, volumeFn("cart", stateVolume()))
	require.NoError(t, err)
	dir, err := r.volumes.Reserve(ctx, id, created.GetObjectMeta().UID, 1<<20, v1.DeletionRetain)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.db"), []byte("new"), 0o600))
	require.Equal(t, fault.Conflict, fault.KindOf(q.Return(ctx, cartRef(), time.Time{})), "the volume holds a file")
	entries, err := r.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, quarantine.Kept, entries[0].State, "a refused return moves nothing")

	require.NoError(t, os.Remove(filepath.Join(dir, "new.db")))
	require.NoError(t, q.Return(ctx, cartRef(), time.Time{}))
	b, err := os.ReadFile(filepath.Join(dir, "orders.db"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(b))
	entries, err = r.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Empty(t, entries, "a returned entry is gone")
	require.Equal(t, fault.NotFound, fault.KindOf(q.Return(ctx, cartRef(), time.Time{})), "run again: no entry")
}

// TestReturnVolumeResumes (ADR-0288 Decisions 2 and 6): a return cut after it wrote the entry returning and copied part
// of it resumes on the next run: the volume, which holds the partial copy, is emptied, then filled from the entry.
func TestReturnVolumeResumes(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	ctx := t.Context()
	id := reserveOrphan(t, r, "cart", v1.DeletionDelete, "rows")
	e, err := r.Quarantine().Move(ctx, cartRef(), time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, r.volumes.Quarantine().Mark(ctx, id, e.Since, quarantine.Returning))
	created, err := r.store.Create(ctx, volumeFn("cart", stateVolume()))
	require.NoError(t, err)
	dir, err := r.volumes.Reserve(ctx, id, created.GetObjectMeta().UID, 1<<20, v1.DeletionRetain)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.db"), []byte("ro"), 0o600))

	require.NoError(t, r.Quarantine().(quarantine.Returner).Return(ctx, cartRef(), time.Time{}))
	b, err := os.ReadFile(filepath.Join(dir, "orders.db"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(b), "the partial copy is replaced by the whole one")
}

// TestVolumeQuarantineExpire (ADR-0288 Decision 5): Expire deletes a kept entry older than the cutoff and reports it,
// keeps a newer one and a returning one, and removes an entry with neither data nor state without reporting it.
func TestVolumeQuarantineExpire(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	root := withVolumes(t, r)
	ctx := t.Context()
	t0 := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	for i, fn := range []v1.ObjectName{"old", "fresh", "busy"} {
		reserveOrphan(t, r, fn, v1.DeletionDelete, "rows")
		ref := quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "shop", Name: fn}, Volume: "state"}
		at := t0
		if i == 1 {
			at = t0.Add(48 * time.Hour)
		}
		e, err := r.Quarantine().Move(ctx, ref, at)
		require.NoError(t, err)
		if fn == "busy" {
			require.NoError(t, r.volumes.Quarantine().Mark(ctx, volume.ID{Namespace: "shop", Function: fn, Name: "state"}, e.Since, quarantine.Returning))
		}
	}
	husk := filepath.Join(root, ".quarantine", "volume", "shop", "husk", "state", quarantine.Stamp(t0))
	require.NoError(t, os.MkdirAll(husk, 0o700))

	gone, err := r.Quarantine().Expire(ctx, t0.Add(24*time.Hour))
	require.NoError(t, err)
	require.Len(t, gone, 1)
	require.Equal(t, v1.ObjectName("old"), gone[0].Name)
	require.NoDirExists(t, husk)
	left, err := r.Quarantine().List(ctx)
	require.NoError(t, err)
	var names []v1.ObjectName
	for _, e := range left {
		names = append(names, e.Name)
	}
	require.ElementsMatch(t, []v1.ObjectName{"fresh", "busy"}, names)
}
