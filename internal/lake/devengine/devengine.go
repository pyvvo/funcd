//go:build dev

// Package devengine is the process-mode driver of the add-on-provider Runtime port
// (internal/provider) for `funcdctl dev` (ADR-0125 Decision 5, FEAT-0001/F90). Where the prod
// provider runtime supervises the curated `funcd/runtime-duckdb` CONTAINER (ADR-0086/0087), this
// launches the go:embed'd DuckDB+Quack engine (internal/lake/embedengine) as a host SUBPROCESS
// serving the Quack endpoint — a process-mode lake engine: no container, no root, no cgo (a
// subprocess, not linked). It is injected into the Lake reconciler in dev via
// funcd.WithLakeProviderRuntime, so the same reconciler + ProviderSpec drive it as in prod.
//
// Converge is idempotent (one engine per provider Ref; a live one is reused); Teardown stops it.
// The engine LOADs the extensions and starts `quack_serve` in a background thread — the driver holds
// the duckdb REPL's stdin open so the process stays alive serving until Teardown closes it.
//
// SCOPE (ADR-0125 M2): this delivers the engine LIFECYCLE (extract → serve Quack → ready → stop) and
// the S3 secret wiring. Attaching the DuckLake and running a live query *through* a function is the
// deferred dev-lake-query live lane; the engine here serves Quack so that lane can build on it.
package devengine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/lake/embedengine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

const (
	// reapGrace is how long the reap at New waits after SIGTERM before SIGKILL: the process driver's default stop
	// grace, which `funcdctl dev` uses (ADR-0167).
	reapGrace = 3 * time.Second
	// serveTimeout bounds an engine's start, until its quack_serve row.
	serveTimeout = 20 * time.Second
)

// DevQuackToken is the fixed local Quack auth token the dev engine serves with when spec.Env carries
// none. `funcdctl dev` writes it into the synthesized lake's QUACK_TOKEN Secret so the consumer
// (resolveLakeEnv → FUNCD_LAKE_<ALIAS>_TOKEN) presents the SAME token the engine serves with.
const DevQuackToken = "funcd-dev-lake"

// Runtime is the dev process-mode provider.Runtime: at most one duckdb+quack subprocess per provider.
type Runtime struct {
	logger *slog.Logger
	mu     sync.Mutex
	procs  map[provider.ProviderRef]*engineProc
	// lakeDir, when non-empty, is the DURABLE root under which each provider's DuckLake SQLite
	// catalog file lives (<lakeDir>/<provider>/catalog.db) — set by `funcdctl dev` under --persist so the
	// lake metadata survives a restart (the Parquet DATA already persists in blob). Empty ⇒ the
	// catalog file lives in the engine's ephemeral temp dir (fresh each boot), the default.
	lakeDir string
	// stateDir, when non-empty, holds engines.json, the saved registry of the engines this runtime started, so New
	// reaps the engines a crashed run left behind (ADR-0167). reg is that registry; nil saves nothing.
	stateDir string
	reg      *procreg.Registry
	// bundled and extract reach the embedded engine; a test swaps them for a fake engine.
	bundled func() bool
	extract func(dir string) (embedengine.Paths, error)
}

// Option configures the dev lake engine runtime.
type Option func(*Runtime)

// WithLakeDir persists each provider's DuckLake SQLite catalog under dir (durable across restarts)
// instead of the engine's ephemeral temp dir. Empty dir ⇒ ephemeral (the default). Set by `funcdctl
// dev` under --persist, mirroring the durable metastore.
func WithLakeDir(dir string) Option { return func(r *Runtime) { r.lakeDir = dir } }

// WithStateDir saves the engines this runtime starts in <dir>/engines.json, so New reaps the engines a crashed run
// left there (ADR-0167).
func WithStateDir(dir string) Option { return func(r *Runtime) { r.stateDir = dir } }

// New builds the dev lake engine runtime. With a state dir it first reaps the engines an earlier run saved there,
// and fails with fault.Conflict while another process holds that registry.
func New(logger *slog.Logger, opts ...Option) (*Runtime, error) {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Runtime{
		logger:  logger.With("component", "devengine"),
		procs:   make(map[provider.ProviderRef]*engineProc),
		bundled: embedengine.Bundled,
		extract: embedengine.Extract,
	}
	for _, o := range opts {
		o(r)
	}
	if r.stateDir == "" {
		return r, nil
	}
	reg, err := procreg.Open(r.stateDir, "engines")
	if err != nil {
		return nil, err
	}
	if _, err := reg.Reap(context.Background(), reapGrace); err != nil {
		_ = reg.Close()
		return nil, err
	}
	r.reg = reg
	return r, nil
}

