// Package storecontract is the shared conformance suite for the store port
// (ADR-0006). RunContract asserts the store SEMANTICS that must hold identically
// for every Engine — memory and badger run it, proving the in-memory fake
// behaves like the real engine (the driver-conformance-parity scenario).
package storecontract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
	"github.com/pyvvo/funcd/internal/store"
)

// RunContract runs the engine-agnostic store scenarios against a Store built by
// newStore. newStore must return a fresh, empty store on each call.
func RunContract(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()
	t.Run("crud-roundtrip", func(t *testing.T) { testCrudRoundtrip(t, newStore(t)) })
	t.Run("not-found", func(t *testing.T) { testNotFound(t, newStore(t)) })
	t.Run("optimistic-concurrency", func(t *testing.T) { testOptimisticConcurrency(t, newStore(t)) })
	t.Run("generation-bumps-on-spec-change", func(t *testing.T) { testGenerationBumps(t, newStore(t)) })
	t.Run("list-by-namespace-and-filter", func(t *testing.T) { testListFilter(t, newStore(t)) })
	t.Run("watch-streams-changes", func(t *testing.T) { testWatchStreams(t, newStore(t)) })
	t.Run("watch-replays-from-resourceversion", func(t *testing.T) { testWatchReplays(t, newStore(t)) })
	t.Run("version-names-the-timeline", func(t *testing.T) { testVersionTimeline(t, newStore(t)) })
	t.Run("snapshot-omits-the-timeline", func(t *testing.T) { testSnapshotOmitsTimeline(t, newStore(t)) })
}

