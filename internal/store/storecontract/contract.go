// Package storecontract is the shared conformance suite for the store port
// (ADR-0006). RunContract asserts the store SEMANTICS that must hold identically
// for every Engine — memory and badger run it, proving the in-memory fake
// behaves like the real engine (the driver-conformance-parity scenario).
package storecontract

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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
