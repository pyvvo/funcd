package e2e_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	shimnode "github.com/pyvvo/funcd-typescript/shim"
)

// e2eNodeShim writes the embedded Node shim into t.TempDir(). tests/e2e may not import internal/
// (ADR-0025), so it reads the pinned language module directly instead of internal/testkit/langmod
// (ADR-0141).
func e2eNodeShim(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "shim.mjs")
	require.NoError(t, os.WriteFile(p, shimnode.Shim, 0o600))
	return p
}
