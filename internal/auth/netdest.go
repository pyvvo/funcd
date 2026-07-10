package auth

import (
	"net/netip"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// NetDestination is the parsed egress destination (ADR-0117, F81): the resolved dst IP:port plus the
// forwarder-attested domain set. It rides the EntityRef.Path (the KindNetDestination reinterpretation);
// the codec here is the SINGLE source of truth the egress gateway (producer) and the cedar capability
// (consumer) share, so the encoded Path and the decoded UID agree byte-for-byte.
type NetDestination struct {
	IP      netip.Addr
	Port    uint16
	Domains []string // forwarder-attested domains (incl. any matched wildcard token); empty for pure-IP/CIDR
}

// EncodeNetDestPath encodes a NetDestination into the PINNED EntityRef.Path form
// "<ip>:<port>#<domain1>,<domain2>,…" (the domain segment is empty for a pure-IP/CIDR target).
func EncodeNetDestPath(ip netip.Addr, port uint16, domains []string) string {
	return netip.AddrPortFrom(ip, port).String() + "#" + strings.Join(domains, ",")
}

// ParseNetDestPath parses the pinned Path form back into a NetDestination. A malformed prefix is
// fault.Invalid.
func ParseNetDestPath(path string) (NetDestination, error) {
	const op = "auth.ParseNetDestPath"
	ipport, domainSeg, _ := strings.Cut(path, "#")
	ap, err := netip.ParseAddrPort(ipport)
	if err != nil {
		return NetDestination{}, fault.Invalidf(op, "invalid <ip>:<port> prefix %q: %v", ipport, err)
	}
	var domains []string
	if domainSeg != "" {
		domains = strings.Split(domainSeg, ",")
	}
	return NetDestination{IP: ap.Addr(), Port: ap.Port(), Domains: domains}, nil
}

// UIDString returns the stable "<ip>:<port>" prefix (the part before '#') used verbatim as the
// NetDestination Cedar entity id — so the resource UID is independent of the (mutable) domain set.
func (d NetDestination) UIDString() string {
	return netip.AddrPortFrom(d.IP, d.Port).String()
}

// Ref builds the resource EntityRef for a NetDestination in the caller's namespace (Name unused —
// the destination rides the Path).
func (d NetDestination) Ref(ns v1.NamespaceName) EntityRef {
	return EntityRef{
		Type:      v1.KindNetDestination,
		Namespace: ns,
		Path:      EncodeNetDestPath(d.IP, d.Port, d.Domains),
	}
}
