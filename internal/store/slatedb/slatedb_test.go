//go:build slatedb

package slatedb_test

import (
	"context"
	"strconv"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/slatedb"
	"github.com/green-0-rabbit/funcd/internal/store/storecontract"
)

// scenario: driver-conformance-parity (slatedb side) — the real engine passes the
// identical store contract as the in-memory fake.
func TestScenario_DriverConformanceParity(t *testing.T) {
	storecontract.RunContract(t, func(t *testing.T) store.Store {
		eng, err := slatedb.Open("memory:///")
		if err != nil {
			t.Fatalf("open slatedb (memory): %v", err)
		}
		t.Cleanup(func() { _ = eng.Close() })
		return store.New(eng)
	})
}

// scenario: crash-recovery — objects + the monotonic revision written and flushed
// to the file backend survive a close/reopen.
func TestScenario_CrashRecovery(t *testing.T) {
	dir := t.TempDir()
	url := "file://" + dir
	ctx := context.Background()

	// session 1: write, then close.
	eng1, err := slatedb.Open(url)
	if err != nil {
		t.Fatalf("open(1): %v", err)
	}
	s1 := store.New(eng1)
	created, err := s1.Create(ctx, mkConfig(t, "default", "persisted", "rg1", map[string]string{"k": "v"}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rv, _ := strconv.ParseUint(created.GetObjectMeta().ResourceVersion, 10, 64)
	if err := eng1.Close(); err != nil {
		t.Fatalf("close(1): %v", err)
	}

	// session 2: reopen on the same path — object + revision recovered.
	eng2, err := slatedb.Open(url)
	if err != nil {
		t.Fatalf("open(2): %v", err)
	}
	defer func() { _ = eng2.Close() }()
	s2 := store.New(eng2)

	got, err := s2.Get(ctx, v1.KindConfig.GVK(), "default", "persisted")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	gc, _ := got.(*v1.Config)
	if gc == nil || gc.Spec.Data["k"] != "v" {
		t.Fatalf("recovered data mismatch: %+v", got)
	}

	// the monotonic revision continues from the recovered value (no reset to 0).
	next, err := s2.Create(ctx, mkConfig(t, "default", "after", "rg1", nil))
	if err != nil {
		t.Fatalf("create after reopen: %v", err)
	}
	nextRV, _ := strconv.ParseUint(next.GetObjectMeta().ResourceVersion, 10, 64)
	if nextRV <= rv {
		t.Fatalf("revision not recovered: next=%d <= recovered=%d", nextRV, rv)
	}
}

func mkConfig(t *testing.T, ns, name, rg string, data map[string]string) *v1.Config {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindConfig)
	if !ok {
		t.Fatal("NewObject(Config) returned false")
	}
	c, _ := obj.(*v1.Config)
	c.Name = v1.ObjectName(name)
	c.Namespace = v1.NamespaceName(ns)
	c.ResourceGroup = v1.ResourceGroupName(rg)
	c.Spec.Data = data
	return c
}
