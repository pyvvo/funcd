// Package kvstorecontract is the shared conformance suite for the kvstore.KV port
// (ADR-0019): Run asserts put/get/overwrite/delete/list/missing-key against any driver.
// The in-memory driver runs it; the JetStream / database-layer drivers will inherit it.
package kvstorecontract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore"
)

// Run asserts the KV port guarantee against kv.
func Run(t *testing.T, kv kvstore.KV) {
	t.Helper()
	ctx := context.Background()

	// missing key
	_, found, err := kv.Get(ctx, "absent")
	require.NoError(t, err)
	require.False(t, found, "missing key is (nil,false,nil)")

	// put + get
	require.NoError(t, kv.Put(ctx, "a/k1", []byte("v1")))
	v, found, err := kv.Get(ctx, "a/k1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("v1"), v)

	// overwrite
	require.NoError(t, kv.Put(ctx, "a/k1", []byte("v2")))
	v, _, err = kv.Get(ctx, "a/k1")
	require.NoError(t, err)
	require.Equal(t, []byte("v2"), v)

	// list by prefix
	require.NoError(t, kv.Put(ctx, "a/k2", []byte("v3")))
	require.NoError(t, kv.Put(ctx, "b/k1", []byte("v4")))
	keys, err := kv.List(ctx, "a/")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a/k1", "a/k2"}, keys, "list returns only the prefix's keys")

	// delete (then absent); deleting a missing key is a no-op
	require.NoError(t, kv.Delete(ctx, "a/k1"))
	_, found, err = kv.Get(ctx, "a/k1")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, kv.Delete(ctx, "a/k1"), "deleting a missing key is a no-op")
}
