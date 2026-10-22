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
	// engineStarts bounds the starts of an engine whose port another process took before it bound it (ADR-0260
	// Decision 3), as dev's S3 port (cmd/funcdctl/dev.go, #627).
	engineStarts = 3
)

// errPortTaken is a start whose engine reported that a port it was given was taken.
var errPortTaken = errors.New("a port of the engine was taken")

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
	// pickPort chooses each port the engine binds; a test swaps it to hand out a taken one.
	pickPort func() (int, error)
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
		logger:   logger.With("component", "sql-devengine"),
		procs:    make(map[provider.ProviderRef]*engineProc),
		bundled:  embedengine.Bundled,
		extract:  embedengine.Extract,
		pickPort: freePort,
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
	out  *engineOutput
	done chan struct{} // closed once the engine has exited and been reaped, after its output was read
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

// launch starts the engine and waits until it is ready. A start whose engine reported a taken port is made again at
// once with new ports, engineStarts starts in all; any other failure ends it (ADR-0260 Decision 3).
func (r *Runtime) launch(ctx context.Context, spec provider.ProviderSpec) (*engineProc, error) {
	for n := 1; ; n++ {
		p, err := r.launchOnce(ctx, spec)
		if err == nil || !errors.Is(err, errPortTaken) || n == engineStarts {
			return p, err
		}
		r.logger.WarnContext(ctx, "a port of the sql engine was taken before it bound it; starting it again with new ports",
			"database", string(spec.Ref.Name), "start", n, "err", err)
	}
}

// launchOnce extracts the engine into a temporary directory with its auth file, starts it on two free loopback ports
// with its data under the root, and waits until it is ready.
func (r *Runtime) launchOnce(ctx context.Context, spec provider.ProviderSpec) (*engineProc, error) {
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
	httpPort, err := r.pickPort()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "reserve the HTTP port")
	}
	raftPort, err := r.pickPort()
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
	out := newEngineOutput(r.logger, string(spec.Ref.Name), addr)
	cmd.Stdout = out
	cmd.Stderr = out
	// Its own process group, so a stop and the reap after a crash reach everything it started (ADR-0167).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "start rqlited")
	}
	p := &engineProc{id: id, cmd: cmd, dir: dir, addr: addr, out: out, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		out.flush()
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

// waitReady waits until the engine said it listens on its own HTTP address, then polls its /readyz, which its auth
// file opens to anonymous callers, until it answers 200. A /readyz answer alone proves nothing: another process can
// hold the port (ADR-0260 Decision 3).
func waitReady(ctx context.Context, p *engineProc, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-p.out.listening:
	case <-p.done:
		return p.ended()
	case <-ctx.Done():
		return ctx.Err()
	}
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
			return p.ended()
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ended is the error of an engine that exited during its start, with its last lines; errPortTaken when one of them
// says a port it was given was taken. done is closed, so the output is read to its end.
func (p *engineProc) ended() error {
	last := strings.Join(p.out.last, " | ")
	if p.out.taken {
		return fmt.Errorf("%w: %s", errPortTaken, last)
	}
	return fmt.Errorf("rqlited exited: %s", last)
}

// freePort chooses a free loopback port and releases it: rqlited binds the ports on its command line itself, and a
// start whose port was taken in between is made again (ADR-0260 Decision 3).
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// engineOutput forwards the engine's output to the logger at debug level and reads the two lines a start waits on
// (ADR-0260 Decision 3): rqlited's `[http] … service listening on <addr>` once it bound its HTTP address (its cluster
// service logs the same words with the Raft address), and Go's `address already in use` when a port it was given was
// taken. The engine's stdout and stderr share it, so os/exec calls Write
// from one goroutine at a time.
type engineOutput struct {
	logger    *slog.Logger
	database  string
	listen    string
	listening chan struct{} // closed at the listen line
	taken     bool          // read once the engine has exited
	last      []string      // the engine's last lines, for the start's error
	partial   string        // a line not ended yet
}

func newEngineOutput(logger *slog.Logger, database, addr string) *engineOutput {
	return &engineOutput{logger: logger, database: database, listen: "service listening on " + addr, listening: make(chan struct{})}
}

func (o *engineOutput) Write(b []byte) (int, error) {
	lines := strings.Split(o.partial+string(b), "\n")
	o.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		o.line(line)
	}
	return len(b), nil
}

// flush reads a last line the engine did not end; the reaper calls it once the engine's output is closed.
func (o *engineOutput) flush() {
	o.line(o.partial)
	o.partial = ""
}

func (o *engineOutput) line(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	o.logger.Debug("sql engine", "database", o.database, "line", line)
	switch {
	case strings.HasPrefix(line, "[http] ") && strings.HasSuffix(strings.TrimSpace(line), o.listen):
		select {
		case <-o.listening:
		default:
			close(o.listening)
		}
	case strings.Contains(line, "address already in use"):
		o.taken = true
	}
	o.last = append(o.last, line)
	if len(o.last) > 3 {
		o.last = o.last[1:]
	}
}