// Converge idempotently brings up the engine for spec.Ref and reports readiness. When the dev binary
// carries only the placeholder (no engine built in) it reports not-Ready with a clear reason rather
// than failing the reconcile — dev stays up, lake is simply unavailable.
func (r *Runtime) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	const op = "devengine.Converge"
	r.mu.Lock()
	defer r.mu.Unlock()

	if spec.Storage != nil {
		return provider.ProviderStatus{}, fault.Invalidf(op, "the dev lake engine has no durable storage (ADR-0231)")
	}
	if !r.bundled() {
		r.logger.Warn("lake engine not embedded in this dev build — lake unavailable",
			"provider", spec.Ref.Name, "remedy", "just build-lake-engine <os> <arch>")
		return provider.ProviderStatus{Reason: "LakeEngineNotBundled"}, nil
	}

	if p, ok := r.procs[spec.Ref]; ok && p.alive() {
		return provider.ProviderStatus{Running: 1, Ready: true, Address: p.addr}, nil
	}
	// A dead/absent engine is (re)launched — this re-convergence IS the supervision, matching the
	// prod provider runtime.
	if p, ok := r.procs[spec.Ref]; ok {
		r.stopLocked(p)
		delete(r.procs, spec.Ref)
	}

	p, err := r.launch(ctx, spec)
	if err != nil {
		return provider.ProviderStatus{}, fault.Wrapf(err, fault.KindOf(err), op, "launch lake engine")
	}
	r.procs[spec.Ref] = p
	r.logger.Info("dev lake engine ready", "provider", spec.Ref.Name, "address", p.addr)
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
	r.stopLocked(p)
	delete(r.procs, ref)
	return nil
}

// StopAll tears down every running engine and releases the registry — the dev command calls it on shutdown.
func (r *Runtime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ref, p := range r.procs {
		r.stopLocked(p)
		delete(r.procs, ref)
	}
	if r.reg != nil {
		_ = r.reg.Close()
		r.reg = nil
	}
}

// stopLocked stops an engine and forgets its registry entry; caller holds r.mu.
func (r *Runtime) stopLocked(p *engineProc) {
	p.stop()
	if r.reg != nil {
		_ = r.reg.Delete(p.id)
	}
}

// save records a started engine in the registry; caller holds r.mu.
func (r *Runtime) save(p *engineProc, token string) error {
	if r.reg == nil {
		return nil
	}
	pid := p.cmd.Process.Pid
	st, err := procreg.StartTime(pid)
	if err != nil {
		return err
	}
	boot, err := procreg.BootID()
	if err != nil {
		return err
	}
	return r.reg.Put(procreg.Entry{
		ID: p.id, PID: pid, PGID: pid, StartTime: st, BootID: boot, Token: token, Files: []string{p.dir},
	})
}

// engineProc is one running duckdb+quack subprocess.
type engineProc struct {
	id      string // the instance ID drawn per engine start; its dir name and -init argv carry it (ADR-0167)
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	dir     string
	addr    string           // the address the engine's quack_serve row named
	watcher *rowWatcher      // reads that row from the engine's stdout
	stderr  *prefixLogWriter // the engine's error output, whose last lines a failed start reports
	done    chan struct{}    // closed once the engine has exited and been reaped
}

