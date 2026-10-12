//go:build dev

// Package devengine runs SQL databases for `funcdctl dev` (ADR-0237 Decision 2): a provider.Runtime and
// provider.Volumes for owner kind SQLDatabase that starts the embedded rqlited (internal/sql/embedengine) as a host
// subprocess bound to loopback, with the arguments and auth file of the container entrypoint (engineargs), so the
// SQL reconciler, facade and roles work as in production with no container.
package devengine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
	"github.com/pyvvo/funcd/internal/services/sql/engineargs"
	"github.com/pyvvo/funcd/internal/sql/embedengine"
)

const (
	// reapGrace is how long a stop or the reap at New waits after SIGTERM before SIGKILL (ADR-0167).
	reapGrace = 3 * time.Second
	// readyTimeout bounds an engine's start, a fresh one or one replaying its log.
	readyTimeout = 30 * time.Second
)

// Runtime runs at most one rqlited per database.
type Runtime struct {
	logger   *slog.Logger
	mu       sync.Mutex
	procs    map[provider.ProviderRef]*engineProc
	dataDir  string // the data root: <dataDir>/<ns>/<name> per database
	tempData bool   // dataDir was made by New and is removed by StopAll
	stateDir string
	reg      *procreg.Registry
	// bundled and extract reach the embedded engine; a test swaps them for a fake engine.
	bundled func() bool
	extract func(dir string) (string, error)
}

// Option configures the runtime.
type Option func(*Runtime)

// WithStateDir saves the engines this runtime starts in <dir>/sql-engines.json, so New reaps the engines a killed run
// left there (ADR-0167).
func WithStateDir(dir string) Option { return func(r *Runtime) { r.stateDir = dir } }

// WithDataDir is the data root; "" is a temporary root that StopAll removes.
func WithDataDir(dir string) Option { return func(r *Runtime) { r.dataDir = dir } }

// New builds the runtime. With a state dir it first reaps the engines an earlier run saved there, and fails with
// fault.Conflict while another process holds that registry.
func New(logger *slog.Logger, opts ...Option) (*Runtime, error) {
	const op = "devengine.New"
	if logger == nil {
		logger = slog.Default()
	}
	r := &Runtime{
		logger:  logger.With("component", "sql-devengine"),
		procs:   make(map[provider.ProviderRef]*engineProc),
		bundled: embedengine.Bundled,
		extract: embedengine.Extract,
	}
	for _, o := range opts {
		o(r)
	}
	if r.dataDir == "" {
		dir, err := os.MkdirTemp("", "funcd-dev-sql-*")
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "make the data root")
		}
		r.dataDir, r.tempData = dir, true
	}
	if r.stateDir == "" {
		return r, nil
	}
	reg, err := procreg.Open(r.stateDir, "sql-engines")
	if err != nil {
		r.removeTempData()
		return nil, err
	}
	if _, err := reg.Reap(context.Background(), reapGrace); err != nil {
		_ = reg.Close()
		r.removeTempData()
		return nil, err
	}
	r.reg = reg
	return r, nil
}

var (
	_ provider.Runtime = (*Runtime)(nil)
	_ provider.Volumes = (*Runtime)(nil)
)

// Converge starts spec's engine unless a live one serves it, and reports it Ready at 127.0.0.1:<port>. The storage
// spec's path, uid and gid are a container's and are ignored: the data lives under the data root.
func (r *Runtime) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	const op = "devengine.Converge"
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.bundled() {
		return provider.ProviderStatus{Reason: "SQLEngineNotBundled"}, nil
	}
	if p, ok := r.procs[spec.Ref]; ok {
		if p.alive() {
			return provider.ProviderStatus{Running: 1, Ready: true, Address: p.addr}, nil
		}
		r.stopLocked(p)
		delete(r.procs, spec.Ref)
	}
	p, err := r.launch(ctx, spec)
	if err != nil {
		return provider.ProviderStatus{}, fault.Wrapf(err, fault.KindOf(err), op, "start the engine of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
	}
	r.procs[spec.Ref] = p
	r.logger.Info("dev sql engine ready", "database", string(spec.Ref.Name), "address", p.addr)
	return provider.ProviderStatus{Running: 1, Ready: true, Address: p.addr}, nil
}

// Teardown stops ref's engine; its data stays.
func (r *Runtime) Teardown(_ context.Context, ref provider.ProviderRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.procs[ref]; ok {
		r.stopLocked(p)
		delete(r.procs, ref)
	}
	return nil
}

// Reclaim removes ref's data; Conflict while its engine runs.
func (r *Runtime) Reclaim(_ context.Context, ref provider.ProviderRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.procs[ref]; ok && p.alive() {
		return fault.Conflictf("devengine.Reclaim", "the engine of %s/%s still runs", ref.Namespace, ref.Name)
	}
	return os.RemoveAll(r.dataPath(ref))
}

// List returns the databases that have data under the root, sorted.
func (r *Runtime) List(context.Context) ([]provider.ProviderRef, error) {
	nss, err := os.ReadDir(r.dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "devengine.List", "read %s", r.dataDir)
	}
	var out []provider.ProviderRef
	for _, ns := range nss {
		if !ns.IsDir() {
			continue
		}
		dbs, err := os.ReadDir(filepath.Join(r.dataDir, ns.Name()))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, "devengine.List", "read %s", ns.Name())
		}
		for _, db := range dbs {
			if db.IsDir() {
				out = append(out, provider.ProviderRef{Namespace: v1.NamespaceName(ns.Name()), Name: v1.ObjectName(db.Name())})
			}
		}
	}
	slices.SortFunc(out, func(a, b provider.ProviderRef) int {
		return strings.Compare(string(a.Namespace)+"/"+string(a.Name), string(b.Namespace)+"/"+string(b.Name))
	})
	return out, nil
}

