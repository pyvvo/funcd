//go:build dev

// Package devengine is the process-mode driver of the add-on-provider Runtime port
// (internal/provider) for `funcdctl dev` (ADR-0125 Decision 5, FEAT-0001/F90). Where the prod
// provider runtime supervises the curated `funcd/runtime-duckdb` CONTAINER (ADR-0086/0087), this
// launches the go:embed'd DuckDB+Quack engine (internal/catalog/embedengine) as a host SUBPROCESS
// serving the Quack endpoint — a process-mode catalog engine: no container, no root, no cgo (a
// subprocess, not linked). It is injected into the CatalogService reconciler in dev via
// funcd.WithCatalogProviderRuntime, so the same reconciler + ProviderSpec drive it as in prod.
//
// Converge is idempotent (one engine per provider Ref; a live one is reused); Teardown stops it.
// The engine LOADs the extensions and starts `quack_serve` in a background thread — the driver holds
// the duckdb REPL's stdin open so the process stays alive serving until Teardown closes it.
//
// SCOPE (ADR-0125 M2): this delivers the engine LIFECYCLE (extract → serve Quack → ready → stop) and
// the S3 secret wiring. Attaching the DuckLake and running a live query *through* a function is the
// deferred dev-catalog-query live lane; the engine here serves Quack so that lane can build on it.
package devengine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/catalog/embedengine"
	"github.com/pyvvo/funcd/internal/provider"
)

// DevQuackToken is the fixed local Quack auth token the dev engine serves with when spec.Env carries
// none. `funcdctl dev` writes it into the synthesized catalog's QUACK_TOKEN Secret so the consumer
// (resolveCatalogEnv → FUNCD_CATALOG_<ALIAS>_TOKEN) presents the SAME token the engine serves with.
const DevQuackToken = "funcd-dev-catalog"

// Runtime is the dev process-mode provider.Runtime: at most one duckdb+quack subprocess per provider.
type Runtime struct {
	logger *slog.Logger
	mu     sync.Mutex
	procs  map[provider.ProviderRef]*engineProc
	// catalogDir, when non-empty, is the DURABLE root under which each provider's DuckLake SQLite
	// catalog lives (<catalogDir>/<provider>/catalog.db) — set by `funcdctl dev` under --persist so the
	// catalog metadata survives a restart (the Parquet DATA already persists in blob). Empty ⇒ the
	// catalog lives in the engine's ephemeral temp dir (fresh each boot), the default.
	catalogDir string
}

// Option configures the dev catalog engine runtime.
type Option func(*Runtime)

// WithCatalogDir persists each provider's DuckLake SQLite catalog under dir (durable across restarts)
// instead of the engine's ephemeral temp dir. Empty dir ⇒ ephemeral (the default). Set by `funcdctl
// dev` under --persist, mirroring the durable metastore.
func WithCatalogDir(dir string) Option { return func(r *Runtime) { r.catalogDir = dir } }

// New builds the dev catalog engine runtime.
func New(logger *slog.Logger, opts ...Option) *Runtime {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Runtime{
		logger: logger.With("component", "devengine"),
		procs:  make(map[provider.ProviderRef]*engineProc),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Converge idempotently brings up the engine for spec.Ref and reports readiness. When the dev binary
// carries only the placeholder (no engine built in) it reports not-Ready with a clear reason rather
// than failing the reconcile — dev stays up, catalog is simply unavailable.
func (r *Runtime) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	const op = "devengine.Converge"
	r.mu.Lock()
	defer r.mu.Unlock()

	if !embedengine.Bundled() {
		r.logger.Warn("catalog engine not embedded in this dev build — catalog unavailable",
			"provider", spec.Ref.Name, "remedy", "just build-catalog-engine <os> <arch>")
		return provider.ProviderStatus{Reason: "CatalogEngineNotBundled"}, nil
	}

	if p, ok := r.procs[spec.Ref]; ok && p.alive() {
		return provider.ProviderStatus{Running: 1, Ready: true, Address: p.addr}, nil
	}
	// A dead/absent engine is (re)launched — this re-convergence IS the supervision, matching the
	// prod provider runtime.
	if p, ok := r.procs[spec.Ref]; ok {
		p.stop()
		delete(r.procs, spec.Ref)
	}

	p, err := r.launch(ctx, spec)
	if err != nil {
		return provider.ProviderStatus{}, fault.Wrapf(err, fault.KindOf(err), op, "launch catalog engine")
	}
	r.procs[spec.Ref] = p
	r.logger.Info("dev catalog engine ready", "provider", spec.Ref.Name, "address", p.addr)
	return provider.ProviderStatus{Running: 1, Ready: true, Address: p.addr}, nil
}

// Teardown stops the engine subprocess for ref (idempotent).
func (r *Runtime) Teardown(_ context.Context, ref provider.ProviderRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[ref]
	if !ok {
		return nil
	}
	p.stop()
	delete(r.procs, ref)
	return nil
}

// StopAll tears down every running engine — the dev command calls it on shutdown.
func (r *Runtime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ref, p := range r.procs {
		p.stop()
		delete(r.procs, ref)
	}
}

// engineProc is one running duckdb+quack subprocess.
type engineProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	dir   string
	addr  string
}

