// Package egress is the transparent egress PEP (ADR-0117, FEAT-0007/F81): the enforcement point F80's
// nftables substrate REDIRECTs remaining external worker TCP into. Per redirected connection the Linux
// driver recovers the pre-DNAT destination (SO_ORIGINAL_DST), authenticates the caller by source IP via
// the WorkerIndex, builds the FORWARDER-ATTESTED destination (the B1 invariant — client SNI/Host is never
// the authorization basis), asks the auth.Authorizer PDP whether that worker's principal (its Function or
// Lake) may egress::connect to that NetDestination, and splices on ALLOW / refuses on DENY —
// auditing every connection. It is default-deny,
// fail-closed, opt-in (server.network.egress), and Linux/containerd only. Disabled or on a non-Linux host
// New returns a no-op Gateway and worker egress stays open exactly as before. The SNI/Host parsers, the
// forwarder-attested destination build, and the decision flow are pure + unit-tested cross-platform; the
// SO_ORIGINAL_DST recovery, conn peek/replay, and splice are the Linux driver (Lima e2e verifies behavior).
package egress

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
)

// Gateway is the transparent egress PEP: it accepts F80-redirected worker connections on GatewayPort,
// recovers the original destination, identifies the caller, asks the PDP, and splices or refuses —
// auditing every connection. Serve blocks until ctx is cancelled or Close is called.
type Gateway interface {
	Serve(ctx context.Context) error
	Close() error
}

// Deps wires the gateway. On a non-Linux host or when disabled, New returns a no-op Gateway.
type Deps struct {
	GatewayPort uint16          // F80's REDIRECT target (server.network.egressGatewayPort)
	Workers     WorkerIndex     // src-IP → caller Ref (containerd runtime populates it, §5)
	DNS         DomainResolver  // forwarder-attested (worker, dst-IP) → domains (the trust anchor)
	Authz       auth.Authorizer // the PDP (egress::connect over a NetDestination)
	Audit       AuditSink       // every connection (allowed + blocked) → the funclog channel
	Logger      *slog.Logger    // nil ⇒ slog.Default()
	// NodeAddrs reads the addresses on the node's interfaces, which a domain rule never reaches (ADR-0315); nil reads them
	// from the host.
	NodeAddrs func() ([]netip.Addr, error)
	// Engines holds the addresses of the add-on engines, which no rule reaches: only funcd's own components connect to
	// them (ADR-0332 Decision 3). nil holds none.
	Engines WorkerIndex
	// Edge holds each address and port the data plane listens on, which no rule reaches either, a cidrs rule included: a
	// workload reaches the edge only on the bridge address, which pins it to its namespace (ADR-0353 Decision 2). An
	// unspecified address stands for every node address (NodeAddrs).
	Edge []netip.AddrPort
}

// WorkerIndex resolves a worker's funcd0 source IP to the principal Ref of its owner kind (a Function or a
// Lake).
type WorkerIndex interface {
	Lookup(ip netip.Addr) (ref auth.EntityRef, ok bool)
}

// DomainResolver returns the domains THIS worker resolved to dst via the funcd forwarder — the
// authoritative domain→IP binding (never client SNI/Host). It includes any namespace wildcard pattern
// token an attested FQDN matched (so wildcard rules stay exact set-membership). Empty ⇒ only a CIDR rule
// can permit dst.
type DomainResolver interface {
	DomainsFor(src netip.Addr, dst netip.Addr) []string
}

// AuditSink records one egress decision to the funclog audit channel.
type AuditSink interface {
	Egress(ctx context.Context, rec AuditRecord)
}

// AuditRecord is one egress decision (allowed or blocked).
type AuditRecord struct {
	Namespace, Function, Domain string
	IP                          netip.Addr
	Port                        uint16
	Allowed                     bool
	Reason                      string
}

