package bench

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// Backend selects the durable substrate compared by the bench. The store is memory in both
// (the file store = badger (ADR-0065), out of scope, ADR-0040); only the blob + JetStream bus move.
type Backend string

const (
	BackendMemory Backend = "memory" // gocloud mem:// blob + NATS MemoryStorage (InMemory())
	BackendFile   Backend = "file"   // gocloud file:// blob + NATS FileStorage
)

// Config parameterizes a bench run.
type Config struct {
	Backends    []Backend     // which substrates to run (default: memory, file)
	Concurrency int           // load workers
	Duration    time.Duration // load phase per backend
	Density     int           // extra warm functions deployed for the density sweep
	MemBudgetMB int           // function-memory budget for the verdict (e.g. 16384)
	TargetFns   int           // agents to size for (e.g. 100)
}

// Report is one substrate's sustainability numbers.
type Report struct {
	Backend             Backend       `json:"backend"`
	RPS                 float64       `json:"rps"`
	Latency             Latency       `json:"latency"`
	PlatformBaselineMB  float64       `json:"platformBaselineMB"`
	PerWorkerMB         float64       `json:"perWorkerMB"`
	IdleMB              float64       `json:"idleMB"`              // shim RSS while the only fn is scaled-to-zero (≈0 ⇒ reclaim works)
	ColdStart           time.Duration `json:"coldStart"`           // wake → first response (timed only after 0 replicas)
	PerFunctionMB       float64       `json:"perFunctionMB"`       // marginal slope: (RSS_at_K − perWorker) / K
	MaxDensity          int           `json:"maxDensity"`          // functions within MemBudgetMB
	FitsTarget          bool          `json:"fitsTarget"`          // TargetFns within MemBudgetMB? (optimistic — process RSS < cgroup)
	PooledPerFunctionMB float64       `json:"pooledPerFunctionMB"` // same K handlers in ONE worker_threads pool / K (ADR-0044); 0 if not measured
	PooledRPS           float64       `json:"pooledRPS"`           // throughput at one handler IN the pool, hit DIRECTLY (the worker_threads path); 0 if not measured
	PooledLatency       Latency       `json:"pooledLatency"`       // tail latency at one pooled handler, direct — the structured-clone cost
	SingleDirectRPS     float64       `json:"singleDirectRPS"`     // throughput of ONE single-tenant shim on the SAME direct path — the fair baseline for the pool
	SingleDirectLatency Latency       `json:"singleDirectLatency"` // tail latency of the single-tenant shim, same direct path
	PooledK             int           `json:"pooledK"`             // pool size the pooled numbers were measured at
}

// Run boots funcd once per Backend, drives the scenarios, and returns a Report per substrate.
// shimPath is the single-tenant node shim; poolShimPath is the pooled worker_threads shim
// (ADR-0044) — if non-empty, Run also measures pooled density for comparison. Node-gated.
func Run(ctx context.Context, shimPath, poolShimPath string, cfg Config) ([]Report, error) {
	const op = "bench.Run"
	bundle := filepath.Join(os.TempDir(), "funcd-bench-handler.mjs")
	if err := os.WriteFile(bundle, []byte("export function handle() { return { ok: true }; }\n"), 0o600); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "write handler bundle")
	}
	defer func() { _ = os.Remove(bundle) }()

	reports := make([]Report, 0, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		r, err := runBackend(ctx, shimPath, bundle, backend, cfg)
		if err != nil {
			// A bench tool reports what it can: one slow/flaky substrate (e.g. reconcile
			// starved by ephemeral-port pressure under load) must not lose the whole run.
			slog.Warn("bench: substrate failed; skipping", "backend", backend, "error", err)
			continue
		}
		reports = append(reports, r)
	}
	if len(reports) == 0 {
		return nil, fault.Unavailablef(op, "all %d substrate(s) failed", len(cfg.Backends))
	}

	// Pooled measurement (ADR-0044): K handlers in one worker_threads pool vs K per-function shims —
	// both its density (MB/function) AND its throughput/tail-latency under load (the worker_threads
	// message-passing cost). Substrate-independent (it's the shim process), so measured once at one
	// pool size + attached to every report.
	if poolShimPath != "" {
		k := max(cfg.Density, 4)
		if pm, perr := measurePool(ctx, shimPath, poolShimPath, k, cfg.Concurrency, cfg.Duration); perr != nil {
			slog.Warn("bench: pooled measurement failed; skipping", "error", perr)
		} else {
			for i := range reports {
				reports[i].PooledPerFunctionMB = pm.MBPerFn
				reports[i].PooledRPS = pm.PoolRPS
				reports[i].PooledLatency = pm.PoolLatency
				reports[i].SingleDirectRPS = pm.SingleRPS
				reports[i].SingleDirectLatency = pm.SingleLatency
				reports[i].PooledK = pm.K
			}
		}
	}
	return reports, nil
}

