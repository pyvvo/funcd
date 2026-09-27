package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// PyPoolComparison is the Python solo-vs-pooled head-to-head (ADR-0050) — the Python analog of the
// Node WritePoolReport comparison. K trivial handlers in one subinterpreter pool host (pool.py) vs
// one solo shim (shim.py), both hit DIRECTLY on their own port (no data plane), so the difference
// isolates the pool host. Process RSS, dev machine (ADR-0040 caveats apply).
type PyPoolComparison struct {
	K             int           `json:"k"`
	Python        string        `json:"python"`
	SoloMBPerFn   float64       `json:"soloMBPerFunction"`   // one solo shim's RSS = a function's marginal cost
	PooledMBPerFn float64       `json:"pooledMBPerFunction"` // pool host RSS / K
	DensityGain   float64       `json:"densityGain"`         // soloMBPerFn / pooledMBPerFn
	SoloRPS       float64       `json:"soloRPS"`
	SoloP99       time.Duration `json:"soloP99"`
	PooledRPS     float64       `json:"pooledRPS"`
	PooledP99     time.Duration `json:"pooledP99"`
}

// pythonVersion returns the interpreter's "X.Y.Z" version (path-free, safe to write into a report);
// "3.14+" if it can't be read.
func pythonVersion(python string) string {
	out, err := exec.Command(python, "-c", "import sys;print('.'.join(map(str, sys.version_info[:3])))").Output()
	if err != nil {
		return "3.14+"
	}
	return strings.TrimSpace(string(out))
}

// runLoadSpread drives one worker per URL concurrently (the realistic pooled workload: many
// handlers, ~1 in-flight each) and returns the AVERAGE per-handler req/s with the worst-case p99.
func runLoadSpread(ctx context.Context, urls []string, body string, dur time.Duration) loadResult {
	results := make([]loadResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			results[i] = runLoad(ctx, u, body, 1, dur, nil)
		}(i, u)
	}
	wg.Wait()
	out := loadResult{}
	for _, r := range results {
		out.rps += r.rps
		if r.latency.P99 > out.latency.P99 {
			out.latency = r.latency
		}
	}
	if len(urls) > 0 {
		out.rps /= float64(len(urls)) // mean per-handler throughput
	}
	return out
}

