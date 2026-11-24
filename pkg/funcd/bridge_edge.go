package funcd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/edge"
	"github.com/pyvvo/funcd/internal/network/egress"
)

// bridgeBindRetry paces the bind of the edge's listener on the bridge address while that address does not exist yet: the
// bridge appears with the first worker, as for the DNS forwarder (ADR-0315).
const bridgeBindRetry = time.Second

// bridgeEdge is funcd's address on the container bridge, where the edge pins each caller to its namespace (ADR-0353
// Decision 1): only while the egress gateway runs, whose indexes name the callers there. Without a bridge address, as
// on the process runtime, there is no bridge and no pin.
func (p *Platform) bridgeEdge() (netip.Addr, bool) {
	if p.exec.egressSources == nil || p.cfg.bridgeAddress == "" {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(p.cfg.bridgeAddress)
	return a.Unmap(), err == nil
}

// edgeOnTheBridge follows the data plane's bind at bound (ADR-0353 Decisions 1 and 2): the workers' allow list gains the
// bridge address at the bound port, the gateway never dials an address and port the data plane listens on, and the
// bridge address gets a listener of its own unless the bound one accepts there already.
func (p *Platform) edgeOnTheBridge(bound netip.AddrPort) {
	bridge, ok := p.bridgeEdge()
	if !ok {
		return
	}
	edge := netip.AddrPortFrom(bridge, bound.Port())
	p.cfg.netPolicy.InternalAllow = append(p.cfg.netPolicy.InternalAllow, edge)
	p.exec.edgeBound(edge, bound)
	if addr := bound.Addr().Unmap(); !addr.IsUnspecified() && addr != bridge {
		p.bridgeListen = edge
	}
}

// serveBridge serves the data plane on the bridge address with serve, as the bound listener is served, TLS included,
// retrying each bridgeBindRetry while that address does not exist yet; any other bind error ends it.
func (p *Platform) serveBridge(ctx context.Context, serve func(*http.Server, net.Listener) error) {
	addr := p.bridgeListen.String()
	for warned := false; ; {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err == nil {
			if err := serve(p.dataPlaneServer, ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				p.logger.ErrorContext(ctx, "the edge on the bridge address stopped", "error", err)
			}
			return
		}
		if !errors.Is(err, syscall.EADDRNOTAVAIL) {
			p.logger.ErrorContext(ctx, "the edge cannot listen on the bridge address", "addr", addr, "error", err)
			return
		}
		if !warned {
			p.logger.WarnContext(ctx, "the edge cannot listen on the bridge address yet; retrying each second", "addr", addr)
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(bridgeBindRetry):
		}
	}
}

// clientAddress names each listener request's client address (ADR-0354 Decision 2), which the rate key, the access log and
// the backends' X-Forwarded-For read: the peer, unless the first trusted proxy entry that matches it names another. The
// kind of an add-on object's engine is read only for an addonKinds entry, through the egress indexes and the store.
func (p *Platform) clientAddress(next http.Handler) http.Handler {
	proxies := p.cfg.proxies
	if len(proxies.Entries) == 0 {
		return next
	}
	sources, kinds := p.exec.egressSources, proxies.HasAddonKinds()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := edge.Peer(r)
		kind := ""
		if ref, ok := lookupSource(sources, peer); kinds && ok && ref.Type == v1.KindAddonObject {
			var err error
			if kind, err = addonKindOf(context.WithoutCancel(r.Context()), p.cfg.store, ref); err != nil {
				p.logger.WarnContext(r.Context(), "the edge cannot read the kind of the engine that called it", "error", err)
			}
		}
		next.ServeHTTP(w, r.WithContext(edge.WithClient(r.Context(), proxies.Client(r, peer, kind))))
	})
}

// lookupSource is the principal sources names for addr; false with no egress gateway, which keeps no index.
func lookupSource(sources egress.WorkerIndex, addr netip.Addr) (auth.EntityRef, bool) {
	if sources == nil || !addr.IsValid() {
		return auth.EntityRef{}, false
	}
	return sources.Lookup(addr)
}

// warnUnmatchedKinds says at start that an addonKinds entry matches nothing without the egress gateway, which names the
// engines that reach the edge (ADR-0354 Decision 3).
func (p *Platform) warnUnmatchedKinds() {
	if p.cfg.proxies.HasAddonKinds() && p.exec.egressSources == nil {
		p.logger.Warn("server.trustedProxies: an addonKinds entry matches nothing here, since only the egress gateway's " +
			"indexes name the engines that reach the edge (containerd with server.network.egress and a dnsForwarderPort)")
	}
}
