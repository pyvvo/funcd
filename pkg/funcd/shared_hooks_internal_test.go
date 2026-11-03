package funcd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: store-rules-on-the-write-path
// The KVStore rules run on the write path (ADR-0072/0073): a namespace at its KVStore cap refuses one more, and a store
// that holds keys refuses its delete.
func TestKVStoreRulesRunOnTheWritePath(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithKVStoreQuota(1))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()
	kvStore := func(name v1.ObjectName) *v1.KVStore {
		return &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: v1.ObjectMeta{Namespace: "default", ResourceGroup: "rg1", Name: name}}
	}
	_, err = c.Apply(ctx, kvStore("orders"))
	require.NoError(t, err)
	_, err = c.Apply(ctx, kvStore("carts"))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "the namespace holds its one KVStore: %v", err)
	require.NoError(t, p.cfg.kvStore.Put(ctx, "default/orders/items/1", []byte("x")))
	err = c.Delete(ctx, v1.KindKVStore, "default", "orders")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a store that holds keys stays: %v", err)
}

// scenario: dependency-check-in-list-order
// The dependency check reads the providers' bindings in list order, kv, blob, sql and then the links, and reports the
// first that fails: each binding of orders-api fails until its store exists, and a link to a missing Function fails.
func TestDependencyCheck_ReadsTheProvidersInListOrder(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	p := healthPlatform(t, kvmemory.New(), mem)
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Namespace: "default", ResourceGroup: "rg1", Name: name}
	}
	function := func(name v1.ObjectName, b v1.FunctionBindings) *v1.Function {
		return &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: meta(name),
			Spec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://" + string(name) + ":1", Bindings: b}}
	}
	create(t, p.cfg.store, function("orders-api", v1.FunctionBindings{
		KV:    []v1.FunctionKV{{Alias: "store", Store: "orders", Table: "items"}},
		Blob:  []v1.FunctionBlob{{Alias: "files", Bucket: "files", Prefix: "in"}},
		SQL:   []v1.FunctionSQL{{Alias: "db", Database: "ledger"}},
		Links: []v1.FunctionLink{{Alias: "next", Target: "billing"}},
	}))
	create(t, p.cfg.store, function("caller", v1.FunctionBindings{Links: []v1.FunctionLink{{Alias: "next", Target: "billing"}}}))
	failsOn := func(fn v1.ObjectName, kind, binding string) {
		t.Helper()
		var rep *local.DependencyReport
		require.Eventually(t, func() bool {
			var code int
			code, rep = dependencies(t, p, fn)
			return code == http.StatusServiceUnavailable && rep != nil && rep.Kind == kind
		}, 5*time.Second, 5*time.Millisecond, "%s's check fails on its %s binding, last report %+v", fn, kind, rep)
		require.Equal(t, binding, rep.Binding)
	}
	failsOn("orders-api", "kv", "store")
	create(t, p.cfg.store, &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: meta("orders"),
		Spec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "items", Owner: "orders-api"}}}})
	failsOn("orders-api", "blob", "files")
	create(t, p.cfg.store, &v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: meta("files"),
		Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "in", Owner: v1.FunctionOwner("orders-api")}}}})
	failsOn("orders-api", "sql", "db")
	failsOn("caller", "link", "next")
}
