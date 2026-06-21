package local

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Manager provisions one per-function worker-node local API listener (ADR-0064), lazily and
// idempotently: SocketFor(ns, name) ensures a UDS listener for that caller is serving and returns
// its path — the reconciler sets it as FUNCD_INVOKE_SOCKET so the caller's shim can dial
// context.invoke. The caller Ref is fixed per listener (connection-scoped identity). Close() stops
// every listener. This is the same-node (process/containerd) provisioning; the V2 multi-node lattice
// is a transport swap behind the Invoker.
type Manager struct {
	dir     string
	store   FunctionStore
	invoker Invoker
	logger  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	active map[string]*serving // "ns/name" → its listener
}

// serving is one function's running local API listener.
type serving struct {
	path   string
	srv    *http.Server
	cancel context.CancelFunc // stops this listener's watcher goroutine (and closes srv)
}

// NewManager builds a Manager serving sockets under dir. invoker is the (possibly late-bound)
// data-plane forwarder; store backs link resolution.
func NewManager(dir string, store FunctionStore, invoker Invoker, logger *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		dir: dir, store: store, invoker: invoker, logger: logger.With("component", "workernode.local"),
		ctx: ctx, cancel: cancel, active: map[string]*serving{},
	}
}

// SocketFor ensures the local API for (ns, name) is serving and returns its socket path. Idempotent.
func (m *Manager) SocketFor(ns v1.NamespaceName, name v1.ObjectName) (string, error) {
	const op = "workernode.local.SocketFor"
	key := string(ns) + "/" + string(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.active[key]; ok {
		return s.path, nil
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "create socket dir %q", m.dir)
	}
	path := filepath.Join(m.dir, sockName(key))
	_ = os.Remove(path) // clear a stale socket
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "listen on %q", path)
	}
	h := NewHandler(Ref{Namespace: ns, Function: name}, NewResolver(m.store), m.invoker)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	sctx, scancel := context.WithCancel(m.ctx) // child of m.ctx: cancelled by Remove OR Close
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-sctx.Done()
		_ = srv.Close()
	}()
	m.active[key] = &serving{path: path, srv: srv, cancel: scancel}
	m.logger.Debug("serving worker-node local API", "function", key, "socket", path)
	return path, nil
}

// Remove stops + deletes the local API listener for (ns, name), if any — called when the Function is
// deleted, so the socket lifecycle tracks the resource (controller-driven, not leaked).
func (m *Manager) Remove(ns v1.NamespaceName, name v1.ObjectName) {
	key := string(ns) + "/" + string(name)
	m.mu.Lock()
	s, ok := m.active[key]
	delete(m.active, key)
	m.mu.Unlock()
	if !ok {
		return
	}
	s.cancel() // stops the watcher goroutine, which closes srv (no leak)
	_ = os.Remove(s.path)
}

// Close stops every serving listener.
func (m *Manager) Close() { m.cancel() }

func sockName(key string) string {
	return strings.ReplaceAll(key, "/", "-") + ".sock"
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
