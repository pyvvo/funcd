//go:build linux

package network

import (
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/stretchr/testify/require"
)

// scenario: ruleset-programmed — the structural plan F80 programs: a bridge-family lateral-deny table and
// an inet egress-policy table whose forward-filter chain defaults to drop (so the accepts must precede it)
// and whose prerouting chain is NAT (the redirect site). This is the correct-by-construction core; the
// runtime effect (drops/redirects/accepts) is verified by the Lima e2e lane (deferred — needs a kernel).
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
	require.False(t, chains["input"].dropPol, "the input chain accepts the redirected-to-gateway packets")
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
