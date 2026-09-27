package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// WriteReport writes the bench reports to dir as both machine-readable JSON (for a future CI
// gate) and a human-readable markdown sustainability report, returning their paths.
func WriteReport(dir string, reports []Report) (mdPath, jsonPath string, err error) {
	const op = "bench.WriteReport"
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "mkdir report dir")
	}
	jsonPath = filepath.Join(dir, "report.json")
	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "marshal json")
	}
	if err := os.WriteFile(jsonPath, append(data, '\n'), 0o600); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "write json")
	}
	mdPath = filepath.Join(dir, "report.md")
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(reports)), 0o600); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "write markdown")
	}
	return mdPath, jsonPath, nil
}

// renderMarkdown formats the reports as a sustainability report a human reads.
func renderMarkdown(reports []Report) string {
	var b strings.Builder
	b.WriteString("# funcd sustainability report (ADR-0040)\n\n")
	b.WriteString("> **What this is:** the *sustainability* verdict — can the box run ~100 agents? — per **substrate** " +
		"(`memory` vs `file`). The single-vs-pooled density/throughput comparisons live in `pool-report.md` (Node) and " +
		"`py-pool-report.md` (Python).\n\n")
	b.WriteString("> Per-worker memory is **process RSS**, not container cgroup memory — a lower-bound proxy, " +
		"so the fits-target verdict is **optimistic** (ADR-0040). The store is memory in both substrates.\n\n")

	b.WriteString("| metric | " + strings.Join(backendCols(reports), " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(reports)+1) + "\n")
	row := func(label string, f func(Report) string) {
		cells := make([]string, len(reports))
		for i, r := range reports {
			cells[i] = f(r)
		}
		fmt.Fprintf(&b, "| %s | %s |\n", label, strings.Join(cells, " | "))
	}
	row("throughput (req/s)", func(r Report) string { return fmt.Sprintf("%.0f", r.RPS) })
	row("latency p50", func(r Report) string { return r.Latency.P50.String() })
	row("latency p99", func(r Report) string { return r.Latency.P99.String() })
	row("latency p99.9", func(r Report) string { return r.Latency.P999.String() })
	row("latency max", func(r Report) string { return r.Latency.Max.String() })
	row("platform baseline (MB)", func(r Report) string { return fmt.Sprintf("%.1f", r.PlatformBaselineMB) })
	row("per-worker RSS (MB)", func(r Report) string { return fmt.Sprintf("%.1f", r.PerWorkerMB) })
	row("idle RSS (MB)", func(r Report) string { return fmt.Sprintf("%.1f", r.IdleMB) })
	row("cold-start (wake)", func(r Report) string { return r.ColdStart.Round(time.Millisecond).String() })
	row("marginal MB/function", func(r Report) string { return fmt.Sprintf("%.1f", r.PerFunctionMB) })
	row("pooled MB/function (worker_threads)", func(r Report) string {
		if r.PooledPerFunctionMB == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f", r.PooledPerFunctionMB)
	})
	row("pool density gain (×)", func(r Report) string {
		if r.PooledPerFunctionMB == 0 || r.PerFunctionMB == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f×", r.PerFunctionMB/r.PooledPerFunctionMB)
	})
	row("max density (fns)", func(r Report) string { return fmt.Sprintf("%d", r.MaxDensity) })
	row("fits target?", func(r Report) string {
		if r.FitsTarget {
			return "✅ yes"
		}
		return "❌ no"
	})
	return b.String()
}

func backendCols(reports []Report) []string {
	cols := make([]string, len(reports))
	for i, r := range reports {
		cols[i] = string(r.Backend)
	}
	return cols
}
