package memory_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore/kvstorecontract"
	"github.com/pyvvo/funcd/internal/kvstore/memory"
)

// scenario: kv-roundtrips — put/get/delete/list round-trip on the in-memory driver.
func TestScenarioKVRoundtrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := memory.New()

	require.NoError(t, kv.Put(ctx, "ns/b/k", []byte("hello")))
	v, found, err := kv.Get(ctx, "ns/b/k")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("hello"), v)

	keys, err := kv.List(ctx, "ns/b/")
	require.NoError(t, err)
	require.Equal(t, []string{"ns/b/k"}, keys)

	require.NoError(t, kv.Delete(ctx, "ns/b/k"))
	_, found, err = kv.Get(ctx, "ns/b/k")
	require.NoError(t, err)
	require.False(t, found)
}

// scenario: kvstore-contract-holds — the in-memory driver satisfies the port contract.
func TestScenarioKVStoreContractHolds(t *testing.T) {
	t.Parallel()
	kvstorecontract.Run(t, memory.New())
}
