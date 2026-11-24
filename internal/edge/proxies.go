// Package edge holds what the edge's parts share about a request: its client address behind the proxies the
// server trusts (ADR-0354), which the rate limit, the access log and the data plane read.
package edge

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// TrustedProxy is one server.trustedProxies entry (ADR-0354 Decision 1): the hops it trusts, by CIDR range or by the
// add-on kinds of the engines that reach the edge on the bridge address, and the header such a hop sets to the client;
// "" walks X-Forwarded-For.
type TrustedProxy struct {
	CIDRs      []netip.Prefix
	AddonKinds []string
	Header     string
}

// Proxies decides a request's client address (Decisions 2 and 3); Workers is runtime.containerd.subnetCIDR, the zero
// Prefix on the process runtime.
type Proxies struct {
	Entries []TrustedProxy
	Workers netip.Prefix
}

// Client is r's client address; peerKind is the kind of the stored add-on object that egress.Sources names for peer, ""
// when it names none. The first entry, in list order, that matches the peer decides; a peer that matches none is the
// client.
func (p Proxies) Client(r *http.Request, peer netip.Addr, peerKind string) netip.Addr {
	peer = peer.Unmap()
	for _, e := range p.Entries {
		if !p.matches(e, peer, peerKind) {
			continue
		}
		if e.Header != "" {
			if v := r.Header.Values(e.Header); len(v) == 1 {
				if a, err := netip.ParseAddr(strings.TrimSpace(v[0])); err == nil {
					return a.Unmap()
				}
			}
			return peer
		}
		return p.walk(r, peer)
	}
	return peer
}

// HasAddonKinds reports whether an entry trusts add-on kinds, which a peer's kind is read for.
func (p Proxies) HasAddonKinds() bool {
	return slices.ContainsFunc(p.Entries, func(e TrustedProxy) bool { return len(e.AddonKinds) > 0 })
}

// matches reports whether e trusts peer: a cidrs entry holds it, outside the worker subnet, where only an addonKinds
// entry can trust a workload; an addonKinds entry names the kind of the add-on object peer is, spelled the same.
func (p Proxies) matches(e TrustedProxy, peer netip.Addr, peerKind string) bool {
	if len(e.AddonKinds) > 0 {
		return peerKind != "" && slices.Contains(e.AddonKinds, peerKind)
	}
	return p.trustedRange(peer, e)
}

func (p Proxies) trustedRange(a netip.Addr, e TrustedProxy) bool {
	if p.Workers.IsValid() && p.Workers.Contains(a) {
		return false
	}
	return slices.ContainsFunc(e.CIDRs, func(c netip.Prefix) bool { return c.Contains(a) })
}

// walk reads X-Forwarded-For from the right past each address that a cidrs entry trusts: the first other one is the
// client, the leftmost when every one is trusted, and the peer when an element is not one IP address.
func (p Proxies) walk(r *http.Request, peer netip.Addr) netip.Addr {
	var chain []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		chain = append(chain, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(chain) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return peer
		}
		client = a.Unmap()
		if !slices.ContainsFunc(p.Entries, func(e TrustedProxy) bool { return len(e.AddonKinds) == 0 && p.trustedRange(client, e) }) {
			return client
		}
	}
	return client
}

// clientKey carries a request's client address.
type clientKey struct{}

// WithClient marks ctx with the request's client address.
func WithClient(ctx context.Context, client netip.Addr) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

// ClientOf is r's client address: the one WithClient set, else the host of r.RemoteAddr, the peer; invalid when neither
// is an address.
func ClientOf(r *http.Request) netip.Addr {
	if a, ok := r.Context().Value(clientKey{}).(netip.Addr); ok {
		return a
	}
	return Peer(r)
}

// Peer is the address of r's connection, from r.RemoteAddr.
func Peer(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