// alive reads done, not cmd.ProcessState: only cmd.Wait fills ProcessState, and launch's reaper
// goroutine is its sole caller, so an engine that crashes between Converges is seen dead.
func (p *engineProc) alive() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
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
		<-p.done
	}
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// launch extracts the engine, writes the init SQL, starts duckdb serving Quack on a 127.0.0.1 port it chooses, and
// blocks until the engine names that address on its stdout (or ends, or times out): the port is the engine's own from
// the moment it binds, so no other process can take it (ADR-0262 Decision 4).
func (r *Runtime) launch(ctx context.Context, spec provider.ProviderSpec) (*engineProc, error) {
	const op = "devengine.launch"
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	id := hex.EncodeToString(raw[:])
	token := "funcd-engine-" + id
	dir, err := os.MkdirTemp("", token+"-*")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "engine temp dir")
	}
	paths, err := r.extract(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	// The SQLite catalog lives in the ephemeral engine dir by default (fresh each boot); under
	// --persist (lakeDir set) it lives in a durable per-provider subdir OUTSIDE the temp dir, so
	// stop()'s temp-dir cleanup never removes it and the DuckLake reopens it next boot (its Parquet
	// DATA persists in blob). SQLite recovers its own WAL on reopen.
	catalogFile := filepath.Join(dir, "catalog.db")
	if r.lakeDir != "" {
		catDir := filepath.Join(r.lakeDir, string(spec.Ref.Name))
		if merr := os.MkdirAll(catDir, 0o755); merr != nil {
			_ = os.RemoveAll(dir)
			return nil, fault.Wrapf(merr, fault.Internal, op, "durable lake dir")
		}
		catalogFile = filepath.Join(catDir, "catalog.db")
	}

	initFile := filepath.Join(dir, "init.sql")
	if werr := os.WriteFile(initFile, []byte(buildInitSQL(paths.ExtensionDir, catalogFile, spec.Env)), 0o600); werr != nil {
		_ = os.RemoveAll(dir)
		return nil, fault.Wrapf(werr, fault.Internal, op, "write init.sql")
	}

	// `-init` runs the SQL (LOADs + quack_serve, which serves on a background thread), then duckdb
	// drops into the REPL reading stdin — held open below so the process stays alive serving Quack.
	cmd := exec.CommandContext(ctx, paths.DuckDB, "-init", initFile, ":memory:") //nolint:gosec // paths from our own embedded engine
	// DuckDB 2.0's CLI renders for an AI agent when an agent's variable is set and stdout is no terminal (ADR-0262
	// Decision 6).
	cmd.Env = append(os.Environ(), "DUCKDB_AGENT_MODE=0")
	watcher := &rowWatcher{seen: make(chan struct{})}
	stderr := &prefixLogWriter{logger: r.logger, provider: string(spec.Ref.Name)}
	cmd.Stdout = watcher
	cmd.Stderr = stderr
	// Its own process group, so the reap after a crash reaches everything the engine started (ADR-0167).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

	p := &engineProc{id: id, cmd: cmd, stdin: stdin, dir: dir, watcher: watcher, stderr: stderr, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		stderr.flush()
		close(p.done)
	}()
	if serr := r.save(p, token); serr != nil {
		p.stop()
		return nil, fault.Wrapf(serr, fault.KindOf(serr), op, "save engine %s", id)
	}
	addr, rerr := waitServing(ctx, p, serveTimeout)
	if rerr != nil {
		r.stopLocked(p)
		return nil, fault.Wrapf(rerr, fault.Unavailable, op, "engine did not become ready")
	}
	p.addr = addr
	return p, nil
}

// waitServing returns the address the watcher stored, or the engine's error output when it ends first. The engine's
// own row names the port it took, so a start reports no address the engine does not serve (ADR-0262 Decision 4).
func waitServing(ctx context.Context, p *engineProc, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-p.watcher.seen:
		return p.watcher.addr, nil
	case <-p.done:
		return "", fmt.Errorf("the engine ended before it served: %s", strings.Join(p.stderr.last, " | "))
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for the engine's quack_serve row: %w", ctx.Err())
	}
}

// serveRow matches the address on a complete line of the engine's listen_url row.
var serveRow = regexp.MustCompile(`http://(127\.0\.0\.1:[1-9][0-9]*)`)

// maxLine bounds the unended line the watcher keeps; the listen_url row is far shorter.
const maxLine = 4096

// rowWatcher buffers the engine's standard output to whole lines. On the first line that serveRow matches, it stores
// the address and closes seen; it never blocks the stdout copier. Only that copier calls Write, and addr is read once
// seen is closed.
type rowWatcher struct {
	seen    chan struct{}
	addr    string
	matched bool
	line    []byte
}

func (w *rowWatcher) Write(b []byte) (int, error) {
	n := len(b)
	for !w.matched && len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			w.line = append(w.line, b...)
			if len(w.line) > maxLine {
				w.line = append(w.line[:0], w.line[len(w.line)-maxLine:]...)
			}
			break
		}
		w.line = append(w.line, b[:i]...)
		if m := serveRow.FindSubmatch(w.line); m != nil {
			w.addr = string(m[1])
			w.matched = true
			close(w.seen)
		}
		w.line = w.line[:0]
		b = b[i+1:]
	}
	return n, nil
}

