// Command badger-bench characterizes Badger's inherent limits for funcd's metastore / per-function KV
// use case (the slatedb → pure-Go engine question). It writes up to N keys spread across F function
// prefixes (modeling the storage ADR's prefix-per-function layout), then measures throughput AND the OS
// RSS cost of every operation class: bulk-write, txn-write, point-get (warm + cold), full scan, prefix
// scan, point-delete, DropPrefix (per-function wipe), parallel stream, full backup, Subscribe, the merge
// operator, and steady idle RSS — plus a close/reopen to expose the dormant-store reopen cost.
//
// RSS is the headline number: funcd's target is RAM-bound, so the binding question is "what does Badger
// hold resident", not throughput. Run the same workload under three profiles (default / lowmem / inmem)
// to see how far the Badger memory-usage knobs move it. Isolated in its own module — not in funcd's build.
//
//	go run . -keys 1000000 -profile lowmem -json out-lowmem.json
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

// rng is a seeded, deterministic random source so runs are comparable.
type rng struct{ r *rand.Rand }

func newRNG(seed int64) *rng { return &rng{rand.New(rand.NewSource(seed))} }

func (g *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return g.r.Intn(n)
}

func gomaxprocs() int { return runtime.GOMAXPROCS(0) }

func main() {
	keys := flag.Int("keys", 1_000_000, "number of keys to write")
	funcs := flag.Int("funcs", 1000, "function prefixes to spread keys across (keys/funcs per function)")
	valsize := flag.Int("valsize", 256, "value size in bytes (funcd resources are small blobs)")
	profile := flag.String("profile", "lowmem", "option profile: default | lowmem | inmem")
	dir := flag.String("dir", "", "data dir (default: a temp dir, removed unless -keep)")
	getops := flag.Int("gets", 200_000, "random point gets")
	mergeops := flag.Int("merges", 100_000, "merge-operator increments")
	subops := flag.Int("subs", 20_000, "writes under an active Subscribe")
	delops := flag.Int("dels", 100_000, "random point deletes")
	txnops := flag.Int("txns", 100_000, "extra txn-path writes")
	txnbatch := flag.Int("txnbatch", 16, "puts per txn-path transaction")
	jsonOut := flag.String("json", "", "also write the JSON report to this path")
	keep := flag.Bool("keep", false, "keep the data dir after the run")
	sync := flag.Bool("sync", false, "SyncWrites (fsync on every commit)")
	flag.Parse()

	d := *dir
	cleanup := func() {}
	if d == "" {
		var err error
		if d, err = os.MkdirTemp("", "badger-bench-*"); err != nil {
			fatal(err)
		}
		if !*keep {
			cleanup = func() { _ = os.RemoveAll(d) }
		}
	} else if err := os.MkdirAll(d, 0o755); err != nil {
		fatal(err)
	}
	defer cleanup()

	val := make([]byte, *valsize)
	_, _ = newRNG(1).r.Read(val)

	fmt.Printf("opening Badger v4: profile=%s dir=%s keys=%s funcs=%d value=%dB sync=%v\n",
		*profile, d, human(*keys), *funcs, *valsize, *sync)
	runPeak := startPeakSampler()
	opts := openOptions(*profile, d, *valsize+16, *sync)
	db, err := badger.Open(opts)
	if err != nil {
		fatal(err)
	}

	rnd := newRNG(42)
	var results []Result
	add := func(r Result) {
		results = append(results, r)
		fmt.Printf("  %-30s %10s ops %7dms  RSS %.0f→%.0f (peak %.0f)  %s\n",
			r.Name, human(r.Ops), r.Millis, r.RSSBeforeMB, r.RSSAfterMB, r.RSSPeakMB, r.Note)
	}

	// Phase A — normal "serving" ops (writes, reads, scans, deletes). The steady idle RSS measured at
	// the END of this phase is the clean dormant-store headline (no export buffers yet allocated).
	add(bulkWrite(db, *keys, *funcs, val))
	add(pointGet(db, "get (warm, random)", *keys, *getops, *funcs, rnd))
	add(scanAll(db, false))
	add(scanAll(db, true))
	add(prefixScan(db, *funcs/2))
	add(txnWrite(db, *keys, *txnops, *txnbatch, *funcs, val))
	add(pointDelete(db, *keys, *delops, *funcs, rnd))
	add(mergeOp(db, *mergeops))
	add(idleHold(db, 2*time.Second).rename("idle-hold (serving, clean)"))

	// Phase B — the maintenance/export path. Subscribe/backup/stream allocate large pooled off-heap
	// z.Buffers; the idle-hold AFTER them shows the RSS high-water those leave behind (a real cost to
	// weigh: running exports inflates the process's resident footprint independent of dataset size).
	add(subscribe(db, *subops, *funcs, val))
	add(backup(db))
	add(streamRead(db))
	add(dropPrefix(db, *funcs/2))
	add(idleHold(db, 2*time.Second).rename("idle-hold (after export)"))

	lsm, vlog := db.Size()
	dirBytes := dirSize(d)

	// close + reopen → the dormant-store reopen cost: cold open time, cold idle RSS, cold-cache gets.
	if *profile != "inmem" {
		if err := db.Close(); err != nil {
			fatal(err)
		}
		t0 := time.Now()
		if db, err = badger.Open(opts); err != nil {
			fatal(err)
		}
		add(measure("reopen (cold open)", 1, func() (string, error) {
			return fmt.Sprintf("opened in %dms", time.Since(t0).Milliseconds()), nil
		}))
		add(idleHold(db, 2*time.Second).rename("idle-hold (after reopen)"))
		add(pointGet(db, "get (cold, random)", *keys, *getops, *funcs, rnd))
	}

	finalRSS := processRSSMB()
	if err := db.Close(); err != nil {
		fatal(err)
	}
	peak := runPeak.stopAndPeakMB()

	rep := Report{
		Profile: *profile, Keys: *keys, Funcs: *funcs, ValueBytes: *valsize, SyncWrites: *sync,
		GOMAXPROCS: gomaxprocs(), LSMBytes: lsm, VlogBytes: vlog, DirBytes: dirBytes,
		RunPeakRSS: round(peak), FinalRSSMB: round(finalRSS), Results: results,
	}
	rep.printTable()
	if *jsonOut != "" {
		if err := rep.writeJSON(*jsonOut); err != nil {
			fatal(err)
		}
		fmt.Printf("\nJSON → %s\n", *jsonOut)
	}
}

func dirSize(d string) int64 {
	var n int64
	_ = filepath.WalkDir(d, func(_ string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if info, e2 := e.Info(); e2 == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "FATAL:", err)
	os.Exit(1)
}
