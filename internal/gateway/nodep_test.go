package gateway_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: lura-dependency-gone — ADR-0029 dropped the Lura driver; this guard
// asserts the luraproject dependency is gone from go.mod so it can't silently
// creep back (the depguard `main` deny blocks re-import at the source level; this
// guards the module manifest).
func TestScenarioLuraDependencyGone(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(file)
	var root string
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found")
		dir = parent
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	require.NotContains(t, string(mod), "luraproject", "the Lura dependency must stay dropped (ADR-0029)")
	require.NotContains(t, string(mod), "krakend/flatmap", "Lura's transitive dep must be gone too")
}
