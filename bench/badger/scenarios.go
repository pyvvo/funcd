package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
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

// concurrentMixed runs `readers` get-goroutines + `writers` put-goroutines against the populated DB for
// `dur` — keys spread across the keyspace (mostly disjoint, so it measures concurrent throughput scaling,
// the gateway serving many replicas' requests). Conflicts here are rare (single-key Set, no read).
func concurrentMixed(db *badger.DB, readers, writers int, dur time.Duration, keys, funcs int, val []byte) Result {
	var reads, writes, retries int64
	r := measure(fmt.Sprintf("concurrent mixed (%dR/%dW, %s)", readers, writers, dur), 0, func() (string, error) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < readers; i++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				g := newRNG(int64(seed))
				for {
					select {
					case <-stop:
						return
					default:
					}
					_ = db.View(func(txn *badger.Txn) error {
						if item, err := txn.Get(keyFor(g.intn(keys), funcs)); err == nil {
							_ = item.Value(func([]byte) error { return nil })
						}
						return nil
					})
					atomic.AddInt64(&reads, 1)
				}
			}(i*7 + 1)
		}
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				g := newRNG(int64(seed))
				for {
					select {
					case <-stop:
						return
					default:
					}
					for {
						err := db.Update(func(txn *badger.Txn) error { return txn.Set(keyFor(g.intn(keys), funcs), val) })
						if err == badger.ErrConflict {
							atomic.AddInt64(&retries, 1)
							continue
						}
						break
					}
					atomic.AddInt64(&writes, 1)
				}
			}(i*13 + 3)
		}
		time.Sleep(dur)
		close(stop)
		wg.Wait()
		return "", nil
	})
	rd, wr := atomic.LoadInt64(&reads), atomic.LoadInt64(&writes)
	secs := float64(r.Millis) / 1000
	r.Ops = int(rd + wr)
	if secs > 0 {
		r.OpsPerSec = float64(rd+wr) / secs
		r.Note = fmt.Sprintf("%s reads/s + %s writes/s, %d conflicts",
			human(int(float64(rd)/secs)), human(int(float64(wr)/secs)), retries)
	}
	return r
}

// contendedHotKey has `writers` goroutines read-modify-write the SAME key in serializable txns — the
// worst case: every overlapping txn conflicts. It quantifies how badly uncoordinated multi-writer
// contention degrades (the empirical case FOR the single-writer gateway), via the SSI conflict-retry rate.
func contendedHotKey(db *badger.DB, writers int, dur time.Duration) Result {
	var commits, conflicts int64
	hot := []byte("fn000000/HOT-COUNTER")
	r := measure(fmt.Sprintf("contended hot-key RMW (%dW, %s)", writers, dur), 0, func() (string, error) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					err := db.Update(func(txn *badger.Txn) error {
						var cur uint64
						if item, e := txn.Get(hot); e == nil {
							_ = item.Value(func(v []byte) error {
								if len(v) == 8 {
									cur = binary.BigEndian.Uint64(v)
								}
								return nil
							})
						}
						b := make([]byte, 8)
						binary.BigEndian.PutUint64(b, cur+1)
						return txn.Set(hot, b)
					})
					switch err {
					case badger.ErrConflict:
						atomic.AddInt64(&conflicts, 1)
					case nil:
						atomic.AddInt64(&commits, 1)
					}
				}
			}()
		}
		time.Sleep(dur)
		close(stop)
		wg.Wait()
		return "", nil
	})
	secs := float64(r.Millis) / 1000
	c := atomic.LoadInt64(&commits)
	r.Ops = int(c)
	if secs > 0 {
		r.OpsPerSec = float64(c) / secs
	}
	total := c + atomic.LoadInt64(&conflicts)
	rate := 0.0
	if total > 0 {
		rate = 100 * float64(conflicts) / float64(total)
	}
	r.Note = fmt.Sprintf("%s commits/s, %s conflict-retries (%.0f%% of attempts) — SSI", human(int(r.OpsPerSec)), human(int(conflicts)), rate)
	return r
}

