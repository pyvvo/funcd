//go:build linux

package network

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// forwarderPolicy is goodPolicy with the F81 DNS forwarder configured.
func forwarderPolicy() Policy {
	p := goodPolicy()
	p.DNSForwarderPort = 15053
	return p
}

// scenario: dns-redirect-flip (F81) — when a DNS forwarder is configured, worker :53 (UDP+TCP) is
// REDIRECTed into it (so the forwarder is the only resolver), the general external-TCP redirect stays
// LAST in prerouting, and the input chain accepts the redirected DNS on the forwarder port. Behavioral
// effect is Lima-verified; here the rule PLAN is asserted (correct-by-construction).
func TestScenarioDNSRedirectFlip(t *testing.T) {
	rules := planRules(forwarderPolicy())

	var pre, in []rulePlan
	for _, r := range rules {
		switch r.chain {
		case egressPre:
			pre = append(pre, r)
		case egressIn:
			in = append(in, r)
		}
	}

	// prerouting: two DNS-redirect rules (UDP+TCP :53), and the general redirect is still LAST.
	dnsRedirectProtos := map[byte]bool{}
	for _, r := range pre {
		if r.kind == kindDNSRedirect {
			require.Equal(t, uint16(53), r.port, "the DNS redirect matches :53")
			dnsRedirectProtos[r.proto] = true
		}
	}
	require.True(t, dnsRedirectProtos[unix.IPPROTO_UDP], "UDP :53 is redirected into the forwarder")
	require.True(t, dnsRedirectProtos[unix.IPPROTO_TCP], "TCP :53 is redirected into the forwarder")
	require.Equal(t, kindRedirect, pre[len(pre)-1].kind, "the general external-TCP redirect stays LAST")

	// The node resolver is NOT skipped anymore — the worker cannot reach it directly (forwarder is the only
	// resolver). Match the resolver's full addr:port: an InternalAllow endpoint may legitimately share the
	// resolver's IP on a different port (e.g. 10.63.0.1:9000 vs 10.63.0.1:53), and that skip is allowed.
	dns := forwarderPolicy().DNSResolver
	for _, r := range pre {
		if r.kind == kindRedirectSkip {
			require.False(t, r.dst == dns.Addr() && r.port == dns.Port(), "no direct-resolver (addr:port) skip when the forwarder is armed")
		}
	}

	// input: accept the redirected DNS on the forwarder port (UDP+TCP).
	inputDNS := map[byte]bool{}
	for _, r := range in {
		if r.kind == kindDNSInputAccept {
			require.Equal(t, uint16(15053), r.port, "input accepts the forwarder port")
			inputDNS[r.proto] = true
		}
	}
	require.True(t, inputDNS[unix.IPPROTO_UDP])
	require.True(t, inputDNS[unix.IPPROTO_TCP])

	// The forward chain has NO DNS accept when the forwarder is armed (DNS is DNAT'd to local, never
	// forwarded). Same addr:port precision — an InternalAllow forward-accept may share the resolver IP.
	for _, r := range rules {
		if r.chain == egressFwd && r.kind == kindForwardAccept {
			require.False(t, r.dst == dns.Addr() && r.port == dns.Port(), "no forward-accept for the node resolver (addr:port) under the forwarder")
		}
	}
}
