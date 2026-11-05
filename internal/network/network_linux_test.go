//go:build linux

package network

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// scenario: ruleset-programmed — the structural plan F80 programs: a bridge-family lateral-deny table and
// an inet egress-policy table whose forward-filter chain defaults to drop (so the accepts must precede it)
// and whose prerouting chain is NAT (the redirect site), and whose input chain ends a worker's traffic with a rule, as its
// policy stays accept. This is the correct-by-construction core; the runtime effect (drops/redirects/accepts) is verified
// by the Lima e2e lane (deferred — needs a kernel).
func TestScenarioRulesetProgrammed(t *testing.T) {
	plans := planTables(goodPolicy())

	byName := map[string]tablePlan{}
	for _, tp := range plans {
		byName[tp.name] = tp
	}

	lat, ok := byName[lateralTableName]
	require.True(t, ok, "the bridge lateral-deny table is planned")
	require.Equal(t, nftables.TableFamilyBridge, lat.family, "lateral deny is bridge-family (L2 — same-bridge frames never hit the inet hooks)")

	eg, ok := byName[egressTableName]
	require.True(t, ok, "the inet egress-policy table is planned")
	require.Equal(t, nftables.TableFamilyINet, eg.family)

	chains := map[string]chainPlan{}
	for _, ch := range eg.chains {
		chains[ch.name] = ch
	}
	require.Equal(t, nftables.ChainTypeNAT, chains["prerouting"].typ, "the redirect chain is NAT-type (dstnat)")
	require.True(t, chains["forward"].dropPol, "the inet forward chain defaults to drop — accepts precede the default-deny (fold of judge B2)")
	require.False(t, chains["input"].dropPol, "the input chain's policy leaves the node's other traffic alone")

	var input []ruleKind
	for _, r := range planRules(goodPolicy()) {
		if r.chain == egressIn {
			input = append(input, r.kind)
		}
	}
	require.Equal(t, []ruleKind{kindInputAccept, kindInputEstablished, kindInputInternal, kindInputWorkerDrop}, input,
		"every accept precedes the drop of a worker's traffic")
}

// The prerouting rule plan excludes internal-service destinations from the redirect: every
// redirect-skip (return) rule MUST precede the single redirect, or a NAT redirect (dst rewritten before
// routing) would hijack internal traffic to the gateway. (Review Blocker fix.)
func TestPreroutingExcludesInternalBeforeRedirect(t *testing.T) {
	p := goodPolicy() // one internal endpoint
	rules := planRules(p)

	var pre []rulePlan
	for _, r := range rules {
		if r.chain == egressPre {
			pre = append(pre, r)
		}
	}
	require.NotEmpty(t, pre, "prerouting rules are planned")

	redirectIdx := -1
	for i, r := range pre {
		if r.kind == kindRedirect {
			require.Equal(t, -1, redirectIdx, "exactly one redirect rule")
			redirectIdx = i
		}
	}
	require.NotEqual(t, -1, redirectIdx, "a redirect rule exists")
	require.Equal(t, len(pre)-1, redirectIdx, "the redirect is LAST — every internal skip precedes it")

	// The skips cover the internal endpoint, so it is not redirected.
	skipped := map[netip.Addr]bool{}
	for _, r := range pre[:redirectIdx] {
		require.Equal(t, kindRedirectSkip, r.kind, "every pre-redirect rule is a return/skip")
		skipped[r.dst] = true
	}
	require.True(t, skipped[p.InternalAllow[0].Addr()], "the internal-service endpoint is skipped (not redirected)")

	// forward accepts the same internal destinations (so the returned traffic passes the drop policy).
	fwdAccepts := 0
	for _, r := range rules {
		if r.chain == egressFwd && r.kind == kindForwardAccept {
			fwdAccepts++
		}
	}
	require.Equal(t, len(p.InternalAllow), fwdAccepts, "forward accepts each internal endpoint")
}

