package main

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
)

func containerdCfg(mod func(*config.Config)) config.Config {
	var c config.Config
	c.Runtime.Mode = "containerd"
	c.Runtime.Containerd.SubnetCIDR = "10.63.0.0/16"
	c.S3Gateway.ListenAddr = "127.0.0.1:9000"
	if mod != nil {
		mod(&c)
	}
	return c
}

// The daemon hands the library the derived bridge address under containerd (scenario bridge-address-from-subnet: the
// proxies, the feed and the allow list read this one value), and none in the process runtime.
func TestDaemonBridgeAddress(t *testing.T) {
	a, ok := bridgeAddress(containerdCfg(nil))
	require.True(t, ok)
	require.Equal(t, netip.MustParseAddr("10.63.0.1"), a)
	_, ok = bridgeAddress(containerdCfg(func(c *config.Config) { c.Runtime.Mode = "process" }))
	require.False(t, ok, "the process runtime keeps the library's loopback")
}

// scenario: s3-endpoint-from-bridge — an empty s3gateway.endpoint derives from the bridge address when the gateway
// listens on an empty or unspecified host or on the bridge; a loopback listener or an explicit endpoint is kept.
func TestScenarioS3EndpointFromBridge(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:9000":   "http://10.63.0.1:9000",
		"[::]:9000":      "http://10.63.0.1:9000",
		":9000":          "http://10.63.0.1:9000",
		"10.63.0.1:9000": "http://10.63.0.1:9000",
		"127.0.0.1:9000": "",
	} {
		require.Equal(t, want, s3Endpoint(containerdCfg(func(c *config.Config) { c.S3Gateway.ListenAddr = listen })), listen)
	}
	require.Equal(t, "http://s3.example:9000", s3Endpoint(containerdCfg(func(c *config.Config) {
		c.S3Gateway.ListenAddr, c.S3Gateway.Endpoint = "0.0.0.0:9000", "http://s3.example:9000"
	})))
	require.Empty(t, s3Endpoint(containerdCfg(func(c *config.Config) { c.Runtime.Mode, c.S3Gateway.ListenAddr = "process", "0.0.0.0:9000" })))
}
