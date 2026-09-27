package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// fakeEngine is a minimal in-test store.Engine. It lives in package store (white-box)
// because the test needs to shrink the unexported replay-ring cap; importing the real
// memory driver here would create an import cycle (memory imports store).
type fakeEngine struct{ data map[string]map[string][]byte }

func newFakeEngine() *fakeEngine { return &fakeEngine{data: map[string]map[string][]byte{}} }

func (e *fakeEngine) View(_ context.Context, fn func(Txn) error) error   { return fn(&fakeTxn{e: e}) }
func (e *fakeEngine) Update(_ context.Context, fn func(Txn) error) error { return fn(&fakeTxn{e: e}) }
func (e *fakeEngine) Close() error                                       { return nil }

type fakeTxn struct{ e *fakeEngine }

func (t *fakeTxn) Get(bucket, key string) ([]byte, bool, error) {
	m := t.e.data[bucket]
	if m == nil {
		return nil, false, nil
	}
	v, ok := m[key]
	return v, ok, nil
}

func (t *fakeTxn) Put(bucket, key string, val []byte) error {
	if t.e.data[bucket] == nil {
		t.e.data[bucket] = map[string][]byte{}
	}
	t.e.data[bucket][key] = val
	return nil
}

func (t *fakeTxn) Delete(bucket, key string) error {
	if m := t.e.data[bucket]; m != nil {
		delete(m, key)
	}
	return nil
}

func (t *fakeTxn) Scan(bucket string, fn func(key string, val []byte) error) error {
	for k, v := range t.e.data[bucket] {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// scenario: watch-replays-from-resourceversion (too-old arm) — a Watch since a
// resourceVersion older than the retained replay ring returns fault.Unavailable.
func TestWatchSinceTooOldReturnsUnavailable(t *testing.T) {
	ctx := context.Background()
	st, ok := New(newFakeEngine()).(*store)
	if !ok {
		t.Fatal("New did not return *store")
	}
	st.ringCap = 3 // shrink the ring so a few creates overflow it

	for i := 1; i <= 5; i++ {
		obj, _ := v1.NewObject(v1.KindConfigMap)
		c, _ := obj.(*v1.ConfigMap)
		c.Name = v1.ObjectName(fmt.Sprintf("c%d", i))
		c.Namespace = "default"
		c.ResourceGroup = "rg1"
		if _, err := st.Create(ctx, c); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	// rev is now 5; the cap-3 ring retains rv 3,4,5 — rv 1 was evicted.
	if _, err := st.Watch(ctx, v1.KindConfigMap.GVK(), WatchOptions{SinceResourceVersion: "1"}); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("Watch(since=1, evicted): kind=%v want unavailable", fault.KindOf(err))
	}
	// a since at the current revision needs no replay → no error.
	w, err := st.Watch(ctx, v1.KindConfigMap.GVK(), WatchOptions{SinceResourceVersion: "5"})
	if err != nil {
		t.Fatalf("Watch(since=current): %v", err)
	}
	w.Stop()
}
