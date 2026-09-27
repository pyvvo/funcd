// Package langmod locates the language repos funcd pins as Go modules (ADR-0141): a pinned module's
// root, for the committed example builds, and the embedded Node shims written to a temp dir, for the
// tests that exec them.
package langmod

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	shimnode "github.com/pyvvo/funcd-typescript/shim"
)

// The language modules funcd pins in go.mod.
const (
	TypeScript = "github.com/pyvvo/funcd-typescript"
	Python     = "github.com/pyvvo/funcd-python"
)

// Dir returns module mod's root as the go command resolves it: the pinned module-cache dir, or a
// go.work override. The module cache is read-only, so callers never write under it.
func Dir(t testing.TB, mod string) string {
	t.Helper()
	out, err := exec.Command("go", "mod", "download", mod).CombinedOutput()
	require.NoError(t, err, "go mod download %s: %s", mod, out)
	out, err = exec.Command("go", "list", "-m", "-f", "{{.Dir}}", mod).Output()
	require.NoError(t, err, "go list -m %s", mod)
	dir := strings.TrimSpace(string(out))
	require.NotEmpty(t, dir, "go list -m %s resolved no module dir", mod)
	return dir
}

// NodeShim writes the embedded single-tenant Node shim into t.TempDir() and returns its path.
func NodeShim(t testing.TB) string {
	t.Helper()
	return write(t, "shim.mjs", shimnode.Shim)
}

// PoolShim writes the embedded worker_threads pool shim into t.TempDir() and returns its path.
func PoolShim(t testing.TB) string {
	t.Helper()
	return write(t, "pool.mjs", shimnode.Pool)
}

func write(t testing.TB, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, data, 0o600), "write %s", name)
	return p
}
