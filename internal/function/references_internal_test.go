package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func seedBucket(t *testing.T, r *Reconciler, name string, prefixes ...string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindBucket)
	b := obj.(*v1.Bucket)
	b.Name, b.Namespace, b.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	for _, p := range prefixes {
		b.Spec.Prefixes = append(b.Spec.Prefixes, v1.BucketPrefix{Name: p, Owner: "some-owner"})
	}
	_, err := r.store.Create(context.Background(), b)
	require.NoError(t, err)
}

func seedKVStore(t *testing.T, r *Reconciler, name string, tables ...string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindKVStore)
	ks := obj.(*v1.KVStore)
	ks.Name, ks.Namespace, ks.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	for _, tb := range tables {
		ks.Spec.Tables = append(ks.Spec.Tables, v1.KVTable{Name: tb, Owner: "some-owner"})
	}
	_, err := r.store.Create(context.Background(), ks)
	require.NoError(t, err)
}

// scenario: blob-binding-waits-for-bucket (ADR-0121) — resolveDataReferences requeues with BucketNotFound
// while the bound Bucket/prefix is absent, then resolves once it exists (accept-and-requeue, no reject).
func TestScenarioBlobBindingWaitsForBucket(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	fn := sampleFn()
	fn.Spec.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "data", Prefix: "bronze"}}

	requeue, reason, _, err := r.resolveDataReferences(context.Background(), fn)
	require.NoError(t, err)
	require.True(t, requeue, "a missing bucket requeues (holds the function not-Ready)")
	require.Equal(t, "BucketNotFound", reason)

	// bucket present but the wrong prefix ⇒ still requeues.
	seedBucket(t, r, "data", "gold")
	requeue, reason, _, err = r.resolveDataReferences(context.Background(), fn)
	require.NoError(t, err)
	require.True(t, requeue, "a bucket without the bound prefix still requeues")
	require.Equal(t, "BucketNotFound", reason)

	// the bound prefix exists ⇒ resolves.
	seedBucket(t, r, "data2", "bronze")
	fn.Spec.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "data2", Prefix: "bronze"}}
	requeue, _, _, err = r.resolveDataReferences(context.Background(), fn)
	require.NoError(t, err)
	require.False(t, requeue, "the bound bucket/prefix now exists ⇒ resolves")
}

// scenario: kv-binding-waits-for-store (ADR-0121) — same accept-and-requeue for spec.kv → KVStoreNotFound.
func TestScenarioKVBindingWaitsForStore(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	fn := sampleFn()
	fn.Spec.KV = []v1.FunctionKV{{Alias: "kv", Store: "orders", Table: "customers"}}

	requeue, reason, _, err := r.resolveDataReferences(context.Background(), fn)
	require.NoError(t, err)
	require.True(t, requeue, "a missing store requeues")
	require.Equal(t, "KVStoreNotFound", reason)

	seedKVStore(t, r, "orders", "customers")
	requeue, _, _, err = r.resolveDataReferences(context.Background(), fn)
	require.NoError(t, err)
	require.False(t, requeue, "the bound store/table now exists ⇒ resolves")
}

// scenario: no-data-bindings-no-requeue — a function without spec.blob/spec.kv never requeues at this gate.
func TestScenarioNoDataBindingsNoRequeue(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	requeue, _, _, err := r.resolveDataReferences(context.Background(), sampleFn())
	require.NoError(t, err)
	require.False(t, requeue)
}