func runBackend(ctx context.Context, shimPath, bundle string, backend Backend, cfg Config) (Report, error) {
	const op = "bench.runBackend"
	tmp, err := os.MkdirTemp("", "funcd-bench-*")
	if err != nil {
		return Report{}, fault.Wrapf(err, fault.Internal, op, "temp dir")
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	opts := []funcd.Option{
		funcd.InMemory(),
		funcd.WithRuntimeShim("node", shimPath),
		funcd.WithArtifactStore(filepath.Join(tmp, "artifacts")),
	}
	if backend == BackendFile {
		blobDir, natsDir := filepath.Join(tmp, "blob"), filepath.Join(tmp, "nats")
		if err := os.MkdirAll(blobDir, 0o700); err != nil {
			return Report{}, fault.Wrapf(err, fault.Internal, op, "blob dir")
		}
		if err := os.MkdirAll(natsDir, 0o700); err != nil {
			return Report{}, fault.Wrapf(err, fault.Internal, op, "nats dir")
		}
		bucket, err := gocloud.Open(ctx, "file://"+blobDir)
		if err != nil {
			return Report{}, fault.Wrapf(err, fault.Internal, op, "open file blob")
		}
		messaging, err := nats.Open(ctx, nats.Options{Storage: nats.FileStorage, StoreDir: natsDir})
		if err != nil {
			return Report{}, fault.Wrapf(err, fault.Internal, op, "open file bus")
		}
		opts = append(opts, funcd.WithBlob(bucket), funcd.WithBus(messaging))
	}

	p, err := funcd.New(opts...)
	if err != nil {
		return Report{}, fault.Wrapf(err, fault.Internal, op, "new platform")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = p.Run(runCtx); close(done) }()
	// drain on exit: cancel + wait for Run to return so this backend's shim processes are
	// killed before the next backend samples RSS (no cross-backend memory contamination).
	defer func() { cancel(); <-done }()
	if err := waitListening(p.Addr(), 10*time.Second); err != nil {
		return Report{}, err
	}

	report := Report{Backend: backend, PlatformBaselineMB: rssMB(os.Getpid())}

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	if err != nil {
		return Report{}, fault.Wrapf(err, fault.Internal, op, "sdk client")
	}
	ref := "oci-layout://" + filepath.Join(tmp, "layout") + ":v1"
	if _, err := artifact.Push(ctx, ref, bundle, nil); err != nil {
		return Report{}, fault.Wrapf(err, fault.Internal, op, "push artifact")
	}
	dataPlane := "http://" + p.DataPlaneAddr()

	// "z" is the only function until the density sweep — so every memory sample is clean.
	// (1) idle + cold-start: deploy scale-to-zero, settle Idle, then time a cold (wake) request.
	if err := deploy(ctx, c, "default", "z", ref, 0, 0, 200*time.Millisecond); err != nil {
		return Report{}, err
	}
	if err := waitPhase(ctx, c, "default", "z", v1.PhaseIdle, 30*time.Second); err != nil {
		return Report{}, err
	}
	report.IdleMB = shimRSSMB(shimPath) // ≈0: the only fn is scaled-to-zero ⇒ reclaim freed it
	report.ColdStart = timeColdRequest(ctx, dataPlane+"/function/z")
	if err := waitPhase(ctx, c, "default", "z", v1.PhaseReady, 30*time.Second); err != nil {
		return Report{}, err
	}

	// (2) per-worker + throughput: "z" is now the single running worker.
	report.PerWorkerMB = shimRSSMB(shimPath)
	lr := runLoad(ctx, dataPlane+"/function/z", `{}`, cfg.Concurrency, cfg.Duration, nil)
	report.RPS, report.Latency = lr.rps, lr.latency

	// (3) density: deploy K more warm functions, measure the marginal RSS per function.
	for i := range cfg.Density {
		name := "d" + strconv.Itoa(i)
		if err := deploy(ctx, c, "default", name, ref, 1, 1, 0); err != nil {
			return Report{}, err
		}
		if err := waitPhase(ctx, c, "default", name, v1.PhaseReady, 30*time.Second); err != nil {
			return Report{}, err
		}
	}
	if cfg.Density > 0 {
		// Marginal slope: (RSS with K+1 workers − RSS with 1) / K = the average cost of one more
		// worker. RSS-based, so it over-counts the shared node runtime (see shimRSSMB) — the
		// containerd lane's cgroup delta is the shared-page-correct version of this same slope.
		totalShim := shimRSSMB(shimPath)
		report.PerFunctionMB = (totalShim - report.PerWorkerMB) / float64(cfg.Density)
	} else {
		report.PerFunctionMB = report.PerWorkerMB
	}
	if report.PerFunctionMB > 0 {
		report.MaxDensity = int((float64(cfg.MemBudgetMB) - report.PlatformBaselineMB) / report.PerFunctionMB)
	}
	report.FitsTarget = cfg.TargetFns > 0 && report.MaxDensity >= cfg.TargetFns

	return report, nil
}

func deploy(ctx context.Context, c *sdk.Client, ns, name, ref string, minReplicas, replicas int, idle time.Duration) error {
	obj, ok := v1.NewObject(v1.KindFunction)
	if !ok {
		return fault.Internalf("bench.deploy", "unknown kind %s", v1.KindFunction)
	}
	fn, _ := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref}
	fn.Spec.Replicas = replicas
	fn.Spec.Scaling = v1.Scaling{MinReplicas: minReplicas, IdleTimeout: idle}
	_, err := c.Apply(ctx, fn)
	return err
}

func waitPhase(ctx context.Context, c *sdk.Client, ns, name string, want v1.Phase, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if obj, err := c.Get(ctx, v1.KindFunction, v1.NamespaceName(ns), v1.ObjectName(name)); err == nil {
			if fn, ok := obj.(*v1.Function); ok && fn.Status.Phase == want {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fault.Unavailablef("bench.waitPhase", "function %q did not reach %s within %s", name, want, timeout)
}

func timeColdRequest(ctx context.Context, url string) time.Duration {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return 0
	}
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return time.Since(start)
}

func waitListening(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fault.Unavailablef("bench.waitListening", "control plane %s did not come up within %s", addr, timeout)
}
