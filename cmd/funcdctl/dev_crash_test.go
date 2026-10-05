//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// devCrashEnv makes a re-executed test binary run `funcdctl dev` on "<persist|cache>\n<dir>\n<project>" until killed:
// with --persist on <dir>, or without it on the user cache dir <dir>.
const devCrashEnv = "FUNCDCTL_TEST_DEV_CRASH"

func crashConfig(spec string) (devConfig, string) {
	mode, rest, _ := strings.Cut(spec, "\n")
	root, project, _ := strings.Cut(rest, "\n")
	if mode == "persist" {
		return devConfig{persist: true, persistTo: root}, project
	}
	return devConfig{cacheDir: root}, project
}

// runDevChild runs `funcdctl dev` in the re-executed test binary until it is killed; it reports whether this process
// is that child.
func runDevChild(t *testing.T) bool {
	spec := os.Getenv(devCrashEnv)
	if spec == "" {
		return false
	}
	cfg, dir := crashConfig(spec)
	_, err := (&cli{out: io.Discard}).startDev(context.Background(), dir, "", cfg)
	require.NoError(t, err)
	select {}
}

func savedWorkers(t *testing.T, stateDir string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "workers.json"))
	if err != nil {
		return nil
	}
	var all []procreg.Entry
	require.NoError(t, json.Unmarshal(b, &all))
	var live []procreg.Entry
	for _, e := range all {
		if procreg.Alive(e) {
			live = append(live, e)
		}
	}
	return live
}

// reapAtCleanup kills, at test end, every worker a `funcdctl dev` run registered in stateDir and left running.
func reapAtCleanup(t *testing.T, stateDir string) {
	t.Cleanup(func() {
		if r, err := procreg.Open(stateDir, "workers"); err == nil {
			_, _ = r.Reap(context.Background(), time.Second)
			_ = r.Close()
		}
	})
}

// devRestartReaps kills a `funcdctl dev` run with SIGKILL and starts it again on the same state: the first run's
// worker is reaped and the new run's replica serves. It returns the project dir.
func devRestartReaps(t *testing.T, mode, root string) string {
	requireRuntime(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return { pid: process.pid }; }\n",
	})
	spec := mode + "\n" + root + "\n" + dir
	cfg, _ := crashConfig(spec)
	state, err := devStateDir(cfg, dir)
	require.NoError(t, err)
	reapAtCleanup(t, state)

	first := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	first.Env = append(os.Environ(), devCrashEnv+"="+spec)
	require.NoError(t, first.Start())
	var old []procreg.Entry
	require.Eventually(t, func() bool { old = savedWorkers(t, state); return len(old) == 1 }, 60*time.Second, 50*time.Millisecond,
		"the first run starts its worker")
	require.NoError(t, first.Process.Kill())
	_ = first.Wait()
	require.True(t, procreg.Alive(old[0]), "the killed run left its worker running")

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, "", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = inst.stop() })
	require.False(t, procreg.Alive(old[0]), "the first run's worker survived the restart")

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		resp, perr := http.Post(inst.gatewayURL+"/function/"+inst.functions[0], "application/json", strings.NewReader(`{}`))
		if assert.NoError(c, perr) {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			assert.Equal(c, http.StatusOK, resp.StatusCode)
		}
	}, 30*time.Second, 100*time.Millisecond, "the new run's replica serves")
	now := savedWorkers(t, state)
	require.Len(t, now, 1)
	require.NotEqual(t, old[0].PID, now[0].PID)
	return dir
}

// scenario: dev-persist-restart-reaps — `funcdctl dev --persist` killed with SIGKILL and started again on the same
// --persist-to dir reaps the first run's workers, and the new run's replicas serve. (The dev catalog engine's reap is
// TestStateDirReapsEnginesOfACrashedRun in internal/catalog/devengine.)
func TestScenarioDevPersistRestartReaps(t *testing.T) {
	if runDevChild(t) {
		return
	}
	devRestartReaps(t, "persist", t.TempDir())
}

// Without --persist the registry lives in the user cache dir, per project, and a restart on the same project reaps
// the first run's workers. The test passes its own cache dir, so the real one gets nothing.
func TestDevRestartWithoutPersistReaps(t *testing.T) {
	if runDevChild(t) {
		return
	}
	dir := devRestartReaps(t, "cache", t.TempDir())
	if real, err := os.UserCacheDir(); err == nil {
		state, err := devStateDir(devConfig{cacheDir: real}, dir)
		require.NoError(t, err)
		require.NoDirExists(t, filepath.Dir(state), "the test wrote into the real user cache dir")
	}
}

