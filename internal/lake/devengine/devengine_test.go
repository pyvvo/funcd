//go:build dev

package devengine

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/lake/embedengine"
	"github.com/pyvvo/funcd/internal/provider"
)

const (
	// fakeEngineEnv makes the test binary act as the duckdb engine (TestMain): it serves on the port its -init script
	// passes to quack_serve, port 0 taking a free one that its row names, until stdin closes, as the real REPL does.
	fakeEngineEnv = "DEVENGINE_TEST_FAKE_ENGINE"
	// fakeModeEnv changes the fake engine: "norow" serves without printing its quack_serve row, "fail" ends on an init
	// error before quack_serve, as the real CLI does, and "agent" fails unless DUCKDB_AGENT_MODE=0 turns agent mode off.
	fakeModeEnv = "DEVENGINE_TEST_FAKE_MODE"
)

var quackServeAddr = regexp.MustCompile(`quack_serve\('quack:([^']+)'`)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEngineEnv) == "1" {
		os.Exit(runFakeEngine(os.Args[1:]))
	}
	if dir := os.Getenv(crashedRunEnv); dir != "" {
		os.Exit(runUntilKilled(dir))
	}
	os.Exit(m.Run())
}

func runFakeEngine(args []string) int {
	if len(args) < 2 || args[0] != "-init" {
		return 2
	}
	sql, err := os.ReadFile(args[1])
	if err != nil {
		return 2
	}
	addr := quackServeAddr.FindSubmatch(sql)
	if addr == nil || string(addr[1]) != "127.0.0.1:0" {
		fmt.Fprintln(os.Stderr, "the init does not serve on 127.0.0.1:0")
		return 2
	}
	switch os.Getenv(fakeModeEnv) {
	case "fail":
		fmt.Fprintln(os.Stderr, "Lake Error: Table with name no_such_table does not exist!")
		fmt.Fprintf(os.Stderr, "Encountered errors while executing init file %q. Exiting.\n", args[1])
		return 1
	case "agent":
		if os.Getenv("DUCKDB_AGENT_MODE") != "0" {
			fmt.Fprintln(os.Stderr, `{"type":"error","message":"rendered for an AI agent"}`)
			return 1
		}
	}
	l, err := net.Listen("tcp", string(addr[1]))
	if err != nil {
		fmt.Fprintf(os.Stderr, "IO Error: Failed to bind DuckDB Quack RPC server to http://%s\n", addr[1])
		return 1
	}
	go func() {
		for {
			c, aerr := l.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if os.Getenv(fakeModeEnv) != "norow" {
		if _, err := fmt.Fprintf(os.Stdout, "│ listen_url │\n│ http://%s │\n", l.Addr()); err != nil {
			return 1
		}
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// fakeRuntime is a runtime whose engine is this test binary (TestMain).
func fakeRuntime(t *testing.T) *Runtime {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	t.Setenv(fakeEngineEnv, "1")
	r := mustNew(t)
	r.bundled = func() bool { return true }
	r.extract = func(dir string) (embedengine.Paths, error) {
		return embedengine.Paths{DuckDB: exe, ExtensionDir: dir}, nil
	}
	t.Cleanup(r.StopAll)
	return r
}

// requireTaken fails unless a plain bind of addr fails.
func requireTaken(t *testing.T, addr string) {
	t.Helper()
	if l, err := net.Listen("tcp", addr); err == nil {
		_ = l.Close()
		t.Fatalf("a plain bind took %s", addr)
	}
}

// scenario: dev-engine-port-zero — the engine's init asks for port 0, and the start reports the port the engine's
// listen_url row names, which the engine holds (ADR-0263 Decision 4).
func TestScenarioDevEnginePortZero(t *testing.T) {
	if sql := buildInitSQL("/ext", "/tmp/catalog.db", nil); !strings.Contains(sql, "quack_serve('quack:127.0.0.1:0'") {
		t.Fatalf("the init does not ask for port 0:\n%s", sql)
	}
	r := fakeRuntime(t)
	st, err := r.Converge(context.Background(), testSpec())
	if err != nil || !st.Ready {
		t.Fatalf("Converge: Ready=%v err=%v", st.Ready, err)
	}
	if _, port, _ := net.SplitHostPort(st.Address); port == "" || port == "0" {
		t.Fatalf("Address %q names no port the engine took", st.Address)
	}
	conn, err := net.DialTimeout("tcp", st.Address, 2*time.Second)
	if err != nil {
		t.Fatalf("the engine does not serve the reported %s: %v", st.Address, err)
	}
	_ = conn.Close()
	requireTaken(t, st.Address)
}

// TestRowWatcherSplitRow: the watcher reads whole lines, so a row split inside its port gives the whole port, and a
// line before the row matches nothing.
func TestRowWatcherSplitRow(t *testing.T) {
	w := &rowWatcher{seen: make(chan struct{})}
	for _, part := range []string{"WARNING:\nsome", " text\n│ http://127.0.0.1:12", "345 │\n│ http://127.0.0.1:1 │\n"} {
		select {
		case <-w.seen:
			t.Fatalf("matched before the row ended, at %q", w.addr)
		default:
		}
		if n, err := w.Write([]byte(part)); n != len(part) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", part, n, err)
		}
	}
	select {
	case <-w.seen:
	default:
		t.Fatal("the row was not seen")
	}
	if w.addr != "127.0.0.1:12345" {
		t.Fatalf("addr %q, want 127.0.0.1:12345", w.addr)
	}
}

// scenario: dev-engine-early-exit — an engine that ends before its row fails the start at once with its error output;
// one that never prints its row fails at the timeout.
func TestScenarioDevEngineEarlyExit(t *testing.T) {
	t.Run("a listening engine without its row is not Ready", func(t *testing.T) {
		t.Setenv(fakeModeEnv, "norow")
		r := fakeRuntime(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if st, err := r.Converge(ctx, testSpec()); err == nil || st.Ready {
			t.Fatalf("Converge: Ready=%v err=%v, want a failed start", st.Ready, err)
		}
		if len(r.procs) != 0 {
			t.Fatalf("the engine of the failed start still runs: %v", r.procs)
		}
	})
	t.Run("an engine that ends fails the start at once", func(t *testing.T) {
		t.Setenv(fakeModeEnv, "fail")
		r := fakeRuntime(t)
		start := time.Now()
		_, err := r.Converge(context.Background(), testSpec())
		if err == nil || !strings.Contains(err.Error(), "no_such_table") {
			t.Fatalf("Converge: %v, want the engine's error output", err)
		}
		if took := time.Since(start); took >= serveTimeout {
			t.Fatalf("the failed start took %s, the whole timeout", took)
		}
	})
}

// scenario: agent-environment-same-output — with an AI agent's variable set, and agent mode asked for, the engine still
// runs with DUCKDB_AGENT_MODE=0, so its row and its errors are rendered as without them (ADR-0263 Decision 6).
func TestScenarioAgentEnvironmentSameOutput(t *testing.T) {
	t.Setenv("AI_AGENT", "1")
	t.Setenv("DUCKDB_AGENT_MODE", "1")
	t.Setenv(fakeModeEnv, "agent")
	r := fakeRuntime(t)
	if st, err := r.Converge(context.Background(), testSpec()); err != nil || !st.Ready {
		t.Fatalf("Converge: Ready=%v err=%v, want the engine run with agent mode off", st.Ready, err)
	}
}

func testSpec() provider.ProviderSpec {
	return provider.ProviderSpec{
		Ref:      provider.ProviderRef{Namespace: "default", Name: "lake"},
		Image:    "funcd/runtime-duckdb",
		Port:     8080,
		Replicas: 1,
		Env:      map[string]string{"FUNCD_DUCKLAKE_CATALOG": "s3://dev/lake"},
	}
}

// scenario: dev-lake-query (contract half, ADR-0125 M2) — a dev binary built WITHOUT the engine
// (the committed placeholder) reports lake-unavailable at Converge gracefully (not-Ready, no
// error), so `funcdctl dev` still boots. This runs everywhere; the live query through the real
// engine is the deferred lane.
func TestConvergeNotBundledReportsUnavailable(t *testing.T) {
	if embedengine.Bundled() {
		t.Skip("a real engine is embedded; this case asserts the placeholder path")
	}
	st, err := mustNew(t).Converge(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Converge (placeholder): unexpected error %v", err)
	}
	if st.Ready {
		t.Fatal("Converge (placeholder): Ready=true, want false")
	}
	if st.Reason != "LakeEngineNotBundled" {
		t.Fatalf("Converge (placeholder): Reason=%q, want LakeEngineNotBundled", st.Reason)
	}
}

// scenario: dev-lake-query — with the real engine embedded (just build-lake-engine), Converge
// extracts it, launches duckdb serving Quack, and reports Ready with a reachable Address; a second
// Converge is idempotent; Teardown stops it. Skipped where only the placeholder is embedded (CI) —
// the same gating the other engine-dependent lanes use. No docker, no network: purely the embedded
// subprocess.
func TestConvergeLaunchesEngineWhenBundled(t *testing.T) {
	if !embedengine.Bundled() {
		t.Skip("no real engine embedded (placeholder); run `just build-lake-engine <os> <arch>` first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	r := mustNew(t)
	spec := testSpec()

	st, err := r.Converge(ctx, spec)
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !st.Ready || st.Address == "" {
		t.Fatalf("Converge: Ready=%v Address=%q, want Ready with an address", st.Ready, st.Address)
	}
	conn, derr := net.DialTimeout("tcp", st.Address, 2*time.Second)
	if derr != nil {
		t.Fatalf("Quack engine not reachable at %s: %v", st.Address, derr)
	}
	_ = conn.Close()
	// Ready came from the engine's quack_serve row, and the engine holds the port that row named (ADR-0263).
	requireTaken(t, st.Address)
	requireNoDeprecation(t, ctx)

	// Idempotent: a second Converge returns the same running engine, not a new one.
	st2, err2 := r.Converge(ctx, spec)
	if err2 != nil || st2.Address != st.Address {
		t.Fatalf("Converge (2nd): err=%v addr=%q, want the same addr %q", err2, st2.Address, st.Address)
	}

	if terr := r.Teardown(ctx, spec.Ref); terr != nil {
		t.Fatalf("Teardown: %v", terr)
	}
	time.Sleep(300 * time.Millisecond)
	if c, e := net.DialTimeout("tcp", st.Address, 500*time.Millisecond); e == nil {
		_ = c.Close()
		t.Fatalf("engine still reachable at %s after Teardown", st.Address)
	}
}

// requireNoDeprecation is scenario no-deprecated-extension-setting on the real CLI: it runs the embedded engine on
// buildInitSQL's script once, with stdin at EOF so the REPL ends, and fails on a deprecation line in its output,
// which 2.0 prints to stdout (ADR-0263 Decision 5).
func requireNoDeprecation(t *testing.T, ctx context.Context) {
	t.Helper()
	dir := t.TempDir()
	paths, err := embedengine.Extract(dir)
	if err != nil {
		t.Fatalf("extract the engine: %v", err)
	}
	init := filepath.Join(dir, "init.sql")
	if err := os.WriteFile(init, []byte(buildInitSQL(paths.ExtensionDir, filepath.Join(dir, "catalog.db"), testSpec().Env)), 0o600); err != nil {
		t.Fatalf("write the init: %v", err)
	}
	cmd := exec.CommandContext(ctx, paths.DuckDB, "-init", init, ":memory:")
	cmd.Env = append(os.Environ(), "DUCKDB_AGENT_MODE=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the engine on its init: %v\n%s", err, out)
	}
	if !serveRow.Match(out) {
		t.Fatalf("the engine printed no listen_url row:\n%s", out)
	}
	if strings.Contains(strings.ToLower(string(out)), "deprecated") {
		t.Fatalf("the engine printed a deprecation line:\n%s", out)
	}
}

// Issue 105: an engine that dies on its own (crash, OOM kill, SIGKILL) is relaunched by the next
// Converge instead of being reported Ready at its dead address. The engine is this test binary
// (TestMain), so the lifecycle runs where only the placeholder engine is embedded.
func TestIssue105_ConvergeRelaunchesCrashedEngine(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	t.Setenv(fakeEngineEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := mustNew(t)
	r.bundled = func() bool { return true }
	r.extract = func(dir string) (embedengine.Paths, error) {
		return embedengine.Paths{DuckDB: exe, ExtensionDir: dir}, nil
	}
	t.Cleanup(r.StopAll)
	spec := testSpec()

	if _, err := r.Converge(ctx, spec); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	crashed := r.procs[spec.Ref]
	if err := crashed.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL engine: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := r.Converge(ctx, spec)
		if err != nil {
			t.Fatalf("Converge after crash: %v", err)
		}
		if r.procs[spec.Ref] != crashed {
			conn, derr := net.DialTimeout("tcp", st.Address, 2*time.Second)
			if !st.Ready || derr != nil {
				t.Fatalf("relaunched engine: Ready=%v, dial %s: %v", st.Ready, st.Address, derr)
			}
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Converge still reports Ready=%v at the crashed engine (pid %d, %s): never relaunched",
				st.Ready, crashed.cmd.Process.Pid, st.Address)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// scenario: dev-lake-persists-across-restart — with WithLakeDir (funcdctl dev --persist), the
// DuckLake SQLite catalog is written under the durable dir (not the ephemeral temp) and SURVIVES
// Teardown, so the next boot reopens it (its Parquet DATA persists in blob).
func TestWithLakeDirPersistsLake(t *testing.T) {
	if !embedengine.Bundled() {
		t.Skip("no real engine embedded (placeholder); run `just build-lake-engine <os> <arch>` first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	durable := t.TempDir()
	r := mustNew(t, WithLakeDir(durable))
	spec := testSpec()

	if _, err := r.Converge(ctx, spec); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	lake := filepath.Join(durable, string(spec.Ref.Name), "catalog.db")
	if _, err := os.Stat(lake); err != nil {
		t.Fatalf("durable lake not created at %s: %v", lake, err)
	}
	if terr := r.Teardown(ctx, spec.Ref); terr != nil {
		t.Fatalf("Teardown: %v", terr)
	}
	if _, err := os.Stat(lake); err != nil {
		t.Fatalf("durable lake removed by Teardown (should survive): %v", err)
	}
}

func TestDataPathFor(t *testing.T) {
	cases := map[string]string{
		"s3://releves/gold/_ducklake/catalog.db": "s3://releves/gold/",
		"s3://b/p/_ducklake/catalog.db":          "s3://b/p/",
		"s3://b/p/catalog.db":                    "s3://b/p/", // no marker → parent dir
		"s3://b/only":                            "s3://b/",
	}
	for in, want := range cases {
		if got := dataPathFor(in); got != want {
			t.Errorf("dataPathFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// scenario: dev-lake-attaches-ducklake — buildInitSQL ATTACHes the DuckLake as `lakehouse` (the
// prod lake name, for consumer-SQL portability) with the S3-derived DATA_PATH, so a served
// quack_query DDL writes gold. Absent FUNCD_DUCKLAKE_CATALOG, no ATTACH is emitted.
func TestBuildInitSQLAttachesDuckLake(t *testing.T) {
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":      "AK",
		"AWS_SECRET_ACCESS_KEY":  "SK",
		"AWS_ENDPOINT_URL_S3":    "http://127.0.0.1:9000",
		"FUNCD_DUCKLAKE_CATALOG": "s3://releves/gold/_ducklake/catalog.db",
		"QUACK_TOKEN":            "tok",
	}
	sql := buildInitSQL("/ext", "/tmp/catalog.db", env)
	for _, want := range []string{
		"SET extension_directories=['/ext'];",
		"ATTACH 'ducklake:sqlite:/tmp/catalog.db' AS lakehouse (DATA_PATH 's3://releves/gold/', OVERRIDE_DATA_PATH TRUE, DUCKLAKE_VERSION '1.1')",
		"CREATE OR REPLACE SECRET funcd_dev_s3",
		"quack_serve",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("buildInitSQL missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "AUTOMATIC_MIGRATION") || strings.Contains(sql, "extension_directory=") {
		t.Errorf("buildInitSQL migrates the catalog or sets the deprecated extension_directory:\n%s", sql)
	}
	// No lake env ⇒ no ATTACH (a non-DuckLake engine still serves quack).
	noCat := buildInitSQL("/ext", "/tmp/catalog.db", map[string]string{"QUACK_TOKEN": "t"})
	if strings.Contains(noCat, "ATTACH") {
		t.Errorf("buildInitSQL emitted ATTACH with no FUNCD_DUCKLAKE_CATALOG:\n%s", noCat)
	}
}

func mustNew(t *testing.T, opts ...Option) *Runtime {
	t.Helper()
	r, err := New(nil, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}
