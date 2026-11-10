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

// racers is how many writers race one conditional write in RunConditional.
const racers = 32

// race runs racers writes at once and returns their results.
func race(t *testing.T, write func(i int) (kvstore.Result, error)) []kvstore.Result {
	t.Helper()
	out := make([]kvstore.Result, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			r, err := write(i)
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
			out[i] = r
		})
	}
	close(start)
	wg.Wait()
	return out
}

// winner returns the one Done result of rs, failing unless exactly one is Done and every other one reports the
// winner's version.
func winner(t *testing.T, rs []kvstore.Result) (int, kvstore.Result) {
	t.Helper()
	won := slices.IndexFunc(rs, func(r kvstore.Result) bool { return r.Done })
	require.GreaterOrEqual(t, won, 0, "no writer won")
	for i, r := range rs {
		if i == won {
			continue
		}
		require.False(t, r.Done, "writers %d and %d both won", won, i)
		require.Equal(t, rs[won].Version, r.Version, "loser %d reports another version than the winner's", i)
	}
	require.NotEmpty(t, rs[won].Version)
	return won, rs[won]
}

// expireSoon writes key with a 1 s TTL and waits until it expired.
func expireSoon(t *testing.T, kv kvstore.KV, c kvstore.Conditional, key string) {
	t.Helper()
	ctx := context.Background()
	_, err := c.PutIf(ctx, key, []byte("x"), kvstore.Write{TTL: time.Second})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, found, err := kv.Get(ctx, key)
		return err == nil && !found
	}, 4*time.Second, 20*time.Millisecond, "%s expires", key)
}

