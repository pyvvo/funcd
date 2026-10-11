package procreg_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// startChild starts sh in its own process group with extra argv elements; the trailing command keeps sh from
// exec'ing sleep, so the argv stays sh's. cmd.Start returns once the exec closes the close-on-exec fds, before the
// kernel fills in the new argv (#679), so startChild waits for sh's first output: from then on argv is readable.
func startChild(t *testing.T, args ...string) (*exec.Cmd, procreg.Entry) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", "echo; sleep 30; true"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_, err = out.Read(make([]byte, 1)) // before Wait, which closes the pipe
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	require.NoError(t, err, "sh runs")
	st, err := procreg.StartTime(cmd.Process.Pid)
	require.NoError(t, err)
	return cmd, procreg.Entry{PID: cmd.Process.Pid, PGID: cmd.Process.Pid, StartTime: st, BootID: mustBootID(t)}
}

// foreignBootID is a boot ID no host has: an entry saved in another boot.
const foreignBootID = "00000000-0000-4000-8000-000000000000"

// alive reports whether pid still runs (a zombie child does not count).
func alive(t *testing.T, pid int) bool {
	return procreg.Alive(procreg.Entry{PID: pid, StartTime: mustStart(pid), BootID: mustBootID(t)})
}

func mustBootID(t *testing.T) string {
	t.Helper()
	id, err := procreg.BootID()
	require.NoError(t, err)
	return id
}

func mustStart(pid int) uint64 {
	st, _ := procreg.StartTime(pid)
	return st
}

func savedEntries(t *testing.T, dir, name string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	require.NoError(t, err)
	var out []procreg.Entry
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// seed writes a registry file as a crashed run leaves it.
func seed(t *testing.T, dir, name string, entries ...procreg.Entry) {
	t.Helper()
	r, err := procreg.Open(dir, name)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, r.Put(e))
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), b, 0o600))
}

// Every write replaces the file whole through a temp file, and a reopen reads back what was saved.
func TestRegistryWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	require.NoError(t, r.Put(procreg.Entry{ID: "b", PID: 2, Token: "--funcd-instance=b"}))
	require.NoError(t, r.Put(procreg.Entry{ID: "a", PID: 1, Token: "--funcd-instance=a", Files: []string{"/x"}}))
	got := savedEntries(t, dir, "workers")
	require.Equal(t, []string{"a", "b"}, []string{got[0].ID, got[1].ID})
	require.Equal(t, []string{"/x"}, got[0].Files)
	require.NoFileExists(t, filepath.Join(dir, "workers.json.tmp"))

	require.NoError(t, r.Delete("a"))
	require.Len(t, savedEntries(t, dir, "workers"), 1)
	require.NoError(t, r.Close())
	require.Empty(t, savedEntries(t, dir, "workers"), "Close empties the registry")
	require.NoError(t, r.Close(), "a second Close does nothing")
}

// The lock gives one registry one owner; the driver's and devengine's registries in one dir never contend.
func TestRegistryLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	_, err = procreg.Open(dir, "workers")
	require.Equal(t, fault.Conflict, fault.KindOf(err))

	e, err := procreg.Open(dir, "engines")
	require.NoError(t, err, "a separate lock per registry")
	require.NoError(t, e.Close())

	require.NoError(t, r.Close())
	r, err = procreg.Open(dir, "workers")
	require.NoError(t, err, "Close releases the lock")
	require.NoError(t, r.Close())
}

// Issue #932: Close releases the lock while a child forked meanwhile, as the driver forks its workers, still holds a
// copy of the descriptor until it execs, so the next Open in this process is not refused; a close-on-exec copy of the
// descriptor stands in for the child's.
func TestIssue932_CloseReleasesLockWhileChildHoldsDescriptor(t *testing.T) {
	dir := t.TempDir()
	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	child, err := unix.FcntlInt(procreg.LockFile(r).Fd(), unix.F_DUPFD_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(child) })
	require.NoError(t, r.Close())
	r, err = procreg.Open(dir, "workers")
	require.NoError(t, err)
	require.NoError(t, r.Close())
}

