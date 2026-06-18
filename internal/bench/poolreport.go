package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// PoolComparison is the head-to-head the separate pool report renders (ADR-0044). Two axes, NOT of
// equal reliability:
//   - density (the primary, stable result) — K handlers in ONE worker_threads pool (pool RSS / K)
//     vs K separate single-tenant shim processes (the marginal RSS of one more).
//   - throughput/latency (indicative only) — the same handler hit DIRECTLY on each shim's own port
//     (no gateway/activator/data plane). The pooled figure is stable run-to-run; the single-tenant
//     baseline is ONE short-lived single-process shim and swings widely (≈15–65k req/s on a dev box)
//     — single-threaded throughput is far more sensitive to JIT/core/GC timing than the pool's
//     long-lived worker threads. So the pair shows throughput is COMPARABLE within that noise, not a
//     precise ratio, and it is NOT a clean "worker_threads hop" isolation (the pool also gains a
//     2-core HTTP/handler pipeline). Read density as the decisive number.
type PoolComparison struct {
	K         int     `json:"k"`
	Substrate Backend `json:"substrate"`
	// density (stable, primary)
	PerFunctionMB float64 `json:"perFunctionMB"`       // marginal RSS of one more separate shim
	PooledMBPerFn float64 `json:"pooledMBPerFunction"` // pool RSS / K
	DensityGain   float64 `json:"densityGain"`         // perFunctionMB / pooledMBPerFn
	// throughput (indicative) — both measured DIRECTLY on the shim port (no data plane)
	SingleDirectRPS    float64       `json:"singleDirectRPS"`    // one single-tenant shim, direct — HIGH VARIANCE
	SingleDirectP99    time.Duration `json:"singleDirectP99"`    //
	PooledRPS          float64       `json:"pooledRPS"`          // one handler in the pool, direct (stable)
	PooledP99          time.Duration `json:"pooledP99"`          //
	ThroughputRetained float64       `json:"throughputRetained"` // pooledRPS / singleDirectRPS — indicative only
}

// poolComparison builds the head-to-head from the report that carries the pooled numbers (the
// pooled measurement is substrate-independent, so any report with PooledK>0 works).
func poolComparison(reports []Report) (PoolComparison, bool) {
	for _, r := range reports {
		if r.PooledK == 0 || r.PooledPerFunctionMB == 0 {
			continue
		}
		c := PoolComparison{
			K:               r.PooledK,
			Substrate:       r.Backend,
			PerFunctionMB:   r.PerFunctionMB,
			PooledMBPerFn:   r.PooledPerFunctionMB,
			SingleDirectRPS: r.SingleDirectRPS,
			SingleDirectP99: r.SingleDirectLatency.P99,
			PooledRPS:       r.PooledRPS,
			PooledP99:       r.PooledLatency.P99,
		}
		if c.PooledMBPerFn > 0 {
			c.DensityGain = c.PerFunctionMB / c.PooledMBPerFn
		}
		if c.SingleDirectRPS > 0 {
			c.ThroughputRetained = c.PooledRPS / c.SingleDirectRPS
		}
		return c, true
	}
	return PoolComparison{}, false
}

// WritePoolReport writes the worker-pool comparison (ADR-0044) to dir as pool-report.{md,json},
// returning their paths. Returns ("", "", nil) — no error, no files — when no report carries
// pooled numbers (pooled measurement skipped or failed). Separate from the substrate report so
// the density-vs-latency trade reads on its own.
func WritePoolReport(dir string, reports []Report) (mdPath, jsonPath string, err error) {
	const op = "bench.WritePoolReport"
	c, ok := poolComparison(reports)
	if !ok {
		return "", "", nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "mkdir report dir")
	}
	jsonPath = filepath.Join(dir, "pool-report.json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "marshal json")
	}
	if err := os.WriteFile(jsonPath, append(data, '\n'), 0o600); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "write json")
	}
	mdPath = filepath.Join(dir, "pool-report.md")
	if err := os.WriteFile(mdPath, []byte(renderPoolMarkdown(c)), 0o600); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "write markdown")
	}
	return mdPath, jsonPath, nil
}

func renderPoolMarkdown(c PoolComparison) string {
	var b strings.Builder
	b.WriteString("# funcd Node worker-pool comparison (ADR-0044)\n\n")
	fmt.Fprintf(&b, "K=%d handlers in one `worker_threads` pool vs a single-tenant shim — both hit directly on the shim "+
		"port (no data plane), HTTP/1.1 keep-alive. Process RSS, dev box (ADR-0040 caveats).\n\n", c.K)

	fmt.Fprintf(&b, "| metric | single fn | pooled (×%d) |\n|---|---|---|\n", c.K)
	fmt.Fprintf(&b, "| memory MB / function | %.1f | %.1f |\n", c.PerFunctionMB, c.PooledMBPerFn)
	fmt.Fprintf(&b, "| density (functions / GB) | 1× | %.1f× |\n", c.DensityGain)
	fmt.Fprintf(&b, "| throughput (req/s, ~1 in-flight/fn) | %.0f | %.0f *(avg/handler)* |\n", c.SingleDirectRPS, c.PooledRPS)
	fmt.Fprintf(&b, "| latency p99 | %s | %s |\n\n", c.SingleDirectP99.Round(time.Microsecond), c.PooledP99.Round(time.Microsecond))

	fmt.Fprintf(&b, "~%.1f× density (the win). Per-handler throughput is lower than a dedicated shim (the pool's "+
		"shared dispatch event loop) but ample for bursty low-QPS agents. Same-namespace only.\n", c.DensityGain)
	return b.String()
}