// scenario: no-forwarder-nothing-out — with no forwarder a worker's DNS has no rule of its own: UDP :53 falls to the forward
// chain's drop policy and TCP :53 to the redirect, whose gateway is not there. The internal endpoints and the redirect stand.
func TestScenarioNoForwarderNothingOut(t *testing.T) {
	p := goodPolicy()
	p.DNSResolver = netip.MustParseAddrPort("192.0.2.53:53")
	for _, r := range planRules(p) {
		require.NotEqual(t, uint16(53), r.port, "no rule names :53: %+v", r)
		require.NotEqual(t, p.DNSResolver.Addr(), r.dst, "no rule names the resolver: %+v", r)
		require.NotEqual(t, kindDNSRedirect, r.kind)
		require.NotEqual(t, kindDNSInputAccept, r.kind)
	}
	var kinds []ruleKind
	for _, r := range planRules(p) {
		if r.chain == egressPre {
			kinds = append(kinds, r.kind)
		}
	}
	require.Equal(t, []ruleKind{kindRedirectSkip, kindRedirect}, kinds, "only the internal endpoint is excluded from the redirect")
}

// redirected is the verdict runRule reports for a rule that ends in a redirect, which has no verdict of its own.
const redirected expr.VerdictKind = 100

// packet is what a chain's rules read of one packet or frame. iif is the interface index it came in on, 1 for loopback;
// arp marks an ARP frame, whose bytes the header shows as an IPv4 packet's, as no rule may read them; bridged marks one
// read in a table of the bridge family.
type packet struct {
	src, dst netip.Addr
	iif      uint32
	proto    byte
	dport    uint16
	ct       uint32
	arp      bool
	bridged  bool
}

func (pkt packet) ethertype() uint16 {
	switch {
	case pkt.arp:
		return unix.ETH_P_ARP
	case pkt.src.Is4():
		return unix.ETH_P_IP
	}
	return unix.ETH_P_IPV6
}

// header is the packet's network header, with its addresses where IPv4 or IPv6 holds them.
func (pkt packet) header() []byte {
	if pkt.src.Is4() {
		return append(append(make([]byte, 12), pkt.src.AsSlice()...), pkt.dst.AsSlice()...)
	}
	return append(append(make([]byte, 8), pkt.src.AsSlice()...), pkt.dst.AsSlice()...)
}

// runRule reads one rule's expressions over pkt as the kernel does, in one register: it reports the rule's verdict, and
// false when a match fails.
func runRule(t *testing.T, exprs []expr.Any, pkt packet) (expr.VerdictKind, bool) {
	t.Helper()
	var reg []byte
	for _, e := range exprs {
		switch e := e.(type) {
		case *expr.Meta:
			switch e.Key {
			case expr.MetaKeyNFPROTO:
				switch {
				case pkt.bridged:
					reg = []byte{unix.NFPROTO_BRIDGE}
				case pkt.src.Is4():
					reg = []byte{unix.NFPROTO_IPV4}
				default:
					reg = []byte{unix.NFPROTO_IPV6}
				}
			case expr.MetaKeyPROTOCOL:
				reg = binaryutil.BigEndian.PutUint16(pkt.ethertype())
			case expr.MetaKeyL4PROTO:
				reg = []byte{pkt.proto}
			case expr.MetaKeyIIF:
				reg = binaryutil.NativeEndian.PutUint32(pkt.iif)
			default:
				t.Fatalf("no reading of meta key %v", e.Key)
			}
		case *expr.Ct:
			reg = binaryutil.NativeEndian.PutUint32(pkt.ct)
		case *expr.Payload:
			hdr := pkt.header()
			if e.Base == expr.PayloadBaseTransportHeader {
				hdr = binary.BigEndian.AppendUint16([]byte{0, 0}, pkt.dport)
			}
			reg = hdr[e.Offset : e.Offset+e.Len]
		case *expr.Bitwise:
			masked := make([]byte, len(reg))
			for i := range reg {
				masked[i] = reg[i]&e.Mask[i] ^ e.Xor[i]
			}
			reg = masked
		case *expr.Cmp:
			if bytes.Equal(reg, e.Data) != (e.Op == expr.CmpOpEq) {
				return 0, false
			}
		case *expr.Verdict:
			return e.Kind, true
		case *expr.Immediate:
		case *expr.Redir:
			return redirected, true
		default:
			t.Fatalf("no reading of %T", e)
		}
	}
	return 0, false
}

