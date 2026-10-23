package embedimg

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// The duckdb runtime image's shim checkpoints its DuckLake catalog after each committed transaction, not only on
// SIGTERM, so a killed engine loses at most one poll interval of lake changes (ADR-0086, #998). The cases live
// beside the shim, in images/runtime/duckdb/shim_test.py, and run on the host's python3.
func TestIssue998_ShimCheckpointsPerCommit(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH")
	}
	test := filepath.Join("..", "..", "..", "images", "runtime", "duckdb", "shim_test.py")
	out, err := exec.Command(python, test).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", test, err, out)
	}
}

// On SIGTERM the duckdb runtime image's shim stops serving Quack and detaches its catalog before the last checkpoint,
// so a commit acknowledged to a client is in the catalog the next engine recovers (#1198). The cases are
// TestIssue1198ShutdownStopsServingFirst in images/runtime/duckdb/shim_test.py, run on the host's python3.
func TestIssue1198_ShimStopsServingBeforeItsLastCheckpoint(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH")
	}
	test := filepath.Join("..", "..", "..", "images", "runtime", "duckdb", "shim_test.py")
	out, err := exec.Command(python, test, "TestIssue1198ShutdownStopsServingFirst").CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", test, err, out)
	}
}
