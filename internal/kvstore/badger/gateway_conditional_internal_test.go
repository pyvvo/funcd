package badger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore"
)

func ifAbsent(key, val string) *writeReq {
	return &writeReq{key: key, val: []byte(val), cond: &kvstore.Write{IfAbsent: true}, versioned: true}
}

// scenario: lost-condition-fails-only-its-request
// TestGatewayLostConditionIsItsOwnResult: in one batch, a put-if-absent of a key that exists and a compare-and-set
// against another version lose alone and write nothing while the batch's other writes commit, and a second
// put-if-absent of a key the batch already wrote runs in the next txn and loses to the first, with its version.
func TestGatewayLostConditionIsItsOwnResult(t *testing.T) {
	d, outs := oneBatchOn(t, nil, []*writeReq{ifAbsent("ns/s/held", "first")})
	require.NoError(t, outs[0].err)
	held := outs[0].res
	require.True(t, held.Done)

	cas := &writeReq{key: "ns/s/held", val: []byte("cas"), cond: &kvstore.Write{IfVersion: held.Version + "-other"}, versioned: true}
	_, outs = oneBatchOn(t, d, []*writeReq{
		ifAbsent("ns/s/held", "second"),
		{key: "ns/s/plain", val: []byte("p")},
		ifAbsent("ns/s/new", "a"),
		ifAbsent("ns/s/new", "b"),
		cas,
		{key: "ns/s/plain", val: []byte("p2")},
	})
	for i, o := range outs {
		require.NoError(t, o.err, "request %d", i)
	}
	require.Equal(t, kvstore.Result{Version: held.Version}, outs[0].res, "a held key loses with its version")
	require.True(t, outs[2].res.Done)
	require.Equal(t, kvstore.Result{Version: outs[2].res.Version}, outs[3].res, "the second claim sees the first committed")
	require.Equal(t, kvstore.Result{Version: held.Version}, outs[4].res, "another version loses")
	ctx := context.Background()
	for key, want := range map[string]string{"ns/s/held": "first", "ns/s/new": "a", "ns/s/plain": "p2"} {
		v, _, err := d.Get(ctx, key)
		require.NoError(t, err)
		require.Equal(t, want, string(v), key)
	}
}

// scenario: version-names-one-write
// TestGatewayVersionNamesOneWrite: a plain write of a key batched after a versioned one runs in the next txn, so the
// version the versioned write reports holds its value alone, and a compare-and-set against it loses to the plain write
// instead of overwriting it.
// oracle: docs/design/storage/kv-claims.fizz#VersionNamesItsWrite
func TestGatewayVersionNamesOneWrite(t *testing.T) {
	d, outs := oneBatchOn(t, nil, []*writeReq{
		{key: "ns/s/k", val: []byte("versioned"), versioned: true},
		{key: "ns/s/k", val: []byte("plain")},
	})
	require.NoError(t, outs[0].err)
	require.NoError(t, outs[1].err)
	ctx := context.Background()
	e, _, err := d.GetEntry(ctx, "ns/s/k")
	require.NoError(t, err)
	require.Equal(t, "plain", string(e.Value))
	require.NotEqual(t, outs[0].res.Version, e.Version, "the plain write has a version of its own")
	lost, err := d.PutIf(ctx, "ns/s/k", []byte("cas"), kvstore.Write{IfVersion: outs[0].res.Version})
	require.NoError(t, err)
	require.Equal(t, kvstore.Result{Version: e.Version}, lost)
}