// chainVerdict runs a chain's planned rules over pkt: the first rule that matches decides, and none leaves the chain's
// policy.
func chainVerdict(t *testing.T, p Policy, chain string, pkt packet) expr.VerdictKind {
	t.Helper()
	pkt.bridged = chain == lateralFwd
	sub := p.WorkerSubnet.Masked()
	for _, r := range planRules(p) {
		if r.chain != chain {
			continue
		}
		if v, ok := runRule(t, exprsFor(r, p, sub.Addr().As4(), net4Mask(sub.Bits())), pkt); ok {
			return v
		}
	}
	for _, tp := range planTables(p) {
		for _, ch := range tp.chains {
			if tp.name+"/"+ch.name == chain && ch.dropPol {
				return expr.VerdictDrop
			}
		}
	}
	return expr.VerdictAccept
}

// inputVerdict runs the input chain's planned rules over pkt.
func inputVerdict(t *testing.T, p Policy, pkt packet) expr.VerdictKind {
	t.Helper()
	return chainVerdict(t, p, egressIn, pkt)
}

// A worker's traffic to the node stops at the input chain, except its replies, the gateway, the forwarder and
// InternalAllow; the node's other traffic stays under the chain's accept policy (ADR-0115 Scope). The kernel's effect is
// the egress lane's.
func TestInputChainStopsAWorkersTraffic(t *testing.T) {
	const (
		loopback = 1
		bridge   = 5
		uplink   = 2
	)
	addr := netip.MustParseAddr
	worker, bridgeAddr, otherAddr := addr("10.63.0.5"), addr("10.63.0.1"), addr("192.0.2.1")
	p := forwarderPolicy()
	p.InternalAllow = append(p.InternalAllow, netip.AddrPortFrom(otherAddr, 9001))

	for _, tc := range []struct {
		name string
		pkt  packet
		want expr.VerdictKind
	}{
		{"a worker's UDP to the bridge address", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_UDP, dport: 5353}, expr.VerdictDrop},
		{"a worker's UDP to the node's other address", packet{src: worker, dst: otherAddr, iif: bridge, proto: unix.IPPROTO_UDP, dport: 5353}, expr.VerdictDrop},
		{"a worker's ICMP to the node", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_ICMP}, expr.VerdictDrop},
		{"a worker's TCP that is no reply and no redirect", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_TCP, dport: 8080}, expr.VerdictDrop},
		{"a worker's UDP to an internal endpoint's port", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_UDP, dport: 9000}, expr.VerdictDrop},
		{"a worker's TCP to an internal endpoint's address on another port", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_TCP, dport: 9001}, expr.VerdictDrop},
		{"a worker's TCP redirected to the gateway", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_TCP, dport: 15001}, expr.VerdictAccept},
		{"a worker's UDP redirected to the forwarder", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_UDP, dport: 15053}, expr.VerdictAccept},
		{"a worker's TCP redirected to the forwarder", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_TCP, dport: 15053}, expr.VerdictAccept},
		{"a worker's TCP to an internal endpoint", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_TCP, dport: 9000}, expr.VerdictAccept},
		{"a worker's TCP to an internal endpoint on the node's other address", packet{src: worker, dst: otherAddr, iif: bridge, proto: unix.IPPROTO_TCP, dport: 9001}, expr.VerdictAccept},
		{"a worker's reply to the node", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_UDP, dport: 40000, ct: expr.CtStateBitESTABLISHED}, expr.VerdictAccept},
		{"a worker's ICMP error about the node's call", packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_ICMP, ct: expr.CtStateBitRELATED}, expr.VerdictAccept},
		{"the node's UDP call to its bridge address, on loopback", packet{src: bridgeAddr, dst: bridgeAddr, iif: loopback, proto: unix.IPPROTO_UDP, dport: 5353}, expr.VerdictAccept},
		{"the node's TCP call to its bridge address, on loopback", packet{src: bridgeAddr, dst: bridgeAddr, iif: loopback, proto: unix.IPPROTO_TCP, dport: 8080}, expr.VerdictAccept},
		{"another host's UDP to the node", packet{src: addr("192.0.2.2"), dst: otherAddr, iif: uplink, proto: unix.IPPROTO_UDP, dport: 5353}, expr.VerdictAccept},
		{"an IPv6 packet with the subnet's bytes where IPv4 holds the source", packet{src: addr("2001:db8:a3f:1::2"), dst: addr("2001:db8:a3f:1::1"), iif: uplink, proto: unix.IPPROTO_UDP, dport: 5353}, expr.VerdictAccept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, inputVerdict(t, p, tc.pkt))
		})
	}

	t.Run("with no forwarder a worker's DNS to the node stops too", func(t *testing.T) {
		require.Equal(t, expr.VerdictDrop, inputVerdict(t, goodPolicy(), packet{src: worker, dst: bridgeAddr, iif: bridge, proto: unix.IPPROTO_UDP, dport: 53}))
	})
}

