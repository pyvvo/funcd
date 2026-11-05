package config

import "net"

// IsLoopbackAddr reports whether addr, a host:port to bind, stays on this machine: its host is localhost or an IP with
// IsLoopback (ADR-0314 Decision 2). 0.0.0.0, ::, an empty host, any other address or hostname and an addr that does
// not split into host and port are not.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
