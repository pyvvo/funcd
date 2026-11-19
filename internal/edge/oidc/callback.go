package oidc

import (
	"net"
	"strconv"
)

// Callbacks builds the edge's sign-in callbacks (ADR-0335 Decision 9): <scheme>://<host>[:<port>]/.auth/callback.
type Callbacks struct {
	// TLS is server.tls.enabled: funcd terminates TLS, so the scheme is https.
	TLS bool
	// Scheme is server.edgePublicScheme: https behind a proxy that terminates TLS.
	Scheme string
	// Port is server.edgePublicPort; 0 is Bound, or none when only Scheme makes the scheme https.
	Port int
	// Bound is the data plane's bound port.
	Bound func() int
}

// HTTPS reports whether the callbacks are https, with Secure __Host- cookies.
func (c Callbacks) HTTPS() bool { return c.TLS || c.Scheme == "https" }

// URL is host's callback; the scheme's own port is left out.
func (c Callbacks) URL(host string) string {
	scheme, port := "http", c.Port
	if c.HTTPS() {
		scheme = "https"
	}
	if port == 0 && (c.TLS || !c.HTTPS()) && c.Bound != nil {
		port = c.Bound()
	}
	if port == 0 || scheme == "https" && port == defaultHTTPSPort || scheme == "http" && port == defaultHTTPPort {
		return scheme + "://" + bracketed(host) + callbackPath
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + callbackPath
}

const (
	defaultHTTPSPort = 443
	defaultHTTPPort  = 80
)

// bracketed puts an IPv6 host in brackets, as a URL names it.
func bracketed(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + host + "]"
	}
	return host
}