// devStateDir is <persist-to>/process under --persist, else <cache>/funcd/dev/<first 16 hex digits of the sha256 of
// the absolute project dir>/process (ADR-0167 Decision 2).
func TestDevStateDir(t *testing.T) {
	root, cache, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	got, err := devStateDir(devConfig{persist: true, persistTo: root, cacheDir: cache}, a)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "process"), got)

	got, err = devStateDir(devConfig{cacheDir: cache}, a)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(a))
	require.Equal(t, filepath.Join(cache, "funcd", "dev", hex.EncodeToString(sum[:])[:16], "process"), got)
	other, err := devStateDir(devConfig{cacheDir: cache}, b)
	require.NoError(t, err)
	require.NotEqual(t, got, other, "each project has its own state dir")

	wd, err := os.Getwd()
	require.NoError(t, err)
	rel, err := devStateDir(devConfig{cacheDir: cache}, ".")
	require.NoError(t, err)
	abs, err := devStateDir(devConfig{cacheDir: cache}, wd)
	require.NoError(t, err)
	require.Equal(t, abs, rel, "a relative project dir is keyed by its absolute path")
}

// devHangupEnv makes a re-executed test binary run the `funcdctl dev` command on "<persist-to>\n<project>" until a
// signal stops it.
const devHangupEnv = "FUNCDCTL_TEST_DEV_HANGUP"

// A hangup (a closed terminal or a dropped SSH session) stops `funcdctl dev` as SIGINT and SIGTERM do: the platform
// stops, its worker, which runs in its own process group and never sees the hangup, exits, and the shim temp dir is
// removed.
func TestIssue700_DevHangupStopsWorkers(t *testing.T) {
	if spec := os.Getenv(devHangupEnv); spec != "" {
		persistTo, dir, _ := strings.Cut(spec, "\n")
		root := newRootCmd(io.Discard)
		root.SetArgs([]string{"dev", dir, "--persist", "--persist-to", persistTo})
		require.NoError(t, root.Execute())
		return
	}
	requireRuntime(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return {}; }\n",
	})
	persistTo, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(persistTo) })
	state := filepath.Join(persistTo, "process")
	reapAtCleanup(t, state)
	out, err := os.Create(filepath.Join(t.TempDir(), "dev.out"))
	require.NoError(t, err)
	defer func() { _ = out.Close() }()
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := os.ReadFile(out.Name())
			t.Logf("dev output:\n%s", logs)
		}
	})

	// A forked child gets the default action for a signal this process handles, so it does not inherit the ignored
	// hangup of a test run under nohup.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP)
	defer signal.Stop(guard)

	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), devHangupEnv+"="+persistTo+"\n"+dir)
	child.Stdout, child.Stderr = out, out
	require.NoError(t, child.Start())
	var waitErr error
	exited := make(chan struct{})
	go func() { waitErr = child.Wait(); close(exited) }()
	t.Cleanup(func() { _ = child.Process.Kill(); <-exited })

	var workers []procreg.Entry
	require.Eventually(t, func() bool { workers = savedWorkers(t, state); return len(workers) == 1 }, 60*time.Second,
		50*time.Millisecond, "dev starts its worker")
	argv, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(workers[0].PID)).Output()
	require.NoError(t, err)
	args := strings.Fields(string(argv))
	i := slices.IndexFunc(args, func(a string) bool { return strings.Contains(a, "funcdctl-dev-shim") })
	require.GreaterOrEqual(t, i, 0, "the worker runs no dev shim: %s", argv)
	shimDir := filepath.Dir(args[i])
	t.Cleanup(func() { _ = os.RemoveAll(shimDir) })

	require.NoError(t, child.Process.Signal(syscall.SIGHUP))
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("dev did not stop on a hangup")
	}
	assert.Eventually(t, func() bool { return !procreg.Alive(workers[0]) }, 5*time.Second, 50*time.Millisecond,
		"the hangup left the worker running")
	assert.NoError(t, waitErr, "dev did not stop gracefully on a hangup")
	assert.NoDirExists(t, shimDir, "the hangup left the dev shim temp dir")
}