// startPyShim launches `python entry` with env, waits for it to write its port to portFile (the
// ADR-0030 handshake), and returns the running command + the port. The caller kills it when done.
func startPyShim(ctx context.Context, python, entry string, env []string, portFile string) (*exec.Cmd, string, error) {
	const op = "bench.startPyShim"
	cmd := exec.CommandContext(ctx, python, entry) //nolint:gosec // entry is platform-internal
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return nil, "", fault.Wrapf(err, fault.Internal, op, "start py shim")
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(portFile); rerr == nil && len(b) > 0 {
			return cmd, strings.TrimSpace(string(b)), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil, "", fault.Unavailablef(op, "py shim %q did not become ready", filepath.Base(entry))
}

// MeasurePythonPool runs the Python solo-vs-pooled comparison (ADR-0050). It launches the
// subinterpreter pool host (poolEntry) with k identical handlers, samples its RSS (→ MB/function,
// the density), and drives load at one handler; then — for a fair baseline — it launches ONE solo
// shim (shimEntry) hosting the same handler and drives load on the SAME direct path. The two run
// sequentially so neither contends for CPU. The python interpreter must be ≥3.14 (the pool host).
func MeasurePythonPool(ctx context.Context, python, soloEntry, poolHostEntry string, k, concurrency int, duration time.Duration) (PyPoolComparison, error) {
	const op = "bench.MeasurePythonPool"
	if k < 1 {
		k = 1
	}
	dir, err := os.MkdirTemp("", "funcd-pypool-*")
	if err != nil {
		return PyPoolComparison{}, fault.Wrapf(err, fault.Internal, op, "temp dir")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	const handlerSrc = "def handle(context, event):\n    return {'ok': True, 'data': event.get('data')}\n"
	manifest := make([]poolEntry, k)
	for i := range k {
		art := filepath.Join(dir, "h"+strconv.Itoa(i)+".py")
		if werr := os.WriteFile(art, []byte(handlerSrc), 0o600); werr != nil {
			return PyPoolComparison{}, fault.Wrapf(werr, fault.Internal, op, "write handler")
		}
		manifest[i] = poolEntry{Name: "f" + strconv.Itoa(i), Artifact: art, Handler: "handle"}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return PyPoolComparison{}, fault.Wrapf(err, fault.Internal, op, "marshal manifest")
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if werr := os.WriteFile(manifestPath, data, 0o600); werr != nil {
		return PyPoolComparison{}, fault.Wrapf(werr, fault.Internal, op, "write manifest")
	}

	// (1) pool: K handlers in one subinterpreter host — sample RSS, then load one handler.
	poolPortFile := filepath.Join(dir, "pool.port")
	poolCmd, poolPort, err := startPyShim(ctx, python, poolHostEntry,
		[]string{"FUNCD_POOL_MANIFEST=" + manifestPath, "FUNCD_PORTFILE=" + poolPortFile}, poolPortFile)
	if err != nil {
		return PyPoolComparison{}, err
	}
	time.Sleep(500 * time.Millisecond) // let the K subinterpreters settle before sampling RSS
	rss := rssMB(poolCmd.Process.Pid)
	// Spread the load ACROSS the K handlers (one in-flight each) — the realistic pool workload (many
	// low-QPS handlers) that exercises the per-interpreter GIL, NOT 8-deep on one handler (which would
	// funnel through a single max_workers=1 worker and just measure dispatch contention).
	urls := make([]string, k)
	for i := range k {
		urls[i] = "http://127.0.0.1:" + poolPort + "/function/f" + strconv.Itoa(i)
	}
	runLoadSpread(ctx, urls, `{}`, 500*time.Millisecond) // warm the worker interpreters + the hot path
	poolLoad := runLoadSpread(ctx, urls, `{}`, duration)
	stopShim(poolCmd) // kill before the baseline so they never contend for CPU
	if rss == 0 {
		return PyPoolComparison{}, fault.Unavailablef(op, "could not sample pool RSS")
	}

	// (2) baseline: ONE solo shim hosting the same handler, hit on the SAME direct path.
	soloPortFile := filepath.Join(dir, "solo.port")
	soloCmd, soloPort, err := startPyShim(ctx, python, soloEntry,
		[]string{"FUNCD_ARTIFACT=" + manifest[0].Artifact, "FUNCD_HANDLER=handle", "FUNCD_PORTFILE=" + soloPortFile}, soloPortFile)
	if err != nil {
		return PyPoolComparison{}, err
	}
	soloRSS := rssMB(soloCmd.Process.Pid)
	soloURL := "http://127.0.0.1:" + soloPort + "/"
	// 1 in-flight — the same per-function concurrency a pooled handler sees in runLoadSpread, so the
	// solo (one fn) and the pooled avg/handler are a fair per-function comparison (the bursty target).
	runLoad(ctx, soloURL, `{}`, 1, 500*time.Millisecond, nil) // warm
	soloLoad := runLoad(ctx, soloURL, `{}`, 1, duration, nil)
	stopShim(soloCmd)

	cmp := PyPoolComparison{
		K: k, Python: pythonVersion(python), // the VERSION, never the (local) interpreter path
		SoloMBPerFn: soloRSS, PooledMBPerFn: rss / float64(k),
		SoloRPS: soloLoad.rps, SoloP99: soloLoad.latency.P99,
		PooledRPS: poolLoad.rps, PooledP99: poolLoad.latency.P99,
	}
	if cmp.PooledMBPerFn > 0 {
		cmp.DensityGain = cmp.SoloMBPerFn / cmp.PooledMBPerFn
	}
	return cmp, nil
}

// WritePyPoolReport writes the Python pool comparison (ADR-0050) to dir as py-pool-report.{md,json}.
func WritePyPoolReport(dir string, c PyPoolComparison) (mdPath, jsonPath string, err error) {
	const op = "bench.WritePyPoolReport"
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "mkdir report dir")
	}
	jsonPath = filepath.Join(dir, "py-pool-report.json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "marshal json")
	}
	if werr := os.WriteFile(jsonPath, append(data, '\n'), 0o600); werr != nil {
		return "", "", fault.Wrapf(werr, fault.Internal, op, "write json")
	}
	mdPath = filepath.Join(dir, "py-pool-report.md")
	if werr := os.WriteFile(mdPath, []byte(renderPyPoolMarkdown(c)), 0o600); werr != nil {
		return "", "", fault.Wrapf(werr, fault.Internal, op, "write markdown")
	}
	return mdPath, jsonPath, nil
}

func renderPyPoolMarkdown(c PyPoolComparison) string {
	var b strings.Builder
	b.WriteString("# funcd Python worker-pool comparison (ADR-0050)\n\n")
	fmt.Fprintf(&b, "K=%d handlers in one subinterpreter pool host (`pool.py`) vs a solo `shim.py` — both hit directly "+
		"(no data plane), HTTP/1.1 keep-alive. Process RSS, dev box; Python %s.\n\n", c.K, c.Python)

	fmt.Fprintf(&b, "| metric | single fn | pooled (×%d) |\n|---|---|---|\n", c.K)
	fmt.Fprintf(&b, "| memory MB / function | %.1f | %.1f |\n", c.SoloMBPerFn, c.PooledMBPerFn)
	fmt.Fprintf(&b, "| density (functions / GB) | 1× | %.1f× |\n", c.DensityGain)
	fmt.Fprintf(&b, "| throughput (req/s, ~1 in-flight/fn) | %.0f | %.0f *(avg/handler)* |\n", c.SoloRPS, c.PooledRPS)
	fmt.Fprintf(&b, "| latency p99 | %s | %s |\n\n", c.SoloP99.Round(time.Microsecond), c.PooledP99.Round(time.Microsecond))

	fmt.Fprintf(&b, "~%.1f× density *with* per-interpreter isolation + per-GIL CPU parallelism; needs Python ≥3.14. "+
		"Pool when memory is the binding constraint.\n", c.DensityGain)
	return b.String()
}