func (p *engineProc) alive() bool {
	return p != nil && p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil
}

// stop closes the held-open REPL stdin (so duckdb exits cleanly), kills the process if it lingers,
// and removes the extracted engine dir.
func (p *engineProc) stop() {
	if p == nil {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// launch extracts the engine, writes the init SQL, starts duckdb serving Quack on a free 127.0.0.1
// port, and blocks until the port accepts (or times out).
func (r *Runtime) launch(ctx context.Context, spec provider.ProviderSpec) (*engineProc, error) {
	const op = "devengine.launch"
	dir, err := os.MkdirTemp("", "funcd-dev-catalog-*")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "engine temp dir")
	}
	paths, err := embedengine.Extract(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fault.Wrapf(err, fault.Internal, op, "reserve engine port")
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)

	// The SQLite catalog lives in the ephemeral engine dir by default (fresh each boot); under
	// --persist (catalogDir set) it lives in a durable per-provider subdir OUTSIDE the temp dir, so
	// stop()'s temp-dir cleanup never removes it and the DuckLake reopens it next boot (its Parquet
	// DATA persists in blob). SQLite recovers its own WAL on reopen.
	localCatalog := filepath.Join(dir, "catalog.db")
	if r.catalogDir != "" {
		catDir := filepath.Join(r.catalogDir, string(spec.Ref.Name))
		if merr := os.MkdirAll(catDir, 0o755); merr != nil {
			_ = os.RemoveAll(dir)
			return nil, fault.Wrapf(merr, fault.Internal, op, "durable catalog dir")
		}
		localCatalog = filepath.Join(catDir, "catalog.db")
	}

	initFile := filepath.Join(dir, "init.sql")
	if werr := os.WriteFile(initFile, []byte(buildInitSQL(paths.ExtensionDir, localCatalog, addr, spec.Env)), 0o600); werr != nil {
		_ = os.RemoveAll(dir)
		return nil, fault.Wrapf(werr, fault.Internal, op, "write init.sql")
	}

	// `-init` runs the SQL (LOADs + quack_serve, which serves on a background thread), then duckdb
	// drops into the REPL reading stdin — held open below so the process stays alive serving Quack.
	cmd := exec.CommandContext(ctx, paths.DuckDB, "-init", initFile, ":memory:") //nolint:gosec // paths from our own embedded engine
	cmd.Stdout = io.Discard
	cmd.Stderr = &prefixLogWriter{logger: r.logger, provider: string(spec.Ref.Name)}
	stdin, perr := cmd.StdinPipe()
	if perr != nil {
		_ = os.RemoveAll(dir)
		return nil, fault.Wrapf(perr, fault.Internal, op, "engine stdin pipe")
	}
	if serr := cmd.Start(); serr != nil {
		_ = stdin.Close()
		_ = os.RemoveAll(dir)
		return nil, fault.Wrapf(serr, fault.Internal, op, "start duckdb engine")
	}

	p := &engineProc{cmd: cmd, stdin: stdin, dir: dir, addr: addr}
	if rerr := waitReady(ctx, addr, 20*time.Second); rerr != nil {
		p.stop()
		return nil, fault.Wrapf(rerr, fault.Unavailable, op, "engine did not become ready at %s", addr)
	}
	return p, nil
}

