package scripts_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

// bashMajor returns the major version of the bash at path.
func bashMajor(t *testing.T, path string) int {
	t.Helper()
	out, err := exec.Command(path, "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	require.NoError(t, err)
	major, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	return major
}

// devBash returns the bash on PATH, which the dev shell provides at version 4 or later.
func devBash(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	require.GreaterOrEqual(t, bashMajor(t, bash), 4, "the dev shell provides bash 4 or later on PATH")
	return bash
}

// The cached environment script uses the bash 4 case terminator `;&`. macOS's /bin/bash 3.2 stopped sourcing it at
// that line without an error, so the rest of the environment and the shellHook were skipped. d re-runs itself under the
// dev shell's bash, and stops with an error when the dev shell has no bash 4 either.
func TestIssue857_OldBashSourcesTheWholeEnvironment(t *testing.T) {
	bash := devBash(t)
	root := devshellCheckout(t, `export PATH="$PWD/devbin"
f () {
    case "$1" in
        a) echo a ;&
        b) echo b ;;
    esac
}
export DEVSHELL_WHOLE=yes
`)
	devbin := filepath.Join(root, "devbin")
	require.NoError(t, os.MkdirAll(devbin, 0o755))
	require.NoError(t, os.Symlink(bash, filepath.Join(devbin, "bash")))
	d := filepath.Join(root, "scripts", "agent", "d")
	run := func(sh string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, sh, d, "sh", "-c", `printf %s "$DEVSHELL_WHOLE"`)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "FUNCD_DEVSHELL_KEY=")
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		require.NoError(t, ctx.Err(), "d under %s must not re-run itself forever", sh)
		return string(out), err
	}

	for _, sh := range []string{"/bin/bash", bash} {
		out, err := run(sh)
		require.NoError(t, err, "%s: %s", sh, out)
		require.Equal(t, "yes", out, "d under %s must source the whole environment", sh)
	}

	if bashMajor(t, "/bin/bash") < 4 {
		require.NoError(t, os.Remove(filepath.Join(devbin, "bash")))
		require.NoError(t, os.Symlink("/bin/bash", filepath.Join(devbin, "bash")))
		out, err := run("/bin/bash")
		require.Error(t, err, "%s", out)
		require.Contains(t, out, "bash 4 or later")
	}
}

// The cached environment script creates a nix-shell.XXXXXX directory and points TMPDIR at it. d ends with exec, so no
// exit trap removed it and every call left one behind. d removes the empty directory and restores TMPDIR.
func TestIssue856_CallLeavesNoTempDir(t *testing.T) {
	bash := devBash(t)
	root := devshellCheckout(t, `export NIX_BUILD_TOP="$(mktemp -d -t nix-shell.XXXXXX)"
export TMP="$NIX_BUILD_TOP"
export TMPDIR="$NIX_BUILD_TOP"
export TEMP="$NIX_BUILD_TOP"
export TEMPDIR="$NIX_BUILD_TOP"
`)
	d := filepath.Join(root, "scripts", "agent", "d")
	tmp := t.TempDir()
	env := []string{"TMPDIR=" + tmp, "FUNCD_DEVSHELL_KEY="}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains([]string{"TMPDIR", "TMP", "TEMP", "TEMPDIR", "NIX_BUILD_TOP", "FUNCD_DEVSHELL_KEY"}, name) {
			env = append(env, kv)
		}
	}

	var seen []string
	for range 3 {
		cmd := exec.Command(bash, d, "sh", "-c", `printf '%s|%s|%s' "$TMPDIR" "${TMP-unset}" "${NIX_BUILD_TOP-unset}"`)
		cmd.Dir = root
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		seen = append(seen, string(out))
	}
	left, err := filepath.Glob(filepath.Join(tmp, "nix-shell.*"))
	require.NoError(t, err)
	require.Empty(t, left, "d must remove the directory the environment script creates")
	require.Equal(t, slices.Repeat([]string{tmp + "|unset|unset"}, 3), seen, "d must keep TMPDIR")

	callers := filepath.Join(tmp, "nix-shell.callers")
	require.NoError(t, os.Mkdir(callers, 0o755))
	cmd := exec.Command(bash, filepath.Join(devshellCheckout(t, "true\n"), "scripts", "agent", "d"),
		"sh", "-c", `printf %s "$NIX_BUILD_TOP"`)
	cmd.Env = append(env, "NIX_BUILD_TOP="+callers)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.DirExists(t, callers, "d must remove only the directory the environment script creates")
	require.Equal(t, callers, string(out), "d must keep the caller's NIX_BUILD_TOP")
}