// singleWriterGateway implements + measures the storage-ADR's design: ALL writes to a store go through ONE
// serializing writer with group commit (queued ops coalesced into one txn). It is the SOLUTION to the
// contended-hot-key problem — because there are never two concurrent txns on the store, write-write
// conflicts are 0 by construction, and group commit amortizes the per-commit cost across the batch.
// `hot`=true sends every client to the SAME key (the direct contrast to contendedHotKey's 86% conflicts);
// false spreads writes across the keyspace (the realistic gateway load — is the single writer a bottleneck?).
func singleWriterGateway(db *badger.DB, clients int, dur time.Duration, batchMax int, hot bool, keys, funcs int, val []byte) Result {
	type req struct {
		key  []byte
		done chan struct{}
	}
	reqs := make(chan req, clients*2)
	var served, txns, batchSum int64
	writerStop := make(chan struct{})
	writerDone := make(chan struct{})

	// THE single writer — owns the store's write path. Group commit by GREEDY DRAIN: block for one req,
	// then non-blockingly pull whatever else is queued (up to batchMax) into the same txn, and commit
	// immediately. No fixed timer — the batch self-tunes to load, and throughput is bounded by commit
	// latency, not an artificial wait. (A timer-based flush throttles to 1/period and is the classic
	// group-commit footgun.)
	go func() {
		defer close(writerDone)
		batch := make([]req, 0, batchMax)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			_ = db.Update(func(txn *badger.Txn) error {
				for _, r := range batch {
					if e := txn.Set(r.key, val); e != nil {
						return e
					}
				}
				return nil
			})
			atomic.AddInt64(&txns, 1)
			atomic.AddInt64(&batchSum, int64(len(batch)))
			atomic.AddInt64(&served, int64(len(batch)))
			for _, r := range batch {
				close(r.done)
			}
			batch = batch[:0]
		}
		for {
			select {
			case r := <-reqs:
				batch = append(batch, r)
			case <-writerStop:
				for { // drain anything still queued, then exit
					select {
					case r := <-reqs:
						batch = append(batch, r)
						if len(batch) >= batchMax {
							flush()
						}
					default:
						flush()
						return
					}
				}
			}
			for draining := true; draining && len(batch) < batchMax; { // greedily coalesce what's queued now
				select {
				case r := <-reqs:
					batch = append(batch, r)
				default:
					draining = false
				}
			}
			flush()
		}
	}()

	label := "single-writer gateway (spread)"
	hotKey := []byte("fn000000/GW-HOT")
	if hot {
		label = "single-writer gateway (hot key)"
	}
	r := measure(label, 0, func() (string, error) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for c := 0; c < clients; c++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				g := newRNG(int64(seed))
				for {
					select {
					case <-stop:
						return
					default:
					}
					k := hotKey
					if !hot {
						k = keyFor(g.intn(keys), funcs)
					}
					d := make(chan struct{})
					reqs <- req{k, d} // the writer is alive until writerStop (after wg.Wait), so this always completes
					<-d
				}
			}(c*5 + 1)
		}
		time.Sleep(dur)
		close(stop)
		wg.Wait()        // clients finish their in-flight op, then exit
		close(writerStop) // only now stop the writer
		<-writerDone
		return "", nil
	})
	secs := float64(r.Millis) / 1000
	sv, tx := atomic.LoadInt64(&served), atomic.LoadInt64(&txns)
	avg := 0.0
	if tx > 0 {
		avg = float64(batchSum) / float64(tx)
	}
	r.Ops = int(sv)
	if secs > 0 {
		r.OpsPerSec = float64(sv) / secs
		r.Note = fmt.Sprintf("%s ops/s served via %s txns/s (avg batch %.0f) — 0 conflicts (serialized)",
			human(int(float64(sv)/secs)), human(int(float64(tx)/secs)), avg)
	}
	return r
}

// syncCostCompare opens two fresh DBs — SyncWrites off and on — and times `n` single-key commits on each.
// The delta is the cost of an fsync-durable ack (funcd's "200 only after the commit is on disk"). Returns
// both Results so the table shows them side by side.
func syncCostCompare(baseDir string, n int, val []byte) []Result {
	var out []Result
	for _, sync := range []bool{false, true} {
		d := filepath.Join(baseDir, fmt.Sprintf("synccmp-%v", sync))
		opts := badger.DefaultOptions(d).WithLoggingLevel(badger.ERROR).
			WithSyncWrites(sync).WithValueLogFileSize(64 << 20)
		db, err := badger.Open(opts)
		if err != nil {
			out = append(out, Result{Name: fmt.Sprintf("commit latency (sync=%v)", sync), Note: "ERR: " + err.Error()})
			continue
		}
		r := measure(fmt.Sprintf("commit latency (sync=%v)", sync), n, func() (string, error) {
			for i := 0; i < n; i++ {
				k := []byte(fmt.Sprintf("k%012d", i))
				if err := db.Update(func(txn *badger.Txn) error { return txn.Set(k, val) }); err != nil {
					return "", err
				}
			}
			return "", nil
		})
		if n > 0 {
			r.Note = fmt.Sprintf("%.1f µs/commit", float64(r.Millis)*1000/float64(n))
		}
		_ = db.Close()
		out = append(out, r)
	}
	return out
}

// rename relabels a Result (the reopen phase reuses idleHold under a clearer name).
func (r Result) rename(name string) Result { r.Name = name; return r }

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