// buildInitSQL assembles the duckdb init script: pin the extension dir, LOAD the engine extensions,
// wire an S3 secret from the injected dev keypair (so httpfs reaches the dev S3 gateway), ATTACH the
// DuckLake (a LOCAL sqlite catalog + the S3 DATA_PATH derived from FUNCD_DUCKLAKE_CATALOG) as the
// default catalog so a consumer's server-side `quack_query` DDL writes gold Parquet, then start the
// Quack server. This mirrors the prod duckdb runtime shim (images/runtime/duckdb/shim.py) minus the
// boto3 catalog recovery/checkpoint — dev starts a fresh local catalog each boot (the Parquet DATA
// persists in blob under --persist); durable catalog metadata across dev restarts is out of scope.
func buildInitSQL(extDir, localCatalog, addr string, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SET extension_directory=%s;\n", sqlStr(extDir))
	b.WriteString("LOAD httpfs;\nLOAD ducklake;\nLOAD quack;\nLOAD sqlite_scanner;\n")

	if key := env["AWS_ACCESS_KEY_ID"]; key != "" {
		endpoint := firstNonEmpty(env["AWS_ENDPOINT_URL_S3"], env["AWS_ENDPOINT_URL"])
		useSSL := strings.HasPrefix(endpoint, "https://")
		host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
		fmt.Fprintf(&b, "CREATE OR REPLACE SECRET funcd_dev_s3 (TYPE s3, KEY_ID %s, SECRET %s, REGION %s, ENDPOINT %s, URL_STYLE 'path', USE_SSL %t);\n",
			sqlStr(key), sqlStr(env["AWS_SECRET_ACCESS_KEY"]), sqlStr(firstNonEmpty(env["AWS_REGION"], "us-east-1")), sqlStr(host), useSSL)
	}

	// ATTACH the DuckLake as `lakehouse` (the same catalog name the prod duckdb shim uses, so a
	// consumer's SQL is portable) so a served `CREATE TABLE lakehouse.…` materializes under gold. The
	// S3 secret above authorizes both the silver read_parquet and the gold Parquet write (the provider
	// keypair owns gold; dev-relaxed writes cover it). A served quack_query runs with `memory` as its
	// default catalog (verified), so the consumer qualifies with `lakehouse` / `USE lakehouse` — no
	// init-time `USE` here (it wouldn't carry to served queries). Needs the S3 secret to exist first.
	if cat := env["FUNCD_DUCKLAKE_CATALOG"]; cat != "" {
		fmt.Fprintf(&b, "ATTACH %s AS lakehouse (DATA_PATH %s);\n",
			sqlStr("ducklake:sqlite:"+localCatalog), sqlStr(dataPathFor(cat)))
	}

	host, port, _ := net.SplitHostPort(addr)
	token := firstNonEmpty(env["FUNCD_QUACK_TOKEN"], env["QUACK_TOKEN"], DevQuackToken)
	fmt.Fprintf(&b, "SELECT listen_url FROM quack_serve(%s, token := %s);\n", sqlStr("quack:"+host+":"+port), sqlStr(token))
	return b.String()
}

// dataPathFor derives the DuckLake Parquet DATA_PATH (s3://<bucket>/<prefix>/) from the catalog URL
// (s3://<bucket>/<prefix>/_ducklake/catalog.db), mirroring the prod shim's _data_path. Without the
// marker it falls back to the URL's parent directory.
func dataPathFor(catalogURL string) string {
	const marker = "/_ducklake/"
	if i := strings.Index(catalogURL, marker); i >= 0 {
		return catalogURL[:i+1]
	}
	if i := strings.LastIndex(catalogURL, "/"); i >= 0 {
		return catalogURL[:i+1]
	}
	return catalogURL
}

// prefixLogWriter forwards duckdb stderr lines to the driver's logger at debug level.
type prefixLogWriter struct {
	logger   *slog.Logger
	provider string
}

func (w *prefixLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			w.logger.Debug("catalog engine", "provider", w.provider, "line", line)
		}
	}
	return len(p), nil
}

// waitReady polls until addr accepts a TCP connection (the Quack server is listening) or ctx/timeout.
func waitReady(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s", timeout, addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// freePort reserves an ephemeral 127.0.0.1 port and returns it (the listener is closed immediately;
// the small reuse race is acceptable for a local dev engine).
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// sqlStr renders s as a single-quoted DuckDB string literal (doubling embedded quotes).
func sqlStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