// scenario: worker-traffic-reaches-no-sibling — funcd_lateral drops an IPv4 frame from the worker subnet to the subnet, the
// limited broadcast or multicast. ARP, and traffic from any other source, pass on any bridge of the node. The kernel's
// effect is the egress lane's.
func TestScenarioWorkerTrafficReachesNoSibling(t *testing.T) {
	addr := netip.MustParseAddr
	worker, sibling, outside := addr("10.63.0.5"), addr("10.63.0.6"), addr("192.0.2.2")
	for _, tc := range []struct {
		name string
		pkt  packet
		want expr.VerdictKind
	}{
		{"a worker's unicast to a sibling", packet{src: worker, dst: sibling}, expr.VerdictDrop},
		{"a worker's subnet broadcast", packet{src: worker, dst: addr("10.63.255.255")}, expr.VerdictDrop},
		{"a worker's limited broadcast", packet{src: worker, dst: addr("255.255.255.255")}, expr.VerdictDrop},
		{"a worker's multicast, low end", packet{src: worker, dst: addr("224.0.0.0")}, expr.VerdictDrop},
		{"a worker's multicast", packet{src: worker, dst: addr("239.1.1.1")}, expr.VerdictDrop},
		{"a worker's multicast, high end", packet{src: worker, dst: addr("239.255.255.255")}, expr.VerdictDrop},
		{"a worker's frame just below multicast", packet{src: worker, dst: addr("223.255.255.255")}, expr.VerdictAccept},
		{"a worker's frame just above multicast", packet{src: worker, dst: addr("240.0.0.0")}, expr.VerdictAccept},
		{"a worker's frame to the node or beyond", packet{src: worker, dst: outside}, expr.VerdictAccept},
		{"a worker's ARP to the bridge", packet{arp: true, src: worker, dst: addr("10.63.0.1")}, expr.VerdictAccept},
		{"a worker's ARP broadcast", packet{arp: true, src: worker, dst: addr("255.255.255.255")}, expr.VerdictAccept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, chainVerdict(t, goodPolicy(), lateralFwd, tc.pkt))
		})
	}
}

