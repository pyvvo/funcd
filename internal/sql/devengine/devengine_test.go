//go:build dev

package devengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

const (
	// fakeEngineEnv makes the test binary act as rqlited: it serves /readyz on its -http-addr until SIGTERM.
	fakeEngineEnv = "SQL_DEVENGINE_TEST_FAKE_ENGINE"
	// fakeModeEnv changes the fake engine: "silent" serves without saying it listens, "fail" ends on another error.
	fakeModeEnv = "SQL_DEVENGINE_TEST_FAKE_MODE"
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
	var addr, raft string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-http-addr":
			addr = args[i+1]
		case "-raft-addr":
			raft = args[i+1]
		}
	}
	if os.Getenv(fakeModeEnv) == "fail" {
		fmt.Fprintln(os.Stderr, "failed to create store: the data directory is locked")
		return 1
	}
	// As rqlited v10.5.3: a plain net.Listen of the Raft address, then of the HTTP address, log.Fatalf on a taken port,
	// and once each is bound "service listening on <addr>" from its service's logger.
	rl, err := net.Listen("tcp", raft)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[rqlited] 2026/10/22 16:21:06 failed to listen on %s: %s\n", raft, err)
		return 1
	}
	defer func() { _ = rl.Close() }()
	fmt.Fprintf(os.Stderr, "[cluster] 2026/10/22 16:21:01 service listening on %s\n", rl.Addr())
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[rqlited] 2026/10/22 16:21:06 failed to start HTTP server: %s\n", err)
		return 1
	}
	if os.Getenv(fakeModeEnv) != "silent" {
		fmt.Fprintf(os.Stderr, "[http] 2026/10/22 16:21:01 service listening on %s\n", ln.Addr())
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
		}
	})}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	go func() { <-stop; _ = srv.Close() }()
	if err := srv.Serve(ln); err != http.ErrServerClosed {
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
	require.Equal(t, filepath.Join(root, "dev", "todo-db", "db"), p.cmd.Args[len(p.cmd.Args)-1])
	require.NotContains(t, p.cmd.Args, "-cdc-config", "no change feed was asked for")

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

// With FUNCD_SQL_CDC_ENDPOINT the engine posts its changes there under an epoch kept beside its data, as in the
// engine container (ADR-0235).
func TestEngineChangeFeed(t *testing.T) {
	root := t.TempDir()
	r := mustNew(t, WithDataDir(root))
	t.Cleanup(r.StopAll)
	fake(t, r)
	s := spec()
	s.Env["FUNCD_SQL_CDC_ENDPOINT"] = "http://127.0.0.1:7999/sql/x"
	_, err := r.Converge(context.Background(), s)
	require.NoError(t, err)
	args := r.procs[s.Ref].cmd.Args
	i := slices.Index(args, "-cdc-config")
	require.Positive(t, i)
	raw, err := os.ReadFile(args[i+1])
	require.NoError(t, err)
	var cfg struct {
		Endpoint  string `json:"endpoint"`
		ServiceID string `json:"service_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	epoch, err := os.ReadFile(filepath.Join(root, "dev", "todo-db", "epoch"))
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:7999/sql/x", cfg.Endpoint)
	require.Equal(t, strings.TrimSpace(string(epoch)), cfg.ServiceID)
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

// scenario: sql-engine-port-taken — a port another process takes before the engine binds it starts the engine again
// with new ports, even when that process answers /readyz, and the database is Ready at an address its own engine
// serves; three starts in all; an engine that ends for another reason fails the start at once with its output
// (ADR-0260 Decision 3).
func TestScenarioSQLEnginePortTaken(t *testing.T) {
	stranger := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(stranger.Close)
	taken := stranger.Listener.Addr().(*net.TCPAddr).Port
	run := func(t *testing.T, pick func(n int) (int, error)) (provider.ProviderStatus, int, error) {
		t.Helper()
		r := mustNew(t)
		t.Cleanup(r.StopAll)
		fake(t, r)
		picks := 0
		r.pickPort = func() (int, error) { picks++; return pick(picks) }
		st, err := r.Converge(context.Background(), spec())
		return st, picks, err
	}

	t.Run("a taken port starts it again", func(t *testing.T) {
		st, picks, err := run(t, func(n int) (int, error) {
			if n == 1 {
				return taken, nil
			}
			return freePort()
		})
		require.NoError(t, err)
		require.True(t, st.Ready)
		require.NotEqual(t, "127.0.0.1:"+strconv.Itoa(taken), st.Address, "the server on the taken port does not count")
		require.Equal(t, 4, picks, "a second start with two new ports")
	})
	t.Run("three starts in all", func(t *testing.T) {
		_, picks, err := run(t, func(int) (int, error) { return taken, nil })
		require.ErrorIs(t, err, errPortTaken)
		require.Equal(t, 6, picks)
	})
	t.Run("another end fails at once", func(t *testing.T) {
		t.Setenv(fakeModeEnv, "fail")
		_, picks, err := run(t, func(int) (int, error) { return freePort() })
		require.ErrorContains(t, err, "the data directory is locked")
		require.NotErrorIs(t, err, errPortTaken)
		require.Equal(t, 2, picks, "one start")
	})
}

// An engine that answers /readyz but never said it listens on its address is not Ready: the answer could come from
// another process (ADR-0260 Decision 3).
func TestEngineIsReadyOnlyOnItsOwnListenLine(t *testing.T) {
	t.Setenv(fakeModeEnv, "silent")
	r := mustNew(t)
	t.Cleanup(r.StopAll)
	fake(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := r.Converge(ctx, spec())
	require.Error(t, err)
	require.Empty(t, r.procs, "the engine of the failed start is stopped")
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

// A database's data moves into <root>/.quarantine/sqldatabase/<ns>/<name>/<since> (ADR-0288), and List reads no
// namespace from the quarantine's own directory.
func TestQuarantineOutsideTheListing(t *testing.T) {
	root := t.TempDir()
	r := mustNew(t, WithDataDir(root))
	t.Cleanup(r.StopAll)
	ctx := context.Background()
	ref := provider.ProviderRef{Namespace: "default", Name: "todo-db"}
	require.NoError(t, os.MkdirAll(r.dataPath(ref), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.dataPath(ref), "db.sqlite"), []byte("rows"), 0o600))

	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	e, err := r.Quarantine().Move(ctx, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindSQLDatabase, Namespace: "default", Name: "todo-db"}}, now)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, ".quarantine", "sqldatabase", "default", "todo-db", quarantine.Stamp(e.Since), "db.sqlite"))
	refs, err := r.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs, "the quarantine is no namespace")
	entries, err := r.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{e}, entries)
}