// buildInitSQL assembles the duckdb init script: pin the extension dir, LOAD the engine extensions,
// wire an S3 secret from the injected dev keypair (so httpfs reaches the dev S3 gateway), ATTACH the
// DuckLake (a LOCAL SQLite catalog file + the S3 DATA_PATH derived from FUNCD_DUCKLAKE_CATALOG) as the
// default lake so a consumer's server-side `quack_query` DDL writes gold Parquet, then start the
// Quack server. This mirrors the prod duckdb runtime shim (images/runtime/duckdb/shim.py) minus the
// boto3 lake recovery/checkpoint — dev starts a fresh local lake each boot (the Parquet DATA
// persists in blob under --persist); durable lake metadata across dev restarts is out of scope.
// It serves on 127.0.0.1:0 and attaches the lake with DUCKLAKE_VERSION '1.1' (ADR-0262).
func buildInitSQL(extDir, catalogFile string, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SET extension_directories=[%s];\n", sqlStr(extDir))
	b.WriteString("LOAD httpfs;\nLOAD ducklake;\nLOAD quack;\nLOAD sqlite_scanner;\n")

	if key := env["AWS_ACCESS_KEY_ID"]; key != "" {
		endpoint := firstNonEmpty(env["AWS_ENDPOINT_URL_S3"], env["AWS_ENDPOINT_URL"])
		useSSL := strings.HasPrefix(endpoint, "https://")
		host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
		fmt.Fprintf(&b, "CREATE OR REPLACE SECRET funcd_dev_s3 (TYPE s3, KEY_ID %s, SECRET %s, REGION %s, ENDPOINT %s, URL_STYLE 'path', USE_SSL %t);\n",
			sqlStr(key), sqlStr(env["AWS_SECRET_ACCESS_KEY"]), sqlStr(firstNonEmpty(env["AWS_REGION"], "us-east-1")), sqlStr(host), useSSL)
	}

	// ATTACH the DuckLake as `lakehouse` (the same lake name the prod duckdb shim uses, so a
	// consumer's SQL is portable) so a served `CREATE TABLE lakehouse.…` materializes under gold. The
	// S3 secret above authorizes both the silver read_parquet and the gold Parquet write (the provider
	// keypair owns gold; dev-relaxed writes cover it). A served quack_query runs with `memory` as its
	// default lake (verified), so the consumer qualifies with `lakehouse` / `USE lakehouse` — no
	// init-time `USE` here (it wouldn't carry to served queries). Needs the S3 secret to exist first.
	if cat := env["FUNCD_DUCKLAKE_CATALOG"]; cat != "" {
		// OVERRIDE_DATA_PATH: a lake restored under another prefix (ADR-0222 asLocation) records its old one
		fmt.Fprintf(&b, "ATTACH %s AS lakehouse (DATA_PATH %s, OVERRIDE_DATA_PATH TRUE, DUCKLAKE_VERSION '1.1');\n",
			sqlStr("ducklake:sqlite:"+catalogFile), sqlStr(dataPathFor(cat)))
	}

	token := firstNonEmpty(env["FUNCD_QUACK_TOKEN"], env["QUACK_TOKEN"], DevQuackToken)
	fmt.Fprintf(&b, "SELECT listen_url FROM quack_serve('quack:127.0.0.1:0', token := %s);\n", sqlStr(token))
	return b.String()
}

// dataPathFor derives the DuckLake Parquet DATA_PATH (s3://<bucket>/<prefix>/) from the lake URL
// (s3://<bucket>/<prefix>/_ducklake/catalog.db), mirroring the prod shim's _data_path. Without the
// marker it falls back to the URL's parent directory.
func dataPathFor(lakeURL string) string {
	const marker = "/_ducklake/"
	if i := strings.Index(lakeURL, marker); i >= 0 {
		return lakeURL[:i+1]
	}
	if i := strings.LastIndex(lakeURL, "/"); i >= 0 {
		return lakeURL[:i+1]
	}
	return lakeURL
}

// ansiColor is a terminal color code, which the duckdb CLI writes around its messages.
var ansiColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// prefixLogWriter forwards duckdb stderr lines to the driver's logger at debug level and keeps the last ones, which a
// failed start reports. Only the stderr copier calls Write; last is read once the engine has exited.
type prefixLogWriter struct {
	logger   *slog.Logger
	provider string
	last     []string
	partial  string // a line not ended yet
}

func (w *prefixLogWriter) Write(p []byte) (int, error) {
	lines := strings.Split(w.partial+string(p), "\n")
	w.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		w.line(line)
	}
	return len(p), nil
}

// flush reads a last line the engine did not end; the reaper calls it once the engine's output is closed.
func (w *prefixLogWriter) flush() {
	w.line(w.partial)
	w.partial = ""
}

func (w *prefixLogWriter) line(line string) {
	line = strings.TrimSpace(ansiColor.ReplaceAllString(line, ""))
	if line == "" {
		return
	}
	w.logger.Debug("lake engine", "provider", w.provider, "line", line)
	w.last = append(w.last, line)
	if len(w.last) > 3 {
		w.last = w.last[1:]
	}
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
