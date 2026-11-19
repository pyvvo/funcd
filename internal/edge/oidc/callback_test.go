package oidc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ADR-0335 Decision 9: the scheme is https with server.tls.enabled or server.edgePublicScheme https; the port is
// server.edgePublicPort, else the bound port, or none when only the public scheme makes it https; the scheme's own
// port is left out.
func TestCallbackURLFollowsDecision9(t *testing.T) {
	t.Parallel()
	bound := func() int { return 8081 }
	for _, tc := range []struct {
		name string
		c    Callbacks
		want string
	}{
		{"plain http on the bound port", Callbacks{Bound: bound}, "http://localhost:8081/.auth/callback"},
		{"funcd terminates TLS", Callbacks{TLS: true, Bound: bound}, "https://localhost:8081/.auth/callback"},
		{"a TLS proxy in front", Callbacks{Scheme: "https", Bound: bound}, "https://localhost/.auth/callback"},
		{"a TLS proxy on a port", Callbacks{Scheme: "https", Port: 8443, Bound: bound}, "https://localhost:8443/.auth/callback"},
		{"https on its own port", Callbacks{TLS: true, Port: 443, Bound: bound}, "https://localhost/.auth/callback"},
		{"http on its own port", Callbacks{Port: 80, Bound: bound}, "http://localhost/.auth/callback"},
	} {
		require.Equal(t, tc.want, tc.c.URL("localhost"), tc.name)
	}
	require.Equal(t, "http://[::1]:8081/.auth/callback", Callbacks{Bound: bound}.URL("::1"))
	require.Equal(t, "http://127.0.0.1:8081/.auth/callback", Callbacks{Bound: bound}.URL("127.0.0.1"))
	require.True(t, Callbacks{Scheme: "https"}.HTTPS())
	require.False(t, Callbacks{Scheme: "http"}.HTTPS())
}
