// Package kvstorecontract is the shared conformance suite for the kvstore.KV port
// (ADR-0019): Run asserts put/get/overwrite/delete/list/missing-key against any driver.
// The in-memory driver runs it; the JetStream / database-layer drivers will inherit it.
package kvstorecontract

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/snapshot"
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

// RunUsage asserts the Usager capability against kv (ADR-0292): the bytes of the keys and values under a prefix, which
// an overwrite and a delete change, and no key outside it.
func RunUsage(t *testing.T, kv kvstore.KV) {
	t.Helper()
	ctx := context.Background()
	u, ok := kv.(kvstore.Usager)
	require.True(t, ok, "the driver measures a prefix's usage")
	require.NoError(t, kv.Put(ctx, "u/k1", []byte("12345")))
	require.NoError(t, kv.Put(ctx, "u/k2", []byte("1")))
	require.NoError(t, kv.Put(ctx, "u/k1", []byte("123")))
	require.NoError(t, kv.Put(ctx, "uv/k", []byte("not under u/")))
	n, err := u.Usage(ctx, "u/")
	require.NoError(t, err)
	require.Equal(t, int64(len("u/k1")+3+len("u/k2")+1), n)
	require.NoError(t, kv.Delete(ctx, "u/k2"))
	n, err = u.Usage(ctx, "u/")
	require.NoError(t, err)
	require.Equal(t, int64(len("u/k1")+3), n)
}

// Exporter is the per-store export a driver offers beyond the port (ADR-0209 Decision 7).
type Exporter interface {
	SnapshotPrefix(prefix string) snapshot.Source
}

// RunSnapshotPrefix asserts ADR-0209 Decision 7 against kv, which must be an Exporter: a bad prefix is fault.Invalid
// before any emit; SnapshotPrefix("a/s1/") emits each live key of a/s1/ without the prefix, with its value, in key
// order, version "", while writers on a/s1/ and a/s10/ run: a writer's keys form an unbroken run, and no key of
// a/s10/ or a/s1x comes out.
func RunSnapshotPrefix(t *testing.T, kv kvstore.KV) {
	t.Helper()
	ctx := context.Background()
	exp, ok := kv.(Exporter)
	require.True(t, ok, "the driver offers no SnapshotPrefix")
	for _, k := range []string{"a/s1/b", "a/s1/a", "a/s1/gone", "a/s10/x", "a/s1x"} {
		require.NoError(t, kv.Put(ctx, k, []byte("v-"+k)))
	}
	require.NoError(t, kv.Delete(ctx, "a/s1/gone"))
	for _, bad := range []string{"", "a/s1", "\x00backup/"} {
		emitted := 0
		_, err := exp.SnapshotPrefix(bad).Snapshot(ctx, func(snapshot.Record) error { emitted++; return nil })
		require.Equal(t, fault.Invalid, fault.KindOf(err), "prefix %q", bad)
		require.Zero(t, emitted, "prefix %q emitted before its refusal", bad)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	var written [2]atomic.Int64
	for i, prefix := range []string{"a/s1/", "a/s10/"} {
		wg.Go(func() {
			for n := 0; !stop.Load(); n++ {
				k := fmt.Sprintf("%sw-%06d", prefix, n)
				if err := kv.Put(ctx, k, []byte("v-"+k)); err != nil {
					t.Errorf("put %s: %v", k, err)
					return
				}
				written[i].Add(1)
			}
		})
	}
	require.Eventually(t, func() bool { return written[0].Load() > 20 && written[1].Load() > 20 }, 10*time.Second, time.Millisecond)
	var got []string
	rv, err := exp.SnapshotPrefix("a/s1/").Snapshot(ctx, func(r snapshot.Record) error {
		got = append(got, string(r.Key))
		require.Equal(t, "v-a/s1/"+string(r.Key), string(r.Value))
		return nil
	})
	stop.Store(true)
	wg.Wait()
	require.NoError(t, err)
	require.Empty(t, rv)
	require.True(t, slices.IsSorted(got), "not in key order: %v", got)
	require.Equal(t, []string{"a", "b"}, got[:2], "the store's own keys, without the prefix; the deleted one absent")
	for i, k := range got[2:] {
		require.Equal(t, fmt.Sprintf("w-%06d", i), k, "the writer's keys form no unbroken run")
	}
}

// Loader is the per-store load a driver offers beyond the port (ADR-0222 Decision 7).
type Loader interface {
	LoadPrefix(ctx context.Context, prefix string, next func() (snapshot.Record, error)) error
}

// RunLoadPrefix asserts ADR-0222's LoadPrefix against kv, which must be an Exporter and a Loader: a bad prefix is
// fault.Invalid; a store holding a key is fault.Conflict and gains nothing; an empty one gets every record under the
// prefix, which SnapshotPrefix then emits as loaded, and the neighbour a/s10/ is untouched.
func RunLoadPrefix(t *testing.T, kv kvstore.KV) {
	t.Helper()
	ctx := context.Background()
	ld, ok := kv.(Loader)
	require.True(t, ok, "the driver offers no LoadPrefix")
	recs := []snapshot.Record{{Key: []byte("t/1"), Value: []byte("one")}, {Key: []byte("t/2"), Value: []byte("two")}}
	from := func() func() (snapshot.Record, error) {
		i := 0
		return func() (snapshot.Record, error) {
			if i == len(recs) {
				return snapshot.Record{}, io.EOF
			}
			i++
			return recs[i-1], nil
		}
	}
	require.Equal(t, fault.Invalid, fault.KindOf(ld.LoadPrefix(ctx, "a/s1", from())))
	require.NoError(t, kv.Put(ctx, "a/s1/old", []byte("x")))
	require.Equal(t, fault.Conflict, fault.KindOf(ld.LoadPrefix(ctx, "a/s1/", from())))
	keys, err := kv.List(ctx, "a/s1/")
	require.NoError(t, err)
	require.Equal(t, []string{"a/s1/old"}, keys, "a refused load writes nothing")

	require.NoError(t, kv.Put(ctx, "a/s10/x", []byte("x")))
	require.NoError(t, ld.LoadPrefix(ctx, "a/s1x/", from()))
	require.NoError(t, ld.LoadPrefix(ctx, "a/s2/", from()))
	var got []snapshot.Record
	_, err = kv.(Exporter).SnapshotPrefix("a/s2/").Snapshot(ctx, func(r snapshot.Record) error {
		got = append(got, r)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, recs, got)
	keys, err = kv.List(ctx, "a/s10/")
	require.NoError(t, err)
	require.Equal(t, []string{"a/s10/x"}, keys)
}
