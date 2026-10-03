package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// The PreToolUse hook refuses a git stash that changes the stash, wherever git is the command, and lets every
// other command run, including ones that only mention git stash in their arguments (issue #560).
func TestIssue560_StashHookBlocksStashWrites(t *testing.T) {
	hook, err := filepath.Abs(filepath.Join("agent", "no-stash.py"))
	require.NoError(t, err)
	run := func(input string) (int, string) {
		cmd := exec.Command("python3", hook)
		cmd.Stdin = strings.NewReader(input)
		return exitCode(t, cmd)
	}
	bash := func(command string) string {
		return `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
	}
	for _, c := range []string{"git stash", "git stash pop", "git stash -u", "git -C wt stash push -m wip",
		"cd a && git stash apply", "scripts/agent/d git stash drop", "git --no-pager stash clear", "git -P stash pop",
		`git -C "$(git rev-parse --show-toplevel)" stash pop`, "git --work-tree . stash", `bash -c "git stash pop"`,
		"nix develop -c git stash", "FOO=1 git stash", "git status\ngit stash pop"} {
		code, out := run(bash(c))
		require.Equal(t, 2, code, "%q must be blocked", c)
		require.Contains(t, out, "git show <base>:<file>", c)
	}
	for _, c := range []string{"git stash list", "git stash show -p", "git status", "git log --grep=stash", "echo stash",
		"echo git stash", "grep -rn 'git stash' .", "rg 'git stash pop' docs/", "git grep 'git stash'",
		"git log -S 'git stash'", `git commit -m "fix: git stash pop lost changes"`, "gh pr create --body 'never git stash'"} {
		code, out := run(bash(c))
		require.Equal(t, 0, code, "%q must run: %s", c, out)
	}
	for _, input := range []string{`{"tool_name":"Edit","tool_input":{"command":"git stash"}}`, "[]", "not json"} {
		code, _ := run(input)
		require.Equal(t, 0, code, "%s is not a Bash command and must pass", input)
	}

	settings, err := os.ReadFile(filepath.Join("..", ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(settings), "scripts/agent/no-stash.py", "the project settings wire the hook")
}

// laneEnv is the environment of a lane-lock test: its own lock dir and a fake limactl.
type laneEnv struct {
	lock, dir, log string
	vms            func(string) string
}

func newLaneEnv(t *testing.T) laneEnv {
	t.Helper()
	tmp := t.TempDir()
	for name, out := range map[string]string{"none": "colima Running", "one": "funcd-bench-kv Running"} {
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "limactl-"+name), []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755))
	}
	lock, err := filepath.Abs("lane-lock.sh")
	require.NoError(t, err)
	return laneEnv{lock: lock, dir: filepath.Join(tmp, "locks"), log: filepath.Join(tmp, "log"),
		vms: func(name string) string { return filepath.Join(tmp, "limactl-"+name) }}
}

// holder is a lane recipe: it takes the lock with its own pid, then runs script ($2 is the log file).
func (e laneEnv) holder(script string, extra ...string) *exec.Cmd {
	cmd := exec.Command("bash", "-c", `"$1" $$ || exit 1; `+script, "lane", e.lock, e.log)
	cmd.Env = append(os.Environ(), "FUNCD_LANE_LOCK_DIR="+e.dir, "FUNCD_LANE_LIMACTL="+e.vms("none"),
		"FUNCD_LANE_WAIT=30", "FUNCD_LANE_POLL=0.05")
	cmd.Env = append(cmd.Env, extra...)
	return cmd
}

func (e laneEnv) readLog() string {
	b, _ := os.ReadFile(e.log)
	return string(b)
}

// staleLock leaves a lock naming a process identity that no longer runs.
func (e laneEnv) staleLock(t *testing.T, identity string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(e.dir, 0o755))
	require.NoError(t, os.Symlink(identity, filepath.Join(e.dir, "lane.lock")))
}

// Two lane runs on one host take turns; a recipe run by the holder inherits the lock; a bounded wait gives up; a
// running funcd VM stops a lane before it boots its own; the holder's exit frees the lock (issue #561).
func TestIssue561_LaneLockRunsOneLaneAtATime(t *testing.T) {
	e := newLaneEnv(t)
	first := e.holder(`echo "A $$" >> "$2"; sleep 3; echo A-done >> "$2"`)
	require.NoError(t, first.Start())
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Wait() }() // reap it as soon as it exits: a zombie still has its identity
	require.Eventually(t, func() bool { return strings.HasPrefix(e.readLog(), "A ") }, 10*time.Second, 20*time.Millisecond)
	holderPID := strings.Fields(e.readLog())[1]

	code, out := exitCode(t, e.holder(`echo nested >> "$2"`, "FUNCD_LANE_LOCK_HOLDER="+holderPID))
	require.Equal(t, 0, code, out)
	code, out = exitCode(t, e.holder(`echo impatient >> "$2"`, "FUNCD_LANE_WAIT=1"))
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "gave up waiting")
	code, out = exitCode(t, e.holder(`echo B >> "$2"`))
	require.Equal(t, 0, code, out)
	require.NoError(t, <-firstDone)
	require.Equal(t, []string{"A " + holderPID, "nested", "A-done", "B"}, strings.Split(strings.TrimSpace(e.readLog()), "\n"),
		"the nested recipe runs while its parent holds the lock, and the second lane only after the first ends")

	lock := filepath.Join(e.dir, "lane.lock")
	require.Eventually(t, func() bool { _, err := os.Lstat(lock); return os.IsNotExist(err) }, 5*time.Second, 50*time.Millisecond,
		"the watcher frees the lock once its holder exits")

	code, out = exitCode(t, e.holder(`echo booted >> "$2"`, "FUNCD_LANE_LIMACTL="+e.vms("one")))
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "limactl delete -f funcd-bench-kv")
	require.NotContains(t, e.readLog(), "booted", "a lane does not boot beside a running funcd VM")
	_, err := os.Lstat(lock)
	require.True(t, os.IsNotExist(err), "the refused lane releases the lock it took")
}

// Lanes that start together against the lock of a holder that has exited take turns: the takeover never lets two
// in at once, and a lock naming a live pid with another start time (a reused pid) counts as free (issue #561).
func TestIssue561_LaneLockTakeoverHasNoRace(t *testing.T) {
	for trial := range 10 {
		e := newLaneEnv(t)
		e.staleLock(t, "999999 Thu Jan  1 00:00:00 1970")
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, err := e.holder(`echo "in $$" >> "$2"; sleep 0.1; echo "out $$" >> "$2"`).CombinedOutput()
				if err != nil {
					errs[i] = fmt.Errorf("%w: %s", err, out)
				}
			}()
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err, "trial %d", trial)
		}
		lines := strings.Split(strings.TrimSpace(e.readLog()), "\n")
		require.Len(t, lines, 8, "trial %d: every lane ran: %v", trial, lines)
		for i := 0; i < len(lines); i += 2 {
			in, out := strings.Fields(lines[i]), strings.Fields(lines[i+1])
			require.Equal(t, "in", in[0], "trial %d: two lanes overlapped: %v", trial, lines)
			require.Equal(t, []string{"out", in[1]}, out, "trial %d: two lanes overlapped: %v", trial, lines)
		}
	}

	e := newLaneEnv(t)
	e.staleLock(t, strconv.Itoa(os.Getpid())+" Thu Jan  1 00:00:00 1970")
	code, out := exitCode(t, e.holder(`echo reused >> "$2"`, "FUNCD_LANE_WAIT=5"))
	require.Equal(t, 0, code, out)
	require.Contains(t, e.readLog(), "reused", "a live pid with another start time is not the holder")
}

// The gate's host check stops on a saturated port range, before the gate's first step, and passes otherwise
// (issue #562).
func TestIssue562_HostCheckStopsOnTimeWait(t *testing.T) {
	check, err := filepath.Abs(filepath.Join("agent", "host-check.sh"))
	require.NoError(t, err)
	run := func(name, count string) (int, string) {
		cmd := exec.Command(name)
		cmd.Env = append(os.Environ(), "FUNCD_TIME_WAIT_COUNT="+count, "FUNCD_GATE_MAX_TIME_WAIT=8000")
		return exitCode(t, cmd)
	}
	code, out := run(check, "9000")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "9000 sockets in TIME_WAIT")
	code, out = run(check, "12")
	require.Equal(t, 0, code, out)

	gate, err := filepath.Abs(filepath.Join("agent", "gate.sh"))
	require.NoError(t, err)
	code, out = run(gate, "9000")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "GATE FAIL")
	require.NotContains(t, out, "audit", "the gate stops before its first step")
}

// The wave check names a branch that conflicts with the ones merged before it and a branch that does not exist,
// refuses to run on stale refs, gates a clean wave, and keeps the gate's logs (issue #563).
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
	failingGate := filepath.Join(t.TempDir(), "gate")
	require.NoError(t, os.WriteFile(failingGate, []byte("#!/bin/sh\nmkdir -p .cache/gate && echo boom > .cache/gate/unit.log\nexit 1\n"), 0o755))
	wave := func(gate string, branches ...string) (int, string) {
		cmd := exec.Command(script, branches...)
		cmd.Env = append(env, "WAVE_REPO="+repo, "WAVE_BASE=main", "WAVE_GATE="+gate, "TMPDIR="+t.TempDir())
		return exitCode(t, cmd)
	}

	code, out := wave("true", "one", "two", "three", "four")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "merged    one")
	require.Contains(t, out, "CONFLICT  two against main + one: a.txt")
	require.Contains(t, out, "merged    three")
	require.Contains(t, out, "MISSING   four")
	require.NotContains(t, out, "gating", "a conflicting wave is not gated")

	code, out = wave("true", "one", "three")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "WAVE PASS")

	code, out = wave(failingGate, "one", "three")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "logs in .cache/wave-gate/")
	logged, err := os.ReadFile(filepath.Join(repo, ".cache", "wave-gate", "unit.log"))
	require.NoError(t, err, "the failing gate's logs outlive the scratch worktree")
	require.Equal(t, "boom\n", string(logged))

	list := exec.Command("git", "-C", repo, "worktree", "list")
	listed, err := list.Output()
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(listed, []byte("\n")), "the scratch worktrees are removed: %s", listed)

	git("remote", "add", "origin", filepath.Join(t.TempDir(), "gone"))
	code, out = wave("true", "one")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "git fetch origin failed")
}
