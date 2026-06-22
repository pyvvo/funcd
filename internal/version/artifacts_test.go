package version_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// repoRoot walks up from this test file to the directory holding go.mod.
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

// scenario: packaging-artifacts-present — a drift guard that the shipped packaging
// artifacts exist and stay coherent (reads them off disk; no new dep).
func TestScenarioPackagingArtifactsPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	unit, err := os.ReadFile(filepath.Join(root, "configs/systemd/funcd.service"))
	require.NoError(t, err, "systemd unit must exist")
	u := string(unit)
	require.Contains(t, u, "ExecStart=")
	for _, directive := range []string{"NoNewPrivileges=true", "ProtectSystem=strict", "PrivateTmp=true", "StateDirectory=funcd"} {
		require.Containsf(t, u, directive, "unit must ship hardened (%s)", directive)
	}

	script, err := os.ReadFile(filepath.Join(root, "scripts/build.sh"))
	require.NoError(t, err, "build script must exist")
	sc := string(script)
	require.Contains(t, sc, "internal/version", "build.sh must reference the version package")
	require.Contains(t, sc, ".Version=", "build.sh must stamp Version via -ldflags -X")
	require.Contains(t, sc, "-ldflags")
	require.Contains(t, sc, "CGO_ENABLED", "build.sh must document the pure-Go (CGO_ENABLED=0) build — ADR-0065 removed the cgo/slatedb lane")

	_, err = os.Stat(filepath.Join(root, "docs/install.md"))
	require.NoError(t, err, "install docs (Debian/RHEL) must exist")
}
