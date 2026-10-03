package network

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func goodPolicy() Policy {
	return Policy{
		WorkerSubnet:  netip.MustParsePrefix("10.63.0.0/16"),
		GatewayPort:   15001,
		InternalAllow: []netip.AddrPort{netip.MustParseAddrPort("10.63.0.1:9000")},
		DNSResolver:   netip.MustParseAddrPort("10.63.0.1:53"),
	}
}

// scenario: disabled-passthrough — New(false, nil) is a no-op Manager: Apply/Remove program nothing and never
// error, so worker egress stays open exactly as before.
func TestScenarioDisabledPassthrough(t *testing.T) {
	m := New(false, nil)
	require.IsType(t, noop{}, m, "disabled ⇒ the no-op Manager")
	require.NoError(t, m.Apply(context.Background(), goodPolicy()))
	require.NoError(t, m.Remove(context.Background()))
}

// scenario: teardown-clean — Remove is safe and idempotent: the no-op Manager returns nil whether or not
// anything was applied. The Linux driver's actual table deletion (delFuncdTables → Flush) needs a kernel
// and is exercised by the Lima e2e lane (deferred), alongside the other behavioral scenarios.
func TestScenarioTeardownClean(t *testing.T) {
	require.NoError(t, New(false, nil).Remove(context.Background()), "Remove on a disabled Manager is clean")
	require.NoError(t, New(false, nil).Remove(context.Background()), "Remove is idempotent (safe if never applied)")
}

// Policy.Validate gates a programmable policy: a non-zero subnet, a gateway-port target, and a resolver.
func TestPolicyValidate(t *testing.T) {
	require.NoError(t, goodPolicy().Validate())

	bad := goodPolicy()
	bad.GatewayPort = 0
	require.Error(t, bad.Validate(), "a zero gateway port is rejected — the redirect must have a target")

	bad = goodPolicy()
	bad.WorkerSubnet = netip.Prefix{}
	require.Error(t, bad.Validate(), "an invalid worker subnet is rejected")

	bad = goodPolicy()
	bad.DNSResolver = netip.AddrPort{}
	require.Error(t, bad.Validate(), "an invalid DNS resolver is rejected")
}
