package gateway

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// Manager owns the per-Lake lake PEP proxies (ADR-0137): it runs ONE node-private
// http.Server per lake, each fronting that lake's engine. The Lake reconciler calls
// Listen on every pass to bind the lake's listener on its recorded port (ADR-0162), Ensure on its Ready
// branch to target the engine, Suspend while it is not Ready, and Release on delete; it calls Remove only once
// no Function binds a deleted lake, so the URL internal functions are injected with
// (FUNCD_LAKE_<ALIAS>_URL) never changes within a daemon run.
// One listener endpoint per lake fixes the PEP's target lake by the endpoint (the proxy takes
// the namespace from the resolved principal — see NewLakeProxy).
type Manager struct {
	keys     LakeKeys
	pdp      auth.Authorizer
	log      *slog.Logger
	proxyLog *slog.Logger    // the proxies' logger (component lake.gateway)
	engines  *http.Transport // the proxies' engine transport, kept across retargets

	// host is the address each proxy binds and the host injected into functions as FUNCD_LAKE_<ALIAS>_URL: the
	// container bridge address under containerd, node-private, which a worker in its own netns reaches (ADR-0238), and
	// 127.0.0.1 in the process runtime (funcdctl dev, shared loopback).
	host string

	mu      sync.Mutex
	servers map[string]*managedProxy // key = "ns/name"
}

// managedProxy is one running node-private proxy: its lake, its listener, its serving http.Server, its handler
// slot, and the (upstream, engineToken) it targets — so Ensure can detect a change and retarget it.
type managedProxy struct {
	ns          v1.NamespaceName
	name        v1.ObjectName
	listener    net.Listener
	server      *http.Server
	handler     *retargetable
	upstream    string
	engineToken string
	released    bool // the Lake is deleted; the listener is kept for the Functions that bind it (ADR-0162)
}

// retargetable is a proxy's handler slot. Listen installs a 503 handler; Ensure swaps in a proxy for the engine
// target, and again when the engine moves or its shared token rotates, keeping the listener, so the URL already
// injected into consumers stays valid: nothing re-provisions a consumer when a lake's engine changes.
type retargetable struct {
	mu sync.RWMutex
	h  http.Handler
}

func (s *retargetable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.h
	s.mu.RUnlock()
	h.ServeHTTP(w, r)
}

func (s *retargetable) set(h http.Handler) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

// NewManager builds the lake proxy Manager over the shared token resolver + PDP (ADR-0137). The
// same LakeKeys/Authorizer back every per-lake proxy; each proxy differs only in its
// EngineTarget (the lake ref + its netns engine URL + shared engine token). host is the address each proxy binds
// and publishes (see Manager); empty ⇒ 127.0.0.1.
func NewManager(host string, keys LakeKeys, pdp auth.Authorizer, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return &Manager{
		keys:     keys,
		pdp:      pdp,
		log:      log.With("component", "lake.gateway.manager"),
		proxyLog: log.With("component", "lake.gateway"),
		engines:  httpx.NodeTransport(),
		host:     host,
		servers:  make(map[string]*managedProxy),
	}
}

// managerKey is the servers-map key for a lake ref: "ns/name".
func managerKey(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name)
}

// Ensure guarantees a node-private proxy is running for lake, fronting upstream with engineToken,
// and returns the bare "<host>:<port>" host:port the function injects as FUNCD_LAKE_<ALIAS>_URL
// (its handler wraps it in quack://; the engine endpoint was bare too). If a
// proxy already runs for the lake it is reused (idempotent — a re-reconcile does not rebind); if the
// upstream or engineToken differs, the running proxy is retargeted in place and its URL stays the same.
// Thread-safe.
func (m *Manager) Ensure(lake auth.EntityRef, upstream, engineToken string) (string, error) {
	const op = "lake.gateway.Manager.Ensure"
	key := managerKey(lake.Namespace, lake.Name)

	m.mu.Lock()
	defer m.mu.Unlock()

	target := func() http.Handler {
		return newLakeProxy(m.keys, m.pdp, EngineTarget{Lake: lake, Upstream: upstream, EngineToken: engineToken}, m.engines, m.proxyLog)
	}
	if existing, ok := m.servers[key]; ok {
		existing.released = false
		if existing.upstream != upstream || existing.engineToken != engineToken {
			// The engine moved or its shared token rotated: retarget the SAME listener, so the URL
			// consumers already hold keeps working.
			existing.handler.set(target())
			existing.upstream, existing.engineToken = upstream, engineToken
			m.log.Debug("lake proxy retargeted", "lake", key, "upstream", upstream)
		}
		return m.publishURL(existing.listener.Addr()), nil
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(m.host, "0"))
	if err != nil {
		return "", fault.Unavailablef(op, "bind node-private lake proxy listener for %s: %v", key, err)
	}
	mp := m.serve(lake.Namespace, lake.Name, ln, target())
	mp.upstream, mp.engineToken = upstream, engineToken

	url := m.publishURL(ln.Addr())
	m.log.Debug("lake proxy ensured", "lake", key, "url", url, "upstream", upstream)
	return url, nil
}

