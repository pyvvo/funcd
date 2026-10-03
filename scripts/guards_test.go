package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// exitCode runs cmd and returns its exit code and combined output.
func exitCode(t *testing.T, cmd *exec.Cmd) (int, string) {
	t.Helper()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	require.NoError(t, err, "%s", out)
	return 0, string(out)
}

// The PreToolUse hook refuses a git stash that changes the stash, and lets every other command run (issue #560).
func TestIssue560_StashHookBlocksStashWrites(t *testing.T) {
	hook, err := filepath.Abs(filepath.Join("agent", "no-stash.py"))
	require.NoError(t, err)
	run := func(tool, command string) (int, string) {
		cmd := exec.Command("python3", hook)
		cmd.Stdin = strings.NewReader(`{"tool_name":` + strconv.Quote(tool) + `,"tool_input":{"command":` + strconv.Quote(command) + `}}`)
		return exitCode(t, cmd)
	}
	for _, c := range []string{"git stash", "git stash pop", "git stash -u", "git -C wt stash push -m wip",
		"cd a && git stash apply", "scripts/agent/d git stash drop", "git --no-pager stash clear"} {
		code, out := run("Bash", c)
		require.Equal(t, 2, code, "%q must be blocked", c)
		require.Contains(t, out, "git show <base>:<file>", c)
	}
	for _, c := range []string{"git stash list", "git stash show -p", "git status", "git log --grep=stash", "echo stash"} {
		code, out := run("Bash", c)
		require.Equal(t, 0, code, "%q must run: %s", c, out)
	}
	code, _ := run("Edit", "git stash")
	require.Equal(t, 0, code, "only Bash commands are checked")

	settings, err := os.ReadFile(filepath.Join("..", ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(settings), "scripts/agent/no-stash.py", "the project settings wire the hook")
}

// Two lane runs on one host take turns; a nested recipe inherits the lock; a dead holder's lock is taken over; a
// running funcd VM stops a lane before it boots its own (issue #561).
func TestIssue561_LaneLockRunsOneLaneAtATime(t *testing.T) {
	lock, err := filepath.Abs("lane-lock.sh")
	require.NoError(t, err)
	tmp := t.TempDir()
	noVMs := filepath.Join(tmp, "limactl-none")
	require.NoError(t, os.WriteFile(noVMs, []byte("#!/bin/sh\necho 'colima Running'\n"), 0o755))
	oneVM := filepath.Join(tmp, "limactl-one")
	require.NoError(t, os.WriteFile(oneVM, []byte("#!/bin/sh\necho 'funcd-bench-kv Running'\n"), 0o755))
	lockDir := filepath.Join(tmp, "locks")
	logFile := filepath.Join(tmp, "log")
	holder := func(limactl, script string, extra ...string) *exec.Cmd {
		cmd := exec.Command("bash", "-c", script, "lane", lock, logFile)
		cmd.Env = append(os.Environ(), "FUNCD_LANE_LOCK_DIR="+lockDir, "FUNCD_LANE_LIMACTL="+limactl, "FUNCD_LANE_WAIT=30")
		cmd.Env = append(cmd.Env, extra...)
		return cmd
	}
	readLog := func() string {
		b, _ := os.ReadFile(logFile)
		return string(b)
	}

	first := holder(noVMs, `"$1" $$ || exit 1; echo "A $$" >> "$2"; sleep 5; echo A-done >> "$2"`)
	require.NoError(t, first.Start())
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Wait() }() // reap it as soon as it exits: a zombie still answers kill -0
	require.Eventually(t, func() bool { return strings.HasPrefix(readLog(), "A ") }, 10*time.Second, 50*time.Millisecond)
	holderPID := strings.Fields(readLog())[1]

	nested := holder(noVMs, `"$1" $$ || exit 1; echo nested >> "$2"`, "FUNCD_LANE_LOCK_HOLDER="+holderPID)
	code, out := exitCode(t, nested)
	require.Equal(t, 0, code, out)
	require.Contains(t, readLog(), "nested\n", "a recipe run by the holder does not wait for it")

	impatient := holder(noVMs, `"$1" $$ || exit 1; echo impatient >> "$2"`, "FUNCD_LANE_WAIT=1")
	code, out = exitCode(t, impatient)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gave up waiting")

	second := holder(noVMs, `"$1" $$ || exit 1; echo B >> "$2"`)
	code, out = exitCode(t, second)
	require.Equal(t, 0, code, out)
	require.NoError(t, <-firstDone)
	lines := strings.Split(strings.TrimSpace(readLog()), "\n")
	require.Equal(t, []string{"A-done", "B"}, lines[len(lines)-2:], "the second lane starts only after the first ends")

	dead := exec.Command("true")
	require.NoError(t, dead.Run())
	require.NoError(t, os.MkdirAll(filepath.Join(lockDir, "lane.lock"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lockDir, "lane.lock", "pid"), []byte(strconv.Itoa(dead.Process.Pid)+"\n"), 0o644))
	code, out = exitCode(t, holder(noVMs, `"$1" $$ || exit 1; echo after-dead >> "$2"`, "FUNCD_LANE_WAIT=5"))
	require.Equal(t, 0, code, out)
	require.Contains(t, readLog(), "after-dead\n", "the lock of a holder that has exited is taken over")

	code, out = exitCode(t, holder(oneVM, `"$1" $$ || exit 1; echo booted >> "$2"`))
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "funcd-bench-kv")
	require.NotContains(t, readLog(), "booted", "a lane does not boot beside a running funcd VM")
	require.NoDirExists(t, filepath.Join(lockDir, "lane.lock"), "the refused lane releases the lock it took")
}

