package config_test

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

func bridgeCfg(t *testing.T, yaml string) config.Config {
	t.Helper()
	c, err := config.Load(writeCfg(t, yaml), config.Flags{})
	require.NoError(t, err)
	return c
}

func requireBridge(t *testing.T, c config.Config, want string) {
	t.Helper()
	a, err := c.BridgeAddress()
	require.NoError(t, err)
	require.Equal(t, netip.MustParseAddr(want), a)
}

func refused(t *testing.T, yaml, key string) {
	t.Helper()
	_, err := config.Load(writeCfg(t, yaml), config.Flags{})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, key)
}

const containerd = "runtime:\n  mode: containerd\n"

// scenario: bridge-address-from-subnet — under containerd with nothing configured the address is the default subnet's
// network address + 1, and nothing is reported.
func TestScenarioBridgeAddressFromSubnet(t *testing.T) {
	t.Parallel()
	c := bridgeCfg(t, containerd)
	requireBridge(t, c, "10.63.0.1")
	require.Empty(t, c.CheckBridge())
}

// scenario: bridge-address-follows-subnet
func TestScenarioBridgeAddressFollowsSubnet(t *testing.T) {
	t.Parallel()
	requireBridge(t, bridgeCfg(t, containerd+"  containerd:\n    subnetCIDR: 10.70.0.0/16\n"), "10.70.0.1")
}

// scenario: bridge-address-explicit
func TestScenarioBridgeAddressExplicit(t *testing.T) {
	t.Parallel()
	requireBridge(t, bridgeCfg(t, containerd+"  containerd:\n    subnetCIDR: 10.70.0.0/16\n    bridgeAddress: 10.70.0.9\n"), "10.70.0.9")
}

// scenario: bridge-address-outside-subnet-refused
func TestScenarioBridgeAddressOutsideSubnetRefused(t *testing.T) {
	t.Parallel()
	refused(t, containerd+"  containerd:\n    bridgeAddress: 10.70.0.9\n", "runtime.containerd.bridgeAddress")
	refused(t, containerd+"  containerd:\n    bridgeAddress: funcd0\n", "runtime.containerd.bridgeAddress")
}

// scenario: proxy-host-refused — lake.proxyHost, the field this replaces, is no longer read: a config that sets it
// is refused at start, naming it.
func TestScenarioProxyHostRefused(t *testing.T) {
	t.Parallel()
	refused(t, containerd+"lake:\n  proxyHost: 10.63.0.1\n", "proxyHost")
}

// scenario: process-runtime-loopback
func TestScenarioProcessRuntimeLoopback(t *testing.T) {
	t.Parallel()
	c := bridgeCfg(t, "runtime:\n  mode: process\n  containerd:\n    bridgeAddress: 10.70.0.9\n")
	requireBridge(t, c, "127.0.0.1")
	require.Empty(t, c.CheckBridge(), "the process runtime does not read the field")
}
