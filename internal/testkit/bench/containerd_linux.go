//go:build linux

package bench

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/containerd"
	"github.com/pyvvo/funcd/internal/runtime/ctrmanager"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// readyTimeout is how long the lane waits for the first container to become Ready before
// concluding the production path is not provisioned (a generous window — the first image pull +
// crun start is slow). A timeout here is treated as a *skip*, not a failure: on a correctly
// provisioned box the function becomes Ready, and the only reasons it would not are infra
// (missing curated image, crun, or CNI) — exactly what this lane is opt-in for.
const readyTimeout = 90 * time.Second

// RunContainerd boots funcd over the real containerd/crun production path (ADR-0032 driver),
// deploys functions that run in real crun containers, and measures each container's cgroup-v2
// memory.current — the honest production footprint (ADR-0052). It runs only on a Linux host
// (root) with a reachable containerd; on any missing precondition it returns
// FootprintReport{Skipped:true,…} with a nil error, never failing the run.
func RunContainerd(ctx context.Context, cfg ContainerdConfig) (FootprintReport, error) {
	const op = "bench.RunContainerd"
	if os.Geteuid() != 0 {
		return skip("containerd footprint lane needs root (cgroup + netns); re-run with sudo")
	}
	// Isolated funcd resource namespace (ADR-0055 dedicated-box): the throwaway functions live
	// here, never mixing with a live daemon's "default" — the containerd driver maps it to its
	// own containerd namespace. Default to "funcd-bench" when the caller left it empty.
	ns := cfg.Namespace
	if ns == "" {
		ns = "funcd-bench"
	}

	images := ctrmanager.Config{ImageOverride: cfg.ImageOverride}
	ccfg := containerd.Config{
		Socket: cfg.Socket, Snapshotter: cfg.Snapshotter,
		CNIBinDir: cfg.CNIBinDir, CNIConfDir: cfg.CNIConfDir, SubnetCIDR: cfg.SubnetCIDR,
		Pullable: images.Pullable(cfg.ImagePrefix),
	}
	rt, err := containerd.New(ccfg)
	if err != nil {
		return skip("containerd unavailable (" + err.Error() + "); install containerd + crun + CNI and retry")
	}
	defer func() { _ = rt.Close() }()

	// A SECOND, independent driver handle, used only to tear the namespace down. The platform Closes
	// the runtime it is given (rt) on shutdown, so a deferred sweep through rt would hit a closed
	// client; cleanupRT stays open for a teardown that must run AFTER the platform stops (so the
	// reconciler can't re-create) yet with a live containerd client. Deferred, it fires on EVERY exit
	// path — success, the not-Ready skip, an error, or a SIGINT-cancelled unwind — so the lane always
	// leaves its namespace empty (no orphaned container/snapshot to collide with the next run).
	cleanupRT, err := containerd.New(ccfg)
	if err != nil {
		return skip("containerd unavailable (" + err.Error() + "); install containerd + crun + CNI and retry")
	}
	defer func() { _ = cleanupRT.Close() }()
	defer teardownContainers(cleanupRT, ns)

	// Defensive sweep before deploying: clear anything a prior HARD kill left behind (where the
	// deferred teardown above could not run). teardownContainers prefers the driver's Sweep, which
	// walks containerd directly, so it reaps cross-process orphans the freshly-started managed
	// containerd reconnected to from its data-root (the in-process List/Stop path only sees this
	// process). Retried, because containerd discovers orphaned shims lazily after the socket is up.
	teardownContainers(cleanupRT, ns)

	tmp, err := os.MkdirTemp("", "funcd-footprint-*")
	if err != nil {
		return FootprintReport{}, fault.Wrapf(err, fault.Internal, op, "temp dir")
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	bundle := filepath.Join(tmp, "handler.mjs")
	if werr := os.WriteFile(bundle, []byte("export function handle() { return { ok: true }; }\n"), 0o600); werr != nil {
		return FootprintReport{}, fault.Wrapf(werr, fault.Internal, op, "write handler bundle")
	}

	imageFor := images.ImageFor(cfg.ImagePrefix)
	p, err := funcd.New(
		funcd.InMemory(),
		// InMemory() scopes the dev token to "default"; the lane deploys into its own isolated
		// namespace (ns), so re-scope the dev developer to both — otherwise authz denies the Apply.
		funcd.WithDevAuth(funcd.DevToken, "default", ns),
		funcd.WithRuntime(rt),
		funcd.WithContainerExecution(imageFor),
		funcd.WithArtifactStore(filepath.Join(tmp, "artifacts")),
	)
	if err != nil {
		return FootprintReport{}, fault.Wrapf(err, fault.Internal, op, "new platform")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = p.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	if werr := waitListening(p.Addr(), 10*time.Second); werr != nil {
		return FootprintReport{}, werr
	}

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	if err != nil {
		return FootprintReport{}, fault.Wrapf(err, fault.Internal, op, "sdk client")
	}
	ref := "oci-layout://" + filepath.Join(tmp, "layout") + ":v1"
	if _, perr := artifact.Push(ctx, ref, bundle, nil, "", ""); perr != nil {
		return FootprintReport{}, fault.Wrapf(perr, fault.Internal, op, "push artifact")
	}
	dataPlane := "http://" + p.DataPlaneAddr()

	report := FootprintReport{PlatformBaselineMB: rssMB(os.Getpid())}

	// (1) one warm container — and the skip detector: if it never reaches Ready, the production
	// path is not provisioned (treat as skip, not failure — see readyTimeout).
	if derr := deploy(ctx, c, ns, "z", ref, 1, 1, 0); derr != nil {
		return FootprintReport{}, derr
	}
	if perr := waitPhase(ctx, c, ns, "z", v1.PhaseReady, readyTimeout); perr != nil {
		return skip("warm function did not reach Ready within " + readyTimeout.String() +
			" — check the curated image import (just build-runtime-images + ctr images import), containerd, crun, CNI")
	}
	baseCgroup := sumContainerCgroupMB(ctx, rt, ns)
	baseRSS := shimRSSMB(cfg.ShimPath)

	// The lane deploys "z" into its OWN isolated namespace (ns, ADR-0055), but the data plane
	// resolves a missing X-Funcd-Namespace header to "default" — so the load must carry the
	// namespace header, or every request 404s ("default/z" not found) and throughput reads 0.
	nsHeader := map[string]string{"X-Funcd-Namespace": string(ns)}
	lr := runLoad(ctx, dataPlane+"/function/z", `{}`, cfg.Concurrency, cfg.Duration, nsHeader)
	report.RPS, report.Latency = lr.rps, lr.latency

	// (2) density sweep — K more warm containers; the marginal cgroup + RSS per function.
	for i := range cfg.Density {
		name := "d" + strconv.Itoa(i)
		if derr := deploy(ctx, c, ns, name, ref, 1, 1, 0); derr != nil {
			return FootprintReport{}, derr
		}
		if perr := waitPhase(ctx, c, ns, name, v1.PhaseReady, readyTimeout); perr != nil {
			return FootprintReport{}, fault.Unavailablef(op, "density function %q did not reach Ready", name)
		}
	}
	if cfg.Density > 0 {
		// Marginal slope at the cgroup level: (cgroup with 1+K containers − cgroup with 1) / K. The
		// delta nets out shared image pages — same-image containers share the runtime's read-only
		// pages via the page cache (charged once, not per container: overlayfs page-cache sharing,
		// https://docs.docker.com/engine/storage/drivers/overlayfs-driver/), which is exactly why
		// this marginal lands BELOW per-process RSS. Same goal as PSS, measured from the cgroup.
		totalCgroup := sumContainerCgroupMB(ctx, rt, ns)
		totalRSS := shimRSSMB(cfg.ShimPath)
		report.PerFunctionCgroupMB = (totalCgroup - baseCgroup) / float64(cfg.Density)
		report.PerFunctionRSSMB = (totalRSS - baseRSS) / float64(cfg.Density)
	} else {
		report.PerFunctionCgroupMB = baseCgroup
		report.PerFunctionRSSMB = baseRSS
	}
	if report.PerFunctionCgroupMB <= 0 {
		return skip("could not read any container cgroup memory.current (cgroup v2 mounted at /sys/fs/cgroup?)")
	}
	if report.PerFunctionRSSMB > 0 {
		report.CgroupOverRSS = report.PerFunctionCgroupMB / report.PerFunctionRSSMB
	}
	report.MaxDensity = int((float64(cfg.MemBudgetMB) - report.PlatformBaselineMB) / report.PerFunctionCgroupMB)
	report.FitsTarget = cfg.TargetFns > 0 && report.MaxDensity >= cfg.TargetFns
	return report, nil // teardown is the deferred cleanupRT sweep (runs on every exit path)
}

// teardownContainers force-removes every function container in the bench namespace — this run's, or
// any a previous run orphaned — so the privately-managed containerd can be shut down with nothing
// live and the lane stays cleanly re-runnable with no manual `ctr` cleanup. It prefers the driver's
// Sweep, which walks containerd directly (client.Containers) and so recovers cross-process orphans a
// hard-killed predecessor left (the in-process List/Stop path only sees THIS process's containers,
// which can't clean a dirty namespace); Stop/List is the fallback for a non-containerd runtime. It
// re-sweeps until two consecutive passes find nothing, because the freshly-started managed containerd
// discovers + reconnects to orphaned shims LAZILY after its socket is up — a single pass can run
// before recovery completes and miss one. A fresh bounded context is used so this still runs after
// the caller's ctx was cancelled by SIGINT (the graceful-teardown path).
func teardownContainers(rt runtime.Runtime, ns string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if sw, ok := rt.(interface {
		Sweep(context.Context, v1.NamespaceName) (int, error)
	}); ok {
		empties := 0
		for range 20 {
			n, err := sw.Sweep(ctx, v1.NamespaceName(ns))
			if err != nil {
				return
			}
			if n == 0 {
				if empties++; empties >= 2 {
					return
				}
			} else {
				empties = 0
			}
			time.Sleep(250 * time.Millisecond)
		}
		return
	}
	insts, err := rt.List(ctx, v1.NamespaceName(ns))
	if err != nil {
		return
	}
	for _, inst := range insts {
		_ = rt.Stop(ctx, inst.ID)
	}
}

// sumContainerCgroupMB sums the cgroup-v2 memory.current of funcd's function containers in the
// given namespace (the isolated bench namespace), deduped per cgroup path so each container
// counts exactly once.
func sumContainerCgroupMB(ctx context.Context, rt runtime.Runtime, ns string) float64 {
	insts, err := rt.List(ctx, v1.NamespaceName(ns))
	if err != nil {
		return 0
	}
	seen := make(map[string]struct{}, len(insts))
	var total float64
	for _, inst := range insts {
		if inst.PID <= 0 {
			continue
		}
		path := cgroupPath(inst.PID)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		total += cgroupMemMB(inst.PID)
	}
	return total
}

// skip builds a Skipped footprint report (nil error — the lane never fails the run).
func skip(reason string) (FootprintReport, error) {
	return FootprintReport{Skipped: true, SkipReason: reason}, nil
}
