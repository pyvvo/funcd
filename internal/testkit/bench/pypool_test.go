package bench_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/green-0-rabbit/funcd/internal/testkit/bench"
	shimpython "github.com/green-0-rabbit/funcd/shim/python"
)

// TestPythonPoolSmoke exercises the Python solo-vs-pool comparison (ADR-0050). Py-gated: skipped
// without a Python ≥3.14 (the subinterpreter pool host floor), exactly as the node smoke skips
// without node. It proves the lane runs end-to-end and that pooling is denser than solo.
func TestPythonPoolSmoke(t *testing.T) {
	python := findPy314(t)
	if python == "" {
		t.Skip("no Python ≥3.14 on PATH (needs concurrent.interpreters); skipping the Python pool smoke")
	}
	// ADR-0123/0071: the shim imports fastjsonschema at load (it compiles the delivered I/O schema
	// at worker warm-up), so the interpreter hosting the shim must carry it — the runtime image
	// ships it, but a bare dev interpreter may not. Skip (like the ≥3.14 gate) when it is absent,
	// rather than fail: this is a host-environment gap, not a shim defect. Point FUNCD_PYTHON at an
	// interpreter with fastjsonschema (e.g. the shim's uv venv) to exercise the lane.
	if err := exec.Command(python, "-c", "import fastjsonschema").Run(); err != nil {
		t.Skip("the target Python lacks fastjsonschema (the shim's runtime dep, ADR-0071); skipping the pool smoke")
	}
	dir := t.TempDir()
	soloEntry, poolEntry, err := shimpython.Extract(dir)
	if err != nil {
		t.Fatalf("extract python shim: %v", err)
	}
	cmp, err := bench.MeasurePythonPool(context.Background(), python, soloEntry, poolEntry, 4, 4, 1*time.Second)
	if err != nil {
		t.Fatalf("MeasurePythonPool: %v", err)
	}
	if cmp.PooledMBPerFn <= 0 || cmp.SoloMBPerFn <= 0 {
		t.Fatalf("RSS not sampled: solo=%.1f pooled=%.1f", cmp.SoloMBPerFn, cmp.PooledMBPerFn)
	}
	// density: K handlers in one host cost less per function than a solo shim (the whole point).
	if cmp.PooledMBPerFn >= cmp.SoloMBPerFn {
		t.Errorf("pooled MB/fn (%.1f) must be below solo (%.1f) — pooling should be denser", cmp.PooledMBPerFn, cmp.SoloMBPerFn)
	}
	if cmp.PooledRPS <= 0 {
		t.Errorf("pooled throughput not measured (load did not flow through the pool host)")
	}
	// the report writes path-free (a version string, never the interpreter path).
	if filepath.IsAbs(cmp.Python) {
		t.Errorf("report must not carry the interpreter path, got %q", cmp.Python)
	}
}

func findPy314(t *testing.T) string {
	t.Helper()
	for _, c := range []string{os.Getenv("FUNCD_PYTHON"), "python3.14", "python3"} {
		if c == "" {
			continue
		}
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		if exec.Command(p, "-c", "import sys;raise SystemExit(0 if sys.version_info>=(3,14) else 1)").Run() == nil {
			return p
		}
	}
	return ""
}
