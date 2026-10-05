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
	"path/filepath"
	"strings"
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
		if procreg.Owned(e) {
			live = append(live, e)
		}
	}
	return live
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
	t.Cleanup(func() {
		if r, err := procreg.Open(state, "workers"); err == nil {
			_, _ = r.Reap(context.Background(), time.Second)
			_ = r.Close()
		}
	})

	first := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	first.Env = append(os.Environ(), devCrashEnv+"="+spec)
	require.NoError(t, first.Start())
	var old []procreg.Entry
	require.Eventually(t, func() bool { old = savedWorkers(t, state); return len(old) == 1 }, 60*time.Second, 50*time.Millisecond,
		"the first run starts its worker")
	require.NoError(t, first.Process.Kill())
	_ = first.Wait()
	require.True(t, procreg.Owned(old[0]), "the killed run left its worker running")

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, "", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = inst.stop() })
	require.False(t, procreg.Owned(old[0]), "the first run's worker survived the restart")

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
