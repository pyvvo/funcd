package scripts_test

import (
	"bytes"
	"context"
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

// stashHook runs the PreToolUse stash hook on a tool event.
func stashHook(t *testing.T, event string) (int, string) {
	t.Helper()
	cmd := exec.Command("python3", filepath.Join("agent", "no-stash.py"))
	cmd.Stdin = strings.NewReader(event)
	return exitCode(t, cmd)
}

// bash is the hook's event for a Bash command.
func bash(command string) string {
	return `{"tool_name":"Bash","tool_input":{"command":` + strconv.Quote(command) + `}}`
}

// The PreToolUse hook refuses a git stash that changes the stash, wherever git is the command, and lets every
// other command run, including ones that only mention git stash in their arguments (issue #560).
func TestIssue560_StashHookBlocksStashWrites(t *testing.T) {
	for _, c := range []string{"git stash", "git stash pop", "git stash -u", "git -C wt stash push -m wip",
		"cd a && git stash apply", "scripts/agent/d git stash drop", "git --no-pager stash clear", "git -P stash pop",
		`git -C "$(git rev-parse --show-toplevel)" stash pop`, "git --work-tree . stash", `bash -c "git stash pop"`,
		"nix develop -c git stash", "FOO=1 git stash", "git status\ngit stash pop",
		"# revert check\ngit stash\ngo test ./x/\ngit stash pop", "cd wt  # the worktree\ngit stash pop",
		"nix run nixpkgs#hello; git stash pop", "if git diff --quiet; then git stash pop; fi",
		"for f in a b; do git stash push $f; done", "{ git stash pop; }", "! git stash pop",
		`out="$(git stash pop 2>&1)"`, `echo "$(git stash pop)"`, "echo `git stash pop`", "bash -lc 'git stash pop'",
		"sudo -u me git stash pop", "nice -n 10 git stash pop", "timeout 60 git stash pop", "xargs git stash drop"} {
		code, out := stashHook(t, bash(c))
		require.Equal(t, 2, code, "%q must be blocked", c)
		require.Contains(t, out, "git show <base>:<file>", c)
	}
	for _, c := range []string{"git stash list", "git stash show -p", "git status", "git log --grep=stash", "echo stash",
		"echo git stash", "grep -rn 'git stash' .", "rg 'git stash pop' docs/", "git grep 'git stash'",
		"git log -S 'git stash'", `git commit -m "fix: git stash pop lost changes"`, "gh pr create --body 'never git stash'"} {
		code, out := stashHook(t, bash(c))
		require.Equal(t, 0, code, "%q must run: %s", c, out)
	}
	for _, input := range []string{`{"tool_name":"Edit","tool_input":{"command":"git stash"}}`, "[]", "not json"} {
		code, _ := stashHook(t, input)
		require.Equal(t, 0, code, "%s is not a Bash command and must pass", input)
	}

	settings, err := os.ReadFile(filepath.Join("..", ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(settings), "scripts/agent/no-stash.py", "the project settings wire the hook")
}

// The hook reads a quote that spans lines and a heredoc as the shell does: a commit message or a heredoc that only
// mentions git stash runs, a heredoc that feeds a shell does not, nor a function body or watch's script; a
// wrapper's arguments are not its command (issue #576).
func TestIssue576_StashHookReadsHeredocsAndWrappers(t *testing.T) {
	for _, c := range []string{"function f { git stash pop; }", "watch 'git stash pop'", "watch -n 5 git stash drop",
		"bash <<'EOF'\ngit stash pop\nEOF", "cat <<'EOF' | sh\ngit stash pop\nEOF", "cat <<EOF\n$(git stash pop)\nEOF",
		"echo \"two\nlines\"\ngit stash pop", "grep -n '<<EOF' f\ngit stash pop"} {
		code, out := stashHook(t, bash(c))
		require.Equal(t, 2, code, "%q must be blocked: %s", c, out)
	}
	for _, c := range []string{
		"git commit -m \"$(cat <<'EOF'\nfix(agents): refuse `git stash pop`\n\ngit stash pop lost a fix.\nEOF\n)\"",
		"git commit -F - <<'EOF'\ngit stash drop is refused\nEOF", "cat > notes.txt <<'EOF'\ndon't run git stash pop\nEOF\ngit status",
		"timeout 5 echo git stash", "watch -n 5 git stash list"} {
		code, out := stashHook(t, bash(c))
		require.Equal(t, 0, code, "%q must run: %s", c, out)
	}
}

// laneEnv is the environment of a lane-lock test: its own lock dir and a fake limactl.
type laneEnv struct {
	lock, dir, log string
	vms            func(string) string
}

// newLaneEnv runs a copy of the script from its own temp dir, so the test can tell its watchers apart and fails when
// one outlives the test.
func newLaneEnv(t *testing.T) laneEnv {
	t.Helper()
	tmp := t.TempDir()
	for name, out := range map[string]string{"none": "colima Running", "one": "funcd-bench-kv Running"} {
		require.NoError(t, os.WriteFile(filepath.Join(tmp, "limactl-"+name), []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755))
	}
	src, err := os.ReadFile("lane-lock.sh")
	require.NoError(t, err)
	lock := filepath.Join(tmp, "lane-lock.sh")
	require.NoError(t, os.WriteFile(lock, src, 0o755))
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return exec.Command("pgrep", "-f", lock).Run() != nil }, 10*time.Second,
			50*time.Millisecond, "a lock watcher outlived its holder")
	})
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
	require.NoError(t, os.Symlink(identity, filepath.Join(e.dir, "lane.holder")))
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

	lock := filepath.Join(e.dir, "lane.holder")
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

