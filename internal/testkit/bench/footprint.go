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

// ContainerdConfig parameterizes the containerd footprint lane (ADR-0052). It mirrors the
// composition root's containerd wiring (cmd/funcd executionOptions) plus the bench's own
// measurement inputs. The lane boots funcd over the real containerd/crun production path and
// measures each function container's cgroup-v2 memory.current — the true footprint.
type ContainerdConfig struct {
	Socket      string // containerd socket to dial (ADR-0054 Manager-provided)
	Namespace   string // isolated containerd namespace for the lane's throwaway containers (ADR-0055 dedicated-box); "" ⇒ "funcd-bench"
	Snapshotter string // overlayfs
	CNIBinDir   string // /opt/cni/bin
	CNIConfDir  string // funcd-written conflist dir
	SubnetCIDR  string // lateral bridge subnet
	ImagePrefix string // "funcd/runtime-"; image = prefix + runtime + ":latest"

	// ShimPath is the in-container shim cmdline token (the curated image's entrypoint —
	// "/opt/funcd/shim.mjs", ADR-0032). The container processes are host-visible PIDs, so
	// shimRSSMB(ShimPath) sums the SAME containers' RSS — a same-population cgroup-vs-RSS ratio.
	ShimPath string

	Density     int           // extra warm functions for the marginal-cgroup sweep
	MemBudgetMB int           // function-memory budget for the verdict
	TargetFns   int           // agents to size for
	Concurrency int           // load workers
	Duration    time.Duration // load phase
}

// FootprintReport is the containerd lane's result (ADR-0052). Skipped is true (with a reason and
// a nil error from RunContainerd) on any host that lacks the production path — the lane never
// fails the run. When not skipped, every memory number is the container cgroup memory.current,
// the honest production footprint.
type FootprintReport struct {
	Skipped    bool   `json:"skipped"`
	SkipReason string `json:"skipReason,omitempty"`

	PerFunctionCgroupMB float64 `json:"perFunctionCgroupMB"` // marginal cgroup memory.current / fn (the true footprint)
	PerFunctionRSSMB    float64 `json:"perFunctionRSSMB"`    // marginal RSS of the SAME container processes (the baseline)
	CgroupOverRSS       float64 `json:"cgroupOverRSS"`       // container cgroup ÷ RSS — how much process RSS under-counts
	PlatformBaselineMB  float64 `json:"platformBaselineMB"`  // funcd control-plane RSS
	MaxDensity          int     `json:"maxDensity"`          // functions within MemBudgetMB, from the cgroup number (honest)
	FitsTarget          bool    `json:"fitsTarget"`          // TargetFns within MemBudgetMB? — NOT optimistic (cgroup-measured)
	RPS                 float64 `json:"rps"`
	Latency             Latency `json:"latency"`
}

// WriteFootprintReport writes the containerd footprint lane (ADR-0052) to dir as
// footprint-report.{md,json}, returning their paths. Unlike report.md (process RSS, ADR-0040),
// this measures the container cgroup — the honest absolute footprint over the production path.
func WriteFootprintReport(dir string, r FootprintReport) (mdPath, jsonPath string, err error) {
	const op = "bench.WriteFootprintReport"
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "mkdir report dir")
	}
	jsonPath = filepath.Join(dir, "footprint-report.json")
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "marshal json")
	}
	if err := os.WriteFile(jsonPath, append(data, '\n'), 0o600); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "write json")
	}
	mdPath = filepath.Join(dir, "footprint-report.md")
	if err := os.WriteFile(mdPath, []byte(renderFootprintMarkdown(r)), 0o600); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "write markdown")
	}
	return mdPath, jsonPath, nil
}

func renderFootprintMarkdown(r FootprintReport) string {
	var b strings.Builder
	b.WriteString("# funcd containerd footprint report (ADR-0052)\n\n")
	if r.Skipped {
		b.WriteString("> **Skipped.** " + r.SkipReason + "\n\n")
		b.WriteString("This lane runs only on a Linux host (root) with containerd, crun, CNI, and the curated runtime image. " +
			"To provision: install `containerd` + `crun` + CNI plugins, write a CNI conflist, `just build-runtime-images` then " +
			"import the image into containerd (`ctr -n funcd-bench images import …`), and run `sudo funcd bench --containerd`.\n")
		return b.String()
	}
	b.WriteString("> **The true production footprint:** each function runs in a **real crun container under containerd** " +
		"(gateway → activator → worker netns → shim), and the number below is the memory **actually charged to that " +
		"container's cgroup** (`memory.current`) — which, for a shared curated image, differs from the per-process RSS in " +
		"`report.md` in a way the note below explains.\n\n")

	fmt.Fprintf(&b, "| metric | value |\n|---|---|\n")
	fmt.Fprintf(&b, "| **marginal** cgroup memory / fn (MB) | **%.1f** |\n", r.PerFunctionCgroupMB)
	fmt.Fprintf(&b, "| marginal process RSS / fn (MB) | %.1f |\n", r.PerFunctionRSSMB)
	fmt.Fprintf(&b, "| cgroup ÷ RSS | %.2f× |\n", r.CgroupOverRSS)
	fmt.Fprintf(&b, "| platform baseline (MB) | %.1f |\n", r.PlatformBaselineMB)
	fmt.Fprintf(&b, "| throughput (req/s) | %.0f |\n", r.RPS)
	fmt.Fprintf(&b, "| latency p99 | %s |\n", r.Latency.P99.Round(time.Millisecond))
	fmt.Fprintf(&b, "| max density (fns within budget) | %d |\n", r.MaxDensity)
	fmt.Fprintf(&b, "| fits target? | %s |\n\n", fitsCell(r.FitsTarget))

	if r.CgroupOverRSS > 0 && r.CgroupOverRSS < 1 {
		// The measured-in-practice case for a shared curated image: marginal cgroup < per-process RSS.
		fmt.Fprintf(&b, "The **marginal** cgroup cost of one more function is **%.2f×** its process RSS — *lower*, because "+
			"functions share one curated image: the runtime's read-only pages (node binary, libs, shim) are charged **once** "+
			"to the first container and shared by the rest via the page cache, whereas per-process RSS counts them in **every** "+
			"process. So RSS *over*-counts shared pages and the real density ceiling (**%d** fns) is **higher** than the "+
			"process-RSS lane suggests. (The *absolute* first-container footprint is still > one RSS — page cache + kernel — "+
			"but that one-time cost amortizes across the fleet.)\n", r.CgroupOverRSS, r.MaxDensity)
	} else {
		fmt.Fprintf(&b, "The cgroup footprint is **%.2f×** the process RSS — the page-cache + kernel overhead `report.md` "+
			"cannot see, so its RSS verdict is an optimistic lower bound. The density above is computed from the cgroup "+
			"number — the realistic ceiling.\n", r.CgroupOverRSS)
	}
	return b.String()
}

func fitsCell(ok bool) string {
	if ok {
		return "✅ yes"
	}
	return "❌ no"
}
