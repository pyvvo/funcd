package funcd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const libAdminToken = "lib-admin-0123456789abcdef"

// ADR-0171 Decision 7: WithCredentials refuses each malformed list, naming the index and never the token.
func TestWithCredentialsRejects(t *testing.T) {
	t.Parallel()
	const tok, other = "secret-token-aaaa", "secret-token-bbbb"
	dev := []string{"team-a"}
	for name, tc := range map[string]struct {
		creds []funcd.Credential
		index string
	}{
		"none":               {nil, "no credential"},
		"empty-token":        {[]funcd.Credential{{Token: "", Role: "admin"}}, "credentials[0]"},
		"space-in-token":     {[]funcd.Credential{{Token: "secret token", Role: "admin"}}, "credentials[0]"},
		"newline-in-token":   {[]funcd.Credential{{Token: "secret\ntoken", Role: "admin"}}, "credentials[0]"},
		"non-ascii-token":    {[]funcd.Credential{{Token: "secret-tökén", Role: "admin"}}, "credentials[0]"},
		"duplicate":          {[]funcd.Credential{{Token: tok, Role: "admin"}, {Token: other, Role: "viewer", Namespaces: dev}, {Token: tok, Role: "developer", Namespaces: dev}}, "credentials[0] and credentials[2]"},
		"dev-token":          {[]funcd.Credential{{Token: funcd.DevToken, Role: "admin"}}, "credentials[0]"},
		"unknown-role":       {[]funcd.Credential{{Token: tok, Role: "root"}}, "credentials[0]"},
		"admin-namespaces":   {[]funcd.Credential{{Token: tok, Role: "admin", Namespaces: dev}}, "credentials[0]"},
		"developer-unscoped": {[]funcd.Credential{{Token: other, Role: "admin"}, {Token: tok, Role: "developer"}}, "credentials[1]"},
		"viewer-unscoped":    {[]funcd.Credential{{Token: tok, Role: "viewer"}}, "credentials[0]"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := funcd.New(funcd.InMemory(), funcd.WithCredentials(tc.creds...))
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.Contains(t, err.Error(), tc.index)
			require.NotContains(t, err.Error(), "secret", "no token in the error")
			require.NotContains(t, err.Error(), funcd.DevToken, "no token in the error")
		})
	}
}

// scenario: library-credentials-replace-dev-token — WithCredentials after InMemory replaces the dev token: DevToken
// gets 401 and the admin token lists Namespaces.
func TestScenarioLibraryCredentialsReplaceDevToken(t *testing.T) {
	t.Parallel()
	p, err := funcd.New(funcd.InMemory(), funcd.WithCredentials(funcd.Credential{Token: libAdminToken, Role: "admin"}))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})

	devClient, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	_, err = devClient.List(ctx, v1.KindNamespace, "")
	require.Equal(t, fault.Unauthorized, fault.KindOf(err), "the dev token is gone: %v", err)

	admin, err := sdk.New("http://"+p.Addr(), sdk.WithToken(libAdminToken))
	require.NoError(t, err)
	_, err = admin.List(ctx, v1.KindNamespace, "")
	require.NoError(t, err, "the admin lists the cluster-scoped Namespace kind")
}

// productionDrivers is Production() with in-memory drivers and a data plane on loopback, for what New does with a
// credential and an address.
func productionDrivers(t *testing.T) []funcd.Option {
	t.Helper()
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	return []funcd.Option{
		funcd.Production(), funcd.WithStore(store.New(memory.New())), funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithRuntime(process.New(nil)), funcd.WithDataPlaneAddr("127.0.0.1:0"),
	}
}

// scenario: library-follows-the-rule, and edge-auth-follows-the-rule's library half (ADR-0314 Decisions 5 and 6)
func TestScenarioLibraryFollowsTheRule(t *testing.T) {
	t.Parallel()
	const own = "lib-own-0123456789abcdef"
	inMemory := func(opts ...funcd.Option) func(*testing.T) []funcd.Option {
		return func(*testing.T) []funcd.Option { return append([]funcd.Option{funcd.InMemory()}, opts...) }
	}
	inProduction := func(opts ...funcd.Option) func(*testing.T) []funcd.Option {
		return func(t *testing.T) []funcd.Option { return append(productionDrivers(t), opts...) }
	}
	for name, tc := range map[string]struct {
		opts      func(*testing.T) []funcd.Option
		key, addr string
	}{
		"production-dev-token":      {inProduction(funcd.WithDevAuth(funcd.DevToken, "default")), "WithListenAddr", "0.0.0.0:8080"},
		"every-address":             {inMemory(funcd.WithListenAddr("0.0.0.0:0")), "WithListenAddr", "0.0.0.0:0"},
		"every-address-no-host":     {inMemory(funcd.WithListenAddr(":0")), "WithListenAddr", ":0"},
		"a-lan-address":             {inMemory(funcd.WithListenAddr("192.0.2.1:0")), "WithListenAddr", "192.0.2.1:0"},
		"a-hostname":                {inMemory(funcd.WithListenAddr("example.test:0")), "WithListenAddr", "example.test:0"},
		"empty-resolves-to-default": {inMemory(funcd.WithListenAddr("")), "WithListenAddr", "0.0.0.0:8080"},
		"edge-every-address":        {inMemory(funcd.WithEdgeAuth(), funcd.WithDataPlaneAddr("0.0.0.0:0")), "WithDataPlaneAddr", "0.0.0.0:0"},
		"edge-empty-resolves":       {inMemory(funcd.WithEdgeAuth(), funcd.WithDataPlaneAddr("")), "WithDataPlaneAddr", "0.0.0.0:8081"},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			t.Parallel()
			_, err := funcd.New(tc.opts(t)...)
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, tc.key+` "`+tc.addr+`" is off loopback`)
			require.NotContains(t, err.Error(), funcd.DevToken, "no token in the error")
		})
	}
	admin := funcd.Credential{Token: libAdminToken, Role: "admin"}
	for name, opts := range map[string]func(*testing.T) []funcd.Option{
		"in-memory":                 inMemory(),
		"localhost":                 inMemory(funcd.WithListenAddr("localhost:0")),
		"edge-on-loopback":          inMemory(funcd.WithEdgeAuth()),
		"edge-off-every-address":    inMemory(funcd.WithDataPlaneAddr("0.0.0.0:0")),
		"own-token-every-address":   inMemory(funcd.WithDevAuth(own, "default"), funcd.WithListenAddr("0.0.0.0:0")),
		"credentials-every-address": inMemory(funcd.WithCredentials(admin), funcd.WithListenAddr("0.0.0.0:0")),
		"own-token-edge":            inMemory(funcd.WithDevAuth(own, "default"), funcd.WithEdgeAuth(), funcd.WithDataPlaneAddr("0.0.0.0:0")),
		"production-own-token":      inProduction(funcd.WithDevAuth(own, "default"), funcd.WithListenAddr("0.0.0.0:0")),
		"production-on-loopback":    inProduction(funcd.WithDevAuth(funcd.DevToken, "default"), funcd.WithListenAddr("127.0.0.1:0")),
	} {
		t.Run("starts/"+name, func(t *testing.T) {
			t.Parallel()
			p, err := funcd.New(opts(t)...)
			require.NoError(t, err)
			require.NoError(t, p.Shutdown(context.Background()))
		})
	}
}
