package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	shimpython "github.com/pyvvo/funcd-python/shim"
	shimnode "github.com/pyvvo/funcd-typescript/shim"
	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/runtime/ctrmanager"
	"github.com/pyvvo/funcd/internal/testkit/bench"
)

// benchContainerdNamespace is the dedicated, isolated containerd namespace the --containerd lane
// runs its throwaway functions in (ADR-0055 scenario dedicated-box) — never the live daemon's.
const benchContainerdNamespace = "funcd-bench"

// benchConfig holds the parsed `funcd bench` flags — the union of the in-process lane
// (ADR-0040), the containerd footprint lane (ADR-0052), and the doctor check. Mode is
// selected by the mutually-exclusive --containerd / --doctor bool flags.
type benchConfig struct {
	shim        string
	poolShim    string
	python      string
	concurrency int
	duration    time.Duration
	density     int
	budget      int
	target      int
	out         string
	backends    string

	// mode flags — mutually exclusive; neither set ⇒ the in-process lane (default).
	containerd bool
	doctor     bool

	// containerd footprint lane (ADR-0052) — Linux+root only.
	ctrSocket   string
	snapshotter string
	cniBinDir   string
	cniConfDir  string
	subnetCIDR  string
	imagePrefix string
}

// newBenchCmd builds the single `funcd bench` cobra verb (ADR-0055): the sustainability
// harness folded into the funcd binary. Mode is chosen by mutually-exclusive flags — bare
// = the in-process embed lane (memory+file substrate, RSS — ADR-0040, the default);
// --containerd = the cgroup-footprint lane (ADR-0052); --doctor = a component check + guidance
// (never installs, spins a VM, or ships Lima). It reuses internal/testkit/bench unchanged. Status is
// written to out (the test seam); errors are returned, not os.Exit'd.
func newBenchCmd(out io.Writer) *cobra.Command {
	var c benchConfig
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark & sustainability harness (ADR-0040/0052) — one flag-driven verb",
		Long: "bench runs the funcd sustainability harness in-process by default (memory+file " +
			"substrate, RSS). --containerd runs only the cgroup-footprint lane; --doctor reports " +
			"whether the containerd lane can run and warns on a live daemon (it never installs anything). " +
			"--containerd and --doctor are mutually exclusive. Run it on a dedicated/idle box.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBench(cmd.Context(), out, c)
		},
	}
	f := cmd.Flags()
	f.StringVar(&c.shim, "shim", "", "path to the node runtime shim (default: the embedded shim)")
	f.StringVar(&c.poolShim, "pool-shim", "",
		"path to the pooled worker_threads shim (ADR-0044; default: the embedded pool shim; a missing path skips the comparison)")
	f.StringVar(&c.python, "python", "",
		"python ≥3.14 for the Python pool comparison (ADR-0050); default auto-detect, none found ⇒ skip")
	f.IntVar(&c.concurrency, "concurrency", 8, "load workers")
	f.DurationVar(&c.duration, "duration", 5*time.Second, "load phase per backend")
	f.IntVar(&c.density, "density", 5, "extra warm functions for the density sweep")
	f.IntVar(&c.budget, "mem-budget-mb", 16384, "function-memory budget for the verdict")
	f.IntVar(&c.target, "target-fns", 100, "agents to size for")
	f.StringVar(&c.out, "out", "bench", "output dir for the report")
	f.StringVar(&c.backends, "backends", "memory,file", "comma-separated substrates")
	f.BoolVar(&c.containerd, "containerd", false,
		"run only the containerd cgroup-footprint lane (ADR-0052; Linux + root + containerd/crun/CNI + curated image)")
	f.BoolVar(&c.doctor, "doctor", false,
		"report whether the lanes can run + warn on a live daemon — checks only, never installs/spins a VM/ships Lima")
	f.StringVar(&c.ctrSocket, "containerd-socket", "",
		"external containerd socket (with --containerd); \"\" (default) = funcd brings its own privately-managed containerd (ADR-0054)")
	f.StringVar(&c.snapshotter, "snapshotter", "overlayfs", "containerd snapshotter (with --containerd)")
	f.StringVar(&c.cniBinDir, "cni-bin-dir", "/opt/cni/bin", "CNI plugin dir (with --containerd)")
	f.StringVar(&c.cniConfDir, "cni-conf-dir", "/etc/cni/net.d", "CNI conflist dir (with --containerd)")
	f.StringVar(&c.subnetCIDR, "subnet-cidr", "10.63.0.0/16", "lateral bridge subnet (with --containerd)")
	f.StringVar(&c.imagePrefix, "image-prefix", config.DefaultImagePrefix, "curated image prefix (with --containerd)")
	return cmd
}

