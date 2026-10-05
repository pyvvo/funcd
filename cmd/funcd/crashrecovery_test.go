//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/platform/config"
	fnruntime "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/containerd"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// childDaemonEnv makes a re-executed test binary run the daemon on the config file it names, until it is killed.
const childDaemonEnv = "FUNCD_TEST_CHILD_DAEMON_CONFIG"

const childToken = "crash-recovery-token"

// crashDaemon is a funcd daemon in process mode run as a child process, so a test can SIGKILL it (ADR-0167).
type crashDaemon struct {
	dataDir, configPath string
	control, dataPlane  string
}

// runChildDaemon serves in the child; it reports whether this process is that child.
func runChildDaemon(t *testing.T) bool {
	path := os.Getenv(childDaemonEnv)
	if path == "" {
		return false
	}
	err := serve(context.Background(), path, nil, os.Stderr)
	t.Fatalf("child daemon stopped: %v", err)
	return true
}

func newCrashDaemon(t *testing.T) *crashDaemon {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	d := &crashDaemon{dataDir: shortDataDir(t)}
	d.control, d.dataPlane = freeAddr(t), freeAddr(t)
	d.configPath = filepath.Join(d.dataDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(d.configPath, fmt.Appendf(nil,
		"server:\n  listenAddr: %q\n  dataPlaneAddr: %q\nstorage:\n  mode: file\n  dataDir: %q\n"+
			"auth:\n  token: %s\nruntime:\n  process:\n    stopGrace: 1s\nlog:\n  level: warn\n",
		d.control, d.dataPlane, d.dataDir, childToken), 0o600))
	// Whatever a failed test leaves running is reaped through the registry the daemon saved.
	t.Cleanup(func() {
		if r, err := procreg.Open(filepath.Join(d.dataDir, "process"), "workers"); err == nil {
			_, _ = r.Reap(context.Background(), time.Second)
			_ = r.Close()
		}
	})
	return d
}

// start runs the daemon as a child process and waits until its control plane answers.
func (d *crashDaemon) start(t *testing.T) (*exec.Cmd, *sdk.Client) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), childDaemonEnv+"="+d.configPath)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Logf("daemon output:\n%s", logs.String())
		}
	})
	c, err := sdk.New("http://"+d.control, sdk.WithToken(childToken))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, lerr := c.List(context.Background(), v1.KindFunction, "default")
		return lerr == nil
	}, 30*time.Second, 50*time.Millisecond, "the daemon serves")
	return cmd, c
}

// kill SIGKILLs the daemon, so nothing in it runs its shutdown.
func kill(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	require.NoError(t, cmd.Process.Kill())
	require.Eventually(t, func() bool { return syscall.Kill(cmd.Process.Pid, 0) != nil }, 10*time.Second, 10*time.Millisecond)
}

// workers returns the live workers of the Function name that the daemon saved in its registry.
func (d *crashDaemon) workers(t *testing.T, name string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.dataDir, "process", "workers.json"))
	if err != nil {
		return nil
	}
	var all []procreg.Entry
	require.NoError(t, json.Unmarshal(b, &all))
	var out []procreg.Entry
	for _, e := range all {
		if strings.HasPrefix(e.ID, "default/"+name+"/") && procreg.Owned(e) {
			out = append(out, e)
		}
	}
	return out
}

func (d *crashDaemon) apply(t *testing.T, c *sdk.Client, name string, replicas int) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() { return { pid: process.pid }; }\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	_, err := artifact.Push(context.Background(), ref, bundle, nil, "", "")
	require.NoError(t, err)
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", ref
	fn.Spec.Replicas, fn.Spec.Scaling = replicas, v1.Scaling{MinReplicas: replicas}
	_, err = c.Apply(context.Background(), fn)
	require.NoError(t, err)
}

func pidsOf(entries []procreg.Entry) []int {
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.PID)
	}
	return out
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// scenario: crash-restart-leaves-desired-workers — a daemon killed with SIGKILL and restarted on the same data dir
// runs exactly the Function's 2 replicas, none of them left from the first run.
func TestScenarioCrashRestartLeavesDesiredWorkers(t *testing.T) {
	if runChildDaemon(t) {
		return
	}
	d := newCrashDaemon(t)
	first, c := d.start(t)
	d.apply(t, c, "echo", 2)
	var old []procreg.Entry
	require.Eventually(t, func() bool { old = d.workers(t, "echo"); return len(old) == 2 }, 30*time.Second, 50*time.Millisecond)

	kill(t, first)
	require.Len(t, d.workers(t, "echo"), 2, "the killed daemon left its workers running")

	d.start(t)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		now := d.workers(t, "echo")
		assert.Len(c, now, 2, "exactly the desired replicas run")
		for _, e := range now {
			assert.NotContains(c, pidsOf(old), e.PID, "a worker of the first run")
		}
	}, 30*time.Second, 50*time.Millisecond)
	for _, e := range old {
		require.False(t, procreg.Owned(e), "worker %d of the first run still runs", e.PID)
		require.NotEmpty(t, e.Files, "worker %d was saved without its files", e.PID)
		for _, f := range e.Files {
			require.NoFileExists(t, f, "a file of worker %d of the first run", e.PID)
		}
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		resp, err := http.Post("http://"+d.dataPlane+"/function/echo", "application/json", strings.NewReader(`{}`))
		if assert.NoError(c, err) {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			assert.Equal(c, http.StatusOK, resp.StatusCode)
		}
	}, 30*time.Second, 100*time.Millisecond, "the restarted daemon serves the Function")
}

