package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/pb"
	"github.com/dgraph-io/ristretto/v2/z"
)

// keyFor models the funcd prefix-per-function layout from the storage ADR: keys live under "fnNNNNNN/…",
// so a function's whole store is one prefix — what prefix-scan and DropPrefix operate on.
func keyFor(i, funcs int) []byte {
	return []byte(fmt.Sprintf("fn%06d/%012d", i%funcs, i))
}

func fnPrefix(fnID int) []byte { return []byte(fmt.Sprintf("fn%06d/", fnID)) }

// measure wraps a scenario: sample RSS before, run a background peak sampler, time it, sample after.
func measure(name string, ops int, fn func() (string, error)) Result {
	before := processRSSMB()
	ps := startPeakSampler()
	start := time.Now()
	note, err := fn()
	el := time.Since(start)
	peak := ps.stopAndPeakMB()
	r := Result{
		Name: name, Ops: ops, Millis: el.Milliseconds(),
		RSSBeforeMB: round(before), RSSAfterMB: round(processRSSMB()),
		RSSPeakMB: round(peak), GoHeapMB: round(goHeapMB()), Note: note,
	}
	if s := el.Seconds(); s > 0 {
		r.OpsPerSec = float64(ops) / s
	}
	if err != nil {
		r.Note = "ERR: " + err.Error()
	}
	return r
}

// bulkWrite ingests `keys` entries via a WriteBatch (Badger's fast bulk path), spread across `funcs`.
func bulkWrite(db *badger.DB, keys, funcs int, val []byte) Result {
	return measure("bulk-write (WriteBatch)", keys, func() (string, error) {
		wb := db.NewWriteBatch()
		defer wb.Cancel()
		for i := 0; i < keys; i++ {
			if err := wb.Set(keyFor(i, funcs), val); err != nil {
				return "", err
			}
		}
		return "", wb.Flush()
	})
}

// txnWrite writes `n` more entries in transactions of `batch` puts each (the gateway's group-commit path).
func txnWrite(db *badger.DB, base, n, batch, funcs int, val []byte) Result {
	return measure(fmt.Sprintf("txn-write (Update, batch=%d)", batch), n, func() (string, error) {
		for i := 0; i < n; i += batch {
			err := db.Update(func(txn *badger.Txn) error {
				for j := i; j < i+batch && j < n; j++ {
					if e := txn.Set(keyFor(base+j, funcs), val); e != nil {
						return e
					}
				}
				return nil
			})
			if err != nil {
				return "", err
			}
		}
		return "", nil
	})
}

// pointGet does `gets` random point reads (reading the value) against the `keys` already written.
func pointGet(db *badger.DB, label string, keys, gets, funcs int, rnd *rng) Result {
	return measure(label, gets, func() (string, error) {
		var miss int
		err := db.View(func(txn *badger.Txn) error {
			for i := 0; i < gets; i++ {
				item, err := txn.Get(keyFor(rnd.intn(keys), funcs))
				if err == badger.ErrKeyNotFound {
					miss++
					continue
				}
				if err != nil {
					return err
				}
				if err := item.Value(func([]byte) error { return nil }); err != nil {
					return err
				}
			}
			return nil
		})
		return fmt.Sprintf("%d miss", miss), err
	})
}

// scanAll iterates the entire keyspace (keys only, or keys+values) — the full-table read cost.
func scanAll(db *badger.DB, withValues bool) Result {
	label := "scan (keys only)"
	if withValues {
		label = "scan (keys+values)"
	}
	var n int
	return measure(label, 0, func() (string, error) {
		err := db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.PrefetchValues = withValues
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Rewind(); it.Valid(); it.Next() {
				n++
				if withValues {
					if err := it.Item().Value(func([]byte) error { return nil }); err != nil {
						return err
					}
				}
			}
			return nil
		})
		return fmt.Sprintf("%s keys", human(n)), err
	}).withOps(n)
}

// prefixScan reads ONE function's keys (the per-function range scan) — should be O(that function), not O(all).
func prefixScan(db *badger.DB, fnID int) Result {
	var n int
	prefix := fnPrefix(fnID)
	return measure("prefix-scan (one fn)", 0, func() (string, error) {
		err := db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = prefix
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
				n++
				if err := it.Item().Value(func([]byte) error { return nil }); err != nil {
					return err
				}
			}
			return nil
		})
		return fmt.Sprintf("%s keys in fn%06d", human(n), fnID), err
	}).withOps(n)
}