// Listen binds the lake's listener on port, its recorded status.proxyPort, unless one is bound in this daemon run
// (ADR-0162). When port is 0 or that bind fails it binds a new port; moved reports a failed non-zero bind. A new
// listener answers 503 until Ensure targets the engine; a bound one stops being released. A failed last bind is
// fault.Unavailable.
func (m *Manager) Listen(ns v1.NamespaceName, name v1.ObjectName, port int) (bound int, moved bool, err error) {
	const op = "lake.gateway.Manager.Listen"
	key := managerKey(ns, name)

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.servers[key]; ok {
		existing.released = false
		return listenerPort(existing.listener.Addr()), false, nil
	}
	var ln net.Listener
	if port > 0 {
		if ln, err = net.Listen("tcp", net.JoinHostPort(m.host, strconv.Itoa(port))); err != nil {
			// An address not on this host yet (the bridge before its first container, ADR-0238) keeps the recorded port.
			if errors.Is(err, syscall.EADDRNOTAVAIL) {
				return 0, false, fault.Wrapf(err, fault.Unavailable, op, "bind node-private lake proxy listener for %s", key)
			}
			moved = true
		}
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", net.JoinHostPort(m.host, "0")); err != nil {
			return 0, false, fault.Wrapf(err, fault.Unavailable, op, "bind node-private lake proxy listener for %s", key)
		}
	}
	m.serve(ns, name, ln, http.HandlerFunc(lakeNotReady))
	m.log.Debug("lake proxy listening", "lake", key, "url", m.publishURL(ln.Addr()))
	return listenerPort(ln.Addr()), moved, nil
}

// serve starts a proxy server on ln behind a retargetable slot holding h and records it. The caller must hold m.mu.
func (m *Manager) serve(ns v1.NamespaceName, name v1.ObjectName, ln net.Listener, h http.Handler) *managedProxy {
	key := managerKey(ns, name)
	handler := &retargetable{}
	handler.set(h)
	srv := newProxyServer(handler, m.proxyLog)
	mp := &managedProxy{ns: ns, name: name, listener: ln, server: srv, handler: handler}
	m.servers[key] = mp
	go func() {
		if serr := srv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
			m.log.Error("lake proxy serve stopped", "lake", key, "err", serr)
		}
	}()
	return mp
}

// Release suspends a deleted lake's listener as Suspend does and marks it released: its URL stays bound for the
// Functions that bind the lake until Remove closes it (ADR-0162). Releasing an unknown lake is a no-op.
func (m *Manager) Release(ns v1.NamespaceName, name v1.ObjectName) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mp, ok := m.servers[managerKey(ns, name)]; ok {
		mp.handler.set(http.HandlerFunc(lakeNotReady))
		mp.upstream, mp.engineToken = "", ""
		mp.released = true
	}
}

// Released returns the lakes of ns whose listener is released, sorted.
func (m *Manager) Released(ns v1.NamespaceName) []v1.ObjectName {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []v1.ObjectName
	for _, mp := range m.servers {
		if mp.released && mp.ns == ns {
			out = append(out, mp.name)
		}
	}
	slices.Sort(out)
	return out
}

// ProxyURL returns the bare "<host>:<port>" of the lake's listener when one is bound in this daemon run,
// and false when none is: a restarted daemon binds none until the lake's first pass, so a URL stored before the
// restart may be dead (#662, ADR-0162).
func (m *Manager) ProxyURL(ns v1.NamespaceName, name v1.ObjectName) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mp, ok := m.servers[managerKey(ns, name)]
	if !ok {
		return "", false
	}
	return m.publishURL(mp.listener.Addr()), true
}

// Remove stops and forgets the proxy for a lake (ADR-0137 teardown path). Idempotent: removing an
// unknown lake is a no-op.
func (m *Manager) Remove(ns v1.NamespaceName, name v1.ObjectName) {
	key := managerKey(ns, name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if mp, ok := m.servers[key]; ok {
		m.closeProxy(key, mp)
	}
}

// Suspend keeps a lake's listener, so the URL consumers were injected with stays valid (#59), but answers every
// request 503 and drops the engine target and token until Ensure retargets it: a not-Ready lake serves no caller
// (#372). Suspending an unknown lake is a no-op.
func (m *Manager) Suspend(ns v1.NamespaceName, name v1.ObjectName) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mp, ok := m.servers[managerKey(ns, name)]; ok {
		mp.handler.set(http.HandlerFunc(lakeNotReady))
		mp.upstream, mp.engineToken = "", ""
	}
}

func lakeNotReady(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "lake not ready", http.StatusServiceUnavailable)
}

// Shutdown stops every running proxy (daemon shutdown). Thread-safe and idempotent.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, mp := range m.servers {
		m.closeProxy(key, mp)
	}
	m.engines.CloseIdleConnections()
}

// closeProxy stops one proxy and deletes it from the map. The caller must hold m.mu. The listener is closed here too:
// http.Server.Close closes only the listeners its Serve goroutine already tracks, so a port could otherwise stay bound
// after Remove returns. The errors are best-effort (a shutting-down proxy).
func (m *Manager) closeProxy(key string, mp *managedProxy) {
	_ = mp.server.Close()
	_ = mp.listener.Close()
	delete(m.servers, key)
	m.log.Debug("lake proxy removed", "lake", key)
}

// newProxyServer builds a proxy's http.Server. The read timeouts cut off a peer that stalls before the PEP has
// read its token, as on the data plane (issue #312); net/http clears the read deadline once the body is read,
// so a long query is not cut. IdleTimeout outlasts the 90 s client keep-alive of the data plane's edge reverse
// proxy (ADR-0138), so that client closes an idle connection first. ErrorLog sends net/http's own errors
// through log, not the stdlib log package (#454).
func newProxyServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
}

// listenerPort is the TCP port a listener bound.
func listenerPort(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// publishURL renders the BARE "<host>:<port>" host:port a function is injected with: the ephemeral PORT the
// listener bound, joined to the Manager's host. Bare because the handler prepends
// quack:// (a scheme here would produce quack://http://… → Invalid Port). On a malformed addr it falls back
// to the raw addr string.
func (m *Manager) publishURL(addr net.Addr) string {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return net.JoinHostPort(m.host, port)
}
