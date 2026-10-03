package scripts_test

import (
	"crypto/sha1" //nolint:gosec // the cache key of scripts/agent/d, not a security use
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// devshellCheckout copies scripts/agent/d into a fresh git checkout with a stand-in flake and a cached environment
// already in place, so d runs without nix. It returns the checkout's root.
func devshellCheckout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput()
	require.NoError(t, err, "%s", out)
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	src, err := os.ReadFile(filepath.Join("agent", "d"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts", "agent"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "agent", "d"), src, 0o755)) //nolint:gosec // an executable script
	flake, lock := "{ }\n", "{}\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "flake.nix"), []byte(flake), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "flake.lock"), []byte(lock), 0o600))
	sum := sha1.Sum([]byte(flake + lock)) //nolint:gosec // matches d's shasum key
	env := filepath.Join(root, ".cache", "devshell-env-"+hex.EncodeToString(sum[:])[:16]+".sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(env), 0o755))
	require.NoError(t, os.WriteFile(env, []byte("true\n"), 0o600))
	return root
}

// golangci-lint caches each package's issues with their absolute paths. A cache shared by every worktree replays the
// issues found in a worktree that was later removed, and it can no longer read that worktree's //nolint lines or match
// its path rules, so a clean tree fails lint (seen after a wave check removed its merged worktree). The agent shell
// gives each checkout its own cache, and keeps one the caller set.
func TestAgentShellGivesEachCheckoutItsOwnLintCache(t *testing.T) {
	root := devshellCheckout(t)
	d := filepath.Join(root, "scripts", "agent", "d")

	cmd := exec.Command(d, "sh", "-c", `printf %s "$GOLANGCI_LINT_CACHE"`)
	cmd.Env = append(os.Environ(), "GOLANGCI_LINT_CACHE=")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Equal(t, filepath.Join(root, ".cache", "golangci-lint"), string(out))

	cmd = exec.Command(d, "sh", "-c", `printf %s "$GOLANGCI_LINT_CACHE"`)
	cmd.Env = append(os.Environ(), "GOLANGCI_LINT_CACHE=/elsewhere")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Equal(t, "/elsewhere", string(out))
}
