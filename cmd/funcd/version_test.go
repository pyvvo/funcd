package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found above the test file")
		dir = parent
	}
}

// scenario: funcd-version-subcommand-stamped — builds cmd/funcd with a real -ldflags -X
// stamp (pure-Go, CI-buildable) and asserts `funcd version` reports it, without starting
// the platform. Proves the stamp end-to-end (mirrors the lint-fixture shell-outs).
func TestScenarioFuncdVersionSubcommandStamped(t *testing.T) {
	if testing.Short() {
		t.Skip("builds + runs the binary; skipped under -short")
	}
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "funcd")
	ldflags := "-X github.com/green-0-rabbit/funcd/internal/version.Version=v9.9.9"

	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, "./cmd/funcd")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "build failed: %s", out)

	run, err := exec.Command(bin, "version").CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(run), "v9.9.9", "funcd version reports the stamped version")
}
