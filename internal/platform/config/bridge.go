package config

import (
	"net"
	"net/netip"

	"github.com/pyvvo/funcd/api/fault"
)

// BridgeAddress is funcd's address on its container bridge (ADR-0238), where the catalog proxies, the SQL change feed
// and a derived S3 endpoint are reached from a container: catalog.proxyHost when set, else under containerd
// runtime.containerd.bridgeAddress or the subnet's network address + 1, else 127.0.0.1. Validate refuses a value
// CheckBridge refuses, so the error is only for a Config built by hand.
func (c Config) BridgeAddress() (netip.Addr, error) {
	const op = "config.BridgeAddress"
	if c.Catalog.ProxyHost != "" {
		a, err := netip.ParseAddr(c.Catalog.ProxyHost)
		if err != nil {
			return netip.Addr{}, fault.Invalidf(op, "catalog.proxyHost %q: want an IP address", c.Catalog.ProxyHost)
		}
		return a, nil
	}
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

// CheckBridge applies ADR-0238 Decision 2: runtime.containerd.bridgeAddress is an IP in the subnet; catalog.proxyHost,
// its old name, is an IP of this host or of the bridge subnet and, under containerd, agrees with the new field; a set
// catalog.proxyHost is reported for its new name.
func (c Config) CheckBridge() []Finding {
	var f findings
	sub, subErr := netip.ParsePrefix(c.Runtime.Containerd.SubnetCIDR)
	containerd := c.Runtime.Mode == "containerd"
	var bridge netip.Addr
	if b := c.Runtime.Containerd.BridgeAddress; b != "" && containerd {
		a, err := netip.ParseAddr(b)
		switch {
		case err != nil:
			f.refuse("runtime.containerd.bridgeAddress", "runtime.containerd.bridgeAddress %q: want an IP address", b)
		case subErr == nil && !sub.Contains(a):
			f.refuse("runtime.containerd.bridgeAddress", "runtime.containerd.bridgeAddress %s is outside runtime.containerd.subnetCIDR %s", a, sub)
		default:
			bridge = a
		}
	}
	if h := c.Catalog.ProxyHost; h != "" {
		a, err := netip.ParseAddr(h)
		onBridge := containerd && subErr == nil && sub.Contains(a)
		switch {
		case err != nil:
			f.refuse("catalog.proxyHost", "catalog.proxyHost %q: want an IP address", h)
		case !onBridge && !hostAddress(a):
			f.refuse("catalog.proxyHost", "catalog.proxyHost %s is not an address of this host or of the bridge subnet, so nothing can bind it", a)
		case bridge.IsValid() && a != bridge:
			f.refuse("catalog.proxyHost", "catalog.proxyHost %s and runtime.containerd.bridgeAddress %s differ: set runtime.containerd.bridgeAddress only", a, bridge)
		default:
			f.warn("catalog.proxyHost", "catalog.proxyHost is the old name of runtime.containerd.bridgeAddress (ADR-0238): set that instead")
		}
	}
	return append(f.errs, f.warns...)
}

// hostAddress reports whether a is loopback or an address of one of this host's interfaces.
func hostAddress(a netip.Addr) bool {
	if a.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, x := range addrs {
		if n, ok := x.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == a.Unmap() {
				return true
			}
		}
	}
	return false
}
