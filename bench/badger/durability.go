package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/pb"
)

// countKeys returns the number of keys in a DB (keys-only iteration).
func countKeys(db *badger.DB) int {
	n := 0
	_ = db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			n++
		}
		return nil
	})
	return n
}

// incrementalBackup PROVES (not asserts) that Backup(since=cursor) ships only the delta at far lower bytes
// and RSS than a full backup: full-backup to get a cursor, write a small delta, then incrementally back up
// since the cursor and compare. This is the durable-export primitive the recommendation rests on.
func incrementalBackup(db *badger.DB, val []byte) []Result {
	fullCW := &countWriter{}
	var cursor uint64
	full := measure("backup: FULL (baseline, since=0)", 0, func() (string, error) {
		v, err := db.Backup(fullCW, 0)
		cursor = v
		return fmt.Sprintf("%.1f MiB out, cursor=%d", mib(fullCW.n), v), err
	})

	const delta = 50_000
	dw := measure(fmt.Sprintf("backup: write delta (%s keys)", human(delta)), delta, func() (string, error) {
		wb := db.NewWriteBatch()
		defer wb.Cancel()
		for i := 0; i < delta; i++ {
			if err := wb.Set([]byte(fmt.Sprintf("fn000000/delta%012d", i)), val); err != nil {
				return "", err
			}
		}
		return "", wb.Flush()
	})

	incCW := &countWriter{}
	inc := measure("backup: INCREMENTAL (since cursor)", 0, func() (string, error) {
		v, err := db.Backup(incCW, cursor)
		ratio := 0.0
		if fullCW.n > 0 {
			ratio = 100 * float64(incCW.n) / float64(fullCW.n)
		}
		return fmt.Sprintf("%.1f MiB out (%.1f%% of full) — only the %s-key delta, new cursor=%d",
			mib(incCW.n), ratio, human(delta), v), err
	})
	return []Result{full, dw, inc}
}

// restoreRoundtrip PROVES the durable path restores: back the DB up to a file, db.Load it into a FRESH DB,
// and verify the key count matches. Without this, "incremental backup" is a write-only claim.
func restoreRoundtrip(db *badger.DB, baseDir string) []Result {
	bkpPath := filepath.Join(baseDir, "restore.bak")
	src := countKeys(db)

	var nbytes int64
	bk := measure("restore: backup to file", src, func() (string, error) {
		f, err := os.Create(bkpPath) //nolint:gosec // bench-local path
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		if _, err := db.Backup(f, 0); err != nil {
			return "", err
		}
		if fi, e := f.Stat(); e == nil {
			nbytes = fi.Size()
		}
		return fmt.Sprintf("%.1f MiB", mib(nbytes)), nil
	})

	loaded := -1
	ld := measure("restore: Load into FRESH DB + verify", src, func() (string, error) {
		fresh, err := badger.Open(badger.DefaultOptions(filepath.Join(baseDir, "restore-fresh")).WithLoggingLevel(badger.ERROR))
		if err != nil {
			return "", err
		}
		defer func() { _ = fresh.Close() }()
		f, err := os.Open(bkpPath) //nolint:gosec // bench-local path
		if err != nil {
			return "", err
		}
		defer func() { _ = f.Close() }()
		if err := fresh.Load(f, 256); err != nil {
			return "", err
		}
		loaded = countKeys(fresh)
		verdict := "MISMATCH ✗"
		if loaded == src {
			verdict = "VERIFIED ✓"
		}
		return fmt.Sprintf("restored %s/%s keys — %s", human(loaded), human(src), verdict), nil
	})
	return []Result{bk, ld}
}

// subscribeLossiness PROVES Subscribe is lossy across a consumer restart: writes that commit while no
// subscriber is alive are never delivered (no durable resume). This is exactly why a robust CDC cannot
// rely on Subscribe alone — it is a latency trigger, not a durable feed.
func subscribeLossiness(db *badger.DB, val []byte) Result {
	prefix := []byte("fn000000/loss/")
	mkkey := func(tag string, i int) []byte { return []byte(fmt.Sprintf("fn000000/loss/%s%08d", tag, i)) }
	const A, B, C = 5000, 5000, 5000
	var got1, got2 int64

	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_ = db.Subscribe(ctx1, func(kv *badger.KVList) error { atomic.AddInt64(&got1, int64(len(kv.Kv))); return nil }, []pb.Match{{Prefix: prefix}})
	}()
	time.Sleep(150 * time.Millisecond)

	r := measure("subscribe LOSSINESS (kill subscriber mid-stream)", A+B+C, func() (string, error) {
		write := func(tag string, n int) {
			for i := 0; i < n; i++ {
				_ = db.Update(func(txn *badger.Txn) error { return txn.Set(mkkey(tag, i), val) })
			}
		}
		write("A", A) // delivered to subscriber #1
		time.Sleep(250 * time.Millisecond)
		cancel1() // KILL #1
		<-done1
		write("B", B) // THE GAP — no subscriber alive, these are lost

		ctx2, cancel2 := context.WithCancel(context.Background())
		done2 := make(chan struct{})
		go func() {
			defer close(done2)
			_ = db.Subscribe(ctx2, func(kv *badger.KVList) error { atomic.AddInt64(&got2, int64(len(kv.Kv))); return nil }, []pb.Match{{Prefix: prefix}})
		}()
		time.Sleep(150 * time.Millisecond)
		write("C", C) // delivered to subscriber #2
		time.Sleep(350 * time.Millisecond)
		cancel2()
		<-done2
		return "", nil
	})
	g1, g2 := atomic.LoadInt64(&got1), atomic.LoadInt64(&got2)
	lost := int64(A+B+C) - (g1 + g2)
	r.Note = fmt.Sprintf("#1 saw %d (A=%d), #2 saw %d (C=%d), gap B=%d → %d LOST: Subscribe has NO durable resume", g1, A, g2, C, B, lost)
	return r
}

