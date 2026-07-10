// Package egress is the transparent egress PEP (ADR-0117, FEAT-0007/F81): the enforcement point F80's
// nftables substrate REDIRECTs remaining external worker TCP into. Per redirected connection the Linux
// driver recovers the pre-DNAT destination (SO_ORIGINAL_DST), authenticates the caller by source IP via
// the WorkerIndex, builds the FORWARDER-ATTESTED destination (the B1 invariant — client SNI/Host is never
// the authorization basis), asks the auth.Authorizer PDP whether that Function may egress::connect to that
// NetDestination, and splices on ALLOW / refuses on DENY — auditing every connection. It is default-deny,
// fail-closed, opt-in (server.network.egress), and Linux/containerd only. Disabled or on a non-Linux host
// New returns a no-op Gateway and worker egress stays open exactly as before. The SNI/Host parsers, the
// forwarder-attested destination build, and the decision flow are pure + unit-tested cross-platform; the
// SO_ORIGINAL_DST recovery, conn peek/replay, and splice are the Linux driver (Lima e2e verifies behavior).
package egress

import (
	"context"
	"net/netip"

	"github.com/green-0-rabbit/funcd/internal/auth"
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
}

// WorkerIndex resolves a worker's funcd0 source IP to its (namespace, function) principal Ref.
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

// decideConnect is the pure decision flow: authenticate the source IP → build the forwarder-attested
// destination → ask the PDP. It returns the decision and the audit record; the driver handles I/O
// (splice/refuse) and emits the record. An unknown source IP is denied+audited (fail-closed).
func decideConnect(ctx context.Context, d Deps, src netip.Addr, dst netip.AddrPort, asserted string) (auth.Decision, AuditRecord, error) {
	ref, ok := d.Workers.Lookup(src)
	if !ok {
		rec := AuditRecord{IP: dst.Addr(), Port: dst.Port(), Allowed: false, Reason: "unknown source worker " + src.String()}
		return auth.Decision{Allowed: false, Reason: rec.Reason}, rec, nil
	}
	attested := d.DNS.DomainsFor(src, dst.Addr())
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

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