// Owned needs the saved start time and, on Linux, the saved boot; the argv is read only for a Linux entry without a
// boot ID; a dead pid is never ours.
func TestOwned(t *testing.T) {
	linux := runtime.GOOS == "linux"
	_, e := startChild(t, "funcd-test", "--funcd-instance=r10")
	e.Token = "--funcd-instance=r10"
	require.True(t, procreg.Owned(e), "a live child")

	_, retitled := startChild(t)
	retitled.Token = e.Token
	require.True(t, procreg.Owned(retitled), "a live child whose argv lacks the token")

	stale := e
	stale.StartTime++
	require.False(t, procreg.Owned(stale), "another start time")

	other := e
	other.BootID = foreignBootID
	require.Equal(t, !linux, procreg.Owned(other), "another boot: never ours on Linux; macOS start times differ across boots")

	legacy := e
	legacy.BootID = ""
	require.True(t, procreg.Owned(legacy), "a legacy entry whose argv holds the token")
	legacyNoToken := retitled
	legacyNoToken.BootID = ""
	require.Equal(t, !linux, procreg.Owned(legacyNoToken), "a legacy entry without the token: ours only on macOS")

	dead := exec.Command("true")
	require.NoError(t, dead.Run())
	require.False(t, procreg.Owned(procreg.Entry{PID: dead.Process.Pid, StartTime: e.StartTime, BootID: e.BootID, Token: e.Token}), "a dead pid")
}

// scenario: reused-pid-never-killed — an entry whose pid now belongs to a process with another start time is never
// signalled at open, and the entry is cleared.
func TestScenarioReusedPidNeverKilled(t *testing.T) {
	dir := t.TempDir()
	otherStart, e := startChild(t, "funcd-test", "--funcd-instance=default/f/r1")
	e.ID, e.Token = "default/f/r1", "--funcd-instance=default/f/r1"
	e.StartTime++
	seed(t, dir, "workers", e)

	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	killed, err := r.Reap(context.Background(), 200*time.Millisecond)
	require.NoError(t, err)
	require.Zero(t, killed)
	require.True(t, alive(t, otherStart.Process.Pid), "a pid with another start time is not signalled")
	require.Empty(t, savedEntries(t, dir, "workers"), "the entry is cleared")
	require.NoError(t, r.Close())
}

// scenario: legacy-entry-rule — an entry without a boot ID, written by an earlier release, whose live pid has the saved
// start time is reaped on Linux only if its argv holds the token, and on macOS whether or not it does.
func TestScenarioLegacyEntryRule(t *testing.T) {
	linux := runtime.GOOS == "linux"
	dir := t.TempDir()
	withToken, e1 := startChild(t, "funcd-test", "--funcd-instance=default/f/r1")
	e1.ID, e1.Token, e1.BootID = "default/f/r1", "--funcd-instance=default/f/r1", ""
	noToken, e2 := startChild(t)
	e2.ID, e2.Token, e2.BootID = "default/f/r2", "--funcd-instance=default/f/r2", ""
	seed(t, dir, "workers", e1, e2)

	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	killed, err := r.Reap(context.Background(), 2*time.Second)
	require.NoError(t, err)
	want := 2
	if linux {
		want = 1
	}
	require.Equal(t, want, killed)
	require.Eventually(t, func() bool { return !alive(t, withToken.Process.Pid) }, 5*time.Second, 20*time.Millisecond,
		"a legacy entry whose argv holds the token is reaped")
	if linux {
		require.True(t, alive(t, noToken.Process.Pid), "a legacy Linux entry without the token is not signalled")
	} else {
		require.Eventually(t, func() bool { return !alive(t, noToken.Process.Pid) }, 5*time.Second, 20*time.Millisecond,
			"a legacy macOS entry is reaped without the token")
	}
	require.Empty(t, savedEntries(t, dir, "workers"))
	require.NoError(t, r.Close())
}