// The lock's holder identity does not depend on the caller's time zone, its watcher ends even when the lock dir is
// deleted under it, and a directory at the lock's path is refused rather than slipped past (issue #561).
func TestIssue561_LaneLockSurvivesItsEnvironment(t *testing.T) {
	e := newLaneEnv(t)
	first := e.holder(`echo A >> "$2"; sleep 2; echo A-done >> "$2"`, "TZ=UTC")
	require.NoError(t, first.Start())
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Wait() }()
	require.Eventually(t, func() bool { return strings.HasPrefix(e.readLog(), "A") }, 10*time.Second, 20*time.Millisecond)
	code, out := exitCode(t, e.holder(`echo tokyo >> "$2"`, "TZ=Asia/Tokyo", "FUNCD_LANE_WAIT=1"))
	require.Equal(t, 1, code, "a holder in another time zone is still the holder: %s", out)
	require.NotContains(t, e.readLog(), "tokyo")
	require.NoError(t, os.RemoveAll(e.dir), "the lock dir vanishes under a live holder")
	require.NoError(t, <-firstDone)

	e = newLaneEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(e.dir, "lane.holder"), 0o755))
	code, out = exitCode(t, e.holder(`echo slipped >> "$2"`))
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "is a directory")
	require.NotContains(t, e.readLog(), "slipped")
}