// pointDelete removes `n` random keys (the per-key delete path).
func pointDelete(db *badger.DB, keys, n, funcs int, rnd *rng) Result {
	return measure("delete (point)", n, func() (string, error) {
		for i := 0; i < n; i += 256 {
			err := db.Update(func(txn *badger.Txn) error {
				for j := i; j < i+256 && j < n; j++ {
					if e := txn.Delete(keyFor(rnd.intn(keys), funcs)); e != nil {
						return e
					}
				}
				return nil
			})
			if err != nil {
				return "", err
			}
		}
		return "", nil
	})
}

// dropPrefix wipes one function's whole store — the funcd per-function GDPR-delete (the `rm file.db` equivalent).
func dropPrefix(db *badger.DB, fnID int) Result {
	return measure("DropPrefix (per-fn wipe)", 1, func() (string, error) {
		return fmt.Sprintf("dropped fn%06d", fnID), db.DropPrefix(fnPrefix(fnID))
	})
}

// streamRead does a parallel full read via NewStream (the machinery backup/export ride on).
func streamRead(db *badger.DB) Result {
	var count int64
	return measure("stream (parallel read)", 0, func() (string, error) {
		st := db.NewStream()
		st.NumGo = gomaxprocs()
		st.Send = func(buf *z.Buffer) error {
			list, err := badger.BufferToKVList(buf)
			if err != nil {
				return err
			}
			atomic.AddInt64(&count, int64(len(list.Kv)))
			return nil
		}
		err := st.Orchestrate(context.Background())
		return fmt.Sprintf("%s kvs", human(int(count))), err
	}).withOps(int(count))
}

// backup does a full Backup(since=0) to a counting sink — the DR-export cost + the bytes it emits.
func backup(db *badger.DB) Result {
	cw := &countWriter{}
	var version uint64
	r := measure("backup (full, since=0)", 0, func() (string, error) {
		v, err := db.Backup(cw, 0)
		version = v
		return fmt.Sprintf("%.1f MiB out, version=%d", mib(cw.n), version), err
	})
	return r
}

// subscribe runs db.Subscribe (the latency trigger, NOT the durable path) while writing `n` keys under a
// prefix, and reports how many notifications landed — i.e. whether the trigger keeps up, not durability.
func subscribe(db *badger.DB, n, funcs int, val []byte) Result {
	prefix := fnPrefix(0)
	var got int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = db.Subscribe(ctx, func(kv *badger.KVList) error {
			atomic.AddInt64(&got, int64(len(kv.Kv)))
			return nil
		}, []pb.Match{{Prefix: prefix}})
	}()
	time.Sleep(100 * time.Millisecond) // let Subscribe register before writing

	r := measure("subscribe (writes under watch)", n, func() (string, error) {
		for i := 0; i < n; i++ {
			key := []byte(fmt.Sprintf("fn000000/sub%012d", i))
			if err := db.Update(func(txn *badger.Txn) error { return txn.Set(key, val) }); err != nil {
				return "", err
			}
		}
		return "", nil
	})
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt64(&got) < int64(n) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	r.Note = fmt.Sprintf("%s/%s notifications delivered", human(int(atomic.LoadInt64(&got))), human(n))
	return r
}

// mergeOp exercises the MergeOperator (a server-side read-modify-write counter): `n` increments, then read.
func mergeOp(db *badger.DB, n int) Result {
	add := func(existing, delta []byte) []byte {
		var cur uint64
		if len(existing) == 8 {
			cur = binary.BigEndian.Uint64(existing)
		}
		cur += binary.BigEndian.Uint64(delta)
		out := make([]byte, 8)
		binary.BigEndian.PutUint64(out, cur)
		return out
	}
	m := db.GetMergeOperator([]byte("fn000000/merge-counter"), add, 200*time.Millisecond)
	defer m.Stop()
	one := make([]byte, 8)
	binary.BigEndian.PutUint64(one, 1)
	r := measure("merge-operator (counter)", n, func() (string, error) {
		for i := 0; i < n; i++ {
			if err := m.Add(one); err != nil {
				return "", err
			}
		}
		v, err := m.Get()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("counter=%d", binary.BigEndian.Uint64(v)), nil
	})
	return r
}

// idleHold measures the steady RSS of holding the open DB with all keys, doing nothing (the dormant cost).
func idleHold(db *badger.DB, d time.Duration) Result {
	_ = db
	return measure("idle-hold (steady RSS)", 0, func() (string, error) {
		runtime.GC()
		time.Sleep(d)
		return "after GC + idle", nil
	})
}

// withOps backfills the op count for scenarios that only know it after the run (scans/streams).
func (r Result) withOps(ops int) Result {
	r.Ops = ops
	if s := float64(r.Millis) / 1000; s > 0 {
		r.OpsPerSec = float64(ops) / s
	}
	return r
}

// rename relabels a Result (the reopen phase reuses idleHold under a clearer name).
func (r Result) rename(name string) Result { r.Name = name; return r }

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
