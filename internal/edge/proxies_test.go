package edge_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/edge"
)

// ADR-0354 Decisions 2 and 3: the first entry that matches the peer decides; a header entry takes its header when it
// holds exactly one IP address, else the peer; a cidrs entry walks X-Forwarded-For from the right past the addresses a
// cidrs entry trusts, the peer when an element is no address; a cidrs entry never trusts the worker subnet, and an
// addonKinds entry trusts only an engine of its kinds, spelled the same.
func TestProxiesClient(t *testing.T) {
	t.Parallel()
	tunnel := edge.TrustedProxy{AddonKinds: []string{"Tunnel"}, Header: "X-Real-IP"}
	cdn := edge.TrustedProxy{CIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}}
	wide := edge.TrustedProxy{CIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	workers := netip.MustParsePrefix("10.63.0.0/16")
	for _, tc := range []struct {
		what     string
		entries  []edge.TrustedProxy
		peer     string
		kind     string
		headers  map[string][]string
		expected string
	}{
		{"tunnel-client-from-header", []edge.TrustedProxy{tunnel}, "10.63.0.3", "Tunnel", map[string][]string{"X-Real-Ip": {"198.51.100.7"}}, "198.51.100.7"},
		{"forwarded-for-walk", []edge.TrustedProxy{cdn}, "203.0.113.9", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7, 203.0.113.4"}}, "198.51.100.7"},
		{"untrusted-peer-headers-dropped", []edge.TrustedProxy{cdn}, "192.0.2.1", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7"}, "X-Real-Ip": {"198.51.100.8"}}, "192.0.2.1"},
		{"a-function-cannot-forge: the header entry", []edge.TrustedProxy{tunnel, wide}, "10.63.0.4", "", map[string][]string{"X-Real-Ip": {"198.51.100.7"}, "X-Forwarded-For": {"198.51.100.8"}}, "10.63.0.4"},
		{"an engine of another kind", []edge.TrustedProxy{tunnel}, "10.63.0.3", "IdentityProvider", map[string][]string{"X-Real-Ip": {"198.51.100.7"}}, "10.63.0.3"},
		{"the kind in another case", []edge.TrustedProxy{tunnel}, "10.63.0.3", "TUNNEL", map[string][]string{"X-Real-Ip": {"198.51.100.7"}}, "10.63.0.3"},
		{"a header that is no address", []edge.TrustedProxy{tunnel}, "10.63.0.3", "Tunnel", map[string][]string{"X-Real-Ip": {"198.51.100.7, 198.51.100.8"}}, "10.63.0.3"},
		{"a header twice", []edge.TrustedProxy{tunnel}, "10.63.0.3", "Tunnel", map[string][]string{"X-Real-Ip": {"198.51.100.7", "198.51.100.8"}}, "10.63.0.3"},
		{"no header", []edge.TrustedProxy{tunnel}, "10.63.0.3", "Tunnel", nil, "10.63.0.3"},
		{"an IPv6 header", []edge.TrustedProxy{tunnel}, "10.63.0.3", "Tunnel", map[string][]string{"X-Real-Ip": {" 2001:db8::7 "}}, "2001:db8::7"},
		{"an all-trusted chain", []edge.TrustedProxy{cdn}, "203.0.113.9", "", map[string][]string{"X-Forwarded-For": {"203.0.113.1, 203.0.113.4"}}, "203.0.113.1"},
		{"no chain", []edge.TrustedProxy{cdn}, "203.0.113.9", "", nil, "203.0.113.9"},
		{"an element that is no address", []edge.TrustedProxy{cdn}, "203.0.113.9", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7, garbage"}}, "203.0.113.9"},
		{"a chain on two lines", []edge.TrustedProxy{cdn}, "203.0.113.9", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7", "203.0.113.4"}}, "198.51.100.7"},
		{"a worker-subnet peer under a wide range", []edge.TrustedProxy{wide}, "10.63.0.4", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7"}}, "10.63.0.4"},
		{"the wide range off the worker subnet", []edge.TrustedProxy{wide}, "10.1.0.4", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7"}}, "198.51.100.7"},
		{"a worker-subnet hop in the chain", []edge.TrustedProxy{wide}, "10.1.0.4", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7, 10.63.0.5"}}, "10.63.0.5"},
		{"the first matching entry decides", []edge.TrustedProxy{cdn, {CIDRs: cdn.CIDRs, Header: "X-Real-IP"}}, "203.0.113.9", "",
			map[string][]string{"X-Forwarded-For": {"198.51.100.7"}, "X-Real-Ip": {"198.51.100.8"}}, "198.51.100.7"},
		{"an engine whose header entry comes second", []edge.TrustedProxy{cdn, tunnel}, "10.63.0.3", "Tunnel", map[string][]string{"X-Real-Ip": {"198.51.100.7"}}, "198.51.100.7"},
		{"no entry", nil, "198.51.100.9", "", map[string][]string{"X-Forwarded-For": {"198.51.100.7"}}, "198.51.100.9"},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
		r.Header = http.Header(tc.headers)
		got := edge.Proxies{Entries: tc.entries, Workers: workers}.Client(r, netip.MustParseAddr(tc.peer), tc.kind)
		require.Equal(t, netip.MustParseAddr(tc.expected), got, tc.what)
	}
}

// The client address a step set is the one every later step reads; without one, the peer from RemoteAddr.
func TestClientOfFallsBackToThePeer(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	r.RemoteAddr = "[::ffff:192.0.2.1]:4242"
	require.Equal(t, netip.MustParseAddr("192.0.2.1"), edge.ClientOf(r))
	r = r.WithContext(edge.WithClient(r.Context(), netip.MustParseAddr("198.51.100.7")))
	require.Equal(t, netip.MustParseAddr("198.51.100.7"), edge.ClientOf(r))
	require.Equal(t, netip.MustParseAddr("192.0.2.1"), edge.Peer(r))
}
