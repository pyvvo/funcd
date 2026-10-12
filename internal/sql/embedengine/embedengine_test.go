//go:build dev

package embedengine

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The committed rqlited.gz is the placeholder: it extracts as an executable file and reports not bundled, so
// `funcdctl dev` refuses a SQL binding at start (scenario dev-engine-unavailable).
func TestPlaceholderExtractsAndIsNotBundled(t *testing.T) {
	if Bundled() {
		t.Skip("a real engine is embedded (just build-sql-engine)")
	}
	path, err := Extract(t.TempDir())
	require.NoError(t, err)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.NotZero(t, fi.Mode()&0o100, "the engine is executable")
}
