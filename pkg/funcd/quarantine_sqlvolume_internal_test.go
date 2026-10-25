package funcd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/volume"
)

// mapQuarantine is a SQL fake's quarantine over its dirs map (ADR-0288): a move takes the directory out of the map
// and records the entry, which List returns.
type mapQuarantine struct {
	mu    *sync.Mutex
	dirs  map[provider.ProviderRef]bool
	moved *[]quarantine.Entry
}

func (q mapQuarantine) Move(_ context.Context, ref quarantine.Ref, now time.Time) (quarantine.Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	pref := provider.ProviderRef{Namespace: ref.Namespace, Name: ref.Name}
	if !q.dirs[pref] {
		return quarantine.Entry{}, fault.NotFoundf("test", "%s has no directory", ref)
	}
	delete(q.dirs, pref)
	e := quarantine.Entry{Ref: ref, Since: now.UTC().Truncate(time.Millisecond), State: quarantine.Kept}
	*q.moved = append(*q.moved, e)
	return e, nil
}

func (q mapQuarantine) List(context.Context) ([]quarantine.Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(*q.moved), nil
}

func (mapQuarantine) Expire(context.Context, time.Time) ([]quarantine.Entry, error) { return nil, nil }

// noQuarantine is the quarantine of a SQL fake that lists no directory: nothing to move.
func noQuarantine() quarantine.Store {
	return provider.NewDirQuarantine("", v1.KindSQLDatabase, nil, nil)
}

func (f *fileEngines) Quarantine() quarantine.Store { return mapQuarantine{&f.mu, f.dirs, &f.moved} }

// quarantineRig is the backup rig with SQL engines and Function volumes, started over an orphan of each: the
// directory of SQLDatabase orders/todo-db, which no object names, and the deletion: delete volume orders/cart/state
// with a file, whose Function is not stored.
func quarantineRig(t *testing.T) (*wbRig, *fileEngines, string) {
	t.Helper()
	engines := newFileEngines(t)
	volRoot := t.TempDir()
	r := newWBRig(t, wbOptions{
		opts: []Option{WithSQLProviderRuntime(engines), WithFunctionVolumes(volume.Options{Root: volRoot}, false)},
		setup: func(r *wbRig) {
			engines.mu.Lock()
			engines.dirs[provider.ProviderRef{Namespace: "orders", Name: "todo-db"}] = true
			engines.mu.Unlock()
			dir, err := r.p.volumes.Reserve(r.ctx, cartState(), "uid-gone", 1<<20, v1.DeletionDelete)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.db"), []byte("rows"), 0o600))
		},
	})
	return r, engines, volRoot
}

func cartState() volume.ID { return volume.ID{Namespace: "orders", Function: "cart", Name: "state"} }

func cartStateRef() quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "orders", Name: "cart"}, Volume: "state"}
}

func cartFunction() *v1.Function {
	return &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta("cart"), Spec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs",
		Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20, Deletion: v1.DeletionDelete}},
	}}
}

// waitEmptyVolume waits for the stored Function's pass to make its volume, and checks it is empty.
func (r *wbRig) waitEmptyVolume(id volume.ID) {
	r.t.Helper()
	require.Eventually(r.t, func() bool {
		empty, err := r.p.volumes.Empty(r.ctx, id)
		return err == nil && empty
	}, 10*time.Second, 10*time.Millisecond, "volume %s is made, empty", id)
}

// scenario: sql-and-volume-quarantined — a SQL database directory with no SQLDatabase and a deletion: delete local
// volume of a Function that is not stored: after an unheld start both are in the quarantine, listed with their since,
// each named by one Warn line, and a Function applied again under that name gets a new, empty volume.
//
// oracle: docs/design/storage/quarantine.fizz#NothingLost
func TestScenarioSQLAndVolumeQuarantined(t *testing.T) {
	t.Parallel()
	r, engines, volRoot := quarantineRig(t)

	sqlEntries, err := r.p.sqlReconciler.Quarantine().List(r.ctx)
	require.NoError(t, err)
	require.Len(t, sqlEntries, 1)
	require.Equal(t, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindSQLDatabase, Namespace: "orders", Name: "todo-db"}},
		sqlEntries[0].Ref)
	engines.mu.Lock()
	require.False(t, engines.dirs[provider.ProviderRef{Namespace: "orders", Name: "todo-db"}], "the directory left its place")
	engines.mu.Unlock()

	volEntries, err := r.p.fnReconciler.Quarantine().List(r.ctx)
	require.NoError(t, err)
	require.Len(t, volEntries, 1)
	require.Equal(t, cartStateRef(), volEntries[0].Ref)
	require.Equal(t, quarantine.Kept, volEntries[0].State)
	b, err := os.ReadFile(filepath.Join(volRoot, ".quarantine", "volume", "orders", "cart", "state",
		quarantine.Stamp(volEntries[0].Since), "data", "orders.db"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(b))
	logs := r.logs.String()
	require.Contains(t, logs, `msg="moved the data of a SQLDatabase with no object into the quarantine" component=sql.reconciler namespace=orders name=todo-db until=`)
	require.Contains(t, logs, `msg="moved the data of a Function with no object into the quarantine"`)

	r.create(r.dev, cartFunction())
	r.waitEmptyVolume(cartState())
	entries, err := r.p.fnReconciler.Quarantine().List(r.ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1, "applying the Function returns nothing on its own")
}

// scenario: quarantine-returned, the volume case — a Function's quarantined volume returns into the volume of the
// Function applied again, which must exist and be empty; run again, the return fails NotFound.
//
// oracle: docs/design/storage/quarantine.fizz#ReturnsIntoEmpty
func TestScenarioQuarantineReturnedVolume(t *testing.T) {
	t.Parallel()
	r, _, _ := quarantineRig(t)
	ret, ok := r.p.fnReconciler.Quarantine().(quarantine.Returner)
	require.True(t, ok, "the volume quarantine returns")
	require.Equal(t, fault.Conflict, fault.KindOf(ret.Return(r.ctx, cartStateRef(), time.Time{})), "the Function is not stored")

	r.create(r.dev, cartFunction())
	r.waitEmptyVolume(cartState())
	dir, err := r.p.volumes.Reserve(r.ctx, cartState(), r.getFunctionUID("cart"), 1<<20, v1.DeletionDelete)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.db"), []byte("new"), 0o600))
	require.Equal(t, fault.Conflict, fault.KindOf(ret.Return(r.ctx, cartStateRef(), time.Time{})), "the volume holds a file")
	require.NoError(t, os.Remove(filepath.Join(dir, "new.db")))

	require.NoError(t, ret.Return(r.ctx, cartStateRef(), time.Time{}))
	b, err := os.ReadFile(filepath.Join(dir, "orders.db"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(b), "the data is back in the Function's volume")
	entries, err := r.p.fnReconciler.Quarantine().List(r.ctx)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Equal(t, fault.NotFound, fault.KindOf(ret.Return(r.ctx, cartStateRef(), time.Time{})), "run again: no entry")
}

func (r *wbRig) getFunctionUID(name v1.ObjectName) v1.UID {
	r.t.Helper()
	obj, err := r.st.Get(r.ctx, v1.KindFunction.GVK(), "orders", name)
	require.NoError(r.t, err)
	return obj.GetObjectMeta().UID
}