// scenario: other-traffic-stays-untouched — traffic that is no worker's passes each chain as if the rules were not there.
// The IPv6 source holds the subnet's bytes where IPv4 holds a source, and an internal endpoint's where IPv4 holds a
// destination, so each rule reads a match there unless it matches IPv4 first. A worker's own traffic is the control.
func TestScenarioOtherTrafficStaysUntouched(t *testing.T) {
	addr := netip.MustParseAddr
	host6, node6, other6 := addr("2001:db8:a3f:1:a3f:1::2"), addr("2001:db8:a3f:1::1"), addr("2001:db8:a3f:2:a3f:2::3")
	worker, internal, external := addr("10.63.0.5"), addr("10.63.0.1"), addr("192.0.2.9")
	const (
		tcp = unix.IPPROTO_TCP
		udp = unix.IPPROTO_UDP
	)
	for _, tc := range []struct {
		name  string
		chain string
		pkt   packet
		want  expr.VerdictKind
	}{
		{"IPv6 TCP to a node service", egressPre, packet{src: host6, dst: node6, proto: tcp, dport: 8080}, expr.VerdictAccept},
		{"IPv6 TCP to an internal endpoint's port", egressPre, packet{src: host6, dst: node6, proto: tcp, dport: 9000}, expr.VerdictAccept},
		{"IPv6 UDP to port 53", egressPre, packet{src: host6, dst: node6, proto: udp, dport: 53}, expr.VerdictAccept},
		{"IPv6 TCP to port 53", egressPre, packet{src: host6, dst: node6, proto: tcp, dport: 53}, expr.VerdictAccept},
		{"IPv6 to an internal endpoint's port takes the forward policy", egressFwd, packet{src: host6, dst: other6, proto: tcp, dport: 9000}, expr.VerdictDrop},
		{"IPv6 to a node service in the input chain", egressIn, packet{src: host6, dst: node6, iif: 2, proto: tcp, dport: 8080}, expr.VerdictAccept},
		{"IPv6 between two hosts of a bridge", lateralFwd, packet{src: host6, dst: other6, proto: tcp, dport: 8080}, expr.VerdictAccept},
		{"IPv4 unicast between two hosts of another bridge", lateralFwd, packet{src: addr("192.0.2.2"), dst: addr("192.0.2.3")}, expr.VerdictAccept},
		{"IPv4 unicast from another source into the subnet", lateralFwd, packet{src: addr("192.0.2.2"), dst: addr("10.63.0.6")}, expr.VerdictAccept},
		{"IPv4 broadcast from another source", lateralFwd, packet{src: addr("192.0.2.2"), dst: addr("255.255.255.255")}, expr.VerdictAccept},
		{"IPv4 broadcast from no address yet", lateralFwd, packet{src: addr("0.0.0.0"), dst: addr("255.255.255.255")}, expr.VerdictAccept},
		{"IPv4 multicast from another source", lateralFwd, packet{src: addr("192.0.2.2"), dst: addr("224.0.0.251")}, expr.VerdictAccept},
		{"a worker's TCP is redirected", egressPre, packet{src: worker, dst: external, proto: tcp, dport: 443}, redirected},
		{"a worker's UDP to port 53 is redirected", egressPre, packet{src: worker, dst: external, proto: udp, dport: 53}, redirected},
		{"a worker's TCP to an internal endpoint is skipped", egressPre, packet{src: worker, dst: internal, proto: tcp, dport: 9000}, expr.VerdictReturn},
		{"a worker's TCP to an internal endpoint is forwarded", egressFwd, packet{src: worker, dst: internal, proto: tcp, dport: 9000}, expr.VerdictAccept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, chainVerdict(t, forwarderPolicy(), tc.chain, tc.pkt))
		})
	}
}

// Every planned rule that reads a network-header field matches IPv4 first, by the match its table's family can read: meta
// protocol in the bridge family, where meta nfproto reads NFPROTO_BRIDGE, and meta nfproto in the inet family.
func TestEveryAddressRuleMatchesIPv4First(t *testing.T) {
	p := forwarderPolicy()
	sub := p.WorkerSubnet.Masked()
	reads := 0
	for _, r := range planRules(p) {
		wantKey, wantData := expr.MetaKeyNFPROTO, []byte{unix.NFPROTO_IPV4}
		if r.chain == lateralFwd {
			wantKey, wantData = expr.MetaKeyPROTOCOL, binaryutil.BigEndian.PutUint16(unix.ETH_P_IP)
		}
		exprs := exprsFor(r, p, sub.Addr().As4(), net4Mask(sub.Bits()))
		matched := false
		for i, e := range exprs {
			if m, ok := e.(*expr.Meta); ok && m.Key == wantKey {
				cmp := exprs[i+1].(*expr.Cmp)
				matched = cmp.Op == expr.CmpOpEq && bytes.Equal(cmp.Data, wantData)
			}
			if pl, ok := e.(*expr.Payload); ok && pl.Base == expr.PayloadBaseNetworkHeader {
				reads++
				require.True(t, matched, "rule kind %d in %s reads an address before it matches IPv4", r.kind, r.chain)
			}
		}
	}
	require.Positive(t, reads)
}
