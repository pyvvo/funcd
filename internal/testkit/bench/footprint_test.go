package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRunContainerdSkipsWithoutInfra is the non-gated assertion for scenario
// lane-skips-without-containerd (ADR-0052): on a host without the production path — macOS (the
// !linux stub) or a Linux CI box without root/containerd (the linux precondition checks) — the
// lane returns a Skipped report with a reason and a nil error, never failing the run. `just ci`
// runs this on every platform. (The real cgroup measurement is a deferred FUNCD_IT=1 homebox e2e.)
func TestRunContainerdSkipsWithoutInfra(t *testing.T) {
	r, err := RunContainerd(context.Background(), ContainerdConfig{ShimPath: "/opt/funcd/shim.mjs"})
	if err != nil {
		t.Fatalf("RunContainerd returned an error %v; the lane must never fail the run (skip instead)", err)
	}
	if !r.Skipped {
		t.Skip("running on a provisioned containerd host — skip-path assertion not applicable")
	}
	if r.SkipReason == "" {
		t.Error("Skipped report must carry a non-empty SkipReason")
	}
}

// TestWriteFootprintReportSkipped checks a Skipped report still writes a readable
// footprint-report.{md,json} (so the operator sees why it skipped), for scenario
// footprint-report-separate's skip branch.
func TestWriteFootprintReportSkipped(t *testing.T) {
	dir := t.TempDir()
	md, jsonPath, err := WriteFootprintReport(dir, FootprintReport{Skipped: true, SkipReason: "no containerd here"})
	if err != nil {
		t.Fatalf("WriteFootprintReport: %v", err)
	}
	if filepath.Dir(md) != dir || filepath.Dir(jsonPath) != dir {
		t.Errorf("reports not written under %s: %s, %s", dir, md, jsonPath)
	}
	body, err := os.ReadFile(md)
	if err != nil {
		t.Fatalf("read md: %v", err)
	}
	if len(body) == 0 {
		t.Error("skipped footprint report is empty")
	}
}
