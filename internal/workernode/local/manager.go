package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// Manager provisions one per-function worker-node local API listener (ADR-0064), lazily and
// idempotently: SocketFor(ns, name) ensures a UDS listener for that caller is serving and returns
// its path — the reconciler sets it as FUNCD_INVOKE_SOCKET so the caller's shim can dial
// context.invoke. The caller Ref is fixed per listener (connection-scoped identity); a pool worker's
// listener (PoolSocketFor) serves each request as the pool member it names in MemberHeader. Close() stops
// every listener. This is the same-node (process/containerd) provisioning; the V2 multi-node lattice
// is a transport swap behind the Invoker.
type Manager struct {
	dir     string
	store   FunctionStore
	invoker Invoker
	authz   auth.Authorizer // the invoke PDP (ADR-0075); nil ⇒ no link::invoke gate (link-as-grant only)
	kv      KV              // the function-facing KV port (ADR-0069); nil ⇒ no /kv routes
	blob    Blob            // the function-facing blob port (ADR-0127); nil ⇒ no /blob routes
	logger  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	serves sync.WaitGroup // one per srv.Serve goroutine; Close waits for them (issue #433)
	mu     sync.Mutex
	closed bool
	active map[string]*serving // "ns/name" (a Function or a pool worker) → its listener
}

// serving is one function's or one pool worker's running local API listener.
type serving struct {
	path    string
	srv     *http.Server
	cancel  context.CancelFunc // closes srv
	done    chan struct{}      // closed once srv.Serve returns, so its listener is closed
	members *poolMembers       // nil for a single function's listener
}

// MemberHeader names, on every request to a pool worker's local API, the pool member it is made for.
const MemberHeader = "X-Funcd-Member"

// NewManager builds a Manager serving sockets under dir. invoker is the (possibly late-bound)
// data-plane forwarder; store backs link resolution; authz (nil-able) is the invoke PDP (ADR-0075)
// the per-sandbox handler asks link::invoke; kv (nil-able) is the function-facing KV port (ADR-0069)
// the per-sandbox handler routes /kv/… to; blob (nil-able) is the function-facing blob port (ADR-0127)
// the handler routes /blob/… to.
func NewManager(dir string, store FunctionStore, invoker Invoker, authz auth.Authorizer, kv KV, blob Blob, logger *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		dir: dir, store: store, invoker: invoker, authz: authz, kv: kv, blob: blob, logger: logger.With("component", "workernode.local"),
		ctx: ctx, cancel: cancel, active: map[string]*serving{},
	}
}

// SocketFor ensures the local API for (ns, name) is serving and returns its socket path. Idempotent.
func (m *Manager) SocketFor(ns v1.NamespaceName, name v1.ObjectName) (string, error) {
	const op = "workernode.local.SocketFor"
	key := string(ns) + "/" + string(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", fault.Unavailablef(op, "the local API manager is closed")
	}
	if s, ok := m.active[key]; ok {
		return s.path, nil
	}
	s, err := m.bind(op, key, m.handlerFor(Ref{Namespace: ns, Function: name}), nil)
	if err != nil {
		return "", err
	}
	return s.path, nil
}

// PoolSocketFor ensures the local API of the pool worker named pool is serving and returns its socket
// path, which every member's process shares. A request naming one of members in MemberHeader is served
// as that member's Ref, as its own SocketFor listener would serve it (invoke, KV, blob); any other
// request, one with no MemberHeader included, is refused 403 before any port is called. A repeated call
// keeps the path and replaces the member set, as members join and leave the pool. Remove(ns, pool) stops it.
func (m *Manager) PoolSocketFor(ns v1.NamespaceName, pool v1.ObjectName, members []v1.ObjectName) (string, error) {
	const op = "workernode.local.PoolSocketFor"
	key := string(ns) + "/" + string(pool)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", fault.Unavailablef(op, "the local API manager is closed")
	}
	if s, ok := m.active[key]; ok {
		if s.members == nil {
			return "", fault.Conflictf(op, "%s is a function's local API, not a pool's", key)
		}
		s.members.set(ns, members, m.handlerFor)
		return s.path, nil
	}
	pm := &poolMembers{key: key, logger: m.logger}
	pm.set(ns, members, m.handlerFor)
	s, err := m.bind(op, key, pm, pm)
	if err != nil {
		return "", err
	}
	return s.path, nil
}

// handlerFor builds the local API handler that serves caller.
func (m *Manager) handlerFor(caller Ref) http.Handler {
	return NewHandler(caller, NewResolver(m.store), m.invoker, m.authz, m.kv, m.blob, m.logger)
}

