//go:build linux

package network

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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
	require.Error(t, FenceNetns("/nonexistent", []netip.AddrPort{netip.MustParseAddrPort("[::1]:9000")}, nil), "an IPv6 endpoint is refused")
}

// The input chain accepts loopback, then established or related traffic, then IPv4 from each admit address, funcd's
// bridge address, and nothing else: its drop policy refuses a connection from any other worker (ADR-0332 Decision 3).
// The packet-level effect is the addon-rauthy lane's.
func TestFenceAdmitsOnlyLoopbackAndTheBridgeAddress(t *testing.T) {
	bridge := netip.MustParseAddr("10.63.0.1")
	rules := admitRules([]netip.Addr{bridge})
	require.Len(t, rules, 3)
	for i, r := range rules {
		v, ok := r[len(r)-1].(*expr.Verdict)
		require.True(t, ok && v.Kind == expr.VerdictAccept, "rule %d ends in accept", i)
	}
	require.Equal(t, expr.MetaKeyIIF, rules[0][0].(*expr.Meta).Key, "rule 0 accepts what comes in on loopback")
	require.IsType(t, &expr.Ct{}, rules[1][0], "rule 1 accepts established or related traffic")
	var src []byte
	for _, e := range rules[2] {
		if p, ok := e.(*expr.Payload); ok {
			require.Equal(t, uint32(srcOffset), p.Offset, "rule 2 reads the source address")
		}
		if c, ok := e.(*expr.Cmp); ok && len(c.Data) == 4 {
			src = c.Data
		}
	}
	require.Equal(t, []byte{10, 63, 0, 1}, src, "rule 2 matches the bridge address")

	require.Len(t, admitRules([]netip.Addr{}), 2, "an empty list admits loopback alone")
	require.Error(t, FenceNetns("/nonexistent", []netip.AddrPort{}, []netip.Addr{netip.MustParseAddr("::1")}), "an IPv6 source is refused")
}

// ADR-0352 Decision 2: with no allow list the fence writes no output chain, so a host-only engine with egress on keeps
// its input chain alone; an empty allow list still writes one that allows no new connection.
func TestFenceChainsLeaveOutTheOutputChainWithNoAllowList(t *testing.T) {
	bridge := []netip.Addr{netip.MustParseAddr("10.63.0.1")}
	names := func(chains []fenceChain) []string {
		var out []string
		for _, c := range chains {
			out = append(out, c.name)
		}
		return out
	}
	require.Equal(t, []string{"input"}, names(fenceChains(nil, bridge)))
	require.Equal(t, []string{"output", "input"}, names(fenceChains([]netip.AddrPort{}, bridge)))
	require.Equal(t, []string{"output"}, names(fenceChains([]netip.AddrPort{netip.MustParseAddrPort("10.63.0.1:9000")}, nil)))
	require.Equal(t, nftables.ChainHookInput, fenceChains(nil, bridge)[0].hook)
}

// ipv6Tree makes a tree shaped as /proc/sys/net/ipv6, each link's disable_ipv6 reading 0.
func ipv6Tree(t *testing.T, links ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, link := range links {
		dir := filepath.Join(root, "conf", link)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "disable_ipv6"), []byte("0"), 0o600))
	}
	return root
}

// scenario: worker-eth0-has-no-ipv6 — the writes: eth0 and default read 1, and all and lo keep what they held, so loopback
// keeps IPv6. A missing setting under a present tree is an error, so the worker does not start; a kernel with no IPv6 has
// nothing to write. The kernel's effect is the egress lane's.
func TestScenarioWorkerEth0HasNoIPv6(t *testing.T) {
	root := ipv6Tree(t, "all", "default", "eth0", "lo")
	require.NoError(t, disableIPv6(root))
	for link, want := range map[string]string{"all": "0", "default": "1", "eth0": "1", "lo": "0"} {
		got, err := os.ReadFile(filepath.Join(root, "conf", link, "disable_ipv6"))
		require.NoError(t, err)
		require.Equal(t, want, string(got), link)
	}

	require.ErrorIs(t, disableIPv6(ipv6Tree(t, "default", "lo")), os.ErrNotExist, "no eth0")
	require.ErrorIs(t, disableIPv6(ipv6Tree(t, "eth0", "lo")), os.ErrNotExist, "no default")

	noIPv6 := filepath.Join(t.TempDir(), "ipv6")
	require.NoError(t, disableIPv6(noIPv6), "a kernel with no IPv6")
	require.NoDirExists(t, noIPv6)
}

// A path that is no network namespace is an error, whether it cannot be opened or cannot be entered.
func TestDisableWorkerIPv6RefusesAPathThatIsNoNetns(t *testing.T) {
	require.Error(t, DisableWorkerIPv6(filepath.Join(t.TempDir(), "none")))
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	require.ErrorIs(t, DisableWorkerIPv6(file), unix.EINVAL)
}
