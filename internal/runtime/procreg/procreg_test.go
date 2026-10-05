package procreg_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
	return cmd, procreg.Entry{PID: cmd.Process.Pid, PGID: cmd.Process.Pid, StartTime: st}
}

// alive reports whether pid still runs (a zombie child does not count).
func alive(pid int) bool {
	return procreg.Owned(procreg.Entry{PID: pid, StartTime: mustStart(pid), Token: ""})
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

// Owned needs both the saved start time and the token in argv; a dead pid is never ours.
func TestOwned(t *testing.T) {
	_, e := startChild(t, "funcd-test", "--funcd-instance=r10")
	e.Token = "--funcd-instance=r10"
	require.True(t, procreg.Owned(e), "a live child with its token")

	stale := e
	stale.StartTime++
	require.False(t, procreg.Owned(stale), "another start time")

	_, reused := startChild(t)
	reused.Token = "--funcd-instance=r10"
	require.False(t, procreg.Owned(reused), "a reused pid without the token")

	dead := exec.Command("true")
	require.NoError(t, dead.Run())
	require.False(t, procreg.Owned(procreg.Entry{PID: dead.Process.Pid, StartTime: e.StartTime, Token: e.Token}), "a dead pid")
}

// scenario: reused-pid-never-killed — an entry whose pid now has another start time, or an argv without the
// entry's token, is never signalled at open, and the entry is cleared.
func TestScenarioReusedPidNeverKilled(t *testing.T) {
	dir := t.TempDir()
	noToken, e1 := startChild(t)
	e1.ID, e1.Token = "default/f/r1", "--funcd-instance=default/f/r1"
	otherStart, e2 := startChild(t, "funcd-test", "--funcd-instance=default/f/r2")
	e2.ID, e2.Token = "default/f/r2", "--funcd-instance=default/f/r2"
	e2.StartTime++
	seed(t, dir, "workers", e1, e2)

	r, err := procreg.Open(dir, "workers")
	require.NoError(t, err)
	killed, err := r.Reap(context.Background(), 200*time.Millisecond)
	require.NoError(t, err)
	require.Zero(t, killed)
	require.True(t, alive(noToken.Process.Pid), "a pid without the token is not signalled")
	require.True(t, alive(otherStart.Process.Pid), "a pid with another start time is not signalled")
	require.Empty(t, savedEntries(t, dir, "workers"), "the entries are cleared")
	require.NoError(t, r.Close())
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
	require.Eventually(t, func() bool { return !alive(owned.Process.Pid) }, 5*time.Second, 20*time.Millisecond)
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
	seed(t, dir, "workers", procreg.Entry{ID: "default/f/r1", PID: leader, PGID: leader, StartTime: mustStart(leader), Token: token})
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