// New returns the Linux driver when enabled on Linux, else a no-op (build-tagged newGateway() dispatch,
// mirroring internal/network's nftables_linux.go / _other.go split).
func New(enabled bool, d Deps) Gateway {
	if !enabled {
		return noopGateway{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return newGateway(d)
}

// noopGateway is the disabled / non-Linux Gateway: it serves nothing and never errors.
type noopGateway struct{}

func (noopGateway) Serve(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (noopGateway) Close() error                    { return nil }

// buildDestination applies the B1 invariant: the authorized domain set is the FORWARDER-ATTESTED set
// (attested = DomainResolver.DomainsFor(src, dst)); the asserted SNI/Host is honored only as a
// confirmation hint and only if it is ITSELF attested (else dropped — a spoofed SNI to an unattested IP
// contributes nothing). It returns the NetDestination to authorize plus the confirmed domain for the
// audit record ("" if the asserted name was not attested / none asserted).
func buildDestination(dst netip.AddrPort, attested []string, asserted string) (auth.NetDestination, string) {
	nd := auth.NetDestination{
		IP:      dst.Addr(),
		Port:    dst.Port(),
		Domains: append([]string(nil), attested...),
	}
	confirmed := ""
	if asserted != "" && contains(attested, asserted) {
		confirmed = asserted
	}
	return nd, confirmed
}

// notGlobal is what a domain rule does not reach (ADR-0317 Decision 1), read 2026-11-05 from IANA's IPv4 and IPv6
// special-purpose registries: what they mark not globally reachable, each entry whole; then their rows that are deprecated
// or carry an IPv4 address, multicast, deprecated site-local and Azure's virtual public address. Immutable.
var notGlobal = func() []netip.Prefix { //nolint:gochecknoglobals // an immutable lookup table
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
		"192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64", "100:0:0:1::/64", "2001::/23", "2001:db8::/32",
		"3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10",
		"192.88.99.0/24", "2002::/16", "64:ff9b::/96",
		"224.0.0.0/4", "ff00::/8", "fec0::/10", "168.63.129.16/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// restricted reports whether a domain rule may not reach ip (ADR-0317 Decision 1): an invalid address, one in notGlobal, or
// one on the node's interfaces, read with any IPv4 mapping and zone removed. Only a CIDR rule permits those.
func (d Deps) restricted(ip netip.Addr) (bool, error) {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || slices.ContainsFunc(notGlobal, func(p netip.Prefix) bool { return p.Contains(ip) }) {
		return true, nil
	}
	node, err := d.nodeAddrs()
	if err != nil {
		return true, err
	}
	return slices.Contains(node, ip), nil
}

// nodeAddrs reads the addresses on the node's interfaces.
func (d Deps) nodeAddrs() ([]netip.Addr, error) {
	if d.NodeAddrs == nil {
		return interfaceAddrs()
	}
	return d.NodeAddrs()
}

// onEdge reports whether dst is an address and port the data plane listens on (ADR-0353 Decision 2).
func (d Deps) onEdge(dst netip.AddrPort) (bool, error) {
	ip := dst.Addr().Unmap().WithZone("")
	for _, e := range d.Edge {
		switch {
		case e.Port() != dst.Port():
		case e.Addr().Unmap() == ip:
			return true, nil
		case e.Addr().IsUnspecified():
			node, err := d.nodeAddrs()
			if err != nil || slices.Contains(node, ip) {
				return true, err
			}
		}
	}
	return false, nil
}

// GloballyReachable reports whether ip lies outside ADR-0317's table and off the node's interfaces: what a domain rule
// may reach, and all the edge's sign-in back channel reaches (ADR-0335 Decision 7). A failed read of the node's
// addresses is false with the error.
func GloballyReachable(ip netip.Addr) (bool, error) {
	restricted, err := Deps{}.restricted(ip)
	return !restricted, err
}

// interfaceAddrs reads the addresses on the host's interfaces, each with any IPv4 mapping removed.
func interfaceAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "egress.interfaceAddrs", "read the node's interface addresses")
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out, nil
}

// decideConnect is the pure decision flow: authenticate the source IP → refuse an add-on engine's address and the
// edge's → build the forwarder-attested destination, which carries no domains at a restricted address → ask the PDP.
// It returns the decision and the
// audit record; the driver handles I/O (splice/refuse) and emits the record. An unknown source IP is denied+audited
// (fail-closed); a failed read of the node's addresses is an error, which permits nothing.
func decideConnect(ctx context.Context, d Deps, src netip.Addr, dst netip.AddrPort, asserted string) (auth.Decision, AuditRecord, error) {
	ref, ok := d.Workers.Lookup(src)
	if !ok {
		rec := AuditRecord{IP: dst.Addr(), Port: dst.Port(), Allowed: false, Reason: "unknown source worker " + src.String()}
		return auth.Decision{Allowed: false, Reason: rec.Reason}, rec, nil
	}
	if d.Engines != nil {
		if _, engine := d.Engines.Lookup(dst.Addr().Unmap()); engine {
			rec := AuditRecord{Namespace: string(ref.Namespace), Function: string(ref.Name), IP: dst.Addr(), Port: dst.Port(),
				Allowed: false, Reason: "an add-on engine's address, which only funcd reaches"}
			return auth.Decision{Allowed: false, Reason: rec.Reason}, rec, nil
		}
	}
	edge, err := d.onEdge(dst)
	if err != nil {
		return auth.Decision{}, AuditRecord{}, err
	}
	if edge {
		rec := AuditRecord{Namespace: string(ref.Namespace), Function: string(ref.Name), IP: dst.Addr(), Port: dst.Port(),
			Allowed: false, Reason: "an address funcd's edge listens on, which a workload reaches on the bridge address only"}
		return auth.Decision{Allowed: false, Reason: rec.Reason}, rec, nil
	}
	attested := d.DNS.DomainsFor(src, dst.Addr())
	if len(attested) > 0 {
		restricted, err := d.restricted(dst.Addr())
		if err != nil {
			return auth.Decision{}, AuditRecord{}, err
		}
		if restricted {
			attested = nil
		}
	}
	nd, confirmed := buildDestination(dst, attested, asserted)
	resource := nd.Ref(ref.Namespace)
	dec, err := d.Authz.Authorize(ctx, auth.Request{
		Identity: auth.Identity{Principal: &ref},
		Action:   auth.ActionEgressConnect,
		Resource: &resource,
	})
	if err != nil {
		return auth.Decision{}, AuditRecord{}, err
	}
	rec := AuditRecord{
		Namespace: string(ref.Namespace),
		Function:  string(ref.Name),
		Domain:    confirmed,
		IP:        dst.Addr(),
		Port:      dst.Port(),
		Allowed:   dec.Allowed,
		Reason:    dec.Reason,
	}
	return dec, rec, nil
}

// Decide is the gateway's decision on one connection from src to dst, as Serve makes it for a redirected one; asserted is
// the name the client's SNI or Host gave, "" for none.
func (d Deps) Decide(ctx context.Context, src netip.Addr, dst netip.AddrPort, asserted string) (auth.Decision, AuditRecord, error) {
	return decideConnect(ctx, d, src, dst, asserted)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
