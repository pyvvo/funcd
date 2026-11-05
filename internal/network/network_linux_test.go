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

// packet is what the input chain's rules read of one packet. iif is the interface index it came in on, 1 for loopback.
type packet struct {
	src, dst netip.Addr
	iif      uint32
	proto    byte
	dport    uint16
	ct       uint32
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
				reg = []byte{unix.NFPROTO_IPV6}
				if pkt.src.Is4() {
					reg = []byte{unix.NFPROTO_IPV4}
				}
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
		default:
			t.Fatalf("no reading of %T", e)
		}
	}
	return 0, false
}

// inputVerdict runs the input chain's planned rules over pkt: the first rule that matches decides, and none leaves the
// chain's policy.
func inputVerdict(t *testing.T, p Policy, pkt packet) expr.VerdictKind {
	t.Helper()
	sub := p.WorkerSubnet.Masked()
	for _, r := range planRules(p) {
		if r.chain != egressIn {
			continue
		}
		if v, ok := runRule(t, exprsFor(r, p, sub.Addr().As4(), net4Mask(sub.Bits())), pkt); ok {
			return v
		}
	}
	for _, tp := range planTables(p) {
		for _, ch := range tp.chains {
			if tp.name+"/"+ch.name == egressIn && ch.dropPol {
				return expr.VerdictDrop
			}
		}
	}
	return expr.VerdictAccept
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