// testVersionTimeline: every version a store mints is "<its 16-hex timeline>-<n>", the list's included, and a
// since-version of another timeline, a plain one included, cannot be replayed (ADR-0202).
func testVersionTimeline(t *testing.T, s store.Store) {
	ctx := context.Background()
	a := mustCreate(t, s, mkConfigMap(t, "default", "a", "rg1", nil))
	b := mustCreate(t, s, mkConfigMap(t, "default", "b", "rg1", nil))
	va := mustParse(t, a.GetObjectMeta().ResourceVersion)
	vb := mustParse(t, b.GetObjectMeta().ResourceVersion)
	if len(va.Timeline) != 16 || vb.Timeline != va.Timeline || vb.N != va.N+1 {
		t.Fatalf("versions %q then %q, want <16 hex>-n then the same timeline at n+1", va, vb)
	}
	l, err := s.List(ctx, v1.KindConfigMap.GVK(), store.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if l.ResourceVersion != vb.String() {
		t.Fatalf("List version %q, want the last write's %q", l.ResourceVersion, vb)
	}
	for _, since := range []string{store.Version{N: va.N}.String(), store.Version{Timeline: "0123456789abcdef", N: va.N}.String()} {
		if _, err := s.Watch(ctx, v1.KindConfigMap.GVK(), store.WatchOptions{SinceResourceVersion: since}); fault.KindOf(err) != fault.Unavailable {
			t.Fatalf("Watch since %q of another timeline: kind=%v want unavailable", since, fault.KindOf(err))
		}
	}
}

// testSnapshotOmitsTimeline: Store.Snapshot returns the version of its read and leaves the timeline record out.
func testSnapshotOmitsTimeline(t *testing.T, s store.Store) {
	ctx := context.Background()
	mustCreate(t, s, mkConfigMap(t, "default", "a", "rg1", nil))
	var keys [][]byte
	rv, err := s.Snapshot(ctx, func(r snapshot.Record) error {
		keys = append(keys, r.Key)
		return nil
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	l, err := s.List(ctx, v1.KindConfigMap.GVK(), store.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if rv != l.ResourceVersion {
		t.Fatalf("Snapshot version %q, want the store's %q", rv, l.ResourceVersion)
	}
	if len(keys) != 2 {
		t.Fatalf("Snapshot emitted %d records, want the revision and the object", len(keys))
	}
	for _, k := range keys {
		if i := bytes.LastIndexByte(k, 0); i >= 0 && store.IsTimelineRecord(string(k[:i]), string(k[i+1:])) {
			t.Fatal("Snapshot emitted the timeline record")
		}
	}
}

// Restore loads src's snapshot into the empty engine dst and opens it: a store restored from src.
func Restore(t *testing.T, src store.Store, dst store.Engine) store.Store {
	t.Helper()
	var recs []snapshot.Record
	if _, err := src.Snapshot(context.Background(), func(r snapshot.Record) error {
		recs = append(recs, r)
		return nil
	}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := dst.Load(context.Background(), snapshotcontract.Feed(recs)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return store.New(dst)
}

// SnapshotSubject is a metastore over the empty engine eng for snapshotcontract.Run: Load goes to the engine,
// before store.New; Snapshot and Set go through the Store, opened on first use. Set writes a ConfigMap.
func SnapshotSubject(t *testing.T, eng store.Engine) snapshotcontract.Subject {
	t.Helper()
	t.Cleanup(func() { _ = eng.Close() })
	return &metaSubject{eng: eng}
}

type metaSubject struct {
	eng  store.Engine
	once sync.Once
	st   store.Store
}

func (m *metaSubject) store() store.Store {
	m.once.Do(func() { m.st = store.New(m.eng) })
	return m.st
}

func (m *metaSubject) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	return m.store().Snapshot(ctx, emit)
}

func (m *metaSubject) Load(ctx context.Context, next func() (snapshot.Record, error)) error {
	return m.eng.Load(ctx, next)
}

func (m *metaSubject) Set(ctx context.Context, name string, n int) error {
	s := m.store()
	c := &v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}}
	c.Name, c.Namespace, c.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	c.Spec.Data = map[string]string{"n": strconv.Itoa(n)}
	cur, err := s.Get(ctx, v1.KindConfigMap.GVK(), "default", c.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		_, err = s.Create(ctx, c)
	case err == nil:
		c.ResourceVersion = cur.GetObjectMeta().ResourceVersion
		_, err = s.Update(ctx, c)
	}
	return err
}

func (m *metaSubject) Value(r snapshot.Record) (string, int, bool) {
	i := bytes.LastIndexByte(r.Key, 0)
	if i < 0 || string(r.Key[:i]) != v1.KindConfigMap.GVK().String() {
		return "", 0, false
	}
	var c v1.ConfigMap
	if err := json.Unmarshal(r.Value, &c); err != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(c.Spec.Data["n"])
	return string(c.Name), n, err == nil
}

func mkConfigMap(t *testing.T, ns, name, rg string, data map[string]string) *v1.ConfigMap {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindConfigMap)
	if !ok {
		t.Fatal("NewObject(ConfigMap) returned false")
	}
	c, ok := obj.(*v1.ConfigMap)
	if !ok {
		t.Fatalf("NewObject(ConfigMap) is %T, want *ConfigMap", obj)
	}
	c.Name = v1.ObjectName(name)
	c.Namespace = v1.NamespaceName(ns)
	c.ResourceGroup = v1.ResourceGroupName(rg)
	c.Spec.Data = data
	return c
}

func testCrudRoundtrip(t *testing.T, s store.Store) {
	ctx := context.Background()
	created, err := s.Create(ctx, mkConfigMap(t, "default", "cfg1", "rg1", map[string]string{"k": "v"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := created.GetObjectMeta()
	if m.ResourceVersion == "" {
		t.Error("Create: empty resourceVersion")
	}
	if m.Generation != 1 {
		t.Errorf("Create: generation=%d want 1", m.Generation)
	}
	if m.UID == "" {
		t.Error("Create: empty uid")
	}
	got, err := s.Get(ctx, v1.KindConfigMap.GVK(), "default", "cfg1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gc, _ := got.(*v1.ConfigMap)
	if gc == nil || gc.Spec.Data["k"] != "v" {
		t.Fatalf("Get: data roundtrip mismatch: %+v", got)
	}
	if got.GetObjectMeta().ResourceVersion != m.ResourceVersion {
		t.Error("Get: resourceVersion mismatch vs created")
	}
}

func testNotFound(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.Get(ctx, v1.KindConfigMap.GVK(), "default", "nope"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Get(absent): kind=%v want not_found", fault.KindOf(err))
	}
	if err := s.Delete(ctx, v1.KindConfigMap.GVK(), "default", "nope", ""); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Delete(absent): kind=%v want not_found", fault.KindOf(err))
	}
	stale := mkConfigMap(t, "default", "nope", "rg1", nil)
	stale.ResourceVersion = "1"
	if _, err := s.Update(ctx, stale); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Update(absent): kind=%v want not_found", fault.KindOf(err))
	}
}

func testOptimisticConcurrency(t *testing.T, s store.Store) {
	ctx := context.Background()
	created, err := s.Create(ctx, mkConfigMap(t, "default", "race", "rg1", map[string]string{"n": "0"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rv := created.GetObjectMeta().ResourceVersion

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u := mkConfigMap(t, "default", "race", "rg1", map[string]string{"n": fmt.Sprintf("%d", i+1)})
			u.ResourceVersion = rv
			_, errs[i] = s.Update(ctx, u)
		}(i)
	}
	wg.Wait()

	var ok, conflict int
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case fault.KindOf(e) == fault.Conflict:
			conflict++
		default:
			t.Fatalf("unexpected update error: %v", e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("racing updates with same rv: ok=%d conflict=%d, want 1/1", ok, conflict)
	}
}

func testGenerationBumps(t *testing.T, s store.Store) {
	ctx := context.Background()
	created, err := s.Create(ctx, mkConfigMap(t, "default", "gen", "rg1", map[string]string{"k": "v1"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g := created.GetObjectMeta().Generation; g != 1 {
		t.Fatalf("create generation=%d want 1", g)
	}

	// spec change -> generation increments
	specChange := mkConfigMap(t, "default", "gen", "rg1", map[string]string{"k": "v2"})
	specChange.ResourceVersion = created.GetObjectMeta().ResourceVersion
	afterSpec, err := s.Update(ctx, specChange)
	if err != nil {
		t.Fatalf("Update(spec): %v", err)
	}
	if g := afterSpec.GetObjectMeta().Generation; g != 2 {
		t.Fatalf("after spec change generation=%d want 2", g)
	}

	// metadata-only change (tags) -> generation unchanged
	metaOnly := mkConfigMap(t, "default", "gen", "rg1", map[string]string{"k": "v2"})
	metaOnly.Tags = v1.Tags{"team": "core"}
	metaOnly.ResourceVersion = afterSpec.GetObjectMeta().ResourceVersion
	afterMeta, err := s.Update(ctx, metaOnly)
	if err != nil {
		t.Fatalf("Update(meta): %v", err)
	}
	if g := afterMeta.GetObjectMeta().Generation; g != 2 {
		t.Fatalf("after meta-only change generation=%d want 2 (unchanged)", g)
	}
}

func testListFilter(t *testing.T, s store.Store) {
	ctx := context.Background()
	cfgA := mkConfigMap(t, "ns1", "a", "rga", nil)
	cfgA.Tags = v1.Tags{"t": "1"}
	mustCreate(t, s, cfgA)
	mustCreate(t, s, mkConfigMap(t, "ns1", "b", "rgb", nil))
	mustCreate(t, s, mkConfigMap(t, "ns2", "c", "rga", nil))

	all, err := s.List(ctx, v1.KindConfigMap.GVK(), store.ListOptions{})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all.Items) != 3 {
		t.Fatalf("List(all): %d items want 3", len(all.Items))
	}
	if all.ResourceVersion == "" {
		t.Error("List(all): empty collection resourceVersion")
	}
	if got := count(t, s, store.ListOptions{Namespace: "ns1"}); got != 2 {
		t.Errorf("List(ns1): %d want 2", got)
	}
	if got := count(t, s, store.ListOptions{Namespace: "ns1", ResourceGroup: "rga"}); got != 1 {
		t.Errorf("List(ns1,rga): %d want 1", got)
	}
	if got := count(t, s, store.ListOptions{Tags: v1.Tags{"t": "1"}}); got != 1 {
		t.Errorf("List(tag t=1): %d want 1", got)
	}
}

func testWatchStreams(t *testing.T, s store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	w, err := s.Watch(ctx, v1.KindConfigMap.GVK(), store.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	created := mustCreate(t, s, mkConfigMap(t, "default", "w", "rg1", map[string]string{"k": "v1"}))
	if ev := recv(t, w); ev.Type != store.Added || ev.Object.GetName() != "w" {
		t.Fatalf("expected Added w, got %s %s", ev.Type, ev.Object.GetName())
	}

	upd := mkConfigMap(t, "default", "w", "rg1", map[string]string{"k": "v2"})
	upd.ResourceVersion = created.GetObjectMeta().ResourceVersion
	updated, err := s.Update(ctx, upd)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if ev := recv(t, w); ev.Type != store.Modified {
		t.Fatalf("expected Modified, got %s", ev.Type)
	}

	if err := s.Delete(ctx, v1.KindConfigMap.GVK(), "default", "w", updated.GetObjectMeta().ResourceVersion); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ev := recv(t, w); ev.Type != store.Deleted {
		t.Fatalf("expected Deleted, got %s", ev.Type)
	}

	// cancelling the context ends the stream promptly.
	cancel()
	select {
	case _, ok := <-w.ResultChan():
		if ok {
			// may deliver buffered events first; drain until closed
			for ok {
				_, ok = <-w.ResultChan()
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not close after ctx cancel")
	}
}

func testWatchReplays(t *testing.T, s store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := mustCreate(t, s, mkConfigMap(t, "default", "a", "rg1", nil))
	rvA := a.GetObjectMeta().ResourceVersion
	mustCreate(t, s, mkConfigMap(t, "default", "b", "rg1", nil)) // rv > rvA

	w, err := s.Watch(ctx, v1.KindConfigMap.GVK(), store.WatchOptions{SinceResourceVersion: rvA})
	if err != nil {
		t.Fatalf("Watch(since): %v", err)
	}
	defer w.Stop()

	// replay: the change after rvA (Added b) is delivered.
	if ev := recv(t, w); ev.Object.GetName() != "b" {
		t.Fatalf("replay: got %s want b", ev.Object.GetName())
	}
	// then live:
	mustCreate(t, s, mkConfigMap(t, "default", "c", "rg1", nil))
	if ev := recv(t, w); ev.Type != store.Added || ev.Object.GetName() != "c" {
		t.Fatalf("live after replay: got %s %s want Added c", ev.Type, ev.Object.GetName())
	}

	// an invalid resourceVersion is rejected.
	if _, err := s.Watch(ctx, v1.KindConfigMap.GVK(), store.WatchOptions{SinceResourceVersion: "not-a-number"}); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("Watch(bad rv): kind=%v want invalid", fault.KindOf(err))
	}
}

// --- helpers ---

func mustParse(t *testing.T, rv string) store.Version {
	t.Helper()
	v, err := store.ParseVersion(rv)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", rv, err)
	}
	return v
}

func mustCreate(t *testing.T, s store.Store, obj v1.Object) v1.Object {
	t.Helper()
	created, err := s.Create(context.Background(), obj)
	if err != nil {
		t.Fatalf("Create(%s): %v", obj.GetName(), err)
	}
	return created
}

func count(t *testing.T, s store.Store, opts store.ListOptions) int {
	t.Helper()
	l, err := s.List(context.Background(), v1.KindConfigMap.GVK(), opts)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(l.Items)
}

func recv(t *testing.T, w store.Watch) store.Event {
	t.Helper()
	select {
	case ev, ok := <-w.ResultChan():
		if !ok {
			t.Fatal("watch channel closed unexpectedly")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
		return store.Event{}
	}
}