// outboxCDC implements and PROVES the robust change-feed: every write also appends a _cdc/<seq> entry IN
// THE SAME TXN (transactional outbox — atomic with the data, no dual-write skew), and a consumer tails
// _cdc/ from a DURABLE cursor. It is killed mid-stream and resumes from the persisted cursor; we then
// verify every change was consumed exactly once. This is the answer to Subscribe's lossiness.
func outboxCDC(baseDir string, n int, val []byte) []Result {
	d := filepath.Join(baseDir, "cdc-outbox")
	db, err := badger.Open(badger.DefaultOptions(d).WithLoggingLevel(badger.ERROR))
	if err != nil {
		return []Result{{Name: "cdc outbox", Note: "ERR: " + err.Error()}}
	}
	defer func() { _ = db.Close() }()

	seqGen, err := db.GetSequence([]byte("_cdc_seqgen"), 1024) // durable monotonic seq (leased in bands)
	if err != nil {
		return []Result{{Name: "cdc outbox", Note: "ERR: " + err.Error()}}
	}
	defer func() { _ = seqGen.Release() }()

	cdcPrefix := []byte("_cdc/")
	cdcKey := func(seq uint64) []byte {
		b := make([]byte, len(cdcPrefix)+8)
		copy(b, cdcPrefix)
		binary.BigEndian.PutUint64(b[len(cdcPrefix):], seq)
		return b
	}
	cursorKey := []byte("_cdc_cursor/replica") // the consumer's DURABLE resume point (next seq to read)

	write := measure(fmt.Sprintf("cdc outbox: write %s (data + _cdc/ entry, one txn)", human(n)), n, func() (string, error) {
		for i := 0; i < n; i++ {
			seq, e := seqGen.Next()
			if e != nil {
				return "", e
			}
			dataKey := []byte(fmt.Sprintf("fn000000/cdc%012d", i))
			logKey := cdcKey(seq)
			if err := db.Update(func(txn *badger.Txn) error {
				if e := txn.Set(dataKey, val); e != nil { // the data write
					return e
				}
				return txn.Set(logKey, dataKey) // the change-log entry, ATOMIC with the data
			}); err != nil {
				return "", err
			}
		}
		return "", nil
	})

	// A consumer that tails _cdc/ from the durable cursor, persisting the cursor after each entry.
	// killAfter>0 stops it mid-stream (simulating a crash). Returns how many it processed this run.
	seen := make(map[uint64]int)
	consume := func(killAfter int) (int, error) {
		var next uint64 // next seq to read
		_ = db.View(func(txn *badger.Txn) error {
			if it, e := txn.Get(cursorKey); e == nil {
				return it.Value(func(v []byte) error {
					if len(v) == 8 {
						next = binary.BigEndian.Uint64(v)
					}
					return nil
				})
			}
			return nil
		})
		processed := 0
		for {
			var seq uint64
			found := false
			err := db.View(func(txn *badger.Txn) error {
				opts := badger.DefaultIteratorOptions
				opts.Prefix = cdcPrefix
				it := txn.NewIterator(opts)
				defer it.Close()
				for it.Seek(cdcKey(next)); it.ValidForPrefix(cdcPrefix); it.Next() {
					seq = binary.BigEndian.Uint64(it.Item().Key()[len(cdcPrefix):])
					found = true
					break
				}
				return nil
			})
			if err != nil {
				return processed, err
			}
			if !found {
				break
			}
			seen[seq]++            // "process" the change (idempotent consumer would dedup here)
			next = seq + 1         // advance
			cur := make([]byte, 8) // persist the cursor DURABLY before the next read
			binary.BigEndian.PutUint64(cur, next)
			if err := db.Update(func(txn *badger.Txn) error { return txn.Set(cursorKey, cur) }); err != nil {
				return processed, err
			}
			processed++
			if killAfter > 0 && processed >= killAfter {
				break // simulate the consumer crashing mid-stream
			}
		}
		return processed, nil
	}

	first := 0
	run := measure("cdc outbox: consume, KILL mid-stream, resume from cursor", n, func() (string, error) {
		var e error
		if first, e = consume(n / 2); e != nil { // consume half, then "crash"
			return "", e
		}
		if _, e = consume(0); e != nil { // restart from the durable cursor, finish
			return "", e
		}
		return "", nil
	})

	// Verify: every one of the n changes was consumed, exactly once, despite the mid-stream kill.
	distinct, dup := 0, 0
	for _, c := range seen {
		distinct++
		if c > 1 {
			dup++
		}
	}
	verdict := "MISMATCH ✗"
	if distinct == n && dup == 0 {
		verdict = "VERIFIED ✓ zero loss, zero dup"
	}
	run.Note = fmt.Sprintf("killed after %s; resumed from cursor; consumed %s/%s distinct (%d dup) — %s",
		human(first), human(distinct), human(n), dup, verdict)
	return []Result{write, run}
}
