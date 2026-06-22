package badger_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	bstore "github.com/green-0-rabbit/funcd/internal/store/badger"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/storecontract"
)

// newBadgerStore opens a fresh Badger-backed store.Store at a temp dir (sync off for test speed).
func newBadgerStore(t *testing.T) store.Store {
	t.Helper()
	e, err := bstore.Open(t.TempDir(), bstore.WithSyncWrites(false), bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	s := store.New(e)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkConfig(t *testing.T, ns, name, rg string, data map[string]string) *v1.Config {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindConfig)
	require.True(t, ok)
	c := obj.(*v1.Config)
	c.Name = v1.ObjectName(name)
	c.Namespace = v1.NamespaceName(ns)
	c.ResourceGroup = v1.ResourceGroupName(rg)
	c.Spec.Data = data
	return c
}

// scenario: badger-engine-passes-store-contract — the Badger engine satisfies the shared
// store contract identically to the memory engine (engine parity behind the port).
func TestScenarioBadgerEnginePassesStoreContract(t *testing.T) {
	storecontract.RunContract(t, newBadgerStore)
}

// scenario: durable-metastore-survives-restart — a resource created in file mode is recovered
// (with its resourceVersion) after the engine is closed and reopened against the same dir.
func TestScenarioDurableMetastoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	e1, err := bstore.Open(dir, bstore.WithValueLogGCInterval(0)) // default SyncWrites=true (durable)
	require.NoError(t, err)
	s1 := store.New(e1)
	created, err := s1.Create(ctx, mkConfig(t, "default", "cfg", "rg1", map[string]string{"k": "v"}))
	require.NoError(t, err)
	wantRV := created.GetObjectMeta().ResourceVersion
	require.NotEmpty(t, wantRV)
	require.NoError(t, s1.Close())

	// reopen the same dir — crash-only recovery
	e2, err := bstore.Open(dir, bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	s2 := store.New(e2)
	t.Cleanup(func() { _ = s2.Close() })

	got, err := s2.Get(ctx, v1.KindConfig.GVK(), "default", "cfg")
	require.NoError(t, err, "the resource survived the restart")
	require.Equal(t, wantRV, got.GetObjectMeta().ResourceVersion, "resourceVersion preserved across restart")
	require.Equal(t, map[string]string{"k": "v"}, got.(*v1.Config).Spec.Data)
}

// scenario: optimistic-concurrency-conflict — an Update carrying a stale resourceVersion is
// rejected with fault.Conflict (the wrapper's RV compare-and-set over Badger's snapshot).
func TestScenarioOptimisticConcurrencyConflict(t *testing.T) {
	ctx := context.Background()
	s := newBadgerStore(t)

	created, err := s.Create(ctx, mkConfig(t, "default", "cfg", "rg1", map[string]string{"k": "v1"}))
	require.NoError(t, err) // `created` holds the original resourceVersion

	// a concurrent fresh handle (same RV) updates first → bumps the store's RV
	fresh, err := s.Get(ctx, v1.KindConfig.GVK(), "default", "cfg")
	require.NoError(t, err)
	fresh.(*v1.Config).Spec.Data = map[string]string{"k": "v2"}
	_, err = s.Update(ctx, fresh)
	require.NoError(t, err)

	// the original handle is now stale → must conflict
	created.(*v1.Config).Spec.Data = map[string]string{"k": "v3"}
	_, err = s.Update(ctx, created)
	require.Error(t, err)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "stale-RV update → fault.Conflict")
}

// scenario: pure-go-build-no-cgo — the slatedb/cgo engine is removed from the tree (the
// CGO_ENABLED=0 build check is the review gate's; this asserts the source removal happened).
func TestScenarioPureGoBuildNoCgo(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// walk up to the module root (the dir containing go.mod)
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached fs root without finding go.mod")
		dir = parent
	}
	_, err := os.Stat(filepath.Join(dir, "internal", "store", "slatedb"))
	require.True(t, os.IsNotExist(err), "internal/store/slatedb must be removed (slatedb/cgo engine gone)")
}