// The lane lock refuses a directory at its path without leaving a link inside it, takes over a dead holder's lock
// even when the lock dir vanishes during the takeover, and tells a held guard from one it cannot create (issue #576).
func TestIssue576_LaneLockReportsItsGuard(t *testing.T) {
	const dead = "999999 Thu Jan  1 00:00:00 1970"
	t.Run("directory at the lock path", func(t *testing.T) {
		e := newLaneEnv(t)
		dir := filepath.Join(e.dir, "lane.holder")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		code, out := exitCode(t, e.holder(`echo slipped >> "$2"`))
		require.Equal(t, 1, code, out)
		require.Contains(t, out, "is a directory")
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries, "no link is left inside the directory")
	})
	t.Run("lock dir vanishes during a takeover", func(t *testing.T) {
		e := newLaneEnv(t)
		e.staleLock(t, dead)
		rm, err := exec.LookPath("rm")
		require.NoError(t, err)
		bin := t.TempDir()
		removeDir := "#!/bin/sh\nexec '" + rm + "' -rf \"${2%/*}\"\n" // rm -f <lock> removes the whole lock dir
		require.NoError(t, os.WriteFile(filepath.Join(bin, "rm"), []byte(removeDir), 0o755))
		code, out := exitCode(t, e.holder(`echo took >> "$2"`, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH")))
		require.Equal(t, 0, code, out)
		require.Equal(t, "took\n", e.readLog())
	})
	t.Run("held guard", func(t *testing.T) {
		e := newLaneEnv(t)
		e.staleLock(t, dead)
		require.NoError(t, os.Mkdir(filepath.Join(e.dir, "lane.holder.guard"), 0o755))
		code, out := exitCode(t, e.holder(`echo ran >> "$2"`, "FUNCD_LANE_GUARD_WAIT=1"))
		require.Equal(t, 1, code, out)
		require.Contains(t, out, "has been held for two minutes")
		require.Empty(t, e.readLog())
	})
	t.Run("guard that cannot be created", func(t *testing.T) {
		e := newLaneEnv(t)
		e.staleLock(t, dead)
		require.NoError(t, os.WriteFile(filepath.Join(e.dir, "lane.holder.guard"), nil, 0o644))
		code, out := exitCode(t, e.holder(`echo ran >> "$2"`, "FUNCD_LANE_GUARD_WAIT=1"))
		require.Equal(t, 1, code, out)
		require.Contains(t, out, "could not take the guard")
		require.NotContains(t, out, "has been held")
		require.Empty(t, e.readLog())
	})
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

// linux-exec.sh refuses a checkout outside $HOME, which the funcd-linux-tests VM does not mount: the VM would see no
// package dir and no testdata. --check, which the gate runs before its linux-tests step, and --boot, which
// `just test-linux` runs first, refuse it the same way, so nothing waits for the lane lock or boots the VM in vain.
func TestLinuxExecRefusesACheckoutOutsideHome(t *testing.T) {
	script, err := filepath.Abs("linux-exec.sh")
	require.NoError(t, err)
	fake := t.TempDir() // a limactl that fails, so a refusal that regresses never reaches the real VM
	require.NoError(t, os.WriteFile(filepath.Join(fake, "limactl"), []byte("#!/bin/sh\nexit 99\n"), 0o755))
	for _, args := range [][]string{{"pkg.test", "-test.v"}, {"--check"}, {"--boot"}} {
		cmd := exec.Command(script, args...)
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
		code, out := exitCode(t, cmd)
		require.Equal(t, 2, code, "%v: %s", args, out)
		require.Contains(t, out, "is outside $HOME", args)
	}
}

// linuxExecEnv is the environment of a linux-exec.sh test that gets past the checkout check: the checkout as $HOME, a
// run lock of its own, a fake go whose toolchain module is in a temp module cache, limactl as the fake limactl, and a
// sudo that only says it ran, so a fake limactl that runs its command here never reaches the host's sudo.
func linuxExecEnv(t *testing.T, limactl string) []string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	modcache := t.TempDir()
	toolchain := filepath.Join(modcache, "golang.org", "toolchain@v0.0.1-go1.0.linux-arm64", "bin")
	require.NoError(t, os.MkdirAll(toolchain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(toolchain, "go"), nil, 0o755))
	fake := t.TempDir()
	for name, body := range map[string]string{
		"limactl": limactl,
		"go":      `case $1 in version) printf 'x: go1.0\n\tbuild\tGOARCH=arm64\n' ;; env) echo "` + modcache + `" ;; esac`,
		"sudo":    `echo "sudo $*"`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(fake, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	return append(os.Environ(), "HOME="+root, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FUNCD_LINUX_TESTS_LOCK="+filepath.Join(t.TempDir(), "lock"))
}

// testBinary is an empty stand-in for a test binary.
func testBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pkg.test")
	require.NoError(t, os.WriteFile(bin, nil, 0o755))
	return bin
}

// sudo starts the test binary with a fresh environment, so linux-exec.sh passes each LINUX_EXEC_<NAME>=<value> of its
// own as <NAME>=<value>: `just test-linux-e2e` sets CI and GOPROXY so. The script runs under the bash on PATH, and the
// dev shell's bash has no compgen: a forwarding built on it passed nothing, and the suite still passed.
func TestLinuxExecPassesItsLinuxExecVariables(t *testing.T) {
	script, err := filepath.Abs("linux-exec.sh")
	require.NoError(t, err)
	cmd := exec.Command(script, testBinary(t), "-test.v") // limactl shell prints its arguments: the VM is never reached
	cmd.Env = append(linuxExecEnv(t, `if [ "$1" = list ]; then echo Running; else printf '%s\n' "$@"; fi`),
		"LINUX_EXEC_CI=true", "LINUX_EXEC_GOPROXY=file:///a dir/download")
	code, out := exitCode(t, cmd)
	require.Equal(t, 0, code, out)
	args := strings.Split(strings.TrimSpace(out), "\n")
	require.Contains(t, args, "CI=true")
	require.Contains(t, args, "GOPROXY=file:///a dir/download")
	require.NotContains(t, out, "LINUX_EXEC_")
	require.Equal(t, "-test.v", args[len(args)-1])
}

// One run at a time in the VM: its runs share runner's home directory and /tmp, and when a gate's linux-tests step
// overlapped an e2e run, tests of both failed. A second --run waits for the first to finish, and a run nested in one
// (a test binary that go test starts under --run) goes on at once instead of waiting for its own run.
func TestLinuxExecRunsOneRunAtATime(t *testing.T) {
	script, err := filepath.Abs("linux-exec.sh")
	require.NoError(t, err)
	env, dir := linuxExecEnv(t, "echo Running"), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, script, append([]string{"--run"}, args...)...)
		cmd.Env = env
		return cmd
	}
	first := run("sh", "-c", `touch "$1/started"; sleep 2; touch "$1/done"`, "sh", dir)
	require.NoError(t, first.Start())
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "started"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	out, err := run("sh", "-c", `test -e "$1/done" && "$2" --run true`, "sh", dir, script).CombinedOutput()
	require.NoError(t, err, "the second run started before the first ended, or its nested run waited: %s", out)
	require.Contains(t, string(out), "waiting for the run of pid")
	require.NoError(t, first.Wait())
}

// A VM that has not finished booting is refused with the reason, instead of running a test as a runner user that its
// provisioning may not have created yet: a run once started while the VM was being recreated.
func TestLinuxExecRefusesAVMStillBooting(t *testing.T) {
	script, err := filepath.Abs("linux-exec.sh")
	require.NoError(t, err)
	if _, err := os.Stat("/run/lima-boot-done"); err == nil {
		t.Skip("this host is a Lima VM that has finished booting")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, script, testBinary(t), "-test.v") // limactl shell runs the command here
	cmd.Env = linuxExecEnv(t, `if [ "$1" = list ]; then echo Running; else shift 4; exec "$@"; fi`)
	code, out := exitCode(t, cmd)
	require.Equal(t, 125, code, out)
	require.Contains(t, out, "the funcd-linux-tests VM has not finished booting")
}
