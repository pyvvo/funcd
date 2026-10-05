//go:build dev

package devengine

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/catalog/embedengine"
	"github.com/pyvvo/funcd/internal/provider"
)

// fakeEngineEnv makes the test binary act as the duckdb engine (TestMain): it serves the address its
// -init script passes to quack_serve until stdin closes, as the real REPL does.
const fakeEngineEnv = "DEVENGINE_TEST_FAKE_ENGINE"

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
	if addr == nil {
		return 2
	}
	l, err := net.Listen("tcp", string(addr[1]))
	if err != nil {
		return 2
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
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
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

// scenario: dev-catalog-query (contract half, ADR-0125 M2) — a dev binary built WITHOUT the engine
// (the committed placeholder) reports catalog-unavailable at Converge gracefully (not-Ready, no
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
	if st.Reason != "CatalogEngineNotBundled" {
		t.Fatalf("Converge (placeholder): Reason=%q, want CatalogEngineNotBundled", st.Reason)
	}
}

// scenario: dev-catalog-query — with the real engine embedded (just build-catalog-engine), Converge
// extracts it, launches duckdb serving Quack, and reports Ready with a reachable Address; a second
// Converge is idempotent; Teardown stops it. Skipped where only the placeholder is embedded (CI) —
// the same gating the other engine-dependent lanes use. No docker, no network: purely the embedded
// subprocess.
func TestConvergeLaunchesEngineWhenBundled(t *testing.T) {
	if !embedengine.Bundled() {
		t.Skip("no real engine embedded (placeholder); run `just build-catalog-engine <os> <arch>` first")
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

// scenario: dev-catalog-persists-across-restart — with WithCatalogDir (funcdctl dev --persist), the
// DuckLake SQLite catalog is written under the durable dir (not the ephemeral temp) and SURVIVES
// Teardown, so the next boot reopens it (its Parquet DATA persists in blob).
func TestWithCatalogDirPersistsCatalog(t *testing.T) {
	if !embedengine.Bundled() {
		t.Skip("no real engine embedded (placeholder); run `just build-catalog-engine <os> <arch>` first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	durable := t.TempDir()
	r := mustNew(t, WithCatalogDir(durable))
	spec := testSpec()

	if _, err := r.Converge(ctx, spec); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	catalog := filepath.Join(durable, string(spec.Ref.Name), "catalog.db")
	if _, err := os.Stat(catalog); err != nil {
		t.Fatalf("durable catalog not created at %s: %v", catalog, err)
	}
	if terr := r.Teardown(ctx, spec.Ref); terr != nil {
		t.Fatalf("Teardown: %v", terr)
	}
	if _, err := os.Stat(catalog); err != nil {
		t.Fatalf("durable catalog removed by Teardown (should survive): %v", err)
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

// scenario: dev-catalog-attaches-ducklake — buildInitSQL ATTACHes the DuckLake as `lakehouse` (the
// prod catalog name, for consumer-SQL portability) with the S3-derived DATA_PATH, so a served
// quack_query DDL writes gold. Absent FUNCD_DUCKLAKE_CATALOG, no ATTACH is emitted.
func TestBuildInitSQLAttachesDuckLake(t *testing.T) {
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":      "AK",
		"AWS_SECRET_ACCESS_KEY":  "SK",
		"AWS_ENDPOINT_URL_S3":    "http://127.0.0.1:9000",
		"FUNCD_DUCKLAKE_CATALOG": "s3://releves/gold/_ducklake/catalog.db",
		"QUACK_TOKEN":            "tok",
	}
	sql := buildInitSQL("/ext", "/tmp/catalog.db", "127.0.0.1:5555", env)
	for _, want := range []string{
		"ATTACH 'ducklake:sqlite:/tmp/catalog.db' AS lakehouse (DATA_PATH 's3://releves/gold/')",
		"CREATE OR REPLACE SECRET funcd_dev_s3",
		"quack_serve",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("buildInitSQL missing %q in:\n%s", want, sql)
		}
	}
	// No catalog env ⇒ no ATTACH (a non-DuckLake engine still serves quack).
	noCat := buildInitSQL("/ext", "/tmp/catalog.db", "127.0.0.1:5555", map[string]string{"QUACK_TOKEN": "t"})
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