// RunConditional asserts kvstore.Conditional against kv: of racing put-if-absent or compare-and-set writes exactly one
// is Done and every loser gets the winner's version; every write gives the key another version; a plain Put
// overwrites and drops a TTL; a conditional delete needs the key's version; an expired key is absent to Get, GetEntry,
// List, Usage and every condition; a compare-and-set with a TTL renews a key; an export and a load carry each key's
// expiry, and a load leaves out a record whose expiry passed; and a sweep deletes an expired key but not one renewed.
// scenarios: claim-race-one-winner, compare-and-set-names-a-version, delete-if-releases-only-its-own,
// plain-put-overwrites, ttl-expires-a-key, renewal-moves-the-expiry, sweep-deletes-what-expired (ADR-0320).
func RunConditional(t *testing.T, kv kvstore.KV) {
	t.Helper()
	ctx := context.Background()
	c, ok := kv.(kvstore.Conditional)
	require.True(t, ok, "the driver offers conditional writes")

	t.Run("put-if-absent-race", func(t *testing.T) {
		rs := race(t, func(i int) (kvstore.Result, error) {
			return c.PutIf(ctx, "c/claim", fmt.Appendf(nil, "holder-%d", i), kvstore.Write{IfAbsent: true})
		})
		won, w := winner(t, rs)
		e, found, err := c.GetEntry(ctx, "c/claim")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, fmt.Sprintf("holder-%d", won), string(e.Value), "the winner's value is stored")
		require.Equal(t, w.Version, e.Version)
	})

	t.Run("compare-and-set-race", func(t *testing.T) {
		first, err := c.PutIf(ctx, "c/cas", []byte("v0"), kvstore.Write{})
		require.NoError(t, err)
		require.True(t, first.Done)
		rs := race(t, func(i int) (kvstore.Result, error) {
			return c.PutIf(ctx, "c/cas", fmt.Appendf(nil, "v-%d", i), kvstore.Write{IfVersion: first.Version})
		})
		won, w := winner(t, rs)
		require.NotEqual(t, first.Version, w.Version, "a write gives the key another version")
		v, _, err := kv.Get(ctx, "c/cas")
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("v-%d", won), string(v))

		stale, err := c.PutIf(ctx, "c/cas", []byte("late"), kvstore.Write{IfVersion: first.Version})
		require.NoError(t, err)
		require.Equal(t, kvstore.Result{Version: w.Version}, stale, "a stale version loses and learns the current one")
		absent, err := c.PutIf(ctx, "c/none", []byte("x"), kvstore.Write{IfVersion: first.Version})
		require.NoError(t, err)
		require.Equal(t, kvstore.Result{}, absent, "a compare-and-set of an absent key loses with no version")
	})

	t.Run("every-write-takes-another-version", func(t *testing.T) {
		a, err := c.PutIf(ctx, "c/ver", []byte("a"), kvstore.Write{})
		require.NoError(t, err)
		require.NoError(t, kv.Put(ctx, "c/ver", []byte("b")))
		e, _, err := c.GetEntry(ctx, "c/ver")
		require.NoError(t, err)
		require.NotEqual(t, a.Version, e.Version, "a plain Put takes another version too")
		b, err := c.PutIf(ctx, "c/ver", []byte("c"), kvstore.Write{IfVersion: e.Version})
		require.NoError(t, err)
		require.True(t, b.Done)
		require.NotEqual(t, e.Version, b.Version)
	})

	t.Run("plain-put-drops-the-ttl", func(t *testing.T) {
		_, err := c.PutIf(ctx, "c/ttl-dropped", []byte("x"), kvstore.Write{TTL: time.Hour})
		require.NoError(t, err)
		e, _, err := c.GetEntry(ctx, "c/ttl-dropped")
		require.NoError(t, err)
		require.NotZero(t, e.ExpiresAt)
		require.NoError(t, kv.Put(ctx, "c/ttl-dropped", []byte("y")))
		e, _, err = c.GetEntry(ctx, "c/ttl-dropped")
		require.NoError(t, err)
		require.Zero(t, e.ExpiresAt, "a plain Put replaces the entry, its TTL included")
	})

	t.Run("each-write-sets-its-own-expiry", func(t *testing.T) {
		w, err := c.PutIf(ctx, "c/own-expiry", []byte("me"), kvstore.Write{IfAbsent: true, TTL: time.Hour})
		require.NoError(t, err)
		cas, err := c.PutIf(ctx, "c/own-expiry", []byte("me"), kvstore.Write{IfVersion: w.Version})
		require.NoError(t, err)
		require.True(t, cas.Done)
		e, _, err := c.GetEntry(ctx, "c/own-expiry")
		require.NoError(t, err)
		require.Zero(t, e.ExpiresAt, "a compare-and-set with no TTL leaves the key with none")
	})

	t.Run("absolute-expiry", func(t *testing.T) {
		at := uint64(time.Now().Add(time.Hour).Unix()) //nolint:gosec // a time after the epoch
		_, err := c.PutIf(ctx, "c/copied", []byte("x"), kvstore.Write{ExpiresAt: at})
		require.NoError(t, err)
		e, _, err := c.GetEntry(ctx, "c/copied")
		require.NoError(t, err)
		require.Equal(t, at, e.ExpiresAt, "a copy keeps the key's own expiry")
	})

	t.Run("delete-if", func(t *testing.T) {
		w, err := c.PutIf(ctx, "c/lease", []byte("me"), kvstore.Write{})
		require.NoError(t, err)
		lost, err := c.DeleteIf(ctx, "c/lease", w.Version+"-other")
		require.NoError(t, err)
		require.Equal(t, kvstore.Result{Version: w.Version}, lost, "another version keeps the key")
		_, found, err := kv.Get(ctx, "c/lease")
		require.NoError(t, err)
		require.True(t, found)
		done, err := c.DeleteIf(ctx, "c/lease", w.Version)
		require.NoError(t, err)
		require.Equal(t, kvstore.Result{Done: true}, done)
		_, found, err = kv.Get(ctx, "c/lease")
		require.NoError(t, err)
		require.False(t, found)
		gone, err := c.DeleteIf(ctx, "c/lease", w.Version)
		require.NoError(t, err)
		require.Equal(t, kvstore.Result{}, gone, "a deleted key loses with no version")
		_, err = c.DeleteIf(ctx, "c/lease", "")
		require.Equal(t, fault.Invalid, fault.KindOf(err), "a conditional delete needs a version")
	})

	t.Run("refused-writes", func(t *testing.T) {
		for _, w := range []kvstore.Write{
			{TTL: -time.Second},
			{IfAbsent: true, IfVersion: "v"},
			{TTL: time.Second, ExpiresAt: 1},
		} {
			_, err := c.PutIf(ctx, "c/bad", nil, w)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%+v", w)
		}
	})

	t.Run("export-and-load-carry-the-expiry", func(t *testing.T) {
		exp, ok := kv.(Exporter)
		if !ok {
			t.Skip("the driver offers no SnapshotPrefix")
		}
		_, err := c.PutIf(ctx, "c/exp/claim", []byte("x"), kvstore.Write{TTL: time.Hour})
		require.NoError(t, err)
		require.NoError(t, kv.Put(ctx, "c/exp/kept", []byte("y")))
		claim, _, err := c.GetEntry(ctx, "c/exp/claim")
		require.NoError(t, err)
		var got []snapshot.Record
		_, err = exp.SnapshotPrefix("c/exp/").Snapshot(ctx, func(r snapshot.Record) error {
			got = append(got, r)
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, []snapshot.Record{
			{Key: []byte("claim"), Value: []byte("x"), ExpiresAt: claim.ExpiresAt},
			{Key: []byte("kept"), Value: []byte("y")},
		}, got)
		ld, ok := kv.(Loader)
		if !ok {
			return
		}
		got = append(got, snapshot.Record{Key: []byte("gone"), Value: []byte("z"), ExpiresAt: 1})
		i := 0
		require.NoError(t, ld.LoadPrefix(ctx, "c/exp2/", func() (snapshot.Record, error) {
			if i == len(got) {
				return snapshot.Record{}, io.EOF
			}
			i++
			return got[i-1], nil
		}))
		keys, err := kv.List(ctx, "c/exp2/")
		require.NoError(t, err)
		require.Equal(t, []string{"c/exp2/claim", "c/exp2/kept"}, keys, "a record whose expiry passed is left out")
		e, _, err := c.GetEntry(ctx, "c/exp2/claim")
		require.NoError(t, err)
		require.Equal(t, claim.ExpiresAt, e.ExpiresAt, "a load keeps the key's expiry")
	})

	t.Run("expiry", func(t *testing.T) {
		t.Parallel()
		before := time.Now()
		w, err := c.PutIf(ctx, "c/exp-ttl/k", []byte("x"), kvstore.Write{IfAbsent: true, TTL: time.Second})
		require.NoError(t, err)
		require.True(t, w.Done)
		e, found, err := c.GetEntry(ctx, "c/exp-ttl/k")
		require.NoError(t, err)
		require.True(t, found)
		require.GreaterOrEqual(t, e.ExpiresAt, uint64(before.Add(time.Second).Unix()), "the key lives at least its TTL")
		require.LessOrEqual(t, e.ExpiresAt, uint64(time.Now().Add(2*time.Second).Unix()), "and at most a second more")
		require.Eventually(t, func() bool {
			_, found, err := kv.Get(ctx, "c/exp-ttl/k")
			return err == nil && !found
		}, 4*time.Second, 20*time.Millisecond, "the key expires")
		_, found, err = c.GetEntry(ctx, "c/exp-ttl/k")
		require.NoError(t, err)
		require.False(t, found)
		keys, err := kv.List(ctx, "c/exp-ttl/")
		require.NoError(t, err)
		require.Empty(t, keys, "List leaves out an expired key")
		if u, ok := kv.(kvstore.Usager); ok {
			n, err := u.Usage(ctx, "c/exp-ttl/")
			require.NoError(t, err)
			require.Zero(t, n, "Usage leaves out an expired key")
		}
		stale, err := c.PutIf(ctx, "c/exp-ttl/k", []byte("late"), kvstore.Write{IfVersion: w.Version})
		require.NoError(t, err)
		require.Equal(t, kvstore.Result{}, stale, "an expired key cannot be renewed")
		again, err := c.PutIf(ctx, "c/exp-ttl/k", []byte("next"), kvstore.Write{IfAbsent: true, TTL: time.Hour})
		require.NoError(t, err)
		require.True(t, again.Done, "an expired key is absent to put-if-absent")
	})

	t.Run("renewal", func(t *testing.T) {
		t.Parallel()
		w, err := c.PutIf(ctx, "c/renew", []byte("me"), kvstore.Write{IfAbsent: true, TTL: time.Second})
		require.NoError(t, err)
		e, _, err := c.GetEntry(ctx, "c/renew")
		require.NoError(t, err)
		renewed, err := c.PutIf(ctx, "c/renew", []byte("me"), kvstore.Write{IfVersion: w.Version, TTL: 3 * time.Second})
		require.NoError(t, err)
		require.True(t, renewed.Done)
		r, _, err := c.GetEntry(ctx, "c/renew")
		require.NoError(t, err)
		require.Greater(t, r.ExpiresAt, e.ExpiresAt, "a renewal moves the expiry")
		require.Eventually(t, func() bool { return !time.Now().Before(time.Unix(int64(e.ExpiresAt), 0)) }, //nolint:gosec // a near Unix second
			3*time.Second, 20*time.Millisecond)
		_, found, err := kv.Get(ctx, "c/renew")
		require.NoError(t, err)
		require.True(t, found, "a renewed key outlives its first expiry")
	})

	t.Run("sweep", func(t *testing.T) {
		t.Parallel()
		s, ok := kv.(kvstore.Sweeper)
		if !ok {
			t.Skip("the driver offers no Sweep")
		}
		w, err := c.PutIf(ctx, "c/sweep/renewed", []byte("me"), kvstore.Write{TTL: time.Second})
		require.NoError(t, err)
		_, err = c.PutIf(ctx, "c/sweep/renewed", []byte("me"), kvstore.Write{IfVersion: w.Version, TTL: time.Hour})
		require.NoError(t, err)
		expireSoon(t, kv, c, "c/sweep/gone")
		swept, _, err := s.Sweep(ctx, time.Now(), 1000, func(string) bool { return false })
		require.NoError(t, err)
		require.Contains(t, swept, "c/sweep/gone")
		require.NotContains(t, swept, "c/sweep/renewed", "a key renewed before its expiry stays")
		_, found, err := kv.Get(ctx, "c/sweep/renewed")
		require.NoError(t, err)
		require.True(t, found)
		again, _, err := s.Sweep(ctx, time.Now(), 1000, func(string) bool { return false })
		require.NoError(t, err)
		require.NotContains(t, again, "c/sweep/gone", "a swept key is swept once")

		expireSoon(t, kv, c, "c/sweep/reclaimed")
		claim, err := c.PutIf(ctx, "c/sweep/reclaimed", []byte("next"), kvstore.Write{IfAbsent: true, TTL: time.Hour})
		require.NoError(t, err)
		require.True(t, claim.Done, "an expired lease is claimed again")
		swept, _, err = s.Sweep(ctx, time.Now(), 1000, func(string) bool { return false })
		require.NoError(t, err)
		require.NotContains(t, swept, "c/sweep/reclaimed", "a lease claimed again before the sweep stays")
		v, found, err := kv.Get(ctx, "c/sweep/reclaimed")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "next", string(v))
	})
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
