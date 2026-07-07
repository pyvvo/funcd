package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// processRSSMB returns the OS-reported resident set size in MiB. Linux reads /proc/self/status (VmRSS);
// macOS falls back to `ps -o rss=` (KiB). RSS — not Go heap — is the number that decides whether Badger
// fits funcd's RAM budget: it counts mmap'd SSTables, the value log, and caches, which the Go heap omits.
func processRSSMB() float64 {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				if f := strings.Fields(line); len(f) >= 2 {
					if kb, err := strconv.ParseFloat(f[1], 64); err == nil {
						return kb / 1024
					}
				}
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err == nil {
		if kb, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			return kb / 1024
		}
	}
	return 0
}

// goHeapMB forces a GC and returns the live Go heap in MiB — the allocator's share of RSS, useful to tell
// "Badger's mmap/cache" RSS apart from "our own driver allocations".
func goHeapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapInuse) / (1 << 20)
}

// peakSampler polls RSS on a ticker and records the maximum — so a scenario's transient spike (compaction,
// a full scan materializing) is captured, not just the quiescent before/after.
type peakSampler struct {
	peakMilliMB int64 // atomic: MiB × 1000, so sub-MiB resolution without floats in the CAS loop
	stop        chan struct{}
	wg          sync.WaitGroup
}

func startPeakSampler() *peakSampler {
	p := &peakSampler{stop: make(chan struct{})}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				cur := int64(processRSSMB() * 1000)
				for {
					old := atomic.LoadInt64(&p.peakMilliMB)
					if cur <= old || atomic.CompareAndSwapInt64(&p.peakMilliMB, old, cur) {
						break
					}
				}
			}
		}
	}()
	return p
}

func (p *peakSampler) peakMB() float64 { return float64(atomic.LoadInt64(&p.peakMilliMB)) / 1000 }

func (p *peakSampler) stopAndPeakMB() float64 {
	close(p.stop)
	p.wg.Wait()
	return p.peakMB()
}
