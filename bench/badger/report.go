package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// Result is one scenario's measurement: throughput plus the RSS the op cost (before/after/peak).
type Result struct {
	Name        string  `json:"name"`
	Ops         int     `json:"ops"`
	Millis      int64   `json:"millis"`
	OpsPerSec   float64 `json:"opsPerSec"`
	RSSBeforeMB float64 `json:"rssBeforeMB"`
	RSSAfterMB  float64 `json:"rssAfterMB"`
	RSSPeakMB   float64 `json:"rssPeakMB"`
	GoHeapMB    float64 `json:"goHeapMB"`
	Note        string  `json:"note,omitempty"`
}

// Report is the whole run: the config it ran under, every scenario, and the on-disk + peak summary.
type Report struct {
	Profile     string   `json:"profile"`
	Keys        int      `json:"keys"`
	Funcs       int      `json:"funcs"`
	ValueBytes  int      `json:"valueBytes"`
	SyncWrites  bool     `json:"syncWrites"`
	GOMAXPROCS  int      `json:"gomaxprocs"`
	LSMBytes    int64    `json:"lsmBytes"`
	VlogBytes   int64    `json:"vlogBytes"`
	DirBytes    int64    `json:"dirBytes"`
	RunPeakRSS  float64  `json:"runPeakRssMB"`
	FinalRSSMB  float64  `json:"finalRssMB"`
	Results     []Result `json:"results"`
}

func round(f float64) float64 { return math.Round(f*100) / 100 }

func mib(b int64) float64 { return round(float64(b) / (1 << 20)) }

// printTable renders the run as a GitHub-flavored markdown table (the human view).
func (rep Report) printTable() {
	fmt.Printf("\n## Badger bench — profile=%s  keys=%d  funcs=%d  value=%dB  sync=%v  GOMAXPROCS=%d\n\n",
		rep.Profile, rep.Keys, rep.Funcs, rep.ValueBytes, rep.SyncWrites, rep.GOMAXPROCS)
	fmt.Println("| scenario | ops | ms | ops/sec | RSS before | RSS after | RSS Δ | RSS peak | Go heap | note |")
	fmt.Println("|---|--:|--:|--:|--:|--:|--:|--:|--:|---|")
	for _, r := range rep.Results {
		fmt.Printf("| %s | %s | %d | %s | %.0f | %.0f | %+.0f | %.0f | %.0f | %s |\n",
			r.Name, human(r.Ops), r.Millis, human(int(r.OpsPerSec)),
			r.RSSBeforeMB, r.RSSAfterMB, r.RSSAfterMB-r.RSSBeforeMB, r.RSSPeakMB, r.GoHeapMB, r.Note)
	}
	fmt.Printf("\n**On disk:** LSM %.1f MiB · vlog %.1f MiB · dir total %.1f MiB  ",
		mib(rep.LSMBytes), mib(rep.VlogBytes), mib(rep.DirBytes))
	fmt.Printf("**Run peak RSS:** %.0f MiB · **final RSS:** %.0f MiB\n", rep.RunPeakRSS, rep.FinalRSSMB)
}

// human formats a count with thousands separators (5_000_000 → 5,000,000).
func human(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// writeJSON persists the full report for diffing across profiles/runs.
func (rep Report) writeJSON(path string) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
