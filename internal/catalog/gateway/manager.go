package gateway

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// Manager owns the per-CatalogService catalog PEP proxies (ADR-0137): it runs ONE node-private
// http.Server per catalog, each fronting that catalog's engine. The CatalogService reconciler calls
// Ensure on its Ready branch to (re)bind a node-private listener for the catalog and learn the proxy
// URL that internal functions are injected with (FUNCD_CATALOG_<ALIAS>_URL), and Remove on teardown.
// One listener endpoint per catalog fixes the PEP's target catalog by the endpoint (the proxy takes
// the namespace from the resolved principal — see NewCatalogProxy).
type Manager struct {
	keys CatalogKeys
	pdp  auth.Authorizer
	log  *slog.Logger

	// bindHost is the interface each proxy listens on; publishHost is the host injected into functions as
	// FUNCD_CATALOG_<ALIAS>_URL. They differ under containerd (ADR-0137, mirroring the s3gateway
	// ListenAddr/Endpoint split): a worker runs in its OWN netns, so it CANNOT reach the daemon's
	// 127.0.0.1 — the proxy must bind a netns-reachable interface (0.0.0.0) and publish the CNI bridge
	// gateway IP (e.g. 10.63.0.1). In the process runtime (funcdctl dev) both are 127.0.0.1 (shared
	// loopback). Empty ⇒ 127.0.0.1 (the dev/loopback default).
	bindHost    string
	publishHost string

	mu      sync.Mutex
	servers map[string]*managedProxy // key = "ns/name"
}

// managedProxy is one running node-private proxy: its listener, its serving http.Server, its handler
// slot, and the (upstream, engineToken) it targets — so Ensure can detect a change and retarget it.
type managedProxy struct {
	listener    net.Listener
	server      *http.Server
	handler     *retargetable
	upstream    string
	engineToken string
}

// retargetable is a proxy's handler slot. Ensure swaps in a proxy for the new engine target when the
// engine moves or its shared token rotates, and keeps the listener, so the URL already injected into
// consumers stays valid: nothing re-provisions a consumer when a catalog's endpoint changes.
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

// NewManager builds the catalog proxy Manager over the shared token resolver + PDP (ADR-0137). The
// same CatalogKeys/Authorizer back every per-catalog proxy; each proxy differs only in its
// EngineTarget (the catalog ref + its netns engine URL + shared engine token). bindHost is the interface
// each proxy binds; publishHost is the host injected into functions (see Manager). Both empty ⇒
// 127.0.0.1 (the process-runtime/dev default, shared loopback); under containerd pass bindHost "0.0.0.0"
// and publishHost the CNI bridge gateway IP so a worker in its own netns can reach the proxy.
func NewManager(bindHost, publishHost string, keys CatalogKeys, pdp auth.Authorizer, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	if publishHost == "" {
		publishHost = "127.0.0.1"
	}
	return &Manager{
		keys:        keys,
		pdp:         pdp,
		log:         log.With("component", "catalog.gateway.manager"),
		bindHost:    bindHost,
		publishHost: publishHost,
		servers:     make(map[string]*managedProxy),
	}
}

// managerKey is the servers-map key for a catalog ref: "ns/name".
func managerKey(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name)
}

// Ensure guarantees a node-private proxy is running for catalog, fronting upstream with engineToken,
// and returns the bare "<publishHost>:<port>" host:port the function injects as FUNCD_CATALOG_<ALIAS>_URL
// (its handler wraps it in quack://; the engine endpoint was bare too). Under containerd publishHost is
// the CNI bridge gateway IP (netns-reachable), NOT the bind interface; in dev both are 127.0.0.1. If a
// proxy already runs for the catalog it is reused (idempotent — a re-reconcile does not rebind); if the
// upstream or engineToken differs, the running proxy is retargeted in place and its URL stays the same.
// Thread-safe.
func (m *Manager) Ensure(catalog auth.EntityRef, upstream, engineToken string) (string, error) {
	const op = "catalog.gateway.Manager.Ensure"
	key := managerKey(catalog.Namespace, catalog.Name)

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.servers[key]; ok {
		if existing.upstream != upstream || existing.engineToken != engineToken {
			// The engine moved or its shared token rotated: retarget the SAME listener, so the URL
			// consumers already hold keeps working.
			existing.handler.set(NewCatalogProxy(m.keys, m.pdp, EngineTarget{Catalog: catalog, Upstream: upstream, EngineToken: engineToken}))
			existing.upstream, existing.engineToken = upstream, engineToken
			m.log.Debug("catalog proxy retargeted", "catalog", key, "upstream", upstream)
		}
		return m.publishURL(existing.listener.Addr()), nil
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(m.bindHost, "0"))
	if err != nil {
		return "", fault.Unavailablef(op, "bind node-private catalog proxy listener for %s: %v", key, err)
	}
	handler := &retargetable{}
	handler.set(NewCatalogProxy(m.keys, m.pdp, EngineTarget{Catalog: catalog, Upstream: upstream, EngineToken: engineToken}))
	srv := newProxyServer(handler)
	mp := &managedProxy{listener: ln, server: srv, handler: handler, upstream: upstream, engineToken: engineToken}
	m.servers[key] = mp

	go func() {
		if serr := srv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
			m.log.Error("catalog proxy serve stopped", "catalog", key, "err", serr)
		}
	}()

	url := m.publishURL(ln.Addr())
	m.log.Debug("catalog proxy ensured", "catalog", key, "url", url, "upstream", upstream)
	return url, nil
}

// Remove stops and forgets the proxy for a catalog (ADR-0137 teardown path). Idempotent: removing an
// unknown catalog is a no-op.
func (m *Manager) Remove(ns v1.NamespaceName, name v1.ObjectName) {
	key := managerKey(ns, name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if mp, ok := m.servers[key]; ok {
		m.closeProxy(key, mp)
	}
}

// Shutdown stops every running proxy (daemon shutdown). Thread-safe and idempotent.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, mp := range m.servers {
		m.closeProxy(key, mp)
	}
}

// closeProxy stops one proxy and deletes it from the map. The caller must hold m.mu. Closing the
// http.Server also closes its listener; the errors are best-effort (a shutting-down proxy).
func (m *Manager) closeProxy(key string, mp *managedProxy) {
	_ = mp.server.Close()
	delete(m.servers, key)
	m.log.Debug("catalog proxy removed", "catalog", key)
}

// newProxyServer builds a proxy's http.Server. The read timeouts cut off a peer that stalls before the PEP has
// read its token, as on the data plane (issue #312); net/http clears the read deadline once the body is read,
// so a long query is not cut. IdleTimeout outlasts the 90 s client keep-alive of the data plane's edge reverse
// proxy (ADR-0138), so that client closes an idle connection first.
func newProxyServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

// publishURL renders the BARE "<publishHost>:<port>" host:port a function is injected with: the ephemeral
// PORT the listener bound, joined to the Manager's publishHost (the netns-reachable host — the CNI bridge
// gateway IP under containerd, 127.0.0.1 in dev), NOT the bind interface. Bare because the handler prepends
// quack:// (a scheme here would produce quack://http://… → Invalid Port). On a malformed addr it falls back
// to the raw addr string.
func (m *Manager) publishURL(addr net.Addr) string {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return net.JoinHostPort(m.publishHost, port)
}