// scenario: deleted-while-down-reaped — a Function deleted from the store while funcd is down has no worker once
// funcd restarts.
func TestScenarioDeletedWhileDownReaped(t *testing.T) {
	if runChildDaemon(t) {
		return
	}
	d := newCrashDaemon(t)
	first, c := d.start(t)
	d.apply(t, c, "gone", 1)
	var old []procreg.Entry
	require.Eventually(t, func() bool { old = d.workers(t, "gone"); return len(old) == 1 }, 30*time.Second, 50*time.Millisecond)
	kill(t, first)

	cfg, err := config.Load(d.configPath, config.Flags{})
	require.NoError(t, err)
	st, err := buildStore(cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "gone", ""))
	require.NoError(t, st.Close())

	_, c = d.start(t)
	require.False(t, procreg.Owned(old[0]), "the deleted Function's worker was reaped at open")
	d.apply(t, c, "marker", 1)
	require.Eventually(t, func() bool { return len(d.workers(t, "marker")) == 1 }, 30*time.Second, 50*time.Millisecond,
		"the restarted daemon reconciles")
	require.Empty(t, d.workers(t, "gone"), "a worker of the deleted Function runs")
}

// scenario: stop-grace-from-config — runtime.process.stopGrace 500ms ends a worker that ignores SIGTERM within 1 s;
// 0s, -1s, soon or more than 10s stops funcd before it serves, naming the key.
func TestScenarioStopGraceFromConfig(t *testing.T) {
	ctx := context.Background()
	dir := shortDataDir(t)
	cfg := cfgProcess(dir)
	cfg.Runtime.Process.StopGrace = "500ms"
	grace, err := processStopGrace(cfg)
	require.NoError(t, err)
	rt, err := process.Open(ctx, filepath.Join(dir, "process"), grace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })
	inst, err := rt.Create(ctx, fnruntime.WorkerSpec{
		Namespace: "default", Name: "stubborn", OwnerKind: v1.KindFunction,
		Command: []string{"sh", "-c", `trap "" TERM; sleep 30; true`}, LogPath: filepath.Join(dir, "w.log"),
	})
	require.NoError(t, err)
	require.NoError(t, rt.Start(ctx, inst.ID))
	time.Sleep(100 * time.Millisecond) // let sh install its trap
	began := time.Now()
	require.NoError(t, rt.Stop(ctx, inst.ID))
	require.Less(t, time.Since(began), time.Second, "Stop waits the configured grace, then SIGKILL")
	require.GreaterOrEqual(t, time.Since(began), 400*time.Millisecond, "the worker ignored SIGTERM until the grace ran out")

	cfg.Runtime.Process.StopGrace = ""
	grace, err = processStopGrace(cfg)
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, grace, "today's default")

	for _, bad := range []string{"0s", "-1s", "soon", "11s"} {
		t.Run(bad, func(t *testing.T) {
			dir := shortDataDir(t)
			path := filepath.Join(dir, "funcdconfig.yaml")
			require.NoError(t, os.WriteFile(path, fmt.Appendf(nil,
				"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
					"storage:\n  mode: memory\n  dataDir: %q\nruntime:\n  process:\n    stopGrace: %q\n", dir, bad), 0o600))
			err := serve(ctx, path, nil, io.Discard)
			require.Error(t, err)
			require.ErrorContains(t, err, "runtime.process.stopGrace")
		})
	}
}

// sweptRuntime is a containerd driver that counts the boot sweeps.
type sweptRuntime struct {
	fnruntime.Runtime
	sweeps int
	err    error
}

func (r *sweptRuntime) SweepAll(context.Context) error {
	r.sweeps++
	return r.err
}

// Containerd mode sweeps every funcd namespace once before it hands the driver to the controllers; a failed sweep is
// logged and funcd still boots (ADR-0167 Decision 8).
func TestContainerdModeSweepsLeftoversAtBoot(t *testing.T) {
	for name, sweepErr := range map[string]error{"swept": nil, "sweep failed": errors.New("namespace busy")} {
		t.Run(name, func(t *testing.T) {
			cfg := cfgProcess(t.TempDir())
			cfg.Runtime.Mode = "containerd"
			cfg.Runtime.Containerd.Socket = filepath.Join(t.TempDir(), "containerd.sock")
			rt := &sweptRuntime{err: sweepErr}
			var logs bytes.Buffer
			opts, closeExec, err := containerdOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(&logs, nil)),
				func(containerd.Config) (fnruntime.Runtime, error) { return rt, nil })
			require.NoError(t, err)
			t.Cleanup(func() { _ = closeExec() })
			require.NotEmpty(t, opts)
			require.Equal(t, 1, rt.sweeps, "the boot sweep runs once")
			require.Equal(t, sweepErr != nil, strings.Contains(logs.String(), "could not remove every container"), "%s", logs.String())
		})
	}
}
