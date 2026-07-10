package auth

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// scenario: netdest-path-roundtrip — the NetDestination Path codec round-trips (with and without a
// domain set), the UID prefix is independent of the domain segment, and a malformed prefix is rejected.
func TestScenarioNetDestPathRoundtrip(t *testing.T) {
	t.Parallel()

	cases := []NetDestination{
		{IP: netip.MustParseAddr("93.184.216.34"), Port: 443, Domains: []string{"api.x.com", "*.x.com"}},
		{IP: netip.MustParseAddr("10.0.0.5"), Port: 5432, Domains: nil}, // pure-IP/CIDR target, no domains
		{IP: netip.MustParseAddr("2001:db8::1"), Port: 443, Domains: []string{"v6.x.com"}},
	}
	for _, want := range cases {
		path := EncodeNetDestPath(want.IP, want.Port, want.Domains)
		got, err := ParseNetDestPath(path)
		require.NoError(t, err)
		require.Equal(t, want.IP, got.IP)
		require.Equal(t, want.Port, got.Port)
		require.Equal(t, want.Domains, got.Domains)

		// The UID prefix is stable regardless of the (mutable) domain set.
		require.Equal(t, netip.AddrPortFrom(want.IP, want.Port).String(), got.UIDString())
		require.Equal(t, want.Ref("acme").Type, v1.KindNetDestination)
	}

	_, err := ParseNetDestPath("not-an-ip-port#x.com")
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed prefix is fault.Invalid")
}
