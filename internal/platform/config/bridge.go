package config

import (
	"net/netip"

	"github.com/pyvvo/funcd/api/fault"
)

// BridgeAddress is funcd's address on its container bridge (ADR-0238), where the catalog proxies, the SQL change feed
// and a derived S3 endpoint are reached from a container: under containerd runtime.containerd.bridgeAddress or the
// subnet's network address + 1, else 127.0.0.1. Validate refuses a value CheckBridge refuses, so the error is only for
// a Config built by hand.
func (c Config) BridgeAddress() (netip.Addr, error) {
	const op = "config.BridgeAddress"
	if c.Runtime.Mode != "containerd" {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1}), nil
	}
	if b := c.Runtime.Containerd.BridgeAddress; b != "" {
		a, err := netip.ParseAddr(b)
		if err != nil {
			return netip.Addr{}, fault.Invalidf(op, "runtime.containerd.bridgeAddress %q: want an IP address", b)
		}
		return a, nil
	}
	sub, err := netip.ParsePrefix(c.Runtime.Containerd.SubnetCIDR)
	if err != nil {
		return netip.Addr{}, fault.Invalidf(op, "runtime.containerd.subnetCIDR %q: %v", c.Runtime.Containerd.SubnetCIDR, err)
	}
	return sub.Masked().Addr().Next(), nil
}

// CheckBridge applies ADR-0238 Decision 1: under containerd, runtime.containerd.bridgeAddress is an IP in the subnet.
func (c Config) CheckBridge() []Finding {
	var f findings
	b := c.Runtime.Containerd.BridgeAddress
	if b == "" || c.Runtime.Mode != "containerd" {
		return nil
	}
	a, err := netip.ParseAddr(b)
	if err != nil {
		f.refuse("runtime.containerd.bridgeAddress", "runtime.containerd.bridgeAddress %q: want an IP address", b)
		return f.errs
	}
	if sub, serr := netip.ParsePrefix(c.Runtime.Containerd.SubnetCIDR); serr == nil && !sub.Contains(a) {
		f.refuse("runtime.containerd.bridgeAddress", "runtime.containerd.bridgeAddress %s is outside runtime.containerd.subnetCIDR %s", a, sub)
	}
	return f.errs
}