// runBench dispatches the chosen mode. --containerd and --doctor are mutually exclusive (both
// set ⇒ a clean api/fault usage error); --doctor reports + guides; --containerd runs only the
// cgroup-footprint lane; bare runs the in-process embed lane (the default).
func runBench(ctx context.Context, out io.Writer, c benchConfig) error {
	if c.containerd && c.doctor {
		return fault.Invalidf("funcd bench", "--containerd and --doctor are mutually exclusive; pass at most one")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	switch {
	case c.doctor:
		return runDoctor(out, c)
	case c.containerd:
		return runContainerdLane(ctx, out, c)
	default:
		return runInProcess(ctx, out, c)
	}
}

// runInProcess is the bare default: the in-process embed bench over the memory+file substrate
// (RSS — ADR-0040), the Node worker-pool comparison (ADR-0044), and the Python pool comparison
// (ADR-0050). It needs node on PATH (the shim runs JS); a missing node skips the lane.
func runInProcess(ctx context.Context, out io.Writer, c benchConfig) error {
	if _, err := exec.LookPath("node"); err != nil {
		logf(out, "funcd bench: node not on PATH (the shim runs JS) — skipping the in-process lane\n")
		return nil
	}
	shimPath, poolShimPath, cleanup, err := resolveShimPaths(c)
	defer cleanup()
	if err != nil {
		return err
	}
	reports, err := runSubstrateBench(ctx, out, c, shimPath, poolShimPath)
	if err != nil {
		return err
	}
	if err := reportNodePool(out, c.out, reports); err != nil {
		return err
	}
	return reportPythonPool(ctx, out, c)
}

// resolveShimPaths returns the node shim and the pool shim to run. An empty flag extracts the
// embedded copy (ADR-0141) into a temp dir that cleanup removes; an explicit --shim must exist, and an
// explicit --pool-shim that is missing leaves poolShimPath "" (pooling comparison skipped).
func resolveShimPaths(c benchConfig) (shimPath, poolShimPath string, cleanup func(), err error) {
	cleanup = func() {}
	if c.shim == "" || c.poolShim == "" {
		dir, derr := os.MkdirTemp("", "funcd-bench-nodeshim-*")
		if derr != nil {
			return "", "", cleanup, fmt.Errorf("temp dir for the embedded node shims: %w", derr)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		if c.shim == "" {
			if shimPath, err = writeEmbeddedShim(dir, "shim.mjs", shimnode.Shim); err != nil {
				return "", "", cleanup, err
			}
		}
		if c.poolShim == "" {
			if poolShimPath, err = writeEmbeddedShim(dir, "pool.mjs", shimnode.Pool); err != nil {
				return "", "", cleanup, err
			}
		}
	}
	if c.shim != "" {
		if shimPath, err = filepath.Abs(c.shim); err != nil {
			return "", "", cleanup, fmt.Errorf("resolve shim path: %w", err)
		}
		if _, serr := os.Stat(shimPath); serr != nil {
			return "", "", cleanup, fmt.Errorf("shim not found at %s: %w", shimPath, serr)
		}
	}
	if c.poolShim != "" {
		if p, perr := filepath.Abs(c.poolShim); perr == nil {
			if _, serr := os.Stat(p); serr == nil {
				poolShimPath = p
			}
		}
	}
	return shimPath, poolShimPath, cleanup, nil
}

func writeEmbeddedShim(dir, name string, data []byte) (string, error) {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", fmt.Errorf("extract the embedded %s: %w", name, err)
	}
	return p, nil
}

// runSubstrateBench runs the per-substrate sustainability bench, writes the report, prints one
// line per substrate, and returns the reports (which also carry the pool-comparison numbers).
func runSubstrateBench(ctx context.Context, out io.Writer, c benchConfig, shimPath, poolShimPath string) ([]bench.Report, error) {
	var backends []bench.Backend
	for _, b := range strings.Split(c.backends, ",") {
		backends = append(backends, bench.Backend(strings.TrimSpace(b)))
	}
	logf(out, "funcd bench: %v · %d workers × %s · target %d fns / %d MB\n",
		backends, c.concurrency, c.duration, c.target, c.budget)

	reports, err := bench.Run(ctx, shimPath, poolShimPath, bench.Config{
		Backends: backends, Concurrency: c.concurrency, Duration: c.duration,
		Density: c.density, MemBudgetMB: c.budget, TargetFns: c.target,
	})
	if err != nil {
		return nil, fmt.Errorf("substrate bench: %w", err)
	}
	mdPath, jsonPath, err := bench.WriteReport(c.out, reports)
	if err != nil {
		return nil, fmt.Errorf("write report: %w", err)
	}
	for _, r := range reports {
		logf(out, "  [%s] %.0f req/s · p99 %s · per-worker %.1f MB · idle %.1f MB · cold-start %s · max-density %d · fits=%v\n",
			r.Backend, r.RPS, r.Latency.P99.Round(time.Millisecond), r.PerWorkerMB, r.IdleMB,
			r.ColdStart.Round(time.Millisecond), r.MaxDensity, r.FitsTarget)
	}
	logf(out, "funcd bench: wrote %s and %s\n", mdPath, jsonPath)
	return reports, nil
}

// reportNodePool writes + prints the Node worker-pool comparison (ADR-0044): density + a fair
// pooled-vs-single-tenant throughput comparison, both hit directly on the shim port.
func reportNodePool(out io.Writer, outDir string, reports []bench.Report) error {
	mdPath, jsonPath, err := bench.WritePoolReport(outDir, reports)
	if err != nil {
		return fmt.Errorf("write pool report: %w", err)
	}
	if mdPath == "" {
		return nil
	}
	for _, r := range reports {
		if r.PooledK == 0 {
			continue
		}
		logf(out, "  [pool ×%d] %.1f MB/fn (%.1f× density) · pooled %.0f req/s p99 %s vs single-direct %.0f req/s p99 %s (%.0f%% throughput)\n",
			r.PooledK, r.PooledPerFunctionMB, densityGain(r.PerFunctionMB, r.PooledPerFunctionMB),
			r.PooledRPS, r.PooledLatency.P99.Round(time.Millisecond),
			r.SingleDirectRPS, r.SingleDirectLatency.P99.Round(time.Millisecond),
			percent(r.PooledRPS, r.SingleDirectRPS))
		break
	}
	logf(out, "funcd bench: wrote %s and %s\n", mdPath, jsonPath)
	return nil
}

// reportPythonPool runs the Python solo-vs-pool comparison (ADR-0050) when a Python ≥3.14 is
// present, writing + printing it (skipped with a note otherwise). It settles first: the Node
// sweep leaves the box loaded, which would otherwise dominate the Python numbers.
func reportPythonPool(ctx context.Context, out io.Writer, c benchConfig) error {
	time.Sleep(3 * time.Second)
	python := findPython314(c.python)
	if python == "" {
		logf(out, "funcd bench: no Python ≥3.14 found — skipping the Python pool comparison (set --python or FUNCD_PYTHON)\n")
		return nil
	}
	dir, err := os.MkdirTemp("", "funcd-bench-pyshim-*")
	if err != nil {
		return fmt.Errorf("temp dir for python shim: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	soloEntry, poolEntry, err := shimpython.Extract(dir)
	if err != nil {
		return fmt.Errorf("extract python shim: %w", err)
	}
	cmp, err := bench.MeasurePythonPool(ctx, python, soloEntry, poolEntry,
		c.density, c.concurrency, c.duration)
	if err != nil {
		return fmt.Errorf("python pool comparison: %w", err)
	}
	mdPath, jsonPath, err := bench.WritePyPoolReport(c.out, cmp)
	if err != nil {
		return fmt.Errorf("write python pool report: %w", err)
	}
	logf(out, "  [py-pool ×%d] %.1f MB/fn (%.1f× density) · pooled %.0f req/s p99 %s vs solo %.0f req/s p99 %s\n",
		cmp.K, cmp.PooledMBPerFn, cmp.DensityGain, cmp.PooledRPS, cmp.PooledP99.Round(time.Millisecond),
		cmp.SoloRPS, cmp.SoloP99.Round(time.Millisecond))
	logf(out, "funcd bench: wrote %s and %s\n", mdPath, jsonPath)
	return nil
}

// runContainerdLane runs only the containerd cgroup-footprint lane (ADR-0052): it boots funcd
// over the real containerd/crun production path and measures each container's cgroup
// memory.current. On any host without the path (macOS dev, or Linux missing root/image)
// it skips with a logged reason and the command still succeeds.
//
// This completes ADR-0055's --containerd lane on top of ADR-0054's runtime Manager: instead of a
// fixed system socket + a hand-provisioned image, it brings up funcd's OWN runtime via
// ctrmanager — a privately-managed containerd (--containerd-socket "" default) that imports the
// embedded curated image — and runs in a dedicated, isolated namespace + bench-specific data-root
// (the dedicated-box isolation). A non-empty --containerd-socket defers to that external daemon.
// When Ensure fails (non-Linux dev box, non-root, or the private containerd can't start) the lane
// SKIPS cleanly with exit 0 — the run succeeds; only hosts on the production path measure.
func runContainerdLane(ctx context.Context, out io.Writer, c benchConfig) error {
	// SIGINT/SIGTERM-safe: cancel ctx on the first signal so RunContainerd unwinds and its deferred
	// container teardown + the Manager.Close below run (graceful cleanup), instead of the process
	// being killed mid-run and orphaning the managed containerd + its container shims (which would
	// make the next `funcd bench --containerd` collide on the same container ids).
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ADR-0054 runtime Manager: ExternalSocket "" ⇒ a privately-managed containerd + embedded
	// image import; a bench-specific DataRoot so it never collides with a live funcd daemon's
	// containerd data-root (the dedicated-box isolation).
	mgrCfg := ctrmanager.Config{
		ExternalSocket: c.ctrSocket,
		DataRoot:       filepath.Join(os.TempDir(), "funcd-bench-containerd"),
		ImageOverride:  imageOverrides(),
	}
	mgr, err := ctrmanager.New(mgrCfg)
	if err != nil {
		return fmt.Errorf("build containerd runtime manager: %w", err)
	}
	defer func() { _ = mgr.Close() }()

	socket, err := mgr.Ensure(ctx)
	if err != nil {
		// Manager unavailable (non-Linux/non-root/private containerd can't start) ⇒ clean SKIP.
		logf(out, "funcd bench: containerd footprint lane skipped — %s\n", err)
		return nil
	}

	r, err := bench.RunContainerd(ctx, bench.ContainerdConfig{
		Socket: socket, Namespace: benchContainerdNamespace, Snapshotter: c.snapshotter,
		CNIBinDir: c.cniBinDir, CNIConfDir: c.cniConfDir, SubnetCIDR: c.subnetCIDR,
		ImagePrefix: c.imagePrefix, ImageOverride: mgrCfg.ImageOverride, ShimPath: "/opt/funcd/shim.mjs",
		Density: c.density, MemBudgetMB: c.budget, TargetFns: c.target,
		Concurrency: c.concurrency, Duration: c.duration,
	})
	if err != nil {
		return fmt.Errorf("containerd footprint lane: %w", err)
	}
	if r.Skipped {
		logf(out, "funcd bench: containerd footprint lane skipped — %s\n", r.SkipReason)
		return nil
	}
	mdPath, jsonPath, err := bench.WriteFootprintReport(c.out, r)
	if err != nil {
		return fmt.Errorf("write footprint report: %w", err)
	}
	logf(out, "  [containerd] %.1f MB/fn cgroup (%.2f× the %.1f MB RSS) · %.0f req/s · max-density %d · fits=%v\n",
		r.PerFunctionCgroupMB, r.CgroupOverRSS, r.PerFunctionRSSMB, r.RPS, r.MaxDensity, r.FitsTarget)
	logf(out, "funcd bench: wrote %s and %s\n", mdPath, jsonPath)
	return nil
}

// runDoctor reports whether each bench lane can run + suggests a fix — it never installs
// anything, spins a VM, or ships Lima (ADR-0055 scenario doctor-checks-not-installs). It always
// exits 0 (a missing component is reported, not a failure). It also warns when a funcd daemon
// appears to be running, since a density+load bench wants a dedicated/idle box (the kubemark rule).
func runDoctor(out io.Writer, c benchConfig) error {
	logf(out, "funcd bench --doctor: component check (no install, no VM, no Lima)\n")

	// In-process lane: needs node on PATH (the shim runs JS).
	if node, err := exec.LookPath("node"); err == nil {
		logf(out, "  in-process lane:  OK (node at %s)\n", node)
	} else {
		logf(out, "  in-process lane:  not available — node not on PATH\n")
	}

	// Python pool: needs a Python ≥3.14 (the pool host floor, ADR-0050).
	if python := findPython314(c.python); python != "" {
		logf(out, "  python pool:      OK (python ≥3.14 at %s)\n", python)
	} else {
		logf(out, "  python pool:      skipped — no python ≥3.14 (set --python or FUNCD_PYTHON)\n")
	}

	// Containerd lane: probe components without running the lane.
	if reason := doctorContainerd(c); reason == "" {
		logf(out, "  containerd lane:  OK (crun + containerd socket + cgroup-v2 present)\n")
	} else {
		logf(out, "  containerd lane:  not available — %s\n", reason)
	}

	// Dedicated-box guard: warn if a funcd daemon appears to be running (ADR-0024/0028 listen addr).
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:8080", 300*time.Millisecond); err == nil {
		_ = conn.Close()
		logf(out, "  WARNING: a funcd daemon appears to be running at 127.0.0.1:8080 — "+
			"run the bench on a dedicated/idle box (a density+load bench contends for the host, the kubemark rule)\n")
	}
	return nil
}

// doctorContainerd reports why the containerd lane can't run, or "" if every component is present.
// It probes without running the lane: Linux-only, crun on PATH, the containerd socket present, and
// cgroup-v2 mounted. The reason names every missing component (non-empty ⇒ not available).
func doctorContainerd(c benchConfig) string {
	if runtime.GOOS != "linux" {
		return "Linux-only (this box is " + runtime.GOOS + ")"
	}
	var missing []string
	if _, err := exec.LookPath("crun"); err != nil {
		missing = append(missing, "crun not on PATH")
	}
	// An empty --containerd-socket means the privately-managed containerd (ADR-0054): funcd
	// brings its own, so there is no external socket to probe. Only check an external one.
	if c.ctrSocket != "" {
		if _, err := os.Stat(c.ctrSocket); err != nil {
			missing = append(missing, "containerd socket "+c.ctrSocket+" absent")
		}
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		missing = append(missing, "cgroup-v2 not mounted")
	}
	if len(missing) == 0 {
		return ""
	}
	return strings.Join(missing, "; ") + " (suggest a Linux box/VM with containerd + crun)"
}

// findPython314 returns the first candidate that is Python ≥3.14 (the pool-host floor, ADR-0050):
// the explicit flag, then FUNCD_PYTHON, then python3.14 / python3 on PATH. "" if none.
// (pythonAtLeast314 lives in main.go — same package — and is reused here.)
func findPython314(explicit string) string {
	for _, candidate := range []string{explicit, os.Getenv("FUNCD_PYTHON"), "python3.14", "python3"} {
		if candidate == "" {
			continue
		}
		if p, err := exec.LookPath(candidate); err == nil && pythonAtLeast314(p) {
			return p
		}
	}
	return ""
}

// densityGain is solo MB/fn ÷ pooled MB/fn (0 when pooled is unmeasured).
func densityGain(soloMB, pooledMB float64) float64 {
	if pooledMB <= 0 {
		return 0
	}
	return soloMB / pooledMB
}

// percent is 100 × num/den (0 when den is unmeasured).
func percent(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return 100 * num / den
}

// logf writes a status line to out (the bench command's user-facing output seam).
func logf(out io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(out, format, a...)
}

// imageOverrides parses FUNCD_IMAGE_OVERRIDE ("runtime=ref,runtime=ref") into the Manager's
// --image override map (ADR-0054) for the bench --containerd lane: a listed runtime is pulled from
// its registry ref instead of imported from the embedded curated tar. Empty/malformed entries are
// skipped. (The daemon resolves the same env via internal/platform/config; the bench is its own harness.)
func imageOverrides() map[string]string {
	raw := os.Getenv("FUNCD_IMAGE_OVERRIDE")
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		if rt, ref, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && rt != "" && ref != "" {
			out[rt] = ref
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
