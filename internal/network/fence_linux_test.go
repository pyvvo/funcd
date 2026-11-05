//go:build linux

package network

import (
	"net/netip"
	"testing"

	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/require"
)

// scenario: query-cannot-redirect — the fence accepts loopback, then established or related traffic, then IPv4 TCP to
// each allowed endpoint, and nothing else: the chain's drop policy refuses the rest (ADR-0269). The packet-level effect
// is the duckdb lane's.
func TestScenarioFenceRefusesOtherDestinations(t *testing.T) {
	gw := netip.MustParseAddrPort("10.63.0.1:9000")
	rules := fenceRules([]netip.AddrPort{gw})
	require.Len(t, rules, 3)
	for i, r := range rules {
		v, ok := r[len(r)-1].(*expr.Verdict)
		require.True(t, ok && v.Kind == expr.VerdictAccept, "rule %d ends in accept", i)
	}
	require.IsType(t, &expr.Meta{}, rules[0][0])
	require.Equal(t, expr.MetaKeyOIF, rules[0][0].(*expr.Meta).Key, "rule 0 accepts loopback")
	require.Equal(t, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(1)}, rules[0][1], "and only loopback")
	require.IsType(t, &expr.Ct{}, rules[1][0], "rule 1 accepts established or related traffic")
	var dst, port []byte
	for _, e := range rules[2] {
		if c, ok := e.(*expr.Cmp); ok && len(c.Data) == 4 {
			dst = c.Data
		}
		if c, ok := e.(*expr.Cmp); ok && len(c.Data) == 2 {
			port = c.Data
		}
	}
	require.Equal(t, []byte{10, 63, 0, 1}, dst, "rule 2 matches the gateway's address")
	require.Equal(t, []byte{0x23, 0x28}, port, "rule 2 matches the gateway's port")

	require.Len(t, fenceRules([]netip.AddrPort{}), 2, "an empty list allows no new outbound connection")
	require.Error(t, FenceNetns("/nonexistent", []netip.AddrPort{netip.MustParseAddrPort("[::1]:9000")}), "an IPv6 endpoint is refused")
}