// scenario: zombie-leader-counts-as-gone — an owned entry whose leader exits on SIGTERM but stays a zombie, because its
// parent does not wait for it, counts as gone: Reap does not wait out the grace, and the group member that ignores
// SIGTERM gets SIGKILL.
func TestScenarioZombieLeaderCountsAsGone(t *testing.T) {
	const token = "--funcd-instance=default/f/r1"
	cmd := exec.Command("sh", "-c", `sh -c 'trap "" TERM; echo $$; exec sleep 300' & wait; true`, "sh", token)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	line, err := bufio.NewReader(out).ReadString('\n')
	member, aerr := strconv.Atoi(strings.TrimSpace(line))
	leader := cmd.Process.Pid
	t.Cleanup(func() {
		if aerr == nil {
			_ = syscall.Kill(member, syscall.SIGKILL)
		}
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	require.NoError(t, err)
	require.NoError(t, aerr)

	dir := t.TempDir()
	e := procreg.Entry{ID: "default/f/r1", PID: leader, PGID: leader, StartTime: mustStart(leader), BootID: mustBootID(t), Token: token}
	seed(t, dir, "workers", e)
	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	begin := time.Now()
	killed, err := r.Reap(context.Background(), 2*time.Second)
	took := time.Since(begin)
	require.NoError(t, err)
	require.Equal(t, 1, killed)
	require.Less(t, took, time.Second, "Reap waited on a zombie leader")
	require.False(t, procreg.Alive(e), "the unwaited leader is a zombie")
	require.Eventually(t, func() bool { return syscall.Kill(member, 0) != nil }, 2*time.Second, 20*time.Millisecond,
		"pid %d of the zombie leader's group still runs after the reap", member)
}

// scenario: temp-files-removed — the temp files a crashed run named in the registry are gone after open, whether
// or not their process is still ours, and an owned process is ended.
func TestScenarioTempFilesRemoved(t *testing.T) {
	dir, tmp := t.TempDir(), t.TempDir()
	var files []string
	for _, f := range []string{"funcd-worker-1.log", "funcd-worker-1.log.port", "funcd-worker-2.port"} {
		files = append(files, filepath.Join(tmp, f))
		require.NoError(t, os.WriteFile(files[len(files)-1], []byte("x"), 0o600))
	}
	owned, e1 := startChild(t, "funcd-test", "--funcd-instance=default/f/r1")
	e1.ID, e1.Token, e1.Files = "default/f/r1", "--funcd-instance=default/f/r1", files[:2]
	e2 := procreg.Entry{ID: "default/f/r2", PID: 1 << 22, PGID: 1 << 22, Token: "--funcd-instance=default/f/r2", Files: files[2:]}
	seed(t, dir, "workers", e1, e2)

	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	killed, err := r.Reap(context.Background(), 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, killed)
	for _, f := range files {
		require.NoFileExists(t, f)
	}
	require.Eventually(t, func() bool { return !alive(t, owned.Process.Pid) }, 5*time.Second, 20*time.Millisecond)
	require.Empty(t, savedEntries(t, dir, "workers"))
	require.NoError(t, r.Close())
}

// Issue 725: a worker's group leader that exits on SIGTERM leaves no SIGKILL to the rest of its group, so a
// subprocess that ignores SIGTERM outlived the reap.
func TestIssue725_ReapKillsGroupAfterLeaderExits(t *testing.T) {
	const token = "--funcd-instance=default/f/r1"
	cmd := exec.Command("sh", "-c", `sh -c 'trap "" TERM; echo $$; exec sleep 300' & wait; true`, "sh", token)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	line, err := bufio.NewReader(out).ReadString('\n')
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	child, aerr := strconv.Atoi(strings.TrimSpace(line))
	t.Cleanup(func() {
		if aerr == nil {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	require.NoError(t, err)
	require.NoError(t, aerr)
	leader := cmd.Process.Pid
	pgid, err := syscall.Getpgid(child)
	require.NoError(t, err)
	require.Equal(t, leader, pgid, "the subprocess is in the worker's process group")

	dir := t.TempDir()
	seed(t, dir, "workers", procreg.Entry{ID: "default/f/r1", PID: leader, PGID: leader, StartTime: mustStart(leader), BootID: mustBootID(t), Token: token})
	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	killed, err := r.Reap(context.Background(), 500*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 1, killed)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the leader survived the reap")
	}
	require.Eventually(t, func() bool { return syscall.Kill(child, 0) != nil }, 2*time.Second, 20*time.Millisecond,
		"pid %d of the reaped worker's process group still runs after the reap", child)
}

// scenario: retitled-worker-reaped — a crashed run's Node worker that set process.title, so its argv no longer holds
// the token, is still reaped at open (#730).
func TestScenarioRetitledWorkerReaped(t *testing.T) {
	const token = "--funcd-instance=default/f/r1"
	node, err := exec.LookPath("node")
	require.NoError(t, err, "node is on PATH")
	cmd := exec.Command(node, "-e", `process.title="my-function"; console.log("up"); setInterval(()=>{},1000)`, "--", token)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_, err = bufio.NewReader(out).ReadString('\n')
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	require.NoError(t, err, "node runs")
	pid := cmd.Process.Pid

	dir := t.TempDir()
	seed(t, dir, "workers", procreg.Entry{ID: "default/f/r1", PID: pid, PGID: pid, StartTime: mustStart(pid), BootID: mustBootID(t), Token: token})
	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	killed, err := r.Reap(context.Background(), 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, killed)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the retitled worker survived the reap")
	}
}
