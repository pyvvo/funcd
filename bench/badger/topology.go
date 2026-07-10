package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	badger "github.com/dgraph-io/badger/v4"
)

// The topology bench answers the funcd store-tiering question empirically instead of by argument:
// is ONE Badger instance (metastore + workflow run-state + DLQ as key prefixes — "store/", "run/",
// "dl/") enough, or do the three concerns each warrant their own instance? It models the real churn
// asymmetry — a near-static, read-heavy metastore next to high-churn ephemeral workflow/DLQ writes —
// and measures the three things the decision actually turns on:
//
//   1. RSS: three instances each carry their own memtables + block/index caches + compactors; one
//      instance carries a single set. The delta is the RAM tax of splitting (funcd is RAM-bound).
//   2. Interference: does the churn (LSM compaction driven by the DLQ/workflow overwrites+deletes)
//      degrade metastore READ latency when they share an instance? This is the only real cost of
//      co-tenancy — Badger compaction/vlog-GC are per-INSTANCE, not per-prefix.
//   3. Backup: one instance = one whole-DB db.Backup = a single CONSISTENT point-in-time across all
//      three tables (the Postgres "back up the database, not the table" model). Three instances = three
//      independent snapshots with no cross-store consistency, and no single recovery point.
//
// Run "shared" and "isolated" as SEPARATE processes for clean RSS (RSS is process-global); the "both"
// mode runs them in-sequence for convenience and reports an RSS delta relative to a per-mode baseline.
//
//	go run . -topology -topo both -metakeys 50000 -readers 8 -writers 4 -concdur 5s

// topoDBs holds the Badger handles for one topology. In "shared" all three fields point at the SAME
// *badger.DB (one instance, three prefixes); in "isolated" they are three independent instances.
type topoDBs struct {
	meta, run, dl *badger.DB
	all           []*badger.DB // the DISTINCT handles to close/backup/GC (1 for shared, 3 for isolated)
	shared        bool
}

func storeKey(i int) []byte { return []byte(fmt.Sprintf("store/%012d", i)) } // metastore (near-static)
func runKey(i int) []byte   { return []byte(fmt.Sprintf("run/%012d", i)) }   // workflow run-state (churn)
func dlKey(i int) []byte    { return []byte(fmt.Sprintf("dl/%012d", i)) }    // dead-letter (high churn)

func openTopoDBs(shared bool, dir, profile string, valThresh int, sync bool) (*topoDBs, error) {
	open := func(name string) (*badger.DB, error) {
		return badger.Open(openOptions(profile, filepath.Join(dir, name), valThresh, sync))
	}
	if shared {
		db, err := open("shared")
		if err != nil {
			return nil, err
		}
		return &topoDBs{meta: db, run: db, dl: db, all: []*badger.DB{db}, shared: true}, nil
	}
	m, err := open("store")
	if err != nil {
		return nil, err
	}
	r, err := open("workflow")
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	d, err := open("deadletter")
	if err != nil {
		_ = m.Close()
		_ = r.Close()
		return nil, err
	}
	return &topoDBs{meta: m, run: r, dl: d, all: []*badger.DB{m, r, d}}, nil
}

// countingWriter is a discard sink that tallies bytes — so db.Backup's export size is measured without
// buffering the whole snapshot.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// topoReport is one topology's measured result.
type topoReport struct {
	Mode           string  `json:"mode"`
	Instances      int     `json:"instances"`
	PopulateMs     int64   `json:"populateMs"`
	RSSBaselineMB  float64 `json:"rssBaselineMB"`
	RSSPopulatedMB float64 `json:"rssPopulatedMB"`
	RSSDeltaMB     float64 `json:"rssDeltaMB"`
	DiskBytes      int64   `json:"diskBytes"`
	ReaderOps      int     `json:"readerOps"`
	ReaderOpsSec   float64 `json:"readerOpsSec"`
	ReaderP50us    float64 `json:"readerP50us"`
	ReaderP99us    float64 `json:"readerP99us"`
	WriterTxns     int     `json:"writerTxns"`
	WriterTxnsSec  float64 `json:"writerTxnsSec"`
	RunPeakRSSMB   float64 `json:"runPeakRssMB"`
	BackupMs       int64   `json:"backupMs"`
	BackupBytes    int64   `json:"backupBytes"`
}

