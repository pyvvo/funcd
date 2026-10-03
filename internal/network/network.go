// Package network is the worker network-isolation substrate (ADR-0115, FEAT-0007/F80): the L3/L4 egress
// policy layer. When enabled on Linux it programs — over the existing funcd0 CNI bridge, never replacing
// it — a bridge-family lateral-deny plus an inet table that redirects remaining external worker TCP into
// the egress gateway (F81) and default-denies the rest. Disabled, or on a non-Linux host, every method is
// a no-op and worker egress stays open exactly as before. Behavioral egress *decisions* (allow/deny a
// destination) are F81's; this package only frames the isolation.
package network

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/pyvvo/funcd/api/fault"
)

// Manager programs the host-level worker-egress isolation substrate (F80). It is enabled opt-in; disabled
// or on a non-Linux host every method is a no-op. Apply is idempotent — it is called once at daemon start
// (before any worker serves) and reconciles the funcd tables to Policy; Remove tears them down at shutdown
// (safe if never applied).
type Manager interface {
	Apply(ctx context.Context, p Policy) error
	Remove(ctx context.Context) error
}

// Policy is the static isolation frame F80 programs. Behavioral egress decisions are F81's — this only
// frames default-deny + the internal allowlist + the redirect hook. See ADR-0115 Contracts.
type Policy struct {
	// WorkerSubnet is the funcd0 host-local IPAM subnet (from containerd.Config.SubnetCIDR); rules match
	// it as the source.
	WorkerSubnet netip.Prefix
	// GatewayPort is where remaining external TCP is REDIRECTed (F81's egress gateway). Zero is rejected
	// when enabled — the redirect must have a target.
	GatewayPort uint16
	// InternalAllow are funcd node-private services a worker may reach directly (S3 frontend,
	// ingress/catalog) — passed, never redirected. These are the WORKER-REACHABLE endpoint addresses
	// (the funcd0-gateway-IP:port a service's injected Endpoint resolves to), NOT the service's 127.0.0.1
	// listen address (which is the worker's own netns loopback and unreachable).
	InternalAllow []netip.AddrPort
	// DNSResolver is the single node resolver a worker may reach on :53 — the accept is scoped to this
	// address (not any :53), so it is not an open exfil channel.
	DNSResolver netip.AddrPort
	// DNSForwarderPort, when non-zero, is the funcd DNS forwarder's host port (ADR-0117, F81): worker
	// :53 (UDP+TCP) is REDIRECTed into it so the forwarder is the ONLY reachable resolver (the F80
	// DNS-workaround exit — a worker cannot resolve out-of-band). Zero ⇒ legacy F80 behavior (worker DNS
	// goes straight to DNSResolver, un-correlated).
	DNSForwarderPort uint16
}

// Validate reports whether the Policy is programmable: a valid non-zero worker subnet, a non-zero gateway
// port (the redirect must have a target), and a valid resolver. Called by the enabled driver before it
// programs anything.
func (p Policy) Validate() error {
	const op = "network.Policy.Validate"
	if !p.WorkerSubnet.IsValid() || p.WorkerSubnet.Bits() == 0 {
		return fault.Invalidf(op, "worker subnet is required")
	}
	if p.GatewayPort == 0 {
		return fault.Invalidf(op, "gateway port is required (the redirect must have a target)")
	}
	if !p.DNSResolver.IsValid() {
		return fault.Invalidf(op, "DNS resolver address is required")
	}
	return nil
}

// New returns a Manager: the google/nftables driver when enabled on Linux, else a no-op. This file is
// un-tagged and delegates to the build-tagged newDriver (nftables_linux.go vs nftables_other.go),
// so New compiles on every GOOS. A nil logger means slog.Default().
func New(enabled bool, logger *slog.Logger) Manager {
	if !enabled {
		return noop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return newDriver(logger)
}

// noop is the disabled / non-Linux Manager: it programs nothing and never errors.
type noop struct{}

func (noop) Apply(context.Context, Policy) error { return nil }
func (noop) Remove(context.Context) error        { return nil }