// bind starts serving h on key's socket and records it in active. The caller holds mu.
func (m *Manager) bind(op, key string, h http.Handler, members *poolMembers) (*serving, error) {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "create socket dir %q", m.dir)
	}
	path := filepath.Join(m.dir, sockName(key))
	sctx, scancel := context.WithCancel(m.ctx) // child of m.ctx: cancelled by Remove OR Close
	ln, srv, err := listen(sctx, op, path, h, m.logger)
	if err != nil {
		scancel()
		return nil, err
	}
	s := &serving{path: path, srv: srv, cancel: scancel, done: make(chan struct{}), members: members}
	m.active[key] = s
	m.serves.Add(1)
	go m.serve(key, s, func() error { return srv.Serve(ln) })
	m.logger.Debug("serving worker-node local API", "function", key, "socket", path)
	return s, nil
}

// poolMembers serves a pool worker's local API: each request as the member it names. The member set is
// swapped whole, so a request reads one consistent set without taking Manager.mu.
type poolMembers struct {
	key      string
	logger   *slog.Logger
	handlers atomic.Pointer[map[v1.ObjectName]http.Handler]
}

// set replaces the member set, keeping the handler of a member that stays. The caller holds Manager.mu.
func (p *poolMembers) set(ns v1.NamespaceName, members []v1.ObjectName, build func(Ref) http.Handler) {
	prev := p.handlers.Load()
	next := make(map[v1.ObjectName]http.Handler, len(members))
	for _, name := range members {
		if prev != nil {
			if h, ok := (*prev)[name]; ok {
				next[name] = h
				continue
			}
		}
		next[name] = build(Ref{Namespace: ns, Function: name})
	}
	p.handlers.Store(&next)
}

func (p *poolMembers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	names := r.Header.Values(MemberHeader)
	var h http.Handler
	if len(names) == 1 && names[0] != "" {
		h = (*p.handlers.Load())[v1.ObjectName(names[0])]
	}
	if h == nil {
		p.logger.Warn("local API call refused: no pool member named", "pool", p.key, "member", names)
		fault.WriteProblem(w, fault.Forbiddenf("workernode.local.pool",
			"the request must name one member of pool %s in %s", p.key, MemberHeader))
		return
	}
	h.ServeHTTP(w, r)
}

// serve runs s's server (srv.Serve on its listener) until it stops. A stop other than Close or Remove is
// logged and drops s from active, so the next SocketFor binds the socket again instead of handing out a
// dead path (issue #546). done is closed before mu is taken: Remove holds mu while it waits on done.
func (m *Manager) serve(key string, s *serving, run func() error) {
	defer m.serves.Done()
	err := run()
	close(s.done)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}
	m.logger.Warn("worker-node local API stopped serving", "function", key, "socket", s.path, "err", err.Error())
	m.mu.Lock()
	if m.active[key] == s {
		delete(m.active, key)
	}
	m.mu.Unlock()
	s.cancel()
}

// Remove stops + deletes the local API listener for (ns, name), a Function's or a pool worker's, if any —
// called when the Function or the pool is deleted, so the socket lifecycle tracks the resource
// (controller-driven, not leaked). It returns once the listener is closed: closing a Unix listener unlinks
// its path, which a Function re-created under the same name binds again (issue #491). It holds mu until
// then, so a concurrent SocketFor binds after it.
func (m *Manager) Remove(ns v1.NamespaceName, name v1.ObjectName) {
	key := string(ns) + "/" + string(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.active[key]
	if !ok {
		return
	}
	delete(m.active, key)
	s.cancel() // closes srv (no leak)
	<-s.done
	_ = os.Remove(s.path)
}

// Close stops every serving listener and returns once each is closed. SocketFor fails after Close.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.serves.Wait()
}

// maxSocketPath is the longest socket path that binds and that a shim can dial: sun_path less its
// terminating NUL (103 bytes on macOS, 107 on Linux).
const maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// CheckDir reports an error when dir is too long to hold the local API sockets. Every socket name has
// the same length, so a dir that passes holds a socket for any function.
func CheckDir(dir string) error {
	if n := len(filepath.Join(dir, sockName(""))); n > maxSocketPath {
		return fault.Invalidf("workernode.local.CheckDir",
			"socket dir %q is too long: its socket paths are %d bytes, over the %d-byte Unix socket limit", dir, n, maxSocketPath)
	}
	return nil
}

// sockName is a fixed-length name for key, so a socket path's length does not depend on the
// namespace and function names (issue #41).
func sockName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:10]) + ".sock"
}

// HandlerHolder is a settable http.Handler indirection. The local API Invoker is built from it
// BEFORE the data-plane handler exists (the reconciler is constructed before the data plane, since
// the data plane wraps the reconciler's activator/Endpoints); Set is called once the data plane is
// built. Until then it answers 503.
type HandlerHolder struct {
	mu sync.RWMutex
	h  http.Handler
}

// Set installs the real target handler.
func (hh *HandlerHolder) Set(h http.Handler) {
	hh.mu.Lock()
	hh.h = h
	hh.mu.Unlock()
}

// ServeHTTP delegates to the installed handler, or 503 until one is set.
func (hh *HandlerHolder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hh.mu.RLock()
	h := hh.h
	hh.mu.RUnlock()
	if h == nil {
		http.Error(w, "data plane not ready", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}
