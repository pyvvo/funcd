package bench

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// poolEntry is one manifest row the pooled shim (ADR-0044) hosts.
type poolEntry struct {
	Name     string `json:"name"`
	Artifact string `json:"artifact"`
	Handler  string `json:"handler"`
}

// poolMeasurement is the worker-pool comparison's raw numbers (ADR-0044). Both the pool handler and
// the single-tenant baseline are hit DIRECTLY on the shim's own port (no gateway/activator/data
// plane in between) and both are warmed before measuring (see runLoad), so SingleRPS and PoolRPS
// are steady-state. They are NOT a clean "worker_threads hop" isolation, though: the pool's main
// thread parses HTTP while the handler runs on its worker thread (a 2-core pipeline), whereas the
// single shim does both on one event loop — so the pair captures the whole pooled-vs-solo runtime
// difference, not the hop alone. The density number (MBPerFn) is the comparison's primary result.
type poolMeasurement struct {
	K             int
	MBPerFn       float64 // pool process RSS / K — the density (the primary result)
	PoolRPS       float64 // AVG per-handler throughput, ~1 in-flight each (spread across the K handlers)
	PoolLatency   Latency
	SingleRPS     float64 // one single-tenant shim at the SAME 1 in-flight — the fair per-function baseline
	SingleLatency Latency
}

// startNodeShim launches `node shimPath` with env, waits for it to write its bound port to
// portFile (the ADR-0030 handshake), and returns the running command + the port. The caller kills
// the command when done.
func startNodeShim(ctx context.Context, shimPath string, env []string, portFile string) (*exec.Cmd, string, error) {
	const op = "bench.startNodeShim"
	cmd := exec.CommandContext(ctx, "node", shimPath) //nolint:gosec // shimPath is platform-internal
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return nil, "", fault.Wrapf(err, fault.Internal, op, "start shim")
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
	return nil, "", fault.Unavailablef(op, "shim %q did not become ready", filepath.Base(shimPath))
}

// waitPoolMembers waits until the pool host on port reports ready, then fails when a member failed to
// load: RSS sampled over a pool missing a member understates the per-function cost.
func waitPoolMembers(ctx context.Context, port string) error {
	const op = "bench.waitPoolMembers"
	base := "http://127.0.0.1:" + port + "/health/"
	client := httpx.NodeClient(5 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if status, _, err := httpGet(ctx, client, base+"readiness"); err == nil && status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			return fault.Unavailablef(op, "the pool host did not report ready")
		}
		select {
		case <-ctx.Done():
			return fault.Wrapf(ctx.Err(), fault.Unavailable, op, "wait for the pool host")
		case <-tick.C:
		}
	}
	status, body, err := httpGet(ctx, client, base+"members")
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "read the pool members")
	}
	if status != http.StatusOK {
		return fault.Unavailablef(op, "the pool members answered %d", status)
	}
	var members []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &members); err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "decode the pool members")
	}
	for _, m := range members {
		if m.State == "failed" {
			return fault.Unavailablef(op, "pool member %q failed to load: %s", m.Name, m.Error)
		}
	}
	return nil
}

func httpGet(ctx context.Context, client *http.Client, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer httpx.CloseBody(resp.Body)
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

func stopShim(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// measurePool runs the worker-pool comparison (ADR-0044). It launches the pooled shim (pool.mjs)
// with k identical handlers, samples its RSS (→ MB/function, the density), and drives load spread
// across the handlers (~1 in-flight each — the bursty target) for the AVERAGE per-handler
// throughput. Then — for a fair per-function baseline — it launches ONE single-tenant shim hosting
// the same handler and drives it at the SAME 1 in-flight (the shim's own port, no data plane). The
// two run sequentially so neither contends for CPU. Density (MB/function) is the primary result.
func measurePool(ctx context.Context, shimPath, poolShimPath string, k, concurrency int, duration time.Duration) (poolMeasurement, error) {
	const op = "bench.measurePool"
	if k < 1 {
		k = 1
	}
	dir, err := os.MkdirTemp("", "funcd-pool-bench-*")
	if err != nil {
		return poolMeasurement{}, fault.Wrapf(err, fault.Internal, op, "temp dir")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	const handlerSrc = "export function handle(_, e) { return { ok: true, data: e.data }; }\n"
	manifest := make([]poolEntry, k)
	for i := range k {
		art := filepath.Join(dir, "h"+strconv.Itoa(i)+".mjs")
		if werr := os.WriteFile(art, []byte(handlerSrc), 0o600); werr != nil {
			return poolMeasurement{}, fault.Wrapf(werr, fault.Internal, op, "write handler")
		}
		manifest[i] = poolEntry{Name: "f" + strconv.Itoa(i), Artifact: art, Handler: "handle"}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return poolMeasurement{}, fault.Wrapf(err, fault.Internal, op, "marshal manifest")
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if werr := os.WriteFile(manifestPath, data, 0o600); werr != nil {
		return poolMeasurement{}, fault.Wrapf(werr, fault.Internal, op, "write manifest")
	}

	// (1) pool: K handlers in one worker_threads process — sample RSS, then load one handler.
	poolPortFile := filepath.Join(dir, "pool.port")
	poolCmd, poolPort, err := startNodeShim(ctx, poolShimPath,
		[]string{"FUNCD_POOL_MANIFEST=" + manifestPath, "FUNCD_PORTFILE=" + poolPortFile}, poolPortFile)
	if err != nil {
		return poolMeasurement{}, err
	}
	if err := waitPoolMembers(ctx, poolPort); err != nil {
		stopShim(poolCmd)
		return poolMeasurement{}, err
	}
	time.Sleep(250 * time.Millisecond) // let the worker heaps settle before sampling
	rss := rssMB(poolCmd.Process.Pid)
	// Spread the load across the K handlers (~1 in-flight each — the bursty target) and report the
	// AVERAGE per-handler throughput, so it's a fair per-function comparison with the single shim.
	urls := make([]string, k)
	for i := range k {
		urls[i] = "http://127.0.0.1:" + poolPort + "/function/f" + strconv.Itoa(i)
	}
	runLoadSpread(ctx, urls, `{}`, 500*time.Millisecond) // warm
	poolLoad := runLoadSpread(ctx, urls, `{}`, duration) // avg per handler
	stopShim(poolCmd)                                    // kill before the baseline so they never contend for CPU
	if rss == 0 {
		return poolMeasurement{}, fault.Unavailablef(op, "could not sample pool RSS")
	}

	// (2) baseline: ONE single-tenant shim hosting the same handler, hit at the SAME 1 in-flight.
	singlePortFile := filepath.Join(dir, "single.port")
	singleCmd, singlePort, err := startNodeShim(ctx, shimPath,
		[]string{"FUNCD_ARTIFACT=" + manifest[0].Artifact, "FUNCD_HANDLER=handle", "FUNCD_PORTFILE=" + singlePortFile}, singlePortFile)
	if err != nil {
		return poolMeasurement{}, err
	}
	singleURL := "http://127.0.0.1:" + singlePort + "/"
	runLoad(ctx, singleURL, `{}`, 1, 500*time.Millisecond, nil) // warm
	singleLoad := runLoad(ctx, singleURL, `{}`, 1, duration, nil)
	stopShim(singleCmd)

	return poolMeasurement{
		K: k, MBPerFn: rss / float64(k),
		PoolRPS: poolLoad.rps, PoolLatency: poolLoad.latency,
		SingleRPS: singleLoad.rps, SingleLatency: singleLoad.latency,
	}, nil
}