// StopAll stops every engine, releases the registry and removes a temporary data root; the dev command calls it on
// shutdown.
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
	r.removeTempData()
}

func (r *Runtime) removeTempData() {
	if r.tempData {
		_ = os.RemoveAll(r.dataDir)
	}
}

// dataPath is ref's data directory. Both name parts are validated DNS labels, so it cannot leave the root.
func (r *Runtime) dataPath(ref provider.ProviderRef) string {
	return filepath.Join(r.dataDir, string(ref.Namespace), string(ref.Name))
}

// stopLocked stops an engine and forgets its registry entry; the caller holds r.mu.
func (r *Runtime) stopLocked(p *engineProc) {
	p.stop()
	if r.reg != nil {
		_ = r.reg.Delete(p.id)
	}
}

// engineProc is one running rqlited.
type engineProc struct {
	id   string // drawn per start; the engine's temporary directory, in its argv, carries it (ADR-0167)
	cmd  *exec.Cmd
	dir  string // the engine binary and its auth file
	addr string
	done chan struct{} // closed once the engine has exited and been reaped
}

func (p *engineProc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop sends SIGTERM to the engine's group, so rqlited closes its store cleanly, then SIGKILL after the grace, and
// removes its temporary directory.
func (p *engineProc) stop() {
	if p.alive() {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(reapGrace):
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	}
	_ = os.RemoveAll(p.dir)
}

// launch extracts the engine into a temporary directory with its auth file, starts it on two free loopback ports
// with its data under the root, and waits until it is ready.
func (r *Runtime) launch(ctx context.Context, spec provider.ProviderSpec) (*engineProc, error) {
	const op = "devengine.launch"
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	id := hex.EncodeToString(raw[:])
	token := "funcd-sql-engine-" + id
	dir, err := os.MkdirTemp("", token+"-*")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "make the engine directory")
	}
	p, err := r.start(ctx, spec, id, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := r.save(p, token); err != nil {
		p.stop()
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "save engine %s", id)
	}
	if err := waitReady(ctx, p, readyTimeout); err != nil {
		r.stopLocked(p)
		return nil, fault.Wrapf(err, fault.Unavailable, op, "the engine did not become ready at %s", p.addr)
	}
	return p, nil
}

func (r *Runtime) start(ctx context.Context, spec provider.ProviderSpec, id, dir string) (*engineProc, error) {
	const op = "devengine.launch"
	bin, err := r.extract(dir)
	if err != nil {
		return nil, err
	}
	auth, err := engineargs.AuthFile(spec.Env["FUNCD_SQL_PASSWORD"])
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "the engine's auth file")
	}
	authFile := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authFile, auth, 0o600); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "write the auth file")
	}
	// As in the engine container: rqlited's data in db/, the change feed's epoch beside it.
	data := r.dataPath(spec.Ref)
	if err := os.MkdirAll(filepath.Join(data, "db"), 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "make the data directory")
	}
	httpPort, err := freePort()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "reserve the HTTP port")
	}
	raftPort, err := freePort()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "reserve the Raft port")
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort))
	args := engineargs.Args{
		NodeID: string(spec.Ref.Name), HTTPAddr: addr, HTTPAdvAddr: addr,
		RaftAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(raftPort)), AuthFile: authFile, DataDir: filepath.Join(data, "db"),
	}
	if endpoint := spec.Env["FUNCD_SQL_CDC_ENDPOINT"]; endpoint != "" {
		if args.CDCConfig, err = changeFeed(endpoint, data, dir); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "the change feed's config")
		}
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), bin, args.Argv()...) //nolint:gosec // our own embedded engine
	cmd.Stdout = &lineLogger{logger: r.logger, database: string(spec.Ref.Name)}
	cmd.Stderr = cmd.Stdout
	// Its own process group, so a stop and the reap after a crash reach everything it started (ADR-0167).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "start rqlited")
	}
	p := &engineProc{id: id, cmd: cmd, dir: dir, addr: addr, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// changeFeed writes the change feed's config into the engine's directory, under the epoch kept beside its data, as the
// container entrypoint does (ADR-0235 Decision 1), and returns its path.
func changeFeed(endpoint, data, dir string) (string, error) {
	epoch, err := engineargs.Epoch(os.ReadFile, os.WriteFile, filepath.Join(data, "epoch"))
	if err != nil {
		return "", err
	}
	cfg, err := engineargs.CDCConfig(endpoint, epoch)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "cdc.json")
	return path, os.WriteFile(path, cfg, 0o600)
}

// save records a started engine in the registry; the caller holds r.mu.
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
	return r.reg.Put(procreg.Entry{ID: p.id, PID: pid, PGID: pid, StartTime: st, BootID: boot, Token: token, Files: []string{p.dir}})
}

// waitReady polls the engine's /readyz, which its auth file opens to anonymous callers, until it answers 200.
func waitReady(ctx context.Context, p *engineProc, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := "http://" + p.addr + "/readyz"
	client := httpx.NodeClient(time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			httpx.CloseBody(resp.Body)
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-p.done:
			return fmt.Errorf("rqlited exited")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// freePort reserves a loopback port; the small race until the engine binds it is acceptable on a dev host.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// lineLogger forwards the engine's output to the logger at debug level.
type lineLogger struct {
	logger   *slog.Logger
	database string
}

func (w *lineLogger) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			w.logger.Debug("sql engine", "database", w.database, "line", line)
		}
	}
	return len(b), nil
}
