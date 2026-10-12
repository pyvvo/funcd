//go:build dev

package devengine

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

const (
	// fakeEngineEnv makes the test binary act as rqlited: it serves /readyz on its -http-addr until SIGTERM.
	fakeEngineEnv = "SQL_DEVENGINE_TEST_FAKE_ENGINE"
	// crashedRunEnv makes the test binary converge one fake engine under the state dir it names, until killed.
	crashedRunEnv = "SQL_DEVENGINE_TEST_CRASHED_RUN"
)

func TestMain(m *testing.M) {
	switch {
	case os.Getenv(fakeEngineEnv) == "1":
		os.Exit(fakeEngine(os.Args[1:]))
	case os.Getenv(crashedRunEnv) != "":
		os.Exit(runUntilKilled(os.Getenv(crashedRunEnv)))
	}
	os.Exit(m.Run())
}

func fakeEngine(args []string) int {
	var addr string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-http-addr" {
			addr = args[i+1]
		}
	}
	srv := &http.Server{Addr: addr, ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
		}
	})}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	go func() { <-stop; _ = srv.Close() }()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return 1
	}
	return 0
}

// fake makes r run the test binary as its engine.
func fake(t *testing.T, r *Runtime) {
	t.Helper()
	t.Setenv(fakeEngineEnv, "1")
	exe, err := os.Executable()
	require.NoError(t, err)
	r.bundled = func() bool { return true }
	r.extract = func(string) (string, error) { return exe, nil }
}

func spec() provider.ProviderSpec {
	return provider.ProviderSpec{
		Ref:     provider.ProviderRef{Namespace: "dev", Name: "todo-db"},
		Env:     map[string]string{"FUNCD_SQL_PASSWORD": "pw"},
		Storage: &provider.StorageSpec{Path: "/data", UID: 65532, GID: 65532},
	}
}

func mustNew(t *testing.T, opts ...Option) *Runtime {
	t.Helper()
	r, err := New(nil, opts...)
	require.NoError(t, err)
	return r
}

// A build without the engine reports the database not Ready with its reason and starts nothing.
func TestConvergeNotBundled(t *testing.T) {
	r := mustNew(t)
	t.Cleanup(r.StopAll)
	r.bundled = func() bool { return false }
	st, err := r.Converge(context.Background(), spec())
	require.NoError(t, err)
	require.Equal(t, provider.ProviderStatus{Reason: "SQLEngineNotBundled"}, st)
}

// An engine serves on loopback with its auth file at 0600 and its data under the root; a second Converge reuses it;
// Teardown stops it and keeps the data, which Reclaim removes, and only once the engine is stopped.
func TestEngineLifecycle(t *testing.T) {
	root := t.TempDir()
	r := mustNew(t, WithDataDir(root))
	t.Cleanup(r.StopAll)
	fake(t, r)
	ctx := context.Background()
	st, err := r.Converge(ctx, spec())
	require.NoError(t, err)
	require.True(t, st.Ready)
	require.Regexp(t, `^127\.0\.0\.1:[0-9]+$`, st.Address)
	again, err := r.Converge(ctx, spec())
	require.NoError(t, err)
	require.Equal(t, st.Address, again.Address, "a live engine is reused")

	p := r.procs[spec().Ref]
	fi, err := os.Stat(filepath.Join(p.dir, "auth.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	require.Contains(t, p.cmd.Args, "-raft-addr")
	require.Equal(t, filepath.Join(root, "dev", "todo-db"), p.cmd.Args[len(p.cmd.Args)-1])

	require.Equal(t, fault.Conflict, fault.KindOf(r.Reclaim(ctx, spec().Ref)), "a running engine's data is not reclaimed")
	require.NoError(t, r.Teardown(ctx, spec().Ref))
	require.NoDirExists(t, p.dir, "the engine's temporary directory goes with it")
	refs, err := r.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []provider.ProviderRef{spec().Ref}, refs, "the data outlives the engine")
	require.NoError(t, r.Reclaim(ctx, spec().Ref))
	refs, err = r.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs)
}

// An engine that exited is started again at the next Converge.
func TestConvergeRestartsExitedEngine(t *testing.T) {
	r := mustNew(t)
	t.Cleanup(r.StopAll)
	fake(t, r)
	_, err := r.Converge(context.Background(), spec())
	require.NoError(t, err)
	first := r.procs[spec().Ref]
	require.NoError(t, first.cmd.Process.Kill())
	<-first.done
	st, err := r.Converge(context.Background(), spec())
	require.NoError(t, err)
	require.True(t, st.Ready)
	require.NotEqual(t, first.cmd.Process.Pid, r.procs[spec().Ref].cmd.Process.Pid)
}

// Without a data dir the data lives in a temporary root that StopAll removes.
func TestStopAllRemovesTemporaryData(t *testing.T) {
	r := mustNew(t)
	fake(t, r)
	_, err := r.Converge(context.Background(), spec())
	require.NoError(t, err)
	root := r.dataDir
	require.DirExists(t, filepath.Join(root, "dev", "todo-db"))
	r.StopAll()
	require.NoDirExists(t, root)
}

func runUntilKilled(dir string) int {
	exe, err := os.Executable()
	if err != nil || os.Setenv(fakeEngineEnv, "1") != nil {
		return 2
	}
	r, err := New(nil, WithStateDir(dir), WithDataDir(filepath.Join(dir, "data")))
	if err != nil {
		return 2
	}
	r.bundled = func() bool { return true }
	r.extract = func(string) (string, error) { return exe, nil }
	if _, err := r.Converge(context.Background(), spec()); err != nil {
		return 2
	}
	select {}
}

func savedEngines(t *testing.T, dir string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "sql-engines.json"))
	if err != nil {
		return nil
	}
	var out []procreg.Entry
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// The next New on a killed run's state dir reaps its engine and the engine's temporary directory, and keeps the data
// (ADR-0167, scenario dev-crashed-engine-reaped).
func TestStateDirReapsEngineOfAKilledRun(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), crashedRunEnv+"="+dir)
	require.NoError(t, cmd.Start())
	var saved []procreg.Entry
	require.Eventually(t, func() bool {
		saved = savedEngines(t, dir)
		return len(saved) == 1 && procreg.Alive(saved[0])
	}, 20*time.Second, 20*time.Millisecond, "the first run saves a live engine")
	engine := saved[0]
	require.Len(t, engine.Files, 1)
	require.Contains(t, filepath.Base(engine.Files[0]), engine.Token, "the engine directory carries the token")
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	r := mustNew(t, WithStateDir(dir), WithDataDir(filepath.Join(dir, "data")))
	t.Cleanup(r.StopAll)
	require.False(t, procreg.Alive(engine), "the killed run's engine is gone")
	require.NoDirExists(t, engine.Files[0])
	require.Empty(t, savedEngines(t, dir))
	require.DirExists(t, filepath.Join(dir, "data", "dev", "todo-db"), "the data stays")
	_, err := New(nil, WithStateDir(dir))
	require.Error(t, err, "a second runtime cannot open the registry another one holds")
}
