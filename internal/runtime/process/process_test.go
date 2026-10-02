package process_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/runtimecontract"
)

// scenario: driver-conformance-parity (and the lifecycle/logs/stop/not-found/exec
// scenarios it drives) — the process driver satisfies the shared runtime contract.
func TestProcessDriverContract(t *testing.T) {
	t.Parallel()
	runtimecontract.RunContract(t, func(t *testing.T) runtime.Runtime {
		t.Helper()
		return process.New()
	})
}

// Create keeps rejecting an ID whose instance is live (Created or Running); only an exited one is replaced
// (ADR-0142).
func TestCreateRejectsLiveInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	spec := runtime.WorkerSpec{Namespace: "default", Name: "live", Command: []string{"sleep", "30"}, LogPath: filepath.Join(t.TempDir(), "w.log")}

	inst, err := rt.Create(ctx, spec)
	require.NoError(t, err)
	_, err = rt.Create(ctx, spec)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a Created instance is live")

	require.NoError(t, rt.Start(ctx, inst.ID))
	_, err = rt.Create(ctx, spec)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a Running instance is live")
	require.NoError(t, rt.Stop(ctx, inst.ID))
}

// A worker's end reclaims every process it started, as a container's exit tears down its PID namespace (issue 45):
// Stop, Close and a crash each leave no subprocess running.
func TestIssue45_WorkerEndReclaimsSubprocesses(t *testing.T) {
	t.Parallel()
	ends := map[string]func(t *testing.T, rt runtime.Runtime, inst runtime.Instance){
		"stop": func(t *testing.T, rt runtime.Runtime, inst runtime.Instance) {
			t.Helper()
			require.NoError(t, rt.Stop(context.Background(), inst.ID))
		},
		"close": func(t *testing.T, rt runtime.Runtime, _ runtime.Instance) {
			t.Helper()
			require.NoError(t, rt.Close())
		},
		"crash": func(t *testing.T, _ runtime.Runtime, inst runtime.Instance) {
			t.Helper()
			require.NoError(t, syscall.Kill(inst.PID, syscall.SIGKILL))
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "child.pid")
			rt := process.New()
			t.Cleanup(func() { _ = rt.Close() })
			inst, err := rt.Create(ctx, runtime.WorkerSpec{
				Namespace: "default",
				Name:      "spawner",
				Command:   []string{"sh", "-c", `sleep 600 & echo $! > "$PIDFILE"; wait`},
				Env:       map[string]string{"PIDFILE": pidFile},
				LogPath:   filepath.Join(dir, "w.log"),
			})
			require.NoError(t, err)
			require.NoError(t, rt.Start(ctx, inst.ID))

			var child int
			require.Eventually(t, func() bool {
				b, rerr := os.ReadFile(pidFile) //nolint:gosec // test-owned temp file
				pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
				child = pid
				return rerr == nil && perr == nil && pid > 0
			}, 5*time.Second, 10*time.Millisecond, "the worker never reported its subprocess")
			t.Cleanup(func() {
				if t.Failed() {
					_ = syscall.Kill(child, syscall.SIGKILL)
				}
			})

			inst, err = rt.Status(ctx, inst.ID)
			require.NoError(t, err)
			end(t, rt, inst)
			require.Eventually(t, func() bool { return syscall.Kill(child, 0) != nil }, 5*time.Second, 20*time.Millisecond,
				"subprocess %d outlived its worker", child)
		})
	}
}

// Replacing an exited instance deletes the replaced instance's driver-owned log and port files, so a crash loop or a
// scale-to-zero wake leaves one pair per instance, not one per restart.
func TestIssue46_ReplaceRemovesDriverFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx := context.Background()
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	spec := runtime.WorkerSpec{Namespace: "default", Name: "crasher", Command: []string{"sh", "-c", `echo 1 > "$FUNCD_PORTFILE"; exit 1`}}
	files := func() []string {
		got, err := filepath.Glob(filepath.Join(tmp, "funcd-worker-*"))
		require.NoError(t, err)
		return got
	}

	for range 4 {
		inst, err := rt.Create(ctx, spec)
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))
		require.Eventually(t, func() bool {
			got, serr := rt.Status(ctx, inst.ID)
			return serr == nil && got.State.Terminal()
		}, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, rt.Stop(ctx, inst.ID))
	}
	require.Len(t, files(), 2, "only the current instance's log and port files remain")

	require.NoError(t, rt.Remove(ctx, runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)))
	require.Empty(t, files(), "Remove deletes the last instance's files")
}

// A Create rejected because the instance is live (Created or Running) leaves no driver-created log file behind.
func TestIssue365_RejectedCreateLeaksNoLog(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx := context.Background()
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	spec := runtime.WorkerSpec{Namespace: "default", Name: "live", Command: []string{"sleep", "30"}}
	logs := func() []string {
		got, err := filepath.Glob(filepath.Join(tmp, "funcd-worker-*.log"))
		require.NoError(t, err)
		return got
	}

	inst, err := rt.Create(ctx, spec)
	require.NoError(t, err)
	_, err = rt.Create(ctx, spec)
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Len(t, logs(), 1, "a Create rejected on a Created instance makes no log file")

	require.NoError(t, rt.Start(ctx, inst.ID))
	_, err = rt.Create(ctx, spec)
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Len(t, logs(), 1, "a Create rejected on a Running instance makes no log file")

	require.NoError(t, rt.Stop(ctx, inst.ID))
	require.NoError(t, rt.Remove(ctx, inst.ID))
	require.Empty(t, logs())
}
