package e2e_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// scenario: contract-suite-coverage-guard — the L2 drift guard (ADR-0025). It reads the
// filesystem only (no internal/ import, so it stays inside the e2e-boundary): every
// <port>contract package must hold a real contract.go, and the known port set must all
// be present. New-port coverage stays review-enforced (the FS can't see a port interface).
func TestContractSuiteCoverage(t *testing.T) {
	t.Parallel()
	internalDir := filepath.Join(repoRoot(t), "internal")

	found := map[string]bool{}
	err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && strings.HasSuffix(d.Name(), "contract") {
			_, statErr := os.Stat(filepath.Join(path, "contract.go"))
			require.NoError(t, statErr, "%s must contain contract.go", d.Name())
			found[d.Name()] = true
		}
		return nil
	})
	require.NoError(t, err)

	// The known port suites must all be present — a deleted/emptied one fails CI.
	known := []string{
		"storecontract", "blobcontract", "buscontract", "gatewaycontract",
		"runtimecontract", "schedulercontract", "authcontract", "kvstorecontract",
	}
	for _, k := range known {
		require.Truef(t, found[k], "port contract suite %q is missing (L2 drift)", k)
	}
	require.GreaterOrEqual(t, len(found), len(known), "expected at least the known contract suites")
}
