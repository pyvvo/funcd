package store_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/store/storecontract"
)

func badgerAt(t *testing.T, dir string) store.Engine {
	t.Helper()
	e, err := bstore.Open(dir, bstore.WithSyncWrites(false), bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func configMap(name, n string) *v1.ConfigMap {
	c := &v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}}
	c.Name, c.Namespace, c.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	c.Spec.Data = map[string]string{"n": n}
	return c
}

func put(t *testing.T, s store.Store, name, n string) v1.Object {
	t.Helper()
	c := configMap(name, n)
	cur, err := s.Get(context.Background(), v1.KindConfigMap.GVK(), "default", c.Name)
	if fault.KindOf(err) == fault.NotFound {
		out, cerr := s.Create(context.Background(), c)
		require.NoError(t, cerr)
		return out
	}
	require.NoError(t, err)
	c.ResourceVersion = cur.GetObjectMeta().ResourceVersion
	out, err := s.Update(context.Background(), c)
	require.NoError(t, err)
	return out
}

func rvOf(t *testing.T, s store.Store, name string) string {
	t.Helper()
	got, err := s.Get(context.Background(), v1.KindConfigMap.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return got.GetObjectMeta().ResourceVersion
}

func parse(t *testing.T, rv string) store.Version {
	t.Helper()
	v, err := store.ParseVersion(rv)
	require.NoError(t, err)
	return v
}

func records(t *testing.T, src snapshot.Source) (string, []snapshot.Record) {
	t.Helper()
	var recs []snapshot.Record
	rv, err := src.Snapshot(context.Background(), func(r snapshot.Record) error {
		recs = append(recs, r)
		return nil
	})
	require.NoError(t, err)
	return rv, recs
}

func load(t *testing.T, e store.Engine, recs []snapshot.Record) store.Store {
	t.Helper()
	require.NoError(t, e.Load(context.Background(), snapshotcontract.Feed(recs)))
	return store.New(e)
}

// atRevision opens a store on eng and writes x until the store's revision is n.
func atRevision(t *testing.T, eng store.Engine, n int) store.Store {
	t.Helper()
	s := store.New(eng)
	for i := 1; i <= n; i++ {
		put(t, s, "x", strconv.Itoa(i))
	}
	return s
}

// nextVersion is the version s mints for its next write, the create of name.
func nextVersion(t *testing.T, s store.Store, name string) store.Version {
	t.Helper()
	return parse(t, put(t, s, name, "0").GetObjectMeta().ResourceVersion)
}

// scenario: snapshot-loads-back — a metastore's snapshot loads into an empty engine of the other kind and back
// (memory → Badger → memory): every object keeps its resourceVersion, a loaded store mints
// <its new timeline>-<revision+1> next, and a non-empty engine refuses the load with fault.Conflict, unchanged.
func TestScenarioSnapshotLoadsBack(t *testing.T) {
	t.Parallel()
	a := atRevision(t, memory.New(), 3)
	put(t, a, "y", "y")
	rvA, recsA := records(t, a)
	va := parse(t, rvA)
	require.Equal(t, uint64(4), va.N)

	b := load(t, badgerAt(t, t.TempDir()), recsA)
	rvB, recsB := records(t, b)
	require.Equal(t, recsA, recsB, "the Badger store holds the memory store's records")
	vb := parse(t, rvB)
	require.Equal(t, va.N, vb.N)
	require.NotEqual(t, va.Timeline, vb.Timeline)
	for _, name := range []string{"x", "y"} {
		require.Equal(t, rvOf(t, a, name), rvOf(t, b, name), "%s keeps its resourceVersion", name)
	}
	require.Equal(t, store.Version{Timeline: vb.Timeline, N: 5}, nextVersion(t, b, "b-next"), "B mints <new timeline>-<revision+1>")

	cEng := memory.New()
	_, recsB = records(t, b)
	c := load(t, cEng, recsB)
	_, recsC := records(t, c)
	require.Equal(t, recsB, recsC, "the memory store holds the Badger store's records")
	vc := nextVersion(t, c, "c-next")
	require.Equal(t, uint64(6), vc.N)
	require.NotContains(t, []string{va.Timeline, vb.Timeline}, vc.Timeline)
	require.Equal(t, rvOf(t, a, "x"), rvOf(t, c, "x"))

	_, before := records(t, cEng)
	require.Equal(t, fault.Conflict, fault.KindOf(cEng.Load(context.Background(), snapshotcontract.Feed(recsA))))
	_, after := records(t, cEng)
	require.Equal(t, before, after, "a refused Load leaves the engine unchanged")
}

// scenario: first-start-takes-timeline — a store opened on an empty engine versions its first object
// <16 hex>-1; another empty engine gets another timeline; a reopened Badger store keeps its own.
func TestScenarioFirstStartTakesTimeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e1, err := bstore.Open(dir, bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	s1 := store.New(e1)
	first := put(t, s1, "a", "1").GetObjectMeta().ResourceVersion
	require.Regexp(t, `^[0-9a-f]{16}-1$`, first)
	require.NoError(t, s1.Close())

	other := put(t, store.New(memory.New()), "a", "1").GetObjectMeta().ResourceVersion
	require.Regexp(t, `^[0-9a-f]{16}-1$`, other)
	require.NotEqual(t, parse(t, first).Timeline, parse(t, other).Timeline)

	s2 := store.New(badgerAt(t, dir))
	require.Equal(t, first, rvOf(t, s2, "a"))
	require.Equal(t, store.Version{Timeline: parse(t, first).Timeline, N: 2}, parse(t, put(t, s2, "b", "1").GetObjectMeta().ResourceVersion),
		"a reopened store keeps its timeline")
}

// scenario: legacy-store-takes-timeline — a Badger store written before ADR-0202 (plain versions, revision 120,
// no timeline) opens with its objects' versions kept; an Update carrying one returns <timeline>-121, and so
// does List.
func TestScenarioLegacyStoreTakesTimeline(t *testing.T) {
	t.Parallel()
	eng := badgerAt(t, t.TempDir())
	require.NoError(t, eng.Update(context.Background(), func(tx store.Txn) error {
		for name, rv := range map[string]string{"x": "119", "y": "120"} {
			c := configMap(name, rv)
			c.UID, c.Generation, c.ResourceVersion = v1.UID("uid-"+name), 1, rv
			raw, err := json.Marshal(c)
			if err != nil {
				return err
			}
			if err := tx.Put(v1.KindConfigMap.GVK().String(), "default/"+name, raw); err != nil {
				return err
			}
		}
		return tx.Put("\x00store-meta", "revision", []byte("120"))
	}))

	s := store.New(eng)
	require.Equal(t, "119", rvOf(t, s, "x"))
	require.Equal(t, "120", rvOf(t, s, "y"))

	x := configMap("x", "changed")
	x.ResourceVersion = "119"
	updated, err := s.Update(context.Background(), x)
	require.NoError(t, err)
	v := parse(t, updated.GetObjectMeta().ResourceVersion)
	require.Len(t, v.Timeline, 16)
	require.Equal(t, uint64(121), v.N)
	l, err := s.List(context.Background(), v1.KindConfigMap.GVK(), store.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, v.String(), l.ResourceVersion)
	require.Equal(t, "120", rvOf(t, s, "y"), "an object not rewritten keeps its plain version")
}

// scenario: restore-takes-new-timeline — A's Store.Snapshot (T1, revision 100) loaded and opened as B (memory)
// and C (Badger), and A's Engine.Snapshot, its timeline record included, loaded and opened as D (Badger) and E
// (memory): each holds A's objects and versions, has a timeline unlike T1 and the others, and mints <own>-101.
func TestScenarioRestoreTakesNewTimeline(t *testing.T) {
	t.Parallel()
	aEng := memory.New()
	a := atRevision(t, aEng, 100)
	rvA, storeRecs := records(t, a)
	t1 := parse(t, rvA)
	require.Equal(t, uint64(100), t1.N)
	_, engineRecs := records(t, aEng)
	require.Len(t, engineRecs, len(storeRecs)+1, "the engine's snapshot carries the timeline record")

	timelines := map[string]bool{t1.Timeline: true}
	for name, s := range map[string]store.Store{
		"B": load(t, memory.New(), storeRecs),
		"C": load(t, badgerAt(t, t.TempDir()), storeRecs),
		"D": load(t, badgerAt(t, t.TempDir()), engineRecs),
		"E": load(t, memory.New(), engineRecs),
	} {
		require.Equal(t, rvOf(t, a, "x"), rvOf(t, s, "x"), "%s holds A's object and version", name)
		next := nextVersion(t, s, "next")
		require.Equal(t, uint64(101), next.N, "%s mints <own>-101", name)
		require.False(t, timelines[next.Timeline], "%s has a timeline of its own", name)
		timelines[next.Timeline] = true
	}
}

// scenario: stale-update-conflicts — B restored from A; A's later writes leave x at T1-130 and B's at T2-130: an
// Update or a Delete of x on B carrying T1-130 gets fault.Conflict, and x is unchanged.
func TestScenarioStaleUpdateConflicts(t *testing.T) {
	t.Parallel()
	a := atRevision(t, memory.New(), 100)
	b := storecontract.Restore(t, a, memory.New())
	for i := 101; i <= 130; i++ {
		put(t, a, "x", "a"+strconv.Itoa(i))
		put(t, b, "x", "b"+strconv.Itoa(i))
	}
	stale, kept := rvOf(t, a, "x"), rvOf(t, b, "x")
	require.Equal(t, parse(t, stale).N, parse(t, kept).N, "both counters reached 130")
	require.NotEqual(t, stale, kept)

	x := configMap("x", "from A")
	x.ResourceVersion = stale
	_, err := b.Update(context.Background(), x)
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Equal(t, fault.Conflict, fault.KindOf(b.Delete(context.Background(), v1.KindConfigMap.GVK(), "default", "x", stale)))
	got, err := b.Get(context.Background(), v1.KindConfigMap.GVK(), "default", "x")
	require.NoError(t, err)
	require.Equal(t, kept, got.GetObjectMeta().ResourceVersion)
	require.Equal(t, "b130", got.(*v1.ConfigMap).Spec.Data["n"])
}

// scenario: stale-watch-relists (store) — a Watch on B, restored from A, resuming from a T1 or a plain version
// gets fault.Unavailable, so the caller re-lists; one from B's own version resumes.
func TestScenarioStaleWatchRelists(t *testing.T) {
	t.Parallel()
	a := atRevision(t, memory.New(), 100)
	t1 := rvOf(t, a, "x")
	b := storecontract.Restore(t, a, memory.New())
	for _, since := range []string{t1, store.Version{N: parse(t, t1).N}.String()} {
		_, err := b.Watch(context.Background(), v1.KindConfigMap.GVK(), store.WatchOptions{SinceResourceVersion: since})
		require.Equal(t, fault.Unavailable, fault.KindOf(err), "since %q", since)
	}
	own := put(t, b, "x", "b").GetObjectMeta().ResourceVersion
	w, err := b.Watch(context.Background(), v1.KindConfigMap.GVK(), store.WatchOptions{SinceResourceVersion: own})
	require.NoError(t, err)
	w.Stop()
}

// scenario: initial-list-spans-timelines — B holds restored (T1) and new (T2) objects: a Watch opened without a
// version delivers every object once as Added, then B's next write.
func TestScenarioInitialListSpansTimelines(t *testing.T) {
	t.Parallel()
	a := store.New(memory.New())
	put(t, a, "r1", "1")
	put(t, a, "r2", "1")
	b := storecontract.Restore(t, a, memory.New())
	put(t, b, "n1", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := b.Watch(ctx, v1.KindConfigMap.GVK(), store.WatchOptions{})
	require.NoError(t, err)
	defer w.Stop()
	listed := map[v1.ObjectName]int{}
	for range 3 {
		ev := next(t, w)
		require.Equal(t, store.Added, ev.Type)
		listed[ev.Object.GetName()]++
	}
	require.Equal(t, map[v1.ObjectName]int{"r1": 1, "r2": 1, "n1": 1}, listed)
	put(t, b, "n2", "1")
	ev := next(t, w)
	require.Equal(t, store.Added, ev.Type)
	require.Equal(t, v1.ObjectName("n2"), ev.Object.GetName(), "no object is delivered twice")
}

func next(t *testing.T, w store.Watch) store.Event {
	t.Helper()
	select {
	case ev, ok := <-w.ResultChan():
		require.True(t, ok, "the watch closed")
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no watch event")
		return store.Event{}
	}
}