// The gate's host check stops on a saturated port range and passes otherwise (issue #562).
func TestIssue562_HostCheckStopsOnTimeWait(t *testing.T) {
	check, err := filepath.Abs(filepath.Join("agent", "host-check.sh"))
	require.NoError(t, err)
	run := func(count string) (int, string) {
		cmd := exec.Command(check)
		cmd.Env = append(os.Environ(), "FUNCD_TIME_WAIT_COUNT="+count, "FUNCD_GATE_MAX_TIME_WAIT=8000")
		return exitCode(t, cmd)
	}
	code, out := run("9000")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "9000 sockets in TIME_WAIT")
	code, out = run("12")
	require.Equal(t, 0, code, out)

	gate, err := os.ReadFile(filepath.Join("agent", "gate.sh"))
	require.NoError(t, err)
	require.Contains(t, string(gate), "scripts/agent/host-check.sh", "gate.sh runs the host check before its steps")
}

// The wave check names a branch that conflicts with the ones merged before it, and gates the merged wave
// (issue #563).
func TestIssue563_WaveCheckReportsConflicts(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("agent", "wave-check.sh"))
	require.NoError(t, err)
	repo := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=wave", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=wave", "GIT_COMMITTER_EMAIL=")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644))
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	for _, b := range []struct{ name, file, body string }{{"one", "a.txt", "one\n"}, {"two", "a.txt", "two\n"}, {"three", "c.txt", "c\n"}} {
		git("switch", "-q", "-c", b.name, "main")
		write(b.file, b.body)
		git("add", ".")
		git("commit", "-q", "-m", b.name)
	}
	git("switch", "-q", "main")
	wave := func(gate string, branches ...string) (int, string) {
		cmd := exec.Command(script, branches...)
		cmd.Env = append(env, "WAVE_REPO="+repo, "WAVE_BASE=main", "WAVE_GATE="+gate, "TMPDIR="+t.TempDir())
		return exitCode(t, cmd)
	}

	code, out := wave("true", "one", "two", "three")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "merged    one")
	require.Contains(t, out, "CONFLICT  two against main + one: a.txt")
	require.Contains(t, out, "merged    three")
	require.NotContains(t, out, "gating", "a conflicting wave is not gated")

	code, out = wave("true", "one", "three")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "WAVE PASS")

	code, out = wave("false", "one", "three")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "WAVE FAIL")

	list := exec.Command("git", "-C", repo, "worktree", "list")
	listed, err := list.Output()
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(listed, []byte("\n")), "the scratch worktrees are removed: %s", listed)
}
