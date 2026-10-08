package scripts_test

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// devshellCheckout copies scripts/agent/d into a fresh git checkout with a stand-in flake and envScript as its cached
// environment, so d runs without nix. It returns the checkout's root.
func devshellCheckout(t *testing.T, envScript string) string {
	t.Helper()
	root := t.TempDir()
	out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput()
	require.NoError(t, err, "%s", out)
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	src, err := os.ReadFile(filepath.Join("agent", "d"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts", "agent"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "agent", "d"), src, 0o755))
	flake, lock := "{ }\n", "{}\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "flake.nix"), []byte(flake), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "flake.lock"), []byte(lock), 0o600))
	sum := sha1.Sum([]byte(flake + lock))
	env := filepath.Join(root, ".cache", "devshell-env-"+hex.EncodeToString(sum[:])[:16]+".sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(env), 0o755))
	require.NoError(t, os.WriteFile(env, []byte(envScript), 0o600))
	return root
}

// golangci-lint caches each package's issues with their absolute paths. A cache shared by every worktree replays the
// issues found in a worktree that was later removed, and it can no longer read that worktree's //nolint lines or match
// its path rules, so a clean tree fails lint (seen after a wave check removed its merged worktree). The agent shell
// gives each checkout its own cache, and keeps one the caller set.
func TestAgentShellGivesEachCheckoutItsOwnLintCache(t *testing.T) {
	root := devshellCheckout(t, "true\n")
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

// The cached `nix print-dev-env` script points TMPDIR at a fresh nix-shell.XXXXXX directory under the current one, so
// a d call nested in another (gate.sh under `d timeout`) grew TMPDIR by one level and pushed a test's socket path past
// the Unix limit (#41). A nested call keeps the environment it inherits; a direct call, or one from another flake's
// environment, still sources the pinned toolchain.
func TestIssue853_NestedCallKeepsTheEnvironment(t *testing.T) {
	root := devshellCheckout(t, `export TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/nix-shell.XXXXXX")"
export PATH="$PWD/toolchain/bin"
`)
	bin := filepath.Join(root, "toolchain", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pinned-tool"), []byte("#!/bin/sh\necho pinned\n"), 0o755))
	d := filepath.Join(root, "scripts", "agent", "d")
	run := func(key, script string) []string {
		t.Helper()
		cmd := exec.Command(d, "sh", "-c", script)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir(), "FUNCD_DEVSHELL_KEY="+key)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	lines := run("", `echo "$TMPDIR"; scripts/agent/d sh -c 'echo "$TMPDIR"; pinned-tool'`)
	require.Len(t, lines, 3)
	require.Equal(t, lines[0], lines[1], "a nested call must keep TMPDIR")
	require.Equal(t, "pinned", lines[2], "a nested call must keep the toolchain on PATH")

	require.Equal(t, []string{"pinned"}, run("", "pinned-tool"), "a direct call must provide the toolchain")
	require.Equal(t, []string{"pinned"}, run("0123456789abcdef", "pinned-tool"),
		"a call from another flake's environment must source this one")
}