// populateMeta writes the near-static metastore keyspace via the bulk path.
func populateMeta(db *badger.DB, metakeys int, val []byte) error {
	wb := db.NewWriteBatch()
	defer wb.Cancel()
	for i := 0; i < metakeys; i++ {
		if err := wb.Set(storeKey(i), val); err != nil {
			return err
		}
	}
	return wb.Flush()
}

// mixedRun runs the concurrent workload for dur: `readers` goroutines point-getting random metastore
// keys (recording per-op latency) while `writers` goroutines churn the DLQ (append + bounded sweep) and
// the workflow run-state (overwrite a bounded working set → LSM garbage → compaction). A per-instance
// value-log-GC ticker runs alongside (models funcd's GC loop; for small inline values it's a near no-op,
// so the co-tenancy cost that shows up is LSM COMPACTION, not vlog GC). Returns reader ops, p50/p99 read
// latency (µs), and committed write txns.
func mixedRun(t *topoDBs, val []byte, metakeys, churnWS, readers, writers, churnRate int, dur time.Duration) (int, float64, float64, int) {
	stop := make(chan struct{})
	gcStop := make(chan struct{})
	var rOps, wTxns int64
	lat := make([][]int64, readers)
	var wg, gcwg sync.WaitGroup

	// churnRate > 0 caps the AGGREGATE churn at that many txn/sec (funcd's real DLQ/workflow rate is
	// modest, not saturated); 0 lets the writers run flat-out (worst-case interference).
	var iterSleep time.Duration
	if churnRate > 0 {
		iterSleep = time.Duration(float64(2*writers) / float64(churnRate) * float64(time.Second))
	}

	for _, db := range t.all { // per-instance vlog GC — shared: one GC over ALL prefixes; isolated: three
		gcwg.Add(1)
		go func(db *badger.DB) {
			defer gcwg.Done()
			tk := time.NewTicker(400 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-gcStop:
					return
				case <-tk.C:
					for db.RunValueLogGC(0.5) == nil { //nolint:revive // drain reclaimable vlog files
					}
				}
			}
		}(db)
	}

	for i := 0; i < readers; i++ { // metastore readers — the latency-sensitive path
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			g := newRNG(int64(1000 + id))
			ls := make([]int64, 0, 1<<16)
			for {
				select {
				case <-stop:
					lat[id] = ls
					return
				default:
				}
				k := storeKey(g.intn(metakeys))
				s := time.Now()
				_ = t.meta.View(func(txn *badger.Txn) error {
					item, err := txn.Get(k)
					if err != nil {
						return nil
					}
					return item.Value(func([]byte) error { return nil })
				})
				ls = append(ls, time.Since(s).Nanoseconds())
				atomic.AddInt64(&rOps, 1)
			}
		}(i)
	}

	var seq uint64
	for i := 0; i < writers; i++ { // DLQ + workflow churn — drives compaction on the shared instance
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				n := int(atomic.AddUint64(&seq, 1))
				_ = t.dl.Update(func(txn *badger.Txn) error { // append newest, sweep oldest (bounded DLQ)
					if err := txn.Set(dlKey(n), val); err != nil {
						return err
					}
					if n > churnWS {
						return txn.Delete(dlKey(n - churnWS))
					}
					return nil
				})
				_ = t.run.Update(func(txn *badger.Txn) error { // overwrite a bounded run-state slot → garbage
					return txn.Set(runKey(n%churnWS), val)
				})
				atomic.AddInt64(&wTxns, 2)
				if iterSleep > 0 {
					time.Sleep(iterSleep)
				}
			}
		}()
	}

	time.Sleep(dur)
	close(stop)
	wg.Wait()
	close(gcStop)
	gcwg.Wait()

	var all []int64
	for _, ls := range lat {
		all = append(all, ls...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	pct := func(q float64) float64 {
		if len(all) == 0 {
			return 0
		}
		return float64(all[int(q*float64(len(all)-1))]) / 1000 // ns → µs
	}
	return int(rOps), round(pct(0.50)), round(pct(0.99)), int(wTxns)
}

// runTopology executes one topology end-to-end (populate → idle RSS → mixed run → disk → backup) and
// returns its measured report.
func runTopology(mode, baseDir, profile string, val []byte, metakeys, churnWS, readers, writers, churnRate int, dur time.Duration, sync bool) topoReport {
	shared := mode == "shared"
	dir := filepath.Join(baseDir, "topo-"+mode)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	rep := topoReport{Mode: mode}
	rep.RSSBaselineMB = round(processRSSMB())

	t, err := openTopoDBs(shared, dir, profile, len(val)+16, sync)
	if err != nil {
		fatal(err)
	}
	rep.Instances = len(t.all)

	st := time.Now()
	if err := populateMeta(t.meta, metakeys, val); err != nil {
		fatal(err)
	}
	rep.PopulateMs = time.Since(st).Milliseconds()

	time.Sleep(1 * time.Second) // let flushes settle
	_ = goHeapMB()              // force a GC so the RSS reading is the resident floor, not allocator slack
	rep.RSSPopulatedMB = round(processRSSMB())
	rep.RSSDeltaMB = round(rep.RSSPopulatedMB - rep.RSSBaselineMB)

	ps := startPeakSampler()
	rOps, p50, p99, wTxns := mixedRun(t, val, metakeys, churnWS, readers, writers, churnRate, dur)
	rep.RunPeakRSSMB = round(ps.stopAndPeakMB())
	rep.ReaderOps, rep.WriterTxns = rOps, wTxns
	rep.ReaderOpsSec = round(float64(rOps) / dur.Seconds())
	rep.WriterTxnsSec = round(float64(wTxns) / dur.Seconds())
	rep.ReaderP50us, rep.ReaderP99us = p50, p99

	rep.DiskBytes = dirSize(dir)

	cw := &countingWriter{}
	bst := time.Now()
	for _, db := range t.all { // shared: ONE consistent snapshot; isolated: N independent snapshots
		if _, err := db.Backup(cw, 0); err != nil {
			fatal(err)
		}
	}
	rep.BackupMs = time.Since(bst).Milliseconds()
	rep.BackupBytes = cw.n

	for _, db := range t.all {
		_ = db.Close()
	}
	return rep
}

func (r topoReport) print() {
	fmt.Printf("  instances                %d\n", r.Instances)
	fmt.Printf("  populate                 %dms\n", r.PopulateMs)
	fmt.Printf("  RSS delta (populate)     %.0f MiB (%.0f → %.0f)\n", r.RSSDeltaMB, r.RSSBaselineMB, r.RSSPopulatedMB)
	fmt.Printf("  run peak RSS             %.0f MiB\n", r.RunPeakRSSMB)
	fmt.Printf("  metastore read           %s ops/sec  p50 %.1fµs  p99 %.1fµs\n", human(int(r.ReaderOpsSec)), r.ReaderP50us, r.ReaderP99us)
	fmt.Printf("  churn write              %s txn/sec\n", human(int(r.WriterTxnsSec)))
	fmt.Printf("  on disk                  %.1f MiB\n", float64(r.DiskBytes)/(1<<20))
	fmt.Printf("  backup                   %dms  %.1f MiB  (%d snapshot(s))\n", r.BackupMs, float64(r.BackupBytes)/(1<<20), r.Instances)
}

func ratio(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return round(a / b)
}

// printTopoVerdict renders the side-by-side and the computed conclusions.
func printTopoVerdict(shared, isolated topoReport) {
	fmt.Printf("\n## Topology verdict — shared (1 instance) vs isolated (%d instances)\n\n", isolated.Instances)
	fmt.Println("| metric | shared (1) | isolated (3) | shared ÷ isolated |")
	fmt.Println("|---|--:|--:|--:|")
	fmt.Printf("| RSS delta on populate (MiB) | %.0f | %.0f | ×%.2f |\n", shared.RSSDeltaMB, isolated.RSSDeltaMB, ratio(shared.RSSDeltaMB, isolated.RSSDeltaMB))
	fmt.Printf("| run peak RSS (MiB) | %.0f | %.0f | ×%.2f |\n", shared.RunPeakRSSMB, isolated.RunPeakRSSMB, ratio(shared.RunPeakRSSMB, isolated.RunPeakRSSMB))
	fmt.Printf("| metastore read p50 (µs) | %.1f | %.1f | ×%.2f |\n", shared.ReaderP50us, isolated.ReaderP50us, ratio(shared.ReaderP50us, isolated.ReaderP50us))
	fmt.Printf("| metastore read p99 (µs) | %.1f | %.1f | ×%.2f |\n", shared.ReaderP99us, isolated.ReaderP99us, ratio(shared.ReaderP99us, isolated.ReaderP99us))
	fmt.Printf("| metastore read ops/sec | %s | %s | ×%.2f |\n", human(int(shared.ReaderOpsSec)), human(int(isolated.ReaderOpsSec)), ratio(shared.ReaderOpsSec, isolated.ReaderOpsSec))
	fmt.Printf("| churn write txn/sec | %s | %s | ×%.2f |\n", human(int(shared.WriterTxnsSec)), human(int(isolated.WriterTxnsSec)), ratio(shared.WriterTxnsSec, isolated.WriterTxnsSec))
	fmt.Printf("| on disk (MiB) | %.1f | %.1f | ×%.2f |\n", float64(shared.DiskBytes)/(1<<20), float64(isolated.DiskBytes)/(1<<20), ratio(float64(shared.DiskBytes), float64(isolated.DiskBytes)))
	fmt.Printf("| backup | %dms · 1 consistent snapshot | %dms · %d snapshots, no cross-store point-in-time | — |\n", shared.BackupMs, isolated.BackupMs, isolated.Instances)

	rssSaved := isolated.RSSDeltaMB - shared.RSSDeltaMB
	peakSaved := isolated.RunPeakRSSMB - shared.RunPeakRSSMB
	fmt.Printf("\n**RAM saved by one instance:** %.0f MiB idle · %.0f MiB run-peak.  ", rssSaved, peakSaved)
	fmt.Printf("**Co-tenancy read penalty:** metastore p50 ×%.2f, p99 ×%.2f, throughput ×%.2f.  ",
		ratio(shared.ReaderP50us, isolated.ReaderP50us), ratio(shared.ReaderP99us, isolated.ReaderP99us), ratio(shared.ReaderOpsSec, isolated.ReaderOpsSec))
	fmt.Printf("**Backup:** one instance yields a single consistent recovery point; three do not.\n")
}

// runTopologyBench orchestrates the requested mode(s) and prints the reports (+ verdict + optional JSON).
func runTopologyBench(mode, baseDir, profile string, val []byte, metakeys, churnWS, readers, writers, churnRate int, dur time.Duration, sync bool, jsonOut string) {
	modes := []string{mode}
	if mode == "both" {
		modes = []string{"shared", "isolated"}
	}
	rateNote := "saturated"
	if churnRate > 0 {
		rateNote = fmt.Sprintf("%s txn/sec cap", human(churnRate))
	}
	fmt.Printf("topology bench: profile=%s metakeys=%s churnWS=%s readers=%d writers=%d churn=%s dur=%s sync=%v GOMAXPROCS=%d\n",
		profile, human(metakeys), human(churnWS), readers, writers, rateNote, dur, sync, gomaxprocs())
	if mode == "both" {
		fmt.Println("(note: RSS is process-global; the isolated run inherits the shared run's allocator floor — for the")
		fmt.Println(" cleanest RSS headline run `-topo shared` and `-topo isolated` as separate processes. Throughput/latency")
		fmt.Println(" and the RSS *delta* per mode are unaffected.)")
	}
	var reps []topoReport
	for _, m := range modes {
		fmt.Printf("\n=== topology: %s ===\n", m)
		r := runTopology(m, baseDir, profile, val, metakeys, churnWS, readers, writers, churnRate, dur, sync)
		r.print()
		reps = append(reps, r)
	}
	if len(reps) == 2 {
		printTopoVerdict(reps[0], reps[1])
	}
	if jsonOut != "" {
		b, err := json.MarshalIndent(reps, "", "  ")
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(jsonOut, append(b, '\n'), 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("\nJSON → %s\n", jsonOut)
	}
}
